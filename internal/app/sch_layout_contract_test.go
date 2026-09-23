package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/schguard"
)

// A deliberately tiny synthetic circuit: one measured core, one two-pin
// peripheral, two explicitly classified rails and a real NC pin. No library,
// board name, part model or production UUID is available to the algorithm.
func layoutContractFixture() SchematicLayoutInput {
	in := standaloneLayoutFixture()
	in.MaxCandidates = 20000
	angles := [][]float64{{0, 270, 180}, {180, 0}}
	for i := range in.Components {
		for j := range in.Components[i].Measurement.Pins {
			a := angles[i][j]
			in.Components[i].Measurement.Pins[j].Rotation = &a
		}
	}
	return in
}

func layoutContractJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Mirror the actual world geometry about its anchor's vertical axis BEFORE
// rotating it. There is no production mirror helper: reflection must change
// every measured point, the bbox and world pin angles, not just a display flag.
func layoutContractReflect(c SchematicPlacement) SchematicPlacement {
	c = plTranslate(c, 0, 0)
	reflectBox := func(b SchematicBox) SchematicBox { return SchematicBox{2*c.X - b.MaxX, b.MinY, 2*c.X - b.MinX, b.MaxY} }
	c.BBox = reflectBox(c.BBox)
	for i, b := range c.TextBBoxes {
		c.TextBBoxes[i] = reflectBox(b)
	}
	for i := range c.Pins {
		q := &c.Pins[i]
		q.X = 2*c.X - q.X
		angle := math.Mod(180-*q.Rotation+360, 360)
		q.Rotation = &angle
	}
	c.Mirror = !c.Mirror
	return c
}

// Use the SAME pure guard as the daemon, but adapt the completed geometry as
// an independent raw observation. Marker leads are real segments too.
func layoutContractRuntimeFindings(t *testing.T, out *SchematicLayoutResult) []schguard.Finding {
	t.Helper()
	plan := powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags}
	segments, err := schTerminalSegments(&plan)
	if err != nil {
		t.Fatal(err)
	}
	var components, wires []any
	for _, c := range out.Placements {
		var pins []any
		for _, q := range c.Pins {
			if q.Rotation == nil {
				t.Fatalf("lost measured world angle %s.%s", c.Designator, q.Number)
			}
			pins = append(pins, map[string]any{"pinNumber": q.Number, "x": q.X, "y": q.Y, "rotation": *q.Rotation, "net": q.Net, "noConnected": out.PinStates[out.ComponentIDs[c.Designator]][q.Number] == "nc"})
		}
		bbox := map[string]any{}
		if err := json.Unmarshal(layoutContractJSON(t, c.BBox), &bbox); err != nil {
			t.Fatal(err)
		}
		components = append(components, map[string]any{"componentType": "part", "designator": c.Designator, "primitiveId": out.ComponentIDs[c.Designator], "bbox": bbox, "pinsAvailable": true, "pins": pins})
	}
	for i, w := range segments {
		wires = append(wires, map[string]any{"primitiveId": fmt.Sprintf("synthetic-wire-%d", i), "points": w.Points})
	}
	return schguard.AnalyzeWireGeometry(map[string]any{"components": components, "wires": wires, "wiresAvailable": true})
}

