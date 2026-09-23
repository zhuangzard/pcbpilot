package pcbauto

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// LayerKind separates routable copper from planes.
type LayerKind string

const (
	KindSignal LayerKind = "signal"
	KindPlane  LayerKind = "plane"
)

// StackLayer is one copper layer of the decided stackup, top to bottom.
type StackLayer struct {
	ID    int       `json:"id"` // EasyEDA layer id
	Name  string    `json:"name"`
	Kind  LayerKind `json:"kind"`
	Nets  []string  `json:"nets,omitempty"` // plane nets (first = background)
	Dir   string    `json:"dir,omitempty"`  // preferred routing direction h|v
	Outer bool      `json:"outer"`
	// PourNets are nets flooded on a signal layer after routing (e.g. GND).
	PourNets []string `json:"pourNets,omitempty"`
}

// Stackup is the layer-count decision with its evidence.
type Stackup struct {
	Layers       int          `json:"layers"`
	Stack        []StackLayer `json:"stack"`
	JLCStackup   string       `json:"jlcStackup,omitempty"`
	RefHeightMil float64      `json:"refHeightMil"` // outer signal → nearest plane
	Er           float64      `json:"er"`
	Reasons      []string     `json:"reasons"`
	Metrics      StackMetrics `json:"metrics"`
}

// StackMetrics are the quantities the decision was based on.
type StackMetrics struct {
	AreaIn2          float64 `json:"areaIn2"`
	PadCount         int     `json:"padCount"`
	PinDensity       float64 `json:"pinDensityPerIn2"`
	SignalNets       int     `json:"signalNets"`
	Connections      int     `json:"connections"`
	WireDemandIn     float64 `json:"wireDemandIn"`     // estimated routed length
	DemandAreaIn2    float64 `json:"demandAreaIn2"`    // length × routing pitch
	LayerCapacityIn2 float64 `json:"layerCapacityIn2"` // usable area per signal layer
	SignalLayersNeed float64 `json:"signalLayersNeeded"`
	FinestPitchMil   float64 `json:"finestPitchMil"`
	BGARings         int     `json:"bgaRings"`
	DiffPairs        int     `json:"diffPairs"`
	HSPairs          int     `json:"highSpeedPairs"`
	PowerRails       int     `json:"powerRails"`
	HighCurrentRails int     `json:"highCurrentRails"`
	RFNets           int     `json:"rfNets"`
	Placed           bool    `json:"placed"`
}

// StackOptions constrain the decision.
type StackOptions struct {
	// Force fixes the layer count (0 = decide). Mechanical or cost specs set it.
	Force int
	// MaxLayers caps the decision (cost tier); 0 = 6.
	MaxLayers int
	// Utilisation is the fraction of a signal layer's area usable for tracks
	// after pads, vias and clearances (default 0.28).
	Utilisation float64
}

// looksPlaced reports whether parts occupy distinct positions (vs a fresh
// import where everything is stacked at the origin or in a heap outside).
func looksPlaced(b *Board) bool {
	if len(b.Parts) < 2 {
		return true
	}
	bb := b.Bounds()
	inside, overlap := 0, 0
	bodies := make([]Rect, len(b.Parts))
	for i, p := range b.Parts {
		bodies[i] = p.Body()
		if bb.Contains(bodies[i].Center()) {
			inside++
		}
	}
	for i := range bodies {
		for j := i + 1; j < len(bodies) && j < i+40; j++ {
			if bodies[i].OverlapArea(bodies[j]) > 0.5*math.Min(bodies[i].Area(), bodies[j].Area()) {
				overlap++
			}
		}
	}
	return float64(inside) > 0.8*float64(len(b.Parts)) && float64(overlap) < 0.1*float64(len(b.Parts))
}

// mstLength is the Manhattan minimum-spanning-tree length over points (Prim).
func mstLength(pts []Point) float64 {
	n := len(pts)
	if n < 2 {
		return 0
	}
	in := make([]bool, n)
	d := make([]float64, n)
	for i := range d {
		d[i] = math.Inf(1)
	}
	d[0] = 0
	total := 0.0
	for k := 0; k < n; k++ {
		best := -1
		for i := 0; i < n; i++ {
			if !in[i] && (best < 0 || d[i] < d[best]) {
				best = i
			}
		}
		in[best] = true
		total += d[best]
		for i := 0; i < n; i++ {
			if !in[i] {
				d[i] = math.Min(d[i], pts[best].Manhattan(pts[i]))
			}
		}
	}
	return total
}

