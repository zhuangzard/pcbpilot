package app

// pcb_intent_escapes.go — pre-routed pad escapes for intent nets.
//
// Every intent net class is in fastroute's --no-neckdown-classes, so a
// current-carrying net is routed at its full class width everywhere. A
// full-width trace cannot leave a fine-pitch pad (PicoRick: RP2040 QFN-56,
// 0.4 mm pitch, +3V3 / +1V1 at 15.75 mil: 11 connections blocked). Instead
// of reopening neck-down, each such pad gets a short fixed escape at the
// widest width the pitch allows (never below widthMil.min); the router
// continues at full width from its end:
//
//   - outward: from the pad's outer tip straight out to where a full-width
//     trace fits (fastroute's octagon end cap: checked as a square), or the
//     courtyard exit when it never does;
//   - bridge: to an adjacent same-net pad of the same row, across their inner
//     ends (RP2040 43–44, 48–49), when the outward side is taken;
//   - inward + via: from the inner tip toward the part body, ending in a via
//     (the router continues on any layer), when neither fits.
//
// Every escape stays inside the pad-escape zone the intent-widths gate
// accepts (padEscapeReachMil, the internal router's neck-down zone), so the
// gate and the post-layout sim see it as what it is: a short narrower
// section at the pad. Clearances use the DRC's geometry (pad rectangles,
// track capsules, via discs) plus the router margin.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// padEscapeReachMil is how far from its own pad's copper a track may be
// narrower than its net's width (never below widthMil.min): max(3 widths,
// 30 mil) — the internal router's neck-down zone (pkg/pcbauto router.inNeck)
// and the intent-widths gate's pad-escape rule.
func padEscapeReachMil(widthMil float64) float64 { return math.Max(3*widthMil, 30) }

// intentEscape is one planned pad escape (mil, y-up board frame).
type intentEscape struct {
	Pad      string     `json:"pad"`  // REF.PIN
	Kind     string     `json:"kind"` // outward | bridge | inward-via
	Net      string     `json:"net"`
	Layer    int        `json:"layer"` // pcbpilot layer id (1 top, 2 bottom)
	WidthMil float64    `json:"widthMil"`
	FullMil  float64    `json:"fullMil"`
	MinMil   float64    `json:"minMil"`
	From     [2]float64 `json:"from"`
	To       [2]float64 `json:"to"`
	// Via: a via of ViaDiaMil / ViaDrillMil at To.
	Via         bool    `json:"via,omitempty"`
	ViaDiaMil   float64 `json:"viaDiaMil,omitempty"`
	ViaDrillMil float64 `json:"viaDrillMil,omitempty"`
	// FullFitsAtEnd: a full-width trace end clears every obstacle at To.
	FullFitsAtEnd bool `json:"fullFitsAtEnd"`
	// LimitedBy names the obstacle that set the width.
	LimitedBy string `json:"limitedBy,omitempty"`
}

// specctraEscape is the escape as a DSN fixed wire (mil; layer named by
// layerName).
func (e intentEscape) specctraEscape(layerName func(int) string) specctra.Escape {
	return specctra.Escape{Net: e.Net, Layer: layerName(e.Layer), WidthMil: e.WidthMil, Path: [][2]float64{e.From, e.To}, Via: e.Via}
}

// escapeEnv is the copper an escape must clear. Pad W/H are board-axis
// extents (pcbPadP, KiCad snapshot pads).
type escapeEnv struct {
	pads []pcbPadP
	// court: courtyard box per designator; a part without one uses its pad
	// extent grown by defaultCourtyardMil.
	court map[string]layoutBBox
	// clr: board clearance incl. the router margin; netClr: a net's own
	// clearance (netclass / intent) without the margin; margin: added to it.
	clr, margin float64
	netClr      map[string]float64
	tracks      []specctra.Track
	vias        []widenVia // existing and planned escape vias
	planned     []intentEscape
	// viaDia / viaDrill: the via an inward escape ends in (0 = none).
	viaDia, viaDrill float64
}

// defaultCourtyardMil: IPC-7351 nominal courtyard excess over the pads.
const defaultCourtyardMil = 10

