package pcbauto

// The electrical-item tolerance of the aesthetics stages (user decision
// 2026-10-02, "option B").
//
// Both aesthetics stages are judged by a routed comparison against the
// same board without them: the placement stage by the routed guard
// (placeab.go: full stage → held-back rerun → grid-only → strict grid-only
// → pre-stage placement), the post-route beautification by its gate
// (beautify.go). Routing responds chaotically to 2–5 mil placement moves
// (baseline.md §11.4: moving one diode 1.9 mil cost the hot loop 2.7
// points), so "no electrical sub-score may get worse by any amount" threw
// away whole aesthetic stages for routing noise.
//
// The rule now: an aesthetic stage may lower each ELECTRICAL SUB-SCORE of
// the joint score (hot loop, decap loop, ESD stub, RF feed, IR-drop score,
// diff-pair score, high-speed score …) by at most the profile's
// ElectricalTol (AesElectricalTol = 0.5 points for balanced and precision;
// functional 0 — it spends no slack anyway). Every such trade is reported
// (AesTrade, in the stage's report, the result notes and report.md); it is
// never silent.
//
// Never traded, whatever the tolerance (zero tolerance):
//   - safety: isolation creepage/clearance findings and infeasible bridges,
//     board-edge findings (HV edge bands and the fabrication edge rule),
//     via-current shortfalls, safety gates, delivery blockers and the
//     deliverability verdict;
//   - completion, disconnected connections, open plane connections, DRC;
//   - SI finding counts (all SI findings, intent-pair findings, high-speed
//     defects) and the isolation finding count;
//   - the via count (may not increase);
//   - copper narrower than its current (Σ deficit × length; the beautify
//     gate, which edits the copper of one placement);
//   - raw IR drop on power nets: no net may end over its drop budget where
//     it got worse (and the over-budget / open count may not grow). Within
//     budget, the IR-drop score item carries the 0.5-point tolerance and
//     the raw increase is reported as a trade.
//
// ConstraintPriority (joint.go) still ranks aesthetics last: the tolerance
// is a bounded, reported allowance against routing noise, not a re-ranking.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// AesElectricalTol is THE electrical sub-score tolerance of the aesthetics
// stages (score points per joint-score electrical item). Profiles carry it
// in AesProfile.ElectricalTol; a custom style may lower it, never raise it.
const AesElectricalTol = 0.5

// aesScoreRound is the rounding floor of the placement guard: the report
// prints scores to 0.1, so differences below 0.05 were never "worse" there
// (functional keeps exactly this). The beautify gate has no floor.
const aesScoreRound = 0.05

// AesTrade is one electrical figure an aesthetic stage made worse within
// the tolerance: an electrical item of the joint score (score points), or
// the raw IR drop of a power net inside its budget (mV).
type AesTrade struct {
	Item string  `json:"item"`
	From float64 `json:"from"`
	To   float64 `json:"to"`
	// Tolerance is the allowance the trade was judged against (points;
	// for a raw IR drop, the net's budget in mV).
	Tolerance float64 `json:"tolerance"`
	Unit      string  `json:"unit,omitempty"` // "" score points | "mV"
}

// String renders the trade the way the notes and report.md print it, e.g.
// "decap loop −0.08 (82.48 → 82.40, ≤0.5 tolerance)".
func (t AesTrade) String() string {
	d := t.From - t.To
	prec := 2
	if d < 0.01 {
		prec = 3
	}
	if t.Unit == "mV" {
		return fmt.Sprintf("IR drop of %s +%.*f mV (%.2f → %.2f mV, within its %.0f mV budget)", strings.TrimPrefix(t.Item, "ir:"), prec, -d, t.From, t.To, t.Tolerance)
	}
	return fmt.Sprintf("%s −%.*f (%.2f → %.2f, ≤%g tolerance)", strings.ReplaceAll(t.Item, "-", " "), prec, d, t.From, t.To, t.Tolerance)
}

