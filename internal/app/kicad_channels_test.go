package app

import (
	"fmt"
	"math"
	"slices"
	"testing"
)

// rowEnv: a 12-pad fine-pitch part U1 (15.7 mil pitch, 8×30 mil pads, one net
// each) with optional blocker parts above and below the row.
func rowEnv(blockGap float64) *escapeEnv {
	env := &escapeEnv{court: map[string]layoutBBox{}, netClr: map[string]float64{}, clr: 5.9, viaDia: 23.622}
	for i := 0; i < 12; i++ {
		env.pads = append(env.pads, pcbPadP{Designator: "U1", Number: fmt.Sprint(i + 1), Net: fmt.Sprintf("N%d", i), Layer: 1,
			X: float64(i) * 15.7, Y: 0, W: 8, H: 30})
	}
	if blockGap > 0 {
		for _, s := range []float64{1, -1} {
			env.pads = append(env.pads, pcbPadP{Designator: fmt.Sprintf("B%d", int(s)+1), Number: "1", Net: "BLK", Layer: 1,
				X: 85, Y: s * (15 + blockGap + 10), W: 400, H: 20})
		}
	}
	return env
}

func TestChannelOfOpenPadHasViaDirections(t *testing.T) {
	env := rowEnv(0)
	cp := defaultChannelParams(env)
	// An end pad of the row: free to the side and up/down.
	ch := env.channelOf(env.pads[0], cp)
	if ch.ViaDirs < 3 {
		t.Errorf("open end pad: viaDirs = %d, want ≥ 3 (%+v)", ch.ViaDirs, ch)
	}
	if ch.BestRun < cp.ViaDiaMil {
		t.Errorf("open end pad: bestRun %.1f < via %.1f", ch.BestRun, cp.ViaDiaMil)
	}
}

func TestChannelOfEnclosedPadIsTrapped(t *testing.T) {
	env := rowEnv(15) // blockers 15 mil above/below the pad edge: a track fits, a via does not
	cp := defaultChannelParams(env)
	pr, all := env.scorePlacement(cp)
	if pr.DenseICCount != 1 {
		t.Fatalf("dense ICs = %d, want 1", pr.DenseICCount)
	}
	if !slices.Contains(pr.Trapped, "U1.6") {
		t.Errorf("middle pad U1.6 should be trapped; trapped=%v tight=%v", pr.Trapped, pr.Tight)
	}
	if pr.EstUnrouted != len(pr.Trapped) || len(all) != 12 {
		t.Errorf("estUnrouted=%d trapped=%d measured=%d", pr.EstUnrouted, len(pr.Trapped), len(all))
	}
	// The same row with room is strictly better.
	open, _ := rowEnv(80).scorePlacement(cp)
	if !open.Better(pr) || pr.Better(open) {
		t.Errorf("open placement should rank better: open=%+v enclosed=%+v", open, pr)
	}
}

func TestChannelOfSameNetIsNotAnObstacle(t *testing.T) {
	env := rowEnv(0)
	cp := defaultChannelParams(env)
	before := env.channelOf(env.pads[5], cp)
	env.pads[6].Net = env.pads[5].Net // the right neighbour joins the pad's own net
	after := env.channelOf(env.pads[5], cp)
	if after.ViaDirs < before.ViaDirs {
		t.Errorf("a same-net neighbour must not block: before %d after %d", before.ViaDirs, after.ViaDirs)
	}
}

func TestDenseICsNeedsPitchAndPads(t *testing.T) {
	env := rowEnv(0)
	if _, ok := env.denseICs()["U1"]; !ok {
		t.Error("12 pads at 0.4 mm should be a fine-pitch IC")
	}
	for i := range env.pads {
		env.pads[i].X *= 3 // 1.2 mm pitch
	}
	if len(env.denseICs()) != 0 {
		t.Error("1.2 mm pitch is not fine-pitch")
	}
}

func TestAUC(t *testing.T) {
	if a := auc([]float64{3, 4}, []float64{1, 2}); a != 1 {
		t.Errorf("perfect separation: %v", a)
	}
	if a := auc([]float64{1, 2}, []float64{3, 4}); a != 0 {
		t.Errorf("inverted: %v", a)
	}
	if a := auc([]float64{1, 1}, []float64{1, 1}); a != 0.5 {
		t.Errorf("ties: %v", a)
	}
	if !math.IsNaN(auc(nil, []float64{1})) {
		t.Error("empty positive set should be NaN")
	}
}

func TestParsePlaceVariants(t *testing.T) {
	v, err := parsePlaceVariants("scratch, halo:40,refine,refine-halo:25.5")
	if err != nil || len(v) != 4 {
		t.Fatalf("%v %v", v, err)
	}
	if v[1].HaloMil != 40 || v[1].Refine || !v[2].Refine || v[2].HaloMil != 0 || !v[3].Refine || v[3].HaloMil != 25.5 {
		t.Errorf("%+v", v)
	}
	for _, bad := range []string{"", "halo", "halo:-3", "foo", "refine-halo:x"} {
		if _, err := parsePlaceVariants(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestRefOf(t *testing.T) {
	if refOf("U303.22") != "U303" || refOf("X") != "X" {
		t.Error("refOf")
	}
}
