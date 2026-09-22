package pcbauto

import "math"

// All geometry in this package uses mil with +y pointing up, the EasyEDA PCB
// convention. Callers converting from DSN or mm must do so before entering.

// Point is a position in mil.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

func (p Point) Add(q Point) Point         { return Point{p.X + q.X, p.Y + q.Y} }
func (p Point) Sub(q Point) Point         { return Point{p.X - q.X, p.Y - q.Y} }
func (p Point) Scale(k float64) Point     { return Point{p.X * k, p.Y * k} }
func (p Point) Dist(q Point) float64      { return math.Hypot(p.X-q.X, p.Y-q.Y) }
func (p Point) Manhattan(q Point) float64 { return math.Abs(p.X-q.X) + math.Abs(p.Y-q.Y) }

// Rotate turns p about the origin by deg degrees counter-clockwise.
func (p Point) Rotate(deg float64) Point {
	if deg == 0 {
		return p
	}
	s, c := math.Sincos(deg * math.Pi / 180)
	return Point{p.X*c - p.Y*s, p.X*s + p.Y*c}
}

// Rect is an axis-aligned rectangle.
type Rect struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
}

// EmptyRect is the identity for Union.
func EmptyRect() Rect {
	return Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
}

func (r Rect) Empty() bool   { return r.MinX > r.MaxX || r.MinY > r.MaxY }
func (r Rect) W() float64    { return r.MaxX - r.MinX }
func (r Rect) H() float64    { return r.MaxY - r.MinY }
func (r Rect) Area() float64 { return math.Max(0, r.W()) * math.Max(0, r.H()) }
func (r Rect) Center() Point { return Point{(r.MinX + r.MaxX) / 2, (r.MinY + r.MaxY) / 2} }
func (r Rect) Contains(p Point) bool {
	return p.X >= r.MinX && p.X <= r.MaxX && p.Y >= r.MinY && p.Y <= r.MaxY
}

func (r Rect) Expand(d float64) Rect {
	return Rect{r.MinX - d, r.MinY - d, r.MaxX + d, r.MaxY + d}
}

func (r Rect) AddPoint(p Point) Rect {
	return Rect{math.Min(r.MinX, p.X), math.Min(r.MinY, p.Y), math.Max(r.MaxX, p.X), math.Max(r.MaxY, p.Y)}
}

func (r Rect) Union(o Rect) Rect {
	if o.Empty() {
		return r
	}
	if r.Empty() {
		return o
	}
	return Rect{math.Min(r.MinX, o.MinX), math.Min(r.MinY, o.MinY), math.Max(r.MaxX, o.MaxX), math.Max(r.MaxY, o.MaxY)}
}

func (r Rect) Overlaps(o Rect) bool {
	return r.MinX < o.MaxX && o.MinX < r.MaxX && r.MinY < o.MaxY && o.MinY < r.MaxY
}

// OverlapArea is the intersection area (0 when disjoint).
func (r Rect) OverlapArea(o Rect) float64 {
	w := math.Min(r.MaxX, o.MaxX) - math.Max(r.MinX, o.MinX)
	h := math.Min(r.MaxY, o.MaxY) - math.Max(r.MinY, o.MinY)
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

// Translate moves the rect by d.
func (r Rect) Translate(d Point) Rect {
	return Rect{r.MinX + d.X, r.MinY + d.Y, r.MaxX + d.X, r.MaxY + d.Y}
}

// Corners returns the rect's corners counter-clockwise.
func (r Rect) Corners() []Point {
	return []Point{{r.MinX, r.MinY}, {r.MaxX, r.MinY}, {r.MaxX, r.MaxY}, {r.MinX, r.MaxY}}
}

// PolyBounds returns the bounding rect of a polygon.
func PolyBounds(poly []Point) Rect {
	r := EmptyRect()
	for _, p := range poly {
		r = r.AddPoint(p)
	}
	return r
}

// PolyContains is an even-odd point-in-polygon test.
func PolyContains(poly []Point, p Point) bool {
	in := false
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a.Y > p.Y) != (b.Y > p.Y) {
			x := (b.X-a.X)*(p.Y-a.Y)/(b.Y-a.Y) + a.X
			if p.X < x {
				in = !in
			}
		}
	}
	return in
}

// PolyArea is the absolute shoelace area.
func PolyArea(poly []Point) float64 {
	s := 0.0
	n := len(poly)
	for i := 0; i < n; i++ {
		a, b := poly[i], poly[(i+1)%n]
		s += a.X*b.Y - b.X*a.Y
	}
	return math.Abs(s) / 2
}

