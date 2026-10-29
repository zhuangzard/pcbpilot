package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Hierarchical KiCad names that fastroute's pairs / tune files cannot carry
// (spaces, auto-page " (1/2)", quotes, '=') get aliases; plain names with
// slashes, braces and parentheses stay as they are.
func TestFastrouteNetAliases(t *testing.T) {
	names := []string{
		"/CAN-FD, RS-422, USB service/FT_DM", "/CAN-FD, RS-422, USB service/FT_DP",
		"/Sup (1/2)/A", "/{slash}B(x)/C", "/I{slash}O/D=1", "GND",
	}
	dsn := `(pcb x (network (net "/CAN-FD, RS-422, USB service/FT_DM" (pins U1-1)) (net "/CAN-FD, RS-422, USB service/FT_DP" (pins U1-2))
 (net "/Sup (1/2)/A" (pins U1-3)) (net /{slash}B(x)/C (pins U1-4)) (net "/I{slash}O/D=1" (pins U1-5)) (net GND (pins U1-6))
 (class c "/CAN-FD, RS-422, USB service/FT_DM" "/Sup (1/2)/A")) (wiring (wire (net "/Sup (1/2)/A"))))`
	a := newFastrouteNetAliases(names, dsn)
	for _, n := range []string{"/CAN-FD, RS-422, USB service/FT_DM", "/Sup (1/2)/A", "/I{slash}O/D=1"} {
		if !strings.HasPrefix(a.name(n), "pcbpilot_net_") || a.real(a.name(n)) != n {
			t.Fatalf("%q -> %q", n, a.name(n))
		}
	}
	for _, n := range []string{"/{slash}B(x)/C", "GND"} {
		if a.name(n) != n {
			t.Fatalf("safe name %q aliased to %q", n, a.name(n))
		}
	}
	out, err := a.apply(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "USB service") || strings.Contains(out, "(1/2)") || !strings.Contains(out, "(net "+a.name("/Sup (1/2)/A")+" (pins U1-3))") ||
		!strings.Contains(out, "(class c "+a.name("/CAN-FD, RS-422, USB service/FT_DM")+" "+a.name("/Sup (1/2)/A")+")") {
		t.Fatalf("aliased DSN:\n%s", out)
	}
	// The pairs file is whitespace-tokenised: every token is one name.
	in := &designIntent{Nets: map[string]*intentNet{
		names[0]: {DiffPair: names[1], PairGapMil: 6, LengthGroup: "USB grp"},
		names[1]: {DiffPair: names[0], PairGapMil: 6, LengthGroup: "USB grp"},
	}}
	pairs, tune := intentPairsAndTune(in, a)
	f := strings.Fields(strings.TrimSpace(pairs))
	if len(f) != 4 || f[1] != a.name(names[0]) || f[2] != a.name(names[1]) {
		t.Fatalf("pairs %q", pairs)
	}
	if !strings.Contains(tune, "group USB_grp") || strings.Contains(tune, "USB service") {
		t.Fatalf("tune %q", tune)
	}
	// The session comes back with the real names, quoted.
	dir := t.TempDir()
	ses := filepath.Join(dir, "r.ses")
	_ = os.WriteFile(ses, []byte("(session x (net "+a.name(names[0])+" (wire)) (net \""+a.name(names[1])+"\" (wire)) (net GND (wire)))"), 0o644)
	if err := a.unaliasSession(ses, ses+".real"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ses + ".real")
	if !strings.Contains(string(b), `(net "/CAN-FD, RS-422, USB service/FT_DM" (wire))`) || !strings.Contains(string(b), "(net GND (wire))") || !strings.Contains(string(b), `(net "/CAN-FD, RS-422, USB service/FT_DP" (wire))`) {
		t.Fatalf("session %s", b)
	}
	if _, err := a.apply(`(pcb x (network (net GND)))`); err == nil {
		t.Fatal("a missing aliased net must fail")
	}
}

