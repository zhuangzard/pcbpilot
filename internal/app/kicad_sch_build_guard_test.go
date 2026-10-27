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

func sbTestCheckpoints(t *testing.T, out string) []sbCheckpointMeta {
	t.Helper()
	list, _ := listSbCheckpoints(out)
	return list
}

// field report 2026-10-26: a line appended in KiCad was lost by sch-edit.
func TestSchEditRefusesExternalEdit(t *testing.T) {
	out := sbGuardBuild(t)
	sheet := filepath.Join(out, "p.kicad_sch")
	edited := sbGuardAppend(t, sheet)
	nCP := len(sbTestCheckpoints(t, out))

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
	if n := len(sbTestCheckpoints(t, out)); n != nCP {
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
	if x := sbDetect(t, out, mustSbState(t, out), nil); x.any() || x.Basis != "checkpoint 1" {
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

// sbDetect is phase 1's comparison.
func sbDetect(t *testing.T, out string, st *sbState, touch []string) *sbExternalEdits {
	t.Helper()
	snap, err := sbTakeSnapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	b, err := sbLoadBasis(out, st, snap)
	if err != nil {
		t.Fatal(err)
	}
	return sbDiff(snap, b, touch)
}

func sbWriteRel(t *testing.T, out, rel, s string) {
	t.Helper()
	p := filepath.Join(out, filepath.FromSlash(rel))
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// sbFakeCommit records a manifest the way a commit does (staged bytes =
// the files on disk here).
func sbFakeCommit(t *testing.T, out string, owned []string) *sbState {
	t.Helper()
	snap, err := sbTakeSnapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	// basis = the files as they are, owned as given: no conflicts
	g := &sbGuard{out: out, baseline: snap, basis: &sbBasis{Name: "manifest", Hashes: snap.Files, Owned: owned}}
	man, f := g.beforeCommit(out, owned, nil, &sbReport{})
	if f != nil {
		t.Fatalf("commit: %+v", f)
	}
	return &sbState{Files: owned, Manifest: man}
}

// The comparison itself, without KiCad: changed / added / removed, nested
// files and locks, side files, a restored older checkpoint, no record.
func TestSbDetectExternalEdits(t *testing.T) {
	out := t.TempDir()
	if x := sbDetect(t, out, nil, nil); x.any() {
		t.Fatalf("empty --out: %+v", x)
	}
	owned := []string{"b.kicad_pro", "b.kicad_sch", "lib.kicad_sym", "Lib.pretty/R.kicad_mod", "pg.kicad_sch", "sym-lib-table"}
	for _, f := range owned {
		sbWriteRel(t, out, f, "v1 "+f)
	}
	sbWriteRel(t, out, "mine.kicad_sym", "a foreign library present at build time")
	st := sbFakeCommit(t, out, owned)
	if _, ok := st.Manifest["mine.kicad_sym"]; !ok {
		t.Fatal("a project file present at commit is not in the manifest")
	}
	if x := sbDetect(t, out, st, nil); x.any() {
		t.Fatalf("clean: %+v", x)
	}
	sbWriteRel(t, out, "b.kicad_prl", "view")
	sbWriteRel(t, out, "_autosave-b.kicad_sch", "autosave")
	sbWriteRel(t, out, "b.kicad_sch-bak", "bak")
	sbWriteRel(t, out, "b-backups/x.kicad_sch", "zip")
	sbWriteRel(t, out, ".history/b.kicad_sch", "local history")
	sbWriteRel(t, out, "mine.kicad_sym", "foreign file changed: never overwritten, not an edit")
	sbWriteRel(t, out, "sub/~child.kicad_sch.lck", "lock")
	x := sbDetect(t, out, st, nil)
	if x.any() || strings.Join(x.Locks, ",") != "sub/~child.kicad_sch.lck" {
		t.Fatalf("side files / nested lock: %+v", x)
	}
	_ = os.Remove(filepath.Join(out, "sub", "~child.kicad_sch.lck"))
	sbWriteRel(t, out, "b.kicad_sch", "v2")
	sbWriteRel(t, out, "Lib.pretty/R.kicad_mod", "v2")
	sbWriteRel(t, out, "sub/child.kicad_sch", "a hierarchical sheet added in KiCad")
	_ = os.Remove(filepath.Join(out, "pg.kicad_sch"))
	x = sbDetect(t, out, st, nil)
	if strings.Join(x.Changed, ",") != "Lib.pretty/R.kicad_mod,b.kicad_sch" || strings.Join(x.Added, ",") != "sub/child.kicad_sch" || strings.Join(x.Removed, ",") != "pg.kicad_sch" {
		t.Fatalf("diff: %+v", x)
	}

	// a lock beside a sheet referenced from outside --out
	ext := t.TempDir()
	out5 := filepath.Join(ext, "proj")
	sbWriteRel(t, out5, "r.kicad_sch", `(kicad_sch (sheet (property "Sheetfile" "../shared/s.kicad_sch")))`)
	sbWriteRel(t, ext, "shared/s.kicad_sch", "(kicad_sch)")
	sbWriteRel(t, ext, "shared/~s.kicad_sch.lck", "lock")
	if x := sbDetect(t, out5, nil, nil); len(x.Locks) != 1 {
		t.Fatalf("lock beside an external sheet: %+v", x)
	}

	// a foreign library (recorded, unchanged) that the next build generates
	out4 := t.TempDir()
	sbWriteRel(t, out4, "p.kicad_sch", "v1")
	sbWriteRel(t, out4, "mylib.kicad_sym", "user library")
	st4 := sbFakeCommit(t, out4, []string{"p.kicad_sch"})
	if x := sbDetect(t, out4, st4, nil); x.any() {
		t.Fatalf("phase 1 must not flag an untouched foreign file: %+v", x)
	}
	if x := sbDetect(t, out4, st4, []string{"p.kicad_sch", "mylib.kicad_sym"}); strings.Join(x.Overwrites, ",") != "mylib.kicad_sym" {
		t.Fatalf("foreign library becoming an output: %+v", x)
	}

	// legacy: checkpoints only; the files equal an older one (restored)
	out2 := t.TempDir()
	files := []string{"a.kicad_pro", "a.kicad_sch"}
	sbWriteRel(t, out2, "a.kicad_pro", "{}")
	sbWriteRel(t, out2, "a.kicad_sch", "v1")
	if _, err := saveSbCheckpoint(out2, files, "build"); err != nil {
		t.Fatal(err)
	}
	sbWriteRel(t, out2, "a.kicad_sch", "v2")
	if _, err := saveSbCheckpoint(out2, files, "edit"); err != nil {
		t.Fatal(err)
	}
	legacy := &sbState{Files: files}
	if x := sbDetect(t, out2, legacy, nil); x.any() || x.Basis != "checkpoint 2" {
		t.Fatalf("legacy latest: %+v", x)
	}
	sbWriteRel(t, out2, "a.kicad_sch", "v1")
	if x := sbDetect(t, out2, legacy, nil); x.any() || x.Basis != "checkpoint 1" {
		t.Fatalf("legacy restored: %+v", x)
	}
	sbWriteRel(t, out2, "a.kicad_sch", "gui")
	if x := sbDetect(t, out2, legacy, nil); !x.any() || x.Basis != "checkpoint 2" || len(x.Changed) != 1 {
		t.Fatalf("legacy edited: %+v", x)
	}

	// a KiCad project pcbpilot has no record of: only overwrites conflict
	out3 := t.TempDir()
	sbWriteRel(t, out3, "x.kicad_sch", "hand drawn")
	if x := sbDetect(t, out3, nil, nil); x.any() {
		t.Fatalf("unrecorded, untouched: %+v", x)
	}
	if x := sbDetect(t, out3, nil, []string{"x.kicad_sch"}); x.Basis != "none" || strings.Join(x.Overwrites, ",") != "x.kicad_sch" {
		t.Fatalf("unrecorded, overwritten: %+v", x)
	}
}

// .kicad_pro: key order, whitespace and KiCad's UI-state fields do not count;
// a real setting does.
func TestSbProjectHashIgnoresVolatileFields(t *testing.T) {
	base := `{"meta":{"filename":"p.kicad_pro","version":3},"schematic":{"legacy_lib_dir":"","legacy_lib_list":[]}}`
	h := sbContentHash("p.kicad_pro", []byte(base))
	same := "{\n  \"schematic\": {\"legacy_lib_list\": [], \"legacy_lib_dir\": \"\"},\n  \"meta\": {\"version\": 3, \"filename\": \"p.kicad_pro\"},\n" +
		`  "pcbnew": {"last_paths": {"plot": "C:/x"}}, "board": {"viewports": [], "3dviewports": [{"name": "v"}]}}`
	if sbContentHash("p.kicad_pro", []byte(same)) != h {
		t.Fatal("formatting or UI state changed the .kicad_pro hash")
	}
	set := `{"meta":{"filename":"p.kicad_pro","version":3},"schematic":{"legacy_lib_dir":"","legacy_lib_list":[]},"text_variables":{"REV":"B"}}`
	if sbContentHash("p.kicad_pro", []byte(set)) == h {
		t.Fatal("a text variable must change the .kicad_pro hash")
	}
	if sbContentHash("p.kicad_sch", []byte(" x")) == sbContentHash("p.kicad_sch", []byte("x")) {
		t.Fatal("sheets are hashed byte for byte")
	}
}

// Fail closed: an unreadable directory or file refuses, before and at commit,
// and no manifest is published.
func TestSbGuardFailsClosed(t *testing.T) {
	out := t.TempDir()
	sbWriteRel(t, out, "p.kicad_sch", "v1")
	st := sbFakeCommit(t, out, []string{"p.kicad_sch"})
	defer func() { sbReadFile, sbReadDir = os.ReadFile, os.ReadDir }()
	sbReadDir = func(string) ([]os.DirEntry, error) { return nil, os.ErrPermission }
	if _, f := sbGuardStart(out, st, sbOptions{}, &sbReport{}); f == nil || f.Code != "PROJECT_UNREADABLE" {
		t.Fatalf("dir read error: %+v", f)
	}
	sbReadDir = os.ReadDir
	sbReadFile = func(string) ([]byte, error) { return nil, os.ErrPermission }
	if _, f := sbGuardStart(out, st, sbOptions{}, &sbReport{}); f == nil || f.Code != "PROJECT_UNREADABLE" {
		t.Fatalf("file read error: %+v", f)
	}
	sbReadFile = os.ReadFile
	g, f := sbGuardStart(out, st, sbOptions{}, &sbReport{})
	if f != nil {
		t.Fatal(f)
	}
	stage := t.TempDir() // the staged file cannot be read: no manifest
	if man, f := g.beforeCommit(stage, []string{"p.kicad_sch"}, nil, &sbReport{}); f == nil || f.Code != "PROJECT_UNREADABLE" || man != nil {
		t.Fatalf("staged read error: %v %+v", man, f)
	}
	// and a directory error at commit time
	sbReadDir = func(string) ([]os.DirEntry, error) { return nil, os.ErrPermission }
	if man, f := g.beforeCommit(out, []string{"p.kicad_sch"}, nil, &sbReport{}); f == nil || f.Code != "PROJECT_UNREADABLE" || man != nil {
		t.Fatalf("commit-time dir error: %v %+v", man, f)
	}
}

// KiCad ignores pcbpilot's lock: a save or a lock that appears while the
// build runs aborts the commit; a save right after publication is caught
// by the next run because the manifest is hashed from the staged bytes.
func TestSchEditRaceWithKicad(t *testing.T) {
	out := sbGuardBuild(t)
	sheet := filepath.Join(out, "p.kicad_sch")
	defer func() { sbTestBeforeCommit, sbTestAfterPublish = nil, nil }()
	built, _ := os.ReadFile(sheet)
	stBefore, _ := os.ReadFile(filepath.Join(sbMetaDir(out), "state.json"))
	nCP := len(sbTestCheckpoints(t, out))

	for _, discard := range []bool{false, true} {
		var edited []byte
		sbTestBeforeCommit = func(string) { edited = sbGuardAppend(t, sheet) }
		rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: discard}, sbGuardOps)
		sbTestBeforeCommit = nil
		if err != nil || rep.Error == nil || rep.Error.Code != "CONCURRENT_CHANGE" {
			t.Fatalf("discard=%v: save during build: %v %+v", discard, err, rep.Error)
		}
		if b, _ := os.ReadFile(sheet); !bytes.Equal(b, edited) {
			t.Fatal("the commit replaced a sheet saved during the build")
		}
		if b, _ := os.ReadFile(filepath.Join(sbMetaDir(out), "state.json")); !bytes.Equal(b, stBefore) {
			t.Fatal("an aborted commit changed state.json")
		}
		if n := len(sbTestCheckpoints(t, out)); n != nCP {
			t.Fatal("an aborted commit took a checkpoint")
		}
		// the racing save is an edit for the next run
		rep, _ = runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
		if rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" {
			t.Fatalf("after the racing save: %+v", rep.Error)
		}
		_ = os.WriteFile(sheet, built, 0o644)
	}

	// a lock that appears mid-build
	sbTestBeforeCommit = func(string) { _ = os.WriteFile(filepath.Join(out, "~p.kicad_sch.lck"), []byte("{}"), 0o644) }
	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: true}, sbGuardOps)
	sbTestBeforeCommit = nil
	if err != nil || rep.Error == nil || rep.Error.Code != "KICAD_OPEN" {
		t.Fatalf("lock mid-build: %v %+v", err, rep.Error)
	}
	if b, _ := os.ReadFile(sheet); !bytes.Equal(b, built) {
		t.Fatal("a commit went through while KiCad took the lock")
	}
	_ = os.Remove(filepath.Join(out, "~p.kicad_sch.lck"))

	// a save between publication and the manifest
	sbTestAfterPublish = func(string) { sbGuardAppend(t, sheet) }
	rep, err = runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	sbTestAfterPublish = nil
	if err != nil || !rep.OK {
		t.Fatalf("edit: %v %+v", err, rep.Error)
	}
	rep, _ = runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, []sbOp{{Op: "set_value", Ref: "R1", Value: "2k2"}})
	if rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" || strings.Join(rep.External.Changed, ",") != "p.kicad_sch" {
		t.Fatalf("a save after publication was blessed by the manifest: %+v %+v", rep.Error, rep.External)
	}
}

