package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const eprj3Fixture = "../eprj3/testdata/synthetic-minimal"

func TestProjectInspectEprj3JSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"project", "inspect-eprj3", eprj3Fixture, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var rep struct {
		Mode   string         `json:"mode"`
		Totals map[string]int `json:"totals"`
		Errors int            `json:"errors"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Mode != "project" || rep.Totals["vias"] != 1 || rep.Totals["tracks"] != 2 || rep.Errors != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestProjectInspectEprj3TextAndExitCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"project", "inspect-eprj3", eprj3Fixture}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "totals: components=4 wires=2 nets=4 tracks=2 vias=1 pours=1") {
		t.Fatalf("text output:\n%s", stdout.String())
	}

	// A malformed document is an error finding → non-zero exit, report still printed.
	bad := filepath.Join(t.TempDir(), "bad.epcb2")
	if err := os.WriteFile(bad, []byte(`{"type":"DOCHEAD","ticket":1}||{"docType":"PCB","client":"0123456789abcdef","uuid":"c000000000000001"}|`+"\n"+`{"type":"VIA",`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"project", "inspect-eprj3", bad}, &stdout, &stderr); code == 0 {
		t.Fatal("malformed input must exit non-zero")
	}
	if !strings.Contains(stdout.String(), "[error] malformed-line bad.epcb2:2") {
		t.Fatalf("text output:\n%s", stdout.String())
	}

	// An unusable path is a plain error.
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"project", "inspect-eprj3", filepath.Join(t.TempDir(), "nope.txt")}, &stdout, &stderr); code == 0 {
		t.Fatal("missing input must exit non-zero")
	}
}
