package tile

import (
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// DefaultCornerSteps is the number of staircase rectangles per quadrant of a
// rounded end (spec 01 §2.4 CornerSteps).
const DefaultCornerSteps = 3

// MinStairStep is the floor of the default staircase step: StairStep =
// max(clearance/2, 25 µm) (spec 01 §2.4).
const MinStairStep int64 = 25_000

// maxRefine bounds local staircase refinement: at most 2 halvings of the step
// (spec 01 §5).
const maxRefine = 2

// gridCell is the bucket size of a plane's obstacle registry, 1 mm (spec 01
// §2.3 suggests about 1 mm buckets for routed copper).
const gridCell int64 = 1_000_000

var emptyRect = geom.Rect{}

// Plane is one corner-stitched plane: its bounds are covered exactly once by
// tiles kept in maximal horizontal strips (spec 01 §2.4). A tile's Owners is
// the union of the nets of every obstacle whose staircase covers it; a tile
// without owners is Space. Points outside the bounds belong to no tile and are
// not passable. A Plane is not safe for concurrent use.
type Plane struct {
	bounds  geom.Rect
	step    int64 // staircase step of refinement level 0
	corners int   // CornerSteps of refinement level 0
	n       int   // live tiles
	hint    *Tile // start of the next point location

	obs   []obstacle // by grid slot; a deleted slot has id 0 and no rects
	slots map[uint32]int32
	grid  *geom.Grid

	single map[geom.NetID][]geom.NetID // interned one-net owner sets
	sets   map[string][]geom.NetID     // interned larger sets, by key
}

// NewPlane returns a plane over bounds with one Space tile. step is the
// staircase step for diagonal shapes (spec 01 §2.4 StairStep); it must be
// positive. It panics on empty bounds or bounds outside ±geom.MaxCoord.
func NewPlane(bounds geom.Rect, step int64) *Plane {
	if bounds.Empty() {
		panic("tile: empty plane bounds")
	}
	if err := geom.Check(bounds); err != nil {
		panic(err)
	}
	if step <= 0 {
		panic("tile: staircase step must be positive")
	}
	t := &Tile{Rect: bounds, Kind: Space}
	return &Plane{
		bounds: bounds, step: step, corners: DefaultCornerSteps, n: 1, hint: t,
		slots: map[uint32]int32{}, grid: geom.NewGrid(bounds, gridCell),
		single: map[geom.NetID][]geom.NetID{}, sets: map[string][]geom.NetID{},
	}
}

// Bounds is the area the plane covers.
func (p *Plane) Bounds() geom.Rect { return p.bounds }

// Len is the number of tiles.
func (p *Plane) Len() int { return p.n }

// Passable reports whether net may occupy t: t is Space, or every owner of t
// is net (own-net copper, spec 01 §2.4). Net 0 copper blocks every net.
func (t *Tile) Passable(net geom.NetID) bool {
	return t.Kind == Space || net != 0 && len(t.Owners) == 1 && t.Owners[0] == net
}

// Locate returns the tile holding pt, or nil when pt is outside the plane.
// It walks the stitches from the last tile found (Ousterhout's point
// location, expected O(√n)).
func (p *Plane) Locate(pt geom.Pt) *Tile {
	if !p.bounds.Contains(pt) {
		return nil
	}
	t := p.locate(p.hint, pt)
	p.hint = t
	return t
}

// locate walks from t to the tile holding pt, which must be in the plane.
func (p *Plane) locate(t *Tile, pt geom.Pt) *Tile {
	for {
		for pt.Y < t.Rect.MinY {
			t = t.stitches[bl]
		}
		for pt.Y >= t.Rect.MaxY {
			t = t.stitches[tr]
		}
		switch {
		case pt.X < t.Rect.MinX:
			for pt.X < t.Rect.MinX {
				t = t.stitches[lb]
			}
		case pt.X >= t.Rect.MaxX:
			for pt.X >= t.Rect.MaxX {
				t = t.stitches[rt]
			}
		default:
			return t
		}
	}
}

// Area calls fn with every tile that meets r, each once, until fn returns
// false; it returns false when fn stopped. The order is fixed: tiles on r's
// left edge from top to bottom, each followed depth-first by the tiles to its
// right that it is the anchor of (the left neighbour holding the lower of the
// tile's and r's bottom). fn must not change the plane.
func (p *Plane) Area(r geom.Rect, fn func(*Tile) bool) bool {
	r = clip(r, p.bounds)
	if r.Empty() {
		return true
	}
	t := p.locate(p.hint, geom.Pt{X: r.MinX, Y: r.MaxY - 1})
	p.hint = t
	var stack []*Tile
	for {
		stack = append(stack[:0], t)
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !fn(x) {
				return false
			}
			if x.Rect.MaxX >= r.MaxX {
				continue
			}
			mark := len(stack)
			x.Neighbors(Right, func(n *Tile) bool {
				if n.Rect.MinY >= r.MaxY {
					return true
				}
				if n.Rect.MaxY <= r.MinY {
					return false
				}
				if y := max(n.Rect.MinY, r.MinY); x.Rect.MinY <= y && y < x.Rect.MaxY {
					stack = append(stack, n)
				}
				return true
			})
			// Visit the right neighbours top to bottom.
			for i, j := mark, len(stack)-1; i < j; i, j = i+1, j-1 {
				stack[i], stack[j] = stack[j], stack[i]
			}
		}
		if t.Rect.MinY <= r.MinY {
			return true
		}
		n := t.stitches[bl]
		for n.Rect.MaxX <= r.MinX {
			n = n.stitches[rt]
		}
		t = n
	}
}

// clip is the intersection of r and b, or the zero Rect.
func clip(r, b geom.Rect) geom.Rect {
	r = geom.Rect{MinX: max(r.MinX, b.MinX), MinY: max(r.MinY, b.MinY), MaxX: min(r.MaxX, b.MaxX), MaxY: min(r.MaxY, b.MaxY)}
	if r.Empty() {
		return emptyRect
	}
	return r
}
