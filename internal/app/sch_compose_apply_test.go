package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

// Build an explicit native readback for the synthetic circuit. The complete
// part/pin inventory is also usable as a pre-reset baseline after an edit.
func composeApplyFixture(t *testing.T, matching bool) (*schCompositionPlan, map[string]any) {
	t.Helper()
	src := composeFixture(2)
	src.Modules[0].Placements[0].Rotation = 90
	src.Modules[1].Placements[0].Rotation = 270
	src.Modules[1].Placements[0].Mirror = true
	p, err := planSchComposition(src)
	if err != nil {
		t.Fatal(err)
	}
	parts := []any{map[string]any{"componentType": "sheet", "primitiveId": "sheet", "bbox": p.Sheet}}
	nc := map[string]bool{}
	for _, c := range p.Connectivity.Components {
		for _, pin := range c.Pins {
			nc[c.Ref+"."+pin.Number] = pin.NoConnected
		}
	}
	for _, c := range p.Layout.Placements {
		var device *schematicDeviceExpectation
		for _, canonical := range p.Connectivity.Components {
			if canonical.Ref == c.Designator {
				device = &schematicDeviceExpectation{LibraryUUID: canonical.Device.LibraryUUID, UUID: canonical.Device.UUID}
			}
		}
		pins := []any{}
		for _, pin := range c.Pins {
			pins = append(pins, map[string]any{"pinNumber": pin.Number, "pinName": pin.Name, "x": pin.X, "y": pin.Y, "net": pin.Net, "noConnected": nc[c.Designator+"."+pin.Number]})
		}
		parts = append(parts, map[string]any{"componentType": "part", "primitiveId": "live-" + c.Designator, "device": device, "designator": c.Designator, "x": c.X, "y": c.Y, "rotation": c.Rotation, "mirror": c.Mirror, "bbox": c.BBox, "pinsAvailable": true, "pins": pins})
	}
	wires := []any{}
	for _, w := range p.Layout.Wires {
		for i := 1; i < len(w.Points); i++ {
			a, b := w.Points[i-1], w.Points[i]
			wires = append(wires, map[string]any{"x0": a[0], "y0": a[1], "x1": b[0], "y1": b[1], "net": w.Net})
		}
	}
	for i, f := range p.Layout.Flags {
		x, y := f.PinX, f.PinY
		var rotation float64
		// composeFixture uses only the calibrated power-up / ground-down
		// pair, each stored at rotation zero.
		switch {
		case f.Kind == "power" && f.Direction == "up":
			y += f.Offset
		case f.Kind == "ground" && f.Direction == "down":
			y -= f.Offset
		default:
			t.Fatalf("fixture needs native marker geometry for %+v", f)
		}
		parts = append(parts, map[string]any{"componentType": "netflag", "primitiveId": fmt.Sprintf("flag-%d", i), "net": f.Net, "x": x, "y": y, "rotation": rotation})
		wires = append(wires, map[string]any{"x0": f.PinX, "y0": f.PinY, "x1": x, "y1": y, "net": f.Net})
	}
	env := map[string]any{"context": map[string]any{"projectUuid": p.Connectivity.ProjectID, "documentUuid": p.Connectivity.DocumentID, "documentType": "schematic"}, "result": map[string]any{"components": parts, "wires": wires, "count": len(parts), "connectivitySummary": map[string]any{"scope": "activePage", "wires": len(wires), "buses": 0, "shortSymbols": 0}}}
	// Match the numeric/container shapes returned by the JSON HTTP protocol.
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if !matching {
		part := env["result"].(map[string]any)["components"].([]any)[1].(map[string]any)
		part["x"] = part["x"].(float64) + 5
	}
	return p, env
}

