package app

// kicad_sch_build_layout.go — zone layouts for `kicad sch-build`: every zone
// is placed and wired independently (core-relative KiCad mm, y down) by the
// offline layout planner (PlanSchematicLayout, the `sch layout-plan`
// engine), else by a grid + the autoconnect planner. A zone layout is cached
// in the build state under a hash of its inputs, so `kicad sch-edit` redraws
// only the zones an edit touches.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbPose is one placed symbol unit (zone-relative mm).
type sbPose struct {
	At   kicad.Pt `json:"at"`
	Rot  float64  `json:"rot"`
	Unit int      `json:"unit,omitempty"`
}

// sbMarker is a power/ground symbol or a net label (Kind power | ground |
// label). Dir is the stub direction (up/down/left/right) the marker reads
// along; OnWire labels sit on an existing wire (no stub).
type sbMarker struct {
	Kind   string   `json:"kind"`
	Net    string   `json:"net"`
	At     kicad.Pt `json:"at"`
	Dir    string   `json:"dir"`
	OnWire bool     `json:"onWire,omitempty"`
}

// sbZoneLayout is one zone drawn relative to its core.
type sbZoneLayout struct {
	Hash       string              `json:"hash"`
	Method     string              `json:"method"` // planner | grid-autoconnect
	Parts      map[string][]sbPose `json:"parts"`
	Wires      [][]kicad.Pt        `json:"wires"`
	Markers    []sbMarker          `json:"markers"`
	NoConnects []kicad.Pt          `json:"noConnects,omitempty"`
	Box        kicad.Box           `json:"box"`
	Notes      []string            `json:"notes,omitempty"`
	Ms         float64             `json:"ms"`
	Findings   []string            `json:"findings,omitempty"` // sch-check quality findings of the zone alone
}

// sbZoneFindings renders one zone alone on a scratch sheet and runs the
// kicad sch-check quality checks on it.
func sbZoneFindings(d *sbDesign, zi *sbZoneIn, zl *sbZoneLayout) []string {
	e, err := kicad.OpenSchematic(kicad.NewSchematicText("A0", kicad.NewUUID(), true))
	if err != nil {
		return []string{err.Error()}
	}
	e.Project, e.InstancePath = "zone", "/x"
	t := kicad.Pt{X: sbSnap(60 - zl.Box.MinX), Y: sbSnap(60 - zl.Box.MinY)}
	if err := renderSbPage(e, d, newSbState(), sbPage{ID: zi.Zone.Page}, []*sbZoneIn{zi},
		map[string]*sbZoneLayout{zi.Zone.ID: zl}, map[string]kicad.Pt{zi.Zone.ID: t}, nil); err != nil {
		return []string{err.Error()}
	}
	text, err := e.Render()
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	for _, f := range kicad.CheckSchematic(text, kicad.CheckOptions{}) {
		out = append(out, f.Kind+": "+f.Message)
	}
	if pg, ok := sbEGPageOf("zone", text, d, nil); ok { // the engineer-grade hard rules too
		for _, h := range pg.Hard {
			if !strings.HasPrefix(h, "EG-06") {
				out = append(out, h)
			}
		}
		for _, c := range pg.Decoupling { // one finding per far cap, weighted
			out = append(out, "EG-06 "+c, "EG-06 "+c)
		}
	}
	return out
}

func sbShortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:4])
}

// ── symbol measurement ──────────────────────────────────────────────────────

// sbMeasure places each distinct library symbol at the origin (rotation 0)
// on a scratch sheet and reads its box and pins back.
type sbMeasure struct {
	mu    sync.Mutex
	cache map[string]kicad.SceneSymbol // libID|unit
}

func (m *sbMeasure) get(p *sbRPart, unit int) (kicad.SceneSymbol, error) {
	key := fmt.Sprintf("%s|%d", p.LibID, unit)
	m.mu.Lock()
	if s, ok := m.cache[key]; ok {
		m.mu.Unlock()
		return s, nil
	}
	m.mu.Unlock()
	e, err := kicad.OpenSchematic(kicad.NewSchematicText("A0", kicad.NewUUID(), true))
	if err != nil {
		return kicad.SceneSymbol{}, err
	}
	if err := e.AddLibSymbol(p.LibID, p.SymText); err != nil {
		return kicad.SceneSymbol{}, err
	}
	if _, err := e.PlaceSymbol(kicad.SymbolInstance{LibID: p.LibID, Ref: "X1", Unit: unit}); err != nil {
		return kicad.SceneSymbol{}, err
	}
	text, err := e.Render()
	if err != nil {
		return kicad.SceneSymbol{}, err
	}
	e2, err := kicad.OpenSchematic(text)
	if err != nil {
		return kicad.SceneSymbol{}, err
	}
	sc, err := e2.Scene()
	if err != nil || len(sc.Symbols) != 1 {
		return kicad.SceneSymbol{}, fmt.Errorf("%s: cannot measure symbol %s: %v", p.Ref, p.LibID, err)
	}
	s := sc.Symbols[0]
	if !s.HasBox {
		s.Box = kicad.Box{MinX: -2.54, MinY: -2.54, MaxX: 2.54, MaxY: 2.54}
		s.HasBox = true
	}
	m.mu.Lock()
	m.cache[key] = s
	m.mu.Unlock()
	return s, nil
}

// sbUnits lists the units of a part (1 for single-unit symbols).
func sbUnits(p *sbRPart) []int {
	if !p.Multi {
		return []int{1}
	}
	seen := map[int]bool{}
	var out []int
	for _, q := range p.LPins {
		if q.Unit > 0 && !seen[q.Unit] {
			seen[q.Unit] = true
			out = append(out, q.Unit)
		}
	}
	sort.Ints(out)
	return out
}

// sbTransform maps a point/box measured at the origin (rotation 0) to pose.
func sbXform(p kicad.Pt, pose sbPose) kicad.Pt {
	a := pose.Rot * math.Pi / 180
	c, s := math.Round(math.Cos(a)), math.Round(math.Sin(a))
	// screen frame, y down: KiCad rotates counter-clockwise on screen
	x, y := p.X*c+p.Y*s, -p.X*s+p.Y*c
	return kicad.Pt{X: math.Round((pose.At.X+x)*1e4) / 1e4, Y: math.Round((pose.At.Y+y)*1e4) / 1e4}
}

