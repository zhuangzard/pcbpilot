package app

// cmd_kicad_lcsc_verify.go — `pcbpilot kicad lcsc --verify`: every assembled
// part's LCSC number is looked up exactly (its EasyEDA library record, the one
// `--import` uses; a catalog search returns near matches only) and compared
// with the part: the number must exist, a chip passive's size code
// must match the catalog package, and an R/C/L value must match the catalog
// description. A number can be valid in form and still order the wrong part.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

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
	// Chip size codes, imperial (KiCad names R_0402_1005Metric: imperial first).
	reChipSize = regexp.MustCompile(`(?:^|[^0-9])(01005|0201|0402|0603|0805|1008|1206|1210|1806|1812|2010|2512)(?:[^0-9]|$)`)
	reDescRV   = regexp.MustCompile(`([0-9.]+\s*[pnuµμmkKMG]?)\s*Ω`)
	reDescCV   = regexp.MustCompile(`(?:^|[\s,;])([0-9.]+\s*[pnuµμm]?)F(?:[\s,;]|$)`)
	reDescLV   = regexp.MustCompile(`(?:^|[\s,;])([0-9.]+\s*[pnuµμm]?)H(?:[\s,;]|$)`)
	reRefKind  = regexp.MustCompile(`^([A-Z]+)[0-9]`)
)

// lcscChipSize is the imperial size code in a footprint name or catalog
// package ("" when there is none).
func lcscChipSize(s string) string {
	if m := reChipSize.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

// lcscValue parses a schematic value or catalog value ("4k7", "100nF", "10kΩ").
func lcscValue(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	for _, u := range []string{"Ω", "ohm", "Ohm", "OHM"} {
		s = strings.ReplaceAll(s, u, "")
	}
	s = strings.ReplaceAll(s, "µ", "u")
	s = strings.ReplaceAll(s, "μ", "u")
	s = strings.TrimRight(s, "FHfh")
	// 4k7 / 2R2 / 4n7 notation.
	if m := regexp.MustCompile(`^([0-9]+)([RpnumkKMG])([0-9]+)$`).FindStringSubmatch(s); m != nil {
		mult := m[2]
		if mult == "R" {
			mult = ""
		}
		s = m[1] + "." + m[3] + mult
	}
	if strings.HasSuffix(s, "R") {
		s = strings.TrimSuffix(s, "R")
	}
	if s == "" {
		return 0, false
	}
	return powersim.ParseValue(s)
}

// lcscDescValue is the R/C/L value in a catalog description.
func lcscDescValue(kind, desc string) (float64, bool) {
	re := map[string]*regexp.Regexp{"R": reDescRV, "C": reDescCV, "L": reDescLV}[kind]
	if re == nil {
		return 0, false
	}
	m := re.FindStringSubmatch(desc)
	if m == nil {
		return 0, false
	}
	return lcscValue(m[1])
}

// lcscVerify compares each assembled part with the catalog row of its LCSC
// number. catalog holds the rows found; a number absent from it was not found.
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
			add("error", "LCSC number not found in the JLC catalog", "")
			continue
		}
		fpSize := lcscChipSize(footprintName(p.Footprint))
		catSize := ""
		for _, s := range []string{h.Package, h.Description, h.URL} {
			if catSize = lcscChipSize(s); catSize != "" {
				break
			}
		}
		if fpSize != "" && catSize != "" && fpSize != catSize {
			add("error", fmt.Sprintf("footprint size %s, catalog package %s", fpSize, catSize), h.Package)
		}
		kind := ""
		if m := reRefKind.FindStringSubmatch(strings.ToUpper(p.Ref)); m != nil {
			kind = map[string]string{"R": "R", "C": "C", "L": "L"}[m[1]]
		}
		if kind != "" {
			want, okW := lcscValue(p.Value)
			got, okG := lcscDescValue(kind, h.Value)
			if !okG {
				got, okG = lcscDescValue(kind, h.Description)
			}
			if okW && okG && want > 0 && math.Abs(got-want)/want > 0.005 {
				add("error", fmt.Sprintf("value %s, catalog %s", p.Value, powersimFormat(got, kind)), strings.TrimSpace(h.Value+" "+h.Description))
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

func powersimFormat(v float64, kind string) string {
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
// throttles bursts). A number with no record is absent from the map.
func lcscLookup(numbers []string, lookup func(n string) (kicad.EasyEDAPart, bool, error)) (map[string]kicad.EasyEDAPart, error) {
	out := map[string]kicad.EasyEDAPart{}
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, n := range numbers {
		wg.Add(1)
		sem <- struct{}{}
		go func(n string) {
			defer wg.Done()
			defer func() { <-sem }()
			part, found, err := lookup(n)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			if found {
				out[n] = part
			}
		}(n)
	}
	wg.Wait()
	return out, firstErr
}

func runLcscVerify(stdout, stderr io.Writer, pcb, sch string, asJSON bool) error {
	if pcb == "" {
		return fmt.Errorf("--verify needs --pcb (placed parts come from the board)")
	}
	bd, err := kicad.ReadBoard(pcb)
	if err != nil {
		return err
	}
	var rows []kicad.SchPart
	if sch != "" {
		cli, err := kicad.ResolveFabCLI()
		if err != nil {
			return err
		}
		if rows, err = kicad.ReadSchematicParts(cli, sch); err != nil {
			return err
		}
	}
	parts, warns := kicad.MergeParts(bd, rows, sch != "")
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
	catalog, err := lcscLookup(numbers, kicad.LookupEasyEDAPart)
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
