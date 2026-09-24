package pcbauto

import (
	"fmt"
	"math"
	"sort"
)

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// Cell flags.
const (
	flagHard  uint8 = 1 << iota // no copper at all (edge band, keepout, hole)
	flagNoVia                   // no via centre here (SMD pad copper, via keepout)
)

// grid is a uniform multi-layer occupancy grid. Every copper layer of the
// stackup has a slice; only signal layers are routable, plane layers only
// receive via/THT anti-pad claims so plane perforation can be simulated.
//
// Clearance model: every copper object "claims" the cells within
// (its half width + its clearance share) of its centreline. Two objects of
// different nets conflict iff they share a claimed cell. With the shares
// chosen as c/2 this is exactly the DRC condition d ≥ w₁/2 + w₂/2 + c, up
// to one grid cell of discretisation which the exact DRC re-checks.
type grid struct {
	g        float64
	ox, oy   float64
	W, H     int
	layers   []int // copper layer ids, top→bottom
	routable []bool
	flags    []uint8  // per layer-cell
	pad      []int32  // per layer-cell: -1 none, net id, -2 several nets
	use      []uint16 // per layer-cell: routed/fanout claims (distinct nets)
	hist     []float32
	// Coarse 8×8-cell block summaries, so a congestion sum over an empty
	// neighbourhood is a few block reads instead of a disk of cells:
	// bUse = sum of use in the block, bHist = cells with non-zero history.
	bUse, bHist []int32
	bW, bH      int
	noVia    []bool // per x,y (all layers)
	baseClr  float64
	dcache   map[int]offs
	diskMRU  [4]diskEntry
	rcache   map[int]ringEntry
}

func (gr *grid) idx(l, x, y int) int { return (l*gr.H+y)*gr.W + x }

const blockCells = 8

// blockOf returns the block summary index of layer-cell i.
func (gr *grid) blockOf(i int) int {
	x := i % gr.W
	r := i / gr.W
	return ((r/gr.H)*gr.bH+(r%gr.H)/blockCells)*gr.bW + x/blockCells
}
func (gr *grid) xy(i int) (l, x, y int) {
	x = i % gr.W
	r := i / gr.W
	return r / gr.H, x, r % gr.H
}
func (gr *grid) center(x, y int) Point {
	return Point{gr.ox + (float64(x)+0.5)*gr.g, gr.oy + (float64(y)+0.5)*gr.g}
}
func (gr *grid) cellOf(p Point) (int, int) {
	return int(math.Floor((p.X - gr.ox) / gr.g)), int(math.Floor((p.Y - gr.oy) / gr.g))
}
func (gr *grid) in(x, y int) bool { return x >= 0 && y >= 0 && x < gr.W && y < gr.H }
func (gr *grid) layerIndex(id int) int {
	for i, l := range gr.layers {
		if l == id {
			return i
		}
	}
	return -1
}

// disk returns the claim footprint for radius r (mil): offsets within
// k = ceil(r/g − ½) cells (plus a quarter cell so the diagonal ring is kept).
// Two claims of radii ra, rb that share no cell are then ≥ ka+kb+1 ≥
// (ra+rb)/g cells apart along the axes — the grid test implies the exact
// clearance instead of approximating it.
type offs [][2]int

func (gr *grid) disk(r float64) offs {
	for k := range gr.diskMRU {
		if e := &gr.diskMRU[k]; e.d != nil && e.r == r {
			return e.d
		}
	}
	d := gr.diskSlow(r)
	copy(gr.diskMRU[1:], gr.diskMRU[:len(gr.diskMRU)-1])
	gr.diskMRU[0] = diskEntry{r, d}
	return d
}

type diskEntry struct {
	r float64
	d offs
}

func (gr *grid) diskSlow(r float64) offs {
	rc := math.Ceil(r/gr.g-0.5-1e-9) + 0.25
	key := int(math.Round(rc * 100))
	if gr.dcache == nil {
		gr.dcache = map[int]offs{}
	}
	if d, ok := gr.dcache[key]; ok {
		return d
	}
	n := int(math.Ceil(rc))
	var d offs
	for dy := -n; dy <= n; dy++ {
		for dx := -n; dx <= n; dx++ {
			if float64(dx*dx+dy*dy) <= rc*rc {
				d = append(d, [2]int{dx, dy})
			}
		}
	}
	gr.dcache[key] = d
	return d
}

func newGrid(b *Board, stack *Stackup, g float64) (*grid, error) {
	bb := b.Bounds()
	if bb.Empty() {
		return nil, fmt.Errorf("board has no outline and no parts")
	}
	bb = bb.Expand(g * 2)
	gr := &grid{g: g, ox: bb.MinX, oy: bb.MinY, baseClr: b.Rules.Clearance}
	gr.W = int(math.Ceil(bb.W() / g))
	gr.H = int(math.Ceil(bb.H() / g))
	if gr.W*gr.H > 12_000_000 {
		return nil, fmt.Errorf("grid %dx%d too large at %.1f mil; raise --grid", gr.W, gr.H, g)
	}
	for _, l := range stack.Stack {
		gr.layers = append(gr.layers, l.ID)
		gr.routable = append(gr.routable, l.Kind == KindSignal)
	}
	n := len(gr.layers) * gr.W * gr.H
	gr.flags = make([]uint8, n)
	gr.pad = make([]int32, n)
	for i := range gr.pad {
		gr.pad[i] = -1
	}
	gr.use = make([]uint16, n)
	gr.hist = make([]float32, n)
	gr.bW, gr.bH = (gr.W+blockCells-1)/blockCells, (gr.H+blockCells-1)/blockCells
	gr.bUse = make([]int32, len(gr.layers)*gr.bW*gr.bH)
	gr.bHist = make([]int32, len(gr.layers)*gr.bW*gr.bH)
	gr.noVia = make([]bool, gr.W*gr.H)
	return gr, nil
}

