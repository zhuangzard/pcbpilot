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
	ic := &CatalogPart{LCSC: "C500", MPN: "LDO-33", Package: "SOT-23-5", Category: "Voltage Regulators - Linear", Base: bp(false),
		Stock: i64(0), Buyable: bp(true), Attrs: map[string]string{"Output Voltage": "3.3V"}}
	same := &CatalogPart{LCSC: "C501", MPN: "LDO-33", Package: "SOT-23-5", Category: "Voltage Regulators - Linear", Base: bp(false),
		Stock: i64(50000), Buyable: bp(true)}
	samePkgOther := &CatalogPart{LCSC: "C502", MPN: "LDO-33", Package: "SOT-89-5", Category: "Voltage Regulators - Linear", Base: bp(false),
		Stock: i64(50000), Buyable: bp(true)}
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
