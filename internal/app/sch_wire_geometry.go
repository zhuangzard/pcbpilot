package app

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/schguard"
)

// schWireGeometryFindings is the app adapter for the SAME pure guard used by
// daemon mutation pre/post checks. Stable wire IDs are retained for actionable
// findings; the original observation map is never overwritten.
func schWireGeometryFindings(result map[string]any, wires []schGroupWire) []checkFinding {
	snapshot := make(map[string]any, len(result)+1)
	for k, v := range result {
		snapshot[k] = v
	}
	rows := make([]any, 0, len(wires))
	for _, w := range wires {
		// getState_Line is a segment array for an even vertex count >=4,
		// NOT a polyline. Match the official collector: do not invent connecting
		// edges between disjoint segments of a merged tree.
		stride := 2
		vertices := len(w.Points) / 2
		if vertices >= 4 && vertices%2 == 0 {
			stride = 4
		}
		if len(w.Points) < 4 || len(w.Points)%2 != 0 {
			rows = append(rows, map[string]any{"primitiveId": w.ID, "points": w.Points})
			continue
		}
		for i := 0; i+3 < len(w.Points); i += stride {
			rows = append(rows, map[string]any{"primitiveId": w.ID, "x0": w.Points[i], "y0": w.Points[i+1], "x1": w.Points[i+2], "y1": w.Points[i+3]})
		}
	}
	snapshot["wires"] = rows
	findings := schguard.AnalyzeWireGeometry(snapshot)
	b, _ := json.Marshal(findings)
	var out []checkFinding
	_ = json.Unmarshal(b, &out)
	for i := range out {
		out[i].Level = strings.ToLower(out[i].Level)
	}
	return out
}

func zeroLengthIdentity(f checkFinding) string {
	if f.Type != "zero-length-wire" {
		return ""
	}
	if f.WirePrimitiveId != "" {
		return "id:" + f.WirePrimitiveId
	}
	if f.At != nil {
		return fmt.Sprintf("at:%.4f,%.4f", f.At.X, f.At.Y)
	}
	return ""
}

// checkLevelRank orders finding levels for duplicate merging. Unknown levels
// rank with warn, matching checkLevelBlocks (they block only under strict).
func checkLevelRank(level string) int {
	switch strings.ToLower(level) {
	case "fatal":
		return 3
	case "error":
		return 2
	case "info":
		return 0
	default:
		return 1
	}
}

// mergeDuplicateZeroLengthFindings folds the geometry guard's findings (extra)
// into the connector's (existing) without reporting one zero-length wire
// twice. A duplicate keeps the HIGHEST severity together with that finding's
// own evidence: a guard ERROR for a primitive the connector only warned about
// replaces the WARN in place, so the blocking finding is never lost. It
// returns the possibly upgraded existing findings and the extra findings that
// are still new.
func mergeDuplicateZeroLengthFindings(existing, extra []checkFinding) (merged, added []checkFinding) {
	merged = append([]checkFinding(nil), existing...)
	type slot struct {
		inAdded bool
		index   int
	}
	seen := map[string]slot{}
	for i, f := range merged {
		k := zeroLengthIdentity(f)
		if k == "" {
			continue
		}
		if prev, ok := seen[k]; ok {
			// existing already holds a duplicate; point at its most severe copy.
			if checkLevelRank(f.Level) <= checkLevelRank(merged[prev.index].Level) {
				continue
			}
		}
		seen[k] = slot{index: i}
	}
	added = make([]checkFinding, 0, len(extra))
	for _, f := range extra {
		k := zeroLengthIdentity(f)
		if k == "" {
			added = append(added, f)
			continue
		}
		prev, ok := seen[k]
		if !ok {
			seen[k] = slot{inAdded: true, index: len(added)}
			added = append(added, f)
			continue
		}
		held := &merged[prev.index]
		if prev.inAdded {
			held = &added[prev.index]
		}
		if checkLevelRank(f.Level) > checkLevelRank(held.Level) {
			*held = f
		}
	}
	return merged, added
}
