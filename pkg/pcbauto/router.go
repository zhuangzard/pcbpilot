package pcbauto

import (
	"context"
	"math"
	"os"
	"sort"
	"time"
)

// Track is a routed copper segment.
type Track struct {
	Net   string  `json:"net"`
	Layer int     `json:"layer"`
	A     Point   `json:"a"`
	B     Point   `json:"b"`
	Width float64 `json:"width"`
	Kind  string  `json:"kind,omitempty"` // route | fanout | stub
}

// Via is a through via.
type Via struct {
	Net   string  `json:"net"`
	C     Point   `json:"c"`
	Drill float64 `json:"drill"`
	Dia   float64 `json:"dia"`
	Kind  string  `json:"kind,omitempty"` // route | fanout
}

// Unrouted describes a connection the router could not complete.
type Unrouted struct {
	Net    string   `json:"net"`
	Pads   []string `json:"pads"`
	Reason string   `json:"reason"`
}

// RouteOptions tune the router. Zero values pick defaults.
type RouteOptions struct {
	GridMil      float64       `json:"gridMil"`
	MaxIters     int           `json:"maxIters"`
	ViaCostMil   float64       `json:"viaCostMil"`
	WrongDirCost float64       `json:"wrongDirCost"` // multiplier for off-preferred moves
	Timeout      time.Duration `json:"-"`
	// Nets limits routing to these nets (others stay as obstacles).
	Nets []string `json:"nets,omitempty"`
	// NoFanout skips plane fan-out (e.g. when planes are handled elsewhere).
	NoFanout bool `json:"noFanout,omitempty"`
	// BGA enables the BGA dog-bone fan-out. Off by default until it beats the
	// generic fan-out on the real BGA boards (2026-09-23: K230 50.2 → 48.5 %,
	// RK3568 41.2 → 39.8 %; most balls had no legal site with the board via).
	BGA bool `json:"bga,omitempty"`
	// NoRepair skips the exact-DRC repair loop (diagnostics only).
	NoRepair bool `json:"noRepair,omitempty"`
}

// RouteStats summarises a routing run.
type RouteStats struct {
	Nets                int     `json:"nets"`
	Connections         int     `json:"connections"`
	Routed              int     `json:"routed"`
	Unrouted            int     `json:"unrouted"`
	Completion          float64 `json:"completion"`
	Vias                int     `json:"vias"`
	FanoutVias          int     `json:"fanoutVias"`
	WireLengthIn        float64 `json:"wireLengthIn"`
	Iterations          int     `json:"iterations"`
	Conflicts           int     `json:"conflictsLeft"`
	Repaired            int     `json:"repairedNets"`
	Tuned               int     `json:"lengthTunedNets"`
	EscapesReleased     int     `json:"escapesReleased,omitempty"`
	KeptOnTimeout       int     `json:"keptOnTimeout,omitempty"`
	PreRepairViolations int     `json:"preRepairViolations"`
	ConflictTrace       []int   `json:"conflictTrace,omitempty"`
	GridMil             float64 `json:"gridMil"`
	Millis              int64   `json:"millis"`
}

// RouteResult is the router output.
type RouteResult struct {
	Tracks   []Track       `json:"tracks"`
	Vias     []Via         `json:"vias"`
	Unrouted []Unrouted    `json:"unrouted,omitempty"`
	Planes   []PlaneRegion `json:"planes,omitempty"`
	Stats    RouteStats    `json:"stats"`
	Notes    []string      `json:"notes,omitempty"`
}

// rnet is the router's per-net state.
type rnet struct {
	id        int32
	name      string
	plan      *NetPlan
	width     float64
	share     float64 // clearance share each side
	radius    float64 // claim radius = width/2 + share
	viaR      float64 // via claim radius = viaDia/2 + share
	onPlane   bool    // delivered by a plane layer: pads fan out, groups joined by the plane
	poured    bool    // 2-layer pour net
	groups    [][]*Pad
	route     bool
	fixed     []int32 // fan-out claims (never ripped)
	claims    []int32 // routed claims
	paths     []rpath
	fanTracks []Track
	fanVias   []Via
	fanFull   [][]int32 // full claim set of each fan-out via (+ its stub)
	fanTrack  []int     // index into fanTracks, -1 for in-pad thermal vias
	fanPinned []bool    // fan-outs that never yield (BGA dog-bones: no fallback inside a ball field)
	// Shared stubs: a plane ball tied to a neighbouring ball's via of the same
	// net (no via of its own). shareClaims[i] are the cells of shareTracks[i].
	shareTracks []Track
	shareClaims [][]int32
	escs        []*bgaEsc // BGA escapes: fixed copper from a ball to the array boundary
	failed      []Unrouted
	conflict    bool
	neckW       float64        // pad-entry width when the full width does not fit
	neckR       float64        // claim radius at neck width
	neck        map[int32]bool // columns near own pads where necking is allowed
}

type rpath struct {
	nodes          []int32 // grid indices (layer-aware)
	from           Point   // exact pad centre at the start (for stubs)
	to             Point
	fromPad, toPad bool
}

// router holds all state for one run.
type router struct {
	// BGA dog-bone state: balls fanned out by bgaFanout, and the via cell of
	// each signal ball's escape (an extra access node on every layer).
	bgaDone map[*Pad]bool
	escape  map[*Pad][2]int
	// bgaZones are the ball fields (array bbox + one pitch) with the part
	// they belong to: a net with a ball there may neck down anywhere inside.
	bgaZones []bgaZone
	// escOf is each pre-escaped ball's escape (its exit is the access node).
	escOf   map[*Pad]*bgaEsc
	escIdx  *escIndex // exact fixed-copper index while escapes are planned
	yielded int       // plane fan-outs dropped for a blocked signal

	b      *Board
	st     *Stackup
	an     *Analysis
	gr     *grid
	opt    RouteOptions
	nets   []*rnet
	byName map[string]*rnet
	// A* scratch
	gcost       []float32
	parent      []int32
	dir         []int8
	stamp       []int32
	closed      []int32
	cur         int32
	heap        iheap
	searchStats searchStats
	tgt         []int32 // search targets, stamped with cur
	floodSrc    []int32 // provablyUnreachable: sources, stamped with cur
	floodSeen   []int32
	floodQ      []int32
	nodeC       []float32 // cached node cost for the current search (NaN = unknown)
	nodeS       []int32
	claimStamp  []int32
	claimCur    int32
	presFac     float64
	strict      bool
	deadline    time.Time
	split       map[int]*coarse // split-plane labelling per layer id
	pairField   map[int32]float32
	pbuckets    [][]padEntry
	pbW, pbH    int
	viaS        []int32
	statics     map[int][]uint8
	staticMRU   [8]staticEntry
	owners      map[int][]uint32 // nodeOK pad-owner summaries per radius key
	ownerMRU    [8]ownerEntry
	viaC        []float64
}

// auditHook lets tests observe router state between phases.
var auditHook func(phase string, r *router)

var dirs8 = [8][2]int{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}}

