package app

// pcb_silk_overlap.go — designator-vs-designator silk overlap, judged on the
// connector's REAL rendered bboxes (pcb.silk.list), shared by the `pcb check`
// silk-overlap rule and the `pcb silk-align` convergence loop.
//
// Why both need it: on 2026-09-25 (ceshi E2E, desktop V3 3.2.149, connector
// 0.2.9) `pcb silk-align` reported 0 unresolved while the fresh dump showed
// seven designator pairs printed on top of each other (C1/U4, C2/C3, C4/C5,
// C6/R3, C7/R6, R5/R9, R8/U3), and `pcb check` had no rule that could see it.
// The connector's slot scorer let one label overlap pass as "clean" (see
// silkScoreSlot in extension/src/actions.ts); independent of that bug, the
// only trustworthy verdict is the readback, so the CLI now re-aligns against it.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// silkOverlapPair is two visible designators whose rendered boxes intersect.
type silkOverlapPair struct {
	A, B pcbSilkText
	Area float64 // intersection area, mil²
}

// isVisibleDesignator: a component designator attribute that is actually
// printed (rendered box known, not hidden) on a silk layer.
func isVisibleDesignator(t pcbSilkText) bool {
	if t.Hidden || t.BBox == nil || t.Kind != "attribute" || !strings.EqualFold(t.Key, "Designator") {
		return false
	}
	if strings.TrimSpace(t.Text) == "" {
		return false
	}
	return t.Layer == silkTopLayer || t.Layer == silkBottomLayer
}

// designatorSilkOverlaps lists every pair of visible designators on the same
// silk layer whose rendered boxes share positive area. Touching edges are not
// an overlap. Sorted by designator text for deterministic output.
func designatorSilkOverlaps(silk []pcbSilkText) []silkOverlapPair {
	var ds []pcbSilkText
	for _, t := range silk {
		if isVisibleDesignator(t) {
			ds = append(ds, t)
		}
	}
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].Text < ds[j].Text })
	var out []silkOverlapPair
	for i := 0; i < len(ds); i++ {
		for j := i + 1; j < len(ds); j++ {
			a, b := ds[i], ds[j]
			if a.Layer != b.Layer {
				continue
			}
			w := math.Min(a.BBox.MaxX, b.BBox.MaxX) - math.Max(a.BBox.MinX, b.BBox.MinX)
			h := math.Min(a.BBox.MaxY, b.BBox.MaxY) - math.Max(a.BBox.MinY, b.BBox.MinY)
			if w <= pcbCoincEps || h <= pcbCoincEps {
				continue
			}
			out = append(out, silkOverlapPair{A: a, B: b, Area: w * h})
		}
	}
	return out
}

// findSilkOverlap is the `pcb check` silk-overlap rule: WARN per overlapping
// designator pair (two labels printed on top of each other read as neither).
func findSilkOverlap(silk []pcbSilkText) []pcbCheckFinding {
	var out []pcbCheckFinding
	for _, p := range designatorSilkOverlaps(silk) {
		a, b := p.A, p.B
		cx := (math.Max(a.BBox.MinX, b.BBox.MinX) + math.Min(a.BBox.MaxX, b.BBox.MaxX)) / 2
		cy := (math.Max(a.BBox.MinY, b.BBox.MinY) + math.Min(a.BBox.MaxY, b.BBox.MaxY)) / 2
		side := "top"
		if a.Layer == silkBottomLayer {
			side = "bottom"
		}
		out = append(out, pcbCheckFinding{
			Type: "silk-overlap", Level: "WARN", Layer: a.Layer,
			Designator: a.Text, Primitives: []string{a.ID, b.ID},
			At: &pcbXY{round2(cx), round2(cy)},
			Message: fmt.Sprintf("designators %s and %s overlap on %s silk (%.0f mil², boxes [%.1f,%.1f,%.1f,%.1f] vs [%.1f,%.1f,%.1f,%.1f]) — neither reads; re-run `pcb silk-align --refs %s --refs %s` or place one with `pcb silk-set`%s",
				a.Text, b.Text, side, p.Area,
				a.BBox.MinX, a.BBox.MinY, a.BBox.MaxX, a.BBox.MaxY,
				b.BBox.MinX, b.BBox.MinY, b.BBox.MaxX, b.BBox.MaxY,
				a.Text, b.Text, docRule("11.2", "丝印清晰可读")),
		})
	}
	return out
}

// ── silk-align convergence loop ─────────────────────────────────────────────

