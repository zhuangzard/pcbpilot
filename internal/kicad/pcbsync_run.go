package kicad

// pcbsync_run.go — the KiCad side of `kicad pcb-sync`: the netlist export
// with KiCad net identities, the bridge calls (sync-read, fp-info,
// sync-apply into a stage directory) and the schematic-parity DRC run.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

// ErrNetlistExport: kicad-cli could not export the schematic netlist.
const ErrNetlistExport = "E_NETLIST_EXPORT"

// ReadSyncNetlist parses a netlist export and gives every net its KiCad
// identity (pkg/netname: the escaped full name the board carries).
// kicadxml writes names unescaped, so they are resolved the way
// ParseSchNetlist does (sheet paths, then the label texts of the sheet
// files under rootDir as evidence; an undecidable name is an error), and
// mapped back by net code. kicadsexpr names are used as written.
func ReadSyncNetlist(data []byte, format, rootDir string) (*SyncNetlist, error) {
	nl, err := ParseSyncNetlist(data, format)
	if err != nil {
		return nil, err
	}
	if format == "" && bytes.HasPrefix(bytes.TrimSpace(data), []byte("(")) {
		format = NetlistKicadSexpr
	}
	if format == NetlistKicadSexpr {
		return nl, nil
	}
	ids, err := parseSchNetlist(data, rootDir)
	if err != nil {
		return nil, err
	}
	byCode := map[string]string{}
	for _, n := range ids.Nets {
		byCode[n.Code] = n.Name
	}
	for i := range nl.Nets {
		id, ok := byCode[nl.Nets[i].Code]
		if !ok {
			return nil, fmt.Errorf("net code %s (%q) has no identity", nl.Nets[i].Code, nl.Nets[i].Name)
		}
		nl.Nets[i].Name = id
	}
	return nl, nil
}

// ExportSyncNetlist runs `kicad-cli sch export netlist` on the root sheet
// (format kicadxml or kicadsexpr) and returns the raw export.
func ExportSyncNetlist(cli, sch, format string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "pcbpilot-sync-net-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "net."+format)
	var buf bytes.Buffer
	cmd, cancel := SchCLICommand(cli, "sch", "export", "netlist", "--format", format, "-o", out, sch)
	defer cancel()
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kicad-cli sch export netlist: %v: %s", err, strings.TrimSpace(buf.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("kicad-cli wrote no netlist: %s", strings.TrimSpace(buf.String()))
	}
	return data, nil
}

// SyncRead is the bridge's sync-read of a board.
func (t *Tools) SyncRead(pcb string) (*SyncInventory, error) {
	raw, err := t.RunBridge("sync-read", pcb)
	if err != nil {
		return nil, err
	}
	var inv SyncInventory
	if err := json.Unmarshal(raw, &inv); err != nil {
		return nil, fmt.Errorf("kicad bridge sync-read: %w", err)
	}
	return &inv, nil
}

