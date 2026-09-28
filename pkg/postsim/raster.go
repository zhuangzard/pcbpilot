package postsim

import (
	"math"
	"sort"
)

// Grid is the simulation raster: square cells over the board bounds, each
// sampled by Sub×Sub copper sub-samples. Copper is painted at sub-sample
// resolution (tracks as capsules, pads by shape, areas by even-odd scanline
// fill) so a cell knows the fraction of it each net covers.
type Grid struct {
	X0, Y0 float64 // lower-left corner (mil)
	Cell   float64 // mil
	NX, NY int
	Sub    int
	Inside []bool // cell centre on the board (inside the outline, outside cutouts)

	// per stack layer: sub-sample owners (net index+1, 0 = none)
	own  [][]int16 // any copper (thermal coverage, carving)
	area [][]int16 // area copper only: pours, fills, planes (electrical sheet)

	netIdx  map[string]int16
	netName []string
}

func (g *Grid) cells() int        { return g.NX * g.NY }
func (g *Grid) subNX() int        { return g.NX * g.Sub }
func (g *Grid) subNY() int        { return g.NY * g.Sub }
func (g *Grid) subPitch() float64 { return g.Cell / float64(g.Sub) }

// cellCentre of cell index c.
func (g *Grid) cellCentre(c int) Point {
	ix, iy := c%g.NX, c/g.NX
	return Point{g.X0 + (float64(ix)+0.5)*g.Cell, g.Y0 + (float64(iy)+0.5)*g.Cell}
}

// cellAt returns the cell index of p (-1 outside the raster).
func (g *Grid) cellAt(p Point) int {
	ix := int(math.Floor((p.X - g.X0) / g.Cell))
	iy := int(math.Floor((p.Y - g.Y0) / g.Cell))
	if ix < 0 || iy < 0 || ix >= g.NX || iy >= g.NY {
		return -1
	}
	return iy*g.NX + ix
}

// subAt returns the sub-sample index of p (-1 outside).
func (g *Grid) subAt(p Point) int {
	s := g.subPitch()
	ix := int(math.Floor((p.X - g.X0) / s))
	iy := int(math.Floor((p.Y - g.Y0) / s))
	if ix < 0 || iy < 0 || ix >= g.subNX() || iy >= g.subNY() {
		return -1
	}
	return iy*g.subNX() + ix
}

// cellOfSub maps a sub-sample index to its cell.
func (g *Grid) cellOfSub(si int) int {
	sx, sy := si%g.subNX(), si/g.subNX()
	return (sy/g.Sub)*g.NX + sx/g.Sub
}

func (g *Grid) subCentre(sx, sy int) Point {
	s := g.subPitch()
	return Point{g.X0 + (float64(sx)+0.5)*s, g.Y0 + (float64(sy)+0.5)*s}
}

// subRange clips a box to sub-sample index ranges.
func (g *Grid) subRange(r Rect) (x0, y0, x1, y1 int) {
	s := g.subPitch()
	x0 = int(math.Floor((r.MinX - g.X0) / s))
	y0 = int(math.Floor((r.MinY - g.Y0) / s))
	x1 = int(math.Ceil((r.MaxX - g.X0) / s))
	y1 = int(math.Ceil((r.MaxY - g.Y0) / s))
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, g.subNX()-1), min(y1, g.subNY()-1)
	return
}

func (g *Grid) net(n string) int16 {
	if i, ok := g.netIdx[n]; ok {
		return i
	}
	g.netName = append(g.netName, n)
	i := int16(len(g.netName))
	g.netIdx[n] = i
	return i
}

// NewGrid sizes the raster: cellMil (≤0 → 0.5 mm), refined only by the
// cell budget (a large board gets coarser cells so a layer stays ≤ maxCells).
func NewGrid(b *Board, cellMil float64, sub, maxCells int) *Grid {
	if cellMil <= 0 {
		cellMil = 0.5 / MilMm
	}
	if sub <= 0 {
		sub = 5
	}
	if maxCells <= 0 {
		maxCells = 60000
	}
	bb := b.Box
	w, h := bb.MaxX-bb.MinX, bb.MaxY-bb.MinY
	if n := (w / cellMil) * (h / cellMil); n > float64(maxCells) {
		cellMil = math.Sqrt(w * h / float64(maxCells))
	}
	g := &Grid{X0: bb.MinX, Y0: bb.MinY, Cell: cellMil, Sub: sub, netIdx: map[string]int16{}}
	g.NX = max(1, int(math.Ceil(w/cellMil)))
	g.NY = max(1, int(math.Ceil(h/cellMil)))
	g.Inside = make([]bool, g.cells())
	outline := [][]Point{b.Outline}
	for c := range g.Inside {
		p := g.cellCentre(c)
		if !insideEvenOdd(p, outline) {
			continue
		}
		in := true
		for _, ho := range b.Holes {
			if p.X >= ho.Box.MinX && p.X <= ho.Box.MaxX && p.Y >= ho.Box.MinY && p.Y <= ho.Box.MaxY && insideEvenOdd(p, ho.Contours) {
				in = false
				break
			}
		}
		g.Inside[c] = in
	}
	return g
}

