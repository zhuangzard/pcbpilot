package pcbauto

import (
	"container/heap"
	"fmt"
	"math"
	"sort"
)

// Joint placement+routing score.
//
// The product is the routed board; a placement score only predicts it. So
// the joint score is gated, then scaled by completion, then by quality:
//
//	overall = gate × completion factor × quality
//	  gate              shorts or part overlaps → "not deliverable", capped at 40
//	  completion factor (completion/100)² × (plane pads tied)² × 0.97^DRC — the last 5 % of
//	                    unrouted connections costs a disproportionate share
//	                    of manual work, so completion is squared
//	  quality           weighted geometric mean of three groups, so no group can
//	                    be averaged away by another:
//	    electrical 45 % measured on the routed copper: hot-loop path, decap
//	                    loop, ESD stub, RF feed length, diff-pair/HS findings
//	    efficiency 25 % detour ratio (routed length / Euclidean pad MST over
//	                    fully routed nets; 45° routing beats Manhattan), vias per
//	                    connection
//	    placement  30 % what routing cannot see: assembly, tidiness, edge I/O,
//	                    partition (supplied by the caller from layout-score)

// JointItem is one measured term.
type JointItem struct {
	Group  string  `json:"group"` // electrical | efficiency | placement
	ID     string  `json:"id"`
	Score  float64 `json:"score"` // 0–100
	Weight float64 `json:"weight"`
	Detail string  `json:"detail"`
}

// JointScore is the combined verdict for one placed-and-routed board.
type JointScore struct {
	Overall     float64  `json:"overall"`
	Deliverable bool     `json:"deliverable"`
	Gates       []string `json:"gates,omitempty"`
	Completion  float64  `json:"completion"`
	// PlanePads / PlaneOpen: connections of plane-delivered and ground nets
	// (pads − 1 per net) and how many failed to close. Completion counts
	// signal connections only, so without this a board whose ground pads are
	// floating would still score as complete.
	PlanePads        int                `json:"planePads"`
	PlaneOpen        int                `json:"planeOpen"`
	DRC              int                `json:"drc"`
	CompletionFactor float64            `json:"completionFactor"`
	Quality          float64            `json:"quality"`
	Groups           map[string]float64 `json:"groups"`
	Items            []JointItem        `json:"items"`
}

// JointOptions carries what the caller measured outside the engine.
type JointOptions struct {
	// PlacementScore is the placement-only quality (0–100), e.g. the mean
	// of layout-score's tidy/compact/edge-io/partition/clearance dimensions.
	// Negative means "not measured" and the group is left out.
	PlacementScore float64
	// Overlaps is the count of part body overlaps (a hard gate).
	Overlaps int
}

var jointGroupWeight = map[string]float64{"electrical": 0.45, "efficiency": 0.25, "placement": 0.30}

