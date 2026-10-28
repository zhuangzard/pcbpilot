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
