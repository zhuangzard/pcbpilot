package kicad

import (
	"strings"
	"testing"
)

func TestAuthorSymbol_SingleUnit(t *testing.T) {
	spec := SymbolSpec{
		Name:      "TestIC",
		Reference: "U",
		Value:     "TestIC",
		Footprint: "Package_SO:SOIC-8",
		Datasheet: "https://example.com/ds.pdf",
		LCSC:      "C12345",
		Pins: []SymbolPin{
			{Number: "1", Name: "VCC", Type: "power_in", Side: "left"},
			{Number: "2", Name: "IN", Type: "input", Side: "left"},
			{Number: "3", Name: "OUT", Type: "output", Side: "right"},
			{Number: "4", Name: "GND", Type: "power_in", Side: "right"},
		},
	}
	text, err := AuthorSymbol(spec)
	if err != nil {
		t.Fatal(err)
	}

	// Check structural elements.
	checks := []string{
		`(symbol "TestIC"`,
		`(pin_names (offset 1.016))`,
		`(in_bom yes)`,
		`(on_board yes)`,
		`(property "Reference" "U"`,
		`(property "Value" "TestIC"`,
		`(property "Footprint" "Package_SO:SOIC-8"`,
		`(property "Datasheet" "https://example.com/ds.pdf"`,
		`(property "LCSC Part #" "C12345"`,
		`(symbol "TestIC_0_1"`,  // body sub-symbol
		`(symbol "TestIC_1_1"`,  // pins sub-symbol
		`(pin power_in line`,    // pin type
		`(name "VCC"`,
		`(number "1"`,
	}
	for _, c := range checks {
		if !strings.Contains(text, c) {
			t.Errorf("missing %q in output:\n%s", c, text)
		}
	}
}

func TestAuthorSymbol_MultiUnit(t *testing.T) {
	spec := SymbolSpec{
		Name:      "DualOpAmp",
		Reference: "U",
		Units:     2,
		Pins: []SymbolPin{
			{Number: "1", Name: "OUT_A", Type: "output", Side: "right", Unit: 1},
			{Number: "2", Name: "IN-_A", Type: "input", Side: "left", Unit: 1},
			{Number: "3", Name: "IN+_A", Type: "input", Side: "left", Unit: 1},
			{Number: "5", Name: "IN+_B", Type: "input", Side: "left", Unit: 2},
			{Number: "6", Name: "IN-_B", Type: "input", Side: "left", Unit: 2},
			{Number: "7", Name: "OUT_B", Type: "output", Side: "right", Unit: 2},
			{Number: "4", Name: "VCC", Type: "power_in", Side: "top", Unit: 0},
			{Number: "8", Name: "GND", Type: "power_in", Side: "bottom", Unit: 0},
		},
	}
	text, err := AuthorSymbol(spec)
	if err != nil {
		t.Fatal(err)
	}

	// Must have common body _0_1, common pins _0_2, and per-unit _1_1, _2_1.
	for _, sub := range []string{"DualOpAmp_0_1", "DualOpAmp_0_2", "DualOpAmp_1_1", "DualOpAmp_2_1"} {
		if !strings.Contains(text, `"`+sub+`"`) {
			t.Errorf("missing sub-symbol %q", sub)
		}
	}
}

func TestAuthorSymbol_FourSides(t *testing.T) {
	spec := SymbolSpec{
		Name: "FourSide",
		Pins: []SymbolPin{
			{Number: "1", Name: "L1", Type: "passive", Side: "left"},
			{Number: "2", Name: "R1", Type: "passive", Side: "right"},
			{Number: "3", Name: "T1", Type: "passive", Side: "top"},
			{Number: "4", Name: "B1", Type: "passive", Side: "bottom"},
		},
	}
	text, err := AuthorSymbol(spec)
	if err != nil {
		t.Fatal(err)
	}

	// Single-unit: pins only in _1_1 (no _0_2).
	// Verify all four pin numbers are present.
	for _, num := range []string{`"1"`, `"2"`, `"3"`, `"4"`} {
		if !strings.Contains(text, "(number "+num) {
			t.Errorf("missing pin number %s", num)
		}
	}
	// Verify all four sides have pins (left=0°, right=180°, top=270°, bottom=90°).
	for _, angle := range []string{"0)", "180)", "270)", "90)"} {
		if !strings.Contains(text, angle) {
			t.Errorf("missing pin angle %s", angle)
		}
	}
}

func TestAuthorSymbol_Validation(t *testing.T) {
	tests := []struct {
		name string
		spec SymbolSpec
		want string
	}{
		{"empty name", SymbolSpec{Pins: []SymbolPin{{Number: "1"}}}, "name is required"},
		{"no pins", SymbolSpec{Name: "X"}, "at least one pin"},
		{"empty pin number", SymbolSpec{Name: "X", Pins: []SymbolPin{{}}}, "number is required"},
		{"bad type", SymbolSpec{Name: "X", Pins: []SymbolPin{{Number: "1", Type: "bogus"}}}, "unknown type"},
		{"bad side", SymbolSpec{Name: "X", Pins: []SymbolPin{{Number: "1", Side: "diagonal"}}}, "unknown side"},
		{"unit out of range", SymbolSpec{Name: "X", Units: 1, Pins: []SymbolPin{{Number: "1", Unit: 5}}}, "out of range"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AuthorSymbol(tt.spec)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestAuthorSymbol_HidePinNames(t *testing.T) {
	spec := SymbolSpec{
		Name:         "R1",
		Reference:    "R",
		HidePinNames: true,
		Pins: []SymbolPin{
			{Number: "1", Name: "A", Type: "passive", Side: "left"},
			{Number: "2", Name: "B", Type: "passive", Side: "right"},
		},
	}
	text, err := AuthorSymbol(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "(hide yes)") {
		t.Error("missing (hide yes) for pin names")
	}
}

func TestAuthorSymbolLib(t *testing.T) {
	sym := `(symbol "R" (pin_names (offset 1.016)) (in_bom yes) (on_board yes))`
	lib := AuthorSymbolLib(map[string]string{"R": sym})
	if !strings.Contains(lib, "kicad_symbol_lib") {
		t.Error("missing kicad_symbol_lib wrapper")
	}
	if !strings.Contains(lib, `(version 20251024)`) {
		t.Error("missing version")
	}
	if !strings.Contains(lib, `(generator "pcbpilot")`) {
		t.Error("missing generator")
	}
}

func TestAuthorSymbol_DefaultEmptyType(t *testing.T) {
	spec := SymbolSpec{
		Name: "X",
		Pins: []SymbolPin{
			{Number: "1", Name: "A", Type: "", Side: "left"},
		},
	}
	text, err := AuthorSymbol(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Empty type should default to passive.
	if !strings.Contains(text, "(pin passive line") {
		t.Error("empty type should default to passive")
	}
}
