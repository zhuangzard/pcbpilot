package app

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"github.com/zhuangzard/pcbpilot/internal/schguard"
)

type schematicLayoutEditContext struct {
	ProjectUUID  string `json:"projectUuid"`
	ProjectName  string `json:"projectName"`
	DocumentUUID string `json:"documentUuid"`
}

type SchematicLayoutEditSnapshot struct {
	Context schematicLayoutEditContext `json:"context"`
	Result  map[string]any             `json:"result"`
}

type SchematicLayoutEditChange struct {
	ComponentID  string  `json:"componentId"`
	Designator   string  `json:"designator"`
	DX           float64 `json:"dx"`
	DY           float64 `json:"dy"`
	FromRotation float64 `json:"fromRotation"`
	ToRotation   float64 `json:"toRotation"`
}

type SchematicLayoutEditReport struct {
	SchemaVersion       int                         `json:"schemaVersion"`
	Operation           string                      `json:"operation"`
	Status              string                      `json:"status"`
	SourceSHA256        string                      `json:"sourceSha256"`
	PageSHA256          string                      `json:"pageSha256"`
	SnapshotSHA256      string                      `json:"snapshotSha256"`
	SnapshotFingerprint string                      `json:"snapshotFingerprint,omitempty"`
	ZoneID              string                      `json:"zoneId,omitempty"`
	ComponentID         string                      `json:"componentId,omitempty"`
	PinNumber           string                      `json:"pinNumber,omitempty"`
	Strategy            string                      `json:"strategy,omitempty"`
	TargetX             float64                     `json:"targetX,omitempty"`
	TargetY             float64                     `json:"targetY,omitempty"`
	RelativeChanges     []SchematicLayoutEditChange `json:"relativeChanges,omitempty"`
	RejectedCandidates  []string                    `json:"rejectedCandidates,omitempty"`
	Error               string                      `json:"error,omitempty"`
}

func decodeSchematicLayoutEditSnapshot(raw []byte) (SchematicLayoutEditSnapshot, error) {
	var snapshot SchematicLayoutEditSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return snapshot, err
	}
	if snapshot.Context.ProjectUUID == "" || snapshot.Context.DocumentUUID == "" || snapshot.Result == nil {
		return snapshot, fmt.Errorf("response context projectUuid/documentUuid and result are required")
	}
	if available, ok := snapshot.Result["wiresAvailable"].(bool); ok && !available {
		return snapshot, fmt.Errorf("wire inventory unavailable")
	}
	if _, ok := snapshot.Result["components"].([]any); !ok {
		return snapshot, fmt.Errorf("components unavailable")
	}
	if _, ok := snapshot.Result["wires"].([]any); !ok {
		return snapshot, fmt.Errorf("wires unavailable")
	}
	return snapshot, nil
}

func copyLayoutEditPage(page SchematicRenderInput) (SchematicRenderInput, error) {
	raw, err := json.Marshal(page)
	if err != nil {
		return page, err
	}
	var out SchematicRenderInput
	err = json.Unmarshal(raw, &out)
	return out, err
}

func layoutEditSourceMaps(source SchematicZonesInput) (map[string]SchematicLayoutComponent, map[string]SchematicZone, map[string]string, error) {
	components := map[string]SchematicLayoutComponent{}
	zones := map[string]SchematicZone{}
	owners := map[string]string{}
	for _, component := range source.Components {
		if component.ID == "" || components[component.ID].ID != "" {
			return nil, nil, nil, fmt.Errorf("duplicate source component %s", component.ID)
		}
		components[component.ID] = component
	}
	for _, zone := range source.Zones {
		if zone.ID == "" || zones[zone.ID].ID != "" {
			return nil, nil, nil, fmt.Errorf("duplicate source zone %s", zone.ID)
		}
		zones[zone.ID] = zone
		for _, id := range zone.ComponentIDs {
			if components[id].ID == "" || owners[id] != "" {
				return nil, nil, nil, fmt.Errorf("invalid/multiply owned component %s", id)
			}
			owners[id] = zone.ID
		}
	}
	return components, zones, owners, nil
}

func layoutEditStableID(component map[string]any) string {
	props, _ := component["otherProperty"].(map[string]any)
	id, _ := props[connectivity.ComponentIDProperty].(string)
	return id
}

func layoutEditSnapshotComponents(snapshot SchematicLayoutEditSnapshot) ([]map[string]any, error) {
	raw, ok := snapshot.Result["components"].([]any)
	if !ok {
		return nil, fmt.Errorf("components unavailable")
	}
	out := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid component record")
		}
		out = append(out, item)
	}
	return out, nil
}