// Route runs fan-out, plane splitting and negotiated-congestion routing.
func Route(ctx context.Context, b *Board, st *Stackup, an *Analysis, opt RouteOptions) (*RouteResult, error) {
	start := time.Now()
	if opt.GridMil <= 0 {
		opt.GridMil = defaultGrid(b.Rules)
	}
	if opt.MaxIters <= 0 {
		opt.MaxIters = 40
	}
	if opt.ViaCostMil <= 0 {
		opt.ViaCostMil = 60
	}
	if opt.WrongDirCost <= 0 {
		opt.WrongDirCost = 1.6
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 3 * time.Minute
	}
	gr, err := newGrid(b, st, opt.GridMil)
	if err != nil {
		return nil, err
	}
	r := &router{b: b, st: st, an: an, gr: gr, opt: opt, byName: map[string]*rnet{}}
	r.searchStats.stage = -1
	r.deadline = start.Add(opt.Timeout)
	n := len(gr.flags)
	r.gcost = make([]float32, n)
	r.parent = make([]int32, n)
	r.dir = make([]int8, n)
	r.stamp = make([]int32, n)
	r.closed = make([]int32, n)
	r.heap.pos = make([]int32, n)
	r.tgt = make([]int32, n)
	r.floodSrc = make([]int32, n)
	r.floodSeen = make([]int32, n)
	r.nodeC = make([]float32, n)
	r.nodeS = make([]int32, n)
	r.claimStamp = make([]int32, n)
	r.viaS = make([]int32, gr.W*gr.H)
	r.viaC = make([]float64, gr.W*gr.H)

	res := &RouteResult{}
	r.setupNets()
	r.rasterise()
	if !opt.NoFanout {
		r.fanout(res)
		r.bgaEscape(res)
	}
	if auditHook != nil {
		auditHook("fanout", r)
	}
	res.Planes = r.splitPlanes()
	r.planeGroups(res)
	if err := r.negotiate(ctx, res); err != nil {
		return nil, err
	}
	if auditHook != nil {
		auditHook("negotiate", r)
	}
	// Correctness steps get their own time: bridging plane/pour islands and
	// the DRC repair searches used to inherit an exhausted deadline, so every
	// search returned "timeout" at once (K230 with a poured power layer: 548
	// of 549 plane connections left open).
	r.deadline = time.Now().Add(postBudget(opt.Timeout))
	pctx, pcancel := context.WithTimeout(context.WithoutCancel(ctx), postBudget(opt.Timeout)+5*time.Second)
	defer pcancel()
	r.pourRepair(pctx, res)
	if auditHook != nil {
		auditHook("pourRepair", r)
	}
	r.emit(res)
	if os.Getenv("PCBAUTO_ROUTESTATS") != "" {
		st := r.searchStats
		res.Notes = append(res.Notes, sprintf("radii: %d statics, grid %dx%dx%d; searches ok %d (%d exp) failed %d (%d exp); by window stage [okN okExp failN failExp] %v; %d proven unreachable", len(r.statics), gr.W, gr.H, len(gr.layers), st.okN, st.okExp, st.failN, st.failExp, st.byStage, st.proofs))
	}
	res.Stats.GridMil = opt.GridMil
	res.Stats.Millis = time.Since(start).Milliseconds()
	return res, nil
}

// postBudget is the time reserved after negotiation for pour bridging and
// the exact-DRC repair: a sixth of the routing budget, at least 20 s.
func postBudget(t time.Duration) time.Duration {
	if b := t / 6; b > 20*time.Second {
		return b
	}
	return 20 * time.Second
}

// defaultGrid picks a pitch that resolves the tightest track/clearance.
//
// The pitch is derived from the dominant signal class so that its claim
// radius (w/2 + c/2) is exactly m+½ cells: two such tracks then never share a
// cell iff their centres are ≥ 2m+1 cells = w + c apart, i.e. the grid test
// is exact for the class that fills most of the board (a 10/6 rule gives
// 3.2 mil, where a naive 3 mil grid would allow a 15 mil pitch for a 16 mil
// requirement).
func defaultGrid(ru Rules) float64 {
	ra := ru.TrackWidth/2 + ru.Clearance/2
	best, bestErr := 3.0, math.Inf(1)
	for m := 1; m <= 8; m++ {
		g := ra / (float64(m) + 0.5)
		if g < 2 || g > 5 {
			continue
		}
		if e := math.Abs(g - 3); e < bestErr {
			best, bestErr = g, e
		}
	}
	return best
}

func (r *router) setupNets() {
	allow := map[string]bool{}
	for _, n := range r.opt.Nets {
		allow[n] = true
	}
	base := r.b.Rules.Clearance
	for i, n := range r.b.Nets() {
		plan := r.an.Plan(n.Name, r.b.Rules)
		rn := &rnet{id: int32(i), name: n.Name, plan: plan}
		rn.width = plan.WidthMil
		if rn.width <= 0 {
			rn.width = r.b.Rules.TrackWidth
		}
		// Clearance share: base nets c/2; high-voltage nets take the excess.
		rn.share = base / 2
		if plan.ClearanceMil > base {
			rn.share = plan.ClearanceMil - base/2
		}
		rn.radius = rn.width/2 + rn.share
		rn.viaR = r.b.Rules.ViaDia/2 + rn.share
		rn.neckW, rn.neckR = rn.width, rn.radius
		if nw := r.b.Rules.MinTrack; nw < rn.width { // fine-pitch escape: neck to the process minimum
			rn.neckW, rn.neckR = nw, nw/2+rn.share
		}
		if _, ok := r.st.PlaneFor(n.Name); ok {
			rn.onPlane = true
		}
		for _, l := range r.st.Stack {
			for _, pn := range l.PourNets {
				if pn == n.Name && !rn.onPlane {
					rn.poured = true
				}
			}
		}
		rn.route = len(allow) == 0 || allow[n.Name]
		for _, pd := range n.Pads {
			rn.groups = append(rn.groups, []*Pad{pd})
		}
		r.nets = append(r.nets, rn)
		r.byName[n.Name] = rn
	}
}

func (r *router) rasterise() {
	gr := r.gr
	// Pad buckets for exact checks: a pad is listed in every 50-mil bucket
	// its copper plus the largest possible reach (clearance + half via) touches.
	// Pad buckets for exact checks: a pad is listed in every bucket its
	// copper plus the largest possible reach (clearance + half via) touches,
	// so a lookup only needs the query point's own bucket.
	reach := r.b.Rules.ViaDia/2 + r.b.Rules.Clearance
	for _, rn := range r.nets {
		reach = math.Max(reach, rn.plan.ClearanceMil+rn.width/2)
	}
	reach += 2 * gr.g
	r.pbW = int(float64(gr.W)*gr.g/padBucket) + 1
	r.pbH = int(float64(gr.H)*gr.g/padBucket) + 1
	r.pbuckets = make([][]padEntry, r.pbW*r.pbH)
	for _, p := range r.b.Parts {
		for _, pd := range p.Pads {
			e := padEntry{pd: pd, net: -2, clr: r.b.Rules.Clearance}
			if rn := r.byName[pd.Net]; rn != nil {
				e.net, e.clr = rn.id, math.Max(e.clr, rn.plan.ClearanceMil)
			}
			bb := pd.Box.Bounds().Expand(reach)
			for bx := max(int((bb.MinX-gr.ox)/padBucket), 0); bx <= min(int((bb.MaxX-gr.ox)/padBucket), r.pbW-1); bx++ {
				for by := max(int((bb.MinY-gr.oy)/padBucket), 0); by <= min(int((bb.MaxY-gr.oy)/padBucket), r.pbH-1); by++ {
					r.pbuckets[by*r.pbW+bx] = append(r.pbuckets[by*r.pbW+bx], e)
				}
			}
		}
	}
	gr.markEdge(r.b.Outline, r.b.Rules.EdgeClearance)
	for _, k := range r.b.Keepouts {
		gr.markKeepout(k)
	}
	for _, h := range r.b.Holes {
		gr.markHole(h)
	}
	viaR := r.b.Rules.ViaDia / 2
	for _, p := range r.b.Parts {
		for _, pd := range p.Pads {
			id := int32(-2) // netless pad: hard for everyone
			share := r.b.Rules.Clearance / 2
			if rn := r.byName[pd.Net]; rn != nil {
				id, share = rn.id, rn.share
			}
			gr.markPad(pd, id, share, viaR)
		}
	}
}

