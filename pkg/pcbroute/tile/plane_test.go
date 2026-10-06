package tile

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

const mm = 1_000_000

// tilesOf lists every tile of p through Area.
func tilesOf(p *Plane) []*Tile {
	var ts []*Tile
	p.Area(p.bounds, func(t *Tile) bool { ts = append(ts, t); return true })
	return ts
}

// bruteAt is the tile of ts holding pt, by linear scan.
func bruteAt(ts []*Tile, pt geom.Pt) *Tile {
	for _, t := range ts {
		if t.Rect.Contains(pt) {
			return t
		}
	}
	return nil
}

// checkPlane verifies the corner-stitching invariants: the tiles cover the
// bounds exactly once, every stitch points to the neighbour a brute-force
// scan finds, Neighbors lists exactly the tiles sharing an edge stretch, the
// strips are maximal, and each tile's owners are the union of the nets of the
// obstacle rectangles over it (checked at every rectangle corner and at
// random points).
func checkPlane(t *testing.T, p *Plane, rng *rand.Rand) {
	t.Helper()
	ts := tilesOf(p)
	if len(ts) != p.Len() {
		t.Fatalf("Area found %d tiles, Len is %d", len(ts), p.Len())
	}
	var area int64
	for _, x := range ts {
		if x.Rect.Empty() || clip(x.Rect, p.bounds) != x.Rect {
			t.Fatalf("tile %v empty or outside %v", x.Rect, p.bounds)
		}
		if (x.Kind == Solid) != (len(x.Owners) > 0) {
			t.Fatalf("tile %v kind %d owners %v", x.Rect, x.Kind, x.Owners)
		}
		area += (x.Rect.MaxX - x.Rect.MinX) * (x.Rect.MaxY - x.Rect.MinY)
	}
	b := p.bounds
	if want := (b.MaxX - b.MinX) * (b.MaxY - b.MinY); area != want {
		t.Fatalf("tile area %d, bounds area %d", area, want)
	}
	// Equal total area plus pairwise disjointness is an exact cover.
	byX := slices.Clone(ts)
	slices.SortFunc(byX, func(a, c *Tile) int { return int(a.Rect.MinX - c.Rect.MinX) })
	for i, a := range byX {
		for _, c := range byX[i+1:] {
			if c.Rect.MinX >= a.Rect.MaxX {
				break
			}
			if a.Rect.Intersects(c.Rect) {
				t.Fatalf("tiles %v and %v overlap", a.Rect, c.Rect)
			}
		}
	}
	for _, x := range ts {
		r := x.Rect
		want := [4]*Tile{
			rt: bruteAt(ts, geom.Pt{X: r.MaxX, Y: r.MaxY - 1}),
			tr: bruteAt(ts, geom.Pt{X: r.MaxX - 1, Y: r.MaxY}),
			lb: bruteAt(ts, geom.Pt{X: r.MinX - 1, Y: r.MinY}),
			bl: bruteAt(ts, geom.Pt{X: r.MinX, Y: r.MinY - 1}),
		}
		if x.stitches != want {
			t.Fatalf("tile %v stitches wrong", r)
		}
		for s := Right; s <= Bottom; s++ {
			var got, exp []*Tile
			x.Neighbors(s, func(n *Tile) bool { got = append(got, n); return true })
			for _, n := range ts {
				if shares(x.Rect, n.Rect, s) {
					exp = append(exp, n)
				}
			}
			if len(got) != len(exp) {
				t.Fatalf("tile %v side %d: %d neighbours, want %d", r, s, len(got), len(exp))
			}
			for _, n := range got {
				if !slices.Contains(exp, n) {
					t.Fatalf("tile %v side %d: %v is no neighbour", r, s, n.Rect)
				}
				if s == Right && same(n.Owners, x.Owners) {
					t.Fatalf("tiles %v and %v side by side with owners %v", r, n.Rect, x.Owners)
				}
				if s == Top && same(n.Owners, x.Owners) && n.Rect.MinX == r.MinX && n.Rect.MaxX == r.MaxX {
					t.Fatalf("tiles %v and %v stacked with owners %v", r, n.Rect, x.Owners)
				}
			}
		}
	}
	var probes []geom.Pt
	for _, ob := range p.obs {
		for _, q := range ob.rects {
			probes = append(probes, geom.Pt{X: q.MinX, Y: q.MinY}, geom.Pt{X: q.MaxX - 1, Y: q.MaxY - 1},
				geom.Pt{X: q.MinX - 1, Y: q.MaxY}, geom.Pt{X: q.MaxX, Y: q.MinY - 1})
		}
	}
	for range 200 {
		probes = append(probes, randPt(rng, b))
	}
	for _, q := range probes {
		if !b.Contains(q) {
			continue
		}
		var want []geom.NetID
		for _, ob := range p.obs {
			for _, r := range ob.rects {
				if r.Contains(q) {
					want = append(want, ob.Net)
					break
				}
			}
		}
		slices.Sort(want)
		want = slices.Compact(want)
		if got := p.Locate(q); !slices.Equal(got.Owners, want) {
			t.Fatalf("owners at %v: %v, want %v", q, got.Owners, want)
		}
	}
}

