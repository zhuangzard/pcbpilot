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
	// Not both existing: compare from the nearest existing ancestor of each
	// (a writer may still create the missing directories in between, e.g.
	// <dir>/new/report.json vs <junction to dir>/new/report.json).
	ancA, restA := existingAncestor(a)
	ancB, restB := existingAncestor(b)
	if len(restA) != len(restB) {
		return false
	}
	fold := len(restA) > 0 && caseFoldingDir(ancA)
	for i := range restA {
		if restA[i] != restB[i] && !(fold && strings.EqualFold(restA[i], restB[i])) {
			return false
		}
	}
	da, errA := os.Stat(ancA)
	db, errB := os.Stat(ancB)
	return errA == nil && errB == nil && os.SameFile(da, db)
}

// existingAncestor splits p into its nearest existing ancestor (followed
// through links by os.Stat) and the path elements below it that do not
// exist yet, top-down.
func existingAncestor(p string) (string, []string) {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	var rest []string
	for {
		if _, err := os.Stat(abs); err == nil {
			break
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			break
		}
		rest = append([]string{filepath.Base(abs)}, rest...)
		abs = parent
	}
	return abs, rest
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
