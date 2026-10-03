package pcbauto

import (
	"bytes"
	"strings"
	"testing"
)

// tolFacts is a clean routed result: deliverable, 100 %, DRC 0.
func tolFacts() aesFacts {
	return aesFacts{completion: 100, vias: 20, electrical: 85,
		items:    map[string]float64{"decap-loop": 82.4, "hot-loop": 78, "esd-stub": 90, "ir-drop": 100, "diff-pair": 100},
		irMV:     map[string]float64{"+3V3": 18, "+5V": 40},
		irBudget: map[string]float64{"+3V3": 99, "+5V": 250}}
}

func balancedOpt(round float64) aesJudgeOpt {
	p, _ := AesProfileByName("balanced")
	// round 0 is the beautify gate (same placement), aesScoreRound the
	// placement guard (two placements).
	return aesJudgeOpt{Tol: p.ElectricalTol, Round: round, SameLayout: round == 0}
}

// Option B: a 0.4-point drop of one electrical sub-score is accepted and
// reported, on both stages' judges.
func TestAesTolAcceptsAndReportsSmallTrade(t *testing.T) {
	for _, round := range []float64{0, aesScoreRound} { // beautify gate, placement guard
		a := tolFacts()
		a.items["decap-loop"] -= 0.4
		a.electrical -= 0.1
		why, trades := aesJudge(a, tolFacts(), balancedOpt(round))
		if why != "" {
			t.Fatalf("round %g: 0.4 trade refused: %s", round, why)
		}
		if len(trades) != 1 || trades[0].Item != "decap-loop" {
			t.Fatalf("round %g: trades %+v, want exactly decap-loop", round, trades)
		}
		s := AesTradesText(trades)
		if !strings.Contains(s, "decap loop −0.40") || !strings.Contains(s, "≤0.5 tolerance") {
			t.Errorf("trade text %q", s)
		}
	}
}

// A 0.6-point drop exceeds the tolerance.
func TestAesTolRejectsLargeTrade(t *testing.T) {
	for _, item := range []string{"decap-loop", "hot-loop", "esd-stub", "ir-drop", "diff-pair"} {
		a := tolFacts()
		a.items[item] -= 0.6
		why, _ := aesJudge(a, tolFacts(), balancedOpt(aesScoreRound))
		if !strings.Contains(why, item) {
			t.Errorf("%s −0.6: why %q, want a refusal naming it", item, why)
		}
	}
}

// Safety, completion, DRC, finding counts, vias, current-carrying width and
// over-budget IR drop are never traded, whatever the tolerance — even when
// every electrical item improves.
func TestAesTolNeverTradesHardCounts(t *testing.T) {
	for name, f := range map[string]func(*aesFacts){
		"safety gate":          func(r *aesFacts) { r.gates++ },
		"isolation finding":    func(r *aesFacts) { r.isoFindings++ },
		"isolation infeasible": func(r *aesFacts) { r.isoInfeasible++ },
		"board edge (HV band)": func(r *aesFacts) { r.edgeErrors++ },
		"via current":          func(r *aesFacts) { r.viaShort++ },
		"delivery blocker":     func(r *aesFacts) { r.blockers++ },
		"deliverability":       func(r *aesFacts) { r.undeliverable = true },
		"completion":           func(r *aesFacts) { r.completion -= 0.1 },
		"disconnected":         func(r *aesFacts) { r.disconnected++ },
		"plane open":           func(r *aesFacts) { r.planeOpen++ },
		"DRC":                  func(r *aesFacts) { r.drc++ },
		"pair findings":        func(r *aesFacts) { r.pair++ },
		"high-speed findings":  func(r *aesFacts) { r.hs++ },
		"SI findings":          func(r *aesFacts) { r.si++ },
		"vias":                 func(r *aesFacts) { r.vias++ },
		"IR violations":        func(r *aesFacts) { r.irViol++ },
		"IR over budget":       func(r *aesFacts) { r.irMV["+3V3"] = 100 },
	} {
		for _, round := range []float64{0, aesScoreRound} {
			a := tolFacts()
			for k := range a.items {
				a.items[k] += 1 // better everywhere else
			}
			a.electrical += 1
			f(&a)
			if why, _ := aesJudge(a, tolFacts(), balancedOpt(round)); why == "" {
				t.Errorf("%s (round %g): regression accepted with the 0.5 tolerance", name, round)
			}
		}
	}
	// Copper narrower than its current: zero tolerance on the beautify gate
	// (one placement's copper edited); between two placements it is routing
	// noise and not judged.
	a0 := tolFacts()
	a0.underWidth += 10
	if why, _ := aesJudge(a0, tolFacts(), balancedOpt(0)); why == "" {
		t.Errorf("beautify gate accepted more under-width copper")
	}
	// A net without a drop budget: refused on the beautify gate, left to the
	// IR-drop score item by the placement guard.
	a1, b1 := tolFacts(), tolFacts()
	b1.irMV["GND"], b1.irBudget["GND"] = 1.0, 0
	a1.irMV["GND"], a1.irBudget["GND"] = 1.2, 0
	if why, _ := aesJudge(a1, b1, balancedOpt(0)); why == "" {
		t.Errorf("beautify gate accepted a drop increase on a net without a budget")
	}
	if why, tr := aesJudge(a1, b1, balancedOpt(aesScoreRound)); why != "" || len(tr) != 0 {
		t.Errorf("placement guard judged a net without a budget: %q %+v", why, tr)
	}
	// Fewer vias are fine.
	a := tolFacts()
	a.vias--
	if why, _ := aesJudge(a, tolFacts(), balancedOpt(0)); why != "" {
		t.Errorf("fewer vias refused: %s", why)
	}
}