func sbXformBox(b kicad.Box, pose sbPose) kicad.Box {
	out := kicad.Box{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	for _, c := range []kicad.Pt{{X: b.MinX, Y: b.MinY}, {X: b.MaxX, Y: b.MinY}, {X: b.MinX, Y: b.MaxY}, {X: b.MaxX, Y: b.MaxY}} {
		q := sbXform(c, pose)
		out.MinX, out.MinY = math.Min(out.MinX, q.X), math.Min(out.MinY, q.Y)
		out.MaxX, out.MaxY = math.Max(out.MaxX, q.X), math.Max(out.MaxY, q.Y)
	}
	return out
}

func sbBoxUnion(a, b kicad.Box) kicad.Box {
	if math.IsInf(a.MinX, 1) || a == (kicad.Box{}) {
		return b
	}
	return kicad.Box{MinX: math.Min(a.MinX, b.MinX), MinY: math.Min(a.MinY, b.MinY), MaxX: math.Max(a.MaxX, b.MaxX), MaxY: math.Max(a.MaxY, b.MaxY)}
}

func sbGrow(b kicad.Box, d float64) kicad.Box {
	return kicad.Box{MinX: b.MinX - d, MinY: b.MinY - d, MaxX: b.MaxX + d, MaxY: b.MaxY + d}
}

func sbOverlap(a, b kicad.Box) bool {
	return a.MinX < b.MaxX && b.MinX < a.MaxX && a.MinY < b.MaxY && b.MinY < a.MaxY
}

// sbTextW estimates a 1.27 mm text width.
func sbTextW(s string) float64 { return 1.1*float64(len([]rune(s))) + 0.6 }

// sbFieldPlan places Reference / Value next to the body (absolute mm):
// above/below when those sides carry no pins, else beside it.
type sbFieldPlan struct {
	Ref, Val     kicad.Pt
	RefBox, VBox kicad.Box
}

func sbFields(sym kicad.SceneSymbol, pose sbPose, ref, value string) sbFieldPlan {
	box := sbXformBox(sym.Box, pose)
	sides := map[string]bool{}
	for _, q := range sym.Pins {
		at := sbXform(kicad.Pt{X: q.At.X - sym.At.X, Y: q.At.Y - sym.At.Y}, pose)
		const eps = 0.05
		switch {
		case math.Abs(at.Y-box.MinY) < eps:
			sides["top"] = true
		case math.Abs(at.Y-box.MaxY) < eps:
			sides["bottom"] = true
		case math.Abs(at.X-box.MinX) < eps:
			sides["left"] = true
		case math.Abs(at.X-box.MaxX) < eps:
			sides["right"] = true
		}
	}
	cx, cy := (box.MinX+box.MaxX)/2, (box.MinY+box.MaxY)/2
	snap := func(v float64) float64 { return math.Round(v/0.635) * 0.635 }
	tb := func(at kicad.Pt, s string) kicad.Box {
		w := sbTextW(s)
		return kicad.Box{MinX: at.X - w/2, MinY: at.Y - 0.9, MaxX: at.X + w/2, MaxY: at.Y + 0.9}
	}
	var f sbFieldPlan
	switch {
	case !sides["top"] && !sides["bottom"]:
		f.Ref = kicad.Pt{X: snap(cx), Y: snap(box.MinY - 1.27)}
		f.Val = kicad.Pt{X: snap(cx), Y: snap(box.MaxY + 1.27)}
	case !sides["right"]:
		w := math.Max(sbTextW(ref), sbTextW(value))
		x := snap(box.MaxX + 0.8 + w/2)
		f.Ref, f.Val = kicad.Pt{X: x, Y: snap(cy - 1.27)}, kicad.Pt{X: x, Y: snap(cy + 1.27)}
	case !sides["left"]:
		w := math.Max(sbTextW(ref), sbTextW(value))
		x := snap(box.MinX - 0.8 - w/2)
		f.Ref, f.Val = kicad.Pt{X: x, Y: snap(cy - 1.27)}, kicad.Pt{X: x, Y: snap(cy + 1.27)}
	default:
		f.Ref = kicad.Pt{X: snap(cx), Y: snap(box.MinY - 1.27)}
		f.Val = kicad.Pt{X: snap(cx), Y: snap(box.MaxY + 1.27)}
	}
	f.RefBox, f.VBox = tb(f.Ref, ref), tb(f.Val, value)
	return f
}

// ── zone inputs + hash ──────────────────────────────────────────────────────

type sbZoneIn struct {
	Zone   sbZone
	Parts  []*sbRPart // core first
	Policy map[string]string
}

// zoneInputs groups the design's parts by zone and derives the net
// policies the planner uses for each zone.
func (d *sbDesign) zoneInputs() []*sbZoneIn {
	var out []*sbZoneIn
	netZones := map[string]map[string]bool{}
	for k, n := range d.PinNet {
		ref, _, _ := strings.Cut(k, ".")
		if netZones[n] == nil {
			netZones[n] = map[string]bool{}
		}
		netZones[n][d.ByRef[ref].ZoneID] = true
	}
	for _, z := range d.Zones {
		zi := &sbZoneIn{Zone: z, Policy: map[string]string{}}
		for _, p := range d.Parts {
			if p.ZoneID == z.ID {
				zi.Parts = append(zi.Parts, p)
			}
		}
		if len(zi.Parts) == 0 {
			continue
		}
		core := z.Core
		if core == "" || d.ByRef[core] == nil || d.ByRef[core].ZoneID != z.ID {
			best := -1
			for _, p := range zi.Parts {
				if len(p.LPins) > best {
					best, core = len(p.LPins), p.Ref
				}
			}
		}
		sort.SliceStable(zi.Parts, func(i, j int) bool {
			if (zi.Parts[i].Ref == core) != (zi.Parts[j].Ref == core) {
				return zi.Parts[i].Ref == core
			}
			return refLessSB(zi.Parts[i].Ref, zi.Parts[j].Ref)
		})
		for _, p := range zi.Parts {
			for _, q := range p.LPins {
				n, ok := d.PinNet[sbPinKey(p.Ref, q.Number)]
				if !ok {
					continue
				}
				switch d.NetKind[n] {
				case "ground":
					zi.Policy[n] = "local_ground"
				case "power":
					zi.Policy[n] = "local_power"
				default:
					inZone := 0
					for _, k := range d.netPins(n) {
						r, _, _ := strings.Cut(k, ".")
						if d.ByRef[r].ZoneID == z.ID {
							inZone++
						}
					}
					if len(netZones[n]) == 1 && inZone >= 2 {
						zi.Policy[n] = "direct"
					} else {
						zi.Policy[n] = "module_port"
					}
				}
			}
		}
		out = append(out, zi)
	}
	// reading order follows the power / signal flow: connectors and inputs,
	// regulators, the main controller, then peripherals (stable otherwise)
	rank := func(zi *sbZoneIn) int {
		core := zi.Parts[0]
		hasConn, hasReg := false, false
		for _, p := range zi.Parts {
			switch sbRefPrefix(p.Ref) {
			case "J", "CN", "P", "USB":
				hasConn = true
			case "L":
				hasReg = true
			}
			if len(p.PowerPins) > 0 && len(p.LPins) <= 8 {
				hasReg = true
			}
		}
		maxPins := 0
		for _, q := range out {
			if q.Zone.Page == zi.Zone.Page {
				maxPins = max(maxPins, len(q.Parts[0].LPins))
			}
		}
		switch {
		case hasConn && len(core.LPins) < maxPins:
			return 0
		case hasReg:
			return 1
		case len(core.LPins) == maxPins && maxPins > 8:
			return 2
		}
		return 3
	}
	if !d.ZoneOrderFixed {
		sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	}
	return out
}

func (d *sbDesign) netPins(n string) []string {
	for _, nn := range d.Nets {
		if nn.Name == n {
			return nn.Pins
		}
	}
	return nil
}

func refLessSB(a, b string) bool {
	pa, pb := sbRefPrefix(a), sbRefPrefix(b)
	if pa != pb {
		return pa < pb
	}
	return sbPinLess(strings.TrimPrefix(a, pa), strings.TrimPrefix(b, pb))
}

// hash covers what shapes the zone: parts, symbols, pin connectivity (nets
// numbered canonically, so a rename keeps the hash), policies, NC pins.
func (zi *sbZoneIn) hash(d *sbDesign, method string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "v1|%s|", method)
	canon := map[string]int{}
	for _, p := range zi.Parts {
		fmt.Fprintf(&b, "%s=%s|", p.Ref, sbShortHash(p.SymText))
		for _, q := range p.LPins {
			k := sbPinKey(p.Ref, q.Number)
			if n, ok := d.PinNet[k]; ok {
				if _, seen := canon[n]; !seen {
					canon[n] = len(canon)
				}
				fmt.Fprintf(&b, "%s:%d/%s/%s,", q.Number, canon[n], zi.Policy[n], d.NetKind[n])
			} else if d.NC[k] {
				fmt.Fprintf(&b, "%s:nc,", q.Number)
			}
		}
		fmt.Fprintf(&b, "%d|", len(sbFieldText(p)))
	}
	// label lengths of ports shape the layout too
	var ports []string
	for n, pol := range zi.Policy {
		if pol == "module_port" {
			ports = append(ports, fmt.Sprintf("%d:%d", canon[n], len(n)))
		}
	}
	sort.Strings(ports)
	b.WriteString(strings.Join(ports, ","))
	return sbShortHash(b.String()) + sbShortHash(b.String()+"#")
}

func sbFieldText(p *sbRPart) string { return p.Ref + "/" + p.Value }

// ── planner path ────────────────────────────────────────────────────────────

func ptsToEE(p kicad.Pt) (float64, float64) { return mmToEE(p) }

