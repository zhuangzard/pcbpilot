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

	// Must have _0_1 (common body+pins), _1_1, _2_1 — no _0_2 sibling.
	for _, sub := range []string{"DualOpAmp_0_1", "DualOpAmp_1_1", "DualOpAmp_2_1"} {
		if !strings.Contains(text, `"`+sub+`"`) {
			t.Errorf("missing sub-symbol %q", sub)
		}
	}
	if strings.Contains(text, `"DualOpAmp_0_2"`) {
		t.Error("_0_2 should not exist (common pins belong in _0_1)")
	}
	// Common pins (VCC, GND) must be inside _0_1.
	// Find the _0_1 block and verify it contains the common pin numbers.
	idx01 := strings.Index(text, `"DualOpAmp_0_1"`)
	if idx01 < 0 {
		t.Fatal("_0_1 not found")
	}
	block01 := text[idx01:]
	if !strings.Contains(block01, `(number "4"`) || !strings.Contains(block01, `(number "8"`) {
		t.Error("common pins 4 (VCC) and 8 (GND) should be inside _0_1")
	}
	// Per-unit _1_1 should have body rectangle AND pins (not split into separate siblings).
	idx11 := strings.Index(text, `"DualOpAmp_1_1"`)
	if idx11 < 0 {
		t.Fatal("_1_1 not found")
	}
	block11 := text[idx11:]
	if !strings.Contains(block11, "rectangle") {
		t.Error("_1_1 should contain a body rectangle")
	}
	if !strings.Contains(block11, `(number "1"`) {
		t.Error("_1_1 should contain pins")
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
	if !strings.Contains(lib, "(version "+SymLibVersion+")") {
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

// Regression: common and unit-specific pins must not share the same coordinates.
// A multi-unit symbol with a common left pin (GND) and a per-unit left pin (IN)
// must place them at distinct Y positions. (Codex+Kimi finding #1)
func TestAuthorSymbol_CommonAndUnitPinsNoCollision(t *testing.T) {
	spec := SymbolSpec{
		Name:  "MultiCollide",
		Units: 2,
		Pins: []SymbolPin{
			{Number: "4", Name: "GND", Type: "power_in", Side: "left", Unit: 0},
			{Number: "1", Name: "IN_A", Type: "input", Side: "left", Unit: 1},
			{Number: "2", Name: "OUT_A", Type: "output", Side: "right", Unit: 1},
			{Number: "3", Name: "IN_B", Type: "input", Side: "left", Unit: 2},
		},
	}
	text, err := AuthorSymbol(spec)
	if err != nil {
		t.Fatal(err)
	}

	// Extract pin coordinates from a delimited sub-symbol block.
	type coord struct{ x, y string }
	extractPinCoords := func(block string) []coord {
		var coords []coord
		for _, line := range strings.Split(block, "\n") {
			idx := strings.Index(line, "(pin ")
			if idx < 0 {
				continue
			}
			atIdx := strings.Index(line, "(at ")
			if atIdx < 0 {
				continue
			}
			rest := line[atIdx+4:]
			end := strings.Index(rest, ")")
			if end < 0 {
				continue
			}
			parts := strings.Fields(rest[:end])
			if len(parts) >= 2 {
				coords = append(coords, coord{parts[0], parts[1]})
			}
		}
		return coords
	}

	// Split sub-symbol blocks by finding each (symbol "...") boundary.
	extractBlock := func(text, marker string) string {
		start := strings.Index(text, marker)
		if start < 0 {
			return ""
		}
		rest := text[start+len(marker):]
		// Find the next (symbol " which starts the next sub-symbol.
		next := strings.Index(rest, `(symbol "`)
		if next >= 0 {
			return rest[:next]
		}
		return rest
	}

	block01 := extractBlock(text, `"MultiCollide_0_1"`)
	if block01 == "" {
		t.Fatal("_0_1 not found")
	}
	block11 := extractBlock(text, `"MultiCollide_1_1"`)
	if block11 == "" {
		t.Fatal("_1_1 not found")
	}
	block21 := extractBlock(text, `"MultiCollide_2_1"`)
	if block21 == "" {
		t.Fatal("_2_1 not found")
	}

	commonCoords := extractPinCoords(block01)
	unit1Coords := extractPinCoords(block11)
	unit2Coords := extractPinCoords(block21)

	// Check that no common pin coordinate collides with any unit-specific pin.
	for _, cc := range commonCoords {
		for _, uc := range unit1Coords {
			if cc.x == uc.x && cc.y == uc.y {
				t.Errorf("common pin at (%s,%s) collides with unit-1 pin", cc.x, cc.y)
			}
		}
		for _, uc := range unit2Coords {
			if cc.x == uc.x && cc.y == uc.y {
				t.Errorf("common pin at (%s,%s) collides with unit-2 pin", cc.x, cc.y)
			}
		}
	}

	// Sanity: common block should have exactly 1 pin (GND).
	if len(commonCoords) != 1 {
		t.Errorf("expected 1 common pin, got %d", len(commonCoords))
	}
	// Unit 1 should have 2 pins (IN_A, OUT_A).
	if len(unit1Coords) != 2 {
		t.Errorf("expected 2 unit-1 pins, got %d", len(unit1Coords))
	}
}

// Regression: declared units without unit-specific pins must still emit a child.
// A 3-unit symbol where unit 3 has no dedicated pins must still emit _3_1.
// (Codex+Kimi finding #2)
func TestAuthorSymbol_EmptyUnitEmitted(t *testing.T) {
	spec := SymbolSpec{
		Name:  "ThreeUnit",
		Units: 3,
		Pins: []SymbolPin{
			{Number: "1", Name: "VCC", Type: "power_in", Side: "top", Unit: 0},
			{Number: "2", Name: "GND", Type: "power_in", Side: "bottom", Unit: 0},
			{Number: "3", Name: "A", Type: "input", Side: "left", Unit: 1},
			// Unit 2 has a pin.
			{Number: "4", Name: "B", Type: "input", Side: "left", Unit: 2},
			// Unit 3 has NO unit-specific pins — only common pins.
		},
	}
	text, err := AuthorSymbol(spec)
	if err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{"ThreeUnit_0_1", "ThreeUnit_1_1", "ThreeUnit_2_1", "ThreeUnit_3_1"} {
		if !strings.Contains(text, `"`+sub+`"`) {
			t.Errorf("missing sub-symbol %q — declared unit must always be emitted", sub)
		}
	}
}
