package tile

import (
	"slices"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// Obstacle is one shape inserted into a plane. Its staircase covers every
// lattice point closer than R to Shape (spec 01 §2.2: R is the routed half
// width plus the clearance). An Outline obstacle is the board outline: the
// staircase also covers everything outside the polygon (spec 01 §4 "board
// edge").
type Obstacle struct {
	ID      uint32     // unique within the plane, not 0
	Net     geom.NetID // 0: an obstacle to every net
	Shape   geom.Shape
	R       int64
	Outline bool
}

// obstacle is an Obstacle with its current staircase.
type obstacle struct {
	Obstacle
	level int // refinement level, 0..maxRefine
	rects []geom.Rect
}

// Insert adds o. It panics on ID 0, a duplicate ID or a shape outside
// ±geom.MaxCoord. Cost: O(k·√n) for k staircase rectangles (spec 01 §3.3).
func (p *Plane) Insert(o Obstacle) {
	if o.ID == 0 {
		panic("tile: obstacle id 0")
	}
	if _, dup := p.slots[o.ID]; dup {
		panic("tile: duplicate obstacle id")
	}
	ob := obstacle{Obstacle: o}
	ob.rects = p.cover(&ob)
	p.register(ob)
	p.paint(ob.rects, func(s []geom.NetID) []geom.NetID { return p.with(s, o.Net) })
}

// cover is ob's staircase at its refinement level.
func (p *Plane) cover(ob *obstacle) []geom.Rect {
	step, corners := max(p.step>>ob.level, 1), p.corners<<ob.level
	var rs []geom.Rect
	if ob.Outline {
		rs = Outline(ob.Shape, ob.R, step, p.bounds)
	} else {
		rs = Cover(ob.Shape, ob.R, step, corners)
	}
	out := rs[:0]
	for _, r := range rs {
		if r = clip(r, p.bounds); !r.Empty() {
			out = append(out, r)
		}
	}
	return out
}

// register stores ob in the registry under the bounds of its staircase.
func (p *Plane) register(ob obstacle) {
	var box geom.Rect
	for _, r := range ob.rects {
		box = box.Union(r)
	}
	s := p.grid.Insert(box)
	if int(s) == len(p.obs) {
		p.obs = append(p.obs, ob)
	} else {
		p.obs[s] = ob
	}
	p.slots[ob.ID] = s
}

// with is the interned owner set s ∪ {n}.
func (p *Plane) with(s []geom.NetID, n geom.NetID) []geom.NetID {
	if len(s) == 0 {
		o := p.single[n]
		if o == nil {
			o = []geom.NetID{n}
			p.single[n] = o
		}
		return o
	}
	i, found := slices.BinarySearch(s, n)
	if found {
		return s
	}
	u := slices.Insert(slices.Clone(s), i, n)
	var b strings.Builder
	for _, v := range u {
		b.WriteString(strconv.Itoa(int(v)))
		b.WriteByte(',')
	}
	if o, ok := p.sets[b.String()]; ok {
		return o
	}
	p.sets[b.String()] = u
	return u
}

// same reports whether two interned owner sets are equal.
func same(a, b []geom.NetID) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

func setOwners(t *Tile, s []geom.NetID) {
	t.Owners = s
	t.Kind = Space
	if len(s) > 0 {
		t.Kind = Solid
	}
}

// paint replaces the owners of every point in rs by f(owners), then restores
// the canonical form once for all of them (Ousterhout 1984 area update). f
// must map interned sets to interned sets and be idempotent, since rs may
// overlap.
func (p *Plane) paint(rs []geom.Rect, f func([]geom.NetID) []geom.NetID) {
	var todo, work []*Tile
	for _, r := range rs {
		r = clip(r, p.bounds)
		if r.Empty() {
			continue
		}
		todo = todo[:0]
		p.Area(r, func(t *Tile) bool {
			if !same(f(t.Owners), t.Owners) {
				todo = append(todo, t)
			}
			return true
		})
		for _, t := range todo {
			if t.Rect.MaxY > r.MaxY {
				work = append(work, p.splitY(t, r.MaxY))
			}
			if t.Rect.MinY < r.MinY {
				work = append(work, t)
				t = p.splitY(t, r.MinY)
			}
			if t.Rect.MinX < r.MinX {
				work = append(work, t)
				t = p.splitX(t, r.MinX)
			}
			if t.Rect.MaxX > r.MaxX {
				work = append(work, p.splitX(t, r.MaxX))
			}
			setOwners(t, f(t.Owners))
			work = append(work, t)
		}
	}
	if len(work) > 0 {
		p.canon(work)
	}
}

func dead(t *Tile) bool { return t.Rect.Empty() }

// canon restores maximal horizontal strips around the tiles in work, which
// must hold every tile whose extent or owners changed. Phase 1 joins every
// pair of side-by-side tiles with equal owners, first cutting the taller one
// to the shared rows; it ends because each join removes shared edge length.
// Phase 2 joins stacked tiles with equal owners and equal x span; that never
// makes side-by-side pairs equal again. Afterwards no two tiles with equal
// owners touch side by side, and no two touch on top with equal x span, which
// is the unique canonical form.
func (p *Plane) canon(work []*Tile) {
	touched := slices.Clone(work)
	push := func(ts ...*Tile) {
		work = append(work, ts...)
		touched = append(touched, ts...)
	}
	for len(work) > 0 {
		t := work[len(work)-1]
		work = work[:len(work)-1]
		if dead(t) {
			continue
		}
		var a, b *Tile
		t.Neighbors(Right, func(n *Tile) bool {
			if same(n.Owners, t.Owners) {
				a, b = t, n
				return false
			}
			return true
		})
		if a == nil {
			t.Neighbors(Left, func(n *Tile) bool {
				if same(n.Owners, t.Owners) {
					a, b = n, t
					return false
				}
				return true
			})
		}
		if a == nil {
			continue
		}
		if a.Rect.MinY < b.Rect.MinY {
			push(a)
			a = p.splitY(a, b.Rect.MinY)
		} else if b.Rect.MinY < a.Rect.MinY {
			push(b)
			b = p.splitY(b, a.Rect.MinY)
		}
		if a.Rect.MaxY > b.Rect.MaxY {
			push(p.splitY(a, b.Rect.MaxY))
		} else if b.Rect.MaxY > a.Rect.MaxY {
			push(p.splitY(b, a.Rect.MaxY))
		}
		p.joinX(a, b)
		push(a)
	}
	for _, t := range touched {
		if dead(t) {
			continue
		}
		for {
			d := t.stitches[bl]
			if d == nil || d.Rect.MinX != t.Rect.MinX || d.Rect.MaxX != t.Rect.MaxX || !same(d.Owners, t.Owners) {
				break
			}
			p.joinY(d, t)
			t = d
		}
		for {
			u := t.stitches[tr]
			if u == nil || u.Rect.MinX != t.Rect.MinX || u.Rect.MaxX != t.Rect.MaxX || !same(u.Owners, t.Owners) {
				break
			}
			p.joinY(t, u)
		}
	}
}
