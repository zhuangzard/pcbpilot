package pcbauto

import (
	"math"
)

// Differential pairs are routed as a unit (pairsi.go measures the result).
//
// The leader leg is routed first; the follower is then routed against it
// under pair constraints:
//
//   - the cells one pair pitch (width + gap, from the impedance target) off
//     the leader's centreline on the same layer are cheap (the coupled
//     path); every other cell costs more, except in the breakout zone round
//     the follower's own pads, where the pins force the legs apart;
//   - a layer the leader does not use costs more (same layer sequence);
//   - a via is cheap only in the ring beside one of the leader's vias (the
//     vias go in pairs, symmetrically at the same transition).
//
// The constraints are costs, not walls: a pair that cannot run coupled is
// still connected (completion ranks above coupling) and CheckSI reports it.
// When the follower comes out poorly coupled or with a different via count,
// the other leg leads instead and the better unit is kept. Negotiation rips
// up and re-routes the pair atomically (a conflict on either leg moves both).

// Pair constraint factors (step-cost multipliers). The field factor keeps
// the old default discount's order (0.55 → 0.4: the field now competes with
// an off-field penalty instead of a bare discount).
const (
	pairFieldFac = 0.4 // on the coupled offset path
	pairOffFac   = 2.0 // off the path, outside the breakout zone
	pairLayerFac = 3.0 // on a layer the leader does not use
	pairViaFac   = 4.0 // a via not beside a leader via
)

// pairCons is the follower's constraint set while it is searched.
type pairCons struct {
	field    map[int32]float32 // layer-cells at the pair pitch → cost factor
	layerOK  []bool            // grid layers the leader runs on
	viaOK    map[int32]bool    // columns (y·W+x) beside a leader via
	breakout map[int32]bool    // columns near the follower's own pads
	// off, layer, via are the penalty factors after negotiation decay.
	off, layer, via float32
}

// pairDecay is how much of the pair penalties survive negotiation round k:
// full in the first routing, halving each round after. The coupled path
// stays cheapest (the legacy 0.55 discount at worst), but a pair squeezed
// between other pairs at a dense connector can give way — undecayed, the
// penalties kept three USB3 pairs fighting over the same J1 escape for 40
// rounds (usb3-typec: 7 conflicts, 95 % instead of 100 %).
func pairDecay(round int) float32 {
	return float32(math.Pow(0.5, float64(max(round, 0))))
}

func decayed(full float32, d float32) float32 { return 1 + (full-1)*d }

// factor is the cost multiplier of layer-cell i.
func (pc *pairCons) factor(gr *grid, i int) float32 {
	col := int32(i % (gr.W * gr.H))
	l := i / (gr.W * gr.H)
	f := float32(1)
	if v, ok := pc.field[int32(i)]; ok {
		f = v
	} else if !pc.breakout[col] {
		f = pc.off
	}
	if l < len(pc.layerOK) && !pc.layerOK[l] {
		f *= pc.layer
	}
	return f
}

// pairHScale bounds the heuristic scale of a follower search: the field
// factor makes the plain-distance heuristic an overestimate, and scaled all
// the way down to it the search degrades toward Dijkstra (usb3-2layer:
// 50 s for 83.9 %); at 0.7 it still finds the coupled detours (18 s,
// 87.5 %, vs 17.9 s / 80.4 % leg by leg) — a bounded-suboptimal A*.
const pairHScale = 0.7

// hScale is the A* heuristic scale of a follower search.
func (pc *pairCons) hScale() float32 {
	s := float32(pairHScale)
	for _, v := range pc.field {
		if v > s {
			s = v // a decayed field above the bound: exact again
		}
		break // one value for the whole field
	}
	return s
}

// viaFactor is the multiplier of a via at column (x, y).
func (pc *pairCons) viaFactor(gr *grid, x, y int) float32 {
	if pc.viaOK[int32(y*gr.W+x)] {
		return 1
	}
	return pc.via
}

// pairBreakoutMil is the radius round the follower's pads where it may run
// uncoupled without penalty: half the class breakout budget (the SI check
// keeps the rest as slack), at least two pitches.
func pairBreakoutMil(n *rnet, pitch float64) float64 {
	b := float64(breakoutSlow)
	if hc := ClassifyHS(n.plan); hc != nil && hc.BreakoutMil > 0 {
		b = hc.BreakoutMil
	}
	return math.Max(b/2, 2*pitch)
}

