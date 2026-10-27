package kicad

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// kicadDemo copies a KiCad-shipped demo project into a temp dir (skip when
// the stock library / demos are not installed).
func kicadDemo(t *testing.T, name string) string {
	t.Helper()
	sd := StockSymbolDir()
	if sd == "" {
		t.Skip("KiCad stock library not found")
	}
	src := filepath.Join(sd, "..", "demos", name)
	if _, err := os.Stat(src); err != nil {
		t.Skip("KiCad demo " + name + " not installed")
	}
	dst := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(src, "*.kicad_*"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, filepath.Base(f)), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// TestFlattenMatchesKiCad: flattening a stock derived symbol gives the
// fields and attributes KiCad itself embedded for it in a shipped demo.
func TestFlattenMatchesKiCad(t *testing.T) {
	sd := StockSymbolDir()
	if sd == "" {
		t.Skip("KiCad stock library not found")
	}
	cases := []struct{ lib, name, sheet string }{
		{"Connector", "Conn_ARM_SWD_TagConnect_TC2030-NL", "royalblue54L_feather/sch/Debugger.kicad_sch"},
		{"Transistor_FET", "IRF540N", "simulation/power_supplies/buck_conv/buck_conv.kicad_sch"},
	}
	for _, c := range cases {
		sheet := filepath.Join(sd, "..", "demos", filepath.FromSlash(c.sheet))
		e, err := OpenSchematicFile(sheet)
		if err != nil {
			t.Skip(err)
		}
		kicadSym := e.libs[c.lib+":"+c.name]
		if kicadSym == nil {
			t.Fatalf("%s not embedded in %s", c.name, c.sheet)
		}
		text, err := LibSymbolCached(filepath.Join(sd, c.lib+".kicad_sym"), c.name)
		if err != nil {
			t.Fatal(err)
		}
		ours, err := parseSx(text)
		if err != nil {
			t.Fatal(err)
		}
		for name := range flattenInherits {
			if got, want := propVal(ours, name), propVal(kicadSym, name); got != want {
				t.Errorf("%s %s = %q, KiCad embedded %q", c.name, name, got, want)
			}
		}
		for _, h := range []string{"exclude_from_sim", "in_bom", "on_board"} {
			a, b := ours.child(h), kicadSym.child(h)
			if a == nil || b == nil || a.list[1].atom != b.list[1].atom {
				t.Errorf("%s %s: ours %v, KiCad %v", c.name, h, a, b)
			}
		}
		if (ours.child("pin_names") == nil) != (kicadSym.child("pin_names") == nil) {
			t.Errorf("%s pin_names presence differs", c.name)
		}
	}
}

// A placed De Morgan instance (body style 2) has its pins where style 2
// draws them; common-style pins stay.
func TestPinPositionsFollowBodyStyle(t *testing.T) {
	gate := `(symbol "G"
		(symbol "G_0_0" (pin power_in line (at 0 7.62 270) (length 2.54) (name "VCC" (effects (font (size 1.27 1.27)))) (number "14" (effects (font (size 1.27 1.27))))))
		(symbol "G_1_1" (pin input line (at -5.08 0 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))
		(symbol "G_1_2" (pin input line (at -7.62 2.54 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))
	)`
	e, _ := OpenSchematic(NewSchematicText("A4", NewUUID(), true))
	e.Project = "t"
	if err := e.AddLibSymbol("my:G", gate); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PlaceSymbol(SymbolInstance{LibID: "my:G", Ref: "U1", Value: "G", At: Pt{50.8, 50.8}}); err != nil {
		t.Fatal(err)
	}
	text, _ := e.Render()
	n1, _ := OpenSchematic(text)
	p1, _ := n1.SymbolPinPositions("U1")
	alt, _ := OpenSchematic(strings.Replace(text, "(unit 1)\n", "(unit 1)\n\t\t(body_style 2)\n", 1))
	p2, err := alt.SymbolPinPositions("U1")
	if err != nil {
		t.Fatal(err)
	}
	if p1["1"] != (Pt{45.72, 50.8}) || p2["1"] != (Pt{43.18, 48.26}) {
		t.Errorf("pin 1: style 1 %v, style 2 %v", p1["1"], p2["1"])
	}
	if p2["14"] != p1["14"] || p1["14"] == (Pt{}) {
		t.Errorf("common pin 14: %v / %v", p1["14"], p2["14"])
	}
	sc, _ := alt.Scene()
	for _, s := range sc.Symbols {
		for _, p := range s.Pins {
			if p.Number == "1" && p.At != p2["1"] {
				t.Errorf("scene pin 1 at %v", p.At)
			}
		}
	}
}

func TestRenameValidation(t *testing.T) {
	e := multiUnitSheet(t)
	if err := e.AddLibSymbol("test:R", testRes); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PlaceSymbol(SymbolInstance{LibID: "test:R", Ref: "R1", Value: "1k", At: Pt{101.6, 50.8}}); err != nil {
		t.Fatal(err)
	}
	text, _ := e.Render()
	for _, bad := range []string{"", "U:9", "U 9", "U\t9", "U9\n", "U.9", "U\x019", `U"9`, "${X}", "#PWR9", "R1"} {
		e2, _ := OpenSchematic(text)
		if err := e2.SetField("U1", "Reference", bad); err == nil {
			t.Errorf("rename to %q accepted", bad)
		}
		if out, _ := e2.Render(); out != text {
			t.Errorf("refused rename to %q changed the sheet", bad)
		}
	}
	e2, _ := OpenSchematic(text)
	if err := e2.SetField("U1", "Reference", "U10"); err != nil {
		t.Fatal(err)
	}
}

// TestHierarchyRefusals: on KiCad's complex_hierarchy demo ampli_ht.kicad_sch
// is used by two sheet symbols (U201 / U301 are one symbol); a reference on
// another sheet is not a free name; units split over sheets are not edited
// one sheet at a time.
func TestHierarchyRefusals(t *testing.T) {
	dir := kicadDemo(t, "complex_hierarchy")
	ampli := filepath.Join(dir, "ampli_ht.kicad_sch")
	e, err := OpenSchematicFile(ampli)
	if err != nil {
		t.Fatal(err)
	}
	if !e.RepeatedSheet() {
		t.Fatal("ampli_ht.kicad_sch not seen as a repeated sheet")
	}
	if err := e.SetField("U201", "Reference", "U999"); err == nil || !strings.Contains(err.Error(), "several sheet symbols") {
		t.Errorf("rename on a repeated sheet: %v", err)
	}
	if err := e.SetField("U201", "Value", "LM358"); err != nil { // fields are shared by every use
		t.Errorf("field edit on a repeated sheet: %v", err)
	}
	root, err := OpenSchematicFile(filepath.Join(dir, "complex_hierarchy.kicad_sch"))
	if err != nil {
		t.Fatal(err)
	}
	if got := root.RefElsewhere("U201"); len(got) != 1 || got[0] != "ampli_ht.kicad_sch" {
		t.Errorf("RefElsewhere(U201) = %v", got)
	}
	if err := root.SetField("U101", "Reference", "U201"); err == nil || !strings.Contains(err.Error(), "ampli_ht.kicad_sch") {
		t.Errorf("rename onto a reference of another sheet: %v", err)
	}
	if err := root.SetField("U101", "Reference", "U301"); err == nil { // the second use's name
		t.Error("rename onto U301 accepted")
	}

	// one component, unit 1 on a.kicad_sch and unit 2 on b.kicad_sch, both
	// sub-sheets of split.kicad_sch
	split := t.TempDir()
	writeProject(t, split, "split", map[string]string{"A": "a.kicad_sch", "B": "b.kicad_sch"})
	for i, name := range []string{"a", "b"} {
		s, _ := OpenSchematic(NewSchematicText("A4", NewUUID(), false))
		s.Project, s.InstancePath = "split", "/x/"+name
		_ = s.AddLibSymbol("my:DUAL", dualSym)
		if _, err := s.PlaceSymbol(SymbolInstance{LibID: "my:DUAL", Ref: "U1", Unit: i + 1, Value: "DUAL", At: Pt{50.8, 50.8}}); err != nil {
			t.Fatal(err)
		}
		out, _ := s.Render()
		_ = os.WriteFile(filepath.Join(split, name+".kicad_sch"), []byte(out), 0o644)
	}
	open := func() *SchEditor {
		a, err := OpenSchematicFile(filepath.Join(split, "a.kicad_sch"))
		if err != nil || a.HierarchyErr() != nil {
			t.Fatalf("%v / %v", err, a.HierarchyErr())
		}
		return a
	}
	if err := open().SetField("U1", "Value", "X"); err == nil || !strings.Contains(err.Error(), "b.kicad_sch") {
		t.Errorf("field edit of a split component: %v", err)
	}
	if err := open().SetField("U1", "Reference", "U2"); err == nil {
		t.Error("rename of a split component accepted")
	}
	if _, err := open().DeleteSymbol("U1"); err == nil {
		t.Error("deleting every unit of a split component accepted")
	}
	if _, err := open().DeleteSymbol("U1:1"); err != nil {
		t.Errorf("deleting one unit: %v", err)
	}
}

// writeProject writes <name>.kicad_pro and a root <name>.kicad_sch whose
// sheet symbols (sheet name → Sheetfile) use the given files.
func writeProject(t *testing.T, dir, name string, sheets map[string]string) string {
	t.Helper()
	var syms []string
	for sn, file := range sheets {
		syms = append(syms, sheetSymbol(sn, file))
	}
	sort.Strings(syms)
	root := strings.Replace(NewSchematicText("A4", NewUUID(), true), "\t(sheet_instances", "\t"+strings.Join(syms, "\n\t")+"\n\t(sheet_instances", 1)
	_ = os.WriteFile(filepath.Join(dir, name+".kicad_pro"), []byte("{}\n"), 0o644)
	p := filepath.Join(dir, name+".kicad_sch")
	if err := os.WriteFile(p, []byte(root), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func sheetSymbol(name, file string) string {
	return `(sheet (at 25.4 25.4) (size 20 10) (uuid "` + NewUUID() + `")
		(property "Sheetname" "` + name + `" (at 25.4 24 0) (effects (font (size 1.27 1.27))))
		(property "Sheetfile" "` + file + `" (at 25.4 36 0) (effects (font (size 1.27 1.27)))))`
}

// An empty sheet two levels below the root gets its full instance path.
func TestDeepEmptySheetInstancePath(t *testing.T) {
	p := t.TempDir()
	rootPath := writeProject(t, p, "deep", map[string]string{"Mid": "mid.kicad_sch"})
	mid := strings.Replace(NewSchematicText("A4", NewUUID(), false), "\t(embedded_fonts", "\t"+sheetSymbol("Leaf", "leaf/leaf.kicad_sch")+"\n\t(embedded_fonts", 1)
	_ = os.WriteFile(filepath.Join(p, "mid.kicad_sch"), []byte(mid), 0o644)
	_ = os.MkdirAll(filepath.Join(p, "leaf"), 0o755)
	_ = os.WriteFile(filepath.Join(p, "leaf", "leaf.kicad_sch"), []byte(NewSchematicText("A4", NewUUID(), false)), 0o644)
	r, _ := parseSx(mustRead(t, rootPath))
	m, _ := parseSx(mid)
	want := "/" + r.child("uuid").list[1].atom + "/" + r.child("sheet").child("uuid").list[1].atom + "/" + m.child("sheet").child("uuid").list[1].atom
	leaf, err := OpenSchematicFile(filepath.Join(p, "leaf", "leaf.kicad_sch"))
	if err != nil || leaf.HierarchyErr() != nil {
		t.Fatalf("%v / %v", err, leaf.HierarchyErr())
	}
	if leaf.InstancePath != want {
		t.Errorf("instance path %q, want %q", leaf.InstancePath, want)
	}
	_ = leaf.AddLibSymbol("my:DUAL", dualSym)
	if _, err := leaf.PlaceSymbol(SymbolInstance{LibID: "my:DUAL", Ref: "U1", Value: "DUAL", At: Pt{50.8, 50.8}}); err != nil {
		t.Fatal(err)
	}
	out, _ := leaf.Render()
	if !strings.Contains(out, `(path "`+want+`"`) {
		t.Error("placed symbol lacks the full instance path")
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Without a known hierarchy (a sub-sheet outside any project, or two
// projects whose roots do not include it) renames and deleting every unit
// are refused; a plain field edit proceeds. With several .kicad_pro in one
// directory the project whose tree holds the sheet is used.
func TestUnknownHierarchyRefuses(t *testing.T) {
	lone := t.TempDir()
	s, _ := OpenSchematic(NewSchematicText("A4", NewUUID(), false))
	s.Project = "lone"
	_ = s.AddLibSymbol("my:DUAL", dualSym)
	for u := 1; u <= 2; u++ {
		_, _ = s.PlaceSymbol(SymbolInstance{LibID: "my:DUAL", Ref: "U1", Unit: u, Value: "DUAL", At: Pt{50.8, 25.4 * float64(u)}})
	}
	text, _ := s.Render()
	sub := filepath.Join(lone, "sub.kicad_sch")
	_ = os.WriteFile(sub, []byte(text), 0o644)
	e, err := OpenSchematicFile(sub)
	if err != nil {
		t.Fatal(err)
	}
	if e.HierarchyErr() == nil {
		t.Fatal("a sub-sheet outside any project has a known hierarchy")
	}
	if err := e.SetField("U1", "Reference", "U2"); err == nil || !strings.Contains(err.Error(), "hierarchy") {
		t.Errorf("rename: %v", err)
	}
	if _, err := e.DeleteSymbol("U1"); err == nil || !strings.Contains(err.Error(), "hierarchy") {
		t.Errorf("delete every unit: %v", err)
	}
	if err := e.SetField("U1", "Value", "LM358"); err != nil {
		t.Errorf("field edit: %v", err)
	}
	// two projects in one directory: only "good" reaches sub.kicad_sch
	writeProject(t, lone, "other", map[string]string{"X": "x.kicad_sch"})
	writeProject(t, lone, "good", map[string]string{"S": "sub.kicad_sch"})
	e, _ = OpenSchematicFile(sub)
	if e.HierarchyErr() != nil {
		t.Errorf("project not chosen among two: %v", e.HierarchyErr())
	}
	writeProject(t, lone, "twin", map[string]string{"S": "sub.kicad_sch"})
	e, _ = OpenSchematicFile(sub)
	if err := e.HierarchyErr(); err == nil || !strings.Contains(err.Error(), "several projects") {
		t.Errorf("two projects holding the sheet: %v", err)
	}
}

// Sheetfile spelled with other letter case still names the sheet on the
// case-insensitive file systems of Windows and macOS.
func TestMixedCaseNestedSheet(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("case-sensitive file system")
	}
	p := t.TempDir()
	writeProject(t, p, "mc", map[string]string{"A": "Sub/Child.KiCad_Sch", "B": "sub/child.kicad_sch"})
	_ = os.MkdirAll(filepath.Join(p, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(p, "sub", "child.kicad_sch"), []byte(NewSchematicText("A4", NewUUID(), false)), 0o644)
	e, err := OpenSchematicFile(filepath.Join(p, "SUB", "CHILD.kicad_sch"))
	if err != nil || e.HierarchyErr() != nil {
		t.Fatalf("%v / %v", err, e.HierarchyErr())
	}
	if !e.RepeatedSheet() || e.sheetUses != 2 {
		t.Errorf("mixed-case uses of one sheet: repeated=%v uses=%d", e.RepeatedSheet(), e.sheetUses)
	}
}

// Queued renames count before Render: two renames onto one name are refused,
// a renamed symbol answers to its new name, a freed name can be reused, and
// repeated edits of one item replace each other instead of overlapping.
func TestQueuedRenames(t *testing.T) {
	e := multiUnitSheet(t)
	_ = e.AddLibSymbol("test:R", testRes)
	for i, r := range []string{"R1", "R2"} {
		if _, err := e.PlaceSymbol(SymbolInstance{LibID: "test:R", Ref: r, Value: "1k", At: Pt{101.6 + 25.4*float64(i), 50.8}}); err != nil {
			t.Fatal(err)
		}
	}
	text, _ := e.Render()
	e, _ = OpenSchematic(text)
	if err := e.SetField("R1", "Reference", "R9"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetField("R2", "Reference", "R9"); err == nil {
		t.Fatal("second rename onto R9 accepted")
	}
	if err := e.SetField("R9", "Value", "2k"); err != nil { // the renamed R1
		t.Fatal(err)
	}
	if err := e.SetField("R9", "Reference", "R10"); err != nil { // renamed twice: last wins
		t.Fatal(err)
	}
	if err := e.SetField("R2", "Reference", "R1"); err != nil { // R1 is free now
		t.Fatal(err)
	}
	if err := e.SetField("U1", "Reference", "R10"); err == nil {
		t.Fatal("rename onto a queued name accepted")
	}
	for _, at := range []Pt{{127, 101.6}, {152.4, 101.6}} {
		if err := e.MoveSymbol("U1:2", at, 0); err != nil {
			t.Fatal(err)
		}
	}
	out, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := OpenSchematic(out)
	if u := e2.SymbolUnits("R10"); len(u) != 1 || len(e2.SymbolUnits("R9")) != 0 || len(e2.SymbolUnits("R1")) != 1 || len(e2.SymbolUnits("R2")) != 0 {
		t.Errorf("after the renames: R10 %v R9 %v R1 %v R2 %v", u, e2.SymbolUnits("R9"), e2.SymbolUnits("R1"), e2.SymbolUnits("R2"))
	}
	if f := e2.SymbolFields("R10"); len(f) < 2 || f[1].Value != "2k" {
		t.Errorf("R10 fields %+v", f)
	}
	if x, _, _, _ := e2.SymbolAt("U1:2"); x != 152.4 {
		t.Errorf("U1:2 at x=%v, want the last move", x)
	}
}

// A deletion batch is resolved as one set: overlapping and repeated keys,
// in either order, delete each unit once.
func TestDeleteSymbolsBatch(t *testing.T) {
	for _, keys := range [][]string{{"U1:1", "U1"}, {"U1", "U1:1"}, {"U1", "U1"}, {"U1:2", "U1:2", "U1:1"}} {
		e := multiUnitSheet(t)
		got, err := e.DeleteSymbols(keys)
		if err != nil {
			t.Fatalf("%v: %v", keys, err)
		}
		if u := got["U1"]; len(u) != 2 || u[0] != 1 || u[1] != 2 {
			t.Errorf("%v: removed %v", keys, u)
		}
		out, err := e.Render()
		if err != nil {
			t.Fatalf("%v: %v", keys, err)
		}
		if strings.Contains(out, `(lib_id "my:DUAL")`) {
			t.Errorf("%v: a unit survived", keys)
		}
		if _, err := e.DeleteSymbol("U1"); err == nil {
			t.Errorf("%v: deleting a queued-deleted symbol again succeeded", keys)
		}
	}
	e := multiUnitSheet(t)
	if _, err := e.DeleteSymbols([]string{"U1:2", "U1:3"}); err == nil {
		t.Error("batch with a missing unit accepted")
	}
	if out, _ := e.Render(); !strings.Contains(out, "(unit 2)") {
		t.Error("a refused batch deleted something")
	}
}

// TestNestedReusedSheet: sub-sheets in a subdirectory are found by walking
// the hierarchy from the project's root sheet.
func TestNestedReusedSheet(t *testing.T) {
	dir := kicadDemo(t, "complex_hierarchy")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "ampli_ht.kicad_sch"), filepath.Join(dir, "sub", "ampli_ht.kicad_sch")); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(dir, "complex_hierarchy.kicad_sch")
	b, _ := os.ReadFile(rootPath)
	_ = os.WriteFile(rootPath, []byte(strings.ReplaceAll(string(b), `(property "Sheetfile" "ampli_ht.kicad_sch"`, `(property "Sheetfile" "sub/ampli_ht.kicad_sch"`)), 0o644)
	root, err := OpenSchematicFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := root.RefElsewhere("U201"); len(got) != 1 || got[0] != "sub/ampli_ht.kicad_sch" {
		t.Errorf("RefElsewhere(U201) from the root = %v", got)
	}
	sub, err := OpenSchematicFile(filepath.Join(dir, "sub", "ampli_ht.kicad_sch"))
	if err != nil {
		t.Fatal(err)
	}
	if got := sub.RefElsewhere("U101"); len(got) != 1 || got[0] != "complex_hierarchy.kicad_sch" {
		t.Errorf("RefElsewhere(U101) from the nested sheet = %v", got)
	}
	if !sub.RepeatedSheet() || sub.sheetUses != 2 {
		t.Errorf("nested reused sheet: repeated=%v uses=%d", sub.RepeatedSheet(), sub.sheetUses)
	}

	// a symbol-less nested sheet used twice: only the hierarchy walk sees it
	p := t.TempDir()
	_ = os.WriteFile(filepath.Join(p, "top.kicad_pro"), []byte("{}\n"), 0o644)
	sheet := func(name string) string {
		return `(sheet (at 25.4 25.4) (size 20 10) (uuid "` + NewUUID() + `")
		(property "Sheetname" "` + name + `" (at 25.4 24 0) (effects (font (size 1.27 1.27))))
		(property "Sheetfile" "sub/child.kicad_sch" (at 25.4 36 0) (effects (font (size 1.27 1.27)))))`
	}
	top := strings.Replace(NewSchematicText("A4", NewUUID(), true), "\t(sheet_instances", "\t"+sheet("A")+"\n\t"+sheet("B")+"\n\t(sheet_instances", 1)
	_ = os.WriteFile(filepath.Join(p, "top.kicad_sch"), []byte(top), 0o644)
	_ = os.MkdirAll(filepath.Join(p, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(p, "sub", "child.kicad_sch"), []byte(NewSchematicText("A4", NewUUID(), false)), 0o644)
	child, err := OpenSchematicFile(filepath.Join(p, "sub", "child.kicad_sch"))
	if err != nil {
		t.Fatal(err)
	}
	if !child.RepeatedSheet() {
		t.Error("sub/child.kicad_sch used by two sheet symbols not seen as repeated")
	}
}

// A sheet reached through a link (hard link, or symlink where allowed) is
// the same node as the sheet: both uses count.
func TestSheetAliases(t *testing.T) {
	for _, kind := range []string{"hardlink", "symlink"} {
		p := t.TempDir()
		writeProject(t, p, "al", map[string]string{"A": "sub/child.kicad_sch", "B": "sub/alias.kicad_sch"})
		_ = os.MkdirAll(filepath.Join(p, "sub"), 0o755)
		child := filepath.Join(p, "sub", "child.kicad_sch")
		_ = os.WriteFile(child, []byte(NewSchematicText("A4", NewUUID(), false)), 0o644)
		alias := filepath.Join(p, "sub", "alias.kicad_sch")
		var err error
		if kind == "hardlink" {
			err = os.Link(child, alias)
		} else {
			err = os.Symlink("child.kicad_sch", alias)
		}
		if err != nil {
			t.Logf("%s not available: %v", kind, err)
			continue
		}
		e, err := OpenSchematicFile(child)
		if err != nil || e.HierarchyErr() != nil {
			t.Fatalf("%s: %v / %v", kind, err, e.HierarchyErr())
		}
		if !e.RepeatedSheet() || e.sheetUses != 2 {
			t.Errorf("%s: a sheet and its alias: repeated=%v uses=%d", kind, e.RepeatedSheet(), e.sheetUses)
		}
	}
}

// A symbol on a reused sheet is every use's: a field edit through its first
// reference is refused when only its second reference (another use) also
// has units on another sheet.
func TestReusedSheetFieldEditChecksEveryReference(t *testing.T) {
	p := t.TempDir()
	writeProject(t, p, "ru", map[string]string{"A": "sub.kicad_sch", "B": "sub.kicad_sch", "C": "other.kicad_sch"})
	s, _ := OpenSchematic(NewSchematicText("A4", NewUUID(), false))
	s.Project, s.InstancePath = "ru", "/r/a"
	_ = s.AddLibSymbol("my:DUAL", dualSym)
	if _, err := s.PlaceSymbol(SymbolInstance{LibID: "my:DUAL", Ref: "U1", Value: "DUAL", At: Pt{50.8, 50.8}}); err != nil {
		t.Fatal(err)
	}
	text, _ := s.Render()
	// the second use names it U2
	text = strings.Replace(text, "\t\t\t\t(path \"/r/a\"\n\t\t\t\t\t(reference \"U1\")\n\t\t\t\t\t(unit 1)\n\t\t\t\t)",
		"\t\t\t\t(path \"/r/a\"\n\t\t\t\t\t(reference \"U1\")\n\t\t\t\t\t(unit 1)\n\t\t\t\t)\n\t\t\t\t(path \"/r/b\"\n\t\t\t\t\t(reference \"U2\")\n\t\t\t\t\t(unit 1)\n\t\t\t\t)", 1)
	if !strings.Contains(text, `(reference "U2")`) {
		t.Fatal("fixture: second instance path not added")
	}
	_ = os.WriteFile(filepath.Join(p, "sub.kicad_sch"), []byte(text), 0o644)
	o, _ := OpenSchematic(NewSchematicText("A4", NewUUID(), false))
	o.Project, o.InstancePath = "ru", "/r/c"
	_ = o.AddLibSymbol("my:DUAL", dualSym)
	_, _ = o.PlaceSymbol(SymbolInstance{LibID: "my:DUAL", Ref: "U2", Unit: 2, Value: "DUAL", At: Pt{50.8, 50.8}})
	ot, _ := o.Render()
	_ = os.WriteFile(filepath.Join(p, "other.kicad_sch"), []byte(ot), 0o644)
	e, err := OpenSchematicFile(filepath.Join(p, "sub.kicad_sch"))
	if err != nil || e.HierarchyErr() != nil {
		t.Fatalf("%v / %v", err, e.HierarchyErr())
	}
	if err := e.SetField("U1", "Value", "X"); err == nil || !strings.Contains(err.Error(), "U2") || !strings.Contains(err.Error(), "other.kicad_sch") {
		t.Errorf("field edit through U1 while U2 has units elsewhere: %v", err)
	}
	if out, _ := e.Render(); out != text {
		t.Error("a refused edit queued changes")
	}
	if _, err := e.DeleteSymbol("U1:1"); err == nil {
		t.Error("delete on a reused sheet accepted")
	}
}
