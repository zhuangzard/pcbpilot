package pcbauto

import "math"

// RUDY congestion (Rectangular Uniform wire DensitY).
//
// Half-perimeter wirelength says how long the wires are, not where they
// crowd. RUDY spreads each net's expected wire (its HPWL) uniformly over its
// bounding box, adds a pin-access charge per pad, and compares the demand in
// every grid cell with the tracks the cell can carry
// (signal layers × cell / track pitch). Demand above a utilisation threshold
// is overflow — the regions a router will fail in or detour around.
//
// The placer charges overflow during annealing (incrementally: only the cells
// under the moved parts' nets change), which is what spreads parts into the
// board's free area instead of packing them tight around the wirelength
// optimum. Ground nets are excluded (fan-out vias into a plane); on 4+ layer
// boards power nets are too.

type rudy struct {
	g        float64 // cell size, mil
	x0, y0   float64
	nx, ny   int
	cap      float64 // tracks per cell (all signal layers)
	dem      []float64
	nets     [][]*Pad
	box      []Rect // bounding box of each net as last applied
	partNets map[*Part][]int
	weight   float64
	pinDem   float64
	util     float64 // utilisation threshold

	// pending move
	pending bool
	mnets   []int
	oldBox  []Rect
	newBox  []Rect
	oldPins [][]Point
	newPins [][]Point
	mparts  []*Part
	stamp   []int
	epoch   int
}

// newRudy builds the demand grid for the current placement.
func newRudy(b *Board, an *Analysis, region Rect, weight float64) *rudy {
	side := math.Max(region.W(), region.H())
	g := math.Max(40, side/40)
	r := &rudy{g: g, x0: region.MinX, y0: region.MinY, weight: weight, pinDem: 0.5, util: 0.8,
		partNets: map[*Part][]int{}}
	r.nx = int(math.Ceil(region.W()/g)) + 1
	r.ny = int(math.Ceil(region.H()/g)) + 1
	r.dem = make([]float64, r.nx*r.ny)
	r.stamp = make([]int, r.nx*r.ny)
	layers := b.CopperLayers
	signal := 2.0
	if layers >= 6 {
		signal = float64(layers - 2)
	}
	pitch := b.Rules.TrackWidth + b.Rules.Clearance
	if pitch <= 0 {
		pitch = 12
	}
	r.cap = signal * g / pitch
	idx := map[string]int{}
	for _, n := range b.Nets() {
		role := an.Plan(n.Name, b.Rules).Role
		if role == RoleGround || role == RolePower && layers >= 4 {
			continue
		}
		if len(n.Pads) < 2 || len(n.Pads) > 60 {
			continue
		}
		idx[n.Name] = len(r.nets)
		r.nets = append(r.nets, n.Pads)
	}
	r.box = make([]Rect, len(r.nets))
	for i := range r.nets {
		r.box[i] = r.netBox(i)
		r.applyNet(r.box[i], 1)
	}
	for _, p := range b.Parts {
		seen := map[int]bool{}
		for _, pd := range p.Pads {
			if i, ok := idx[pd.Net]; ok && !seen[i] {
				seen[i] = true
				r.partNets[p] = append(r.partNets[p], i)
			}
			r.applyPin(pd.Box.C, 1)
		}
	}
	return r
}

func (r *rudy) netBox(i int) Rect {
	bb := EmptyRect()
	for _, pd := range r.nets[i] {
		bb = bb.AddPoint(pd.Box.C)
	}
	return bb
}

func (r *rudy) cell(x, y float64) (int, int) {
	i := int((x - r.x0) / r.g)
	j := int((y - r.y0) / r.g)
	return clampInt(i, 0, r.nx-1), clampInt(j, 0, r.ny-1)
}

// applyNet spreads HPWL/g tracks of demand over the (at least one-cell) box.
func (r *rudy) applyNet(bb Rect, sign float64) {
	hpwl := bb.W() + bb.H()
	if hpwl <= 0 {
		return
	}
	// Grow degenerate boxes to one cell so a straight net still lands.
	if bb.W() < r.g {
		c := bb.Center().X
		bb.MinX, bb.MaxX = c-r.g/2, c+r.g/2
	}
	if bb.H() < r.g {
		c := bb.Center().Y
		bb.MinY, bb.MaxY = c-r.g/2, c+r.g/2
	}
	area := bb.Area()
	i0, j0 := r.cell(bb.MinX, bb.MinY)
	i1, j1 := r.cell(bb.MaxX, bb.MaxY)
	tracks := hpwl / r.g
	for j := j0; j <= j1; j++ {
		for i := i0; i <= i1; i++ {
			c := Rect{r.x0 + float64(i)*r.g, r.y0 + float64(j)*r.g, r.x0 + float64(i+1)*r.g, r.y0 + float64(j+1)*r.g}
			if ov := c.OverlapArea(bb); ov > 0 {
				r.dem[j*r.nx+i] += sign * tracks * ov / area
			}
		}
	}
}

func (r *rudy) applyPin(p Point, sign float64) {
	i, j := r.cell(p.X, p.Y)
	r.dem[j*r.nx+i] += sign * r.pinDem
}

