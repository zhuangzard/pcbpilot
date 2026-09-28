package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExtensionRouteAndActivitySink(t *testing.T) {
	srv := New(Options{Version: "0.7.0-test"})
	srv.Handle("/ext/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	var got []Activity
	srv.OnActivity(func(a Activity) { got = append(got, a) })
	mux := srv.routes(61832)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ext/x", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("extension route not mounted: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	body := `{"action":"system.health","outputDir":"/tmp/work","clientId":"host:1:test"}`
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/action", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("action: %d %s", rec.Code, rec.Body)
	}
	if len(got) != 1 || got[0].Action != "system.health" || got[0].OutputDir != "/tmp/work" || got[0].ClientID != "host:1:test" {
		t.Fatalf("activity = %+v", got)
	}
}

func TestActivityFromKeepsOnlyVerdicts(t *testing.T) {
	a := activityFrom(auditEntry{Action: "pcb.drc.check", Payload: map[string]any{"secret": "x"},
		Result: map[string]any{"passed": false, "violations": []any{1, 2, 3}, "big": map[string]any{"k": 1}}})
	if a.Result["passed"] != false || a.Result["violations"] != 3 {
		t.Fatalf("verdict fields: %+v", a.Result)
	}
	if _, ok := a.Result["big"]; ok {
		t.Fatal("non-verdict result fields must be dropped")
	}
}
