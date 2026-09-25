package pcbauto

import (
	"context"
	"math"
	"sort"
	"time"
)

// PlaneRegion is copper to pour for a net on a layer (plane or split plane).
type PlaneRegion struct {
	Net     string    `json:"net"`
	Layer   int       `json:"layer"`
	Polys   [][]Point `json:"polys"`
	AreaIn2 float64   `json:"areaIn2"`
	// Priority orders overlapping pours: 1 is poured first (small islands),
	// the background rail last.
	Priority int  `json:"priority"`
	Plane    bool `json:"plane"` // layer should be switched to PLANE type
}

// ---- fan-out --------------------------------------------------------------

// fanout drops a via next to every SMD pad of a plane or pour net so the pad
// reaches its plane/pour on the other layers — the "planes first" step. It
// runs strictly (no overlap) before any signal routing.
func (r *router) fanout(res *RouteResult) {
	gr := r.gr
	r.strict = true
	defer func() { r.strict = false }()
	r.bgaFanout(res)
	var pads []*Pad
	for _, n := range r.nets {
		if !n.onPlane && !n.poured {
			continue
		}
		for _, g := range n.groups {
			for _, pd := range g {
				if pd.Layer != LayerMulti && !r.bgaDone[pd] {
					pads = append(pads, pd)
				}
			}
		}
	}
	// Fine-pitch pads first: they have the fewest escape options.
	sort.SliceStable(pads, func(i, j int) bool {
		ai, aj := math.Min(pads[i].Box.W, pads[i].Box.H), math.Min(pads[j].Box.W, pads[j].Box.H)
		if ai != aj {
			return ai < aj
		}
		return pads[i].Key() < pads[j].Key()
	})
	for _, pd := range pads {
		n := r.byName[pd.Net]
		li := gr.layerIndex(pd.Layer)
		if li < 0 || !gr.routable[li] {
			continue
		}
		// A 2-layer pour net only needs the via if the pad would otherwise rely
		// on a single layer; every GND pad gets one (return-path discipline).
		need := 1
		padArea := pd.Box.W * pd.Box.H
		viaArea := math.Pi * r.b.Rules.ViaDia * r.b.Rules.ViaDia / 4
		if padArea > 6*viaArea {
			need = clampInt(int(math.Ceil(float64(n.plan.ViasPerTransition)/2)), 1, 3)
			if padArea > 30*viaArea {
				need = clampInt(n.plan.ViasPerTransition, 2, 9)
			}
		}
		stubW := r.fanStubW(n, pd)
		placed := r.placeFanoutVias(n, pd, li, need, stubW, res)
		if placed == 0 && r.shareFanout(n, pd, li, stubW) {
			placed = 1
		}
		if placed == 0 {
			res.Notes = append(res.Notes, sprintf("fan-out: no via site for %s (%s); it will be routed as a track", pd.Key(), n.name))
		}
	}
}

