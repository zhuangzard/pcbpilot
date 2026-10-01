package pcbauto

// Beautify — aesthetics phase B, routing track
// (docs/reviews/2026-09-routing-aesthetics/README.md §5B): post-route passes
// that make the emitted copper look designed without changing what it
// connects or how well it does it.
//
//   - pad entry: the last leg into a pad runs along a pad axis from the pad
//     centre (45° as the second choice), never out of a corner;
//   - S-jogs: two parallel legs offset by a fraction of a track width are
//     put on one line (the vertex next to the jog slides along its other leg);
//   - collinear merge: consecutive collinear pieces of one width become one
//     segment; a narrower piece between two wider ones is widened when the
//     exact check allows (never narrowed);
//   - grid landing: free segments move onto the 5 mil design grid (a
//     horizontal segment to y ∈ 5ℤ, a diagonal to x∓y ∈ 5ℤ), and route vias
//     onto 5 mil grid points, by at most one routing cell.
//
// Aesthetics is the lowest tier of ConstraintPriority. Every candidate is
// judged by the exact DRC (checkDRCTol on the neighbourhood, with a 0.05 mil
// margin) and must not cross its own net; directions stay octilinear and
// widths never shrink. Nets whose geometry is electrical are left alone:
// differential pairs, RF feeds, length-tuned nets, per-pair / HV clearance
// nets and isolation domains; vias of current-sized transitions (arrays)
// never move. The pipeline (Run) then gates the whole pass: it is kept only
// when DRC, completion, via count, the electrical group and every one of its
// items, the SI / pair findings and the isolation findings are no worse —
// otherwise it is retried without any length increase, then rolled back.

import (
	"math"
	"sort"
)

// BeautifyStats reports the post-route beautification pass.
type BeautifyStats struct {
	Fanouts      int    `json:"fanouts"`      // fan-out vias moved onto the pad centre's rays
	PadEntries   int    `json:"padEntries"`   // pad entries rebuilt along the pad axis (or at 45°)
	SJogs        int    `json:"sJogs"`        // S-jogs put on one line
	Merged       int    `json:"merged"`       // collinear vertices removed
	Widened      int    `json:"widened"`      // narrower collinear pieces widened to their neighbour
	LinesSnapped int    `json:"linesSnapped"` // free segments moved onto the 5 mil grid
	ViasSnapped  int    `json:"viasSnapped"`  // route vias moved onto the 5 mil grid
	Rejected     int    `json:"rejected"`     // candidates refused by the exact check
	Strict       bool   `json:"strict,omitempty"`
	Kept         bool   `json:"kept"`
	Reason       string `json:"reason,omitempty"`
}

func (s *BeautifyStats) changes() int {
	return s.Fanouts + s.PadEntries + s.SJogs + s.Merged + s.Widened + s.LinesSnapped + s.ViasSnapped
}

// NoBeautify disables the post-route beautification (A/B diagnostics and
// the Options switch).
var noBeautify bool

// bfyDebug observes refused candidates (diagnostics).
var bfyDebug func(format string, args ...any)

func (z *bfy) dbg(format string, args ...any) {
	if bfyDebug != nil {
		bfyDebug(format, args...)
	}
}

// beautifyGrid is the design grid the free vertices and vias land on.
const beautifyGrid = 5.0

// bfy is the working state of one beautification pass.
type bfy struct {
	b      *Board
	an     *Analysis
	st     *Stackup
	ts     []Track
	dead   []bool
	vs     []Via
	margin float64
	strict bool // no length increase at all
	pix    *padIndex
	partBB []Rect
	stats  *BeautifyStats
	// skipNet: geometry is electrical (pairs, RF, tuned, HV / isolation).
	skipNet map[string]bool
	// fixVias: nets whose vias never move (current arrays).
	fixVias map[string]bool
	// sensitive: nets whose routed length an electrical item measures.
	sensitive map[string]bool
	cur       string // the net being worked on
	planes    []PlaneRegion
	// zones: isolation moats and milled slots — no new copper near them.
	zones [][]Point
}

// beautify runs the passes over rr's copper and returns the new tracks and
// vias (rr is not modified).
func beautify(b *Board, an *Analysis, c *Circuit, st *Stackup, rr *RouteResult, strict bool, slots ...[]Point) ([]Track, []Via, *BeautifyStats) {
	_ = b.Part("") // index the parts before the window sub-boards share it
	z := &bfy{b: b, an: an, st: st, strict: strict, stats: &BeautifyStats{Strict: strict}, sensitive: map[string]bool{}}
	// Nets an electrical item of the joint score measures along the copper
	// (decap and hot loops, IR drop, ESD stubs and RF feeds of interface
	// chains, high-speed budgets) never get longer.
	if an != nil {
		for _, np := range an.Nets {
			switch np.Role {
			case RolePower, RoleGround, RoleSwitch, RoleClock, RoleAnalog:
				z.sensitive[np.Net] = true
			}
			if np.Interface != "" || np.CurrentA > 0 || np.SimCurrentA > 0 || np.MaxSkewMil > 0 {
				z.sensitive[np.Net] = true
			}
		}
	}
	if c != nil {
		for _, ch := range c.Chains {
			for _, n := range ch.Nets {
				z.sensitive[n] = true
			}
		}
	}
	z.planes = rr.Planes
	z.zones = append(z.zones, slots...)
	for _, h := range b.Holes {
		if len(h.Poly) >= 3 && h.Owner == "" {
			z.zones = append(z.zones, h.Poly)
		}
	}
	z.ts = append([]Track(nil), rr.Tracks...)
	z.dead = make([]bool, len(z.ts))
	z.vs = append([]Via(nil), rr.Vias...)
	z.pix = newPadIndex(b)
	maxClr, maxW := b.Rules.Clearance, b.Rules.TrackWidth
	z.skipNet, z.fixVias = map[string]bool{}, map[string]bool{}
	if an != nil {
		for _, np := range an.Nets {
			maxClr = math.Max(maxClr, np.ClearanceMil)
			switch {
			case np.Role == RoleDiff || np.PairWith != "" || np.Role == RoleRF || np.LengthGroup != "":
				z.skipNet[np.Net] = true
			case np.ClearanceMil > b.Rules.Clearance+0.5:
				z.skipNet[np.Net] = true
			}
			if np.ViasPerTransition > 1 || np.ExtraVias > 0 {
				z.fixVias[np.Net] = true
			}
		}
		if an.Iso != nil {
			for _, p := range an.Iso.Pairs {
				maxClr = math.Max(maxClr, p.ClearanceMil)
			}
			// Copper near the isolation bands and slots follows creepage:
			// nothing moves within two clearances of them (the per-pair
			// clearance itself is in the exact check, creepage in the gate).
			if len(an.Iso.Pairs) > 0 {
				z.zones = append(z.zones, isoMoats(b, an.Iso)...)
			}
		}
	}
	for _, t := range z.ts {
		maxW = math.Max(maxW, t.Width)
	}
	for _, v := range z.vs {
		maxW = math.Max(maxW, v.Dia)
	}
	z.margin = maxClr + maxW + 5
	for _, p := range b.Parts {
		bb := EmptyRect()
		for _, pd := range p.Pads {
			bb = bb.Union(pd.Box.Bounds())
		}
		z.partBB = append(z.partBB, bb)
	}
	nets := map[string]bool{}
	for _, t := range z.ts {
		if t.Kind == "route" && !z.skipNet[t.Net] {
			nets[t.Net] = true
		}
	}
	names := make([]string, 0, len(nets))
	for n := range nets {
		names = append(names, n)
	}
	sort.Strings(names)
	z.straightenFanouts()
	for _, net := range names {
		z.net(net)
	}
	var out []Track
	for i, t := range z.ts {
		if !z.dead[i] {
			out = append(out, t)
		}
	}
	return out, z.vs, z.stats
}

// net runs every pass over one net.
func (z *bfy) net(net string) {
	z.cur = net
	z.splitTees(net)
	z.eachChain(net, z.padEntries)
	z.eachChain(net, z.merge)
	z.eachChain(net, z.sJogs)
	z.eachChain(net, z.merge)
	z.eachChain(net, z.snapLines)
	if !z.fixVias[net] {
		z.snapVias(net)
		z.eachChain(net, z.snapLines)
	}
	z.eachChain(net, z.merge)
	z.dropPadStubs(net)
}

