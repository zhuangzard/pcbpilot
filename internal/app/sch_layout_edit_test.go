package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

func layoutEditPageFixture(t *testing.T, source SchematicZonesInput) SchematicRenderInput {
	t.Helper()
	planned, err := PlanSchematicZones(source)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(planned)
	var render SchematicRenderInput
	if err := json.Unmarshal(raw, &render); err != nil {
		t.Fatal(err)
	}
	render.Sheet = &SchematicRenderSheet{
		Bounds:   SchematicBox{MinX: 0, MinY: 0, MaxX: 3000, MaxY: 2000},
		Border:   SchematicBox{MinX: 0, MinY: 0, MaxX: 3000, MaxY: 2000},
		Keepouts: []SchematicBox{}, Padding: 20, Gap: 20, Flow: "z",
	}
	pages, err := PlanSchematicSheets(render)
	if err != nil || len(pages.Pages) != 1 {
		t.Fatalf("fixture sheet plan failed: pages=%d err=%v", len(pages.Pages), err)
	}
	return pages.Pages[0]
}

func layoutEditSnapshotFromPage(t *testing.T, page SchematicRenderInput, skipNet string) SchematicLayoutEditSnapshot {
	t.Helper()
	components, wires := []any{}, []any{}
	wireIndex, markerIndex := 0, 0
	for _, zone := range page.Zones {
		dx := zone.SheetPosition.X - zone.Frame.Rect.MinX
		dy := zone.SheetPosition.Y - zone.Frame.Rect.MaxY
		for _, placement := range zone.Layout.Placements {
			pins := []any{}
			for _, pin := range placement.Pins {
				rotation := pin.Rotation
				if rotation == nil {
					value, err := libPinOutwardRotation(pin, placement.BBox)
					if err != nil {
						t.Fatal(err)
					}
					rotation = &value
				}
				pins = append(pins, map[string]any{"pinNumber": pin.Number, "x": pin.X + dx, "y": pin.Y + dy, "rotation": *rotation, "net": pin.Net})
			}
			id := zone.Layout.ComponentIDs[placement.Designator]
			components = append(components, map[string]any{
				"componentType": "part", "primitiveId": "live-" + id, "designator": placement.Designator,
				"otherProperty": map[string]any{connectivity.ComponentIDProperty: id},
				"x":             placement.X + dx, "y": placement.Y + dy, "rotation": placement.Rotation,
				"bbox":          map[string]any{"minX": placement.BBox.MinX + dx, "minY": placement.BBox.MinY + dy, "maxX": placement.BBox.MaxX + dx, "maxY": placement.BBox.MaxY + dy},
				"pinsAvailable": true, "pins": pins,
			})
		}
		for _, wire := range zone.Layout.Wires {
			for i := 1; i < len(wire.Points); i++ {
				wireIndex++
				wires = append(wires, map[string]any{"primitiveId": "layout-wire-" + zone.ID + "-" + string(rune('a'+wireIndex)), "x0": wire.Points[i-1][0] + dx, "y0": wire.Points[i-1][1] + dy, "x1": wire.Points[i][0] + dx, "y1": wire.Points[i][1] + dy})
			}
		}
		for _, flag := range zone.Layout.Flags {
			if flag.Net == skipNet {
				continue
			}
			markerIndex++
			ex, ey := endpointFor(flag.PinX+dx, flag.PinY+dy, flag.Offset, flag.Direction)
			family := "port"
			componentType := "netport"
			if flag.Kind == "power" || flag.Kind == "ground" {
				family, componentType = flag.Kind, "netflag"
			}
			components = append(components, map[string]any{"componentType": componentType, "primitiveId": "layout-marker-" + zone.ID + "-" + string(rune('a'+markerIndex)), "x": ex, "y": ey, "rotation": flagBodyRotation[family][flag.Direction], "net": flag.Net})
			wireIndex++
			wires = append(wires, map[string]any{"primitiveId": "layout-wire-" + zone.ID + "-" + string(rune('a'+wireIndex)), "x0": flag.PinX + dx, "y0": flag.PinY + dy, "x1": ex, "y1": ey})
		}
	}
	return SchematicLayoutEditSnapshot{Context: schematicLayoutEditContext{ProjectUUID: "project", DocumentUUID: "document"}, Result: map[string]any{"components": components, "wires": wires, "wiresAvailable": true}}
}

