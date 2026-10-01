package pcbauto

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// pcieM2 loads the HS stress PCIe M.2 board (root port U1 → M.2 socket J1:
// TX through the AC caps C1/C2, RX and REFCLK direct) with its derived
// intent.
func pcieM2(t *testing.T) (*Board, *Analysis, *Circuit, PowerSpec) {
	t.Helper()
	raw, err := os.ReadFile("../../internal/app/testdata/stress/hs/pcie-m2/board.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	b.CopperLayers = 4
	ir, err := os.ReadFile("testdata/pcie-m2.intent.json")
	if err != nil {
		t.Fatal(err)
	}
	it, err := ParseIntent(ir)
	if err != nil {
		t.Fatal(err)
	}
	spec := PowerSpec{Intent: it}
	an := Analyze(b, spec, nil)
	return b, an, Understand(b, an), spec
}

// Direct pairs (connector pin → IC pin, no in-line part) keep a corridor
// too, and a part carrying several pairs feels each with 1/n of the force:
// with only the AC-coupled TX pair kept clear, the CLKREQ# pull-up R1 was
// pushed into the RX / REFCLK breakout and the root port shifted to line
// TX up with the caps (stress pcie-m2, seeds 5/6: a leg with no legal path).
func TestCorridorsCoverDirectPairs(t *testing.T) {
	b, an, c, _ := pcieM2(t)
	pl := &placer{b: b, an: an, c: c}
	pl.setupCorridors()
	got := map[string]bool{}
	for _, cr := range pl.corridors {
		got[cr.p.Nets[0]+"/"+cr.n.Nets[0]] = true
	}
	if len(pl.corridors) != 3 {
		t.Fatalf("want 3 corridors (TX chain via C1/C2, RX and REFCLK direct), got %d: %v", len(pl.corridors), got)
	}
	u1 := b.Part("U1")
	if s := pl.share(u1); math.Abs(s-1.0/3) > 1e-9 {
		t.Errorf("U1 carries 3 pairs: share %.3f, want 1/3", s)
	}
	if s := pl.share(b.Part("C1")); s != 1 {
		t.Errorf("C1 carries one pair: share %.3f, want 1", s)
	}
	// The pull-ups are foreign to every corridor.
	for _, cr := range pl.corridors {
		if cr.related[b.Part("R1")] || cr.related[b.Part("R2")] {
			t.Errorf("pull-up related to the %s corridor", cr.p.Nets[0])
		}
	}
	pl.opt.NoCorridors = true
	pl.setupCorridors()
	if len(pl.corridors) != 0 {
		t.Errorf("NoCorridors: %d corridors", len(pl.corridors))
	}
}

// abBetter ranks safety, completion, open plane connections, DRC, then —
// only where the plain placement costs no pair check — the electrical
// group, then the SI findings; a tie keeps the first (the corridors).
func TestCorridorABRanking(t *testing.T) {
	mk := func(gates int, comp float64, open, drc int, elec float64, pair, hs int) *abVariant {
		js := &JointScore{PlaneOpen: open, Groups: map[string]float64{"electrical": elec}}
		for i := 0; i < gates; i++ {
			js.Gates = append(js.Gates, "g")
		}
		out := &Result{Route: &RouteResult{}, DRC: &DRCReport{}}
		out.Route.Stats.Completion = comp
		for i := 0; i < drc; i++ {
			out.DRC.Violations = append(out.DRC.Violations, Violation{})
		}
		return &abVariant{out: out, js: js, pair: pair, hs: hs}
	}
	cases := []struct {
		name string
		x, y *abVariant
		want bool
	}{
		{"completion first", mk(0, 92.3, 0, 0, 95, 0, 0), mk(0, 100, 0, 0, 60, 3, 4), true},
		{"safety before completion", mk(0, 90, 0, 0, 80, 0, 0), mk(1, 100, 0, 0, 90, 0, 0), false},
		{"plane open", mk(0, 100, 1, 0, 90, 0, 0), mk(0, 100, 0, 0, 80, 0, 0), true},
		{"drc", mk(0, 100, 0, 0, 90, 0, 0), mk(0, 100, 0, 2, 95, 0, 0), false},
		{"electrical", mk(0, 100, 0, 0, 80, 1, 0), mk(0, 100, 0, 0, 85, 1, 0), true},
		{"electrical never at a pair check's cost", mk(0, 100, 0, 0, 75.6, 1, 0), mk(0, 100, 0, 0, 80.8, 3, 0), false},
		{"tie: fewer SI findings", mk(0, 100, 0, 0, 61.0, 1, 4), mk(0, 100, 0, 0, 61.2, 1, 3), true},
		{"tie keeps corridors", mk(0, 100, 0, 0, 85, 0, 2), mk(0, 100, 0, 0, 85.3, 0, 2), false},
	}
	for _, tc := range cases {
		if got := abBetter(tc.x, tc.y); got != tc.want {
			t.Errorf("%s: abBetter = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A placement never loses routability to the corridors: PlaceThenRoute
// routes the corridor and the plain placement and keeps the better, and
// leaves the board at the placement it reports. PCIe M.2 seed 5 on the
// virtual clock: the corridors of 3ceaf3b1 (TX only) routed 84.6 %; the
// plain placement and the corridors of all three pairs route 100 %.
func TestPlaceThenRouteNeverLosesCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("places and routes a board twice")
	}
	b, an, c, spec := pcieM2(t)
	ropt := Options{Power: spec, Stack: StackOptions{Force: 4}, Route: RouteOptions{Timeout: 2 * time.Minute, WorkRate: 3e6}}
	pr, out, err := PlaceThenRoute(context.Background(), b, an, c, nil, PlaceOptions{Seed: 5}, ropt)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Route.Stats.Completion; got < 100 {
		t.Errorf("completion %.1f%% < 100%%", got)
	}
	note := ""
	for _, n := range pr.Notes {
		if strings.HasPrefix(n, "pair corridors:") {
			note = n
		}
	}
	if note == "" {
		t.Errorf("no pair-corridor A/B note: %v", pr.Notes)
	}
	t.Log(note)
	for _, pp := range pr.Placements {
		p := b.Part(pp.Ref)
		if math.Abs(p.Pos.X-pp.X) > 0.01 || math.Abs(p.Pos.Y-pp.Y) > 0.01 || p.Rotation != pp.Rot {
			t.Errorf("%s left at %.2f,%.2f r%.0f, reported %.2f,%.2f r%.0f", pp.Ref, p.Pos.X, p.Pos.Y, p.Rotation, pp.X, pp.Y, pp.Rot)
		}
	}
}