// fanStubW is the width of a fan-out stub from pd.
func (r *router) fanStubW(n *rnet, pd *Pad) float64 {
	return math.Min(math.Max(n.width, r.b.Rules.TrackWidth), math.Max(math.Min(pd.Box.W, pd.Box.H), r.b.Rules.TrackWidth))
}

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// relocateFanout moves fan-out k of n that fails the final exact DRC to
// another site around its pad: the fan-out is withdrawn and the pad's
// candidate sites are tried again, each vetted by accept against all
// emitted copper. The ESP32 D1.1 +5V via sat in an M3 keep ring; the final
// gate then dropped the net's bridging and the via, leaving two plane
// connections open. Pinned (BGA) fan-outs are not moved. It reports whether
// the fan-out was withdrawn (moved, or reported fanout-drc when no site
// passes).
func (r *router) relocateFanout(n *rnet, k int, res *RouteResult, accept func() bool) bool {
	if k < len(n.fanPinned) && n.fanPinned[k] {
		return false
	}
	var pd *Pad
	via := n.fanVias[k]
	// Stubs of other pads or escapes hanging on the via would go with it.
	for _, t := range n.shareTracks {
		if t.B.Dist(via.C) < 0.5 {
			return false
		}
	}
	for _, e := range n.escs {
		if e.hasVia && e.via.Dist(via.C) < 0.5 {
			return false
		}
	}
	for _, g := range n.groups {
		for _, q := range g {
			if ti := n.fanTrack[k]; ti >= 0 && q.Box.C.Dist(n.fanTracks[ti].A) < 0.5 || ti < 0 && q.Box.Dist(via.C) == 0 {
				pd = q
			}
		}
	}
	if pd == nil {
		return false
	}
	li := r.gr.layerIndex(pd.Layer)
	if li < 0 || !r.gr.routable[li] {
		return false
	}
	var keep []int
	for i := range n.fanVias {
		if i != k {
			keep = append(keep, i)
		}
	}
	r.keepFanouts(n, keep)
	res.Stats.FanoutVias--
	strict := r.strict
	r.strict = true
	defer func() { r.strict = strict }()
	if r.placeFanoutViasChecked(n, pd, li, 1, r.fanStubW(n, pd), res, accept) > 0 {
		res.Notes = append(res.Notes, sprintf("final gate: moved the %s fan-out via of %s to another site (exact DRC)", n.name, pd.Key()))
	} else {
		n.failed = append(n.failed, Unrouted{Net: n.name, Pads: []string{pd.Key()}, Reason: "fanout-drc"})
	}
	return true
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (r *router) placeFanoutVias(n *rnet, pd *Pad, li, need int, stubW float64, res *RouteResult) int {
	return r.placeFanoutViasChecked(n, pd, li, need, stubW, res, nil)
}

// placeFanoutViasChecked is placeFanoutVias with an exact vetting step:
// accept (nil = none) runs after each commit and a rejected site is
// withdrawn; at most 12 sites are vetted.
func (r *router) placeFanoutViasChecked(n *rnet, pd *Pad, li, need int, stubW float64, res *RouteResult, accept func() bool) int {
	gr := r.gr
	vetted := 0
	part := r.b.Part(pd.Part)
	away := Point{}
	if part != nil {
		away = pd.Box.C.Sub(part.Body().Center())
	}
	type cand struct {
		x, y int
		c    Point
		s    float64
	}
	maxR := math.Max(80, 3.5*r.b.Rules.ViaDia)
	cx, cy := gr.cellOf(pd.Box.C)
	rc := int(math.Ceil(maxR / gr.g))
	var cs []cand
	// Large pads (EPAD) may host vias inside: those are thermal vias and
	// allowed; for normal pads the via sits just outside the copper.
	// Only an IC's exposed/thermal pad hosts vias inside (filled + capped by
	// the fab as a thermal array). Connector shells, switch legs and passive
	// pads got in-pad vias from the size test alone: 21 via-in-pad warnings
	// on the ESP32 board, each a solder-wicking joint.
	// On 6+ layer boards JLC fills and caps via-in-pad (POFV) as standard,
	// so large passive pads may host vias there too: forbidding them cost
	// the 6-layer K230 fixture 300 fan-out vias and 5 points of completion.
	inPadOK := need > 1 && part != nil && (ClassifyPart(part) == KindIC || ClassifyPart(part) == KindModule || r.st != nil && r.st.Layers >= 6)
	for dy := -rc; dy <= rc; dy += 1 {
		for dx := -rc; dx <= rc; dx += 1 {
			x, y := cx+dx, cy+dy
			if !gr.in(x, y) {
				continue
			}
			c := gr.center(x, y)
			d := c.Dist(pd.Box.C)
			if d > maxR {
				continue
			}
			edge := pd.Box.Dist(c)
			if edge == 0 && !inPadOK {
				continue
			}
			score := edge + 0.15*d
			if away.X != 0 || away.Y != 0 {
				v := c.Sub(pd.Box.C)
				if v.X*away.X+v.Y*away.Y < 0 {
					score += 25 // prefer escaping away from the part body
				}
			}
			cs = append(cs, cand{x, y, c, score})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].s != cs[j].s {
			return cs[i].s < cs[j].s
		}
		return cs[i].y*gr.W+cs[i].x < cs[j].y*gr.W+cs[j].x
	})
	placed := 0
	for _, c := range cs {
		if placed >= need {
			break
		}
		inside := pd.Box.Dist(c.c) == 0
		if r.holeClash(c.c, r.b.Rules.ViaDrill) {
			continue
		}
		if inside {
			// Thermal via inside an EPAD: only pad/hard checks, the via copper
			// merges with the pad.
			if !r.nodeOK(n, li, c.x, c.y, n.viaR) {
				continue
			}
			if occ, _ := r.nodeCong(li, c.x, c.y, n.viaR); occ > 0 {
				continue
			}
			ok := true
			for l := range gr.layers {
				if !r.nodeOK(n, l, c.x, c.y, n.viaR) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		} else {
			if math.IsInf(r.viaCostUncached(n, c.x, c.y), 1) {
				continue
			}
			if !r.segmentOK(n, li, pd.Box.C, c.c, stubW, true) {
				continue
			}
		}
		if accept != nil && vetted >= 12 {
			break
		}
		r.commitFanout(n, pd, li, c.x, c.y, c.c, stubW, inside, false)
		if accept != nil {
			vetted++
			if !accept() {
				r.keepFanouts(n, seq(len(n.fanVias)-1))
				continue
			}
		}
		res.Stats.FanoutVias++
		placed++
	}
	return placed
}

// commitFanout claims a fan-out via at grid cell (x, y) — centre c — plus the
// stub from pd on layer li (none for a via inside the pad), records it as
// fixed copper of n, and emits the track and via.
func (r *router) commitFanout(n *rnet, pd *Pad, li, x, y int, c Point, stubW float64, inside, pinned bool, via ...float64) {
	drill, dia, rad := r.b.Rules.ViaDrill, r.b.Rules.ViaDia, n.viaR
	if len(via) == 2 {
		// A via class other than the board default (BGA fan-out).
		drill, dia = via[0], via[1]
		rad = dia/2 + n.share
	}
	gr := r.gr
	var cl []int32
	trackIdx := -1
	if !inside {
		cl = r.claimSegment(n, li, pd.Box.C, c, stubW, cl)
		trackIdx = len(n.fanTracks)
		n.fanTracks = append(n.fanTracks, Track{Net: n.name, Layer: gr.layers[li], A: pd.Box.C, B: c, Width: stubW, Kind: "fanout"})
	}
	r.claimCur++
	for l := range gr.layers {
		for _, o := range gr.disk(rad) {
			xx, yy := x+o[0], y+o[1]
			if gr.in(xx, yy) {
				cl = append(cl, int32(gr.idx(l, xx, yy)))
			}
		}
	}
	// Only cells this net does not already claim: two fan-out vias of the
	// same net overlap, and a cell must count once per net.
	cl = dedup(cl)
	n.fanFull = append(n.fanFull, append([]int32(nil), cl...))
	n.fanTrack = append(n.fanTrack, trackIdx)
	n.fanPinned = append(n.fanPinned, pinned)
	r.claimCur++
	for _, i := range n.fixed {
		r.claimStamp[i] = r.claimCur
	}
	fresh := cl[:0]
	for _, i := range cl {
		if r.claimStamp[i] != r.claimCur {
			fresh = append(fresh, i)
		}
	}
	r.applyClaims(fresh, +1)
	n.fixed = dedup(append(n.fixed, fresh...))
	n.fanVias = append(n.fanVias, Via{Net: n.name, C: c, Drill: drill, Dia: dia, Kind: "fanout"})
	r.addFanHole(n.fanVias[len(n.fanVias)-1])
}

// antipadMask marks the cells inside the antipad of every via and
// through-hole pad not belonging to n: centre closer than its copper radius
// plus the clearance.
func (r *router) antipadMask(n *rnet) []bool {
	gr := r.gr
	m := make([]bool, gr.W*gr.H)
	clr := r.b.Rules.Clearance
	mark := func(c Point, rad float64) {
		cx, cy := gr.cellOf(c)
		rc := int(math.Ceil(rad/gr.g)) + 1
		for dy := -rc; dy <= rc; dy++ {
			for dx := -rc; dx <= rc; dx++ {
				x, y := cx+dx, cy+dy
				if gr.in(x, y) && gr.center(x, y).Dist(c) < rad {
					m[y*gr.W+x] = true
				}
			}
		}
	}
	for _, o := range r.nets {
		if o == n {
			continue
		}
		for _, v := range o.fanVias {
			mark(v.C, v.Dia/2+clr)
		}
		for _, p := range o.paths {
			for k := 1; k < len(p.nodes); k++ {
				pl, px, py := gr.xy(int(p.nodes[k-1]))
				l, x, y := gr.xy(int(p.nodes[k]))
				if pl != l && px == x && py == y {
					mark(gr.center(x, y), r.b.Rules.ViaDia/2+clr)
				}
			}
		}
		for _, g := range o.groups {
			for _, pd := range g {
				if pd.Layer == LayerMulti {
					mark(pd.Box.C, math.Max(pd.Box.W, pd.Box.H)/2+clr)
				}
			}
		}
	}
	// Through-hole pads of parts on no routed net still cut the plane.
	for _, p := range r.b.Parts {
		for _, pd := range p.Pads {
			if pd.Layer == LayerMulti && pd.Net == "" {
				mark(pd.Box.C, math.Max(pd.Box.W, pd.Box.H)/2+clr)
			}
		}
	}
	return m
}

// ---- split planes ---------------------------------------------------------

// splitPlanes partitions each multi-net plane layer into per-rail regions
// (Voronoi from the rail's via/THT seeds, with a split gap) and returns the
// pour regions for every plane layer. Single-net planes get the whole board.
func (r *router) splitPlanes() []PlaneRegion {
	var out []PlaneRegion
	outline := r.b.Outline
	if len(outline) < 3 {
		outline = r.b.Bounds().Corners()
	}
	for _, l := range r.st.Stack {
		if l.Kind == KindSignal && len(l.PourNets) > 0 {
			// Pours on a routing layer (2-layer GND, or a 4-layer mixed power
			// layer): flooded around the tracks after routing.
			if len(l.PourNets) == 1 {
				out = append(out, PlaneRegion{Net: l.PourNets[0], Layer: l.ID, Polys: [][]Point{outline},
					AreaIn2: PolyArea(outline) / 1e6, Priority: 1})
				continue
			}
			out = append(out, r.splitLayer(StackLayer{ID: l.ID, Name: l.Name, Kind: KindSignal, Nets: l.PourNets}, outline)...)
			continue
		}
		if l.Kind != KindPlane || len(l.Nets) == 0 {
			continue
		}
		if len(l.Nets) == 1 {
			out = append(out, PlaneRegion{Net: l.Nets[0], Layer: l.ID, Polys: [][]Point{outline},
				AreaIn2: PolyArea(outline) / 1e6, Priority: 1, Plane: true})
			continue
		}
		out = append(out, r.splitLayer(l, outline)...)
	}
	return out
}

type coarse struct {
	nets   []string // net per label index
	cg     float64
	ox, oy float64
	W, H   int
	label  []int
}

func (c *coarse) center(x, y int) Point {
	return Point{c.ox + (float64(x)+0.5)*c.cg, c.oy + (float64(y)+0.5)*c.cg}
}

func (r *router) splitLayer(l StackLayer, outline []Point) []PlaneRegion {
	bb := PolyBounds(outline)
	cg := math.Max(20, r.gr.g)
	c := &coarse{cg: cg, ox: bb.MinX, oy: bb.MinY, nets: l.Nets}
	c.W = int(math.Ceil(bb.W()/cg)) + 1
	c.H = int(math.Ceil(bb.H()/cg)) + 1
	c.label = make([]int, c.W*c.H)
	inside := make([]bool, c.W*c.H)
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			p := c.center(x, y)
			inside[y*c.W+x] = PolyContains(outline, p) && PolyEdgeDist(outline, p) >= r.b.Rules.EdgeClearance
			c.label[y*c.W+x] = -1
		}
	}
	// Seeds: every via / THT pad of each rail, weighted by the rail's current
	// (heavier rails grow faster so they keep wide copper).
	type seed struct {
		x, y, k int
	}
	var seeds []seed
	for k, net := range l.Nets {
		n := r.byName[net]
		if n == nil {
			continue
		}
		pts := []Point{}
		for _, v := range n.fanVias {
			pts = append(pts, v.C)
		}
		for _, g := range n.groups {
			for _, pd := range g {
				if pd.Layer == LayerMulti {
					pts = append(pts, pd.Box.C)
				}
			}
		}
		for _, p := range pts {
			x := int((p.X - c.ox) / cg)
			y := int((p.Y - c.oy) / cg)
			if x >= 0 && y >= 0 && x < c.W && y < c.H {
				seeds = append(seeds, seed{x, y, k})
			}
		}
	}
	// Multi-source Dijkstra (8-neighbour, weighted by rail current share).
	dist := make([]float64, c.W*c.H)
	for i := range dist {
		dist[i] = math.Inf(1)
	}
	weight := make([]float64, len(l.Nets))
	for k, net := range l.Nets {
		weight[k] = 1
		if n := r.byName[net]; n != nil && n.plan.CurrentA > 0 {
			weight[k] = 1 / math.Sqrt(math.Max(n.plan.CurrentA, 0.05))
		}
	}
	q := &fpq{}
	for _, s := range seeds {
		i := s.y*c.W + s.x
		dist[i] = 0
		c.label[i] = s.k
		heapPushF(q, fItem{i, 0})
	}
	for q.Len() > 0 {
		it := heapPopF(q)
		if it.d > dist[it.i] {
			continue
		}
		x, y := it.i%c.W, it.i/c.W
		k := c.label[it.i]
		for d := 0; d < 8; d++ {
			xx, yy := x+dirs8[d][0], y+dirs8[d][1]
			if xx < 0 || yy < 0 || xx >= c.W || yy >= c.H {
				continue
			}
			j := yy*c.W + xx
			if !inside[j] {
				continue
			}
			step := 1.0
			if d%2 == 1 {
				step = math.Sqrt2
			}
			nd := it.d + step*weight[k]
			if nd < dist[j] {
				dist[j] = nd
				c.label[j] = k
				heapPushF(q, fItem{j, nd})
			}
		}
	}
	for i := range c.label {
		if !inside[i] {
			c.label[i] = -1
		}
	}
	// Split gap: clear cells bordering another rail.
	gap := make([]bool, len(c.label))
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			i := y*c.W + x
			if c.label[i] < 0 {
				continue
			}
			for d := 0; d < 8; d += 2 {
				xx, yy := x+dirs8[d][0], y+dirs8[d][1]
				if xx < 0 || yy < 0 || xx >= c.W || yy >= c.H {
					continue
				}
				j := yy*c.W + xx
				if c.label[j] >= 0 && c.label[j] != c.label[i] && c.label[i] > c.label[j] {
					gap[i] = true
				}
			}
		}
	}
	for i, g := range gap {
		if g {
			c.label[i] = -1
		}
	}
	// Stash the labelling for connectivity simulation of this layer.
	if r.split == nil {
		r.split = map[int]*coarse{}
	}
	r.split[l.ID] = c

	var regions []PlaneRegion
	for k, net := range l.Nets {
		loops := traceLoops(c, k)
		var polys [][]Point
		area := 0.0
		for _, lp := range loops {
			a := signedArea(lp)
			if a <= 0 {
				continue // holes are handled by pour priority
			}
			// Drop islands without any seed of this rail: dead copper.
			has := false
			for _, s := range seeds {
				if s.k == k && PolyContains(lp, c.center(s.x, s.y)) {
					has = true
					break
				}
			}
			if !has {
				continue
			}
			polys = append(polys, lp)
			area += a
		}
		if len(polys) == 0 {
			continue
		}
		regions = append(regions, PlaneRegion{Net: net, Layer: l.ID, Polys: polys, AreaIn2: area / 1e6})
	}
	sort.SliceStable(regions, func(i, j int) bool { return regions[i].AreaIn2 < regions[j].AreaIn2 })
	for i := range regions {
		regions[i].Priority = i + 1
	}
	return regions
}

