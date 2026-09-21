package app

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectArchiveValidationAndNoOverwrite(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	w, _ := z.Create("project.epru")
	w.Write([]byte("native fixture"))
	z.Close()
	value := map[string]any{"uuid": "project-1", "format": "epro2", "size": float64(b.Len()), "base64": base64.StdEncoding.EncodeToString(b.Bytes())}
	out := filepath.Join(t.TempDir(), "nested", "test.epro2")
	report, err := writeProjectArchive(value, out)
	if err != nil {
		t.Fatal(err)
	}
	if report["restoreVerified"] != false || report["zipIntegrityVerified"] != true {
		t.Fatal(report)
	}
	if _, err = writeProjectArchive(value, out); err == nil {
		t.Fatal("overwrote existing archive")
	}
	got, _ := os.ReadFile(out)
	if !bytes.Equal(got, b.Bytes()) {
		t.Fatal("archive bytes changed")
	}
	value["size"] = float64(1)
	if _, err = writeProjectArchive(value, out+"2"); err == nil {
		t.Fatal("accepted inconsistent size")
	}
	value["size"] = float64(3)
	value["base64"] = base64.StdEncoding.EncodeToString([]byte("bad"))
	if _, err = writeProjectArchive(value, out+"3"); err == nil {
		t.Fatal("accepted non-ZIP")
	}
}

func TestProjectTransferCLIRejectsUnsafeOrAmbiguousArguments(t *testing.T) {
	for _, args := range [][]string{
		{"project", "open", "--project-uuid", "p", "--window", "w"},
		{"project", "open", "--project-uuid", "p", "--uuid", "d", "--allow-discard-unsaved", "--window", "w"},
		{"project", "export", "--project-uuid", "p", "--window", "w", "--out", "bad.zip"},
		{"project", "export", "--project-uuid", "p", "--out", filepath.Join(t.TempDir(), "a.epro2")},
	} {
		var out, err bytes.Buffer
		if Run(args, &out, &err) == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestProjectTransferDispatchesTypedActions(t *testing.T) {
	for _, operation := range []string{"open", "export"} {
		t.Run(operation, func(t *testing.T) {
			for _, missing := range []bool{false, true} {
				calls := 0
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/health" {
						w.Write([]byte(`{"service":"easyeda-agent","windows":[{"windowId":"w"}]}`))
						return
					}
					var req struct {
						Action   string         `json:"action"`
						WindowID string         `json:"windowId"`
						Payload  map[string]any `json:"payload"`
					}
					json.NewDecoder(r.Body).Decode(&req)
					calls++
					if req.Action != "project."+operation || req.WindowID != "w" || req.Payload["projectUuid"] != "p" {
						t.Errorf("unexpected request: %+v", req)
					}
					if _, ok := req.Payload["code"]; ok {
						t.Error("script payload")
					}
					if operation == "open" && (req.Payload["allowDiscardUnsaved"] != true || req.Payload["pageUuid"] != "page") {
						t.Errorf("missing open guards: %+v", req)
					}
					if missing {
						json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"code": "UNKNOWN_ACTION", "message": "upgrade connector"}})
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"uuid": "p"}})
				}))
				host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
				_, _, err := projectTransferRequest(&appConfig{host: host, ports: port + "-" + port}, "w", operation, "p", "page")
				srv.Close()
				if (err != nil) != missing {
					t.Fatalf("error=%v missing=%v", err, missing)
				}
				if calls != 1 {
					t.Fatalf("unexpected retries or fallback: %d", calls)
				}
			}
		})
	}
}
