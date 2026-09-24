package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocListAndOpenRecoverWhenProjectHasNoActiveDocument(t *testing.T) {
	active := ""
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1","context":{"projectUuid":"project-1","projectName":"target"}}]}`))
			return
		}
		var req struct {
			Action  string         `json:"action"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		calls = append(calls, req.Action)
		context := map[string]any{"projectUuid": "project-1", "projectName": "target"}
		result := map[string]any{}
		switch req.Action {
		case "document.current":
			if active == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"ok": false, "result": result, "context": context,
					"error": map[string]any{"code": "EDA_CALL_FAILED", "message": "No active document."},
				})
				return
			}
			context["documentUuid"] = active
			context["documentType"] = "pcb"
			context["tabId"] = "tab-pcb"
			result = map[string]any{
				"uuid": active, "tabId": "tab-pcb", "documentType": "pcb",
				"parentProjectUuid": "project-1",
			}
		case "schematic.pages.list":
			result["pages"] = []any{map[string]any{
				"uuid": "page-1", "name": "P1", "parentSchematicUuid": "schematic-1",
			}}
		case "pcb.documents.list":
			result["pcbs"] = []any{map[string]any{
				"uuid": "pcb-target", "name": "PCB1", "parentProjectUuid": "project-1",
			}}
		case "document.open":
			if req.Payload["uuid"] != "pcb-target" {
				t.Errorf("document.open uuid=%v", req.Payload["uuid"])
			}
			active = "pcb-target"
			result = map[string]any{"tabId": "tab-pcb", "ready": true}
		case "pcb.components.list", "pcb.components.count":
			result = map[string]any{"components": []any{map[string]any{"primitiveId": "c1"}}, "count": 1}
		default:
			t.Errorf("unexpected action %s", req.Action)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result, "context": context})
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	cfg := &appConfig{host: host, ports: port + "-" + port, project: "target"}

	var listed bytes.Buffer
	ls := newDocCmd(cfg, &listed, &listed)
	ls.SetArgs([]string{"ls", "--json"})
	if err := ls.Execute(); err != nil {
		t.Fatalf("doc ls with no active tab: %v\n%s", err, listed.String())
	}
	var inventory struct {
		ActiveUUID string        `json:"activeUuid"`
		Documents  []openableDoc `json:"documents"`
	}
	if err := json.Unmarshal(listed.Bytes(), &inventory); err != nil {
		t.Fatalf("decode doc ls: %v\n%s", err, listed.String())
	}
	if inventory.ActiveUUID != "" || len(inventory.Documents) != 2 {
		t.Fatalf("inventory=%+v", inventory)
	}
	for _, doc := range inventory.Documents {
		if doc.Active {
			t.Fatalf("document unexpectedly active without an editor tab: %+v", doc)
		}
	}

	var opened bytes.Buffer
	open := newDocCmd(cfg, &opened, &opened)
	open.SetArgs([]string{"open", "pcb-target", "--json"})
	if err := open.Execute(); err != nil {
		t.Fatalf("doc open recovery: %v\n%s", err, opened.String())
	}
	if active != "pcb-target" {
		t.Fatalf("active=%q, want pcb-target", active)
	}
	openAt, confirmedAt := -1, -1
	for i, action := range calls {
		if action == "document.open" {
			openAt = i
		}
		if openAt >= 0 && i > openAt && action == "document.current" {
			confirmedAt = i
			break
		}
	}
	if openAt < 0 || confirmedAt < 0 {
		t.Fatalf("open was not followed by fresh document.current: %v", calls)
	}
}

func TestDiscoverDocsDoesNotMaskOtherDocumentCurrentFailures(t *testing.T) {
	listed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1","context":{"projectName":"target"}}]}`))
			return
		}
		var req struct {
			Action string `json:"action"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Action != "document.current" {
			listed = true
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": map[string]any{"code": "EDA_CALL_FAILED", "message": "Failed to read current document info."},
		})
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	cfg := &appConfig{host: host, ports: port + "-" + port, project: "target"}
	if _, _, _, err := discoverDocs(cfg, ""); err == nil {
		t.Fatal("arbitrary document.current failure was accepted")
	}
	if listed {
		t.Fatal("inventory reads must not run after an unrecognized current-document failure")
	}
}

func TestDocOpenRequiresFreshCurrentAfterNoActiveRecovery(t *testing.T) {
	opened, probed := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1","context":{"projectName":"target"}}]}`))
			return
		}
		var req struct {
			Action string `json:"action"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := map[string]any{}
		switch req.Action {
		case "document.current":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": false, "result": result,
				"error": map[string]any{"code": "EDA_CALL_FAILED", "message": "No active document."},
			})
			return
		case "schematic.pages.list":
			result["pages"] = []any{}
		case "pcb.documents.list":
			result["pcbs"] = []any{map[string]any{"uuid": "pcb-target", "name": "PCB1"}}
		case "document.open":
			opened++
		case "pcb.components.list", "pcb.components.count":
			probed++
		default:
			t.Errorf("unexpected action %s", req.Action)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	cfg := &appConfig{host: host, ports: port + "-" + port, project: "target"}
	cmd := newDocCmd(cfg, &bytes.Buffer{}, &bytes.Buffer{})
	cmd.SetArgs([]string{"open", "pcb-target"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("doc open accepted a tab that fresh document.current did not confirm")
	}
	if opened != 1 || probed != 0 {
		t.Fatalf("opened=%d probed=%d; settle must not run before identity confirmation", opened, probed)
	}
}
