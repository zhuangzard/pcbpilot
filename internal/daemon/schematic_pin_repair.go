package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"github.com/zhuangzard/pcbpilot/internal/protocol"
	"github.com/zhuangzard/pcbpilot/internal/schguard"
)

const (
	pinRepairReadBudget    = 20 * time.Second
	pinRepairDeleteBudget  = 25 * time.Second
	pinRepairConnectBudget = 35 * time.Second
)

type pinRepairExpected struct {
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

type pinRepairReplacement struct {
	Kind      string  `json:"kind"`
	Net       string  `json:"net"`
	Direction string  `json:"direction"`
	Offset    float64 `json:"offset"`
	Rotation  float64 `json:"rotation"`
}

type pinRepairPayload struct {
	Expected    pinRepairExpected    `json:"expected"`
	Replacement pinRepairReplacement `json:"replacement"`
}

type pinRepairJournalEntry struct {
	Phase     string         `json:"phase"`
	Action    string         `json:"action"`
	RequestID string         `json:"requestId"`
	OK        bool           `json:"ok"`
	Seq       *int           `json:"seq,omitempty"`
	Result    map[string]any `json:"result,omitempty"`
}

func decodePinRepairPayload(payload map[string]any) (pinRepairPayload, error) {
	var out pinRepairPayload
	raw, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	e, r := out.Expected, out.Replacement
	for name, value := range map[string]string{
		"expected.projectUuid": e.ProjectUUID, "expected.documentUuid": e.DocumentUUID,
		"expected.sceneFingerprint": e.SceneFingerprint, "expected.componentId": e.ComponentID,
		"expected.componentPrimitiveId": e.ComponentPrimitiveID, "expected.designator": e.Designator,
		"expected.pinNumber": e.PinNumber, "expected.net": e.Net,
		"expected.wirePrimitiveId": e.WirePrimitiveID, "expected.markerPrimitiveId": e.MarkerPrimitiveID,
		"expected.markerType": e.MarkerType, "replacement.kind": r.Kind,
		"replacement.net": r.Net, "replacement.direction": r.Direction,
	} {
		if strings.TrimSpace(value) == "" {
			return out, fmt.Errorf("%s is required", name)
		}
	}
	for name, value := range map[string]float64{
		"pinX": e.PinX, "pinY": e.PinY, "pinRotation": e.PinRotation,
		"wireX0": e.WireX0, "wireY0": e.WireY0, "wireX1": e.WireX1, "wireY1": e.WireY1,
		"markerX": e.MarkerX, "markerY": e.MarkerY, "markerRotation": e.MarkerRotation,
		"replacement.offset": r.Offset, "replacement.rotation": r.Rotation,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return out, fmt.Errorf("%s must be finite", name)
		}
	}
	if e.WirePrimitiveID == e.MarkerPrimitiveID {
		return out, fmt.Errorf("old wire and marker ids must differ")
	}
	if r.Net != e.Net {
		return out, fmt.Errorf("replacement net %q differs from expected pin net %q", r.Net, e.Net)
	}
	if r.Offset < 5 || r.Offset > 80 || !pinRepairGrid(r.Offset) {
		return out, fmt.Errorf("replacement offset must be on the 5-raw grid in 5..80")
	}
	if !pinRepairGrid(e.PinX) || !pinRepairGrid(e.PinY) || !pinRepairGrid(r.Rotation) {
		return out, fmt.Errorf("pin coordinate and replacement rotation must be on their declared grids")
	}
	wantDirection, err := pinRepairOutward(e.PinRotation)
	if err != nil {
		return out, err
	}
	if r.Direction != wantDirection {
		return out, fmt.Errorf("replacement direction %q is not pin outward direction %q", r.Direction, wantDirection)
	}
	wantMarkerType, err := pinRepairMarkerType(r.Kind)
	if err != nil {
		return out, err
	}
	if e.MarkerType != wantMarkerType {
		return out, fmt.Errorf("replacement kind %q cannot replace measured marker type %q", r.Kind, e.MarkerType)
	}
	wantRotation, err := pinRepairStoredRotation(r.Kind, r.Direction)
	if err != nil {
		return out, err
	}
	actualRotation := math.Mod(math.Mod(r.Rotation, 360)+360, 360)
	if !pinRepairEqual(actualRotation, wantRotation) {
		return out, fmt.Errorf("replacement rotation %g does not orient %s outward for %s; want %g", r.Rotation, r.Kind, r.Direction, wantRotation)
	}
	return out, nil
}

