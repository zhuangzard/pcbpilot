package board

import "github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"

// unionFind is a disjoint-set forest over 0 … n-1 with path halving and union
// by size. The smaller root joins the larger; on equal sizes the higher index
// joins the lower, so the result does not depend on call order of equal
// unions.
type unionFind struct{ parent, size []int32 }

func newUnionFind(n int) *unionFind {
	u := &unionFind{parent: make([]int32, n), size: make([]int32, n)}
	for i := range u.parent {
		u.parent[i] = int32(i)
		u.size[i] = 1
	}
	return u
}

func (u *unionFind) find(i int32) int32 {
	for u.parent[i] != i {
		u.parent[i] = u.parent[u.parent[i]]
		i = u.parent[i]
	}
	return i
}

func (u *unionFind) union(a, b int32) {
	a, b = u.find(a), u.find(b)
	if a == b {
		return
	}
	if u.size[a] < u.size[b] || u.size[a] == u.size[b] && a > b {
		a, b = b, a
	}
	u.parent[b] = a
	u.size[a] += u.size[b]
}

// netComps is the connectivity of one net: every live item of the net mapped
// to its component, numbered by the smallest item ID in the component.
type netComps map[ItemID]ItemID

// components computes the copper islands of net n as lv sees them. Items of
// the net are joined when they touch (see touches) or are pieces of the same
// pin (same non-empty Ref).
func components(lv level, n geom.NetID) netComps {
	ids := lv.members(n)
	pos := make(map[ItemID]int32, len(ids))
	for i, id := range ids {
		pos[id] = int32(i)
	}
	u := newUnionFind(len(ids))
	pin := map[string]int32{}
	for i, id := range ids {
		it, _ := lv.get(id)
		if !conducts(it) {
			continue
		}
		if it.Ref != "" {
			if j, ok := pin[it.Ref]; ok {
				u.union(j, int32(i))
			} else {
				pin[it.Ref] = int32(i)
			}
		}
		for _, l := range layers(it) {
			lv.scan(it.Shape.Bounds(), l, func(o Item) bool {
				if j, ok := pos[o.ID]; ok && j > int32(i) && touches(it, o) {
					u.union(int32(i), j)
				}
				return true
			})
		}
	}
	// ids ascend, so the first member seen of each root is its smallest ID.
	first := make([]ItemID, len(ids))
	out := make(netComps, len(ids))
	for i, id := range ids {
		r := u.find(int32(i))
		if first[r] == 0 {
			first[r] = id
		}
		out[id] = first[r]
	}
	return out
}
