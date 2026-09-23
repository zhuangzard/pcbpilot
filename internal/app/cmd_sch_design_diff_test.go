package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

func designDiffPlan(t *testing.T) schCompositionPlan {
	t.Helper()
	p, err := planSchComposition(composeFixture(2))
	if err != nil {
		t.Fatal(err)
	}
	return *p
}
func designDiffBytes(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func designDiffInput(t *testing.T, value any) schDesignInput {
	t.Helper()
	p, err := decodeSchDesignInput(designDiffBytes(t, value))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func TestSchDesignDiffCompletePlanDrawingChanges(t *testing.T) {
	tests := []struct {
		name, contains string
		mutate         func(*schCompositionPlan)
	}{
		{"wire direction", "wires", func(p *schCompositionPlan) {
			w := &p.Layout.Wires[0]
			a, b := w.Points[0], w.Points[1]
			w.Points = [][2]float64{a, {a[0], a[1] + 5}, {b[0], b[1] + 5}, b}
		}},
		{"flag direction", "flags", func(p *schCompositionPlan) { p.Layout.Flags[0].Direction = "down" }},
		{"flag offset", "flags", func(p *schCompositionPlan) { p.Layout.Flags[0].Offset += 5 }},
		{"solid frame", "lineType", func(p *schCompositionPlan) { p.Layout.Frames[0].LineType = 0 }},
		{"dotted frame", "lineType", func(p *schCompositionPlan) { p.Layout.Frames[0].LineType = 2 }},
		{"dot dashed frame", "lineType", func(p *schCompositionPlan) { p.Layout.Frames[0].LineType = 3 }},
		{"angled wire", "wires", func(p *schCompositionPlan) { p.Layout.Wires[0].Points[1][1] += 5 }},
		{"arbitrary rotation", "rotation", func(p *schCompositionPlan) {
			p.Layout.Placements[0].Rotation = 45
			ref := p.Layout.Placements[0].Designator
			for i := range p.Connectivity.Components {
				if p.Connectivity.Components[i].Ref == ref {
					p.Connectivity.Components[i].Placement.Rotation = 45
				}
			}
		}},
		{"frame title", "title", func(p *schCompositionPlan) { p.Layout.Frames[0].Title = "SUPPLY" }},
		{"frame rectangle", "rect", func(p *schCompositionPlan) { p.Layout.Frames[0].Rect.MinX -= 1 }},
		{"title text position", "titleX", func(p *schCompositionPlan) { p.Layout.Frames[0].TitleX += 1 }},
		{"font size", "fontSize", func(p *schCompositionPlan) { p.Layout.Frames[0].FontSize += 1 }},
		{"font color", "color", func(p *schCompositionPlan) { p.Layout.Frames[0].Color = "#FF00FF" }},
		{"sheet", "sheet", func(p *schCompositionPlan) { p.Sheet.MaxX += 10 }},
		{"keepout", "keepouts", func(p *schCompositionPlan) {
			p.Keepouts = append(p.Keepouts, layoutBBox{MinX: 1, MinY: 1, MaxX: 2, MaxY: 2})
		}},
		{"expected nets", "expectedPinNets", func(p *schCompositionPlan) { p.Layout.ExpectedPinNets["U1.1"] = "CHANGED" }},
		{"value", "value", func(p *schCompositionPlan) { p.Layout.Placements[0].Value = "new value" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := designDiffPlan(t)
			var b schCompositionPlan
			_ = json.Unmarshal(designDiffBytes(t, a), &b)
			tc.mutate(&b)
			diff, err := compareSchDesignInputs(designDiffInput(t, a), designDiffInput(t, b))
			if err != nil {
				t.Fatal(err)
			}
			if diff.Status != "different" || !diff.Coverage.DrawingCompared || diff.Coverage.Scope != "local-compose-plan" || diff.ExpectedRevision == diff.ActualRevision {
				t.Fatalf("drawing difference missed %+v", diff)
			}
			found := false
			for _, c := range diff.Changes {
				if strings.HasPrefix(c.Path, "/drawing/") && strings.Contains(c.Path, tc.contains) {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing %s: %+v", tc.contains, diff.Changes)
			}
		})
	}
}

func TestSchDesignDiffPlanSetsAndRuntimeIDsDoNotCauseChurn(t *testing.T) {
	a := designDiffPlan(t)
	var b schCompositionPlan
	_ = json.Unmarshal(designDiffBytes(t, a), &b)
	b.Layout.Placements[0], b.Layout.Placements[1] = b.Layout.Placements[1], b.Layout.Placements[0]
	for i := range b.Layout.Placements {
		c := &b.Layout.Placements[i]
		c.PrimitiveID = "different-session-id"
		c.Pins[0], c.Pins[1] = c.Pins[1], c.Pins[0]
	}
	b.Layout.Wires[0], b.Layout.Wires[1] = b.Layout.Wires[1], b.Layout.Wires[0]
	b.Layout.Flags[0], b.Layout.Flags[1] = b.Layout.Flags[1], b.Layout.Flags[0]
	diff, err := compareSchDesignInputs(designDiffInput(t, a), designDiffInput(t, b))
	if err != nil || diff.Status != "synced" || diff.ExpectedRevision != diff.ActualRevision {
		t.Fatalf("runtime/order churn %+v %v", diff, err)
	}
	if len(diff.Coverage.Unverified) == 0 || diff.Coverage.Unverified[0].Path != "/editor" {
		t.Fatal("local sync must not claim actual EDA rendering")
	}
	b.Layout.Frames[0], b.Layout.Frames[1] = b.Layout.Frames[1], b.Layout.Frames[0]
	diff, err = compareSchDesignInputs(designDiffInput(t, a), designDiffInput(t, b))
	if err != nil || diff.Status != "different" {
		t.Fatalf("frame order not retained %+v %v", diff, err)
	}
}

func TestSchDesignDiffEquivalentWireTraversalSignedZeroAndMissingRuntimeID(t *testing.T) {
	a := designDiffPlan(t)
	var b schCompositionPlan
	_ = json.Unmarshal(designDiffBytes(t, a), &b)
	for i := range b.Layout.Wires {
		points := b.Layout.Wires[i].Points
		for left, right := 0, len(points)-1; left < right; left, right = left+1, right-1 {
			points[left], points[right] = points[right], points[left]
		}
	}
	diff, err := compareSchDesignInputs(designDiffInput(t, a), designDiffInput(t, b))
	if err != nil || diff.Status != "synced" || diff.ExpectedRevision != diff.ActualRevision {
		t.Fatalf("reversed traversal is same shape %+v %v", diff, err)
	}
	raw := string(designDiffBytes(t, a))
	negativeZero := strings.Replace(raw, `"rotation":0`, `"rotation":-0`, 1)
	if negativeZero == raw {
		t.Fatal("signed-zero mutation did not apply")
	}
	negative, err := decodeSchDesignInput([]byte(negativeZero))
	if err != nil {
		t.Fatal(err)
	}
	diff, err = compareSchDesignInputs(designDiffInput(t, a), negative)
	if err != nil || diff.Status != "synced" || diff.ExpectedRevision != diff.ActualRevision {
		t.Fatalf("signed zero must have equal content hash %+v %v", diff, err)
	}
	var runtimeFree map[string]any
	_ = json.Unmarshal([]byte(raw), &runtimeFree)
	placements := runtimeFree["layout"].(map[string]any)["placements"].([]any)
	for _, item := range placements {
		delete(item.(map[string]any), "primitiveId")
	}
	diff, err = compareSchDesignInputs(designDiffInput(t, a), designDiffInput(t, runtimeFree))
	if err != nil || diff.Status != "synced" {
		t.Fatalf("omitted ephemeral IDs made plan incomplete %+v %v", diff, err)
	}
	// Sorting must retain duplicate wires, which are a real inventory change.
	b = a
	b.Layout.Wires = append(append([]powerLayoutWire{}, a.Layout.Wires...), a.Layout.Wires[0])
	diff, err = compareSchDesignInputs(designDiffInput(t, a), designDiffInput(t, b))
	if err != nil || diff.Status != "different" {
		t.Fatalf("duplicate wire was silently discarded %+v %v", diff, err)
	}
}

func TestSchDesignDiffMixedInputsAreExplicitlyCanonicalOnly(t *testing.T) {
	p := designDiffPlan(t)
	a := designDiffInput(t, p)
	b := designDiffInput(t, p.Connectivity)
	diff, err := compareSchDesignInputs(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Status != "synced" || diff.Coverage.Scope != "canonical" || diff.Coverage.DrawingCompared || !strings.Contains(diff.Coverage.Unverified[0].Reason, "only shared canonical") {
		t.Fatalf("mixed input coverage incorrect %+v", diff)
	}
	source := composeFixture(1)
	envelope := map[string]any{"connectivity": source.Connectivity}
	for _, v := range []any{source, envelope} {
		in := designDiffInput(t, v)
		d, err := compareSchDesignInputs(in, in)
		if err != nil || d.Status != "incomplete" || d.Coverage.DrawingCompared {
			t.Fatalf("absent canonical geometry must remain incomplete %+v %v", d, err)
		}
	}
}

func TestSchDesignDiffRejectsUnknownAliasMissingAndInvalidDrawing(t *testing.T) {
	raw := string(designDiffBytes(t, designDiffPlan(t)))
	tests := map[string]string{
		"unknown drawing":   strings.Replace(raw, `"wires":`, `"wrires":`, 1),
		"alias drawing":     strings.Replace(raw, `"sheet":`, `"Sheet":`, 1),
		"duplicate drawing": strings.Replace(raw, `"rows":1`, `"rows":1,"rows":2`, 1),
		"tail":              raw + ` {}`,
		"missing sheet":     strings.Replace(raw, `"sheet":`, `"ignoredSheet":`, 1),
		"bad flag enum":     strings.Replace(raw, `"direction":"up"`, `"direction":"diagonal"`, 1),
		"bad line enum":     strings.Replace(raw, `"lineType":1`, `"lineType":4`, 1),
		"null scalar":       strings.Replace(raw, `"rowHeight":`, `"rowHeight":null,"duplicateRowHeight":`, 1),
		"wrong point arity": strings.Replace(raw, `"points":[[`, `"points":[[1,`, 1),
		"unsupported plan":  strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":2`, 1),
	}
	for name, b := range tests {
		t.Run(name, func(t *testing.T) {
			if b == raw {
				t.Fatal("negative fixture mutation did not apply")
			}
			if _, err := decodeSchDesignInput([]byte(b)); err == nil {
				t.Fatal("expected strict decode error")
			}
		})
	}
	var obj map[string]any
	_ = json.Unmarshal([]byte(raw), &obj)
	delete(obj, "rowHeight")
	_, err := decodeSchDesignInput(designDiffBytes(t, obj))
	var missing *connectivity.IncompleteDesignError
	if !errors.As(err, &missing) {
		t.Fatalf("missing numeric field should be incomplete: %v", err)
	}
	p := designDiffPlan(t)
	p.Connectivity.Components[0].Placement.X += 5
	if _, err := decodeSchDesignInput(designDiffBytes(t, p)); err == nil {
		t.Fatal("contradictory canonical/drawing coordinates accepted")
	}
	p = designDiffPlan(t)
	p.Layout.Placements[0].Pins[0].Number = "unknown"
	if _, err := decodeSchDesignInput(designDiffBytes(t, p)); err == nil {
		t.Fatal("different drawing pin identity accepted")
	}
}

func TestSchDesignDiffExplicitBorderAndLegacyDiagnostics(t *testing.T) {
	a := designDiffPlan(t)
	var b schCompositionPlan
	_ = json.Unmarshal(designDiffBytes(t, a), &b)
	border := a.Sheet
	border.MinX += 5
	border.MaxY -= 5
	b.SheetBorder = &border
	diff, err := compareSchDesignInputs(designDiffInput(t, a), designDiffInput(t, b))
	if err != nil || diff.Status != "different" {
		t.Fatalf("border change missed %+v %v", diff, err)
	}
	found := false
	for _, change := range diff.Changes {
		if change.Path == "/drawing/sheetBorder" {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit sheetBorder not covered")
	}
	var legacy map[string]any
	_ = json.Unmarshal(designDiffBytes(t, a), &legacy)
	for _, key := range []string{"rowHeights", "usableBounds", "placementBoundarySource"} {
		delete(legacy, key)
	}
	old := designDiffInput(t, legacy)
	same, err := compareSchDesignInputs(old, old)
	if err != nil || same.Status != "synced" || len(same.Coverage.Unverified) != 7 {
		t.Fatalf("legacy diagnostics synthesized or hidden %+v %v", same, err)
	}
	diff, err = compareSchDesignInputs(old, designDiffInput(t, a))
	if err != nil || diff.Status != "different" {
		t.Fatalf("new planning metadata should differ %+v %v", diff, err)
	}
}

func TestSchDesignDiffCLIExitCodes(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a.json")
	b := filepath.Join(tmp, "b.json")
	plan := designDiffPlan(t)
	_ = os.WriteFile(a, designDiffBytes(t, plan.Connectivity), 0600)
	run := func(args ...string) (connectivity.DesignDiff, error) {
		var out, stderr bytes.Buffer
		cmd := newSchDesignDiffCmd(&out, &stderr)
		cmd.SetArgs(args)
		err := cmd.Execute()
		var diff connectivity.DesignDiff
		if out.Len() > 0 {
			if decodeErr := json.Unmarshal(out.Bytes(), &diff); decodeErr != nil {
				t.Fatalf("bad JSON %s %v", out.String(), decodeErr)
			}
		}
		return diff, err
	}
	diff, err := run(a, a, "--exit-code")
	if err != nil || diff.Status != "synced" {
		t.Fatalf("same %v %+v", err, diff)
	}
	changed := plan.Connectivity
	changed.Components[0].Ref = "U001"
	_ = os.WriteFile(b, designDiffBytes(t, changed), 0600)
	diff, err = run(a, b, "--exit-code")
	var exit exitCodeError
	if !errors.As(err, &exit) || exit.code != 2 || diff.Status != "different" {
		t.Fatalf("difference exit %+v %v", diff, err)
	}
	if _, err = run(a, b); err != nil {
		t.Fatalf("--exit-code optional: %v", err)
	}
	changed.ProjectID = "other"
	_ = os.WriteFile(b, designDiffBytes(t, changed), 0600)
	diff, err = run(a, b)
	if !errors.As(err, &exit) || exit.code != 3 || diff.Status != "wrong-target" {
		t.Fatalf("wrong target %+v %v", diff, err)
	}
	changed = composeFixture(1).Connectivity
	_ = os.WriteFile(b, designDiffBytes(t, changed), 0600)
	diff, err = run(b, b)
	if !errors.As(err, &exit) || exit.code != 3 || diff.Status != "incomplete" {
		t.Fatalf("missing evidence %+v %v", diff, err)
	}
}
