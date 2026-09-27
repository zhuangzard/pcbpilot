package pcbauto

import (
	"container/heap"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Segment currents, branch widths and DC IR drop on the routed copper.
//
// With simulated per-pad currents (--sim) every power net's routed copper is
// turned into a resistor network — tracks ρL/(w·t), via barrels
// ρh/(π(d+t)t), planes and pours as a coarse resistive sheet grid — the sink
// pads draw their simulated current, the supplying part is the voltage
// reference, and the network is solved (Laplacian, conjugate gradient). The
// solve gives the current in every segment (on a tree: the sum of the pads
// downstream of it), which tapers each track to the width its own current
// needs, and the drop at every sink pad, which is checked against the budget.

const (
	rhoCu       = 1.72e-8  // Ω·m, annealed copper at 20 °C
	milToM      = 25.4e-6  // m per mil
	platingMil  = 0.7      // via barrel plating (JLC standard 18 µm)
	ozThickMil  = milPerOz // 1 oz/ft² = 1.378 mil
	planeCells  = 6000     // target cells per plane region grid
	minPlaneMil = 25.0     // finest plane grid pitch
)

// IRBudget is the allowed DC drop: max(Pct % of the rail, MV) for rails up
// to 5 V; above 5 V only the percentage applies (when set).
type IRBudget struct {
	Pct float64 `json:"pct"`
	MV  float64 `json:"mv"`
}

// DefaultIRBudget is max(2 %, 30 mV).
func DefaultIRBudget() IRBudget { return IRBudget{Pct: 2, MV: 30} }

// Volts is the budget for a rail of the given voltage.
func (b IRBudget) Volts(rail float64) float64 {
	pct := math.Abs(rail) * b.Pct / 100
	if math.Abs(rail) > 5 && b.Pct > 0 {
		return pct
	}
	return math.Max(pct, b.MV/1000)
}

func (b IRBudget) String() string {
	var p []string
	if b.Pct > 0 {
		p = append(p, strconv.FormatFloat(b.Pct, 'g', -1, 64)+"%")
	}
	if b.MV > 0 {
		p = append(p, strconv.FormatFloat(b.MV, 'g', -1, 64)+"mV")
	}
	return strings.Join(p, ",")
}

// ParseIRBudget reads "2%", "30mV", "0.05V" or a comma list of them
// ("2%,30mV" = the larger of the two).
func ParseIRBudget(s string) (IRBudget, error) {
	var b IRBudget
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(strings.ToLower(f))
		if f == "" {
			continue
		}
		var err error
		switch {
		case strings.HasSuffix(f, "%"):
			b.Pct, err = strconv.ParseFloat(strings.TrimSuffix(f, "%"), 64)
		case strings.HasSuffix(f, "mv"):
			b.MV, err = strconv.ParseFloat(strings.TrimSuffix(f, "mv"), 64)
		case strings.HasSuffix(f, "v"):
			var v float64
			v, err = strconv.ParseFloat(strings.TrimSuffix(f, "v"), 64)
			b.MV = v * 1000
		default:
			err = fmt.Errorf("want N%%, NmV or NV")
		}
		if err != nil {
			return b, fmt.Errorf("ir budget %q: %v", f, err)
		}
	}
	if b.Pct <= 0 && b.MV <= 0 {
		return b, fmt.Errorf("ir budget %q: empty", s)
	}
	return b, nil
}

// ---- resistor network --------------------------------------------------------

type resEdge struct {
	a, b  int
	g     float64 // conductance, S
	track int     // index into the net's track list, -1 otherwise
	kind  string  // track | via | plane | spread
}

type resNet struct {
	parent []int
	label  []string
	edges  []resEdge
}

func (n *resNet) node(label string) int {
	n.parent = append(n.parent, len(n.parent))
	n.label = append(n.label, label)
	return len(n.parent) - 1
}

func (n *resNet) find(i int) int {
	for n.parent[i] != i {
		n.parent[i] = n.parent[n.parent[i]]
		i = n.parent[i]
	}
	return i
}

// short joins two nodes with an ideal conductor.
func (n *resNet) short(a, b int) {
	ra, rb := n.find(a), n.find(b)
	if ra != rb {
		n.parent[rb] = ra
	}
}

// resistor adds ohm between a and b (≤ 1 nΩ is a short).
func (n *resNet) resistor(a, b int, ohm float64, track int, kind string) {
	if a == b {
		return
	}
	if ohm <= 1e-9 {
		n.short(a, b)
		return
	}
	n.edges = append(n.edges, resEdge{a: a, b: b, g: 1 / ohm, track: track, kind: kind})
}

// resSol is a solved network: drop[i] = V(ref) − V(i) per original node
// (NaN where the node has no copper path to the reference).
type resSol struct {
	net  *resNet
	drop []float64
	open []int // injection nodes without a path to the reference
}

// current is the current through e from e.a to e.b (A).
func (s *resSol) current(e resEdge) float64 {
	return (s.drop[e.b] - s.drop[e.a]) * e.g
}