func (env *escapeEnv) pairClr(a, b string) float64 {
	c := env.clr
	for _, n := range []string{a, b} {
		if v, ok := env.netClr[strings.ToUpper(n)]; ok {
			c = math.Max(c, v+env.margin)
		}
	}
	return c
}

// probe is the shape whose clearance gap is measured: a centre line (a
// track of some width along it) or a box (fastroute's full-width trace end).
type probe interface {
	rect(minX, minY, maxX, maxY float64) float64
	seg(x1, y1, x2, y2 float64) float64
	pt(x, y float64) float64
}

type segProbe struct{ a, b [2]float64 }

func (p segProbe) rect(x0, y0, x1, y1 float64) float64 {
	return rectSegDist(x0, y0, x1, y1, p.a[0], p.a[1], p.b[0], p.b[1])
}
func (p segProbe) seg(x1, y1, x2, y2 float64) float64 {
	return segSegDist(p.a[0], p.a[1], p.b[0], p.b[1], x1, y1, x2, y2)
}
func (p segProbe) pt(x, y float64) float64 { return segPointDist(p.a[0], p.a[1], p.b[0], p.b[1], x, y) }

type boxProbe struct{ b layoutBBox }

func (p boxProbe) rect(x0, y0, x1, y1 float64) float64 {
	return math.Hypot(math.Max(math.Max(x0-p.b.MaxX, p.b.MinX-x1), 0), math.Max(math.Max(y0-p.b.MaxY, p.b.MinY-y1), 0))
}
func (p boxProbe) seg(x1, y1, x2, y2 float64) float64 {
	return rectSegDist(p.b.MinX, p.b.MinY, p.b.MaxX, p.b.MaxY, x1, y1, x2, y2)
}
func (p boxProbe) pt(x, y float64) float64 {
	return rectPtDist(p.b.MinX, p.b.MinY, p.b.MaxX, p.b.MaxY, x, y)
}

// gap is the smallest (distance − pair clearance) from pr of net on layer
// (0 = every layer: a via) to any other net's copper, and what it is.
func (env *escapeEnv) gap(net string, layer int, pr probe) (float64, string) {
	best, who := math.Inf(1), ""
	try := func(d float64, other, name string) {
		if g := d - env.pairClr(net, other); g < best {
			best, who = g, name
		}
	}
	on := func(l int) bool { return layer == 0 || l == layer || l == pcbLayerMulti }
	for _, q := range env.pads {
		if q.Net != "" && strings.EqualFold(q.Net, net) || !on(q.Layer) {
			continue
		}
		try(pr.rect(q.X-q.W/2, q.Y-q.H/2, q.X+q.W/2, q.Y+q.H/2), q.Net, q.Designator+"."+q.Number)
	}
	for _, t := range env.tracks {
		if strings.EqualFold(t.Net, net) || !on(t.Layer) {
			continue
		}
		try(pr.seg(t.X1, t.Y1, t.X2, t.Y2)-t.Width/2, t.Net, "track "+t.Net)
	}
	for _, v := range env.vias {
		if !strings.EqualFold(v.Net, net) {
			try(pr.pt(v.X, v.Y)-v.Diameter/2, v.Net, "via "+v.Net)
		}
	}
	for _, e := range env.planned {
		if strings.EqualFold(e.Net, net) || !on(e.Layer) {
			continue
		}
		try(pr.seg(e.From[0], e.From[1], e.To[0], e.To[1])-e.WidthMil/2, e.Net, "escape "+e.Pad)
	}
	return best, who
}

// lineGap is gap for a track centre line a–b.
func (env *escapeEnv) lineGap(net string, layer int, a, b [2]float64) (float64, string) {
	return env.gap(net, layer, segProbe{a, b})
}

// fullFits: a full-width trace end at p clears everything. fastroute draws
// trace ends as octagons; the square of the same half width contains them.
func (env *escapeEnv) fullFits(net string, layer int, p [2]float64, full float64) bool {
	h := full / 2
	g, _ := env.gap(net, layer, boxProbe{layoutBBox{MinX: p[0] - h, MinY: p[1] - h, MaxX: p[0] + h, MaxY: p[1] + h}})
	return g >= 0
}

