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
		// body + value text (the text turns with the symbol: it runs across
		// the stub, centred ~3.6 mm out)
		half, reach := math.Max(1.9, sbTextW(mk.Net)/2+0.6), 5.6
		switch mk.Dir {
		case "up":
			return kicad.Box{MinX: p.X - half, MinY: p.Y - reach, MaxX: p.X + half, MaxY: p.Y}
		case "down":
			return kicad.Box{MinX: p.X - half, MinY: p.Y, MaxX: p.X + half, MaxY: p.Y + reach}
		case "left":
			return kicad.Box{MinX: p.X - reach, MinY: p.Y - half, MaxX: p.X, MaxY: p.Y + half}
		default:
			return kicad.Box{MinX: p.X, MinY: p.Y - half, MaxX: p.X + reach, MaxY: p.Y + half}
		}
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
				return nil, err
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
	foot := func(s kicad.SceneSymbol) kicad.Box {
		sides := map[string]bool{}
		for _, q := range s.Pins {
			sides[sbOutwardDir(q.Outward)] = true
		}
		r := func(side string) float64 {
			if sides[side] {
				return room
			}
			return 2.54
		}
		b := s.Box
		return kicad.Box{MinX: b.MinX - r("left"), MinY: b.MinY - r("up") - 2.54, MaxX: b.MaxX + r("right"), MaxY: b.MaxY + r("down") + 2.54}
	}
	core := foot(items[0].s)
	zl.Parts[items[0].p.Ref] = append(zl.Parts[items[0].p.Ref], sbPose{At: kicad.Pt{}, Unit: items[0].unit})
	colH := math.Max(core.H(), 90)
	x, y, colW := core.MaxX+2.54, core.MinY, 0.0
	for _, it := range items[1:] {
		f := foot(it.s)
		if y > core.MinY && y+f.H() > core.MinY+colH {
			x, y, colW = x+colW+2.54, core.MinY, 0
		}
		at := kicad.Pt{X: sbSnap(x - f.MinX), Y: sbSnap(y - f.MinY)}
		zl.Parts[it.p.Ref] = append(zl.Parts[it.p.Ref], sbPose{At: at, Unit: it.unit})
		y += f.H()
		colW = math.Max(colW, f.W())
	}
	// scratch sheet holding the zone (offset to positive coordinates)
	off := kicad.Pt{X: 254, Y: 254}
	e, err := kicad.OpenSchematic(kicad.NewSchematicText("A0", kicad.NewUUID(), true))
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if err := e.AddLibSymbol(it.p.LibID, it.p.SymText); err != nil {
			return nil, err
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
			At: kicad.Pt{X: pose.At.X + off.X, Y: pose.At.Y + off.Y}}); err != nil {
			return nil, err
		}
	}
	text, err := e.Render()
	if err != nil {
		return nil, err
	}
	e2, err := kicad.OpenSchematic(text)
	if err != nil {
		return nil, err
	}
	sc, err := e2.Scene()
	if err != nil {
		return nil, err
	}
	sc.TitleBlock = nil
	scene, pinMM := kicadAcScene(sc, nil)
	scene.TitleBlockProvisional = false
	var conns []acConnSpec
	for _, it := range items {
		for _, q := range it.s.Pins {
			k := sbPinKey(it.p.Ref, q.Number)
			n, ok := d.PinNet[k]
			if !ok {
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
			if !ok || done[k] {
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
	zl.Box = zoneBox(zl, zi, syms, m)
	return zl, nil
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

// sbGridVariant spaces the grid fallback: Room scales the label room,
// OffsetMin the autoconnect stub start (planner units).
type sbGridVariant struct{ Room, OffsetMin float64 }

var sbGridVariants = []sbGridVariant{{1, 0}, {1.5, 0}, {1, 30}, {1.8, 30}, {2.4, 40}}

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
				if zp, perr = planZoneTimeout(d, zi, m, o.Budget, o.Timeout); zp != nil {
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

// planZoneTimeout bounds one planner run; a run past the timeout is
// abandoned (the zone falls back to the grid).
func planZoneTimeout(d *sbDesign, zi *sbZoneIn, m *sbMeasure, budget int, timeout time.Duration) (*sbZoneLayout, error) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	type res struct {
		zl  *sbZoneLayout
		err error
	}
	ch := make(chan res, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- res{nil, fmt.Errorf("planner panic: %v", r)}
			}
		}()
		zl, err := planZone(d, zi, m, budget)
		ch <- res{zl, err}
	}()
	select {
	case r := <-ch:
		return r.zl, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("planner exceeded %s", timeout)
	}
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
