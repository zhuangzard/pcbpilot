package pcbauto

import (
	"context"
	"fmt"
	"math"
	"strings"
)

// Corridor A/B: a placement must never lose routability to the pair
// corridors.
//
// The corridor terms (placer_corridor.go) are a hypothesis about the routed
// board: keep the pairs' runs clear and their in-line parts turned with the
// flow, and the pairs couple. Where the breakout has no room for it — a
// dense connector pinout, an IC carrying several pairs — the same forces
// shift parts into the next pair's path, and a leg finds no legal path
// (PCIe M.2 seeds 5/6: 84.6 / 92.3 % where the plain placement routed
// 100 %). The router is the only judge: a board with corridors is placed
// twice, with and without them, both are routed, and the better result is
// kept — safety first, then completion, open plane connections, DRC, the
// electrical group of the joint score (never at the cost of a pair check)
// and the SI findings; a tie keeps the corridors (abBetter).

// boardPose is the part poses and placement-owned board state of one
// variant, to switch the board between variants.
type boardPose struct {
	poses    map[*Part]savedPart
	outline  []Point
	holes    []*Hole
	keepouts []*Keepout
}

type savedPart struct {
	pos Point
	rot float64
}

func snapshotPose(b *Board) *boardPose {
	s := &boardPose{poses: map[*Part]savedPart{}, outline: append([]Point(nil), b.Outline...),
		holes: append([]*Hole(nil), b.Holes...), keepouts: append([]*Keepout(nil), b.Keepouts...)}
	for _, p := range b.Parts {
		s.poses[p] = savedPart{p.Pos, p.Rotation}
	}
	return s
}

func (s *boardPose) restore(b *Board) {
	for p, ps := range s.poses {
		p.MoveTo(ps.pos, ps.rot)
	}
	b.Outline = append([]Point(nil), s.outline...)
	b.Holes = append([]*Hole(nil), s.holes...)
	b.Keepouts = append([]*Keepout(nil), s.keepouts...)
	_ = b.Index()
}

// abVariant is one routed placement.
type abVariant struct {
	pr   *PlaceResult
	out  *Result
	js   *JointScore
	pose *boardPose
	// pre is the board before routing (the aesthetics guard re-routes the
	// same board with the pre-aesthetics poses).
	pre *boardPose
	// hs and pair count the variant's SI defects: skew / via / split /
	// group-skew findings, and coupling / symmetry findings of the
	// intent-declared pairs.
	hs, pair int
	// si is the variant's SI report (nil in unit tests).
	si *SIReport
}

// facts are the routed figures the aesthetics guard judges v on.
func (v *abVariant) facts() aesFacts {
	return factsOf(v.js, v.out.Route, v.out.DRC, v.out.Isolation, v.out.Edge, v.si, v.pair, v.hs)
}

// abBetter reports that the plain variant y beats the corridor variant x:
// fewer safety gates, then more signal completion, fewer open plane
// connections, fewer DRC violations; then — only where y costs no pair
// check, the corridors' own purpose (ESP32 mini seed 4: the plain
// placement scored 5 points better on decoupling and lost the USB pair's
// via and layer symmetry) — a better electrical group by more than half a
// point; within that half point, fewer SI defects (PCIe M.2 seed 4:
// electrical 61.0 vs 61.2, 5 vs 4 skew/via findings). A tie keeps the
// corridors.
func abBetter(x, y *abVariant) bool {
	if gx, gy := len(x.js.Gates), len(y.js.Gates); gx != gy {
		return gy < gx
	}
	if cx, cy := x.out.Route.Stats.Completion, y.out.Route.Stats.Completion; cx != cy {
		return cy > cx
	}
	if ox, oy := x.js.PlaneOpen, y.js.PlaneOpen; ox != oy {
		return oy < ox
	}
	if dx, dy := len(x.out.DRC.Violations), len(y.out.DRC.Violations); dx != dy {
		return dy < dx
	}
	if y.pair > x.pair {
		return false
	}
	ex, ey := x.js.Groups["electrical"], y.js.Groups["electrical"]
	if math.Abs(ex-ey) > 0.5 {
		return ey > ex
	}
	return y.hs+y.pair < x.hs+x.pair
}

// placeRouteAB places and routes b; with intent-declared pairs it also
// places and routes without their corridors and keeps the better (see
// above). The board is left at the kept placement. corridors reports
// whether the kept placement is the corridor one.
func placeRouteAB(ctx context.Context, b *Board, an *Analysis, c *Circuit, m *Mechanics, popt PlaceOptions, ropt Options) (v *abVariant, corridors bool, err error) {
	return placeRouteCorridors(ctx, b, an, c, m, popt, ropt)
}

