package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
	"github.com/zhuangzard/pcbpilot/internal/protocol"
	"github.com/zhuangzard/pcbpilot/internal/schguard"
)

func geometryFixture(t *testing.T) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(`{"components":[{"componentType":"part","primitiveId":"usbc1","designator":"USBC1","bbox":{"minX":529.5,"maxX":580.5,"minY":949.5,"maxY":1080.5},"pinsAvailable":true,"pins":[{"pinNumber":"A7","x":590,"y":1020,"rotation":0}]}],"wires":[],"wiresAvailable":true}`), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func geometryRequest(points string) protocol.Request {
	var req protocol.Request
	_ = json.Unmarshal([]byte(`{"id":"req","action":"schematic.wire.create","windowId":"w1","forceReason":"try anyway","forceUnsafe":true,"payload":{"dryRun":true,"points":`+points+`}}`), &req)
	return req
}

func geometryDispatcher(before, after map[string]any, writes *int, switchDoc bool) dispatchFn {
	seq, abandoned := 0, 0
	return func(_ context.Context, req protocol.Request) (*protocol.Response, error) {
		seq++
		n := seq
		r := &protocol.Response{OK: true, Seq: &n, SeqAbandoned: &abandoned, Context: &protocol.Context{ProjectUUID: "p1", DocumentUUID: "d1", DocumentType: "schematic"}}
		if req.Action == "schematic.components.list" {
			r.Result = before
			if *writes > 0 {
				r.Result = after
				if switchDoc {
					r.Context.DocumentUUID = "other"
				}
			}
		} else {
			*writes++
			r.Result = map[string]any{"primitiveId": "new-wire"}
		}
		return r, nil
	}
}

func TestSchematicGeometryRefusesRawWireBeforeDispatch(t *testing.T) {
	s := New(Options{})
	writes := 0
	before := geometryFixture(t)
	res, err := s.forwardSchematicGeometry(context.Background(), geometryRequest(`[[590,1020],[535,1020]]`), geometryDispatcher(before, before, &writes, false))
	if err != nil || res.OK || writes != 0 || res.Error.Code != "SCHEMATIC_GEOMETRY_INVALID" {
		t.Fatalf("invalid raw wire escaped guard: writes=%d res=%+v err=%v", writes, res, err)
	}
	if !schematicGeometrySerializes(ptrRequest(geometryRequest(`[]`))) {
		t.Fatal("fake dryRun flag bypassed serialization")
	}
}

func ptrRequest(req protocol.Request) *protocol.Request { return &req }

func TestSchematicGeometryReadbackCannotReportAPISuccess(t *testing.T) {
	old := wireSettleStep
	wireSettleStep = time.Millisecond
	defer func() { wireSettleStep = old }()
	for _, scenario := range []string{"valid", "bad-after", "wrong-doc", "missing-inventory", "missing-rotation", "not-landed"} {
		t.Run(scenario, func(t *testing.T) {
			s := New(Options{})
			before, after := geometryFixture(t), geometryFixture(t)
			after["wires"] = []any{map[string]any{"x0": 590., "y0": 1020., "x1": 620., "y1": 1020.}}
			if scenario == "bad-after" {
				after["wires"] = []any{map[string]any{"x0": 590., "y0": 1020., "x1": 535., "y1": 1020.}}
			}
			if scenario == "not-landed" {
				after["wires"] = []any{}
			}
			if scenario == "missing-inventory" {
				delete(before, "wiresAvailable")
			}
			if scenario == "missing-rotation" {
				delete(before["components"].([]any)[0].(map[string]any)["pins"].([]any)[0].(map[string]any), "rotation")
			}
			writes := 0
			res, err := s.forwardSchematicGeometry(context.Background(), geometryRequest(`[[590,1020],[620,1020]]`), geometryDispatcher(before, after, &writes, scenario == "wrong-doc"))
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "valid" {
				if !res.OK || writes != 1 {
					t.Fatalf("valid wire rejected: %+v", res)
				}
				return
			}
			if res.OK {
				t.Fatalf("%s accepted", scenario)
			}
			wantWrites := 1
			if scenario == "missing-inventory" || scenario == "missing-rotation" {
				wantWrites = 0
			}
			if writes != wantWrites {
				t.Fatalf("writes=%d want %d", writes, wantWrites)
			}
			if wantWrites == 1 && res.Result["partial"] != true {
				t.Fatal("lost evidence of already-landed mutation")
			}
		})
	}
}

