package kicad

import (
	"strings"
	"testing"
)

func TestAuthorFootprint_SMD(t *testing.T) {
	spec := FootprintSpec{
		Name:        "R_0402",
		Description: "Resistor 0402 (1005 metric)",
		Tags:        "resistor 0402",
		Attr:        "smd",
		Pads: []PadSpec{
			{Number: "1", Type: "smd", Shape: "roundrect", At: [3]float64{-0.5, 0, 0}, Size: [2]float64{0.6, 0.5}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
			{Number: "2", Type: "smd", Shape: "roundrect", At: [3]float64{0.5, 0, 0}, Size: [2]float64{0.6, 0.5}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
		},
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}

	checks := []string{
		`(footprint "R_0402"`,
		`(version ` + FPFileVersion + `)`,
		`(generator "pcbpilot")`,
		`(layer "F.Cu")`,
		`(descr "Resistor 0402 (1005 metric)")`,
		`(tags "resistor 0402")`,
		`(attr smd)`,
		`(property "Reference" "REF**"`,
		`(property "Value" "R_0402"`,
		`(pad "1" smd roundrect`,
		`(pad "2" smd roundrect`,
		`(roundrect_rratio`,
		`(layer "F.SilkS")`,
		`(layer "F.Fab")`,
		`(layer "F.CrtYd")`,
	}
	for _, c := range checks {
		if !strings.Contains(text, c) {
			t.Errorf("missing %q in output:\n%s", c, text)
		}
	}
}

func TestAuthorFootprint_THT(t *testing.T) {
	spec := FootprintSpec{
		Name: "Header_1x2",
		Attr: "through_hole",
		Pads: []PadSpec{
			{Number: "1", Type: "thru_hole", Shape: "rect", At: [3]float64{0, 0, 0}, Size: [2]float64{1.7, 1.7}, Drill: &DrillSpec{Diameter: 1.0}, Layers: []string{"*.Cu", "*.Mask"}},
			{Number: "2", Type: "thru_hole", Shape: "oval", At: [3]float64{2.54, 0, 0}, Size: [2]float64{1.7, 1.7}, Drill: &DrillSpec{Diameter: 1.0}, Layers: []string{"*.Cu", "*.Mask"}},
		},
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(text, "(attr through_hole)") {
		t.Error("missing through_hole attr")
	}
	if !strings.Contains(text, `(pad "1" thru_hole rect`) {
		t.Error("missing THT pad 1")
	}
	if !strings.Contains(text, "(drill 1)") {
		t.Error("missing drill")
	}
}

func TestAuthorFootprint_OvalDrill(t *testing.T) {
	spec := FootprintSpec{
		Name: "OvalTest",
		Pads: []PadSpec{
			{Number: "1", Type: "thru_hole", Shape: "oval", At: [3]float64{0, 0, 0}, Size: [2]float64{2, 3},
				Drill: &DrillSpec{Width: 1.0, Height: 1.5}, Layers: []string{"*.Cu", "*.Mask"}},
		},
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "(drill oval 1 1.5)") {
		t.Errorf("expected oval drill, got:\n%s", text)
	}
}

func TestAuthorFootprint_PadRotation(t *testing.T) {
	spec := FootprintSpec{
		Name: "Rotated",
		Pads: []PadSpec{
			{Number: "1", Type: "smd", Shape: "rect", At: [3]float64{0, 0, 45}, Size: [2]float64{1, 2}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
		},
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "(at 0 0 45)") {
		t.Errorf("expected rotation 45 in at, got:\n%s", text)
	}
}

func TestAuthorFootprint_Validation(t *testing.T) {
	tests := []struct {
		name string
		spec FootprintSpec
		want string
	}{
		{"empty name", FootprintSpec{Pads: []PadSpec{{Number: "1", Type: "smd", Shape: "rect", Size: [2]float64{1, 1}, Layers: []string{"F.Cu"}}}}, "name is required"},
		{"no pads", FootprintSpec{Name: "X"}, "at least one pad"},
		{"bad type", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Type: "bogus", Shape: "rect", Size: [2]float64{1, 1}, Layers: []string{"F.Cu"}}}}, "unknown type"},
		{"bad shape", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Type: "smd", Shape: "star", Size: [2]float64{1, 1}, Layers: []string{"F.Cu"}}}}, "unknown shape"},
		{"custom shape", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Type: "smd", Shape: "custom", Size: [2]float64{1, 1}, Layers: []string{"F.Cu"}}}}, "custom shape is not supported"},
		{"zero size", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Type: "smd", Shape: "rect", Size: [2]float64{0, 1}, Layers: []string{"F.Cu"}}}}, "size must be positive"},
		{"smd with drill", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Type: "smd", Shape: "rect", Size: [2]float64{1, 1}, Drill: &DrillSpec{Diameter: 0.5}, Layers: []string{"F.Cu"}}}}, "cannot have a drill"},
		{"tht without drill", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Type: "thru_hole", Shape: "oval", Size: [2]float64{1, 1}, Layers: []string{"*.Cu"}}}}, "requires a drill"},
		{"empty type", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Shape: "rect", Size: [2]float64{1, 1}, Layers: []string{"F.Cu"}}}}, "type is required"},
		{"empty shape", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Type: "smd", Size: [2]float64{1, 1}, Layers: []string{"F.Cu"}}}}, "shape is required"},
		{"empty layers", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Type: "smd", Shape: "rect", Size: [2]float64{1, 1}}}}, "layers is required"},
		{"zero drill", FootprintSpec{Name: "X", Pads: []PadSpec{{Number: "1", Type: "thru_hole", Shape: "oval", Size: [2]float64{2, 2}, Drill: &DrillSpec{}, Layers: []string{"*.Cu"}}}}, "drill diameter must be positive"},
		{"bad attr", FootprintSpec{Name: "X", Attr: "bogus", Pads: []PadSpec{{Number: "1", Type: "smd", Shape: "rect", Size: [2]float64{1, 1}, Layers: []string{"F.Cu"}}}}, "attr must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := AuthorFootprint(tt.spec)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
		})
	}
}

