package geom

import "slices"

// Entry is one shape in an Index.
type Entry struct {
	ID    uint32  // the caller's handle, e.g. a board.ItemID
	Net   NetID   // 0: no net, an obstacle to every net
	Layer LayerID // AllLayers: present on every layer
	Shape Shape
}

// Index is the exact clearance index of spec 01 §2.3: per layer, a static
// R-tree for fixed items and a dynamic bucket grid for routed copper. Items
// on AllLayers are stored once and seen from every layer. Results come in a
// fixed order (layers ascending; per layer fixed items in tree order, then
// routed items in grid order), so a caller that stops at the first hit is
// deterministic.
type Index struct {
	area   Rect
	cell   int64
	layers map[LayerID]*layerIndex
	order  []LayerID // keys of layers, ascending
	slots  map[dynKey]int32
}

type layerIndex struct {
	fixed []Entry // in the order of the boxes given to tree
	tree  *RTree
	grid  *Grid
	dyn   []Entry // by grid slot
}

type dynKey struct {
	id    uint32
	layer LayerID
}

// NewIndex builds the static part from fixed and prepares the bucket grids
// over area, with buckets of side cell > 0 (spec 01 §2.3 suggests 4 × (default
// track width + default clearance)). An empty area defaults to the bounds of
// fixed. It panics on a shape outside ±MaxCoord (see Check).
func NewIndex(fixed []Entry, area Rect, cell int64) *Index {
	if cell <= 0 {
		panic("geom: grid cell must be positive")
	}
	ix := &Index{area: area, cell: cell, layers: map[LayerID]*layerIndex{}, slots: map[dynKey]int32{}}
	var bounds Rect
	for _, e := range fixed {
		mustCheck(e.Shape)
		li := ix.layer(e.Layer)
		li.fixed = append(li.fixed, e)
		bounds = bounds.Union(e.Shape.Bounds())
	}
	if ix.area.Empty() {
		ix.area = bounds
	}
	for _, l := range ix.order {
		li := ix.layers[l]
		boxes := make([]Rect, len(li.fixed))
		for i, e := range li.fixed {
			boxes[i] = e.Shape.Bounds()
		}
		li.tree = NewRTree(boxes)
	}
	return ix
}

// layer returns the structures of l, creating them on first use.
func (ix *Index) layer(l LayerID) *layerIndex {
	li := ix.layers[l]
	if li == nil {
		li = &layerIndex{tree: NewRTree(nil)}
		ix.layers[l] = li
		i, _ := slices.BinarySearch(ix.order, l)
		ix.order = slices.Insert(ix.order, i, l)
	}
	return li
}

// Insert adds routed copper. (e.ID, e.Layer) must be unique among inserted
// entries; a via spanning several layers is inserted once per layer. It
// panics on a duplicate or on a shape outside ±MaxCoord.
func (ix *Index) Insert(e Entry) {
	mustCheck(e.Shape)
	k := dynKey{e.ID, e.Layer}
	if _, dup := ix.slots[k]; dup {
		panic("geom: duplicate index entry")
	}
	li := ix.layer(e.Layer)
	if li.grid == nil {
		li.grid = NewGrid(ix.area, ix.cell)
	}
	s := li.grid.Insert(e.Shape.Bounds())
	if int(s) == len(li.dyn) {
		li.dyn = append(li.dyn, e)
	} else {
		li.dyn[s] = e
	}
	ix.slots[k] = s
}

// Delete removes the routed entry (id, layer) and reports whether it existed.
// Fixed entries cannot be deleted.
func (ix *Index) Delete(id uint32, layer LayerID) bool {
	k := dynKey{id, layer}
	s, ok := ix.slots[k]
	if !ok {
		return false
	}
	li := ix.layers[layer]
	li.grid.Delete(s)
	li.dyn[s] = Entry{}
	delete(ix.slots, k)
	return true
}

// Nearby calls fn with every entry seen from layer whose bounds meet r, until
// fn returns false; layer AllLayers sees every layer. It returns false when fn
// stopped.
func (ix *Index) Nearby(r Rect, layer LayerID, fn func(Entry) bool) bool {
	for _, l := range ix.order {
		if layer != AllLayers && l != layer && l != AllLayers {
			continue
		}
		li := ix.layers[l]
		if !li.tree.Search(r, func(i int32) bool { return fn(li.fixed[i]) }) {
			return false
		}
		if li.grid != nil && !li.grid.Search(r, func(s int32) bool { return fn(li.dyn[s]) }) {
			return false
		}
	}
	return true
}

// Hits calls fn with every foreign entry closer to s than its clearance, until
// fn returns false. Foreign means another net; net 0 copper is foreign to
// every net, and a query with net 0 treats everything as foreign (spec 01 §4
// "own net"). clr gives the clearance an entry needs (spec 03 §2: the pair
// value of the two classes); nil means rmax for all. clr must not exceed rmax,
// which bounds the search window. It returns false when fn stopped. It panics
// when rmax exceeds MaxCoord, even if no entry is near.
func (ix *Index) Hits(s Shape, layer LayerID, net NetID, rmax int64, clr func(Entry) int64, fn func(Entry) bool) bool {
	if rmax > MaxCoord {
		panic("geom: clearance outside MaxCoord")
	}
	if rmax <= 0 {
		return true
	}
	return ix.Nearby(s.Bounds().Grow(rmax), layer, func(e Entry) bool {
		if net != 0 && e.Net == net {
			return true
		}
		r := rmax
		if clr != nil {
			if r = clr(e); r > rmax {
				panic("geom: clearance exceeds the query window")
			}
		}
		return !Within(s, e.Shape, r) || fn(e)
	})
}

// Clear is spec 03 §2's query: true when no foreign entry on layer lies closer
// to s than its clearance clr(e) ≤ rmax (nil clr: rmax for all).
func (ix *Index) Clear(s Shape, layer LayerID, net NetID, rmax int64, clr func(Entry) int64) bool {
	return ix.Hits(s, layer, net, rmax, clr, func(Entry) bool { return false })
}

// Collides is spec 01 §2.3's query: true when a foreign entry on layer lies
// closer than r to s.
func (ix *Index) Collides(s Shape, layer LayerID, net NetID, r int64) bool {
	return !ix.Clear(s, layer, net, r, nil)
}
