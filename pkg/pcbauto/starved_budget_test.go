package pcbauto

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Safety and electrical results must not depend on the time budget
// (priority: safety > electrical > DRC > completion > aesthetics). A loaded
// `go test ./...` cut the placer's anneal short on the mains/SELV board and
// the routed copper kept AC_N↔GND surface paths of 155.9–178.4 mil below the
// 181.1 mil reinforced creepage, and AC_N↔ZC paths of 179.6/180.2 mil round
// a slot end (2026-09-30). The cause was not a starved repair but the
// router's fence: its straight-line territory split cannot keep the pair
// distance to a bridge part's pinched pads, and its neck allowance was one
// number per pad. A starved placement merely produced the geometry.
//
// Reproduced deterministically: a starved anneal (Moves) on fixed seeds —
// the cases the base engine failed — routed on a full, a starved and a
// tiny work budget. Whatever the budget, the routed copper meets every
// insulation pair and the edge bands; what the budget did not finish leaves
// the board explicitly not deliverable, with its reasons.
func TestStarvedBudgetMainsSelvStaysSafe(t *testing.T) {
	if testing.Short() {
		t.Skip("places and routes a board several times")
	}
	type tc struct {
		seed  int64
		moves int
		why   string
	}
	cases := []tc{
		{2, 1500, "base: T1.2 (AC_N) ↔ via (GND) 155.9 mil, GND neck allowance"},
		{5, 500, "base: AC_N via ↔ U1.4 (ZC) 180.2 mil round the slot end"},
	}
	for _, c := range cases {
		for _, wr := range []float64{3e6, 3e4, 3e3} {
			b := mainsSelvBoard()
			in := loadIntent(t, "iso-mains-selv.intent.json")
			power := PowerSpec{Intent: in}
			pre := Analyze(b, power, nil)
			circ := Understand(b, pre)
			pr, err := Place(b, pre, circ, nil, PlaceOptions{Seed: c.seed, Moves: c.moves, Timeout: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			res, err := Run(ctx, b, Options{Power: power, Stack: StackOptions{Force: 2}, Route: RouteOptions{Timeout: 20 * time.Second, WorkRate: wr}})
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			js := Joint(b, res.Analysis, circ, res.Stackup, res.Route, res.DRC, JointOptions{PlacementScore: -1, Overlaps: pr.Metrics.Overlaps, Isolation: res.Isolation, Edge: res.Edge})
			t.Logf("seed %d moves %d work rate %g (%s): routed %.1f%%, DRC %d, isolation %d, edge errors %d, deliverable %v %v",
				c.seed, c.moves, wr, c.why, res.Route.Stats.Completion, len(res.DRC.Violations), len(res.Isolation.Findings), res.Edge.Errors(), js.Deliverable, js.NotDeliverable())
			for _, f := range res.Isolation.Findings {
				t.Errorf("seed %d work rate %g: %s", c.seed, wr, f.Message)
			}
			if res.Edge == nil || res.Edge.Errors() > 0 {
				t.Errorf("seed %d work rate %g: board-edge errors %+v", c.seed, wr, res.Edge)
			}
			assertVerdict(t, res, js)
		}
	}
}

// The 4-layer mains/SELV board on a tiny budget: the HV edge bands, the
// planes' inset and the isolation hold; incompletion is a stated reason.
func TestStarvedBudgetEdgeBands(t *testing.T) {
	if testing.Short() {
		t.Skip("routes a board")
	}
	for _, wr := range []float64{3e6, 3e3} {
		b := mainsSelvBoard()
		in := loadIntent(t, "iso-mains-selv.intent.json")
		res, err := Run(context.Background(), b, Options{Power: PowerSpec{Intent: in}, Stack: StackOptions{Force: 4}, Route: RouteOptions{Timeout: 20 * time.Second, WorkRate: wr}, NoEscalate: true})
		if err != nil {
			t.Fatal(err)
		}
		an := res.Analysis
		js := Joint(b, an, Understand(b, an), res.Stackup, res.Route, res.DRC, JointOptions{PlacementScore: -1, Isolation: res.Isolation, Edge: res.Edge})
		t.Logf("work rate %g: routed %.1f%%, edge errors %d, isolation %d, %v", wr, res.Route.Stats.Completion, res.Edge.Errors(), len(res.Isolation.Findings), js.NotDeliverable())
		if res.Edge.Errors() > 0 {
			t.Errorf("work rate %g: edge errors %+v", wr, res.Edge.Findings)
		}
		if len(res.Isolation.Findings) > 0 {
			t.Errorf("work rate %g: isolation %+v", wr, res.Isolation.Findings)
		}
		assertVerdict(t, res, js)
	}
}

// A current-carrying transition whose routing budget is spent before the
// via-array fix-up — the state a loaded machine or a slow board reached
// (base: "no time left in the routing budget for alternatives", one via
// carrying 0.89 A of 2 A) — on the wall clock and on the virtual clock: the
// fix-up runs on its own work budget and the array completes, or the
// shortfall is an explicit electrical blocker.
func TestStarvedBudgetViaArrays(t *testing.T) {
	if testing.Short() {
		t.Skip("routes a board")
	}
	defer func() { auditHook = nil }()
	for _, wr := range []float64{0, 3e6} {
		b, an, st := viaWindowBoard([]Rect{{980, 280, 1020, 320}, {850, 500, 1150, 700}})
		auditHook = func(phase string, r *router) {
			if phase == "tune" {
				r.deadline = r.now().Add(-time.Hour)
			}
		}
		rr, err := Route(context.Background(), b, st, an, RouteOptions{Timeout: 20 * time.Second, WorkRate: wr})
		auditHook = nil
		if err != nil {
			t.Fatal(err)
		}
		if rr.Stats.Completion < 100 {
			t.Fatalf("rate %g: completion %.1f%%: %v", wr, rr.Stats.Completion, rr.Unrouted)
		}
		errs := 0
		for _, f := range CheckViaCurrent(b, rr.Tracks, rr.Vias, intentFromAnalysis(an)).Findings {
			if f.Level == "ERROR" {
				errs++
				t.Errorf("rate %g: via-current ERROR left: %s", wr, f.Message)
			}
		}
		_, elec := deliveryBlockers(nil, nil, rr)
		t.Logf("rate %g: routed %.1f%%, via-current errors %d, shortfalls %d, blockers %v", wr, rr.Stats.Completion, errs, len(rr.ViaShortfalls), elec)
		for _, s := range rr.ViaShortfalls {
			if strings.Contains(strings.Join(s.Tried, ";"), "no time left") {
				t.Errorf("rate %g: the fix-up was skipped for time: %+v", wr, s)
			}
		}
		if len(rr.ViaShortfalls) > 0 && len(elec) == 0 {
			t.Errorf("rate %g: via shortfalls without an electrical blocker", wr)
		}
	}
}

// assertVerdict: every safety/electrical finding is a stated blocker, and
// the joint verdict is deliverable exactly when nothing at all blocks it.
func assertVerdict(t *testing.T, res *Result, js *JointScore) {
	t.Helper()
	sg, eb := deliveryBlockers(res.Isolation, res.Edge, res.Route)
	if len(res.Blockers) != len(sg)+len(eb) {
		t.Errorf("result blockers %v, expected %v + %v", res.Blockers, sg, eb)
	}
	why := js.NotDeliverable()
	if js.Deliverable != (len(why) == 0) {
		t.Errorf("deliverable %v but reasons %v", js.Deliverable, why)
	}
	for _, b := range res.Blockers {
		found := false
		for _, w := range why {
			found = found || w == b
		}
		if !found {
			t.Errorf("blocker %q missing from the joint verdict %v", b, why)
		}
	}
	if res.Route.Stats.Completion < 100 {
		if js.Deliverable || !strings.Contains(strings.Join(why, ";"), "routing incomplete") {
			t.Errorf("%.1f%% routed: verdict %v %v", res.Route.Stats.Completion, js.Deliverable, why)
		}
	}
}