// ---- claims ---------------------------------------------------------------

// nodeOK reports whether net n may place copper of claim radius rad at a
// node. The grid test covers the claim disk; when another net's pad claim
// lies in the ring just outside it (where discretisation can hide a
// violation), the exact pad distance decides.
func (r *router) nodeOK(n *rnet, l, x, y int, rad float64) bool {
	gr := r.gr
	switch r.static(rad)[gr.idx(l, x, y)] {
	case staticClear:
		return true
	case staticBlocked:
		return false
	}
	if ow := r.ownerArray(rad); ow != nil && len(r.nets) < ownerNetLimit && !noOwnerCache {
		// Which nets own the pads in the inner disk and in the ring is
		// static: summarised once per (radius, cell), so later calls are one
		// array read instead of a ring scan (a third of K230's routing time).
		i := gr.idx(l, x, y)
		v := ow[i]
		if v == 0 {
			v = r.scanOwners(l, x, y, rad)
			ow[i] = v
		}
		inner, ring := int32(v>>16)-2, int32(v&0xffff)-2
		if inner == ownerMany || inner >= 0 && inner != n.id {
			return false
		}
		if ring == ownerMany || ring >= 0 && ring != n.id {
			return r.padsClear(n, gr.layers[l], gr.center(x, y), rad-n.share)
		}
		return true
	}
	offs, inner := gr.ring(rad)
	near := false
	for k, o := range offs {
		xx, yy := x+o[0], y+o[1]
		if !gr.in(xx, yy) {
			if k < inner {
				return false
			}
			continue
		}
		j := gr.idx(l, xx, yy)
		p := gr.pad[j]
		if k < inner {
			if gr.flags[j]&flagHard != 0 || p != -1 && p != n.id {
				return false
			}
		} else if p != -1 && p != n.id {
			near = true
			break
		}
	}
	if near {
		return r.padsClear(n, gr.layers[l], gr.center(x, y), rad-n.share)
	}
	return true
}

const (
	ownerNone int32 = -1 // no pad
	ownerMany int32 = -2 // pads of several nets (or a multi-net pad)
	// ownerNetLimit keeps net ids inside the 16-bit fields of an owner entry.
	ownerNetLimit = 0xfff0
	// ownerMaxBytes caps the owner arrays; beyond it nodeOK scans as before.
	ownerMaxBytes = 512 << 20
)

// ownerArray returns the lazily filled owner summary for claim radius rad:
// per layer-cell 0 = not computed, else (inner+2)<<16 | (ring+2) with inner
// and ring each ownerNone, ownerMany or the single net owning those pads.
func (r *router) ownerArray(rad float64) []uint32 {
	for k := range r.ownerMRU {
		if e := &r.ownerMRU[k]; e.m != nil && e.rad == rad {
			return e.m
		}
	}
	key := int(math.Round(rad * 100))
	m, ok := r.owners[key]
	if !ok {
		if (len(r.owners)+1)*len(r.gr.flags)*4 > ownerMaxBytes {
			return nil
		}
		if r.owners == nil {
			r.owners = map[int][]uint32{}
		}
		m = make([]uint32, len(r.gr.flags))
		r.owners[key] = m
	}
	copy(r.ownerMRU[1:], r.ownerMRU[:len(r.ownerMRU)-1])
	r.ownerMRU[0] = ownerEntry{rad, m}
	return m
}

type ownerEntry struct {
	rad float64
	m   []uint32
}

// scanOwners summarises the pads around a staticPads cell (whose inner disk
// is inside the grid and free of hard cells and multi-net pads).
func (r *router) scanOwners(l, x, y int, rad float64) uint32 {
	gr := r.gr
	inner, ring := ownerNone, ownerNone
	merge := func(o *int32, p int32) {
		switch {
		case p == -1:
		case p < 0 || *o == ownerMany:
			*o = ownerMany
		case *o == ownerNone:
			*o = p
		case *o != p:
			*o = ownerMany
		}
	}
	offs, in := gr.ring(rad)
	for k, o := range offs {
		xx, yy := x+o[0], y+o[1]
		if !gr.in(xx, yy) {
			continue
		}
		p := gr.pad[gr.idx(l, xx, yy)]
		if k < in {
			merge(&inner, p)
		} else {
			merge(&ring, p)
		}
	}
	return uint32(inner+2)<<16 | uint32(ring+2)
}

const (
	staticUnknown uint8 = iota
	staticClear         // no hard cell in the disk and no pad claim in disk+ring: legal for every net
	staticBlocked       // a hard cell (edge, keepout, hole, netless pad) in the disk: illegal for every net
	staticPads          // pad claims nearby: decide per net
)

// static returns the net-independent legality map for claim radius rad,
// computed once per distinct radius. It turns most node checks into a
// single byte read; only cells near pads fall back to the per-net test.
func (r *router) static(rad float64) []uint8 {
	// A search asks for the same two or three radii millions of times; the
	// map lookup (with its rounding) was ~7 % of routing time.
	for k := range r.staticMRU {
		if e := &r.staticMRU[k]; e.m != nil && e.rad == rad {
			return e.m
		}
	}
	m := r.staticSlow(rad)
	copy(r.staticMRU[1:], r.staticMRU[:len(r.staticMRU)-1])
	r.staticMRU[0] = staticEntry{rad, m}
	return m
}

type staticEntry struct {
	rad float64
	m   []uint8
}

func (r *router) staticSlow(rad float64) []uint8 {
	gr := r.gr
	key := int(math.Round(rad * 100))
	if m, ok := r.statics[key]; ok {
		return m
	}
	m := make([]uint8, len(gr.flags))
	offs, inner := gr.ring(rad)
	for l := range gr.layers {
		for y := 0; y < gr.H; y++ {
			for x := 0; x < gr.W; x++ {
				v := staticClear
				for k, o := range offs {
					xx, yy := x+o[0], y+o[1]
					if !gr.in(xx, yy) {
						if k < inner {
							v = staticBlocked
							break
						}
						continue
					}
					j := gr.idx(l, xx, yy)
					p := gr.pad[j]
					if k < inner && (gr.flags[j]&flagHard != 0 || p == -2) {
						v = staticBlocked
						break
					}
					if p != -1 {
						v = staticPads
					}
				}
				m[gr.idx(l, x, y)] = v
			}
		}
	}
	if r.statics == nil {
		r.statics = map[int][]uint8{}
	}
	r.statics[key] = m
	return m
}

// nodeRadius returns the claim radius net n uses at a node: full width when
// it fits, the neck width near n's own pads, or -1 when neither is legal.
// It depends only on static obstacles, so it is stable across searches.
func (r *router) nodeRadius(n *rnet, l, x, y int) float64 {
	if r.nodeOK(n, l, x, y, n.radius) {
		return n.radius
	}
	if n.neckR < n.radius && r.inNeck(n, x, y) && r.nodeOK(n, l, x, y, n.neckR) {
		return n.neckR
	}
	return -1
}

