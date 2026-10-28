package kicad

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStepAndIPCArgs(t *testing.T) {
	got := strings.Join(StepArgs("b.kicad_pcb", "o.step", StepOptions{}), " ")
	if got != "pcb export step --force --output o.step b.kicad_pcb" {
		t.Errorf("default step args: %s", got)
	}
	got = strings.Join(StepArgs("b.kicad_pcb", "o.step", StepOptions{BoardOnly: true, NoDNP: true, SubstModels: true}), " ")
	if got != "pcb export step --force --output o.step --subst-models --board-only --no-dnp b.kicad_pcb" {
		t.Errorf("full step args: %s", got)
	}
	got = strings.Join(IPCD356Args("b.kicad_pcb", "o.d356"), " ")
	if got != "pcb export ipcd356 --output o.d356 b.kicad_pcb" {
		t.Errorf("ipc args: %s", got)
	}
}

const modelBoard = `(kicad_pcb (version 20260101) (generator "t")
 (layers (0 "F.Cu" signal) (2 "B.Cu" signal))
 (footprint "Resistor_SMD:R_0402" (layer "F.Cu") (at 1 1)
  (property "Reference" "R1") (property "Value" "1k")
  (model "${KICAD10_3DMODEL_DIR}/Resistor_SMD.3dshapes/R_0402.step" (offset (xyz 0 0 0))))
 (footprint "Capacitor_SMD:C_0402" (layer "F.Cu") (at 2 1)
  (property "Reference" "C1") (property "Value" "100n"))
 (footprint "Capacitor_SMD:C_0603" (layer "F.Cu") (at 3 1)
  (property "Reference" "C10") (property "Value" "1u")
  (model "x.step" (hide yes)))
 (footprint "Capacitor_SMD:C_0603" (layer "F.Cu") (at 4 1)
  (property "Reference" "C2") (property "Value" "1u")
  (model "x.step" (hide no)))
 (footprint "MountingHole:M3" (layer "F.Cu") (at 5 1) (attr exclude_from_bom exclude_from_pos_files)
  (property "Reference" "H1") (property "Value" "M3")))`

