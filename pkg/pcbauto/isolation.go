package pcbauto

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

// Isolation between voltage domains (intent pairs): the territory field the
// router fences each domain's copper with, milled slots under bridge parts,
// the no-pour moat regions, and the creepage / clearance check of routed
// copper that credits slots.

// ---- territory field ----------------------------------------------------------

// isoFieldCell is the resolution of the territory field (mil). Distances
// are taken one cell short (conservative).
const isoFieldCell = 5.0

// isoField labels the board into domain territories (nearest pad of a
// domain that takes part in an insulation pair) and holds, per domain, the
// distance of every cell to that domain's territory.
type isoField struct {
	ox, oy, g float64
	W, H      int
	doms      []string
	di        map[string]int
	label     []int8
	distT     [][]float32 // per domain: distance (mil) to its territory
	partners  [][]isoPartner
	// pads / pinched: per domain, its pads and those the territory fence
	// does not keep the pair distance from (router only, setupIsoPads).
	pads, pinched [][]*Pad
}

type isoPartner struct {
	f            int
	clear, creep float64 // mil
	slotW        float64 // the pair's slot width: narrower cutouts do not break its creepage
}

// buildIsoField computes the territories of the paired domains over the
// board bounds. nil when no pair has copper on both sides.
func buildIsoField(b *Board, iso *IsoRules, cell float64) *isoField {
	if iso == nil || len(iso.Pairs) == 0 {
		return nil
	}
	f := &isoField{g: cell, di: map[string]int{}}
	for _, p := range iso.Pairs {
		for _, d := range []string{p.A, p.B} {
			if _, ok := f.di[d]; !ok {
				f.di[d] = len(f.doms)
				f.doms = append(f.doms, d)
			}
		}
	}
	bb := b.Bounds().Expand(4 * cell)
	f.ox, f.oy = bb.MinX, bb.MinY
	f.W, f.H = int(math.Ceil(bb.W()/cell))+1, int(math.Ceil(bb.H()/cell))+1
	if f.W*f.H > 8_000_000 {
		f.g = math.Sqrt(bb.W() * bb.H() / 8_000_000)
		f.W, f.H = int(math.Ceil(bb.W()/f.g))+1, int(math.Ceil(bb.H()/f.g))+1
	}
	n := f.W * f.H
	seeds := make([][]bool, len(f.doms))
	has := make([]bool, len(f.doms))
	for i := range seeds {
		seeds[i] = make([]bool, n)
	}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			d, ok := f.di[iso.NetDomain[pd.Net]]
			if !ok || iso.NetDomain[pd.Net] == "" {
				continue
			}
			pb := pd.Box.Bounds().Expand(f.g)
			x0, y0 := f.cellOf(Point{pb.MinX, pb.MinY})
			x1, y1 := f.cellOf(Point{pb.MaxX, pb.MaxY})
			for y := max(y0, 0); y <= min(y1, f.H-1); y++ {
				for x := max(x0, 0); x <= min(x1, f.W-1); x++ {
					if pd.Box.Dist(f.center(x, y)) <= f.g/2 {
						seeds[d][y*f.W+x] = true
						has[d] = true
					}
				}
			}
		}
	}
	live := 0
	for _, p := range iso.Pairs {
		if has[f.di[p.A]] && has[f.di[p.B]] {
			live++
		}
	}
	if live == 0 {
		return nil
	}
	// Territory = nearest domain pad (exact Euclidean transform per domain).
	near := make([][]float32, len(f.doms))
	for d := range f.doms {
		if has[d] {
			near[d] = edt(seeds[d], f.W, f.H)
		}
	}
	f.label = make([]int8, n)
	for i := 0; i < n; i++ {
		best, bd := int8(-1), float32(math.Inf(1))
		for d := range f.doms {
			if near[d] != nil && near[d][i] < bd {
				best, bd = int8(d), near[d][i]
			}
		}
		f.label[i] = best
	}
	f.distT = make([][]float32, len(f.doms))
	for d := range f.doms {
		if !has[d] {
			continue
		}
		in := make([]bool, n)
		for i, l := range f.label {
			in[i] = int(l) == d
		}
		dt := edt(in, f.W, f.H)
		for i := range dt {
			dt[i] *= float32(f.g)
		}
		f.distT[d] = dt
	}
	f.partners = make([][]isoPartner, len(f.doms))
	for _, p := range iso.Pairs {
		a, bb := f.di[p.A], f.di[p.B]
		if !has[a] || !has[bb] {
			continue
		}
		f.partners[a] = append(f.partners[a], isoPartner{f: bb, clear: p.ClearanceMil, creep: p.CreepageMil, slotW: p.SlotWidthMil})
		f.partners[bb] = append(f.partners[bb], isoPartner{f: a, clear: p.ClearanceMil, creep: p.CreepageMil, slotW: p.SlotWidthMil})
	}
	return f
}

func (f *isoField) cellOf(p Point) (int, int) {
	return int(math.Floor((p.X - f.ox) / f.g)), int(math.Floor((p.Y - f.oy) / f.g))
}

func (f *isoField) center(x, y int) Point {
	return Point{f.ox + (float64(x)+0.5)*f.g, f.oy + (float64(y)+0.5)*f.g}
}

// slack returns, for copper of domain d at p, the smallest margin
// (distance to a partner territory − half the pair requirement), taken one
// cell short. Outer layers need half the creepage, inner layers half the
// clearance. +Inf when d has no partner.
func (f *isoField) slack(d int, p Point, outer bool) float64 {
	x, y := f.cellOf(p)
	if x < 0 || y < 0 || x >= f.W || y >= f.H {
		return math.Inf(1)
	}
	i := y*f.W + x
	s := math.Inf(1)
	for _, pt := range f.partners[d] {
		req := pt.clear
		if outer {
			req = pt.creep
		}
		s = math.Min(s, float64(f.distT[pt.f][i])-f.g-req/2)
	}
	return s
}

// edt is the exact Euclidean distance transform (Felzenszwalb–Huttenlocher)
// in cell units: distance from every cell to the nearest set cell.
func edt(set []bool, W, H int) []float32 {
	sq := edtSq(set, W, H)
	out := make([]float32, W*H)
	for i, d := range sq {
		if d == math.MaxUint32 {
			out[i] = float32(math.Inf(1))
		} else {
			out[i] = float32(math.Sqrt(float64(d)))
		}
	}
	return out
}

// edtSq returns the exact squared distance (cells²) from every cell to the
// nearest set cell, math.MaxUint32 when there is none (or beyond it).
func edtSq(set []bool, W, H int) []uint32 {
	const inf = 1e20
	g := make([]float64, W*H)
	for i, s := range set {
		if !s {
			g[i] = inf
		}
	}
	n := max(W, H)
	fb, db := make([]float64, n), make([]float64, n)
	v, z := make([]int, n), make([]float64, n+1)
	pass := func(get func(int) float64, put func(int, float64), m int) {
		for q := 0; q < m; q++ {
			fb[q] = get(q)
		}
		sect := func(q, r int) float64 {
			return ((fb[q] + float64(q*q)) - (fb[r] + float64(r*r))) / float64(2*q-2*r)
		}
		k := 0
		v[0], z[0], z[1] = 0, -inf, inf
		for q := 1; q < m; q++ {
			s := sect(q, v[k])
			for s <= z[k] {
				k--
				s = sect(q, v[k])
			}
			k++
			v[k], z[k], z[k+1] = q, s, inf
		}
		k = 0
		for q := 0; q < m; q++ {
			for z[k+1] < float64(q) {
				k++
			}
			r := v[k]
			db[q] = float64((q-r)*(q-r)) + fb[r]
		}
		for q := 0; q < m; q++ {
			put(q, db[q])
		}
	}
	for x := 0; x < W; x++ {
		pass(func(q int) float64 { return g[q*W+x] }, func(q int, val float64) { g[q*W+x] = val }, H)
	}
	for y := 0; y < H; y++ {
		pass(func(q int) float64 { return g[y*W+q] }, func(q int, val float64) { g[y*W+q] = val }, W)
	}
	out := make([]uint32, W*H)
	for i, d := range g {
		if d >= math.MaxUint32 {
			out[i] = math.MaxUint32
		} else {
			out[i] = uint32(d)
		}
	}
	return out
}