// Rasterise paints the board's copper on every stack layer.
func (g *Grid) Rasterise(b *Board, st *Stackup) {
	nl := len(st.Layers)
	ns := g.subNX() * g.subNY()
	g.own = make([][]int16, nl)
	g.area = make([][]int16, nl)
	for k := range g.own {
		g.own[k] = make([]int16, ns)
		g.area[k] = make([]int16, ns)
	}
	// 1. Materialised area copper (poured fills, static fills).
	for _, a := range b.Areas {
		if a.Carve {
			continue
		}
		if k := st.index(a.Layer); k >= 0 {
			v := g.net(a.Net)
			g.fillPoly(a.Contours, a.Box, func(si int) { g.own[k][si], g.area[k][si] = v, v })
		}
	}
	// 2. Pads, tracks, via rings.
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			v := g.net(pd.Net)
			for k, l := range st.Layers {
				if pd.OnLayer(l.ID) {
					g.paintPad(pd, 0, func(si int) { g.own[k][si] = v })
				}
			}
		}
	}
	for _, t := range b.Tracks {
		if k := st.index(t.Layer); k >= 0 {
			v := g.net(t.Net)
			g.paintCapsule(t.A, t.B, t.W/2, func(si int) { g.own[k][si] = v })
		}
	}
	for _, via := range b.Vias {
		v := g.net(via.Net)
		for k := range st.Layers {
			g.paintCapsule(via.C, via.C, via.Dia/2, func(si int) { g.own[k][si] = v })
		}
	}
	// 3. Carved areas: unpoured pour outlines and negative planes. Their
	// copper is the outline minus (other nets' copper + clearance), minus the
	// copper-to-edge strip and cutouts.
	var carved []Area
	for _, a := range b.Areas {
		if a.Carve {
			carved = append(carved, a)
		}
	}
	if len(carved) == 0 {
		return
	}
	edge := g.edgeMask(b)
	for _, a := range carved {
		k := st.index(a.Layer)
		if k < 0 {
			continue
		}
		v := g.net(a.Net)
		blocked := g.otherNetMask(k, v, b.Rules.ClearanceMil)
		g.fillPoly(a.Contours, a.Box, func(si int) {
			if edge[si] || blocked[si] {
				return
			}
			if o := g.own[k][si]; o == 0 || o == v {
				g.own[k][si], g.area[k][si] = v, v
			}
		})
	}
}

// edgeMask marks sub-samples outside the board or within copper-to-edge of
// the outline / a cutout.
func (g *Grid) edgeMask(b *Board) []bool {
	snx, sny := g.subNX(), g.subNY()
	m := make([]bool, snx*sny)
	e := b.Rules.CopperToEdgeMil
	outline := [][]Point{b.Outline}
	edges := func(cs [][]Point, fn func(a, b Point)) {
		for _, c := range cs {
			for i := range c {
				fn(c[i], c[(i+1)%len(c)])
			}
		}
	}
	for sy := 0; sy < sny; sy++ {
		for sx := 0; sx < snx; sx++ {
			p := g.subCentre(sx, sy)
			si := sy*snx + sx
			if !insideEvenOdd(p, outline) {
				m[si] = true
			}
		}
	}
	mark := func(a, bb Point, r float64) {
		box := Rect{math.Min(a.X, bb.X) - r, math.Min(a.Y, bb.Y) - r, math.Max(a.X, bb.X) + r, math.Max(a.Y, bb.Y) + r}
		x0, y0, x1, y1 := g.subRange(box)
		for sy := y0; sy <= y1; sy++ {
			for sx := x0; sx <= x1; sx++ {
				if segDist(g.subCentre(sx, sy), a, bb) <= r {
					m[sy*snx+sx] = true
				}
			}
		}
	}
	edges(outline, func(a, bb Point) { mark(a, bb, e) })
	for _, h := range b.Holes {
		edges(h.Contours, func(a, bb Point) { mark(a, bb, e) })
		x0, y0, x1, y1 := g.subRange(h.Box)
		for sy := y0; sy <= y1; sy++ {
			for sx := x0; sx <= x1; sx++ {
				if insideEvenOdd(g.subCentre(sx, sy), h.Contours) {
					m[sy*snx+sx] = true
				}
			}
		}
	}
	return m
}

