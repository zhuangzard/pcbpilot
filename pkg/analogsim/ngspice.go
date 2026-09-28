package analogsim

import (
	"github.com/zhuangzard/pcbpilot/pkg/simtools"

	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FindNgspice returns the ngspice binary (explicit path, $PCBPILOT_NGSPICE,
// PATH, then the common Homebrew/MacPorts/Linux locations) and its version.
func FindNgspice(explicit string) (string, string) {
	var cands []string
	if explicit != "" {
		cands = append(cands, explicit)
	}
	if env := os.Getenv("PCBPILOT_NGSPICE"); env != "" {
		cands = append(cands, env)
	}
	if p, err := exec.LookPath("ngspice"); err == nil {
		cands = append(cands, p)
	}
	// The shared locator (pcbpilot sim tools) also knows the Windows
	// install dirs that the installer does not put on PATH.
	if p, err := simtools.Find("ngspice"); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands, "/opt/homebrew/bin/ngspice", "/usr/local/bin/ngspice", "/opt/local/bin/ngspice", "/usr/bin/ngspice")
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, ngspiceVersion(c)
		}
	}
	return "", ""
}

var reNgVer = regexp.MustCompile(`ngspice-([0-9][0-9.]*)`)

func ngspiceVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, "-v").CombinedOutput()
	if m := reNgVer.FindStringSubmatch(string(out)); m != nil {
		return "ngspice-" + m[1]
	}
	return "unknown"
}

// table is one wrdata file.
type table struct {
	header []string
	rows   [][]float64
}

// col returns column i of the table.
func (t *table) col(i int) []float64 {
	out := make([]float64, 0, len(t.rows))
	for _, r := range t.rows {
		if i < len(r) {
			out = append(out, r[i])
		}
	}
	return out
}

func readTable(path string) (*table, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	t := &table{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		if _, err := strconv.ParseFloat(fields[0], 64); err != nil {
			t.header = fields
			continue
		}
		row := make([]float64, len(fields))
		for i, s := range fields {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				v = 0
			}
			row[i] = v
		}
		t.rows = append(t.rows, row)
	}
	return t, sc.Err()
}

// simResult is one ngspice run.
type simResult struct {
	netlist string
	op      map[string]float64   // last .op print block
	ops     []map[string]float64 // every .op print block in order (MC)
	data    map[string]*table
	log     string
	errs    []string
}

var (
	rePrint = regexp.MustCompile(`^\s*([a-zA-Z][a-zA-Z0-9_.#\[\]()-]*)\s*=\s*([-+0-9.eE]+)\s*$`)
	reNgErr = regexp.MustCompile(`(?i)(^error|singular matrix|timestep too small|no convergence|fatal|could not find|unknown subckt|too few nodes)`)
)

// runner owns ngspice runs of one Run.
type runner struct {
	c         *circuit
	lib       *Library
	stock     *StockLib
	spec      *Spec
	opt       Options
	bin       string
	version   string
	dir       string // work dir (netlists + data)
	keep      bool   // keep the work dir (Options.WorkDir set)
	libPath   string
	runs      int
	railsFrom string
	artifacts []Artifact
}

