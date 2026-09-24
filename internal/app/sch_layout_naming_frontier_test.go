package app

import (
	"errors"
	"testing"
)

func TestSingletonPortPlacementPreservesMeasuredNamingExit(t *testing.T) {
	core := powerLayoutPlacement{Designator: "U1", BBox: layoutBBox{-35.5, -210.5, 35.5, 210.5}, Pins: []powerLayoutPin{
		{Number: "2", Net: "V3V3", X: -45, Y: 15, Rotation: directionNumber(180)},
		{Number: "3", Net: "MCU_EN", X: -45, Y: 5, Rotation: directionNumber(180)},
		{Number: "36", Net: "UART0_RX", X: -45, Y: -5, Rotation: directionNumber(180)},
		{Number: "37", Net: "UART0_TX", X: -45, Y: -15, Rotation: directionNumber(180)},
	}}
	q1 := powerLayoutPlacement{Designator: "Q1", X: -65, Y: -25, BBox: layoutBBox{-65.5, -35.5, -54.5, -14.5},
		TextBBoxes: []layoutBBox{{-50, -25, -40.662109375, -17}}, Pins: []powerLayoutPin{
			{Number: "3", Net: "MCU_EN", X: -55, Y: -5, Rotation: directionNumber(90)},
			{Number: "1", Net: "DTR_BASE", X: -75, Y: -25, Rotation: directionNumber(180)},
			{Number: "2", Net: "RTS_N", X: -55, Y: -45, Rotation: directionNumber(270)},
		}}
	c5 := powerLayoutPlacement{Designator: "C5", X: -65, Y: 20, BBox: layoutBBox{-75.5, 11.5, -54.5, 28.5}, TextBBoxes: []layoutBBox{{-75, 30, -66.0517578125, 38}},
		Pins: []powerLayoutPin{{Number: "1", Net: "V3V3", X: -85, Y: 20, Rotation: directionNumber(180)}, {Number: "2", Net: "GND", X: -45, Y: 20, Rotation: directionNumber(0)}}}
	c6 := powerLayoutPlacement{Designator: "C6", X: -65, Y: 55, BBox: layoutBBox{-75.5, 46.5, -54.5, 63.5}, TextBBoxes: []layoutBBox{{-75, 65, -66.0517578125, 73}},
		Pins: []powerLayoutPin{{Number: "2", Net: "GND", X: -45, Y: 55, Rotation: directionNumber(0)}, {Number: "1", Net: "V3V3", X: -85, Y: 55, Rotation: directionNumber(180)}}}
	before := powerLayoutPlan{Placements: []powerLayoutPlacement{core, c5, c6}}
	after := powerLayoutPlan{Placements: []powerLayoutPlacement{core, c5, c6, q1}}
	if err := validateLibGeometry(&after); err != nil {
		t.Fatal("invalid measured counterexample", err)
	}
	port := libIsland{net: "UART0_RX", pins: []powerLayoutPin{core.Pins[2]}}
	budget := 10000
	if ok, complete := libNamingFrontier(&before, port, "module_port", &budget); !ok || !complete {
		t.Fatal("baseline core pin has no naming lead")
	}
	budget = 10000
	if ok, complete := libNamingFrontier(&after, port, "module_port", &budget); ok || !complete {
		t.Fatal("measured Q1 pose did not seal UART0_RX", ok, complete)
	}
	ctx := &schematicRoutingContext{netPins: map[string]int{"UART0_RX": 1, "UART0_TX": 1}}
	policies := map[string]string{"UART0_RX": "module_port", "UART0_TX": "module_port"}
	budget = 10000
	var obstruction *schGeometryObstruction
	if err := libValidateSingletonNamingPlacement(&before, &after, q1, policies, ctx, &budget); !errors.As(err, &obstruction) || obstruction.kind != "marker-frontier-sealed" || len(obstruction.blockers) != 1 || obstruction.blockers[0] != "Q1" {
		t.Fatal("bad Q1 pose was not rejected with exact blocker", err)
	}
	budget = 1
	if err := libValidateSingletonNamingPlacement(&before, &after, q1, policies, ctx, &budget); !errors.Is(err, errLibLayoutBudget) {
		t.Fatal("incomplete marker probe was treated as geometric proof", err)
	}
}

