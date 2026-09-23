package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type reloadFixture struct {
	t                  *testing.T
	srv                *httptest.Server
	actions            []string
	openCount          int
	openTimeoutMs      int
	opened             bool
	closed             bool
	failReopen         bool
	failClose          bool
	noActiveAfterClose bool
}

func newReloadFixture(t *testing.T, failReopen bool) (*reloadFixture, *appConfig) {
	t.Helper()
	fx := &reloadFixture{t: t, failReopen: failReopen}
	fx.srv = httptest.NewServer(http.HandlerFunc(fx.serveHTTP))
	t.Cleanup(fx.srv.Close)
	host, port, _ := strings.Cut(strings.TrimPrefix(fx.srv.URL, "http://"), ":")
	return fx, &appConfig{host: host, ports: port + "-" + port}
}

func (fx *reloadFixture) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`))
		return
	}
	var req struct {
		Action    string         `json:"action"`
		Payload   map[string]any `json:"payload"`
		TimeoutMs int            `json:"timeoutMs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fx.t.Error(err)
		return
	}
	fx.actions = append(fx.actions, req.Action)
	result := map[string]any{}
	ctx := map[string]any{
		"projectUuid": "project-1", "documentUuid": "pcb-target",
		"documentType": "pcb", "tabId": "tab-target",
	}
	ok := true
	errorMessage := "fixture failure"
	switch req.Action {
	case "document.current":
		if fx.closed && !fx.opened {
			if fx.noActiveAfterClose {
				ok = false
				ctx = map[string]any{"projectUuid": "project-1"}
				errorMessage = "No active document."
			} else {
				ctx["documentUuid"], ctx["documentType"], ctx["tabId"] = "other-doc", "schematic", "tab-other"
			}
		}
	case "pcb.save":
		// Successful checkpoint before close.
	case "pcb.components.list":
		result["count"] = 69
	case "document.close":
		if got := req.Payload["uuid"]; got != "pcb-target" {
			fx.t.Errorf("close uuid=%v", got)
		}
		if got := req.Payload["tabId"]; got != "tab-target" {
			fx.t.Errorf("close tabId=%v", got)
		}
		if fx.failClose {
			ok = false
			errorMessage = "host close failed"
			break
		}
		fx.closed = true
		ctx["documentUuid"], ctx["documentType"], ctx["tabId"] = "other-doc", "schematic", "tab-other"
		result = map[string]any{"closed": true, "uuid": "pcb-target", "tabId": "tab-target", "splitScreenId": "target-split"}
	case "document.open":
		fx.openCount++
		fx.openTimeoutMs = req.TimeoutMs
		if got := req.Payload["uuid"]; got != "pcb-target" {
			fx.t.Errorf("open uuid=%v", got)
		}
		if got := req.Payload["splitScreenId"]; got != "target-split" {
			fx.t.Errorf("open splitScreenId=%v, want preserved target-split", got)
		}
		if fx.failReopen {
			ok = false
			errorMessage = "host open failed"
		} else {
			fx.opened = true
			result = map[string]any{"tabId": "tab-reopened", "ready": true}
		}
	default:
		fx.t.Errorf("unexpected action %s", req.Action)
	}
	response := map[string]any{"ok": ok, "result": result, "context": ctx}
	if !ok {
		response["error"] = map[string]any{"code": "EDA_CALL_FAILED", "message": errorMessage}
	}
	_ = json.NewEncoder(w).Encode(response)
}

func TestReloadDocumentTreatsNoActiveDocumentAsClosed(t *testing.T) {
	fx, cfg := newReloadFixture(t, false)
	fx.noActiveAfterClose = true
	if _, err := reloadDocumentByUUID(cfg, "w1", "pcb-target"); err != nil {
		t.Fatal(err)
	}
	if fx.openCount != 1 {
		t.Fatalf("document.open count=%d, want exactly one after the last tab closed", fx.openCount)
	}
}

func TestReloadDocumentPreservesTargetSplitAndWaitsForClose(t *testing.T) {
	fx, cfg := newReloadFixture(t, false)
	docType, err := reloadDocumentByUUID(cfg, "w1", "pcb-target")
	if err != nil {
		t.Fatal(err)
	}
	if docType != "pcb" {
		t.Fatalf("docType=%q", docType)
	}
	if fx.openCount != 1 {
		t.Fatalf("document.open count=%d, want exactly one", fx.openCount)
	}
	if fx.openTimeoutMs != 15_000 {
		t.Fatalf("document.open timeoutMs=%d, want bounded 15000", fx.openTimeoutMs)
	}
	want := []string{"document.current", "pcb.save", "document.close", "document.current", "document.open", "document.current", "pcb.components.list", "pcb.components.list"}
	if strings.Join(fx.actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions=%v, want %v", fx.actions, want)
	}
}

func TestReloadDocumentCloseFailureDoesNotOpen(t *testing.T) {
	fx, cfg := newReloadFixture(t, false)
	fx.failClose = true
	_, err := reloadDocumentByUUID(cfg, "w1", "pcb-target")
	if err == nil || !strings.Contains(err.Error(), "close document failed") {
		t.Fatalf("error=%v", err)
	}
	if fx.openCount != 0 {
		t.Fatalf("document.open count=%d, want zero after failed close", fx.openCount)
	}
}

func TestReloadDocumentOpenFailureDoesNotRetryAndReportsRecovery(t *testing.T) {
	fx, cfg := newReloadFixture(t, true)
	_, err := reloadDocumentByUUID(cfg, "w1", "pcb-target")
	if err == nil {
		t.Fatal("expected reopen failure")
	}
	if fx.openCount != 1 {
		t.Fatalf("document.open count=%d, want exactly one", fx.openCount)
	}
	message := err.Error()
	for _, text := range []string{"target tab is closed", "automatic retry was suppressed", "project tree"} {
		if !strings.Contains(message, text) {
			t.Fatalf("error missing %q: %s", text, message)
		}
	}
}
