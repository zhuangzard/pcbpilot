package app

// kicad_sch_build_pack.go — two-dimensional packing of one page's zone
// frames for `kicad sch-build`.
//
// The row packer (kicadPackZones: frames left to right in reading order, a
// new row when the next frame does not fit) let one wide or tall zone set
// the height of its whole row, and it gave up on a paper as soon as the
// reading order happened not to fit, though another arrangement did: the
// seven field boards (2026-11) came out on papers one or two sizes too large,
// half of each sheet blank. sbPack2D places each frame at the candidate
// corner (beside or under a placed frame) that keeps the union of the
// frames closest to the paper's shape, for the reading order and two
// size-sorted orders, and keeps the most compact result. sbPackFresh takes
// the smallest paper any order fits.

import (
	"math"
	"sort"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

const (
	sbPackGap    = 5.08 // between zone frames, and to the title block
	sbPackMargin = 5.08 // inside the sheet border
	// sbFlagBand is the depth a page's PWR_FLAG row takes under the lowest
	// frame (sbFlagRowBox: 12.7 mm down, symbols 8.89 mm under that, + snap)
	sbFlagBand = 12.7 + 8.89 + 2.54
)

// sbPackItem is one zone frame to place: its content box (zone-relative).
type sbPackItem struct {
	ID  string
	Box kicad.Box
}

// sbPack2D packs the frames of items inside a pw × ph paper (border 10 mm,
// clear of the title block), reserving the PWR_FLAG band under them when
// flagW > 0. It returns each zone's translation (2.54 mm grid) and the
// compactness score (the larger of the union's width and height over the
// usable width and height: ≤ 1, lower = more compact), or ok=false when the
// frames do not fit.
func sbPack2D(items []sbPackItem, pw, ph, flagW float64) (map[string]kicad.Pt, float64, bool) {
	if len(items) == 0 {
		return map[string]kicad.Pt{}, 0, true
	}
	full := kicad.Box{MinX: 10 + sbPackMargin, MinY: 10 + sbPackMargin, MaxX: pw - 10 - sbPackMargin, MaxY: ph - 10 - sbPackMargin}
	// with a PWR_FLAG row, first keep its band free under all frames, then
	// try the full height (the row only needs the band under the lowest
	// frame, and must stay out of the title block: sbFlagRowFits decides)
	areas := []kicad.Box{full}
	if flagW > 0 {
		banded := full
		banded.MaxY -= sbFlagBand
		areas = []kicad.Box{banded, full}
	}
	title := kicad.Box{MinX: pw - 10 - 112, MinY: ph - 10 - 34, MaxX: pw - 10, MaxY: ph - 10}
	orders := [][]sbPackItem{items}
	byH := append([]sbPackItem(nil), items...)
	sort.SliceStable(byH, func(i, j int) bool { return byH[i].Box.H() > byH[j].Box.H() })
	byA := append([]sbPackItem(nil), items...)
	sort.SliceStable(byA, func(i, j int) bool {
		return byA[i].Box.W()*byA[i].Box.H() > byA[j].Box.W()*byA[j].Box.H()
	})
	byW := append([]sbPackItem(nil), items...)
	sort.SliceStable(byW, func(i, j int) bool { return byW[i].Box.W() > byW[j].Box.W() })
	orders = append(orders, byH, byA, byW)
	// each order with two greedy objectives (keep the union in the paper's
	// shape / keep its area small); the result whose union is smallest
	// relative to the paper's shape wins (then the smaller area)
	var best map[string]kicad.Pt
	bestScore, bestArea := math.Inf(1), math.Inf(1)
	for _, area := range areas {
		for _, ord := range orders {
			for _, byArea := range []bool{false, true} {
				out, score, a, ok := sbPackOrder(ord, area, title, flagW, pw, ph, byArea)
				if ok && (score < bestScore-1e-9 || (score <= bestScore+1e-9 && a < bestArea-1e-6)) {
					best, bestScore, bestArea = out, score, a
				}
			}
		}
		if best != nil {
			break
		}
	}
	return best, bestScore, best != nil
}

// sbPackOrder places items in the given order: each at the feasible
// candidate corner with the lowest score: the union's larger relative side,
// then its area (byArea: the other way round), then top-most, left-most.
func sbPackOrder(items []sbPackItem, area, title kicad.Box, flagW, pw, ph float64, byArea bool) (map[string]kicad.Pt, float64, float64, bool) {
	aw, ah := area.W(), area.H()
	if aw <= 0 || ah <= 0 {
		return nil, 0, 0, false
	}
	type pt struct{ X, Y float64 }
	cands := []pt{{area.MinX, area.MinY}}
	var placed []kicad.Box
	union := kicad.Box{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	out := map[string]kicad.Pt{}
	// frames closer than the gap clash; exactly the gap apart is fine (the
	// candidates sit exactly one gap from a frame: without the tolerance
	// floating-point noise rejected them and a page went up a paper size)
	const eps = 1e-6
	hit := func(a, b kicad.Box) bool {
		g := sbPackGap - eps
		return a.MinX < b.MaxX+g && b.MinX < a.MaxX+g && a.MinY < b.MaxY+g && b.MinY < a.MaxY+g
	}
	ceilGrid := func(v float64) float64 { return math.Ceil(v/sbGrid-1e-6) * sbGrid }
	for _, it := range items {
		fp0 := sbFootprint(it.Box, kicad.Pt{})
		bestS, bestA, bestY, bestX := math.Inf(1), math.Inf(1), math.Inf(1), math.Inf(1)
		var bestT kicad.Pt
		var bestR kicad.Box
		found := false
		for _, c := range cands {
			// the zone origin on the grid, its frame at or right/below c
			t := kicad.Pt{X: ceilGrid(c.X - fp0.MinX), Y: ceilGrid(c.Y - fp0.MinY)}
			r := sbFootprint(it.Box, t)
			if r.MinX < area.MinX-1e-6 || r.MinY < area.MinY-1e-6 || r.MaxX > area.MaxX+1e-6 || r.MaxY > area.MaxY+1e-6 {
				continue
			}
			if hit(r, title) {
				continue
			}
			clash := false
			for _, q := range placed {
				if hit(r, q) {
					clash = true
					break
				}
			}
			if clash {
				continue
			}
			u := sbBoxUnion(union, r)
			s := math.Max((u.MaxX-area.MinX)/aw, (u.MaxY-area.MinY)/ah)
			a := (u.MaxX - area.MinX) * (u.MaxY - area.MinY)
			if byArea {
				s, a = a/(aw*ah), s
			}
			better := s < bestS-1e-9 ||
				(math.Abs(s-bestS) <= 1e-9 && (a < bestA-1e-6 ||
					(math.Abs(a-bestA) <= 1e-6 && (r.MinY < bestY-1e-6 || (math.Abs(r.MinY-bestY) <= 1e-6 && r.MinX < bestX)))))
			if better {
				bestS, bestA, bestY, bestX, bestT, bestR, found = s, a, r.MinY, r.MinX, t, r, true
			}
		}
		if !found {
			return nil, 0, 0, false
		}
		out[it.ID] = bestT
		placed = append(placed, bestR)
		union = sbBoxUnion(union, bestR)
		cands = append(cands,
			pt{bestR.MaxX + sbPackGap, bestR.MinY},
			pt{bestR.MinX, bestR.MaxY + sbPackGap},
			pt{area.MinX, bestR.MaxY + sbPackGap},
			pt{bestR.MaxX + sbPackGap, area.MinY})
		// corners against every placed frame's far sides at this frame's
		// edges: fills the holes a tall neighbour leaves
		for _, q := range placed[:len(placed)-1] {
			cands = append(cands, pt{q.MaxX + sbPackGap, bestR.MaxY + sbPackGap}, pt{bestR.MaxX + sbPackGap, q.MaxY + sbPackGap})
		}
	}
	if !sbFlagRowFits(placed, flagW, pw, ph) {
		return nil, 0, 0, false
	}
	return out, math.Max((union.MaxX-area.MinX)/aw, (union.MaxY-area.MinY)/ah), (union.MaxX - area.MinX) * (union.MaxY - area.MinY), true
}
