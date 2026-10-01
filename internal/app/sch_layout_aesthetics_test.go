package app

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

// ams1117LibLayoutSource loads the measured AMS1117 lib-layout fixture.
func ams1117LibLayoutSource(t *testing.T) libLayoutSource {
	t.Helper()
	raw, err := os.ReadFile("testdata/lib-layout/ams1117.json")
	if err != nil {
		t.Fatal(err)
	}
	src, err := decodeLibLayout(raw)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func schAesPinNets(src *schCompositionSource) map[string]string {
	out := map[string]string{}
	for _, m := range src.Modules {
		for _, c := range m.Placements {
			for _, q := range c.Pins {
				out[c.Designator+"."+q.Number] = q.Net
			}
		}
	}
	return out
}

// Default output must stay byte-identical to the Phase A fixture: the pass is
// opt-in and the solver/maze order is unchanged when it is off.
func TestSchAesOffByDefaultIsUnchanged(t *testing.T) {
	out, err := planLibLayout(ams1117LibLayoutSource(t))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.MarshalIndent(out, "", "  ")
	want, err := os.ReadFile("../../pkg/schaes/testdata/ams1117-lib-layout.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(append(got, '\n')) != string(want) {
		t.Fatal("lib-layout without --aesthetics drifted from the Phase A fixture")
	}
}

// Phase B acceptance on the AMS1117 fixture: crossing 1 → 0, T-junction
// quality 0 → ≥75, identical connectivity, no new check/lint finding.
func TestSchAesAMS1117Acceptance(t *testing.T) {
	base, err := planLibLayout(ams1117LibLayoutSource(t))
	if err != nil {
		t.Fatal(err)
	}
	src := ams1117LibLayoutSource(t)
	src.Aesthetics = &SchematicAestheticsOptions{Style: "balanced"}
	var reports []libLayoutAestheticsReport
	out, err := planLibLayoutWithReports(src, &reports)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("want one module report, got %d", len(reports))
	}
	rep := reports[0].Report
	if rep.Status != "improved" || !rep.ConnectivityIdentical || !rep.PinNetIdentical {
		t.Fatalf("status %s connectivity %v", rep.Status, rep.ConnectivityIdentical)
	}
	if rep.MetricsBefore["W2"] >= 100 || rep.MetricsAfter["W2"] != 100 {
		t.Fatalf("W2 crossings: before %v after %v (want <100 → 100)", rep.MetricsBefore["W2"], rep.MetricsAfter["W2"])
	}
	if rep.MetricsBefore["W5"] != 0 || rep.MetricsAfter["W5"] < 75 {
		t.Fatalf("W5 T-junction: before %v after %v (want 0 → ≥75)", rep.MetricsBefore["W5"], rep.MetricsAfter["W5"])
	}
	if worse := schAesCheckWorse(rep.CheckBefore, rep.CheckAfter); worse != "" {
		t.Fatalf("new offline check/lint findings: %s", worse)
	}
	if !reflect.DeepEqual(schAesPinNets(base), schAesPinNets(out)) {
		t.Fatal("pin→net map changed")
	}
	// Independent re-measure of the emitted compose source.
	raw, _ := json.Marshal(out)
	snap, err := schaes.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := schaes.Analyze(snap, nil)
	if c := r.Counts["crossings"]; c != 0 {
		t.Fatalf("re-measured crossings %d", c)
	}
	if w5 := r.Metric("W5"); w5.Score < 75 {
		t.Fatalf("re-measured W5 %v", w5.Score)
	}
	if _, err := planSchComposition(*out); err != nil {
		t.Fatalf("compose validation: %v", err)
	}
	again, err := planLibLayoutWithReports(src, nil)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("beautify pass is not deterministic", err)
	}
}

