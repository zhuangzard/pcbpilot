package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

const simFixture = "../../pkg/powersim/testdata/esp32mini"

func TestSimPowerOfflineCLI(t *testing.T) {
	dir := t.TempDir()
	outPath, mdPath, cirPath := filepath.Join(dir, "sim.json"), filepath.Join(dir, "sim.md"), filepath.Join(dir, "sim.cir")
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"sim", "power",
		"--connectivity", filepath.Join(simFixture, "sch-905bb85957eaf435.json"),
		"--connectivity", filepath.Join(simFixture, "sch-950ae6609e91d753.json"),
		"--values", filepath.Join(simFixture, "values.json"),
		"--models-lib", "../../.agents/skills/pcbpilot/references/power-models.json",
		"--out", outPath, "--report", mdPath, "--spice", cirPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var out powersim.Output
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Generator != "pcbpilot sim power" || out.Scenarios[len(out.Scenarios)-1] != "worst" {
		t.Fatalf("output header %+v", out.Scenarios)
	}
	peak := out.Results[1]
	if v := peak.Nets["+3V3"].Voltage; v < 3.30 || v > 3.33 {
		t.Fatalf("+3V3 = %.4f", v)
	}
	if !strings.Contains(stderr.String(), "sim power peak:") {
		t.Fatalf("summary missing: %s", stderr.String())
	}
	for _, p := range []string{mdPath, cirPath} {
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Fatalf("%s not written: %v", p, err)
		}
	}
}

func TestSimPowerFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"sim", "power", "--connectivity", "x.json", "--pages", "P1"},
		{"sim", "power", "--values", "v.json"},
		{"sim", "power", "--connectivity", "x.json", "--switch", "SW1=maybe"},
	} {
		var out bytes.Buffer
		root := newRootCmd(&out, &out)
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	sw, err := parseSimSwitches([]string{"SW1=closed", "SW2=open"})
	if err != nil || !sw["SW1"] || sw["SW2"] {
		t.Fatalf("switches %v %v", sw, err)
	}
}
