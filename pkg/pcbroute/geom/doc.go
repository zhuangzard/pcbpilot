// Package geom holds the integer geometry of routing engine v2 (pkg/pcbroute):
// primitives, exact distance kernels, orientation predicates and the per-layer
// spatial index (spec 01 §2.1–§2.3; PLAN.md M1).
//
// Every coordinate is an int64 in nanometres (1 mil = 25 400 nm). Geometric
// predicates never use floating point; floats appear only in costs elsewhere.
//
// M0 freezes the scalar IDs, the primitive types and the Shape interface below so
// that rules, board and dsn can be written in parallel. M1 adds the kernels and
// the index without changing these declarations.
package geom

// NetID numbers a net. 0 means "no net".
type NetID int32

// LayerID numbers a copper layer, 0 = top. AllLayers marks items that exist on
// every copper layer (through-hole pads, all-layer keep-outs).
type LayerID int16

// AllLayers is the LayerID of an item present on every copper layer.
const AllLayers LayerID = -1

// Pt is a point.
type Pt struct{ X, Y int64 }

// Rect is a half-open axis-aligned box: MinX <= x < MaxX, MinY <= y < MaxY.
type Rect struct{ MinX, MinY, MaxX, MaxY int64 }

// Seg is a track piece: the capsule of radius HalfW around segment AB. Routed
// segments are octilinear; a 45° segment has |dx| == |dy| exactly.
type Seg struct {
	A, B  Pt
	HalfW int64
}

// Circle is a round pad or a via land.
type Circle struct {
	C Pt
	R int64
}

// Poly is a simple polygon (pads, zones, keep-outs, board outline). Pts lists
// the vertices once each, without repeating the first one.
type Poly struct{ Pts []Pt }

// Shape is one of Rect, Seg, Circle or Poly. Shapes are closed point sets:
// points at exactly HalfW or R belong to the shape.
type Shape interface {
	// Bounds is the smallest half-open Rect that contains the shape.
	Bounds() Rect
}

// Bounds returns r itself.
func (r Rect) Bounds() Rect { return r }

// Bounds covers both end caps of the capsule.
func (s Seg) Bounds() Rect {
	return Rect{
		MinX: min(s.A.X, s.B.X) - s.HalfW, MinY: min(s.A.Y, s.B.Y) - s.HalfW,
		MaxX: max(s.A.X, s.B.X) + s.HalfW + 1, MaxY: max(s.A.Y, s.B.Y) + s.HalfW + 1,
	}
}

// Bounds covers the disc.
func (c Circle) Bounds() Rect {
	return Rect{MinX: c.C.X - c.R, MinY: c.C.Y - c.R, MaxX: c.C.X + c.R + 1, MaxY: c.C.Y + c.R + 1}
}

// Bounds covers every vertex; an empty polygon has the zero Rect.
func (p Poly) Bounds() Rect {
	if len(p.Pts) == 0 {
		return Rect{}
	}
	r := Rect{MinX: p.Pts[0].X, MinY: p.Pts[0].Y, MaxX: p.Pts[0].X, MaxY: p.Pts[0].Y}
	for _, q := range p.Pts[1:] {
		r.MinX, r.MinY = min(r.MinX, q.X), min(r.MinY, q.Y)
		r.MaxX, r.MaxY = max(r.MaxX, q.X), max(r.MaxY, q.Y)
	}
	r.MaxX++
	r.MaxY++
	return r
}
