package pcbauto

import (
	"fmt"
	"math"
	"sort"
)

// Stage 0 — physical feasibility and stackup decision.
//
// Before a single part is placed, the board has to be possible: the parts
// must fit on the sides we are willing to assemble, the wiring must fit in
// the signal layers we are willing to pay for, every BGA must be able to
// escape, and each BGA's pitch fixes the via technology. This stage measures
// those four things and picks the cheapest stackup that clears all of them
// with margin; the placer and router then work inside that decision.
//
//	occupancy     Σ courtyard (body + assembly gap) per side / board area
//	demand        Σ estimated routed length × (track + clearance), normalised
//	              to standard-track inches; power rails count only when they
//	              are not on planes (2-layer boards)
//	capacity      per signal layer: (board − that layer's pad blockage −
//	              keep-outs) / routing pitch × routing efficiency η
//	BGA escape    signal rings / rings each layer can escape → signal layers;
//	              pitch and balls → via technology
//	options       2 / 4 / 6 / 8 layers, each with its utilisation and the
//	              constraints it meets; the cheapest "ok" one wins

// RoutingEfficiency is the share of a signal layer's free area that real
// routing fills before it jams (channels lost to via fields, direction
// changes, keep-outs). Calibrated on the fixture boards whose human layer
// count is known to have been built — see TestFeasibilityCalibration.
const RoutingEfficiency = 0.35

// UnplacedWireK estimates, before placement, the mean Manhattan length of a
// connection as k·√(board area). Measured on the seven fixture boards (real
// placements, TestDonathCalibration): k = 0.13–0.28, median 0.14; the long-
// wire interface boards (MIPI adapter 0.28, bbclaw 0.26) set the upper end.
// 0.22 errs toward caution for a new design without being 2× pessimistic.
const UnplacedWireK = 0.22

// BGANeed is one ball-grid package's escape requirement.
type BGANeed struct {
	Ref          string  `json:"ref"`
	PitchMil     float64 `json:"pitchMil"`
	BallMil      float64 `json:"ballMil"`
	SignalBalls  int     `json:"signalBalls"`
	EscapeRings  int     `json:"escapeRings"`  // rings that hold signal balls
	TopRings     int     `json:"topRings"`     // rings escaping on the top layer
	InnerRings   int     `json:"innerRings"`   // rings each further signal layer escapes
	SignalLayers int     `json:"signalLayers"` // signal layers the escape needs
	ViaTech      string  `json:"viaTech"`
	ViaDrill     float64 `json:"viaDrillMil,omitempty"`
	ViaDia       float64 `json:"viaDiaMil,omitempty"`
	MinLayers    int     `json:"minLayers"`            // stackup the via technology requires
	RegionRule   string  `json:"regionRule,omitempty"` // a BGA-area rule the escape needs
}

// StackOption is one candidate stackup.
type StackOption struct {
	Layers int `json:"layers"`
	// Mixed: a 4-layer stack whose second inner layer carries signals with
	// the supplies poured around them (TOP/GND/SIG+PWR/BOTTOM) — three
	// routable layers at the 4-layer price, at the cost of a fragmented
	// supply plane.
	Mixed        bool     `json:"mixed,omitempty"`
	SignalLayers int      `json:"signalLayers"`
	CapacityIn   float64  `json:"capacityIn"`  // standard-track inches
	DemandIn     float64  `json:"demandIn"`    // standard-track inches on this stackup
	Utilisation  float64  `json:"utilisation"` // demand / capacity
	Verdict      string   `json:"verdict"`     // ok | tight | insufficient | excluded
	Why          []string `json:"why,omitempty"`
	CostTier     int      `json:"costTier"` // relative JLC price tier
}

