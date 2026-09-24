package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Deliberately asymmetric synthetic geometry exercises source preservation,
// measured poses, terminal naming, and the shared candidate allowance.
func schematicRepairFixture() SchematicLayoutInput {
	return SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: "core", MaxCandidates: 20000,
		NetPolicies: map[string]string{"G": "local_ground", "N0": "module_port", "N1": "module_port", "N2": "module_port"},
		Components: []SchematicLayoutComponent{
			{ID: "core", Measurement: SchematicPlacement{Designator: "U1", BBox: SchematicBox{-20, -40, 20, 40}, Pins: []SchematicPin{
				{Number: "4", Net: "G", X: 0, Y: -50}, {Number: "1", Net: "N0", X: 30, Y: 20}, {Number: "2", Net: "N1", X: 30, Y: 0}, {Number: "3", Net: "N2", X: 30, Y: -20}, {Number: "5", X: -30, Y: 20},
			}}, PinStates: map[string]string{"5": "nc"}},
			{ID: "R1", Measurement: SchematicPlacement{Designator: "R1", BBox: SchematicBox{-5, -20, 5, 20}, Pins: []SchematicPin{{Number: "1", Net: "N0", X: 0, Y: -30}, {Number: "2", Net: "G", X: 0, Y: 30}}}},
			{ID: "R2", Measurement: SchematicPlacement{Designator: "R2", Rotation: 180, Mirror: true, BBox: SchematicBox{-15, -15, 15, 15}, Pins: []SchematicPin{{Number: "1", Net: "N1", X: -25, Y: 0}, {Number: "2", Net: "G", X: 25, Y: 0}}}},
			{ID: "R3", Measurement: SchematicPlacement{Designator: "R3", BBox: SchematicBox{-10, -15, 10, 15}, Pins: []SchematicPin{{Number: "1", Net: "N2", X: 0, Y: 25}, {Number: "2", Net: "G", X: 0, Y: -25}}}},
		}}
}

func TestSchematicRepairPrioritizesOwnedChildBeforeUnrelatedCoreBranch(t *testing.T) {
	measured := map[string]powerLayoutPlacement{
		"core":    {Designator: "U1"},
		"host":    {Designator: "Q1", Pins: []powerLayoutPin{{Net: "BASE"}, {Net: "OUT"}, {Net: "GND"}}},
		"child":   {Designator: "R1", Pins: []powerLayoutPin{{Net: "BASE"}, {Net: "GND"}}},
		"sibling": {Designator: "J1", Pins: []powerLayoutPin{{Net: "A"}, {Net: "B"}}},
		"rail":    {Designator: "C1", Pins: []powerLayoutPin{{Net: "GND"}}},
	}
	hints := map[string]SchematicLayoutPeripheral{
		"host":    {ComponentID: "host", AttachTo: &SchematicLayoutAttach{ComponentID: "core", PinNumber: "1"}},
		"child":   {ComponentID: "child", AttachTo: &SchematicLayoutAttach{ComponentID: "host", PinNumber: "1"}},
		"sibling": {ComponentID: "sibling", AttachTo: &SchematicLayoutAttach{ComponentID: "core", PinNumber: "2"}},
		"rail":    {ComponentID: "rail", AttachTo: &SchematicLayoutAttach{ComponentID: "core", PinNumber: "3"}},
	}
	s := &schematicRepairSearch{input: SchematicLayoutInput{NetPolicies: map[string]string{
		"BASE": "direct", "OUT": "module_port", "GND": "local_ground",
		"A": "module_port", "B": "module_port", "C": "module_port", "D": "module_port",
	}}, measured: measured, hints: hints, depth: schematicAttachmentDepths("core", []string{"core", "host", "child", "sibling", "rail"}, hints)}
	got := s.orderedPending(powerLayoutPlan{Placements: []powerLayoutPlacement{measured["core"], measured["host"]}}, []string{"sibling", "child", "rail"})
	if !reflect.DeepEqual(got, []string{"rail", "child", "sibling"}) {
		t.Fatalf("owned child lost its host corridor or rail priority: %v", got)
	}
	measured["other-host"] = powerLayoutPlacement{Designator: "Q2", Pins: measured["host"].Pins}
	measured["other-child"] = powerLayoutPlacement{Designator: "R2", Pins: measured["child"].Pins}
	hints["other-host"] = SchematicLayoutPeripheral{ComponentID: "other-host", AttachTo: &SchematicLayoutAttach{ComponentID: "core", PinNumber: "4"}}
	hints["other-child"] = SchematicLayoutPeripheral{ComponentID: "other-child", AttachTo: &SchematicLayoutAttach{ComponentID: "other-host", PinNumber: "1"}}
	s.depth = schematicAttachmentDepths("core", []string{"core", "host", "child", "other-host", "other-child"}, hints)
	got = s.orderedPending(powerLayoutPlan{Placements: []powerLayoutPlacement{measured["core"], measured["host"], measured["other-host"]}}, []string{"child", "other-child"})
	if !reflect.DeepEqual(got, []string{"other-child", "child"}) {
		t.Fatalf("newest host's equal-depth child should be tried first: %v", got)
	}
}

