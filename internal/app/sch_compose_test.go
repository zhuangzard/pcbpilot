package app

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

// A deliberately small synthetic three-pin device: VDD on the left, GND on
// the lower right, and an observed NC on the upper right. Each module owns one
// instance and two real wire-to-marker trees. No editor or ignored data is used.
func composeFixture(moduleCount int) schCompositionSource {
	src := schCompositionSource{
		SchemaVersion: 1,
		Sheet:         layoutBBox{MinX: 50, MinY: -20, MaxX: 530, MaxY: 580},
		Connectivity: connectivity.Document{
			SchemaVersion: "1.4", ProjectID: "synthetic-project", DocumentID: "single-sheet",
			Nets: []connectivity.Net{{ID: "stable-vdd-id", Name: "+3V3"}, {ID: "stable-gnd-id", Name: "GND"}},
		},
	}
	for i := 0; i < moduleCount; i++ {
		ref, moduleID := fmt.Sprintf("U%d", i+1), fmt.Sprintf("module-%d", i+1)
		componentID := "stable-" + ref
		src.Connectivity.Components = append(src.Connectivity.Components, connectivity.Component{
			ID: componentID, Ref: ref,
			Device: connectivity.Device{LibraryUUID: "11111111111111111111111111111111", UUID: "22222222222222222222222222222222", Name: "synthetic-3-pin"},
			Pins:   []connectivity.Pin{{Number: "1", Name: "VDD"}, {Number: "2", Name: "GND"}, {Number: "3", Name: "RESERVED", NoConnected: true}},
		})
		src.Connectivity.Connections = append(src.Connectivity.Connections,
			connectivity.Connection{ComponentID: componentID, PinNumber: "1", NetID: "stable-vdd-id", Kind: "pin_net"},
			connectivity.Connection{ComponentID: componentID, PinNumber: "2", NetID: "stable-gnd-id", Kind: "pin_net"})
		src.Connectivity.Modules = append(src.Connectivity.Modules, connectivity.Module{ID: moduleID, Name: ref, CoreComponents: []string{componentID}})
		src.Modules = append(src.Modules, schCompositionModule{
			ID: moduleID, Title: "MODULE " + ref,
			Placements: []powerLayoutPlacement{{PrimitiveID: "obsolete-instance-" + ref, Designator: ref, Value: "synthetic", X: 100, Y: 100,
				BBox: layoutBBox{MinX: 90, MinY: 80, MaxX: 110, MaxY: 120},
				Pins: []powerLayoutPin{{Number: "1", Name: "VDD", Net: "+3V3", X: 70, Y: 110}, {Number: "2", Name: "GND", Net: "GND", X: 130, Y: 90}, {Number: "3", Name: "RESERVED", X: 130, Y: 110}},
			}},
			Wires: []powerLayoutWire{{Net: "+3V3", Points: [][2]float64{{70, 110}, {50, 110}}}, {Net: "GND", Points: [][2]float64{{130, 90}, {150, 90}}}},
			Flags: []powerLayoutFlag{
				{Net: "+3V3", Kind: "power", PinX: 50, PinY: 110, Direction: "up", Offset: 15 + 10*float64(i)},
				{Net: "GND", Kind: "ground", PinX: 150, PinY: 90, Direction: "down", Offset: 15},
			},
		})
	}
	return src
}

