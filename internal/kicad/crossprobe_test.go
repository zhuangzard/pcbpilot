package kicad

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSchHierarchy(t *testing.T) {
	pages, syms, err := ParseSchHierarchy("testdata/fab/fab_root.kicad_sch")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Name != "" || pages[1].Name != "Sub" ||
		pages[1].Path != "/aaaaaaaa-0000-0000-0000-000000000000/aaaaaaaa-0000-0000-0000-0000000000ff" {
		t.Fatalf("pages = %+v", pages)
	}
	got := map[string]SchSymbol{}
	for _, s := range syms {
		got[s.Ref] = s
	}
	r1, u1 := got["R1"], got["U1"]
	if r1.Page != 0 || u1.Page != 1 || len(syms) != 2 {
		t.Fatalf("symbols = %+v", syms)
	}
	// R1 sits at (50.8, 50.8): its body+pins box must contain the anchor.
	if !r1.Box.Valid() || r1.Box.MinX > 50.8 || r1.Box.MaxX < 50.8 || r1.Box.MinY > 50.8 || r1.Box.MaxY < 50.8 {
		t.Errorf("R1 box %+v does not hold its anchor", r1.Box)
	}
	if !r1.InBOM || !r1.OnBoard {
		t.Errorf("R1 in_bom/on_board = %v/%v", r1.InBOM, r1.OnBoard)
	}
}

func TestPCBNotInBOM(t *testing.T) {
	ex, err := PCBNotInBOM("testdata/fab/fab_board.kicad_pcb")
	if err != nil {
		t.Fatal(err)
	}
	if !ex["H1"] || ex["R1"] || len(ex) != 1 {
		t.Errorf("not-in-BOM = %v, want only H1", ex)
	}
}

func TestExportCrossProbeSVGs(t *testing.T) {
	if _, err := KicadCLI(); err != nil {
		t.Skip(err)
	}
	// kicad-cli writes a .kicad_prl next to the board: plot a copy
	fab := t.TempDir()
	for _, f := range []string{"fab_root.kicad_sch", "fab_sub.kicad_sch", "fab_board.kicad_pcb"} {
		b, err := os.ReadFile(filepath.Join("testdata/fab", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fab, f), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sch := filepath.Join(fab, "fab_root.kicad_sch")
	pages, _, err := ParseSchHierarchy(sch)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ps, ls, err := ExportCrossProbeSVGs(sch, filepath.Join(fab, "fab_board.kicad_pcb"), pages, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0] == "" || ps[1] == "" || filepath.Base(ps[1]) != "fab_root-Sub.svg" {
		t.Fatalf("page SVGs = %v", ps)
	}
	if len(ls) != len(CrossProbeLayers) {
		t.Fatalf("layer SVGs = %v", ls)
	}
	for _, f := range append(ps, ls...) {
		if st, err := os.Stat(f); err != nil || st.Size() == 0 {
			t.Errorf("%s: %v", f, err)
		}
	}
}
