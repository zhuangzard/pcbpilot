package app

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// A post-layout-sim open on a net KiCad's DRC shows connected is marked as
// a sim/KiCad mismatch; the gate still fails, and the IR closure still runs
// on the real drop next to it.
func TestSimKicadMismatchKeepsIRClosure(t *testing.T) {
	gates := []gateResult{
		{Gate: "kicad-drc", Pass: true},
		{Gate: "post-layout-sim", Items: []string{
			"+5V: no copper path from U1.1 to C3.1",
			"+5V drops 41.00 mV at U2.3 (typical) — over the 30.0 mV budget",
			"SV1: no copper path from Q1.3 to J2.1",
		}},
	}
	drc := &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "unconnected_items", Section: "unconnected_items", Net: "SV1"}}}
	mm := markSimKicadMismatch(gates, drc)
	if len(mm) != 1 || !strings.Contains(mm[0], "+5V") || !strings.Contains(mm[0], simOpenHostConnected) {
		t.Fatalf("mismatch %v, want the +5V open only (SV1 is unconnected in KiCad too)", mm)
	}
	if gates[1].Pass {
		t.Fatal("the gate must keep failing: the drop of the mismatched pads is not proven")
	}
	// SV1 is a real open (KiCad agrees): no closure.
	if r := irOverBudget(map[string]any{"gates": gates}); r != nil {
		t.Fatalf("real open must block the IR closure, got %v", r)
	}
	gates[1].Items = gates[1].Items[:2]
	r := irOverBudget(map[string]any{"gates": gates})
	if r == nil || r["+5V"] < 1.3 {
		t.Fatalf("mismatch open must not block the IR closure, got %v", r)
	}
	// Without a KiCad report nothing is marked.
	if markSimKicadMismatch(gates, nil) != nil {
		t.Fatal("no DRC report: no mismatch claim")
	}
}

// The sign-off as a run gate (kicad route and pcb auto route): an error or
// a failing sign-off gate fails it; missing inputs are named.
func TestSignoffGateFromRunDir(t *testing.T) {
	dir := t.TempDir()
	so := signoffOpts{intent: filepath.Join(dir, "missing-intent.json")}
	so.fillFromRunDir(dir)
	if so.outDir != filepath.Join(dir, "signoff") {
		t.Fatalf("outDir %q", so.outDir)
	}
	g := signoffGate(so, nil, io.Discard)
	if g.Pass || g.Gate != "signoff" || !strings.Contains(g.Detail, "intent") {
		t.Fatalf("gate %+v", g)
	}
}

// Only the pads of a part whose box reaches the outline (the copper-to-edge
// gate's edge-mounted parts) near the edge are exempt from the DSN bands.
func TestEdgeExemptBoxes(t *testing.T) {
	snap := &boardSnapshot{Outline: &boardOutline{BBox: layoutBBox{0, 0, 2000, 1000}, Points: [][2]float64{{0, 0}, {2000, 0}, {2000, 1000}, {0, 1000}}}}
	snap.Components = []boardComp{
		{Designator: "J1", BBox: &layoutBBox{900, -40, 1100, 120}, Pads: []boardPad{
			{Number: "1", X: 950, Y: 10, W: 20, H: 40}, {Number: "2", X: 1050, Y: 10, W: 20, H: 40}, {Number: "S", X: 1000, Y: 100, W: 30, H: 30}}},
		{Designator: "R1", BBox: &layoutBBox{100, 5, 220, 70}, Pads: []boardPad{{Number: "1", X: 130, Y: 30, W: 30, H: 30}}},
	}
	boxes, names := edgeExemptBoxes(snap, 30, 8)
	if strings.Join(names, ",") != "J1.1,J1.2" || len(boxes) != 2 || boxes[0] != [4]float64{932, -18, 968, 38} {
		t.Fatalf("names %v boxes %v", names, boxes)
	}
}
