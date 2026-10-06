package geom

import (
	"math"
	"math/rand/v2"
	"testing"
)

// The oracle below is an independent float64 brute force: shapes as point
// lists plus a radius, distance as the minimum over all point–edge pairs,
// overlap by float orientation and crossing number. Test coordinates stay
// below 2²⁵ nm so float products of differences are exact.

type fshape struct {
	pts [][2]float64
	r   float64
}

func toF(s Shape) fshape {
	var buf [4]Pt
	c := coreOf(s, &buf)
	f := fshape{r: float64(c.r)}
	for _, p := range c.pts {
		f.pts = append(f.pts, [2]float64{float64(p.X), float64(p.Y)})
	}
	return f
}

func fPtSeg(p, a, b [2]float64) float64 {
	ux, uy := b[0]-a[0], b[1]-a[1]
	l := ux*ux + uy*uy
	t := 0.0
	if l > 0 {
		t = math.Max(0, math.Min(1, ((p[0]-a[0])*ux+(p[1]-a[1])*uy)/l))
	}
	return math.Hypot(p[0]-a[0]-t*ux, p[1]-a[1]-t*uy)
}

func fCross(a, b, c [2]float64) float64 { return (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0]) }

func fSegsMeet(a, b, c, d [2]float64) bool {
	d1, d2, d3, d4 := fCross(c, d, a), fCross(c, d, b), fCross(a, b, c), fCross(a, b, d)
	if d1*d2 < 0 && d3*d4 < 0 {
		return true
	}
	return fPtSeg(a, c, d) == 0 || fPtSeg(b, c, d) == 0 || fPtSeg(c, a, b) == 0 || fPtSeg(d, a, b) == 0
}

func fInside(p [2]float64, poly [][2]float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[j], poly[i]
		if fPtSeg(p, a, b) == 0 {
			return true
		}
		if (a[1] > p[1]) != (b[1] > p[1]) {
			x := a[0] + (p[1]-a[1])*(b[0]-a[0])/(b[1]-a[1])
			if p[0] < x {
				in = !in
			}
		}
	}
	return in
}

// fEdges lists a point as a zero-length edge, a segment as one edge and a
// polygon as its closed ring.
func fEdges(s fshape) [][2][2]float64 {
	switch len(s.pts) {
	case 1:
		return [][2][2]float64{{s.pts[0], s.pts[0]}}
	case 2:
		return [][2][2]float64{{s.pts[0], s.pts[1]}}
	}
	var out [][2][2]float64
	for i, j := 0, len(s.pts)-1; i < len(s.pts); j, i = i, i+1 {
		out = append(out, [2][2]float64{s.pts[j], s.pts[i]})
	}
	return out
}

func oracleDist(a, b Shape) float64 {
	fa, fb := toF(a), toF(b)
	if len(fa.pts) >= 3 && fInside(fb.pts[0], fa.pts) || len(fb.pts) >= 3 && fInside(fa.pts[0], fb.pts) {
		return 0
	}
	best := math.Inf(1)
	for _, e := range fEdges(fa) {
		for _, f := range fEdges(fb) {
			if fSegsMeet(e[0], e[1], f[0], f[1]) {
				return 0
			}
			best = min(best, fPtSeg(e[0], f[0], f[1]), fPtSeg(e[1], f[0], f[1]),
				fPtSeg(f[0], e[0], e[1]), fPtSeg(f[1], e[0], e[1]))
		}
	}
	return math.Max(0, best-fa.r-fb.r)
}

