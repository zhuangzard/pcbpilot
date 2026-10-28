package app

// Regression tests for the WP2 review round 3 findings.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// Finding 1: a class-clearance probe attributable to the netclasses never
// proves the .kicad_dru. Width floors at the board minimum, a class
// clearance above Default and an ignored .kicad_dru: cannot-check.
func TestKicadRulesIgnoredDRUWithClassClearance(t *testing.T) {
	f := newRulesFixture(t)
	_ = os.WriteFile(f.o.intent, []byte(`{"nets":{"SIG":{"role":"signal","widthMil":{"outer":5.905512,"inner":5.905512,"min":5.905512},"clearanceMil":10}}}`), 0o644)
	f.fake.ignoreDRU = true
	f.o.plan = true
	rep, code, _ := runRules(t, f.o)
	c := checkOf(rep, "staged-readback")
	if code != 5 || c.Status != gate.CannotCheck || !strings.Contains(c.Detail, "0 of 1 .kicad_dru probe(s)") || !strings.Contains(c.Detail, "1 of 1 class-clearance probe(s) attributable") {
		t.Fatalf("exit %d %+v", code, c)
	}
	f.o.plan, f.o.apply = false, true
	if rep, code, _ = runRules(t, f.o); code != 5 || rep.Committed {
		t.Fatalf("apply must refuse: exit %d", code)
	}
}

// Finding 2: track_width minimums resolve per copper layer, explicit layer
// names included.
func TestKicadRulesLayerSpecificOverride(t *testing.T) {
	for _, tc := range []struct {
		layers int
		layer  string
	}{{2, "B.Cu"}, {4, "In2.Cu"}} {
		t.Run(tc.layer, func(t *testing.T) {
			f := newRulesFixture(t)
			f.fake.layers = tc.layers
			a := f.o
			a.apply = true
			if rep, code, se := runRules(t, a); code != 0 {
				t.Fatalf("apply: %d %+v %s", code, rep.Errors, se)
			}
			c := f.o
			c.check, c.stage = true, "placed"
			if rep, _, _ := runRules(t, c); checkOf(rep, "effective-rules").Status != gate.Pass {
				t.Fatalf("unedited: %+v", checkOf(rep, "effective-rules"))
			}
			dru := boardSibling(f.o.pcb, ".kicad_dru")
			b, _ := os.ReadFile(dru)
			_ = os.WriteFile(dru, append(b, []byte("(rule \"thin\"\n  (layer "+tc.layer+")\n  (condition \"A.NetName == 'VCC'\")\n  (constraint track_width (min 0.01mm)))\n")...), 0o644)
			rep, _, _ := runRules(t, c)
			e := checkOf(rep, "effective-rules")
			if e.Status != gate.Fail || !strings.Contains(strings.Join(e.Items, "\n"), "on "+tc.layer) {
				t.Fatalf("%+v", e)
			}
		})
	}
	if ruleOnLayer("outer", "In1.Cu") || !ruleOnLayer("inner", "In2.Cu") || !ruleOnLayer("", "B.Cu") || ruleOnLayer("B.Cu", "F.Cu") {
		t.Fatal("ruleOnLayer")
	}
	if got := copperLayerNames(4); strings.Join(got, ",") != "F.Cu,In1.Cu,In2.Cu,B.Cu" {
		t.Fatal(got)
	}
}

// Finding 3: route rejects a block that no longer matches the floored
// classes (board minimum widened) or was edited by hand.
func TestVerifyRulesBlock(t *testing.T) {
	classes := []kicad.NetclassClassNM{{NetClass: kicad.NetClass{Name: "PP1_W10"}, NM: kicad.ClassNM{Track: 254000, Inner: 254000, Min: 152400}}}
	sha := strings.Repeat("ab", 32)
	dir := t.TempDir()
	pcb := filepath.Join(dir, "b.kicad_pcb")
	write := func(text string) {
		if err := os.WriteFile(boardSibling(pcb, ".kicad_dru"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	good, _ := placeRulesBlock("(version 1)\n", rulesBlockText(rulesBlockRules(classes, 2), sha, kicad.SizeSet{}))
	write(strings.ReplaceAll(good, "\n", "\r\n"))
	if err := verifyRulesBlock(pcb, classes, 2); err != nil {
		t.Fatalf("matching block: %v", err)
	}
	// The board minimum widened after apply: the classes are now floored
	// higher than the block's floors.
	wider := []kicad.NetclassClassNM{{NetClass: kicad.NetClass{Name: "PP1_W10"}, NM: kicad.ClassNM{Track: 254000, Inner: 254000, Min: 200000}}}
	if err := verifyRulesBlock(pcb, wider, 2); err == nil || !strings.Contains(err.Error(), "RULES_STALE") || !strings.Contains(err.Error(), "rerun") {
		t.Fatalf("widened minimum: %v", err)
	}
	write(strings.Replace(good, "(min 0.1524mm)", "(min 0.1mm)", 1))
	if err := verifyRulesBlock(pcb, classes, 2); err == nil || !strings.Contains(err.Error(), "RULES_STALE") {
		t.Fatalf("hand-edited body: %v", err)
	}
	write(strings.Replace(good, sha, "deadbeef", 1))
	if err := verifyRulesBlock(pcb, classes, 2); err == nil || !strings.Contains(err.Error(), "stamp") {
		t.Fatalf("invalid stamp: %v", err)
	}
}
