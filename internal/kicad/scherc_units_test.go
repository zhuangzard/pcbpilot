package kicad

import (
	"fmt"
	"testing"
)

// A report written with --units in or mils comes out in mm like the
// default "mm" report; the 10.0.6 1/100 scale applies on top of it.
func TestParseERCReportCoordinateUnits(t *testing.T) {
	for _, u := range []string{"mm", "", "MM"} {
		if got := ercUnitMM(u); got != 1 {
			t.Errorf("ercUnitMM(%q) = %v, want 1", u, got)
		}
	}
	for _, c := range []struct {
		ver, units string
		x, y       float64
	}{
		{"10.0.7", "mm", 88.9, 85.09},
		{"10.0.7", "in", 3.5, 3.35},
		{"10.0.7", "mils", 3500, 3350},
		{"10.0.6", "mm", 0.889, 0.8509},
		{"10.0.6", "in", 0.035, 0.0335},
		{"10.0.6", "mils", 35, 33.5},
	} {
		js := fmt.Sprintf(`{"kicad_version": %q, "coordinate_units": %q, "sheets": [{"path": "/", "uuid_path": "/r",
 "violations": [{"description": "d", "severity": "error", "type": "t",
 "items": [{"description": "Symbol R3 Pin 1", "pos": {"x": %v, "y": %v}, "uuid": "u"}]}]}]}`, c.ver, c.units, c.x, c.y)
		r, err := ParseERCReport([]byte(js), ERCParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Violations[0].Items[0].Pos; got != (Pt{88.9, 85.09}) {
			t.Errorf("%s %s: at %+v, want (88.9, 85.09)", c.ver, c.units, got)
		}
	}
}
