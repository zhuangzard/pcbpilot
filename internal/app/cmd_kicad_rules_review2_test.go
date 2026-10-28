package app

// Regression tests for the WP2 review round 2 findings.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// Finding 1: for two-item constraints an unplanned operand proves nothing.
func TestDRUDisjointBinary(t *testing.T) {
	ours := map[string]bool{"VCC": true, "PP1_W10": true}
	for _, c := range []struct {
		cond          string
		unary, binary bool
	}{
		// Unary (one item, A): a B term never matches, so a B literal
		// outside pins the rule off; binary: VCC–OTHER is governed.
		{"A.NetName == 'VCC' && B.NetName == 'OTHER'", true, false},
		{"A.NetName == 'OTHER'", true, false},
		{"A.NetName == 'OTHER' && B.NetName == 'X'", true, true},
		{"(A.NetName == 'OTHER' && B.NetName == 'X') || A.NetName == 'Y'", true, false},
		{"A.hasNetclass('USER') && B.hasNetclass('PP1_W10')", true, false},
	} {
		if got := druDisjointFor(c.cond, ours, false); got != c.unary {
			t.Errorf("unary %q = %v", c.cond, got)
		}
		if got := druDisjointFor(c.cond, ours, true); got != c.binary {
			t.Errorf("binary %q = %v", c.cond, got)
		}
	}
	// A pair rule for another domain pair never governs this pair.
	sa, sb := map[string]bool{"PPD_HV": true, "HV": true}, map[string]bool{"PPD_LV": true, "VCC": true}
	other := "(A.hasNetclass('PPD_HV') && B.hasNetclass('PPD_MID')) || (A.hasNetclass('PPD_MID') && B.hasNetclass('PPD_HV'))"
	if !druDisjointPair(other, sa, sb) {
		t.Error("HV|MID governs HV|LV")
	}
	if druDisjointPair(isoCondition("PPD_HV", "PPD_LV"), sa, sb) {
		t.Error("HV|LV is disjoint from itself")
	}

	f := newRulesFixture(t)
	for name, rule := range map[string]string{
		"two-sided": "(rule \"vcc-other\"\n  (condition \"A.NetName == 'VCC' && B.NetName == 'OTHER'\")\n  (constraint clearance (min 0.1mm)))\n",
		"one-sided": "(rule \"other\"\n  (condition \"A.NetName == 'OTHER'\")\n  (constraint clearance (min 0.1mm)))\n",
	} {
		t.Run(name, func(t *testing.T) {
			_ = os.WriteFile(boardSibling(f.o.pcb, ".kicad_dru"), []byte("(version 1)\n"+rule), 0o644)
			a := f.o
			a.apply = true
			rep, code, _ := runRules(t, a)
			if code != 6 || rep.Committed || len(rep.Conflicts) == 0 || rep.Conflicts[0].Kind != "dru-weakened" {
				t.Fatalf("exit %d %+v", code, rep.Conflicts)
			}
		})
	}
}

const rulesEdgeIntent = `"edge":{"outerMil":20,"innerMil":30,"byDomain":{"HVD":{"mil":80}}},`