func pinRepairGrid(v float64) bool {
	return math.Abs(v/5-math.Round(v/5)) <= 1e-6
}

func pinRepairEqual(a, b float64) bool { return math.Abs(a-b) <= 1e-6 }

func pinRepairOutward(rotation float64) (string, error) {
	r := math.Mod(math.Mod(rotation, 360)+360, 360)
	if math.Abs(r/90-math.Round(r/90)) > 1e-6 {
		return "", fmt.Errorf("expected pin rotation %g is not cardinal", rotation)
	}
	switch int(math.Round(r)) % 360 {
	case 0:
		return "right", nil
	case 90:
		return "up", nil
	case 180:
		return "left", nil
	case 270:
		return "down", nil
	default:
		return "", fmt.Errorf("expected pin rotation %g is unsupported", rotation)
	}
}

func pinRepairMarkerType(kind string) (string, error) {
	switch kind {
	case "power", "ground", "analog_ground", "protective_ground", "protect_ground":
		return "netflag", nil
	case "net_port_in", "net_port_out", "net_port_bi":
		return "netport", nil
	default:
		return "", fmt.Errorf("unsupported replacement marker kind %q", kind)
	}
}

// pinRepairStoredRotation mirrors the calibrated stored-rotation truth in
// .agents/skills/pcbpilot/references/orientation.json. The generated playbook
// already uses this table; the daemon repeats the check so a hand-edited target
// cannot keep the wire outward while turning the marker body back toward it.
func pinRepairStoredRotation(kind, direction string) (float64, error) {
	family := "power"
	switch {
	case kind == "ground" || kind == "analog_ground" || kind == "protective_ground" || kind == "protect_ground":
		family = "ground"
	case strings.HasPrefix(kind, "net_port"):
		family = "port"
	case kind != "power":
		return 0, fmt.Errorf("unsupported replacement marker kind %q", kind)
	}
	table := map[string]map[string]float64{
		"power":  {"up": 0, "left": 90, "down": 180, "right": 270},
		"ground": {"up": 180, "left": 270, "down": 0, "right": 90},
		"port":   {"up": 90, "left": 180, "down": 270, "right": 0},
	}
	rotation, ok := table[family][direction]
	if !ok {
		return 0, fmt.Errorf("unsupported replacement direction %q", direction)
	}
	return rotation, nil
}

func pinRepairEndpoint(x, y float64, r pinRepairReplacement) (float64, float64) {
	switch r.Direction {
	case "left":
		x -= r.Offset
	case "right":
		x += r.Offset
	case "up":
		y += r.Offset
	case "down":
		y -= r.Offset
	}
	return x, y
}

func pinRepairFailure(req protocol.Request, phase string, mutationAttempted bool, detail string, journal []pinRepairJournalEntry, evidence any) *protocol.Response {
	r := errorResponse(req.ID, "SCHEMATIC_PIN_REPAIR_FAILED", "protected pin marker repair failed ("+phase+")", detail)
	r.Result = map[string]any{
		"phase":             phase,
		"verified":          false,
		"mutationAttempted": mutationAttempted,
		"journal":           journal,
	}
	if evidence != nil {
		r.Result["evidence"] = evidence
	}
	if mutationAttempted {
		r.Result["partial"] = true
		r.Warnings = []string{"The scoped replacement may be partially landed. No rollback or retry was attempted; read the actual page before any next write."}
	}
	return &r
}

func pinRepairNumber(value any) (float64, bool) {
	switch value := value.(type) {
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	case float32:
		return float64(value), !math.IsNaN(float64(value)) && !math.IsInf(float64(value), 0)
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		n, err := value.Float64()
		return n, err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
	default:
		return 0, false
	}
}

