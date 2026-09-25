package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F1 (2026-09-25 E2E): layout-sheet-plan needs the inner border, but no typed
// getter exposed it; the agent guessed. The border now comes from the sheet
// symbol's own attributes (titleblock.get titleBlockData).

// a4TitleBlockData mirrors the live 3.2.149 A4 sheet attributes (ceshi P2).
func a4TitleBlockData() map[string]any {
	v := func(s string) map[string]any { return map[string]any{"value": s} }
	return map[string]any{
		"Border": v("1"), "Blade Width": v("10"), "Width": v("1170"), "Height": v("825"),
		"Region Start": v("1"), "X Region Count": v("6"), "Y Region Count": v("4"),
		"Title Block Position": v("3"), "Name": v(""), "@Board Name": v("Board1"),
	}
}

func TestDeriveSheetBorderFromSymbolAttributes(t *testing.T) {
	sheet := &layoutBBox{0, 0, 1170, 825}
	b, warns := deriveSheetBorder(sheet, a4TitleBlockData())
	if b.BBox == nil || *b.BBox != (layoutBBox{10, 10, 1160, 815}) {
		t.Fatalf("A4 border = %+v (warns %v)", b.BBox, warns)
	}
	if b.Source != sheetBorderSourceAttributes || b.Status != sheetBorderStatusSourceOnly {
		t.Fatalf("provenance = %s/%s", b.Source, b.Status)
	}
	if b.Attributes["Blade Width"] != "10" || len(warns) != 0 {
		t.Fatalf("attributes/warnings: %+v %v", b.Attributes, warns)
	}
	// Offset sheet: inset follows the live bbox.
	b, _ = deriveSheetBorder(&layoutBBox{100, 50, 1270, 875}, a4TitleBlockData())
	if *b.BBox != (layoutBBox{110, 60, 1260, 865}) {
		t.Fatalf("offset border = %+v", b.BBox)
	}
}

func TestDeriveSheetBorderRefusesToGuess(t *testing.T) {
	sheet := &layoutBBox{0, 0, 1170, 825}
	hidden := a4TitleBlockData()
	hidden["Border"] = map[string]any{"value": "0"}
	noBlade := a4TitleBlockData()
	delete(noBlade, "Blade Width")
	junk := a4TitleBlockData()
	junk["Blade Width"] = map[string]any{"value": "wide"}
	huge := a4TitleBlockData()
	huge["Blade Width"] = map[string]any{"value": "500"}
	for name, tc := range map[string]struct {
		sheet *layoutBBox
		data  map[string]any
	}{
		"hidden frame": {sheet, hidden}, "no blade": {sheet, noBlade}, "junk blade": {sheet, junk},
		"blade too wide": {sheet, huge}, "no data": {sheet, nil}, "no sheet": {nil, a4TitleBlockData()},
	} {
		b, warns := deriveSheetBorder(tc.sheet, tc.data)
		if b.BBox != nil || b.Source != sheetSourceNone || len(warns) == 0 {
			t.Errorf("%s: must not derive a border: %+v %v", name, b, warns)
		}
	}
	// Symbol size disagreeing with the live bbox is reported, live bbox wins.
	b, warns := deriveSheetBorder(&layoutBBox{0, 0, 1654, 1170}, a4TitleBlockData())
	if b.BBox == nil || b.BBox.MaxX != 1644 || len(warns) != 2 {
		t.Fatalf("size mismatch: %+v %v", b.BBox, warns)
	}
}

func writeSheetPlanInput(t *testing.T, dir string, dropBorder bool) (string, SchematicRenderInput) {
	t.Helper()
	in := sheetFixture(t)
	raw, _ := json.Marshal(in)
	if dropBorder {
		var top map[string]json.RawMessage
		json.Unmarshal(raw, &top)
		var sheet map[string]json.RawMessage
		json.Unmarshal(top["sheet"], &sheet)
		delete(sheet, "border")
		top["sheet"], _ = json.Marshal(sheet)
		raw, _ = json.Marshal(top)
	}
	from := filepath.Join(dir, "in.json")
	if err := os.WriteFile(from, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return from, in
}

func writeSheetGeometry(t *testing.T, dir string, bounds layoutBBox, data map[string]any) string {
	t.Helper()
	g := deriveSheetGeometry(&bounds, nil)
	b, _ := deriveSheetBorder(&bounds, data)
	g.Border = &b
	raw, _ := json.Marshal(map[string]any{"ok": true, "result": g}) // enveloped, as --json emits
	path := filepath.Join(dir, "geom.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSheetPlanTakesBorderFromSheetGeometry(t *testing.T) {
	dir := t.TempDir()
	from, in := writeSheetPlanInput(t, dir, true)
	geom := writeSheetGeometry(t, dir, in.Sheet.Bounds, a4TitleBlockData())
	out := filepath.Join(dir, "pages.json")
	// Without a border the plan is refused, exactly as in the E2E.
	c := newSchLayoutSheetPlanCmd(&bytes.Buffer{})
	c.SetArgs([]string{"--from", from, "--out", out})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "border") {
		t.Fatalf("missing border must be refused: %v", err)
	}
	c = newSchLayoutSheetPlanCmd(&bytes.Buffer{})
	c.SetArgs([]string{"--from", from, "--out", out, "--sheet-geometry", geom})
	if err := c.Execute(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	var plan SchematicSheetsPreview
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	s := plan.Pages[0].Sheet
	want := layoutBBox{in.Sheet.Bounds.MinX + 10, in.Sheet.Bounds.MinY + 10, in.Sheet.Bounds.MaxX - 10, in.Sheet.Bounds.MaxY - 10}
	if s.Border != want || !strings.Contains(s.BorderSource, sheetBorderSourceAttributes) {
		t.Fatalf("border %+v source %q", s.Border, s.BorderSource)
	}
}

func TestSheetPlanRejectsConflictingOrMissingGeometryBorder(t *testing.T) {
	dir := t.TempDir()
	from, in := writeSheetPlanInput(t, dir, false) // hand-written border = bounds (≠ inset)
	geom := writeSheetGeometry(t, dir, in.Sheet.Bounds, a4TitleBlockData())
	c := newSchLayoutSheetPlanCmd(&bytes.Buffer{})
	c.SetArgs([]string{"--from", from, "--out", filepath.Join(dir, "o.json"), "--sheet-geometry", geom})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting border must be rejected: %v", err)
	}
	hidden := a4TitleBlockData()
	hidden["Border"] = map[string]any{"value": "0"}
	from, in = writeSheetPlanInput(t, dir, true)
	geom = writeSheetGeometry(t, dir, in.Sheet.Bounds, hidden)
	c = newSchLayoutSheetPlanCmd(&bytes.Buffer{})
	c.SetArgs([]string{"--from", from, "--out", filepath.Join(dir, "o.json"), "--sheet-geometry", geom})
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "no derived border") {
		t.Fatalf("geometry without a border must be rejected: %v", err)
	}
}