func layoutEditNumber(m map[string]any, key string) (float64, bool) {
	v, ok := m[key].(float64)
	return v, ok && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func layoutEditString(m map[string]any, key string) (string, bool) {
	v, ok := m[key].(string)
	return v, ok && v != ""
}

func layoutEditLivePart(snapshot SchematicLayoutEditSnapshot, source SchematicLayoutComponent) (map[string]any, error) {
	components, err := layoutEditSnapshotComponents(snapshot)
	if err != nil {
		return nil, err
	}
	var matched []map[string]any
	for _, component := range components {
		if component["componentType"] != "part" {
			continue
		}
		stable := layoutEditStableID(component)
		primitive, _ := component["primitiveId"].(string)
		if stable == source.ID || stable == "" && primitive == source.Measurement.PrimitiveID {
			matched = append(matched, component)
		}
	}
	if len(matched) != 1 {
		return nil, fmt.Errorf("stable component %s matched %d live parts", source.ID, len(matched))
	}
	return matched[0], nil
}

func layoutEditZoneCoreLocal(zone SchematicRenderZone) (*powerLayoutPlacement, error) {
	if zone.Layout == nil {
		return nil, fmt.Errorf("zone %s lacks layout", zone.ID)
	}
	for i := range zone.Layout.Placements {
		placement := &zone.Layout.Placements[i]
		if zone.Layout.ComponentIDs[placement.Designator] == zone.CoreComponentID {
			return placement, nil
		}
	}
	return nil, fmt.Errorf("zone %s lacks core placement %s", zone.ID, zone.CoreComponentID)
}

func layoutEditCorePagePoint(zone SchematicRenderZone) (float64, float64, error) {
	core, err := layoutEditZoneCoreLocal(zone)
	if err != nil {
		return 0, 0, err
	}
	if zone.Frame == nil || zone.SheetPosition == nil {
		return 0, 0, fmt.Errorf("zone %s needs frame and sheetPosition", zone.ID)
	}
	dx := zone.SheetPosition.X - zone.Frame.Rect.MinX
	dy := zone.SheetPosition.Y - zone.Frame.Rect.MaxY
	return core.X + dx, core.Y + dy, nil
}

func layoutEditComponentPageGeometry(zone SchematicRenderZone, componentID, pinNumber string) (float64, float64, float64, float64, error) {
	if zone.Layout == nil || zone.Frame == nil || zone.SheetPosition == nil {
		return 0, 0, 0, 0, fmt.Errorf("zone %s lacks fixed page geometry", zone.ID)
	}
	dx := zone.SheetPosition.X - zone.Frame.Rect.MinX
	dy := zone.SheetPosition.Y - zone.Frame.Rect.MaxY
	for _, placement := range zone.Layout.Placements {
		if zone.Layout.ComponentIDs[placement.Designator] != componentID {
			continue
		}
		for _, pin := range placement.Pins {
			if pin.Number == pinNumber {
				return placement.X + dx, placement.Y + dy, pin.X + dx, pin.Y + dy, nil
			}
		}
		return 0, 0, 0, 0, fmt.Errorf("zone %s component %s lacks pin %s", zone.ID, componentID, pinNumber)
	}
	return 0, 0, 0, 0, fmt.Errorf("zone %s lacks component %s", zone.ID, componentID)
}

func layoutEditSetCorePagePoint(zone *SchematicRenderZone, x, y float64) error {
	core, err := layoutEditZoneCoreLocal(*zone)
	if err != nil {
		return err
	}
	if zone.Frame == nil {
		return fmt.Errorf("zone %s lacks frame", zone.ID)
	}
	dx, dy := x-core.X, y-core.Y
	zone.SheetPosition = &SchematicSheetPosition{X: zone.Frame.Rect.MinX + dx, Y: zone.Frame.Rect.MaxY + dy}
	zone.Variants = nil
	return nil
}

func validateLayoutEditPageSource(source SchematicZonesInput, page SchematicRenderInput) error {
	_, zones, _, err := layoutEditSourceMaps(source)
	if err != nil {
		return err
	}
	if len(zones) != len(page.Zones) {
		return fmt.Errorf("page/source zone count differs")
	}
	seen := map[string]bool{}
	for _, pageZone := range page.Zones {
		sourceZone, ok := zones[pageZone.ID]
		if !ok || seen[pageZone.ID] || sourceZone.CoreComponentID != pageZone.CoreComponentID {
			return fmt.Errorf("page/source zone identity differs for %s", pageZone.ID)
		}
		seen[pageZone.ID] = true
		if pageZone.Layout == nil {
			return fmt.Errorf("zone %s lacks layout", pageZone.ID)
		}
		for _, id := range sourceZone.ComponentIDs {
			found := false
			for _, actual := range pageZone.Layout.ComponentIDs {
				if actual == id {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("zone %s layout lacks component %s", pageZone.ID, id)
			}
		}
	}
	return nil
}

func layoutEditLocalInput(source SchematicZonesInput, zone SchematicZone) SchematicLayoutInput {
	wanted := map[string]bool{}
	for _, id := range zone.ComponentIDs {
		wanted[id] = true
	}
	local := SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: zone.CoreComponentID, NetPolicies: map[string]string{}, MaxCandidates: source.MaxCandidates, Optimization: source.Optimization, Routing: source.Routing}
	for _, component := range source.Components {
		if !wanted[component.ID] {
			continue
		}
		local.Components = append(local.Components, component)
		for _, pin := range component.Measurement.Pins {
			if pin.Net != "" {
				local.NetPolicies[pin.Net] = source.NetPolicies[pin.Net]
			}
		}
	}
	for _, attachment := range source.Attachments {
		if wanted[attachment.ComponentID] {
			local.Attachments = append(local.Attachments, attachment)
		}
	}
	for _, anchor := range source.MarkerAnchors {
		if (anchor.Type == "pin" && wanted[anchor.ComponentID]) || (anchor.Type == "wire_tree" && anchor.ZoneID == zone.ID) {
			local.MarkerAnchors = append(local.MarkerAnchors, anchor)
		}
	}
	return local
}

func layoutEditRelativeChanges(before, after *SchematicLayoutResult) []SchematicLayoutEditChange {
	if before == nil || after == nil {
		return nil
	}
	old := map[string]powerLayoutPlacement{}
	for _, placement := range before.Placements {
		old[before.ComponentIDs[placement.Designator]] = placement
	}
	var changes []SchematicLayoutEditChange
	for _, placement := range after.Placements {
		id := after.ComponentIDs[placement.Designator]
		previous, ok := old[id]
		if !ok {
			continue
		}
		if previous.X != placement.X || previous.Y != placement.Y || previous.Rotation != placement.Rotation {
			changes = append(changes, SchematicLayoutEditChange{ComponentID: id, Designator: placement.Designator, DX: placement.X - previous.X, DY: placement.Y - previous.Y, FromRotation: previous.Rotation, ToRotation: placement.Rotation})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ComponentID < changes[j].ComponentID })
	return changes
}

func layoutEditPageValid(page SchematicRenderInput) error {
	if err := validateSchematicSheet(page); err != nil {
		return err
	}
	if err := validateCompleteLayoutPreview(page); err != nil {
		return err
	}
	_, err := RenderSchematicLayoutSVG(page)
	return err
}

func planSchematicCoreMove(source SchematicZonesInput, page SchematicRenderInput, snapshot SchematicLayoutEditSnapshot, coreID string, targetX, targetY float64, report *SchematicLayoutEditReport) (*SchematicRenderInput, error) {
	if err := validateLayoutEditPageSource(source, page); err != nil {
		return nil, err
	}
	components, zones, owners, err := layoutEditSourceMaps(source)
	if err != nil {
		return nil, err
	}
	targetZoneID := owners[coreID]
	targetZone := zones[targetZoneID]
	if targetZoneID == "" || targetZone.CoreComponentID != coreID {
		return nil, fmt.Errorf("%s is not an owned zone core", coreID)
	}
	fingerprint, err := schguard.SceneFingerprint(snapshot.Result)
	if err != nil {
		return nil, err
	}
	report.SnapshotFingerprint, report.ZoneID, report.ComponentID, report.TargetX, report.TargetY = fingerprint, targetZoneID, coreID, targetX, targetY

	if err := rejectLayoutEditCrossZoneTrees(snapshot, owners, components, targetZoneID); err != nil {
		return nil, err
	}
	for _, zone := range page.Zones {
		sourceCore := components[zone.CoreComponentID]
		live, err := layoutEditLivePart(snapshot, sourceCore)
		if err != nil {
			return nil, err
		}
		liveX, xok := layoutEditNumber(live, "x")
		liveY, yok := layoutEditNumber(live, "y")
		pageX, pageY, pointErr := layoutEditCorePagePoint(zone)
		if !xok || !yok || pointErr != nil || liveX != pageX || liveY != pageY {
			return nil, fmt.Errorf("source-drift: zone %s core page coordinate differs from fresh snapshot", zone.ID)
		}
	}

	base, err := copyLayoutEditPage(page)
	if err != nil {
		return nil, err
	}
	base.Sheet.Flow = "fixed"
	targetIndex := -1
	for i := range base.Zones {
		if base.Zones[i].ID == targetZoneID {
			targetIndex = i
		}
	}
	if targetIndex < 0 {
		return nil, fmt.Errorf("target zone missing from page")
	}
	original := base.Zones[targetIndex]
	if err := layoutEditSetCorePagePoint(&base.Zones[targetIndex], targetX, targetY); err != nil {
		return nil, err
	}
	if err := layoutEditPageValid(base); err == nil {
		report.Strategy = "rigid-translation"
		report.RelativeChanges = []SchematicLayoutEditChange{}
		return &base, nil
	} else {
		report.RejectedCandidates = append(report.RejectedCandidates, "rigid-translation: "+err.Error())
	}

	local := layoutEditLocalInput(source, targetZone)
	replanned, err := PlanSchematicLayout(local)
	if err != nil {
		return nil, fmt.Errorf("fixed-core zone replan: %w", err)
	}
	setSchematicMarkerAnchorZone(replanned, targetZoneID)
	type candidate struct {
		id     string
		layout *SchematicLayoutResult
	}
	candidates := []candidate{{"replanned-baseline", replanned}}
	for _, variant := range replanned.Variants {
		candidates = append(candidates, candidate{"replanned-" + variant.ID, variant.Layout})
	}
	for _, candidate := range candidates {
		measured, err := measureSchematicZoneVariant(targetZone, candidate.id, candidate.layout, source.Spacing)
		if err != nil {
			report.RejectedCandidates = append(report.RejectedCandidates, candidate.id+": "+err.Error())
			continue
		}
		trial, _ := copyLayoutEditPage(page)
		trial.Sheet.Flow = "fixed"
		zone := &trial.Zones[targetIndex]
		zone.Layout, zone.ContentBounds, zone.Frame = measured.Layout, &measured.ContentBounds, &measured.Frame
		zone.SelectedVariantID, zone.Variants = candidate.id, nil
		if err := layoutEditSetCorePagePoint(zone, targetX, targetY); err != nil {
			report.RejectedCandidates = append(report.RejectedCandidates, candidate.id+": "+err.Error())
			continue
		}
		if err := layoutEditPageValid(trial); err != nil {
			report.RejectedCandidates = append(report.RejectedCandidates, candidate.id+": "+err.Error())
			continue
		}
		report.Strategy = candidate.id
		report.RelativeChanges = layoutEditRelativeChanges(original.Layout, candidate.layout)
		return &trial, nil
	}
	return nil, fmt.Errorf("fixed core target has no legal zone shape within the declared search budgets")
}

type layoutEditSegment struct {
	ID   string
	A, B [2]float64
}

func layoutEditWireSegments(snapshot SchematicLayoutEditSnapshot) ([]layoutEditSegment, error) {
	raw, _ := snapshot.Result["wires"].([]any)
	var out []layoutEditSegment
	for _, value := range raw {
		wire, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid wire")
		}
		id, _ := wire["primitiveId"].(string)
		if id == "" {
			return nil, fmt.Errorf("wire id unavailable")
		}
		x0, a := layoutEditNumber(wire, "x0")
		y0, b := layoutEditNumber(wire, "y0")
		x1, c := layoutEditNumber(wire, "x1")
		y1, d := layoutEditNumber(wire, "y1")
		if !a || !b || !c || !d {
			return nil, fmt.Errorf("wire %s coordinates unavailable", id)
		}
		out = append(out, layoutEditSegment{ID: id, A: [2]float64{x0, y0}, B: [2]float64{x1, y1}})
	}
	return out, nil
}

