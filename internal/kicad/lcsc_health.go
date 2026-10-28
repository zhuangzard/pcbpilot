package kicad

// lcsc_health.go — `kicad lcsc --health`: a read-only BOM risk report before a
// JLC order. Every check is pass / fail / cannot-check (internal/gate) with
// the evidence it rests on; a check that could not look is never a pass, and
// the overall verdict is gate.Aggregate over them.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

// HealthReportKind / HealthReportVersion identify the JSON written by --health.
const (
	HealthReportKind    = "pcbpilot.lcsc-health"
	HealthReportVersion = 1
)

// HealthCheck is one check of the report.
type HealthCheck struct {
	gate.Result
	Items []string `json:"items,omitempty"` // the evidence: failing / unverifiable entries
	Info  []string `json:"info,omitempty"`
}

// HealthLine is one BOM line: assembled parts sharing an LCSC number and a footprint.
type HealthLine struct {
	LCSC        string       `json:"lcsc"`
	Refs        []string     `json:"refs"`
	Value       string       `json:"value"`
	Footprint   string       `json:"footprint"`
	DesignMPN   string       `json:"designMpn,omitempty"`
	Placements  int          `json:"placements"`
	Library     string       `json:"library"`        // basic | extended | unknown
	Need        *int64       `json:"need,omitempty"` // for the whole LCSC number: placements × boards + loss allowance
	Stock       *int64       `json:"stock"`
	Package     string       `json:"packageCheck,omitempty"` // match | mismatch | unknown
	Catalog     *CatalogPart `json:"catalog,omitempty"`
	LookupError string       `json:"lookupError,omitempty"`
}

// HealthReport is the JSON document `kicad lcsc --health` prints.
type HealthReport struct {
	Kind      string        `json:"kind"`
	Version   int           `json:"version"`
	Verdict   gate.Status   `json:"verdict"`
	ExitCode  int           `json:"exitCode"`
	Boards    int           `json:"boards"`
	QueriedAt string        `json:"queriedAt"`
	Source    string        `json:"source"`
	Checks    []HealthCheck `json:"checks"`
	Lines     []HealthLine  `json:"lines"`
	Warnings  []string      `json:"warnings,omitempty"`
	Notes     []string      `json:"notes,omitempty"`
}

// HealthInput is everything RunHealth needs.
type HealthInput struct {
	Parts     []Part
	DesignMPN map[string]string // ref → MPN field on the board
	Boards    int
	Catalog   Catalog
	Now       time.Time
	Warnings  []string
}

// healthNotes are printed with every report: what the numbers do and do not mean.
var healthNotes = []string{
	"stock is what the JLC catalog reported at queriedAt, not a reservation; it can change before the order",
	"need = placements × boards + the catalog's loss allowance (lossNumber, taken as JLC's assembly attrition; verify at order time)",
	"footprint-package compares chip size (imperial) or family + pin count only; body width, pitch and pad geometry are not compared",
}

func refList(refs []string) string { return strings.Join(refs, ",") }

func descLine(l HealthLine) string {
	return fmt.Sprintf("%s [%s] %s %s", l.LCSC, refList(l.Refs), l.Value, footprintName(l.Footprint))
}

func descPart(p Part) string {
	return fmt.Sprintf("%s %s %s", p.Ref, p.Value, footprintName(p.Footprint))
}

// aggregateItems: fail if any item failed; else cannot-check if any could not
// be checked; else pass. Returns the status for a check built from per-item verdicts.
func aggregateItems(fails, cannots int) gate.Status {
	switch {
	case fails > 0:
		return gate.Fail
	case cannots > 0:
		return gate.CannotCheck
	}
	return gate.Pass
}