// runFastroute end to end with the fake router: fastroute reads an aliased
// DSN copy and pairs file; the returned session has the real names.
func TestRunFastrouteAliasesSpacedNets(t *testing.T) {
	dir := t.TempDir()
	bin := fakeRouterBin(t, "fastroute")
	t.Setenv(envFakeFRMode, "echo-pairs")
	dsn := filepath.Join(dir, "b.dsn")
	n1, n2 := "/USB service (1/2)/D_N", "/USB service (1/2)/D_P"
	_ = os.WriteFile(dsn, []byte(`(pcb x (resolution um 10) (network (net "`+n1+`" (pins U1-1)) (net "`+n2+`" (pins U1-2))))`), 0o644)
	in := &designIntent{Nets: map[string]*intentNet{n1: {DiffPair: n2}, n2: {DiffPair: n1}}}
	al := newFastrouteNetAliases(intentRouterNets(in), `(net "`+n1+`") (net "`+n2+`")`)
	pairs, _ := intentPairsAndTune(in, al)
	pf := filepath.Join(dir, "b-pairs.txt")
	_ = os.WriteFile(pf, []byte(pairs), 0o644)
	ses, runs, err := runFastroute(fastrouteOpts{bin: bin, timeout: time.Minute, pairsFile: pf, aliases: al}, dsn, filepath.Join(dir, "b"), os.Stderr)
	if err != nil || len(runs) != 1 {
		t.Fatalf("%v %+v", err, runs)
	}
	got, _ := os.ReadFile(ses)
	if !strings.HasSuffix(ses, ".names.ses") || !strings.Contains(string(got), `(net "`+n1+`"`) || !strings.Contains(string(got), `(net "`+n2+`"`) {
		t.Fatalf("session %s:\n%s", ses, got)
	}
	fdsn, _ := os.ReadFile(filepath.Join(dir, "b.fastroute.dsn"))
	if strings.Contains(string(fdsn), "USB service") {
		t.Fatalf("fastroute's DSN still has the spaced names:\n%s", fdsn)
	}
	if real, _ := os.ReadFile(dsn); !strings.Contains(string(real), n1) {
		t.Fatal("the real DSN was changed")
	}
}

// A fastroute that writes a checkpoint and then neither stops nor reports
// is stopped at --max-time + grace, killed after the kill grace, and its
// checkpoint is the (partial) result — not an error.
func TestRunFastroutePartialCheckpoint(t *testing.T) {
	dir := t.TempDir()
	bin := fakeRouterBin(t, "fastroute")
	t.Setenv(envFakeFRMode, "checkpoint-hang")
	var log strings.Builder
	start := time.Now()
	ses, runs, err := runFastroute(fastrouteOpts{bin: bin, timeout: time.Hour, maxTime: time.Second, stopGrace: time.Second, killGrace: time.Second, rounds: 2},
		filepath.Join(dir, "b.dsn"), filepath.Join(dir, "b"), &log)
	if err != nil {
		t.Fatalf("err %v\n%s", err, log.String())
	}
	if len(runs) != 1 || runs[0].Status != "partial" || runs[0].Unrouted != -1 || ses != runs[0].Session {
		t.Fatalf("runs %+v ses %s\n%s", runs, ses, log.String())
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("took %s: the max-time stop was not enforced", d)
	}
	res := fastrouteResult(lastUsable(runs), fastrouteOpts{bin: bin}, "b.dsn")
	if !res.Partial {
		t.Fatal("result not partial")
	}
	if ok, why := quickCheck(res, 0); ok || !strings.Contains(why, "time limit") {
		t.Fatalf("quick check %v %s", ok, why)
	}
	counted := &routeResult{UnroutedCount: 500}
	if unroutedRank(res) <= unroutedRank(counted) {
		t.Fatal("a partial result must rank after a counted one")
	}
	if !strings.Contains(log.String(), "last checkpoint session") {
		t.Fatalf("log:\n%s", log.String())
	}
}

func TestFastrouteDeadlineAndAutoMaxTime(t *testing.T) {
	if d := fastrouteDeadline(fastrouteOpts{timeout: 45 * time.Minute, maxTime: 30 * time.Minute}); d != 33*time.Minute {
		t.Fatalf("deadline %s", d)
	}
	if d := fastrouteDeadline(fastrouteOpts{timeout: 10 * time.Minute, maxTime: 30 * time.Minute}); d != 10*time.Minute {
		t.Fatalf("hard limit first: %s", d)
	}
	if d := fastrouteDeadline(fastrouteOpts{timeout: 45 * time.Minute}); d != 45*time.Minute {
		t.Fatalf("no max-time: %s", d)
	}
	if fastrouteAutoMaxTime(0) != 10*time.Minute || fastrouteAutoMaxTime(3202) != 26*time.Minute+time.Second || fastrouteAutoMaxTime(100000) != 40*time.Minute {
		t.Fatalf("auto %s %s %s", fastrouteAutoMaxTime(0), fastrouteAutoMaxTime(3202), fastrouteAutoMaxTime(100000))
	}
}