// ---- router fence ---------------------------------------------------------------

// setupIso builds the territory field for the router and tags every net
// with its domain.
func (r *router) setupIso() {
	if r.an == nil || r.an.Iso == nil {
		return
	}
	r.iso = buildIsoField(r.b, r.an.Iso, math.Max(isoFieldCell, r.gr.g))
	for _, n := range r.nets {
		n.isoDom = -1
		if r.iso == nil {
			continue
		}
		if d, ok := r.iso.di[r.an.Iso.NetDomain[n.name]]; ok && r.an.Iso.NetDomain[n.name] != "" && len(r.iso.partners[d]) > 0 {
			n.isoDom = d
		}
	}
	if r.iso != nil {
		r.setupIsoPads()
	}
}

// setupIsoPads lists every paired domain's pads, and among them the
// pinched ones: pads closer to another domain's territory than half the
// largest requirement of their domain (bridge parts' rows, closely set
// parts of two domains). The territory fence alone guarantees the pair
// distance only to pads at least half the requirement inside their own
// territory — copper half the requirement outside the partner territory
// plus a pinched pad's shortfall is less than the requirement. The fence
// checks the exact distance (and surface path round the slots) to these.
func (r *router) setupIsoPads() {
	f := r.iso
	iso := r.an.Iso
	f.pads = make([][]*Pad, len(f.doms))
	f.pinched = make([][]*Pad, len(f.doms))
	for _, p := range r.b.Parts {
		for _, pd := range p.Pads {
			d, ok := f.di[iso.NetDomain[pd.Net]]
			if !ok || iso.NetDomain[pd.Net] == "" || len(f.partners[d]) == 0 {
				continue
			}
			f.pads[d] = append(f.pads[d], pd)
			half := 0.0
			for _, pt := range f.partners[d] {
				half = math.Max(half, math.Max(pt.clear, pt.creep)/2)
			}
			x, y := f.cellOf(pd.Box.C)
			if x < 0 || y < 0 || x >= f.W || y >= f.H {
				continue
			}
			// Distance from the pad's copper to the nearest cell of another
			// territory (two cells short: the field is discrete).
			edge := math.Inf(1)
			for o := range f.doms {
				if o != d && f.distT[o] != nil {
					edge = math.Min(edge, float64(f.distT[o][y*f.W+x]))
				}
			}
			edge -= math.Hypot(pd.Box.W, pd.Box.H)/2 + 2*f.g
			if edge < half {
				f.pinched[d] = append(f.pinched[d], pd)
			}
		}
	}
}

// isoOK is the domain fence: copper of half extent hw of net n at node
// (l,x,y) must keep half the pair requirement from the partner domains'
// territory (creepage on the outer layers, clearance inside). Within the
// neck of the net's own pads the fence yields — a bridge pad itself may sit
// inside the band (its pad rows are the footprint's; a slot or the part's
// own spacing carries them, and the isolation check judges the result).
func (r *router) isoOK(n *rnet, l, x, y int, hw float64) bool {
	id := r.gr.layers[l]
	outer := id == LayerTop || id == LayerBottom
	p := r.gr.center(x, y)
	sp := r.iso.slack(n.isoDom, p, outer)
	if sp >= hw {
		return r.isoPadsOK(n, l, x, y, p, hw, false)
	}
	if !r.inNeck(n, x, y) {
		return false
	}
	// In the neck the fence yields only as far as the pad itself reaches:
	// copper may not come closer to the partner territory than its own pad
	// edge. Yielding outright let a SELV via sit 11 mil inside the creepage
	// of an isolated DC/DC's far-side pin (400 V inverter stress board).
	var own *Pad
	best := math.Inf(1)
	for _, g := range n.groups {
		for _, pd := range g {
			if d := pd.Box.Dist(p); d < best {
				best, own = d, pd
			}
		}
	}
	if own == nil {
		return true
	}
	if sp-hw < r.padSlack(n, own, outer) {
		return false
	}
	return r.isoPadsOK(n, l, x, y, p, hw, true)
}

// padSlack is the smallest fence margin over a pad's own copper outline:
// how deep into the partner band the footprint itself reaches.
func (r *router) padSlack(n *rnet, pd *Pad, outer bool) float64 {
	k := 0
	if outer {
		k = 1
	}
	if v, ok := r.isoPadSlack[k][pd]; ok {
		return v
	}
	s := math.Inf(1)
	poly := padPoly(pd)
	for i, a := range poly {
		c := poly[(i+1)%len(poly)]
		// The outline and its edges sampled at the field cell: the field is
		// piecewise, a corner alone can miss the edge's closest cell.
		steps := max(1, int(math.Ceil(a.Dist(c)/r.iso.g)))
		for j := 0; j < steps; j++ {
			q := a.Add(c.Sub(a).Scale(float64(j) / float64(steps)))
			s = math.Min(s, r.iso.slack(n.isoDom, q, outer))
		}
	}
	if r.isoPadSlack[k] == nil {
		r.isoPadSlack[k] = map[*Pad]float64{}
	}
	r.isoPadSlack[k][pd] = s
	return s
}

// isoPadsOK is the exact half of the fence, against partner-domain pads.
// The territory field is a straight-line, half-and-half split: it keeps
// copper the pair distance from a partner pad only where that pad sits at
// least half the requirement inside its own territory. Bridge parts' rows
// and closely set parts of two domains do not (pinched pads), and neither
// does copper in the neck of its own pad, where the field yields:
//
//   - the mains/SELV opto's GND pad reaches 35 mil into the band towards the
//     opto's own AC_N row across the slot, and a GND fan-out via used that
//     allowance 29 mil below the pad, towards a transformer AC_N pin:
//     155.9 mil surface path < 181.1 mil reinforced creepage;
//   - an AC_N via 110 mil beside the opto's AC row, outside any neck, took
//     the field's half of the band while the opto's ZC pad sat 80 mil from
//     the slot midline: 180.2 mil round the slot end < 181.1 mil
//     (2026-09-30, both seen when a loaded machine cut the placer's anneal
//     short).
//
// So every partner pad within reach is measured exactly: on the outer
// layers the surface path round the milled slots against the creepage
// (and the straight gap against the clearance), inside the straight gap
// against the clearance. Outside the neck the copper meets the requirement;
// in the neck of its own pads it may come no closer than the net's own
// nearest pad already is (a bridge part's rows, judged with their slot by
// CheckIsolation). neck=false checks only the pinched pads — every other
// partner pad is covered by the field.
func (r *router) isoPadsOK(n *rnet, l, x, y int, p Point, hw float64, neck bool) bool {
	f := r.iso
	near := false
	for _, pt := range f.partners[n.isoDom] {
		pads := f.pinched[pt.f]
		if neck {
			pads = f.pads[pt.f]
		}
		reach := math.Max(pt.clear, pt.creep)
		for _, pd := range pads {
			if pd.Box.Dist(p)-hw < reach {
				near = true
				break
			}
		}
		if near {
			break
		}
	}
	if !near {
		return true
	}
	k := uint64(r.gr.idx(l, x, y))<<25 | uint64(math.Round(hw*100))&0xffffff<<1
	if neck {
		k |= 1
	}
	if v, ok := n.isoMemo[k]; ok {
		return v
	}
	v := r.isoPadsExact(n, l, p, hw, neck)
	if n.isoMemo == nil {
		n.isoMemo = map[uint64]bool{}
	}
	n.isoMemo[k] = v
	return v
}

