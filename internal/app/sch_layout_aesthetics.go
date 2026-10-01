package app

// sch_layout_aesthetics.go — schematic aesthetics Phase B (generation).
//
// An opt-in beautify pass that runs AFTER the layout solver has produced a
// complete, validated zone (sch lib-layout / layout-plan --aesthetics STYLE).
// It never changes the solver's search: with the option absent every output is
// byte-identical to before. Priority stays fixed (docs/reviews/2026-10-
// schematic-aesthetics/README.md §4): connectivity correctness > readability >
// aesthetics. Every candidate move is accepted only when
//
//   - validateLibGeometry and validateSchCompositionNets pass (the same gates
//     the solver uses: wire-through-body, foreign pin/stem, pin exit, marker
//     body/text, every pin on a named tree, no NC touched);
//   - the pin → net map, NC states and the physical wire islands are identical
//     (long-wire → label may split only non-direct islands that do not join a
//     core pin to a peripheral pin);
//   - the peripheral-direct ownership gate still passes;
//   - the offline sch check / layout-lint finding counts do not increase;
//   - the pkg/schaes objective improves (fewer readability defects first,
//     then a higher profile score).
//
// Otherwise the move is dropped (rolled back); the incumbent is never lost.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

// SchematicAestheticsOptions enables the Phase B beautify pass.
type SchematicAestheticsOptions struct {
	// Style: functional | balanced | precision | auto (same names as
	// sch aesthetics / pcb aesthetics). Empty = balanced.
	Style string `json:"style,omitempty"`
	// NativeBus additionally proposes a native bus line for every complete
	// virtual bus lane (report only; Apply never draws it). The native bus
	// actions are live-unverified, so this stays an explicit opt-in.
	NativeBus bool `json:"nativeBus,omitempty"`
}

// SchematicAestheticsPass counts the candidates of one move family.
type SchematicAestheticsPass struct {
	Pass     string         `json:"pass"`
	Tried    int            `json:"tried"`
	Accepted int            `json:"accepted"`
	Rejected map[string]int `json:"rejected,omitempty"`
}

// SchematicAestheticsReport documents what the pass did and proved.
type SchematicAestheticsReport struct {
	Style         string             `json:"style"`
	AutoReason    string             `json:"autoReason,omitempty"`
	Status        string             `json:"status"` // improved | unchanged | skipped
	Reason        string             `json:"reason,omitempty"`
	ScoreBefore   float64            `json:"scoreBefore"`
	ScoreAfter    float64            `json:"scoreAfter"`
	DefectsBefore int                `json:"defectsBefore"`
	DefectsAfter  int                `json:"defectsAfter"`
	MetricsBefore map[string]float64 `json:"metricsBefore"`
	MetricsAfter  map[string]float64 `json:"metricsAfter"`
	CheckBefore   map[string]int     `json:"checkBefore"`
	CheckAfter    map[string]int     `json:"checkAfter"`
	// PinNetIdentical: every pin keeps its net and NC state (always required).
	PinNetIdentical bool `json:"pinNetIdentical"`
	// ConnectivityIdentical: additionally the physical wire islands are
	// unchanged. Only an accepted long-wire → label split makes it false.
	ConnectivityIdentical bool                      `json:"connectivityIdentical"`
	LabelSplits           []string                  `json:"labelSplits,omitempty"`
	Passes                []SchematicAestheticsPass `json:"passes"`
	Evaluations           int                       `json:"evaluations"`
	BusLanes              []SchematicBusLane        `json:"busLanes,omitempty"`
	Priority              string                    `json:"priority"`
}

const schAesPriority = "connectivity > readability > aesthetics: every move re-passes geometry, nets, ownership and offline check/lint counts, else it is rolled back"

type aesEngine struct {
	profile      schaes.Profile
	policies     map[string]string
	roles        map[string]string
	coreID       string
	componentIDs map[string]string // designator → component id
	pinStates    map[string]map[string]string
	baseFP       aesFingerprint
	baseCheck    map[string]int
	evals        int
	evalLimit    int
	work         int // validations and measurements; bounded by evalLimit·5
	routing      *schematicRoutingContext
	passes       map[string]*SchematicAestheticsPass
	order        []string
	splits       []string
	kinds        map[string]string // net → marker kind seen in the solver output
	nativeBus    bool
	lanes        []SchematicBusLane
}

func (e *aesEngine) pass(name string) *SchematicAestheticsPass {
	if e.passes[name] == nil {
		e.passes[name] = &SchematicAestheticsPass{Pass: name, Rejected: map[string]int{}}
		e.order = append(e.order, name)
	}
	return e.passes[name]
}

func (e *aesEngine) budgetLeft() bool { return e.evals < e.evalLimit && e.work < 5*e.evalLimit }

// resolveSchAesProfile maps a style word to a profile (auto from the zone).
func resolveSchAesProfile(style string, s *schaes.Snapshot) (schaes.Profile, error) {
	if style == "auto" {
		return schaes.AutoProfile(s), nil
	}
	return schaes.ProfileByName(style)
}

func clonePowerLayoutPlan(p *powerLayoutPlan) *powerLayoutPlan {
	q := *p
	q.Placements = append([]powerLayoutPlacement(nil), p.Placements...)
	q.Wires = clonePowerLayoutWires(p.Wires)
	q.Flags = append([]powerLayoutFlag(nil), p.Flags...)
	return &q
}

func (e *aesEngine) snapshot(p *powerLayoutPlan) (*schaes.Snapshot, error) {
	flags := make([]powerLayoutFlag, len(p.Flags))
	for i, f := range p.Flags {
		f.Anchor = nil
		flags[i] = f
	}
	raw, err := json.Marshal(struct {
		Placements   []powerLayoutPlacement       `json:"placements"`
		Wires        []powerLayoutWire            `json:"wires"`
		Flags        []powerLayoutFlag            `json:"flags"`
		ComponentIDs map[string]string            `json:"componentIds"`
		PinStates    map[string]map[string]string `json:"pinStates"`
	}{p.Placements, append([]powerLayoutWire{}, p.Wires...), append([]powerLayoutFlag{}, flags...), e.componentIDs, e.pinStates})
	if err != nil {
		return nil, err
	}
	return schaes.Parse(raw)
}

