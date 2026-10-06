package geom

import "fmt"

// MaxCoord bounds every coordinate, radius and clearance: |v| <= MaxCoord
// (about 1.07 m). Within that range a coordinate difference fits in 31 bits, a
// product of two differences in 62 bits and a cross product in int64, so the
// kernels need no math/big (spec 01 §2.3, §5 "numerical overflow"). Boards
// outside it are rejected at load time with Check.
const MaxCoord int64 = 1<<30 - 1

// Add returns p+q.
func (p Pt) Add(q Pt) Pt { return Pt{p.X + q.X, p.Y + q.Y} }

// Sub returns p-q.
func (p Pt) Sub(q Pt) Pt { return Pt{p.X - q.X, p.Y - q.Y} }

// Dir8 is one of the 8 octilinear directions: 0 is +X, counter-clockwise in 45°
// steps (spec 01 §2.1).
type Dir8 uint8

var dirVec = [8]Pt{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}}

// Vec is d's step vector; both components are -1, 0 or 1.
func (d Dir8) Vec() Pt { return dirVec[d&7] }

// Diagonal reports whether d is a 45° direction.
func (d Dir8) Diagonal() bool { return d&1 == 1 }

// Step moves p by n along d. A diagonal step moves n on both axes, so the
// segment from p keeps |dx| == |dy| exactly.
func (p Pt) Step(d Dir8, n int64) Pt {
	v := d.Vec()
	return Pt{p.X + v.X*n, p.Y + v.Y*n}
}

// DirOf returns the direction from a to b; ok is false when a == b or AB is
// not octilinear.
func DirOf(a, b Pt) (d Dir8, ok bool) {
	dx, dy := b.X-a.X, b.Y-a.Y
	if (dx == 0 && dy == 0) || !(dx == 0 || dy == 0 || abs(dx) == abs(dy)) {
		return 0, false
	}
	v := Pt{sign(dx), sign(dy)}
	for i, w := range dirVec {
		if w == v {
			return Dir8(i), true
		}
	}
	return 0, false
}

// IsOctilinear reports whether AB is horizontal, vertical or exactly 45°.
func IsOctilinear(a, b Pt) bool {
	dx, dy := abs(b.X-a.X), abs(b.Y-a.Y)
	return dx == 0 || dy == 0 || dx == dy
}

// Check returns an error when a coordinate or radius of s is outside ±MaxCoord
// or a radius is negative.
func Check(s Shape) error {
	bad := func(v int64) bool { return v < -MaxCoord || v > MaxCoord }
	pt := func(p Pt) error {
		if bad(p.X) || bad(p.Y) {
			return fmt.Errorf("geom: point %v outside ±%d nm", p, MaxCoord)
		}
		return nil
	}
	rad := func(r int64) error {
		if r < 0 || r > MaxCoord {
			return fmt.Errorf("geom: radius %d outside [0, %d] nm", r, MaxCoord)
		}
		return nil
	}
	switch s := s.(type) {
	case Rect:
		if err := pt(Pt{s.MinX, s.MinY}); err != nil {
			return err
		}
		return pt(Pt{s.MaxX - 1, s.MaxY - 1})
	case Seg:
		if err := pt(s.A); err != nil {
			return err
		}
		if err := pt(s.B); err != nil {
			return err
		}
		return rad(s.HalfW)
	case Circle:
		if err := pt(s.C); err != nil {
			return err
		}
		return rad(s.R)
	case Poly:
		for _, p := range s.Pts {
			if err := pt(p); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("geom: unknown shape %T", s)
}

func mustCheck(s Shape) {
	if err := Check(s); err != nil {
		panic(err)
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func sign(v int64) int64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}
