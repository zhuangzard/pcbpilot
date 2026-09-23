package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

type schDesignInput struct {
	canonical      connectivity.DesignEvidence
	drawing        map[string]any
	kind           string
	missingDrawing []connectivity.DesignUnverified
	modulesPresent bool
}

func decodeSchDesignInput(raw []byte) (schDesignInput, error) {
	var result schDesignInput
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return result, err
	}
	cRaw, hasEnvelope := root["connectivity"]
	if !hasEnvelope {
		_, result.modulesPresent = root["modules"]
		e, err := connectivity.DecodeDesignEvidence(raw)
		result.canonical, result.kind = e, "canonical"
		return result, err
	}
	var canonicalFields map[string]json.RawMessage
	if err := json.Unmarshal(cRaw, &canonicalFields); err != nil {
		return result, err
	}
	_, result.modulesPresent = canonicalFields["modules"]
	if _, hasPlan := root["layout"]; hasPlan {
		var p schCompositionPlan
		if err := connectivity.DecodeStrictDesignJSON(raw, &p); err != nil {
			return result, err
		}
		if err := requireSchDesignFields(raw, reflect.TypeOf(p), nil); err != nil {
			return result, err
		}
		if _, present := root["usableBounds"]; present && !plBoxValid(p.UsableBounds) {
			return result, fmt.Errorf("invalid plan usableBounds")
		}
		if _, present := root["rowHeights"]; present {
			if len(p.RowHeights) != p.Rows {
				return result, fmt.Errorf("rowHeights must cover every declared row")
			}
			for _, height := range p.RowHeights {
				if height <= 0 {
					return result, fmt.Errorf("row heights must be positive")
				}
			}
		}
		if err := validateSchDesignPlan(p); err != nil {
			return result, err
		}
		// Complete plan geometry is explicit evidence, including zero coordinates
		// omitted by canonical Pin.X/Y's historical omitempty serialization.
		enriched, err := enrichSchDesignPlanGeometry(cRaw, p)
		if err != nil {
			return result, err
		}
		result.canonical, err = connectivity.DecodeDesignEvidence(enriched)
		if err != nil {
			return result, err
		}
		result.drawing, err = schDesignDrawingState(p)
		for _, key := range []string{"placementBoundarySource", "usableBounds", "rowHeights"} {
			if _, ok := root[key]; !ok {
				delete(result.drawing, key)
				result.missingDrawing = append(result.missingDrawing, connectivity.DesignUnverified{Path: "/drawing/" + key, Reason: "older plan omitted this additive planning diagnostic; it was not synthesized"})
			}
		}
		result.kind = "compose-plan"
		return result, err
	}
	if _, hasSchema := root["schemaVersion"]; hasSchema {
		var p schCompositionSource
		if err := connectivity.DecodeStrictDesignJSON(raw, &p); err != nil {
			return result, err
		}
		if p.SchemaVersion != 1 {
			return result, fmt.Errorf("compose source requires schemaVersion:1")
		}
		result.kind = "compose-source"
	} else {
		var envelope struct {
			Connectivity connectivity.Document `json:"connectivity"`
		}
		if err := connectivity.DecodeStrictDesignJSON(raw, &envelope); err != nil {
			return result, err
		}
		result.kind = "connectivity-envelope"
	}
	var err error
	result.canonical, err = connectivity.DecodeDesignEvidence(cRaw)
	return result, err
}

