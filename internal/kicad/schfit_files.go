package kicad

import (
	"os"
	"path/filepath"
)

// SheetFiles returns the root schematic and every hierarchical sub-sheet
// file it references (recursively, each once).
func SheetFiles(root string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	var walk func(p string) error
	walk = func(p string) error {
		abs, _ := filepath.Abs(p)
		if seen[abs] {
			return nil
		}
		seen[abs] = true
		out = append(out, p)
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n, err := parseSx(string(src))
		if err != nil {
			return err
		}
		for _, s := range n.list {
			if s.head() != "sheet" {
				continue
			}
			for _, pr := range s.list {
				if pr.head() == "property" && len(pr.list) > 2 && (pr.list[1].atom == "Sheetfile" || pr.list[1].atom == "Sheet file") {
					if err := walk(filepath.Join(filepath.Dir(p), pr.list[2].atom)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	return out, walk(root)
}
