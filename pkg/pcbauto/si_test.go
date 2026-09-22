package pcbauto

import (
	"testing"
	"time"
)

func TestSIOnMIPIBoard(t *testing.T) {
	if testing.Short() {
		t.Skip("routes a board")
	}
	b := loadFixture(t, "lckfb-mipi-3in1-adapter.json")
	out, err := Run(t.Context(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, Route: RouteOptions{Timeout: 2 * time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	si := CheckSI(b, out.Analysis, out.Stackup, out.Route)
	for _, p := range si.Pairs {
		t.Logf("pair %s/%s skew %.0f mil (limit %.0f)", p.P, p.N, p.SkewMil, p.LimitMil)
	}
	for _, f := range si.Findings {
		t.Logf("finding %+v", f)
	}
	t.Logf("completion %.1f%%", out.Route.Stats.Completion)
	for _, n := range out.Route.Notes {
		t.Logf("note %s", n)
	}
	if len(si.Pairs) == 0 {
		t.Fatal("MIPI pairs not measured")
	}
}
