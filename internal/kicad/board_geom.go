package kicad

// board_geom.go — exact containment and overlap tests for kicad board
// shapes. Every shape is its true boundary (segments and circular arcs, no
// sampling) plus an exact signed distance; two shapes "touch" when their
// boundaries come within BoardTol. board.py ports the same formulas for the
// checks against the board's own outline.

import "math"

// BoardTol is the distance (mm) at or below which two boundaries count as
// touching: a cutout must stay at least this far inside the outline and
// away from other cutouts.
const BoardTol = 0.001

// bdEdge is one boundary piece: a segment A→B, or an arc around C of
// radius R from angle A0 sweeping SW radians (KiCad frame; |SW| ≥ 2π is a
// full circle).
type bdEdge struct {
	Arc       bool
	A, B      XY
	C         XY
	R, A0, SW float64
}

func bdSegEdge(a, b XY) bdEdge { return bdEdge{A: a, B: b} }

func bdArcEdge(c XY, r, a0, sw float64) bdEdge { return bdEdge{Arc: true, C: c, R: r, A0: a0, SW: sw} }

func (e bdEdge) full() bool { return e.Arc && math.Abs(e.SW) >= 2*math.Pi-1e-12 }

func (e bdEdge) ends() []XY {
	if !e.Arc {
		return []XY{e.A, e.B}
	}
	if e.full() {
		return nil
	}
	at := func(a float64) XY { return XY{e.C[0] + e.R*math.Cos(a), e.C[1] + e.R*math.Sin(a)} }
	return []XY{at(e.A0), at(e.A0 + e.SW)}
}

// start is a point on the edge.
func (e bdEdge) start() XY {
	if !e.Arc {
		return e.A
	}
	return XY{e.C[0] + e.R*math.Cos(e.A0), e.C[1] + e.R*math.Sin(e.A0)}
}

// inSpan: the direction of angle t from the centre lies on the arc.
func (e bdEdge) inSpan(t float64) bool {
	if e.full() {
		return true
	}
	d := t - e.A0
	if e.SW < 0 {
		d = -d
	}
	d = math.Mod(d, 2*math.Pi)
	if d < 0 {
		d += 2 * math.Pi
	}
	return d <= math.Abs(e.SW)+1e-12 || d >= 2*math.Pi-1e-12
}

// edges is the exact boundary of a (validated) shape.
func (s BoardShape) edges() []bdEdge {
	switch s.kind() {
	case "rect":
		r := s.Rect
		x0, y0, x1, y1, rad := r.X, r.Y, r.X+r.Width, r.Y+r.Height, r.CornerRadius
		if rad <= 0 {
			return []bdEdge{bdSegEdge(XY{x0, y0}, XY{x1, y0}), bdSegEdge(XY{x1, y0}, XY{x1, y1}),
				bdSegEdge(XY{x1, y1}, XY{x0, y1}), bdSegEdge(XY{x0, y1}, XY{x0, y0})}
		}
		var out []bdEdge
		add := func(a, b XY) {
			if bdDist(a, b) > 1e-9 {
				out = append(out, bdSegEdge(a, b))
			}
		}
		add(XY{x0 + rad, y0}, XY{x1 - rad, y0})
		out = append(out, bdArcEdge(XY{x1 - rad, y0 + rad}, rad, -math.Pi/2, math.Pi/2))
		add(XY{x1, y0 + rad}, XY{x1, y1 - rad})
		out = append(out, bdArcEdge(XY{x1 - rad, y1 - rad}, rad, 0, math.Pi/2))
		add(XY{x1 - rad, y1}, XY{x0 + rad, y1})
		out = append(out, bdArcEdge(XY{x0 + rad, y1 - rad}, rad, math.Pi/2, math.Pi/2))
		add(XY{x0, y1 - rad}, XY{x0, y0 + rad})
		return append(out, bdArcEdge(XY{x0 + rad, y0 + rad}, rad, math.Pi, math.Pi/2))
	case "circle":
		c := s.Circle
		return []bdEdge{bdArcEdge(XY{c.X, c.Y}, c.Diameter/2, 0, 2*math.Pi)}
	case "slot":
		sl := s.Slot
		a, b, r := XY{sl.X1, sl.Y1}, XY{sl.X2, sl.Y2}, sl.Width/2
		if bdDist(a, b) < 1e-9 {
			return []bdEdge{bdArcEdge(a, r, 0, 2*math.Pi)}
		}
		ang := math.Atan2(b[1]-a[1], b[0]-a[0])
		nx, ny := -math.Sin(ang)*r, math.Cos(ang)*r
		return []bdEdge{
			bdSegEdge(XY{a[0] + nx, a[1] + ny}, XY{b[0] + nx, b[1] + ny}),
			bdArcEdge(b, r, ang+math.Pi/2, -math.Pi),
			bdSegEdge(XY{b[0] - nx, b[1] - ny}, XY{a[0] - nx, a[1] - ny}),
			bdArcEdge(a, r, ang-math.Pi/2, -math.Pi),
		}
	case "polygon":
		p := s.Polygon
		out := make([]bdEdge, len(p))
		for i := range p {
			out[i] = bdSegEdge(p[i], p[(i+1)%len(p)])
		}
		return out
	}
	return nil
}