// Every non-omitempty plan field must be supplied. Null slices/maps are the
// existing Go plan's empty-inventory encoding; null scalars never mean zero.
func requireSchDesignFields(raw []byte, t reflect.Type, path []string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if string(raw) == "null" {
		if t.Kind() == reflect.Map || t.Kind() == reflect.Slice {
			return nil
		}
		return &connectivity.IncompleteDesignError{Reason: "missing plan evidence at /" + strings.Join(path, "/")}
	}
	switch t.Kind() {
	case reflect.Struct:
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return err
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			name := tag[0]
			if name == "" || name == "-" {
				continue
			}
			item, present := obj[name]
			// The canonical decoder applies its own optionality/evidence contract.
			if name == "connectivity" && len(path) == 0 {
				continue
			}
			// Runtime primitive handles are explicitly excluded from content
			// comparison, so their absence cannot make a local plan incomplete.
			if name == "primitiveId" && len(path) == 3 && path[0] == "layout" && path[1] == "placements" {
				continue
			}
			optional := len(tag) > 1 && tag[1] == "omitempty"
			if len(path) == 0 && (name == "placementBoundarySource" || name == "usableBounds" || name == "rowHeights") {
				optional = true
			}
			if !present {
				if optional {
					continue
				}
				return &connectivity.IncompleteDesignError{Reason: "missing plan field /" + strings.Join(append(path, name), "/")}
			}
			if err := requireSchDesignFields(item, f.Type, append(path, name)); err != nil {
				return err
			}
		}
	case reflect.Array, reflect.Slice:
		var list []json.RawMessage
		if err := json.Unmarshal(raw, &list); err != nil {
			return err
		}
		for i, item := range list {
			if err := requireSchDesignFields(item, t.Elem(), append(path, fmt.Sprint(i))); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate representable input and identity evidence, not the project's visual
// design rules. Solid/dotted frames, angled wires and arbitrary finite rotations
// must remain comparable even if compose/frame lint would refuse to draw them.
func validateSchDesignPlan(p schCompositionPlan) error {
	if p.SchemaVersion != 1 || p.Layout.SchemaVersion != 1 {
		return fmt.Errorf("compose plan and layout require schemaVersion:1")
	}
	if p.Layout.DocumentID != p.Connectivity.DocumentID || p.Layout.DocumentID == "" {
		return fmt.Errorf("compose layout documentId disagrees with canonical target")
	}
	if !plBoxValid(p.Sheet) || p.Rows < 1 || p.RowHeight <= 0 || p.PageMargin < 0 || p.ModuleGap < 0 {
		return fmt.Errorf("invalid compose sheet/row dimensions")
	}
	if p.SheetBorder != nil && (!plBoxValid(*p.SheetBorder) || !boxInside(*p.SheetBorder, p.Sheet)) {
		return fmt.Errorf("invalid plan sheetBorder")
	}
	switch p.PlacementBoundarySource {
	case "", "sheet-bbox-fallback", "explicit-sheet-border":
	default:
		return fmt.Errorf("unknown placementBoundarySource %q", p.PlacementBoundarySource)
	}
	for _, b := range p.Keepouts {
		if !plBoxValid(b) {
			return fmt.Errorf("invalid compose keepout")
		}
	}
	refs := map[string]bool{}
	for _, c := range p.Connectivity.Components {
		refs[c.Ref] = true
	}
	seen := map[string]bool{}
	for _, c := range p.Layout.Placements {
		if !refs[c.Designator] || seen[c.Designator] {
			return fmt.Errorf("unknown/duplicate drawing placement %q", c.Designator)
		}
		seen[c.Designator] = true
		if !plBoxValid(c.BBox) || !plFinite(c.X) || !plFinite(c.Y) || !plFinite(c.Rotation) {
			return fmt.Errorf("invalid drawing placement %s", c.Designator)
		}
		pins := map[string]bool{}
		for _, pin := range c.Pins {
			if strings.TrimSpace(pin.Number) == "" || pins[pin.Number] {
				return fmt.Errorf("invalid/duplicate drawing pin on %s", c.Designator)
			}
			pins[pin.Number] = true
		}
	}
	if len(seen) != len(refs) {
		return &connectivity.IncompleteDesignError{Reason: "drawing placements must cover every canonical component"}
	}
	for _, w := range p.Layout.Wires {
		if w.Net == "" || len(w.Points) < 2 {
			return fmt.Errorf("wire needs a net and at least two points")
		}
	}
	for _, f := range p.Layout.Flags {
		if f.Net == "" || f.Offset <= 0 {
			return fmt.Errorf("invalid drawing flag net/offset")
		}
		switch f.Direction {
		case "left", "right", "up", "down":
		default:
			return fmt.Errorf("unsupported flag direction %q", f.Direction)
		}
		switch f.Kind {
		case "power", "ground", "net_port_in", "net_port_out", "net_port_bi":
		default:
			return fmt.Errorf("unsupported flag kind %q", f.Kind)
		}
	}
	frameIDs := map[string]bool{}
	for _, f := range p.Layout.Frames {
		if f.ID == "" || frameIDs[f.ID] || !plBoxValid(f.Rect) || f.FontSize <= 0 || !schFrameColorRE.MatchString(f.Color) || (f.LineType < 0 || f.LineType > 3) {
			return fmt.Errorf("invalid/duplicate frame %q", f.ID)
		}
		frameIDs[f.ID] = true
	}
	return nil
}

func enrichSchDesignPlanGeometry(raw []byte, p schCompositionPlan) ([]byte, error) {
	var canonical map[string]any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return nil, err
	}
	placements := map[string]powerLayoutPlacement{}
	for _, c := range p.Layout.Placements {
		placements[c.Designator] = c
	}
	pinNets := map[string]map[string]string{}
	netNames := map[string]string{}
	for _, n := range p.Connectivity.Nets {
		netNames[n.ID] = n.Name
	}
	for _, e := range p.Connectivity.Connections {
		if pinNets[e.ComponentID] == nil {
			pinNets[e.ComponentID] = map[string]string{}
		}
		pinNets[e.ComponentID][e.PinNumber] = netNames[e.NetID]
	}
	components, ok := canonical["components"].([]any)
	if !ok {
		return nil, &connectivity.IncompleteDesignError{Reason: "missing canonical components inventory"}
	}
	for _, item := range components {
		c := item.(map[string]any)
		ref, _ := c["ref"].(string)
		id, _ := c["id"].(string)
		placed := placements[ref]
		pins := map[string]powerLayoutPin{}
		for _, pin := range placed.Pins {
			pins[pin.Number] = pin
		}
		cPins, ok := c["pins"].([]any)
		if !ok || len(cPins) != len(pins) {
			return nil, &connectivity.IncompleteDesignError{Reason: ref + " drawing pin inventory differs from canonical"}
		}
		for _, item := range cPins {
			pin := item.(map[string]any)
			number, _ := pin["number"].(string)
			drawn, exists := pins[number]
			if !exists || drawn.Net != pinNets[id][number] {
				return nil, fmt.Errorf("%s.%s drawing pin identity/net disagrees with canonical", ref, number)
			}
			for k, v := range map[string]float64{"x": drawn.X, "y": drawn.Y} {
				if old, exists := pin[k]; exists && normalizeSchDesignDrawingNumbers(old) != connectivity.NormalizeDesignNumber(v) {
					return nil, fmt.Errorf("%s.%s canonical/drawing %s disagree", ref, number, k)
				}
				pin[k] = v
			}
		}
		// Existing placement values must agree; absence can be filled from the
		// complete typed layout, without fabricating a measurement.
		b, _ := json.Marshal(connectivity.Placement{X: placed.X, Y: placed.Y, Rotation: placed.Rotation, Mirror: placed.Mirror, BBox: &connectivity.BBox{MinX: placed.BBox.MinX, MinY: placed.BBox.MinY, MaxX: placed.BBox.MaxX, MaxY: placed.BBox.MaxY}})
		var replacement map[string]any
		_ = json.Unmarshal(b, &replacement)
		if old, exists := c["placement"]; exists {
			oldObj, ok := old.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s placement must be an object", ref)
			}
			for key, defaultValue := range map[string]any{"rotation": float64(0), "mirror": false} {
				if _, ok := oldObj[key]; !ok {
					oldObj[key] = defaultValue
				}
			}
			for k, v := range oldObj {
				newVal := replacement[k]
				if newVal == nil {
					if k == "rotation" {
						newVal = float64(0)
					}
					if k == "mirror" {
						newVal = false
					}
				}
				if !reflect.DeepEqual(normalizeSchDesignDrawingNumbers(v), normalizeSchDesignDrawingNumbers(newVal)) {
					return nil, fmt.Errorf("%s canonical/drawing placement %s disagree", ref, k)
				}
			}
		}
		c["placement"] = replacement
	}
	return json.Marshal(canonical)
}

