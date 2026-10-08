package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

const kicadFixBoard = "../kicad/testdata/fab/fab_board.kicad_pcb"

func runKiCadCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs(append([]string{"kicad"}, args...))
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestKiCadLcscCheckListsMissing(t *testing.T) {
	out, _, err := runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, "--check")
	if !errors.Is(err, errActionFailed) {
		t.Fatalf("want non-zero (errActionFailed), got %v", err)
	}
	for _, s := range []string{"8 footprints, 6 assembled, 1 missing LCSC, 1 invalid", "U1", "Q1", `"AO3400A"`} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, "R2 ") || strings.Contains(out, "H1 ") {
		t.Errorf("DNP/excluded parts must not be listed:\n%s", out)
	}
}

func TestKiCadLcscCheckJSON(t *testing.T) {
	out, _, _ := runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, "--check", "--json")
	var v struct {
		Missing []struct{ Ref string } `json:"missing"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Missing) != 1 || v.Missing[0].Ref != "U1" {
		t.Fatalf("%v %s", err, out)
	}
}

func TestKiCadLcscModesExclusive(t *testing.T) {
	if _, _, err := runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, "--check", "--search", "x"); err == nil {
		t.Fatal("two modes must be rejected")
	}
	if _, _, err := runKiCadCmd(t, "lcsc", "--pcb", kicadFixBoard, "--set", "R1=25744"); err == nil {
		t.Fatal("bad C-number must be rejected")
	}
}

func TestKiCadFabGateMessage(t *testing.T) {
	t.Setenv("PCBPILOT_KICAD_CLI", "/bin/sh") // never reached: the gate fails first
	_, errOut, err := runKiCadCmd(t, "fab", "--pcb", kicadFixBoard, "--out", t.TempDir())
	if err == nil {
		t.Fatal("want failure")
	}
	for _, s := range []string{"1 placed part(s) without an LCSC part number", "U1", "kicad lcsc", "--no-assembly"} {
		if !strings.Contains(errOut, s) {
			t.Errorf("stderr lacks %q:\n%s", s, errOut)
		}
	}
}

func TestKiCadLcscSearchOffline(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	out, _, err := runKiCadCmd(t, "lcsc", "--search", "100nF 0402", "--offline")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "standard-parts.json") || !strings.Contains(out, "C1525") {
		t.Fatalf("%s", out)
	}
}
