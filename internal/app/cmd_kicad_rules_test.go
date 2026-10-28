package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kfile"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// ── width basis ─────────────────────────────────────────────────────────────

func testIntent(t *testing.T, js string) *designIntent {
	t.Helper()
	in, err := parseDesignIntent([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func basisOf(bs []rulesNetBasis, net string) rulesNetBasis {
	for _, b := range bs {
		if b.Net == net {
			return b
		}
	}
	return rulesNetBasis{}
}

func TestPlanRulesWidths(t *testing.T) {
	in := testIntent(t, `{"nets":{
	 "VBUS":{"role":"power","currentA":0.5,"currentSource":"simulated","widthMil":{"outer":10,"inner":10,"min":10}},
	 "VIN":{"role":"power","currentA":0.6,"widthMil":{"outer":20,"inner":16,"min":12}},
	 "GND":{"role":"ground","currentA":2,"currentSource":"simulated","widthMil":{"outer":10,"inner":10,"min":10}},
	 "USB_DP":{"role":"diff","diffPair":"USB_DM","impedanceOhm":90,"widthMil":{"outer":8,"inner":8,"min":8}},
	 "USB_DM":{"role":"diff","diffPair":"USB_DP","impedanceOhm":90,"widthMil":{"outer":8,"inner":8,"min":8}},
	 "MOTOR":{"role":"power","currentA":1,"currentSource":"declared","pulsed":true,"widthMil":{"outer":30,"inner":30,"min":20}},
	 "SIG":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6}}}}`)
	cu := rulesCopper{OuterOz: 1, InnerOz: 0.5, TempRiseC: 10}
	sim := &pcbauto.SimPower{Scenario: "worst", Nets: map[string]*pcbauto.SimNet{
		"VBUS": {CurrentA: 0.5}, "GND": {CurrentA: 2}, "USB_DP": {CurrentA: 0.8}}}

	// No sim: the intent's simulated current is used; VIN (no source) is
	// cannot-check with its intent floor; the pulsed net without a peak too.
	widths, bs := planRulesWidths(in, nil, nil, cu)
	if b := basisOf(bs, "VIN"); b.Status != "cannot-check" || b.Width.OuterMil != 20 || b.Width.InnerMil != 16 {
		t.Fatalf("VIN without sim: %+v", b)
	}
	if b := basisOf(bs, "MOTOR"); b.Status != "cannot-check" || !strings.Contains(b.Detail, "pulsed") {
		t.Fatalf("MOTOR pulsed: %+v", b)
	}
	if b := basisOf(bs, "VBUS"); b.Status != "pass" || b.Current == nil || b.Current.Source != "intent" {
		t.Fatalf("VBUS from the intent current: %+v", b)
	}
	if b := basisOf(bs, "SIG"); b.Status != "pass" || b.NeedsCurrent {
		t.Fatalf("SIG: %+v", b)
	}
	_ = widths

	// With sim: GND 2 A needs more than 10 mil outer, and more on the
	// thinner inner copper; the intent floor governs VBUS.
	widths, bs = planRulesWidths(in, sim, declaredCurrents{"MOTOR": {PeakA: 3}}, cu)
	g := basisOf(bs, "GND")
	wantOuter := metricCeilMil(pcbauto.TraceWidthForCurrent(2, 10, 1, false))
	wantInner := metricCeilMil(pcbauto.TraceWidthForCurrent(2, 10, 0.5, false))
	if g.Status != "pass" || g.Width.OuterMil != wantOuter || g.Width.InnerMil != wantInner || wantInner <= wantOuter || widths["GND"].OuterMil != wantOuter {
		t.Fatalf("GND 2 A: %+v (want %g / %g)", g, wantOuter, wantInner)
	}
	if b := basisOf(bs, "VBUS"); b.Width.OuterMil != 10 || widths["VBUS"] != (rulesWidth{}) {
		t.Fatalf("VBUS: the intent governs: %+v", b)
	}
	// A declared peak resolves the pulsed net.
	if b := basisOf(bs, "MOTOR"); b.Status != "pass" || b.Current.Source != "declared" || b.Current.A != 3 {
		t.Fatalf("MOTOR declared: %+v", b)
	}
	// R8: a current width above an impedance width is incompatible.
	if b := basisOf(bs, "USB_DP"); b.Status != "fail" || !strings.Contains(b.Detail, "incompatible") || widths["USB_DP"] != (rulesWidth{}) {
		t.Fatalf("USB_DP: %+v", b)
	}
}

func TestMetricCeilMil(t *testing.T) {
	for _, c := range []struct{ in, mm float64 }{{0, 0}, {9.8, 0.25}, {9.84, 0.25}, {9.9, 0.3}, {10, 0.3}, {23.6, 0.6}} {
		got := metricCeilMil(c.in)
		if c.mm == 0 && got != 0 || c.mm > 0 && nmOfMil(got) != int64(c.mm*1e6+0.5) {
			t.Fatalf("metricCeilMil(%g) = %g mil (%d nm), want %g mm", c.in, got, nmOfMil(got), c.mm)
		}
	}
}

func TestCopperBasis(t *testing.T) {
	in := testIntent(t, `{"nets":{"A":{"widthMil":{"outer":6}}},"copper":{"outerOz":2,"innerOz":1,"tempRiseC":20}}`)
	c := copperBasis([]kicad.StackupCopper{{Layer: "F.Cu", ThicknessMm: 0.035}, {Layer: "In1.Cu", ThicknessMm: 0.0175}, {Layer: "In2.Cu", ThicknessMm: 0.0175}, {Layer: "B.Cu", ThicknessMm: 0.07}}, in)
	if c.Source != "stackup" || c.OuterOz != 1 || c.InnerOz != 0.5 || c.TempRiseC != 20 || len(c.Assumptions) != 0 {
		t.Fatalf("stackup: %+v", c)
	}
	c = copperBasis([]kicad.StackupCopper{{Layer: "F.Cu", ThicknessMm: 0}, {Layer: "B.Cu", ThicknessMm: 0}}, in)
	if c.Source != "intent" || c.OuterOz != 2 || c.InnerOz != 2 || len(c.Assumptions) != 2 {
		t.Fatalf("intent fallback (2 layers, no inner layer): %+v", c)
	}
	// Partially specified: known thicknesses stay, only the missing layers
	// fall back. A known 0.5 oz inner layer is never replaced by 1 oz.
	c = copperBasis([]kicad.StackupCopper{{Layer: "F.Cu", ThicknessMm: 0.035}, {Layer: "In1.Cu"}, {Layer: "In2.Cu", ThicknessMm: 0.0175}, {Layer: "B.Cu", ThicknessMm: 0.035}}, in)
	if c.Source != "stackup+intent" || c.OuterOz != 1 || c.InnerOz != 0.5 || len(c.Assumptions) != 1 {
		t.Fatalf("partial stackup + intent: %+v", c)
	}
	c = copperBasis([]kicad.StackupCopper{{Layer: "F.Cu", ThicknessMm: 0.07}, {Layer: "In1.Cu", ThicknessMm: 0.0175}, {Layer: "In2.Cu"}, {Layer: "B.Cu"}},
		testIntent(t, `{"nets":{"A":{"widthMil":{"outer":6}}}}`))
	if c.Source != "stackup+kicad-default" || c.OuterOz != 1 || c.InnerOz != 0.5 || len(c.Assumptions) != 3 {
		t.Fatalf("partial stackup + KiCad default: %+v", c)
	}
	c = copperBasis(nil, testIntent(t, `{"nets":{"A":{"widthMil":{"outer":6}}}}`))
	if c.Source != "kicad-default" || c.OuterOz != 1 || c.TempRiseC != 10 || len(c.Assumptions) != 2 {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestDeriveKicadRulesSharedWithRoute(t *testing.T) {
	in := testIntent(t, `{"nets":{"V":{"role":"power","widthMil":{"outer":10,"inner":8,"min":6},"clearanceMil":6}},
	 "domains":[{"id":"A","nets":["V"]},{"id":"B","nets":["X"]}],"pairs":[{"a":"A","b":"B","clearanceMm":1}]}`)
	route := deriveKicadRules(in, "", nil)
	if r := route.Reqs["V"]; r.OuterMil != 10 || r.InnerMil != 8 || r.ClearanceMil < 39 {
		t.Fatalf("route derivation: %+v", r)
	}
	// kicadRequirements is the route derivation exactly.
	if a, b := route.request(nil).Body(), (kicad.NetclassRequest{Nets: kicadRequirements(in), Isolation: kicadIsolation(in), Edges: kicadEdges("", in)}).Body(); rulesJSON(t, a) != rulesJSON(t, b) {
		t.Fatalf("shared derivation drifted:\n%s\n%s", rulesJSON(t, a), rulesJSON(t, b))
	}
	raised := deriveKicadRules(in, "", map[string]rulesWidth{"V": {OuterMil: 20, InnerMil: 4}})
	if r := raised.Reqs["V"]; r.OuterMil != 20 || r.InnerMil != 8 {
		t.Fatalf("raise never lowers: %+v", r)
	}
	if route.sha() == raised.sha() {
		t.Fatal("the derivation sha must change with the widths")
	}
	if _, ok := (kicad.NetclassRequest{}).Body()["predefinedSizes"]; ok {
		t.Fatal("route's request must not carry predefinedSizes")
	}
}

func rulesJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ── the .kicad_dru block ───────────────────────────────────────────────────

func TestRulesBlockPlacementAndFloors(t *testing.T) {
	classes := []kicad.NetclassClassNM{
		{NetClass: kicad.NetClass{Name: "PP1_W10"}, NM: kicad.ClassNM{Track: 254000, Inner: 254000, Min: 152400}},
		{NetClass: kicad.NetClass{Name: "PP2_W20"}, NM: kicad.ClassNM{Track: 508000, Inner: 600000}},
		{NetClass: kicad.NetClass{Name: "PPD_HV"}},
	}
	rules := rulesBlockRules(classes, 4)
	if len(rules) != 4 || rules[0].MinNm != 152400 || rules[2].MinNm != 508000 || rules[3].MinNm != 600000 || rules[3].Layer != "inner" {
		t.Fatalf("rules: %+v", rules)
	}
	if two := rulesBlockRules(classes, 2); len(two) != 2 {
		t.Fatalf("2-layer: no inner rules: %+v", two)
	}
	owned := kicad.SizeSet{Tracks: []int64{254000, 600000}, Vias: [][2]int64{{600000, 300000}}}
	block := rulesBlockText(rules, "abc123", owned)
	route := kicad.DRURouteBegin + "\n(rule \"pcbpilot PP2_W20 inner\"\n  (layer inner)\n  (condition \"A.hasNetclass('PP2_W20')\")\n  (constraint track_width (min 0.15mm) (opt 0.6mm)))\n" + kicad.DRURouteEnd + "\n"
	user := "(version 1)\n(rule \"mine\"\n  (condition \"A.NetName == 'X'\")\n  (constraint clearance (min 1mm)))\n"
	text, err := placeRulesBlock(user+route, block)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(text, "mine") > strings.Index(text, kicad.DRURouteBegin) || strings.Index(text, kicad.DRURouteBegin) > strings.Index(text, kicad.DRURulesBegin) {
		t.Fatalf("order user → route block → rules block:\n%s", text)
	}
	// Idempotent: placing the same block again changes nothing.
	again, _ := placeRulesBlock(text, block)
	if again != text {
		t.Fatalf("not idempotent:\n%s\n---\n%s", text, again)
	}
	// KiCad's Python rewrites the file with CRLF on Windows: still the same.
	if crlf, _ := placeRulesBlock(strings.ReplaceAll(text, "\n", "\r\n"), block); crlf != text {
		t.Fatalf("CRLF input changed the result:\n%q", crlf)
	}
	st, ok := parseRulesStamp(text)
	if !ok || st.RequestSHA != "abc123" || len(st.Owned.Tracks) != 2 || st.Owned.Vias[0] != [2]int64{600000, 300000} {
		t.Fatalf("stamp: %+v %v", st, ok)
	}
	parsed, err := kicad.ParseDRU(text)
	if err != nil {
		t.Fatal(err)
	}
	// KiCad applies the last matching rule: the route block's weaker inner
	// rule (0.15 mm) must never override the class's floor (0.6 mm).
	if v, name := rulesTrackFloor(parsed, "PP2_W20", "inner"); v != expectedFloor(classes[1].NM, "inner") || !strings.HasPrefix(name, kicad.DRURulesPrefix) {
		t.Fatalf("inner floor %d from %s, want %d from the rules block", v, name, expectedFloor(classes[1].NM, "inner"))
	}
	// A route run re-appends its block at the end; moving the rules block
	// back restores the order.
	dir := t.TempDir()
	pcb := filepath.Join(dir, "b.kicad_pcb")
	swapped := strings.Replace(text, route, "", 1) + route
	if err := os.WriteFile(boardSibling(pcb, ".kicad_dru"), []byte(strings.ReplaceAll(swapped, "\n", "\r\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := moveRulesBlockLast(pcb); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(boardSibling(pcb, ".kicad_dru")); string(b) != text {
		t.Fatalf("moveRulesBlockLast:\n%s", b)
	}
	if v, _ := rulesTrackFloor(parsed, "PP1_W10", "outer"); v != 152400 {
		t.Fatalf("outer floor %d", v)
	}
	if v, _ := rulesTrackFloor(parsed, "Nope", "outer"); v != 0 {
		t.Fatal("unknown class has no floor")
	}
	// A user rule of the same shape is not pcbpilot's and never counts.
	fake := "(rule \"x\"\n  (condition \"A.hasNetclass('PP1_W10')\")\n  (constraint track_width (min 9mm)))\n"
	parsed, _ = kicad.ParseDRU(text + fake)
	if v, _ := rulesTrackFloor(parsed, "PP1_W10", "outer"); v != 152400 {
		t.Fatalf("user rule counted: %d", v)
	}
}

func TestRemoveUserDRURules(t *testing.T) {
	text := "(version 1)\n(rule \"keep\"\n  (constraint clearance (min 1mm)))\n(rule \"drop me\"\n  (condition \"A.NetName == '(x)'\")\n  (constraint track_width (min 2mm)))\n" +
		kicad.DRURouteBegin + "\n(rule \"drop me\"\n  (constraint clearance (min 1mm)))\n" + kicad.DRURouteEnd + "\n"
	out, removed, err := removeUserDRURules(text, []string{"drop me"})
	if err != nil || len(removed) != 1 || strings.Count(out, "drop me") != 1 || !strings.Contains(out, "keep") || !strings.Contains(out, kicad.DRURouteBegin) {
		t.Fatalf("%v %v\n%s", err, removed, out)
	}
}

// ── flows on the fake KiCad ────────────────────────────────────────────────

const rulesTestBoard = `(kicad_pcb (version 20241229) (generator "pcbnew")
  (setup (stackup (layer "F.Cu" (type "copper") (thickness 0.035)) (layer "dielectric 1" (type "core") (thickness 1.51)) (layer "B.Cu" (type "copper") (thickness 0.035)))))
`

const rulesTestIntent = `{"nets":{
 "VCC":{"role":"power","domain":"LV","currentA":0.5,"currentSource":"simulated","widthMil":{"outer":10,"inner":10,"min":8},"clearanceMil":6},
 "GND":{"role":"ground","domain":"LV","currentA":0.5,"currentSource":"simulated","widthMil":{"outer":10,"inner":10,"min":8},"clearanceMil":6},
 "HV":{"role":"power","domain":"HVD","currentA":0.1,"currentSource":"declared","widthMil":{"outer":12,"inner":12,"min":12},"clearanceMil":20},
 "SIG":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6},"clearanceMil":6}},
 "domains":[{"id":"LV","nets":["VCC","GND"]},{"id":"HVD","nets":["HV"]}],
 "pairs":[{"a":"HVD","b":"LV","clearanceMm":2,"creepageMm":3,"bridges":["U9"]}],
 "copper":{"outerOz":1,"innerOz":1,"tempRiseC":10}}`

const rulesTestSim = `{"schemaVersion":1,"results":[{"scenario":"worst","nets":{"VCC":{"currentA":1.5,"role":"power"},"GND":{"currentA":1.5,"role":"ground"},"HV":{"currentA":0.1,"role":"power"}}}]}`

// rulesTestSnap: two parts per domain, routed copper (a 6 mil VCC track
// under its plan), a via, courtyards 1 mm apart across the barrier.
const rulesTestSnap = `{"source":"kicad","copperLayers":2,"outline":{"bbox":{"minX":0,"minY":0,"maxX":2000,"maxY":1000}},
 "components":[
  {"designator":"J1","side":"top","courtyard":{"minX":100,"minY":100,"maxX":300,"maxY":300},"pads":[{"net":"VCC","x":200,"y":200,"width":60,"height":60},{"net":"GND","x":250,"y":200,"width":60,"height":60}]},
  {"designator":"U1","side":"top","courtyard":{"minX":339.37,"minY":100,"maxX":600,"maxY":300},"pads":[{"net":"HV","x":400,"y":200,"width":60,"height":60},{"net":"HV","x":500,"y":200,"width":60,"height":60}]},
  {"designator":"R1","side":"top","courtyard":{"minX":800,"minY":100,"maxX":900,"maxY":300},"pads":[{"net":"VCC","x":850,"y":200,"width":40,"height":40},{"net":"SIG","x":850,"y":250,"width":40,"height":40}]},
  {"designator":"R2","side":"top","courtyard":{"minX":950,"minY":100,"maxX":1000,"maxY":300},"pads":[{"net":"SIG","x":970,"y":200,"width":40,"height":40},{"net":"GND","x":970,"y":250,"width":40,"height":40}]}],
 "copper":{"lines":[
   {"primitiveId":"t-vcc","net":"VCC","layer":1,"startX":200,"startY":200,"endX":850,"endY":200,"lineWidth":6},
   {"primitiveId":"t-gnd","net":"GND","layer":2,"startX":250,"startY":200,"endX":970,"endY":250,"lineWidth":40},
   {"primitiveId":"t-hv","net":"HV","layer":1,"startX":400,"startY":200,"endX":500,"endY":200,"lineWidth":30},
   {"primitiveId":"t-sig","net":"SIG","layer":1,"startX":850,"startY":250,"endX":970,"endY":200,"lineWidth":10}],
  "arcs":[],"vias":[{"primitiveId":"v1","net":"VCC","x":600,"y":200,"diameter":10,"holeDiameter":5}],"poured":[]}}`

type rulesFixture struct {
	dir  string
	o    kicadRulesOpts
	fake *rulesFakeKiCad
}

func newRulesFixture(t *testing.T) *rulesFixture {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	pcb := write("b.kicad_pcb", rulesTestBoard)
	if err := rulesFakeWrite(boardSibling(pcb, ".kicad_pro"), rulesFakeDefault()); err != nil {
		t.Fatal(err)
	}
	fake := &rulesFakeKiCad{t: t, snap: []byte(rulesTestSnap), nets: []string{"AUX", "GND", "HV", "SIG", "VCC"}, layers: 2}
	o := kicadRulesOpts{pcb: pcb, intent: write("intent.json", rulesTestIntent), sim: write("sim.json", rulesTestSim), backend: fake, stage: "routed",
		env: &kfile.Env{Host: "HOST", User: "user", Procs: func() ([]kfile.Proc, error) { return nil, nil }}}
	return &rulesFixture{dir: dir, o: o, fake: fake}
}

func runRules(t *testing.T, o kicadRulesOpts) (*rulesReport, int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runKicadRules(o, &stdout, &stderr)
	code := 0
	var ec exitCodeError
	if errors.As(err, &ec) {
		code = ec.code
	} else if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var rep rulesReport
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
			t.Fatalf("stdout is not a report: %v\n%s", err, stdout.String())
		}
	}
	return &rep, code, stderr.String()
}

func checkOf(rep *rulesReport, name string) rulesCheck {
	for _, c := range rep.Checks {
		if c.Name == name {
			return c
		}
	}
	return rulesCheck{}
}

func netOf(rep *rulesReport, net string) rulesNetPlan {
	for _, n := range rep.Nets {
		if n.Net == net {
			return n
		}
	}
	return rulesNetPlan{}
}

func rulesFileSHA(t *testing.T, p string) string {
	t.Helper()
	sn, err := kfile.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return sn.SHA256
}

func TestKicadRulesPlan(t *testing.T) {
	f := newRulesFixture(t)
	before := rulesFileSHA(t, boardSibling(f.o.pcb, ".kicad_pro"))
	f.o.plan = true
	f.o.planOut = filepath.Join(f.dir, "plan.json")
	rep, code, stderr := runRules(t, f.o)
	if code != 0 || rep.Verdict != gate.Pass || !rep.OK {
		t.Fatalf("plan: exit %d %s %+v\n%s", code, rep.Verdict, rep.Checks, stderr)
	}
	v := netOf(rep, "VCC")
	want := metricCeilMil(pcbauto.TraceWidthForCurrent(1.5, 10, 1, false))
	if v.Width.OuterMil != want || nmOfMil(v.OuterMil) != nmOfMil(want) || v.Current == nil || v.Current.Source != "sim" {
		t.Fatalf("VCC: %+v (want %g)", v, want)
	}
	if v.ClearanceMil < 78 { // the 2 mm pair clearance on every net of the pair
		t.Fatalf("VCC clearance %g", v.ClearanceMil)
	}
	if s := netOf(rep, "SIG"); s.OuterMil < milOfNm(200000)-0.01 { // Default's 0.2 mm kept (never lowered)
		t.Fatalf("SIG kept at its class width: %+v", s)
	}
	if rulesFileSHA(t, boardSibling(f.o.pcb, ".kicad_pro")) != before || fileExists(boardSibling(f.o.pcb, ".kicad_dru")) {
		t.Fatal("--plan wrote the project")
	}
	// Two DRC runs: the probes with the rules, and the negative control.
	if c := checkOf(rep, "staged-readback"); c.Status != gate.Pass || f.fake.drcCalls != 2 {
		t.Fatalf("staged readback: %+v (DRC runs %d)", c, f.fake.drcCalls)
	}
	var pf rulesPlanFile
	b, _ := os.ReadFile(f.o.planOut)
	if err := json.Unmarshal(b, &pf); err != nil || pf.Revision != rep.Revision || pf.Revision == "" {
		t.Fatalf("plan file: %v %s / %s", err, pf.Revision, rep.Revision)
	}
}

func TestKicadRulesApplyReadbackAndIdempotent(t *testing.T) {
	f := newRulesFixture(t)
	f.o.apply = true
	rep, code, stderr := runRules(t, f.o)
	if code != 0 || !rep.Committed || len(rep.Files) != 2 || checkOf(rep, "readback").Status != gate.Pass {
		t.Fatalf("apply: exit %d committed %v files %v %+v\n%s", code, rep.Committed, rep.Files, rep.Checks, stderr)
	}
	dru, _ := os.ReadFile(boardSibling(f.o.pcb, ".kicad_dru"))
	if !strings.Contains(string(dru), kicad.DRURulesBegin) || !strings.Contains(string(dru), "pcbpilot isolation PPD_HVD|PPD_LV") {
		t.Fatalf("dru:\n%s", dru)
	}
	if st, ok := parseRulesStamp(string(dru)); !ok || st.RequestSHA != rep.RequestSHA {
		t.Fatalf("stamp %+v", st)
	}
	pro := rulesFakeRead(f.o.pcb)
	if len(pro.Sizes.Tracks) == 0 {
		t.Fatal("no pre-defined sizes written")
	}
	// Re-apply: the same inputs give the same files — nothing written.
	sum := rulesFileSHA(t, boardSibling(f.o.pcb, ".kicad_dru")) + rulesFileSHA(t, boardSibling(f.o.pcb, ".kicad_pro"))
	rep, code, stderr = runRules(t, f.o)
	if code != 0 || len(rep.Files) != 0 || rulesFileSHA(t, boardSibling(f.o.pcb, ".kicad_dru"))+rulesFileSHA(t, boardSibling(f.o.pcb, ".kicad_pro")) != sum {
		t.Fatalf("re-apply: exit %d files %v\n%s", code, rep.Files, stderr)
	}
	// A stale owned size is removed when the plan no longer needs it.
	in := strings.Replace(rulesTestIntent, `"outer":12,"inner":12,"min":12`, `"outer":16,"inner":16,"min":12`, 1)
	if err := os.WriteFile(f.o.intent, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, code, _ = runRules(t, f.o)
	pro = rulesFakeRead(f.o.pcb)
	if code != 0 || !rep.Committed || rulesHasInt(pro.Sizes.Tracks, nmOfMil(12)) || !rulesHasInt(pro.Sizes.Tracks, nmOfMil(16)) {
		t.Fatalf("re-plan: exit %d sizes %v", code, pro.Sizes.Tracks)
	}
}

func rulesHasInt(xs []int64, v int64) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func TestKicadRulesReadbackMismatchRollsBack(t *testing.T) {
	f := newRulesFixture(t)
	pro := boardSibling(f.o.pcb, ".kicad_pro")
	before := rulesFileSHA(t, pro)
	f.o.apply = true
	f.o.beforeReadback = func() {
		// Something between the commit and KiCad's readback lowers a class.
		p := rulesFakeRead(f.o.pcb)
		for i := range p.Classes {
			if strings.HasPrefix(p.Classes[i].Name, "PP") && p.Classes[i].Track != nil {
				p.Classes[i].Track = rulesI64(100000)
			}
		}
		_ = rulesFakeWrite(pro, p)
	}
	rep, code, _ := runRules(t, f.o)
	// The tampered project is not what this run wrote: the rollback refuses
	// to overwrite it (kfile base check) — a rollback failure, exit 1.
	if code != 1 || rep.Errors[len(rep.Errors)-1].Code != errRulesRollback {
		t.Fatalf("tampered: exit %d %+v", code, rep.Errors)
	}

	// A readback that differs while the files are exactly what was written
	// (KiCad resolving differently) rolls back cleanly.
	f = newRulesFixture(t)
	pro = boardSibling(f.o.pcb, ".kicad_pro")
	before = rulesFileSHA(t, pro)
	f.o.apply = true
	f.o.beforeReadback = func() { f.fake.ignoreDRU = true }
	rep, code, _ = runRules(t, f.o)
	if code != 6 || !rep.RolledBack || rep.Committed || checkOf(rep, "readback").Status != gate.Fail {
		t.Fatalf("readback mismatch: exit %d rolledBack %v %+v", code, rep.RolledBack, rep.Checks)
	}
	if rulesFileSHA(t, pro) != before || fileExists(boardSibling(f.o.pcb, ".kicad_dru")) {
		t.Fatal("rollback did not restore the project / remove the created rules file")
	}
}

func TestKicadRulesStagedDRUIgnoredFails(t *testing.T) {
	f := newRulesFixture(t)
	f.fake.ignoreDRU = true // KiCad would not apply the .kicad_dru
	f.o.apply = true
	rep, code, _ := runRules(t, f.o)
	if code != 6 || rep.Committed || checkOf(rep, "staged-readback").Status != gate.Fail || fileExists(boardSibling(f.o.pcb, ".kicad_dru")) {
		t.Fatalf("exit %d %+v", code, checkOf(rep, "staged-readback"))
	}
}

func TestKicadRulesUserConflicts(t *testing.T) {
	f := newRulesFixture(t)
	p := rulesFakeDefault()
	p.Classes = []rulesFakeClass{{Name: "POWER", Priority: 0, Track: rulesI64(150000), Clearance: rulesI64(150000)}, {Name: "PP7_mine", Priority: 1}, {Name: "KEEP", Priority: 2, Track: rulesI64(900000)}}
	p.Patterns = [][2]string{{"VCC", "POWER"}, {"SIG", "KEEP"}}
	pro := boardSibling(f.o.pcb, ".kicad_pro")
	if err := rulesFakeWrite(pro, p); err != nil {
		t.Fatal(err)
	}
	dru := boardSibling(f.o.pcb, ".kicad_dru")
	if err := os.WriteFile(dru, []byte("(version 1)\n(rule \"wide vcc\"\n  (condition \"A.NetName == 'VCC'\")\n  (constraint track_width (min 2mm)))\n(rule \"other\"\n  (condition \"A.NetName == 'X'\")\n  (constraint track_width (min 2mm)))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := rulesFileSHA(t, pro) + rulesFileSHA(t, dru)
	f.o.apply = true
	rep, code, _ := runRules(t, f.o)
	names := map[string]string{}
	for _, c := range rep.Conflicts {
		names[c.Name] = c.Kind
	}
	if code != 6 || rep.Committed || names["POWER"] != "class-override" || names["PP7_mine"] != "class-name" || names["wide vcc"] != "dru-weakened" || names["other"] != "" || names["KEEP"] != "" {
		t.Fatalf("conflicts: exit %d %+v", code, rep.Conflicts)
	}
	if rulesFileSHA(t, pro)+rulesFileSHA(t, dru) != before {
		t.Fatal("a refused apply wrote files")
	}
	if c := checkOf(rep, "user-rules"); c.Status != gate.Fail {
		t.Fatalf("user-rules check: %+v", c)
	}
	// --replace by name: the named user rule leaves the .kicad_dru, the
	// rest stays.
	f.o.replace = []string{"POWER", "PP7_mine", "wide vcc"}
	rep, code, stderr := runRules(t, f.o)
	if code != 0 || !rep.Committed {
		t.Fatalf("replace: exit %d %+v\n%s", code, rep.Errors, stderr)
	}
	text, _ := os.ReadFile(dru)
	if strings.Contains(string(text), "wide vcc") || !strings.Contains(string(text), "\"other\"") {
		t.Fatalf("dru after replace:\n%s", text)
	}
	np := rulesFakeRead(f.o.pcb)
	keep := false
	for _, c := range np.Classes {
		keep = keep || c.Name == "KEEP"
	}
	if !keep {
		t.Fatal("a non-conflicting user class was removed")
	}
	if s := netOf(rep, "SIG"); nmOfMil(s.OuterMil) != 900000 {
		t.Fatalf("the user's wider class value is kept: %+v", s)
	}
}

func TestKicadRulesIncompletePlanRefused(t *testing.T) {
	f := newRulesFixture(t)
	f.o.sim = ""
	in := strings.Replace(rulesTestIntent, `"currentA":0.5,"currentSource":"simulated"`, `"currentA":0.5`, 1)
	if err := os.WriteFile(f.o.intent, []byte(in), 0o644); err != nil {
		t.Fatal(err)
	}
	f.o.plan = true
	rep, code, _ := runRules(t, f.o)
	if code != 5 || rep.Verdict != gate.CannotCheck || checkOf(rep, "plan-coverage").Status != gate.CannotCheck {
		t.Fatalf("plan: exit %d %s", code, rep.Verdict)
	}
	if v := netOf(rep, "VCC"); v.Status != "cannot-check" || v.OuterMil < 10-reqEpsMil {
		t.Fatalf("VCC keeps its intent floor: %+v", v)
	}
	f.o.plan, f.o.apply = false, true
	rep, code, _ = runRules(t, f.o)
	if code != 5 || rep.Committed || rep.Errors[len(rep.Errors)-1].Code != errRulesIncomplete || fileExists(boardSibling(f.o.pcb, ".kicad_dru")) {
		t.Fatalf("apply: exit %d %+v", code, rep.Errors)
	}
}

func TestKicadRulesIncompatibleFails(t *testing.T) {
	f := newRulesFixture(t)
	in := strings.Replace(rulesTestIntent, `"SIG":{"role":"signal"`, `"SIG":{"role":"signal","impedanceOhm":50`, 1)
	sim := strings.Replace(rulesTestSim, `"HV":{`, `"SIG":{"currentA":2,"role":"signal"},"HV":{`, 1)
	_ = os.WriteFile(f.o.intent, []byte(in), 0o644)
	_ = os.WriteFile(f.o.sim, []byte(sim), 0o644)
	f.o.apply = true
	rep, code, _ := runRules(t, f.o)
	if code != 6 || rep.Committed || checkOf(rep, "plan-coverage").Status != gate.Fail {
		t.Fatalf("exit %d %+v", code, checkOf(rep, "plan-coverage"))
	}
}

func TestKicadRulesExitCodes(t *testing.T) {
	// Usage: exit 1, nothing on stdout.
	f := newRulesFixture(t)
	for _, o := range []kicadRulesOpts{
		{pcb: f.o.pcb, intent: f.o.intent},                                            // no mode
		{pcb: f.o.pcb, intent: f.o.intent, plan: true, apply: true},                   // two modes
		{pcb: f.o.pcb, plan: true},                                                    // no intent
		{pcb: f.o.pcb, intent: f.o.intent, apply: true, planOut: "x.json"},            // --plan-out without --plan
		{pcb: f.o.pcb, intent: f.o.intent, applyPlan: "p.json"},                       // no --revision
		{pcb: f.o.pcb, intent: f.o.intent, plan: true, report: f.o.intent},            // report over an input
		{pcb: f.o.pcb, intent: f.o.intent, plan: true, report: "r.kicad_dru"},         // KiCad extension
		{pcb: f.o.pcb, intent: f.o.intent, check: true, stage: "half"},                // bad --stage
		{pcb: filepath.Join(f.dir, "none.kicad_pcb"), intent: f.o.intent, plan: true}, // missing board
	} {
		if o.stage == "" {
			o.stage = "routed"
		}
		o.backend = f.fake
		var out, errb bytes.Buffer
		err := runKicadRules(o, &out, &errb)
		var ec exitCodeError
		if !errors.As(err, &ec) || ec.code != 1 || out.Len() != 0 || errb.Len() == 0 {
			t.Fatalf("%+v: %v stdout %q", o, err, out.String())
		}
	}

	// KiCad has the project open: KICAD_OPEN, exit 4, nothing written.
	f = newRulesFixture(t)
	if err := os.WriteFile(filepath.Join(f.dir, "~b.kicad_pro.lck"), []byte(`{"hostname":"HOST","username":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.o.env.Procs = func() ([]kfile.Proc, error) { return []kfile.Proc{{PID: 9, Name: "kicad.exe"}}, nil }
	f.o.apply = true
	rep, code, _ := runRules(t, f.o)
	if code != 4 || rep.Errors[0].Code != errRulesKiCadOpen || fileExists(boardSibling(f.o.pcb, ".kicad_dru")) {
		t.Fatalf("locked: exit %d %+v", code, rep.Errors)
	}
	// A read-only run only warns.
	f.o.apply, f.o.plan = false, true
	if rep, code, _ = runRules(t, f.o); code != 0 || len(rep.Warnings) == 0 || !strings.Contains(strings.Join(rep.Warnings, " "), "sourceOpenInKiCad") {
		t.Fatalf("read-only beside an open project: exit %d %v", code, rep.Warnings)
	}

	// An input changed during the run: E_FILE_CHANGED, exit 4.
	f = newRulesFixture(t)
	f.o.apply = true
	f.o.beforeCommit = func() { _ = os.WriteFile(f.o.intent, []byte(rulesTestIntent+" "), 0o644) }
	rep, code, _ = runRules(t, f.o)
	if code != 4 || rep.Errors[0].Code != string(kfile.CodeChanged) || rep.Committed {
		t.Fatalf("changed: exit %d %+v", code, rep.Errors)
	}

	// --apply-plan: the revision must be the plan's and the files'.
	f = newRulesFixture(t)
	f.o.plan, f.o.planOut = true, filepath.Join(f.dir, "plan.json")
	plan, code, _ := runRules(t, f.o)
	if code != 0 {
		t.Fatal(code)
	}
	ap := f.o
	ap.plan, ap.planOut, ap.applyPlan, ap.revision = false, "", f.o.planOut, "deadbeef"
	if rep, code, _ = runRules(t, ap); code != 4 || rep.Errors[0].Code != string(kfile.CodeRevision) {
		t.Fatalf("wrong revision: exit %d %+v", code, rep.Errors)
	}
	_ = os.WriteFile(f.o.sim, []byte(strings.Replace(rulesTestSim, "1.5", "1.6", 2)), 0o644)
	ap.revision = plan.Revision
	if rep, code, _ = runRules(t, ap); code != 4 || rep.Errors[0].Code != string(kfile.CodeRevision) || rep.Committed {
		t.Fatalf("files changed since the plan: exit %d %+v", code, rep.Errors)
	}
	_ = os.WriteFile(f.o.sim, []byte(rulesTestSim), 0o644)
	if rep, code, _ = runRules(t, ap); code != 0 || !rep.Committed {
		t.Fatalf("apply-plan: exit %d %+v", code, rep.Errors)
	}

	// A KiCad tool failure is exit 1; a DRC that cannot run is cannot-check.
	f = newRulesFixture(t)
	f.fake.readErr = errors.New("pcbnew crashed")
	f.o.plan = true
	if rep, code, _ = runRules(t, f.o); code != 1 || rep.Errors[0].Code != errRulesTool {
		t.Fatalf("tool failure: exit %d %+v", code, rep.Errors)
	}
	f = newRulesFixture(t)
	f.fake.drcErr = errors.New("kicad-cli: no report")
	f.o.plan = true
	if rep, code, _ = runRules(t, f.o); code != 5 || checkOf(rep, "staged-readback").Status != gate.CannotCheck {
		t.Fatalf("DRC cannot run: exit %d %+v", code, rep.Checks)
	}
}

func TestKicadRulesCheck(t *testing.T) {
	f := newRulesFixture(t)
	// Before apply: the board's classes are below the plan and it has no
	// track_width minimum rules.
	f.o.check = true
	f.o.report = filepath.Join(f.dir, "check.json")
	rep, code, _ := runRules(t, f.o)
	if code != 6 || checkOf(rep, "effective-rules").Status != gate.Fail || !fileExists(f.o.report) {
		t.Fatalf("unapplied: exit %d %+v", code, checkOf(rep, "effective-rules"))
	}
	if c := checkOf(rep, "effective-rules"); !strings.Contains(strings.Join(c.Items, "\n"), "no track_width minimum rule") {
		t.Fatalf("%+v", c.Items)
	}
	// After apply: the rules pass; the 6 mil VCC track and the small via fail;
	// the courtyards 1 mm apart across a 2 mm barrier are an advisory warning.
	a := f.o
	a.check, a.apply, a.report = false, true, ""
	if _, code, _ = runRules(t, a); code != 0 {
		t.Fatal(code)
	}
	rep, code, _ = runRules(t, f.o)
	if checkOf(rep, "effective-rules").Status != gate.Pass {
		t.Fatalf("applied: %+v", checkOf(rep, "effective-rules"))
	}
	cw := checkOf(rep, "copper-widths")
	if code != 6 || cw.Status != gate.Fail || !strings.Contains(strings.Join(cw.Items, "\n"), "t-vcc") || strings.Contains(strings.Join(cw.Items, "\n"), "t-gnd") {
		t.Fatalf("copper: exit %d %+v", code, cw)
	}
	if c := checkOf(rep, "via-size"); c.Status != gate.Fail {
		t.Fatalf("via-size: %+v", c)
	}
	pp := checkOf(rep, "isolation-placement")
	if !pp.Advisory || pp.Status != gate.Warn || !strings.Contains(strings.Join(pp.Items, "\n"), "J1 ↔ U1") || !strings.Contains(pp.Detail, "not copper clearance") {
		t.Fatalf("placement proxy: %+v", pp)
	}
	// The 6 mil VCC track is below the 8 mil floor: a neck-down never
	// excuses that. At 9 mil (above the floor, below the 21.65 mil width) an
	// intent neck-down at J1 covers it; without one it fails.
	if c := checkOf(rep, "copper-widths"); !strings.Contains(strings.Join(c.Items, "\n"), "below the floor") {
		t.Fatalf("6 mil: %+v", c)
	}
	f.fake.snap = []byte(strings.Replace(rulesTestSnap, `"lineWidth":6}`, `"lineWidth":9}`, 1))
	rep, _, _ = runRules(t, f.o)
	if c := checkOf(rep, "copper-widths"); c.Status != gate.Fail || !strings.Contains(strings.Join(c.Items, "\n"), "outside any intent neck-down") {
		t.Fatalf("9 mil, no neck-down: %+v", c)
	}
	in := strings.Replace(rulesTestIntent, `"VCC":{"role":"power"`, `"VCC":{"neckdown":[{"refs":["J1"],"maxLengthMil":700,"minWidthMil":8}],"role":"power"`, 1)
	_ = os.WriteFile(f.o.intent, []byte(in), 0o644)
	if _, code, _ = runRules(t, a); code != 0 {
		t.Fatal(code)
	}
	rep, _, _ = runRules(t, f.o)
	if c := checkOf(rep, "copper-widths"); strings.Contains(strings.Join(c.Items, "\n"), "t-vcc") || !strings.Contains(strings.Join(c.Info, "\n"), "intent neck-down at J1") {
		t.Fatalf("9 mil with a neck-down: %+v", c)
	}
	// --stage placed: copper checks do not apply; an unrouted board's nets
	// are not-routed (cannot-check) when it is judged as routed.
	f.fake.snap = []byte(strings.Replace(rulesTestSnap, `"lines":[`, `"lines":[],"x":[`, 1))
	f.o.stage = "placed"
	rep, _, _ = runRules(t, f.o)
	if checkOf(rep, "copper-widths").Name != "" {
		t.Fatalf("placed: copper judged: %+v", rep.Checks)
	}
	f.o.stage = "routed"
	rep, code, _ = runRules(t, f.o)
	if c := checkOf(rep, "copper-widths"); c.Status != gate.CannotCheck || !strings.Contains(strings.Join(c.Items, "\n"), "not-routed") || code == 0 {
		t.Fatalf("unrouted: exit %d %+v", code, c)
	}
}

func TestRulesSegLength(t *testing.T) {
	mx, my := 0.0, 100.0
	arc := rulesSeg{X1: -100, Y1: 0, X2: 100, Y2: 0, MidX: &mx, MidY: &my, ArcDeg: -180}
	if l := arc.length(); l < 314 || l > 315 {
		t.Fatalf("half circle r=100: %g", l)
	}
	if l := (rulesSeg{X1: 0, Y1: 0, X2: 3, Y2: 4}).length(); l != 5 {
		t.Fatal(l)
	}
}

// TestRouteRulesStamp: kicad route checks rules applied by kicad rules. No
// rules block: route's own derivation, unchanged. A block from the same
// inputs: route uses kicad rules' derivation. A stale block: route stops.
func TestRouteRulesStamp(t *testing.T) {
	f := newRulesFixture(t)
	in, err := loadDesignIntent(f.o.intent)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	r := &kicadRun{o: kicadRouteOpts{pcb: f.o.pcb, intent: f.o.intent, sim: f.o.sim}, in: in, stderr: &stderr, summary: map[string]any{}}
	own := deriveKicadRules(in, f.o.intent, nil)
	got, err := r.rulesStamp(own, nil)
	if err != nil || got.sha() != own.sha() || r.summary["kicadRules"] != nil {
		t.Fatalf("no rules block: %v", err)
	}
	a := f.o
	a.apply = true
	if _, code, se := runRules(t, a); code != 0 {
		t.Fatalf("apply: %d %s", code, se)
	}
	got, err = r.rulesStamp(own, nil)
	if err != nil || got.sha() == own.sha() || got.Reqs["VCC"].OuterMil <= own.Reqs["VCC"].OuterMil {
		t.Fatalf("matching rules: %v (sha %s vs %s)", err, got.sha(), own.sha())
	}
	// Route without the sim the rules were made with: stale.
	r.o.sim = ""
	if _, err = r.rulesStamp(own, nil); err == nil || !strings.Contains(err.Error(), "RULES_STALE") || !strings.Contains(err.Error(), "rerun") {
		t.Fatalf("stale: %v", err)
	}
}

// TestPlanRulesWidthsRepoIntents runs the width basis on the repo's intent
// testdata. The esp32-v05 intent was derived from its own sim.json: with
// that sim no width rises above the intent (route and rules agree), every
// net has a verified basis. The hand-written esp32-mini intent records no
// current source: without a sim its power nets are cannot-check.
func TestPlanRulesWidthsRepoIntents(t *testing.T) {
	in, err := loadDesignIntent(filepath.Join("testdata", "esp32-v05", "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "esp32-v05", "sim.json"))
	if err != nil {
		t.Fatal(err)
	}
	sp, err := pcbauto.ParseSim(raw)
	if err != nil {
		t.Fatal(err)
	}
	cu := copperBasis([]kicad.StackupCopper{{Layer: "F.Cu", ThicknessMm: 0.035}, {Layer: "In1.Cu", ThicknessMm: 0.0175}, {Layer: "In2.Cu", ThicknessMm: 0.0175}, {Layer: "B.Cu", ThicknessMm: 0.035}}, in)
	widths, bs := planRulesWidths(in, sp, nil, cu)
	if len(bs) != len(in.Nets) {
		t.Fatalf("%d bases for %d nets", len(bs), len(in.Nets))
	}
	for _, b := range bs {
		if b.Status != "pass" {
			t.Errorf("%s: %s %s", b.Net, b.Status, b.Detail)
		}
	}
	for n, w := range widths {
		t.Errorf("%s raised to %+v: the intent was derived from this sim", n, w)
	}
	if b := basisOf(bs, "+3V3"); b.Current == nil || b.Current.Source != "sim" || b.CurrentWidth == nil {
		t.Fatalf("+3V3: %+v", b)
	}

	mini, err := loadDesignIntent(filepath.Join("testdata", "intent", "esp32-mini.intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, bs = planRulesWidths(mini, nil, nil, copperBasis(nil, mini))
	for _, b := range bs {
		power := b.Role == "power" || b.Role == "ground" || b.Role == "switch"
		if power != (b.Status == "cannot-check") {
			t.Errorf("%s (%s): %s", b.Net, b.Role, b.Status)
		}
	}
}