func layoutEditD1Fixture(t *testing.T) (SchematicZonesInput, SchematicRenderInput, SchematicLayoutEditSnapshot) {
	t.Helper()
	base := standaloneLayoutFixture()
	base.Components[0].ID = "stable-d1"
	base.Components[0].Measurement.Designator = "D1"
	base.Components[0].Measurement.Pins[2].Net = "USB_DM"
	base.Components[0].Measurement.Pins[2].X = base.Components[0].Measurement.BBox.MinX
	base.Components[0].Measurement.Pins[2].Y = base.Components[0].Measurement.Y
	rotation := 180.0
	base.Components[0].Measurement.Pins[2].Rotation = &rotation
	base.Components[0].PinStates = nil
	base.Components[1].ID = "stable-r1"
	base.NetPolicies["USB_DM"] = "module_port"
	spacing := 20.0
	source := SchematicZonesInput{SchemaVersion: 1, Spacing: &spacing, Components: base.Components, NetPolicies: base.NetPolicies, Zones: []SchematicZone{{ID: "usb", Title: "USB", CoreComponentID: "stable-d1", ComponentIDs: []string{"stable-d1", "stable-r1"}}}}
	page := layoutEditPageFixture(t, source)
	page.Sheet.Flow = "fixed"
	if err := layoutEditSetCorePagePoint(&page.Zones[0], 185, 1050); err != nil {
		t.Fatal(err)
	}
	if err := layoutEditPageValid(page); err != nil {
		t.Fatal(err)
	}
	snapshot := layoutEditSnapshotFromPage(t, page, "USB_DM")
	snapshot.Result["components"] = append(snapshot.Result["components"].([]any), map[string]any{"componentType": "netport", "primitiveId": "marker-old", "x": 245.0, "y": 1050.0, "rotation": 0.0, "net": "USB_DM"})
	snapshot.Result["wires"] = append(snapshot.Result["wires"].([]any), map[string]any{"primitiveId": "wire-old", "x0": 165.0, "y0": 1050.0, "x1": 245.0, "y1": 1050.0})
	return source, page, snapshot
}

func TestLayoutEditD1Pin3BuildsOutwardProtectedRepair(t *testing.T) {
	source, page, snapshot := layoutEditD1Fixture(t)
	report := &SchematicLayoutEditReport{}
	target, playbook, err := planSchematicPinMarkerRepair(source, page, snapshot, "stable-d1", "3", 20, report)
	if err != nil {
		t.Fatal(err)
	}
	if target.Expected.WirePrimitiveID != "wire-old" || target.Expected.MarkerPrimitiveID != "marker-old" || target.Replacement.Direction != "left" || target.Replacement.Offset != 20 || target.Replacement.Rotation != 180 {
		t.Fatalf("unexpected D1.3 repair target: %+v", target)
	}
	if report.TargetX != 145 || report.TargetY != 1050 || report.Strategy != "pin-outward-axis" {
		t.Fatalf("wrong D1.3 endpoint/report: %+v", report)
	}
	if playbook == nil || len(playbook.Steps) != 3 || playbook.Steps[0].Action != "schematic.pin.repair_marker" || playbook.Steps[0].TimeoutSec == nil || *playbook.Steps[0].TimeoutSec != 120 {
		t.Fatalf("protected playbook contract missing: %+v", playbook)
	}
	if target.Replacement.Net != "USB_DM" || target.Expected.PinNumber != "3" {
		t.Fatal("repair lost electrical binding")
	}
}

func TestLayoutEditD1Pin3SearchesOnlyOutwardAxis(t *testing.T) {
	source, page, snapshot := layoutEditD1Fixture(t)
	// Block the preferred 20-raw endpoint with an unrelated vertical wire. The
	// next legal result may change length, never direction or anchor type.
	snapshot.Result["wires"] = append(snapshot.Result["wires"].([]any), map[string]any{"primitiveId": "block", "x0": 145.0, "y0": 1040.0, "x1": 145.0, "y1": 1060.0})
	report := &SchematicLayoutEditReport{}
	target, _, err := planSchematicPinMarkerRepair(source, page, snapshot, "stable-d1", "3", 20, report)
	if err != nil {
		t.Fatal(err)
	}
	if target.Replacement.Direction != "left" || target.Replacement.Offset != 10 || report.TargetX != 155 || len(report.RejectedCandidates) == 0 {
		t.Fatalf("repair left the pin axis or did not record rejection: target=%+v report=%+v", target, report)
	}
}