// AesTradesText joins trades for a note ("" when none).
func AesTradesText(ts []AesTrade) string {
	var s []string
	for _, t := range ts {
		s = append(s, t.String())
	}
	return strings.Join(s, "; ")
}

// aesElectricalTol is the tolerance of a profile (nil = the default
// profile).
func aesElectricalTol(p *AesProfile) float64 {
	if p == nil {
		d := AesProfiles[DefaultAesProfile]
		return d.ElectricalTol
	}
	return p.ElectricalTol
}

// aesFacts are the routed figures an aesthetic stage is judged on.
type aesFacts struct {
	// safety and delivery
	gates, blockers            int
	isoFindings, isoInfeasible int
	edgeErrors, viaShort       int
	undeliverable              bool
	// completion and rules
	completion                   float64
	disconnected, planeOpen, drc int
	si, pair, hs, vias           int
	underWidth                   float64
	irViol                       int
	irMV, irBudget               map[string]float64
	electrical                   float64
	items                        map[string]float64
}

// factsOf collects the facts of a routed result. si may be nil (counts
// then come from pair / hs only).
func factsOf(js *JointScore, rr *RouteResult, drc *DRCReport, iso *IsolationReport, edge *EdgeCheck, si *SIReport, pair, hs int) aesFacts {
	f := aesFacts{items: map[string]float64{}, pair: pair, hs: hs}
	if js != nil {
		f.gates, f.blockers, f.planeOpen = len(js.Gates), len(js.Blockers), js.PlaneOpen
		f.undeliverable = len(js.NotDeliverable()) > 0
		f.electrical = js.Groups["electrical"]
		for _, it := range js.Items {
			if it.Group == "electrical" {
				f.items[it.ID] = it.Score
			}
		}
	}
	if iso != nil {
		f.isoFindings, f.isoInfeasible = len(iso.Findings), len(iso.Infeasible)
	}
	if edge != nil {
		f.edgeErrors = edge.Errors()
	}
	if si != nil {
		f.si = len(si.Findings)
	}
	if drc != nil {
		f.drc, f.disconnected = len(drc.Violations), len(drc.Disconnected)
	}
	if rr != nil {
		f.completion, f.vias, f.viaShort = rr.Stats.Completion, len(rr.Vias), len(rr.ViaShortfalls)
		if pw := rr.Power; pw != nil {
			f.irViol = pw.Violations()
			f.irMV, f.irBudget = map[string]float64{}, map[string]float64{}
			for _, n := range pw.Nets {
				f.irMV[n.Net], f.irBudget[n.Net] = n.WorstMV, n.BudgetMV
				for _, sg := range n.Segments {
					if sg.WidthMil < sg.NeedMil-1e-6 {
						f.underWidth += (sg.NeedMil - sg.WidthMil) * sg.LengthMil
					}
				}
			}
		}
	}
	return f
}

// aesJudgeOpt sets how an aesthetic result is judged.
type aesJudgeOpt struct {
	// Tol is the per-item electrical tolerance (points).
	Tol float64
	// Round is the floor below which a score difference is not "worse"
	// (placement guard aesScoreRound; beautify gate ~0).
	Round float64
	// SameLayout: a and b route the same placement (the beautify gate, a
	// local copper edit). The copper narrower than its current may not
	// grow, a net without a drop budget may not drop more, and at
	// tolerance 0 no raw IR drop may grow at all. Between two placements
	// (the routed guard) those raw figures move with routing noise: only
	// the over-budget rule applies there, and the IR-drop score item.
	SameLayout bool
}