// buildPairCons builds net n's constraints from its routed partner (nil
// when n has no partner or the partner has no routed copper).
func (r *router) buildPairCons(n *rnet) *pairCons {
	p := r.byName[n.plan.PairWith]
	if p == nil || len(p.paths) == 0 || !n.route {
		return nil
	}
	gr := r.gr
	gap := n.plan.PairGapMil
	if gap <= 0 {
		gap = r.b.Rules.Clearance
	}
	pitch := (n.width+p.width)/2 + gap
	ring := pairRing(gr, n.radius, p.radius, pitch)
	d := pairDecay(r.pairRound)
	fac := r.pairFac
	if fac <= 0 {
		fac = min(pairFieldFac+(0.55-pairFieldFac)*(1-d), 0.55)
	}
	pc := &pairCons{field: map[int32]float32{}, layerOK: make([]bool, len(gr.layers)), viaOK: map[int32]bool{}, breakout: map[int32]bool{},
		off: decayed(pairOffFac, d), layer: decayed(pairLayerFac, d), via: decayed(pairViaFac, d)}
	// Via pairs: a follower via one via pitch from a leader via (pads a
	// clearance apart, never closer than the track pitch).
	vp := math.Max(pitch, math.Max(n.viaDia, p.viaDia)+r.b.Rules.Clearance)
	vlo, vhi := vp/gr.g-0.5, vp/gr.g+2.5
	vk := int(math.Ceil(vhi))
	for _, path := range p.paths {
		prevL := -1
		for _, node := range path.nodes {
			l, x, y := gr.xy(int(node))
			pc.layerOK[l] = true
			for _, o := range ring {
				if xx, yy := x+o[0], y+o[1]; gr.in(xx, yy) {
					pc.field[int32(gr.idx(l, xx, yy))] = fac
				}
			}
			if prevL >= 0 && l != prevL {
				for dy := -vk; dy <= vk; dy++ {
					for dx := -vk; dx <= vk; dx++ {
						d := math.Hypot(float64(dx), float64(dy))
						if xx, yy := x+dx, y+dy; d >= vlo && d <= vhi && gr.in(xx, yy) {
							pc.viaOK[int32(yy*gr.W+xx)] = true
						}
					}
				}
			}
			prevL = l
		}
	}
	// The follower's pads: breakout zone, and their layers are its own.
	rad := pairBreakoutMil(n, pitch)
	rc := int(math.Ceil(rad/gr.g)) + 1
	for _, g := range n.groups {
		for _, pd := range g {
			if li := gr.layerIndex(pd.Layer); li >= 0 {
				pc.layerOK[li] = true
			}
			cx, cy := gr.cellOf(pd.Box.C)
			for y := cy - rc; y <= cy+rc; y++ {
				for x := cx - rc; x <= cx+rc; x++ {
					if gr.in(x, y) && pd.Box.Dist(gr.center(x, y)) <= rad {
						pc.breakout[int32(y*gr.W+x)] = true
					}
				}
			}
		}
	}
	return pc
}

// pathVias counts n's layer changes on the grid.
func (r *router) pathVias(n *rnet) int {
	c := 0
	for _, p := range n.paths {
		prev := -1
		for _, node := range p.nodes {
			l, _, _ := r.gr.xy(int(node))
			if prev >= 0 && l != prev {
				c++
			}
			prev = l
		}
	}
	return c
}

// followShare is the share of the follower's routed cells outside its
// breakout zone that lie on the coupled path of pc (1 when all are breakout).
func (r *router) followShare(f *rnet, pc *pairCons) float64 {
	if pc == nil {
		return 0
	}
	gr := r.gr
	in, tot := 0, 0
	for _, p := range f.paths {
		for _, node := range p.nodes {
			col := int32(int(node) % (gr.W * gr.H))
			if pc.breakout[col] {
				continue
			}
			tot++
			if _, ok := pc.field[node]; ok {
				in++
			}
		}
	}
	if tot == 0 {
		return 1
	}
	return float64(in) / float64(tot)
}