// Finding 2: --check resolves ordinary clearance and domain edge rules
// through KiCad's last-match semantics.
func TestKicadRulesCheckClearanceAndEdge(t *testing.T) {
	for name, tc := range map[string]struct {
		edit   func(string) string
		status gate.Status
		item   string
	}{
		"later low clearance": {func(s string) string {
			return s + "(rule \"low\"\n  (condition \"A.NetName == 'VCC'\")\n  (constraint clearance (min 0.01mm)))\n"
		}, gate.Fail, "may lower its clearance"},
		"unparsable clearance": {func(s string) string {
			return s + "(rule \"expr\"\n  (condition \"A.NetName == 'SIG'\")\n  (constraint clearance (min (1mm + 1mm))))\n"
		}, gate.CannotCheck, "cannot compare"},
		"edge rule deleted": {func(s string) string {
			i := strings.Index(s, "(rule \"pcbpilot edge PPD_HVD\"")
			j := strings.Index(s[i:], ")))") + i + 4
			return s[:i] + s[j:]
		}, gate.Fail, "no edge rule"},
		"edge rule weakened": {func(s string) string {
			return strings.Replace(s, "(constraint edge_clearance (min 2.032mm))", "(constraint edge_clearance (min 0.5mm))", 1)
		}, gate.Fail, "is not edge_clearance"},
		"later edge override": {func(s string) string {
			return s + "(rule \"edge-all\"\n  (constraint edge_clearance (min 0.3mm)))\n"
		}, gate.Fail, "may lower edge_clearance"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRulesFixture(t)
			_ = os.WriteFile(f.o.intent, []byte(strings.Replace(rulesTestIntent, `"copper":`, rulesEdgeIntent+`"copper":`, 1)), 0o644)
			a := f.o
			a.apply = true
			rep, code, se := runRules(t, a)
			if code != 0 {
				t.Fatalf("apply: %d %+v %s", code, rep.Errors, se)
			}
			// The clearance probes ran against the unplanned net.
			if !strings.Contains(strings.Join(rep.Checks[len(rep.Checks)-1].Info, " ")+checkOf(rep, "readback").Detail, "attributable") {
				t.Fatalf("readback: %+v", checkOf(rep, "readback"))
			}
			dru := boardSibling(f.o.pcb, ".kicad_dru")
			b, _ := os.ReadFile(dru)
			if !strings.Contains(string(b), "pcbpilot edge PPD_HVD") {
				t.Fatalf("no edge rule written:\n%s", b)
			}
			// Unedited: pass.
			c := f.o
			c.check, c.stage = true, "placed"
			if rep, _, _ := runRules(t, c); checkOf(rep, "effective-rules").Status != gate.Pass {
				t.Fatalf("unedited: %+v", checkOf(rep, "effective-rules"))
			}
			nb := tc.edit(string(b))
			if nb == string(b) {
				t.Fatal("the edit changed nothing")
			}
			_ = os.WriteFile(dru, []byte(nb), 0o644)
			rep, _, _ = runRules(t, c)
			e := checkOf(rep, "effective-rules")
			all := strings.Join(append(append([]string{}, e.Items...), e.Info...), "\n")
			if e.Status != tc.status || !strings.Contains(all, tc.item) {
				t.Fatalf("%s: %+v", e.Status, e)
			}
		})
	}
}

// Finding 2: clearance probes with the negative control prove the class
// clearance (the control drops pcbpilot's classes).
func TestKicadRulesClearanceProbeAttributed(t *testing.T) {
	f := newRulesFixture(t)
	f.o.plan = true
	rep, code, _ := runRules(t, f.o)
	if code != 0 {
		t.Fatalf("exit %d %+v", code, rep.Checks)
	}
	aux := 0
	for _, ts := range f.fake.probes {
		for _, tr := range ts {
			if tr.Net == "AUX" {
				aux++
			}
		}
	}
	if aux == 0 {
		t.Fatal("no clearance probe against the unplanned net")
	}
}

// Finding 3: a user inner-layer width rule is compared with the inner floor.
func TestKicadRulesTrackConflictPerLayer(t *testing.T) {
	f := newRulesFixture(t)
	f.fake.layers = 4
	_ = os.WriteFile(f.o.intent, []byte(`{"nets":{"SIG":{"role":"signal","widthMil":{"outer":20,"inner":6},"clearanceMil":6}}}`), 0o644)
	_ = os.WriteFile(boardSibling(f.o.pcb, ".kicad_dru"), []byte("(version 1)\n(rule \"inner10\"\n  (layer inner)\n  (condition \"A.NetName == 'SIG'\")\n  (constraint track_width (min 10mil)))\n"), 0o644)
	a := f.o
	a.apply = true
	rep, code, _ := runRules(t, a)
	if code != 6 || rep.Committed || len(rep.Conflicts) != 1 || rep.Conflicts[0].Name != "inner10" || !strings.Contains(rep.Conflicts[0].Detail, "inner") {
		t.Fatalf("exit %d %+v", code, rep.Conflicts)
	}
	a.replace = []string{"inner10"}
	if rep, code, _ = runRules(t, a); code != 0 || !rep.Committed {
		t.Fatalf("with --replace: exit %d %+v", code, rep.Errors)
	}
}

