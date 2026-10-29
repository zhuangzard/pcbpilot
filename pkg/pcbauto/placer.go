package pcbauto

import (
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"
)

// PlaceOptions tune the placer.
type PlaceOptions struct {
	// Moves is the annealing budget per movable part (default 6000; the
	// deadline still bounds it, cooling tracks wall time).
	Moves int `json:"moves"`
	// Seed makes runs reproducible.
	Seed int64 `json:"seed"`
	// SpacingMil is the courtyard gap between parts (default: one routing
	// channel, track + 2 × clearance, at least 12).
	SpacingMil float64 `json:"spacingMil"`
	// NoRotate keeps rotations.
	NoRotate bool `json:"noRotate"`
	// Refine starts from the current placement instead of constructing one.
	Refine  bool          `json:"refine"`
	Timeout time.Duration `json:"-"`
	// Congestion weights the RUDY routing-congestion term during annealing
	// (0 = default 1, negative disables).
	Congestion float64 `json:"congestion,omitempty"`
	// Halo adds keep-clear distance (mil) around named parts — the place/route
	// loop inflates parts in regions the router failed.
	Halo map[string]float64 `json:"halo,omitempty"`
	// Macro enables the experimental two-stage placement (see freezeMacros):
	// each core is seated with its critical auxiliaries, the group anneals as
	// one rigid body, then everything is polished. Off by default: on the
	// 8-board A/B (2026-09-25) it lost to the single-stage anneal on 7 boards
	// (longer wiring, decaps and crystals further out) — freezing the cluster
	// before the global arrangement exists fixes the wrong local geometry.
	Macro bool `json:"macro,omitempty"`
	// Only restricts the movable set to these designators (with Refine: a
	// local adjustment of named parts on an otherwise confirmed layout).
	Only []string `json:"only,omitempty"`
	// Labels makes every part keep room for its designator (placer_label.go);
	// nil = off.
	Labels *LabelSpec `json:"labels,omitempty"`
	// NoCorridors places without the differential-pair corridor terms
	// (corridor intrusion, in-line part flow, access-path twists) — the
	// plain placement PlaceThenRoute and the place/route loop route next
	// to the corridor one, keeping the better (placer_corridor.go).
	NoCorridors bool `json:"noCorridors,omitempty"`
	// Aesthetics is the style profile of the placement aesthetics stage
	// (placer_aes.go): nil = DefaultAesProfile (balanced), Name "auto" =
	// chosen from the board. Soft objectives only: every aesthetic move is
	// judged against the safety, electrical and efficiency tiers.
	Aesthetics *AesProfile `json:"-"`
	// NoAesthetics skips the aesthetics stage (the frame search's trial
	// placements: they only test whether the parts fit).
	NoAesthetics bool `json:"noAesthetics,omitempty"`
	// TidyOnly keeps the current placement (a human or confirmed layout)
	// and runs only the aesthetics stage on it: no construction,
	// legalisation, annealing or polish, no new isolation strips.
	TidyOnly bool `json:"tidyOnly,omitempty"`
	// Progress, when set, is told each phase as it starts (construct,
	// legalise, anneal, polish, aesthetics, metrics …) with the run's seed.
	// It is called from the placement's own goroutine: PlaceSeeds runs
	// several at once, so it must be safe for concurrent use.
	Progress func(seed int64, phase string) `json:"-"`
	// Deadline bounds the whole placement in wall time (zero = none): the
	// anneal ends early enough to leave a quarter of the remaining time;
	// past the deadline legalisation, polish, the decap rescue and the
	// aesthetics stage stop where they are (aesthetics is skipped when it
	// would only start then). The result is the best so far, its metrics as
	// measured, and PlaceMetrics.TimeBudgetHit names the first phase the
	// deadline cut short ("aesthetics": the electrical placement finished).
	Deadline time.Time `json:"-"`
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
	// TetherExcessMil sums how far every tethered auxiliary sits beyond its
	// role's slack from the pin it serves (0 = every relation satisfied).
	TetherExcessMil float64 `json:"tetherExcessMil"`
	// CriticalExcessMil is the same sum over the physics-critical roles only
	// (decap, hot loop, bootstrap, power stage, clock, protection, power path).
	CriticalExcessMil float64 `json:"criticalExcessMil"`
	BoardAreaIn2      float64 `json:"boardAreaIn2"`
	PartAreaIn2       float64 `json:"partAreaIn2"`
	Utilisation       float64 `json:"utilisation"`
	// Corridors is the number of differential-pair corridors the placement
	// kept clear (0 with PlaceOptions.NoCorridors or no intent pairs).
	Corridors int `json:"corridors,omitempty"`
	// LabelBlocked counts parts with no free slot for their designator
	// (PlaceOptions.Labels; 0 when off).
	LabelBlocked int   `json:"labelBlocked,omitempty"`
	Millis       int64 `json:"millis"`
	// TimeBudgetHit is the first phase PlaceOptions.Deadline cut short
	// ("" = every phase ran to its end).
	TimeBudgetHit string `json:"timeBudgetHit,omitempty"`
}

// PlaceResult is the placer output.
type PlaceResult struct {
	Placements []Placement  `json:"placements"`
	Zones      []Zone       `json:"zones,omitempty"`
	Barriers   []Keepout    `json:"barrierKeepouts,omitempty"`
	Outline    []Point      `json:"outline,omitempty"` // set when auto-sized
	Metrics    PlaceMetrics `json:"metrics"`
	Notes      []string     `json:"notes,omitempty"`
	// Illegal names every illegal placement left (overlapping courtyard
	// pairs, parts outside the board or their zone); ZoneFill is each
	// placement zone's room.
	Illegal  []string   `json:"illegal,omitempty"`
	ZoneFill []ZoneFill `json:"zoneFill,omitempty"`
	// Aesthetics is what the aesthetics stage moved (nil when skipped).
	Aesthetics *AesPlaceReport `json:"aesthetics,omitempty"`
}

type pnet struct {
	pads   []*Pad
	weight float64
}

type placer struct {
	// lastMove is the pose tryMove saved for undo (per placer: concurrent
	// placements, PlaceBest, must not share it).
	lastMove savedPose
	hvReach  float64 // largest high-voltage net clearance (0 = no HV nets)
	b        *Board
	an       *Analysis
	c        *Circuit
	m        *Mechanics
	opt      PlaceOptions
	rng      *rand.Rand
	movable  []*Part
	// Two-stage placement: macro[core] are the critical auxiliaries frozen
	// to their core after stage 1; they move with it rigidly in the anneal.
	macro   map[*Part][]*Part
	nets    []*pnet
	partNet map[*Part][]int
	zoneOf  map[*Part]Rect // allowed centre region
	region  Rect           // placement region (board inset)
	// edgeReq is a part's own board-edge distance when one of its pads is
	// on a domain with an edge band (mains/hazardous: reinforced creepage to
	// an accessible edge) — more than the layer default region keeps.
	edgeReq  map[*Part]float64
	edgeBand map[*Part][]padBand // its pads with a band, each with the board inset it must stay in (precomputed: partCost is hot)
	decap    map[*Part]*Pad      // decap → the core power pad it serves
	servedBy map[string][]*Part  // core ref → its decaps, in board order
	tether   map[*Part]*tether   // auxiliary → the core pads it serves, by role
	conv     map[*Part]*Converter
	swPads   map[*Converter][]*Pad // switch-node pads, resolved once
	chains   map[*Part][]*SignalChain
	reserve  []portReserve   // connector pin-side strips kept for the port's own parts
	pairOf   map[*Part]*Part // same step of the other half of a diff pair
	hardOnly bool            // partCost: constraint terms only (legalisation)
	apart    [][2]*Part      // core pairs to keep apart (noisy vs sensitive)
	spacing  float64
	bucket   *bucketGrid
	boxes    map[*Part]Rect
	rudy     *rudy // congestion grid, live during annealing only
	intimate map[[2]*Part]bool
	// intimateOf[p] are p's intimate partners (intimate, indexed by part:
	// the overlap term asks once per part, not once per neighbour).
	intimateOf map[*Part]map[*Part]bool
	// netBuf is localCost's and groupCost's scratch net list (each uses it
	// to the end before the next call; the anneal calls them per move).
	netBuf []int
	strips []*Keepout // isolation strips this placement added to the board
	// corridors keep intent-declared differential pairs' runs clear of
	// foreign parts (placer_corridor.go).
	corridors []*pairCorridor
	// corridorShare counts the corridors each part is related to.
	corridorShare map[*Part]int
	// aes is the aesthetics stage's state while it runs (placer_aes.go);
	// aesHold are parts the stage must leave alone (the guard's rerun).
	aes     *aesState
	aesHold map[*Part]bool
	// aesStrict: the stage runs without the grid quantisation allowance.
	aesStrict bool
	// wallClock: the anneal ran past 80 % of its time budget, where cooling
	// follows the clock — the placement then depends on machine load.
	wallClock bool
	// curPhase is the phase running now; budgetHit the first one the
	// PlaceOptions.Deadline cut short.
	curPhase, budgetHit string
}

// intimateGap is the courtyard gap kept between parts that connect directly
// and must not be split by foreign tracks: a hot-loop cap and its IC, a decap
// and its pin, a crystal and its loads. Everyone else keeps a routing channel.
const intimateGap = 12

const placeBucket = 150.0