func signedArea(poly []Point) float64 {
	s := 0.0
	for i := range poly {
		a, b := poly[i], poly[(i+1)%len(poly)]
		s += a.X*b.Y - b.X*a.Y
	}
	return s / 2
}

// traceLoops returns the boundary loops of cells labelled k, CCW for outer
// boundaries, with collinear vertices removed.
func traceLoops(c *coarse, k int) [][]Point {
	in := func(x, y int) bool {
		return x >= 0 && y >= 0 && x < c.W && y < c.H && c.label[y*c.W+x] == k
	}
	type pt struct{ x, y int }
	// Directed edges keep the region on their left (CCW outer boundary).
	next := map[pt][]pt{}
	for y := 0; y < c.H; y++ {
		for x := 0; x < c.W; x++ {
			if !in(x, y) {
				continue
			}
			if !in(x, y-1) {
				next[pt{x, y}] = append(next[pt{x, y}], pt{x + 1, y})
			}
			if !in(x+1, y) {
				next[pt{x + 1, y}] = append(next[pt{x + 1, y}], pt{x + 1, y + 1})
			}
			if !in(x, y+1) {
				next[pt{x + 1, y + 1}] = append(next[pt{x + 1, y + 1}], pt{x, y + 1})
			}
			if !in(x-1, y) {
				next[pt{x, y + 1}] = append(next[pt{x, y + 1}], pt{x, y})
			}
		}
	}
	starts := make([]pt, 0, len(next))
	for p := range next {
		starts = append(starts, p)
	}
	sort.Slice(starts, func(i, j int) bool {
		if starts[i].y != starts[j].y {
			return starts[i].y < starts[j].y
		}
		return starts[i].x < starts[j].x
	})
	var loops [][]Point
	for _, s := range starts {
		for len(next[s]) > 0 {
			var raw []pt
			p := s
			prev := pt{s.x - 1, s.y}
			for {
				outs := next[p]
				if len(outs) == 0 {
					break
				}
				// At a pinch vertex prefer the left-most turn to keep loops simple.
				pick := 0
				if len(outs) > 1 {
					din := pt{p.x - prev.x, p.y - prev.y}
					best := -10
					for i, o := range outs {
						dout := pt{o.x - p.x, o.y - p.y}
						cross := din.x*dout.y - din.y*dout.x
						if cross > best {
							best, pick = cross, i
						}
					}
				}
				o := outs[pick]
				next[p] = append(outs[:pick], outs[pick+1:]...)
				raw = append(raw, p)
				prev, p = p, o
				if p == s {
					break
				}
			}
			if len(raw) < 4 {
				continue
			}
			// Remove collinear points.
			var lp []Point
			n := len(raw)
			for i := 0; i < n; i++ {
				a, b, cc := raw[(i+n-1)%n], raw[i], raw[(i+1)%n]
				if (b.x-a.x)*(cc.y-b.y)-(b.y-a.y)*(cc.x-b.x) == 0 {
					continue
				}
				lp = append(lp, Point{c.ox + float64(b.x)*c.cg, c.oy + float64(b.y)*c.cg})
			}
			if len(lp) >= 3 {
				loops = append(loops, lp)
			}
		}
	}
	return loops
}

