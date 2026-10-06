package tile

import (
	"math"
	"math/big"
	"slices"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// Conservative rectilinear inflation (spec 01 §2.2, §2.4). Cover returns
// rectangles whose union holds every lattice point p with geom.Dist(p, s) < r,
// the points where a centreline would violate the clearance. A point at
// exactly r stays free, matching geom.Within.
//
//   - A box core (Rect, a horizontal or vertical Seg, a Circle, an axis-aligned
//     rectangular Poly) is exact per band: a middle band over the core's rows
//     and corners bands above and below, each as wide as the rounded shape at
//     the band's row nearest the core.
//   - Any other shape is covered by geom.Octagon(s, r) cut into bands of
//     height step aligned to multiples of step, so a finer step nests in the
//     coarser one. Each band spans the octagon's widest x range over its rows.

// Cover is the staircase of s grown by r with the given step and corner
// steps. It returns nil when r ≤ 0 (nothing is closer than 0) or s is empty.
func Cover(s geom.Shape, r, step int64, corners int) []geom.Rect {
	if r <= 0 || s.Bounds().Empty() {
		return nil
	}
	switch s := s.(type) {
	case geom.Rect:
		return roundBox(s.MinX, s.MinY, s.MaxX-1, s.MaxY-1, r, corners)
	case geom.Circle:
		return roundBox(s.C.X, s.C.Y, s.C.X, s.C.Y, s.R+r, corners)
	case geom.Seg:
		if s.A.X == s.B.X || s.A.Y == s.B.Y {
			return roundBox(min(s.A.X, s.B.X), min(s.A.Y, s.B.Y), max(s.A.X, s.B.X), max(s.A.Y, s.B.Y), s.HalfW+r, corners)
		}
	case geom.Poly:
		if b, ok := boxPoly(s); ok {
			return roundBox(b.MinX, b.MinY, b.MaxX-1, b.MaxY-1, r, corners)
		}
	}
	return octBands(geom.Octagon(s, r), step)
}

// roundBox covers the points closer than R to the closed box [x0,x1]×[y0,y1].
func roundBox(x0, y0, x1, y1, R int64, corners int) []geom.Rect {
	corners = max(corners, 1)
	// halfW(d) is the largest dx with dx² + d² < R².
	halfW := func(d int64) int64 { return isqrt(R*R - d*d - 1) }
	// Band i holds the distances [a_i, b_i] from the core, a_0 = 0.
	cut := func(i int) int64 { return ((R-1)*int64(i+1) + int64(corners) - 1) / int64(corners) }
	b0 := cut(0)
	w := halfW(0)
	out := []geom.Rect{{MinX: x0 - w, MinY: y0 - b0, MaxX: x1 + w + 1, MaxY: y1 + b0 + 1}}
	for i, a := 1, b0+1; i < corners && a <= R-1; i++ {
		b := cut(i)
		w := halfW(a)
		out = append(out,
			geom.Rect{MinX: x0 - w, MinY: y1 + a, MaxX: x1 + w + 1, MaxY: y1 + b + 1},
			geom.Rect{MinX: x0 - w, MinY: y0 - b, MaxX: x1 + w + 1, MaxY: y0 - a + 1})
		a = b + 1
	}
	return out
}

// isqrt is ⌊√n⌋ for n ≥ 0 (0 for n < 0).
func isqrt(n int64) int64 {
	if n <= 0 {
		return 0
	}
	k := int64(math.Sqrt(float64(n)))
	for k*k > n {
		k--
	}
	for (k+1)*(k+1) <= n {
		k++
	}
	return k
}

// boxPoly reports whether p is an axis-aligned rectangle and returns its
// half-open bounds.
func boxPoly(p geom.Poly) (geom.Rect, bool) {
	if len(p.Pts) != 4 {
		return geom.Rect{}, false
	}
	b := p.Bounds()
	for _, q := range p.Pts {
		if (q.X != b.MinX && q.X != b.MaxX-1) || (q.Y != b.MinY && q.Y != b.MaxY-1) {
			return geom.Rect{}, false
		}
	}
	for i := range p.Pts {
		a, c := p.Pts[i], p.Pts[(i+1)%4]
		if a.X != c.X && a.Y != c.Y {
			return geom.Rect{}, false
		}
	}
	return b, true
}

// octBands cuts the closed convex octagon o into bands of height step.
func octBands(o geom.Poly, step int64) []geom.Rect {
	if len(o.Pts) == 0 {
		return nil
	}
	q := o.Pts[0]
	x0, x1, y0, y1 := q.X, q.X, q.Y, q.Y
	s0, s1, d0, d1 := q.X+q.Y, q.X+q.Y, q.X-q.Y, q.X-q.Y
	for _, q := range o.Pts[1:] {
		x0, x1, y0, y1 = min(x0, q.X), max(x1, q.X), min(y0, q.Y), max(y1, q.Y)
		s0, s1 = min(s0, q.X+q.Y), max(s1, q.X+q.Y)
		d0, d1 = min(d0, q.X-q.Y), max(d1, q.X-q.Y)
	}
	var out []geom.Rect
	for ya := y0; ya <= y1; {
		yb := min(floorDiv(ya, step)*step+step-1, y1)
		lo := max(x0, s0-yb, d0+ya)
		hi := min(x1, s1-ya, d1+yb)
		if lo <= hi {
			out = append(out, geom.Rect{MinX: lo, MinY: ya, MaxX: hi + 1, MaxY: yb + 1})
		}
		ya = yb + 1
	}
	return out
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// circleSides is the number of sides of the polygon inscribed in a round
// outline; the inside it loses is at most C.R·(1-cos(π/64)), about 0.12 %.
const circleSides = 64

// Outline covers what a centreline of clearance R may not reach around the
// board outline s (a Poly, Rect or Circle): every lattice point closer than R
// to the outline and everything outside it, within bounds. Between two
// successive vertex rows, the inside is shrunk to the columns that are inside
// on every row of the band, so the cover is conservative. A Circle is
// replaced by an inscribed polygon: a point inside it that is closer than R
// to the circle is also closer than R to the polygon, so that stays
// conservative too.
func Outline(s geom.Shape, R, step int64, bounds geom.Rect) []geom.Rect {
	var pts []geom.Pt
	switch s := s.(type) {
	case geom.Poly:
		pts = s.Pts
	case geom.Circle:
		pts = inscribed(s)
	case geom.Rect:
		if s.Empty() {
			return []geom.Rect{bounds}
		}
		pts = []geom.Pt{{X: s.MinX, Y: s.MinY}, {X: s.MaxX - 1, Y: s.MinY}, {X: s.MaxX - 1, Y: s.MaxY - 1}, {X: s.MinX, Y: s.MaxY - 1}}
	default:
		panic("tile: outline must be a Poly, a Rect or a Circle")
	}
	if len(pts) < 3 {
		return []geom.Rect{bounds}
	}
	var out []geom.Rect
	for i := range pts {
		out = append(out, Cover(geom.Seg{A: pts[i], B: pts[(i+1)%len(pts)]}, R, step, DefaultCornerSteps)...)
	}
	ys := make([]int64, 0, len(pts))
	for _, q := range pts {
		ys = append(ys, q.Y)
	}
	slices.Sort(ys)
	ys = slices.Compact(ys)
	full := func(ya, yb int64) { // rows [ya, yb) are outside
		if ya < yb {
			out = append(out, geom.Rect{MinX: bounds.MinX, MinY: ya, MaxX: bounds.MaxX, MaxY: yb})
		}
	}
	full(bounds.MinY, ys[0])
	full(ys[len(ys)-1]+1, bounds.MaxY)
	for k := 0; k+1 < len(ys); k++ {
		ya, yb := ys[k], ys[k+1]
		type cross struct {
			a, b geom.Pt // the edge, a.Y < b.Y
		}
		var cs []cross
		for i := range pts {
			a, b := pts[i], pts[(i+1)%len(pts)]
			if a.Y > b.Y {
				a, b = b, a
			}
			if a.Y <= ya && b.Y >= yb && a.Y < b.Y {
				cs = append(cs, cross{a, b})
			}
		}
		// Edges do not cross inside the band: order them by x at mid-band.
		mid := func(c cross) *big.Rat {
			// x(y) = a.X + (b.X-a.X)(y-a.Y)/(b.Y-a.Y) at y = (ya+yb)/2.
			num := big.NewInt(c.b.X - c.a.X)
			num.Mul(num, big.NewInt(ya+yb-2*c.a.Y))
			r := new(big.Rat).SetFrac(num, big.NewInt(2*(c.b.Y-c.a.Y)))
			return r.Add(r, new(big.Rat).SetInt64(c.a.X))
		}
		slices.SortStableFunc(cs, func(p, q cross) int { return mid(p).Cmp(mid(q)) })
		// xAt rounds x(y) toward the inside: up for a left edge, down for a right one.
		xAt := func(c cross, y int64, up bool) int64 {
			n := (c.b.X - c.a.X) * (y - c.a.Y)
			d := c.b.Y - c.a.Y
			q := floorDiv(n, d)
			if up && q*d != n {
				q++
			}
			return c.a.X + q
		}
		x := bounds.MinX
		for i := 0; i+1 < len(cs); i += 2 {
			lo := max(xAt(cs[i], ya, true), xAt(cs[i], yb, true))
			hi := min(xAt(cs[i+1], ya, false), xAt(cs[i+1], yb, false))
			if lo > hi {
				continue
			}
			if x < lo {
				out = append(out, geom.Rect{MinX: x, MinY: ya, MaxX: lo, MaxY: yb + 1})
			}
			x = hi + 1
		}
		if x < bounds.MaxX {
			out = append(out, geom.Rect{MinX: x, MinY: ya, MaxX: bounds.MaxX, MaxY: yb + 1})
		}
	}
	return out
}

// inscribed is a polygon with integer vertices inside the closed disk c.
func inscribed(c geom.Circle) []geom.Pt {
	var pts []geom.Pt
	for i := range circleSides {
		a := 2 * math.Pi * float64(i) / circleSides
		dx, dy := int64(float64(c.R)*math.Cos(a)), int64(float64(c.R)*math.Sin(a))
		for dx*dx+dy*dy > c.R*c.R { // float rounding: pull the vertex in
			if abs(dx) >= abs(dy) {
				dx -= sign(dx)
			} else {
				dy -= sign(dy)
			}
		}
		q := geom.Pt{X: c.C.X + dx, Y: c.C.Y + dy}
		if len(pts) == 0 || pts[len(pts)-1] != q {
			pts = append(pts, q)
		}
	}
	if len(pts) > 1 && pts[0] == pts[len(pts)-1] {
		pts = pts[:len(pts)-1]
	}
	return pts
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func sign(v int64) int64 {
	if v < 0 {
		return -1
	}
	return 1
}
