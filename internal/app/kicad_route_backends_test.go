package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func TestTracemakerConstraints(t *testing.T) {
	reqs := map[string]kicad.NetRequirement{
		"+24V":  {OuterMil: 24},                  // wider than the floor, no intent minimum: never necks down
		"+5V":   {OuterMil: 24, MinMil: 24},      // minimum equals the class width: never necks down
		"+3V3":  {OuterMil: 15.75, MinMil: 9.84}, // may neck at the pads down to its intent minimum
		"D_ONE": {OuterMil: 6},                   // at or below the floor: nothing special
	}
	in := &designIntent{Nets: map[string]*intentNet{
		"USB_DP": {DiffPair: "USB_DM", PairGapMil: 6, MaxSkewMil: 4},
		"USB_DM": {DiffPair: "USB_DP", PairGapMil: 6, MaxSkewMil: 4},
	}}
	c := tracemakerConstraints(reqs, 8, in)
	if !c.Nets["+24V"].NoNeckdown || !c.Nets["+5V"].NoNeckdown || c.Nets["+3V3"].MinWidthMM != 0.25 || c.Nets["+3V3"].NoNeckdown {
		t.Fatalf("nets %+v", c.Nets)
	}
	if c.Nets["+24V"].WidthMM != 0.61 || c.Nets["+3V3"].WidthMM != 0.401 { // explicit widths: 24 mil = 0.6096 → 0.610 up
		t.Fatalf("widths %+v", c.Nets)
	}
	// a net at or under the floor still gets an explicit, non-necking entry
	if d := c.Nets["D_ONE"]; !d.NoNeckdown || d.WidthMM != 0.153 {
		t.Fatalf("floor net entry %+v", d)
	}
	if len(c.Pairs) != 1 || c.Pairs[0].P != "USB_DM" || c.Pairs[0].N != "USB_DP" || c.Pairs[0].GapMM != 0.153 || c.Pairs[0].SkewMM != 0.101 {
		t.Fatalf("pairs %+v", c.Pairs)
	}
	b, _ := json.Marshal(c)
	if !strings.Contains(string(b), `"version":1`) || !strings.Contains(string(b), `"no_neckdown":true`) {
		t.Fatalf("json %s", b)
	}
}

func TestTracemakerResult(t *testing.T) {
	rep := &tmReport{Connections: 359, Routed: 357, Seconds: 90}
	rep.Unrouted = append(rep.Unrouted,
		struct {
			Net     string `json:"net"`
			From    string `json:"from"`
			To      string `json:"to"`
			Blocked bool   `json:"blocked"`
			Reason  string `json:"reason"`
		}{"D9", "U303.13", "IC101.16", false, "ripped up"},
		struct {
			Net     string `json:"net"`
			From    string `json:"from"`
			To      string `json:"to"`
			Blocked bool   `json:"blocked"`
			Reason  string `json:"reason"`
		}{"+3V3", "U303.33", "U303.22", true, "boxed in"})
	res := tracemakerResult(rep, []string{"route"}, "0.9.0", "abc", "out.kicad_pcb", "rep.json")
	if res.Router != "tracemaker" || res.Board != "out.kicad_pcb" || res.UnroutedCount != 2 || len(res.Blocked) != 1 || res.Blocked[0].Net != "+3V3" || res.Session != "" {
		t.Fatalf("%+v", res)
	}
	if g := routeCompleteGate(res); g.Pass || !strings.Contains(g.Detail, "tracemaker result: 2 unrouted") {
		t.Fatalf("gate %+v", g)
	}
	// a count without the list still fails the gate
	res = tracemakerResult(&tmReport{Connections: 10, Routed: 8}, nil, "", "", "o", "r")
	if res.UnroutedCount != 2 {
		t.Fatalf("count %d", res.UnroutedCount)
	}
	if res = tracemakerResult(&tmReport{Connections: 10, Routed: 10}, nil, "", "", "o", "r"); !routeCompleteGate(res).Pass {
		t.Fatal("complete run must pass route-complete")
	}
}

// A stand-in tracemaker (this test binary, fake_router_test.go): answers
// `version` with 9.9.9 and otherwise behaves as named — ok (checks
// --constraints, writes the board and a report), incomplete (exit 3 with
// unrouted connections), crash (exit 1), norep (a board but no report) or
// sleep (30 s). The behaviour holds until the next call or the test's end.
func fakeTracemaker(t *testing.T, behaviour string) string {
	t.Helper()
	bin := fakeRouterBin(t, "tracemaker")
	t.Setenv(envFakeTMBehaves, behaviour)
	return bin
}

