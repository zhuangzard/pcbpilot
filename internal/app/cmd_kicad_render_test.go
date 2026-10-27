package app

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// `kicad render --compare` without an input compares the existing --out:
// exit 0 / 6 / 5 for pass / fail / cannot-check, one JSON report each time.
func TestKicadRenderCompareExitCodes(t *testing.T) {
	root := t.TempDir()
	write := func(dir, name, body string) {
		_ = os.MkdirAll(dir, 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`<svg xmlns="http://www.w3.org/2000/svg">`+body+`</svg>`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base, same, moved := filepath.Join(root, "base"), filepath.Join(root, "same"), filepath.Join(root, "moved")
	write(base, "p.svg", `<path d="M1 1 L2 2"/>`)
	write(same, "p.svg", `<path d="M1 1 L2 2"/>`)
	write(moved, "p.svg", `<path d="M1 2 L2 3"/>`)
	for _, c := range []struct {
		out, verdict string
		code         int
	}{{same, "pass", 0}, {moved, "fail", 6}, {filepath.Join(root, "empty"), "cannot-check", 5}} {
		_ = os.MkdirAll(c.out, 0o755)
		stdout, stderr, code := runCLI(t, "kicad", "render", "--out", c.out, "--compare", base)
		var rep kicadRenderReport
		if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
			t.Fatalf("%s: no JSON report: %v %s %s", c.out, err, stdout, stderr)
		}
		if code != c.code || string(rep.Verdict) != c.verdict || rep.Compare == nil {
			t.Errorf("%s: exit %d verdict %s, want %d %s", c.out, code, rep.Verdict, c.code, c.verdict)
		}
	}
	// Comparing a directory with itself (also through a link or junction),
	// or giving nothing to do, is refused.
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(base, alias); err != nil && runtime.GOOS == "windows" {
		_ = exec.Command("cmd", "/c", "mklink", "/J", alias, base).Run()
	}
	refused := [][]string{{"kicad", "render", "--out", base, "--compare", base}, {"kicad", "render"}}
	if _, err := os.Stat(alias); err == nil {
		refused = append(refused, []string{"kicad", "render", "--out", alias, "--compare", base})
	} else {
		t.Log("no link / junction here; alias case skipped")
	}
	for _, args := range refused {
		stdout, _, code := runCLI(t, args...)
		var rep kicadRenderReport
		_ = json.Unmarshal([]byte(stdout), &rep)
		if code == 0 || rep.Error == nil || rep.Error.Code != "RENDER_BAD_INPUT" || rep.Error.Fix == "" {
			t.Errorf("%v: exit %d %+v", args, code, rep.Error)
		}
	}
}

// sch-build --render plots through kicad.RenderSchSVG: two builds of one spec
// into different directories give byte-identical, date-free SVGs, and
// kicad render --compare passes on them.
func TestSchBuildRenderIsNormalized(t *testing.T) {
	sbNeedKicad(t)
	root := t.TempDir()
	var dirs []string
	for _, n := range []string{"a", "b"} {
		out := filepath.Join(root, n, "p")
		rep, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, NoIntent: true, Render: true, PlannerBudget: 20000}, nil)
		if err != nil || !rep.OK {
			t.Fatalf("build %s: %v %+v", n, err, rep.Error)
		}
		if rep.Outputs["render"] == "" {
			t.Fatalf("build %s rendered nothing: %v", n, rep.Warnings)
		}
		dirs = append(dirs, filepath.Join(out, "render"))
	}
	a, err := os.ReadFile(filepath.Join(dirs[0], "p.svg"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dirs[1], "p.svg"))
	if string(a) != string(b) {
		t.Fatal("two builds of one spec rendered different SVGs")
	}
	if strings.Contains(string(a), " date 20") || strings.Contains(string(a), root) || strings.Contains(string(a), filepath.ToSlash(root)) {
		t.Error("render keeps the plot date or a build path")
	}
	stdout, _, code := runCLI(t, "kicad", "render", "--out", dirs[1], "--compare", dirs[0])
	if code != 0 {
		t.Fatalf("compare exit %d: %s", code, stdout)
	}
}

// --report may not land inside the input's project, --out or --compare
// (it could overwrite the input or become foreign content of a render).
func TestKicadRenderReportPathGuard(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	sch := filepath.Join(proj, "top.kicad_sch")
	_ = os.MkdirAll(proj, 0o755)
	const body = "(kicad_sch (version 20231120))\n"
	if err := os.WriteFile(sch, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "base")
	_ = os.MkdirAll(base, 0o755)
	for _, r := range []string{sch, strings.ToUpper(sch), filepath.Join(proj, "r.json"), filepath.Join(root, "out", "r.json"), filepath.Join(base, "r.json")} {
		stdout, _, code := runCLI(t, "kicad", "render", "--sch", sch, "--out", filepath.Join(root, "out"), "--compare", base, "--report", r)
		var rep kicadRenderReport
		_ = json.Unmarshal([]byte(stdout), &rep)
		if code != 1 || rep.Error == nil || rep.Error.Code != "RENDER_BAD_INPUT" {
			t.Errorf("--report %s: exit %d %+v", r, code, rep.Error)
		}
		if b, _ := os.ReadFile(sch); string(b) != body {
			t.Fatalf("--report %s overwrote the input", r)
		}
		if _, err := os.Stat(filepath.Join(proj, "r.json")); err == nil {
			t.Fatal("report written into the project")
		}
	}
}