// solve holds every ref node at 0 and injects inj[i] (A drawn out of the
// network at node i; negative = fed in) elsewhere.
func (n *resNet) solve(refs []int, inj map[int]float64) *resSol {
	sol := &resSol{net: n, drop: make([]float64, len(n.parent))}
	for i := range sol.drop {
		sol.drop[i] = math.NaN()
	}
	if len(refs) == 0 {
		for i := range inj {
			sol.open = append(sol.open, i)
		}
		sort.Ints(sol.open)
		return sol
	}
	for _, r := range refs[1:] {
		n.short(refs[0], r)
	}
	ref := n.find(refs[0])
	// Adjacency over roots.
	type half struct {
		to int
		g  float64
	}
	adj := map[int][]half{}
	for _, e := range n.edges {
		a, b := n.find(e.a), n.find(e.b)
		if a == b {
			continue
		}
		adj[a] = append(adj[a], half{b, e.g})
		adj[b] = append(adj[b], half{a, e.g})
	}
	// Reachable roots from the reference.
	idx := map[int]int{ref: -1}
	queue := []int{ref}
	var order []int
	for k := 0; k < len(queue); k++ {
		u := queue[k]
		for _, h := range adj[u] {
			if _, ok := idx[h.to]; !ok {
				idx[h.to] = len(order)
				order = append(order, h.to)
				queue = append(queue, h.to)
			}
		}
	}
	m := len(order)
	rhs := make([]float64, m)
	var injKeys []int
	for i := range inj {
		injKeys = append(injKeys, i)
	}
	sort.Ints(injKeys)
	for _, i := range injKeys {
		r := n.find(i)
		k, ok := idx[r]
		if !ok {
			sol.open = append(sol.open, i)
			continue
		}
		if k >= 0 {
			rhs[k] += inj[i]
		}
	}
	// Sparse rows (reference eliminated: its drop is 0).
	diag := make([]float64, m)
	cols := make([][]int, m)
	vals := make([][]float64, m)
	for k, u := range order {
		for _, h := range adj[u] {
			diag[k] += h.g
			if j := idx[h.to]; j >= 0 {
				cols[k] = append(cols[k], j)
				vals[k] = append(vals[k], -h.g)
			}
		}
	}
	x := make([]float64, m)
	if m > 0 {
		if m <= 400 {
			x = denseSolve(diag, cols, vals, rhs)
		} else {
			x = cgSolve(diag, cols, vals, rhs)
		}
	}
	rootDrop := map[int]float64{ref: 0}
	for k, u := range order {
		rootDrop[u] = x[k]
	}
	for i := range sol.drop {
		if d, ok := rootDrop[n.find(i)]; ok {
			sol.drop[i] = d
		}
	}
	return sol
}

// denseSolve is Gaussian elimination with partial pivoting (small systems).
func denseSolve(diag []float64, cols [][]int, vals [][]float64, rhs []float64) []float64 {
	m := len(diag)
	a := make([][]float64, m)
	for i := range a {
		a[i] = make([]float64, m+1)
		a[i][i] = diag[i]
		for k, j := range cols[i] {
			a[i][j] += vals[i][k]
		}
		a[i][m] = rhs[i]
	}
	for c := 0; c < m; c++ {
		p := c
		for r := c + 1; r < m; r++ {
			if math.Abs(a[r][c]) > math.Abs(a[p][c]) {
				p = r
			}
		}
		a[c], a[p] = a[p], a[c]
		if a[c][c] == 0 {
			continue
		}
		for r := c + 1; r < m; r++ {
			f := a[r][c] / a[c][c]
			if f == 0 {
				continue
			}
			for k := c; k <= m; k++ {
				a[r][k] -= f * a[c][k]
			}
		}
	}
	x := make([]float64, m)
	for i := m - 1; i >= 0; i-- {
		s := a[i][m]
		for k := i + 1; k < m; k++ {
			s -= a[i][k] * x[k]
		}
		if a[i][i] != 0 {
			x[i] = s / a[i][i]
		}
	}
	return x
}

// cgSolve is Jacobi-preconditioned conjugate gradient on the SPD reduced
// Laplacian.
func cgSolve(diag []float64, cols [][]int, vals [][]float64, rhs []float64) []float64 {
	m := len(diag)
	mul := func(v, out []float64) {
		for i := 0; i < m; i++ {
			s := diag[i] * v[i]
			for k, j := range cols[i] {
				s += vals[i][k] * v[j]
			}
			out[i] = s
		}
	}
	x := make([]float64, m)
	r := append([]float64(nil), rhs...)
	z := make([]float64, m)
	for i := range z {
		z[i] = r[i] / diag[i]
	}
	p := append([]float64(nil), z...)
	ap := make([]float64, m)
	rz := 0.0
	bn := 0.0
	for i := range r {
		rz += r[i] * z[i]
		bn += rhs[i] * rhs[i]
	}
	if bn == 0 {
		return x
	}
	for it := 0; it < 20*m+200; it++ {
		mul(p, ap)
		pap := 0.0
		for i := range p {
			pap += p[i] * ap[i]
		}
		if pap <= 0 {
			break
		}
		alpha := rz / pap
		rn := 0.0
		for i := range x {
			x[i] += alpha * p[i]
			r[i] -= alpha * ap[i]
			rn += r[i] * r[i]
		}
		if rn <= 1e-26*bn {
			break
		}
		rz2 := 0.0
		for i := range z {
			z[i] = r[i] / diag[i]
			rz2 += r[i] * z[i]
		}
		beta := rz2 / rz
		rz = rz2
		for i := range p {
			p[i] = z[i] + beta*p[i]
		}
	}
	return x
}

// ---- copper → network ------------------------------------------------------

// Physical resistances (Ω) from mil geometry.
func trackOhm(lenMil, widthMil, oz float64) float64 {
	return rhoCu * lenMil * milToM / (widthMil * milToM * oz * ozThickMil * milToM)
}

func sheetOhm(oz float64) float64 { return rhoCu / (oz * ozThickMil * milToM) }

func viaOhm(heightMil, drillMil float64) float64 {
	area := math.Pi * (drillMil + platingMil) * platingMil * milToM * milToM
	return rhoCu * heightMil * milToM / area
}

