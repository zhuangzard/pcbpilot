package pcbauto

import (
	"math"
	"sort"
)

// netOut is one net's emitted copper.
type netOut struct {
	tracks []Track
	vias   []Via
}

// emit converts grid paths into tracks and vias, then closes the loop with
// the exact DRC: nets involved in a violation are ripped up and re-routed
// strictly with a larger clearance margin; whatever still violates after
// three rounds is removed and reported unrouted — never shipped as a short.
func (r *router) emit(res *RouteResult) {
	outs := map[*rnet]*netOut{}
	for _, n := range r.nets {
		outs[n] = r.emitNet(n)
	}
	collect := func() ([]Track, []Via) {
		var ts []Track
		var vs []Via
		for _, n := range r.nets {
			ts = append(ts, n.fanTracks...)
			ts = append(ts, n.shareTracks...)
			ts = append(ts, escTracks(n)...)
			vs = append(vs, n.fanVias...)
			ts = append(ts, outs[n].tracks...)
			vs = append(vs, outs[n].vias...)
		}
		return ts, vs
	}
	routedOnly := func(v Violation) []*rnet {
		// The movable party of a violation: a net with routed copper, the
		// less important one first when both are.
		var c []*rnet
		for _, name := range []string{v.NetA, v.NetB} {
			if n := r.byName[name]; n != nil && len(n.paths) > 0 {
				c = append(c, n)
			}
		}
		if len(c) == 2 && c[0].plan.Priority < c[1].plan.Priority {
			c[0], c[1] = c[1], c[0]
		}
		if len(c) > 1 {
			c = c[:1]
		}
		return c
	}
	rounds := 3
	if r.opt.NoRepair {
		rounds = 0
	}
	for round := 0; round < rounds; round++ {
		ts, vs := collect()
		drc := CheckDRC(r.b, r.an, r.st, ts, vs)
		bad := map[*rnet]bool{}
		var order []*rnet
		for _, v := range drc.Violations {
			for _, n := range routedOnly(v) {
				if !bad[n] {
					bad[n] = true
					order = append(order, n)
				}
			}
		}
		if round == 0 {
			res.Stats.PreRepairViolations = len(drc.Violations)
		}
		if len(order) == 0 {
			break
		}
		res.Stats.Repaired += len(order)
		r.strict = true
		for _, n := range order {
			r.applyClaims(n.claims, -1)
			n.claims, n.paths = nil, nil
			// Grow the margin gently: a quarter cell first (most misses are
			// sub-cell), more only if the net violates again.
			if round == 0 {
				r.inflate(n, r.gr.g/4)
			} else {
				r.inflate(n, r.gr.g/2)
			}
			if n.onPlane || n.poured {
				// A plane net is not rerouted pad by pad (K230 GND: 878 pads,
				// every one "timeout"): only its islands are bridged again.
				saved := n.groups
				if gs := r.simulate(n); len(gs) > 1 {
					n.groups = gs
					r.routeNet(n)
				}
				n.groups = saved
				outs[n] = r.emitNet(n)
				continue
			}
			if !r.routeNet(n) && len(n.escs) > 0 {
				// Its own fixed escapes may be what walls it in: release
				// them and route from the vias and pads instead.
				for len(n.escs) > 0 {
					r.dropEscape(n, 0)
					res.Stats.EscapesReleased++
				}
				r.routeNet(n)
			}
			outs[n] = r.emitNet(n)
		}
		r.strict = false
	}
	// High-speed pairs: equalise intra-pair length before the final gate.
	r.tuneLengths(outs, res)
	// Final gate: nothing that violates DRC is shipped. A violation with a
	// routed party drops that net's routing; one with none — two fan-out vias,
	// a fan-out via and the board edge — drops the offending fan-out (R-0: the
	// old gate skipped those, so timed-out BGA boards shipped via-via
	// violations). Repeat until clean.
	for pass := 0; pass < 4 && !r.opt.NoRepair; pass++ {
		ts, vs := collect()
		final := CheckDRC(r.b, r.an, r.st, ts, vs).Violations
		if len(final) == 0 {
			break
		}
		changed := false
		for _, v := range final {
			if nets := routedOnly(v); len(nets) > 0 {
				for _, n := range nets {
					r.applyClaims(n.claims, -1)
					n.claims, n.paths = nil, nil
					outs[n] = &netOut{}
					var pads []string
					for _, g := range n.groups {
						pads = append(pads, padKeys(g)...)
					}
					n.failed = []Unrouted{{Net: n.name, Pads: pads, Reason: "drc-unrepairable"}}
					changed = true
				}
				continue
			}
			if r.dropFanoutAt(v) {
				changed = true
			}
		}
		if !changed {
			res.Notes = append(res.Notes, sprintf("final gate: %d violation(s) with no removable party", len(final)))
			break
		}
	}

	conn, routed := 0, 0
	for _, n := range r.nets {
		o := outs[n]
		res.Tracks = append(res.Tracks, n.fanTracks...)
		res.Tracks = append(res.Tracks, n.shareTracks...)
		res.Tracks = append(res.Tracks, escTracks(n)...)
		res.Vias = append(res.Vias, n.fanVias...)
		res.Tracks = append(res.Tracks, o.tracks...)
		res.Vias = append(res.Vias, o.vias...)
		res.Stats.Vias += len(o.vias)
		for _, t := range o.tracks {
			res.Stats.WireLengthIn += t.A.Dist(t.B) / 1000
		}
		res.Unrouted = append(res.Unrouted, n.failed...)
		if len(n.groups) > 1 && !n.onPlane && !n.poured && n.route {
			conn += len(n.groups) - 1
			if len(n.paths) > 0 {
				routed += len(n.groups) - 1 - failedGroups(n)
			}
		}
	}
	res.Stats.Connections = conn
	res.Stats.Routed = max(routed, 0)
	res.Stats.Unrouted = len(res.Unrouted)
	if conn > 0 {
		res.Stats.Completion = math.Round(float64(res.Stats.Routed)/float64(conn)*1000) / 10
	} else {
		res.Stats.Completion = 100
	}
	sort.SliceStable(res.Unrouted, func(i, j int) bool { return res.Unrouted[i].Net < res.Unrouted[j].Net })
}