func (r *router) isoPadsExact(n *rnet, l int, p Point, hw float64, neck bool) bool {
	f := r.iso
	id := r.gr.layers[l]
	outer := id == LayerTop || id == LayerBottom
	var cu []Point // the copper disk, circumscribed (never under-sized)
	for _, pt := range f.partners[n.isoDom] {
		pads := f.pinched[pt.f]
		if neck {
			pads = f.pads[pt.f]
		}
		for _, pd := range pads {
			if !pd.OnLayer(id) {
				continue
			}
			gap := pd.Box.Dist(p) - hw
			if gap >= math.Max(pt.clear, pt.creep)-0.01 {
				continue
			}
			path := gap
			if outer && gap < pt.creep-0.01 {
				if cu == nil {
					cu = circlePoly(p, hw)
				}
				path = r.isoSurfacePath(cu, padPoly(pd), pt.slotW)
			}
			okClear := gap >= pt.clear-0.01
			okCreep := !outer || path >= pt.creep-0.01
			if okClear && okCreep {
				continue
			}
			if !neck {
				return false
			}
			ownGap, ownPath := r.isoOwnDist(n, pd, id, pt.slotW)
			if !okClear && gap < math.Min(pt.clear, ownGap)-0.01 {
				return false
			}
			if !okCreep && path < math.Min(pt.creep, ownPath)-0.01 {
				return false
			}
		}
	}
	return true
}

// isoSurfacePath is the surface path between two copper polygons round the
// board's cutouts at least slotW wide.
func (r *router) isoSurfacePath(a, c []Point, slotW float64) float64 {
	if r.isoSlots == nil {
		r.isoSlots = map[float64][][]Point{}
	}
	slots, ok := r.isoSlots[slotW]
	if !ok {
		slots = isoSlotPolys(r.b, nil, slotW)
		r.isoSlots[slotW] = slots
	}
	d, pa, pc := polyDist(a, c)
	path, _ := surfacePath(a, c, pa, pc, d, slots)
	return path
}

// isoOwnDist is how close net n's own pads on layer id already are to the
// partner pad pd: the straight gap and the surface path (round slots).
func (r *router) isoOwnDist(n *rnet, pd *Pad, id int, slotW float64) (gap, path float64) {
	gap, path = math.Inf(1), math.Inf(1)
	for _, g := range n.groups {
		for _, o := range g {
			if !o.OnLayer(id) {
				continue
			}
			k := [2]*Pad{o, pd}
			v, ok := r.isoOwn[k]
			if !ok {
				a, c := padPoly(o), padPoly(pd)
				d, _, _ := polyDist(a, c)
				v = [2]float64{d, r.isoSurfacePath(a, c, slotW)}
				if r.isoOwn == nil {
					r.isoOwn = map[[2]*Pad][2]float64{}
				}
				r.isoOwn[k] = v
			}
			gap, path = math.Min(gap, v[0]), math.Min(path, v[1])
		}
	}
	return gap, path
}

// ---- bridge geometry and slots ------------------------------------------------

// BridgeGeom is the layout of a bridge part's two pad groups.
type BridgeGeom struct {
	GapMil     float64 // closest copper distance between the two sides
	RowGapMil  float64 // pad-row face to face, along the barrier normal
	SpanMil    float64 // extent of both rows along the barrier
	Axis       Point   // unit normal of the barrier (A side → B side)
	Mid        float64 // slot centre along Axis
	SpanCenter float64 // slot centre along the barrier
}

// padPoly returns a convex polygon covering the pad copper.
func padPoly(pd *Pad) []Point {
	b := pd.Box
	if b.Round {
		r := math.Min(b.W, b.H) / 2
		if math.Abs(b.W-b.H) < 1e-6 {
			return circlePoly(b.C, r)
		}
		// Stadium: two circles along the long axis.
		hl := math.Max(b.W, b.H)/2 - r
		dir := Point{1, 0}
		if b.H > b.W {
			dir = Point{0, 1}
		}
		dir = dir.Rotate(b.Rot)
		return capsulePoly(b.C.Sub(dir.Scale(hl)), b.C.Add(dir.Scale(hl)), r)
	}
	hw, hh := b.W/2, b.H/2
	out := make([]Point, 4)
	for i, c := range [4]Point{{-hw, -hh}, {hw, -hh}, {hw, hh}, {-hw, hh}} {
		out[i] = c.Rotate(b.Rot).Add(b.C)
	}
	return out
}

// circlePoly is a 16-gon circumscribing a circle (never under-sizes copper).
func circlePoly(c Point, r float64) []Point {
	const n = 16
	rr := r / math.Cos(math.Pi/n)
	out := make([]Point, n)
	for i := range out {
		a := 2 * math.Pi * (float64(i) + 0.5) / n
		out[i] = Point{c.X + rr*math.Cos(a), c.Y + rr*math.Sin(a)}
	}
	return out
}

// capsulePoly circumscribes a track (segment a–b of half width r).
func capsulePoly(a, b Point, r float64) []Point {
	if a.Dist(b) < 1e-9 {
		return circlePoly(a, r)
	}
	pts := append(circlePoly(a, r), circlePoly(b, r)...)
	return convexHull(pts)
}

// convexHull (monotone chain), CCW.
func convexHull(pts []Point) []Point {
	ps := append([]Point(nil), pts...)
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].X != ps[j].X {
			return ps[i].X < ps[j].X
		}
		return ps[i].Y < ps[j].Y
	})
	cross := func(o, a, b Point) float64 { return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X) }
	var h []Point
	for _, p := range ps {
		for len(h) >= 2 && cross(h[len(h)-2], h[len(h)-1], p) <= 0 {
			h = h[:len(h)-1]
		}
		h = append(h, p)
	}
	lo := len(h) + 1
	for i := len(ps) - 2; i >= 0; i-- {
		p := ps[i]
		for len(h) >= lo && cross(h[len(h)-2], h[len(h)-1], p) <= 0 {
			h = h[:len(h)-1]
		}
		h = append(h, p)
	}
	return h[:len(h)-1]
}

func dot(a, b Point) float64 { return a.X*b.X + a.Y*b.Y }

// bridgeGeometry measures a bridge part's two sides for a pair (nil when
// the part has no pad on one of them).
func bridgeGeometry(p *Part, iso *IsoRules, ip *IsoPair) *BridgeGeom {
	if p == nil {
		return nil
	}
	var as, bs [][]Point
	ca, cb := Point{}, Point{}
	for _, pd := range p.Pads {
		switch iso.NetDomain[pd.Net] {
		case ip.A:
			as = append(as, padPoly(pd))
			ca = ca.Add(pd.Box.C)
		case ip.B:
			bs = append(bs, padPoly(pd))
			cb = cb.Add(pd.Box.C)
		}
	}
	if len(as) == 0 || len(bs) == 0 {
		return nil
	}
	ca, cb = ca.Scale(1/float64(len(as))), cb.Scale(1/float64(len(bs)))
	u := cb.Sub(ca)
	if l := math.Hypot(u.X, u.Y); l > 1e-9 {
		u = u.Scale(1 / l)
	} else {
		u = Point{1, 0}
	}
	// Snap the barrier normal to the footprint's own axes: with pins used
	// asymmetrically (EE10: pins 1–2 on one row, 4 and 6 on the other) the
	// centroid line tilts ~10° and the slot came out skewed and 30 mil long.
	if a := math.Atan2(u.Y, u.X) * 180 / math.Pi; true {
		k := math.Round((a - p.Rotation) / 90)
		if snap := p.Rotation + 90*k; math.Abs(a-snap) <= 25 {
			r := snap * math.Pi / 180
			u = Point{math.Cos(r), math.Sin(r)}
		}
	}
	v := Point{-u.Y, u.X}
	g := &BridgeGeom{Axis: u, GapMil: math.Inf(1)}
	maxA, minB := math.Inf(-1), math.Inf(1)
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, pa := range as {
		for _, q := range pa {
			maxA = math.Max(maxA, dot(q, u))
			lo, hi = math.Min(lo, dot(q, v)), math.Max(hi, dot(q, v))
		}
		for _, pb := range bs {
			d, _, _ := polyDist(pa, pb)
			g.GapMil = math.Min(g.GapMil, d)
		}
	}
	for _, pb := range bs {
		for _, q := range pb {
			minB = math.Min(minB, dot(q, u))
			lo, hi = math.Min(lo, dot(q, v)), math.Max(hi, dot(q, v))
		}
	}
	g.RowGapMil = minB - maxA
	g.Mid = (maxA + minB) / 2
	g.SpanMil = hi - lo
	g.SpanCenter = (lo + hi) / 2
	return g
}