// sdf is the exact signed distance from p to the shape (< 0 inside).
func (s BoardShape) sdf(p XY) float64 {
	switch s.kind() {
	case "circle":
		c := s.Circle
		return bdDist(p, XY{c.X, c.Y}) - c.Diameter/2
	case "slot":
		sl := s.Slot
		return bdPointSegDist(p, XY{sl.X1, sl.Y1}, XY{sl.X2, sl.Y2}) - sl.Width/2
	case "rect":
		r := s.Rect
		rad := r.CornerRadius
		cx, cy := r.X+r.Width/2, r.Y+r.Height/2
		qx := math.Abs(p[0]-cx) - (r.Width/2 - rad)
		qy := math.Abs(p[1]-cy) - (r.Height/2 - rad)
		return math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - rad
	case "polygon":
		d := math.Inf(1)
		for i := range s.Polygon {
			d = math.Min(d, bdPointSegDist(p, s.Polygon[i], s.Polygon[(i+1)%len(s.Polygon)]))
		}
		if bdPointInPoly(s.Polygon, p) {
			return -d
		}
		return d
	}
	return math.Inf(1)
}

func bdPointSegDist(p, a, b XY) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l2 := dx*dx + dy*dy
	if l2 == 0 {
		return bdDist(p, a)
	}
	t := math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l2))
	return bdDist(p, XY{a[0] + t*dx, a[1] + t*dy})
}

func bdPointArcDist(p XY, e bdEdge) float64 {
	d := bdDist(p, e.C)
	if d < 1e-12 {
		return e.R
	}
	if e.inSpan(math.Atan2(p[1]-e.C[1], p[0]-e.C[0])) {
		return math.Abs(d - e.R)
	}
	best := math.Inf(1)
	for _, q := range e.ends() {
		best = math.Min(best, bdDist(p, q))
	}
	return best
}

func bdPointEdgeDist(p XY, e bdEdge) float64 {
	if e.Arc {
		return bdPointArcDist(p, e)
	}
	return bdPointSegDist(p, e.A, e.B)
}

// bdSegArcDist is the exact distance between segment a→b and arc e.
func bdSegArcDist(a, b XY, e bdEdge) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	fx, fy := a[0]-e.C[0], a[1]-e.C[1]
	A, B, C := dx*dx+dy*dy, 2*(fx*dx+fy*dy), fx*fx+fy*fy-e.R*e.R
	if A > 0 {
		if disc := B*B - 4*A*C; disc >= 0 {
			sq := math.Sqrt(disc)
			for _, t := range []float64{(-B - sq) / (2 * A), (-B + sq) / (2 * A)} {
				if t >= -1e-12 && t <= 1+1e-12 {
					x, y := a[0]+t*dx, a[1]+t*dy
					if e.inSpan(math.Atan2(y-e.C[1], x-e.C[0])) {
						return 0
					}
				}
			}
		}
	}
	best := math.Min(bdPointArcDist(a, e), bdPointArcDist(b, e))
	for _, q := range e.ends() {
		best = math.Min(best, bdPointSegDist(q, a, b))
	}
	// The point of the segment nearest the centre, toward the arc.
	t := 0.0
	if A > 0 {
		t = math.Max(0, math.Min(1, -(fx*dx+fy*dy)/A))
	}
	q := XY{a[0] + t*dx, a[1] + t*dy}
	if d := bdDist(q, e.C); d > 1e-12 && e.inSpan(math.Atan2(q[1]-e.C[1], q[0]-e.C[0])) {
		best = math.Min(best, math.Abs(d-e.R))
	}
	return best
}