// planZone runs the offline layout planner on one zone.
func planZone(d *sbDesign, zi *sbZoneIn, m *sbMeasure, budget int) (*sbZoneLayout, error) {
	in := SchematicLayoutInput{SchemaVersion: 1, NetPolicies: map[string]string{}, MaxCandidates: budget}
	syms := map[string]kicad.SceneSymbol{}
	for _, p := range zi.Parts {
		if p.Multi {
			return nil, fmt.Errorf("%s is multi-unit (planner places single units)", p.Ref)
		}
		s, err := m.get(p, 1)
		if err != nil {
			return nil, err
		}
		if len(s.Pins) == 0 {
			return nil, fmt.Errorf("%s has no pins", p.Ref)
		}
		syms[p.Ref] = s
		meas := SchematicPlacement{Designator: p.Ref, Value: p.Value, BBox: boxToEE(sbPinClampedBox(s))}
		f := sbFields(s, sbPose{}, p.Ref, p.Value)
		meas.TextBBoxes = []layoutBBox{boxToEE(f.RefBox), boxToEE(f.VBox)}
		states := map[string]string{}
		seen := map[string]bool{}
		for _, q := range s.Pins {
			if seen[q.Number] {
				return nil, fmt.Errorf("%s: duplicate pin number %s", p.Ref, q.Number)
			}
			seen[q.Number] = true
			x, y := ptsToEE(q.At)
			r := q.Outward
			k := sbPinKey(p.Ref, q.Number)
			net := d.PinNet[k]
			if net == "" {
				if d.NC[k] {
					states[q.Number] = "nc"
				} else {
					states[q.Number] = "unconnected"
				}
			} else {
				in.NetPolicies[net] = zi.Policy[net]
			}
			meas.Pins = append(meas.Pins, SchematicPin{Number: q.Number, Name: q.Name, Net: net, X: x, Y: y, Rotation: &r})
		}
		comp := SchematicLayoutComponent{ID: p.Ref, Measurement: meas}
		if p.Ref != zi.Parts[0].Ref && len(s.Pins) <= 3 {
			comp.AllowedRotations = []float64{0, 90, 180, 270} // small parts may turn to meet their pin
		}
		if len(states) > 0 {
			comp.PinStates = states
		}
		in.Components = append(in.Components, comp)
	}
	in.CoreComponentID = zi.Parts[0].Ref
	// the planner's own beautify and bounded rotation refinement (buses are
	// not drawn on KiCad: off)
	if len(syms[zi.Parts[0].Ref].Pins) <= 16 { // both passes are costly around a big core
		noBus := false
		in.Aesthetics = &SchematicAestheticsOptions{Style: "balanced", NativeBus: &noBus}
		in.Optimization = &SchematicLayoutOptimization{MaxVariants: 2, MaxAttempts: 8}
	}
	res, err := PlanSchematicLayout(in)
	if err != nil {
		return nil, err
	}
	zl := &sbZoneLayout{Method: "planner", Parts: map[string][]sbPose{}}
	for _, pl := range res.Placements {
		s, ok := syms[pl.Designator]
		if !ok {
			return nil, fmt.Errorf("planner placed unknown %s", pl.Designator)
		}
		at := eeToMM(pl.X, pl.Y)
		want := map[string]kicad.Pt{}
		for _, q := range pl.Pins {
			want[q.Number] = eeToMM(q.X, q.Y)
		}
		found := false
		for _, r := range []float64{math.Mod(pl.Rotation+360, 360), 0, 90, 180, 270} {
			pose := sbPose{At: at, Rot: r}
			ok := true
			for _, q := range s.Pins {
				w, has := want[q.Number]
				g := sbXform(q.At, pose)
				if has && (math.Abs(w.X-g.X) > 0.01 || math.Abs(w.Y-g.Y) > 0.01) {
					ok = false
					break
				}
			}
			if ok {
				zl.Parts[pl.Designator] = []sbPose{pose}
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("planner pose of %s matches no KiCad rotation", pl.Designator)
		}
	}
	if len(zl.Parts) != len(zi.Parts) {
		return nil, fmt.Errorf("planner placed %d of %d parts", len(zl.Parts), len(zi.Parts))
	}
	netWires := map[string][][]kicad.Pt{}
	for _, w := range res.Wires {
		var pts []kicad.Pt
		for _, p := range w.Points {
			pts = append(pts, eeToMM(p[0], p[1]))
		}
		if len(pts) >= 2 {
			zl.Wires = append(zl.Wires, pts)
			netWires[w.Net] = append(netWires[w.Net], pts)
		}
	}
	marked := map[string]bool{}
	for _, f := range res.Flags {
		ex, ey := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
		from, to := eeToMM(f.PinX, f.PinY), eeToMM(ex, ey)
		if from != to {
			zl.Wires = append(zl.Wires, []kicad.Pt{from, to})
		}
		kind := "label"
		switch f.Kind {
		case "power":
			kind = "power"
		case "ground":
			kind = "ground"
		}
		zl.Markers = append(zl.Markers, sbMarker{Kind: kind, Net: f.Net, At: to, Dir: f.Direction})
		marked[f.Net] = true
	}
	// every net needs its name on the sheet: label direct nets on their
	// longest wire segment
	var direct []string
	for n, pol := range zi.Policy {
		if (pol == "direct" || pol == "direct_label") && !marked[n] {
			direct = append(direct, n)
		}
	}
	sort.Strings(direct)
	for _, n := range direct {
		mk, ok := sbWireLabel(n, netWires[n])
		if !ok {
			return nil, fmt.Errorf("direct net %s has no wire to carry its label", n)
		}
		zl.Markers = append(zl.Markers, mk)
	}
	for _, p := range zi.Parts {
		pose := zl.Parts[p.Ref][0]
		for _, q := range syms[p.Ref].Pins {
			if d.NC[sbPinKey(p.Ref, q.Number)] {
				zl.NoConnects = append(zl.NoConnects, sbXform(q.At, pose))
			}
		}
	}
	zl.Box = zoneBox(zl, zi, syms, m)
	return zl, nil
}

// sbPinClampedBox is the symbol box with every pin end on its side: body
// graphics that reach past a pin end (an LED's arrows) are cut off, as the
// planner leaves each pin outward from the box edge.
func sbPinClampedBox(s kicad.SceneSymbol) kicad.Box {
	b := s.Box
	for _, q := range s.Pins {
		switch sbOutwardDir(q.Outward) {
		case "left":
			b.MinX = math.Max(b.MinX, q.At.X)
		case "right":
			b.MaxX = math.Min(b.MaxX, q.At.X)
		case "up":
			b.MinY = math.Max(b.MinY, q.At.Y)
		case "down":
			b.MaxY = math.Min(b.MaxY, q.At.Y)
		}
	}
	if b.MaxX-b.MinX < 0.5 || b.MaxY-b.MinY < 0.5 {
		return s.Box
	}
	return b
}

// sbWireLabel puts a net label on the middle of the longest wire segment of
// the net (horizontal preferred), on the 1.27 mm grid.
func sbWireLabel(net string, wires [][]kicad.Pt) (sbMarker, bool) {
	best, bestLen, horiz := [2]kicad.Pt{}, -1.0, false
	for _, w := range wires {
		for i := 0; i+1 < len(w); i++ {
			a, b := w[i], w[i+1]
			h := math.Abs(a.Y-b.Y) < 1e-6
			l := math.Hypot(a.X-b.X, a.Y-b.Y)
			if h {
				l += 1000 // prefer horizontal runs
			}
			if l > bestLen {
				best, bestLen, horiz = [2]kicad.Pt{a, b}, l, h
			}
		}
	}
	if bestLen < 0 {
		return sbMarker{}, false
	}
	g := func(v float64) float64 { return math.Round(v/1.27) * 1.27 }
	a, b := best[0], best[1]
	if horiz {
		x0 := math.Min(a.X, b.X)
		return sbMarker{Kind: "label", Net: net, At: kicad.Pt{X: g(x0 + 1.27), Y: a.Y}, Dir: "right", OnWire: true}, true
	}
	y0 := math.Max(a.Y, b.Y)
	return sbMarker{Kind: "label", Net: net, At: kicad.Pt{X: a.X, Y: g(y0 - 1.27)}, Dir: "up", OnWire: true}, true
}

// zoneBox is the extent of everything drawn in a zone (fields included).
func zoneBox(zl *sbZoneLayout, zi *sbZoneIn, syms map[string]kicad.SceneSymbol, m *sbMeasure) kicad.Box {
	b := kicad.Box{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	add := func(x kicad.Box) {
		b.MinX, b.MinY = math.Min(b.MinX, x.MinX), math.Min(b.MinY, x.MinY)
		b.MaxX, b.MaxY = math.Max(b.MaxX, x.MaxX), math.Max(b.MaxY, x.MaxY)
	}
	for _, p := range zi.Parts {
		for _, pose := range zl.Parts[p.Ref] {
			s, ok := syms[p.Ref]
			if !ok || pose.Unit > 1 {
				s, _ = m.get(p, max(pose.Unit, 1))
			}
			add(sbXformBox(s.Box, pose))
			f := sbFields(s, pose, p.Ref, p.Value)
			add(f.RefBox)
			add(f.VBox)
		}
	}
	for _, w := range zl.Wires {
		for _, p := range w {
			add(kicad.Box{MinX: p.X, MinY: p.Y, MaxX: p.X, MaxY: p.Y})
		}
	}
	for _, mk := range zl.Markers {
		add(sbMarkerBox(mk))
	}
	if math.IsInf(b.MinX, 1) {
		return kicad.Box{}
	}
	return b
}

// sbMarkerBox estimates a marker's drawn extent.
func sbMarkerBox(mk sbMarker) kicad.Box {
	p := mk.At
	switch mk.Kind {
	case "power", "ground":
		// drawn upright (supply up, ground down; a stub arriving the other
		// way jogs 2.54 mm right first): body + value text
		half, reach := math.Max(1.9, sbTextW(mk.Net)/2+0.6), 5.6
		if (mk.Kind == "power" && mk.Dir == "down") || (mk.Kind == "ground" && mk.Dir == "up") {
			p.X += 2.54
		}
		if mk.Kind == "power" {
			return kicad.Box{MinX: p.X - half, MinY: p.Y - reach, MaxX: p.X + half, MaxY: p.Y}
		}
		return kicad.Box{MinX: p.X - half, MinY: p.Y, MaxX: p.X + half, MaxY: p.Y + reach}
	}
	w := sbTextW(mk.Net) + 2
	switch mk.Dir {
	case "left":
		return kicad.Box{MinX: p.X - w, MinY: p.Y - 1.6, MaxX: p.X, MaxY: p.Y + 1.6}
	case "up":
		return kicad.Box{MinX: p.X - 1.6, MinY: p.Y - w, MaxX: p.X + 1.6, MaxY: p.Y}
	case "down":
		return kicad.Box{MinX: p.X - 1.6, MinY: p.Y, MaxX: p.X + 1.6, MaxY: p.Y + w}
	}
	return kicad.Box{MinX: p.X, MinY: p.Y - 1.6, MaxX: p.X + w, MaxY: p.Y + 1.6}
}

// ── fallback: grid + autoconnect ────────────────────────────────────────────

const sbGrid = 2.54

func sbSnap(v float64) float64 { return math.Round(v/sbGrid) * sbGrid }

// gridZone places the zone's parts in columns right of the core and
// connects every pin with the autoconnect planner (power/ground symbols and
// labels on short stubs). Pins the planner cannot land get a plain stub.
func gridZone(d *sbDesign, zi *sbZoneIn, m *sbMeasure, v sbGridVariant) (*sbZoneLayout, error) {
	// a part whose attach wire leaves another pin no clean stub is placed
	// without attaching, and the zone is redrawn (a few rounds)
	exclude := map[string]bool{}
	var last error
	for round := 0; round < 5; round++ {
		zl, bad, err := gridZoneOnce(d, zi, m, v, exclude)
		if err == nil {
			return zl, nil
		}
		last = err
		if len(bad) == 0 {
			break
		}
		for _, r := range bad {
			exclude[r] = true
		}
	}
	return nil, last
}

func gridZoneOnce(d *sbDesign, zi *sbZoneIn, m *sbMeasure, v sbGridVariant, exclude map[string]bool) (*sbZoneLayout, []string, error) {
	zl := &sbZoneLayout{Method: "grid-autoconnect", Parts: map[string][]sbPose{}}
	type unitRef struct {
		p    *sbRPart
		unit int
		s    kicad.SceneSymbol
	}
	var items []unitRef
	maxLabel := 0.0
	for _, p := range zi.Parts {
		for _, u := range sbUnits(p) {
			s, err := m.get(p, u)
			if err != nil {
				return nil, nil, err
			}
			items = append(items, unitRef{p, u, s})
		}
		for _, q := range p.LPins {
			if n, ok := d.PinNet[sbPinKey(p.Ref, q.Number)]; ok {
				maxLabel = math.Max(maxLabel, sbTextW(n))
			}
		}
	}
	room := sbSnap((maxLabel + 10) * v.Room) // stub + label on a side with pins
	// per-side room: only sides with pins need space for stubs + labels
	foot := func(s kicad.SceneSymbol, ref string) kicad.Box {
		sides := map[string]float64{}
		for _, q := range s.Pins {
			need := 2.54 // NC pin: just its flag
			if n, ok := d.PinNet[sbPinKey(ref, q.Number)]; ok {
				need = room
				if k := d.NetKind[n]; k == "power" || k == "ground" {
					need = 7.62 // a supply / ground symbol on a short stub
				}
			}
			side := sbOutwardDir(q.Outward)
			sides[side] = math.Max(sides[side], need)
		}
		r := func(side string) float64 {
			if v, ok := sides[side]; ok {
				return v
			}
			return 2.54
		}
		b := s.Box
		return kicad.Box{MinX: b.MinX - r("left"), MinY: b.MinY - r("up") - 2.54, MaxX: b.MaxX + r("right"), MaxY: b.MaxY + r("down") + 2.54}
	}
	core := foot(items[0].s, items[0].p.Ref)
	zl.Parts[items[0].p.Ref] = append(zl.Parts[items[0].p.Ref], sbPose{At: kicad.Pt{}, Unit: items[0].unit})
	placed := []kicad.Box{sbGrow(items[0].s.Box, 2.54)} // the core's body; its labels are re-checked after
	owner := []string{items[0].p.Ref}
	type pinAt struct {
		at  kicad.Pt
		dir string
		ref string
	}
	pins := map[string]pinAt{} // "REF.NUM" of placed units
	addPins := func(it unitRef, pose sbPose) {
		for _, q := range it.s.Pins {
			r := math.Mod(q.Outward+pose.Rot+360, 360)
			pins[sbPinKey(it.p.Ref, q.Number)] = pinAt{sbXform(q.At, pose), sbOutwardDir(r), it.p.Ref}
		}
	}
	addPins(items[0], sbPose{Unit: items[0].unit})
	direct := map[string]bool{} // pins joined by a short wire (no own marker)
	opp := map[string]string{"up": "down", "down": "up", "left": "right", "right": "left"}
	vec := map[string]kicad.Pt{"up": {Y: -1}, "down": {Y: 1}, "left": {X: -1}, "right": {X: 1}}
	segClear := func(a, b kicad.Pt, skip map[string]bool, net string) bool {
		for k, pa := range pins { // never through a foreign pin end (a short)
			if d.PinNet[k] != net && (pa.at == a || pa.at == b || sbOnInterior(pa.at, [2]kicad.Pt{a, b})) {
				return false
			}
		}
		sb := kicad.Box{MinX: math.Min(a.X, b.X) - 0.3, MinY: math.Min(a.Y, b.Y) - 0.3, MaxX: math.Max(a.X, b.X) + 0.3, MaxY: math.Max(a.Y, b.Y) + 0.3}
		for i, pb := range placed {
			if !skip[owner[i]] && sbOverlap(sb, sbGrow(pb, -2.0)) {
				return false
			}
		}
		return true
	}
	// attach: each part faces the placed pin it shares a net with (the
	// near hint first, then signal and supply nets before ground), at a
	// short real wire; decoupling caps sit beside the IC pin they serve
	type attachWire struct {
		ref string
		w   []kicad.Pt
	}
	var attachWires []attachWire
	isDecap := func(p *sbRPart) bool {
		if sbRefPrefix(p.Ref) != "C" || len(p.LPins) != 2 {
			return false
		}
		k := map[string]bool{}
		for _, q := range p.LPins {
			k[d.NetKind[d.PinNet[sbPinKey(p.Ref, q.Number)]]] = true
		}
		return k["power"] && k["ground"]
	}
	attach := func(it unitRef) bool {
		var targets []string
		if it.p.Near != "" {
			if r, pin, ok := sbSplitPin(it.p.Near); ok && d.ByRef[r] != nil {
				if nums, err := d.ByRef[r].resolvePins(pin); err == nil {
					for _, n := range nums {
						targets = append(targets, sbPinKey(r, n))
					}
				}
			}
		}
		for _, pass := range []string{"signal", "power", "ground"} {
			for _, q := range it.s.Pins {
				n, ok := d.PinNet[sbPinKey(it.p.Ref, q.Number)]
				if !ok || d.NetKind[n] != pass {
					continue
				}
				var ks []string
				for k, pa := range pins {
					if d.PinNet[k] == n && !direct[k] && pa.ref != it.p.Ref {
						ks = append(ks, k)
					}
				}
				sort.Slice(ks, func(i, j int) bool {
					ci, cj := pins[ks[i]].ref == items[0].p.Ref, pins[ks[j]].ref == items[0].p.Ref
					if ci != cj {
						return ci
					}
					return ks[i] < ks[j]
				})
				targets = append(targets, ks...)
			}
		}
		for _, tk := range targets {
			t := pins[tk]
			if t.dir == "" {
				continue
			}
			n := d.PinNet[tk]
			for _, q := range it.s.Pins {
				qk := sbPinKey(it.p.Ref, q.Number)
				if d.PinNet[qk] != n {
					continue
				}
				for _, rot := range []float64{0, 90, 180, 270} {
					if sbOutwardDir(math.Mod(q.Outward+rot+360, 360)) != opp[t.dir] {
						continue
					}
					qrel := sbXform(q.At, sbPose{Rot: rot})
					dv := vec[t.dir]
					pv := kicad.Pt{X: math.Abs(dv.Y), Y: math.Abs(dv.X)}
					minGap := 7.62
					switch d.NetKind[n] {
					case "power", "ground":
						minGap = 12.7 // room for the supply tee clear of the neighbour pins' stubs
					default:
						minGap = math.Max(minGap, sbSnap(sbTextW(n)+5.08)) // the name rides on the wire
					}
					for _, gap := range []float64{minGap, minGap + 5.08, minGap + 10.16, minGap + 15.24} {
						for _, off := range []float64{0, 5.08, -5.08, 10.16, -10.16, 15.24, -15.24} {
							qp := kicad.Pt{X: t.at.X + dv.X*gap + pv.X*off, Y: t.at.Y + dv.Y*gap + pv.Y*off}
							pose := sbPose{At: kicad.Pt{X: sbSnap(qp.X - qrel.X), Y: sbSnap(qp.Y - qrel.Y)}, Rot: rot, Unit: it.unit}
							qp = sbXform(q.At, pose)
							if isDecap(it.p) && sbBoxPointDist(sbXformBox(it.s.Box, pose), t.at) > 22.86 {
								continue // decoupling stays within reach of its pin (EG-06)
							}
							fb := sbGrow(sbXformBox(it.s.Box, pose), 2.54)
							// label room on the sides with other pins
							for _, o := range it.s.Pins {
								if o.Number == q.Number {
									continue
								}
								switch sbOutwardDir(math.Mod(o.Outward+rot+360, 360)) {
								case "left":
									fb.MinX -= room
								case "right":
									fb.MaxX += room
								case "up":
									fb.MinY -= room
								case "down":
									fb.MaxY += room
								}
							}
							ok := true
							for _, pb := range placed {
								if sbOverlap(fb, pb) {
									ok = false
									break
								}
							}
							if !ok {
								continue
							}
							// orthogonal: straight when aligned, else out along the pin,
							// across, and in
							var wire []kicad.Pt
							if (dv.X != 0 && math.Abs(qp.Y-t.at.Y) < 1e-6) || (dv.Y != 0 && math.Abs(qp.X-t.at.X) < 1e-6) {
								wire = []kicad.Pt{t.at, qp}
							} else if dv.X != 0 {
								mx := sbSnapHalf(t.at.X + dv.X*gap/2)
								wire = []kicad.Pt{t.at, {X: mx, Y: t.at.Y}, {X: mx, Y: qp.Y}, qp}
							} else {
								my := sbSnapHalf(t.at.Y + dv.Y*gap/2)
								wire = []kicad.Pt{t.at, {X: t.at.X, Y: my}, {X: qp.X, Y: my}, qp}
							}
							if (dv.X != 0 && (qp.X-t.at.X)*dv.X <= 2.54) || (dv.Y != 0 && (qp.Y-t.at.Y)*dv.Y <= 2.54) {
								continue // the part must sit out along the pin
							}
							skip := map[string]bool{t.ref: true}
							clear := true
							// keep the stub room of every other pin free: the new wire
							// crosses no pin ray, the new part's pin rays cross no wire
							var rays [][]kicad.Pt
							for k, pa := range pins {
								if _, hasNet := d.PinNet[k]; hasNet && k != tk && !direct[k] && pa.dir != "" {
									rays = append(rays, []kicad.Pt{pa.at, {X: pa.at.X + vec[pa.dir].X*10.16, Y: pa.at.Y + vec[pa.dir].Y*10.16}})
								}
							}
							for i := 0; i+1 < len(wire) && clear; i++ {
								if sbTouchesWires(wire[i], wire[i+1], rays, kicad.Pt{X: math.NaN()}) {
									clear = false
								}
							}
							body := sbGrow(sbXformBox(it.s.Box, pose), 0.6)
							for k, pa := range pins { // the body stays out of other pins' stub + label room
								nn, hasNet := d.PinNet[k]
								if !clear || !hasNet || k == tk || direct[k] || pa.dir == "" {
									continue
								}
								if sbOverlap(body, sbPinRoom(pa.at, pa.dir, nn, d.NetKind[nn], room)) {
									clear = false
								}
							}
							for _, o := range it.s.Pins {
								if o.Number == q.Number || !clear {
									continue
								}
								od := sbOutwardDir(math.Mod(o.Outward+rot+360, 360))
								op := sbXform(o.At, pose)
								oe := kicad.Pt{X: op.X + vec[od].X*10.16, Y: op.Y + vec[od].Y*10.16}
								if sbTouchesWires(op, oe, append(append([][]kicad.Pt(nil), zl.Wires...), wire), kicad.Pt{X: math.NaN()}) {
									clear = false
								}
							}
							for i := 0; i+1 < len(wire); i++ {
								if !segClear(wire[i], wire[i+1], skip, n) || sbTouchesWires(wire[i], wire[i+1], zl.Wires, t.at) {
									clear = false
								}
							}
							if !clear {
								continue
							}
							// the island's name rides on the wire: a label on it, or a
							// supply / ground symbol on a short tee (no room: next spot)
							a, b := wire[0], wire[1] // a = the target pin
							tm := kicad.Pt{X: sbSnapHalf(b.X - dv.X*2.54), Y: sbSnapHalf(b.Y - dv.Y*2.54)}
							if len(wire) != 2 || tm == a {
								tm = kicad.Pt{X: sbSnapHalf((a.X + b.X) / 2), Y: sbSnapHalf((a.Y + b.Y) / 2)}
							}
							var tee []kicad.Pt
							var mk sbMarker
							switch d.NetKind[n] {
							case "power", "ground":
								dir := "up"
								if d.NetKind[n] == "ground" {
									dir = "down"
								}
								if math.Abs(a.X-b.X) < 1e-6 { // vertical wire: tee sideways
									dir = "right"
								}
								end := kicad.Pt{X: tm.X + vec[dir].X*2.54, Y: tm.Y + vec[dir].Y*2.54}
								if sbTouchesWires(tm, end, zl.Wires, kicad.Pt{X: math.NaN()}) || sbTouchesWires(tm, end, rays, kicad.Pt{X: math.NaN()}) {
									continue
								}
								mb := sbMarkerBox(sbMarker{Kind: d.NetKind[n], Net: n, At: end, Dir: dir})
								hit := false
								for _, pb := range placed {
									hit = hit || sbOverlap(mb, sbGrow(pb, -1.5))
								}
								if hit || sbOverlap(mb, sbXformBox(it.s.Box, pose)) {
									continue
								}
								tee = []kicad.Pt{tm, end}
								mk = sbMarker{Kind: d.NetKind[n], Net: n, At: end, Dir: dir}
							default:
								at := kicad.Pt{X: sbSnapHalf(a.X + dv.X*1.27), Y: sbSnapHalf(a.Y + dv.Y*1.27)}
								mk = sbMarker{Kind: "label", Net: n, At: at, Dir: t.dir, OnWire: true}
							}
							zl.Parts[it.p.Ref] = append(zl.Parts[it.p.Ref], pose)
							zl.Wires = append(zl.Wires, wire)
							attachWires = append(attachWires, attachWire{it.p.Ref, wire})
							direct[qk], direct[tk] = true, true
							if tee != nil {
								zl.Wires = append(zl.Wires, tee)
							}
							zl.Markers = append(zl.Markers, mk)
							placed, owner = append(placed, fb), append(owner, it.p.Ref)
							addPins(it, pose)
							return true
						}
					}
				}
			}
		}
		return false
	}
	// beside the core pin a part serves (decoupling next to its supply
	// pin), power pin up / ground pin down for two-pin parts
	coreRef := items[0].p.Ref
	sidePlace := func(it unitRef) bool {
		rot := sbTwoPinRot(d, it.p, it.s)
		pose0 := sbPose{Rot: rot, Unit: it.unit}
		var anchor *pinAt
		for _, pass := range []string{"power", "signal", "ground"} {
			for _, q := range it.s.Pins {
				n, ok := d.PinNet[sbPinKey(it.p.Ref, q.Number)]
				if !ok || d.NetKind[n] != pass {
					continue
				}
				for k, pa := range pins {
					if pa.ref == coreRef && d.PinNet[k] == n && (anchor == nil || pa.at.Y < anchor.at.Y) {
						a := pa
						anchor = &a
					}
				}
			}
			if anchor != nil {
				break
			}
		}
		if anchor == nil || it.p.Multi || v.NoAttach {
			return false
		}
		side := anchor.dir
		if side == "up" || side == "down" {
			side = "right"
		}
		fb0 := sbXformBox(foot(sbRotScene(it.s, rot), it.p.Ref), sbPose{})
		cb := sbGrow(items[0].s.Box, 0)
		// candidate spots: beside the pin on its side, or above / below the
		// core near the pin; nearest to the pin first
		type cand struct {
			at   kicad.Pt
			dist float64
		}
		var cands []cand
		gap0 := math.Min(room, 12.7)
		if isDecap(it.p) { // supply up / ground down: no label room needed sideways
			gap0 = 5.08
		}
		add := func(at kicad.Pt) {
			at = kicad.Pt{X: sbSnap(at.X), Y: sbSnap(at.Y)}
			body := sbXformBox(sbRotScene(it.s, rot).Box, sbPose{At: at})
			cands = append(cands, cand{at, sbBoxPointDist(body, anchor.at)})
		}
		for dxStep := 0; dxStep < 8; dxStep++ {
			g := gap0 + float64(dxStep)*2.54
			for step := -16; step <= 16; step++ {
				dy := float64(step) * 2.54
				y := anchor.at.Y + dy - (fb0.MinY+fb0.MaxY)/2
				if side == "left" {
					add(kicad.Pt{X: cb.MinX - g - fb0.MaxX, Y: y})
				} else {
					add(kicad.Pt{X: cb.MaxX + g - fb0.MinX, Y: y})
				}
				x := anchor.at.X + dy - (fb0.MinX+fb0.MaxX)/2
				add(kicad.Pt{X: x, Y: cb.MinY - g - fb0.MaxY})
				add(kicad.Pt{X: x, Y: cb.MaxY + g - fb0.MinY})
			}
		}
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].dist < cands[j].dist })
		found := false
		for _, c := range cands {
			if found {
				break
			}
			at := c.at
			f := kicad.Box{MinX: fb0.MinX + at.X, MinY: fb0.MinY + at.Y, MaxX: fb0.MaxX + at.X, MaxY: fb0.MaxY + at.Y}
			ok := true
			for _, pb := range placed {
				if sbOverlap(f, pb) {
					ok = false
					break
				}
			}
			for k, pa := range pins { // keep the stub + marker room of connected pins free
				n, hasNet := d.PinNet[k]
				if !ok || !hasNet || direct[k] || pa.dir == "" {
					continue
				}
				if sbOverlap(f, sbPinRoom(pa.at, pa.dir, n, d.NetKind[n], room)) {
					ok = false
				}
			}
			if ok {
				pose := pose0
				pose.At = at
				zl.Parts[it.p.Ref] = append(zl.Parts[it.p.Ref], pose)
				placed, owner = append(placed, f), append(owner, it.p.Ref)
				addPins(it, pose)
				found = true
				// an upright two-pin part takes its supply / ground symbol
				// straight on the pin (no autoconnect search)
				if len(it.s.Pins) == 2 {
					for _, q := range it.s.Pins {
						k := sbPinKey(it.p.Ref, q.Number)
						kind := d.NetKind[d.PinNet[k]]
						dir := sbOutwardDir(math.Mod(q.Outward+rot+360, 360))
						if !((kind == "power" && dir == "up") || (kind == "ground" && dir == "down")) {
							continue
						}
						a := sbXform(q.At, pose)
						b := kicad.Pt{X: a.X, Y: a.Y + vec[dir].Y*2.54}
						zl.Wires = append(zl.Wires, []kicad.Pt{a, b})
						zl.Markers = append(zl.Markers, sbMarker{Kind: kind, Net: d.PinNet[k], At: b, Dir: dir})
						direct[k] = true
					}
				}
			}
		}
		return found
	}
	// decoupling first, so it gets the room next to the supply pins
	var others, rest, far []unitRef
	for _, it := range items[1:] {
		if !isDecap(it.p) || it.p.Multi {
			others = append(others, it)
			continue
		}
		// a row of upright caps (supply up, ground down) beside the pin, else
		// a short wire to it
		if !v.NoAttach && sidePlace(it) {
			continue
		}
		if !v.NoAttach && !exclude[it.p.Ref] && attach(it) {
			continue
		}
		rest = append(rest, it)
	}
	todo := others
	for progress := true; progress && len(todo) > 0; {
		progress = false
		var next []unitRef
		for _, it := range todo {
			if !v.NoAttach && !exclude[it.p.Ref] && !it.p.Multi && attach(it) {
				progress = true
			} else {
				next = append(next, it)
			}
		}
		todo = next
	}
	for _, it := range append(rest, todo...) {
		if v.NoAttach || it.p.Multi || !sidePlace(it) {
			far = append(far, it)
		}
	}
	colH := math.Max(core.H(), 90)
	maxX := core.MaxX
	for _, pb := range placed {
		maxX = math.Max(maxX, pb.MaxX)
	}
	x, y, colW := maxX+2.54, core.MinY, 0.0
	for _, it := range far {
		rot := sbTwoPinRot(d, it.p, it.s)
		f := foot(sbRotScene(it.s, rot), it.p.Ref)
		if y > core.MinY && y+f.H() > core.MinY+colH {
			x, y, colW = x+colW+2.54, core.MinY, 0
		}
		at := kicad.Pt{X: sbSnap(x - f.MinX), Y: sbSnap(y - f.MinY)}
		zl.Parts[it.p.Ref] = append(zl.Parts[it.p.Ref], sbPose{At: at, Rot: rot, Unit: it.unit})
		y += f.H()
		colW = math.Max(colW, f.W())
	}
	// scratch sheet holding the zone (offset to positive coordinates)
	off := kicad.Pt{X: 254, Y: 254}
	e, err := kicad.OpenSchematic(kicad.NewSchematicText("A0", kicad.NewUUID(), true))
	if err != nil {
		return nil, nil, err
	}
	for _, it := range items {
		if err := e.AddLibSymbol(it.p.LibID, it.p.SymText); err != nil {
			return nil, nil, err
		}
	}
	for _, it := range items {
		var pose sbPose
		for _, ps := range zl.Parts[it.p.Ref] {
			if ps.Unit == it.unit {
				pose = ps
			}
		}
		if _, err := e.PlaceSymbol(kicad.SymbolInstance{LibID: it.p.LibID, Ref: it.p.Ref, Unit: it.unit,
			At: kicad.Pt{X: pose.At.X + off.X, Y: pose.At.Y + off.Y}, Rot: pose.Rot}); err != nil {
			return nil, nil, err
		}
	}
	for _, w := range zl.Wires { // the attach wires are obstacles for the stubs
		var pts []kicad.Pt
		for _, p := range w {
			pts = append(pts, kicad.Pt{X: p.X + off.X, Y: p.Y + off.Y})
		}
		e.AddWire(pts...)
	}
	text, err := e.Render()
	if err != nil {
		return nil, nil, err
	}
	e2, err := kicad.OpenSchematic(text)
	if err != nil {
		return nil, nil, err
	}
	sc, err := e2.Scene()
	if err != nil {
		return nil, nil, err
	}
	sc.TitleBlock = nil
	scene, pinMM := kicadAcScene(sc, nil)
	scene.TitleBlockProvisional = false
	var conns []acConnSpec
	for _, it := range items {
		for _, q := range it.s.Pins {
			k := sbPinKey(it.p.Ref, q.Number)
			n, ok := d.PinNet[k]
			if !ok || direct[k] {
				continue
			}
			kind := "net_label"
			if len(d.NetPages[n]) > 1 {
				kind = "net_port_bi" // drawn as a (larger) global label
			}
			switch d.NetKind[n] {
			case "power":
				kind = "power"
			case "ground":
				kind = "ground"
			}
			conns = append(conns, acConnSpec{PinRef: it.p.Ref + ":" + q.Number, Kind: kind, Net: n})
		}
	}
	rules := defaultAutoconnectRules()
	rules.AvoidTitleBlock = false
	if v.OffsetMin > 0 {
		rules.OffsetMin = v.OffsetMin
	}
	report := newAcReport(scene, rules)
	done := map[string]bool{}
	planAutoconnectBatch(&scene, conns, rules, acRunOpts{}, &report, acConnectHooks{
		connect: func(pin acPin, canonicalKind, net string, sel acCandidate, cr *acConnResult) error {
			key := pin.Designator + "." + pin.PinNumber
			from, ok := pinMM[key]
			if !ok {
				return fmt.Errorf("pin %s not on the scratch sheet", key)
			}
			to := eeToMM(sel.EndPoint.X, sel.EndPoint.Y)
			from = kicad.Pt{X: from.X - off.X, Y: from.Y - off.Y}
			to = kicad.Pt{X: to.X - off.X, Y: to.Y - off.Y}
			zl.Wires = append(zl.Wires, []kicad.Pt{from, to})
			kind := "label"
			switch canonicalKind {
			case "power":
				kind = "power"
			case "ground", "analog_ground", "protective_ground", "protect_ground":
				kind = "ground"
			}
			zl.Markers = append(zl.Markers, sbMarker{Kind: kind, Net: net, At: to, Dir: sel.Direction})
			done[key] = true
			return nil
		},
	})
	// plain outward stubs for anything the planner left
	for _, it := range items {
		var pose sbPose
		for _, ps := range zl.Parts[it.p.Ref] {
			if ps.Unit == it.unit {
				pose = ps
			}
		}
		for _, q := range it.s.Pins {
			k := sbPinKey(it.p.Ref, q.Number)
			at := sbXform(q.At, pose)
			if d.NC[k] {
				zl.NoConnects = append(zl.NoConnects, at)
				continue
			}
			n, ok := d.PinNet[k]
			if !ok || done[k] || direct[k] {
				continue
			}
			dir := sbOutwardDir(q.Outward)
			to := at
			switch dir {
			case "up":
				to.Y -= 5.08
			case "down":
				to.Y += 5.08
			case "left":
				to.X -= 5.08
			default:
				to.X += 5.08
			}
			zl.Wires = append(zl.Wires, []kicad.Pt{at, to})
			kind := "label"
			if d.NetKind[n] == "power" || d.NetKind[n] == "ground" {
				kind = d.NetKind[n]
			}
			zl.Markers = append(zl.Markers, sbMarker{Kind: kind, Net: n, At: to, Dir: dir})
			zl.Notes = append(zl.Notes, fmt.Sprintf("%s: plain stub (autoconnect found no clean spot)", k))
		}
	}
	syms := map[string]kicad.SceneSymbol{}
	var bodies []kicad.Box
	for _, it := range items {
		if it.unit == 1 {
			syms[it.p.Ref] = it.s
		}
		for _, ps := range zl.Parts[it.p.Ref] {
			if ps.Unit == it.unit {
				bodies = append(bodies, sbXformBox(it.s.Box, ps))
				f := sbFields(it.s, ps, it.p.Ref, it.p.Value)
				bodies = append(bodies, f.RefBox, f.VBox)
			}
		}
	}
	sbDeconflict(zl, bodies)
	// no stub may end on or cross a foreign wire or pin (a silent short)
	pinNet := map[kicad.Pt]string{}
	for _, it := range items {
		for _, ps := range zl.Parts[it.p.Ref] {
			if ps.Unit != it.unit {
				continue
			}
			for _, q := range it.s.Pins {
				pinNet[sbXform(q.At, ps)] = d.PinNet[sbPinKey(it.p.Ref, q.Number)] + "#" + it.p.Ref + "." + q.Number
			}
		}
	}
	if bad := sbFixStubs(zl, pinNet, bodies); len(bad) > 0 {
		var refs, desc []string
		for _, b := range bad {
			desc = append(desc, b.Desc)
			for _, aw := range attachWires {
				for i := 0; i+1 < len(aw.w); i++ {
					if sbTouchesWires(aw.w[i], aw.w[i+1], [][]kicad.Pt{{b.A, b.B}}, kicad.Pt{X: math.NaN()}) {
						refs = append(refs, aw.ref)
					}
				}
			}
			if _, pk, ok := strings.Cut(pinNet[b.A], "#"); ok {
				if r, _, _ := strings.Cut(pk, "."); r != "" && r != items[0].p.Ref {
					refs = append(refs, r)
				}
			}
		}
		return nil, refs, fmt.Errorf("%d stub(s) would touch a foreign wire or pin: %s", len(bad), strings.Join(desc, "; "))
	}
	zl.Box = zoneBox(zl, zi, syms, m)
	return zl, nil, nil
}

