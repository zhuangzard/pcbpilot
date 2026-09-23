package app

import (
	"errors"
	"testing"
)

// A long core and an occupied branch leave a narrow corridor for Q's child R.
func sameShellCorridorFixture() (powerLayoutPlan, powerLayoutPlacement, powerLayoutPlacement, libAttachmentPair) {
	core := powerLayoutPlacement{Designator: "U", BBox: layoutBBox{-20, -100, 20, 100}, Pins: []powerLayoutPin{
		{Number: "1", Net: "BOOT", X: 30, Y: 90, Rotation: directionNumber(0)},
	}}
	branch := powerLayoutPlacement{Designator: "B", X: 60, Y: 80, BBox: layoutBBox{50, 75, 70, 85}, Pins: []powerLayoutPin{
		{Number: "1", Net: "BOOT", X: 40, Y: 80, Rotation: directionNumber(180)},
	}}
	occupied := powerLayoutPlacement{Designator: "W", X: 15, Y: 135, BBox: layoutBBox{0, 125, 29, 145}, TextBBoxes: []layoutBBox{{0, 150, 5, 158}}}
	q := powerLayoutPlacement{Designator: "Q", BBox: layoutBBox{-5, -5, 5, 5}, Pins: []powerLayoutPin{
		{Number: "3", Net: "BOOT", X: 10, Y: 0, Rotation: directionNumber(0)},
		{Number: "1", Net: "BASE", X: -10, Y: 0, Rotation: directionNumber(180)},
	}}
	r := powerLayoutPlacement{Designator: "R", BBox: layoutBBox{-5, -5, 5, 5}, Pins: []powerLayoutPin{
		{Number: "2", Net: "BASE", X: 15, Y: 0, Rotation: directionNumber(0)},
	}}
	plan := powerLayoutPlan{Placements: []powerLayoutPlacement{core, branch, occupied}, Wires: []powerLayoutWire{
		{Net: "BOOT", Points: [][2]float64{{30, 90}, {35, 90}}},
		{Net: "BOOT", Points: [][2]float64{{35, 90}, {35, 80}}},
		{Net: "BOOT", Points: [][2]float64{{35, 80}, {40, 80}}},
	}}
	return plan, q, r, libAttachmentPair{host: core.Pins[0], own: q.Pins[0], side: "right"}
}

func sameShellPolicies() map[string]string {
	return map[string]string{"BOOT": "direct", "BASE": "direct"}
}

func TestSameShellManualCompleteWitness(t *testing.T) {
	base, q, r, _ := sameShellCorridorFixture()
	if err := validateLibGeometry(&base); err != nil {
		t.Fatalf("invalid measured starting state: %v", err)
	}
	witness := base
	witness.Placements = append(append([]powerLayoutPlacement(nil), base.Placements...), plTranslate(q, 45, 135), plTranslate(r, 15, 175))
	witness.Wires = append(append([]powerLayoutWire(nil), base.Wires...),
		powerLayoutWire{Net: "BOOT", Points: [][2]float64{{35, 90}, {60, 90}}},
		powerLayoutWire{Net: "BOOT", Points: [][2]float64{{60, 90}, {60, 135}}},
		powerLayoutWire{Net: "BOOT", Points: [][2]float64{{60, 135}, {55, 135}}},
		powerLayoutWire{Net: "BASE", Points: [][2]float64{{35, 135}, {30, 135}}},
		powerLayoutWire{Net: "BASE", Points: [][2]float64{{30, 135}, {30, 155}}},
		powerLayoutWire{Net: "BASE", Points: [][2]float64{{30, 155}, {35, 155}}},
		powerLayoutWire{Net: "BASE", Points: [][2]float64{{35, 155}, {35, 175}}},
		powerLayoutWire{Net: "BASE", Points: [][2]float64{{35, 175}, {30, 175}}},
	)
	if err := validateLibGeometry(&witness); err != nil {
		t.Fatalf("manually placed circuit is not legal: %v", err)
	}
	budget := 10000
	complete, err := libFinishSchematicLayoutRegenerate(witness, sameShellPolicies(), &budget, nil)
	if err != nil || complete == nil {
		t.Fatalf("manual circuit cannot pass complete routing/naming gate: %v", err)
	}
	if err := validateSchCompositionNets(complete); err != nil {
		t.Fatalf("manual circuit is not connected: %v", err)
	}
}