func bdArcArcDist(e, f bdEdge) float64 {
	best := math.Inf(1)
	for _, q := range e.ends() {
		best = math.Min(best, bdPointArcDist(q, f))
	}
	for _, q := range f.ends() {
		best = math.Min(best, bdPointArcDist(q, e))
	}
	d := bdDist(e.C, f.C)
	if d < 1e-12 { // concentric: |r1 - r2| where the spans overlap
		if e.full() || f.full() {
			return math.Min(best, math.Abs(e.R-f.R))
		}
		for _, q := range e.ends() {
			if f.inSpan(math.Atan2(q[1]-f.C[1], q[0]-f.C[0])) {
				best = math.Min(best, math.Abs(e.R-f.R))
			}
		}
		for _, q := range f.ends() {
			if e.inSpan(math.Atan2(q[1]-e.C[1], q[0]-e.C[0])) {
				best = math.Min(best, math.Abs(e.R-f.R))
			}
		}
		return best
	}
	if d <= e.R+f.R && d >= math.Abs(e.R-f.R) { // the circles cross
		a := (e.R*e.R - f.R*f.R + d*d) / (2 * d)
		h := math.Sqrt(math.Max(0, e.R*e.R-a*a))
		ux, uy := (f.C[0]-e.C[0])/d, (f.C[1]-e.C[1])/d
		mx, my := e.C[0]+a*ux, e.C[1]+a*uy
		for _, s := range []float64{1, -1} {
			x, y := mx-s*h*uy, my+s*h*ux
			if e.inSpan(math.Atan2(y-e.C[1], x-e.C[0])) && f.inSpan(math.Atan2(y-f.C[1], x-f.C[0])) {
				return 0
			}
		}
	}
	// Critical points on the line of centres.
	ux, uy := (f.C[0]-e.C[0])/d, (f.C[1]-e.C[1])/d
	for _, s := range []float64{1, -1} {
		if p := (XY{e.C[0] + s*e.R*ux, e.C[1] + s*e.R*uy}); e.inSpan(math.Atan2(s*uy, s*ux)) {
			best = math.Min(best, bdPointArcDist(p, f))
		}
		if p := (XY{f.C[0] - s*f.R*ux, f.C[1] - s*f.R*uy}); f.inSpan(math.Atan2(-s*uy, -s*ux)) {
			best = math.Min(best, bdPointArcDist(p, e))
		}
	}
	return best
}

// bdEdgeDist is the exact distance between two boundary pieces.
func bdEdgeDist(e, f bdEdge) float64 {
	switch {
	case !e.Arc && !f.Arc:
		if bdSegsTouch(e.A, e.B, f.A, f.B) {
			return 0
		}
		return math.Min(math.Min(bdPointSegDist(e.A, f.A, f.B), bdPointSegDist(e.B, f.A, f.B)),
			math.Min(bdPointSegDist(f.A, e.A, e.B), bdPointSegDist(f.B, e.A, e.B)))
	case !e.Arc:
		return bdSegArcDist(e.A, e.B, f)
	case !f.Arc:
		return bdSegArcDist(f.A, f.B, e)
	}
	return bdArcArcDist(e, f)
}

// BoardShapeGap is the smallest distance between the boundaries of a and b.
func BoardShapeGap(a, b BoardShape) float64 {
	best := math.Inf(1)
	for _, e := range a.edges() {
		for _, f := range b.edges() {
			best = math.Min(best, bdEdgeDist(e, f))
		}
	}
	return best
}

// BoardShapeInside: in lies strictly inside out, its boundary more than
// BoardTol from out's.
func BoardShapeInside(in, out BoardShape) bool {
	es := in.edges()
	return len(es) > 0 && BoardShapeGap(in, out) > BoardTol && out.sdf(es[0].start()) < 0
}

// BoardShapesOverlap: the shapes share area or their boundaries come
// within BoardTol (one inside the other counts).
func BoardShapesOverlap(a, b BoardShape) bool {
	ea, eb := a.edges(), b.edges()
	if len(ea) == 0 || len(eb) == 0 {
		return false
	}
	return BoardShapeGap(a, b) <= BoardTol || b.sdf(ea[0].start()) < 0 || a.sdf(eb[0].start()) < 0
}

// edgeJSON is the boundary as board.py reads it: {"seg": [ax, ay, bx, by]}
// or {"arc": [cx, cy, r, a0, sweep]}.
func (s BoardShape) edgeJSON() []map[string][]float64 {
	var out []map[string][]float64
	for _, e := range s.edges() {
		if e.Arc {
			out = append(out, map[string][]float64{"arc": {e.C[0], e.C[1], e.R, e.A0, e.SW}})
		} else {
			out = append(out, map[string][]float64{"seg": {e.A[0], e.A[1], e.B[0], e.B[1]}})
		}
	}
	return out
}
