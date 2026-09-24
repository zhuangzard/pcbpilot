package daemon

import (
	"fmt"
	"strings"
	"sync"

	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

// Daemon-level stale-read advisory.
//
// After a PCB mutation (rip-up / route / delete / via / track / pour edits) the
// per-document engine state serves STALE data to list/DRC reads until the
// document is reloaded (`pcbpilot doc reload`) — observed repeatedly on real
// boards. Until now this was enforced only by the agent remembering rule 5,
// helped along by a non-blocking `staleRisk` advisory this guard attached to the
// response.
//
// A stale result must remain visible, but it is not an authorization problem.
// Reads therefore continue and carry staleRisk. Example-driven batch workflows
// finish with save -> real reload -> readback when the result must be
// authoritative; if that refresh fails they report the data as unavailable.
// This keeps the evidence without making an agent unlock a workflow state.
//
// State machine (per windowId, in-memory):
//   SET    — a PCB-domain action with Mutates=true succeeds (catalog-driven,
//            same source of truth as autosave), except the exempt sets below.
//   CLEAR  — a `doc reload` completes. Reload is a CLI composite (save → typed
//            document.close → reopen), so a successful close is the daemon-side
//            proof that the old per-doc engine was discarded. Older clients used
//            debug.exec_js closeDocument; keep accepting that proof during the
//            compatibility window. A mere `doc switch`/document.open does NOT
//            clear. pcb.pour.rebuild also clears — it recomputes the pour
//            connectivity that goes stale (pour-mediated Connection Errors).
//   WARN   — a PCB-domain read (Mutates=false) arrives while the flag is set:
//            the response is returned with staleRisk populated.
//
// Exemptions (never SET the flag) — the first three are scar tissue, do not
// remove them:
//   - pcb.save: saving changes no copper; the daemon's debounced autosave also
//     bypasses /action entirely (dispatchSave), so neither path false-flags.
//   - pcb.pour.rebuild: it is the FIX for stale pour connectivity, not a new
//     hazard — it clears instead.
//   - any request carrying `dryRun:true`: the catalog's Mutates flag is
//     action-name granular and cannot see that a preview enumerates without
//     touching the board. `pcb clear --dry-run` used to arm the flag and make
//     every later read cry stale on an untouched board (issue #112).
//   - view-only editor state (staleViewOnlyActions): added when the advisory
//     became a refusal, because a false positive now costs a failed command
//     instead of a stderr line. See that var for the argument.
//
// windowIds churn on reconnect; a reconnected window starts clean (a window
// reload re-reads the saved document, which is exactly the stale-fix), so
// per-window in-memory state is the right lifetime.

// staleExemptActions never mark the window stale even though the catalog says
// Mutates=true (see package comment).
var staleExemptActions = map[string]bool{
	"pcb.save":         true,
	"pcb.pour.rebuild": true,
}

// staleViewOnlyActions are catalog-Mutates PCB actions that change only what the
// EDITOR SHOWS — the viewed side, the active layer, layer visibility — and never
// a primitive an enumeration reads back. They cannot make a later read stale, so
// they must not arm the flag.
//
// This was tolerable while the guard only annotated; as a refusal it would be a
// guaranteed false positive, because the action catalog itself prescribes the
// mutate→read sequence these would break: pcb.view.side declares
// VerifyWith=[pcb.layers.list, pcb.snapshot] and pcb.layers.set_current declares
// VerifyWith=[pcb.layers.list] (internal/protocol/actions.go). A guard that
// refuses the catalog's own documented verification step is wrong.
var staleViewOnlyActions = map[string]bool{
	"pcb.view.side":          true,
	"pcb.layers.set_current": true,
	"pcb.layers.visibility":  true,
	"pcb.origin.set":         true,
}

// staleMetadataReadActions return editor/document metadata that is independent
// of the PCB geometry engine. A pending copper/placement refresh cannot make
// the canvas-origin offset stale, so these reads must not inherit staleRisk.
var staleMetadataReadActions = map[string]bool{
	"pcb.origin.get": true,
}

// pcbStaleMarks reports whether a successful request should mark the window's
// PCB engine state as possibly stale: any PCB-domain mutating request
// (requestMutates = catalog Mutates minus dry-run previews, the same predicate
// autosave uses) minus the exempt set.
func pcbStaleMarks(req *protocol.Request) bool {
	return docTypeForAction(req.Action) == "pcb" && requestMutates(req) &&
		!staleExemptActions[req.Action] && !staleViewOnlyActions[req.Action]
}

// pcbStaleRead reports whether a request is a PCB-domain read that can return
// stale data (any non-mutating pcb.* request: lists, DRC, report, snapshot …).
// A dry-run preview counts as a read on BOTH sides of the state machine: it
// changes nothing (so it never marks), and its enumeration is read back off the
// same engine state (so it earns the advisory) — `pcb clear --dry-run` on an
// un-reloaded board is exactly the miscount that opened issue #112.
func pcbStaleRead(req *protocol.Request) bool {
	return docTypeForAction(req.Action) == "pcb" && !requestMutates(req) &&
		!staleMetadataReadActions[req.Action]
}

// pcbStaleClears reports whether a successful request resets the stale flag.
// `doc reload` has no single action — its unique step is document.close (or the
// legacy debug.exec_js closeDocument call); pcb.pour.rebuild clears because
// rebuilding pours is the documented stale-connectivity fix.
func pcbStaleClears(req *protocol.Request, resp *protocol.Response) bool {
	switch req.Action {
	case "pcb.pour.rebuild":
		return true
	case "document.close":
		if resp == nil {
			return false
		}
		closed, _ := resp.Result["closed"].(bool)
		return closed
	case "debug.exec_js":
		code, _ := req.Payload["code"].(string)
		if !strings.Contains(code, "closeDocument") || resp == nil {
			return false
		}
		// A debug action can be transport-successful while closeDocument itself
		// returns false.  Only the explicit result from doc reload proves that the
		// old document actually closed and its PCB engine state was discarded.
		value, _ := resp.Result["value"].(map[string]any)
		closed, _ := value["closed"].(bool)
		return closed
	}
	return false
}

// staleGuard is the per-window stale-read state machine. Methods are safe for
// concurrent use.
type staleGuard struct {
	mu sync.Mutex
	// last maps windowId → the name of the last successful PCB mutation not yet
	// followed by a reload ("" / absent = no stale risk).
	last map[string]string
}

func newStaleGuard() *staleGuard {
	return &staleGuard{last: map[string]string{}}
}

// observe applies one completed action to the state machine: it may annotate
// resp with a staleRisk advisory (reads while stale) and updates the per-window
// flag (successful mutations set it, reload/pour-rebuild clear it). Call it
// with the connector's response before writing it to the caller.
func (g *staleGuard) observe(req *protocol.Request, resp *protocol.Response) {
	if g == nil || req == nil || resp == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	// Annotate reads first: the read itself never changes the state.
	if pcbStaleRead(req) {
		if mutation := g.last[req.WindowID]; mutation != "" {
			resp.StaleRisk = staleRiskMessage(mutation, req.Action)
		}
		return
	}

	// Only successful actions move the state machine.
	if !resp.OK {
		return
	}
	if pcbStaleClears(req, resp) {
		delete(g.last, req.WindowID)
		return
	}
	if pcbStaleMarks(req) {
		g.last[req.WindowID] = req.Action
	}
}

// staleRiskMessage builds the advisory. Deliberately free of timestamps so the
// CLI can deduplicate identical warnings within one composite command.
func staleRiskMessage(mutation, read string) string {
	return fmt.Sprintf(
		"PCB was mutated by %s since the last reload — %s (and DRC) may read stale engine state; authoritative workflows should save, run `pcbpilot doc reload`, then read back; if refresh fails, treat the data as unavailable",
		mutation, read)
}