// A user library in --out (recorded in the manifest, never written by
// pcbpilot) that the next build would generate: refused at commit; the
// override checkpoints it first.
// On Windows/macOS the user's MYLIB.kicad_sym is the generated mylib.kicad_sym.
func TestSchBuildRefusesToOverwriteForeignLibrary(t *testing.T) {
	sbNeedKicad(t)
	names := []string{"mylib.kicad_sym"}
	if sbFoldCase {
		names = append(names, "MYLIB.kicad_sym")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) { sbForeignLibraryCase(t, name) })
	}
}

func sbForeignLibraryCase(t *testing.T, name string) {
	out := filepath.Join(t.TempDir(), "p")
	sbWriteRel(t, out, name, testSymLib)
	if rep, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil); err != nil || !rep.OK {
		t.Fatalf("build: %v %+v", err, rep.Error)
	}
	if st := mustSbState(t, out); st.Manifest[name] == "" || sbContains(st.Files, name) {
		t.Fatalf("foreign library must be recorded, not owned: %+v", st)
	}
	spec := sbGuardSpec()
	spec.Parts[0].Symbol = "mylib:R"
	rep, err := runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" || strings.Join(rep.External.Overwrites, ",") != name {
		t.Fatalf("overwriting a user library: %+v %+v", rep.Error, rep.External)
	}
	if b, _ := os.ReadFile(filepath.Join(out, name)); string(b) != testSymLib {
		t.Fatal("refused build changed the user library")
	}
	rep, err = runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: true}, nil)
	if err != nil || !rep.OK || rep.External == nil || rep.External.Checkpoint == 0 {
		t.Fatalf("override: %v %+v %+v", err, rep.Error, rep.External)
	}
	cp := filepath.Join(sbCheckpointDir(out), strconv.Itoa(rep.External.Checkpoint))
	for _, f := range []string{name, "p.kicad_sch", "p.kicad_pro", "sym-lib-table"} {
		if !fileExists(filepath.Join(cp, f)) {
			t.Errorf("pre-discard checkpoint lacks %s", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(cp, name)); string(b) != testSymLib {
		t.Error("checkpoint does not hold the user library")
	}
}

// Legacy project with no state.json, only a checkpoint: the pre-discard
// checkpoint holds every existing project file, not only the added ones.
func TestSchBuildLegacyNoStateDiscardCheckpointsAll(t *testing.T) {
	out := sbGuardBuild(t)
	if err := os.Remove(filepath.Join(sbMetaDir(out), "state.json")); err != nil {
		t.Fatal(err)
	}
	sbGuardAppend(t, filepath.Join(out, "p.kicad_sch"))
	sbWriteRel(t, out, "sub/child.kicad_sch", "(kicad_sch)")
	rep, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil)
	if err != nil || rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" || rep.External.Basis != "checkpoint 1" {
		t.Fatalf("legacy without state: %v %+v %+v", err, rep.Error, rep.External)
	}
	if !sbContains(rep.External.Added, "sub/child.kicad_sch") || !sbContains(rep.External.Changed, "p.kicad_sch") {
		t.Fatalf("nested added sheet / changed sheet: %+v", rep.External)
	}
	snap, _ := sbTakeSnapshot(out)
	rep, err = runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: true}, nil)
	if err != nil || !rep.OK || rep.External == nil || rep.External.Checkpoint == 0 {
		t.Fatalf("override: %v %+v %+v", err, rep.Error, rep.External)
	}
	cp := filepath.Join(sbCheckpointDir(out), strconv.Itoa(rep.External.Checkpoint))
	for _, f := range snap.paths() {
		if !fileExists(filepath.Join(cp, filepath.FromSlash(f))) {
			t.Errorf("pre-discard checkpoint lacks %s", f)
		}
	}
}

