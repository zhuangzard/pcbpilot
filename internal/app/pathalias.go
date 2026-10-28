package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// sameFileSlot reports whether paths a and b name one file, including a file
// that does not exist yet: both exist and are the same file, or both have the
// same base name in one directory reached through any alias. The directory
// check uses os.Stat, which follows symlinks AND Windows junctions —
// filepath.EvalSymlinks does not resolve a junction (Go >= 1.23 reports mount
// points as irregular files, not symlinks), so a path compare alone let
// <junction>/out.json and <dir>/out.json pass as distinct. Base names that
// differ only in case are one slot only where the directory's file system
// folds case (caseFoldingDir).
func sameFileSlot(a, b string) bool {
	fa, errA := os.Stat(a)
	fb, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(fa, fb)
	}
	ba, bb := filepath.Base(a), filepath.Base(b)
	if ba != bb && !(strings.EqualFold(ba, bb) && caseFoldingDir(filepath.Dir(a))) {
		return false
	}
	da, errA := os.Stat(filepath.Dir(a))
	db, errB := os.Stat(filepath.Dir(b))
	return errA == nil && errB == nil && os.SameFile(da, db)
}

// caseFoldingDir reports whether names in dir are looked up without regard
// to case. Windows (NTFS default): yes. Linux: no. macOS: APFS/HFS+ may be
// either, so it is probed — the deepest path element with letters is looked
// up with its case flipped and must be the same directory. A path without
// letters cannot be probed and counts as case-sensitive (exact compare).
func caseFoldingDir(dir string) bool {
	switch runtime.GOOS {
	case "windows":
		return true
	case "darwin":
	default:
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for p := abs; ; {
		base := filepath.Base(p)
		if flipped := flipCase(base); flipped != base {
			a, err1 := os.Stat(p)
			b, err2 := os.Stat(filepath.Join(filepath.Dir(p), flipped))
			return err1 == nil && err2 == nil && os.SameFile(a, b)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

func flipCase(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsUpper(r):
			return unicode.ToLower(r)
		case unicode.IsLower(r):
			return unicode.ToUpper(r)
		}
		return r
	}, s)
}