// shares reports whether n touches r along a stretch of side s.
func shares(r, n geom.Rect, s Side) bool {
	switch s {
	case Right:
		return n.MinX == r.MaxX && n.MinY < r.MaxY && r.MinY < n.MaxY
	case Left:
		return n.MaxX == r.MinX && n.MinY < r.MaxY && r.MinY < n.MaxY
	case Top:
		return n.MinY == r.MaxY && n.MinX < r.MaxX && r.MinX < n.MaxX
	}
	return n.MaxY == r.MinY && n.MinX < r.MaxX && r.MinX < n.MaxX
}

func randPt(rng *rand.Rand, b geom.Rect) geom.Pt {
	return geom.Pt{X: b.MinX + rng.Int64N(b.MaxX-b.MinX), Y: b.MinY + rng.Int64N(b.MaxY-b.MinY)}
}

// randShape is a random pad, via or track: rect, circle, axis, 45° or
// arbitrary segment, or a rotated rectangle.
func randShape(rng *rand.Rand, b geom.Rect) geom.Shape {
	c := randPt(rng, b.Grow(-mm))
	d := func() int64 { return 50_000 + rng.Int64N(mm) }
	switch rng.IntN(6) {
	case 0:
		return geom.Rect{MinX: c.X, MinY: c.Y, MaxX: c.X + d(), MaxY: c.Y + d()}
	case 1:
		return geom.Circle{C: c, R: d() / 2}
	case 2:
		e := c
		if rng.IntN(2) == 0 {
			e.X += d()
		} else {
			e.Y += d()
		}
		return geom.Seg{A: c, B: e, HalfW: 50_000 + rng.Int64N(150_000)}
	case 3:
		n := d()
		return geom.Seg{A: c, B: geom.Pt{X: c.X + n, Y: c.Y - n}, HalfW: 50_000 + rng.Int64N(150_000)}
	case 4:
		return geom.Seg{A: c, B: geom.Pt{X: c.X + d(), Y: c.Y + d()/3}, HalfW: 100_000}
	}
	w, h := d(), d()/2
	return geom.Poly{Pts: []geom.Pt{c, {X: c.X + w, Y: c.Y + w/2}, {X: c.X + w - h/2, Y: c.Y + w/2 + h}, {X: c.X - h/2, Y: c.Y + h}}}
}

func TestNewPlaneIsOneSpaceTile(t *testing.T) {
	b := geom.Rect{MinX: -5, MinY: 3, MaxX: 100, MaxY: 40}
	p := NewPlane(b, 10)
	if p.Len() != 1 || p.Locate(geom.Pt{X: 0, Y: 10}).Rect != b || p.Locate(geom.Pt{X: 100, Y: 10}) != nil {
		t.Fatal("new plane is not one tile over its bounds")
	}
}