// finestPitch returns the smallest centre distance between same-part pads.
func finestPitch(b *Board) float64 {
	best := math.Inf(1)
	for _, p := range b.Parts {
		if len(p.Pads) > 200 {
			continue
		}
		for i := range p.Pads {
			for j := i + 1; j < len(p.Pads); j++ {
				if d := p.Pads[i].Box.C.Dist(p.Pads[j].Box.C); d > 1 && d < best {
					best = d
				}
			}
		}
	}
	return best
}

// bgaRings estimates the escape rings of the densest area-array part.
func bgaRings(b *Board) int {
	best := 0
	for _, p := range b.Parts {
		if len(p.Pads) < 16 {
			continue
		}
		xs, ys := map[int]bool{}, map[int]bool{}
		for _, pd := range p.Pads {
			xs[int(math.Round(pd.Box.C.X))] = true
			ys[int(math.Round(pd.Box.C.Y))] = true
		}
		// An area array has many rows AND many columns with pads inside.
		rows := int(math.Min(float64(len(xs)), float64(len(ys))))
		if rows >= 4 && len(p.Pads) >= int(0.5*float64(len(xs)*len(ys))) {
			rings := (rows + 1) / 2
			if rings > best {
				best = rings
			}
		}
	}
	return best
}

// DecideStackup chooses the layer count and assigns planes. The analysis may
// come from Analyze(b, spec, nil); it is re-run by callers once decided.
func DecideStackup(b *Board, a *Analysis, opt StackOptions) *Stackup {
	if opt.MaxLayers <= 0 {
		opt.MaxLayers = 6
	}
	if opt.Utilisation <= 0 {
		opt.Utilisation = 0.28
	}
	s := &Stackup{}
	m := &s.Metrics
	area := b.Area()
	m.AreaIn2 = area / 1e6
	m.Placed = looksPlaced(b)
	for _, p := range b.Parts {
		m.PadCount += len(p.Pads)
	}
	if m.AreaIn2 > 0 {
		m.PinDensity = float64(m.PadCount) / m.AreaIn2
	}
	m.FinestPitchMil = finestPitch(b)
	if math.IsInf(m.FinestPitchMil, 1) {
		m.FinestPitchMil = 0
	}
	m.BGARings = bgaRings(b)

	var rails []*NetPlan
	pitch := b.Rules.TrackWidth + b.Rules.Clearance
	nets := b.Nets()
	for _, n := range nets {
		np := a.Plan(n.Name, b.Rules)
		switch np.Role {
		case RolePower:
			rails = append(rails, np)
			if np.CurrentA >= 1 {
				m.HighCurrentRails++
			}
			continue
		case RoleGround:
			continue
		case RoleDiff:
			m.DiffPairs++
			if hc := ClassifyHS(np); hc != nil && hc.Name != "USB2" && hc.Name != "CAN/RS-485" {
				m.HSPairs++
			}
		case RoleRF:
			m.RFNets++
		}
		if len(n.Pads) < 2 {
			continue
		}
		m.SignalNets++
		m.Connections += len(n.Pads) - 1
		var l float64
		if m.Placed {
			pts := make([]Point, len(n.Pads))
			for i, pd := range n.Pads {
				pts[i] = pd.Box.C
			}
			l = mstLength(pts)
		} else {
			// Unplaced: Donath-style estimate, average connection ≈ 0.35·√(A/parts)·√parts^0.3
			l = float64(len(n.Pads)-1) * 0.35 * math.Sqrt(area)
		}
		w := math.Max(np.WidthMil, b.Rules.TrackWidth)
		// Detour factor 1.3 over Manhattan MST for real routing.
		m.WireDemandIn += 1.3 * l / 1000
		m.DemandAreaIn2 += 1.3 * l * (w + np.ClearanceMil) / 1e6
	}
	m.DiffPairs /= 2
	m.HSPairs /= 2
	m.PowerRails = len(rails)
	m.LayerCapacityIn2 = m.AreaIn2 * opt.Utilisation
	if m.LayerCapacityIn2 > 0 {
		m.SignalLayersNeed = m.DemandAreaIn2 / m.LayerCapacityIn2
	}
	_ = pitch

	why := func(format string, args ...any) { s.Reasons = append(s.Reasons, fmt.Sprintf(format, args...)) }
	layers := 2
	needPlanes := false
	sigLayers := int(math.Ceil(m.SignalLayersNeed - 0.05))
	if sigLayers < 1 {
		sigLayers = 1
	}
	why("routing demand %.2f in² vs %.2f in²/layer → %.2f signal layers", m.DemandAreaIn2, m.LayerCapacityIn2, m.SignalLayersNeed)
	if m.SignalLayersNeed > 1.6 {
		needPlanes = true
		why("demand exceeds what 2 layers route while keeping a ground return")
	}
	if m.HSPairs > 0 {
		needPlanes = true
		why("%d high-speed differential pair(s) (MIPI/HDMI/USB3/PCIe/Ethernet/LVDS) need an adjacent reference plane for impedance control", m.HSPairs)
	} else if m.DiffPairs > 0 {
		why("%d low-speed differential pair(s) (USB2 full-speed/CAN/RS-485) route fine as coupled pairs on 2 layers", m.DiffPairs)
	}
	if m.RFNets > 0 {
		needPlanes = true
		why("RF nets need a solid ground reference under the feed line")
	}
	if m.BGARings >= 3 {
		needPlanes = true
		why("area-array part with %d escape rings", m.BGARings)
	}
	if m.FinestPitchMil > 0 && m.FinestPitchMil < 20 && m.PinDensity > 80 {
		needPlanes = true
		why("fine pitch %.1f mil at %.0f pins/in²", m.FinestPitchMil, m.PinDensity)
	}
	if m.PowerRails >= 3 && m.SignalLayersNeed > 1.0 {
		needPlanes = true
		why("%d supply rails on a board already short of routing room — a split power plane frees the signal layers", m.PowerRails)
	}
	if needPlanes {
		layers = 4
		// Outer layers route; each extra pair adds two inner signal layers.
		if m.SignalLayersNeed > 2.2 || m.BGARings >= 4 {
			layers = 6
			why("signal demand %.2f layers or deep BGA exceeds two outer layers", m.SignalLayersNeed)
		}
	} else {
		why("2 layers suffice: low demand, no impedance-controlled or RF nets")
	}
	if layers > opt.MaxLayers {
		why("capped at %d layers by cost tier (wanted %d)", opt.MaxLayers, layers)
		layers = opt.MaxLayers
	}
	if opt.Force > 0 {
		if opt.Force != layers {
			why("forced to %d layers by specification (decision was %d)", opt.Force, layers)
		}
		layers = opt.Force
	}
	s.Layers = layers
	s.buildStack(rails, b)
	return s
}