// Place decides poses for every movable part.
func Place(b *Board, an *Analysis, c *Circuit, m *Mechanics, opt PlaceOptions) (*PlaceResult, error) {
	start := time.Now()
	if opt.Moves <= 0 {
		opt.Moves = 6000
	}
	if opt.SpacingMil <= 0 {
		// One routing channel between neighbours: a track and its clearance
		// on both sides. Tighter than this and a pad facing into a cluster
		// can only escape around it — the routed MIPI adapter lost 30 % of
		// its connections at the old 12 mil default with 6/6 rules.
		opt.SpacingMil = math.Max(12, b.Rules.TrackWidth+2*b.Rules.Clearance)
		// Dense boards cannot afford a channel beside every part: the
		// routes there come from vias to inner layers, not gaps. Keep the
		// courtyards within ~85 % of each side's area.
		if fit := fitSpacing(b); fit < opt.SpacingMil {
			opt.SpacingMil = math.Max(intimateGap, fit)
		}
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 90 * time.Second
	}
	if m == nil {
		m = &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}}
	}
	if !labelSpecValid(opt.Labels) {
		opt.Labels = nil
	}
	pl := &placer{b: b, an: an, c: c, m: m, opt: opt, rng: rand.New(rand.NewSource(opt.Seed + 1)),
		partNet: map[*Part][]int{}, zoneOf: map[*Part]Rect{}, decap: map[*Part]*Pad{}, spacing: opt.SpacingMil}
	res := &PlaceResult{}
	pl.phase("setup")
	pl.setup(res)
	res.Metrics.StartWireIn = pl.wirelength() / 1000
	if !opt.TidyOnly {
		if !opt.Refine {
			pl.phase("construct")
			pl.construct()
		}
		pl.phase("legalise")
		pl.legalise()
		if opt.Macro && len(opt.Only) == 0 {
			// Stage 1: seat every critical auxiliary at its pin, then freeze it
			// to its core. Stage 2 anneals macros + loose parts only, so a
			// crystal or decap can no longer be dragged away from its pin (the
			// szpi crystal ended 36 mm from its hub IC when it annealed alone).
			pl.freezeMacros()
		}
		pl.phase("anneal")
		d, short := pl.annealDeadline(start)
		if pl.anneal(d) && short && pl.budgetHit == "" {
			// It stopped on the budget's clock before its moves were done.
			pl.budgetHit = "anneal"
		}
		if pl.wallClock {
			res.Notes = append(res.Notes, "annealing ran past 80 % of its time budget: cooling followed the clock, so this placement depends on machine load (not reproducible move for move)")
		}
		pl.unfreezeMacros()
		// Stage 3: local fine-tune in the gaps the macros left.
		pl.phase("legalise-2")
		pl.legalise()
		pl.phase("polish")
		pl.polish()
		pl.phase("rescue")
		pl.rescueDecaps()
		pl.phase("tidy")
		pl.tidyV07()
		pl.phase("resolve")
		if n := pl.resolveOverlaps(); n > 0 {
			res.Notes = append(res.Notes, sprintf("resolve: %d courtyard overlap(s) the legaliser left cleared by moving a part within its own zone (no isolation, keep-out or edge term worse)", n))
		}
	}
	if !opt.NoAesthetics && pl.overBudget() {
		pl.phase("aesthetics")
		res.Notes = append(res.Notes, "time budget reached: aesthetics stage skipped")
	} else if !opt.NoAesthetics {
		// The placement before the aesthetics stage: the routed guard
		// (placeab.go) falls back to it when the aesthetic one routes worse.
		pl.phase("aesthetics")
		raw := pl.aesSnapshot(res, nil)
		prof := pl.resolveAesProfile()
		// The lighter stages from the same start, the guard's next choices
		// when the full stage routes worse: grid-only (functional: fold +
		// snap, no slack), then grid-only without the snap's quantisation
		// allowance (every tier strictly no worse).
		var lites []*aesPoses
		fp, _ := AesProfileByName("functional")
		for _, strict := range []bool{false, true} {
			if !strict && len(aesPasses(prof)) <= 2 {
				continue // the full stage is the grid-only one
			}
			pl.aesStrict = strict
			var lr PlaceResult
			pl.aesthetics(&lr, fp)
			pl.aesStrict = false
			if lr.Aesthetics.Moved > 0 {
				lr.Aesthetics.Strict = strict
				lites = append(lites, pl.aesSnapshot(res, lr.Aesthetics))
			}
			raw.apply(b)
		}
		pl.aesthetics(res, prof)
		ar := res.Aesthetics
		ar.raw, ar.lites = raw, lites
		full := map[*Part]savedPart{}
		for _, p := range b.Parts {
			full[p] = savedPart{p.Pos, p.Rotation}
		}
		base := res.Metrics
		ar.rerun = func(hold map[*Part]bool) *aesPoses {
			raw.apply(b)
			pl.aesHold = hold
			var r PlaceResult
			r.Metrics = base
			pl.aesthetics(&r, prof)
			pl.aesHold = nil
			return pl.aesSnapshot(&r, r.Aesthetics)
		}
		for p, ps := range full {
			p.MoveTo(ps.pos, ps.rot)
		}
	}
	if m.AutoSize {
		res.Outline = pl.autosize()
	}
	if !opt.TidyOnly && !opt.NoAesthetics && len(pl.overlapPairs()) > 0 {
		// The aesthetics stage's moves are judged on the legaliser's terms,
		// which price a small courtyard overlap instead of forbidding it.
		pl.phase("resolve-2")
		if n := pl.resolveOverlaps(); n > 0 {
			res.Notes = append(res.Notes, sprintf("resolve after aesthetics: %d courtyard overlap(s) cleared", n))
		}
	}
	pl.phase("metrics")
	pl.metrics(res)
	pairs := pl.overlapPairs()
	res.Illegal = pl.illegalList(pairs)
	res.ZoneFill = pl.zoneFill(res, pairs)
	for _, z := range res.ZoneFill {
		switch {
		case z.Fill > 1:
			res.Notes = append(res.Notes, sprintf("zone %s is over-full: its parts need %.0f mm² (fixed parts %.0f), the zone offers %.0f mm² (%d illegal) — enlarge the zone or move parts out of it", z.Domain, z.NeedMm2, z.FixedMm2, z.OfferMm2, z.Illegal))
		case z.Fill > ZoneTightFill && z.Illegal > 0:
			res.Notes = append(res.Notes, sprintf("zone %s is practically full: its parts need %.0f mm² (fixed parts %.0f) of the %.0f mm² it offers (fill %.2f, %d illegal) — enlarge the zone or move parts out of it", z.Domain, z.NeedMm2, z.FixedMm2, z.OfferMm2, z.Fill, z.Illegal))
		case z.Illegal > 0:
			res.Notes = append(res.Notes, sprintf("zone %s: %d illegal placement(s) although it has room (parts need %.0f mm² of %.0f mm², fill %.2f): a local conflict (isolation, keep-out or a fixed part) the placer could not clear — see illegal", z.Domain, z.Illegal, z.NeedMm2, z.OfferMm2, z.Fill))
		}
	}
	if pl.budgetHit != "" {
		res.Metrics.TimeBudgetHit = pl.budgetHit
		res.Notes = append(res.Notes, sprintf("time budget reached during %s: this is the best placement found by then, not a finished one (metrics as measured)", pl.budgetHit))
	}
	pl.carveStrips(res)
	for _, p := range b.Parts {
		res.Placements = append(res.Placements, Placement{Ref: p.Ref, ID: p.ID, X: round2(p.Pos.X), Y: round2(p.Pos.Y),
			Rot: p.Rotation, Side: p.Side, Fixed: p.Fixed, Block: c.BlockOf[p.Ref]})
	}
	res.Metrics.Millis = time.Since(start).Milliseconds()
	if ar := res.Aesthetics; ar != nil && ar.Moved == 0 {
		ar.raw, ar.lites, ar.rerun = nil, nil, nil // nothing to fall back to
	}
	pl.phase("done")
	return res, nil
}

// phase reports the phase starting now (PlaceOptions.Progress).
func (pl *placer) phase(name string) {
	pl.curPhase = name
	if pl.opt.Progress != nil {
		pl.opt.Progress(pl.opt.Seed, name)
	}
}

// overBudget reports whether PlaceOptions.Deadline has passed, and
// records the phase it first cut short.
func (pl *placer) overBudget() bool {
	if pl.opt.Deadline.IsZero() || time.Now().Before(pl.opt.Deadline) {
		return false
	}
	if pl.budgetHit == "" {
		pl.budgetHit = pl.curPhase
	}
	return true
}

