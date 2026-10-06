package geom

// Empty reports whether r holds no point.
func (r Rect) Empty() bool { return r.MinX >= r.MaxX || r.MinY >= r.MaxY }

// Intersects reports whether r and o share a point.
func (r Rect) Intersects(o Rect) bool {
	return r.MinX < o.MaxX && o.MinX < r.MaxX && r.MinY < o.MaxY && o.MinY < r.MaxY
}

// Contains reports whether p lies in r.
func (r Rect) Contains(p Pt) bool {
	return r.MinX <= p.X && p.X < r.MaxX && r.MinY <= p.Y && p.Y < r.MaxY
}

// Grow returns r widened by d on every side.
func (r Rect) Grow(d int64) Rect { return Rect{r.MinX - d, r.MinY - d, r.MaxX + d, r.MaxY + d} }

// Union is the smallest Rect holding r and o; an empty operand is ignored.
func (r Rect) Union(o Rect) Rect {
	if r.Empty() {
		return o
	}
	if o.Empty() {
		return r
	}
	return Rect{min(r.MinX, o.MinX), min(r.MinY, o.MinY), max(r.MaxX, o.MaxX), max(r.MaxY, o.MaxY)}
}

// core is a shape as a point set "within radius r of pts": one point (Circle),
// a segment (Seg) or a closed polygon region (Rect, Poly with ≥ 3 vertices).
// A Rect is the closed box of its integer points, [Min, Max-1].
type core struct {
	pts []Pt
	r   int64
}

// coreOf converts s; buf backs a Rect's corners so no allocation is needed.
// An empty shape has no pts.
func coreOf(s Shape, buf *[4]Pt) core {
	mustCheck(s)
	switch s := s.(type) {
	case Rect:
		if s.Empty() {
			return core{}
		}
		buf[0], buf[1] = Pt{s.MinX, s.MinY}, Pt{s.MaxX - 1, s.MinY}
		buf[2], buf[3] = Pt{s.MaxX - 1, s.MaxY - 1}, Pt{s.MinX, s.MaxY - 1}
		return core{pts: buf[:]}
	case Seg:
		buf[0], buf[1] = s.A, s.B
		return core{pts: buf[:2], r: s.HalfW}
	case Circle:
		buf[0] = s.C
		return core{pts: buf[:1], r: s.R}
	case Poly:
		return core{pts: s.Pts}
	}
	return core{}
}

// Octagon returns the smallest octilinear polygon (sides at 0°, 45°, 90°, …)
// that contains s grown by d ≥ 0: the 45°-hull that spec 02 §3.3 uses as a
// shove obstacle. Vertices are counter-clockwise and integer; the containment
// is exact because the diagonal offsets are rounded up. An empty shape gives
// an empty Poly. It panics when d < 0 or the grown radius exceeds MaxCoord.
func Octagon(s Shape, d int64) Poly {
	var buf [4]Pt
	c := coreOf(s, &buf)
	if len(c.pts) == 0 {
		return Poly{}
	}
	r := c.r + d
	if d < 0 || r > MaxCoord {
		panic("geom: octagon offset outside [0, MaxCoord]")
	}
	k := int64(ceilSqrt(2 * uint64(r) * uint64(r))) // r·√2 rounded up
	p0 := c.pts[0]
	x0, x1, y0, y1 := p0.X, p0.X, p0.Y, p0.Y
	s0, s1, d0, d1 := p0.X+p0.Y, p0.X+p0.Y, p0.X-p0.Y, p0.X-p0.Y
	for _, p := range c.pts[1:] {
		x0, x1, y0, y1 = min(x0, p.X), max(x1, p.X), min(y0, p.Y), max(y1, p.Y)
		s0, s1 = min(s0, p.X+p.Y), max(s1, p.X+p.Y)
		d0, d1 = min(d0, p.X-p.Y), max(d1, p.X-p.Y)
	}
	x0, x1, y0, y1 = x0-r, x1+r, y0-r, y1+r
	// A diagonal never cuts beyond the box corner.
	s0, s1 = max(s0-k, x0+y0), min(s1+k, x1+y1)
	d0, d1 = max(d0-k, x0-y1), min(d1+k, x1-y0)
	v := [8]Pt{
		{s0 - y0, y0}, {d1 + y0, y0}, // bottom
		{x1, x1 - d1}, {x1, s1 - x1}, // right
		{s1 - y1, y1}, {d0 + y1, y1}, // top
		{x0, x0 - d0}, {x0, s0 - x0}, // left
	}
	out := make([]Pt, 0, 8)
	for _, p := range v {
		if len(out) == 0 || out[len(out)-1] != p {
			out = append(out, p)
		}
	}
	if len(out) > 1 && out[0] == out[len(out)-1] {
		out = out[:len(out)-1]
	}
	return Poly{Pts: out}
}
