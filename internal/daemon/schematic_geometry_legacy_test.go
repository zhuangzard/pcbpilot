package daemon

import (
	"context"
	"testing"
)

func TestLocalWirePreservesLegacyDiagnostics(t *testing.T) {
	for _, scenario := range []string{"zero-length", "old-pin-exit", "new-defect", "worsened-old-defect", "missing-data", "new-invalid-path", "missing-wire"} {
		t.Run(scenario, func(t *testing.T) {
			before, after := geometryFixture(t), geometryFixture(t)
			old := map[string]any{"primitiveId": "old", "x0": 100., "y0": 100., "x1": 100., "y1": 100.}
			if scenario == "old-pin-exit" {
				old = map[string]any{"primitiveId": "old", "x0": 590., "y0": 1020., "x1": 590., "y1": 1050.}
			}
			before["wires"] = []any{old}
			landed := map[string]any{"primitiveId": "added", "x0": 590., "y0": 1020., "x1": 620., "y1": 1020.}
			after["wires"] = []any{old, landed}
			if scenario == "new-defect" {
				after["wires"] = append(after["wires"].([]any), map[string]any{"primitiveId": "new-zero", "x0": 200., "y0": 200., "x1": 200., "y1": 200.})
			}
			if scenario == "worsened-old-defect" {
				after["wires"] = []any{map[string]any{"primitiveId": "old", "x0": 200., "y0": 100., "x1": 200., "y1": 100.}, landed}
			}
			if scenario == "missing-data" {
				delete(before["components"].([]any)[0].(map[string]any), "bbox")
			}
			if scenario == "missing-wire" {
				after["wires"] = []any{old}
			}
			path := `[[590,1020],[620,1020]]`
			if scenario == "new-invalid-path" {
				path = `[[590,1020],[535,1020]]`
			}
			writes := 0
			res, err := New(Options{}).forwardSchematicGeometry(context.Background(), geometryRequest(path), geometryDispatcher(before, after, &writes, false))
			if err != nil {
				t.Fatal(err)
			}
			pass := scenario == "zero-length" || scenario == "old-pin-exit"
			if res.OK != pass {
				t.Fatalf("result=%+v", res)
			}
			if pass {
				guard := res.Result["geometryGuard"].(map[string]any)
				if guard["preexistingFindings"].(int) == 0 || guard["baselineFindings"] == nil || len(res.Warnings) == 0 {
					t.Fatalf("lost historical diagnostics: %+v", res)
				}
			}
			wantWrites := 1
			if scenario == "missing-data" || scenario == "new-invalid-path" {
				wantWrites = 0
			}
			if writes != wantWrites {
				t.Fatalf("writes=%d, want=%d", writes, wantWrites)
			}
			if !pass && writes == 1 && res.Result["partial"] != true {
				t.Fatal("lost partial-write evidence")
			}
		})
	}
}