func rejectLayoutEditCrossZoneTrees(snapshot SchematicLayoutEditSnapshot, owners map[string]string, source map[string]SchematicLayoutComponent, targetZone string) error {
	segments, err := layoutEditWireSegments(snapshot)
	if err != nil {
		return err
	}
	parent := make([]int, len(segments))
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := range segments {
		for j := range segments[:i] {
			if segments[i].ID == segments[j].ID || plSegmentsContact(segments[i].A, segments[i].B, segments[j].A, segments[j].B) {
				parent[root(i)] = root(j)
			}
		}
	}
	rootZones := map[int]map[string]bool{}
	for id, component := range source {
		live, err := layoutEditLivePart(snapshot, component)
		if err != nil {
			return err
		}
		pins, _ := live["pins"].([]any)
		for _, value := range pins {
			pin, _ := value.(map[string]any)
			x, xok := layoutEditNumber(pin, "x")
			y, yok := layoutEditNumber(pin, "y")
			if !xok || !yok {
				return fmt.Errorf("%s live pin geometry unavailable", id)
			}
			for i, segment := range segments {
				if plOnSegment([2]float64{x, y}, segment.A, segment.B) {
					r := root(i)
					if rootZones[r] == nil {
						rootZones[r] = map[string]bool{}
					}
					rootZones[r][owners[id]] = true
				}
			}
		}
	}
	for _, zones := range rootZones {
		if zones[targetZone] && len(zones) > 1 {
			return fmt.Errorf("cross-zone physical wire tree touches target zone; boundary branch ownership is ambiguous")
		}
	}
	return nil
}

