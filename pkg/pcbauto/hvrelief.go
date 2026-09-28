package pcbauto

import "math"

// High-voltage footprint relief.
//
// A high-voltage net's clearance (IPC-2221B B2 at its peak: 2.5 mm at 325 V,
// 4.2 mm at 850 V) is larger than the pad-to-pad gap many parts rated for that
// voltage have in their own footprint: an MB10S bridge has its AC and DC pads
// 1.5 mm apart, a 1206 divider resistor 1.8 mm, a DPAK drain tab 2.5 mm from
// its gate lead. Applied literally, the neighbour pad's clearance zone covers
// the pad itself: the pad is unreachable (every pad of the MB10S came out
// "pad-inaccessible" on the flyback stress board) and DRC flags the unavoidable
// exit. The component's own spacing is certified with the part.
//
// Relief: near its own pad P (within the neck reach) copper of net n may come
// as close to another pad Q of the SAME part as P itself is — never closer —
// when the normal requirement between the two nets exceeds that gap. Copper
// therefore never gets closer to Q than the footprint already is, and outside
// the neck the full clearance applies. Boards whose nets all use the board
// clearance never enter this path.

// hvReliefReach is how far from its own pad a net keeps the relief (mil):
// enough to leave the neighbour's clearance zone in any direction.
func (r *router) hvReliefReach(n *rnet) float64 {
	return math.Max(math.Max(3*n.width, 30), r.maxClr+n.width)
}

// setupRelief enables the relief when some net needs more than the board
// clearance.
func (r *router) setupRelief() {
	base := r.b.Rules.Clearance
	r.maxClr = base
	for _, n := range r.nets {
		if n.plan.ClearanceMil > base+1e-9 {
			r.relief = true
		}
		r.maxClr = math.Max(r.maxClr, n.plan.ClearanceMil)
	}
	r.padGap = map[[2]*Pad]float64{}
	r.padShare = map[*Pad]float64{}
}

// reliefOwn returns n's pads whose relief reach covers column (x,y).
func (r *router) reliefOwn(n *rnet, x, y int) []*Pad {
	if n.relief == nil {
		n.relief = map[int32][]*Pad{}
		gr := r.gr
		reach := r.hvReliefReach(n)
		for _, g := range n.groups {
			for _, pd := range g {
				pd := pd
				gr.forCellsNear(pd.Box.Bounds(), reach, pd.Box.Dist, func(xx, yy int) {
					k := int32(yy*gr.W + xx)
					n.relief[k] = append(n.relief[k], pd)
				})
			}
		}
	}
	return n.relief[int32(y*r.gr.W+x)]
}

// footprintGap is the copper gap between two pads of one part.
func (r *router) footprintGap(a, b *Pad) float64 {
	k := [2]*Pad{a, b}
	if g, ok := r.padGap[k]; ok {
		return g
	}
	g, _, _ := polyDist(padPoly(a), padPoly(b))
	r.padGap[k] = g
	return g
}

// reliefOK is nodeOK's exact fallback near n's own pads on a high-voltage
// board: hard cells (edge, keep-outs, holes) still block; every other pad
// needs its full clearance except pads of a part n has a pad on, which need
// only the footprint's own gap to that pad (never less than the board rule).
func (r *router) reliefOK(n *rnet, l, x, y int, rad float64) bool {
	own := r.reliefOwn(n, x, y)
	if len(own) == 0 && len(r.an.spans) == 0 {
		return false
	}
	gr := r.gr
	offs, inner := gr.ring(rad)
	for k := 0; k < inner; k++ {
		xx, yy := x+offs[k][0], y+offs[k][1]
		if !gr.in(xx, yy) || gr.flags[gr.idx(l, xx, yy)]&flagHard != 0 {
			return false
		}
	}
	p := gr.center(x, y)
	id := gr.layers[l]
	hw := rad - n.share
	bx := int((p.X - gr.ox) / padBucket)
	by := int((p.Y - gr.oy) / padBucket)
	if bx < 0 || by < 0 || bx >= r.pbW || by >= r.pbH {
		return false
	}
	base := r.b.Rules.Clearance
	for _, e := range r.pbuckets[by*r.pbW+bx] {
		if e.net == n.id && e.net >= 0 {
			continue
		}
		if id != LayerMulti && !e.pd.OnLayer(id) {
			continue
		}
		req := r.pairReq(n, e)
		if req > base {
			for _, op := range own {
				if op.Part == e.pd.Part && op != e.pd {
					req = math.Max(base, math.Min(req, r.footprintGap(op, e.pd)))
				}
			}
		}
		if e.pd.Box.Dist(p) < hw+req-1e-6 {
			return false
		}
	}
	return true
}

