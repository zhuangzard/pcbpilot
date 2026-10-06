package app

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

func TestCheckIntentWidths(t *testing.T) {
	reqs := map[string]specctra.NetRequirement{
		"+12V": {OuterMil: 21.65, InnerMil: 41.34, MinMil: 21.65},
		"GND":  {OuterMil: 21.65, InnerMil: 43.31, MinMil: 10},
	}
	pads := []boardPad{{Net: "GND", Layer: 1, X: 0, Y: 0, W: 11, H: 59}}
	tracks := []specctra.Track{
		{ID: "ok-outer", Net: "+12V", Layer: 1, X1: 1000, X2: 2000, Width: 21.65},
		{ID: "inner-thin", Net: "+12V", Layer: 15, X1: 1000, X2: 2000, Width: 21.65},           // 21.65 < 41.34 inner, below min? no: min 21.65 -> away from pins
		{ID: "neck-at-pin", Net: "GND", Layer: 1, X1: 0, Y1: 20, X2: 60, Y2: 20, Width: 11.02}, // starts on the pad
		{ID: "neck-far", Net: "GND", Layer: 1, X1: 500, Y1: 500, X2: 600, Y2: 500, Width: 11.02},
		{ID: "below-min", Net: "GND", Layer: 1, X1: 0, Y1: 20, X2: 30, Y2: 20, Width: 6},
		{ID: "unconstrained", Net: "SIG", Layer: 1, Width: 4},
	}
	vs := checkIntentWidths(tracks, pads, reqs, nil)
	got := map[string]string{}
	for _, v := range vs {
		got[v.ID] = v.Reason
	}
	if len(vs) != 3 || got["inner-thin"] == "" || got["neck-far"] == "" || !strings.Contains(got["below-min"], "minimum") {
		t.Fatalf("violations = %+v", vs)
	}
	if lines := summarizeWidthViolations(vs); len(lines) != 2 || !strings.HasPrefix(lines[1], "GND: 2 track(s), worst 6.00") {
		t.Fatalf("summary = %v", lines)
	}
}

func TestApplyWaivers(t *testing.T) {
	ws := []gateWaiver{{Gate: "post-layout-sim", Match: "SV1_DRV", Reason: "drain path inside 2 % of 12 V", By: "taisen 2026-10-06"}}
	g := gateResult{Gate: "post-layout-sim", Items: []string{"ir-drop SV1_DRV 158 mV > 30 mV"}}
	applyWaivers(&g, ws)
	if !g.Pass || len(g.Waived) != 1 {
		t.Fatalf("fully waived gate = %+v", g)
	}
	g = gateResult{Gate: "post-layout-sim", Items: []string{"ir-drop SV1_DRV 158 mV", "via-current +12V"}}
	applyWaivers(&g, ws)
	if g.Pass {
		t.Fatal("gate with an unwaived item passed")
	}
	g = gateResult{Gate: "native-drc", Detail: "3 violation(s)"}
	applyWaivers(&g, []gateWaiver{{Gate: "native-drc", Match: "", Reason: "x", By: "y"}})
	if g.Pass {
		t.Fatal("gate without items was waived")
	}
	g = gateResult{Gate: "intent-widths", Items: []string{"SV1_DRV: 1 track(s)"}}
	applyWaivers(&g, ws)
	if g.Pass {
		t.Fatal("waiver for another gate applied")
	}
}

// A thin GND track running inside the GND pour of its layer is carried by
// the pour; the same track outside it (or in a pour cut-out) is not.
func TestCheckIntentWidthsPourBacked(t *testing.T) {
	reqs := map[string]specctra.NetRequirement{"GND": {OuterMil: 21.65, InnerMil: 43.31, MinMil: 10}}
	poured := []any{map[string]any{"net": "GND", "layer": "1", "fills": []any{map[string]any{"source": []any{
		[]any{0.0, 0.0, "L", 1000.0, 0.0, 1000.0, 1000.0, 0.0, 1000.0},
		[]any{400.0, 400.0, "L", 600.0, 400.0, 600.0, 600.0, 400.0, 600.0}, // cut-out
	}}}}}
	tracks := []specctra.Track{
		{ID: "in-pour", Net: "GND", Layer: 1, X1: 100, Y1: 100, X2: 300, Y2: 100, Width: 16.24},
		{ID: "through-cutout", Net: "GND", Layer: 1, X1: 300, Y1: 500, X2: 700, Y2: 500, Width: 16.24},
		{ID: "other-layer", Net: "GND", Layer: 2, X1: 100, Y1: 100, X2: 300, Y2: 100, Width: 16.24},
	}
	vs := checkIntentWidths(tracks, nil, reqs, pouredLookup(poured))
	ids := []string{}
	for _, v := range vs {
		ids = append(ids, v.ID)
	}
	if strings.Join(ids, ",") != "other-layer,through-cutout" {
		t.Fatalf("violations = %v", ids)
	}
}

