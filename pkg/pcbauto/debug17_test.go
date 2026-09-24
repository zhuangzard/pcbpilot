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
	defer func() { negotiateNoKeep, negotiateNoStall, repairWholeNet = false, false, false }()
	for _, m := range modes {
		negotiateNoKeep = m == "old"
		negotiateNoStall = m == "nostall"
		repairWholeNet = m == "wholenet"
		repairNarrow = m == "narrow"
		repairNoSwap = m == "noswap"
		repairLocalRounds = 2
		if m == "round0" {
			repairLocalRounds = 1
		}
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
		if nets := os.Getenv("PCBAUTO_KB_WATCH"); nets != "" {
			auditHook = func(phase string, r *router) {
				for _, name := range strings.Split(nets, ",") {
					if n := r.byName[name]; n != nil && name == "GND" {
						t.Logf("   [%s] GND onPlane=%v poured=%v fanVias=%d", phase, n.onPlane, n.poured, len(n.fanVias))
					}
					if n := r.byName[name]; n != nil {
						t.Logf("   [%s] %s paths %d failed %v", phase, name, len(n.paths), n.failed)
					}
				}
			}
			defer func() { auditHook = nil }()
		}
		if os.Getenv("PCBAUTO_KB_LIST") != "" {
			repairLocalHook = func(net string, hit, kept, groups int, ok bool, failed []Unrouted) {
				t.Logf("   local repair %s: ripped %d kept %d groups %d ok=%v failed=%v", net, hit, kept, groups, ok, failed)
			}
			defer func() { repairLocalHook = nil }()
			drcRepairHook = func(round int, vs []Violation) {
				for _, v := range vs {
					t.Logf("   repair round %d: %s %s/%s at %.1f,%.1f need %.2f got %.2f", round, v.Kind, v.NetA, v.NetB, v.At.X, v.At.Y, v.Required, v.Gap)
				}
			}
			defer func() { drcRepairHook = nil }()
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
		if os.Getenv("PCBAUTO_KB_LIST") != "" {
			t.Logf("   pre-repair violations %d, repaired nets %d", res.Route.Stats.PreRepairViolations, res.Route.Stats.Repaired)
			for _, u := range res.Route.Unrouted {
				t.Logf("   unrouted %s %v %s", u.Net, u.Pads, u.Reason)
			}
		}
		for _, n := range res.Route.Notes {
			if strings.HasPrefix(n, "negotiation") || strings.Contains(n, "bridging") {
				t.Log("   ", n)
			}
		}
	}
}