func pinRepairArray(value any) ([]any, bool) {
	if values, ok := value.([]any); ok {
		return values, true
	}
	if values, ok := value.([]map[string]any); ok {
		out := make([]any, len(values))
		for i := range values {
			out[i] = values[i]
		}
		return out, true
	}
	return nil, false
}

func pinRepairPointPair(value any) ([2]float64, bool) {
	var out [2]float64
	values, ok := pinRepairArray(value)
	if !ok || len(values) != 2 {
		return out, false
	}
	x, xok := pinRepairNumber(values[0])
	y, yok := pinRepairNumber(values[1])
	return [2]float64{x, y}, xok && yok
}

func pinRepairWirePoints(wire map[string]any) ([][2]float64, bool) {
	if raw, exists := wire["points"]; exists {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, false
		}
		var values []any
		if json.Unmarshal(encoded, &values) != nil || len(values) < 2 {
			return nil, false
		}
		if _, nested := values[0].([]any); nested {
			out := make([][2]float64, 0, len(values))
			for _, value := range values {
				point, ok := pinRepairPointPair(value)
				if !ok {
					return nil, false
				}
				out = append(out, point)
			}
			return out, true
		}
		if len(values)%2 != 0 {
			return nil, false
		}
		out := make([][2]float64, 0, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			x, xok := pinRepairNumber(values[i])
			y, yok := pinRepairNumber(values[i+1])
			if !xok || !yok {
				return nil, false
			}
			out = append(out, [2]float64{x, y})
		}
		return out, true
	}
	x0, a := pinRepairNumber(wire["x0"])
	y0, b := pinRepairNumber(wire["y0"])
	x1, c := pinRepairNumber(wire["x1"])
	y1, d := pinRepairNumber(wire["y1"])
	return [][2]float64{{x0, y0}, {x1, y1}}, a && b && c && d
}

func pinRepairComponents(result map[string]any) ([]map[string]any, error) {
	values, ok := pinRepairArray(result["components"])
	if !ok {
		return nil, fmt.Errorf("component inventory unavailable")
	}
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		component, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("malformed component inventory")
		}
		out = append(out, component)
	}
	return out, nil
}

