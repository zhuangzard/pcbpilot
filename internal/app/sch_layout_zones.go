package app

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
)

// Explicit ownership avoids inventing functional relationships from shared rails.
type SchematicZone struct {
	ID              string                  `json:"id"`
	Title           string                  `json:"title"`
	CoreComponentID string                  `json:"coreComponentId"`
	ComponentIDs    []string                `json:"componentIds"`
	Placement       *SchematicZonePlacement `json:"placement,omitempty"`
	// EmbedIn solves this zone first and places it as one macro part inside
	// the named parent zone (one level; see sch_layout_macro.go).
	EmbedIn string `json:"embedIn,omitempty"`
}
type SchematicZonesInput struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Spacing       *float64                    `json:"spacing,omitempty"`
	Components    []SchematicLayoutComponent  `json:"components"`
	NetPolicies   map[string]string           `json:"netPolicies"`
	Attachments   []SchematicLayoutPeripheral `json:"attachments,omitempty"`
	MaxCandidates int                         `json:"maxCandidates,omitempty"`
	// MaxCandidatesCeiling (isolated-budget mode only): a zone whose search
	// stops on its budget is retried at 4x, up to this ceiling. Solved zones
	// never re-run; structural failures are not retried.
	MaxCandidatesCeiling int                          `json:"maxCandidatesCeiling,omitempty"`
	// SameSheetMarker "net_label": every in-zone direct net is named with a
	// small net label (policy direct_label) - ports stay for module_port nets,
	// which by convention are the cross-sheet ones.
	SameSheetMarker string `json:"sameSheetMarker,omitempty"`
	Zones                []SchematicZone              `json:"zones"`
	Optimization         *SchematicLayoutOptimization `json:"optimization,omitempty"`
	Routing              *SchematicRoutingOptions     `json:"routing,omitempty"`
	MarkerAnchors        []SchematicMarkerAnchor      `json:"markerAnchors,omitempty"`
}
type SchematicZoneVariant struct {
	ID            string                 `json:"id"`
	ContentBounds SchematicBox           `json:"contentBounds"`
	Frame         schFrameSpec           `json:"frame"`
	Layout        *SchematicLayoutResult `json:"layout"`
}
type SchematicZoneResult struct {
	ID                string                  `json:"id"`
	Title             string                  `json:"title"`
	CoreComponentID   string                  `json:"coreComponentId"`
	ContentBounds     SchematicBox            `json:"contentBounds"`
	Frame             schFrameSpec            `json:"frame"`
	Layout            *SchematicLayoutResult  `json:"layout"`
	Placement         *SchematicZonePlacement `json:"placement,omitempty"`
	Variants          []SchematicZoneVariant  `json:"variants,omitempty"`
	SelectedVariantID string                  `json:"selectedVariantId,omitempty"`
}
type SchematicZonesResult struct {
	SchemaVersion  int                   `json:"schemaVersion"`
	Spacing        *float64              `json:"spacing,omitempty"`
	Zones          []SchematicZoneResult `json:"zones"`
	CandidatesUsed int                   `json:"candidatesUsed"`
}

type schematicZoneOwnership struct {
	components map[string]SchematicLayoutComponent
	owners     map[string]string
}

