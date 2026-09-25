package pcbauto

import "testing"

// A route-only run re-applies the mech spec to a board that already has its
// holes and keep-outs: nothing may be written twice (ESP32 mini 2026-09-25).
func TestDropExistingMech(t *testing.T) {
	sq := func(x, y float64) []Point { return []Point{{x, y}, {x + 100, y}, {x + 100, y + 100}, {x, y + 100}} }
	b := &Board{
		Holes:    []*Hole{{C: Point{100, 100}, Dia: 126}},
		Keepouts: []*Keepout{{Poly: sq(0, 0)}},
	}
	b.Holes = append(b.Holes, &Hole{C: Point{100.4, 100}, Dia: 126}, &Hole{C: Point{900, 100}, Dia: 126})
	b.Keepouts = append(b.Keepouts, &Keepout{Poly: sq(0.5, 0)}, &Keepout{Poly: sq(500, 0)})
	nh, nk := DropExistingMech(b, 1, 1)
	if len(nh) != 1 || nh[0].C.X != 900 || len(nk) != 1 || nk[0].Poly[0].X != 500 {
		t.Fatalf("new holes %v keepouts %v, want only the (900,100) hole and the x=500 keep-out", nh, nk)
	}
	if len(b.Holes) != 2 || len(b.Keepouts) != 2 {
		t.Fatalf("model keeps duplicates: %d holes, %d keep-outs", len(b.Holes), len(b.Keepouts))
	}
	if !SameOutline(sq(0, 0), sq(0.3, 0), 0.5) || SameOutline(sq(0, 0), sq(2, 0), 0.5) {
		t.Fatal("SameOutline tolerance")
	}
}