// Feasibility is the stage-0 result.
type Feasibility struct {
	BoardAreaIn2 float64       `json:"boardAreaIn2"`
	Placed       bool          `json:"placed"`
	TopOccupancy float64       `json:"topOccupancy"`    // courtyard share of the top side
	BotOccupancy float64       `json:"bottomOccupancy"` // … of the bottom side
	OneSideFits  bool          `json:"oneSideFits"`     // all parts fit on the top alone
	Connections  int           `json:"connections"`
	WireLengthM  float64       `json:"wireLengthM"`    // estimated routed length, metres
	SignalDemand float64       `json:"signalDemandIn"` // standard-track inches, signals
	PowerDemand  float64       `json:"powerDemandIn"`  // … power rails when routed
	NeedPlanes   []string      `json:"needPlanes,omitempty"`
	BGAs         []BGANeed     `json:"bgas,omitempty"`
	Options      []StackOption `json:"options"`
	Choice       int           `json:"choice"`
	ChoiceMixed  bool          `json:"choiceMixed,omitempty"`
	Reasons      []string      `json:"reasons"`
	Efficiency   float64       `json:"efficiency"`
	// Negotiation: what the mechanical side must give when nothing fits.
	Negotiation []string `json:"negotiation,omitempty"`
	hsLongest   float64
}

// signalLayersOf is how many routable layers each stackup offers (matches
// buildStack).
func signalLayersOf(layers int) int {
	switch layers {
	case 2:
		return 2
	case 4:
		return 2
	case 6:
		return 3
	default:
		return 4
	}
}

// FeasibilityOptions carry what stage 0 needs beyond the board.
type FeasibilityOptions struct {
	MaxLayers int
	// Barriers are the isolation barriers the circuit needs; each takes a
	// creepage-wide strip across the board.
	Barriers []*Barrier
}

