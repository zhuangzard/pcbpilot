package pcbauto

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Keep-best / legalisation-reserve A/B on a fixture (BGA off).
func TestDebugKeepBest(t *testing.T) {
	f := os.Getenv("PCBAUTO_KB")
	if f == "" {
		t.Skip()
	}
	to := 3 * time.Minute
	if v := os.Getenv("PCBAUTO_KB_T"); v != "" {
		to, _ = time.ParseDuration(v)
	}
	modes := []string{"new"}
	if os.Getenv("PCBAUTO_KB_OLD") != "" {
		modes = []string{"old", "new"}
	}
	if v := os.Getenv("PCBAUTO_KB_MODES"); v != "" {
		modes = strings.Split(v, ",")
	}
	defer func() { negotiateNoKeep, negotiateNoStall = false, false }()
	for _, m := range modes {
		negotiateNoKeep = m == "old"
		negotiateNoStall = m == "nostall"
		var b *Board
		if f == "synthetic" {
			b = bgaBoard()
		} else {
			b = loadFixture(t, f)
		}
		auditHook = func(phase string, r *router) {
			if phase != "negotiate" && phase != "pourRepair" {
				return
			}
			g := r.byName["GND"]
			if g == nil {
				return
			}
			exp := make([]int, len(r.gr.use))
			for _, n := range r.nets {
				for _, i := range n.fixed {
					exp[i]++
				}
				for _, i := range n.claims {
					exp[i]++
				}
			}
			over, under := 0, 0
			for i, u := range r.gr.use {
				if int(u) > exp[i] {
					over++
				} else if int(u) < exp[i] {
					under++
				}
			}
			t.Logf("   [%s] grid use vs claims: %d cells over, %d under", phase, over, under)
			t.Logf("   [%s] GND fanVias %d islands %d paths %d fixed %d", phase, len(g.fanVias), len(r.simulate(g)), len(g.paths), len(g.fixed))
		}
		res, err := Run(context.Background(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, NoEscalate: true, Route: RouteOptions{Timeout: to}})
		auditHook = nil
		if err != nil {
			t.Fatal(err)
		}
		c := Understand(b, res.Analysis)
		js := Joint(b, res.Analysis, c, res.Stackup, res.Route, res.DRC, JointOptions{PlacementScore: -1})
		reasons := map[string]int{}
		for _, u := range res.Route.Unrouted {
			reasons[u.Reason]++
		}
		t.Logf("%s %s: completion %.1f%% planeOpen %d/%d joint %.1f DRC %d iters %d trace %v", f, m, res.Route.Stats.Completion, js.PlaneOpen, js.PlanePads, js.Overall, len(res.DRC.Violations), res.Route.Stats.Iterations, res.Route.Stats.ConflictTrace)
		t.Logf("   reasons %v keptOnTimeout %d", reasons, res.Route.Stats.KeptOnTimeout)
		nl := map[string]int{}
		for _, u := range res.Route.Unrouted {
			if u.Reason == "no-legal-path" {
				nl[u.Net]++
			}
		}
		t.Logf("   no-legal-path by net %v", topN(nl, 6))
		for _, n := range res.Route.Notes {
			if strings.HasPrefix(n, "negotiation") || strings.Contains(n, "bridging") {
				t.Log("   ", n)
			}
		}
	}
}