func TestSchematicRepairPreservesMeasuredSourceAndGeometry(t *testing.T) {
	in := schematicRepairFixture()
	before, _ := json.Marshal(in)
	measured := map[string]powerLayoutPlacement{}
	for _, c := range in.Components {
		measured[c.ID] = c.Measurement
	}
	out, err := PlanSchematicLayout(in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Search == nil {
		t.Fatal("search accounting is missing")
	}
	for i, c := range out.Placements {
		m := measured[out.ComponentIDs[c.Designator]]
		if c.Rotation != m.Rotation || c.Mirror != m.Mirror || len(c.Pins) != len(m.Pins) {
			t.Fatal("changed measured pose/pin membership")
		}
		for j, q := range c.Pins {
			if q.Number != m.Pins[j].Number || q.Net != m.Pins[j].Net || q.X-c.X != m.Pins[j].X-m.X || q.Y-c.Y != m.Pins[j].Y-m.Y {
				t.Fatal("changed pin/net/relative geometry")
			}
		}
		if i == 0 && (c.Designator != "U1" || c.X != 0 || c.Y != 0) {
			t.Fatal("moved core")
		}
	}
	if out.PinStates["core"]["5"] != "nc" {
		t.Fatal("lost NC")
	}
	p := powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags}
	if err = validateLibGeometry(&p); err != nil {
		t.Fatal(err)
	}
	if err = validateSchCompositionNets(&p); err != nil {
		t.Fatal("repaired suffix has invalid or stale routes", err)
	}
	again, err := PlanSchematicLayout(in)
	if err != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("search is not deterministic", err)
	}
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("mutated source evidence")
	}
	if out.CandidatesUsed > in.MaxCandidates || out.CandidatesUsed <= 0 {
		t.Fatal("invalid shared budget accounting")
	}
}

func TestRepairBranchLimitUsesSharedCandidateAllowance(t *testing.T) {
	in := schematicRepairFixture()
	measured := map[string]powerLayoutPlacement{}
	members := make([]string, 0, len(in.Components))
	for _, component := range in.Components {
		measured[component.ID] = component.Measurement
		members = append(members, component.ID)
	}
	budget := 4096
	search := newSchematicRepairSearch(in, measured, members, nil, &budget)
	if search.diagnostics.BranchLimit != budget {
		t.Fatalf("branch cap discarded shared search capacity: got %d want %d", search.diagnostics.BranchLimit, budget)
	}
	budget = 7
	search = newSchematicRepairSearch(in, measured, members, nil, &budget)
	if search.diagnostics.BranchLimit != 128 {
		t.Fatalf("tiny-budget secondary guard changed: got %d want 128", search.diagnostics.BranchLimit)
	}
}

func TestCandidateReserveRequiresMovableAttributedTerminalBlocker(t *testing.T) {
	core := powerLayoutPlacement{Designator: "U1", BBox: layoutBBox{-10, -10, 10, 10}}
	blocker := powerLayoutPlacement{Designator: "C1", X: 50, BBox: layoutBBox{45, -5, 55, 5}}
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{core, blocker}}
	newSearch := func(cause error) *schematicRepairSearch {
		budget := 10
		s := newSchematicRepairSearch(SchematicLayoutInput{CoreComponentID: "core"}, map[string]powerLayoutPlacement{
			"core": core, "blocker": blocker,
		}, []string{"core", "blocker"}, nil, &budget)
		s.initial = 1000 // reserve=512, well above the remaining 10 candidates.
		s.lastTerminal, s.lastTerminalErr = p, cause
		return s
	}
	for name, cause := range map[string]error{
		"naming":  errors.New("no safe naming lead"),
		"unknown": &schematicRouteConflict{blockers: map[string]bool{"X1": true}, ownersComplete: true},
		"core":    &schematicRouteConflict{blockers: map[string]bool{"U1": true}, ownersComplete: true},
		"opaque":  &schematicRouteConflict{blockers: map[string]bool{"C1": true}, ownersComplete: false},
	} {
		s := newSearch(cause)
		if s.canTargetedTerminalRelocation() {
			t.Fatalf("%s reserved for an unusable target", name)
		}
		if _, err := s.search(p, nil); errors.Is(err, errSchematicCandidateReserve) {
			t.Fatalf("%s stopped with an unusable reserve: %v", name, err)
		}
	}
	s := newSearch(&schematicRouteConflict{blockers: map[string]bool{"C1": true}, ownersComplete: true})
	if !s.canTargetedTerminalRelocation() {
		t.Fatal("movable measured blocker was not eligible")
	}
	if _, err := s.search(p, nil); !errors.Is(err, errSchematicCandidateReserve) {
		t.Fatalf("eligible blocker lost its candidate reserve: %v", err)
	}
}

