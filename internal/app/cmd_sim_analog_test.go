package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
	"github.com/zhuangzard/pcbpilot/pkg/intent"
)

const (
	analogFixture = "../../testdata/analog/frontend"
	skillRefsDir  = "../../.agents/skills/pcbpilot/references"
)

func analogArgs(dir string, extra ...string) []string {
	args := []string{"sim", "analog",
		"--connectivity", filepath.Join(analogFixture, "sch-frontend.json"),
		"--values", filepath.Join(analogFixture, "values.json"),
		"--spec", filepath.Join(analogFixture, "spec.json"),
		"--analog-models", filepath.Join(skillRefsDir, "spice-models", "analog-models.json"),
		"--models-lib", filepath.Join(skillRefsDir, "power-models.json"),
		"--parts", filepath.Join(skillRefsDir, "standard-parts.json"),
		"--mc-runs", "20",
		"--out", filepath.Join(dir, "analog.json"), "--report", filepath.Join(dir, "analog.md"),
		"--plots-dir", filepath.Join(dir, "plots"), "--apply-plan", filepath.Join(dir, "plan.json")}
	return append(args, extra...)
}

// Offline CLI without ngspice: analytic checks, the missing-ngspice note, a plan.
func TestSimAnalogCLINoNgspice(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs(analogArgs(dir, "--ngspice", "none"))
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, "analog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out analogsim.Output
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Ngspice.Available || out.Summary.Blocks != 3 || out.Summary.Simulated != 0 {
		t.Fatalf("summary %+v ngspice %+v", out.Summary, out.Ngspice)
	}
	found := false
	for _, f := range out.Findings {
		found = found || (f.Kind == "analog-ngspice-missing" && strings.Contains(f.Suggestion, "sim tools install"))
	}
	if !found || !strings.Contains(stderr.String(), "ngspice missing") {
		t.Fatalf("missing-ngspice note absent: %s", stderr.String())
	}
	pb, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := analogsim.ParsePlan(pb)
	if err != nil || len(plan.Changes) == 0 || !plan.RequiresUserConfirmation {
		t.Fatalf("plan: %v %+v", err, plan)
	}
}

// With ngspice: plots, report, netlists and the compile-plan round trip.
func TestSimAnalogCLINgspice(t *testing.T) {
	if p, _ := analogsim.FindNgspice(""); p == "" {
		t.Skip("ngspice not installed")
	}
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs(analogArgs(dir))
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	var out analogsim.Output
	b, _ := os.ReadFile(filepath.Join(dir, "analog.json"))
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Summary.Simulated != 3 || out.WorkDir != "analog-ngspice" || len(out.Artifacts) == 0 {
		t.Fatalf("summary %+v workDir %q artifacts %d", out.Summary, out.WorkDir, len(out.Artifacts))
	}
	for _, a := range out.Artifacts {
		if _, err := os.Stat(filepath.Join(dir, out.WorkDir, a.Path)); err != nil {
			t.Errorf("artifact %s: %v", a.Path, err)
		}
	}
	md, _ := os.ReadFile(filepath.Join(dir, "analog.md"))
	for _, want := range []string{"Sallen-Key 低通", "修改计划", "plots/analog-a3-bode.svg", "相位裕度"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("analog.md lacks %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "plots", "analog-a3-loop.svg")); err != nil {
		t.Error(err)
	}
	// compile-plan against a synthetic sch list.
	list := `{"result":{"components":[{"designator":"C2","primitiveId":"pid-c2"},{"designator":"C3","primitiveId":"pid-c3"},{"designator":"R5","primitiveId":"pid-r5"},{"designator":"R4","primitiveId":"pid-r4"}]}}`
	lp := filepath.Join(dir, "sch-list.json")
	if err := os.WriteFile(lp, []byte(list), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	root = newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"sim", "analog", "compile-plan", "--plan", filepath.Join(dir, "plan.json"), "--components", lp, "--allow-value-only", "--out", filepath.Join(dir, "pb.json")})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	var pb struct {
		Version int              `json:"version"`
		Steps   []map[string]any `json:"steps"`
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "pb.json"))
	if err := json.Unmarshal(raw, &pb); err != nil || pb.Version != 1 || len(pb.Steps) < 2 {
		t.Fatalf("playbook %v %s", err, raw)
	}
	for _, s := range pb.Steps[:len(pb.Steps)-1] {
		if s["confirm"] != true {
			t.Errorf("step without confirm: %+v", s)
		}
	}
}

func TestIntentDeriveRunsAnalog(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"intent", "derive",
		"--connectivity", filepath.Join(analogFixture, "sch-frontend.json"),
		"--values", filepath.Join(analogFixture, "values.json"),
		"--analog-spec", filepath.Join(analogFixture, "spec.json"),
		"--analog-models", filepath.Join(skillRefsDir, "spice-models", "analog-models.json"),
		"--models-lib", filepath.Join(skillRefsDir, "power-models.json"),
		"--analog-out", filepath.Join(dir, "analog.json"),
		"--out", filepath.Join(dir, "intent.json")})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "intent.json"))
	var it intent.Intent
	if err := json.Unmarshal(raw, &it); err != nil {
		t.Fatal(err)
	}
	if it.Analog == nil || it.Analog.Blocks != 3 {
		t.Fatalf("intent.analog %+v", it.Analog)
	}
	has := false
	for _, f := range it.Findings {
		has = has || f.Kind == "analog-target-miss"
	}
	if !has {
		t.Errorf("the Sallen-Key Q miss (spec) did not reach intent findings")
	}
}

func TestSimToolsStatus(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs([]string{"sim", "tools", "status", "--ngspice", "/nonexistent/ngspice"})
	err := root.Execute()
	if p, _ := analogsim.FindNgspice(""); p == "" {
		if err == nil || !strings.Contains(stdout.String(), "install") {
			t.Fatalf("missing ngspice not reported: %v %s", err, stdout.String())
		}
		return
	}
	if err != nil || !strings.Contains(stdout.String(), "ngspice") {
		t.Fatalf("status: %v %s", err, stdout.String())
	}
}