// Every style and every fast in-repo zone fixture: the pass never breaks a
// gate, never changes connectivity and never makes the objective worse.
func TestSchAesStylesKeepEveryGate(t *testing.T) {
	inputs := map[string]SchematicLayoutInput{"standalone": standaloneLayoutFixture()}
	if !testing.Short() {
		inputs["buck"] = buckZoneFixture()
	}
	for name, in := range inputs {
		for _, style := range []string{"functional", "balanced", "precision", "auto"} {
			in := in
			in.MaxCandidates = 200000
			in.Aesthetics = &SchematicAestheticsOptions{Style: style}
			out, err := PlanSchematicLayout(in)
			if err != nil {
				t.Fatalf("%s/%s: %v", name, style, err)
			}
			a := out.Aesthetics
			if a == nil || !a.ConnectivityIdentical || !a.PinNetIdentical {
				t.Fatalf("%s/%s: report %+v", name, style, a)
			}
			if worse := schAesCheckWorse(a.CheckBefore, a.CheckAfter); worse != "" {
				t.Fatalf("%s/%s: new findings %s", name, style, worse)
			}
			if a.DefectsAfter > a.DefectsBefore || (a.DefectsAfter == a.DefectsBefore && a.ScoreAfter < a.ScoreBefore) {
				t.Fatalf("%s/%s: objective got worse %+v", name, style, a)
			}
			p := powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags}
			if err := validateLibGeometry(&p); err != nil {
				t.Fatalf("%s/%s: %v", name, style, err)
			}
			if err := validateSchCompositionNets(&p); err != nil {
				t.Fatalf("%s/%s: %v", name, style, err)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// hand-built plans for B2 (long wire → label) and B3 (bus lanes)

func schAesPart(des string, x, y float64, box SchematicBox, pins ...SchematicPin) powerLayoutPlacement {
	return powerLayoutPlacement{Designator: des, X: x, Y: y, BBox: box, Pins: pins}
}

func schAesRun(t *testing.T, core string, ids map[string]string, policies, roles map[string]string, p powerLayoutPlan, style string) *SchematicLayoutResult {
	t.Helper()
	states := map[string]map[string]string{}
	for _, id := range ids {
		states[id] = nil
	}
	if err := validateLibGeometry(&p); err != nil {
		t.Fatalf("fixture geometry: %v", err)
	}
	if err := validateSchCompositionNets(&p); err != nil {
		t.Fatalf("fixture nets: %v", err)
	}
	res := &SchematicLayoutResult{ComponentIDs: ids, PinStates: states, Placements: p.Placements, Wires: p.Wires, Flags: p.Flags}
	in := SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: core, Aesthetics: &SchematicAestheticsOptions{Style: style}}
	budget := 0
	return applySchematicAesthetics(in, res, policies, roles, &budget)
}

// Core U1 owns R1 (net A) and R2 (net B) by real wires; SIG joins the two
// peripherals with an 840-unit wire (> balanced longWireUnits 600).
func longWireFixture() (map[string]string, powerLayoutPlan) {
	left, right, up, down := directionNumber(180), directionNumber(0), directionNumber(90), directionNumber(270)
	ids := map[string]string{"U1": "core", "R1": "r1", "R2": "r2"}
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{
		schAesPart("U1", 400, -200, SchematicBox{MinX: 380, MinY: -220, MaxX: 420, MaxY: -180},
			SchematicPin{Number: "1", Net: "GND", X: 400, Y: -230, Rotation: down},
			SchematicPin{Number: "2", Net: "A", X: 370, Y: -200, Rotation: left},
			SchematicPin{Number: "3", Net: "B", X: 430, Y: -200, Rotation: right}),
		schAesPart("R1", 0, 0, SchematicBox{MinX: -8.5, MinY: -10.5, MaxX: 8.5, MaxY: 10.5},
			SchematicPin{Number: "1", Net: "SIG", X: 0, Y: 20, Rotation: up}, SchematicPin{Number: "2", Net: "A", X: 0, Y: -20, Rotation: down}),
		schAesPart("R2", 800, 0, SchematicBox{MinX: 791.5, MinY: -10.5, MaxX: 808.5, MaxY: 10.5},
			SchematicPin{Number: "1", Net: "SIG", X: 800, Y: 20, Rotation: up}, SchematicPin{Number: "2", Net: "B", X: 800, Y: -20, Rotation: down}),
	}}
	p.Wires = libPointsRoute("SIG", [2]float64{0, 20}, [2]float64{0, 40}, [2]float64{800, 40}, [2]float64{800, 20})
	p.Wires = append(p.Wires, libPointsRoute("A", [2]float64{0, -20}, [2]float64{0, -200}, [2]float64{370, -200})...)
	p.Wires = append(p.Wires, libPointsRoute("B", [2]float64{800, -20}, [2]float64{800, -200}, [2]float64{430, -200})...)
	p.Flags = []powerLayoutFlag{
		{Net: "GND", Kind: "ground", PinX: 400, PinY: -230, Direction: "down", Offset: 10},
		{Net: "A", Kind: "net_port_bi", PinX: 0, PinY: -100, Direction: "left", Offset: 10},
		{Net: "B", Kind: "net_port_bi", PinX: 800, PinY: -100, Direction: "right", Offset: 10},
		{Net: "SIG", Kind: "net_port_bi", PinX: 0, PinY: 40, Direction: "left", Offset: 10},
	}
	return ids, p
}

func schAesSigIslands(p *powerLayoutPlan) int {
	n := 0
	for _, island := range libIslands(p) {
		if island.net == "SIG" {
			n++
		}
	}
	return n
}

func TestSchAesLongWireBecomesLabelsOnlyWhenPolicyAllows(t *testing.T) {
	roles := map[string]string{"SIG": "signal", "GND": "ground", "A": "signal", "B": "signal"}
	policy := func(sig string) map[string]string {
		return map[string]string{"SIG": sig, "GND": "local_ground", "A": "direct", "B": "direct"}
	}
	ids, p := longWireFixture()
	out := schAesRun(t, "core", ids, policy("module_port"), roles, p, "balanced")
	plan := powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags}
	if len(out.Aesthetics.LabelSplits) == 0 || schAesSigIslands(&plan) != 2 || !out.Aesthetics.PinNetIdentical || out.Aesthetics.ConnectivityIdentical {
		t.Fatalf("module_port long wire was not split into labelled islands: %+v", out.Aesthetics)
	}
	if out.Aesthetics.MetricsAfter["N1"] != 100 || out.Aesthetics.MetricsBefore["N1"] == 100 {
		t.Fatalf("N1 %v → %v", out.Aesthetics.MetricsBefore["N1"], out.Aesthetics.MetricsAfter["N1"])
	}
	if err := validateSchCompositionNets(&plan); err != nil {
		t.Fatal(err)
	}

	// A direct net is a wire tree by contract: never converted.
	ids, p = longWireFixture()
	out = schAesRun(t, "core", ids, policy("direct"), roles, p, "balanced")
	plan = powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags}
	if len(out.Aesthetics.LabelSplits) != 0 || schAesSigIslands(&plan) != 1 {
		t.Fatalf("direct net was split: %+v", out.Aesthetics.LabelSplits)
	}

	// A core↔peripheral wire is kept physically even under a label policy.
	ids, p = longWireFixture()
	out = schAesRun(t, "r1", ids, policy("module_port"), roles, p, "balanced")
	plan = powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags}
	if len(out.Aesthetics.LabelSplits) != 0 || schAesSigIslands(&plan) != 1 {
		t.Fatalf("core↔peripheral wire was split: %+v", out.Aesthetics.LabelSplits)
	}
}

