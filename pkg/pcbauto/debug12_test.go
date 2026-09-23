package pcbauto

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestDebugK230Planes(t *testing.T) {
	f := os.Getenv("PCBAUTO_PLANES")
	if f == "" {
		t.Skip()
	}
	b := loadFixture(t, f)
	res, err := Run(context.Background(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, NoEscalate: true,
		Route: RouteOptions{Timeout: 3 * time.Minute, BGA: true}})
	if err != nil {
		t.Fatal(err)
	}
	js := Joint(b, res.Analysis, Understand(b, res.Analysis), res.Stackup, res.Route, res.DRC, JointOptions{PlacementScore: -1})
	reasons := map[string]int{}
	for _, u := range res.Route.Unrouted {
		reasons[u.Reason+" "+func() string {
			if js != nil && res.Analysis.Plan(u.Net, b.Rules).Plane {
				return "(plane)"
			}
			return ""
		}()]++
	}
	t.Logf("stack %s completion %.1f%% DRC %d planeOpen %d/%d joint %.1f millis %d", stackLabel(res.Stackup), res.Route.Stats.Completion,
		len(res.DRC.Violations), js.PlaneOpen, js.PlanePads, js.Overall, res.Route.Stats.Millis)
	t.Logf("reasons %v", reasons)
	for _, n := range res.Route.Notes {
		if len(n) > 3 && (n[:3] == "BGA" || n[:4] == "pour" || n[:7] == "timeout") {
			t.Log("  ", n)
		}
	}
}
