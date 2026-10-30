package kicad

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

func cap0402(code, mpn, farads, volt, tol, tc string, base bool, stock int64) *CatalogPart {
	return &CatalogPart{LCSC: code, MPN: mpn, Package: "0402", Category: "Multilayer Ceramic Capacitors MLCC - SMD/SMT",
		Base: bp(base), Stock: i64(stock), Buyable: bp(true), Loss: i64(0),
		Attrs: map[string]string{"Capacitance": farads, "Voltage Rating": volt, "Tolerance": tol, "Temperature Coefficient": tc}}
}

func altLine(t *testing.T, rep *AltReport, ref string) AltLine {
	t.Helper()
	for _, l := range rep.Lines {
		for _, r := range l.Refs {
			if r == ref {
				return l
			}
		}
	}
	t.Fatalf("no line for %s", ref)
	return AltLine{}
}

func TestAlternativesReplacesOutOfStockResistor(t *testing.T) {
	cur := resistor("C100", "OLD-10K", "0402", "10kΩ", "±1%", "62.5mW", false, 0)
	good := resistor("C25744", "0402WGF1002TCE", "0402", "10kΩ", "±1%", "62.5mW", true, 1000000)
	better := resistor("C777", "TIGHT", "0402", "10kΩ", "±0.5%", "100mW", true, 1000000)
	wrongValue := resistor("C1", "W", "0402", "100kΩ", "±1%", "62.5mW", true, 1000000)
	looser := resistor("C2", "L", "0402", "10kΩ", "±5%", "62.5mW", true, 1000000)
	weaker := resistor("C3", "P", "0402", "10kΩ", "±1%", "31mW", true, 1000000)
	bigger := resistor("C4", "B", "0603", "10kΩ", "±1%", "100mW", true, 1000000)
	noPower := resistor("C5", "N", "0402", "10kΩ", "±1%", "", true, 1000000)
	delete(noPower.Attrs, "Power(Watts)")
	short := resistor("C6", "S", "0402", "10kΩ", "±1%", "62.5mW", true, 4)
	cat := &fakeCatalog{
		byCode: map[string]*CatalogPart{"C100": cur},
		search: map[string][]CatalogPart{"10kΩ 0402": {*good, *better, *wrongValue, *looser, *weaker, *bigger, *noPower, *short}},
	}
	parts := partsOf([4]string{"R1", "10k", r0402, "C100"}, [4]string{"R3", "10k", r0402, "C100"})
	rep := Alternatives(AltInput{Parts: parts, Boards: 3, Catalog: cat})
	l := altLine(t, rep, "R1")
	if !l.Blocking || len(l.Reasons) == 0 || l.Reasons[0] != "out-of-stock" {
		t.Fatalf("reasons %v", l.Reasons)
	}
	if l.Proposal == nil || l.Proposal.LCSC != "C777" || !l.Proposal.RequiresReview {
		t.Fatalf("proposal %+v (candidates %+v)", l.Proposal, l.Candidates)
	}
	if rep.Assignments["R1"] != "C777" || rep.Assignments["R3"] != "C777" || rep.Expect["R1"] != "C100" {
		t.Errorf("assignments %v expect %v", rep.Assignments, rep.Expect)
	}
	if rep.Verdict != gate.Pass || rep.ExitCode != 0 {
		t.Errorf("verdict %s", rep.Verdict)
	}
	listed := map[string]AltCandidate{}
	for _, c := range l.Candidates {
		listed[c.LCSC] = c
	}
	for _, rejected := range []string{"C1", "C2", "C3", "C4"} {
		if _, ok := listed[rejected]; ok && listed[rejected].Eligible {
			t.Errorf("%s must not be eligible", rejected)
		}
	}
	// With the limit at 3 the unverified one may or may not be listed; it must never be eligible.
	if c, ok := listed["C5"]; ok && c.Eligible {
		t.Error("a missing Power attribute is unverified, not ok")
	}
	if c, ok := listed["C6"]; ok && c.Eligible {
		t.Error("candidate stock 4 < need 6")
	}
}