// padsClear is the exact (non-grid) check of copper of half-size hw centred
// at p against other nets' pads on layer id (LayerMulti = every layer).
// It removes the grid's discretisation error where it matters most.
func (r *router) padsClear(n *rnet, id int, p Point, hw float64) bool {
	bx := int((p.X - r.gr.ox) / padBucket)
	by := int((p.Y - r.gr.oy) / padBucket)
	if bx < 0 || by < 0 || bx >= r.pbW || by >= r.pbH {
		return true
	}
	for _, e := range r.pbuckets[by*r.pbW+bx] {
		if e.net == n.id && e.net >= 0 {
			continue
		}
		if id != LayerMulti && !e.pd.OnLayer(id) {
			continue
		}
		if e.pd.Box.Dist(p) < hw+math.Max(e.clr, n.plan.ClearanceMil) {
			return false
		}
	}
	return true
}

// inNeck reports whether column (x,y) is close enough to one of n's pads for
// the track to neck down (within max(3 widths, 30 mil) of the pad copper).
func (r *router) inNeck(n *rnet, x, y int) bool {
	if n.neck == nil {
		n.neck = map[int32]bool{}
		gr := r.gr
		reach := math.Max(3*n.width, 30)
		for _, g := range n.groups {
			for _, pd := range g {
				gr.forCellsNear(pd.Box.Bounds(), reach, pd.Box.Dist, func(xx, yy int) {
					n.neck[int32(yy*gr.W+xx)] = true
				})
				// Inside a ball field the whole escape runs between balls and
				// dog-bone vias, where only the minimum width fits — not just
				// the first 30 mil from the ball.
				for _, z := range r.bgaZones {
					if z.part != pd.Part {
						continue
					}
					x0, y0 := gr.cellOf(Point{z.box.MinX, z.box.MinY})
					x1, y1 := gr.cellOf(Point{z.box.MaxX, z.box.MaxY})
					for yy := max(y0, 0); yy <= min(y1, gr.H-1); yy++ {
						for xx := max(x0, 0); xx <= min(x1, gr.W-1); xx++ {
							n.neck[int32(yy*gr.W+xx)] = true
						}
					}
				}
			}
		}
	}
	return n.neck[int32(y*r.gr.W+x)]
}

// nodeCong sums other nets' claims inside the disk.
func (r *router) nodeCong(l, x, y int, rad float64) (occ float64, hist float64) {
	gr := r.gr
	// Empty neighbourhood (the common case): the block summaries prove the
	// sum is zero. Skipping zero terms leaves the float sum unchanged.
	ri := int(math.Ceil(rad/gr.g)) + 1
	bx0, bx1 := max(x-ri, 0)/blockCells, min(x+ri, gr.W-1)/blockCells
	by0, by1 := max(y-ri, 0)/blockCells, min(y+ri, gr.H-1)/blockCells
	empty := true
	for by := by0; by <= by1 && empty; by++ {
		base := (l*gr.bH + by) * gr.bW
		for bx := bx0; bx <= bx1; bx++ {
			if gr.bUse[base+bx] != 0 || gr.bHist[base+bx] != 0 {
				empty = false
				break
			}
		}
	}
	if empty {
		return 0, 0
	}
	for _, o := range gr.disk(rad) {
		j := gr.idx(l, x+o[0], y+o[1])
		if u := gr.use[j]; u > 0 {
			occ += float64(u)
		}
		hist += float64(gr.hist[j])
	}
	return
}

// cost returns the congestion-weighted cost factor of occupying cell i for
// net n, or +Inf when illegal. Cached per search.
func (r *router) cost(n *rnet, i int) float32 {
	if r.nodeS[i] == r.cur {
		return r.nodeC[i]
	}
	gr := r.gr
	l, x, y := gr.xy(i)
	var c float32
	if rad := r.nodeRadius(n, l, x, y); rad < 0 {
		c = float32(math.Inf(1))
	} else {
		occ, hist := r.nodeCong(l, x, y, rad)
		if r.strict && occ > 0 {
			c = float32(math.Inf(1))
		} else {
			area := float64(len(gr.disk(n.radius)))
			c = float32((1 + hist/area) * (1 + r.presFac*occ))
			if r.pairField != nil {
				if f, ok := r.pairField[int32(i)]; ok {
					c *= f // run alongside the routed partner at the pair pitch
				}
			}
		}
	}
	r.nodeS[i], r.nodeC[i] = r.cur, c
	return c
}

// viaCost returns the cost factor of a via at column (x,y), or +Inf.
func (r *router) viaCost(n *rnet, x, y int) float64 {
	gr := r.gr
	col := y*gr.W + x
	if r.viaS[col] == r.cur {
		return r.viaC[col]
	}
	v := r.viaCostUncached(n, x, y)
	r.viaS[col], r.viaC[col] = r.cur, v
	return v
}

func (r *router) viaCostUncached(n *rnet, x, y int) float64 {
	return r.viaCostR(n, x, y, n.viaR)
}

// viaCostR is viaCostUncached for a via of claim radius rad (a BGA via class
// may be smaller than the board default).
func (r *router) viaCostR(n *rnet, x, y int, rad float64) float64 {
	gr := r.gr
	if gr.noVia[y*gr.W+x] {
		return math.Inf(1)
	}
	total := 1.0
	for l := range gr.layers {
		// Vias perforate plane layers too, but planes retreat (anti-pad);
		// only the signal layers and hard/pad claims constrain placement.
		if !r.nodeOK(n, l, x, y, rad) {
			return math.Inf(1)
		}
		if gr.routable[l] {
			occ, _ := r.nodeCong(l, x, y, rad)
			if r.strict && occ > 0 {
				return math.Inf(1)
			}
			total += r.presFac * occ
		}
	}
	return total
}

// stampClaims adds (sign=+1) or removes (-1) a claim list.
func (r *router) applyClaims(list []int32, sign int) {
	gr := r.gr
	for _, i := range list {
		if sign > 0 {
			gr.use[i]++
			gr.bUse[gr.blockOf(int(i))]++
		} else if gr.use[i] > 0 {
			gr.use[i]--
			gr.bUse[gr.blockOf(int(i))]--
		}
	}
}

// claimPath collects the unique cells claimed by a path of nodes (and vias).
func (r *router) claimNodes(n *rnet, nodes []int32, out []int32) []int32 {
	gr := r.gr
	r.claimCur++
	add := func(l, x, y int, rad float64) {
		for _, o := range gr.disk(rad) {
			xx, yy := x+o[0], y+o[1]
			if !gr.in(xx, yy) {
				continue
			}
			j := gr.idx(l, xx, yy)
			if r.claimStamp[j] != r.claimCur {
				r.claimStamp[j] = r.claimCur
				out = append(out, int32(j))
			}
		}
	}
	for k, i := range nodes {
		l, x, y := gr.xy(int(i))
		if rad := r.nodeRadius(n, l, x, y); rad > 0 {
			add(l, x, y, rad)
		}
		if k > 0 {
			pl, px, py := gr.xy(int(nodes[k-1]))
			if pl != l && px == x && py == y {
				for ll := range gr.layers {
					add(ll, x, y, n.viaR)
				}
			}
		}
	}
	return out
}

