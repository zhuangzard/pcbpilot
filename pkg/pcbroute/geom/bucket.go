package geom

// Grid is the dynamic uniform bucket grid for routed copper (spec 01 §2.3).
// Each entry is stored in every bucket its box touches; insert and delete
// cost O(buckets touched). Boxes outside the grid's area fall into the edge
// buckets, so nothing is lost, only slower to find.
//
// Entries are numbered by slots that Insert hands out; a deleted slot is
// reused. Search order is deterministic: buckets row by row, and inside a
// bucket the insertion order.
type Grid struct {
	area   Rect
	cell   int64
	nx, ny int64
	cells  [][]int32
	boxes  []Rect
	live   []bool
	free   []int32
}

// NewGrid covers area with square buckets of side cell > 0.
func NewGrid(area Rect, cell int64) *Grid {
	if cell <= 0 {
		panic("geom: grid cell must be positive")
	}
	if area.Empty() {
		area = Rect{0, 0, 1, 1}
	}
	nx := (area.MaxX - area.MinX + cell - 1) / cell
	ny := (area.MaxY - area.MinY + cell - 1) / cell
	return &Grid{area: area, cell: cell, nx: nx, ny: ny, cells: make([][]int32, nx*ny)}
}

// col and row are the bucket column and row of a coordinate, clamped to the
// grid; both are monotone.
func (g *Grid) col(x int64) int64 { return min(max((x-g.area.MinX)/g.cell, 0), g.nx-1) }
func (g *Grid) row(y int64) int64 { return min(max((y-g.area.MinY)/g.cell, 0), g.ny-1) }

// span is the bucket range [x0, x1] × [y0, y1] that r touches; r must not be
// empty.
func (g *Grid) span(r Rect) (x0, y0, x1, y1 int64) {
	return g.col(r.MinX), g.row(r.MinY), g.col(r.MaxX - 1), g.row(r.MaxY - 1)
}

// Insert stores box and returns its slot. An empty box is stored but never
// found.
func (g *Grid) Insert(box Rect) int32 {
	var s int32
	if n := len(g.free); n > 0 {
		s, g.free = g.free[n-1], g.free[:n-1]
		g.boxes[s], g.live[s] = box, true
	} else {
		s = int32(len(g.boxes))
		g.boxes, g.live = append(g.boxes, box), append(g.live, true)
	}
	if box.Empty() {
		return s
	}
	x0, y0, x1, y1 := g.span(box)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			c := &g.cells[y*g.nx+x]
			*c = append(*c, s)
		}
	}
	return s
}

// Delete removes slot s; it is a no-op for a slot that is not live.
func (g *Grid) Delete(s int32) {
	if s < 0 || int(s) >= len(g.live) || !g.live[s] {
		return
	}
	g.live[s] = false
	g.free = append(g.free, s)
	box := g.boxes[s]
	if box.Empty() {
		return
	}
	x0, y0, x1, y1 := g.span(box)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			c := &g.cells[y*g.nx+x]
			for i, v := range *c {
				if v == s {
					*c = append((*c)[:i], (*c)[i+1:]...)
					break
				}
			}
		}
	}
}

// Box returns the box of a live slot.
func (g *Grid) Box(s int32) Rect { return g.boxes[s] }

// Search calls fn once with every live slot whose box intersects r, until fn
// returns false. It returns false when fn stopped the search.
func (g *Grid) Search(r Rect, fn func(s int32) bool) bool {
	if r.Empty() {
		return true
	}
	x0, y0, x1, y1 := g.span(r)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			for _, s := range g.cells[y*g.nx+x] {
				b := g.boxes[s]
				if !b.Intersects(r) {
					continue
				}
				// Report an entry only in the bucket that holds the corner
				// max(b.Min, r.Min), which both ranges contain: no duplicates
				// and no visited set.
				if g.col(max(b.MinX, r.MinX)) == x && g.row(max(b.MinY, r.MinY)) == y && !fn(s) {
					return false
				}
			}
		}
	}
	return true
}