func TestRunTracemaker(t *testing.T) {
	dir := t.TempDir()
	in, out, rep := filepath.Join(dir, "in.kicad_pcb"), filepath.Join(dir, "out.kicad_pcb"), filepath.Join(dir, "r.json")
	bin := fakeTracemaker(t, "ok")
	cons := filepath.Join(dir, "c.json")
	if err := os.WriteFile(cons, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := runTracemaker(context.Background(), tracemakerOpts{bin: bin, timeout: 10 * time.Second}, in, out, cons, rep, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if res.Router != "tracemaker" || res.UnroutedCount != 1 || res.Version != "9.9.9" || res.Board != out || res.PatchSHA == "" || res.Seconds != 1.5 {
		t.Fatalf("%+v", res)
	}
	joined := strings.Join(res.Args, " ")
	for _, want := range []string{"--constraints", "--priority-order-only", "--soft-zones", "--time 10", "--no-kb"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q lack %q", joined, want)
		}
	}
	// exit 3 = finished with unrouted connections: an incomplete result, not an error
	inc := fakeTracemaker(t, "incomplete")
	if r3, err := runTracemaker(context.Background(), tracemakerOpts{bin: inc, timeout: 10 * time.Second}, in, out, cons, rep, io.Discard); err != nil || r3.UnroutedCount != 2 {
		t.Fatalf("exit 3 must give the incomplete result: %v %+v", err, r3)
	}
	// a crash is an error, never an empty success
	bad := fakeTracemaker(t, "crash")
	if _, err := runTracemaker(context.Background(), tracemakerOpts{bin: bad}, in, out, cons, rep, io.Discard); err == nil {
		t.Fatal("crashed tracemaker must fail")
	}
	// no report = no result
	noRep := fakeTracemaker(t, "norep")
	if _, err := runTracemaker(context.Background(), tracemakerOpts{bin: noRep}, in, out, cons, rep, io.Discard); err == nil {
		t.Fatal("a run without a report must fail")
	}
}

func TestRunTracemakerCancel(t *testing.T) {
	dir := t.TempDir()
	bin := fakeTracemaker(t, "sleep")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := runTracemaker(ctx, tracemakerOpts{bin: bin}, filepath.Join(dir, "i"), filepath.Join(dir, "o"), filepath.Join(dir, "c"), filepath.Join(dir, "r"), io.Discard)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 15*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

func TestResolveTracemaker(t *testing.T) {
	home := fakeHome(t) // HOME and USERPROFILE (os.UserHomeDir on Windows)
	t.Setenv("TRACEMAKER_BIN", "")
	t.Setenv("PATH", t.TempDir())
	if _, err := resolveTracemaker(""); err == nil || !strings.Contains(err.Error(), "tracemaker not found") {
		t.Fatalf("err %v", err)
	}
	dir := filepath.Join(home, ".pcbpilot", "tracemaker", "pcbpilot-abc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := "tracemaker"
	if runtime.GOOS == "windows" {
		exe = "tracemaker.exe"
	}
	bin := filepath.Join(dir, exe)
	_ = os.WriteFile(bin, []byte("x"), 0o755)
	linkCurrent(t, home, "tracemaker", dir) // a junction on unprivileged Windows
	got, err := resolveTracemaker("")
	// sameFile, not EvalSymlinks: that does not resolve a Windows junction.
	if err != nil || !sameFile(t, got, bin) {
		t.Fatalf("got %q err %v want %q", got, err, bin)
	}
	if _, err := resolveTracemaker(filepath.Join(home, "nope")); err == nil {
		t.Fatal("explicit missing path must fail")
	}
}

func TestQuickCheck(t *testing.T) {
	ok := &routeResult{Router: "x"}
	if q, _ := quickCheck(ok, 0); !q {
		t.Fatal("complete + clean must pass")
	}
	if q, n := quickCheck(&routeResult{UnroutedCount: 1}, 0); q || !strings.Contains(n, "unrouted") {
		t.Fatal("unrouted must fail")
	}
	if q, n := quickCheck(ok, 2); q || !strings.Contains(n, "DRC") {
		t.Fatal("new DRC errors must fail")
	}
	if q, _ := quickCheck(&routeResult{Fixable: 1}, 0); q {
		t.Fatal("fixable violations must fail")
	}
	if q, _ := quickCheck(nil, 0); q {
		t.Fatal("no result must fail")
	}
}

func fakeCand(name string, delay time.Duration, res *routeResult, err error) *routeCandidate {
	return &routeCandidate{Name: name,
		exec: func(ctx context.Context) (*routeResult, error) {
			select {
			case <-time.After(delay):
				return res, err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		commit: func(*routeResult) (string, error) { return name + ".kicad_pcb", nil }}
}

func TestRaceFirstQuickPassWins(t *testing.T) {
	fast := fakeCand("fast", 10*time.Millisecond, &routeResult{Router: "fast", Seconds: 1}, nil)
	slow := fakeCand("slow", 30*time.Second, &routeResult{Router: "slow"}, nil)
	var mu sync.Mutex
	var checked []string
	start := time.Now()
	w := raceCandidates(context.Background(), []*routeCandidate{slow, fast}, func(c *routeCandidate) {
		mu.Lock()
		checked = append(checked, c.Name)
		mu.Unlock()
		c.Quick, _ = quickCheck(c.Res, 0)
	})
	if w != fast || time.Since(start) > 10*time.Second {
		t.Fatalf("winner %v after %s", w, time.Since(start))
	}
	if !slow.Cancelled || slow.Res != nil || len(checked) != 1 {
		t.Fatalf("loser %+v checked %v", slow, checked)
	}
	// the cancelled router is still tried (rerun) when the winner fails a full gate
	ranked := rankCandidates([]*routeCandidate{slow, fast}, w)
	if ranked[0] != fast || ranked[1] != slow {
		t.Fatalf("order %s %s", ranked[0].Name, ranked[1].Name)
	}
}

func TestRaceNoQuickPassRanksByCompleteness(t *testing.T) {
	a := fakeCand("a", 5*time.Millisecond, &routeResult{Router: "a", UnroutedCount: 5}, nil)
	b := fakeCand("b", 20*time.Millisecond, &routeResult{Router: "b", UnroutedCount: 1}, nil)
	c := fakeCand("c", 1*time.Millisecond, nil, errors.New("crashed"))
	w := raceCandidates(context.Background(), []*routeCandidate{a, b, c}, func(x *routeCandidate) { x.Quick, x.QuickNote = quickCheck(x.Res, 0) })
	if w != nil {
		t.Fatalf("winner %v", w)
	}
	r := rankCandidates([]*routeCandidate{a, b, c}, nil)
	if r[0] != b || r[1] != a || r[2] != c {
		t.Fatalf("order %s %s %s", r[0].Name, r[1].Name, r[2].Name)
	}
	if c.Err == nil || c.Cancelled {
		t.Fatalf("a real failure is not a cancel: %+v", c)
	}
}

func TestRaceWinnerFailsCommitFallsThrough(t *testing.T) {
	a := fakeCand("a", 1*time.Millisecond, &routeResult{Router: "a"}, nil)
	a.commit = func(*routeResult) (string, error) { return "", errors.New("import failed") }
	b := fakeCand("b", 50*time.Millisecond, &routeResult{Router: "b"}, nil)
	w := raceCandidates(context.Background(), []*routeCandidate{a, b}, func(x *routeCandidate) { x.Quick, _ = quickCheck(x.Res, 0) })
	if w != b || a.Err == nil {
		t.Fatalf("winner %v a.Err %v", w, a.Err)
	}
}

func TestValidRouter(t *testing.T) {
	for _, ok := range []string{"fastroute", "tracemaker", "both"} {
		if !validRouter(ok) {
			t.Errorf("%s", ok)
		}
	}
	if validRouter("") || validRouter("freerouting") {
		t.Fatal("only the three backends")
	}
}

func TestCeilNetclassWidths(t *testing.T) {
	pro := filepath.Join(t.TempDir(), "in.kicad_pro")
	src := `{"net_settings":{"classes":[{"name":"PP3_W19p69","track_width":0.500126},{"name":"PP1","track_width":0.254},{"name":"Default","track_width":0.2001}]},"x":1}`
	if err := os.WriteFile(pro, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ceilNetclassWidths(pro); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(pro)
	var doc struct {
		NS struct {
			Classes []struct {
				Name string  `json:"name"`
				W    float64 `json:"track_width"`
			} `json:"classes"`
		} `json:"net_settings"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{"PP3_W19p69": 0.501, "PP1": 0.254, "Default": 0.2001}
	for _, c := range doc.NS.Classes {
		if c.W != want[c.Name] {
			t.Errorf("%s width %v, want %v", c.Name, c.W, want[c.Name])
		}
	}
}

func TestBestFailedCandidate(t *testing.T) {
	mk := func(name string, unrouted, drc int, tried bool) *routeCandidate {
		return &routeCandidate{Name: name, Res: &routeResult{UnroutedCount: unrouted}, NewDRC: drc, Tried: tried, Board: "b"}
	}
	fr, tm := mk("fastroute", 5, 0, true), mk("tracemaker", 2, 0, true)
	if got := bestFailedCandidate([]*routeCandidate{fr, tm}); got != tm {
		t.Fatalf("want tracemaker, got %v", got.Name)
	}
	if got := bestFailedCandidate([]*routeCandidate{mk("a", 1, 0, false)}); got != nil {
		t.Fatal("untried candidate must not be chosen")
	}
}

func TestDefaultRouteThreads(t *testing.T) {
	for _, c := range []struct {
		cpus int
		both bool
		want int
	}{{12, false, 8}, {12, true, 5}, {4, true, 2}, {2, false, 1}} {
		if got := defaultRouteThreads(c.cpus, c.both); got != c.want {
			t.Errorf("cpus=%d both=%v: got %d want %d", c.cpus, c.both, got, c.want)
		}
	}
}

// A router that finishes after the winner is not committed (its board does
// not exist yet) but keeps its result: when the winner then fails a full
// gate it goes through commit + the full gates, never skipped, never read as
// the winner's.
func TestRaceLateFinisherStaysUncommittedFallback(t *testing.T) {
	released := make(chan struct{})
	a := fakeCand("a", 0, &routeResult{Router: "a"}, nil)
	committedB := false
	b := &routeCandidate{Name: "b",
		exec: func(ctx context.Context) (*routeResult, error) {
			<-released // ignores ctx: finishes after the winner was chosen
			return &routeResult{Router: "b", UnroutedCount: 1}, nil
		},
		commit: func(*routeResult) (string, error) { committedB = true; return "b.kicad_pcb", nil }}
	w := raceCandidates(context.Background(), []*routeCandidate{a, b}, func(x *routeCandidate) {
		x.Quick, _ = quickCheck(x.Res, 0)
		close(released)
	})
	if w != a {
		t.Fatalf("winner %v", w)
	}
	if committedB || b.Board != "" || b.Res == nil || b.Err != nil {
		t.Fatalf("late finisher must keep its result uncommitted: %+v committed=%v", b, committedB)
	}
	r := rankCandidates([]*routeCandidate{b, a}, w)
	if r[0] != a || r[1] != b {
		t.Fatalf("order %s %s", r[0].Name, r[1].Name)
	}
}

// A candidate whose post-route pipeline crashed (Tried false) is never the
// "best result" the run's outputs are re-judged on.
func TestBestFailedCandidateIgnoresCrashed(t *testing.T) {
	crashed := &routeCandidate{Name: "tracemaker", Res: &routeResult{UnroutedCount: 0}, Board: "b", Tried: false}
	judged := &routeCandidate{Name: "fastroute", Res: &routeResult{UnroutedCount: 5}, Board: "b", Tried: true}
	if got := bestFailedCandidate([]*routeCandidate{crashed, judged}); got != judged {
		t.Fatalf("got %v", got)
	}
}

// widthMil.inner stays in the .kicad_dru `(layer inner)` rule (bridge.py) the
// router reads; the constraints file carries the outer width and never a
// narrower value because of it.
func TestTracemakerConstraintsInnerWidthKept(t *testing.T) {
	c := tracemakerConstraints(map[string]kicad.NetRequirement{"VCC": {OuterMil: 20, InnerMil: 30, MinMil: 20}}, 8, nil)
	n := c.Nets["VCC"]
	if !n.NoNeckdown || n.WidthMM != mmCeilMil(20) {
		t.Fatalf("VCC %+v", n)
	}
	src, err := os.ReadFile("../kicad/bridge.py")
	if err != nil || !strings.Contains(string(src), "(layer inner)") {
		t.Fatalf("the inner-layer track_width rule must come from the .kicad_dru writer: %v", err)
	}
}