// AssessFeasibility runs stage 0 on the board as it stands (placed or not).
func AssessFeasibility(b *Board, an *Analysis, maxLayers int, extra ...FeasibilityOptions) *Feasibility {
	var fo FeasibilityOptions
	if len(extra) > 0 {
		fo = extra[0]
	}
	f := &Feasibility{Efficiency: RoutingEfficiency}
	why := func(format string, args ...any) { f.Reasons = append(f.Reasons, fmt.Sprintf(format, args...)) }
	area := b.Area()
	f.BoardAreaIn2 = area / 1e6
	f.Placed = looksPlaced(b)
	if area <= 0 {
		f.Choice = 2
		why("no board outline: stage 0 cannot size anything")
		return f
	}

	// ---- occupancy ----
	const assemblyGap = 10 // mil, courtyard margin for pick-and-place
	var top, bot, all float64
	for _, p := range b.Parts {
		bb := p.Body().Expand(assemblyGap / 2)
		a := bb.Area()
		all += a
		switch {
		case hasTHT(p):
			top += a
			bot += a
		case p.Side == LayerBottom:
			bot += a
		default:
			top += a
		}
	}
	f.TopOccupancy, f.BotOccupancy = top/area, bot/area
	f.OneSideFits = all/area <= 0.75
	if f.Placed {
		why("parts occupy %.0f%% of the top and %.0f%% of the bottom (courtyards incl. %d mil assembly gap)", 100*f.TopOccupancy, 100*f.BotOccupancy, assemblyGap)
	} else if !f.OneSideFits {
		why("all courtyards need %.0f%% of one side: the board needs double-sided assembly or more area", 100*all/area)
	}

	// ---- demand ----
	pitch := b.Rules.TrackWidth + b.Rules.Clearance
	for _, n := range b.Nets() {
		if len(n.Pads) < 2 {
			continue
		}
		np := an.Plan(n.Name, b.Rules)
		if np.Role == RoleGround {
			continue
		}
		var l float64
		if f.Placed {
			pts := make([]Point, len(n.Pads))
			for i, pd := range n.Pads {
				pts[i] = pd.Box.C
			}
			l = mstLength(pts)
		} else {
			l = float64(len(n.Pads)-1) * UnplacedWireK * math.Sqrt(area)
		}
		w := math.Max(np.WidthMil, b.Rules.TrackWidth)
		std := 1.3 * l / 1000 * (w + math.Max(np.ClearanceMil, b.Rules.Clearance)) / pitch
		if np.Role == RolePower {
			f.PowerDemand += std
			continue
		}
		f.Connections += len(n.Pads) - 1
		f.WireLengthM += 1.3 * l / 1000 * 0.0254
		f.SignalDemand += std
		switch np.Role {
		case RoleDiff:
			if hc := ClassifyHS(np); hc != nil && hc.Name != "USB2" && hc.Name != "CAN/RS-485" {
				f.addPlaneNeed("阻抗受控的高速差分对")
				f.hsLongest = math.Max(f.hsLongest, 1.3*l)
			}
		case RoleRF:
			f.addPlaneNeed("射频馈线")
		}
	}
	why("%d signal connections, ≈%.1f m of routed track (%.0f standard-track in); power rails %.0f in when routed as tracks",
		f.Connections, f.WireLengthM, f.SignalDemand, f.PowerDemand)

	// ---- BGA escape ----
	maxBGALayers, minTech := 0, 0
	for _, g := range detectBGAs(b) {
		need := bgaNeed(g, b, an)
		f.BGAs = append(f.BGAs, need)
		maxBGALayers = max(maxBGALayers, need.SignalLayers)
		minTech = max(minTech, need.MinLayers)
		why("BGA %s: %.2f mm pitch, %d signal balls in %d rings → %d on top + %d per further layer = %d signal layers; %s",
			need.Ref, need.PitchMil*0.0254, need.SignalBalls, need.EscapeRings, need.TopRings, need.InnerRings, need.SignalLayers, need.ViaTech)
		if need.RegionRule != "" {
			f.Negotiation = append(f.Negotiation, fmt.Sprintf("%s 在板级规则下无法逃逸，需要设置%s", need.Ref, need.RegionRule))
		}
		if need.SignalLayers >= 99 {
			f.Negotiation = append(f.Negotiation, fmt.Sprintf("%s 即使按工艺极限也无法逃逸：需要盘中孔（树脂塞孔电镀盖帽）或 HDI 激光盲孔", need.Ref))
			maxBGALayers = 0 // do not exclude every stackup for it; the negotiation item stands
		}
	}

	// ---- per-layer capacity ----
	padBlock := map[int]float64{} // side → pad area incl. clearance
	thtBlock := 0.0
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			a := (pd.Box.W + 2*b.Rules.Clearance) * (pd.Box.H + 2*b.Rules.Clearance)
			if pd.Layer == LayerMulti {
				thtBlock += a
			} else {
				padBlock[pd.Layer] += a
			}
		}
	}
	keep := 0.0
	for _, k := range b.Keepouts {
		keep += PolyArea(k.Poly)
	}
	layerCap := func(outer bool, side int) float64 {
		free := area - keep - thtBlock
		if outer {
			free -= padBlock[side]
		}
		return math.Max(0, free) / pitch / 1000 * f.Efficiency
	}
	type variant struct {
		layers int
		mixed  bool
	}
	for _, vr := range []variant{{2, false}, {4, false}, {4, true}, {6, false}, {8, false}} {
		layers := vr.layers
		o := StackOption{Layers: layers, SignalLayers: signalLayersOf(layers), CostTier: layers / 2, Mixed: vr.mixed}
		if vr.mixed {
			o.SignalLayers = 3
			o.Why = append(o.Why, "IN2 同时走信号和电源铺铜：电源平面被切碎，电源完整性变差")
		}
		o.CapacityIn = layerCap(true, LayerTop) + layerCap(true, LayerBottom)
		for k := 2; k < o.SignalLayers; k++ {
			o.CapacityIn += layerCap(false, 0)
		}
		o.DemandIn = f.SignalDemand
		if layers == 2 {
			o.DemandIn += f.PowerDemand // no planes: rails are tracks
			// A 2-layer board keeps a ground pour: count a third of one side lost.
			o.CapacityIn *= 0.85
		}
		if vr.mixed {
			// Supply pours share IN2 with signals: a third of it is copper.
			o.CapacityIn -= layerCap(false, 0) / 3
			o.DemandIn += f.PowerDemand * 0.3 // short supply stubs to the pours
		}
		if o.CapacityIn > 0 {
			o.Utilisation = o.DemandIn / o.CapacityIn
		}
		switch {
		case o.Utilisation <= 0.75:
			o.Verdict = "ok"
		case o.Utilisation <= 1.0:
			o.Verdict = "tight"
			o.Why = append(o.Why, fmt.Sprintf("布线利用率 %.0f%%：只有布局足够分散时才能布通", 100*o.Utilisation))
		default:
			o.Verdict = "insufficient"
			o.Why = append(o.Why, fmt.Sprintf("布线需求是容量的 %.0f%%", 100*o.Utilisation))
		}
		if layers == 2 && len(f.NeedPlanes) > 0 {
			if f.hsLongest > 0 && f.hsLongest < 3000 && len(f.NeedPlanes) == 1 {
				// Short high-speed runs (an adapter board) survive on 2 layers
				// as coplanar lines over a solid bottom pour.
				if o.Verdict == "ok" {
					o.Verdict = "tight"
				}
				o.Why = append(o.Why, fmt.Sprintf("高速差分线较短（≤%.0f mil）：可在完整底层铺地上按共面接地走线，但阻抗不受控", f.hsLongest))
			} else {
				o.Verdict = "excluded"
				o.Why = append(o.Why, "需要参考平面："+joinCN(f.NeedPlanes))
			}
		}
		if o.SignalLayers < maxBGALayers {
			o.Verdict = "excluded"
			o.Why = append(o.Why, fmt.Sprintf("BGA 逃逸需要 %d 层信号层", maxBGALayers))
		}
		if layers < minTech {
			o.Verdict = "excluded"
			o.Why = append(o.Why, fmt.Sprintf("BGA 过孔工艺至少需要 %d 层", minTech))
		}
		if maxLayers > 0 && layers > maxLayers {
			o.Verdict = "excluded"
			o.Why = append(o.Why, fmt.Sprintf("超过 %d 层成本上限", maxLayers))
		}
		f.Options = append(f.Options, o)
	}
	// Cheapest ok; else cheapest tight; else the largest allowed.
	pick := func(v string) int {
		for _, o := range f.Options {
			if o.Verdict == v {
				f.ChoiceMixed = o.Mixed
				return o.Layers
			}
		}
		return 0
	}
	if f.Choice = pick("ok"); f.Choice == 0 {
		if f.Choice = pick("tight"); f.Choice != 0 {
			why("no stackup has comfortable margin; %d layers is tight — spread the placement or enlarge the board", f.Choice)
		} else {
			for _, o := range f.Options {
				if o.Verdict != "excluded" {
					f.Choice = o.Layers
				}
			}
			if f.Choice == 0 {
				// Every stackup is excluded: take the largest allowed and say why.
				for _, o := range f.Options {
					if maxLayers <= 0 || o.Layers <= maxLayers {
						f.Choice = o.Layers
					}
				}
			}
			why("even %d layers are short of routing room: the board needs more area, finer rules or HDI", f.Choice)
		}
	}
	f.negotiate(b, all, fo)
	for _, o := range f.Options {
		if o.Layers == f.Choice && o.Mixed == f.ChoiceMixed {
			label := ""
			if o.Mixed {
				label = " mixed (TOP/GND/SIG+PWR/BOTTOM)"
			}
			why("chosen: %d layers%s (%d signal), routing %.0f%% of capacity", o.Layers, label, o.SignalLayers, 100*o.Utilisation)
		}
	}
	return f
}