func failedGroups(n *rnet) int {
	k := 0
	for _, f := range n.failed {
		if f.Reason != "drc-unrepairable" {
			k++
		}
	}
	return k
}

// inflate widens a net's clearance share (repair after an exact-DRC miss).
func (r *router) inflate(n *rnet, d float64) {
	n.share += d
	n.radius = n.width/2 + n.share
	n.viaR = r.b.Rules.ViaDia/2 + n.share
	n.neckR = n.neckW/2 + n.share
	if n.neckW >= n.width {
		n.neckR = n.radius
	}
}

// emitNet straightens n's grid paths into tracks and vias against the
// current occupancy and re-claims exactly what it emits.
func (r *router) emitNet(n *rnet) *netOut {
	gr := r.gr
	o := &netOut{}
	if len(n.paths) == 0 {
		return o
	}
	r.applyClaims(n.claims, -1)
	var newClaims []int32
	for _, p := range n.paths {
		runs := splitRuns(gr, p)
		for ri, run := range runs {
			li := run.layer
			full := n.width
			if !r.st.Stack[li].Outer && n.plan.InnerWidthMil > 0 {
				full = n.plan.InnerWidthMil
			}
			// Per-point width: full, or the neck width where only that fits.
			widths := make([]float64, len(run.pts))
			for k, node := range run.nodes {
				_, x, y := gr.xy(int(node))
				widths[k] = full
				if r.nodeRadius(n, li, x, y) < n.radius {
					widths[k] = n.neckW
				}
			}
			pts := run.pts
			// The off-grid stub from a pad centre to its access node is checked
			// on its own: in dense connectors only the neck width fits.
			stubW := func(a, b Point, w float64) float64 {
				if w > n.neckW && !r.segmentOK(n, li, a, b, w, true) {
					return n.neckW
				}
				return w
			}
			if ri == 0 && p.fromPad {
				pts = append([]Point{p.from}, pts...)
				widths = append([]float64{stubW(p.from, pts[1], widths[0])}, widths...)
			}
			if ri == len(runs)-1 && p.toPad {
				last := pts[len(pts)-1]
				pts = append(pts, p.to)
				widths = append(widths, stubW(last, p.to, widths[len(widths)-1]))
			}
			// Constant-width sub-runs; the step between two sub-runs uses the
			// narrower width so a full-width segment never enters a neck node.
			addSeg := func(a, b Point, w float64) {
				if a.Dist(b) < 1e-6 {
					return
				}
				o.tracks = append(o.tracks, Track{Net: n.name, Layer: gr.layers[li], A: a, B: b, Width: w, Kind: "route"})
				newClaims = r.claimSegment(n, li, a, b, w, newClaims)
			}
			start := 0
			for k := 1; k <= len(pts); k++ {
				if k < len(pts) && widths[k] == widths[start] {
					continue
				}
				sub := r.stringPull(n, li, compress(append([]Point(nil), pts[start:k]...)), widths[start])
				for q := 1; q < len(sub); q++ {
					addSeg(sub[q-1], sub[q], widths[start])
				}
				if k < len(pts) {
					addSeg(pts[k-1], pts[k], math.Min(widths[k-1], widths[k]))
				}
				start = k
			}
			if ri > 0 {
				o.vias = append(o.vias, Via{Net: n.name, C: run.pts[0], Drill: r.b.Rules.ViaDrill, Dia: r.b.Rules.ViaDia, Kind: "route"})
				newClaims = r.claimVia(n, run.nodes[0], newClaims)
			}
		}
	}
	n.claims = r.minusFixed(n, dedup(newClaims))
	r.applyClaims(n.claims, +1)
	return o
}