func TestLayoutEditRepairRejectsSourceOrPageDrift(t *testing.T) {
	for _, edit := range []func(*SchematicZonesInput, *SchematicRenderInput, *SchematicLayoutEditSnapshot){
		func(source *SchematicZonesInput, _ *SchematicRenderInput, _ *SchematicLayoutEditSnapshot) {
			source.Components[0].Measurement.Pins[2].Net = "STALE"
		},
		func(_ *SchematicZonesInput, _ *SchematicRenderInput, snapshot *SchematicLayoutEditSnapshot) {
			for _, value := range snapshot.Result["components"].([]any) {
				component := value.(map[string]any)
				if component["primitiveId"] == "live-stable-d1" {
					component["x"] = component["x"].(float64) + 5
				}
			}
		},
	} {
		source, page, snapshot := layoutEditD1Fixture(t)
		edit(&source, &page, &snapshot)
		if _, _, err := planSchematicPinMarkerRepair(source, page, snapshot, "stable-d1", "3", 20, &SchematicLayoutEditReport{}); err == nil || !strings.Contains(err.Error(), "source-drift") {
			t.Fatalf("stale source/page accepted: %v", err)
		}
	}
}

func TestLayoutEditCoreRigidMoveTranslatesZoneOnceAndKeepsOthersFixed(t *testing.T) {
	source := zonesFixture()
	spacing := 20.0
	source.Spacing = &spacing
	leaf := source.Components[1]
	leaf.ID = "leaf"
	leaf.Measurement.Designator = "R3"
	source.Components = append(source.Components, leaf)
	source.Zones[0].ComponentIDs = append(source.Zones[0].ComponentIDs, leaf.ID)
	source.Attachments = []SchematicLayoutPeripheral{
		{ComponentID: "peripheral", AttachTo: &SchematicLayoutAttach{ComponentID: "anchor", PinNumber: "1"}},
		{ComponentID: "leaf", AttachTo: &SchematicLayoutAttach{ComponentID: "peripheral", PinNumber: "1"}},
	}
	page := layoutEditPageFixture(t, source)
	snapshot := layoutEditSnapshotFromPage(t, page, "")
	beforePage, _ := json.Marshal(page)
	beforeTarget := *page.Zones[0].Layout
	beforeOther, _ := json.Marshal(page.Zones[1])

	var targetX, targetY float64
	found := false
	for _, point := range [][2]float64{{2500, 500}, {2000, 500}, {2500, 1000}, {1500, 500}} {
		trial, _ := copyLayoutEditPage(page)
		trial.Sheet.Flow = "fixed"
		if layoutEditSetCorePagePoint(&trial.Zones[0], point[0], point[1]) == nil && layoutEditPageValid(trial) == nil {
			targetX, targetY, found = point[0], point[1], true
			break
		}
	}
	if !found {
		t.Fatal("fixture has no empty rigid target")
	}
	report := &SchematicLayoutEditReport{}
	result, err := planSchematicCoreMove(source, page, snapshot, "anchor", targetX, targetY, report)
	if err != nil {
		t.Fatal(err)
	}
	x, y, err := layoutEditCorePagePoint(result.Zones[0])
	if err != nil || x != targetX || y != targetY || report.Strategy != "rigid-translation" || len(report.RelativeChanges) != 0 {
		t.Fatalf("rigid target not preserved: (%g,%g) report=%+v err=%v", x, y, report, err)
	}
	if !reflect.DeepEqual(*result.Zones[0].Layout, beforeTarget) {
		t.Fatal("rigid move applied attachment/core displacement more than once")
	}
	afterOther, _ := json.Marshal(result.Zones[1])
	if !bytes.Equal(beforeOther, afterOther) {
		t.Fatal("unaffected zone moved or changed")
	}
	afterPage, _ := json.Marshal(page)
	if !bytes.Equal(beforePage, afterPage) {
		t.Fatal("core move mutated retained selected page")
	}
}