func TestSchematicGeometryHTTPActionCannotBypassPreflight(t *testing.T) {
	base, cleanup := startDaemon(t)
	defer cleanup()
	c := dialConnector(t, base, "guard-window")
	defer c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	seen := make(chan string, 5)
	fixture := geometryFixture(t)
	go func() {
		for {
			var req protocol.Request
			if err := wsjson.Read(ctx, c, &req); err != nil {
				return
			}
			if req.Type != protocol.TypeRequest {
				continue
			}
			seen <- req.Action
			seq, abandoned := 1, 0
			_ = wsjson.Write(ctx, c, protocol.Response{Envelope: protocol.Envelope{ID: req.ID, Type: protocol.TypeResponse}, OK: true, Result: fixture, Context: &protocol.Context{ProjectUUID: "p1", DocumentUUID: "d1", DocumentType: "schematic"}, Seq: &seq, SeqAbandoned: &abandoned})
		}
	}()
	// The connector registration is asynchronous; wait for observable registration.
	for len(getHealth(t, base).Windows) == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("connector never registered")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	resp, err := http.Post("http://"+base+"/action", "application/json", strings.NewReader(`{"action":"schematic.wire.create","windowId":"guard-window","forceReason":"bypass","forceUnsafe":true,"payload":{"dryRun":true,"points":[[590,1020],[535,1020]]}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var result protocol.Response
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Error == nil || result.Error.Code != "SCHEMATIC_GEOMETRY_INVALID" {
		t.Fatalf("raw action bypassed: %+v", result)
	}
	if action := <-seen; action != "schematic.components.list" {
		t.Fatalf("write dispatched before validation: %s", action)
	}
	select {
	case action := <-seen:
		t.Fatalf("unexpected write: %s", action)
	default:
	}
}

func TestSchematicGeometryConnectDefaultsMatchConnector(t *testing.T) {
	for kind, want := range map[string][2]float64{"ground": {0, -30}, "analog_ground": {0, -30}, "protect_ground": {0, -30}, "protective_ground": {0, -30}, "power": {0, 30}, "net_port_in": {-30, 0}, "net_port_bi": {30, 0}} {
		w, err := proposedSchematicWire(protocol.Request{Action: "schematic.power.connect_pin", Payload: map[string]any{"pinX": 0., "pinY": 0., "kind": kind}})
		if err != nil || w["x1"] != want[0] || w["y1"] != want[1] {
			t.Fatalf("%s: %+v %v", kind, w, err)
		}
	}
}

func TestSchematicGeometryRejectsUnexpectedContactDespiteEqualEdges(t *testing.T) {
	for _, junction := range []bool{false, true} {
		s := New(Options{})
		wire := func(x0, y0, x1, y1 float64) any {
			return map[string]any{"x0": x0, "y0": y0, "x1": x1, "y1": y1}
		}
		before := map[string]any{"components": []any{}, "wiresAvailable": true, "wires": []any{wire(-20, 0, 20, 0)}}
		after := map[string]any{"components": []any{}, "wiresAvailable": true, "wires": []any{wire(-20, 0, 20, 0), wire(0, -20, 0, 20)}}
		if junction {
			after["wires"] = []any{wire(-20, 0, 0, 0), wire(0, 0, 20, 0), wire(0, -20, 0, 20)}
		}
		writes := 0
		res, err := s.forwardSchematicGeometry(context.Background(), geometryRequest(`[[0,-20],[0,20]]`), geometryDispatcher(before, after, &writes, false))
		if err != nil || writes != 1 || res.OK == junction {
			t.Fatalf("junction=%v writes=%d res=%+v err=%v", junction, writes, res, err)
		}
		if junction && (res.Result["partial"] != true || !strings.Contains(res.Error.Detail, "wire-contact-topology")) {
			t.Fatalf("unexpected junction lost failure evidence: %+v", res)
		}
	}
}

// A rejection must carry WHY in error.detail, not only inside
// result.geometryGuard. `sch autoconnect` renders one error line per pin, so a
// generic "geometry guard failed (preflight)" leaves the operator unable to tell
// a wrong exit direction from a wire crossing a body.
func TestGeometryFailureDetailNamesTheRule(t *testing.T) {
	findings := []schguard.Finding{
		{Type: "pin-exit-direction", Level: "ERROR", Designator: "R3", Pins: []string{"2"},
			Message: "pin 2 outward rotation 0° requires its first wire segment to leave outward"},
		{Type: "wire-through-body", Level: "ERROR", Designator: "U1", Message: "segment crosses the body"},
	}
	res := geometryFailure(geometryRequest("[]"), "preflight", false, findings, "Rejected before write: replan.")
	if res.Error == nil {
		t.Fatal("expected an error response")
	}
	for _, want := range []string{
		"Rejected before write: replan.",
		"pin-exit-direction (R3:2)",
		"requires its first wire segment to leave outward",
		"wire-through-body (U1)",
	} {
		if !strings.Contains(res.Error.Detail, want) {
			t.Errorf("detail missing %q:\n%s", want, res.Error.Detail)
		}
	}
	// The category stays in message so existing matchers keep working.
	if !strings.Contains(res.Error.Message, "schematic geometry guard failed (preflight)") {
		t.Errorf("message changed: %q", res.Error.Message)
	}
	// findings stay in the structured result for callers that render them fully.
	guard, _ := res.Result["geometryGuard"].(map[string]any)
	if guard == nil || guard["findings"] == nil {
		t.Error("geometryGuard.findings must still be present")
	}
}

func TestGeometryFailureDetailTruncatesAndToleratesNoFindings(t *testing.T) {
	var many []schguard.Finding
	for i := 0; i < 5; i++ {
		many = append(many, schguard.Finding{Type: "pin-exit-direction", Message: "outward"})
	}
	res := geometryFailure(geometryRequest("[]"), "preflight", false, many, "")
	if !strings.Contains(res.Error.Detail, "+2 more") {
		t.Errorf("expected the digest to be capped: %q", res.Error.Detail)
	}
	// nil findings (a read failure, not a rule violation) must not invent detail.
	plain := geometryFailure(geometryRequest("[]"), "preflight", false, nil, "read failed")
	if plain.Error.Detail != "read failed" {
		t.Errorf("detail must be untouched without findings: %q", plain.Error.Detail)
	}
}

// EasyEDA Pro V4 Web acknowledged wire.create before its list showed the wire
// (live 2026-09-24). The guard re-reads, never re-writes, until it appears.
func TestSchematicGeometryWaitsForLaggingWireInventory(t *testing.T) {
	old := wireSettleStep
	wireSettleStep = time.Millisecond
	defer func() { wireSettleStep = old }()
	for _, lag := range []int{1, 3, wireSettleRereads + 1} {
		s := New(Options{})
		before, after := geometryFixture(t), geometryFixture(t)
		after["wires"] = []any{map[string]any{"x0": 620., "y0": 1020., "x1": 590., "y1": 1020.}}
		writes, afterReads := 0, 0
		inner := geometryDispatcher(before, after, &writes, false)
		dispatch := func(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
			res, err := inner(ctx, req)
			if req.Action == "schematic.components.list" && writes > 0 {
				afterReads++
				if afterReads <= lag {
					res.Result = geometryFixture(t)
				}
			}
			return res, err
		}
		res, err := s.forwardSchematicGeometry(context.Background(), geometryRequest(`[[590,1020],[620,1020]]`), dispatch)
		if err != nil || writes != 1 {
			t.Fatalf("lag %d: writes=%d err=%v", lag, writes, err)
		}
		if lag <= wireSettleRereads && !res.OK {
			t.Fatalf("lag %d: lagging inventory rejected: %+v", lag, res)
		}
		if lag > wireSettleRereads && (res.OK || res.Result["partial"] != true) {
			t.Fatalf("lag %d: never-landed wire accepted", lag)
		}
	}
}
