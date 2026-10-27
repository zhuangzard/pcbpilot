package kicad

// pcbsync_sheets.go — the schematic files a sync depends on: the root sheet
// and every sheet it references (sheet symbols' Sheetfile, recursively),
// wherever they live (../ included), read once so they can be hashed and
// copied with their layout. pcb-sync exports the netlist and runs parity
// from such a copy, never from the live files.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrSheetPath: a sheet reference that cannot be followed into a copy (an
// absolute path, a missing or unreadable file).
const ErrSheetPath = "E_SHEET_PATH"

// SchSheetFile is one schematic file and the bytes read.
type SchSheetFile struct {
	Path string // absolute, cleaned
	Data []byte
}

// SchematicSheets reads root and, recursively, every sheet a sheet symbol
// names. A relative Sheetfile is resolved against the referencing sheet's
// directory (KiCad's rule, see scherc.go); when the same name also exists
// relative to the root's directory, that file is included too (the
// dependency set may only be larger, never miss one). An absolute Sheetfile
// is refused: a copy of the sheets would still point at the live file.
// Root first, the rest sorted.
func SchematicSheets(root string) ([]SchSheetFile, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootDir := filepath.Dir(abs)
	seen := map[string]bool{}
	var out []SchSheetFile
	var visit func(p string, from string, depth int) error
	visit = func(p, from string, depth int) error {
		key := strings.ToLower(filepath.Clean(p))
		if seen[key] {
			return nil
		}
		seen[key] = true
		if depth > 64 {
			return &SyncRefusal{Errors: []SyncError{{Code: ErrSheetPath, Path: p, Detail: "sheet hierarchy deeper than 64 levels"}}}
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return &SyncRefusal{Errors: []SyncError{{Code: ErrSheetPath, Path: p,
				Detail: fmt.Sprintf("sheet %s (referenced from %s) cannot be read: %v", p, from, err), Fix: "fix the sheet file name in the parent sheet"}}}
		}
		out = append(out, SchSheetFile{Path: filepath.Clean(p), Data: b})
		n, err := parseSx(string(b))
		if err != nil {
			return &SyncRefusal{Errors: []SyncError{{Code: ErrSheetPath, Path: p, Detail: "not a KiCad schematic: " + err.Error()}}}
		}
		for _, s := range n.list {
			if s.head() != "sheet" {
				continue
			}
			f := propVal(s, "Sheetfile")
			if f == "" {
				f = propVal(s, "Sheet file")
			}
			if f == "" {
				continue
			}
			if filepath.IsAbs(f) || filepath.VolumeName(f) != "" || strings.HasPrefix(f, "/") || strings.HasPrefix(f, `\`) {
				return &SyncRefusal{Errors: []SyncError{{Code: ErrSheetPath, Path: p,
					Detail: fmt.Sprintf("sheet %s references %q by an absolute path", p, f),
					Fix:    "make the sheet path relative (KiCad: sheet properties > File name)"}}}
			}
			cands := []string{filepath.Join(filepath.Dir(p), f)}
			if alt := filepath.Join(rootDir, f); !strings.EqualFold(alt, cands[0]) {
				if _, err := os.Stat(alt); err == nil {
					cands = append(cands, alt)
				}
			}
			for _, c := range cands {
				if err := visit(c, p, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(abs, "the command line", 0); err != nil {
		return nil, err
	}
	rest := out[1:]
	sort.Slice(rest, func(i, j int) bool { return rest[i].Path < rest[j].Path })
	return out, nil
}

// CopySheetTree writes files under dst with their layout relative to the
// deepest directory containing all of them, and returns where each landed
// (same order).
func CopySheetTree(files []SchSheetFile, dst string) ([]string, error) {
	if len(files) == 0 {
		return nil, nil
	}
	base := filepath.Dir(files[0].Path)
	for _, f := range files[1:] {
		for !within(f.Path, base) {
			up := filepath.Dir(base)
			if up == base {
				return nil, fmt.Errorf("%s and %s share no directory", files[0].Path, f.Path)
			}
			base = up
		}
	}
	out := make([]string, len(files))
	for i, f := range files {
		rel, err := filepath.Rel(base, f.Path)
		if err != nil {
			return nil, err
		}
		p := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			return nil, err
		}
		out[i] = p
	}
	return out, nil
}

func within(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
