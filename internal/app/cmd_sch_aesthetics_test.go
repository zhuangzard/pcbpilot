package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runSchCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	root := newRootCmd(&out, &errb)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), errb.String(), err
}

// `sch aesthetics` runs offline on every supported snapshot format, reports
// weight 0 and never fails on a low score.
func TestSchAestheticsCmdOffline(t *testing.T) {
	for _, path := range []string{
		"../../pkg/schaes/testdata/ams1117-lib-layout.json",
		"../../pkg/schaes/testdata/synthetic/esp32-mcu-point-to-point.json",
		"testdata/esp32-v05/sch-950ae6609e91d753.json",
		"../schguard/testdata/d1-pin3-reversed.json",
	} {
		out, errs, err := runSchCLI(t, "sch", "aesthetics", "--snapshot", path, "--json")
		if err != nil {
			t.Fatalf("%s: %v %s", path, err, errs)
		}
		var rep struct {
			Weight   float64               `json:"weight"`
			Verdict  string                `json:"verdict"`
			Metrics  []struct{ ID string } `json:"metrics"`
			Priority []string              `json:"priority"`
		}
		if err := json.Unmarshal([]byte(out), &rep); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if rep.Weight != 0 || len(rep.Metrics) != 18 || len(rep.Priority) != 3 || rep.Verdict == "" {
			t.Fatalf("%s: %+v", path, rep)
		}
		if !strings.Contains(errs, "applied 0") {
			t.Fatalf("stderr must state the applied weight: %s", errs)
		}
	}
	out, _, err := runSchCLI(t, "sch", "aesthetics", "--snapshot", "testdata/esp32-v05/sch-950ae6609e91d753.json", "--style", "auto")
	if err != nil || !strings.Contains(out, "report-only, weight 0.00") || !strings.Contains(out, "skipped: source carries no wires") ||
		!strings.Contains(out, "auto: complexity") || !strings.Contains(out, "uart") {
		t.Fatalf("text report: %v\n%s", err, out)
	}
	if _, _, err := runSchCLI(t, "sch", "aesthetics", "--snapshot", "testdata/esp32-v05/sch-950ae6609e91d753.json", "--style", "nope"); err == nil {
		t.Fatal("unknown style accepted")
	}
}

func TestSchBusCreateDryRunValidatesOffline(t *testing.T) {
	out, _, err := runSchCLI(t, "sch", "bus", "create", "--name", "D[0:7]", "--points", "400,600,400,300", "--points", "400,300,700,300", "--line-width", "2", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	var dr struct {
		DryRun  bool           `json:"dryRun"`
		Action  string         `json:"action"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(out), &dr); err != nil || !dr.DryRun || dr.Action != "schematic.bus.create" || dr.Payload["busName"] != "D[0:7]" {
		t.Fatalf("dry run: %v %s", err, out)
	}
	if line, ok := dr.Payload["line"].([]any); !ok || len(line) != 2 {
		t.Fatalf("two polylines expected: %v", dr.Payload["line"])
	}
	for _, bad := range [][]string{
		{"--name", "A", "--points", "0,0,10,10"},                          // diagonal
		{"--name", "A", "--points", "0,0,0,10", "--points", "50,0,50,10"}, // disjoint
		{"--name", "A", "--points", "0,0"},                                // one point
		{"--name", "A", "--points", "0,0,0,0"},                            // zero length
		{"--name", "", "--points", "0,0,0,10"},
		{"--name", "A", "--points", "0,0,0,10", "--line-width", "11"},
		{"--name", "A", "--points", "0,0,0,10", "--color", "red"},
		{"--name", "A", "--points", "0,x,0,10"},
	} {
		if _, _, err := runSchCLI(t, append([]string{"sch", "bus", "create", "--dry-run"}, bad...)...); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
	out, errs, err := runSchCLI(t, "sch", "bus", "create", "--name", "DATA", "--points", "0,0,0,10", "--dry-run")
	if err != nil || !strings.Contains(out, "NAME[a:b]") {
		t.Fatalf("unconventional name must warn, not fail: %v %s %s", err, out, errs)
	}
	out, _, err = runSchCLI(t, "sch", "bus", "delete", "--ids", "b1,b2", "--dry-run")
	if err != nil || !strings.Contains(out, "schematic.bus.delete") {
		t.Fatalf("delete dry run: %v %s", err, out)
	}
}

func TestSchBusCandidatesOffline(t *testing.T) {
	out, _, err := runSchCLI(t, "sch", "bus", "candidates", "--snapshot", "testdata/esp32-v05/sch-950ae6609e91d753.json")
	if err != nil || !strings.Contains(out, "U0_UART") || !strings.Contains(out, "U0RXD, U0TXD") {
		t.Fatalf("%v\n%s", err, out)
	}
}
