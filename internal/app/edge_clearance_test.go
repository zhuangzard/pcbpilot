package app

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// The consumer reads the producer's "edge" field (additive; absent in older
// intents → defaults) with the same resolution rules as pcb auto.
func TestIntentEdgeContract(t *testing.T) {
	in, err := loadDesignIntent("testdata/intent/esp32-mini.derived.intent.json")
	if err != nil {
		t.Fatal(err)
	}
	if in.Edge == nil || in.Edge.EdgeKind != "routed" || in.Edge.OuterMil != 20 || in.Edge.InnerMil != 30 {
		t.Fatalf("derived edge %+v", in.Edge)
	}
	pol := in.edgePolicy()
	if pol.Source != "intent" || pol.LayerReq(pcbauto.LayerTop) != 20 || pol.LayerReq(pcbauto.LayerInner1) != 30 {
		t.Fatalf("policy %+v", pol)
	}
	old := mustIntent(t) // predates "edge": defaults
	if old.Edge != nil || old.edgePolicy().Source != "default" || old.edgePolicy().LayerReq(pcbauto.LayerInner1) != 30 {
		t.Fatalf("intent without edge: %+v", old.edgePolicy())
	}
	hv, err := parseDesignIntent([]byte(`{"nets":{"AC_L":{"role":"power","domain":"MAINS"},"GND":{"role":"ground","domain":"SELV"}},
		"domains":[{"id":"MAINS","kind":"mains","nets":["AC_L"],"workingVrms":230,"workingVpeak":325},{"id":"SELV","kind":"SELV","nets":["GND"]}],
		"edge":{"outerMil":20,"innerMil":30,"vcutMil":0,"edgeKind":"routed","byDomain":{"MAINS":{"mil":261.9,"clearanceMm":4,"creepageMm":6.65,"insulation":"reinforced"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p := hv.edgePolicy(); p.NetReq("AC_L") != 261.9 || p.NetReq("GND") != 0 {
		t.Fatalf("hv policy AC_L %v GND %v", p.NetReq("AC_L"), p.NetReq("GND"))
	}
	// An older HV intent without "edge": the domain distance is computed.
	hvOld, _ := parseDesignIntent([]byte(`{"standard":{"name":"IEC62368-1"},"nets":{"AC_L":{"role":"power","domain":"MAINS"}},
		"domains":[{"id":"MAINS","kind":"mains","nets":["AC_L"],"workingVrms":230,"workingVpeak":325}]}`))
	if p := hvOld.edgePolicy(); p.NetReq("AC_L") <= 20 || p.ByDomain["MAINS"].Insulation != "reinforced" {
		t.Fatalf("computed hv policy %+v", p.ByDomain["MAINS"])
	}
	for _, bad := range []string{
		`{"nets":{"A":{"role":"power"}},"edge":{"edgeKind":"laser"}}`,
		`{"nets":{"A":{"role":"power"}},"edge":{"outerMil":-1}}`,
		`{"nets":{"A":{"role":"power"}},"edge":{"byDomain":{"M":null}}}`,
		`{"nets":{"A":{"role":"power"}},"edge":{"byDomain":{"M":{"mil":-3}}}}`,
	} {
		if _, err := parseDesignIntent([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

// A class with hazardous members keeps its domain distance in the Board
// Outline cells of its PP_ rule; the default rule carries the general
// distance; the native creepage rule is reported, never enabled.
func TestIntentRulesEdgeHVClass(t *testing.T) {
	in, err := parseDesignIntent([]byte(`{"nets":{"AC_L":{"role":"power","domain":"MAINS","netClass":"MAINS"},"AC_N":{"role":"power","domain":"MAINS","netClass":"MAINS"},"GND":{"role":"ground","domain":"SELV"}},
		"domains":[{"id":"MAINS","kind":"mains","nets":["AC_L","AC_N"],"workingVrms":230,"workingVpeak":325},{"id":"SELV","kind":"SELV","nets":["GND"]}],
		"pairs":[{"a":"domain:MAINS","b":"domain:SELV","clearanceMm":4,"creepageMm":6.65}],
		"netClasses":[{"name":"MAINS","nets":["AC_L","AC_N"],"trackMil":30,"clearanceMil":20}],
		"edge":{"outerMil":20,"innerMil":30,"edgeKind":"routed","byDomain":{"MAINS":{"mil":261.9,"clearanceMm":4,"creepageMm":6.65,"insulation":"reinforced"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	cfg := cleanHostConfig(t, []string{"AC_L", "AC_N", "GND"})
	p := planIntentRules(in, snapshotOf(t, cfg), nil, nil)
	if len(p.Conflicts) != 0 {
		t.Fatalf("conflicts %+v", p.Conflicts)
	}
	rc := p.ruleConfiguration
	mains := mnav(rc, "Spacing", "Safe Spacing", "PP_MAINS", "tables", "1", "content").([]any)
	closeTo(t, "PP_MAINS Board Outline/Track = mains edge distance", mains[11].([]any)[0], 261.9*0.0254)
	closeTo(t, "PP_MAINS Board Outline/TH Pad", mains[11].([]any)[2], 261.9*0.0254)
	def := mnav(rc, "Spacing", "Safe Spacing", "copperThickness1oz", "tables", "1", "content").([]any)
	closeTo(t, "default Board Outline/Via", def[11].([]any)[5], 30*0.0254)
	closeTo(t, "default Board Outline/Slot untouched", def[11].([]any)[8], 0.29972)
	if p.Edge == nil || p.Edge.Classes["PP_MAINS"] != 261.9 || p.Edge.RuleMil != 30 {
		t.Fatalf("edge plan %+v", p.Edge)
	}
	advised := false
	for _, a := range p.Advisories {
		if a.Item == "native creepage rule" && strings.Contains(a.Detail, "6.65 mm") && strings.Contains(a.Detail, "NOT enabled") {
			advised = true
		}
	}
	if !advised {
		t.Fatalf("no native creepage advisory: %+v", p.Advisories)
	}
	if cr := mnav(rc, "Spacing", "Creepage Distance"); cr != nil && strings.Contains(mustJSON(t, cr), `"creepageDistance":6.65`) {
		t.Fatal("native creepage rule must not be enabled")
	}
}

func mustJSON(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// `pcb check --board` on the live ESP32 v0.5 dump: the 14.1 mil pours and
// the 10 mil negative-plane pull-back are ERRORs and --strict fails.
func TestPcbCheckCopperToEdgeLiveDump(t *testing.T) {
	var out, errb bytes.Buffer
	err := runPcbCheckIntent(nil, "", 3, nil, "", "../../pkg/pcbauto/testdata/esp32-v05-live.reload.json", "", true, true, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "--strict") {
		t.Fatalf("strict gate: %v", err)
	}
	var rep pcbCheckReport
	if jerr := json.Unmarshal(out.Bytes(), &rep); jerr != nil {
		t.Fatal(jerr)
	}
	types := map[string]int{}
	for _, f := range rep.Findings {
		if f.Level == "ERROR" {
			types[f.Type]++
		}
	}
	if types["copper-to-edge"] < 3 || types["plane-pullback"] != 1 || rep.Summary.CopperToEdge == 0 || rep.Passed || rep.Edge == nil {
		t.Fatalf("findings %v summary %+v", types, rep.Summary)
	}
	// The fixed plan passes.
	out.Reset()
	if err := runPcbCheckIntent(nil, "", 3, nil, "", "../../pkg/pcbauto/testdata/esp32-v05-fixed.routed.json", "", true, true, &out, &errb); err != nil {
		t.Fatalf("fixed board: %v\n%s", err, out.String())
	}
}

// pour-fit / power-pour boundary: the centre-line outline inset by the edge
// distance, rounded corners kept clear; rail rectangles clipped to it.
func TestPourBoundaryInset(t *testing.T) {
	outline := pcbauto.RoundedRect(pcbauto.Rect{MinX: 0, MinY: 0, MaxX: 1811.34, MaxY: 1791.34}, 39.37)
	pts, bb, err := insetBoundary(outline, 20)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(bb[0]-20.05) > 0.02 || math.Abs(bb[3]-(1791.34-20.05)) > 0.02 {
		t.Fatalf("bounds %v", bb)
	}
	var poly []pcbauto.Point
	for _, p := range pts {
		poly = append(poly, pcbauto.Point{X: p[0], Y: p[1]})
	}
	for _, p := range poly {
		if d := pcbauto.PolyEdgeDist(outline, p); d < 20-0.01 {
			t.Fatalf("boundary vertex %v only %.2f mil from the edge", p, d)
		}
	}
	plans := fitPowerPourToBoundary([]powerPourPlan{
		{Net: "GND", Layer: 1, Kind: "gnd-plane", Points: [][]float64{{bb[0], bb[1]}, {bb[2], bb[1]}, {bb[2], bb[3]}, {bb[0], bb[3]}}},
		{Net: "+3V3", Layer: 1, Kind: "rail-local", Points: [][]float64{{bb[0], bb[1]}, {300, bb[1]}, {300, 300}, {bb[0], 300}}},
	}, pts)
	for _, pl := range plans {
		for _, p := range pl.Points {
			if d := pcbauto.PolyEdgeDist(outline, pcbauto.Point{X: p[0], Y: p[1]}); d < 20-0.01 {
				t.Fatalf("%s vertex %v %.2f mil from the edge (rectangle cut the rounded corner)", pl.Net, p, d)
			}
		}
	}
	if _, _, err := insetBoundary(pcbauto.Rect{MaxX: 40, MaxY: 40}.Corners(), 30); err == nil {
		t.Fatal("a board narrower than twice the inset must be refused")
	}
	o := pourEdgeOpts{inset: 5, insetSet: true}
	var warn bytes.Buffer
	if v := o.insetFor(pcbauto.DefaultEdgePolicy(""), 1, "GND", &warn); v != 8 || !strings.Contains(warn.String(), "fab floor") {
		t.Fatalf("--inset below the fab floor: %v %q", v, warn.String())
	}
	if v := (pourEdgeOpts{}).insetFor(pcbauto.DefaultEdgePolicy(""), 15, "GND", &warn); v != 30 {
		t.Fatalf("inner default inset %v", v)
	}
}
