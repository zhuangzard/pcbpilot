package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// copyDRCBoard copies the synthetic owner/rules fixture project into a
// fresh directory and returns its board path.
func copyDRCBoard(t *testing.T) string {
	t.Helper()
	src := filepath.Join("..", "kicad", "testdata", "drcboard")
	dst := t.TempDir()
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dst, rel)), 0o755)
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dst, "board.kicad_pcb")
}

// fakeKicadCLIEnv makes this test binary the kicad-cli of `kicad drc` /
// `kicad fab --preflight`, writing fixture as every DRC report.
func fakeKicadCLIEnv(t *testing.T, fixture, code string) {
	t.Helper()
	kt := fakeDRCTools(t, fixture, code)
	t.Setenv("PCBPILOT_KICAD_CLI", kt.CLI)
}

func runDRCReport(t *testing.T, args ...string) (*kicadDRCReport, int) {
	t.Helper()
	out, _, err := runKiCadCmd(t, append([]string{"drc"}, args...)...)
	code := 0
	var ec exitCodeError
	if errors.As(err, &ec) {
		code = ec.code
	} else if err != nil {
		t.Fatalf("drc %v: %v", args, err)
	}
	var rep kicadDRCReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("report: %v\n%s", err, out)
	}
	if rep.ExitCode != code {
		t.Fatalf("report exitCode %d, process %d", rep.ExitCode, code)
	}
	return &rep, code
}

func checkStatus(cs []drcCheck) map[string]gate.Status {
	out := map[string]gate.Status{}
	for _, c := range cs {
		out[c.Name] = c.Status
	}
	return out
}

func TestKicadDRCCommandVerdicts(t *testing.T) {
	pcb := copyDRCBoard(t)
	fakeKicadCLIEnv(t, "drc/drcboard-all.json", "0")
	out := t.TempDir()
	rep, code := runDRCReport(t, "--pcb", pcb, "--schematic-parity", "--out", out)
	want := map[string]gate.Status{"kicad-drc": gate.Fail, "kicad-drc-policy": gate.Pass, "schematic-parity": gate.Fail}
	if code != 6 || rep.Verdict != gate.Fail || !reflect.DeepEqual(checkStatus(rep.Checks), want) {
		t.Fatalf("exit %d verdict %s checks %+v", code, rep.Verdict, rep.Checks)
	}
	// Owners: the clearance is routing (a track and R1's pad), the excluded
	// courtyard overlap is placement, the silk warning R1's own.
	classes := map[string]string{}
	for _, v := range append(rep.Violations, rep.Excluded...) {
		classes[v.Rule] = v.Owner.Class
	}
	if !reflect.DeepEqual(classes, map[string]string{"clearance": "routing", "courtyards_overlap": "placement",
		"silk_over_copper": "footprint", "footprint_symbol_mismatch": "schematic-parity"}) || rep.Errors != 1 || rep.Warnings != 2 {
		t.Fatalf("owners %v errors %d warnings %d", classes, rep.Errors, rep.Warnings)
	}
	g := rep.Checks[0]
	if len(g.Items) != 1 || !strings.Contains(g.Items[0], "clearance (error, routing: R1 [VCC]) @ (12, 10) mm") {
		t.Fatalf("kicad-drc items %v", g.Items)
	}
	// The soft exclusion is silenced, not failing.
	if p := rep.Checks[1]; !strings.Contains(p.Detail, "courtyards_overlap (excluded)") || len(p.Items) != 0 {
		t.Fatalf("policy %+v", p)
	}
	for _, f := range []string{"drc-report.json", "drc.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Fatalf("--out %s: %v", f, err)
		}
	}

	// Signed waivers accept each item they match.
	w := filepath.Join(t.TempDir(), "w.json")
	_ = os.WriteFile(w, []byte(`[{"gate":"kicad-drc","match":"clearance (error, routing: R1","reason":"reviewed","by":"t"},
		{"gate":"schematic-parity","match":"footprint_symbol_mismatch","reason":"library rename","by":"t"}]`), 0o644)
	rep, code = runDRCReport(t, "--pcb", pcb, "--schematic-parity", "--waivers", w)
	if code != 0 || rep.Verdict != gate.Pass || len(rep.Checks[0].Waived) != 1 || len(rep.Checks[2].Waived) != 1 {
		t.Fatalf("waived: exit %d %+v", code, rep.Checks)
	}

	// Against its own report nothing is new.
	rep, code = runDRCReport(t, "--pcb", pcb, "--schematic-parity", "--baseline", filepath.Join(out, "drc-report.json"))
	if code != 0 || rep.Baseline == nil || rep.Baseline.Kind != "report" || len(rep.Baseline.New) != 0 || rep.Baseline.Matched != 3 {
		t.Fatalf("baseline: exit %d %+v", code, rep.Baseline)
	}
	// A raw kicad-cli baseline without the clearance: only it is new.
	raw := filepath.Join(t.TempDir(), "old.json")
	var doc map[string]any
	_ = json.Unmarshal(mustRead(t, filepath.Join("..", "kicad", "testdata", "drc", "drcboard-all.json")), &doc)
	doc["violations"] = doc["violations"].([]any)[1:]
	b, _ := json.Marshal(doc)
	_ = os.WriteFile(raw, b, 0o644)
	rep, code = runDRCReport(t, "--pcb", pcb, "--schematic-parity", "--baseline", raw)
	if code != 6 || rep.Baseline.Kind != "kicad-json" || len(rep.Baseline.New) != 1 || rep.Baseline.New[0].Rule != "clearance" ||
		checkStatus(rep.Checks)["schematic-parity"] != gate.Pass {
		t.Fatalf("raw baseline: exit %d %+v %+v", code, rep.Baseline, rep.Checks)
	}
	// --baseline-pcb runs DRC on that board too (same fake report: nothing new).
	rep, code = runDRCReport(t, "--pcb", pcb, "--baseline-pcb", copyDRCBoard(t))
	if code != 0 || rep.Baseline.Kind != "pcb" || len(rep.Baseline.New) != 0 {
		t.Fatalf("baseline-pcb: exit %d %+v", code, rep.Baseline)
	}
}