// float priority queue for the plane Dijkstra.
type fItem struct {
	i int
	d float64
}
type fpq []fItem

func (q fpq) Len() int { return len(q) }
func heapPushF(q *fpq, it fItem) {
	*q = append(*q, it)
	i := len(*q) - 1
	for i > 0 {
		p := (i - 1) / 2
		if (*q)[p].d <= (*q)[i].d {
			break
		}
		(*q)[p], (*q)[i] = (*q)[i], (*q)[p]
		i = p
	}
}
func heapPopF(q *fpq) fItem {
	old := *q
	top := old[0]
	n := len(old) - 1
	old[0] = old[n]
	*q = old[:n]
	i := 0
	for {
		l, rr, m := 2*i+1, 2*i+2, i
		if l < n && (*q)[l].d < (*q)[m].d {
			m = l
		}
		if rr < n && (*q)[rr].d < (*q)[m].d {
			m = rr
		}
		if m == i {
			break
		}
		(*q)[m], (*q)[i] = (*q)[i], (*q)[m]
		i = m
	}
	return top
}

// ---- plane / pour connectivity -------------------------------------------

// planeGroups disables track routing for plane and pour nets: their copper
// delivery is simulated after signal routing (pourRepair).
func (r *router) planeGroups(res *RouteResult) {
	for _, n := range r.nets {
		if n.onPlane || n.poured {
			n.route = false
		}
	}
}

