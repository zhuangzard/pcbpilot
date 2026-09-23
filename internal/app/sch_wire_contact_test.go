package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

func crossingLayout(sameNet bool) *SchematicLayoutResult {
	a, b := "A", "B"
	if sameNet {
		b = a
	}
	l := &SchematicLayoutResult{ComponentIDs: map[string]string{"U1": "core", "R1": "peripheral"}, Wires: []SchematicWire{
		{Net: a, Points: [][2]float64{{-20, 0}, {20, 0}}}, {Net: b, Points: [][2]float64{{0, -20}, {0, 20}}},
	}, Placements: []SchematicPlacement{
		{Designator: "U1", BBox: SchematicBox{-40, -10, -30, 10}, Pins: []SchematicPin{{Number: "1", Net: a, X: -20, Y: 0}}},
		{Designator: "R1", BBox: SchematicBox{-10, -40, 10, -30}, Pins: []SchematicPin{{Number: "1", Net: b, X: 0, Y: -20}}},
	}}
	return l
}

func TestProperCrossingDoesNotMergePhysicalIslandsOrRenderDot(t *testing.T) {
	for _, same := range []bool{false, true} {
		l := crossingLayout(same)
		pins, e := schematicVariantPhysicalPinIslands(l)
		if e != nil {
			t.Fatal(e)
		}
		if pins[schematicVariantPinIdentity{"core", "1"}].root == pins[schematicVariantPinIdentity{"peripheral", "1"}].root {
			t.Fatal("proper X merged")
		}
		p := powerLayoutPlan{Placements: l.Placements, Wires: l.Wires}
		if len(libIslands(&p)) != 2 {
			t.Fatal("planner islands use net name or geometric crossing")
		}
		for _, dot := range layoutJunctions(l) {
			if dot == [2]float64{0, 0} {
				t.Fatal("proper X got junction dot")
			}
		}
	}
	// Same-net marker cannot disguise missing physical peripheral attachment.
	l := crossingLayout(true)
	mods := []connectivity.Module{{ID: "m", CoreComponents: []string{"core"}, PeripheralComponents: []string{"peripheral"}}}
	if e := ValidateSchematicPeripheralDirect(l, mods); e == nil || !strings.Contains(e.Error(), "peripheral-direct-missing") {
		t.Fatal("same-name X faked a dedicated path", e)
	}
}

func TestWireTContactForeignRejectedAndMergePreservesJunction(t *testing.T) {
	h := SchematicWire{Net: "A", Points: [][2]float64{{-20, 0}, {20, 0}}}
	v := SchematicWire{Net: "B", Points: [][2]float64{{0, 0}, {0, 20}}}
	p := powerLayoutPlan{Wires: []SchematicWire{h, v}}
	if e := validatePowerLayout(&p, layoutBBox{-100, -100, 100, 100}); e == nil {
		t.Fatal("foreign T accepted")
	}
	for _, net := range []string{"A", "B"} {
		cross := SchematicWire{Net: net, Points: [][2]float64{{0, -20}, {0, 20}}}
		left := SchematicWire{Net: "A", Points: [][2]float64{{-20, 0}, {0, 0}}}
		right := SchematicWire{Net: "A", Points: [][2]float64{{0, 0}, {20, 0}}}
		// The original endpoint at X is an actual contact; collinear merging
		// may dedup edges but cannot remove this junction or hide a short.
		merged := libAppendRoute([]SchematicWire{cross, left}, []SchematicWire{right, left})
		l := &SchematicLayoutResult{Wires: merged}
		dots := layoutJunctions(l)
		found := false
		for _, dot := range dots {
			found = found || dot == [2]float64{0, 0}
		}
		if (net == "A") != found {
			t.Fatalf("net=%s dots=%v", net, dots)
		}
		p.Wires = merged
		e := validatePowerLayout(&p, layoutBBox{-100, -100, 100, 100})
		if (net == "B") != (e != nil) {
			t.Fatalf("merge hid foreign contact or broke same-net junction: %s %v", net, e)
		}
		for i, w := range merged {
			for _, o := range merged[:i] {
				if w.Net == o.Net && reflect.DeepEqual(w.Points, o.Points) {
					t.Fatal("merge left duplicate intervals")
				}
			}
		}
	}
}

func TestContactPreservingMergeDoesNotCreateRoundoffLengthWires(t *testing.T) {
	const delta = 1e-10
	existing := []SchematicWire{
		{Net: "A", Points: [][2]float64{{-20, 0}, {0, 0}}},
		{Net: "A", Points: [][2]float64{{0, 0}, {0, 20}}},
		{Net: "A", Points: [][2]float64{{delta, 0}, {delta, -20}}},
	}
	out := libAppendRoute(existing, []SchematicWire{{Net: "A", Points: [][2]float64{{-10, 0}, {20, 0}}}})
	if _, e := drawingEdges(out); e != nil {
		t.Fatal("same physical junction created a sub-grid/zero-length segment", e, out)
	}
}

func TestRendererCollinearActionVertexDoesNotInventContact(t *testing.T) {
	l := crossingLayout(true)
	l.Wires[0].Points = [][2]float64{{-20, 0}, {0, 0}, {20, 0}}
	for _, p := range layoutJunctions(l) {
		if p == [2]float64{0, 0} {
			t.Fatal("collinear API vertex fabricated dot")
		}
	}
	mods := []connectivity.Module{{ID: "m", CoreComponents: []string{"core"}, PeripheralComponents: []string{"peripheral"}}}
	if ValidateSchematicPeripheralDirect(l, mods) == nil {
		t.Fatal("collinear API vertex fabricated ownership")
	}
}