// claimSegment claims cells along an exact segment (for fan-out stubs).
func (r *router) claimSegment(n *rnet, l int, a, b Point, width float64, out []int32) []int32 {
	gr := r.gr
	rad := width/2 + n.share
	r.claimCur++
	steps := int(math.Ceil(a.Dist(b)/(gr.g/2))) + 1
	for s := 0; s <= steps; s++ {
		p := a.Add(b.Sub(a).Scale(float64(s) / float64(steps)))
		cx, cy := gr.cellOf(p)
		for _, o := range gr.disk(rad) {
			xx, yy := cx+o[0], cy+o[1]
			if !gr.in(xx, yy) {
				continue
			}
			j := gr.idx(l, xx, yy)
			if r.claimStamp[j] != r.claimCur {
				r.claimStamp[j] = r.claimCur
				out = append(out, int32(j))
			}
		}
	}
	return out
}

// segmentOK checks an exact straight segment for net n on layer l.
func (r *router) segmentOK(n *rnet, l int, a, b Point, width float64, strict bool) bool {
	gr := r.gr
	rad := width/2 + n.share
	steps := int(math.Ceil(a.Dist(b)/(gr.g/2))) + 1
	for s := 0; s <= steps; s++ {
		p := a.Add(b.Sub(a).Scale(float64(s) / float64(steps)))
		x, y := gr.cellOf(p)
		if !gr.in(x, y) || !r.nodeOK(n, l, x, y, rad) {
			return false
		}
		// Off-grid sample: judge pads at the true point, not the cell centre.
		if r.static(rad)[gr.idx(l, x, y)] == staticPads && !r.padsClear(n, gr.layers[l], p, width/2) {
			return false
		}
		if strict {
			if occ, _ := r.nodeCong(l, x, y, rad); occ > 0 {
				return false
			}
		}
	}
	return true
}

// ---- pad access -----------------------------------------------------------

// access returns grid nodes inside the pad copper usable by net n, nearest
// to the pad centre first (at most 9 per layer).
func (r *router) access(n *rnet, pd *Pad) []int32 {
	gr := r.gr
	var out []int32
	if es := r.escOf[pd]; es != nil {
		// Pre-escaped ball: the net starts at the array boundary.
		return []int32{es.end}
	}
	if e, ok := r.escape[pd]; ok {
		// A dog-bone escape: the via is reachable on every routable layer.
		for l := range gr.layers {
			if gr.routable[l] {
				out = append(out, int32(gr.idx(l, e[0], e[1])))
			}
		}
	}
	for l, id := range gr.layers {
		if !gr.routable[l] || !pd.OnLayer(id) {
			continue
		}
		type cand struct {
			i int32
			d float64
		}
		var cs []cand
		bb := pd.Box.Bounds()
		x0, y0 := gr.cellOf(Point{bb.MinX, bb.MinY})
		x1, y1 := gr.cellOf(Point{bb.MaxX, bb.MaxY})
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				if !gr.in(x, y) {
					continue
				}
				c := gr.center(x, y)
				if pd.Box.Dist(c) > 0 {
					continue
				}
				if r.nodeRadius(n, l, x, y) > 0 {
					cs = append(cs, cand{int32(gr.idx(l, x, y)), c.Dist(pd.Box.C)})
				}
			}
		}
		if len(cs) == 0 {
			// Pad smaller than a cell: the nearest node, if legal.
			x, y := gr.cellOf(pd.Box.C)
			for _, o := range gr.disk(gr.g * 1.5) {
				xx, yy := x+o[0], y+o[1]
				if gr.in(xx, yy) && r.nodeRadius(n, l, xx, yy) > 0 {
					cs = append(cs, cand{int32(gr.idx(l, xx, yy)), gr.center(xx, yy).Dist(pd.Box.C)})
				}
			}
		}
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].d != cs[j].d {
				return cs[i].d < cs[j].d
			}
			return cs[i].i < cs[j].i
		})
		for k := 0; k < len(cs) && k < 9; k++ {
			out = append(out, cs[k].i)
		}
	}
	return out
}

// ---- A* -------------------------------------------------------------------

type pqItem struct {
	i int32
	f float32
}

// iheap is a binary min-heap on (f, i) with a position index per node, so a
// node's key can be lowered in place.
type iheap struct {
	items []pqItem
	pos   []int32 // node → heap slot (valid while the node is open)
}

func (q *iheap) less(a, b pqItem) bool { return a.f < b.f || a.f == b.f && a.i < b.i }

// set inserts it, or lowers the key of a node already open.
func (q *iheap) set(it pqItem, open bool) {
	k := len(q.items)
	if open {
		k = int(q.pos[it.i])
		q.items[k] = it
	} else {
		q.items = append(q.items, it)
	}
	q.up(k)
}

func (q *iheap) up(k int) {
	h := q.items
	it := h[k]
	for k > 0 {
		p := (k - 1) / 2
		if !q.less(it, h[p]) {
			break
		}
		h[k] = h[p]
		q.pos[h[k].i] = int32(k)
		k = p
	}
	h[k] = it
	q.pos[it.i] = int32(k)
}

func (q *iheap) pop() pqItem {
	h := q.items
	top := h[0]
	n := len(h) - 1
	last := h[n]
	h = h[:n]
	q.items = h
	if n == 0 {
		return top
	}
	k := 0
	for {
		l := 2*k + 1
		if l >= n {
			break
		}
		m := l
		if l+1 < n && q.less(h[l+1], h[l]) {
			m = l + 1
		}
		if !q.less(h[m], last) {
			break
		}
		h[k] = h[m]
		q.pos[h[k].i] = int32(k)
		k = m
	}
	h[k] = last
	q.pos[last.i] = int32(k)
	return top
}

