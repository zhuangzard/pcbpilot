package app

// route_completeness.go — what a route run still lacks to be release-ready.
//
// A run's exit code says whether a gate failed. It does not say the work is
// done: a gate accepted by a waiver, a gate that did not run, a design
// report whose own verdict is FAIL or an analog design without an analog
// simulation all leave the board unfinished. The summary therefore carries
// complete / incomplete[] next to pass, and the last line a run prints says
// which of the two it is.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
)

// reAgentSigner matches a waiver signature that names an AI agent instead of
// a person. A heuristic on free text: it only adds a warning.
var reAgentSigner = regexp.MustCompile(`(?i)\b(claude|codex|chatgpt|gpt|gemini|kimi|copilot|agent|assistant|ai|llm|bot|pcbpilot)\b`)

// agentSigned reports whether by looks like an AI agent's signature.
func agentSigned(by string) bool { return reAgentSigner.MatchString(by) }

// waiverSigners lists who signed the waivers behind waived (the items of
// applyWaivers / waiveItems, which end in "waived: <reason> (<by>)").
func waiverSigners(waived []string, ws []gateWaiver) []string {
	var out []string
	seen := map[string]bool{}
	for _, it := range waived {
		for _, w := range ws {
			if !seen[w.By] && strings.HasSuffix(it, fmt.Sprintf("waived: %s (%s)", w.Reason, w.By)) {
				seen[w.By] = true
				out = append(out, w.By)
			}
		}
	}
	return out
}

// usedWaiver is one waiver a run applied, as the summary lists it.
type usedWaiver struct {
	Gate        string `json:"gate"`
	Match       string `json:"match"`
	Reason      string `json:"reason"`
	By          string `json:"by"`
	AgentSigned bool   `json:"agentSigned,omitempty"`
}

// usedWaivers lists the waivers of ws that accepted an item of waived.
func usedWaivers(waived []string, ws []gateWaiver) []usedWaiver {
	var out []usedWaiver
	seen := map[gateWaiver]bool{}
	for _, it := range waived {
		for _, w := range ws {
			if !seen[w] && strings.Contains(it, w.Match) && strings.HasSuffix(it, fmt.Sprintf("waived: %s (%s)", w.Reason, w.By)) {
				seen[w] = true
				out = append(out, usedWaiver{Gate: w.Gate, Match: w.Match, Reason: w.Reason, By: w.By, AgentSigned: agentSigned(w.By)})
			}
		}
	}
	return out
}

// runCompleteness is the release-readiness of one route run.
type runCompleteness struct {
	// Incomplete lists what stands between the run and a finished board
	// ("<gate> <status>: <why>"); empty = complete.
	Incomplete []string
	Waivers    []usedWaiver
}

func (c runCompleteness) complete() bool { return len(c.Incomplete) == 0 }

// write puts complete / incomplete / waivers into a run summary.
func (c runCompleteness) write(summary map[string]any) {
	summary["complete"] = c.complete()
	inc := c.Incomplete
	if inc == nil {
		inc = []string{}
	}
	summary["incomplete"] = inc
	if len(c.Waivers) > 0 {
		summary["waivers"] = c.Waivers
	} else {
		delete(summary, "waivers")
	}
}

// print writes the run's last line: release-ready or not, and what is
// missing; before it a warning per waiver, louder for an agent's signature.
func (c runCompleteness) print(w io.Writer, cmd string) {
	for _, u := range c.Waivers {
		if u.AgentSigned {
			fmt.Fprintf(w, "WARNING: the waiver for %s (%q) is signed by %q, which names an AI agent: an agent-signed waiver is not a review and not the user's decision — a waiver must come from the user\n", u.Gate, u.Match, u.By)
		} else {
			fmt.Fprintf(w, "warning: %s (%q) was accepted by a waiver signed by %q, not checked\n", u.Gate, u.Match, u.By)
		}
	}
	if c.complete() {
		fmt.Fprintf(w, "%s: COMPLETE — every gate ran and passed, no waiver\n", cmd)
		return
	}
	fmt.Fprintf(w, "%s: NOT release-ready — %d item(s) missing: %s\n", cmd, len(c.Incomplete), strings.Join(c.Incomplete, "; "))
}

// clipReason shortens a gate detail for the incomplete list.
func clipReason(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}

