package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// The producer's via block (pkg/intent NetVia) must reach both consumers:
// `pcb rules apply` (designIntent) and the engine / pcb check
// (pcbauto.ParseIntent) — a field rename on either side fails here.
func TestIntentViaContract(t *testing.T) {
	src := intent.Intent{Nets: map[string]*intent.NetPlan{"VOUT": {Role: "power", CurrentA: 2, NetClass: "POWER_HI", ViasPerTransition: 3,
		Via: &intent.NetVia{DrillMil: 15.75, DiaMil: 27.56, CountPerTransition: 3, AmpacityA: 2.67, MarginPct: 33.5, Why: "3 × 0.4 mm"}}},
		NetClasses: []*intent.NetClass{{Name: "POWER_HI", Nets: []string{"VOUT"}, TrackMil: 31.5, ClearanceMil: 6, ViaDrillMil: 15.75, ViaDiaMil: 27.56}}}
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseDesignIntent(raw)
	if err != nil {
		t.Fatal(err)
	}
	v := in.Nets["VOUT"].Via
	if v == nil || v.DrillMil != 15.75 || v.DiaMil != 27.56 || v.CountPerTransition != 3 || v.AmpacityA != 2.67 || v.MarginPct != 33.5 || v.Why == "" {
		t.Fatalf("rules consumer via %+v", v)
	}
	pi, err := pcbauto.ParseIntent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pv := pi.Nets["VOUT"].Via; pv == nil || pv.DrillMil != 15.75 || pv.CountPerTransition != 3 {
		t.Fatalf("engine consumer via %+v", pv)
	}
	bad := strings.Replace(string(raw), `"diaMil":27.56,"countPerTransition"`, `"diaMil":10,"countPerTransition"`, 1)
	if _, err := parseDesignIntent([]byte(bad)); err == nil {
		t.Fatal("rules consumer accepted a via drill ≥ its diameter")
	}
	if _, err := pcbauto.ParseIntent([]byte(bad)); err == nil {
		t.Fatal("engine consumer accepted a via drill ≥ its diameter")
	}
}

// `pcb rules apply` writes the class's sized via (POWER_HI 0.4/0.7 mm), not
// the board default, as the Via Size rule default.
func TestIntentRulesPlanWritesSizedClassVia(t *testing.T) {
	in := mustIntent(t)
	for i := range in.NetClasses {
		if in.NetClasses[i].Name == "POWER" {
			in.NetClasses[i].ViaDrillMil, in.NetClasses[i].ViaDiaMil = 15.75, 27.56
		}
	}
	p := planIntentRules(in, snapshotOf(t, cleanHostConfig(t, esp32PcbNets)), netSet(esp32PcbNets), nil)
	if len(p.Conflicts) != 0 {
		t.Fatalf("conflicts: %+v", p.Conflicts)
	}
	via := mnav(p.ruleConfiguration, "Physics", "Via Size", "PP_POWER", "form").(map[string]any)
	closeTo(t, "via outer", via["viaOuterdiameterDefault"], 27.56*0.0254)
	closeTo(t, "via hole", via["viaInnerdiameterDefault"], 15.75*0.0254)
}