func pinRepairFindComponent(result map[string]any, id string) (map[string]any, error) {
	components, err := pinRepairComponents(result)
	if err != nil {
		return nil, err
	}
	var found []map[string]any
	for _, component := range components {
		if component["primitiveId"] == id {
			found = append(found, component)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("primitive %s matched %d components", id, len(found))
	}
	return found[0], nil
}

func pinRepairFindPin(component map[string]any, number string) (map[string]any, error) {
	values, ok := pinRepairArray(component["pins"])
	if !ok {
		return nil, fmt.Errorf("target pin inventory unavailable")
	}
	var found []map[string]any
	for _, value := range values {
		pin, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("malformed target pin inventory")
		}
		actual, _ := pin["pinNumber"].(string)
		if actual == "" {
			actual, _ = pin["number"].(string)
		}
		if actual == number {
			found = append(found, pin)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("target pin %s matched %d records", number, len(found))
	}
	return found[0], nil
}

func pinRepairStableID(component map[string]any) string {
	properties, _ := component["otherProperty"].(map[string]any)
	id, _ := properties[connectivity.ComponentIDProperty].(string)
	return id
}

func pinRepairValidatePart(result map[string]any, expected pinRepairExpected) error {
	component, err := pinRepairFindComponent(result, expected.ComponentPrimitiveID)
	if err != nil {
		return err
	}
	if component["componentType"] != "part" || component["designator"] != expected.Designator || pinRepairStableID(component) != expected.ComponentID {
		return fmt.Errorf("target component identity differs from retained source")
	}
	pin, err := pinRepairFindPin(component, expected.PinNumber)
	if err != nil {
		return err
	}
	x, xok := pinRepairNumber(pin["x"])
	y, yok := pinRepairNumber(pin["y"])
	rotation, rok := pinRepairNumber(pin["rotation"])
	net, _ := pin["net"].(string)
	if !xok || !yok || !rok || !pinRepairEqual(x, expected.PinX) || !pinRepairEqual(y, expected.PinY) || !pinRepairEqual(rotation, expected.PinRotation) || net != expected.Net {
		return fmt.Errorf("target pin identity/geometry/net differs from retained source")
	}
	return nil
}

func pinRepairValidateOldWire(result map[string]any, expected pinRepairExpected) error {
	values, ok := pinRepairArray(result["wires"])
	if !ok {
		return fmt.Errorf("wire inventory unavailable")
	}
	var found []map[string]any
	for _, value := range values {
		wire, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("malformed wire inventory")
		}
		if wire["primitiveId"] == expected.WirePrimitiveID {
			found = append(found, wire)
		}
	}
	if len(found) != 1 {
		return fmt.Errorf("old wire %s matched %d records", expected.WirePrimitiveID, len(found))
	}
	points, ok := pinRepairWirePoints(found[0])
	if !ok || len(points) != 2 {
		return fmt.Errorf("old wire is not one fully measured segment")
	}
	wantA, wantB := [2]float64{expected.WireX0, expected.WireY0}, [2]float64{expected.WireX1, expected.WireY1}
	equalPoint := func(a, b [2]float64) bool { return pinRepairEqual(a[0], b[0]) && pinRepairEqual(a[1], b[1]) }
	if !(equalPoint(points[0], wantA) && equalPoint(points[1], wantB)) && !(equalPoint(points[0], wantB) && equalPoint(points[1], wantA)) {
		return fmt.Errorf("old wire geometry differs from retained source")
	}
	return nil
}

func pinRepairValidateMarker(result map[string]any, expected pinRepairExpected) error {
	marker, err := pinRepairFindComponent(result, expected.MarkerPrimitiveID)
	if err != nil {
		return err
	}
	x, xok := pinRepairNumber(marker["x"])
	y, yok := pinRepairNumber(marker["y"])
	rotation, rok := pinRepairNumber(marker["rotation"])
	net, _ := marker["net"].(string)
	if marker["componentType"] != expected.MarkerType || !xok || !yok || !rok || !pinRepairEqual(x, expected.MarkerX) || !pinRepairEqual(y, expected.MarkerY) || !pinRepairEqual(rotation, expected.MarkerRotation) || net != expected.Net {
		return fmt.Errorf("old marker identity/geometry/net differs from retained source")
	}
	return nil
}

func pinRepairFindingTargets(f schguard.Finding, expected pinRepairExpected) bool {
	if f.WirePrimitiveId != expected.WirePrimitiveID || f.PrimitiveId != expected.ComponentPrimitiveID {
		return false
	}
	if f.Type == "wire-through-body" {
		return true
	}
	if f.Type != "pin-exit-direction" {
		return false
	}
	for _, pin := range f.Pins {
		if pin == expected.PinNumber {
			return true
		}
	}
	return false
}

func pinRepairCloneResult(result map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}

func pinRepairRemove(result map[string]any, componentID, wireID string) {
	if values, ok := pinRepairArray(result["components"]); ok {
		kept := make([]any, 0, len(values))
		for _, value := range values {
			component, _ := value.(map[string]any)
			if component["primitiveId"] != componentID {
				kept = append(kept, value)
			}
		}
		result["components"] = kept
	}
	if values, ok := pinRepairArray(result["wires"]); ok {
		kept := make([]any, 0, len(values))
		for _, value := range values {
			wire, _ := value.(map[string]any)
			if wire["primitiveId"] != wireID {
				kept = append(kept, value)
			}
		}
		result["wires"] = kept
	}
}

func pinRepairHasPrimitive(result map[string]any, id string) bool {
	if values, ok := pinRepairArray(result["components"]); ok {
		for _, value := range values {
			component, _ := value.(map[string]any)
			if component["primitiveId"] == id {
				return true
			}
		}
	}
	if values, ok := pinRepairArray(result["wires"]); ok {
		for _, value := range values {
			wire, _ := value.(map[string]any)
			if wire["primitiveId"] == id {
				return true
			}
		}
	}
	return false
}