func TestNamingIslandTargetsIncludeOnlyExplicitSameNetAttachments(t *testing.T) {
	core := powerLayoutPlacement{Designator: "U1", Pins: []powerLayoutPin{{Number: "4", Net: "SUPPLY"}, {Number: "5", Net: "FB"}}}
	child := powerLayoutPlacement{Designator: "C1", Pins: []powerLayoutPin{{Number: "1", Net: "SUPPLY"}}}
	other := powerLayoutPlacement{Designator: "R1", Pins: []powerLayoutPin{{Number: "1", Net: "FB"}}}
	s := schematicRepairSearch{input: SchematicLayoutInput{CoreComponentID: "core"}, measured: map[string]powerLayoutPlacement{
		"core": core, "child": child, "other": other,
	}, hints: map[string]SchematicLayoutPeripheral{
		"child": {ComponentID: "child", PinNumber: "1", AttachTo: &SchematicLayoutAttach{ComponentID: "core", PinNumber: "4"}},
		"other": {ComponentID: "other", PinNumber: "1", AttachTo: &SchematicLayoutAttach{ComponentID: "core", PinNumber: "5"}},
	}}
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{core, child, other}}
	conflict := &schematicNamingConflict{net: "SUPPLY", endpointOwners: map[string]bool{"U1": true}, ownersComplete: true}
	if got := s.namingIslandTargets(&p, conflict); !reflect.DeepEqual(got, []string{"C1"}) {
		t.Fatalf("isolated core island lost exact owned peripheral: %v", got)
	}
	conflict.ownersComplete = false
	if got := s.namingIslandTargets(&p, conflict); len(got) != 0 {
		t.Fatalf("incomplete owner evidence selected a target: %v", got)
	}
}

