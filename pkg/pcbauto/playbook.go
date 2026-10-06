package pcbauto

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
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
	// Replace deletes the previous plan's mechanics before writing new ones.
	Replace MechReplace
	Name    string
}

// Step is one `pcbpilot apply` step.
type Step struct {
	ID      string         `json:"id"`
	Name    string         `json:"name,omitempty"`
	Action  string         `json:"action"`
	Payload map[string]any `json:"payload,omitempty"`
	// Capture records result values in the apply journal. Mechanics
	// (holes, keep-outs) capture their primitiveId as MECH_FILL_* /
	// MECH_REGION_* so the next plan can replace exactly them (--replace).
	Capture map[string]string `json:"capture,omitempty"`
}

// Journal capture prefixes of the mechanics a playbook creates.
const (
	MechFillVar   = "MECH_FILL_"
	MechRegionVar = "MECH_REGION_"
)

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
		st := Step{ID: id, Name: name, Action: action, Payload: payload}
		switch action {
		case "pcb.fill.create":
			st.Capture = map[string]string{MechFillVar + varName(id): "$.primitiveId"}
		case "pcb.region.create":
			st.Capture = map[string]string{MechRegionVar + varName(id): "$.primitiveId"}
		}
		pb.Steps = append(pb.Steps, st)
	}
	res := in.Result
	st := res.Stackup
	// 1. Stackup: count first, all inner layers SIGNAL until poured. A
	// placement-only plan on a board that already has this layer count
	// leaves the stackup alone: resetting it would turn the live GND plane
	// back into a signal layer while nothing re-pours it.
	placeOnly := res.Route == nil
	if st != nil && st.Layers >= 2 && !(placeOnly && in.Board.CopperLayers == st.Layers) {
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
	// 2. Mechanics. A re-plan first removes exactly what the previous
	// playbook created (primitiveIds captured in its journal): apply is not
	// idempotent and would otherwise stack a second set of holes/keep-outs.
	if len(in.Replace.Fills) > 0 {
		add("replace-fills", "remove the previous plan's holes", "pcb.fill.delete", map[string]any{"primitiveIds": in.Replace.Fills})
	}
	if len(in.Replace.Regions) > 0 {
		add("replace-regions", "remove the previous plan's keep-outs", "pcb.region.delete", map[string]any{"primitiveIds": in.Replace.Regions})
	}
	if in.OutlineChanged && len(in.Board.Outline) >= 3 {
		add("outline", "board outline", "pcb.outline.set", map[string]any{"points": pts2(in.Board.Outline), "replace": true})
	}
	for i, h := range in.NewHoles {
		add(sprintf("hole-%d", i+1), "mounting hole "+h.Name, "pcb.fill.create", map[string]any{"points": pts2(circle(h.C, h.Dia/2, 24)), "layer": LayerMulti})
		if h.Keep > 0 {
			add(sprintf("hole-keep-%d", i+1), "screw head keep-out", "pcb.region.create", map[string]any{
				"points": pts2(circle(h.C, h.Dia/2+h.Keep, 24)), "layer": LayerMulti, "ruleType": []string{"no-components", "no-wires", "no-pours", "no-inner-electrical"}})
		}
	}
	for i, k := range in.NewKeepouts {
		var rules []string
		// An owned keep-out (an RF module's antenna end) covers its owner:
		// EasyEDA regions cannot exempt a device, so native DRC flagged the
		// module itself as "Device to Prohibited Region". Copper rules cover
		// the whole area; no-components only the strips beside the owner.
		owner := (*Part)(nil)
		if k.Owner != "" {
			owner = in.Board.Part(k.Owner)
		}
		if k.NoParts && owner == nil {
			rules = append(rules, "no-components")
		}
		if k.NoCopper {
			rules = append(rules, "no-wires", "no-pours")
		}
		if len(rules) > 0 {
			add(sprintf("keepout-%d", i+1), k.Name, "pcb.region.create", map[string]any{"points": pts2(k.Poly), "layer": LayerMulti, "ruleType": rules})
		}
		if k.NoParts && owner != nil {
			// The native footprint extends to the silk the body model trimmed
			// (≤ silkMarginMil per side): a strip inside it still overlaps.
			for j, r := range beside(PolyBounds(k.Poly), owner.Body().Expand(silkMarginMil)) {
				add(sprintf("keepout-%d-parts-%d", i+1, j+1), k.Name+" (no parts beside "+k.Owner+")", "pcb.region.create",
					map[string]any{"points": pts2(r.Corners()), "layer": LayerMulti, "ruleType": []string{"no-components"}})
			}
		}
	}
	// 2b. Isolation: milled slots (MULTI-layer fill = board cutout, the same
	// typed primitive as `pcb slot` / mounting holes) and the isolation
	// band as live regions, so EasyEDA pours stay out of it.
	if iso := res.Isolation; iso != nil {
		for i, s := range iso.Slots {
			add(sprintf("iso-slot-%d", i+1), sprintf("isolation slot under %s (%s|%s, %.0f×%.0f mil)", s.Ref, s.A, s.B, s.WidthMil, s.LengthMil),
				"pcb.fill.create", map[string]any{"points": pts2(s.Poly), "layer": LayerMulti})
		}
	}
	if in.Placement != nil && len(in.Placement.Barriers) > 0 {
		for i, k := range in.Placement.Barriers {
			add(sprintf("iso-band-%d", i+1), k.Name, "pcb.region.create", map[string]any{
				"points": pts2(k.Poly), "layer": LayerMulti, "ruleType": []string{"no-wires", "no-pours"}})
		}
	} else if iso := res.Isolation; iso != nil {
		for i, m := range iso.Moats {
			add(sprintf("iso-moat-%d", i+1), "isolation no-pour band", "pcb.region.create", map[string]any{
				"points": pts2(m), "layer": LayerMulti, "ruleType": []string{"no-pours"}})
		}
	}
	// 3. Part poses (only parts that moved).
	if in.Placement != nil {
		pb.Steps = append(pb.Steps, PlacementSteps(in.Placement, in.Original)...)
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
		// 5b. Negative planes ignore the pour polygon once flipped: the host
		// draws them to the Board Outline rule. A no-inner-electrical band
		// along the outline (EPCB region rule 8) pulls them back by the
		// inner edge distance (or a hazardous plane net's domain distance).
		if res.Analysis != nil && len(in.Board.Outline) >= 3 {
			if band := PlaneBandMil(res.Analysis.edgePolicy(in.Board), st); band > 0 {
				for i, poly := range EdgeBand(in.Board.Outline, band) {
					add(sprintf("plane-edge-%d", i+1), sprintf("inner plane pull-back %.1f mil (board-edge safety distance)", band), "pcb.region.create", map[string]any{
						"points": pts2(poly), "layer": LayerMulti, "ruleType": []string{"no-inner-electrical"}})
				}
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

// beside returns the parts of keep-out k not covered by the owner body o,
// as up to four rectangles (left/right full height, then below/above within
// the owner's x-span).
func beside(k, o Rect) []Rect {
	var out []Rect
	add := func(r Rect) {
		if r.W() > 1 && r.H() > 1 {
			out = append(out, r)
		}
	}
	add(Rect{k.MinX, k.MinY, math.Min(k.MaxX, o.MinX), k.MaxY})
	add(Rect{math.Max(k.MinX, o.MaxX), k.MinY, k.MaxX, k.MaxY})
	x0, x1 := math.Max(k.MinX, o.MinX), math.Min(k.MaxX, o.MaxX)
	add(Rect{x0, k.MinY, x1, math.Min(k.MaxY, o.MinY)})
	add(Rect{x0, math.Max(k.MinY, o.MaxY), x1, k.MaxY})
	return out
}

// varName turns a step id into a journal variable suffix.
func varName(id string) string {
	b := []byte(strings.ToUpper(id))
	for i, c := range b {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			b[i] = '_'
		}
	}
	return string(b)
}

// MechReplace lists the previous plan's mechanics to delete first.
type MechReplace struct {
	Fills   []string `json:"fills,omitempty"`
	Regions []string `json:"regions,omitempty"`
}

// MechFromJournal collects the MECH_* primitiveIds an apply journal captured.
func MechFromJournal(raw []byte) MechReplace {
	var out MechReplace
	for _, line := range strings.Split(string(raw), "\n") {
		var e struct {
			Status   string            `json:"status"`
			Captured map[string]string `json:"captured"`
		}
		if json.Unmarshal([]byte(line), &e) != nil || !strings.HasPrefix(e.Status, "ok") {
			continue
		}
		keys := make([]string, 0, len(e.Captured))
		for k := range e.Captured {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch {
			case strings.HasPrefix(k, MechFillVar):
				out.Fills = append(out.Fills, e.Captured[k])
			case strings.HasPrefix(k, MechRegionVar):
				out.Regions = append(out.Regions, e.Captured[k])
			}
		}
	}
	return out
}

// DropReplaced removes from the board model the holes and keep-outs a
// --replace will delete live, so the re-plan does not treat the previous
// plan's mechanics (an owner-less copy of the antenna keep-out, the old
// corner holes) as obstacles.
func DropReplaced(b *Board, rp MechReplace) (holes, keeps int) {
	gone := map[string]bool{}
	for _, id := range append(append([]string(nil), rp.Fills...), rp.Regions...) {
		gone[id] = true
	}
	hs := b.Holes[:0]
	for _, h := range b.Holes {
		if gone[h.Name] {
			holes++
			continue
		}
		hs = append(hs, h)
	}
	b.Holes = hs
	ks := b.Keepouts[:0]
	for _, k := range b.Keepouts {
		if k.ID != "" && gone[k.ID] {
			keeps++
			continue
		}
		ks = append(ks, k)
	}
	b.Keepouts = ks
	return holes, keeps
}

// StaleMechFills returns the primitiveIds of board-level MULTI fills of
// mounting-hole size (b.Holes[:holesBefore], read from the live board) that
// the playbook must delete because the mech spec added its own holes after
// holesBefore. `board copy` duplicates the source board's mounting-hole fills
// with their old ids, at old positions or exactly where the new holes go;
// either way the playbook creates (and journals) the new set, so every old
// hole-sized fill goes — one that coincides with a new hole would otherwise be
// stacked under it (Gas Module V5 B 2026-10-06: two or three fills per corner,
// fastroute pin-pin violations on all layers). Footprint holes (Owner set or
// "ref:id" names), exact slot polygons and fills whose size is not within 25 %
// of a new hole's drill are kept. With no new holes nothing is stale.
func StaleMechFills(b *Board, holesBefore int) []string {
	added := b.Holes[holesBefore:]
	if len(added) == 0 {
		return nil
	}
	var stale []string
	for _, h := range b.Holes[:holesBefore] {
		if h.Owner != "" || h.Name == "" || strings.Contains(h.Name, ":") || h.Poly != nil {
			continue
		}
		for _, n := range added {
			if n.Dia > 0 && math.Abs(h.Dia-n.Dia) <= 0.25*n.Dia {
				stale = append(stale, h.Name)
				break
			}
		}
	}
	return stale
}

// StaleBoardRegions returns the primitiveIds of board-level MULTI-layer rule
// regions (raw `pcb dump` snapshot) that a new outline replaces: any region
// not fully inside the new outline's bounds was drawn for another board
// (`board copy` brings the source board's screw-head rings and inner-plane
// edge bands along), and any region whose bounds match a keep-out the mech
// spec adds (±1 mil) would be stacked under the new one. Gas Module V5 B,
// 2026-10-06: eight such regions from the 150×110 source survived on the
// 100×80 copy; EasyEDA exports MULTI regions as pins, so fastroute saw
// overlapping pins at the corners. Regions fully inside the board that the
// spec does not recreate (user keep-outs) are kept.
func StaleBoardRegions(raw []byte, outline []Point, added []*Keepout) ([]string, error) {
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	if s.Copper == nil || len(outline) < 3 {
		return nil, nil
	}
	board := PolyBounds(outline).Expand(1)
	var stale []string
	for _, r := range s.Copper.Regions {
		id := str(r["primitiveId"])
		if id == "" || int(num(r["layer"])) != LayerMulti {
			continue
		}
		bb, ok := anyBBox(r["bbox"])
		if !ok {
			continue
		}
		inside := bb.MinX >= board.MinX && bb.MinY >= board.MinY && bb.MaxX <= board.MaxX && bb.MaxY <= board.MaxY
		dup := false
		for _, k := range added {
			kb := PolyBounds(k.Poly)
			if math.Abs(kb.MinX-bb.MinX) <= 1 && math.Abs(kb.MinY-bb.MinY) <= 1 && math.Abs(kb.MaxX-bb.MaxX) <= 1 && math.Abs(kb.MaxY-bb.MaxY) <= 1 {
				dup = true
				break
			}
		}
		if !inside || dup {
			stale = append(stale, id)
		}
	}
	return stale, nil
}

// PlacementSteps are the playbook steps that move each placed part from its
// original pose (only parts that moved). Poses are absolute, so a playbook of
// another placement of the same board can swap them in (trial routing of
// several placement candidates).
func PlacementSteps(pl *PlaceResult, original map[string]Placement) []Step {
	var out []Step
	for _, p := range pl.Placements {
		o, ok := original[p.Ref]
		if !ok || p.ID == "" {
			continue
		}
		if math.Abs(o.X-p.X) < 0.01 && math.Abs(o.Y-p.Y) < 0.01 && o.Rot == p.Rot {
			continue
		}
		out = append(out, Step{ID: "place-" + p.Ref, Name: "place " + p.Ref, Action: "pcb.component.modify", Payload: map[string]any{
			"primitiveId": p.ID, "patch": map[string]any{"x": p.X, "y": p.Y, "rotation": p.Rot}}})
	}
	return out
}

// WithPlacement returns a copy of pb whose place-* steps are those of pl.
func WithPlacement(pb *Playbook, pl *PlaceResult, original map[string]Placement, name string) *Playbook {
	cp := *pb
	cp.Meta = map[string]any{}
	for k, v := range pb.Meta {
		cp.Meta[k] = v
	}
	cp.Meta["name"] = name
	cp.Steps = nil
	inserted := false
	for _, st := range pb.Steps {
		if strings.HasPrefix(st.ID, "place-") {
			if !inserted {
				cp.Steps = append(cp.Steps, PlacementSteps(pl, original)...)
				inserted = true
			}
			continue
		}
		cp.Steps = append(cp.Steps, st)
	}
	if !inserted {
		// No part moved in pb: insert before save.
		var steps []Step
		for _, st := range cp.Steps {
			if st.ID == "save" && !inserted {
				steps = append(steps, PlacementSteps(pl, original)...)
				inserted = true
			}
			steps = append(steps, st)
		}
		cp.Steps = steps
	}
	return &cp
}