// IsoSlot is a milled slot (board cutout) planned under a bridge part.
type IsoSlot struct {
	Ref       string   `json:"ref"`
	A         string   `json:"a"`
	B         string   `json:"b"`
	Poly      []Point  `json:"poly"`
	WidthMil  float64  `json:"widthMil"`
	LengthMil float64  `json:"lengthMil"`
	GapMil    float64  `json:"padGapMil"`
	Why       []string `json:"why,omitempty"`
}

// isoSlotPrefix names the planned slots in Board.Holes.
const isoSlotPrefix = "ISO_SLOT:"

// IsoInfeasible is a bridge part whose own pads cannot meet the pair: the
// gap is below the clearance (a slot does not lengthen an air path) or a slot
// of the minimum width plus the copper keep-back does not fit between its pad
// rows. No routing, slot or placement fixes it — choose another part.
type IsoInfeasible struct {
	Ref         string  `json:"ref"`
	A           string  `json:"a"`
	B           string  `json:"b"`
	GapMil      float64 `json:"gapMil"`
	RequiredMil float64 `json:"requiredMil"`
	Reason      string  `json:"reason"`
}

// isoSlotPlan is one planned slot before it is added to the board.
type isoSlotPlan struct {
	slot IsoSlot
	c    Point
}

// isoBridgeVerdicts judges every bridge part against every pair it spans:
// fine as is, slot plan, or infeasible. It does not change the board.
func isoBridgeVerdicts(b *Board, iso *IsoRules) (plans []isoSlotPlan, bad []IsoInfeasible, notes []string) {
	if iso == nil {
		return
	}
	slotClr := math.Max(DefaultSlotClearance, b.Rules.Clearance)
	for _, ip := range iso.Pairs {
		for _, p := range b.Parts {
			g := bridgeGeometry(p, iso, ip)
			if g == nil || g.GapMil >= ip.CreepageMil {
				continue
			}
			w := math.Max(ip.SlotWidthMil, mmToMil(safety.SlotMinWidth(iso.Standard.PollutionDegree)))
			if g.GapMil < ip.ClearanceMil {
				msg := sprintf("%s: %s|%s pads %.0f mil apart < %.0f mil clearance — a slot does not lengthen the air path; choose a wider-body part", p.Ref, ip.A, ip.B, g.GapMil, ip.ClearanceMil)
				notes = append(notes, msg)
				bad = append(bad, IsoInfeasible{Ref: p.Ref, A: ip.A, B: ip.B, GapMil: round2(g.GapMil), RequiredMil: ip.ClearanceMil, Reason: msg})
				continue
			}
			if g.RowGapMil-2*slotClr < w {
				msg := sprintf("%s: a %.0f mil slot plus %.1f mil copper keep-back does not fit between pad rows %.0f mil apart — choose a wider-body part", p.Ref, w, slotClr, g.RowGapMil)
				notes = append(notes, msg)
				bad = append(bad, IsoInfeasible{Ref: p.Ref, A: ip.A, B: ip.B, GapMil: round2(g.RowGapMil), RequiredMil: w + 2*slotClr, Reason: msg})
				continue
			}
			e := mmToMil(safety.SlotExtension(ip.CreepageMil*0.0254, g.RowGapMil*0.0254, w*0.0254))
			length := g.SpanMil + 2*e + 2 // 1 mil rounding margin per end
			u, v := g.Axis, Point{-g.Axis.Y, g.Axis.X}
			c := u.Scale(g.Mid).Add(v.Scale(g.SpanCenter))
			poly := []Point{
				c.Add(u.Scale(-w / 2)).Add(v.Scale(-length / 2)), c.Add(u.Scale(w / 2)).Add(v.Scale(-length / 2)),
				c.Add(u.Scale(w / 2)).Add(v.Scale(length / 2)), c.Add(u.Scale(-w / 2)).Add(v.Scale(length / 2)),
			}
			s := IsoSlot{Ref: p.Ref, A: ip.A, B: ip.B, Poly: poly, WidthMil: round2(w), LengthMil: round2(length), GapMil: round2(g.GapMil)}
			s.Why = append(s.Why, sprintf("%s pads %.0f mil apart < %.0f mil creepage (%s): slot %.0f × %.0f mil (≥ %.2f mm wide; ends %.0f mil past the pad rows so the path around them ≥ creepage)",
				p.Ref, g.GapMil, ip.CreepageMil, ip.Ref, w, length, w*0.0254, e))
			if len(b.Outline) >= 3 {
				for _, q := range poly {
					if !PolyContains(b.Outline, q) {
						s.Why = append(s.Why, "slot runs past the board outline — check the mechanical drawing")
						break
					}
				}
			}
			plans = append(plans, isoSlotPlan{slot: s, c: c})
		}
	}
	return
}

// PlanIsoSlots plans a milled slot under every bridge part whose two sides
// are closer than the pair's creepage, adds it to the board as an owned
// hole (the router keeps copper off it, the part carries it), and returns
// the plan plus the bridges that cannot be fixed by a slot.
func PlanIsoSlots(b *Board, iso *IsoRules) (slots []IsoSlot, notes []string) {
	slots, notes, _ = PlanIsoSlotsDetail(b, iso)
	return slots, notes
}

// PlanIsoSlotsDetail is PlanIsoSlots with the infeasible bridges itemised.
func PlanIsoSlotsDetail(b *Board, iso *IsoRules) (slots []IsoSlot, notes []string, infeasible []IsoInfeasible) {
	// Drop the previous plan's slots (place/route loop passes).
	hs := b.Holes[:0]
	for _, h := range b.Holes {
		if !strings.HasPrefix(h.Name, isoSlotPrefix) {
			hs = append(hs, h)
		}
	}
	b.Holes = hs
	if iso == nil || len(iso.Pairs) == 0 {
		return nil, nil, nil
	}
	slotClr := math.Max(DefaultSlotClearance, b.Rules.Clearance)
	plans, bad, notes := isoBridgeVerdicts(b, iso)
	for _, pl := range plans {
		s := pl.slot
		slots = append(slots, s)
		// The hole gets its own copy of the polygon: an owned hole moves with
		// its part (MoveTo rewrites Poly in place), and a shared slice made
		// the reported slot — and the playbook's pcb.fill.create — follow
		// the part into the NEXT loop pass's placement while the board was
		// restored to the best pass (flyback E2E: slot 500 mil from U2).
		b.Holes = append(b.Holes, &Hole{Name: isoSlotPrefix + s.Ref + ":" + s.A + "|" + s.B, Owner: s.Ref, C: pl.c, Poly: append([]Point(nil), s.Poly...), Clr: slotClr})
	}
	if len(slots) > 0 {
		_ = b.Index()
	}
	return slots, notes, bad
}

// ---- the isolation check ----------------------------------------------------------