// randShape draws a shape near c with features up to size.
func randShape(rng *rand.Rand, c Pt, size int64, kind int) Shape {
	p := func() Pt { return Pt{c.X + rng.Int64N(2*size+1) - size, c.Y + rng.Int64N(2*size+1) - size} }
	switch kind {
	case 0:
		return Circle{C: p(), R: rng.Int64N(size/4 + 1)}
	case 1:
		return Seg{A: p(), B: p(), HalfW: rng.Int64N(size/8 + 1)}
	case 2: // octilinear segment
		a := p()
		return Seg{A: a, B: a.Step(Dir8(rng.IntN(8)), rng.Int64N(size)), HalfW: rng.Int64N(size/8 + 1)}
	case 3:
		a, b := p(), p()
		return Rect{min(a.X, b.X), min(a.Y, b.Y), max(a.X, b.X) + 1, max(a.Y, b.Y) + 1}
	default: // star-shaped, hence simple, polygon
		n := 3 + rng.IntN(6)
		q := p()
		pts := make([]Pt, n)
		for i := range pts {
			ang := 2 * math.Pi * (float64(i) + 0.8*rng.Float64()) / float64(n)
			rad := float64(1 + rng.Int64N(size))
			pts[i] = Pt{q.X + int64(rad*math.Cos(ang)), q.Y + int64(rad*math.Sin(ang))}
		}
		return Poly{Pts: pts}
	}
}

func TestDistAgainstOracle(t *testing.T) {
	n := 100_000
	if testing.Short() {
		n = 20_000
	}
	rng := rand.New(rand.NewPCG(1, 2))
	var kinds [5][5]int
	for i := 0; i < n; i++ {
		size := int64(1) << (4 + rng.IntN(16)) // 16 nm … 0.5 mm features
		c := Pt{rng.Int64N(1<<24) - 1<<23, rng.Int64N(1<<24) - 1<<23}
		ka, kb := rng.IntN(5), rng.IntN(5)
		kinds[ka][kb]++
		a := randShape(rng, c, size, ka)
		b := randShape(rng, c.Add(Pt{rng.Int64N(4*size+1) - 2*size, rng.Int64N(4*size+1) - 2*size}), size, kb)
		got, want := Dist(a, b), oracleDist(a, b)
		if math.Abs(float64(got)-want) > 1 {
			t.Fatalf("case %d: Dist(%v, %v) = %d, oracle %.3f", i, a, b, got, want)
		}
		if Dist(b, a) != got {
			t.Fatalf("case %d: Dist not symmetric for %v, %v", i, a, b)
		}
		// Within is the exact predicate; it must agree with the floored distance
		// (⌊D⌋ < r ⇔ D < r for integer r).
		for _, r := range []int64{1, got, got + 1, got + 1 + rng.Int64N(size+1)} {
			if r > 0 && Within(a, b, r) != (got < r) {
				t.Fatalf("case %d: Within(%v, %v, %d) = %v, Dist %d", i, a, b, r, !(got < r), got)
			}
		}
	}
	for ka := range kinds {
		for kb := range kinds[ka] {
			if kinds[ka][kb] == 0 {
				t.Errorf("kind pair %d/%d never drawn", ka, kb)
			}
		}
	}
}

// Large coordinates near ±MaxCoord: the exact kernels must neither overflow
// nor lose a nanometre.
func TestDistLargeCoordinates(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for i := 0; i < 20_000; i++ {
		size := int64(1) << (10 + rng.IntN(19))
		edge := MaxCoord - 2*size - 1
		c := Pt{rng.Int64N(2*edge+1) - edge, rng.Int64N(2*edge+1) - edge}
		a := randShape(rng, c, size, rng.IntN(3)) // circles and segments: no float overlap test
		b := randShape(rng, c, size, rng.IntN(3))
		if got, want := Dist(a, b), oracleDist(a, b); math.Abs(float64(got)-want) > 1 {
			t.Fatalf("Dist(%v, %v) = %d, oracle %.3f", a, b, got, want)
		}
	}
	// Two segments spanning the whole range.
	a := Seg{A: Pt{-MaxCoord, -MaxCoord}, B: Pt{MaxCoord, MaxCoord}}
	b := Seg{A: Pt{-MaxCoord, -MaxCoord + 1000}, B: Pt{MaxCoord - 1000, MaxCoord}}
	if got := Dist(a, b); got != 707 { // 1000/√2 = 707.1
		t.Errorf("long diagonal segments: Dist = %d, want 707", got)
	}
}