// pairUnitQuality is a routed pair unit's standing.
type pairUnitQuality struct {
	failed  int     // connections lost
	viaDiff int     // |vias(lead) − vias(follow)|
	share   float64 // follower's coupled share outside the breakout zones
	lenMil  float64 // both legs' routed grid length + |skew|
	coupled bool    // share at the class minimum
	min     float64 // the class minimum share
}

// better ranks units: connected first; then a unit that runs coupled beats
// one that does not; between coupled units, symmetric vias, then the
// higher share, then the shorter; between uncoupled units the shorter pair
// wins, symmetric vias only breaking ties. (routePairUnit keeps the free
// unit whenever no unit couples: a pair the geometry keeps apart gains
// nothing from constraints that cannot couple it — only detours: ESP32 USB
// with a resistor in the corridor, D+ went round J2, 972 mil of skew and a
// 578 mil ESD stub, for 0/0 vias.)
func (q pairUnitQuality) better(o pairUnitQuality) bool {
	if q.failed != o.failed {
		return q.failed < o.failed
	}
	if q.coupled != o.coupled {
		return q.coupled
	}
	if q.coupled {
		if q.viaDiff != o.viaDiff {
			return q.viaDiff < o.viaDiff
		}
		if math.Abs(q.share-o.share) > 0.02 {
			return q.share > o.share
		}
		return q.lenMil < o.lenMil-1e-6
	}
	if math.Abs(q.lenMil-o.lenMil) > 0.02*math.Max(q.lenMil, o.lenMil) {
		return q.lenMil < o.lenMil
	}
	if q.viaDiff != o.viaDiff {
		return q.viaDiff < o.viaDiff
	}
	return q.lenMil < o.lenMil-1e-6
}

// pathLen is n's routed grid length (mil).
func (r *router) pathLen(n *rnet) float64 {
	gr := r.gr
	s := 0.0
	for _, p := range n.paths {
		for k := 1; k < len(p.nodes); k++ {
			_, x0, y0 := gr.xy(int(p.nodes[k-1]))
			_, x1, y1 := gr.xy(int(p.nodes[k]))
			s += gr.center(x0, y0).Dist(gr.center(x1, y1))
		}
	}
	return s
}

func (r *router) pairQuality(lead, follow *rnet) pairUnitQuality {
	d := r.pathVias(lead) - r.pathVias(follow)
	if d < 0 {
		d = -d
	}
	la, lb := r.pathLen(lead), r.pathLen(follow)
	q := pairUnitQuality{failed: len(lead.failed) + len(follow.failed), viaDiff: d, share: r.followShare(follow, r.buildPairCons(follow)),
		lenMil: la + lb + math.Abs(la-lb)}
	min := float64(coupledPctSlow) / 100
	if hc := ClassifyHS(lead.plan); hc != nil && hc.MinCoupledPct > 0 {
		min = hc.MinCoupledPct / 100
	}
	q.coupled, q.min = q.share >= min, min
	return q
}

// good reports a unit that needs no second try: complete, coupled,
// symmetric vias.
func (q pairUnitQuality) good() bool {
	return q.failed == 0 && q.viaDiff == 0 && q.coupled
}

// netState is a net's routed state (for restoring a rejected attempt).
type netState struct {
	paths  []rpath
	claims []int32
	failed []Unrouted
}

func (r *router) saveNet(n *rnet) netState {
	return netState{append([]rpath(nil), n.paths...), append([]int32(nil), n.claims...), append([]Unrouted(nil), n.failed...)}
}

// clearNet removes n's routed copper (fan-outs stay).
func (r *router) clearNet(n *rnet) {
	r.applyClaims(n.claims, -1)
	n.claims, n.paths, n.failed = nil, nil, nil
}

// restoreNet replaces n's routed copper by s.
func (r *router) restoreNet(n *rnet, s netState) {
	r.applyClaims(n.claims, -1)
	n.paths, n.claims, n.failed = s.paths, s.claims, s.failed
	r.applyClaims(n.claims, +1)
}

// pairPartner returns n's routable partner when the pair is routed as a
// unit, or nil. Unit routing is for pairs an intent declares (an interface:
// USB, PCIe, HDMI, …): a pair guessed from net names alone keeps the soft
// pair field, as the recouple pass does — the intent carries the gap, the
// class limits and the interface the constraints are sized from.
func (r *router) pairPartner(n *rnet) *rnet {
	if noPairUnit || r.opt.NoPairUnit || n.plan == nil || n.plan.Interface == "" {
		return nil
	}
	p := r.byName[n.plan.PairWith]
	if p == nil || p == n || !p.route || len(p.groups) < 2 || !n.route || len(n.groups) < 2 || p.plan.Interface == "" {
		return nil
	}
	return p
}

