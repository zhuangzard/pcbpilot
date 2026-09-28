package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
)

func TestIntentDeriveOfflineCLI(t *testing.T) {
	dir := t.TempDir()
	outPath, mdPath, simPath := filepath.Join(dir, "intent.json"), filepath.Join(dir, "intent.md"), filepath.Join(dir, "sim.json")
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"intent", "derive",
		"--connectivity", filepath.Join(simFixture, "sch-905bb85957eaf435.json"),
		"--connectivity", filepath.Join(simFixture, "sch-950ae6609e91d753.json"),
		"--values", filepath.Join(simFixture, "values.json"),
		"--models-lib", "../../.agents/skills/pcbpilot/references/power-models.json",
		"--out", outPath, "--report", mdPath, "--sim-out", simPath, "--strict"})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var doc intent.Intent
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Generator != "pcbpilot intent derive" || doc.Nets["SW"] == nil || doc.Nets["SW"].NetClass != "SWITCH" {
		t.Fatalf("intent header/nets: %s", doc.Generator)
	}
	if !strings.Contains(doc.Sources.Sim, simPath) || len(doc.Sources.Schematic) != 3 {
		t.Fatalf("sources %+v", doc.Sources)
	}
	if !strings.Contains(stderr.String(), "intent: 9 blocks") {
		t.Fatalf("summary: %s", stderr.String())
	}
	for _, p := range []string{mdPath, simPath} {
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Fatalf("%s not written: %v", p, err)
		}
	}
	// The written sim feeds a second, sim-only derivation.
	stdout.Reset()
	root = newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"intent", "derive", "--sim", simPath, "--models-lib", "../../.agents/skills/pcbpilot/references/power-models.json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("sim-only: %v\n%s", err, stderr.String())
	}
	var doc2 intent.Intent
	if err := json.Unmarshal(stdout.Bytes(), &doc2); err != nil || doc2.Nets["+3V3"] == nil {
		t.Fatalf("sim-only stdout: %v", err)
	}
}

func TestIntentDeriveFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"intent", "derive", "--connectivity", "x.json", "--pages", "P1"},
		{"intent", "derive", "--values", "v.json"},
		{"intent", "derive", "--connectivity", "x.json", "--switch", "SW1=maybe"},
		{"intent", "derive", "--sim", "missing.json"},
		{"intent", "derive", "--spec", "missing.json", "--sim", "missing.json"},
	} {
		var out bytes.Buffer
		root := newRootCmd(&out, &out)
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

// A reference PCB without its schematic: the netlist comes from the pads,
// the layer count from the board, and the HS plan from the net names.
func TestIntentDeriveFromBoardCLI(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "intent.json")
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"intent", "derive", "--board", "testdata/boards/lckfb-rk3568-4layer.json",
		"--models-lib", "../../.agents/skills/pcbpilot/references/power-models.json", "--out", outPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	var doc intent.Intent
	if err := json.Unmarshal(mustReadFile(t, outPath), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Copper.Layers != 4 || !strings.Contains(doc.Sources.Schematic[0], "PCB pads") {
		t.Fatalf("copper %+v sources %+v", doc.Copper, doc.Sources)
	}
	found := false
	for _, f := range doc.Findings {
		found = found || f.Kind == "netlist-from-board"
	}
	if !found {
		t.Fatal("netlist-from-board finding missing")
	}
	for net, want := range map[string]string{"PCIE20_TXP": "PCIE", "USB3_HOST1_SSTX_P": "USB3", "USB3_HOST1_DP": "USB", "HDMI_0P": "HDMI", "DDR_A_DQS0P": "DDR"} {
		if np := doc.Nets[net]; np == nil || np.Interface != want {
			t.Errorf("%s: %+v, want %s", net, np, want)
		}
	}
	if np := doc.Nets["HDMI_CP"]; np.LengthGroup != "HDMI_LANES" || np.LengthTolMil != 100 {
		t.Errorf("HDMI_CP group %s ±%v", np.LengthGroup, np.LengthTolMil)
	}
	root = newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"intent", "derive", "--board", "x.json", "--connectivity", "y.json"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "--board") {
		t.Fatalf("--board with --connectivity: %v", err)
	}
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