func (f *Feasibility) addPlaneNeed(what string) {
	for _, w := range f.NeedPlanes {
		if w == what {
			return
		}
	}
	f.NeedPlanes = append(f.NeedPlanes, what)
}

func joinCN(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += "、"
		}
		out += x
	}
	return out
}

// bgaNeed sizes one BGA's escape: rings holding signal balls, rings per
// layer, and the via technology its pitch allows.
func bgaNeed(g *bgaPart, b *Board, an *Analysis) BGANeed {
	need := bgaNeedWith(g, b, an, b.Rules)
	if need.SignalLayers < 99 {
		return need
	}
	// The board rules cannot escape this array. Real designs give the BGA
	// area its own rule set; retry at JLC's 4–6 layer limits and say so.
	region := b.Rules
	region.MinTrack, region.Clearance = math.Min(region.MinTrack, jlcRegionTrack), math.Min(region.Clearance, jlcRegionClear)
	rn := bgaNeedWith(g, b, an, region)
	rn.RegionRule = fmt.Sprintf("BGA 区域规则 %.1f/%.1f mil（嘉立创 4–6 层工艺极限）", jlcRegionTrack, jlcRegionClear)
	return rn
}

// JLC 4–6 layer fine-line capability used for a BGA region rule.
const (
	jlcRegionTrack = 3.5
	jlcRegionClear = 3.5
)