func TestNamingRelocationSearchesPastBlockedNearShells(t *testing.T) {
	core := powerLayoutPlacement{Designator: "U1", BBox: layoutBBox{-30.5, -40.5, 40.5, 30.5},
		TextBBoxes: []layoutBBox{{-30, 40, -11.7, 48}}, Pins: []powerLayoutPin{
			{Number: "1", Net: "A", X: -40, Y: -10, Rotation: directionNumber(180)},
			{Number: "2", Net: "B", X: -40, Y: 10, Rotation: directionNumber(180)},
		}}
	port := powerLayoutPlacement{Designator: "R3", X: -60, Y: -5, BBox: layoutBBox{-70.5, -9.5, -49.5, -0.5},
		TextBBoxes: []layoutBBox{{-70, 0, -61.05, 8}}, Pins: []powerLayoutPin{
			{Number: "1", Net: "N", X: -40, Y: -5, Rotation: directionNumber(0)},
			{Number: "2", X: -80, Y: -5, Rotation: directionNumber(180)},
		}}
	plan := powerLayoutPlan{Placements: []powerLayoutPlacement{core, port}}
	if err := validateLibGeometry(&plan); err != nil {
		t.Fatal(err)
	}
	for _, distance := range []float64{0, 5, 10, 15} {
		trial := plan
		trial.Placements = append([]powerLayoutPlacement(nil), plan.Placements...)
		trial.Placements[1] = plTranslate(port, -distance, 0)
		pin := trial.Placements[1].Pins[0]
		budget := 10000
		reachable, complete := libNamingFrontier(&trial, libIsland{net: "N", pins: []powerLayoutPin{pin}}, "module_port", &budget)
		if !complete || reachable != (distance == 15) {
			t.Fatalf("unexpected naming threshold at outward distance %g: reachable=%v complete=%v", distance, reachable, complete)
		}
	}
	input := SchematicLayoutInput{CoreComponentID: "core", NetPolicies: map[string]string{"N": "module_port", "A": "module_port", "B": "module_port"}}
	measured := map[string]powerLayoutPlacement{"core": core, "port": port}
	remaining := 20000
	s := newSchematicRepairSearch(input, measured, []string{"core", "port"}, nil, &remaining,
		&schematicRoutingContext{netPins: map[string]int{"N": 1, "A": 1, "B": 1}})
	conflict := &schematicNamingConflict{net: "N", pin: port.Pins[0], endpointOwners: map[string]bool{"R3": true}, ownersComplete: true}
	out, ok := s.tryNamingIslandRelocation(plan, conflict)
	if !ok || out == nil || out.Placements[1].X > -75 || s.diagnostics.TargetedRelocations < 3 || remaining <= 0 || remaining >= 20000 {
		t.Fatalf("bounded relocation missed the first viable outward shell: ok=%v remaining=%d diagnostics=%+v", ok, remaining, s.diagnostics)
	}
	if err := validateLibGeometry(out); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetStopRetainsConcreteTerminalConflict(t *testing.T) {
	budget := 0
	s := newSchematicRepairSearch(SchematicLayoutInput{}, nil, nil, nil, &budget)
	s.lastErr = errSchematicCandidateReserve // Ancestor recursion must not duplicate this sentinel.
	s.lastTerminalErr = &schematicTerminalFailure{cause: schObstruction("wire-text", errors.New("3V3 wire touches C6 Designator"), "C6")}
	_, err := s.solve(powerLayoutPlan{}, nil)
	if err == nil || !errors.Is(err, errLibLayoutBudget) || !strings.Contains(err.Error(), "3V3 wire touches C6 Designator") || strings.Contains(err.Error(), "reserved for terminal blocker relocation") {
		t.Fatalf("resource stop hid its concrete cause: %v", err)
	}
	raw, _ := json.Marshal(schLayoutFailureDiagnostics(err))
	if !bytes.Contains(raw, []byte(`"kind":"terminal-conflict"`)) || !bytes.Contains(raw, []byte(`"obstructionKind":"wire-text"`)) || !bytes.Contains(raw, []byte(`"blockerRefs":["C6"]`)) {
		t.Fatalf("terminal blocker absent from structured report: %s", raw)
	}
	if got := schLayoutFailureClass(err, "solve"); got != "candidate-budget-exhausted" {
		t.Fatalf("bounded failure misclassified as %s", got)
	}
}

// A source pin with a valid 5-raw exit before a wall has no outward naming
// lead: every candidate must first cross the wall. This keeps the two budget
// boundary tests on the real terminal naming path rather than a mocked error.
func namingBudgetBoundaryFixture() (powerLayoutPlan, SchematicLayoutInput, map[string]powerLayoutPlacement) {
	core := powerLayoutPlacement{Designator: "U1", BBox: layoutBBox{-10, -200, 10, 200}, TextBBoxes: []layoutBBox{{-10, 205, -5, 213}}}
	policies := map[string]string{}
	for i, y := range []float64{-175, -125, -75, -25, 25, 75, 125, 175} {
		net := fmt.Sprintf("N%d", i)
		core.Pins = append(core.Pins, powerLayoutPin{Number: fmt.Sprint(i + 1), Net: net, X: 20, Y: y, Rotation: directionNumber(0)})
		policies[net] = "module_port"
	}
	wall := powerLayoutPlacement{Designator: "W1", BBox: layoutBBox{30, -500, 200, 500}, TextBBoxes: []layoutBBox{{205, 505, 215, 513}}}
	return powerLayoutPlan{Placements: []powerLayoutPlacement{core, wall}}, SchematicLayoutInput{CoreComponentID: "core", NetPolicies: policies}, map[string]powerLayoutPlacement{"core": core, "wall": wall}
}

func TestSharedNamingBudgetExhaustionDoesNotInventTerminalConflict(t *testing.T) {
	p, input, measured := namingBudgetBoundaryFixture()
	if err := validateLibGeometry(&p); err != nil {
		t.Fatalf("invalid boundary fixture: %v", err)
	}
	budget := 1
	s := newSchematicRepairSearch(input, measured, []string{"core", "wall"}, nil, &budget)
	out, err := s.solve(p, nil)
	if out != nil || err == nil || budget != 0 || !errors.Is(err, errLibLayoutBudget) || s.lastTerminalErr != nil {
		t.Fatalf("shared boundary invented a conflict or result: out=%v budget=%d err=%v", out, budget, err)
	}
	var naming *schematicNamingConflict
	if errors.As(err, &naming) || schLayoutFailureClass(err, "solve") != "candidate-budget-exhausted" {
		t.Fatalf("unsearched naming candidates were called infeasible: %v", err)
	}
	raw, _ := json.Marshal(schLayoutFailureDiagnostics(err))
	if bytes.Contains(raw, []byte(`"terminal-conflict"`)) || bytes.Contains(raw, []byte(`"naming-conflict"`)) {
		t.Fatalf("resource-only report fabricated a terminal diagnosis: %s", raw)
	}
}

func TestResourceStopKeepsLastObservedTerminalConflict(t *testing.T) {
	p, input, measured := namingBudgetBoundaryFixture()
	// Establish a concrete completed naming failure first. The subsequent
	// one-candidate attempt stops before its own naming proof and must retain
	// the earlier observation as historical evidence, not a fresh proof.
	observed := wireTreeNamingFixture()
	observed.Wires = append(observed.Wires,
		powerLayoutWire{Net: "X", Points: [][2]float64{{100, 10}, {130, 10}}},
		powerLayoutWire{Net: "Y", Points: [][2]float64{{100, -10}, {130, -10}}},
	)
	concrete := libNameIslands(&observed, map[string]string{"N": "module_port"})
	var naming *schematicNamingConflict
	if !errors.As(concrete, &naming) || naming.net != "N" {
		t.Fatalf("fixture did not establish a concrete naming conflict: %v", concrete)
	}
	budget := 1
	s := newSchematicRepairSearch(input, measured, []string{"core", "wall"}, nil, &budget)
	lastObserved := &schematicTerminalFailure{cause: concrete, preRegenerationLayout: &observed}
	s.lastTerminalErr = lastObserved
	s.lastTerminal = p
	_, err := s.search(p, nil)
	if !errors.Is(err, errLibLayoutBudget) || budget != 0 || s.lastTerminalErr != lastObserved || s.diagnostics.TargetedRelocations != 0 {
		t.Fatalf("local slice lost last observed conflict: budget=%d err=%v last=%v", budget, err, s.lastTerminalErr)
	}
	_, err = s.solve(p, nil)
	if err == nil || !errors.Is(err, errLibLayoutBudget) || !errors.As(err, &naming) || schLayoutFailureClass(err, "solve") != "candidate-budget-exhausted" {
		t.Fatalf("resource stop and prior concrete cause were not separate facts: %v", err)
	}
	if !strings.Contains(err.Error(), "last observed terminal conflict") {
		t.Fatalf("prior conflict was mislabeled as the final attempt: %v", err)
	}
	raw, _ := json.Marshal(schLayoutFailureDiagnostics(err))
	if !bytes.Contains(raw, []byte(`"preRegenerationLayout"`)) || bytes.Contains(raw, []byte(`"localLayout"`)) || !bytes.Contains(raw, []byte(`"naming-conflict"`)) {
		t.Fatalf("terminal checkpoint mislabeled or omitted: %s", raw)
	}
}

// Distilled from the measured buck source: the old 3V3 rail crosses C6's
// official Designator. A fixed-placement detour exists, so this collision must
// stay a hard geometry rejection without being mistaken for no layout capacity.
func TestMeasuredBuckDesignatorBlocksOldRailButAllowsFixedPlacementDetour(t *testing.T) {
	left := directionNumber(180)
	c6 := powerLayoutPlacement{Designator: "C6", X: 15, Y: -85,
		BBox: layoutBBox{4.5, -93.5, 25.5, -76.5}, TextBBoxes: []layoutBBox{{5, -75, 13.9482421875, -67}},
		Pins: []powerLayoutPin{{Number: "1", Net: "3V3", X: -5, Y: -85, Rotation: left}, {Number: "2", Net: "GND", X: 35, Y: -85}}}
	c7 := powerLayoutPlacement{Designator: "C7", X: 75, Y: -70,
		BBox: layoutBBox{64.5, -78.5, 85.5, -61.5}, TextBBoxes: []layoutBBox{{65, -60, 73.9482421875, -52}},
		Pins: []powerLayoutPin{{Number: "1", Net: "3V3", X: 55, Y: -70, Rotation: left}, {Number: "2", Net: "GND", X: 95, Y: -70}}}
	old := powerLayoutPlan{Placements: []powerLayoutPlacement{c6, c7}, Wires: []powerLayoutWire{
		{Net: "3V3", Points: [][2]float64{{-5, -85}, {-5, -70}}},
		{Net: "3V3", Points: [][2]float64{{-5, -70}, {55, -70}}},
	}}
	var obstruction *schGeometryObstruction
	if err := validateLibGeometry(&old); !errors.As(err, &obstruction) || obstruction.kind != "wire-text" || !slices.Contains(obstruction.blockers, "C6") {
		t.Fatalf("old rail was not rejected by measured C6 text: %v", err)
	}
	detour := powerLayoutPlan{Placements: old.Placements, Wires: []powerLayoutWire{
		{Net: "3V3", Points: [][2]float64{{-5, -85}, {-10, -85}}},
		{Net: "3V3", Points: [][2]float64{{-10, -85}, {-10, -65}}},
		{Net: "3V3", Points: [][2]float64{{-10, -65}, {50, -65}}},
		{Net: "3V3", Points: [][2]float64{{50, -65}, {50, -70}}},
		{Net: "3V3", Points: [][2]float64{{50, -70}, {55, -70}}},
	}}
	if err := validateLibGeometry(&detour); err != nil || !libPinsShareIsland(&detour, c6.Pins[0], c7.Pins[0]) {
		t.Fatalf("fixed-placement text-safe rail failed geometry/connectivity: %v", err)
	}
}

// The current failed terminal candidate places C5 only 10 raw from U3.IN.
// Its VIN_OR tree has no room for a marker between the bodies and nearby pin
// exits. A larger attachment shell opens that corridor without changing the
// measured symbols or bypassing marker/Designator geometry checks.
func TestBuckNamingCorridorOpensAtLaterAttachmentShell(t *testing.T) {
	right, left := directionNumber(0), directionNumber(180)
	core := powerLayoutPlacement{Designator: "U3", BBox: layoutBBox{-25.5, -20.5, 25.5, 20.5},
		TextBBoxes: []layoutBBox{{-25, 20, -16.0517578125, 28}}, Pins: []powerLayoutPin{
			{Number: "4", Net: "VIN_OR", X: 35, Y: -10, Rotation: right},
			{Number: "5", Net: "FB", X: 35, Y: 10, Rotation: right},
		}}
	c6 := powerLayoutPlacement{Designator: "C6", X: 20, Y: -45, BBox: layoutBBox{9.5, -53.5, 30.5, -36.5},
		TextBBoxes: []layoutBBox{{10, -35, 18.9482421875, -27}}, Pins: []powerLayoutPin{{Number: "2", Net: "GND", X: 40, Y: -45, Rotation: right}}}
	c7 := powerLayoutPlacement{Designator: "C7", X: 25, Y: -80, BBox: layoutBBox{14.5, -88.5, 35.5, -71.5},
		TextBBoxes: []layoutBBox{{15, -70, 23.9482421875, -62}}, Pins: []powerLayoutPin{{Number: "2", Net: "GND", X: 45, Y: -80, Rotation: right}}}
	for _, c5X := range []float64{65, 75} {
		c5 := powerLayoutPlacement{Designator: "C5", X: c5X, Y: -10,
			BBox:       layoutBBox{c5X - 10.5, -18.5, c5X + 10.5, -1.5},
			TextBBoxes: []layoutBBox{{c5X - 10, 0, c5X - 1.0517578125, 8}},
			Pins:       []powerLayoutPin{{Number: "1", Net: "VIN_OR", X: c5X - 20, Y: -10, Rotation: left}, {Number: "2", Net: "GND", X: c5X + 20, Y: -10, Rotation: right}}}
		p := powerLayoutPlan{Placements: []powerLayoutPlacement{core, c5, c6, c7}, Wires: []powerLayoutWire{{Net: "VIN_OR", Points: [][2]float64{{35, -10}, {c5X - 20, -10}}}}}
		initial := p
		if err := validateLibGeometry(&p); err != nil {
			t.Fatalf("C5 x=%g fixture geometry invalid: %v", c5X, err)
		}
		var island *libIsland
		for _, candidate := range libIslands(&p) {
			if candidate.net == "VIN_OR" {
				island = &candidate
				break
			}
		}
		if island == nil {
			t.Fatal("missing VIN_OR island")
		}
		budget := 10000
		placed := libPlaceWireTreeMarker(&p, *island, "net_port_bi", &budget)
		if placed != (c5X == 75) {
			t.Fatalf("C5 x=%g marker placement=%v, expected later-shell clearance", c5X, placed)
		}
		if placed {
			if err := validateLibGeometry(&p); err != nil {
				t.Fatalf("later-shell marker violates geometry: %v", err)
			}
		}
		if c5X == 65 {
			measured := map[string]powerLayoutPlacement{"core": core, "child": c5, "other-a": c6, "other-b": c7}
			hints := map[string]SchematicLayoutPeripheral{"child": {ComponentID: "child", PinNumber: "1", AttachTo: &SchematicLayoutAttach{ComponentID: "core", PinNumber: "4"}}}
			input := SchematicLayoutInput{CoreComponentID: "core", NetPolicies: map[string]string{"VIN_OR": "module_port", "FB": "module_port", "GND": "local_ground"}}
			probe := func(h map[string]SchematicLayoutPeripheral, allowance int) (*powerLayoutPlan, *schematicRepairSearch) {
				budget := allowance
				s := newSchematicRepairSearch(input, measured, []string{"core", "child", "other-a", "other-b"}, h, &budget)
				out, ok := s.tryNamingIslandRelocation(initial, &schematicNamingConflict{net: "VIN_OR", endpointOwners: map[string]bool{"U3": true}, ownersComplete: true})
				if ok != (out != nil) || budget < 0 || budget > allowance {
					t.Fatalf("invalid naming relocation result/budget: ok=%v budget=%d", ok, budget)
				}
				return out, s
			}
			if out, s := probe(nil, 10000); out != nil || s.diagnostics.TargetedRelocations != 0 {
				t.Fatal("unowned same-net member became a naming relocation target")
			}
			first, a := probe(hints, 10000)
			second, b := probe(hints, 10000)
			if first == nil || !reflect.DeepEqual(first, second) || !reflect.DeepEqual(a.diagnostics.RelocationAttempts, b.diagnostics.RelocationAttempts) || len(a.diagnostics.RelocationAttempts) != 1 || a.diagnostics.RelocationAttempts[0].Result != "accepted" {
				t.Fatalf("attached naming relocation was not deterministic: first=%v second=%v attempts=%+v", first != nil, second != nil, a.diagnostics.RelocationAttempts)
			}
			if out, s := probe(hints, 1); out != nil || s.diagnostics.TargetedRelocations > 1 {
				t.Fatal("tiny shared budget accepted unverified naming relocation")
			}
			if err := validateLibGeometry(first); err != nil {
				t.Fatalf("accepted naming relocation has invalid geometry: %v", err)
			}
		}
	}
}

func TestRouteConflictIncludesWireOwnersAndKeepsUnknownOwnersSearchable(t *testing.T) {
	a := powerLayoutPlacement{Designator: "A", BBox: layoutBBox{-120, -20, -80, 20}, Pins: []powerLayoutPin{{Number: "1", Net: "N", X: -70, Y: 0, Rotation: directionNumber(0)}}}
	b := powerLayoutPlacement{Designator: "B", BBox: layoutBBox{80, -20, 120, 20}, Pins: []powerLayoutPin{{Number: "1", Net: "N", X: 70, Y: 0}}}
	owner := powerLayoutPlacement{Designator: "Q", BBox: layoutBBox{190, 190, 210, 210}, Pins: []powerLayoutPin{{Number: "1", Net: "BLOCK", X: 180, Y: 200}}}
	// Enclose A in a finite, physically contacted wire cage. Unlike the former
	// open wall, this cannot be escaped by routing around an endpoint. Teeth on
	// every 5-raw crossing turn a would-be X into an endpoint/T/overlap contact;
	// the proper-X rule itself remains unchanged.
	barriers := []powerLayoutWire{
		{Net: "BLOCK", Points: [][2]float64{{-140, -50}, {-40, -50}}},
		{Net: "BLOCK", Points: [][2]float64{{-40, -50}, {-40, 50}}},
		{Net: "BLOCK", Points: [][2]float64{{-40, 50}, {-140, 50}}},
		{Net: "BLOCK", Points: [][2]float64{{-140, 50}, {-140, -50}}},
	}
	for y := -50.0; y <= 50; y += 5 {
		barriers = append(barriers,
			powerLayoutWire{Net: "BLOCK", Points: [][2]float64{{-40, y}, {-35, y}}},
			powerLayoutWire{Net: "BLOCK", Points: [][2]float64{{-140, y}, {-145, y}}},
		)
	}
	for x := -140.0; x <= -40; x += 5 {
		barriers = append(barriers,
			powerLayoutWire{Net: "BLOCK", Points: [][2]float64{{x, 50}, {x, 55}}},
			powerLayoutWire{Net: "BLOCK", Points: [][2]float64{{x, -50}, {x, -55}}},
		)
	}
	for _, known := range []bool{true, false} {
		p := powerLayoutPlan{Placements: []powerLayoutPlacement{a, b}, Wires: barriers}
		if known {
			p.Placements = append(p.Placements, owner)
		}
		err := libJoinNetsMode(&p, map[string]string{"N": "direct", "BLOCK": "module_port"}, false, false)
		var conflict *schematicRouteConflict
		if !errors.As(err, &conflict) || conflict.net != "N" || conflict.ownersComplete != known {
			t.Fatalf("known=%v: missing structured route conflict/ownership: %v", known, err)
		}
		if known && !conflict.blockers["Q"] {
			t.Fatal("ignored a component whose wire, but not body, blocks the route")
		}
		if !conflict.endpointOwners["A"] || !conflict.endpointOwners["B"] {
			t.Fatalf("lost physical-island endpoint owners: %+v", conflict.endpointOwners)
		}
		s := schematicRepairSearch{measured: map[string]powerLayoutPlacement{"owner": owner, "other": {Designator: "R", Pins: []powerLayoutPin{{Net: "OTHER"}}}}}
		if !s.participates("owner", conflict) || (!known && !s.participates("other", conflict)) {
			t.Fatal("unsafe pruning for real or unknown wire owners")
		}
		s.focused = true
		if !known && !s.participates("other", conflict) {
			t.Fatal("focused pass pruned unknown wire ownership")
		}
	}
}

func TestFocusedRouteConflictUsesActualBlockerBeforeEndpointFallback(t *testing.T) {
	conflict := &schematicRouteConflict{net: "N", endpointOwners: map[string]bool{"A": true, "B": true}, blockers: map[string]bool{"Q": true}, ownersComplete: true}
	s := schematicRepairSearch{focused: true, measured: map[string]powerLayoutPlacement{
		"a": {Designator: "A", Pins: []powerLayoutPin{{Net: "N"}}},
		"c": {Designator: "C", Pins: []powerLayoutPin{{Net: "N"}}},
		"q": {Designator: "Q", Pins: []powerLayoutPin{{Net: "BLOCK"}}},
	}}
	if s.participates("a", conflict) || s.participates("c", conflict) || !s.participates("q", conflict) {
		t.Fatal("focused pass did not isolate actual rejected-edge blockers")
	}
	s.focused = false
	if !s.participates("a", conflict) || s.participates("c", conflict) || !s.participates("q", conflict) {
		t.Fatal("full pass did not combine endpoint and actual blocker owners")
	}
}

func TestTargetedRelocationExpandsPrimaryAxisWithinBound(t *testing.T) {
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{
		{Designator: "U1", X: 0, Y: 0},
		{Designator: "J1", X: -20, Y: 5},
	}}
	deltas := schematicTargetedRelocationDeltas(&p, "J1", "U1")
	if len(deltas) != 32 || deltas[0] != [2]float64{-5, 0} || deltas[1] != [2]float64{-10, 0} || deltas[6] != [2]float64{-35, 0} || deltas[7] != [2]float64{-40, 0} {
		t.Fatal("primary outward relocation shell is incomplete or unordered", deltas)
	}
	for _, delta := range deltas {
		if math.Abs(delta[0]) > 40 || math.Abs(delta[1]) > 40 || (delta[0] != 0 && delta[1] != 0) {
			t.Fatal("relocation escaped its bounded cardinal shell", delta)
		}
	}
}

