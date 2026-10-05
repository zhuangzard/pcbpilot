package app

// sch_native_bus.go — native bus primitives in schematic layout plans.
//
// User decision 2026-10-03: the schematic layout draws NATIVE BUSES
// (sch_PrimitiveBus, live-verified on V3 3.2.149 desktop: create → save/reload
// readback → delete). A native bus is drawing only:
//
//   - the extension API has no bus-entry primitive, so every member keeps its
//     ordinary pin stub wire + same-name net label/port (the existing typed
//     connect paths); the bus itself is NEVER connectivity evidence;
//   - the bus never touches a wire, pin, component body, designator or marker
//     (5-unit clearance; stricter than "taps only"): its comb branches stop
//     5 units short of each member label;
//   - orthogonal segments on the 5-unit grid; branches start on the trunk.
//
// The plan carries buses in the official encoding (busName + line as flat
// polylines), so `sch aesthetics --snapshot` and `sch bus apply` read them
// unchanged. Apply: `sch bus apply` (cmd_sch_bus_apply.go), appended by
// `sch compose --playbook` after every member label exists.

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// SchematicNativeBus is one native bus primitive of a layout plan.
type SchematicNativeBus struct {
	// PrimitiveID is set only on host readback, never in a plan.
	PrimitiveID string `json:"primitiveId,omitempty"`
	BusName     string `json:"busName"`
	// Line is the official sch_PrimitiveBus.create encoding: polylines of
	// flat x,y pairs. Line[0] is the trunk; the rest are comb branches that
	// start on the trunk.
	Line    [][]float64 `json:"line"`
	Group   string      `json:"group,omitempty"`
	Kind    string      `json:"kind,omitempty"`
	Members []string    `json:"members,omitempty"`
	// Status: planned (host bus API live-verified) | host-unverified (V4 or
	// an unprobed host line: still created, reported unverified).
	Status string `json:"status,omitempty"`
}

// UnmarshalJSON accepts the host's flat single-polyline line as well as
// nested polylines (sch bus list returns either).
func (b *SchematicNativeBus) UnmarshalJSON(raw []byte) error {
	type plain SchematicNativeBus
	var aux struct {
		plain
		Line json.RawMessage `json:"line"`
	}
	if err := json.Unmarshal(raw, &aux); err != nil {
		return err
	}
	*b = SchematicNativeBus(aux.plain)
	b.Line = nil
	if len(aux.Line) == 0 || string(aux.Line) == "null" {
		return nil
	}
	var flat []float64
	if json.Unmarshal(aux.Line, &flat) == nil {
		b.Line = [][]float64{flat}
		return nil
	}
	var nested [][]float64
	if err := json.Unmarshal(aux.Line, &nested); err != nil {
		return fmt.Errorf("bus line: %w", err)
	}
	b.Line = nested
	return nil
}

func cloneNativeBuses(in []SchematicNativeBus) []SchematicNativeBus {
	if in == nil {
		return nil
	}
	out := make([]SchematicNativeBus, len(in))
	for i, b := range in {
		b.Members = append([]string(nil), b.Members...)
		line := make([][]float64, len(b.Line))
		for k, l := range b.Line {
			line[k] = append([]float64(nil), l...)
		}
		b.Line = line
		out[i] = b
	}
	return out
}

func translateNativeBuses(bs []SchematicNativeBus, dx, dy float64) {
	for i := range bs {
		for k := range bs[i].Line {
			for j := 0; j+1 < len(bs[i].Line[k]); j += 2 {
				bs[i].Line[k][j] += dx
				bs[i].Line[k][j+1] += dy
			}
		}
	}
}

// segments returns every bus segment as point pairs.
func (b SchematicNativeBus) segments() [][2][2]float64 {
	var out [][2][2]float64
	for _, l := range b.Line {
		for j := 2; j+1 < len(l); j += 2 {
			out = append(out, [2][2]float64{{l[j-2], l[j-1]}, {l[j], l[j+1]}})
		}
	}
	return out
}

// schNativeBusNameBox is the reserved box of the rendered bus name: above
// the top end of a vertical trunk (extending away from the labels), right of
// the right end of a horizontal trunk. Font ≈ 7, 6 units per character.
func schNativeBusNameBox(b SchematicNativeBus) layoutBBox {
	if len(b.Line) == 0 || len(b.Line[0]) < 4 {
		return layoutBBox{}
	}
	t := b.Line[0]
	w := 6*float64(len([]rune(b.BusName))) + 4
	if t[0] == t[2] { // vertical trunk
		top := math.Max(t[1], t[3])
		x := t[0]
		// Away from the labels: a branch [T,a,E,a] ends on the label side.
		side := 1.0
		if len(b.Line) > 1 && len(b.Line[1]) >= 4 && b.Line[1][2] > x {
			side = -1
		}
		if side > 0 {
			return layoutBBox{MinX: x - 2, MinY: top + 10, MaxX: x - 2 + w, MaxY: top + 21}
		}
		return layoutBBox{MinX: x + 2 - w, MinY: top + 10, MaxX: x + 2, MaxY: top + 21}
	}
	right := math.Max(t[0], t[2])
	return layoutBBox{MinX: right + 10, MinY: t[1] - 5.5, MaxX: right + 10 + w, MaxY: t[1] + 5.5}
}