func TestFootprintsWithoutModel(t *testing.T) {
	bd, err := ParseBoard([]byte(modelBoard))
	if err != nil {
		t.Fatal(err)
	}
	got := FootprintsWithoutModel(bd)
	want := []NoModelFootprint{
		{Ref: "C1", Footprint: "Capacitor_SMD:C_0402", Reason: "no 3D model", Assembled: true},
		{Ref: "C10", Footprint: "Capacitor_SMD:C_0603", Reason: "3D model hidden", Assembled: true},
		{Ref: "H1", Footprint: "MountingHole:M3", Reason: "no 3D model", Assembled: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestCheckSTEPFile(t *testing.T) {
	d := t.TempDir()
	w := func(name, body string) string {
		p := filepath.Join(d, name)
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	if err := CheckSTEPFile(w("ok.step", "ISO-10303-21;\nHEADER;\nENDSEC;\nEND-ISO-10303-21;\n")); err != nil {
		t.Errorf("valid: %v", err)
	}
	for name, body := range map[string]string{
		"empty.step": "", "junk.step": "hello", "trunc.step": "ISO-10303-21;\nHEADER;\nDATA;\n#1=X;\n",
	} {
		if err := CheckSTEPFile(w(name, body)); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
	if err := CheckSTEPFile(filepath.Join(d, "missing.step")); err == nil {
		t.Error("missing file must be rejected")
	}
}

func TestCountIPCD356Records(t *testing.T) {
	d := t.TempDir()
	w := func(name, body string) string {
		p := filepath.Join(d, name)
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	hdr := "P  VER   IPC-D-356A\r\n"
	n, err := CountIPCD356Records(w("a.d356", hdr+"327N/C   R1  -1\r\n317GND  U1 -2\r\n367X\r\n999\r\n"))
	if err != nil || n != 3 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for name, body := range map[string]string{
		"empty": "", "nohdr": "327N/C R1\n", "norec": hdr + "P  JOB x\n999\n",
	} {
		if _, err := CountIPCD356Records(w(name, body)); err == nil {
			t.Errorf("%s must be rejected", name)
		}
	}
}

func listing(t *testing.T, root string) []string {
	t.Helper()
	var l []string
	filepath.Walk(root, func(p string, st os.FileInfo, err error) error {
		if err == nil && !st.IsDir() {
			rel, _ := filepath.Rel(root, p)
			l = append(l, filepath.ToSlash(rel))
		}
		return nil
	})
	return l
}

func TestRunFabStepIPC356(t *testing.T) {
	tools := fakeCLI(t)
	out := t.TempDir()
	rep, err := RunFab(FabOptions{PCB: fixBoard, OutDir: out, NoAssembly: true, Step: true, IPC356: true, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Step == nil || rep.IPC356 == nil || len(rep.Step.SHA256) != 64 || rep.IPC356.Records != 1 {
		t.Fatalf("report %+v %+v", rep.Step, rep.IPC356)
	}
	if len(rep.Step.KiCadWarnings) != 1 || !strings.Contains(rep.Step.KiCadWarnings[0], "foo.wrl") {
		t.Errorf("warnings %v", rep.Step.KiCadWarnings)
	}
	if len(rep.Step.FootprintsNoModel) != 8 { // the fixture board has no 3D models at all
		t.Errorf("want all 8 footprints without a model, got %d", len(rep.Step.FootprintsNoModel))
	}
	files := strings.Join(listing(t, out), " ")
	for _, want := range []string{"fab_board.step", "fab_board.d356", "fab-report.json", "fab_board-gerber.zip"} {
		if !strings.Contains(files, want) {
			t.Errorf("OUT lacks %s: %s", want, files)
		}
	}
	var m fabManifest
	b, _ := os.ReadFile(filepath.Join(out, fabManifestName))
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(m.Files, " "), "fab_board.step") || !strings.Contains(strings.Join(m.Files, " "), "fab_board.d356") {
		t.Errorf("manifest %v", m.Files)
	}
	// The zip stays Gerber+drill only.
	for _, z := range rep.ZipFiles {
		if strings.HasSuffix(z, ".step") || strings.HasSuffix(z, ".d356") {
			t.Errorf("zip must not hold %s", z)
		}
	}
	// A rerun without the flags drops the extras (they were manifest-owned).
	if _, err := RunFab(FabOptions{PCB: fixBoard, OutDir: out, NoAssembly: true, Tools: tools}); err != nil {
		t.Fatal(err)
	}
	if f := strings.Join(listing(t, out), " "); strings.Contains(f, ".step") || strings.Contains(f, ".d356") {
		t.Errorf("stale extras after rerun: %s", f)
	}
}

func TestRunFabBoardOnlySkipsModelCheck(t *testing.T) {
	tools := fakeCLI(t)
	rep, err := RunFab(FabOptions{PCB: fixBoard, OutDir: t.TempDir(), NoAssembly: true, Step: true, StepBoardOnly: true, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Step.BoardOnly || !strings.HasPrefix(rep.Step.ModelCheck, "skipped") || len(rep.Step.FootprintsNoModel) != 0 {
		t.Errorf("%+v", rep.Step)
	}
}

func TestRunFabBadExtrasPublishNothing(t *testing.T) {
	tools := fakeCLI(t)
	for name, env := range map[string][2]string{
		"empty step": {"PCBPILOT_FAKE_STEP", "empty"}, "no step file": {"PCBPILOT_FAKE_STEP", "none"}, "truncated step": {"PCBPILOT_FAKE_STEP", "truncated"}, "no d356 records": {"PCBPILOT_FAKE_D356", "norec"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(env[0], env[1])
			out := t.TempDir()
			_, err := RunFab(FabOptions{PCB: fixBoard, OutDir: out, NoAssembly: true, Step: true, IPC356: true, Tools: tools})
			if err == nil {
				t.Fatal("want failure")
			}
			if l := listing(t, out); len(l) != 1 || l[0] != fabLockName { // only the (empty) lock file
				t.Errorf("nothing may be published, got %v", l)
			}
		})
	}
}

func TestRunFabWithoutExtrasUnchanged(t *testing.T) {
	tools := fakeCLI(t)
	out := t.TempDir()
	rep, err := RunFab(FabOptions{PCB: fixBoard, OutDir: out, NoAssembly: true, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Step != nil || rep.IPC356 != nil {
		t.Error("no extras requested")
	}
	b, _ := os.ReadFile(filepath.Join(out, fabReportName))
	if strings.Contains(string(b), `"step"`) || strings.Contains(string(b), "ipc356") {
		t.Errorf("report must not mention extras:\n%s", b)
	}
	var m fabManifest
	mb, _ := os.ReadFile(filepath.Join(out, fabManifestName))
	json.Unmarshal(mb, &m)
	if !reflect.DeepEqual(m.Files, []string{"fab_board-gerber.zip", fabReportName}) {
		t.Errorf("manifest files %v", m.Files)
	}
}

// Real kicad-cli: STEP and IPC-D-356 of the fixture board (PCBPILOT_KICAD_LIVE=1).
func TestLiveFabStepIPC356(t *testing.T) {
	tools := liveTools(t)
	out := t.TempDir()
	rep, err := RunFab(FabOptions{PCB: fixBoard, OutDir: out, NoAssembly: true, Step: true, IPC356: true, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Step.Bytes == 0 || rep.IPC356.Records == 0 {
		t.Fatalf("%+v %+v", rep.Step, rep.IPC356)
	}
	if err := CheckSTEPFile(filepath.Join(out, "fab_board.step")); err != nil {
		t.Error(err)
	}
}
