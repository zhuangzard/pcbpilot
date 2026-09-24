package app

import (
	"errors"
	"reflect"
	"testing"
)

func TestMazeContinuesAfterValidRouteRejectedByTerminalConstraint(t *testing.T) {
	source := mazeTestPart("A", "N", layoutBBox{-20, -10, 0, 10}, powerLayoutPin{Number: "1", Net: "N", X: 5, Y: 0, Rotation: mazeTestRotation(0)})
	target := mazeTestPart("B", "N", layoutBBox{200, -10, 220, 10}, powerLayoutPin{Number: "1", Net: "N", X: 195, Y: 0, Rotation: mazeTestRotation(180)})
	blocker := powerLayoutPlacement{Designator: "X", BBox: layoutBBox{70, -100, 130, 100}}
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{source, target, blocker}}
	ctx, err := newSchematicRoutingContext(&SchematicRoutingOptions{MaxExpandedNodes: 200000, MaxReroutes: 1}, []SchematicLayoutComponent{{ID: "a", Measurement: source}, {ID: "b", Measurement: target}, {ID: "x", Measurement: blocker}})
	if err != nil {
		t.Fatal(err)
	}
	islands := libIslands(&p)
	calls := 0
	var first []powerLayoutWire
	route, err := libMazeRouteAccepted(&p, islands[0], islands[1], ctx, func(trial *powerLayoutPlan) bool {
		calls++
		if calls == 1 {
			first = clonePowerLayoutWires(trial.Wires)
			return false
		}
		return !reflect.DeepEqual(first, trial.Wires)
	})
	if err != nil || calls < 2 {
		t.Fatalf("alternate valid path was not explored: calls=%d err=%v", calls, err)
	}
	p.Wires = libAppendRoute(p.Wires, route)
	if err := validateLibGeometry(&p); err != nil || !libPinsShareIsland(&p, source.Pins[0], target.Pins[0]) {
		t.Fatalf("accepted alternative is invalid: %v", err)
	}
}

func TestMazeRejectedTerminalRouteIsInconclusiveAndNotCached(t *testing.T) {
	source := mazeTestPart("A", "N", layoutBBox{-20, -10, 0, 10}, powerLayoutPin{Number: "1", Net: "N", X: 5, Y: 0, Rotation: mazeTestRotation(0)})
	target := mazeTestPart("B", "N", layoutBBox{50, -10, 70, 10}, powerLayoutPin{Number: "1", Net: "N", X: 45, Y: 0, Rotation: mazeTestRotation(180)})
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{source, target}}
	ctx, err := newSchematicRoutingContext(&SchematicRoutingOptions{MaxExpandedNodes: 200000, MaxReroutes: 1}, []SchematicLayoutComponent{{ID: "a", Measurement: source}, {ID: "b", Measurement: target}})
	if err != nil {
		t.Fatal(err)
	}
	islands := libIslands(&p)
	rejected := 0
	_, err = libMazeRouteAccepted(&p, islands[0], islands[1], ctx, func(*powerLayoutPlan) bool {
		rejected++
		ctx.addRejection(SchematicRoutingRejection{ComponentID: "a", ComponentRef: "A", Net: "N", Reason: "fixture-geometric-edge", AttributionComplete: true})
		return false
	})
	var failure *schematicRoutingFailure
	if !errors.As(err, &failure) || rejected == 0 || failure.Kind != "route-attempt-node-limit" {
		t.Fatalf("callback rejection was reported as a geometric no-path: rejected=%d err=%v", rejected, err)
	}
	if len(failure.BlockingEvidence) == 0 || !failure.BlockingEvidence[0].AttributionComplete || failure.OwnersComplete {
		t.Fatalf("callback rejection inherited complete geometric ownership: evidence=%+v ownersComplete=%v", failure.BlockingEvidence, failure.OwnersComplete)
	}
	conflict := &schematicRouteConflict{ownersComplete: failure.OwnersComplete, blockers: map[string]bool{"A": true}}
	search := &schematicRepairSearch{focused: true, measured: map[string]powerLayoutPlacement{"b": target}}
	if !search.participates("b", conflict) {
		t.Fatal("unknown callback blocker caused strong checkpoint pruning")
	}
	if len(ctx.cache) != 0 {
		t.Fatalf("inconclusive callback rejection polluted the geometric route cache: %+v", ctx.cache)
	}
	route, err := libMazeRoute(&p, islands[0], islands[1], ctx)
	if err != nil || len(route) == 0 {
		t.Fatalf("same geometry remained blocked after callback was removed: route=%v err=%v", route, err)
	}
}
