package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// sameFileSlot reports whether paths a and b name one file, including a file
// that does not exist yet: both exist and are the same file, or both have the
// same base name in one directory reached through any alias. The directory
// check uses os.Stat, which follows symlinks AND Windows junctions —
// filepath.EvalSymlinks does not resolve a junction (Go >= 1.23 reports mount
// points as irregular files, not symlinks), so a path compare alone let
// <junction>/out.json and <dir>/out.json pass as distinct.
func sameFileSlot(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(fa, fb)
	}
	ba, bb := filepath.Base(a), filepath.Base(b)
	if ba != bb && !((runtime.GOOS == "windows" || runtime.GOOS == "darwin") && strings.EqualFold(ba, bb)) {
		return false
	}
	da, errA := os.Stat(filepath.Dir(a))
	db, errB := os.Stat(filepath.Dir(b))
	return errA == nil && errB == nil && os.SameFile(da, db)
}
