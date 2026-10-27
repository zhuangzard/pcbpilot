package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func sbGuardSpec() *sbSpec {
	return &sbSpec{Parts: []sbPart{{Ref: "R1", Value: "1k", Symbol: "Device:R"}, {Ref: "R2", Value: "2k", Symbol: "Device:R"}},
		Nets: []sbNet{{Name: "A", Pins: []string{"R1:1", "R2:1"}}, {Name: "GND", Pins: []string{"R1:2", "R2:2"}}}}
}

var sbGuardOps = []sbOp{{Op: "set_value", Ref: "R2", Value: "4k7"}}

func sbGuardBuild(t *testing.T) string {
	t.Helper()
	sbNeedKicad(t)
	out := filepath.Join(t.TempDir(), "p")
	rep, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil)
	if err != nil || !rep.OK {
		t.Fatalf("build: %v %+v", err, rep)
	}
	st, _ := loadSbState(out)
	if st == nil || st.Manifest["p.kicad_sch"] == "" || st.Manifest["p.kicad_pro"] == "" || st.Manifest["sym-lib-table"] == "" {
		t.Fatalf("no manifest in state: %+v", st)
	}
	return out
}

// sbGuardAppend makes the GUI edit of the field report: one more line in a sheet.
func sbGuardAppend(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	i := bytes.LastIndexByte(b, ')')
	edited := append(append(append([]byte(nil), b[:i]...), []byte("  (wire (pts (xy 10 10) (xy 20 10)) (stroke (width 0) (type default)) (uuid \"00000000-0000-0000-0000-00000000abcd\"))\n")...), b[i:]...)
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	return edited
}

func sbGuardCheckpoints(t *testing.T, out string) []sbCheckpointMeta {
	t.Helper()
	list, _ := listSbCheckpoints(out)
	return list
}

// field report 2026-10-26: a line appended in KiCad was lost by sch-edit.
func TestSchEditRefusesExternalEdit(t *testing.T) {
	out := sbGuardBuild(t)
	sheet := filepath.Join(out, "p.kicad_sch")
	edited := sbGuardAppend(t, sheet)
	nCP := len(sbGuardCheckpoints(t, out))

	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" {
		t.Fatalf("want EXTERNAL_EDIT, got ok=%v %+v", rep.OK, rep.Error)
	}
	if rep.External == nil || rep.External.Basis != "manifest" || len(rep.External.Changed) != 1 || rep.External.Changed[0] != "p.kicad_sch" {
		t.Fatalf("external edits = %+v", rep.External)
	}
	if !strings.Contains(rep.Error.Fix, "sch-read") || !strings.Contains(rep.Error.Fix, "--discard-external-edits") {
		t.Errorf("fix hint: %s", rep.Error.Fix)
	}
	if b, _ := os.ReadFile(sheet); !bytes.Equal(b, edited) {
		t.Fatal("a refused edit changed the sheet")
	}
	if n := len(sbGuardCheckpoints(t, out)); n != nCP {
		t.Fatalf("a refused edit took a checkpoint (%d → %d)", nCP, n)
	}
	var so, se bytes.Buffer
	if err := finishSbRun(rep, nil, "", &so, &se); err == nil || !strings.Contains(se.String(), "EXTERNAL_EDIT") {
		t.Fatalf("refusal must exit non-zero and name the code: %v %q", err, se.String())
	}

	// a dry-run only warns
	dr, err := runSchEdit(sbOptions{OutDir: out, DryRun: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil || dr.Error != nil || !strings.Contains(strings.Join(dr.Warnings, "\n"), "external edits") {
		t.Fatalf("dry-run: %v %+v %v", err, dr.Error, dr.Warnings)
	}

	// a rebuild of the spec into the same --out is refused the same way
	br, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil)
	if err != nil || br.Error == nil || br.Error.Code != "EXTERNAL_EDIT" {
		t.Fatalf("in-place sch-build: %v %+v", err, br.Error)
	}

	// --discard-external-edits: checkpoint of the edited files, then overwrite
	rep, err = runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: true}, sbGuardOps)
	if err != nil || !rep.OK {
		t.Fatalf("override: %v %+v", err, rep.Error)
	}
	if rep.External == nil || rep.External.Checkpoint == 0 || !strings.Contains(strings.Join(rep.Warnings, "\n"), "p.kicad_sch") {
		t.Fatalf("override must say what it overwrites and checkpoint first: %+v %v", rep.External, rep.Warnings)
	}
	saved, _ := os.ReadFile(filepath.Join(sbCheckpointDir(out), strconv.Itoa(rep.External.Checkpoint), "p.kicad_sch"))
	if !bytes.Equal(saved, edited) {
		t.Fatal("the pre-discard checkpoint does not hold the edited sheet")
	}
	now, _ := os.ReadFile(sheet)
	if bytes.Contains(now, []byte("00000000abcd")) || !bytes.Contains(now, []byte(`"4k7"`)) {
		t.Fatal("override did not regenerate the sheet from the edited spec")
	}
	// the next edit compares with the new manifest: clean
	if rep, err = runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, []sbOp{{Op: "set_value", Ref: "R1", Value: "2k2"}}); err != nil || !rep.OK {
		t.Fatalf("edit after override: %v %+v", err, rep.Error)
	}
}

