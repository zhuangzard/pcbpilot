package app

// pcb_via_arrays.go — post-route via arrays. The intent sizes each power
// net's layer transitions as k vias of drill/dia in parallel (intent
// nets[].via.countPerTransition); fastroute places one via per transition.
// Gas Module v9: seven single 12/24 mil vias carried 0.74 A where +12V, GND
// and VIN_F need 1.46–1.51 A. This adds the k−1 missing vias around each
// transition via, the way the internal router's via arrays do: each extra
// via either sits on the net's track of a layer it joins or is tied to the
// transition via by a stub of the net's width on that layer, and every via
// and stub keeps the clearance to all other nets' copper. Sites that do not
// fit are reported, never forced; the via-current gate then fails.

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// viaNeed is a net's via sizing from the intent.
type viaNeed struct {
	Count              int
	DiaMil, DrillMil   float64
	OuterMil, InnerMil float64
}

type viaArrayPlan struct {
	Vias      []specctra.NewVia   `json:"vias"`
	Stubs     []specctra.NewTrack `json:"stubs"`
	Shortfall []string            `json:"shortfall,omitempty"`
}

// planViaArrays plans the missing vias of every transition via whose net
// needs more than one. bounds is the board area a via centre may use.
func planViaArrays(tracks []specctra.Track, vias []widenVia, pads []boardPad, needs map[string]viaNeed, clr float64, bounds layoutBBox) viaArrayPlan {
	var plan viaArrayPlan
	type copper struct {
		net       string
		layer     int // 0 = all layers (via)
		a, b      [2]float64
		halfWidth float64
	}
	var cu []copper
	for _, t := range tracks {
		cu = append(cu, copper{t.Net, t.Layer, [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}, t.Width / 2})
	}
	allVias := append([]widenVia(nil), vias...)
	sameNet := func(a, b string) bool { return strings.EqualFold(a, b) }
	padDist := func(p boardPad, q [2]float64) float64 {
		return math.Hypot(math.Max(math.Abs(q[0]-p.X)-p.W/2, 0), math.Max(math.Abs(q[1]-p.Y)-p.H/2, 0))
	}
	padSegDist := func(p boardPad, a, b [2]float64) float64 {
		return rectSegDist(p.X-p.W/2, p.Y-p.H/2, p.X+p.W/2, p.Y+p.H/2, a[0], a[1], b[0], b[1])
	}

	// viaFits: a through via of net at q clears every other net's copper on
	// every layer, every hole, and same-net vias by a hole gap.
	viaFits := func(net string, q [2]float64, dia float64) bool {
		if q[0]-dia/2 < bounds.MinX || q[0]+dia/2 > bounds.MaxX || q[1]-dia/2 < bounds.MinY || q[1]+dia/2 > bounds.MaxY {
			return false
		}
		for _, c := range cu {
			if !sameNet(c.net, net) && segDist(q, c.a, c.b)-c.halfWidth-dia/2 < clr {
				return false
			}
		}
		for _, v := range allVias {
			d := math.Hypot(v.X-q[0], v.Y-q[1]) - v.Diameter/2 - dia/2
			if d < clr {
				return false
			}
		}
		for _, p := range pads { // no via in any pad
			if padDist(p, q)-dia/2 < clr {
				return false
			}
		}
		return true
	}
	// stubFits: a track of net on layer from a to b clears other nets.
	stubFits := func(net string, layer int, a, b [2]float64, half float64) bool {
		for _, c := range cu {
			if sameNet(c.net, net) || (c.layer != layer && c.layer != 0) {
				continue
			}
			if segSegDist(a[0], a[1], b[0], b[1], c.a[0], c.a[1], c.b[0], c.b[1])-c.halfWidth-half < clr {
				return false
			}
		}
		for _, v := range allVias {
			if !sameNet(v.Net, net) && segDist([2]float64{v.X, v.Y}, a, b)-v.Diameter/2-half < clr {
				return false
			}
		}
		for _, p := range pads {
			if sameNet(p.Net, net) || (p.Layer != layer && p.Layer != pcbLayerMulti) {
				continue
			}
			if padSegDist(p, a, b)-half < clr {
				return false
			}
		}
		return true
	}

	order := append([]widenVia(nil), vias...)
	sort.Slice(order, func(i, j int) bool {
		if order[i].Net != order[j].Net {
			return order[i].Net < order[j].Net
		}
		if order[i].X != order[j].X {
			return order[i].X < order[j].X
		}
		return order[i].Y < order[j].Y
	})
	for _, v := range order {
		need, ok := needs[v.Net]
		if !ok || need.Count < 2 {
			continue
		}
		dia := need.DiaMil
		if dia <= 0 {
			dia = v.Diameter
		}
		P := [2]float64{v.X, v.Y}
		pitch := dia + clr
		// Layers this via joins: same-net tracks ending on it.
		widthOn := map[int]float64{}
		for _, t := range tracks {
			if !sameNet(t.Net, v.Net) {
				continue
			}
			for _, e := range [][2]float64{{t.X1, t.Y1}, {t.X2, t.Y2}} {
				if math.Hypot(e[0]-P[0], e[1]-P[1]) <= v.Diameter/2 {
					w := need.OuterMil
					if t.Layer >= 15 {
						w = need.InnerMil
					}
					widthOn[t.Layer] = math.Max(w, t.Width)
				}
			}
		}
		if len(widthOn) < 2 {
			continue // not a track-to-track transition
		}
		have := 0
		for _, o := range allVias {
			if sameNet(o.Net, v.Net) && math.Hypot(o.X-P[0], o.Y-P[1]) <= 3.5*pitch {
				have++
			}
		}
		if have >= need.Count {
			continue
		}
		var cands [][2]float64
		// Rings of 16 sites out to three pitches: a crowded transition
		// (Gas v10 A, VIN_F) can still take its array a little further
		// along or beside its tracks; nearest sites are tried first.
		for _, r := range []float64{pitch, 1.5 * pitch, 2 * pitch, 3 * pitch} {
			for k := 0; k < 16; k++ {
				a := float64(k) * math.Pi / 8
				cands = append(cands, [2]float64{round3(P[0] + r*math.Cos(a)), round3(P[1] + r*math.Sin(a))})
			}
		}
		layers := make([]int, 0, len(widthOn))
		for l := range widthOn {
			layers = append(layers, l)
		}
		sort.Ints(layers)
		for _, q := range cands {
			if have >= need.Count {
				break
			}
			if !viaFits(v.Net, q, dia) {
				continue
			}
			var stubs []specctra.NewTrack
			okAll := true
			for _, l := range layers {
				w := widthOn[l]
				onTrack := false
				for _, t := range tracks {
					if t.Layer == l && sameNet(t.Net, v.Net) && segDist(q, [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}) <= t.Width/2-dia/4 {
						onTrack = true
						break
					}
				}
				if onTrack {
					continue
				}
				if !stubFits(v.Net, l, P, q, w/2) {
					okAll = false
					break
				}
				stubs = append(stubs, specctra.NewTrack{Net: v.Net, Layer: l, X1: P[0], Y1: P[1], X2: q[0], Y2: q[1], Width: w})
			}
			if !okAll {
				continue
			}
			nv := specctra.NewVia{Net: v.Net, X: q[0], Y: q[1], DiameterMil: dia}
			plan.Vias = append(plan.Vias, nv)
			plan.Stubs = append(plan.Stubs, stubs...)
			allVias = append(allVias, widenVia{Net: v.Net, X: q[0], Y: q[1], Diameter: dia})
			for _, s := range stubs {
				cu = append(cu, copper{s.Net, s.Layer, [2]float64{s.X1, s.Y1}, [2]float64{s.X2, s.Y2}, s.Width / 2})
			}
			have++
		}
		if have < need.Count {
			plan.Shortfall = append(plan.Shortfall, fmt.Sprintf("%s via at (%.1f, %.1f): %d of %d vias fit", v.Net, P[0], P[1], have, need.Count))
		}
	}
	return plan
}

