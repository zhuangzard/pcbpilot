package pcbauto

import (
	"container/heap"
	"math"
	"sort"
)

// BGA escape: the step between the dog-bone fan-out and general routing.
//
// A ball deep in the array can only leave it through the channels between the
// balls (top layer) or between the dog-bone vias (every other layer), at the
// minimum track width. Left to the negotiated router, every PathFinder
// iteration re-threads hundreds of nets through the same few channels at
// full congestion cost — on RK3568 that was where the routing budget went
// (131 of 148 failed ball connections were timeouts, spread over all rings).
//
// So the escapes are solved once, before routing, as fixed copper:
//
//   - every signal ball whose net leaves the array escapes to the array
//     boundary (the ball bbox grown by one pitch), outer rings first;
//   - balls of the top-escape rings start from the pad on its own layer;
//     dog-boned balls start from their via and try the inner signal layers
//     first, then the bottom, then the top — the stackup order of practice;
//   - each escape is a single-layer, conflict-free search at the escape width
//     (the process minimum), heading for the side facing the rest of the net;
//   - the exit becomes the ball's only access node: the router starts the net
//     outside the array, at full width.
//
// An escape that fails leaves the ball to the router as before.

// bgaSkipEscape disables the escape pre-routing (diagnostics only).
var bgaSkipEscape bool

// bgaEsc is one committed escape.
type bgaEsc struct {
	pd     *Pad
	tracks []Track
	claims []int32
	via    Point // the dog-bone via it starts from (zero for a top escape)
	hasVia bool
	end    int32 // grid node of the exit
	endPt  Point
}

// escObs is an exact index of the fixed copper escapes must clear: fan-out
// vias and stubs, shared stubs and committed escapes. The grid claims round
// to whole cells; inside a 0.65 mm ball field the margin is about a mil, so
// the first cut (grid legality only) put 64 K230 escapes and 41 fan-outs
// into the exact-DRC gate.
type escItem struct {
	net   int32
	layer int // grid layer, -1 = every layer (a via)
	a, b  Point
	hw    float64
}

type escIndex struct {
	cell    float64
	buckets map[[2]int][]escItem
	maxHW   float64
}

func (x *escIndex) add(it escItem) {
	bb := EmptyRect().AddPoint(it.a).AddPoint(it.b).Expand(it.hw)
	for by := int(math.Floor(bb.MinY / x.cell)); by <= int(math.Floor(bb.MaxY/x.cell)); by++ {
		for bx := int(math.Floor(bb.MinX / x.cell)); bx <= int(math.Floor(bb.MaxX/x.cell)); bx++ {
			x.buckets[[2]int{bx, by}] = append(x.buckets[[2]int{bx, by}], it)
		}
	}
	x.maxHW = math.Max(x.maxHW, it.hw)
}

func (r *router) buildEscIndex() *escIndex {
	x := &escIndex{cell: 20, buckets: map[[2]int][]escItem{}}
	gr := r.gr
	for _, n := range r.nets {
		for _, v := range n.fanVias {
			x.add(escItem{n.id, -1, v.C, v.C, v.Dia / 2})
		}
		for _, t := range append(append([]Track(nil), n.fanTracks...), n.shareTracks...) {
			x.add(escItem{n.id, gr.layerIndex(t.Layer), t.A, t.B, t.Width / 2})
		}
	}
	return x
}

// segClear is the exact clearance test of a track segment of net n on grid
// layer l against the index and against other nets' pads.
func (r *router) segClear(x *escIndex, n *rnet, l int, a, b Point, hw float64) bool {
	gr := r.gr
	for _, p := range []Point{a, a.Add(b).Scale(0.5), b} {
		if !r.padsClear(n, gr.layers[l], p, hw) {
			return false
		}
	}
	reach := hw + x.maxHW + 2*math.Max(r.b.Rules.Clearance, n.plan.ClearanceMil) + 1
	bb := EmptyRect().AddPoint(a).AddPoint(b).Expand(reach)
	for by := int(math.Floor(bb.MinY / x.cell)); by <= int(math.Floor(bb.MaxY/x.cell)); by++ {
		for bx := int(math.Floor(bb.MinX / x.cell)); bx <= int(math.Floor(bb.MaxX/x.cell)); bx++ {
			for _, it := range x.buckets[[2]int{bx, by}] {
				if it.net == n.id || it.layer >= 0 && it.layer != l {
					continue
				}
				clr := math.Max(r.b.Rules.Clearance, math.Max(n.plan.ClearanceMil, r.nets[it.net].plan.ClearanceMil))
				if SegSegDist(a, b, it.a, it.b) < hw+it.hw+clr+0.2 {
					return false
				}
			}
		}
	}
	return true
}