// Finding 4: ownership comes from the block spans, not the rule name.
func TestDRUOwnershipBySpan(t *testing.T) {
	text := "(version 1)\n(rule \"pcbpilot-rules PP1_W10 outer\"\n  (condition \"A.hasNetclass('PP1_W10')\")\n  (constraint track_width (min 0.01mm)))\n" +
		kicad.DRURulesBegin + "\n(rule \"pcbpilot-rules PP1_W10 outer\"\n  (layer outer)\n  (condition \"A.hasNetclass('PP1_W10')\")\n  (constraint track_width (min 0.3mm)))\n" + kicad.DRURulesEnd + "\n"
	rules, err := kicad.ParseDRU(text)
	if err != nil || len(rules) != 2 || rules[0].Block != "" || rules[1].Block != "rules" {
		t.Fatalf("%v %+v", err, rules)
	}
	f := newRulesFixture(t)
	_ = os.WriteFile(boardSibling(f.o.pcb, ".kicad_dru"), []byte(text), 0o644)
	a := f.o
	a.apply = true
	rep, code, _ := runRules(t, a)
	if code != 6 || rep.Committed || len(rep.Conflicts) == 0 || rep.Conflicts[0].Kind != "dru-name" {
		t.Fatalf("exit %d %+v", code, rep.Conflicts)
	}
}

// Finding 5: the derivation uses only the bytes read (and hashed) once.
func TestKicadRulesUsesCapturedBytes(t *testing.T) {
	f := newRulesFixture(t)
	req := filepath.Join(f.dir, "currents.json")
	_ = os.WriteFile(req, []byte(`{"nets":{}}`), 0o644)
	f.o.requirements = []string{req}
	f.o.plan = true
	f.o.afterRead = func() {
		_ = os.WriteFile(req, []byte(`{"nets":{"SIG":{"peakA":9}}}`), 0o644)
		_ = os.WriteFile(f.o.intent, []byte(strings.Replace(rulesTestIntent, `"SIG":{"role":"signal","widthMil":{"outer":6`, `"SIG":{"role":"signal","widthMil":{"outer":60`, 1)), 0o644)
	}
	rep, code, _ := runRules(t, f.o)
	if s := netOf(rep, "SIG"); s.Current != nil || s.Intent.Outer != 6 {
		t.Fatalf("the derivation read changed files: %+v", s)
	}
	if code != 4 || rep.Errors[len(rep.Errors)-1].Code != "E_FILE_CHANGED" {
		t.Fatalf("exit %d %+v", code, rep.Errors)
	}
	if d, _, err := declaredCurrentsFrom([]string{"x.json"}, func(string) ([]byte, error) { return []byte(`{"nets":{"A":{"rmsA":2}}}`), nil }); err != nil || d["A"].RmsA != 2 {
		t.Fatalf("%v %+v", err, d)
	}
}

// Finding 6: Default's explicit values count like any user class.
func TestKicadRulesDefaultClassConflict(t *testing.T) {
	f := newRulesFixture(t)
	p := rulesFakeDefault()
	p.Default.Track = 300000 // explicit: KiCad's own default is 0.2 mm
	_ = rulesFakeWrite(boardSibling(f.o.pcb, ".kicad_pro"), p)
	a := f.o
	a.apply = true
	rep, code, _ := runRules(t, a)
	found := false
	for _, c := range rep.Conflicts {
		found = found || c.Name == "Default" && c.Kind == "class-override" && strings.Contains(c.Detail, "VCC track")
	}
	if code != 6 || rep.Committed || !found {
		t.Fatalf("exit %d %+v", code, rep.Conflicts)
	}
	a.replace = []string{"Default"}
	if rep, code, _ = runRules(t, a); code != 0 {
		t.Fatalf("with --replace Default: exit %d %+v", code, rep.Errors)
	}
	// KiCad's built-in values are not the project's: no conflict.
	f = newRulesFixture(t)
	a = f.o
	a.apply = true
	if rep, code, _ = runRules(t, a); code != 0 || len(rep.Conflicts) != 0 {
		t.Fatalf("built-in Default: exit %d %+v", code, rep.Conflicts)
	}
}