func composeApplyBytes(t *testing.T, env map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func composeStep(t *testing.T, pb *playbook, id string) (int, *playbookStep) {
	t.Helper()
	for i := range pb.Steps {
		if pb.Steps[i].ID == id {
			return i, &pb.Steps[i]
		}
	}
	t.Fatalf("missing composition step %s", id)
	return -1, nil
}

func TestComposeApplyUsesAbsolutePoseAfterZeroRotationCreate(t *testing.T) {
	p, env := composeApplyFixture(t, false)
	p.Connectivity.Components[0].Role = "rf_mcu"
	pb, err := schCompositionPlaybook(p, composeApplyBytes(t, env), true)
	if err != nil {
		t.Fatal(err)
	}
	if !pb.RequireFullExecution || pb.Defaults.Retry == nil || *pb.Defaults.Retry != 0 || pb.Defaults.ContinueOnError == nil || *pb.Defaults.ContinueOnError {
		t.Fatal("destructive composition must run every guard and stop without blind retries")
	}
	resetIndex, reset := composeStep(t, pb, "reset-target-preserving-sheet")
	residualIndex, residual := composeStep(t, pb, "verify-no-residual-primitives")
	emptyIndex, empty := composeStep(t, pb, "verify-cleared-target")
	pinIndex, pinGate := composeStep(t, pb, "verify-physical-pins-before-wiring")
	if reset.Run != "sch clear" || residualIndex <= resetIndex || emptyIndex <= residualIndex || residual.Run != "sch clear" || residual.Flags["dry-run"] != true || residual.Flags["expect-empty"] != true {
		t.Fatal("rebuild must verify residual wires/graphics by a complete dry-run before placement")
	}
	if empty.ExpectSchematic == nil || !empty.ExpectSchematic.ExactParts || len(empty.ExpectSchematic.Parts) != 0 || empty.Assert["$.count"] != "==1" {
		t.Fatal("cleared target must retain only its sheet and no physical parts")
	}
	for i, c := range p.Layout.Placements {
		createIndex, create := composeStep(t, pb, fmt.Sprintf("place-%03d", i))
		orientIndex, orient := composeStep(t, pb, fmt.Sprintf("orient-%03d", i))
		rotation, _ := toFloat(create.Payload["rotation"])
		if createIndex <= emptyIndex || orientIndex != createIndex+1 || orientIndex >= pinIndex || rotation != 0 || create.Payload["mirror"] != false {
			t.Fatalf("%s must be created neutral then absolutely oriented before pin verification", c.Designator)
		}
		capture := fmt.Sprintf("part_%03d", i)
		if create.Action != "schematic.component.place" || create.Capture[capture] != "$.primitiveId" || orient.Action != "schematic.component.modify" || orient.Payload["primitiveId"] != "${"+capture+"}" {
			t.Fatalf("%s must orient the freshly returned primitive, never a source ID", c.Designator)
		}
		patch := orient.Payload["patch"].(map[string]any)
		if patch["rotation"] != c.Rotation || patch["mirror"] != c.Mirror || patch["x"] != c.X || patch["y"] != c.Y {
			t.Fatalf("%s absolute stored pose lost: %+v", c.Designator, patch)
		}
		canonical := p.Connectivity.Components[i]
		properties, ok := patch["otherProperty"].(map[string]any)
		if !ok || properties[connectivity.ComponentIDProperty] != canonical.ID || patch["designator"] != canonical.Ref {
			t.Fatalf("canonical identity must survive fresh placement: %+v", patch)
		}
		if canonical.Role != "" {
			if properties[connectivity.ComponentRoleProperty] != canonical.Role {
				t.Fatal("functional role must be bound independently of the visible reference")
			}
		} else if _, exists := properties[connectivity.ComponentRoleProperty]; exists {
			t.Fatal("placement must not invent a role")
		}
		if create.Payload["otherProperty"] != nil || create.Payload["customAttributes"] != nil {
			t.Fatal("place must not rely on unsupported custom-property inputs")
		}
		assertComponentBindingReadback(t, orient, canonical)
		if pinGate.ExpectSchematic.Parts[c.Designator].BBox == nil || len(pinGate.ExpectSchematic.Parts[c.Designator].Pins) != len(c.Pins) {
			t.Fatal("pre-wiring gate must cover real bbox and every physical pin")
		}
	}
	for i, s := range pb.Steps {
		if (s.Action == "schematic.wire.create" || s.Action == "schematic.power.connect_pin") && i <= pinIndex {
			t.Fatal("wire write precedes the real pin/bbox gate")
		}
	}
	finalIndex, final := composeStep(t, pb, "verify-all-pins-nets-nc")
	saveIndex, _ := composeStep(t, pb, "save-composition")
	if finalIndex >= saveIndex || final.ExpectSchematic.Drawing == nil || !final.ExpectSchematic.ExactParts {
		t.Fatal("save must follow complete electrical and drawing verification")
	}
	// Round-trip the queue through its public file loader, including captured
	// primitive substitution and the newly added bbox/NC/drawing expectations.
	file := filepath.Join(t.TempDir(), "composition.json")
	b, _ := json.Marshal(pb)
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadPlaybook(file)
	if err != nil {
		t.Fatal(err)
	}
	if errs := preflight(loaded, nil); len(errs) != 0 {
		t.Fatalf("generated queue cannot be replayed: %v", errs)
	}
}

func TestComposeApplyRejectsIncompleteDestructiveBaseline(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any, map[string]any)
	}{
		{"missing-pin-inventory", func(_ map[string]any, c map[string]any) { delete(c, "pins") }},
		{"unavailable-pins", func(_ map[string]any, c map[string]any) { c["pinsAvailable"] = false }},
		{"missing-pin-position", func(_ map[string]any, c map[string]any) { delete(c["pins"].([]any)[0].(map[string]any), "x") }},
		{"unknown-pin-net", func(_ map[string]any, c map[string]any) { c["pins"].([]any)[0].(map[string]any)["net"] = nil }},
		{"missing-pin-net", func(_ map[string]any, c map[string]any) { delete(c["pins"].([]any)[0].(map[string]any), "net") }},
		{"duplicate-pin", func(_ map[string]any, c map[string]any) { c["pins"] = append(c["pins"].([]any), c["pins"].([]any)[0]) }},
		{"duplicate-part", func(r map[string]any, c map[string]any) { r["components"] = append(r["components"].([]any), c) }},
		{"ambiguous-net", func(_ map[string]any, c map[string]any) { c["netAmbiguous"] = true }},
		{"unknown-NC", func(_ map[string]any, c map[string]any) { c["pins"].([]any)[0].(map[string]any)["noConnected"] = nil }},
		{"unknown-body", func(_ map[string]any, c map[string]any) { delete(c, "bbox") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, env := composeApplyFixture(t, false)
			r := env["result"].(map[string]any)
			c := r["components"].([]any)[1].(map[string]any)
			tc.edit(r, c)
			if pb, err := schCompositionPlaybook(p, composeApplyBytes(t, env), true); err == nil || pb != nil {
				t.Fatalf("incomplete evidence produced a destructive queue: %v", err)
			}
		})
	}
}