func TestSchematicRepairFailureKeepsSharedBudgetAndNoPartialResult(t *testing.T) {
	in := schematicRepairFixture()
	before, _ := json.Marshal(in)
	for _, maximum := range []int{1, 7, 128, 512} {
		budget := maximum
		out, err := planSchematicLayoutWithBudget(in, &budget)
		if err == nil || out != nil || !errors.Is(err, errLibLayoutBudget) {
			t.Fatalf("max=%d: invalid failure result %+v, %v", maximum, out, err)
		}
		if budget < 0 || budget >= maximum || !strings.Contains(err.Error(), "no capacity proof") {
			t.Fatal("budget reset/underflow or ambiguous failure", budget, err)
		}
	}
	after, _ := json.Marshal(in)
	if !bytes.Equal(before, after) {
		t.Fatal("failed search changed evidence")
	}
}

func TestAttachmentPairsShareDistanceShellBudget(t *testing.T) {
	core := powerLayoutPlacement{Designator: "U1", BBox: layoutBBox{-20, -20, 20, 20}, Pins: []powerLayoutPin{{Number: "L", Net: "N", X: -30, Y: 0}, {Number: "R", Net: "N", X: 30, Y: 0}}}
	// Leave the core's required 5raw outward exit clear; the wall still
	// blocks attachment on this side without making every trial invalid.
	wall := powerLayoutPlacement{Designator: "X1", BBox: layoutBBox{-60, -300, -36, 300}, TextBBoxes: []layoutBBox{{-60, -10, -50, 10}}, Pins: []powerLayoutPin{{Number: "1", X: -70, Y: 0}}}
	part := powerLayoutPlacement{Designator: "R1", BBox: layoutBBox{-5, -5, 5, 5}, Pins: []powerLayoutPin{{Number: "1", Net: "N", X: -15, Y: 0}, {Number: "2", Net: "G", X: 15, Y: 0}}}
	current := powerLayoutPlan{Placements: []powerLayoutPlacement{core, wall}}
	bad := libAttachmentPair{host: core.Pins[0], own: part.Pins[0], side: "left"}
	good := libAttachmentPair{host: core.Pins[1], own: part.Pins[0], side: "right"}
	policies := map[string]string{"N": "local_power", "G": "local_ground"}
	budget := 1000
	if out, err := libPlacePeripheral(current, part, bad, policies, &budget); out != nil || err == nil {
		t.Fatal("first pair should be blocked by the fixed wall")
	}
	budget = 1000
	out, err := libPlacePeripheralPairs(current, part, []libAttachmentPair{bad, good}, policies, &budget, nil)
	if err != nil || out == nil || out.Placements[2].X <= 0 || budget <= 0 {
		t.Fatalf("first blocked pair monopolized the budget: %v; remaining=%d", err, budget)
	}
}

