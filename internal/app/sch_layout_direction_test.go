package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/schguard"
)

func directionNumber(x float64) *float64 { return &x }

func TestPlannerPinDirectionMatchesExecutionGuard(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		for _, tt := range []struct {
			name string
			end  [2]float64
			pass bool
		}{{"outward", [2]float64{40, 0}, true}, {"sideways", [2]float64{30, 10}, false}, {"inward", [2]float64{25, 0}, false}} {
			t.Run(tt.name, func(t *testing.T) {
				pin := powerLayoutPin{Number: "1", Net: "N", X: 30, Y: 0}
				if explicit {
					pin.Rotation = directionNumber(0)
				}
				p := powerLayoutPlan{Placements: []powerLayoutPlacement{{Designator: "U1", BBox: layoutBBox{-20, -20, 20, 20}, Pins: []powerLayoutPin{pin}}}, Wires: []powerLayoutWire{{Net: "N", Points: [][2]float64{{30, 0}, tt.end}}}}
				err := validatePowerLayout(&p, layoutBBox{-1000, -1000, 1000, 1000})
				if (err == nil) != tt.pass {
					t.Fatalf("explicit=%v err=%v", explicit, err)
				}
				if !tt.pass && !strings.Contains(err.Error(), "pin-exit-direction") {
					t.Fatal(err)
				}
				live := map[string]any{"components": []any{map[string]any{"componentType": "part", "designator": "U1", "bbox": map[string]any{"minX": -20.0, "maxX": 20.0, "minY": -20.0, "maxY": 20.0}, "pinsAvailable": true, "pins": []any{map[string]any{"pinNumber": "1", "x": 30.0, "y": 0.0, "rotation": 0.0}}}}, "wires": []any{map[string]any{"points": p.Wires[0].Points}}}
				if got := len(schguard.AnalyzeWireGeometry(live)) == 0; got != tt.pass {
					t.Fatalf("offline/runtime mismatch: %+v", schguard.AnalyzeWireGeometry(live))
				}
			})
		}
	}
}

func TestPlannerDirectionRotationPreservesRawEvidence(t *testing.T) {
	p := powerLayoutPlacement{X: 100, Y: 100, Rotation: 270, Mirror: true, BBox: layoutBBox{90, 90, 110, 110}, Pins: []powerLayoutPin{{Number: "1", X: 120, Y: 100, Rotation: directionNumber(0)}}}
	before, _ := json.Marshal(p)
	turned := plRotate(p, 1)
	after, _ := json.Marshal(p)
	if string(before) != string(after) || turned.Pins[0].X != 100 || turned.Pins[0].Y != 120 || *turned.Pins[0].Rotation != 90 {
		t.Fatalf("world rotation transform/source corruption: %+v", turned)
	}
	if side, err := libPinSide(powerLayoutPin{Number: "1", X: 30, Y: 30, Rotation: directionNumber(90)}, layoutBBox{-20, -20, 20, 20}); err != nil || side != "up" {
		t.Fatalf("official angle was replaced with ambiguous bbox guess: %s %v", side, err)
	}
	if _, err := libPinSide(powerLayoutPin{Number: "1", X: 30, Y: 30}, layoutBBox{-20, -20, 20, 20}); err == nil {
		t.Fatal("legacy ambiguous side accepted")
	}
}

func TestPlannerRoutesHonorBothOutwardEndpoints(t *testing.T) {
	for _, a := range []float64{0, 90, 180, 270} {
		for _, b := range []float64{0, 90, 180, 270} {
			pa, pb := powerLayoutPin{Number: "1", Net: "N", X: 0, Y: 0, Rotation: directionNumber(a)}, powerLayoutPin{Number: "2", Net: "N", X: 50, Y: 40, Rotation: directionNumber(b)}
			routes := append(libRoutes(pa, pb), libDetourRoutes(pa, pb)...)
			if len(routes) == 0 {
				t.Fatalf("no outward route candidate for %g/%g", a, b)
			}
			for _, r := range routes {
				if !libRouteOutward(r, pa, pb, a, b, true, true) {
					t.Fatalf("invalid route %g/%g: %+v", a, b, r)
				}
			}
		}
	}
}

