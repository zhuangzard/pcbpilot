package postsim

import (
	"fmt"
	"math"
	"sort"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Actionable copper feedback from the solved board: which track must be
// wider, which corner crowds current, which via is a bottleneck, which pour
// neck is too narrow. Items use the pcb auto feedback.json schema
// (pcbauto.FeedbackItem) so the layout loop can read both.
//
// Limits (all configurable):
//   - track capacity: IPC-2221 external curve I = 0.048·ΔT^0.44·A^0.725 at the
//     intent's tempRiseC (IPC-2152: internal ≈ external); a track needs
//     width ≥ IPC width(I·margin);
//   - optional absolute current density --j-max (A/mm²);
//   - trace self-heating: a Joule-only thermal solve (same board, only the
//     copper losses as sources) gives the rise the copper itself causes over
//     the board; > tempRiseC → widen;
//   - corners: interior angle ≤ 95° concentrates current on the inner edge;
//     crowding factor k = 1 + (180° − angle)/180° (90° → 1.5, 45° → 1.75);
//     I·k over the track capacity → chamfer / round the corner or widen;
//   - vias: barrel current over 50 % of its ampacity → add vias so each
//     carries ≤ ampacity/margin;
//   - pour necks: sheet cells whose current density exceeds what a strip one
//     cell wide may carry at tempRiseC.

// Feedback kinds of the post-layout verification.
const (
	FBWiden  = "widen-segment"
	FBCorner = "corner-crowding"
	FBVia    = "via-bottleneck"
)

type keptNet struct {
	en   *eNet
	sol  *eSolution
	nres *NetResult
}

// ipcWidth is the IPC-2221 external-curve width (mil) for amps at dT on
// copper tMil thick.
func ipcWidth(amps, tMil, dT float64) float64 {
	if amps <= 0 {
		return 0
	}
	a := math.Pow(amps/(0.048*math.Pow(dT, 0.44)), 1/0.725)
	return a / tMil
}

func ceilHalf(v float64) float64 { return math.Ceil(v*2-1e-9) / 2 }

func (res *Result) buildFeedback(kept []keptNet, g *Grid, st *Stackup, rise []float64, o Options) {
	dT := o.TempRiseC
	margin := o.Margin
	nc := g.cells()
	items := []*pcbauto.FeedbackItem{}
	next := map[string]int{}
	add := func(kind, sev, title string, ev pcbauto.FeedbackEvidence, summary string, actions []string, conf float64) {
		next[kind]++
		items = append(items, &pcbauto.FeedbackItem{ID: fmt.Sprintf("post-%s-%d", kind, next[kind]), Kind: kind, Severity: sev, Title: title,
			Evidence: ev, Proposal: pcbauto.FeedbackProposal{Summary: summary, Actions: actions}, Confidence: conf,
			Status: "live-unverified", Caveats: []string{"offline post-layout simulation on the board dump; re-dump, re-run sim post-layout and DRC after any change"}})
	}
	for _, kn := range kept {
		en, sol, nres := kn.en, kn.sol, kn.nres
		// Per original track: current at each end, max current, max rise.
		type tstat struct {
			imax, jmax, rise float64
			iA, iB           float64
			pieces           int
		}
		ts := make([]tstat, len(en.tracks))
		for _, s := range en.segs {
			e := en.edges[s.Edge]
			i := math.Abs(sol.current(e))
			t := &ts[s.Track]
			tr := en.tracks[s.Track]
			j := i / (s.W * MilMm * st.Layers[s.K].CuMm)
			t.imax, t.jmax = math.Max(t.imax, i), math.Max(t.jmax, j)
			if rise != nil {
				if c := g.cellAt(lerp(s.A, s.B, 0.5)); c >= 0 {
					t.rise = math.Max(t.rise, rise[s.K*nc+c])
				}
			}
			if dist(s.A, tr.A) < 1e-6 || dist(s.B, tr.A) < 1e-6 {
				t.iA = i
			}
			if dist(s.A, tr.B) < 1e-6 || dist(s.B, tr.B) < 1e-6 {
				t.iB = i
			}
			t.pieces++
		}
		for ti, tr := range en.tracks {
			t := ts[ti]
			nres.MaxTraceRiseC = math.Max(nres.MaxTraceRiseC, t.rise)
			if t.imax <= 0 {
				continue
			}
			k := st.index(tr.Layer)
			tMil := st.Layers[k].CuMm / MilMm
			capA := trackAmpacity(tr.W, tMil, dT)
			need := ipcWidth(t.imax*margin, tMil, dT)
			L := dist(tr.A, tr.B)
			var why []string
			sev := ""
			switch {
			case t.imax > capA:
				sev = "high"
				why = append(why, fmt.Sprintf("%.3f A > IPC capacity %.3f A of %.1f mil at ΔT %.0f °C", t.imax, capA, tr.W, dT))
			case tr.W+1e-6 < need && L >= 50:
				sev = "medium"
				why = append(why, fmt.Sprintf("%.3f A × margin %.2g needs %.1f mil (IPC, ΔT %.0f °C); routed %.1f mil", t.imax, margin, need, dT, tr.W))
			}
			if t.rise > dT {
				if sev == "" || sev == "medium" {
					sev = "high"
				}
				why = append(why, fmt.Sprintf("copper self-heating raises it %.1f °C over the board > %.0f °C", t.rise, dT))
				need = math.Max(need, tr.W*math.Sqrt(t.rise/dT))
			}
			if o.JMax > 0 && t.jmax > o.JMax {
				if sev == "" {
					sev = "medium"
				}
				why = append(why, fmt.Sprintf("J %.1f A/mm² > limit %.0f A/mm²", t.jmax, o.JMax))
				need = math.Max(need, tr.W*t.jmax/o.JMax)
			}
			if sev == "" {
				continue
			}
			need = math.Max(ceilHalf(need), tr.W+0.5)
			what := "track"
			if L < 60 {
				what = "neck"
			}
			add(FBWiden, sev, fmt.Sprintf("%s %s %s %.1f → %.1f mil", nres.Net, st.Layers[k].Name, what, tr.W, need),
				pcbauto.FeedbackEvidence{Metrics: map[string]float64{"currentA": round(t.imax, 4), "jAmm2": round(t.jmax, 2), "widthMil": tr.W,
					"capacityA": round(capA, 3), "recommendMil": need, "lengthMil": round(L, 1), "riseC": round(t.rise, 2)},
					Nets: []string{nres.Net}, Points: []pcbauto.FeedbackPoint{{Label: "a", X: round(tr.A.X, 2), Y: round(tr.A.Y, 2)}, {Label: "b", X: round(tr.B.X, 2), Y: round(tr.B.Y, 2)}},
					Detail: append(why, "track "+tr.ID)},
				fmt.Sprintf("widen %s %s %s (%.0f,%.0f)→(%.0f,%.0f) from %.1f to ≥ %.1f mil", nres.Net, st.Layers[k].Name, what, tr.A.X, tr.A.Y, tr.B.X, tr.B.Y, tr.W, need),
				[]string{fmt.Sprintf("set width ≥ %.1f mil (IPC-2152 for %.3f A × %.2g at ΔT %.0f °C)", need, t.imax, margin, dT),
					"or add parallel copper: a same-net pour / second track on another layer with vias at both ends"}, 0.8)
		}
		// Corners between two tracks meeting end to end on one layer.
		type end struct {
			ti int
			p  Point
			o  Point // the track's other end
			i  float64
		}
		var ends []end
		for ti, tr := range en.tracks {
			ends = append(ends, end{ti, tr.A, tr.B, ts[ti].iA}, end{ti, tr.B, tr.A, ts[ti].iB})
		}
		for a := 0; a < len(ends); a++ {
			var mates []int
			for b := 0; b < len(ends); b++ {
				if b != a && ends[b].ti != ends[a].ti && en.tracks[ends[b].ti].Layer == en.tracks[ends[a].ti].Layer && dist(ends[a].p, ends[b].p) < 0.5 {
					mates = append(mates, b)
				}
			}
			if len(mates) != 1 || mates[0] < a {
				continue
			}
			e1, e2 := ends[a], ends[mates[0]]
			t1, t2 := en.tracks[e1.ti], en.tracks[e2.ti]
			if dist(e1.p, e1.o) < 1 || dist(e2.p, e2.o) < 1 {
				continue
			}
			u := Point{(e1.o.X - e1.p.X) / dist(e1.p, e1.o), (e1.o.Y - e1.p.Y) / dist(e1.p, e1.o)}
			v := Point{(e2.o.X - e2.p.X) / dist(e2.p, e2.o), (e2.o.Y - e2.p.Y) / dist(e2.p, e2.o)}
			ang := math.Acos(math.Max(-1, math.Min(1, u.X*v.X+u.Y*v.Y))) * 180 / math.Pi
			if ang > 95 {
				continue
			}
			i := math.Min(e1.i, e2.i)
			w := math.Min(t1.W, t2.W)
			k := st.index(t1.Layer)
			tMil := st.Layers[k].CuMm / MilMm
			crowd := 1 + (180-ang)/180
			capA := trackAmpacity(w, tMil, dT)
			sev := ""
			switch {
			case i*crowd > capA:
				sev = "medium"
			case ang < 85 && i >= 0.05:
				sev = "low"
			}
			if sev == "" {
				continue
			}
			need := math.Max(ceilHalf(ipcWidth(i*crowd*margin, tMil, dT)), w)
			add(FBCorner, sev, fmt.Sprintf("%s %s corner %.0f° at (%.0f,%.0f)", nres.Net, st.Layers[k].Name, ang, e1.p.X, e1.p.Y),
				pcbauto.FeedbackEvidence{Metrics: map[string]float64{"angleDeg": round(ang, 1), "currentA": round(i, 4), "crowding": round(crowd, 2),
					"widthMil": w, "capacityA": round(capA, 3), "recommendMil": need},
					Nets: []string{nres.Net}, Points: []pcbauto.FeedbackPoint{{Label: "corner", X: round(e1.p.X, 2), Y: round(e1.p.Y, 2)}},
					Detail: []string{fmt.Sprintf("interior angle %.0f° → inner-edge crowding ×%.2f: %.3f A acts like %.3f A on %.1f mil (capacity %.3f A)", ang, crowd, i, i*crowd, w, capA),
						"tracks " + t1.ID + " / " + t2.ID}},
				fmt.Sprintf("chamfer (two 45° bends) or round the %.0f° corner of %s at (%.0f,%.0f)", ang, nres.Net, e1.p.X, e1.p.Y),
				[]string{"replace the corner by two 45° segments or an arc (radius ≥ 3× width)",
					fmt.Sprintf("or widen both legs to ≥ %.1f mil", need)}, 0.6)
		}
		// Pour necks.
		for _, h := range nres.Hotspots {
			if h.Kind != "sheet" {
				continue
			}
			k := -1
			for kk, l := range st.Layers {
				if l.Name == h.Layer {
					k = kk
				}
			}
			if k < 0 {
				continue
			}
			tMm := st.Layers[k].CuMm
			cellMil := g.Cell
			jLim := trackAmpacity(cellMil, tMm/MilMm, dT) / (cellMil * MilMm * tMm)
			if o.JMax > 0 {
				jLim = math.Min(jLim, o.JMax)
			}
			if h.Value <= jLim {
				continue
			}
			add(FBWiden, "medium", fmt.Sprintf("%s %s pour neck at (%.0f,%.0f)", nres.Net, h.Layer, h.X, h.Y),
				pcbauto.FeedbackEvidence{Metrics: map[string]float64{"jAmm2": h.Value, "limitAmm2": round(jLim, 1), "currentA": h.CurrentA},
					Nets: []string{nres.Net}, Points: []pcbauto.FeedbackPoint{{Label: "neck", X: h.X, Y: h.Y}},
					Detail: []string{fmt.Sprintf("sheet current density %.1f A/mm² > %.1f A/mm² (IPC for a %.2f mm wide strip at ΔT %.0f °C)", h.Value, jLim, cellMil*MilMm, dT)}},
				fmt.Sprintf("widen the %s pour neck on %s around (%.0f,%.0f): move the obstacle, shrink its clearance or add a parallel track", nres.Net, h.Layer, h.X, h.Y),
				[]string{"open the neck (move vias/tracks of other nets out of the channel)", "or stitch the neck with a same-net track / second layer"}, 0.5)
		}
	}
	// Vias.
	for _, v := range res.Vias {
		if v.CurrentA <= 0.5*v.AmpacityA {
			continue
		}
		need := int(math.Ceil(v.CurrentA * margin / v.AmpacityA))
		if need < 2 {
			need = 2
		}
		sev := "low"
		switch {
		case v.CurrentA > v.AmpacityA:
			sev = "high"
		case v.CurrentA > 0.8*v.AmpacityA:
			sev = "medium"
		}
		add(FBVia, sev, fmt.Sprintf("%s via at (%.0f,%.0f) carries %.0f %% of its ampacity", v.Net, v.X, v.Y, v.UsePct),
			pcbauto.FeedbackEvidence{Metrics: map[string]float64{"currentA": v.CurrentA, "ampacityA": v.AmpacityA, "usePct": v.UsePct, "drillMil": v.DrillMil, "viasNeeded": float64(need)},
				Nets: []string{v.Net}, Points: []pcbauto.FeedbackPoint{{Label: "via", X: v.X, Y: v.Y}},
				Detail: []string{fmt.Sprintf("barrel %.1f mil drill, %.2g mil plating: IPC ampacity %.2f A at ΔT %.0f °C (%s)", v.DrillMil, o.PlatingMil, v.AmpacityA, o.ViaDeltaTC, v.Scenario)}},
			fmt.Sprintf("use %d vias (or a larger drill) for this %s layer change", need, v.Net),
			[]string{fmt.Sprintf("add %d via(s) next to via %s", need-1, v.ID)}, 0.8)
	}
	sort.SliceStable(items, func(i, j int) bool { return sevRank(items[i].Severity) < sevRank(items[j].Severity) })
	res.Feedback = items
}

func sevRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	}
	return 2
}

// MergeFeedback appends the post-layout items to a feedback.json document
// (pcb auto's, or a fresh one when raw is empty); previous post-* items are
// replaced.
func MergeFeedback(raw []byte, items []*pcbauto.FeedbackItem) (*pcbauto.Feedback, error) {
	fb := &pcbauto.Feedback{SchemaVersion: 1, Source: "pcbpilot sim post-layout"}
	if len(raw) > 0 {
		if err := jsonUnmarshal(raw, fb); err != nil {
			return nil, fmt.Errorf("feedback: %w", err)
		}
	}
	var keep []*pcbauto.FeedbackItem
	for _, it := range fb.Items {
		if len(it.ID) < 5 || it.ID[:5] != "post-" {
			keep = append(keep, it)
		}
	}
	fb.Items = append(append([]*pcbauto.FeedbackItem{}, keep...), items...)
	rule := "post-layout items (post-*) are copper changes on the routed board: apply them in the layout, then save → reload → pcb dump --include-copper → sim post-layout → DRC before calling them fixed"
	if !contains(fb.Rules, rule) {
		fb.Rules = append(fb.Rules, rule)
	}
	return fb, nil
}