// routePairUnit routes a and b as one differential pair: a leads, b
// follows. With explore (the first routing pass), a unit that is not good is
// tried again with the other leg leading and as two free legs (the pre-unit
// soft pair field); the best is kept (pairUnitQuality.better) and fixes the
// pair's leader and mode for its negotiation re-routes, which repeat that
// one unit: re-deciding every round kept the pair flipping between layouts
// and negotiation never converged (usb3-typec: 7 conflicts for 40 rounds).
func (r *router) routePairUnit(a, b *rnet, explore bool) {
	if b.pairLeads {
		a, b = b, a
	}
	type attempt struct {
		q      pairUnitQuality
		sa, sb netState
		lead   *rnet
		free   bool
	}
	run := func(lead, follow *rnet, free bool) attempt {
		r.clearNet(a)
		r.clearNet(b)
		r.pairFree = free
		r.routeNet(lead)
		r.routeNet(follow)
		r.pairFree = false
		return attempt{q: r.pairQuality(lead, follow), sa: r.saveNet(a), sb: r.saveNet(b), lead: lead, free: free}
	}
	first := run(a, b, a.pairFree)
	if first.q.good() || !explore || r.now().After(r.deadline) || noPairSwap {
		a.pairLeads, b.pairLeads = true, false
		return
	}
	best := first
	var free *attempt
	if first.free {
		free = &first
	}
	for _, alt := range []struct {
		lead, follow *rnet
		free         bool
	}{{b, a, false}, {a, b, false}, {a, b, true}} {
		if alt.lead == first.lead && alt.free == first.free || r.now().After(r.deadline) {
			continue
		}
		t := run(alt.lead, alt.follow, alt.free)
		if t.free {
			free = &t
		}
		if t.q.better(best.q) {
			best = t
		}
	}
	if best.q.share < best.q.min/2 && free != nil {
		// No constrained unit couples even half the pair: the geometry keeps
		// its legs apart, and the constraints would only bend them (longer,
		// stubs off protection pads). It is routed free, as before pair
		// units, and stays free for its next re-routes.
		best = *free
	}
	a.pairLeads, b.pairLeads = best.lead == a, best.lead == b
	a.pairFree, b.pairFree = best.free, best.free
	r.clearNet(a)
	r.clearNet(b)
	r.restoreNet(a, best.sa)
	r.restoreNet(b, best.sb)
}

// noPairSwap disables the leader swap (A/B diagnostics).
var noPairSwap bool

// noPairUnit restores the pre-2026-09-30 behaviour: each leg routed alone
// with the 0.55 soft field (A/B diagnostics).
var noPairUnit bool

// pairRing is the set of cell offsets at which a follower of claim radius
// rn runs beside a leader node of claim radius rp: the nearest distances
// whose claim disks do not share a cell (at the target pitch or just past
// it). A ring reaching half a cell inside the pitch (the old pair field)
// put the two legs' claims on common cells: every negotiation round saw the
// pair in conflict with itself, history piled up on the coupled path and
// the re-routed follower avoided it (ESP32 USB: 85 % → 0 % coupled).
func pairRing(gr *grid, rn, rp, pitch float64) [][2]int {
	dn, dp := gr.disk(rn), gr.disk(rp)
	in := make(map[[2]int]bool, len(dp))
	for _, o := range dp {
		in[o] = true
	}
	clash := func(dx, dy int) bool {
		for _, o := range dn {
			if in[[2]int{o[0] + dx, o[1] + dy}] {
				return true
			}
		}
		return false
	}
	lo := pitch/gr.g - 1
	k := int(math.Ceil(pitch/gr.g + 2))
	minD := math.Inf(1)
	type cand struct {
		o [2]int
		d float64
	}
	var cs []cand
	for dy := -k; dy <= k; dy++ {
		for dx := -k; dx <= k; dx++ {
			d := math.Hypot(float64(dx), float64(dy))
			if d < lo || d > pitch/gr.g+2 || clash(dx, dy) {
				continue
			}
			cs = append(cs, cand{[2]int{dx, dy}, d})
			minD = math.Min(minD, d)
		}
	}
	// The band: from the nearest clash-free distance, 1.2 cells wide.
	hi := math.Max(minD, pitch/gr.g) + 1.2
	var ring [][2]int
	for _, c := range cs {
		if c.d <= hi {
			ring = append(ring, c.o)
		}
	}
	return ring
}

