package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// rvTestNetlist is a synthetic design: an open-drain interrupt between two
// ICs on a 3.3 V rail, no pull-up.
func rvTestNetlist() *kicad.SchNetlist {
	ic := func(ref, pin3 string) kicad.SchNetComp {
		return kicad.SchNetComp{Ref: ref, Lib: "Synth", Part: "IC", Sheet: "/", Fields: map[string]string{},
			Pins: []kicad.SchNetPin{{Number: "1", Name: "VDD", Type: "power_in"}, {Number: "2", Name: "GND", Type: "power_in"}, {Number: "3", Name: "INT", Type: pin3}}}
	}
	return &kicad.SchNetlist{
		Components: []kicad.SchNetComp{ic("U1", "open_collector"), ic("U2", "input"),
			{Ref: "C1", Value: "10u", Lib: "Device", Part: "C", Sheet: "/", Pins: []kicad.SchNetPin{{Number: "1", Type: "passive"}, {Number: "2", Type: "passive"}}}},
		Nets: []kicad.SchNet{
			{Code: "1", Name: "+3V3", Nodes: []kicad.SchNetNode{{Ref: "U1", Pin: "1", Type: "power_in"}, {Ref: "U2", Pin: "1", Type: "power_in"}, {Ref: "C1", Pin: "1", Type: "passive"}}},
			{Code: "2", Name: "GND", Nodes: []kicad.SchNetNode{{Ref: "U1", Pin: "2", Type: "power_in"}, {Ref: "U2", Pin: "2", Type: "power_in"}, {Ref: "C1", Pin: "2", Type: "passive"}}},
			{Code: "3", Name: "/IRQ", Nodes: []kicad.SchNetNode{{Ref: "U1", Pin: "3", Type: "open_collector"}, {Ref: "U2", Pin: "3", Type: "input"}}},
		}}
}

func rvCheck(t *testing.T, r *kicad.SchReview, id string) kicad.ReviewCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s", id)
	return kicad.ReviewCheck{}
}

// TestLoadReviewHints: intent nets, regulator blocks and the review notes
// become hints keyed by net identity; unknown names are notes; an old
// intent.json without review{} still loads.
func TestLoadReviewHints(t *testing.T) {
	dir := t.TempDir()
	nl := rvTestNetlist()
	ids := []string{"+3V3", "GND", "/IRQ"}
	p := filepath.Join(dir, "intent.json")
	_ = os.WriteFile(p, []byte(`{"schemaVersion":1,"nets":{"+3V3":{"role":"power","voltage":{"nom":3.3}},"IRQ":{"role":"signal"}},
	  "blocks":[{"function":"ldo","core":"U9"}],
	  "review":{"internalPullups":["U2.INT"],"referenceRails":["NOPE"],"testPoints":["TP9"]}}`), 0o644)
	h, notes, err := loadReviewHints(p, "", ids, nl)
	if err != nil {
		t.Fatal(err)
	}
	if h.NetVoltage["+3V3"] != 3.3 || h.NetRole["/IRQ"] != "signal" || h.Regulators["U9"] == "" || h.PullUp["U2.INT"] == "" {
		t.Fatalf("%+v", h)
	}
	if len(notes) != 2 || !strings.Contains(strings.Join(notes, " "), "NOPE") || !strings.Contains(strings.Join(notes, " "), "TP9") {
		t.Fatalf("notes %v", notes)
	}
	in := kicad.ReviewInput{Netlist: nl, ERC: &kicad.ERCReport{}, HierRan: true, Hints: h}
	r, err := kicad.Review(in)
	if err != nil {
		t.Fatal(err)
	}
	if c := rvCheck(t, r, kicad.RvOpenDrain); c.Status != "pass" || len(c.Exempt) != 1 || !strings.Contains(c.Exempt[0].Source, "intent review.internalPullups") {
		t.Fatalf("%+v", c)
	}
	// backward compatible: an intent.json without review{}
	_ = os.WriteFile(p, []byte(`{"schemaVersion":1,"nets":{"+3V3":{"role":"power","voltage":{"nom":3.3}}}}`), 0o644)
	if h, _, err = loadReviewHints(p, "", ids, nl); err != nil || h.Intent == "" {
		t.Fatal(err)
	}
	// not an intent document → input error, not a verdict
	_ = os.WriteFile(p, []byte(`{"foo":1}`), 0o644)
	if _, _, err := loadReviewHints(p, "", ids, nl); err == nil {
		t.Fatal("non-intent accepted")
	}
	// sim voltages fill what intent does not give
	sp := filepath.Join(dir, "sim.json")
	_ = os.WriteFile(sp, []byte(`{"results":[{"scenario":"worst","nets":{"+3V3":{"voltage":3.29}}}]}`), 0o644)
	h, _, err = loadReviewHints("", sp, ids, nl)
	if err != nil || h.NetVoltage["+3V3"] != 3.29 || !strings.HasPrefix(h.VoltageSource["+3V3"], "sim ") {
		t.Fatalf("%v %+v", err, h)
	}
}