type run struct {
	layer int
	pts   []Point
	nodes []int32
}

// claimVia claims the via disk on every layer at the node's column.
func (r *router) claimVia(n *rnet, node int32, out []int32) []int32 {
	gr := r.gr
	_, x, y := gr.xy(int(node))
	for l := range gr.layers {
		for _, o := range gr.disk(n.viaR) {
			if xx, yy := x+o[0], y+o[1]; gr.in(xx, yy) {
				out = append(out, int32(gr.idx(l, xx, yy)))
			}
		}
	}
	return out
}

// splitRuns cuts a node path at vias into per-layer point runs.
func splitRuns(gr *grid, p rpath) []run {
	var out []run
	cur := run{layer: -1}
	for _, i := range p.nodes {
		l, x, y := gr.xy(int(i))
		c := gr.center(x, y)
		if l != cur.layer {
			if cur.layer >= 0 {
				out = append(out, cur)
			}
			cur = run{layer: l}
		}
		cur.pts = append(cur.pts, c)
		cur.nodes = append(cur.nodes, i)
	}
	if cur.layer >= 0 {
		out = append(out, cur)
	}
	return out
}

// compress drops collinear interior points.
func compress(pts []Point) []Point {
	if len(pts) < 3 {
		return pts
	}
	out := []Point{pts[0]}
	for i := 1; i < len(pts)-1; i++ {
		a, b, c := out[len(out)-1], pts[i], pts[i+1]
		if a.Dist(b) < 1e-9 {
			continue
		}
		cross := (b.X-a.X)*(c.Y-b.Y) - (b.Y-a.Y)*(c.X-b.X)
		dot := (b.X-a.X)*(c.X-b.X) + (b.Y-a.Y)*(c.Y-b.Y)
		if math.Abs(cross) < 1e-6 && dot > 0 {
			continue
		}
		out = append(out, b)
	}
	return append(out, pts[len(pts)-1])
}

func octilinear(a, b Point) bool {
	dx, dy := math.Abs(b.X-a.X), math.Abs(b.Y-a.Y)
	return dx < 1e-6 || dy < 1e-6 || math.Abs(dx-dy) < 1e-6
}

// stringPull greedily replaces vertex chains with the farthest octilinear,
// clear straight segment. Pad stubs (first/last segment) may be any angle.
func (r *router) stringPull(n *rnet, li int, pts []Point, width float64) []Point {
	if len(pts) < 3 {
		return pts
	}
	out := []Point{pts[0]}
	i := 0
	for i < len(pts)-1 {
		j := len(pts) - 1
		for ; j > i+1; j-- {
			a, b := pts[i], pts[j]
			anyAngle := i == 0 && j == 1 || j == len(pts)-1 && i == len(pts)-2
			if !anyAngle && !octilinear(a, b) {
				continue
			}
			if r.segmentOK(n, li, a, b, width, true) {
				break
			}
		}
		out = append(out, pts[j])
		i = j
	}
	// Chamfer remaining 90° corners into 45° where clear.
	return r.chamfer(n, li, compress(out), width)
}

