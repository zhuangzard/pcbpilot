package pcbauto

import (
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
	for _, g := range detectBGAs(r.b) {
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
		placed, missed := 0, 0
		for _, jb := range jobs {
			pd, n := jb.pd, jb.n
			li := gr.layerIndex(pd.Layer)
			if li < 0 || !gr.routable[li] {
				continue
			}
			// Plane balls in the top-escape rings point their via inward: the
			// voids outside them are the signal escape lanes. Outward only if
			// no inward void is left.
			inward := (n.onPlane || n.poured) && g.depth[pd] < rings
			x, y, c, ok := r.dogboneSite(n, pd, li, g, inward, dia)
			if !ok && inward {
				x, y, c, ok = r.dogboneSite(n, pd, li, g, false, dia)
			}
			if !ok {
				missed++
				continue
			}
			stubW := math.Max(r.b.Rules.MinTrack, math.Min(n.width, r.b.Rules.TrackWidth))
			r.commitFanout(n, pd, li, x, y, c, stubW, false, true, drill, dia)
			res.Stats.FanoutVias++
			r.bgaDone[pd] = true
			if !(n.onPlane || n.poured) {
				r.escape[pd] = [2]int{x, y}
			}
			placed++
		}
		res.Notes = append(res.Notes, sprintf("BGA %s: pitch %.1f mil, %d rings escape on top, %d dog-bone vias, %d balls without a legal site",
			g.part.Ref, g.pitch, rings, placed, missed))
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