func TestComposeFixedMarginsAndContentHeightZRows(t *testing.T) {
	src := composeFixture(4)
	plan, err := planSchComposition(src)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Rows != 2 || len(plan.Layout.Frames) != 4 {
		t.Fatalf("four ordered modules must form two rows: rows=%d frames=%d", plan.Rows, len(plan.Layout.Frames))
	}
	if plan.PageMargin != 10 || plan.ModuleGap != 10 {
		t.Fatalf("fixed 0.1-inch edge/gap contract changed: %g/%g", plan.PageMargin, plan.ModuleGap)
	}
	f := plan.Layout.Frames
	if f[0].Rect.MinX != src.Sheet.MinX+10 || f[0].Rect.MaxY != src.Sheet.MaxY-10 || f[2].Rect.MinX != src.Sheet.MinX+10 {
		t.Fatal("Z rows must start at the fixed top-left inset, then return to the left edge")
	}
	for i, frame := range f {
		if frame.ID != src.Modules[i].ID || frame.Rect.MaxY-frame.Rect.MinY > plan.RowHeights[i/2] {
			t.Fatalf("module order or row maximum diagnostic changed: %+v", frame)
		}
		if !boxInside(frame.Rect, layoutBBox{MinX: src.Sheet.MinX + 10, MinY: src.Sheet.MinY + 10, MaxX: src.Sheet.MaxX - 10, MaxY: src.Sheet.MaxY - 10}) {
			t.Fatal("frame crosses the fixed paper inset")
		}
	}
	for _, first := range []int{0, 2} {
		if f[first+1].Rect.MinX-f[first].Rect.MaxX != 10 || f[first].Rect.MaxY != f[first+1].Rect.MaxY {
			t.Fatal("neighbours must share top edges and the fixed horizontal gap")
		}
	}
	if math.Min(f[0].Rect.MinY, f[1].Rect.MinY)-f[2].Rect.MaxY != 10 {
		t.Fatal("row-to-row gap must follow the tallest frame in the previous row")
	}
	if f[0].Rect.MinY == f[1].Rect.MinY || f[0].Rect.MaxY-f[0].Rect.MinY == plan.RowHeight {
		t.Fatal("the short first module must not inherit another module's height")
	}
}

func TestComposeTerminalDeclarationsCompileToGuardedStraightLeads(t *testing.T) {
	src := composeFixture(1)
	src.Modules[0].Wires, src.Modules[0].Flags = nil, nil
	src.Modules[0].Terminals = []schCompositionTerminal{
		{Designator: "U1", Pin: "1", Direction: "left", Kind: "power"},
		{Designator: "U1", Pin: "2", Direction: "right", Kind: "ground"},
	}
	before, _ := json.Marshal(src)
	p, err := planSchComposition(src)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(src)
	if string(before) != string(after) || !reflect.DeepEqual(src.Connectivity.Connections, p.Connectivity.Connections) {
		t.Fatal("terminal routing changed source data or canonical connections")
	}
	if len(p.Layout.Wires) != 0 || len(p.Layout.Flags) != 2 {
		t.Fatalf("terminal routing must emit only two straight marker leads: %+v", p.Layout)
	}
	for i, f := range p.Layout.Flags {
		pin := p.Layout.Placements[0].Pins[i]
		if f.PinX != pin.X || f.PinY != pin.Y || f.Net != pin.Net || f.Offset < 10 || f.Direction != src.Modules[0].Terminals[i].Direction {
			t.Fatalf("generated lead lost measured pin geometry/net: %+v", f)
		}
	}
	live := map[string]any{"context": map[string]any{"projectUuid": p.Connectivity.ProjectID, "documentUuid": p.Connectivity.DocumentID}, "result": map[string]any{"components": []any{map[string]any{"componentType": "sheet", "primitiveId": "sheet", "bbox": p.Sheet}}, "wires": []any{}, "count": 1, "connectivitySummary": map[string]any{"scope": "activePage", "wires": 0, "buses": 0, "shortSymbols": 0}}}
	pb, err := schCompositionPlaybook(p, composeApplyBytes(t, live), true)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, step := range pb.Steps {
		if step.Action == "schematic.power.connect_pin" {
			count++
		}
		if step.Action == "schematic.wire.create" {
			t.Fatal("straight terminal declarations produced extra polyline wires")
		}
	}
	if count != 2 || !pb.RequireFullExecution {
		t.Fatalf("expected guarded queue with two real lead actions: count=%d", count)
	}
}