// FPInfo loads library footprints through the bridge (fp-info), keyed
// "Lib:Name". A footprint that does not load is an E_FP_NOT_FOUND error;
// err is a bridge failure.
func (t *Tools) FPInfo(refs []FPRef) (map[string]SyncFPInfo, []SyncError, error) {
	dir, err := os.MkdirTemp("", "pcbpilot-fpinfo-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	var spec []map[string]string
	for _, r := range refs {
		spec = append(spec, map[string]string{"id": r.Lib + ":" + r.Name, "libDir": r.LibDir, "name": r.Name})
	}
	b, _ := json.Marshal(spec)
	path := filepath.Join(dir, "fp-info.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, nil, err
	}
	raw, err := t.RunBridge("fp-info", path)
	if err != nil {
		return nil, nil, err
	}
	var res struct {
		Footprints map[string]SyncFPInfo `json:"footprints"`
		Problems   []struct {
			ID     string `json:"id"`
			Detail string `json:"detail"`
		} `json:"problems"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, nil, fmt.Errorf("kicad bridge fp-info: %w", err)
	}
	var bad []SyncError
	for _, p := range res.Problems {
		bad = append(bad, SyncError{Code: ErrFPNotFound, Detail: p.ID + ": " + p.Detail,
			Fix: "check the footprint name and the library folder"})
	}
	return res.Footprints, bad, nil
}

// SyncApplyResult is the bridge's sync-apply answer.
type SyncApplyResult struct {
	Out          string         `json:"out"`
	Done         map[string]int `json:"done"`
	NetsCreated  []string       `json:"netsCreated"`
	Staged       []string       `json:"staged"`
	KiCadVersion string         `json:"kicadVersion"`
	Problems     []string       `json:"problems,omitempty"`
}

// NewStageDir creates the stage directory for a target file: a fresh
// ".pcbpilot-stage-<rand>" beside it, on the same volume, so the staged
// files can later be committed by rename.
func NewStageDir(target string) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(target), ".pcbpilot-stage-"+hex.EncodeToString(b[:]))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// SyncApply applies plan's mutating ops to pcb ("" = a new board) and saves
// the result as stage/<base>.kicad_pcb. The bridge refuses any output that
// is not directly inside stage; nothing outside it is written.
func (t *Tools) SyncApply(pcb string, plan *SyncPlan, stage, base string) (*SyncApplyResult, error) {
	b, err := json.Marshal(plan.ApplyDoc())
	if err != nil {
		return nil, err
	}
	planPath := filepath.Join(stage, ".sync-plan.json")
	if err := os.WriteFile(planPath, b, 0o600); err != nil {
		return nil, err
	}
	defer os.Remove(planPath)
	src := pcb
	if src == "" {
		src = "-"
	}
	raw, err := t.RunBridgeEnv([]string{"PCBPILOT_STAGE_DIR=" + stage}, "sync-apply", src, planPath, filepath.Join(stage, base+".kicad_pcb"))
	var res SyncApplyResult
	if raw != nil {
		_ = json.Unmarshal(raw, &res)
	}
	if err != nil {
		return &res, err
	}
	return &res, nil
}

// ParityResult is the schematic-parity gate: kicad-cli pcb drc
// --schematic-parity on a copy of the board beside a copy of the sheets.
type ParityResult struct {
	Status gate.Status `json:"status"`
	Detail string      `json:"detail"`
	Items  []string    `json:"items,omitempty"`
	Info   []string    `json:"info,omitempty"`
}

// RunParity copies pcb (+ pro when present) and every .kicad_sch under the
// root sheet's directory to a temp directory, the board renamed to the root
// sheet's basename (kicad-cli finds the schematic by that name), and runs
// the parity DRC there with the isolated config (parity needs no library).
// Every schematic_parity item fails the gate unless expected(v) says it is
// one the plan chose to keep (a flagged orphan). Anything that keeps the
// check from running — no kicad-cli, a crash, a report that does not parse
// or lacks the schematic_parity section — is CannotCheck.
func (t *Tools) RunParity(pcb, pro, schRoot string, expected func(Violation) bool) ParityResult {
	cc := func(f string, a ...any) ParityResult {
		return ParityResult{Status: gate.CannotCheck, Detail: fmt.Sprintf(f, a...)}
	}
	if t == nil || t.CLI == "" {
		return cc("kicad-cli not found")
	}
	if _, err := os.Stat(t.CLI); err != nil {
		return cc("kicad-cli not found at %s", t.CLI)
	}
	dir, err := os.MkdirTemp("", "pcbpilot-parity-")
	if err != nil {
		return cc("temp dir: %v", err)
	}
	defer os.RemoveAll(dir)
	base := strings.TrimSuffix(filepath.Base(schRoot), filepath.Ext(schRoot))
	if err := copyPlain(pcb, filepath.Join(dir, base+".kicad_pcb")); err != nil {
		return cc("copy board: %v", err)
	}
	if pro != "" {
		if _, err := os.Stat(pro); err == nil {
			if err := copyPlain(pro, filepath.Join(dir, base+".kicad_pro")); err != nil {
				return cc("copy project: %v", err)
			}
		}
	}
	root := filepath.Dir(schRoot)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || strings.HasSuffix(d.Name(), ".pretty")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".kicad_sch") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		return copyPlain(p, filepath.Join(dir, rel))
	})
	if err != nil {
		return cc("copy sheets: %v", err)
	}
	out := filepath.Join(dir, "parity.json")
	var buf bytes.Buffer
	cmd, cancel := SchCLICommand(t.CLI, "pcb", "drc", "--schematic-parity", "--format", "json", "--severity-all", "-o", out, filepath.Join(dir, base+".kicad_pcb"))
	defer cancel()
	cmd.Stdout, cmd.Stderr = &buf, &buf
	runErr := cmd.Run()
	data, rerr := os.ReadFile(out)
	if runErr != nil || rerr != nil {
		why := runErr
		if why == nil {
			why = rerr
		}
		return cc("kicad-cli pcb drc --schematic-parity: %v: %s", why, strings.TrimSpace(buf.String()))
	}
	rep, err := ParseDRCWith(data, DRCParseOptions{SchematicParity: true})
	if err != nil {
		return cc("%v", err)
	}
	res := ParityResult{Status: gate.Pass}
	all := append(append([]Violation{}, rep.Violations...), rep.ExcludedItems...)
	for _, v := range all {
		if v.Section != "schematic_parity" {
			continue
		}
		line := v.Rule + ": " + v.Message
		if v.Excluded {
			line += " (excluded in the project; pcb-sync still requires parity)"
		}
		if expected != nil && expected(v) {
			res.Info = append(res.Info, line)
			continue
		}
		res.Items = append(res.Items, line)
	}
	if len(res.Items) > 0 {
		res.Status = gate.Fail
	}
	res.Detail = fmt.Sprintf("kicad-cli %s schematic parity: %d issue(s), %d expected (flagged orphans)", rep.KiCadVersion, len(res.Items), len(res.Info))
	return res
}

func copyPlain(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

// IsSyncRefusal returns the refusal inside err.
func IsSyncRefusal(err error) (*SyncRefusal, bool) {
	var r *SyncRefusal
	ok := errors.As(err, &r)
	return r, ok
}