func TestSingletonPortLeadCanBeSealedBeyondLocalPinRadius(t *testing.T) {
	// Distilled from an MCU zone: a marker can escape past the nearby Q1,
	// while R5 farther down that escape corridor closes the only legal lead.
	core := powerLayoutPlacement{Designator: "U1", BBox: layoutBBox{-35.5, -210.5, 35.5, 210.5}, Pins: []powerLayoutPin{
		{Number: "2", Net: "V3V3", X: -45, Y: 15, Rotation: directionNumber(180)},
		{Number: "3", Net: "MCU_EN", X: -45, Y: 5, Rotation: directionNumber(180)},
		{Number: "36", Net: "UART0_RX", X: -45, Y: -5, Rotation: directionNumber(180)},
		{Number: "37", Net: "UART0_TX", X: -45, Y: -15, Rotation: directionNumber(180)},
	}}
	c5 := powerLayoutPlacement{Designator: "C5", X: -65, Y: 20, BBox: layoutBBox{-75.5, 11.5, -54.5, 28.5}, TextBBoxes: []layoutBBox{{-75, 30, -66.0517578125, 38}},
		Pins: []powerLayoutPin{{Number: "1", Net: "V3V3", X: -85, Y: 20, Rotation: directionNumber(180)}, {Number: "2", Net: "GND", X: -45, Y: 20, Rotation: directionNumber(0)}}}
	c6 := powerLayoutPlacement{Designator: "C6", X: -65, Y: 55, BBox: layoutBBox{-75.5, 46.5, -54.5, 63.5}, TextBBoxes: []layoutBBox{{-75, 65, -66.0517578125, 73}},
		Pins: []powerLayoutPin{{Number: "2", Net: "GND", X: -45, Y: 55, Rotation: directionNumber(0)}, {Number: "1", Net: "V3V3", X: -85, Y: 55, Rotation: directionNumber(180)}}}
	q1 := powerLayoutPlacement{Designator: "Q1", X: -75, Y: -20, BBox: layoutBBox{-75.5, -30.5, -64.5, -9.5}, TextBBoxes: []layoutBBox{{-60, -20, -50.662109375, -12}},
		Pins: []powerLayoutPin{{Number: "3", Net: "MCU_EN", X: -65, Y: 0, Rotation: directionNumber(90)}, {Number: "1", Net: "DTR_BASE", X: -85, Y: -20, Rotation: directionNumber(180)}, {Number: "2", Net: "RTS_N", X: -65, Y: -40, Rotation: directionNumber(270)}}}
	r5 := powerLayoutPlacement{Designator: "R5", X: -115, Y: 20, BBox: layoutBBox{-125.5, 15.5, -104.5, 24.5}, TextBBoxes: []layoutBBox{{-125, 25, -116.0517578125, 33}},
		Pins: []powerLayoutPin{{Number: "2", Net: "V3V3", X: -95, Y: 20, Rotation: directionNumber(0)}, {Number: "1", Net: "MCU_EN", X: -135, Y: 20, Rotation: directionNumber(180)}}}
	before := powerLayoutPlan{Placements: []powerLayoutPlacement{
		core, c5, c6, q1,
	}}
	after := before
	after.Placements = append(append([]powerLayoutPlacement(nil), before.Placements...), r5)
	if err := validateLibGeometry(&after); err != nil {
		t.Fatal("counterexample has invalid base geometry", err)
	}
	corePin, ok := libPin(before.Placements[0], "36")
	if !ok || corePin.Net != "UART0_RX" {
		t.Fatal("source core UART0_RX pin changed")
	}
	island := libIsland{net: corePin.Net, pins: []powerLayoutPin{corePin}}
	budget := 10000
	if reachable, complete := libNamingFrontier(&before, island, "module_port", &budget); !reachable || !complete {
		t.Fatal("baseline lead missing")
	}
	budget = 10000
	if reachable, complete := libNamingFrontier(&after, island, "module_port", &budget); reachable || !complete {
		t.Fatal("distant R5 did not seal the lead", reachable, complete)
	}
	ctx := &schematicRoutingContext{netPins: map[string]int{"UART0_RX": 1}}
	budget = 10000
	var obstruction *schGeometryObstruction
	if err := libValidateSingletonNamingPlacement(&before, &after, r5, map[string]string{"UART0_RX": "module_port"}, ctx, &budget); !errors.As(err, &obstruction) || obstruction.kind != "marker-frontier-sealed" || len(obstruction.blockers) != 1 || obstruction.blockers[0] != "R5" {
		t.Fatal("distant lead blocker was skipped", err)
	}
}
