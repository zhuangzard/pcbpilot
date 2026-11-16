package pcbauto

import (
	"sort"
	"testing"
	"time"
)

// Every movable part piled on one spot: the grid seats each on a spot of
// zero hard cost, largest first, and none overlaps another afterwards.
func TestGridSeatUnpilesAStack(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	c := pl.region.Center()
	var parts []*Part
	for _, p := range pl.movable {
		if pl.c.IsBridge(p.Ref) {
			continue
		}
		pl.zoneOf[p] = pl.region
		movePartCentre(p, c, p.Rotation)
		parts = append(parts, p)
	}
	if len(parts) < 10 {
		t.Skip("fixture has too few movable parts")
	}
	pl.rebuildBuckets()
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].Body().Area() > parts[j].Body().Area() })
	fg := pl.newFreeGrid()
	if fg == nil {
		t.Fatal("no grid")
	}
	start := time.Now()
	seated := 0
	for _, p := range parts {
		if pl.hardCost(p) <= 1e-6 {
			continue
		}
		if !pl.gridSeat(fg, p) {
			fg.put(p, pl.boxes[p])
			continue
		}
		fg.put(p, pl.boxes[p])
		seated++
		if hc := pl.hardCost(p); hc > 1e-6 {
			t.Fatalf("%s seated at hard cost %g", p.Ref, hc)
		}
		if outside(p.Body(), pl.region) > 1 {
			t.Fatalf("%s seated outside the placement region", p.Ref)
		}
	}
	if seated == 0 {
		t.Fatal("the grid seated nothing")
	}
	left := 0
	for _, p := range parts {
		if pl.hardCost(p) > 1e-6 {
			left++
		}
	}
	if left > 0 {
		t.Fatalf("%d of %d parts still illegal on a board with room", left, len(parts))
	}
	if el := time.Since(start); el > 20*time.Second {
		t.Fatalf("seating %d parts took %s", len(parts), el)
	}
}

// The grid's count of a cell range is the cells some box touches, on the
// side asked; a part taken out leaves nothing behind.
func TestFreeGridCountsPerSide(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	var smd *Part
	for _, p := range pl.movable {
		if !hasTHT(p) {
			smd = p
			break
		}
	}
	if smd == nil {
		t.Skip("fixture has no surface-mount part")
	}
	fg := pl.newFreeGrid()
	for _, p := range pl.b.Parts {
		fg.take(p)
	}
	count := func(side, ix0, iy0, ix1, iy1 int) (n int) {
		for y := iy0; y <= iy1; y++ {
			for x := ix0; x <= ix1; x++ {
				if fg.cnt[side][y*fg.nx+x] != 0 {
					n++
				}
				if (fg.cnt[side][y*fg.nx+x] != 0) == fg.free(side, x, y, 1, 1, true, false) {
					t.Fatalf("cell %d,%d: count and bit disagree", x, y)
				}
			}
		}
		return n
	}
	// Keep-outs and holes aside, the grid is now what they left; note it.
	base := [3]int{0, count(LayerTop, 0, 0, fg.nx-1, fg.ny-1), count(LayerBottom, 0, 0, fg.nx-1, fg.ny-1)}
	smd.Side = LayerBottom
	if fg.nx < 70 {
		t.Skip("fixture board too small")
	}
	// Far from the corner: across a word boundary of the bit rows.
	bx := Rect{fg.x0 + 62*fg.g, fg.y0 + 2*fg.g, fg.x0 + 66.5*fg.g, fg.y0 + 4*fg.g}
	wasFree := fg.free(LayerBottom, 62, 2, 5, 2, true, false)
	fg.put(smd, bx)
	if n := count(LayerTop, 0, 0, fg.nx-1, fg.ny-1); n != base[LayerTop] {
		t.Fatalf("a bottom part takes %d top cells", n-base[LayerTop])
	}
	// 62..66.5 touches cells 62 to 66; 2..4 cells 2 and 3.
	if n := count(LayerBottom, 62, 2, 66, 3); n != 10 {
		t.Fatalf("the box takes %d of its 10 cells", n)
	}
	if fg.free(LayerBottom, 60, 1, 8, 4, true, false) || fg.free(LayerBottom, 66, 3, 1, 1, true, false) {
		t.Fatal("cells under the box read free")
	}
	if n := count(LayerBottom, 0, 0, fg.nx-1, fg.ny-1) - base[LayerBottom]; n < 0 || n > 10 {
		t.Fatalf("the box added %d cells, want at most 10", n)
	}
	fg.take(smd)
	if n := count(LayerBottom, 0, 0, fg.nx-1, fg.ny-1); n != base[LayerBottom] {
		t.Fatalf("taking the part out left %d cells", n-base[LayerBottom])
	}
	if fg.free(LayerBottom, 62, 2, 5, 2, true, false) != wasFree {
		t.Fatal("the cells did not return to what they were")
	}
}