// simulate computes which pads of a plane/pour net end up connected once the
// planes and pours are flooded around all other copper, including the
// anti-pads other nets' vias punch into the planes. It returns pad groups.
func (r *router) simulate(n *rnet) [][]*Pad {
	gr := r.gr
	N := len(gr.flags)
	own := make([]bool, N)
	for _, i := range n.claims {
		own[i] = true
	}
	for _, i := range n.fixed {
		own[i] = true
	}
	// Layers where this net floods copper.
	floods := make([]bool, len(gr.layers))
	for li, id := range gr.layers {
		st := r.st.Stack[li]
		if st.ID != id {
			continue
		}
		for _, pn := range append(append([]string{}, st.Nets...), st.PourNets...) {
			if pn == n.name {
				floods[li] = true
			}
		}
	}
	parent := make([]int32, N)
	for i := range parent {
		parent[i] = -1
	}
	var find func(int32) int32
	find = func(i int32) int32 {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int32) {
		ra, rb := find(a), find(b)
		if ra != rb {
			if ra < rb {
				parent[rb] = ra
			} else {
				parent[ra] = rb
			}
		}
	}
	// Plane layers: foreign vias and through-hole pads cut an antipad of
	// exactly (radius + clearance). Claimed cells overstate it (radius share
	// plus grid rounding) and closed the real 7.6 mil webs of a 0.65 mm BGA
	// via field, islanding every ground via — measure the antipads exactly.
	antipad := make([][]bool, len(gr.layers))
	for li := range gr.layers {
		if floods[li] && r.st.Stack[li].Kind == KindPlane {
			antipad[li] = r.antipadMask(n)
			// One mask serves every plane layer (through vias cut them all).
			for lj := li + 1; lj < len(gr.layers); lj++ {
				if floods[lj] && r.st.Stack[lj].Kind == KindPlane {
					antipad[lj] = antipad[li]
				}
			}
			break
		}
	}
	// A cell is copper of n if: own pad copper, own track centreline/via
	// (claims approximate this), or flooded plane/pour not claimed by others.
	isCopper := func(li, x, y int) bool {
		i := gr.idx(li, x, y)
		if gr.flags[i]&flagHard != 0 {
			return false
		}
		p := gr.pad[i]
		if p == n.id {
			return true
		}
		if p != -1 {
			return false
		}
		otherUse := int(gr.use[i])
		if own[i] {
			otherUse--
		}
		if !floods[li] {
			return false
		}
		if m := antipad[li]; m != nil {
			if m[y*gr.W+x] {
				return false
			}
		} else if otherUse > 0 {
			return false
		}
		if c := r.split[gr.layers[li]]; c != nil {
			pt := gr.center(x, y)
			cx, cy := int((pt.X-c.ox)/c.cg), int((pt.Y-c.oy)/c.cg)
			if cx < 0 || cy < 0 || cx >= c.W || cy >= c.H {
				return false
			}
			k := c.label[cy*c.W+cx]
			if k < 0 || c.nets[k] != n.name {
				return false
			}
		}
		return true
	}
	// Own routed/fan-out copper is always copper on its layer (centrelines
	// are inside claims). Mark copper cells.
	for li := range gr.layers {
		for y := 0; y < gr.H; y++ {
			for x := 0; x < gr.W; x++ {
				i := gr.idx(li, x, y)
				if isCopper(li, x, y) {
					parent[i] = int32(i)
				}
			}
		}
	}
	addOwn := func(pts []Point, layers []int) {
		for _, p := range pts {
			x, y := gr.cellOf(p)
			if !gr.in(x, y) {
				continue
			}
			var prev int32 = -1
			for _, li := range layers {
				i := int32(gr.idx(li, x, y))
				if parent[i] < 0 {
					parent[i] = i
				}
				if prev >= 0 {
					union(prev, i)
				}
				prev = i
			}
		}
	}
	allLayers := make([]int, len(gr.layers))
	for i := range allLayers {
		allLayers[i] = i
	}
	// Vias and THT pads join every layer at their column.
	var viaPts []Point
	for _, v := range n.fanVias {
		viaPts = append(viaPts, v.C)
	}
	for _, p := range n.paths {
		for k := 1; k < len(p.nodes); k++ {
			pl, px, py := gr.xy(int(p.nodes[k-1]))
			l, x, y := gr.xy(int(p.nodes[k]))
			if pl != l && px == x && py == y {
				viaPts = append(viaPts, gr.center(x, y))
			}
		}
	}
	addOwn(viaPts, allLayers)
	for _, g := range n.groups {
		for _, pd := range g {
			if pd.Layer == LayerMulti {
				addOwn([]Point{pd.Box.C}, allLayers)
			}
		}
	}
	// Fan-out stubs and routed paths as chains of own cells.
	for _, t := range append(append([]Track(nil), n.fanTracks...), n.shareTracks...) {
		li := gr.layerIndex(t.Layer)
		steps := int(math.Ceil(t.A.Dist(t.B)/(gr.g/2))) + 1
		var prev int32 = -1
		for s := 0; s <= steps; s++ {
			p := t.A.Add(t.B.Sub(t.A).Scale(float64(s) / float64(steps)))
			x, y := gr.cellOf(p)
			i := int32(gr.idx(li, x, y))
			if parent[i] < 0 {
				parent[i] = i
			}
			if prev >= 0 {
				union(prev, i)
			}
			prev = i
		}
	}
	for _, p := range n.paths {
		for k, i := range p.nodes {
			if parent[i] < 0 {
				parent[i] = i
			}
			if k > 0 {
				union(p.nodes[k-1], i)
			}
		}
	}
	// Flood unions: 4-neighbour within each layer.
	for li := range gr.layers {
		for y := 0; y < gr.H; y++ {
			for x := 0; x < gr.W; x++ {
				i := int32(gr.idx(li, x, y))
				if parent[i] < 0 {
					continue
				}
				if x+1 < gr.W {
					if j := i + 1; parent[j] >= 0 {
						union(i, j)
					}
				}
				if y+1 < gr.H {
					if j := i + int32(gr.W); parent[j] >= 0 {
						union(i, j)
					}
				}
			}
		}
	}
	groups := map[int32][]*Pad{}
	var order []int32
	for _, g := range n.groups {
		for _, pd := range g {
			root := int32(-1)
			for li, id := range gr.layers {
				if !pd.OnLayer(id) {
					continue
				}
				x, y := gr.cellOf(pd.Box.C)
				if !gr.in(x, y) {
					continue
				}
				i := int32(gr.idx(li, x, y))
				if parent[i] >= 0 {
					root = find(i)
					break
				}
			}
			if root < 0 {
				root = int32(-1 - len(order)) // isolated pad
			}
			if _, ok := groups[root]; !ok {
				order = append(order, root)
			}
			groups[root] = append(groups[root], pd)
		}
	}
	out := make([][]*Pad, 0, len(order))
	for _, k := range order {
		out = append(out, groups[k])
	}
	return out
}

