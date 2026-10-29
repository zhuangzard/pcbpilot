package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

func TestFastrouteArgs(t *testing.T) {
	o := fastrouteOpts{multiStart: 8, minTraceUm: 152, maxTime: 10 * time.Minute}
	got := strings.Join(fastrouteArgs(o, "b.dsn", "b.r1.ses", "b.r1-report.json", "b.r0.ses"), " ")
	want := "-de b.dsn -do b.r1.ses --report=b.r1-report.json --diagnose --multi-start=8 --router.min_trace_width_um=152 --max-time=600 --initial-session=b.r0.ses"
	if got != want {
		t.Fatalf("args\n got %s\nwant %s", got, want)
	}
	if got := strings.Join(fastrouteArgs(fastrouteOpts{}, "b.dsn", "b.ses", "r.json", ""), " "); got != "-de b.dsn -do b.ses --report=r.json --diagnose" {
		t.Fatalf("minimal args = %s", got)
	}
	// Single-threaded (the default): no parallel multi-start either.
	if got := strings.Join(fastrouteArgs(fastrouteOpts{threads: 1}, "b.dsn", "b.ses", "r.json", ""), " "); !strings.Contains(got, "--multi-start=1 ") || !strings.Contains(got, "--router.autorouter.max_threads=1 --router.optimizer.max_threads=1") {
		t.Fatalf("single-threaded args = %s", got)
	}
}

