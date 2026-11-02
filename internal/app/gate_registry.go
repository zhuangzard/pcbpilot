package app

// gate_registry.go — one ordered, typed gate registry that drives execution,
// stderr and summary.json. Every gate in a kicad route run goes through
// reg.Add(); the registry is the single source of truth for gate status,
// evidence, timing and the process exit code.

import (
	"fmt"
	"io"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

// GateRecord is one gate as the registry sees it.
type GateRecord struct {
	ID        string      `json:"id"`                 // stable, e.g. "kicad-drc"
	Status    gate.Status `json:"status"`             // pass | fail | cannot-check
	Reason    string      `json:"reason"`             // human-readable detail
	Items     []string    `json:"items,omitempty"`    // per-item failures
	Waived    []string    `json:"waived,omitempty"`   // waived items
	Info      []string    `json:"info,omitempty"`     // passing notes
	Evidence  []string    `json:"evidence,omitempty"` // file paths
	BoardSHA  string      `json:"boardSha,omitempty"` // sha256 of the board this gate checked
	Scope     string      `json:"scope,omitempty"`    // "board" | "session" | "report" | ""
	DurationS float64     `json:"durationS,omitempty"`
	// Legacy fields for backward-compatible summary.json consumers.
	Gate   string `json:"gate"`   // == ID
	Pass   bool   `json:"pass"`   // == Status.OK()
	Detail string `json:"detail"` // == Reason
}

// gateRegistry is the ordered list of gate records for one kicad route run.
// All gate output (stderr, summary.json, exit code) derives from it.
type gateRegistry struct {
	records  []GateRecord
	stderr   io.Writer
	waivers  []gateWaiver
	boardSHA string // current board hash (updated after geometry stages)
}

func newGateRegistry(stderr io.Writer, waivers []gateWaiver) *gateRegistry {
	return &gateRegistry{stderr: stderr, waivers: waivers}
}

// Add applies waivers, prints the gate to stderr, and appends the record.
func (reg *gateRegistry) Add(g GateRecord) {
	// Fill legacy fields.
	g.Gate, g.Pass, g.Detail = g.ID, g.Status.OK(), g.Reason
	// Fill board SHA from current if not set.
	if g.BoardSHA == "" && g.Scope == "board" {
		g.BoardSHA = reg.boardSHA
	}
	// Apply waivers through the legacy adapter.
	legacy := GateRecord2gateResult(g)
	applyWaivers(&legacy, reg.waivers)
	if legacy.Pass && !g.Pass {
		// Waivers turned a fail into a pass.
		g.Status = gate.Pass
		g.Waived = legacy.Waived
		g.Items = nil
	}
	g.Pass = g.Status.OK()
	reg.records = append(reg.records, g)
	// stderr: unified format for every gate.
	mark := statusMark(g.Status)
	fmt.Fprintf(reg.stderr, "gate %-24s %s  %s\n", g.ID, mark, g.Reason)
}

// SetBoardSHA updates the current board hash after a geometry stage writes a
// new board. Earlier records keep their original SHA. Board-scoped gates
// registered after this call carry the updated SHA for traceability;
// Aggregate/ExitCode do not enforce it.
func (reg *gateRegistry) SetBoardSHA(sha string) {
	reg.boardSHA = sha
}

// Reset drops all records except those matching keepIDs, mirroring the
// qualityGates() reset that runs when a new board candidate is judged.
// Without this, stale failed gates from superseded boards would permanently
// fail the run.
func (reg *gateRegistry) Reset(keepIDs map[string]bool) {
	var keep []GateRecord
	for _, g := range reg.records {
		if keepIDs[g.ID] {
			keep = append(keep, g)
		}
	}
	reg.records = keep
}

// Records returns an ordered snapshot.
func (reg *gateRegistry) Records() []GateRecord {
	out := make([]GateRecord, len(reg.records))
	copy(out, reg.records)
	return out
}

// Aggregate returns the overall gate.Status: fail if any gate failed;
// cannot-check if any gate could not run (and none failed); pass if all passed.
// No records → cannot-check.
func (reg *gateRegistry) Aggregate() gate.Status {
	if len(reg.records) == 0 {
		return gate.CannotCheck
	}
	var rs []gate.Result
	for _, g := range reg.records {
		rs = append(rs, gate.Result{Name: g.ID, Status: g.Status})
	}
	return gate.Aggregate(rs...)
}

// ExitCode returns the process exit code: 0 pass, 6 fail, 5 cannot-check.
// A tool error (returned as error from runKicadRoute) sets exit 1 and
// preempts this.
func (reg *gateRegistry) ExitCode() int { return reg.Aggregate().ExitCode() }

// Passed returns true only when every gate passed.
func (reg *gateRegistry) Passed() bool { return reg.Aggregate().OK() }

// statusMark is the human-readable status word for stderr.
func statusMark(s gate.Status) string {
	switch s {
	case gate.Pass:
		return "PASS"
	case gate.Fail:
		return "FAIL"
	case gate.CannotCheck:
		return "CANNOT-CHECK"
	case gate.Warn:
		return "WARN"
	case gate.Skip:
		return "SKIP"
	default:
		return string(s)
	}
}

// toRecord converts a legacy gateResult to a GateRecord. The caller provides
// evidence paths, scope, and duration; status is inferred from Pass (fail for
// false, pass for true). Use toRecordCC for explicit cannot-check.
func toRecord(g gateResult, evidence []string, scope string, dur time.Duration) GateRecord {
	st := gate.Fail
	if g.Pass {
		st = gate.Pass
	}
	return GateRecord{
		ID: g.Gate, Status: st, Reason: g.Detail,
		Items: g.Items, Waived: g.Waived, Info: g.Info,
		Evidence: evidence, Scope: scope,
		DurationS: dur.Seconds(),
	}
}

// toRecordCC converts a legacy gateResult to a GateRecord with an explicit
// cannot-check status (overrides the Pass bool).
func toRecordCC(g gateResult, reason string, evidence []string, scope string) GateRecord {
	return GateRecord{
		ID: g.Gate, Status: gate.CannotCheck, Reason: reason,
		Items: g.Items, Info: g.Info,
		Evidence: evidence, Scope: scope,
	}
}

// GateRecord2gateResult converts back for waiver application.
func GateRecord2gateResult(g GateRecord) gateResult {
	return gateResult{
		Gate: g.ID, Pass: g.Status.OK(), Detail: g.Reason,
		Items: g.Items, Waived: g.Waived, Info: g.Info,
	}
}

// boardFileSHA256 computes the sha256 of a file on disk.
// (Delegates to filePathSHA256 in route_instrumentation.go.)
func boardFileSHA256(path string) string {
	return filePathSHA256(path)
}
