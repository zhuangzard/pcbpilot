package rules

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// Clearance lookups are cached per clearance profile instead of per net: two
// nets share a profile when nothing but their class decides their
// clearances (no net-scope clearance, no intent voltage or clearance). The
// spec keys its cache by (classA, classB, layer, type) (04 §3.6); profiles are
// that key widened to nets with their own clearance rules.
//
// Each (layer, type) has a lazily allocated nprof×nprof table of atomic int32
// cells (-1 = not computed yet). Two readers may compute the same cell at
// once; both store the same value, so reads are lock-free and safe.

// Cacheable type slots: the 6×6 ordered kind pairs, then the two specials.
const (
	numKinds     = int(TestPoint) + 1
	numTypeSlots = numKinds*numKinds + 2
)

type clrCache struct {
	nprof  int
	tables []atomic.Pointer[[]atomic.Int32] // [layer position][type slot]
}

// initProfiles assigns clearance profiles to the default net and every known
// net, then sizes the cache.
func (t *table) initProfiles() {
	ids := map[string]int32{}
	assign := func(info *netInfo) {
		sig := t.profileSig(info)
		p, ok := ids[sig]
		if !ok {
			p = int32(len(ids))
			ids[sig] = p
		}
		info.prof = p
	}
	assign(&t.def)
	for i := range t.infos {
		assign(&t.infos[i])
	}
	t.nprof = len(ids)
	t.tables = make([]atomic.Pointer[[]atomic.Int32], len(t.layers)*numTypeSlots)
}

// profileSig lists the values, besides the class, that a net's clearances
// depend on. Nets with equal net-scope values resolve alike and share it.
func (t *table) profileSig(info *netInfo) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "c%d", info.class)
	if info.net == 0 {
		return sb.String()
	}
	write := func(tag string, rs *RuleSet) {
		if rs == nil || len(rs.Clearance) == 0 {
			return
		}
		keys := make([]ClrType, 0, len(rs.Clearance))
		for k := range rs.Clearance {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return clrLess(keys[i], keys[j]) })
		fmt.Fprintf(&sb, "|%s", tag)
		for _, k := range keys {
			fmt.Fprintf(&sb, " %v=%d", k, rs.Clearance[k])
		}
	}
	write("n", t.book.scope(scopeKey{kind: ScopeNet, net: info.net}))
	write("i*", t.book.scope(scopeKey{kind: ScopeIntent, net: info.net, layer: geom.AllLayers}))
	for _, l := range t.layers {
		write(fmt.Sprint("n", l.ID), t.book.scope(scopeKey{kind: ScopeNetLayer, net: info.net, layer: l.ID}))
		write(fmt.Sprint("i", l.ID), t.book.scope(scopeKey{kind: ScopeIntent, net: info.net, layer: l.ID}))
	}
	// Coated counts on its own: it selects B4 against the other net's voltage.
	if in := info.intent; in != nil && (in.VoltageKnown || in.VoltageV != 0 || in.Coated) {
		fmt.Fprintf(&sb, "|v%g k%t c%t", in.VoltageV, in.VoltageKnown, in.Coated)
	}
	return sb.String()
}

func clrLess(a, b ClrType) bool {
	if a.Special != b.Special {
		return a.Special < b.Special
	}
	if a.A != b.A {
		return a.A < b.A
	}
	return a.B < b.B
}

// Clearance is the minimum distance between objects a and b on a layer
// (04 §3.6 ClearanceBetween). layer AllLayers returns the largest value over
// all layers.
func (t *table) Clearance(a, b Obj, layer geom.LayerID) int64 {
	if layer == geom.AllLayers {
		var c int64
		for _, l := range t.layers {
			c = max(c, t.Clearance(a, b, l.ID))
		}
		return c
	}
	ia, ib := t.info(a.Net), t.info(b.Net)
	same := a.Net != 0 && a.Net == b.Net
	ct := clrType(a.Kind, b.Kind, same)
	if same && ct.Special == SpecialNone {
		return 0 // own copper is never an obstacle (01 §4)
	}
	li := -1
	if layer >= 0 && int(layer) < len(t.layerIdx) {
		li = int(t.layerIdx[layer])
	}
	slot := typeSlot(ct)
	if li < 0 || slot < 0 || t.inRegion(a, b) {
		return t.compute(a, b, ia, ib, ct, layer)
	}
	pa, pb := ia.prof, ib.prof
	if a.Kind > b.Kind || a.Kind == b.Kind && pa > pb {
		pa, pb = pb, pa
	}
	tp := &t.tables[li*numTypeSlots+slot]
	cells := tp.Load()
	if cells == nil {
		fresh := make([]atomic.Int32, t.nprof*t.nprof)
		for i := range fresh {
			fresh[i].Store(-1)
		}
		if !tp.CompareAndSwap(nil, &fresh) {
			cells = tp.Load()
		} else {
			cells = &fresh
		}
	}
	cell := &(*cells)[int(pa)*t.nprof+int(pb)]
	if v := cell.Load(); v >= 0 {
		return int64(v)
	}
	c := t.compute(a, b, ia, ib, ct, layer)
	if c <= math.MaxInt32 {
		cell.Store(int32(c))
	}
	return c
}

