package app

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Single-pad escape stubs (v0.9.2 phase 3). A pad of a fine-pitch IC whose
// channel metrics say it is tight (few directions where a via still fits, or
// a short free run) gets, before routing, a locked short track plus a via: the
// router then starts from a pad that already reaches the other layer instead
// of having to find a corridor between two neighbour pads. Only that small
// piece of copper is locked; every connection is still the router's.

// stubSafetyMil: a via keeps this much more than the clearance (rounding of
// mil values against the DRC's nm geometry).
const stubSafetyMil = 0.25

// escapeStubParams steers the planner (mil).
type escapeStubParams struct {
	// MaxViaDirs: a pad with at most this many via-capable directions is a
	// candidate; MinRunMil: so is a pad whose best free run is shorter.
	MaxViaDirs int
	MinRunMil  float64
	// Refs restricts the ICs (default: every fine-pitch IC).
	Refs []string
	// SkipNets are never stubbed (plane nets reach their layer anyway).
	SkipNets []string
	// ReachMil is the longest stub examined from the pad edge to the via centre.
	ReachMil float64
	// Defaults for nets without intent.
	DefaultWidthMil, DefaultMinMil float64
}

// escapeStub is one planned stub with its via.
type escapeStub struct {
	Pad       string     `json:"pad"` // REF.NUM
	Net       string     `json:"net"`
	Layer     int        `json:"layer"`
	From      [2]float64 `json:"from"`
	Via       [2]float64 `json:"via"`
	WidthMil  float64    `json:"widthMil"`
	IntentMil float64    `json:"intentWidthMil"`
	MinMil    float64    `json:"minWidthMil"`
	ViaDia    float64    `json:"viaDiaMil"`
	ViaDrill  float64    `json:"viaDrillMil"`
	DirDeg    float64    `json:"dirDeg"`
	LenMil    float64    `json:"lenMil"`
	MarginMil float64    `json:"marginMil"` // smallest clearance surplus of the via / stub
	ViaDirs   int        `json:"viaDirsBefore"`
	BestRun   float64    `json:"bestRunBeforeMil"`
	Necked    bool       `json:"necked,omitempty"`
}

type escapeStubSkip struct {
	Pad    string `json:"pad"`
	Net    string `json:"net"`
	Reason string `json:"reason"`
}

type escapeStubPlan struct {
	Params  escapeStubParams `json:"params"`
	Stubs   []escapeStub     `json:"stubs"`
	Skipped []escapeStubSkip `json:"skipped,omitempty"`
}