func TestSameShellBacktrackFindsDependentCorridor(t *testing.T) {
	base, q, r, qPair := sameShellCorridorFixture()
	budget := 1000
	cursor := 70.0
	rejected := map[[2]float64]bool{}
	placeQ := func() *powerLayoutPlan {
		candidate, err := libPlacePeripheralPairsWithRouting(base, q, []libAttachmentPair{qPair}, sameShellPolicies(), &budget, rejected, nil, &cursor)
		if err != nil || candidate == nil {
			t.Fatalf("Q placement failed: %v", err)
		}
		placed := candidate.Placements[len(candidate.Placements)-1]
		if rejected[[2]float64{placed.X, placed.Y}] {
			t.Fatalf("replayed rejected Q at (%g,%g)", placed.X, placed.Y)
		}
		rejected[[2]float64{placed.X, placed.Y}] = true
		return candidate
	}
	placeR := func(candidate *powerLayoutPlan) (*powerLayoutPlan, error) {
		placed := candidate.Placements[len(candidate.Placements)-1]
		pair := libAttachmentPair{host: placed.Pins[1], own: r.Pins[0], side: "left"}
		quota := 80
		if quota > budget {
			quota = budget
		}
		before := quota
		out, err := libPlacePeripheralPairsWithRouting(*candidate, r, []libAttachmentPair{pair}, sameShellPolicies(), &quota, nil, nil, nil)
		budget -= before - quota
		return out, err
	}
	first := placeQ()
	if placed := first.Placements[len(first.Placements)-1]; placed.X != 50 || placed.Y != 130 {
		t.Fatalf("fixture no longer selects the locally attractive Q: (%g,%g)", placed.X, placed.Y)
	}
	if child, err := placeR(first); child != nil || err == nil {
		t.Fatalf("first Q unexpectedly leaves a bounded R corridor: %v", err)
	}
	second := placeQ()
	if placed := second.Placements[len(second.Placements)-1]; placed.X != 45 || placed.Y != 135 {
		t.Fatalf("skipped a distinct legal Q in the same shell: (%g,%g)", placed.X, placed.Y)
	}
	child, err := placeR(second)
	if err != nil || child == nil {
		t.Fatalf("same-shell Q did not leave a bounded R corridor: %v", err)
	}
	complete, err := libFinishSchematicLayoutRegenerate(*child, sameShellPolicies(), &budget, nil)
	if err != nil || complete == nil {
		t.Fatalf("same-shell branch did not produce a complete circuit: %v", err)
	}
	if err := validateSchCompositionNets(complete); err != nil {
		t.Fatalf("same-shell branch lost connectivity: %v", err)
	}
	if budget < 0 || budget >= 1000 {
		t.Fatalf("invalid shared candidate accounting: remaining=%d", budget)
	}
}

func TestSameShellBudgetStopDoesNotPublishPartialLayout(t *testing.T) {
	base, q, _, pair := sameShellCorridorFixture()
	budget := 1
	cursor := 70.0
	out, err := libPlacePeripheralPairsWithRouting(base, q, []libAttachmentPair{pair}, sameShellPolicies(), &budget, nil, nil, &cursor)
	if out != nil || !errors.Is(err, errLibLayoutBudget) || budget != 0 {
		t.Fatalf("bounded placement published a partial result: out=%v budget=%d err=%v", out, budget, err)
	}
	if len(base.Placements) != 3 || len(base.Wires) != 3 {
		t.Fatal("bounded placement mutated its input checkpoint")
	}
}