func TestLayoutEditCoreMoveIsInvariantToDesignatorAndNetRename(t *testing.T) {
	source := zonesFixture()
	spacing := 20.0
	source.Spacing = &spacing
	refs := map[string]string{"U1": "CORE_A", "R1": "PASSIVE_A", "U2": "CORE_B", "R2": "PASSIVE_B"}
	nets := map[string]string{"SUPPLY": "RENAMED_SUPPLY", "RETURN": "RENAMED_RETURN"}
	for i := range source.Components {
		measurement := &source.Components[i].Measurement
		measurement.Designator = refs[measurement.Designator]
		for j := range measurement.Pins {
			if replacement := nets[measurement.Pins[j].Net]; replacement != "" {
				measurement.Pins[j].Net = replacement
			}
		}
	}
	renamedPolicies := map[string]string{}
	for net, policy := range source.NetPolicies {
		if replacement := nets[net]; replacement != "" {
			net = replacement
		}
		renamedPolicies[net] = policy
	}
	source.NetPolicies = renamedPolicies
	page := layoutEditPageFixture(t, source)
	snapshot := layoutEditSnapshotFromPage(t, page, "")

	var targetX, targetY float64
	for _, point := range [][2]float64{{2500, 500}, {2000, 500}, {2500, 1000}, {1500, 500}} {
		trial, _ := copyLayoutEditPage(page)
		trial.Sheet.Flow = "fixed"
		if layoutEditSetCorePagePoint(&trial.Zones[0], point[0], point[1]) == nil && layoutEditPageValid(trial) == nil {
			targetX, targetY = point[0], point[1]
			break
		}
	}
	if targetX == 0 && targetY == 0 {
		t.Fatal("renamed fixture has no empty rigid target")
	}
	renamedBefore, _ := json.Marshal(struct {
		Source   SchematicZonesInput
		Page     SchematicRenderInput
		Snapshot SchematicLayoutEditSnapshot
	}{source, page, snapshot})
	renamedReport := &SchematicLayoutEditReport{}
	renamed, err := planSchematicCoreMove(source, page, snapshot, "anchor", targetX, targetY, renamedReport)
	if err != nil {
		t.Fatalf("%v: %+v", err, renamedReport)
	}
	renamedAfter, _ := json.Marshal(struct {
		Source   SchematicZonesInput
		Page     SchematicRenderInput
		Snapshot SchematicLayoutEditSnapshot
	}{source, page, snapshot})
	if !bytes.Equal(renamedBefore, renamedAfter) {
		t.Fatal("rename-invariant move mutated its source/page/snapshot")
	}
	x, y, err := layoutEditCorePagePoint(renamed.Zones[0])
	if err != nil || x != targetX || y != targetY || renamedReport.Strategy != "rigid-translation" {
		t.Fatalf("renamed core target changed: (%g,%g) report=%+v err=%v", x, y, renamedReport, err)
	}
	wantIDs := map[string]bool{"anchor": true, "peripheral": true}
	for ref, id := range renamed.Zones[0].Layout.ComponentIDs {
		if !wantIDs[id] || (ref != "CORE_A" && ref != "PASSIVE_A") {
			t.Fatalf("stable ownership or renamed refs changed: %s=%s", ref, id)
		}
		delete(wantIDs, id)
	}
	if len(wantIDs) != 0 {
		t.Fatalf("renamed output lost stable members: %v", wantIDs)
	}
	for _, placement := range renamed.Zones[0].Layout.Placements {
		for _, pin := range placement.Pins {
			if pin.Net != "" && pin.Net != "RENAMED_SUPPLY" && pin.Net != "RENAMED_RETURN" {
				t.Fatalf("renamed electrical invariant lost on %s.%s: %s", placement.Designator, pin.Number, pin.Net)
			}
		}
	}
}