// splitTees cuts a route segment where another track of its net ends on
// it (or a via of the net sits on it) mid-span: the tee becomes a vertex
// of degree three, an anchor no pass moves.
func (z *bfy) splitTees(net string) {
	var ends []struct {
		p     Point
		layer int // -1: a via (every layer)
	}
	for i, t := range z.ts {
		if !z.dead[i] && t.Net == net {
			ends = append(ends, struct {
				p     Point
				layer int
			}{t.A, t.Layer}, struct {
				p     Point
				layer int
			}{t.B, t.Layer})
		}
	}
	for _, v := range z.vs {
		if v.Net == net {
			ends = append(ends, struct {
				p     Point
				layer int
			}{v.C, -1})
		}
	}
	for i := 0; i < len(z.ts); i++ {
		t := z.ts[i]
		if z.dead[i] || t.Net != net || t.Kind != "route" {
			continue
		}
		for _, e := range ends {
			if e.layer >= 0 && e.layer != t.Layer || e.p.Dist(t.A) < 0.05 || e.p.Dist(t.B) < 0.05 {
				continue
			}
			if PointSegDist(e.p, t.A, t.B) > 0.02 {
				continue
			}
			x := segNearest(e.p, t.A, t.B)
			if x.Dist(t.A) < 0.05 || x.Dist(t.B) < 0.05 {
				continue
			}
			x = e.p // keep the tee point exact
			z.dead[i] = true
			a, b := t, t
			a.B, b.A = x, x
			z.ts = append(z.ts, a, b)
			z.dead = append(z.dead, false, false)
			break
		}
	}
}

// dropPadStubs removes route pieces left wholly inside one own pad with a
// dead end there (a T-junction moved to the pad centre leaves the old stub
// from the centre to the access node): the pad copper already joins them.
func (z *bfy) dropPadStubs(net string) {
	for pass := 0; pass < 4; pass++ {
		deg := map[[2]int64]int{}
		key := func(l int, p Point) [2]int64 {
			k := bk(p)
			return [2]int64{k.x*64 + int64(l), k.y}
		}
		for i, t := range z.ts {
			if !z.dead[i] && t.Net == net {
				deg[key(t.Layer, t.A)]++
				deg[key(t.Layer, t.B)]++
			}
		}
		for _, v := range z.vs {
			if v.Net == net {
				for _, l := range z.stLayers() {
					deg[key(l, v.C)] += 2
				}
			}
		}
		changed := false
		for i, t := range z.ts {
			if z.dead[i] || t.Net != net || t.Kind != "route" {
				continue
			}
			pa, pb := z.pix.at(t.A, t.Layer, net), z.pix.at(t.B, t.Layer, net)
			if pa == nil || pa != pb || !inPad(pa, t.A, -0.01) || !inPad(pa, t.B, -0.01) {
				continue
			}
			if deg[key(t.Layer, t.A)] == 1 && t.A.Dist(pa.Box.C) > 0.05 || deg[key(t.Layer, t.B)] == 1 && t.B.Dist(pa.Box.C) > 0.05 ||
				deg[key(t.Layer, t.A)] == 1 && deg[key(t.Layer, t.B)] == 1 {
				z.dead[i] = true
				changed = true
				deg[key(t.Layer, t.A)]--
				deg[key(t.Layer, t.B)]--
			}
		}
		if !changed {
			return
		}
	}
}

func (z *bfy) stLayers() []int {
	var out []int
	if z.st != nil {
		for _, l := range z.st.Stack {
			out = append(out, l.ID)
		}
	}
	return out
}

// ---- fan-out stubs ----------------------------------------------------------

// straightenFanouts moves a fan-out via whose stub leaves its pad at a skew
// onto one of the pad centre's eight octilinear rays — the axis away from
// the part body first, the least displacement next — never farther from the
// pad than it was (the stub length is part of the decap / hot loops), only
// into room the routed copper leaves (exact check), and inside every plane
// region that held it. A via other copper hangs on (shared stubs, escapes,
// routed bridges) and the pads with several fan-out vias (current arrays)
// stay. Done after routing so the routing itself is unchanged.
func (z *bfy) straightenFanouts() {
	n0 := len(z.ts)
	for ti := 0; ti < n0; ti++ {
		t := z.ts[ti]
		if z.dead[ti] || t.Kind != "fanout" || z.skipNet[t.Net] || odir(t.A, t.B) >= 0 {
			continue
		}
		pd := z.pix.at(t.A, t.Layer, t.Net)
		if pd == nil || pd.Box.C.Dist(t.A) > 0.05 {
			continue
		}
		vi := -1
		for i, v := range z.vs {
			if v.Net == t.Net && bk(v.C) == bk(t.B) {
				if vi >= 0 {
					vi = -2
					break
				}
				vi = i
			}
		}
		if vi < 0 {
			continue
		}
		hang, fromPad := 0, 0
		for i, o := range z.ts {
			if z.dead[i] || o.Net != t.Net {
				continue
			}
			if bk(o.A) == bk(t.B) || bk(o.B) == bk(t.B) {
				hang++
			}
			if o.Kind == "fanout" && o.Layer == t.Layer && bk(o.A) == bk(t.A) {
				fromPad++
			}
		}
		if hang != 1 || fromPad != 1 {
			continue
		}
		C, V := pd.Box.C, t.B
		old := C.Dist(V)
		var away Point
		if p := z.b.Part(pd.Part); p != nil {
			away = C.Sub(p.Body().Center())
		}
		al := math.Hypot(away.X, away.Y)
		type cand struct {
			c Point
			s float64
		}
		var cs []cand
		square := math.Abs(pd.Box.W-pd.Box.H) < t.Width
		for k := 0; k < 8; k++ {
			u := dunit(k)
			rank := 0.0
			if k%2 == 1 {
				rank = 2
				if square {
					rank = 20 // out of a square pad's corner: last resort
				}
			}
			if al > 0 {
				dot := (u.X*away.X + u.Y*away.Y) / al
				switch {
				case dot < -0.3:
					rank += 8
				case dot < 0.7 && k%2 == 0:
					rank += 1
				}
			}
			for d := math.Floor(old*2) / 2; d > 0; d -= 0.5 {
				c := C.Add(u.Scale(d))
				c = Point{math.Round(c.X*1000) / 1000, math.Round(c.Y*1000) / 1000}
				if pd.Box.Dist(c) == 0 {
					break
				}
				cs = append(cs, cand{c, 10*rank + c.Dist(V)})
			}
		}
		sort.SliceStable(cs, func(i, j int) bool { return cs[i].s < cs[j].s })
		tried := 0
		for _, cd := range cs {
			if tried >= 40 {
				break
			}
			if !z.inSamePlanes(t.Net, V, cd.c, z.vs[vi].Dia/2) {
				continue
			}
			tried++
			nt := t
			nt.B = cd.c
			if !z.okEdit(t.Net, map[int]bool{ti: true}, nil, []Track{nt}, vi, cd.c) {
				z.stats.Rejected++
				continue
			}
			z.dead[ti] = true
			z.ts = append(z.ts, nt)
			z.dead = append(z.dead, false)
			z.vs[vi].C = cd.c
			z.stats.Fanouts++
			break
		}
	}
}

// inSamePlanes: a via moved from a to b stays inside every plane / pour
// region of its net that held it, at least as far from the region edge as
// it was (or a via radius).
func (z *bfy) inSamePlanes(net string, a, b Point, rad float64) bool {
	for _, pr := range z.planes {
		if pr.Net != net {
			continue
		}
		for _, poly := range pr.Polys {
			if len(poly) < 3 || !PolyContains(poly, a) {
				continue
			}
			if !PolyContains(poly, b) || PolyEdgeDist(poly, b) < math.Min(PolyEdgeDist(poly, a), rad) {
				return false
			}
		}
	}
	return true
}

// ---- chains ---------------------------------------------------------------