func bgaNeedWith(g *bgaPart, b *Board, an *Analysis, rules Rules) BGANeed {
	ball := g.part.Pads[0].Box.W
	need := BGANeed{Ref: g.part.Ref, PitchMil: g.pitch, BallMil: ball, MinLayers: 2}
	for _, pd := range g.part.Pads {
		if pd.Net == "" {
			continue
		}
		r := an.Plan(pd.Net, b.Rules).Role
		if r == RoleGround || r == RolePower {
			continue
		}
		need.SignalBalls++
		need.EscapeRings = max(need.EscapeRings, g.depth[pd]+1)
	}
	need.TopRings = topEscapeRings(g.pitch, ball, rules)
	drill, dia, why := bgaViaClass(g, rules)
	switch {
	case dia <= 0 && g.pitch >= 19:
		need.ViaTech = "盘中孔（树脂塞孔、电镀盖帽）— " + why
		need.MinLayers = 6
	case dia <= 0:
		need.ViaTech = "HDI 激光盲孔 — " + why
		need.MinLayers = 6
	case why != "":
		need.ViaTech = fmt.Sprintf("通孔狗骨头，BGA 专用过孔 %.1f/%.1f mil（嘉立创 4 层以上工艺，需与板厂确认）", drill, dia)
		need.MinLayers = 4
		need.ViaDrill, need.ViaDia = drill, dia
	default:
		need.ViaTech = fmt.Sprintf("通孔狗骨头，板默认过孔 %.1f/%.1f mil", drill, dia)
		need.ViaDrill, need.ViaDia = drill, dia
	}
	// Escape by boundary crossing: every signal ball deeper than ring r must
	// cross ring r through the gaps of that ring — between balls on the top
	// layer, between dog-bone vias on the others — so for each ring
	//   signals deeper than r ≤ gaps(r) × (k_top + (L−1)·k_inner).
	// Inside the ball field tracks neck down to the minimum width.
	viaDia := dia
	if viaDia <= 0 {
		viaDia = ball // via-in-pad: the via sits in the ball
	}
	w := math.Max(rules.MinTrack, 3)
	tracks := func(gap float64) int {
		k := int(math.Floor((gap - rules.Clearance) / (w + rules.Clearance)))
		return max(0, min(k, 3))
	}
	kTop := tracks(g.pitch - ball)
	kInner := tracks(g.pitch - viaDia)
	need.TopRings, need.InnerRings = 1+kTop, 1+kInner
	ringBalls := map[int]int{}
	deeper := map[int]int{} // signal balls at depth > r
	for _, pd := range g.part.Pads {
		ringBalls[g.depth[pd]]++
	}
	maxDepth := 0
	for _, pd := range g.part.Pads {
		maxDepth = max(maxDepth, g.depth[pd])
	}
	for _, pd := range g.part.Pads {
		if pd.Net == "" {
			continue
		}
		if r := an.Plan(pd.Net, b.Rules).Role; r == RoleGround || r == RolePower {
			continue
		}
		for r := 0; r < g.depth[pd]; r++ {
			deeper[r]++
		}
	}
	need.SignalLayers = 1
	bb := EmptyRect()
	for _, pd := range g.part.Pads {
		bb = bb.AddPoint(pd.Box.C)
	}
	for r := 0; r <= maxDepth; r++ {
		if deeper[r] == 0 {
			continue
		}
		// Gaps along ring r: its geometric perimeter over the pitch. A
		// depopulated ring has wider gaps, never fewer, so counting balls
		// would under-count exactly where the array thins out.
		wr := bb.W() - 2*float64(r)*g.pitch
		hr := bb.H() - 2*float64(r)*g.pitch
		gaps := int(math.Max(4, 2*(wr+hr)/g.pitch))
		_ = ringBalls
		for L := 1; L <= 12; L++ {
			if gaps*(kTop+(L-1)*kInner) >= deeper[r] {
				need.SignalLayers = max(need.SignalLayers, L)
				break
			}
			if L == 12 || kInner == 0 && L > 1 {
				need.SignalLayers = max(need.SignalLayers, 99) // cannot escape with these rules
				break
			}
		}
	}
	return need
}

