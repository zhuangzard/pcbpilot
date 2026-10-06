package pcbauto

import (
	"fmt"
	"math"
	"sort"
	"sync"
)

// SeedTrial is one placement attempt of PlaceBest.
type SeedTrial struct {
	Seed    int64        `json:"seed"`
	Metrics PlaceMetrics `json:"metrics"`
	Score   float64      `json:"score"`
	Illegal int          `json:"illegal"`
	Err     string       `json:"error,omitempty"`
}

// placeScore ranks a placement: illegal counts first (overlaps, outside the
// board / zone, keep-out and height hits), then weighted wirelength plus
// every tether excess (critical relations included), both in inches — a
// compact placement around its cores. Weighting the critical relations ten
// times more picked a scattered placement on Gas Module V5 (seed 3: 136.5 in,
// empty corners) over the compact one the user asked for (seed 2: 121.9 in);
// the annealer already holds the critical relations by their tethers.
func placeScore(m PlaceMetrics) (illegal int, score float64) {
	illegal = m.Overlaps + m.OutOfBoard + m.OutOfZone + m.KeepoutHits + m.HeightHits
	return illegal, m.WirelengthIn + m.TetherExcessMil/1000
}

// better reports whether trial a beats trial b.
func better(a, b SeedTrial) bool {
	if a.Illegal != b.Illegal {
		return a.Illegal < b.Illegal
	}
	return a.Score < b.Score
}

// PlaceBest runs Place with seeds opt.Seed … opt.Seed+n-1 in parallel, each
// on its own copy of b, and keeps the best (placeScore). The annealer is a
// stochastic search: on Gas Module V5 one start state gave 121.9–156.5 in of
// wirelength across eight seeds, so a single seed made placement quality a
// lottery (the "first version" was seed 1 on one start; a later run on the
// same start drew 139.5). b ends up as the winning copy.
//
// The analysis and circuit hold pointers to the board's parts, so every
// copy gets its own from prep (Analyze + circuit understanding on the copy);
// sharing the caller's crashed the annealer and would move the original's
// parts.
func PlaceBest(b *Board, prep func(*Board) (*Analysis, *Circuit, error), m *Mechanics, opt PlaceOptions, n int) (*PlaceResult, []SeedTrial, error) {
	outs, trials, err := PlaceSeeds(b, prep, m, opt, n)
	if err != nil {
		return nil, trials, err
	}
	*b = *outs[0].Board
	return outs[0].Result, trials, nil
}

// SeedOutcome is one placement of PlaceSeeds: its board copy and result.
type SeedOutcome struct {
	Trial  SeedTrial
	Board  *Board
	Result *PlaceResult
}

// PlaceSeeds runs the n seeds like PlaceBest and returns every successful
// placement, best first (b itself is not changed). The best few can then be
// trial-routed: a compact placement is not always the most routable (Gas
// Module V5 B, seed 7 window: 3–4 unrouted while A's draw routed fully).
func PlaceSeeds(b *Board, prep func(*Board) (*Analysis, *Circuit, error), m *Mechanics, opt PlaceOptions, n int) ([]SeedOutcome, []SeedTrial, error) {
	if n < 1 {
		n = 1
	}
	trials := make([]SeedTrial, n)
	outs := make([]*SeedOutcome, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			o := opt
			o.Seed = opt.Seed + int64(i)
			bc := b.Clone()
			trials[i].Seed = o.Seed
			an, c, err := prep(bc)
			var r *PlaceResult
			if err == nil {
				r, err = Place(bc, an, c, m, o)
			}
			if err != nil {
				trials[i].Err, trials[i].Illegal, trials[i].Score = err.Error(), math.MaxInt32, math.Inf(1)
				return
			}
			trials[i].Metrics = r.Metrics
			trials[i].Illegal, trials[i].Score = placeScore(r.Metrics)
			outs[i] = &SeedOutcome{Trial: trials[i], Board: bc, Result: r}
		}(i)
	}
	wg.Wait()
	var ok []SeedOutcome
	for _, o := range outs {
		if o != nil {
			ok = append(ok, *o)
		}
	}
	if len(ok) == 0 {
		return nil, trials, fmt.Errorf("placement failed for every seed: %s", trials[0].Err)
	}
	sort.SliceStable(ok, func(i, j int) bool { return better(ok[i].Trial, ok[j].Trial) })
	if n > 1 {
		ok[0].Result.Notes = append(ok[0].Result.Notes, fmt.Sprintf("best of %d seeds: seed %d (score %.1f)", n, ok[0].Trial.Seed, ok[0].Trial.Score))
	}
	return ok, trials, nil
}

// ApplyStartPoses moves b's parts to the poses of the same designators in
// src (another board's snapshot: board-v6A.json, board.placed.json) so a
// placement can start from a known good layout although the two boards'
// primitiveIds differ. Fixed parts, parts missing in src and parts on the
// other side keep their pose. Returns how many moved.
func ApplyStartPoses(b, src *Board) int {
	moved := 0
	for _, p := range b.Parts {
		q := src.Part(p.Ref)
		if q == nil || p.Fixed || q.Side != p.Side {
			continue
		}
		if q.Pos != p.Pos || q.Rotation != p.Rotation {
			p.MoveTo(q.Pos, q.Rotation)
			moved++
		}
	}
	return moved
}