func TestAuthorFootprint_AutoAttr(t *testing.T) {
	// SMD pads → auto attr smd.
	spec := FootprintSpec{
		Name: "Auto",
		Pads: []PadSpec{
			{Number: "1", Type: "smd", Shape: "rect", At: [3]float64{0, 0, 0}, Size: [2]float64{1, 1}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
		},
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "(attr smd)") {
		t.Error("expected auto-detected smd attr")
	}
}

func TestAuthorFootprint_ChamferPin1(t *testing.T) {
	spec := FootprintSpec{
		Name: "Chamfer",
		Pads: []PadSpec{
			{Number: "1", Type: "smd", Shape: "rect", At: [3]float64{-1, -1, 0}, Size: [2]float64{1, 1}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
			{Number: "2", Type: "smd", Shape: "rect", At: [3]float64{1, 1, 0}, Size: [2]float64{1, 1}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
		},
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Should have 5 fab lines (4 edges + 1 chamfer) + 4 silk + 4 courtyard.
	fabCount := strings.Count(text, `(layer "F.Fab")`)
	if fabCount < 5 {
		t.Errorf("expected at least 5 fab lines (with chamfer), got %d", fabCount)
	}
}

func TestEditFootprintPads_Rename(t *testing.T) {
	src := `(footprint "Test" (pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu")) (pad "2" smd rect (at 1 0) (size 1 1) (layers "F.Cu")))`
	newNum := "A1"
	result, err := EditFootprintPads(src, []PadEdit{
		{Number: "1", NewNumber: &newNum},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"A1"`) {
		t.Errorf("expected renamed pad A1, got:\n%s", result)
	}
}

func TestEditFootprintPads_ByIndex(t *testing.T) {
	src := `(footprint "Test" (pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu")) (pad "1" smd rect (at 1 0) (size 1 1) (layers "F.Cu")))`
	newNum := "EP"
	result, err := EditFootprintPads(src, []PadEdit{
		{Index: 1, UseIndex: true, NewNumber: &newNum},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Second pad should be renamed to EP.
	if !strings.Contains(result, `"EP"`) {
		t.Errorf("expected renamed pad EP, got:\n%s", result)
	}
}

func TestEditFootprintPads_Size(t *testing.T) {
	src := `(footprint "Test" (pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu")))`
	newSize := [2]float64{2, 3}
	result, err := EditFootprintPads(src, []PadEdit{
		{Number: "1", Size: &newSize},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "(size 2 3)") {
		t.Errorf("expected new size, got:\n%s", result)
	}
}

func TestEditFootprintPads_NotFound(t *testing.T) {
	src := `(footprint "Test" (pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu")))`
	_, err := EditFootprintPads(src, []PadEdit{
		{Number: "99"},
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestSetFootprintProperty_Existing(t *testing.T) {
	src := `(footprint "Test" (property "Value" "Old" (at 0 0 0) (layer "F.Fab")))`
	result, err := SetFootprintProperty(src, "Value", "New")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, `"New"`) {
		t.Errorf("expected new value, got:\n%s", result)
	}
	// Should preserve layer.
	if !strings.Contains(result, "F.Fab") {
		t.Error("layer was not preserved")
	}
}

func TestSetFootprintProperty_New(t *testing.T) {
	src := `(footprint "Test" (property "Value" "X" (at 0 0 0)))`
	result, err := SetFootprintProperty(src, "MPN", "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "MPN") || !strings.Contains(result, "ABC123") {
		t.Errorf("expected new MPN property, got:\n%s", result)
	}
}

// ---- Regression tests for WP5 review findings ----

// Finding #1: fp_circle must use PCB fill syntax "(fill solid)", not schematic "(fill (type solid))".
func TestAuthorFootprint_FillSyntax(t *testing.T) {
	spec := FootprintSpec{
		Name: "FillTest",
		Pads: []PadSpec{
			{Number: "1", Type: "smd", Shape: "rect", At: [3]float64{0, 0, 0}, Size: [2]float64{1, 1}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
		},
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "(fill (type solid))") {
		t.Error("fp_circle uses schematic fill syntax; PCB footprints need (fill solid)")
	}
	if !strings.Contains(text, "(fill solid)") {
		t.Error("expected (fill solid) in pin-1 circle")
	}
}

// Finding #8: drill validation rejects conflicting specs (diameter + oval dims).
func TestAuthorFootprint_DrillConflict(t *testing.T) {
	// diameter AND width+height should be rejected.
	spec := FootprintSpec{
		Name: "ConflictDrill",
		Pads: []PadSpec{
			{Number: "1", Type: "thru_hole", Shape: "oval", Size: [2]float64{2, 3},
				Drill: &DrillSpec{Diameter: 1.0, Width: 0.8, Height: 1.2}, Layers: []string{"*.Cu", "*.Mask"}},
		},
	}
	_, err := AuthorFootprint(spec)
	if err == nil {
		t.Fatal("expected error for conflicting drill specs")
	}
	if !strings.Contains(err.Error(), "either diameter") {
		t.Errorf("error should mention conflicting specs, got: %v", err)
	}
}

// Finding #8: negative drill rejected.
func TestAuthorFootprint_NegativeDrill(t *testing.T) {
	spec := FootprintSpec{
		Name: "NegDrill",
		Pads: []PadSpec{
			{Number: "1", Type: "thru_hole", Shape: "oval", Size: [2]float64{2, 2},
				Drill: &DrillSpec{Diameter: -0.5}, Layers: []string{"*.Cu", "*.Mask"}},
		},
	}
	_, err := AuthorFootprint(spec)
	if err == nil || !strings.Contains(err.Error(), "positive") {
		t.Errorf("expected positive drill error, got: %v", err)
	}
}

// Finding #8: oval drill needs both width and height positive.
func TestAuthorFootprint_OvalDrillOneDim(t *testing.T) {
	spec := FootprintSpec{
		Name: "HalfOval",
		Pads: []PadSpec{
			{Number: "1", Type: "thru_hole", Shape: "oval", Size: [2]float64{2, 3},
				Drill: &DrillSpec{Width: 1.0, Height: 0}, Layers: []string{"*.Cu", "*.Mask"}},
		},
	}
	_, err := AuthorFootprint(spec)
	if err == nil || !strings.Contains(err.Error(), "positive width and height") {
		t.Errorf("expected oval drill error, got: %v", err)
	}
}

// Finding #9: pin-1 mark uses pad numbered "1", not pads[0]; shuffled order.
func TestAuthorFootprint_Pin1ShuffledPads(t *testing.T) {
	spec := FootprintSpec{
		Name: "Shuffled",
		Pads: []PadSpec{
			{Number: "2", Type: "smd", Shape: "rect", At: [3]float64{1, 0, 0}, Size: [2]float64{1, 1}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
			{Number: "1", Type: "smd", Shape: "rect", At: [3]float64{-1, 0, 0}, Size: [2]float64{1, 1}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
		},
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}
	// Pin 1 is at x=-1, so silk dot should be to the left of -1, not to left of pad[0] at x=1.
	if !strings.Contains(text, "fp_circle") {
		t.Fatal("expected pin-1 silk circle")
	}
	// The circle center should be near x=-1.8 (pad1 at -1, half-size 0.5, offset -0.3).
	if strings.Contains(text, "center 0.2") {
		t.Error("silk dot is near pad[0] (x=1), not pad '1' (x=-1)")
	}
}

// Finding #9: chamfer follows pad "1" corner, not always top-left.
func TestAuthorFootprint_ChamferFollowsPad1(t *testing.T) {
	// Pad 1 at bottom-right: chamfer should be "br" corner.
	spec := FootprintSpec{
		Name: "BRChamfer",
		Pads: []PadSpec{
			{Number: "1", Type: "smd", Shape: "rect", At: [3]float64{1, 1, 0}, Size: [2]float64{1, 1}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
			{Number: "2", Type: "smd", Shape: "rect", At: [3]float64{-1, -1, 0}, Size: [2]float64{1, 1}, Layers: []string{"F.Cu", "F.Paste", "F.Mask"}},
		},
	}
	text, err := AuthorFootprint(spec)
	if err != nil {
		t.Fatal(err)
	}
	// 5 fab lines required (4 edges + 1 chamfer).
	fabCount := strings.Count(text, `(layer "F.Fab")`)
	if fabCount < 5 {
		t.Errorf("expected at least 5 fab lines, got %d", fabCount)
	}
}

// Finding #10: editing duplicate pad numbers by name is rejected as ambiguous.
func TestEditFootprintPads_AmbiguousDuplicate(t *testing.T) {
	src := `(footprint "Test" (pad "2" smd rect (at 0 0) (size 1 1) (layers "F.Cu")) (pad "2" smd rect (at 1 0) (size 1 1) (layers "F.Cu")))`
	newSize := [2]float64{3, 3}
	_, err := EditFootprintPads(src, []PadEdit{
		{Number: "2", Size: &newSize},
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("expected ambiguous error for duplicate pad number, got: %v", err)
	}
}

// Finding #10: editing pad with invalid shape rejected.
func TestEditFootprintPads_BadShape(t *testing.T) {
	src := `(footprint "Test" (pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu")))`
	s := "star"
	_, err := EditFootprintPads(src, []PadEdit{
		{Number: "1", Shape: &s},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown pad shape") {
		t.Errorf("expected shape validation error, got: %v", err)
	}
}

// Finding #10: editing pad size to zero rejected.
func TestEditFootprintPads_ZeroSize(t *testing.T) {
	src := `(footprint "Test" (pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu")))`
	newSize := [2]float64{0, 1}
	_, err := EditFootprintPads(src, []PadEdit{
		{Number: "1", Size: &newSize},
	})
	if err == nil || !strings.Contains(err.Error(), "positive") {
		t.Errorf("expected size validation error, got: %v", err)
	}
}

// Finding #10: shape→roundrect adds rratio, non-roundrect removes it.
func TestEditFootprintPads_ShapeRoundRectManaged(t *testing.T) {
	src := `(footprint "Test" (pad "1" smd rect (at 0 0) (size 1 1) (layers "F.Cu")))`
	rr := "roundrect"
	result, err := EditFootprintPads(src, []PadEdit{
		{Number: "1", Shape: &rr},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "roundrect_rratio") {
		t.Error("changing to roundrect should add roundrect_rratio")
	}
	// Now change back to rect.
	rect := "rect"
	result2, err := EditFootprintPads(result, []PadEdit{
		{Number: "1", Shape: &rect},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result2, "roundrect_rratio") {
		t.Error("changing away from roundrect should remove roundrect_rratio")
	}
}

func TestFootprintPadNumbers(t *testing.T) {
	src := `(footprint "Test" (pad "1" smd rect (at 0 0) (size 1 1)) (pad "2" smd rect (at 1 0) (size 1 1)) (pad "" np_thru_hole circle (at 0 0) (size 1 1)))`
	nums, err := FootprintPadNumbers(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(nums) != 3 {
		t.Fatalf("expected 3 pads, got %d", len(nums))
	}
	if nums[0] != "1" || nums[1] != "2" || nums[2] != "" {
		t.Errorf("unexpected pad numbers: %v", nums)
	}
}