type SchematicPinMarkerRepairTarget struct {
	SchemaVersion int                              `json:"schemaVersion"`
	Operation     string                           `json:"operation"`
	Expected      schematicPinMarkerRepairExpected `json:"expected"`
	Replacement   schematicPinMarkerReplacement    `json:"replacement"`
}

type schematicPinMarkerRepairExpected struct {
	ProjectUUID          string  `json:"projectUuid"`
	DocumentUUID         string  `json:"documentUuid"`
	SceneFingerprint     string  `json:"sceneFingerprint"`
	ComponentID          string  `json:"componentId"`
	ComponentPrimitiveID string  `json:"componentPrimitiveId"`
	Designator           string  `json:"designator"`
	PinNumber            string  `json:"pinNumber"`
	PinX                 float64 `json:"pinX"`
	PinY                 float64 `json:"pinY"`
	PinRotation          float64 `json:"pinRotation"`
	Net                  string  `json:"net"`
	WirePrimitiveID      string  `json:"wirePrimitiveId"`
	WireX0               float64 `json:"wireX0"`
	WireY0               float64 `json:"wireY0"`
	WireX1               float64 `json:"wireX1"`
	WireY1               float64 `json:"wireY1"`
	MarkerPrimitiveID    string  `json:"markerPrimitiveId"`
	MarkerType           string  `json:"markerType"`
	MarkerX              float64 `json:"markerX"`
	MarkerY              float64 `json:"markerY"`
	MarkerRotation       float64 `json:"markerRotation"`
}