// search finds a path from any source node to any target node for net n,
// restricted to bounds (cell rect). Returns the node list source→target.
func (r *router) search(n *rnet, sources []int32, targets map[int32]bool, bounds [4]int) (found []int32) {
	gr := r.gr
	r.cur++
	if r.cur == math.MaxInt32 {
		for i := range r.stamp {
			r.stamp[i], r.closed[i], r.nodeS[i], r.tgt[i] = 0, 0, 0, 0
			r.floodSrc[i], r.floodSeen[i] = 0, 0
		}
		for i := range r.viaS {
			r.viaS[i] = 0
		}
		r.cur = 1
	}
	// Heuristic: octile distance to the targets' bounding box.
	tb := [4]int{math.MaxInt32, math.MaxInt32, -1, -1}
	for t := range targets {
		_, x, y := gr.xy(int(t))
		tb = [4]int{min(tb[0], x), min(tb[1], y), max(tb[2], x), max(tb[3], y)}
		r.tgt[t] = r.cur // array membership: a map lookup per pop was ~1 %
	}
	g := float32(gr.g)
	h := func(x, y int) float32 {
		dx := max(tb[0]-x, 0, x-tb[2])
		dy := max(tb[1]-y, 0, y-tb[3])
		mn, mx := min(dx, dy), max(dx, dy)
		return g * (float32(mx-mn) + 1.4142*float32(mn))
	}
	// Indexed heap with decrease-key: every open node is in it once. Pops come
	// out in the same (f, index) order as the old lazy-deletion heap, whose
	// stale duplicates were skipped — the paths are identical, the heap is a
	// fraction of the size (the lazy heap was 27 % of routing time).
	q := &r.heap
	q.items = q.items[:0]
	for _, s := range sources {
		c := r.cost(n, int(s))
		if math.IsInf(float64(c), 1) && r.tgt[s] != r.cur {
			continue
		}
		open := r.stamp[s] == r.cur
		r.stamp[s], r.gcost[s], r.parent[s], r.dir[s] = r.cur, 0, -1, -1
		_, x, y := gr.xy(int(s))
		q.set(pqItem{s, h(x, y)}, open)
	}
	nl := len(gr.layers)
	viaCost := float32(r.opt.ViaCostMil)
	expansions := 0
	defer func() { r.searchStats.add(expansions, found) }()
	for len(q.items) > 0 {
		it := q.pop()
		i := it.i
		r.closed[i] = r.cur
		if r.tgt[i] == r.cur {
			var path []int32
			for k := i; k >= 0; k = r.parent[k] {
				path = append(path, k)
			}
			for a, b := 0, len(path)-1; a < b; a, b = a+1, b-1 {
				path[a], path[b] = path[b], path[a]
			}
			return path
		}
		expansions++
		if expansions&0xfff == 0 && time.Now().After(r.deadline) {
			return nil
		}
		l, x, y := gr.xy(int(i))
		gi := r.gcost[i]
		pd := r.dir[i]
		pref := r.st.Stack[l].Dir
		for d := 0; d < 8; d++ {
			if pd >= 0 {
				turn := (d - int(pd) + 8) % 8
				if turn == 3 || turn == 5 || turn == 4 {
					continue // no acute turns, no reversal
				}
			}
			xx, yy := x+dirs8[d][0], y+dirs8[d][1]
			if xx < bounds[0] || yy < bounds[1] || xx > bounds[2] || yy > bounds[3] {
				continue
			}
			j := int32(gr.idx(l, xx, yy))
			if r.closed[j] == r.cur {
				continue
			}
			c := r.cost(n, int(j))
			if math.IsInf(float64(c), 1) {
				continue
			}
			step := g
			if d%2 == 1 {
				step *= 1.4142
				if pref != "" {
					step *= 1.15
				}
			} else if (pref == "h" && d%4 == 2) || (pref == "v" && d%4 == 0) {
				step *= float32(r.opt.WrongDirCost)
			}
			if pd >= 0 && int(pd) != d {
				if (d-int(pd)+8)%8 == 2 || (d-int(pd)+8)%8 == 6 {
					step += 2 * g // 90° bend
				} else {
					step += 0.4 * g // 45° bend
				}
			}
			ng := gi + step*c
			if open := r.stamp[j] == r.cur; !open || ng < r.gcost[j] {
				r.stamp[j], r.gcost[j], r.parent[j], r.dir[j] = r.cur, ng, i, int8(d)
				q.set(pqItem{j, ng + h(xx, yy)}, open)
			}
		}
		// Layer change through a via.
		if nl > 1 {
			vc := float32(-1)
			for ll := 0; ll < nl; ll++ {
				if ll == l || !gr.routable[ll] {
					continue
				}
				j := int32(gr.idx(ll, x, y))
				if r.closed[j] == r.cur {
					continue
				}
				if vc < 0 {
					v := r.viaCost(n, x, y)
					if math.IsInf(v, 1) {
						break
					}
					vc = float32(v)
				}
				c := r.cost(n, int(j))
				if math.IsInf(float64(c), 1) {
					continue
				}
				ng := gi + viaCost*vc
				if open := r.stamp[j] == r.cur; !open || ng < r.gcost[j] {
					r.stamp[j], r.gcost[j], r.parent[j], r.dir[j] = r.cur, ng, i, -1
					q.set(pqItem{j, ng + h(x, y)}, open)
				}
			}
		}
	}
	return nil
}

// ---- per-net routing ------------------------------------------------------

func padKeys(ps []*Pad) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Key()
	}
	return out
}

// ripUp removes net n's claims (routed and optionally fixed) from the grid.
func (r *router) ripUp(n *rnet, fixed bool) {
	r.applyClaims(n.claims, -1)
	n.claims, n.paths = nil, nil
	if fixed {
		r.applyClaims(n.fixed, -1)
	}
}

// routeNet connects all terminal groups of n with a Steiner-like tree.
func (r *router) routeNet(n *rnet) bool { return r.routeNetKeep(n, false) }

// routeNetKeep routes n's terminal groups. With keep, existing paths stay and
// the new connections are appended (used to bridge plane/pour islands).
func (r *router) routeNetKeep(n *rnet, keep bool) bool {
	old := n.paths
	r.ripUp(n, true)
	defer r.applyClaims(n.fixed, +1)
	r.pairField = r.buildPairField(n)
	defer func() { r.pairField = nil }()
	n.failed = nil
	if keep {
		n.paths = old
	}
	if len(n.groups) < 2 {
		return true
	}
	gr := r.gr
	// Tree sources: every access node of the first group plus fan-out copper.
	type term struct {
		nodes []int32
		pads  []*Pad
	}
	terms := make([]term, 0, len(n.groups))
	for _, g := range n.groups {
		t := term{pads: g}
		for _, pd := range g {
			t.nodes = append(t.nodes, r.access(n, pd)...)
		}
		terms = append(terms, t)
	}
	// Start from the group nearest the net's centroid for balanced trees.
	var cen Point
	cnt := 0.0
	for _, t := range terms {
		for _, pd := range t.pads {
			cen = cen.Add(pd.Box.C)
			cnt++
		}
	}
	cen = cen.Scale(1 / cnt)
	best := 0
	for k, t := range terms {
		if t.pads[0].Box.C.Dist(cen) < terms[best].pads[0].Box.C.Dist(cen) && len(t.nodes) > 0 {
			best = k
		}
	}
	// Bridging islands (plane nets, partial repairs): the largest island is
	// the root — it is the body of the net, reachable from everywhere. The
	// group nearest the centroid can be a lone pad walled in by signals, and
	// every island then failed against it (K230 GND: 170 of 170).
	bridging := false
	if big := largestGroup(terms, func(t term) int { return len(t.pads) }, func(t term) bool { return len(t.nodes) > 0 }); big >= 0 && len(terms[big].pads) > 1 {
		best, bridging = big, true
	}
	terms[0], terms[best] = terms[best], terms[0]
	if len(terms[0].nodes) == 0 {
		for _, t := range terms {
			n.failed = append(n.failed, Unrouted{Net: n.name, Pads: padKeys(t.pads), Reason: "pad-inaccessible"})
		}
		return false
	}
	tree := append([]int32(nil), terms[0].nodes...)
	treePads := map[int32]*Pad{}
	for _, pd := range terms[0].pads {
		for _, a := range r.access(n, pd) {
			treePads[a] = pd
		}
	}
	remaining := terms[1:]
	rootPads := terms[0].pads
	grown := false
	ok := true
	for len(remaining) > 0 {
		targets := map[int32]bool{}
		owner := map[int32]int{}
		for k, t := range remaining {
			for _, a := range t.nodes {
				targets[a] = true
				owner[a] = k
			}
		}
		if len(targets) == 0 {
			for _, t := range remaining {
				n.failed = append(n.failed, Unrouted{Net: n.name, Pads: padKeys(t.pads), Reason: "pad-inaccessible"})
			}
			// Paths found so far must still be claimed below.
			ok = false
			break
		}
		var path []int32
		// Growing search windows: tight bbox first, whole board last.
		bb := [4]int{math.MaxInt32, math.MaxInt32, -1, -1}
		for _, s := range tree {
			_, x, y := gr.xy(int(s))
			bb = [4]int{min(bb[0], x), min(bb[1], y), max(bb[2], x), max(bb[3], y)}
		}
		for t := range targets {
			_, x, y := gr.xy(int(t))
			bb = [4]int{min(bb[0], x), min(bb[1], y), max(bb[2], x), max(bb[3], y)}
		}
		// A connection whose targets sit in a closed pocket fails in every
		// window; proving that with a cheap flood from the target side skips
		// three full A* searches (K230: 95 % of all search work went into
		// failed searches, and the larger windows rescued 5 of 729).
		unreachable := r.provablyUnreachable(n, tree, targets)
		if unreachable {
			r.searchStats.proofs++
		}
		for stage, grow := range []int{int(120 / gr.g), int(400 / gr.g), 1 << 20} {
			if unreachable {
				break
			}
			m := grow + (bb[2]-bb[0]+bb[3]-bb[1])/4
			w := [4]int{max(bb[0]-m, 0), max(bb[1]-m, 0), min(bb[2]+m, gr.W-1), min(bb[3]+m, gr.H-1)}
			r.searchStats.stage = stage
			path = r.search(n, tree, targets, w)
			r.searchStats.stage = -1
			if path != nil {
				break
			}
			if time.Now().After(r.deadline) {
				break
			}
		}
		if path == nil && bridging && !grown && len(remaining) > 1 && !time.Now().After(r.deadline) {
			// Nothing reached from the root: the root is the likely culprit
			// (walled in), not the group it failed to reach. It is the root
			// that is reported; routing restarts from the largest remaining
			// group.
			n.failed = append(n.failed, Unrouted{Net: n.name, Pads: padKeys(rootPads), Reason: r.failReason()})
			ok = false
			k := largestGroup(remaining, func(t term) int { return len(t.pads) }, func(t term) bool { return len(t.nodes) > 0 })
			if k < 0 {
				k = 0
			}
			tree = append([]int32(nil), remaining[k].nodes...)
			treePads = map[int32]*Pad{}
			for _, pd := range remaining[k].pads {
				for _, a := range r.access(n, pd) {
					treePads[a] = pd
				}
			}
			rootPads = remaining[k].pads
			remaining = append(remaining[:k], remaining[k+1:]...)
			continue
		}
		if path == nil {
			// Nearest remaining group fails; report it and continue with others.
			k := 0
			for kk, t := range remaining {
				if len(t.nodes) > 0 {
					k = kk
					break
				}
			}
			n.failed = append(n.failed, Unrouted{Net: n.name, Pads: padKeys(remaining[k].pads), Reason: r.failReason()})
			remaining = append(remaining[:k], remaining[k+1:]...)
			ok = false
			continue
		}
		end := path[len(path)-1]
		k := owner[end]
		rp := rpath{nodes: path}
		if pd, ok := treePads[path[0]]; ok {
			rp.from, rp.fromPad = r.anchor(pd), true
		}
		for _, pd := range remaining[k].pads {
			if pd.Box.Dist(gr.center(xyOf(gr, end))) == 0 || contains(r.access(n, pd), end) {
				rp.to, rp.toPad = r.anchor(pd), true
				break
			}
		}
		n.paths = append(n.paths, rp)
		grown = true
		tree = append(tree, path...)
		for _, pd := range remaining[k].pads {
			for _, a := range r.access(n, pd) {
				tree = append(tree, a)
				treePads[a] = pd
			}
		}
		remaining = append(remaining[:k], remaining[k+1:]...)
	}
	// Claim everything routed; each path separately so vias register.
	n.claims = n.claims[:0]
	for _, p := range n.paths {
		n.claims = r.claimNodes(n, p.nodes, n.claims)
	}
	n.claims = r.minusFixed(n, dedup(n.claims))
	r.applyClaims(n.claims, +1)
	return ok
}

