package kicad

// schreview_hier.go — the sheet-pin / hierarchical-label comparison of the
// design review, read from the sheet files: each sheet symbol's pins
// against the hierarchical labels of the file it instantiates, once per
// sheet instance path (a repeated sheet is reported under each path).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// HierMismatch is a sheet pin without its hierarchical label or back.
type HierMismatch struct {
	Path   string `json:"path"`   // sheet instance path by name ("/Power/")
	Parent string `json:"parent"` // the parent sheet's file
	File   string `json:"file"`   // the sheet's file
	Kind   string `json:"kind"`   // pin-without-label | label-without-pin
	Name   string `json:"name"`
}

// SheetHierarchyMismatches walks the hierarchy of root and returns every
// mismatch and the number of sheet instances compared.
func SheetHierarchyMismatches(root string) ([]HierMismatch, int, error) {
	var out []HierMismatch
	n := 0
	cache := map[string]*sexp{}
	load := func(f string) (*sexp, error) {
		if t, ok := cache[f]; ok {
			return t, nil
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		t, err := parseSx(string(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		cache[f] = t
		return t, nil
	}
	var walk func(file, path string, depth int) error
	walk = func(file, path string, depth int) error {
		if depth > 32 {
			return fmt.Errorf("sheet hierarchy deeper than 32 (recursive?)")
		}
		t, err := load(file)
		if err != nil {
			return err
		}
		for _, s := range t.list {
			if s.head() != "sheet" {
				continue
			}
			name := propVal(s, "Sheetname")
			if name == "" {
				name = propVal(s, "Sheet name")
			}
			f := propVal(s, "Sheetfile")
			if f == "" {
				f = propVal(s, "Sheet file")
			}
			if f == "" {
				continue
			}
			child := filepath.Join(filepath.Dir(file), f)
			ct, err := load(child)
			if err != nil {
				return err
			}
			n++
			sub := path + name + "/"
			pins, labels := map[string]bool{}, map[string]bool{}
			for _, c := range s.list {
				if c.head() == "pin" && len(c.list) > 1 {
					pins[c.list[1].atom] = true
				}
			}
			for _, c := range ct.list {
				if c.head() == LabelHier && len(c.list) > 1 {
					labels[c.list[1].atom] = true
				}
			}
			var miss []HierMismatch
			for p := range pins {
				if !labels[p] {
					miss = append(miss, HierMismatch{Path: sub, Parent: filepath.Base(file), File: f, Kind: "pin-without-label", Name: p})
				}
			}
			for l := range labels {
				if !pins[l] {
					miss = append(miss, HierMismatch{Path: sub, Parent: filepath.Base(file), File: f, Kind: "label-without-pin", Name: l})
				}
			}
			sort.Slice(miss, func(i, j int) bool {
				if miss[i].Kind != miss[j].Kind {
					return miss[i].Kind > miss[j].Kind
				}
				return miss[i].Name < miss[j].Name
			})
			out = append(out, miss...)
			if err := walk(child, sub, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	err := walk(root, "/", 0)
	return out, n, err
}

// LibStacked lists, per library symbol embedded in the sheet files ("Lib:
// Part"), the groups of pin numbers drawn at one position of one unit and
// body style with one name — KiCad's stacked pins, one electrical pin.
func LibStacked(files []string) map[string][][]string {
	out := map[string][][]string{}
	for _, f := range files {
		e, err := OpenSchematicFile(f)
		if err != nil {
			continue
		}
		for id := range e.libs {
			if _, done := out[id]; done {
				continue
			}
			pins, err := e.LibPins(id)
			if err != nil {
				continue
			}
			at := map[string][]string{}
			var keys []string
			for _, p := range pins {
				k := fmt.Sprintf("%d|%d|%.4f|%.4f|%s", p.Unit, p.Style, p.At.X, p.At.Y, p.Name)
				if _, ok := at[k]; !ok {
					keys = append(keys, k)
				}
				at[k] = append(at[k], p.Number)
			}
			sort.Strings(keys)
			var groups [][]string
			for _, k := range keys {
				if g := at[k]; len(g) > 1 {
					sort.Strings(g)
					groups = append(groups, g)
				}
			}
			out[id] = groups
		}
	}
	return out
}
