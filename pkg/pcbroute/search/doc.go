// Package search routes one two-terminal connection of routing engine v2
// (pkg/pcbroute): tile A* with the cost model, octile-plus-via heuristic and via
// moves, then corridor embedding into an octilinear track (spec 01 §3–§5;
// PLAN.md M7).
//
// M0 freezes Request, CostFn, the errors and Route's signature. M7 replaces the
// body of Route.
package search

import (
	"errors"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/tile"
)

// LayerSet is a bit set of copper layers: bit i is geom.LayerID(i).
type LayerSet uint64

// Request asks for one connection of Net from any source to any target item.
// Budget caps the node expansions; 0 picks the default.
type Request struct {
	Net              geom.NetID
	Sources, Targets []board.ItemID
	Layers           LayerSet
	Budget           int
}

// CostFn adds a congestion cost for entering t on layer (spec 02 present and
// history cost). A nil CostFn adds 0.
type CostFn func(t *tile.Tile, layer geom.LayerID) float64

// Path is a found route and its search cost.
type Path struct {
	Route board.Route
	Cost  float64
}

// Stats counts search work.
type Stats struct {
	Expanded int
	Replans  int
}

var (
	ErrNoPath = errors.New("search: no path")
	ErrBudget = errors.New("search: budget exhausted")

	errNotImplemented = errors.New("search: not implemented (PLAN.md M7)")
)

// Route searches one connection on db's committed copper.
func Route(db board.View, rs rules.Resolver, req Request, cost CostFn) (Path, Stats, error) {
	return Path{}, Stats{}, errNotImplemented
}
