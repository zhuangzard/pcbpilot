package pcbauto

import "math"

// gridLegaliseParts is the movable-part count from which the legaliser
// seats parts through a free-space grid before it spirals. A spiral that
// finds no free spot scans four turns of 400 rings, 2.5 million poses, and
// reaches 400 steps — on a board of thousands of parts and hundreds of
// millimetres most of the free room lies farther, so part after part paid
// the whole scan for nothing and three passes did not end within the hour.
const gridLegaliseParts = 800

const (
	freeGridCell  = 5.0  // mil; what a box loses to the grid per axis, at most
	freeGridCells = 4096 // cells per axis at most
	// gridVerifyMax bounds the grid-free spots tried per part whose full
	// cost was not zero after all (pad edge bands, isolation gaps: terms
	// the grid does not hold).
	gridVerifyMax = 4000
	// gridSpiralRings is the reach of the spiral of a part the grid found
	// no room for: it can only lessen the overlap nearby.
	gridSpiralRings = 48
)

// freeGrid is the room taken on each board side, in cells of g mil over
// the placement region: every part's box (courtyard and spacing), the
// keep-outs and the board's holes, rasterised outward. A box whose cells
// all count zero overlaps none of them.
type freeGrid struct {
	x0, y0, g float64
	nx, ny    int
	wpr       int         // words per row of bits
	cnt       [3][]uint16 // by side (LayerTop, LayerBottom)
	bits      [3][]uint64 // cnt != 0, a row of wpr words per grid row
	cu        [3][]uint64 // copper-only keep-outs: they bar all but bridges
	np        [3][]uint64 // parts-only keep-outs: they bar the parts mounted on that side
	filed     map[*Part]Rect
	// full[zone and sides] are the boxes (cells, wider side first; as they
	// stand when parts may not turn) the grid had no room for: a box at
	// least as large in both has none either, until a part moves or the
	// next pass of the legaliser asks again. The room a part leaves by
	// being taken out itself is asked apart.
	full map[gridZone][][2]int
}

type gridZone struct {
	z     Rect
	sides int
}

func partSides(p *Part) []int {
	switch {
	case hasTHT(p):
		return []int{LayerTop, LayerBottom}
	case p.Side == LayerBottom:
		return []int{LayerBottom}
	}
	return []int{LayerTop}
}

func (pl *placer) newFreeGrid() *freeGrid {
	r := pl.region
	w, h := r.MaxX-r.MinX, r.MaxY-r.MinY
	if !(w > 0 && h > 0) {
		return nil
	}
	g := math.Max(freeGridCell, math.Max(w, h)/freeGridCells)
	fg := &freeGrid{x0: r.MinX, y0: r.MinY, g: g, nx: int(math.Ceil(w / g)), ny: int(math.Ceil(h / g)), filed: map[*Part]Rect{}, full: map[gridZone][][2]int{}}
	fg.wpr = (fg.nx + 63) / 64
	for _, s := range []int{LayerTop, LayerBottom} {
		fg.cnt[s] = make([]uint16, fg.nx*fg.ny)
		fg.bits[s] = make([]uint64, fg.wpr*fg.ny)
		fg.cu[s] = make([]uint64, fg.wpr*fg.ny)
		fg.np[s] = make([]uint64, fg.wpr*fg.ny)
	}
	for _, k := range pl.b.Keepouts {
		if !(k.NoParts || k.NoCopper) || k.Owner != "" {
			continue
		}
		for _, s := range []int{LayerTop, LayerBottom} {
			if len(k.Layers) != 0 && !k.onLayer(s) {
				continue
			}
			if k.NoParts && k.NoCopper {
				fg.mark(PolyBounds(k.Poly), s, 1)
				continue
			}
			// A bridge may straddle a copper-only keep-out, and a part
			// mounted on the other side may stand under a parts-only one,
			// through-hole or not (repelsPart, noPartsOn).
			bits := fg.cu[s]
			if k.NoParts {
				bits = fg.np[s]
			}
			if ix0, iy0, ix1, iy1, ok := fg.cells(PolyBounds(k.Poly)); ok {
				for y := iy0; y <= iy1; y++ {
					for x := ix0; x <= ix1; x++ {
						bits[y*fg.wpr+x>>6] |= 1 << (x & 63)
					}
				}
			}
		}
	}
	for _, hl := range pl.b.Holes {
		if hl.Owner == "" {
			hr := hl.Bounds().Expand(hl.Keep)
			fg.mark(hr, LayerTop, 1)
			fg.mark(hr, LayerBottom, 1)
		}
	}
	for _, q := range pl.b.Parts {
		fg.put(q, pl.boxes[q])
	}
	return fg
}