// outward returns the escape direction of pad (away from its part's pad
// centroid, along the pad's long axis when it has one) and the pad's half
// extent along it; ok=false for a part with a single pad.
func outward(pad pcbPadP, part []pcbPadP) (dir [2]float64, half float64, ok bool) {
	if len(part) < 2 {
		return dir, 0, false
	}
	cx, cy := 0.0, 0.0
	for _, q := range part {
		cx += q.X / float64(len(part))
		cy += q.Y / float64(len(part))
	}
	dx, dy := pad.X-cx, pad.Y-cy
	if math.Hypot(dx, dy) < 1e-6 {
		return dir, 0, false
	}
	alongX := math.Abs(dx) >= math.Abs(dy)
	switch {
	case pad.W > 1.3*pad.H:
		alongX = true
	case pad.H > 1.3*pad.W:
		alongX = false
	}
	if alongX {
		if dx == 0 {
			dx = 1
		}
		return [2]float64{math.Copysign(1, dx), 0}, pad.W / 2, true
	}
	if dy == 0 {
		dy = 1
	}
	return [2]float64{0, math.Copysign(1, dy)}, pad.H / 2, true
}

// courtyardOf returns the part's courtyard (or pad extent + default excess).
func (env *escapeEnv) courtyardOf(ref string, part []pcbPadP) layoutBBox {
	if b, ok := env.court[ref]; ok && b.MaxX > b.MinX {
		return b
	}
	b := layoutBBox{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	for _, q := range part {
		b.MinX, b.MinY = math.Min(b.MinX, q.X-q.W/2), math.Min(b.MinY, q.Y-q.H/2)
		b.MaxX, b.MaxY = math.Max(b.MaxX, q.X+q.W/2), math.Max(b.MaxY, q.Y+q.H/2)
	}
	return layoutBBox{MinX: b.MinX - defaultCourtyardMil, MinY: b.MinY - defaultCourtyardMil, MaxX: b.MaxX + defaultCourtyardMil, MaxY: b.MaxY + defaultCourtyardMil}
}

// exitDist is the distance from p along dir to the box boundary (0 when p
// is already outside).
func exitDist(b layoutBBox, p, dir [2]float64) float64 {
	var d float64
	switch {
	case dir[0] > 0:
		d = b.MaxX - p[0]
	case dir[0] < 0:
		d = p[0] - b.MinX
	case dir[1] > 0:
		d = b.MaxY - p[1]
	default:
		d = p[1] - b.MinY
	}
	return math.Max(d, 0)
}

const escapeStepMil = 0.5

// escapeCand is a pad that needs an escape: the full width does not leave it
// straight out to its courtyard.
type escapeCand struct {
	pad             pcbPadP
	part            []pcbPadP
	dir             [2]float64
	half, full, min float64
	tCourt          float64
	// mate: an adjacent same-net pad of the same row (bridge partner).
	mate *pcbPadP
}

func (c escapeCand) key() string { return c.pad.Designator + "." + c.pad.Number }

// along returns the point t mil from the pad's centre along dir.
func (c escapeCand) along(t float64) [2]float64 {
	return [2]float64{round3(c.pad.X + c.dir[0]*t), round3(c.pad.Y + c.dir[1]*t)}
}

// planIntentEscapes plans an escape for every SMD pad of an intent net
// whose pitch / pad cannot take the net's full outer width straight out.
// Pads with a same-net row neighbour are planned first (they can still
// bridge to it); the pads still to be planned hold a widthMil.min outward
// stub meanwhile, so adjacent different-net pads do not take each other's
// room. skip(pad) excludes pads that already have an escape (--escapes,
// ground pre-escapes). skipped lists the pads that cannot get widthMil.min.
func planIntentEscapes(env *escapeEnv, reqs map[string]specctra.NetRequirement, skip func(pcbPadP) bool) (esc []intentEscape, skipped []string) {
	byRef := map[string][]pcbPadP{}
	for _, q := range env.pads {
		byRef[q.Designator] = append(byRef[q.Designator], q)
	}
	pads := append([]pcbPadP(nil), env.pads...)
	sort.SliceStable(pads, func(i, j int) bool {
		if pads[i].Designator != pads[j].Designator {
			return pads[i].Designator < pads[j].Designator
		}
		return naturalLess(pads[i].Number, pads[j].Number)
	})
	var cands []escapeCand
	for _, pad := range pads {
		r, ok := reqs[pad.Net]
		if !ok || pad.Layer != 1 && pad.Layer != 2 {
			continue
		}
		full, minW := r.OuterMil, r.MinMil
		if full <= 0 || minW <= 0 || minW >= full-specctraEps {
			continue // no neck-down allowed: the full width or nothing
		}
		if skip != nil && skip(pad) {
			continue
		}
		part := byRef[pad.Designator]
		dir, half, ok := outward(pad, part)
		if !ok {
			continue
		}
		c := escapeCand{pad: pad, part: part, dir: dir, half: half, full: full, min: minW}
		edge := c.along(half)
		c.tCourt = half + math.Max(math.Min(exitDist(env.courtyardOf(pad.Designator, part), edge, dir)+1, padEscapeReachMil(full)-1), 1)
		if g, _ := env.lineGap(pad.Net, pad.Layer, [2]float64{pad.X, pad.Y}, c.along(c.tCourt)); g >= full/2 && env.fullFits(pad.Net, pad.Layer, c.along(c.tCourt), full) {
			continue // the full width leaves the pad on its own
		}
		c.mate = rowMate(pad, part, dir)
		cands = append(cands, c)
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].mate != nil && cands[j].mate == nil })
	held := make([]intentEscape, len(cands))
	for i, c := range cands {
		held[i] = env.reserve(c)
	}
	for i, c := range cands {
		local := env.near(c)
		local.planned = append(local.planned, held[i+1:]...)
		e, why := local.escapeOne(c)
		if why != "" {
			skipped = append(skipped, why)
			continue
		}
		env.planned = append(env.planned, e)
		if e.Via {
			env.vias = append(env.vias, widenVia{Net: e.Net, X: e.To[0], Y: e.To[1], Diameter: e.ViaDiaMil})
		}
		esc = append(esc, e)
	}
	return esc, skipped
}

