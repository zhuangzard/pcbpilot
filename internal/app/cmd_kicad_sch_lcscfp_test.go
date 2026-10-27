package app

import (
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