// Shared source gate: review and planning must agree on explicit ownership.
func validateSchematicZoneOwnership(in SchematicZonesInput) (*schematicZoneOwnership, error) {
	if in.SchemaVersion != 1 || len(in.Zones) == 0 || len(in.Components) == 0 {
		return nil, fmt.Errorf("schemaVersion:1, zones and components required")
	}
	if err := validateSchematicSpacing(in.Spacing); err != nil {
		return nil, err
	}
	components := map[string]SchematicLayoutComponent{}
	refs := map[string]bool{}
	for _, c := range in.Components {
		if strings.TrimSpace(c.ID) == "" || components[c.ID].ID != "" || refs[c.Measurement.Designator] {
			return nil, fmt.Errorf("duplicate/empty component or designator %s", c.ID)
		}
		components[c.ID] = c
		refs[c.Measurement.Designator] = true
	}
	owners, zoneIDs := map[string]string{}, map[string]bool{}
	ids, placements := []string{}, []*SchematicZonePlacement{}
	for _, z := range in.Zones {
		ids, placements = append(ids, z.ID), append(placements, z.Placement)
		if strings.TrimSpace(z.ID) == "" || strings.TrimSpace(z.Title) == "" || zoneIDs[z.ID] {
			return nil, fmt.Errorf("duplicate/empty zone ID or title %s", z.ID)
		}
		zoneIDs[z.ID] = true
		for _, id := range z.ComponentIDs {
			if components[id].ID == "" || owners[id] != "" {
				return nil, fmt.Errorf("zone %s: unknown or multiply owned component %s", z.ID, id)
			}
			owners[id] = z.ID
		}
		if owners[z.CoreComponentID] != z.ID {
			return nil, fmt.Errorf("zone %s: core must be a member", z.ID)
		}
	}
	if err := validateSchematicZonePlacements(ids, placements); err != nil {
		return nil, err
	}
	netOwners := map[string]map[string]bool{}
	for _, c := range in.Components {
		if owners[c.ID] == "" {
			return nil, fmt.Errorf("component %s needs explicit zone ownership", c.ID)
		}
		for _, p := range c.Measurement.Pins {
			if p.Net == "" {
				continue
			}
			if netOwners[p.Net] == nil {
				netOwners[p.Net] = map[string]bool{}
			}
			netOwners[p.Net][owners[c.ID]] = true
		}
	}
	for net, zones := range netOwners {
		switch in.NetPolicies[net] {
		case "local_ground", "local_power", "module_port", "net_label":
		case "direct", "direct_label":
			if len(zones) > 1 {
				return nil, fmt.Errorf("cross-zone net %s requires module_port policy", net)
			}
		default:
			return nil, fmt.Errorf("net %s needs explicit policy", net)
		}
	}
	for net := range in.NetPolicies {
		if netOwners[net] == nil {
			return nil, fmt.Errorf("unused policy %s", net)
		}
	}
	for _, h := range in.Attachments {
		if owners[h.ComponentID] == "" || (h.AttachTo != nil && owners[h.AttachTo.ComponentID] != owners[h.ComponentID]) {
			return nil, fmt.Errorf("unknown/cross-zone attachment %s", h.ComponentID)
		}
	}
	return &schematicZoneOwnership{components: components, owners: owners}, nil
}

