package kicad

import (
	"strings"
	"testing"
)

func testSymbol(pins ...SymbolPin) string {
	spec := SymbolSpec{
		Name: "TestSym",
		Pins: pins,
	}
	text, err := AuthorSymbol(spec)
	if err != nil {
		panic(err)
	}
	return text
}

func testFootprint(pads ...PadSpec) string {
	spec := FootprintSpec{
		Name: "TestFP",
		Pads: pads,
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		panic(err)
	}
	return text
}

func smdPad(num string, x float64) PadSpec {
	return PadSpec{
		Number: num, Type: "smd", Shape: "rect",
		At: [3]float64{x, 0, 0}, Size: [2]float64{1, 1},
		Layers: []string{"F.Cu", "F.Paste", "F.Mask"},
	}
}

func TestCheckPinPad_Match(t *testing.T) {
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "A", Type: "passive", Side: "left"},
		SymbolPin{Number: "2", Name: "B", Type: "passive", Side: "right"},
	)
	fp := testFootprint(smdPad("1", -1), smdPad("2", 1))

	result, err := CheckPinPad(sym, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("expected OK, got errors: %v", result.Errors)
	}
	if result.MatchedCount != 2 {
		t.Errorf("expected 2 matches, got %d", result.MatchedCount)
	}
}

func TestCheckPinPad_UnmatchedPin(t *testing.T) {
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "A", Type: "passive", Side: "left"},
		SymbolPin{Number: "2", Name: "B", Type: "passive", Side: "right"},
		SymbolPin{Number: "3", Name: "C", Type: "passive", Side: "left"},
	)
	fp := testFootprint(smdPad("1", -1), smdPad("2", 1))

	result, err := CheckPinPad(sym, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Error("expected not OK")
	}
	if len(result.UnmatchedPins) != 1 || result.UnmatchedPins[0] != "3" {
		t.Errorf("expected unmatched pin 3, got %v", result.UnmatchedPins)
	}
}

func TestCheckPinPad_UnmatchedPad(t *testing.T) {
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "A", Type: "passive", Side: "left"},
	)
	fp := testFootprint(smdPad("1", -1), smdPad("2", 1))

	result, err := CheckPinPad(sym, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Error("expected not OK")
	}
	if len(result.UnmatchedPads) != 1 || result.UnmatchedPads[0] != "2" {
		t.Errorf("expected unmatched pad 2, got %v", result.UnmatchedPads)
	}
}

func TestCheckPinPad_ExcludePad(t *testing.T) {
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "A", Type: "passive", Side: "left"},
	)
	// Footprint has pad 1 + an NPTH pad "MH" (mechanical).
	fp := testFootprint(
		smdPad("1", -1),
		PadSpec{Number: "MH", Type: "np_thru_hole", Shape: "circle",
			At: [3]float64{5, 5, 0}, Size: [2]float64{3, 3},
			Drill: &DrillSpec{Diameter: 2.2}, Layers: []string{"*.Cu", "*.Mask"}},
	)

	result, err := CheckPinPad(sym, fp, []string{"MH"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("expected OK with excluded MH pad, got errors: %v", result.Errors)
	}
}

func TestCheckPinPad_BlankPadExcluded(t *testing.T) {
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "A", Type: "passive", Side: "left"},
	)
	// Footprint has a blank-number NPTH.
	fpText := `(footprint "Test" (pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu")) (pad "" np_thru_hole circle (at 5 5) (size 3 3) (drill 2.2) (layers "*.Cu")))`

	result, err := CheckPinPad(sym, fpText, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("expected OK (blank pad excluded), got errors: %v", result.Errors)
	}
}

func TestCheckPinPad_DuplicatePadNumbers(t *testing.T) {
	// Symbol has 2 pins, footprint has 3 pads (GND split into two).
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "IN", Type: "input", Side: "left"},
		SymbolPin{Number: "2", Name: "GND", Type: "power_in", Side: "right"},
	)
	fp := testFootprint(smdPad("1", -1), smdPad("2", 1), smdPad("2", 2))

	result, err := CheckPinPad(sym, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("expected OK (duplicate pads match same pin), got errors: %v", result.Errors)
	}
}

func TestCheckPinPad_NCPinStillMatched(t *testing.T) {
	// NC pins must still have footprint pads (NC = don't connect, not "no pad").
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "NC", Type: "no_connect", Side: "left"},
	)
	fp := testFootprint(smdPad("1", 0))

	result, err := CheckPinPad(sym, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Error("NC pin with matching pad should be OK")
	}
}

