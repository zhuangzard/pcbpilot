package app

// Planned reservations never become editor objects. These helpers project the
// same candidate geometry for route planning and independent fresh verification.
import (
	"encoding/json"
	"fmt"
	"math"
)

func attachPCBLayoutRouting(candidate *pcbLayoutCandidate, in pcbLayoutPlanInput, mod pcbLayoutModuleSpec, variant pcbLayoutVariant, all map[string]boardComp, baseline *boardSnapshot) error {
	planning, err := removePCBModuleOwnedObjects(baseline, mod.ExistingObjects)
	if err != nil {
		return err
	}
	working, err := projectPCBLayoutPoses(planning, variant.comps)
	if err != nil {
		return err
	}
	var reflow pcbReflowCopperPlan
	if candidate.Reflow != nil {
		reflow = candidate.Reflow.Copper
		if err := removePCBProjectedRoutes(working, reflow.ReplacePrimitiveIDs); err != nil {
			return err
		}
		addPCBProjectedRoutes(working, "reflow", reflow.Routes, reflow.Vias)
	}
	routeBoard, err := clonePCBLayoutSnapshot(working)
	if err != nil {
		return err
	}
	if mod.CrystalGuard != nil {
		if err := removePCBProjectedRoutes(routeBoard, mod.CrystalGuard.ReplacePrimitiveIDs); err != nil {
			return err
		}
	}
	escape, err := planPCBEscape(*in.Routing, routeBoard)
	if err != nil || escape.Status != "pass" {
		return fmt.Errorf("joint escape %s: %v; %v", escape.Status, err, escape.Reasons)
	}
	candidate.Escape = &escape
	candidate.RoutingRequirementsSHA256 = pcbEscapeIntentHash(*in.Routing)
	if mod.Strategy == "crystal-guard" {
		// Keep the declared old OSC set for its replacement identity proof. All
		// OTHER virtual reservations constrain signal/guard/fence generation.
		osc := map[string]bool{}
		for _, p := range mod.CrystalGuard.Ports {
			osc[p.Net] = true
		}
		var virtual []pcbModuleRoute
		for _, r := range escape.Routes {
			if !osc[r.Net] {
				virtual = append(virtual, r)
			}
		}
		addPCBProjectedRoutes(working, "reservation", virtual, escape.Vias)
		members := map[string]boardComp{}
		for _, m := range mod.Members {
			members[m.Ref] = variant.comps[m.Ref]
		}
		projectedAll := working.byDesignator()
		if err := attachCrystalGuardBundle(candidate, mod, pcbLayoutVariant{label: variant.label, comps: members}, projectedAll, working); err != nil {
			return fmt.Errorf("protected module with reserved escape lanes: %w", err)
		}
		if len(candidate.Bundle.Pours) > 0 {
			candidate.Bundle.UnreservedPours = append([]pcbModulePour(nil), candidate.Bundle.Pours...)
			clearance := math.Max(working.Rules.ClearanceMil, working.Rules.ClearanceTrackTrackMil)
			candidate.Bundle.Pours, err = clipPCBLocalPours(candidate.Bundle.Pours, escape.Routes, escape.Vias, clearance)
			if err != nil {
				return err
			}
		}
	} else {
		candidate.Bundle = &pcbLayoutModuleBundle{Kind: "routing-aware-placement"}
		for _, m := range mod.Members {
			candidate.Bundle.OwnedRefs = append(candidate.Bundle.OwnedRefs, m.Ref)
			candidate.Bundle.Envelope = unionLayoutBBox(candidate.Bundle.Envelope, variant.comps[m.Ref].BBox)
		}
	}
	candidate.Bundle.ReplacedObjects = mod.ExistingObjects
	candidate.Bundle.ReflowRoutes = reflow.Routes
	candidate.Bundle.ReflowVias = reflow.Vias
	candidate.Bundle.ReflowReplaceIDs = reflow.ReplacePrimitiveIDs
	// Rebuild executable steps once, after all copper geometry is final.
	buildPCBLayoutRoutingApply(candidate, in, mod, all, baseline)
	final, err := projectPCBLayoutCandidate(baseline, *candidate)
	if err != nil {
		return err
	}
	if err := verifyPCBEscapeGeometry(*in.Routing, escape, final); err != nil {
		return fmt.Errorf("final protection blocks joint escapes: %w", err)
	}
	candidate.Escape.BoardSemanticSHA256, err = moduleCheckSemanticSHA256(final)
	return err
}