// IsoFinding is one clearance / creepage shortfall between two domains.
type IsoFinding struct {
	Kind         string  `json:"kind"` // iso-clearance | iso-creepage
	Severity     string  `json:"severity"`
	A            string  `json:"domainA"`
	B            string  `json:"domainB"`
	NetA         string  `json:"netA"`
	NetB         string  `json:"netB"`
	ItemA        string  `json:"itemA"`
	ItemB        string  `json:"itemB"`
	Layer        int     `json:"layer,omitempty"`
	At           Point   `json:"at"`
	GapMil       float64 `json:"gapMil"`
	PathMil      float64 `json:"pathMil,omitempty"` // creepage path (around slots)
	RequiredMil  float64 `json:"requiredMil"`
	SlotCredited bool    `json:"slotCredited,omitempty"`
	Message      string  `json:"message"`
}

// IsoCopper is the copper the isolation check sees.
type IsoCopper struct {
	Tracks []Track
	Vias   []Via
	// Planes are poured regions (planes, pours) — any polygon shape.
	Planes []PlaneRegion
	// Slots are extra cutouts (polygons) besides the board's holes.
	Slots [][]Point
}

// IsolationReport is the per-run isolation outcome.
type IsolationReport struct {
	Standard string       `json:"standard"`
	Pairs    []*IsoPair   `json:"pairs"`
	Slots    []IsoSlot    `json:"slots,omitempty"`
	Moats    [][]Point    `json:"noPourRegions,omitempty"`
	Findings []IsoFinding `json:"findings,omitempty"`
	Notes    []string     `json:"notes,omitempty"`
	// Infeasible lists the bridges no routing, slot or placement can make
	// compliant (their own pads are closer than the clearance, or a slot
	// does not fit): the design must change the part.
	Infeasible []IsoInfeasible `json:"infeasible,omitempty"`
}

type isoItem struct {
	net, dom string
	name     string
	poly     []Point
	bb       Rect
	top, bot bool // on the outer surfaces
	inner    []int
	multi    bool
}

// isoSlotPolys collects the cutouts that break creepage paths: board holes
// with a polygon (milled slots, MULTI-layer cutouts) and round holes, when
// at least minW wide.
func isoSlotPolys(b *Board, extra [][]Point, minW float64) [][]Point {
	var out [][]Point
	add := func(poly []Point) {
		if len(poly) < 3 {
			return
		}
		if polyMinWidth(poly) >= minW-1e-6 {
			out = append(out, poly)
		}
	}
	for _, h := range b.Holes {
		if len(h.Poly) >= 3 {
			add(h.Poly)
		} else if h.Dia > 0 {
			add(circlePoly(h.C, h.Dia/2*math.Cos(math.Pi/16)))
		}
	}
	for _, p := range extra {
		add(p)
	}
	return out
}

// polyMinWidth is the smallest width of a convex polygon (rotating the
// support over its edge normals).
func polyMinWidth(poly []Point) float64 {
	best := math.Inf(1)
	n := len(poly)
	for i := 0; i < n; i++ {
		a, b := poly[i], poly[(i+1)%n]
		e := b.Sub(a)
		l := math.Hypot(e.X, e.Y)
		if l < 1e-9 {
			continue
		}
		nrm := Point{-e.Y / l, e.X / l}
		lo, hi := math.Inf(1), math.Inf(-1)
		for _, q := range poly {
			d := dot(q, nrm)
			lo, hi = math.Min(lo, d), math.Max(hi, d)
		}
		best = math.Min(best, hi-lo)
	}
	return best
}

// CheckIsolation measures every pair of copper items of two insulated
// domains: the straight distance against the clearance on any shared layer,
// and on the outer surfaces the shortest surface path — around milled slots
// at least the pair's slot width wide — against the creepage. Pours are not
// included (the no-pour regions keep them back live).
func CheckIsolation(b *Board, iso *IsoRules, cu IsoCopper) []IsoFinding {
	if iso == nil || len(iso.Pairs) == 0 {
		return nil
	}
	var items []*isoItem
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			d := iso.NetDomain[pd.Net]
			if d == "" {
				continue
			}
			it := &isoItem{net: pd.Net, dom: d, name: p.Ref + "." + pd.Number, poly: padPoly(pd)}
			switch pd.Layer {
			case LayerMulti:
				it.top, it.bot, it.multi = true, true, true
			case LayerBottom:
				it.bot = true
			default:
				it.top = true
			}
			items = append(items, it)
		}
	}
	for i, t := range cu.Tracks {
		d := iso.NetDomain[t.Net]
		if d == "" {
			continue
		}
		it := &isoItem{net: t.Net, dom: d, name: sprintf("track#%d", i+1), poly: capsulePoly(t.A, t.B, t.Width/2)}
		switch t.Layer {
		case LayerTop:
			it.top = true
		case LayerBottom:
			it.bot = true
		default:
			it.inner = []int{t.Layer}
		}
		items = append(items, it)
	}
	for i, v := range cu.Vias {
		d := iso.NetDomain[v.Net]
		if d == "" {
			continue
		}
		items = append(items, &isoItem{net: v.Net, dom: d, name: sprintf("via#%d", i+1), poly: circlePoly(v.C, v.Dia/2), top: true, bot: true, multi: true})
	}
	for _, it := range items {
		it.bb = PolyBounds(it.poly)
	}
	type key struct{ kind, a, b string }
	worst := map[key]IsoFinding{}
	for _, ip := range iso.Pairs {
		slots := isoSlotPolys(b, cu.Slots, ip.SlotWidthMil)
		var as, bs []*isoItem
		for _, it := range items {
			switch it.dom {
			case ip.A:
				as = append(as, it)
			case ip.B:
				bs = append(bs, it)
			}
		}
		reach := math.Max(ip.ClearanceMil, ip.CreepageMil)
		for _, a := range as {
			for _, c := range bs {
				if !a.bb.Expand(reach).Overlaps(c.bb) {
					continue
				}
				d, pa, pc := polyDist(a.poly, c.poly)
				at := pa.Add(pc).Scale(0.5)
				// Clearance: any layer the two share (vias / THT pads span all).
				if sharesIsoLayer(a, c) && d < ip.ClearanceMil-0.01 {
					f := IsoFinding{Kind: "iso-clearance", Severity: "error", A: ip.A, B: ip.B, NetA: a.net, NetB: c.net, ItemA: a.name, ItemB: c.name,
						At: roundPt(at), GapMil: round2(d), RequiredMil: ip.ClearanceMil,
						Message: sprintf("%s (%s) ↔ %s (%s): %.1f mil < %.1f mil %s clearance (%s)", a.name, a.net, c.name, c.net, d, ip.ClearanceMil, ip.Insulation, shortRef(ip.Ref))}
					k := key{"c", a.name, c.name}
					if o, ok := worst[k]; !ok || f.GapMil < o.GapMil {
						worst[k] = f
					}
				}
				// Creepage on a shared outer surface.
				if !(a.top && c.top || a.bot && c.bot) || d >= ip.CreepageMil-0.01 {
					continue
				}
				path, credited := surfacePath(a.poly, c.poly, pa, pc, d, slots)
				if path >= ip.CreepageMil-0.01 {
					continue
				}
				f := IsoFinding{Kind: "iso-creepage", Severity: "error", A: ip.A, B: ip.B, NetA: a.net, NetB: c.net, ItemA: a.name, ItemB: c.name,
					At: roundPt(at), GapMil: round2(d), PathMil: round2(path), RequiredMil: ip.CreepageMil, SlotCredited: credited,
					Message: sprintf("%s (%s) ↔ %s (%s): surface path %.1f mil < %.1f mil %s creepage (%s)", a.name, a.net, c.name, c.net, path, ip.CreepageMil, ip.Insulation, shortRef(ip.Ref))}
				if credited {
					f.Message += " — path already runs around a slot: lengthen the slot"
				} else {
					f.Message += sprintf(" — mill a ≥ %.2f mm slot between them or move the copper apart", ip.SlotWidthMil*0.0254)
				}
				k := key{"p", a.name, c.name}
				if o, ok := worst[k]; !ok || f.PathMil < o.PathMil {
					worst[k] = f
				}
			}
		}
	}
	// Planes / pours: the clearance to every partner-domain item that shares
	// the layer (vias and THT pads cross every layer). A pour is judged on
	// its straight distance (clearance inner, creepage outer — a flood has
	// no surface path around a slot worth crediting here).
	for pi, pr := range cu.Planes {
		d := iso.NetDomain[pr.Net]
		if d == "" {
			continue
		}
		outer := pr.Layer == LayerTop || pr.Layer == LayerBottom
		for _, ip := range iso.Pairs {
			other := ""
			switch d {
			case ip.A:
				other = ip.B
			case ip.B:
				other = ip.A
			default:
				continue
			}
			req := ip.ClearanceMil
			if outer {
				req = ip.CreepageMil
			}
			for _, it := range items {
				if it.dom != other {
					continue
				}
				on := it.multi || outer && (pr.Layer == LayerTop && it.top || pr.Layer == LayerBottom && it.bot)
				for _, l := range it.inner {
					on = on || l == pr.Layer
				}
				if !on {
					continue
				}
				for _, poly := range pr.Polys {
					g, at := polyGapAny(poly, it.poly)
					if g >= req-0.01 {
						continue
					}
					kind := "iso-clearance"
					if outer {
						kind = "iso-creepage"
					}
					f := IsoFinding{Kind: kind, Severity: "error", A: ip.A, B: ip.B, NetA: pr.Net, NetB: it.net, ItemA: sprintf("pour#%d(%s L%d)", pi+1, pr.Net, pr.Layer), ItemB: it.name,
						Layer: pr.Layer, At: roundPt(at), GapMil: round2(g), RequiredMil: req,
						Message: sprintf("%s pour on layer %d ↔ %s (%s): %.1f mil < %.1f mil %s (%s) — clip the pour to its domain", pr.Net, pr.Layer, it.name, it.net, g, req, ip.Insulation, shortRef(ip.Ref))}
					k := key{"g", f.ItemA, it.name}
					if o, ok := worst[k]; !ok || f.GapMil < o.GapMil {
						worst[k] = f
					}
				}
			}
		}
	}
	out := make([]IsoFinding, 0, len(worst))
	for _, f := range worst {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		ri, rj := out[i].GapMil/out[i].RequiredMil, out[j].GapMil/out[j].RequiredMil
		if out[i].PathMil > 0 {
			ri = out[i].PathMil / out[i].RequiredMil
		}
		if out[j].PathMil > 0 {
			rj = out[j].PathMil / out[j].RequiredMil
		}
		if ri != rj {
			return ri < rj
		}
		return out[i].ItemA+out[i].ItemB < out[j].ItemA+out[j].ItemB
	})
	return out
}