func pinRepairNonEmpty(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case []any:
		return len(value) > 0
	case []string:
		return len(value) > 0
	case map[string]any:
		return len(value) > 0
	case string:
		return value != ""
	default:
		return true
	}
}

func pinRepairDeleteApplied(result map[string]any, expected pinRepairExpected) error {
	total, ok := pinRepairNumber(result["total"])
	requested, reqOK := pinRepairNumber(result["requested"])
	partial, _ := result["partial"].(bool)
	if !ok || !reqOK || total != 2 || requested != 2 || partial || pinRepairNonEmpty(result["survived"]) || pinRepairNonEmpty(result["notFound"]) {
		return fmt.Errorf("delete did not prove exactly two requested primitives removed")
	}
	deletedByKind, _ := result["deletedIds"].(map[string]any)
	seen := map[string]bool{}
	for _, value := range deletedByKind {
		ids, _ := pinRepairArray(value)
		for _, raw := range ids {
			id, _ := raw.(string)
			seen[id] = true
		}
	}
	if !seen[expected.WirePrimitiveID] || !seen[expected.MarkerPrimitiveID] || len(seen) != 2 {
		return fmt.Errorf("delete readback ids do not exactly match the scoped old branch")
	}
	return nil
}

func (s *Server) dispatchPinRepairStep(ctx context.Context, parent protocol.Request, phase, action string, payload map[string]any, budget time.Duration, dispatch dispatchFn) (*protocol.Response, error, pinRepairJournalEntry) {
	req := parent
	req.ID = parent.ID + "-pin-repair-" + phase
	req.Action = action
	req.Payload = payload
	req.TimeoutMs = int(budget / time.Millisecond)
	req.CreatedAt = time.Now().UTC()
	stepCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	started := time.Now().UTC()
	res, err := dispatch(stepCtx, req)
	entry := pinRepairJournalEntry{Phase: phase, Action: action, RequestID: req.ID}
	if res != nil {
		entry.OK, entry.Seq, entry.Result = res.OK, res.Seq, res.Result
		s.audit.Append(fromResponse(started, &req, res))
	} else if err != nil {
		failed := errorResponse(req.ID, "DISPATCH_FAILED", "connector did not respond", err.Error())
		s.audit.Append(fromResponse(started, &req, &failed))
	}
	return res, err, entry
}

func (s *Server) readPinRepairScene(ctx context.Context, parent protocol.Request, phase string, dispatch dispatchFn) (*protocol.Response, pinRepairJournalEntry, error) {
	res, err, entry := s.dispatchPinRepairStep(ctx, parent, phase, "schematic.components.list", map[string]any{
		"includeBBox": true, "includePins": true, "includePinNets": true, "includeWires": true,
	}, pinRepairReadBudget, dispatch)
	if err != nil {
		return res, entry, err
	}
	if res == nil || !res.OK {
		return res, entry, fmt.Errorf("scene read failed")
	}
	if res.Context == nil || res.Context.ProjectUUID == "" || res.Context.DocumentUUID == "" || res.Context.DocumentType != "schematic" {
		return res, entry, fmt.Errorf("scene read lacks an identified active schematic")
	}
	if available, _ := res.Result["wiresAvailable"].(bool); !available {
		return res, entry, fmt.Errorf("wire inventory unavailable")
	}
	return res, entry, nil
}

func pinRepairContextMatches(res *protocol.Response, expected pinRepairExpected) bool {
	return res != nil && res.Context != nil && res.Context.ProjectUUID == expected.ProjectUUID && res.Context.DocumentUUID == expected.DocumentUUID && res.Context.DocumentType == "schematic"
}

