package pcbauto

import "math"

// Differential-pair corridors in placement.
//
// A pair routes coupled only where the space between its endpoints is free:
// on the ESP32 mini a USB-C CC resistor (R4) sat in the USBLC6 → CH340 run
// and the pair split round it (0 mil coupled, 2/0 vias, 307 mil uncoupled
// at the CH340 end); with R3/R4 moved by hand the pair router coupled the
// whole run. The placer now keeps, for every intent-declared differential
// pair (both legs carry an intent interface), a corridor along the pair's
// chain — connector → protection / series parts → IC — clear of every part
// that has no pad on the pair: a part whose body reaches into the corridor
// pays per mil of intrusion. The corridor follows the midline of the two
// legs' pad-to-pad skeletons between parts (not inside a part: an ESD array
// or a series part is the pair's own), and is as wide as the legs are apart
// plus one pair pitch and a clearance on each side.
//
// The term is soft and weighted below the electrical tethers (decap 6–7,
// protection 5: an IC's decoupler next to the pair's pins and the ESD array
// on the line stay put) and above the routing-comfort ones (pull 2, signal
// 1.5): a pull-up, a CC resistor or an LED moves out of the way first.

// corridorWeight is the cost per mil of corridor intrusion.
const corridorWeight = 4.0

type pairCorridor struct {
	p, n    *SignalChain
	related map[*Part]bool // parts with a pad on either leg
	margin  float64        // pair pitch + clearance, each side of the legs
}

// corridorPoint is one skeleton point of a leg and the part it belongs to.
type corridorPoint struct {
	at   Point
	part string
}

// setupCorridors collects the intent-declared pairs whose chains run
// through placeable parts.
func (pl *placer) setupCorridors() {
	pl.corridors = nil
	seen := map[*SignalChain]bool{}
	for _, ch := range pl.c.Chains {
		if ch.Pair == nil || seen[ch] || ch.conn == nil || ch.Pair.conn == nil {
			continue
		}
		seen[ch], seen[ch.Pair] = true, true
		np, nn := pl.an.ByNet[ch.Nets[0]], pl.an.ByNet[ch.Pair.Nets[0]]
		if np == nil || nn == nil || np.Interface == "" || nn.Interface == "" {
			continue
		}
		nets := map[string]bool{}
		for _, n := range append(append([]string(nil), ch.Nets...), ch.Pair.Nets...) {
			nets[n] = true
		}
		cr := &pairCorridor{p: ch, n: ch.Pair, related: map[*Part]bool{}}
		w := math.Max(np.WidthMil, pl.b.Rules.TrackWidth)
		cr.margin = w + pairGap(np, pl.b.Rules) + pl.b.Rules.Clearance
		for _, q := range pl.b.Parts {
			for _, pd := range q.Pads {
				if nets[pd.Net] {
					cr.related[q] = true
					break
				}
			}
		}
		pl.corridors = append(pl.corridors, cr)
	}
}

// corridorSkeleton is a leg's pad-to-pad path with the part of each point:
// from the connector pad that makes the shortest path (a USB-C receptacle
// carries each leg twice).
func corridorSkeleton(b *Board, ch *SignalChain) []corridorPoint {
	var best []Point
	bestLen := math.Inf(1)
	for _, sp := range connPadsOf(b, ch) {
		pts := chainSkeletonFrom(b, ch, sp)
		l := 0.0
		for i := 1; i < len(pts); i++ {
			l += pts[i-1].Dist(pts[i])
		}
		if l < bestLen {
			best, bestLen = pts, l
		}
	}
	// Tag each point with its part (skeleton points are pad centres).
	out := make([]corridorPoint, 0, len(best))
	for _, at := range best {
		out = append(out, corridorPoint{at: at, part: padPartAt(b, ch, at)})
	}
	return out
}

// padPartAt finds the chain part whose pad centre is at.
func padPartAt(b *Board, ch *SignalChain, at Point) string {
	refs := []string{}
	if ch.conn != nil {
		refs = append(refs, ch.conn.Part)
	}
	for _, n := range ch.Nodes {
		refs = append(refs, n.Ref)
	}
	if ch.ic != nil {
		refs = append(refs, ch.ic.Part)
	}
	for _, r := range refs {
		if p := b.Part(r); p != nil {
			for _, pd := range p.Pads {
				if pd.Box.C.Dist(at) < 1e-6 {
					return r
				}
			}
		}
	}
	return ""
}