type schematicPinMarkerReplacement struct {
	Kind      string  `json:"kind"`
	Net       string  `json:"net"`
	Direction string  `json:"direction"`
	Offset    float64 `json:"offset"`
	Rotation  float64 `json:"rotation"`
}

func layoutEditPin(component map[string]any, number string) (map[string]any, error) {
	pins, _ := component["pins"].([]any)
	var matched []map[string]any
	for _, value := range pins {
		pin, _ := value.(map[string]any)
		if pin["pinNumber"] == number {
			matched = append(matched, pin)
		}
	}
	if len(matched) != 1 {
		return nil, fmt.Errorf("pin %s matched %d live pins", number, len(matched))
	}
	return matched[0], nil
}

func layoutEditSourcePin(component SchematicLayoutComponent, number string) (SchematicPin, error) {
	var matched []SchematicPin
	for _, pin := range component.Measurement.Pins {
		if pin.Number == number {
			matched = append(matched, pin)
		}
	}
	if len(matched) != 1 {
		return SchematicPin{}, fmt.Errorf("source pin %s.%s matched %d records", component.ID, number, len(matched))
	}
	return matched[0], nil
}

func layoutEditOutwardDirection(rotation float64) (string, error) {
	switch math.Mod(math.Mod(rotation, 360)+360, 360) {
	case 0:
		return "right", nil
	case 90:
		return "up", nil
	case 180:
		return "left", nil
	case 270:
		return "down", nil
	}
	return "", fmt.Errorf("pin rotation must be cardinal")
}

func layoutEditMarkerKind(component map[string]any, net string) (string, error) {
	kind, _ := component["componentType"].(string)
	switch kind {
	case "netport":
		return "net_port_bi", nil
	case "netflag":
		if strings.EqualFold(net, "GND") {
			return "ground", nil
		}
		return "power", nil
	default:
		return "", fmt.Errorf("marker %s is not a supported netport/netflag", component["primitiveId"])
	}
}

func layoutEditCandidateOffsets(preferred float64) ([]float64, error) {
	if !plGrid(preferred) || preferred <= 0 || preferred > 80 {
		return nil, fmt.Errorf("offset must be 5-raw grid in 5..80")
	}
	out := []float64{preferred}
	for n := 10.0; n <= 80; n += 5 {
		if n != preferred {
			out = append(out, n)
		}
	}
	return out, nil
}

