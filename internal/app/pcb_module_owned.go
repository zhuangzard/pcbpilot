package app

import "fmt"

// Explicit ownership is an input fact backed by a prior journal, never inferred
// from a shared net name. Expected retains the exact fresh before-state.
type pcbModuleOwnedObject struct {
	Kind        string         `json:"kind"`
	PrimitiveID string         `json:"primitiveId"`
	Expected    map[string]any `json:"expected"`
	Source      string         `json:"source"`
}

func pcbModuleObjectLists(s *boardSnapshot) map[string]*[]any {
	return map[string]*[]any{"track": &s.Copper.Lines, "arc": &s.Copper.Arcs, "via": &s.Copper.Vias, "region": &s.Copper.Regions, "pour": &s.Copper.Pours}
}

func removePCBModuleOwnedObjects(s *boardSnapshot, owned []pcbModuleOwnedObject) (*boardSnapshot, error) {
	out, err := clonePCBLayoutSnapshot(s)
	if err != nil {
		return nil, err
	}
	if len(owned) == 0 {
		return out, nil
	}
	if out.Copper == nil {
		return nil, fmt.Errorf("owned object replacement requires complete copper inventory")
	}
	lists := pcbModuleObjectLists(out)
	seen := map[string]bool{}
	removedPours := map[string]bool{}
	for _, o := range owned {
		list, ok := lists[o.Kind]
		if !ok || list == nil || *list == nil || o.PrimitiveID == "" || seen[o.PrimitiveID] || o.Source == "" || len(o.Expected) == 0 || asString(o.Expected["primitiveId"]) != o.PrimitiveID {
			return nil, fmt.Errorf("invalid/unknown owned %s object %s or missing source/expected state", o.Kind, o.PrimitiveID)
		}
		category := map[string]string{"track": "routing", "arc": "routing", "via": "vias", "region": "regions", "pour": "pours"}[o.Kind]
		if out.Copper.Availability[category] != "available" {
			return nil, fmt.Errorf("owned %s inventory is unknown", o.Kind)
		}
		if (o.Kind == "region" || o.Kind == "pour") && o.Expected["geometryAvailable"] != true {
			return nil, fmt.Errorf("owned %s geometry is unknown", o.Kind)
		}
		seen[o.PrimitiveID] = true
		kept := make([]any, 0, len(*list))
		matches := 0
		for _, raw := range *list {
			m, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("unknown %s object inventory", o.Kind)
			}
			if asString(m["primitiveId"]) != o.PrimitiveID {
				kept = append(kept, raw)
				continue
			}
			matches++
			if canonicalJSON(m) != canonicalJSON(o.Expected) {
				return nil, fmt.Errorf("owned %s %s changed since declared before-state", o.Kind, o.PrimitiveID)
			}
		}
		if matches != 1 {
			return nil, fmt.Errorf("owned %s %s has %d matches; want exactly one", o.Kind, o.PrimitiveID, matches)
		}
		*list = kept
		if o.Kind == "pour" {
			removedPours[o.PrimitiveID] = true
		}
	}
	if len(removedPours) > 0 {
		if out.Copper.Poured == nil {
			return nil, fmt.Errorf("owned pour replacement requires known materialized inventory")
		}
		kept := make([]any, 0, len(out.Copper.Poured))
		parents := map[string]bool{}
		for _, raw := range out.Copper.Poured {
			m, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("unknown materialized object")
			}
			parent := asString(m["pourPrimitiveId"])
			if parent == "" || parents[parent] {
				return nil, fmt.Errorf("missing/ambiguous materialized pour parent")
			}
			parents[parent] = true
			if !removedPours[parent] {
				kept = append(kept, raw)
			}
		}
		out.Copper.Poured = kept
	}
	out.SemanticSHA256 = ""
	out.ContentSHA256 = ""
	return out, nil
}

func pcbModuleOwnedDeleteSteps(owned []pcbModuleOwnedObject) []playbookStep {
	var routes, regions, pours []string
	for _, o := range owned {
		switch o.Kind {
		case "track", "arc", "via":
			routes = append(routes, o.PrimitiveID)
		case "region":
			regions = append(regions, o.PrimitiveID)
		case "pour":
			pours = append(pours, o.PrimitiveID)
		}
	}
	var out []playbookStep
	add := func(id, action string, ids []string) {
		if len(ids) > 0 {
			out = append(out, playbookStep{ID: id, Name: id, Action: action, Payload: map[string]any{"primitiveIds": ids}})
		}
	}
	if len(routes) > 0 {
		out = append(out, playbookStep{ID: "unlock-owned-protection", Name: "unlock declared previous module copper", Action: "pcb.track.lock", Payload: map[string]any{"primitiveIds": routes, "locked": false}})
	}
	add("delete-owned-protection", "pcb.route.delete", routes)
	add("delete-owned-regions", "pcb.region.delete", regions)
	add("delete-owned-pours", "pcb.pour.delete", pours)
	return out
}

func verifyPCBModuleOwnedRemoved(before, after *boardSnapshot, owned []pcbModuleOwnedObject) (*boardSnapshot, error) {
	filtered, err := removePCBModuleOwnedObjects(before, owned)
	if err != nil {
		return nil, err
	}
	if len(owned) == 0 {
		return filtered, nil
	}
	if after == nil || after.Copper == nil {
		return nil, fmt.Errorf("owned removal needs fresh copper inventory")
	}
	lists := pcbModuleObjectLists(after)
	for _, o := range owned {
		for _, raw := range *lists[o.Kind] {
			m, _ := raw.(map[string]any)
			if asString(m["primitiveId"]) == o.PrimitiveID {
				return nil, fmt.Errorf("replaced %s %s still exists", o.Kind, o.PrimitiveID)
			}
		}
		if o.Kind == "pour" {
			for _, raw := range after.Copper.Poured {
				m, _ := raw.(map[string]any)
				if asString(m["pourPrimitiveId"]) == o.PrimitiveID {
					return nil, fmt.Errorf("replaced pour %s still has materialized copper", o.PrimitiveID)
				}
			}
		}
	}
	return filtered, nil
}