// otherNetMask dilates every other net's copper on layer k by clearance.
func (g *Grid) otherNetMask(k int, net int16, clearance float64) []bool {
	snx, sny := g.subNX(), g.subNY()
	m := make([]bool, snx*sny)
	s := g.subPitch()
	r := int(math.Ceil(clearance / s))
	var offs [][2]int
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if math.Hypot(float64(dx), float64(dy))*s <= clearance+0.5*s {
				offs = append(offs, [2]int{dx, dy})
			}
		}
	}
	own := g.own[k]
	for sy := 0; sy < sny; sy++ {
		for sx := 0; sx < snx; sx++ {
			o := own[sy*snx+sx]
			if o == 0 || o == net {
				continue
			}
			for _, d := range offs {
				x, y := sx+d[0], sy+d[1]
				if x >= 0 && y >= 0 && x < snx && y < sny {
					m[y*snx+x] = true
				}
			}
		}
	}
	return m
}

// paintCapsule calls fn for sub-samples within r of segment ab.
func (g *Grid) paintCapsule(a, b Point, r float64, fn func(int)) {
	// Keep a thin trace connected on the raster: at least half a sub-sample.
	r = math.Max(r, 0.5*g.subPitch()*math.Sqrt2)
	box := Rect{math.Min(a.X, b.X) - r, math.Min(a.Y, b.Y) - r, math.Max(a.X, b.X) + r, math.Max(a.Y, b.Y) + r}
	x0, y0, x1, y1 := g.subRange(box)
	snx := g.subNX()
	for sy := y0; sy <= y1; sy++ {
		for sx := x0; sx <= x1; sx++ {
			if segDist(g.subCentre(sx, sy), a, b) <= r {
				fn(sy*snx + sx)
			}
		}
	}
}

func (g *Grid) paintPad(p *Pad, margin float64, fn func(int)) {
	x0, y0, x1, y1 := g.subRange(p.bounds().expand(margin))
	snx := g.subNX()
	hit := false
	for sy := y0; sy <= y1; sy++ {
		for sx := x0; sx <= x1; sx++ {
			if p.contains(g.subCentre(sx, sy), margin) {
				fn(sy*snx + sx)
				hit = true
			}
		}
	}
	if !hit { // smaller than a sub-sample: its centre
		if si := g.subAt(p.C); si >= 0 {
			fn(si)
		}
	}
}

// fillPoly scanline-fills even-odd contours at sub-sample centres.
func (g *Grid) fillPoly(cs [][]Point, box Rect, fn func(int)) {
	x0, y0, x1, y1 := g.subRange(box)
	snx := g.subNX()
	s := g.subPitch()
	var xs []float64
	for sy := y0; sy <= y1; sy++ {
		y := g.Y0 + (float64(sy)+0.5)*s
		xs = xs[:0]
		for _, c := range cs {
			n := len(c)
			for i, j := 0, n-1; i < n; j, i = i, i+1 {
				a, b := c[i], c[j]
				if (a.Y > y) != (b.Y > y) {
					xs = append(xs, a.X+(y-a.Y)*(b.X-a.X)/(b.Y-a.Y))
				}
			}
		}
		sort.Float64s(xs)
		for i := 0; i+1 < len(xs); i += 2 {
			// sub-sample centres with xs[i] <= x < xs[i+1]
			lo := int(math.Ceil((xs[i]-g.X0)/s - 0.5))
			hi := int(math.Ceil((xs[i+1]-g.X0)/s-0.5)) - 1
			lo, hi = max(lo, x0), min(hi, x1)
			for sx := lo; sx <= hi; sx++ {
				fn(sy*snx + sx)
			}
		}
	}
}

// coverage returns, per cell, the fraction of sub-samples of layer k owned
// by net (net 0 = any copper). area selects the area-only map.
func (g *Grid) coverage(k int, net int16, area bool) []float64 {
	src := g.own[k]
	if area {
		src = g.area[k]
	}
	out := make([]float64, g.cells())
	inv := 1 / float64(g.Sub*g.Sub)
	snx := g.subNX()
	for si, o := range src {
		if o == 0 || net != 0 && o != net {
			continue
		}
		sx, sy := si%snx, si/snx
		out[(sy/g.Sub)*g.NX+sx/g.Sub] += inv
	}
	return out
}

// boundaryShare is the fraction of the shared edge between cell c and its
// +x (dir 0) or +y (dir 1) neighbour where both sides carry net copper in
// the area map of layer k.
func (g *Grid) boundaryShare(k, c, dir int, net int16) float64 {
	ix, iy := c%g.NX, c/g.NX
	snx := g.subNX()
	m := g.area[k]
	n := 0
	for i := 0; i < g.Sub; i++ {
		var a, b int
		if dir == 0 {
			sy := iy*g.Sub + i
			a, b = sy*snx+(ix+1)*g.Sub-1, sy*snx+(ix+1)*g.Sub
		} else {
			sx := ix*g.Sub + i
			a, b = ((iy+1)*g.Sub-1)*snx+sx, (iy+1)*g.Sub*snx+sx
		}
		if m[a] == net && m[b] == net {
			n++
		}
	}
	return float64(n) / float64(g.Sub)
}
