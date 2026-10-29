package pcbauto

// placer_resolve.go — no courtyard overlap is accepted silently.
//
// Why: on a 1443-part board with intent isolation domains, 6–11 courtyard
// overlaps per seed survived legalisation. The legaliser minimises one sum
// (overlap 8/mil², isolation shortfall 800/mil, zone exit …), so in a
// crowded domain it bought a small overlap instead of a creepage shortfall
// or a zone exit. resolveOverlaps is the strict last word: an overlapping
// part moves only to a pose with no courtyard overlap at all, inside its
// own zone, and no worse on every isolation, keep-out and edge term; what
// it cannot resolve stays illegal and is named (PlaceResult.Illegal), and
// zoneFill says whether the zone has the room at all.

import (
	"math"
	"sort"
	"time"
)

// ZoneFill is one placement zone's room: the courtyards (plus half the
// placement spacing) of the movable parts assigned to it and of the fixed
// parts inside it, against the zone's area inside the placement region
// minus keep-outs and holes (mm²).
type ZoneFill struct {
	Domain  string  `json:"domain"` // "board": parts without a domain zone
	Parts   int     `json:"parts"`
	NeedMm2 float64 `json:"needMm2"`
	// FixedMm2 is the part of NeedMm2 that parts which do not move (fixed,
	// mechanical) and bridges inside the zone take.
	FixedMm2 float64 `json:"fixedMm2,omitempty"`
	// OfferMm2 is the usable area; Fill = NeedMm2 / OfferMm2 (> 1: over-full,
	// no placement can be legal; above ZoneTightFill packing rarely
	// succeeds).
	OfferMm2 float64 `json:"offerMm2"`
	// Fill is null when the zone offers no usable area at all.
	Fill *float64 `json:"fill"`
	// OverFull: the parts cannot fit (fill > 1, or parts but no usable
	// area); Tight: fill above ZoneTightFill.
	OverFull bool `json:"overFull,omitempty"`
	Tight    bool `json:"tight,omitempty"`
	// Illegal counts the illegal placements (overlaps, zone exits) of its
	// parts in the final placement.
	Illegal int `json:"illegal,omitempty"`
}

const mil2ToMm2 = 0.00064516

// ZoneTightFill is the fill above which a zone is reported as practically
// full: rectangles of mixed sizes rarely pack denser.
const ZoneTightFill = 0.85

// resolveBudget bounds resolveOverlaps (it runs past PlaceOptions.Deadline:
// legality comes before the budget, but not without end).
const resolveBudget = 30 * time.Second

// courtyardGap is the courtyard-to-courtyard gap resolveOverlaps keeps:
// courtyards apart (KiCad's courtyard DRC) with a hair of margin, far below
// the routing channel the legaliser asks for — legal, not roomy.
const courtyardGap = 2.0

// overlapPairs are the colliding part pairs whose courtyards overlap by
// more than 1 mil² (metrics' Overlaps; resolveOverlaps can move only the
// movable ones).
func (pl *placer) overlapPairs() [][2]*Part {
	var out [][2]*Part
	parts := pl.b.Parts
	for i, p := range parts {
		bi := p.Body()
		for _, q := range parts[i+1:] {
			if collide(p, q) && bi.OverlapArea(q.Body()) > 1 {
				out = append(out, [2]*Part{p, q})
			}
		}
	}
	return out
}

// resolveOverlaps moves one part of every overlapping pair (the smaller
// first, then the other) to the nearest pose that clears every courtyard,
// stays in its zone and worsens no hard term (resolvePart). Returns how
// many pairs it cleared.
func (pl *placer) resolveOverlaps() int {
	pairs := pl.overlapPairs()
	if len(pairs) == 0 {
		return 0
	}
	movable := map[*Part]bool{}
	for _, p := range pl.movable {
		movable[p] = true
	}
	pl.rebuildBuckets()
	stop := time.Now().Add(resolveBudget)
	cleared := 0
	for _, pr := range pairs {
		a, b := pr[0], pr[1]
		if !collide(a, b) || a.Body().OverlapArea(b.Body()) <= 1 {
			cleared++ // an earlier move cleared it
			continue
		}
		if a.Body().Area() > b.Body().Area() || a.Body().Area() == b.Body().Area() && a.Ref > b.Ref {
			a, b = b, a
		}
		for _, p := range []*Part{a, b} {
			if !movable[p] || time.Now().After(stop) {
				continue
			}
			if pl.resolvePart(p, stop) {
				cleared++
				break
			}
		}
	}
	return cleared
}

