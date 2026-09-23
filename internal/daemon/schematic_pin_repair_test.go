package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"github.com/zhuangzard/pcbpilot/internal/protocol"
	"github.com/zhuangzard/pcbpilot/internal/schguard"
)

func TestPinRepairStoredRotationMatchesOrientationJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", ".agents", "skills", "pcbpilot", "references", "orientation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var truth struct {
		FrozenTable map[string]map[string]float64 `json:"frozenTable"`
	}
	if err := json.Unmarshal(raw, &truth); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{"power": "power", "ground": "ground", "port": "net_port_bi"}
	for family, kind := range kinds {
		for direction, want := range truth.FrozenTable[family] {
			got, err := pinRepairStoredRotation(kind, direction)
			if err != nil || got != want {
				t.Fatalf("%s/%s rotation=%g want=%g err=%v", kind, direction, got, want, err)
			}
		}
	}
}

func pinRepairScene(t *testing.T, repaired bool) map[string]any {
	t.Helper()
	components := []any{
		map[string]any{
			"componentType": "part", "primitiveId": "d1-live", "designator": "D1",
			"otherProperty": map[string]any{connectivity.ComponentIDProperty: "stable-d1"},
			"bbox":          map[string]any{"minX": 165.0, "minY": 1040.0, "maxX": 205.0, "maxY": 1060.0},
			"pinsAvailable": true,
			"pins":          []any{map[string]any{"pinNumber": "3", "x": 165.0, "y": 1050.0, "rotation": 180.0, "net": "USB_DM"}},
		},
		// Keep one unrelated legacy error in the scene. A successful protected
		// repair must preserve its exact signature rather than merely preserving
		// the total error count.
		map[string]any{
			"componentType": "part", "primitiveId": "legacy-live", "designator": "X9",
			"otherProperty": map[string]any{connectivity.ComponentIDProperty: "stable-legacy"},
			"bbox":          map[string]any{"minX": 300.0, "minY": -10.0, "maxX": 320.0, "maxY": 10.0},
			"pinsAvailable": true,
			"pins":          []any{map[string]any{"pinNumber": "1", "x": 300.0, "y": 0.0, "rotation": 180.0, "net": "OLD"}},
		},
	}
	wires := []any{map[string]any{"primitiveId": "legacy-wire", "x0": 300.0, "y0": 0.0, "x1": 340.0, "y1": 0.0}}
	if repaired {
		components = append(components, map[string]any{"componentType": "netport", "primitiveId": "marker-new", "x": 145.0, "y": 1050.0, "rotation": 180.0, "net": "USB_DM"})
		wires = append(wires, map[string]any{"primitiveId": "wire-new", "x0": 165.0, "y0": 1050.0, "x1": 145.0, "y1": 1050.0})
	} else {
		components = append(components, map[string]any{"componentType": "netport", "primitiveId": "marker-old", "x": 245.0, "y": 1050.0, "rotation": 0.0, "net": "USB_DM"})
		wires = append(wires, map[string]any{"primitiveId": "wire-old", "x0": 165.0, "y0": 1050.0, "x1": 245.0, "y1": 1050.0})
	}
	return map[string]any{"components": components, "wires": wires, "wiresAvailable": true}
}

func pinRepairRequest(t *testing.T, before map[string]any) protocol.Request {
	t.Helper()
	fingerprint, err := schguard.SceneFingerprint(before)
	if err != nil {
		t.Fatal(err)
	}
	payload := pinRepairPayload{
		Expected: pinRepairExpected{
			ProjectUUID: "project", DocumentUUID: "document", SceneFingerprint: fingerprint,
			ComponentID: "stable-d1", ComponentPrimitiveID: "d1-live", Designator: "D1",
			PinNumber: "3", PinX: 165, PinY: 1050, PinRotation: 180, Net: "USB_DM",
			WirePrimitiveID: "wire-old", WireX0: 165, WireY0: 1050, WireX1: 245, WireY1: 1050,
			MarkerPrimitiveID: "marker-old", MarkerType: "netport", MarkerX: 245, MarkerY: 1050, MarkerRotation: 0,
		},
		Replacement: pinRepairReplacement{Kind: "net_port_bi", Net: "USB_DM", Direction: "left", Offset: 20, Rotation: 180},
	}
	raw, _ := json.Marshal(payload)
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	return protocol.Request{Envelope: protocol.Envelope{ID: "repair", Version: "v1", WindowID: "window"}, Action: "schematic.pin.repair_marker", TimeoutMs: 120000, Payload: values}
}

type pinRepairDispatchMode string

const (
	pinRepairDispatchOK          pinRepairDispatchMode = "ok"
	pinRepairDispatchPartial     pinRepairDispatchMode = "partial-delete"
	pinRepairDispatchConnectFail pinRepairDispatchMode = "connect-fail"
	pinRepairDispatchConnectPart pinRepairDispatchMode = "connect-partial"
	pinRepairDispatchOutsideEdit pinRepairDispatchMode = "outside-edit"
)