// shortRef trims a table reference to its standard and clause.
func shortRef(ref string) string {
	for _, sep := range []string{" §", " ("} {
		if i := strings.Index(ref, sep); i > 0 {
			ref = ref[:i]
		}
	}
	return ref
}

func roundPt(p Point) Point { return Point{round2(p.X), round2(p.Y)} }

func sharesIsoLayer(a, c *isoItem) bool {
	if a.multi || c.multi {
		return true
	}
	if a.top && c.top || a.bot && c.bot {
		return true
	}
	for _, l := range a.inner {
		for _, m := range c.inner {
			if l == m {
				return true
			}
		}
	}
	return false
}

// polyDist returns the distance between two convex polygons and the closest
// points (0 when they overlap).
func polyDist(p, q []Point) (float64, Point, Point) {
	if polysOverlap(p, q) {
		c := p[0]
		return 0, c, c
	}
	best := math.Inf(1)
	var bp, bq Point
	for i := range p {
		for j := range q {
			a, b := q[j], q[(j+1)%len(q)]
			if c := segNearest(p[i], a, b); c.Dist(p[i]) < best {
				best, bp, bq = c.Dist(p[i]), p[i], c
			}
			a2, b2 := p[i], p[(i+1)%len(p)]
			if c := segNearest(q[j], a2, b2); c.Dist(q[j]) < best {
				best, bp, bq = c.Dist(q[j]), c, q[j]
			}
		}
	}
	return best, bp, bq
}

func polysOverlap(p, q []Point) bool {
	if PolyContains(q, p[0]) || PolyContains(p, q[0]) {
		return true
	}
	for i := range p {
		for j := range q {
			if segsIntersect(p[i], p[(i+1)%len(p)], q[j], q[(j+1)%len(q)]) {
				return true
			}
		}
	}
	return false
}

// crossesSlot reports whether segment a–b passes through a slot's interior.
func crossesSlot(a, b Point, slot []Point) bool {
	n := len(slot)
	for i := 0; i < n; i++ {
		if segsIntersect(a, b, slot[i], slot[(i+1)%n]) {
			return true
		}
	}
	// Fully inside, or along a diagonal between vertices.
	for _, t := range []float64{0.25, 0.5, 0.75} {
		if PolyContains(slot, a.Add(b.Sub(a).Scale(t))) && PolyEdgeDist(slot, a.Add(b.Sub(a).Scale(t))) > 1e-3 {
			return true
		}
	}
	return false
}

func blocked(a, b Point, slots [][]Point) bool {
	for _, s := range slots {
		if crossesSlot(a, b, s) {
			return true
		}
	}
	return false
}

// surfacePath is the shortest surface distance between two convex copper
// polygons when milled slots are voids: straight when no slot is in the
// way, else a Dijkstra over the slot corners (the path bends only there).
// credited reports that a slot lengthened the path.
func surfacePath(pa, pb []Point, ca, cb Point, straight float64, slots [][]Point) (float64, bool) {
	if len(slots) == 0 || !blocked(ca, cb, slots) {
		return straight, false
	}
	// Relevant slots: those near the straight line.
	var nodes []Point
	for _, s := range slots {
		cen := Point{}
		for _, q := range s {
			cen = cen.Add(q)
		}
		cen = cen.Scale(1 / float64(len(s)))
		for _, q := range s {
			d := q.Sub(cen)
			l := math.Hypot(d.X, d.Y)
			if l < 1e-9 {
				continue
			}
			nodes = append(nodes, q.Add(d.Scale(0.02/l))) // just outside the corner
		}
	}
	nearOn := func(poly []Point, p Point) Point {
		best, bp := math.Inf(1), poly[0]
		for i := range poly {
			c := segNearest(p, poly[i], poly[(i+1)%len(poly)])
			if d := c.Dist(p); d < best {
				best, bp = d, c
			}
		}
		return bp
	}
	n := len(nodes)
	dist := make([]float64, n)
	done := make([]bool, n)
	for i := range dist {
		dist[i] = math.Inf(1)
		s := nearOn(pa, nodes[i])
		if !blocked(s, nodes[i], slots) {
			dist[i] = s.Dist(nodes[i])
		}
	}
	best := math.Inf(1)
	for {
		u := -1
		for i := 0; i < n; i++ {
			if !done[i] && !math.IsInf(dist[i], 1) && (u < 0 || dist[i] < dist[u]) {
				u = i
			}
		}
		if u < 0 || dist[u] >= best {
			break
		}
		done[u] = true
		if e := nearOn(pb, nodes[u]); !blocked(nodes[u], e, slots) {
			best = math.Min(best, dist[u]+nodes[u].Dist(e))
		}
		for v := 0; v < n; v++ {
			if done[v] {
				continue
			}
			if nd := dist[u] + nodes[u].Dist(nodes[v]); nd < dist[v] && !blocked(nodes[u], nodes[v], slots) {
				dist[v] = nd
			}
		}
	}
	return best, true
}

// ---- no-pour moat ----------------------------------------------------------------