// PointSegDist is the distance from p to segment ab.
func PointSegDist(p, a, b Point) float64 {
	d := b.Sub(a)
	l2 := d.X*d.X + d.Y*d.Y
	if l2 == 0 {
		return p.Dist(a)
	}
	t := ((p.X-a.X)*d.X + (p.Y-a.Y)*d.Y) / l2
	t = math.Max(0, math.Min(1, t))
	return p.Dist(a.Add(d.Scale(t)))
}

// PolyEdgeDist is the distance from p to the polygon boundary.
func PolyEdgeDist(poly []Point, p Point) float64 {
	best := math.Inf(1)
	n := len(poly)
	for i := 0; i < n; i++ {
		best = math.Min(best, PointSegDist(p, poly[i], poly[(i+1)%n]))
	}
	return best
}

func segsIntersect(a, b, c, d Point) bool {
	cross := func(o, p, q Point) float64 { return (p.X-o.X)*(q.Y-o.Y) - (p.Y-o.Y)*(q.X-o.X) }
	d1, d2 := cross(c, d, a), cross(c, d, b)
	d3, d4 := cross(a, b, c), cross(a, b, d)
	return ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0))
}

// SegSegDist is the minimum distance between segments ab and cd.
func SegSegDist(a, b, c, d Point) float64 {
	if segsIntersect(a, b, c, d) {
		return 0
	}
	return math.Min(math.Min(PointSegDist(a, c, d), PointSegDist(b, c, d)),
		math.Min(PointSegDist(c, a, b), PointSegDist(d, a, b)))
}

// OrientedBox is a rectangle of size W×H centred at C, rotated by Rot degrees.
// Pads and part bodies use it; Round marks a stadium/circle whose corner radius
// is min(W,H)/2.
type OrientedBox struct {
	C     Point   `json:"c"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Rot   float64 `json:"rot"`
	Round bool    `json:"round,omitempty"`
}

// Dist returns the distance from p to the box copper (0 inside).
func (b OrientedBox) Dist(p Point) float64 {
	q := p.Sub(b.C).Rotate(-b.Rot)
	hw, hh := b.W/2, b.H/2
	if b.Round {
		// Stadium: a segment core swept by radius r.
		r := math.Min(hw, hh)
		ax, ay := hw-r, hh-r
		dx := math.Max(math.Abs(q.X)-ax, 0)
		dy := math.Max(math.Abs(q.Y)-ay, 0)
		return math.Max(math.Hypot(dx, dy)-r, 0)
	}
	dx := math.Max(math.Abs(q.X)-hw, 0)
	dy := math.Max(math.Abs(q.Y)-hh, 0)
	return math.Hypot(dx, dy)
}

// SegDist returns the distance from segment ab to the box copper.
func (b OrientedBox) SegDist(a, c Point) float64 {
	// Transform into box frame; then segment vs axis-aligned rect (or stadium).
	qa := a.Sub(b.C).Rotate(-b.Rot)
	qc := c.Sub(b.C).Rotate(-b.Rot)
	hw, hh := b.W/2, b.H/2
	if b.Round {
		r := math.Min(hw, hh)
		ax, ay := hw-r, hh-r
		var core0, core1 Point
		if ax >= ay {
			core0, core1 = Point{-ax, 0}, Point{ax, 0}
		} else {
			core0, core1 = Point{0, -ay}, Point{0, ay}
		}
		return math.Max(SegSegDist(qa, qc, core0, core1)-r, 0)
	}
	r := Rect{-hw, -hh, hw, hh}
	if r.Contains(qa) || r.Contains(qc) {
		return 0
	}
	cs := r.Corners()
	best := math.Inf(1)
	for i := 0; i < 4; i++ {
		best = math.Min(best, SegSegDist(qa, qc, cs[i], cs[(i+1)%4]))
	}
	return best
}

// Bounds returns the axis-aligned bounds of the box.
func (b OrientedBox) Bounds() Rect {
	r := EmptyRect()
	for _, c := range (Rect{-b.W / 2, -b.H / 2, b.W / 2, b.H / 2}).Corners() {
		r = r.AddPoint(b.C.Add(c.Rotate(b.Rot)))
	}
	return r
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func roundUpTo(v, step float64) float64 {
	if step <= 0 {
		return v
	}
	return math.Ceil(v/step-1e-9) * step
}
