package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseFreeroutingLog(t *testing.T) {
	log := `2026-11-11 14:14:34.182 INFO   Freerouting v2.5.0 (build-date: 2026-10-05)
INFO   [A\B] Auto-routing pass #1 on board 'x' was completed in 1.97 seconds with score 497.89 (3 unrouted and 0 violations), using 1.50 CPU seconds.
INFO   [A\B] Auto-routing stage completed: started with 24 unrouted nets, completed in 19.27 seconds, final score: 497.89 (9 unrouted and 7 violations), using 11.00 total CPU seconds.
INFO   [A\B] Skipping optimization stage: the board has 9 unrouted connection(s) (optimizer only runs on completely routed boards).
`
	u, v, ver, ok := parseFreeroutingLog(log)
	if !ok || u != 9 || v != 7 || ver != "2.5.0" {
		t.Fatalf("%d %d %q %v", u, v, ver, ok)
	}
	// the optimizer's summary is the final one when it ran
	u, v, _, ok = parseFreeroutingLog(log + "INFO   Optimization was completed in 0.04 seconds with the score of 990.00 (0 unrouted and 1 violation).\n")
	if !ok || u != 0 || v != 1 {
		t.Fatalf("%d %d %v", u, v, ok)
	}
	// a per-pass line alone is not a final count
	if _, _, _, ok := parseFreeroutingLog("Auto-routing pass #1 on board 'x' was completed in 1 seconds with score 1 (0 unrouted and 0 violations)"); ok {
		t.Fatal("a pass line must not count as the final result")
	}
}

func fakeFreerouting(t *testing.T, behaviour string) string {
	t.Helper()
	bin := fakeRouterBin(t, "freerouting")
	t.Setenv(envFakeFreeBehaves, behaviour)
	return bin
}

func TestRunFreerouting(t *testing.T) {
	dir := t.TempDir()
	dsn, ses, log := filepath.Join(dir, "in.dsn"), filepath.Join(dir, "out.ses"), filepath.Join(dir, "fr.log")
	if err := os.WriteFile(dsn, []byte("(pcb x)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(behaviour string) (*routeResult, error) {
		return runFreerouting(context.Background(), freeroutingOpts{bin: fakeFreerouting(t, behaviour), passes: 7, threads: 2}, dsn, ses, log, io.Discard)
	}
	res, err := run("ok")
	if err != nil {
		t.Fatal(err)
	}
	if res.Router != "freerouting" || res.UnroutedCount != 0 || res.Violations != 1 || res.Version != "9.9.9" || res.Session != ses || res.Partial || res.PatchSHA == "" || res.Report != log {
		t.Fatalf("%+v", res)
	}
	joined := strings.Join(res.Args, " ")
	for _, want := range []string{"-de " + dsn, "-do " + ses, "--gui.enabled=false", "-da", "--router.autorouter.max_passes=7", "-mt 2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q lack %q", joined, want)
		}
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "Optimization was completed") {
		t.Fatalf("log not kept: %q", b)
	}
	// unrouted connections: an incomplete result, not an error
	if res, err := run("incomplete"); err != nil || res.UnroutedCount != 9 || res.Violations != 7 || res.Partial {
		t.Fatalf("%v %+v", err, res)
	}
	// a session whose counts the log does not state is never a complete result
	if res, err := run("nocount"); err != nil || !res.Partial || res.UnroutedCount != -1 {
		t.Fatalf("%v %+v", err, res)
	}
	if ok, _ := quickCheck(&routeResult{Partial: true, UnroutedCount: -1}, 0); ok {
		t.Fatal("an uncounted result must fail the quick check")
	}
	// exit 0 without a session, and a crash, are errors; the stale session of
	// the run before must not stand in
	for _, b := range []string{"noses", "crash"} {
		if _, err := run(b); err == nil {
			t.Fatalf("%s must fail", b)
		}
		if _, err := os.Stat(ses); err == nil {
			t.Fatalf("%s: stale session left behind", b)
		}
	}
}

func TestRunFreeroutingCancelAndLimit(t *testing.T) {
	dir := t.TempDir()
	bin := fakeFreerouting(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := runFreerouting(ctx, freeroutingOpts{bin: bin}, filepath.Join(dir, "i"), filepath.Join(dir, "o"), filepath.Join(dir, "l"), io.Discard)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 15*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
	start = time.Now()
	_, err = runFreerouting(context.Background(), freeroutingOpts{bin: bin, timeout: 300 * time.Millisecond}, filepath.Join(dir, "i"), filepath.Join(dir, "o"), filepath.Join(dir, "l"), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--freerouting-time") || time.Since(start) > 15*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

func TestResolveFreerouting(t *testing.T) {
	home := fakeHome(t)
	t.Setenv("FREEROUTING_BIN", "")
	t.Setenv("PATH", t.TempDir())
	if _, err := resolveFreerouting(""); err == nil || !strings.Contains(err.Error(), "freerouting not found") {
		t.Fatalf("err %v", err)
	}
	dir := filepath.Join(home, ".pcbpilot", "freerouting", "2.5.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := "freerouting"
	if runtime.GOOS == "windows" {
		exe = "freerouting.exe"
	}
	bin := filepath.Join(dir, exe)
	_ = os.WriteFile(bin, []byte("x"), 0o755)
	linkCurrent(t, home, "freerouting", dir)
	got, err := resolveFreerouting("")
	if err != nil || !sameFile(t, got, bin) {
		t.Fatalf("got %q err %v want %q", got, err, bin)
	}
}
