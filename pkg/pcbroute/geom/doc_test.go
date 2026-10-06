package geom

import "testing"

func TestBoundsHalfOpen(t *testing.T) {
	cases := []struct {
		s    Shape
		want Rect
	}{
		{Rect{0, 0, 10, 5}, Rect{0, 0, 10, 5}},
		{Seg{A: Pt{10, 0}, B: Pt{0, 10}, HalfW: 2}, Rect{-2, -2, 13, 13}},
		{Circle{C: Pt{5, 5}, R: 3}, Rect{2, 2, 9, 9}},
		{Poly{Pts: []Pt{{0, 0}, {4, 0}, {4, 7}}}, Rect{0, 0, 5, 8}},
		{Poly{}, Rect{}},
	}
	for _, c := range cases {
		if got := c.s.Bounds(); got != c.want {
			t.Errorf("%#v.Bounds() = %v, want %v", c.s, got, c.want)
		}
	}
}