// sbFixStubs moves every marker stub that touches another wire, another
// pin or another marker to the first clear direction / length.
type sbBadStub struct {
	Desc string
	A, B kicad.Pt
}

func sbFixStubs(zl *sbZoneLayout, pinNet map[kicad.Pt]string, bodies []kicad.Box) (unresolved []sbBadStub) {
	vec := map[string]kicad.Pt{"up": {Y: -1}, "down": {Y: 1}, "left": {X: -1}, "right": {X: 1}}
	for mi := range zl.Markers {
		mk := &zl.Markers[mi]
		if mk.OnWire {
			continue
		}
		wi := -1
		for j, w := range zl.Wires {
			if len(w) == 2 && w[1] == mk.At {
				wi = j
			}
		}
		if wi < 0 {
			continue
		}
		start := zl.Wires[wi][0]
		others := append(append([][]kicad.Pt(nil), zl.Wires[:wi]...), zl.Wires[wi+1:]...)
		bad := func(a, b kicad.Pt) bool {
			if sbTouchesWires(a, b, others, a) {
				return true
			}
			own := pinNet[a]
			for p, n := range pinNet {
				if p != a && n != own && (p == b || sbOnInterior(p, [2]kicad.Pt{a, b})) {
					return true
				}
			}
			for j, o := range zl.Markers {
				if j != mi && (o.At == b || sbOnInterior(o.At, [2]kicad.Pt{a, b})) {
					return true
				}
			}
			mb := sbGrow(sbMarkerBox(sbMarker{Kind: mk.Kind, Net: mk.Net, At: b, Dir: mk.Dir}), -0.3)
			sbx := kicad.Box{MinX: math.Min(a.X, b.X), MinY: math.Min(a.Y, b.Y), MaxX: math.Max(a.X, b.X), MaxY: math.Max(a.Y, b.Y)}
			for _, bb := range bodies {
				if sbOverlap(mb, sbGrow(bb, -0.5)) || sbOverlap(sbGrow(sbx, 0.05), sbGrow(bb, -0.5)) {
					return true
				}
			}
			for _, w := range others { // no wire through the marker or its text
				for i := 0; i+1 < len(w); i++ {
					sb := kicad.Box{MinX: math.Min(w[i].X, w[i+1].X), MinY: math.Min(w[i].Y, w[i+1].Y), MaxX: math.Max(w[i].X, w[i+1].X), MaxY: math.Max(w[i].Y, w[i+1].Y)}
					if sbOverlap(sbGrow(sb, 0.2), mb) && !(w[i] == b || w[i+1] == b) {
						return true
					}
				}
			}
			for j, o := range zl.Markers {
				if j != mi && sbOverlap(mb, sbGrow(sbMarkerBox(o), -0.3)) {
					return true
				}
			}
			return false
		}
		if !bad(start, mk.At) {
			continue
		}
		dirs := []string{mk.Dir}
		for _, dd := range []string{"up", "down", "left", "right"} {
			if dd != mk.Dir {
				dirs = append(dirs, dd)
			}
		}
		for _, dd := range dirs {
			moved := false
			for _, l := range []float64{5.08, 7.62, 10.16, 12.7, 15.24, 2.54} {
				end := kicad.Pt{X: sbRound4(start.X + vec[dd].X*l), Y: sbRound4(start.Y + vec[dd].Y*l)}
				save := mk.Dir
				mk.Dir = dd
				if !bad(start, end) {
					mk.At = end
					zl.Wires[wi][1] = end
					moved = true
					break
				}
				mk.Dir = save
			}
			if moved {
				break
			}
		}
		// a stub still touching a foreign wire, pin or marker would short
		// nets: count it (text-only clashes are left to the quality gate)
		var foreign [][]kicad.Pt // wires of this pin's own island are no short
		for _, w := range others {
			same := false
			for i := 0; i+1 < len(w); i++ {
				same = same || w[i] == start || w[i+1] == start || sbOnInterior(start, [2]kicad.Pt{w[i], w[i+1]})
			}
			if !same {
				foreign = append(foreign, w)
			}
		}
		if sbTouchesWires(start, mk.At, foreign, start) {
			unresolved = append(unresolved, sbBadStub{fmt.Sprintf("%s stub at %v touches a wire", mk.Net, start), start, mk.At})
			continue
		}
		own := pinNet[start]
		for p, n := range pinNet {
			if p != start && n != own && (p == mk.At || sbOnInterior(p, [2]kicad.Pt{start, mk.At})) {
				unresolved = append(unresolved, sbBadStub{fmt.Sprintf("%s stub at %v touches pin %s", mk.Net, start, n), start, mk.At})
				break
			}
		}
	}
	return unresolved
}

