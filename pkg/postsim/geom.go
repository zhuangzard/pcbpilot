package postsim

import (
	"math"
	"strings"
)

// Geometry helpers. Board units are mil, y-up (the `pcb dump` convention).

// Point is a board coordinate in mil.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Rect is an axis-aligned box in mil.
type Rect struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
}

func emptyRect() Rect {
	return Rect{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
}

func (r *Rect) add(p Point) {
	r.MinX, r.MinY = math.Min(r.MinX, p.X), math.Min(r.MinY, p.Y)
	r.MaxX, r.MaxY = math.Max(r.MaxX, p.X), math.Max(r.MaxY, p.Y)
}

func (r Rect) valid() bool { return r.MaxX > r.MinX && r.MaxY > r.MinY }

func (r Rect) expand(d float64) Rect {
	return Rect{r.MinX - d, r.MinY - d, r.MaxX + d, r.MaxY + d}
}

func polyBounds(contours ...[]Point) Rect {
	r := emptyRect()
	for _, c := range contours {
		for _, p := range c {
			r.add(p)
		}
	}
	return r
}

// arcPoints flattens an EasyEDA "ARC" command: from p0 to p1 sweeping deg
// degrees, counter-clockwise positive in the y-up frame (verified on a pour
// clearance corner of the ESP32 board: L …(409.25,1551.03) ARC 90 → (413.35,
// 1546.93) wraps the obstacle corner (413.35,1551.03)). Returns the points
// after p0, ending exactly at p1.
func arcPoints(p0, p1 Point, deg float64) []Point {
	dx, dy := p1.X-p0.X, p1.Y-p0.Y
	c := math.Hypot(dx, dy)
	th := deg * math.Pi / 180
	if c < 1e-9 || math.Abs(th) < 1e-6 || math.Abs(math.Abs(th)-2*math.Pi) < 1e-9 {
		return []Point{p1}
	}
	r := c / (2 * math.Sin(math.Abs(th)/2))
	sgn := 1.0
	if th < 0 {
		sgn = -1
	}
	// Left normal of the chord; a CCW sweep below 180° has its centre on the left.
	nx, ny := -dy/c, dx/c
	off := sgn * r * math.Cos(math.Abs(th)/2)
	cx, cy := (p0.X+p1.X)/2+nx*off, (p0.Y+p1.Y)/2+ny*off
	a0 := math.Atan2(p0.Y-cy, p0.X-cx)
	// ≤ 10° per chord and ≤ 2 mil sagitta.
	n := int(math.Ceil(math.Abs(th) / (10 * math.Pi / 180)))
	if s := int(math.Ceil(math.Abs(th) * r / 20)); s > n {
		n = s
	}
	if n < 1 {
		n = 1
	}
	if n > 180 {
		n = 180
	}
	out := make([]Point, 0, n)
	for i := 1; i < n; i++ {
		a := a0 + th*float64(i)/float64(n)
		out = append(out, Point{cx + r*math.Cos(a), cy + r*math.Sin(a)})
	}
	return append(out, p1)
}

// parseSource reads an EasyEDA polygon source into flattened contours.
// Forms: [x,y,"L",x,y,…,"ARC",deg,x,y,…] (one contour), a list of such lists
// (complex polygon: outer + holes, even-odd), ["R",x,y,w,h,…] and
// ["CIRCLE",cx,cy,r].
func parseSource(v any) [][]Point {
	var out [][]Point
	for _, c := range parsePaths(v) {
		if len(c) >= 3 {
			out = append(out, c)
		}
	}
	return out
}

// parsePaths is parseSource without the closed-polygon filter: a stroked
// path (thermal spoke) has two points.
func parsePaths(v any) [][]Point {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	if _, nested := arr[0].([]any); nested {
		var out [][]Point
		for _, c := range arr {
			out = append(out, parsePaths(c)...)
		}
		return out
	}
	if s, ok := arr[0].(string); ok {
		switch strings.ToUpper(s) {
		case "CIRCLE":
			if len(arr) >= 4 {
				c := Point{num(arr[1]), num(arr[2])}
				return [][]Point{circlePoly(c, num(arr[3]), 48)}
			}
		case "R":
			if len(arr) >= 5 {
				x, y, w, h := num(arr[1]), num(arr[2]), num(arr[3]), num(arr[4])
				// EasyEDA "R": top-left corner in y-up means (x, y) and extends down.
				return [][]Point{{{x, y}, {x + w, y}, {x + w, y - h}, {x, y - h}}}
			}
		}
		return nil
	}
	var pts []Point
	mode := "L"
	var nums []float64
	flush := func() {
		switch mode {
		case "L":
			for i := 0; i+1 < len(nums); i += 2 {
				pts = append(pts, Point{nums[i], nums[i+1]})
			}
		case "ARC", "CARC":
			for i := 0; i+2 < len(nums); i += 3 {
				p1 := Point{nums[i+1], nums[i+2]}
				if len(pts) == 0 {
					pts = append(pts, p1)
					continue
				}
				pts = append(pts, arcPoints(pts[len(pts)-1], p1, nums[i])...)
			}
		}
		nums = nums[:0]
	}
	// The first two numbers are the start point.
	i := 0
	if len(arr) >= 2 {
		if x, okx := toNum(arr[0]); okx {
			if y, oky := toNum(arr[1]); oky {
				pts = append(pts, Point{x, y})
				i = 2
			}
		}
	}
	for ; i < len(arr); i++ {
		switch t := arr[i].(type) {
		case string:
			flush()
			mode = strings.ToUpper(t)
		default:
			if f, ok := toNum(t); ok {
				nums = append(nums, f)
			}
		}
	}
	flush()
	// Drop a closing duplicate.
	if n := len(pts); n > 2 && math.Hypot(pts[0].X-pts[n-1].X, pts[0].Y-pts[n-1].Y) < 1e-6 {
		pts = pts[:n-1]
	}
	if len(pts) < 2 {
		return nil
	}
	return [][]Point{pts}
}

func circlePoly(c Point, r float64, n int) []Point {
	out := make([]Point, n)
	for i := range out {
		a := 2 * math.Pi * float64(i) / float64(n)
		out[i] = Point{c.X + r*math.Cos(a), c.Y + r*math.Sin(a)}
	}
	return out
}

func toNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	}
	return 0, false
}