func schDesignDrawingState(p schCompositionPlan) (map[string]any, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var state map[string]any
	if err = json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	delete(state, "connectivity")
	normalizeSchDesignDrawingNumbers(state)
	layout := state["layout"].(map[string]any)
	if layout["expectedPinNets"] == nil {
		layout["expectedPinNets"] = map[string]any{}
	}
	refs := map[string]string{}
	for _, c := range p.Connectivity.Components {
		refs[c.Ref] = c.ID
	}
	placements := map[string]any{}
	for _, item := range designDrawingList(layout["placements"]) {
		c := item.(map[string]any)
		delete(c, "primitiveId")
		pins := map[string]any{}
		for _, item := range designDrawingList(c["pins"]) {
			pin := item.(map[string]any)
			pins[pin["number"].(string)] = pin
		}
		c["pins"] = pins
		placements[refs[c["designator"].(string)]] = c
	}
	layout["placements"] = placements
	frames := map[string]any{}
	order := []any{}
	for _, item := range designDrawingList(layout["frames"]) {
		f := item.(map[string]any)
		id := f["id"].(string)
		order = append(order, id)
		if title, ok := f["titleLayout"].(map[string]any); ok {
			title["obstacles"] = sortDesignDrawingList(title["obstacles"])
		}
		frames[id] = f
	}
	layout["frames"] = frames
	layout["frameOrder"] = order
	// Wires have no direction: traversing an entire polyline in reverse yields
	// the same drawing. Preserve intermediate point adjacency and duplicates;
	// only normalize the two equivalent whole-path traversal directions.
	for _, item := range designDrawingList(layout["wires"]) {
		wire := item.(map[string]any)
		points := wire["points"].([]any)
		reversed := make([]any, len(points))
		for i, p := range points {
			reversed[len(points)-1-i] = p
		}
		forward, _ := json.Marshal(points)
		backward, _ := json.Marshal(reversed)
		if string(backward) < string(forward) {
			wire["points"] = reversed
		}
	}
	// Wire/flag collection order is immaterial; duplicate records are retained.
	layout["wires"] = sortDesignDrawingList(layout["wires"])
	layout["flags"] = sortDesignDrawingList(layout["flags"])
	state["keepouts"] = sortDesignDrawingList(state["keepouts"])
	return state, nil
}

