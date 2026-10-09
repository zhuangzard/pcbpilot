package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	if _, ok := c.Nets["D_ONE"]; ok {
		t.Fatalf("unconstrained net listed: %+v", c.Nets)
	}
	if len(c.Pairs) != 1 || c.Pairs[0].P != "USB_DM" || c.Pairs[0].N != "USB_DP" || c.Pairs[0].GapMM != 0.152 || c.Pairs[0].SkewMM != 0.102 {
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

// A stand-in tracemaker: writes the board and a report, checks its arguments.
func fakeTracemaker(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tracemaker")
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'tracemaker 9.9.9'; exit 0; fi\n" + body
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunTracemaker(t *testing.T) {
	dir := t.TempDir()
	in, out, rep := filepath.Join(dir, "in.kicad_pcb"), filepath.Join(dir, "out.kicad_pcb"), filepath.Join(dir, "r.json")
	bin := fakeTracemaker(t, `
out=""; json=""; cons=""
while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift;; --json) json=$2; shift;; --constraints) cons=$2; shift;; esac; shift; done
[ -f "$cons" ] || exit 3
echo routed > "$out"
echo '{"connections":5,"routed":4,"seconds":1.5,"unrouted":[{"net":"N","from":"U1.1","to":"U2.2","blocked":false,"reason":"x"}]}' > "$json"
`)
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
	// a crash is an error, never an empty success
	bad := fakeTracemaker(t, "exit 1\n")
	if _, err := runTracemaker(context.Background(), tracemakerOpts{bin: bad}, in, out, cons, rep, io.Discard); err == nil {
		t.Fatal("crashed tracemaker must fail")
	}
	// no report = no result
	noRep := fakeTracemaker(t, "out=''; while [ $# -gt 0 ]; do case \"$1\" in -o) out=$2; shift;; esac; shift; done; echo x > \"$out\"\n")
	if _, err := runTracemaker(context.Background(), tracemakerOpts{bin: noRep}, in, out, cons, rep, io.Discard); err == nil {
		t.Fatal("a run without a report must fail")
	}
}

func TestRunTracemakerCancel(t *testing.T) {
	dir := t.TempDir()
	bin := fakeTracemaker(t, "sleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := runTracemaker(ctx, tracemakerOpts{bin: bin}, filepath.Join(dir, "i"), filepath.Join(dir, "o"), filepath.Join(dir, "c"), filepath.Join(dir, "r"), io.Discard)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 15*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(start))
	}
}

func TestResolveTracemaker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TRACEMAKER_BIN", "")
	t.Setenv("PATH", t.TempDir())
	if _, err := resolveTracemaker(""); err == nil || !strings.Contains(err.Error(), "tracemaker not found") {
		t.Fatalf("err %v", err)
	}
	dir := filepath.Join(home, ".pcbpilot", "tracemaker", "pcbpilot-abc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "tracemaker")
	_ = os.WriteFile(bin, []byte("x"), 0o755)
	_ = os.Symlink(dir, filepath.Join(home, ".pcbpilot", "tracemaker", "current"))
	got, err := resolveTracemaker("")
	want, _ := filepath.EvalSymlinks(bin)
	if g, _ := filepath.EvalSymlinks(got); err != nil || g != want {
		t.Fatalf("got %q err %v want %q", got, err, want)
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
