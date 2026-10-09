package postsim

import (
	"math"
	"sort"
)

// DC resistive network of one net on the real copper.
//
//   - tracks (lines and flattened arcs): 1-D resistors R = ρ·L/(w·t), split
//     at T-junctions, vias and pad centres and into pieces no longer than a
//     cell (so their heat lands where it is made);
//   - area copper (poured fills, static fills, negative planes, unpoured pour
//     outlines carved around other nets): a sheet of cells per layer, link
//     G = (t/ρ)·min(harmonic mean of the two cells' coverage, fraction of the
//     shared edge where both sides are copper);
//   - pads: one node, shorted to every sheet cell they overlap on their
//     layer(s); a through-hole pad is one node on every layer;
//   - vias: one node per stack layer, barrel R = ρ·h/(π·(d+t)·t) between
//     consecutive layers (h = layer-centre distance, d = drill, t = plating);
//     each layer node is shorted to the sheet it lands in and to the track
//     ends inside its ring.
//
// Ideal connections are merged with union-find; the reduced Laplacian is
// solved by Jacobi-preconditioned CG with the reference pads held at 0.

const (
	kindTrack = iota
	kindSheet
	kindVia
)

type eEdge struct {
	a, b  int
	g     float64
	kind  int
	k     int // stack index (sheet / track); upper layer for a via barrel
	cellA int // sheet: cells joined; track: cell of the midpoint
	cellB int
	idx   int // segment index (track) or via index (via)
	dir   int // sheet: 0 = +x, 1 = +y
}

type eSeg struct {
	K     int
	A, B  Point
	W     float64 // mil
	Edge  int
	Track int // index into eNet.tracks (the original track)
}

type eNet struct {
	net    string
	id     int16
	uf     unionFind
	edges  []eEdge
	segs   []eSeg
	cell   map[int]int // k*NC + c → node
	pad    map[*Pad]int
	pads   []*Pad
	vias   []*Via
	viaN   [][]int   // per via, per stack layer
	sheetG []float64 // per stack layer: t/ρ (S per square)
	cov    [][]float64
	nc     int
	tracks []Track // the net's original tracks (lines, arc pieces, spokes)
}

func (n *eNet) node() int { return n.uf.add() }

func (n *eNet) resistor(a, b int, g float64, e eEdge) {
	if math.IsInf(g, 1) || g > 1e9 {
		n.uf.union(a, b)
		return
	}
	e.a, e.b, e.g = a, b, g
	n.edges = append(n.edges, e)
}

