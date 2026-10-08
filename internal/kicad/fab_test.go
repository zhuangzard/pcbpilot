package kicad

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixBoard = "testdata/fab/fab_board.kicad_pcb"

func loadFix(t *testing.T) *Board {
	t.Helper()
	bd, err := ReadBoard(fixBoard)
	if err != nil {
		t.Fatal(err)
	}
	return bd
}

func TestJLCGerberLayers4Layer(t *testing.T) {
	got := JLCGerberLayers(loadFix(t))
	want := []string{"F.Cu", "In1.Cu", "In2.Cu", "B.Cu", "F.Mask", "B.Mask", "F.SilkS", "B.SilkS", "F.Paste", "B.Paste", "Edge.Cuts"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("layers\n got %v\nwant %v", got, want)
	}
}

func TestJLCGerberLayersLegacyIDs(t *testing.T) {
	// KiCad ≤8 numbering (B.Cu = 31) and long silkscreen names.
	bd, err := ParseBoard([]byte(`(kicad_pcb (layers (0 "F.Cu" signal) (31 "B.Cu" signal) (37 "F.Silkscreen" user) (44 "Edge.Cuts" user)))`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"F.Cu", "B.Cu", "F.Silkscreen", "Edge.Cuts"}
	if got := JLCGerberLayers(bd); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseBoardFootprints(t *testing.T) {
	bd := loadFix(t)
	by := map[string]Footprint{}
	for _, f := range bd.Footprints {
		by[f.Ref] = f
	}
	if len(by) != 8 {
		t.Fatalf("want 8 footprints, got %d", len(by))
	}
	if f := by["R2"]; !f.DNP {
		t.Errorf("R2 should be DNP")
	}
	if f := by["H1"]; !f.ExcludeBOM || !f.ExcludePos {
		t.Errorf("H1 flags: %+v", f)
	}
	if f := by["D1"]; f.Layer != "B.Cu" || f.Rot != 270 || f.X != 125 {
		t.Errorf("D1: %+v", f)
	}
	if v, fld := LookupLCSC(by["D1"].Fields); v != "C2286" || fld != "JLCPCB Part #" {
		t.Errorf("D1 lcsc %q from %q", v, fld)
	}
}

func TestMergeAndCheckBoardOnly(t *testing.T) {
	parts, warns := MergeParts(loadFix(t), nil, false)
	if len(warns) != 0 {
		t.Fatalf("warns %v", warns)
	}
	missing, invalid := CheckLCSC(parts)
	if len(missing) != 1 || missing[0].Ref != "U1" {
		t.Fatalf("missing %+v", missing)
	}
	if len(invalid) != 1 || invalid[0].Ref != "Q1" || invalid[0].LCSC != "AO3400A" {
		t.Fatalf("invalid %+v", invalid)
	}
}

func TestMergeSchematicWins(t *testing.T) {
	sch := []SchPart{
		{Ref: "R1", Value: "10k", LCSC: "C25744", LCSCField: "LCSC"},
		{Ref: "R3", Value: "10k", LCSC: "C11702", LCSCField: "LCSC Part #"}, // differs from board
		{Ref: "U1", Value: "LDO", LCSC: "C6186", LCSCField: "LCSC"},
		{Ref: "Q1", Value: "AO3400", LCSC: "C20917", LCSCField: "LCSC"},
		{Ref: "D1", Value: "RED", DNP: true},
		{Ref: "R2", Value: "0R"},
		// J1 absent → excluded, warned
	}
	parts, warns := MergeParts(loadFix(t), sch, true)
	missing, invalid := CheckLCSC(parts)
	if len(missing)+len(invalid) != 0 {
		t.Fatalf("missing %+v invalid %+v", missing, invalid)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "J1") || !strings.Contains(joined, "R3: schematic LCSC C11702 differs") {
		t.Fatalf("warns: %s", joined)
	}
	for _, p := range parts {
		if p.Ref == "D1" && p.Assembled() {
			t.Errorf("D1 is DNP in the schematic")
		}
		if p.Ref == "R3" && (p.LCSC != "C11702" || p.LCSCSource != "sch:LCSC Part #") {
			t.Errorf("R3: %+v", p)
		}
	}
}

func TestBuildBOMGroups(t *testing.T) {
	parts := []Part{
		{Ref: "R10", Value: "10k", Footprint: "Resistor_SMD:R_0402_1005Metric", LCSC: "C25744"},
		{Ref: "R2", Value: "10k", Footprint: "Resistor_SMD:R_0402_1005Metric", LCSC: "C25744"},
		{Ref: "C1", Value: "100nF", Footprint: "Capacitor_SMD:C_0402_1005Metric", LCSC: "C1525"},
		{Ref: "R5", Value: "0R", Footprint: "R_0402", DNP: true},
	}
	got := BuildBOM(parts)
	want := [][]string{
		{"Comment", "Designator", "Footprint", "LCSC Part #"},
		{"100nF", "C1", "C_0402_1005Metric", "C1525"},
		{"10k", "R2,R10", "R_0402_1005Metric", "C25744"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestConvertPosToCPL(t *testing.T) {
	pos := "Ref,Val,Package,PosX,PosY,Rot,Side\n" +
		`"R1","10k","R_0402_1005Metric",110.000000,-100.000000,90.000000,top` + "\n" +
		`"U1","LDO","SOT-23",120.000000,-105.000000,180.000000,top` + "\n" +
		`"D1","RED","LED_0603_1608Metric",125.000000,-110.000000,-90.000000,bottom` + "\n"
	ov := &CPLOverrides{
		Refs:       map[string]RotationOverride{"D1": {Rotate: 180, DX: 0.1}},
		Footprints: map[string]RotationOverride{"SOT-23": {Rotate: 270}},
	}
	rows, applied, err := ConvertPosToCPL(strings.NewReader(pos), ov,
		map[string]string{"U1": "Package_TO_SOT_SMD:SOT-23", "D1": "LED_SMD:LED_0603_1608Metric"})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		CPLHeader,
		{"R1", "110mm", "-100mm", "Top", "90"},
		{"U1", "120mm", "-105mm", "Top", "90"},
		{"D1", "125.1mm", "-110mm", "Bottom", "90"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows\n got %v\nwant %v", rows, want)
	}
	if len(applied) != 2 {
		t.Fatalf("applied %v", applied)
	}
}

func TestConvertPosMissingColumn(t *testing.T) {
	if _, _, err := ConvertPosToCPL(strings.NewReader("Ref,PosX\nR1,1\n"), nil, nil); err == nil {
		t.Fatal("want error")
	}
}

func TestLoadCPLOverridesRejectsUnknown(t *testing.T) {
	p := filepath.Join(t.TempDir(), "o.json")
	os.WriteFile(p, []byte(`{"refs":{"U1":{"rotation":90}}}`), 0o644)
	if _, err := LoadCPLOverrides(p); err == nil {
		t.Fatal("typo'd key must be rejected")
	}
}

func TestCLIArgsMatchJLC(t *testing.T) {
	g := strings.Join(GerberArgs("b.kicad_pcb", "out", []string{"F.Cu", "B.Cu"}), " ")
	for _, s := range []string{"--subtract-soldermask", "--check-zones", "--precision 6", "--layers F.Cu,B.Cu"} {
		if !strings.Contains(g, s) {
			t.Errorf("gerber args missing %q: %s", s, g)
		}
	}
	for _, s := range []string{"--no-protel-ext", "--no-x2", "--no-netlist"} {
		if strings.Contains(g, s) {
			t.Errorf("gerber args must not contain %q", s)
		}
	}
	d := strings.Join(DrillArgs("b.kicad_pcb", "out"), " ")
	for _, s := range []string{"--format excellon", "--drill-origin absolute", "--excellon-zeros-format decimal",
		"--excellon-oval-format alternate", "--excellon-units mm", "--excellon-separate-th"} {
		if !strings.Contains(d, s) {
			t.Errorf("drill args missing %q: %s", s, d)
		}
	}
	p := strings.Join(PosArgs("b.kicad_pcb", "p.csv"), " ")
	for _, s := range []string{"--format csv", "--units mm", "--side both", "--exclude-dnp"} {
		if !strings.Contains(p, s) {
			t.Errorf("pos args missing %q", s)
		}
	}
}

func TestIsFabFile(t *testing.T) {
	yes := []string{"b-F_Cu.gtl", "b-In1_Cu.g1", "b-Edge_Cuts.gm1", "b-PTH.drl", "b-NPTH.drl", "b-F_Paste.gtp", "x.gbr"}
	no := []string{"b-job.gbrjob", "b-PTH-drl_map.gbr", "b-pos.csv", "readme.txt"}
	for _, n := range yes {
		if !isFabFile(n) {
			t.Errorf("%s should be zipped", n)
		}
	}
	for _, n := range no {
		if isFabFile(n) {
			t.Errorf("%s should not be zipped", n)
		}
	}
}

func TestParseSchBOM(t *testing.T) {
	csv := `"Reference","Value","Footprint","DNP","EXCLUDE_FROM_BOM","LCSC Part #","LCSC","LCSC Part","JLCPCB Part #"` + "\n" +
		`"R1","10k","R:R_0402","","","","C25744","",""` + "\n" +
		`"R2,R3","1k","R:R_0402","DNP","","C11702","","",""` + "\n"
	got, err := ParseSchBOM([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].LCSC != "C25744" || got[0].LCSCField != "LCSC" || !got[2].DNP || got[2].Ref != "R3" {
		t.Fatalf("%+v", got)
	}
}

func TestParseAssignments(t *testing.T) {
	m, err := ParseAssignments([]string{"R1=c25744", "C1=C1525,C2=C1525"})
	if err != nil || m["R1"] != "C25744" || m["C2"] != "C1525" || len(m) != 3 {
		t.Fatalf("%v %v", m, err)
	}
	for _, bad := range []string{"R1", "R1=25744", "=C1"} {
		if _, err := ParseAssignments([]string{bad}); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestZipFiles(t *testing.T) {
	d := t.TempDir()
	a := filepath.Join(d, "a.gtl")
	os.WriteFile(a, []byte("G04*"), 0o644)
	z := filepath.Join(d, "o.zip")
	if err := ZipFiles(z, []string{a}); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(z)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != 1 || r.File[0].Name != "a.gtl" {
		t.Fatalf("zip %v", r.File)
	}
}

func TestRunFabGateFailsBeforeWriting(t *testing.T) {
	out := t.TempDir()
	rep, err := RunFab(FabOptions{PCB: fixBoard, OutDir: out, Tools: FabTools{CLI: "/nonexistent/kicad-cli"}})
	if !errors.Is(err, ErrMissingLCSC) {
		t.Fatalf("want ErrMissingLCSC, got %v", err)
	}
	if rep == nil || len(rep.Missing) != 1 || len(rep.Invalid) != 1 {
		t.Fatalf("report %+v", rep)
	}
	if ents, _ := os.ReadDir(out); len(ents) != 0 {
		t.Fatalf("nothing may be written on a failed gate, got %d entries", len(ents))
	}
}

// ── fab.py set-sch (needs any python3, not KiCad) ──────────────────────────

func python3(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	return p
}

func copyFixtures(t *testing.T, names ...string) string {
	t.Helper()
	d := t.TempDir()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join("testdata/fab", n))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(d, n), b, 0o644)
	}
	return d
}

func TestFabPySetSchematicHierarchy(t *testing.T) {
	py := python3(t)
	d := copyFixtures(t, "fab_root.kicad_sch", "fab_sub.kicad_sch")
	root := filepath.Join(d, "fab_root.kicad_sch")
	subBefore, _ := os.ReadFile(filepath.Join(d, "fab_sub.kicad_sch"))
	res, err := SetLCSC(FabTools{Python: py}, "", root, map[string]string{"R1": "C25744", "U1": "C6186", "X9": "C1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Changed) != 2 || !reflect.DeepEqual(res[0].NotFound, []string{"X9"}) {
		t.Fatalf("%+v", res)
	}
	// Both files still parse, and the fields are readable.
	for name, want := range map[string]map[string]string{
		"fab_root.kicad_sch": {"R1": "C25744"},
		"fab_sub.kicad_sch":  {"U1": "C6186"},
	} {
		b, _ := os.ReadFile(filepath.Join(d, name))
		n, err := parseSexpr(b)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, sym := range n.children("symbol") {
			fields := map[string]string{}
			for _, p := range sym.children("property") {
				fields[p.arg(0)] = p.arg(1)
			}
			v, _ := LookupLCSC(fields)
			if v != want[fields["Reference"]] {
				t.Errorf("%s %s: lcsc %q", name, fields["Reference"], v)
			}
		}
	}
	// U1 already had an (empty) "LCSC" field: only that value string changes.
	subAfter, _ := os.ReadFile(filepath.Join(d, "fab_sub.kicad_sch"))
	if want := bytes.Replace(subBefore, []byte(`(property "LCSC" ""`), []byte(`(property "LCSC" "C6186"`), 1); !bytes.Equal(subAfter, want) {
		t.Errorf("sub sheet edit was not minimal:\n%s", subAfter)
	}
	// R1 had none: a hidden "LCSC Part #" is inserted.
	rootAfter, _ := os.ReadFile(root)
	if !bytes.Contains(rootAfter, []byte(`(property "LCSC Part #" "C25744"`)) {
		t.Errorf("root: inserted field missing")
	}
	// Idempotent.
	res, err = SetLCSC(FabTools{Python: py}, "", root, map[string]string{"R1": "C25744", "U1": "C6186"}, "")
	if err != nil || len(res[0].Changed) != 0 {
		t.Fatalf("second run changed %+v %v", res, err)
	}
}

// ── live (KiCad installed) ─────────────────────────────────────────────────

func liveTools(t *testing.T) FabTools {
	t.Helper()
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run KiCad live tests")
	}
	cli, err := ResolveFabCLI()
	if err != nil {
		t.Fatal(err)
	}
	py, err := ResolveFabPython()
	if err != nil {
		t.Fatal(err)
	}
	return FabTools{CLI: cli, Python: py}
}

func TestLiveSetPCBThenFab(t *testing.T) {
	tools := liveTools(t)
	d := copyFixtures(t, "fab_board.kicad_pcb")
	pcb := filepath.Join(d, "fab_board.kicad_pcb")
	res, err := SetLCSC(tools, pcb, "", map[string]string{"U1": "C6186", "Q1": "C20917"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Changed) != 2 {
		t.Fatalf("%+v", res)
	}
	bd, err := ReadBoard(pcb)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range bd.Footprints {
		if f.Ref == "Q1" {
			// Q1 already had "LCSC Part" (invalid value) → updated in place.
			if f.Fields["LCSC Part"] != "C20917" {
				t.Errorf("Q1 fields %v", f.Fields)
			}
		}
		if f.Ref == "U1" && f.Fields["LCSC Part #"] != "C6186" {
			t.Errorf("U1 fields %v", f.Fields)
		}
	}

	out := filepath.Join(d, "fab")
	os.MkdirAll(out, 0o755)
	rep, err := RunFab(FabOptions{PCB: pcb, OutDir: out, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(rep.Zip)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	all := strings.Join(names, " ")
	for _, ext := range []string{".gtl", ".g1", ".g2", ".gbl", ".gts", ".gbs", ".gto", ".gbo", ".gtp", ".gbp", ".gm1", "-PTH.drl", "-NPTH.drl"} {
		if !strings.Contains(all, ext) {
			t.Errorf("zip lacks %s: %v", ext, names)
		}
	}
	if strings.Contains(all, "drl_map") || strings.Contains(all, "gbrjob") {
		t.Errorf("zip has non-fab files: %v", names)
	}
	cpl, _ := os.ReadFile(rep.CPL)
	if !strings.HasPrefix(string(cpl), "Designator,Mid X,Mid Y,Layer,Rotation\n") ||
		!strings.Contains(string(cpl), "D1,125mm,-110mm,Bottom,") ||
		strings.Contains(string(cpl), "R2,") || strings.Contains(string(cpl), "H1,") {
		t.Errorf("cpl:\n%s", cpl)
	}
	bom, _ := os.ReadFile(rep.BOM)
	if !strings.Contains(string(bom), "10k,\"R1,R3\",R_0402_1005Metric,C25744") || strings.Contains(string(bom), "R2") {
		t.Errorf("bom:\n%s", bom)
	}
	var back FabReport
	b, _ := os.ReadFile(filepath.Join(out, "fab-report.json"))
	if err := json.Unmarshal(b, &back); err != nil || back.BOMLines != 5 {
		t.Errorf("report %+v %v", back, err)
	}
}

func TestLiveSchematicBOM(t *testing.T) {
	tools := liveTools(t)
	d := copyFixtures(t, "fab_root.kicad_sch", "fab_sub.kicad_sch")
	rows, err := ReadSchematicParts(tools.CLI, filepath.Join(d, "fab_root.kicad_sch"))
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]bool{}
	for _, r := range rows {
		refs[r.Ref] = true
	}
	if !refs["R1"] || !refs["U1"] {
		t.Fatalf("hierarchy not resolved: %+v", rows)
	}
}
