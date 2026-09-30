package pcbauto

// Aesthetics — Phase A of the placement + routing aesthetics plan
// (docs/reviews/2026-09-routing-aesthetics/README.md §4–§5A): a pure-geometry
// analyser that MEASURES how designed a board looks. It is report-only: the
// joint score carries it with weight 0 and no generator reads it yet.
//
// Every metric has a raw value, a 0–100 score from a ramp whose thresholds
// cite their source, the worst offenders (refs / nets / primitive ids) and,
// where it applies, the exemptions it hard-codes. Electrical intent always
// wins over looks, so the exemptions are decided up front from the analysis
// (differential pairs, RF feeds, length-tuned nets, power via arrays,
// isolation bands and slots) — never explained away after the fact.
//
// Sources cited in the threshold comments:
//
//	[plan]    docs/reviews/2026-09-routing-aesthetics/README.md (§1 baseline, §4 P1–P9)
//	[agent]   raw/claude-agent.md (Opus review with measured baseline)
//	[fable]   raw/fable.md
//	[kimi]    raw/kimi.md
//	[tidy]    internal/app/pcb_score_tidy.go (layout-score tidy dimension, #153)
//
// All thresholds are initial values awaiting Phase E calibration (pairwise
// human preference → Bradley–Terry weights); the golden test pins today's
// numbers so any drift is deliberate.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// AestheticsWeight is the weight of the aesthetics group inside the joint
// score. Phase A is report-only: 0 keeps every existing overall score
// unchanged ([plan] §5A; the planned 0.10 lands in Phase C).
const AestheticsWeight = 0.0

// ConstraintTier is one level of the design-constraint priority order.
type ConstraintTier struct {
	Rank   int    `json:"rank"` // 1 = highest priority
	Name   string `json:"name"`
	Kind   string `json:"kind"` // hard | soft
	Covers string `json:"covers"`
}

// ConstraintPriority is THE priority order of every constraint the flow
// applies (docs/concepts.md "约束优先级"). Aesthetics is an ADDITIONAL, soft
// constraint set layered under all the established research steps: it may
// only choose among solutions that leave tiers 1–6 unchanged, and it never
// replaces, relaxes or bypasses them. The analyser encodes this as hard-coded
// exemptions (see aesRouting) and as weight 0 in the joint score (Phase A).
var ConstraintPriority = []ConstraintTier{
	{1, "safety", "hard", "isolation creepage/clearance bands and milled slots (intent pairs: IEC 62368-1 / 60601-1 MOOP·MOPP / 61010-1, IPC-2221B), board-edge copper distance bands, antenna keep-outs"},
	{2, "electrical", "hard", "intent per-net currents, trace widths and necks, via sizing by current and via arrays, DC IR-drop budget and post-layout thermal/IR, hot/decap loops, diff pairs, RF feeds, length groups, reference planes, analog SPICE targets"},
	{3, "manufacturing", "hard", "rules apply/check, DRC clearance, hole-to-hole, slot clearance, fab minimums"},
	{4, "completion", "hard", "every signal and plane connection routed"},
	{5, "efficiency", "soft", "detour ratio, vias per connection"},
	{6, "placement", "soft", "layout-score: partition, flow order, edge I/O, protection, compactness, assembly clearance"},
	{7, "aesthetics", "soft", "P1–P9 placement and R1–R9 routing looks — report-only in Phase A (weight 0)"},
}

// AesOffender names one object that pulled a metric down.
type AesOffender struct {
	Ref   string  `json:"ref,omitempty"`
	Net   string  `json:"net,omitempty"`
	ID    string  `json:"id,omitempty"` // primitive id (track / via)
	At    *Point  `json:"at,omitempty"`
	Value float64 `json:"value"`
	Note  string  `json:"note,omitempty"`
}

// AesMetric is one measured aesthetic criterion.
type AesMetric struct {
	ID      string             `json:"id"`    // P1…P9, R1…R9
	Group   string             `json:"group"` // placement | routing
	Name    string             `json:"name"`
	Value   float64            `json:"value"`
	Unit    string             `json:"unit"`
	Score   float64            `json:"score"` // 0–100 (meaningless when Skipped)
	Skipped bool               `json:"skipped,omitempty"`
	Reason  string             `json:"reason,omitempty"`
	Detail  string             `json:"detail,omitempty"`
	Extra   map[string]float64 `json:"extra,omitempty"`
	Worst   []AesOffender      `json:"worst,omitempty"`
}

// AesExemption records objects left out of a metric on electrical grounds.
type AesExemption struct {
	Kind    string   `json:"kind"` // diff-pair | rf | length-tuned | power-via-array | isolation-band | slot
	Metrics string   `json:"metrics"`
	Items   []string `json:"items"`
	Why     string   `json:"why"`
}

// AestheticsReport is the aesthetics verdict of one board.
type AestheticsReport struct {
	// Score is the arithmetic mean of the placement and routing groups
	// (groups are arithmetic means of their metrics: [plan] §2 item 2 —
	// sub-items may compensate each other inside the group).
	Score     float64 `json:"score"`
	Weight    float64 `json:"weight"` // weight inside the joint score (0 in Phase A)
	Placement float64 `json:"placement"`
	Routing   float64 `json:"routing"`
	// RoutedShare scales the routing group: unrouted connections count as 0
	// ([plan] §2 anti-gaming 3), so routing less can never look better.
	RoutedShare float64 `json:"routedShare"`
	// Tier is the aesthetics rank in ConstraintPriority (the lowest).
	Tier int `json:"tier"`
	// Profile is the style the report was scored with (its Weight is the
	// planned Phase C weight; Weight above is what the joint applies now).
	Profile    AesProfile         `json:"profile"`
	Metrics    []AesMetric        `json:"metrics"`
	Symmetry   []SymmetryGroup    `json:"symmetry"`
	Exemptions []AesExemption     `json:"exemptions,omitempty"`
	Notes      []string           `json:"notes,omitempty"`
	Counts     map[string]int     `json:"counts"`
	Grid       map[string]float64 `json:"grid,omitempty"`
}

// Metric returns the metric with id (nil when absent).
func (r *AestheticsReport) Metric(id string) *AesMetric {
	for i := range r.Metrics {
		if r.Metrics[i].ID == id {
			return &r.Metrics[i]
		}
	}
	return nil
}

// SilkText is a designator on the silkscreen (pcb dump silk[] Key=Designator).
type SilkText struct {
	Ref      string  `json:"ref"`
	Text     string  `json:"text"`
	Layer    int     `json:"layer"`
	X, Y     float64 `json:"-"`
	Rotation float64 `json:"rotation"`
	FontSize float64 `json:"fontSize"`
	BBox     Rect    `json:"bbox"`
	HasBBox  bool    `json:"-"`
}

