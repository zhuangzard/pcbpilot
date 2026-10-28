package app

// Regression tests for the WP2 review round 1 findings.

import (
	"os"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Finding 1: every power / ground / switch net needs current evidence; an
// evidenced zero passes, a missing value is cannot-check.
func TestPlanRulesWidthsMissingCurrentEveryRole(t *testing.T) {
	in := testIntent(t, `{"nets":{
	 "P":{"role":"power","widthMil":{"outer":10,"inner":10,"min":10}},
	 "G":{"role":"ground","widthMil":{"outer":10,"inner":10,"min":10}},
	 "S":{"role":"switch","widthMil":{"outer":20,"inner":20,"min":20}},
	 "PZ":{"role":"power","currentA":0,"currentSource":"simulated","widthMil":{"outer":10,"inner":10,"min":10}},
	 "GZ":{"role":"ground","widthMil":{"outer":10,"inner":10,"min":10}},
	 "SIG":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6}}}}`)
	cu := rulesCopper{OuterOz: 1, InnerOz: 1, TempRiseC: 10}
	sim := &pcbauto.SimPower{Scenario: "worst", Nets: map[string]*pcbauto.SimNet{"GZ": {CurrentA: 0}}}
	_, bs := planRulesWidths(in, sim, nil, cu)
	for _, n := range []string{"P", "G", "S"} {
		if b := basisOf(bs, n); b.Status != "cannot-check" || !b.NeedsCurrent || b.Width.OuterMil != b.Intent.Outer {
			t.Errorf("%s without current: %+v", n, b)
		}
	}
	for _, n := range []string{"PZ", "GZ"} {
		if b := basisOf(bs, n); b.Status != "pass" || !strings.Contains(strings.Join(b.Basis, " "), "evidenced zero") {
			t.Errorf("%s evidenced zero: %+v", n, b)
		}
	}
	if b := basisOf(bs, "SIG"); b.Status != "pass" || b.NeedsCurrent {
		t.Errorf("signal: %+v", b)
	}
}

// Finding 3: an impedance net whose current needs more only on the inner
// layers is incompatible too, and nothing is raised on either layer.
func TestPlanRulesWidthsInnerOnlyConflict(t *testing.T) {
	in := testIntent(t, `{"nets":{"D":{"role":"diff","impedanceOhm":90,"diffPair":"E","widthMil":{"outer":12,"inner":6,"min":6}},
	 "E":{"role":"diff","impedanceOhm":90,"diffPair":"D","widthMil":{"outer":12,"inner":6,"min":6}}}}`)
	cu := rulesCopper{OuterOz: 1, InnerOz: 0.5, TempRiseC: 10}
	sim := &pcbauto.SimPower{Scenario: "worst", Nets: map[string]*pcbauto.SimNet{"D": {CurrentA: 0.5}}}
	widths, bs := planRulesWidths(in, sim, nil, cu)
	b := basisOf(bs, "D")
	if b.CurrentWidth == nil || b.CurrentWidth.OuterMil > 12 || b.CurrentWidth.InnerMil <= 6 {
		t.Fatalf("fixture: %+v", b.CurrentWidth)
	}
	if b.Status != "fail" || !strings.Contains(b.Detail, "incompatible") || b.Width != (rulesWidth{OuterMil: 12, InnerMil: 6}) || widths["D"] != (rulesWidth{}) {
		t.Fatalf("inner-only conflict: %+v", b)
	}
}

// Finding 4: property and wildcard conditions overlap pcbpilot's nets and
// refuse the apply; a provably disjoint literal does not.
func TestKicadRulesUserRuleOverlap(t *testing.T) {
	names := map[string]bool{"VCC": true, "PP1_W10": true}
	for cond, want := range map[string]bool{
		"":                                    false,
		"A.Type == 'Track'":                   false,
		"A.NetName == 'V*'":                   false,
		"A.NetName == 'VCC'":                  false,
		"A.NetName == 'X'":                    true,
		"A.NetName == 'X' && A.Type == 'Via'": true,
		"A.NetName == 'X' || A.Type == 'Via'": false,
		"(A.hasNetclass('USER') && B.NetName == 'Y')": true,
		"A.hasNetclass('PP1_W10')":                    false,
		"A.insideArea('X')":                           false,
	} {
		if got := druDisjoint(cond, names); got != want {
			t.Errorf("druDisjoint(%q) = %v, want %v", cond, got, want)
		}
	}
	f := newRulesFixture(t)
	dru := boardSibling(f.o.pcb, ".kicad_dru")
	_ = os.WriteFile(dru, []byte(`(version 1)
(rule "props"
  (condition "A.Type == 'Track'")
  (constraint track_width (min 2mm)))
(rule "wild"
  (condition "A.NetName == 'V*'")
  (constraint track_width (min 2mm)))
(rule "allclr"
  (constraint clearance (min 0.1mm)))
(rule "elsewhere"
  (condition "A.NetName == 'X' && A.Type == 'Track'")
  (constraint track_width (min 2mm)))
`), 0o644)
	f.o.apply = true
	rep, code, _ := runRules(t, f.o)
	got := map[string]string{}
	for _, c := range rep.Conflicts {
		got[c.Name] = c.Kind
	}
	if code != 6 || rep.Committed || got["props"] != "dru-weakened" || got["wild"] != "dru-weakened" || got["allclr"] != "dru-weakened" || got["elsewhere"] != "" {
		t.Fatalf("exit %d %+v", code, rep.Conflicts)
	}
}

// Finding 5: probes flagged with and without pcbpilot's rules prove
// nothing; with every floor at the board minimum and the .kicad_dru
// ignored, the result is cannot-check, never pass.
func TestKicadRulesProbesNeedAttribution(t *testing.T) {
	f := newRulesFixture(t)
	// Floors at the fake board minimum (0.15 mm), no insulation pair.
	in := `{"nets":{"SIG":{"role":"signal","widthMil":{"outer":5.905512,"inner":5.905512,"min":5.905512},"clearanceMil":6}}}`
	_ = os.WriteFile(f.o.intent, []byte(in), 0o644)
	f.fake.ignoreDRU = true
	f.o.plan = true
	rep, code, _ := runRules(t, f.o)
	c := checkOf(rep, "staged-readback")
	if code != 5 || c.Status != gate.CannotCheck || !strings.Contains(strings.Join(c.Info, " "), "no DRC probe is attributable") {
		t.Fatalf("exit %d %+v", code, c)
	}
	// The same board with the rules honoured is still not proven (the
	// floor is the board minimum): cannot-check.
	f.fake.ignoreDRU = false
	if rep, code, _ = runRules(t, f.o); code != 5 || checkOf(rep, "staged-readback").Status != gate.CannotCheck {
		t.Fatalf("floors at the board minimum: exit %d %+v", code, checkOf(rep, "staged-readback"))
	}
	// A floor above the board minimum is attributable: pass.
	_ = os.WriteFile(f.o.intent, []byte(strings.ReplaceAll(in, "5.905512", "8")), 0o644)
	if rep, code, _ = runRules(t, f.o); code != 0 || checkOf(rep, "staged-readback").Status != gate.Pass {
		t.Fatalf("floor above the minimum: exit %d %+v", code, checkOf(rep, "staged-readback"))
	}
}

// Finding 6: an isolation rule counts only with the exact pair condition,
// every requested constraint and no later rule overriding it.
func TestKicadRulesIsolationRuleValidated(t *testing.T) {
	for name, edit := range map[string]func(string) string{
		"creepage deleted": func(s string) string {
			return strings.Replace(s, "\n  (constraint creepage (min 3mm))", "", 1)
		},
		"nonmatching condition": func(s string) string {
			return strings.Replace(s, "B.hasNetclass('PPD_LV')) ||", "B.hasNetclass('PPD_XX')) ||", 1)
		},
		"later override": func(s string) string {
			return s + "(rule \"late\"\n  (condition \"A.Type == 'Track'\")\n  (constraint clearance (min 0.1mm)))\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newRulesFixture(t)
			a := f.o
			a.apply = true
			if _, code, se := runRules(t, a); code != 0 {
				t.Fatalf("apply: %d %s", code, se)
			}
			dru := boardSibling(f.o.pcb, ".kicad_dru")
			b, _ := os.ReadFile(dru)
			nb := edit(string(b))
			if nb == string(b) {
				t.Fatal("the edit changed nothing")
			}
			_ = os.WriteFile(dru, []byte(nb), 0o644)
			f.o.check, f.o.stage = true, "placed"
			rep, code, _ := runRules(t, f.o)
			c := checkOf(rep, "effective-rules")
			if code != 6 || c.Status != gate.Fail || !strings.Contains(strings.Join(c.Items, "\n"), "pair HVD|LV") {
				t.Fatalf("exit %d %+v", code, c)
			}
		})
	}
}

