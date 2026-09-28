package postsim

import (
	"math"
	"os"
	"testing"
)

// The live ESP32-S3 mini board (4 layers, poured TOP/BOTTOM GND, IN2 power
// split, IN1 negative GND plane) replayed from its trimmed dump. Numbers are
// the offline-verified baseline of docs/examples/esp32-mini-post-layout/.
func TestESP32MiniPostLayout(t *testing.T) {
	read := func(n string) []byte {
		b, err := os.ReadFile("testdata/esp32mini/" + n)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	o := DefaultOptions()
	if err := ApplyIntent(read("intent.json"), &o); err != nil {
		t.Fatal(err)
	}
	r, err := Run(read("board.json"), read("sim.json"), o)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.CompareWithPlan(read("plan-ir.json")); err != nil {
		t.Fatal(err)
	}
	if r.Settings.Planes["IN1"] != "GND" {
		t.Errorf("IN1 plane: %v", r.Settings.Planes)
	}
	want := map[string]float64{"+3V3": 2.70, "VSYS_5V": 13.61, "USB_VBUS": 17.07, "+5V_TERM": 0.99}
	for net, mv := range want {
		n := netOf(t, r, net)
		if n.Status != "ok" || math.Abs(n.WorstMV-mv) > 0.05*mv {
			t.Errorf("%s: %s %.3f mV, want ≈ %.2f mV", net, n.Status, n.WorstMV, mv)
		}
		for _, c := range r.Compare {
			if c.Net == net && c.PostMV > c.AutoMV {
				t.Errorf("%s: post-layout %.2f mV above pcb auto's %.2f mV (the real copper and pad bypass should only lower it)", net, c.PostMV, c.AutoMV)
			}
		}
	}
	if g := netOf(t, r, "GND"); g.Status != "info" || g.WorstMV <= 0 || g.WorstMV > 1 {
		t.Errorf("GND %+v", g)
	}
	if len(r.Vias) == 0 || math.Abs(r.Vias[0].CurrentA-0.5014) > 0.01 || r.Vias[0].Net != "+3V3" {
		t.Errorf("top via %+v", r.Vias[0])
	}
	th := r.Thermal
	if math.Abs(th.MaxBoardC-86.3) > 1.5 || th.MaxAt.What != "U3" || math.Abs(th.BalanceErrPct) > 1 {
		t.Errorf("thermal max %.2f °C at %s, balance %.4f %%", th.MaxBoardC, th.MaxAt.What, th.BalanceErrPct)
	}
	if th.Parts[0].Ref != "U3" || th.Parts[0].Status != "needs-datasheet" {
		t.Errorf("hottest part %+v", th.Parts[0])
	}
	if r.Verdict.Status != "pass" || len(r.Feedback) != 0 {
		t.Errorf("verdict %s, %d feedback item(s): %v", r.Verdict.Status, len(r.Feedback), r.Verdict.Reasons)
	}
	for _, n := range r.Nets {
		t.Logf("%-9s %-7s %-14s worst %7.3f mV @ %-6s J %.1f A/mm²", n.Net, n.Role, n.Scenario, n.WorstMV, n.WorstPad, n.MaxJAmm2)
	}
	t.Logf("board max %.1f °C (%s), copper self-heating %.2f °C", th.MaxBoardC, th.Scenario, th.MaxCopperRiseC)
}
