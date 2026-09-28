package intent

import (
	"math"
	"strings"
	"testing"
)

// The flyback's 2 A secondary: its 31.5 mil track holds 3 class (12/24)
// vias of the 4 the current needs with the 20 % margin, so the via is
// upsized to 0.4 mm × 3 and POWER_HI carries that via size (what `pcb rules
// apply` writes), not the board default.
func TestFlybackPowerHiViaSized(t *testing.T) {
	withSafety(t)
	it := stressIntent(t, "flyback", "spec.json", nil)
	v := it.Nets["VOUT"].Via
	if v == nil || v.CountPerTransition != 3 || math.Abs(v.DrillMil-15.75) > 0.01 || v.AmpacityA < 2.0*1.2 {
		t.Fatalf("VOUT via %+v", v)
	}
	if it.Nets["VOUT"].ViasPerTransition != v.CountPerTransition {
		t.Fatal("viasPerTransition must equal via.countPerTransition")
	}
	if v.DropMV <= 0 || v.ResistanceMOhm <= 0 || v.PlatingMil != 0.7 {
		t.Fatalf("via numbers %+v", v)
	}
	var hi *NetClass
	for _, c := range it.NetClasses {
		if c.Name == "POWER_HI" {
			hi = c
		}
	}
	if hi == nil || math.Abs(hi.ViaDrillMil-15.75) > 0.01 || math.Abs(hi.ViaDiaMil-27.56) > 0.01 {
		t.Fatalf("POWER_HI class %+v", hi)
	}
	// Low-current nets keep the board via.
	if v := it.Nets["VCC_P"].Via; v == nil || v.DrillMil != 12 || v.CountPerTransition != 1 {
		t.Fatalf("VCC_P via %+v", v)
	}
	for _, f := range it.Findings {
		if strings.HasPrefix(f.Kind, "via-") {
			t.Fatalf("sized intent raised %s: %s", f.Kind, f.Message)
		}
	}
}

// A via declared in spec.rails is rated, not resized: one 12/24 via for a
// 2 A rail is a via-undersized error.
func TestDeclaredViaUndersized(t *testing.T) {
	withSafety(t)
	it := stressIntent(t, "flyback", "spec.json", func(in *Input) {
		in.Spec.Rails = append(in.Spec.Rails, SpecRail{Net: "VOUT", Via: &SpecVia{DrillMil: 12, DiaMil: 24, Count: 1}})
	})
	v := it.Nets["VOUT"].Via
	if v == nil || v.Source != "declared" || v.CountPerTransition != 1 || v.AmpacityA >= 2 {
		t.Fatalf("declared VOUT via %+v", v)
	}
	var f *Finding
	for _, x := range it.Findings {
		if x.Kind == "via-undersized" && len(x.Nets) == 1 && x.Nets[0] == "VOUT" {
			f = x
		}
	}
	if f == nil || f.Severity != "error" || !strings.Contains(f.Suggestion, "15.8/27.6") {
		t.Fatalf("via-undersized finding %+v", f)
	}
}

func TestSpecViaValidation(t *testing.T) {
	for _, bad := range []string{
		`{"rails":[{"net":"V","via":{"drillMil":24,"diaMil":12}}]}`,
		`{"rails":[{"net":"V","via":{"count":-1}}]}`,
		`{"rules":{"viaPlatingMil":5}}`,
	} {
		if _, err := ParseSpec([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, err := ParseSpec([]byte(`{"rules":{"viaPlatingMil":1.0,"viaMarginPct":30},"rails":[{"net":"V","via":{"drillMil":15.75,"diaMil":27.56}}]}`)); err != nil {
		t.Fatal(err)
	}
}