func TestReadFastrouteReport(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.json")
	// Shape of a fastroute 0.1.7 --report file (trimmed).
	if err := os.WriteFile(p, []byte(`{"fastroute":"0.1.7","stats":{"layers":4,"connections":462,"unrouted":3,"violations":2},"unrouted":[],
"clearance_violations":[
 {"layer":"TopLayer","xy":[1.8334,-1.1437],"clearance_mm":0.006,"actual_mm":0.003,"unfixable":false,"first":{"kind":"trace","net":"GND"},"second":{"kind":"pin","component":"u1"}},
 {"layer":"TopLayer","xy":[0.1575,-2.9921],"unfixable":true,"first":{"kind":"pin"},"second":{"kind":"pin"}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := readFastrouteReport(p, specctra.ReportMilPerUnit("(PCB x (resolution mil 10) (unit mil)"))
	if err != nil || r.Unrouted != 3 || r.Violations != 2 || r.Fixable != 1 || r.FixableList[0] != "TopLayer at (1833.4, 1143.7) mil: trace GND / pin " {
		t.Fatalf("got %+v %v", r, err)
	}
	// A KiCad DSN is in um: the same report numbers are mm (Board-B V5 A:
	// C78.2 at (20.32, -33.336) = board (800, 1312.44) mil).
	r, err = readFastrouteReport(p, specctra.ReportMilPerUnit("(pcb x (parser) (resolution um 10) (unit um)"))
	if err != nil || r.FixableList[0] != "TopLayer at (72.2, 45.0) mil: trace GND / pin " {
		t.Fatalf("KiCad units: got %+v %v", r.FixableList, err)
	}
	if err := os.WriteFile(p, []byte(`{"stats":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readFastrouteReport(p, 1000); err == nil {
		t.Fatal("report without stats.unrouted accepted")
	}
}

func TestResolveFastroute(t *testing.T) {
	dir := t.TempDir()
	exe := "fastroute"
	if runtime.GOOS == "windows" {
		exe = "fastroute.exe" // PATH lookup goes by PATHEXT; it is not run here
	}
	bin := filepath.Join(dir, exe)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("FASTROUTE_BIN", "")
	saved := fastrouteLookPath
	fastrouteLookPath = exec.LookPath
	defer func() { fastrouteLookPath = saved }()
	if got, err := resolveFastroute(""); err != nil || got != bin {
		t.Fatalf("PATH lookup = %q %v", got, err)
	}
	if _, err := resolveFastroute(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing explicit binary accepted")
	}
	t.Setenv("PATH", t.TempDir())
	_, err := resolveFastroute("")
	if err == nil || !strings.Contains(err.Error(), "install-fastroute.sh") || !strings.Contains(err.Error(), "never downloads") {
		t.Fatalf("not-installed error = %v", err)
	}
}

// fakeHome points os.UserHomeDir at a temp directory on every OS.
func fakeHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func writeExe(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// linkCurrent makes <home>/.pcbpilot/<tool>/current point at target (a
// symlink, as the installers do; skipped where symlinks need privileges).
func linkCurrent(t *testing.T, home, tool, target string) {
	t.Helper()
	cur := filepath.Join(home, ".pcbpilot", tool, "current")
	if err := os.MkdirAll(filepath.Dir(cur), 0o755); err != nil {
		t.Fatal(err)
	}
	err := os.Symlink(target, cur)
	if err != nil && runtime.GOOS == "windows" {
		// Unprivileged Windows: a directory junction is what a user gets
		// from `mklink /J`, and EvalSymlinks follows it the same way.
		if st, serr := os.Stat(target); serr == nil && st.IsDir() {
			sh := os.Getenv("ComSpec")
			if sh == "" {
				sh = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
			}
			err = exec.Command(sh, "/c", "mklink", "/J", cur, target).Run()
		}
	}
	if err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// sameFile: a and b name one file (through links or junctions).
func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	sa, err1 := os.Stat(a)
	sb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(sa, sb)
}

// The Windows install: current -> 0.1.14, the release zip unpacked beside
// it. resolveFastroute must return the executable inside, not the directory.
func TestResolveFastrouteInstallTree(t *testing.T) {
	home := fakeHome(t)
	t.Setenv("FASTROUTE_BIN", "")
	saved := fastrouteLookPath
	fastrouteLookPath = func(string) (string, error) { return "", errors.New("no PATH lookup in this test") }
	defer func() { fastrouteLookPath = saved }()
	exe := "fastroute"
	if runtime.GOOS == "windows" {
		exe = "fastroute.exe"
	}
	ver := filepath.Join(home, ".pcbpilot", "fastroute", "0.1.14")
	bin := filepath.Join(ver, "fastroute-0.1.14-windows-x64", exe)
	writeExe(t, bin)
	for _, f := range []string{"LICENSE", "NOTICE", "SHA256SUMS.txt", "fastroute-0.1.14-windows-x64.zip"} {
		_ = os.WriteFile(filepath.Join(ver, f), []byte("x"), 0o644)
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(bin), "LICENSE"), []byte("x"), 0o644)
	linkCurrent(t, home, "fastroute", ver)
	if got, err := resolveFastroute(""); err != nil || !sameFile(t, got, bin) {
		t.Fatalf("current -> version dir: got %q %v, want %q", got, err, bin)
	}
	// An explicit directory resolves the same way.
	if got, err := resolveFastroute(ver); err != nil || filepath.Base(got) != exe {
		t.Fatalf("explicit dir: got %q %v", got, err)
	}
	// current -> the binary itself (scripts/fastroute-upgrade-check.sh).
	if err := os.Remove(filepath.Join(home, ".pcbpilot", "fastroute", "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bin, filepath.Join(home, ".pcbpilot", "fastroute", "current")); err != nil {
		t.Logf("current -> binary not checked: %v", err) // file links need privileges on Windows
		return
	}
	if got, err := resolveFastroute(""); err != nil || filepath.Base(got) != exe {
		t.Fatalf("current -> binary: got %q %v", got, err)
	}
}

// A current directory with no executable fails at resolve time (kicad route
// setup), naming the directory, instead of handing a directory to exec.
func TestResolveFastrouteEmptyInstallDir(t *testing.T) {
	home := fakeHome(t)
	t.Setenv("FASTROUTE_BIN", "")
	saved := fastrouteLookPath
	fastrouteLookPath = func(string) (string, error) { return "", errors.New("no PATH lookup in this test") }
	defer func() { fastrouteLookPath = saved }()
	ver := filepath.Join(home, ".pcbpilot", "fastroute", "0.1.14")
	if err := os.MkdirAll(filepath.Join(ver, "fastroute-0.1.14-windows-x64"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(ver, "fastroute-0.1.14-windows-x64.zip"), []byte("x"), 0o644)
	linkCurrent(t, home, "fastroute", ver)
	_, err := resolveFastroute("")
	if err == nil || !strings.Contains(err.Error(), "without a fastroute") || !strings.Contains(err.Error(), "--fastroute-bin") {
		t.Fatalf("empty install dir: err %v", err)
	}
	// Two unpacked releases in one directory: ambiguous, never a guess.
	exe := "fastroute"
	if runtime.GOOS == "windows" {
		exe = "fastroute.exe"
	}
	writeExe(t, filepath.Join(ver, "a", exe))
	writeExe(t, filepath.Join(ver, "b", exe))
	if _, err := resolveFastroute(""); err == nil || !strings.Contains(err.Error(), "2 executables") {
		t.Fatalf("ambiguous install dir: err %v", err)
	}
}

// A direct file must be executable: no x bit on Unix, no .exe/.com/.bat/.cmd
// on Windows (a LICENSE or a .zip handed to --fastroute-bin) is refused.
func TestResolveFastrouteRefusesNonExecutableFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fastroute.zip")
	if err := os.WriteFile(p, []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveFastroute(p); err == nil || !strings.Contains(err.Error(), "not an executable") {
		t.Fatalf("non-executable file: err %v", err)
	}
}

// cmdExe is a real Windows program to launch in tests (copied under another
// name), or a skip.
func cmdExe(t *testing.T) []byte {
	t.Helper()
	sh := os.Getenv("ComSpec")
	if sh == "" {
		sh = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	b, err := os.ReadFile(sh)
	if err != nil {
		t.Skipf("no cmd.exe to copy: %v", err)
	}
	return b
}

// Windows: a .bat / .cmd cannot be started by exec.Command without cmd.exe
// (and its quoting), so it is refused; a directory entry is matched without
// regard to case (FASTROUTE.EXE) and the resolved file really launches.
func TestResolveFastrouteWindowsExecutables(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows executable rules")
	}
	dir := t.TempDir()
	for _, n := range []string{"fastroute.bat", "fastroute.cmd"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("@echo off\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := resolveFastroute(p); err == nil || !strings.Contains(err.Error(), "not an executable") || !strings.Contains(err.Error(), ".bat / .cmd") {
			t.Fatalf("%s accepted: %v", n, err)
		}
	}
	// A directory holding only the wrapper has no executable.
	if _, err := resolveFastroute(dir); err == nil || !strings.Contains(err.Error(), "without a fastroute") {
		t.Fatalf("wrapper-only dir: %v", err)
	}
	rel := filepath.Join(dir, "fastroute-0.1.14-windows-x64")
	if err := os.MkdirAll(rel, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rel, "FASTROUTE.EXE"), cmdExe(t), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := resolveFastroute(dir)
	if err != nil || !strings.EqualFold(filepath.Base(got), "fastroute.exe") || !filepath.IsAbs(got) {
		t.Fatalf("upper-case exe: got %q %v", got, err)
	}
	if out, err := exec.Command(got, "/c", "exit 0").CombinedOutput(); err != nil {
		t.Fatalf("launch %s: %v\n%s", got, err, out)
	}
}

// A relative --fastroute-bin resolves to an absolute path, so exec runs
// that file: neither exec.ErrDot nor a same-named binary earlier on PATH.
func TestResolveFastrouteRelativeExplicitIsAbsolute(t *testing.T) {
	work, other := t.TempDir(), t.TempDir()
	name, args := "fastroute", []string{}
	if runtime.GOOS == "windows" {
		// A real program to launch: a copy of cmd.exe named fastroute.exe.
		b := cmdExe(t)
		name, args = "fastroute.exe", []string{"/c", "echo %CD%"}
		for _, d := range []string{work, other} {
			if err := os.WriteFile(filepath.Join(d, name), b, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	} else {
		for _, d := range []string{work, other} {
			if err := os.WriteFile(filepath.Join(d, name), []byte("#!/bin/sh\necho "+d+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Chdir(work)
	t.Setenv("PATH", other) // a conflicting fastroute first on PATH
	got, err := resolveFastroute(name)
	want := filepath.Join(work, name)
	if err != nil || !filepath.IsAbs(got) || !sameFile(t, got, want) {
		t.Fatalf("relative explicit: got %q %v, want %q", got, err, want)
	}
	cmd := exec.Command(got, args...)
	cmd.Dir = t.TempDir() // launching must not depend on the current directory
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("launch %s: %v\n%s", got, err, out)
	}
}

func TestResolveTracemakerInstallTree(t *testing.T) {
	home := fakeHome(t)
	t.Setenv("TRACEMAKER_BIN", "")
	t.Setenv("PATH", t.TempDir())
	exe := "tracemaker"
	if runtime.GOOS == "windows" {
		exe = "tracemaker.exe"
	}
	ver := filepath.Join(home, ".pcbpilot", "tracemaker", "pcbpilot-abc")
	bin := filepath.Join(ver, "tracemaker-windows-x64", exe)
	writeExe(t, bin)
	linkCurrent(t, home, "tracemaker", ver)
	if got, err := resolveTracemaker(""); err != nil || !sameFile(t, got, bin) {
		t.Fatalf("got %q %v, want %q", got, err, bin)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveTracemaker(""); err == nil || !strings.Contains(err.Error(), "without a tracemaker") {
		t.Fatalf("empty install dir: err %v", err)
	}
}

func TestDsnFixFlagsEscapes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "esc.json")
	if err := os.WriteFile(p, []byte(`[{"net":"GND","layer":"TopLayer","widthMil":10,"path":[[4776.1,313.9],[4710.65,313.9]],"via":true}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := dsnFixFlags{edgeOuterMil: 20, edgeInnerMil: 30, planeNet: "GND", escapesFile: p}
	opt, err := f.options()
	if err != nil {
		t.Fatal(err)
	}
	if opt.PlaneNet != "" {
		t.Error("plane set without --gnd-plane")
	}
	if len(opt.Escapes) != 1 || !opt.Escapes[0].Via || opt.Escapes[0].Path[1][0] != 4710.65 {
		t.Fatalf("escapes = %+v", opt.Escapes)
	}
	// Short form recorded on the Board-B v6A3 run.
	if err := os.WriteFile(p, []byte(`[[[2399.6, 1427.2], [2334.15, 1427.2]], [[1537.4, 1801.2], [1602.85, 1801.2]]]`), 0o644); err != nil {
		t.Fatal(err)
	}
	opt, err = f.options()
	if err != nil || len(opt.Escapes) != 2 || opt.Escapes[1].Net != "GND" || opt.Escapes[1].Layer != "TopLayer" || !opt.Escapes[1].Via || opt.Escapes[1].Path[1][0] != 1602.85 {
		t.Fatalf("short-form escapes = %+v %v", opt.Escapes, err)
	}
	f.gndPlane = true
	if opt, _ := f.options(); opt.PlaneNet != "GND" {
		t.Error("--gnd-plane did not set the plane net")
	}
}

func TestMainPowerRailAndPourLayers(t *testing.T) {
	pads := []pcbPadP{{Net: "GND"}, {Net: "GND"}, {Net: "GND"}, {Net: "+12V"}, {Net: "+12V"}, {Net: "+3V3"}, {Net: "SIG"}, {Net: "SIG"}, {Net: "SIG"}}
	if got := mainPowerRail(pads); got != "+12V" {
		t.Fatalf("main rail = %q, want +12V", got)
	}
	if got := mainPowerRail([]pcbPadP{{Net: "GND"}, {Net: "SIG"}}); got != "" {
		t.Fatalf("no rail = %q", got)
	}
	if g, p := defaultPourLayers(4); len(g) != 3 || g[0] != 1 || g[1] != 15 || g[2] != 2 || p != 16 {
		t.Fatalf("4-layer pours = %v %d", g, p)
	}
	if g, p := defaultPourLayers(2); len(g) != 2 || p != 0 {
		t.Fatalf("2-layer pours = %v %d", g, p)
	}
}

func TestRunImproved(t *testing.T) {
	a := fastrouteRun{Unrouted: 2, Fixable: 3}
	for _, c := range []struct {
		b    fastrouteRun
		want bool
	}{{fastrouteRun{Unrouted: 1, Fixable: 9}, true}, {fastrouteRun{Unrouted: 2, Fixable: 2}, true}, {fastrouteRun{Unrouted: 2, Fixable: 3}, false}, {fastrouteRun{Unrouted: 3}, false}} {
		if got := runImproved(a, c.b); got != c.want {
			t.Errorf("runImproved(%+v, %+v) = %v", a, c.b, got)
		}
	}
}

func TestFastrouteArgsNoNeckdown(t *testing.T) {
	got := strings.Join(fastrouteArgs(fastrouteOpts{noNeckdown: []string{"+12V", "SW5"}}, "b.dsn", "b.ses", "r.json", ""), " ")
	if !strings.Contains(got, "--no-neckdown-classes=+12V,SW5") {
		t.Fatalf("args = %s", got)
	}
}

// The pre-route gate: intent nets become DSN requirements, the DSN is raised
// to them and re-checked; a DSN that cannot carry them stops routing.
func TestPrepareDSNGate(t *testing.T) {
	raw, err := os.ReadFile("../pcb/specctra/testdata/easyeda-export.dsn")
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseDesignIntent([]byte(`{"schemaVersion":1,"nets":{
 "+12V":{"role":"power","widthMil":{"outer":21.65,"inner":41.34,"min":21.65},"clearanceMil":6,"viasPerTransition":3},
 "GND":{"role":"ground","widthMil":{"outer":21.65,"inner":43.31,"min":10},"clearanceMil":6},
 "SIG":{"role":"signal","widthMil":{"outer":6,"min":6},"clearanceMil":6}}}`))
	if err != nil {
		t.Fatal(err)
	}
	reqs := intentRequirements(in)
	if reqs["SIG"].InnerMil != 6 || reqs["+12V"].InnerMil != 41.34 {
		t.Fatalf("requirements = %+v", reqs)
	}
	text, _, rq, err := prepareDSN(string(raw), specctra.FixOptions{}, reqs)
	if err != nil {
		t.Fatal(err)
	}
	if short, _ := specctra.CheckNetRequirements(text, reqs); len(short) != 0 {
		t.Fatalf("prepared DSN still short: %v", short)
	}
	if rq.MinTraceMil != 6 || strings.Join(rq.NoNeckdown, ",") != "+12V,GND" { // SIG sits at the 6 mil floor
		t.Fatalf("requirement report = %+v", rq)
	}
	// A DSN not in mil cannot be checked: the gate refuses rather than guess.
	notMil := strings.Replace(string(raw), "(resolution mil 1000)", "(resolution um 10)", 1)
	if _, _, _, err := prepareDSN(notMil, specctra.FixOptions{}, reqs); err == nil || !strings.Contains(err.Error(), "pre-route gate") {
		t.Fatalf("non-mil DSN passed the gate: %v", err)
	}
}

// fastroute 0.1.7 can panic in its parallel autorouter after writing a
// checkpoint session but no report (Board-B v9). The run must be recorded
// as crashed with unknown counts and repeated once single-threaded.
func TestRunFastrouteCrashRetry(t *testing.T) {
	dir := t.TempDir()
	bin := fakeRouterBin(t, "fastroute") // fake_router_test.go
	t.Setenv(envFakeFRMode, "crash-parallel")
	var log strings.Builder
	ses, runs, err := runFastroute(fastrouteOpts{bin: bin, multiStart: 8, timeout: time.Minute}, "b.dsn", filepath.Join(dir, "b"), &log)
	if err != nil {
		t.Fatalf("err = %v\n%s", err, log.String())
	}
	if len(runs) != 2 || runs[0].Status != "crashed" || runs[0].Unrouted != -1 || runs[0].Violations != -1 {
		t.Fatalf("crashed run not recorded as unknown: %+v", runs)
	}
	if !runs[1].Retry || runs[1].Status != "ok" || runs[1].Unrouted != 0 || ses != runs[1].Session {
		t.Fatalf("single-threaded retry = %+v (ses %s)", runs[1], ses)
	}
	if !strings.Contains(log.String(), "retrying single-threaded") {
		t.Fatalf("log: %s", log.String())
	}

	// A binary that always crashes: round 0 has no good result -> error.
	t.Setenv(envFakeFRMode, "crash")
	if _, runs, err := runFastroute(fastrouteOpts{bin: bin, timeout: time.Minute}, "b.dsn", filepath.Join(dir, "c"), io.Discard); err == nil || runs[len(runs)-1].Unrouted != -1 {
		t.Fatalf("always-crashing router accepted: %+v %v", runs, err)
	}
}

// A run that reaches its timeout is asked to stop the way fastroute
// documents (SIGINT on Unix, Ctrl+Break on Windows — never a kill first), so
// it writes its best board and the round is a usable best-so-far result,
// well before the 30 s WaitDelay kill.
func TestRunFastrouteTimeoutStopsGracefully(t *testing.T) {
	// Skipped only when Windows positively has no console to send Ctrl+Break
	// through (GetConsoleProcessList); any other failure to stop is a failure.
	if ok, why := ctrlBreakDeliverable(); !ok {
		t.Skipf("Ctrl+Break cannot be delivered: %s", why)
	}
	dir := t.TempDir()
	bin := fakeRouterBin(t, "fastroute")
	t.Setenv(envFakeFRMode, "wait-stop")
	var log strings.Builder
	start := time.Now()
	ses, runs, err := runFastroute(fastrouteOpts{bin: bin, timeout: 2 * time.Second}, "b.dsn", filepath.Join(dir, "b"), &log)
	if err != nil || len(runs) == 0 || runs[0].Status != "ok" || runs[0].Unrouted != 0 || ses != runs[0].Session {
		t.Fatalf("timed-out run did not stop gracefully: %+v err %v\n%s", runs, err, log.String())
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("graceful stop took %s (killed after WaitDelay?)", d)
	}
}

// LVDS-style constraints from the intent reach fastroute: pairs with their
// gap, a skew group per pair, and the declared length groups.
func TestIntentPairsAndTune(t *testing.T) {
	in, err := parseDesignIntent([]byte(`{"schemaVersion":1,"nets":{
 "LVDS_CLK_P":{"role":"signal","widthMil":{"outer":5,"min":5},"diffPair":"LVDS_CLK_N","pairGapMil":5,"maxSkewMil":5,"lengthGroup":"LVDS"},
 "LVDS_CLK_N":{"role":"signal","widthMil":{"outer":5,"min":5},"diffPair":"LVDS_CLK_P","pairGapMil":5,"maxSkewMil":5,"lengthGroup":"LVDS"},
 "LVDS_D0_P":{"role":"signal","widthMil":{"outer":5,"min":5},"lengthGroup":"LVDS","lengthTolMil":20},
 "SIG":{"role":"signal","widthMil":{"outer":6,"min":6}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	pairs, tune := intentPairsAndTune(in, nil)
	if pairs != "pair LVDS_CLK_N LVDS_CLK_P gap=0.127 skew=0.127\n" {
		t.Fatalf("pairs = %q", pairs)
	}
	for _, want := range []string{"group LVDS tolerance=0.508\n", "  LVDS_D0_P\n", "group pair_LVDS_CLK_N_LVDS_CLK_P tolerance=0.127\n"} {
		if !strings.Contains(tune, want) {
			t.Fatalf("tune lacks %q:\n%s", want, tune)
		}
	}
	if !strings.Contains(strings.Join(fastrouteArgs(fastrouteOpts{pairsFile: "p.txt", tuneFile: "t.txt"}, "b.dsn", "b.ses", "r.json", ""), " "), "--pairs=p.txt --tune=t.txt") {
		t.Fatal("pairs/tune not passed to fastroute")
	}
}

// fakeFastroute writes a session and a report with unrouted connections,
// the first blocked of them classed "blocked" by --diagnose; it logs one
// line per call to calls. The fake is this test binary (fake_router_test.go);
// its behaviour holds until the next fakeFastroute call or the test's end.
func fakeFastroute(t *testing.T, unrouted, blocked int) (bin, calls string) {
	t.Helper()
	dir := t.TempDir()
	bin, calls = fakeRouterBin(t, "fastroute"), filepath.Join(dir, "calls")
	var items []string
	for i := 0; i < unrouted; i++ {
		cls := "congestion"
		if i < blocked {
			cls = "blocked"
		}
		items = append(items, fmt.Sprintf(`{"net":"N%d","from":{"component":"U1","pin":"%d"},"to":{"component":"R1","pin":"1"},"diagnosis":{"class":"%s"}}`, i, i+1, cls))
	}
	rep := fmt.Sprintf(`{"stats":{"unrouted":%d,"violations":0},"unrouted":[%s]}`, unrouted, strings.Join(items, ","))
	t.Setenv(envFakeCalls, calls)
	t.Setenv(envFakeReport, rep)
	return bin, calls
}

// --diagnose: when every unrouted connection is blocked by geometry no
// continuation run is made (it cannot help); congestion keeps continuing.
func TestRunFastrouteStopsOnBlocked(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "b.dsn")
	_ = os.WriteFile(dsn, []byte("(pcb x (resolution um 10))"), 0o644)
	for _, c := range []struct{ unrouted, blocked, wantCalls int }{{2, 2, 1}, {2, 1, 2}} {
		bin, calls := fakeFastroute(t, c.unrouted, c.blocked)
		_, runs, err := runFastroute(fastrouteOpts{bin: bin, threads: 1, rounds: 3, timeout: time.Minute}, dsn, filepath.Join(dir, fmt.Sprintf("r%d", c.blocked)), io.Discard)
		raw, _ := os.ReadFile(calls)
		if err != nil || strings.Count(string(raw), "call") != c.wantCalls || runs[0].Blocked != c.blocked {
			t.Fatalf("%+v: calls %d runs %+v err %v", c, strings.Count(string(raw), "call"), runs, err)
		}
		g := routeCompleteGate(fastrouteResult(&runs[0], fastrouteOpts{bin: bin}, dsn))
		if n := strings.Count(strings.Join(g.Items, "\n"), "blocked by geometry"); n != c.blocked {
			t.Fatalf("gate items %v", g.Items)
		}
	}
	got := strings.Join(fastrouteArgs(fastrouteOpts{multiStart: 4, threads: 1, noOptimizer: true, optThreshold: 2}, "b.dsn", "b.ses", "r.json", ""), " ")
	for _, w := range []string{"--multi-start=4", "--router.optimizer.enabled=false", "--router.optimizer.optimization_improvement_threshold=2"} {
		if !strings.Contains(got, w) {
			t.Fatalf("args %s lack %s", got, w)
		}
	}
}

// Every intent class above the neck-down floor is passed to
// --no-neckdown-classes, also when its widthMil.min < outer: fastroute's
// fanout micro neck-down narrows congested segments anywhere (to 1/2 of the
// class width at worst), and the global min trace is only a floor.
func TestIntentClassesForbidNeckdown(t *testing.T) {
	raw, err := os.ReadFile("../pcb/specctra/testdata/easyeda-export.dsn")
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseDesignIntent([]byte(`{"schemaVersion":1,"nets":{
 "+12V":{"role":"power","widthMil":{"outer":30,"inner":30,"min":12},"clearanceMil":6},
 "GND":{"role":"ground","widthMil":{"outer":21.65,"inner":21.65,"min":20},"clearanceMil":6},
 "SIG":{"role":"signal","widthMil":{"outer":6,"min":6},"clearanceMil":6}}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _, rq, err := prepareDSN(string(raw), specctra.FixOptions{}, intentRequirements(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(rq.NoNeckdown, ",") != "+12V,GND" {
		t.Fatalf("no-neckdown %v: every intent class above the floor must be listed", rq.NoNeckdown)
	}
	got := strings.Join(fastrouteArgs(fastrouteOpts{noNeckdown: rq.NoNeckdown}, "b.dsn", "b.ses", "r.json", ""), " ")
	if !strings.Contains(got, "--no-neckdown-classes=+12V,GND") {
		t.Fatalf("args %s", got)
	}
}

// One executable across every location: a stale copy at the root and the
// release subdirectory's, or name.exe beside name.com, is ambiguous; a hard
// link seen twice is one file.
func TestResolveFastrouteAmbiguousLocations(t *testing.T) {
	exe := "fastroute"
	if runtime.GOOS == "windows" {
		exe = "fastroute.exe"
	}
	dir := t.TempDir()
	child := filepath.Join(dir, "fastroute-0.1.14-windows-x64", exe)
	writeExe(t, child)
	if got, err := resolveFastroute(dir); err != nil || !sameFile(t, got, child) {
		t.Fatalf("release subdir only: %q %v", got, err)
	}
	// The same file again under bin/ (hard link): still one candidate.
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(child, filepath.Join(dir, "bin", exe)); err == nil {
		if got, err := resolveFastroute(dir); err != nil || !sameFile(t, got, child) {
			t.Fatalf("hard link counted twice: %q %v", got, err)
		}
	}
	writeExe(t, filepath.Join(dir, exe)) // a stale root copy
	if _, err := resolveFastroute(dir); err == nil || !strings.Contains(err.Error(), "executables") {
		t.Fatalf("root vs release subdir not ambiguous: %v", err)
	}
	if runtime.GOOS == "windows" {
		d2 := t.TempDir()
		writeExe(t, filepath.Join(d2, "fastroute.exe"))
		writeExe(t, filepath.Join(d2, "fastroute.com"))
		if _, err := resolveFastroute(d2); err == nil || !strings.Contains(err.Error(), "2 executables") {
			t.Fatalf(".exe vs .com not ambiguous: %v", err)
		}
	}
}

// PATH (PATHEXT) finding only fastroute.bat / .cmd is refused like an
// explicit wrapper, not handed to exec.
func TestResolveFastroutePathWrapperRefused(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PATHEXT wrappers are Windows-only")
	}
	fakeHome(t)
	t.Setenv("FASTROUTE_BIN", "")
	saved := fastrouteLookPath
	fastrouteLookPath = exec.LookPath
	defer func() { fastrouteLookPath = saved }()
	for _, ext := range []string{".bat", ".cmd"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "fastroute"+ext), []byte("@echo off\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
		if _, err := resolveFastroute(""); err == nil || !strings.Contains(err.Error(), "not an executable") {
			t.Fatalf("PATH %s accepted: %v", ext, err)
		}
	}
}

// File-system errors in the lookup fail it (naming the path) instead of
// being skipped: an unreadable release subdirectory must not let a stale
// root executable win, and an unreadable `current` must not fall through
// to PATH. Only a missing bin/ is skipped.
func TestResolveFastrouteFileSystemErrors(t *testing.T) {
	exe := "fastroute"
	if runtime.GOOS == "windows" {
		exe = "fastroute.exe"
	}
	dir := t.TempDir()
	writeExe(t, filepath.Join(dir, exe)) // root copy
	sub := filepath.Join(dir, "fastroute-0.1.14-windows-x64")
	writeExe(t, filepath.Join(sub, exe))
	if err := os.Remove(filepath.Join(sub, exe)); err != nil { // the subdir stays, the lookup must read it
		t.Fatal(err)
	}
	denied := &os.PathError{Op: "open", Path: sub, Err: os.ErrPermission}
	savedRD, savedSt, savedL := routerReadDir, routerStat, routerLstat
	defer func() { routerReadDir, routerStat, routerLstat = savedRD, savedSt, savedL }()

	// No bin/: fine, the root copy resolves.
	if got, err := resolveFastroute(dir); err != nil || filepath.Base(got) != exe {
		t.Fatalf("root only: %q %v", got, err)
	}
	// Unreadable release subdirectory: fails, not the root copy.
	routerReadDir = func(p string) ([]os.DirEntry, error) {
		if p == sub {
			return nil, denied
		}
		return os.ReadDir(p)
	}
	if _, err := resolveFastroute(dir); err == nil || !strings.Contains(err.Error(), "cannot read") || !strings.Contains(err.Error(), sub) {
		t.Fatalf("unreadable subdir: %v", err)
	}
	// bin/ that exists but cannot be read: fails too.
	bin := filepath.Join(dir, "bin")
	routerReadDir = func(p string) ([]os.DirEntry, error) {
		if p == bin {
			return nil, &os.PathError{Op: "open", Path: bin, Err: os.ErrPermission}
		}
		return os.ReadDir(p)
	}
	if _, err := resolveFastroute(dir); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("unreadable bin/: %v", err)
	}
	// A candidate that cannot be stat'ed: fails.
	routerReadDir = os.ReadDir
	routerStat = func(p string) (os.FileInfo, error) {
		if strings.EqualFold(filepath.Base(p), exe) {
			return nil, &os.PathError{Op: "stat", Path: p, Err: os.ErrPermission}
		}
		return os.Stat(p)
	}
	if _, err := resolveFastroute(dir); err == nil || !strings.Contains(err.Error(), "cannot stat") {
		t.Fatalf("unstatable candidate: %v", err)
	}
	routerStat = os.Stat

	// An unreadable ~/.pcbpilot/fastroute/current fails; PATH is not used.
	fakeHome(t)
	t.Setenv("FASTROUTE_BIN", "")
	savedLook := fastrouteLookPath
	defer func() { fastrouteLookPath = savedLook }()
	onPath := filepath.Join(t.TempDir(), exe)
	writeExe(t, onPath)
	fastrouteLookPath = func(string) (string, error) { return onPath, nil }
	routerLstat = func(p string) (os.FileInfo, error) {
		return nil, &os.PathError{Op: "lstat", Path: p, Err: os.ErrPermission}
	}
	if got, err := resolveFastroute(""); err == nil || !strings.Contains(err.Error(), "cannot read") {
		t.Fatalf("unreadable current fell through to PATH: %q %v", got, err)
	}
	// A missing current does fall through to PATH.
	routerLstat = os.Lstat
	if got, err := resolveFastroute(""); err != nil || !sameFile(t, got, onPath) {
		t.Fatalf("missing current, PATH: %q %v", got, err)
	}
}

// A release directory reached through a symlink (or a Windows junction) is
// searched like a real one: a stale root executable plus a linked release
// subdirectory is ambiguous, never the root one; a dangling link fails.
// Same lookup for tracemaker.
func TestResolveRouterSymlinkedReleaseDir(t *testing.T) {
	for _, tool := range []string{"fastroute", "tracemaker"} {
		exe := tool
		if runtime.GOOS == "windows" {
			exe = tool + ".exe"
		}
		dir, real := t.TempDir(), t.TempDir()
		writeExe(t, filepath.Join(real, exe))
		link := filepath.Join(dir, tool+"-1.0-x64")
		err := os.Symlink(real, link)
		if err != nil && runtime.GOOS == "windows" {
			sh := os.Getenv("ComSpec")
			if sh == "" {
				sh = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
			}
			err = exec.Command(sh, "/c", "mklink", "/J", link, real).Run()
		}
		if err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		resolve := map[string]func(string) (string, error){"fastroute": resolveFastroute, "tracemaker": resolveTracemaker}[tool]
		if got, err := resolve(dir); err != nil || !sameFile(t, got, filepath.Join(real, exe)) {
			t.Fatalf("%s via linked dir: %q %v", tool, got, err)
		}
		writeExe(t, filepath.Join(dir, exe)) // stale root copy
		if got, err := resolve(dir); err == nil || !strings.Contains(err.Error(), "executables") {
			t.Fatalf("%s: stale root won over the linked release dir: %q %v", tool, got, err)
		}
		if err := os.Remove(filepath.Join(dir, exe)); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(real); err != nil {
			t.Fatal(err)
		}
		if _, err := resolve(dir); err == nil || !strings.Contains(err.Error(), "cannot stat") {
			t.Fatalf("%s: dangling link: %v", tool, err)
		}
	}
}
