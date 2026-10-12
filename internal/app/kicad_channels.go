package app

import (
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Escape-channel metrics (v0.9.2 experiment E1/E2).
//
// A placement decides, before any copper exists, how much room each pad has
// to leave its own part. channelOf measures that room offline and cheaply
// (milliseconds, no router): from the pad centre it sends a track of the
// pad's net width along each of 8 compass directions and records how far the
// corridor stays clear of every foreign-net pad (and, with copper, track /
// via), and in how many directions a via still fits at the end of a free run.
// A pad with no via-capable direction cannot reach another layer and can only
// join its own net by a free surface path — the situation that left the
// PicoRick U303 (QFN 0.4 mm) connections unrouted.
//
// It is a proxy: it reads pads and optionally existing copper, never decides
// a gate, and never touches the board.

// channelDirs are the 8 corridor directions (board frame, y up).
var channelDirs = [8][2]float64{
	{1, 0}, {math.Sqrt2 / 2, math.Sqrt2 / 2}, {0, 1}, {-math.Sqrt2 / 2, math.Sqrt2 / 2},
	{-1, 0}, {-math.Sqrt2 / 2, -math.Sqrt2 / 2}, {0, -1}, {math.Sqrt2 / 2, -math.Sqrt2 / 2},
}

// channelParams are the geometry the corridor test uses.
type channelParams struct {
	TrackMil   float64 // track width of the corridor
	ViaDiaMil  float64 // via that must fit at the end of a run
	ReachMil   float64 // longest run examined from the pad edge
	StepMil    float64 // run resolution
	DensityMil float64 // half side of the square the density is taken over
}

func defaultChannelParams(env *escapeEnv) channelParams {
	p := channelParams{TrackMil: 6, ViaDiaMil: 23.622, ReachMil: 120, StepMil: 4, DensityMil: 150}
	if env.viaDia > 0 {
		p.ViaDiaMil = env.viaDia
	}
	return p
}

// padChannel is one pad's measured room.
type padChannel struct {
	Pad      string  `json:"pad"` // REF.NUM
	Net      string  `json:"net"`
	FreeDirs int     `json:"freeDirs"` // directions whose corridor is clear for ≥ one via diameter
	ViaDirs  int     `json:"viaDirs"`  // directions where a via fits at the end of a free run
	BestRun  float64 `json:"bestRunMil"`
	MinGap   float64 `json:"minGapMil"` // pad edge → nearest foreign-net copper, minus clearance (< 0 = already violating)
	Density  float64 `json:"density"`   // fraction of the surrounding square covered by other parts' courtyards
}

// padBox is a pad's axis-aligned placed extent.
func chanPadBox(q pcbPadP) layoutBBox {
	return layoutBBox{MinX: q.X - q.W/2, MinY: q.Y - q.H/2, MaxX: q.X + q.W/2, MaxY: q.Y + q.H/2}
}

// channelOf measures pad q.
func (env *escapeEnv) channelOf(q pcbPadP, cp channelParams) padChannel {
	out := padChannel{Pad: q.Designator + "." + q.Number, Net: q.Net, MinGap: math.Inf(1)}
	layer := q.Layer
	if layer == pcbLayerMulti {
		layer = 1 // a through-hole pad escapes on the top layer
	}
	box := chanPadBox(q)
	if g, _ := env.gap(q.Net, layer, boxProbe{box}); g < out.MinGap {
		out.MinGap = g
	}
	half := cp.TrackMil / 2
	for _, d := range channelDirs {
		// The corridor starts at the pad centre; the pad itself is own-net
		// copper and never blocks. Run length is measured from the pad edge.
		edge := padEdgeDist(q, d)
		run, viaOK := 0.0, false
		for l := cp.StepMil; l <= cp.ReachMil; l += cp.StepMil {
			end := [2]float64{q.X + d[0]*(edge+l), q.Y + d[1]*(edge+l)}
			if g, _ := env.lineGap(q.Net, layer, [2]float64{q.X, q.Y}, end); g < half {
				break
			}
			run = l
			if !viaOK {
				h := cp.ViaDiaMil / 2
				if env.fullFitsVia(q.Net, end, h) {
					viaOK = true
				}
			}
		}
		out.BestRun = math.Max(out.BestRun, run)
		if run >= cp.ViaDiaMil {
			out.FreeDirs++
		}
		if viaOK {
			out.ViaDirs++
		}
	}
	out.Density = env.density(q, cp.DensityMil)
	return out
}

// fullFitsVia: a via of radius h centred at p clears every other net on all
// layers.
func (env *escapeEnv) fullFitsVia(net string, p [2]float64, h float64) bool {
	g, _ := env.gap(net, 0, boxProbe{layoutBBox{MinX: p[0] - h, MinY: p[1] - h, MaxX: p[0] + h, MaxY: p[1] + h}})
	return g >= 0
}

// padEdgeDist is the distance from the pad centre to the pad boundary along d.
func padEdgeDist(q pcbPadP, d [2]float64) float64 {
	t := math.Inf(1)
	if math.Abs(d[0]) > 1e-9 {
		t = math.Min(t, q.W/2/math.Abs(d[0]))
	}
	if math.Abs(d[1]) > 1e-9 {
		t = math.Min(t, q.H/2/math.Abs(d[1]))
	}
	return t
}

// density is the share of the square around q covered by other parts'
// courtyards (a part without one counts as its pad extent grown by the
// default excess).
func (env *escapeEnv) density(q pcbPadP, half float64) float64 {
	if env.courtCache == nil {
		byRef := map[string][]pcbPadP{}
		for _, p := range env.pads {
			byRef[p.Designator] = append(byRef[p.Designator], p)
		}
		env.courtCache = make(map[string]layoutBBox, len(byRef))
		for ref, part := range byRef {
			env.courtCache[ref] = env.courtyardOf(ref, part)
		}
	}
	sq := layoutBBox{MinX: q.X - half, MinY: q.Y - half, MaxX: q.X + half, MaxY: q.Y + half}
	area := 0.0
	for ref, c := range env.courtCache {
		if ref == q.Designator {
			continue
		}
		w := math.Min(c.MaxX, sq.MaxX) - math.Max(c.MinX, sq.MinX)
		h := math.Min(c.MaxY, sq.MaxY) - math.Max(c.MinY, sq.MinY)
		if w > 0 && h > 0 {
			area += w * h
		}
	}
	return math.Min(area/((2*half)*(2*half)), 1)
}

// envOfBoard is the escape environment of a placer board (no copper): pads at
// their placed extents, each part's body as courtyard, clearance and via
// from the caller.
func envOfBoard(b *pcbauto.Board, clrMil, viaDiaMil float64) *escapeEnv {
	env := &escapeEnv{court: map[string]layoutBBox{}, netClr: map[string]float64{}, clr: clrMil, viaDia: viaDiaMil}
	for _, p := range b.Parts {
		c := p.Body()
		env.court[p.Ref] = layoutBBox{MinX: c.MinX, MinY: c.MinY, MaxX: c.MaxX, MaxY: c.MaxY}
		for _, pd := range p.Pads {
			r := pd.Box.Bounds()
			layer := pd.Layer
			if layer == 0 {
				layer = 1
			}
			env.pads = append(env.pads, pcbPadP{Designator: p.Ref, Number: pd.Number, Net: pd.Net, Layer: layer,
				X: pd.Box.C.X, Y: pd.Box.C.Y, W: r.MaxX - r.MinX, H: r.MaxY - r.MinY})
		}
	}
	return env
}

// denseICs are the parts whose pads need a deliberate fan-out: ≥ 12 pads at
// ≤ 0.65 mm pitch (nearest-neighbour pad distance), the QFN/QFP/BGA class.
func (env *escapeEnv) denseICs() map[string][]pcbPadP {
	byRef := map[string][]pcbPadP{}
	for _, q := range env.pads {
		byRef[q.Designator] = append(byRef[q.Designator], q)
	}
	out := map[string][]pcbPadP{}
	for ref, part := range byRef {
		if len(part) < 12 {
			continue
		}
		var pitch []float64
		for i, a := range part {
			best := math.Inf(1)
			for j, b := range part {
				if i != j {
					best = math.Min(best, math.Hypot(a.X-b.X, a.Y-b.Y))
				}
			}
			pitch = append(pitch, best)
		}
		sort.Float64s(pitch)
		if pitch[len(pitch)/2] <= 25.6 { // 0.65 mm
			out[ref] = part
		}
	}
	return out
}

// placementProxy is the cheap pre-route score of one placement.
type placementProxy struct {
	Trapped      []string `json:"trapped"`      // signal pads of fine-pitch ICs with no via-capable direction
	Tight        []string `json:"tight"`        // exactly one via-capable direction
	Violating    int      `json:"violating"`    // pads whose clearance to foreign copper is already < 0
	EstUnrouted  int      `json:"estUnrouted"`  // trapped pads (each strands ≥ 1 connection)
	MeanViaDirs  float64  `json:"meanViaDirs"`  // over the fine-pitch IC pads
	MinGapMil    float64  `json:"minGapMil"`    // smallest pad gap on the fine-pitch ICs
	DenseICCount int      `json:"denseICCount"` // fine-pitch parts examined
	PadsMeasured int      `json:"padsMeasured"`
}

// scorePlacement measures every connected pad of every fine-pitch IC plus the
// pads on the same net within reach of them (the decoupling pad the escape has
// to meet), and summarises. Lower Rank is better.
func (env *escapeEnv) scorePlacement(cp channelParams) (placementProxy, []padChannel) {
	pr := placementProxy{MinGapMil: math.Inf(1)}
	dense := env.denseICs()
	pr.DenseICCount = len(dense)
	var all []padChannel
	sum := 0
	for _, part := range dense {
		for _, q := range part {
			if q.Net == "" {
				continue
			}
			ch := env.channelOf(q, cp)
			all = append(all, ch)
			sum += ch.ViaDirs
			pr.MinGapMil = math.Min(pr.MinGapMil, ch.MinGap)
			switch {
			case ch.ViaDirs == 0:
				pr.Trapped = append(pr.Trapped, ch.Pad)
			case ch.ViaDirs == 1:
				pr.Tight = append(pr.Tight, ch.Pad)
			}
			if ch.MinGap < 0 {
				pr.Violating++
			}
		}
	}
	pr.PadsMeasured = len(all)
	if len(all) > 0 {
		pr.MeanViaDirs = float64(sum) / float64(len(all))
	} else {
		pr.MinGapMil = 0
	}
	sort.Strings(pr.Trapped)
	sort.Strings(pr.Tight)
	pr.EstUnrouted = len(pr.Trapped)
	return pr, all
}

// Rank orders placements: fewer trapped pads, then fewer tight pads, then a
// larger mean of via-capable directions. Returns true when a is better.
func (a placementProxy) Better(b placementProxy) bool {
	if a.EstUnrouted != b.EstUnrouted {
		return a.EstUnrouted < b.EstUnrouted
	}
	if len(a.Tight) != len(b.Tight) {
		return len(a.Tight) < len(b.Tight)
	}
	return a.MeanViaDirs > b.MeanViaDirs
}

// connectionChannel is one (blocked) connection with the channel metrics of
// both ends and the distance between them.
type connectionChannel struct {
	Conn     string      `json:"connection"`
	From     *padChannel `json:"from,omitempty"`
	To       *padChannel `json:"to,omitempty"`
	DistMil  float64     `json:"distMil"`
	Blocked  bool        `json:"blocked"`
	TrapPart string      `json:"trapPart,omitempty"`
}

// channelsFor measures the pads named in conns (REF.NUM) of env.
func (env *escapeEnv) channelsFor(conns []routeConn, blocked bool, cp channelParams) []connectionChannel {
	by := map[string]pcbPadP{}
	for _, q := range env.pads {
		by[q.Designator+"."+q.Number] = q
	}
	var out []connectionChannel
	for _, c := range conns {
		cc := connectionChannel{Conn: c.String(), Blocked: blocked}
		a, okA := by[c.From]
		b, okB := by[c.To]
		if okA {
			ch := env.channelOf(a, cp)
			cc.From = &ch
		}
		if okB {
			ch := env.channelOf(b, cp)
			cc.To = &ch
		}
		if okA && okB {
			cc.DistMil = math.Hypot(a.X-b.X, a.Y-b.Y)
		}
		out = append(out, cc)
	}
	return out
}

// auc is the probability that a random positive scores higher than a random
// negative (ties count half): 0.5 = no signal, 1 = perfect separation.
func auc(pos, neg []float64) float64 {
	if len(pos) == 0 || len(neg) == 0 {
		return math.NaN()
	}
	w := 0.0
	for _, p := range pos {
		for _, n := range neg {
			switch {
			case p > n:
				w++
			case p == n:
				w += 0.5
			}
		}
	}
	return w / float64(len(pos)*len(neg))
}

// refOf is the designator part of REF.NUM.
func refOf(pad string) string {
	if i := strings.LastIndex(pad, "."); i > 0 {
		return pad[:i]
	}
	return pad
}