// Case folding without depending on the host file system.
func TestSbDiffFoldsCase(t *testing.T) {
	defer func(v bool) { sbFoldCase = v }(sbFoldCase)
	sbFoldCase = true
	snap := &sbSnapshot{Files: map[string]string{"P.kicad_sch": "h1", "MYLIB.kicad_sym": "u"}}
	b := &sbBasis{Name: "manifest", Hashes: map[string]string{"p.kicad_sch": "h1", "MYLIB.kicad_sym": "u"}, Owned: []string{"p.kicad_sch"}}
	if x := sbDiff(snap, b, nil); x.any() {
		t.Fatalf("same files, other case: %+v", x)
	}
	if x := sbDiff(snap, b, []string{"p.kicad_sch", "mylib.kicad_sym"}); strings.Join(x.Overwrites, ",") != "MYLIB.kicad_sym" {
		t.Fatalf("generated mylib over the user's MYLIB: %+v", x)
	}
	if m := sbMoved(snap, &sbSnapshot{Files: map[string]string{"p.kicad_sch": "h1", "mylib.kicad_sym": "u"}}); len(m) != 0 {
		t.Fatalf("respelled snapshot: %v", m)
	}
	sbFoldCase = false
	if x := sbDiff(snap, b, []string{"p.kicad_sch", "mylib.kicad_sym"}); len(x.Overwrites) != 0 || strings.Join(x.Removed, ",") != "p.kicad_sch" || strings.Join(x.Added, ",") != "P.kicad_sch" {
		t.Fatalf("case-sensitive file system: %+v", x)
	}
}