func TestLayoutEditCoreMoveBudgetExhaustionReturnsNoTarget(t *testing.T) {
	source := zonesFixture()
	spacing := 20.0
	source.Spacing = &spacing
	page := layoutEditPageFixture(t, source)
	snapshot := layoutEditSnapshotFromPage(t, page, "")
	targetX, targetY, err := layoutEditCorePagePoint(page.Zones[1])
	if err != nil {
		t.Fatal(err)
	}
	source.MaxCandidates = 1
	result, err := planSchematicCoreMove(source, page, snapshot, "anchor", targetX, targetY, &SchematicLayoutEditReport{})
	if err == nil || result != nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget exhaustion produced a target: result=%+v err=%v", result, err)
	}
}

func TestLayoutEditCoreCollisionFixesCoreAndReplansOnlyItsZone(t *testing.T) {
	source := zonesFixture()
	spacing := 20.0
	source.Spacing = &spacing
	page := layoutEditPageFixture(t, source)
	page.Sheet.Flow = "fixed"

	// Make the retained first-zone shape legally wider without changing its
	// ownership or electrical data. The source solver still produces the compact
	// shape; this creates a target where rigid translation collides but a local
	// replan fits while the core remains exactly at the requested coordinate.
	expandedRaw, _ := json.Marshal(page.Zones[0].Layout)
	var expanded SchematicLayoutResult
	if err := json.Unmarshal(expandedRaw, &expanded); err != nil {
		t.Fatal(err)
	}
	if len(expanded.Flags) == 0 {
		t.Fatal("fixture needs a naming marker")
	}
	for i := range expanded.Flags {
		expanded.Flags[i].Offset += 200
	}
	measuredExpanded, err := measureSchematicZoneVariant(source.Zones[0], "retained-expanded", &expanded, source.Spacing)
	if err != nil {
		t.Fatal(err)
	}
	page.Zones[0].Layout = measuredExpanded.Layout
	page.Zones[0].Frame = &measuredExpanded.Frame
	page.Zones[0].ContentBounds = &measuredExpanded.ContentBounds
	page.Zones[0].SelectedVariantID = "retained-expanded"
	if err := layoutEditSetCorePagePoint(&page.Zones[0], 500, 700); err != nil {
		t.Fatal(err)
	}
	if err := layoutEditSetCorePagePoint(&page.Zones[1], 2000, 700); err != nil {
		t.Fatal(err)
	}
	if err := layoutEditPageValid(page); err != nil {
		t.Fatalf("expanded retained page is not legal: %v", err)
	}

	compact, err := PlanSchematicLayout(layoutEditLocalInput(source, source.Zones[0]))
	if err != nil {
		t.Fatal(err)
	}
	compactMeasured, err := measureSchematicZoneVariant(source.Zones[0], "compact", compact, source.Spacing)
	if err != nil {
		t.Fatal(err)
	}
	otherRect, err := sheetPreviewRect(page.Zones[1], source.Spacing)
	if err != nil {
		t.Fatal(err)
	}
	compactCore, err := layoutEditZoneCoreLocal(SchematicRenderZone{ID: "compact", CoreComponentID: source.Zones[0].CoreComponentID, Layout: compactMeasured.Layout})
	if err != nil {
		t.Fatal(err)
	}
	clearance := page.Sheet.Gap + 1
	targetX := plFloor(otherRect.MinX - clearance - (compactMeasured.Frame.Rect.MaxX - compactCore.X))
	_, targetY, err := layoutEditCorePagePoint(page.Zones[1])
	if err != nil {
		t.Fatal(err)
	}
	trial, _ := copyLayoutEditPage(page)
	if err := layoutEditSetCorePagePoint(&trial.Zones[0], targetX, targetY); err != nil {
		t.Fatal(err)
	}
	if err := layoutEditPageValid(trial); err == nil {
		t.Fatalf("fixture did not force rigid collision: flags=%+v compactFrame=%+v expandedFrame=%+v other=%+v targetX=%g", expanded.Flags, compactMeasured.Frame.Rect, measuredExpanded.Frame.Rect, otherRect, targetX)
	}

	snapshot := layoutEditSnapshotFromPage(t, page, "")
	otherBefore, _ := json.Marshal(page.Zones[1])
	report := &SchematicLayoutEditReport{}
	result, err := planSchematicCoreMove(source, page, snapshot, source.Zones[0].CoreComponentID, targetX, targetY, report)
	if err != nil {
		t.Fatalf("%v: %+v", err, report)
	}
	coreX, coreY, err := layoutEditCorePagePoint(result.Zones[0])
	if err != nil || coreX != targetX || coreY != targetY || !strings.HasPrefix(report.Strategy, "replanned-") {
		t.Fatalf("fixed-core replan contract failed: core=(%g,%g) strategy=%s err=%v", coreX, coreY, report.Strategy, err)
	}
	otherAfter, _ := json.Marshal(result.Zones[1])
	if !bytes.Equal(otherBefore, otherAfter) {
		t.Fatal("fixed-core replan changed another zone")
	}
}