func TestPlacementAlternativeCursorAdvancesDistanceShell(t *testing.T) {
	core := powerLayoutPlacement{Designator: "U1", BBox: layoutBBox{-20, -20, 20, 20}, Pins: []powerLayoutPin{{Number: "1", Net: "N", X: 30, Y: 0, Rotation: directionNumber(0)}}}
	part := powerLayoutPlacement{Designator: "R1", BBox: layoutBBox{-10, -10, 10, 10}, Pins: []powerLayoutPin{{Number: "1", Net: "N", X: -20, Y: 0, Rotation: directionNumber(180)}}}
	pair := libAttachmentPair{host: core.Pins[0], own: part.Pins[0], side: "right"}
	budget, cursor := 1000, 5.0
	first, err := libPlacePeripheralPairsWithRouting(powerLayoutPlan{Placements: []powerLayoutPlacement{core}}, part, []libAttachmentPair{pair}, map[string]string{"N": "module_port"}, &budget, nil, nil, &cursor)
	if err != nil || first == nil || cursor <= 5 {
		t.Fatalf("first shell did not advance cursor: cursor=%g err=%v", cursor, err)
	}
	secondStart := cursor
	firstXY := [2]float64{first.Placements[1].X, first.Placements[1].Y}
	second, err := libPlacePeripheralPairsWithRouting(powerLayoutPlan{Placements: []powerLayoutPlacement{core}}, part, []libAttachmentPair{pair}, map[string]string{"N": "module_port"}, &budget, map[[2]float64]bool{firstXY: true}, nil, &cursor)
	if err != nil || second == nil || cursor != secondStart+5 {
		t.Fatalf("second shell did not advance cursor: cursor=%g err=%v", cursor, err)
	}
	if second.Placements[1].Pins[0].X-first.Placements[1].Pins[0].X != 5 {
		t.Fatalf("checkpoint alternative stayed in the exhausted shell: first=%+v second=%+v", first.Placements[1], second.Placements[1])
	}
}

