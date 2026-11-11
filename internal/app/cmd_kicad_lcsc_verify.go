package app

// cmd_kicad_lcsc_verify.go — `pcbpilot kicad lcsc --verify`: every assembled
// part's LCSC number is looked up exactly (its EasyEDA library record, the one
// `--import` uses; a catalog search returns near matches only) and compared
// with the part: the number must exist, a chip size must match the record's
// package, and an R/C/L value must match the record's value. A number can be
// valid in form and still order the wrong part; what cannot be established is
// an error, never a pass.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// lcscVerifyDeadline bounds all lookups of one run.
const lcscVerifyDeadline = 10 * time.Minute

// lcscFinding is one verify result. Level "error" fails the run.
type lcscFinding struct {
	Level     string `json:"level"` // error | warning
	Ref       string `json:"ref"`
	LCSC      string `json:"lcsc"`
	Value     string `json:"value"`
	Footprint string `json:"footprint"`
	What      string `json:"what"`
	Catalog   string `json:"catalog,omitempty"` // the record's package, value or MPN
}

var (
	// A size token: a size code, optionally "Metric" (KiCad's
	// R_0402_1005Metric names both); in a package name also R0402 / C0603 /
	// L0805 / LED0603 / D0603 / FB0402.
	reSizeToken    = regexp.MustCompile(`^([0-9]{4,5})(Metric|metric)?$`)
	reSizePkgToken = regexp.MustCompile(`^(?:R|C|L|D|F|FB|LED)([0-9]{4,5})$`)
	reSpacedUnit   = regexp.MustCompile(`([0-9])\s+([pnuµμmkKMGRrΩ])`)
	reOhmMark      = regexp.MustCompile(`(?i)(Ω|ohm|^[0-9.]+\s*[kKM]?R|^[0-9]+[Rr][0-9]+)`)
	reSizeSplit    = regexp.MustCompile(`[_\-\s()/,;:+\[\]]+`)
	reURLNumber    = regexp.MustCompile(`(?i)_C[0-9]+\.html$`)
	imperial       = map[string]bool{"01005": true, "0201": true, "0402": true, "0603": true, "0805": true, "1008": true, "1206": true, "1210": true, "1806": true, "1812": true, "2010": true, "2512": true}
	metricToImp    = map[string]string{"0402": "01005", "0603": "0201", "1005": "0402", "1608": "0603", "2012": "0805", "2520": "1008", "3216": "1206", "3225": "1210", "4516": "1806", "4532": "1812", "5025": "2010", "6332": "2512"}
	// A value with its unit in a record: "10kΩ", "100nF (104)", "4.7uH".
	reRecVal = map[string]*regexp.Regexp{
		"R": regexp.MustCompile(`(?:^|[^0-9A-Za-z.])([0-9]+(?:\.[0-9]+)?\s*[pnuµμmkKMG]?)\s*(?:Ω|ohm)`),
		"C": regexp.MustCompile(`(?:^|[^0-9A-Za-z.])([0-9]+(?:\.[0-9]+)?\s*[pnuµμm]?)F(?:[^A-Za-z]|$)`),
		"L": regexp.MustCompile(`(?:^|[^0-9A-Za-z.])([0-9]+(?:\.[0-9]+)?\s*[pnuµμm]?)H(?:[^A-Za-z]|$)`),
	}
	reRefKind = regexp.MustCompile(`^([A-Z]+)[0-9]`)
	reRKM     = regexp.MustCompile(`^([0-9]+)([RrpnumkKMG])([0-9]+)$`)
)

// lcscChipSize is the imperial size code in a footprint name, record
// description or URL ("" when there is none). Only whole tokens count, so model
// numbers, pitches, C-numbers and the URL's own number never read as a size.
func lcscChipSize(s string) string { return chipSize(s, false) }

// lcscPackageSize is lcscChipSize for a package name, where R0402 / C0603 and
// the like are sizes too.
func lcscPackageSize(s string) string { return chipSize(s, true) }

func chipSize(s string, pkg bool) string {
	s = reURLNumber.ReplaceAllString(s, "")
	for _, t := range reSizeSplit.Split(s, -1) {
		m := reSizeToken.FindStringSubmatch(t)
		if m == nil && pkg {
			if n := reSizePkgToken.FindStringSubmatch(t); n != nil {
				m = []string{t, n[1], ""}
			}
		}
		if m == nil {
			continue
		}
		if m[2] != "" {
			if imp, ok := metricToImp[m[1]]; ok {
				return imp
			}
			continue
		}
		if imperial[m[1]] {
			return m[1]
		}
	}
	return ""
}

