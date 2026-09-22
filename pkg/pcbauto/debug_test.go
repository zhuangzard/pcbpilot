package pcbauto

import (
	"os"
	"testing"
	"time"
)

func TestDebugESP(t *testing.T) {
	if os.Getenv("PCBAUTO_DEBUG") == "" {
		t.Skip()
	}
	auditHook = func(phase string, r *router) {
		o, m := r.audit()
		t.Logf("audit %s: overlap=%d mismatch=%d", phase, o, m)
	}
	defer func() { auditHook = nil }()
	b := loadFixture(t, os.Getenv("PCBAUTO_DEBUG"))
	_, an, res, drc := runPipeline(t, b, b.CopperLayers, 4*time.Minute)
	cnt := map[string]int{}
	for _, v := range drc.Violations {
		cnt[v.NetA+" | "+v.NetB]++
	}
	for k, v := range cnt {
		if v > 3 {
			t.Logf("%3d %s", v, k)
		}
	}
	hist := map[string]int{}
	for _, v := range drc.Violations {
		switch {
		case v.Gap < 0:
			hist["<0"]++
		case v.Gap < v.Required-2:
			hist["<req-2"]++
		default:
			hist["within 2mil"]++
		}
	}
	t.Logf("gap histogram %v", hist)
	for i, v := range drc.Violations {
		if i < 8 {
			t.Logf("%+v", v)
		}
	}
	for _, n := range []string{"GND", "+3V3", "3V3", "VBUS"} {
		if p := an.ByNet[n]; p != nil {
			t.Logf("plan %s role=%s I=%.2f w=%.1f clr=%.1f", n, p.Role, p.CurrentA, p.WidthMil, p.ClearanceMil)
		}
	}
	kinds := map[string]int{}
	for _, tr := range res.Tracks {
		kinds[tr.Kind]++
	}
	t.Logf("track kinds %v", kinds)
}