// bchain is a run of route segments of one net on one layer through free
// vertices (degree 2, no via, not a pad centre); its ends are anchors.
type bchain struct {
	net   string
	layer int
	pts   []Point
	w     []float64 // w[i] is the width of segment pts[i]→pts[i+1]
	tix   []int     // the tracks the chain was built from
	dirty bool
}

func (c *bchain) seg(i int) Track {
	return Track{Net: c.net, Layer: c.layer, A: c.pts[i], B: c.pts[i+1], Width: c.w[i], Kind: "route"}
}

func (c *bchain) length() float64 {
	l := 0.0
	for i := 1; i < len(c.pts); i++ {
		l += c.pts[i].Dist(c.pts[i-1])
	}
	return l
}

type bkey struct{ x, y int64 }

func bk(p Point) bkey { return bkey{int64(math.Round(p.X / 0.02)), int64(math.Round(p.Y / 0.02))} }

// eachChain builds the chains of net (layer by layer), runs fn on each and
// commits the changed ones.
func (z *bfy) eachChain(net string, fn func(*bchain)) {
	layers := map[int]bool{}
	for i, t := range z.ts {
		if !z.dead[i] && t.Net == net && t.Kind == "route" {
			layers[t.Layer] = true
		}
	}
	ls := make([]int, 0, len(layers))
	for l := range layers {
		ls = append(ls, l)
	}
	sort.Ints(ls)
	for _, l := range ls {
		for _, c := range z.chains(net, l) {
			j0 := z.chainJogs(c, c.pts, c.w)
			p0 := append([]Point(nil), c.pts...)
			fn(c)
			if j := z.chainJogs(c, c.pts, c.w); j > j0 {
				z.dbg("JOGS UP %s L%d %d→%d: %v → %v", net, l, j0, j, p0, c.pts)
			}
			if c.dirty {
				z.commit(c)
			}
		}
	}
}

// chains extracts the route chains of net on layer.
func (z *bfy) chains(net string, layer int) []*bchain {
	deg := map[bkey]int{}
	route := map[bkey][]int{}
	for i, t := range z.ts {
		if z.dead[i] || t.Net != net || t.Layer != layer || t.A.Dist(t.B) < 1e-6 {
			continue
		}
		deg[bk(t.A)]++
		deg[bk(t.B)]++
		if t.Kind == "route" {
			route[bk(t.A)] = append(route[bk(t.A)], i)
			route[bk(t.B)] = append(route[bk(t.B)], i)
		}
	}
	via := map[bkey]bool{}
	for _, v := range z.vs {
		if v.Net == net {
			via[bk(v.C)] = true
		}
	}
	anchor := func(p Point) bool {
		k := bk(p)
		if deg[k] != 2 || len(route[k]) != 2 || via[k] {
			return true
		}
		pd := z.pix.at(p, layer, net)
		return pd != nil && pd.Box.C.Dist(p) < 0.05
	}
	used := map[int]bool{}
	var out []*bchain
	walk := func(start Point, first int) *bchain {
		c := &bchain{net: net, layer: layer, pts: []Point{start}}
		cur, ti := start, first
		for {
			t := z.ts[ti]
			used[ti] = true
			nxt := t.B
			if bk(t.B) == bk(cur) {
				nxt = t.A
			}
			c.pts = append(c.pts, nxt)
			c.w = append(c.w, t.Width)
			c.tix = append(c.tix, ti)
			if anchor(nxt) {
				return c
			}
			next := -1
			for _, j := range route[bk(nxt)] {
				if !used[j] {
					next = j
				}
			}
			if next < 0 {
				return c
			}
			cur, ti = nxt, next
		}
	}
	keys := make([]bkey, 0, len(route))
	for k := range route {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].x != keys[j].x {
			return keys[i].x < keys[j].x
		}
		return keys[i].y < keys[j].y
	})
	for _, k := range keys {
		for _, ti := range route[k] {
			if used[ti] {
				continue
			}
			t := z.ts[ti]
			p := t.A
			if bk(t.B) == k {
				p = t.B
			}
			if !anchor(p) {
				continue
			}
			out = append(out, walk(p, ti))
		}
	}
	// Loops without an anchor are left alone.
	return out
}

// commit replaces the chain's tracks with its current geometry.
func (z *bfy) commit(c *bchain) {
	for _, i := range c.tix {
		z.dead[i] = true
	}
	var tix []int
	for i := 0; i+1 < len(c.pts); i++ {
		if c.pts[i].Dist(c.pts[i+1]) < 1e-6 {
			continue
		}
		z.ts = append(z.ts, c.seg(i))
		z.dead = append(z.dead, false)
		tix = append(tix, len(z.ts)-1)
	}
	c.tix, c.dirty = tix, false
}

// ---- octilinear geometry -------------------------------------------------

// odir is the octilinear direction index (dirs8) of a→b, or -1.
func odir(a, b Point) int {
	if a.Dist(b) < 1e-6 {
		return -1
	}
	ang := angDeg(a, b)
	k := int(math.Round(ang/45)) % 8
	if math.Abs(ang-45*math.Round(ang/45)) > aesAngTol {
		return -1
	}
	return k
}

func dunit(k int) Point {
	u := Point{float64(dirs8[k][0]), float64(dirs8[k][1])}
	return u.Scale(1 / math.Hypot(u.X, u.Y))
}

// meet intersects the line through p along direction kp with the line
// through q along kq (false when parallel).
func meet(p Point, kp int, q Point, kq int) (Point, bool) {
	if kp < 0 || kq < 0 || kp%4 == kq%4 {
		return Point{}, false
	}
	u, v := dunit(kp), dunit(kq)
	den := u.X*v.Y - u.Y*v.X
	w := q.Sub(p)
	t := (w.X*v.Y - w.Y*v.X) / den
	return p.Add(u.Scale(t)), true
}

// along reports b−a pointing along direction k with length ≥ min.
func along(a, b Point, k int, min float64) bool {
	d := b.Sub(a)
	u := dunit(k)
	l := d.X*u.X + d.Y*u.Y
	return l >= min && math.Abs(d.X*u.Y-d.Y*u.X) < 0.01
}

// turnOK: the corner from direction a into direction b is a 45° bend or
// none (the router's own corners are all 45°: a new 90° corner would be a
// regression of R4 and an acid trap).
func turnOK(a, b int) bool {
	t := (b - a + 8) % 8
	return t <= 1 || t >= 7
}

// ---- the exact check ------------------------------------------------------

// ok reports that the chain c with segments [from, to) replaced by add (and
// via vi moved to nv when vi ≥ 0) passes the exact DRC around the change
// and does not run over its own net.
func (z *bfy) ok(c *bchain, from, to int, add []Track, vi int, nv Point) bool {
	return z.okMulti([]*bchain{c}, [][2]int{{from, to}}, [][]Track{add}, vi, nv)
}

func (z *bfy) okMulti(cs []*bchain, rng [][2]int, adds [][]Track, vi int, nv Point) bool {
	var add []Track
	for _, a := range adds {
		add = append(add, a...)
	}
	skip := map[int]bool{}
	var keep []Track
	for ci, c := range cs {
		for _, i := range c.tix {
			skip[i] = true
		}
		for i := 0; i+1 < len(c.pts); i++ {
			if i < rng[ci][0] || i >= rng[ci][1] {
				keep = append(keep, c.seg(i))
			}
		}
	}
	return z.okEdit(cs[0].net, skip, keep, add, vi, nv)
}