// corridorSeg is one stretch of the corridor between two parts.
type corridorSeg struct {
	a, b   Point
	ra, rb float64 // half-width at a and b
}

// segs returns the corridor stretches of cr on the current placement: the
// midline of the two legs between consecutive skeleton points that belong
// to different parts. nil when the legs' skeletons do not line up.
func (pl *placer) corridorSegs(cr *pairCorridor) []corridorSeg {
	a, c := corridorSkeleton(pl.b, cr.p), corridorSkeleton(pl.b, cr.n)
	if len(a) != len(c) || len(a) < 2 {
		return nil
	}
	var out []corridorSeg
	for i := 1; i < len(a); i++ {
		if a[i].part == a[i-1].part && c[i].part == c[i-1].part {
			continue // through a part of the pair's own
		}
		ma, mb := a[i-1].at.Add(c[i-1].at).Scale(0.5), a[i].at.Add(c[i].at).Scale(0.5)
		out = append(out, corridorSeg{a: ma, b: mb,
			ra: a[i-1].at.Dist(c[i-1].at)/2 + cr.margin, rb: a[i].at.Dist(c[i].at)/2 + cr.margin})
	}
	return out
}

// intrusion is how far body r reaches into the corridor stretches (mil,
// summed over stretches).
func corridorIntrusion(segs []corridorSeg, r Rect) float64 {
	s := 0.0
	for _, sg := range segs {
		rad := math.Max(sg.ra, sg.rb)
		bb := Rect{math.Min(sg.a.X, sg.b.X), math.Min(sg.a.Y, sg.b.Y), math.Max(sg.a.X, sg.b.X), math.Max(sg.a.Y, sg.b.Y)}.Expand(rad)
		if !bb.Overlaps(r) {
			continue
		}
		d, t := segRectDist(sg.a, sg.b, r)
		if w := sg.ra + t*(sg.rb-sg.ra); d < w {
			s += w - d
		}
	}
	return s
}

// segRectDist is the distance between segment ab and rectangle r (0 when
// they touch) and the fraction along ab of the closest point.
func segRectDist(a, b Point, r Rect) (float64, float64) {
	if r.Contains(a) {
		return 0, 0
	}
	if r.Contains(b) {
		return 0, 1
	}
	cs := r.Corners()
	for i := range cs {
		if segsIntersect(a, b, cs[i], cs[(i+1)%len(cs)]) {
			// Straddling the midline: a negative distance, the depth the
			// body must still move to clear the line (the shorter side).
			u := b.Sub(a)
			l := math.Hypot(u.X, u.Y)
			if l == 0 {
				return 0, 0
			}
			nx, ny := -u.Y/l, u.X/l
			pos, neg := 0.0, 0.0
			for _, c := range cs {
				sd := (c.X-a.X)*nx + (c.Y-a.Y)*ny
				pos, neg = math.Max(pos, sd), math.Max(neg, -sd)
			}
			_, along := segDist(r.Center(), a, b)
			return -math.Min(pos, neg), along / l
		}
	}
	best, bt := math.Inf(1), 0.0
	l := a.Dist(b)
	for _, c := range cs {
		d, along := segDist(c, a, b)
		if d < best {
			best, bt = d, 0
			if l > 0 {
				bt = along / l
			}
		}
	}
	for _, e := range []Point{a, b} {
		if d := distToRect(e, r); d < best {
			best = d
			bt = 0
			if e == b {
				bt = 1
			}
		}
	}
	return best, bt
}

// corridorCost prices p against the corridors: a part off the pair pays
// its own intrusion; a part of the pair (connector, ESD, series, IC) pays
// every foreign part's intrusion, since moving it moves the corridor.
func (pl *placer) corridorCost(p *Part) float64 {
	if len(pl.corridors) == 0 {
		return 0
	}
	cost := 0.0
	for _, cr := range pl.corridors {
		segs := pl.corridorSegs(cr)
		if len(segs) == 0 {
			continue
		}
		if !cr.related[p] {
			cost += corridorIntrusion(segs, p.Body())
			continue
		}
		for _, q := range pl.b.Parts {
			if !cr.related[q] {
				cost += corridorIntrusion(segs, q.Body())
			}
		}
	}
	return corridorWeight * cost
}

