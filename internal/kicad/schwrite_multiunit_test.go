package kicad

import (
	"strings"
	"testing"
)

// A derived symbol as KiCad's stock libraries ship them (LM358 extends
// LM2904): properties only, the units live in the parent.
var derivedLib = map[string]string{
	"BASE": `(symbol "BASE"
		(pin_names (offset 1.016))
		(exclude_from_sim no) (in_bom yes) (on_board yes) (in_pos_files yes)
		(property "Reference" "U" (at 0 5.08 0) (effects (font (size 1.27 1.27))))
		(property "Value" "BASE" (at 0 -5.08 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "Package:BASE_FP" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Datasheet" "base.pdf" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Description" "base description" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "ki_fp_filters" "BASE*" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Sim.Device" "SUBCKT" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(symbol "BASE_1_1" (pin input line (at -5.08 0 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))
		(symbol "BASE_2_1" (pin input line (at -5.08 0 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27))))))
	)`,
	// MID leaves Footprint/Description empty (inherited), overrides in_bom
	// and pin_names
	"MID": `(symbol "MID" (extends "BASE")
		(pin_names (hide yes))
		(in_bom no)
		(property "Value" "MID" (at 0 -6 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Description" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "ki_keywords" "mid" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27)))))`,
	// TOP: empty keywords/filters/datasheet inherit; on_board, exclude_from_sim,
	// in_pos_files override; pin_numbers is new (BASE has none)
	"TOP": `(symbol "TOP" (extends "MID")
		(pin_numbers (hide yes))
		(exclude_from_sim yes) (on_board no) (in_pos_files no)
		(property "Reference" "IC" (at 0 7 0) (effects (font (size 1.27 1.27))))
		(property "Value" "TOP" (at 0 -7 0) (effects (font (size 1.27 1.27))))
		(property "Datasheet" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "ki_keywords" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "ki_fp_filters" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "MPN" "TOP-1" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27)))))`,
	"LOOP": `(symbol "LOOP" (extends "LOOP"))`,
}