// aesJudge compares the aesthetic result a against the reference b. why
// names the first count on which a is worse beyond what may be traded
// ("" = acceptable); trades lists every electrical figure a made worse
// within the tolerance (reported, never silent).
func aesJudge(a, b aesFacts, o aesJudgeOpt) (why string, trades []AesTrade) {
	const eps = 1e-9
	switch {
	case a.gates > b.gates:
		return fmt.Sprintf("safety gates %d > %d", a.gates, b.gates), nil
	case a.isoFindings > b.isoFindings:
		return fmt.Sprintf("isolation findings %d > %d", a.isoFindings, b.isoFindings), nil
	case a.isoInfeasible > b.isoInfeasible:
		return fmt.Sprintf("isolation infeasible bridges %d > %d", a.isoInfeasible, b.isoInfeasible), nil
	case a.edgeErrors > b.edgeErrors:
		return fmt.Sprintf("board-edge findings %d > %d", a.edgeErrors, b.edgeErrors), nil
	case a.viaShort > b.viaShort:
		return fmt.Sprintf("via-current shortfalls %d > %d", a.viaShort, b.viaShort), nil
	case a.blockers > b.blockers:
		return fmt.Sprintf("delivery blockers %d > %d", a.blockers, b.blockers), nil
	case a.completion < b.completion-eps:
		return fmt.Sprintf("completion %.1f%% < %.1f%%", a.completion, b.completion), nil
	case a.disconnected > b.disconnected:
		return fmt.Sprintf("disconnected %d > %d", a.disconnected, b.disconnected), nil
	case a.planeOpen > b.planeOpen:
		return fmt.Sprintf("open plane connections %d > %d", a.planeOpen, b.planeOpen), nil
	case a.drc > b.drc:
		return fmt.Sprintf("DRC %d > %d", a.drc, b.drc), nil
	case a.pair > b.pair:
		return fmt.Sprintf("pair findings %d > %d", a.pair, b.pair), nil
	case a.hs > b.hs:
		return fmt.Sprintf("high-speed findings %d > %d", a.hs, b.hs), nil
	case a.si > b.si:
		return fmt.Sprintf("SI findings %d > %d", a.si, b.si), nil
	case a.vias > b.vias:
		return fmt.Sprintf("vias %d > %d", a.vias, b.vias), nil
	case a.irViol > b.irViol:
		return fmt.Sprintf("IR-drop violations %d > %d", a.irViol, b.irViol), nil
	case a.undeliverable && !b.undeliverable:
		// Last of the counts: a specific cause above names the parts.
		return "deliverability: deliverable → not deliverable", nil
	case o.SameLayout && a.underWidth > b.underWidth*(1+1e-6)+1e-6:
		return fmt.Sprintf("copper narrower than its current %.1f > %.1f mil²", a.underWidth, b.underWidth), nil
	}
	nets := make([]string, 0, len(b.irMV))
	for n := range b.irMV {
		nets = append(nets, n)
	}
	sort.Strings(nets)
	for _, n := range nets {
		v, ok := a.irMV[n]
		if !ok || v <= b.irMV[n]*(1+1e-6)+1e-6 {
			continue
		}
		budget := a.irBudget[n]
		if budget <= 0 && !o.SameLayout {
			continue // no budget to exceed: the IR-drop score item judges it
		}
		if o.SameLayout && o.Tol <= 0 || budget <= 0 || v > budget {
			return fmt.Sprintf("IR drop of %s %.2f → %.2f mV", n, b.irMV[n], v), nil
		}
		trades = append(trades, AesTrade{Item: "ir:" + n, From: b.irMV[n], To: v, Tolerance: budget, Unit: "mV"})
	}
	tol := math.Max(o.Tol, o.Round)
	ids := make([]string, 0, len(b.items))
	for id := range b.items {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var itemTrades []AesTrade
	for _, id := range ids {
		v, ok := a.items[id]
		if !ok {
			return fmt.Sprintf("electrical %s missing", id), nil
		}
		d := b.items[id] - v
		if d > tol+eps {
			return fmt.Sprintf("electrical %s %.2f < %.2f (−%.2f > %.2g tolerance)", id, v, b.items[id], d, tol), nil
		}
		if d > eps {
			itemTrades = append(itemTrades, AesTrade{Item: id, From: b.items[id], To: v, Tolerance: tol})
		}
	}
	if d := b.electrical - a.electrical; d > tol+eps {
		return fmt.Sprintf("electrical group %.2f < %.2f", a.electrical, b.electrical), nil
	}
	return "", append(itemTrades, trades...)
}
