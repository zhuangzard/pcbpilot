package app

// Real-board regressions (synthetic fixtures): insulation pairs named
// "domain:<id>" as pkg/intent writes them, a domain with no board net,
// pads of one footprint closer than the class clearance, and an order
// contract above the board's minimums.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// rulesTestIntent with pkg/intent's pair ends ("domain:<id>").
var rulesPrefixedIntent = strings.Replace(rulesTestIntent, `"pairs":[{"a":"HVD","b":"LV"`, `"pairs":[{"a":"domain:HVD","b":"domain:LV","clearanceSource":"declared","creepageSource":"derived"`, 1)

func TestKicadIsolationResolvesDomainPrefix(t *testing.T) {
	in := testIntent(t, rulesPrefixedIntent)
	iso := kicadIsolation(in)
	if len(iso) != 1 {
		t.Fatalf("iso %+v", iso)
	}
	if a, b := iso[0]["aNets"].([]string), iso[0]["bNets"].([]string); len(a) != 1 || a[0] != "HV" || len(b) != 2 {
		t.Fatalf("pair nets %v / %v", a, b)
	}
	// The per-net raise keeps its old behaviour for "domain:" pairs (route
	// byte-identical): the pair rule is the enforcement.
	if r := kicadRequirements(in)["VCC"]; r.ClearanceMil > 7 {
		t.Fatalf("VCC raised to %g mil", r.ClearanceMil)
	}
	bare := testIntent(t, rulesTestIntent)
	if r := kicadRequirements(bare)["VCC"]; r.ClearanceMil < 78 {
		t.Fatalf("bare pair: VCC %g mil", r.ClearanceMil)
	}
	if intentDomainID("domain:A") != "A" || intentDomainID(" Domain: B ") != "B" || intentDomainID("C") != "C" {
		t.Fatal("intentDomainID")
	}
}

func TestKicadRulesPrefixedPairsProbedAndPlaced(t *testing.T) {
	f := newRulesFixture(t)
	if err := os.WriteFile(f.o.intent, []byte(rulesPrefixedIntent), 0o644); err != nil {
		t.Fatal(err)
	}
	f.o.plan = true
	rep, code, stderr := runRules(t, f.o)
	if code != 0 {
		t.Fatalf("plan exit %d %+v\n%s", code, rep.Checks, stderr)
	}
	if len(rep.Isolation) != 1 || rep.Isolation[0].BoardNetsA != 1 || rep.Isolation[0].BoardNetsB != 2 || rep.Isolation[0].ClearanceSource != "declared" {
		t.Fatalf("isolation %+v", rep.Isolation)
	}
	if c := checkOf(rep, "isolation-coverage"); c.Status != gate.Pass {
		t.Fatalf("coverage %+v", c)
	}
	sr := checkOf(rep, "staged-readback")
	if strings.Contains(strings.Join(sr.Info, "\n"), "nothing to probe") || !strings.Contains(sr.Detail, "2 of 2 .kicad_dru probe") && !strings.Contains(sr.Detail, "attributable") {
		t.Fatalf("staged %+v", sr)
	}
	// --check --stage placed: the placement proxy sees the pair (J1 ↔ U1).
	a := f.o
	a.plan, a.apply = false, true
	if _, code, _ := runRules(t, a); code != 0 {
		t.Fatal(code)
	}
	c := f.o
	c.plan, c.check, c.stage = false, true, "placed"
	rep, _, _ = runRules(t, c)
	pp := checkOf(rep, "isolation-placement")
	if pp.Status != gate.Warn || !strings.Contains(strings.Join(pp.Items, "\n"), "J1 ↔ U1") {
		t.Fatalf("placement %+v", pp)
	}
}