func (r *rudy) cellCost(k int) float64 {
	over := r.dem[k] - r.util*r.cap
	if over <= 0 {
		return 0
	}
	return r.weight * r.g * (over + over*over/math.Max(r.cap, 1))
}

// Total is the whole-grid overflow cost.
func (r *rudy) total() float64 {
	s := 0.0
	for k := range r.dem {
		s += r.cellCost(k)
	}
	return s
}

// begin records the state of the nets and pins of parts about to move.
func (r *rudy) begin(parts []*Part) {
	r.pending = true
	r.mparts = append(r.mparts[:0], parts...)
	r.mnets = r.mnets[:0]
	seen := map[int]bool{}
	for _, p := range parts {
		for _, i := range r.partNets[p] {
			if !seen[i] {
				seen[i] = true
				r.mnets = append(r.mnets, i)
			}
		}
	}
	r.oldBox = r.oldBox[:0]
	for _, i := range r.mnets {
		r.oldBox = append(r.oldBox, r.box[i])
	}
	r.oldPins = r.oldPins[:0]
	for _, p := range parts {
		var ps []Point
		for _, pd := range p.Pads {
			ps = append(ps, pd.Box.C)
		}
		r.oldPins = append(r.oldPins, ps)
	}
}

// commit applies the move to the grid and returns the cost change over the
// touched cells.
func (r *rudy) commit() float64 {
	if !r.pending {
		return 0
	}
	r.newBox = r.newBox[:0]
	for _, i := range r.mnets {
		r.newBox = append(r.newBox, r.netBox(i))
	}
	r.newPins = r.newPins[:0]
	for _, p := range r.mparts {
		var ps []Point
		for _, pd := range p.Pads {
			ps = append(ps, pd.Box.C)
		}
		r.newPins = append(r.newPins, ps)
	}
	// Touched cells.
	r.epoch++
	var cells []int
	mark := func(bb Rect) {
		if bb.Empty() {
			return
		}
		bb = bb.Expand(r.g / 2)
		i0, j0 := r.cell(bb.MinX, bb.MinY)
		i1, j1 := r.cell(bb.MaxX, bb.MaxY)
		for j := j0; j <= j1; j++ {
			for i := i0; i <= i1; i++ {
				k := j*r.nx + i
				if r.stamp[k] != r.epoch {
					r.stamp[k] = r.epoch
					cells = append(cells, k)
				}
			}
		}
	}
	for k := range r.mnets {
		mark(r.oldBox[k])
		mark(r.newBox[k])
	}
	for _, ps := range append(append([][]Point(nil), r.oldPins...), r.newPins...) {
		for _, p := range ps {
			mark(Rect{p.X, p.Y, p.X, p.Y})
		}
	}
	before := 0.0
	for _, k := range cells {
		before += r.cellCost(k)
	}
	r.swap(false)
	after := 0.0
	for _, k := range cells {
		after += r.cellCost(k)
	}
	return after - before
}

// swap moves the grid from old to new state (or back with reverse).
func (r *rudy) swap(reverse bool) {
	from, to := r.oldBox, r.newBox
	fp, tp := r.oldPins, r.newPins
	if reverse {
		from, to, fp, tp = to, from, tp, fp
	}
	for k, i := range r.mnets {
		r.applyNet(from[k], -1)
		r.applyNet(to[k], 1)
		r.box[i] = to[k]
	}
	for k := range fp {
		for _, p := range fp[k] {
			r.applyPin(p, -1)
		}
		for _, p := range tp[k] {
			r.applyPin(p, 1)
		}
	}
}

// revert undoes the last committed move.
func (r *rudy) revert() {
	if r.pending {
		r.swap(true)
	}
	r.pending = false
}

// done accepts the last committed move.
func (r *rudy) done() { r.pending = false }

// CongestionMap reports utilisation (demand / capacity) per cell for the
// current placement — the same model the placer optimises, exported for
// reports and for calibration against real routing.
type CongestionMap struct {
	CellMil  float64   `json:"cellMil"`
	Origin   Point     `json:"origin"`
	NX       int       `json:"nx"`
	NY       int       `json:"ny"`
	Util     []float64 `json:"util"`
	Overflow float64   `json:"overflow"` // Σ max(0, util−0.8) over cells
	MaxUtil  float64   `json:"maxUtil"`
}

// Congestion estimates routing congestion for the board as placed.
func Congestion(b *Board, an *Analysis) *CongestionMap {
	region := EmptyRect()
	for _, p := range b.Outline {
		region = region.AddPoint(p)
	}
	if region.Empty() {
		for _, p := range b.Parts {
			region = region.Union(p.Body())
		}
	}
	r := newRudy(b, an, region, 1)
	cm := &CongestionMap{CellMil: r.g, Origin: Point{r.x0, r.y0}, NX: r.nx, NY: r.ny, Util: make([]float64, len(r.dem))}
	for k, d := range r.dem {
		u := d / math.Max(r.cap, 1e-9)
		cm.Util[k] = u
		cm.Overflow += math.Max(0, u-r.util)
		cm.MaxUtil = math.Max(cm.MaxUtil, u)
	}
	return cm
}