func num(v any) float64 {
	f, _ := toNum(v)
	return f
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// insideEvenOdd is the even-odd point-in-polygon test over all contours
// (holes of a complex polygon are separate contours).
func insideEvenOdd(p Point, contours [][]Point) bool {
	in := false
	for _, c := range contours {
		n := len(c)
		for i, j := 0, n-1; i < n; j, i = i, i+1 {
			a, b := c[i], c[j]
			if (a.Y > p.Y) != (b.Y > p.Y) {
				x := a.X + (p.Y-a.Y)*(b.X-a.X)/(b.Y-a.Y)
				if p.X < x {
					in = !in
				}
			}
		}
	}
	return in
}

// segDist is the distance from p to segment ab.
func segDist(p, a, b Point) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	l2 := dx*dx + dy*dy
	if l2 < 1e-12 {
		return math.Hypot(p.X-a.X, p.Y-a.Y)
	}
	t := ((p.X-a.X)*dx + (p.Y-a.Y)*dy) / l2
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(p.X-(a.X+t*dx), p.Y-(a.Y+t*dy))
}

// segParam returns the projection parameter of p on ab (unclamped).
func segParam(p, a, b Point) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	l2 := dx*dx + dy*dy
	if l2 < 1e-12 {
		return 0
	}
	return ((p.X-a.X)*dx + (p.Y-a.Y)*dy) / l2
}

func dist(a, b Point) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }

func lerp(a, b Point, t float64) Point { return Point{a.X + (b.X-a.X)*t, a.Y + (b.Y-a.Y)*t} }

// touchEps (mil) is the overlap below which copper counts as touching.
const touchEps = 1e-3

// segSegDist is the distance between segments ab and cd (0 when they cross).
func segSegDist(a, b, c, d Point) float64 {
	if segsCross(a, b, c, d) {
		return 0
	}
	return math.Min(math.Min(segDist(a, c, d), segDist(b, c, d)), math.Min(segDist(c, a, b), segDist(d, a, b)))
}

// closestPair returns the closest points of segments ab and cd.
func closestPair(a, b, c, d Point) (Point, Point) {
	if segsCross(a, b, c, d) {
		p := crossPoint(a, b, c, d)
		return p, p
	}
	proj := func(p, s, e Point) Point {
		return lerp(s, e, math.Max(0, math.Min(1, segParam(p, s, e))))
	}
	type cand struct{ p, q Point }
	cs := []cand{{a, proj(a, c, d)}, {b, proj(b, c, d)}, {proj(c, a, b), c}, {proj(d, a, b), d}}
	best := cs[0]
	for _, x := range cs[1:] {
		if dist(x.p, x.q) < dist(best.p, best.q) {
			best = x
		}
	}
	return best.p, best.q
}

func cross2(o, a, b Point) float64 { return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X) }

// segsCross reports a proper or touching intersection of ab and cd.
func segsCross(a, b, c, d Point) bool {
	d1, d2 := cross2(c, d, a), cross2(c, d, b)
	d3, d4 := cross2(a, b, c), cross2(a, b, d)
	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}
	return (d1 == 0 && segDist(a, c, d) < 1e-9) || (d2 == 0 && segDist(b, c, d) < 1e-9) ||
		(d3 == 0 && segDist(c, a, b) < 1e-9) || (d4 == 0 && segDist(d, a, b) < 1e-9)
}

func crossPoint(a, b, c, d Point) Point {
	den := (b.X-a.X)*(d.Y-c.Y) - (b.Y-a.Y)*(d.X-c.X)
	if math.Abs(den) < 1e-12 {
		return a
	}
	t := ((c.X-a.X)*(d.Y-c.Y) - (c.Y-a.Y)*(d.X-c.X)) / den
	return lerp(a, b, t)
}