// intentViaNeeds reads the per-net via sizing of the intent.
func intentViaNeeds(in *designIntent) map[string]viaNeed {
	out := map[string]viaNeed{}
	for name, n := range in.Nets {
		if n == nil {
			continue
		}
		v := viaNeed{Count: n.ViasPerTransition, OuterMil: n.WidthMil.Outer, InnerMil: n.WidthMil.Inner}
		if n.Via != nil {
			if n.Via.CountPerTransition > v.Count {
				v.Count = n.Via.CountPerTransition
			}
			v.DiaMil, v.DrillMil = n.Via.DiaMil, n.Via.DrillMil
		}
		if v.InnerMil <= 0 {
			v.InnerMil = v.OuterMil
		}
		if v.Count > 1 {
			out[name] = v
		}
	}
	return out
}

// applyViaArrays plans and creates the via arrays on the live board (read
// after save + reload). Vias take the intent's drill and diameter; stubs the
// net's layer width. Shortfalls are returned for the summary; the via-current
// gate judges them.
func applyViaArrays(cfg *appConfig, window string, in *designIntent, stderr io.Writer) (*viaArrayPlan, error) {
	needs := intentViaNeeds(in)
	if len(needs) == 0 {
		return &viaArrayPlan{}, nil
	}
	if err := saveAndReload(cfg, window); err != nil {
		return nil, err
	}
	snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{withRules: true, withLayers: true, withCopper: true})
	if err != nil || snap.Copper == nil {
		return nil, fmt.Errorf("via arrays: board snapshot: %v", err)
	}
	if snap.Outline == nil {
		return nil, fmt.Errorf("via arrays: board outline unreadable (bring the PCB to the foreground)")
	}
	var tracks []specctra.Track
	var vias []widenVia
	if err := decodeAny(snap.Copper.Lines, &tracks); err != nil {
		return nil, err
	}
	if err := decodeAny(snap.Copper.Vias, &vias); err != nil {
		return nil, err
	}
	var pads []boardPad
	for _, c := range snap.Components {
		pads = append(pads, c.Pads...)
	}
	clr, edge := 6.0, 20.0
	if snap.Rules != nil && snap.Rules.ClearanceMil > 0 {
		clr = snap.Rules.ClearanceMil
	}
	if snap.Rules != nil && snap.Rules.CopperToEdgeMil > edge {
		edge = snap.Rules.CopperToEdgeMil
	}
	clr += specctra.ClearanceMarginMil // EasyEDA's DRC rounds tighter than the rule
	b := snap.Outline.BBox
	bounds := layoutBBox{MinX: b.MinX + edge, MinY: b.MinY + edge, MaxX: b.MaxX - edge, MaxY: b.MaxY - edge}
	plan := planViaArrays(tracks, vias, pads, needs, clr, bounds)
	fmt.Fprintf(stderr, "via arrays: %d via(s) and %d stub(s) added, %d transition(s) short\n", len(plan.Vias), len(plan.Stubs), len(plan.Shortfall))
	for _, v := range plan.Vias {
		payload := map[string]any{"x": v.X, "y": v.Y, "net": v.Net}
		if n := needs[v.Net]; n.DrillMil > 0 {
			payload["holeDiameter"] = n.DrillMil
		}
		if v.DiameterMil > 0 {
			payload["diameter"] = v.DiameterMil
		}
		if _, err := requestAction(cfg, "pcb.via.create", window, payload); err != nil {
			return &plan, fmt.Errorf("via arrays: create via %s (%.1f,%.1f): %w", v.Net, v.X, v.Y, err)
		}
	}
	for _, t := range plan.Stubs {
		if err := createPcbTrack(cfg, window, t); err != nil {
			return &plan, fmt.Errorf("via arrays: create stub %s: %w", t.Net, err)
		}
	}
	return &plan, nil
}