// A checkpoint the comparison cannot fully read refuses: a missing copy of
// the edited sheet used to make that sheet look unchanged.
func TestSchEditRefusesPartialCheckpoint(t *testing.T) {
	out := sbGuardBuild(t)
	statePath := filepath.Join(sbMetaDir(out), "state.json")
	var raw map[string]any
	b, _ := os.ReadFile(statePath)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	delete(raw, "manifest")
	if err := writeJSONFile(statePath, raw); err != nil {
		t.Fatal(err)
	}
	edited := sbGuardAppend(t, filepath.Join(out, "p.kicad_sch"))
	cpSheet := filepath.Join(sbCheckpointDir(out), "1", "p.kicad_sch")
	if err := os.Remove(cpSheet); err != nil {
		t.Fatal(err)
	}
	for _, discard := range []bool{false, true} {
		rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: discard}, sbGuardOps)
		if err != nil || rep.Error == nil || rep.Error.Code != "PROJECT_UNREADABLE" {
			t.Fatalf("discard=%v: missing checkpoint copy: %v %+v", discard, err, rep.Error)
		}
		if got, _ := os.ReadFile(filepath.Join(out, "p.kicad_sch")); !bytes.Equal(got, edited) {
			t.Fatal("refused edit changed the sheet")
		}
	}
	// unreadable (not missing) copy, and an unreadable checkpoint.json
	_ = os.WriteFile(cpSheet, []byte("x"), 0o644)
	defer func() { sbReadFile = os.ReadFile }()
	for _, bad := range []string{cpSheet, filepath.Join(sbCheckpointDir(out), "1", "checkpoint.json")} {
		sbReadFile = func(p string) ([]byte, error) {
			if filepath.Clean(p) == filepath.Clean(bad) {
				return nil, os.ErrPermission
			}
			return os.ReadFile(p)
		}
		if _, f := sbGuardStart(out, mustSbState(t, out), sbOptions{}, &sbReport{}); f == nil || f.Code != "PROJECT_UNREADABLE" {
			t.Fatalf("unreadable %s: %+v", bad, f)
		}
	}
	sbReadFile = os.ReadFile
	_ = os.Remove(filepath.Join(sbCheckpointDir(out), "1", "checkpoint.json"))
	if _, f := sbGuardStart(out, mustSbState(t, out), sbOptions{}, &sbReport{}); f == nil || f.Code != "PROJECT_UNREADABLE" {
		t.Fatalf("checkpoint without checkpoint.json: %+v", f)
	}
}