// okEdit judges an edit of net's copper: the tracks in skip removed, keep
// and add placed (add is the new copper) and via vi moved to nv (vi < 0:
// none) — the exact DRC around it with a 0.05 mil margin, and no new copper
// running over its own net away from a shared end or a tee.
func (z *bfy) okEdit(net string, skip map[int]bool, keep, add []Track, vi int, nv Point) bool {
	win := EmptyRect()
	for _, t := range add {
		win = win.AddPoint(t.A).AddPoint(t.B)
	}
	if vi >= 0 {
		win = win.AddPoint(nv).AddPoint(z.vs[vi].C)
	}
	if win.Empty() {
		return true
	}
	if len(z.zones) > 0 {
		keep := 2 * z.b.Rules.Clearance
		for _, t := range add {
			for _, zn := range z.zones {
				if !EmptyRect().AddPoint(t.A).AddPoint(t.B).Expand(t.Width/2 + keep).Overlaps(PolyBounds(zn)) {
					continue
				}
				n := int(math.Ceil(t.A.Dist(t.B)/5)) + 1
				for k := 0; k <= n; k++ {
					p := t.A.Add(t.B.Sub(t.A).Scale(float64(k) / float64(n)))
					if PolyContains(zn, p) || PolyEdgeDist(zn, p) < keep+t.Width/2 {
						z.dbg("    refused: near an isolation band / slot")
						return false
					}
				}
			}
		}
		if vi >= 0 {
			for _, zn := range z.zones {
				if PolyContains(zn, nv) || PolyEdgeDist(zn, nv) < keep+z.vs[vi].Dia/2 {
					return false
				}
			}
		}
	}
	win = win.Expand(z.margin)
	segBB := func(t Track) Rect { return EmptyRect().AddPoint(t.A).AddPoint(t.B).Expand(t.Width / 2) }
	var ts []Track
	for i, t := range z.ts {
		if z.dead[i] || skip[i] || !segBB(t).Overlaps(win) {
			continue
		}
		ts = append(ts, t)
	}
	for _, t := range keep {
		if segBB(t).Overlaps(win) {
			ts = append(ts, t)
		}
	}
	// Own-net crossings: a new segment may touch its own copper only where
	// they share an end, and never runs back along it.
	for _, a := range add {
		for _, t := range ts {
			if t.Net != net || t.Layer != a.Layer {
				continue
			}
			if SegSegDist(a.A, a.B, t.A, t.B) > 0.01 {
				continue
			}
			// A tee: one end lies on the other segment.
			tee := false
			for _, p := range []Point{a.A, a.B} {
				if PointSegDist(p, t.A, t.B) < 0.05 && p.Dist(t.A) > 0.05 && p.Dist(t.B) > 0.05 {
					tee = true
				}
			}
			for _, q := range []Point{t.A, t.B} {
				if PointSegDist(q, a.A, a.B) < 0.05 && q.Dist(a.A) > 0.05 && q.Dist(a.B) > 0.05 {
					tee = true
				}
			}
			if tee {
				continue
			}
			shared := false
			for _, p := range []Point{a.A, a.B} {
				for _, q := range []Point{t.A, t.B} {
					if p.Dist(q) < 0.05 {
						shared = true
						// running back along it: same direction from the shared end
						oa, ot := a.A, t.A
						if p.Dist(a.A) < 0.05 {
							oa = a.B
						}
						if q.Dist(t.A) < 0.05 {
							ot = t.B
						}
						da, dt := oa.Sub(p), ot.Sub(q)
						la, lt := math.Hypot(da.X, da.Y), math.Hypot(dt.X, dt.Y)
						if la > 1e-6 && lt > 1e-6 && (da.X*dt.X+da.Y*dt.Y)/(la*lt) > 0.9998 {
							z.dbg("    refused: runs back along own net at %.1f,%.1f", p.X, p.Y)
							return false
						}
					}
				}
			}
			if !shared {
				// Crossing inside an own pad is one copper area anyway.
				x := segNearest(segNearest(a.A, t.A, t.B), a.A, a.B)
				if pd := z.pix.at(x, a.Layer, net); pd != nil && inPad(pd, x, -0.01) {
					continue
				}
				z.dbg("    refused: crosses own net at %.1f,%.1f (%v-%v kind %s)", a.A.X, a.A.Y, t.A, t.B, t.Kind)
				return false
			}
		}
	}
	first := len(ts)
	ts = append(ts, add...)
	var vs []Via
	moved := -1
	for i, v := range z.vs {
		if i == vi {
			v.C = nv
			moved = len(vs)
		}
		if !(Rect{v.C.X, v.C.Y, v.C.X, v.C.Y}).Expand(v.Dia / 2).Overlaps(win) {
			if i == vi {
				moved = -1
			}
			continue
		}
		vs = append(vs, v)
	}
	sub := *z.b
	sub.Parts = nil
	for i, p := range z.b.Parts {
		if z.partBB[i].Overlaps(win) {
			sub.Parts = append(sub.Parts, p)
		}
	}
	rep := checkDRCTol(&sub, z.an, z.st, ts, vs, -0.05)
	for _, v := range rep.Violations {
		for _, r := range []drcRef{v.ra, v.rb} {
			if r.kind == 1 && r.idx >= first || r.kind == 2 && moved >= 0 && r.idx == moved {
				z.dbg("    refused: %s %s/%s L%d at %.1f,%.1f gap %.2f < %.2f", v.Kind, v.NetA, v.NetB, v.Layer, v.At.X, v.At.Y, v.Gap, v.Required)
				return false
			}
		}
	}
	return true
}

// lenOK: a change from old to new length is allowed. A net an electrical
// item measures never gets longer (nor any net in the strict retry); a
// plain signal may grow by max(4 mil, 10 %) of the piece replaced.
func (z *bfy) lenOK(old, nw float64) bool {
	if z.strict || z.sensitive[z.cur] {
		return nw <= old+0.01
	}
	return nw <= old+math.Max(4, 0.10*old)
}

// ---- pad entry ------------------------------------------------------------

// padEntries rebuilds the pad entries at both ends of c.
func (z *bfy) padEntries(c *bchain) {
	z.padEntry(c)
	reverse(c)
	z.padEntry(c)
	reverse(c)
}

func reverse(c *bchain) {
	for i, j := 0, len(c.pts)-1; i < j; i, j = i+1, j-1 {
		c.pts[i], c.pts[j] = c.pts[j], c.pts[i]
	}
	for i, j := 0, len(c.w)-1; i < j; i, j = i+1, j-1 {
		c.w[i], c.w[j] = c.w[j], c.w[i]
	}
}

// entryGood reports the current entry from pad pd as already fine: the
// first segment leaving the pad starts at the centre and runs along a pad
// axis (or at 45° out of a non-square pad).
func entryGood(pd *Pad, pts []Point) bool {
	for i := 0; i+1 < len(pts); i++ {
		if inPad(pd, pts[i+1], -0.01) {
			continue
		}
		if pts[i].Dist(pd.Box.C) > 0.05 {
			return false
		}
		rel := math.Mod(angDeg(pts[i], pts[i+1])-pd.Box.Rot+720, 90)
		dev := math.Min(rel, 90-rel)
		if dev < aesAngTol {
			return true
		}
		return math.Abs(dev-45) < aesAngTol && math.Abs(pd.Box.W-pd.Box.H) > 1 && odir(pts[i], pts[i+1]) >= 0
	}
	return true
}

