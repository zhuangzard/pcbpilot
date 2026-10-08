package app

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestKicadProjectZip(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"G.kicad_pro", "G.kicad_sch", "G.kicad_pcb", "G.kicad_prl", "fp-lib-table", "~G.kicad_pcb.lck", "G-backups"} {
		if n == "G-backups" {
			_ = os.Mkdir(filepath.Join(dir, n), 0o755)
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, name, err := kicadProjectZip(filepath.Join(dir, "G.kicad_pro"))
	if err != nil || name != "G.zip" {
		t.Fatalf("%v %q", err, name)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range zr.File {
		got = append(got, f.Name)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "G.kicad_pcb,G.kicad_pro,G.kicad_sch,fp-lib-table" {
		t.Fatalf("zip %v", got)
	}
	if _, _, err := kicadProjectZip(t.TempDir()); err == nil {
		t.Fatal("folder without .kicad_pro accepted")
	}
}