func TestFlattenLibSymbol(t *testing.T) {
	text, err := FlattenLibSymbol(derivedLib, "TOP")
	if err != nil {
		t.Fatal(err)
	}
	n, err := parseSx(text)
	if err != nil {
		t.Fatal(err)
	}
	if n.list[1].atom != "TOP" || n.child("extends") != nil {
		t.Fatalf("not flattened: %s", text)
	}
	for k, want := range map[string]string{"Reference": "IC", "Value": "TOP", "Footprint": "Package:BASE_FP",
		"Datasheet": "base.pdf", "Description": "base description", "ki_keywords": "mid", "ki_fp_filters": "BASE*",
		"Sim.Device": "SUBCKT", "MPN": "TOP-1"} {
		if got := propVal(n, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// every attribute once, with the nearest override
	for h, want := range map[string]string{"pin_names": "(pin_names (hide yes))", "pin_numbers": "(pin_numbers (hide yes))",
		"exclude_from_sim": "(exclude_from_sim yes)", "in_bom": "(in_bom no)", "on_board": "(on_board no)", "in_pos_files": "(in_pos_files no)"} {
		var got []string
		for _, c := range n.list {
			if c.head() == h {
				got = append(got, text[c.beg:c.end])
			}
		}
		if len(got) != 1 || got[0] != want {
			t.Errorf("%s: %q, want [%s]", h, got, want)
		}
	}
	if c := strings.Count(text, `(property "Footprint"`); c != 1 {
		t.Errorf("Footprint property ×%d", c)
	}
	if !strings.Contains(text, `(symbol "TOP_1_1"`) || !strings.Contains(text, `(symbol "TOP_2_1"`) || strings.Contains(text, `(symbol "BASE_`) {
		t.Errorf("unit sub-symbols not renamed: %s", text)
	}
	pins, err := LibSymbolPins(text)
	if err != nil || len(pins) != 2 || pins[1].Unit != 2 {
		t.Fatalf("pins %+v %v", pins, err)
	}
	if _, err := FlattenLibSymbol(derivedLib, "LOOP"); err == nil {
		t.Error("extends loop accepted")
	}
	if got, _ := FlattenLibSymbol(derivedLib, "BASE"); got != derivedLib["BASE"] {
		t.Error("a plain symbol changed")
	}
}

// De Morgan body style 2 draws the same pins elsewhere; only style 1 counts.
func TestLibPinsSkipsDeMorgan(t *testing.T) {
	sym := `(symbol "G"
		(symbol "G_1_1" (pin input line (at -5.08 0 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))
		(symbol "G_1_2" (pin input line (at -7.62 2 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))
	)`
	pins, err := LibSymbolPins(sym)
	if err != nil || len(pins) != 1 || pins[0].At.X != -5.08 {
		t.Fatalf("%+v %v", pins, err)
	}
}

func multiUnitSheet(t *testing.T) *SchEditor {
	t.Helper()
	e, err := OpenSchematic(NewSchematicText("A4", NewUUID(), true))
	if err != nil {
		t.Fatal(err)
	}
	e.Project = "t"
	if err := e.AddLibSymbol("my:DUAL", dualSym); err != nil {
		t.Fatal(err)
	}
	for u, at := range map[int]Pt{1: {50.8, 50.8}, 2: {50.8, 76.2}} {
		if _, err := e.PlaceSymbol(SymbolInstance{LibID: "my:DUAL", Ref: "U1", Unit: u, Value: "DUAL", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	text, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	e2, err := OpenSchematic(text)
	if err != nil {
		t.Fatal(err)
	}
	return e2
}

func TestSchEditorMultiUnitAddressing(t *testing.T) {
	e := multiUnitSheet(t)
	if n, _ := e.LibUnitCount("my:DUAL"); n != 2 {
		t.Errorf("LibUnitCount = %d", n)
	}
	if u := e.SymbolUnits("U1"); len(u) != 2 || u[0] != 1 || u[1] != 2 {
		t.Errorf("SymbolUnits = %v", u)
	}
	all, err := e.SymbolPinPositions("U1")
	if err != nil || len(all) != 4 {
		t.Fatalf("pins of every unit: %v %v", all, err)
	}
	if all["3"] != (Pt{45.72, 76.2}) {
		t.Errorf("unit 2 pin 3 at %v", all["3"])
	}
	u2, _ := e.SymbolPinPositions("U1:2")
	if len(u2) != 2 || u2["3"] != all["3"] {
		t.Errorf("U1:2 pins %v", u2)
	}
	if _, _, _, err := e.SymbolAt("U1"); err == nil || !strings.Contains(err.Error(), "U1:1, U1:2") {
		t.Errorf("ambiguous SymbolAt: %v", err)
	}
	if _, _, _, err := e.SymbolAt("U1:3"); err == nil {
		t.Error("unit 3 found")
	}
	if err := e.MoveSymbol("U1", Pt{100, 100}, 0); err == nil {
		t.Error("ambiguous move accepted")
	}
	if err := e.MoveSymbol("U1:2", Pt{101.6, 76.2}, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.SetField("U1:1", "Value", "X"); err == nil {
		t.Error("per-unit field accepted")
	}
	for _, f := range [][2]string{{"Value", "LM358"}, {"LCSC", "C7950"}, {"Reference", "U7"}} {
		if err := e.SetField("U1", f[0], f[1]); err != nil {
			t.Fatal(err)
		}
	}
	text, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	for s, n := range map[string]int{`(property "Value" "LM358"`: 2, `(property "LCSC" "C7950"`: 2, `(reference "U7")`: 2, `(property "Reference" "U7"`: 2, `"U1"`: 0} {
		if c := strings.Count(text, s); c != n {
			t.Errorf("%s ×%d, want %d", s, c, n)
		}
	}
	e2, _ := OpenSchematic(text)
	if x, y, _, _ := e2.SymbolAt("U7:2"); x != 101.6 || y != 76.2 {
		t.Errorf("U7:2 at %v,%v", x, y)
	}
	if x, _, _, _ := e2.SymbolAt("U7:1"); x != 50.8 {
		t.Errorf("unit 1 moved to x=%v", x)
	}
}

func TestSchEditorDeleteUnits(t *testing.T) {
	e := multiUnitSheet(t)
	if u, err := e.DeleteSymbol("U1:2"); err != nil || len(u) != 1 || u[0] != 2 {
		t.Fatalf("%v %v", u, err)
	}
	text, _ := e.Render()
	e2, _ := OpenSchematic(text)
	if u := e2.SymbolUnits("U1"); len(u) != 1 || u[0] != 1 {
		t.Fatalf("after U1:2: %v", u)
	}
	// a bare reference removes every unit, pending ones too
	if _, err := e2.PlaceSymbol(SymbolInstance{LibID: "my:DUAL", Ref: "U1", Unit: 2, Value: "DUAL", At: Pt{50.8, 101.6}}); err != nil {
		t.Fatal(err)
	}
	if u, err := e2.DeleteSymbol("U1"); err != nil || len(u) != 2 {
		t.Fatalf("%v %v", u, err)
	}
	text, _ = e2.Render()
	if strings.Contains(text, `(lib_id "my:DUAL")`) {
		t.Error("a unit survived")
	}
	e3, _ := OpenSchematic(text)
	if _, err := e3.DeleteSymbol("U1"); err == nil {
		t.Error("deleting a missing symbol succeeded")
	}
}