// isoMoats traces the band around every territory boundary where copper of
// one domain would sit closer than half the creepage to the other domain's
// territory: live no-pour regions keep the pours (which EasyEDA floods with
// the board clearance only) out of it.
func isoMoats(b *Board, iso *IsoRules) [][]Point {
	f := buildIsoField(b, iso, 10)
	if f == nil {
		return nil
	}
	c := &coarse{cg: f.g, ox: f.ox, oy: f.oy, W: f.W, H: f.H, label: make([]int, f.W*f.H), nets: []string{"moat"}}
	for i := range c.label {
		c.label[i] = -1
	}
	for y := 0; y < f.H; y++ {
		for x := 0; x < f.W; x++ {
			i := y*f.W + x
			d := int(f.label[i])
			if d < 0 {
				continue
			}
			p := f.center(x, y)
			if len(b.Outline) >= 3 && !PolyContains(b.Outline, p) && PolyEdgeDist(b.Outline, p) > f.g {
				continue
			}
			// Dilated by one cell so the simplified polygon never undercuts.
			if f.slack(d, p, true) < 2*f.g {
				c.label[i] = 0
			}
		}
	}
	var out [][]Point
	for _, lp := range traceLoops(c, 0) {
		if signedArea(lp) <= 0 {
			continue
		}
		lp = simplifyLoop(lp, f.g)
		if len(lp) >= 3 {
			out = append(out, lp)
		}
	}
	return out
}

// simplifyLoop is Douglas–Peucker on a closed loop.
func simplifyLoop(lp []Point, tol float64) []Point {
	if len(lp) <= 4 {
		return lp
	}
	var dp func(pts []Point) []Point
	dp = func(pts []Point) []Point {
		if len(pts) < 3 {
			return pts
		}
		a, z := pts[0], pts[len(pts)-1]
		k, dm := 0, 0.0
		for i := 1; i < len(pts)-1; i++ {
			if d := PointSegDist(pts[i], a, z); d > dm {
				k, dm = i, d
			}
		}
		if dm <= tol {
			return []Point{a, z}
		}
		l := dp(pts[:k+1])
		r := dp(pts[k:])
		return append(l[:len(l)-1], r...)
	}
	// Split at the vertex farthest from the first.
	far := 0
	for i := range lp {
		if lp[i].Dist(lp[0]) > lp[far].Dist(lp[0]) {
			far = i
		}
	}
	h1 := dp(append([]Point(nil), lp[:far+1]...))
	h2 := dp(append(append([]Point(nil), lp[far:]...), lp[0]))
	out := append(h1[:len(h1)-1], h2[:len(h2)-1]...)
	return out
}

// isolationReport runs the post-route isolation outcome for a result.
func isolationReport(b *Board, an *Analysis, rr *RouteResult, slots []IsoSlot, notes []string, infeasible []IsoInfeasible) *IsolationReport {
	if an == nil || an.Iso == nil {
		return nil
	}
	rep := &IsolationReport{Standard: safety.NormStandard(an.Iso.Standard.Name), Pairs: an.Iso.Pairs, Slots: slots,
		Notes: append(append([]string(nil), an.Iso.Notes...), notes...), Infeasible: infeasible}
	if len(an.Iso.Pairs) == 0 {
		return rep
	}
	rep.Moats = isoMoats(b, an.Iso)
	var cu IsoCopper
	if rr != nil {
		cu.Tracks, cu.Vias, cu.Planes = rr.Tracks, rr.Vias, rr.Planes
	}
	rep.Findings = CheckIsolation(b, an.Iso, cu)
	return rep
}

// ---- isolation check of a measured board (pcb check --intent) -----------------

// IsolationCheck is the result of checking a `pcb dump --include-copper`
// snapshot against an intent.
type IsolationCheck struct {
	Standard string       `json:"standard"`
	Pairs    []*IsoPair   `json:"pairs"`
	Tracks   int          `json:"tracks"`
	Vias     int          `json:"vias"`
	Cutouts  int          `json:"cutouts"`
	Findings []IsoFinding `json:"findings"`
	Notes    []string     `json:"notes,omitempty"`
	// Infeasible bridges (see IsolationReport.Infeasible).
	Infeasible []IsoInfeasible `json:"infeasible,omitempty"`
}