// TestRandomInsertDelete is PLAN.md M6's stitch-invariant property test.
func TestRandomInsertDelete(t *testing.T) {
	for seed := range uint64(6) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 7))
			b := geom.Rect{MaxX: 12 * mm, MaxY: 9 * mm}
			p := NewPlane(b, 100_000)
			var live []uint32
			ops := 120
			if testing.Short() {
				ops = 50
			}
			for i := range ops {
				if len(live) > 0 && rng.IntN(3) == 0 {
					k := rng.IntN(len(live))
					if !p.Delete(live[k]) {
						t.Fatalf("delete %d failed", live[k])
					}
					live = slices.Delete(live, k, k+1)
				} else {
					id := uint32(i + 1)
					p.Insert(Obstacle{ID: id, Net: geom.NetID(rng.IntN(4)), Shape: randShape(rng, b), R: 100_000 + rng.Int64N(300_000)})
					live = append(live, id)
				}
				checkPlane(t, p, rng)
			}
			for _, id := range live {
				p.Delete(id)
			}
			if p.Len() != 1 || p.Locate(geom.Pt{X: 1, Y: 1}).Kind != Space {
				t.Fatalf("after deleting everything: %d tiles", p.Len())
			}
			if p.Delete(1_000_000) {
				t.Fatal("deleted an unknown obstacle")
			}
		})
	}
}

// TestConservative is PLAN.md M6's conservativeness property: no point of a
// tile lies closer than R to an obstacle whose net is not an owner of the
// tile (so no Space point is within the inflated distance of any obstacle),
// checked with the exact geom kernels.
func TestConservative(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 3))
	b := geom.Rect{MaxX: 15 * mm, MaxY: 15 * mm}
	p := NewPlane(b, 100_000)
	var obs []Obstacle
	for i := range 80 {
		o := Obstacle{ID: uint32(i + 1), Net: geom.NetID(rng.IntN(5)), Shape: randShape(rng, b), R: 1 + rng.Int64N(400_000)}
		obs = append(obs, o)
		p.Insert(o)
	}
	p.Refine(geom.Rect{MinX: 5 * mm, MinY: 5 * mm, MaxX: 9 * mm, MaxY: 9 * mm})
	checkConservative(t, p, obs, rng)
}

func checkConservative(t *testing.T, p *Plane, obs []Obstacle, rng *rand.Rand) {
	t.Helper()
	for _, x := range tilesOf(p) {
		r := x.Rect
		pts := []geom.Pt{{X: r.MinX, Y: r.MinY}, {X: r.MaxX - 1, Y: r.MinY}, {X: r.MinX, Y: r.MaxY - 1},
			{X: r.MaxX - 1, Y: r.MaxY - 1}, {X: (r.MinX + r.MaxX) / 2, Y: (r.MinY + r.MaxY) / 2}}
		for range 4 {
			pts = append(pts, randPt(rng, r))
		}
		for _, q := range pts {
			for _, o := range obs {
				if geom.Within(geom.Circle{C: q}, o.Shape, o.R) && !slices.Contains(x.Owners, o.Net) {
					t.Fatalf("point %v of tile %v (owners %v) is closer than %d to obstacle %d %v", q, r, x.Owners, o.R, o.ID, o.Shape)
				}
			}
		}
	}
}

func TestLocateMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 5))
	b := geom.Rect{MinX: -3 * mm, MinY: -2 * mm, MaxX: 10 * mm, MaxY: 8 * mm}
	p := NewPlane(b, 50_000)
	for i := range 60 {
		p.Insert(Obstacle{ID: uint32(i + 1), Net: geom.NetID(i % 3), Shape: randShape(rng, b), R: 150_000})
	}
	ts := tilesOf(p)
	for range 5000 {
		q := randPt(rng, b)
		if got, want := p.Locate(q), bruteAt(ts, q); got != want {
			t.Fatalf("Locate(%v) = %v, want %v", q, got.Rect, want.Rect)
		}
	}
	if p.Locate(geom.Pt{X: 10 * mm, Y: 0}) != nil {
		t.Fatal("Locate outside the plane found a tile")
	}
}

