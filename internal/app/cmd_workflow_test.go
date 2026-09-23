package app

import "testing"

// TestWorkflowAdvanceBlocked pins the exit-code contract of `workflow advance`
// (issue #113): a blocked flow MUST exit non-zero, or a `set -e` script / CI
// loop walks on with a sick board. Before the fix the post-route check gate
// rejected 7 blocking findings and the process still exited 0.
func TestWorkflowAdvanceBlocked(t *testing.T) {
	// Every `next` workflowNext can emit, so a wording change here can't silently
	// turn a human-sign-off stop back into an exit 0.
	const (
		nextSetAssembly    = "pcbpilot pcb stage set-assembly --profile hand-solder|reflow"
		nextImport         = "pcbpilot pcb import-changes"
		nextLayoutLint     = "pcbpilot pcb layout-lint --gate"
		nextConfirmLayout  = "pcbpilot pcb stage confirm-layout --note \"...\""
		nextOutlineFit     = "pcbpilot pcb outline-fit"
		nextConfirmOutline = "pcbpilot pcb stage confirm-outline --note \"...\""
		nextRoute          = "pcbpilot pcb route-short   (or autoroute)"
		nextAdvance        = "pcbpilot workflow advance   (runs the pcb-check gate)"
		nextDelivery       = "pcbpilot pcb silk-align && pcb drc && pcb save   (P9/P10 delivery)"
	)

	cases := []struct {
		name        string
		gateBlocked bool
		next        string
		want        bool
	}{
		// The regression itself: the gate rejected, so advance must NOT exit 0 —
		// whatever `next` says. `next` here is the real post-gate-failure value.
		{"gate rejected → blocked", true, nextAdvance, true},
		{"gate could not run → blocked", true, nextDelivery, true},
		// Human sign-offs: the pre-existing contract must survive the fix.
		{"awaiting confirm-layout → blocked", false, nextConfirmLayout, true},
		{"awaiting confirm-outline → blocked", false, nextConfirmOutline, true},
		{"assembly profile unset → blocked", false, nextSetAssembly, true},
		// Mechanical next steps an automated loop is free to run itself.
		{"gate passed, delivery next → free", false, nextDelivery, false},
		{"import next → free", false, nextImport, false},
		{"layout-lint next → free", false, nextLayoutLint, false},
		{"outline-fit next → free", false, nextOutlineFit, false},
		{"routing authorized → free", false, nextRoute, false},
		{"advance again (gate not yet run) → free", false, nextAdvance, false},
		// Both conditions at once must not cancel out.
		{"gate blocked AND awaiting sign-off → blocked", true, nextConfirmLayout, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := workflowAdvanceBlocked(tc.gateBlocked, tc.next); got != tc.want {
				t.Errorf("workflowAdvanceBlocked(%v, %q) = %v, want %v", tc.gateBlocked, tc.next, got, tc.want)
			}
		})
	}
}

// TestWorkflowNextBlockedAlignment guards the coupling the exit code depends on:
// workflowAdvanceBlocked matches on `next` SUBSTRINGS, so if workflowNext ever
// reworded a human-sign-off step the match would silently fall through to exit 0.
// This pins the two together through the real workflowNext.
func TestWorkflowNextBlockedAlignment(t *testing.T) {
	// Assembly profile unset — the first gate in workflowNext.
	st := &pcbStageState{}
	next, _ := workflowNext(st, workflowFacts{Reachable: true, Components: 3})
	if !workflowAdvanceBlocked(false, next) {
		t.Errorf("workflowNext returned %q for an unset assembly profile, which workflowAdvanceBlocked does not treat as blocked", next)
	}

	// Placement awaiting the human sign-off (layout lint clean, not confirmed).
	st = &pcbStageState{Assembly: &pcbAssemblyProfile{}, Layout: &pcbLayoutGateSummary{}}
	next, _ = workflowNext(st, workflowFacts{Reachable: true, Components: 3})
	if !workflowAdvanceBlocked(false, next) {
		t.Errorf("workflowNext returned %q for an unconfirmed placement, which workflowAdvanceBlocked does not treat as blocked", next)
	}
}
