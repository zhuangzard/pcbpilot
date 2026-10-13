package kicad

import "testing"

// kicad-cli 10.0.x schematic ERC JSON says "mm" but reports positions in units
// of 100 mm; real millimetre reports must pass through unchanged.
func TestERCPosScale(t *testing.T) {
	cases := []struct {
		units  string
		maxAbs float64
		want   float64
	}{
		{"mm", 0.4953, 100}, // pin at 49.53 mm reported as 0.4953
		{"mm", 5.8674, 100}, // A0 sheet, far corner 586.74 mm
		{"mm", 49.53, 1},    // a real millimetre report
		{"mm", 0, 1},        // no violations
		{"in", 1.95, 25.4},  // inches
		{"mils", 1950, 0.0254},
	}
	for _, c := range cases {
		if got := ercPosScale(c.units, c.maxAbs); got != c.want {
			t.Errorf("ercPosScale(%q, %v) = %v, want %v", c.units, c.maxAbs, got, c.want)
		}
	}
}
