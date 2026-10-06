package board

import "github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"

// connect splits every net into two-terminal connections (spec 02 §2.1): the
// edges of a minimum spanning tree over the net's islands that hold a
// terminal. Terminals are pads and zones; copper already joined (fixed or
// pre-routed wiring, same-pin pieces) forms one island and needs no
// connection. The distance between two islands is the smallest
// centre-to-centre distance of their terminals, and each connection runs
// between that closest pair.
//
// Prim's algorithm starts from the island with the smallest item ID and
// breaks ties by island order, then item ID, so the result is deterministic.
// Connections are numbered net by net.
func connect(d *db) []Connection {
	var out []Connection
	for n := geom.NetID(1); int(n) <= len(d.nets); n++ {
		comps := d.comps(n)
		var islands [][]Item // by root, in ascending root ID
		at := map[ItemID]int{}
		for _, id := range d.members(n) {
			it, _ := d.get(id)
			if it.Kind != Pad && it.Kind != Zone {
				continue
			}
			r := comps[id]
			i, ok := at[r]
			if !ok {
				i = len(islands)
				at[r] = i
				islands = append(islands, nil)
			}
			islands[i] = append(islands[i], it)
		}
		for _, e := range spanningTree(islands) {
			out = append(out, Connection{ID: ConnID(len(out) + 1), Net: n,
				From: Terminal{Item: e[0].ID, At: center(e[0])}, To: Terminal{Item: e[1].ID, At: center(e[1])}})
		}
	}
	return out
}

// spanningTree returns the MST edges (tree terminal, new terminal) in the
// order Prim adds them.
func spanningTree(islands [][]Item) [][2]Item {
	k := len(islands)
	if k < 2 {
		return nil
	}
	type cand struct {
		d        uint64
		from, to Item
		ok       bool
	}
	best := make([]cand, k)
	in := make([]bool, k)
	var edges [][2]Item
	cur := 0
	for range k - 1 {
		in[cur] = true
		for j := range islands {
			if in[j] {
				continue
			}
			for _, a := range islands[cur] {
				for _, b := range islands[j] {
					if dd := dist2(center(a), center(b)); !best[j].ok || dd < best[j].d {
						best[j] = cand{dd, a, b, true}
					}
				}
			}
		}
		next := -1
		for j := range islands {
			if !in[j] && (next < 0 || best[j].d < best[next].d) {
				next = j
			}
		}
		edges = append(edges, [2]Item{best[next].from, best[next].to})
		cur = next
	}
	return edges
}

// dist2 is the squared distance; coordinates within ±MaxCoord keep it in
// uint64.
func dist2(p, q geom.Pt) uint64 {
	dx, dy := uint64(abs(p.X-q.X)), uint64(abs(p.Y-q.Y))
	return dx*dx + dy*dy
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