// planEscapeStubs measures every connected pad of the fine-pitch ICs, picks
// the tight ones and places a stub + via for each, tightest first, treating
// the stubs already placed as copper.
func (env *escapeEnv) planEscapeStubs(nets map[string]prebridgeNet, p escapeStubParams, cp channelParams) escapeStubPlan {
	if p.MaxViaDirs <= 0 {
		p.MaxViaDirs = 2
	}
	if p.ReachMil <= 0 {
		p.ReachMil = 90
	}
	plan := escapeStubPlan{Params: p}
	want := map[string]bool{}
	for _, r := range p.Refs {
		want[strings.TrimSpace(r)] = true
	}
	skipNet := map[string]bool{}
	for _, n := range p.SkipNets {
		skipNet[strings.ToUpper(strings.TrimSpace(n))] = true
	}
	netPads := map[string]int{}
	for _, q := range env.pads {
		if q.Net != "" {
			netPads[q.Net]++
		}
	}
	type cand struct {
		q  pcbPadP
		ch padChannel
		cx float64
		cy float64
	}
	var cands []cand
	for ref, part := range env.denseICs() {
		if len(want) > 0 && !want[ref] {
			continue
		}
		var cx, cy float64
		for _, q := range part {
			cx += q.X / float64(len(part))
			cy += q.Y / float64(len(part))
		}
		for _, q := range part {
			if q.Net == "" || netPads[q.Net] < 2 || skipNet[strings.ToUpper(q.Net)] || q.Layer == pcbLayerMulti {
				continue
			}
			ch := env.channelOf(q, cp)
			if ch.ViaDirs <= p.MaxViaDirs || (p.MinRunMil > 0 && ch.BestRun < p.MinRunMil) {
				cands = append(cands, cand{q, ch, cx, cy})
			}
		}
	}
	netOf := func(q pcbPadP) prebridgeNet {
		n, ok := nets[q.Net]
		if !ok {
			n = prebridgeNet{Width: p.DefaultWidthMil, Min: p.DefaultMinMil}
		}
		if n.Width <= 0 {
			n.Width = p.DefaultWidthMil
		}
		if n.Min <= 0 || n.Min > n.Width {
			n.Min = math.Min(p.DefaultMinMil, n.Width)
		}
		return n
	}
	// Most-constrained first: each round re-measures how many directions every
	// open pad still has (the stubs already placed are copper), places the pad
	// with the fewest, so a pad is not crowded out by a neighbour that had room
	// to spare.
	for len(cands) > 0 {
		bi, bn := -1, 1<<30
		var bst *escapeStub
		var bwhy string
		for i, c := range cands {
			st, nfeas, why := env.stubFor(c.q, c.cx, c.cy, netOf(c.q), p, cp)
			if st == nil {
				// nothing fits any more: report now
				plan.Skipped = append(plan.Skipped, escapeStubSkip{Pad: c.ch.Pad, Net: c.q.Net, Reason: why})
				cands = append(cands[:i], cands[i+1:]...)
				bi = -2
				break
			}
			better := bi == -1 || nfeas < bn ||
				(nfeas == bn && (c.ch.ViaDirs < cands[bi].ch.ViaDirs ||
					(c.ch.ViaDirs == cands[bi].ch.ViaDirs && (c.ch.BestRun < cands[bi].ch.BestRun ||
						(c.ch.BestRun == cands[bi].ch.BestRun && c.ch.Pad < cands[bi].ch.Pad)))))
			if better {
				bi, bn, bst, bwhy = i, nfeas, st, why
			}
		}
		if bi == -2 {
			continue
		}
		_ = bwhy
		c := cands[bi]
		cands = append(cands[:bi], cands[bi+1:]...)
		bst.ViaDirs, bst.BestRun = c.ch.ViaDirs, c.ch.BestRun
		plan.Stubs = append(plan.Stubs, *bst)
		env.planned = append(env.planned, intentEscape{Pad: bst.Pad, Net: bst.Net, Layer: bst.Layer, WidthMil: bst.WidthMil, From: bst.From, To: bst.Via})
		env.vias = append(env.vias, widenVia{Net: bst.Net, X: bst.Via[0], Y: bst.Via[1], Diameter: bst.ViaDia})
	}
	sort.Slice(plan.Stubs, func(i, j int) bool { return plan.Stubs[i].Pad < plan.Stubs[j].Pad })
	sort.Slice(plan.Skipped, func(i, j int) bool { return plan.Skipped[i].Pad < plan.Skipped[j].Pad })
	return plan
}

// stubFor tries the 8 directions and keeps the one with the most clearance
// surplus (shorter stub breaks ties). why != "" = no stub fits.
func (env *escapeEnv) stubFor(q pcbPadP, cx, cy float64, n prebridgeNet, p escapeStubParams, cp channelParams) (*escapeStub, int, string) {
	if env.viaDia <= 0 {
		return nil, 0, "board has no via rule"
	}
	// local view: the gap scans stay cheap on a 1000-pad board
	r := p.ReachMil + env.viaDia + 4*env.clr + 2*math.Max(q.W, q.H) + 60
	env = env.localTo(q.X, q.Y, r)
	hv := env.viaDia / 2
	ref := fmt.Sprintf("%s.%s", q.Designator, q.Number)
	const capMargin = 30.0
	nfeas := 0
	var best *escapeStub
	var bestScore float64
	var last string
	outX, outY := q.X-cx, q.Y-cy
	if l := math.Hypot(outX, outY); l > 0 {
		outX, outY = outX/l, outY/l
	}
	for di, d := range channelDirs {
		edge := padEdgeDist(q, d)
		for l := env.viaDia / 2; l <= p.ReachMil; l += 2 {
			v := [2]float64{q.X + d[0]*(edge+l+hv), q.Y + d[1]*(edge+l+hv)}
			// via: ring clears every foreign copper on all layers
			vg, vwho := env.gap(q.Net, 0, segProbe{v, v})
			if vg < hv+stubSafetyMil {
				last = vwho
				continue
			}
			if who := env.viaAtSMD(q.Net, v); who != "" {
				last = who
				continue
			}
			if who := env.sameNetViaClash(q.Net, v); who != "" {
				last = who
				continue
			}
			a := [2]float64{q.X, q.Y}
			sg, swho := env.lineGap(q.Net, q.Layer, a, v)
			w := math.Floor(math.Min(n.Width, 2*sg)*100) / 100
			if w+specctraEps < n.Min {
				last = swho
				continue
			}
			margin := math.Min(vg-hv, math.Min(sg-w/2, capMargin))
			// prefer clearance surplus, then away from the part body (the
			// stub does not wall in the pads beside it), then short
			score := margin + 2*(d[0]*outX+d[1]*outY) - 0.05*l
			if best == nil || score > bestScore {
				best = &escapeStub{Pad: ref, Net: q.Net, Layer: q.Layer, From: a,
					Via: [2]float64{round3(v[0]), round3(v[1])}, WidthMil: w, IntentMil: n.Width, MinMil: n.Min,
					ViaDia: env.viaDia, ViaDrill: env.viaDrill, DirDeg: float64(di) * 45,
					LenMil:    math.Round(math.Hypot(v[0]-q.X, v[1]-q.Y)*100) / 100,
					MarginMil: math.Round(margin*100) / 100, Necked: w < n.Width-0.011}
				bestScore = score
			}
			nfeas++
			break // shortest feasible stub in this direction
		}
	}
	if best == nil {
		if last == "" {
			last = "no free corridor"
		}
		return nil, 0, fmt.Sprintf("no stub+via fits within %.0f mil in any of 8 directions (last blocked by %s)", p.ReachMil, last)
	}
	return best, nfeas, ""
}

