package geom

import (
	"math"
	"math/bits"
)

// Exact distance kernels (spec 01 §2.3). Every shape is a core (point,
// segment or polygon region) grown by a radius, so the distance of two shapes
// is max(0, d(coreA, coreB) - rA - rB). Squared core distances are kept as an
// exact fraction num/den of unsigned 128-bit integers; only the final integer
// square root starts from a float64 guess, which is then corrected exactly.

type u128 struct{ hi, lo uint64 }

func mul64(a, b uint64) u128 {
	hi, lo := bits.Mul64(a, b)
	return u128{hi, lo}
}

func (a u128) less(b u128) bool { return a.hi < b.hi || a.hi == b.hi && a.lo < b.lo }

// sq is a squared distance num/den, den ≥ 1.
type sq struct {
	num u128
	den uint64
}

// lessSq reports whether s < R², for 0 ≤ R < 2³².
func (s sq) lessSq(R uint64) bool { return s.num.less(mul64(R*R, s.den)) }

// floorSqrt is ⌊√(num/den)⌋.
func (s sq) floorSqrt() int64 {
	f := math.Sqrt((float64(s.num.hi)*0x1p64 + float64(s.num.lo)) / float64(s.den))
	d := uint64(f)
	for d > 0 && s.num.less(mul64(d*d, s.den)) {
		d--
	}
	for !s.num.less(mul64((d+1)*(d+1), s.den)) {
		d++
	}
	return int64(d)
}

// ceilSqrt is the smallest k with k² ≥ n, for n < 2⁶³.
func ceilSqrt(n uint64) uint64 {
	k := uint64(math.Sqrt(float64(n)))
	for k > 0 && (k-1)*(k-1) >= n {
		k--
	}
	for k*k < n {
		k++
	}
	return k
}

func ptPt(p, q Pt) sq {
	dx, dy := uint64(abs(p.X-q.X)), uint64(abs(p.Y-q.Y))
	return sq{u128{0, dx*dx + dy*dy}, 1}
}

// ptSeg is the squared distance from p to the closed segment AB.
func ptSeg(p, a, b Pt) sq {
	ux, uy := b.X-a.X, b.Y-a.Y
	vx, vy := p.X-a.X, p.Y-a.Y
	t := ux*vx + uy*vy
	if t <= 0 {
		return ptPt(p, a)
	}
	l := ux*ux + uy*uy
	if t >= l {
		return ptPt(p, b)
	}
	c := uint64(abs(ux*vy - uy*vx))
	return sq{mul64(c, c), uint64(l)}
}

// visit reports whether the cores of a and b touch. Otherwise it calls fn
// with squared distances of sub-pieces whose minimum is the squared core
// distance; fn returns true to stop early. Pieces may be reported before an
// intersection is found, so callers must treat touch as overriding them.
func visit(a, b core, fn func(sq) bool) (touch bool) {
	if len(a.pts) > len(b.pts) {
		a, b = b, a
	}
	pa, pb := a.pts, b.pts
	switch {
	case len(pb) == 1:
		fn(ptPt(pa[0], pb[0]))
		return false
	case len(pb) == 2:
		if len(pa) == 1 {
			fn(ptSeg(pa[0], pb[0], pb[1]))
			return false
		}
		if SegsIntersect(pa[0], pa[1], pb[0], pb[1]) {
			return true
		}
		return segSeg(pa[0], pa[1], pb[0], pb[1], fn)
	}
	// b is a polygon region. One holds the other, or their boundaries meet.
	if InPoly(pa[0], pb) || len(pa) >= 3 && InPoly(pb[0], pa) {
		return true
	}
	for i, j := 0, len(pb)-1; i < len(pb); j, i = i, i+1 {
		c, d := pb[j], pb[i]
		if len(pa) == 1 {
			if fn(ptSeg(pa[0], c, d)) {
				return false
			}
			continue
		}
		for k, l := 0, len(pa)-1; k < len(pa); l, k = k, k+1 {
			if len(pa) == 2 && k == 0 {
				continue // a segment has one edge, not two
			}
			if SegsIntersect(pa[l], pa[k], c, d) {
				return true
			}
			if segSeg(pa[l], pa[k], c, d, fn) {
				return false
			}
		}
	}
	return false
}

// segSeg feeds fn the four end-point distances of two disjoint segments; their
// minimum is the segment distance. It returns true when fn stopped.
func segSeg(a, b, c, d Pt, fn func(sq) bool) bool {
	return fn(ptSeg(a, c, d)) || fn(ptSeg(b, c, d)) || fn(ptSeg(c, a, b)) || fn(ptSeg(d, a, b))
}

// Dist is the Euclidean distance between the closed shapes a and b, rounded
// down to whole nanometres; 0 when they touch or overlap. It returns
// math.MaxInt64 when either shape is empty. It panics on a shape outside
// ±MaxCoord (see Check).
func Dist(a, b Shape) int64 {
	var ba, bb [4]Pt
	ca, cb := coreOf(a, &ba), coreOf(b, &bb)
	if len(ca.pts) == 0 || len(cb.pts) == 0 {
		return math.MaxInt64
	}
	best := int64(math.MaxInt64)
	if visit(ca, cb, func(s sq) bool {
		best = min(best, s.floorSqrt())
		return false
	}) {
		return 0
	}
	// ⌊x⌋ - k == ⌊x - k⌋ for integer k, so this is the floor of the true distance.
	return max(0, best-ca.r-cb.r)
}

// Within reports whether Dist(a, b) < r exactly, without rounding: true when
// some point of a lies closer than r to some point of b. It is false for
// r ≤ 0 and for empty shapes. It panics on a shape or r outside ±MaxCoord.
func Within(a, b Shape, r int64) bool {
	if r > MaxCoord {
		panic("geom: clearance outside MaxCoord")
	}
	if r <= 0 {
		return false
	}
	var ba, bb [4]Pt
	ca, cb := coreOf(a, &ba), coreOf(b, &bb)
	if len(ca.pts) == 0 || len(cb.pts) == 0 {
		return false
	}
	R := uint64(r + ca.r + cb.r) // < 3·2³⁰, so R² fits in uint64
	hit := false
	if visit(ca, cb, func(s sq) bool {
		hit = s.lessSq(R)
		return hit
	}) {
		return true
	}
	return hit
}