// sbDeconflict lengthens marker stubs (2.54 mm steps along the stub) until a
// marker's drawn extent clears the markers before it and the part bodies
// and field texts (obstacles).
func sbDeconflict(zl *sbZoneLayout, obstacles []kicad.Box) {
	var placed []kicad.Box
	for i := range zl.Markers {
		mk := &zl.Markers[i]
		if mk.OnWire {
			placed = append(placed, sbMarkerBox(*mk))
			continue
		}
		wi := -1
		for j, w := range zl.Wires {
			if len(w) == 2 && w[1] == mk.At {
				wi = j
			}
		}
		clear := func(b kicad.Box) bool {
			b = sbGrow(b, -0.2)
			for _, p := range placed {
				if sbOverlap(b, p) {
					return false
				}
			}
			for _, o := range obstacles {
				if sbOverlap(b, o) {
					return false
				}
			}
			return true
		}
		for step := 0; step < 8 && wi >= 0 && !clear(sbMarkerBox(*mk)); step++ {
			d := kicad.Pt{}
			switch mk.Dir {
			case "up":
				d.Y = -sbGrid
			case "down":
				d.Y = sbGrid
			case "left":
				d.X = -sbGrid
			default:
				d.X = sbGrid
			}
			mk.At = kicad.Pt{X: sbRound4(mk.At.X + d.X), Y: sbRound4(mk.At.Y + d.Y)}
			zl.Wires[wi][1] = mk.At
		}
		placed = append(placed, sbMarkerBox(*mk))
	}
}