func pinRepairOrdered(responses ...*protocol.Response) error {
	if len(responses) == 0 {
		return fmt.Errorf("no FIFO evidence")
	}
	var previous int
	var abandoned int
	for i, response := range responses {
		if response == nil || response.Unordered || response.Seq == nil || response.SeqAbandoned == nil {
			return fmt.Errorf("response %d lacks ordered FIFO evidence", i)
		}
		if i == 0 {
			previous, abandoned = *response.Seq, *response.SeqAbandoned
			continue
		}
		if *response.Seq <= previous || *response.SeqAbandoned != abandoned {
			return fmt.Errorf("FIFO sequence/abandonment changed across protected replacement")
		}
		previous = *response.Seq
	}
	return nil
}

func pinRepairValidateNewMarker(result map[string]any, id string, replacement pinRepairReplacement, x, y float64) error {
	marker, err := pinRepairFindComponent(result, id)
	if err != nil {
		return err
	}
	wantType, _ := pinRepairMarkerType(replacement.Kind)
	mx, xok := pinRepairNumber(marker["x"])
	my, yok := pinRepairNumber(marker["y"])
	rotation, rok := pinRepairNumber(marker["rotation"])
	net, _ := marker["net"].(string)
	if marker["componentType"] != wantType || !xok || !yok || !rok || !pinRepairEqual(mx, x) || !pinRepairEqual(my, y) || !pinRepairEqual(rotation, replacement.Rotation) || net != replacement.Net {
		return fmt.Errorf("new marker readback differs from the generated target")
	}
	return nil
}