func pcbBundleAllRoutes(b *pcbLayoutModuleBundle) []pcbModuleRoute {
	if b == nil {
		return nil
	}
	out := append([]pcbModuleRoute(nil), b.SignalRoutes...)
	out = append(out, b.GroundRoutes...)
	return append(out, b.ReflowRoutes...)
}

func pcbBundleAllVias(b *pcbLayoutModuleBundle) []pcbModuleVia {
	if b == nil {
		return nil
	}
	return append(append([]pcbModuleVia(nil), b.Vias...), b.ReflowVias...)
}

func buildPCBLayoutRoutingApply(c *pcbLayoutCandidate, in pcbLayoutPlanInput, mod pcbLayoutModuleSpec, base map[string]boardComp, snap *boardSnapshot) {
	v := pcbLayoutVariant{label: c.Variant, comps: map[string]boardComp{}}
	set := map[string]bool{}
	for _, p := range c.Placements {
		comp := base[p.Ref]
		comp.X = p.XMil
		comp.Y = p.YMil
		comp.Rotation = p.RotationDeg
		comp.Layer = p.Layer
		comp.BBox = p.BBox
		comp.Pads = p.Pads
		v.comps[p.Ref] = comp
		set[p.Ref] = true
	}
	placements := buildPCBLayoutCandidate(in, mod, v, base, snap, set)
	c.Actions = placements.Actions
	c.Apply = placements.Apply
	if mod.CrystalGuard != nil {
		// The legacy builder knows oscillator topology; reflow routes are
		// temporarily appended only for serialization, not sensitive coverage.
		b := *c.Bundle
		b.GroundRoutes = append(append([]pcbModuleRoute(nil), b.GroundRoutes...), b.ReflowRoutes...)
		b.Vias = pcbBundleAllVias(c.Bundle)
		rebuildCrystalApply(c, mod.CrystalGuard, &b)
	} else {
		c.Actions = c.Actions[:len(c.Actions)-1]
		c.Apply.Steps = c.Apply.Steps[:len(c.Apply.Steps)-1]
		for _, r := range c.Bundle.ReflowRoutes {
			for i := 1; i < len(r.Points); i++ {
				a, z := r.Points[i-1], r.Points[i]
				addPCBLayoutStep(c, fmt.Sprintf("reflow-%s-%d", r.ID, i), "pcb.line.create", map[string]any{"net": r.Net, "layer": r.Layer, "lineWidth": r.WidthMil, "startX": a[0], "startY": a[1], "endX": z[0], "endY": z[1]}, true)
			}
		}
		for _, v := range c.Bundle.ReflowVias {
			addPCBLayoutStep(c, "reflow-"+v.ID, "pcb.via.create", map[string]any{"net": v.Net, "x": v.X, "y": v.Y, "holeDiameter": v.HoleMil, "diameter": v.DiameterMil}, true)
		}
		addPCBLayoutStep(c, "save", "pcb.save", map[string]any{}, false)
	}
	if len(c.Bundle.ReflowReplaceIDs) > 0 {
		step := playbookStep{ID: "delete-reflow-copper", Name: "replace explicitly owned reflow copper", Action: "pcb.route.delete", Payload: map[string]any{"primitiveIds": c.Bundle.ReflowReplaceIDs}}
		c.Apply.Steps = append([]playbookStep{step}, c.Apply.Steps...)
		c.Actions = append([]pcbLayoutTypedAction{{ID: step.ID, Action: step.Action, Payload: step.Payload}}, c.Actions...)
	}
	ownedSteps := pcbModuleOwnedDeleteSteps(c.Bundle.ReplacedObjects)
	var ownedActions []pcbLayoutTypedAction
	for _, step := range ownedSteps {
		ownedActions = append(ownedActions, pcbLayoutTypedAction{ID: step.ID, Action: step.Action, Payload: step.Payload})
	}
	c.Apply.Steps = append(ownedSteps, c.Apply.Steps...)
	c.Actions = append(ownedActions, c.Actions...)
	c.Apply.RequireFullExecution = true
}

