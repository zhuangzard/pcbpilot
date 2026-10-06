package pcbauto

import "testing"

func TestStaleMechFills(t *testing.T) {
	b := &Board{Holes: []*Hole{
		{Name: "oldA", C: Point{100, 100}, Dia: 126},              // copied from the 150x110 source, old corner
		{Name: "same", C: Point{200, 200}, Dia: 126},              // coincides with a new hole: kept
		{Name: "J2:fill1", Owner: "J2", C: Point{50, 50}},         // footprint locating hole
		{Name: "slot", C: Point{300, 300}, Poly: []Point{{0, 0}}}, // milled slot polygon
		// holes added by the mech spec:
		{Name: "M1", C: Point{200, 200.5}, Dia: 126},
		{Name: "M2", C: Point{3800, 3000}, Dia: 126},
	}}
	got := StaleMechFills(b, 4)
	if len(got) != 1 || got[0] != "oldA" {
		t.Fatalf("stale = %v, want [oldA]", got)
	}
	if got := StaleMechFills(b, len(b.Holes)); got != nil {
		t.Fatalf("no new holes but stale = %v", got)
	}
	// Dropping them keeps the added holes at the tail.
	n, _ := DropReplaced(b, MechReplace{Fills: got})
	if n != 1 || b.Holes[len(b.Holes)-1].Name != "M2" || b.Holes[len(b.Holes)-2].Name != "M1" {
		t.Fatalf("after drop: %d %+v", n, b.Holes)
	}
}
