package app

// Regression tests for the WP2 review round 5 findings.

import (
	"os"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Finding 1: a possibly matching stronger rule never erases a definite
// weaker one; a possibly matching weaker one is cannot-check.
func TestKicadRulesPossibleMatchNeverErasesWeaker(t *testing.T) {
	for name, tc := range map[string]struct {
		extra  string
		status gate.Status
		item   string
	}{
		"definite weaker then possible stronger": {"(rule \"vcc thin\"\n  (layer B.Cu)\n  (condition \"A.NetName == 'VCC'\")\n  (constraint track_width (min 0.01mm)))\n" +
			"(rule \"via wide\"\n  (layer B.Cu)\n  (condition \"A.Type == 'Via'\")\n  (constraint track_width (min 2mm)))\n", gate.Fail, `rule "vcc thin") on B.Cu`},
		"possible weaker": {"(rule \"tracks thin\"\n  (layer B.Cu)\n  (condition \"A.Type == 'Track'\")\n  (constraint track_width (min 0.01mm)))\n", gate.CannotCheck, `"tracks thin" may lower track_width on B.Cu`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRulesFixture(t)
			a := f.o
			a.apply = true
			if rep, code, se := runRules(t, a); code != 0 {
				t.Fatalf("apply: %d %+v %s", code, rep.Errors, se)
			}
			dru := boardSibling(f.o.pcb, ".kicad_dru")
			b, _ := os.ReadFile(dru)
			_ = os.WriteFile(dru, append(b, []byte(tc.extra)...), 0o644)
			c := f.o
			c.check, c.stage = true, "placed"
			rep, _, _ := runRules(t, c)
			e := checkOf(rep, "effective-rules")
			all := strings.Join(append(append([]string{}, e.Items...), e.Info...), "\n")
			if e.Status != tc.status || !strings.Contains(all, tc.item) {
				t.Fatalf("%s: %+v", e.Status, e)
			}
		})
	}
}

// Finding 2: rules keyed to the PP class this plan assigns are part of the
// net's identity in conflicts and in the check.
func TestKicadRulesNewPPClassIdentity(t *testing.T) {
	edgeIntent := strings.Replace(rulesTestIntent, `"copper":`, rulesEdgeIntent+`"copper":`, 1)
	classes := func(t *testing.T) (vcc, hv string) {
		f := newRulesFixture(t)
		_ = os.WriteFile(f.o.intent, []byte(edgeIntent), 0o644)
		f.o.plan = true
		rep, _, _ := runRules(t, f.o)
		return netOf(rep, "VCC").Class, netOf(rep, "HV").Class
	}
	vcc, hv := classes(t)
	if vcc == "" || hv == "" || vcc == hv {
		t.Fatalf("classes %q %q", vcc, hv)
	}
	for name, rule := range map[string]string{
		"creepage":       "(rule \"u\"\n  (condition \"A.hasNetclass('" + vcc + "')\")\n  (constraint creepage (min 5mm)))\n",
		"edge_clearance": "(rule \"u\"\n  (condition \"A.hasNetclass('" + hv + "')\")\n  (constraint edge_clearance (min 9mm)))\n",
		"clearance":      "(rule \"u\"\n  (condition \"A.hasNetclass('" + vcc + "') && B.hasNetclass('" + hv + "')\")\n  (constraint clearance (min 0.1mm)))\n",
	} {
		t.Run("apply "+name, func(t *testing.T) {
			f := newRulesFixture(t)
			_ = os.WriteFile(f.o.intent, []byte(edgeIntent), 0o644)
			_ = os.WriteFile(boardSibling(f.o.pcb, ".kicad_dru"), []byte("(version 1)\n"+rule), 0o644)
			a := f.o
			a.apply = true
			rep, code, _ := runRules(t, a)
			if code != 6 || rep.Committed || len(rep.Conflicts) != 1 || rep.Conflicts[0].Name != "u" {
				t.Fatalf("exit %d %+v", code, rep.Conflicts)
			}
		})
	}
	for name, tc := range map[string]struct{ rule, item string }{
		"later creepage":  {"(rule \"late\"\n  (condition \"A.hasNetclass('" + vcc + "') && B.hasNetclass('" + hv + "')\")\n  (constraint creepage (min 0.1mm)))\n", "may set creepage"},
		"later edge":      {"(rule \"late\"\n  (condition \"A.hasNetclass('" + hv + "')\")\n  (constraint edge_clearance (min 0.1mm)))\n", "may lower edge_clearance"},
		"later clearance": {"(rule \"late\"\n  (condition \"A.hasNetclass('" + vcc + "') && B.hasNetclass('" + hv + "')\")\n  (constraint clearance (min 0.1mm)))\n", "may lower its clearance"},
	} {
		t.Run("check "+name, func(t *testing.T) {
			f := newRulesFixture(t)
			_ = os.WriteFile(f.o.intent, []byte(edgeIntent), 0o644)
			a := f.o
			a.apply = true
			if rep, code, se := runRules(t, a); code != 0 {
				t.Fatalf("apply: %d %+v %s", code, rep.Errors, se)
			}
			dru := boardSibling(f.o.pcb, ".kicad_dru")
			b, _ := os.ReadFile(dru)
			_ = os.WriteFile(dru, append(b, []byte(tc.rule)...), 0o644)
			c := f.o
			c.check, c.stage = true, "placed"
			rep, _, _ := runRules(t, c)
			e := checkOf(rep, "effective-rules")
			if e.Status != gate.Fail || !strings.Contains(strings.Join(e.Items, "\n"), tc.item) {
				t.Fatalf("%+v", e)
			}
		})
	}
}

// Finding 3: only an explicit 0 is an evidenced zero current.
func TestPlanRulesWidthsMissingIsNotZero(t *testing.T) {
	cu := rulesCopper{OuterOz: 1, InnerOz: 1, TempRiseC: 10}
	in := testIntent(t, `{"nets":{
	 "A":{"role":"power","currentSource":"simulated","widthMil":{"outer":10,"inner":10,"min":10}},
	 "B":{"role":"power","currentSource":"simulated","currentA":0,"widthMil":{"outer":10,"inner":10,"min":10}},
	 "C":{"role":"power","widthMil":{"outer":10,"inner":10,"min":10}},
	 "D":{"role":"power","widthMil":{"outer":10,"inner":10,"min":10}}}}`)
	raw := []byte(`{"schemaVersion":1,"results":[{"scenario":"worst","nets":{"C":{},"D":{"currentA":0}}}]}`)
	sp, err := pcbauto.ParseSim(raw)
	if err != nil {
		t.Fatal(err)
	}
	has := simEvidence(raw)
	if has["C"] || !has["D"] {
		t.Fatalf("evidence %v", has)
	}
	_, bs := planRulesWidths(in, sp, nil, cu, simCurrents(raw))
	for net, want := range map[string]string{"A": "cannot-check", "B": "pass", "C": "cannot-check", "D": "pass"} {
		if b := basisOf(bs, net); b.Status != want {
			t.Errorf("%s: %s, want %s (%+v)", net, b.Status, want, b)
		}
	}
}
