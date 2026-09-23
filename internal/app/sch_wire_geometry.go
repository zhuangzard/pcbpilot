package app

import (
	"encoding/json"
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
	return out
}
