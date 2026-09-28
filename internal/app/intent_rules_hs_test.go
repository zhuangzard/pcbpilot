package app

import (
	"strings"
	"testing"
)

// Diff pairs carry their interface's intra-pair tolerance (intent maxSkewMil,
// else the HS class table), the host's one global rule takes the strictest,
// and declared length groups are planned (host schema not captured).
func TestIntentRulesPlanHSTolerancesAndGroups(t *testing.T) {
	raw := `{"nets":{
	 "USB3_SSTX_P":{"role":"diff","diffPair":"USB3_SSTX_N","interface":"USB3","maxSkewMil":5,"widthMil":{"outer":10.8},"pairGapMil":6,"impedanceOhm":90},
	 "USB3_SSTX_N":{"role":"diff","diffPair":"USB3_SSTX_P","interface":"USB3","maxSkewMil":5,"widthMil":{"outer":10.8},"pairGapMil":6,"impedanceOhm":90},
	 "USB_DP":{"role":"diff","diffPair":"USB_DM","interface":"USB","widthMil":{"outer":10.8},"pairGapMil":6,"impedanceOhm":90},
	 "USB_DM":{"role":"diff","diffPair":"USB_DP","interface":"USB","widthMil":{"outer":10.8},"pairGapMil":6,"impedanceOhm":90},
	 "HDMI_D0_P":{"role":"diff","diffPair":"HDMI_D0_N","interface":"HDMI","lengthGroup":"HDMI_LANES","lengthTolMil":100,"widthMil":{"outer":10.8},"pairGapMil":6},
	 "HDMI_D0_N":{"role":"diff","diffPair":"HDMI_D0_P","interface":"HDMI","lengthGroup":"HDMI_LANES","lengthTolMil":100,"widthMil":{"outer":10.8},"pairGapMil":6},
	 "HDMI_CLK_P":{"role":"diff","diffPair":"HDMI_CLK_N","interface":"HDMI","lengthGroup":"HDMI_LANES","lengthTolMil":100,"widthMil":{"outer":10.8},"pairGapMil":6},
	 "HDMI_CLK_N":{"role":"diff","diffPair":"HDMI_CLK_P","interface":"HDMI","lengthGroup":"HDMI_LANES","lengthTolMil":100,"widthMil":{"outer":10.8},"pairGapMil":6}
	}}`
	in, err := parseDesignIntent([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var nets []string
	for n := range in.Nets {
		nets = append(nets, n)
	}
	p := planIntentRules(in, snapshotOf(t, cleanHostConfig(t, nets)), netSet(nets), nil)
	if len(p.Conflicts) != 0 {
		t.Fatalf("conflicts %+v", p.Conflicts)
	}
	tol := map[string]float64{}
	for _, d := range p.DiffPairs {
		tol[d.Positive] = d.LengthTolMil
	}
	if tol["USB3_SSTX_P"] != 5 || tol["USB_DP"] != 100 || tol["HDMI_D0_P"] != 5 || tol["HDMI_CLK_P"] != 5 {
		t.Fatalf("per-pair tolerances %v", tol)
	}
	dp := mnav(p.ruleConfiguration, "Physics", "Differential Pair", "differentialPair", "form").(map[string]any)
	closeTo(t, "global tolerance = strictest pair", dp["differentailPairLenTolerMax"], 5*0.0254)
	if len(p.LengthGroups) != 1 || p.LengthGroups[0].Name != "HDMI_LANES" || p.LengthGroups[0].TolMil != 100 || p.LengthGroups[0].Pairs != 2 {
		t.Fatalf("length groups %+v", p.LengthGroups)
	}
	if !strings.Contains(p.Unsupported[len(p.Unsupported)-1].Detail, "within 100 mil") {
		t.Fatalf("unsupported %+v", p.Unsupported)
	}
	// Members of one pair disagreeing on the interface is a conflict.
	in.Nets["USB_DM"].Interface = "USB3"
	if p := planIntentRules(in, snapshotOf(t, cleanHostConfig(t, nets)), netSet(nets), nil); len(p.Conflicts) == 0 {
		t.Fatal("interface disagreement not reported")
	}
}