func TestCheckIntentLengths(t *testing.T) {
	in, err := parseDesignIntent([]byte(`{"schemaVersion":1,"nets":{
 "A_P":{"role":"signal","widthMil":{"outer":5},"diffPair":"A_N","maxSkewMil":5},
 "A_N":{"role":"signal","widthMil":{"outer":5},"diffPair":"A_P","maxSkewMil":5},
 "D0":{"role":"signal","widthMil":{"outer":5},"lengthGroup":"BUS","lengthTolMil":50},
 "D1":{"role":"signal","widthMil":{"outer":5},"lengthGroup":"BUS","lengthTolMil":50}}}`))
	if err != nil {
		t.Fatal(err)
	}
	tracks := []specctra.Track{
		{Net: "A_P", X2: 1000}, {Net: "A_N", X2: 1003},
		{Net: "D0", X2: 1000}, {Net: "D1", X2: 1100},
	}
	fails, n := checkIntentLengths(in, tracks)
	if n != 2 || len(fails) != 1 || !strings.HasPrefix(fails[0], "group BUS: spread 100.0") {
		t.Fatalf("fails = %v (%d groups)", fails, n)
	}
}

// Per-segment basis: a GND track carrying 0.2 A of a 1.46 A net needs only
// its own IPC width (Gas Module v15 A: 14.75 mil tracks vs a 21.65 mil net
// requirement); a trunk carrying most of the net current keeps the net's.
func TestSegmentWidthNeed(t *testing.T) {
	in, err := parseDesignIntent([]byte(`{"schemaVersion":1,"copper":{"outerOz":1,"innerOz":0.5,"tempRiseC":10},"nets":{
 "GND":{"role":"ground","currentA":1.51,"widthMil":{"outer":21.65,"inner":43.31,"min":10}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	res := &postsim.Result{Stackup: &postsim.Stackup{Layers: []postsim.StackLayer{{ID: 1, Name: "TOP"}}}, Nets: []*postsim.NetResult{{Net: "GND", CurrentA: 1.46,
		AllSegments: []postsim.Segment{
			{Layer: "TOP", A: postsim.Point{X: 0, Y: 0}, B: postsim.Point{X: 100, Y: 0}, CurrentA: 0.21},
			{Layer: "TOP", A: postsim.Point{X: 0, Y: 500}, B: postsim.Point{X: 100, Y: 500}, CurrentA: 1.2},
		}}}}
	need := segmentWidthNeed(in, res)
	reqs := intentRequirements(in)
	tracks := []specctra.Track{
		{ID: "stub", Net: "GND", Layer: 1, X1: 0, Y1: 0, X2: 100, Y2: 0, Width: 14.75},
		{ID: "trunk", Net: "GND", Layer: 1, X1: 0, Y1: 500, X2: 100, Y2: 500, Width: 14.75},
	}
	vs := checkIntentWidths(tracks, nil, reqs, nil, need)
	if len(vs) != 1 || vs[0].ID != "trunk" {
		t.Fatalf("violations = %+v", vs)
	}
	if vs := checkIntentWidths(tracks, nil, reqs, nil); len(vs) != 2 {
		t.Fatalf("net basis = %+v", vs)
	}
}

func TestSegmentViaOK(t *testing.T) {
	in, err := parseDesignIntent([]byte(`{"schemaVersion":1,"nets":{"GND":{"role":"ground","currentA":1.51,"widthMil":{"outer":21.65},"via":{"drillMil":12,"diaMil":24,"countPerTransition":3,"marginPct":20}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	res := &postsim.Result{
		Nets: []*postsim.NetResult{{Net: "GND", CurrentA: 1.46}},
		Vias: []postsim.ViaResult{{ID: "v1", Net: "GND", CurrentA: 0.004, AmpacityA: 0.74}, {ID: "v2", Net: "GND", CurrentA: 1.2, AmpacityA: 0.74}},
	}
	ok := segmentViaOK(in, res)
	if pass, why := ok([]string{"v1"}, "GND"); !pass || why == "" {
		t.Fatal("milliamp GND via should pass on its simulated current")
	}
	if pass, _ := ok([]string{"v2"}, "GND"); pass {
		t.Fatal("a via carrying most of the net current must keep the net rating")
	}
	if pass, _ := ok([]string{"unknown"}, "GND"); pass {
		t.Fatal("a via the sim does not know must not pass")
	}
}