// A capped spiral scans its few rings only, and starts at the zone when
// the zone lies beyond them.
func TestShortSpiralStartsAtAFarZone(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	var p *Part
	for _, q := range pl.movable {
		if len(q.Pads) == 2 && !pl.c.IsBridge(q.Ref) {
			p = q
			break
		}
	}
	if p == nil {
		t.Skip("fixture has no two-pad part")
	}
	g := pl.region
	zone := Rect{g.MaxX - 400, g.MaxY - 400, g.MaxX, g.MaxY}
	pl.zoneOf[p] = zone
	movePartCentre(p, Point{g.MinX + 100, g.MinY + 100}, p.Rotation)
	pl.rebuildBuckets()
	if zone.Center().Dist(p.Body().Center()) < 48*12*2 {
		t.Skip("fixture board too small for a far zone")
	}
	pl.spiralRings = gridSpiralRings
	start := time.Now()
	pl.spiral(p)
	pl.spiralRings = 0
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("a %d-ring spiral took %s", gridSpiralRings, el)
	}
	if c := p.Body().Center(); outside(Rect{c.X, c.Y, c.X + 1, c.Y + 1}, zone.Expand(gridSpiralRings*12+50)) > 0 {
		t.Fatalf("the short spiral left the part at %v, not at its zone %v", c, zone)
	}
}

// isoCost's indexed lookups and shared pad bounds give, part for part, the
// sum the plain form gives: every pad pair's domains by net name, each
// pad's bounds computed per pair.
func TestIsoCostMatchesThePlainSum(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	names := []string{"", "d1", "d2", "d3"}
	iso := &IsoRules{NetDomain: map[string]string{}, byKey: map[[2]string]*IsoPair{}}
	for i, n := range pl.b.Nets() {
		if d := names[i%len(names)]; d != "" {
			iso.NetDomain[n.Name] = d
		}
	}
	for _, pr := range []*IsoPair{{A: "d1", B: "d2", ClearanceMil: 120, CreepageMil: 260}, {A: "d3", B: "d2", ClearanceMil: 80, CreepageMil: 140}} {
		iso.Pairs = append(iso.Pairs, pr)
		iso.byKey[isoKey(pr.A, pr.B)] = pr
	}
	pl.an.Iso = iso
	// Pull the parts together so that pads stand inside each other's creepage.
	c := pl.region.Center()
	for i, p := range pl.movable {
		at := p.Body().Center()
		movePartCentre(p, Point{c.X + (at.X-c.X)*0.35, c.Y + (at.Y-c.Y)*0.35}, normDeg(p.Rotation+float64(i%4)*45))
	}
	pl.rebuildBuckets()
	plain := func(p *Part, bx Rect) float64 {
		cost := 0.0
		pl.forBucketsOnce(bx.Expand(iso.MaxClearanceMil()), func(q *Part) bool {
			if q == p {
				return true
			}
			for _, a := range p.Pads {
				da := iso.NetDomain[a.Net]
				if da == "" {
					continue
				}
				for _, c := range q.Pads {
					ip := iso.Pair(da, iso.NetDomain[c.Net])
					if ip == nil {
						continue
					}
					if short := ip.CreepageMil - isoPadGap(a, c, ip.CreepageMil); short > 0 {
						cost += 800 * short
					}
				}
			}
			return true
		})
		return cost
	}
	nonZero := 0
	for range 2 { // the second round reads the tables the first one built
		for _, p := range pl.b.Parts {
			want, got := plain(p, pl.boxes[p]), pl.isoCost(p, pl.boxes[p])
			if got != want {
				t.Fatalf("%s: isoCost %v, the plain sum %v", p.Ref, got, want)
			}
			if want > 0 {
				nonZero++
			}
		}
	}
	if nonZero == 0 {
		t.Fatal("the fixture put no pads inside a creepage: nothing was compared")
	}
}

