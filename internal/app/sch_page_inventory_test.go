package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSchPageListedInInventoryFailsClosed(t *testing.T) {
	page := func(uuid, name string) map[string]any { return map[string]any{"uuid": uuid, "name": name} }
	bad := map[string]map[string]any{
		"missing field":   {},
		"null":            {"pages": nil},
		"not an array":    {"pages": map[string]any{"uuid": "other"}},
		"string":          {"pages": "none"},
		"non-object item": {"pages": []any{page("other", "P1"), "target"}},
		"item sans uuid":  {"pages": []any{map[string]any{"name": "P1"}}},
		"numeric uuid":    {"pages": []any{map[string]any{"uuid": 7.0}}},
		"empty uuid":      {"pages": []any{page("", "P1")}},
	}
	for label, result := range bad {
		if _, listed, err := schPageListedInInventory(result, "target"); err == nil {
			t.Errorf("%s: err=nil listed=%v, want an error (absence is not proven)", label, listed)
		}
	}
	if _, listed, err := schPageListedInInventory(map[string]any{"pages": []any{}}, "target"); err != nil || listed {
		t.Fatalf("empty array: listed=%v err=%v, want absent", listed, err)
	}
	if _, listed, err := schPageListedInInventory(map[string]any{"pages": []any{page("other", "P1")}}, "target"); err != nil || listed {
		t.Fatalf("other page only: listed=%v err=%v, want absent", listed, err)
	}
	name, listed, err := schPageListedInInventory(map[string]any{"pages": []any{page("other", "P1"), page("target", "P2")}}, "target")
	if err != nil || !listed || name != "P2" {
		t.Fatalf("survivor: name=%q listed=%v err=%v", name, listed, err)
	}
}

// End to end through the command: only a well-formed inventory without the
// target prints "deleted"; everything else is an error.
func TestSchPageDeleteRequiresProvenAbsence(t *testing.T) {
	run := func(pagesJSON string) (string, error) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/health":
				_, _ = w.Write([]byte(`{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`))
			case "/action":
				var body struct {
					Action string `json:"action"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Action == "schematic.pages.list" {
					_, _ = w.Write([]byte(`{"ok":true,"result":` + pagesJSON + `}`))
					return
				}
				_, _ = w.Write([]byte(`{"ok":true,"result":{"ok":true}}`))
			}
		}))
		defer srv.Close()
		host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
		cfg := &appConfig{host: host, ports: port + "-" + port}
		var out bytes.Buffer
		cmd := newSchCmd(cfg, &out, &out)
		cmd.SetArgs([]string{"page-delete", "--page", "target"})
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SilenceUsage = true
		err := cmd.Execute()
		return out.String(), err
	}
	for label, pagesJSON := range map[string]string{
		"missing":   `{}`,
		"null":      `{"pages":null}`,
		"malformed": `{"pages":[{"name":"P1"}]}`,
		"survivor":  `{"pages":[{"uuid":"target","name":"P2"}]}`,
	} {
		out, err := run(pagesJSON)
		if err == nil || strings.Contains(out, "deleted page") {
			t.Errorf("%s: err=%v out=%q, want a refusal without a deleted line", label, err, out)
		}
	}
	out, err := run(`{"pages":[{"uuid":"other","name":"P1"}]}`)
	if err != nil || !strings.Contains(out, "deleted page target") {
		t.Fatalf("proven absence: err=%v out=%q", err, out)
	}
}