func TestAreaVisitsEachTileOnce(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 1))
	b := geom.Rect{MaxX: 10 * mm, MaxY: 10 * mm}
	p := NewPlane(b, 100_000)
	for i := range 50 {
		p.Insert(Obstacle{ID: uint32(i + 1), Net: 1, Shape: randShape(rng, b), R: 100_000})
	}
	all := tilesOf(p)
	for range 200 {
		a, c := randPt(rng, b), randPt(rng, b)
		r := geom.Rect{MinX: min(a.X, c.X), MinY: min(a.Y, c.Y), MaxX: max(a.X, c.X) + 1, MaxY: max(a.Y, c.Y) + 1}
		seen := map[*Tile]int{}
		p.Area(r, func(x *Tile) bool { seen[x]++; return true })
		for _, x := range all {
			if want := x.Rect.Intersects(r); want != (seen[x] == 1) || seen[x] > 1 {
				t.Fatalf("Area(%v): tile %v visited %d times, intersects %v", r, x.Rect, seen[x], want)
			}
		}
	}
}

func TestPassable(t *testing.T) {
	p := NewPlane(geom.Rect{MaxX: 10 * mm, MaxY: 10 * mm}, 100_000)
	p.Insert(Obstacle{ID: 1, Net: 7, Shape: geom.Circle{C: geom.Pt{X: 2 * mm, Y: 2 * mm}, R: 100_000}, R: 200_000})
	p.Insert(Obstacle{ID: 2, Net: 7, Shape: geom.Circle{C: geom.Pt{X: 2*mm + 250_000, Y: 2 * mm}, R: 100_000}, R: 200_000})
	p.Insert(Obstacle{ID: 3, Net: 0, Shape: geom.Circle{C: geom.Pt{X: 6 * mm, Y: 6 * mm}, R: 100_000}, R: 200_000})
	p.Insert(Obstacle{ID: 4, Net: 8, Shape: geom.Circle{C: geom.Pt{X: 2*mm + 500_000, Y: 2 * mm}, R: 100_000}, R: 200_000})
	own := p.Locate(geom.Pt{X: 2 * mm, Y: 2 * mm})
	if !own.Passable(7) || own.Passable(8) || own.Kind != Solid {
		t.Fatalf("own-net tile %v owners %v", own.Rect, own.Owners)
	}
	shared := p.Locate(geom.Pt{X: 2*mm + 375_000, Y: 2 * mm})
	if shared.Passable(7) || shared.Passable(8) || !slices.Equal(shared.Owners, []geom.NetID{7, 8}) {
		t.Fatalf("shared tile owners %v", shared.Owners)
	}
	if nc := p.Locate(geom.Pt{X: 6 * mm, Y: 6 * mm}); nc.Passable(0) || nc.Passable(7) {
		t.Fatal("no-net copper is passable")
	}
	if !p.Locate(geom.Pt{X: 9 * mm, Y: 9 * mm}).Passable(0) {
		t.Fatal("space is not passable")
	}
}

// connected reports whether Space tiles link a and b through shared edges.
func connected(p *Plane, a, b geom.Pt, net geom.NetID) bool {
	start, goal := p.Locate(a), p.Locate(b)
	if start == nil || goal == nil || !start.Passable(net) || !goal.Passable(net) {
		return false
	}
	seen := map[*Tile]bool{start: true}
	q := []*Tile{start}
	for len(q) > 0 {
		x := q[0]
		q = q[1:]
		if x == goal {
			return true
		}
		for s := Right; s <= Bottom; s++ {
			x.Neighbors(s, func(n *Tile) bool {
				if !seen[n] && n.Passable(net) {
					seen[n] = true
					q = append(q, n)
				}
				return true
			})
		}
	}
	return false
}

