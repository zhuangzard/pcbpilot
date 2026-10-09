package app

import "testing"

// F7 (e2e-round-2026-08-25): phase A rejected its own regenerated stubs
// (R5 terminal overlap / collinear flags) and the whole page produced
// nothing. zfSettlePassive staggers a stub or moves one terminal to another
// body side before giving up; only two terminals on one pin point stay fatal.
func TestZfSettlePassive_RepairsInsteadOfRejecting(t *testing.T) {
	body := layoutBBox{MinX: 100, MinY: 100, MaxX: 110, MaxY: 130}
	cases := []struct {
		name string
		pins []zfPinsPin
	}{
		// both pins left of the body on one line: two left stubs would be collinear
		{"collinear left stubs", []zfPinsPin{{"1", 85, 90, "GND"}, {"2", 90, 90, "+5V"}}},
		// corner pins whose flags overlap across sides (R5)
		{"cross-side overlap", []zfPinsPin{{"1", 95, 95, "VIN_EXT"}, {"2", 110, 100, "GND"}}},
	}
	for _, c := range cases {
		g := zfOpposedGroup(zfPinsPart{"J9", body, c.pins})
		pg, err := zfGenPassive(g)
		if err != nil {
			t.Fatalf("%s: still rejected: %v", c.name, err)
		}
		if err := zfCheckPassiveOpposed(pg); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if err := zfCheckTermOverlap(pg); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		for i, tm := range pg.Terms {
			wire, marker := zfTermGeom(tm.PinX, tm.PinY, tm.Offset, tm.Dir, tm.Kind, tm.Net, tm.SpreadX)
			if marker != tm.BBox || wire != pg.Wires[i] {
				t.Errorf("%s: term %d geometry is not zfTermGeom's (two rulers)", c.name, i)
			}
		}
		t.Logf("%s → %s/%g, %s/%g", c.name, pg.Terms[0].Dir, pg.Terms[0].Offset, pg.Terms[1].Dir, pg.Terms[1].Offset)
	}
}