// silkActionFunc is the typed action round-trip (requestAction in production,
// a fake board in tests).
type silkActionFunc func(action string, payload map[string]any) (map[string]any, error)

// silkAlignRound is one pcb.silk.align pass plus the readback it was judged on.
type silkAlignRound struct {
	Round    int            `json:"round"`
	Refs     []string       `json:"refs,omitempty"` // empty = the caller's full scope
	Result   map[string]any `json:"result"`
	Overlaps [][2]string    `json:"overlapsAfter"`
}

// silkAlignReport is what `pcb silk-align` prints: every round, then the
// readback verdict. unresolvedPairs comes from pcb.silk.list, never from the
// connector's own planning model.
type silkAlignReport struct {
	Rounds          []silkAlignRound `json:"rounds"`
	Converged       bool             `json:"converged"`
	UnresolvedPairs [][2]string      `json:"unresolvedPairs"`
	Unresolved      int              `json:"unresolved"` // last pass's boxed-in / pad-collision count
	Note            string           `json:"note,omitempty"`
}

// silkPairsOf projects overlap pairs to designator names.
func silkPairsOf(ps []silkOverlapPair) [][2]string {
	out := make([][2]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, [2]string{p.A.Text, p.B.Text})
	}
	return out
}

// silkRealignRefs picks which designators the next round moves: every member of
// a still-overlapping pair that is inside the caller's scope (nil scope = all).
// Everything else stays fixed and is a frozen obstacle in the connector.
func silkRealignRefs(pairs [][2]string, scope []string) []string {
	in := func(d string) bool {
		if len(scope) == 0 {
			return true
		}
		for _, s := range scope {
			if s == d {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	var out []string
	for _, p := range pairs {
		for _, d := range p {
			if in(d) && !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	sort.Strings(out)
	return out
}

// silkPairsTouch keeps only the pairs with at least one member in scope — a
// pre-existing overlap between two out-of-scope labels is not this run's to fix.
func silkPairsTouch(pairs [][2]string, scope []string) [][2]string {
	if len(scope) == 0 {
		return pairs
	}
	set := map[string]bool{}
	for _, s := range scope {
		set[s] = true
	}
	var out [][2]string
	for _, p := range pairs {
		if set[p[0]] || set[p[1]] {
			out = append(out, p)
		}
	}
	return out
}

// runSilkAlignConverge: align → read back real silk boxes → re-align only the
// still-overlapping designators (all others frozen) → … up to maxRounds passes
// in total. The verdict is the last readback.
func runSilkAlignConverge(act silkActionFunc, base map[string]any, scope []string, maxRounds int) (silkAlignReport, error) {
	if maxRounds < 1 {
		maxRounds = 1
	}
	var rep silkAlignReport
	refs := scope
	for round := 1; round <= maxRounds; round++ {
		payload := map[string]any{}
		for k, v := range base {
			payload[k] = v
		}
		if len(refs) > 0 {
			payload["refs"] = refs
		}
		res, err := act("pcb.silk.align", payload)
		if err != nil {
			return rep, fmt.Errorf("silk-align round %d: %w", round, err)
		}
		if n, ok := asFloatOK(res["unresolved"]); ok {
			rep.Unresolved = int(n)
		}
		silk, err := silkFromListResult(act)
		if err != nil {
			return rep, fmt.Errorf("silk-align round %d readback: %w", round, err)
		}
		pairs := silkPairsTouch(silkPairsOf(designatorSilkOverlaps(silk)), scope)
		r := silkAlignRound{Round: round, Result: res, Overlaps: pairs}
		if round > 1 || len(scope) > 0 {
			r.Refs = refs
		}
		rep.Rounds = append(rep.Rounds, r)
		rep.UnresolvedPairs = pairs
		if len(pairs) == 0 {
			rep.Converged = true
			return rep, nil
		}
		next := silkRealignRefs(pairs, scope)
		if len(next) == 0 {
			break
		}
		refs = next
	}
	rep.Note = fmt.Sprintf("%d designator pair(s) still overlap in the readback after %d round(s) — the area is too dense for the label size: loosen placement there, or place these with `pcb silk-set`", len(rep.UnresolvedPairs), len(rep.Rounds))
	return rep, nil
}

// silkFromListResult reads pcb.silk.list through the same parser `pcb check`
// uses (fetchPcbSilk), so both judge the same boxes.
func silkFromListResult(act silkActionFunc) ([]pcbSilkText, error) {
	res, err := act("pcb.silk.list", nil)
	if err != nil {
		return nil, err
	}
	return parsePcbSilkList(res), nil
}
