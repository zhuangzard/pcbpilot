package app

// kicad_rules_padgeom.go — pad-to-pad copper distance for the
// footprint-pad-clearance check, from the snapshot's pad shape (kind,
// size, absolute rotation): a circle is a point with a radius, an oval a
// segment with a radius, a rectangle (also KiCad's rounded / chamfered
// rectangles, which lie inside it: the distance is never overstated) a
// rotated rectangle. Other shapes (custom polygons) are their axis-aligned
// bounding box: a lower bound of the distance, and not exact.

import "math"

type padShape struct {
	pts   [][2]float64 // convex core: 1 point, 2 (segment) or 4 (rectangle)
	r     float64      // radius around the core
	exact bool         // the shape is modelled (not a bounding box)
}

func shapeNum(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok && f > 0 && !math.IsNaN(f) && !math.IsInf(f, 0)
}

// padShapeOf builds the shape of a pad at (x, y) (mil, y up) with its
// bounding box w × h, rotation rot (degrees, counter-clockwise) and
// snapshot shape [kind, sizeX, sizeY].
func padShapeOf(x, y, w, h, rot float64, shape []any) padShape {
	box := func() padShape {
		return padShape{pts: [][2]float64{{x - w/2, y - h/2}, {x + w/2, y - h/2}, {x + w/2, y + h/2}, {x - w/2, y + h/2}}}
	}
	if len(shape) < 3 {
		return box()
	}
	kind, _ := shape[0].(string)
	sw, ok1 := shapeNum(shape[1])
	sh, ok2 := shapeNum(shape[2])
	if !ok1 || !ok2 {
		return box()
	}
	s, c := math.Sincos(rot * math.Pi / 180)
	at := func(lx, ly float64) [2]float64 { return [2]float64{x + lx*c - ly*s, y + lx*s + ly*c} }
	switch kind {
	case "ELLIPSE", "OVAL":
		if math.Abs(sw-sh) < 1e-6 {
			return padShape{pts: [][2]float64{{x, y}}, r: sw / 2, exact: true}
		}
		if sw > sh {
			half := (sw - sh) / 2
			return padShape{pts: [][2]float64{at(-half, 0), at(half, 0)}, r: sh / 2, exact: kind == "OVAL"}
		}
		half := (sh - sw) / 2
		// A true ellipse lies inside the stadium of the same axes, so the
		// stadium distance is a lower bound: exact only for an oval.
		return padShape{pts: [][2]float64{at(0, -half), at(0, half)}, r: sw / 2, exact: kind == "OVAL"}
	case "RECT":
		return padShape{pts: [][2]float64{at(-sw/2, -sh/2), at(sw/2, -sh/2), at(sw/2, sh/2), at(-sw/2, sh/2)}, exact: true}
	}
	return box()
}

// padShapeGap is the copper gap of two pad shapes (mil, ≤ 0 = touching or
// overlapping) and whether both shapes are modelled.
func padShapeGap(a, b padShape) (float64, bool) {
	return convexDist(a.pts, b.pts) - a.r - b.r, a.exact && b.exact
}

// convexDist is the distance between two convex point sets (1, 2 or 4
// points in order); 0 when they intersect.
func convexDist(a, b [][2]float64) float64 {
	if len(a) >= 3 && pointInConvex(b[0], a) || len(b) >= 3 && pointInConvex(a[0], b) {
		return 0
	}
	edges := func(p [][2]float64) [][2][2]float64 {
		switch len(p) {
		case 1:
			return [][2][2]float64{{p[0], p[0]}}
		case 2:
			return [][2][2]float64{{p[0], p[1]}}
		}
		out := make([][2][2]float64, len(p))
		for i := range p {
			out[i] = [2][2]float64{p[i], p[(i+1)%len(p)]}
		}
		return out
	}
	d := math.Inf(1)
	for _, ea := range edges(a) {
		for _, eb := range edges(b) {
			d = math.Min(d, padSegSegDist(ea[0], ea[1], eb[0], eb[1]))
		}
	}
	return d
}

func pointInConvex(p [2]float64, poly [][2]float64) bool {
	sign := 0.0
	for i := range poly {
		a, b := poly[i], poly[(i+1)%len(poly)]
		cr := (b[0]-a[0])*(p[1]-a[1]) - (b[1]-a[1])*(p[0]-a[0])
		if cr != 0 {
			if sign != 0 && cr*sign < 0 {
				return false
			}
			sign = cr
		}
	}
	return true
}

func padPointSegDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l := dx*dx + dy*dy
	t := 0.0
	if l > 0 {
		t = math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l))
	}
	return math.Hypot(p[0]-a[0]-t*dx, p[1]-a[1]-t*dy)
}

func padSegSegDist(a, b, c, d [2]float64) float64 {
	cross := func(o, p, q [2]float64) float64 { return (p[0]-o[0])*(q[1]-o[1]) - (p[1]-o[1])*(q[0]-o[0]) }
	d1, d2 := cross(c, d, a), cross(c, d, b)
	d3, d4 := cross(a, b, c), cross(a, b, d)
	if (d1 > 0 && d2 < 0 || d1 < 0 && d2 > 0) && (d3 > 0 && d4 < 0 || d3 < 0 && d4 > 0) {
		return 0
	}
	return math.Min(math.Min(padPointSegDist(a, c, d), padPointSegDist(b, c, d)), math.Min(padPointSegDist(c, a, b), padPointSegDist(d, a, b)))
}
