package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

const postFixture = "../../pkg/postsim/testdata/esp32mini"

// TestSimPostLayoutCLI: the ESP32 board replay through the CLI writes
// post.json, the report, per-layer heat maps, the Elmer deck (skipped: no
// ElmerSolver) and merges feedback; `report design --post` then publishes a
// packaged version with chapter 6A, the heat maps and the data files.
func TestSimPostLayoutCLI(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "post")
	var stdout, stderr bytes.Buffer
	t.Setenv("PATH", t.TempDir()) // no ElmerSolver
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"sim", "post-layout",
		"--board", filepath.Join(postFixture, "board.json"), "--sim", filepath.Join(postFixture, "sim.json"),
		"--intent", filepath.Join(postFixture, "intent.json"), "--plan", filepath.Join(postFixture, "plan-ir.json"),
		"--models-lib", "../../.agents/skills/pcbpilot/references/power-models.json",
		"--out", filepath.Join(out, "post.json"), "--report", filepath.Join(out, "post.md"),
		"--svg-dir", filepath.Join(out, "heatmaps"), "--feedback", filepath.Join(out, "feedback.json"), "--elmer-check"})
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "post-layout PASS") {
		t.Fatalf("stdout %q", stdout.String())
	}
	raw, _ := os.ReadFile(filepath.Join(out, "post.json"))
	var res postsim.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Elmer == nil || res.Elmer.Status != "skipped" || len(res.Compare) != 6 || len(res.Maps) != 8 {
		t.Fatalf("elmer %+v compare %d maps %d", res.Elmer, len(res.Compare), len(res.Maps))
	}
	for _, f := range []string{"heatmaps/temp-TOP.svg", "heatmaps/current-IN2.svg", "elmer/case.sif", "elmer/mesh/mesh.nodes", "feedback.json"} {
		if st, err := os.Stat(filepath.Join(out, f)); err != nil || st.Size() == 0 {
			t.Fatalf("%s: %v", f, err)
		}
	}
	md, _ := os.ReadFile(filepath.Join(out, "post.md"))
	if !strings.Contains(string(md), "![TOP temperature](heatmaps/temp-TOP.svg)") {
		t.Fatalf("post.md does not link the maps")
	}

	// report design --post: chapter 6A + packaged heat maps and data.
	rep := filepath.Join(dir, "reports", "esp32")
	stdout.Reset()
	stderr.Reset()
	root = newRootCmd(&stdout, &stderr)
	t.Setenv("SOURCE_DATE_EPOCH", "1790000000")
	root.SetArgs([]string{"report", "design", "--out-dir", rep, "--project-name", "ESP32 mini",
		"--models", "../../.agents/skills/pcbpilot/references/power-models.json",
		"--sim", filepath.Join(postFixture, "sim.json"), "--board", filepath.Join(postFixture, "board.json"),
		"--post", filepath.Join(out, "post.json")})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	v1 := filepath.Join(rep, "v1")
	rmd, _ := os.ReadFile(filepath.Join(v1, "report.md"))
	for _, want := range []string{"## 6A 设计后仿真验证 Post-layout verification", "数据 [data/post.json](data/post.json)", "设计后仿真（post-layout） | **PASS**", "TOP 温度", "](assets/"} {
		if !strings.Contains(string(rmd), want) {
			t.Fatalf("report.md lacks %q", want)
		}
	}
	var man reportManifest
	mb, _ := os.ReadFile(filepath.Join(v1, "manifest.json"))
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatal(err)
	}
	roles := map[string]int{}
	for _, f := range man.Files {
		roles[f.Role]++
		if st, err := os.Stat(filepath.Join(v1, f.Rel)); err != nil || int(st.Size()) != f.Bytes || f.SHA256 == "" || f.Producer == "" {
			t.Fatalf("manifest entry %+v: %v", f, err)
		}
	}
	if roles["image:heat"] != 8 || roles["data:post"] != 1 || roles["elmer"] == 0 || roles["chart"] == 0 || roles["report-html"] != 1 {
		t.Fatalf("roles %v", roles)
	}
	zr, err := zip.OpenReader(filepath.Join(rep, man.Zip))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != len(man.Files)+1 || !strings.HasPrefix(zr.File[0].Name, "pcbpilot-report-ESP32-mini-v1/") {
		t.Fatalf("zip has %d entries (%s), manifest %d", len(zr.File), zr.File[0].Name, len(man.Files))
	}
}

// TestAutoPostSimHook: pcb auto run --post-sim verifies the run's own
// board.routed.json with the stackup's plane layers declared, writes
// post.json / heat maps and merges the copper feedback into feedback.json.
func TestAutoPostSimHook(t *testing.T) {
	dir := t.TempDir()
	cp := func(src, dst string) {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, dst), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cp(filepath.Join(postFixture, "board.json"), "board.routed.json")
	cp(filepath.Join(postFixture, "plan-ir.json"), "plan.json")
	if err := os.WriteFile(filepath.Join(dir, "feedback.json"), []byte(`{"schemaVersion":1,"source":"pcb auto run","items":[{"id":"fb-1","kind":"mcu-pin-swap"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &pcbauto.Board{Rules: pcbauto.DefaultRules()}
	rep := &pcbauto.Report{Result: &pcbauto.Result{Stackup: &pcbauto.Stackup{Stack: []pcbauto.StackLayer{
		{ID: 1}, {ID: 15, Kind: pcbauto.KindPlane, Nets: []string{"GND"}}, {ID: 16, Kind: pcbauto.KindPlane, Nets: []string{"+3V3", "VSYS_5V"}}, {ID: 2}}}}}
	var stdout, stderr bytes.Buffer
	post, err := autoPostSim(dir, filepath.Join(postFixture, "sim.json"), filepath.Join(postFixture, "intent.json"), b, rep, &stdout, &stderr)
	if err != nil || post == "" {
		t.Fatalf("%v %q\n%s", err, post, stderr.String())
	}
	raw, _ := os.ReadFile(post)
	var res postsim.Result
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Settings.Planes["IN1"] != "GND" || res.Settings.Planes["IN2"] != "" || res.Inputs.Source == "" || len(res.Compare) == 0 {
		t.Fatalf("planes %v source %q compare %d", res.Settings.Planes, res.Inputs.Source, len(res.Compare))
	}
	fb, _ := os.ReadFile(filepath.Join(dir, "feedback.json"))
	if !strings.Contains(string(fb), `"fb-1"`) || !strings.Contains(string(fb), "post-layout items") {
		t.Fatalf("feedback not merged: %s", fb)
	}
	if _, err := os.Stat(filepath.Join(dir, "heatmaps", "temp-TOP.svg")); err != nil {
		t.Fatal(err)
	}
	// Without --sim the hook is a warning, not an error.
	if p, err := autoPostSim(dir, "", "", b, rep, &stdout, &stderr); err != nil || p != "" || !strings.Contains(stderr.String(), "needs --sim") {
		t.Fatalf("no-sim: %q %v", p, err)
	}
}