func addPCBLayoutStep(c *pcbLayoutCandidate, id, action string, payload map[string]any, capture bool) {
	step := playbookStep{ID: id, Name: id, Action: action, Payload: payload}
	if capture {
		step.Capture = map[string]string{fmt.Sprintf("OBJECT_%d_PID", len(c.Apply.Steps)): "$.primitiveId"}
	}
	c.Actions = append(c.Actions, pcbLayoutTypedAction{ID: id, Action: action, Payload: payload})
	c.Apply.Steps = append(c.Apply.Steps, step)
}

func projectPCBLayoutCandidate(baseline *boardSnapshot, c pcbLayoutCandidate) (*boardSnapshot, error) {
	moved := map[string]boardComp{}
	base := baseline.byDesignator()
	for _, p := range c.Placements {
		comp, ok := base[p.Ref]
		if !ok {
			return nil, fmt.Errorf("unknown placement %s", p.Ref)
		}
		comp.X = p.XMil
		comp.Y = p.YMil
		comp.Rotation = p.RotationDeg
		comp.Layer = p.Layer
		comp.BBox = p.BBox
		comp.Pads = p.Pads
		moved[p.Ref] = comp
	}
	if c.Bundle == nil {
		return nil, fmt.Errorf("missing layout bundle")
	}
	filtered, err := removePCBModuleOwnedObjects(baseline, c.Bundle.ReplacedObjects)
	if err != nil {
		return nil, err
	}
	s, err := projectPCBLayoutPoses(filtered, moved)
	if err != nil {
		return nil, err
	}
	if c.Bundle == nil {
		return nil, fmt.Errorf("missing layout bundle")
	}
	if err := removePCBProjectedRoutes(s, append(append([]string(nil), c.Bundle.ReplacePrimitiveIDs...), c.Bundle.ReflowReplaceIDs...)); err != nil {
		return nil, err
	}
	addPCBProjectedRoutes(s, "candidate", pcbBundleAllRoutes(c.Bundle), pcbBundleAllVias(c.Bundle))
	for _, r := range c.Bundle.Regions {
		s.Copper.Regions = append(s.Copper.Regions, map[string]any{"primitiveId": "planned-region:" + r.ID, "layer": float64(r.Layer), "source": pcbLayoutPolygonSource(r.Points), "geometryAvailable": true, "ruleTypeNames": r.RuleTypes, "ruleType": pcbPlannedRegionRuleNumbers(r.RuleTypes)})
	}
	// Before the host rebuild, the entire editable pour boundary is a
	// conservative obstacle. It never certifies the eventual ground island.
	for _, p := range c.Bundle.Pours {
		s.Copper.Fills = append(s.Copper.Fills, map[string]any{"primitiveId": "planned-pour:" + p.ID, "net": p.Net, "layer": float64(p.Layer), "source": pcbLayoutPolygonSource(p.Points), "geometryAvailable": true, "fill": true})
	}
	return s, nil
}

func pcbLayoutPolygonSource(points [][2]float64) []any {
	var out []any
	for i, p := range points {
		if i > 0 {
			out = append(out, "L")
		}
		out = append(out, p[0], p[1])
	}
	return append(out, "L", points[0][0], points[0][1])
}

func clonePCBLayoutSnapshot(s *boardSnapshot) (*boardSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("nil board snapshot")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	var out boardSnapshot
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func projectPCBLayoutPoses(s *boardSnapshot, moved map[string]boardComp) (*boardSnapshot, error) {
	out, err := clonePCBLayoutSnapshot(s)
	if err != nil {
		return nil, err
	}
	for i, c := range out.Components {
		if p, ok := moved[c.Designator]; ok {
			out.Components[i] = p
		}
	}
	out.SemanticSHA256 = ""
	out.ContentSHA256 = ""
	return out, nil
}

func removePCBProjectedRoutes(s *boardSnapshot, ids []string) error {
	if s.Copper == nil {
		return fmt.Errorf("route replacement requires copper inventory")
	}
	want := map[string]bool{}
	for _, id := range ids {
		if id == "" || want[id] {
			return fmt.Errorf("empty/duplicate replacement id %q", id)
		}
		want[id] = true
	}
	for _, list := range []*[]any{&s.Copper.Lines, &s.Copper.Arcs, &s.Copper.Vias} {
		kept := make([]any, 0, len(*list))
		for _, raw := range *list {
			m, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("unknown routing object")
			}
			id := asString(m["primitiveId"])
			if want[id] {
				delete(want, id)
			} else {
				kept = append(kept, raw)
			}
		}
		*list = kept
	}
	if len(want) > 0 {
		return fmt.Errorf("replacement ids absent from fresh routing inventory: %v", want)
	}
	return nil
}