func TestAlternativesPrefersBasicThenPrice(t *testing.T) {
	cur := cap0402("C10", "OLD", "100nF", "16V", "±10%", "X7R", false, 0)
	ext := cap0402("C11", "EXT", "100nF", "16V", "±10%", "X7R", false, 99999)
	b1 := cap0402("C12", "B1", "100nF", "25V", "±10%", "X7R", true, 99999)
	b2 := cap0402("C13", "B2", "100nF", "50V", "±10%", "X7R", true, 99999)
	b1.Prices = []PriceTier{{From: 1, To: 0, Price: 0.003}}
	b2.Prices = []PriceTier{{From: 1, To: 0, Price: 0.001}}
	x5r := cap0402("C14", "X5R", "100nF", "50V", "±10%", "X5R", true, 99999)
	lowV := cap0402("C15", "LV", "100nF", "10V", "±10%", "X7R", true, 99999)
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C10": cur}, search: map[string][]CatalogPart{"100nF 0402": {*ext, *b1, *b2, *x5r, *lowV}}}
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"C1", "100n", "Capacitor_SMD:C_0402_1005Metric", "C10"}), Boards: 1, Catalog: cat})
	l := altLine(t, rep, "C1")
	if l.Proposal == nil || l.Proposal.LCSC != "C13" {
		t.Fatalf("want cheapest basic C13, got %+v", l.Proposal)
	}
	for _, c := range l.Candidates {
		if (c.LCSC == "C14" || c.LCSC == "C15") && c.Eligible {
			t.Errorf("%s (dielectric / voltage) must not be eligible", c.LCSC)
		}
	}
}

func TestAlternativesReserveStockAcrossLines(t *testing.T) {
	a := resistor("C201", "OLD-A", "0402", "1kΩ", "±1%", "62.5mW", true, 0)
	b := resistor("C202", "OLD-B", "0402", "1kΩ", "±1%", "62.5mW", true, 0)
	only := resistor("C300", "ONLY", "0402", "1kΩ", "±1%", "62.5mW", true, 15) // need per line: 2*? see below
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C201": a, "C202": b}, search: map[string][]CatalogPart{"1kΩ 0402": {*only}}}
	// Each old line: 1 placement × 10 boards + loss 10 = 20 would exceed 15, so use 1 board: need = 1 + loss(10)=11 each > 15/2.
	parts := partsOf([4]string{"R1", "1k", r0402, "C201"}, [4]string{"R2", "1k", r0402, "C202"})
	rep := Alternatives(AltInput{Parts: parts, Boards: 1, Catalog: cat})
	got := 0
	for _, ref := range []string{"R1", "R2"} {
		if altLine(t, rep, ref).Proposal != nil {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("exactly one line may claim the 15 in stock, got %d: %+v", got, rep.Assignments)
	}
	if rep.Verdict != gate.Fail || rep.ExitCode != 6 {
		t.Errorf("the line left without a replacement is blocking: %s", rep.Verdict)
	}
}

func TestAlternativesSameMPNOnlyForUnknownFamilies(t *testing.T) {
	ic := &CatalogPart{LCSC: "C500", MPN: "LDO-33", Brand: "ACME", Package: "SOT-23-5", Category: "Voltage Regulators - Linear", Base: bp(false),
		Stock: i64(0), Buyable: bp(true), Loss: i64(0), Attrs: map[string]string{"Output Voltage": "3.3V"}}
	same := &CatalogPart{LCSC: "C501", MPN: "LDO-33", Brand: "ACME", Package: "SOT-23-5", Category: "Voltage Regulators - Linear", Base: bp(true), // extended current line: a basic replacement
		Stock: i64(50000), Buyable: bp(true), Loss: i64(0)}
	samePkgOther := &CatalogPart{LCSC: "C502", MPN: "LDO-33", Brand: "ACME", Package: "SOT-89-5", Category: "Voltage Regulators - Linear", Base: bp(false),
		Stock: i64(50000), Buyable: bp(true), Loss: i64(0)}
	other := &CatalogPart{LCSC: "C503", MPN: "LDO-33B", Package: "SOT-23-5", Category: "Voltage Regulators - Linear", Base: bp(true),
		Stock: i64(50000), Buyable: bp(true), Attrs: map[string]string{"Output Voltage": "3.3V"}}
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C500": ic}, search: map[string][]CatalogPart{"LDO-33": {*same, *samePkgOther, *other}}}
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"U1", "LDO", "Package_TO_SOT_SMD:SOT-23-5", "C500"}), Boards: 1, Catalog: cat})
	l := altLine(t, rep, "U1")
	if l.Proposal == nil || l.Proposal.LCSC != "C501" {
		t.Fatalf("want same-MPN C501, got %+v", l.Proposal)
	}
	for _, c := range l.Candidates {
		if c.LCSC == "C503" && c.Eligible {
			t.Error("a different MPN of an unverified family must never be proposed")
		}
		if c.LCSC == "C502" && c.Eligible {
			t.Error("a same-MPN part in another package must not be proposed")
		}
	}
	if !l.Proposal.RequiresReview {
		t.Error("every proposal needs review")
	}
}