func TestDistCases(t *testing.T) {
	sq := Rect{0, 0, 101, 101} // closed box [0,100]²
	cases := []struct {
		a, b Shape
		want int64
	}{
		{Circle{Pt{0, 0}, 0}, Circle{Pt{3, 4}, 0}, 5},
		{Circle{Pt{0, 0}, 2}, Circle{Pt{3, 4}, 2}, 1},
		{Circle{Pt{0, 0}, 3}, Circle{Pt{3, 4}, 2}, 0}, // touching
		{Seg{Pt{0, 0}, Pt{100, 0}, 5}, Circle{Pt{50, 20}, 5}, 10},
		{Seg{Pt{0, 0}, Pt{100, 0}, 0}, Seg{Pt{50, -10}, Pt{50, 10}, 0}, 0},  // crossing
		{Seg{Pt{0, 0}, Pt{100, 100}, 0}, Seg{Pt{0, 10}, Pt{90, 100}, 0}, 7}, // parallel 45°: 10/√2
		{sq, Circle{Pt{50, 50}, 1}, 0},                                      // inside
		{sq, Circle{Pt{103, 50}, 1}, 2},                                     // Max is exclusive
		{sq, Seg{Pt{-50, 200}, Pt{200, -50}, 0}, 0},                         // cuts the corner
		{sq, Poly{[]Pt{{200, 0}, {300, 0}, {250, 100}}}, 100},
		{Poly{[]Pt{{0, 0}, {1000, 0}, {0, 1000}}}, Rect{100, 100, 200, 200}, 0}, // overlap, no vertex of the triangle inside
		{Poly{}, sq, math.MaxInt64},
	}
	for _, c := range cases {
		if got := Dist(c.a, c.b); got != c.want {
			t.Errorf("Dist(%v, %v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if Within(Circle{Pt{0, 0}, 3}, Circle{Pt{3, 4}, 2}, 0) {
		t.Error("Within with r = 0 must be false")
	}
	if !Within(Circle{Pt{0, 0}, 3}, Circle{Pt{3, 4}, 2}, 1) {
		t.Error("touching shapes are within any positive r")
	}
}

func TestOverflowAssertion(t *testing.T) {
	big := int64(1) << 31
	for _, s := range []Shape{
		Circle{C: Pt{big, 0}},
		Seg{A: Pt{0, -big}, B: Pt{0, 0}},
		Seg{B: Pt{1, 1}, HalfW: big},
		Rect{0, 0, big, 10},
		Poly{Pts: []Pt{{0, 0}, {big, 0}, {0, 1}}},
		Circle{R: -1},
	} {
		if Check(s) == nil {
			t.Errorf("Check(%v) accepted an out-of-range shape", s)
		}
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Dist(%v, ...) did not panic", s)
				}
			}()
			Dist(s, Circle{})
		}()
	}
	if err := Check(Seg{A: Pt{-MaxCoord, MaxCoord}, B: Pt{MaxCoord, -MaxCoord}, HalfW: MaxCoord}); err != nil {
		t.Errorf("Check rejected an in-range shape: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Error("NewIndex accepted an out-of-range shape")
		}
	}()
	NewIndex([]Entry{{ID: 1, Shape: Circle{C: Pt{0, big}}}}, Rect{}, 1000)
}

// Out-of-range offsets panic instead of giving a wrong hull or a silent miss.
func TestOffsetAssertions(t *testing.T) {
	ix := NewIndex(nil, Rect{0, 0, 1000, 1000}, 100)
	for name, f := range map[string]func(){
		"Octagon d < 0":    func() { Octagon(Circle{R: 10}, -100) },
		"Octagon r > Max":  func() { Octagon(Circle{R: MaxCoord}, 1) },
		"Collides r > Max": func() { ix.Collides(Circle{}, 0, 1, MaxCoord+1) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			f()
		}()
	}
}