// A bridge is seated by the grid too: its centre on its zone (a strip on
// the barrier, which its body overhangs), at zero hard cost, from wherever
// it stood.
func TestGridSeatSeatsABridgeOnItsStrip(t *testing.T) {
	b := tieBoard()
	an := Analyze(b, PowerSpec{}, nil)
	pl := &placer{b: b, an: an, c: Understand(b, an), m: &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}},
		opt: PlaceOptions{SpacingMil: 12, Timeout: time.Minute}, partNet: map[*Part][]int{}, zoneOf: map[*Part]Rect{}, decap: map[*Part]*Pad{}, spacing: 12}
	pl.setup(&PlaceResult{})
	pl.rebuildBuckets()
	g := pl.region
	var bridges []*Part
	for _, p := range pl.movable {
		if pl.c.IsBridge(p.Ref) {
			bridges = append(bridges, p)
		}
	}
	if len(bridges) == 0 {
		t.Skip("fixture has no movable bridge")
	}
	seated := 0
	for _, p := range bridges {
		z, ok := pl.zoneOf[p]
		if !ok {
			// A strip one grid cell wide would do; this one is a line's width.
			z = Rect{g.Center().X - 1, g.MinY, g.Center().X + 1, g.MaxY}
			pl.zoneOf[p] = z
		}
		z = pl.inRegion(z)
		movePartCentre(p, Point{g.MinX + 40, g.MinY + 40}, p.Rotation)
		pl.boxes[p] = pl.box(p)
		pl.rebuildBuckets()
		fg := pl.newFreeGrid()
		if !pl.gridSeat(fg, p) {
			continue
		}
		seated++
		if hc := pl.hardCost(p); hc > 1e-6 {
			t.Fatalf("%s seated at hard cost %g", p.Ref, hc)
		}
		if c := p.Body().Center(); c.X < z.MinX-1e-6 || c.X > z.MaxX+1e-6 || c.Y < z.MinY-1e-6 || c.Y > z.MaxY+1e-6 {
			t.Fatalf("%s seated with its centre %v off its zone %v", p.Ref, c, z)
		}
	}
	if seated == 0 {
		t.Fatalf("none of %d bridges was seated", len(bridges))
	}
}

// What the zone had no room for is asked again once a part has moved, and
// a part whose own cells are free is searched whatever the cache says.
func TestGridFullCacheForgetsOnAMove(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	var p *Part
	for _, q := range pl.movable {
		if !pl.c.IsBridge(q.Ref) && !hasTHT(q) {
			p = q
			break
		}
	}
	if p == nil {
		t.Skip("fixture has no surface-mount part")
	}
	pl.zoneOf[p] = pl.region
	fg := pl.newFreeGrid()
	zk := gridZone{pl.inRegion(pl.region), 4 + partSides(p)[0]}
	fg.full[zk] = [][2]int{{1, 1}} // "not even one cell is free"
	at := p.Body().Center()
	if !pl.gridSeat(fg, p) {
		t.Fatal("a part standing on free cells of its own was refused by the cache")
	}
	if len(fg.full) != 0 {
		t.Fatal("the cache outlived a move")
	}
	if p.Body().Center().Dist(at) > 3*fg.g {
		t.Fatalf("the part went from %v to %v with its own spot free", at, p.Body().Center())
	}
}

// A parts-only keep-out on the top bars the parts mounted there and no
// others: a part on the bottom, through-hole or not, may stand under it
// (noPartsOn). With copper kept out too it bars both.
func TestFreeGridPartsOnlyKeepoutIsPerMountedSide(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	g := pl.region
	sq := []Point{{g.MinX + 100, g.MinY + 100}, {g.MinX + 300, g.MinY + 100}, {g.MinX + 300, g.MinY + 300}, {g.MinX + 100, g.MinY + 300}}
	for _, cu := range []bool{false, true} {
		k := Keepout{Poly: sq, Layers: []int{LayerTop}, NoParts: true, NoCopper: cu}
		kept := pl.b.Keepouts
		pl.b.Keepouts = append(append(kept[:0:0], kept...), &k)
		fg := pl.newFreeGrid()
		pl.b.Keepouts = kept
		for _, p := range pl.b.Parts {
			fg.take(p)
		}
		ix0, iy0, ix1, iy1, ok := fg.cells(PolyBounds(sq))
		if !ok {
			t.Fatal("the keep-out lies off the grid")
		}
		kw, kh := ix1-ix0+1, iy1-iy0+1
		if fg.free(LayerTop, ix0, iy0, kw, kh, true, true) {
			t.Fatalf("noCopper %v: a part mounted on top found room in the keep-out", cu)
		}
		// A through-hole part mounted on the bottom asks the top without np.
		under := fg.free(LayerTop, ix0, iy0, kw, kh, true, false) && fg.free(LayerBottom, ix0, iy0, kw, kh, true, true)
		if under == cu {
			t.Fatalf("noCopper %v: room under the keep-out for a bottom part: %v", cu, under)
		}
	}
}