func TestComposeApplyMatchingCircuitDoesNotResetOrDuplicate(t *testing.T) {
	p, env := composeApplyFixture(t, true)
	pb, err := schCompositionPlaybook(p, composeApplyBytes(t, env), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range pb.Steps {
		if s.Run == "sch clear" || s.Action == "schematic.component.place" || s.Action == "schematic.component.modify" || s.Action == "schematic.wire.create" || s.Action == "schematic.power.connect_pin" {
			t.Fatalf("matching circuit caused electrical mutation: %+v", s)
		}
	}
	_, verify := composeStep(t, pb, "verify-existing-composition")
	if verify.ExpectSchematic == nil || verify.ExpectSchematic.Drawing == nil {
		t.Fatal("matching path must still verify actual drawn wire/marker geometry")
	}
}

func TestComposeApplySameNetsWithDetourRequireRebuild(t *testing.T) {
	p, env := composeApplyFixture(t, true)
	r := env["result"].(map[string]any)
	wires := r["wires"].([]any)
	w := wires[0].(map[string]any)
	x0, y0, x1, y1 := w["x0"].(float64), w["y0"].(float64), w["x1"].(float64), w["y1"].(float64)
	r["wires"] = append([]any{drawingWire(x0, y0, x0, y0+10), drawingWire(x0, y0+10, x1, y1+10), drawingWire(x1, y1+10, x1, y1)}, wires[1:]...)
	before := composeApplyBytes(t, env)
	if _, err := schCompositionPlaybook(p, before, false); err == nil || !strings.Contains(err.Error(), "target differs") {
		t.Fatalf("equal pin nets hid different drawn routes: %v", err)
	}
	pb, err := schCompositionPlaybook(p, before, true)
	if err != nil {
		t.Fatal(err)
	}
	composeStep(t, pb, "reset-target-preserving-sheet")
}

func TestComposeApplyOtherPagesAllowDistinctRefsButRejectTargetCollisions(t *testing.T) {
	for _, matching := range []bool{false, true} {
		t.Run(fmt.Sprintf("matching-%t", matching), func(t *testing.T) {
			p, env := composeApplyFixture(t, matching)
			pb, err := schCompositionPlaybook(p, composeApplyBytes(t, env), !matching)
			if err != nil {
				t.Fatal(err)
			}
			guard := &pb.Steps[0]
			if guard.Action != "schematic.components.list" || guard.Payload["allPages"] != true || guard.Payload["tagPages"] != true || guard.ExpectSchematic == nil || guard.ExpectSchematic.ExactParts || !guard.ExpectSchematic.DesignatorsOnly {
				t.Fatal("first guard must check project-wide uniqueness while allowing other pages' distinct refs")
			}
			for _, slow := range []string{"includePins", "includeBBox", "includeDeviceIdentity", "includeWires", "includeConnectivitySummary"} {
				if _, exists := guard.Payload[slow]; exists {
					t.Fatalf("project-wide designator guard requested unrelated slow field %s", slow)
				}
			}
			for ref, part := range guard.ExpectSchematic.Parts {
				if part.Device != nil || part.BBox != nil || part.Pins != nil || part.Instance != nil || part.X != nil || part.Y != nil || part.Rotation != nil || part.Mirror != nil {
					t.Fatalf("project-wide designator guard retained non-designator state for %s: %+v", ref, part)
				}
			}
			if err := guard.ExpectSchematic.check(env["result"], nil); err != nil {
				t.Fatalf("original complete target inventory did not pass its guard: %v", err)
			}
			r := env["result"].(map[string]any)
			r["components"] = append(r["components"].([]any), map[string]any{"componentType": "part", "designator": "OLD_PAGE_U9", "primitiveId": "off-page-component"})
			if err := guard.ExpectSchematic.check(r, nil); err != nil {
				t.Fatalf("different designator on another page must be allowed: %v", err)
			}
			localID := "verify-source-before-reset"
			if matching {
				localID = "verify-existing-composition"
			}
			_, local := composeStep(t, pb, localID)
			if local.ExpectSchematic == nil || !local.ExpectSchematic.ExactParts {
				t.Fatal("relaxing project scope must not relax the target page's complete part guard")
			}
			if err := local.ExpectSchematic.check(r, nil); err == nil || !strings.Contains(err.Error(), "unexpected part") {
				t.Fatalf("the same additional component on the target page must still be refused: %v", err)
			}
			targetRef := p.Layout.Placements[0].Designator
			r["components"] = append(r["components"].([]any), map[string]any{"componentType": "part", "designator": targetRef, "primitiveId": "off-page-target-collision"})
			if err := guard.ExpectSchematic.check(r, nil); err == nil || !strings.Contains(err.Error(), "duplicate part designator "+targetRef) {
				t.Fatalf("same target designator on another page must block NC/wire attribution: %v", err)
			}
		})
	}
}

func TestComposeApplyEmptyTargetAllowsOtherPageRefsButProtectsNewRefs(t *testing.T) {
	p, env := composeApplyFixture(t, true)
	r := env["result"].(map[string]any)
	r["components"] = []any{r["components"].([]any)[0]} // the measured sheet only
	r["count"], r["wires"] = 1.0, []any{}
	pb, err := schCompositionPlaybook(p, composeApplyBytes(t, env), true)
	if err != nil {
		t.Fatalf("a known empty target page must produce a guarded placement queue: %v", err)
	}
	guard := pb.Steps[0].ExpectSchematic
	if guard == nil || guard.ExactParts || !guard.DesignatorsOnly || len(guard.Parts) != 0 || len(guard.AbsentParts) != len(p.Layout.Placements) {
		t.Fatal("empty target must reserve every soon-to-be-created designator across the project")
	}
	globally := map[string]any{"components": []any{map[string]any{"componentType": "sheet"}, map[string]any{"componentType": "part", "designator": "OTHER_PAGE_R9", "primitiveId": "existing-other-page"}}}
	if err := guard.check(globally, nil); err != nil {
		t.Fatalf("other page's different designators must coexist with an empty target: %v", err)
	}
	for _, planned := range p.Layout.Placements {
		withCollision := map[string]any{"components": append(append([]any{}, globally["components"].([]any)...), map[string]any{"componentType": "part", "designator": planned.Designator, "primitiveId": "already-on-other-page"})}
		if err := guard.check(withCollision, nil); err == nil || !strings.Contains(err.Error(), planned.Designator) {
			t.Fatalf("new designator %s already exists off-page but was not blocked: %v", planned.Designator, err)
		}
	}
}

func TestComposeApplyPartiallyPopulatedTargetChecksExistingAndReservesMissingRefs(t *testing.T) {
	p, env := composeApplyFixture(t, false)
	r := env["result"].(map[string]any)
	var components []any
	for _, entry := range r["components"].([]any) {
		c := entry.(map[string]any)
		if c["componentType"] == "part" && c["designator"] == p.Layout.Placements[1].Designator {
			continue
		}
		components = append(components, entry)
	}
	r["components"], r["count"] = components, len(components)
	pb, err := schCompositionPlaybook(p, composeApplyBytes(t, env), true)
	if err != nil {
		t.Fatal(err)
	}
	guard := pb.Steps[0].ExpectSchematic
	missingRef := p.Layout.Placements[1].Designator
	if guard == nil || len(guard.Parts) != 1 || len(guard.AbsentParts) != 1 || guard.AbsentParts[0] != missingRef {
		t.Fatal("project guard must retain existing part baselines and reserve only missing planned refs")
	}
	if err := guard.check(r, nil); err != nil {
		t.Fatalf("partially populated original target should pass: %v", err)
	}
	r["components"] = append(components, map[string]any{"componentType": "part", "designator": missingRef, "primitiveId": "other-page-collision"})
	if err := guard.check(r, nil); err == nil || !strings.Contains(err.Error(), missingRef) {
		t.Fatalf("planned missing ref occupied on another page passed: %v", err)
	}
}

func composeUnwiredApplyFixture(t *testing.T) (*schCompositionPlan, map[string]any) {
	t.Helper()
	p, env := composeApplyFixture(t, true)
	r := env["result"].(map[string]any)
	var parts []any
	for _, entry := range r["components"].([]any) {
		c := entry.(map[string]any)
		if c["componentType"] != "sheet" && c["componentType"] != "part" {
			continue
		}
		if c["componentType"] == "part" {
			for _, entry := range c["pins"].([]any) {
				pin := entry.(map[string]any)
				pin["net"], pin["noConnected"] = "", false
			}
		}
		parts = append(parts, c)
	}
	r["components"], r["count"], r["wires"] = parts, len(parts), []any{}
	r["connectivitySummary"] = map[string]any{"scope": "activePage", "wires": 0.0, "buses": 0.0, "shortSymbols": 0.0}
	return p, env
}

func TestComposeApplyReusesVerifiedUnwiredPartsAfterNCFailure(t *testing.T) {
	for _, someNCPersisted := range []bool{false, true} {
		t.Run(fmt.Sprintf("some-NC-persisted-%t", someNCPersisted), func(t *testing.T) {
			p, env := composeUnwiredApplyFixture(t)
			r := env["result"].(map[string]any)
			if someNCPersisted {
				r["components"].([]any)[1].(map[string]any)["pins"].([]any)[2].(map[string]any)["noConnected"] = true
			}
			pb, err := schCompositionPlaybook(p, composeApplyBytes(t, env), true)
			if err != nil {
				t.Fatal(err)
			}
			if !pb.RequireFullExecution || pb.Defaults.Retry == nil || *pb.Defaults.Retry != 0 {
				t.Fatal("recovery must replay all guards, never resume past evidence or blindly retry writes")
			}
			baselineIndex, baseline := composeStep(t, pb, "verify-source-before-reset")
			pinIndex, _ := composeStep(t, pb, "verify-physical-pins-before-wiring")
			saveIndex, _ := composeStep(t, pb, "save-placed-parts")
			if baselineIndex >= pinIndex || pinIndex >= saveIndex || baseline.ExpectSchematic == nil || baseline.ExpectSchematic.Drawing == nil {
				t.Fatal("recovery must preserve the complete baseline plus observed empty drawing before the checkpoint save")
			}
			if baseline.Payload["includeConnectivitySummary"] != true || baseline.Assert["$.connectivitySummary.scope"] != "==activePage" || baseline.Assert["$.connectivitySummary.wires"] != "==0" || baseline.Assert["$.connectivitySummary.buses"] != "==0" {
				t.Fatal("unwired recovery must re-read and assert the active page's wire/bus inventory at execution time")
			}
			if err := baseline.ExpectSchematic.check(r, nil); err != nil {
				t.Fatalf("fresh unwired baseline did not pass: %v", err)
			}
			ncCount, wireCount := 0, 0
			for i, s := range pb.Steps {
				if s.Run == "sch clear" || s.Action == "schematic.component.place" || s.Action == "schematic.component.modify" {
					t.Fatalf("verified unwired symbols were unnecessarily recreated: %+v", s)
				}
				if s.Action == "schematic.pin.set_no_connect" {
					ncCount++
					if i <= saveIndex {
						t.Fatal("NC recovery ran before verified placed parts were saved")
					}
				}
				if s.Action == "schematic.wire.create" {
					wireCount++
					if i <= saveIndex {
						t.Fatal("wires ran before recovery geometry/checkpoint gates")
					}
				}
			}
			if ncCount == 0 || wireCount != len(p.Layout.Wires) {
				t.Fatal("recovery omitted pending NC or the complete authored wire drawing")
			}
			// A wire arriving after planning must invalidate the reused baseline,
			// even if all component poses and the stale pin-net read remain equal.
			r["wires"] = []any{drawingWire(0, 0, 10, 0)}
			if err := baseline.ExpectSchematic.check(r, nil); err == nil {
				t.Fatal("concurrent wire creation did not invalidate unwired recovery")
			}
		})
	}
}

func TestComposeApplyCannotReuseUnprovenOrChangedUnwiredState(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"missing-summary", func(r map[string]any) { delete(r, "connectivitySummary") }},
		{"null-summary", func(r map[string]any) { r["connectivitySummary"] = nil }},
		{"missing-wire-count", func(r map[string]any) { delete(r["connectivitySummary"].(map[string]any), "wires") }},
		{"wrong-summary-scope", func(r map[string]any) { r["connectivitySummary"].(map[string]any)["scope"] = "allPages" }},
		{"residual-wire", func(r map[string]any) { r["wires"] = []any{drawingWire(0, 0, 10, 0)} }},
		{"residual-bus", func(r map[string]any) { r["connectivitySummary"].(map[string]any)["buses"] = 1.0 }},
		{"residual-marker", func(r map[string]any) {
			r["components"] = append(r["components"].([]any), map[string]any{"componentType": "netflag", "net": "GND", "x": 0.0, "y": 0.0, "rotation": 0.0})
		}},
		{"wrong-NC-true", func(r map[string]any) {
			r["components"].([]any)[1].(map[string]any)["pins"].([]any)[0].(map[string]any)["noConnected"] = true
		}},
		{"moved-part", func(r map[string]any) {
			c := r["components"].([]any)[1].(map[string]any)
			c["x"] = c["x"].(float64) + 5
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, env := composeUnwiredApplyFixture(t)
			tc.edit(env["result"].(map[string]any))
			pb, err := schCompositionPlaybook(p, composeApplyBytes(t, env), true)
			if err != nil {
				t.Fatalf("explicit --replace should still compile the ordinary guarded rebuild: %v", err)
			}
			composeStep(t, pb, "reset-target-preserving-sheet")
			composeStep(t, pb, "place-000")
		})
	}
}

