package pcbauto

import (
	"math"
	"testing"
	"time"
)

func TestPlaceScoreOrder(t *testing.T) {
	// Gas Module V5 seeds (A start): compact seed 2 must beat seed 3.
	s2 := PlaceMetrics{WirelengthIn: 121.9, TetherExcessMil: 11000, CriticalExcessMil: 9876}
	s3 := PlaceMetrics{WirelengthIn: 136.5, TetherExcessMil: 7500, CriticalExcessMil: 6314}
	i2, sc2 := placeScore(s2)
	i3, sc3 := placeScore(s3)
	if !better(SeedTrial{Illegal: i2, Score: sc2}, SeedTrial{Illegal: i3, Score: sc3}) {
		t.Fatalf("seed 2 (%.1f) should beat seed 3 (%.1f)", sc2, sc3)
	}
	// Any illegal placement loses to a legal one, however short.
	bad := PlaceMetrics{WirelengthIn: 50, Overlaps: 1}
	ib, sb := placeScore(bad)
	if better(SeedTrial{Illegal: ib, Score: sb}, SeedTrial{Illegal: i3, Score: sc3}) {
		t.Fatal("an overlapping placement won")
	}
}

// PlaceBest runs placements concurrently; run with -race. Each copy gets its
// own analysis and circuit (sharing them crashed the annealer on a shared
// undo pose), the winner is the lowest score and the input board becomes it.
func TestPlaceBestConcurrent(t *testing.T) {
	b := aesPlaceBoard()
	prep := func(bc *Board) (*Analysis, *Circuit, error) {
		an := Analyze(bc, PowerSpec{}, nil)
		return an, Understand(bc, an), nil
	}
	r, trials, err := PlaceBest(b, prep, nil, PlaceOptions{Seed: 1, Timeout: 3 * time.Second}, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(trials) != 4 {
		t.Fatalf("trials = %d", len(trials))
	}
	best := trials[0]
	for _, tr := range trials[1:] {
		if better(tr, best) {
			best = tr
		}
	}
	if _, sc := placeScore(r.Metrics); sc != best.Score {
		t.Fatalf("kept score %.2f, best trial %.2f (seed %d)", sc, best.Score, best.Seed)
	}
	for _, p := range r.Placements {
		if q := b.Part(p.Ref); q == nil || math.Abs(q.Pos.X-p.X) > 0.01 || math.Abs(q.Pos.Y-p.Y) > 0.01 { // placements are rounded to 0.01
			t.Fatalf("board does not carry the winning placement for %s: placement %+v part %+v", p.Ref, p, q.Pos)
		}
	}
}

func TestApplyStartPoses(t *testing.T) {
	b := aesPlaceBoard()
	src := b.Clone()
	ref := src.Parts[0].Ref
	q := src.Part(ref)
	q.MoveTo(Point{q.Pos.X + 100, q.Pos.Y + 50}, q.Rotation)
	p := b.Part(ref)
	padBefore := p.Pads[0].Box.C
	if n := ApplyStartPoses(b, src); n != 1 {
		t.Fatalf("moved %d parts, want 1", n)
	}
	if p.Pos != q.Pos {
		t.Fatalf("pose not applied: %v vs %v", p.Pos, q.Pos)
	}
	if d := p.Pads[0].Box.C.Sub(padBefore); math.Abs(d.X-100) > 1e-6 || math.Abs(d.Y-50) > 1e-6 {
		t.Fatalf("pads did not follow: %v", d)
	}
}