// buildStack assigns layer roles and plane nets for the chosen layer count.
func (s *Stackup) buildStack(rails []*NetPlan, b *Board) {
	gnd := groundNet(b)
	sort.SliceStable(rails, func(i, j int) bool {
		// Background of the power plane: the rail with most pads (widest spread).
		if rails[i].PadCount != rails[j].PadCount {
			return rails[i].PadCount > rails[j].PadCount
		}
		return rails[i].Net < rails[j].Net
	})
	railNames := make([]string, 0, len(rails))
	for _, r := range rails {
		railNames = append(railNames, r.Net)
	}
	var gnds []string
	if gnd != "" {
		gnds = []string{gnd}
	}
	// Functional split grounds (starGrounds) are NOT put on the ground plane
	// yet: the Voronoi split grows their region from fan-out seeds and swallowed
	// main-ground pads (ESP32-S3: GND pad failures 8 → 111). Planned: region =
	// the split ground's pad envelope, background = main ground.
	planeGnds := gnds
	switch s.Layers {
	case 2:
		s.Stack = []StackLayer{
			{ID: LayerTop, Name: "TOP", Kind: KindSignal, Dir: "h", Outer: true, PourNets: gnds},
			{ID: LayerBottom, Name: "BOTTOM", Kind: KindSignal, Dir: "v", Outer: true, PourNets: gnds},
		}
		s.JLCStackup = "JLC 2-layer 1.6mm FR4"
		s.RefHeightMil, s.Er = 62.99, 4.5
	case 4:
		s.Stack = []StackLayer{
			{ID: LayerTop, Name: "TOP", Kind: KindSignal, Dir: "h", Outer: true},
			{ID: LayerInner1, Name: "IN1-GND", Kind: KindPlane, Nets: planeGnds},
			{ID: LayerInner1 + 1, Name: "IN2-PWR", Kind: KindPlane, Nets: railNames},
			{ID: LayerBottom, Name: "BOTTOM", Kind: KindSignal, Dir: "v", Outer: true, PourNets: gnds},
		}
		s.JLCStackup = "JLC04161H-7628"
		s.RefHeightMil, s.Er = 8.28, 4.4
	default:
		s.Layers = 6
		// SIG / GND / SIG / PWR / GND / SIG: every signal layer adjacent to a plane.
		s.Stack = []StackLayer{
			{ID: LayerTop, Name: "TOP", Kind: KindSignal, Dir: "h", Outer: true},
			{ID: LayerInner1, Name: "IN1-GND", Kind: KindPlane, Nets: planeGnds},
			{ID: LayerInner1 + 1, Name: "IN2-SIG", Kind: KindSignal, Dir: "v"},
			{ID: LayerInner1 + 2, Name: "IN3-PWR", Kind: KindPlane, Nets: railNames},
			{ID: LayerInner1 + 3, Name: "IN4-GND", Kind: KindPlane, Nets: planeGnds},
			{ID: LayerBottom, Name: "BOTTOM", Kind: KindSignal, Dir: "v", Outer: true},
		}
		s.JLCStackup = "JLC06161H-2116"
		s.RefHeightMil, s.Er = 4.4, 4.2
	}
	if s.Layers >= 4 && len(railNames) == 0 {
		// No rails: the power plane becomes a second ground plane.
		for i := range s.Stack {
			if s.Stack[i].Kind == KindPlane && len(s.Stack[i].Nets) == 0 {
				s.Stack[i].Nets = planeGnds
				s.Stack[i].Name = fmt.Sprintf("IN%d-GND", i)
			}
		}
	}
}

