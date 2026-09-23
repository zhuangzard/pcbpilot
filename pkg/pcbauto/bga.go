package pcbauto

import (
	"container/heap"
	"math"
	"sort"
)

// BGA dog-bone fan-out.
//
// A ball grid cannot be escaped by a router that has to thread each inner
// ball out between its neighbours on the top layer. The standard practice:
//
//   - the outer ring escapes on the top layer, and as many further rings as
//     tracks fit between two balls (k = ⌊(pitch − ball − clearance)/(track + clearance)⌋);
//   - every deeper signal ball, and every plane ball (ground / supply), drops
//     a via into the void between the balls — the "dog-bone" — pointing away
//     from the package centre, so the vias form a regular half-pitch-offset
//     field with routing channels between them on the inner layers;
//   - fan-out runs innermost ring first (it has the fewest choices).
//
// The site is found, not assumed: among legal grid cells within one pitch of
// the ball on the outward side, the one with the largest clearance to other
// balls (the quad centre on a square grid, the triangle centre on a staggered
// one), then the shortest stub. A signal ball's via becomes one of its access
// nodes on every layer, so the router starts its escape from the via.

// bgaExperiment restricts dog-bones to one kind of ball (diagnostics only).
var bgaExperiment string

type bgaZone struct {
	part string
	box  Rect
}

// bgaPart describes a detected ball-grid package.
type bgaPart struct {
	part   *Part
	pitch  float64 // nearest-neighbour ball distance
	centre Point
	depth  map[*Pad]int // ring from the outside, 0 = outermost
}

