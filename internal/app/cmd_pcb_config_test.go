package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestPcbConfigTypedCommands(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		action  string
		payload map[string]any
	}{
		{[]string{"get"}, "pcb.config.get", nil},
		{[]string{"net-color", "--net", "+5V", "--color", "#FF8000", "--dry-run"}, "pcb.net.color.set", map[string]any{"net": "+5V", "color": "#FF8000", "dryRun": true}},
		{[]string{"clearance", "--name", "copperThickness1oz", "--track-to-track", "6", "--dry-run"}, "pcb.config.set", map[string]any{"kind": "clearance", "name": "copperThickness1oz", "trackToTrack": float64(6), "unit": "mil", "dryRun": true}},
		{[]string{"track", "--name", "PWR", "--copy-from", "copperThickness1oz", "--min", "8", "--default", "20"}, "pcb.config.set", map[string]any{"kind": "track", "name": "PWR", "copyFrom": "copperThickness1oz", "min": float64(8), "default": float64(20), "unit": "mil", "dryRun": false}},
		{[]string{"via", "--name", "viaSize", "--min-outer", "0.6096", "--min-hole", "0.3048", "--unit", "mm"}, "pcb.config.set", map[string]any{"kind": "via", "name": "viaSize", "minOuter": 0.6096, "minHole": 0.3048, "unit": "mm", "dryRun": false}},
		{[]string{"bind", "--class", "PWR_Class", "--track-rule", "PWR"}, "pcb.config.set", map[string]any{"kind": "bind", "netClass": "PWR_Class", "trackRule": "PWR", "dryRun": false}},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			var got struct {
				Action  string         `json:"action"`
				Payload map[string]any `json:"payload"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/health" {
					fmt.Fprint(w, `{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`)
					return
				}
				if r.URL.Path != "/action" {
					http.NotFound(w, r)
					return
				}
				_ = json.NewDecoder(r.Body).Decode(&got)
				fmt.Fprint(w, `{"ok":true,"result":{"verified":true,"dryRun":true}}`)
			}))
			defer srv.Close()
			host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
			cfg := &appConfig{host: host, ports: port + "-" + port}
			var stdout, stderr bytes.Buffer
			cmd := newPcbCmd(cfg, &stdout, &stderr)
			cmd.SetArgs(append([]string{"config"}, tc.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if got.Action != tc.action || !reflect.DeepEqual(got.Payload, tc.payload) {
				t.Fatalf("request=%+v want %s %+v", got, tc.action, tc.payload)
			}
		})
	}
}

func TestPcbConfigRejectsBadCLIInputWithoutDispatch(t *testing.T) {
	for _, args := range [][]string{
		{"track", "--name", "PWR"}, {"track", "--min", "8"}, {"track", "--name", "x", "--min", "NaN"},
		{"track", "--name", "x", "--min", "+Inf"}, {"track", "--name", "x", "--min", "0"},
		{"via", "--name", "viaSize", "--min-hole", "-1"}, {"clearance", "--name", "x", "--track-to-track", "6", "--unit", "inch"},
		{"bind", "--class", "PWR_Class"}, {"bind", "--class", " ", "--track-rule", "PWR"},
		{"net-color", "--net", "+5V", "--color", "red"}, {"net-color", "--net", "+5V", "--color", "#FFF"},
	} {
		cfg, captured, cleanup := newCapturingDaemon(t)
		var out bytes.Buffer
		cmd := newPcbCmd(cfg, &out, &out)
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(append([]string{"config"}, args...))
		err := cmd.Execute()
		cleanup()
		if err == nil || captured.action != "" {
			t.Fatalf("args=%v err=%v action=%s", args, err, captured.action)
		}
	}
}

func TestPcbConfigReadbackVerdict(t *testing.T) {
	for _, tc := range []struct {
		result       string
		dry, success bool
	}{
		{`{"ok":true,"result":{"verified":true}}`, false, true},
		{`{"ok":true,"result":{"dryRun":true}}`, true, true},
		{`{"ok":true,"result":{"verified":false}}`, false, false},
		{`{"ok":true,"result":{"verified":true,"partial":true}}`, false, false},
		{`{"ok":true,"result":{"verified":true,"writeFailed":true}}`, false, false},
		{`{"ok":true,"result":{}}`, false, false},
		{`{"ok":false,"result":{"verified":true}}`, false, false},
		{`{`, false, false},
	} {
		if got := checkPcbConfigResponse([]byte(tc.result), tc.dry); (got == nil) != tc.success {
			t.Fatalf("%s: %v", tc.result, got)
		}
	}
}

func TestPcbConfigExportCanRestoreRules(t *testing.T) {
	spec, err := parsePcbDrcRulesSetSpec([]byte(`{"ok":true,"result":{"ruleConfiguration":{"Physics":{}},"netRules":[],"classes":[],"configurationName":"custom"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(spec) != 2 || spec["ruleConfiguration"] == nil || spec["netRules"] == nil {
		t.Fatalf("spec=%#v", spec)
	}
}