// Joint scores a routed board.
func Joint(b *Board, an *Analysis, c *Circuit, st *Stackup, rr *RouteResult, drc *DRCReport, opt JointOptions) *JointScore {
	js := &JointScore{Groups: map[string]float64{}}
	if rr == nil {
		return js
	}
	js.Completion = rr.Stats.Completion
	if drc != nil {
		js.DRC = len(drc.Violations)
		for _, v := range drc.Violations {
			if v.NetB != "" && v.NetA != v.NetB && v.Gap <= 0 {
				js.Gates = append(js.Gates, fmt.Sprintf("short %s↔%s", v.NetA, v.NetB))
				break
			}
		}
	}
	if opt.Overlaps > 0 {
		js.Gates = append(js.Gates, fmt.Sprintf("%d part overlaps", opt.Overlaps))
	}
	cg := newCopperGraph(b, an, st, rr)
	// One net set for numerator and denominator: plane nets plus every
	// ground net (a split ground off the plane is still a ground).
	planeSet := map[string]bool{}
	for net := range cg.plane {
		planeSet[net] = true
	}
	for net := range cg.pads {
		if an.Plan(net, b.Rules).Role == RoleGround {
			planeSet[net] = true
		}
	}
	// Counted in connections, like signal completion: a net of n pads has
	// n−1; each failure record is one connection that did not close (a
	// record lists the whole stranded group, which may be a hundred pads).
	for net := range planeSet {
		if n := len(cg.pads[net]); n > 1 {
			js.PlanePads += n - 1
		}
	}
	for _, u := range rr.Unrouted {
		if planeSet[u.Net] {
			js.PlaneOpen++
		}
	}
	planeOK := 1.0
	if js.PlanePads > 0 {
		planeOK = math.Max(0, 1-float64(js.PlaneOpen)/float64(js.PlanePads))
	}
	js.CompletionFactor = math.Pow(js.Completion/100, 2) * math.Pow(planeOK, 2) * math.Pow(0.97, float64(js.DRC))

	// ---- electrical ----
	add := func(group, id string, score, w float64, detail string) {
		js.Items = append(js.Items, JointItem{Group: group, ID: id, Score: clamp(score, 0, 100), Weight: w, Detail: detail})
	}
	ramp := func(v, good, bad float64) float64 { // 100 at ≤good, 0 at ≥bad
		if bad == good {
			return 100
		}
		return clamp(100*(bad-v)/(bad-good), 0, 100)
	}
	if c != nil {
		// Hot loops along the copper.
		var hl []float64
		for _, cv := range c.Converters {
			if l, ok := routedHotLoop(b, an, cv, cg); ok {
				hl = append(hl, l)
			}
		}
		if len(hl) > 0 {
			m := mean(hl)
			add("electrical", "hot-loop", ramp(m, 300, 1200), 0.3, fmt.Sprintf("%d switcher loops, mean routed perimeter %.0f mil", len(hl), m))
		}
		// Decap loops: cap rail pad → IC pin plus cap ground pad → ground.
		var dl []float64
		for _, bl := range c.Blocks {
			for _, m := range bl.Members {
				if m.Role != "decap" && m.Role != "hot-loop" {
					continue
				}
				if m.Role == "hot-loop" {
					continue
				}
				pin := padAt(b, m.Pin)
				cp := b.Part(m.Ref)
				if pin == nil || cp == nil {
					continue
				}
				var rail, gnd *Pad
				for _, pd := range cp.Pads {
					if pd.Net == pin.Net {
						rail = pd
					} else if an.Plan(pd.Net, b.Rules).Role == RoleGround {
						gnd = pd
					}
				}
				if rail == nil {
					continue
				}
				d := cg.dist(rail, pin)
				if gnd != nil {
					d += cg.toGround(gnd)
				}
				dl = append(dl, d)
			}
		}
		if len(dl) > 0 {
			m := mean(dl)
			add("electrical", "decap-loop", ramp(m, 120, 600), 0.3, fmt.Sprintf("%d decaps, mean routed loop %.0f mil (rail path + ground return)", len(dl), m))
		}
		// Interface chains: ESD stub and RF feed.
		var stubs, feeds []float64
		for _, ch := range c.Chains {
			if ch.conn == nil {
				continue
			}
			if ch.Weight >= 3 {
				// RF feed: connector/antenna pad to the first series part.
				for _, n := range ch.Nodes {
					if n.in != nil {
						feeds = append(feeds, cg.dist(ch.conn, n.in))
						break
					}
				}
				continue
			}
			if ch.ic == nil {
				continue
			}
			for _, n := range ch.Nodes {
				if !n.Shunt || n.in == nil || !isProtectionPart(c, b.Part(n.Ref)) || n.in.Net != ch.conn.Net {
					continue
				}
				// Stub from the trunk in a tree: (d(C,P)+d(P,I)−d(C,I))/2,
				// using the first-net leg to the connector.
				dcp := cg.dist(ch.conn, n.in)
				var dpi, dci float64
				if ch.ic.Net == ch.conn.Net {
					dpi, dci = cg.dist(n.in, ch.ic), cg.dist(ch.conn, ch.ic)
				} else {
					// Series parts in between: measure to the first series in-pad.
					var first *Pad
					for _, s := range ch.Nodes {
						if !s.Shunt && s.in != nil {
							first = s.in
							break
						}
					}
					if first == nil {
						continue
					}
					dpi, dci = cg.dist(n.in, first), cg.dist(ch.conn, first)
				}
				if math.IsInf(dcp+dpi+dci, 0) {
					continue
				}
				stubs = append(stubs, math.Max(0, (dcp+dpi-dci)/2))
			}
		}
		if len(stubs) > 0 {
			m := mean(stubs)
			add("electrical", "esd-stub", ramp(m, 40, 300), 0.2, fmt.Sprintf("%d protected lines, mean routed stub %.0f mil", len(stubs), m))
		}
		if len(feeds) > 0 {
			m := mean(feeds)
			add("electrical", "rf-feed", ramp(m, 200, 1500), 0.2, fmt.Sprintf("%d RF feeds, mean routed length %.0f mil", len(feeds), m))
		}
	}
	if st != nil {
		si := CheckSI(b, an, st, rr)
		if len(si.Nets) > 0 {
			bad := 0
			for _, f := range si.Findings {
				switch f.Kind {
				case "skew", "split-crossing", "vias":
					bad++
				}
			}
			frac := float64(bad) / float64(len(si.Nets))
			add("electrical", "high-speed", 100*(1-math.Min(1, frac*2)), 0.2, fmt.Sprintf("%d HS nets, %d skew/split/via findings", len(si.Nets), bad))
		}
	}

	// ---- efficiency ----
	var routedLen, mstLen float64
	conns := 0
	open := map[string]bool{}
	for _, u := range rr.Unrouted {
		open[u.Net] = true
	}
	nets := make([]string, 0, len(cg.tracksByNet))
	for net := range cg.tracksByNet {
		nets = append(nets, net)
	}
	sort.Strings(nets)
	for _, net := range nets {
		ts := cg.tracksByNet[net]
		if cg.plane[net] || len(ts) == 0 || open[net] {
			continue
		}
		var pts []Point
		for _, pd := range cg.pads[net] {
			pts = append(pts, pd.Box.C)
		}
		if len(pts) < 2 {
			continue
		}
		l := 0.0
		for _, t := range ts {
			l += t.A.Dist(t.B)
		}
		routedLen += l
		mstLen += euclidMST(pts)
		conns += len(pts) - 1
	}
	if mstLen > 0 {
		ratio := routedLen / mstLen
		add("efficiency", "detour", ramp(ratio, 1.2, 2.2), 0.6, fmt.Sprintf("routed %.1f in over pad-MST %.1f in (ratio %.2f, fully routed nets)", routedLen/1000, mstLen/1000, ratio))
	}
	if conns > 0 {
		vpc := float64(rr.Stats.Vias) / float64(conns)
		add("efficiency", "vias", ramp(vpc, 0.5, 3), 0.4, fmt.Sprintf("%d signal vias over %d connections (%.2f each)", rr.Stats.Vias, conns, vpc))
	}

	// ---- placement ----
	if opt.PlacementScore >= 0 {
		add("placement", "assembly", opt.PlacementScore, 1, "layout-score dimensions routing cannot see")
	}

	// Group means, then the weighted geometric mean.
	sum, wsum := map[string]float64{}, map[string]float64{}
	for _, it := range js.Items {
		sum[it.Group] += it.Score * it.Weight
		wsum[it.Group] += it.Weight
	}
	logSum, wTot := 0.0, 0.0
	groups := make([]string, 0, len(sum))
	for g := range sum {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		s := sum[g] / wsum[g]
		js.Groups[g] = s
		w := jointGroupWeight[g]
		logSum += w * math.Log(math.Max(s, 1)/100)
		wTot += w
	}
	js.Quality = 100
	if wTot > 0 {
		js.Quality = 100 * math.Exp(logSum/wTot)
	}
	js.Overall = js.CompletionFactor * js.Quality
	js.Deliverable = len(js.Gates) == 0 && js.Completion >= 100 && js.DRC == 0 && js.PlaneOpen == 0
	if len(js.Gates) > 0 {
		js.Overall = math.Min(js.Overall, 40)
	}
	return js
}

