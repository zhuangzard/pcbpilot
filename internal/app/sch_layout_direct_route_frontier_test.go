package app

import "testing"

func TestDirectRoutePreservesOtherUnfinishedIslandExit(t *testing.T) {
	a := powerLayoutPlacement{Designator: "A", BBox: layoutBBox{-110, -10, -80, 10}, Pins: []powerLayoutPin{{Number: "1", Net: "N", X: -70, Y: 0, Rotation: mazeTestRotation(0)}}}
	b := powerLayoutPlacement{Designator: "B", BBox: layoutBBox{80, -10, 110, 10}, Pins: []powerLayoutPin{{Number: "1", Net: "N", X: 70, Y: 0, Rotation: mazeTestRotation(180)}}}
	before := powerLayoutPlan{Placements: []powerLayoutPlacement{a, b}}
	// Three sides of a foreign net's path leave the source pin an exit to
	// the right. Teeth make an interior X crossing of those sides illegal.
	before.Wires = []powerLayoutWire{
		{Net: "M", Points: [][2]float64{{-100, -20}, {-100, 20}}},
		{Net: "M", Points: [][2]float64{{-100, 20}, {-40, 20}}},
		{Net: "M", Points: [][2]float64{{-100, -20}, {-40, -20}}},
	}
	for y := -20.0; y <= 20; y += 5 {
		before.Wires = append(before.Wires, powerLayoutWire{Net: "M", Points: [][2]float64{{-100, y}, {-105, y}}})
	}
	for x := -100.0; x <= -40; x += 5 {
		before.Wires = append(before.Wires,
			powerLayoutWire{Net: "M", Points: [][2]float64{{x, 20}, {x, 25}}},
			powerLayoutWire{Net: "M", Points: [][2]float64{{x, -20}, {x, -25}}})
	}
	after := before
	after.Wires = clonePowerLayoutWires(before.Wires)
	after.Wires = append(after.Wires, powerLayoutWire{Net: "M", Points: [][2]float64{{-40, -20}, {-40, 20}}})
	for y := -20.0; y <= 20; y += 5 {
		after.Wires = append(after.Wires, powerLayoutWire{Net: "M", Points: [][2]float64{{-40, y}, {-35, y}}})
	}
	if !libDirectIslandLocalFrontier(&before, libIslands(&before)[0], 40, 4096).reachable {
		t.Fatal("open source island fixture has no exit")
	}
	if libDirectRouteKeepsFrontiers(&before, &after, map[string]string{"N": "direct", "M": "module_port"}) {
		t.Fatal("route that newly encloses an unfinished direct island was accepted")
	}
	if !libDirectRouteKeepsFrontiers(&before, &after, map[string]string{"N": "module_port", "M": "module_port"}) {
		t.Fatal("non-direct island was incorrectly treated as mandatory")
	}
	if !libDirectRouteKeepsFrontiers(&after, &after, map[string]string{"N": "direct", "M": "module_port"}) {
		t.Fatal("pre-existing closure was incorrectly blamed on an unchanged route")
	}
}
