package app

import (
	"errors"
	"testing"
)

func TestSingletonNamingPreflightReportsConflictBeforeJointBudget(t *testing.T) {
	core := powerLayoutPlacement{Designator: "A", BBox: layoutBBox{-35.5, -210.5, 35.5, 210.5}, Pins: []powerLayoutPin{
		{Number: "2", Net: "POWER", X: -45, Y: 15, Rotation: directionNumber(180)},
		{Number: "3", Net: "CONTROL", X: -45, Y: 5, Rotation: directionNumber(180)},
		{Number: "36", Net: "PORT_RX", X: -45, Y: -5, Rotation: directionNumber(180)},
		{Number: "37", Net: "PORT_TX", X: -45, Y: -15, Rotation: directionNumber(180)},
	}}
	blocker := powerLayoutPlacement{Designator: "Q", X: -65, Y: -25, BBox: layoutBBox{-65.5, -35.5, -54.5, -14.5},
		TextBBoxes: []layoutBBox{{-50, -25, -40.662109375, -17}}, Pins: []powerLayoutPin{
			{Number: "3", Net: "CONTROL", X: -55, Y: -5, Rotation: directionNumber(90)},
			{Number: "1", Net: "BASE", X: -75, Y: -25, Rotation: directionNumber(180)},
			{Number: "2", Net: "OTHER", X: -55, Y: -45, Rotation: directionNumber(270)},
		}}
	upper := powerLayoutPlacement{Designator: "C1", X: -65, Y: 20, BBox: layoutBBox{-75.5, 11.5, -54.5, 28.5}, TextBBoxes: []layoutBBox{{-75, 30, -66.0517578125, 38}},
		Pins: []powerLayoutPin{{Number: "1", Net: "POWER", X: -85, Y: 20, Rotation: directionNumber(180)}, {Number: "2", Net: "GROUND", X: -45, Y: 20, Rotation: directionNumber(0)}}}
	lower := powerLayoutPlacement{Designator: "C2", X: -65, Y: 55, BBox: layoutBBox{-75.5, 46.5, -54.5, 63.5}, TextBBoxes: []layoutBBox{{-75, 65, -66.0517578125, 73}},
		Pins: []powerLayoutPin{{Number: "2", Net: "GROUND", X: -45, Y: 55, Rotation: directionNumber(0)}, {Number: "1", Net: "POWER", X: -85, Y: 55, Rotation: directionNumber(180)}}}
	plan := powerLayoutPlan{Placements: []powerLayoutPlacement{core, upper, lower, blocker}}
	if err := validateLibGeometry(&plan); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"module_port", "direct"} {
		t.Run(policy, func(t *testing.T) {
			trial := plan
			budget := 10000
			err := libNameIslands(&trial, map[string]string{"PORT_RX": policy, "PORT_TX": "module_port"}, &budget)
			var conflict *schematicNamingConflict
			if !errors.As(err, &conflict) || conflict.net != "PORT_RX" || budget < 9500 || len(trial.Flags) != 0 {
				t.Fatalf("sealed island did not fail early without publishing labels: err=%v remaining=%d flags=%d", err, budget, len(trial.Flags))
			}
		})
	}
}
