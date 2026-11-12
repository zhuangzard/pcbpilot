package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
)

// A gate a waiver accepted is waived, never pass: own status, pass false,
// the signer on the record, WAIVED on stderr. The exit-code verdict still
// accepts it.
func TestGateRegistryWaivedIsNotPass(t *testing.T) {
	ws := []gateWaiver{{Gate: "design-review", Match: noReviewMatch, Reason: "prototype", By: "A. Reviewer"}}
	var stderr bytes.Buffer
	reg := newGateRegistry(&stderr, ws)
	g := designReviewGate("design", reviewRunOpts{noReview: true}, nil, ws, map[string]any{}, io.Discard)
	if !g.Pass || len(g.Waived) != 1 {
		t.Fatalf("the waiver must accept the skipped review for the exit code: %+v", g)
	}
	reg.Add(toRecord(g, nil, "", 0))
	rec := reg.Records()[0]
	if rec.Status != gate.Waived || rec.Pass || len(rec.WaivedBy) != 1 || rec.WaivedBy[0] != "A. Reviewer" {
		t.Fatalf("record %+v, want status waived, pass false, waivedBy the signer", rec)
	}
	if !reg.Passed() || reg.Aggregate().ExitCode() != 0 {
		t.Fatalf("a valid waiver keeps exit 0: aggregate %s", reg.Aggregate())
	}
	if s := stderr.String(); !strings.Contains(s, "WAIVED") || strings.Contains(s, "PASS") {
		t.Fatalf("stderr must say WAIVED, not PASS:\n%s", s)
	}
	b, _ := json.Marshal(rec)
	if !strings.Contains(string(b), `"status":"waived"`) || !strings.Contains(string(b), `"waivedBy":["A. Reviewer"]`) {
		t.Fatalf("summary record: %s", b)
	}
}

func TestAgentSigned(t *testing.T) {
	for by, want := range map[string]bool{
		"Claude (agent), on behalf of project owner": true,
		"codex":            true,
		"AI assistant":     true,
		"A. Reviewer":      false,
		"Gail, lead EE":    false,
		"Clauderic Aimard": false,
	} {
		if got := agentSigned(by); got != want {
			t.Errorf("agentSigned(%q) = %v, want %v", by, got, want)
		}
	}
}

