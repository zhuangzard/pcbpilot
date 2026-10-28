package app

// Regression tests for the v0.12 joint final review.

import (
	"os"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// 1: inner layers use IPC-2221's internal constant: same copper weight,
// same current → a wider inner than outer width.
func TestPlanRulesWidthsInnerInternalConstant(t *testing.T) {
	in := testIntent(t, `{"nets":{"P":{"role":"power","currentA":1.5,"currentSource":"simulated","widthMil":{"outer":6,"inner":6,"min":6}}}}`)
	_, bs := planRulesWidths(in, nil, nil, rulesCopper{OuterOz: 1, InnerOz: 1, TempRiseC: 10})
	b := basisOf(bs, "P")
	if b.CurrentWidth == nil || b.CurrentWidth.InnerMil <= b.CurrentWidth.OuterMil ||
		b.CurrentWidth.InnerMil != metricCeilMil(pcbauto.TraceWidthForCurrent(1.5, 10, 1, true)) {
		t.Fatalf("%+v", b.CurrentWidth)
	}
}

// 2: null is not a number.
func TestCurrentNullIsNotEvidence(t *testing.T) {
	in := testIntent(t, `{"nets":{"A":{"role":"power","currentSource":"simulated","currentA":null,"widthMil":{"outer":10,"inner":10,"min":10}}}}`)
	if in.Nets["A"].currentAGiven {
		t.Fatal("intent currentA null counted as given")
	}
	cu := rulesCopper{OuterOz: 1, InnerOz: 1, TempRiseC: 10}
	if _, bs := planRulesWidths(in, nil, nil, cu); basisOf(bs, "A").Status != "cannot-check" {
		t.Fatalf("intent null: %+v", basisOf(bs, "A"))
	}
	for name, raw := range map[string]string{
		"net current": `{"schemaVersion":1,"results":[{"scenario":"worst","nets":{"B":{"currentA":null}}}]}`,
		"pin current": `{"schemaVersion":1,"results":[{"scenario":"worst","nets":{"B":{"pins":[{"ref":"U1","pin":"1","currentA":null,"dir":"sink"}]}}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := simCurrents([]byte(raw))["B"]; ok {
				t.Fatal("null counted as a current")
			}
			sp, err := pcbauto.ParseSim([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			b := testIntent(t, `{"nets":{"B":{"role":"power","widthMil":{"outer":10,"inner":10,"min":10}}}}`)
			if _, bs := planRulesWidths(b, sp, nil, cu, simCurrents([]byte(raw))); basisOf(bs, "B").Status != "cannot-check" {
				t.Fatalf("%+v", basisOf(bs, "B"))
			}
		})
	}
}

// 3: evidence and current come from one pass, the worst case over every
// result: an empty entry in one scenario never hides 2 A in another.
func TestSimCurrentsWorstOverScenarios(t *testing.T) {
	raw := []byte(`{"schemaVersion":1,"results":[{"scenario":"typical","nets":{"VCC":{}}},{"scenario":"peak","nets":{"VCC":{"currentA":2}}}]}`)
	sc := simCurrents(raw)
	if c := sc["VCC"]; c.A != 2 || c.Scenario != "peak" {
		t.Fatalf("%+v", sc)
	}
	sp, err := pcbauto.ParseSim(raw)
	if err != nil {
		t.Fatal(err)
	}
	in := testIntent(t, `{"nets":{"VCC":{"role":"power","widthMil":{"outer":10,"inner":10,"min":10}}}}`)
	_, bs := planRulesWidths(in, sp, nil, rulesCopper{OuterOz: 1, InnerOz: 1, TempRiseC: 10}, sc)
	if b := basisOf(bs, "VCC"); b.Status != "pass" || b.Current == nil || b.Current.A != 2 {
		t.Fatalf("%+v", b)
	}
	// With a "worst" result present every result still counts (worst case).
	raw = []byte(`{"schemaVersion":1,"results":[{"scenario":"worst","nets":{"VCC":{}}},{"scenario":"peak","nets":{"VCC":{"currentA":3}}}]}`)
	if c := simCurrents(raw)["VCC"]; c.A != 3 {
		t.Fatalf("%+v", c)
	}
}

// 4: the via-current gate rates the vias against the resolved current, not
// the intent's: intent 0.1 A, sim 5 A → one 5 mil via fails.
func TestKicadRulesViaCurrentUsesResolvedCurrent(t *testing.T) {
	f := newRulesFixture(t)
	in := strings.Replace(rulesTestIntent, `"VCC":{"role":"power","domain":"LV","currentA":0.5`, `"VCC":{"role":"power","domain":"LV","currentA":0.1`, 1)
	_ = os.WriteFile(f.o.intent, []byte(in), 0o644)
	run := func(sim string) rulesCheck {
		_ = os.WriteFile(f.o.sim, []byte(sim), 0o644)
		c := f.o
		c.check = true
		rep, _, _ := runRules(t, c)
		return checkOf(rep, "via-current")
	}
	if v := run(strings.Replace(rulesTestSim, `"VCC":{"currentA":1.5`, `"VCC":{"currentA":0.1`, 1)); v.Status != gate.Pass {
		t.Fatalf("0.1 A: %+v", v)
	}
	if v := run(strings.Replace(rulesTestSim, `"VCC":{"currentA":1.5`, `"VCC":{"currentA":5`, 1)); v.Status != gate.Fail || !strings.Contains(strings.Join(v.Items, "\n"), "VCC") {
		t.Fatalf("5 A: %+v", v)
	}
}

// Recheck: ripple RMS is taken per result in the same pass and the worst
// case over results kept: typical 1 A DC + 10 A RMS, worst 2 A DC + 3 A
// RMS → a 10 A basis, also for the via-current gate.
func TestSimCurrentsRippleWorstOverScenarios(t *testing.T) {
	raw := []byte(`{"schemaVersion":1,"results":[
	 {"scenario":"typical","nets":{"SW":{"currentA":1,"role":"switch"}},"ripple":{"SW":{"iRmsA":10}}},
	 {"scenario":"worst","nets":{"SW":{"currentA":2,"role":"switch"}},"ripple":{"SW":{"iRmsA":3}}}]}`)
	sc := simCurrents(raw)
	if c := sc["SW"]; c.A != 10 || !c.Ripple || c.Scenario != "typical" || c.DC != 1 {
		t.Fatalf("%+v", c)
	}
	sp, err := pcbauto.ParseSim(raw)
	if err != nil {
		t.Fatal(err)
	}
	in := testIntent(t, `{"nets":{"SW":{"role":"switch","widthMil":{"outer":20,"inner":20,"min":20}}}}`)
	_, bs := planRulesWidths(in, sp, nil, rulesCopper{OuterOz: 1, InnerOz: 1, TempRiseC: 10}, sc)
	if b := basisOf(bs, "SW"); b.Current == nil || b.Current.A != 10 || !strings.Contains(b.Current.Why, "ripple RMS 10.000 A") {
		t.Fatalf("%+v", b.Current)
	}
	// The via-current gate gets the same basis (viaIntent raises the net).
	r := &rulesRun{bases: bs, data: map[string][]byte{"i.json": []byte(`{"nets":{"SW":{"role":"switch","currentA":1,"widthMil":{"outer":20}}}}`)},
		o: kicadRulesOpts{intent: "i.json"}, rep: &rulesReport{Copper: &rulesCopper{TempRiseC: 10}}}
	if vi := r.viaIntent(); vi.Nets["SW"].CurrentA != 10 {
		t.Fatalf("via intent: %+v", vi.Nets["SW"])
	}
}
