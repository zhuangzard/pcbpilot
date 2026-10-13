package app

// pcb_power_stubs.go — jointly solved stub + via for power pads of fine-pitch
// parts (default on in `kicad route`, --no-power-stubs disables).
//
// A 0.4 mm QFN pad cannot take its net's full intent width out of the
// footprint (PicoRick U303, +3V3 / +1V1 at 15.75 mil), and the sequential
// escape planner (pcb_intent_escapes.go) places one pad's escape at a time,
// so a via it takes can block a neighbour. Here every such pad of the part
// is solved TOGETHER: for each pad the legal via sites within the pad-escape
// zone are enumerated with the DRC's geometry (pad rectangles, track
// capsules, via discs, the router margin), adjacent same-net pads of one row
// may share one via, and a small depth-first search picks the assignment that
// blocks the fewest straight signal exits, then has the shortest stubs. The
// result is written as fixed copper (stub at the net's widthMil.min, via at
// the board via size) before routing. A pad with no legal site is reported
// geometry-blocked, never silently skipped. No gate is changed: the stub is
// a pad escape in the sense of the intent-widths gate (same zone as the
// sequential planner).
//
// Evidence (PicoRick, direct fastroute, In2 as a +3V3 plane): 13 unrouted ->
// 1 with these stubs + vias on every U303 power pad (see docs in the pcbpilot
// skill, references/pcb-routing.md).

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// stubFinePitchMil is the pad pitch (centre to centre of neighbouring pads of one
// part) up to which a part counts as fine pitch: 0.5 mm.
const stubFinePitchMil = 0.5 / 0.0254

// Via-at-own-pad modes.
const (
	viaAtPadOff   = "off"    // the existing planner's rule: clear of own-net SMD pads too
	viaAtPadTouch = "touch"  // may touch an own-net pad, never overlap its copper
	viaAtPadIn    = "in-pad" // may sit on the own pad (via-in-pad: fab must plug/cap it)
)

type powerStubOpts struct {
	Disabled bool
	// ViaAtPad: off (default) | touch | in-pad.
	ViaAtPad string
}

const (
	stubGridMil    = 1.0  // via site grid
	stubTopSites   = 40   // sites kept per group for the joint search
	stubHoleGapMil = 9.85 // 0.25 mm hole-to-hole between vias
	stubSigEscMil  = 55.0 // a signal pad's straight exit that a via must not block
	stubSigWMil    = 5.9  // ... drawn this wide
	stubNodeBudget = 300000
)

// powerStub is one stub of the plan (pad -> via site), reported in the summary.
type powerStub struct {
	Pad      string     `json:"pad"`
	Net      string     `json:"net"`
	WidthMil float64    `json:"widthMil"`
	LenMil   float64    `json:"lengthMil"`
	Via      [2]float64 `json:"viaMil"`
	// SharedWith: the neighbouring pad that shares this via.
	SharedWith string `json:"sharedWith,omitempty"`
	// BlockedExits: signal pads whose straight exit this via / stub takes.
	BlockedExits int `json:"blockedSignalExits"`
}

// powerStubReport is summary.powerStubs.
type powerStubReport struct {
	Mode            string      `json:"viaAtPad"`
	Candidates      int         `json:"candidatePads"`
	Planned         []powerStub `json:"planned"`
	Vias            int         `json:"vias"`
	GeometryBlocked []string    `json:"geometryBlocked,omitempty"`
	// Sequential: pads the joint solve found no site for that the sequential
	// escape planner then served (no via).
	Sequential []string `json:"sequentialFallback,omitempty"`
}

type stubSite struct {
	v    [2]float64
	segs [][2][2]float64 // per member: stub a -> v
	blk  int
	tot  float64
	nsig map[string]bool
}

type stubGroup struct {
	members []escapeCand
	sites   []*stubSite
}

func (g *stubGroup) net() string { return g.members[0].pad.Net }

