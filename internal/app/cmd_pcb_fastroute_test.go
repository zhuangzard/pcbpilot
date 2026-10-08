package app

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	r, err := readFastrouteReport(p)
	if err != nil || r.Unrouted != 3 || r.Violations != 2 || r.Fixable != 1 || r.FixableList[0] != "TopLayer at (1833.4, 1143.7) mil: trace GND / pin " {
		t.Fatalf("got %+v %v", r, err)
	}
	if err := os.WriteFile(p, []byte(`{"stats":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readFastrouteReport(p); err == nil {
		t.Fatal("report without stats.unrouted accepted")
	}
}

func TestResolveFastroute(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fastroute")
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
	// Short form recorded on the Gas Module v6A3 run.
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
	if rq.MinTraceMil != 6 || strings.Join(rq.NoNeckdown, ",") != "+12V" { // SIG sits at the 6 mil floor
		t.Fatalf("requirement report = %+v", rq)
	}
	// A DSN not in mil cannot be checked: the gate refuses rather than guess.
	notMil := strings.Replace(string(raw), "(resolution mil 1000)", "(resolution um 10)", 1)
	if _, _, _, err := prepareDSN(notMil, specctra.FixOptions{}, reqs); err == nil || !strings.Contains(err.Error(), "pre-route gate") {
		t.Fatalf("non-mil DSN passed the gate: %v", err)
	}
}

// fastroute 0.1.7 can panic in its parallel autorouter after writing a
// checkpoint session but no report (Gas Module v9). The run must be recorded
// as crashed with unknown counts and repeated once single-threaded.
func TestRunFastrouteCrashRetry(t *testing.T) {
	skipOnWindows(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "fastroute")
	script := `#!/bin/sh
for a in "$@"; do
  case "$a" in
    -do) next=ses ;;
    --report=*) report="${a#--report=}" ;;
    --multi-start=1) single=1 ;;
    *) if [ "$next" = ses ]; then ses="$a"; next=; fi ;;
  esac
done
echo "(session x)" > "$ses"
if [ -z "$single" ]; then echo "thread panicked: free list corrupted" >&2; exit 101; fi
echo '{"stats":{"unrouted":0,"violations":0}}' > "$report"
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
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
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 101\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, runs, err := runFastroute(fastrouteOpts{bin: bin, timeout: time.Minute}, "b.dsn", filepath.Join(dir, "c"), io.Discard); err == nil || runs[len(runs)-1].Unrouted != -1 {
		t.Fatalf("always-crashing router accepted: %+v %v", runs, err)
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
	pairs, tune := intentPairsAndTune(in)
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