func pinRepairDispatcher(t *testing.T, before, after map[string]any, mode pinRepairDispatchMode, writes *int) dispatchFn {
	t.Helper()
	seq, reads := 0, 0
	return func(_ context.Context, req protocol.Request) (*protocol.Response, error) {
		seq++
		n, abandoned := seq, 0
		response := &protocol.Response{Envelope: protocol.Envelope{ID: req.ID}, OK: true, Seq: &n, SeqAbandoned: &abandoned, Context: &protocol.Context{ProjectUUID: "project", DocumentUUID: "document", DocumentType: "schematic"}}
		switch req.Action {
		case "schematic.components.list":
			reads++
			response.Result = before
			if reads > 1 {
				response.Result = after
				if mode == pinRepairDispatchOutsideEdit {
					response.Result = pinRepairClone(t, after)
					components := response.Result["components"].([]any)
					response.Result["components"] = append(components, map[string]any{"componentType": "netport", "primitiveId": "unexpected", "x": 900.0, "y": 900.0, "rotation": 0.0, "net": "OTHER"})
				}
			}
		case "schematic.primitives.delete":
			*writes++
			if mode == pinRepairDispatchPartial {
				response.Result = map[string]any{"requested": 2.0, "total": 1.0, "partial": true, "deletedIds": map[string]any{"wires": []any{"wire-old"}}, "survived": map[string]any{"components": []any{"marker-old"}}}
			} else {
				response.Result = map[string]any{"requested": 2.0, "total": 2.0, "deletedIds": map[string]any{"wires": []any{"wire-old"}, "components": []any{"marker-old"}}}
			}
		case "schematic.power.connect_pin":
			*writes++
			if mode == pinRepairDispatchConnectFail {
				return nil, errors.New("connector timeout")
			}
			response.Result = map[string]any{"wirePrimitiveId": "wire-new", "flagPrimitiveId": "marker-new", "endPoint": map[string]any{"x": 145.0, "y": 1050.0}, "rotation": 180.0}
			if mode == pinRepairDispatchConnectPart {
				response.Result["partial"] = true
			}
		default:
			t.Fatalf("unexpected nested action %s", req.Action)
		}
		return response, nil
	}
}

func pinRepairClone(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(value)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestProtectedPinMarkerRepairPreservesUnrelatedLegacyFindings(t *testing.T) {
	before, after := pinRepairScene(t, false), pinRepairScene(t, true)
	request := pinRepairRequest(t, before)
	writes := 0
	server := New(Options{})
	response, err := server.forwardSchematicGeometry(context.Background(), request, pinRepairDispatcher(t, before, after, pinRepairDispatchOK, &writes))
	if err != nil || !response.OK || writes != 2 || response.Result["verified"] != true || response.Result["partial"] != false {
		t.Fatalf("protected repair failed: writes=%d response=%+v err=%v", writes, response, err)
	}
	if response.Result["wirePrimitiveId"] != "wire-new" || response.Result["flagPrimitiveId"] != "marker-new" {
		t.Fatal("actual replacement ids were not returned")
	}
	if n, _ := response.Result["preexistingFindings"].(int); n == 0 {
		t.Fatal("fixture did not prove preservation with unrelated legacy findings")
	}
}

func TestProtectedPinMarkerRepairRejectsStaleSourceBeforeWrite(t *testing.T) {
	before, after := pinRepairScene(t, false), pinRepairScene(t, true)
	request := pinRepairRequest(t, before)
	request.Payload["expected"].(map[string]any)["sceneFingerprint"] = strings.Repeat("0", 64)
	writes := 0
	server := New(Options{})
	response, err := server.forwardSchematicGeometry(context.Background(), request, pinRepairDispatcher(t, before, after, pinRepairDispatchOK, &writes))
	if err != nil || response.OK || writes != 0 || response.Result["partial"] == true || response.Result["phase"] != "preflight" {
		t.Fatalf("stale source was not rejected before mutation: writes=%d response=%+v err=%v", writes, response, err)
	}
}

func TestProtectedPinMarkerRepairRejectsTamperedMarkerRotationBeforeWrite(t *testing.T) {
	before, after := pinRepairScene(t, false), pinRepairScene(t, true)
	request := pinRepairRequest(t, before)
	request.Payload["replacement"].(map[string]any)["rotation"] = 0.0
	writes := 0
	server := New(Options{})
	response, err := server.forwardSchematicGeometry(context.Background(), request, pinRepairDispatcher(t, before, after, pinRepairDispatchOK, &writes))
	if err != nil || response.OK || writes != 0 || response.Result["partial"] == true || response.Result["phase"] != "decode" {
		t.Fatalf("tampered marker orientation was not rejected before mutation: writes=%d response=%+v err=%v", writes, response, err)
	}
}

func TestProtectedPinMarkerRepairReportsPartialWithoutContinuing(t *testing.T) {
	for _, mode := range []pinRepairDispatchMode{pinRepairDispatchPartial, pinRepairDispatchConnectFail, pinRepairDispatchConnectPart, pinRepairDispatchOutsideEdit} {
		t.Run(string(mode), func(t *testing.T) {
			before, after := pinRepairScene(t, false), pinRepairScene(t, true)
			request := pinRepairRequest(t, before)
			writes := 0
			server := New(Options{})
			response, err := server.forwardSchematicGeometry(context.Background(), request, pinRepairDispatcher(t, before, after, mode, &writes))
			if err != nil || response.OK || response.Result["partial"] != true {
				t.Fatalf("%s did not report a scoped partial: writes=%d response=%+v err=%v", mode, writes, response, err)
			}
			if mode == pinRepairDispatchPartial && writes != 1 {
				t.Fatalf("partial delete continued to connect: writes=%d", writes)
			}
		})
	}
}
