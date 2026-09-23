package pcbauto

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestDebugBGAReal(t *testing.T) {
	f := os.Getenv("PCBAUTO_BGA_REAL")
	if f == "" {
		t.Skip()
	}
	for _, noBGA := range []bool{true, false} {
		b := loadFixture(t, f)
		res, err := Run(context.Background(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, Route: RouteOptions{Timeout: 3 * time.Minute, BGA: !noBGA}})
		if err != nil {
			t.Fatal(err)
		}
		c := Understand(b, res.Analysis)
		js := Joint(b, res.Analysis, c, res.Stackup, res.Route, res.DRC, JointOptions{PlacementScore: -1})
		t.Logf("%s noBGA=%v stack %s completion %.1f%% DRC %d planeOpen %d/%d joint %.1f", f, noBGA, stackLabel(res.Stackup),
			res.Route.Stats.Completion, len(res.DRC.Violations), js.PlaneOpen, js.PlanePads, js.Overall)
		for _, n := range res.Route.Notes {
			if len(n) > 3 && n[:3] == "BGA" {
				t.Log("   ", n)
			}
		}
	}
}