func mean(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

// euclidMST is the Euclidean minimum spanning tree length (Prim, O(n²)).
func euclidMST(pts []Point) float64 {
	n := len(pts)
	if n < 2 {
		return 0
	}
	in := make([]bool, n)
	best := make([]float64, n)
	for i := range best {
		best[i] = math.Inf(1)
	}
	best[0] = 0
	total := 0.0
	for k := 0; k < n; k++ {
		u := -1
		for i := 0; i < n; i++ {
			if !in[i] && (u < 0 || best[i] < best[u]) {
				u = i
			}
		}
		in[u] = true
		total += best[u]
		for i := 0; i < n; i++ {
			if !in[i] {
				if d := pts[u].Dist(pts[i]); d < best[i] {
					best[i] = d
				}
			}
		}
	}
	return total
}

// ---- copper graph ----------------------------------------------------------

// copperGraph measures distances along a net's routed copper: track segments
// are edges, a via adds a layer-change cost, a pad joins every track end it
// covers. Plane nets have no tracks to follow; their current flows through
// the plane, so a pad reaches the plane through its nearest via/pad on that
// net plus one transition.
type copperGraph struct {
	tracksByNet map[string][]Track
	pads        map[string][]*Pad
	vias        map[string][]Via
	plane       map[string]bool
	cache       map[[2]*Pad]float64
}

const (
	viaTransitionMil = 30 // a through-via hop, ≈ inductance-equivalent length
	// planeFactor scales distance travelled inside a plane: a solid plane's
	// spreading inductance is a fraction of a trace's over the same span.
	planeFactor = 0.25
)

func newCopperGraph(b *Board, an *Analysis, st *Stackup, rr *RouteResult) *copperGraph {
	g := &copperGraph{tracksByNet: map[string][]Track{}, pads: map[string][]*Pad{}, vias: map[string][]Via{},
		plane: map[string]bool{}, cache: map[[2]*Pad]float64{}}
	for _, t := range rr.Tracks {
		g.tracksByNet[t.Net] = append(g.tracksByNet[t.Net], t)
	}
	for _, v := range rr.Vias {
		g.vias[v.Net] = append(g.vias[v.Net], v)
	}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net != "" {
				g.pads[pd.Net] = append(g.pads[pd.Net], pd)
			}
		}
	}
	if st != nil {
		for _, l := range st.Stack {
			for _, n := range l.Nets {
				g.plane[n] = true
			}
		}
	}
	for n, np := range an.ByNet {
		if np.Plane {
			g.plane[n] = true
		}
	}
	return g
}