func layoutContractAssertComplete(t *testing.T, in SchematicLayoutInput, out *SchematicLayoutResult) {
	t.Helper()
	if out == nil || len(out.Placements) != len(in.Components) || out.CandidatesUsed <= 0 || out.CandidatesUsed > in.MaxCandidates {
		t.Fatalf("incomplete result or budget overrun: %+v", out)
	}
	_, allowed, err := schematicOptimizationSettings(in)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateSchematicOptimizationEvidence(in, out, allowed); err != nil {
		t.Fatal("not a rigid transformation of measured source", err)
	}
	roles := map[string]string{}
	for net, policy := range in.NetPolicies {
		switch policy {
		case "local_power":
			roles[net] = "power"
		case "local_ground":
			roles[net] = "ground"
		default:
			roles[net] = "signal"
		}
	}
	if err = validateSchematicLayoutPeripheralDirect(out, in.CoreComponentID, roles); err != nil {
		t.Fatal("physical ownership guard disagrees with planner", err)
	}
	if findings := layoutContractRuntimeFindings(t, out); len(findings) > 0 {
		t.Fatalf("runtime raw-angle guard disagrees with planner: %+v", findings)
	}
	plan := powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags}
	if err = validateLibGeometry(&plan); err != nil {
		t.Fatal("complete geometry gate", err)
	}
	if err = validateSchCompositionNets(&plan); err != nil {
		t.Fatal("complete naming gate", err)
	}
	segments, err := schTerminalSegments(&plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range in.Components {
		if !reflect.DeepEqual(out.PinStates[source.ID], source.PinStates) {
			t.Fatalf("lost explicit NC state for %s", source.ID)
		}
		if out.ComponentIDs[source.Measurement.Designator] != source.ID {
			t.Fatal("lost opaque identity")
		}
		for _, placed := range out.Placements {
			if out.ComponentIDs[placed.Designator] != source.ID {
				continue
			}
			for _, pin := range placed.Pins {
				if source.PinStates[pin.Number] != "nc" {
					continue
				}
				if pin.Net != "" {
					t.Fatal("NC became a net")
				}
				for _, w := range segments {
					if plOnSegment([2]float64{pin.X, pin.Y}, w.Points[0], w.Points[1]) {
						t.Fatal("explicit NC acquired a wire")
					}
				}
			}
		}
	}
}

func layoutContractSolve(t *testing.T, in SchematicLayoutInput) *SchematicLayoutResult {
	t.Helper()
	before := layoutContractJSON(t, in)
	first, err := PlanSchematicLayout(in)
	if err != nil {
		t.Fatal("small synthetic contract fixture unexpectedly failed", err)
	}
	layoutContractAssertComplete(t, in, first)
	second, err := PlanSchematicLayout(in)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("repeated solve is not deterministic", err)
	}
	if !bytes.Equal(before, layoutContractJSON(t, in)) {
		t.Fatal("solver mutated source data")
	}
	return first
}

func TestLayoutContractOpaqueIdentityAndNetRenaming(t *testing.T) {
	for _, names := range []struct{ ids, refs, nets [2]string }{
		{[2]string{"opaque-alpha", "opaque-beta"}, [2]string{"X7", "Y4"}, [2]string{"RAIL_X", "EARTH_"}},
		{[2]string{"17/nonsemantic", "branch:epsilon"}, [2]string{"ZZ1", "AA2"}, [2]string{"arbitrary_supply", "arbitrary_return"}},
	} {
		t.Run(names.refs[0]+"-"+names.refs[1], func(t *testing.T) {
			in := layoutContractFixture()
			in.CoreComponentID = names.ids[0]
			in.NetPolicies = map[string]string{names.nets[0]: "local_power", names.nets[1]: "local_ground"}
			for i := range in.Components {
				in.Components[i].ID = names.ids[i]
				in.Components[i].Measurement.Designator = names.refs[i]
				for j := range in.Components[i].Measurement.Pins {
					q := &in.Components[i].Measurement.Pins[j]
					switch q.Net {
					case "SUPPLY":
						q.Net = names.nets[0]
					case "RETURN":
						q.Net = names.nets[1]
					}
				}
			}
			layoutContractSolve(t, in)
		})
	}
}

func TestLayoutContractGridTranslationIsSourceInvariant(t *testing.T) {
	baseline := layoutContractSolve(t, layoutContractFixture())
	for _, delta := range [][2]float64{{-735, 215}, {1200, -995}, {-5, -5}} {
		t.Run(fmt.Sprint(delta), func(t *testing.T) {
			in := layoutContractFixture()
			for i := range in.Components {
				in.Components[i].Measurement = plTranslate(in.Components[i].Measurement, delta[0], delta[1])
			}
			out := layoutContractSolve(t, in)
			if !reflect.DeepEqual(baseline, out) {
				t.Fatal("absolute source origin affected core-normalized result")
			}
		})
	}
}