// bgaEscape runs after all fan-out vias are placed.
func (r *router) bgaEscape(res *RouteResult) {
	r.escOf = map[*Pad]*bgaEsc{}
	if !r.opt.BGA || bgaSkipEscape {
		return
	}
	gr := r.gr
	r.strict = true
	defer func() { r.strict = false }()
	r.escIdx = r.buildEscIndex()
	defer func() { r.escIdx = nil }()
	for _, g := range detectBGAs(r.b) {
		var zone Rect
		for _, z := range r.bgaZones {
			if z.part == g.part.Ref {
				zone = z.box
			}
		}
		if zone.W() <= 0 {
			continue
		}
		ball := g.part.Pads[0].Box.W
		rings := topEscapeRings(g.pitch, ball, r.b.Rules)
		type job struct {
			pd  *Pad
			n   *rnet
			dir Point // towards the rest of the net
		}
		var jobs []job
		for _, pd := range g.part.Pads {
			n := r.byName[pd.Net]
			if n == nil || !n.route || n.onPlane || n.poured {
				continue
			}
			_, dog := r.escape[pd]
			if !dog && g.depth[pd] >= rings {
				continue // no via: nothing to start from (the router tries)
			}
			var cen Point
			cnt := 0.0
			for _, gp := range n.groups {
				for _, q := range gp {
					if !zone.Contains(q.Box.C) {
						cen = cen.Add(q.Box.C)
						cnt++
					}
				}
			}
			if cnt == 0 {
				continue // the net stays inside the array
			}
			jobs = append(jobs, job{pd, n, cen.Scale(1 / cnt).Sub(pd.Box.C)})
		}
		// Outer rings first: their escapes are short and straight, and they
		// leave the channels between them for the rings behind.
		sort.SliceStable(jobs, func(i, j int) bool {
			di, dj := g.depth[jobs[i].pd], g.depth[jobs[j].pd]
			if di != dj {
				return di < dj
			}
			return jobs[i].pd.Key() < jobs[j].pd.Key()
		})
		// Layer order for via escapes: inner signal layers, bottom, top.
		var viaLayers []int
		for l := range gr.layers {
			if gr.routable[l] && !r.st.Stack[l].Outer {
				viaLayers = append(viaLayers, l)
			}
		}
		for _, l := range []int{len(gr.layers) - 1, 0} {
			if gr.routable[l] {
				viaLayers = append(viaLayers, l)
			}
		}
		okN, byLayer, failed, failTop, failRing := 0, map[string]int{}, 0, 0, map[int]int{}
		for _, jb := range jobs {
			pd, n := jb.pd, jb.n
			w := r.b.Rules.MinTrack
			if w <= 0 {
				w = r.b.Rules.TrackWidth
			}
			w = math.Min(n.width, w)
			rad := w/2 + n.share
			var layers []int
			var src func(l int) []int32
			e, dog := r.escape[pd]
			if dog {
				layers = viaLayers
				src = func(l int) []int32 { return []int32{int32(gr.idx(l, e[0], e[1]))} }
			} else {
				li := gr.layerIndex(pd.Layer)
				if li < 0 || !gr.routable[li] {
					continue
				}
				layers = []int{li}
				src = func(l int) []int32 {
					var out []int32
					for _, a := range r.access(n, pd) {
						if al, _, _ := gr.xy(int(a)); al == l {
							out = append(out, a)
						}
					}
					return out
				}
			}
			done := false
			for _, l := range layers {
				for _, facing := range []bool{true, false} {
					path := r.escSearch(n, l, src(l), zone, jb.dir, facing, rad)
					if path == nil {
						continue
					}
					r.commitEscape(n, pd, l, path, w, dog, e)
					okN++
					byLayer[r.st.Stack[l].Name]++
					done = true
					break
				}
				if done {
					break
				}
			}
			if !done {
				failed++
				failRing[g.depth[pd]]++
				if !dog {
					failTop++
				}
			}
		}
		res.Notes = append(res.Notes, sprintf("BGA %s escape: %d of %d signal balls pre-escaped to the array boundary %v, %d left to the router (%d top-ring pads; by ring %v)",
			g.part.Ref, okN, len(jobs), byLayer, failed, failTop, failRing))
	}
}