func layoutEditDeepResult(result map[string]any) (map[string]any, error) {
	raw, e := json.Marshal(result)
	if e != nil {
		return nil, e
	}
	var out map[string]any
	e = json.Unmarshal(raw, &out)
	return out, e
}

func removeLayoutEditObject(result map[string]any, componentID, wireID string) {
	components, _ := result["components"].([]any)
	keptComponents := []any{}
	for _, v := range components {
		m, _ := v.(map[string]any)
		if m["primitiveId"] != componentID {
			keptComponents = append(keptComponents, v)
		}
	}
	result["components"] = keptComponents
	wires, _ := result["wires"].([]any)
	keptWires := []any{}
	for _, v := range wires {
		m, _ := v.(map[string]any)
		if m["primitiveId"] != wireID {
			keptWires = append(keptWires, v)
		}
	}
	result["wires"] = keptWires
}

func layoutEditBox(m map[string]any) (layoutBBox, bool) {
	raw, ok := m["bbox"].(map[string]any)
	if !ok {
		return layoutBBox{}, false
	}
	a, aok := layoutEditNumber(raw, "minX")
	b, bok := layoutEditNumber(raw, "minY")
	c, cok := layoutEditNumber(raw, "maxX")
	d, dok := layoutEditNumber(raw, "maxY")
	box := layoutBBox{MinX: a, MinY: b, MaxX: c, MaxY: d}
	return box, aok && bok && cok && dok && plBoxValid(box)
}

func validateLayoutEditMarkerCandidate(snapshot SchematicLayoutEditSnapshot, oldWireID, oldMarkerID, targetComponentID, targetPin string, flag powerLayoutFlag) error {
	result, err := layoutEditDeepResult(snapshot.Result)
	if err != nil {
		return err
	}
	removeLayoutEditObject(result, oldMarkerID, oldWireID)
	x, y := endpointFor(flag.PinX, flag.PinY, flag.Offset, flag.Direction)
	wires, _ := result["wires"].([]any)
	for _, value := range wires {
		wire, _ := value.(map[string]any)
		x0, a := layoutEditNumber(wire, "x0")
		y0, b := layoutEditNumber(wire, "y0")
		x1, c := layoutEditNumber(wire, "x1")
		y1, d := layoutEditNumber(wire, "y1")
		if a && b && c && d && plSegmentsContact([2]float64{flag.PinX, flag.PinY}, [2]float64{x, y}, [2]float64{x0, y0}, [2]float64{x1, y1}) {
			return fmt.Errorf("new dedicated stub touches existing wire %s", wire["primitiveId"])
		}
	}
	components, _ := result["components"].([]any)
	boxes := schTerminalMarkerBoxes(flag)
	for _, value := range components {
		component, _ := value.(map[string]any)
		id, _ := component["primitiveId"].(string)
		kind, _ := component["componentType"].(string)
		if box, ok := layoutEditBox(component); ok {
			for _, candidate := range boxes {
				if boxesGapOverlap(candidate, box, 5) {
					return fmt.Errorf("new marker is too close to %s", id)
				}
			}
		}
		if kind != "part" {
			continue
		}
		pins, _ := component["pins"].([]any)
		for _, raw := range pins {
			pin, _ := raw.(map[string]any)
			number, _ := pin["pinNumber"].(string)
			px, a := layoutEditNumber(pin, "x")
			py, b := layoutEditNumber(pin, "y")
			if !a || !b {
				continue
			}
			if id == targetComponentID && number == targetPin {
				continue
			}
			if plOnSegment([2]float64{px, py}, [2]float64{flag.PinX, flag.PinY}, [2]float64{x, y}) {
				return fmt.Errorf("new stub touches pin %s.%s", component["designator"], number)
			}
		}
	}
	result["wires"] = append(wires, map[string]any{"primitiveId": "__PROPOSED_WIRE__", "x0": flag.PinX, "y0": flag.PinY, "x1": x, "y1": y})
	findings := schguard.AnalyzeWireGeometry(result)
	for _, finding := range findings {
		if finding.WirePrimitiveId == "__PROPOSED_WIRE__" {
			return fmt.Errorf("%s: %s", finding.Type, finding.Message)
		}
	}
	return nil
}