// markHardOutside blocks cells outside the outline or closer to the edge than
// edgeClr - baseClr/2 (so a claim of hw+c/2 keeps copper hw+edgeClr away).
func (gr *grid) markEdge(outline []Point, edgeClr float64) {
	if len(outline) < 3 {
		return
	}
	band := math.Max(edgeClr-gr.baseClr/2, 0)
	for y := 0; y < gr.H; y++ {
		for x := 0; x < gr.W; x++ {
			c := gr.center(x, y)
			if !PolyContains(outline, c) || PolyEdgeDist(outline, c) < band {
				for l := range gr.layers {
					gr.flags[gr.idx(l, x, y)] |= flagHard
				}
				gr.noVia[y*gr.W+x] = true
			}
		}
	}
}

// forCellsNear calls fn for every cell whose centre is within r of the
// distance function's zero set, restricted to the bounding rect bb.
func (gr *grid) forCellsNear(bb Rect, r float64, dist func(Point) float64, fn func(x, y int)) {
	bb = bb.Expand(r + gr.g)
	x0, y0 := gr.cellOf(Point{bb.MinX, bb.MinY})
	x1, y1 := gr.cellOf(Point{bb.MaxX, bb.MaxY})
	for y := max(y0, 0); y <= min(y1, gr.H-1); y++ {
		for x := max(x0, 0); x <= min(x1, gr.W-1); x++ {
			if dist(gr.center(x, y)) <= r+gr.g*0.25 {
				fn(x, y)
			}
		}
	}
}

func (gr *grid) markKeepout(k *Keepout) {
	bb := PolyBounds(k.Poly)
	gr.forCellsNear(bb, 0, func(p Point) float64 {
		if PolyContains(k.Poly, p) {
			return 0
		}
		return math.Inf(1)
	}, func(x, y int) {
		for li, id := range gr.layers {
			if k.NoCopper && k.onLayer(id) {
				gr.flags[gr.idx(li, x, y)] |= flagHard
			}
		}
		if k.NoVias || k.NoCopper {
			gr.noVia[y*gr.W+x] = true
		}
	})
}

func (gr *grid) markHole(h *Hole) {
	r := h.Dia/2 + h.Keep
	gr.forCellsNear(Rect{h.C.X, h.C.Y, h.C.X, h.C.Y}, r, func(p Point) float64 { return p.Dist(h.C) }, func(x, y int) {
		for li := range gr.layers {
			gr.flags[gr.idx(li, x, y)] |= flagHard
		}
		gr.noVia[y*gr.W+x] = true
	})
}

// markPad records the pad claim (copper + clearance share) on its layers.
func (gr *grid) markPad(pd *Pad, netID int32, share, viaR float64) {
	bb := pd.Box.Bounds()
	for li, id := range gr.layers {
		if !pd.OnLayer(id) {
			continue
		}
		gr.forCellsNear(bb, share, pd.Box.Dist, func(x, y int) {
			i := gr.idx(li, x, y)
			switch cur := gr.pad[i]; {
			case cur == -1:
				gr.pad[i] = netID
			case cur != netID:
				gr.pad[i] = -2
			}
		})
	}
	// Via centres may not sit where the via copper would touch any pad
	// (via-in-pad is excluded; THT pads are already vias).
	gr.forCellsNear(bb, viaR, pd.Box.Dist, func(x, y int) { gr.noVia[y*gr.W+x] = true })
}

const padBucket = 50.0

// padEntry is a pad in the exact-check buckets with its net id and clearance.
type padEntry struct {
	pd  *Pad
	net int32
	clr float64
}

// ring returns the claim disk for radius r followed by a 1.5-cell outer ring
// (sorted by distance); inner is the count of offsets inside the disk.
func (gr *grid) ring(r float64) (offs, int) {
	rc := r/gr.g + 0.25
	key := int(math.Round(rc * 100))
	if e, ok := gr.rcache[key]; ok {
		return e.o, e.inner
	}
	var d offs
	ro := rc + 1.5
	n := int(math.Ceil(ro))
	for dy := -n; dy <= n; dy++ {
		for dx := -n; dx <= n; dx++ {
			if float64(dx*dx+dy*dy) <= ro*ro {
				d = append(d, [2]int{dx, dy})
			}
		}
	}
	sort.Slice(d, func(i, j int) bool {
		a, b := d[i][0]*d[i][0]+d[i][1]*d[i][1], d[j][0]*d[j][0]+d[j][1]*d[j][1]
		if a != b {
			return a < b
		}
		return d[i][1]*1000+d[i][0] < d[j][1]*1000+d[j][0]
	})
	inner := 0
	for inner < len(d) && float64(d[inner][0]*d[inner][0]+d[inner][1]*d[inner][1]) <= rc*rc {
		inner++
	}
	if gr.rcache == nil {
		gr.rcache = map[int]ringEntry{}
	}
	gr.rcache[key] = ringEntry{d, inner}
	return d, inner
}

type ringEntry struct {
	o     offs
	inner int
}
