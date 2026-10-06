// Package rules is the single design-rule resolver of routing engine v2
// (pkg/pcbroute): DSN scope precedence, class-pair and object-type clearances,
// per-layer widths, neck-down, the via catalogue, keep-outs and board edge, and
// intent floors (spec 04 §2, §3.5–§3.6, §4; PLAN.md M2, decisions D2–D4).
//
// M0 freezes the types and the Builder/Resolver interfaces below. The DSN reader
// (M3) and the pcbauto adapter (M2) fill a Builder; every other package asks a
// Resolver. M2 provides the implementation and a constructor for the Builder.
// Lengths are int64 nanometres.
//
// NewBuilder returns a *Book, the Builder. Besides the interface it takes
// SetIntent (a net's current, voltage and neck-down intent) and SetEdge (the
// host or DSN board-edge rule), which the frozen interface has no method for.
// Build validates and freezes the Book; FromPcbauto fills one from pcbauto's
// Analysis and Stackup.
//
// Design choices that the specs leave open (M2):
//
//   - A plain (clearance X) is the Generic type. Up the scope chain, the first
//     scope that has the queried type or a Generic value decides; a typed value
//     beats the Generic one of the same scope only. Same-net specials never
//     fall back to Generic, and other same-net pairs have clearance 0.
//   - Width scopes: pcb < layer < class < class+layer < net < net+layer; the
//     intent floor and the layer's fab minimum then raise the width. Region
//     scopes affect clearance only (a width query has no position).
//   - ScopeIntent sets (net, layer or AllLayers) are floors like the derived
//     ones: only Width and Clearance are read, and both merge as max().
//   - Voltage floors apply between conductors only, never against an Area.
//     IPC-2221 rows for altitudes above 3050 m are not shipped (no input
//     carries an altitude).
//   - Neck-down (decision Q6): a net may not neck down when its intent says
//     NoNeckDown (pcbauto intent widthMil.min >= widthMil.outer, the same test
//     internal/pcb/specctra uses for its no-neck-down classes) or
//     ControlledImpedance, or when its resolved MinWidth is not below its
//     width. A DSN expresses a no-neck-down class as MinWidth = Width on that
//     class; no new DSN keyword is needed.
//   - Clearance cache: keyed by clearance profile (class plus any net-scope or
//     intent clearance input) instead of class, lock-free atomic cells.
//   - Board edge: SetEdge, else DefaultEdge (D3). FromPcbauto uses the larger
//     of the intent edge policy's outer and inner bands, since Edge has no
//     layer argument.
package rules

import "github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"

// ClassID numbers a net class. 0 is the default class.
type ClassID int32

// LayerKind classifies a copper layer.
type LayerKind uint8

const (
	Signal LayerKind = iota
	Power            // plane layer
	Mixed
)

// Layer describes one copper layer of the stack-up.
type Layer struct {
	ID       geom.LayerID
	Name     string
	Kind     LayerKind
	Outer    bool    // top or bottom copper
	CopperUm float64 // finished copper thickness, e.g. 35 outer, 17.5 inner
	MinWidth int64   // fab minimum track width on this layer (04 §3.6 layerMinWidth)
}

// ObjKind is the object type used by clearance rules.
type ObjKind uint8

const (
	Pin ObjKind = iota // through-hole pad
	SMD
	Via
	Wire
	Area // keep-out or board boundary
	TestPoint
)

// Special clearance kinds of ClrType.Special (04 §3.6).
const (
	SpecialNone uint8 = iota
	SameNetSMDVia
	SameNetViaVia
)

// ClrType is a clearance type: an object pair (A <= B) or a special kind.
type ClrType struct {
	A, B    ObjKind
	Special uint8
}

// ViaArraySpec requests a via array (04 §3.7).
type ViaArraySpec struct {
	Template   string // via padstack name
	Rows, Cols int
}

// RuleSet is one scope's rules. Only fields that are set override lower scopes.
type RuleSet struct {
	Width     *int64            // nominal track width
	MinWidth  *int64            // neck-down floor
	Clearance map[ClrType]int64 // per clearance type
	UseVia    []string          // allowed via padstack names, preference order
	ViaArray  *ViaArraySpec
	NeckZone  *int64 // max neck-down length measured from a pad edge
}

// ScopeKind lists the supported scopes, lowest precedence first (04 §2).
type ScopeKind uint8

const (
	ScopePCB ScopeKind = iota
	ScopeLayer
	ScopeClass
	ScopeClassLayer
	ScopeNet
	ScopeNetLayer
	ScopeClassClass
	ScopeClassClassLayer
	ScopeRegion
	ScopeIntent // merged as floors, never lowers an explicit rule
)

// Scope keys a RuleSet. Only the fields that Kind uses are read.
type Scope struct {
	Kind   ScopeKind
	Layer  geom.LayerID
	Class  ClassID
	Class2 ClassID // second class of a class_class scope
	Net    geom.NetID
	Region geom.Shape // ScopeRegion; Class or Net narrow it when set
}

// ViaType is one entry of the via catalogue. v2.0 uses through vias only (D8).
type ViaType struct {
	Name     string
	Pad      int64 // land diameter
	Drill    int64
	From, To geom.LayerID
}

// KeepoutKind says what a keep-out forbids.
type KeepoutKind uint8

const (
	KeepoutAll KeepoutKind = iota
	KeepoutVia
	KeepoutWire
)

// Keepout is a routing keep-out area.
type Keepout struct {
	Shape geom.Shape
	Layer geom.LayerID // geom.AllLayers for every layer
	Kind  KeepoutKind
}

// Obj is one side of a clearance query.
type Obj struct {
	Kind ObjKind
	Net  geom.NetID
	At   geom.Pt // reference point for region rules
}

// PadSize is the pad a neck-down starts from.
type PadSize struct{ Long, Narrow int64 }

// Neck is the neck-down rule at one pad (04 §3.5, decision D2). Zone 0 means
// neck-down is not allowed.
type Neck struct {
	MinWidth int64
	Zone     int64
}

// Floors are the intent-derived minimums of a net on a layer (04 §4.6).
type Floors struct {
	Width     int64 // current → width
	Clearance int64 // voltage → clearance against nets of unknown voltage
}

// Builder collects rules. Set merges rs into the scope's existing RuleSet.
type Builder interface {
	AddLayer(l Layer)
	SetClass(net geom.NetID, c ClassID)
	Set(s Scope, rs RuleSet)
	AddVia(v ViaType)
	AddKeepout(k Keepout)
	Build() (Resolver, error)
}

// Resolver answers rule queries. Results already include layer and intent floors.
// Implementations are safe for concurrent reads.
type Resolver interface {
	Layers() []Layer
	Class(net geom.NetID) ClassID
	Width(net geom.NetID, layer geom.LayerID) int64
	Neck(net geom.NetID, layer geom.LayerID, pad PadSize) Neck
	Clearance(a, b Obj, layer geom.LayerID) int64
	Vias(net geom.NetID) []ViaType // preference order
	Edge() int64                   // board-edge clearance (D3)
	Keepouts(layer geom.LayerID) []Keepout
	IntentFloors(net geom.NetID, layer geom.LayerID) Floors
}