// padEntry rebuilds the start of c when it begins on a pad: pad centre →
// Q along a pad axis (45° as second choice) → an existing vertex pts[k],
// all octilinear, no acute corner, Q outside the pad copper.
func (z *bfy) padEntry(c *bchain) {
	if len(c.pts) < 2 {
		return
	}
	p0 := c.pts[0]
	pd := z.pix.at(p0, c.layer, c.net)
	if pd == nil {
		return
	}
	for _, v := range z.vs {
		if v.Net == c.net && bk(v.C) == bk(p0) {
			return // a via in the pad (thermal / fan-out) anchors the start
		}
	}
	C := pd.Box.C
	if p0.Dist(C) > 0.05 && !inPad(pd, p0, -0.01) {
		return
	}
	round := pd.Box.Round && math.Abs(pd.Box.W-pd.Box.H) < 0.5
	if round || entryGood(pd, c.pts) {
		// Round pads accept any direction; still straighten a skewed stub.
		ok := true
		for i := 0; i+1 < len(c.pts); i++ {
			if odir(c.pts[i], c.pts[i+1]) < 0 {
				ok = false
			}
		}
		if ok && (round || p0.Dist(C) < 0.05) {
			return
		}
	}
	rot := math.Mod(pd.Box.Rot+720, 45)
	if math.Min(rot, 45-rot) > 0.01 {
		return // pad not on an octilinear rotation
	}
	// Candidate entry directions: the pad axes first (the long axis away
	// from the part body best), then the pad diagonals.
	type edir struct {
		k    int
		rank float64
	}
	var away Point
	if p := z.b.Part(pd.Part); p != nil {
		away = C.Sub(p.Body().Center())
	}
	al := math.Hypot(away.X, away.Y)
	long := pd.Box.Rot
	if pd.Box.H > pd.Box.W {
		long += 90
	}
	var eds []edir
	for k := 0; k < 8; k++ {
		ang := float64(k) * 45
		rel := math.Mod(ang-pd.Box.Rot+720, 90)
		axial := math.Min(rel, 90-rel) < 0.01
		lr := math.Mod(ang-long+720, 180)
		isLong := math.Min(lr, 180-lr) < 0.01
		dot := 1.0
		if al > 0 {
			u := dunit(k)
			dot = (u.X*away.X + u.Y*away.Y) / al
		}
		// Rank: along an axis away from the part body first (a gull-wing
		// pin's long axis, a chip passive's part axis), then the long axis
		// sideways, the short axis sideways, the pad diagonals; never into
		// the body unless nothing else fits.
		rank := 0.0
		switch {
		case !axial && round:
			rank = 0
		case !axial && math.Abs(pd.Box.W-pd.Box.H) < c.w[0]:
			continue // a square pad's diagonal leaves by a corner
		case !axial:
			rank = 4
		case dot > 0.7:
			rank = 0
		case isLong:
			rank = 1
		default:
			rank = 2
		}
		if dot < -0.3 {
			rank += 8 // into the part body
		} else if !axial && dot < 0.3 {
			rank += 1
		}
		eds = append(eds, edir{k, rank})
	}
	// Distance from the centre to the pad edge along direction k.
	exit := func(k int) float64 {
		u := dunit(k)
		t := 0.0
		for t < 400 && pd.Box.Dist(C.Add(u.Scale(t))) == 0 {
			t += 0.25
		}
		return t
	}
	type cand struct {
		k   int     // the new prefix rejoins the chain at pts[k]
		pts []Point // C, …, pts[k]
		w   []float64
		s   float64
	}
	var cs []cand
	isBend90 := func(a, b int) bool { return (b-a+8)%8 == 2 || (b-a+8)%8 == 6 }
	add := func(k int, pts []Point, w []float64, rank float64) {
		ol, nl := 0.0, 0.0
		for i := 1; i <= k; i++ {
			ol += c.pts[i].Dist(c.pts[i-1])
		}
		for i := 1; i < len(pts); i++ {
			nl += pts[i].Dist(pts[i-1])
		}
		for i := 0; i+1 < len(pts); i++ {
			if i > 0 && pd.Box.SegDist(pts[i], pts[i+1]) == 0 {
				return // a later leg runs back over the pad
			}
		}
		bends := 0.0
		for i := 1; i+1 < len(pts); i++ {
			if isBend90(odir(pts[i-1], pts[i]), odir(pts[i], pts[i+1])) {
				bends += 1.5
			}
		}
		cs = append(cs, cand{k, pts, w, rank + bends + 0.2*(nl-ol) - 3*float64(k) + 0.5*float64(len(pts)-2)})
	}
	maxK := min(len(c.pts)-1, 5)
	for k := 1; k <= maxK; k++ {
		pk := c.pts[k]
		// (1)/(2) rejoin at pts[k] itself: it must lie off the pad;
		// (3)/(4) only use its segment's line.
		fixedOK := !inPad(pd, pk, 0.5)
		next := -1
		if k+1 < len(c.pts) {
			if next = odir(pk, c.pts[k+1]); next < 0 {
				continue
			}
		}
		// widths: the stub keeps the first width, the rest the widest piece
		// it replaces (never narrower).
		w0, wr := c.w[0], 0.0
		for i := 1; i < k; i++ {
			wr = math.Max(wr, c.w[i])
		}
		if wr == 0 {
			wr = w0
		}
		for _, e := range eds {
			u := dunit(e.k)
			te := exit(e.k)
			// (1) straight from the centre to pts[k]
			if fixedOK && along(C, pk, e.k, te+0.5) && (next < 0 || turnOK(e.k, next)) {
				add(k, []Point{C, pk}, []float64{w0}, e.rank)
			}
			// (2) out along e, then one leg to pts[k]
			for v := 0; v < 8 && fixedOK; v++ {
				if v == e.k || !turnOK(e.k, v) || next >= 0 && !turnOK(v, next) {
					continue
				}
				q, okm := meet(C, e.k, pk, v)
				if !okm {
					continue
				}
				d := q.Sub(C)
				if t := d.X*u.X + d.Y*u.Y; t < te+0.5 || !along(q, pk, v, 0.5) {
					continue
				}
				w1 := wr
				if k == 1 {
					w1 = w0
				}
				add(k, []Point{C, q, pk}, []float64{w0, w1}, e.rank)
			}
			if next < 0 {
				continue
			}
			// pts[k] is a free vertex: it may slide along its next segment.
			pn := c.pts[k+1]
			wn := c.w[k]
			// (3) the entry ray meets the next segment's line directly
			if x, okm := meet(C, e.k, pk, next); okm && turnOK(e.k, next) &&
				along(C, x, e.k, te+0.5) && along(x, pn, next, 0.5) {
				add(k+1, []Point{C, x, pn}, []float64{w0, wn}, e.rank)
			}
			// (4) a short exit along e just past the pad edge, then one leg
			// onto the next segment's line
			q := C.Add(u.Scale(te + math.Max(1, w0/2)))
			for v := 0; v < 8; v++ {
				if v == e.k || v == next || !turnOK(e.k, v) || !turnOK(v, next) {
					continue
				}
				x, okm := meet(q, v, pk, next)
				if !okm || !along(q, x, v, 0.5) || !along(x, pn, next, 0.5) {
					continue
				}
				add(k+1, []Point{C, q, x, pn}, []float64{w0, math.Max(wr, w0), wn}, e.rank+0.5)
			}
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].s < cs[j].s })
	z.dbg("entry %s %s L%d: %d candidates (chain %d pts, start off-centre %.2f)", pd.Key(), c.net, c.layer, len(cs), len(c.pts), p0.Dist(C))
	tried := 0
	for _, cd := range cs {
		if tried >= 24 {
			break
		}
		ol := 0.0
		for i := 1; i <= cd.k; i++ {
			ol += c.pts[i].Dist(c.pts[i-1])
		}
		nl := 0.0
		for i := 1; i < len(cd.pts); i++ {
			nl += cd.pts[i].Dist(cd.pts[i-1])
		}
		if !z.lenOK(ol, nl) {
			z.dbg("  len k=%d %.1f → %.1f %v", cd.k, ol, nl, cd.pts)
			continue
		}
		var add []Track
		for i := 0; i+1 < len(cd.pts); i++ {
			add = append(add, Track{Net: c.net, Layer: c.layer, A: cd.pts[i], B: cd.pts[i+1], Width: cd.w[i], Kind: "route"})
		}
		np := append(append([]Point(nil), cd.pts...), c.pts[cd.k+1:]...)
		nw := append(append([]float64(nil), cd.w...), c.w[cd.k:]...)
		if z.chainJogs(c, np, nw) > z.chainJogs(c, c.pts, c.w) {
			z.dbg("  jog k=%d %v", cd.k, cd.pts)
			continue
		}
		tried++
		z.dbg("  try k=%d %v w=%v", cd.k, cd.pts, cd.w)
		if !z.ok(c, 0, cd.k, add, -1, Point{}) {
			z.stats.Rejected++
			continue
		}
		c.pts = append(append([]Point(nil), cd.pts...), c.pts[cd.k+1:]...)
		c.w = append(append([]float64(nil), cd.w...), c.w[cd.k:]...)
		c.dirty = true
		z.stats.PadEntries++
		return
	}
}