// pairLeadRoomFac prices a leader cell that leaves no room for the partner
// on either side: the leader stays off pins and pads it would hug, so the
// follower fits beside it (ESP32 USB: D- ran 2 mil above the CH340 pin row
// and D+ could not run coupled below it).
const pairLeadRoomFac = 1.6

// pairLead is the leader's room test: its copper widened by one pair
// pitch must be legal, except in the breakout zones round the pair's pads.
type pairLead struct {
	rad      float64
	breakout map[int32]bool
}

func (pl *pairLead) factor(r *router, n *rnet, i int) float32 {
	gr := r.gr
	col := int32(i % (gr.W * gr.H))
	if pl.breakout[col] {
		return 1
	}
	l, x, y := gr.xy(i)
	if r.nodeOK(n, l, x, y, pl.rad) {
		return 1
	}
	return decayed(pairLeadRoomFac, pairDecay(r.pairRound))
}

// buildPairLead sets up the leader's room test when n's partner is not
// routed yet (nil otherwise, or when n has no routable partner).
func (r *router) buildPairLead(n *rnet) *pairLead {
	p := r.pairPartner(n)
	if p == nil || len(p.paths) > 0 {
		return nil
	}
	gap := n.plan.PairGapMil
	if gap <= 0 {
		gap = r.b.Rules.Clearance
	}
	pitch := (n.width+p.width)/2 + gap
	gr := r.gr
	pl := &pairLead{rad: n.radius + pitch, breakout: map[int32]bool{}}
	rad := pairBreakoutMil(n, pitch)
	rc := int(math.Ceil(rad/gr.g)) + 1
	for _, m := range []*rnet{n, p} {
		for _, g := range m.groups {
			for _, pd := range g {
				cx, cy := gr.cellOf(pd.Box.C)
				for y := cy - rc; y <= cy+rc; y++ {
					for x := cx - rc; x <= cx+rc; x++ {
						if gr.in(x, y) && pd.Box.Dist(gr.center(x, y)) <= rad {
							pl.breakout[int32(y*gr.W+x)] = true
						}
					}
				}
			}
		}
	}
	return pl
}