func TestAttachmentDepthKeepsOwnedPeripheralClusterTogether(t *testing.T) {
	hints := map[string]SchematicLayoutPeripheral{
		"host-a":  {ComponentID: "host-a", AttachTo: &SchematicLayoutAttach{ComponentID: "core"}},
		"child-a": {ComponentID: "child-a", AttachTo: &SchematicLayoutAttach{ComponentID: "host-a"}},
		"leaf-a":  {ComponentID: "leaf-a", AttachTo: &SchematicLayoutAttach{ComponentID: "child-a"}},
		"host-b":  {ComponentID: "host-b", AttachTo: &SchematicLayoutAttach{ComponentID: "core"}},
	}
	depth := schematicAttachmentDepths("core", []string{"core", "host-a", "child-a", "leaf-a", "host-b"}, hints)
	if depth["core"] != 0 || depth["host-a"] != 1 || depth["child-a"] != 2 || depth["leaf-a"] != 3 || depth["host-b"] != 1 {
		t.Fatalf("unexpected attachment depths: %+v", depth)
	}
}

func TestConnectedPinCountIsNameAndOrderIndependent(t *testing.T) {
	dense := powerLayoutPlacement{Designator: "J9", Pins: []powerLayoutPin{{Net: "A"}, {Net: "B"}, {Net: ""}, {Net: "C"}}}
	small := powerLayoutPlacement{Designator: "D1", Pins: []powerLayoutPin{{Net: "A"}, {Net: "B"}}}
	if libConnectedPinCount(dense) != 3 || libConnectedPinCount(small) != 2 {
		t.Fatal("constraint rank depends on something other than connected source pins")
	}
	dense.Designator, small.Designator = "X1", "Z99"
	if libConnectedPinCount(dense) <= libConnectedPinCount(small) {
		t.Fatal("renaming changed constrained-first ordering")
	}
}

func TestExplicitDenseAttachmentHostRanksBeforeAutomaticOrSparseHost(t *testing.T) {
	measured := map[string]powerLayoutPlacement{
		"dense":  {Designator: "X1", Pins: []powerLayoutPin{{Net: "A"}, {Net: "B"}, {Net: "C"}}},
		"sparse": {Designator: "X2", Pins: []powerLayoutPin{{Net: "A"}}},
	}
	hints := map[string]SchematicLayoutPeripheral{
		"dense-child":  {ComponentID: "dense-child", AttachTo: &SchematicLayoutAttach{ComponentID: "dense"}},
		"sparse-child": {ComponentID: "sparse-child", AttachTo: &SchematicLayoutAttach{ComponentID: "sparse"}},
		"automatic":    {ComponentID: "automatic"},
	}
	if got := schematicAttachmentHostPinCount("dense-child", measured, hints); got != 3 {
		t.Fatalf("dense host rank=%d", got)
	}
	if schematicAttachmentHostPinCount("dense-child", measured, hints) <= schematicAttachmentHostPinCount("sparse-child", measured, hints) || schematicAttachmentHostPinCount("automatic", measured, hints) != 0 {
		t.Fatal("host constraint rank depends on source order instead of explicit measured ownership")
	}
}