func TestCheckPinPad_NCPinNoMatchReported(t *testing.T) {
	// NC pin without pad should be reported as unmatched.
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "NC", Type: "no_connect", Side: "left"},
		SymbolPin{Number: "2", Name: "A", Type: "passive", Side: "right"},
	)
	fp := testFootprint(smdPad("2", 0))

	result, err := CheckPinPad(sym, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK {
		t.Error("expected not OK (NC pin 1 has no pad)")
	}
	if len(result.UnmatchedPins) != 1 || result.UnmatchedPins[0] != "1" {
		t.Errorf("expected unmatched pin 1, got %v", result.UnmatchedPins)
	}
}

func TestCheckPinPad_BadSymbol(t *testing.T) {
	// A bare atom is a valid S-expression but yields zero pins. The check
	// should report pad "1" as unmatched (not OK).
	result, err := CheckPinPad("not_a_symbol", `(footprint "X" (pad "1" smd rect (at 0 0) (size 1 1)))`, nil)
	if err != nil {
		return // error is acceptable too
	}
	if result.OK {
		t.Error("expected not OK for nonsense symbol")
	}
	if result.MatchedCount != 0 {
		t.Error("expected zero matches for nonsense symbol")
	}
}

func TestCheckPinPad_BothEmpty(t *testing.T) {
	// Both empty should NOT be OK — rejects malformed/wrong inputs.
	result, err := CheckPinPad("bare_atom", "bare_atom", nil)
	if err != nil {
		return // error is acceptable
	}
	if result.OK {
		t.Error("expected not OK when both sides are empty")
	}
}

func TestCheckPinPad_ErrorMessages(t *testing.T) {
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "A", Type: "passive", Side: "left"},
		SymbolPin{Number: "2", Name: "B", Type: "passive", Side: "right"},
	)
	fp := testFootprint(smdPad("1", -1), smdPad("3", 1))

	result, err := CheckPinPad(sym, fp, nil)
	if err != nil {
		t.Fatal(err)
	}

	hasPin := false
	hasPad := false
	for _, e := range result.Errors {
		if strings.Contains(e, "pin") {
			hasPin = true
		}
		if strings.Contains(e, "pad") {
			hasPad = true
		}
	}
	if !hasPin || !hasPad {
		t.Errorf("expected both pin and pad error messages, got: %v", result.Errors)
	}
}

// Finding #7: np_thru_hole pads auto-excluded without explicit --exclude-pad.
func TestCheckPinPad_NPTHAutoExcluded(t *testing.T) {
	sym := testSymbol(
		SymbolPin{Number: "1", Name: "A", Type: "passive", Side: "left"},
	)
	// Footprint has pad 1 + a numbered NPTH "MH" (mechanical hole).
	fp := testFootprint(
		smdPad("1", -1),
		PadSpec{Number: "MH", Type: "np_thru_hole", Shape: "circle",
			At: [3]float64{5, 5, 0}, Size: [2]float64{3, 3},
			Drill: &DrillSpec{Diameter: 2.2}, Layers: []string{"*.Cu", "*.Mask"}},
	)

	// Without explicit exclusion, np_thru_hole pads should be auto-excluded.
	result, err := CheckPinPad(sym, fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("expected OK with auto-excluded NPTH pad, got errors: %v", result.Errors)
	}
}

// Finding #6: multi-symbol library with --symbol-name CLI regression.
func TestCheckPinPad_MultiSymbolLib(t *testing.T) {
	// Build a two-symbol library.
	sym1, _ := AuthorSymbol(SymbolSpec{
		Name: "SymA",
		Pins: []SymbolPin{
			{Number: "1", Name: "A1", Type: "passive", Side: "left"},
			{Number: "2", Name: "A2", Type: "passive", Side: "right"},
		},
	})
	sym2, _ := AuthorSymbol(SymbolSpec{
		Name: "SymB",
		Pins: []SymbolPin{
			{Number: "1", Name: "B1", Type: "passive", Side: "left"},
			{Number: "2", Name: "B2", Type: "passive", Side: "right"},
			{Number: "3", Name: "B3", Type: "passive", Side: "right"},
		},
	})

	fp2 := testFootprint(smdPad("1", -1), smdPad("2", 0), smdPad("3", 1))

	// Check against SymB specifically.
	result, err := CheckPinPad(sym2, fp2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Errorf("SymB should match 3-pad footprint, got errors: %v", result.Errors)
	}

	// Check against SymA: should show unmatched pad 3.
	result2, err := CheckPinPad(sym1, fp2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result2.OK {
		t.Error("SymA (2 pins) should NOT match 3-pad footprint")
	}
}