// chainJogs counts the S-jogs of a polyline by the R3 definition: segment m
// with its neighbours running the same way and shifted sideways by less
// than four track widths, or m shorter than 25 mil.
func (z *bfy) chainJogs(c *bchain, pts []Point, w []float64) int {
	n := len(pts) - 1
	k := 0
	for m := 1; m+1 < n; m++ {
		// R3 only sees bends off pads (a corner under pad copper is hidden).
		if z.pix.at(pts[m], c.layer, c.net) != nil || z.pix.at(pts[m+1], c.layer, c.net) != nil {
			continue
		}
		a, b, c := odir(pts[m-1], pts[m]), odir(pts[m], pts[m+1]), odir(pts[m+1], pts[m+2])
		if a < 0 || b < 0 || a != c || a == b || w[m-1] != w[m] || w[m] != w[m+1] {
			continue // (a width change inside is an intent neck, not a jog)
		}
		u := dunit(a)
		j := pts[m+1].Sub(pts[m])
		off := math.Abs(j.X*u.Y - j.Y*u.X)
		if off < 4*math.Max(w[m], w[m-1]) || pts[m].Dist(pts[m+1]) < 25 {
			k++
		}
	}
	return k
}

// ---- collinear merge ------------------------------------------------------

func (z *bfy) merge(c *bchain) {
	for i := 1; i+1 < len(c.pts); {
		a, b := odir(c.pts[i-1], c.pts[i]), odir(c.pts[i], c.pts[i+1])
		if a < 0 || a != b {
			i++
			continue
		}
		w := math.Max(c.w[i-1], c.w[i])
		if c.w[i-1] != c.w[i] {
			// Widen the narrower piece — never into a pad narrower than the
			// track, never on a pad-anchored piece (a neck at a pad is
			// electrical).
			if z.pix.at(c.pts[i-1], c.layer, c.net) != nil || z.pix.at(c.pts[i+1], c.layer, c.net) != nil {
				i++
				continue
			}
		}
		add := []Track{{Net: c.net, Layer: c.layer, A: c.pts[i-1], B: c.pts[i+1], Width: w, Kind: "route"}}
		if !z.ok(c, i-1, i+1, add, -1, Point{}) {
			z.stats.Rejected++
			i++
			continue
		}
		if c.w[i-1] != c.w[i] {
			z.stats.Widened++
		}
		z.stats.Merged++
		c.pts = append(c.pts[:i], c.pts[i+1:]...)
		c.w = append(append(c.w[:i-1], w), c.w[i+1:]...)
		c.dirty = true
	}
}

// ---- S-jogs ----------------------------------------------------------------

// sJogs puts the two parallel legs of an S-jog on one line: segment m is
// the jog when segments m−1 and m+1 run the same way and m shifts them
// sideways by less than four track widths (or is shorter than 25 mil, the
// R3 definition).
func (z *bfy) sJogs(c *bchain) {
	for pass := 0; pass < 8; pass++ {
		if !z.sJog(c) {
			return
		}
	}
}

func (z *bfy) sJog(c *bchain) bool {
	n := len(c.pts) - 1 // segments 0..n-1
	d := make([]int, n)
	for i := 0; i < n; i++ {
		d[i] = odir(c.pts[i], c.pts[i+1])
	}
	for m := 1; m+1 < n; m++ {
		if d[m-1] < 0 || d[m] < 0 || d[m+1] < 0 || d[m-1] != d[m+1] || d[m] == d[m-1] {
			continue
		}
		if c.w[m-1] != c.w[m] || c.w[m] != c.w[m+1] {
			continue // an intent neck transition
		}
		u := dunit(d[m-1])
		j := c.pts[m+1].Sub(c.pts[m])
		off := math.Abs(j.X*u.Y - j.Y*u.X)
		ml := c.pts[m].Dist(c.pts[m+1])
		if !(off < 4*c.w[m] || ml < 25) {
			continue
		}
		z.dbg("jog %s L%d at %.1f,%.1f off %.1f: m=%d n=%d", c.net, c.layer, c.pts[m].X, c.pts[m].Y, off, m, n)
		type opt struct {
			from, to int
			pts      []Point
			w        []float64
		}
		var opts []opt
		// A: leg m−1 onto the line of leg m+1; pts[m−1] slides along
		// segment m−2.
		if m >= 2 && d[m-2] >= 0 {
			if q, ok := meet(c.pts[m-2], d[m-2], c.pts[m+2], d[m+1]); ok &&
				along(c.pts[m-2], q, d[m-2], 0.5) && along(q, c.pts[m+2], d[m+1], 0.5) {
				opts = append(opts, opt{m - 2, m + 2, []Point{c.pts[m-2], q, c.pts[m+2]}, []float64{c.w[m-2], c.w[m+1]}})
			}
		}
		// B: leg m+1 onto the line of leg m−1; pts[m+2] slides along
		// segment m+2.
		if m+2 < n && d[m+2] >= 0 {
			if q, ok := meet(c.pts[m-1], d[m-1], c.pts[m+3], d[m+2]); ok &&
				along(c.pts[m-1], q, d[m-1], 0.5) && along(q, c.pts[m+3], d[m+2], 0.5) {
				opts = append(opts, opt{m - 1, m + 3, []Point{c.pts[m-1], q, c.pts[m+3]}, []float64{c.w[m-1], c.w[m+2]}})
			}
		}
		// The variant that moves the shorter leg first.
		if len(opts) == 2 && c.pts[m-1].Dist(c.pts[m]) > c.pts[m+1].Dist(c.pts[m+2]) {
			opts[0], opts[1] = opts[1], opts[0]
		}
		// C: both legs onto a line between them (each moves part of the
		// offset: one side alone may not have the room).
		if m >= 2 && m+2 < n && d[m-2] >= 0 && d[m+2] >= 0 {
			for _, f := range []float64{0.5, 0.25, 0.75} {
				p := c.pts[m].Add(c.pts[m+1].Sub(c.pts[m]).Scale(f))
				q1, ok1 := meet(c.pts[m-2], d[m-2], p, d[m-1])
				q2, ok2 := meet(p, d[m+1], c.pts[m+3], d[m+2])
				if ok1 && ok2 && along(c.pts[m-2], q1, d[m-2], 0.5) && along(q1, q2, d[m-1], 0.5) && along(q2, c.pts[m+3], d[m+2], 0.5) {
					opts = append(opts, opt{m - 2, m + 3, []Point{c.pts[m-2], q1, q2, c.pts[m+3]}, []float64{c.w[m-2], c.w[m-1], c.w[m+2]}})
				}
			}
		}
		for _, o := range opts {
			if !turnOKAt(c, o.from, o.to, o.pts) {
				continue
			}
			ol, nl := 0.0, 0.0
			for i := o.from; i < o.to; i++ {
				ol += c.pts[i].Dist(c.pts[i+1])
			}
			for i := 1; i < len(o.pts); i++ {
				nl += o.pts[i].Dist(o.pts[i-1])
			}
			if !z.lenOK(ol, nl) {
				continue
			}
			var add []Track
			for i := 0; i+1 < len(o.pts); i++ {
				add = append(add, Track{Net: c.net, Layer: c.layer, A: o.pts[i], B: o.pts[i+1], Width: o.w[i], Kind: "route"})
			}
			if !z.ok(c, o.from, o.to, add, -1, Point{}) {
				z.dbg("  jog option %d..%d refused by the exact check", o.from, o.to)
				z.stats.Rejected++
				continue
			}
			c.pts = append(append(append([]Point(nil), c.pts[:o.from]...), o.pts...), c.pts[o.to+1:]...)
			c.w = append(append(append([]float64(nil), c.w[:o.from]...), o.w...), c.w[o.to:]...)
			c.dirty = true
			z.stats.SJogs++
			return true
		}
	}
	return false
}

// turnOKAt checks that replacing pts[from..to] by np keeps every corner
// at the joins non-acute.
func turnOKAt(c *bchain, from, to int, np []Point) bool {
	full := append(append(append([]Point(nil), c.pts[:from]...), np...), c.pts[to+1:]...)
	lo, hi := max(from-1, 0), min(from+len(np), len(full)-2)
	for i := lo; i+2 <= hi+1 && i+2 < len(full); i++ {
		a, b := odir(full[i], full[i+1]), odir(full[i+1], full[i+2])
		if a >= 0 && b >= 0 && !turnOK(a, b) {
			return false
		}
	}
	return true
}

// ---- grid landing -----------------------------------------------------------