// routeVariant routes the board as it stands and scores the result.
func routeVariant(ctx context.Context, b *Board, an *Analysis, c *Circuit, pr *PlaceResult, ropt Options) (*abVariant, error) {
	pre := snapshotPose(b)
	out, err := Run(ctx, b, ropt)
	if err != nil {
		return nil, err
	}
	js := Joint(b, out.Analysis, c, out.Stackup, out.Route, out.DRC, JointOptions{PlacementScore: -1, Overlaps: pr.Metrics.Overlaps, Isolation: out.Isolation, Edge: out.Edge})
	si := CheckSI(b, out.Analysis, out.Stackup, out.Route)
	_, pair := intentPairDefects(out.Analysis, si)
	return &abVariant{pr: pr, out: out, js: js, pose: snapshotPose(b), pre: pre, hs: hsFindings(si), pair: pair, si: si}, nil
}

// placeRouteCorridors is the corridor A/B of placeRouteAB.
func placeRouteCorridors(ctx context.Context, b *Board, an *Analysis, c *Circuit, m *Mechanics, popt PlaceOptions, ropt Options) (v *abVariant, corridors bool, err error) {
	start := snapshotPose(b)
	run := func(po PlaceOptions) (*abVariant, error) {
		pr, err := Place(b, an, c, m, po)
		if err != nil {
			return nil, err
		}
		v, err := routeVariant(ctx, b, an, c, pr, ropt)
		if err != nil {
			return nil, err
		}
		// Each variant is judged with and without its aesthetics stage
		// before the corridor comparison: the stage must never decide
		// which corridor variant wins.
		return aesGuard(ctx, b, an, c, v, ropt), nil
	}
	a, err := run(popt)
	if err != nil || popt.NoCorridors || a.pr.Metrics.Corridors == 0 {
		return a, err == nil && a != nil && a.pr.Metrics.Corridors > 0, err
	}
	// The plain placement starts from the same board as the corridor one.
	start.restore(b)
	po := popt
	po.NoCorridors = true
	plain, err := run(po)
	if err != nil {
		if ctx.Err() != nil {
			// Out of time: the corridor result stands.
			a.pose.restore(b)
			a.pr.Notes = append(a.pr.Notes, "pair corridors: kept (no time left to route the plain placement)")
			return a, true, nil
		}
		return nil, false, err
	}
	desc := func(v *abVariant) string {
		d := fmt.Sprintf("%.1f%%, plane open %d, DRC %d, electrical %.1f, pair findings %d, HS findings %d", v.out.Route.Stats.Completion, v.js.PlaneOpen, len(v.out.DRC.Violations), v.js.Groups["electrical"], v.pair, v.hs)
		if ar := v.pr.Aesthetics; ar != nil && ar.Guard != "" {
			g := ar.Guard
			if i := strings.Index(g, ":"); i > 0 {
				g = g[:i]
			}
			d += ", aesthetics " + g
		}
		return d
	}
	if abBetter(a, plain) {
		plain.pr.Notes = append(plain.pr.Notes, fmt.Sprintf("pair corridors: dropped — the plain placement routed better (%s; with %d corridor(s) %s)", desc(plain), a.pr.Metrics.Corridors, desc(a)))
		return plain, false, nil
	}
	a.pose.restore(b)
	a.pr.Notes = append(a.pr.Notes, fmt.Sprintf("pair corridors: kept (%d corridor(s): %s; plain placement %s)", a.pr.Metrics.Corridors, desc(a), desc(plain)))
	return a, true, nil
}

// PlaceThenRoute places b once and routes it (pcb auto run --place
// --loops 0): with intent-declared pairs, the corridor and the plain
// placement are both routed and the better kept (placeRouteAB). The board
// is left at the kept placement.
func PlaceThenRoute(ctx context.Context, b *Board, an *Analysis, c *Circuit, m *Mechanics, popt PlaceOptions, ropt Options) (*PlaceResult, *Result, error) {
	v, _, err := placeRouteAB(ctx, b, an, c, m, popt, ropt)
	if err != nil {
		return nil, nil, err
	}
	return v.pr, v.out, nil
}

// Aesthetics guard: the placement aesthetics stage (placer_aes.go) judges
// every move on the placer's own terms; the router is the judge of the
// result. A placement the stage changed is routed a second time with the
// poses it had before the stage, and the aesthetic one is kept only when it
// routes no worse on every zero-tolerance count — safety (gates, isolation,
// board edge, via current, blockers, deliverability), completion, open
// plane connections, DRC, SI / pair / high-speed finding counts, power
// nets over their IR budget — adds at most the profile's
// PlacementViaAllowance vias (1; only when the electrical group does not
// drop) and lowers no electrical item of the joint score by more than the
// profile's ElectricalTol (aesthetics_tolerance.go; never below the 0.05
// rounding of the report). Every item it does lower, and a used via
// allowance, is reported as a trade. Otherwise the board rolls back to the
// next rung, and the report says why.