// resolveCost is what resolvePart must not worsen, every hard term but the
// neighbour overlap (it demands none at all) on its own: a candidate may
// not buy one term with another (leave its zone to clear a keep-out, or
// open a creepage shortfall to a new pad while closing an old one).
type resolveCost struct {
	board, band, zoneCentre, zoneBody float64
	keepouts, holes, heights          []float64 // per keep-out / hole / height zone, in board order
	// gaps is every pad pair short of its isolation (creepage) or HV
	// clearance: mil short, per pair and kind.
	gaps map[resolveGap]float64
}

type resolveGap struct {
	a, c *Pad
	hv   bool
}

func (pl *placer) resolveTerms(p *Part) resolveCost {
	bx := pl.boxes[p]
	c := resolveCost{gaps: map[resolveGap]float64{}}
	if !p.Fixed {
		c.board = 20 * outside(bx, pl.region)
		c.band = 40 * padsOutside(pl.edgeBand[p])
		z := pl.zoneOf[p]
		cen := bx.Center()
		dx := math.Max(0, math.Max(z.MinX-cen.X, cen.X-z.MaxX))
		dy := math.Max(0, math.Max(z.MinY-cen.Y, cen.Y-z.MaxY))
		c.zoneCentre = 30 * (dx + dy) * math.Max(bx.W(), bx.H())
		if !pl.c.IsBridge(p.Ref) && z != pl.region {
			c.zoneBody = 20 * outside(bx, z)
		}
	}
	for _, k := range pl.b.Keepouts {
		if (k.NoParts || k.NoCopper) && k.Owner != p.Ref && !(pl.c.IsBridge(p.Ref) && !k.NoParts) {
			c.keepouts = append(c.keepouts, 20*bx.OverlapArea(PolyBounds(k.Poly)))
		}
	}
	for _, h := range pl.b.Holes {
		if h.Owner != p.Ref {
			c.holes = append(c.holes, 20*bx.OverlapArea(h.Bounds().Expand(h.Keep)))
		}
	}
	for _, hz := range pl.m.HeightZones {
		if p.Height > hz.MaxHeight {
			c.heights = append(c.heights, 20*bx.OverlapArea(hz.Rect))
		}
	}
	pl.padShortfalls(p, bx, c.gaps)
	return c
}

// padShortfalls records every pad pair of p short of its isolation
// creepage (isoCost's pairs) or HV clearance (hvPadCost's pairs), by how
// many mil.
func (pl *placer) padShortfalls(p *Part, bx Rect, out map[resolveGap]float64) {
	if pl.an == nil {
		return
	}
	if iso := pl.an.Iso; iso != nil && len(iso.Pairs) > 0 {
		pl.forBucketsOnce(bx.Expand(iso.MaxClearanceMil()), func(q *Part) bool {
			if q == p {
				return true
			}
			for _, a := range p.Pads {
				da := iso.NetDomain[a.Net]
				if da == "" {
					continue
				}
				for _, c := range q.Pads {
					if ip := iso.Pair(da, iso.NetDomain[c.Net]); ip != nil {
						if short := ip.CreepageMil - isoPadGap(a, c, ip.CreepageMil); short > 0 {
							out[resolveGap{a, c, false}] = short
						}
					}
				}
			}
			return true
		})
	}
	if pl.hvReach > 0 {
		base := pl.b.Rules.Clearance
		pl.forBucketsOnce(bx.Expand(pl.hvReach), func(q *Part) bool {
			if q == p {
				return true
			}
			for _, a := range p.Pads {
				if a.Net == "" {
					continue
				}
				for _, c := range q.Pads {
					if c.Net == "" || c.Net == a.Net {
						continue
					}
					req := pl.an.PairClearanceMil(a.Net, c.Net, pl.b.Rules)
					if req <= base+hvExcessMil {
						continue
					}
					if short := req - isoPadGap(a, c, req); short > 0 {
						out[resolveGap{a, c, true}] = short
					}
				}
			}
			return true
		})
	}
}

