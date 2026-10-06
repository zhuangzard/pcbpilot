// Package tile keeps the corner-stitched free-space planes of routing engine v2
// (pkg/pcbroute), one per (layer, inflation key), with conservative staircase
// inflation, incremental insert and delete, and via planes (spec 01 §2.4, §3.3,
// §5; PLAN.md M6).
//
// M0 freezes Tile, because search's CostFn receives one. M6 implements planes.
//
// A Plane is a bounded corner-stitched plane (Ousterhout 1984): its bounds
// are covered exactly once by tiles, each with four stitches; a nil stitch is
// the plane boundary. Obstacles are inserted as staircases of rectangles
// (inflate.go) and removed again; Set builds and caches the planes of a board
// per (layer, inflation key) and the via planes (cache.go, viaplane.go).
//
// Design choices of M6 that the specs leave open:
//   - A tile's Owners is the sorted union of the nets of every obstacle whose
//     staircase covers it; Kind is Solid exactly when Owners is not empty.
//     Owner sets are interned, so equal sets share one slice.
//   - Every tile, Solid as well as Space, is kept in maximal horizontal
//     strips: no two side-by-side tiles have equal owners, and no two stacked
//     tiles have equal owners and equal x span. That form is unique, so a
//     plane depends only on its obstacles, never on the insertion order.
//   - An insert paints all rectangles of one obstacle and then restores the
//     canonical form once. A delete clears the obstacle's rectangles and
//     paints back every other obstacle there; obstacles are found through a
//     geom.Grid registry.
//   - Staircases: a box core (Rect, horizontal or vertical Seg, Circle,
//     axis-aligned rectangle Poly) is covered exactly per band, with
//     CornerSteps bands on each rounded side. Every other shape is covered by
//     its geom.Octagon hull cut into StairStep bands aligned to multiples of
//     the step, so refined bands nest in coarse ones. Non-convex polygons are
//     covered by their hull.
//   - Refinement (spec 01 §5) is per obstacle: an obstacle whose staircase
//     meets the window is covered again with half the step and twice the
//     corner steps, at most twice.
//   - InflationKey.Class holds the net's clearance row, not its DSN class:
//     nets that need the same clearance to every object of every other net
//     share a row (spec 01 §2.2 keys planes by "clearanceRow"). A design that
//     gives every net its own class, as the bench DSN does, would otherwise
//     need one plane per net.
//   - A plane inflates each obstacle by the key's half-width (rounded up) plus
//     the largest clearance any of the plane's nets needs to it (spec 01 §4).
//     Region rules are not applied in planes; the exact check of the
//     embedding uses them.
//   - Keep-outs come from the rules (with their kind); board Keepout items
//     duplicate them and are skipped. The outline is an obstacle of net 0
//     that also covers everything outside it, inflated by the edge clearance.
//     A round outline (a DSN circle boundary) is replaced by an inscribed
//     64-gon, which stays conservative.
//   - A via plane spans the via's layers; pours on plane layers are not via
//     obstacles (the antipad is the pour's rule, spec 01 §3.5).
//   - Set planes cover the bounds of every item and keep-out grown by 1 mm;
//     their StairStep is max(clearance/2, 25 µm) with the plane's first net's
//     clearance to a default-class wire.
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

// InflationKey selects a plane: the routed track's half-width and its clearance class
// (M6: the clearance row of Set.KeyOf, see the package doc).
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