// Finding 7: a net carried only by pours (or a pour island) is
// cannot-check for copper widths, never pass.
func TestKicadRulesZoneOnlyNetCannotCheck(t *testing.T) {
	for name, poured := range map[string]string{
		"zone-only":   `[{"net":"HV","layer":1,"fills":[{"source":[["M",380,180],["L",520,180],["L",520,220],["L",380,220],["Z"]]}]}]`,
		"zone island": `[{"net":"HV","layer":2,"fills":[{"source":[["M",1500,800],["L",1600,800],["L",1600,900],["Z"]]}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newRulesFixture(t)
			a := f.o
			a.apply = true
			if _, code, se := runRules(t, a); code != 0 {
				t.Fatalf("apply: %d %s", code, se)
			}
			snap := strings.Replace(rulesTestSnap, `{"primitiveId":"t-hv","net":"HV","layer":1,"startX":400,"startY":200,"endX":500,"endY":200,"lineWidth":30},`, "", 1)
			snap = strings.Replace(snap, `"poured":[]`, `"poured":`+poured, 1)
			if snap == rulesTestSnap {
				t.Fatal("fixture unchanged")
			}
			f.fake.snap = []byte(snap)
			f.o.check = true
			rep, _, _ := runRules(t, f.o)
			c := checkOf(rep, "copper-widths")
			if c.Status == gate.Pass || !strings.Contains(strings.Join(c.Items, "\n"), "HV: zone-only") {
				t.Fatalf("%+v", c)
			}
		})
	}
}

// Finding 8: the expected floor comes from the class values, and a weaker
// route rule placed last is reported, not accepted.
func TestKicadRulesRouteRuleCannotWeakenFloor(t *testing.T) {
	q := kicad.ClassNM{Track: 508000, Inner: 600000}
	if expectedFloor(q, "inner") != 600000 || expectedFloor(q, "outer") != 508000 {
		t.Fatal(expectedFloor(q, "inner"))
	}
	if expectedFloor(kicad.ClassNM{Track: 508000, Inner: 600000, Min: 300000}, "inner") != 300000 {
		t.Fatal("the intent minimum is the floor")
	}
	block := rulesBlockText(rulesBlockRules([]kicad.NetclassClassNM{{NetClass: kicad.NetClass{Name: "PP2_W20"}, NM: q}}, 4), "x", kicad.SizeSet{})
	route := kicad.DRURouteBegin + "\n(rule \"pcbpilot PP2_W20 inner\"\n  (layer inner)\n  (condition \"A.hasNetclass('PP2_W20')\")\n  (constraint track_width (min 0.15mm) (opt 0.6mm)))\n" + kicad.DRURouteEnd + "\n"
	// The wrong order (route block last) must resolve to the weaker value.
	rules, err := kicad.ParseDRU("(version 1)\n" + block + route)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := rulesTrackFloor(rules, "PP2_W20", "inner"); v >= expectedFloor(q, "inner") {
		t.Fatalf("route block last should weaken the floor in this fixture: %d", v)
	}
	text, _ := placeRulesBlock("(version 1)\n"+block+route, block)
	rules, _ = kicad.ParseDRU(text)
	if v, _ := rulesTrackFloor(rules, "PP2_W20", "inner"); v != expectedFloor(q, "inner") {
		t.Fatalf("placed block: %d", v)
	}
}