// aesRoutedWorse names the first count on which the aesthetic variant routes
// worse than the raw one beyond the tolerance tol and the via allowance
// allow ("" = acceptable), and the electrical trades it makes within them.
func aesRoutedWorse(aes, raw *abVariant, tol float64, allow int) (string, []AesTrade) {
	return aesJudge(aes.facts(), raw.facts(), aesJudgeOpt{Tol: tol, Round: aesScoreRound, ViaAllowance: allow})
}

// aesHoldRadius is how far around a regressed item the held-back rerun
// keeps parts still (mil, body to body).
const aesHoldRadius = 250.0

// aesHoldFor names the parts behind the first count on which v routed worse
// (why, from aesRoutedWorse) and every part within aesHoldRadius of them
// (for a via increase: the parts of the small nets that gained vias over
// raw). Empty for safety gates, blockers and board-wide items (IR drop): those go
// straight to the grid-only stage.
func aesHoldFor(b *Board, an *Analysis, c *Circuit, v, raw *abVariant, why string) map[*Part]bool {
	var anchors []*Part
	add := func(ref string) {
		if p := b.Part(ref); p != nil {
			anchors = append(anchors, p)
		}
	}
	role := func(roles ...string) {
		for _, bl := range c.Blocks {
			for _, m := range bl.Members {
				if containsStr(roles, m.Role) {
					add(m.Ref)
					add(bl.Core)
				}
			}
		}
	}
	pairNets := func() {
		for _, p := range b.Parts {
			for _, pd := range p.Pads {
				if np := an.ByNet[pd.Net]; np != nil && (np.PairWith != "" || np.Role == RoleDiff) {
					anchors = append(anchors, p)
					break
				}
			}
		}
	}
	switch {
	case strings.HasPrefix(why, "completion"), strings.HasPrefix(why, "open plane"):
		for _, u := range v.out.Route.Unrouted {
			for _, k := range u.Pads {
				if pd := padAt(b, k); pd != nil {
					add(pd.Part)
				}
			}
		}
	case strings.HasPrefix(why, "DRC"):
		for _, vi := range v.out.DRC.Violations {
			for _, p := range b.Parts {
				if distToRect(vi.At, p.Body()) <= 30 {
					anchors = append(anchors, p)
				}
			}
		}
	case strings.HasPrefix(why, "pair findings"), strings.HasPrefix(why, "high-speed"),
		strings.HasPrefix(why, "electrical diff-pair"), strings.HasPrefix(why, "electrical high-speed"):
		pairNets()
	case strings.HasPrefix(why, "vias"):
		// The parts of the nets that gained vias (nets of more than six
		// parts — ground, rails — would hold half the board still).
		count := func(x *abVariant) map[string]int {
			m := map[string]int{}
			for _, vi := range x.out.Route.Vias {
				m[vi.Net]++
			}
			return m
		}
		var rawCount map[string]int
		if raw != nil {
			rawCount = count(raw)
		}
		for net, n := range count(v) {
			if n <= rawCount[net] {
				continue
			}
			var ps []*Part
			seen := map[*Part]bool{}
			for _, p := range b.Parts {
				for _, pd := range p.Pads {
					if pd.Net == net && !seen[p] {
						seen[p] = true
						ps = append(ps, p)
					}
				}
			}
			if len(ps) <= 6 {
				anchors = append(anchors, ps...)
			}
		}
	case strings.HasPrefix(why, "electrical hot-loop"):
		for _, cv := range c.Converters {
			for _, r := range append([]string{cv.Core, cv.Inductor, cv.HotCap, cv.Diode, cv.Bootstrap}, cv.Feedback...) {
				add(r)
			}
		}
	case strings.HasPrefix(why, "electrical decap-loop"):
		role("decap")
	case strings.HasPrefix(why, "electrical esd-stub"):
		role("protection")
	case strings.HasPrefix(why, "electrical rf-feed"):
		for _, bl := range c.Blocks {
			if bl.Kind == "rf" {
				for _, r := range bl.Parts {
					add(r)
				}
			}
		}
	}
	if len(anchors) == 0 {
		return nil
	}
	hold := map[*Part]bool{}
	for _, p := range b.Parts {
		pb := p.Body()
		for _, a := range anchors {
			if rectDist(pb, a.Body()) <= aesHoldRadius {
				hold[p] = true
				break
			}
		}
	}
	return hold
}

