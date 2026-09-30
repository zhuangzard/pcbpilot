package pcbauto

import (
	"math"
	"sort"
)

// Via arrays at layer transitions. A routed power net whose current needs
// k vias per layer change (NetPlan.Via, SizeVias) gets k−1 extra vias of its
// own via size clustered around the transition point P: each extra via Q
// sits on the net's copper of one layer or is tied to P by a stub of the
// net's width on that layer, so the k barrels carry the current in
// parallel. Sites are vetted strictly on the grid (no other net's claim, no
// hole clash); an array via the exact DRC rejects is dropped on its own
// (never the net's routing) and its site is not tried again.

// viaCount is the vias per layer transition of n (≥ 1).
func (n *rnet) viaCount() int {
	if n.viaK > 0 {
		return n.viaK // an alternative size chosen by completeViaArrays
	}
	if n.plan == nil || n.plan.ViasPerTransition < 1 {
		return 1
	}
	return n.plan.ViasPerTransition
}

// viaArray adds up to extra array vias (and their stubs) around the
// transition between runs a and b at node, appending to o and to claims.
func (r *router) viaArray(n *rnet, a, b run, node int32, extra int, o *netOut, claims []int32) []int32 {
	gr := r.gr
	_, px, py := gr.xy(int(node))
	P := gr.center(px, py)
	pitch := viaPitch(ViaSize{n.viaDrill, n.viaDia}, r.b.Rules.Clearance, r.b.Rules.HoleGap)
	width := func(li int) float64 {
		if !r.st.Stack[li].Outer && n.plan.InnerWidthMil > 0 {
			return n.plan.InnerWidthMil
		}
		return n.width
	}
	onRun := func(q Point, rn run) bool {
		w := width(rn.layer)
		for k := 1; k < len(rn.pts); k++ {
			if PointSegDist(q, rn.pts[k-1], rn.pts[k]) <= w/2-n.viaDia/4 {
				return true
			}
		}
		return false
	}
	near := func(q Point, rn run) float64 {
		d := math.Inf(1)
		for k := 1; k < len(rn.pts); k++ {
			d = math.Min(d, PointSegDist(q, rn.pts[k-1], rn.pts[k]))
		}
		if len(rn.pts) == 1 {
			d = q.Dist(rn.pts[0])
		}
		return d
	}
	span := math.Max(math.Max(n.width, width(a.layer)), viaTransitionSpanMil)/2 + pitch
	rc := int(math.Ceil(span / gr.g))
	type cand struct {
		x, y int
		c    Point
		s    float64
	}
	var cs []cand
	for dy := -rc; dy <= rc; dy++ {
		for dx := -rc; dx <= rc; dx++ {
			x, y := px+dx, py+dy
			if !gr.in(x, y) {
				continue
			}
			c := gr.center(x, y)
			d := c.Dist(P)
			if d < pitch-1e-6 || d > span {
				continue
			}
			cs = append(cs, cand{x, y, c, math.Min(near(c, a), near(c, b)) + 0.3*d})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].s != cs[j].s {
			return cs[i].s < cs[j].s
		}
		return cs[i].y*gr.W+cs[i].x < cs[j].y*gr.W+cs[j].x
	})
	placed := []Point{P}
	for _, c := range cs {
		if extra == 0 {
			break
		}
		if n.arrayBad[c.c] {
			continue
		}
		ok := true
		for _, q := range placed {
			if q.Dist(c.c) < pitch-1e-6 {
				ok = false
				break
			}
		}
		if !ok || math.IsInf(r.viaCostR(n, c.x, c.y, n.viaR), 1) || r.holeClash(c.c, n.viaDrill) {
			continue
		}
		for l := range gr.layers {
			if gr.routable[l] {
				if occ, _ := r.nodeCong(l, c.x, c.y, n.viaR); occ > 0 {
					ok = false
					break
				}
			}
		}
		if !ok {
			continue
		}
		var stubs []Track
		for _, rn := range []run{a, b} {
			if onRun(c.c, rn) {
				continue
			}
			w := width(rn.layer)
			if !r.segmentOK(n, rn.layer, c.c, P, w, true) {
				ok = false
				break
			}
			stubs = append(stubs, Track{Net: n.name, Layer: gr.layers[rn.layer], A: c.c, B: P, Width: w, Kind: "array"})
		}
		if !ok {
			continue
		}
		for _, t := range stubs {
			o.tracks = append(o.tracks, t)
			claims = r.claimSegment(n, gr.layerIndex(t.Layer), t.A, t.B, t.Width, claims)
		}
		o.vias = append(o.vias, Via{Net: n.name, C: c.c, Drill: n.viaDrill, Dia: n.viaDia, Kind: "array"})
		claims = r.claimVia(n, int32(gr.idx(0, c.x, c.y)), claims)
		placed = append(placed, c.c)
		extra--
	}
	if extra > 0 {
		n.shortAt = append(n.shortAt, P)
	}
	n.viaShort += extra
	return claims
}

// dropArrayAt removes the array via (with its stubs) that is a party of a
// DRC violation v over the collected copper ts/vs, and remembers the site.
func (r *router) dropArrayAt(v Violation, ts []Track, vs []Via, outs map[*rnet]*netOut) bool {
	for _, ref := range []drcRef{v.ra, v.rb} {
		var at Point
		var net string
		switch {
		case ref.kind == 2 && ref.idx < len(vs) && vs[ref.idx].Kind == "array":
			at, net = vs[ref.idx].C, vs[ref.idx].Net
		case ref.kind == 1 && ref.idx < len(ts) && ts[ref.idx].Kind == "array":
			at, net = ts[ref.idx].A, ts[ref.idx].Net
		default:
			continue
		}
		n := r.byName[net]
		if n == nil || outs[n] == nil {
			continue
		}
		o := outs[n]
		vias := o.vias[:0]
		for _, x := range o.vias {
			if !(x.Kind == "array" && x.C == at) {
				vias = append(vias, x)
			}
		}
		tracks := o.tracks[:0]
		for _, t := range o.tracks {
			if !(t.Kind == "array" && t.A == at) {
				tracks = append(tracks, t)
			}
		}
		o.vias, o.tracks = vias, tracks
		if n.arrayBad == nil {
			n.arrayBad = map[Point]bool{}
		}
		n.arrayBad[at] = true
		n.viaShort++
		n.shortAt = append(n.shortAt, at)
		return true
	}
	return false
}
