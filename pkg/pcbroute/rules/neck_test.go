package rules

import (
	"math"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

func TestNeck(t *testing.T) {
	b := fourLayer()
	floor := Scope{Kind: ScopePCB}
	b.Set(floor, RuleSet{MinWidth: ptr(120_000)})
	b.Set(Scope{Kind: ScopeNet, Net: 1}, RuleSet{Width: ptr(500_000)})
	b.Set(Scope{Kind: ScopeNet, Net: 2}, RuleSet{Width: ptr(500_000), NeckZone: ptr(400_000)})
	b.Set(Scope{Kind: ScopeNet, Net: 3}, RuleSet{Width: ptr(500_000)})
	b.SetIntent(3, Intent{NoNeckDown: true})
	b.Set(Scope{Kind: ScopeNet, Net: 4}, RuleSet{Width: ptr(500_000)})
	b.SetIntent(4, Intent{ControlledImpedance: true})
	b.Set(Scope{Kind: ScopeNet, Net: 5}, RuleSet{Width: ptr(500_000), MinWidth: ptr(500_000)})
	b.Set(Scope{Kind: ScopeNet, Net: 6}, RuleSet{Width: ptr(500_000), NeckZone: ptr(9 * mm)})
	b.Set(Scope{Kind: ScopeNet, Net: 7}, RuleSet{Width: ptr(500_000), MinWidth: ptr(50_000)})
	r := build(t, b)
	pad := PadSize{Long: 600_000, Narrow: 250_000}
	cases := []struct {
		net  geom.NetID
		want Neck
	}{
		{1, Neck{MinWidth: 120_000, Zone: 600_000}}, // zone = 1.0 × the longer pad side
		{2, Neck{MinWidth: 120_000, Zone: 400_000}},
		{3, Neck{MinWidth: 500_000}}, // intent NoNeckDown
		{4, Neck{MinWidth: 500_000}}, // controlled impedance
		{5, Neck{MinWidth: 500_000}}, // MinWidth = Width: the DSN form of "no neck-down"
		{6, Neck{MinWidth: 120_000, Zone: 3 * mm}},
		{7, Neck{MinWidth: 100_000, Zone: 600_000}}, // never below the fab minimum
	}
	for _, c := range cases {
		if got := r.Neck(c.net, 0, pad); got != c.want {
			t.Errorf("net %d: %+v, want %+v", c.net, got, c.want)
		}
	}
	if got := r.Neck(1, 0, PadSize{}); got.Zone != 0 {
		t.Errorf("zero pad gives zone %d", got.Zone)
	}
	// WidthAt: inside the zone the pad's narrow side, clamped to the floor.
	for _, c := range []struct {
		net  geom.NetID
		pad  PadSize
		dist int64
		want int64
	}{
		{1, pad, 0, 250_000},
		{1, pad, 600_000, 250_000},
		{1, pad, 600_001, 500_000},
		{1, PadSize{Long: 600_000, Narrow: 50_000}, 10, 120_000},
		{1, PadSize{Long: 600_000, Narrow: 900_000}, 10, 500_000},
		{3, pad, 0, 500_000},
	} {
		if got := WidthAt(r, c.net, 0, c.pad, c.dist); got != c.want {
			t.Errorf("WidthAt(net %d, %+v, %d) = %d, want %d", c.net, c.pad, c.dist, got, c.want)
		}
	}
}

// A necked current-carrying track must carry its current at twice the target
// rise (04 §3.5).
func TestNeckAmpacity(t *testing.T) {
	b := fourLayer()
	b.Set(Scope{Kind: ScopePCB}, RuleSet{MinWidth: ptr(100_000)})
	b.SetIntent(1, Intent{CurrentA: 2, TempRiseC: 10})
	r := build(t, b)
	n := r.Neck(1, 0, PadSize{Long: mm, Narrow: mm})
	want := IntentWidth(2, 20, 35, true)
	if n.MinWidth != want || n.MinWidth >= r.Width(1, 0) {
		t.Fatalf("neck floor %d, want %d below width %d", n.MinWidth, want, r.Width(1, 0))
	}
}

// IPC-2221 spot checks from 04 §4.6.
func TestIPC2221(t *testing.T) {
	near := func(got int64, wantMM, tolMM float64) bool { return math.Abs(float64(got)/1e6-wantMM) <= tolMM }
	if w := IntentWidth(2, 10, 35, true); !near(w, 0.78, 0.01) || w%1000 != 0 {
		t.Errorf("2 A / 10 °C / 35 µm outer: %d nm, want ≈ 0.78 mm on a 1 µm step", w)
	}
	if w := IntentWidth(2, 10, 17.5, false); !near(w, 4.1, 0.05) {
		t.Errorf("2 A / 10 °C / 17.5 µm inner: %d nm, want ≈ 4.1 mm", w)
	}
	if IntentWidth(0, 10, 35, true) != 0 || IntentWidth(1, 10, 0, true) != 0 {
		t.Error("no current or no copper must give no floor")
	}
	for _, c := range []struct {
		v             float64
		outer, coated bool
		want          int64
	}{
		{5, true, false, 100_000}, {5, false, false, 50_000},
		{400, true, false, 2_500_000}, {400, false, false, 250_000},
		{600, true, false, 3_000_000}, {-230, true, false, 1_250_000}, {230, true, true, 400_000},
	} {
		if got := VoltageClearance(c.v, c.outer, c.coated); got != c.want {
			t.Errorf("VoltageClearance(%g, outer %v, coated %v) = %d, want %d", c.v, c.outer, c.coated, got, c.want)
		}
	}
	// 0.3 mm drill, 25 µm plating: π·(11.81 + 0.98)·0.98 ≈ 39.6 mil² → ≈ 0.95 A
	// under the (conservative) inner-layer formula at 10 °C.
	if a := ViaAmpacity(300_000, 0, 10); math.Abs(a-0.951) > 0.005 {
		t.Errorf("via ampacity %g A", a)
	}
}
