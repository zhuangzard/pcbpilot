package app

// Regression tests for the WP2 review round 6 finding: user track_width
// maximums are carried, never dropped.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func applyWithDRU(t *testing.T, intent, dru string, replace ...string) (*rulesFixture, *rulesReport, int) {
	t.Helper()
	f := newRulesFixture(t)
	if intent != "" {
		_ = os.WriteFile(f.o.intent, []byte(intent), 0o644)
	}
	_ = os.WriteFile(boardSibling(f.o.pcb, ".kicad_dru"), []byte("(version 1)\n"+dru), 0o644)
	a := f.o
	a.apply, a.replace = true, replace
	rep, code, _ := runRules(t, a)
	return f, rep, code
}

func blockRuleFor(rep *rulesReport, net string) rulesBlockRule {
	cls := netOf(rep, net).Class
	for _, r := range rep.DRURules {
		if r.Class == cls && r.Layer == "outer" {
			return r
		}
	}
	return rulesBlockRule{}
}

func TestKicadRulesUserMaxCarried(t *testing.T) {
	for name, rule := range map[string]string{
		"min+max":  "(rule \"imp\"\n  (condition \"A.NetName == 'SIG'\")\n  (constraint track_width (min 0.15mm) (max 0.25mm)))\n",
		"max only": "(rule \"imp\"\n  (condition \"A.NetName == 'SIG'\")\n  (constraint track_width (max 0.25mm)))\n",
	} {
		t.Run(name, func(t *testing.T) {
			f, rep, code := applyWithDRU(t, "", rule)
			if code != 0 || !rep.Committed {
				t.Fatalf("apply: exit %d %+v %+v", code, rep.Errors, rep.Conflicts)
			}
			if br := blockRuleFor(rep, "SIG"); br.MaxNm != 250000 || br.MaxFrom != "imp" {
				t.Fatalf("SIG's owned rule: %+v", br)
			}
			b, _ := os.ReadFile(boardSibling(f.o.pcb, ".kicad_dru"))
			if !strings.Contains(string(b), "(max 0.25mm)") {
				t.Fatalf("dru:\n%s", b)
			}
			// --check: the max is in effect; dropping it from the block fails.
			c := f.o
			c.check, c.stage = true, "placed"
			if rep, _, _ := runRules(t, c); checkOf(rep, "effective-rules").Status != gate.Pass {
				t.Fatalf("check: %+v", checkOf(rep, "effective-rules"))
			}
			// Drop the carried max from the owned rule (the last occurrence;
			// the user's own rule comes first).
			i := strings.LastIndex(string(b), " (max 0.25mm)")
			_ = os.WriteFile(boardSibling(f.o.pcb, ".kicad_dru"), []byte(string(b)[:i]+string(b)[i+len(" (max 0.25mm)"):]), 0o644)
			rep, _, _ = runRules(t, c)
			if e := checkOf(rep, "effective-rules"); e.Status != gate.Fail || !strings.Contains(strings.Join(e.Items, "\n"), "is not in effect") {
				t.Fatalf("max dropped: %+v", e)
			}
		})
	}
}

func TestKicadRulesPlanAboveUserMax(t *testing.T) {
	rule := "(rule \"vcc narrow\"\n  (condition \"A.NetName == 'VCC'\")\n  (constraint track_width (max 0.25mm)))\n"
	_, rep, code := applyWithDRU(t, "", rule)
	if code != 6 || rep.Committed || len(rep.Conflicts) != 1 || rep.Conflicts[0].Name != "vcc narrow" || !strings.Contains(rep.Conflicts[0].Detail, "incompatible") {
		t.Fatalf("exit %d %+v", code, rep.Conflicts)
	}
	f, rep, code := applyWithDRU(t, "", rule, "vcc narrow")
	if code != 0 || !rep.Committed || blockRuleFor(rep, "VCC").MaxNm != 0 {
		t.Fatalf("--replace: exit %d %+v", code, rep.Errors)
	}
	if b, _ := os.ReadFile(boardSibling(f.o.pcb, ".kicad_dru")); strings.Contains(string(b), "vcc narrow") {
		t.Fatal("the replaced rule is still there")
	}
	// An impedance net under a smaller max: incompatible requirement (R8).
	in := strings.Replace(rulesTestIntent, `"SIG":{"role":"signal"`, `"SIG":{"role":"signal","impedanceOhm":50`, 1)
	_, rep, code = applyWithDRU(t, in, "(rule \"z0\"\n  (condition \"A.NetName == 'SIG'\")\n  (constraint track_width (max 0.1mm)))\n")
	if code != 6 || checkOf(rep, "plan-coverage").Status != gate.Fail || netOf(rep, "SIG").Status != "fail" {
		t.Fatalf("impedance: exit %d %+v %+v", code, checkOf(rep, "plan-coverage"), netOf(rep, "SIG"))
	}
}

