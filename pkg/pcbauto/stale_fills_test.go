package pcbauto

import "testing"

func TestStaleMechFills(t *testing.T) {
	b := &Board{Holes: []*Hole{
		{Name: "oldA", C: Point{100, 100}, Dia: 126},                // copied from a bigger source board, old corner
		{Name: "same", C: Point{200, 200}, Dia: 126},                // exactly where a new hole goes: stacked otherwise
		{Name: "cutout", C: Point{900, 900}, Dia: 400},              // large cutout, not hole-sized: kept
		{Name: "J2:fill1", Owner: "J2", C: Point{50, 50}, Dia: 126}, // footprint locating hole
		{Name: "slot", C: Point{300, 300}, Poly: []Point{{0, 0}}},   // milled slot polygon
		// holes added by the mech spec:
		{Name: "M1", C: Point{200, 200.5}, Dia: 126},
		{Name: "M2", C: Point{3800, 3000}, Dia: 126},
	}}
	got := StaleMechFills(b, 5)
	if len(got) != 2 || got[0] != "oldA" || got[1] != "same" {
		t.Fatalf("stale = %v, want [oldA same]", got)
	}
	if got := StaleMechFills(b, len(b.Holes)); got != nil {
		t.Fatalf("no new holes but stale = %v", got)
	}
	// Dropping them keeps the added holes at the tail.
	n, _ := DropReplaced(b, MechReplace{Fills: got})
	if n != 2 || b.Holes[len(b.Holes)-1].Name != "M2" || b.Holes[len(b.Holes)-2].Name != "M1" || len(b.Holes) != 5 {
		t.Fatalf("after drop: %d %+v", n, b.Holes)
	}
}
