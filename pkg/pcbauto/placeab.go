package pcbauto

import (
	"context"
	"fmt"
	"math"
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
	// hs and pair count the variant's SI defects: skew / via / split /
	// group-skew findings, and coupling / symmetry findings of the
	// intent-declared pairs.
	hs, pair int
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
	start := snapshotPose(b)
	run := func(po PlaceOptions) (*abVariant, error) {
		pr, err := Place(b, an, c, m, po)
		if err != nil {
			return nil, err
		}
		out, err := Run(ctx, b, ropt)
		if err != nil {
			return nil, err
		}
		js := Joint(b, out.Analysis, c, out.Stackup, out.Route, out.DRC, JointOptions{PlacementScore: -1, Overlaps: pr.Metrics.Overlaps, Isolation: out.Isolation, Edge: out.Edge})
		si := CheckSI(b, out.Analysis, out.Stackup, out.Route)
		_, pair := intentPairDefects(out.Analysis, si)
		return &abVariant{pr: pr, out: out, js: js, pose: snapshotPose(b), hs: hsFindings(si), pair: pair}, nil
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
		return fmt.Sprintf("%.1f%%, plane open %d, DRC %d, electrical %.1f, pair findings %d, HS findings %d", v.out.Route.Stats.Completion, v.js.PlaneOpen, len(v.out.DRC.Violations), v.js.Groups["electrical"], v.pair, v.hs)
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