// Normalize numbers before sorting/reversing drawing collections. Canonical
// and drawing comparisons share one precision and content-hash contract.
func normalizeSchDesignDrawingNumbers(value any) any {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			v[key] = normalizeSchDesignDrawingNumbers(item)
		}
	case []any:
		for i, item := range v {
			v[i] = normalizeSchDesignDrawingNumbers(item)
		}
	case float64:
		return connectivity.NormalizeDesignNumber(v)
	}
	return value
}

func designDrawingList(v any) []any {
	items, _ := v.([]any)
	if items == nil {
		return []any{}
	}
	return items
}
func sortDesignDrawingList(v any) []any {
	items := designDrawingList(v)
	sort.SliceStable(items, func(i, j int) bool {
		a, _ := json.Marshal(items[i])
		b, _ := json.Marshal(items[j])
		return string(a) < string(b)
	})
	return items
}

func compareSchDesignInputs(a, b schDesignInput) (connectivity.DesignDiff, error) {
	diff, err := connectivity.CompareDesignEvidence(a.canonical, b.canonical)
	if err != nil {
		return diff, err
	}
	if a.drawing != nil && b.drawing != nil {
		diff.Coverage.Scope = "local-compose-plan"
		diff.Coverage.DrawingCompared = true
		for _, side := range []struct {
			name  string
			input schDesignInput
		}{{"expected", a}, {"actual", b}} {
			for _, u := range side.input.missingDrawing {
				u.Side = side.name
				diff.Coverage.Unverified = append(diff.Coverage.Unverified, u)
			}
		}
		// Local complete drawings are compared; actual editor execution/rendering
		// remains outside the evidence this offline operation has received.
		diff.Coverage.Unverified[0] = connectivity.DesignUnverified{Side: "both", Path: "/editor", Reason: "local plans do not establish actual EDA wire/frame/text state; use fresh readback and frame check/export-image"}
		connectivity.AppendDesignChanges([]string{"drawing"}, a.drawing, b.drawing, &diff.Changes)
		diff.ExpectedRevision, err = schFullDesignRevision(diff.ExpectedRevision, a.drawing)
		if err != nil {
			return diff, err
		}
		diff.ActualRevision, err = schFullDesignRevision(diff.ActualRevision, b.drawing)
		if err != nil {
			return diff, err
		}
		if diff.Status == "synced" && len(diff.Changes) > 0 {
			diff.Status = "different"
		}
	} else {
		diff.Coverage.Unverified[0].Reason = fmt.Sprintf("drawing comparison requires two complete compose plans; received %s and %s; only shared canonical content was compared", a.kind, b.kind)
		markSchDesignReadbackCoverage(a, b, &diff)
	}
	connectivity.SortDesignChanges(diff.Changes)
	return diff, nil
}