// netCopper is one net's copper prepared for the solve.
type netCopper struct {
	net    string
	rn     *resNet
	tracks []Track // the net's tracks, split at T-junctions
	origin []int   // index of each track in the route result (-1 for a split tail)
	pads   map[*Pad]int
	cells  int // plane grid nodes
}

type layerInfo struct {
	ord   map[int]int // layer id → stack index
	oz    map[int]float64
	z     map[int]float64 // depth of layer id (mil)
	ids   []int
	plane map[int]bool
}

func stackInfo(b *Board, st *Stackup) layerInfo {
	li := layerInfo{ord: map[int]int{}, oz: map[int]float64{}, z: map[int]float64{}, plane: map[int]bool{}}
	if st == nil || len(st.Stack) == 0 {
		st = &Stackup{Layers: 2, Stack: []StackLayer{{ID: LayerTop, Outer: true}, {ID: LayerBottom, Outer: true}}}
	}
	nl := len(st.Stack)
	for k, l := range st.Stack {
		li.ord[l.ID] = k
		li.ids = append(li.ids, l.ID)
		li.oz[l.ID] = b.Rules.CopperOz
		if !l.Outer {
			li.oz[l.ID] = b.Rules.InnerCopperOz
		}
		if nl > 1 {
			li.z[l.ID] = b.Rules.BoardThickMil * float64(k) / float64(nl-1)
		}
		li.plane[l.ID] = l.Kind == KindPlane
	}
	return li
}