func TestAlternativesExtendedOnlyNeedsBasic(t *testing.T) {
	cur := resistor("C400", "EXT", "0402", "2kΩ", "±1%", "62.5mW", false, 900000)
	ext2 := resistor("C401", "EXT2", "0402", "2kΩ", "±1%", "62.5mW", false, 900000)
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C400": cur}, search: map[string][]CatalogPart{"2kΩ 0402": {*ext2}}}
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"R1", "2k", r0402, "C400"}), Boards: 1, Catalog: cat})
	l := altLine(t, rep, "R1")
	if l.Blocking || l.Proposal != nil || len(rep.Assignments) != 0 {
		t.Fatalf("an extended part with only extended alternatives stays: %+v", l)
	}
	if rep.Verdict != gate.Pass {
		t.Errorf("extended-only lines are advisory: %s", rep.Verdict)
	}
	basic := resistor("C402", "BASIC", "0402", "2kΩ", "±1%", "62.5mW", true, 900000)
	cat.search["2kΩ 0402"] = []CatalogPart{*ext2, *basic}
	rep = Alternatives(AltInput{Parts: partsOf([4]string{"R1", "2k", r0402, "C400"}), Boards: 1, Catalog: cat})
	if p := altLine(t, rep, "R1").Proposal; p == nil || p.LCSC != "C402" {
		t.Errorf("want the basic part, got %+v", p)
	}
}

func TestAlternativesUnknownNumberAndOffline(t *testing.T) {
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"R1", "1k", r0402, "C999"}), Boards: 1, Catalog: &fakeCatalog{}})
	l := altLine(t, rep, "R1")
	if !l.Blocking || l.Reasons[0] != "not-in-catalog" || l.Proposal != nil || rep.Verdict != gate.Fail {
		t.Errorf("%+v verdict %s", l, rep.Verdict)
	}
	rep = Alternatives(AltInput{Parts: partsOf([4]string{"R1", "1k", r0402, "C999"}), Boards: 1, Catalog: &fakeCatalog{lookErr: errString("offline")}})
	if rep.Verdict != gate.CannotCheck || rep.ExitCode != 5 {
		t.Errorf("offline: %s", rep.Verdict)
	}
	if len(rep.Assignments) != 0 {
		t.Error("offline must propose nothing")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestAlternativesNothingToDo(t *testing.T) {
	good := resistor("C25744", "X", "0402", "10kΩ", "±1%", "62.5mW", true, 1000000)
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"R1", "10k", r0402, "C25744"}), Boards: 1, Catalog: &fakeCatalog{byCode: map[string]*CatalogPart{"C25744": good}}})
	if rep.Verdict != gate.Pass || len(rep.Assignments) != 0 || len(rep.Lines[0].Reasons) != 0 {
		t.Errorf("%+v", rep)
	}
	for _, l := range rep.Lines {
		if strings.Join(l.Reasons, "") != "" {
			t.Errorf("reasons on a healthy line: %v", l.Reasons)
		}
	}
}

