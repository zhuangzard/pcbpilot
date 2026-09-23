package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

func TestLayoutLintLegacyGateIsDiagnosticOnly(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"service":"pcbpilot","version":"v0.1.0","windows":[{"windowId":"w1"}]}`)
			return
		}
		var req struct {
			Action string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch req.Action {
		case "pcb.components.list":
			fmt.Fprint(w, `{"ok":true,"result":{"components":[]}}`)
		case "pcb.outline.get":
			fmt.Fprint(w, `{"ok":true,"result":{"bbox":{"minX":0,"minY":0,"maxX":100,"maxY":100}}}`)
		default:
			fmt.Fprint(w, `{"ok":true,"result":{}}`)
		}
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &appConfig{host: u.Hostname(), ports: fmt.Sprintf("%d-%d", port, port), project: "gate-compat"}
	var stdout bytes.Buffer
	err = runPcbLayoutLint(cfg, "w1", 8, true, pcbLayoutGateOpts{
		gate: true, project: cfg.project, minScore: 101, maxCrossings: -1,
	}, &stdout, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("a missed score threshold must remain diagnostic: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), `"pass": false`) {
		t.Fatalf("the historical threshold result should still be visible: %s", stdout.String())
	}
	st, err := workflow.Load(cfg.project)
	if err != nil {
		t.Fatal(err)
	}
	if st.Has(workflow.StagePreRoutePassed) {
		t.Fatal("layout-lint --gate must not write a routing authorization stage")
	}
}