// aesGuard routes v's pre-aesthetics placement and keeps v only when it
// routes no worse; otherwise it tries the grid-only stage (fold + snap,
// no slack) against the same reference, and finally falls back to the
// pre-aesthetics placement. The board is left at the kept placement.
func aesGuard(ctx context.Context, b *Board, an *Analysis, c *Circuit, v *abVariant, ropt Options) *abVariant {
	ar := v.pr.Aesthetics
	if ar == nil || ar.raw == nil || v.pre == nil {
		return v
	}
	if ctx.Err() != nil {
		ar.Guard = "kept unverified: no time left to route the placement without the aesthetics stage"
		return v
	}
	desc := func(x *abVariant) string {
		return fmt.Sprintf("%.1f%%, plane open %d, DRC %d, vias %d, electrical %.1f, gates %d", x.out.Route.Stats.Completion, x.js.PlaneOpen, len(x.out.DRC.Violations), len(x.out.Route.Vias), x.js.Groups["electrical"], len(x.js.Gates))
	}
	tol, allow := ar.ElectricalTol, ar.ViaAllowance
	// traded renders the electrical trades of a kept rung (never silent).
	traded := func(ts []AesTrade) string {
		if len(ts) == 0 {
			return "; no electrical item traded"
		}
		return "; traded: " + AesTradesText(ts)
	}
	// variant routes the board at poses a (from the pre-routing board).
	variant := func(a *aesPoses) (*abVariant, error) {
		v.pre.restore(b)
		a.apply(b)
		pr := *v.pr
		pr.Placements, pr.Metrics = a.place, a.metrics
		if a.outline != nil {
			pr.Outline = a.outline
		}
		pr.Notes = append([]string(nil), v.pr.Notes...)
		return routeVariant(ctx, b, an, c, &pr, ropt)
	}
	raw, err := variant(ar.raw)
	if err != nil {
		v.pose.restore(b)
		ar.Guard = "kept unverified: routing the placement without the aesthetics stage failed (" + err.Error() + ")"
		return v
	}
	why, trades := aesRoutedWorse(v, raw, tol, allow)
	if why == "" {
		v.pose.restore(b)
		ar.Trades = trades
		ar.Guard = fmt.Sprintf("kept: routes no worse than without the stage within the %.2g-point electrical tolerance and the %d-via allowance (with %s, without %s)%s", tol, allow, desc(v), desc(raw), traded(trades))
		v.pr.Notes = append(v.pr.Notes, "aesthetics "+ar.Guard)
		return v
	}
	full := fmt.Sprintf("the full stage routes worse (%s; with %s, without %s)", why, desc(v), desc(raw))
	// Second choice: the stage again, holding still every part near what
	// got worse (the parts behind the failing item and their surroundings).
	if hold := aesHoldFor(b, an, c, v, raw, why); len(hold) > 0 && ar.rerun != nil && ctx.Err() == nil {
		v.pre.restore(b)
		local := ar.rerun(hold)
		if local.rep != nil && local.rep.Moved > 0 {
			if hv, err := variant(local); err == nil {
				if hwhy, htr := aesRoutedWorse(hv, raw, tol, allow); hwhy == "" {
					hr := *local.rep
					hr.ElectricalTol, hr.ViaAllowance, hr.Trades = tol, allow, htr
					hr.Guard = fmt.Sprintf("kept with %d part(s) near the regression held still: %s; held-back stage %s%s", len(hold), full, desc(hv), traded(htr))
					hr.raw, hr.lites, hr.rerun = nil, nil, nil
					hv.pr.Aesthetics = &hr
					hv.pr.Notes = append(hv.pr.Notes, "aesthetics "+hr.Guard)
					return hv
				} else {
					full += fmt.Sprintf("; held back near it (%d parts) too (%s)", len(hold), hwhy)
				}
			}
		}
	}
	for _, lite := range ar.lites {
		if ctx.Err() != nil {
			break
		}
		name := "grid-only"
		if lite.rep.Strict {
			name = "strict grid-only"
		}
		lv, err := variant(lite)
		if err != nil {
			continue
		}
		if lwhy, ltr := aesRoutedWorse(lv, raw, tol, allow); lwhy == "" {
			lr := *lite.rep
			lr.ElectricalTol, lr.ViaAllowance, lr.Trades = tol, allow, ltr
			lr.Guard = name + " stage kept: " + full + fmt.Sprintf("; %s %s%s", name, desc(lv), traded(ltr))
			lr.raw, lr.lites, lr.rerun = nil, nil, nil
			lv.pr.Aesthetics = &lr
			lv.pr.Notes = append(lv.pr.Notes, "aesthetics "+lr.Guard)
			return lv
		} else {
			full += fmt.Sprintf("; the %s stage too (%s)", name, lwhy)
		}
	}
	raw.pose.restore(b)
	ar.Guard = "rolled back: " + full
	raw.pr.Notes = append(raw.pr.Notes, "aesthetics "+ar.Guard)
	return raw
}