// toGround is the path from a ground pad into the ground system: to the
// nearest ground via (then through the plane) or, with no via, the nearest
// other ground pad reached along copper.
func (g *copperGraph) toGround(pd *Pad) float64 {
	best := math.Inf(1)
	for _, v := range g.vias[pd.Net] {
		best = math.Min(best, pd.Box.C.Dist(v.C)+viaTransitionMil)
	}
	if math.IsInf(best, 1) {
		best = 200 // no via data: a nominal return path
	}
	return best
}

// dist is the shortest copper path between two pads of one net.
func (g *copperGraph) dist(a, b *Pad) float64 {
	if a == nil || b == nil || a.Net != b.Net {
		return math.Inf(1)
	}
	if a == b {
		return 0
	}
	key := [2]*Pad{a, b}
	if a.Box.C.X > b.Box.C.X {
		key = [2]*Pad{b, a}
	}
	if d, ok := g.cache[key]; ok {
		return d
	}
	var d float64
	if g.plane[a.Net] || len(g.tracksByNet[a.Net]) == 0 {
		// Through the plane: each pad down its nearest via, straight across.
		d = g.toGround(a) + g.toGround(b) + planeFactor*a.Box.C.Dist(b.Box.C)
		if len(g.vias[a.Net]) == 0 {
			d = a.Box.C.Dist(b.Box.C) * 1.2 // untracked, unplaned: assume a modest detour
		}
	} else {
		d = g.trackPath(a, b)
	}
	g.cache[key] = d
	return d
}