// DRC that did not run, or a baseline that cannot be read, is
// cannot-check (exit 5) — never a pass, whatever the waivers say.
func TestKicadDRCCommandCannotCheck(t *testing.T) {
	pcb := copyDRCBoard(t)
	w := filepath.Join(t.TempDir(), "w.json")
	_ = os.WriteFile(w, []byte(`[{"gate":"kicad-drc","match":" ","reason":"any","by":"t"}]`), 0o644)
	for _, tc := range uncheckableDRC {
		fakeKicadCLIEnv(t, tc.fixture, tc.code)
		rep, code := runDRCReport(t, "--pcb", pcb, "--waivers", w)
		if code != 5 || rep.Verdict != gate.CannotCheck || len(rep.Checks) != 1 || rep.Checks[0].Status != gate.CannotCheck {
			t.Errorf("%s (exit %s): exit %d %+v", tc.fixture, tc.code, code, rep.Checks)
		}
	}
	fakeKicadCLIEnv(t, "drc/drcboard-all.json", "0")
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte(`{"violations": null}`), 0o644)
	if rep, code := runDRCReport(t, "--pcb", pcb, "--baseline", bad); code != 5 || checkStatus(rep.Checks)["baseline"] != gate.CannotCheck {
		t.Fatalf("unreadable baseline: exit %d %+v", code, rep.Checks)
	}
	// Parity requested without the project's root schematic.
	_ = os.Remove(strings.TrimSuffix(pcb, ".kicad_pcb") + ".kicad_sch")
	if _, code := runDRCReport(t, "--pcb", pcb, "--schematic-parity"); code != 5 {
		t.Fatalf("parity without schematic: exit %d", code)
	}
}

// The preflight's contract checks on the fixture board (bare board: the
// fake kicad-cli does no position export).
func TestKicadFabPreflightFake(t *testing.T) {
	pcb := copyDRCBoard(t)
	fakeKicadCLIEnv(t, "drc/drcboard-all.json", "0")
	contract := filepath.Join(t.TempDir(), "contract.json")
	_ = os.WriteFile(contract, []byte(`{"vendor":"JLCPCB","layers":4,"finish":"ENIG",
	  "limits":{"minTrackMm":0.127,"minSpaceMm":0.127,"minDrillMm":0.3,"minAnnularMm":0.13},
	  "sources":[{"item":"limits","url":"https://example.com/capabilities","retrieved":"2026-10-27"}]}`), 0o644)
	outDir := filepath.Join(t.TempDir(), "fab")
	stdout, _, err := runKiCadCmd(t, "fab", "--preflight", "--pcb", pcb, "--contract", contract, "--no-assembly", "--out", outDir)
	var ec exitCodeError
	if !errors.As(err, &ec) || ec.code != 6 {
		t.Fatalf("want exit 6, got %v", err)
	}
	var rep fabPreflightReport
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatal(err)
	}
	want := map[string]gate.Status{"kicad-drc": gate.Fail, "kicad-drc-policy": gate.Pass, "board-outline": gate.Pass,
		"contract": gate.Pass, "layer-count": gate.Pass, "min-track": gate.Pass, "min-space": gate.Fail, "min-drill": gate.Fail,
		"min-annular": gate.Pass, "finish": gate.Pass, "assembly-sides": gate.Skip, "lcsc": gate.Skip, "bom-cpl": gate.Skip}
	if got := checkStatus(rep.Checks); !reflect.DeepEqual(got, want) || rep.Verdict != gate.Fail {
		t.Fatalf("checks %v verdict %s", got, rep.Verdict)
	}
	if _, err := os.Stat(outDir); !os.IsNotExist(err) {
		t.Fatalf("--preflight created --out: %v", err)
	}
	// No contract: every contract check is cannot-check, so never a pass.
	stdout, _, _ = runKiCadCmd(t, "fab", "--preflight", "--pcb", pcb, "--no-assembly")
	_ = json.Unmarshal([]byte(stdout), &rep)
	if s := checkStatus(rep.Checks); s["contract"] != gate.CannotCheck || s["min-space"] != gate.CannotCheck || s["layer-count"] != gate.CannotCheck {
		t.Fatalf("no contract: %v", s)
	}
	if _, _, err := runKiCadCmd(t, "fab", "--pcb", pcb, "--out", outDir, "--contract", contract); err == nil || !strings.Contains(err.Error(), "--preflight flags") {
		t.Fatalf("--contract without --preflight: %v", err)
	}
}

