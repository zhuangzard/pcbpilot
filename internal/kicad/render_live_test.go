package kicad

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

// renderDemoCopy copies a KiCad demo project into a fresh temp dir (the
// install is read-only), or skips: needs kicad-cli and the demos, not -short.
func renderDemoCopy(t *testing.T, demo string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("kicad-cli render: not in -short")
	}
	if _, err := KicadCLI(); err != nil {
		t.Skip(err)
	}
	demos := kicadDemosDir()
	if demos == "" {
		t.Skipf("KiCad demos not found (set %s)", EnvKiCadDemos)
	}
	dst := filepath.Join(t.TempDir(), demo)
	copyTree(t, filepath.Join(demos, demo), dst)
	return dst
}

func mustRender(t *testing.T, o RenderOptions) *RenderManifest {
	t.Helper()
	m, err := Render(o)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The same schematic rendered from two copies in different directories gives
// byte-identical SVGs; moving one symbol in a third copy changes only its page.
func TestRenderLiveSchDeterministicAndChange(t *testing.T) {
	a, b := renderDemoCopy(t, "pic_programmer"), renderDemoCopy(t, "pic_programmer")
	out := t.TempDir()
	ra, rb := filepath.Join(out, "a"), filepath.Join(out, "b")
	ma := mustRender(t, RenderOptions{Sch: filepath.Join(a, "pic_programmer.kicad_sch"), OutDir: ra})
	mustRender(t, RenderOptions{Sch: filepath.Join(b, "pic_programmer.kicad_sch"), OutDir: rb})
	if len(ma.Files) != 2 {
		t.Fatalf("pages %+v, want root + pic_sockets", ma.Files)
	}
	for _, f := range ma.Files {
		x, _ := os.ReadFile(filepath.Join(ra, f.File))
		y, _ := os.ReadFile(filepath.Join(rb, f.File))
		if len(x) == 0 || string(x) != string(y) {
			t.Fatalf("%s differs between two renders", f.File)
		}
		if strings.Contains(string(x), a) || strings.Contains(string(x), filepath.ToSlash(a)) {
			t.Errorf("%s holds the temp path", f.File)
		}
	}
	if r := CompareRenders(ra, rb); r.Verdict != gate.Pass {
		t.Fatalf("compare of identical renders: %s %+v", r.Verdict, r.Files)
	}

	// Move U1 (PIC12C508A) one grid step on the sub-sheet.
	sub := filepath.Join(b, "pic_sockets.kicad_sch")
	src, err := os.ReadFile(sub)
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, `(lib_id "pic_programmer:PIC12C508A")`)
	if i < 0 {
		t.Fatal("demo changed: no PIC12C508A on pic_sockets")
	}
	moved := s[:i] + strings.Replace(s[i:], "(at 95.25 73.66 0)", "(at 97.79 73.66 0)", 1)
	if moved == s {
		t.Fatal("demo changed: PIC12C508A not at 95.25 73.66")
	}
	if err := os.WriteFile(sub, []byte(moved), 0o644); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(out, "c")
	mustRender(t, RenderOptions{Sch: filepath.Join(b, "pic_programmer.kicad_sch"), OutDir: rc})
	r := CompareRenders(ra, rc)
	if r.Verdict != gate.Fail || r.Counts["changed"] != 1 || r.Counts["identical"] != 1 {
		t.Fatalf("after the move: %s %v", r.Verdict, r.Counts)
	}
	for _, f := range r.Files {
		if f.Status == "changed" && (f.File != "pic_programmer-pic_sockets.svg" || f.ChangedElements == 0 || f.Reordered) {
			t.Errorf("changed file %+v", f)
		}
	}
}

// A multi-sheet hierarchy (one sub-sheet used twice) renders every page,
// reproducibly.
func TestRenderLiveHierarchyDeterministic(t *testing.T) {
	a := renderDemoCopy(t, "complex_hierarchy")
	out := t.TempDir()
	r1, r2 := filepath.Join(out, "1"), filepath.Join(out, "2")
	m := mustRender(t, RenderOptions{Sch: filepath.Join(a, "complex_hierarchy.kicad_sch"), OutDir: r1})
	if len(m.Files) != 3 {
		t.Fatalf("pages %+v, want 3", m.Files)
	}
	mustRender(t, RenderOptions{Sch: filepath.Join(a, "complex_hierarchy.kicad_sch"), OutDir: r2})
	if r := CompareRenders(r1, r2); r.Verdict != gate.Pass || r.Counts["identical"] != 3 {
		t.Fatalf("%s %v", r.Verdict, r.Counts)
	}
}

// Board SVGs (composite + per layer) are reproducible; the bottom side is a
// different set of files.
func TestRenderLivePCBDeterministic(t *testing.T) {
	a, b := renderDemoCopy(t, "pic_programmer"), renderDemoCopy(t, "pic_programmer")
	out := t.TempDir()
	r1, r2, r3 := filepath.Join(out, "1"), filepath.Join(out, "2"), filepath.Join(out, "3")
	m := mustRender(t, RenderOptions{PCB: filepath.Join(a, "pic_programmer.kicad_pcb"), OutDir: r1})
	if len(m.Files) != 5 || m.Files[len(m.Files)-1].File != "pic_programmer-pcb-top.svg" {
		t.Fatalf("files %+v", m.Files)
	}
	for _, f := range m.Files {
		if f.Elements == 0 {
			t.Errorf("%s: no drawn element", f.File)
		}
	}
	mustRender(t, RenderOptions{PCB: filepath.Join(b, "pic_programmer.kicad_pcb"), OutDir: r2})
	if r := CompareRenders(r1, r2); r.Verdict != gate.Pass {
		t.Fatalf("%s %+v", r.Verdict, r.Files)
	}
	mustRender(t, RenderOptions{PCB: filepath.Join(a, "pic_programmer.kicad_pcb"), Side: "bottom", OutDir: r3})
	if r := CompareRenders(r1, r3); r.Verdict != gate.Fail || len(r.Notes) == 0 {
		t.Fatalf("top vs bottom: %s %v %v", r.Verdict, r.Counts, r.Notes)
	}
}