// uniq drops repeats, keeping the first of each (a stage that runs twice,
// such as the design review, reports its waiver once).
func uniq[T comparable](in []T) []T {
	seen := map[T]bool{}
	var out []T
	for _, x := range in {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// recordsCompleteness judges registry records (kicad route): every gate must
// be pass — not waived, not skipped, not cannot-check — and at least one gate
// must have run; runErr is the tool error of the run, if any.
func recordsCompleteness(recs []GateRecord, ws []gateWaiver, runErr error) (c runCompleteness) {
	if runErr != nil {
		c.Incomplete = append(c.Incomplete, "run stopped: "+clipReason(runErr.Error()))
	}
	if len(recs) == 0 {
		c.Incomplete = append(c.Incomplete, "no gate ran")
	}
	defer func() { c.Incomplete, c.Waivers = uniq(c.Incomplete), uniq(c.Waivers) }()
	for _, g := range recs {
		c.Waivers = append(c.Waivers, usedWaivers(g.Waived, ws)...)
		switch g.Status {
		case gate.Pass:
		case gate.Waived:
			c.Incomplete = append(c.Incomplete, fmt.Sprintf("%s waived by %s: %s", g.ID, signerList(g.WaivedBy), clipReason(g.Reason)))
		case gate.Skip:
			c.Incomplete = append(c.Incomplete, fmt.Sprintf("%s not run: %s", g.ID, clipReason(g.Reason)))
		default:
			c.Incomplete = append(c.Incomplete, fmt.Sprintf("%s %s: %s", g.ID, g.Status, clipReason(g.Reason)))
		}
	}
	return c
}

// legacyGateStatus is the registry status of a legacy gate result: a pass
// that rests on a waiver is waived, a gate that did not run is skip.
func legacyGateStatus(g gateResult) gate.Status {
	st := toRecord(g, nil, "", 0).Status
	if st == gate.Pass && len(g.Waived) > 0 {
		st = gate.Waived
	}
	return st
}

// gatesCompleteness is recordsCompleteness for the legacy gate list (pcb
// auto route).
func gatesCompleteness(gates []gateResult, ws []gateWaiver, runErr error) runCompleteness {
	var recs []GateRecord
	for _, g := range gates {
		r := toRecord(g, nil, "", 0)
		r.Status = legacyGateStatus(g)
		r.WaivedBy = waiverSigners(g.Waived, ws)
		recs = append(recs, r)
	}
	return recordsCompleteness(recs, ws, runErr)
}

func signerList(by []string) string {
	if len(by) == 0 {
		return "an unnamed waiver"
	}
	return `"` + strings.Join(by, `", "`) + `"`
}

// gateMark is the status word of a legacy gate on stderr.
func gateMark(g gateResult) string {
	switch {
	case g.NotRun:
		return "NOT-RUN"
	case g.Pass && len(g.Waived) > 0:
		return "WAIVED"
	case g.Pass:
		return "PASS"
	case g.CannotCheck:
		return "CANNOT-CHECK"
	}
	return "FAIL"
}

// intentAnalog is intent.json "analog": the summary of the analog SPICE step
// intent derive ran on the same schematic (pkg/intent AnalogInfo).
type intentAnalog struct {
	Ngspice   string `json:"ngspice"` // version, or "missing"
	Blocks    int    `json:"blocks"`
	Simulated int    `json:"simulated"`
	Failing   int    `json:"failing"`
	Status    string `json:"status"`
	Output    string `json:"output,omitempty"`
}

// analogSimGate is the analog-sim gate: a design with analog blocks
// (op-amp stages, filters, ADC front ends, references …) needs an ngspice
// simulation of them on the same schematic.
//
// The evidence is the intent's own analog summary (intent derive runs the
// analog step on the schematic the intent is derived from), or analogPath
// (--analog, a 'pcbpilot sim analog' document) which must describe the same
// number of blocks. Blocks found and ngspice missing, nothing simulated, or
// a FAIL status fail the gate; no evidence at all (an intent derived with
// --no-analog, from a board, or by hand) is "not run": the run goes on and
// is reported as not complete.
func analogSimGate(in *designIntent, analogPath string) gateResult {
	g := gateResult{Gate: "analog-sim"}
	var a *intentAnalog
	src := "intent.json analog"
	if in != nil {
		a = in.Analog
	}
	if analogPath != "" {
		b, err := os.ReadFile(analogPath)
		if err != nil {
			g.Detail = "--analog: " + err.Error()
			return g
		}
		var out analogsim.Output
		if err := json.Unmarshal(b, &out); err != nil {
			g.Detail = fmt.Sprintf("--analog %s: %v", analogPath, err)
			return g
		}
		if out.SchemaVersion != analogsim.SchemaVersion {
			g.Detail = fmt.Sprintf("--analog %s: schemaVersion %d is not a pcbpilot sim analog document (want %d)", analogPath, out.SchemaVersion, analogsim.SchemaVersion)
			return g
		}
		if out.Summary.Blocks != len(out.Blocks) {
			g.Detail = fmt.Sprintf("--analog %s: summary counts %d block(s), the document lists %d", analogPath, out.Summary.Blocks, len(out.Blocks))
			g.Items = []string{fmt.Sprintf("analog simulation document is inconsistent (summary.blocks %d, %d listed): re-run pcbpilot sim analog --out %s", out.Summary.Blocks, len(out.Blocks), analogPath)}
			return g
		}
		doc := &intentAnalog{Ngspice: out.Ngspice.Version, Blocks: len(out.Blocks), Simulated: out.Summary.Simulated,
			Failing: out.Summary.Failing, Status: out.Summary.Status, Output: analogPath}
		if !out.Ngspice.Available {
			doc.Ngspice = "missing"
		}
		if a != nil && a.Blocks != doc.Blocks {
			g.Detail = fmt.Sprintf("%s has %d analog block(s), the intent's schematic has %d", analogPath, doc.Blocks, a.Blocks)
			g.Items = []string{fmt.Sprintf("analog simulation is not of this design: %s has %d block(s), intent.json analog has %d — re-run pcbpilot sim analog on the schematic the intent was derived from", analogPath, doc.Blocks, a.Blocks)}
			return g
		}
		a, src = doc, analogPath
	}
	switch {
	case a == nil:
		g.Pass, g.NotRun = true, true
		g.Detail = "not run: the intent has no analog summary (derived with --no-analog, from a board, or not by intent derive) and no --analog analog.json was given, so analog coverage is unknown — run pcbpilot intent derive without --no-analog, or pcbpilot sim analog --out analog.json and pass --analog analog.json"
	case a.Blocks == 0:
		g.Pass = true
		g.Detail = fmt.Sprintf("no analog blocks detected (%s)", src)
	default:
		g.Detail = fmt.Sprintf("%d analog block(s), %d simulated, ngspice %s, status %s (%s)", a.Blocks, a.Simulated, nzs(a.Ngspice, "version unknown"), nzs(a.Status, "unknown"), src)
		switch {
		case a.Ngspice == "missing":
			g.Items = append(g.Items, fmt.Sprintf("%d analog block(s) found but ngspice is not installed, only analytic checks ran: run pcbpilot sim tools check (install: pcbpilot sim tools install --yes), then re-run pcbpilot intent derive / pcbpilot sim analog on this design", a.Blocks))
		case a.Simulated == 0:
			g.Items = append(g.Items, fmt.Sprintf("none of the %d analog block(s) was simulated: see the sim analog report for why each block was skipped", a.Blocks))
		}
		switch st := strings.ToUpper(a.Status); {
		case st == "FAIL" || a.Failing > 0:
			g.Items = append(g.Items, fmt.Sprintf("analog simulation status %s (%d failing target(s)): fix the design or its targets and re-run pcbpilot sim analog", nzs(a.Status, "unknown"), a.Failing))
		case st != "PASS" && st != "WARN":
			g.Items = append(g.Items, fmt.Sprintf("analog simulation status %q is not a result (want PASS or WARN): re-run pcbpilot sim analog on this design", a.Status))
		}
		if a.Simulated < 0 || a.Failing < 0 || a.Simulated > a.Blocks {
			g.Items = append(g.Items, fmt.Sprintf("analog summary is inconsistent: %d simulated of %d block(s)", a.Simulated, a.Blocks))
		}
		g.Pass = len(g.Items) == 0
		if g.Pass && a.Simulated < a.Blocks {
			g.Info = append(g.Info, fmt.Sprintf("%d of %d block(s) simulated with ngspice; the others were judged analytically (see the sim analog report)", a.Simulated, a.Blocks))
		}
	}
	return g
}

// nzs is s, or d when s is empty.
func nzs(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// reportVerdictItems are the failing items of a design-report gate whose
// published report has verdict FAIL (nil otherwise): the report is the
// document the user reads, a run cannot pass over its FAIL.
func reportVerdictItems(status string, reasons []string) []string {
	if !strings.EqualFold(status, "FAIL") {
		return nil
	}
	if len(reasons) == 0 {
		return []string{"report verdict FAIL"}
	}
	var items []string
	for _, r := range reasons {
		items = append(items, "report verdict FAIL: "+r)
	}
	return items
}
