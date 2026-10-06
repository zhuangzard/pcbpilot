package board

import (
	"slices"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// txn is a copy-on-write overlay (spec 03 §2 "Transaction"). It records
// edited items by ID (a zero Item marks a removal) and replaced routes; reads
// fall through to the parent level. New item IDs continue the parent's
// numbering, so a committed overlay keeps its IDs and a rolled-back one
// leaves no trace.
type txn struct {
	reader
	d      *db
	parent level
	up     *txn // parent overlay, nil when the parent is the DB
	edits  map[ItemID]Item
	keys   []ItemID // sorted keys of edits; nil when stale
	routes map[ConnID]*Route
	next   ItemID
	cache  map[geom.NetID]netComps
	child  *txn
	closed bool
}

func newTxn(d *db, parent level, up *txn) *txn {
	t := &txn{d: d, parent: parent, up: up, edits: map[ItemID]Item{}, routes: map[ConnID]*Route{},
		next: parent.limit(), cache: map[geom.NetID]netComps{}}
	t.reader = reader{t}
	return t
}

func (t *txn) base() *db { return t.d }

func (t *txn) get(id ItemID) (Item, bool) {
	if e, ok := t.edits[id]; ok {
		return e, e.ID != 0
	}
	return t.parent.get(id)
}

func (t *txn) limit() ItemID { return t.next }

func (t *txn) route(c ConnID) *Route {
	if r, ok := t.routes[c]; ok {
		return r
	}
	return t.parent.route(c)
}

func (t *txn) sorted() []ItemID {
	if t.keys == nil {
		t.keys = make([]ItemID, 0, len(t.edits))
		for id := range t.edits {
			t.keys = append(t.keys, id)
		}
		slices.Sort(t.keys)
	}
	return t.keys
}

func (t *txn) scan(r geom.Rect, l geom.LayerID, fn func(Item) bool) bool {
	if !t.parent.scan(r, l, func(it Item) bool {
		if _, edited := t.edits[it.ID]; edited {
			return true
		}
		return fn(it)
	}) {
		return false
	}
	for _, id := range t.sorted() {
		it := t.edits[id]
		if it.ID != 0 && onLayer(it, l) && it.Shape.Bounds().Intersects(r) && !fn(it) {
			return false
		}
	}
	return true
}

func (t *txn) members(n geom.NetID) []ItemID {
	var out []ItemID
	for _, id := range t.parent.members(n) {
		if _, edited := t.edits[id]; !edited {
			out = append(out, id)
		}
	}
	for _, id := range t.sorted() {
		if it := t.edits[id]; it.ID != 0 && it.Net == n {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func (t *txn) comps(n geom.NetID) netComps {
	c, ok := t.cache[n]
	if !ok {
		c = components(t, n)
		t.cache[n] = c
	}
	return c
}

// writable panics unless t may be edited: open and without an open child.
func (t *txn) writable() {
	if t.closed {
		panic("board: transaction already committed or rolled back")
	}
	if t.child != nil {
		panic("board: transaction has an open nested transaction")
	}
}

// set records the new state of one item ID (a zero Item removes it).
func (t *txn) set(id ItemID, it Item) {
	if old, ok := t.get(id); ok {
		delete(t.cache, old.Net)
	}
	delete(t.cache, it.Net)
	if _, ok := t.edits[id]; !ok {
		t.keys = nil
	}
	t.edits[id] = it
}

func (t *txn) conn(c ConnID) Connection {
	cn, ok := t.Connection(c)
	if !ok {
		panic("board: unknown connection")
	}
	return cn
}

// Add inserts a new item and returns its ID. An item with Owner set is also
// appended to that connection's route. Invalid items panic.
func (t *txn) Add(it Item) ItemID {
	t.writable()
	it.ID = t.next
	if err := checkItem(it, len(t.d.nets), t.d.nlayers); err != nil {
		panic(err)
	}
	if it.Owner != 0 {
		var items []Item
		if r := t.conn(it.Owner).Route; r != nil {
			items = slices.Clone(r.Items)
		}
		t.routes[it.Owner] = &Route{Items: append(items, it)}
	}
	t.next++
	t.set(it.ID, it)
	return it.ID
}

// Remove deletes a live item; an unknown or removed ID is ignored. An item of
// a route also leaves the route; a route left empty becomes nil (unrouted).
// Removing an item that was Fixed at Build panics.
func (t *txn) Remove(id ItemID) {
	t.writable()
	it, ok := t.get(id)
	if !ok {
		return
	}
	if t.d.static(id) {
		panic("board: an item fixed at Build cannot be removed")
	}
	if it.Owner != 0 {
		r := t.conn(it.Owner).Route
		items := slices.DeleteFunc(slices.Clone(r.Items), func(o Item) bool { return o.ID == id })
		t.routes[it.Owner] = nil
		if len(items) > 0 {
			t.routes[it.Owner] = &Route{Items: items}
		}
	}
	t.set(id, Item{})
}

// SetRoute removes c's current route items and adds r's items with fresh IDs
// and Owner c; the stored route holds those numbered items. nil rips c up.
func (t *txn) SetRoute(c ConnID, r *Route) {
	t.writable()
	if old := t.conn(c).Route; old != nil {
		for _, it := range old.Items {
			t.set(it.ID, Item{})
		}
	}
	if r == nil {
		t.routes[c] = nil
		return
	}
	items := make([]Item, len(r.Items))
	for i, it := range r.Items {
		it.ID, it.Owner = t.next, c
		if err := checkItem(it, len(t.d.nets), t.d.nlayers); err != nil {
			panic(err)
		}
		t.next++
		t.set(it.ID, it)
		items[i] = it
	}
	t.routes[c] = &Route{Items: items}
}

// Begin opens a nested overlay (a shove attempt) on top of t.
func (t *txn) Begin() Txn {
	t.writable()
	t.child = newTxn(t.d, t, t)
	return t.child
}

// Commit folds t into its parent: the parent overlay, or the DB.
func (t *txn) Commit() {
	t.writable()
	t.closed = true
	if t.up != nil {
		t.up.absorb(t)
		t.up.child = nil
		return
	}
	t.d.apply(t)
	t.d.open = nil
}

// Rollback drops t and any open nested overlay.
func (t *txn) Rollback() {
	if t.closed {
		panic("board: transaction already committed or rolled back")
	}
	if t.child != nil {
		t.child.Rollback()
	}
	t.closed = true
	if t.up != nil {
		t.up.child = nil
	} else {
		t.d.open = nil
	}
}

// absorb merges a committed child overlay into t.
func (t *txn) absorb(c *txn) {
	for _, id := range c.sorted() {
		t.set(id, c.edits[id])
	}
	for id, r := range c.routes {
		t.routes[id] = r
	}
	t.next = c.next
}

// apply writes a committed outermost overlay into the DB, in ID order.
func (d *db) apply(t *txn) {
	for _, id := range t.sorted() {
		if _, ok := d.get(id); ok {
			d.drop(id)
		}
		if it := t.edits[id]; it.ID != 0 {
			d.put(it)
		}
	}
	for c, r := range t.routes {
		d.conns[c-1].Route = r
	}
}