func TestAlternativesUnknownFieldsAndUnnumberedPartsAreNotPass(t *testing.T) {
	p := resistor("C1", "X", "0402", "1kΩ", "±1%", "62.5mW", true, 0)
	p.Stock, p.Buyable = nil, nil
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"R1", "1k", r0402, "C1"}), Boards: 1, Catalog: &fakeCatalog{byCode: map[string]*CatalogPart{"C1": p}}})
	if rep.Verdict != gate.CannotCheck {
		t.Errorf("record without stock/buyable: %s", rep.Verdict)
	}
	good := resistor("C25744", "X", "0402", "10kΩ", "±1%", "62.5mW", true, 1000000)
	rep = Alternatives(AltInput{Parts: partsOf([4]string{"R1", "10k", r0402, "C25744"}, [4]string{"R2", "10k", r0402, ""}), Boards: 1,
		Catalog: &fakeCatalog{byCode: map[string]*CatalogPart{"C25744": good}}})
	if rep.Verdict != gate.CannotCheck || len(rep.Warnings) != 1 {
		t.Errorf("unnumbered part: %s %v", rep.Verdict, rep.Warnings)
	}
}

func TestNormMPNKeepsDecimalPoint(t *testing.T) {
	if NormMPN("AMS1117-3.3") == NormMPN("AMS1117-33") {
		t.Error("3.3 V and 33 must differ")
	}
	if NormMPN("rc0402fr-07 1kl") != NormMPN("RC0402FR_071KL") {
		t.Error("case, space, - and _ are ignored")
	}
}

func TestAlternativesSameMPNOtherBrandIsNotSame(t *testing.T) {
	ic := &CatalogPart{LCSC: "C500", MPN: "LM358", Brand: "TI", Package: "SOP-8", Category: "Op Amps", Base: bp(false), Stock: i64(0), Buyable: bp(true)}
	other := &CatalogPart{LCSC: "C501", MPN: "LM358", Brand: "Other Corp", Package: "SOP-8", Category: "Op Amps", Base: bp(false), Stock: i64(99999), Buyable: bp(true)}
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C500": ic}, search: map[string][]CatalogPart{"LM358": {*other}}}
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"U1", "op", "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm", "C500"}), Boards: 1, Catalog: cat})
	if altLine(t, rep, "U1").Proposal != nil {
		t.Error("another manufacturer's part must not be proposed as same-MPN")
	}
}

// ── review round 2 ─────────────────────────────────────────────────────────

func ldo(code, mpn, brand string, stock int64) *CatalogPart {
	return &CatalogPart{LCSC: code, MPN: mpn, Brand: brand, Package: "SOP-8", Category: "Op Amps", Base: bp(true), Stock: i64(stock), Buyable: bp(true), Loss: i64(0)}
}

const sop8 = "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm"

// Finding 1: same MPN needs a known, equal brand on both sides.
func TestAlternativesSameMPNNeedsKnownBrandOnBothSides(t *testing.T) {
	for name, brands := range map[string][2]string{"current missing": {"", "TI"}, "candidate missing": {"TI", ""}, "both missing": {"", ""}} {
		cur := ldo("C500", "LM358", brands[0], 0)
		cand := ldo("C501", "LM358", brands[1], 99999)
		cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C500": cur}, search: map[string][]CatalogPart{"LM358": {*cand}}}
		rep := Alternatives(AltInput{Parts: partsOf([4]string{"U1", "op", sop8, "C500"}), Boards: 1, Catalog: cat})
		l := altLine(t, rep, "U1")
		if l.Proposal != nil || len(rep.Assignments) != 0 {
			t.Errorf("%s: same MPN without a known matching brand must not be proposed: %+v", name, l.Proposal)
		}
		found := false
		for _, c := range l.Candidates {
			if c.LCSC == "C501" {
				found = true
				if c.Eligible || c.SameMPN || !strings.Contains(strings.Join(c.Unverified, ";"), "manufacturer") {
					t.Errorf("%s: %+v", name, c)
				}
			}
		}
		if !found {
			t.Errorf("%s: the candidate should be listed as unverified", name)
		}
	}
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C500": ldo("C500", "LM358", "Ti", 0)}, search: map[string][]CatalogPart{"LM358": {*ldo("C501", "lm-358", "TI", 99999)}}}
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"U1", "op", sop8, "C500"}), Boards: 1, Catalog: cat})
	if p := altLine(t, rep, "U1").Proposal; p == nil || p.LCSC != "C501" {
		t.Errorf("equal normalised brands are the same maker: %+v", p)
	}
}