// clrType canonicalises the type of a pair; same-net pairs map to the
// same-net specials or to no type at all.
func clrType(a, b ObjKind, same bool) ClrType {
	if a > b {
		a, b = b, a
	}
	switch {
	case same && a == SMD && b == Via:
		return ClrType{Special: SameNetSMDVia}
	case same && a == Via && b == Via:
		return ClrType{Special: SameNetViaVia}
	}
	return ClrType{A: a, B: b}
}

func typeSlot(ct ClrType) int {
	switch ct.Special {
	case SameNetSMDVia:
		return numKinds * numKinds
	case SameNetViaVia:
		return numKinds*numKinds + 1
	case SpecialNone:
		if int(ct.B) < numKinds {
			return int(ct.A)*numKinds + int(ct.B)
		}
	}
	return -1
}

func (t *table) inRegion(a, b Obj) bool {
	for i := range t.book.regions {
		r := &t.book.regions[i]
		if r.applies(a, t.info(a.Net).class) || r.applies(b, t.info(b.Net).class) {
			return true
		}
	}
	return false
}

// compute is the uncached clearance: each side resolves its value up its
// scope chain, the larger side wins, a region overrides, and the intent
// floors of conductors raise the result.
func (t *table) compute(a, b Obj, ia, ib *netInfo, ct ClrType, layer geom.LayerID) int64 {
	c := max(t.side(a, ia, ib, ct, layer), t.side(b, ib, ia, ct, layer))
	found := false
	var rc int64
	for i := range t.book.regions {
		r := &t.book.regions[i]
		if !r.applies(a, ia.class) && !r.applies(b, ib.class) {
			continue
		}
		if v, ok := r.rs.clr(ct); ok {
			rc, found = max(rc, v), true
		}
	}
	if found {
		c = rc
	}
	if ct.Special != SpecialNone || a.Kind == Area || b.Kind == Area {
		return c
	}
	l := &Layer{ID: layer}
	if li := t.layerPos(layer); li >= 0 {
		l = &t.layers[li]
	}
	c = max(c, voltageFloor(ia.intent, ib.intent, l))
	for _, n := range []geom.NetID{a.Net, b.Net} {
		for _, s := range t.intentScopes(n, layer) {
			if v, ok := s.clr(ct); ok {
				c = max(c, v)
			}
		}
	}
	return c
}

// side resolves one object's clearance, highest scope first:
// class_class+L > class_class > net+L > net > class+L > class > layer > pcb.
func (t *table) side(o Obj, own, other *netInfo, ct ClrType, layer geom.LayerID) int64 {
	c1, c2 := min(own.class, other.class), max(own.class, other.class)
	for _, k := range []scopeKey{
		{kind: ScopeClassClassLayer, class: c1, class2: c2, layer: layer},
		{kind: ScopeClassClass, class: c1, class2: c2},
		{kind: ScopeNetLayer, net: o.Net, layer: layer},
		{kind: ScopeNet, net: o.Net},
		{kind: ScopeClassLayer, class: own.class, layer: layer},
		{kind: ScopeClass, class: own.class},
		{kind: ScopeLayer, layer: layer},
		{kind: ScopePCB},
	} {
		if k.net == 0 && (k.kind == ScopeNet || k.kind == ScopeNetLayer) {
			continue
		}
		if v, ok := t.book.scope(k).clr(ct); ok {
			return v
		}
	}
	return 0
}

func (t *table) layerPos(layer geom.LayerID) int {
	if layer >= 0 && int(layer) < len(t.layerIdx) {
		return int(t.layerIdx[layer])
	}
	return -1
}