// rowMate is the same-net pad of the same part next to pad in its row
// (same position along dir, the nearest across it, within 2 pad widths).
func rowMate(pad pcbPadP, part []pcbPadP, dir [2]float64) *pcbPadP {
	cross := pad.W
	if dir[0] != 0 {
		cross = pad.H
	}
	var best *pcbPadP
	bd := math.Inf(1)
	for i := range part {
		q := &part[i]
		if q.Number == pad.Number || !strings.EqualFold(q.Net, pad.Net) {
			continue
		}
		axial := (q.X-pad.X)*dir[0] + (q.Y-pad.Y)*dir[1]
		lat := math.Abs((q.X-pad.X)*dir[1] - (q.Y-pad.Y)*dir[0])
		if math.Abs(axial) > 0.5 || lat > 2.5*cross || lat >= bd {
			continue
		}
		best, bd = q, lat
	}
	return best
}

// mateReach: the bridge's far end (inside the mate) stays within the
// pad-escape zone of c's own pad.
func (c escapeCand) mateReach(m *pcbPadP) bool {
	lat := math.Abs((m.X-c.pad.X)*c.dir[1] - (m.Y-c.pad.Y)*c.dir[0])
	cross := c.pad.W
	if c.dir[0] != 0 {
		cross = c.pad.H
	}
	return lat-cross/2 <= padEscapeReachMil(c.full)-1
}

// reserve is cand's widthMil.min outward stub, held for it while the pads
// planned before it choose theirs.
func (env *escapeEnv) reserve(c escapeCand) intentEscape {
	from, end := c.outwardSpan(env, c.min)
	return intentEscape{Pad: c.key(), Net: c.pad.Net, Layer: c.pad.Layer, WidthMil: c.min + 0.05, From: from, To: end}
}