// CheckIsolationSnapshot checks the copper of a board dump (tracks, vias,
// pads; MULTI-layer cutouts credited as slots) against the intent pairs.
func CheckIsolationSnapshot(raw []byte, in *Intent) (*IsolationCheck, error) {
	b, err := FromSnapshot(raw)
	if err != nil {
		return nil, err
	}
	var s struct {
		Copper *struct {
			Lines []map[string]any `json:"lines"`
			Arcs  []map[string]any `json:"arcs"`
			Vias  []map[string]any `json:"vias"`
			Fills []map[string]any `json:"fills"`
			Pours []map[string]any `json:"pours"`
		} `json:"copper"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	iso := buildIsoRules(b, in)
	out := &IsolationCheck{Standard: safety.NormStandard(in.Standard.Name), Pairs: iso.Pairs, Notes: iso.Notes}
	var cu IsoCopper
	if s.Copper == nil {
		out.Notes = append(out.Notes, "snapshot has no copper section (pcb dump --include-copper): pads only")
	} else {
		for _, l := range s.Copper.Lines {
			cu.Tracks = append(cu.Tracks, Track{Net: str(l["net"]), Layer: int(num(l["layer"])), Width: num(l["lineWidth"]),
				A: Point{num(l["startX"]), num(l["startY"])}, B: Point{num(l["endX"]), num(l["endY"])}})
		}
		for _, a := range s.Copper.Arcs {
			// Arcs as their chord (bounded error; beautify arcs are short).
			cu.Tracks = append(cu.Tracks, Track{Net: str(a["net"]), Layer: int(num(a["layer"])), Width: num(a["lineWidth"]),
				A: Point{num(a["startX"]), num(a["startY"])}, B: Point{num(a["endX"]), num(a["endY"])}})
		}
		if len(s.Copper.Arcs) > 0 {
			out.Notes = append(out.Notes, sprintf("%d arc(s) checked as their chord", len(s.Copper.Arcs)))
		}
		for _, v := range s.Copper.Vias {
			cu.Vias = append(cu.Vias, Via{Net: str(v["net"]), C: Point{num(v["x"]), num(v["y"])}, Dia: num(v["diameter"]), Drill: num(v["holeDiameter"])})
		}
		for _, p := range s.Copper.Pours {
			if poly := sourcePoints(p["source"]); len(poly) >= 3 && str(p["net"]) != "" {
				cu.Planes = append(cu.Planes, PlaneRegion{Net: str(p["net"]), Layer: int(num(p["layer"])), Polys: [][]Point{poly}})
			}
		}
		for _, f := range s.Copper.Fills {
			if int(num(f["layer"])) != LayerMulti {
				continue
			}
			poly := sourcePoints(f["source"])
			if len(poly) < 3 {
				if bb, ok := anyBBox(f["bbox"]); ok {
					poly = bb.Corners()
				}
			}
			if len(poly) >= 3 {
				cu.Slots = append(cu.Slots, convexHull(poly))
				out.Cutouts++
			}
		}
	}
	// Board holes from the snapshot duplicate the cutouts as circles; keep
	// only the footprint slots (owned) and use the exact cutout polygons.
	hs := b.Holes[:0]
	for _, h := range b.Holes {
		if h.Owner != "" || len(h.Poly) >= 3 {
			hs = append(hs, h)
		}
	}
	b.Holes = hs
	out.Tracks, out.Vias = len(cu.Tracks), len(cu.Vias)
	out.Findings = CheckIsolation(b, iso, cu)
	_, out.Infeasible, _ = isoBridgeVerdicts(b, iso)
	if len(cu.Planes) > 0 {
		out.Notes = append(out.Notes, sprintf("%d pour outline(s) checked as drawn (the flooded copper stays inside them) — re-check after pcb pour rebuild with native DRC", len(cu.Planes)))
	} else {
		out.Notes = append(out.Notes, "pours are not measured: the isolation no-pour regions keep them back — re-check after pcb pour rebuild with native DRC")
	}
	return out, nil
}

// PadPoly returns a convex polygon covering a pad's copper.
func PadPoly(pd *Pad) []Point { return padPoly(pd) }

// SegmentPoly returns a convex polygon covering a track segment of half
// width r.
func SegmentPoly(a, b Point, r float64) []Point { return capsulePoly(a, b, r) }

// PolyGap is the distance between two convex polygons (0 when they overlap).
func PolyGap(p, q []Point) float64 {
	d, _, _ := polyDist(p, q)
	return d
}

// ---- planes and pours stay in their own domain ---------------------------------

// isoPlaneCell is the raster (mil) planes are clipped on.
const isoPlaneCell = 20.0

// clipPlanesToIso trims every plane / pour region of a net that belongs to an
// insulated domain to that domain's territory, half the pair requirement
// back from the partner territory (clearance on inner layers, creepage on
// the outer ones). Without it the SELV ground plane of a 4-layer board ran
// under the whole high-voltage section — 0.2 mm of prepreg away from 450 V
// copper, and on the power layer a SELV rail region butted a 450 V region
// with the board clearance between them. The trimmed region is rebuilt as
// merged row rectangles (hole-free, never larger than the territory).
func clipPlanesToIso(b *Board, an *Analysis, rr *RouteResult) []string {
	if an == nil || an.Iso == nil || len(an.Iso.Pairs) == 0 || rr == nil || len(rr.Planes) == 0 {
		return nil
	}
	f := buildIsoField(b, an.Iso, isoPlaneCell)
	if f == nil {
		return nil
	}
	var notes []string
	margin := f.g // a kept cell (corners at 0.71 g) stays inside the allowed area with float slack
	for i := range rr.Planes {
		pr := &rr.Planes[i]
		di, ok := f.di[an.Iso.NetDomain[pr.Net]]
		if !ok || an.Iso.NetDomain[pr.Net] == "" || len(f.partners[di]) == 0 {
			continue
		}
		outer := pr.Layer == LayerTop || pr.Layer == LayerBottom
		// Distance (cells) to the partner domains' copper on this layer: a
		// bridge's far-side pad sits inside the band, so the territory alone
		// left the pour a mil short of it.
		req := 0.0
		partner := map[string]bool{}
		for _, pt := range f.partners[di] {
			partner[f.doms[pt.f]] = true
			if outer {
				req = math.Max(req, pt.creep)
			} else {
				req = math.Max(req, pt.clear)
			}
		}
		cuDist := isoPartnerDist(b, an.Iso, rr, f, partner, pr.Layer)
		// The rebuilt rectangles are whole cells: each keeps the board-edge
		// distance with its corners, not only its centre.
		edgeReq := an.edgePolicy(b).Req(pr.Layer, pr.Net)
		type run struct{ x0, x1, y0, y1 int }
		var rects []run
		open := map[[2]int]int{} // (x0,x1) → index of the rectangle still growing
		dropped := false
		for y := 0; y < f.H; y++ {
			next := map[[2]int]int{}
			x := 0
			for x < f.W {
				keep := func(x int) bool {
					c := f.center(x, y)
					in := false
					for _, poly := range pr.Polys {
						if PolyContains(poly, c) {
							in = true
							break
						}
					}
					if !in {
						return false
					}
					if len(b.Outline) >= 3 && !edgeCellOK(b.Outline, c, f.g/2, edgeReq) {
						return false
					}
					if f.slack(di, c, outer) < margin || float64(cuDist[y*f.W+x])*f.g-f.g < req+margin/2 {
						dropped = true
						return false
					}
					return true
				}
				if !keep(x) {
					x++
					continue
				}
				x0 := x
				for x < f.W && keep(x) {
					x++
				}
				k := [2]int{x0, x - 1}
				if j, ok := open[k]; ok {
					rects[j].y1 = y
					next[k] = j
				} else {
					rects = append(rects, run{x0, x - 1, y, y})
					next[k] = len(rects) - 1
				}
			}
			open = next
		}
		if !dropped {
			continue
		}
		var polys [][]Point
		for _, r := range rects {
			if r.x1-r.x0 < 1 && r.y1-r.y0 < 1 {
				continue // a lone cell is not worth a pour
			}
			lo := Point{f.ox + float64(r.x0)*f.g, f.oy + float64(r.y0)*f.g}
			hi := Point{f.ox + float64(r.x1+1)*f.g, f.oy + float64(r.y1+1)*f.g}
			polys = append(polys, Rect{lo.X, lo.Y, hi.X, hi.Y}.Corners())
		}
		notes = append(notes, sprintf("%s on layer %d clipped to its insulation domain %s (%d region(s))", pr.Net, pr.Layer, an.Iso.NetDomain[pr.Net], len(polys)))
		pr.Polys = polys
	}
	return notes
}

// polyGapAny is the distance between an arbitrary (possibly non-convex)
// polygon and a convex one: 0 when they overlap.
func polyGapAny(poly, conv []Point) (float64, Point) {
	if len(poly) < 3 || len(conv) == 0 {
		return math.Inf(1), Point{}
	}
	if PolyContains(poly, conv[0]) || PolyContains(conv, poly[0]) {
		return 0, conv[0]
	}
	best, at := math.Inf(1), Point{}
	n := len(poly)
	for i := 0; i < n; i++ {
		a, c := poly[i], poly[(i+1)%n]
		for j := range conv {
			q, r := conv[j], conv[(j+1)%len(conv)]
			if segsIntersect(a, c, q, r) {
				return 0, a
			}
			if p := segNearest(q, a, c); p.Dist(q) < best {
				best, at = p.Dist(q), p
			}
			if p := segNearest(a, q, r); p.Dist(a) < best {
				best, at = p.Dist(a), a
			}
		}
	}
	return best, at
}

// isoPartnerDist is, per cell of f, the distance (cells) to the nearest
// copper (pad, track, via) of the given domains on a layer.
func isoPartnerDist(b *Board, iso *IsoRules, rr *RouteResult, f *isoField, doms map[string]bool, layer int) []float32 {
	set := make([]bool, f.W*f.H)
	mark := func(bb Rect, dist func(Point) float64) {
		bb = bb.Expand(f.g)
		x0, y0 := f.cellOf(Point{bb.MinX, bb.MinY})
		x1, y1 := f.cellOf(Point{bb.MaxX, bb.MaxY})
		for y := max(y0, 0); y <= min(y1, f.H-1); y++ {
			for x := max(x0, 0); x <= min(x1, f.W-1); x++ {
				if dist(f.center(x, y)) <= f.g*0.71 {
					set[y*f.W+x] = true
				}
			}
		}
	}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if doms[iso.NetDomain[pd.Net]] && pd.OnLayer(layer) {
				pd := pd
				mark(pd.Box.Bounds(), pd.Box.Dist)
			}
		}
	}
	for _, t := range rr.Tracks {
		if doms[iso.NetDomain[t.Net]] && t.Layer == layer {
			t := t
			mark(EmptyRect().AddPoint(t.A).AddPoint(t.B).Expand(t.Width/2), func(q Point) float64 { return PointSegDist(q, t.A, t.B) - t.Width/2 })
		}
	}
	for _, v := range rr.Vias {
		if doms[iso.NetDomain[v.Net]] {
			v := v
			mark(Rect{v.C.X, v.C.Y, v.C.X, v.C.Y}.Expand(v.Dia/2), func(q Point) float64 { return q.Dist(v.C) - v.Dia/2 })
		}
	}
	return edt(set, f.W, f.H)
}
