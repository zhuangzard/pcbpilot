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
	return sbDiff(snap, sbLoadBasis(out, st, snap), touch)
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
	nCP := len(sbGuardCheckpoints(t, out))

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
		if n := len(sbGuardCheckpoints(t, out)); n != nCP {
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
func TestSchBuildRefusesToOverwriteForeignLibrary(t *testing.T) {
	sbNeedKicad(t)
	out := filepath.Join(t.TempDir(), "p")
	sbWriteRel(t, out, "mylib.kicad_sym", testSymLib)
	if rep, err := runSchBuild(sbGuardSpec(), sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil); err != nil || !rep.OK {
		t.Fatalf("build: %v %+v", err, rep.Error)
	}
	if st := mustSbState(t, out); st.Manifest["mylib.kicad_sym"] == "" || sbContains(st.Files, "mylib.kicad_sym") {
		t.Fatalf("foreign library must be recorded, not owned: %+v", st)
	}
	spec := sbGuardSpec()
	spec.Parts[0].Symbol = "mylib:R"
	rep, err := runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Error == nil || rep.Error.Code != "EXTERNAL_EDIT" || strings.Join(rep.External.Overwrites, ",") != "mylib.kicad_sym" {
		t.Fatalf("overwriting a user library: %+v %+v", rep.Error, rep.External)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "mylib.kicad_sym")); string(b) != testSymLib {
		t.Fatal("refused build changed the user library")
	}
	rep, err = runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000, DiscardExternal: true}, nil)
	if err != nil || !rep.OK || rep.External == nil || rep.External.Checkpoint == 0 {
		t.Fatalf("override: %v %+v %+v", err, rep.Error, rep.External)
	}
	cp := filepath.Join(sbCheckpointDir(out), strconv.Itoa(rep.External.Checkpoint))
	for _, f := range []string{"mylib.kicad_sym", "p.kicad_sch", "p.kicad_pro", "sym-lib-table"} {
		if !fileExists(filepath.Join(cp, f)) {
			t.Errorf("pre-discard checkpoint lacks %s", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(cp, "mylib.kicad_sym")); string(b) != testSymLib {
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