// cells is the cell range a rectangle touches (outward), clipped to the
// grid; ok is false when it lies off the grid.
func (fg *freeGrid) cells(r Rect) (ix0, iy0, ix1, iy1 int, ok bool) {
	const eps = 1e-6
	ix0 = int(math.Floor((r.MinX-fg.x0)/fg.g + eps))
	iy0 = int(math.Floor((r.MinY-fg.y0)/fg.g + eps))
	ix1 = int(math.Ceil((r.MaxX-fg.x0)/fg.g-eps)) - 1
	iy1 = int(math.Ceil((r.MaxY-fg.y0)/fg.g-eps)) - 1
	ix0, iy0 = max(ix0, 0), max(iy0, 0)
	ix1, iy1 = min(ix1, fg.nx-1), min(iy1, fg.ny-1)
	return ix0, iy0, ix1, iy1, ix0 <= ix1 && iy0 <= iy1
}

func (fg *freeGrid) mark(r Rect, side, d int) {
	ix0, iy0, ix1, iy1, ok := fg.cells(r)
	if !ok {
		return
	}
	c, bits := fg.cnt[side], fg.bits[side]
	for y := iy0; y <= iy1; y++ {
		row := c[y*fg.nx+ix0 : y*fg.nx+ix1+1]
		for i := range row {
			row[i] = uint16(int(row[i]) + d)
			x := ix0 + i
			if w, m := &bits[y*fg.wpr+x>>6], uint64(1)<<(x&63); row[i] != 0 {
				*w |= m
			} else {
				*w &^= m
			}
		}
	}
}

// put files part p's box bx; take removes what put filed.
func (fg *freeGrid) put(p *Part, bx Rect) {
	fg.filed[p] = bx
	for _, s := range partSides(p) {
		fg.mark(bx, s, 1)
	}
}

func (fg *freeGrid) take(p *Part) {
	bx, ok := fg.filed[p]
	if !ok {
		return
	}
	delete(fg.filed, p)
	for _, s := range partSides(p) {
		fg.mark(bx, s, -1)
	}
}

// free reports whether the kw x kh cells from (ix0, iy0) are all empty on
// side; cu counts the copper-only keep-outs as taken (all parts but
// bridges), np the parts-only ones (the side the part is mounted on).
func (fg *freeGrid) free(side, ix0, iy0, kw, kh int, cu, np bool) bool {
	ix1 := ix0 + kw - 1
	w0, w1 := ix0>>6, ix1>>6
	m0, m1 := ^uint64(0)<<(ix0&63), ^uint64(0)>>(63-ix1&63)
	if w0 == w1 {
		m0, m1 = m0&m1, 0
	}
	for i, bits := range [3][]uint64{fg.bits[side], fg.cu[side], fg.np[side]} {
		if i == 1 && !cu || i == 2 && !np {
			continue
		}
		for y := iy0; y < iy0+kh; y++ {
			row := bits[y*fg.wpr : (y+1)*fg.wpr]
			if row[w0]&m0 != 0 || row[w1]&m1 != 0 {
				return false
			}
			for w := w0 + 1; w < w1; w++ {
				if row[w] != 0 {
					return false
				}
			}
		}
	}
	return true
}

