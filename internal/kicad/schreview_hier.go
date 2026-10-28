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
	return out, n, walk(root, "/", 0)
}