func TestObservedOwnershipCrossingUsesPhysicalContactsNotNames(t *testing.T) {
	for _, same := range []bool{false, true} {
		l := crossingLayout(same)
		raw := map[string]any{}
		var parts, wires []any
		for _, c := range l.Placements {
			q := c.Pins[0]
			parts = append(parts, map[string]any{"componentType": "part", "designator": c.Designator, "pinsAvailable": true, "pins": []any{map[string]any{"pinNumber": q.Number, "x": q.X, "y": q.Y, "net": q.Net, "noConnected": false}}})
		}
		for _, w := range l.Wires {
			wires = append(wires, map[string]any{"x0": w.Points[0][0], "y0": w.Points[0][1], "x1": w.Points[1][0], "y1": w.Points[1][1], "net": w.Net})
		}
		raw["components"], raw["wires"] = parts, wires
		got, e := schematicObservedOwnershipLayout(raw, l.ComponentIDs)
		if e != nil {
			t.Fatal("proper-X raw observation rejected", e)
		}
		pins, e := schematicVariantPhysicalPinIslands(got)
		if e != nil {
			t.Fatal(e)
		}
		if pins[schematicVariantPinIdentity{"core", "1"}].root == pins[schematicVariantPinIdentity{"peripheral", "1"}].root {
			t.Fatal("observed X falsely merged")
		}
		// Real split endpoint at X now means physical contact; foreign names
		// must be rejected while same-net contact becomes one true island.
		wires[0] = map[string]any{"x0": -20, "y0": 0, "x1": 0, "y1": 0, "net": "A"}
		wires = append(wires, map[string]any{"x0": 0, "y0": 0, "x1": 20, "y1": 0, "net": "A"})
		raw["wires"] = wires
		got, e = schematicObservedOwnershipLayout(raw, l.ComponentIDs)
		if !same {
			if e == nil {
				t.Fatal("observed foreign T short accepted")
			}
			continue
		}
		if e != nil {
			t.Fatal(e)
		}
		pins, e = schematicVariantPhysicalPinIslands(got)
		if e != nil {
			t.Fatal(e)
		}
		if pins[schematicVariantPinIdentity{"core", "1"}].root != pins[schematicVariantPinIdentity{"peripheral", "1"}].root {
			t.Fatal("observed same-net T lost")
		}
	}
}

func TestDrawingReadbackRejectsContactChangeWithIdenticalEdges(t *testing.T) {
	l := crossingLayout(true)
	e := schematicDrawingExpectation{Wires: l.Wires}
	live := drawingContactSnapshot(l.Wires)
	live["components"] = []any{}
	live["connectivitySummary"] = emptyDrawingSummary()
	if err := e.check(live); err != nil {
		t.Fatal(err)
	}
	joined := append([]SchematicWire(nil), l.Wires[1:]...)
	joined = append(joined, SchematicWire{Net: "A", Points: [][2]float64{{-20, 0}, {0, 0}}}, SchematicWire{Net: "A", Points: [][2]float64{{0, 0}, {20, 0}}})
	want, _ := drawingEdges(l.Wires)
	have, _ := drawingEdges(joined)
	if !reflect.DeepEqual(want, have) {
		t.Fatal("fixture edges differ")
	}
	live["wires"] = drawingContactSnapshot(joined)["wires"]
	if err := e.check(live); err == nil || !strings.Contains(err.Error(), "contact topology") {
		t.Fatal("same edges concealed new X junction", err)
	}
	e.Wires = joined
	live["wires"] = drawingContactSnapshot(l.Wires)["wires"]
	if err := e.check(live); err == nil {
		t.Fatal("same edges concealed removed junction")
	}
	// A same-action internal collinear vertex is not an explicit junction.
	e.Wires = append([]SchematicWire(nil), l.Wires...)
	e.Wires[0].Points = [][2]float64{{-20, 0}, {0, 0}, {20, 0}}
	if err := e.check(live); err != nil {
		t.Fatal("authored collinear vertex was not normalized", err)
	}
}

func TestAlternatingBoundaryPinsRouteWithVerifiedNoncontactCrossings(t *testing.T) {
	r := 0.0
	core := SchematicPlacement{Designator: "U1", BBox: SchematicBox{-20, -40, 20, 40}, TextBBoxes: []SchematicBox{{-10, 45, 10, 50}}, Pins: []SchematicPin{
		{Number: "1", Net: "A", X: 30, Y: 30, Rotation: &r}, {Number: "2", Net: "B", X: 30, Y: 10, Rotation: &r},
		{Number: "3", Net: "A", X: 30, Y: -10, Rotation: &r}, {Number: "4", Net: "B", X: 30, Y: -30, Rotation: &r},
	}}
	in := SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: "core", Components: []SchematicLayoutComponent{{ID: "core", Measurement: core}}, NetPolicies: map[string]string{"A": "direct", "B": "direct"}, MaxCandidates: 20000}
	out, e := PlanSchematicLayout(in)
	if e != nil {
		t.Fatal(e)
	}
	if out == nil || len(out.Wires) == 0 {
		t.Fatal("missing physical routes")
	}
	pins, e := schematicVariantPhysicalPinIslands(out)
	if e != nil {
		t.Fatal(e)
	}
	a1, a2 := pins[schematicVariantPinIdentity{"core", "1"}], pins[schematicVariantPinIdentity{"core", "3"}]
	b1, b2 := pins[schematicVariantPinIdentity{"core", "2"}], pins[schematicVariantPinIdentity{"core", "4"}]
	if a1 != a2 || b1 != b2 || a1.root == b1.root {
		t.Fatal("ABAB routed by fake union", pins)
	}
}