// routeDoneRun runs the kicad route command around body and returns the exit
// code, summary.json and stderr.
func routeDoneRun(t *testing.T, waivers string, body func(*kicadRun) error) (int, map[string]any, string) {
	t.Helper()
	dir := t.TempDir()
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{"GND":{"role":"ground","widthMil":{"outer":16,"inner":16,"min":10},"clearanceMil":8}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	wf := filepath.Join(dir, "waivers.json")
	if err := os.WriteFile(wf, []byte(waivers), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "route")
	o := kicadRouteOpts{pcb: filepath.Join(dir, "x.kicad_pcb"), intent: intent, outDir: out, requirements: []string{intent}, waivers: wf, silk: defaultSilkTightOpts(), testRunBody: body}
	var stderr bytes.Buffer
	root := &cobra.Command{Use: "route", SilenceUsage: true, SilenceErrors: true, RunE: func(*cobra.Command, []string) error {
		return runKicadRoute(o, io.Discard, &stderr)
	}}
	root.SetArgs(nil)
	code := execute(root, &stderr)
	data, err := os.ReadFile(filepath.Join(out, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sum map[string]any
	if err := json.Unmarshal(data, &sum); err != nil {
		t.Fatal(err)
	}
	return code, sum, stderr.String()
}

func lastStderrLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// The field failure: --no-review under a waiver the agent signed itself was
// reported as design-review pass and the board delivered as done. The run
// still exits 0 (the waiver is valid), but the summary says complete:false
// with the reason, names the signer, and the last line is NOT release-ready.
func TestKicadRouteWaivedRunIsNotComplete(t *testing.T) {
	const by = "Claude (agent), on behalf of project owner"
	waivers := `[{"gate":"design-review","match":"--no-review","reason":"no reviewer CLI on this machine","by":"` + by + `"}]`
	code, sum, stderr := routeDoneRun(t, waivers, func(r *kicadRun) error {
		r.add(designReviewGate("design", reviewRunOpts{noReview: true}, nil, r.waivers, r.summary, io.Discard))
		r.add(gateResult{Gate: "native-drc", Pass: true, Detail: "0 violations"})
		return nil
	})
	if code != 0 || sum["pass"] != true {
		t.Fatalf("a valid waiver keeps the exit code: exit %d pass %v\n%s", code, sum["pass"], stderr)
	}
	if sum["complete"] != false {
		t.Fatalf("complete = %v, want false", sum["complete"])
	}
	inc, _ := sum["incomplete"].([]any)
	if len(inc) != 1 || !strings.HasPrefix(inc[0].(string), "design-review waived by ") || !strings.Contains(inc[0].(string), by) {
		t.Fatalf("incomplete = %v", inc)
	}
	ws, _ := sum["waivers"].([]any)
	if len(ws) != 1 {
		t.Fatalf("waivers = %v", sum["waivers"])
	}
	if w := ws[0].(map[string]any); w["gate"] != "design-review" || w["by"] != by || w["agentSigned"] != true {
		t.Fatalf("waiver = %v", w)
	}
	g := sum["gates"].([]any)[0].(map[string]any)
	if g["status"] != "waived" || g["pass"] != false || g["waivedBy"].([]any)[0] != by {
		t.Fatalf("design-review gate = %v", g)
	}
	if !strings.Contains(stderr, "WARNING: the waiver for design-review") || !strings.Contains(stderr, "an agent-signed waiver is not a review") {
		t.Fatalf("no agent-signed warning:\n%s", stderr)
	}
	if l := lastStderrLine(stderr); !strings.HasPrefix(l, "kicad route: NOT release-ready — 1 item(s) missing: design-review waived") {
		t.Fatalf("last line: %s", l)
	}
	if strings.Contains(stderr, "design-review    PASS") {
		t.Fatalf("a waived review printed as PASS:\n%s", stderr)
	}
}

// A person's waiver is still not a pass, but it is not flagged as agent-signed.
func TestKicadRouteUserWaiverIsNotAgentSigned(t *testing.T) {
	waivers := `[{"gate":"design-review","match":"--no-review","reason":"reviewed on paper","by":"A. Reviewer"}]`
	_, sum, stderr := routeDoneRun(t, waivers, func(r *kicadRun) error {
		r.add(designReviewGate("design", reviewRunOpts{noReview: true}, nil, r.waivers, r.summary, io.Discard))
		return nil
	})
	if sum["complete"] != false {
		t.Fatalf("complete = %v", sum["complete"])
	}
	if w := sum["waivers"].([]any)[0].(map[string]any); w["agentSigned"] != nil {
		t.Fatalf("waiver = %v", w)
	}
	if strings.Contains(stderr, "WARNING:") || !strings.Contains(stderr, `accepted by a waiver signed by "A. Reviewer", not checked`) {
		t.Fatalf("stderr:\n%s", stderr)
	}
}

// Every gate pass, none waived: complete, and the last line says so. A gate
// that did not run (analog-sim without evidence) keeps the exit code and
// makes the run incomplete; a failing gate is listed too.
func TestKicadRouteCompleteness(t *testing.T) {
	code, sum, stderr := routeDoneRun(t, "[]", func(r *kicadRun) error {
		r.add(gateResult{Gate: "native-drc", Pass: true, Detail: "0 violations"})
		r.add(analogSimGate(&designIntent{Analog: &intentAnalog{Ngspice: "45", Blocks: 0, Status: "INFO"}}, ""))
		return nil
	})
	if code != 0 || sum["complete"] != true || len(sum["incomplete"].([]any)) != 0 || sum["waivers"] != nil {
		t.Fatalf("exit %d summary complete=%v incomplete=%v waivers=%v\n%s", code, sum["complete"], sum["incomplete"], sum["waivers"], stderr)
	}
	if l := lastStderrLine(stderr); !strings.HasPrefix(l, "kicad route: COMPLETE") {
		t.Fatalf("last line: %s", l)
	}

	code, sum, stderr = routeDoneRun(t, "[]", func(r *kicadRun) error {
		r.add(gateResult{Gate: "native-drc", Pass: true, Detail: "0 violations"})
		r.add(analogSimGate(&designIntent{}, ""))
		return nil
	})
	if code != 0 || sum["pass"] != true || sum["complete"] != false {
		t.Fatalf("not-run analog: exit %d pass %v complete %v\n%s", code, sum["pass"], sum["complete"], stderr)
	}
	if inc := sum["incomplete"].([]any); len(inc) != 1 || !strings.HasPrefix(inc[0].(string), "analog-sim not run: ") {
		t.Fatalf("incomplete = %v", inc)
	}
	g := sum["gates"].([]any)[1].(map[string]any)
	if g["status"] != "skip" || g["pass"] != false || g["advisory"] != true {
		t.Fatalf("analog-sim gate = %v", g)
	}
	if !strings.Contains(stderr, "NOT-RUN") || !strings.Contains(lastStderrLine(stderr), "NOT release-ready") {
		t.Fatalf("stderr:\n%s", stderr)
	}

	code, sum, stderr = routeDoneRun(t, "[]", func(r *kicadRun) error {
		r.add(gateResult{Gate: "signoff", Detail: "2 check(s) failed", Items: []string{"report verdict FAIL"}})
		return nil
	})
	if code != 6 || sum["pass"] != false || sum["complete"] != false {
		t.Fatalf("failing gate: exit %d pass %v complete %v", code, sum["pass"], sum["complete"])
	}
	if inc := sum["incomplete"].([]any); len(inc) != 1 || !strings.HasPrefix(inc[0].(string), "signoff fail: ") {
		t.Fatalf("incomplete = %v\n%s", inc, stderr)
	}
}

func TestAnalogSimGate(t *testing.T) {
	dir := t.TempDir()
	doc := func(name string, o analogsim.Output) string {
		p := filepath.Join(dir, name)
		b, _ := json.Marshal(o)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	blocks := func(n int) []*analogsim.Block {
		out := make([]*analogsim.Block, n)
		for i := range out {
			out[i] = &analogsim.Block{}
		}
		return out
	}
	simulated := doc("ok.json", analogsim.Output{SchemaVersion: analogsim.SchemaVersion, Ngspice: analogsim.NgspiceInfo{Available: true, Version: "45"},
		Summary: analogsim.Summary{Blocks: 2, Simulated: 2, Status: "PASS"}, Blocks: blocks(2)})
	noSpice := doc("nospice.json", analogsim.Output{SchemaVersion: analogsim.SchemaVersion,
		Summary: analogsim.Summary{Blocks: 2, Status: "WARN"}, Blocks: blocks(2)})
	notADoc := filepath.Join(dir, "other.json")
	if err := os.WriteFile(notADoc, []byte(`{"nets":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name   string
		in     *designIntent
		path   string
		pass   bool
		notRun bool
		want   string // in the detail or an item
	}{
		{"no intent, no document: not run", nil, "", true, true, "not run"},
		{"intent without analog summary: not run", &designIntent{}, "", true, true, "pcbpilot sim analog --out analog.json"},
		{"no analog blocks", &designIntent{Analog: &intentAnalog{Ngspice: "45", Status: "INFO"}}, "", true, false, "no analog blocks detected"},
		{"blocks simulated", &designIntent{Analog: &intentAnalog{Ngspice: "45", Blocks: 3, Simulated: 3, Status: "PASS"}}, "", true, false, "3 analog block(s), 3 simulated"},
		{"blocks, ngspice missing", &designIntent{Analog: &intentAnalog{Ngspice: "missing", Blocks: 3, Status: "WARN"}}, "", false, false, "pcbpilot sim tools check"},
		{"blocks, none simulated", &designIntent{Analog: &intentAnalog{Ngspice: "45", Blocks: 3, Status: "WARN"}}, "", false, false, "none of the 3 analog block(s) was simulated"},
		{"status missing", &designIntent{Analog: &intentAnalog{Ngspice: "45", Blocks: 1, Simulated: 1}}, "", false, false, "is not a result"},
		{"failing targets under a PASS status", &designIntent{Analog: &intentAnalog{Ngspice: "45", Blocks: 1, Simulated: 1, Failing: 1, Status: "PASS"}}, "", false, false, "1 failing target(s)"},
		{"simulation FAIL", &designIntent{Analog: &intentAnalog{Ngspice: "45", Blocks: 3, Simulated: 3, Failing: 2, Status: "FAIL"}}, "", false, false, "analog simulation status FAIL (2 failing target(s))"},
		{"--analog covers an intent without summary", &designIntent{}, simulated, true, false, "2 analog block(s), 2 simulated"},
		{"--analog replaces a missing-ngspice intent summary", &designIntent{Analog: &intentAnalog{Ngspice: "missing", Blocks: 2}}, simulated, true, false, "ok.json"},
		{"--analog run without ngspice", &designIntent{}, noSpice, false, false, "ngspice is not installed"},
		{"--analog of another design", &designIntent{Analog: &intentAnalog{Ngspice: "missing", Blocks: 5}}, simulated, false, false, "not of this design"},
		{"--analog is not a sim analog document", &designIntent{}, notADoc, false, false, "not a pcbpilot sim analog document"},
		{"--analog unreadable", &designIntent{}, filepath.Join(dir, "none.json"), false, false, "--analog"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := analogSimGate(c.in, c.path)
			if g.Gate != "analog-sim" || g.Pass != c.pass || g.NotRun != c.notRun {
				t.Fatalf("%+v, want pass %v notRun %v", g, c.pass, c.notRun)
			}
			if all := g.Detail + "\n" + strings.Join(g.Items, "\n"); !strings.Contains(all, c.want) {
				t.Fatalf("no %q in:\n%s", c.want, all)
			}
			if !c.pass && c.notRun {
				t.Fatal("a failing gate is not a not-run gate")
			}
		})
	}
}

// A published report whose own verdict is FAIL fails the design-report gate.
func TestReportVerdictItems(t *testing.T) {
	if it := reportVerdictItems("PASS", nil); it != nil {
		t.Fatalf("PASS: %v", it)
	}
	if it := reportVerdictItems("WARN", []string{"x"}); it != nil {
		t.Fatalf("WARN: %v", it)
	}
	if it := reportVerdictItems("FAIL", nil); len(it) != 1 || it[0] != "report verdict FAIL" {
		t.Fatalf("FAIL without reasons: %v", it)
	}
	it := reportVerdictItems("fail", []string{"IR drop 9 % over the 5 % limit", "2 DRC error(s)"})
	if len(it) != 2 || it[0] != "report verdict FAIL: IR drop 9 % over the 5 % limit" {
		t.Fatalf("FAIL with reasons: %v", it)
	}
	// The gate rule both route paths use: no failing item ⇔ pass.
	g := gateResult{Gate: "design-report", Items: it}
	g.Pass = len(g.Items) == 0
	if g.Pass {
		t.Fatal("a FAIL report passed the gate")
	}
	for file, want := range map[string]string{
		"cmd_kicad_route.go":    "reportVerdictItems(rep.Verdict.Status, rep.Verdict.Reasons)",
		"cmd_pcb_auto_route.go": "reportVerdictItems(rep.Verdict.Status, rep.Verdict.Reasons)",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), want) {
			t.Errorf("%s: the design-report gate no longer reads the report verdict", file)
		}
	}
}

// gate-set judges only that every mandatory gate ran; its detail must not
// read as "all passed" when one failed, was waived or did not run.
func TestGateSetNamesGatesThatDidNotPass(t *testing.T) {
	var all []gateResult
	for _, m := range mandatoryRouteGates {
		all = append(all, gateResult{Gate: m.names[0], Pass: true})
	}
	if g := gateSetGate(all); !g.Pass || !strings.HasSuffix(g.Detail, "mandatory gate(s) ran, all passed") {
		t.Fatalf("all pass: %+v", g)
	}
	for i := range all {
		switch all[i].Gate {
		case "signoff":
			all[i].Pass = false
		case "design-review":
			all[i].Waived = []string{"design review skipped (--no-review) — waived: r (x)"}
		case "analog-sim":
			all[i].NotRun = true
		}
	}
	g := gateSetGate(all)
	if !g.Pass {
		t.Fatalf("every gate ran: %+v", g)
	}
	for _, w := range []string{"3 did NOT pass", "signoff (fail)", "design-review (waived)", "analog-sim (not run)"} {
		if !strings.Contains(g.Detail, w) {
			t.Errorf("no %q in %q", w, g.Detail)
		}
	}
	if strings.Contains(g.Detail, "all passed") {
		t.Errorf("detail: %s", g.Detail)
	}
}

// pcb auto route's legacy gate list gets the same verdict.
func TestGatesCompleteness(t *testing.T) {
	ws := []gateWaiver{{Gate: "silkscreen", Match: "overlap", Reason: "cosmetic", By: "codex"}}
	silk := gateResult{Gate: "silkscreen", Items: []string{"label overlap R1"}}
	applyWaivers(&silk, ws)
	c := gatesCompleteness([]gateResult{{Gate: "native-drc", Pass: true}, silk, analogSimGate(nil, "")}, ws, nil)
	if c.complete() || len(c.Incomplete) != 2 || len(c.Waivers) != 1 || !c.Waivers[0].AgentSigned {
		t.Fatalf("%+v", c)
	}
	if gateMark(silk) != "WAIVED" || gateMark(analogSimGate(nil, "")) != "NOT-RUN" || gateMark(gateResult{Pass: true}) != "PASS" ||
		gateMark(gateResult{}) != "FAIL" || gateMark(gateResult{CannotCheck: true}) != "CANNOT-CHECK" {
		t.Fatal("gateMark")
	}
	sum := map[string]any{}
	c.write(sum)
	if sum["complete"] != false || len(sum["incomplete"].([]string)) != 2 {
		t.Fatalf("%v", sum)
	}
	if c := gatesCompleteness([]gateResult{{Gate: "native-drc", Pass: true}}, nil, nil); !c.complete() {
		t.Fatalf("%+v", c)
	}
	if c := gatesCompleteness(nil, nil, nil); c.complete() {
		t.Fatal("a run without gates is not complete")
	}
}

// sch-build: ok says the build succeeded; a skipped, waived or warn gate
// leaves it not complete, in the report and on the last stderr line.
func TestSbReportIncomplete(t *testing.T) {
	rep := &sbReport{OK: true, Outputs: map[string]string{}}
	rep.gate("erc", "pass", "0 errors", nil)
	sbReviewGate(rep, sbOptions{}, t.TempDir(), nil) // no --requirements: skip
	rep.gate(schReviewGate, "warn", "3 finding(s)", nil)
	var so, se bytes.Buffer
	if err := finishSbRun(rep, nil, "", &so, &se); err != nil {
		t.Fatalf("a build without a failed gate keeps exit 0: %v", err)
	}
	if !rep.OK || rep.Complete || len(rep.Incomplete) != 2 {
		t.Fatalf("ok %v complete %v incomplete %v", rep.OK, rep.Complete, rep.Incomplete)
	}
	if !strings.HasPrefix(rep.Incomplete[0], "design-review skip: ") || !strings.HasPrefix(rep.Incomplete[1], schReviewGate+" warn: ") {
		t.Fatalf("incomplete = %v", rep.Incomplete)
	}
	if l := lastStderrLine(se.String()); !strings.HasPrefix(l, "sch-build: built, but NOT complete — 2 gate(s) not pass: design-review skip") {
		t.Fatalf("last line: %s", l)
	}
	var out map[string]any
	if err := json.Unmarshal(so.Bytes(), &out); err != nil || out["ok"] != true || out["complete"] != false || len(out["incomplete"].([]any)) != 2 {
		t.Fatalf("stdout report: %v %v", err, out)
	}

	// --no-review under a waiver: waived, never pass; an agent's signature
	// is called out.
	ws := []gateWaiver{{Gate: "design-review", Match: noReviewMatch, Reason: "later", By: "Claude (agent)"}}
	rep = &sbReport{OK: true, Outputs: map[string]string{}}
	sbReviewGate(rep, sbOptions{Requirements: []string{"req.md"}, NoReview: true}, t.TempDir(), ws)
	if g := rep.Gates[0]; g.Status != "waived" || !strings.Contains(g.Detail, `"Claude (agent)"`) || !strings.Contains(g.Detail, "NOT reviewed") {
		t.Fatalf("gate %+v", g)
	}
	se.Reset()
	if err := finishSbRun(rep, nil, "", io.Discard, &se); err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Complete || !strings.Contains(se.String(), "WARNING: the design-review waiver is signed by") || !strings.Contains(lastStderrLine(se.String()), "design-review waived") {
		t.Fatalf("ok %v complete %v\n%s", rep.OK, rep.Complete, se.String())
	}

	// All gates pass: complete, nothing extra printed.
	rep = &sbReport{OK: true}
	rep.gate("erc", "pass", "", nil)
	se.Reset()
	if err := finishSbRun(rep, nil, "", io.Discard, &se); err != nil || !rep.Complete || se.Len() != 0 {
		t.Fatalf("complete %v err %v stderr %q", rep.Complete, err, se.String())
	}
	// A failed build is never complete.
	rep = &sbReport{OK: true}
	rep.gate("erc", "fail", "2 errors", nil)
	if err := finishSbRun(rep, nil, "", io.Discard, io.Discard); err == nil || rep.Complete {
		t.Fatalf("failed gate: err %v complete %v", err, rep.Complete)
	}
}

// A signed skip waiver covers what the skip leaves missing downstream, and
// nothing else; unsigned skips derive nothing.
func TestWithSkipWaivers(t *testing.T) {
	ws := []gateWaiver{
		{Gate: "board-manual", Match: "--no-manual", Reason: "prototype", By: "owner"},
		{Gate: "design-review", Match: "--no-review", Reason: "prototype", By: "owner"},
	}
	if got := withSkipWaivers(ws, false, false); len(got) != 2 {
		t.Fatalf("no skip flag must derive nothing: %+v", got)
	}
	got := withSkipWaivers(ws, true, true)
	want := map[string]bool{"design-report": false, "signoff-artifacts": false, "signoff-review": false}
	for _, w := range got[2:] {
		if _, ok := want[w.Gate]; !ok || w.By != "owner" || w.Reason != "prototype" {
			t.Fatalf("unexpected derived waiver %+v", w)
		}
		want[w.Gate] = true
	}
	for g, seen := range want {
		if !seen {
			t.Fatalf("no derived waiver for %s: %+v", g, got)
		}
	}
	g := gateResult{Gate: "design-report", Items: []string{"report input missing: manual", "report verdict FAIL"}}
	applyWaivers(&g, got)
	if g.Pass {
		t.Fatalf("a failing report verdict must not be waived by --no-manual: %+v", g)
	}
	if got := withSkipWaivers(nil, true, true); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// A stage recorded twice reports its waiver and its gap once.
func TestRecordsCompletenessDedups(t *testing.T) {
	ws := []gateWaiver{{Gate: "design-review", Match: "--no-review", Reason: "r", By: "owner"}}
	rec := GateRecord{ID: "design-review", Status: gate.Waived, Reason: "skipped",
		Waived: []string{"design review skipped (--no-review) — waived: r (owner)"}, WaivedBy: []string{"owner"}}
	c := recordsCompleteness([]GateRecord{rec, rec}, ws, nil)
	if len(c.Incomplete) != 1 || len(c.Waivers) != 1 {
		t.Fatalf("%+v", c)
	}
}