// gridShift is the perpendicular move that puts the line through p along
// direction k on the design grid (H: y ∈ 5ℤ, V: x ∈ 5ℤ, diagonals: x∓y ∈ 5ℤ).
func gridShift(p Point, k int) Point {
	g := beautifyGrid
	r := func(v float64) float64 { return g*math.Round(v/g) - v }
	switch k % 4 {
	case 0: // horizontal
		return Point{0, r(p.Y)}
	case 2: // vertical
		return Point{r(p.X), 0}
	case 1: // (1,1): y − x constant
		dv := r(p.Y - p.X)
		return Point{-dv / 2, dv / 2}
	default: // (−1,1): y + x constant
		dv := r(p.Y + p.X)
		return Point{dv / 2, dv / 2}
	}
}

// snapLines moves each free segment of c (both ends free vertices) onto
// the design grid; its end vertices slide along their other segments.
func (z *bfy) snapLines(c *bchain) {
	n := len(c.pts) - 1
	for i := 1; i+1 < n; i++ {
		ka, k, kb := odir(c.pts[i-1], c.pts[i]), odir(c.pts[i], c.pts[i+1]), odir(c.pts[i+1], c.pts[i+2])
		if ka < 0 || k < 0 || kb < 0 {
			continue
		}
		sh := gridShift(c.pts[i], k)
		if math.Hypot(sh.X, sh.Y) < 0.005 {
			continue
		}
		p := c.pts[i].Add(sh)
		a, ok1 := meet(c.pts[i-1], ka, p, k)
		b, ok2 := meet(p, k, c.pts[i+2], kb)
		if !ok1 || !ok2 || a.Dist(c.pts[i]) > 4 || b.Dist(c.pts[i+1]) > 4 {
			z.dbg("snap %s seg %d: geometry (ok %v %v)", c.net, i, ok1, ok2)
			continue
		}
		if !along(c.pts[i-1], a, ka, 0.5) || !along(a, b, k, 0.5) || !along(b, c.pts[i+2], kb, 0.5) {
			z.dbg("snap %s seg %d: a leg too short", c.net, i)
			continue
		}
		np := []Point{c.pts[i-1], a, b, c.pts[i+2]}
		full := append([]Point(nil), c.pts...)
		full[i], full[i+1] = a, b
		if z.chainJogs(c, full, c.w) > z.chainJogs(c, c.pts, c.w) {
			continue // a leg pulled under the S-jog limits
		}
		ol := c.pts[i-1].Dist(c.pts[i]) + c.pts[i].Dist(c.pts[i+1]) + c.pts[i+1].Dist(c.pts[i+2])
		nl := np[0].Dist(np[1]) + np[1].Dist(np[2]) + np[2].Dist(np[3])
		if !z.lenOK(ol, nl) {
			z.dbg("snap %s seg %d: longer %.2f → %.2f", c.net, i, ol, nl)
			continue
		}
		var add []Track
		for j := 0; j < 3; j++ {
			add = append(add, Track{Net: c.net, Layer: c.layer, A: np[j], B: np[j+1], Width: c.w[i-1+j], Kind: "route"})
		}
		if !z.ok(c, i-1, i+2, add, -1, Point{}) {
			z.dbg("snap %s seg %d: exact check", c.net, i)
			z.stats.Rejected++
			continue
		}
		c.pts[i], c.pts[i+1] = a, b
		c.dirty = true
		z.stats.LinesSnapped++
	}
}

// snapVias moves the route vias of net onto the nearest design-grid point
// when every chain ending at the via can follow it (its last segment keeps
// its direction; the vertex before it slides along its other segment).
func (z *bfy) snapVias(net string) {
	for vi := range z.vs {
		v := z.vs[vi]
		if v.Net != net || v.Kind != "route" {
			continue
		}
		g := beautifyGrid
		gp := Point{g * math.Round(v.C.X/g), g * math.Round(v.C.Y/g)}
		if gp.Dist(v.C) < 0.005 || gp.Dist(v.C) > 4 {
			continue
		}
		// Every track at the via must be the end of a route chain.
		k := bk(v.C)
		var cs []*bchain
		rng := [][2]int{}
		var adds [][]Track
		okAll := true
		touch := 0
		for i, t := range z.ts {
			if !z.dead[i] && t.Net == net && (bk(t.A) == k || bk(t.B) == k) {
				touch++
				if t.Kind != "route" {
					okAll = false
				}
			}
		}
		if !okAll || touch == 0 {
			continue
		}
		layers := map[int]bool{}
		for i, t := range z.ts {
			if !z.dead[i] && t.Net == net && (bk(t.A) == k || bk(t.B) == k) {
				layers[t.Layer] = true
			}
		}
		ls := make([]int, 0, len(layers))
		for l := range layers {
			ls = append(ls, l)
		}
		sort.Ints(ls)
		found := 0
		for _, l := range ls {
			for _, c := range z.chains(net, l) {
				end := -1
				if bk(c.pts[0]) == k {
					end = 0
				} else if bk(c.pts[len(c.pts)-1]) == k {
					end = 1
				}
				if end < 0 {
					continue
				}
				if end == 0 {
					reverse(c)
				}
				n := len(c.pts) - 1
				if n < 2 {
					okAll = false
					break
				}
				kl, kp := odir(c.pts[n-1], c.pts[n]), odir(c.pts[n-2], c.pts[n-1])
				if kl < 0 || kp < 0 {
					okAll = false
					break
				}
				q, okm := meet(c.pts[n-2], kp, gp, kl)
				if !okm || !along(c.pts[n-2], q, kp, 0.5) || !along(q, gp, kl, 0.5) {
					okAll = false
					break
				}
				full := append([]Point(nil), c.pts...)
				full[n-1], full[n] = q, gp
				if z.chainJogs(c, full, c.w) > z.chainJogs(c, c.pts, c.w) {
					okAll = false
					break
				}
				cs = append(cs, c)
				rng = append(rng, [2]int{n - 2, n})
				adds = append(adds, []Track{
					{Net: net, Layer: l, A: c.pts[n-2], B: q, Width: c.w[n-2], Kind: "route"},
					{Net: net, Layer: l, A: q, B: gp, Width: c.w[n-1], Kind: "route"}})
				found++
			}
			if !okAll {
				break
			}
		}
		if !okAll || found == 0 {
			continue
		}
		ol, nl := 0.0, 0.0
		for ci, c := range cs {
			n := len(c.pts) - 1
			ol += c.pts[n-2].Dist(c.pts[n-1]) + c.pts[n-1].Dist(c.pts[n])
			nl += adds[ci][0].A.Dist(adds[ci][0].B) + adds[ci][1].A.Dist(adds[ci][1].B)
		}
		if !z.lenOK(ol, nl) {
			continue
		}
		if !z.okMulti(cs, rng, adds, vi, gp) {
			z.stats.Rejected++
			continue
		}
		for ci, c := range cs {
			n := len(c.pts) - 1
			c.pts[n-1], c.pts[n] = adds[ci][0].B, gp
			c.dirty = true
			z.commit(c)
		}
		z.vs[vi].C = gp
		z.stats.ViasSnapped++
	}
}

// ---- the pipeline gate -----------------------------------------------------

// routeFacts are the figures the beautification must not make worse.
type routeFacts struct {
	drc, disconnected, vias, planeOpen, si, iso, blockers int
	completion, electrical                                float64
	items                                                 map[string]float64
}

func measureRoute(b *Board, an *Analysis, c *Circuit, st *Stackup, rr *RouteResult, drc *DRCReport, iso func(*RouteResult) int) routeFacts {
	f := routeFacts{drc: len(drc.Violations), disconnected: len(drc.Disconnected), vias: len(rr.Vias),
		completion: rr.Stats.Completion, items: map[string]float64{}}
	j := Joint(b, an, c, st, rr, drc, JointOptions{PlacementScore: -1})
	f.planeOpen, f.electrical, f.blockers = j.PlaneOpen, j.Groups["electrical"], len(j.Blockers)
	for _, it := range j.Items {
		if it.Group == "electrical" {
			f.items[it.ID] = it.Score
		}
	}
	f.si = len(CheckSI(b, an, st, rr).Findings)
	if iso != nil {
		f.iso = iso(rr)
	}
	return f
}