// The live re-check is the last step: a save while the discard checkpoint
// is taken or the staged files are hashed still aborts the commit.
func TestSchEditSaveDuringCommitPreparation(t *testing.T) {
	out := sbGuardBuild(t)
	sheet := filepath.Join(out, "p.kicad_sch")
	defer func() { sbTestDuringPrepare = nil }()
	// plain edit (nothing to discard)
	var edited []byte
	sbTestDuringPrepare = func(string) { edited = sbGuardAppend(t, sheet) }
	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil || rep.Error == nil || rep.Error.Code != "CONCURRENT_CHANGE" {
		t.Fatalf("save during staged hashing: %v %+v", err, rep.Error)
	}
	if got, _ := os.ReadFile(sheet); !bytes.Equal(got, edited) {
		t.Fatal("commit replaced a sheet saved during preparation")
	}
	// discard: the checkpoint is taken, then a second save aborts
	nCP := len(sbTestCheckpoints(t, out))
	sbTestDuringPrepare = func(string) { edited = sbGuardAppend(t, sheet) }
	rep, err = runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: true}, sbGuardOps)
	if err != nil || rep.Error == nil || rep.Error.Code != "CONCURRENT_CHANGE" || rep.External == nil || rep.External.Checkpoint != nCP+1 {
		t.Fatalf("save during the discard checkpoint: %v %+v %+v", err, rep.Error, rep.External)
	}
	if got, _ := os.ReadFile(sheet); !bytes.Equal(got, edited) {
		t.Fatal("commit replaced a sheet saved during the discard checkpoint")
	}
}