// noWorse reports whether every term of t is at most the same term of
// before (1e-6 slack): the board, the edge band, the zone (a part inside
// its zone stays inside), each keep-out, hole and height zone, and each
// pad pair's isolation or HV shortfall — a pair short now must have been
// short by as much before.
func (t resolveCost) noWorse(before resolveCost) bool {
	const eps = 1e-6
	if t.board > before.board+eps || t.band > before.band+eps || t.zoneCentre > before.zoneCentre+eps || t.zoneBody > before.zoneBody+eps {
		return false
	}
	for _, l := range [][2][]float64{{t.keepouts, before.keepouts}, {t.holes, before.holes}, {t.heights, before.heights}} {
		for i, v := range l[0] {
			if i >= len(l[1]) || v > l[1][i]+eps {
				return false
			}
		}
	}
	for k, v := range t.gaps {
		if v > before.gaps[k]+eps {
			return false
		}
	}
	return true
}

// courtyardClear reports whether body (grown by half the courtyard gap)
// touches no colliding part's courtyard (grown alike).
func (pl *placer) courtyardClear(p *Part, body Rect) bool {
	g := body.Expand(courtyardGap / 2)
	clear := true
	pl.forBucketsOnce(g.Expand(pl.spacing), func(q *Part) bool {
		if q != p && collide(p, q) && g.OverlapArea(q.Body().Expand(courtyardGap/2)) > 0 {
			clear = false
		}
		return clear
	})
	return clear
}

// resolvePart moves p to the nearest pose (ring by ring around it, its own
// rotation first) with clear courtyards and no hard term worse than now,
// the lowest local cost of that ring; false (p unmoved) when there is none
// within reach.
func (pl *placer) resolvePart(p *Part, stop time.Time) bool {
	pos0, rot0 := p.Pos, p.Rotation
	orig := p.Body().Center()
	before := pl.resolveTerms(p)
	pl.bucketOp(p, false)
	step := math.Max(math.Min(pl.spacing, 10), 5)
	rots := []float64{rot0}
	if !pl.opt.NoRotate {
		rots = append(rots, normDeg(rot0+90), normDeg(rot0+180), normDeg(rot0+270))
	}
	halo := pl.spacing/2 + pl.opt.Halo[p.Ref]
	sc := &spiralCtx{p: p, z: pl.zoneOf[p], zoneBody: !pl.c.IsBridge(p.Ref) && pl.zoneOf[p] != pl.region}
	found := false
	var bestPos Point
	var bestRot float64
	const reach = 2000.0 // mil: a resolve is a local repair, not a re-placement
	for ring := 1; float64(ring)*step <= reach && !found; ring++ {
		if ring&7 == 0 && time.Now().After(stop) {
			break
		}
		r := float64(ring) * step
		cs := spiralRing(ring)
		best := math.Inf(1)
		for _, rot := range rots {
			nrot := normDeg(rot)
			for k := range cs {
				c := orig.Add(Point{r * cs[k].X, r * cs[k].Y})
				if rel, ok := p.relBody[nrot]; ok && len(p.Pads) > 0 {
					// Courtyard check on the computed pose first (as spiral).
					off := rel.Translate(p.Pos).Center().Sub(p.Pos)
					pos := c.Sub(off)
					body := rel.Translate(pos)
					if !pl.courtyardClear(p, body) || !pl.zoneNoWorse(sc, body.Expand(halo), before) {
						p.Pos, p.Rotation = pos, nrot
						continue
					}
				}
				movePartCentre(p, c, rot)
				pl.boxes[p] = pl.box(p)
				if !pl.courtyardClear(p, p.Body()) {
					continue
				}
				if !pl.resolveTerms(p).noWorse(before) {
					continue
				}
				if cost := pl.localCost(p, nil); cost < best {
					best, bestPos, bestRot, found = cost, p.Pos, p.Rotation, true
				}
			}
		}
	}
	if found {
		p.MoveTo(bestPos, bestRot)
	} else {
		p.MoveTo(pos0, rot0)
	}
	pl.boxes[p] = pl.box(p)
	pl.bucketOp(p, true)
	return found
}

// zoneNoWorse is resolveCost.noWorse's board and zone terms for a box bx
// (no pads needed): the cheap first test of a candidate.
func (pl *placer) zoneNoWorse(sc *spiralCtx, bx Rect, before resolveCost) bool {
	if sc.p.Fixed {
		return true
	}
	t1, t3, t4 := pl.zoneTerms(sc, bx)
	const eps = 1e-6
	return t1 <= before.board+eps && t3 <= before.zoneCentre+eps && t4 <= before.zoneBody+eps
}

