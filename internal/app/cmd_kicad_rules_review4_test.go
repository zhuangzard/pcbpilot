package app

// Regression tests for the WP2 review round 4 findings.

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

// Finding 1: floors resolve per net: a rule naming another planned net
// never counts for this one.
func TestKicadRulesFloorResolvedPerNet(t *testing.T) {
	f := newRulesFixture(t)
	a := f.o
	a.apply = true
	if rep, code, se := runRules(t, a); code != 0 {
		t.Fatalf("apply: %d %+v %s", code, rep.Errors, se)
	}
	dru := boardSibling(f.o.pcb, ".kicad_dru")
	b, _ := os.ReadFile(dru)
	b = append(b, []byte("(rule \"gnd thin\"\n  (layer B.Cu)\n  (condition \"A.NetName == 'GND'\")\n  (constraint track_width (min 0.01mm)))\n"+
		"(rule \"sig wide\"\n  (layer B.Cu)\n  (condition \"A.NetName == 'SIG'\")\n  (constraint track_width (min 2mm)))\n")...)
	_ = os.WriteFile(dru, b, 0o644)
	c := f.o
	c.check, c.stage = true, "placed"
	rep, _, _ := runRules(t, c)
	e := checkOf(rep, "effective-rules")
	items := strings.Join(e.Items, "\n")
	if e.Status != gate.Fail || !strings.Contains(items, "GND: track_width minimum 0.01mm (rule \"gnd thin\") on B.Cu") {
		t.Fatalf("%+v", e)
	}
}

// Finding 2: a user rule on a domain class the apply introduces overlaps.
func TestKicadRulesConflictOnNewDomainClass(t *testing.T) {
	f := newRulesFixture(t)
	_ = os.WriteFile(boardSibling(f.o.pcb, ".kicad_dru"), []byte("(version 1)\n(rule \"hv wide\"\n  (condition \"A.hasNetclass('PPD_HVD')\")\n  (constraint track_width (min 2mm)))\n"), 0o644)
	a := f.o
	a.apply = true
	rep, code, _ := runRules(t, a)
	if code != 6 || rep.Committed || len(rep.Conflicts) != 1 || rep.Conflicts[0].Name != "hv wide" || !strings.Contains(rep.Conflicts[0].Detail, "HV") {
		t.Fatalf("exit %d %+v", code, rep.Conflicts)
	}
}

// Finding 3: a rules block without a valid stamp is stale for route.
func TestRouteRulesStampHeaderDeleted(t *testing.T) {
	f := newRulesFixture(t)
	a := f.o
	a.apply = true
	if _, code, se := runRules(t, a); code != 0 {
		t.Fatalf("apply: %d %s", code, se)
	}
	dru := boardSibling(f.o.pcb, ".kicad_dru")
	b, _ := os.ReadFile(dru)
	nb := regexp.MustCompile(`(?m)^# request-sha256 [0-9a-f]+\n`).ReplaceAll(b, nil)
	if bytes.Equal(nb, b) {
		t.Fatal("no header removed")
	}
	_ = os.WriteFile(dru, nb, 0o644)
	in, _ := loadDesignIntent(f.o.intent)
	var stderr bytes.Buffer
	r := &kicadRun{o: kicadRouteOpts{pcb: f.o.pcb, intent: f.o.intent, sim: f.o.sim}, in: in, stderr: &stderr, summary: map[string]any{}}
	if _, err := r.rulesStamp(deriveKicadRules(in, f.o.intent, nil), nil); err == nil || !strings.Contains(err.Error(), "RULES_STALE") || !strings.Contains(err.Error(), "stamp") {
		t.Fatalf("header deleted: %v", err)
	}
	// A malformed header too.
	_ = os.WriteFile(dru, regexp.MustCompile(`(?m)^# request-sha256 [0-9a-f]+`).ReplaceAll(b, []byte("# request-sha256 xyz")), 0o644)
	if _, err := r.rulesStamp(deriveKicadRules(in, f.o.intent, nil), nil); err == nil || !strings.Contains(err.Error(), "RULES_STALE") {
		t.Fatalf("malformed header: %v", err)
	}
}

// Finding 4: with no .kicad_dru probe possible (every floor at or below the
// probe step, no pair), class probes alone never pass an ignored .kicad_dru.
func TestKicadRulesNoDRUProbePossible(t *testing.T) {
	f := newRulesFixture(t)
	_ = os.WriteFile(f.o.intent, []byte(`{"nets":{"SIG":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":0.04},"clearanceMil":10}}}`), 0o644)
	f.fake.ignoreDRU = true
	f.o.plan = true
	rep, code, _ := runRules(t, f.o)
	c := checkOf(rep, "staged-readback")
	if code != 5 || c.Status != gate.CannotCheck || !strings.Contains(c.Detail, "0 of 0 .kicad_dru probe(s)") {
		t.Fatalf("exit %d %+v", code, c)
	}
}