// Windows/macOS: a user's MYLIB.KICAD_SYM (upper-case extension) is the
// generated mylib.kicad_sym; it must be in the snapshot, refused, and come
// back from the override's checkpoint.
func TestSchBuildUpperCaseExtensionOverwrite(t *testing.T) {
	if !sbFoldCase {
		t.Skip("case-sensitive file system")
	}
	sbNeedKicad(t)
	out := filepath.Join(t.TempDir(), "p")
	const name = "MYLIB.KICAD_SYM"
	sbWriteRel(t, out, name, testSymLib)
	if rep, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil); err != nil || !rep.OK {
		t.Fatalf("build: %v %+v", err, rep.Error)
	}
	if st := mustSbState(t, out); st.Manifest[name] == "" {
		t.Fatalf("upper-case library not in the manifest: %v", st.Manifest)
	}
	spec := sbGuardSpec()
	spec.Parts[0].Symbol = "mylib:R"
	rep, err := runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil)
	if err != nil || rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" || strings.Join(rep.External.Overwrites, ",") != name {
		t.Fatalf("overwrite of %s: %v %+v %+v", name, err, rep.Error, rep.External)
	}
	if b, _ := os.ReadFile(filepath.Join(out, name)); string(b) != testSymLib {
		t.Fatal("refused build changed the user library")
	}
	rep, err = runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: true}, nil)
	if err != nil || !rep.OK || rep.External == nil || rep.External.Checkpoint == 0 {
		t.Fatalf("override: %v %+v %+v", err, rep.Error, rep.External)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "mylib.kicad_sym")); string(b) == testSymLib {
		t.Fatal("override did not regenerate the library")
	}
	if _, err := restoreSbCheckpoint(out, rep.External.Checkpoint); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, name)); string(b) != testSymLib {
		t.Fatal("restoring the pre-discard checkpoint did not bring the user library back")
	}
}

// A write target the project-file filter does not classify is still
// checked on its own and checkpointed by the override.
func TestSbGuardChecksUnclassifiedTargets(t *testing.T) {
	out := t.TempDir()
	sbWriteRel(t, out, "p.kicad_sch", "v1")
	sbWriteRel(t, out, "notes.dat", "user data")
	st := sbFakeCommit(t, out, []string{"p.kicad_sch"})
	stage := t.TempDir()
	sbWriteRel(t, stage, "p.kicad_sch", "v2")
	sbWriteRel(t, stage, "notes.dat", "generated")
	g, f := sbGuardStart(out, st, sbOptions{}, &sbReport{})
	if f != nil {
		t.Fatal(f)
	}
	if _, f := g.beforeCommit(stage, []string{"notes.dat", "p.kicad_sch"}, nil, &sbReport{}); f == nil || f.Code != "EXTERNAL_EDIT" || !strings.Contains(f.Message, "notes.dat") {
		t.Fatalf("unclassified target: %+v", f)
	}
	g, _ = sbGuardStart(out, st, sbOptions{DiscardExternal: true}, &sbReport{})
	rep := &sbReport{}
	if _, f := g.beforeCommit(stage, []string{"notes.dat", "p.kicad_sch"}, nil, rep); f != nil || rep.External == nil || rep.External.Checkpoint == 0 {
		t.Fatalf("override: %+v %+v", f, rep.External)
	}
	if b, _ := os.ReadFile(filepath.Join(sbCheckpointDir(out), strconv.Itoa(rep.External.Checkpoint), "notes.dat")); string(b) != "user data" {
		t.Fatal("checkpoint lacks the unclassified target")
	}
}

// sbCheckRefreshed: a report replaced anyway was checkpointed and named in a warning.
func sbCheckRefreshed(t *testing.T, rep *sbReport, out, rel, was string) {
	t.Helper()
	if rep.Error != nil || !rep.OK || rep.External == nil || !sbContains(rep.External.Refreshed, rel) || rep.External.Checkpoint == 0 {
		t.Fatalf("%s must be checkpointed and replaced: %+v %+v", rel, rep.Error, rep.External)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), "WARNING: report(s) "+rel) {
		t.Fatalf("no warning naming %s: %v", rel, rep.Warnings)
	}
	cp := filepath.Join(sbCheckpointDir(out), strconv.Itoa(rep.External.Checkpoint), filepath.FromSlash(rel))
	if b, _ := os.ReadFile(cp); string(b) != was {
		t.Fatalf("checkpoint %d lacks the previous %s", rep.External.Checkpoint, rel)
	}
	if b, _ := os.ReadFile(filepath.Join(out, filepath.FromSlash(rel))); string(b) == was {
		t.Fatalf("%s was not regenerated", rel)
	}
}