// starGrounds are the functional split grounds: ground-named nets with at
// least three pads that meet the main ground through one two-pad tie (0 Ω
// resistor, ferrite bead, inductor or net tie) — the star point. An isolated
// ground has no such tie and is left off the main ground's plane layer, where
// a split gap could never meet its creepage distance.
func starGrounds(b *Board, gnd string) []string {
	if gnd == "" {
		return nil
	}
	tied := map[string]bool{}
	for _, p := range b.Parts {
		if len(p.Pads) != 2 {
			continue
		}
		prefix := strings.TrimRightFunc(upper(p.Ref), func(r rune) bool { return r >= '0' && r <= '9' })
		switch prefix {
		case "R", "L", "FB", "NT", "J", "JP":
		default:
			continue
		}
		a, c := p.Pads[0].Net, p.Pads[1].Net
		if a == gnd && c != gnd && reGround.MatchString(upper(c)) {
			tied[c] = true
		}
		if c == gnd && a != gnd && reGround.MatchString(upper(a)) {
			tied[a] = true
		}
	}
	var out []string
	for _, n := range b.Nets() {
		if tied[n.Name] && len(n.Pads) >= 3 {
			out = append(out, n.Name)
		}
	}
	sort.Strings(out)
	return out
}

// groundNet picks the main ground net (most pads among ground-named nets).
func groundNet(b *Board) string {
	best, bestN := "", 0
	for _, n := range b.Nets() {
		if reGround.MatchString(upper(n.Name)) && len(n.Pads) > bestN {
			best, bestN = n.Name, len(n.Pads)
		}
	}
	return best
}

// SignalLayers returns routable layer ids top→bottom.
func (s *Stackup) SignalLayers() []int {
	var out []int
	for _, l := range s.Stack {
		if l.Kind == KindSignal {
			out = append(out, l.ID)
		}
	}
	return out
}

// PlaneFor returns the plane layer carrying net, if any.
func (s *Stackup) PlaneFor(net string) (StackLayer, bool) {
	for _, l := range s.Stack {
		if l.Kind != KindPlane {
			continue
		}
		for _, n := range l.Nets {
			if n == net {
				return l, true
			}
		}
	}
	return StackLayer{}, false
}

// AllLayerIDs returns every copper layer id top→bottom.
func (s *Stackup) AllLayerIDs() []int {
	out := make([]int, len(s.Stack))
	for i, l := range s.Stack {
		out[i] = l.ID
	}
	return out
}

// MixedPower converts the power plane of a 4+ layer stack into a mixed
// layer: it routes signals and carries the rails as pours in the space left
// (split by rail), the usual fix when two outer layers cannot route a dense
// board. It returns false when there is no such plane.
func (s *Stackup) MixedPower() bool {
	gnd := ""
	for _, l := range s.Stack {
		if l.Kind == KindPlane && len(l.Nets) > 0 && reGround.MatchString(upper(l.Nets[0])) {
			gnd = l.Nets[0]
			break
		}
	}
	for i := range s.Stack {
		l := &s.Stack[i]
		if l.Kind != KindPlane || len(l.Nets) == 0 || l.Nets[0] == gnd {
			continue
		}
		l.Kind, l.PourNets, l.Nets = KindSignal, l.Nets, nil
		l.Dir = "v"
		l.Name = strings.Replace(l.Name, "PWR", "SIG+PWR", 1)
		// Keep the outer layers orthogonal to the new inner routing layer.
		for j := range s.Stack {
			if s.Stack[j].Outer && s.Stack[j].ID == LayerBottom {
				s.Stack[j].Dir = "h"
			}
		}
		s.Reasons = append(s.Reasons, "power plane converted to a mixed signal+power-pour layer: outer layers alone could not route the board")
		return true
	}
	return false
}
