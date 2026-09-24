package app

import "testing"

func TestRouteNamingClosedBaselineCacheIsStable(t *testing.T) {
	part := powerLayoutPlacement{Designator: "A", BBox: layoutBBox{-20, -20, -10, 20}, Pins: []powerLayoutPin{{Number: "1", Net: "PORT", X: 0, Y: 0, Rotation: directionNumber(0)}}}
	before := powerLayoutPlan{Placements: []powerLayoutPlacement{part}}
	before.Wires = []powerLayoutWire{{Net: "OTHER", Points: [][2]float64{{10, -300}, {10, 300}}}}
	for y := -300.0; y <= 300; y += 5 {
		before.Wires = append(before.Wires, powerLayoutWire{Net: "OTHER", Points: [][2]float64{{10, y}, {15, y}}})
	}
	before.Wires = append(before.Wires,
		powerLayoutWire{Net: "OTHER", Points: [][2]float64{{5, 10}, {10, 10}}},
		powerLayoutWire{Net: "OTHER", Points: [][2]float64{{5, -10}, {10, -10}}},
	)
	if err := validateLibGeometry(&before); err != nil {
		t.Fatal(err)
	}
	probeBudget := 2048
	if open, complete := libNamingFrontier(&before, libIslands(&before)[0], "module_port", &probeBudget); open || !complete {
		t.Fatalf("fixture does not have a proven closed baseline: open=%v complete=%v", open, complete)
	}
	after := before
	after.Wires = append(clonePowerLayoutWires(before.Wires), powerLayoutWire{Net: "OTHER", Points: [][2]float64{{100, -20}, {100, 20}}})
	if err := validateLibGeometry(&after); err != nil {
		t.Fatal(err)
	}
	policies := map[string]string{"PORT": "module_port", "OTHER": "direct"}
	budget := 5000
	ctx := &schematicRoutingContext{candidateBudget: &budget}
	firstOK, known := libRouteKeepsCompletedNamingFrontiers(&before, &after, policies, ctx, nil)
	if !firstOK {
		t.Fatal("unrelated route rejected on first call")
	}
	secondOK, _ := libRouteKeepsCompletedNamingFrontiers(&before, &after, policies, ctx, known)
	if !secondOK {
		t.Fatal("same unrelated route rejected after caching the closed baseline")
	}
}