func TestComposePreservesElectricalIdentityAndRigidModuleGeometry(t *testing.T) {
	src := composeFixture(4)
	before, _ := json.Marshal(src)
	plan, err := planSchComposition(src)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(src)
	if string(before) != string(after) {
		t.Fatal("planning mutated its input IR or measured geometry")
	}
	if !reflect.DeepEqual(src.Connectivity.Nets, plan.Connectivity.Nets) || !reflect.DeepEqual(src.Connectivity.Connections, plan.Connectivity.Connections) || !reflect.DeepEqual(src.Connectivity.Modules, plan.Connectivity.Modules) {
		t.Fatal("composition changed network IDs, connections or canonical membership")
	}
	for i, c := range plan.Connectivity.Components {
		original := src.Connectivity.Components[i]
		if c.ID != original.ID || c.Ref != original.Ref || c.Device != original.Device || c.PageID != src.Connectivity.DocumentID || c.Placement == nil || len(c.Pins) != 3 {
			t.Fatalf("lost component identity or single-page target: %+v", c)
		}
		placed, local := plan.Layout.Placements[i], src.Modules[i].Placements[0]
		if placed.PrimitiveID != "" {
			t.Fatal("old editor instance ID must not be reused as a placement identity")
		}
		dx, dy := placed.X-local.X, placed.Y-local.Y
		for j, pin := range c.Pins {
			op, lp, pp := original.Pins[j], local.Pins[j], placed.Pins[j]
			if pin.Number != op.Number || pin.Name != op.Name || pin.NoConnected != op.NoConnected {
				t.Fatal("pin identity or explicit NC was lost")
			}
			if pp.X-lp.X != dx || pp.Y-lp.Y != dy || pin.X != pp.X || pin.Y != pp.Y {
				t.Fatal("module packing distorted a measured pin rather than translating it")
			}
		}
	}
	if len(plan.Layout.ExpectedPinNets) != 8 {
		t.Fatal("every connected pin must retain its expected net")
	}
	// Old page coordinates are irrelevant: shift each complete module by a
	// different amount and require the same final composition, including routes.
	for i := range src.Modules {
		m := &src.Modules[i]
		p := powerLayoutPlan{Placements: m.Placements, Wires: m.Wires, Flags: m.Flags}
		translatePowerLayout(&p, float64(i+1)*-750, float64(i+1)*325)
		m.Placements, m.Wires, m.Flags = p.Placements, p.Wires, p.Flags
	}
	again, err := planSchComposition(src)
	if err != nil || !reflect.DeepEqual(plan, again) {
		t.Fatalf("source-page XY must not affect final placement: %v", err)
	}
}

