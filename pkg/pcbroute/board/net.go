package board

import "github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"

// level is one state of the board: the committed DB or a transaction overlay
// on top of its parent level. reader turns a level into a View.
type level interface {
	get(id ItemID) (Item, bool)
	// limit is one past the largest item ID the level may hold.
	limit() ItemID
	route(c ConnID) *Route
	// scan calls fn for live items on l whose bounds meet r until fn returns
	// false. On AllLayers a multi-layer item may be reported more than once.
	scan(r geom.Rect, l geom.LayerID, fn func(Item) bool) bool
	// members lists the live item IDs of net n (n > 0), ascending.
	members(n geom.NetID) []ItemID
	comps(n geom.NetID) netComps
	base() *db
}

// reader implements View on any level.
type reader struct{ lv level }

func (r reader) NumNets() int { return len(r.lv.base().nets) }

// NetName returns the name of net n, "" for net 0 or an unknown net.
func (r reader) NetName(n geom.NetID) string {
	nets := r.lv.base().nets
	if n <= 0 || int(n) > len(nets) {
		return ""
	}
	return nets[n-1]
}

func (r reader) Item(id ItemID) (Item, bool) { return r.lv.get(id) }

// Items visits live items in ascending ID order.
func (r reader) Items(fn func(Item) bool) {
	for id, end := ItemID(1), r.lv.limit(); id < end; id++ {
		if it, ok := r.lv.get(id); ok && !fn(it) {
			return
		}
	}
}

// Query visits each matching item once. The order is fixed: committed items
// in index order, then each overlay's own edits in ascending ID.
func (r reader) Query(box geom.Rect, layer geom.LayerID, fn func(Item) bool) {
	if layer != geom.AllLayers {
		r.lv.scan(box, layer, fn)
		return
	}
	seen := map[ItemID]bool{}
	r.lv.scan(box, layer, func(it Item) bool {
		if seen[it.ID] {
			return true
		}
		seen[it.ID] = true
		return fn(it)
	})
}

// Connections returns every connection in ID order with its current route.
func (r reader) Connections() []Connection {
	cs := r.lv.base().conns
	out := make([]Connection, len(cs))
	for i, c := range cs {
		c.Route = r.lv.route(c.ID)
		out[i] = c
	}
	return out
}

func (r reader) Connection(id ConnID) (Connection, bool) {
	cs := r.lv.base().conns
	if id == 0 || int(id) > len(cs) {
		return Connection{}, false
	}
	c := cs[id-1]
	c.Route = r.lv.route(id)
	return c, true
}

// Connected reports whether a and b are live copper of the same net in one
// island. An item is connected to itself.
func (r reader) Connected(a, b ItemID) bool {
	ia, ok := r.lv.get(a)
	if !ok || !conducts(ia) {
		return false
	}
	ib, ok := r.lv.get(b)
	if !ok || !conducts(ib) || ia.Net != ib.Net {
		return false
	}
	c := r.lv.comps(ia.Net)
	return c[a] == c[b]
}