// A domain whose nets are not on the board: cannot-check, apply refused,
// placement not a pass.
func TestKicadRulesEmptyDomainIsCannotCheck(t *testing.T) {
	f := newRulesFixture(t)
	in := strings.Replace(rulesPrefixedIntent, `{"id":"HVD","nets":["HV"]}`, `{"id":"HVD","nets":["HV_SHORT_NAME"]}`, 1)
	if err := os.WriteFile(f.o.intent, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	f.o.plan = true
	rep, code, _ := runRules(t, f.o)
	cov := checkOf(rep, "isolation-coverage")
	if code != 5 || rep.Verdict != gate.CannotCheck || cov.Status != gate.CannotCheck || !strings.Contains(strings.Join(cov.Items, "\n"), "domain HVD has 1 intent net(s), none on the board") {
		t.Fatalf("plan exit %d %+v", code, cov)
	}
	a := f.o
	a.plan, a.apply = false, true
	rep, code, _ = runRules(t, a)
	if code != 5 || rep.Committed || fileExists(boardSibling(f.o.pcb, ".kicad_dru")) {
		t.Fatalf("apply exit %d committed %v", code, rep.Committed)
	}
	c := f.o
	c.plan, c.check, c.stage = false, true, "placed"
	rep, _, _ = runRules(t, c)
	if pp := checkOf(rep, "isolation-placement"); pp.Status != gate.CannotCheck || pp.Advisory {
		t.Fatalf("placement %+v", pp)
	}
}

func TestPadGapMil(t *testing.T) {
	// 0.5 mm pitch, 0.35 mm wide pads: 0.15 mm gap.
	g := padGapMil(0, 0, 13.78, 40, 19.685, 0, 13.78, 40)
	if g < 5.9 || g > 5.92 {
		t.Fatalf("gap %g", g)
	}
	if padGapMil(0, 0, 10, 10, 5, 0, 10, 10) != 0 {
		t.Fatal("overlap")
	}
	if g := padGapMil(0, 0, 10, 10, 13, 14, 10, 10); g < 4.99 || g > 5.01 {
		t.Fatalf("diagonal %g", g)
	}
}

// A footprint whose pads are 0.15 mm apart under a 0.2 mm clearance is
// reported (advisory) per footprint with the fix; a local clearance
// override below the gap clears it.
func TestKicadRulesFootprintPadClearance(t *testing.T) {
	f := newRulesFixture(t)
	snap := strings.Replace(rulesTestSnap, `{"designator":"R2"`, `{"designator":"U5","footprint":"Lib:VSSOP-8_P0.5mm","side":"top","courtyard":{"minX":1200,"minY":100,"maxX":1300,"maxY":300},"pads":[{"padNumber":"1","net":"SIG","x":1250,"y":200,"width":13.78,"height":40},{"padNumber":"2","net":"AUX","x":1269.685,"y":200,"width":13.78,"height":40}]},
  {"designator":"R2"`, 1)
	// U6: an exposed pad (GND) with an unnamed thermal-via PTH inside it.
	snap = strings.Replace(snap, `{"designator":"R2"`, `{"designator":"U6","footprint":"Lib:SOIC-8-EP","side":"top","courtyard":{"minX":1400,"minY":100,"maxX":1500,"maxY":300},"pads":[{"padNumber":"9","net":"GND","layer":1,"x":1450,"y":200,"width":80,"height":80},{"padNumber":"","net":"","layer":12,"x":1440,"y":190,"width":24,"height":24}]},
  {"designator":"R2"`, 1)
	f.fake.snap = []byte(snap)
	f.o.plan = true
	rep, code, _ := runRules(t, f.o)
	if c := checkOf(rep, "footprint-pad-clearance"); !strings.Contains(strings.Join(c.Items, "\n"), "Lib:SOIC-8-EP: pad(s) without a net overlap pad 9 (GND)") {
		t.Fatalf("thermal via: %+v", c.Items)
	}
	c := checkOf(rep, "footprint-pad-clearance")
	items := strings.Join(c.Items, "\n")
	if code != 0 || c.Status != gate.Warn || !c.Advisory || !strings.Contains(items, "Lib:VSSOP-8_P0.5mm: pad gap 0.15mm (pads 1/2) < clearance 0.2mm") || !strings.Contains(items, "on U5") || !strings.Contains(items, "footprint-local clearance override") {
		t.Fatalf("exit %d %+v", code, c)
	}
	f.fake.snap = []byte(strings.Replace(snap, `"footprint":"Lib:VSSOP-8_P0.5mm","side":"top",`, `"footprint":"Lib:VSSOP-8_P0.5mm","localClearance":5.5,"side":"top",`, 1))
	rep, _, _ = runRules(t, f.o)
	if c := checkOf(rep, "footprint-pad-clearance"); strings.Contains(strings.Join(c.Items, "\n"), "VSSOP") {
		t.Fatalf("override ignored: %+v", c)
	}
}

func TestRaiseProjectToContract(t *testing.T) {
	pro := []byte(`{"board":{"design_settings":{"rules":{"min_clearance":0.0,"min_track_width":0.2,"min_via_annular_width":0.1,"min_through_hole_diameter":0.3,"min_via_diameter":0.5}}},"meta":{"version":3}}`)
	c := &kicad.FabContract{Vendor: "V", Layers: 2}
	c.Limits.MinTrackMm, c.Limits.MinSpaceMm, c.Limits.MinDrillMm, c.Limits.MinAnnularMm = 0.09, 0.09, 0.15, 0.13
	nb, raised, err := raiseProjectToContract(pro, c)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Board struct {
			DS struct {
				Rules map[string]float64 `json:"rules"`
			} `json:"design_settings"`
		} `json:"board"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal(nb, &m); err != nil {
		t.Fatal(err)
	}
	r := m.Board.DS.Rules
	if r["min_clearance"] != 0.09 || r["min_via_annular_width"] != 0.13 || r["min_track_width"] != 0.2 || r["min_through_hole_diameter"] != 0.3 || r["min_via_diameter"] != 0.5 || m.Meta["version"] == nil {
		t.Fatalf("rules %v", r)
	}
	if len(raised) != 2 {
		t.Fatalf("raised %+v", raised)
	}
	// Nothing below: the bytes are returned unchanged.
	if nb2, raised2, _ := raiseProjectToContract(nb, c); len(raised2) != 0 || string(nb2) != string(nb) {
		t.Fatal("second raise changed something")
	}
}

// --contract on the fake: the staged project carries the raised minimums
// and fab-contract passes; --check of the unraised board fails it.
func TestKicadRulesContract(t *testing.T) {
	f := newRulesFixture(t)
	p := rulesFakeDefault()
	pb, _ := json.Marshal(p)
	var pm map[string]any
	_ = json.Unmarshal(pb, &pm)
	pm["board"] = map[string]any{"design_settings": map[string]any{"rules": map[string]any{"min_via_annular_width": 0.1, "min_clearance": 0.15, "min_track_width": 0.15, "min_through_hole_diameter": 0.25}}}
	pb, _ = json.MarshalIndent(pm, "", "  ")
	if err := os.WriteFile(boardSibling(f.o.pcb, ".kicad_pro"), pb, 0o644); err != nil {
		t.Fatal(err)
	}
	contract := filepath.Join(f.dir, "contract.json")
	_ = os.WriteFile(contract, []byte(`{"vendor":"V","layers":2,"limits":{"minTrackMm":0.09,"minSpaceMm":0.09,"minDrillMm":0.15,"minAnnularMm":0.13},
 "sources":[{"item":"limits","url":"https://example.com/caps","retrieved":"2026-01-01"},{"item":"layers","url":"https://example.com/caps","retrieved":"2026-01-01"}]}`), 0o644)
	f.o.contract = contract
	c := f.o
	c.check = true
	rep, _, _ := runRules(t, c)
	if fc := checkOf(rep, "fab-contract"); fc.Status != gate.Fail || !strings.Contains(strings.Join(fc.Items, "\n"), "annular") {
		t.Fatalf("check before apply: %+v", fc)
	}
	f.o.plan = true
	rep, _, stderr := runRules(t, f.o)
	fc := checkOf(rep, "fab-contract")
	if fc.Status == gate.Fail || !strings.Contains(strings.Join(fc.Info, "\n"), "min_via_annular_width raised 0.1 → 0.13") {
		t.Fatalf("plan: %+v\n%s", fc, stderr)
	}
	in := false
	for _, x := range rep.Inputs {
		in = in || x.Role == "contract" && x.SHA256 != ""
	}
	if !in {
		t.Fatalf("contract not an input: %+v", rep.Inputs)
	}
}