func TestComposeRejectsIncompleteOrUnsafeInput(t *testing.T) {
	cases := []struct {
		name, want string
		change     func(*schCompositionSource)
	}{
		{"missing measured pin", "pin set differs", func(s *schCompositionSource) { s.Modules[0].Placements[0].Pins = s.Modules[0].Placements[0].Pins[:2] }},
		{"missing IR connection", "exactly one net or explicit NC", func(s *schCompositionSource) {
			s.Connectivity.Connections = s.Connectivity.Connections[1:]
			s.Modules[0].Placements[0].Pins[0].Net = ""
		}},
		{"missing NC evidence", "exactly one net or explicit NC", func(s *schCompositionSource) { s.Connectivity.Components[0].Pins[2].NoConnected = false }},
		{"foreign pin net", "differs from IR", func(s *schCompositionSource) { s.Modules[0].Placements[0].Pins[0].Net = "GND" }},
		{"wire joins two nets", "wire bridge", func(s *schCompositionSource) { s.Modules[0].Wires[0].Net = "GND" }},
		{"wire touches NC", "NC/foreign pin", func(s *schCompositionSource) {
			s.Modules[0].Wires = append(s.Modules[0].Wires, powerLayoutWire{Net: "+3V3", Points: [][2]float64{{70, 110}, {130, 110}}})
		}},
		{"wire crosses symbol body", "passes through", func(s *schCompositionSource) {
			s.Modules[0].Wires = append(s.Modules[0].Wires, powerLayoutWire{Net: "+3V3", Points: [][2]float64{{70, 110}, {100, 110}, {100, 140}, {70, 140}, {70, 110}}})
		}},
		{"missing real marker", "named wire tree", func(s *schCompositionSource) { s.Modules[0].Flags = s.Modules[0].Flags[1:] }},
		{"unsupported marker alias", "unsupported marker kind", func(s *schCompositionSource) { s.Modules[0].Flags[0].Kind = "netport" }},
		{"orphan marker", "orphan", func(s *schCompositionSource) {
			s.Modules[0].Flags = append(s.Modules[0].Flags, powerLayoutFlag{Net: "+3V3", Kind: "power", PinX: 250, PinY: 250, Direction: "up", Offset: 15})
		}},
		{"unknown module member", "unknown/duplicate member", func(s *schCompositionSource) { s.Connectivity.Modules[0].CoreComponents[0] = "nonexistent-component" }},
		{"wrong module ownership", "unknown/repeated/unowned", func(s *schCompositionSource) { s.Modules[0].Placements[0].Designator = "U2" }},
		{"omitted module", "cover every", func(s *schCompositionSource) { s.Modules = s.Modules[:1] }},
		{"unresolved device", "real library/device UUID", func(s *schCompositionSource) { s.Connectivity.Components[0].Device.UUID = "editor-instance" }},
		{"ambiguous net names", "different net IDs share name", func(s *schCompositionSource) { s.Connectivity.Nets[1].Name = "+3V3" }},
		{"off-grid component", "grid/orientation", func(s *schCompositionSource) { s.Modules[0].Placements[0].X++ }},
		{"off-grid pin", "geometry/net differs", func(s *schCompositionSource) { s.Modules[0].Placements[0].Pins[0].Y++ }},
		{"page too narrow", "wider than", func(s *schCompositionSource) { s.Sheet.MaxX = s.Sheet.MinX + 50 }},
		{"page too short", "exceed the single sheet", func(s *schCompositionSource) { s.Sheet.MaxY = s.Sheet.MinY + 50 }},
		{"title block overlap", "title-block keepout", func(s *schCompositionSource) { s.Keepouts = []layoutBBox{s.Sheet} }},
		{"keepout outside sheet", "invalid keepout", func(s *schCompositionSource) {
			s.Keepouts = []layoutBBox{{MinX: -100, MinY: -100, MaxX: 100, MaxY: 100}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := composeFixture(2)
			tc.change(&src)
			if _, err := planSchComposition(src); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want refusal containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestComposeUsesRuntimeA4TitleBlockKeepout(t *testing.T) {
	sheet := layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825}
	visible := true
	g := deriveSheetGeometry(&sheet, &visible)
	want := layoutBBox{MinX: 468, MinY: 0, MaxX: 1170, MaxY: 198}
	if g.TitleBlock.BBox == nil || *g.TitleBlock.BBox != want || g.TitleBlock.Source != sheetSourceKnownTemplate {
		t.Fatalf("A4 keepout must cover the calibrated full table, not the stale 22%% x 14%% sub-rectangle: %+v", g.TitleBlock)
	}
	src := composeFixture(2)
	src.Sheet = sheet
	src.Keepouts = []layoutBBox{want}
	plan, err := planSchComposition(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan.Layout.Frames {
		if boxesGapOverlap(f.Rect, want, 0) {
			t.Fatal("accepted composition overlaps the runtime-derived title-block keepout")
		}
	}
}

func TestComposeEnforcesTenUnitTitleBlockClearance(t *testing.T) {
	src := composeFixture(2)
	plan, err := planSchComposition(src)
	if err != nil {
		t.Fatal(err)
	}
	frame := plan.Layout.Frames[0].Rect
	// The exclusion is below the frame, wholly inside the synthetic sheet.
	// Nine units is still too close; ten units is a legal fixed-margin fit.
	src.Keepouts = []layoutBBox{{MinX: frame.MinX, MinY: frame.MinY - 30, MaxX: frame.MaxX, MaxY: frame.MinY - 9}}
	if _, err := planSchComposition(src); err == nil || !strings.Contains(err.Error(), "title-block keepout") {
		t.Fatalf("nine-unit title-block clearance must fail: %v", err)
	}
	src.Keepouts[0].MaxY = frame.MinY - 10
	if _, err := planSchComposition(src); err != nil {
		t.Fatalf("exactly ten units must remain usable: %v", err)
	}
}