// Finding 3: a line that is extended AND blocked still needs a basic replacement.
func TestAlternativesExtendedAndOutOfStockNeedsBasic(t *testing.T) {
	cur := resistor("C400", "EXT", "0402", "2kΩ", "±1%", "62.5mW", false, 0)
	ext2 := resistor("C401", "EXT2", "0402", "2kΩ", "±1%", "62.5mW", false, 900000)
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C400": cur}, search: map[string][]CatalogPart{"2kΩ 0402": {*ext2}}}
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"R1", "2k", r0402, "C400"}), Boards: 1, Catalog: cat})
	l := altLine(t, rep, "R1")
	if !l.Blocking || l.Proposal != nil || rep.Verdict != gate.Fail {
		t.Fatalf("an extended replacement for an extended line is not allowed: %+v", l)
	}
	basic := resistor("C402", "BASIC", "0402", "2kΩ", "±1%", "62.5mW", true, 900000)
	cat.search["2kΩ 0402"] = []CatalogPart{*ext2, *basic}
	rep = Alternatives(AltInput{Parts: partsOf([4]string{"R1", "2k", r0402, "C400"}), Boards: 1, Catalog: cat})
	if p := altLine(t, rep, "R1").Proposal; p == nil || p.LCSC != "C402" {
		t.Errorf("want the basic part, got %+v", p)
	}
	// A blocked line whose current part is basic may take an extended replacement.
	curB := resistor("C410", "B", "0402", "2kΩ", "±1%", "62.5mW", true, 0)
	cat = &fakeCatalog{byCode: map[string]*CatalogPart{"C410": curB}, search: map[string][]CatalogPart{"2kΩ 0402": {*ext2}}}
	rep = Alternatives(AltInput{Parts: partsOf([4]string{"R1", "2k", r0402, "C410"}), Boards: 1, Catalog: cat})
	if p := altLine(t, rep, "R1").Proposal; p == nil || p.LCSC != "C401" {
		t.Errorf("basic → extended is allowed for a blocked line: %+v", p)
	}
}

type searchErrCatalog struct{ *fakeCatalog }

func (searchErrCatalog) Search(string, int) ([]CatalogPart, error) {
	return nil, errString("search down")
}

// Finding 4: a failed search on an extended-only line is cannot-check, not a pass.
func TestAlternativesSearchErrorOnExtendedOnlyLine(t *testing.T) {
	cur := resistor("C400", "EXT", "0402", "2kΩ", "±1%", "62.5mW", false, 900000)
	cat := searchErrCatalog{&fakeCatalog{byCode: map[string]*CatalogPart{"C400": cur}}}
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"R1", "2k", r0402, "C400"}), Boards: 1, Catalog: cat})
	l := altLine(t, rep, "R1")
	if l.Blocking || l.Error == "" {
		t.Fatalf("%+v", l)
	}
	if rep.Verdict != gate.CannotCheck || rep.ExitCode != 5 {
		t.Errorf("verdict %s", rep.Verdict)
	}
}

// Finding 5: a number the catalog does not know is searched from the design's own data, strictly.
func TestAlternativesNotInCatalogSearchesFromDesign(t *testing.T) {
	cand := resistor("C25744", "RC0402FR-0710KL", "0402", "10kΩ", "±1%", "62.5mW", true, 1000000)
	cand.Brand = "YAGEO"
	cat := &fakeCatalog{search: map[string][]CatalogPart{"RC0402FR-0710KL": {*cand}, "10k 0402": {*cand}}}
	parts := partsOf([4]string{"R1", "10k", r0402, "C999"})
	rep := Alternatives(AltInput{Parts: parts, DesignMPN: map[string]string{"R1": "RC0402FR-0710KL"}, Boards: 1, Catalog: cat})
	l := altLine(t, rep, "R1")
	if len(cat.queries) != 2 {
		t.Errorf("queries %v: MPN and value + package expected", cat.queries)
	}
	if l.Proposal != nil || len(rep.Assignments) != 0 || rep.Verdict != gate.Fail {
		t.Fatalf("no record to compare against: nothing may be proposed: %+v", l)
	}
	if len(l.Candidates) != 1 || l.Candidates[0].Eligible || !strings.Contains(strings.Join(l.Candidates[0].Unverified, ";"), "insufficient evidence") {
		t.Errorf("candidates %+v", l.Candidates)
	}
	if !l.hasReason("insufficient-evidence") {
		t.Errorf("reasons %v", l.Reasons)
	}
	// The catalog being unreachable is not "not in catalog": no search, cannot-check.
	cat2 := &fakeCatalog{lookErr: errString("offline")}
	rep = Alternatives(AltInput{Parts: parts, DesignMPN: map[string]string{"R1": "X"}, Boards: 1, Catalog: cat2})
	if len(cat2.queries) != 0 || rep.Verdict != gate.CannotCheck {
		t.Errorf("queries %v verdict %s", cat2.queries, rep.Verdict)
	}
}