// outwardSpan is the outward stub's centre line for width w: from inside the
// outer tip (w/2 back, never behind the pad centre) to 1 mil past the first
// point where the full width fits, else to the courtyard exit.
func (c escapeCand) outwardSpan(env *escapeEnv, w float64) ([2]float64, [2]float64) {
	from := c.along(math.Max(c.half-w/2, 0))
	limit := c.half + padEscapeReachMil(c.full) - 1
	for t := c.half + escapeStepMil; t <= limit; t += escapeStepMil {
		if env.fullFits(c.pad.Net, c.pad.Layer, c.along(t), c.full) {
			return from, c.along(math.Min(t+1, limit))
		}
	}
	return from, c.along(c.tCourt)
}

// escapeOne picks one candidate's escape: outward, else the bridge to its
// row mate, else inward with a via.
func (env *escapeEnv) escapeOne(c escapeCand) (intentEscape, string) {
	mk := func(kind string, from, to [2]float64, w float64, who string) intentEscape {
		return intentEscape{Pad: c.key(), Kind: kind, Net: c.pad.Net, Layer: c.pad.Layer, WidthMil: w, FullMil: c.full, MinMil: c.min, From: from, To: to, LimitedBy: who}
	}
	width := func(g float64) float64 { return math.Floor(math.Min(c.full, 2*g)*100) / 100 }
	// Outward.
	from, end := c.outwardSpan(env, c.min)
	g, who := env.lineGap(c.pad.Net, c.pad.Layer, from, end)
	if w := width(g); w+specctraEps >= c.min {
		// The tip start moves back by w/2: re-check at the chosen width.
		if f2, e2 := c.outwardSpan(env, w); w > c.min {
			g2, who2 := env.lineGap(c.pad.Net, c.pad.Layer, f2, e2)
			if w2 := math.Min(w, width(g2)); w2+specctraEps >= c.min {
				from, end, w, who = f2, e2, w2, who2
			} else {
				w = c.min
			}
		}
		e := mk("outward", from, end, w, who)
		e.FullFitsAtEnd = env.fullFits(c.pad.Net, c.pad.Layer, end, c.full)
		return e, ""
	}
	why := fmt.Sprintf("outward %.2f mil (limited by %s)", math.Max(width(g), 0), who)
	// Bridge to the row mate across the inner ends.
	if m := c.mate; m != nil && c.mateReach(m) {
		t := -c.half + c.min/2
		a := c.along(t)
		b := [2]float64{round3(m.X + c.dir[0]*t), round3(m.Y + c.dir[1]*t)}
		bg, bwho := env.lineGap(c.pad.Net, c.pad.Layer, a, b)
		if w := width(bg); w+specctraEps >= c.min {
			// Re-centre at the chosen width (still inside both pads).
			t = -c.half + math.Min(w, 2*c.half)/2
			a, b = c.along(t), [2]float64{round3(m.X + c.dir[0]*t), round3(m.Y + c.dir[1]*t)}
			return mk("bridge", a, b, w, bwho), ""
		}
		why += fmt.Sprintf("; bridge to %s.%s %.2f mil (limited by %s)", m.Designator, m.Number, math.Max(width(bg), 0), bwho)
	}
	// Inward with a via.
	if env.viaDia > 0 {
		reach := padEscapeReachMil(c.full)
		a := c.along(-math.Max(c.half-c.min/2, 0))
		var last string
		for d := env.viaDia/2 + 1; d <= reach-1; d += escapeStepMil {
			v := c.along(-c.half - d)
			if vg, vwho := env.gap(c.pad.Net, 0, segProbe{v, v}); vg < env.viaDia/2 {
				last = vwho
				continue
			}
			if who := env.viaAtSMD(c.pad.Net, v); who != "" {
				last = who
				continue
			}
			sg, swho := env.lineGap(c.pad.Net, c.pad.Layer, a, v)
			if w := width(sg); w+specctraEps >= c.min {
				a = c.along(-math.Max(c.half-w/2, 0))
				e := mk("inward-via", a, v, w, swho)
				e.Via, e.ViaDiaMil, e.ViaDrillMil = true, env.viaDia, env.viaDrill
				return e, ""
			} else {
				last = swho
			}
		}
		why += fmt.Sprintf("; inward via blocked (by %s)", last)
	}
	return intentEscape{}, fmt.Sprintf("%s (%s): no escape of widthMil.min %.2f mil — %s", c.key(), c.pad.Net, c.min, why)
}

