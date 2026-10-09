package kicad

import (
	"strings"
	"testing"
)

// A small A4 sheet with a resistor symbol and a wire; the library resistor
// spans ±3.81 mm vertically with pins.
const schA4 = `(kicad_sch (version 20250114) (generator "eeschema")
	(paper "A4")
	(lib_symbols
		(symbol "Device:R"
			(symbol "R_0_1" (rectangle (start -1.016 -2.54) (end 1.016 2.54)))
			(symbol "R_1_1"
				(pin passive line (at 0 3.81 270) (length 1.27))
				(pin passive line (at 0 -3.81 90) (length 1.27)))))
	(symbol (lib_id "Device:R") (at 100 80 0) (property "Reference" "R1" (at 102 80 0)))
	(wire (pts (xy 100 76.19) (xy 100 60)))
	(label "SIG" (at 100 60 0))
)`

func TestFitSheetKeepsA4(t *testing.T) {
	out, r, err := FitSheet(schA4)
	if err != nil {
		t.Fatal(err)
	}
	if r.To != "A4" || r.Changed || out != schA4 || !r.ContentFound {
		t.Fatalf("%+v", r)
	}
	if r.Content.MinY > 60 || r.Content.MinY < 58 || r.Content.MaxY < 83.8 {
		t.Fatalf("content %+v", r.Content)
	}
}

// Content reaching x=380 needs A3 (it would also enter A4's title block).
func TestFitSheetGrowsToA3(t *testing.T) {
	src := strings.Replace(schA4, `(label "SIG" (at 100 60 0))`, `(label "SIG" (at 100 60 0)) (label "FAR" (at 380 60 0))`, 1)
	out, r, err := FitSheet(src)
	if err != nil {
		t.Fatal(err)
	}
	if r.To != "A3" || !strings.Contains(out, `(paper "A3")`) {
		t.Fatalf("%+v", r)
	}
}

// An A2 sheet whose content fits A4 shrinks back.
func TestFitSheetShrinks(t *testing.T) {
	src := strings.Replace(schA4, `(paper "A4")`, `(paper "A2")`, 1)
	out, r, _ := FitSheet(src)
	if r.From != "A2" || r.To != "A4" || !strings.Contains(out, `(paper "A4")`) {
		t.Fatalf("%+v", r)
	}
}

// Content in negative coordinates is shifted into the drawing area on the
// 1.27 mm grid; library coordinates are not touched.
func TestFitSheetShiftsContent(t *testing.T) {
	src := strings.NewReplacer(`(at 100 80 0)`, `(at -40 80 0)`, `(xy 100 76.19) (xy 100 60)`, `(xy -40 76.19) (xy -40 60)`, `(at 100 60 0)`, `(at -40 60 0)`, `(at 102 80 0)`, `(at -38 80 0)`).Replace(schA4)
	out, r, err := FitSheet(src)
	if err != nil {
		t.Fatal(err)
	}
	if r.ShiftX <= 0 || r.To != "A4" || !strings.Contains(out, "(rectangle (start -1.016 -2.54)") {
		t.Fatalf("%+v\n%s", r, out)
	}
	if _, r2, _ := FitSheet(out); r2.ShiftX != 0 || r2.Changed {
		t.Fatalf("not idempotent: %+v", r2)
	}
}

func TestFitSheetTooBig(t *testing.T) {
	src := strings.Replace(schA4, `(label "SIG" (at 100 60 0))`, `(label "SIG" (at 100 60 0)) (label "HUGE" (at 2000 60 0))`, 1)
	_, r, _ := FitSheet(src)
	if !r.TooBig {
		t.Fatalf("%+v", r)
	}
}