// chamfer cuts right-angle corners (acid-trap and reflection prone) with a
// 45° segment of length up to 3 track widths when the copper stays clear.
func (r *router) chamfer(n *rnet, li int, pts []Point, width float64) []Point {
	if len(pts) < 3 {
		return pts
	}
	out := []Point{pts[0]}
	for i := 1; i < len(pts)-1; i++ {
		a, b, c := out[len(out)-1], pts[i], pts[i+1]
		u := b.Sub(a)
		v := c.Sub(b)
		lu, lv := math.Hypot(u.X, u.Y), math.Hypot(v.X, v.Y)
		if lu < 1e-6 || lv < 1e-6 || math.Abs((u.X*v.X+u.Y*v.Y)/(lu*lv)) > 1e-6 {
			out = append(out, b)
			continue
		}
		// Cut on whole grid steps so the chamfer points stay on grid nodes
		// (off-grid vertices are where discretisation errors appear).
		cut := math.Floor(math.Min(3*width, math.Min(lu, lv)/2)/r.gr.g) * r.gr.g
		p1 := b.Sub(u.Scale(cut / lu))
		p2 := b.Add(v.Scale(cut / lv))
		if cut > r.gr.g && r.segmentOK(n, li, p1, p2, width, true) {
			out = append(out, p1, p2)
		} else {
			out = append(out, b)
		}
	}
	return append(out, pts[len(pts)-1])
}

// dropFanoutAt removes the fan-out via (or stub) of NetA/NetB at violation
// v: an unpinned one first, then the less important net's. The pad it served
// is reported (the pour or plane may still reach it; it is not assumed).
func (r *router) dropFanoutAt(v Violation) bool {
	type cand struct {
		n      *rnet
		k      int
		pinned bool
	}
	var cs []cand
	// A shared stub in violation goes first: dropping it costs one ball's
	// tie, not a via other balls hang on.
	for _, name := range []string{v.NetA, v.NetB} {
		n := r.byName[name]
		if n == nil {
			continue
		}
		for k, es := range n.escs {
			for _, t := range es.tracks {
				if d, _ := segDist(v.At, t.A, t.B); d <= t.Width/2+v.Required+2 {
					pd := es.pd
					r.dropEscape(n, k)
					n.failed = append(n.failed, Unrouted{Net: n.name, Pads: []string{pd.Key()}, Reason: "escape-drc"})
					return true
				}
			}
		}
		for k, t := range n.shareTracks {
			if d, _ := segDist(v.At, t.A, t.B); d <= t.Width/2+v.Required+2 {
				r.dropShare(n, k)
				n.failed = append(n.failed, Unrouted{Net: n.name, Reason: "fanout-drc"})
				return true
			}
		}
	}
	for _, name := range []string{v.NetA, v.NetB} {
		n := r.byName[name]
		if n == nil {
			continue
		}
		for k, via := range n.fanVias {
			near := via.C.Dist(v.At) <= via.Dia/2+v.Required+2
			if !near && n.fanTrack[k] >= 0 {
				t := n.fanTracks[n.fanTrack[k]]
				d, _ := segDist(v.At, t.A, t.B)
				near = d <= t.Width/2+v.Required+2
			}
			if near {
				cs = append(cs, cand{n, k, k < len(n.fanPinned) && n.fanPinned[k]})
			}
		}
	}
	if len(cs) == 0 {
		return false
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].pinned != cs[j].pinned {
			return !cs[i].pinned
		}
		return cs[i].n.plan.Priority > cs[j].n.plan.Priority
	})
	c := cs[0]
	var keep []int
	for k := range c.n.fanVias {
		if k != c.k {
			keep = append(keep, k)
		}
	}
	// The pad the fan-out served: its stub starts at the pad centre.
	pad := ""
	if ti := c.n.fanTrack[c.k]; ti >= 0 {
		a := c.n.fanTracks[ti].A
		for _, g := range c.n.groups {
			for _, pd := range g {
				if pd.Box.C.Dist(a) < 0.5 {
					pad = pd.Key()
				}
			}
		}
	}
	r.keepFanouts(c.n, keep)
	// A dog-bone that served a signal ball no longer offers an escape node.
	for pd, e := range r.escape {
		if pd.Key() == pad {
			delete(r.escape, pd)
			_ = e
		}
	}
	var pads []string
	if pad != "" {
		pads = []string{pad}
	}
	c.n.failed = append(c.n.failed, Unrouted{Net: c.n.name, Pads: pads, Reason: "fanout-drc"})
	return true
}

// dropShare removes shared stub k of n and its claims.
func (r *router) dropShare(n *rnet, k int) {
	cl := n.shareClaims[k]
	r.applyClaims(cl, -1)
	drop := map[int32]bool{}
	for _, i := range cl {
		drop[i] = true
	}
	var fixed []int32
	for _, i := range n.fixed {
		if !drop[i] {
			fixed = append(fixed, i)
		}
	}
	n.fixed = fixed
	n.shareTracks = append(n.shareTracks[:k], n.shareTracks[k+1:]...)
	n.shareClaims = append(n.shareClaims[:k], n.shareClaims[k+1:]...)
}