func TestSchEditIgnoresKicadSideFiles(t *testing.T) {
	out := sbGuardBuild(t)
	for _, f := range []string{"p.kicad_prl", "_autosave-p.kicad_sch", "p.kicad_sch-bak", "fp-info-cache", "p-backups/p-2026-10-26_120000.zip", "p.kicad_pcb"} {
		p := filepath.Join(out, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("side file\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil || !rep.OK || rep.External != nil {
		t.Fatalf("side files counted as edits: %v %+v %+v", err, rep.Error, rep.External)
	}
}

func TestSchEditRefusesWhileKicadHasItOpen(t *testing.T) {
	out := sbGuardBuild(t)
	before, _ := os.ReadFile(filepath.Join(out, "p.kicad_sch"))
	if err := os.WriteFile(filepath.Join(out, "~p.kicad_sch.lck"), []byte(`{"hostname":"h","username":"u"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, discard := range []bool{false, true} { // the override does not cover an open editor
		rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: discard}, sbGuardOps)
		if err != nil || rep.Error == nil || rep.Error.Code != "KICAD_OPEN" || !strings.Contains(rep.Error.Fix, "close") {
			t.Fatalf("discard=%v: want KICAD_OPEN, got %v %+v", discard, err, rep.Error)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(out, "p.kicad_sch")); !bytes.Equal(before, after) {
		t.Fatal("refused edit changed the sheet")
	}
}

// A project built before manifests: compared with its checkpoints; with no
// checkpoint either, an in-place edit needs the override.
func TestSchEditLegacyProjectWithoutManifest(t *testing.T) {
	out := sbGuardBuild(t)
	statePath := filepath.Join(sbMetaDir(out), "state.json")
	var raw map[string]any
	b, _ := os.ReadFile(statePath)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "manifest")
	stripManifest := func() {
		t.Helper()
		if err := writeJSONFile(statePath, raw); err != nil {
			t.Fatal(err)
		}
	}
	stripManifest()
	// unchanged: matches checkpoint 1
	if x := sbDetectExternalEdits(out, mustSbState(t, out)); x == nil || x.any() || x.Basis != "checkpoint 1" {
		t.Fatalf("unchanged legacy project: %+v", x)
	}
	sbGuardAppend(t, filepath.Join(out, "p.kicad_sch"))
	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil || rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" || rep.External.Basis != "checkpoint 1" {
		t.Fatalf("legacy edited: %v %+v %+v", err, rep.Error, rep.External)
	}
	// no manifest and no checkpoint: nothing to compare with → refused
	if err := os.RemoveAll(sbCheckpointDir(out)); err != nil {
		t.Fatal(err)
	}
	rep, err = runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil || rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" || rep.External.Basis != "none" {
		t.Fatalf("no record: %v %+v %+v", err, rep.Error, rep.External)
	}
	rep, err = runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: true}, sbGuardOps)
	if err != nil || !rep.OK || rep.External == nil || rep.External.Checkpoint != 1 || rep.Checkpoint != 2 {
		t.Fatalf("override on a project with no record: %v %+v %+v cp=%d", err, rep.Error, rep.External, rep.Checkpoint)
	}
	if st := mustSbState(t, out); st.Manifest == nil {
		t.Fatal("the override build did not record a manifest")
	}
}

func mustSbState(t *testing.T, out string) *sbState {
	t.Helper()
	st, _ := loadSbState(out)
	if st == nil {
		t.Fatal("no state")
	}
	return st
}

// The comparison itself, without KiCad: changed / added / removed, side files,
// a restored older checkpoint, a fresh --out.
func TestSbDetectExternalEdits(t *testing.T) {
	out := t.TempDir()
	if x := sbDetectExternalEdits(out, nil); x != nil {
		t.Fatalf("empty --out: %+v", x)
	}
	write := func(rel, s string) {
		p := filepath.Join(out, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	owned := []string{"b.kicad_pro", "b.kicad_sch", "lib.kicad_sym", "Lib.pretty/R.kicad_mod", "pg.kicad_sch", "sym-lib-table"}
	for _, f := range owned {
		write(f, "v1 "+f)
	}
	write("mine.kicad_sym", "a foreign library present at build time")
	st := &sbState{Files: owned}
	st.Manifest = sbMakeManifest(out, owned)
	if _, ok := st.Manifest["mine.kicad_sym"]; !ok {
		t.Fatal("a project file present at commit is not in the manifest")
	}
	if x := sbDetectExternalEdits(out, st); x == nil || x.any() {
		t.Fatalf("clean: %+v", x)
	}
	write("b.kicad_prl", "view")
	write("~b.kicad_pro.lck", "lock")
	write("_autosave-b.kicad_sch", "autosave")
	write("b-backups/x.zip", "zip")
	write("mine.kicad_sym", "foreign file changed: never overwritten, not an edit")
	x := sbDetectExternalEdits(out, st)
	if x.any() || len(x.Locks) != 1 {
		t.Fatalf("side files: %+v", x)
	}
	_ = os.Remove(filepath.Join(out, "~b.kicad_pro.lck"))
	write("b.kicad_sch", "v2")
	write("Lib.pretty/R.kicad_mod", "v2")
	write("new.kicad_sch", "a sheet added in KiCad")
	_ = os.Remove(filepath.Join(out, "pg.kicad_sch"))
	x = sbDetectExternalEdits(out, st)
	if strings.Join(x.Changed, ",") != "Lib.pretty/R.kicad_mod,b.kicad_sch" || strings.Join(x.Added, ",") != "new.kicad_sch" || strings.Join(x.Removed, ",") != "pg.kicad_sch" {
		t.Fatalf("diff: %+v", x)
	}

	// legacy: checkpoints only; the files equal an older one (restored)
	out2 := t.TempDir()
	for _, f := range []string{"a.kicad_sch", "a.kicad_pro"} {
		_ = os.WriteFile(filepath.Join(out2, f), []byte("v1"), 0o644)
	}
	files := []string{"a.kicad_pro", "a.kicad_sch"}
	if _, err := saveSbCheckpoint(out2, files, "build"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(out2, "a.kicad_sch"), []byte("v2"), 0o644)
	if _, err := saveSbCheckpoint(out2, files, "edit"); err != nil {
		t.Fatal(err)
	}
	legacy := &sbState{Files: files}
	if x := sbDetectExternalEdits(out2, legacy); x.any() || x.Basis != "checkpoint 2" {
		t.Fatalf("legacy latest: %+v", x)
	}
	_ = os.WriteFile(filepath.Join(out2, "a.kicad_sch"), []byte("v1"), 0o644)
	if x := sbDetectExternalEdits(out2, legacy); x.any() || x.Basis != "checkpoint 1" {
		t.Fatalf("legacy restored: %+v", x)
	}
	_ = os.WriteFile(filepath.Join(out2, "a.kicad_sch"), []byte("gui"), 0o644)
	if x := sbDetectExternalEdits(out2, legacy); !x.any() || x.Basis != "checkpoint 2" || len(x.Changed) != 1 {
		t.Fatalf("legacy edited: %+v", x)
	}

	// a KiCad project pcbpilot has no record of
	out3 := t.TempDir()
	_ = os.WriteFile(filepath.Join(out3, "x.kicad_sch"), []byte("hand drawn"), 0o644)
	if x := sbDetectExternalEdits(out3, nil); x == nil || x.Basis != "none" || len(x.Added) != 1 {
		t.Fatalf("unrecorded project: %+v", x)
	}
}