// lcscValue parses a schematic or record value ("4k7", "100nF", "10 kΩ",
// "10K 1%", "0R", "4r7", "600R@100MHz"): the first word is the value.
func lcscValue(s string) (float64, bool) {
	s = reSpacedUnit.ReplaceAllString(strings.TrimSpace(s), "$1$2")
	if i := strings.IndexAny(s, " \t/@,;"); i > 0 {
		s = s[:i]
	}
	for _, u := range []string{"Ω", "ohm", "Ohm", "OHM"} {
		s = strings.ReplaceAll(s, u, "")
	}
	s = strings.ReplaceAll(s, "µ", "u")
	s = strings.ReplaceAll(s, "μ", "u")
	s = strings.TrimRight(s, "FHfh")
	if m := reRKM.FindStringSubmatch(s); m != nil { // 4k7, 2R2, 4n7
		mult := m[2]
		if mult == "R" {
			mult = ""
		}
		s = m[1] + "." + m[3] + mult
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "R"), "r")
	if s == "" {
		return 0, false
	}
	return powersim.ParseValue(s)
}

// lcscRecordValue is the value in unit (R ohms, C farads, L henries) a record
// states, from its Value field, else its description.
func lcscRecordValue(unit string, h kicad.EasyEDAPart) (float64, bool) {
	for _, s := range []string{h.Value, h.Description} {
		if m := reRecVal[unit].FindStringSubmatch(s); m != nil {
			if v, ok := lcscValue(m[1]); ok {
				return v, true
			}
		}
	}
	return 0, false
}

// lcscUnit is the unit a part's value is compared in: its kind's, except an
// L whose value is in ohms (a ferrite bead, "600R@100MHz") compares ohms.
func lcscUnit(kind, value string) string {
	if kind == "L" && reOhmMark.MatchString(strings.TrimSpace(value)) {
		return "R"
	}
	return kind
}

// lcscVerify compares each assembled part with the record of its LCSC number.
// catalog holds the records found; a number absent from it has none.
func lcscVerify(parts []kicad.Part, catalog map[string]kicad.EasyEDAPart) []lcscFinding {
	var out []lcscFinding
	for _, p := range parts {
		if !p.Assembled() || !kicad.ValidLCSC(p.LCSC) {
			continue
		}
		f := lcscFinding{Ref: p.Ref, LCSC: p.LCSC, Value: p.Value, Footprint: p.Footprint}
		add := func(level, what, cat string) {
			g := f
			g.Level, g.What, g.Catalog = level, what, cat
			out = append(out, g)
		}
		h, ok := catalog[p.LCSC]
		if !ok {
			add("error", "LCSC number has no record (not a real part number)", "")
			continue
		}
		kind := ""
		if m := reRefKind.FindStringSubmatch(strings.ToUpper(p.Ref)); m != nil {
			kind = map[string]string{"R": "R", "C": "C", "L": "L"}[m[1]]
		}
		fpSize := lcscPackageSize(footprintName(p.Footprint))
		catSize := lcscPackageSize(h.Package)
		for _, s := range []string{h.Description, h.URL} {
			if catSize != "" {
				break
			}
			catSize = lcscChipSize(s)
		}
		switch {
		case fpSize != "" && catSize != "" && fpSize != catSize:
			add("error", fmt.Sprintf("footprint size %s, record package %s", fpSize, catSize), h.Package)
		case fpSize != "" && catSize == "" && kind != "":
			add("error", fmt.Sprintf("footprint size %s, the record names no chip size: check the part by hand", fpSize), h.Package)
		case fpSize != "" && catSize == "":
			add("warning", fmt.Sprintf("footprint size %s, the record names no chip size", fpSize), h.Package)
		}
		if kind != "" {
			unit := lcscUnit(kind, p.Value)
			want, okW := lcscValue(p.Value)
			got, okG := lcscRecordValue(unit, h)
			rec := strings.TrimSpace(h.Value + " " + h.Description)
			switch {
			case !okW:
				add("error", fmt.Sprintf("value %q cannot be read: check the part by hand", p.Value), rec)
			case !okG:
				add("error", fmt.Sprintf("the record states no %s value (a different kind of part?)", map[string]string{"R": "resistance", "C": "capacitance", "L": "inductance"}[unit]), rec)
			case want == 0 && got != 0, want != 0 && math.Abs(got-want)/want > 0.005:
				add("error", fmt.Sprintf("value %s, record %s", p.Value, lcscFormat(got, unit)), rec)
			}
		}
		if h.Stock <= 0 {
			add("warning", "LCSC stock 0", h.MPN)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Level != out[j].Level {
			return out[i].Level == "error"
		}
		return out[i].Ref < out[j].Ref
	})
	return out
}

