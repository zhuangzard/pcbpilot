package board

// Snapshot records every connection's committed route (spec 02 §2.3). Routes
// are immutable, so this is one pointer per connection. The congestion arrays
// of spec 02 §2.3 belong to the negotiation state, not to the board, and are
// snapshotted there. Items added or removed outside routes are not covered.
func (d *db) Snapshot() Snapshot {
	s := Snapshot{routes: make([]*Route, len(d.conns))}
	for i, c := range d.conns {
		s.routes[i] = c.Route
	}
	return s
}

// Restore puts back the routes of s: every connection whose route differs
// loses its current items and gets the snapshot's items back under their
// original IDs (IDs of committed items are never reused, so they are free).
// It panics while a transaction is open or for a snapshot of another board.
func (d *db) Restore(s Snapshot) {
	if d.open != nil {
		panic("board: Restore with an open transaction")
	}
	if len(s.routes) != len(d.conns) {
		panic("board: snapshot of another board")
	}
	// Drop first, then add: a trimmed route shares items with its original.
	for i, c := range d.conns {
		if c.Route != s.routes[i] && c.Route != nil {
			for _, it := range c.Route.Items {
				d.drop(it.ID)
			}
		}
	}
	for i, c := range d.conns {
		if c.Route != s.routes[i] {
			if r := s.routes[i]; r != nil {
				for _, it := range r.Items {
					d.put(it)
				}
			}
			d.conns[i].Route = s.routes[i]
		}
	}
}
