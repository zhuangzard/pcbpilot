// Package optimize improves routed copper of engine v2 (pkg/pcbroute): scoring,
// dangling cleanup, pull-tight, chamfers, via minimisation, reroute-improve,
// Steiner shift and tail cleanup (spec 03 §2–§5, spec 04 §5; PLAN.md M12).
//
// Rerouting goes through the injected Router, so this package builds and tests
// without search. M0 freezes Router, Weights, Budget, Report and Run's
// signature; M12 replaces the body of Run.
package optimize

import (
	"errors"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// Weights scale the router's via and length costs for a reroute; 0 means 1.
type Weights struct{ Via, Length float64 }

// Router reroutes connection c inside txn and reports success.
type Router interface {
	Reroute(txn board.Txn, c board.ConnID, w Weights) bool
}

// Budget bounds a run (spec 03 §2). An expired budget returns the best
// committed state, never a half-applied edit.
type Budget struct {
	Deadline        time.Time
	MaxRejectStreak int
}

// Report gives the unclamped score before and after the run (spec 03 §3.8).
type Report struct{ ScoreBefore, ScoreAfter float64 }

var errNotImplemented = errors.New("optimize: not implemented (PLAN.md M12)")

// Run applies the optimizer passes to db.
func Run(db board.DB, rs rules.Resolver, r Router, b Budget) (Report, error) {
	return Report{}, errNotImplemented
}
