package pcbauto

import (
	"math"
	"sort"
)

// keepoutOnPart reports whether keep-out k reaches part p on the layers it
// names: an all-layer keep-out reaches every part, a layer-bound one the
// parts on that side, and a copper keep-out also the through-hole pads of a
// part on the other side. A no-parts area on the top side alone leaves the
// bottom side free (KiCad tests the courtyard of the keep-out's layer).
func keepoutOnPart(k *Keepout, p *Part) bool {
	if len(k.Layers) == 0 || k.onLayer(p.Side) {
		return true
	}
	return k.NoCopper && hasTHT(p)
}

// noPartsOn reports whether k forbids part p's body where it lies.
func noPartsOn(k *Keepout, p *Part) bool {
	return k.NoParts && k.Owner != p.Ref && (len(k.Layers) == 0 || k.onLayer(p.Side))
}

// repelsPart reports whether the placer keeps part p's box out of keep-out
// k (partCost's keep-out term): a no-parts or copper keep-out that is not
// p's own, on p's side; a bridge may straddle a copper-only keep-out.
func (pl *placer) repelsPart(k *Keepout, p *Part) bool {
	if !(k.NoParts || k.NoCopper) || k.Owner == p.Ref {
		return false
	}
	if pl.c.IsBridge(p.Ref) && !k.NoParts {
		return false
	}
	return keepoutOnPart(k, p)
}