func busLaneFixture() (map[string]string, powerLayoutPlan) {
	right, down := directionNumber(0), directionNumber(270)
	pins := []SchematicPin{{Number: "9", Net: "GND", X: 0, Y: -60, Rotation: down}}
	flags := []powerLayoutFlag{{Net: "GND", Kind: "ground", PinX: 0, PinY: -60, Direction: "down", Offset: 10}}
	for i, net := range []string{"D0", "D1", "D2", "D3"} {
		y := 30 - 20*float64(i)
		pins = append(pins, SchematicPin{Number: itoaPin(i + 1), Net: net, X: 40, Y: y, Rotation: right})
		off := 10.0
		if i%2 == 1 {
			off = 30
		}
		flags = append(flags, powerLayoutFlag{Net: net, Kind: "net_port_bi", PinX: 40, PinY: y, Direction: "right", Offset: off})
	}
	return map[string]string{"U1": "core"}, powerLayoutPlan{
		Placements: []powerLayoutPlacement{schAesPart("U1", 0, 0, SchematicBox{MinX: -30, MinY: -50, MaxX: 30, MaxY: 50}, pins...)},
		Flags:      flags,
	}
}

func TestSchAesBusLaneAlignsLabels(t *testing.T) {
	ids, p := busLaneFixture()
	policies := map[string]string{"GND": "local_ground", "D0": "module_port", "D1": "module_port", "D2": "module_port", "D3": "module_port"}
	roles := map[string]string{"GND": "ground", "D0": "signal", "D1": "signal", "D2": "signal", "D3": "signal"}
	out := schAesRun(t, "core", ids, policies, roles, p, "balanced")
	a := out.Aesthetics
	if a.MetricsBefore["N3"] >= 90 || a.MetricsAfter["N3"] < 90 {
		t.Fatalf("N3 %v → %v (want <90 → ≥90)", a.MetricsBefore["N3"], a.MetricsAfter["N3"])
	}
	if len(a.BusLanes) != 1 || a.BusLanes[0].Aligned != 1 || a.BusLanes[0].SameDir != 1 || a.BusLanes[0].Native != nil {
		t.Fatalf("lane %+v", a.BusLanes)
	}
	// Native bus is an opt-in, report-only, live-unverified proposal.
	ids, p = busLaneFixture()
	res := &SchematicLayoutResult{ComponentIDs: ids, PinStates: map[string]map[string]string{"core": nil}, Placements: p.Placements, Wires: p.Wires, Flags: p.Flags}
	budget := 0
	out = applySchematicAesthetics(SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: "core", Aesthetics: &SchematicAestheticsOptions{Style: "balanced", NativeBus: true}}, res, policies, roles, &budget)
	lanes := out.Aesthetics.BusLanes
	if len(lanes) != 1 || lanes[0].Native == nil || lanes[0].Native.Status != "live-unverified" {
		t.Fatalf("native proposal %+v", lanes)
	}
	if len(out.Wires) != 0 {
		t.Fatalf("native bus proposal must never draw geometry: %+v", out.Wires)
	}
}

func TestSchAesStyleFileGenerateKeys(t *testing.T) {
	p, err := schaes.ParseStyle([]byte(`{"base":"balanced","generate":{"alignMoveUnits":0,"busPitchUnits":20}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Generate.AlignMove != 0 || p.Generate.BusPitch != 20 || p.Generate.JunctionClearance != 10 || p.Generate.MaxEvaluations == 0 {
		t.Fatalf("generate override %+v", p.Generate)
	}
	for _, bad := range []string{`{"generate":{"busPitchUnits":7}}`, `{"generate":{"alignMoveUnits":500}}`, `{"generate":{"junctionClearanceUnits":3}}`} {
		if _, err := schaes.ParseStyle([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
