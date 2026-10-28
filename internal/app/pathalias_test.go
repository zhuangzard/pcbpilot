package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Names differing only in case are one file slot only where the directory
// folds case: always on Windows, never on Linux, probed on macOS (APFS can be
// case-sensitive).
func TestSameFileSlotCase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Work")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !sameFileSlot(filepath.Join(dir, "out.json"), filepath.Join(dir, "out.json")) {
		t.Fatal("one name is one slot")
	}
	if sameFileSlot(filepath.Join(dir, "a.json"), filepath.Join(dir, "b.json")) {
		t.Fatal("different names are different slots")
	}
	folds := caseFoldingDir(dir)
	switch runtime.GOOS {
	case "windows":
		if !folds {
			t.Fatal("Windows folds case")
		}
	case "linux":
		if folds {
			t.Fatal("Linux is case-sensitive")
		}
	case "darwin":
		// Ground truth for this volume: does the flipped name find the dir?
		a, err1 := os.Stat(dir)
		b, err2 := os.Stat(filepath.Join(filepath.Dir(dir), "wORK"))
		if want := err1 == nil && err2 == nil && os.SameFile(a, b); folds != want {
			t.Fatalf("macOS probe = %v, volume folds = %v", folds, want)
		}
	}
	if got := sameFileSlot(filepath.Join(dir, "Out.json"), filepath.Join(dir, "out.json")); got != folds {
		t.Fatalf("Out.json vs out.json: same slot %v, want %v (case folding)", got, folds)
	}
}

// Paths whose parents do not exist yet still alias through a linked
// ancestor: <dir>/new/report.json and <link to dir>/new/report.json are one
// file once a writer creates "new" (a symlink on Unix, a junction on
// unprivileged Windows).
func TestSameFileSlotThroughLinkedAncestor(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "J")
	dirLinkForTest(t, dir, link)
	if !sameFileSlot(filepath.Join(dir, "new", "report.json"), filepath.Join(link, "new", "report.json")) {
		t.Fatal("not-yet-created subdir through a linked ancestor accepted as distinct")
	}
	if !sameFileSlot(filepath.Join(dir, "a", "b", "r.json"), filepath.Join(link, "a", "b", "r.json")) {
		t.Fatal("two missing levels through a linked ancestor accepted as distinct")
	}
	for _, pair := range [][2]string{
		{filepath.Join(dir, "new", "a.json"), filepath.Join(link, "new", "b.json")},
		{filepath.Join(dir, "new", "r.json"), filepath.Join(link, "other", "r.json")},
		{filepath.Join(dir, "new", "r.json"), filepath.Join(link, "r.json")},
	} {
		if sameFileSlot(pair[0], pair[1]) {
			t.Errorf("distinct paths treated as one: %s vs %s", pair[0], pair[1])
		}
	}
	// The command paths reject it before writing.
	if err := schDesignatorsDistinctFiles(filepath.Join(dir, "new", "out.json"), filepath.Join(link, "new", "out.json")); err == nil {
		t.Error("sch designators accepted a future output alias under a linked ancestor")
	}
	from := filepath.Join(dir, "source.json")
	if err := os.WriteFile(from, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := schLayoutReportPaths(from, filepath.Join(dir, "new", "layout.json"), filepath.Join(link, "new", "layout.json")); err == nil {
		t.Error("sch layout-plan accepted a future --out/--report alias under a linked ancestor")
	}
}