func TestComposeApplyUnwiredRecoveryStopsOnLiveSummaryChange(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"bus-added-after-plan", func(r map[string]any) { r["connectivitySummary"].(map[string]any)["buses"] = 1.0 }},
		{"wire-count-disagrees-with-empty-geometry", func(r map[string]any) { r["connectivitySummary"].(map[string]any)["wires"] = 1.0 }},
		{"scope-changed-after-plan", func(r map[string]any) { r["connectivitySummary"].(map[string]any)["scope"] = "allPages" }},
		{"summary-unavailable-at-execution", func(r map[string]any) { delete(r, "connectivitySummary") }},
		{"bus-count-unavailable-at-execution", func(r map[string]any) { delete(r["connectivitySummary"].(map[string]any), "buses") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, env := composeUnwiredApplyFixture(t)
			compiled, err := schCompositionPlaybook(p, composeApplyBytes(t, env), true)
			if err != nil {
				t.Fatal(err)
			}
			_, baseline := composeStep(t, compiled, "verify-source-before-reset")
			live := env["result"].(map[string]any)
			// Symbol geometry and the empty wire/marker drawing remain unchanged;
			// only the fresh enumeration reveals the new bus or missing evidence.
			tc.edit(live)
			b, _ := json.Marshal(live)
			cfg, daemon, cleanup := newBlockApplyTestDaemon(t, func(call blockApplyTestCall) string {
				return `{"ok":true,"result":` + string(b) + `}`
			})
			defer cleanup()
			pb := &playbook{Version: 1, Meta: playbookMeta{Name: "unwired recovery summary gate"}, Defaults: compiled.Defaults, Steps: []playbookStep{*baseline, {Action: "schematic.wire.create", Payload: map[string]any{"points": [][2]float64{{0, 0}, {20, 0}}}}}}
			var out bytes.Buffer
			runner := &applyRunner{cfg: cfg, stdout: &out, stderr: &out, pb: pb, vars: map[string]string{}, window: "w1", yes: true, journalPath: filepath.Join(t.TempDir(), "recovery.jsonl"), toIdx: 1}
			if err := runner.execute(); err == nil {
				t.Fatal("new wire/bus or unavailable live summary did not stop recovery")
			}
			calls := daemon.snapshot()
			if len(calls) != 1 || calls[0].Action != "schematic.components.list" || calls[0].Payload["includeConnectivitySummary"] != true {
				t.Fatalf("summary was not requested or a subsequent wire write escaped the gate: %+v", calls)
			}
		})
	}
}