// finePitchPad: pad of an IC-like part (>= 5 pads) whose nearest neighbour
// pad centre is within 0.5 mm.
func finePitchPad(pad pcbPadP, part []pcbPadP) bool {
	if len(part) < 5 {
		return false
	}
	best := math.Inf(1)
	for _, q := range part {
		if q.Number == pad.Number || q.Layer != pad.Layer {
			continue
		}
		best = math.Min(best, math.Hypot(q.X-pad.X, q.Y-pad.Y))
	}
	return best <= stubFinePitchMil+0.01
}

// padPart is pad's part's pads.
func partsOf(pads []pcbPadP) map[string][]pcbPadP {
	m := map[string][]pcbPadP{}
	for _, q := range pads {
		m[q.Designator] = append(m[q.Designator], q)
	}
	return m
}

// planPowerStubs plans the joint stub + via for the candidate pads of
// fine-pitch parts. cands are the pads whose net's full width does not leave
// the pad (escapeCandidates). The planned stubs are appended to env.planned /
// env.vias (obstacles for what is planned next); pads without a legal site
// are returned in the report as geometry-blocked and left to the sequential
// planner.
func planPowerStubs(env *escapeEnv, cands []escapeCand, o powerStubOpts) (esc []intentEscape, rep powerStubReport) {
	rep.Mode = o.ViaAtPad
	if rep.Mode == "" {
		rep.Mode = viaAtPadOff
	}
	if o.Disabled || env.viaDia <= 0 {
		return nil, rep
	}
	parts := partsOf(env.pads)
	var fine []escapeCand
	for _, c := range cands {
		if finePitchPad(c.pad, parts[c.pad.Designator]) {
			fine = append(fine, c)
		}
	}
	rep.Candidates = len(fine)
	if len(fine) == 0 {
		return nil, rep
	}
	inSet := map[string]bool{}
	for _, c := range fine {
		inSet[c.key()] = true
	}
	// Groups: a pad and its adjacent same-net row mate (both candidates) share a via.
	var groups []*stubGroup
	used := map[string]bool{}
	for _, c := range fine {
		if used[c.key()] {
			continue
		}
		used[c.key()] = true
		g := &stubGroup{members: []escapeCand{c}}
		if m := c.mate; m != nil {
			k := m.Designator + "." + m.Number
			if inSet[k] && !used[k] && c.mateReach(m) {
				for _, d := range fine {
					if d.key() == k {
						g.members = append(g.members, d)
						used[k] = true
						break
					}
				}
			}
		}
		groups = append(groups, g)
	}
	for _, g := range groups {
		g.sites = env.stubSites(g, o, parts, inSet)
	}
	chosen, unsolved := solveStubGroups(env, groups)
	for _, g := range unsolved {
		for _, m := range g.members {
			rep.GeometryBlocked = append(rep.GeometryBlocked, fmt.Sprintf("%s (%s): no legal stub+via site within %.1f mil of the pad (via %.2f mil, stub %.2f mil)",
				m.key(), m.pad.Net, padEscapeReachMil(m.full), env.viaDia, m.min))
		}
	}
	sort.Strings(rep.GeometryBlocked)
	for _, g := range groups {
		s := chosen[g]
		if s == nil {
			continue
		}
		for i, m := range g.members {
			a := s.segs[i][0]
			e := intentEscape{Pad: m.key(), Kind: "plane-stub", Net: m.pad.Net, Layer: m.pad.Layer, WidthMil: m.min, FullMil: m.full, MinMil: m.min,
				From: a, To: s.v, LimitedBy: "joint stub+via solve"}
			if i == 0 {
				e.Via, e.ViaDiaMil, e.ViaDrillMil = true, env.viaDia, env.viaDrill
			}
			esc = append(esc, e)
			ps := powerStub{Pad: e.Pad, Net: e.Net, WidthMil: round3(e.WidthMil), LenMil: round3(math.Hypot(a[0]-s.v[0], a[1]-s.v[1])), Via: s.v, BlockedExits: s.blk}
			if len(g.members) > 1 {
				ps.SharedWith = g.members[1-i].key()
			}
			rep.Planned = append(rep.Planned, ps)
		}
		rep.Vias++
	}
	for _, e := range esc {
		env.planned = append(env.planned, e)
		if e.Via {
			env.vias = append(env.vias, widenVia{Net: e.Net, X: e.To[0], Y: e.To[1], Diameter: e.ViaDiaMil})
		}
	}
	return esc, rep
}