// TestSchReviewWaiverFile: waivers of gate sch-review match the finding's
// fingerprint exactly; other gates' waivers and text matches do not apply.
func TestSchReviewWaiverFile(t *testing.T) {
	nl := rvTestNetlist()
	r, err := kicad.Review(kicad.ReviewInput{Netlist: nl, ERC: &kicad.ERCReport{}, HierRan: true, Hints: kicad.ReviewHints{Intent: "i"}})
	if err != nil {
		t.Fatal(err)
	}
	c := rvCheck(t, r, kicad.RvOpenDrain)
	if c.Status != "fail" {
		t.Fatalf("%+v", c)
	}
	fp := c.Findings[0].Fingerprint
	r.Judge(reviewWaiver([]gateWaiver{{Gate: "kicad-drc", Match: fp, Reason: "x", By: "y"}, {Gate: schReviewGate, Match: "IRQ", Reason: "x", By: "y"}}))
	if r.Status.OK() {
		t.Fatal("a waiver of another gate or a text match applied")
	}
	r.Judge(reviewWaiver([]gateWaiver{{Gate: schReviewGate, Match: fp, Reason: "reviewed: pull-up on the host board", By: "engineer"}}))
	if !r.Status.OK() || r.Verdict != "pass-with-waivers" || !strings.Contains(rvCheck(t, r, kicad.RvOpenDrain).Findings[0].Waived, "engineer") {
		t.Fatalf("%s %+v", r.Verdict, rvCheck(t, r, kicad.RvOpenDrain))
	}
}

func TestReviewModeAndFlags(t *testing.T) {
	for in, want := range map[string]string{"": "advisory", "advisory": "advisory", "strict": "strict", "off": "off"} {
		if got, err := reviewMode(in); err != nil || got != want {
			t.Errorf("%q → %q %v", in, got, err)
		}
	}
	if _, err := reviewMode("on"); err == nil {
		t.Error("bad mode accepted")
	}
	var out, errb bytes.Buffer
	c := newKicadSchCheckCmd(&out, &errb)
	c.SetArgs([]string{"--sch", "x.kicad_sch", "--review", "--fix-pwr-flag"})
	c.SilenceUsage, c.SilenceErrors = true, true
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("--review --fix-pwr-flag: %v", err)
	}
	c = newKicadSchCheckCmd(&out, &errb)
	c.SetArgs([]string{"--sch", "x.kicad_sch", "--intent", "i.json"})
	c.SilenceUsage, c.SilenceErrors = true, true
	if err := c.Execute(); err == nil || !strings.Contains(err.Error(), "--review flags") {
		t.Fatalf("--intent without --review: %v", err)
	}
}

// TestSchReviewGateLive builds a small synthetic design (a connector-fed
// LDO with no capacitors) with sch-build: advisory reports the review and
// commits sch-review.json; strict blocks the build and writes nothing.
func TestSchReviewGateLive(t *testing.T) {
	sbNeedKicad(t)
	if testing.Short() {
		t.Skip("live KiCad build")
	}
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	spec := func() *sbSpec {
		return &sbSpec{Name: "rvldo", Title: sbTitle{Title: "Review test", Date: "2026-10-28", Rev: "1", Company: "Synthetic"},
			Parts: []sbPart{
				{Ref: "J1", Value: "IN", Symbol: "Connector:Conn_01x02_Pin"},
				{Ref: "U1", Value: "AMS1117-3.3", Symbol: "Regulator_Linear:AMS1117-3.3"},
				{Ref: "J2", Value: "OUT", Symbol: "Connector:Conn_01x02_Pin"},
			},
			Nets: []sbNet{{Name: "+5V", Kind: "power", Pins: []string{"J1:1", "U1:3"}}, {Name: "+3V3", Kind: "power", Pins: []string{"U1:2", "J2:1"}},
				{Name: "GND", Kind: "ground", Pins: []string{"J1:2", "U1:1", "J2:2"}}}}
	}
	out := filepath.Join(t.TempDir(), "adv")
	res, err := runSchBuild(spec(), sbOptions{OutDir: out, NoIntent: true, Offline: true, PlannerBudget: 20000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st := sbGateStatus(res, schReviewGate); st != "warn" || !fileExists(filepath.Join(out, "sch-review.json")) {
		t.Fatalf("advisory gate %q, ok=%v %+v", st, res.OK, res.Gates)
	}
	var rv kicad.SchReview
	b, _ := os.ReadFile(filepath.Join(out, "sch-review.json"))
	if err := json.Unmarshal(b, &rv); err != nil || rvCheck(t, &rv, kicad.RvRegOutputCap).Status != "fail" || rvCheck(t, &rv, kicad.RvRegInputCap).Status != "fail" {
		t.Fatalf("%v %+v", err, rv.Checks)
	}
	strictOut := filepath.Join(t.TempDir(), "strict")
	res, err = runSchBuild(spec(), sbOptions{OutDir: strictOut, NoIntent: true, Offline: true, PlannerBudget: 20000, SchReview: "strict"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Error == nil || res.Error.Code != "SCH_REVIEW_FAILED" || fileExists(filepath.Join(strictOut, "rvldo.kicad_sch")) {
		t.Fatalf("strict: ok=%v err=%+v", res.OK, res.Error)
	}
}