// buildNetCopper turns net's routed copper into a resistor network.
func buildNetCopper(b *Board, st *Stackup, rr *RouteResult, net string) *netCopper {
	info := stackInfo(b, st)
	nc := &netCopper{net: net, rn: &resNet{}, pads: map[*Pad]int{}}
	rn := nc.rn
	var pads []*Pad
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net == net {
				pads = append(pads, pd)
				nc.pads[pd] = rn.node(pd.Key())
			}
		}
	}
	var trackIdx []int
	for i, t := range rr.Tracks {
		if t.Net == net && t.A.Dist(t.B) > 1e-6 {
			trackIdx = append(trackIdx, i)
		}
	}
	var vias []Via
	for _, v := range rr.Vias {
		if v.Net == net {
			vias = append(vias, v)
		}
	}
	// T-junctions: a track end (or a via) inside another track's copper away
	// from its ends splits that track, so each piece carries its own current.
	type split struct {
		at   Point
		frac float64
	}
	splits := map[int][]split{}
	probe := func(p Point, layer, self int) {
		for _, k := range trackIdx {
			if k == self {
				continue
			}
			s := rr.Tracks[k]
			if s.Layer != layer {
				continue
			}
			if PointSegDist(p, s.A, s.B) > s.Width/2+0.1 {
				continue
			}
			q := segNearest(p, s.A, s.B)
			if q.Dist(s.A) < 0.5 || q.Dist(s.B) < 0.5 {
				continue
			}
			d := s.B.Sub(s.A)
			f := ((q.X-s.A.X)*d.X + (q.Y-s.A.Y)*d.Y) / (d.X*d.X + d.Y*d.Y)
			splits[k] = append(splits[k], split{q, f})
		}
	}
	for _, k := range trackIdx {
		t := rr.Tracks[k]
		probe(t.A, t.Layer, k)
		probe(t.B, t.Layer, k)
	}
	for _, v := range vias {
		for _, id := range info.ids {
			probe(v.C, id, -1)
		}
	}
	for _, k := range trackIdx {
		t := rr.Tracks[k]
		ss := splits[k]
		sort.Slice(ss, func(i, j int) bool { return ss[i].frac < ss[j].frac })
		prev := t.A
		first := true
		for _, s := range ss {
			if s.at.Dist(prev) < 0.5 {
				continue
			}
			piece := t
			piece.A, piece.B = prev, s.at
			nc.tracks = append(nc.tracks, piece)
			if first {
				nc.origin = append(nc.origin, k)
				first = false
			} else {
				nc.origin = append(nc.origin, -1-k)
			}
			prev = s.at
		}
		piece := t
		piece.A = prev
		nc.tracks = append(nc.tracks, piece)
		if first {
			nc.origin = append(nc.origin, k)
		} else {
			nc.origin = append(nc.origin, -1-k)
		}
	}
	// Point nodes per layer, merged within 0.5 mil.
	type pkey struct {
		layer, x, y int
	}
	buckets := map[pkey][]int{}
	pts := map[int]Point{}
	ptLayer := map[int]int{}
	var ptOrder []int // creation order: map iteration would make edge order (and float sums) vary
	pointNode := func(layer int, p Point) int {
		bx, by := int(math.Floor(p.X/2)), int(math.Floor(p.Y/2))
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, id := range buckets[pkey{layer, bx + dx, by + dy}] {
					if pts[id].Dist(p) < 0.5 {
						return id
					}
				}
			}
		}
		id := rn.node(fmt.Sprintf("L%d(%.0f,%.0f)", layer, p.X, p.Y))
		buckets[pkey{layer, bx, by}] = append(buckets[pkey{layer, bx, by}], id)
		pts[id], ptLayer[id] = p, layer
		ptOrder = append(ptOrder, id)
		return id
	}
	// Tracks.
	for i, t := range nc.tracks {
		a, bn := pointNode(t.Layer, t.A), pointNode(t.Layer, t.B)
		rn.resistor(a, bn, trackOhm(t.A.Dist(t.B), t.Width, info.oz[t.Layer]), i, "track")
	}
	// Junction shorts: a track end inside another track's copper.
	for _, t := range nc.tracks {
		for _, p := range []Point{t.A, t.B} {
			id := pointNode(t.Layer, p)
			for _, s := range nc.tracks {
				if s.Layer != t.Layer || PointSegDist(p, s.A, s.B) > s.Width/2+0.1 {
					continue
				}
				q := segNearest(p, s.A, s.B)
				if q.Dist(s.A) <= q.Dist(s.B) {
					rn.short(id, pointNode(s.Layer, s.A))
				} else {
					rn.short(id, pointNode(s.Layer, s.B))
				}
			}
		}
	}
	// Pads: every track end inside the pad copper on its layer.
	for _, id := range ptOrder {
		p := pts[id]
		for _, pd := range pads {
			if pd.OnLayer(ptLayer[id]) && pd.Box.Dist(p) <= 0.01 {
				rn.short(id, nc.pads[pd])
			}
		}
	}
	// Vias: one node per layer, barrels between consecutive layers.
	viaNodes := make([]map[int]int, len(vias))
	for k, v := range vias {
		viaNodes[k] = map[int]int{}
		var prev int
		for j, lid := range info.ids {
			id := rn.node(fmt.Sprintf("via(%.0f,%.0f)L%d", v.C.X, v.C.Y, lid))
			viaNodes[k][lid] = id
			if j > 0 {
				rn.resistor(prev, id, viaOhm(math.Abs(info.z[lid]-info.z[info.ids[j-1]]), v.Drill), -1, "via")
			}
			prev = id
			// Track ends on this layer inside the via copper.
			for _, pid := range ptOrder {
				if p := pts[pid]; ptLayer[pid] == lid && p.Dist(v.C) <= v.Dia/2+0.1 {
					rn.short(pid, id)
				}
			}
			// A via inside a pad of the same net (thermal via).
			for _, pd := range pads {
				if pd.OnLayer(lid) && pd.Box.Dist(v.C) <= 0.01 {
					rn.short(id, nc.pads[pd])
				}
			}
		}
	}
	// Planes and pours: a coarse sheet grid per region.
	for _, pr := range rr.Planes {
		if pr.Net != net || len(pr.Polys) == 0 {
			continue
		}
		oz := info.oz[pr.Layer]
		if oz <= 0 {
			oz = b.Rules.InnerCopperOz
		}
		rs := sheetOhm(oz)
		bb := EmptyRect()
		area := 0.0
		for _, poly := range pr.Polys {
			bb = bb.Union(PolyBounds(poly))
			area += math.Abs(PolyArea(poly))
		}
		pitch := math.Max(minPlaneMil, math.Sqrt(math.Max(area, 1)/planeCells))
		W := int(math.Ceil(bb.W()/pitch)) + 1
		H := int(math.Ceil(bb.H()/pitch)) + 1
		cell := make([]int, W*H)
		for i := range cell {
			cell[i] = -1
		}
		center := func(x, y int) Point {
			return Point{bb.MinX + (float64(x)+0.5)*pitch, bb.MinY + (float64(y)+0.5)*pitch}
		}
		for y := 0; y < H; y++ {
			for x := 0; x < W; x++ {
				c := center(x, y)
				for _, poly := range pr.Polys {
					if PolyContains(poly, c) {
						cell[y*W+x] = rn.node(fmt.Sprintf("plane L%d(%.0f,%.0f)", pr.Layer, c.X, c.Y))
						nc.cells++
						break
					}
				}
			}
		}
		for y := 0; y < H; y++ {
			for x := 0; x < W; x++ {
				a := cell[y*W+x]
				if a < 0 {
					continue
				}
				if x+1 < W && cell[y*W+x+1] >= 0 {
					rn.resistor(a, cell[y*W+x+1], rs, -1, "plane")
				}
				if y+1 < H && cell[(y+1)*W+x] >= 0 {
					rn.resistor(a, cell[(y+1)*W+x], rs, -1, "plane")
				}
			}
		}
		nearest := func(p Point) int {
			cx, cy := int((p.X-bb.MinX)/pitch), int((p.Y-bb.MinY)/pitch)
			best, bd := -1, math.Inf(1)
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					x, y := cx+dx, cy+dy
					if x < 0 || y < 0 || x >= W || y >= H || cell[y*W+x] < 0 {
						continue
					}
					if d := center(x, y).Dist(p); d < bd {
						best, bd = cell[y*W+x], d
					}
				}
			}
			return best
		}
		// Spreading resistance from a contact of radius r into its cell.
		spread := func(r float64) float64 {
			r = math.Max(r, 1)
			return rs / (2 * math.Pi) * math.Log(math.Max(pitch/2, r+0.5)/r)
		}
		for k, v := range vias {
			if id, ok := viaNodes[k][pr.Layer]; ok {
				if c := nearest(v.C); c >= 0 {
					rn.resistor(id, c, spread(v.Dia/2), -1, "spread")
				}
			}
		}
		for _, pd := range pads {
			if pd.OnLayer(pr.Layer) {
				if c := nearest(pd.Box.C); c >= 0 {
					rn.resistor(nc.pads[pd], c, spread(math.Min(pd.Box.W, pd.Box.H)/2), -1, "spread")
				}
			}
		}
		// A pour on a routing layer merges with the net's tracks there.
		if !info.plane[pr.Layer] {
			for _, pid := range ptOrder {
				if ptLayer[pid] == pr.Layer {
					if c := nearest(pts[pid]); c >= 0 {
						rn.resistor(pid, c, spread(b.Rules.TrackWidth/2), -1, "spread")
					}
				}
			}
		}
	}
	return nc
}

