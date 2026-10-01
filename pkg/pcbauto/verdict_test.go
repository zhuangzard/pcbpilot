package pcbauto

import (
	"strings"
	"testing"
)

// Any isolation finding, infeasible bridge or board-edge ERROR gates the
// joint score (not deliverable, capped like a short) and lands in the
// pipeline's blockers with its reason; via-array shortfalls and IR drop over
// budget are electrical blockers. NotDeliverable lists them, safety first,
// with incompletion and DRC after them.
func TestSafetyFindingsBlockDelivery(t *testing.T) {
	iso := &IsolationReport{Findings: []IsoFinding{{Kind: "iso-creepage", Severity: "error", Message: "T1.2 (AC_N) ↔ via#6 (GND): surface path 155.9 mil < 181.1 mil"}}}
	pol := &EdgePolicy{ByDomain: map[string]*EdgeDomain{"MAINS": {Mil: 181.2}}, netDomain: map[string]string{"AC_L": "MAINS"}}
	edge := &EdgeCheck{Policy: pol, Findings: []EdgeFinding{
		{Rule: "copper-to-edge", Level: "ERROR", Net: "AC_L", GapMil: 10, RequiredMil: 181.2, Message: "layer 1 track of net AC_L is 10.0 mil from the board edge"},
		// An edge connector's SELV shell pad on the outline: the fab rule,
		// a blocker, not a safety gate (hdmi-tx stress J1.S1).
		{Rule: "copper-to-edge", Level: "ERROR", Net: "GND", GapMil: 0, RequiredMil: 20, Message: "layer 1 pad J1.S1 of net GND is 0.0 mil from the board edge"},
	}}
	rr := &RouteResult{Stats: RouteStats{Completion: 100}, ViaShortfalls: []ViaShortfall{{Net: "VOUT", Reason: "1 via short"}}}
	sg, eb := deliveryBlockers(iso, edge, rr)
	if len(sg) != 2 || !strings.Contains(sg[0], "155.9") || !strings.Contains(sg[1], "board edge") {
		t.Fatalf("safety %v", sg)
	}
	if len(eb) != 2 || !strings.Contains(eb[0], "J1.S1") || !strings.Contains(eb[1], "VOUT") {
		t.Fatalf("electrical %v", eb)
	}
	b := mainsSelvBoard()
	an := Analyze(b, PowerSpec{}, nil)
	js := Joint(b, an, Understand(b, an), nil, rr, &DRCReport{}, JointOptions{PlacementScore: -1, Isolation: iso, Edge: edge})
	if js.Deliverable || len(js.Gates) != 2 || js.Overall > 40 || len(js.Blockers) != 2 {
		t.Fatalf("joint deliverable %v gates %v blockers %v overall %.1f", js.Deliverable, js.Gates, js.Blockers, js.Overall)
	}
	why := js.NotDeliverable()
	if len(why) < 4 || !strings.Contains(why[0], "isolation") || !strings.Contains(why[3], "via current") {
		t.Fatalf("reasons %v", why)
	}
	clean := Joint(b, an, Understand(b, an), nil, &RouteResult{Stats: RouteStats{Completion: 100}}, &DRCReport{}, JointOptions{PlacementScore: -1, Isolation: &IsolationReport{}, Edge: &EdgeCheck{}})
	if len(clean.Gates) != 0 || len(clean.Blockers) != 0 {
		t.Fatalf("clean board gated: %v %v", clean.Gates, clean.Blockers)
	}
	part := Joint(b, an, Understand(b, an), nil, &RouteResult{Stats: RouteStats{Completion: 90}}, &DRCReport{}, JointOptions{PlacementScore: -1})
	if part.Deliverable || len(part.NotDeliverable()) == 0 || !strings.Contains(part.NotDeliverable()[0], "routing incomplete") {
		t.Fatalf("incomplete board: %v %v", part.Deliverable, part.NotDeliverable())
	}
}