// PlanSchematicZones computes independent, core-normalized zones, not sheet
// positions or rendered frames. No partial result escapes on any zone failure.
func PlanSchematicZones(in SchematicZonesInput) (*SchematicZonesResult, error) {
	switch in.SameSheetMarker {
	case "", "net_port":
	case "net_label":
		policies := make(map[string]string, len(in.NetPolicies))
		for net, p := range in.NetPolicies {
			if p == "direct" {
				p = "direct_label"
			}
			policies[net] = p
		}
		in.NetPolicies = policies
	default:
		return nil, fmt.Errorf("sameSheetMarker must be net_label or net_port")
	}
	if c := in.MaxCandidatesCeiling; c != 0 && (c < max(in.MaxCandidates, 20000) || c > 4000000) {
		return nil, fmt.Errorf("maxCandidatesCeiling must be maxCandidates..4000000")
	}
	index, err := validateSchematicZoneOwnership(in)
	if err != nil {
		return nil, err
	}
	components, owners := index.components, index.owners
	budget := in.MaxCandidates
	if budget == 0 {
		budget = 20000
	}
	if budget < 1 || budget > 1000000 {
		return nil, fmt.Errorf("maxCandidates must be 1..1000000")
	}
	initial := budget
	out := &SchematicZonesResult{SchemaVersion: 1}
	if in.Spacing != nil {
		spacing := *in.Spacing
		out.Spacing = &spacing
	}
	children := map[string][]SchematicZone{}
	zoneByID := map[string]SchematicZone{}
	for _, z := range in.Zones {
		zoneByID[z.ID] = z
	}
	for _, z := range in.Zones {
		if z.EmbedIn == "" {
			continue
		}
		parent, ok := zoneByID[z.EmbedIn]
		if !ok || parent.EmbedIn != "" || z.EmbedIn == z.ID {
			return nil, fmt.Errorf("zone %s embedIn %q must name a top-level zone", z.ID, z.EmbedIn)
		}
		children[z.EmbedIn] = append(children[z.EmbedIn], z)
	}
	netsOf := func(ids []string) map[string]bool {
		out := map[string]bool{}
		for _, id := range ids {
			for _, p := range components[id].Measurement.Pins {
				if p.Net != "" {
					out[p.Net] = true
				}
			}
		}
		return out
	}
	for _, z := range in.Zones {
		if z.EmbedIn != "" {
			continue // solved as a macro inside its parent
		}
		var macros []*schematicMacro
		for _, child := range children[z.ID] {
			ports := map[string]bool{}
			parentNets := netsOf(z.ComponentIDs)
			for net := range netsOf(child.ComponentIDs) {
				if pol := in.NetPolicies[net]; parentNets[net] && (libDirectPolicy(pol) || libPortPolicy(pol)) {
					ports[net] = true
				}
			}
			if len(ports) == 0 {
				return nil, fmt.Errorf("zone %s embedIn %s shares no signal net with it", child.ID, z.ID)
			}
			sub := SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: child.CoreComponentID, NetPolicies: map[string]string{}, Routing: in.Routing}
			for _, id := range child.ComponentIDs {
				c := components[id]
				sub.Components = append(sub.Components, c)
				for _, p := range c.Measurement.Pins {
					if p.Net != "" {
						sub.NetPolicies[p.Net] = in.NetPolicies[p.Net]
						if ports[p.Net] {
							sub.NetPolicies[p.Net] = "module_port"
						}
					}
				}
			}
			for _, h := range in.Attachments {
				if owners[h.ComponentID] == child.ID {
					sub.Attachments = append(sub.Attachments, h)
				}
			}
			childBudget := initial
			childLayout, err := planSchematicLayoutWithBudget(sub, &childBudget)
			if err != nil {
				return nil, fmt.Errorf("zone %s (embedded in %s): %w", child.ID, z.ID, err)
			}
			out.CandidatesUsed += initial - childBudget
			macro, err := buildSchematicMacro(child, childLayout, ports)
			if err != nil {
				return nil, err
			}
			macros = append(macros, macro)
		}
		local := SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: z.CoreComponentID, NetPolicies: map[string]string{}, Optimization: in.Optimization, Routing: in.Routing}
		for _, macro := range macros {
			local.Components = append(local.Components, macro.part)
			for net := range macro.ports {
				local.NetPolicies[net] = "direct"
			}
		}
		for _, id := range z.ComponentIDs {
			c := components[id]
			local.Components = append(local.Components, c)
			for _, p := range c.Measurement.Pins {
				if p.Net != "" {
					if _, macroNet := local.NetPolicies[p.Net]; !macroNet || local.NetPolicies[p.Net] != "direct" {
						local.NetPolicies[p.Net] = in.NetPolicies[p.Net]
					}
				}
			}
		}
		for _, h := range in.Attachments {
			if owners[h.ComponentID] == z.ID {
				local.Attachments = append(local.Attachments, h)
			}
		}
		for _, anchor := range in.MarkerAnchors {
			if anchor.Type == "pin" && owners[anchor.ComponentID] == z.ID || anchor.Type == "wire_tree" && anchor.ZoneID == z.ID {
				local.MarkerAnchors = append(local.MarkerAnchors, anchor)
			}
		}
		zoneBudget := &budget
		if in.Spacing != nil || in.Optimization != nil {
			// Unified two-level mode is isolated: a harder earlier zone cannot
			// consume a later zone's search/compaction allowance.
			isolatedBudget := initial
			zoneBudget = &isolatedBudget
		}
		before := *zoneBudget
		layout, err := planSchematicLayoutWithBudget(local, zoneBudget)
		lastPrint := ""
		for tier := initial * 4; err != nil && zoneBudget != &budget && tier <= in.MaxCandidatesCeiling && schematicBudgetStop(err); tier *= 4 {
			// No progress: the same conflict stopped the previous tier too.
			print := schematicConflictFingerprint(err)
			if print != "" && print == lastPrint {
				err = fmt.Errorf("%w (escalation stopped: the same conflict %s persisted across budget tiers; revise zoning/geometry, more budget will not help)", err, print)
				break
			}
			lastPrint = print
			out.CandidatesUsed += before - *zoneBudget
			retry := tier
			zoneBudget, before = &retry, tier
			layout, err = planSchematicLayoutWithBudget(local, zoneBudget)
			if err == nil && layout.Search != nil {
				layout.Search.Strategy += fmt.Sprintf(" [escalated budget %d]", tier)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("zone %s (%s): %w", z.ID, z.Title, err)
		}
		for _, macro := range macros {
			if err := macro.expand(layout); err != nil {
				return nil, fmt.Errorf("zone %s (%s): %w", z.ID, z.Title, err)
			}
			layout.Variants = nil // variants were computed with the macro folded
		}
		setSchematicMarkerAnchorZone(layout, z.ID)
		out.CandidatesUsed += before - *zoneBudget
		main, err := measureSchematicZoneVariant(z, "", layout, in.Spacing)
		if err != nil {
			return nil, err
		}
		result := SchematicZoneResult{ID: z.ID, Title: z.Title, CoreComponentID: z.CoreComponentID,
			ContentBounds: main.ContentBounds, Frame: main.Frame, Layout: main.Layout, Placement: copySchematicZonePlacement(z.Placement)}
		for _, alternative := range layout.Variants {
			variant, err := measureSchematicZoneVariant(z, alternative.ID, alternative.Layout, in.Spacing)
			if err != nil {
				return nil, err
			}
			result.Variants = append(result.Variants, variant)
			if reflect.DeepEqual(main.Layout.Placements, variant.Layout.Placements) && reflect.DeepEqual(main.Layout.Wires, variant.Layout.Wires) && reflect.DeepEqual(main.Layout.Flags, variant.Layout.Flags) {
				result.SelectedVariantID = variant.ID
				// Main retains whole-search cost/report; the alternative retains
				// its own cost. Geometry must match, diagnostics need not.
				result.Frame, result.ContentBounds = variant.Frame, variant.ContentBounds
			}
		}
		if len(result.Variants) > 0 && result.SelectedVariantID == "" {
			return nil, fmt.Errorf("zone %s selected layout missing from variants", z.ID)
		}
		out.Zones = append(out.Zones, result)
	}
	return out, nil
}