// gridSeat moves part p to the free spot nearest to where it stands, inside
// its zone, at its rotation or a quarter turn from it: the nearest spot the
// grid shows free whose full hard cost is zero. A bridge (a part across an
// isolation barrier) has its centre in its zone, not its body, may stand on
// a copper-only keep-out, and is tried at all four turns: which way it
// faces decides which of its pads meet which domain. It reports whether it
// found a spot; if not p stands where it stood. Either way p is left out of
// the grid (the caller files it again) and in its bucket.
func (pl *placer) gridSeat(fg *freeGrid, p *Part) bool {
	fg.take(p)
	sides := partSides(p)
	z, ok := pl.zoneOf[p]
	if !ok {
		z = pl.region
	}
	z = pl.inRegion(z)
	bridge := pl.c.IsBridge(p.Ref)
	orig, rot0 := p.Body().Center(), p.Rotation
	type turn struct {
		rot            float64
		kw, kh         int // the box, in cells
		x0, x1, y0, y1 int // where its first cell may lie
		ox, oy         int // the cell nearest to where the part stands
	}
	var turns []turn
	bx := pl.boxes[p]
	bw, bh := bx.MaxX-bx.MinX, bx.MaxY-bx.MinY
	const eps = 1e-6
	zx0 := int(math.Ceil((z.MinX-fg.x0)/fg.g - eps))
	zy0 := int(math.Ceil((z.MinY-fg.y0)/fg.g - eps))
	zx1 := int(math.Floor((z.MaxX-fg.x0)/fg.g+eps)) - 1
	zy1 := int(math.Floor((z.MaxY-fg.y0)/fg.g+eps)) - 1
	zx0, zy0, zx1, zy1 = max(zx0, 0), max(zy0, 0), min(zx1, fg.nx-1), min(zy1, fg.ny-1)
	add := func(rot, w, h float64) {
		t := turn{rot: rot, kw: max(1, int(math.Ceil(w/fg.g-eps))), kh: max(1, int(math.Ceil(h/fg.g-eps)))}
		t.x0, t.y0, t.x1, t.y1 = zx0, zy0, zx1-t.kw+1, zy1-t.kh+1
		if bridge {
			// One cell more each way: the centre is then moved into the zone
			// (a line, for a bridge on its barrier) by half a cell at most,
			// and the box stays on the cells tested. The first cell lies
			// where that centre can reach the zone, the box on the board.
			t.kw, t.kh = t.kw+1, t.kh+1
			hw, hh := float64(t.kw)/2, float64(t.kh)/2
			t.x0 = max(0, int(math.Ceil((z.MinX-fg.x0)/fg.g-hw-0.5-eps)))
			t.y0 = max(0, int(math.Ceil((z.MinY-fg.y0)/fg.g-hh-0.5-eps)))
			t.x1 = min(fg.nx-t.kw, int(math.Floor((z.MaxX-fg.x0)/fg.g-hw+0.5+eps)))
			t.y1 = min(fg.ny-t.kh, int(math.Floor((z.MaxY-fg.y0)/fg.g-hh+0.5+eps)))
		}
		if t.x1 < t.x0 || t.y1 < t.y0 {
			return
		}
		t.ox = min(max(int(math.Round((orig.X-fg.x0)/fg.g-float64(t.kw)/2)), t.x0), t.x1)
		t.oy = min(max(int(math.Round((orig.Y-fg.y0)/fg.g-float64(t.kh)/2)), t.y0), t.y1)
		turns = append(turns, t)
	}
	add(rot0, bw, bh)
	switch {
	case pl.opt.NoRotate:
	case bridge:
		add(normDeg(rot0+90), bh, bw)
		add(normDeg(rot0+180), bw, bh)
		add(normDeg(rot0+270), bh, bw)
	case math.Abs(bw-bh) > 1:
		add(normDeg(rot0+90), bh, bw)
	}
	if len(turns) == 0 {
		return false
	}
	// A box no smaller than one the zone had no room for has none either.
	zk := gridZone{z, len(sides)*4 + sides[0]}
	if bridge {
		zk.sides += 16
	}
	dims := [2]int{max(turns[0].kw, turns[0].kh), min(turns[0].kw, turns[0].kh)}
	if pl.opt.NoRotate {
		dims = [2]int{turns[0].kw, turns[0].kh}
	}
	// The cells p itself held are free now and were not when the zone was
	// found full: if nothing else is on them, the search is run.
	own := false
	if ix0, iy0, ix1, iy1, ok := fg.cells(bx); ok {
		own = true
		for _, s := range sides {
			own = own && fg.free(s, ix0, iy0, ix1-ix0+1, iy1-iy0+1, !bridge, s == p.Side)
		}
	}
	if !own {
		for _, f := range fg.full[zk] {
			if dims[0] >= f[0] && dims[1] >= f[1] {
				return false
			}
		}
	}
	maxR := 0
	for _, t := range turns {
		maxR = max(maxR, max(max(t.ox-t.x0, t.x1-t.ox), max(t.oy-t.y0, t.y1-t.oy)))
	}
	pl.bucketOp(p, false)
	found, tries, cut := false, 0, false
	// The spots that were free but not legal (a pad inside another domain's
	// creepage, mostly: the grid does not hold that). Their neighbours are
	// seldom better, so the more of them, the wider the berth given to the
	// last few; without it the tries all went to one small patch.
	var failed [8][2]int
	try := func(t *turn, ix, iy int) bool {
		if ix < t.x0 || ix > t.x1 || iy < t.y0 || iy > t.y1 {
			return false
		}
		if berth := tries / 16; berth > 0 {
			for _, f := range failed[:min(tries, len(failed))] {
				if max(f[0]-ix, ix-f[0], f[1]-iy, iy-f[1]) <= berth {
					return false
				}
			}
		}
		for _, s := range sides {
			if !fg.free(s, ix, iy, t.kw, t.kh, !bridge, s == p.Side) {
				return false
			}
		}
		c := Point{fg.x0 + (float64(ix)+float64(t.kw)/2)*fg.g, fg.y0 + (float64(iy)+float64(t.kh)/2)*fg.g}
		if bridge {
			c = Point{math.Min(math.Max(c.X, z.MinX), z.MaxX), math.Min(math.Max(c.Y, z.MinY), z.MaxY)}
		}
		movePartCentre(p, c, t.rot)
		pl.boxes[p] = pl.box(p)
		failed[tries%len(failed)] = [2]int{ix, iy}
		tries++
		return pl.hardCostLim(p, 1e-6+1e-9) <= 1e-6
	}
search:
	for r := 0; r <= maxR && tries < gridVerifyMax; r++ {
		if r&31 == 31 && pl.overBudget() {
			cut = true
			break
		}
		for i := range turns {
			t := &turns[i]
			if r == 0 {
				if try(t, t.ox, t.oy) {
					found = true
					break search
				}
				continue
			}
			// The ring's four edges, nearest the axes first is not worth
			// the code: any spot on ring r is within one ring of the nearest.
			for d := -r; d <= r; d++ {
				if try(t, t.ox+d, t.oy-r) || try(t, t.ox+d, t.oy+r) {
					found = true
					break search
				}
			}
			for d := -r + 1; d < r; d++ {
				if try(t, t.ox-r, t.oy+d) || try(t, t.ox+r, t.oy+d) {
					found = true
					break search
				}
			}
		}
	}
	if found {
		clear(fg.full) // the spot p left may be the room another needs
	} else {
		movePartCentre(p, orig, rot0)
		pl.boxes[p] = pl.box(p)
		if tries == 0 && !cut {
			// Not one free spot in the whole zone (rather than spots whose
			// full cost was not zero): the same goes for any larger box.
			fg.full[zk] = append(fg.full[zk], dims)
		}
	}
	pl.bucketOp(p, true)
	return found
}