func TestLayoutContractFourRotationsAndMeasuredMirror(t *testing.T) {
	for _, mirror := range []bool{false, true} {
		for quarter := 0; quarter < 4; quarter++ {
			t.Run(fmt.Sprintf("mirror=%t/rotation=%d", mirror, quarter*90), func(t *testing.T) {
				in := layoutContractFixture()
				for i := range in.Components {
					c := in.Components[i].Measurement
					if mirror {
						c = layoutContractReflect(c)
					}
					in.Components[i].Measurement = plRotate(c, quarter)
				}
				out := layoutContractSolve(t, in)
				for _, c := range out.Placements {
					if c.Mirror != mirror || c.Rotation != float64(quarter*90) {
						t.Fatal("fixed measured pose was changed")
					}
				}
			})
		}
	}
}

func TestLayoutContractBoundedFailureNeverReturnsPartialPlan(t *testing.T) {
	failures := 0
	for _, limit := range []int{1, 2, 8, 32} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			in := layoutContractFixture()
			in.MaxCandidates = limit
			before := layoutContractJSON(t, in)
			remaining := limit
			out, err := planSchematicLayoutWithBudget(in, &remaining)
			if remaining < 0 || remaining > limit {
				t.Fatalf("budget overrun/reset: initial=%d remaining=%d", limit, remaining)
			}
			if err != nil {
				failures++
				if out != nil {
					t.Fatal("failed search returned a partial plan")
				}
			} else {
				layoutContractAssertComplete(t, in, out)
				if out.CandidatesUsed != limit-remaining {
					t.Fatal("candidate report disagrees with consumed budget")
				}
			}
			if !bytes.Equal(before, layoutContractJSON(t, in)) {
				t.Fatal("failure path mutated input")
			}
		})
	}
	if failures == 0 {
		t.Fatal("negative budget control did not exercise failure")
	}
}

func TestLayoutContractSameNameMarkersCannotReplacePhysicalCoreBranch(t *testing.T) {
	in := layoutContractFixture()
	good := layoutContractSolve(t, in)
	var bad SchematicLayoutResult
	if err := json.Unmarshal(layoutContractJSON(t, good), &bad); err != nil {
		t.Fatal(err)
	}
	bad.Wires = nil
	bad.Flags = nil
	// Deliberately sever the successful solution into isolated, correctly
	// oriented named stubs. Both pins still advertise the identical rail names.
	for i := range bad.Placements {
		if bad.ComponentIDs[bad.Placements[i].Designator] != in.CoreComponentID {
			bad.Placements[i] = plTranslate(bad.Placements[i], 300, 200)
		}
		for _, q := range bad.Placements[i].Pins {
			if q.Net == "" {
				continue
			}
			direction := map[float64]string{0: "right", 90: "up", 180: "left", 270: "down"}[*q.Rotation]
			kind := "power"
			if in.NetPolicies[q.Net] == "local_ground" {
				kind = "ground"
			}
			bad.Flags = append(bad.Flags, SchematicMarker{Net: q.Net, Kind: kind, PinX: q.X, PinY: q.Y, Direction: direction, Offset: 10})
		}
	}
	if findings := layoutContractRuntimeFindings(t, &bad); len(findings) > 0 {
		t.Fatalf("negative control must retain legal pin exits: %+v", findings)
	}
	err := validateSchematicLayoutPeripheralDirect(&bad, in.CoreComponentID, map[string]string{"SUPPLY": "power", "RETURN": "ground"})
	if err == nil || !strings.Contains(err.Error(), "peripheral-direct-missing") {
		t.Fatalf("label-only branch accepted or wrong gate exercised: %v", err)
	}
}
