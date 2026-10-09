package app

import (
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