// escSearch is a single-layer, conflict-free search at claim radius rad from
// sources to the cells just outside zone. With facing, only the sides whose
// outward normal points towards dir count as exits.
func (r *router) escSearch(n *rnet, l int, sources []int32, zone Rect, dir Point, facing bool, rad float64) []int32 {
	gr := r.gr
	hw := rad - n.share
	if len(sources) == 0 {
		return nil
	}
	own := map[int32]bool{}
	for _, i := range n.fixed {
		own[i] = true
	}
	// Window: the zone plus room to widen to full width; exits are cells
	// outside the zone.
	x0, y0 := gr.cellOf(Point{zone.MinX, zone.MinY})
	x1, y1 := gr.cellOf(Point{zone.MaxX, zone.MaxY})
	m := 2 + int(math.Ceil(2*n.radius/gr.g))
	x0, y0, x1, y1 = max(x0-m, 0), max(y0-m, 0), min(x1+m, gr.W-1), min(y1+m, gr.H-1)
	side := func(c Point) bool { // an exit cell, on an allowed side
		out := false
		if c.X < zone.MinX && (!facing || dir.X < 0) {
			out = true
		}
		if c.X > zone.MaxX && (!facing || dir.X > 0) {
			out = true
		}
		if c.Y < zone.MinY && (!facing || dir.Y < 0) {
			out = true
		}
		if c.Y > zone.MaxY && (!facing || dir.Y > 0) {
			out = true
		}
		return out
	}
	legal := func(x, y int) bool { return r.escLegal(n, l, x, y, rad, own) }
	ww := x1 - x0 + 1
	idx := func(x, y int) int { return (y-y0)*ww + (x - x0) }
	size := ww * (y1 - y0 + 1)
	dist := make([]float64, size)
	par := make([]int32, size)
	dirs := make([]int8, size)
	closed := make([]bool, size)
	for i := range dist {
		dist[i] = math.Inf(1)
		par[i] = -1
		dirs[i] = -1
	}
	h := &jointHeap{}
	for _, s := range sources {
		sl, x, y := gr.xy(int(s))
		if sl != l || x < x0 || x > x1 || y < y0 || y > y1 {
			continue
		}
		k := idx(x, y)
		dist[k] = 0
		heap.Push(h, jointItem{node: k})
	}
	g := gr.g
	for h.Len() > 0 {
		it := heap.Pop(h).(jointItem)
		k := it.node
		if closed[k] {
			continue
		}
		closed[k] = true
		x, y := x0+k%ww, y0+k/ww
		// The exit hands over to the router at full width: it must be legal
		// there too (a neck-only exit strands the net: no-legal-path).
		if side(gr.center(x, y)) && r.escLegal(n, l, x, y, n.radius, own) {
			var path []int32
			for q := k; q >= 0; q = int(par[q]) {
				path = append(path, int32(gr.idx(l, x0+q%ww, y0+q/ww)))
			}
			for a, b := 0, len(path)-1; a < b; a, b = a+1, b-1 {
				path[a], path[b] = path[b], path[a]
			}
			return path
		}
		pd := dirs[k]
		for d := 0; d < 8; d++ {
			if pd >= 0 {
				turn := (d - int(pd) + 8) % 8
				if turn == 3 || turn == 5 || turn == 4 {
					continue
				}
			}
			xx, yy := x+dirs8[d][0], y+dirs8[d][1]
			if xx < x0 || yy < y0 || xx > x1 || yy > y1 {
				continue
			}
			j := idx(xx, yy)
			if closed[j] {
				continue
			}
			step := g
			if d%2 == 1 {
				step *= 1.4142
			}
			if pd >= 0 && int(pd) != d {
				step += 0.5 * g
			}
			nd := dist[k] + step
			if nd >= dist[j] {
				continue
			}
			if !legal(xx, yy) {
				closed[j] = true
				continue
			}
			if r.escIdx != nil && !r.segClear(r.escIdx, n, l, gr.center(x, y), gr.center(xx, yy), hw) {
				continue // this step only: the cell may be reachable another way
			}
			dist[j], par[j], dirs[j] = nd, int32(k), int8(d)
			heap.Push(h, jointItem{node: j, cost: nd})
		}
	}
	return nil
}

