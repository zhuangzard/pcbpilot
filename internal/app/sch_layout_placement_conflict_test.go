package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func dependencyBackjumpFixture() SchematicLayoutInput {
	core := SchematicPlacement{Designator: "U1", BBox: SchematicBox{-20, -300, 20, 300}, TextBBoxes: []SchematicBox{{-10, 305, 10, 310}}, Pins: []SchematicPin{{Number: "1", Net: "LINK", X: 30, Y: 0}}}
	host := SchematicPlacement{Designator: "U2", BBox: SchematicBox{-10, -20, 10, 20}, TextBBoxes: []SchematicBox{{-5, -30, 5, -25}}, Pins: []SchematicPin{{Number: "1", Net: "LINK", X: -20, Y: 0}, {Number: "2", Net: "RAIL", X: -20, Y: 10}}}
	child := SchematicPlacement{Designator: "C1", BBox: SchematicBox{-1, -1, 1, 1}, TextBBoxes: []SchematicBox{{-1, -5, 1, -3}}, Pins: []SchematicPin{{Number: "1", Net: "RAIL", X: 5, Y: 0}}}
	in := SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: "root-id", Components: []SchematicLayoutComponent{{ID: "root-id", Measurement: core}, {ID: "host-id", Measurement: host}, {ID: "child-id", Measurement: child}}, NetPolicies: map[string]string{"LINK": "direct", "RAIL": "local_power"}, MaxCandidates: 64000,
		Attachments: []SchematicLayoutPeripheral{{ComponentID: "host-id", PinNumber: "1", AttachTo: &SchematicLayoutAttach{ComponentID: "root-id", PinNumber: "1"}}, {ComponentID: "child-id", PinNumber: "1", AttachTo: &SchematicLayoutAttach{ComponentID: "host-id", PinNumber: "2"}}}}
	for i, ref := range []string{"R1", "R2", "R3"} {
		id := []string{"unrelated-a", "unrelated-b", "unrelated-c"}[i]
		m := SchematicPlacement{Designator: ref, BBox: SchematicBox{-1, -1, 1, 1}, TextBBoxes: []SchematicBox{{-1, -5, 1, -3}}, Pins: []SchematicPin{{Number: "1", Net: "LINK", X: -5, Y: 0}}}
		in.Components = append(in.Components, SchematicLayoutComponent{ID: id, Measurement: m})
		in.Attachments = append(in.Attachments, SchematicLayoutPeripheral{ComponentID: id, PinNumber: "1", AttachTo: &SchematicLayoutAttach{ComponentID: "root-id", PinNumber: "1"}})
	}
	return in
}

// A wider attached body cannot fit in the first host-to-core gap. The host
// must move before the child can be placed; naming repair alone cannot turn
// this geometry conflict into a valid placement.
func dependencyBackjumpPlacementFixture() SchematicLayoutInput {
	in := dependencyBackjumpFixture()
	in.Components[2].Measurement.BBox = SchematicBox{-15, -15, 15, 15}
	in.Components[2].Measurement.Pins[0].X = 20
	return in
}

