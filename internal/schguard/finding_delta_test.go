package schguard

import "testing"

func TestFindingDeltaIncludesGeometryAndMultiplicity(t *testing.T) {
	old := Finding{Type: "wire-through-body", WirePrimitiveId: "wire", PrimitiveId: "U1", Segment: []Point{{0, 0}, {10, 0}}}
	changed := old
	changed.Segment = []Point{{0, 0}, {20, 0}}
	if got := NewGeometryFindings([]Finding{old}, []Finding{old, changed, old}); len(got) != 2 {
		t.Fatalf("new segment or duplicate masked: %+v", got)
	}
}

func TestCompleteTopologyStillRejectsDegenerateWires(t *testing.T) {
	s := map[string]any{"wires": []any{map[string]any{"x0": 0., "y0": 0., "x1": 0., "y1": 0.}}}
	if CompareWireTopology(s, s) == nil {
		t.Fatal("complete drawing validation silently ignored zero length")
	}
}
