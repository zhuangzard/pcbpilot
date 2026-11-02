package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// Auto mode defers on a partial result even at a count of 0; --no-silk-place
// and --silk=off stay the user's skips, and --silk=on always places.
func TestSilkDecision(t *testing.T) {
	complete := &routeResult{}
	partial0 := &routeResult{Partial: true}
	unknown := &routeResult{Partial: true, UnroutedCount: -1}
	open := &routeResult{UnroutedCount: 3}
	for _, c := range []struct {
		name   string
		o      kicadRouteOpts
		route  *routeResult
		reason string
		auto   bool
		budget time.Duration
	}{
		{"auto, complete", kicadRouteOpts{silkMode: "auto", silkBudget: time.Hour}, complete, "", false, 0},
		{"auto, partial at 0", kicadRouteOpts{silkMode: "auto"}, partial0, "partial result, 0 unrouted", true, 0},
		{"auto, count unknown", kicadRouteOpts{silkMode: "auto"}, unknown, "unrouted count unknown", true, 0},
		{"auto, unrouted", kicadRouteOpts{silkMode: "auto"}, open, "3 unrouted", true, 0},
		{"on, partial", kicadRouteOpts{silkMode: "on", silkBudget: time.Hour}, partial0, "", false, time.Hour},
		{"off", kicadRouteOpts{silkMode: "off"}, partial0, "--silk=off", false, 0},
		{"--no-silk-place", kicadRouteOpts{silkMode: "auto", noSilkPlace: true}, partial0, "--no-silk-place", false, 0},
	} {
		r := &kicadRun{o: c.o, route: c.route}
		reason, auto, budget := r.silkDecision()
		if auto != c.auto || budget != c.budget || (c.reason == "") != (reason == "") || !strings.Contains(reason, c.reason) {
			t.Errorf("%s: %q %v %s", c.name, reason, auto, budget)
		}
	}
}

// A board whose labels are already clean does not report silkscreen PASS when
// auto mode deferred placement; --no-silk-place keeps the gate as it is.
func TestSilkDeferredGate(t *testing.T) {
	snap := silkTestBoard(part0603("R1", 1000, 1000))
	opt := defaultSilkTightOpts()
	b := pcbRect{MinX: 1090, MinY: 985, MaxX: 1150, MaxY: 1015}
	snap.Silk[0].BBox = &b
	clean := silkGate(snap, 45, opt)
	if !clean.Pass {
		t.Fatalf("the test needs clean labels: %+v", clean)
	}
	run := func(deferred string, g gateResult) (*kicadRun, *bytes.Buffer) {
		var stderr bytes.Buffer
		r := &kicadRun{stderr: &stderr, summary: map[string]any{}, reg: newGateRegistry(&stderr, nil), silkAutoDeferred: deferred}
		r.addTailGate(g)
		return r, &stderr
	}
	r, stderr := run("deferred: routing incomplete (1 unrouted connection(s))", clean)
	if statusOf(t, r.reg, "silkscreen") != gate.CannotCheck || r.reg.ExitCode() != 5 || r.gates[0].Pass || !r.gates[0].CannotCheck || !strings.Contains(stderr.String(), "CANNOT-CHECK") {
		t.Fatalf("%+v %s", r.reg.Records(), stderr)
	}
	if !strings.Contains(r.gates[0].Detail, "routing incomplete") || !strings.Contains(r.gates[0].Items[0], "--silk=on") {
		t.Fatalf("%+v", r.gates[0])
	}
	// Other tail gates are not touched by it.
	r, _ = run("deferred: routing incomplete", gateResult{Gate: "board-manual", Pass: true})
	if r.reg.ExitCode() != 0 {
		t.Fatalf("%+v", r.reg.Records())
	}
	// A user's skip (or a complete route) leaves the gate's own verdict.
	r, _ = run("", clean)
	if statusOf(t, r.reg, "silkscreen") != gate.Pass {
		t.Fatalf("%+v", r.reg.Records())
	}
}

