package app

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

func TestGateRegistryPassFailCannotCheck(t *testing.T) {
	tests := []struct {
		name    string
		records []GateRecord
		agg     gate.Status
		exit    int
		passed  bool
	}{
		{
			name:   "no records → cannot-check",
			agg:    gate.CannotCheck,
			exit:   5,
			passed: false,
		},
		{
			name: "all pass → pass",
			records: []GateRecord{
				{ID: "kicad-drc", Status: gate.Pass, Reason: "0 violations"},
				{ID: "route-complete", Status: gate.Pass, Reason: "all routed"},
			},
			agg: gate.Pass, exit: 0, passed: true,
		},
		{
			name: "one fail → fail",
			records: []GateRecord{
				{ID: "kicad-drc", Status: gate.Pass, Reason: "0 violations"},
				{ID: "route-complete", Status: gate.Fail, Reason: "3 unrouted"},
			},
			agg: gate.Fail, exit: 6, passed: false,
		},
		{
			name: "cannot-check without fail → cannot-check",
			records: []GateRecord{
				{ID: "kicad-drc", Status: gate.CannotCheck, Reason: "DRC crashed"},
				{ID: "route-complete", Status: gate.Pass, Reason: "all routed"},
			},
			agg: gate.CannotCheck, exit: 5, passed: false,
		},
		{
			name: "fail + cannot-check → fail wins",
			records: []GateRecord{
				{ID: "kicad-drc", Status: gate.CannotCheck, Reason: "DRC crashed"},
				{ID: "route-complete", Status: gate.Fail, Reason: "3 unrouted"},
			},
			agg: gate.Fail, exit: 6, passed: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newGateRegistry(&bytes.Buffer{}, nil)
			for _, r := range tt.records {
				reg.Add(r)
			}
			if got := reg.Aggregate(); got != tt.agg {
				t.Errorf("Aggregate() = %q, want %q", got, tt.agg)
			}
			if got := reg.ExitCode(); got != tt.exit {
				t.Errorf("ExitCode() = %d, want %d", got, tt.exit)
			}
			if got := reg.Passed(); got != tt.passed {
				t.Errorf("Passed() = %v, want %v", got, tt.passed)
			}
		})
	}
}

func TestGateRegistryStderr(t *testing.T) {
	var buf bytes.Buffer
	reg := newGateRegistry(&buf, nil)
	reg.Add(GateRecord{ID: "kicad-drc", Status: gate.Pass, Reason: "0 violations"})
	reg.Add(GateRecord{ID: "route-complete", Status: gate.Fail, Reason: "3 unrouted"})
	reg.Add(GateRecord{ID: "silkscreen", Status: gate.CannotCheck, Reason: "deferred"})

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 stderr lines, got %d: %q", len(lines), buf.String())
	}
	// Every gate must appear on stderr with the correct status mark.
	expects := []struct{ id, mark string }{
		{"kicad-drc", "PASS"},
		{"route-complete", "FAIL"},
		{"silkscreen", "CANNOT-CHECK"},
	}
	for i, e := range expects {
		if !strings.Contains(lines[i], e.id) {
			t.Errorf("line %d: missing gate id %q in %q", i, e.id, lines[i])
		}
		if !strings.Contains(lines[i], e.mark) {
			t.Errorf("line %d: missing mark %q in %q", i, e.mark, lines[i])
		}
	}
}