func planSchematicPinMarkerRepair(source SchematicZonesInput, page SchematicRenderInput, snapshot SchematicLayoutEditSnapshot, componentID, pinNumber string, preferredOffset float64, report *SchematicLayoutEditReport) (*SchematicPinMarkerRepairTarget, *playbook, error) {
	if err := validateLayoutEditPageSource(source, page); err != nil {
		return nil, nil, err
	}
	components, _, owners, err := layoutEditSourceMaps(source)
	if err != nil {
		return nil, nil, err
	}
	sourceComponent := components[componentID]
	if sourceComponent.ID == "" {
		return nil, nil, fmt.Errorf("unknown component %s", componentID)
	}
	live, err := layoutEditLivePart(snapshot, sourceComponent)
	if err != nil {
		return nil, nil, err
	}
	livePrimitiveID, ok := layoutEditString(live, "primitiveId")
	if !ok {
		return nil, nil, fmt.Errorf("target component primitive identity unavailable")
	}
	pin, err := layoutEditPin(live, pinNumber)
	if err != nil {
		return nil, nil, err
	}
	sourcePin, err := layoutEditSourcePin(sourceComponent, pinNumber)
	if err != nil {
		return nil, nil, err
	}
	px, xok := layoutEditNumber(pin, "x")
	py, yok := layoutEditNumber(pin, "y")
	rotation, rok := layoutEditNumber(pin, "rotation")
	net, _ := pin["net"].(string)
	if !xok || !yok || !rok || net == "" {
		return nil, nil, fmt.Errorf("target pin geometry/net incomplete")
	}
	if sourcePin.Net != net {
		return nil, nil, fmt.Errorf("source-drift: source pin net %q differs from fresh snapshot net %q", sourcePin.Net, net)
	}
	liveX, liveXOK := layoutEditNumber(live, "x")
	liveY, liveYOK := layoutEditNumber(live, "y")
	var pageZone *SchematicRenderZone
	for i := range page.Zones {
		if page.Zones[i].ID == owners[componentID] {
			pageZone = &page.Zones[i]
			break
		}
	}
	if pageZone == nil {
		return nil, nil, fmt.Errorf("source-drift: owning zone %s is absent from selected page", owners[componentID])
	}
	pageX, pageY, pagePinX, pagePinY, err := layoutEditComponentPageGeometry(*pageZone, componentID, pinNumber)
	if err != nil {
		return nil, nil, err
	}
	if !liveXOK || !liveYOK || liveX != pageX || liveY != pageY || px != pagePinX || py != pagePinY {
		return nil, nil, fmt.Errorf("source-drift: selected page component/pin coordinates differ from fresh snapshot")
	}
	direction, err := layoutEditOutwardDirection(rotation)
	if err != nil {
		return nil, nil, err
	}
	fingerprint, err := schguard.SceneFingerprint(snapshot.Result)
	if err != nil {
		return nil, nil, err
	}
	report.SnapshotFingerprint, report.ZoneID, report.ComponentID, report.PinNumber = fingerprint, owners[componentID], componentID, pinNumber

	segments, err := layoutEditWireSegments(snapshot)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string][]layoutEditSegment{}
	for _, segment := range segments {
		byID[segment.ID] = append(byID[segment.ID], segment)
	}
	liveComponents, err := layoutEditSnapshotComponents(snapshot)
	if err != nil {
		return nil, nil, err
	}
	type pair struct {
		wire   layoutEditSegment
		marker map[string]any
	}
	var pairs []pair
	for id, group := range byID {
		if len(group) != 1 {
			continue
		}
		segment := group[0]
		var other [2]float64
		switch {
		case segment.A == [2]float64{px, py}:
			other = segment.B
		case segment.B == [2]float64{px, py}:
			other = segment.A
		default:
			continue
		}
		for _, marker := range liveComponents {
			kind, _ := marker["componentType"].(string)
			if kind != "netport" && kind != "netflag" {
				continue
			}
			mx, a := layoutEditNumber(marker, "x")
			my, b := layoutEditNumber(marker, "y")
			markerNet, _ := marker["net"].(string)
			if a && b && mx == other[0] && my == other[1] && markerNet == net {
				segment.ID = id
				pairs = append(pairs, pair{segment, marker})
			}
		}
	}
	if len(pairs) != 1 {
		return nil, nil, fmt.Errorf("pin marker branch must resolve to one two-point wire and one marker; found %d", len(pairs))
	}
	old := pairs[0]
	markerID, _ := old.marker["primitiveId"].(string)
	markerType, _ := old.marker["componentType"].(string)
	mx, _ := layoutEditNumber(old.marker, "x")
	my, _ := layoutEditNumber(old.marker, "y")
	mr, mrok := layoutEditNumber(old.marker, "rotation")
	if markerID == "" || !mrok {
		return nil, nil, fmt.Errorf("old marker identity/rotation unavailable")
	}
	findings := schguard.AnalyzeWireGeometry(snapshot.Result)
	targetFinding := false
	for _, finding := range findings {
		if finding.WirePrimitiveId == old.wire.ID && finding.Designator == sourceComponent.Measurement.Designator {
			for _, p := range finding.Pins {
				if p == pinNumber {
					targetFinding = true
				}
			}
			if finding.Type == "wire-through-body" {
				targetFinding = true
			}
		}
	}
	if !targetFinding {
		return nil, nil, fmt.Errorf("old branch has no measured target direction/body finding")
	}
	kind, err := layoutEditMarkerKind(old.marker, net)
	if err != nil {
		return nil, nil, err
	}
	offsets, err := layoutEditCandidateOffsets(preferredOffset)
	if err != nil {
		return nil, nil, err
	}
	selected := 0.0
	for _, offset := range offsets {
		flag := powerLayoutFlag{Net: net, Kind: kind, PinX: px, PinY: py, Direction: direction, Offset: offset, Anchor: &SchematicMarkerAnchor{Type: "pin", ComponentID: componentID, PinNumber: pinNumber, ZoneID: owners[componentID], Net: net}}
		if err := validateLayoutEditMarkerCandidate(snapshot, old.wire.ID, markerID, livePrimitiveID, pinNumber, flag); err != nil {
			report.RejectedCandidates = append(report.RejectedCandidates, fmt.Sprintf("offset %g: %v", offset, err))
			continue
		}
		selected = offset
		break
	}
	if selected == 0 {
		return nil, nil, fmt.Errorf("no legal outward marker offset in 10..80 raw")
	}
	storedRotation := flagBodyRotation["port"][direction]
	if kind == "power" {
		storedRotation = flagBodyRotation["power"][direction]
	}
	if kind == "ground" {
		storedRotation = flagBodyRotation["ground"][direction]
	}
	expected := schematicPinMarkerRepairExpected{ProjectUUID: snapshot.Context.ProjectUUID, DocumentUUID: snapshot.Context.DocumentUUID, SceneFingerprint: fingerprint, ComponentID: componentID, ComponentPrimitiveID: livePrimitiveID, Designator: sourceComponent.Measurement.Designator, PinNumber: pinNumber, PinX: px, PinY: py, PinRotation: rotation, Net: net, WirePrimitiveID: old.wire.ID, WireX0: old.wire.A[0], WireY0: old.wire.A[1], WireX1: old.wire.B[0], WireY1: old.wire.B[1], MarkerPrimitiveID: markerID, MarkerType: markerType, MarkerX: mx, MarkerY: my, MarkerRotation: mr}
	replacement := schematicPinMarkerReplacement{Kind: kind, Net: net, Direction: direction, Offset: selected, Rotation: storedRotation}
	target := &SchematicPinMarkerRepairTarget{SchemaVersion: 1, Operation: "repair_pin_marker", Expected: expected, Replacement: replacement}
	payloadRaw, _ := json.Marshal(target)
	var payload map[string]any
	_ = json.Unmarshal(payloadRaw, &payload)
	delete(payload, "schemaVersion")
	delete(payload, "operation")
	confirm := false
	repairTimeout := 120
	pb := &playbook{RequireFullExecution: true, Version: 1, Meta: playbookMeta{Name: "protected pin marker repair", Project: snapshot.Context.ProjectUUID, Doc: snapshot.Context.DocumentUUID}, Defaults: stepPolicy{}, Steps: []playbookStep{{ID: "repair-pin-marker", Name: "replace one verified marker branch", Action: "schematic.pin.repair_marker", Payload: payload, Confirm: &confirm, Checkpoint: true, stepPolicy: stepPolicy{TimeoutSec: &repairTimeout}}, {ID: "save", Name: "save verified schematic", Action: "schematic.save", Confirm: &confirm, Checkpoint: true}, {ID: "export", Name: "export official schematic image", Action: "schematic.export.image", Payload: map[string]any{"format": "svg", "scope": "page", "fileName": "layout-edit-" + strings.ReplaceAll(sourceComponent.Measurement.Designator+"-"+pinNumber, "/", "_") + ".svg"}, Confirm: &confirm}}}
	report.Strategy = "pin-outward-axis"
	report.TargetX, report.TargetY = endpointFor(px, py, selected, direction)
	return target, pb, nil
}
