package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbFakeLCSCPart writes a converted LCSC part into the pcbpilot cache (the
// offline state after `kicad lcsc --import`): a two-pin symbol and its
// footprint.
func sbFakeLCSCPart(t *testing.T, lcsc, sym, fp string) {
	t.Helper()
	dir := kicad.LCSCCacheDir(lcsc)
	if err := os.MkdirAll(filepath.Join(dir, "lcsc.pretty"), 0o755); err != nil {
		t.Fatal(err)
	}
	symText := kicad.GenericSymbolText(sym, "C", []kicad.GenPin{{Number: "1"}, {Number: "2"}})
	files := map[string]string{
		"part.json":      `{"lcsc":"` + lcsc + `","symbolName":"` + sym + `","footprintName":"` + fp + `"}`,
		"lcsc.kicad_sym": kicad.SymbolLibText(map[string]string{sym: symText}),
		"lcsc.pretty/" + fp + ".kicad_mod": `(footprint "` + fp + `" (version 20240108) (generator "test") (layer "F.Cu")
	(pad "1" smd rect (at -0.8 0) (size 0.8 0.9) (layers "F.Cu" "F.Paste" "F.Mask"))
	(pad "2" smd rect (at 0.8 0) (size 0.8 0.9) (layers "F.Cu" "F.Paste" "F.Mask")))
`,
	}
	for n, s := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(n)), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSchEditKeepsLCSCFootprints: a sch-edit (even a value-only set_value)
// rebuilds from the stored spec, whose LCSC part already names its
// footprint "lcsc:…"; v0.10.1 then copied no footprint, wrote no
// fp-lib-table and the commit deleted the old ones (ERC footprint_link_issues
// for lcsc, yet ok:true). The footprints must survive, the checkpoint must
// list them, a sch-read → sch-build copy must carry them, and a project
// whose own library cannot resolve must fail the ERC gate.
func TestSchEditKeepsLCSCFootprints(t *testing.T) {
	sbNeedKicad(t)
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	sbFakeLCSCPart(t, "C99901", "TCAP", "TFP_0603")
	out := filepath.Join(t.TempDir(), "lc")
	spec := &sbSpec{Name: "lc",
		Parts: []sbPart{{Ref: "C1", LCSC: "C99901", Value: "100nF"}, {Ref: "R1", Value: "1k", Symbol: "Device:R"}},
		Nets: []sbNet{
			{Name: "+3V3", Kind: "power", Pins: []string{"C1:1", "R1:1"}},
			{Name: "GND", Kind: "ground", Pins: []string{"C1:2", "R1:2"}},
		}}
	opt := sbOptions{OutDir: out, NoIntent: true, Jobs: 2, PlannerBudget: 20000}
	res, err := runSchBuild(spec, opt, nil)
	if err != nil || !res.OK {
		t.Fatalf("build: %v %+v", err, res)
	}
	mod := filepath.Join("lcsc.pretty", "TFP_0603.kicad_mod")
	has := func(dir, what string) {
		t.Helper()
		for _, f := range []string{"fp-lib-table", mod} {
			if !fileExists(filepath.Join(dir, f)) {
				t.Errorf("%s: %s missing", what, f)
			}
		}
	}
	has(out, "build")
	er, err := runSchEdit(opt, []sbOp{{Op: "set_value", Ref: "R1", Value: "2k2"}})
	if err != nil || !er.OK || sbGateStatus(er, "erc") == "fail" {
		t.Fatalf("set_value: %v %+v", err, er)
	}
	has(out, "after sch-edit set_value")
	list, _ := listSbCheckpoints(out)
	if len(list) == 0 {
		t.Fatal("no checkpoint")
	}
	last := strings.Join(list[len(list)-1].Files, " ")
	for _, f := range []string{"fp-lib-table", "lcsc.pretty/TFP_0603.kicad_mod"} {
		if !strings.Contains(last, f) {
			t.Errorf("checkpoint %d lacks %s: %s", list[len(list)-1].N, f, last)
		}
	}
	rd, err := readSbSpec(out, "")
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "copy")
	if res, err := runSchBuild(rd, sbOptions{OutDir: cp, NoIntent: true, Jobs: 2, PlannerBudget: 20000}, nil); err != nil || !res.OK {
		t.Fatalf("sch-read → sch-build: %v %+v", err, res)
	}
	has(cp, "sch-read → sch-build")

	// the footprint gone from the cache and the project: the own library
	// cannot resolve — the ERC gate fails and nothing is written
	_ = os.Remove(filepath.Join(kicad.LCSCCacheDir("C99901"), mod))
	_ = os.Remove(filepath.Join(out, mod))
	before, _ := os.ReadFile(filepath.Join(out, "lc.kicad_sch"))
	// removing the project's footprint is itself an edit outside pcbpilot:
	// refused before any build, unless the user chooses to discard it
	if er, err := runSchEdit(opt, []sbOp{{Op: "set_value", Ref: "R1", Value: "3k3"}}); er == nil || er.OK || er.Error == nil || er.Error.Code != "EXTERNAL_EDIT" {
		t.Errorf("removed project footprint not refused as an external edit: err=%v report=%+v", err, er)
	}
	opt.DiscardExternal = true
	er, err = runSchEdit(opt, []sbOp{{Op: "set_value", Ref: "R1", Value: "3k3"}})
	if er == nil || er.OK || sbGateStatus(er, "erc") != "fail" {
		t.Errorf("unresolvable own footprint library accepted: err=%v report=%+v", err, er)
	}
	if after, _ := os.ReadFile(filepath.Join(out, "lc.kicad_sch")); string(after) != string(before) {
		t.Error("a failed edit changed the project")
	}
}