// ---- per-net power integrity -------------------------------------------------

// IRPad is the solved state of one pad.
type IRPad struct {
	Pad      string  `json:"pad"`
	Dir      string  `json:"dir"` // source | sink | pass (no DC current)
	CurrentA float64 `json:"currentA"`
	DropMV   float64 `json:"dropMV"` // |V(reference) − V(pad)|; -1 = no copper path
	OK       bool    `json:"ok"`
}

// IRStep is one element of the worst path with the drop across it.
type IRStep struct {
	Kind   string  `json:"kind"` // track | via | plane | spread
	Layer  int     `json:"layer,omitempty"`
	What   string  `json:"what"`
	DropMV float64 `json:"dropMV"`
}

// IRSegment is one tapered track: routed width, width after tapering, and
// the current it carries.
type IRSegment struct {
	Layer       int     `json:"layer"`
	A           Point   `json:"a"`
	B           Point   `json:"b"`
	LengthMil   float64 `json:"lengthMil"`
	CurrentA    float64 `json:"currentA"`
	RoutedMil   float64 `json:"routedMil"`
	WidthMil    float64 `json:"widthMil"`
	Kind        string  `json:"kind,omitempty"`
	DropMV      float64 `json:"dropMV"`
	NeedMil     float64 `json:"needMil"` // width its current needs (class floor included)
	Constrained bool    `json:"constrained,omitempty"`
}