// worseThan names the first figure of a that is worse than b ("" = none).
func (a routeFacts) worseThan(b routeFacts) string {
	const eps = 1e-9
	switch {
	case a.drc > b.drc:
		return sprintf("DRC %d → %d", b.drc, a.drc)
	case a.disconnected > b.disconnected:
		return sprintf("disconnected %d → %d", b.disconnected, a.disconnected)
	case a.completion < b.completion-eps:
		return sprintf("completion %.1f → %.1f", b.completion, a.completion)
	case a.vias != b.vias:
		return sprintf("vias %d → %d", b.vias, a.vias)
	case a.planeOpen > b.planeOpen:
		return sprintf("plane connections open %d → %d", b.planeOpen, a.planeOpen)
	case a.electrical < b.electrical-eps:
		return sprintf("electrical group %.3f → %.3f", b.electrical, a.electrical)
	case a.si > b.si:
		return sprintf("SI findings %d → %d", b.si, a.si)
	case a.iso > b.iso:
		return sprintf("isolation findings %d → %d", b.iso, a.iso)
	case a.blockers > b.blockers:
		return sprintf("delivery blockers %d → %d", b.blockers, a.blockers)
	}
	ids := make([]string, 0, len(b.items))
	for id := range b.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if a.items[id] < b.items[id]-eps {
			return sprintf("electrical %s %.3f → %.3f", id, b.items[id], a.items[id])
		}
	}
	return ""
}

// beautifyRoute runs the beautification on the pipeline's final routing and
// keeps it only when nothing that ranks above aesthetics gets worse. When
// the whole pass fails the gate, the largest set of nets whose changes pass
// together is searched by halving (each candidate set judged by the full
// gate); then the pass is retried with no length increase anywhere; else
// the routing stays as it was.
func beautifyRoute(b *Board, res *Result, slots []IsoSlot, notes []string, bad []IsoInfeasible) {
	rr, an, st := res.Route, res.Analysis, res.Stackup
	if rr == nil || an == nil || res.DRC == nil {
		return
	}
	var iso func(*RouteResult) int
	if an.Iso != nil && len(an.Iso.Pairs) > 0 {
		iso = func(r *RouteResult) int {
			if rep := isolationReport(b, an, r, slots, notes, bad); rep != nil {
				return len(rep.Findings)
			}
			return 0
		}
	}
	c := Understand(b, an)
	before := measureRoute(b, an, c, st, rr, res.DRC, iso)
	try := func(ts []Track, vs []Via) (*RouteResult, *DRCReport, string) {
		rr2 := *rr
		rr2.Tracks, rr2.Vias = dedupTracks(append([]Track(nil), ts...)), append([]Via(nil), vs...)
		rr2.Notes = append([]string(nil), rr.Notes...)
		wl := 0.0
		for _, t := range rr2.Tracks {
			if t.Kind == "route" {
				wl += t.A.Dist(t.B) / 1000
			}
		}
		rr2.Stats.WireLengthIn = wl
		rr2.Power = powerIntegrity(b, an, st, &rr2)
		drc2 := CheckDRCStrict(b, an, st, rr2.Tracks, rr2.Vias)
		return &rr2, drc2, measureRoute(b, an, c, st, &rr2, drc2, iso).worseThan(before)
	}
	var last *BeautifyStats
	for _, strict := range []bool{false, true} {
		var slotPolys [][]Point
		for _, sl := range slots {
			slotPolys = append(slotPolys, sl.Poly)
		}
		ts, vs, stats := beautify(b, an, c, st, rr, strict, slotPolys...)
		last = stats
		if stats.changes() == 0 {
			stats.Reason = "nothing to change"
			break
		}
		rr2, drc2, why := try(ts, vs)
		kept, total := 0, 0
		if why != "" {
			nets := changedNets(rr, ts, vs)
			total = len(nets)
			keep := gateSearch(nets, func(set []string) bool {
				t, v := composeNets(rr, ts, vs, set)
				_, _, w := try(t, v)
				return w == ""
			})
			if len(keep) > 0 {
				t, v := composeNets(rr, ts, vs, keep)
				rr2, drc2, _ = try(t, v)
				kept = len(keep)
			}
		}
		if why != "" && kept == 0 {
			stats.Reason = why
			continue
		}
		stats.Kept = true
		if why != "" {
			stats.Reason = sprintf("kept the changes of %d of %d nets (the full pass: %s)", kept, total, why)
		}
		rr2.Beautify = stats
		note := sprintf("beautify: %d fan-out vias onto the pad rays, %d pad entries, %d S-jogs, %d collinear merges (%d widened), %d segments and %d vias onto the 5 mil grid; %d candidates refused by the exact check",
			stats.Fanouts, stats.PadEntries, stats.SJogs, stats.Merged, stats.Widened, stats.LinesSnapped, stats.ViasSnapped, stats.Rejected)
		if strict {
			note += " (no length increase)"
		}
		if stats.Reason != "" {
			note += "; " + stats.Reason
		}
		rr2.Notes = append(rr2.Notes, note)
		res.Route, res.DRC = rr2, drc2
		return
	}
	rr.Beautify = last
	if last != nil && last.Reason != "" && last.Reason != "nothing to change" {
		rr.Notes = append(rr.Notes, "beautify: rolled back — "+last.Reason)
	}
}

// changedNets lists the nets whose tracks or vias the pass changed.
func changedNets(rr *RouteResult, ts []Track, vs []Via) []string {
	sig := func(ts []Track, vs []Via) map[string]map[string]int {
		m := map[string]map[string]int{}
		add := func(net, k string) {
			if m[net] == nil {
				m[net] = map[string]int{}
			}
			m[net][k]++
		}
		for _, t := range ts {
			a, b := t.A, t.B
			if b.X < a.X || b.X == a.X && b.Y < a.Y {
				a, b = b, a
			}
			add(t.Net, sprintf("t%d %.3f %.3f %.3f %.3f %.3f %s", t.Layer, a.X, a.Y, b.X, b.Y, t.Width, t.Kind))
		}
		for _, v := range vs {
			add(v.Net, sprintf("v %.3f %.3f %.3f", v.C.X, v.C.Y, v.Dia))
		}
		return m
	}
	o, n := sig(rr.Tracks, rr.Vias), sig(ts, vs)
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]map[string]int{o, n} {
		for net := range m {
			if seen[net] {
				continue
			}
			seen[net] = true
			a, b := o[net], n[net]
			same := len(a) == len(b)
			for k, v := range a {
				if b[k] != v {
					same = false
				}
			}
			if !same {
				out = append(out, net)
			}
		}
	}
	sort.Strings(out)
	return out
}

// composeNets takes the pass's copper for the nets in set and the original
// copper for every other net.
func composeNets(rr *RouteResult, ts []Track, vs []Via, set []string) ([]Track, []Via) {
	in := map[string]bool{}
	for _, n := range set {
		in[n] = true
	}
	var ot []Track
	var ov []Via
	for _, t := range rr.Tracks {
		if !in[t.Net] {
			ot = append(ot, t)
		}
	}
	for _, t := range ts {
		if in[t.Net] {
			ot = append(ot, t)
		}
	}
	for _, v := range rr.Vias {
		if !in[v.Net] {
			ov = append(ov, v)
		}
	}
	for _, v := range vs {
		if in[v.Net] {
			ov = append(ov, v)
		}
	}
	return ot, ov
}

// gateMaxEvals bounds the gate evaluations of one rollback search.
const gateMaxEvals = 24

// gateSearch returns a large subset of nets (the whole set already failed)
// whose changes pass together: halves are searched recursively and their
// survivors recombined.
func gateSearch(nets []string, pass func([]string) bool) []string {
	evals := 0
	var search func(s []string, failed bool) []string
	search = func(s []string, failed bool) []string {
		if len(s) == 0 || evals >= gateMaxEvals {
			return nil
		}
		if !failed {
			evals++
			if pass(s) {
				return s
			}
		}
		if len(s) == 1 {
			return nil
		}
		h := len(s) / 2
		ka, kb := search(s[:h], false), search(s[h:], false)
		if len(ka) > 0 && len(kb) > 0 && evals < gateMaxEvals {
			evals++
			u := append(append([]string(nil), ka...), kb...)
			if pass(u) {
				return u
			}
		}
		if len(kb) > len(ka) {
			return kb
		}
		return ka
	}
	return search(nets, true)
}