func (s *Server) forwardSchematicPinMarkerRepair(ctx context.Context, req protocol.Request, dispatch dispatchFn) (*protocol.Response, error) {
	payload, err := decodePinRepairPayload(req.Payload)
	if err != nil {
		return pinRepairFailure(req, "decode", false, err.Error(), nil, nil), nil
	}
	expected, replacement := payload.Expected, payload.Replacement
	journal := []pinRepairJournalEntry{}

	before, entry, err := s.readPinRepairScene(ctx, req, "before", dispatch)
	journal = append(journal, entry)
	if err != nil {
		return pinRepairFailure(req, "preflight", false, err.Error(), journal, nil), nil
	}
	if !pinRepairContextMatches(before, expected) {
		return pinRepairFailure(req, "preflight", false, "active project/page differs from the generated target", journal, before.Context), nil
	}
	fingerprint, err := schguard.SceneFingerprint(before.Result)
	if err != nil || fingerprint != expected.SceneFingerprint {
		return pinRepairFailure(req, "preflight", false, "source scene fingerprint is stale", journal, map[string]any{"expected": expected.SceneFingerprint, "actual": fingerprint, "error": fmt.Sprint(err)}), nil
	}
	if err := pinRepairValidatePart(before.Result, expected); err != nil {
		return pinRepairFailure(req, "preflight", false, err.Error(), journal, nil), nil
	}
	if err := pinRepairValidateOldWire(before.Result, expected); err != nil {
		return pinRepairFailure(req, "preflight", false, err.Error(), journal, nil), nil
	}
	if err := pinRepairValidateMarker(before.Result, expected); err != nil {
		return pinRepairFailure(req, "preflight", false, err.Error(), journal, nil), nil
	}

	baselineFindings := schguard.AnalyzeWireGeometry(before.Result)
	targetFinding := false
	for _, finding := range baselineFindings {
		if strings.Contains(finding.Type, "unverified") || strings.Contains(finding.Type, "unavailable") {
			return pinRepairFailure(req, "preflight", false, "required geometry is incomplete", journal, baselineFindings), nil
		}
		if pinRepairFindingTargets(finding, expected) {
			targetFinding = true
		}
	}
	if !targetFinding {
		return pinRepairFailure(req, "preflight", false, "the generated target finding is no longer present", journal, baselineFindings), nil
	}
	baselineOutside := schguard.FindingSignatures(baselineFindings, expected.WirePrimitiveID)

	expectedAfter, err := pinRepairCloneResult(before.Result)
	if err != nil {
		return pinRepairFailure(req, "preflight", false, err.Error(), journal, nil), nil
	}
	pinRepairRemove(expectedAfter, expected.MarkerPrimitiveID, expected.WirePrimitiveID)
	endX, endY := pinRepairEndpoint(expected.PinX, expected.PinY, replacement)
	candidateWireID := "__PIN_REPAIR_CANDIDATE__"
	candidateWire := map[string]any{"primitiveId": candidateWireID, "x0": expected.PinX, "y0": expected.PinY, "x1": endX, "y1": endY}
	wires, _ := pinRepairArray(expectedAfter["wires"])
	expectedAfter["wires"] = append(wires, candidateWire)
	candidateFindings := schguard.AnalyzeWireGeometry(expectedAfter)
	for _, finding := range candidateFindings {
		if finding.WirePrimitiveId == candidateWireID {
			return pinRepairFailure(req, "preflight", false, "generated replacement is not geometrically legal", journal, candidateFindings), nil
		}
	}
	if outside := schguard.FindingSignatures(candidateFindings, candidateWireID); !reflect.DeepEqual(outside, baselineOutside) {
		return pinRepairFailure(req, "preflight", false, "generated replacement changes an out-of-scope finding", journal, map[string]any{"before": baselineOutside, "candidate": outside}), nil
	}

	deleteResult, deleteErr, deleteEntry := s.dispatchPinRepairStep(ctx, req, "delete", "schematic.primitives.delete", map[string]any{
		"primitiveIds": []string{expected.WirePrimitiveID, expected.MarkerPrimitiveID},
	}, pinRepairDeleteBudget, dispatch)
	journal = append(journal, deleteEntry)
	if deleteErr != nil || deleteResult == nil || !deleteResult.OK {
		return pinRepairFailure(req, "delete", true, fmt.Sprintf("scoped delete result unknown/failed: %v", deleteErr), journal, deleteResult), nil
	}
	if !pinRepairContextMatches(deleteResult, expected) {
		return pinRepairFailure(req, "delete", true, "document changed during scoped delete", journal, deleteResult.Context), nil
	}
	if err := pinRepairDeleteApplied(deleteResult.Result, expected); err != nil {
		return pinRepairFailure(req, "delete", true, err.Error(), journal, deleteResult.Result), nil
	}

	connectPayload := map[string]any{
		"pinX": expected.PinX, "pinY": expected.PinY, "kind": replacement.Kind,
		"net": replacement.Net, "direction": replacement.Direction,
		"offset": replacement.Offset, "rotation": replacement.Rotation,
	}
	connectResult, connectErr, connectEntry := s.dispatchPinRepairStep(ctx, req, "connect", "schematic.power.connect_pin", connectPayload, pinRepairConnectBudget, dispatch)
	journal = append(journal, connectEntry)
	if connectErr != nil || connectResult == nil || !connectResult.OK {
		return pinRepairFailure(req, "connect", true, fmt.Sprintf("replacement result unknown/failed: %v", connectErr), journal, connectResult), nil
	}
	connectEvidence := req
	connectEvidence.Action = "schematic.power.connect_pin"
	connectEvidence.Payload = connectPayload
	if effectFromResponse(&connectEvidence, connectResult) == effectNotLanded {
		return pinRepairFailure(req, "connect", true, "connector reported a partial/not-applied replacement", journal, connectResult.Result), nil
	}
	if !pinRepairContextMatches(connectResult, expected) {
		return pinRepairFailure(req, "connect", true, "document changed during replacement", journal, connectResult.Context), nil
	}
	newWireID, _ := connectResult.Result["wirePrimitiveId"].(string)
	newMarkerID, _ := connectResult.Result["flagPrimitiveId"].(string)
	if newWireID == "" || newMarkerID == "" || newWireID == newMarkerID || newWireID == expected.WirePrimitiveID || newMarkerID == expected.MarkerPrimitiveID {
		return pinRepairFailure(req, "connect", true, "replacement did not return distinct fresh wire/marker ids", journal, connectResult.Result), nil
	}

	after, afterEntry, err := s.readPinRepairScene(ctx, req, "after", dispatch)
	journal = append(journal, afterEntry)
	if err != nil {
		return pinRepairFailure(req, "readback", true, err.Error(), journal, nil), nil
	}
	if !pinRepairContextMatches(after, expected) {
		return pinRepairFailure(req, "readback", true, "active project/page changed before verification", journal, after.Context), nil
	}
	if err := pinRepairOrdered(before, deleteResult, connectResult, after); err != nil {
		return pinRepairFailure(req, "readback", true, err.Error(), journal, nil), nil
	}
	if pinRepairHasPrimitive(after.Result, expected.WirePrimitiveID) || pinRepairHasPrimitive(after.Result, expected.MarkerPrimitiveID) {
		return pinRepairFailure(req, "readback", true, "old branch primitives still exist", journal, nil), nil
	}
	if err := pinRepairValidatePart(after.Result, expected); err != nil {
		return pinRepairFailure(req, "readback", true, err.Error(), journal, nil), nil
	}
	if err := pinRepairValidateNewMarker(after.Result, newMarkerID, replacement, endX, endY); err != nil {
		return pinRepairFailure(req, "readback", true, err.Error(), journal, nil), nil
	}
	proposed := map[string]any{"primitiveId": newWireID, "x0": expected.PinX, "y0": expected.PinY, "x1": endX, "y1": endY}
	if err := schguard.VerifyWirePresent(after.Result, proposed); err != nil {
		return pinRepairFailure(req, "readback", true, err.Error(), journal, nil), nil
	}
	if err := schguard.CompareWireTopology(expectedAfter, after.Result); err != nil {
		return pinRepairFailure(req, "readback", true, err.Error(), journal, nil), nil
	}

	afterFindings := schguard.AnalyzeWireGeometry(after.Result)
	for _, finding := range afterFindings {
		if strings.Contains(finding.Type, "unverified") || strings.Contains(finding.Type, "unavailable") || finding.WirePrimitiveId == newWireID {
			return pinRepairFailure(req, "readback", true, "replacement has invalid or incomplete geometry", journal, afterFindings), nil
		}
	}
	afterOutside := schguard.FindingSignatures(afterFindings, newWireID)
	if !reflect.DeepEqual(afterOutside, baselineOutside) {
		return pinRepairFailure(req, "readback", true, "out-of-scope findings changed", journal, map[string]any{"before": baselineOutside, "after": afterOutside}), nil
	}
	beforeScoped, err := schguard.SceneFingerprint(before.Result, expected.WirePrimitiveID, expected.MarkerPrimitiveID)
	if err != nil {
		return pinRepairFailure(req, "readback", true, err.Error(), journal, nil), nil
	}
	afterScoped, err := schguard.SceneFingerprint(after.Result, newWireID, newMarkerID)
	if err != nil || beforeScoped != afterScoped {
		return pinRepairFailure(req, "readback", true, "an out-of-scope object changed", journal, map[string]any{"before": beforeScoped, "after": afterScoped, "error": fmt.Sprint(err)}), nil
	}

	removed := []string{expected.WirePrimitiveID, expected.MarkerPrimitiveID}
	sort.Strings(removed)
	return &protocol.Response{
		Envelope:     protocol.Envelope{ID: req.ID, Type: protocol.TypeResponse, Version: req.Version, WindowID: req.WindowID, CreatedAt: time.Now().UTC()},
		OK:           true,
		Context:      after.Context,
		Seq:          after.Seq,
		SeqAbandoned: after.SeqAbandoned,
		Result: map[string]any{
			"verified":                true,
			"partial":                 false,
			"removedPrimitiveIds":     removed,
			"wirePrimitiveId":         newWireID,
			"flagPrimitiveId":         newMarkerID,
			"endPoint":                map[string]any{"x": endX, "y": endY},
			"direction":               replacement.Direction,
			"offset":                  replacement.Offset,
			"rotation":                replacement.Rotation,
			"preexistingFindings":     len(baselineOutside),
			"rangeOutsideFingerprint": afterScoped,
			"journal":                 journal,
		},
	}, nil
}