// stubStart is where a stub leaves pad towards v: up to 0.3 mm from the pad
// centre, pulled back until it is inside the pad copper (>= 2 mil from v when
// the pad allows, so the wire has a length).
func stubStart(pad pcbPadP, v [2]float64) [2]float64 {
	c := [2]float64{pad.X, pad.Y}
	d := math.Hypot(v[0]-c[0], v[1]-c[1])
	inside := func(p [2]float64) bool {
		return rectPtDist(pad.X-pad.W/2, pad.Y-pad.H/2, pad.X+pad.W/2, pad.Y+pad.H/2, p[0], p[1]) == 0
	}
	if d < 1e-6 {
		return c
	}
	for t := math.Min(0.3/0.0254, d); t > 0; t -= 0.25 {
		p := [2]float64{round3(c[0] + (v[0]-c[0])*t/d), round3(c[1] + (v[1]-c[1])*t/d)}
		if inside(p) {
			if math.Hypot(p[0]-v[0], p[1]-v[1]) >= 2 {
				return p
			}
			// via on the pad: start 2 mil back along the pad's long axis
			return stubBack(pad, v)
		}
	}
	return c
}

func stubBack(pad pcbPadP, v [2]float64) [2]float64 {
	if pad.W >= pad.H {
		return [2]float64{round3(v[0] - math.Copysign(2, v[0]-pad.X+1e-9)), v[1]}
	}
	return [2]float64{v[0], round3(v[1] - math.Copysign(2, v[1]-pad.Y+1e-9))}
}

// stubSites enumerates the legal via sites of one group, best first.
func (env *escapeEnv) stubSites(g *stubGroup, o powerStubOpts, parts map[string][]pcbPadP, inSet map[string]bool) []*stubSite {
	first := g.members[0]
	reach := padEscapeReachMil(first.full)
	r := reach + math.Max(first.pad.W, first.pad.H)/2 + 1
	local := env.near(first)
	// signal pads whose straight exit a via must not take
	var sig []pcbPadP
	for _, q := range parts[first.pad.Designator] {
		if q.Net == "" || strings.EqualFold(q.Net, first.pad.Net) || inSet[q.Designator+"."+q.Number] {
			continue
		}
		sig = append(sig, q)
	}
	var out []*stubSite
	n := int(r / stubGridMil)
	for i := -n; i <= n; i++ {
		for j := -n; j <= n; j++ {
			v := [2]float64{round3(first.pad.X + float64(i)*stubGridMil), round3(first.pad.Y + float64(j)*stubGridMil)}
			s := &stubSite{v: v, nsig: map[string]bool{}}
			ok := true
			for _, m := range g.members {
				if !local.siteOK(m, v, o, s) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			for _, q := range sig {
				dir, _, okd := outward(q, parts[q.Designator])
				if !okd {
					continue
				}
				a := [2]float64{q.X, q.Y}
				b := [2]float64{q.X + dir[0]*stubSigEscMil, q.Y + dir[1]*stubSigEscMil}
				hit := segPointDist(a[0], a[1], b[0], b[1], v[0], v[1]) < env.viaDia/2+stubSigWMil/2+env.pairClr(first.pad.Net, q.Net)
				for _, sg := range s.segs {
					hit = hit || segSegDist(a[0], a[1], b[0], b[1], sg[0][0], sg[0][1], sg[1][0], sg[1][1]) < first.min/2+stubSigWMil/2+env.pairClr(first.pad.Net, q.Net)
				}
				if hit {
					s.nsig[q.Designator+"."+q.Number] = true
				}
			}
			s.blk = len(s.nsig)
			for _, sg := range s.segs {
				s.tot += math.Hypot(sg[0][0]-sg[1][0], sg[0][1]-sg[1][1])
			}
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].blk != out[b].blk {
			return out[a].blk < out[b].blk
		}
		if math.Abs(out[a].tot-out[b].tot) > 1e-9 {
			return out[a].tot < out[b].tot
		}
		if out[a].v[0] != out[b].v[0] {
			return out[a].v[0] < out[b].v[0]
		}
		return out[a].v[1] < out[b].v[1]
	})
	if len(out) > stubTopSites {
		// thin the tail: keep the best half, then every k-th of the rest
		keep := out[:stubTopSites/2]
		rest := out[stubTopSites/2:]
		step := len(rest) / (stubTopSites / 2)
		if step < 1 {
			step = 1
		}
		for k := 0; k < len(rest) && len(keep) < stubTopSites; k += step {
			keep = append(keep, rest[k])
		}
		out = keep
	}
	return out
}