// run writes the netlist, runs ngspice -b in the work dir and parses the outputs.
func (r *runner) run(blk *Block, s spiceSetup, tag string) (*simResult, error) {
	if r.bin == "" {
		return nil, fmt.Errorf("%s", MissingNgspiceNote)
	}
	s.libPath = r.libPath
	nl := r.netlist(blk, s)
	base := fmt.Sprintf("%s-%s", blk.ID, s.mode)
	if tag != "" {
		base += "-" + tag
	}
	// Data files carry the tag too, so before/after runs do not overwrite.
	if tag != "" {
		for role, f := range nl.files {
			nf := strings.TrimSuffix(f, ".txt") + "-" + tag + ".txt"
			nl.text = strings.ReplaceAll(nl.text, " "+f, " "+nf)
			nl.files[role] = nf
		}
	}
	cir := filepath.Join(r.dir, base+".cir")
	if err := os.WriteFile(cir, []byte(nl.text), 0o644); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, r.bin, "-b", filepath.Base(cir))
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	r.runs++
	res := &simResult{netlist: nl.text, data: map[string]*table{}, log: string(out)}
	logPath := filepath.Join(r.dir, base+".log")
	_ = os.WriteFile(logPath, []byte(stableLog(string(out))), 0o644)
	if r.keep && s.mode != modeMC {
		r.artifacts = append(r.artifacts, Artifact{Block: blk.ID, Kind: "netlist", Path: base + ".cir"}, Artifact{Block: blk.ID, Kind: "log", Path: base + ".log"})
	} else if r.keep {
		r.artifacts = append(r.artifacts, Artifact{Block: blk.ID, Kind: "netlist", Path: base + ".cir"})
	}
	if tag == "" && r.keep && (blk.Netlist == "" || s.mode == modeMain) && s.mode != modeMC && s.mode != modeDC && s.mode != modeLoop && s.mode != modeSample {
		blk.Netlist = base + ".cir"
	}
	cur := map[string]float64{}
	flush := func() {
		if len(cur) > 0 {
			res.ops = append(res.ops, cur)
			res.op = cur
			cur = map[string]float64{}
		}
	}
	prevWasPrint := false
	for _, line := range strings.Split(string(out), "\n") {
		if m := rePrint.FindStringSubmatch(line); m != nil {
			if v, e := strconv.ParseFloat(m[2], 64); e == nil {
				key := strings.ToLower(m[1])
				if _, dup := cur[key]; dup {
					flush()
				}
				cur[key] = v
				prevWasPrint = true
				continue
			}
		}
		if prevWasPrint && strings.TrimSpace(line) != "" {
			flush()
			prevWasPrint = false
		}
		if reNgErr.MatchString(strings.TrimSpace(line)) {
			res.errs = append(res.errs, strings.TrimSpace(line))
		}
	}
	flush()
	roles := make([]string, 0, len(nl.files))
	for role := range nl.files {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		f := nl.files[role]
		p := filepath.Join(r.dir, f)
		t, e := readTable(p)
		if e != nil {
			continue
		}
		res.data[role] = t
		if s.mode == modeMC {
			_ = os.Remove(p)
		} else if r.keep {
			kind := "data"
			if len(t.rows) > maxKeptRows {
				logX := s.mode == modeMain && role == "ac" || s.mode == modeLoop || s.mode == modeXtal
				_ = writeDecimated(p, t, logX)
				kind = "data (decimated)"
			}
			r.artifacts = append(r.artifacts, Artifact{Block: blk.ID, Kind: kind, Path: f})
		}
	}
	if err != nil && len(res.data) == 0 && len(res.ops) == 0 {
		return res, fmt.Errorf("ngspice failed (%v): %s", err, firstLines(string(out), 6))
	}
	if len(res.data) == 0 && len(res.ops) == 0 {
		return res, fmt.Errorf("ngspice produced no data: %s", firstLines(strings.Join(res.errs, "\n")+"\n"+string(out), 6))
	}
	return res, nil
}

func firstLines(s string, n int) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, strings.TrimSpace(l))
		if len(out) >= n {
			break
		}
	}
	return strings.Join(out, " | ")
}

// maxKeptRows bounds the wrdata files kept in the work dir / report package;
// the metrics are computed from the full-resolution data before decimation.
const maxKeptRows = 500

// writeDecimated rewrites a kept wrdata file with ≤ maxKeptRows rows (the
// netlist next to it regenerates the full resolution).
func writeDecimated(path string, t *table, logX bool) error {
	x := t.col(0)
	cols := len(t.rows[0])
	ys := make([][]float64, cols-1)
	for i := 1; i < cols; i++ {
		ys[i-1] = t.col(i)
	}
	xd, yd := decimate(x, ys, maxKeptRows, logX)
	var b strings.Builder
	if len(t.header) > 0 {
		b.WriteString(" " + strings.Join(t.header, " ") + "\n")
	}
	for k := range xd {
		fmt.Fprintf(&b, " %.9e", xd[k])
		for i := range yd {
			fmt.Fprintf(&b, " %.9e", yd[i][k])
		}
		b.WriteString("\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// stableLog drops ngspice's progress lines ("Reference value : …", timing
// dependent) so kept logs are reproducible.
func stableLog(s string) string {
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "Reference value") || strings.Contains(l, "\r") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}