func TestKicadRulesCheckOversizedSegment(t *testing.T) {
	// SIG's routed track is 10 mil = 0.254 mm, the carried max 0.25 mm.
	f, rep, code := applyWithDRU(t, "", "(rule \"imp\"\n  (condition \"A.NetName == 'SIG'\")\n  (constraint track_width (min 0.15mm) (max 0.25mm)))\n")
	if code != 0 {
		t.Fatalf("apply: %d %+v", code, rep.Errors)
	}
	c := f.o
	c.check = true
	rep, _, _ = runRules(t, c)
	cw := checkOf(rep, "copper-widths")
	if cw.Status != gate.Fail || !strings.Contains(strings.Join(cw.Items, "\n"), "t-sig") || !strings.Contains(strings.Join(cw.Items, "\n"), "wider than the track_width max 0.25mm") {
		t.Fatalf("%+v", cw)
	}
}

// Route recomputes the carried maximums: the block with a carried max
// verifies only with the same maxes.
func TestVerifyRulesBlockWithMax(t *testing.T) {
	classes := []kicad.NetclassClassNM{{NetClass: kicad.NetClass{Name: "PP1_W7p87", Nets: []string{"SIG"}}, NM: kicad.ClassNM{Track: 200000, Inner: 200000, Min: 152400}}}
	mx := trackMaxes{"PP1_W7p87": {"outer": {Nm: 250000, Rule: "imp"}}}
	sha := strings.Repeat("cd", 32)
	pcb := filepath.Join(t.TempDir(), "b.kicad_pcb")
	text, _ := placeRulesBlock("(version 1)\n", rulesBlockText(rulesBlockRules(classes, 2, mx), sha, kicad.SizeSet{}))
	_ = os.WriteFile(boardSibling(pcb, ".kicad_dru"), []byte(text), 0o644)
	if err := verifyRulesBlock(pcb, classes, 2, mx); err != nil {
		t.Fatal(err)
	}
	if err := verifyRulesBlock(pcb, classes, 2); err == nil {
		t.Fatal("a block whose max route cannot recompute must be stale")
	}
	rules, _ := kicad.ParseDRU("(version 1)\n(rule \"imp\"\n  (condition \"A.NetName == 'SIG'\")\n  (constraint track_width (max 0.25mm)))\n")
	got, bad := trackMaxCarry(rules, classes, 2, func(n string) map[string]bool { return map[string]bool{n: true, "PP1_W7p87": true} }, nil)
	if len(bad) != 0 || got.get("PP1_W7p87", "outer").Nm != 250000 {
		t.Fatalf("%+v %+v", got, bad)
	}
}

// Round 7: --replace cannot resolve R8 — the impedance net under a smaller
// user max fails although the rule is replaced, and nothing is written.
func TestKicadRulesImpedanceMaxNotReplaceable(t *testing.T) {
	in := strings.Replace(rulesTestIntent, `"SIG":{"role":"signal"`, `"SIG":{"role":"signal","impedanceOhm":50`, 1)
	f, rep, code := applyWithDRU(t, in, "(rule \"z0\"\n  (condition \"A.NetName == 'SIG'\")\n  (constraint track_width (max 0.1mm)))\n", "z0")
	if code != 6 || rep.Committed || checkOf(rep, "plan-coverage").Status != gate.Fail || netOf(rep, "SIG").Status != "fail" {
		t.Fatalf("exit %d %+v %+v", code, checkOf(rep, "plan-coverage"), netOf(rep, "SIG"))
	}
	if b, _ := os.ReadFile(boardSibling(f.o.pcb, ".kicad_dru")); !strings.Contains(string(b), "\"z0\"") || strings.Contains(string(b), kicad.DRURulesBegin) {
		t.Fatalf("something was written:\n%s", b)
	}
}

// Round 7: a user max below the plan added after the apply stops route
// with RULES_STALE naming the rule.
func TestRouteCheckRulesBlockUserMaxBelowPlan(t *testing.T) {
	classes := []kicad.NetclassClassNM{{NetClass: kicad.NetClass{Name: "PP1_W7p87", Nets: []string{"SIG"}}, NM: kicad.ClassNM{Track: 200000, Inner: 200000, Min: 152400}}}
	sha := strings.Repeat("ef", 32)
	pcb := filepath.Join(t.TempDir(), "b.kicad_pcb")
	text, _ := placeRulesBlock("(version 1)\n", rulesBlockText(rulesBlockRules(classes, 2), sha, kicad.SizeSet{}))
	full := &kicad.NetclassFull{ClassesNM: classes}
	r := &kicadRun{inRaw: []byte(`{"netClassOf":{"SIG":"PP1_W7p87"}}`)}
	_ = os.WriteFile(boardSibling(pcb, ".kicad_dru"), []byte(text), 0o644)
	if err := r.checkRulesBlock(pcb, full, 2); err != nil {
		t.Fatalf("unchanged: %v", err)
	}
	late := "(rule \"late narrow\"\n  (condition \"A.NetName == 'SIG'\")\n  (constraint track_width (max 0.1mm)))\n"
	_ = os.WriteFile(boardSibling(pcb, ".kicad_dru"), []byte(strings.Replace(text, "(version 1)\n", "(version 1)\n"+late, 1)), 0o644)
	if err := r.checkRulesBlock(pcb, full, 2); err == nil || !strings.Contains(err.Error(), "RULES_STALE") || !strings.Contains(err.Error(), "late narrow") {
		t.Fatalf("below-plan max: %v", err)
	}
}
