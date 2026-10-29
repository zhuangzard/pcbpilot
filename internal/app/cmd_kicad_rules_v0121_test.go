package app

// Real-board regressions (synthetic fixtures): insulation pairs named
// "domain:<id>" as pkg/intent writes them, a domain with no board net,
// pads of one footprint closer than the class clearance, and an order
// contract above the board's minimums.

import (
	"encoding/json"
	"fmt"
	"math"
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

func TestPadShapeGap(t *testing.T) {
	near := func(what string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 0.01 {
			t.Fatalf("%s: %g, want %g", what, got, want)
		}
	}
	rect := func(x, y, w, h, rot float64) padShape {
		return padShapeOf(x, y, 0, 0, rot, []any{"RECT", w, h})
	}
	// 0.5 mm pitch, 0.35 mm wide pads: 0.15 mm gap.
	g, ok := padShapeGap(rect(0, 0, 13.78, 40, 0), rect(19.685, 0, 13.78, 40, 0))
	near("pitch", g, 5.905)
	if !ok {
		t.Fatal("rect not exact")
	}
	// Two 40 × 10 mil pads rotated 45°, centres 30 mil apart along their
	// short axis: 20 mil gap. Their bounding boxes overlap (the old box
	// test skipped this pair as touching).
	d := 30 / math.Sqrt2
	g, _ = padShapeGap(rect(0, 0, 40, 10, 45), rect(-d, d, 40, 10, 45))
	near("rotated", g, 20)
	// Round pads 30 mil wide, centres 40 mil apart diagonally: 10 mil gap
	// (their boxes overlap).
	c := func(x, y float64) padShape { return padShapeOf(x, y, 30, 30, 0, []any{"ELLIPSE", 30.0, 30.0}) }
	g, ok = padShapeGap(c(0, 0), c(40/math.Sqrt2, 40/math.Sqrt2))
	near("round", g, 10)
	if !ok {
		t.Fatal("circle not exact")
	}
	// Oval 60 × 20 at 90°: a vertical stadium; a round pad 30 mil to its
	// right edge.
	o := padShapeOf(0, 0, 0, 0, 90, []any{"OVAL", 60.0, 20.0})
	g, _ = padShapeGap(o, c(10+30+15, 20))
	near("oval", g, 30)
	// A via inside an exposed pad: overlap.
	if g, _ := padShapeGap(rect(0, 0, 80, 80, 0), c(10, 10)); g > 0 {
		t.Fatalf("inside: %g", g)
	}
	// A custom shape is its box and not exact.
	if _, ok := padShapeGap(padShapeOf(0, 0, 10, 10, 0, []any{"POLYGON", 10.0, 10.0}), c(50, 0)); ok {
		t.Fatal("polygon exact")
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
	snap = strings.Replace(snap, `{"designator":"R2"`, `{"designator":"U6","footprint":"Lib:SOIC-8-EP","side":"top","courtyard":{"minX":1400,"minY":100,"maxX":1500,"maxY":300},"pads":[{"padNumber":"9","net":"GND","layer":1,"x":1450,"y":200,"width":80,"height":80,"shape":["RECT",80,80]},{"padNumber":"","net":"","layer":12,"x":1440,"y":190,"width":24,"height":24,"shape":["ELLIPSE",24,24]}]},
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

// Probe rows stay apart with wide tracks: a 700 mil (17.8 mm) power class
// width probe never sits within the largest clearance of another row.
func TestKicadRulesProbeRowsWideTracks(t *testing.T) {
	f := newRulesFixture(t)
	in := strings.Replace(rulesTestIntent, `"widthMil":{"outer":12,"inner":12,"min":12}`, `"widthMil":{"outer":700,"inner":700,"min":700}`, 1)
	if err := os.WriteFile(f.o.intent, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	f.o.plan = true
	rep, code, stderr := runRules(t, f.o)
	if code != 0 {
		t.Fatalf("exit %d %+v\n%s", code, rep.Checks, stderr)
	}
	maxClr := 0.0
	for _, p := range rep.Isolation {
		maxClr = max(maxClr, p.ClearanceMm/0.0254)
	}
	for _, c := range rep.Nets {
		maxClr = max(maxClr, c.ClearanceMil)
	}
	wide := false
	for _, tracks := range f.fake.probes {
		for i := range tracks {
			wide = wide || tracks[i].WidthNm > 17000000
			for j := i + 1; j < len(tracks); j++ {
				a, b := tracks[i], tracks[j]
				gap := math.Abs(a.StartY-b.StartY) - float64(a.WidthNm+b.WidthNm)/2/25400
				// A clearance probe pair: two consecutive equal tracks put
				// inside a clearance on purpose.
				if j == i+1 && a.WidthNm == b.WidthNm && a.Net != b.Net && gap < maxClr+1 {
					continue
				}
				if gap < maxClr {
					t.Fatalf("probe %d and %d %.1f mil apart (< %.1f): %+v %+v", i, j, gap, maxClr, a, b)
				}
			}
		}
	}
	if !wide {
		t.Fatal("no wide probe")
	}
	if probeRowStep(100, 250) < 2*100+2*250 {
		t.Fatal("probeRowStep")
	}
}

// Rotated and round pads are judged on their own shape; a custom pad whose
// box overlaps its neighbour is not judged (a warning, never a pass).
func TestKicadRulesPadClearanceShapes(t *testing.T) {
	f := newRulesFixture(t)
	d := 7.0 / math.Sqrt2 // 45° pads 7 mil apart (< 0.2 mm): boxes overlap
	pads := fmt.Sprintf(`{"padNumber":"1","net":"SIG","layer":1,"x":1600,"y":200,"width":31.8,"height":31.8,"rotation":45,"shape":["RECT",40,5]},{"padNumber":"2","net":"AUX","layer":1,"x":%.4f,"y":%.4f,"width":31.8,"height":31.8,"rotation":45,"shape":["RECT",40,5]}`, 1600-d-5/math.Sqrt2, 200+d+5/math.Sqrt2)
	round := `{"padNumber":"1","net":"SIG","layer":1,"x":1800,"y":200,"width":20,"height":20,"shape":["ELLIPSE",20,20]},{"padNumber":"2","net":"AUX","layer":1,"x":1814.85,"y":214.85,"width":20,"height":20,"shape":["ELLIPSE",20,20]}`
	custom := `{"padNumber":"1","net":"SIG","layer":1,"x":2000,"y":200,"width":30,"height":30,"shape":["POLYGON",30,30]},{"padNumber":"2","net":"AUX","layer":1,"x":2020,"y":200,"width":30,"height":30,"shape":["POLYGON",30,30]}`
	snap := strings.Replace(rulesTestSnap, `{"designator":"R2"`, `{"designator":"U7","footprint":"Lib:ROT45","side":"top","pads":[`+pads+`]},{"designator":"U8","footprint":"Lib:ROUND","side":"top","pads":[`+round+`]},{"designator":"U9","footprint":"Lib:CUSTOM","side":"top","pads":[`+custom+`]},
  {"designator":"R2"`, 1)
	f.fake.snap = []byte(snap)
	f.o.plan = true
	rep, _, _ := runRules(t, f.o)
	c := checkOf(rep, "footprint-pad-clearance")
	items := strings.Join(c.Items, "\n")
	for _, want := range []string{"Lib:ROT45: pad gap 0.178mm", "Lib:ROUND: pad gap 0.025mm", "Lib:CUSTOM: pads 1/2 not judged"} {
		if !strings.Contains(items, want) {
			t.Fatalf("missing %q:\n%s", want, items)
		}
	}
	if c.Status != gate.Warn {
		t.Fatalf("%+v", c)
	}
}
