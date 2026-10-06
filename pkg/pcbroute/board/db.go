package board

import (
	"errors"
	"slices"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// defaultCell is the bucket-grid cell when no rules give a track pitch.
const defaultCell = 1_000_000 // 1 mm

// builder is the Builder returned by NewBuilder.
type builder struct {
	nets  []string
	items []Item
	built bool
}

// NewBuilder returns an empty board Builder. Items get IDs 1, 2, … in the
// order they are added; nets get IDs 1, 2, ….
func NewBuilder() Builder { return &builder{} }

func (b *builder) AddNet(name string) geom.NetID {
	b.nets = append(b.nets, name)
	return geom.NetID(len(b.nets))
}

func (b *builder) AddItem(it Item) ItemID {
	it.ID = ItemID(len(b.items) + 1)
	b.items = append(b.items, it)
	return it.ID
}

// Build validates the items, indexes them (items Fixed here go into the
// static R-tree and can never be removed) and splits every net into
// connections (connect.go). rs may be nil; it then gives neither a layer
// count to check against nor a grid pitch.
func (b *builder) Build(rs rules.Resolver) (DB, error) {
	if b.built {
		return nil, errors.New("board: Build called twice")
	}
	b.built = true
	d := &db{nets: b.nets, items: b.items, byNet: make([][]ItemID, len(b.nets)+1),
		cache: make([]netComps, len(b.nets)+1), inTree: make([]bool, len(b.items))}
	d.reader = reader{d}
	if rs != nil {
		d.nlayers = len(rs.Layers())
	}
	var fixed []geom.Entry
	var area geom.Rect
	for i, it := range b.items {
		if err := checkItem(it, len(b.nets), d.nlayers); err != nil {
			return nil, err
		}
		if it.Owner != 0 {
			return nil, errors.New("board: items added before Build cannot belong to a connection")
		}
		area = area.Union(it.Shape.Bounds())
		if it.Net != 0 {
			d.byNet[it.Net] = append(d.byNet[it.Net], it.ID)
		}
		if it.Fixed {
			d.inTree[i] = true
			for _, l := range layers(it) {
				fixed = append(fixed, geom.Entry{ID: uint32(it.ID), Net: it.Net, Layer: l, Shape: it.Shape})
			}
		}
	}
	if area.Empty() {
		area = geom.Rect{MaxX: 1, MaxY: 1}
	}
	d.ix = geom.NewIndex(fixed, area, gridCell(rs))
	for i, it := range b.items {
		if !d.inTree[i] {
			d.index(it)
		}
	}
	d.conns = connect(d)
	return d, nil
}

// gridCell follows spec 01 §2.3: 4 × (default track width + default
// wire-wire clearance).
func gridCell(rs rules.Resolver) int64 {
	if rs == nil {
		return defaultCell
	}
	c := 4 * (rs.Width(0, 0) + rs.Clearance(rules.Obj{Kind: rules.Wire}, rules.Obj{Kind: rules.Wire}, 0))
	if c <= 0 || c > geom.MaxCoord {
		return defaultCell
	}
	return c
}

// db is the committed board. One transaction chain may be open at a time;
// the DB itself changes only when that chain's outermost Txn commits, or on
// Restore.
type db struct {
	reader
	nets    []string
	nlayers int
	items   []Item     // by ID-1; a removed slot has ID 0
	inTree  []bool     // by ID-1 for items of Build: indexed as fixed
	byNet   [][]ItemID // live IDs per net, ascending; [0] unused
	ix      *geom.Index
	conns   []Connection // by ID-1; Route is the committed route
	cache   []netComps   // per net; nil when stale
	open    *txn
}

func (d *db) base() *db { return d }

func (d *db) get(id ItemID) (Item, bool) {
	if id == 0 || int(id) > len(d.items) {
		return Item{}, false
	}
	it := d.items[id-1]
	return it, it.ID != 0
}

func (d *db) limit() ItemID { return ItemID(len(d.items) + 1) }

func (d *db) route(c ConnID) *Route {
	if c == 0 || int(c) > len(d.conns) {
		return nil
	}
	return d.conns[c-1].Route
}

func (d *db) scan(r geom.Rect, l geom.LayerID, fn func(Item) bool) bool {
	return d.ix.Nearby(r, l, func(e geom.Entry) bool { return fn(d.items[e.ID-1]) })
}

func (d *db) members(n geom.NetID) []ItemID { return d.byNet[n] }

func (d *db) comps(n geom.NetID) netComps {
	if d.cache[n] == nil {
		d.cache[n] = components(d, n)
	}
	return d.cache[n]
}

// index puts a non-static item into the dynamic part of the index.
func (d *db) index(it Item) {
	for _, l := range layers(it) {
		d.ix.Insert(geom.Entry{ID: uint32(it.ID), Net: it.Net, Layer: l, Shape: it.Shape})
	}
}

// put stores a live item at its ID (growing the slots) and indexes it.
func (d *db) put(it Item) {
	for int(it.ID) > len(d.items) {
		d.items = append(d.items, Item{})
	}
	d.items[it.ID-1] = it
	d.index(it)
	if it.Net != 0 {
		ids := d.byNet[it.Net]
		i, _ := slices.BinarySearch(ids, it.ID)
		d.byNet[it.Net] = slices.Insert(ids, i, it.ID)
		d.cache[it.Net] = nil
	}
}

// drop removes the live item id from the slots and the index.
func (d *db) drop(id ItemID) {
	it := d.items[id-1]
	if d.static(id) {
		panic("board: an item fixed at Build cannot be removed")
	}
	for _, l := range layers(it) {
		d.ix.Delete(uint32(id), l)
	}
	d.items[id-1] = Item{}
	if it.Net != 0 {
		ids := d.byNet[it.Net]
		if i, ok := slices.BinarySearch(ids, id); ok {
			d.byNet[it.Net] = slices.Delete(ids, i, i+1)
		}
		d.cache[it.Net] = nil
	}
}

func (d *db) static(id ItemID) bool { return int(id) <= len(d.inTree) && d.inTree[id-1] }

// Begin opens a transaction on the committed board. Only one may be open.
func (d *db) Begin() Txn {
	if d.open != nil {
		panic("board: a transaction is already open")
	}
	d.open = newTxn(d, d, nil)
	return d.open
}