// schNativeBusObstacles is the occupancy of one bus (segments ± 0.5 and the
// name box) for frames and previews.
func schNativeBusObstacles(b SchematicNativeBus) []layoutBBox {
	var out []layoutBBox
	for _, s := range b.segments() {
		out = append(out, layoutBBox{MinX: math.Min(s[0][0], s[1][0]) - .5, MinY: math.Min(s[0][1], s[1][1]) - .5, MaxX: math.Max(s[0][0], s[1][0]) + .5, MaxY: math.Max(s[0][1], s[1][1]) + .5})
	}
	if nb := schNativeBusNameBox(b); plBoxValid(nb) {
		out = append(out, nb)
	}
	return out
}

func segBox(a, b [2]float64) layoutBBox {
	return layoutBBox{MinX: math.Min(a[0], b[0]), MinY: math.Min(a[1], b[1]), MaxX: math.Max(a[0], b[0]), MaxY: math.Max(a[1], b[1])}
}

// schNativeBusClearance: every bus segment and the name box keep this much
// clearance from bodies, designators, pins, markers, wires and other buses.
const schNativeBusClearance = 5.0

// validateSchNativeBusShape: name, official line rules (orthogonal, ≥2
// points, polylines touching) and the 5-unit grid.
func validateSchNativeBusShape(b SchematicNativeBus) error {
	name := strings.TrimSpace(b.BusName)
	if name == "" || name != b.BusName || len([]rune(name)) > 64 {
		return fmt.Errorf("bus name %q must be 1…64 characters without surrounding spaces", b.BusName)
	}
	if !busNameConvention.MatchString(name) {
		return fmt.Errorf("bus name %q must be NAME[a:b] (e.g. D[0:7]): the host rejects other names (live V3 3.2.149)", name)
	}
	if b.PrimitiveID != "" {
		return fmt.Errorf("bus %s: a plan must not carry a host primitiveId", name)
	}
	if err := validateBusLine(b.Line); err != nil {
		return fmt.Errorf("bus %s: %w", name, err)
	}
	for _, l := range b.Line {
		for _, v := range l {
			if !plGrid(v) {
				return fmt.Errorf("bus %s: coordinate %g is off the 5-unit grid", name, v)
			}
		}
	}
	// branches start on the trunk (validateBusLine only proves connectivity)
	trunk := b.Line[0]
	for k, l := range b.Line[1:] {
		start := [2]float64{l[0], l[1]}
		on := false
		for j := 2; j+1 < len(trunk); j += 2 {
			if plOnSegment(start, [2]float64{trunk[j-2], trunk[j-1]}, [2]float64{trunk[j], trunk[j+1]}) {
				on = true
			}
		}
		if !on {
			return fmt.Errorf("bus %s: branch #%d does not start on the trunk", name, k+1)
		}
	}
	return nil
}

// schNativeBusKeepout checks one bus against the plan geometry and the
// buses already accepted. The bus touches nothing: no body, designator, pin,
// marker (body + text band), wire, marker lead or other bus within the
// clearance.
func schNativeBusKeepout(p *powerLayoutPlan, b SchematicNativeBus, others []SchematicNativeBus) error {
	gap := schNativeBusClearance
	type obstacle struct {
		box  layoutBBox
		what string
	}
	var obs []obstacle
	for _, c := range p.Placements {
		obs = append(obs, obstacle{c.BBox, c.Designator + " body"})
		for _, t := range libPartLabelBoxes(c) {
			obs = append(obs, obstacle{t, c.Designator + " designator"})
		}
		for _, q := range c.Pins {
			obs = append(obs, obstacle{layoutBBox{MinX: q.X, MinY: q.Y, MaxX: q.X, MaxY: q.Y}, "pin " + c.Designator + "." + q.Number})
		}
	}
	for _, f := range p.Flags {
		for _, box := range schTerminalMarkerBoxes(f) {
			obs = append(obs, obstacle{box, "marker " + f.Net})
		}
		x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
		obs = append(obs, obstacle{segBox([2]float64{f.PinX, f.PinY}, [2]float64{x, y}), "lead " + f.Net})
	}
	for _, w := range p.Wires {
		for j := 1; j < len(w.Points); j++ {
			obs = append(obs, obstacle{segBox(w.Points[j-1], w.Points[j]), "wire " + w.Net})
		}
	}
	for _, o := range others {
		for _, s := range o.segments() {
			obs = append(obs, obstacle{segBox(s[0], s[1]), "bus " + o.BusName})
		}
		if nb := schNativeBusNameBox(o); plBoxValid(nb) {
			obs = append(obs, obstacle{nb, "bus name " + o.BusName})
		}
	}
	var mine []obstacle
	for _, s := range b.segments() {
		mine = append(mine, obstacle{segBox(s[0], s[1]), fmt.Sprintf("segment (%g,%g)→(%g,%g)", s[0][0], s[0][1], s[1][0], s[1][1])})
	}
	if nb := schNativeBusNameBox(b); plBoxValid(nb) {
		mine = append(mine, obstacle{nb, "name"})
	}
	for _, m := range mine {
		for _, o := range obs {
			if boxesGapOverlap(m.box, o.box, gap) {
				return fmt.Errorf("bus %s %s is within %g units of %s", b.BusName, m.what, gap, o.what)
			}
		}
	}
	return nil
}