func TestLayoutEditCoreRejectsCrossZoneTreeAndStaleSnapshot(t *testing.T) {
	source := zonesFixture()
	spacing := 20.0
	source.Spacing = &spacing
	page := layoutEditPageFixture(t, source)
	for _, mode := range []string{"cross-tree", "stale-core"} {
		t.Run(mode, func(t *testing.T) {
			snapshot := layoutEditSnapshotFromPage(t, page, "")
			parts := []map[string]any{}
			for _, raw := range snapshot.Result["components"].([]any) {
				component := raw.(map[string]any)
				if component["componentType"] == "part" {
					parts = append(parts, component)
				}
			}
			if mode == "stale-core" {
				for _, part := range parts {
					if layoutEditStableID(part) == "anchor" {
						part["x"] = part["x"].(float64) + 5
					}
				}
			} else {
				var a, b [2]float64
				for _, part := range parts {
					pin := part["pins"].([]any)[0].(map[string]any)
					point := [2]float64{pin["x"].(float64), pin["y"].(float64)}
					switch layoutEditStableID(part) {
					case "anchor":
						a = point
					case "second-core":
						b = point
					}
				}
				wires := snapshot.Result["wires"].([]any)
				wires = append(wires,
					map[string]any{"primitiveId": "cross-zone-a", "x0": a[0], "y0": a[1], "x1": b[0], "y1": a[1]},
					map[string]any{"primitiveId": "cross-zone-b", "x0": b[0], "y0": a[1], "x1": b[0], "y1": b[1]},
				)
				snapshot.Result["wires"] = wires
			}
			if _, err := planSchematicCoreMove(source, page, snapshot, "anchor", 2500, 500, &SchematicLayoutEditReport{}); err == nil {
				t.Fatalf("%s was accepted", mode)
			}
		})
	}
}

func TestLayoutEditCommandIsRegistered(t *testing.T) {
	command := newSchCmd(&appConfig{}, &bytes.Buffer{}, &bytes.Buffer{})
	found := false
	for _, child := range command.Commands() {
		found = found || child.Name() == "layout-edit"
	}
	if !found {
		t.Fatal("sch layout-edit is not registered")
	}
}

func TestLayoutEditCLIEmitsD1RepairTargetReportAndPlaybook(t *testing.T) {
	source, page, snapshot := layoutEditD1Fixture(t)
	dir := t.TempDir()
	paths := map[string]string{}
	for _, name := range []string{"source", "page", "snapshot", "target", "report", "playbook"} {
		paths[name] = filepath.Join(dir, name+".json")
	}
	for path, value := range map[string]any{paths["source"]: source, paths["page"]: page, paths["snapshot"]: snapshot} {
		raw, _ := json.MarshalIndent(value, "", "  ")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := newSchLayoutEditCmd(&bytes.Buffer{})
	command.SetArgs([]string{"--source", paths["source"], "--page", paths["page"], "--snapshot", paths["snapshot"], "--repair-pin", "stable-d1:3", "--offset", "20", "--out", paths["target"], "--report", paths["report"], "--playbook", paths["playbook"]})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var target SchematicPinMarkerRepairTarget
	var report SchematicLayoutEditReport
	var generated playbook
	for path, value := range map[string]any{paths["target"]: &target, paths["report"]: &report, paths["playbook"]: &generated} {
		raw, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(raw, value) != nil {
			t.Fatalf("invalid generated artifact %s: %v", path, err)
		}
	}
	if target.Replacement.Direction != "left" || target.Replacement.Offset != 20 || report.Status != "planned" || generated.Steps[0].Action != "schematic.pin.repair_marker" {
		t.Fatalf("CLI artifacts disagree: target=%+v report=%+v playbook=%+v", target, report, generated)
	}
}