// pourRepair simulates plane/pour connectivity after signal routing and adds
// strict track connections between islands until each net is one piece.
func (r *router) pourRepair(ctx context.Context, res *RouteResult) {
	// Grounds first, then the largest nets: bridging runs against a fixed
	// budget (it used all 30 s on K230), and when it runs out the nets left
	// should be small supplies, not GND.
	nets := append([]*rnet(nil), r.nets...)
	sort.SliceStable(nets, func(i, j int) bool {
		gi, gj := nets[i].plan.Role == RoleGround, nets[j].plan.Role == RoleGround
		if gi != gj {
			return gi
		}
		return len(nets[i].groups) > len(nets[j].groups)
	})
	start := time.Now()
	defer func() {
		res.Notes = append(res.Notes, sprintf("plane/pour bridging: %.1f s of the post-routing budget", time.Since(start).Seconds()))
	}()
	for pass := 0; pass < 2; pass++ {
		fixedAny := false
		for _, n := range nets {
			if !(n.onPlane || n.poured) || ctx.Err() != nil {
				continue
			}
			if len(r.opt.Nets) > 0 && !contains32(r.opt.Nets, n.name) {
				continue
			}
			groups := r.simulate(n)
			if len(groups) <= 1 {
				continue
			}
			saved := n.groups
			n.groups = groups
			r.strict = true
			r.presFac = 1e6
			n.route = true
			ok := r.routeNetKeep(n, true)
			r.strict = false
			n.groups = saved
			fixedAny = true
			if !ok && pass == 1 {
				res.Notes = append(res.Notes, sprintf("%s: %d plane/pour island(s) could not be bridged", n.name, len(n.failed)))
			}
			// Keep per-pad groups for reporting; routed bridges stay claimed.
			if pass == 1 {
				r.recordPlaneFailures(n, res)
			}
		}
		if !fixedAny {
			break
		}
	}
}