// largestGroup returns the index of the largest element by size among
// those passing ok, or -1.
func largestGroup[T any](xs []T, size func(T) int, ok func(T) bool) int {
	best := -1
	for k, x := range xs {
		if ok(x) && (best < 0 || size(x) > size(xs[best])) {
			best = k
		}
	}
	return best
}

func (r *router) failReason() string {
	if time.Now().After(r.deadline) {
		return "timeout"
	}
	if r.strict {
		return "no-legal-path"
	}
	return "blocked"
}

func xyOf(gr *grid, i int32) (int, int) {
	_, x, y := gr.xy(int(i))
	return x, y
}

func contains(xs []int32, v int32) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func dedup(xs []int32) []int32 {
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	out := xs[:0]
	for i, x := range xs {
		if i == 0 || x != xs[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// conflicts marks nets whose claims overlap another net's claims.
func (r *router) conflicts() int {
	total := 0
	for _, n := range r.nets {
		n.conflict = false
		for _, i := range n.claims {
			if r.gr.use[i] > 1 {
				n.conflict = true
				break
			}
		}
		if n.conflict {
			total++
		}
	}
	return total
}

func (r *router) routeOrder() []*rnet {
	var out []*rnet
	for _, n := range r.nets {
		if n.route && len(n.groups) > 1 {
			out = append(out, n)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.plan.Priority != b.plan.Priority {
			return a.plan.Priority < b.plan.Priority
		}
		// Short nets first: they have the fewest alternatives.
		return netSpan(a) < netSpan(b)
	})
	// Differential partners route back to back so the second can hug the first.
	placed := map[*rnet]bool{}
	var paired []*rnet
	for _, n := range out {
		if placed[n] {
			continue
		}
		placed[n] = true
		paired = append(paired, n)
		if p := r.byName[n.plan.PairWith]; p != nil && !placed[p] && p.route && len(p.groups) > 1 {
			placed[p] = true
			paired = append(paired, p)
		}
	}
	return paired
}

// buildPairField marks, for net n, the cells lying exactly one pair pitch
// (width + gap) from its routed partner's centreline on the same layer: the
// search makes them cheaper, so the pair is routed coupled.
func (r *router) buildPairField(n *rnet) map[int32]float32 {
	p := r.byName[n.plan.PairWith]
	if p == nil || len(p.paths) == 0 {
		return nil
	}
	gr := r.gr
	gap := n.plan.PairGapMil
	if gap <= 0 {
		gap = r.b.Rules.Clearance
	}
	pitch := (n.width+p.width)/2 + gap
	lo, hi := pitch/gr.g-0.5, pitch/gr.g+0.7
	k := int(math.Ceil(hi))
	var ring [][2]int
	for dy := -k; dy <= k; dy++ {
		for dx := -k; dx <= k; dx++ {
			d := math.Hypot(float64(dx), float64(dy))
			if d >= lo && d <= hi {
				ring = append(ring, [2]int{dx, dy})
			}
		}
	}
	f := map[int32]float32{}
	for _, path := range p.paths {
		for _, node := range path.nodes {
			l, x, y := gr.xy(int(node))
			for _, o := range ring {
				if xx, yy := x+o[0], y+o[1]; gr.in(xx, yy) {
					f[int32(gr.idx(l, xx, yy))] = 0.55
				}
			}
		}
	}
	return f
}

func netSpan(n *rnet) float64 {
	r := EmptyRect()
	for _, g := range n.groups {
		for _, p := range g {
			r = r.AddPoint(p.Box.C)
		}
	}
	return r.W() + r.H()
}

// negotiate is the PathFinder loop: route everything allowing overlap at a
// rising price, rip up only nets still in conflict, accumulate history on
// contested cells. Leftover conflicts are resolved strictly at the end.
func (r *router) negotiate(ctx context.Context, res *RouteResult) error {
	order := r.routeOrder()
	res.Stats.Nets = len(order)
	r.presFac = 0.6
	for _, n := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.routeNet(n)
	}
	if auditHook != nil {
		auditHook("initial", r)
	}
	it := 1
	var lastIter time.Duration
	for ; it < r.opt.MaxIters; it++ {
		c := r.conflicts()
		res.Stats.ConflictTrace = append(res.Stats.ConflictTrace, c)
		if c == 0 || time.Now().After(r.deadline) {
			break
		}
		// Stalled: two iterations without a 3 % gain on the best conflict
		// count. Negotiating on changes nothing, while legalisation — which
		// otherwise starts after the deadline and times out on every repair —
		// gets the time (RK3568: 176 → 177 → 176 → 174 over 3 min, 399
		// connections lost to timeouts).
		// Only when iterations are expensive (the last one took over a tenth of
		// the budget): cheap iterations keep finding gains after a plateau
		// (ESP32-S3: 34 conflicts for three rounds, then 30; stopping there
		// cost 10 points of completion).
		if tr := res.Stats.ConflictTrace; !negotiateNoStall && len(tr) >= 3 && lastIter > r.opt.Timeout/10 {
			best := tr[0]
			for _, v := range tr[:len(tr)-2] {
				best = min(best, v)
			}
			if float64(min(tr[len(tr)-1], tr[len(tr)-2])) > 0.97*float64(best) {
				res.Notes = append(res.Notes, sprintf("negotiation: stalled at %d conflicts (trace %v); legalising with the remaining time", c, tr))
				break
			}
		}
		// History: every over-used cell becomes more expensive for good.
		for i, u := range r.gr.use {
			if u > 1 {
				if r.gr.hist[i] == 0 {
					r.gr.bHist[r.gr.blockOf(i)]++
				}
				r.gr.hist[i] += 0.5 * float32(u-1)
			}
		}
		r.presFac *= 1.6
		iterStart := time.Now()
		for _, n := range order {
			if err := ctx.Err(); err != nil {
				return err
			}
			// Out of time: stop, do not rip up. Ripping up a net whose search
			// then returns "timeout" at once loses all its paths — the loop
			// used to do that to every conflicting net left in the iteration
			// (RK3568: 42.1 % → 45.4 % once it stopped).
			if time.Now().After(r.deadline) {
				break
			}
			if n.conflict {
				r.rerouteKeepOnTimeout(n, res)
			}
		}
		lastIter = time.Since(iterStart)
	}
	res.Stats.Iterations = it
	// Strict legalisation: lowest-priority conflicting nets are re-routed with
	// overlap forbidden; if impossible they are left unrouted (never shorted).
	r.strict = true
	for pass := 0; pass < 3 && r.conflicts() > 0; pass++ {
		victims := []*rnet{}
		for k := len(order) - 1; k >= 0; k-- {
			if order[k].conflict {
				victims = append(victims, order[k])
			}
		}
		for _, n := range victims {
			if !n.conflict {
				continue
			}
			r.repairPartial(n, true)
			r.conflicts()
		}
	}
	// Anything still conflicting is removed entirely: an unrouted net is
	// honest, a short is not.
	for _, n := range order {
		if r.conflicts() == 0 {
			break
		}
		if n.conflict {
			r.repairPartial(n, false)
		}
	}
	res.Stats.Conflicts = r.conflicts()
	r.strict = false
	res.Notes = append(res.Notes, sprintf("negotiation: legalisation dropped %d plane fan-outs that blocked signals", r.yielded))
	return nil
}

// floodCap bounds the target-side flood of provablyUnreachable: a pocket
// larger than this is left to the search.
const floodCap = 60000

// provablyUnreachable reports whether no path can exist from sources to
// targets on the whole board: a flood from the targets under relaxed rules
// (any turn, same node and via legality as search) exhausts its region
// without touching a source. Relaxed moves reach a superset of what search
// can, so a true result is a proof; a large or open region, a source in
// reach, or the deadline all give false and the search runs as before.
func (r *router) provablyUnreachable(n *rnet, sources []int32, targets map[int32]bool) bool {
	if noUnreachableProof {
		return false
	}
	gr := r.gr
	r.cur++ // fresh cost cache for this flood
	cur := r.cur
	for _, s := range sources {
		r.floodSrc[s] = cur
	}
	queue := r.floodQ[:0]
	defer func() { r.floodQ = queue[:0] }()
	for t := range targets {
		if r.floodSrc[t] == cur {
			return false
		}
		if math.IsInf(float64(r.cost(n, int(t))), 1) || r.floodSeen[t] == cur {
			continue
		}
		r.floodSeen[t] = cur
		queue = append(queue, t)
	}
	nl := len(gr.layers)
	for k := 0; k < len(queue); k++ {
		if len(queue) > floodCap || k&0xfff == 0 && time.Now().After(r.deadline) {
			return false
		}
		i := queue[k]
		l, x, y := gr.xy(int(i))
		visit := func(j int32) bool {
			if r.floodSeen[j] == cur {
				return true
			}
			if math.IsInf(float64(r.cost(n, int(j))), 1) {
				return true
			}
			if r.floodSrc[j] == cur {
				return false // a source is in reach
			}
			r.floodSeen[j] = cur
			queue = append(queue, j)
			return true
		}
		for d := 0; d < 8; d++ {
			xx, yy := x+dirs8[d][0], y+dirs8[d][1]
			if !gr.in(xx, yy) {
				continue
			}
			if !visit(int32(gr.idx(l, xx, yy))) {
				return false
			}
		}
		if nl > 1 && !math.IsInf(r.viaCost(n, x, y), 1) {
			for ll := 0; ll < nl; ll++ {
				if ll == l || !gr.routable[ll] {
					continue
				}
				if !visit(int32(gr.idx(ll, x, y))) {
					return false
				}
			}
		}
	}
	return true
}

// noUnreachableProof disables provablyUnreachable (A/B diagnostics).
var noUnreachableProof bool

// searchStats counts A* work by outcome (diagnostics).
type searchStats struct {
	okN, failN     int
	okExp, failExp int64
	proofs         int         // connections proven unreachable without a search
	stage          int         // window stage of routeNetKeep's current search, -1 otherwise
	byStage        [3][4]int64 // ok n, ok exp, fail n, fail exp
}

func (s *searchStats) add(exp int, found []int32) {
	if found != nil {
		s.okN++
		s.okExp += int64(exp)
	} else {
		s.failN++
		s.failExp += int64(exp)
	}
	if s.stage >= 0 && s.stage < 3 {
		k := 0
		if found == nil {
			k = 2
		}
		s.byStage[s.stage][k]++
		s.byStage[s.stage][k+1] += int64(exp)
	}
}

// noOwnerCache disables the nodeOK pad-owner summaries (A/B diagnostics).
var noOwnerCache bool

// negotiateNoStall disables the stall stop (A/B diagnostics).
var negotiateNoStall bool

// negotiateNoKeep disables rerouteKeepOnTimeout (A/B diagnostics).
var negotiateNoKeep bool

// rerouteKeepOnTimeout rips up and re-routes a conflicting net; when the
// deadline cuts the new search short and it connects less than before, the
// previous (conflicting) paths come back — legalisation can repair a
// conflict, but a ripped-up net that timed out is simply lost.
func (r *router) rerouteKeepOnTimeout(n *rnet, res *RouteResult) {
	if negotiateNoKeep {
		r.routeNet(n)
		return
	}
	paths := append([]rpath(nil), n.paths...)
	claims := append([]int32(nil), n.claims...)
	failed := append([]Unrouted(nil), n.failed...)
	r.routeNet(n)
	if !time.Now().After(r.deadline) || len(n.paths) >= len(paths) {
		return
	}
	r.applyClaims(n.claims, -1)
	n.paths, n.claims, n.failed = paths, claims, failed
	r.applyClaims(n.claims, +1)
	res.Stats.KeptOnTimeout++
}