func (e *aesEngine) measure(p *powerLayoutPlan) (*schaes.Report, libAesObjective) {
	e.evals++
	e.work += 4
	s, err := e.snapshot(p)
	if err != nil {
		return nil, libAesObjective{Defects: math.MaxInt32}
	}
	prof := e.profile
	r := schaes.Analyze(s, &prof)
	return r, schAesObjective(r)
}

func schAesObjective(r *schaes.Report) libAesObjective {
	extra := func(id, key string) int {
		m := r.Metric(id)
		if m == nil || m.Skipped {
			return 0
		}
		if key == "" {
			return int(math.Round(m.Value))
		}
		return int(math.Round(m.Extra[key]))
	}
	o := libAesObjective{Score: r.Score, Classes: [5]int{extra("W2", "crossings"), extra("W3", ""), extra("W4", ""), extra("W7", "segments"), extra("L6", "overlaps")}}
	for _, n := range o.Classes {
		o.Defects += n
	}
	return o
}

func schAesMetricScores(r *schaes.Report) map[string]float64 {
	out := map[string]float64{"score": r.Score}
	for _, m := range r.Metrics {
		if !m.Skipped {
			out[m.ID] = m.Score
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// connectivity fingerprint

type aesFingerprint struct {
	net    map[string]string // pin key → net ("" = NC/unconnected)
	island map[string]string // pin key → canonical physical island
	core   map[string]bool   // pin key belongs to the core component
}

func (e *aesEngine) pinKey(designator, number string) string {
	if id := e.componentIDs[designator]; id != "" {
		return id + "." + number
	}
	return designator + "." + number
}

func (e *aesEngine) fingerprint(p *powerLayoutPlan) aesFingerprint {
	fp := aesFingerprint{net: map[string]string{}, island: map[string]string{}, core: map[string]bool{}}
	parent := make([]int, len(p.Wires))
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i, w := range p.Wires {
		for j := 0; j < i; j++ {
			v := p.Wires[j]
			if w.Net == v.Net && len(w.Points) == 2 && len(v.Points) == 2 && plSegmentsContact(w.Points[0], w.Points[1], v.Points[0], v.Points[1]) {
				parent[root(i)] = root(j)
			}
		}
	}
	groups := map[string][]string{}
	for _, c := range p.Placements {
		for _, q := range c.Pins {
			k := e.pinKey(c.Designator, q.Number)
			fp.net[k] = q.Net
			fp.core[k] = e.componentIDs[c.Designator] == e.coreID
			g := "pin:" + k
			for i, w := range p.Wires {
				if q.Net != "" && w.Net == q.Net && len(w.Points) == 2 && plOnSegment([2]float64{q.X, q.Y}, w.Points[0], w.Points[1]) {
					g = fmt.Sprintf("wire:%d", root(i))
					break
				}
			}
			groups[g] = append(groups[g], k)
		}
	}
	for _, members := range groups {
		sort.Strings(members)
		key := strings.Join(members, ",")
		for _, k := range members {
			fp.island[k] = key
		}
	}
	return fp
}

// compareFingerprint: strict = identical nets and identical physical
// islands. splitOK additionally allows a non-direct island to be split, as
// long as no core pin is separated from a peripheral pin it was wired to.
func (e *aesEngine) compareFingerprint(after aesFingerprint, splitOK bool) string {
	before := e.baseFP
	if len(before.net) != len(after.net) {
		return "pin set changed"
	}
	for k, n := range before.net {
		if after.net[k] != n {
			return "pin net changed: " + k
		}
	}
	for k, isl := range before.island {
		if after.island[k] == isl {
			continue
		}
		if !splitOK {
			return "physical island changed at " + k
		}
		net := before.net[k]
		if net == "" || libDirectPolicy(e.policies[net]) || !libLabelSplitAllowed(e.policies[net]) {
			return "direct island changed at " + k
		}
		for _, other := range strings.Split(isl, ",") {
			if other == k || before.core[other] == before.core[k] {
				continue
			}
			if after.island[other] != after.island[k] {
				return "core↔peripheral wire removed at " + k
			}
		}
		// A split may never merge foreign pins into this island.
		for _, other := range strings.Split(after.island[k], ",") {
			if before.island[other] != isl {
				return "physical island merged at " + k
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// offline check / lint facts

// schAesOfflineCheck mirrors the offline-computable parts of sch check and
// sch layout-lint on a plan: marker geometry findings (duplicate, overlap,
// folded), reversed flags, strict wire crossings, dangling wire ends and
// zero-length wires, plus the layout-lint body/pin/spacing/grid findings.
func schAesOfflineCheck(p *powerLayoutPlan) map[string]int {
	out := map[string]int{}
	if validateLibGeometry(p) != nil {
		out["geometry-error"] = 1
	}
	if validateSchCompositionNets(p) != nil {
		out["nets-error"] = 1
	}
	comps := []layoutComp{}
	var parts []layoutComp
	for _, c := range p.Placements {
		c := c
		lc := layoutComp{ID: c.Designator, Designator: c.Designator, ComponentType: "part", BBox: &c.BBox, X: c.X, Y: c.Y, AnchorAvailable: true, PinsAvailable: true}
		for _, q := range c.Pins {
			lc.Pins = append(lc.Pins, layoutPin{Number: q.Number, X: q.X, Y: q.Y})
		}
		comps = append(comps, lc)
		parts = append(parts, lc)
	}
	var wires []schGroupWire
	for i, w := range p.Wires {
		if len(w.Points) != 2 {
			continue
		}
		if w.Points[0] == w.Points[1] {
			out["zero-length"]++
		}
		wires = append(wires, schGroupWire{ID: fmt.Sprintf("w%d", i), Points: []float64{w.Points[0][0], w.Points[0][1], w.Points[1][0], w.Points[1][1]}})
	}
	for i, f := range p.Flags {
		x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
		body := predictedMarkerBody(x, y, f.Kind, f.Direction, f.Net)
		family, kind := f.Kind, "netflag"
		if isNetPortKind(f.Kind) {
			family, kind = "port", "netport"
		}
		if f.Kind == "net_label" {
			family, kind = "port", "netlabel"
		}
		rotation := flagBodyRotation[family][f.Direction]
		comps = append(comps, layoutComp{ID: fmt.Sprintf("marker-%03d", i), ComponentType: kind, Net: f.Net, X: x, Y: y, AnchorAvailable: true, Rotation: &rotation, BBox: &body})
		wires = append(wires, schGroupWire{ID: fmt.Sprintf("lead%d", i), Points: []float64{f.PinX, f.PinY, x, y}})
	}
	out["marker-geometry"] = len(analyzeMarkerGeometry(comps, nil, sheetSourceNone, schMarkerOverlapEps))
	out["reversed-net-flag"] = len(reversedNetFlagFindings(comps, wires))
	pins := libPinPointSet(p)
	flags := libFlagPointSet(p)
	for _, w := range p.Wires {
		for _, q := range w.Points {
			if _, ok := pins[q]; ok || flags[q] {
				continue
			}
			if libWirePointDegree(p.Wires, w.Net, q) <= 1 {
				out["dangling"]++
			}
		}
	}
	segs := append([]powerLayoutWire(nil), p.Wires...)
	for _, f := range p.Flags {
		x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
		segs = append(segs, powerLayoutWire{Net: f.Net, Points: [][2]float64{{f.PinX, f.PinY}, {x, y}}})
	}
	for i, a := range segs {
		for _, b := range segs[:i] {
			if len(a.Points) == 2 && len(b.Points) == 2 && plSegmentsMeet(a.Points[0], a.Points[1], b.Points[0], b.Points[1]) && !plSegmentsContact(a.Points[0], a.Points[1], b.Points[0], b.Points[1]) {
				out["wire-crossing"]++
			}
		}
	}
	// The solver's own per-marker placement rules (lead retrace, 5-unit marker
	// clearance to bodies/pins/markers, lead onto a foreign pin/wire): count
	// the markers that would not be accepted if placed last.
	for i, f := range p.Flags {
		rest := *p
		rest.Flags = append(append([]powerLayoutFlag(nil), p.Flags[:i]...), p.Flags[i+1:]...)
		segments, err := schTerminalSegments(&rest)
		if err != nil || libMarkerRetraces(f, segments) || schTerminalCandidate(&rest, f, segments) != nil {
			out["marker-terminal"]++
		}
	}
	rep := analyzeLayoutWithOwnership(parts, mmToSchematicUnits(2.54), 0, func(a, b string) bool { return true })
	out["lint-overlap"] = len(rep.Overlaps)
	out["lint-pin-coincidence"] = len(rep.PinCoincidences)
	out["lint-tight"] = len(rep.TightPairs)
	out["lint-off-grid"] = len(detectOffGridAnchors(parts, schAnchorGrid, acCoordEps))
	return out
}

func schAesCheckWorse(before, after map[string]int) string {
	keys := map[string]bool{}
	for k := range before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	var worse []string
	for k := range keys {
		if after[k] > before[k] {
			worse = append(worse, fmt.Sprintf("%s %d→%d", k, before[k], after[k]))
		}
	}
	sort.Strings(worse)
	return strings.Join(worse, ", ")
}

// admit runs every hard gate; "" = admitted, otherwise the rejection reason.
func (e *aesEngine) admit(p *powerLayoutPlan, splitOK bool) string {
	if err := validateLibGeometry(p); err != nil {
		return "geometry"
	}
	if err := validateSchCompositionNets(p); err != nil {
		return "nets"
	}
	if reason := e.compareFingerprint(e.fingerprint(p), splitOK); reason != "" {
		return "connectivity"
	}
	layout := &SchematicLayoutResult{ComponentIDs: e.componentIDs, Placements: p.Placements, Wires: p.Wires, Flags: p.Flags}
	if err := validateSchematicLayoutPeripheralDirect(layout, e.coreID, e.roles); err != nil {
		return "ownership"
	}
	if worse := schAesCheckWorse(e.baseCheck, schAesOfflineCheck(p)); worse != "" {
		return "check"
	}
	return ""
}

// ---------------------------------------------------------------------------
// marker (re)placement

func (e *aesEngine) markerKind(net string) string {
	if k := e.kinds[net]; k != "" {
		return k
	}
	return libPortMarkerKind(e.policies[net])
}

// libIslandNamed: some marker attaches to the island (a pin or a wire point).
func libIslandNamed(p *powerLayoutPlan, island libIsland) bool {
	for _, f := range p.Flags {
		if f.Net != island.net {
			continue
		}
		at := [2]float64{f.PinX, f.PinY}
		for _, q := range island.pins {
			if at == [2]float64{q.X, q.Y} {
				return true
			}
		}
		for _, i := range island.wireIndices {
			if i < len(p.Wires) && len(p.Wires[i].Points) == 2 && plOnSegment(at, p.Wires[i].Points[0], p.Wires[i].Points[1]) {
				return true
			}
		}
	}
	return false
}

func libUnnamedIslands(p *powerLayoutPlan) []libIsland {
	var out []libIsland
	for _, island := range libIslands(p) {
		if !libIslandNamed(p, island) {
			out = append(out, island)
		}
	}
	return out
}

func (e *aesEngine) markerCap(p *powerLayoutPlan, net, kind string) (float64, float64) {
	start := 10.0
	if kind == "net_label" {
		start = netLabelMinLead(net)
	}
	cap := start + 30
	if isNetPortKind(kind) {
		cap = math.Min(libMarkerOffsetCap(p, net, kind), start+50)
	}
	return start, cap
}

// aesMarkerProposal is one unvalidated marker lead: optional tap wires from
// a pin (escape + jog) plus the flag. Proposals are ranked cheaply first and
// only the best few are fully validated (validateLibGeometry is the cost).
type aesMarkerProposal struct {
	added []powerLayoutWire
	flag  powerLayoutFlag
	key   [3]float64
}

// markerOptions enumerates marker leads for one unnamed island — from each
// pin (straight, or a short outward escape with an optional lateral jog) and
// from every 5-unit point of the island's wires — ranks them by a cheap key
// (foreign crossings, crowded-T / flow faults, added length) and returns up
// to keep fully validated candidates in that order.
func (e *aesEngine) markerOptions(base *powerLayoutPlan, island libIsland, kind string, keep int) []*powerLayoutPlan {
	start, cap := e.markerCap(base, island.net, kind)
	all := []string{"up", "down", "left", "right"}
	back := map[string]string{"left": "right", "right": "left", "up": "down", "down": "up"}
	var props []aesMarkerProposal
	add := func(added []powerLayoutWire, at [2]float64, dirs []string, maxOff float64) {
		for _, d := range dirs {
			for _, step := range []float64{0, 5, 10, 15, 20, 30, 40, 50} {
				if off := start + step; off <= maxOff {
					props = append(props, aesMarkerProposal{added: added, flag: powerLayoutFlag{Net: island.net, Kind: kind, PinX: at[0], PinY: at[1], Direction: d, Offset: off}})
				}
			}
		}
	}
	for _, q := range island.pins {
		var body layoutBBox
		found := false
		for _, c := range base.Placements {
			for _, cp := range c.Pins {
				if libSamePhysicalPin(cp, q) {
					body, found = c.BBox, true
				}
			}
		}
		if !found {
			continue
		}
		side, err := libPinSide(q, body)
		if err != nil {
			continue
		}
		pt := [2]float64{q.X, q.Y}
		add(nil, pt, []string{side}, cap)
		for _, outward := range []float64{5, 10, 20, 30} {
			for _, lateral := range []float64{0, 5, 10, 20, -5, -10, -20} {
				x, y := endpointFor(q.X, q.Y, outward, side)
				bend := [2]float64{x, y}
				tap := bend
				if lateral != 0 {
					if side == "left" || side == "right" {
						tap[1] += lateral
					} else {
						tap[0] += lateral
					}
				}
				var dirs []string
				for _, d := range all {
					if lateral == 0 && (d == side || d == back[side]) {
						continue
					}
					dirs = append(dirs, d)
				}
				add(libPointsRoute(q.Net, pt, bend, tap), tap, dirs, math.Min(cap, start+10))
			}
		}
	}
	for _, i := range island.wireIndices {
		if i >= len(base.Wires) || len(base.Wires[i].Points) != 2 {
			continue
		}
		a, b := base.Wires[i].Points[0], base.Wires[i].Points[1]
		dirs := []string{"up", "down"}
		if a[0] == b[0] {
			dirs = []string{"left", "right"}
		}
		n := int(math.Round((math.Abs(b[0]-a[0]) + math.Abs(b[1]-a[1])) / schAnchorGrid))
		for k := 0; k <= n; k++ {
			t := float64(k) / math.Max(1, float64(n))
			pt := [2]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t}
			if !plGrid(pt[0]) || !plGrid(pt[1]) {
				continue
			}
			ds := dirs
			if k == 0 || k == n {
				ds = all
			}
			add(nil, pt, ds, cap)
		}
	}
	others := append([]powerLayoutWire(nil), base.Wires...)
	for _, o := range base.Flags {
		ox, oy := endpointFor(o.PinX, o.PinY, o.Offset, o.Direction)
		others = append(others, powerLayoutWire{Net: o.Net, Points: [][2]float64{{o.PinX, o.PinY}, {ox, oy}}})
	}
	for i := range props {
		props[i].key = aesQuickKey(base.Wires, others, props[i], e.profile.Generate.JunctionClearance)
	}
	sort.SliceStable(props, func(i, j int) bool {
		for k := 0; k < 3; k++ {
			if props[i].key[k] != props[j].key[k] {
				return props[i].key[k] < props[j].key[k]
			}
		}
		return false
	})
	var out []*powerLayoutPlan
	seen := map[string]bool{}
	checks := 0
	baseSegments, _ := schTerminalSegments(base)
	for _, pr := range props {
		if len(out) >= keep || checks >= 100 {
			break
		}
		trial := clonePowerLayoutPlan(base)
		segments := baseSegments
		if len(pr.added) > 0 {
			trial.Wires = libAppendRoute(base.Wires, pr.added)
			var err error
			if segments, err = schTerminalSegments(trial); err != nil {
				continue
			}
		}
		if libMarkerRetraces(pr.flag, segments) {
			continue
		}
		raw, _ := json.Marshal(struct {
			W []powerLayoutWire
			F powerLayoutFlag
		}{trial.Wires, pr.flag})
		if seen[string(raw)] {
			continue
		}
		seen[string(raw)] = true
		checks++
		e.work++
		if schTerminalCandidate(trial, pr.flag, segments) != nil {
			continue
		}
		trial.Flags = append(trial.Flags, pr.flag)
		if validateLibGeometry(trial) != nil {
			continue
		}
		out = append(out, trial)
	}
	return out
}

// nameUnnamed names every unnamed island greedily (ground, power, signals),
// choosing for each the legal lead with the best objective.
func (e *aesEngine) nameUnnamed(p *powerLayoutPlan) bool {
	for guard := 0; guard < 64; guard++ {
		islands := libUnnamedIslands(p)
		if len(islands) == 0 {
			return true
		}
		sort.SliceStable(islands, func(i, j int) bool {
			return libNetPriority(e.policies[islands[i].net]) < libNetPriority(e.policies[islands[j].net])
		})
		island := islands[0]
		options := e.markerOptions(p, island, e.markerKind(island.net), 4)
		var best *powerLayoutPlan
		var bestObj libAesObjective
		for _, o := range options {
			if !e.budgetLeft() && best != nil {
				break
			}
			_, obj := e.measure(o)
			if best == nil || obj.better(bestObj) {
				best, bestObj = o, obj
			}
		}
		if best == nil {
			return false
		}
		*p = *best
	}
	return false
}

// removeIslandFlags drops every marker naming the island(s) of net that
// contain any of the given points; returns the removed kinds.
func removeFlagsOnIsland(p *powerLayoutPlan, net string, wires []powerLayoutWire, pins []powerLayoutPin) {
	var kept []powerLayoutFlag
	for _, f := range p.Flags {
		at := [2]float64{f.PinX, f.PinY}
		drop := false
		if f.Net == net {
			for _, q := range pins {
				drop = drop || at == [2]float64{q.X, q.Y}
			}
			for _, w := range wires {
				drop = drop || (len(w.Points) == 2 && plOnSegment(at, w.Points[0], w.Points[1]))
			}
		}
		if !drop {
			kept = append(kept, f)
		}
	}
	p.Flags = kept
}

func libIslandOfPoint(p *powerLayoutPlan, net string, at [2]float64) (libIsland, bool) {
	for _, island := range libIslands(p) {
		if island.net != net {
			continue
		}
		for _, q := range island.pins {
			if [2]float64{q.X, q.Y} == at {
				return island, true
			}
		}
		for _, i := range island.wireIndices {
			if i < len(p.Wires) && len(p.Wires[i].Points) == 2 && plOnSegment(at, p.Wires[i].Points[0], p.Wires[i].Points[1]) {
				return island, true
			}
		}
	}
	return libIsland{}, false
}

func islandWires(p *powerLayoutPlan, island libIsland) []powerLayoutWire {
	var out []powerLayoutWire
	for _, i := range island.wireIndices {
		if i < len(p.Wires) {
			out = append(out, p.Wires[i])
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// the pass

func (e *aesEngine) try(name string, current *powerLayoutPlan, currentObj libAesObjective, candidate *powerLayoutPlan, splitOK bool) (libAesObjective, bool) {
	ps := e.pass(name)
	ps.Tried++
	if candidate == nil {
		ps.Rejected["no-candidate"]++
		return currentObj, false
	}
	if reason := e.admit(candidate, splitOK); reason != "" {
		ps.Rejected[reason]++
		return currentObj, false
	}
	_, obj := e.measure(candidate)
	if !obj.better(currentObj) {
		ps.Rejected["not-better"]++
		return currentObj, false
	}
	ps.Accepted++
	*current = *candidate
	return obj, true
}

// markerPass relocates one marker at a time to its best legal lead.
func (e *aesEngine) markerPass(p *powerLayoutPlan, obj libAesObjective) (libAesObjective, bool) {
	improved := false
	for i := 0; i < len(p.Flags) && e.budgetLeft(); i++ {
		f := p.Flags[i]
		base := clonePowerLayoutPlan(p)
		base.Flags = append(append([]powerLayoutFlag(nil), p.Flags[:i]...), p.Flags[i+1:]...)
		libPruneDanglingWires(base)
		island, ok := libIslandOfPoint(base, f.Net, [2]float64{f.PinX, f.PinY})
		if !ok {
			// The marker sat on a pruned escape: name the pin island it served.
			for _, u := range libUnnamedIslands(base) {
				if u.net == f.Net {
					island, ok = u, true
					break
				}
			}
		}
		if !ok {
			continue
		}
		if libIslandNamed(base, island) {
			// redundant marker: dropping it is a candidate of its own
			if o, acc := e.try("marker", p, obj, base, false); acc {
				obj, improved = o, true
				i = -1
			}
			continue
		}
		options := e.markerOptions(base, island, f.Kind, 10)
		var best *powerLayoutPlan
		bestObj := obj
		for _, o := range options {
			if !e.budgetLeft() {
				break
			}
			_, so := e.measure(o)
			if so.better(bestObj) {
				best, bestObj = o, so
			}
		}
		if best == nil {
			e.pass("marker").Tried++
			e.pass("marker").Rejected["not-better"]++
			continue
		}
		if o, acc := e.try("marker", p, obj, best, false); acc {
			obj, improved = o, true
		}
	}
	return obj, improved
}

type aesRoutePlan struct {
	plan *powerLayoutPlan
	key  [2]float64
}

// trunkPass rips up one wire trunk at a time (with its island's markers and
// any marker the new route collides with), re-routes it with bend/crossing
// aware candidates — re-landing a branch at a clean T when that is better —
// and re-names the affected islands. Four-way junctions and crowded T
// junctions are resolved here because a re-landed branch is a staggered T.
func (e *aesEngine) trunkPass(p *powerLayoutPlan, obj libAesObjective, hotOnly bool) (libAesObjective, bool) {
	improved := false
	trunks := libWireTrunks(p)
	// Trunks touching a measured defect (crossing, 4-way, overlap, crowded T,
	// wire through a body/marker/text) are searched wider and first.
	rep, _ := e.measure(p)
	var hotspots [][2]float64
	for _, id := range []string{"W2", "W3", "W4", "W5", "W7"} {
		if m := rep.Metric(id); m != nil && !m.Skipped {
			for _, w := range m.Worst {
				if w.At != nil {
					hotspots = append(hotspots, [2]float64{w.At.X, w.At.Y})
				}
			}
		}
	}
	hot := func(t libTrunk) bool {
		for _, h := range hotspots {
			for _, i := range t.indices {
				w := p.Wires[i].Points
				lo := [2]float64{math.Min(w[0][0], w[1][0]) - 5, math.Min(w[0][1], w[1][1]) - 5}
				hi := [2]float64{math.Max(w[0][0], w[1][0]) + 5, math.Max(w[0][1], w[1][1]) + 5}
				if h[0] >= lo[0] && h[0] <= hi[0] && h[1] >= lo[1] && h[1] <= hi[1] {
					return true
				}
			}
		}
		return false
	}
	sort.SliceStable(trunks, func(i, j int) bool {
		if hi, hj := hot(trunks[i]), hot(trunks[j]); hi != hj {
			return hi
		}
		return trunks[i].length > trunks[j].length
	})
	for _, t := range trunks {
		if !e.budgetLeft() {
			break
		}
		// The incumbent may have changed; skip trunks that no longer exist.
		exists := true
		for _, i := range t.indices {
			exists = exists && i < len(p.Wires) && p.Wires[i].Net == t.net
		}
		if !exists {
			continue
		}
		width := 4
		if hot(t) {
			width = 12
		} else if hotOnly {
			continue
		}
		cand := e.rerouteTrunk(p, t, width)
		if cand == nil {
			e.pass("trunk").Tried++
			e.pass("trunk").Rejected["no-candidate"]++
			continue
		}
		if o, acc := e.try("trunk", p, obj, cand, false); acc {
			obj, improved = o, true
			return obj, improved // trunk indices changed: restart from the caller
		}
	}
	return obj, improved
}

func (e *aesEngine) rerouteTrunk(p *powerLayoutPlan, t libTrunk, width int) *powerLayoutPlan {
	island, ok := libIslandOfPoint(p, t.net, p.Wires[t.indices[0]].Points[0])
	if !ok {
		return nil
	}
	base := clonePowerLayoutPlan(p)
	removed := map[int]bool{}
	for _, i := range t.indices {
		removed[i] = true
	}
	var kept []powerLayoutWire
	for i, w := range base.Wires {
		if !removed[i] {
			kept = append(kept, w)
		}
	}
	removeFlagsOnIsland(base, t.net, islandWires(p, island), island.pins)
	base.Wires = kept
	libPruneDanglingWires(base)
	// Group the original island's pins by their new physical islands.
	groupOf := map[string]int{}
	var groups []libIsland
	for _, cur := range libIslands(base) {
		if cur.net != t.net {
			continue
		}
		member := false
		for _, q := range cur.pins {
			for _, o := range island.pins {
				member = member || libSamePhysicalPin(q, o)
			}
		}
		if member {
			groupOf[cur.key] = len(groups)
			groups = append(groups, cur)
		}
	}
	finish := func(c *powerLayoutPlan) *powerLayoutPlan {
		if !e.nameUnnamed(c) {
			return nil
		}
		return c
	}
	switch len(groups) {
	case 1:
		// The trunk was redundant (a loop or a pure marker stub).
		return finish(base)
	case 2:
	default:
		return nil
	}
	pinDir := func(q powerLayoutPin) string {
		for _, c := range base.Placements {
			for _, cp := range c.Pins {
				if libSamePhysicalPin(cp, q) {
					side, err := libPinSide(cp, c.BBox)
					if err == nil {
						return side
					}
				}
			}
		}
		return ""
	}
	terminalsOf := func(g libIsland, near [2]float64, radius float64) []libRouteTerminal {
		var out []libRouteTerminal
		for _, q := range g.pins {
			out = append(out, libRouteTerminal{pt: [2]float64{q.X, q.Y}, dir: pinDir(q)})
		}
		for _, i := range g.wireIndices {
			w := base.Wires[i]
			a, b := w.Points[0], w.Points[1]
			n := int(math.Round((math.Abs(b[0]-a[0]) + math.Abs(b[1]-a[1])) / schAnchorGrid))
			for k := 0; k <= n; k++ {
				pt := [2]float64{a[0] + (b[0]-a[0])*float64(k)/math.Max(1, float64(n)), a[1] + (b[1]-a[1])*float64(k)/math.Max(1, float64(n))}
				if !plGrid(pt[0]) || !plGrid(pt[1]) || math.Abs(pt[0]-near[0])+math.Abs(pt[1]-near[1]) > radius {
					continue
				}
				dup := false
				for _, o := range out {
					dup = dup || o.pt == pt
				}
				if !dup {
					out = append(out, libRouteTerminal{pt: pt})
				}
			}
		}
		return out
	}
	radius := t.length + 60
	var screened []aesRoutePlan
	consider := func(route []powerLayoutWire, terminals ...libRouteTerminal) {
		if len(route) == 0 || !libRouteLeavesTerminals(route, terminals...) {
			return
		}
		trial := clonePowerLayoutPlan(base)
		trial.Wires = libAppendRoute(base.Wires, route)
		if !libPinsShareIsland(trial, groups[0].pins[0], groups[1].pins[0]) {
			return
		}
		if validateLibGeometry(trial) != nil {
			// Displace markers the new route collides with; they are re-named below.
			var keptFlags []powerLayoutFlag
			for _, f := range trial.Flags {
				x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
				hit := false
				for _, w := range route {
					for _, box := range schTerminalMarkerBoxes(f) {
						hit = hit || plSegmentBox(w.Points[0], w.Points[1], layoutBBox{box.MinX - 5, box.MinY - 5, box.MaxX + 5, box.MaxY + 5})
					}
					hit = hit || plSegmentsMeet(w.Points[0], w.Points[1], [2]float64{f.PinX, f.PinY}, [2]float64{x, y})
				}
				if !hit {
					keptFlags = append(keptFlags, f)
				}
			}
			if len(keptFlags) == len(trial.Flags) {
				return
			}
			trial.Flags = keptFlags
			libPruneDanglingWires(trial)
			if !libPinsShareIsland(trial, groups[0].pins[0], groups[1].pins[0]) || validateLibGeometry(trial) != nil {
				return
			}
		}
		length := 0.0
		for _, w := range route {
			length += math.Abs(w.Points[1][0]-w.Points[0][0]) + math.Abs(w.Points[1][1]-w.Points[0][1])
		}
		bends := len(route) - 1
		crossings := 0
		others := append([]powerLayoutWire(nil), trial.Wires...)
		for _, f := range trial.Flags {
			fx, fy := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
			others = append(others, powerLayoutWire{Net: f.Net, Points: [][2]float64{{f.PinX, f.PinY}, {fx, fy}}})
		}
		for _, w := range route {
			for _, v := range others {
				if v.Net != t.net && plSegmentsMeet(w.Points[0], w.Points[1], v.Points[0], v.Points[1]) {
					crossings++
				}
			}
		}
		g := e.profile.Generate
		screened = append(screened, aesRoutePlan{plan: trial, key: [2]float64{float64(crossings), length + g.BendCost*float64(bends)}})
	}
	// Generate every bounded route cheaply, pre-rank by [foreign crossings,
	// length + bend cost], then validate in that order under a check cap.
	type rawRoute struct {
		route     []powerLayoutWire
		terminals []libRouteTerminal
		key       [2]float64
	}
	others := append([]powerLayoutWire(nil), base.Wires...)
	for _, f := range p.Flags {
		fx, fy := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
		others = append(others, powerLayoutWire{Net: f.Net, Points: [][2]float64{{f.PinX, f.PinY}, {fx, fy}}})
	}
	var raws []rawRoute
	seenRaw := map[string]bool{}
	push := func(route []powerLayoutWire, terminals ...libRouteTerminal) {
		if len(route) == 0 || !libRouteLeavesTerminals(route, terminals...) {
			return
		}
		raw, _ := json.Marshal(route)
		if seenRaw[string(raw)] {
			return
		}
		seenRaw[string(raw)] = true
		var key [2]float64
		for _, w := range route {
			key[1] += math.Abs(w.Points[1][0]-w.Points[0][0]) + math.Abs(w.Points[1][1]-w.Points[0][1])
			for _, v := range others {
				if v.Net != t.net && plSegmentsMeet(w.Points[0], w.Points[1], v.Points[0], v.Points[1]) {
					key[0]++
				}
			}
		}
		key[1] += e.profile.Generate.BendCost * float64(len(route)-1)
		raws = append(raws, rawRoute{route: route, terminals: terminals, key: key})
	}
	escapes := []float64{5, 10, 20, 30, 40}
	if width < 8 {
		escapes = []float64{5, 10, 20}
	}
	for side := 0; side < 2; side++ {
		src, dst := groups[side], groups[1-side]
		var sources []libRouteTerminal
		for _, near := range [][2]float64{t.a, t.b} {
			sources = append(sources, terminalsOf(src, near, 0.1)...)
		}
		for _, s := range sources {
			for _, d := range terminalsOf(dst, s.pt, radius) {
				for _, route := range libTerminalRoutes(t.net, s, d, escapes) {
					push(route, s, d)
				}
			}
		}
	}
	if libDirectPolicy(e.policies[t.net]) && e.routing != nil {
		if route, err := libMazeRoute(base, groups[0], groups[1], e.routing); err == nil {
			push(route)
		}
	}
	sort.SliceStable(raws, func(i, j int) bool {
		if raws[i].key[0] != raws[j].key[0] {
			return raws[i].key[0] < raws[j].key[0]
		}
		return raws[i].key[1] < raws[j].key[1]
	})
	maxChecks := 10 * width
	for i := 0; i < len(raws) && i < maxChecks && len(screened) < width; i++ {
		e.work++
		consider(raws[i].route, raws[i].terminals...)
	}
	sort.SliceStable(screened, func(i, j int) bool {
		if screened[i].key[0] != screened[j].key[0] {
			return screened[i].key[0] < screened[j].key[0]
		}
		return screened[i].key[1] < screened[j].key[1]
	})
	if len(screened) > width {
		screened = screened[:width]
	}
	var best *powerLayoutPlan
	var bestObj libAesObjective
	for _, s := range screened {
		c := finish(s.plan)
		if c == nil || e.admit(c, false) != "" {
			continue
		}
		_, o := e.measure(c)
		if best == nil || o.better(bestObj) {
			best, bestObj = c, o
		}
	}
	return best
}

// labelSplitPass (B2) replaces one trunk of a long / crossing-heavy wire tree
// by local labels — only for nets whose policy allows labels, never a direct
// net and never a core↔peripheral wire (fingerprint gate, splitOK mode).
func (e *aesEngine) labelSplitPass(p *powerLayoutPlan, obj libAesObjective) (libAesObjective, bool) {
	if !e.profile.Generate.LabelSplit {
		return obj, false
	}
	r, _ := e.measure(p)
	m := r.Metric("N1")
	if m == nil || m.Skipped || m.Value == 0 {
		return obj, false
	}
	long := map[string]bool{}
	for _, w := range m.Worst {
		long[w.Net] = true
	}
	for _, t := range libWireTrunks(p) {
		if !long[t.net] || !libLabelSplitAllowed(e.policies[t.net]) || !e.budgetLeft() {
			continue
		}
		island, ok := libIslandOfPoint(p, t.net, p.Wires[t.indices[0]].Points[0])
		if !ok {
			continue
		}
		base := clonePowerLayoutPlan(p)
		removed := map[int]bool{}
		for _, i := range t.indices {
			removed[i] = true
		}
		var kept []powerLayoutWire
		for i, w := range base.Wires {
			if !removed[i] {
				kept = append(kept, w)
			}
		}
		removeFlagsOnIsland(base, t.net, islandWires(p, island), island.pins)
		base.Wires = kept
		libPruneDanglingWires(base)
		if !e.nameUnnamed(base) {
			e.pass("label-split").Tried++
			e.pass("label-split").Rejected["no-label-room"]++
			continue
		}
		if o, acc := e.try("label-split", p, obj, base, true); acc {
			e.splits = append(e.splits, fmt.Sprintf("%s: %.0f-unit trunk %v→%v replaced by local labels", t.net, t.length, t.a, t.b))
			return o, true
		}
	}
	return obj, false
}

// applySchematicAesthetics runs the opt-in beautify pass on a finished zone.
// It returns the (possibly unchanged) result with a report attached.
func applySchematicAesthetics(input SchematicLayoutInput, result *SchematicLayoutResult, policies, roles map[string]string, budget *int) *SchematicLayoutResult {
	opts := input.Aesthetics
	report := &SchematicAestheticsReport{Style: opts.Style, Priority: schAesPriority, Status: "unchanged"}
	if report.Style == "" {
		report.Style = schaes.DefaultProfile
	}
	plan := &powerLayoutPlan{Placements: result.Placements, Wires: clonePowerLayoutWires(result.Wires), Flags: append([]powerLayoutFlag(nil), result.Flags...)}
	for i := range plan.Flags {
		plan.Flags[i].Anchor = nil
	}
	e := &aesEngine{policies: policies, roles: roles, coreID: input.CoreComponentID, componentIDs: result.ComponentIDs, pinStates: result.PinStates,
		passes: map[string]*SchematicAestheticsPass{}, kinds: map[string]string{}, nativeBus: opts.NativeBus}
	for _, f := range plan.Flags {
		e.kinds[f.Net] = f.Kind
	}
	snap, err := e.snapshot(plan)
	if err == nil {
		e.profile, err = resolveSchAesProfile(opts.Style, snap)
	}
	if err != nil {
		report.Status, report.Reason = "skipped", err.Error()
		result.Aesthetics = report
		return result
	}
	if e.profile.Auto != nil {
		report.AutoReason = e.profile.Auto.Reason
		report.Style = "auto→" + e.profile.Auto.Chosen
	}
	e.evalLimit = e.profile.Generate.MaxEvaluations
	g := e.profile.Generate
	components := make([]SchematicLayoutComponent, 0, len(plan.Placements))
	for _, c := range plan.Placements {
		components = append(components, SchematicLayoutComponent{ID: result.ComponentIDs[c.Designator], Measurement: c})
	}
	if routing, rerr := newSchematicRoutingContext(input.Routing, components); rerr == nil {
		routing.policies = policies
		routing.bendWeight = int(math.Round(g.BendCost / schAnchorGrid))
		routing.crossWeight = int(math.Round(g.CrossCost / schAnchorGrid))
		e.routing = routing
	}
	e.baseFP = e.fingerprint(plan)
	e.baseCheck = schAesOfflineCheck(plan)
	report.CheckBefore = e.baseCheck
	if e.baseCheck["geometry-error"] > 0 || e.baseCheck["nets-error"] > 0 {
		report.Status, report.Reason = "skipped", "solver output does not pass the offline geometry/net gate; nothing to beautify safely"
		report.CheckAfter = e.baseCheck
		result.Aesthetics = report
		return result
	}
	before, obj := e.measure(plan)
	report.ScoreBefore, report.DefectsBefore, report.MetricsBefore = before.Score, obj.Defects, schAesMetricScores(before)
	start := obj

	// B4 alignment first (it regenerates the wire forest), then wire cleanup.
	if g.AlignMove > 0 && budget != nil {
		obj, _ = e.alignPass(plan, obj, budget)
	}
	for round := 0; round < 4 && e.budgetLeft(); round++ {
		changed := false
		var acc bool
		// Defect-touching trunks first; the remaining (cold) trunks get a
		// narrow search only from the second round on, budget permitting.
		for again := true; again && e.budgetLeft(); {
			obj, acc = e.trunkPass(plan, obj, round == 0)
			again = acc
			changed = changed || acc
		}
		obj, acc = e.markerPass(plan, obj)
		changed = changed || acc
		obj, acc = e.labelSplitPass(plan, obj)
		changed = changed || acc
		obj, acc = e.busLanePass(plan, obj)
		changed = changed || acc
		if !changed {
			break
		}
	}
	if e.nativeBus {
		e.proposeNativeBuses(plan)
	}
	after, final := e.measure(plan)
	report.ScoreAfter, report.DefectsAfter, report.MetricsAfter = after.Score, final.Defects, schAesMetricScores(after)
	report.CheckAfter = schAesOfflineCheck(plan)
	report.ConnectivityIdentical = e.compareFingerprint(e.fingerprint(plan), false) == ""
	report.PinNetIdentical = e.compareFingerprint(e.fingerprint(plan), true) == ""
	report.LabelSplits = e.splits
	report.BusLanes = e.lanes
	report.Evaluations = e.evals
	for _, name := range e.order {
		ps := *e.passes[name]
		if len(ps.Rejected) == 0 {
			ps.Rejected = nil
		}
		report.Passes = append(report.Passes, ps)
	}
	if final.better(start) {
		report.Status = "improved"
		out := *result
		out.Placements, out.Wires, out.Flags = plan.Placements, plan.Wires, plan.Flags
		out.Score = libCandidateScore(plan)
		out.Aesthetics = report
		return &out
	}
	report.ScoreAfter, report.DefectsAfter, report.MetricsAfter, report.CheckAfter = report.ScoreBefore, report.DefectsBefore, report.MetricsBefore, report.CheckBefore
	report.ConnectivityIdentical, report.PinNetIdentical = true, true
	result.Aesthetics = report
	return result
}

// aesQuickKey is a cheap pre-rank of a marker proposal before validation
// and full schaes measurement: [foreign crossings, crowded/flow faults,
// added length].
func aesQuickKey(wires, others []powerLayoutWire, pr aesMarkerProposal, clearance float64) [3]float64 {
	var key [3]float64
	f := pr.flag
	x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
	added := append([]powerLayoutWire{{Net: f.Net, Points: [][2]float64{{f.PinX, f.PinY}, {x, y}}}}, pr.added...)
	for _, a := range added {
		key[2] += math.Abs(a.Points[1][0]-a.Points[0][0]) + math.Abs(a.Points[1][1]-a.Points[0][1])
		for _, o := range others {
			if o.Net != a.Net && plSegmentsMeet(a.Points[0], a.Points[1], o.Points[0], o.Points[1]) {
				key[0]++
			}
		}
	}
	key[2] += 5 * float64(len(pr.added)) // each jog segment is a bend
	switch {
	case f.Kind == "power" && f.Direction != "up", f.Kind == "ground" && f.Direction != "down":
		key[1]++
	case (isNetPortKind(f.Kind) || f.Kind == "net_label") && (f.Direction == "up" || f.Direction == "down"):
		key[1]++
	}
	at := [2]float64{f.PinX, f.PinY}
	if len(pr.added) == 0 && libWirePointDegree(wires, f.Net, at) >= 1 {
		for _, w := range wires {
			if w.Net != f.Net {
				continue
			}
			for _, q := range w.Points {
				if q == at || libWirePointDegree(wires, f.Net, q) < 2 {
					continue
				}
				if d := math.Abs(q[0]-at[0]) + math.Abs(q[1]-at[1]); d < clearance && (q[0] == at[0] || q[1] == at[1]) {
					key[1]++
				}
			}
		}
	}
	return key
}

// schAesOptionsFromFlags merges CLI flags over an input's aesthetics object.
func schAesOptionsFromFlags(in *SchematicAestheticsOptions, style string, nativeBus bool) *SchematicAestheticsOptions {
	out := &SchematicAestheticsOptions{}
	if in != nil {
		*out = *in
	}
	if style != "" {
		out.Style = style
	}
	out.NativeBus = out.NativeBus || nativeBus
	return out
}
