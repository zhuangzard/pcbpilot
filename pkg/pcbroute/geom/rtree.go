package geom

import (
	"math"
	"sort"
)

// rtreeFanout is the node fan-out of the static R-tree (spec 01 §2.3).
const rtreeFanout = 16

// RTree is a static R-tree (Guttman 1984) bulk-loaded with Sort-Tile-Recursive
// packing (Leutenegger et al. 1997). It indexes boxes by their position in
// the slice given to NewRTree and does not change after construction.
type RTree struct {
	boxes  []Rect
	perm   []int32   // box indices in leaf order
	levels [][]rnode // levels[0] are the leaves; the last level holds the root
}

// rnode covers the entries [first, first+n) of the level below, or of perm
// for a leaf.
type rnode struct {
	box      Rect
	first, n int32
}

// NewRTree packs boxes. The STR sorts are stable, so ties keep input order and
// the tree is the same on every run.
func NewRTree(boxes []Rect) *RTree {
	t := &RTree{boxes: boxes, perm: make([]int32, len(boxes))}
	for i := range t.perm {
		t.perm[i] = int32(i)
	}
	strSort(t.perm, func(i int32) Rect { return boxes[i] })
	level := pack(len(t.perm), func(i int) Rect { return boxes[t.perm[i]] })
	for len(level) > 1 {
		// Reordering a level is safe: its nodes point into the level below.
		strSort(level, func(n rnode) Rect { return n.box })
		t.levels = append(t.levels, level)
		lv := level
		level = pack(len(lv), func(i int) Rect { return lv[i].box })
	}
	if len(level) == 1 {
		t.levels = append(t.levels, level)
	}
	return t
}

// strSort orders xs into vertical slabs by centre x, then each slab by
// centre y, so consecutive runs of rtreeFanout make compact nodes.
func strSort[T any](xs []T, box func(T) Rect) {
	n := len(xs)
	if n <= rtreeFanout {
		return
	}
	pages := (n + rtreeFanout - 1) / rtreeFanout
	slabs := int(math.Ceil(math.Sqrt(float64(pages))))
	per := slabs * rtreeFanout
	cx := func(r Rect) int64 { return r.MinX + r.MaxX }
	cy := func(r Rect) int64 { return r.MinY + r.MaxY }
	sort.SliceStable(xs, func(i, j int) bool { return cx(box(xs[i])) < cx(box(xs[j])) })
	for lo := 0; lo < n; lo += per {
		s := xs[lo:min(lo+per, n)]
		sort.SliceStable(s, func(i, j int) bool { return cy(box(s[i])) < cy(box(s[j])) })
	}
}

// pack groups n consecutive entries into nodes of rtreeFanout.
func pack(n int, box func(int) Rect) []rnode {
	out := make([]rnode, 0, (n+rtreeFanout-1)/rtreeFanout)
	for lo := 0; lo < n; lo += rtreeFanout {
		hi := min(lo+rtreeFanout, n)
		nd := rnode{first: int32(lo), n: int32(hi - lo)}
		for i := lo; i < hi; i++ {
			nd.box = nd.box.Union(box(i))
		}
		out = append(out, nd)
	}
	return out
}

// Len is the number of indexed boxes.
func (t *RTree) Len() int { return len(t.boxes) }

// Search calls fn with the index of every box that intersects r, until fn
// returns false. The order is fixed by the tree. It returns false when fn
// stopped the search.
func (t *RTree) Search(r Rect, fn func(i int32) bool) bool {
	if len(t.levels) == 0 || r.Empty() {
		return true
	}
	top := len(t.levels) - 1
	for i := range t.levels[top] {
		if !t.search(top, int32(i), r, fn) {
			return false
		}
	}
	return true
}

func (t *RTree) search(l int, i int32, r Rect, fn func(int32) bool) bool {
	nd := t.levels[l][i]
	if !nd.box.Intersects(r) {
		return true
	}
	for c := nd.first; c < nd.first+nd.n; c++ {
		if l > 0 {
			if !t.search(l-1, c, r, fn) {
				return false
			}
			continue
		}
		if b := t.perm[c]; t.boxes[b].Intersects(r) && !fn(b) {
			return false
		}
	}
	return true
}
