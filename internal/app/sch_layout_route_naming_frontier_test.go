package app

import "testing"

func TestRouteNamingProbeUsesSharedCandidateBudget(t *testing.T) {
	part := powerLayoutPlacement{Designator: "A", BBox: layoutBBox{-20, -20, -10, 20}, Pins: []powerLayoutPin{{Number: "1", Net: "PORT", X: 0, Y: 0, Rotation: directionNumber(0)}}}
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{part}}
	island := libIslands(&p)[0]
	if witness, complete := libRouteNamingProbe(&p, island, "module_port", nil); witness != nil || complete {
		t.Fatal("standalone route performed an unmetered marker proof")
	}
	if witness, complete := libRouteNamingProbe(&p, island, "module_port", &schematicRoutingContext{}); witness != nil || complete {
		t.Fatal("provisional rail route performed an unmetered marker proof")
	}
	budget := 100
	ctx := &schematicRoutingContext{candidateBudget: &budget}
	if witness, complete := libRouteNamingProbe(&p, island, "module_port", ctx); witness == nil || !complete || budget >= 100 {
		t.Fatalf("budgeted route marker proof did not debit shared allowance: complete=%v remaining=%d", complete, budget)
	}
}

func TestRouteNamingFrontierRejectsNewlySealedSingleton(t *testing.T) {
	part := powerLayoutPlacement{Designator: "A", BBox: layoutBBox{-20, -20, -10, 20}, Pins: []powerLayoutPin{{Number: "1", Net: "PORT", X: 0, Y: 0, Rotation: directionNumber(0)}}}
	before := powerLayoutPlan{Placements: []powerLayoutPlacement{part}}
	after := before
	after.Wires = []powerLayoutWire{{Net: "OTHER", Points: [][2]float64{{10, -300}, {10, 300}}}}
	for y := -300.0; y <= 300; y += 5 {
		after.Wires = append(after.Wires, powerLayoutWire{Net: "OTHER", Points: [][2]float64{{10, y}, {15, y}}})
	}
	after.Wires = append(after.Wires,
		powerLayoutWire{Net: "OTHER", Points: [][2]float64{{5, 10}, {10, 10}}},
		powerLayoutWire{Net: "OTHER", Points: [][2]float64{{5, -10}, {10, -10}}},
	)
	if err := validateLibGeometry(&after); err != nil {
		t.Fatalf("foreign route counterexample is not geometrically legal: %v", err)
	}
	probe := 2000
	if reachable, complete := libNamingFrontier(&after, libIslands(&after)[0], "module_port", &probe); reachable || !complete {
		t.Fatalf("foreign route did not seal the singleton marker frontier: reachable=%v complete=%v", reachable, complete)
	}
	budget := 2000
	if ok, complete := libNamingFrontier(&before, libIslands(&before)[0], "module_port", &budget); !ok || !complete {
		t.Fatal("baseline port has no legal naming lead")
	}
	ctx := &schematicRoutingContext{}
	allowance := 2000
	ctx.candidateBudget = &allowance
	keeps, _ := libRouteKeepsCompletedNamingFrontiers(&before, &after, map[string]string{"PORT": "module_port", "OTHER": "direct"}, ctx, nil)
	if keeps || allowance <= 0 {
		t.Fatalf("foreign route sealing the singleton was accepted or misreported as resource exhaustion: keeps=%v remaining=%d", keeps, allowance)
	}
	// An incomplete probe is not a proof of closure.
	tiny := 1
	ctx.candidateBudget = &tiny
	keeps, _ = libRouteKeepsCompletedNamingFrontiers(&before, &after, map[string]string{"PORT": "module_port", "OTHER": "direct"}, ctx, nil)
	if !keeps {
		t.Fatal("incomplete naming probe incorrectly pruned the route")
	}
	reserved := 2000
	ctx.candidateBudget = &reserved
	ctx.namingReserve = 1999
	keeps, _ = libRouteKeepsCompletedNamingFrontiers(&before, &after, map[string]string{"PORT": "module_port", "OTHER": "direct"}, ctx, nil)
	if !keeps || reserved < ctx.namingReserve {
		t.Fatalf("route lookahead consumed the final naming reserve: keeps=%v remaining=%d", keeps, reserved)
	}
	ctx.namingReserve = 0
	// A nearby route that leaves an alternate legal lead must be retained.
	open := before
	open.Wires = []powerLayoutWire{{Net: "OTHER", Points: [][2]float64{{10, -300}, {10, 300}}}}
	if err := validateLibGeometry(&open); err != nil {
		t.Fatal(err)
	}
	allowance = 2000
	ctx.candidateBudget = &allowance
	keeps, _ = libRouteKeepsCompletedNamingFrontiers(&before, &open, map[string]string{"PORT": "module_port", "OTHER": "direct"}, ctx, nil)
	if !keeps {
		t.Fatal("route with an alternate marker lead was rejected")
	}
}