// annealDeadline is the anneal's end: Timeout after the placement started.
// When legalisation alone took longer (a 1500-part board), that moment is
// already past — the anneal then had no clock at all and ran its full move
// count, tens of minutes — so it gets a third of Timeout from now instead.
// Either way it leaves a quarter of the time to PlaceOptions.Deadline for
// the phases after it; the flag says the budget shortened it.
func (pl *placer) annealDeadline(start time.Time) (time.Time, bool) {
	now := time.Now()
	d := start.Add(pl.opt.Timeout)
	if !d.After(now) {
		d = now.Add(pl.opt.Timeout / 3)
	}
	if dl := pl.opt.Deadline; !dl.IsZero() {
		if last := dl.Add(-dl.Sub(now) / 4); last.Before(d) {
			return last, true
		}
	}
	return d, false
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
	// Bodies cover their pads: keeping a body the outer board-edge distance
	// in keeps its pads there (the spacing margin usually covers it).
	pol := pl.an.edgePolicy(b)
	base := math.Max(b.Rules.EdgeClearance+pl.spacing, pol.LayerReq(LayerTop))
	pl.region = bb.Expand(-base)
	pl.edgeReq, pl.edgeBand = map[*Part]float64{}, map[*Part][]padBand{}
	for _, p := range b.Parts {
		if r := partEdgeReq(p, pol); r > base {
			pl.edgeReq[p] = r
			// Per pad: a bridge (opto, transformer, Y cap) keeps the band on
			// its hazardous pads only, its SELV side may sit nearer the edge.
			for _, pd := range p.Pads {
				l := pd.Layer
				if l == LayerMulti {
					l = LayerTop
				}
				if req := pol.Req(l, pd.Net); req > base {
					pl.edgeBand[p] = append(pl.edgeBand[p], padBand{pd: pd, in: bb.Expand(-req)})
				}
			}
		}
	}
	only := map[string]bool{}
	for _, r := range pl.opt.Only {
		only[r] = true
	}
	for _, p := range b.Parts {
		if !p.Fixed && pl.c.Kinds[p.Ref] != KindMechanical && (len(only) == 0 || only[p.Ref]) {
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
		// A net whose current is known (simulated or declared) is IR drop
		// per mil: weight it by that current (the drop gate is I·R, so
		// linear). Board-B V5 B weighed its 0.45 A valve drains like a
		// 5 mA rail, the drain loops spread and failed the 30 mV drop
		// gate. Heuristic rail currents stay out: they are guesses.
		if np.Role != RoleGround && (np.Source == "simulated" || np.Source == "declared") && np.CurrentA >= 0.2 {
			w = math.Max(w, math.Min(1+4*np.CurrentA, 6))
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
	pl.setupIntimate()
	pl.pinAccessHalo()
	for _, np := range pl.an.Nets {
		if np.ClearanceMil > pl.b.Rules.Clearance+hvExcessMil {
			pl.hvReach = math.Max(pl.hvReach, np.ClearanceMil)
		}
	}
	if pl.opt.TidyOnly {
		// A kept placement keeps its own zones: no new isolation strips.
		for _, p := range pl.movable {
			pl.zoneOf[p] = pl.region
		}
		return
	}
	pl.zones(res)
}

// pairOverlap is the courtyard overlap of p and q, measured with the tight
// gap for intimate pairs and the channel gap (plus halos) otherwise.
func (pl *placer) pairOverlap(p, q *Part) float64 {
	if pl.intimate[[2]*Part{p, q}] {
		return p.Body().Expand(intimateGap / 2).OverlapArea(q.Body().Expand(intimateGap / 2))
	}
	return pl.boxes[p].OverlapArea(pl.boxes[q])
}

var intimateRoles = map[string]bool{"hot-loop": true, "bootstrap": true, "decap": true, "clock": true, "clock-load": true, "power-stage": true}

func (pl *placer) setupIntimate() {
	pl.intimate = map[[2]*Part]bool{}
	pl.intimateOf = map[*Part]map[*Part]bool{}
	pair := func(a, b *Part) {
		if a != nil && b != nil && a != b {
			pl.intimate[[2]*Part{a, b}] = true
			pl.intimate[[2]*Part{b, a}] = true
			for _, e := range [][2]*Part{{a, b}, {b, a}} {
				if pl.intimateOf[e[0]] == nil {
					pl.intimateOf[e[0]] = map[*Part]bool{}
				}
				pl.intimateOf[e[0]][e[1]] = true
			}
		}
	}
	for _, bl := range pl.c.Blocks {
		core := pl.b.Part(bl.Core)
		var inner []*Part
		for _, m := range bl.Members {
			if intimateRoles[m.Role] {
				p := pl.b.Part(m.Ref)
				pair(p, core)
				inner = append(inner, p)
			}
		}
		// A converter's hot loop, inductor and bootstrap sit on one another.
		for i, a := range inner {
			for _, b := range inner[i+1:] {
				if role(pl, a) != "decap" && role(pl, b) != "decap" {
					pair(a, b)
				}
			}
		}
	}
}

func role(pl *placer, p *Part) string {
	if t := pl.tether[p]; t != nil {
		return t.role
	}
	return ""
}

// pinAccessHalo widens the keep-clear of parts whose pads carry nets wider
// than a default track, so the wider track can still leave the pad: half the
// extra width on each side. Caller-supplied halos (the place/route loop) add.
func (pl *placer) pinAccessHalo() {
	halo := map[string]float64{}
	for k, v := range pl.opt.Halo {
		halo[k] = v
	}
	base := pl.b.Rules.TrackWidth
	for _, p := range pl.b.Parts {
		extra, hv := 0.0, 0.0
		for _, pd := range p.Pads {
			if pd.Net == "" {
				continue
			}
			np := pl.an.Plan(pd.Net, pl.b.Rules)
			// A high-voltage net needs its IPC-2221B clearance to every other
			// net's copper: the parts it sits on keep half the excess over the
			// board rule each (the neighbour keeps the other half), or its
			// track cannot pass between them (flyback stress board: the bulk
			// and drain nets were unroutable between tightly packed parts).
			hv = math.Max(hv, (np.ClearanceMil-pl.b.Rules.Clearance)/2)
			if np.Plane || np.Role == RoleGround {
				continue // reaches its plane through a fan-out via
			}
			extra = math.Max(extra, (np.WidthMil-base)/2)
		}
		if extra > 0 {
			halo[p.Ref] += math.Min(extra, 20)
		}
		if hv > hvExcessMil/2 {
			halo[p.Ref] += math.Min(hv, 100)
		}
	}
	pl.opt.Halo = halo
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
	// A re-placement (place/route loop pass) replaces the previous pass's
	// isolation strips instead of stacking another copy.
	ks := b.Keepouts[:0]
	for _, k := range b.Keepouts {
		if !strings.HasPrefix(k.Name, isoStripPrefix) {
			ks = append(ks, k)
		}
	}
	b.Keepouts = ks
	stripOf := map[[2]string]Rect{}
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
		ws, ds := make([]float64, len(ks)), make([]*Domain, len(ks))
		for i, k := range ks {
			ws[i], ds[i] = (pl.region.W()-pl.gapTotal())*k.area/total, k.d
		}
		pl.sizeZonesByOccupancy(ds, ws)
		if note := pl.widenZones(ds, ws); note != "" {
			res.Notes = append(res.Notes, note)
		}
		x := pl.region.MinX
		for i, k := range ks {
			gap := 0.0
			if i+1 < len(ks) {
				gap = pl.barrierGap(k.d.ID, ks[i+1].d.ID)
			}
			w := ws[i]
			r := Rect{x, pl.region.MinY, x + w, pl.region.MaxY}
			userZone[k.d.ID] = r
			res.Zones = append(res.Zones, Zone{Domain: k.d.ID, Rect: r})
			if gap > 0 {
				strip := Rect{x + w, b.Bounds().MinY, x + w + gap, b.Bounds().MaxY}
				ko := Keepout{Name: isoStripPrefix + k.d.ID + "|" + ks[i+1].d.ID, Poly: strip.Corners(), NoCopper: true, NoVias: true}
				b.Keepouts = append(b.Keepouts, &ko)
				pl.strips = append(pl.strips, &ko)
				res.Barriers = append(res.Barriers, ko)
				stripOf[isoKey(k.d.ID, ks[i+1].d.ID)] = strip
				res.Notes = append(res.Notes, sprintf("isolation strip %.0f mil wide between %s and %s (no copper; bridges straddle it)", gap, k.d.ID, ks[i+1].d.ID))
			}
			x += w + gap
		}
	}
	for _, p := range pl.movable {
		d := c.DomainOf[p.Ref]
		if r, ok := userZone[d]; ok && !c.IsBridge(p.Ref) {
			pl.zoneOf[p] = r
			continue
		}
		pl.zoneOf[p] = pl.region
		if c.IsBridge(p.Ref) && len(res.Barriers) > 0 {
			// A bridge sits centred on the strip of its own barrier.
			s := PolyBounds(res.Barriers[0].Poly)
			for _, br := range c.Barriers {
				if r, ok := stripOf[isoKey(br.A, br.B)]; ok && containsStr(br.Bridges, p.Ref) {
					s = r
					break
				}
			}
			pl.zoneOf[p] = Rect{s.Center().X - 1, pl.region.MinY, s.Center().X + 1, pl.region.MaxY}
		}
	}
}

// zoneOccupied is the room a strip must hold (mil²): the courtyards plus
// half the placement spacing of its domain's movable parts (bridges sit on
// the isolation strip), and of every part that does not move (fixed,
// mechanical) and every bridge — any domain — as far as it lies inside
// the strip, plus the keep-outs and holes in it.
// It is zoneFill's need plus the area zoneFill's offer leaves out, so a
// strip sized by it reports the fill its neighbours report.
func (pl *placer) zoneOccupied(d *Domain, strip Rect, movable map[*Part]bool) float64 {
	occ := 0.0
	for _, ref := range d.Parts {
		if p := pl.b.Part(ref); p != nil && movable[p] && !pl.c.IsBridge(ref) {
			occ += p.Body().Expand(pl.spacing / 2).Area()
		}
	}
	r := pl.region
	in := Rect{math.Max(strip.MinX, r.MinX), math.Max(strip.MinY, r.MinY), math.Min(strip.MaxX, r.MaxX), math.Min(strip.MaxY, r.MaxY)}
	for _, q := range pl.b.Parts {
		if !movable[q] || pl.c.IsBridge(q.Ref) {
			occ += in.OverlapArea(q.Body().Expand(pl.spacing / 2))
		}
	}
	for _, k := range pl.b.Keepouts {
		if k.NoParts || k.NoCopper {
			occ += in.OverlapArea(PolyBounds(k.Poly))
		}
	}
	for _, h := range pl.b.Holes {
		if h.Owner == "" {
			occ += in.OverlapArea(h.Bounds().Expand(h.Keep))
		}
	}
	return occ
}

// sizeZonesByOccupancy re-splits the strip widths ws (same total) in
// proportion to what each strip must hold (zoneOccupied) instead of its
// domain's part area alone: fixed mounting holes and mechanically placed
// edge connectors take a strip's room without being its domain's parts —
// on a 1443-part board one strip ended 96 % full with overlaps the
// legaliser could not clear. Strip positions follow the widths, so the
// split is refined a few times. A board with no fixed part, bridge,
// keep-out or hole keeps its area-split widths unchanged.
func (pl *placer) sizeZonesByOccupancy(doms []*Domain, ws []float64) {
	movable := map[*Part]bool{}
	for _, p := range pl.movable {
		movable[p] = true
	}
	extra := false
	for _, q := range pl.b.Parts {
		if !movable[q] || pl.c.IsBridge(q.Ref) {
			extra = true
			break
		}
	}
	extra = extra || len(pl.b.Holes) > 0
	for _, k := range pl.b.Keepouts {
		extra = extra || k.NoParts || k.NoCopper
	}
	if !extra {
		return
	}
	total := 0.0
	for _, w := range ws {
		total += w
	}
	occ := make([]float64, len(ws))
	for it := 0; it < 4; it++ {
		x, sum := pl.region.MinX, 0.0
		for i, d := range doms {
			occ[i] = pl.zoneOccupied(d, Rect{x, pl.region.MinY, x + ws[i], pl.region.MaxY}, movable)
			sum += occ[i]
			x += ws[i]
			if i+1 < len(doms) {
				x += pl.barrierGap(d.ID, doms[i+1].ID)
			}
		}
		if sum <= 0 {
			return
		}
		for i := range ws {
			ws[i] = total * occ[i] / sum
		}
	}
}

// widenZones makes every domain's strip (widths ws, split by part area) at
// least as wide as the narrow side of its widest non-bridge part's box: a
// small domain holding a big connector got a strip narrower than the
// connector (183 mil for a 433 mil header on a 1443-part board), so the
// part could never be legal and every legalisation pass searched its
// whole spiral for nothing. The width comes from the other strips in
// proportion to what they have beyond their own need; widths already wide
// enough are left exactly as they were, and the widths' sum never changes.
// When the others' slack cannot cover every shortfall, each short strip
// gets its share of the slack and the returned note says the region is too
// narrow ("" when every strip fits its widest part).
func (pl *placer) widenZones(doms []*Domain, ws []float64) string {
	need := make([]float64, len(doms))
	short := false
	for i, d := range doms {
		for _, ref := range d.Parts {
			p := pl.b.Part(ref)
			if p == nil || p.Fixed || pl.c.IsBridge(ref) || pl.c.Kinds[ref] == KindMechanical {
				continue
			}
			bx := pl.box(p)
			need[i] = math.Max(need[i], math.Min(bx.W(), bx.H())+1)
		}
		short = short || ws[i] < need[i]
	}
	if !short {
		return ""
	}
	deficit, slack := 0.0, 0.0
	for i := range ws {
		if ws[i] < need[i] {
			deficit += need[i] - ws[i]
		} else {
			slack += ws[i] - need[i]
		}
	}
	// give: the share of each shortfall granted; take: the share of each
	// slack given up. Both ≤ 1, and give·deficit = take·slack.
	give, take := 1.0, 1.0
	if deficit <= slack {
		take = deficit / slack
	} else {
		give = slack / deficit // slack 0: nothing to give
	}
	for i := range ws {
		if ws[i] < need[i] {
			ws[i] += (need[i] - ws[i]) * give
		} else {
			ws[i] -= (ws[i] - need[i]) * take
		}
	}
	if give >= 1 {
		return ""
	}
	var short2 []string
	for i, d := range doms {
		if ws[i] < need[i]-1e-6 {
			short2 = append(short2, sprintf("%s (%.0f of %.0f mil)", d.ID, ws[i], need[i]))
		}
	}
	return sprintf("domain zones too narrow: the board's placement width cannot give every domain strip its widest part — %s; enlarge the board or move the wide parts to another domain", strings.Join(short2, ", "))
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

func (pl *placer) box(p *Part) Rect {
	return p.Body().Expand(pl.spacing/2 + pl.opt.Halo[p.Ref])
}

// partCost is the constraint cost of one part in its current pose,
// excluding net length (overlap with others, zone, board, keepouts, height).
func (pl *placer) partCost(p *Part) float64 { return pl.partCostLim(p, math.Inf(1)) }

// partCostLim is partCost, except that with hardOnly set it may stop
// summing once the sum reaches lim and return that partial sum: every hard
// term is ≥ 0, so a caller asking "cost < lim?" gets partCost's answer.
// Below lim the sum is partCost's, term for term in the same order.
func (pl *placer) partCostLim(p *Part, lim float64) float64 {
	if !pl.hardOnly {
		lim = math.Inf(1)
	}
	bx := pl.boxes[p]
	// Overlap with neighbours (bucketed).
	cost := pl.overlapCost(p, bx, nil, lim)
	if cost >= lim {
		return cost
	}
	// Board / zone containment of the body.
	z := pl.zoneOf[p]
	if !p.Fixed {
		cost += 20 * outside(bx, pl.region)
		// Domain edge band: the band is a copper-to-edge distance, so it
		// holds the pads (a terminal's body may overhang toward the edge).
		if bs := pl.edgeBand[p]; len(bs) > 0 {
			// Per mil of shortfall, 1/20 of the isolation weight (isoCost
			// 800/mil): the legaliser must never buy band by giving up the
			// barrier between domains (inverter/flyback stress lost 1–8 mil
			// of creepage at equal weights); a band remainder it cannot clear
			// stays visible to pcb check copper-to-edge.
			cost += 40 * padsOutside(bs)
		}
		cen := bx.Center()
		dx := math.Max(0, math.Max(z.MinX-cen.X, cen.X-z.MaxX))
		dy := math.Max(0, math.Max(z.MinY-cen.Y, cen.Y-z.MaxY))
		cost += 30 * (dx + dy) * math.Max(bx.W(), bx.H())
		// Zone body containment for non-bridges keeps HV parts wholly in zone.
		if !pl.c.IsBridge(p.Ref) && z != pl.region {
			cost += 20 * outside(bx, z)
		}
	}
	for _, k := range pl.b.Keepouts {
		if (k.NoParts || k.NoCopper) && k.Owner != p.Ref {
			kb := PolyBounds(k.Poly)
			if pl.c.IsBridge(p.Ref) && !k.NoParts {
				continue // bridges straddle isolation strips by design
			}
			cost += 20 * bx.OverlapArea(kb)
		}
	}
	for _, h := range pl.b.Holes {
		if h.Owner == p.Ref {
			continue // a footprint's own locating holes sit inside its body
		}
		hr := h.Bounds().Expand(h.Keep)
		cost += 20 * bx.OverlapArea(hr)
	}
	for _, hz := range pl.m.HeightZones {
		if p.Height > hz.MaxHeight {
			cost += 20 * bx.OverlapArea(hz.Rect)
		}
	}
	if cost >= lim {
		return cost
	}
	cost += pl.isoCost(p, bx)
	if cost >= lim {
		return cost
	}
	cost += pl.hvPadCost(p, bx)
	if !pl.hardOnly {
		cost += pl.tetherCost(p)
		cost += pl.converterCost(p)
		cost += pl.chainCost(p)
		cost += pl.corridorCost(p)
		cost += pl.pairFlowCost(p)
		cost += pl.reserveCost(p)
		cost += pl.labelCost(p)
		for _, pr := range pl.apart {
			if pr[0] == p || pr[1] == p {
				d := pr[0].Body().Center().Dist(pr[1].Body().Center())
				cost += 3 * math.Max(0, keepApartMil-d)
			}
		}
	}
	return cost
}

// overlapCost is partCost's neighbour term for p with box bx and body
// body (nil: p.Body(), read only when an intimate neighbour needs it): 8 ×
// the courtyard overlap with every colliding part in the buckets, summed in
// bucket order. It stops once the sum reaches lim.
func (pl *placer) overlapCost(p *Part, bx Rect, body *Rect, lim float64) float64 {
	return pl.overlapCostIn(p, bx, body, pl.intimateOf[p], lim)
}

// overlapCostIn is overlapCost with p's intimate partners given.
func (pl *placer) overlapCostIn(p *Part, bx Rect, body *Rect, intimate map[*Part]bool, lim float64) float64 {
	cost := 0.0
	pl.forBucketsOnce(bx, func(q *Part) bool {
		if q == p || !collide(p, q) {
			return true
		}
		if intimate[q] {
			pb := body
			if pb == nil {
				r := p.Body()
				pb = &r
			}
			cost += 8 * pb.Expand(intimateGap/2).OverlapArea(q.Body().Expand(intimateGap/2))
		} else {
			cost += 8 * bx.OverlapArea(pl.boxes[q])
		}
		return cost < lim
	})
	return cost
}

// boundRejects reports whether a pose whose hard cost is at least lb can be
// skipped by a search that accepts only cost < lim. hardBound sums a subset
// of partCost's non-negative terms in partCost's order, and rounding to
// nearest is monotone, so lb ≤ cost exactly; the margin (relative 1e-9,
// absolute 1e-9 mil-cost) only prunes less, never more — a pose within it
// of the limit goes to the full evaluation the search always did.
func boundRejects(lb, lim float64) bool {
	return lb >= lim+1e-9*math.Abs(lim)+1e-9
}

// spiralCtx is what hardBound needs of the part a spiral moves, read once
// per spiral: nothing in it changes while the part searches (the other
// parts, keep-outs and holes stay put).
type spiralCtx struct {
	p           *Part
	z           Rect
	zoneBody    bool   // the zone's body containment term applies
	keepouts    []Rect // in Board.Keepouts order, the ones partCost counts for p
	holes       []Rect // in Board.Holes order, other parts' holes with their keep
	heightZones []Rect
	intimate    map[*Part]bool
}

func (pl *placer) newSpiralCtx(p *Part) *spiralCtx {
	sc := &spiralCtx{p: p, z: pl.zoneOf[p], intimate: pl.intimateOf[p]}
	bridge := pl.c.IsBridge(p.Ref)
	sc.zoneBody = !bridge && sc.z != pl.region
	for _, k := range pl.b.Keepouts {
		if (k.NoParts || k.NoCopper) && k.Owner != p.Ref {
			kb := PolyBounds(k.Poly)
			if bridge && !k.NoParts {
				continue
			}
			sc.keepouts = append(sc.keepouts, kb)
		}
	}
	for _, h := range pl.b.Holes {
		if h.Owner != p.Ref {
			sc.holes = append(sc.holes, h.Bounds().Expand(h.Keep))
		}
	}
	for _, hz := range pl.m.HeightZones {
		if p.Height > hz.MaxHeight {
			sc.heightZones = append(sc.heightZones, hz.Rect)
		}
	}
	return sc
}

// zoneTerms are partCost's board and zone containment terms of a box bx
// (a movable part), summed in partCost's order without the pad band between
// them. They feed hardBound only, whose callers compare: plain comparisons
// stand in for math.Max (equal but for NaN and the sign of a zero).
func (pl *placer) zoneTerms(sc *spiralCtx, bx Rect) (t1, t3, t4 float64) {
	z := sc.z
	t1 = 20 * fastOutside(bx, pl.region)
	cx, cy := (bx.MinX+bx.MaxX)/2, (bx.MinY+bx.MaxY)/2
	dx := fmax(0, fmax(z.MinX-cx, cx-z.MaxX))
	dy := fmax(0, fmax(z.MinY-cy, cy-z.MaxY))
	t3 = 30 * (dx + dy) * fmax(bx.MaxX-bx.MinX, bx.MaxY-bx.MinY)
	if sc.zoneBody {
		t4 = 20 * fastOutside(bx, z)
	}
	return
}

func fmax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// fastOutside is outside for hardBound (see zoneTerms).
func fastOutside(bx, r Rect) float64 {
	return fmax(0, bx.MaxX-bx.MinX)*fmax(0, bx.MaxY-bx.MinY) - bx.OverlapArea(r)
}

// hardBound is a lower bound of the hard cost (hardCost) of the spiral's
// part were its box bx and its body body: the terms that need only the box,
// summed in partCost's order. The omitted ones (pad edge bands, isolation
// and HV pad gaps) are ≥ 0 and float addition of non-negative terms is
// monotone, so the bound never exceeds partCost's sum. It returns as soon
// as a partial sum (the containment terms alone first: no neighbour
// search) reaches lim.
func (pl *placer) hardBound(sc *spiralCtx, bx, body Rect, lim float64) float64 {
	p := sc.p
	var t1, t3, t4 float64
	if !p.Fixed {
		t1, t3, t4 = pl.zoneTerms(sc, bx)
		if z := t1 + t3 + t4; z >= lim {
			return z
		}
	}
	cost := pl.overlapCostIn(p, bx, &body, sc.intimate, lim)
	if cost >= lim {
		return cost
	}
	if !p.Fixed {
		cost += t1
		cost += t3
		cost += t4
	}
	for _, kb := range sc.keepouts {
		cost += 20 * bx.OverlapArea(kb)
	}
	for _, hr := range sc.holes {
		cost += 20 * bx.OverlapArea(hr)
	}
	for _, hz := range sc.heightZones {
		cost += 20 * bx.OverlapArea(hz)
	}
	return cost
}

// outside is the body area outside r.
// padBand is one pad that must stay inside `in` (the board inset by the
// pad's domain edge distance).
type padBand struct {
	pd *Pad
	in Rect
}

// padsOutside is how far (mil, summed over pads) the banded pads reach past
// their insets — 0 when every pad keeps its domain edge band.
func padsOutside(bs []padBand) float64 {
	out := 0.0
	for _, b := range bs {
		pb, r := b.pd.Box.Bounds(), b.in
		out += math.Max(0, math.Max(r.MinX-pb.MinX, pb.MaxX-r.MaxX)) + math.Max(0, math.Max(r.MinY-pb.MinY, pb.MaxY-r.MaxY))
	}
	return out
}

// partEdgeReq is the largest board-edge distance any pad of p needs.
func partEdgeReq(p *Part, pol *EdgePolicy) float64 {
	req := 0.0
	for _, pd := range p.Pads {
		l := pd.Layer
		if l == LayerMulti {
			l = LayerTop
		}
		req = math.Max(req, pol.Req(l, pd.Net))
	}
	return req
}

func outside(bx, r Rect) float64 {
	return bx.Area() - bx.OverlapArea(r)
}

// bucketEntry is a part in a placement bucket, with the first bucket
// (lowest x, then lowest y) of the range it was filed under.
type bucketEntry struct {
	p      *Part
	x0, y0 int
}

// bucketGrid files parts by placeBucket cell: a dense array over the board
// and a margin around it (parts off the board — a pile next to it — included),
// a map for cells beyond. The legaliser queries it millions of times; a map
// of every cell spent most of that time hashing.
type bucketGrid struct {
	x0, y0, nx, ny int
	cells          [][]bucketEntry
	far            map[[2]int]*[]bucketEntry
}

const bucketGridMargin = 20000.0 // mil around the board kept dense

func newBucketGrid(bb Rect) *bucketGrid {
	g := &bucketGrid{far: map[[2]int]*[]bucketEntry{}}
	if bb.Empty() || bb.W() > 1e6 || bb.H() > 1e6 {
		return g // no sane outline: every cell in the map
	}
	x0, y0, x1, y1 := bucketRange(bb.Expand(bucketGridMargin))
	g.x0, g.y0, g.nx, g.ny = x0, y0, x1-x0+1, y1-y0+1
	g.cells = make([][]bucketEntry, g.nx*g.ny)
	return g
}

func (g *bucketGrid) index(x, y int) int {
	if x < g.x0 || y < g.y0 || x >= g.x0+g.nx || y >= g.y0+g.ny {
		return -1
	}
	return (x-g.x0)*g.ny + (y - g.y0)
}

// at is the cell's parts (read only).
func (g *bucketGrid) at(x, y int) []bucketEntry {
	if i := g.index(x, y); i >= 0 {
		return g.cells[i]
	}
	if c := g.far[[2]int{x, y}]; c != nil {
		return *c
	}
	return nil
}

// cell is the cell's slot, created when missing.
func (g *bucketGrid) cell(x, y int) *[]bucketEntry {
	if i := g.index(x, y); i >= 0 {
		return &g.cells[i]
	}
	k := [2]int{x, y}
	c := g.far[k]
	if c == nil {
		c = new([]bucketEntry)
		g.far[k] = c
	}
	return c
}

func bucketRange(r Rect) (x0, y0, x1, y1 int) {
	return int(math.Floor(r.MinX / placeBucket)), int(math.Floor(r.MinY / placeBucket)),
		int(math.Floor(r.MaxX / placeBucket)), int(math.Floor(r.MaxY / placeBucket))
}

func (pl *placer) forBuckets(r Rect, fn func(*Part)) {
	x0, y0, x1, y1 := bucketRange(r)
	for x := x0; x <= x1; x++ {
		for y := y0; y <= y1; y++ {
			for _, e := range pl.bucket.at(x, y) {
				fn(e.p)
			}
		}
	}
}

// forBucketsOnce calls fn once per part filed in r's buckets, at its first
// bucket in forBuckets' order (x, then y) — the order a seen-set over
// forBuckets visits them, without the set: a part filed under buckets
// [ex0..]×[ey0..] is first met in r's range at (max(ex0, x0), max(ey0, y0)).
// fn returns false to stop.
func (pl *placer) forBucketsOnce(r Rect, fn func(*Part) bool) {
	x0, y0, x1, y1 := bucketRange(r)
	for x := x0; x <= x1; x++ {
		for y := y0; y <= y1; y++ {
			for _, e := range pl.bucket.at(x, y) {
				if max(e.x0, x0) != x || max(e.y0, y0) != y {
					continue
				}
				if !fn(e.p) {
					return
				}
			}
		}
	}
}

func (pl *placer) bucketOp(p *Part, add bool) {
	x0, y0, x1, y1 := bucketRange(pl.boxes[p])
	for x := x0; x <= x1; x++ {
		for y := y0; y <= y1; y++ {
			c := pl.bucket.cell(x, y)
			if add {
				*c = append(*c, bucketEntry{p, x0, y0})
				continue
			}
			l := *c
			for i, e := range l {
				if e.p == p {
					l[i] = l[len(l)-1]
					*c = l[:len(l)-1]
					break
				}
			}
		}
	}
}

func (pl *placer) rebuildBuckets() {
	pl.bucket = newBucketGrid(pl.b.Bounds())
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
			if len(l.Kinds) == 1 && l.Kinds[0] == "power" {
				// Sharing a rail says nothing about adjacency: planes deliver
				// power. Only signals pull blocks together.
				w = 0.4
			}
			for _, kind := range l.Kinds {
				if kind == "clock" || kind == "usb" || kind == "diff" || kind == "rf" {
					w *= 2
				}
			}
			if a.bl.Kind == "rf" || bb.bl.Kind == "rf" {
				// A radio and its antenna/RF port: every mil of feed costs
				// loss and detuning; this pair outranks any bus.
				w = math.Max(w, 6) * 4
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

var roleOrder = []string{"hot-loop", "bootstrap", "decap", "clock", "clock-load", "power-stage", "feedback", "protection", "power-path", "pin-filter", "pull", "group", "signal", "chain", "test"}

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
	// The pin's side is the body edge nearest to it: a pin at the bottom of
	// a long left-hand column is on the left edge, even though it is far
	// below the centre.
	tc := target.Box.C
	edges := []struct {
		d   float64
		out Point
	}{
		{tc.X - cb.MinX, Point{-1, 0}}, {cb.MaxX - tc.X, Point{1, 0}},
		{tc.Y - cb.MinY, Point{0, -1}}, {cb.MaxY - tc.Y, Point{0, 1}},
	}
	out := edges[0].out
	best := edges[0].d
	for _, e := range edges[1:] {
		if e.d < best-1e-6 {
			best, out = e.d, e.out
		}
	}
	_ = cc
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
	// stuck: parts whose last spiral found no better pose, at that cost. The
	// full spiral (400 rings × 4 turns) of a bridge that cannot reach zero
	// (a transformer straddling the isolation strip) costs 10–40 s; with its
	// local cost unchanged the same search finds the same nothing.
	stuck := map[*Part]float64{}
	for pass := 0; pass < 3 && !pl.overBudget(); pass++ {
		moved := false
		for _, p := range order {
			if pl.overBudget() {
				break
			}
			hc := pl.hardCost(p)
			if hc <= 1e-6 {
				continue
			}
			if c, ok := stuck[p]; ok && c == hc {
				continue
			}
			if pl.spiral(p) {
				moved = true
				delete(stuck, p)
			} else {
				stuck[p] = pl.hardCost(p)
			}
		}
		if !moved {
			break
		}
	}
}

// hardCost counts only the constraint violations (not decap/length terms).
func (pl *placer) hardCost(p *Part) float64 { return pl.hardCostLim(p, math.Inf(1)) }

// hardCostLim is hardCost, or a partial sum ≥ lim once it reaches lim.
func (pl *placer) hardCostLim(p *Part, lim float64) float64 {
	pl.hardOnly = true
	c := pl.partCostLim(p, lim)
	pl.hardOnly = false
	return c
}

// spiralRings[ring][k] is (cos a, sin a) of the spiral's k-th point on that
// ring, a = 2πk/n with n = max(8·ring, 1) — the same math.Cos / math.Sin
// values the spiral computed per point, once: a full spiral (400 rings,
// four turns) asked for them 2.5 million times per part.
var (
	spiralRingsOnce sync.Once
	spiralRings     [][]Point
)

const spiralMaxRing = 400

func spiralRing(ring int) []Point {
	spiralRingsOnce.Do(func() {
		spiralRings = make([][]Point, spiralMaxRing+1)
		for r := range spiralRings {
			n := max(8*r, 1)
			cs := make([]Point, n)
			for k := range cs {
				a := 2 * math.Pi * float64(k) / float64(n)
				cs[k] = Point{math.Cos(a), math.Sin(a)}
			}
			spiralRings[r] = cs
		}
	})
	if ring <= spiralMaxRing {
		return spiralRings[ring]
	}
	n := 8 * ring
	cs := make([]Point, n)
	for k := range cs {
		a := 2 * math.Pi * float64(k) / float64(n)
		cs[k] = Point{math.Cos(a), math.Sin(a)}
	}
	return cs
}

// spiralCentre is where the spiral of a part centred at orig starts: orig,
// unless its zone z lies beyond the spiral's reach (its last ring, radius
// (spiralMaxRing-1)·step, a circle: the distance is Euclidean) — then the
// zone's nearest point, since no ring around orig could enter the zone.
func spiralCentre(orig Point, z Rect, step float64) Point {
	if z.Empty() {
		return orig
	}
	in := Point{clamp(orig.X, z.MinX, z.MaxX), clamp(orig.Y, z.MinY, z.MaxY)}
	if in.Dist(orig) > float64(spiralMaxRing)*step {
		return in
	}
	return orig
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
	halo := pl.spacing/2 + pl.opt.Halo[p.Ref]
	sc := pl.newSpiralCtx(p)
	// A part constructed far outside its zone (more than the spiral's reach:
	// a 1500-part board's satellites of an off-board pile) can never get
	// there from its own spot, and every ring is a full search for nothing.
	// Spiral around the nearest point of the zone instead.
	centre := orig
	if z, ok := pl.zoneOf[p]; ok {
		centre = spiralCentre(orig, z, step)
	}
	for _, rot := range rots {
		if pl.overBudget() {
			break
		}
		if bestCost <= 1e-6 {
			break
		}
		// The body at this rotation, once known (looked up again only until
		// the first real move creates it).
		nrot := normDeg(rot)
		rel, relOK := p.relBody[nrot]
		relOK = relOK && len(p.Pads) > 0
		for ring := 0; ring < spiralMaxRing && bestCost > 1e-6; ring++ {
			r := float64(ring) * step
			n := max(8*ring, 1)
			if ring&15 == 15 && pl.overBudget() {
				break
			}
			cs := spiralRing(ring)
			for k := 0; k < n; k++ {
				c := centre.Add(Point{r * cs[k].X, r * cs[k].Y})
				lim := bestCost - 1e-6
				// Most spots of a crowded board are rejected on their box alone
				// (overlap, zone, board, keep-outs): judge those on the pose
				// movePartCentre would compute, without re-posing every pad. A
				// rigid part's body at rot is its cached relative body moved, so
				// the pose and box are movePartCentre's to the bit, and the next
				// real move starts from the same anchor as it always did.
				if relOK {
					off := rel.Translate(p.Pos).Center().Sub(p.Pos)
					pos := c.Sub(off)
					body := rel.Translate(pos)
					if boundRejects(pl.hardBound(sc, body.Expand(halo), body, lim), lim) {
						// The pads lag behind until the next real move, which
						// re-poses them from this anchor (movePartCentre's
						// first MoveTo); nothing reads them in between.
						p.Pos, p.Rotation = pos, nrot
						continue
					}
				}
				movePartCentre(p, c, rot)
				pl.boxes[p] = pl.box(p)
				if !relOK {
					rel, relOK = p.relBody[nrot]
					relOK = relOK && len(p.Pads) > 0
				}
				if cost := pl.hardCostLim(p, lim); cost < lim {
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

// anneal reports whether it stopped on its deadline before every move was
// tried.
func (pl *placer) anneal(deadline time.Time) bool {
	if len(pl.movable) == 0 {
		return false
	}
	pl.rebuildBuckets()
	if w := pl.opt.Congestion; w >= 0 {
		if w == 0 {
			w = 1
		}
		pl.rudy = newRudy(pl.b, pl.an, pl.region, w)
		defer func() { pl.rudy = nil }()
	}
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
			} else {
				return true // no time left at all (PlaceOptions.Deadline passed)
			}
			if timeFrac >= 1 {
				return true
			}
		}
		// Cooling follows the move count, so a seed reproduces its layout
		// whatever the machine load; the clock only takes over past 80 % of
		// the budget, to end cold instead of mid-melt. Mixing it in from the
		// start made a loaded machine strand the szpi crystal 36 mm from its
		// hub IC in one run and place it 0.4 mm away in the next.
		frac := float64(i) / float64(total)
		if timeFrac > 0.8 {
			frac = math.Max(frac, timeFrac)
			pl.wallClock = true
		}
		// Cool geometrically; the last 15% is greedy descent.
		t := temp * math.Pow(0.001, frac/0.85)
		r := math.Max(minR, radius*math.Pow(0.02, frac))
		p := pl.movable[pl.rng.Intn(len(pl.movable))]
		d := pl.tryMove(p, r, true)
		if d > 0 && (frac > 0.85 || pl.rng.Float64() >= math.Exp(-d/t)) {
			pl.undo(p)
		} else if pl.rudy != nil {
			pl.rudy.done()
		}
	}
	return false
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

// tryMove perturbs p (shift, rotate or swap) and returns the cost delta.
// With keep=false the move is undone immediately.
func (pl *placer) tryMove(p *Part, radius float64, keep bool) float64 {
	pl.lastMove = savedPose{pos: p.Pos, rot: p.Rotation}
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
	macro := pl.macro[p]
	if len(macro) > 0 {
		// A macro core always carries its frozen auxiliaries; no swaps.
		if kind == 9 || kind == 7 {
			kind = 0
			q = nil
		}
		for _, d := range macro {
			grp = append(grp, d)
			pl.lastMove.grp = append(pl.lastMove.grp, posePart{d, d.Pos, d.Rotation})
		}
	} else if kind == 7 && len(pl.servedBy[p.Ref]) > 0 {
		// Block move: the core carries its auxiliaries rigidly.
		for _, d := range pl.servedBy[p.Ref] {
			if !d.Fixed {
				grp = append(grp, d)
				pl.lastMove.grp = append(pl.lastMove.grp, posePart{d, d.Pos, d.Rotation})
			}
		}
	} else if kind == 7 {
		kind = 0
	}
	before := pl.localCost(p, q)
	for _, d := range grp {
		before += pl.groupCost(d)
		if len(macro) > 0 {
			before += pl.partCost(d)
		}
	}
	if pl.rudy != nil {
		moved := append([]*Part{p}, grp...)
		if kind == 9 && q != nil {
			moved = append(moved, q)
		}
		pl.rudy.begin(moved)
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
		pl.lastMove.q, pl.lastMove.qp, pl.lastMove.qr = q, q.Pos, q.Rotation
		pc, qc := p.Body().Center(), q.Body().Center()
		pl.bucketOp(p, false)
		pl.bucketOp(q, false)
		movePartCentre(p, qc, p.Rotation)
		movePartCentre(q, pc, q.Rotation)
		pl.boxes[p], pl.boxes[q] = pl.box(p), pl.box(q)
		pl.bucketOp(p, true)
		pl.bucketOp(q, true)
	case kind == 8 && !pl.opt.NoRotate:
		ang := 90 * float64(1+pl.rng.Intn(3))
		pc := p.Body().Center()
		pl.bucketOp(p, false)
		movePartCentre(p, pc, normDeg(p.Rotation+ang))
		pl.boxes[p] = pl.box(p)
		pl.bucketOp(p, true)
		for _, d := range grp { // macro: rotate rigidly about the core centre
			dc := d.Body().Center().Sub(pc).Rotate(ang).Add(pc)
			pl.bucketOp(d, false)
			movePartCentre(d, dc, normDeg(d.Rotation+ang))
			pl.boxes[d] = pl.box(d)
			pl.bucketOp(d, true)
		}
	default:
		delta := Point{(pl.rng.Float64()*2 - 1) * radius, (pl.rng.Float64()*2 - 1) * radius}
		for _, d := range append([]*Part{p}, grp...) {
			pl.bucketOp(d, false)
			movePartCentre(d, d.Body().Center().Add(delta), d.Rotation)
			pl.boxes[d] = pl.box(d)
			pl.bucketOp(d, true)
		}
	}
	after := pl.localCost(p, q)
	for _, d := range grp {
		after += pl.groupCost(d)
		if len(macro) > 0 {
			after += pl.partCost(d)
		}
	}
	if pl.rudy != nil {
		after += pl.rudy.commit()
	}
	if !keep {
		pl.undo(p)
	}
	return after - before
}

func (pl *placer) undo(p *Part) {
	if pl.rudy != nil {
		pl.rudy.revert()
	}
	s := pl.lastMove
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
	nets := append(pl.netBuf[:0], pl.partNet[p]...)
	cost := pl.partCost(p)
	if q != nil {
		nets = append(nets, pl.partNet[q]...)
		cost += pl.partCost(q)
	}
	pl.netBuf = nets
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
	nets := append(pl.netBuf[:0], pl.partNet[d]...)
	pl.netBuf = nets
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

// autosize shrinks the outline to the placement plus margin — each part's
// own margin: a part with a domain edge band keeps that band to the edge.
func (pl *placer) autosize() []Point {
	pol := pl.an.edgePolicy(pl.b)
	base := math.Max(pl.m.MarginMil+pl.b.Rules.EdgeClearance, pol.LayerReq(LayerTop))
	r := EmptyRect()
	for _, p := range pl.b.Parts {
		r = r.Union(p.Body().Expand(base))
		for _, pd := range p.Pads {
			l := pd.Layer
			if l == LayerMulti {
				l = LayerTop
			}
			if req := pol.Req(l, pd.Net); req > base {
				r = r.Union(pd.Box.Bounds().Expand(req))
			}
		}
	}
	pl.b.Outline = RoundedRect(r, math.Min(80, r.W()/10))
	return pl.b.Outline
}

func (pl *placer) metrics(res *PlaceResult) {
	pl.rebuildBuckets()
	m := &res.Metrics
	m.WirelengthIn = pl.wirelength() / 1000
	m.Corridors = len(pl.corridors)
	m.LabelBlocked = pl.labelBlocked()
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
		if z, ok := pl.zoneOf[p]; ok && z != pl.region && !pl.c.IsBridge(p.Ref) && outside(bi, z) > 1 {
			m.OutOfZone++
		}
		for _, k := range pl.b.Keepouts {
			if k.NoParts && k.Owner != p.Ref && bi.OverlapArea(PolyBounds(k.Poly)) > 1 {
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
	excess, critical := 0.0, 0.0
	for p, t := range pl.tether {
		if t.w > 0 {
			e := pl.tetherCost(p) / t.w
			excess += e
			if criticalRoles[t.role] {
				critical += e
			}
		}
	}
	m.TetherExcessMil = math.Round(excess)
	m.CriticalExcessMil = math.Round(critical)
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

// tidyV07 is v0.7's tidy, kept verbatim as the placement every run had
// before the aesthetics stage existed: the stage starts from it and the
// routed guard falls back to it, so a roll-back is exactly the v0.7
// placement. It rarely takes effect (baseline.md §5: its folds swap pad
// nets, its snaps collide with flush neighbours and it requires a zero hard
// cost); placer_aes.go is the working version.
func (pl *placer) tidyV07() {
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
// criticalRoles are the relations whose distance is electrical (loop area,
// decoupling inductance, clock load, clamp path) rather than routing comfort.
var criticalRoles = map[string]bool{"hot-loop": true, "bootstrap": true, "decap": true, "clock": true, "clock-load": true,
	"power-stage": true, "protection": true, "power-path": true, "pin-filter": true}

var tetherRoles = map[string][2]float64{ // weight, slack (mil)
	"hot-loop":    {8, 20},
	"bootstrap":   {7, 25},
	"feedback":    {4, 60},
	"decap":       {6, 30},
	"clock":       {6, 40},
	"clock-load":  {5, 50},
	"power-stage": {5, 60},
	"protection":  {5, 100},
	"power-path":  {5, 80},
	"pin-filter":  {5, 60},
	"group":       {3, 100},
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
	// The smallest cap on each power pin is that pin's high-frequency
	// decoupler whatever its value: a lone 4.7 µF must still hug the pin.
	pinSmallest := map[string]float64{}
	for _, bl := range c.Blocks {
		for _, m := range bl.Members {
			if m.Role != "decap" {
				continue
			}
			v := capRank(b.Part(m.Ref))
			if cur, ok := pinSmallest[m.Pin]; !ok || v < cur {
				pinSmallest[m.Pin] = v
			}
		}
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
				if capRank(p) <= pinSmallest[m.Pin] {
					t.w, t.slack = 7, 25
				}
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
	pl.conv = map[*Part]*Converter{}
	pl.swPads = map[*Converter][]*Pad{}
	for _, cv := range c.Converters {
		for _, q := range b.Parts {
			for _, pd := range q.Pads {
				if pd.Net == cv.SwitchNet {
					pl.swPads[cv] = append(pl.swPads[cv], pd)
				}
			}
		}
		for _, ref := range append([]string{cv.HotCap, cv.Diode}, cv.Feedback...) {
			if p := b.Part(ref); p != nil && movable[p] {
				pl.conv[p] = cv
			}
		}
	}
	pl.chains = map[*Part][]*SignalChain{}
	pl.pairOf = map[*Part]*Part{}
	for _, ch := range c.Chains {
		for i, n := range ch.Nodes {
			p := b.Part(n.Ref)
			if p == nil || !movable[p] {
				continue
			}
			pl.chains[p] = append(pl.chains[p], ch)
			if ch.Pair != nil && i < len(ch.Pair.Nodes) {
				if q := b.Part(ch.Pair.Nodes[i].Ref); q != nil && q != p && movable[q] {
					pl.pairOf[p] = q
				}
			}
		}
	}
	pl.setupReserves()
	pl.setupCorridors()
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
	if t.role == "hot-loop" {
		// The loop polygon is the whole truth for these parts; a second
		// pull toward one pin would fight it (it refused the 180° flip that
		// shrinks the loop). The tether stays only as the fallback.
		if cv := pl.conv[p]; cv != nil {
			if _, _, _, ok := HotLoop(pl.b, pl.an, cv); ok {
				return 0
			}
		}
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

// Hot-loop and feedback weights. The loop polygon runs through pad centres,
// so a tight 0805/SOT-23 layout already measures ~150 mil around.
const (
	hotLoopSlackMil = 150
	fbKeepAwayMil   = 150
)

// converterCost prices a switcher's layout physics for p: the hot-loop
// perimeter and area if p is in the loop, the distance from the switch node
// if p is in the feedback divider.
func (pl *placer) converterCost(p *Part) float64 {
	cv := pl.conv[p]
	if cv == nil {
		return 0
	}
	if p.Ref == cv.HotCap || p.Ref == cv.Diode {
		if _, perim, area, ok := HotLoop(pl.b, pl.an, cv); ok {
			return 3*math.Max(0, perim-hotLoopSlackMil) + 2*math.Sqrt(area)
		}
		return 0
	}
	// Feedback: away from the inductor body and every switch-node pad.
	c := p.Body().Center()
	d := math.Inf(1)
	if l := pl.b.Part(cv.Inductor); l != nil {
		d = math.Min(d, rectDist(p.Body(), l.Body()))
	}
	for _, pd := range pl.swPads[cv] {
		if pd.Part != p.Ref {
			d = math.Min(d, c.Dist(pd.Box.C))
		}
	}
	return 4 * math.Max(0, fbKeepAwayMil-d)
}

// rectDist is the gap between two rectangles (0 when they touch or overlap).
func rectDist(a, b Rect) float64 {
	dx := math.Max(0, math.Max(a.MinX-b.MaxX, b.MinX-a.MaxX))
	dy := math.Max(0, math.Max(a.MinY-b.MaxY, b.MinY-a.MaxY))
	return math.Hypot(dx, dy)
}

// chainCost prices interface order for p: the detour of every chain p sits
// on, and for a diff-pair part the gap to its partner beyond side-by-side.
const pairTwistMil = 400.0

func (pl *placer) chainCost(p *Part) float64 {
	cost := 0.0
	for _, ch := range pl.chains[p] {
		if ex, _, ok := ChainCost(pl.b, pl.c, ch); ok {
			cost += 1.5 * ch.Weight * ex
		}
		// A twisted pair costs vias on a matched line: price a crossing
		// like 400 mil of detour so the placer turns the in-line part. An
		// intent-declared pair is judged on its access paths (pads reached
		// through the side of their part they sit on): an ESD array whose
		// pin row lies along the approach twists the pair as surely as one
		// turned the wrong way round (ESP32 mini USBLC6, 2026-09-30).
		tw := PairTwist(pl.b, ch)
		if pl.corridorChain(ch) {
			tw = PairTwistAccess(pl.b, ch)
		}
		cost += pairTwistMil * ch.Weight * float64(tw)
	}
	if q := pl.pairOf[p]; q != nil {
		pb, qb := p.Body(), q.Body()
		side := (math.Min(pb.W(), pb.H())+math.Min(qb.W(), qb.H()))/2 + pl.spacing
		cost += 3 * math.Max(0, pb.Center().Dist(qb.Center())-side)
	}
	return cost
}

// polishRoles are the parts whose last few mils decide the electrical
// result; annealing gets them close, polish makes them exact.
var polishRoles = map[string]bool{"hot-loop": true, "bootstrap": true, "decap": true, "clock": true, "clock-load": true, "protection": true, "feedback": true, "pin-filter": true, "power-path": true}

// polish is a deterministic exhaustive local search for critical auxiliaries:
// every 15 mil grid point within 180 mil of the target pad, four rotations,
// keeping only overlap-free poses that lower the local cost. Critical parts
// go first (hot loop before decaps before protection), and each part sees
// the already-polished ones as obstacles.
func (pl *placer) polish() {
	type item struct {
		p    *Part
		rank int
		key  float64
	}
	var items []item
	for p, t := range pl.tether {
		if p.Fixed || !polishRoles[t.role] || len(t.pads) == 0 {
			continue
		}
		if t.role == "decap" && t.slack > 30 {
			continue // bulk caps do not need exact placement
		}
		items = append(items, item{p, roleRank(t.role), capRank(p)})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].rank != items[j].rank {
			return items[i].rank < items[j].rank
		}
		if items[i].key != items[j].key {
			return items[i].key < items[j].key
		}
		return items[i].p.Ref < items[j].p.Ref
	})
	const maxPolish = 400
	if len(items) > maxPolish {
		items = items[:maxPolish]
	}
	pl.rebuildBuckets()
	const step, radius = 15.0, 180.0
	rots := []float64{0, 90, 180, 270}
	if pl.opt.NoRotate {
		rots = nil
	}
	for _, it := range items {
		if pl.overBudget() {
			break
		}
		pl.polishPart(it.p, rots, step, radius)
	}
}

// polishPart moves p to the best legal pose on a grid around its tether
// target (exhaustive over positions within radius and the given rotations).
func (pl *placer) polishPart(p *Part, rots []float64, step, radius float64) {
	{
		target := pl.tether[p].pads[0].Box.C
		bestPos, bestRot := p.Pos, p.Rotation
		best := pl.localCost(p, nil)
		if pl.hardCost(p) > 1e-6 {
			best = math.Inf(1)
		}
		centre := p.Body().Center()
		try := func(c Point, r float64) {
			pl.bucketOp(p, false)
			movePartCentre(p, c, r)
			pl.boxes[p] = pl.box(p)
			pl.bucketOp(p, true)
			if pl.hardCost(p) <= 1e-6 {
				if cost := pl.localCost(p, nil); cost < best-1e-6 {
					best, bestPos, bestRot = cost, p.Pos, p.Rotation
				}
			}
		}
		rs := rots
		if rs == nil {
			rs = []float64{p.Rotation}
		}
		for _, r := range rs {
			for dy := -radius; dy <= radius; dy += step {
				for dx := -radius; dx <= radius; dx += step {
					try(Point{target.X + dx, target.Y + dy}, r)
				}
			}
		}
		_ = centre
		pl.bucketOp(p, false)
		p.MoveTo(bestPos, bestRot)
		pl.boxes[p] = pl.box(p)
		pl.bucketOp(p, true)
	}
}

// rescueFarMil is how far a decap may end from the pin it serves before
// rescueDecaps looks for a legal spot near the pin: an inch, far beyond any
// decap tether's slack — on the boards that place well no decap is there.
const rescueFarMil = 1000.0

// decapGap is d's distance from the pin it serves (its nearest pad on the
// pin's net), +Inf when it serves none.
func (pl *placer) decapGap(d *Part) float64 {
	pin := pl.decap[d]
	if pin == nil {
		return math.Inf(1)
	}
	best := math.Inf(1)
	for _, pd := range d.Pads {
		if pd.Net == pin.Net {
			best = math.Min(best, pd.Box.C.Dist(pin.Box.C))
		}
	}
	return best
}

// rescueDecaps brings back decaps the anneal left more than rescueFarMil
// from their pin. On a crowded board (1443 parts, anneal cut by the
// clock) dozens ended 3–5 inches out: polish only searches 180 mil around
// the pin, and the spots there were taken. Each far decap spirals out from
// its pin to the nearest legal pose (fixed parts and every other part stay
// where they are) and moves there only when that lowers its local cost.
func (pl *placer) rescueDecaps() {
	var far []*Part
	for _, d := range pl.movable {
		if g := pl.decapGap(d); !math.IsInf(g, 1) && g > rescueFarMil {
			far = append(far, d)
		}
	}
	if len(far) == 0 {
		return
	}
	pl.rebuildBuckets()
	rots := []float64{0, 90, 180, 270}
	if pl.opt.NoRotate {
		rots = nil
	}
	for _, d := range far {
		if pl.overBudget() {
			return
		}
		pl.rescuePart(d, rots)
	}
}

func (pl *placer) rescuePart(d *Part, rots []float64) {
	pin := pl.decap[d].Box.C
	pos0, rot0 := d.Pos, d.Rotation
	before := pl.localCost(d, nil)
	pl.bucketOp(d, false)
	sc := pl.newSpiralCtx(d)
	halo := pl.spacing/2 + pl.opt.Halo[d.Ref]
	step := math.Max(pl.spacing, 10)
	rs := rots
	if rs == nil {
		rs = []float64{rot0}
	}
	found := false
	var bestPos Point
	var bestRot float64
	// Ring by ring out to the distance the decap is at now: the first ring
	// with a legal pose holds the nearest ones; the best of its poses wins.
	maxRing := int(math.Min(rescueFarMil, pl.decapGap(d)) / step)
	for ring := 0; ring <= maxRing && !found && !pl.overBudget(); ring++ {
		r := float64(ring) * step
		n := max(8*ring, 1)
		best := before
		cs := spiralRing(ring)
		for _, rot := range rs {
			// Past the deadline: keep the best legal pose found (none: the
			// decap goes back), buckets restored below either way.
			if pl.overBudget() {
				break
			}
			for k := 0; k < n; k++ {
				c := pin.Add(Point{r * cs[k].X, r * cs[k].Y})
				if rel, ok := d.relBody[normDeg(rot)]; ok && len(d.Pads) > 0 {
					off := rel.Translate(d.Pos).Center().Sub(d.Pos)
					pos := c.Sub(off)
					body := rel.Translate(pos)
					if boundRejects(pl.hardBound(sc, body.Expand(halo), body, 1e-6), 1e-6) {
						d.Pos, d.Rotation = pos, normDeg(rot)
						continue
					}
				}
				movePartCentre(d, c, rot)
				pl.boxes[d] = pl.box(d)
				if pl.hardCostLim(d, 1e-6) >= 1e-6 {
					continue
				}
				if cost := pl.localCost(d, nil); cost < best-1e-6 {
					best, bestPos, bestRot, found = cost, d.Pos, d.Rotation, true
				}
			}
		}
	}
	if found {
		d.MoveTo(bestPos, bestRot)
	} else {
		d.MoveTo(pos0, rot0)
	}
	pl.boxes[d] = pl.box(d)
	pl.bucketOp(d, true)
}

// portReserve is the strip along a connector's protected pins that belongs
// to that port's protection and filter parts. A buck inductor or an audio
// filter cap parked there pushes the ESD array away from the pins it guards.
type portReserve struct {
	r     Rect
	block string
	side  int
}

func (pl *placer) setupReserves() {
	b, c := pl.b, pl.c
	for _, bl := range c.Blocks {
		conn := b.Part(bl.Core)
		if conn == nil || c.Kinds[conn.Ref] != KindConnector && c.Kinds[conn.Ref] != KindAntenna {
			continue
		}
		var pins []*Pad
		depth, side := 0.0, 0
		for _, m := range bl.Members {
			if m.Role != "protection" {
				continue
			}
			p := b.Part(m.Ref)
			if pd := padAt(b, m.Pin); pd != nil && p != nil {
				pins = append(pins, pd)
				bb := p.Body()
				depth = math.Max(depth, math.Max(bb.W(), bb.H()))
				side = p.Side
			}
		}
		if len(pins) == 0 {
			continue
		}
		cb := conn.Body()
		ext := EmptyRect()
		for _, pd := range pins {
			ext = ext.Union(pd.Box.Bounds())
		}
		ctr := ext.Center()
		depth += 2 * pl.spacing
		// The protected pins' side: the body edge nearest their centroid.
		dl, dr, dd, du := ctr.X-cb.MinX, cb.MaxX-ctr.X, ctr.Y-cb.MinY, cb.MaxY-ctr.Y
		var r Rect
		switch m := math.Min(math.Min(dl, dr), math.Min(dd, du)); m {
		case dl:
			r = Rect{cb.MinX - depth, ext.MinY - pl.spacing, cb.MinX, ext.MaxY + pl.spacing}
		case dr:
			r = Rect{cb.MaxX, ext.MinY - pl.spacing, cb.MaxX + depth, ext.MaxY + pl.spacing}
		case dd:
			r = Rect{ext.MinX - pl.spacing, cb.MinY - depth, ext.MaxX + pl.spacing, cb.MinY}
		default:
			r = Rect{ext.MinX - pl.spacing, cb.MaxY, ext.MaxX + pl.spacing, cb.MaxY + depth}
		}
		pl.reserve = append(pl.reserve, portReserve{r: r, block: bl.ID, side: side})
	}
}

func (pl *placer) reserveCost(p *Part) float64 {
	if len(pl.reserve) == 0 {
		return 0
	}
	cost := 0.0
	own := pl.c.BlockOf[p.Ref]
	for _, rs := range pl.reserve {
		if own == rs.block || p.Side != rs.side && !hasTHT(p) {
			continue
		}
		if ov := pl.boxes[p].OverlapArea(rs.r); ov > 0 {
			cost += 3 * math.Sqrt(ov)
		}
	}
	return cost
}

// fitSpacing is the largest courtyard gap for which every side's parts,
// each grown by half the gap all round, still fit in 85 % of the board.
func fitSpacing(b *Board) float64 {
	board := b.Area()
	if board <= 0 {
		return math.Inf(1)
	}
	best := math.Inf(1)
	for _, side := range []int{LayerTop, LayerBottom} {
		area, perim := 0.0, 0.0
		for _, p := range b.Parts {
			if p.Side != side && !hasTHT(p) {
				continue
			}
			bb := p.Body()
			area += bb.Area()
			perim += 2 * (bb.W() + bb.H())
		}
		if perim == 0 {
			continue
		}
		// area + perim·s/2 ≤ 0.85·board
		best = math.Min(best, 2*(0.85*board-area)/perim)
	}
	return best
}

// freezeMacros ends stage 1 of two-stage placement: every movable auxiliary
// in a physically critical role (criticalRoles: decap, crystal and its load
// caps, converter power stage, ESD/power path at a connector, pin filters)
// is frozen to the core it serves and leaves the movable set; tryMove then
// carries it rigidly with that core.
func (pl *placer) freezeMacros() {
	pl.macro = map[*Part][]*Part{}
	frozen := map[*Part]bool{}
	for _, bl := range pl.c.Blocks {
		core := pl.b.Part(bl.Core)
		if core == nil {
			continue
		}
		for _, m := range bl.Members {
			d := pl.b.Part(m.Ref)
			t := pl.tether[d]
			if d == nil || d.Fixed || t == nil || !criticalRoles[t.role] || frozen[d] {
				continue
			}
			pl.macro[core] = append(pl.macro[core], d)
			frozen[d] = true
		}
	}
	pl.seatMacros()
	kept := pl.movable[:0]
	for _, p := range pl.movable {
		if !frozen[p] {
			kept = append(kept, p)
		}
	}
	pl.movable = kept
}

// seatMacros solves each macro on its own: every other part is taken out of
// the collision buckets, so the core's critical auxiliaries (bulk decaps
// included) pack at their pins as tightly as the core alone allows, instead
// of settling into whatever gap the crowded stage-1 board left.
func (pl *placer) seatMacros() {
	cores := make([]*Part, 0, len(pl.macro))
	for c := range pl.macro {
		cores = append(cores, c)
	}
	sort.Slice(cores, func(i, j int) bool { return cores[i].Ref < cores[j].Ref })
	rots := []float64{0, 90, 180, 270}
	if pl.opt.NoRotate {
		rots = nil
	}
	for _, core := range cores {
		members := append([]*Part(nil), pl.macro[core]...)
		in := map[*Part]bool{core: true}
		for _, d := range members {
			in[d] = true
		}
		pl.rebuildBuckets()
		for _, p := range pl.b.Parts {
			if !in[p] {
				pl.bucketOp(p, false)
			}
		}
		sort.SliceStable(members, func(i, j int) bool {
			ri, rj := roleRank(pl.tether[members[i]].role), roleRank(pl.tether[members[j]].role)
			if ri != rj {
				return ri < rj
			}
			if a, b := capRank(members[i]), capRank(members[j]); a != b {
				return a < b
			}
			return members[i].Ref < members[j].Ref
		})
		for _, d := range members {
			pl.polishPart(d, rots, 15, 240)
		}
	}
	pl.rebuildBuckets()
}

// unfreezeMacros returns the frozen auxiliaries to the movable set for the
// stage-3 fine-tune (legalise + polish).
func (pl *placer) unfreezeMacros() {
	if pl.macro == nil {
		return
	}
	cores := make([]*Part, 0, len(pl.macro))
	for c := range pl.macro {
		cores = append(cores, c)
	}
	sort.Slice(cores, func(i, j int) bool { return cores[i].Ref < cores[j].Ref })
	for _, c := range cores {
		pl.movable = append(pl.movable, pl.macro[c]...)
	}
	pl.macro = nil
}

// isoStripPrefix names the isolation strips the placer adds.
const isoStripPrefix = "isolation "

// carveStrips opens each isolation strip where a bridge part sits on it
// (intent runs only): a bridge whose pad rows are closer than the strip is
// wide would otherwise have its pads inside a no-copper band and be
// unroutable. Across the bridge the domain fence, the milled slot and the
// isolation check carry the insulation instead.
func (pl *placer) carveStrips(res *PlaceResult) {
	if pl.an == nil || pl.an.Iso == nil || len(pl.strips) == 0 {
		return
	}
	margin := math.Max(DefaultSlotClearance, pl.b.Rules.Clearance)
	var out []*Keepout
	res.Barriers = res.Barriers[:0]
	for _, k := range pl.strips {
		s := PolyBounds(k.Poly)
		type span struct{ lo, hi float64 }
		var cut []span
		for _, p := range pl.b.Parts {
			if !pl.c.IsBridge(p.Ref) {
				continue
			}
			body := p.Body()
			for _, pd := range p.Pads {
				body = body.Union(pd.Box.Bounds())
			}
			if body.MaxX < s.MinX || body.MinX > s.MaxX {
				continue
			}
			cut = append(cut, span{body.MinY - margin, body.MaxY + margin})
		}
		sort.Slice(cut, func(i, j int) bool { return cut[i].lo < cut[j].lo })
		y := s.MinY
		piece := 0
		emit := func(y0, y1 float64) {
			if y1-y0 < 1 {
				return
			}
			piece++
			ko := &Keepout{Name: sprintf("%s#%d", k.Name, piece), Poly: Rect{s.MinX, y0, s.MaxX, y1}.Corners(), NoCopper: true, NoVias: true}
			out = append(out, ko)
		}
		for _, c := range cut {
			if c.lo > y {
				emit(y, math.Min(c.lo, s.MaxY))
			}
			y = math.Max(y, c.hi)
		}
		emit(y, s.MaxY)
		if len(cut) > 0 {
			res.Notes = append(res.Notes, sprintf("%s opened at %d bridge position(s): the domain fence, slot and isolation check carry the insulation there", k.Name, len(cut)))
		}
	}
	drop := map[*Keepout]bool{}
	for _, k := range pl.strips {
		drop[k] = true
	}
	ks := pl.b.Keepouts[:0]
	for _, k := range pl.b.Keepouts {
		if !drop[k] {
			ks = append(ks, k)
		}
	}
	pl.b.Keepouts = append(ks, out...)
	pl.strips = out
	for _, k := range out {
		res.Barriers = append(res.Barriers, *k)
	}
}

// isoCost keeps the pads of insulated domains (intent pairs) the pair's
// creepage apart across different parts: the zones and the isolation strip
// only bound part centres and bodies softly, and next to a bridge the strip
// is open. A bridge's own pad rows are its footprint's (slot / part choice).
func (pl *placer) isoCost(p *Part, bx Rect) float64 {
	if pl.an == nil || pl.an.Iso == nil || len(pl.an.Iso.Pairs) == 0 {
		return 0
	}
	iso := pl.an.Iso
	if false {
		return 0
	}
	reach := iso.MaxClearanceMil()
	cost := 0.0
	pl.forBucketsOnce(bx.Expand(reach), func(q *Part) bool {
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
				// Exact pad-to-pad copper gap: the centre-minus-half-width
				// estimate read an elongated inductor pad as 30 mil narrower
				// than it is and left it 8 mil inside a Y capacitor's
				// creepage (flyback stress board).
				gap := isoPadGap(a, c, ip.CreepageMil)
				if short := ip.CreepageMil - gap; short > 0 {
					cost += 800 * short
				}
			}
		}
		return true
	})
	return cost
}

// isoPadGap is the copper gap of two pads, exact but cheap: the bounding-box
// gap (a lower bound) settles pads already farther than need apart and is
// exact for axis-aligned rectangles; only near round / rotated pads is the
// polygon distance computed (the legaliser calls this millions of times).
func isoPadGap(a, c *Pad, need float64) float64 {
	ab, cb := a.Box.Bounds(), c.Box.Bounds()
	dx := math.Max(0, math.Max(ab.MinX-cb.MaxX, cb.MinX-ab.MaxX))
	dy := math.Max(0, math.Max(ab.MinY-cb.MaxY, cb.MinY-ab.MaxY))
	g := math.Hypot(dx, dy)
	if g >= need {
		return g
	}
	aligned := func(p *Pad) bool {
		return !p.Box.Round && math.Abs(math.Remainder(p.Box.Rot, 90)) < 1e-6
	}
	if aligned(a) && aligned(c) {
		return g
	}
	d, _, _ := polyDist(padPoly(a), padPoly(c))
	return d
}

// hvPadCost keeps the pads of two different parts the high-voltage clearance
// their nets need from each other (ΔV-aware, same rule as the router and
// DRC). The halo only splits the excess evenly, which is short when an 850 V
// divider node sits next to a 1 V ADC input (CAT III stress board: 79 mil
// between pads that need 98). Zero cost on boards without high-voltage nets.
func (pl *placer) hvPadCost(p *Part, bx Rect) float64 {
	if pl.an == nil || pl.hvReach <= 0 {
		return 0
	}
	base := pl.b.Rules.Clearance
	cost := 0.0
	pl.forBucketsOnce(bx.Expand(pl.hvReach), func(q *Part) bool {
		if q == p {
			return true
		}
		for _, a := range p.Pads {
			if a.Net == "" {
				continue
			}
			for _, c := range q.Pads {
				if c.Net == "" || c.Net == a.Net {
					continue
				}
				req := pl.an.PairClearanceMil(a.Net, c.Net, pl.b.Rules)
				if req <= base+hvExcessMil {
					continue
				}
				if short := req - isoPadGap(a, c, req); short > 0 {
					cost += 800 * short
				}
			}
		}
		return true
	})
	return cost
}
