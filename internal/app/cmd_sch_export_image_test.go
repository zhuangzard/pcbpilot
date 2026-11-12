package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestExportImageFormatFollowsOutExtension(t *testing.T) {
	for _, c := range []struct{ format, out, want string }{
		{"", "page.png", "png"}, {"", "p.PDF", "pdf"}, {"", "page", ""}, {"", "x.txt", ""},
		{"svg", "page.svg", "svg"}, {"png", "", "png"},
	} {
		if got, err := exportImageFormat(c.format, c.out); err != nil || got != c.want {
			t.Fatalf("%q %q → %q %v, want %q", c.format, c.out, got, err, c.want)
		}
	}
	if _, err := exportImageFormat("svg", "page.png"); err == nil {
		t.Fatal("svg into .png must be refused")
	}
}

// Clear and export cost grows with the page; a hidden, throttled tab pushed the
// default 20s budget into DISPATCH_FAILED during v1.13.0 acceptance, so both
// commands send their own round-trip budget to the daemon.
func TestSchClearAndExportSendHeavyBudgets(t *testing.T) {
	var mu sync.Mutex
	budgets := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`))
		case "/action":
			var body struct {
				Action    string `json:"action"`
				TimeoutMs int    `json:"timeoutMs"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			budgets[body.Action] = body.TimeoutMs
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	cfg := &appConfig{host: host, ports: port + "-" + port}
	for _, args := range [][]string{{"export-image", "--format", "svg"}, {"clear"}} {
		var out bytes.Buffer
		cmd := newSchCmd(cfg, &out, &out)
		cmd.SetArgs(args)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		_ = cmd.Execute() // the fake result fails clear verification; only the budget matters
	}
	if budgets["schematic.export.image"] != 90000 || budgets["schematic.page.clear"] != 120000 {
		t.Fatalf("heavy budgets not sent: %v", budgets)
	}
}
