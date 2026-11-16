package app

import (
	"strings"
	"testing"
)

// An optional simulator that could not be installed is not an update error,
// and its reason is printed exactly once.
func TestUpdateReportsSkippedToolOnce(t *testing.T) {
	const why = "unpacking failed: disk full — install it by hand: example step"
	var res applyResult
	res.add(stepRow{Component: compTools + ":ngspice", Status: "current", From: "ok", To: "ok"})
	res.add(stepRow{Component: compTools + ":elmer", Status: "skipped", From: "missing", To: "missing", Detail: why})
	if res.Failed {
		t.Fatal("a skipped optional tool must not fail the update")
	}
	rep := updateReport{Target: "9.9.9"}
	fillApplyReport(&rep, res)
	var out strings.Builder
	printApplyReport(&out, rep)
	if n := strings.Count(out.String(), why); n != 1 {
		t.Fatalf("reason printed %d times, want once:\n%s", n, out.String())
	}
	notes := 0
	for _, n := range rep.Notes {
		if strings.Contains(n, "tools:elmer") {
			notes++
		}
	}
	if notes != 1 {
		t.Fatalf("want one tools:elmer note, got %d: %v", notes, rep.Notes)
	}
	// the machine-readable step still carries the full reason
	if s := rep.Steps[len(rep.Steps)-1]; s.Detail != why {
		t.Fatalf("%+v", s)
	}
}