// drcRelief lowers the pad requirement of a track/via next to a pad of a
// part its net also has a pad on: the copper may be as close to that pad as
// its own pad is, within the relief reach of its own pad. near is the copper
// point closest to the pad.
func drcRelief(b *Board, req float64, net string, pad *Pad, near Point, reach float64) float64 {
	if net == "" || pad == nil || req <= b.Rules.Clearance {
		return req
	}
	p := b.Part(pad.Part)
	if p == nil {
		return req
	}
	best := req
	for _, op := range p.Pads {
		if op == pad || op.Net != net || op.Box.Dist(near) > reach {
			continue
		}
		g, _, _ := polyDist(padPoly(op), padPoly(pad))
		best = math.Min(best, math.Max(b.Rules.Clearance, g))
	}
	return best
}

// reliefShare is the clearance share net n's copper claims next to its own
// pad pd: the footprint's own gap to the other-net pads of the part, split
// evenly when every neighbour net is high-voltage too (both sides shrink),
// else taken whole against a board-rule neighbour. Never below half the board
// rule, never above the net's full share.
func (r *router) reliefShare(n *rnet, pd *Pad) float64 {
	if s, ok := r.padShare[pd]; ok {
		return s
	}
	base := r.b.Rules.Clearance
	s := n.share
	p := r.b.Part(pd.Part)
	if p != nil {
		gm, allHV, any := math.Inf(1), true, false
		for _, q := range p.Pads {
			if q == pd || q.Net == pd.Net {
				continue
			}
			any = true
			gm = math.Min(gm, r.footprintGap(pd, q))
			if o := r.byName[q.Net]; o == nil || o.plan.ClearanceMil <= base+1e-9 {
				allHV = false
			}
		}
		if any {
			share := gm - base/2
			if allHV {
				share = gm / 2
			}
			s = math.Min(s, math.Max(base/2, share))
		}
	}
	r.padShare[pd] = s
	return s
}

// claimRadius is the radius net n claims at a node: nodeRadius, with the
// share shrunk to the footprint relief next to its own pads so two
// high-voltage tracks leaving adjacent pads of one part (a 1206 across a
// snubber, a bridge rectifier's AC pins) do not claim each other's pads.
func (r *router) claimRadius(n *rnet, l, x, y int) float64 {
	rad := r.nodeRadius(n, l, x, y)
	if rad <= 0 || !r.relief || n.share <= r.b.Rules.Clearance/2+1e-9 {
		return rad
	}
	s := n.share
	for _, pd := range r.reliefOwn(n, x, y) {
		s = math.Min(s, r.reliefShare(n, pd))
	}
	return rad - n.share + s
}

// drcTrackRelief lowers the requirement between two tracks (or a track and
// a via) that leave pads P and Q of the same part: they may be as close as
// the footprint puts P and Q, while both stay within the relief reach of
// their pads.
func drcTrackRelief(b *Board, req float64, a, c *drcItem, padsOf map[string][]*Pad, reach func(w float64) float64) float64 {
	if req <= b.Rules.Clearance || a.net == "" || c.net == "" {
		return req
	}
	near := func(it *drcItem, pd *Pad) bool {
		switch it.kind {
		case 1:
			return pd.Box.SegDist(it.t.A, it.t.B) <= reach(it.t.Width)
		case 2:
			return pd.Box.Dist(it.v.C) <= reach(it.v.Dia)
		}
		return false
	}
	best := req
	for _, p := range padsOf[a.net] {
		if !near(a, p) {
			continue
		}
		for _, q := range padsOf[c.net] {
			if q.Part != p.Part || !near(c, q) {
				continue
			}
			g, _, _ := polyDist(padPoly(p), padPoly(q))
			best = math.Min(best, math.Max(b.Rules.Clearance, g))
		}
	}
	return best
}

// ---- ΔV clearance -----------------------------------------------------------

