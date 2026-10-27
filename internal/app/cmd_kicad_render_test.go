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

// The --report guard runs before any write, even for an argument error, and
// covers external dependencies and hard links; a report is replaced by
// rename, never truncated in place.
func TestKicadRenderReportGuardDeep(t *testing.T) {
	root := t.TempDir()
	// An existing render in X: --report X/render.json with nothing to do
	// must not overwrite its manifest.
	x := filepath.Join(root, "x")
	_ = os.MkdirAll(x, 0o755)
	const manifest = `{"schemaVersion":1,"kind":"sch","files":[{"file":"p.svg","kind":"sch-page","sha256":"","bytes":0}]}`
	_ = os.WriteFile(filepath.Join(x, "render.json"), []byte(manifest), 0o644)
	_ = os.WriteFile(filepath.Join(x, "p.svg"), []byte(`<svg><path d="M1 1 L2 2"/></svg>`), 0o644)
	if _, _, code := runCLI(t, "kicad", "render", "--out", x, "--report", filepath.Join(x, "render.json")); code != 1 {
		t.Errorf("early error: exit %d", code)
	}
	if b, _ := os.ReadFile(filepath.Join(x, "render.json")); string(b) != manifest {
		t.Fatal("an argument error overwrote --out/render.json through --report")
	}

	// A project whose sub-sheet lives outside it.
	proj, shared := filepath.Join(root, "proj"), filepath.Join(root, "shared")
	_ = os.MkdirAll(proj, 0o755)
	_ = os.MkdirAll(shared, 0o755)
	sch, sub := filepath.Join(proj, "top.kicad_sch"), filepath.Join(shared, "sub.kicad_sch")
	const top = `(kicad_sch (version 20231120) (uuid "11111111-1111-1111-1111-111111111111")
	(sheet (at 1 1) (size 5 5) (uuid "22222222-2222-2222-2222-222222222222")
		(property "Sheetname" "sub" (at 1 1 0)) (property "Sheetfile" "../shared/sub.kicad_sch" (at 1 6 0))))
`
	const subText = "(kicad_sch (version 20231120) (uuid \"33333333-3333-3333-3333-333333333333\"))\n"
	_ = os.WriteFile(sch, []byte(top), 0o644)
	_ = os.WriteFile(sub, []byte(subText), 0o644)
	out := filepath.Join(root, "out")
	try := func(name, report string) {
		t.Helper()
		stdout, _, code := runCLI(t, "kicad", "render", "--sch", sch, "--out", out, "--report", report)
		var rep kicadRenderReport
		_ = json.Unmarshal([]byte(stdout), &rep)
		if code != 1 || rep.Error == nil || rep.Error.Code != "RENDER_BAD_INPUT" {
			t.Errorf("%s: exit %d %+v", name, code, rep.Error)
		}
		if b, _ := os.ReadFile(sub); string(b) != subText {
			t.Fatalf("%s: external sheet overwritten", name)
		}
		if b, _ := os.ReadFile(sch); string(b) != top {
			t.Fatalf("%s: input overwritten", name)
		}
	}
	try("external sheet", sub)
	link := filepath.Join(root, "link.json")
	if err := os.Link(sch, link); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	try("hard link to the input", link)
	link2 := filepath.Join(root, "link2.json")
	if err := os.Link(sub, link2); err == nil {
		try("hard link to an external sheet", link2)
	}

	// Replace, not truncate: a hard link to an old report keeps its bytes.
	old := filepath.Join(root, "old.json")
	_ = os.WriteFile(old, []byte("old"), 0o644)
	keep := filepath.Join(root, "keep.json")
	if err := os.Link(old, keep); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "base")
	_ = os.MkdirAll(base, 0o755)
	_ = os.WriteFile(filepath.Join(base, "p.svg"), []byte(`<svg><path d="M1 1 L2 2"/></svg>`), 0o644)
	_, _, _ = runCLI(t, "kicad", "render", "--out", x, "--compare", base, "--report", old)
	if b, _ := os.ReadFile(keep); string(b) != "old" {
		t.Error("report truncated an existing inode")
	}
	if b, _ := os.ReadFile(old); !strings.Contains(string(b), `"verdict"`) {
		t.Errorf("report not written: %s", b)
	}
}

// A missing external sheet stays protected: --report naming it is refused
// and nothing is created there. With the closure incomplete, a --report
// anywhere else is not written either (stdout only).
func TestKicadRenderReportUnresolvedDependency(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	_ = os.MkdirAll(proj, 0o755)
	_ = os.MkdirAll(filepath.Join(root, "shared"), 0o755)
	sch := filepath.Join(proj, "top.kicad_sch")
	_ = os.WriteFile(sch, []byte(`(kicad_sch (version 20231120) (uuid "11111111-1111-1111-1111-111111111111")
	(sheet (at 1 1) (size 5 5) (uuid "22222222-2222-2222-2222-222222222222")
		(property "Sheetname" "sub" (at 1 1 0)) (property "Sheetfile" "../shared/sub.kicad_sch" (at 1 6 0))))
`), 0o644)
	missing := filepath.Join(root, "shared", "sub.kicad_sch")
	stdout, _, code := runCLI(t, "kicad", "render", "--sch", sch, "--out", filepath.Join(root, "out"), "--report", missing)
	var rep kicadRenderReport
	_ = json.Unmarshal([]byte(stdout), &rep)
	if code != 1 || rep.Error == nil || rep.Error.Code != "RENDER_BAD_INPUT" {
		t.Errorf("report on the missing sheet: exit %d %+v", code, rep.Error)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("report written where the missing sheet belongs")
	}
	elsewhere := filepath.Join(root, "r.json")
	stdout, _, code = runCLI(t, "kicad", "render", "--sch", sch, "--out", filepath.Join(root, "out"), "--report", elsewhere)
	rep = kicadRenderReport{}
	_ = json.Unmarshal([]byte(stdout), &rep)
	if code == 0 || rep.OK {
		t.Errorf("incomplete closure rendered: exit %d", code)
	}
	if _, err := os.Stat(elsewhere); !os.IsNotExist(err) {
		t.Error("report file written although the closure is incomplete")
	}
	if !strings.Contains(strings.Join(rep.Warnings, " "), "stdout only") {
		t.Errorf("no note that the report stayed on stdout: %v", rep.Warnings)
	}
}

// A linked .kicad_pro is protected by its resolved target too.
func TestKicadRenderReportLinkedProjectTarget(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	_ = os.MkdirAll(proj, 0o755)
	sch := filepath.Join(proj, "top.kicad_sch")
	_ = os.WriteFile(sch, []byte("(kicad_sch (version 20231120) (uuid \"11111111-1111-1111-1111-111111111111\"))\n"), 0o644)
	target := filepath.Join(root, "elsewhere", "top.kicad_pro")
	_ = os.MkdirAll(filepath.Dir(target), 0o755)
	_ = os.WriteFile(target, []byte("{}"), 0o644)
	if err := os.Symlink(target, filepath.Join(proj, "top.kicad_pro")); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}
	stdout, _, code := runCLI(t, "kicad", "render", "--sch", sch, "--out", filepath.Join(root, "out"), "--report", target)
	if code != 1 || !strings.Contains(stdout, "RENDER_BAD_INPUT") {
		t.Errorf("report on the linked .kicad_pro target: exit %d", code)
	}
	if b, _ := os.ReadFile(target); string(b) != "{}" {
		t.Fatal("linked .kicad_pro target overwritten")
	}
}
