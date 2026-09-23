package pcbauto

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDebugBGACoverage(t *testing.T) {
	if os.Getenv("PCBAUTO_BGACOV") == "" {
		t.Skip()
	}
	for _, f := range []string{"lckfb-k230-canmv.json", "lckfb-rk3568-4layer.json"} {
		b := loadFixture(t, f)
		res, err := Run(context.Background(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, NoEscalate: true,
			Route: RouteOptions{Timeout: 5 * time.Second, BGA: true}})
		if err != nil {
			t.Fatal(err)
		}
		rs := map[string]int{}
		for _, u := range res.Route.Unrouted {
			if strings.Contains(u.Reason, "drc") {
				rs[u.Reason]++
			}
		}
		t.Logf("%s: completion %.1f%% DRC %d, drc-dropped %v", f, res.Route.Stats.Completion, len(res.DRC.Violations), rs)
		for _, n := range res.Route.Notes {
			if strings.HasPrefix(n, "BGA") && (strings.Contains(n, "dog-bone vias") || strings.Contains(n, "escape:")) {
				t.Logf("%s: %s", f, n)
			}
		}
	}
}
