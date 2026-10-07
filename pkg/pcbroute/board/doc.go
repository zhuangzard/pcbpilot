// Package board is the routing database of engine v2 (pkg/pcbroute): items with
// net, layer and fixed flags, nets split into two-terminal connections,
// union-find connectivity, copy-on-write transactions and snapshots (spec 02
// §2.1–§2.3; spec 03 §2; PLAN.md M4).
//
// M0 freezes the item and connection types and the Builder, View, DB and Txn
// interfaces below. M4 implements them. Iteration order is always ascending ID,
// never map order, so results are deterministic.
//
// Design choices of M4 that the specs leave open:
//   - NewBuilder returns the Builder. Items Fixed at Build go into the static
//     R-tree of geom.Index and can never be removed; every other item, and
//     everything added later (Fixed or not), goes into the bucket grid, whose
//     cell is 4 × (default width + wire-wire clearance) (spec 01 §2.3), 1 mm
//     without rules. A multi-layer item has one index entry per layer.
//   - Copper joins when two same-net items share a layer and are closer than
//     1 nm (touching counts), and all pieces of one pin (same Ref) are one
//     piece. Keep-outs, the edge and net-0 items never conduct. Connectivity
//     is a union-find per net, computed on demand and cached per net and per
//     level; an edit drops only the cache of the nets it touches.
//   - Build splits each net into the edges of a minimum spanning tree (Prim)
//     over its islands that hold a pad or a zone, with centre-to-centre
//     distance between the closest terminals. Wiring present at Build joins
//     islands, so it saves connections.
//   - One transaction chain is open at a time; a Txn being nested into is
//     read-only until its child ends. A DB or Txn is not safe for concurrent
//     use: reads fill the connectivity cache. Item IDs are never reused once
//     committed; an overlay allocates after its parent's last ID.
//   - Add with Owner set appends to that connection's route, Remove takes the
//     item out of its route (an empty route becomes nil), and SetRoute numbers
//     the new route's items, so Item.Owner and Route.Items always agree.
//   - A Snapshot is the per-connection route pointers only (congestion state
//     lives with the negotiation, spec 02 §2.3). Restore re-inserts snapshot
//     items under their original IDs.
package board

import (
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// ItemID numbers an item; ConnID numbers a connection. 0 means "none" for both.
type (
	ItemID uint32
	ConnID uint32
)

// Kind is an item kind.
type Kind uint8

const (
	Pad Kind = iota
	Via
	Track
	Keepout
	Edge
	Zone
)

// Item is an immutable piece of the board. An edit replaces it. A track's
// width is its geom.Seg's 2·HalfW; a via's land is its geom.Circle.
type Item struct {
	ID        ItemID
	Kind      Kind
	Net       geom.NetID
	From, To  geom.LayerID // layer span; From == To on one layer
	Shape     geom.Shape
	Fixed     bool   // never moved or ripped
	SoftFixed bool   // fan-out stubs and vias: ripped only at a cost
	Owner     ConnID // connection whose route holds the item, 0 otherwise
	Ref       string // pad reference such as "U1-3"; empty for other kinds
}

// Terminal is one end of a connection: a pad or same-net copper.
type Terminal struct {
	Item ItemID
	At   geom.Pt
}

// Route is the tracks and vias that realise one connection. Routes are
// immutable; snapshots share them by pointer.
type Route struct{ Items []Item }

// Connection is one two-terminal edge of a net's spanning tree (spec 02 §2.2).
type Connection struct {
	ID       ConnID
	Net      geom.NetID
	From, To Terminal
	Route    *Route // nil while unrouted
	RipCount int
	Locked   bool // user or fixed copper: never ripped
	Priority float64
}

// Snapshot holds every connection's route and the congestion state (spec 02
// §2.3). It is opaque; DB.Restore puts it back.
type Snapshot struct{ routes []*Route }

// Builder loads a board. The DSN reader (M3) and the pcbauto adapter (M8) use it.
// Build splits every net into connections.
type Builder interface {
	AddNet(name string) geom.NetID
	AddItem(it Item) ItemID
	Build(rs rules.Resolver) (DB, error)
}

// View is the read side shared by DB and Txn.
type View interface {
	NumNets() int
	NetName(n geom.NetID) string
	Item(id ItemID) (Item, bool)
	// Items calls fn for every item until fn returns false.
	Items(fn func(Item) bool)
	// Query calls fn for every item on layer whose bounds meet r until fn returns false.
	Query(r geom.Rect, layer geom.LayerID, fn func(Item) bool)
	Connections() []Connection
	Connection(id ConnID) (Connection, bool)
	// Connected reports whether two items are joined by copper.
	Connected(a, b ItemID) bool
}

// DB is the committed board.
type DB interface {
	View
	Begin() Txn
	Snapshot() Snapshot
	Restore(s Snapshot)
}

// Txn is a copy-on-write overlay. Reads see its own edits. Begin nests another
// overlay (shove); Commit folds it into its parent, Rollback drops it.
type Txn interface {
	View
	Add(it Item) ItemID
	Remove(id ItemID)
	// SetRoute replaces a connection's route; nil rips it up.
	SetRoute(c ConnID, r *Route)
	Begin() Txn
	Commit()
	Rollback()
}