// escLegal: cell (x,y) on layer l is free for n at claim radius rad — static
// obstacles and other nets' pads, and no other net's claim in the disk.
func (r *router) escLegal(n *rnet, l, x, y int, rad float64, own map[int32]bool) bool {
	gr := r.gr
	if !r.nodeOK(n, l, x, y, rad) {
		return false
	}
	for _, o := range gr.disk(rad) {
		xx, yy := x+o[0], y+o[1]
		if !gr.in(xx, yy) {
			return false
		}
		j := int32(gr.idx(l, xx, yy))
		if gr.use[j] > 0 && !own[j] {
			return false
		}
	}
	return true
}

// commitEscape claims the escape path of pd on layer l as fixed copper,
// emits it as tracks and makes its exit the pad's access node.
func (r *router) commitEscape(n *rnet, pd *Pad, l int, path []int32, w float64, dog bool, e [2]int) {
	gr := r.gr
	rad := w/2 + n.share
	r.claimCur++
	for _, i := range n.fixed {
		r.claimStamp[i] = r.claimCur
	}
	var fresh []int32
	pts := make([]Point, 0, len(path)+1)
	es := &bgaEsc{pd: pd, hasVia: dog}
	if dog {
		es.via = gr.center(e[0], e[1])
	} else {
		pts = append(pts, pd.Box.C)
	}
	for _, node := range path {
		_, x, y := gr.xy(int(node))
		pts = append(pts, gr.center(x, y))
		for _, o := range gr.disk(rad) {
			xx, yy := x+o[0], y+o[1]
			if !gr.in(xx, yy) {
				continue
			}
			j := int32(gr.idx(l, xx, yy))
			if r.claimStamp[j] != r.claimCur {
				r.claimStamp[j] = r.claimCur
				fresh = append(fresh, j)
			}
		}
	}
	r.applyClaims(fresh, +1)
	n.fixed = dedup(append(n.fixed, fresh...))
	pts = compress(pts)
	for k := 1; k < len(pts); k++ {
		if pts[k-1].Dist(pts[k]) > 1e-6 {
			es.tracks = append(es.tracks, Track{Net: n.name, Layer: gr.layers[l], A: pts[k-1], B: pts[k], Width: w, Kind: "escape"})
			if r.escIdx != nil {
				r.escIdx.add(escItem{n.id, l, pts[k-1], pts[k], w / 2})
			}
		}
	}
	es.claims = fresh
	es.end = path[len(path)-1]
	_, ex, ey := gr.xy(int(es.end))
	es.endPt = gr.center(ex, ey)
	n.escs = append(n.escs, es)
	r.escOf[pd] = es
}

// anchor is where a routed path meets pad pd: the escape exit when the ball
// was pre-escaped, else the pad centre.
func (r *router) anchor(pd *Pad) Point {
	if e := r.escOf[pd]; e != nil {
		return e.endPt
	}
	return pd.Box.C
}

// dropEscape removes escape k of n (its claims and tracks); the ball is
// handed back to the router through its via or pad.
func (r *router) dropEscape(n *rnet, k int) {
	es := n.escs[k]
	r.applyClaims(es.claims, -1)
	drop := map[int32]bool{}
	for _, i := range es.claims {
		drop[i] = true
	}
	var fixed []int32
	for _, i := range n.fixed {
		if !drop[i] {
			fixed = append(fixed, i)
		}
	}
	n.fixed = fixed
	delete(r.escOf, es.pd)
	n.escs = append(n.escs[:k], n.escs[k+1:]...)
}

func escTracks(n *rnet) []Track {
	var ts []Track
	for _, e := range n.escs {
		ts = append(ts, e.tracks...)
	}
	return ts
}