// sbTouchesWires reports whether the axis-aligned segment a–b shares a
// point with any wire segment, except at allow.
func sbTouchesWires(a, b kicad.Pt, wires [][]kicad.Pt, allow kicad.Pt) bool {
	const e = 1e-6
	sa := kicad.Box{MinX: math.Min(a.X, b.X) - e, MinY: math.Min(a.Y, b.Y) - e, MaxX: math.Max(a.X, b.X) + e, MaxY: math.Max(a.Y, b.Y) + e}
	for _, w := range wires {
		for i := 0; i+1 < len(w); i++ {
			c, d := w[i], w[i+1]
			sw := kicad.Box{MinX: math.Min(c.X, d.X), MinY: math.Min(c.Y, d.Y), MaxX: math.Max(c.X, d.X), MaxY: math.Max(c.Y, d.Y)}
			if sw.MinX > sa.MaxX || sa.MinX > sw.MaxX || sw.MinY > sa.MaxY || sa.MinY > sw.MaxY {
				continue
			}
			// the shared region is the allowed point only
			ix := kicad.Box{MinX: math.Max(sa.MinX, sw.MinX), MinY: math.Max(sa.MinY, sw.MinY), MaxX: math.Min(sa.MaxX, sw.MaxX), MaxY: math.Min(sa.MaxY, sw.MaxY)}
			if math.Abs(ix.MinX-allow.X) < 1e-3 && math.Abs(ix.MaxX-allow.X) < 1e-3 && math.Abs(ix.MinY-allow.Y) < 1e-3 && math.Abs(ix.MaxY-allow.Y) < 1e-3 {
				continue
			}
			return true
		}
	}
	return false
}