// zoneFill measures every placement zone's room (ZoneFill) and counts the
// illegal placements of its parts, zones in board order of their first
// part. res.Zones names the domain zones; the rest is "board".
func (pl *placer) zoneFill(res *PlaceResult, pairs [][2]*Part) []ZoneFill {
	type zk struct {
		r           Rect
		fill        ZoneFill
		need, fixed float64
		parts       map[*Part]bool
	}
	var zs []*zk
	byRect := map[Rect]*zk{}
	movable := map[*Part]bool{}
	for _, p := range pl.movable {
		movable[p] = true
	}
	name := func(r Rect) string {
		if r == pl.region {
			return "board"
		}
		for _, z := range res.Zones {
			if z.Rect == r && z.Domain != "" {
				return z.Domain
			}
		}
		return "zone"
	}
	for _, p := range pl.movable {
		if pl.c.IsBridge(p.Ref) {
			continue // a bridge sits on the strip between zones
		}
		r := pl.zoneOf[p]
		k := byRect[r]
		if k == nil {
			k = &zk{r: r, fill: ZoneFill{Domain: name(r)}, parts: map[*Part]bool{}}
			byRect[r] = k
			zs = append(zs, k)
		}
		k.fill.Parts++
		k.parts[p] = true
		k.need += p.Body().Expand(pl.spacing / 2).Area()
	}
	for _, k := range zs {
		in := k.r
		if in != pl.region {
			// The zone's part inside the placement region.
			in = Rect{math.Max(in.MinX, pl.region.MinX), math.Max(in.MinY, pl.region.MinY), math.Min(in.MaxX, pl.region.MaxX), math.Min(in.MaxY, pl.region.MaxY)}
		}
		offer := in.Area()
		for _, kp := range pl.b.Keepouts {
			if kp.NoParts || kp.NoCopper {
				offer -= in.OverlapArea(PolyBounds(kp.Poly))
			}
		}
		for _, h := range pl.b.Holes {
			if h.Owner == "" {
				offer -= in.OverlapArea(h.Bounds().Expand(h.Keep))
			}
		}
		for _, q := range pl.b.Parts {
			if !k.parts[q] && (!movable[q] || pl.c.IsBridge(q.Ref)) {
				// Parts that do not move and bridges take their room from
				// the zone (zoneOccupied counts them alike).
				a := in.OverlapArea(q.Body().Expand(pl.spacing / 2))
				k.need += a
				k.fixed += a
			}
		}
		offer = math.Max(offer, 0)
		k.fill.NeedMm2 = math.Round(k.need*mil2ToMm2*10) / 10
		k.fill.OfferMm2 = math.Round(offer*mil2ToMm2*10) / 10
		k.fill.FixedMm2 = math.Round(k.fixed*mil2ToMm2*10) / 10
		if offer > 0 {
			f := math.Round(k.need/offer*100) / 100
			k.fill.Fill = &f
			k.fill.OverFull, k.fill.Tight = k.need > offer, f > ZoneTightFill
		} else {
			k.fill.OverFull = k.need > 0
		}
		for _, pr := range pairs {
			if k.parts[pr[0]] || k.parts[pr[1]] {
				k.fill.Illegal++
			}
		}
		for p := range k.parts {
			if z := k.r; z != pl.region && outside(p.Body(), z) > 1 {
				k.fill.Illegal++
			}
		}
	}
	out := make([]ZoneFill, len(zs))
	for i, k := range zs {
		out[i] = k.fill
	}
	return out
}

// illegalList names every illegal placement of the final layout: the
// overlapping courtyard pairs and the parts outside the board or their
// zone, sorted.
func (pl *placer) illegalList(pairs [][2]*Part) []string {
	var out []string
	for _, pr := range pairs {
		a, b := pr[0].Ref, pr[1].Ref
		if a > b {
			a, b = b, a
		}
		out = append(out, sprintf("overlap %s %s (%.0f mil²)", a, b, pr[0].Body().OverlapArea(pr[1].Body())))
	}
	for _, p := range pl.b.Parts {
		if p.Fixed {
			continue
		}
		bi := p.Body()
		if len(pl.b.Outline) >= 3 {
			for _, c := range bi.Corners() {
				if !PolyContains(pl.b.Outline, c) {
					out = append(out, "outside the board "+p.Ref)
					break
				}
			}
		}
		if z, ok := pl.zoneOf[p]; ok && z != pl.region && !pl.c.IsBridge(p.Ref) && outside(bi, z) > 1 {
			out = append(out, "outside its zone "+p.Ref)
		}
	}
	sort.Strings(out)
	return out
}