func (r *router) recordPlaneFailures(n *rnet, res *RouteResult) {
	_ = res
	// failures are collected in emit from n.failed
}

func contains32(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// shareFanout ties pd to an existing fan-out via of its own net with a stub
// when pd could not get a via of its own. Adjacent same-net pins (a buck's EN
// strapped to VIN) otherwise each dropped a via: the second one either sat
// 7 mil from the first (native Hole to Hole) or, with the hole gap enforced,
// found no site and left the pad off the plane.
func (r *router) shareFanout(n *rnet, pd *Pad, li int, stubW float64) bool {
	const maxShare = 120.0
	type cand struct {
		c Point
		d float64
	}
	var cs []cand
	for _, v := range n.fanVias {
		if d := v.C.Dist(pd.Box.C); d <= maxShare {
			cs = append(cs, cand{v.C, d})
		}
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].d < cs[j].d })
	for _, c := range cs {
		if !r.segmentOK(n, li, pd.Box.C, c.c, stubW, true) {
			continue
		}
		cl := dedup(r.claimSegment(n, li, pd.Box.C, c.c, stubW, nil))
		r.claimCur++
		for _, j := range n.fixed {
			r.claimStamp[j] = r.claimCur
		}
		fresh := cl[:0]
		for _, j := range cl {
			if r.claimStamp[j] != r.claimCur {
				fresh = append(fresh, j)
			}
		}
		r.applyClaims(fresh, +1)
		n.fixed = dedup(append(n.fixed, fresh...))
		n.shareTracks = append(n.shareTracks, Track{Net: n.name, Layer: r.gr.layers[li], A: pd.Box.C, B: c.c, Width: stubW, Kind: "fanout"})
		n.shareClaims = append(n.shareClaims, append([]int32(nil), fresh...))
		return true
	}
	return false
}