// detectBGAs finds ball-grid packages: many identical small SMD pads on a
// regular array.
func detectBGAs(b *Board) []*bgaPart {
	var out []*bgaPart
	for _, p := range b.Parts {
		if len(p.Pads) < 36 {
			continue
		}
		// Identical, roughly square SMD pads.
		w0, h0 := p.Pads[0].Box.W, p.Pads[0].Box.H
		ok := true
		for _, pd := range p.Pads {
			if pd.Layer == LayerMulti || math.Abs(pd.Box.W-w0) > 0.5 || math.Abs(pd.Box.H-h0) > 0.5 || math.Abs(pd.Box.W-pd.Box.H) > 0.25*pd.Box.W {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		// Nearest-neighbour distance (mode, 0.5 mil buckets).
		hist := map[int]int{}
		for i, a := range p.Pads {
			best := math.Inf(1)
			for j, c := range p.Pads {
				if i != j {
					best = math.Min(best, a.Box.C.Dist(c.Box.C))
				}
			}
			hist[int(math.Round(best*2))]++
		}
		mode, cnt := 0, 0
		for k, v := range hist {
			if v > cnt || v == cnt && k < mode {
				mode, cnt = k, v
			}
		}
		pitch := float64(mode) / 2
		if pitch <= 0 || float64(cnt) < 0.8*float64(len(p.Pads)) || pitch > 60 || w0 > 0.7*pitch {
			continue // not a regular fine array (a QFN's pads fail the pitch/size test)
		}
		bb := EmptyRect()
		for _, pd := range p.Pads {
			bb = bb.AddPoint(pd.Box.C)
		}
		if bb.W() < 4*pitch || bb.H() < 4*pitch {
			continue
		}
		g := &bgaPart{part: p, pitch: pitch, centre: bb.Center(), depth: map[*Pad]int{}}
		for _, pd := range p.Pads {
			// Ring = distance to the array's bounding box, in pitches.
			d := math.Min(math.Min(pd.Box.C.X-bb.MinX, bb.MaxX-pd.Box.C.X), math.Min(pd.Box.C.Y-bb.MinY, bb.MaxY-pd.Box.C.Y))
			g.depth[pd] = int(math.Round(d / pitch))
		}
		out = append(out, g)
	}
	return out
}

// topEscapeRings is how many rings leave the array on the top layer: the
// outer ring plus one per track that fits between two balls.
func topEscapeRings(pitch, ball float64, rules Rules) int {
	gap := pitch - ball - rules.Clearance
	k := int(math.Floor(gap / (rules.TrackWidth + rules.Clearance)))
	if k < 0 {
		k = 0
	}
	return 1 + min(k, 2)
}

// bgaFanout places dog-bone vias for every BGA on the board and records the
// signal escapes. It runs before the generic plane fan-out, which then skips
// the balls handled here.
func (r *router) bgaFanout(res *RouteResult) {
	r.bgaDone = map[*Pad]bool{}
	r.escape = map[*Pad][2]int{}
	if !r.opt.BGA {
		return
	}
	gr := r.gr
	bgas := detectBGAs(r.b)
	for _, g := range bgas {
		bb := EmptyRect()
		for _, pd := range g.part.Pads {
			bb = bb.AddPoint(pd.Box.C)
		}
		r.bgaZones = append(r.bgaZones, bgaZone{g.part.Ref, bb.Expand(g.pitch)})
	}
	for _, n := range r.nets {
		n.neck = nil // rebuilt lazily with the zones
	}
	for _, g := range bgas {
		ball := g.part.Pads[0].Box.W
		rings := topEscapeRings(g.pitch, ball, r.b.Rules)
		drill, dia, why := bgaViaClass(g, r.b.Rules)
		if dia <= 0 {
			res.Notes = append(res.Notes, sprintf("BGA %s: %s — dog-bone impossible, needs via-in-pad; left to the generic fan-out", g.part.Ref, why))
			continue
		}
		if why != "" {
			res.Notes = append(res.Notes, sprintf("BGA %s: %s", g.part.Ref, why))
		}
		type job struct {
			pd *Pad
			n  *rnet
		}
		var jobs []job
		for _, pd := range g.part.Pads {
			if pd.Net == "" {
				continue
			}
			n := r.byName[pd.Net]
			if n == nil {
				continue
			}
			plane := n.onPlane || n.poured
			if !plane && (g.depth[pd] < rings || !n.route) {
				continue // escapes on top between the balls
			}
			if bgaExperiment == "signal-only" && plane || bgaExperiment == "plane-only" && !plane {
				continue
			}
			jobs = append(jobs, job{pd, n})
		}
		// Innermost first; stable by key.
		sort.SliceStable(jobs, func(i, j int) bool {
			di, dj := g.depth[jobs[i].pd], g.depth[jobs[j].pd]
			if di != dj {
				return di > dj
			}
			return jobs[i].pd.Key() < jobs[j].pd.Key()
		})
		// Global assignment: every ball competes for the voids around it;
		// a minimum-cost maximum matching gives as many balls as possible a
		// via (greedy left 146 of RK3568 U4's balls without one), then the
		// shortest stubs on the preferred side.
		voids := bgaVoids(g)
		type site struct {
			x, y int
			c    Point
		}
		sites := make([]map[int]site, len(jobs))
		edges := make([][]voidEdge, len(jobs))
		noVoid, noLegal := 0, 0
		for i, jb := range jobs {
			pd, n := jb.pd, jb.n
			sites[i] = map[int]site{}
			li := gr.layerIndex(pd.Layer)
			if li < 0 || !gr.routable[li] {
				continue
			}
			out := pd.Box.C.Sub(g.centre)
			inward := (n.onPlane || n.poured) && g.depth[pd] < rings
			stubW := math.Max(r.b.Rules.MinTrack, math.Min(n.width, r.b.Rules.TrackWidth))
			inRange := 0
			for vi, v := range voids {
				d := v.Dist(pd.Box.C)
				if d > 0.85*g.pitch {
					continue
				}
				inRange++
				cx, cy := gr.cellOf(v)
				found := false
				var st site
				for _, o := range [][2]int{{0, 0}, {1, 0}, {-1, 0}, {0, 1}, {0, -1}, {1, 1}, {-1, -1}, {1, -1}, {-1, 1}} {
					x, y := cx+o[0], cy+o[1]
					if !gr.in(x, y) {
						continue
					}
					c := gr.center(x, y)
					if pd.Box.Dist(c) == 0 || !r.bgaSiteOK(n, x, y, dia) || !r.segmentOK(n, li, pd.Box.C, c, stubW, true) {
						continue
					}
					st, found = site{x, y, c}, true
					break
				}
				if !found {
					continue
				}
				sites[i][vi] = st
				cost := d
				vd := v.Sub(pd.Box.C)
				outward := vd.X*out.X+vd.Y*out.Y >= 0
				if outward == inward {
					cost += 0.5 * g.pitch // wrong side: allowed, not preferred
				}
				edges[i] = append(edges[i], voidEdge{vi, cost})
			}
			if inRange == 0 {
				noVoid++
			} else if len(edges[i]) == 0 {
				noLegal++
			}
		}
		// Signals first: a signal ball without a via cannot escape, a plane
		// ball can still gang to a neighbour. Match signals alone, then plane
		// balls over the voids left.
		match := make([]int, len(jobs))
		for i := range match {
			match[i] = -1
		}
		for _, planePass := range []bool{false, true} {
			var idx []int
			for i, jb := range jobs {
				if (jb.n.onPlane || jb.n.poured) == planePass {
					idx = append(idx, i)
				}
			}
			taken := map[int]bool{}
			for i, m := range match {
				if m >= 0 {
					taken[m] = true
					_ = i
				}
			}
			sub := make([][]voidEdge, len(idx))
			for k, i := range idx {
				for _, e := range edges[i] {
					if !taken[e.v] {
						sub[k] = append(sub[k], e)
					}
				}
			}
			for k, m := range assignVoids(len(idx), len(voids), sub) {
				match[idx[k]] = m
			}
		}
		placed, missed, shared := 0, 0, 0
		viaAt := map[int]*rnet{} // void index → net whose via sits there
		var unmatched []int
		for i, jb := range jobs {
			pd, n := jb.pd, jb.n
			li := gr.layerIndex(pd.Layer)
			st, ok := sites[i][match[i]]
			if match[i] < 0 || !ok || li < 0 {
				unmatched = append(unmatched, i)
				continue
			}
			// Re-check: a neighbour committed first may now crowd this site
			// (staggered arrays put voids closer than a pitch).
			if !r.bgaSiteOK(n, st.x, st.y, dia) {
				unmatched = append(unmatched, i)
				continue
			}
			stubW := math.Max(r.b.Rules.MinTrack, math.Min(n.width, r.b.Rules.TrackWidth))
			r.commitFanout(n, pd, li, st.x, st.y, st.c, stubW, false, true, drill, dia)
			viaAt[match[i]] = n
			res.Stats.FanoutVias++
			r.bgaDone[pd] = true
			if !(n.onPlane || n.poured) {
				r.escape[pd] = [2]int{st.x, st.y}
			}
			placed++
		}
		// Via sharing: a plane ball with no void of its own ties to a
		// neighbouring void already holding a via of its net — standard for
		// ground and supply balls, and the only way a dense array fits (RK3568
		// U4: ~513 balls needing a via, 465 usable voids).
		unPlane, unSignal := 0, 0
		for _, i := range unmatched {
			if jobs[i].n.onPlane || jobs[i].n.poured {
				unPlane++
			} else {
				unSignal++
			}
		}
		shareNoVia, shareStub := 0, 0
		for _, i := range unmatched {
			pd, n := jobs[i].pd, jobs[i].n
			li := gr.layerIndex(pd.Layer)
			done := false
			sawVia := false
			if (n.onPlane || n.poured) && li >= 0 {
				stubW := math.Max(r.b.Rules.MinTrack, math.Min(n.width, r.b.Rules.TrackWidth))
				for vi, v := range voids {
					if viaAt[vi] != n || v.Dist(pd.Box.C) > 0.85*g.pitch {
						continue
					}
					var via Point
					for _, fv := range n.fanVias {
						if fv.C.Dist(v) < gr.g*1.5 {
							via = fv.C
						}
					}
					if via == (Point{}) {
						continue
					}
					sawVia = true
					if !r.gangOK(n, li, pd.Box.C, via, stubW) {
						continue
					}
					cl := dedup(r.claimSegment(n, li, pd.Box.C, via, stubW, nil))
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
					n.shareTracks = append(n.shareTracks, Track{Net: n.name, Layer: pd.Layer, A: pd.Box.C, B: via, Width: stubW, Kind: "fanout"})
					n.shareClaims = append(n.shareClaims, append([]int32(nil), fresh...))
					r.bgaDone[pd] = true
					shared++
					done = true
					break
				}
			}
			if !done {
				missed++
				if n.onPlane || n.poured {
					if sawVia {
						shareStub++
					} else {
						shareNoVia++
					}
				}
			}
		}
		// Ganging: a plane ball still without a via ties straight to an
		// adjacent ball of its net that is already connected — nothing sits
		// between two neighbouring balls. Repeat so chains propagate.
		for progress := true; progress; {
			progress = false
			for _, pd := range g.part.Pads {
				n := r.byName[pd.Net]
				if n == nil || r.bgaDone[pd] || !(n.onPlane || n.poured) {
					continue
				}
				li := gr.layerIndex(pd.Layer)
				if li < 0 {
					continue
				}
				stubW := math.Max(r.b.Rules.MinTrack, math.Min(n.width, r.b.Rules.TrackWidth))
				for _, q := range g.part.Pads {
					if q == pd || q.Net != pd.Net || !r.bgaDone[q] || q.Box.C.Dist(pd.Box.C) > 1.1*g.pitch {
						continue
					}
					if !r.gangOK(n, li, pd.Box.C, q.Box.C, stubW) {
						continue
					}
					cl := dedup(r.claimSegment(n, li, pd.Box.C, q.Box.C, stubW, nil))
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
					n.shareTracks = append(n.shareTracks, Track{Net: n.name, Layer: pd.Layer, A: pd.Box.C, B: q.Box.C, Width: stubW, Kind: "fanout"})
					n.shareClaims = append(n.shareClaims, append([]int32(nil), fresh...))
					r.bgaDone[pd] = true
					shared++
					missed--
					progress = true
					break
				}
			}
		}
		res.Notes = append(res.Notes, sprintf("BGA %s: pitch %.1f mil, %d rings escape on top, %d voids, %d dog-bone vias, %d balls sharing a neighbour's via, %d balls without a legal site (%d with no void in reach, %d with every nearby void blocked; unmatched %d plane / %d signal; share failed: %d no same-net via near, %d stub blocked)",
			g.part.Ref, g.pitch, rings, len(voids), placed, shared, missed, noVoid, noLegal, unPlane, unSignal, shareNoVia, shareStub))
	}
}

// dogboneSite picks the via cell for ball pd: legal (via and stub clear every
// obstacle), on the outward side, maximising clearance to the other balls,
// then the shortest stub.
func (r *router) dogboneSite(n *rnet, pd *Pad, li int, g *bgaPart, inward bool, viaDia float64) (int, int, Point, bool) {
	gr := r.gr
	out := pd.Box.C.Sub(g.centre)
	if out.X == 0 && out.Y == 0 {
		out = Point{1, 1}
	}
	if inward {
		out = out.Scale(-1)
	}
	cx, cy := gr.cellOf(pd.Box.C)
	rc := int(math.Ceil(g.pitch / gr.g))
	stubW := math.Max(r.b.Rules.MinTrack, math.Min(n.width, r.b.Rules.TrackWidth))
	type cand struct {
		x, y  int
		c     Point
		score float64
	}
	var cs []cand
	for dy := -rc; dy <= rc; dy++ {
		for dx := -rc; dx <= rc; dx++ {
			x, y := cx+dx, cy+dy
			if !gr.in(x, y) {
				continue
			}
			c := gr.center(x, y)
			d := c.Dist(pd.Box.C)
			if d > 0.95*g.pitch || pd.Box.Dist(c) == 0 {
				continue
			}
			v := c.Sub(pd.Box.C)
			if v.X*out.X+v.Y*out.Y < 0 {
				continue // wrong side: would eat a neighbour's void or an escape lane
			}
			// Clearance to the nearest other ball.
			clear := math.Inf(1)
			for _, q := range g.part.Pads {
				if q != pd {
					clear = math.Min(clear, q.Box.Dist(c))
				}
			}
			cs = append(cs, cand{x, y, c, -clear + 0.05*d})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].score != cs[j].score {
			return cs[i].score < cs[j].score
		}
		return cs[i].y*gr.W+cs[i].x < cs[j].y*gr.W+cs[j].x
	})
	for k, c := range cs {
		if k > 40 {
			break
		}
		if math.IsInf(r.viaCostR(n, c.x, c.y, viaDia/2+n.share), 1) {
			continue
		}
		if !r.segmentOK(n, li, pd.Box.C, c.c, stubW, true) {
			continue
		}
		return c.x, c.y, c.c, true
	}
	return 0, 0, Point{}, false
}

// JLC via capability: mechanical drill ≥ 0.15 mm, annular ring ≥ 0.05 mm a
// side (so outer diameter ≥ drill + 4 mil).
const (
	jlcMinDrillMil   = 5.9
	jlcMinAnnularMil = 2
)

// bgaVoidDist measures, on interior balls, how far the best via site (the
// point farthest from every ball within one pitch) is from the nearest ball
// centre: pitch/√2 on a square array, pitch/√3 on a staggered one.
func bgaVoidDist(g *bgaPart) float64 {
	var sample []*Pad
	for _, pd := range g.part.Pads {
		if g.depth[pd] >= 2 {
			sample = append(sample, pd)
		}
		if len(sample) >= 12 {
			break
		}
	}
	if len(sample) == 0 {
		return g.pitch / math.Sqrt2
	}
	best := math.Inf(1)
	step := g.pitch / 20
	for _, pd := range sample {
		v := 0.0
		for dy := -g.pitch; dy <= g.pitch; dy += step {
			for dx := -g.pitch; dx <= g.pitch; dx += step {
				c := pd.Box.C.Add(Point{dx, dy})
				m := math.Inf(1)
				for _, q := range g.part.Pads {
					if d := q.Box.C.Dist(c); d < m {
						m = d
					}
				}
				v = math.Max(v, m)
			}
		}
		best = math.Min(best, v)
	}
	return best
}

// bgaViaClass picks the fan-out via for a BGA: the board via if it fits the
// voids, else the largest JLC-manufacturable via that does, else none. why
// explains a change from the board default (for the report).
func bgaViaClass(g *bgaPart, rules Rules) (drill, dia float64, why string) {
	ballR := g.part.Pads[0].Box.W / 2
	void := bgaVoidDist(g)
	maxDia := math.Floor(2*(void-ballR-rules.Clearance)*10) / 10
	// A sparse array measures a large void; a via is still never wider than
	// 80 % of the pitch.
	maxDia = math.Min(maxDia, math.Floor(0.8*g.pitch*10)/10)
	// A via that leaves one routing channel between neighbouring vias: inner
	// layers escape through those channels, so the largest via that merely
	// fits is the wrong choice.
	w := math.Max(rules.MinTrack, 3)
	channel := math.Floor((g.pitch-w-2*rules.Clearance)*10) / 10
	target := math.Min(maxDia, channel)
	minDia := jlcMinDrillMil + 2*jlcMinAnnularMil
	if rules.ViaDia <= target {
		return rules.ViaDrill, rules.ViaDia, ""
	}
	pick := func(d float64, note string) (float64, float64, string) {
		dr := math.Max(jlcMinDrillMil, math.Min(rules.ViaDrill, d-2*jlcMinAnnularMil))
		return dr, d, sprintf("board via %.1f/%.1f mil %s; fan-out uses a BGA via class %.1f/%.1f mil (JLC ≥0.15 mm drill, 0.05 mm ring; 4+ layers) — confirm with the fab",
			rules.ViaDrill, rules.ViaDia, note, dr, d)
	}
	if target >= minDia {
		return pick(target, sprintf("leaves no routing channel between vias at %.1f mil pitch", g.pitch))
	}
	if maxDia >= minDia {
		return pick(maxDia, sprintf("does not fit the %.1f mil voids (no channel between vias even at the process minimum)", void))
	}
	return 0, 0, sprintf("voids %.1f mil from the balls fit a via of at most %.1f mil, below the %.1f mil process minimum", void, maxDia, minDia)
}

// ---- global void assignment -----------------------------------------------

// bgaVoids lists the via sites of a ball array: the circumcentres of empty
// triangles of neighbouring balls (quad centres on a square array, triangle
// centres on a staggered one), plus virtual voids just outside the outer ring.
func bgaVoids(g *bgaPart) []Point {
	pads := g.part.Pads
	nb := make([][]int, len(pads))
	for i := range pads {
		for j := range pads {
			if i != j && pads[i].Box.C.Dist(pads[j].Box.C) <= 1.6*g.pitch {
				nb[i] = append(nb[i], j)
			}
		}
	}
	seen := map[[2]int]bool{}
	var out []Point
	add := func(p Point) {
		k := [2]int{int(math.Round(p.X * 2)), int(math.Round(p.Y * 2))}
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	isNb := func(a, b int) bool {
		for _, x := range nb[a] {
			if x == b {
				return true
			}
		}
		return false
	}
	for a := range pads {
		for _, b := range nb[a] {
			if b <= a {
				continue
			}
			for _, c := range nb[a] {
				if c <= b || !isNb(b, c) {
					continue
				}
				cc, rad, ok := circumcentre(pads[a].Box.C, pads[b].Box.C, pads[c].Box.C)
				if !ok || rad > g.pitch {
					continue
				}
				empty := true
				for _, d := range append(append(append([]int(nil), nb[a]...), nb[b]...), nb[c]...) {
					if d != a && d != b && d != c && pads[d].Box.C.Dist(cc) < rad-0.5 {
						empty = false
						break
					}
				}
				if empty {
					add(cc)
				}
			}
		}
	}
	bb := EmptyRect()
	for _, pd := range pads {
		bb = bb.AddPoint(pd.Box.C)
	}
	h := g.pitch / 2
	for _, pd := range pads {
		if g.depth[pd] != 0 {
			continue
		}
		for _, d := range []Point{{h, h}, {h, -h}, {-h, h}, {-h, -h}} {
			p := pd.Box.C.Add(d)
			if p.X < bb.MinX || p.X > bb.MaxX || p.Y < bb.MinY || p.Y > bb.MaxY {
				add(p)
			}
		}
	}
	return out
}

func circumcentre(a, b, c Point) (Point, float64, bool) {
	d := 2 * (a.X*(b.Y-c.Y) + b.X*(c.Y-a.Y) + c.X*(a.Y-b.Y))
	if math.Abs(d) < 1e-9 {
		return Point{}, 0, false
	}
	a2, b2, c2 := a.X*a.X+a.Y*a.Y, b.X*b.X+b.Y*b.Y, c.X*c.X+c.Y*c.Y
	p := Point{(a2*(b.Y-c.Y) + b2*(c.Y-a.Y) + c2*(a.Y-b.Y)) / d, (a2*(c.X-b.X) + b2*(a.X-c.X) + c2*(b.X-a.X)) / d}
	return p, p.Dist(a), true
}

// assignVoids matches balls to voids with a minimum-cost maximum matching:
// as many balls as possible get a via, then the shortest stubs on the
// preferred side. cost[i] lists (void, cost) edges of ball i.
type voidEdge struct {
	v    int
	cost float64
}

func assignVoids(nBalls, nVoids int, edges [][]voidEdge) []int {
	// Successive shortest paths with potentials on source → balls → voids → sink.
	N := nBalls + nVoids + 2
	src, snk := N-2, N-1
	type arc struct {
		to, rev int
		cap     int
		cost    float64
	}
	g := make([][]arc, N)
	addArc := func(u, v int, cost float64) {
		g[u] = append(g[u], arc{v, len(g[v]), 1, cost})
		g[v] = append(g[v], arc{u, len(g[u]) - 1, 0, -cost})
	}
	for i := 0; i < nBalls; i++ {
		addArc(src, i, 0)
		for _, e := range edges[i] {
			addArc(i, nBalls+e.v, e.cost)
		}
	}
	for v := 0; v < nVoids; v++ {
		addArc(nBalls+v, snk, 0)
	}
	pot := make([]float64, N)
	dist := make([]float64, N)
	prevN := make([]int, N)
	prevA := make([]int, N)
	for {
		for i := range dist {
			dist[i] = math.Inf(1)
		}
		dist[src] = 0
		h := &jointHeap{}
		heap.Push(h, jointItem{node: src})
		for h.Len() > 0 {
			it := heap.Pop(h).(jointItem)
			u := it.node
			if it.cost > dist[u]+1e-9 {
				continue
			}
			for k, a := range g[u] {
				if a.cap <= 0 {
					continue
				}
				nd := dist[u] + a.cost + pot[u] - pot[a.to]
				if nd < dist[a.to]-1e-9 {
					dist[a.to], prevN[a.to], prevA[a.to] = nd, u, k
					heap.Push(h, jointItem{node: a.to, cost: nd})
				}
			}
		}
		if math.IsInf(dist[snk], 1) {
			break
		}
		for i := range pot {
			if !math.IsInf(dist[i], 1) {
				pot[i] += dist[i]
			}
		}
		for v := snk; v != src; v = prevN[v] {
			a := &g[prevN[v]][prevA[v]]
			a.cap--
			g[v][a.rev].cap++
		}
	}
	match := make([]int, nBalls)
	for i := range match {
		match[i] = -1
		for _, a := range g[i] {
			if a.to >= nBalls && a.to < nBalls+nVoids && a.cap == 0 {
				match[i] = a.to - nBalls
			}
		}
	}
	return match
}

// bgaSiteOK is an exact legality test for a BGA via of copper diameter dia at
// cell (x, y): no hard cell (edge, keep-out, hole) under the via on any layer,
// exact clearance to every other net's pad, and no other net's copper
// already claimed within the via. The grid test rounds claims to whole cells
// (±1.4 mil at a 2.8 mil pitch) and refused sites with a real 0.3 mil margin
// in a 0.65 mm ball field; the exact DRC gate still checks the result.
func (r *router) bgaSiteOK(n *rnet, x, y int, dia float64) bool {
	gr := r.gr
	c := gr.center(x, y)
	for l := range gr.layers {
		for _, o := range gr.disk(dia / 2) {
			xx, yy := x+o[0], y+o[1]
			if !gr.in(xx, yy) {
				return false
			}
			j := gr.idx(l, xx, yy)
			if gr.flags[j]&flagHard != 0 {
				return false
			}
			if gr.pad[j] == -1 && gr.use[j] > 0 && !r.ownsCell(n, int32(j)) {
				return false
			}
		}
		if !r.padsClear(n, gr.layers[l], c, dia/2) {
			return false
		}
	}
	return true
}

// ownsCell reports whether n's fixed or routed copper claims cell j.
func (r *router) ownsCell(n *rnet, j int32) bool {
	for _, i := range n.fixed {
		if i == j {
			return true
		}
	}
	return false
}

// gangOK is an exact test for a short same-net tie inside a ball field:
// every sample of the segment keeps the clearance to other nets' pads
// exactly, and crosses no copper claimed by another net. Own copper (the
// neighbour ball, its dog-bone) is not an obstacle.
func (r *router) gangOK(n *rnet, l int, a, b Point, width float64) bool {
	gr := r.gr
	steps := int(math.Ceil(a.Dist(b)/(gr.g/2))) + 1
	own := map[int32]bool{}
	for _, i := range n.fixed {
		own[i] = true
	}
	for s := 0; s <= steps; s++ {
		p := a.Add(b.Sub(a).Scale(float64(s) / float64(steps)))
		if !r.padsClear(n, gr.layers[l], p, width/2) {
			return false
		}
		x, y := gr.cellOf(p)
		if !gr.in(x, y) {
			return false
		}
		for _, o := range gr.disk(width / 2) {
			xx, yy := x+o[0], y+o[1]
			if !gr.in(xx, yy) {
				return false
			}
			j := gr.idx(l, xx, yy)
			if gr.flags[j]&flagHard != 0 {
				return false
			}
			if gr.pad[j] == -1 && gr.use[j] > 0 && !own[int32(j)] {
				return false
			}
		}
	}
	return true
}