// IRNet is the power-integrity result of one net.
type IRNet struct {
	Net        string      `json:"net"`
	Role       NetRole     `json:"role"`
	VoltageV   float64     `json:"voltageV"`
	CurrentA   float64     `json:"currentA"`
	Reference  string      `json:"reference"`          // the supplying part(s), "|"-joined when several
	WorstRef   string      `json:"worstRef,omitempty"` // the supply whose case gives the worst drop
	BudgetMV   float64     `json:"budgetMV"`
	WorstMV    float64     `json:"worstMV"`
	WorstPad   string      `json:"worstPad,omitempty"`
	WorstPath  []IRStep    `json:"worstPath,omitempty"`
	Status     string      `json:"status"` // ok | over-budget | open | no-reference | info
	Pads       []IRPad     `json:"pads"`
	Segments   []IRSegment `json:"segments,omitempty"`
	Narrowed   int         `json:"narrowed"`
	Widened    int         `json:"widened"`
	PlaneCells int         `json:"planeCells,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
	// Reroute is the feedback for another routing pass when the budget is
	// missed with every segment at its routed width.
	Reroute *simBoost `json:"reroute,omitempty"`
}

// IRReport is the post-route power-integrity result.
type IRReport struct {
	Budget   IRBudget `json:"budget"`
	Scenario string   `json:"scenario"`
	Nets     []*IRNet `json:"nets"`
	Passes   int      `json:"passes"`
	Model    []string `json:"model"`
}

// Violations counts over-budget and open power nets.
func (r *IRReport) Violations() int {
	if r == nil {
		return 0
	}
	n := 0
	for _, x := range r.Nets {
		if x.Status == "over-budget" || x.Status == "open" {
			n++
		}
	}
	return n
}

// WorstRatio is the largest drop/budget over power nets (0 without data).
func (r *IRReport) WorstRatio() float64 {
	if r == nil {
		return 0
	}
	w := 0.0
	for _, x := range r.Nets {
		if x.Role == RolePower && x.BudgetMV > 0 {
			w = math.Max(w, x.WorstMV/x.BudgetMV)
		}
	}
	return w
}

// powerIntegrity tapers every simulated power net's tracks to their segment
// currents and checks the DC drop; offending tapered segments are widened
// back step by step (never beyond the routed width, so the geometry stays
// the one the router and DRC cleared). Nets still over budget at routed
// width get reroute feedback.
func powerIntegrity(b *Board, an *Analysis, st *Stackup, rr *RouteResult) *IRReport {
	if an == nil || an.Sim == nil || rr == nil {
		return nil
	}
	rep := &IRReport{Budget: an.IRBudget, Scenario: an.Sim.Scenario, Model: []string{
		"track R = ρ·L/(w·t), ρ = 1.72e-8 Ω·m, t = copper weight × 1.378 mil (outer/inner from the rules)",
		fmt.Sprintf("via R = ρ·h/(π·(d+t)·t), plating t = %.1f mil, h = layer spacing (board thickness / (layers−1))", platingMil),
		fmt.Sprintf("planes and pours = resistive sheet grid (pitch ≥ %.0f mil, ≤ %d cells) over the region outline; antipads and other nets' copper are not subtracted (optimistic)", minPlaneMil, planeCells),
		"each supply (power: source part ≥ 5 % of the largest; ground: the return entry, kind connector-source when marked) is solved as the sole reference; other pads draw (+I) or feed (−I) their simulated current; per-segment currents and per-pad drops are the maxima over these cases (a merged worst-case file does not satisfy KCL)",
		"trunk segments (carrying a case's whole load) carry at least the supplying pin's own current; capacitor pads with ripple RMS keep the width that current needs",
	}}
	info := stackInfo(b, st)
	var plans []*NetPlan
	for _, np := range an.Nets {
		if np.hasPadCurrents() && (np.Role == RolePower || np.Role == RoleGround || np.Role == RoleSwitch) {
			plans = append(plans, np)
		}
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].Net < plans[j].Net })
	for _, np := range plans {
		rep.Nets = append(rep.Nets, netIntegrity(b, an, st, rr, np, info, &rep.Passes))
	}
	return rep
}

// irCase is one supply configuration: the reference part feeds every sink
// on its own. A merged ("worst") simulation takes each pin's maximum over
// the scenarios, so Kirchhoff's current law no longer holds per net — the
// two OR-ing diodes of a 5 V input both appear as full sources. Solving
// each supplying part alone and keeping the per-segment and per-pad maxima
// is the envelope of the scenarios the file came from.
type irCase struct {
	part string
	refI float64 // the reference part's own simulated current
	refs []int
	inj  map[int]float64
	sol  *resSol
}

func netIntegrity(b *Board, an *Analysis, st *Stackup, rr *RouteResult, np *NetPlan, info layerInfo, passes *int) *IRNet {
	out := &IRNet{Net: np.Net, Role: np.Role, VoltageV: np.Voltage, CurrentA: np.CurrentA}
	if np.Role == RolePower {
		out.BudgetMV = an.IRBudget.Volts(np.Voltage) * 1000
	}
	nc := buildNetCopper(b, st, rr, np.Net)
	out.PlaneCells = nc.cells
	cases := irCases(np, nc)
	var names []string
	for _, c := range cases {
		names = append(names, c.part)
	}
	out.Reference = strings.Join(names, "|")
	if len(cases) == 0 {
		out.Status = "no-reference"
		out.Notes = append(out.Notes, "no supplying pad in the simulated currents: segment currents unknown, copper left at routed width")
		return out
	}
	if len(cases) > 1 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d supplies (%s): each solved as the sole source, per-segment and per-pad maxima kept", len(cases), out.Reference))
	}
	floor := classMinWidth(np.Role, b.Rules)
	taper := np.Role != RoleGround || !onPlaneOrPour(st, np.Net)
	if np.Role == RoleGround && !taper {
		out.Notes = append(out.Notes, "ground carried by a plane/pour: tracks keep the net width; fan-out vias sized per pad")
	}
	solveAll := func() {
		for _, c := range cases {
			c.sol = nc.rn.solve(append([]int(nil), c.refs...), c.inj)
		}
	}
	solveAll()
	routed := make([]float64, len(nc.tracks))
	for i, t := range nc.tracks {
		routed[i] = t.Width
	}
	need := make([]float64, len(nc.tracks))
	curr := make([]float64, len(nc.tracks))
	edgeIdx := map[int]int{}
	for k, e := range nc.rn.edges {
		if e.track >= 0 {
			edgeIdx[e.track] = k
		}
	}
	setWidth := func(i int, w float64) {
		t := &nc.tracks[i]
		t.Width = w
		if k, ok := edgeIdx[i]; ok {
			nc.rn.edges[k].g = 1 / trackOhm(t.A.Dist(t.B), w, info.oz[t.Layer])
		}
	}
	// Segment current: the maximum over the supply cases; a trunk segment
	// (carrying the whole load of its case) carries at least the supplying
	// pin's own simulated current — max(Σsink downstream, source current).
	for i, t := range nc.tracks {
		k, ok := edgeIdx[i]
		solved := false
		for _, c := range cases {
			if !ok || math.IsNaN(c.sol.drop[nc.rn.edges[k].a]) {
				continue
			}
			solved = true
			ia := math.Abs(c.sol.current(nc.rn.edges[k]))
			total := 0.0
			for _, v := range c.inj {
				if v > 0 {
					total += v
				}
			}
			if total > 0 && ia >= 0.999*total {
				ia = math.Max(ia, c.refI)
			}
			curr[i] = math.Max(curr[i], ia)
		}
		if !solved {
			need[i] = t.Width
			continue
		}
		need[i] = branchWidth(curr[i], an.TempRiseC, info.oz[t.Layer], floor)
		// A pad sized for its ripple current (a converter's input/output
		// capacitor) keeps that width on the copper that touches it.
		for _, pc := range np.PadCurrents {
			if pc.SizeA > pc.CurrentA && pc.pad.OnLayer(t.Layer) && (pc.pad.Box.Dist(t.A) <= 0.01 || pc.pad.Box.Dist(t.B) <= 0.01) {
				need[i] = math.Max(need[i], branchWidth(pc.SizeA, an.TempRiseC, info.oz[t.Layer], floor))
			}
		}
		if taper && need[i] < t.Width-1e-9 && t.Width > floor {
			setWidth(i, need[i])
			out.Narrowed++
		}
	}
	tapered := make([]float64, len(nc.tracks))
	for i, t := range nc.tracks {
		tapered[i] = t.Width
	}
	// IR loop: widen tapered segments on the worst path one metric step
	// (≤ routed width) until the budget holds or nothing can widen.
	step := 0.05 / 0.0254
	var worst *irCase
	for pass := 0; pass < 40; pass++ {
		solveAll()
		*passes++
		worst = fillPads(out, np, nc, cases)
		if out.Status != "over-budget" || np.Role != RolePower || worst == nil {
			break
		}
		path := worstPath(nc, worst.refs, nc.pads[padByKey(np, out.WorstPad)])
		changed := false
		for _, e := range path {
			if e.track < 0 {
				continue
			}
			i := e.track
			if nc.tracks[i].Width < routed[i]-1e-9 {
				setWidth(i, math.Min(routed[i], nc.tracks[i].Width+step))
				changed = true
			}
		}
		if !changed {
			// Everything on the worst path is at routed width: only a wider
			// re-route (or more vias) can help.
			viaMV, trackMV := 0.0, 0.0
			for _, s := range out.WorstPath {
				switch s.Kind {
				case "via":
					viaMV += s.DropMV
				case "track":
					trackMV += s.DropMV
				}
			}
			bo := &simBoost{WidthMil: metricRound(math.Max(np.WidthMil*1.5, np.WidthMil+step))}
			if viaMV > 0.25*out.WorstMV {
				bo.ExtraVia = np.ExtraVias + 1
			}
			out.Reroute = bo
			out.Notes = append(out.Notes, fmt.Sprintf("worst path at routed width (tracks %.1f mV, vias %.1f mV): re-route with %.1f mil / +%d via(s) per pad requested", trackMV, viaMV, bo.WidthMil, bo.ExtraVia))
			break
		}
	}
	for i, t := range nc.tracks {
		if t.Width > tapered[i]+1e-9 {
			out.Widened++
		}
		dmv := 0.0
		if k, ok := edgeIdx[i]; ok {
			e := nc.rn.edges[k]
			for _, c := range cases {
				if !math.IsNaN(c.sol.drop[e.a]) && !math.IsNaN(c.sol.drop[e.b]) {
					dmv = math.Max(dmv, math.Abs(c.sol.drop[e.b]-c.sol.drop[e.a])*1000)
				}
			}
		}
		out.Segments = append(out.Segments, IRSegment{Layer: t.Layer, A: t.A, B: t.B, LengthMil: t.A.Dist(t.B), CurrentA: curr[i],
			RoutedMil: routed[i], WidthMil: t.Width, Kind: t.Kind, DropMV: dmv, NeedMil: need[i], Constrained: routed[i] < floor})
	}
	writeBack(rr, nc)
	return out
}

// irCases builds the supply cases of a net. The reference side is "source"
// for power and switch nets and "sink" for ground (where the return leaves:
// the connector or supply). Supplies are the reference-side parts carrying at
// least 5 % of the largest one; when the simulation marks supply entry points
// (kind connector-source) only those are supplies and the other
// reference-side pads draw or feed their own current (a buck's low-side
// ground pin). Each supply is solved alone with every other pad injecting
// its current (drawn: +I, fed: −I); other supplies stay idle.
func irCases(np *NetPlan, nc *netCopper) []*irCase {
	refDir := "source"
	if np.Role == RoleGround {
		refDir = "sink"
	}
	partI := map[string]float64{}
	entry := map[string]bool{}
	for _, pc := range np.PadCurrents {
		if pc.Dir == refDir {
			partI[pc.pad.Part] += pc.CurrentA
			if pc.Kind == "connector-source" {
				entry[pc.pad.Part] = true
			}
		}
	}
	maxI := 0.0
	for _, v := range partI {
		maxI = math.Max(maxI, v)
	}
	var supplies []string
	for p, v := range partI {
		if v <= 0 || v < 0.05*maxI || len(entry) > 0 && !entry[p] {
			continue
		}
		supplies = append(supplies, p)
	}
	sort.Slice(supplies, func(i, j int) bool {
		if partI[supplies[i]] != partI[supplies[j]] {
			return partI[supplies[i]] > partI[supplies[j]]
		}
		return supplies[i] < supplies[j]
	})
	// One scenario's currents satisfy KCL: the supplies share the load as
	// simulated — the largest is the reference, the others feed their own
	// current in. Only a merged (non-KCL) file needs the per-supply envelope.
	src, snk := 0.0, 0.0
	for _, pc := range np.PadCurrents {
		switch pc.Dir {
		case "source":
			src += pc.CurrentA
		case "sink":
			snk += pc.CurrentA
		}
	}
	if len(supplies) > 1 && math.Abs(src-snk) <= 0.02*math.Max(src, snk) {
		supplies = supplies[:1]
	}
	isSupply := map[string]bool{}
	for _, p := range supplies {
		isSupply[p] = true
	}
	var cases []*irCase
	for _, sp := range supplies {
		c := &irCase{part: sp, refI: partI[sp], inj: map[int]float64{}}
		for _, pc := range np.PadCurrents {
			id, ok := nc.pads[pc.pad]
			if !ok {
				continue
			}
			switch {
			case pc.Dir == refDir && pc.pad.Part == sp:
				c.refs = append(c.refs, id)
			case pc.Dir == refDir && isSupply[pc.pad.Part]:
				// another supply: idle in this case
			case pc.Dir == "sink":
				c.inj[id] += pc.CurrentA
			case pc.Dir == "source":
				c.inj[id] -= pc.CurrentA
			}
		}
		if len(c.refs) > 0 {
			cases = append(cases, c)
		}
	}
	return cases
}

func onPlaneOrPour(st *Stackup, net string) bool {
	if st == nil {
		return false
	}
	for _, l := range st.Stack {
		for _, n := range l.Nets {
			if n == net {
				return true
			}
		}
		for _, n := range l.PourNets {
			if n == net {
				return true
			}
		}
	}
	return false
}

func padByKey(np *NetPlan, key string) *Pad {
	for _, pc := range np.PadCurrents {
		if pc.Pad == key {
			return pc.pad
		}
	}
	return nil
}

// fillPads records every pad's drop (the maximum over the supply cases), the
// worst one and the status, and returns the case that produced it.
func fillPads(out *IRNet, np *NetPlan, nc *netCopper, cases []*irCase) *irCase {
	out.Pads = out.Pads[:0]
	out.WorstMV, out.WorstPad, out.Status = 0, "", "ok"
	var worst *irCase
	open := false
	for _, pc := range np.PadCurrents {
		p := IRPad{Pad: pc.Pad, Dir: pc.Dir, CurrentA: pc.CurrentA, DropMV: -1}
		id, ok := nc.pads[pc.pad]
		for _, c := range cases {
			if !ok {
				break
			}
			if d := c.sol.drop[id]; !math.IsNaN(d) && math.Abs(d)*1000 > p.DropMV {
				p.DropMV = math.Abs(d) * 1000
				if p.DropMV > out.WorstMV {
					out.WorstMV, out.WorstPad, worst = p.DropMV, p.Pad, c
				}
			}
		}
		if p.DropMV < 0 {
			if pc.CurrentA > 0 {
				open = true
			}
		} else {
			p.OK = out.BudgetMV <= 0 || p.DropMV <= out.BudgetMV
		}
		out.Pads = append(out.Pads, p)
	}
	switch {
	case open:
		out.Status = "open"
	case out.BudgetMV > 0 && out.WorstMV > out.BudgetMV:
		out.Status = "over-budget"
	case out.BudgetMV <= 0:
		out.Status = "info"
	}
	out.WorstPath, out.WorstRef = nil, ""
	if worst == nil {
		return nil
	}
	out.WorstRef = worst.part
	sol := worst.sol
	for _, e := range worstPath(nc, worst.refs, nc.pads[padByKey(np, out.WorstPad)]) {
		st := IRStep{Kind: e.kind, DropMV: math.Abs(sol.drop[e.b]-sol.drop[e.a]) * 1000}
		switch e.kind {
		case "track":
			t := nc.tracks[e.track]
			st.Layer = t.Layer
			st.What = fmt.Sprintf("%.1f mil × %.0f mil (%s)", t.Width, t.A.Dist(t.B), t.Kind)
		default:
			st.What = nc.rn.label[e.a] + " → " + nc.rn.label[e.b]
		}
		// Plane cells (with their contact spreading) merge into one sheet
		// step; the layer hops of one via merge into one via step.
		if st.Kind == "spread" {
			st.Kind = "plane"
		}
		if st.Kind == "plane" {
			st.What = "sheet"
		}
		if st.Kind == "via" {
			st.What = viaName(nc.rn.label[e.a])
		}
		if n := len(out.WorstPath); n > 0 && out.WorstPath[n-1].Kind == st.Kind && st.Kind != "track" && out.WorstPath[n-1].What == st.What {
			out.WorstPath[n-1].DropMV += st.DropMV
			continue
		}
		out.WorstPath = append(out.WorstPath, st)
	}
	return worst
}

// viaName strips the layer from a via node label ("via(x,y)L15" → "via(x,y)").
func viaName(label string) string {
	if i := strings.LastIndex(label, ")"); i >= 0 {
		return label[:i+1]
	}
	return label
}

// worstPath is the least-resistance copper path from the reference to pad
// node to, as the network edges in order (drops telescope to the pad's).
func worstPath(nc *netCopper, refs []int, to int) []resEdge {
	rn := nc.rn
	if len(refs) == 0 || to < 0 {
		return nil
	}
	ref := rn.find(refs[0])
	type half struct {
		to int
		e  int
	}
	adj := map[int][]half{}
	for k, e := range rn.edges {
		a, b := rn.find(e.a), rn.find(e.b)
		if a == b {
			continue
		}
		adj[a] = append(adj[a], half{b, k})
		adj[b] = append(adj[b], half{a, k})
	}
	dist := map[int]float64{ref: 0}
	prev := map[int]int{}
	pq := &floatHeap{{ref, 0}}
	target := rn.find(to)
	for pq.Len() > 0 {
		it := heap.Pop(pq).(fitem)
		if it.d > dist[it.n] {
			continue
		}
		if it.n == target {
			break
		}
		for _, h := range adj[it.n] {
			nd := it.d + 1/rn.edges[h.e].g
			if d, ok := dist[h.to]; !ok || nd < d {
				dist[h.to] = nd
				prev[h.to] = h.e
				heap.Push(pq, fitem{h.to, nd})
			}
		}
	}
	if _, ok := dist[target]; !ok {
		return nil
	}
	var path []resEdge
	for u := target; u != ref; {
		k := prev[u]
		e := rn.edges[k]
		// Orient from the reference side.
		if rn.find(e.b) != u {
			e.a, e.b = e.b, e.a
		}
		path = append(path, e)
		u = rn.find(e.a)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

type fitem struct {
	n int
	d float64
}
type floatHeap []fitem

func (h floatHeap) Len() int           { return len(h) }
func (h floatHeap) Less(i, j int) bool { return h[i].d < h[j].d || h[i].d == h[j].d && h[i].n < h[j].n }
func (h floatHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *floatHeap) Push(x any)        { *h = append(*h, x.(fitem)) }
func (h *floatHeap) Pop() any          { o := *h; x := o[len(o)-1]; *h = o[:len(o)-1]; return x }

// writeBack replaces the net's tracks in rr by the (split, tapered) pieces.
// A track that was split but kept one width for all pieces is written back
// whole, so an untapered net's copper is unchanged.
func writeBack(rr *RouteResult, nc *netCopper) {
	pieces := map[int][]Track{}
	for i, t := range nc.tracks {
		k := nc.origin[i]
		if k < 0 {
			k = -1 - k
		}
		pieces[k] = append(pieces[k], t)
	}
	var out []Track
	for i, t := range rr.Tracks {
		ps, ok := pieces[i]
		if !ok {
			out = append(out, t)
			continue
		}
		same := true
		for _, p := range ps {
			if p.Width != t.Width {
				same = false
			}
		}
		if same {
			out = append(out, t)
			continue
		}
		// Merge consecutive equal-width pieces.
		cur := ps[0]
		for _, p := range ps[1:] {
			if p.Width == cur.Width {
				cur.B = p.B
				continue
			}
			out = append(out, cur)
			cur = p
		}
		out = append(out, cur)
	}
	rr.Tracks = out
}
