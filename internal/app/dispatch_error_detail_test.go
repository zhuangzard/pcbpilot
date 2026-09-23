package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// errorDaemon answers /action with one canned failure envelope.
func errorDaemon(t *testing.T, body string) (*appConfig, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`))
		case "/action":
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	hostPort := strings.TrimPrefix(srv.URL, "http://")
	host, portStr, _ := strings.Cut(hostPort, ":")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse test server port: %v", err)
	}
	return &appConfig{host: host, ports: fmt.Sprintf("%d-%d", port, port)}, srv.Close
}

// error.message is the category; error.detail is the reason. Callers that render
// only the error line (`sch autoconnect` prints one row per pin) used to show
// "schematic geometry guard failed (preflight)" with no way to tell which rule
// rejected the write — the reason was carried on the wire but never decoded.
func TestActionErrorCarriesDetail(t *testing.T) {
	cfg, cleanup := errorDaemon(t, `{"ok":false,"error":{"code":"SCHEMATIC_GEOMETRY_INVALID",
		"message":"schematic geometry guard failed (preflight)",
		"detail":"Rejected before write: replan. pin-exit-direction (R3:2): pin 2 outward rotation 0 requires its first wire segment to leave outward"}}`)
	defer cleanup()

	_, err := requestAction(cfg, "schematic.power.connect_pin", "w1", map[string]any{})
	if err == nil {
		t.Fatal("expected the failure envelope to produce an error")
	}
	for _, want := range []string{
		"schematic geometry guard failed (preflight)",
		"requires its first wire segment to leave outward",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%s", want, err.Error())
		}
	}
	var ae *actionError
	if !errors.As(err, &ae) || ae.Code != "SCHEMATIC_GEOMETRY_INVALID" {
		t.Errorf("error.code must survive so STALE_READ-style branching still works: %#v", err)
	}
}

// No detail, or a detail already quoted in the message, must not change the text
// callers match on today.
func TestActionErrorWithoutDetailIsUnchanged(t *testing.T) {
	cfg, cleanup := errorDaemon(t, `{"ok":false,"error":{"code":"STALE_READ","message":"pcb.components.list is stale"}}`)
	defer cleanup()
	_, err := requestAction(cfg, "pcb.components.list", "w1", map[string]any{})
	if err == nil || err.Error() != "pcb.components.list failed: pcb.components.list is stale" {
		t.Errorf("unexpected error text: %v", err)
	}
	if !isStaleRead(err) {
		t.Errorf("STALE_READ branching must still match: %v", err)
	}

	dup, cleanupDup := errorDaemon(t, `{"ok":false,"error":{"code":"X","message":"boom: already said","detail":"already said"}}`)
	defer cleanupDup()
	_, err = requestAction(dup, "pcb.components.list", "w1", map[string]any{})
	if err == nil || strings.Count(err.Error(), "already said") != 1 {
		t.Errorf("a detail already inside message must not be appended twice: %v", err)
	}
}