// RunHealth builds the report.
func RunHealth(in HealthInput) *HealthReport {
	if in.Boards < 1 {
		in.Boards = 1
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	rep := &HealthReport{Kind: HealthReportKind, Version: HealthReportVersion, Boards: in.Boards,
		QueriedAt: in.Now.UTC().Format(time.RFC3339), Source: "JLCPCB SMT catalog (live)",
		Warnings: in.Warnings, Notes: healthNotes}

	var assembled, missing, invalid []Part
	for _, p := range in.Parts {
		if !p.Assembled() {
			continue
		}
		assembled = append(assembled, p)
		switch {
		case p.LCSC == "":
			missing = append(missing, p)
		case !ValidLCSC(p.LCSC):
			invalid = append(invalid, p)
		}
	}

	// BOM lines among parts with a valid number.
	type lkey struct{ code, fp string }
	lines := map[lkey]*HealthLine{}
	var order []lkey
	placements := map[string]int{}
	var codes []string
	for _, p := range assembled {
		if !ValidLCSC(p.LCSC) {
			continue
		}
		k := lkey{p.LCSC, footprintName(p.Footprint)}
		l := lines[k]
		if l == nil {
			l = &HealthLine{LCSC: p.LCSC, Value: p.Value, Footprint: p.Footprint, Library: "unknown"}
			lines[k] = l
			order = append(order, k)
		}
		l.Refs = append(l.Refs, p.Ref)
		l.Placements++
		if l.DesignMPN == "" {
			l.DesignMPN = in.DesignMPN[p.Ref]
		}
		if placements[p.LCSC] == 0 {
			codes = append(codes, p.LCSC)
		}
		placements[p.LCSC]++
	}
	sort.Strings(codes)
	sort.SliceStable(order, func(a, b int) bool {
		la, lb := lines[order[a]], lines[order[b]]
		sortRefs(la.Refs)
		sortRefs(lb.Refs)
		return refLess(la.Refs[0], lb.Refs[0])
	})

	// Catalog.
	var lookups map[string]CatalogLookup
	var catErr string
	if len(codes) > 0 {
		var err error
		lookups, err = in.Catalog.Lookup(codes)
		if err != nil {
			catErr = err.Error()
		}
	}
	look := func(code string) CatalogLookup {
		if catErr != "" {
			return CatalogLookup{Err: catErr}
		}
		r, ok := lookups[code]
		if !ok {
			return CatalogLookup{Err: "the catalog helper returned no answer for " + code}
		}
		return r
	}
	need := map[string]int64{}
	for _, code := range codes {
		if r := look(code); r.Part != nil {
			var loss int64
			if r.Part.Loss != nil {
				loss = *r.Part.Loss
			}
			need[code] = int64(placements[code])*int64(in.Boards) + loss
		}
	}
	for _, k := range order {
		l := lines[k]
		sortRefs(l.Refs)
		r := look(l.LCSC)
		l.LookupError = r.Err
		if r.Part != nil {
			l.Catalog = r.Part
			l.Stock = r.Part.Stock
			if b, known := r.Part.IsBase(); known {
				l.Library = "extended"
				if b {
					l.Library = "basic"
				}
			}
			n := need[l.LCSC]
			l.Need = &n
			c, _ := PackageCompat(l.Footprint, r.Part.Package)
			l.Package = string(c)
		}
		rep.Lines = append(rep.Lines, *l)
	}

	rep.Checks = []HealthCheck{
		checkPresent(assembled, missing),
		checkValid(assembled, invalid),
		checkFound(codes, look),
		checkAvailability(codes, look),
		checkStock(codes, look, need, placements, in.Boards),
		checkExtended(codes, look),
		checkPackage(rep.Lines),
		checkDuplicateMPN(rep.Lines, codes, look),
	}
	var rs []gate.Result
	for _, c := range rep.Checks {
		rs = append(rs, c.Result)
	}
	rep.Verdict = gate.Aggregate(rs...)
	rep.ExitCode = rep.Verdict.ExitCode()
	return rep
}

func checkPresent(assembled, missing []Part) HealthCheck {
	c := HealthCheck{Result: gate.Result{Name: "lcsc-present"}}
	if len(assembled) == 0 {
		c.Status, c.Detail = gate.CannotCheck, "no assembled parts (all DNP / excluded, or an empty board): nothing to order"
		return c
	}
	for _, p := range missing {
		c.Items = append(c.Items, descPart(p))
	}
	c.Status = gate.FromBool(len(missing) == 0)
	c.Detail = fmt.Sprintf("%d of %d assembled part(s) have an LCSC number; %d missing", len(assembled)-len(missing), len(assembled), len(missing))
	if len(missing) > 0 {
		c.Detail += " (the catalog checks below cover only parts with a number)"
	}
	return c
}

func checkValid(assembled, invalid []Part) HealthCheck {
	c := HealthCheck{Result: gate.Result{Name: "lcsc-valid"}}
	if len(assembled) == 0 {
		c.Status, c.Detail = gate.CannotCheck, "no assembled parts"
		return c
	}
	for _, p := range invalid {
		c.Items = append(c.Items, fmt.Sprintf("%s has %q (want C + digits; one number per part)", descPart(p), p.LCSC))
	}
	c.Status = gate.FromBool(len(invalid) == 0)
	c.Detail = fmt.Sprintf("%d part(s) with a malformed LCSC value", len(invalid))
	return c
}

func checkFound(codes []string, look func(string) CatalogLookup) HealthCheck {
	c := HealthCheck{Result: gate.Result{Name: "catalog-found"}}
	if len(codes) == 0 {
		c.Status, c.Detail = gate.CannotCheck, "no valid LCSC numbers to look up"
		return c
	}
	fails, cannots := 0, 0
	for _, code := range codes {
		switch r := look(code); {
		case r.Err != "":
			cannots++
			c.Items = append(c.Items, code+": could not ask the catalog: "+r.Err)
		case r.Part == nil:
			fails++
			c.Items = append(c.Items, code+": not in the JLC catalog (wrong number?)")
		}
	}
	c.Status = aggregateItems(fails, cannots)
	c.Detail = fmt.Sprintf("%d number(s) looked up: %d not found, %d not checkable", len(codes), fails, cannots)
	return c
}

func checkAvailability(codes []string, look func(string) CatalogLookup) HealthCheck {
	c := HealthCheck{Result: gate.Result{Name: "availability"}}
	if len(codes) == 0 {
		c.Status, c.Detail = gate.CannotCheck, "no valid LCSC numbers"
		return c
	}
	fails, cannots, ok := 0, 0, 0
	for _, code := range codes {
		r := look(code)
		if r.Part == nil {
			cannots++ // found-ness is catalog-found's verdict
			continue
		}
		p := r.Part
		switch {
		case p.Buyable == nil:
			cannots++
			c.Items = append(c.Items, code+": the catalog does not say whether it can be bought")
		case !*p.Buyable || p.NoBuyReason != "" || p.MergedCode != "":
			fails++
			why := "not buyable (discontinued / not recommended for new designs)"
			if p.NoBuyReason != "" {
				why += ": " + p.NoBuyReason
			}
			if p.MergedCode != "" {
				why += "; merged into " + p.MergedCode
			}
			if p.AlternativesCode != "" {
				why += "; catalog suggests " + p.AlternativesCode
			}
			c.Items = append(c.Items, code+" "+p.MPN+": "+why)
		default:
			ok++
		}
	}
	c.Status = aggregateItems(fails, cannots)
	c.Detail = fmt.Sprintf("%d buyable, %d not buyable, %d not checkable", ok, fails, cannots)
	return c
}

func checkStock(codes []string, look func(string) CatalogLookup, need map[string]int64, placements map[string]int, boards int) HealthCheck {
	c := HealthCheck{Result: gate.Result{Name: "stock"}}
	if len(codes) == 0 {
		c.Status, c.Detail = gate.CannotCheck, "no valid LCSC numbers"
		return c
	}
	fails, cannots, ok := 0, 0, 0
	for _, code := range codes {
		r := look(code)
		if r.Part == nil {
			cannots++
			continue
		}
		if r.Part.Stock == nil {
			cannots++
			c.Items = append(c.Items, code+": the catalog gives no stock figure")
			continue
		}
		if *r.Part.Stock < need[code] {
			fails++
			c.Items = append(c.Items, fmt.Sprintf("%s %s: catalog stock %d < need %d (%d placement(s) × %d board(s) + loss allowance)",
				code, r.Part.MPN, *r.Part.Stock, need[code], placements[code], boards))
			continue
		}
		ok++
	}
	c.Status = aggregateItems(fails, cannots)
	c.Detail = fmt.Sprintf("%d number(s) covered by catalog stock for %d board(s), %d short, %d not checkable", ok, boards, fails, cannots)
	return c
}

func checkExtended(codes []string, look func(string) CatalogLookup) HealthCheck {
	c := HealthCheck{Result: gate.Result{Name: "extended-library", Advisory: true}}
	if len(codes) == 0 {
		c.Status, c.Detail = gate.CannotCheck, "no valid LCSC numbers"
		return c
	}
	ext, unknown := 0, 0
	for _, code := range codes {
		r := look(code)
		if r.Part == nil {
			unknown++
			continue
		}
		b, known := r.Part.IsBase()
		switch {
		case !known:
			unknown++
		case !b:
			ext++
			c.Items = append(c.Items, code+" "+r.Part.MPN+" ("+r.Part.Package+")")
		}
	}
	switch {
	case ext > 0:
		c.Status = gate.Warn
	case unknown > 0:
		c.Status = gate.CannotCheck
	default:
		c.Status = gate.Pass
	}
	c.Detail = fmt.Sprintf("%d extended-library part type(s) (JLC charges a setup fee for each), %d not checkable", ext, unknown)
	return c
}

func checkPackage(lines []HealthLine) HealthCheck {
	c := HealthCheck{Result: gate.Result{Name: "footprint-package"}}
	if len(lines) == 0 {
		c.Status, c.Detail = gate.CannotCheck, "no BOM lines with a valid LCSC number"
		return c
	}
	fails, cannots, ok := 0, 0, 0
	for _, l := range lines {
		if l.Catalog == nil {
			cannots++
			continue
		}
		cm, why := PackageCompat(l.Footprint, l.Catalog.Package)
		switch cm {
		case CompatMatch:
			ok++
		case CompatMismatch:
			fails++
			c.Items = append(c.Items, descLine(l)+": "+why)
		default:
			cannots++
			c.Items = append(c.Items, descLine(l)+": cannot verify — "+why)
		}
	}
	c.Status = aggregateItems(fails, cannots)
	c.Detail = fmt.Sprintf("%d of %d line(s) match, %d mismatch, %d not verifiable", ok, len(lines), fails, cannots)
	return c
}

func checkDuplicateMPN(lines []HealthLine, codes []string, look func(string) CatalogLookup) HealthCheck {
	c := HealthCheck{Result: gate.Result{Name: "duplicate-mpn", Advisory: true}}
	byMPN := map[string]map[string]bool{}
	shown := map[string]string{}
	withMPN := 0
	for _, l := range lines {
		mpn, src := l.DesignMPN, "design field"
		if mpn == "" && l.Catalog != nil {
			mpn, src = l.Catalog.MPN, "catalog"
		}
		if NormMPN(mpn) == "" {
			continue
		}
		withMPN++
		k := NormMPN(mpn)
		if byMPN[k] == nil {
			byMPN[k] = map[string]bool{}
			shown[k] = mpn + " (" + src + ")"
		}
		byMPN[k][l.LCSC] = true
	}
	if withMPN == 0 {
		c.Status, c.Detail = gate.CannotCheck, "no MPN known for any line (no MPN field on the board and no catalog record)"
		return c
	}
	var keys []string
	for k, set := range byMPN {
		if len(set) > 1 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		var cs []string
		for code := range byMPN[k] {
			cs = append(cs, code)
		}
		sort.Strings(cs)
		c.Items = append(c.Items, shown[k]+" is bought under "+strings.Join(cs, ", "))
	}
	unresolved := 0
	for _, l := range lines {
		if l.DesignMPN == "" && l.Catalog == nil {
			unresolved++
		}
	}
	switch {
	case len(keys) > 0:
		c.Status = gate.Warn
	case unresolved > 0:
		c.Status = gate.CannotCheck
	default:
		c.Status = gate.Pass
	}
	c.Detail = fmt.Sprintf("%d MPN(s) under more than one LCSC number; %d line(s) without a known MPN", len(keys), unresolved)
	return c
}