// trackPath runs Dijkstra over the net's segment endpoints (and via
// centres), joining points closer than half a track width and linking each
// pad to the points inside its copper.
func (g *copperGraph) trackPath(a, b *Pad) float64 {
	ts := g.tracksByNet[a.Net]
	var pts []Point
	idx := func(p Point) int {
		for i, q := range pts {
			if q.Dist(p) < 1 {
				return i
			}
		}
		pts = append(pts, p)
		return len(pts) - 1
	}
	type edge struct {
		to int
		w  float64
	}
	adj := map[int][]edge{}
	link := func(i, j int, w float64) {
		adj[i] = append(adj[i], edge{j, w})
		adj[j] = append(adj[j], edge{i, w})
	}
	for _, t := range ts {
		i, j := idx(t.A), idx(t.B)
		link(i, j, t.A.Dist(t.B))
	}
	for _, v := range g.vias[a.Net] {
		i := idx(v.C)
		for j, p := range pts {
			if j != i && p.Dist(v.C) < v.Dia/2+1 {
				link(i, j, viaTransitionMil)
			}
		}
	}
	// Points lying on another segment's interior (T-junctions).
	for _, t := range ts {
		for j, p := range pts {
			if d, along := segDist(p, t.A, t.B); d < math.Max(t.Width/2, 1) && along > 1 && along < t.A.Dist(t.B)-1 {
				link(idx(t.A), j, along)
			}
		}
	}
	src, dst := len(pts), len(pts)+1
	padLink := func(pd *Pad, node int) {
		bb := pd.Box.Bounds().Expand(2)
		for j, p := range pts {
			if bb.Contains(p) {
				link(node, j, pd.Box.C.Dist(p))
			}
		}
	}
	padLink(a, src)
	padLink(b, dst)
	n := len(pts) + 2
	distv := make([]float64, n)
	for i := range distv {
		distv[i] = math.Inf(1)
	}
	distv[src] = 0
	h := &jointHeap{}
	heap.Push(h, jointItem{node: src, cost: 0})
	for h.Len() > 0 {
		it := heap.Pop(h).(jointItem)
		if it.cost > distv[it.node] {
			continue
		}
		if it.node == dst {
			return it.cost
		}
		for _, e := range adj[it.node] {
			if c := it.cost + e.w; c < distv[e.to] {
				distv[e.to] = c
				heap.Push(h, jointItem{node: e.to, cost: c})
			}
		}
	}
	// Not connected by tracks (partly unrouted): the straight distance
	// doubled, so an open loop never looks good.
	return 2 * a.Box.C.Dist(b.Box.C)
}

// routedHotLoop walks the hot-loop pads along copper: pad-to-pad legs on one
// net follow the routing; legs through a component body are straight.
func routedHotLoop(b *Board, an *Analysis, cv *Converter, g *copperGraph) (float64, bool) {
	pads, ok := hotLoopPads(b, an, cv)
	if !ok {
		return 0, false
	}
	total := 0.0
	for i := range pads {
		p, q := pads[i], pads[(i+1)%len(pads)]
		if p.Net == q.Net {
			d := g.dist(p, q)
			if math.IsInf(d, 1) {
				d = 2 * p.Box.C.Dist(q.Box.C)
			}
			total += d
		} else {
			total += p.Box.C.Dist(q.Box.C)
		}
	}
	return total, true
}

type jointItem struct {
	node int
	cost float64
}
type jointHeap []jointItem

func (h jointHeap) Len() int           { return len(h) }
func (h jointHeap) Less(i, j int) bool { return h[i].cost < h[j].cost }
func (h jointHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *jointHeap) Push(x any)        { *h = append(*h, x.(jointItem)) }
func (h *jointHeap) Pop() any          { o := *h; it := o[len(o)-1]; *h = o[:len(o)-1]; return it }