func TestClosedHostPlacementConflictStopsUnrelatedSuffix(t *testing.T) {
	in := dependencyBackjumpPlacementFixture()
	measured := map[string]powerLayoutPlacement{}
	members := []string{}
	hints := map[string]SchematicLayoutPeripheral{}
	for _, c := range in.Components {
		measured[c.ID] = c.Measurement
		members = append(members, c.ID)
	}
	for _, h := range in.Attachments {
		hints[h.ComponentID] = h
	}
	core := measured[in.CoreComponentID]
	base := powerLayoutPlan{Placements: []powerLayoutPlacement{core}}
	pairs, e := libAttachmentPairs("host-id", measured["host-id"], hints["host-id"], map[string]powerLayoutPlacement{in.CoreComponentID: core}, members, in.NetPolicies)
	if e != nil {
		t.Fatal(e)
	}
	budget := 64000
	first, e := libPlacePeripheralPairs(base, measured["host-id"], pairs, in.NetPolicies, &budget, nil)
	if e != nil {
		t.Fatal(e)
	}
	s := newSchematicRepairSearch(in, measured, members, hints, &budget)
	_, e = s.search(*first, []string{"child-id", "unrelated-a", "unrelated-b", "unrelated-c"})
	var conflict *SchematicPlacementConflict
	if !errors.As(e, &conflict) {
		t.Fatalf("no semantic conflict: %v", e)
	}
	if conflict.FutureHostsPossible || !conflict.OwnersComplete || conflict.CandidateCount == 0 || conflict.SearchExhaustion != "coordinate-window" {
		t.Fatalf("incomplete/incorrect conflict: %+v", conflict)
	}
	if len(s.firstXY) != 0 || s.diagnostics.PlacementFailures != 1 {
		t.Fatal("searched unrelated suffix before returning the actual failing child", s.firstXY, s.diagnostics)
	}
	if !reflect.DeepEqual(conflict.AttachmentHosts, []string{"host-id"}) {
		t.Fatal(conflict.AttachmentHosts)
	}
	if s.placementParticipates("unrelated-a", conflict) {
		t.Fatal("unrelated checkpoint was treated as a blocker")
	}
	raw, e := json.Marshal(conflict.FailureDetails())
	if e != nil || len(raw) == 0 {
		t.Fatal("machine-readable conflict missing", e)
	}
	t.Logf("bounded conflict candidates=%d remaining=%d owners=%v reasons=%v", conflict.CandidateCount, budget, conflict.BlockerOwners, conflict.ReasonCounts)
}

func TestDependencyBackjumpMovesHostAndSolvesWithinSharedBudget(t *testing.T) {
	in := dependencyBackjumpPlacementFixture()
	in.Components = in.Components[:3]
	in.Attachments = in.Attachments[:2]
	before, _ := json.Marshal(in)
	out, e := PlanSchematicLayout(in)
	if e != nil {
		t.Fatal(e)
	}
	if out.Search == nil || out.Search.PlacementFailures == 0 || len(out.Search.BackjumpTargets) == 0 || out.Search.BackjumpTargets[0] != "host-id" {
		t.Fatalf("did not retry actual attachment host: %+v", out.Search)
	}
	if out.CandidatesUsed <= 0 || out.CandidatesUsed >= in.MaxCandidates {
		t.Fatal("invalid/reset shared budget", out.CandidatesUsed)
	}
	after, _ := json.Marshal(in)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("search mutated measured input")
	}
	again, e := PlanSchematicLayout(in)
	if e != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("nondeterministic backjump", e)
	}
	t.Logf("solved candidates=%d backtracks=%d skipped=%d targets=%v", out.CandidatesUsed, out.Search.Backtracks, out.Search.SkippedCheckpoints, out.Search.BackjumpTargets)
}

func TestFutureAutomaticHostAndUnknownOwnershipPreventPruning(t *testing.T) {
	in := dependencyBackjumpFixture()
	measured := map[string]powerLayoutPlacement{}
	for _, c := range in.Components {
		measured[c.ID] = c.Measurement
	}
	s := &schematicRepairSearch{input: in, measured: measured, hints: map[string]SchematicLayoutPeripheral{"child-id": {ComponentID: "child-id", PinNumber: "1"}}}
	if !s.hasFutureAttachmentHost("child-id", []string{"child-id", "host-id"}) {
		t.Fatal("pending same-net host falsely pruned")
	}
	if s.hasFutureAttachmentHost("child-id", []string{"child-id", "unrelated-a"}) {
		t.Fatal("unrelated net treated as future host")
	}
	s.hints["child-id"] = SchematicLayoutPeripheral{ComponentID: "child-id", PinNumber: "1", AttachTo: &SchematicLayoutAttach{ComponentID: "root-id", PinNumber: "1"}}
	if s.hasFutureAttachmentHost("child-id", []string{"child-id", "host-id"}) {
		t.Fatal("fixed host constraint ignored")
	}
	conflict := &SchematicPlacementConflict{OwnersComplete: false, AttachmentHosts: []string{"host-id"}}
	if !s.placementParticipates("unrelated-a", conflict) {
		t.Fatal("unknown owner unsafely pruned")
	}
	conflict.OwnersComplete = true
	conflict.FutureHostsPossible = true
	if !s.placementParticipates("unrelated-a", conflict) {
		t.Fatal("future-host ordering dependency unsafely pruned")
	}
}

