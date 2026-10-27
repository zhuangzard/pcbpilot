package kicad

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The test binary doubles as a fake kicad-cli (PCBPILOT_FAKE_KICAD_CLI=1),
// so RunFab's staging and publishing run without KiCad installed.
func TestMain(m *testing.M) {
	if os.Getenv("PCBPILOT_FAKE_KICAD_CLI") == "1" {
		os.Exit(fakeKiCadCLI(os.Args[1:]))
	}
	os.Exit(m.Run())
}

var fakeLayerExt = map[string]string{"F.Cu": "gtl", "B.Cu": "gbl", "F.Mask": "gts", "B.Mask": "gbs",
	"F.SilkS": "gto", "B.SilkS": "gbo", "F.Paste": "gtp", "B.Paste": "gbp", "Edge.Cuts": "gm1"}

// fakeKiCadCLI writes empty files named the way kicad-cli names them for
// `pcb export gerbers|drill|pos`; pos copies $PCBPILOT_FAKE_POS.
func fakeKiCadCLI(args []string) int {
	flag := func(name string) string {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == name {
				return args[i+1]
			}
		}
		return ""
	}
	pcb := args[len(args)-1]
	base := strings.TrimSuffix(filepath.Base(pcb), filepath.Ext(pcb))
	out := flag("--output")
	touch := func(name string) {
		_ = os.WriteFile(filepath.Join(out, name), []byte("fake\n"), 0o644)
	}
	switch strings.Join(args[:3], " ") {
	case "pcb export gerbers":
		for _, l := range strings.Split(flag("--layers"), ",") {
			ext := fakeLayerExt[l]
			if strings.HasPrefix(l, "In") {
				ext = "g" + strings.TrimSuffix(strings.TrimPrefix(l, "In"), ".Cu")
			}
			touch(base + "-" + strings.ReplaceAll(l, ".", "_") + "." + ext)
		}
		touch(base + "-job.gbrjob")
	case "pcb export drill":
		touch(base + "-PTH.drl")
		touch(base + "-NPTH.drl")
		touch(base + "-PTH-drl_map.gbr")
	case "pcb export pos":
		b, err := os.ReadFile(os.Getenv("PCBPILOT_FAKE_POS"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			return 1
		}
	default:
		fmt.Fprintln(os.Stderr, "fake kicad-cli: unsupported", args)
		return 2
	}
	return 0
}

