package app

// kicad_route_candidates.go — running more than one routing backend and
// choosing which result goes through the full gates. Nothing here relaxes a
// gate: a candidate only decides who is tried first; the winner still passes
// every gate of the run, and a candidate that fails them hands over to the
// next one.

import (
	"context"
	"fmt"
	"sort"
)

// routeCandidate is one backend's attempt.
type routeCandidate struct {
	Name string
	// exec runs the backend until ctx ends. It must not touch shared run
	// state: the caller commits the result afterwards.
	exec func(ctx context.Context) (*routeResult, error)
	// commit makes the result the run's current one and returns the
	// routed board (fastroute: SES import + escapes; tracemaker: the board
	// it wrote). Called on the run's own goroutine only.
	commit func(res *routeResult) (string, error)

	Res       *routeResult
	Err       error
	Cancelled bool // stopped because another candidate already passed the quick check
	Board     string
	Quick     bool   // complete and no routing-caused DRC error
	QuickNote string // why the quick check failed
	NewDRC    int    // DRC errors not present on the input board
	Tried     bool   // the post-route pipeline has run on it
	FullPass  bool
}

// quickCheck judges a materialised candidate: complete (no unrouted
// connection) and no DRC error the input board did not already have.
func quickCheck(res *routeResult, newDRC int) (bool, string) {
	switch {
	case res == nil:
		return false, "no result"
	case res.UnroutedCount > 0:
		return false, fmt.Sprintf("%d connection(s) unrouted", res.UnroutedCount)
	case res.Fixable > 0:
		return false, fmt.Sprintf("%d fixable violation(s)", res.Fixable)
	case newDRC > 0:
		return false, fmt.Sprintf("%d new DRC error(s)", newDRC)
	}
	return true, ""
}

// raceCandidates runs every candidate concurrently. Each result is
// committed and quick-checked on the caller's goroutine as it arrives; the
// first one that passes the quick check wins and the others are cancelled.
// check receives the committed candidate and fills Quick / NewDRC.
func raceCandidates(ctx context.Context, cands []*routeCandidate, check func(c *routeCandidate)) *routeCandidate {
	type done struct {
		c   *routeCandidate
		res *routeResult
		err error
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch := make(chan done, len(cands))
	for _, c := range cands {
		go func(c *routeCandidate) {
			res, err := c.exec(cctx)
			ch <- done{c, res, err}
		}(c)
	}
	var winner *routeCandidate
	for range cands {
		d := <-ch
		c := d.c
		if d.err != nil {
			c.Err = d.err
			c.Cancelled = cctx.Err() != nil && ctx.Err() == nil
			continue
		}
		if winner != nil { // finished after the winner: kept as a fallback, not committed
			c.Res = d.res
			continue
		}
		c.Res = d.res
		board, err := c.commit(d.res)
		if err != nil {
			c.Err = err
			continue
		}
		c.Board = board
		check(c)
		if c.Quick {
			winner = c
			cancel()
		}
	}
	return winner
}

// rankCandidates orders the candidates to try through the full gates: the
// quick-check winner first, then complete before incomplete, fewer unrouted,
// fewer new DRC errors, faster, by name. Candidates that produced nothing
// (error, cancelled before finishing) go last.
func rankCandidates(cs []*routeCandidate, winner *routeCandidate) []*routeCandidate {
	out := append([]*routeCandidate{}, cs...)
	score := func(c *routeCandidate) (int, int, int, float64) {
		if c == winner {
			return -1, 0, 0, 0
		}
		if c.Res == nil || c.Err != nil {
			return 2, 0, 0, 0
		}
		return 0, c.Res.UnroutedCount, c.NewDRC, c.Res.Seconds
	}
	sort.SliceStable(out, func(i, j int) bool {
		a0, a1, a2, a3 := score(out[i])
		b0, b1, b2, b3 := score(out[j])
		switch {
		case a0 != b0:
			return a0 < b0
		case a1 != b1:
			return a1 < b1
		case a2 != b2:
			return a2 < b2
		case a3 != b3:
			return a3 < b3
		}
		return out[i].Name < out[j].Name
	})
	return out
}
