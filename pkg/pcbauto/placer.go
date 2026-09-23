package pcbauto

import (
	"math"
	"math/rand"
	"sort"
	"time"
)

// PlaceOptions tune the placer.
type PlaceOptions struct {
	// Moves is the annealing budget per movable part (default 1500).
	Moves int `json:"moves"`
	// Seed makes runs reproducible.
	Seed int64 `json:"seed"`
	// SpacingMil is the courtyard gap between parts (default 12).
	SpacingMil float64 `json:"spacingMil"`
	// NoRotate keeps rotations.
	NoRotate bool `json:"noRotate"`
	// Refine starts from the current placement instead of constructing one.
	Refine  bool          `json:"refine"`
	Timeout time.Duration `json:"-"`
}

// Placement is a part's decided pose (anchor coordinates, like EasyEDA).
type Placement struct {
	Ref   string  `json:"ref"`
	ID    string  `json:"id,omitempty"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Rot   float64 `json:"rot"`
	Side  int     `json:"side"`
	Fixed bool    `json:"fixed,omitempty"`
	Block string  `json:"block,omitempty"`
}

// PlaceMetrics measure a placement.
type PlaceMetrics struct {
	WirelengthIn float64 `json:"weightedWirelengthIn"`
	StartWireIn  float64 `json:"startWirelengthIn"`
	Overlaps     int     `json:"overlaps"`
	OutOfBoard   int     `json:"outOfBoard"`
	OutOfZone    int     `json:"outOfZone"`
	KeepoutHits  int     `json:"keepoutHits"`
	HeightHits   int     `json:"heightHits"`
	DecapMeanMil float64 `json:"decapToPinMeanMil"`
	BoardAreaIn2 float64 `json:"boardAreaIn2"`
	PartAreaIn2  float64 `json:"partAreaIn2"`
	Utilisation  float64 `json:"utilisation"`
	Millis       int64   `json:"millis"`
}

// PlaceResult is the placer output.
type PlaceResult struct {
	Placements []Placement  `json:"placements"`
	Zones      []Zone       `json:"zones,omitempty"`
	Barriers   []Keepout    `json:"barrierKeepouts,omitempty"`
	Outline    []Point      `json:"outline,omitempty"` // set when auto-sized
	Metrics    PlaceMetrics `json:"metrics"`
	Notes      []string     `json:"notes,omitempty"`
}

type pnet struct {
	pads   []*Pad
	weight float64
}

type placer struct {
	b       *Board
	an      *Analysis
	c       *Circuit
	m       *Mechanics
	opt     PlaceOptions
	rng     *rand.Rand
	movable []*Part
	nets    []*pnet
	partNet map[*Part][]int
	zoneOf  map[*Part]Rect // allowed centre region
	region  Rect           // placement region (board inset)
	decap    map[*Part]*Pad    // decap → the core power pad it serves
	servedBy map[string][]*Part // core ref → its decaps, in board order
	spacing float64
	bucket  map[[2]int][]*Part
	boxes   map[*Part]Rect
}

const placeBucket = 150.0

// Place decides poses for every movable part.
func Place(b *Board, an *Analysis, c *Circuit, m *Mechanics, opt PlaceOptions) (*PlaceResult, error) {
	start := time.Now()
	if opt.Moves <= 0 {
		opt.Moves = 1500
	}
	if opt.SpacingMil <= 0 {
		opt.SpacingMil = 12
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 90 * time.Second
	}
	if m == nil {
		m = &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}}
	}
	pl := &placer{b: b, an: an, c: c, m: m, opt: opt, rng: rand.New(rand.NewSource(opt.Seed + 1)),
		partNet: map[*Part][]int{}, zoneOf: map[*Part]Rect{}, decap: map[*Part]*Pad{}, spacing: opt.SpacingMil}
	res := &PlaceResult{}
	pl.setup(res)
	res.Metrics.StartWireIn = pl.wirelength() / 1000
	if !opt.Refine {
		pl.construct()
	}
	pl.legalise()
	pl.anneal(start.Add(opt.Timeout))
	pl.legalise()
	if m.AutoSize {
		res.Outline = pl.autosize()
	}
	pl.metrics(res)
	for _, p := range b.Parts {
		res.Placements = append(res.Placements, Placement{Ref: p.Ref, ID: p.ID, X: round2(p.Pos.X), Y: round2(p.Pos.Y),
			Rot: p.Rotation, Side: p.Side, Fixed: p.Fixed, Block: c.BlockOf[p.Ref]})
	}
	res.Metrics.Millis = time.Since(start).Milliseconds()
	return res, nil
}

// ---- setup -------------------------------------------------------------------

func (pl *placer) setup(res *PlaceResult) {
	b := pl.b
	bb := b.Bounds()
	if pl.m.AutoSize {
		area := 0.0
		for _, p := range b.Parts {
			area += p.Body().Expand(pl.spacing).Area()
		}
		side := math.Sqrt(area * 2.2)
		if bb.Empty() || bb.W() < side {
			bb = Rect{0, 0, side * 1.3, side}
			b.Outline = bb.Corners()
		}
	}
	pl.region = bb.Expand(-b.Rules.EdgeClearance - pl.spacing)
	for _, p := range b.Parts {
		if !p.Fixed && pl.c.Kinds[p.Ref] != KindMechanical {
			pl.movable = append(pl.movable, p)
		}
	}
	// Nets with placement weights.
	idx := map[string]int{}
	for _, n := range b.Nets() {
		np := pl.an.Plan(n.Name, b.Rules)
		w := 1.0
		switch np.Role {
		case RoleGround:
			w = 0 // delivered by planes / pours + fan-out vias
		case RolePower:
			w = 0.15
		case RoleClock, RoleRF:
			w = 4
		case RoleDiff:
			w = 2.5
		case RoleSwitch:
			w = 3 // SMPS loop: keep tight
		case RoleAnalog:
			w = 1.5
		}
		if len(n.Pads) > 10 {
			w *= 10 / float64(len(n.Pads))
		}
		if w == 0 || len(n.Pads) < 2 {
			continue
		}
		idx[n.Name] = len(pl.nets)
		pl.nets = append(pl.nets, &pnet{pads: n.Pads, weight: w})
	}
	for _, p := range b.Parts {
		seen := map[int]bool{}
		for _, pd := range p.Pads {
			if i, ok := idx[pd.Net]; ok && !seen[i] {
				seen[i] = true
				pl.partNet[p] = append(pl.partNet[p], i)
			}
		}
	}
	// Decoupling: each decap is pulled to the nearest power pad of its core.
	for _, p := range pl.movable {
		if !pl.c.isDecap(b, pl.an, p) {
			continue
		}
		rail := pl.c.decapRail(b, pl.an, p)
		core := b.Part(coreOf(pl.c, p.Ref))
		if core == nil {
			continue
		}
		var pins []*Pad
		for _, pd := range core.Pads {
			if pd.Net == rail {
				pins = append(pins, pd)
			}
		}
		if len(pins) > 0 {
			// Spread decaps over the core's power pins round-robin.
			used := 0
			for q := range pl.decap {
				if coreOf(pl.c, q.Ref) == core.Ref {
					used++
				}
			}
			pl.decap[p] = pins[used%len(pins)]
		}
	}
	pl.servedBy = map[string][]*Part{}
	for _, p := range pl.movable {
		if pin := pl.decap[p]; pin != nil {
			pl.servedBy[pin.Part] = append(pl.servedBy[pin.Part], p)
		}
	}
	pl.zones(res)
}

func coreOf(c *Circuit, ref string) string {
	id := c.BlockOf[ref]
	for _, bl := range c.Blocks {
		if bl.ID == id {
			return bl.Core
		}
	}
	return ""
}

// zones splits the region between voltage domains with an isolation strip
// of the barrier's clearance, and assigns every movable part its zone.
func (pl *placer) zones(res *PlaceResult) {
	b, c := pl.b, pl.c
	userZone := map[string]Rect{}
	for _, z := range pl.m.Zones {
		if z.Domain != "" {
			userZone[z.Domain] = z.Rect
		}
		res.Zones = append(res.Zones, z)
	}
	doms := []*Domain{}
	for _, d := range c.Domains {
		if d.ID != "UNREFERENCED" {
			doms = append(doms, d)
		}
	}
	if len(doms) > 1 && len(userZone) == 0 {
		// Order domains left→right: the one whose fixed parts sit furthest
		// left goes first (mains connectors usually decide it).
		type dk struct {
			d    *Domain
			x    float64
			area float64
		}
		var ks []dk
		for _, d := range doms {
			k := dk{d: d, x: pl.region.Center().X}
			sum, n := 0.0, 0.0
			for _, ref := range d.Parts {
				p := b.Part(ref)
				if p == nil {
					continue
				}
				k.area += p.Body().Expand(pl.spacing).Area()
				if p.Fixed {
					sum += p.Body().Center().X
					n++
				}
			}
			if n > 0 {
				k.x = sum / n
			} else if d.Hazardous {
				k.x = pl.region.MinX
			}
			ks = append(ks, k)
		}
		sort.SliceStable(ks, func(i, j int) bool { return ks[i].x < ks[j].x })
		total := 0.0
		for _, k := range ks {
			total += k.area
		}
		x := pl.region.MinX
		for i, k := range ks {
			gap := 0.0
			if i+1 < len(ks) {
				gap = pl.barrierGap(k.d.ID, ks[i+1].d.ID)
			}
			w := (pl.region.W() - pl.gapTotal()) * k.area / total
			r := Rect{x, pl.region.MinY, x + w, pl.region.MaxY}
			userZone[k.d.ID] = r
			res.Zones = append(res.Zones, Zone{Domain: k.d.ID, Rect: r})
			if gap > 0 {
				strip := Rect{x + w, b.Bounds().MinY, x + w + gap, b.Bounds().MaxY}
				ko := Keepout{Name: "isolation " + k.d.ID + "|" + ks[i+1].d.ID, Poly: strip.Corners(), NoCopper: true, NoVias: true}
				b.Keepouts = append(b.Keepouts, &ko)
				res.Barriers = append(res.Barriers, ko)
				res.Notes = append(res.Notes, sprintf("isolation strip %.0f mil wide between %s and %s (no copper; bridges straddle it)", gap, k.d.ID, ks[i+1].d.ID))
			}
			x += w + gap
		}
	}
	for _, p := range pl.movable {
		d := c.DomainOf[p.Ref]
		if r, ok := userZone[d]; ok && !c.Kinds[p.Ref].Bridges() {
			pl.zoneOf[p] = r
			continue
		}
		pl.zoneOf[p] = pl.region
		if c.Kinds[p.Ref].Bridges() && len(res.Barriers) > 0 {
			// A bridge sits centred on its strip.
			s := PolyBounds(res.Barriers[0].Poly)
			pl.zoneOf[p] = Rect{s.Center().X - 1, pl.region.MinY, s.Center().X + 1, pl.region.MaxY}
		}
	}
}

func (pl *placer) gapTotal() float64 {
	total := 0.0
	for _, br := range pl.c.Barriers {
		total += br.CreepageMil
	}
	return total
}

func (pl *placer) barrierGap(a, b string) float64 {
	for _, br := range pl.c.Barriers {
		if br.A == a && br.B == b || br.A == b && br.B == a {
			// Surface distance governs on a board: the strip is the creepage.
			return br.CreepageMil
		}
	}
	return 0
}

// ---- cost ---------------------------------------------------------------------

func (pl *placer) netCost(i int) float64 {
	n := pl.nets[i]
	r := EmptyRect()
	for _, pd := range n.pads {
		r = r.AddPoint(pd.Box.C)
	}
	return n.weight * (r.W() + r.H())
}

func (pl *placer) wirelength() float64 {
	s := 0.0
	for i := range pl.nets {
		s += pl.netCost(i)
	}
	return s
}

func (pl *placer) box(p *Part) Rect { return p.Body().Expand(pl.spacing / 2) }

// partCost is the constraint cost of one part in its current pose,
// excluding net length (overlap with others, zone, board, keepouts, height).
func (pl *placer) partCost(p *Part) float64 {
	bx := pl.boxes[p]
	cost := 0.0
	// Overlap with neighbours (bucketed).
	seen := map[*Part]bool{}
	pl.forBuckets(bx, func(q *Part) {
		if q == p || seen[q] || !collide(p, q) {
			return
		}
		seen[q] = true
		cost += 8 * bx.OverlapArea(pl.boxes[q])
	})
	// Board / zone containment of the body.
	z := pl.zoneOf[p]
	if !p.Fixed {
		cost += 20 * outside(bx, pl.region)
		cen := bx.Center()
		dx := math.Max(0, math.Max(z.MinX-cen.X, cen.X-z.MaxX))
		dy := math.Max(0, math.Max(z.MinY-cen.Y, cen.Y-z.MaxY))
		cost += 30 * (dx + dy) * math.Max(bx.W(), bx.H())
		// Zone body containment for non-bridges keeps HV parts wholly in zone.
		if !pl.c.Kinds[p.Ref].Bridges() && z != pl.region {
			cost += 20 * outside(bx, z)
		}
	}
	for _, k := range pl.b.Keepouts {
		if k.NoParts || k.NoCopper {
			kb := PolyBounds(k.Poly)
			if pl.c.Kinds[p.Ref].Bridges() && !k.NoParts {
				continue // bridges straddle isolation strips by design
			}
			cost += 20 * bx.OverlapArea(kb)
		}
	}
	for _, h := range pl.b.Holes {
		hr := Rect{h.C.X, h.C.Y, h.C.X, h.C.Y}.Expand(h.Dia/2 + h.Keep)
		cost += 20 * bx.OverlapArea(hr)
	}
	for _, hz := range pl.m.HeightZones {
		if p.Height > hz.MaxHeight {
			cost += 20 * bx.OverlapArea(hz.Rect)
		}
	}
	if pin := pl.decap[p]; pin != nil {
		// Decap: the power pad of the cap to the IC pin, short loop.
		best := math.Inf(1)
		for _, pd := range p.Pads {
			if pd.Net == pin.Net {
				best = math.Min(best, pd.Box.C.Dist(pin.Box.C))
			}
		}
		cost += 6 * best
	}
	return cost
}

// outside is the body area outside r.
func outside(bx, r Rect) float64 {
	return bx.Area() - bx.OverlapArea(r)
}

func (pl *placer) forBuckets(r Rect, fn func(*Part)) {
	x0, y0 := int(math.Floor(r.MinX/placeBucket)), int(math.Floor(r.MinY/placeBucket))
	x1, y1 := int(math.Floor(r.MaxX/placeBucket)), int(math.Floor(r.MaxY/placeBucket))
	for x := x0; x <= x1; x++ {
		for y := y0; y <= y1; y++ {
			for _, q := range pl.bucket[[2]int{x, y}] {
				fn(q)
			}
		}
	}
}

func (pl *placer) bucketOp(p *Part, add bool) {
	r := pl.boxes[p]
	x0, y0 := int(math.Floor(r.MinX/placeBucket)), int(math.Floor(r.MinY/placeBucket))
	x1, y1 := int(math.Floor(r.MaxX/placeBucket)), int(math.Floor(r.MaxY/placeBucket))
	for x := x0; x <= x1; x++ {
		for y := y0; y <= y1; y++ {
			k := [2]int{x, y}
			if add {
				pl.bucket[k] = append(pl.bucket[k], p)
				continue
			}
			l := pl.bucket[k]
			for i, q := range l {
				if q == p {
					l[i] = l[len(l)-1]
					pl.bucket[k] = l[:len(l)-1]
					break
				}
			}
		}
	}
}

func (pl *placer) rebuildBuckets() {
	pl.bucket = map[[2]int][]*Part{}
	pl.boxes = map[*Part]Rect{}
	for _, p := range pl.b.Parts {
		pl.boxes[p] = pl.box(p)
		pl.bucketOp(p, true)
	}
}

// ---- construction -----------------------------------------------------------

// construct places blocks by force-directed layout, then satellites around
// their core pins.
func (pl *placer) construct() {
	b, c := pl.b, pl.c
	type blk struct {
		bl   *Block
		core *Part
		pos  Point
		size float64
		zone Rect
		fix  bool
	}
	var bs []*blk
	byID := map[string]*blk{}
	for _, bl := range c.Blocks {
		core := b.Part(bl.Core)
		k := &blk{bl: bl, core: core}
		area := 0.0
		for _, ref := range bl.Parts {
			if p := b.Part(ref); p != nil {
				area += p.Body().Expand(pl.spacing).Area()
			}
		}
		k.size = math.Sqrt(area * 1.6)
		k.zone = pl.region
		if core != nil {
			k.zone = pl.zoneOf[core]
			if core.Fixed {
				k.fix = true
				k.zone = pl.region
			}
			k.pos = core.Body().Center()
		}
		bs = append(bs, k)
		byID[bl.ID] = k
	}
	// Seed free blocks on a grid inside their zone.
	for i, k := range bs {
		if k.fix {
			continue
		}
		z := k.zone
		cols := int(math.Ceil(math.Sqrt(float64(len(bs)))))
		cx, cy := i%cols, i/cols
		k.pos = Point{z.MinX + (float64(cx)+0.5)*z.W()/float64(cols), z.MinY + (float64(cy)+0.5)*z.H()/float64(cols)}
	}
	// Force-directed: springs along links, repulsion between overlapping
	// blocks, connectors (fixed) act as anchors.
	for it := 0; it < 300; it++ {
		step := 0.3 * (1 - float64(it)/300)
		force := map[*blk]Point{}
		for _, l := range c.Links {
			a, bb := byID[l.From], byID[l.To]
			if a == nil || bb == nil {
				continue
			}
			w := math.Min(float64(len(l.Nets)), 12)
			for _, kind := range l.Kinds {
				if kind == "clock" || kind == "usb" || kind == "diff" || kind == "rf" {
					w *= 2
				}
			}
			d := bb.pos.Sub(a.pos)
			force[a] = force[a].Add(d.Scale(0.02 * w))
			force[bb] = force[bb].Add(d.Scale(-0.02 * w))
		}
		for i, a := range bs {
			for _, bb := range bs[i+1:] {
				d := a.pos.Sub(bb.pos)
				dist := math.Hypot(d.X, d.Y) + 1e-6
				need := (a.size + bb.size) / 2
				if dist < need {
					push := d.Scale((need - dist) / dist * 0.5)
					force[a] = force[a].Add(push)
					force[bb] = force[bb].Add(push.Scale(-1))
				}
			}
		}
		for _, k := range bs {
			if k.fix {
				continue
			}
			k.pos = k.pos.Add(force[k].Scale(step))
			h := k.size / 2
			k.pos.X = clamp(k.pos.X, k.zone.MinX+h, math.Max(k.zone.MinX+h, k.zone.MaxX-h))
			k.pos.Y = clamp(k.pos.Y, k.zone.MinY+h, math.Max(k.zone.MinY+h, k.zone.MaxY-h))
		}
	}
	// Cores at block positions, satellites at their pins.
	for _, k := range bs {
		if k.core == nil || k.core.Fixed {
			continue
		}
		movePartCentre(k.core, k.pos, k.core.Rotation)
	}
	for _, k := range bs {
		for _, ref := range k.bl.Parts {
			p := b.Part(ref)
			if p == nil || p.Fixed || p == k.core {
				continue
			}
			pl.hug(p, k.core)
		}
	}
}

// hug puts a satellite next to the core pin it serves, on the core side that
// pin sits on, turned so the connecting pad faces the pin.
func (pl *placer) hug(p, core *Part) {
	if core == nil {
		movePartCentre(p, pl.region.Center(), p.Rotation)
		return
	}
	target := pl.decap[p]
	if target == nil {
		for _, pd := range p.Pads {
			for _, cp := range core.Pads {
				if pd.Net != "" && cp.Net == pd.Net && !isGlobalNet(pl.an, pd.Net, pl.b.Rules) {
					target = cp
				}
			}
		}
	}
	cb := core.Body()
	cc := cb.Center()
	if target == nil {
		movePartCentre(p, Point{cb.MaxX + p.Body().W(), cc.Y}, p.Rotation)
		return
	}
	d := target.Box.C.Sub(cc)
	var out Point
	if math.Abs(d.X)/math.Max(cb.W(), 1) > math.Abs(d.Y)/math.Max(cb.H(), 1) {
		out = Point{math.Copysign(1, d.X), 0}
	} else {
		out = Point{0, math.Copysign(1, d.Y)}
	}
	// Orient: 2-pad parts along the outward normal so one pad faces the pin.
	rot := p.Rotation
	if len(p.Pads) == 2 && !pl.opt.NoRotate {
		best := math.Inf(1)
		for _, r := range []float64{0, 90, 180, 270} {
			p.MoveTo(p.Pos, r)
			v := p.Pads[1].Box.C.Sub(p.Pads[0].Box.C)
			// The connecting pad should be the one nearer the core.
			conn := 0
			if p.Pads[1].Net == target.Net {
				conn = 1
			}
			if conn == 1 {
				v = v.Scale(-1)
			}
			score := v.X*out.X + v.Y*out.Y // want the connecting pad on the inside
			if -score < best {
				best, rot = -score, r
			}
		}
	}
	p.MoveTo(p.Pos, rot)
	pb := p.Body()
	var c Point
	if out.X != 0 {
		edge := cb.MaxX
		if out.X < 0 {
			edge = cb.MinX
		}
		c = Point{edge + out.X*(pb.W()/2+pl.spacing), target.Box.C.Y}
	} else {
		edge := cb.MaxY
		if out.Y < 0 {
			edge = cb.MinY
		}
		c = Point{target.Box.C.X, edge + out.Y*(pb.H()/2+pl.spacing)}
	}
	movePartCentre(p, c, rot)
}

// ---- legalisation -----------------------------------------------------------

// legalise removes overlaps: parts in overlap order are moved to the nearest
// free spot on a spiral (inside their zone), fixed parts never move.
func (pl *placer) legalise() {
	pl.rebuildBuckets()
	order := append([]*Part(nil), pl.movable...)
	sort.SliceStable(order, func(i, j int) bool { return order[i].Body().Area() > order[j].Body().Area() })
	for pass := 0; pass < 3; pass++ {
		moved := false
		for _, p := range order {
			if pl.hardCost(p) <= 1e-6 {
				continue
			}
			if pl.spiral(p) {
				moved = true
			}
		}
		if !moved {
			break
		}
	}
}

// hardCost counts only the constraint violations (not decap/length terms).
func (pl *placer) hardCost(p *Part) float64 {
	saved := pl.decap[p]
	delete(pl.decap, p)
	c := pl.partCost(p)
	if saved != nil {
		pl.decap[p] = saved
	}
	return c
}

func (pl *placer) spiral(p *Part) bool {
	orig, rot0 := p.Body().Center(), p.Rotation
	best, bestRot, bestCost := orig, rot0, pl.hardCost(p)
	step := math.Max(pl.spacing, 10)
	pl.bucketOp(p, false)
	// Current orientation first; the other three only if it finds no
	// violation-free spot (a long part may only fit turned).
	rots := []float64{rot0}
	if !pl.opt.NoRotate {
		rots = append(rots, normDeg(rot0+90), normDeg(rot0+180), normDeg(rot0+270))
	}
	for _, rot := range rots {
		if bestCost <= 1e-6 {
			break
		}
		for ring := 0; ring < 400 && bestCost > 1e-6; ring++ {
			r := float64(ring) * step
			n := max(8*ring, 1)
			for k := 0; k < n; k++ {
				a := 2 * math.Pi * float64(k) / float64(n)
				c := orig.Add(Point{r * math.Cos(a), r * math.Sin(a)})
				movePartCentre(p, c, rot)
				pl.boxes[p] = pl.box(p)
				if cost := pl.hardCost(p); cost < bestCost-1e-6 {
					best, bestRot, bestCost = c, rot, cost
					if cost <= 1e-6 {
						break
					}
				}
			}
		}
	}
	movePartCentre(p, best, bestRot)
	pl.boxes[p] = pl.box(p)
	pl.bucketOp(p, true)
	return best != orig || bestRot != rot0
}

// ---- annealing ----------------------------------------------------------------

func (pl *placer) anneal(deadline time.Time) {
	if len(pl.movable) == 0 {
		return
	}
	pl.rebuildBuckets()
	total := pl.opt.Moves * len(pl.movable)
	// Initial temperature from the median move delta (the mean is dominated
	// by rare huge overlap penalties and would keep the end state hot).
	var ds []float64
	for i := 0; i < 80; i++ {
		p := pl.movable[pl.rng.Intn(len(pl.movable))]
		if d := math.Abs(pl.tryMove(p, pl.region.W()/10, false)); d > 0 {
			ds = append(ds, d)
		}
	}
	sort.Float64s(ds)
	temp := 1.0
	if len(ds) > 0 {
		temp = ds[len(ds)/2]
	}
	radius := math.Max(pl.region.W(), pl.region.H()) / 3
	minR := pl.spacing
	for i := 0; i < total; i++ {
		if i&0x3ff == 0 && time.Now().After(deadline) {
			break
		}
		frac := float64(i) / float64(total)
		// Cool geometrically; the last 15% is greedy descent.
		t := temp * math.Pow(0.001, frac/0.85)
		r := math.Max(minR, radius*math.Pow(0.02, frac))
		p := pl.movable[pl.rng.Intn(len(pl.movable))]
		d := pl.tryMove(p, r, true)
		if d > 0 && (frac > 0.85 || pl.rng.Float64() >= math.Exp(-d/t)) {
			pl.undo(p)
		}
	}
}

type savedPose struct {
	pos Point
	rot float64
	q   *Part
	qp  Point
	qr  float64
}

var lastMove savedPose

// tryMove perturbs p (shift, rotate or swap) and returns the cost delta.
// With keep=false the move is undone immediately.
func (pl *placer) tryMove(p *Part, radius float64, keep bool) float64 {
	lastMove = savedPose{pos: p.Pos, rot: p.Rotation}
	var q *Part
	kind := pl.rng.Intn(10)
	if kind == 9 {
		// Swap with a similar-size movable part in the same zone.
		q = pl.movable[pl.rng.Intn(len(pl.movable))]
		if q == p || math.Abs(q.Body().Area()-p.Body().Area()) > 0.3*p.Body().Area() || pl.zoneOf[q] != pl.zoneOf[p] {
			q = nil
			kind = 0
		}
	}
	before := pl.localCost(p, q)
	switch {
	case kind == 9:
		lastMove.q, lastMove.qp, lastMove.qr = q, q.Pos, q.Rotation
		pc, qc := p.Body().Center(), q.Body().Center()
		pl.bucketOp(p, false)
		pl.bucketOp(q, false)
		movePartCentre(p, qc, p.Rotation)
		movePartCentre(q, pc, q.Rotation)
		pl.boxes[p], pl.boxes[q] = pl.box(p), pl.box(q)
		pl.bucketOp(p, true)
		pl.bucketOp(q, true)
	case kind == 8 && !pl.opt.NoRotate:
		pl.bucketOp(p, false)
		movePartCentre(p, p.Body().Center(), normDeg(p.Rotation+90*float64(1+pl.rng.Intn(3))))
		pl.boxes[p] = pl.box(p)
		pl.bucketOp(p, true)
	default:
		c := p.Body().Center().Add(Point{(pl.rng.Float64()*2 - 1) * radius, (pl.rng.Float64()*2 - 1) * radius})
		pl.bucketOp(p, false)
		movePartCentre(p, c, p.Rotation)
		pl.boxes[p] = pl.box(p)
		pl.bucketOp(p, true)
	}
	after := pl.localCost(p, q)
	if !keep {
		pl.undo(p)
	}
	return after - before
}

func (pl *placer) undo(p *Part) {
	s := lastMove
	pl.bucketOp(p, false)
	p.MoveTo(s.pos, s.rot)
	pl.boxes[p] = pl.box(p)
	pl.bucketOp(p, true)
	if s.q != nil {
		pl.bucketOp(s.q, false)
		s.q.MoveTo(s.qp, s.qr)
		pl.boxes[s.q] = pl.box(s.q)
		pl.bucketOp(s.q, true)
	}
}

// localCost is the cost affected by moving p (and q): their nets plus their
// constraint terms, with pairwise overlap counted from both sides.
func (pl *placer) localCost(p, q *Part) float64 {
	// Sum in a fixed order: float addition is not associative, and map order
	// would make accept/reject decisions (and so whole runs) irreproducible.
	nets := append([]int(nil), pl.partNet[p]...)
	cost := pl.partCost(p)
	if q != nil {
		nets = append(nets, pl.partNet[q]...)
		cost += pl.partCost(q)
	}
	sort.Ints(nets)
	for k, i := range nets {
		if k == 0 || i != nets[k-1] {
			cost += pl.netCost(i)
		}
	}
	// Decaps that serve p's (or q's) pins move with those pins.
	for _, d := range pl.servedBy[p.Ref] {
		cost += pl.partCost(d)
	}
	if q != nil {
		for _, d := range pl.servedBy[q.Ref] {
			cost += pl.partCost(d)
		}
	}
	return cost
}

// ---- finishing ----------------------------------------------------------------

// autosize shrinks the outline to the placement plus margin.
func (pl *placer) autosize() []Point {
	r := EmptyRect()
	for _, p := range pl.b.Parts {
		r = r.Union(p.Body())
	}
	r = r.Expand(pl.m.MarginMil + pl.b.Rules.EdgeClearance)
	pl.b.Outline = RoundedRect(r, math.Min(80, r.W()/10))
	return pl.b.Outline
}

func (pl *placer) metrics(res *PlaceResult) {
	pl.rebuildBuckets()
	m := &res.Metrics
	m.WirelengthIn = pl.wirelength() / 1000
	parts := pl.b.Parts
	for i, p := range parts {
		bi := p.Body()
		m.PartAreaIn2 += bi.Area() / 1e6
		for _, q := range parts[i+1:] {
			if collide(p, q) && bi.OverlapArea(q.Body()) > 1 {
				m.Overlaps++
			}
		}
		if len(pl.b.Outline) >= 3 && !p.Fixed {
			for _, c := range bi.Corners() {
				if !PolyContains(pl.b.Outline, c) {
					m.OutOfBoard++
					break
				}
			}
		}
		if z, ok := pl.zoneOf[p]; ok && z != pl.region && !pl.c.Kinds[p.Ref].Bridges() && outside(bi, z) > 1 {
			m.OutOfZone++
		}
		for _, k := range pl.b.Keepouts {
			if k.NoParts && bi.OverlapArea(PolyBounds(k.Poly)) > 1 {
				m.KeepoutHits++
			}
		}
	}
	n, sum := 0, 0.0
	for _, d := range pl.movable {
		pin := pl.decap[d]
		if pin == nil {
			continue
		}
		best := math.Inf(1)
		for _, pd := range d.Pads {
			if pd.Net == pin.Net {
				best = math.Min(best, pd.Box.C.Dist(pin.Box.C))
			}
		}
		sum += best
		n++
	}
	if n > 0 {
		m.DecapMeanMil = math.Round(sum / float64(n))
	}
	m.BoardAreaIn2 = pl.b.Area() / 1e6
	if m.BoardAreaIn2 > 0 {
		m.Utilisation = math.Round(m.PartAreaIn2/m.BoardAreaIn2*1000) / 10
	}
}

// collide reports whether two parts compete for the same board surface:
// same side, or either has through-hole leads (present on both sides).
func collide(p, q *Part) bool {
	return p.Side == q.Side || hasTHT(p) || hasTHT(q)
}

func hasTHT(p *Part) bool {
	for _, pd := range p.Pads {
		if pd.Layer == LayerMulti {
			return true
		}
	}
	return false
}