// Finding 6: stock needed by retained lines — including unknown-buyability ones — is reserved.
func TestAlternativesReservesRetainedUnknownLine(t *testing.T) {
	// A (C1) keeps its number: buyable unknown, stock 15, loss 10 → its demand 1+10 = 11 is claimed.
	a := resistor("C1", "KEEP", "0402", "1kΩ", "±1%", "62.5mW", true, 15)
	a.Buyable = nil
	b := resistor("C2", "OLD-B", "0402", "1kΩ", "±1%", "62.5mW", true, 0)
	cand := *a
	cand.Buyable = bp(true)
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C1": a, "C2": b}, search: map[string][]CatalogPart{"1kΩ 0402": {cand}}}
	parts := partsOf([4]string{"R1", "1k", r0402, "C1"}, [4]string{"R2", "1k", r0402, "C2"})
	rep := Alternatives(AltInput{Parts: parts, Boards: 1, Catalog: cat})
	if altLine(t, rep, "R2").Proposal != nil || len(rep.Assignments) != 0 {
		t.Errorf("C1's remaining stock (15-11=4) cannot cover R2 (need 11): %v", rep.Assignments)
	}
	// Known and ample: 100 - 11 = 89 ≥ 11 → fine.
	a.Stock, cand.Stock = i64(100), i64(100)
	cat.search["1kΩ 0402"] = []CatalogPart{cand}
	rep = Alternatives(AltInput{Parts: parts, Boards: 1, Catalog: cat})
	if p := altLine(t, rep, "R2").Proposal; p == nil || p.LCSC != "C1" {
		t.Errorf("ample stock: %+v", p)
	}
	// Retained line with an unknown stock figure: the remainder cannot be established.
	a.Stock = nil
	cat.search["1kΩ 0402"] = []CatalogPart{cand}
	rep = Alternatives(AltInput{Parts: parts, Boards: 1, Catalog: cat})
	if altLine(t, rep, "R2").Proposal != nil {
		t.Error("a number kept by a line with unknown stock cannot be handed to another line")
	}
}

// Finding 9: a missing lossNumber is unknown, not zero.
func TestAlternativesMissingLossIsUnverifiable(t *testing.T) {
	p := resistor("C1", "X", "0402", "1kΩ", "±1%", "62.5mW", true, 5)
	p.Loss = nil
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"R1", "1k", r0402, "C1"}), Boards: 5, Catalog: &fakeCatalog{byCode: map[string]*CatalogPart{"C1": p}}})
	l := altLine(t, rep, "R1")
	if !l.Unknown || !l.hasReason("loss-unknown") || rep.Verdict != gate.CannotCheck {
		t.Errorf("stock exactly = placements × boards with no loss figure must not pass: %+v verdict %s", l, rep.Verdict)
	}
	// As a candidate: no loss figure → not eligible.
	cur := resistor("C2", "OLD", "0402", "1kΩ", "±1%", "62.5mW", true, 0)
	cand := resistor("C3", "NEW", "0402", "1kΩ", "±1%", "62.5mW", true, 1000)
	cand.Loss = nil
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C2": cur}, search: map[string][]CatalogPart{"1kΩ 0402": {*cand}}}
	rep = Alternatives(AltInput{Parts: partsOf([4]string{"R1", "1k", r0402, "C2"}), Boards: 1, Catalog: cat})
	if altLine(t, rep, "R1").Proposal != nil {
		t.Error("candidate without a loss allowance must not be proposed")
	}
}

