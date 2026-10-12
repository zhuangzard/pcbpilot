package app

import (
	"strings"
	"testing"
)

func TestFindPourLayerMismatch(t *testing.T) {
	pours := []pcbPourP{
		// US-Zed2 2026-10-12: inner-plane pours that landed on Top.
		{ID: "e561", Net: "GND", Name: "AUTO_GND_L15", Layer: 1},
		{ID: "e608", Net: "GND", Name: "AUTO_GND_L15_1", Layer: 1},
		{ID: "e2749", Net: "GND", Name: "AUTO_GND_L15_2", Layer: 1},
		// correct ones, incl. underscored / signed net names
		{ID: "ok1", Net: "5VLNA", Name: "AUTO_5VLNA_L16", Layer: 16},
		{ID: "ok2", Net: "+12V", Name: "AUTO_+12V_L16", Layer: 16},
		{ID: "ok3", Net: "GND", Name: "AUTO_GND_L2", Layer: 2},
		{ID: "ok4", Net: "HV_NEG", Name: "AUTO_HV_NEG_L16_1", Layer: 16},
		// hand-named pours are not judged
		{ID: "x", Net: "GND", Name: "my pour", Layer: 1},
		{ID: "y", Net: "GND", Name: "", Layer: 1},
	}
	got := findPourLayerMismatch(pours)
	if len(got) != 3 {
		t.Fatalf("got %d findings, want 3: %+v", len(got), got)
	}
	for i, f := range got {
		if f.Type != "pour-layer-mismatch" || f.Level != "ERROR" || f.Layer != 1 || f.Net != "GND" || len(f.Primitives) != 1 {
			t.Errorf("finding %d: %+v", i, f)
		}
		if !strings.Contains(f.Message, "layer 15") {
			t.Errorf("finding %d message lacks the intended layer: %s", i, f.Message)
		}
	}
	// wrong layer in the other direction (named L2, sits on 16) is also an error
	if g := findPourLayerMismatch([]pcbPourP{{Net: "GND", Name: "AUTO_GND_L2", Layer: 16}}); len(g) != 1 {
		t.Errorf("L2 pour on 16 not flagged: %+v", g)
	}
}

func TestPourLayerMismatchFromSnapshotAndGate(t *testing.T) {
	snap := &boardSnapshot{Copper: &boardCopperSnapshot{Pours: []any{
		map[string]any{"primitiveId": "e561", "net": "GND", "layer": float64(1), "pourName": "AUTO_GND_L15"},
		map[string]any{"primitiveId": "e2781", "net": "GND", "layer": float64(2), "pourName": "AUTO_GND_L2"},
	}}}
	fs := pourLayerMismatchFromSnapshot(snap)
	if len(fs) != 1 || fs[0].Primitives[0] != "e561" {
		t.Fatalf("snapshot findings: %+v", fs)
	}
	g := intentCheckGate("pour-layer-mismatch", "x", &pcbCheckReport{Findings: fs}, nil, nil)
	if g.Pass || len(g.Items) != 1 {
		t.Fatalf("gate must fail on a mismatched pour: %+v", g)
	}
	if g := intentCheckGate("pour-layer-mismatch", "x", &pcbCheckReport{}, nil, nil); !g.Pass {
		t.Fatalf("gate must pass without findings: %+v", g)
	}
	if pourLayerMismatchFromSnapshot(&boardSnapshot{}) != nil {
		t.Fatal("snapshot without copper must yield nothing")
	}
}

func TestPourLayerMismatchIsMandatoryRouteGate(t *testing.T) {
	for _, m := range mandatoryRouteGates {
		if m.category == "pour-layer-mismatch" {
			return
		}
	}
	t.Fatal("pour-layer-mismatch missing from mandatoryRouteGates")
}