func addPCBProjectedRoutes(s *boardSnapshot, prefix string, routes []pcbModuleRoute, vias []pcbModuleVia) {
	for _, r := range routes {
		for i := 1; i < len(r.Points); i++ {
			a, z := r.Points[i-1], r.Points[i]
			s.Copper.Lines = append(s.Copper.Lines, map[string]any{"primitiveId": fmt.Sprintf("%s:%s:%d", prefix, r.ID, i), "net": r.Net, "layer": float64(r.Layer), "lineWidth": r.WidthMil, "startX": a[0], "startY": a[1], "endX": z[0], "endY": z[1]})
		}
	}
	for _, v := range vias {
		if v.ExistingPrimitiveID != "" {
			continue
		}
		s.Copper.Vias = append(s.Copper.Vias, map[string]any{"primitiveId": prefix + ":" + v.ID, "net": v.Net, "x": v.X, "y": v.Y, "diameter": v.DiameterMil, "holeDiameter": v.HoleMil})
	}
}

// A conservative rectangle subtraction clips editable local pour boundaries
// away from reservations. It does not predict materialized copper connectivity.
func clipPCBLocalPours(pours []pcbModulePour, routes []pcbModuleRoute, vias []pcbModuleVia, clearance float64) ([]pcbModulePour, error) {
	var result []pcbModulePour
	for _, pour := range pours {
		box, err := crystalRectPourBBox(pour.Points)
		if err != nil {
			return nil, fmt.Errorf("pour %s reservation clipping: %w", pour.ID, err)
		}
		pieces := []layoutBBox{box}
		cut := func(obstacle layoutBBox) {
			var next []layoutBBox
			for _, b := range pieces {
				next = append(next, subtractPCBLayoutRect(b, obstacle)...)
			}
			pieces = next
		}
		for _, r := range routes {
			if r.Layer != pour.Layer || r.Net == pour.Net {
				continue
			}
			for i := 1; i < len(r.Points); i++ {
				cut(expandLayoutBBox(pointsBBox(r.Points[i-1:i+1]), r.WidthMil/2+clearance+.1))
			}
		}
		for _, v := range vias {
			if v.Net == pour.Net {
				continue
			}
			rad := v.DiameterMil/2 + clearance + .1
			cut(layoutBBox{MinX: v.X - rad, MaxX: v.X + rad, MinY: v.Y - rad, MaxY: v.Y + rad})
		}
		if len(pieces) > 256 {
			return nil, fmt.Errorf("pour clipping exceeds 256 pieces for %s", pour.ID)
		}
		for i, b := range pieces {
			p := pour
			p.ID = fmt.Sprintf("%s-reserved-%03d", pour.ID, i+1)
			p.Points = rectPoints(b)
			result = append(result, p)
		}
	}
	return result, nil
}

func subtractPCBLayoutRect(b, c layoutBBox) []layoutBBox {
	x0, x1 := math.Max(b.MinX, c.MinX), math.Min(b.MaxX, c.MaxX)
	y0, y1 := math.Max(b.MinY, c.MinY), math.Min(b.MaxY, c.MaxY)
	if x1 <= x0 || y1 <= y0 {
		return []layoutBBox{b}
	}
	var out []layoutBBox
	for _, p := range []layoutBBox{
		{MinX: b.MinX, MaxX: x0, MinY: b.MinY, MaxY: b.MaxY},
		{MinX: x1, MaxX: b.MaxX, MinY: b.MinY, MaxY: b.MaxY},
		{MinX: x0, MaxX: x1, MinY: b.MinY, MaxY: y0},
		{MinX: x0, MaxX: x1, MinY: y1, MaxY: b.MaxY},
	} {
		if p.MaxX-p.MinX > netPathGeomEps && p.MaxY-p.MinY > netPathGeomEps {
			out = append(out, p)
		}
	}
	return out
}

func pcbPlannedRegionRuleNumbers(names []string) []any {
	values := map[string]int{"no-components": 2, "no-wires": 5, "no-fills": 6, "no-pours": 7, "no-inner-electrical": 8, "follow-rule": 9}
	out := make([]any, 0, len(names))
	for _, name := range names {
		n, ok := values[name]
		if !ok {
			return nil
		}
		out = append(out, float64(n))
	}
	return out
}