// An edited erc.json (a report, not design data) is checkpointed, named in a
// warning and regenerated — not refused.
func TestSchEditRefreshesEditedReport(t *testing.T) {
	out := sbGuardBuild(t)
	if st := mustSbState(t, out); !sbContains(st.Reports, "erc.json") || st.Manifest["erc.json"] == "" {
		t.Fatalf("erc.json not recorded: %+v", st.Reports)
	}
	const note = `{"note":"my annotated ERC"}`
	if err := os.WriteFile(filepath.Join(out, "erc.json"), []byte(note), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil {
		t.Fatal(err)
	}
	sbCheckRefreshed(t, rep, out, "erc.json", note)
	if ents, _ := filepath.Glob(filepath.Join(sbMetaDir(out), "publish-backup-*")); len(ents) != 0 {
		t.Fatalf("backup left after a successful commit: %v", ents)
	}
	// a design file edited together with a report is still refused
	sbGuardAppend(t, filepath.Join(out, "p.kicad_sch"))
	_ = os.WriteFile(filepath.Join(out, "erc.json"), []byte(note), 0o644)
	rep, _ = runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" || strings.Join(rep.External.Changed, ",") != "p.kicad_sch" {
		t.Fatalf("edited sheet + report: %+v %+v", rep.Error, rep.External)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "erc.json")); string(b) != note {
		t.Fatal("a refused run replaced the report")
	}
}

func TestSbKicadLockNames(t *testing.T) {
	out := t.TempDir()
	sbWriteRel(t, out, "notes.lck", "not KiCad")
	sbWriteRel(t, out, "sub/build.lck", "not KiCad")
	if x := sbDetect(t, out, nil, nil); len(x.Locks) != 0 {
		t.Fatalf("a stray *.lck counted as a KiCad lock: %v", x.Locks)
	}
	sbWriteRel(t, out, "~p.kicad_sch.lck", "KiCad")
	if x := sbDetect(t, out, nil, nil); strings.Join(x.Locks, ",") != "~p.kicad_sch.lck" {
		t.Fatalf("KiCad lock: %v", x.Locks)
	}
}

// A save to a sheet the final snapshot already read: publication moves each
// target aside first, sees the hash differ, puts everything back and aborts.
func TestSchEditSaveDuringFinalSnapshot(t *testing.T) {
	out := sbGuardBuild(t)
	sheet := filepath.Join(out, "p.kicad_sch")
	before := map[string][]byte{}
	for _, f := range []string{"p.kicad_pro", "sym-lib-table", "erc.json"} {
		before[f], _ = os.ReadFile(filepath.Join(out, f))
	}
	defer func() { sbTestDuringPrepare, sbTestScanFile = nil, nil }()
	var edited []byte
	sbTestDuringPrepare = func(string) {
		sbTestScanFile = func(rel string) {
			if rel == "p.kicad_sch" {
				sbTestScanFile = nil
				edited = sbGuardAppend(t, sheet)
			}
		}
	}
	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	sbTestDuringPrepare, sbTestScanFile = nil, nil
	if err != nil || rep.Error == nil || rep.Error.Code != "CONCURRENT_CHANGE" || !strings.Contains(rep.Error.Message, "p.kicad_sch") {
		t.Fatalf("save during the final snapshot: %v %+v", err, rep.Error)
	}
	if b, _ := os.ReadFile(sheet); !bytes.Equal(b, edited) {
		t.Fatal("the saved sheet was overwritten")
	}
	for f, want := range before {
		if b, _ := os.ReadFile(filepath.Join(out, f)); !bytes.Equal(b, want) {
			t.Errorf("%s was not put back", f)
		}
	}
	if ents, _ := filepath.Glob(filepath.Join(sbMetaDir(out), "publish-backup-*")); len(ents) != 1 {
		t.Errorf("the backup of an aborted publication must be kept: %v", ents)
	}
}

func sbBackupFiles(t *testing.T, out, sub string) map[string]string {
	t.Helper()
	got := map[string]string{}
	dirs, _ := filepath.Glob(filepath.Join(sbMetaDir(out), "publish-backup-*", sub))
	for _, d := range dirs {
		_ = filepath.Walk(d, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				b, _ := os.ReadFile(p)
				got[filepath.Base(p)] = string(b)
			}
			return nil
		})
	}
	return got
}

// KiCad recreates a target right after publication moved the original aside:
// the no-replace install refuses, the racing save is kept (never deleted)
// and the original is put back.
func TestSchEditRaceAfterMoveAside(t *testing.T) {
	out := sbGuardBuild(t)
	sheet := filepath.Join(out, "p.kicad_sch")
	orig, _ := os.ReadFile(sheet)
	defer func() { sbTestAfterMoveAside = nil }()
	const racing = "(kicad_sch saved by KiCad meanwhile)"
	sbTestAfterMoveAside = func(rel string) {
		if rel == "p.kicad_sch" {
			sbTestAfterMoveAside = nil
			_ = os.WriteFile(sheet, []byte(racing), 0o644)
		}
	}
	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil || rep.Error == nil || rep.Error.Code != "CONCURRENT_CHANGE" || !strings.Contains(rep.Error.Message, "racing-save") {
		t.Fatalf("recreated target: %v %+v", err, rep.Error)
	}
	if b, _ := os.ReadFile(sheet); !bytes.Equal(b, orig) {
		t.Fatal("the original sheet was not put back")
	}
	found := false
	for _, v := range sbBackupFiles(t, out, "racing-save") {
		found = found || v == racing
	}
	if !found {
		t.Fatal("the racing save was not kept in the backup")
	}
}