// Within its budget a raw IR increase is traded and reported (the IR-drop
// score item carries the tolerance); at tolerance 0 it is refused.
func TestAesTolRawIRWithinBudget(t *testing.T) {
	a := tolFacts()
	a.irMV["+3V3"] = 18.3
	why, trades := aesJudge(a, tolFacts(), balancedOpt(0))
	if why != "" || len(trades) != 1 || trades[0].Unit != "mV" {
		t.Fatalf("in-budget IR: why %q trades %+v", why, trades)
	}
	if s := trades[0].String(); !strings.Contains(s, "+3V3") || !strings.Contains(s, "budget") {
		t.Errorf("IR trade text %q", s)
	}
	if why, _ := aesJudge(a, tolFacts(), aesJudgeOpt{SameLayout: true}); why == "" {
		t.Errorf("strict gate accepted a raw IR increase")
	}
}

// The functional profile trades nothing; the presets carry the shared
// constant; a style file may lower the tolerance but never raise it.
func TestAesTolFunctionalStaysStrict(t *testing.T) {
	want := map[string]float64{"functional": 0, "balanced": AesElectricalTol, "precision": AesElectricalTol}
	for name, w := range want {
		p, err := AesProfileByName(name)
		if err != nil || p.ElectricalTol != w {
			t.Errorf("%s: tolerance %g (%v), want %g", name, p.ElectricalTol, err, w)
		}
	}
	if AesElectricalTol != 0.5 {
		t.Errorf("AesElectricalTol %g, the user's decision is 0.5", AesElectricalTol)
	}
	fp, _ := AesProfileByName("functional")
	a := tolFacts()
	a.items["decap-loop"] -= 0.4
	// Placement guard at functional: only the 0.05 rounding.
	if why, _ := aesJudge(a, tolFacts(), aesJudgeOpt{Tol: fp.ElectricalTol, Round: aesScoreRound}); why == "" {
		t.Errorf("functional placement guard accepted a 0.4 trade")
	}
	// Beautify gate at functional: nothing at all.
	b := tolFacts()
	b.items["decap-loop"] -= 0.01
	if why, _ := aesJudge(b, tolFacts(), aesJudgeOpt{Tol: fp.ElectricalTol, SameLayout: true}); why == "" {
		t.Errorf("functional beautify gate accepted a 0.01 trade")
	}
	if p, err := ParseAesStyle([]byte(`{"profile":"custom","base":"balanced","electricalTolerance":0.2}`)); err != nil || p.ElectricalTol != 0.2 {
		t.Errorf("custom 0.2: %+v %v", p.ElectricalTol, err)
	}
	if _, err := ParseAesStyle([]byte(`{"profile":"custom","base":"balanced","electricalTolerance":0.6}`)); err == nil {
		t.Errorf("custom 0.6 accepted: the tolerance may never exceed %g", AesElectricalTol)
	}
	if got := aesElectricalTol(nil); got != AesElectricalTol {
		t.Errorf("default profile tolerance %g", got)
	}
}

// report.md names every trade of both stages, and says "无" when a stage
// traded nothing.
func TestAesTolReportedInMarkdown(t *testing.T) {
	tr := []AesTrade{{Item: "decap-loop", From: 82.48, To: 82.40, Tolerance: 0.5}}
	r := &Report{
		Placement: &PlaceResult{Aesthetics: &AesPlaceReport{Profile: "balanced", ElectricalTol: 0.5, Guard: "kept", Trades: tr}},
		Result:    &Result{Route: &RouteResult{Beautify: &BeautifyStats{Kept: true, ElectricalTol: 0.5}}},
	}
	var buf bytes.Buffer
	r.WriteMarkdown(&buf)
	md := buf.String()
	if !strings.Contains(md, "decap loop −0.08") || !strings.Contains(md, "≤0.5 tolerance") {
		t.Errorf("placement trade missing from report.md:\n%s", md)
	}
	if !strings.Contains(md, "布线美化电气子项交换") || !strings.Contains(md, "：无") {
		t.Errorf("beautify no-trade line missing from report.md:\n%s", md)
	}
}