func TestPlannerOnlyOwnOutwardPinCanExitBodyStrokeHalo(t *testing.T) {
	c := powerLayoutPlacement{Designator: "U1", BBox: layoutBBox{-10.5, -10.5, 10.5, 10.5}, Pins: []powerLayoutPin{{Number: "1", Net: "N", X: 10, Y: 0, Rotation: directionNumber(0)}}}
	for _, tt := range []struct {
		a, b [2]float64
		pass bool
	}{{[2]float64{10, 0}, [2]float64{20, 0}, true}, {[2]float64{10, 5}, [2]float64{20, 5}, false}, {[2]float64{10, 0}, [2]float64{0, 0}, false}} {
		p := powerLayoutPlan{Placements: []powerLayoutPlacement{c}, Wires: []powerLayoutWire{{Net: "N", Points: [][2]float64{tt.a, tt.b}}}}
		if e := validatePowerLayout(&p, layoutBBox{-1000, -1000, 1000, 1000}); (e == nil) != tt.pass {
			t.Fatalf("halo pass=%v err=%v", tt.pass, e)
		}
	}
}

func TestPinExitSpaceObligationRejectsTightBodyBeforeRouting(t *testing.T) {
	for _, tt := range []struct {
		name string
		edge float64
		net  string
		pass bool
	}{{"only-1.5-raw", -11.5, "N", false}, {"exactly-five-raw", -15, "N", true}, {"nc-no-wire-obligation", -11.5, "", true}} {
		t.Run(tt.name, func(t *testing.T) {
			p := powerLayoutPlan{Placements: []powerLayoutPlacement{
				{Designator: "U1", BBox: layoutBBox{0, -20, 30, 20}, Pins: []powerLayoutPin{{Number: "1", Net: tt.net, X: -10, Y: 0, Rotation: directionNumber(180)}}},
				{Designator: "C1", BBox: layoutBBox{tt.edge - 20, -10, tt.edge, 10}},
			}}
			before, _ := json.Marshal(p)
			e := libValidatePinExitSpace(&p, nil, nil)
			after, _ := json.Marshal(p)
			if (e == nil) != tt.pass {
				t.Fatalf("pass=%v err=%v", tt.pass, e)
			}
			if string(before) != string(after) {
				t.Fatal("exit reservation persisted fake geometry")
			}
		})
	}
}

func TestPinExitSpaceForeignObstaclesAndSameNetJunction(t *testing.T) {
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{{Designator: "U1", BBox: layoutBBox{-20, -20, 20, 20}, Pins: []powerLayoutPin{{Number: "1", Net: "N", X: 30, Y: 0, Rotation: directionNumber(0)}}}}}
	for _, net := range []string{"N", "FOREIGN"} {
		w := []powerLayoutWire{{Net: net, Points: [][2]float64{{35, -10}, {35, 10}}}}
		e := libValidatePinExitSpace(&p, nil, w)
		if (e == nil) != (net == "N") {
			t.Fatalf("same-net touch vs foreign: %s %v", net, e)
		}
	}
	p.Placements[0].TextBBoxes = []layoutBBox{{31, -2, 36, 2}}
	if e := libValidatePinExitSpace(&p, nil, nil); e == nil || !strings.Contains(e.Error(), "Designator") {
		t.Fatal("measured Designator did not block exit", e)
	}
}

func TestPinExitSpaceRejectsForeignExitObligationContact(t *testing.T) {
	plan := powerLayoutPlan{Placements: []powerLayoutPlacement{
		{Designator: "J1", BBox: layoutBBox{-80, -30, -60, 10}, Pins: []powerLayoutPin{{Number: "A5", Net: "CC1", X: -55, Y: -10, Rotation: directionNumber(0)}}},
		{Designator: "D1", BBox: layoutBBox{-40, -20, -20, 0}, Pins: []powerLayoutPin{{Number: "3", Net: "USB_DM", X: -45, Y: -10, Rotation: directionNumber(180)}}},
	}}
	before, _ := json.Marshal(plan)
	err := libValidatePinExitSpace(&plan, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "foreign exit obligation") {
		t.Fatalf("opposing foreign-net exits were accepted: %v", err)
	}
	var obstruction *schGeometryObstruction
	if !errors.As(err, &obstruction) || obstruction.kind != "pin-exit-foreign-exit" || !obstruction.complete {
		t.Fatalf("missing structured obstruction provenance: %#v", err)
	}
	after, _ := json.Marshal(plan)
	if string(before) != string(after) {
		t.Fatal("exit obligation validation mutated source data")
	}

}
