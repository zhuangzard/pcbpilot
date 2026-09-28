package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

func startDaemonVersion(t *testing.T, version string) string {
	t.Helper()
	port := freePort(t)
	srv := New(Options{Host: "127.0.0.1", PortStart: port, PortEnd: port, Version: version, ArtifactDir: t.TempDir(), AuditDir: t.TempDir()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, io.Discard) }()
	base := fmt.Sprintf("127.0.0.1:%d", port)
	waitForHealth(t, "http://"+base+"/health")
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return base
}

func dialConnectorVersion(t *testing.T, base, windowID, connectorVersion string) (*websocket.Conn, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	dctx, dcancel := context.WithTimeout(ctx, 3*time.Second)
	defer dcancel()
	c, _, err := websocket.Dial(dctx, "ws://"+base+"/eda", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	reg := protocol.Register{Type: protocol.TypeRegister, WindowID: windowID, ConnectorVersion: connectorVersion, EasyEDAVersion: "test", Capabilities: []string{"schematic.v1"}}
	if err := wsjson.Write(dctx, c, reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	go echoRequests(ctx, c)
	waitForWindow(t, base, windowID)
	return c, func() { cancel(); c.Close(websocket.StatusNormalClosure, "bye") }
}

func actionStatus(t *testing.T, base, action, window string) (int, protocol.Response) {
	t.Helper()
	body := fmt.Sprintf(`{"action":%q,"windowId":%q,"payload":{}}`, action, window)
	resp, err := http.Post("http://"+base+"/action", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out protocol.Response
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// designAction is a plain design read the connector answers (echo) with no
// daemon-side guard — enough to prove what the gate lets through.
func designAction(t *testing.T) string {
	const name = "schematic.components.list"
	if !knownActions[name] || connectorGateAllowed(name) {
		t.Fatalf("%s must be a known, gated action", name)
	}
	return name
}

// The connector version gate (user decision 2026-09-28): a misaligned
// connector may only run diagnosis actions; a matching connector unblocks
// without restarting the daemon.
func TestConnectorGateBlocksAndUnblocksOnReconnect(t *testing.T) {
	t.Setenv(AllowVersionSkewEnv, "")
	base := startDaemonVersion(t, "v0.6.1")
	_, closeOld := dialConnectorVersion(t, base, "w-old", "0.6.0")
	design := designAction(t)

	code, resp := actionStatus(t, base, design, "w-old")
	if code != http.StatusConflict || resp.Error == nil || resp.Error.Code != "CONNECTOR_VERSION_MISMATCH" {
		t.Fatalf("misaligned connector must be refused: %d %+v", code, resp)
	}
	for _, want := range []string{"1.", "2.", "3.", "Import", "允许外部交互", "0.6.1"} {
		if !strings.Contains(resp.Error.Message+resp.Error.Detail, want) {
			t.Fatalf("refusal must carry the 3 import steps (%q missing): %s / %s", want, resp.Error.Message, resp.Error.Detail)
		}
	}
	for _, allowed := range []string{"document.current", "project.current", "system.page_reload"} {
		if code, resp := actionStatus(t, base, allowed, "w-old"); code != http.StatusOK || !resp.OK {
			t.Fatalf("%s is a diagnosis action and must pass: %d %+v", allowed, code, resp)
		}
	}
	// /health annotates the window and the gate lifts when a matching connector connects.
	if w := waitForWindow(t, base, "w-old"); w.ConnectorVersionOK == nil || *w.ConnectorVersionOK {
		t.Fatalf("health must flag the misaligned window: %+v", w)
	}
	closeOld()
	_, closeNew := dialConnectorVersion(t, base, "w-new", "0.6.1")
	defer closeNew()
	if code, resp := actionStatus(t, base, design, "w-new"); code != http.StatusOK || !resp.OK {
		t.Fatalf("aligned connector must be unblocked without a daemon restart: %d %+v", code, resp)
	}
}

func TestConnectorGateExemptions(t *testing.T) {
	design := designAction(t)
	cases := []struct{ daemon, connector, env string }{
		{"v0.6.1-3-gabc-dirty", "0.6.0", ""}, // dev daemon
		{"v0.6.1", "0.6.2-dev.1", ""},        // dev connector
		{"v0.6.1", "0.6.0", "1"},             // PCBPILOT_ALLOW_VERSION_SKEW=1
		{"v0.6.1", "0.6.1", ""},              // aligned
	}
	for i, c := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Setenv(AllowVersionSkewEnv, c.env)
			base := startDaemonVersion(t, c.daemon)
			_, closeConn := dialConnectorVersion(t, base, "w", c.connector)
			defer closeConn()
			if code, resp := actionStatus(t, base, design, "w"); code != http.StatusOK || !resp.OK {
				t.Fatalf("%+v must not be gated: %d %+v", c, code, resp)
			}
		})
	}
}

func TestActivityTracksInFlightUnsavedAndSave(t *testing.T) {
	a := newActivityTracker()
	mut := ""
	for name, mutates := range mutatesAction {
		if mutates && strings.HasPrefix(name, "schematic.") && !strings.HasSuffix(name, ".save") {
			mut = name
			break
		}
	}
	a.observe(&protocol.Request{Envelope: protocol.Envelope{WindowID: "w1"}, Action: mut}, true)
	if !a.unsaved["w1"] {
		t.Fatal("a successful mutating action must mark the window unsaved")
	}
	a.observe(&protocol.Request{Envelope: protocol.Envelope{WindowID: "w1"}, Action: "schematic.save"}, false)
	if !a.unsaved["w1"] {
		t.Fatal("a FAILED save must not clear the unsaved mark")
	}
	a.observe(&protocol.Request{Envelope: protocol.Envelope{WindowID: "w1"}, Action: "schematic.save"}, true)
	if a.unsaved["w1"] {
		t.Fatal("a successful save must clear the unsaved mark")
	}
	if idle := a.touch(time.Now()); idle >= 0 {
		t.Fatalf("first touch reports no idle period, got %v", idle)
	}
}