// AesInput is what the analyser measures. Only Board is required; without
// tracks the routing metrics are skipped (placement-only boards).
type AesInput struct {
	Board    *Board
	Analysis *Analysis // nil: Analyze(b, PowerSpec{}, nil)
	Circuit  *Circuit  // nil: Understand(b, an)
	Stackup  *Stackup  // nil: TOP h / BOTTOM v / inner alternating (stackup.go defaults)
	Tracks   []Track
	Vias     []Via
	// TrackIDs / ViaIDs are primitive ids parallel to Tracks / Vias; empty
	// means "auto-t<i+1>" / "auto-v<i+1>" (ExportRoutedSnapshot's naming).
	TrackIDs []string
	ViaIDs   []string
	// Copper says the board carries routed copper (a dump with a copper
	// section or a routed result), even when it happens to have no tracks.
	Copper bool
	// PlaneNets are delivered by planes / pours: their pads join through
	// the plane, so connectivity (detour, stubs, routed share) skips them.
	PlaneNets map[string]bool
	// ExemptZones are isolation bands and slot outlines: copper inside
	// them follows creepage, not looks.
	ExemptZones [][]Point
	Silk        []SilkText
	SilkKnown   bool
	// Profile is the aesthetics style (nil = DefaultAesProfile; Name
	// "auto" = chosen from the board by AutoAesProfile). It changes soft
	// objectives only.
	Profile *AesProfile
	// NoExemptions measures every net (review cross-check / diagnostics):
	// the report then no longer honours electrical priority.
	NoExemptions bool
}

// ---------------------------------------------------------------------------
// ramps and shared helpers
// ---------------------------------------------------------------------------

// aesRamp is 100 at v ≤ good and 0 at v ≥ bad (either direction).
func aesRamp(v, good, bad float64) float64 {
	if bad == good {
		return 100
	}
	return clamp(100*(bad-v)/(bad-good), 0, 100)
}

// angDeg is the direction a→b in [0,360).
func angDeg(a, b Point) float64 {
	d := math.Atan2(b.Y-a.Y, b.X-a.X) * 180 / math.Pi
	if d < 0 {
		d += 360
	}
	return d
}

// octiDev is the deviation of a direction from the nearest multiple of 45°.
func octiDev(deg float64) float64 {
	r := math.Mod(deg, 45)
	if r < 0 {
		r += 45
	}
	return math.Min(r, 45-r)
}

// aesAngTol: a segment within 0.5° of an axis/diagonal counts as on it —
// the same tolerance as the [agent] baseline script (aesmetrics.py ANG_TOL),
// so the numbers cross-check.
const aesAngTol = 0.5

// axisClass buckets a direction into H, V, D (45°) or X (any other angle).
func axisClass(deg float64) byte {
	d := math.Mod(deg, 180)
	switch {
	case math.Min(d, 180-d) < aesAngTol:
		return 'H'
	case math.Abs(d-90) < aesAngTol:
		return 'V'
	case math.Abs(d-45) < aesAngTol || math.Abs(d-135) < aesAngTol:
		return 'D'
	}
	return 'X'
}

// onGrid reports p on a g-mil grid within tol.
func onGrid(v, g, tol float64) bool {
	return math.Abs(v/g-math.Round(v/g))*g <= tol
}

// padIndex is a spatial hash of pads for point lookups.
type padIndex struct {
	cell  float64
	cells map[[2]int][]*Pad
}

func newPadIndex(b *Board) *padIndex {
	ix := &padIndex{cell: 100, cells: map[[2]int][]*Pad{}}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			bb := pd.Box.Bounds().Expand(1)
			for x := int(math.Floor(bb.MinX / ix.cell)); x <= int(math.Floor(bb.MaxX/ix.cell)); x++ {
				for y := int(math.Floor(bb.MinY / ix.cell)); y <= int(math.Floor(bb.MaxY/ix.cell)); y++ {
					ix.cells[[2]int{x, y}] = append(ix.cells[[2]int{x, y}], pd)
				}
			}
		}
	}
	return ix
}

// padLocal is pt in the pad's own frame (pad centre origin, pad rotation).
func padLocal(pd *Pad, pt Point) Point { return pt.Sub(pd.Box.C).Rotate(-pd.Box.Rot) }

// inPad: pt inside the pad rectangle plus slack (rectangle even for round
// pads, as the baseline script does).
func inPad(pd *Pad, pt Point, slack float64) bool {
	q := padLocal(pd, pt)
	return math.Abs(q.X) <= pd.Box.W/2+slack && math.Abs(q.Y) <= pd.Box.H/2+slack
}