// sbPinRoom is what a pin's own connection will occupy: a stub with a
// supply / ground symbol (upright, value text included) or a label.
func sbPinRoom(at kicad.Pt, dir, net, kind string, room float64) kicad.Box {
	vec := map[string]kicad.Pt{"up": {Y: -1}, "down": {Y: 1}, "left": {X: -1}, "right": {X: 1}}
	if kind == "power" || kind == "ground" {
		e := kicad.Pt{X: at.X + vec[dir].X*5.08, Y: at.Y + vec[dir].Y*5.08}
		seg := kicad.Box{MinX: math.Min(at.X, e.X) - 0.6, MinY: math.Min(at.Y, e.Y) - 0.6, MaxX: math.Max(at.X, e.X) + 0.6, MaxY: math.Max(at.Y, e.Y) + 0.6}
		return sbBoxUnion(seg, sbGrow(sbMarkerBox(sbMarker{Kind: kind, Net: net, At: e, Dir: dir}), 0.6))
	}
	l := room + 2.54
	e := kicad.Pt{X: at.X + vec[dir].X*l, Y: at.Y + vec[dir].Y*l}
	return kicad.Box{MinX: math.Min(at.X, e.X) - 1.6, MinY: math.Min(at.Y, e.Y) - 1.6, MaxX: math.Max(at.X, e.X) + 1.6, MaxY: math.Max(at.Y, e.Y) + 1.6}
}

// sbTwoPinRot: a two-pin part with exactly one pin on a supply or ground
// net turns so the supply pin points up / the ground pin down (a supply-
// to-ground part: supply up). Everything else keeps 0°.
func sbTwoPinRot(d *sbDesign, p *sbRPart, s kicad.SceneSymbol) float64 {
	if len(s.Pins) != 2 {
		return 0
	}
	want := map[string]string{} // pin → wanted outward
	for _, q := range s.Pins {
		switch d.NetKind[d.PinNet[sbPinKey(p.Ref, q.Number)]] {
		case "power":
			want[q.Number] = "up"
		case "ground":
			want[q.Number] = "down"
		}
	}
	if len(want) == 0 {
		return 0
	}
	if len(want) == 2 && want[s.Pins[0].Number] == want[s.Pins[1].Number] {
		return 0
	}
	for _, rot := range []float64{0, 90, 180, 270} {
		ok := true
		for _, q := range s.Pins {
			if w, has := want[q.Number]; has && sbOutwardDir(math.Mod(q.Outward+rot+360, 360)) != w {
				ok = false
			}
		}
		if ok {
			return rot
		}
	}
	return 0
}

