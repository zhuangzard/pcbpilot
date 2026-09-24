package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

// A stateful fake page: every write appends its wire, reads report the page.
type fakePage struct {
	reads, writes int
	seq, aband    int
	wires         []any
	failWrite     bool
}

func (p *fakePage) dispatch(_ context.Context, req protocol.Request) (*protocol.Response, error) {
	p.seq++
	n := p.seq
	r := &protocol.Response{OK: true, Seq: &n, SeqAbandoned: &p.aband, Context: &protocol.Context{ProjectUUID: "p1", DocumentUUID: "d1", DocumentType: "schematic"}}
	if req.Action == "schematic.components.list" {
		p.reads++
		var comps []any
		_ = json.Unmarshal([]byte(`[{"componentType":"part","primitiveId":"usbc1","designator":"USBC1","bbox":{"minX":529.5,"maxX":580.5,"minY":949.5,"maxY":1080.5},"pinsAvailable":true,"pins":[{"pinNumber":"A7","x":590,"y":1020,"rotation":0},{"pinNumber":"A6","x":590,"y":1040,"rotation":0}]}]`), &comps)
		r.Result = map[string]any{"components": comps, "wires": append([]any(nil), p.wires...), "wiresAvailable": true}
		return r, nil
	}
	p.writes++
	if p.failWrite {
		return &protocol.Response{OK: false, Seq: &n, SeqAbandoned: &p.aband, Error: &protocol.ErrorInfo{Code: "EDA_CALL_FAILED", Message: "boom"}}, nil
	}
	pts := req.Payload["points"].([]any)
	a, b := pts[0].([]any), pts[1].([]any)
	p.wires = append(p.wires, map[string]any{"x0": a[0], "y0": a[1], "x1": b[0], "y1": b[1]})
	r.Result = map[string]any{"primitiveId": "w"}
	return r, nil
}

func wireReq(y int) protocol.Request {
	var req protocol.Request
	_ = json.Unmarshal([]byte(`{"id":"r","action":"schematic.wire.create","windowId":"w1","payload":{"points":[[590,`+itoa(y)+`],[620,`+itoa(y)+`]]}}`), &req)
	return req
}

func itoa(v int) string { b, _ := json.Marshal(v); return string(b) }

func reusedFlag(t *testing.T, res *protocol.Response) bool {
	t.Helper()
	if res == nil || !res.OK {
		t.Fatalf("write rejected: %+v", res)
	}
	g, _ := res.Result["geometryGuard"].(map[string]any)
	v, _ := g["beforeReused"].(bool)
	return v
}

func TestGeometryCacheReusesAfterReadForTheNextWrite(t *testing.T) {
	s := New(Options{})
	page := &fakePage{}
	res1, _ := s.forwardSchematicGeometry(context.Background(), wireReq(1020), page.dispatch)
	if reusedFlag(t, res1) {
		t.Fatal("first write cannot reuse anything")
	}
	res2, _ := s.forwardSchematicGeometry(context.Background(), wireReq(1040), page.dispatch)
	if !reusedFlag(t, res2) {
		t.Fatal("second back-to-back write should reuse the first write's after-read")
	}
	if page.reads != 3 {
		t.Fatalf("page reads = %d, want 3 (before, after, after) instead of 4", page.reads)
	}
}

func TestGeometryCacheInvalidatedByInterveningTransition(t *testing.T) {
	for _, action := range []string{"document.open", "schematic.component.delete", "debug.exec_js"} {
		s := New(Options{})
		page := &fakePage{}
		reusedFlag(t, mustForward(t, s, wireReq(1020), page))
		if !geometryCacheInvalidates(&protocol.Request{Action: action}) {
			t.Fatalf("%s must invalidate the cached page", action)
		}
		s.geometry.bump("w1")
		if reusedFlag(t, mustForward(t, s, wireReq(1040), page)) || page.reads != 4 {
			t.Fatalf("%s: stale snapshot reused (reads=%d)", action, page.reads)
		}
	}
	if geometryCacheInvalidates(&protocol.Request{Action: "document.current"}) || geometryCacheInvalidates(&protocol.Request{Action: "schematic.components.list"}) {
		t.Fatal("pure reads must not invalidate")
	}
}

func TestGeometryCacheExpires(t *testing.T) {
	s := New(Options{})
	clock := time.Now()
	s.geometry.now = func() time.Time { return clock }
	page := &fakePage{}
	reusedFlag(t, mustForward(t, s, wireReq(1020), page))
	clock = clock.Add(geometryCacheTTL + time.Second)
	if reusedFlag(t, mustForward(t, s, wireReq(1040), page)) {
		t.Fatal("expired snapshot reused")
	}
}

func TestGeometryCacheClearedByFailedWrite(t *testing.T) {
	s := New(Options{})
	page := &fakePage{}
	reusedFlag(t, mustForward(t, s, wireReq(1020), page))
	page.failWrite = true
	if res, _ := s.forwardSchematicGeometry(context.Background(), wireReq(1040), page.dispatch); res.OK {
		t.Fatal("failed write reported OK")
	}
	page.failWrite = false
	if reusedFlag(t, mustForward(t, s, wireReq(1040), page)) {
		t.Fatal("a failed write must leave no snapshot behind")
	}
}

func mustForward(t *testing.T, s *Server, req protocol.Request, page *fakePage) *protocol.Response {
	t.Helper()
	res, err := s.forwardSchematicGeometry(context.Background(), req, page.dispatch)
	if err != nil {
		t.Fatal(err)
	}
	return res
}
