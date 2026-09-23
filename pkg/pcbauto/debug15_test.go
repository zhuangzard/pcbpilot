package pcbauto

import (
	"context"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// Where plane opens come from, BGA off vs on: by reason and by owning part.
func TestDebugPlaneOpenBreakdown(t *testing.T) {
	f := os.Getenv("PCBAUTO_POB")
	if f == "" {
		t.Skip()
	}
	to := 60 * time.Second
	if v := os.Getenv("PCBAUTO_POB_T"); v != "" {
		to, _ = time.ParseDuration(v)
	}
	bgaSkipEscape = os.Getenv("PCBAUTO_POB_NOESC") != ""
	defer func() { bgaSkipEscape = false }()
	modes := []bool{false, true}
	if os.Getenv("PCBAUTO_POB_ONLYBGA") != "" {
		modes = []bool{true}
	}
	for _, bga := range modes {
		b := loadFixture(t, f)
		res, err := Run(context.Background(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, NoEscalate: true, Route: RouteOptions{Timeout: to, BGA: bga}})
		if err != nil {
			t.Fatal(err)
		}
		c := Understand(b, res.Analysis)
		js := Joint(b, res.Analysis, c, res.Stackup, res.Route, res.DRC, JointOptions{PlacementScore: -1})
		planes := map[string]bool{}
		for _, l := range res.Stackup.Stack {
			for _, n := range l.Nets {
				planes[n] = true
			}
		}
		reasons, owners, nets := map[string]int{}, map[string]int{}, map[string]int{}
		ballRing := map[string]int{}
		for _, g := range detectBGAs(b) {
			for _, pd := range g.part.Pads {
				ballRing[pd.Key()] = g.depth[pd]
			}
		}
		sigRing, sigReason, sigOther, sigBGA := map[int]int{}, map[string]int{}, 0, 0
		for _, u := range res.Route.Unrouted {
			if !planes[u.Net] && res.Analysis.Plan(u.Net, b.Rules).Role != RoleGround {
				hit := false
				for _, k := range u.Pads {
					if d, ok := ballRing[k]; ok {
						sigRing[d]++
						hit = true
					}
				}
				if hit {
					sigBGA++
					r := u.Reason
					if i := strings.IndexAny(r, "(:0123456789"); i > 0 {
						r = r[:i]
					}
					sigReason[strings.TrimSpace(r)]++
				} else {
					sigOther++
				}
				continue
			}
			r := u.Reason
			if i := strings.IndexAny(r, "(:0123456789"); i > 0 {
				r = r[:i]
			}
			reasons[strings.TrimSpace(r)]++
			nets[u.Net]++
			for _, k := range u.Pads {
				owners[strings.SplitN(k, ".", 2)[0]]++
			}
		}
		t.Logf("BGA=%v completion %.1f%% planeOpen %d/%d joint %.1f", bga, res.Route.Stats.Completion, js.PlaneOpen, js.PlanePads, js.Overall)
		t.Logf("   reasons %v", reasons)
		t.Logf("   nets %v", topN(nets, 8))
		t.Logf("   owners(pads) %v", topN(owners, 10))
		t.Logf("   signal unrouted: %d with BGA balls by ring %v reasons %v; %d elsewhere", sigBGA, sigRing, sigReason, sigOther)
		var bn []string
		for _, n := range res.Route.Notes {
			if strings.Contains(n, "BGA") || strings.Contains(n, "bridging") || strings.Contains(n, "final gate") {
				bn = append(bn, n)
			}
		}
		sort.Strings(bn)
		for _, n := range bn {
			t.Log("   ", n)
		}
	}
}