// CorridorIntrusion reports, for every intent-declared differential pair,
// the parts off the pair whose bodies reach into its corridor on the
// current placement (ref → mil of intrusion). Diagnostics and tests.
func CorridorIntrusion(b *Board, an *Analysis, c *Circuit) map[string]float64 {
	pl := &placer{b: b, an: an, c: c}
	pl.setupCorridors()
	out := map[string]float64{}
	for _, cr := range pl.corridors {
		segs := pl.corridorSegs(cr)
		for _, q := range b.Parts {
			if cr.related[q] {
				continue
			}
			if v := corridorIntrusion(segs, q.Body()); v > 0 {
				out[q.Ref] += round2(v)
			}
		}
	}
	return out
}

// corridorChain reports a chain of an intent-declared pair.
func (pl *placer) corridorChain(ch *SignalChain) bool {
	for _, cr := range pl.corridors {
		if cr.p == ch || cr.n == ch {
			return true
		}
	}
	return false
}

// pairFlowMil prices a pass-through or series part of an intent-declared
// pair whose pin row faces the wrong way: per end, up to this many mil of
// detour for a row lying along the approach (the far leg must pass the near
// pin) and as much again for a row facing away from it.
const pairFlowMil = 150.0

// pairFlowCost is the orientation term for p on the corridors it carries:
// at every entry (exit) of p on a pair chain, the row joining the two legs'
// pads should lie across the direction from the previous (to the next)
// point of the pair, and the pads should be reached from that side of the
// part. The ESP32 mini USBLC6 turned with its entry row along the USB-C →
// ESD approach: D+ had to pass the D− pin and took two vias (2026-09-30).
func (pl *placer) pairFlowCost(p *Part) float64 {
	cost := 0.0
	for _, cr := range pl.corridors {
		if !cr.related[p] {
			continue
		}
		a, c := corridorSkelPads(pl.b, cr.p), corridorSkelPads(pl.b, cr.n)
		if len(a) != len(c) || len(a) < 2 {
			continue
		}
		for i := range a {
			if a[i].pad.Part != p.Ref || c[i].pad.Part != p.Ref {
				continue
			}
			mid := a[i].pad.Box.C.Add(c[i].pad.Box.C).Scale(0.5)
			row := c[i].pad.Box.C.Sub(a[i].pad.Box.C)
			rl := math.Hypot(row.X, row.Y)
			for _, k := range []int{i - 1, i + 1} {
				if k < 0 || k >= len(a) || a[k].pad.Part == p.Ref || (k < i && !a[i].entry) || (k > i && !a[i].exit) {
					continue
				}
				o := a[k].pad.Box.C.Add(c[k].pad.Box.C).Scale(0.5)
				d := o.Sub(mid)
				dl := math.Hypot(d.X, d.Y)
				if dl < 1e-6 {
					continue
				}
				along := 0.0
				if rl > 1e-6 {
					along = math.Abs(row.X*d.X+row.Y*d.Y) / (rl * dl)
				}
				// Facing: the pads' access side toward the other point.
				acc := padAccess(pl.b, a[i].pad).Add(padAccess(pl.b, c[i].pad)).Scale(0.5).Sub(mid)
				al := math.Hypot(acc.X, acc.Y)
				facing := 1.0
				if al > 1e-6 {
					facing = (acc.X*d.X + acc.Y*d.Y) / (al * dl)
				}
				cost += pairFlowMil * cr.p.Weight * (along + (1-facing)/2)
			}
		}
	}
	return cost
}

// corridorSkelPads is corridorSkeleton with the pads (entry/exit flags).
func corridorSkelPads(b *Board, ch *SignalChain) []skelPad {
	var best []skelPad
	bestLen := math.Inf(1)
	for _, sp := range connPadsOf(b, ch) {
		pts := chainSkeletonPads(b, ch, sp)
		l := 0.0
		for i := 1; i < len(pts); i++ {
			l += pts[i-1].pad.Box.C.Dist(pts[i].pad.Box.C)
		}
		if l < bestLen {
			best, bestLen = pts, l
		}
	}
	return best
}