// What the gate found wrong stays a failure under deferral; only the deferral
// itself is unchecked. A waiver of the deferral leaves the failures, and on clean
// labels passes the gate.
func TestSilkDeferredGateKeepsFailures(t *testing.T) {
	const reason = "deferred: routing incomplete (1 unrouted connection(s))"
	dirty := silkGate(silkTestBoard(part0603("R1", 1000, 1000)), 45, defaultSilkTightOpts()) // label far from its part
	if dirty.Pass || len(dirty.Items) == 0 {
		t.Fatalf("the test needs failing labels: %+v", dirty)
	}
	clean := gateResult{Gate: "silkscreen", Pass: true, Detail: "ok"}
	deferral := gateWaiver{Gate: "silkscreen", Match: "silk placement deferred", Reason: "reviewed", By: "test"}
	run := func(g gateResult, ws ...gateWaiver) (*kicadRun, gateResult) {
		var stderr bytes.Buffer
		r := &kicadRun{stderr: &stderr, summary: map[string]any{}, waivers: ws, reg: newGateRegistry(&stderr, ws), silkAutoDeferred: reason}
		r.addTailGate(g)
		return r, r.gates[0]
	}
	for _, c := range []struct {
		name   string
		g      gateResult
		ws     []gateWaiver
		status gate.Status
		exit   int
		items  int
	}{
		{"failures, no waiver", dirty, nil, gate.Fail, 6, len(dirty.Items) + 1},
		{"failures, deferral waived", dirty, []gateWaiver{deferral}, gate.Fail, 6, len(dirty.Items)},
		{"clean, no waiver", clean, nil, gate.CannotCheck, 5, 1},
		{"clean, deferral waived", clean, []gateWaiver{deferral}, gate.Pass, 0, 1},
	} {
		r, g := run(c.g, c.ws...)
		if got := statusOf(t, r.reg, "silkscreen"); got != c.status || r.reg.ExitCode() != c.exit {
			t.Errorf("%s: status %s exit %d %+v", c.name, got, r.reg.ExitCode(), g)
		}
		if c.status != gate.Pass && len(g.Items) != c.items {
			t.Errorf("%s: items %v", c.name, g.Items)
		}
		if c.status == gate.Fail && g.CannotCheck {
			t.Errorf("%s: %+v", c.name, g)
		}
	}
	// A failure with no items stays unwaivable.
	r, g := run(gateResult{Gate: "silkscreen", Detail: "broken"}, deferral)
	if statusOf(t, r.reg, "silkscreen") != gate.Fail || len(g.Items) != 0 {
		t.Fatalf("%+v", g)
	}
}

// fakeSilkTools answers silk placement's bridge calls; snap and edit, when set,
// run per call (n counts from 1).
type fakeSilkTools struct {
	raw           []byte
	snaps, edits  int
	snap          func(ctx context.Context, n int) error
	edit          func(ctx context.Context, n int) error
	lastDeadlined bool
}

func (f *fakeSilkTools) SnapshotContext(ctx context.Context, pcb string) ([]byte, error) {
	f.snaps++
	_, f.lastDeadlined = ctx.Deadline()
	if f.snap != nil {
		if err := f.snap(ctx, f.snaps); err != nil {
			return nil, err
		}
	}
	return f.raw, nil
}

func (f *fakeSilkTools) EditContext(ctx context.Context, pcb string, ops map[string]any, out string) (json.RawMessage, error) {
	f.edits++
	if f.edit != nil {
		if err := f.edit(ctx, f.edits); err != nil {
			return nil, err
		}
	}
	return json.RawMessage(`{}`), nil
}