// voltSpan is the voltage a net's copper takes: lo/hi over the solved
// scenarios, nom in the typical one; swing marks a net whose instantaneous
// voltage sweeps its span (switch node, AC line).
type voltSpan struct {
	lo, hi, nom float64
	swing       bool
	// anchor / relLo / relHi: a net riding on a switch node and its span
	// relative to it (the node itself has anchor = its own name, rel 0).
	anchor       string
	relLo, relHi float64
}

// intentSpans reads every intent net's voltage span.
func intentSpans(b *Board, in *Intent) map[string]voltSpan {
	out := map[string]voltSpan{}
	if in == nil {
		return out
	}
	for _, n := range b.Nets() {
		in := intentLookup(in, n.Name)
		if in == nil {
			continue
		}
		v := in.Voltage
		sp := voltSpan{lo: math.Min(v.Min, v.Max), hi: math.Max(v.Min, v.Max), nom: v.Nom}
		if v.Peak > sp.hi+1e-9 {
			sp.hi, sp.lo, sp.swing = v.Peak, math.Min(sp.lo, 0), true
		}
		if sp.lo < 0 {
			sp.lo, sp.swing = -math.Max(-sp.lo, v.Peak), true
		}
		if in.FloatsOn != "" && in.RelVoltage != nil {
			sp.anchor, sp.relLo, sp.relHi = in.FloatsOn, math.Min(in.RelVoltage.Min, in.RelVoltage.Max), math.Max(in.RelVoltage.Min, in.RelVoltage.Max)
		}
		out[n.Name] = sp
	}
	// The switch nodes themselves are the anchors of their islands.
	for name, sp := range out {
		if sp.anchor == "" {
			continue
		}
		if a, ok := out[sp.anchor]; ok && a.anchor == "" {
			a.anchor = sp.anchor
			out[sp.anchor] = a
		}
		_ = name
	}
	return out
}

// PairClearanceMil is the copper clearance two nets need from each other:
// the larger of their own (IPC-2221B at their peak to the reference), but
// between two nets of the same insulation domain whose voltage spans are
// both known only IPC-2221B at the largest difference that can appear
// between them. Adjacent nodes of an 850 V divider chain are 141 V apart,
// not 850 V: the absolute rule made the chain's own row pitch illegal.
// Pairs of different domains keep their insulation pair requirement
// (applied separately); unknown spans keep the conservative absolute rule.
func (a *Analysis) PairClearanceMil(x, y string, r Rules) float64 {
	if a == nil {
		return r.Clearance
	}
	cx, cy := a.Plan(x, r).ClearanceMil, a.Plan(y, r).ClearanceMil
	req := math.Max(r.Clearance, math.Max(cx, cy))
	if req <= r.Clearance || len(a.spans) == 0 {
		return req
	}
	sx, okx := a.spans[x]
	sy, oky := a.spans[y]
	if !okx || !oky {
		return req
	}
	if a.Iso != nil && a.Iso.NetDomain[x] != a.Iso.NetDomain[y] {
		return req
	}
	// Two DC nodes of one circuit (a divider chain) are compared like with
	// like — both at their maximum, both typical (every source on) — not one
	// node's maximum against the other's source-off zero (a scenario minimum
	// is usually "that supply disconnected", when both nodes are dead). A
	// sweeping net (switch node, AC) is compared across its whole span.
	var dv float64
	if sx.anchor != "" && sx.anchor == sy.anchor {
		// Both ride on the same switch node (or one is the node): they move
		// together — only their relative voltages differ.
		dv = math.Max(sx.relHi-sy.relLo, sy.relHi-sx.relLo)
	} else if sx.swing || sy.swing {
		dv = math.Max(sx.hi-sy.lo, sy.hi-sx.lo)
	} else {
		dv = math.Max(math.Abs(sx.hi-sy.hi), math.Abs(sx.nom-sy.nom))
	}
	return math.Max(r.Clearance, math.Min(req, ClearanceForVoltage(dv, true, a.coated)))
}

// pairReq is the clearance net n needs from pad entry e (ΔV-aware).
func (r *router) pairReq(n *rnet, e padEntry) float64 {
	if e.net < 0 || len(r.an.spans) == 0 {
		return math.Max(e.clr, n.plan.ClearanceMil)
	}
	return r.an.PairClearanceMil(n.name, e.pd.Net, r.b.Rules)
}