// siteOK: via at v and the stub of member m (width widthMil.min) clear every
// other net's copper, v is inside m's pad-escape zone, and v respects the
// own-pad rule of the mode. The stub is appended to s.segs.
func (env *escapeEnv) siteOK(m escapeCand, v [2]float64, o powerStubOpts, s *stubSite) bool {
	pad := m.pad
	dpad := rectPtDist(pad.X-pad.W/2, pad.Y-pad.H/2, pad.X+pad.W/2, pad.Y+pad.H/2, v[0], v[1])
	if dpad > padEscapeReachMil(m.full)-1 {
		return false
	}
	if g, _ := env.gap(pad.Net, 0, segProbe{v, v}); g < env.viaDia/2 {
		return false
	}
	for _, q := range env.pads {
		if q.Layer == pcbLayerMulti || !strings.EqualFold(q.Net, pad.Net) {
			continue
		}
		d := rectPtDist(q.X-q.W/2, q.Y-q.H/2, q.X+q.W/2, q.Y+q.H/2, v[0], v[1])
		switch o.ViaAtPad {
		case viaAtPadIn:
		case viaAtPadTouch:
			if d < env.viaDia/2 { // never on the copper of an own-net pad
				return false
			}
		default:
			if d-env.viaDia/2 < env.pairClr(pad.Net, pad.Net) {
				return false
			}
		}
	}
	a := stubStart(pad, v)
	if g, _ := env.lineGap(pad.Net, pad.Layer, a, v); g < m.min/2 {
		return false
	}
	s.segs = append(s.segs, [2][2]float64{a, v})
	return true
}

// stubCompat: two chosen groups' vias and stubs clear each other.
func (env *escapeEnv) stubCompat(ga *stubGroup, a *stubSite, gb *stubGroup, b *stubSite) bool {
	same := strings.EqualFold(ga.net(), gb.net())
	dv := math.Hypot(a.v[0]-b.v[0], a.v[1]-b.v[1])
	if same {
		return dv >= env.viaDrill+stubHoleGapMil
	}
	clr := env.pairClr(ga.net(), gb.net())
	if dv < env.viaDia+clr {
		return false
	}
	for i, sg := range b.segs {
		if segPointDist(sg[0][0], sg[0][1], sg[1][0], sg[1][1], a.v[0], a.v[1]) < env.viaDia/2+gb.members[i].min/2+clr {
			return false
		}
	}
	for i, sg := range a.segs {
		if segPointDist(sg[0][0], sg[0][1], sg[1][0], sg[1][1], b.v[0], b.v[1]) < env.viaDia/2+ga.members[i].min/2+clr {
			return false
		}
	}
	for i, sa := range a.segs {
		for j, sb := range b.segs {
			if ga.members[i].pad.Layer != gb.members[j].pad.Layer {
				continue
			}
			if segSegDist(sa[0][0], sa[0][1], sa[1][0], sa[1][1], sb[0][0], sb[0][1], sb[1][0], sb[1][1]) < ga.members[i].min/2+gb.members[j].min/2+clr {
				return false
			}
		}
	}
	return true
}

