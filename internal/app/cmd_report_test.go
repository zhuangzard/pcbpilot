package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/designreport"
)

// TestReportDesignCLI: intent derive --report-dir publishes v1 (pre-layout);
// `report design` with a changed intent publishes v2 with a changelog; an
// explicit existing version is refused without --force.
func TestReportDesignCLI(t *testing.T) {
	dir := t.TempDir()
	rep := filepath.Join(dir, "reports", "esp32")
	intentPath, simPath := filepath.Join(dir, "intent.json"), filepath.Join(dir, "sim.json")
	models := "../../.agents/skills/pcbpilot/references/power-models.json"
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"intent", "derive",
		"--connectivity", filepath.Join(simFixture, "sch-905bb85957eaf435.json"),
		"--connectivity", filepath.Join(simFixture, "sch-950ae6609e91d753.json"),
		"--values", filepath.Join(simFixture, "values.json"),
		"--models-lib", models, "--out", intentPath, "--sim-out", simPath,
		"--report-dir", rep, "--report-name", "ESP32 mini"})
	t.Setenv("SOURCE_DATE_EPOCH", "1790000000")
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	for _, f := range []string{"v1/report.html", "v1/report.md", "v1/report.json", "v1/assets/charts/margins.svg", "v1/manifest.json", "v1/data/intent.json", "v1/data/sim.json", "pcbpilot-report-ESP32-mini-v1.zip", "index.json", "CHANGELOG.md"} {
		if st, err := os.Stat(filepath.Join(rep, f)); err != nil || st.Size() == 0 {
			t.Fatalf("%s not written: %v", f, err)
		}
	}
	// v2: the intent declares a 1 A +3V3 load.
	b, _ := os.ReadFile(intentPath)
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	nets := doc["nets"].(map[string]any)
	n := nets["+3V3"].(map[string]any)
	n["currentA"], n["currentSource"] = 1.0, "declared"
	b2, _ := json.MarshalIndent(doc, "", "  ")
	intent2 := filepath.Join(dir, "intent-1A.json")
	if err := os.WriteFile(intent2, b2, 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		stdout.Reset()
		stderr.Reset()
		root := newRootCmd(&stdout, &stderr)
		root.SetArgs(append([]string{"report", "design", "--out-dir", rep, "--models", models}, args...))
		return root.Execute()
	}
	if err := run("--intent", intent2, "--sim", simPath); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	raw, _ := os.ReadFile(filepath.Join(rep, "index.json"))
	idx, err := designreport.ParseIndex(raw)
	if err != nil || len(idx.Versions) != 2 || idx.Versions[1].Changes == nil || idx.Versions[1].Changes.Previous != "v1" {
		t.Fatalf("index %+v %v", idx, err)
	}
	log, _ := os.ReadFile(filepath.Join(rep, "CHANGELOG.md"))
	if !strings.Contains(string(log), "## v2") || !strings.Contains(string(log), "变更 intent") {
		t.Fatalf("changelog:\n%s", log)
	}
	if err := run("--intent", intent2, "--sim", simPath, "--version", "v2"); err == nil {
		t.Fatal("existing v2 overwritten without --force")
	}
	if err := run("--intent", filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("missing input file accepted")
	}
	if err := run("--intent", intent2, "--image", "sch=nofile.png"); err == nil {
		t.Fatal("missing image accepted")
	}
}
