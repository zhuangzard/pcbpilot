package app

// cmd_kicad_rules_keepstricter_test.go — kicad rules --keep-stricter: a
// user rule that is at least as strict as a generated minimum stays in
// force by rule ORDER (the generated minimum goes to a head block before
// the user's rules); no user rule text is changed or removed.

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

const (
	keepTestPair = "pcbpilot isolation PPD_HVD|PPD_LV"
	// The same pair with pkg/intent's pair ends (keepTestPairOnly).
	keepTestPairD = "pcbpilot isolation PPD_domain_HVD|PPD_domain_LV"
	keepTestHead  = "pcbpilot-rules floor isolation PPD_HVD|PPD_LV clearance"
)

// keepTestRule is a user clearance rule on the fixture's insulation pair.
func keepTestRule(name, min string) string {
	return "(rule \"" + name + "\"\n  (condition \"A.NetName == 'HV' && B.NetName == 'VCC'\")\n  (constraint clearance (min " + min + ")))\n"
}

func keepTestWriteDRU(t *testing.T, f *rulesFixture, rules ...string) string {
	t.Helper()
	p := boardSibling(f.o.pcb, ".kicad_dru")
	if err := os.WriteFile(p, []byte("(version 1)\n"+strings.Join(rules, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// keepTestPairOnly gives the fixture pkg/intent's pair ends ("domain:<id>"):
// the pair distance is then the pair rule's alone, the class clearances
// stay the nets' own (0.508 mm / 0.2 mm) — a 1 mm user rule is below the
// pair clearance but above every class clearance.
func keepTestPairOnly(t *testing.T, f *rulesFixture) {
	t.Helper()
	if err := os.WriteFile(f.o.intent, []byte(rulesPrefixedIntent), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keepTestConflict(rep *rulesReport, name string) (rulesConflict, bool) {
	for _, c := range rep.Conflicts {
		if c.Name == name {
			return c, true
		}
	}
	return rulesConflict{}, false
}

func keepTestRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The user's rule is stricter than the generated pair clearance: without
// the flag it is a conflict that names both ways out; with it the generated
// clearance goes before the user's rule, whose text stays byte-identical.
func TestKicadRulesKeepStricterUserStricter(t *testing.T) {
	f := newRulesFixture(t)
	user := keepTestRule("iso_gap", "4mm")
	dru := keepTestWriteDRU(t, f, user)
	before := rulesFileSHA(t, dru)
	f.o.apply = true

	rep, code, _ := runRules(t, f.o)
	c, ok := keepTestConflict(rep, "iso_gap")
	if code != 6 || rep.Committed || !ok || c.Kind != "dru-weakened" || c.Resolved != "" || c.UserVsGenerated != "stricter" ||
		!strings.Contains(c.Detail, "--keep-stricter") || !strings.Contains(c.Detail, "--replace") || rep.KeepStricter != nil {
		t.Fatalf("without the flag: exit %d %+v", code, rep.Conflicts)
	}
	if rulesFileSHA(t, dru) != before {
		t.Fatal("a refused apply wrote the .kicad_dru")
	}

	f.o.keepStricter = true
	rep, code, stderr := runRules(t, f.o)
	c, ok = keepTestConflict(rep, "iso_gap")
	if code != 0 || !rep.Committed || !ok || c.Resolved != "keep-stricter" || c.open() {
		t.Fatalf("with the flag: exit %d conflicts %+v errors %v\n%s", code, rep.Conflicts, rep.Errors, stderr)
	}
	ks := rep.KeepStricter
	if ks == nil || len(ks.Head) != 1 || len(ks.Unresolved) != 0 || ks.Head[0].HeadRule != keepTestHead || ks.Head[0].Generated != keepTestPair ||
		ks.Head[0].Type != "clearance" || ks.Head[0].MinNm != 2000000 || len(ks.Head[0].YieldsTo) != 1 || ks.Head[0].YieldsTo[0] != (keepRef{Rule: "iso_gap", MinNm: 4000000}) {
		b, _ := json.Marshal(ks)
		t.Fatalf("report: %s", b)
	}
	if !strings.Contains(stderr, "keep-stricter:") || !strings.Contains(stderr, "iso_gap") || !strings.Contains(stderr, keepTestPair) {
		t.Fatalf("the text report does not say what keep-stricter did:\n%s", stderr)
	}
	text := keepTestRead(t, dru)
	hb, he, at, rb := strings.Index(text, kicad.DRUHeadBegin), strings.Index(text, kicad.DRUHeadEnd), strings.Index(text, user), strings.Index(text, kicad.DRURouteBegin)
	if hb < 0 || !(hb < he && he < at && at < rb && rb < strings.Index(text, kicad.DRURulesBegin)) {
		t.Fatalf("order (head %d..%d, user %d, route %d):\n%s", hb, he, at, rb, text)
	}
	if !strings.HasPrefix(text, "(version 1)\n"+kicad.DRUHeadBegin) || strings.Count(text, "iso_gap\"\n") != 1 {
		t.Fatalf("head block is not right after (version):\n%s", text)
	}
	rules, err := kicad.ParseDRU(text)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range rules {
		switch u.Name {
		case keepTestHead:
			if u.Block != "head" || len(u.Constraints) != 1 || u.Constraints[0].Type != "clearance" || *u.Constraints[0].Min != 2000000 || u.Condition != isoCondition("PPD_HVD", "PPD_LV") {
				t.Fatalf("head rule: %+v", u)
			}
		case keepTestPair:
			// The creepage no user rule touches stays at the end; the
			// clearance is not there a second time (it would win again).
			if u.Block != "route" || hasConstraint(u, "clearance") || !hasConstraint(u, "creepage") {
				t.Fatalf("route rule: %+v", u)
			}
		case "iso_gap":
			if u.Block != "" {
				t.Fatalf("user rule read as owned: %+v", u)
			}
		}
	}

	// Idempotent: the same run again writes nothing.
	sum := rulesFileSHA(t, dru) + rulesFileSHA(t, boardSibling(f.o.pcb, ".kicad_pro"))
	rep, code, stderr = runRules(t, f.o)
	if code != 0 || len(rep.Files) != 0 || rulesFileSHA(t, dru)+rulesFileSHA(t, boardSibling(f.o.pcb, ".kicad_pro")) != sum {
		t.Fatalf("re-apply: exit %d files %v\n%s\n%s", code, rep.Files, stderr, keepTestRead(t, dru))
	}
	// --check of the board as it is (no flag needed: the head block is
	// there) understands the head block.
	chk := f.o
	chk.apply, chk.check, chk.keepStricter = false, true, false
	rep, _, stderr = runRules(t, chk)
	if c := checkOf(rep, "effective-rules"); len(c.Items) != 0 {
		t.Fatalf("--check effective-rules: %+v\n%s", c, stderr)
	}
	for _, c := range rep.Conflicts {
		if c.open() {
			t.Fatalf("--check: open conflict %+v", c)
		}
	}

	// A later run WITHOUT the flag: the conflict is reported again and the
	// apply is refused; the file is not touched (its order still keeps the
	// user's rule in force).
	f.o.keepStricter = false
	rep, code, _ = runRules(t, f.o)
	if c, ok := keepTestConflict(rep, "iso_gap"); code != 6 || rep.Committed || !ok || c.Resolved != "" || rulesFileSHA(t, dru)+rulesFileSHA(t, boardSibling(f.o.pcb, ".kicad_pro")) != sum {
		t.Fatalf("later run without the flag: exit %d %+v", code, rep.Conflicts)
	}
	// … and whatever a run without the flag writes has no head block: with
	// --replace the user rule goes, the head block goes, and the generated
	// clearance is at the end again.
	f.o.replace = []string{"iso_gap"}
	rep, code, stderr = runRules(t, f.o)
	text = keepTestRead(t, dru)
	if code != 0 || !rep.Committed || strings.Contains(text, kicad.DRUHeadBegin) || strings.Contains(text, kicad.DRUHeadPrefix) || strings.Contains(text, "iso_gap") {
		t.Fatalf("replace without the flag: exit %d %v\n%s\n%s", code, rep.Errors, stderr, text)
	}
	rules, _ = kicad.ParseDRU(text)
	found := false
	for _, u := range rules {
		if u.Name == keepTestPair {
			found = u.Block == "route" && hasConstraint(u, "clearance") && hasConstraint(u, "creepage")
		}
	}
	if !found {
		t.Fatalf("the generated clearance is not back at the end:\n%s", text)
	}
}

// The owner removes the stricter rule by hand: the next run without the
// flag leaves no stale head block.
func TestKicadRulesKeepStricterHeadRemovedWhenNotNeeded(t *testing.T) {
	f := newRulesFixture(t)
	user := keepTestRule("iso_gap", "4mm")
	dru := keepTestWriteDRU(t, f, user)
	f.o.apply, f.o.keepStricter = true, true
	if rep, code, stderr := runRules(t, f.o); code != 0 || !rep.Committed {
		t.Fatalf("apply: %d\n%s", code, stderr)
	}
	text := keepTestRead(t, dru)
	if !strings.Contains(text, kicad.DRUHeadBegin) {
		t.Fatalf("no head block:\n%s", text)
	}
	if err := os.WriteFile(dru, []byte(strings.Replace(text, user, "", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []bool{false, true} {
		f.o.keepStricter = flag
		rep, code, stderr := runRules(t, f.o)
		text = keepTestRead(t, dru)
		if code != 0 || len(rep.Conflicts) != 0 || strings.Contains(text, kicad.DRUHeadBegin) || strings.Contains(text, kicad.DRUHeadPrefix) {
			t.Fatalf("flag %v: exit %d conflicts %+v\n%s\n%s", flag, code, rep.Conflicts, stderr, text)
		}
		rules, _ := kicad.ParseDRU(text)
		ok := false
		for _, u := range rules {
			if u.Name == keepTestPair {
				ok = u.Block == "route" && hasConstraint(u, "clearance")
			}
		}
		if !ok {
			t.Fatalf("flag %v: the generated clearance is not at the end:\n%s", flag, text)
		}
	}
}

// The user's rule is weaker: the generated rule stays last (it wins, as it
// does today); the flag accepts the user rule as it is and says so.
func TestKicadRulesKeepStricterUserWeaker(t *testing.T) {
	f := newRulesFixture(t)
	keepTestPairOnly(t, f)
	user := keepTestRule("iso_gap", "1mm")
	dru := keepTestWriteDRU(t, f, user)
	f.o.apply = true
	rep, code, _ := runRules(t, f.o)
	if c, ok := keepTestConflict(rep, "iso_gap"); code != 6 || !ok || c.UserVsGenerated != "weaker" || !strings.Contains(c.Detail, "--keep-stricter") || !strings.Contains(c.Detail, "--replace") {
		t.Fatalf("without the flag: exit %d %+v", code, rep.Conflicts)
	}
	f.o.keepStricter = true
	rep, code, stderr := runRules(t, f.o)
	c, ok := keepTestConflict(rep, "iso_gap")
	ks := rep.KeepStricter
	if code != 0 || !rep.Committed || !ok || c.Resolved != "keep-stricter" || ks == nil || len(ks.Head) != 0 || len(ks.Unresolved) != 0 ||
		len(ks.Tail) != 1 || ks.Tail[0].Generated != keepTestPairD || ks.Tail[0].Type != "clearance" || len(ks.Tail[0].Over) != 1 || ks.Tail[0].Over[0].Rule != "iso_gap" {
		b, _ := json.Marshal(ks)
		t.Fatalf("with the flag: exit %d %+v %s\n%s", code, rep.Conflicts, b, stderr)
	}
	text := keepTestRead(t, dru)
	if strings.Contains(text, kicad.DRUHeadBegin) || strings.Contains(text, kicad.DRUHeadPrefix) {
		t.Fatalf("a head block for a weaker user rule:\n%s", text)
	}
	at, rb := strings.Index(text, user), strings.Index(text, kicad.DRURouteBegin)
	if at < 0 || rb < at {
		t.Fatalf("the user text changed or the generated block is not after it:\n%s", text)
	}
	rules, _ := kicad.ParseDRU(text)
	for _, u := range rules {
		if u.Name == keepTestPairD && !(hasConstraint(u, "clearance") && *u.Constraints[0].Min == 2000000) {
			t.Fatalf("the generated clearance left the end: %+v", u)
		}
	}
	sum := rulesFileSHA(t, dru)
	if rep, code, _ = runRules(t, f.o); code != 0 || len(rep.Files) != 0 || rulesFileSHA(t, dru) != sum {
		t.Fatalf("re-apply: exit %d files %v", code, rep.Files)
	}
}

// One generated rule would have to be after a weaker user rule and before
// a stricter one: no order does both, the conflict stays and says which
// rules and why. Nothing is written.
func TestKicadRulesKeepStricterImpossibleOrder(t *testing.T) {
	f := newRulesFixture(t)
	keepTestPairOnly(t, f)
	dru := keepTestWriteDRU(t, f, keepTestRule("iso_low", "1mm"), keepTestRule("iso_high", "4mm"))
	before := rulesFileSHA(t, dru)
	f.o.apply, f.o.keepStricter = true, true
	rep, code, stderr := runRules(t, f.o)
	hi, ok := keepTestConflict(rep, "iso_high")
	if code != 6 || rep.Committed || !ok || !hi.open() || rulesFileSHA(t, dru) != before {
		t.Fatalf("exit %d committed %v %+v\n%s", code, rep.Committed, rep.Conflicts, stderr)
	}
	for _, want := range []string{"NOT resolved by --keep-stricter", "iso_low", keepTestPairD, "weaker", "stricter", "--replace"} {
		if !strings.Contains(hi.Detail, want) {
			t.Fatalf("the conflict does not say %q:\n%s", want, hi.Detail)
		}
	}
	// The weaker rule on its own is no problem: the generated rule stays
	// after it.
	if lo, ok := keepTestConflict(rep, "iso_low"); !ok || lo.open() {
		t.Fatalf("iso_low: %+v", lo)
	}
	ks := rep.KeepStricter
	if ks == nil || len(ks.Head) != 0 || len(ks.Unresolved) != 1 || ks.Unresolved[0].UserRule != "iso_high" || ks.Unresolved[0].Generated != keepTestPairD || !strings.Contains(ks.Unresolved[0].Why, "iso_low") {
		b, _ := json.Marshal(ks)
		t.Fatalf("report: %s", b)
	}
	if !strings.Contains(stderr, "NOT resolved") || !strings.Contains(stderr, "iso_high") {
		t.Fatalf("stderr:\n%s", stderr)
	}
}

// What cannot be compared stays a conflict: an expression, a rule that is
// not an error, a rule type keep-stricter does not order.
func TestKicadRulesKeepStricterNotComparable(t *testing.T) {
	for name, rule := range map[string]string{
		"expression":  "(rule \"iso_gap\"\n  (condition \"A.NetName == 'HV' && B.NetName == 'VCC'\")\n  (constraint clearance (min \"2mm + 2mm\")))\n",
		"severity":    "(rule \"iso_gap\"\n  (severity warning)\n  (condition \"A.NetName == 'HV' && B.NetName == 'VCC'\")\n  (constraint clearance (min 4mm)))\n",
		"below-class": "(rule \"iso_gap\"\n  (condition \"A.NetName == 'HV'\")\n  (constraint clearance (min 0.1mm)))\n",
	} {
		f := newRulesFixture(t)
		dru := keepTestWriteDRU(t, f, rule)
		before := rulesFileSHA(t, dru)
		f.o.apply, f.o.keepStricter = true, true
		rep, code, stderr := runRules(t, f.o)
		c, ok := keepTestConflict(rep, "iso_gap")
		if code != 6 || rep.Committed || !ok || !c.open() || !strings.Contains(c.Detail, "NOT resolved by --keep-stricter") || rulesFileSHA(t, dru) != before {
			t.Fatalf("%s: exit %d %+v\n%s", name, code, rep.Conflicts, stderr)
		}
	}
	// track_width: not reordered.
	f := newRulesFixture(t)
	keepTestWriteDRU(t, f, "(rule \"wide_a\"\n  (condition \"A.NetName == 'VCC'\")\n  (constraint track_width (min 2mm)))\n")
	f.o.apply, f.o.keepStricter = true, true
	rep, code, _ := runRules(t, f.o)
	if c, ok := keepTestConflict(rep, "wide_a"); code != 6 || !ok || !c.open() || !strings.Contains(c.Detail, "does not reorder track_width") {
		t.Fatalf("track_width: exit %d %+v", code, rep.Conflicts)
	}
}

// The analysis on its own: two generated rules that may match the same
// items cannot be split across the user's rule when the weaker one stays.
func TestKeepStricterAnalyse(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	nets := map[string][]string{"HV_A": {"N1"}, "LV_B": {"N2"}, "LV_C": {"N2", "N3"}}
	env := keepEnv{nets: func(d string) []string { return nets[d] }, domain: func(d string) map[string]bool {
		m := map[string]bool{"PPD_" + d: true}
		for _, n := range nets[d] {
			m[n] = true
		}
		return m
	}}
	gens := []keepGen{
		{rule: "pcbpilot isolation PPD_HV_A|PPD_LV_B", typ: "clearance", min: 2000000, iso: true, a: "HV_A", b: "LV_B"},
		{rule: "pcbpilot isolation PPD_HV_A|PPD_LV_C", typ: "clearance", min: 1000000, iso: true, a: "HV_A", b: "LV_C"},
		{rule: "pcbpilot edge PPD_HV_A", typ: "edge_clearance", min: 500000, a: "HV_A"},
	}
	user := func(i int, name, cond, typ string, min int64) kicad.DRURule {
		return kicad.DRURule{Name: name, Condition: cond, Index: i, Constraints: []kicad.DRUConstraint{{Type: typ, Min: i64(min)}}}
	}
	// Stricter than the 2 mm pair only, disjoint from LV_C's other net —
	// but N2 is in both LV domains: the 1 mm pair rule may match the same
	// items and has no stricter user rule, so it stays last and the 2 mm
	// one cannot go before the user's rule.
	p := keepStricterAnalyse([]kicad.DRURule{user(0, "iso_gap", "A.hasNetclass('PPD_HV_A') && B.hasNetclass('PPD_LV_B')", "clearance", 3000000)}, gens, env)
	if len(p.rep.Head) != 0 || len(p.rep.Unresolved) != 1 || !strings.Contains(p.rep.Unresolved[0].Why, "PPD_HV_A|PPD_LV_C") || p.verdicts[keepKey{0, 0}].ok {
		t.Fatalf("overlapping generated rules: %+v", p.rep)
	}
	// An edge rule the user's stricter edge rule covers: moved.
	p = keepStricterAnalyse([]kicad.DRURule{user(0, "edge_gap", "A.hasNetclass('PPD_HV_A')", "edge_clearance", 800000)}, gens, env)
	if len(p.rep.Head) != 1 || p.rep.Head[0].HeadRule != "pcbpilot-rules floor edge PPD_HV_A edge_clearance" || len(p.rep.Unresolved) != 0 || !p.verdicts[keepKey{0, 0}].ok || p.verdicts[keepKey{0, 0}].relation != "stricter" {
		t.Fatalf("edge: %+v", p.rep)
	}
	// A condition pcbpilot cannot prove disjoint counts as overlapping.
	p = keepStricterAnalyse([]kicad.DRURule{user(0, "any_gap", "A.Type == 'Track'", "edge_clearance", 100000)}, gens, env)
	if len(p.rep.Head) != 0 || len(p.rep.Tail) != 1 || p.verdicts[keepKey{0, 0}].relation != "weaker" {
		t.Fatalf("unprovable overlap: %+v", p.rep)
	}
	// Equal: nothing to order.
	p = keepStricterAnalyse([]kicad.DRURule{user(0, "edge_gap", "A.hasNetclass('PPD_HV_A')", "edge_clearance", 500000)}, gens, env)
	if len(p.rep.Head) != 0 || len(p.rep.Unresolved) != 0 || p.verdicts[keepKey{0, 0}].relation != "equal" {
		t.Fatalf("equal: %+v", p.rep)
	}
}

// keepHeadBlock is a function of the text and the moves: applied to its own
// result it changes nothing, with no moves it removes the block, and the
// user's lines are never touched.
func TestKeepHeadBlockIdempotent(t *testing.T) {
	user := "# owner note\n(rule \"iso_gap\"\n  (condition \"A.hasNetclass('PPD_HV_A') && B.hasNetclass('PPD_LV_B')\")\n  (constraint clearance (min 4mm)))\n"
	gen := "pcbpilot isolation PPD_HV_A|PPD_LV_B"
	text := "(version 1)\n" + user + kicad.DRURouteBegin + "\n(rule \"" + gen + "\"\n  (condition \"" + isoCondition("PPD_HV_A", "PPD_LV_B") + "\")\n  (constraint clearance (min 2mm)))\n" + kicad.DRURouteEnd + "\n"
	moves := []keepMove{{HeadRule: keepHeadName(gen, "clearance"), Generated: gen, Type: "clearance", MinNm: 2000000, YieldsTo: []keepRef{{Rule: "iso_gap", MinNm: 4000000}}}}
	once, err := keepHeadBlock(text, moves)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(once, user) || strings.Index(once, kicad.DRUHeadEnd) > strings.Index(once, user) || strings.Contains(once, "(rule \""+gen+"\"") {
		t.Fatalf("once:\n%s", once)
	}
	// What bridge netclasses does next: the complete route block again.
	_, _, route, err := kicad.SplitDRU(text)
	if err != nil {
		t.Fatal(err)
	}
	bridged := strings.Replace(once, kicad.DRURouteBegin+"\n"+kicad.DRURouteEnd+"\n", route, 1)
	if bridged == once {
		t.Fatalf("fixture: no emptied route block:\n%s", once)
	}
	twice, err := keepHeadBlock(bridged, moves)
	if err != nil || twice != once {
		t.Fatalf("not idempotent (%v):\n%s\n---\n%s", err, once, twice)
	}
	none, err := keepHeadBlock(bridged, nil)
	if err != nil || none != text {
		t.Fatalf("no moves (%v):\n%s", err, none)
	}
	if _, err := keepHeadBlock(text, []keepMove{{HeadRule: "x", Generated: "pcbpilot isolation PPD_X|PPD_Y", Type: "clearance"}}); err == nil {
		t.Fatal("a move of a rule the route block does not have is not an error")
	}
}

// kicad route re-applies the head block after bridge netclasses re-appended
// the complete route block, and is stale when the user rule changed.
func TestRouteReapplyKeepStricter(t *testing.T) {
	f := newRulesFixture(t)
	user := keepTestRule("iso_gap", "4mm")
	dru := keepTestWriteDRU(t, f, user)
	f.o.apply, f.o.keepStricter = true, true
	if rep, code, stderr := runRules(t, f.o); code != 0 || !rep.Committed {
		t.Fatalf("apply: %d\n%s", code, stderr)
	}
	applied := keepTestRead(t, dru)
	in, err := loadDesignIntent(f.o.intent)
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	r := &kicadRun{o: kicadRouteOpts{pcb: f.o.pcb, intent: f.o.intent, sim: f.o.sim}, in: in, inRaw: []byte(`{}`), stderr: &stderr, summary: map[string]any{}}
	full := &kicad.NetclassFull{}
	full.Domains = map[string][]string{"HVD": {"HV"}, "LV": {"VCC", "GND"}}
	// bridge netclasses: the route block complete again, at the end.
	bridged := strings.Replace(applied, "(rule \""+keepTestPair+"\"\n  (condition \""+isoCondition("PPD_HVD", "PPD_LV")+"\")\n", "(rule \""+keepTestPair+"\"\n  (condition \""+isoCondition("PPD_HVD", "PPD_LV")+"\")\n  (constraint clearance (min 2mm))\n", 1)
	if bridged == applied {
		t.Fatalf("fixture: route rule not found:\n%s", applied)
	}
	classed := f.dir + "/classed.kicad_pcb"
	cdru := f.dir + "/classed.kicad_dru"
	if err := os.WriteFile(cdru, []byte(bridged), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.reapplyKeepStricter(classed, full); err != nil {
		t.Fatal(err)
	}
	got, _ := kicad.ParseDRU(keepTestRead(t, cdru))
	for _, u := range got {
		if u.Name == keepTestPair && (hasConstraint(u, "clearance") || !hasConstraint(u, "creepage")) {
			t.Fatalf("route left the generated clearance behind the user's stricter rule:\n%s", keepTestRead(t, cdru))
		}
	}
	if !strings.Contains(keepTestRead(t, cdru), user) {
		t.Fatal("user text changed")
	}
	// The owner weakened the rule after kicad rules: the head block no
	// longer is what the rules give — stale, never silently reordered.
	if err := os.WriteFile(cdru, []byte(strings.Replace(bridged, "(min 4mm)", "(min 1mm)", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.reapplyKeepStricter(classed, full); err == nil || !strings.Contains(err.Error(), "RULES_STALE") || !strings.Contains(err.Error(), "--keep-stricter") {
		t.Fatalf("stale: %v", err)
	}
	// No head block: route does nothing.
	plain := "(version 1)\n" + user
	if err := os.WriteFile(cdru, []byte(plain), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.reapplyKeepStricter(classed, full); err != nil || keepTestRead(t, cdru) != plain {
		t.Fatalf("no head block: %v", err)
	}
}