// solveStubGroups picks one site per group: no incompatible pair, then the
// fewest blocked signal exits, then the shortest stubs. When no complete
// assignment exists the group with the fewest sites is dropped (reported as
// unsolved) and the rest solved again. Deterministic.
func solveStubGroups(env *escapeEnv, groups []*stubGroup) (map[*stubGroup]*stubSite, []*stubGroup) {
	active := append([]*stubGroup(nil), groups...)
	var unsolved []*stubGroup
	for {
		var live []*stubGroup
		for _, g := range active {
			if len(g.sites) == 0 {
				unsolved = append(unsolved, g)
			} else {
				live = append(live, g)
			}
		}
		active = live
		if len(active) == 0 {
			return map[*stubGroup]*stubSite{}, unsolved
		}
		if sol := dfsStubs(env, active); sol != nil {
			return sol, unsolved
		}
		worst := 0
		for i, g := range active {
			if len(g.sites) < len(active[worst].sites) {
				worst = i
			}
		}
		unsolved = append(unsolved, active[worst])
		active = append(active[:worst:worst], active[worst+1:]...)
	}
}

func dfsStubs(env *escapeEnv, groups []*stubGroup) map[*stubGroup]*stubSite {
	type cost struct {
		blk int
		tot float64
	}
	best := cost{blk: math.MaxInt32}
	var bestSol map[*stubGroup]*stubSite
	nodes := 0
	dom := map[*stubGroup][]*stubSite{}
	for _, g := range groups {
		dom[g] = g.sites
	}
	chosen := map[*stubGroup]*stubSite{}
	var rec func(dom map[*stubGroup][]*stubSite, c cost)
	rec = func(dom map[*stubGroup][]*stubSite, c cost) {
		nodes++
		if nodes > stubNodeBudget {
			return
		}
		if len(dom) == 0 {
			if c.blk < best.blk || c.blk == best.blk && c.tot < best.tot-1e-9 {
				best = c
				bestSol = map[*stubGroup]*stubSite{}
				for k, v := range chosen {
					bestSol[k] = v
				}
			}
			return
		}
		if bestSol != nil && (c.blk > best.blk || c.blk == best.blk && c.tot >= best.tot-1e-9) {
			return
		}
		var pick *stubGroup
		for _, g := range groups {
			if l, ok := dom[g]; ok && (pick == nil || len(l) < len(dom[pick]) || len(l) == len(dom[pick]) && false) {
				pick = g
			}
		}
		for _, s := range dom[pick] {
			nd := map[*stubGroup][]*stubSite{}
			fail := false
			for h, lst := range dom {
				if h == pick {
					continue
				}
				var f []*stubSite
				for _, d := range lst {
					if env.stubCompat(pick, s, h, d) {
						f = append(f, d)
					}
				}
				if len(f) == 0 {
					fail = true
					break
				}
				nd[h] = f
			}
			if fail {
				continue
			}
			chosen[pick] = s
			rec(nd, cost{c.blk + s.blk, c.tot + s.tot})
			delete(chosen, pick)
			if nodes > stubNodeBudget {
				return
			}
		}
	}
	rec(dom, cost{})
	return bestSol
}

// escapeSkipSet is a skip(pad) predicate over planned pad keys.
func escapeSkipSet(esc []intentEscape) func(pcbPadP) bool {
	have := map[string]bool{}
	for _, e := range esc {
		have[e.Pad] = true
	}
	return func(p pcbPadP) bool { return have[p.Designator+"."+p.Number] }
}