// A save over a file pcbpilot already installed, then a later target fails:
// rollback keeps that save (it is not pcbpilot's bytes) instead of removing it.
func TestSchEditRaceAfterEarlierInstall(t *testing.T) {
	out := sbGuardBuild(t)
	pro, sheet := filepath.Join(out, "p.kicad_pro"), filepath.Join(out, "p.kicad_sch")
	origPro, _ := os.ReadFile(pro)
	defer func() { sbTestAfterInstall = nil }()
	const racing = `{"saved":"by KiCad over the new file"}`
	var edited []byte
	sbTestAfterInstall = func(rel string) {
		if rel == "p.kicad_pro" {
			sbTestAfterInstall = nil
			_ = os.WriteFile(pro, []byte(racing), 0o644)
			edited = sbGuardAppend(t, sheet) // the next target no longer matches
		}
	}
	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil || rep.Error == nil || rep.Error.Code != "CONCURRENT_CHANGE" {
		t.Fatalf("race after an earlier install: %v %+v", err, rep.Error)
	}
	if b, _ := os.ReadFile(pro); !bytes.Equal(b, origPro) {
		t.Fatal("the original .kicad_pro was not put back")
	}
	if b, _ := os.ReadFile(sheet); !bytes.Equal(b, edited) {
		t.Fatal("the edited sheet was not put back")
	}
	found := false
	for _, v := range sbBackupFiles(t, out, "racing-save") {
		found = found || v == racing
	}
	if !found {
		t.Fatal("the save over pcbpilot's file was deleted instead of kept")
	}
}

// A report kept by a build that does not write it keeps its hash: build
// --render, rebuild without, annotate the SVG, build --render → the SVG is
// checkpointed, named in a warning and regenerated.
func TestSchBuildRetainedReportKeepsHash(t *testing.T) {
	sbNeedKicad(t)
	out := filepath.Join(t.TempDir(), "p")
	o := sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}
	ro := o
	ro.Render = true
	for i, opt := range []sbOptions{ro, o} {
		if rep, err := runSchBuild(sbGuardSpec(), opt, nil); err != nil || !rep.OK {
			t.Fatalf("build %d: %v %+v", i, err, rep.Error)
		}
	}
	if st := mustSbState(t, out); st.Manifest["render/p.svg"] == "" {
		t.Fatalf("retained SVG lost its hash: %v", st.Manifest)
	}
	const notes = "<svg><!-- my notes --></svg>"
	if err := os.WriteFile(filepath.Join(out, "render", "p.svg"), []byte(notes), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := runSchBuild(sbGuardSpec(), ro, nil)
	if err != nil {
		t.Fatal(err)
	}
	sbCheckRefreshed(t, rep, out, "render/p.svg", notes)
}

// A report of a build that recorded no hashes for reports (pre-v0.10.2):
// checkpointed, named in a warning and regenerated, not refused.
func TestSchBuildLegacyReportRefreshed(t *testing.T) {
	out := sbGuardBuild(t)
	statePath := filepath.Join(sbMetaDir(out), "state.json")
	var raw map[string]any
	b, _ := os.ReadFile(statePath)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	man := raw["manifest"].(map[string]any)
	delete(man, "erc.json")
	delete(raw, "reports")
	if err := writeJSONFile(statePath, raw); err != nil {
		t.Fatal(err)
	}
	const note = `{"annotated":true}`
	_ = os.WriteFile(filepath.Join(out, "erc.json"), []byte(note), 0o644)
	rep, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, sbGuardOps)
	if err != nil {
		t.Fatal(err)
	}
	sbCheckRefreshed(t, rep, out, "erc.json", note)
}

// Windows/macOS: renaming the project P → p rewrites the same files; it is
// not a delete plus a missing file.
func TestSchBuildCaseOnlyRename(t *testing.T) {
	if !sbFoldCase {
		t.Skip("case-sensitive file system")
	}
	sbNeedKicad(t)
	out := filepath.Join(t.TempDir(), "proj")
	if rep, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, Name: "P", NoIntent: true, PlannerBudget: 20000}, nil); err != nil || !rep.OK {
		t.Fatalf("build P: %v %+v", err, rep.Error)
	}
	rep, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, Name: "p", NoIntent: true, PlannerBudget: 20000}, nil)
	if err != nil || !rep.OK {
		t.Fatalf("rebuild as p: %v %+v %+v", err, rep.Error, rep.External)
	}
	ents, _ := os.ReadDir(out)
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if !sbContains(names, "p.kicad_sch") || !sbContains(names, "p.kicad_pro") {
		t.Fatalf("files after the rename: %v", names)
	}
	if rep, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, Name: "p", NoIntent: true, PlannerBudget: 20000}, nil); err != nil || !rep.OK {
		t.Fatalf("third build: %v %+v", err, rep.Error)
	}
}