// unionArea is the area the rectangles cover together (overlaps count
// once), each clipped to in.
func unionArea(in Rect, rs []Rect) float64 {
	var cs []Rect
	var xs []float64
	for _, r := range rs {
		c := Rect{math.Max(r.MinX, in.MinX), math.Max(r.MinY, in.MinY), math.Min(r.MaxX, in.MaxX), math.Min(r.MaxY, in.MaxY)}
		if c.MaxX > c.MinX && c.MaxY > c.MinY {
			cs = append(cs, c)
			xs = append(xs, c.MinX, c.MaxX)
		}
	}
	if len(cs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	sort.Slice(cs, func(i, j int) bool { return cs[i].MinY < cs[j].MinY })
	area := 0.0
	for i := 0; i+1 < len(xs); i++ {
		x0, x1 := xs[i], xs[i+1]
		if x1 <= x0 {
			continue
		}
		// The rectangles spanning this slab, in MinY order: merge their
		// y intervals.
		covered, end, open := 0.0, 0.0, false
		start := 0.0
		for _, c := range cs {
			if c.MinX > x0 || c.MaxX < x1 {
				continue
			}
			if !open {
				start, end, open = c.MinY, c.MaxY, true
				continue
			}
			if c.MinY > end {
				covered += end - start
				start, end = c.MinY, c.MaxY
			} else if c.MaxY > end {
				end = c.MaxY
			}
		}
		if open {
			covered += end - start
		}
		area += covered * (x1 - x0)
	}
	return area
}

// sideBlockers are the rectangles of zone area `in` that movable parts on
// side cannot use: the keep-outs that reach that side, the board's own
// holes, and the parts that do not move (and bridges) which collide with
// that side — the same side, or through-hole. skip are parts left out
// (the zone's own).
func (pl *placer) sideBlockers(in Rect, side int, movable, skip map[*Part]bool) (blocked, fixed []Rect) {
	for _, k := range pl.b.Keepouts {
		if (k.NoParts || k.NoCopper) && (len(k.Layers) == 0 || k.onLayer(side)) {
			if r := PolyBounds(k.Poly); in.Overlaps(r) {
				blocked = append(blocked, r)
			}
		}
	}
	for _, h := range pl.b.Holes {
		if h.Owner == "" {
			if r := h.Bounds().Expand(h.Keep); in.Overlaps(r) {
				blocked = append(blocked, r)
			}
		}
	}
	for _, q := range pl.b.Parts {
		if skip[q] || movable[q] && !pl.c.IsBridge(q.Ref) {
			continue
		}
		if q.Side != side && !hasTHT(q) {
			continue
		}
		if r := q.Body().Expand(pl.spacing / 2); in.Overlaps(r) {
			fixed = append(fixed, r)
		}
	}
	return blocked, fixed
}

// sideFree is the area of `in` left to movable parts on side (mil²).
func (pl *placer) sideFree(in Rect, side int, movable map[*Part]bool) float64 {
	blocked, fixed := pl.sideBlockers(in, side, movable, nil)
	return math.Max(0, in.Area()-unionArea(in, append(blocked, fixed...)))
}

func (pl *placer) inRegion(r Rect) Rect {
	if r == pl.region {
		return r
	}
	g := pl.region
	return Rect{math.Max(r.MinX, g.MinX), math.Max(r.MinY, g.MinY), math.Min(r.MaxX, g.MaxX), math.Min(r.MaxY, g.MaxY)}
}

// zoneUnit is a set of parts that share one zone rectangle: a functional
// block of the domain (or one part without a block).
type zoneUnit struct {
	parts []*Part
	need  [3]float64 // by side (1 top, 2 bottom), with half the spacing
	area  float64
}

// shareRects gives every part one of the rectangles rs of its domain (or
// block): whole functional blocks together where they fit, each in the
// rectangle nearest what it connects to — the parts that do not move and
// the blocks already seated — and, without such a pull, in the emptiest
// one. A rectangle is filled to ZoneTightFill of its free area per side
// before a block goes to a farther one; when none has the room the block
// goes part by part, each where the least is missing. Returns how many
// parts each got.
func (pl *placer) shareRects(rs []Rect, parts []*Part, byBlock bool) []int {
	movable := map[*Part]bool{}
	for _, p := range pl.movable {
		movable[p] = true
	}
	free := make([][3]float64, len(rs))
	for i, r := range rs {
		in := pl.inRegion(r)
		for _, s := range []int{LayerTop, LayerBottom} {
			free[i][s] = ZoneTightFill * pl.sideFree(in, s, movable)
		}
	}
	// Units, in the parts' order.
	var units []*zoneUnit
	unitOf := map[string]*zoneUnit{}
	add := func(u *zoneUnit, p *Part) {
		a := p.Body().Expand(pl.spacing / 2).Area()
		u.parts = append(u.parts, p)
		u.area += a
		switch {
		case hasTHT(p):
			u.need[LayerTop] += a
			u.need[LayerBottom] += a
		case p.Side == LayerBottom:
			u.need[LayerBottom] += a
		default:
			u.need[LayerTop] += a
		}
	}
	for _, p := range parts {
		var u *zoneUnit
		if id := pl.c.BlockOf[p.Ref]; byBlock && id != "" {
			if u = unitOf[id]; u == nil {
				u = &zoneUnit{}
				unitOf[id] = u
				units = append(units, u)
			}
		} else {
			u = &zoneUnit{}
			units = append(units, u)
		}
		add(u, p)
	}
	// A block no rectangle can hold whole is shared part by part.
	whole := units
	units = nil
	for _, u := range whole {
		fits := len(u.parts) == 1
		for i := range rs {
			fits = fits || u.need[LayerTop] <= free[i][LayerTop] && u.need[LayerBottom] <= free[i][LayerBottom]
		}
		if fits {
			units = append(units, u)
			continue
		}
		for _, p := range u.parts {
			s := &zoneUnit{}
			add(s, p)
			units = append(units, s)
		}
	}
	sort.SliceStable(units, func(i, j int) bool { return units[i].area > units[j].area })
	partOf := map[string]*Part{}
	for _, p := range pl.b.Parts {
		partOf[p.Ref] = p
	}
	const railPads = 24 // a net with more pads is a rail: it pulls nowhere
	counts := make([]int, len(rs))
	for len(units) > 0 {
		u := units[0]
		units = units[1:]
		in := map[*Part]bool{}
		for _, p := range u.parts {
			in[p] = true
		}
		// Where the unit's nets lead: parts that stay put, and parts of
		// units seated before it (their rectangle's centre).
		var sum Point
		n := 0.0
		for _, p := range u.parts {
			for _, ni := range pl.partNet[p] {
				net := pl.nets[ni]
				if len(net.pads) > railPads {
					continue
				}
				for _, pd := range net.pads {
					q := partOf[pd.Part]
					if q == nil || in[q] {
						continue
					}
					var at Point
					if z, ok := pl.zoneOf[q]; ok && movable[q] {
						if z == pl.region {
							continue
						}
						at = z.Center()
					} else if !movable[q] {
						at = q.Body().Center()
					} else {
						continue
					}
					sum = sum.Add(Point{at.X * net.weight, at.Y * net.weight})
					n += net.weight
				}
			}
		}
		best, bestKey := -1, [3]float64{}
		for i, r := range rs {
			short := 0.0
			for _, s := range []int{LayerTop, LayerBottom} {
				short += math.Max(0, u.need[s]-free[i][s])
			}
			dist := 0.0
			if n > 0 {
				c := Point{sum.X / n, sum.Y / n}
				dist = rectDist(r, Rect{c.X, c.Y, c.X, c.Y})
			}
			// Least missing room first, then nearest, then emptiest.
			key := [3]float64{short, dist, -(free[i][LayerTop] + free[i][LayerBottom])}
			if best < 0 || key[0] < bestKey[0] || key[0] == bestKey[0] && (key[1] < bestKey[1] || key[1] == bestKey[1] && key[2] < bestKey[2]) {
				best, bestKey = i, key
			}
		}
		if bestKey[0] > 0 && len(u.parts) > 1 {
			// The rectangles have filled up since: no room for the block
			// whole any more, its parts go one by one (next in line).
			var single []*zoneUnit
			for _, p := range u.parts {
				s := &zoneUnit{}
				add(s, p)
				single = append(single, s)
			}
			sort.SliceStable(single, func(i, j int) bool { return single[i].area > single[j].area })
			units = append(single, units...)
			continue
		}
		for _, s := range []int{LayerTop, LayerBottom} {
			free[best][s] -= u.need[s]
		}
		for _, p := range u.parts {
			pl.zoneOf[p] = rs[best]
		}
		counts[best] += len(u.parts)
	}
	return counts
}
