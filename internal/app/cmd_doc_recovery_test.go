package app

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDocGuardRecoversMissingActivePCB(t *testing.T) {
	for _, tc := range []struct {
		name, lookupUUID, lookupProject, selector string
		listFails, lookupFails, wantErr           bool
	}{
		{"empty enumeration", "pcb1", "project1", "PCB1", false, false, false},
		{"failed enumeration", "pcb1", "project1", "PCB1", true, false, false},
		{"uuid selector", "pcb1", "project1", "pcb1", false, false, false},
		{"other target", "pcb1", "project1", "PCB2", false, false, true},
		{"lookup failed", "pcb1", "project1", "PCB1", false, true, true},
		{"tab drift", "pcb2", "project1", "PCB1", false, false, true},
		{"project drift", "pcb1", "project2", "PCB1", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`))
					return
				}
				var req struct {
					Action   string `json:"action"`
					WindowID string `json:"windowId"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				calls = append(calls, req.Action)
				ctx := map[string]any{"documentUuid": "pcb1", "documentType": "pcb", "projectUuid": "project1"}
				result := map[string]any{}
				ok := true
				switch req.Action {
				case "document.current":
					result["uuid"] = "pcb1"
					result["documentType"] = "pcb"
					result["parentProjectUuid"] = "project1"
				case "schematic.pages.list":
					result["pages"] = []any{}
				case "pcb.documents.list":
					result["pcbs"] = []any{}
					ok = !tc.listFails
				case "pcb.board.info":
					ctx["documentUuid"], ctx["projectUuid"] = tc.lookupUUID, tc.lookupProject
					result["pcb"] = map[string]any{"uuid": tc.lookupUUID, "name": "PCB1"}
					ok = !tc.lookupFails
				default:
					t.Errorf("unexpected action %s", req.Action)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "result": result, "context": ctx, "error": map[string]any{"code": "EDA_CALL_FAILED", "message": "fixture failure"}})
			}))
			defer srv.Close()
			hostPort := strings.TrimPrefix(srv.URL, "http://")
			host, port, _ := strings.Cut(hostPort, ":")
			cfg := &appConfig{host: host, ports: port + "-" + port, doc: tc.selector}
			err := ensureActiveDoc(cfg, "w1")
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v, wantErr %v", err, tc.wantErr)
			}
			if cfg.doc != tc.selector {
				t.Fatal("discovery mutated the caller's target guard")
			}
			if tc.selector == "pcb1" {
				if len(calls) != 1 || calls[0] != "document.current" {
					t.Fatalf("UUID should use only live identity: %v", calls)
				}
			} else if len(calls) != 5 || calls[4] != "pcb.board.info" {
				t.Fatalf("unexpected recovery calls: %v", calls)
			}
		})
	}
}

func TestDocGuardLiveUUIDRequiresMatchingIdentity(t *testing.T) {
	for _, field := range []string{"uuid", "documentType", "parentProjectUuid"} {
		t.Run(field, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`))
					return
				}
				var req struct {
					Action string `json:"action"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				result := map[string]any{}
				ctx := map[string]any{"documentUuid": "pcb1", "documentType": "pcb", "projectUuid": "project1"}
				switch req.Action {
				case "document.current":
					result = map[string]any{"uuid": "pcb1", "documentType": "pcb", "parentProjectUuid": "project1"}
					result[field] = "mismatch"
				case "schematic.pages.list", "pcb.documents.list", "pcb.board.info":
					// Metadata is unavailable, so it cannot authorize a fallback.
				default:
					t.Errorf("unexpected action %s", req.Action)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result, "context": ctx})
			}))
			defer srv.Close()
			host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
			cfg := &appConfig{host: host, ports: port + "-" + port, doc: "pcb1"}
			if err := ensureActiveDoc(cfg, "w1"); err == nil {
				t.Fatal("inconsistent live identity accepted")
			}
		})
	}
}

// Gas Module V5 B v23: the save after 2.5 h of repair failed once with the
// connector gone; saveRetrying waits for it to answer and saves again.
func TestSaveRetryingAfterDisconnect(t *testing.T) {
	var calls []string
	saves := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`))
			return
		}
		var req struct {
			Action string `json:"action"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		calls = append(calls, req.Action)
		ok := true
		if req.Action == "pcb.save" {
			saves++
			ok = saves > 1
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "result": map[string]any{}, "context": map[string]any{"documentUuid": "pcb1", "documentType": "pcb"},
			"error": map[string]any{"code": "CONNECTOR_DISCONNECTED", "message": "connector disconnected before responding"}})
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	cfg := &appConfig{host: host, ports: port + "-" + port}
	if err := saveRetrying(cfg, "w1", "pcb.save", time.Minute, func(time.Duration) {}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "pcb.save,document.current,pcb.save" {
		t.Fatalf("calls %v", calls)
	}
	// A save that keeps failing gives up at the budget.
	saves = -1000
	if err := saveRetrying(cfg, "w1", "pcb.save", 0, func(time.Duration) {}); err == nil {
		t.Fatal("expected failure")
	}
}

// Gas Module V5 B: the live daemon, starved at load 115, missed one health
// scan and a 6865 s route died. With daemonWaitBudget set, a request keeps
// scanning until the daemon answers.
func TestDaemonWaitBudget(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}, "context": map[string]any{}})
	})}
	go func() {
		time.Sleep(4 * time.Second)
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return
		}
		_ = srv.Serve(l)
	}()
	defer srv.Close()
	cfg := &appConfig{host: "127.0.0.1", ports: fmt.Sprintf("%d-%d", port, port)}
	old := daemonWaitBudget
	defer func() { daemonWaitBudget = old }()
	daemonWaitBudget = 0
	if _, err := requestAction(cfg, "document.current", "w1", nil); err == nil {
		t.Fatal("no budget: a missing daemon must fail at once")
	}
	daemonWaitBudget = 30 * time.Second
	if _, err := requestAction(cfg, "document.current", "w1", nil); err != nil {
		t.Fatalf("with a budget the late daemon must be found: %v", err)
	}
}