// Canonical exports establish pin-to-net state, but can omit authored modules
// and report a connection's provenance as netlist rather than its drawn marker
// type. Missing evidence is not a deletion or a contrary drawing observation.
// Keep complete local plan comparisons strict, including explicit modules: [].
func markSchDesignReadbackCoverage(a, b schDesignInput, diff *connectivity.DesignDiff) {
	var observed schDesignInput
	side := ""
	if a.kind == "compose-plan" && (b.kind == "canonical" || b.kind == "connectivity-envelope") {
		observed, side = b, "actual"
	} else if b.kind == "compose-plan" && (a.kind == "canonical" || a.kind == "connectivity-envelope") {
		observed, side = a, "expected"
	} else {
		return
	}
	unverified := []connectivity.DesignUnverified{}
	if !observed.modulesPresent {
		for _, path := range []string{"/modules", "/moduleOrder"} {
			unverified = append(unverified, connectivity.DesignUnverified{Side: side, Path: path, Reason: "canonical readback omitted authored modules; absence does not prove an empty module inventory or reading order"})
		}
	}
	changes := make([]connectivity.DesignChange, 0, len(diff.Changes))
	for _, change := range diff.Changes {
		if !observed.modulesPresent && change.Domain == "module" {
			continue
		}
		value := change.After
		if side == "expected" {
			value = change.Before
		}
		if change.Domain == "connection" && strings.HasSuffix(change.Path, "/kind") && value == "netlist" {
			unverified = append(unverified, connectivity.DesignUnverified{Side: side, Path: change.Path, Reason: "netlist proves pin-to-net membership but does not establish the intended wire or marker drawing kind"})
			continue
		}
		changes = append(changes, change)
	}
	diff.Changes = changes
	if len(unverified) != 0 {
		diff.Coverage.Unverified = append(diff.Coverage.Unverified, unverified...)
		diff.Coverage.CanonicalComplete = false
		if diff.Status != "wrong-target" {
			diff.Status = "incomplete"
		}
	}
}

func schFullDesignRevision(canonical string, drawing map[string]any) (string, error) {
	raw, err := json.Marshal(map[string]any{"canonicalRevision": canonical, "drawing": drawing})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