func blockUntilDone(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// silkRun is a kicadRun whose board has one label far from its part, so the
// gate fails and the planner moves it.
func silkRun(t *testing.T) (*kicadRun, *fakeSilkTools) {
	t.Helper()
	raw, err := json.Marshal(silkTestBoard(part0603("R1", 1000, 1000)))
	if err != nil {
		t.Fatal(err)
	}
	return &kicadRun{o: kicadRouteOpts{silk: defaultSilkTightOpts()}, work: t.TempDir(), summary: map[string]any{}, stderr: io.Discard}, &fakeSilkTools{raw: raw}
}

func silkReportOf(t *testing.T, r *kicadRun) *silkTightReport {
	t.Helper()
	rep, ok := r.summary["silk"].(*silkTightReport)
	if !ok {
		t.Fatalf("summary.silk: %#v", r.summary["silk"])
	}
	return rep
}

// The budget ending in a later round's snapshot keeps the board of the round
// before, and says so.
func TestSilkBudgetInterruptsSnapshot(t *testing.T) {
	r, f := silkRun(t)
	f.snap = func(ctx context.Context, n int) error {
		if n == 2 {
			return blockUntilDone(ctx)
		}
		return nil
	}
	start := time.Now()
	board, err := r.silkPlaceWith(f, "in", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Minute {
		t.Fatalf("not interrupted: %s", d)
	}
	rep := silkReportOf(t, r)
	if board == "in" || !strings.Contains(board, "silk") || f.edits != 1 || rep.Moved != 1 || rep.Rounds != 1 {
		t.Fatalf("board %s edits %d %+v", board, f.edits, rep)
	}
	if rep.Budget == nil || !rep.Budget.Expired || rep.Budget.StoppedAt != "round 2: snapshot" || rep.Budget.BudgetS != 2 {
		t.Fatalf("%+v", rep.Budget)
	}
}

// The budget ending inside an edit keeps the board from before it, and the
// report does not claim that round's moves.
func TestSilkBudgetInterruptsEdit(t *testing.T) {
	r, f := silkRun(t)
	f.edit = func(ctx context.Context, n int) error { return blockUntilDone(ctx) }
	board, err := r.silkPlaceWith(f, "in", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	rep := silkReportOf(t, r)
	if board != "in" || rep.Moved != 0 || rep.Rounds != 0 || len(rep.Placed) != 0 || rep.Budget == nil || rep.Budget.StoppedAt != "round 1: edit" {
		t.Fatalf("board %s %+v %+v", board, rep, rep.Budget)
	}
}

// A call that does not honour the deadline: its completed board is kept, the
// loop stops and the overrun is reported.
func TestSilkBudgetReportsOverrun(t *testing.T) {
	r, f := silkRun(t)
	const budget = 2 * time.Second
	f.edit = func(ctx context.Context, n int) error {
		time.Sleep(budget + 500*time.Millisecond)
		return nil
	}
	board, err := r.silkPlaceWith(f, "in", budget)
	if err != nil {
		t.Fatal(err)
	}
	rep := silkReportOf(t, r)
	if board == "in" || f.snaps != 1 || rep.Moved != 1 {
		t.Fatalf("board %s snaps %d %+v", board, f.snaps, rep)
	}
	b := rep.Budget
	if b == nil || !b.Expired || b.StoppedAt != "before round 2" || b.OverrunS <= 0 || b.ElapsedS < b.BudgetS {
		t.Fatalf("%+v", b)
	}
}

// Without a budget nothing changes: no deadline, no report, a tool error is an
// error. An error that is not the budget's stays an error under a budget too.
func TestSilkNoBudgetAndRealErrors(t *testing.T) {
	r, f := silkRun(t)
	f.edit = func(context.Context, int) error { return errors.New("bridge edit rejected") }
	if _, err := r.silkPlaceWith(f, "in", 0); err == nil || f.lastDeadlined {
		t.Fatalf("%v deadline %v", err, f.lastDeadlined)
	}
	r, f = silkRun(t)
	if _, err := r.silkPlaceWith(f, "in", 0); err != nil {
		t.Fatal(err)
	}
	if silkReportOf(t, r).Budget != nil {
		t.Fatalf("%+v", silkReportOf(t, r).Budget)
	}
	r, f = silkRun(t)
	f.snap = func(context.Context, int) error { return errors.New("pcbnew crashed") }
	if _, err := r.silkPlaceWith(f, "in", time.Hour); err == nil {
		t.Fatal("no error")
	}
}

// A budget spent at once writes nothing (where it stops depends on the timer),
// and the planners stop at their deadline instead of finishing the board.
func TestSilkBudgetSpentAndPlannerDeadline(t *testing.T) {
	r, f := silkRun(t)
	board, err := r.silkPlaceWith(f, "in", time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if b := silkReportOf(t, r).Budget; board != "in" || f.edits != 0 || !b.Expired || b.StoppedAt == "" {
		t.Fatalf("%s %d %+v", board, f.edits, b)
	}
	snap := silkTestBoard(part0603("R1", 1000, 1000), part0603("R2", 1300, 1000))
	opt := defaultSilkTightOpts()
	labels, sc, _ := silkTightInput(snap, opt)
	if placed, _ := planSilkTight(labels, sc, opt); len(placed) != 2 {
		t.Fatalf("no deadline: %+v", placed)
	}
	opt.deadline = time.Now().Add(-time.Second)
	if placed, _ := planSilkTight(labels, sc, opt); len(placed) != 0 {
		t.Fatalf("past the deadline: %+v", placed)
	}
}

func TestSilkTimeBudgetRejectsNegative(t *testing.T) {
	cmd := newKicadRouteCmd(io.Discard, io.Discard)
	cmd.SetArgs([]string{"--pcb", "x.kicad_pcb", "--intent", "i.json", "--silk-time-budget", "-1s"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "--silk-time-budget") {
		t.Fatalf("%v", err)
	}
}

// The whole post-route path on real KiCad: a partial result with a count of 0
// defers silk under auto mode, the silkscreen gate is cannot-check, and the
// cannot-check router-vias and via-audit results keep their status through
// qualityGates. Needs PCBPILOT_KICAD_LIVE=1.
func TestKicadRoutePostRouteCannotCheckLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	pcb := filepath.Join(dir, "tiny-route.kicad_pcb")
	if _, err := copyKicadBoard(filepath.Join("..", "kicad", "testdata", "tiny-route.kicad_pcb"), pcb); err != nil {
		t.Fatal(err)
	}
	// A custom rule on via size with a condition cannot be resolved per via.
	if err := os.WriteFile(boardSibling(pcb, ".kicad_dru"), []byte("(version 1)\n(rule r (condition \"A.NetClass == 'x'\") (constraint via_diameter (min 0.5mm)))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{"VCC":{"role":"power","widthMil":{"outer":20,"inner":20,"min":20},"clearanceMil":8},
	"GND":{"role":"ground","widthMil":{"outer":16,"inner":16,"min":10},"clearanceMil":8},
	"SIG_A":{"role":"signal","widthMil":{"outer":8,"inner":8,"min":6},"clearanceMil":6}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := loadDesignIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	run := func(mode string, noSilk bool) *gateRegistry {
		out := filepath.Join(t.TempDir(), "route")
		work := filepath.Join(out, "work")
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		waivers := []gateWaiver{{Gate: "board-manual", Match: "--no-manual", Reason: "live unit test", By: "test"}}
		var stderr bytes.Buffer
		o := kicadRouteOpts{pcb: pcb, intent: intent, outDir: out, widthBasis: "net", pours: "off", gndNet: "GND", powerLayer: -1, silk: defaultSilkTightOpts(),
			noManual: true, projectName: "tiny", projectConfig: "none", widenMax: 40, silkMode: mode, noSilkPlace: noSilk}
		r := &kicadRun{o: o, in: in, kt: kt, work: work, waivers: waivers, stderr: &stderr, summary: map[string]any{}, reg: newGateRegistry(&stderr, waivers),
			route: &routeResult{Router: "fastroute", Partial: true, Gates: []gateResult{{Gate: "router-vias", Detail: "cannot check: test", CannotCheck: true}}}}
		s, _, err := r.snapshot(pcb)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := r.postRoute(pcb, s); err != nil {
			t.Fatalf("%v\n%s", err, stderr.String())
		}
		t.Log(stderr.String())
		return r.reg
	}
	reg := run("auto", false)
	for _, id := range []string{"router-vias", "via-audit"} {
		if got := statusOf(t, reg, id); got != gate.CannotCheck {
			t.Errorf("%s: %s", id, got)
		}
	}
	// The fixture's labels fail the gate: that stays a failure, with the deferral beside it.
	if rec := recordOf(t, reg, "silkscreen"); rec.Status != gate.Fail || len(rec.Unchecked) != 1 || !strings.Contains(rec.Reason, "routing incomplete") {
		t.Errorf("silkscreen: %+v", rec)
	}
	reg = run("auto", true)
	if rec := recordOf(t, reg, "silkscreen"); rec.Status == gate.CannotCheck || len(rec.Unchecked) != 0 {
		t.Errorf("--no-silk-place: silkscreen %+v", rec)
	}
}
