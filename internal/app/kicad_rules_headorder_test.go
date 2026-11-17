package app

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// Two head rules that may govern the same pair: the weaker one after the
// stricter one wins in KiCad, so the stricter pair is not carried.
func TestIsolationHeadRulesAreOrdered(t *testing.T) {
	full := &kicad.NetclassFull{}
	full.Domains = map[string][]string{"HOT": {"H1"}, "WARM": {"H1", "H2"}, "LV": {"L1"}}
	r := &rulesRun{full: full}
	mm := func(v int64) *int64 { v *= 1e6; return &v }
	head := func(a, b string, min int64) kicad.DRURule {
		return kicad.DRURule{
			Name:        keepHeadName("pcbpilot isolation "+a+"|"+b, "clearance"),
			Condition:   isoCondition(a, b),
			Block:       "head",
			Constraints: []kicad.DRUConstraint{{Type: "clearance", Min: mm(min)}},
		}
	}
	strict, weak := head("PPD_HOT", "PPD_LV", 2), head("PPD_WARM", "PPD_LV", 1)
	p := rulesIsoPlan{A: "HOT", B: "LV", ClassA: "PPD_HOT", ClassB: "PPD_LV", ClearanceMm: 2}
	if bad := r.isolationRuleProblems([]kicad.DRURule{weak, strict}, p); len(bad) != 0 {
		t.Fatalf("weaker head rule first: %v", bad)
	}
	bad := r.isolationRuleProblems([]kicad.DRURule{strict, weak}, p)
	if len(bad) != 1 || !strings.Contains(bad[0], "may lower clearance") || !strings.Contains(bad[0], weak.Name) {
		t.Fatalf("weaker head rule last was accepted: %v", bad)
	}
	// A head rule of a pair that shares no net is no one's business.
	other := head("PPD_LV", "PPD_LV2", 1)
	if bad := r.isolationRuleProblems([]kicad.DRURule{strict, other}, p); len(bad) != 0 {
		t.Fatalf("unrelated head rule: %v", bad)
	}
}