// Finding 2: the plan fingerprint follows the substitution-relevant data.
func TestPlanFingerprintTracksRelevantFields(t *testing.T) {
	base := partsOf([4]string{"R1", "10k", r0402, "C100"}, [4]string{"R2", "10k", r0402, "C100"}, [4]string{"R9", "1u", r0402, "C5"})
	mpn := map[string]string{"R1": "MPN-A"}
	codes := PlanCodes(map[string]string{"R1": "C200", "R2": "C200"}, map[string]string{"R1": "C100", "R2": "C100"})
	fp := PlanFingerprint(base, mpn, 3, codes)
	same := func(parts []Part, m map[string]string, boards int) bool {
		return PlanFingerprint(parts, m, boards, codes) == fp
	}
	if !same(base, mpn, 3) {
		t.Fatal("fingerprint must be stable")
	}
	mod := func(f func(ps []Part)) []Part {
		ps := append([]Part(nil), base...)
		f(ps)
		return ps
	}
	cases := map[string]bool{
		"value":       same(mod(func(ps []Part) { ps[0].Value = "4k7" }), mpn, 3),
		"footprint":   same(mod(func(ps []Part) { ps[0].Footprint = "Resistor_SMD:R_0603_1608Metric" }), mpn, 3),
		"mpn":         same(base, map[string]string{"R1": "MPN-B"}, 3),
		"boards":      same(base, mpn, 4),
		"placement":   same(append(append([]Part(nil), base...), Part{Ref: "R10", Value: "10k", Footprint: r0402, LCSC: "C200"}), mpn, 3),
		"dnp":         same(mod(func(ps []Part) { ps[1].DNP = true }), mpn, 3),
		"removed ref": same(base[:1], mpn, 3),
	}
	for name, unchanged := range cases {
		if unchanged {
			t.Errorf("%s change must change the fingerprint", name)
		}
	}
	// Unrelated parts do not invalidate the plan.
	if !same(mod(func(ps []Part) { ps[2].Value = "2u2" }), mpn, 3) {
		t.Error("a part on an unrelated number must not change the fingerprint")
	}
}

func TestAlternativesReportCarriesFingerprint(t *testing.T) {
	cur := resistor("C100", "OLD", "0402", "10kΩ", "±1%", "62.5mW", true, 0)
	good := resistor("C25744", "G", "0402", "10kΩ", "±1%", "62.5mW", true, 1000000)
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C100": cur}, search: map[string][]CatalogPart{"10kΩ 0402": {*good}}}
	parts := partsOf([4]string{"R1", "10k", r0402, "C100"})
	rep := Alternatives(AltInput{Parts: parts, Boards: 2, Catalog: cat})
	if rep.Fingerprint == "" || rep.Fingerprint != PlanFingerprint(parts, nil, 2, PlanCodes(rep.Assignments, rep.Expect)) {
		t.Errorf("fingerprint %q", rep.Fingerprint)
	}
	pl := &Plan{Assign: rep.Assignments, Expect: rep.Expect, Boards: rep.Boards, Fingerprint: rep.Fingerprint}
	if bad := VerifyPlan(parts, nil, pl); len(bad) != 0 {
		t.Errorf("unchanged design: %v", bad)
	}
	changed := partsOf([4]string{"R1", "4k7", r0402, "C100"})
	if bad := VerifyPlan(changed, nil, pl); len(bad) != 1 || !strings.Contains(bad[0], "fingerprint") {
		t.Errorf("changed value: %v", bad)
	}
	added := append(append([]Part(nil), parts...), Part{Ref: "R2", Value: "10k", Footprint: r0402, LCSC: "C25744"})
	if bad := VerifyPlan(added, nil, pl); len(bad) != 1 {
		t.Errorf("added placement on the replacement number: %v", bad)
	}
}