// snapPairGaps moves the follower leg's straight runs that parallel its
// leader within three cells of the pair pitch onto the exact pitch
// (the target gap from the impedance / intent): the grid can only place
// the follower on the nearest cell whose claim does not overlap the
// leader's, up to two cells off the target (usb3-typec, 2.4 mil grid:
// 16.8 mil for a 13 mil pitch — a 7.8 mil gap for a 4 mil target). Only runs between two free vertices
// move (never a pad or via end); a pair whose snapped copper makes a new
// exact-DRC violation is restored.
func (r *router) snapPairGaps(outs map[*rnet]*netOut, res *RouteResult, collect func() ([]Track, []Via)) {
	if noPairUnit || r.opt.NoPairUnit {
		return
	}
	type leg struct {
		n, lead *rnet
		orig    []Track
		gap     float64
		veto    map[int]bool
		src     []int
		moved   int
	}
	var legs []*leg
	for _, n := range r.nets {
		lead := r.pairPartner(n)
		if lead == nil || n.pairFree || n.pairLeads || !lead.pairLeads || outs[n] == nil || outs[lead] == nil {
			continue
		}
		gap := n.plan.PairGapMil
		if gap <= 0 {
			gap = r.b.Rules.Clearance
		}
		legs = append(legs, &leg{n: n, lead: lead, orig: append([]Track(nil), outs[n].tracks...), gap: gap, veto: map[int]bool{}})
	}
	if len(legs) == 0 {
		return
	}
	// The claim disks keep the follower up to two cells beyond the pitch
	// (a disk covers every cell it touches): reach three. A segment whose
	// snapped copper breaks the exact DRC (the partner's pads at a
	// flow-through ESD, another net) is vetoed and the leg snapped again
	// without it; a leg still violating after the rounds is restored.
	apply := func() {
		for _, l := range legs {
			ts, k, src := snapLeg(l.orig, outs[l.lead].tracks, outs[l.n].vias, r.anchorsOf(l.n), l.gap, 3*r.gr.g, l.veto)
			outs[l.n].tracks, l.moved, l.src = ts, k, src
		}
	}
	apply()
	for round := 0; round < 6; round++ {
		ts, vs := collect()
		viol := CheckDRC(r.b, r.an, r.st, ts, vs).Violations
		changed := false
		for _, l := range legs {
			if l.moved == 0 {
				continue
			}
			for _, v := range viol {
				if v.NetA != l.n.name && v.NetB != l.n.name {
					continue
				}
				for k, t := range outs[l.n].tracks {
					if k < len(l.src) && l.src[k] >= 0 && !l.veto[l.src[k]] && PointSegDist(v.At, t.A, t.B) <= t.Width/2+v.Required+1 {
						l.veto[l.src[k]] = true
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
		apply()
	}
	ts, vs := collect()
	bad := map[string]bool{}
	for _, v := range CheckDRC(r.b, r.an, r.st, ts, vs).Violations {
		bad[v.NetA], bad[v.NetB] = true, true
	}
	runs, pairs := 0, 0
	for _, l := range legs {
		if l.moved == 0 {
			continue
		}
		if bad[l.n.name] {
			outs[l.n].tracks = l.orig // restore: never ship a snap that violates
			continue
		}
		runs += l.moved
		pairs++
	}
	if pairs > 0 {
		res.Notes = append(res.Notes, sprintf("pair gap: %d follower run(s) of %d pair(s) snapped onto the exact pair pitch", runs, pairs))
	}
}

// anchorsOf lists n's pad anchors (track ends that must not move).
func (r *router) anchorsOf(n *rnet) []Point {
	var out []Point
	for _, g := range n.groups {
		for _, pd := range g {
			out = append(out, r.anchor(pd), pd.Box.C)
		}
	}
	for _, p := range n.paths {
		if p.fromPad {
			out = append(out, p.from)
		}
		if p.toPad {
			out = append(out, p.to)
		}
	}
	return out
}

// snapLeg shifts the straight segments of leg that run parallel to a
// segment of lead at a centre distance within reach of the pitch
// (width/2 + gap + width/2) onto that pitch. Each moved vertex is rebuilt
// from its two segments: the intersection of their new lines when both
// move; a slide along the unmoved neighbour when that neighbour crosses the
// moved line at 60° or more (it keeps its direction: 45° stays 45°); else a
// short 45° jog from the old vertex into the moved segment. A vertex at a
// via, a pad anchor or a tee (not exactly two segments) never moves, nor do
// the segments ending there. A leg whose rebuilt geometry would reverse or
// collapse a segment is left as it was. It returns the new leg and the
// number of segments moved.
func snapLeg(leg, lead []Track, vias []Via, anchors []Point, gap, reach float64, veto map[int]bool) ([]Track, int, []int) {
	type end struct{ seg, side int } // side 0 = A, 1 = B
	key := func(p Point) [2]int64 { return [2]int64{int64(math.Round(p.X * 1000)), int64(math.Round(p.Y * 1000))} }
	at := map[[2]int64][]end{}
	for i, t := range leg {
		at[key(t.A)] = append(at[key(t.A)], end{i, 0})
		at[key(t.B)] = append(at[key(t.B)], end{i, 1})
	}
	fixed := func(p Point) bool {
		if len(at[key(p)]) != 2 {
			return true
		}
		for _, v := range vias {
			if v.C.Dist(p) < 1e-6 {
				return true
			}
		}
		for _, a := range anchors {
			if a.Dist(p) < 1e-6 {
				return true
			}
		}
		return false
	}
	shift := make([]float64, len(leg))
	normal := make([]Point, len(leg))
	dir := make([]Point, len(leg))
	moved := 0
	for i, t := range leg {
		L := t.A.Dist(t.B)
		if L < 1e-6 {
			continue
		}
		u := t.B.Sub(t.A).Scale(1 / L)
		dir[i], normal[i] = u, Point{-u.Y, u.X}
		if L < 4 || veto[i] || fixed(t.A) || fixed(t.B) {
			continue
		}
		best, bestAbs := 0.0, math.Inf(1)
		for _, o := range lead {
			if o.Layer != t.Layer {
				continue
			}
			ol := o.A.Dist(o.B)
			if ol < 1e-6 {
				continue
			}
			ou := o.B.Sub(o.A).Scale(1 / ol)
			if math.Abs(u.X*ou.Y-u.Y*ou.X) > 0.02 {
				continue // not parallel
			}
			pa, pb := dot(o.A.Sub(t.A), u), dot(o.B.Sub(t.A), u)
			lo, hi := math.Max(0, math.Min(pa, pb)), math.Min(L, math.Max(pa, pb))
			if hi-lo < L/2 {
				continue // beside less than half of t
			}
			d := dot(o.A.Sub(t.A), normal[i]) // signed centre offset of the leader
			pitch := (t.Width+o.Width)/2 + gap
			if off := math.Abs(d) - pitch; math.Abs(off) <= reach && math.Abs(off) < bestAbs {
				bestAbs = math.Abs(off)
				best = off
				if d < 0 {
					best = -off
				}
			}
		}
		if math.IsInf(bestAbs, 1) || bestAbs < 0.05 || 2*bestAbs >= L {
			continue
		}
		shift[i] = best // along normal[i]: toward the leader when too far
		moved++
	}
	if moved == 0 {
		return leg, 0, nil
	}
	out := append([]Track(nil), leg...)
	src := make([]int, len(leg)) // the moved segment behind each changed track
	for i := range src {
		src[i] = -1
		if shift[i] != 0 {
			src[i] = i
		}
	}
	var jogs []Track
	var jogSrc []int
	set := func(e end, p Point) {
		if e.side == 0 {
			out[e.seg].A = p
		} else {
			out[e.seg].B = p
		}
	}
	for _, ends := range at {
		if len(ends) != 2 {
			continue
		}
		i, j := ends[0].seg, ends[1].seg
		if shift[i] == 0 && shift[j] == 0 {
			continue
		}
		var V Point
		if ends[0].side == 0 {
			V = leg[i].A
		} else {
			V = leg[i].B
		}
		if shift[i] != 0 && shift[j] != 0 {
			// Intersection of the two shifted lines.
			pi := V.Add(normal[i].Scale(shift[i]))
			pj := V.Add(normal[j].Scale(shift[j]))
			den := dir[i].X*dir[j].Y - dir[i].Y*dir[j].X
			nv := pi // parallel neighbours: the same line
			if math.Abs(den) > 1e-9 {
				w := pj.Sub(pi)
				tt := (w.X*dir[j].Y - w.Y*dir[j].X) / den
				nv = pi.Add(dir[i].Scale(tt))
			}
			set(ends[0], nv)
			set(ends[1], nv)
			continue
		}
		em, ek := ends[0], ends[1]
		if shift[em.seg] == 0 {
			em, ek = ek, em
		}
		m, k := em.seg, ek.seg
		if c := dot(dir[k], normal[m]); math.Abs(c) >= 0.5 {
			nv := V.Add(dir[k].Scale(shift[m] / c))
			set(em, nv)
			set(ek, nv)
			src[k] = m
			continue
		}
		// The neighbour runs almost along the moved segment: keep it, and
		// enter the moved segment through a 45° jog from V.
		into := dir[m] // from V into segment m
		if em.side == 1 {
			into = into.Scale(-1)
		}
		w := V.Add(normal[m].Scale(shift[m])).Add(into.Scale(math.Abs(shift[m])))
		set(em, w)
		jogs = append(jogs, Track{Net: leg[m].Net, Layer: leg[m].Layer, A: V, B: w, Width: leg[m].Width, Kind: leg[m].Kind})
		jogSrc = append(jogSrc, m)
	}
	for i := range out {
		L0, L1 := leg[i].A.Dist(leg[i].B), out[i].A.Dist(out[i].B)
		if L0 < 1e-6 {
			continue
		}
		if L1 < 0.5 || dot(out[i].B.Sub(out[i].A), leg[i].B.Sub(leg[i].A)) <= 0 {
			return leg, 0, nil // a segment collapsed or reversed
		}
	}
	return append(out, jogs...), moved, append(src, jogSrc...)
}
