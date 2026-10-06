// Package tile keeps the corner-stitched free-space planes of routing engine v2
// (pkg/pcbroute), one per (layer, inflation key), with conservative staircase
// inflation, incremental insert and delete, and via planes (spec 01 §2.4, §3.3,
// §5; PLAN.md M6).
//
// M0 freezes Tile, because search's CostFn receives one. M6 implements planes.
package tile

import (
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// Kind tells free space from obstacle.
type Kind uint8

const (
	Space Kind = iota
	Solid
)

// InflationKey selects a plane: the routed track's half-width and its clearance class.
type InflationKey struct {
	HalfWidth int64
	Class     rules.ClassID
}

// Tile is one rectangle of a plane. Owners lists the nets of a Solid tile; a
// net may pass a tile that only it owns.
type Tile struct {
	Rect     geom.Rect
	Kind     Kind
	Owners   []geom.NetID
	stitches [4]*Tile // right-top, top-right, left-bottom, bottom-left
}
