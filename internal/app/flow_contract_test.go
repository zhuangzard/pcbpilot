package app

// Flow contract: the established research steps must stay in the flow.
//
// TestFlowContract runs the offline full chain on the ESP32 mini and the HV
// flyback fixtures —
//
//	sim power → intent derive → sim analog → pcb auto run --intent --sim
//	→ pcb check --intent → pcb rules (plan against a fake host)
//	→ sim post-layout → pcb aesthetics → report design
//
// — and asserts that every stage still runs and still emits its output with
// the key fields: intent nets with currentA/widthMil, simulated rails, analog
// blocks, intent widths/necks applied in the routed copper, isolation bands
// and slots, via-current sizing, board-edge bands, the rules plan, the IR and
// post-layout thermal results and every report section. Aesthetics is an
// ADDITIONAL, report-only layer: the joint score must be identical with and
// without it.
//
// AGENTS.md: this test protects the research steps; never weaken it to make
// a change pass. A change that drops or bypasses a stage must fail here.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

func flowCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetOut(&stderr)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func flowMust(t *testing.T, stage string, args ...string) string {
	t.Helper()
	out, se, err := flowCLI(t, args...)
	if err != nil {
		t.Fatalf("flow stage %q failed: %v\n%s", stage, err, se)
	}
	return out
}

func flowJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("stage output missing: %v", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

type flowCase struct {
	name string
	// schematic inputs for sim power / sim analog
	conn     []string
	values   string
	models   string
	spec     string
	board    string
	boardSim string // sim.json that matches the board's nets ("" = the chain's own)
	auto     []string
	hv       bool
}

func TestFlowContract(t *testing.T) {
	const refs = "../../.agents/skills/pcbpilot/references"
	lib := filepath.Join(refs, "power-models.json")
	hvDir := "../../testdata/stress/hv/flyback"
	sch := "../../pkg/powersim/testdata/esp32mini"
	cases := []flowCase{
		{name: "esp32", conn: []string{filepath.Join(sch, "sch-905bb85957eaf435.json"), filepath.Join(sch, "sch-950ae6609e91d753.json")},
			values: filepath.Join(sch, "values.json"), board: "../../pkg/pcbauto/testdata/esp32-v05-fixed.routed.json",
			boardSim: "testdata/esp32-v05/sim.json", auto: []string{"--layers", "4", "--seed", "3", "--timeout", "60s"}},
		{name: "hv-flyback", conn: []string{filepath.Join(hvDir, "connectivity.json")}, values: filepath.Join(hvDir, "values.json"),
			models: filepath.Join(hvDir, "models.json"), spec: filepath.Join(hvDir, "spec.json"), board: filepath.Join(hvDir, "board.json"),
			auto: []string{"--seed", "1", "--grid", "5", "--timeout", "20s"}, hv: true},
	}
	for _, fc := range cases {
		t.Run(fc.name, func(t *testing.T) { runFlowContract(t, fc, lib, refs) })
	}
}

func runFlowContract(t *testing.T, fc flowCase, lib, refs string) {
	dir := t.TempDir()
	p := func(n string) string { return filepath.Join(dir, n) }
	schArgs := func() []string {
		var a []string
		for _, c := range fc.conn {
			a = append(a, "--connectivity", c)
		}
		a = append(a, "--values", fc.values, "--models-lib", lib)
		if fc.models != "" {
			a = append(a, "--models", fc.models)
		}
		return a
	}

	// 1. sim power: every scenario converges and carries rail voltages/currents.
	flowMust(t, "sim power", append(append([]string{"sim", "power"}, schArgs()...), "--out", p("sim-sch.json"))...)
	var sim struct {
		Results []struct {
			Scenario string `json:"scenario"`
			Nets     map[string]struct {
				Voltage  float64 `json:"voltage"`
				CurrentA float64 `json:"currentA"`
				Role     string  `json:"role"`
			} `json:"nets"`
		} `json:"results"`
	}
	flowJSON(t, p("sim-sch.json"), &sim)
	if len(sim.Results) == 0 {
		t.Fatal("sim power: no scenario results")
	}
	rails := 0
	for _, n := range sim.Results[0].Nets {
		if n.Role == "power" && n.Voltage > 0 {
			rails++
		}
	}
	if rails == 0 {
		t.Error("sim power: no power rail with a voltage")
	}
	simForBoard := p("sim-sch.json")
	if fc.boardSim != "" {
		simForBoard = fc.boardSim
	}

	// 2. intent derive: nets with currentA and widthMil (+ HV pairs).
	idArgs := []string{"intent", "derive", "--sim", simForBoard, "--models-lib", lib, "--out", p("intent.json")}
	if fc.boardSim == "" {
		idArgs = append(append([]string{"intent", "derive"}, schArgs()...), "--sim", simForBoard, "--out", p("intent.json"))
	}
	if fc.spec != "" {
		idArgs = append(idArgs, "--spec", fc.spec)
	}
	flowMust(t, "intent derive", idArgs...)
	var it intent.Intent
	flowJSON(t, p("intent.json"), &it)
	sized := 0
	for _, n := range it.Nets {
		if n.CurrentA > 0 && n.WidthMil.Outer > 0 {
			sized++
		}
	}
	if sized == 0 || len(it.NetClasses) == 0 {
		t.Fatalf("intent: %d nets with currentA+widthMil, %d net classes", sized, len(it.NetClasses))
	}
	if fc.hv && (len(it.Pairs) == 0 || it.Pairs[0].CreepageMm <= 0) {
		t.Fatalf("intent: HV insulation pairs missing: %+v", it.Pairs)
	}

	// 3. sim analog: blocks recognised (analytic without ngspice).
	flowMust(t, "sim analog", append(append([]string{"sim", "analog"}, schArgs()...),
		"--analog-models", filepath.Join(refs, "spice-models", "analog-models.json"), "--parts", filepath.Join(refs, "standard-parts.json"),
		"--out", p("analog.json"), "--ngspice", "none")...)
	var an struct {
		Blocks []json.RawMessage `json:"blocks"`
	}
	flowJSON(t, p("analog.json"), &an)
	if len(an.Blocks) == 0 {
		t.Error("sim analog: no blocks")
	}

	// 4. pcb auto run --intent --sim.
	flowMust(t, "pcb auto run", append([]string{"pcb", "auto", "run", "--board", fc.board, "--intent", p("intent.json"), "--sim", simForBoard,
		"--no-feedback", "--out-dir", p("auto")}, fc.auto...)...)
	var plan pcbauto.Report
	flowJSON(t, p("auto/plan.json"), &plan)
	res := plan.Result
	if res == nil || res.Route == nil || res.Analysis == nil || res.Stackup == nil || res.DRC == nil {
		t.Fatal("plan.json: result/route/analysis/stackup/drc missing")
	}
	intentNets, viaSized := 0, 0
	for _, np := range res.Analysis.Nets {
		if np.Source == "intent" {
			intentNets++
		}
		if np.Via != nil && np.Via.Count >= 1 && np.Via.AmpacityA > 0 {
			viaSized++
		}
	}
	if intentNets == 0 {
		t.Error("pcb auto: no net plan sourced from the intent")
	}
	if viaSized == 0 {
		t.Error("pcb auto: no via sized by current (NetPlan.via)")
	}
	// intent widths applied in the routed copper: the widest track of an
	// intent-sized net matches its plan width; narrower pieces are necks.
	widest := map[string]float64{}
	for _, tr := range res.Route.Tracks {
		widest[tr.Net] = max(widest[tr.Net], tr.Width)
	}
	applied := 0
	for _, np := range res.Analysis.Nets {
		if w, ok := widest[np.Net]; ok && np.Source == "intent" && np.WidthMil > 0 && w >= np.WidthMil-0.05 {
			applied++
		}
	}
	if applied == 0 {
		t.Error("pcb auto: no intent width reached the routed copper")
	}
	if res.Route.Power == nil || len(res.Route.Power.Nets) == 0 {
		t.Error("pcb auto --sim: routed IR-drop result missing")
	}
	if res.Edge == nil || res.Edge.Policy == nil || len(res.Edge.Layers) == 0 {
		t.Error("pcb auto: board-edge distance bands missing")
	}
	if fc.hv {
		if res.Isolation == nil || len(res.Isolation.Pairs) == 0 || len(res.Isolation.Slots) == 0 || len(res.Isolation.Moats) == 0 {
			t.Fatalf("pcb auto: isolation pairs/slots/bands missing: %+v", res.Isolation)
		}
	}
	// Aesthetics is additive: present, weight 0, and the joint without it
	// is identical.
	if plan.Joint == nil || plan.Joint.Aesthetics == nil || plan.Joint.Aesthetics.Weight != 0 {
		t.Fatal("plan.json: joint.aesthetics missing or weighted")
	}
	raw, _ := os.ReadFile(fc.board)
	b, err := pcbauto.FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	c := pcbauto.Understand(b, res.Analysis)
	j0 := pcbauto.Joint(b, res.Analysis, c, res.Stackup, res.Route, res.DRC, pcbauto.JointOptions{PlacementScore: -1})
	j1 := pcbauto.Joint(b, res.Analysis, c, res.Stackup, res.Route, res.DRC, pcbauto.JointOptions{PlacementScore: -1, Aesthetics: true, Isolation: res.Isolation})
	if j0.Overall != j1.Overall || j0.Quality != j1.Quality || len(j0.Items) != len(j1.Items) {
		t.Errorf("aesthetics changed the joint: %v/%v vs %v/%v", j0.Overall, j0.Quality, j1.Overall, j1.Quality)
	}
	routed := p("auto/board.routed.json")
	if fc.hv {
		var rb struct {
			Copper struct {
				Fills   []map[string]any `json:"fills"`
				Regions []map[string]any `json:"regions"`
			} `json:"copper"`
		}
		flowJSON(t, routed, &rb)
		slot, moat := false, false
		for _, f := range rb.Copper.Fills {
			slot = slot || strings.HasPrefix(f["primitiveId"].(string), "auto-slot")
		}
		for _, r := range rb.Copper.Regions {
			moat = moat || strings.HasPrefix(r["primitiveId"].(string), "auto-moat")
		}
		if !slot || !moat {
			t.Errorf("board.routed.json: slot %v, isolation band %v", slot, moat)
		}
	}

	// 5. pcb check --intent on the routed copper.
	out := flowMust(t, "pcb check", "pcb", "check", "--intent", p("intent.json"), "--board", routed, "--json")
	if err := os.WriteFile(p("check.json"), []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	var chk struct {
		Summary map[string]int  `json:"summary"`
		Edge    json.RawMessage `json:"edge"`
	}
	if err := json.Unmarshal([]byte(out), &chk); err != nil || chk.Summary == nil || len(chk.Edge) == 0 {
		t.Fatalf("pcb check: summary/edge missing (%v)", err)
	}

	// 6. pcb rules: the intent → native rule plan (fake host: no editor).
	var nets []string
	for n := range it.Nets {
		nets = append(nets, n)
	}
	sort.Strings(nets)
	h := &fakeRulesHost{nets: nets, cfg: cleanHostConfig(t, nets), pairs: []any{}}
	rr, _, _ := runRulesCLI(t, h, "apply", "--intent", p("intent.json"), "--dry-run")
	if rr.Plan == nil || len(rr.Plan.Classes) == 0 || rr.Plan.PendingWrites == 0 {
		t.Errorf("pcb rules: no class/rule plan from the intent: %+v", rr.Plan)
	}
	rb, _ := json.Marshal(rr)
	_ = os.WriteFile(p("rules.json"), rb, 0o644)

	// 7. sim post-layout: IR on the real copper + thermal.
	_, _, _ = flowCLI(t, "sim", "post-layout", "--board", routed, "--sim", simForBoard, "--intent", p("intent.json"),
		"--plan", p("auto/plan.json"), "--models-lib", lib, "--out", p("post.json")) // a FAIL verdict still writes post.json
	var post postsim.Result
	flowJSON(t, p("post.json"), &post)
	if len(post.Nets) == 0 || post.Thermal == nil || post.Verdict.Status == "" {
		t.Errorf("sim post-layout: nets %d thermal %v verdict %q", len(post.Nets), post.Thermal != nil, post.Verdict.Status)
	}

	// 8. pcb aesthetics (report-only, lowest tier).
	out = flowMust(t, "pcb aesthetics", "pcb", "aesthetics", "--board", routed, "--json")
	var ae pcbauto.AestheticsReport
	if err := json.Unmarshal([]byte(out), &ae); err != nil || ae.Weight != 0 || ae.Tier != len(pcbauto.ConstraintPriority) || len(ae.Metrics) != 18 {
		t.Fatalf("pcb aesthetics: weight %v tier %d metrics %d (%v)", ae.Weight, ae.Tier, len(ae.Metrics), err)
	}

	// 9. report design with every stage's output: all sections present.
	_, se, err := flowCLI(t, "report", "design", "--out-dir", p("report"), "--project-name", fc.name, "--models", lib,
		"--intent", p("intent.json"), "--sim", simForBoard, "--analog", p("analog.json"), "--plan-dir", p("auto"),
		"--board", routed, "--check", p("check.json"), "--rules-check", p("rules.json"), "--post", p("post.json"),
		"--no-zip", "--date", "2026-09-28T00:00:00Z")
	if err != nil {
		t.Fatalf("report design: %v\n%s", err, se)
	}
	md, err := os.ReadFile(p("report/v1/report.md"))
	if err != nil {
		t.Fatal(err)
	}
	last := -1
	for _, sec := range []string{"## 0 ", "## 1 ", "## 2 ", "## 3 ", "## 3A ", "## 4 ", "## 5 ", "## 6 ", "## 6A ", "## 6B ", "## 7 ", "## 8 ", "## 9 ", "## 10 ", "## 11 "} {
		i := bytes.Index(md, []byte("\n"+sec))
		if i < 0 || i < last {
			t.Errorf("report.md: section %q missing or out of order", sec)
		}
		last = i
	}
	var rj map[string]json.RawMessage
	flowJSON(t, p("report/v1/report.json"), &rj)
	for _, k := range []string{"requirements", "power", "analog", "layout", "postLayout", "aesthetics", "verification", "manufacturing"} {
		if len(rj[k]) == 0 || string(rj[k]) == "null" {
			t.Errorf("report.json: section %q missing", k)
		}
	}
}