// T3 of spec 01 §6 on the plane: a wall with a 0.65 mm gap passes a 0.2 mm
// track at 0.2 mm clearance (R = 0.3 mm from the wall copper); 0.55 mm does
// not. Across an axis-aligned wall the staircase is exact; across a 45° wall
// the default step (100 µm) closes the 50 µm channel and local refinement
// (spec 01 §5) opens it again.
func TestT3GapRefine(t *testing.T) {
	const hw, clr = 100_000, 200_000
	t.Run("axis", func(t *testing.T) {
		for _, c := range []struct {
			gap  int64
			open bool
		}{{650_000, true}, {550_000, false}} {
			p := NewPlane(geom.Rect{MaxX: 30 * mm, MaxY: 10 * mm}, clr/2)
			y0 := 5*mm - c.gap/2
			p.Insert(Obstacle{ID: 1, Shape: geom.Rect{MinX: 15 * mm, MinY: 0, MaxX: 16 * mm, MaxY: y0}, R: hw + clr})
			p.Insert(Obstacle{ID: 2, Shape: geom.Rect{MinX: 15 * mm, MinY: y0 + c.gap, MaxX: 16 * mm, MaxY: 10 * mm}, R: hw + clr})
			if got := connected(p, geom.Pt{X: 2 * mm, Y: 5 * mm}, geom.Pt{X: 28 * mm, Y: 5 * mm}, 1); got != c.open {
				t.Fatalf("gap %d: connected %v", c.gap, got)
			}
		}
	})
	t.Run("diagonal", func(t *testing.T) {
		// Wall copper 0.2 mm wide along y = x; the capsule ends are 0.65 mm
		// apart along the wall (0.85 mm between centres).
		const d = 601_041 // 0.85 mm / √2
		e := int64(10*mm - d/2)
		obs := []Obstacle{
			{ID: 1, Shape: geom.Seg{A: geom.Pt{}, B: geom.Pt{X: e, Y: e}, HalfW: 100_000}, R: hw + clr},
			{ID: 2, Shape: geom.Seg{A: geom.Pt{X: e + d, Y: e + d}, B: geom.Pt{X: 20 * mm, Y: 20 * mm}, HalfW: 100_000}, R: hw + clr},
		}
		p := NewPlane(geom.Rect{MaxX: 20 * mm, MaxY: 20 * mm}, max(clr/2, MinStairStep))
		for _, o := range obs {
			p.Insert(o)
		}
		a, b := geom.Pt{X: 15 * mm, Y: 5 * mm}, geom.Pt{X: 5 * mm, Y: 15 * mm}
		if connected(p, a, b, 1) {
			t.Fatal("the coarse staircase should close the 45° gap")
		}
		win := geom.Rect{MinX: 9 * mm, MinY: 9 * mm, MaxX: 11 * mm, MaxY: 11 * mm}
		for i := range maxRefine {
			if n := p.Refine(win); n != 2 {
				t.Fatalf("refinement %d changed %d obstacles", i+1, n)
			}
		}
		if !connected(p, a, b, 1) {
			t.Fatal("two refinements do not open the 45° gap")
		}
		if p.Refine(win) != 0 {
			t.Fatal("refined beyond the limit")
		}
		rng := rand.New(rand.NewPCG(1, 1))
		checkPlane(t, p, rng)
		checkConservative(t, p, obs, rng)
	})
}

// TestDeterministic builds the same plane twice and compares the tiles in
// Area order.
func TestDeterministic(t *testing.T) {
	dump := func() string {
		rng := rand.New(rand.NewPCG(11, 2))
		b := geom.Rect{MaxX: 10 * mm, MaxY: 10 * mm}
		p := NewPlane(b, 100_000)
		for i := range 70 {
			p.Insert(Obstacle{ID: uint32(i + 1), Net: geom.NetID(i % 4), Shape: randShape(rng, b), R: 120_000})
			if i%5 == 4 {
				p.Delete(uint32(i - 2))
			}
		}
		s := ""
		for _, x := range tilesOf(p) {
			s += fmt.Sprint(x.Rect, x.Owners)
		}
		return s
	}
	if dump() != dump() {
		t.Fatal("same inserts give different planes")
	}
}