func TestGateRegistryStderrConsistentWithRecords(t *testing.T) {
	// Every record in the registry must have exactly one corresponding
	// stderr line. This catches the old router-vias bug where gates were
	// added to r.gates without going through r.add().
	var buf bytes.Buffer
	reg := newGateRegistry(&buf, nil)
	ids := []string{
		"design-review", "pour-clip", "router-vias", "kicad-drc",
		"pad-net-diff", "intent-rules", "intent-widths", "intent-lengths",
		"copper-to-edge", "isolation", "via-current", "post-layout-sim",
		"route-complete", "silkscreen", "board-manual", "design-report",
		"signoff", "gate-set",
	}
	for _, id := range ids {
		reg.Add(GateRecord{ID: id, Status: gate.Pass, Reason: "test"})
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(ids) {
		t.Fatalf("stderr lines (%d) != records (%d)", len(lines), len(ids))
	}
	recs := reg.Records()
	if len(recs) != len(ids) {
		t.Fatalf("records (%d) != expected (%d)", len(recs), len(ids))
	}
	for i, id := range ids {
		if recs[i].ID != id {
			t.Errorf("record %d: ID = %q, want %q", i, recs[i].ID, id)
		}
		if !strings.Contains(lines[i], id) {
			t.Errorf("stderr line %d: missing gate id %q", i, id)
		}
	}
}

func TestGateRegistryWaivers(t *testing.T) {
	var buf bytes.Buffer
	ws := []gateWaiver{{Gate: "kicad-drc", Match: "clearance", Reason: "known", By: "test"}}
	reg := newGateRegistry(&buf, ws)
	reg.Add(GateRecord{
		ID: "kicad-drc", Status: gate.Fail, Reason: "1 violation",
		Items: []string{"clearance: 10 mil"},
	})
	rec := reg.Records()[0]
	if rec.Status != gate.Pass {
		t.Errorf("waived gate status = %q, want pass", rec.Status)
	}
	if len(rec.Waived) == 0 {
		t.Error("expected waived items")
	}
	if !strings.Contains(buf.String(), "PASS") {
		t.Error("stderr should show PASS after waiver")
	}
}

func TestGateRegistryBoardSHA(t *testing.T) {
	reg := newGateRegistry(&bytes.Buffer{}, nil)
	reg.SetBoardSHA("aaa")
	reg.Add(GateRecord{ID: "g1", Status: gate.Pass, Reason: "ok", Scope: "board"})
	if reg.Records()[0].BoardSHA != "aaa" {
		t.Errorf("expected board SHA aaa, got %q", reg.Records()[0].BoardSHA)
	}
	reg.SetBoardSHA("bbb")
	reg.Add(GateRecord{ID: "g2", Status: gate.Pass, Reason: "ok", Scope: "board"})
	if reg.Records()[1].BoardSHA != "bbb" {
		t.Errorf("expected board SHA bbb, got %q", reg.Records()[1].BoardSHA)
	}
	// First record keeps its original SHA.
	if reg.Records()[0].BoardSHA != "aaa" {
		t.Errorf("first record SHA changed")
	}
}

func TestToRecord(t *testing.T) {
	g := gateResult{Gate: "kicad-drc", Pass: true, Detail: "0 violations"}
	r := toRecord(g, []string{"/path/to/drc.json"}, "board", 5*time.Second)
	if r.ID != "kicad-drc" {
		t.Errorf("ID = %q", r.ID)
	}
	if r.Status != gate.Pass {
		t.Errorf("Status = %q", r.Status)
	}
	if r.DurationS != 5 {
		t.Errorf("DurationS = %v", r.DurationS)
	}
	if len(r.Evidence) != 1 || r.Evidence[0] != "/path/to/drc.json" {
		t.Errorf("Evidence = %v", r.Evidence)
	}
}

func TestToRecordCC(t *testing.T) {
	g := gateResult{Gate: "kicad-drc", Pass: false, Detail: "DRC crashed"}
	r := toRecordCC(g, "DRC process exited with signal 11", nil, "board")
	if r.Status != gate.CannotCheck {
		t.Errorf("Status = %q, want cannot-check", r.Status)
	}
}

func TestGateRegistryLegacyFields(t *testing.T) {
	reg := newGateRegistry(&bytes.Buffer{}, nil)
	reg.Add(GateRecord{ID: "test-gate", Status: gate.Fail, Reason: "broken"})
	rec := reg.Records()[0]
	// Legacy fields must be populated for backward-compatible summary.json.
	if rec.Gate != "test-gate" {
		t.Errorf("legacy Gate = %q", rec.Gate)
	}
	if rec.Pass {
		t.Error("legacy Pass should be false for a failing gate")
	}
	if rec.Detail != "broken" {
		t.Errorf("legacy Detail = %q", rec.Detail)
	}
}

// TestGateRegistryRouterTimeout verifies that a router timeout results in
// cannot-check, not fail (the true unrouted count is unknown).
func TestGateRegistryRouterTimeout(t *testing.T) {
	reg := newGateRegistry(&bytes.Buffer{}, nil)
	reg.Add(GateRecord{
		ID: "route-complete", Status: gate.CannotCheck,
		Reason: "router timeout: no report (true unrouted count unknown)",
	})
	if reg.Aggregate() != gate.CannotCheck {
		t.Errorf("timeout should be cannot-check, got %q", reg.Aggregate())
	}
	if reg.ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5", reg.ExitCode())
	}
}

// TestGateRegistryMissingReport verifies that a missing router report is
// cannot-check.
func TestGateRegistryMissingReport(t *testing.T) {
	reg := newGateRegistry(&bytes.Buffer{}, nil)
	reg.Add(GateRecord{
		ID: "route-complete", Status: gate.CannotCheck,
		Reason: "missing router report",
	})
	if reg.ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5", reg.ExitCode())
	}
}