// Round-2 finding 1: a blocking line that finds no replacement keeps its stock reserved.
func TestAlternativesBlockedLineWithoutReplacementKeepsItsStockReserved(t *testing.T) {
	a := resistor("C201", "OLD-A", "0402", "1kΩ", "±1%", "62.5mW", true, 0)
	// C300 itself is blocked: 6 placements need 6+10 > 15 in stock, and nothing replaces it.
	held := resistor("C300", "HELD", "0402", "1kΩ", "±1%", "62.5mW", true, 15)
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C201": a, "C300": held}, search: map[string][]CatalogPart{"1kΩ 0402": {*held}}}
	rows := [][4]string{{"R1", "1k", r0402, "C201"}}
	for _, r := range []string{"R3", "R4", "R5", "R6", "R7", "R8"} {
		rows = append(rows, [4]string{r, "1k", r0402, "C300"})
	}
	rep := Alternatives(AltInput{Parts: partsOf(rows...), Boards: 1, Catalog: cat})
	if !altLine(t, rep, "R3").Blocking {
		t.Fatal("C300 should be blocked on stock")
	}
	if p := altLine(t, rep, "R1").Proposal; p != nil {
		t.Errorf("R1 must not draw on the stock the blocked C300 lines still hold: %+v", p)
	}
	if len(rep.Assignments) != 0 || rep.Verdict != gate.Fail {
		t.Errorf("assignments %v verdict %s", rep.Assignments, rep.Verdict)
	}
}

// Round-2 finding 1: stock is released once every placement of the blocked number is replaced.
func TestAlternativesReplacedBlockedNumberReleasesItsStock(t *testing.T) {
	a := resistor("C201", "OLD-A", "0402", "1kΩ", "±1%", "62.5mW", true, 0)
	b := resistor("C202", "OLD-B", "0402", "1kΩ", "±1%", "62.5mW", true, 0)
	good := resistor("C300", "GOOD", "0402", "1kΩ", "±1%", "62.5mW", true, 100)
	cat := &fakeCatalog{byCode: map[string]*CatalogPart{"C201": a, "C202": b}, search: map[string][]CatalogPart{"1kΩ 0402": {*good}}}
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"R1", "1k", r0402, "C201"}, [4]string{"R2", "1k", r0402, "C202"}), Boards: 1, Catalog: cat})
	if rep.Assignments["R1"] != "C300" || rep.Assignments["R2"] != "C300" || rep.Verdict != gate.Pass {
		t.Errorf("both fit in 100: %v %s", rep.Assignments, rep.Verdict)
	}
}

// Round-2 finding 2: a proposal does not mask a failed catalog query.
type firstSearchErrCatalog struct {
	*fakeCatalog
	n int
}

func (c *firstSearchErrCatalog) Search(q string, l int) ([]CatalogPart, error) {
	c.n++
	if c.n == 1 {
		return nil, errString("search down")
	}
	return c.fakeCatalog.Search(q, l)
}

func TestAlternativesFailedSearchWithProposalIsCannotCheck(t *testing.T) {
	cur := resistor("C100", "OLD-10K", "0402", "10kΩ", "±1%", "62.5mW", false, 0)
	good := resistor("C25744", "0402WGF1002TCE", "0402", "10kΩ", "±1%", "62.5mW", true, 1000000)
	cat := &firstSearchErrCatalog{fakeCatalog: &fakeCatalog{byCode: map[string]*CatalogPart{"C100": cur}, search: map[string][]CatalogPart{"10kΩ 0402": {*good}}}}
	rep := Alternatives(AltInput{Parts: partsOf([4]string{"R1", "10k", r0402, "C100"}), Boards: 1, Catalog: cat})
	l := altLine(t, rep, "R1")
	if l.Proposal == nil || l.Error == "" {
		t.Fatalf("expected a proposal and a search error: %+v", l)
	}
	if rep.Verdict != gate.CannotCheck || rep.ExitCode != 5 {
		t.Errorf("verdict %s", rep.Verdict)
	}
}

// Round-2 finding 3: punctuation other than space, '-' and '_' distinguishes MPNs.
func TestNormMPNPunctuationDistinct(t *testing.T) {
	for _, p := range [][2]string{{"AB#1", "AB1"}, {"AB,1", "AB1"}, {"AB(1)", "AB1"}, {"AB(1)", "AB[1]"}, {"AB#1", "AB,1"}, {"AB;1", "AB1"}, {"AB:1", "AB1"}} {
		if NormMPN(p[0]) == NormMPN(p[1]) {
			t.Errorf("%q and %q must differ", p[0], p[1])
		}
	}
	if NormMPN(" ab-1_2 ") != "AB12" {
		t.Errorf("got %q", NormMPN(" ab-1_2 "))
	}
}