// buildNet assembles net's network. platingMm is the via barrel plating.
func buildNet(b *Board, g *Grid, st *Stackup, net string, platingMm float64) *eNet {
	id, ok := g.netIdx[net]
	n := &eNet{net: net, id: id, cell: map[int]int{}, pad: map[*Pad]int{}, nc: g.cells()}
	if !ok {
		return n
	}
	nl := len(st.Layers)
	n.sheetG = make([]float64, nl)
	n.cov = make([][]float64, nl)
	for k := range st.Layers {
		n.sheetG[k] = st.cuM(k) / RhoCu
	}
	// 1. Sheets.
	for k := 0; k < nl; k++ {
		cov := g.coverage(k, id, true)
		n.cov[k] = cov
		for c, v := range cov {
			if v > 0 {
				n.cell[k*n.nc+c] = n.node()
			}
		}
		for c, v := range cov {
			if v <= 0 {
				continue
			}
			ix, iy := c%g.NX, c/g.NX
			for dir := 0; dir < 2; dir++ {
				var c2 int
				if dir == 0 {
					if ix+1 >= g.NX {
						continue
					}
					c2 = c + 1
				} else {
					if iy+1 >= g.NY {
						continue
					}
					c2 = c + g.NX
				}
				v2 := cov[c2]
				if v2 <= 0 {
					continue
				}
				fb := g.boundaryShare(k, c, dir, id)
				if fb <= 0 {
					continue
				}
				harm := 2 / (1/v + 1/v2)
				n.resistor(n.cell[k*n.nc+c], n.cell[k*n.nc+c2], n.sheetG[k]*math.Min(harm, fb),
					eEdge{kind: kindSheet, k: k, cellA: c, cellB: c2, dir: dir})
			}
		}
	}
	sheetAt := func(k int, p Point) (int, bool) {
		si := g.subAt(p)
		if si < 0 || g.area[k][si] != id {
			return 0, false
		}
		nd, ok := n.cell[k*n.nc+g.cellOfSub(si)]
		return nd, ok
	}
	// 2. Pads.
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net != net {
				continue
			}
			nd := n.node()
			n.pad[pd] = nd
			n.pads = append(n.pads, pd)
			for k, l := range st.Layers {
				if !pd.OnLayer(l.ID) {
					continue
				}
				// Grown by 1.5 sub-samples: a pour joined by thermal-relief
				// spokes touches the pad edge but not its interior.
				g.paintPad(pd, 1.5*g.subPitch(), func(si int) {
					if g.area[k][si] == id {
						if cn, ok := n.cell[k*n.nc+g.cellOfSub(si)]; ok {
							n.uf.union(nd, cn)
						}
					}
				})
			}
		}
	}
	// 3. Vias.
	for i := range b.Vias {
		v := &b.Vias[i]
		if v.Net != net {
			continue
		}
		vi := len(n.vias)
		n.vias = append(n.vias, v)
		nodes := make([]int, nl)
		for k := range nodes {
			nodes[k] = n.node()
		}
		n.viaN = append(n.viaN, nodes)
		d, t := v.Drill*MilMm*1e-3, platingMm*1e-3
		area := math.Pi * (d + t) * t
		for k := 0; k+1 < nl; k++ {
			r := RhoCu * st.spanM(k, k+1) / area
			n.resistor(nodes[k], nodes[k+1], 1/r, eEdge{kind: kindVia, k: k, idx: vi, cellA: g.cellAt(v.C)})
		}
		probe := []Point{v.C}
		for _, ring := range []float64{v.Dia / 2 * 0.7, v.Dia/2 + 1.5*g.subPitch()} {
			for a := 0; a < 8; a++ {
				th := float64(a) * math.Pi / 4
				probe = append(probe, Point{v.C.X + ring*math.Cos(th), v.C.Y + ring*math.Sin(th)})
			}
		}
		for k, l := range st.Layers {
			for _, q := range probe {
				if cn, ok := sheetAt(k, q); ok {
					n.uf.union(nodes[k], cn)
					break
				}
			}
			for _, pd := range n.pads {
				if pd.OnLayer(l.ID) && pd.contains(v.C, 0) {
					n.uf.union(nodes[k], n.pad[pd])
				}
			}
		}
	}
	// 4. Tracks.
	type tr struct {
		t      Track
		k      int
		params []float64
		nodes  []int // node at each param (after split)
	}
	var trs []*tr
	for _, t := range b.Tracks {
		if t.Net != net {
			continue
		}
		k := st.index(t.Layer)
		if k < 0 || dist(t.A, t.B) < 1e-6 {
			continue
		}
		trs = append(trs, &tr{t: t, k: k, params: []float64{0, 1}})
		n.tracks = append(n.tracks, t)
	}
	for _, x := range trs {
		a, bb, w := x.t.A, x.t.B, x.t.W
		addP := func(p Point, reach float64) {
			if segDist(p, a, bb) <= reach {
				if u := segParam(p, a, bb); u > 1e-6 && u < 1-1e-6 {
					x.params = append(x.params, u)
				}
			}
		}
		for _, y := range trs {
			if y == x || y.k != x.k {
				continue
			}
			addP(y.t.A, w/2+0.1)
			addP(y.t.B, w/2+0.1)
		}
		// Where the track enters/leaves a same-net pad or via ring: the part
		// inside runs in parallel with much wider copper and is shorted.
		addCross := func(in func(Point) bool) {
			const n = 64
			prev := in(a)
			for i := 1; i <= n; i++ {
				u := float64(i) / n
				cur := in(lerp(a, bb, u))
				if cur != prev {
					lo, hi := float64(i-1)/n, u
					for it := 0; it < 30; it++ {
						mid := (lo + hi) / 2
						if in(lerp(a, bb, mid)) == prev {
							lo = mid
						} else {
							hi = mid
						}
					}
					if t := (lo + hi) / 2; t > 1e-6 && t < 1-1e-6 {
						x.params = append(x.params, t)
					}
				}
				prev = cur
			}
		}
		for _, v := range n.vias {
			addP(v.C, math.Max(w/2, v.Dia/2))
			if segDist(v.C, a, bb) <= v.Dia/2 {
				c, r := v.C, v.Dia/2
				addCross(func(p Point) bool { return dist(p, c) <= r })
			}
		}
		for _, pd := range n.pads {
			if pd.OnLayer(st.Layers[x.k].ID) {
				addP(pd.C, w/2)
				if pb := pd.bounds(); segDist(pd.C, a, bb) <= math.Hypot(pb.MaxX-pb.MinX, pb.MaxY-pb.MinY)/2 {
					addCross(func(p Point) bool { return pd.contains(p, 0) })
				}
			}
		}
		sort.Float64s(x.params)
		// dedupe and subdivide to ≤ one cell per piece
		L := dist(a, bb)
		var ps []float64
		for i, u := range x.params {
			if i > 0 && (u-ps[len(ps)-1])*L < 0.01 {
				continue
			}
			if i > 0 {
				prev := ps[len(ps)-1]
				m := int(math.Ceil((u - prev) * L / g.Cell))
				for j := 1; j < m; j++ {
					ps = append(ps, prev+(u-prev)*float64(j)/float64(m))
				}
			}
			ps = append(ps, u)
		}
		if ps[len(ps)-1] < 1 {
			ps[len(ps)-1] = 1
		}
		x.params = ps
	}
	type key struct {
		k    int
		x, y int64
	}
	nodeAt := map[key]int{}
	type npt struct {
		nd int
		k  int
		p  Point
		w  float64
	}
	var pts []npt
	for ti, x := range trs {
		x.nodes = make([]int, len(x.params))
		for i, u := range x.params {
			p := lerp(x.t.A, x.t.B, u)
			kk := key{x.k, int64(math.Round(p.X * 20)), int64(math.Round(p.Y * 20))}
			nd, ok := nodeAt[kk]
			if !ok {
				nd = n.node()
				nodeAt[kk] = nd
				pts = append(pts, npt{nd, x.k, p, x.t.W})
			}
			x.nodes[i] = nd
		}
		tM := st.cuM(x.k)
		wM := x.t.W * MilMm * 1e-3
		for i := 0; i+1 < len(x.params); i++ {
			pa, pb := lerp(x.t.A, x.t.B, x.params[i]), lerp(x.t.A, x.t.B, x.params[i+1])
			lm := dist(pa, pb) * MilMm * 1e-3
			if lm < 1e-9 {
				n.uf.union(x.nodes[i], x.nodes[i+1])
				continue
			}
			si := len(n.segs)
			n.segs = append(n.segs, eSeg{K: x.k, A: pa, B: pb, W: x.t.W, Track: ti})
			n.resistor(x.nodes[i], x.nodes[i+1], wM*tM/(RhoCu*lm),
				eEdge{kind: kindTrack, k: x.k, idx: si, cellA: g.cellAt(lerp(pa, pb, 0.5))})
			n.segs[si].Edge = len(n.edges) - 1
		}
	}
	// Track ends that land on another track's body.
	for _, x := range trs {
		for _, e := range []struct {
			p  Point
			nd int
		}{{x.t.A, x.nodes[0]}, {x.t.B, x.nodes[len(x.nodes)-1]}} {
			for _, y := range trs {
				if y == x || y.k != x.k || segDist(e.p, y.t.A, y.t.B) > y.t.W/2+0.1 {
					continue
				}
				u := segParam(e.p, y.t.A, y.t.B)
				best, bd := -1, math.Inf(1)
				for i, v := range y.params {
					if d := math.Abs(v - u); d < bd {
						best, bd = i, d
					}
				}
				if best >= 0 {
					n.uf.union(e.nd, y.nodes[best])
				}
			}
		}
	}
	// Exact geometric overlap (KiCad's connectivity rule: copper that touches
	// is connected) seeds the union before the raster probes: a via ring that
	// overlaps a pad edge by a fraction of a mil, a track end cap on a pad or
	// on another track's edge. The raster (≥ ¼ cell sub-samples) and the
	// centre tests above miss such overlaps and report a false open.
	near := func(x *tr, p Point) int {
		u := segParam(p, x.t.A, x.t.B)
		best, bd := 0, math.Inf(1)
		for i, v := range x.params {
			if d := math.Abs(v - u); d < bd {
				best, bd = i, d
			}
		}
		return x.nodes[best]
	}
	for vi, v := range n.vias {
		for k, l := range st.Layers {
			for _, pd := range n.pads {
				if pd.OnLayer(l.ID) && pd.dist(v.C) <= v.Dia/2+touchEps {
					n.uf.union(n.viaN[vi][k], n.pad[pd])
				}
			}
		}
	}
	for xi, x := range trs {
		a, bb, w := x.t.A, x.t.B, x.t.W
		lid := st.Layers[x.k].ID
		for _, pd := range n.pads {
			if pd.OnLayer(lid) && pd.segDist(a, bb) <= w/2+touchEps {
				n.uf.union(near(x, pd.closestOn(a, bb)), n.pad[pd])
			}
		}
		for vi, v := range n.vias {
			if segDist(v.C, a, bb) <= w/2+v.Dia/2+touchEps {
				n.uf.union(near(x, v.C), n.viaN[vi][x.k])
			}
		}
		for _, y := range trs[xi+1:] {
			if y.k != x.k || segSegDist(a, bb, y.t.A, y.t.B) > (w+y.t.W)/2+touchEps {
				continue
			}
			pa, pb := closestPair(a, bb, y.t.A, y.t.B)
			n.uf.union(near(x, pa), near(y, pb))
		}
	}
	for _, q := range pts {
		lid := st.Layers[q.k].ID
		for _, pd := range n.pads {
			if pd.OnLayer(lid) && pd.contains(q.p, 0.1) {
				n.uf.union(q.nd, n.pad[pd])
			}
		}
		for vi, v := range n.vias {
			if dist(q.p, v.C) <= v.Dia/2+0.1 {
				n.uf.union(q.nd, n.viaN[vi][q.k])
			}
		}
		// The track's own copper reaches W/2 around the node (a spoke ends
		// on the pour boundary): probe the centre, then rings out to W/2 + 1.5 sub.
		if cn, ok := sheetAt(q.k, q.p); ok {
			n.uf.union(q.nd, cn)
			continue
		}
		for _, ring := range []float64{q.w / 4, q.w/2 + 0.5*g.subPitch(), q.w/2 + 1.5*g.subPitch()} {
			hit := false
			for a := 0; a < 8 && !hit; a++ {
				th := float64(a) * math.Pi / 4
				if cn, ok := sheetAt(q.k, Point{q.p.X + ring*math.Cos(th), q.p.Y + ring*math.Sin(th)}); ok {
					n.uf.union(q.nd, cn)
					hit = true
				}
			}
			if hit {
				break
			}
		}
	}
	return n
}

