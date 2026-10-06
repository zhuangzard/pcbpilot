package tile

import (
	"cmp"
	"slices"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// boundsMargin grows the board's extent into a Set's plane bounds, so the
// outline's outside band is part of every plane.
const boundsMargin int64 = 1_000_000

// keepoutID marks obstacle IDs of rule keep-outs, which are not board items.
const keepoutID uint32 = 1 << 31

// Set keeps the planes of one board, built on first use and cached per
// (layer, inflation key) (spec 01 §2.2), plus via planes (spec 01 §3.3).
// Add and Remove keep every built plane in step with routed copper. A Set is
// not safe for concurrent use.
type Set struct {
	view   board.View
	rs     rules.Resolver
	bounds geom.Rect
	kinds  map[geom.LayerID]rules.LayerKind
	planes map[planeKey]*built
	order  []*built // build order, for deterministic updates
	rows   map[rowKey][]rules.ClassID
}

// rowKey names one assignment of clearance rows: the routed object obj on
// the layers from..to.
type rowKey struct {
	obj      rules.ObjKind
	from, to geom.LayerID
}

// rowKinds are the object kinds whose clearances make up a clearance row.
var rowKinds = [...]rules.ObjKind{rules.Pin, rules.SMD, rules.Via, rules.Wire, rules.Area}

// planeKey names a plane: a track plane of Layer, or the via plane of via
// type Via (Layer is then its first layer).
type planeKey struct {
	Layer geom.LayerID
	Via   string
	Key   InflationKey
}

// built is one plane of a Set and what it needs to inflate new copper.
type built struct {
	plane    *Plane
	from, to geom.LayerID  // layers whose obstacles the plane holds
	obj      rules.ObjKind // the routed object: Wire or Via
	half     int64         // its half-width or via land radius
	nets     []geom.NetID  // nets that route on this plane, ascending
	clr      map[clrKey]int64
}

type clrKey struct {
	net   geom.NetID
	kind  rules.ObjKind
	layer geom.LayerID
}

// NewSet prepares the planes of the board in v under the rules rs. Nothing
// is built until a plane is asked for.
func NewSet(v board.View, rs rules.Resolver) *Set {
	s := &Set{view: v, rs: rs, planes: map[planeKey]*built{}, kinds: map[geom.LayerID]rules.LayerKind{},
		rows: map[rowKey][]rules.ClassID{}}
	for _, l := range rs.Layers() {
		s.kinds[l.ID] = l.Kind
	}
	v.Items(func(it board.Item) bool {
		s.bounds = s.bounds.Union(it.Shape.Bounds())
		return true
	})
	for _, l := range rs.Layers() {
		for _, k := range rs.Keepouts(l.ID) {
			s.bounds = s.bounds.Union(k.Shape.Bounds())
		}
	}
	if s.bounds.Empty() {
		s.bounds = geom.Rect{MaxX: 1, MaxY: 1}
	}
	s.bounds = s.bounds.Grow(boundsMargin)
	return s
}

// Bounds is the area every plane of the Set covers.
func (s *Set) Bounds() geom.Rect { return s.bounds }

// half is a width's half, rounded up so the inflation stays conservative.
func half(w int64) int64 { return (w + 1) / 2 }

// KeyOf is the inflation key of net on layer: its track half-width there and
// its clearance row (see row).
func (s *Set) KeyOf(net geom.NetID, layer geom.LayerID) InflationKey {
	return InflationKey{HalfWidth: half(s.rs.Width(net, layer)), Class: s.row(rules.Wire, layer, layer, net)}
}

// row is the clearance row of net for the routed object obj on layers
// from..to (spec 01 §2.2 "clearanceRow"): nets get the same row when they
// need the same clearance to every object kind of every other net, so nets
// of different DSN classes with equal rules share planes. Rows are numbered
// from 0 in ascending net order. A net joins the first row whose first net
// agrees with it everywhere except on the two nets themselves; a plane
// inflates by the largest clearance of its nets, so a looser grouping could
// only cost tightness, never conservativeness.
func (s *Set) row(obj rules.ObjKind, from, to geom.LayerID, net geom.NetID) rules.ClassID {
	k := rowKey{obj, from, to}
	if ids := s.rows[k]; ids != nil {
		return ids[net]
	}
	n := s.view.NumNets()
	vec := make([][]int64, n+1)
	for a := range vec {
		for l := from; l <= to; l++ {
			for m := 0; m <= n; m++ {
				for _, kd := range rowKinds {
					vec[a] = append(vec[a], s.rs.Clearance(rules.Obj{Kind: obj, Net: geom.NetID(a)}, rules.Obj{Kind: kd, Net: geom.NetID(m)}, l))
				}
			}
		}
	}
	agree := func(a, r int) bool {
		for i := range vec[a] {
			if m := i / len(rowKinds) % (n + 1); m != a && m != r && vec[a][i] != vec[r][i] {
				return false
			}
		}
		return true
	}
	ids := make([]rules.ClassID, n+1)
	var firsts []int
next:
	for a := range vec {
		for i, r := range firsts {
			if agree(a, r) {
				ids[a] = rules.ClassID(i)
				continue next
			}
		}
		ids[a] = rules.ClassID(len(firsts))
		firsts = append(firsts, a)
	}
	s.rows[k] = ids
	return ids[net]
}

// Keys lists the distinct inflation keys of all nets on layer, ordered by
// half-width, then clearance row.
func (s *Set) Keys(layer geom.LayerID) []InflationKey {
	var ks []InflationKey
	for n := 1; n <= s.view.NumNets(); n++ {
		ks = append(ks, s.KeyOf(geom.NetID(n), layer))
	}
	slices.SortFunc(ks, cmpKey)
	return slices.Compact(ks)
}

func cmpKey(a, b InflationKey) int {
	return cmp.Or(cmp.Compare(a.HalfWidth, b.HalfWidth), cmp.Compare(a.Class, b.Class))
}

// Plane returns the track plane of layer for key k, building it on first use.
// Its obstacles are every item on layer except keep-out items (the rule
// keep-outs that forbid tracks stand for them), each inflated by
// k.HalfWidth plus the largest clearance any net of row k.Class needs to it,
// and the board outline with the edge clearance (spec 01 §4). k.HalfWidth may
// be any width, for example a neck-down width (spec 01 §4) of the row.
func (s *Set) Plane(layer geom.LayerID, k InflationKey) *Plane {
	pk := planeKey{Layer: layer, Key: k}
	if b := s.planes[pk]; b != nil {
		return b.plane
	}
	b := &built{from: layer, to: layer, obj: rules.Wire, half: k.HalfWidth, clr: map[clrKey]int64{}}
	for n := 1; n <= s.view.NumNets(); n++ {
		if s.row(rules.Wire, layer, layer, geom.NetID(n)) == k.Class {
			b.nets = append(b.nets, geom.NetID(n))
		}
	}
	return s.build(pk, b)
}

// build fills b's plane with the board's obstacles and caches it.
func (s *Set) build(pk planeKey, b *built) *Plane {
	rep := geom.NetID(0)
	if len(b.nets) > 0 {
		rep = b.nets[0]
	}
	c := s.rs.Clearance(rules.Obj{Kind: b.obj, Net: rep}, rules.Obj{Kind: rules.Wire}, b.from)
	b.plane = NewPlane(s.bounds, max(c/2, MinStairStep))
	var obs []Obstacle
	s.view.Items(func(it board.Item) bool {
		if o, ok := s.obstacle(b, it); ok {
			obs = append(obs, o)
		}
		return true
	})
	for l := b.from; l <= b.to; l++ {
		for i, k := range s.rs.Keepouts(l) {
			if k.Layer == geom.AllLayers && l != b.from || !blocks(k.Kind, b.obj) {
				continue
			}
			r := b.half + s.clearance(b, 0, rules.Area, l)
			obs = append(obs, Obstacle{ID: keepoutID | uint32(l-b.from)<<16 | uint32(i), Shape: k.Shape, R: r})
		}
	}
	// Insert in reading order so point location starts next to the last
	// change (spec 01 §3.3: sorted insertion).
	slices.SortStableFunc(obs, func(p, q Obstacle) int {
		a, c := p.Shape.Bounds(), q.Shape.Bounds()
		return cmp.Or(cmp.Compare(a.MinY, c.MinY), cmp.Compare(a.MinX, c.MinX), cmp.Compare(p.ID, q.ID))
	})
	for _, o := range obs {
		b.plane.Insert(o)
	}
	s.planes[pk] = b
	s.order = append(s.order, b)
	return b.plane
}

// blocks reports whether a keep-out of kind k forbids the routed object obj.
func blocks(k rules.KeepoutKind, obj rules.ObjKind) bool {
	return k == rules.KeepoutAll || k == rules.KeepoutWire && obj == rules.Wire || k == rules.KeepoutVia && obj == rules.Via
}

// obstacle is item it as an obstacle of b, if it lies on b's layers.
func (s *Set) obstacle(b *built, it board.Item) (Obstacle, bool) {
	o := Obstacle{ID: uint32(it.ID), Net: it.Net, Shape: it.Shape}
	switch it.Kind {
	case board.Keepout:
		return o, false
	case board.Edge:
		if !spans(it, b.from) {
			return o, false
		}
		o.Net, o.Outline, o.R = 0, true, b.half+s.rs.Edge()
		return o, true
	}
	kind := objKind(it)
	hit := false
	for l := b.from; l <= b.to; l++ {
		if !spans(it, l) || it.Kind == board.Zone && b.obj == rules.Via && s.kinds[l] == rules.Power {
			continue // a via's antipad in a plane layer is the pour's business (spec 01 §3.5)
		}
		hit = true
		o.R = max(o.R, b.half+s.clearance(b, it.Net, kind, l))
	}
	return o, hit
}

// spans reports whether item it is on layer l.
func spans(it board.Item, l geom.LayerID) bool {
	return it.From == geom.AllLayers || it.From <= l && l <= it.To
}

// objKind is the clearance object type of an item.
func objKind(it board.Item) rules.ObjKind {
	switch it.Kind {
	case board.Pad:
		if it.From == it.To && it.From != geom.AllLayers {
			return rules.SMD
		}
		return rules.Pin
	case board.Via:
		return rules.Via
	}
	return rules.Wire
}

// clearance is the largest clearance any foreign net of b needs to an object
// (net, kind) on layer l. Without a foreign net it is the default class's.
// Region rules are not applied: the plane is an approximation and the exact
// check (spec 01 §3.4 step 5) uses the precise value.
func (s *Set) clearance(b *built, net geom.NetID, kind rules.ObjKind, l geom.LayerID) int64 {
	k := clrKey{net, kind, l}
	if v, ok := b.clr[k]; ok {
		return v
	}
	other := rules.Obj{Kind: kind, Net: net}
	c, found := int64(0), false
	for _, m := range b.nets {
		if m != net {
			c, found = max(c, s.rs.Clearance(rules.Obj{Kind: b.obj, Net: m}, other, l)), true
		}
	}
	if !found {
		c = s.rs.Clearance(rules.Obj{Kind: b.obj}, other, l)
	}
	b.clr[k] = c
	return c
}

// Add inserts routed copper into every built plane it lies on.
func (s *Set) Add(it board.Item) {
	for _, b := range s.order {
		if o, ok := s.obstacle(b, it); ok {
			b.plane.Insert(o)
		}
	}
}

// Remove deletes item id from every built plane.
func (s *Set) Remove(id board.ItemID) {
	for _, b := range s.order {
		b.plane.Delete(uint32(id))
	}
}
