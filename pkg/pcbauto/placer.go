package pcbauto

import (
	"math"
	"math/rand"
	"sort"
	"strings"
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
	b        *Board
	an       *Analysis
	c        *Circuit
	m        *Mechanics
	opt      PlaceOptions
	rng      *rand.Rand
	movable  []*Part
	nets     []*pnet
	partNet  map[*Part][]int
	zoneOf   map[*Part]Rect     // allowed centre region
	region   Rect               // placement region (board inset)
	decap    map[*Part]*Pad     // decap → the core power pad it serves
	servedBy map[string][]*Part // core ref → its decaps, in board order
	tether   map[*Part]*tether  // auxiliary → the core pads it serves, by role
	hardOnly bool               // partCost: constraint terms only (legalisation)
	apart    [][2]*Part         // core pairs to keep apart (noisy vs sensitive)
	spacing  float64
	bucket   map[[2]int][]*Part
	boxes    map[*Part]Rect
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
	pl.tidy()
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
	pl.setupTethers()
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
	if !pl.hardOnly {
		cost += pl.tetherCost(p)
		for _, pr := range pl.apart {
			if pr[0] == p || pr[1] == p {
				d := pr[0].Body().Center().Dist(pr[1].Body().Center())
				cost += 3 * math.Max(0, keepApartMil-d)
			}
		}
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
		for _, pr := range pl.apart {
			a, bb := byID["B-"+pr[0].Ref], byID["B-"+pr[1].Ref]
			if a == nil || bb == nil {
				continue
			}
			d := a.pos.Sub(bb.pos)
			dist := math.Hypot(d.X, d.Y) + 1e-6
			need := (a.size+bb.size)/2 + keepApartMil
			if dist < need {
				push := d.Scale((need - dist) / dist * 0.3)
				force[a] = force[a].Add(push)
				force[bb] = force[bb].Add(push.Scale(-1))
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
	// Core orientation: turn each free core so the pins of every link face
	// the block on the other end (a connector's pins, a regulator's output).
	anchor := map[string]Point{}
	for _, k := range bs {
		anchor[k.bl.ID] = k.pos
	}
	for _, k := range bs {
		if k.core != nil && !k.core.Fixed && !pl.opt.NoRotate {
			pl.orientCore(k.core, k.bl.ID, anchor)
		}
	}
	// Satellites in role order: the tightest loops claim the pin-side
	// slots first, looser relations stack behind them.
	for _, k := range bs {
		members := append([]Member(nil), k.bl.Members...)
		// Within a role the smallest cap goes first: it serves the highest
		// band and so owns the slot nearest the pin.
		sort.SliceStable(members, func(i, j int) bool {
			ri, rj := roleRank(members[i].Role), roleRank(members[j].Role)
			if ri != rj {
				return ri < rj
			}
			return capRank(b.Part(members[i].Ref)) < capRank(b.Part(members[j].Ref))
		})
		placed := []*Part{}
		for _, m := range members {
			p := b.Part(m.Ref)
			if p == nil || p.Fixed || p == k.core {
				continue
			}
			pl.hug(p, k.core, placed)
			placed = append(placed, p)
		}
		for _, ref := range k.bl.Parts {
			p := b.Part(ref)
			if p == nil || p.Fixed || p == k.core || containsPart(placed, p) {
				continue
			}
			pl.hug(p, k.core, placed)
			placed = append(placed, p)
		}
	}
}

var roleOrder = []string{"decap", "clock", "clock-load", "power-stage", "protection", "pull", "signal", "chain", "test"}

func roleRank(r string) int {
	for i, x := range roleOrder {
		if x == r {
			return i
		}
	}
	return len(roleOrder)
}

func capRank(p *Part) float64 {
	if p == nil {
		return 0
	}
	f := CapFarads(p.Device)
	if f == 0 {
		return 100e-9
	}
	return f
}

func containsPart(ps []*Part, p *Part) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}

// orientCore picks the rotation whose pads sit closest to the blocks they
// link to (weighted by net count), so escape routes do not wrap the body.
func (pl *placer) orientCore(core *Part, id string, anchor map[string]Point) {
	type pull struct {
		net string
		at  Point
	}
	var pulls []pull
	for _, l := range pl.c.Links {
		other := ""
		switch id {
		case l.From:
			other = l.To
		case l.To:
			other = l.From
		default:
			continue
		}
		at, ok := anchor[other]
		if !ok {
			continue
		}
		for _, n := range l.Nets {
			pulls = append(pulls, pull{n, at})
		}
	}
	if len(pulls) == 0 {
		return
	}
	c := core.Body().Center()
	best, bestRot := math.Inf(1), core.Rotation
	for _, r := range []float64{0, 90, 180, 270} {
		movePartCentre(core, c, r)
		cost := 0.0
		for _, pu := range pulls {
			d := math.Inf(1)
			for _, pd := range core.Pads {
				if pd.Net == pu.net {
					d = math.Min(d, pd.Box.C.Dist(pu.at))
				}
			}
			if !math.IsInf(d, 1) {
				cost += d
			}
		}
		if cost < best-1e-6 {
			best, bestRot = cost, r
		}
	}
	movePartCentre(core, c, bestRot)
}

// hug puts a satellite next to the core pin it serves, on the core side that
// pin sits on, turned so the connecting pad faces the pin.
func (pl *placer) hug(p, core *Part, placed []*Part) {
	if core == nil {
		movePartCentre(p, pl.region.Center(), p.Rotation)
		return
	}
	var target *Pad
	if t := pl.tether[p]; t != nil && len(t.pads) > 0 {
		target = t.pads[0]
		for _, cp := range t.pads {
			if cp.Part == core.Ref {
				target = cp
				break
			}
		}
	}
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
	if !collide(p, core) {
		// Opposite side (a BGA's bottom-side decaps): straight under the
		// pin, spiralling out only past parts already on that side.
		pl.underPin(p, target, placed)
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
	// Slide along the edge, then step outward, until clear of the members
	// already hugging this core.
	along := Point{-out.Y, out.X}
	step := math.Max(math.Min(pb.W(), pb.H()), 20) + pl.spacing
	depth := math.Max(pb.W(), pb.H()) + pl.spacing
	free := func(at Point) bool {
		movePartCentre(p, at, rot)
		box := pl.box(p)
		for _, q := range placed {
			if collide(p, q) && box.OverlapArea(pl.box(q)) > 0 {
				return false
			}
		}
		return box.OverlapArea(pl.box(core)) == 0
	}
	for row := 0; row < 6; row++ {
		for k := 0; k < 12; k++ {
			off := float64((k+1)/2) * step
			if k%2 == 1 {
				off = -off
			}
			at := c.Add(along.Scale(off)).Add(out.Scale(float64(row) * depth))
			if free(at) {
				return
			}
		}
	}
	movePartCentre(p, c, rot)
}

// underPin centres p on target and walks a square spiral until it clears
// every already-placed part on its own side.
func (pl *placer) underPin(p *Part, target *Pad, placed []*Part) {
	rot := p.Rotation
	step := math.Max(math.Min(p.Body().W(), p.Body().H()), 20)/2 + pl.spacing/2
	for ring := 0; ring < 24; ring++ {
		for k := 0; k < max(1, 8*ring); k++ {
			var off Point
			if ring > 0 {
				side, pos := k/(2*ring), float64(k%(2*ring)-ring)
				r := float64(ring)
				off = [4]Point{{pos, -r}, {r, pos}, {-pos, r}, {-r, -pos}}[side]
			}
			movePartCentre(p, target.Box.C.Add(off.Scale(step)), rot)
			box := pl.box(p)
			ok := true
			for _, q := range placed {
				if collide(p, q) && box.OverlapArea(pl.box(q)) > 0 {
					ok = false
					break
				}
			}
			if ok {
				return
			}
		}
	}
	movePartCentre(p, target.Box.C, rot)
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
	pl.hardOnly = true
	c := pl.partCost(p)
	pl.hardOnly = false
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
	// Progress is the larger of moves done and time spent, so a run that
	// hits its deadline still ends cold (greedy) instead of mid-melt.
	start := time.Now()
	budget := deadline.Sub(start).Seconds()
	timeFrac := 0.0
	for i := 0; i < total; i++ {
		if i&0xff == 0 {
			if budget > 0 {
				timeFrac = time.Since(start).Seconds() / budget
			}
			if timeFrac >= 1 {
				break
			}
		}
		frac := math.Max(float64(i)/float64(total), timeFrac)
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
	grp []posePart // block move: the satellites that followed p
}

type posePart struct {
	p   *Part
	pos Point
	rot float64
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
	var grp []*Part
	if kind == 7 && len(pl.servedBy[p.Ref]) > 0 {
		// Block move: the core carries its auxiliaries rigidly.
		for _, d := range pl.servedBy[p.Ref] {
			if !d.Fixed {
				grp = append(grp, d)
				lastMove.grp = append(lastMove.grp, posePart{d, d.Pos, d.Rotation})
			}
		}
	} else if kind == 7 {
		kind = 0
	}
	before := pl.localCost(p, q)
	for _, d := range grp {
		before += pl.groupCost(d)
	}
	switch {
	case kind == 7:
		delta := Point{(pl.rng.Float64()*2 - 1) * radius, (pl.rng.Float64()*2 - 1) * radius}
		for _, d := range append([]*Part{p}, grp...) {
			pl.bucketOp(d, false)
			d.MoveTo(d.Pos.Add(delta), d.Rotation)
			pl.boxes[d] = pl.box(d)
			pl.bucketOp(d, true)
		}
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
	for _, d := range grp {
		after += pl.groupCost(d)
	}
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
	for _, g := range s.grp {
		pl.bucketOp(g.p, false)
		g.p.MoveTo(g.pos, g.rot)
		pl.boxes[g.p] = pl.box(g.p)
		pl.bucketOp(g.p, true)
	}
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

// groupCost is a follower's share of a block move: its nets other than the
// ones it shares with the core (those are already in localCost).
func (pl *placer) groupCost(d *Part) float64 {
	nets := append([]int(nil), pl.partNet[d]...)
	sort.Ints(nets)
	cost := 0.0
	for k, i := range nets {
		if k == 0 || i != nets[k-1] {
			cost += pl.netCost(i)
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

// WeightedWirelength is the placer's objective net term (mil) for the board as
// it stands: Σ weight·HPWL over the nets the placer optimises (ground excluded,
// rails down-weighted, clock/diff/RF/switch up-weighted). Benchmarks use it to
// compare a human placement with an engine placement on equal terms.
func WeightedWirelength(b *Board, an *Analysis, c *Circuit) float64 {
	pl := &placer{b: b, an: an, c: c, m: &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}},
		opt: PlaceOptions{SpacingMil: 12}, partNet: map[*Part][]int{}, zoneOf: map[*Part]Rect{}, decap: map[*Part]*Pad{}, spacing: 12}
	pl.setup(&PlaceResult{})
	return pl.wirelength()
}

// DecapDistance returns the mean distance (mil) from every decoupling cap's
// rail pad to the nearest pin of an IC/module on the same rail — the same
// yardstick layout-score's protection dimension uses — and the cap count.
func DecapDistance(b *Board, an *Analysis, c *Circuit) (float64, int) {
	railPins := map[string][]*Pad{}
	for _, p := range b.Parts {
		if k := c.Kinds[p.Ref]; k != KindIC && k != KindModule {
			continue
		}
		for _, pd := range p.Pads {
			if pd.Net != "" && an.Plan(pd.Net, b.Rules).Role == RolePower {
				railPins[pd.Net] = append(railPins[pd.Net], pd)
			}
		}
	}
	sum, n := 0.0, 0
	for _, p := range b.Parts {
		if !c.isDecap(b, an, p) {
			continue
		}
		rail := c.decapRail(b, an, p)
		best := math.Inf(1)
		for _, pd := range p.Pads {
			if pd.Net != rail {
				continue
			}
			for _, q := range railPins[rail] {
				best = math.Min(best, pd.Box.C.Dist(q.Box.C))
			}
		}
		if math.IsInf(best, 1) {
			continue
		}
		sum += best
		n++
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}

// symmetricPassive reports two-pad parts whose 180° turn is electrically
// neutral for tidiness purposes (R, C, L, FB — not polarised caps/diodes).
func symmetricPassive(p *Part) bool {
	if len(p.Pads) != 2 {
		return false
	}
	ref := upper(p.Ref)
	prefix := strings.TrimRightFunc(ref, func(r rune) bool { return r >= '0' && r <= '9' })
	switch prefix {
	case "R", "C", "L", "FB":
		dev := upper(p.Device)
		return !strings.Contains(dev, "TANT") && !strings.Contains(dev, "ELEC")
	}
	return false
}

// tidy makes the placement look designed: quarter-turns of symmetric
// passives folded to 0°/90°, each designator group turned to its majority
// orientation, and every anchor snapped to the 5 mil grid. Each change is
// kept only when it adds no overlap/zone/keepout violation and costs at most
// a little wirelength.
func (pl *placer) tidy() {
	const grid = 5.0
	pl.rebuildBuckets()
	try := func(p *Part, pos Point, rot float64, slack float64) bool {
		before := pl.localCost(p, nil)
		oldPos, oldRot := p.Pos, p.Rotation
		pl.bucketOp(p, false)
		p.MoveTo(pos, rot)
		pl.boxes[p] = pl.box(p)
		pl.bucketOp(p, true)
		if pl.hardCost(p) <= 1e-6 && pl.localCost(p, nil) <= before*(1+slack)+1 {
			return true
		}
		pl.bucketOp(p, false)
		p.MoveTo(oldPos, oldRot)
		pl.boxes[p] = pl.box(p)
		pl.bucketOp(p, true)
		return false
	}
	// Orientation: fold 180/270 onto 0/90, then align each group's majority.
	groups := map[string][]*Part{}
	for _, p := range pl.movable {
		if !symmetricPassive(p) {
			continue
		}
		if r := normDeg(p.Rotation); r >= 180 {
			try(p, p.Pos.Add(centreOffset(p)).Sub(pl.offsetAt(p, r-180)), r-180, 0.02)
		}
		prefix := strings.TrimRightFunc(upper(p.Ref), func(r rune) bool { return r >= '0' && r <= '9' })
		groups[prefix] = append(groups[prefix], p)
	}
	names := make([]string, 0, len(groups))
	for k := range groups {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		count := map[float64]int{}
		for _, p := range groups[k] {
			count[math.Mod(normDeg(p.Rotation), 180)]++
		}
		major := 0.0
		if count[90] > count[0] {
			major = 90
		}
		for _, p := range groups[k] {
			if math.Mod(normDeg(p.Rotation), 180) != major {
				try(p, p.Pos.Add(centreOffset(p)).Sub(pl.offsetAt(p, major)), major, 0.05)
			}
		}
	}
	// Grid: nearest 5 mil point first, then the other three corners of the cell.
	for _, p := range pl.movable {
		fx, fy := math.Floor(p.Pos.X/grid)*grid, math.Floor(p.Pos.Y/grid)*grid
		cands := []Point{{math.Round(p.Pos.X/grid) * grid, math.Round(p.Pos.Y/grid) * grid},
			{fx, fy}, {fx + grid, fy}, {fx, fy + grid}, {fx + grid, fy + grid}}
		for _, c := range cands {
			if math.Abs(c.X-p.Pos.X) < 1e-6 && math.Abs(c.Y-p.Pos.Y) < 1e-6 {
				break
			}
			if try(p, c, p.Rotation, 0.02) {
				break
			}
		}
	}
}

// offsetAt is the body-centre offset from the anchor at a given rotation.
func (pl *placer) offsetAt(p *Part, rot float64) Point {
	pos, r := p.Pos, p.Rotation
	p.MoveTo(pos, rot)
	off := centreOffset(p)
	p.MoveTo(pos, r)
	return off
}

// keepApartMil is the minimum centre distance between a switching power block
// and an analog/RF block (switch-node noise couples over short distances).
const keepApartMil = 400

// tether ties an auxiliary to the core pads it serves.
type tether struct {
	pads  []*Pad
	w     float64 // mil-cost per mil beyond slack
	slack float64 // free distance, mil
	role  string
}

// Role weights: how tightly each kind of auxiliary must hug its pin.
var tetherRoles = map[string][2]float64{ // weight, slack (mil)
	"decap":       {6, 30},
	"clock":       {6, 40},
	"clock-load":  {5, 50},
	"power-stage": {5, 60},
	"protection":  {5, 100},
	"pull":        {2, 120},
	"signal":      {1.5, 160},
	"chain":       {0.8, 220},
	"test":        {0.6, 300},
}

// setupTethers turns the circuit's core/auxiliary members into placement
// forces, remembers which auxiliaries follow each core, and records core
// pairs that must keep their distance.
func (pl *placer) setupTethers() {
	b, c := pl.b, pl.c
	pl.tether = map[*Part]*tether{}
	pl.servedBy = map[string][]*Part{}
	padByKey := map[string]*Pad{}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			padByKey[pd.Key()] = pd
		}
	}
	movable := map[*Part]bool{}
	for _, p := range pl.movable {
		movable[p] = true
	}
	for _, bl := range c.Blocks {
		core := b.Part(bl.Core)
		for _, m := range bl.Members {
			p := b.Part(m.Ref)
			rw, ok := tetherRoles[m.Role]
			if p == nil || !ok || !movable[p] {
				continue
			}
			t := &tether{w: rw[0], slack: rw[1], role: m.Role}
			if m.Role == "decap" {
				_, t.w, t.slack = decapClass(p.Device)
			}
			if pd := padByKey[m.Pin]; pd != nil {
				t.pads = []*Pad{pd}
			} else if core != nil {
				// No single pin (clock-load, chain): the core pads on shared nets,
				// or failing that the pads of whatever part it shares a net with.
				shared := map[string]bool{}
				for _, pd := range p.Pads {
					if pd.Net != "" && pl.an.Plan(pd.Net, b.Rules).Role != RoleGround {
						shared[pd.Net] = true
					}
				}
				for _, q := range b.Parts {
					if q == p || c.BlockOf[q.Ref] != bl.ID {
						continue
					}
					for _, qd := range q.Pads {
						if shared[qd.Net] {
							t.pads = append(t.pads, qd)
						}
					}
				}
			}
			if len(t.pads) == 0 {
				continue
			}
			pl.tether[p] = t
			if m.Role == "decap" {
				pl.decap[p] = t.pads[0]
			}
			pl.servedBy[bl.Core] = append(pl.servedBy[bl.Core], p)
		}
	}
	// Noisy vs sensitive cores keep apart.
	noisy, quiet := []*Part{}, []*Part{}
	for _, bl := range c.Blocks {
		core := b.Part(bl.Core)
		if core == nil {
			continue
		}
		stage := false
		for _, m := range bl.Members {
			if m.Role == "power-stage" {
				stage = true
			}
		}
		switch {
		case stage || bl.Kind == "power":
			if stage {
				noisy = append(noisy, core)
			}
		case bl.Kind == "rf" || bl.Kind == "analog":
			quiet = append(quiet, core)
		}
	}
	for _, n := range noisy {
		for _, q := range quiet {
			if !n.Fixed || !q.Fixed {
				pl.apart = append(pl.apart, [2]*Part{n, q})
			}
		}
	}
}

// tetherCost is the soft pull of an auxiliary toward the pads it serves.
func (pl *placer) tetherCost(p *Part) float64 {
	t := pl.tether[p]
	if t == nil {
		return 0
	}
	best := math.Inf(1)
	for _, pd := range p.Pads {
		for _, cp := range t.pads {
			if pd.Net == cp.Net || t.role == "clock-load" || t.role == "chain" {
				best = math.Min(best, pd.Box.C.Dist(cp.Box.C))
			}
		}
	}
	if math.IsInf(best, 1) {
		// Pads not on a shared net (e.g. crystal body): use the body centre.
		for _, cp := range t.pads {
			best = math.Min(best, p.Body().Center().Dist(cp.Box.C))
		}
	}
	return t.w * math.Max(0, best-t.slack)
}