// sbRotScene is s turned by rot (box and pin outward directions).
func sbRotScene(s kicad.SceneSymbol, rot float64) kicad.SceneSymbol {
	if rot == 0 {
		return s
	}
	r := s
	r.Box = sbXformBox(s.Box, sbPose{Rot: rot})
	r.Pins = nil
	for _, q := range s.Pins {
		q.At = sbXform(q.At, sbPose{Rot: rot})
		q.Outward = math.Mod(q.Outward+rot+360, 360)
		r.Pins = append(r.Pins, q)
	}
	return r
}

// sbGridVariant spaces the grid fallback: Room scales the label room,
// OffsetMin the autoconnect stub start (planner units).
type sbGridVariant struct {
	Room, OffsetMin float64
	NoAttach        bool // plain columns (no part faces its partner pin)
}

var sbGridVariants = []sbGridVariant{{1, 0, false}, {1.5, 0, false}, {1, 30, false}, {1.8, 30, false}, {1.5, 0, true}, {2.4, 40, true}, {1, 0, true}}

func sbOutwardDir(deg float64) string {
	switch int(math.Round(math.Mod(deg+360, 360)/90)) % 4 {
	case 0:
		return "right"
	case 1:
		return "up"
	case 2:
		return "left"
	}
	return "down"
}

// ── all zones ───────────────────────────────────────────────────────────────

type sbLayoutOpts struct {
	NoPlanner bool
	Budget    int
	Jobs      int
	Timeout   time.Duration
}

// layoutZones draws every zone whose hash changed (reuse = the previous
// state's layouts by zone id); returns layouts by zone id.
func layoutZones(d *sbDesign, zis []*sbZoneIn, reuse map[string]*sbZoneLayout, o sbLayoutOpts) (map[string]*sbZoneLayout, []string) {
	m := &sbMeasure{cache: map[string]kicad.SceneSymbol{}}
	out := map[string]*sbZoneLayout{}
	var notes []string
	var mu sync.Mutex
	var wg sync.WaitGroup
	jobs := o.Jobs
	if jobs <= 0 {
		jobs = 4
	}
	sem := make(chan struct{}, jobs)
	var deadline time.Time
	if o.Timeout > 0 {
		deadline = time.Now().Add(o.Timeout)
	}
	for _, zi := range zis {
		hp, hg := zi.hash(d, "planner"), zi.hash(d, "grid-autoconnect")
		if r := reuse[zi.Zone.ID]; r != nil && (r.Hash == hp || r.Hash == hg) {
			out[zi.Zone.ID] = r
			continue
		}
		wg.Add(1)
		go func(zi *sbZoneIn) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t0 := time.Now()
			var cands []*sbZoneLayout
			var perr error
			if !o.NoPlanner {
				var zp *sbZoneLayout
				if zp, perr = planZoneBounded(d, zi, m, o.Budget, deadline); zp != nil {
					zp.Hash = hp
					cands = append(cands, zp)
				}
			}
			// the quality checks of kicad sch-check pick the cleaner drawing:
			// the planner's, else the first clean grid variant
			var zl *sbZoneLayout
			for _, c := range cands {
				c.Findings = sbZoneFindings(d, zi, c)
				zl = c
			}
			var gerr error
			for _, v := range sbGridVariants {
				if zl != nil && len(zl.Findings) == 0 {
					break
				}
				zg, err := gridZone(d, zi, m, v)
				if err != nil {
					gerr = err
					continue
				}
				zg.Hash = hg
				if perr != nil {
					zg.Notes = append(zg.Notes, "planner: "+sbClip(perr.Error(), 900))
				}
				zg.Findings = sbZoneFindings(d, zi, zg)
				if zl == nil || len(zg.Findings) < len(zl.Findings) {
					zl = zg
				}
			}
			if zl == nil {
				mu.Lock()
				notes = append(notes, fmt.Sprintf("zone %s: %v", zi.Zone.ID, gerr))
				mu.Unlock()
				return
			}
			zl.Ms = float64(time.Since(t0).Microseconds()) / 1000
			mu.Lock()
			out[zi.Zone.ID] = zl
			mu.Unlock()
		}(zi)
	}
	wg.Wait()
	sort.Strings(notes)
	return out, notes
}

// planZoneBounded runs the planner when the layout stage is still inside
// its wall-time budget (deadline); later zones go straight to the grid. A
// single run is bounded by its candidate budget, so nothing is abandoned
// mid-run (the engine has no cancellation point): no goroutine outlives the
// build.
func planZoneBounded(d *sbDesign, zi *sbZoneIn, m *sbMeasure, budget int, deadline time.Time) (zl *sbZoneLayout, err error) {
	if !deadline.IsZero() && time.Now().After(deadline) {
		return nil, fmt.Errorf("layout time budget spent before this zone (--planner-timeout)")
	}
	defer func() {
		if r := recover(); r != nil {
			zl, err = nil, fmt.Errorf("planner panic: %v", r)
		}
	}()
	return planZone(d, zi, m, budget)
}

func sbClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── page packing ────────────────────────────────────────────────────────────

const (
	sbZoneMargin = 5.08 // frame margin around a zone's content
	sbZoneTitle  = 5.08 // title band above the frame
	sbZoneGap    = 7.62
	sbPageOrigin = 25.4
)

// sbPackFresh packs a fresh page with the zones apply's packer
// (kicadPackZones: rows in reading order inside the drawing area of the
// smallest paper that holds them, clear of the title block).
func sbPackFresh(ids []string, layouts map[string]*sbZoneLayout) map[string]kicad.Pt {
	var zs []SchematicZoneResult
	for _, id := range ids {
		fp := sbFootprint(layouts[id].Box, kicad.Pt{})
		zs = append(zs, SchematicZoneResult{ID: id, Title: id, Frame: schFrameSpec{ID: id, Rect: boxToEE(fp)}})
	}
	for _, p := range kicad.Papers {
		page := kicad.Box{MinX: 10, MinY: 10, MaxX: p.W - 10, MaxY: p.H - 10}
		title := kicad.Box{MinX: p.W - 10 - 112, MinY: p.H - 10 - 34, MaxX: p.W - 10, MaxY: p.H - 10}
		frames, ok := kicadPackZones(zs, page, title, nil)
		if !ok {
			continue
		}
		out := map[string]kicad.Pt{}
		for _, f := range frames {
			// the frame's zone origin, snapped to the 2.54 mm grid
			out[f.ID] = kicad.Pt{X: sbSnap(f.core.X), Y: sbSnap(f.core.Y)}
		}
		return out
	}
	return nil
}

// sbFootprint is the zone's frame (content box + margin + title band) at
// translation t.
func sbFootprint(box kicad.Box, t kicad.Pt) kicad.Box {
	return kicad.Box{MinX: box.MinX + t.X - sbZoneMargin, MinY: box.MinY + t.Y - sbZoneMargin - sbZoneTitle,
		MaxX: box.MaxX + t.X + sbZoneMargin, MaxY: box.MaxY + t.Y + sbZoneMargin}
}

// packZones assigns each zone of one page a translation: kept ones (keep)
// stay; the rest are packed in rows (fresh build) or put in free space.
func packZones(ids []string, layouts map[string]*sbZoneLayout, keep map[string]kicad.Pt) map[string]kicad.Pt {
	if len(keep) == 0 {
		if out := sbPackFresh(ids, layouts); out != nil {
			return out
		}
	}
	out := map[string]kicad.Pt{}
	var placed []kicad.Box
	for _, id := range ids {
		if t, ok := keep[id]; ok {
			fp := sbFootprint(layouts[id].Box, t)
			clash := false
			for _, b := range placed {
				if sbOverlap(sbGrow(fp, sbZoneGap/2), b) {
					clash = true
				}
			}
			if !clash {
				out[id] = t
				placed = append(placed, fp)
			}
		}
	}
	area, maxW := 0.0, 0.0
	for _, id := range ids {
		b := layouts[id].Box
		w, h := b.W()+2*sbZoneMargin, b.H()+2*sbZoneMargin+sbZoneTitle
		area += (w + sbZoneGap) * (h + sbZoneGap)
		maxW = math.Max(maxW, w)
	}
	rowW := math.Max(maxW, math.Min(math.Sqrt(area*2.2), 1100)) // landscape pages
	fresh := len(placed) == 0
	x, y, rowH := sbPageOrigin, sbPageOrigin, 0.0
	if !fresh { // continue below/right of the kept content
		maxX := 0.0
		for _, b := range placed {
			maxX = math.Max(maxX, b.MaxX)
		}
		x = maxX + sbZoneGap
	}
	for _, id := range ids {
		if _, ok := out[id]; ok {
			continue
		}
		b := layouts[id].Box
		w, h := b.W()+2*sbZoneMargin, b.H()+2*sbZoneMargin+sbZoneTitle
		if fresh && x > sbPageOrigin && x+w > sbPageOrigin+rowW {
			x, y, rowH = sbPageOrigin, y+rowH+sbZoneGap, 0
		}
		for {
			t := kicad.Pt{X: sbSnap(x + sbZoneMargin - b.MinX), Y: sbSnap(y + sbZoneMargin + sbZoneTitle - b.MinY)}
			fp := sbFootprint(b, t)
			clash := false
			for _, q := range placed {
				if sbOverlap(sbGrow(fp, sbZoneGap/2), q) {
					clash = true
					break
				}
			}
			if !clash {
				out[id] = t
				placed = append(placed, fp)
				break
			}
			y += sbZoneGap * 2
		}
		x += w + sbZoneGap
		rowH = math.Max(rowH, h)
	}
	return out
}
