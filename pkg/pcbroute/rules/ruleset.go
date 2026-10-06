package rules

import (
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// AnyObj stands for every object kind. Generic is the clearance type of a
// plain (clearance X) rule without a (type …): it applies to every pair that
// has no typed value at the same scope (04 §3.6).
const AnyObj ObjKind = 255

// Generic is the untyped clearance key of a RuleSet.
var Generic = ClrType{A: AnyObj, B: AnyObj}

// DefaultEdge is the board-edge clearance when neither the host nor the DSN
// gives one (decision D3): 0.25 mm.
const DefaultEdge int64 = 250_000

// maxNeckZone caps a neck-down zone at 3 mm (04 §3.5 range 0–3 mm).
const maxNeckZone int64 = 3_000_000

// Book is the Builder implementation: an unresolved collection of scopes,
// layers, vias, keep-outs and per-net intent. Build freezes it into a
// Resolver. A Book is not safe for concurrent use.
type Book struct {
	layers   []Layer
	class    map[geom.NetID]ClassID
	scopes   map[scopeKey]*RuleSet
	regions  []region
	vias     []ViaType
	keepouts []Keepout
	intent   map[geom.NetID]Intent
	edge     *int64
	warnings []string
}

// NewBuilder returns an empty Book.
func NewBuilder() *Book {
	return &Book{
		class:  map[geom.NetID]ClassID{},
		scopes: map[scopeKey]*RuleSet{},
		intent: map[geom.NetID]Intent{},
	}
}

var _ Builder = (*Book)(nil)

// AddLayer adds a copper layer. Layers are listed top to bottom.
func (b *Book) AddLayer(l Layer) { b.layers = append(b.layers, l) }

// SetClass puts a net into a class.
func (b *Book) SetClass(net geom.NetID, c ClassID) { b.class[net] = c }

// Set merges rs into scope s: fields set in rs override, clearance entries
// are merged per type. Region scopes are kept in insertion order.
func (b *Book) Set(s Scope, rs RuleSet) {
	if s.Kind == ScopeRegion {
		b.regions = append(b.regions, region{shape: s.Region, class: s.Class, net: s.Net, rs: rs.clone()})
		return
	}
	k := keyOf(s)
	cur := b.scopes[k]
	if cur == nil {
		cur = &RuleSet{}
		b.scopes[k] = cur
	}
	cur.merge(&rs)
}

// AddVia adds a via padstack to the catalogue.
func (b *Book) AddVia(v ViaType) { b.vias = append(b.vias, v) }

// AddKeepout adds a keep-out area.
func (b *Book) AddKeepout(k Keepout) { b.keepouts = append(b.keepouts, k) }

// SetIntent records a net's electrical intent (04 §4.6); its derived width
// and clearance are floors, never lowering an explicit rule.
func (b *Book) SetIntent(net geom.NetID, in Intent) { b.intent[net] = in }

// SetEdge sets the board-edge clearance taken from the host or DSN rule (D3).
func (b *Book) SetEdge(nm int64) { b.edge = &nm }

// Warnings lists what the last Build reported: rule conflicts resolved by an
// intent floor (once per net) and nets flagged for human review.
func (b *Book) Warnings() []string { return b.warnings }

// merge overlays the fields that o sets.
func (r *RuleSet) merge(o *RuleSet) {
	if o.Width != nil {
		r.Width = ptr(*o.Width)
	}
	if o.MinWidth != nil {
		r.MinWidth = ptr(*o.MinWidth)
	}
	if o.NeckZone != nil {
		r.NeckZone = ptr(*o.NeckZone)
	}
	if o.UseVia != nil {
		r.UseVia = append([]string(nil), o.UseVia...)
	}
	if o.ViaArray != nil {
		va := *o.ViaArray
		r.ViaArray = &va
	}
	for t, v := range o.Clearance {
		if r.Clearance == nil {
			r.Clearance = map[ClrType]int64{}
		}
		r.Clearance[canon(t)] = v
	}
}

func (r RuleSet) clone() RuleSet {
	var c RuleSet
	c.merge(&r)
	return c
}

// clr returns the scope's value for t, falling back to its generic value.
func (r *RuleSet) clr(t ClrType) (int64, bool) {
	if r == nil || r.Clearance == nil {
		return 0, false
	}
	if v, ok := r.Clearance[t]; ok {
		return v, true
	}
	if t.Special != SpecialNone {
		return 0, false // a same-net type never falls back to the generic value
	}
	v, ok := r.Clearance[Generic]
	return v, ok
}

// canon orders the pair (A <= B); a special type ignores A and B.
func canon(t ClrType) ClrType {
	if t.Special != SpecialNone {
		return ClrType{Special: t.Special}
	}
	if t.A > t.B {
		t.A, t.B = t.B, t.A
	}
	return t
}

func ptr(v int64) *int64 { return &v }
