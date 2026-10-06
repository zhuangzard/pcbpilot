package geom

import (
	"math/rand/v2"
	"testing"
)

func TestOctilinearSteps(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for i := 0; i < 10_000; i++ {
		a := Pt{rng.Int64N(1<<28) - 1<<27, rng.Int64N(1<<28) - 1<<27}
		d := Dir8(rng.IntN(8))
		n := 1 + rng.Int64N(1<<26)
		b := a.Step(d, n)
		dx, dy := abs(b.X-a.X), abs(b.Y-a.Y)
		if d.Diagonal() && dx != dy {
			t.Fatalf("45° step %v from %v by %d: |dx| %d != |dy| %d", d, a, n, dx, dy)
		}
		if !IsOctilinear(a, b) {
			t.Fatalf("Step(%v, %d) from %v is not octilinear", d, n, a)
		}
		if got, ok := DirOf(a, b); !ok || got != d {
			t.Fatalf("DirOf(%v, %v) = %v, %v; want %v", a, b, got, ok, d)
		}
	}
	for _, c := range [][2]Pt{{{0, 0}, {0, 0}}, {{0, 0}, {3, 2}}, {{0, 0}, {-5, 4}}} {
		if _, ok := DirOf(c[0], c[1]); ok {
			t.Errorf("DirOf(%v, %v) accepted a non-octilinear step", c[0], c[1])
		}
	}
}

func TestPredicates(t *testing.T) {
	if Orient(Pt{0, 0}, Pt{10, 0}, Pt{5, 1}) != 1 || Orient(Pt{0, 0}, Pt{10, 0}, Pt{5, -1}) != -1 ||
		Orient(Pt{0, 0}, Pt{10, 10}, Pt{-3, -3}) != 0 {
		t.Error("Orient signs")
	}
	seg := []struct {
		a, b, c, d Pt
		want       bool
	}{
		{Pt{0, 0}, Pt{10, 10}, Pt{0, 10}, Pt{10, 0}, true},
		{Pt{0, 0}, Pt{10, 0}, Pt{10, 0}, Pt{20, 5}, true},  // shared end point
		{Pt{0, 0}, Pt{10, 0}, Pt{5, 0}, Pt{20, 0}, true},   // collinear overlap
		{Pt{0, 0}, Pt{10, 0}, Pt{11, 0}, Pt{20, 0}, false}, // collinear gap
		{Pt{0, 0}, Pt{10, 0}, Pt{5, 1}, Pt{5, 9}, false},
	}
	for _, c := range seg {
		if SegsIntersect(c.a, c.b, c.c, c.d) != c.want || SegsIntersect(c.c, c.d, c.a, c.b) != c.want {
			t.Errorf("SegsIntersect(%v %v, %v %v) != %v", c.a, c.b, c.c, c.d, c.want)
		}
	}
	// Concave "L": the notch is outside, the boundary inside.
	l := []Pt{{0, 0}, {20, 0}, {20, 10}, {10, 10}, {10, 20}, {0, 20}}
	for p, want := range map[Pt]bool{
		{5, 5}: true, {15, 15}: false, {10, 15}: true, {20, 5}: true, {0, 20}: true, {21, 5}: false, {5, 10}: true,
	} {
		if InPoly(p, l) != want {
			t.Errorf("InPoly(%v, L) != %v", p, want)
		}
	}
}

func TestOctagonContainsInflatedShape(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	for i := 0; i < 300; i++ {
		s := randShape(rng, Pt{0, 0}, 10_000, rng.IntN(5))
		d := rng.Int64N(3_000)
		o := Octagon(s, d)
		for j := range o.Pts {
			if a, b := o.Pts[j], o.Pts[(j+1)%len(o.Pts)]; !IsOctilinear(a, b) {
				t.Fatalf("Octagon(%v, %d) edge %v-%v is not octilinear", s, d, a, b)
			}
		}
		box := s.Bounds().Grow(d + 1)
		for k := 0; k < 200; k++ {
			p := Pt{box.MinX + rng.Int64N(box.MaxX-box.MinX), box.MinY + rng.Int64N(box.MaxY-box.MinY)}
			if Dist(Circle{C: p}, s) <= d && !InPoly(p, o.Pts) {
				t.Fatalf("Octagon(%v, %d) misses %v at distance %d", s, d, p, Dist(Circle{C: p}, s))
			}
		}
	}
	// A disc of radius 100: axis sides at ±100, diagonal offset ⌈100·√2⌉ = 142.
	o := Octagon(Circle{R: 100}, 0)
	want := []Pt{{-42, -100}, {42, -100}, {100, -42}, {100, 42}, {42, 100}, {-42, 100}, {-100, 42}, {-100, -42}}
	if len(o.Pts) != len(want) {
		t.Fatalf("Octagon(disc) = %v", o.Pts)
	}
	for i := range want {
		if o.Pts[i] != want[i] {
			t.Fatalf("Octagon(disc) = %v, want %v", o.Pts, want)
		}
	}
	// A box is its own hull.
	if o := Octagon(Rect{0, 0, 11, 6}, 0); len(o.Pts) != 4 {
		t.Errorf("Octagon(box) = %v, want the 4 corners", o.Pts)
	}
}