func TestPreflightCheckHelpers(t *testing.T) {
	min := func(v float64, srcs ...kicad.RuleSource) *kicad.EffectiveMin {
		return &kicad.EffectiveMin{Item: "space", MinMm: &v, Sources: srcs}
	}
	ct := &kicad.FabContract{Layers: 2}
	ct.Limits.MinSpaceMm = 0.127
	if c := ruleLimitCheck(min(0.127, kicad.RuleSource{Source: "netclass", Mm: 0.127}), ct, kicad.Measured{}); c.Status != gate.Pass {
		t.Errorf("equal to the limit: %+v", c)
	}
	if c := ruleLimitCheck(min(0.1, kicad.RuleSource{Source: "local", Where: "pad 1 of U1", Mm: 0.1}), ct, kicad.Measured{}); c.Status != gate.Fail || len(c.Items) != 1 {
		t.Errorf("below: %+v", c)
	}
	if c := ruleLimitCheck(&kicad.EffectiveMin{Item: "space", Unset: []string{"min_clearance = 0"}}, ct, kicad.Measured{}); c.Status != gate.CannotCheck {
		t.Errorf("unbounded: %+v", c)
	}
	if c := ruleLimitCheck(&kicad.EffectiveMin{Item: "space", MinMm: new(float64), Unknown: []string{"x"}}, ct, kicad.Measured{}); c.Status != gate.CannotCheck {
		t.Errorf("unknown rule: %+v", c)
	}
	if c := ruleLimitCheck(&kicad.EffectiveMin{Item: "track"}, ct, kicad.Measured{}); c.Status != gate.CannotCheck || !strings.Contains(c.Detail, "minTrackMm") {
		t.Errorf("no limit in the contract: %+v", c)
	}
	rows := [][]string{kicad.CPLHeader, {"R1", "0", "0", "Bottom", "0"}, {"C1", "0", "0", "Top", "0"}}
	if s := cplSides(rows); !reflect.DeepEqual(s, []string{"top", "bottom"}) {
		t.Errorf("sides %v", s)
	}
	ct.Assembly = &struct {
		Sides string `json:"sides"`
		Qty   int    `json:"qty,omitempty"`
	}{Sides: "top"}
	if c := sidesCheck(&fabAssemblyReport{CPLSides: []string{"top", "bottom"}}, ct); c.Status != gate.Fail {
		t.Errorf("both vs top: %+v", c)
	}
	if c := sidesCheck(&fabAssemblyReport{CPLSides: []string{"top"}}, ct); c.Status != gate.Pass {
		t.Errorf("top vs top: %+v", c)
	}
	board := []byte(`(kicad_pcb (setup (stackup (copper_finish "HAL lead-free"))) (gr_line (layer "Edge.Cuts")))`)
	ct.Finish = "ENIG"
	if c := finishCheck(board, ct); c.Status != gate.Fail || c.Advisory {
		t.Errorf("finish mismatch: %+v", c)
	}
	ct.Finish = "HAL"
	if c := finishCheck(board, ct); c.Status != gate.Pass {
		t.Errorf("finish match: %+v", c)
	}
	if c := finishCheck([]byte(`(kicad_pcb)`), ct); c.Status != gate.Warn || !c.Advisory {
		t.Errorf("no finish on the board: %+v", c)
	}
	// Outline: an open outline item (even excluded) fails; ignoring the rule
	// leaves it unchecked; no Edge.Cuts fails.
	drc := &kicadDRCReport{Run: &kicad.DRCRun{}, Excluded: []kicad.OwnedViolation{{Violation: kicad.Violation{Rule: "invalid_outline", Excluded: true, Message: "not closed"}}}}
	if c := outlineCheck(board, drc); c.Status != gate.Fail || !strings.HasPrefix(c.Items[0], "EXCLUDED ") {
		t.Errorf("open outline: %+v", c)
	}
	drc = &kicadDRCReport{Run: &kicad.DRCRun{Policy: &kicad.DRCPolicy{Ignored: []string{"invalid_outline"}}}}
	if c := outlineCheck(board, drc); c.Status != gate.CannotCheck {
		t.Errorf("ignored outline test: %+v", c)
	}
	if c := outlineCheck([]byte(`(kicad_pcb)`), &kicadDRCReport{Run: &kicad.DRCRun{}}); c.Status != gate.Fail {
		t.Errorf("no Edge.Cuts: %+v", c)
	}
	if c := outlineCheck(board, &kicadDRCReport{}); c.Status != gate.CannotCheck {
		t.Errorf("DRC did not run: %+v", c)
	}
}
