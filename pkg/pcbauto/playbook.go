package pcbauto

import (
	"math"
	"sort"
)

// PlaybookInput is what the EasyEDA playbook is built from.
type PlaybookInput struct {
	Board     *Board
	Original  map[string]Placement // poses before the run, by designator
	Result    *Result
	Placement *PlaceResult // nil when placement was not run
	Circuit   *Circuit
	// OutlineChanged / HolesFrom mark mechanical edits to write.
	OutlineChanged bool
	NewHoles       []*Hole
	NewKeepouts    []*Keepout
	Name           string
}

// Step is one `pcbpilot apply` step.
type Step struct {
	ID      string         `json:"id"`
	Name    string         `json:"name,omitempty"`
	Action  string         `json:"action"`
	Payload map[string]any `json:"payload,omitempty"`
}

// Playbook is an `pcbpilot apply` document.
type Playbook struct {
	Version int            `json:"version"`
	Meta    map[string]any `json:"meta"`
	Steps   []Step         `json:"steps"`
}

func pts2(ps []Point) [][2]float64 {
	out := make([][2]float64, len(ps))
	for i, p := range ps {
		out[i] = [2]float64{round2(p.X), round2(p.Y)}
	}
	return out
}

func circle(c Point, r float64, n int) []Point {
	out := make([]Point, n)
	for i := range out {
		a := 2 * math.Pi * float64(i) / float64(n)
		out[i] = Point{c.X + r*math.Cos(a), c.Y + r*math.Sin(a)}
	}
	return out
}

// BuildPlaybook turns the plan into typed EasyEDA actions, in the order the
// platform needs: stackup → outline/holes/keepouts → part poses → copper →
// pours → plane flip → rebuild → save → DRC. Plane layers are poured while
// still SIGNAL and flipped afterwards (the verified power-planes recipe).
func BuildPlaybook(in PlaybookInput) *Playbook {
	pb := &Playbook{Version: 1, Meta: map[string]any{"name": in.Name, "description": "pcbauto plan: stackup, placement, routing and planes"}}
	add := func(id, name, action string, payload map[string]any) {
		pb.Steps = append(pb.Steps, Step{ID: id, Name: name, Action: action, Payload: payload})
	}
	res := in.Result
	st := res.Stackup
	// 1. Stackup: count first, all inner layers SIGNAL until poured.
	if st != nil && st.Layers >= 2 {
		var layers []map[string]any
		for _, l := range st.Stack {
			if l.ID >= LayerInner1 {
				layers = append(layers, map[string]any{"id": l.ID, "type": "signal", "name": l.Name})
			}
		}
		payload := map[string]any{"count": st.Layers}
		if len(layers) > 0 {
			payload["layers"] = layers
		}
		add("stackup", "set copper layer count", "pcb.stackup.set", payload)
	}
	// 2. Mechanics.
	if in.OutlineChanged && len(in.Board.Outline) >= 3 {
		add("outline", "board outline", "pcb.outline.set", map[string]any{"points": pts2(in.Board.Outline), "replace": true})
	}
	for i, h := range in.NewHoles {
		add(sprintf("hole-%d", i+1), "mounting hole "+h.Name, "pcb.fill.create", map[string]any{"points": pts2(circle(h.C, h.Dia/2, 24)), "layer": LayerMulti})
		if h.Keep > 0 {
			add(sprintf("hole-keep-%d", i+1), "screw head keep-out", "pcb.region.create", map[string]any{
				"points": pts2(circle(h.C, h.Dia/2+h.Keep, 24)), "layer": LayerMulti, "ruleType": []string{"no-components", "no-wires", "no-pours"}})
		}
	}
	for i, k := range in.NewKeepouts {
		var rules []string
		if k.NoParts {
			rules = append(rules, "no-components")
		}
		if k.NoCopper {
			rules = append(rules, "no-wires", "no-pours")
		}
		add(sprintf("keepout-%d", i+1), k.Name, "pcb.region.create", map[string]any{"points": pts2(k.Poly), "layer": LayerMulti, "ruleType": rules})
	}
	// 3. Part poses (only parts that moved).
	if in.Placement != nil {
		for _, p := range in.Placement.Placements {
			o, ok := in.Original[p.Ref]
			if !ok || p.ID == "" {
				continue
			}
			if math.Abs(o.X-p.X) < 0.01 && math.Abs(o.Y-p.Y) < 0.01 && o.Rot == p.Rot {
				continue
			}
			add("place-"+p.Ref, "place "+p.Ref, "pcb.component.modify", map[string]any{
				"primitiveId": p.ID, "patch": map[string]any{"x": p.X, "y": p.Y, "rotation": p.Rot}})
		}
	}
	// 4. Copper.
	if rr := res.Route; rr != nil {
		for i, t := range rr.Tracks {
			add(sprintf("track-%d", i+1), "", "pcb.line.create", map[string]any{
				"startX": round2(t.A.X), "startY": round2(t.A.Y), "endX": round2(t.B.X), "endY": round2(t.B.Y),
				"layer": t.Layer, "lineWidth": round2(t.Width), "net": t.Net})
		}
		for i, v := range rr.Vias {
			add(sprintf("via-%d", i+1), "", "pcb.via.create", map[string]any{
				"x": round2(v.C.X), "y": round2(v.C.Y), "holeDiameter": round2(v.Drill), "diameter": round2(v.Dia), "net": v.Net})
		}
		// 5. Pours / planes: small islands first (priority 1 pours first).
		planes := append([]PlaneRegion(nil), rr.Planes...)
		sort.SliceStable(planes, func(i, j int) bool { return planes[i].Priority < planes[j].Priority })
		for i, pr := range planes {
			for j, poly := range pr.Polys {
				add(sprintf("pour-%d-%d", i+1, j+1), sprintf("%s on layer %d", pr.Net, pr.Layer), "pcb.pour.create", map[string]any{
					"points": pts2(poly), "net": pr.Net, "layer": pr.Layer, "fill": "solid", "priority": pr.Priority,
					"name": sprintf("auto_%s_L%d", pr.Net, pr.Layer)})
			}
		}
		// 6. Flip solid planes to PLANE after pouring, then rebuild.
		var flip []map[string]any
		for _, l := range st.Stack {
			if l.Kind == KindPlane && len(l.Nets) == 1 {
				flip = append(flip, map[string]any{"id": l.ID, "type": "plane", "name": l.Name})
			}
		}
		if len(flip) > 0 {
			add("planes", "flip solid planes to PLANE", "pcb.stackup.set", map[string]any{"layers": flip})
		}
		add("rebuild", "re-pour all copper", "pcb.pour.rebuild", map[string]any{})
	}
	add("save", "save", "pcb.save", map[string]any{})
	add("drc", "native DRC", "pcb.drc.check", map[string]any{})
	return pb
}