// eSolution holds node drops φ = V(reference) − V(node) in volts (NaN where
// the node has no copper path to the reference).
type eSolution struct {
	phi  []float64 // per node
	iter int
	res  float64
}

// solve holds refs at 0 and draws inj[node] amps out of the network
// (negative = fed in).
func (n *eNet) solve(refs []int, inj map[int]float64) *eSolution {
	nn := len(n.uf)
	sol := &eSolution{phi: make([]float64, nn)}
	for i := range sol.phi {
		sol.phi[i] = math.NaN()
	}
	if len(refs) == 0 {
		return sol
	}
	uf := append(unionFind(nil), n.uf...)
	for _, r := range refs[1:] {
		uf.union(refs[0], r)
	}
	ref := uf.find(refs[0])
	adj := map[int][]int{}
	for ei, e := range n.edges {
		a, b := uf.find(e.a), uf.find(e.b)
		if a == b {
			continue
		}
		adj[a] = append(adj[a], ei)
		adj[b] = append(adj[b], ei)
	}
	idx := map[int]int{ref: -1}
	queue := []int{ref}
	var order []int
	for q := 0; q < len(queue); q++ {
		u := queue[q]
		for _, ei := range adj[u] {
			e := n.edges[ei]
			v := uf.find(e.a)
			if v == u {
				v = uf.find(e.b)
			}
			if _, ok := idx[v]; !ok {
				idx[v] = len(order)
				order = append(order, v)
				queue = append(queue, v)
			}
		}
	}
	lap := newLaplacian(len(order))
	for _, e := range n.edges {
		a, b := uf.find(e.a), uf.find(e.b)
		ia, oka := idx[a]
		ib, okb := idx[b]
		if a == b || !oka || !okb {
			continue
		}
		switch {
		case ia >= 0 && ib >= 0:
			lap.link(ia, ib, e.g)
		case ia >= 0:
			lap.ground(ia, e.g)
		case ib >= 0:
			lap.ground(ib, e.g)
		}
	}
	rhs := make([]float64, len(order))
	for nd, i := range inj {
		if j, ok := idx[uf.find(nd)]; ok && j >= 0 {
			rhs[j] += i
		}
	}
	var x []float64
	if len(order) > 0 {
		x, sol.iter, sol.res = lap.compress().solve(rhs, 1e-11, 20*len(order)+2000)
	}
	for i := 0; i < nn; i++ {
		r := uf.find(i)
		j, ok := idx[r]
		switch {
		case !ok:
		case j < 0:
			sol.phi[i] = 0
		default:
			sol.phi[i] = x[j]
		}
	}
	return sol
}

// current is the real current through e from node a to node b (A).
func (s *eSolution) current(e eEdge) float64 {
	pa, pb := s.phi[e.a], s.phi[e.b]
	if math.IsNaN(pa) || math.IsNaN(pb) {
		return 0
	}
	return e.g * (pb - pa)
}