// validateSchNativeBuses is the composition gate for plan buses: shape,
// keep-outs, distinct names, and every member is a real net of the module
// with its own net label / net port (the bus is never the connection).
func validateSchNativeBuses(p *powerLayoutPlan) error {
	if len(p.Buses) == 0 {
		return nil
	}
	pinNet := map[string]bool{}
	for _, c := range p.Placements {
		for _, q := range c.Pins {
			if q.Net != "" {
				pinNet[q.Net] = true
			}
		}
	}
	labelled := map[string]bool{}
	for _, f := range p.Flags {
		if isNetPortKind(f.Kind) || f.Kind == "net_label" {
			labelled[f.Net] = true
		}
	}
	names := map[string]bool{}
	for i, b := range p.Buses {
		if err := validateSchNativeBusShape(b); err != nil {
			return err
		}
		if names[strings.ToUpper(b.BusName)] {
			return fmt.Errorf("duplicate bus name %s", b.BusName)
		}
		names[strings.ToUpper(b.BusName)] = true
		if len(b.Members) < 2 {
			return fmt.Errorf("bus %s needs ≥2 declared members", b.BusName)
		}
		for _, n := range b.Members {
			if !pinNet[n] {
				return fmt.Errorf("bus %s member %s is on no pin of this module", b.BusName, n)
			}
			if !labelled[n] {
				return fmt.Errorf("bus %s member %s has no net label/port of its own: a bus is never the connection", b.BusName, n)
			}
		}
		if err := schNativeBusKeepout(p, b, p.Buses[:i]); err != nil {
			return err
		}
	}
	return nil
}

// schBusSegmentKeys cuts both paths at every vertex of either side and
// returns undirected unit-free segment keys (0.01 rounding) — the same
// comparison as the connector's sameBusLine: the host reads multiple
// branches back as one out-and-back flat path (V3 3.2.149, 2026-10-01).
func schBusSegmentKeys(lines [][]float64, cut [][2]float64) map[string]bool {
	r := func(v float64) float64 { return math.Round(v*100) / 100 }
	keys := map[string]bool{}
	for _, l := range lines {
		for j := 2; j+1 < len(l); j += 2 {
			x0, y0, x1, y1 := l[j-2], l[j-1], l[j], l[j+1]
			if math.Abs(x0-x1) < 0.01 && math.Abs(y0-y1) < 0.01 {
				continue
			}
			ln := math.Hypot(x1-x0, y1-y0)
			ts := []float64{0, 1}
			for _, q := range cut {
				t := ((q[0]-x0)*(x1-x0) + (q[1]-y0)*(y1-y0)) / (ln * ln)
				if t <= 1e-4 || t >= 1-1e-4 {
					continue
				}
				if math.Hypot(x0+t*(x1-x0)-q[0], y0+t*(y1-y0)-q[1]) < 0.01 {
					ts = append(ts, t)
				}
			}
			sortFloat64s(ts)
			for k := 0; k+1 < len(ts); k++ {
				if ts[k+1]-ts[k] < 1e-6 {
					continue
				}
				ax, ay := r(x0+ts[k]*(x1-x0)), r(y0+ts[k]*(y1-y0))
				bx, by := r(x0+ts[k+1]*(x1-x0)), r(y0+ts[k+1]*(y1-y0))
				if bx < ax || (bx == ax && by < ay) {
					ax, ay, bx, by = bx, by, ax, ay
				}
				keys[fmt.Sprintf("%g,%g-%g,%g", ax, ay, bx, by)] = true
			}
		}
	}
	return keys
}

// schSameBusLine compares two bus paths as undirected segment sets.
func schSameBusLine(a, b [][]float64) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	var cut [][2]float64
	for _, side := range [][][]float64{a, b} {
		for _, l := range side {
			for j := 0; j+1 < len(l); j += 2 {
				cut = append(cut, [2]float64{l[j], l[j+1]})
			}
		}
	}
	ka, kb := schBusSegmentKeys(a, cut), schBusSegmentKeys(b, cut)
	if len(ka) != len(kb) {
		return false
	}
	for k := range ka {
		if !kb[k] {
			return false
		}
	}
	return true
}

func sortFloat64s(v []float64) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
