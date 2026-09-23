package pcbauto

import (
	"math"
	"math/rand"
	"testing"
)

// Incremental RUDY updates (begin → move → commit, then revert or done) must
// leave the same grid as a full rebuild; drift would silently skew annealing.
func TestRudyIncrementalMatchesRebuild(t *testing.T) {
	b := buckBoard()
	an := Analyze(b, PowerSpec{}, nil)
	region := Rect{0, 0, 2000, 1500}
	r := newRudy(b, an, region, 1)
	rng := rand.New(rand.NewSource(7))
	var movable []*Part
	for _, p := range b.Parts {
		movable = append(movable, p)
	}
	for step := 0; step < 400; step++ {
		p := movable[rng.Intn(len(movable))]
		old, oldRot := p.Pos, p.Rotation
		r.begin([]*Part{p})
		p.MoveTo(Point{rng.Float64() * 1800, rng.Float64() * 1300}, float64(90*rng.Intn(4)))
		r.commit()
		if rng.Intn(2) == 0 {
			p.MoveTo(old, oldRot)
			r.revert()
		} else {
			r.done()
		}
	}
	fresh := newRudy(b, an, region, 1)
	for k := range r.dem {
		if math.Abs(r.dem[k]-fresh.dem[k]) > 1e-6 {
			t.Fatalf("cell %d drifted: %.9f vs %.9f", k, r.dem[k], fresh.dem[k])
		}
	}
	if d := math.Abs(r.total() - fresh.total()); d > 1e-6 {
		t.Fatalf("total drifted by %g", d)
	}
}