// footprintName drops a "Library:" prefix.
func footprintName(fp string) string {
	if i := strings.LastIndex(fp, ":"); i >= 0 {
		return fp[i+1:]
	}
	return fp
}

func lcscFormat(v float64, kind string) string {
	unit := map[string]string{"R": "Ω", "C": "F", "L": "H"}[kind]
	for _, s := range []struct {
		f float64
		p string
	}{{1e6, "M"}, {1e3, "k"}, {1, ""}, {1e-3, "m"}, {1e-6, "u"}, {1e-9, "n"}, {1e-12, "p"}} {
		if math.Abs(v) >= s.f*0.999 {
			return fmt.Sprintf("%g%s%s", math.Round(v/s.f*1000)/1000, s.p, unit)
		}
	}
	return fmt.Sprintf("%g%s", v, unit)
}

// lcscLookup fetches the record of each number, two at a time (the endpoint
// throttles bursts). A number with no record is absent from the map. The first
// error cancels the rest; nothing partial is returned with it.
func lcscLookup(ctx context.Context, numbers []string, lookup func(ctx context.Context, n string) (kicad.EasyEDAPart, bool, error)) (map[string]kicad.EasyEDAPart, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := map[string]kicad.EasyEDAPart{}
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, n := range numbers {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			defer func() { <-sem }()
			part, found, err := lookup(ctx, n)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			if found {
				out[n] = part
			}
		}(n)
	}
	wg.Wait()
	if firstErr == nil && ctx.Err() != nil {
		firstErr = ctx.Err()
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func runLcscVerify(stdout, stderr io.Writer, pcb, sch string, asJSON bool) error {
	if pcb == "" || sch == "" {
		return fmt.Errorf("--verify needs --pcb and --sch (placed parts from the board, values from the schematic)")
	}
	bd, err := kicad.ReadBoard(pcb)
	if err != nil {
		return err
	}
	cli, err := kicad.ResolveFabCLI()
	if err != nil {
		return err
	}
	rows, err := kicad.ReadSchematicParts(cli, sch)
	if err != nil {
		return err
	}
	parts, warns := kicad.MergeParts(bd, rows, true)
	missing, invalid := kicad.CheckLCSC(parts)
	seen := map[string]bool{}
	var numbers []string
	for _, p := range parts {
		if p.Assembled() && kicad.ValidLCSC(p.LCSC) && !seen[p.LCSC] {
			seen[p.LCSC] = true
			numbers = append(numbers, p.LCSC)
		}
	}
	sort.Strings(numbers)
	ctx, cancel := context.WithTimeout(context.Background(), lcscVerifyDeadline)
	defer cancel()
	catalog, err := lcscLookup(ctx, numbers, kicad.LookupEasyEDAPart)
	if err != nil {
		return fmt.Errorf("LCSC lookup failed (live, needs the network; nothing was judged): %w", err)
	}
	findings := lcscVerify(parts, catalog)
	errs := 0
	for _, f := range findings {
		if f.Level == "error" {
			errs++
		}
	}
	if asJSON {
		b, _ := json.MarshalIndent(map[string]any{"checked": len(numbers), "findings": findings, "missing": missing, "invalid": invalid, "warnings": warns}, "", "  ")
		fmt.Fprintln(stdout, string(b))
	} else {
		for _, w := range warns {
			fmt.Fprintln(stderr, "warning:", w)
		}
		fmt.Fprintf(stdout, "%s, %d LCSC number(s) looked up, %d mismatch(es), %d missing, %d invalid\n", lcscAssemblySummary(parts), len(numbers), errs, len(missing), len(invalid))
		printLCSCProblems(stdout, missing, invalid)
		for _, f := range findings {
			fmt.Fprintf(stdout, "  %-7s %-8s %-9s %-14s %s", f.Level, f.Ref, f.LCSC, lcscTrunc(f.Value, 14), f.What)
			if f.Catalog != "" {
				fmt.Fprintf(stdout, "  [%s]", lcscTrunc(f.Catalog, 60))
			}
			fmt.Fprintln(stdout)
		}
	}
	if errs+len(missing)+len(invalid) > 0 {
		return errActionFailed
	}
	return nil
}