// viaAtSMD names a same-net SMD pad an escape via at v would come within
// clearance of: routers keep vias off SMD pads of their own net too
// (fastroute / Freerouting via_at_smd off; a via there wicks the paste).
func (env *escapeEnv) viaAtSMD(net string, v [2]float64) string {
	for _, q := range env.pads {
		if q.Layer == pcbLayerMulti || !strings.EqualFold(q.Net, net) {
			continue
		}
		if rectPtDist(q.X-q.W/2, q.Y-q.H/2, q.X+q.W/2, q.Y+q.H/2, v[0], v[1])-env.viaDia/2 < env.pairClr(net, net) {
			return q.Designator + "." + q.Number + " (own net, via at SMD)"
		}
	}
	return ""
}

// near is env reduced to the copper that can matter to c's escape (the
// gap scans stay local on a 600-pad board).
func (env *escapeEnv) near(c escapeCand) *escapeEnv {
	maxClr := env.clr
	for _, v := range env.netClr {
		maxClr = math.Max(maxClr, v+env.margin)
	}
	r := c.half + padEscapeReachMil(c.full) + c.full + maxClr + env.viaDia + 2.5*math.Max(c.pad.W, c.pad.H)
	in := func(x0, y0, x1, y1 float64) bool {
		return math.Max(x0, x1) >= c.pad.X-r && math.Min(x0, x1) <= c.pad.X+r && math.Max(y0, y1) >= c.pad.Y-r && math.Min(y0, y1) <= c.pad.Y+r
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

// naturalLess orders pad numbers numerically when both are numbers.
func naturalLess(a, b string) bool {
	var x, y int
	if _, e1 := fmt.Sscanf(a, "%d", &x); e1 == nil {
		if _, e2 := fmt.Sscanf(b, "%d", &y); e2 == nil && fmt.Sprint(x) == a && fmt.Sprint(y) == b {
			return x < y
		}
	}
	return a < b
}

// escapeSeeded reports pads at which an escape of esc already starts.
func escapeSeeded(esc []specctra.Escape) func(pcbPadP) bool {
	return func(p pcbPadP) bool {
		for _, e := range esc {
			if len(e.Path) > 0 && strings.EqualFold(e.Net, p.Net) && math.Hypot(e.Path[0][0]-p.X, e.Path[0][1]-p.Y) <= 1 {
				return true
			}
		}
		return false
	}
}

// placementHint says, for a connection the router still found blocked,
// which part to move and which way.
type placementHint struct {
	Conn      string  `json:"connection"`
	Pad       string  `json:"pad,omitempty"` // the trapped pad
	Move      string  `json:"move,omitempty"`
	Direction string  `json:"direction,omitempty"`
	ByMil     float64 `json:"byMil,omitempty"`
	Why       string  `json:"why"`
}

func (h placementHint) String() string {
	if h.Move == "" {
		return fmt.Sprintf("%s: %s", h.Conn, h.Why)
	}
	return fmt.Sprintf("%s: move %s %s by ≥ %.0f mil — %s", h.Conn, h.Move, h.Direction, h.ByMil, h.Why)
}

// compass names a board direction (y up).
func compass(d [2]float64) string {
	ang := math.Atan2(d[1], d[0]) * 180 / math.Pi
	names := []string{"right (+X)", "up-right", "up (+Y)", "up-left", "left (−X)", "down-left", "down (−Y)", "down-right"}
	i := int(math.Round(ang/45)+8) % 8
	return names[i]
}

// placementHints turns blocked connections into placement hints. The
// trapped end is the pad on the part with more pads (the fine-pitch side);
// the escape corridor straight out of it (the class width plus clearance on
// each side, padEscapeReach + one width long) names the part in the way and
// how far it has to move along the escape direction. With a free corridor
// the other end's part is moved toward the escape exit; two pads of one part
// (same-net neighbours) have no part to move.
func placementHints(blocked []routeConn, env *escapeEnv, reqs map[string]specctra.NetRequirement) []placementHint {
	byKey := map[string]pcbPadP{}
	byRef := map[string][]pcbPadP{}
	for _, q := range env.pads {
		byKey[q.Designator+"."+q.Number] = q
		byRef[q.Designator] = append(byRef[q.Designator], q)
	}
	var out []placementHint
	for _, c := range blocked {
		a, okA := byKey[c.From]
		b, okB := byKey[c.To]
		h := placementHint{Conn: c.String()}
		if !okA && !okB {
			h.Why = "neither end is a pad (via / track end): reroute"
			out = append(out, h)
			continue
		}
		trap, other, hasOther := a, b, okB
		if !okA || okB && len(byRef[b.Designator]) > len(byRef[a.Designator]) {
			trap, other, hasOther = b, a, okA
		}
		h.Pad = trap.Designator + "." + trap.Number
		dir, half, ok := outward(trap, byRef[trap.Designator])
		if !ok {
			h.Why = "single-pad part: reroute or move it"
			out = append(out, h)
			continue
		}
		w := math.Max(reqs[trap.Net].OuterMil, math.Min(trap.W, trap.H))
		clr := env.pairClr(trap.Net, "")
		length := padEscapeReachMil(w) + w
		edge := [2]float64{trap.X + dir[0]*half, trap.Y + dir[1]*half}
		hw := w/2 + clr
		// Corridor rectangle.
		x0, y0 := edge[0]-math.Abs(dir[1])*hw, edge[1]-math.Abs(dir[0])*hw
		x1, y1 := edge[0]+dir[0]*length+math.Abs(dir[1])*hw, edge[1]+dir[1]*length+math.Abs(dir[0])*hw
		box := layoutBBox{MinX: math.Min(x0, x1), MinY: math.Min(y0, y1), MaxX: math.Max(x0, x1), MaxY: math.Max(y0, y1)}
		nearest, who := math.Inf(1), ""
		for _, q := range env.pads {
			if q.Designator == trap.Designator || strings.EqualFold(q.Net, trap.Net) && q.Net != "" {
				continue
			}
			if q.X+q.W/2 < box.MinX || q.X-q.W/2 > box.MaxX || q.Y+q.H/2 < box.MinY || q.Y-q.H/2 > box.MaxY {
				continue
			}
			// Distance from the pad edge to the obstacle's near side along dir.
			d := (q.X-edge[0])*dir[0] + (q.Y-edge[1])*dir[1] - (q.W/2*math.Abs(dir[0]) + q.H/2*math.Abs(dir[1]))
			if d < nearest {
				nearest, who = d, q.Designator
			}
		}
		switch {
		case who != "":
			h.Move, h.Direction = who, compass(dir)
			h.ByMil = math.Ceil(length - math.Max(nearest, 0))
			h.Why = fmt.Sprintf("%s sits in the escape corridor of %s (%.1f mil wide, %.0f mil long)", who, h.Pad, 2*hw, length)
		case hasOther && other.Designator != trap.Designator && math.Hypot(other.X-trap.X, other.Y-trap.Y) <= 4*length:
			exit := [2]float64{edge[0] + dir[0]*length, edge[1] + dir[1]*length}
			v := [2]float64{exit[0] - other.X, exit[1] - other.Y}
			h.Move, h.Direction, h.ByMil = other.Designator, compass(v), math.Ceil(math.Hypot(v[0], v[1])/2)
			h.Why = fmt.Sprintf("the escape of %s is free; routed copper fences the path to %s.%s — bring it toward the escape exit", h.Pad, other.Designator, other.Number)
		case hasOther && other.Designator != trap.Designator:
			h.Why = fmt.Sprintf("the escape of %s is free and %s.%s is far: the path is fenced along the way — no single part to move (reroute with more room, or move the parts the route has to pass)", h.Pad, other.Designator, other.Number)
		default:
			h.Why = fmt.Sprintf("both ends on %s (same-net pins): no part to move; the routed copper between them blocks the join — widen the pitch side or rotate a neighbour", trap.Designator)
		}
		out = append(out, h)
	}
	return out
}