// sortedBGAs keeps reports stable.
func (f *Feasibility) sortedBGAs() []BGANeed {
	out := append([]BGANeed(nil), f.BGAs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// negotiate turns shortfalls into requests the mechanical side can act on:
// how much larger the board must be, and why.
func (f *Feasibility) negotiate(b *Board, courtyards float64, fo FeasibilityOptions) {
	bb := EmptyRect()
	for _, p := range b.Outline {
		bb = bb.AddPoint(p)
	}
	area := f.BoardAreaIn2 * 1e6
	scaleNeed := 1.0
	var causes []string
	// Routing: the chosen stackup should run at ≤ 75 %.
	for _, o := range f.Options {
		if o.Layers == f.Choice && o.Mixed == f.ChoiceMixed && o.Utilisation > 0.75 {
			k := o.Utilisation / 0.75
			scaleNeed = math.Max(scaleNeed, k)
			causes = append(causes, fmt.Sprintf("布线：%d 层方案利用率 %.0f%%，需要约 %.0f%% 的面积才有 25%% 余量", o.Layers, 100*o.Utilisation, 100*k))
		}
	}
	// Occupancy: a side above 85 % cannot be assembled and routed.
	occ := math.Max(f.TopOccupancy, f.BotOccupancy)
	if !f.Placed {
		occ = courtyards / area / 2 // double-sided at best
	}
	if occ > 0.85 {
		k := occ / 0.8
		scaleNeed = math.Max(scaleNeed, k)
		causes = append(causes, fmt.Sprintf("器件占地：最满的一面 %.0f%%，需要约 %.0f%% 的面积", 100*occ, 100*k))
	}
	// Isolation: each barrier is a creepage-wide strip with nothing on it.
	if len(fo.Barriers) > 0 && bb.W() > 0 {
		short := math.Min(bb.W(), bb.H())
		strip := 0.0
		for _, br := range fo.Barriers {
			strip += br.CreepageMil * short
		}
		used := courtyards/2 + strip
		if !f.Placed {
			used = courtyards + strip
		}
		if k := used / (0.8 * area); k > 1 {
			scaleNeed = math.Max(scaleNeed, k)
			causes = append(causes, fmt.Sprintf("高压隔离：%d 条隔离带（爬电距离合计 %.1f mm）占用 %.1f in²，加上器件后需要约 %.0f%% 的面积",
				len(fo.Barriers), stripMM(fo.Barriers), strip/1e6, 100*k))
		}
	}
	if scaleNeed <= 1 {
		return
	}
	lin := math.Sqrt(scaleNeed)
	msg := fmt.Sprintf("物理空间不足：建议板子放大到约 %.0f×%.0f mm（当前 %.0f×%.0f mm，面积 ×%.2f）", bb.W()*lin*0.0254, bb.H()*lin*0.0254, bb.W()*0.0254, bb.H()*0.0254, scaleNeed)
	f.Negotiation = append([]string{msg}, append(causes, f.Negotiation...)...)
	if f.Choice < 8 {
		f.Negotiation = append(f.Negotiation, "或者：增加层数 / 采用更细的线宽间距（成本上升），需要双方协商")
	}
}

func stripMM(bs []*Barrier) float64 {
	s := 0.0
	for _, b := range bs {
		s += b.CreepageMil * 0.0254
	}
	return s
}