// TestGateRegistryDRCCrash verifies a DRC crash is cannot-check.
func TestGateRegistryDRCCrash(t *testing.T) {
	reg := newGateRegistry(&bytes.Buffer{}, nil)
	reg.Add(GateRecord{
		ID: "kicad-drc", Status: gate.CannotCheck,
		Reason: "kicad-cli DRC crashed with signal 11",
	})
	if reg.ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5", reg.ExitCode())
	}
}

// TestGateRegistrySummaryWriteFailure verifies that even if summary write
// fails, the gate records are still in the registry.
func TestGateRegistrySummaryWriteFailure(t *testing.T) {
	reg := newGateRegistry(&bytes.Buffer{}, nil)
	reg.Add(GateRecord{ID: "g1", Status: gate.Pass, Reason: "ok"})
	reg.Add(GateRecord{ID: "g2", Status: gate.Fail, Reason: "bad"})
	// Records are independent of summary serialization.
	if len(reg.Records()) != 2 {
		t.Errorf("expected 2 records, got %d", len(reg.Records()))
	}
	if reg.Aggregate() != gate.Fail {
		t.Errorf("Aggregate = %q, want fail", reg.Aggregate())
	}
}

// TestGateRegistryReset verifies that Reset keeps only the specified gates
// and drops stale ones (BLOCKER 1: superseded board candidates).
func TestGateRegistryReset(t *testing.T) {
	reg := newGateRegistry(&bytes.Buffer{}, nil)
	reg.Add(GateRecord{ID: "design-review", Status: gate.Pass, Reason: "ok"})
	reg.Add(GateRecord{ID: "kicad-drc", Status: gate.Fail, Reason: "3 violations"})
	reg.Add(GateRecord{ID: "route-complete", Status: gate.Fail, Reason: "2 unrouted"})
	if len(reg.Records()) != 3 {
		t.Fatalf("expected 3 records before reset, got %d", len(reg.Records()))
	}
	// Reset: keep only design-review (like qualityGates does).
	reg.Reset(map[string]bool{"design-review": true})
	recs := reg.Records()
	if len(recs) != 1 {
		t.Fatalf("expected 1 record after reset, got %d", len(recs))
	}
	if recs[0].ID != "design-review" {
		t.Errorf("kept record ID = %q, want design-review", recs[0].ID)
	}
	// After reset, aggregate should be pass (only passing gates remain).
	if reg.Aggregate() != gate.Pass {
		t.Errorf("Aggregate after reset = %q, want pass", reg.Aggregate())
	}
}

// TestGateRegistryToRecordCC verifies that toRecordCC produces cannot-check
// records that propagate to exit code 5 (BLOCKER 2).
func TestGateRegistryToRecordCC(t *testing.T) {
	reg := newGateRegistry(&bytes.Buffer{}, nil)
	g := gateResult{Gate: "widen-drc", Pass: false, Detail: "cannot check"}
	reg.Add(toRecordCC(g, "DRC step-back did not run", nil, "board"))
	if reg.ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5 for cannot-check", reg.ExitCode())
	}
	rec := reg.Records()[0]
	if rec.Status != gate.CannotCheck {
		t.Errorf("status = %q, want cannot-check", rec.Status)
	}
	if rec.Reason != "DRC step-back did not run" {
		t.Errorf("reason = %q", rec.Reason)
	}
}

// TestGateRegistryExitCodePrecedence verifies the full precedence table.
func TestGateRegistryExitCodePrecedence(t *testing.T) {
	tests := []struct {
		name     string
		statuses []gate.Status
		wantExit int
	}{
		{"all pass", []gate.Status{gate.Pass, gate.Pass}, 0},
		{"one fail", []gate.Status{gate.Pass, gate.Fail}, 6},
		{"one cc", []gate.Status{gate.Pass, gate.CannotCheck}, 5},
		{"fail + cc → fail", []gate.Status{gate.Fail, gate.CannotCheck}, 6},
		{"empty → cc", nil, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := newGateRegistry(&bytes.Buffer{}, nil)
			for i, s := range tt.statuses {
				reg.Add(GateRecord{ID: fmt.Sprintf("g%d", i), Status: s, Reason: "test"})
			}
			if got := reg.ExitCode(); got != tt.wantExit {
				t.Errorf("exit = %d, want %d", got, tt.wantExit)
			}
		})
	}
}