// sbFakeFootprintLib writes <dir>/<lib>.pretty/<fp>.kicad_mod (two pads).
func sbFakeFootprintLib(t *testing.T, dir, lib, fp string) {
	t.Helper()
	d := filepath.Join(dir, lib+".pretty")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	text := `(footprint "` + fp + `" (version 20240108) (generator "test") (layer "F.Cu")
	(pad "1" smd rect (at -0.8 0) (size 0.8 0.9) (layers "F.Cu" "F.Paste" "F.Mask"))
	(pad "2" smd rect (at 0.8 0) (size 0.8 0.9) (layers "F.Cu" "F.Paste" "F.Mask")))
`
	if err := os.WriteFile(filepath.Join(d, fp+".kicad_mod"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSchBuildShipsSpecFootprintLibs: a footprint from a project library
// under libDirs travels with the project whatever the symbol came from —
// an LCSC part, a stock symbol, a generated one. v0.12.0 copied it only
// next to a local .kicad_sym symbol; otherwise the library was neither
// copied nor in fp-lib-table, silently, and pcb-sync then failed with
// E_FP_LIB_UNRESOLVED. A libDirs entry may also be the .pretty itself. A
// sch-edit rebuild keeps them; a library found nowhere is a spec error.
func TestSchBuildShipsSpecFootprintLibs(t *testing.T) {
	sbNeedKicad(t)
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	sbFakeLCSCPart(t, "C99902", "TCAP2", "TFP_0402")
	libs := t.TempDir()
	sbFakeFootprintLib(t, libs, "plib", "FP_A")
	sbFakeFootprintLib(t, libs, "plib2", "FP_B")
	out := filepath.Join(t.TempDir(), "fl")
	spec := &sbSpec{Name: "fl", LibDirs: []string{libs, filepath.Join(libs, "plib2.pretty")},
		Parts: []sbPart{
			{Ref: "C1", LCSC: "C99902", Value: "100nF", Footprint: "plib:FP_A"},
			{Ref: "R1", Value: "1k", Symbol: "Device:R", Footprint: "plib:FP_A"},
			{Ref: "U1", Value: "X", Footprint: "plib2:FP_B", Pins: []sbPin{{Number: "1", Name: "A", Type: "passive"}, {Number: "2", Name: "B", Type: "passive"}}},
		},
		Nets: []sbNet{
			{Name: "+3V3", Kind: "power", Pins: []string{"C1:1", "R1:1", "U1:1"}},
			{Name: "GND", Kind: "ground", Pins: []string{"C1:2", "R1:2", "U1:2"}},
		}}
	opt := sbOptions{OutDir: out, NoIntent: true, Jobs: 2, PlannerBudget: 20000}
	res, err := runSchBuild(spec, opt, nil)
	if err != nil || !res.OK {
		t.Fatalf("build: %v %+v", err, res)
	}
	check := func(what string) {
		t.Helper()
		tbl, _ := os.ReadFile(filepath.Join(out, "fp-lib-table"))
		for _, want := range []string{`(name "plib")`, `"${KIPRJMOD}/plib.pretty"`, `(name "plib2")`, `"${KIPRJMOD}/plib2.pretty"`} {
			if !strings.Contains(string(tbl), want) {
				t.Errorf("%s: fp-lib-table lacks %s:\n%s", what, want, tbl)
			}
		}
		for _, f := range []string{"plib.pretty/FP_A.kicad_mod", "plib2.pretty/FP_B.kicad_mod"} {
			if !fileExists(filepath.Join(out, filepath.FromSlash(f))) {
				t.Errorf("%s: %s not shipped", what, f)
			}
		}
	}
	check("build")
	if er, err := runSchEdit(opt, []sbOp{{Op: "set_value", Ref: "R1", Value: "2k2"}}); err != nil || !er.OK {
		t.Fatalf("set_value: %v %+v", err, er)
	}
	check("after sch-edit")

	if kicad.StockFootprintDir() == "" {
		t.Skip("stock footprints not installed: an unknown library cannot be told from a stock one")
	}
	bad := *spec
	bad.Parts = append([]sbPart{}, spec.Parts...)
	bad.Parts[1].Footprint = "nolib_xyz:FP_A"
	bad.Parts[2].Footprint = "plib2:FP_MISSING"
	_, err = runSchBuild(&bad, sbOptions{OutDir: filepath.Join(t.TempDir(), "bad"), NoIntent: true, Jobs: 2}, nil)
	if err == nil || !strings.Contains(err.Error(), "FOOTPRINT_LIB_NOT_FOUND") || !strings.Contains(err.Error(), `"nolib_xyz"`) ||
		!strings.Contains(err.Error(), "FOOTPRINT_NOT_FOUND") {
		t.Errorf("unknown footprint library / missing footprint not reported: %v", err)
	}
}

// sbFakeExternalFP points the footprint check at a fake stock install
// (stock = "" for none) and a KiCad settings home holding table (""
// for no global fp-lib-table at all).
func sbFakeExternalFP(t *testing.T, stock, table string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KICAD_CONFIG_HOME", home)
	if table != "" {
		d := filepath.Join(home, kicad.FPLibVersion)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "fp-lib-table"), []byte(table), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldStock, oldLoad := sbStockFootprintDir, sbLoadFPLibTables
	sbStockFootprintDir = func() string { return stock }
	t.Cleanup(func() { sbStockFootprintDir, sbLoadFPLibTables = oldStock, oldLoad })
}

func sbFPDiagCodes(parts []*sbRPart, notes *sbResolveStats) map[string]string {
	got := map[string]string{}
	for _, d := range sbResolveFootprintLibs(parts, nil, notes) {
		got[d.Code] += d.Message + "\n"
	}
	return got
}

// TestSbFootprintLibsExternal: a footprint outside libDirs is checked to
// its file, in a stock library and through the global fp-lib-table (path
// variables expanded) alike — a known nickname with an unknown footprint is
// FOOTPRINT_NOT_FOUND, never accepted. An existing global table, even an
// empty one, is an inspection: a library it lacks is FOOTPRINT_LIB_NOT_FOUND
// also without a stock install. Only when nothing could be inspected is it a
// note.
func TestSbFootprintLibsExternal(t *testing.T) {
	stock := t.TempDir()
	sbFakeFootprintLib(t, stock, "Stock_SMD", "R_0603")
	glib := t.TempDir()
	sbFakeFootprintLib(t, glib, "glib", "G1")
	sbFakeFootprintLib(t, glib, "offlib", "O1")
	t.Setenv("PCBPILOT_TEST_GLIB", glib)
	table := `(fp_lib_table (version 7)
  (lib (name "glib") (type "KiCad") (uri "${PCBPILOT_TEST_GLIB}/glib.pretty") (options "") (descr ""))
  (lib (name "offlib") (type "KiCad") (uri "${PCBPILOT_TEST_GLIB}/offlib.pretty") (options "") (descr "") (disabled))
)`
	parts := func(fps ...string) []*sbRPart {
		var out []*sbRPart
		for i, f := range fps {
			out = append(out, &sbRPart{sbPart: sbPart{Ref: fmt.Sprintf("R%d", i+1), Footprint: f}})
		}
		return out
	}

	sbFakeExternalFP(t, stock, table)
	var st sbResolveStats
	if got := sbFPDiagCodes(parts("Stock_SMD:R_0603", "glib:G1"), &st); len(got) != 0 {
		t.Errorf("stock and global footprints that exist: %v", got)
	}
	got := sbFPDiagCodes(parts("Stock_SMD:NO_SUCH", "glib:NO_SUCH", "offlib:O1", "nolib:X"), &st)
	if nf := got["FOOTPRINT_NOT_FOUND"]; !strings.Contains(nf, "Stock_SMD:NO_SUCH") || !strings.Contains(nf, "glib:NO_SUCH") {
		t.Errorf("missing footprint in a stock / global library not reported: %v", got)
	}
	if nl := got["FOOTPRINT_LIB_NOT_FOUND"]; !strings.Contains(nl, `"offlib"`) || !strings.Contains(nl, `"nolib"`) {
		t.Errorf("a disabled or unknown library must be FOOTPRINT_LIB_NOT_FOUND: %v", got)
	}

	// no stock install, an empty global table: still an inspection
	sbFakeExternalFP(t, "", "(fp_lib_table (version 7))")
	st = sbResolveStats{}
	if got := sbFPDiagCodes(parts("nolib:X"), &st); !strings.Contains(got["FOOTPRINT_LIB_NOT_FOUND"], `"nolib"`) || len(st.Notes) != 0 {
		t.Errorf("empty global table, no stock: want FOOTPRINT_LIB_NOT_FOUND, got %v notes %v", got, st.Notes)
	}

	// nothing to inspect (the tables cannot be loaded, no stock): a note
	sbFakeExternalFP(t, "", "")
	sbLoadFPLibTables = func(string) (*kicad.FPLibTables, error) { return nil, fmt.Errorf("unreadable") }
	st = sbResolveStats{}
	if got := sbFPDiagCodes(parts("nolib:X"), &st); len(got) != 0 || len(st.Notes) != 1 {
		t.Errorf("uninspectable: want one note, got %v notes %v", got, st.Notes)
	}
}