// at returns the pad under pt on layer (same net when both are named).
func (ix *padIndex) at(pt Point, layer int, net string) *Pad {
	var best *Pad
	bestD := math.Inf(1)
	for _, pd := range ix.cells[[2]int{int(math.Floor(pt.X / ix.cell)), int(math.Floor(pt.Y / ix.cell))}] {
		if !pd.OnLayer(layer) {
			continue
		}
		if net != "" && pd.Net != "" && pd.Net != net {
			continue
		}
		if inPad(pd, pt, 0.5) {
			if d := pd.Box.C.Dist(pt); d < bestD {
				best, bestD = pd, d
			}
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// entry point
// ---------------------------------------------------------------------------

// Aesthetics measures the placement and (when copper is present) routing
// aesthetics of a board. It never mutates the board.
func Aesthetics(in AesInput) *AestheticsReport {
	b := in.Board
	rep := &AestheticsReport{Weight: AestheticsWeight, Tier: ConstraintPriority[len(ConstraintPriority)-1].Rank, Counts: map[string]int{}, Symmetry: []SymmetryGroup{}}
	if b == nil {
		rep.Notes = append(rep.Notes, "no board")
		return rep
	}
	if b.byRef == nil {
		_ = b.Index()
	}
	an := in.Analysis
	if an == nil {
		an = Analyze(b, PowerSpec{}, in.Stackup)
	}
	c := in.Circuit
	if c == nil {
		c = Understand(b, an)
	}
	switch {
	case in.Profile == nil:
		p, _ := AesProfileByName(DefaultAesProfile)
		in.Profile = &p
	case in.Profile.Name == "auto":
		p := AutoAesProfile(b, an, b.CopperLayers)
		in.Profile = &p
	}
	rep.Profile = in.Profile.clone()
	rep.Profile.Auto = in.Profile.Auto
	rep.Counts["parts"] = len(b.Parts)
	rep.Counts["tracks"] = len(in.Tracks)
	rep.Counts["vias"] = len(in.Vias)

	aesPlacement(rep, b, an, c, in)
	if in.Copper || len(in.Tracks) > 0 {
		aesRouting(rep, b, an, in)
	} else {
		for _, m := range routingMetricIDs {
			rep.Metrics = append(rep.Metrics, AesMetric{ID: m[0], Group: "routing", Name: m[1], Skipped: true,
				Reason: "no routed copper (placement-only board / dump without --include-copper)"})
		}
		rep.RoutedShare = 0
	}
	// plan.json must always marshal: no NaN / Inf leaves the analyser.
	fin := func(v float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return v
	}
	for i := range rep.Metrics {
		m := &rep.Metrics[i]
		if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) || math.IsNaN(m.Score) {
			m.Skipped, m.Reason = true, "degenerate geometry (non-finite value)"
		}
		m.Value, m.Score = fin(m.Value), fin(m.Score)
		for k, v := range m.Extra {
			m.Extra[k] = fin(v)
		}
	}
	for i := range rep.Symmetry {
		rep.Symmetry[i].Error = fin(rep.Symmetry[i].Error)
	}
	sum := map[string]float64{}
	n := map[string]float64{}
	for _, m := range rep.Metrics {
		if m.Skipped {
			continue
		}
		w := in.Profile.metricWeight(m.ID)
		sum[m.Group] += w * m.Score
		n[m.Group] += w
	}
	groups := 0.0
	if n["placement"] > 0 { // weighted means (profile metric weights)
		rep.Placement = round3(sum["placement"] / n["placement"])
		rep.Score += rep.Placement
		groups++
	}
	if n["routing"] > 0 {
		rep.Routing = round3(rep.RoutedShare * sum["routing"] / n["routing"])
		rep.Score += rep.Routing
		groups++
	}
	if groups > 0 {
		rep.Score = round3(rep.Score / groups)
	}
	return rep
}

var routingMetricIDs = [][2]string{
	{"R1", "off-octilinear length share"},
	{"R2", "pad-entry quality"},
	{"R3", "S-jogs"},
	{"R4", "bend density"},
	{"R5", "layer-direction discipline"},
	{"R6", "parallel spacing CV"},
	{"R7", "via / vertex grid landing"},
	{"R8", "max single-net detour"},
	{"R9", "dangling stubs"},
}

// capWorst sorts offenders by descending value and keeps the first n.
func capWorst(w []AesOffender, n int) []AesOffender {
	sort.SliceStable(w, func(i, j int) bool {
		if w[i].Value != w[j].Value {
			return w[i].Value > w[j].Value
		}
		return w[i].Ref+w[i].Net+w[i].ID < w[j].Ref+w[j].Net+w[j].ID
	})
	if len(w) > n {
		w = w[:n]
	}
	return w
}

const aesMaxWorst = 8

func ptr(p Point) *Point { q := Point{round2(p.X), round2(p.Y)}; return &q }

// ---------------------------------------------------------------------------
// routing
// ---------------------------------------------------------------------------

type aSeg struct {
	net        string
	layer      int
	a, b       Point
	w, l, ang  float64
	id         string
	exempt     string // non-empty: exempt from penalties (reason)
	padA, padB *Pad
	viaA, viaB bool
}

func (s *aSeg) end(e int) Point {
	if e == 0 {
		return s.a
	}
	return s.b
}
func (s *aSeg) other(e int) Point {
	if e == 0 {
		return s.b
	}
	return s.a
}
func (s *aSeg) pad(e int) *Pad {
	if e == 0 {
		return s.padA
	}
	return s.padB
}
func (s *aSeg) via(e int) bool {
	if e == 0 {
		return s.viaA
	}
	return s.viaB
}

// aKey merges endpoints within 0.05 mil (baseline script TOL).
type aKey struct {
	net   string
	layer int
	x, y  int64
}

func keyOf(net string, layer int, p Point) aKey {
	return aKey{net, layer, int64(math.Round(p.X / 0.05)), int64(math.Round(p.Y / 0.05))}
}

type segEnd struct{ i, e int }

type aBend struct {
	net   string
	layer int
	pt    Point
	turn  float64
	cls   string // collinear | 45 | 90 | acute | odd
	i, j  int
	wchg  bool
}

func aesRouting(rep *AestheticsReport, b *Board, an *Analysis, in AesInput) {
	pix := newPadIndex(b)
	// ---- exemptions, decided before measuring --------------------------
	exemptNet := map[string]string{}
	var diffNets, rfNets, tunedNets []string
	nets := an.Nets
	if in.NoExemptions {
		nets = nil
		rep.Notes = append(rep.Notes, "NoExemptions: differential, RF, tuned, via-array and isolation copper measured like any other (diagnostic mode)")
	}
	for _, np := range nets {
		switch {
		case np.Role == RoleDiff || np.PairWith != "":
			exemptNet[np.Net] = "diff-pair"
			diffNets = append(diffNets, np.Net)
		case np.Role == RoleRF:
			exemptNet[np.Net] = "rf"
			rfNets = append(rfNets, np.Net)
		case np.LengthGroup != "":
			exemptNet[np.Net] = "length-tuned"
			tunedNets = append(tunedNets, np.Net)
		}
	}
	// Partial exemptions: the net is measured, but the metric its electrical
	// rule shapes is not charged.
	//   hv-clearance — a per-pair / HV clearance above the board rule
	//     (intent isolation, hvrelief): its detour and spacing follow
	//     creepage (R6, R8);
	//   via-current  — vias sized / multiplied for current (intent vias,
	//     ViasPerTransition > 1, IR-drop ExtraVias): arrays, not clutter (R7).
	hvNet, viaCurNet := map[string]bool{}, map[string]bool{}
	var hvItems, viaCurItems []string
	for _, np := range nets {
		if np.ClearanceMil > b.Rules.Clearance+0.5 {
			hvNet[np.Net] = true
			hvItems = append(hvItems, np.Net)
		}
		if np.ViasPerTransition > 1 || np.ExtraVias > 0 {
			viaCurNet[np.Net] = true
			viaCurItems = append(viaCurItems, np.Net)
		}
	}
	addEx := func(kind, metrics, why string, items []string) {
		if len(items) == 0 {
			return
		}
		sort.Strings(items)
		rep.Exemptions = append(rep.Exemptions, AesExemption{Kind: kind, Metrics: metrics, Items: items, Why: why})
	}
	addEx("diff-pair", "R1–R9", "coupling, symmetry and skew are set by SI rules, not looks ([plan] §2.4, [kimi] R3/R5)", diffNets)
	addEx("rf", "R1–R9", "RF feed geometry follows impedance and the antenna keep-out ([plan] §2.4)", rfNets)
	addEx("length-tuned", "R1–R9", "serpentine / length-matched nets bend on purpose (tune.go; [fable] R1)", tunedNets)
	addEx("hv-clearance", "R6, R8", "per-pair / HV clearance above the board rule: detours and spacing follow creepage, not looks", hvItems)
	addEx("via-current", "R7", "vias sized or multiplied for current (intent / IR-drop feedback) are arrays, not clutter", viaCurItems)

	viaIDs := func(i int) string {
		if i < len(in.ViaIDs) && in.ViaIDs[i] != "" {
			return in.ViaIDs[i]
		}
		return fmt.Sprintf("auto-v%d", i+1)
	}
	trackID := func(i int) string {
		if i < len(in.TrackIDs) && in.TrackIDs[i] != "" {
			return in.TrackIDs[i]
		}
		return fmt.Sprintf("auto-t%d", i+1)
	}
	// Power via arrays: ≥3 vias of one ground/power net within 2.5 via
	// diameters of each other are a current / stitching array, spaced for
	// ampacity ([plan] §2.4, [fable] R6, [agent] R8).
	arrayVia := map[int]bool{}
	{
		byNet := map[string][]int{}
		for i, v := range in.Vias {
			r := an.Plan(v.Net, b.Rules).Role
			if r == RoleGround || r == RolePower {
				byNet[v.Net] = append(byNet[v.Net], i)
			}
		}
		var items []string
		nets := make([]string, 0, len(byNet))
		for n := range byNet {
			nets = append(nets, n)
		}
		sort.Strings(nets)
		for _, net := range nets {
			idx := byNet[net]
			par := map[int]int{}
			var find func(int) int
			find = func(a int) int {
				if _, ok := par[a]; !ok {
					par[a] = a
				}
				for par[a] != a {
					par[a] = par[par[a]]
					a = par[a]
				}
				return a
			}
			for x := 0; x < len(idx); x++ {
				for y := x + 1; y < len(idx); y++ {
					vi, vj := in.Vias[idx[x]], in.Vias[idx[y]]
					if vi.C.Dist(vj.C) <= 2.5*math.Max(math.Max(vi.Dia, vj.Dia), b.Rules.ViaDia) {
						par[find(idx[x])] = find(idx[y])
					}
				}
			}
			cl := map[int][]int{}
			for _, i := range idx {
				cl[find(i)] = append(cl[find(i)], i)
			}
			for _, g := range cl {
				if len(g) >= 3 && !in.NoExemptions {
					for _, i := range g {
						arrayVia[i] = true
						items = append(items, net+":"+viaIDs(i))
					}
				}
			}
		}
		addEx("power-via-array", "R7", "current / stitching via arrays are spaced for ampacity, not the grid", items)
	}
	inZone := func(p Point) bool {
		for _, z := range in.ExemptZones {
			if len(z) >= 3 && (PolyContains(z, p) || PolyEdgeDist(z, p) < 2*b.Rules.Clearance) {
				return true
			}
		}
		return false
	}
	// ---- segments ------------------------------------------------------
	var segs []*aSeg
	viaAt := map[[2]int64]bool{}
	for _, v := range in.Vias {
		viaAt[[2]int64{int64(math.Round(v.C.X / 0.05)), int64(math.Round(v.C.Y / 0.05))}] = true
	}
	isVia := func(p Point) bool {
		return viaAt[[2]int64{int64(math.Round(p.X / 0.05)), int64(math.Round(p.Y / 0.05))}]
	}
	var zoneItems []string
	for i, t := range in.Tracks {
		if t.A.Dist(t.B) < 1e-6 || t.Net == "" {
			continue
		}
		s := &aSeg{net: t.Net, layer: t.Layer, a: t.A, b: t.B, w: t.Width, l: t.A.Dist(t.B), ang: angDeg(t.A, t.B), id: trackID(i)}
		s.exempt = exemptNet[t.Net]
		if s.exempt == "" && !in.NoExemptions && len(in.ExemptZones) > 0 && inZone(t.A.Add(t.B).Scale(0.5)) {
			s.exempt = "isolation-band"
			zoneItems = append(zoneItems, t.Net+":"+s.id)
		}
		s.padA, s.padB = pix.at(s.a, s.layer, s.net), pix.at(s.b, s.layer, s.net)
		s.viaA, s.viaB = isVia(s.a), isVia(s.b)
		segs = append(segs, s)
	}
	addEx("isolation-band", "R1–R9", "copper in an isolation band / around a slot follows creepage ([plan] §2.4)", zoneItems)
	rep.Counts["segments"] = len(segs)
	var total, totalCounted float64
	for _, s := range segs {
		total += s.l
		if s.exempt == "" {
			totalCounted += s.l
		}
	}
	rep.Counts["routedLenMil"] = int(math.Round(total))
	if len(segs) == 0 {
		for _, m := range routingMetricIDs {
			rep.Metrics = append(rep.Metrics, AesMetric{ID: m[0], Group: "routing", Name: m[1], Skipped: true, Reason: "copper section has no tracks"})
		}
		return
	}
	// adjacency per (net, layer, point)
	adj := map[aKey][]segEnd{}
	for i, s := range segs {
		adj[keyOf(s.net, s.layer, s.a)] = append(adj[keyOf(s.net, s.layer, s.a)], segEnd{i, 0})
		adj[keyOf(s.net, s.layer, s.b)] = append(adj[keyOf(s.net, s.layer, s.b)], segEnd{i, 1})
	}
	keys := make([]aKey, 0, len(adj))
	for k := range adj {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		if a.net != c.net {
			return a.net < c.net
		}
		if a.layer != c.layer {
			return a.layer < c.layer
		}
		if a.x != c.x {
			return a.x < c.x
		}
		return a.y < c.y
	})
	// ---- bends (degree-2 vertices off pads and vias) --------------------
	var bends []aBend
	for _, k := range keys {
		lst := adj[k]
		if len(lst) != 2 {
			continue
		}
		si, sj := segs[lst[0].i], segs[lst[1].i]
		pt := si.end(lst[0].e)
		if isVia(pt) || si.pad(lst[0].e) != nil {
			continue
		}
		din := angDeg(si.other(lst[0].e), pt)
		dout := angDeg(pt, sj.other(lst[1].e))
		turn := math.Abs(math.Mod(dout-din+540, 360) - 180)
		cls := "odd"
		switch {
		case turn < aesAngTol:
			cls = "collinear"
		case math.Abs(turn-45) < aesAngTol:
			cls = "45"
		case math.Abs(turn-90) < aesAngTol:
			cls = "90"
		case turn > 90+aesAngTol:
			cls = "acute"
		}
		bends = append(bends, aBend{net: si.net, layer: si.layer, pt: pt, turn: turn, cls: cls, i: lst[0].i, j: lst[1].i, wchg: si.w != sj.w})
	}
	segBends := map[int][]int{}
	for bi, bd := range bends {
		segBends[bd.i] = append(segBends[bd.i], bi)
		segBends[bd.j] = append(segBends[bd.j], bi)
	}

	// ---- R1 off-octilinear ---------------------------------------------
	{
		m := AesMetric{ID: "R1", Group: "routing", Name: "off-octilinear length share", Unit: "fraction of routed length"}
		var off, micro, padEnd, mid, inner float64
		count, microN, innerN := 0, 0, 0
		var worst []AesOffender
		for _, s := range segs {
			if s.exempt != "" {
				continue
			}
			d := octiDev(s.ang)
			if d <= aesAngTol {
				continue
			}
			if s.padA != nil && s.padA == s.padB {
				// wholly inside one pad: hidden under the copper ([fable] R2
				// "pad 内段不计"); reported, not scored
				inner += s.l
				innerN++
				continue
			}
			count++
			off += s.l
			if s.padA != nil || s.padB != nil {
				padEnd += s.l
			} else {
				mid += s.l
			}
			if d < 3 {
				// 0.5–3°: the "almost straight" skew reads worst ([agent] R2):
				// counted twice in the scored share.
				micro += s.l
				microN++
			}
			pd := s.padA
			if pd == nil {
				pd = s.padB
			}
			o := AesOffender{Net: s.net, ID: s.id, Value: round2(s.l * d / 45), At: ptr(s.a.Add(s.b).Scale(0.5)),
				Note: fmt.Sprintf("%.1f mil, %.1f° off, layer %d", s.l, d, s.layer)}
			if pd != nil {
				o.Ref = pd.Key()
			}
			worst = append(worst, o)
		}
		if totalCounted <= 0 {
			m.Skipped, m.Reason = true, "every track is exempt"
		} else {
			m.Value = round3(off / totalCounted)
			weighted := (off + micro) / totalCounted
			// ramp: 0 → 100, 8 % → 0 ([fable] R2 ramp(f, 0, 0.08)); the
			// micro-skew double weight is [agent] R2. Phase B target ≤ 2 %.
			m.Score = aesRound(aesRamp(weighted, 0, 0.08))
			m.Detail = fmt.Sprintf("%d segments / %.0f mil off 45°-multiples (%.0f mil at a pad end, %.0f mid-route); %d micro-skews (0.5–3°) / %.0f mil",
				count, off, padEnd, mid, microN, micro)
			m.Extra = map[string]float64{"count": float64(count), "lenMil": round2(off), "padEndLenMil": round2(padEnd), "midLenMil": round2(mid),
				"microCount": float64(microN), "microLenMil": round2(micro), "weightedShare": round3(weighted),
				"padInternalCount": float64(innerN), "padInternalLenMil": round2(inner), "shareInclPadInternal": round3((off + inner) / totalCounted)}
			m.Worst = capWorst(worst, aesMaxWorst)
		}
		rep.Metrics = append(rep.Metrics, m)
	}

	// ---- R2 pad entry --------------------------------------------------
	{
		m := AesMetric{ID: "R2", Group: "routing", Name: "pad-entry quality", Unit: "mean entry score (0–1)"}
		n, axial, diag, skew, round, offc, corner := 0, 0, 0, 0, 0, 0, 0
		sum := 0.0
		var worst []AesOffender
		for _, s := range segs {
			if s.exempt != "" {
				continue
			}
			for e := 0; e < 2; e++ {
				pd := s.pad(e)
				if pd == nil {
					continue
				}
				pt, far := s.end(e), s.other(e)
				if inPad(pd, far, -0.01) {
					continue // the whole segment is inside the pad
				}
				n++
				u := padLocal(pd, pt)
				half := math.Min(pd.Box.W, pd.Box.H) / 2
				off := 0.0
				if half > 0 {
					off = math.Hypot(u.X, u.Y) / half
				}
				rel := math.Mod(angDeg(pt, far)-pd.Box.Rot+720, 90)
				dev := math.Min(rel, 90-rel)
				cls := "skew"
				switch {
				case pd.Box.Round && math.Abs(pd.Box.W-pd.Box.H) < 0.5:
					cls = "round"
				case dev < aesAngTol:
					cls = "axial"
				case math.Abs(dev-45) < aesAngTol:
					cls = "diag"
				}
				// Where the ray from the entry point towards the far end
				// leaves the pad, and how close that is to a corner.
				f := padLocal(pd, far)
				du, dv := f.X-u.X, f.Y-u.Y
				tmax := math.Inf(1)
				for _, ax := range [][3]float64{{u.X, du, pd.Box.W / 2}, {u.Y, dv, pd.Box.H / 2}} {
					if math.Abs(ax[1]) > 1e-9 {
						for _, bnd := range []float64{ax[2], -ax[2]} {
							if t := (bnd - ax[0]) / ax[1]; t > 0 {
								tmax = math.Min(tmax, t)
							}
						}
					}
				}
				isCorner := false
				if !math.IsInf(tmax, 1) && cls != "axial" && cls != "round" {
					eu, ev := u.X+du*tmax, u.Y+dv*tmax
					cd := math.Inf(1)
					for _, cu := range []float64{pd.Box.W / 2, -pd.Box.W / 2} {
						for _, cv := range []float64{pd.Box.H / 2, -pd.Box.H / 2} {
							cd = math.Min(cd, math.Hypot(eu-cu, ev-cv))
						}
					}
					isCorner = cd < s.w/2
				}
				// Entry score ([agent] R5): axial 1, 45° 0.7, skew 0; a
				// corner exit −0.5; an off-centre end (> 5 % of the half
				// short side: the baseline script's threshold) halves it.
				sc := 0.0
				switch cls {
				case "axial", "round":
					sc = 1
				case "diag":
					sc = 0.7
				}
				if isCorner {
					sc -= 0.5
					corner++
				}
				if off > 0.05 {
					sc *= 0.5
					offc++
				}
				sc = math.Max(sc, 0)
				sum += sc
				switch cls {
				case "axial":
					axial++
				case "diag":
					diag++
				case "skew":
					skew++
				case "round":
					round++
				}
				if sc < 1 {
					worst = append(worst, AesOffender{Ref: pd.Key(), Net: s.net, ID: s.id, Value: round2(1 - sc), At: ptr(pt),
						Note: fmt.Sprintf("%s entry, %.1f° off pad axis, centre offset %.2f×half-width%s", cls, dev, off, map[bool]string{true: ", corner exit"}[isCorner])})
				}
			}
		}
		if n == 0 {
			m.Skipped, m.Reason = true, "no track enters a pad"
		} else {
			m.Value = round3(sum / float64(n))
			m.Score = aesRound(100 * sum / float64(n))
			f := func(k int) float64 { return round3(float64(k) / float64(n)) }
			m.Detail = fmt.Sprintf("%d entries: axial %d (%.0f%%), 45° %d (%.0f%%), skew %d (%.0f%%), round %d; off-centre %d, corner exits %d",
				n, axial, 100*f(axial), diag, 100*f(diag), skew, 100*f(skew), round, offc, corner)
			m.Extra = map[string]float64{"entries": float64(n), "axial": float64(axial), "diag": float64(diag), "skew": float64(skew), "round": float64(round),
				"axialShare": f(axial), "diagShare": f(diag), "skewShare": f(skew), "offCentre": float64(offc), "cornerExits": float64(corner)}
			m.Worst = capWorst(worst, aesMaxWorst)
		}
		rep.Metrics = append(rep.Metrics, m)
	}

	// ---- R3 S-jogs -----------------------------------------------------
	{
		m := AesMetric{ID: "R3", Group: "routing", Name: "S-jogs", Unit: "per inch of routed length"}
		jogs, necks := 0, 0
		var offs []float64
		var worst []AesOffender
		for mi, s := range segs {
			if s.exempt != "" {
				continue
			}
			var bs []int
			for _, bi := range segBends[mi] {
				if bends[bi].cls != "collinear" {
					bs = append(bs, bi)
				}
			}
			if len(bs) != 2 {
				continue
			}
			nb := func(bi int) int {
				if bends[bi].i == mi {
					return bends[bi].j
				}
				return bends[bi].i
			}
			s1, s2 := segs[nb(bs[0])], segs[nb(bs[1])]
			pA, pB := bends[bs[0]].pt, bends[bs[1]].pt
			dirTo := func(x *aSeg, to Point) float64 {
				a, c := x.a, x.b
				if c.Dist(to) > a.Dist(to) {
					a, c = c, a
				}
				return angDeg(a, c)
			}
			d1 := dirTo(s1, pA)
			d3 := math.Mod(dirTo(s2, pB)+180, 360)
			netTurn := math.Abs(math.Mod(d3-d1+540, 360) - 180)
			if netTurn >= aesAngTol {
				continue
			}
			ux, uy := math.Cos(d1*math.Pi/180), math.Sin(d1*math.Pi/180)
			off := math.Abs((pB.X-pA.X)*-uy + (pB.Y-pA.Y)*ux)
			// An S-jog: in and out run parallel and the middle leg shifts
			// them sideways by < 4 track widths (or is < 25 mil long) — the
			// baseline script's definition ([agent] M3).
			if !in.NoExemptions && (s.w != s1.w || s.w != s2.w) {
				// A width change inside the jog is an intent neck (a wide
				// power track stepping down to a pad): electrical, not a jog.
				necks++
				continue
			}
			if off < 4*math.Max(s.w, s1.w) || s.l < 25 {
				jogs++
				offs = append(offs, off)
				worst = append(worst, AesOffender{Net: s.net, ID: s.id, Value: round2(off), At: ptr(pA),
					Note: fmt.Sprintf("layer %d, lateral offset %.1f mil, middle leg %.1f mil", s.layer, off, s.l)})
			}
		}
		perIn := float64(jogs) / math.Max(totalCounted/1000, 1e-9)
		m.Value = round3(perIn)
		// ramp: 0/in → 100, 1/in → 0 ([agent] R3).
		m.Score = aesRound(aesRamp(perIn, 0, 1))
		sort.Float64s(offs)
		med := 0.0
		if len(offs) > 0 {
			med = offs[len(offs)/2]
		}
		m.Detail = fmt.Sprintf("%d S-jogs over %.2f in, median lateral offset %.1f mil", jogs, totalCounted/1000, med)
		m.Extra = map[string]float64{"count": float64(jogs), "offsetMedianMil": round2(med), "neckTransitionsExempt": float64(necks)}
		m.Worst = capWorst(worst, aesMaxWorst)
		// smallest offsets first: those are the one-grid-cell jogs
		sort.SliceStable(m.Worst, func(i, j int) bool { return m.Worst[i].Value < m.Worst[j].Value })
		rep.Metrics = append(rep.Metrics, m)
	}

	// ---- R4 bend density -----------------------------------------------
	{
		m := AesMetric{ID: "R4", Group: "routing", Name: "bend density", Unit: "bends per inch"}
		cls := map[string]int{}
		collinearW := 0
		perNet := map[string]int{}
		var worst []AesOffender
		for _, bd := range bends {
			if segs[bd.i].exempt != "" {
				continue
			}
			cls[bd.cls]++
			if bd.cls == "collinear" {
				if bd.wchg {
					collinearW++
				}
				continue
			}
			perNet[bd.net]++
			if bd.cls == "90" || bd.cls == "acute" || bd.cls == "odd" {
				worst = append(worst, AesOffender{Net: bd.net, At: ptr(bd.pt), Value: round2(bd.turn), Note: fmt.Sprintf("%s turn %.0f° on layer %d", bd.cls, bd.turn, bd.layer)})
			}
		}
		nb := 0
		for k, v := range cls {
			if k != "collinear" {
				nb += v
			}
		}
		perIn := float64(nb) / math.Max(totalCounted/1000, 1e-9)
		m.Value = round3(perIn)
		// ramp: 4/in → 100, 15/in → 0 ([fable] R1); each 90° corner −10,
		// any acute corner caps at 0 ([agent] R4: acid traps).
		sc := aesRamp(perIn, 4, 15) - 10*float64(cls["90"])
		if cls["acute"] > 0 {
			sc = 0
		}
		m.Score = aesRound(clamp(sc, 0, 100))
		m.Detail = fmt.Sprintf("%d bends over %.2f in (45° %d, 90° %d, acute %d, odd %d); %d redundant collinear vertices (%d at a width change)",
			nb, totalCounted/1000, cls["45"], cls["90"], cls["acute"], cls["odd"], cls["collinear"], collinearW)
		m.Extra = map[string]float64{"bends": float64(nb), "b45": float64(cls["45"]), "b90": float64(cls["90"]), "acute": float64(cls["acute"]),
			"odd": float64(cls["odd"]), "collinear": float64(cls["collinear"]), "collinearWidthChange": float64(collinearW)}
		nets := make([]string, 0, len(perNet))
		for n := range perNet {
			nets = append(nets, n)
		}
		for _, n := range nets {
			worst = append(worst, AesOffender{Net: n, Value: float64(perNet[n]), Note: fmt.Sprintf("%d bends", perNet[n])})
		}
		m.Worst = capWorst(worst, aesMaxWorst)
		rep.Metrics = append(rep.Metrics, m)
	}

	// ---- R5 layer direction --------------------------------------------
	{
		m := AesMetric{ID: "R5", Group: "routing", Name: "layer-direction discipline", Unit: "weighted preferred-axis share"}
		pref := map[int]string{}
		if in.Stackup != nil {
			for _, l := range in.Stackup.Stack {
				if l.Dir != "" {
					pref[l.ID] = l.Dir
				}
			}
		}
		prefOf := func(layer int) string {
			if d, ok := pref[layer]; ok {
				return d
			}
			switch {
			case layer == LayerTop:
				return "h"
			case layer == LayerBottom:
				return "v"
			case layer >= LayerInner1:
				if (layer-LayerInner1)%2 == 1 {
					return "v"
				}
				return "h"
			}
			return ""
		}
		// Component layers route in every direction (fan-out, escapes):
		// their discipline weighs 0.25, the others 1 ([plan] §3 裁决,
		// [agent] R7 "只在非器件层算").
		smdOn := map[int]int{}
		for _, p := range b.Parts {
			for _, pd := range p.Pads {
				if pd.Layer != LayerMulti {
					smdOn[pd.Layer]++
				}
			}
		}
		type lay struct{ total, H, V, D, X float64 }
		L := map[int]*lay{}
		var worst []AesOffender
		for _, s := range segs {
			if s.exempt != "" {
				continue
			}
			l := L[s.layer]
			if l == nil {
				l = &lay{}
				L[s.layer] = l
			}
			l.total += s.l
			c := axisClass(s.ang)
			switch c {
			case 'H':
				l.H += s.l
			case 'V':
				l.V += s.l
			case 'D':
				l.D += s.l
			default:
				l.X += s.l
			}
			pd := prefOf(s.layer)
			if s.l >= 150 && (pd == "h" && c == 'V' || pd == "v" && c == 'H') {
				worst = append(worst, AesOffender{Net: s.net, ID: s.id, Value: round2(s.l), At: ptr(s.a.Add(s.b).Scale(0.5)),
					Note: fmt.Sprintf("%.0f mil against layer %d's %s direction", s.l, s.layer, pd)})
			}
		}
		layers := make([]int, 0, len(L))
		for id := range L {
			layers = append(layers, id)
		}
		sort.Ints(layers)
		var wsum, ssum, vsum float64
		m.Extra = map[string]float64{}
		var parts []string
		for _, id := range layers {
			l := L[id]
			pd := prefOf(id)
			if pd == "" || l.total <= 0 {
				continue
			}
			share := l.H / l.total
			if pd == "v" {
				share = l.V / l.total
			}
			w := 1.0
			if smdOn[id] > 0 {
				w = 0.25
			}
			// ramp: preferred-axis share 95 % → 100, 60 % → 0 ([agent] R7).
			sc := aesRamp(-share, -0.95, -0.60)
			wsum += w * l.total
			ssum += w * l.total * sc
			vsum += w * l.total * share
			m.Extra[fmt.Sprintf("L%d.prefShare", id)] = round3(share)
			m.Extra[fmt.Sprintf("L%d.weight", id)] = w
			parts = append(parts, fmt.Sprintf("L%d(%s, w%.2f) H %.1f%% V %.1f%% D %.1f%% X %.1f%%", id, pd, w,
				100*l.H/l.total, 100*l.V/l.total, 100*l.D/l.total, 100*l.X/l.total))
		}
		if wsum <= 0 {
			m.Skipped, m.Reason = true, "no routed layer with a preferred direction"
		} else {
			m.Value = round3(vsum / wsum)
			m.Score = aesRound(ssum / wsum)
			m.Detail = strings.Join(parts, "; ") + fmt.Sprintf("; %d segments ≥150 mil against the layer direction", len(worst))
			m.Extra["wrongLong"] = float64(len(worst))
			m.Worst = capWorst(worst, aesMaxWorst)
		}
		rep.Metrics = append(rep.Metrics, m)
	}

	// ---- R6 parallel spacing CV ----------------------------------------
	rep.Metrics = append(rep.Metrics, aesParallel(segs, hvNet))

	// ---- R7 grid landing -----------------------------------------------
	{
		m := AesMetric{ID: "R7", Group: "routing", Name: "via / vertex grid landing", Unit: "share on the 5 mil grid"}
		vOn, vN := 0, 0
		var worst []AesOffender
		for i, v := range in.Vias {
			if arrayVia[i] || exemptNet[v.Net] != "" || viaCurNet[v.Net] {
				continue
			}
			vN++
			if onGrid(v.C.X, 5, 0.01) && onGrid(v.C.Y, 5, 0.01) {
				vOn++
			} else if len(worst) < 200 {
				worst = append(worst, AesOffender{Net: v.Net, ID: viaIDs(i), At: ptr(v.C),
					Value: round2(math.Hypot(v.C.X-5*math.Round(v.C.X/5), v.C.Y-5*math.Round(v.C.Y/5))), Note: "via off the 5 mil grid"})
			}
		}
		allOn, allN, freeOn, freeN := 0, 0, 0, 0
		for _, s := range segs {
			if s.exempt != "" {
				continue
			}
			for e := 0; e < 2; e++ {
				p := s.end(e)
				on := onGrid(p.X, 5, 0.01) && onGrid(p.Y, 5, 0.01)
				allN++
				if on {
					allOn++
				}
				if s.pad(e) == nil && !s.via(e) {
					freeN++
					if on {
						freeOn++
					}
				}
			}
		}
		if vN+freeN == 0 {
			m.Skipped, m.Reason = true, "no free vertex or via to land"
		} else {
			vs, fs := 1.0, 1.0
			if vN > 0 {
				vs = float64(vOn) / float64(vN)
			}
			if freeN > 0 {
				fs = float64(freeOn) / float64(freeN)
			}
			// Score = 100 × mean(via share, free-vertex share) ([fable] R6
			// "grid 比例直接"); pad-anchored vertices follow the pad.
			m.Value = round3((vs + fs) / 2)
			m.Score = aesRound(100 * (vs + fs) / 2)
			m.Detail = fmt.Sprintf("vias %d/%d on 5 mil; free vertices %d/%d; all track vertices %d/%d", vOn, vN, freeOn, freeN, allOn, allN)
			m.Extra = map[string]float64{"viasOn": float64(vOn), "vias": float64(vN), "freeVertOn": float64(freeOn), "freeVert": float64(freeN),
				"allVertOn": float64(allOn), "allVert": float64(allN)}
			m.Worst = capWorst(worst, aesMaxWorst)
		}
		rep.Metrics = append(rep.Metrics, m)
	}

	// ---- connectivity: R8 detour, R9 stubs, routed share ----------------
	aesConnectivity(rep, b, an, in, segs, adj, pix, hvNet)
}

func aesRound(v float64) float64 { return math.Round(v*10) / 10 }

// aesParallel is R6: edge-gap CV inside bundles of parallel neighbouring
// tracks (same layer, different nets, same direction, overlap ≥ 30 mil,
// edge gap ≤ 25 mil — the baseline script's M6 bundle definition).
func aesParallel(segs []*aSeg, hvNet map[string]bool) AesMetric {
	m := AesMetric{ID: "R6", Group: "routing", Name: "parallel spacing CV", Unit: "median bundle gap CV"}
	type cand struct {
		i   int
		cls byte
	}
	const cell = 60.0
	grid := map[[3]int][]cand{}
	var cands []cand
	for i, s := range segs {
		if s.exempt != "" || s.l < 30 || hvNet[s.net] {
			continue
		}
		c := axisClass(s.ang)
		if c == 'X' {
			continue
		}
		cd := cand{i, c}
		cands = append(cands, cd)
		bb := EmptyRect().AddPoint(s.a).AddPoint(s.b).Expand(25 + s.w)
		for x := int(math.Floor(bb.MinX / cell)); x <= int(math.Floor(bb.MaxX/cell)); x++ {
			for y := int(math.Floor(bb.MinY / cell)); y <= int(math.Floor(bb.MaxY/cell)); y++ {
				grid[[3]int{s.layer, x, y}] = append(grid[[3]int{s.layer, x, y}], cd)
			}
		}
	}
	type pair struct {
		i, j    int
		gap, ov float64
	}
	var pairs []pair
	seen := map[[2]int]bool{}
	for _, ci := range cands {
		si := segs[ci.i]
		th := si.ang * math.Pi / 180
		ux, uy := math.Cos(th), math.Sin(th)
		bb := EmptyRect().AddPoint(si.a).AddPoint(si.b)
		for x := int(math.Floor(bb.MinX / cell)); x <= int(math.Floor(bb.MaxX/cell)); x++ {
			for y := int(math.Floor(bb.MinY / cell)); y <= int(math.Floor(bb.MaxY/cell)); y++ {
				for _, cj := range grid[[3]int{si.layer, x, y}] {
					if cj.i <= ci.i || cj.cls != ci.cls || seen[[2]int{ci.i, cj.i}] {
						continue
					}
					sj := segs[cj.i]
					if sj.net == si.net {
						continue
					}
					if ci.cls == 'D' && math.Abs(math.Mod(sj.ang-si.ang+360, 180)) > 1 && math.Abs(math.Mod(sj.ang-si.ang+360, 180)-180) > 1 {
						continue
					}
					seen[[2]int{ci.i, cj.i}] = true
					perp := (sj.a.X-si.a.X)*-uy + (sj.a.Y-si.a.Y)*ux
					q0 := (sj.a.X-si.a.X)*ux + (sj.a.Y-si.a.Y)*uy
					q1 := (sj.b.X-si.a.X)*ux + (sj.b.Y-si.a.Y)*uy
					if q0 > q1 {
						q0, q1 = q1, q0
					}
					ov := math.Min(si.l, q1) - math.Max(0, q0)
					if ov < 30 {
						continue
					}
					gap := math.Abs(perp) - (si.w+sj.w)/2
					if gap < 0 || gap > 25 {
						continue
					}
					pairs = append(pairs, pair{ci.i, cj.i, gap, ov})
				}
			}
		}
	}
	par := map[int]int{}
	var find func(int) int
	find = func(a int) int {
		if _, ok := par[a]; !ok {
			par[a] = a
		}
		for par[a] != a {
			par[a] = par[par[a]]
			a = par[a]
		}
		return a
	}
	for _, p := range pairs {
		par[find(p.i)] = find(p.j)
	}
	bund := map[int][]pair{}
	for _, p := range pairs {
		bund[find(p.i)] = append(bund[find(p.i)], p)
	}
	roots := make([]int, 0, len(bund))
	for r := range bund {
		roots = append(roots, r)
	}
	sort.Ints(roots)
	var cvs []float64
	var worst []AesOffender
	for _, r := range roots {
		lst := bund[r]
		if len(lst) < 2 {
			continue
		}
		var sw, mu float64
		for _, p := range lst {
			sw += p.ov
			mu += p.gap * p.ov
		}
		mu /= sw
		v := 0.0
		for _, p := range lst {
			v += p.ov * (p.gap - mu) * (p.gap - mu)
		}
		cv := 0.0
		if mu > 0 {
			cv = math.Sqrt(v/sw) / mu
		}
		cvs = append(cvs, cv)
		nets := map[string]bool{}
		for _, p := range lst {
			nets[segs[p.i].net], nets[segs[p.j].net] = true, true
		}
		nl := make([]string, 0, len(nets))
		for n := range nets {
			nl = append(nl, n)
		}
		sort.Strings(nl)
		worst = append(worst, AesOffender{Net: strings.Join(nl, ","), Value: round3(cv), At: ptr(segs[lst[0].i].a),
			Note: fmt.Sprintf("%d pairs, mean gap %.1f mil", len(lst), mu)})
	}
	m.Extra = map[string]float64{"pairs": float64(len(pairs)), "bundles": float64(len(cvs))}
	if len(cvs) == 0 {
		m.Skipped = true
		m.Reason = fmt.Sprintf("no bundle of ≥2 parallel pairs (%d isolated pairs): CV undefined", len(pairs))
		return m
	}
	sort.Float64s(cvs)
	med := cvs[len(cvs)/2]
	m.Value = round3(med)
	// ramp: CV 0.05 → 100, 0.3 → 0 ([agent] R6, [fable] R7).
	m.Score = aesRound(aesRamp(med, 0.05, 0.3))
	m.Detail = fmt.Sprintf("%d parallel pairs in %d bundles; median bundle CV %.3f", len(pairs), len(cvs), med)
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}

// aesConnectivity measures per-net connectivity through the copper: R8 the
// worst single-net detour over fully routed nets, R9 dangling stubs, and the
// routed share that scales the routing group.
func aesConnectivity(rep *AestheticsReport, b *Board, an *Analysis, in AesInput, segs []*aSeg, adj map[aKey][]segEnd, pix *padIndex, hvNet map[string]bool) {
	plane := in.PlaneNets
	if plane == nil {
		plane = map[string]bool{}
	}
	pads := map[string][]*Pad{}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net != "" {
				pads[pd.Net] = append(pads[pd.Net], pd)
			}
		}
	}
	segsByNet := map[string][]int{}
	for i, s := range segs {
		segsByNet[s.net] = append(segsByNet[s.net], i)
	}
	viasByNet := map[string][]Via{}
	for _, v := range in.Vias {
		viasByNet[v.Net] = append(viasByNet[v.Net], v)
	}
	nets := make([]string, 0, len(pads))
	for n := range pads {
		nets = append(nets, n)
	}
	sort.Strings(nets)
	type det struct {
		net   string
		ratio float64
	}
	var dets []det
	totalConn, openConn := 0, 0
	stubs := 0
	var stubWorst []AesOffender
	for _, net := range nets {
		pl := pads[net]
		if len(pl) < 2 || plane[net] {
			continue
		}
		role := an.Plan(net, b.Rules).Role
		if role == RoleGround {
			continue // ground reaches its plane / pour
		}
		totalConn += len(pl) - 1
		si := segsByNet[net]
		// union-find over pads (0..n-1), segment ends (n+2i, n+2i+1), vias
		n := len(pl)
		nv := len(viasByNet[net])
		par := make([]int, n+2*len(si)+nv)
		for i := range par {
			par[i] = i
		}
		var find func(int) int
		find = func(a int) int {
			for par[a] != a {
				par[a] = par[par[a]]
				a = par[a]
			}
			return a
		}
		uni := func(a, c int) { par[find(a)] = find(c) }
		for k, i := range si {
			s := segs[i]
			uni(n+2*k, n+2*k+1)
			for e := 0; e < 2; e++ {
				pt := s.end(e)
				for pi, pd := range pl {
					if pd.OnLayer(s.layer) && inPad(pd, pt, 0.5) {
						uni(n+2*k+e, pi)
					}
				}
				for vi, v := range viasByNet[net] {
					if v.C.Dist(pt) <= math.Max(v.Dia/2, 1) {
						uni(n+2*k+e, n+2*len(si)+vi)
					}
				}
			}
		}
		// a via landing on a track's interior joins it (copper overlap)
		for vi, v := range viasByNet[net] {
			for k, i := range si {
				if PointSegDist(v.C, segs[i].a, segs[i].b) < segs[i].w/2+v.Dia/2-0.1 {
					uni(n+2*len(si)+vi, n+2*k)
				}
			}
		}
		// shared endpoints / T-junctions on the same layer
		for k, i := range si {
			for e := 0; e < 2; e++ {
				pt := segs[i].end(e)
				for k2, j := range si {
					if j == i || segs[j].layer != segs[i].layer {
						continue
					}
					if d := PointSegDist(pt, segs[j].a, segs[j].b); d <= math.Max(segs[j].w/2, 0.1) {
						uni(n+2*k+e, n+2*k2)
					}
				}
			}
		}
		comps := map[int]bool{}
		for pi := range pl {
			comps[find(pi)] = true
		}
		openConn += len(comps) - 1
		if len(comps) > 1 {
			rep.Notes = append(rep.Notes, fmt.Sprintf("open: %s — %d copper islands over %d pads", net, len(comps), len(pl)))
		}
		// R9 stubs: a free end (not on a pad, via or another segment).
		for k, i := range si {
			s := segs[i]
			if s.exempt != "" {
				continue
			}
			for e := 0; e < 2; e++ {
				pt := s.end(e)
				if s.pad(e) != nil || s.via(e) || len(adj[keyOf(s.net, s.layer, pt)]) > 1 {
					continue
				}
				joined := false
				for _, j := range si {
					if j != i && segs[j].layer == s.layer && PointSegDist(pt, segs[j].a, segs[j].b) <= math.Max(segs[j].w/2, 0.1) {
						joined = true
						break
					}
				}
				for _, v := range viasByNet[net] {
					if v.C.Dist(pt) <= v.Dia/2 {
						joined = true
					}
				}
				if !joined {
					stubs++
					stubWorst = append(stubWorst, AesOffender{Net: net, ID: s.id, At: ptr(pt), Value: round2(s.l), Note: fmt.Sprintf("free end on layer %d", s.layer)})
				}
				_ = k
			}
		}
		if len(comps) != 1 || len(si) == 0 || exemptNetOf(segs, si) || hvNet[net] {
			continue
		}
		var pts []Point
		for _, pd := range pl {
			pts = append(pts, pd.Box.C)
		}
		mst := euclidMST(pts)
		if mst <= 0 {
			continue
		}
		l := 0.0
		for _, i := range si {
			l += segs[i].l
		}
		dets = append(dets, det{net, l / mst})
	}
	rep.RoutedShare = 1
	if totalConn > 0 {
		rep.RoutedShare = round3(1 - float64(openConn)/float64(totalConn))
	}
	rep.Counts["connections"] = totalConn
	rep.Counts["openConnections"] = openConn
	// R8
	m := AesMetric{ID: "R8", Group: "routing", Name: "max single-net detour", Unit: "routed length / pad MST"}
	if len(dets) == 0 {
		m.Skipped, m.Reason = true, "no fully routed track net"
	} else {
		sort.Slice(dets, func(i, j int) bool {
			if dets[i].ratio != dets[j].ratio {
				return dets[i].ratio > dets[j].ratio
			}
			return dets[i].net < dets[j].net
		})
		m.Value = round3(dets[0].ratio)
		// ramp: worst net 1.5× → 100, 3× → 0 ([fable] R10: one net
		// looping round the board spoils it however tidy the rest is).
		m.Score = aesRound(aesRamp(dets[0].ratio, 1.5, 3))
		med := dets[len(dets)/2].ratio
		m.Detail = fmt.Sprintf("%d fully routed nets; worst %s %.2f×, median %.2f×", len(dets), dets[0].net, dets[0].ratio, med)
		m.Extra = map[string]float64{"nets": float64(len(dets)), "median": round3(med)}
		for _, d := range dets {
			if len(m.Worst) >= aesMaxWorst {
				break
			}
			m.Worst = append(m.Worst, AesOffender{Net: d.net, Value: round3(d.ratio), Note: fmt.Sprintf("routed %.2f× its pad MST", d.ratio)})
		}
	}
	rep.Metrics = append(rep.Metrics, m)
	// R9
	m9 := AesMetric{ID: "R9", Group: "routing", Name: "dangling stubs", Unit: "count"}
	m9.Value = float64(stubs)
	// −20 per stub ([fable] R9).
	m9.Score = aesRound(math.Max(0, 100-20*float64(stubs)))
	m9.Detail = fmt.Sprintf("%d track ends touching no pad, via or other track", stubs)
	m9.Worst = capWorst(stubWorst, aesMaxWorst)
	rep.Metrics = append(rep.Metrics, m9)
}

func exemptNetOf(segs []*aSeg, idx []int) bool {
	for _, i := range idx {
		if segs[i].exempt != "" {
			return true
		}
	}
	return false
}