func TestPlacementBudgetExhaustionRetainsSemanticFailureAndCause(t *testing.T) {
	in := dependencyBackjumpFixture()
	in.MaxCandidates = 10
	_, e := PlanSchematicLayout(in)
	if !errors.Is(e, errLibLayoutBudget) {
		t.Fatal("budget cause lost", e)
	}
	var conflict *SchematicPlacementConflict
	if !errors.As(e, &conflict) || conflict.CandidateCount <= 0 || conflict.SearchExhaustion != "candidate-budget" || conflict.OwnersComplete {
		t.Fatalf("budget failure misreported %+v %v", conflict, e)
	}
}

func futureHostSearchFixture(t *testing.T, shared bool) (*schematicRepairSearch, *powerLayoutPlan) {
	t.Helper()
	in := dependencyBackjumpFixture()
	in.Components = in.Components[:3]
	// Keep the synthetic pin outside the core's measured Designator bbox.  Text
	// bounds are closed obstacles, so the old y=310 corner contact was itself an
	// invalid source fixture and masked the dependency-order behavior under test.
	in.Components[0].Measurement.Pins = append(in.Components[0].Measurement.Pins, SchematicPin{Number: "2", Net: "AUX", X: 0, Y: 320})
	in.Components[2].Measurement.BBox = SchematicBox{-6, -1, 6, 1}
	in.Components[2].Measurement.Pins[0].X = 10
	in.NetPolicies["AUX"] = "local_power"
	future := SchematicPlacement{Designator: "U3", BBox: SchematicBox{-10, -20, 10, 20}, TextBBoxes: []SchematicBox{{-5, 25, 5, 30}}, Pins: []SchematicPin{{Number: "1", Net: "AUX", X: 0, Y: -30}, {Number: "2", Net: "RAIL", X: -20, Y: 0}}}
	if !shared {
		future.Pins[1].Net = ""
	}
	in.Components = append(in.Components, SchematicLayoutComponent{ID: "future-id", Measurement: future})
	measured, hints := map[string]powerLayoutPlacement{}, map[string]SchematicLayoutPeripheral{}
	members := []string{}
	for _, c := range in.Components {
		measured[c.ID] = c.Measurement
		members = append(members, c.ID)
	}
	hints["host-id"] = in.Attachments[0]
	hints["child-id"] = SchematicLayoutPeripheral{ComponentID: "child-id", PinNumber: "1"}
	hints["future-id"] = SchematicLayoutPeripheral{ComponentID: "future-id", PinNumber: "1", AttachTo: &SchematicLayoutAttach{ComponentID: "root-id", PinNumber: "2"}}
	budget := in.MaxCandidates
	core := measured["root-id"]
	pairs, err := libAttachmentPairs("host-id", measured["host-id"], hints["host-id"], map[string]powerLayoutPlacement{"root-id": core}, members, in.NetPolicies)
	if err != nil {
		t.Fatal(err)
	}
	base, err := libPlacePeripheralPairs(powerLayoutPlan{Placements: []powerLayoutPlacement{core}}, measured["host-id"], pairs, in.NetPolicies, &budget, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Keep the host checkpoint branchable and nameable. The test exercises a
	// future automatic RAIL host, not the independently invalid one-grid LINK
	// short now rejected by the terminal frontier gate.
	base.Placements[1] = plTranslate(base.Placements[1], 5, 0)
	base.Wires = libPointsRoute("LINK", [2]float64{core.Pins[0].X, core.Pins[0].Y}, [2]float64{base.Placements[1].Pins[0].X, base.Placements[1].Pins[0].Y})
	s := newSchematicRepairSearch(in, measured, members, hints, &budget)
	return s, base
}

func TestFutureAutomaticHostIsPlacedBeforeDependentLeaf(t *testing.T) {
	s, base := futureHostSearchFixture(t, true)
	out, err := s.search(*base, []string{"child-id", "future-id"})
	if err != nil {
		t.Fatalf("%v; diagnostics=%+v; remaining=%d", err, s.diagnostics, *s.budget)
	}
	if len(out.Placements) != 4 {
		t.Fatal("future automatic host was not resolved", s.diagnostics, out.Placements)
	}
	if out.Placements[2].Designator != "U3" || out.Placements[3].Designator != "C1" {
		t.Fatal("unexpected dependency order", out.Placements)
	}
	if err := validateLibGeometry(out); err != nil {
		t.Fatal(err)
	}
}

func TestPlacementConflictSkipsUnrelatedCheckpoint(t *testing.T) {
	s, base := futureHostSearchFixture(t, false)
	out, err := s.search(*base, []string{"future-id", "child-id"})
	var conflict *SchematicPlacementConflict
	if out != nil || !errors.As(err, &conflict) {
		t.Fatal("expected bounded child conflict", out, err)
	}
	if !conflict.OwnersComplete || conflict.FutureHostsPossible || s.diagnostics.SkippedCheckpoints != 1 || s.diagnostics.Backtracks != 0 || s.diagnostics.PlacementFailures != 1 {
		t.Fatal("unrelated checkpoint consumed retries or lost proof", conflict, s.diagnostics)
	}
	if _, placed := s.firstXY["future-id"]; !placed {
		t.Fatal("test did not exercise a real unrelated checkpoint")
	}
}

func TestPlacementConflictIdentityAndRigidTransformContract(t *testing.T) {
	for quarter := 0; quarter < 4; quarter++ {
		t.Run(fmt.Sprint(quarter), func(t *testing.T) {
			in := dependencyBackjumpPlacementFixture()
			in.Components = in.Components[:3]
			in.Attachments = in.Attachments[:2]
			rename := map[string]string{"root-id": "opaque:9", "host-id": "opaque:2", "child-id": "opaque:7"}
			in.CoreComponentID = rename[in.CoreComponentID]
			in.NetPolicies = map[string]string{"renamed-link": "direct", "renamed-rail": "local_power"}
			for i := range in.Components {
				c := &in.Components[i]
				c.ID = rename[c.ID]
				c.Measurement.Designator = fmt.Sprintf("X%d", 30-i)
				for j := range c.Measurement.Pins {
					q := &c.Measurement.Pins[j]
					if q.Net == "LINK" {
						q.Net = "renamed-link"
					} else {
						q.Net = "renamed-rail"
					}
				}
				c.Measurement = plTranslate(plRotate(c.Measurement, quarter), 135, -275)
			}
			for i := range in.Attachments {
				in.Attachments[i].ComponentID = rename[in.Attachments[i].ComponentID]
				in.Attachments[i].AttachTo.ComponentID = rename[in.Attachments[i].AttachTo.ComponentID]
			}
			before, _ := json.Marshal(in)
			out, err := PlanSchematicLayout(in)
			if err != nil {
				t.Fatal(err)
			}
			if out.Search == nil {
				t.Fatal("missing bounded-search accounting")
			}
			if out.Search.PlacementFailures == 0 || len(out.Search.BackjumpTargets) == 0 || out.Search.BackjumpTargets[0] != "opaque:2" {
				t.Fatal("ownership depends on names or axes", out.Search)
			}
			for _, id := range out.Search.RelocationTargets {
				if id != "opaque:2" && id != "opaque:7" {
					t.Fatal("relocation attributed to unrelated component", out.Search)
				}
			}
			if len(out.Placements) != len(in.Components) {
				t.Fatal("incomplete transformed layout", out.Placements)
			}
			for _, placement := range out.Placements {
				id := out.ComponentIDs[placement.Designator]
				var source *SchematicLayoutComponent
				for i := range in.Components {
					if in.Components[i].ID == id {
						source = &in.Components[i]
						break
					}
				}
				if source == nil || placement.Rotation != source.Measurement.Rotation || placement.Mirror != source.Measurement.Mirror || len(placement.Pins) != len(source.Measurement.Pins) {
					t.Fatal("transformed component identity or pose changed", id, placement)
				}
				for j, pin := range placement.Pins {
					original := source.Measurement.Pins[j]
					if pin.Number != original.Number || pin.Net != original.Net || pin.X-placement.X != original.X-source.Measurement.X || pin.Y-placement.Y != original.Y-source.Measurement.Y {
						t.Fatal("transformed pin geometry or net changed", id, pin)
					}
				}
			}
			plan := powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags}
			if err := validateLibGeometry(&plan); err != nil {
				t.Fatal("transformed geometry invalid", err)
			}
			if err := validateSchCompositionNets(&plan); err != nil {
				t.Fatal("transformed connectivity invalid", err)
			}
			after, _ := json.Marshal(in)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("mutated source")
			}
		})
	}
}