// sameNetViaClash: an own-net via closer than hole-to-hole minimum (0.25 mm
// between drills) or one via diameter centre to centre.
func (env *escapeEnv) sameNetViaClash(net string, v [2]float64) string {
	need := math.Max(env.viaDia, env.viaDrill+9.85)
	for _, o := range env.vias {
		if strings.EqualFold(o.Net, net) && math.Hypot(o.X-v[0], o.Y-v[1]) < need {
			return "via " + o.Net + " (same net, hole to hole)"
		}
	}
	return ""
}

// escapeStubOps turns a plan into bridge.py edit ops: locked stub tracks and
// locked vias.
func escapeStubOps(plan escapeStubPlan) map[string]any {
	tracks := make([]map[string]any, 0, len(plan.Stubs))
	vias := make([]map[string]any, 0, len(plan.Stubs))
	for _, s := range plan.Stubs {
		tracks = append(tracks, map[string]any{"net": s.Net, "layer": s.Layer, "locked": true,
			"startX": s.From[0], "startY": s.From[1], "endX": s.Via[0], "endY": s.Via[1], "lineWidth": s.WidthMil})
		vias = append(vias, map[string]any{"net": s.Net, "x": s.Via[0], "y": s.Via[1], "diameter": s.ViaDia, "drill": s.ViaDrill, "locked": true})
	}
	return map[string]any{"addTracks": tracks, "addVias": vias}
}

// localTo is env reduced to the copper within r of (x, y).
func (env *escapeEnv) localTo(x, y, r float64) *escapeEnv {
	in := func(x0, y0, x1, y1 float64) bool {
		return math.Max(x0, x1) >= x-r && math.Min(x0, x1) <= x+r && math.Max(y0, y1) >= y-r && math.Min(y0, y1) <= y+r
	}
	out := &escapeEnv{court: env.court, clr: env.clr, margin: env.margin, netClr: env.netClr, viaDia: env.viaDia, viaDrill: env.viaDrill}
	for _, q := range env.pads {
		if in(q.X-q.W/2, q.Y-q.H/2, q.X+q.W/2, q.Y+q.H/2) {
			out.pads = append(out.pads, q)
		}
	}
	for _, t := range env.tracks {
		if in(t.X1-t.Width, t.Y1-t.Width, t.X2+t.Width, t.Y2+t.Width) {
			out.tracks = append(out.tracks, t)
		}
	}
	for _, v := range env.vias {
		if in(v.X-v.Diameter, v.Y-v.Diameter, v.X+v.Diameter, v.Y+v.Diameter) {
			out.vias = append(out.vias, v)
		}
	}
	for _, e := range env.planned {
		if in(e.From[0]-e.WidthMil, e.From[1]-e.WidthMil, e.To[0]+e.WidthMil, e.To[1]+e.WidthMil) {
			out.planned = append(out.planned, e)
		}
	}
	return out
}