func fakeCLI(t *testing.T) FabTools {
	t.Helper()
	t.Setenv("PCBPILOT_FAKE_KICAD_CLI", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return FabTools{CLI: exe}
}

var reInnerLayers = regexp.MustCompile(`\(\d+ "In\d+\.Cu" [a-z]+\)\s*`)

// writeBoard copies the 4-layer fixture to dir/name, optionally as a
// 2-layer board (inner copper rows dropped from the layer table).
func writeBoard(t *testing.T, dir, name string, twoLayer bool) string {
	t.Helper()
	b, err := os.ReadFile(fixBoard)
	if err != nil {
		t.Fatal(err)
	}
	if twoLayer {
		b = reInnerLayers.ReplaceAll(b, nil)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func hasInner(names []string) bool {
	for _, n := range names {
		if regexp.MustCompile(`In\d+_Cu|\.g\d+$`).MatchString(n) {
			return true
		}
	}
	return false
}

// A 4-layer run then a 2-layer run into the same OUT: the second zip (and
// OUT/gerber) must hold no inner layer, user files must survive, and a
// Gerber pcbpilot never wrote is reported but neither zipped nor deleted.
func TestRunFabRerunDropsStaleGerbers(t *testing.T) {
	tools := fakeCLI(t)
	src, out := t.TempDir(), t.TempDir()
	gdir := filepath.Join(out, "gerber")
	os.MkdirAll(gdir, 0o755)
	// A leftover from a pcbpilot without a manifest, and a user file.
	os.WriteFile(filepath.Join(gdir, "board-In3_Cu.g3"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(gdir, "notes.txt"), []byte("mine"), 0o644)
	os.WriteFile(filepath.Join(out, "panel.txt"), []byte("mine"), 0o644)

	rep, err := RunFab(FabOptions{PCB: writeBoard(t, src, "board.kicad_pcb", false), OutDir: out, NoAssembly: true, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if z := zipNames(t, rep.Zip); !hasInner(z) || strings.Contains(strings.Join(z, " "), "In3") {
		t.Fatalf("4-layer zip: %v", z)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "board-In3_Cu.g3") {
		t.Fatalf("unowned leftover not reported: %v", rep.Warnings)
	}

	rep, err = RunFab(FabOptions{PCB: writeBoard(t, src, "board.kicad_pcb", true), OutDir: out, NoAssembly: true, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	z := zipNames(t, rep.Zip)
	if hasInner(z) || len(z) != 11 { // 2 Cu + 2 mask + 2 silk + 2 paste + edge + PTH + NPTH
		t.Fatalf("2-layer zip has inner layers or wrong count: %v", z)
	}
	g := dirNames(t, gdir)
	for _, n := range g {
		if strings.Contains(n, "In1_Cu") || strings.Contains(n, "In2_Cu") {
			t.Errorf("stale pcbpilot Gerber kept in %s: %v", gdir, g)
		}
	}
	for _, keep := range []string{filepath.Join(gdir, "board-In3_Cu.g3"), filepath.Join(gdir, "notes.txt"), filepath.Join(out, "panel.txt")} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("file pcbpilot does not own was touched: %v", err)
		}
	}
	for _, n := range dirNames(t, out) {
		if strings.HasPrefix(n, ".pcbpilot-fab-") && n != fabManifestName {
			t.Errorf("staging dir left behind: %s", n)
		}
	}

	// Renamed board: the old name's zip and Gerbers were pcbpilot's → gone.
	rep, err = RunFab(FabOptions{PCB: writeBoard(t, src, "renamed.kicad_pcb", true), OutDir: out, NoAssembly: true, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "board-gerber.zip")); !os.IsNotExist(err) {
		t.Errorf("old board's zip kept: %v", err)
	}
	for _, n := range dirNames(t, gdir) {
		if strings.HasPrefix(n, "board-") && n != "board-In3_Cu.g3" {
			t.Errorf("old board's Gerber kept: %s", n)
		}
	}
	for _, n := range zipNames(t, rep.Zip) {
		if !strings.HasPrefix(n, "renamed-") {
			t.Errorf("renamed zip holds %s", n)
		}
	}
}

// A manifest pointing outside OUT never removes anything there.
func TestPublishFabIgnoresUnsafeManifest(t *testing.T) {
	root := t.TempDir()
	out := filepath.Join(root, "out")
	os.MkdirAll(filepath.Join(out, "gerber"), 0o755)
	victim := filepath.Join(root, "victim.txt")
	os.WriteFile(victim, []byte("x"), 0o644)
	os.WriteFile(filepath.Join(out, fabManifestName), []byte(`{"files":["../victim.txt","..","`+strings.ReplaceAll(victim, `\`, `\\`)+`"],"gerbers":["../../victim.txt"]}`), 0o644)
	stage := filepath.Join(out, ".stage")
	os.MkdirAll(filepath.Join(stage, "gerber"), 0o755)
	if err := publishFab(out, stage, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("manifest entry outside OUT removed a file: %v", err)
	}
}

// ── BOM / CPL consistency ──────────────────────────────────────────────────

func cplRows(t *testing.T, csvText string) [][]string {
	t.Helper()
	rows, _, err := ConvertPosToCPL(strings.NewReader(csvText), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func codes(ps []FabProblem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Code+":"+p.Ref)
		if p.Fix == "" || p.Message == "" {
			out = append(out, "NO-FIX:"+p.Ref)
		}
	}
	return out
}

const posHead = "Ref,Val,Package,PosX,PosY,Rot,Side\n"

func TestCheckBOMCPL(t *testing.T) {
	smd := func(ref string) Part { return Part{Ref: ref, Value: "10k", Footprint: "R:R_0402", LCSC: "C1"} }
	dnp, exBOM, exPos, virt := smd("R4"), smd("R5"), smd("H1"), smd("FID1")
	dnp.DNP, exBOM.ExcludeBOM, exPos.ExcludePos = true, true, true
	virt.ExcludeBOM, virt.ExcludePos = true, true
	tht := Part{Ref: "J1", Value: "Conn", Footprint: "C:PinHeader", LCSC: "C2"}
	parts := []Part{smd("R1"), smd("R2"), tht, dnp, exBOM, exPos, virt}

	for _, tc := range []struct {
		name, pos string
		parts     []Part
		want      []string
	}{
		{"consistent (flags honoured, THT placed)", posHead +
			"R1,10k,R_0402,1,2,0,top\nR2,10k,R_0402,3,4,90,top\nJ1,Conn,PinHeader,5,6,0,bottom\n", parts, nil},
		{"BOM part missing from CPL", posHead + "R1,10k,R_0402,1,2,0,top\nJ1,Conn,PinHeader,5,6,0,top\n", parts,
			[]string{"BOM_NOT_IN_CPL:R2"}},
		{"CPL part not in BOM: excluded from BOM only", posHead +
			"R1,10k,R_0402,1,2,0,top\nR2,10k,R_0402,3,4,0,top\nJ1,Conn,PinHeader,5,6,0,top\nR5,10k,R_0402,7,8,0,top\n", parts,
			[]string{"CPL_NOT_IN_BOM:R5"}},
		{"CPL part not in BOM: DNP only in the schematic", posHead +
			"R1,10k,R_0402,1,2,0,top\nR2,10k,R_0402,3,4,0,top\nJ1,Conn,PinHeader,5,6,0,top\nR4,10k,R_0402,7,8,0,top\n", parts,
			[]string{"CPL_NOT_IN_BOM:R4"}},
		{"CPL part unknown to the board", posHead +
			"R1,10k,R_0402,1,2,0,top\nR2,10k,R_0402,3,4,0,top\nJ1,Conn,PinHeader,5,6,0,top\nU9,MCU,QFN,7,8,0,top\n", parts,
			[]string{"CPL_NOT_IN_BOM:U9"}},
		{"duplicate designator in CPL", posHead +
			"R1,10k,R_0402,1,2,0,top\nR2,10k,R_0402,3,4,0,top\nR2,10k,R_0402,9,9,0,top\nJ1,Conn,PinHeader,5,6,0,top\n",
			append(append([]Part{}, parts...), smd("R2")), []string{"CPL_DUPLICATE_REF:R2"}},
		{"empty designator in CPL", posHead +
			"R1,10k,R_0402,1,2,0,top\nR2,10k,R_0402,3,4,0,top\n,10k,R_0402,9,9,0,top\nJ1,Conn,PinHeader,5,6,0,top\n", parts,
			[]string{"CPL_EMPTY_REF:"}},
		{"duplicate designator in BOM only", posHead +
			"R1,10k,R_0402,1,2,0,top\nR2,10k,R_0402,3,4,0,top\nJ1,Conn,PinHeader,5,6,0,top\n",
			append(append([]Part{}, parts...), smd("R1")), []string{"BOM_DUPLICATE_REF:R1"}},
	} {
		got := codes(CheckBOMCPL(tc.parts, cplRows(t, tc.pos)))
		if strings.Join(got, " ") != strings.Join(tc.want, " ") {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestAssembledCPL(t *testing.T) {
	parts := []Part{{Ref: "R1"}, {Ref: "JP1", ExcludeBOM: true}, {Ref: "R4", DNP: true}, {Ref: "R7"}, {Ref: "R7", ExcludeBOM: true}}
	rows := cplRows(t, posHead+"R1,a,b,1,1,0,top\nJP1,a,b,1,1,0,top\nR4,a,b,1,1,0,top\nR7,a,b,1,1,0,top\nU9,a,b,1,1,0,top\n")
	kept, omitted := AssembledCPL(rows, parts)
	var refs []string
	for _, r := range kept[1:] {
		refs = append(refs, r[0])
	}
	if strings.Join(refs, " ") != "R1 R7 U9" || strings.Join(omitted, " ") != "JP1 R4" || kept[0][0] != "Designator" {
		t.Fatalf("kept %v omitted %v", refs, omitted)
	}
}

func TestParseBoardVirtualAttr(t *testing.T) {
	bd, err := ParseBoard([]byte(`(kicad_pcb (layers (0 "F.Cu" signal)) (footprint "Fiducial:F" (layer "F.Cu") (at 1 2) (property "Reference" "FID1") (attr smd virtual)))`))
	if err != nil {
		t.Fatal(err)
	}
	if f := bd.Footprints[0]; !f.ExcludeBOM || !f.ExcludePos {
		t.Fatalf("virtual footprint %+v", f)
	}
}

// RunFab stops on a mismatch and publishes nothing (not even fab-report.json).
func TestRunFabBOMCPLMismatchWritesNothing(t *testing.T) {
	tools := fakeCLI(t)
	src, out := t.TempDir(), t.TempDir()
	pcb := writeBoard(t, src, "board.kicad_pcb", false)
	// Give every assembled fixture part a valid LCSC number so the LCSC gate passes.
	b, _ := os.ReadFile(pcb)
	b = []byte(strings.ReplaceAll(string(b), `(property "LCSC Part" "AO3400A"`, `(property "LCSC Part" "C20917"`))
	b = []byte(strings.Replace(string(b), `(property "Reference" "U1"`, `(property "LCSC" "C6186") (property "Reference" "U1"`, 1))
	os.WriteFile(pcb, b, 0o644)
	bd, err := ReadBoard(pcb)
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := MergeParts(bd, nil, false)
	if m, inv := CheckLCSC(parts); len(m)+len(inv) > 0 {
		t.Fatalf("fixture edit did not satisfy the LCSC gate: %+v %+v", m, inv)
	}
	var pos strings.Builder
	pos.WriteString(posHead)
	for i, p := range parts {
		if p.Assembled() && p.Ref != "R3" { // R3 dropped from the CPL
			fmt.Fprintf(&pos, "%s,%s,x,%d,0,0,top\n", p.Ref, p.Value, i)
		}
	}
	pos.WriteString("H1,MountingHole,x,50,0,0,top\n") // excluded from BOM: left out, not a mismatch
	posFile := filepath.Join(src, "pos.csv")
	os.WriteFile(posFile, []byte(pos.String()), 0o644)
	t.Setenv("PCBPILOT_FAKE_POS", posFile)

	rep, err := RunFab(FabOptions{PCB: pcb, OutDir: out, Tools: tools})
	if !errors.Is(err, ErrBOMCPLMismatch) || rep == nil {
		t.Fatalf("want ErrBOMCPLMismatch, got %v", err)
	}
	if got := codes(rep.Problems); strings.Join(got, " ") != "BOM_NOT_IN_CPL:R3" {
		t.Fatalf("problems %v", got)
	}
	if n := dirNames(t, out); len(n) != 0 {
		t.Fatalf("a failed check must publish nothing, got %v", n)
	}

	// With the CPL complete the same run publishes.
	pos.WriteString("R3,10k,x,99,0,0,top\n")
	os.WriteFile(posFile, []byte(pos.String()), 0o644)
	rep, err = RunFab(FabOptions{PCB: pcb, OutDir: out, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{rep.Zip, rep.BOM, rep.CPL, filepath.Join(out, fabReportName), filepath.Join(out, fabManifestName)} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
	f, _ := os.Open(rep.CPL)
	cpl, _ := io.ReadAll(f)
	f.Close()
	if !strings.Contains(string(cpl), "R3,") || strings.Contains(string(cpl), "H1,") || strings.Join(rep.CPLOmitted, " ") != "H1" {
		t.Errorf("cpl:\n%s", cpl)
	}
}

// ── live (KiCad installed) ─────────────────────────────────────────────────

// kicadDemo copies a project from KiCad's shipped demos into a temp dir.
func kicadDemo(t *testing.T, cli, project string) string {
	t.Helper()
	src := filepath.Join(filepath.Dir(filepath.Dir(cli)), "share", "kicad", "demos", project)
	if _, err := os.Stat(src); err != nil {
		t.Skipf("KiCad demo %s not installed: %v", project, err)
	}
	dst := filepath.Join(t.TempDir(), project)
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// The stale-Gerber case with real kicad-cli output: the 4-layer fixture,
// then KiCad's 2-layer ecc83 demo under the same board name, into one OUT.
func TestLiveFabFourThenTwoLayers(t *testing.T) {
	tools := liveTools(t)
	out := filepath.Join(t.TempDir(), "fab")
	os.MkdirAll(out, 0o755)
	four := writeBoard(t, t.TempDir(), "board.kicad_pcb", false)
	rep, err := RunFab(FabOptions{PCB: four, OutDir: out, NoAssembly: true, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if z := zipNames(t, rep.Zip); !hasInner(z) {
		t.Fatalf("4-layer zip lacks inner layers: %v", z)
	}
	demo := kicadDemo(t, tools.CLI, "ecc83")
	two := filepath.Join(demo, "board.kicad_pcb")
	if err := os.Rename(filepath.Join(demo, "ecc83-pp.kicad_pcb"), two); err != nil {
		t.Fatal(err)
	}
	os.Rename(filepath.Join(demo, "ecc83-pp.kicad_pro"), filepath.Join(demo, "board.kicad_pro"))
	rep, err = RunFab(FabOptions{PCB: two, OutDir: out, NoAssembly: true, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	z := zipNames(t, rep.Zip)
	if hasInner(z) || hasInner(dirNames(t, filepath.Join(out, "gerber"))) {
		t.Fatalf("2-layer run kept inner layers: zip %v, gerber/ %v", z, dirNames(t, filepath.Join(out, "gerber")))
	}
	t.Logf("2-layer zip: %v", z)
}

// The BOM/CPL check on a real demo board with real kicad-cli pos output:
// consistent as shipped; a footprint excluded from the BOM but still placed
// is caught.
func TestLiveBOMCPLOnDemo(t *testing.T) {
	tools := liveTools(t)
	demo := kicadDemo(t, tools.CLI, "pic_programmer")
	pcb := filepath.Join(demo, "pic_programmer.kicad_pcb")
	check := func() ([]FabProblem, []string) {
		bd, err := ReadBoard(pcb)
		if err != nil {
			t.Fatal(err)
		}
		parts, _ := MergeParts(bd, nil, false)
		pos := filepath.Join(demo, "pos.csv")
		if err := runCLI(tools.CLI, PosArgs(pcb, pos), nil); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(pos)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		rows, _, err := ConvertPosToCPL(f, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) < 10 {
			t.Fatalf("demo CPL too small: %d rows", len(rows))
		}
		rows, omitted := AssembledCPL(rows, parts)
		return CheckBOMCPL(parts, rows), omitted
	}
	// pic_programmer ships exclude-from-BOM parts (JP1, P101…) that kicad-cli
	// still writes to the pos file: they are left out, not a mismatch.
	p, omitted := check()
	if len(p) != 0 || !strings.Contains(" "+strings.Join(omitted, " ")+" ", " JP1 ") {
		t.Fatalf("shipped demo: problems %+v, omitted %v", p, omitted)
	}
	b, _ := os.ReadFile(pcb)
	if !bytes.Contains(b, []byte(`(property "Reference" "R2"`)) {
		t.Skip("demo layout changed: no R2")
	}
	os.WriteFile(pcb, bytes.Replace(b, []byte(`(property "Reference" "R2"`), []byte(`(property "Reference" "R1"`), 1), 0o644)
	if p, _ := check(); strings.Join(codes(p), " ") != "CPL_DUPLICATE_REF:R1" {
		t.Fatalf("duplicate R1: %v", codes(p))
	}
}
