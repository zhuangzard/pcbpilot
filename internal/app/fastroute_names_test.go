package app

import (
	"os"
	"path/filepath"
	"sort"
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

// Whole tokens only: a real net named like an alias, or with an alias as a
// prefix, is never rewritten, and the alias picked avoids it.
func TestFastrouteNetAliasesTokenBoundaries(t *testing.T) {
	dsn := `(pcb x (network (net pcbpilot_net_1 (pins U1-1)) (net pcbpilot_net_2_suffix (pins U1-2)) (net "pcbpilot_net_2_q" (pins U1-3))
 (net "/A B" (pins U1-4)) (class c pcbpilot_net_1 "/A B" pcbpilot_net_2_suffix)))`
	a := newFastrouteNetAliases([]string{"/A B"}, dsn)
	al := a.name("/A B")
	if al == "pcbpilot_net_1" || al != "pcbpilot_net_2" {
		t.Fatalf("alias %q", al)
	}
	out, err := a.apply(dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"(net pcbpilot_net_1 (pins", "(net pcbpilot_net_2_suffix (pins", `(net "pcbpilot_net_2_q" (pins`, "(class c pcbpilot_net_1 pcbpilot_net_2 pcbpilot_net_2_suffix)"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("lacks %q:\n%s", keep, out)
		}
	}
	dir := t.TempDir()
	ses := filepath.Join(dir, "s.ses")
	_ = os.WriteFile(ses, []byte(`(session (routes (network_out (net pcbpilot_net_2 (wire)) (net "pcbpilot_net_2" (wire)) (net pcbpilot_net_2_suffix (wire)) (net "pcbpilot_net_2_q" (wire)) (net pcbpilot_net_1 (wire)))))`), 0o644)
	if err := a.unaliasSession(ses, ses+".r"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(ses + ".r")
	want := `(session (routes (network_out (net "/A B" (wire)) (net "/A B" (wire)) (net pcbpilot_net_2_suffix (wire)) (net "pcbpilot_net_2_q" (wire)) (net pcbpilot_net_1 (wire)))))`
	if string(b) != want {
		t.Fatalf("session\n got %s\nwant %s", b, want)
	}
	if got := a.realText("pcbpilot_net_2 / pcbpilot_net_2_suffix"); got != "/A B / pcbpilot_net_2_suffix" {
		t.Fatalf("realText %q", got)
	}
}

// KiCad's DSN spelling round-trips byte for byte: an embedded " written as
// two apostrophes, single quotes, = # * ? and a literal backslash, bare or quoted.
func TestFastrouteNetAliasesKiCadQuoting(t *testing.T) {
	names := []string{`/He said "hi" \ x`, `/it's (1/2)/B`, `/E=mc#2`, `/A*?`}
	dsn := `(pcb x (parser (string_quote ")) (network (net "/He said ''hi'' \ x" (pins U1-1)) (net "/it's (1/2)/B" (pins U1-2)) (net /E=mc#2 (pins U1-3)) (net /A*? (pins U1-4))
 (class c "/He said ''hi'' \ x" /E=mc#2)) (wiring (wire (path F.Cu 1 0 0 1 0) (net "/it's (1/2)/B"))))`
	a := newFastrouteNetAliases(names, dsn)
	out, err := a.apply(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "said") || strings.Contains(out, "it's") || strings.Contains(out, "mc#2") || strings.Contains(out, "/A*?") {
		t.Fatalf("aliased DSN keeps a name:\n%s", out)
	}
	if !strings.Contains(out, `(string_quote ")`) {
		t.Fatal("string_quote damaged")
	}
	var ses strings.Builder
	ses.WriteString("(session (routes (network_out")
	for _, n := range names {
		ses.WriteString(` (net "` + a.name(n) + `" (wire))`)
	}
	ses.WriteString(")))")
	dir := t.TempDir()
	p := filepath.Join(dir, "s.ses")
	_ = os.WriteFile(p, []byte(ses.String()), 0o644)
	if err := a.unaliasSession(p, p+".r"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p + ".r")
	want := `(session (routes (network_out (net "/He said ''hi'' \ x" (wire)) (net "/it's (1/2)/B" (wire)) (net /E=mc#2 (wire)) (net /A*? (wire)))))`
	if string(b) != want {
		t.Fatalf("session\n got %s\nwant %s", b, want)
	}
	for _, at := range dsnAtoms(want) {
		if at.netID && dsnDecode(at.raw) != "" {
			found := false
			for _, n := range names {
				found = found || dsnDecode(at.raw) == n
			}
			if !found {
				t.Fatalf("decoded %q is no real name", dsnDecode(at.raw))
			}
		}
	}
}

// Length-group names that sanitise to the same token stay separate groups
// with their own members and tolerances; an already-safe name keeps itself.
func TestIntentTuneGroupIDsUnique(t *testing.T) {
	in := &designIntent{Nets: map[string]*intentNet{
		"N1": {LengthGroup: "USB A", LengthTolMil: 10}, "N2": {LengthGroup: "USB A", LengthTolMil: 10},
		"N3": {LengthGroup: "USB_A", LengthTolMil: 20}, "N4": {LengthGroup: "USB_A", LengthTolMil: 20},
		"N5": {LengthGroup: "USB*A", LengthTolMil: 30}, "N6": {LengthGroup: "USB*A", LengthTolMil: 30},
		"N7": {LengthGroup: "A#B"}, "N8": {LengthGroup: "A#B"},
		"N9": {LengthGroup: "A=B"}, "N10": {LengthGroup: "A=B"},
	}}
	_, tune, renamed := intentPairsTuneGroups(in, nil)
	groups := map[string][]string{} // id → member lines
	tols := map[string]string{}
	cur := ""
	for _, line := range strings.Split(strings.TrimSpace(tune), "\n") {
		f := strings.Fields(line)
		if f[0] == "group" {
			cur = f[1]
			if _, dup := groups[cur]; dup {
				t.Fatalf("group %s twice:\n%s", cur, tune)
			}
			groups[cur] = nil
			if len(f) > 2 {
				tols[cur] = f[2]
			}
			continue
		}
		groups[cur] = append(groups[cur], f[0])
	}
	if len(groups) != 5 {
		t.Fatalf("%d groups:\n%s", len(groups), tune)
	}
	if strings.Join(groups["USB_A"], ",") != "N3,N4" || tols["USB_A"] != "tolerance=0.508" {
		t.Fatalf("the safe name lost its group:\n%s", tune)
	}
	members := map[string]string{"USB A": "N1,N2", "USB*A": "N5,N6", "A#B": "N7,N8", "A=B": "N10,N9"}
	for id, name := range renamed {
		got := append([]string(nil), groups[id]...)
		sort.Strings(got)
		if strings.Join(got, ",") != members[name] {
			t.Fatalf("group %s (%s) members %v:\n%s", id, name, got, tune)
		}
	}
	wantTol := map[string]string{"USB A": "tolerance=0.254", "USB*A": "tolerance=0.762"}
	for id, name := range renamed {
		if tols[id] != wantTol[name] {
			t.Fatalf("group %s (%s) tolerance %q: %s", id, name, tols[id], tune)
		}
	}
	if len(renamed) != 4 {
		t.Fatalf("renamed %v", renamed)
	}
	// Deterministic.
	_, again, _ := intentPairsTuneGroups(in, nil)
	if again != tune {
		t.Fatal("tune file not deterministic")
	}
}