func measureSchematicZoneVariant(z SchematicZone, id string, layout *SchematicLayoutResult, spacing *float64) (SchematicZoneVariant, error) {
	if layout == nil {
		return SchematicZoneVariant{}, fmt.Errorf("zone %s variant lacks layout", z.ID)
	}
	copyLayout := *layout
	copyLayout.Variants = nil
	layout = &copyLayout
	p := powerLayoutPlan{Placements: layout.Placements, Wires: layout.Wires, Flags: layout.Flags}
	boxes := powerLayoutContentObstacles(&p)
	if len(boxes) == 0 {
		return SchematicZoneVariant{}, fmt.Errorf("zone %s has no content geometry", z.ID)
	}
	b := boxes[0]
	for _, a := range boxes[1:] {
		b.MinX = math.Min(b.MinX, a.MinX)
		b.MinY = math.Min(b.MinY, a.MinY)
		b.MaxX = math.Max(b.MaxX, a.MaxX)
		b.MaxY = math.Max(b.MaxY, a.MaxY)
	}
	frame, err := measureSchModuleFrameObstaclesSpacing(z.ID, z.Title, boxes, nil, nil, spacing)
	if err != nil {
		return SchematicZoneVariant{}, fmt.Errorf("zone %s frame: %w", z.ID, err)
	}
	return SchematicZoneVariant{ID: id, ContentBounds: b, Frame: frame, Layout: layout}, nil
}

// schematicBudgetStop reports a bounded-search stop (worth a larger budget),
// as opposed to a structural refusal (ownership, geometry, attachment).
func schematicBudgetStop(err error) bool {
	if errors.Is(err, errLibLayoutBudget) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "candidate-budget-exhausted") || strings.Contains(msg, "budget exhausted") || strings.Contains(msg, "candidate budget")
}
