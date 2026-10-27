package app

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// A dual op-amp with a separate power unit (units A, B, C), and a derived
// symbol of it the way KiCad's stock libraries ship LM358 (extends LM2904).
const testDualLib = `(kicad_symbol_lib (version 20251024) (generator "test")
	(symbol "OPA2"
		(exclude_from_sim no) (in_bom yes) (on_board yes)
		(property "Reference" "U" (at 0 5.08 0) (effects (font (size 1.27 1.27))))
		(property "Value" "OPA2" (at 0 -5.08 0) (effects (font (size 1.27 1.27))))
		(symbol "OPA2_1_1" (rectangle (start -2.54 2.54) (end 2.54 -2.54) (stroke (width 0.254) (type default)) (fill (type none)))
			(pin input line (at -5.08 0 0) (length 2.54) (name "+" (effects (font (size 1.27 1.27)))) (number "3" (effects (font (size 1.27 1.27)))))
			(pin output line (at 5.08 0 180) (length 2.54) (name "~" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))
		(symbol "OPA2_2_1" (rectangle (start -2.54 2.54) (end 2.54 -2.54) (stroke (width 0.254) (type default)) (fill (type none)))
			(pin input line (at -5.08 0 0) (length 2.54) (name "+" (effects (font (size 1.27 1.27)))) (number "5" (effects (font (size 1.27 1.27)))))
			(pin output line (at 5.08 0 180) (length 2.54) (name "~" (effects (font (size 1.27 1.27)))) (number "7" (effects (font (size 1.27 1.27))))))
		(symbol "OPA2_3_1"
			(pin power_in line (at 0 5.08 270) (length 2.54) (name "V+" (effects (font (size 1.27 1.27)))) (number "8" (effects (font (size 1.27 1.27)))))
			(pin power_in line (at 0 -5.08 90) (length 2.54) (name "V-" (effects (font (size 1.27 1.27)))) (number "4" (effects (font (size 1.27 1.27))))))
	)
	(symbol "OPA2X" (extends "OPA2")
		(property "Reference" "U" (at 0 5.08 0) (effects (font (size 1.27 1.27))))
		(property "Value" "OPA2X" (at 0 -5.08 0) (effects (font (size 1.27 1.27)))))
)`

// TestSchKicadBackendMultiUnit drives sch place / modify / prim-delete
// --backend kicad on the units of one multi-unit symbol.
func TestSchKicadBackendMultiUnit(t *testing.T) {
	dir := t.TempDir()
	sheet := filepath.Join(dir, "t.kicad_sch")
	if err := os.WriteFile(sheet, []byte(kicad.NewSchematicText("A4", kicad.NewUUID(), true)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "my.kicad_sym"), []byte(testDualLib), 0o644); err != nil {
		t.Fatal(err)
	}
	k := []string{"--backend", "kicad", "--kicad-sch", sheet}
	run := func(fail bool, args ...string) string {
		t.Helper()
		var out, errb bytes.Buffer
		code := Run(append(append([]string{"sch"}, args...), k...), &out, &errb)
		if (code != 0) != fail {
			t.Fatalf("%v: exit %d: %s %s", args, code, out.String(), errb.String())
		}
		return out.String() + errb.String()
	}
	place := []string{"place", "--symbol", "my:OPA2X", "--designator", "U1"}
	run(false, append(place, "--value", "OPA2X", "--lcsc", "C7950", "--x", "50.8", "--y", "50.8")...)
	run(false, append(place, "--unit", "2", "--x", "101.6", "--y", "50.8")...)
	if out := run(false, append(place, "--unit", "3", "--x", "76.2", "--y", "76.2")...); !strings.Contains(out, `"units": [`) {
		t.Errorf("place does not report the units: %s", out)
	}
	if out := run(true, append(place, "--unit", "2", "--x", "0", "--y", "0")...); !strings.Contains(out, "unit 2 is already") {
		t.Errorf("duplicate unit: %s", out)
	}
	if out := run(true, append(place, "--unit", "4", "--x", "0", "--y", "0")...); !strings.Contains(out, "3 unit(s)") {
		t.Errorf("unit 4 of a 3-unit symbol: %s", out)
	}
	run(true, "place", "--symbol", "my:OPA2", "--designator", "U1", "--unit", "1", "--x", "0", "--y", "0")
	src, _ := os.ReadFile(sheet)
	if n := strings.Count(string(src), `(property "LCSC" "C7950"`); n != 3 {
		t.Errorf("LCSC on %d units, want 3 (inherited)", n)
	}
	// pins of unit 2 and of the power unit answer to U1
	run(false, "netflag", "--kind", "power", "--net", "VCC", "--at", "U1.8")
	run(false, "wire", "--through", "U1.1", "--through", "U1.5")

	run(false, "modify", "--id", "U1", "--designator", "U9", "--patch", `{"name":"OPA2Y","supplierId":"C7951"}`)
	if out := run(true, "modify", "--id", "U9", "--x", "120"); !strings.Contains(out, "U9:1, U9:2, U9:3") {
		t.Errorf("ambiguous move: %s", out)
	}
	run(false, "modify", "--id", "U9:3", "--x", "127") // the power unit; wires are not dragged
	src, _ = os.ReadFile(sheet)
	s := string(src)
	for frag, n := range map[string]int{`(reference "U9")`: 3, `(property "Value" "OPA2Y"`: 3, `(property "LCSC" "C7951"`: 3, `(reference "U1")`: 0} {
		if c := strings.Count(s, frag); c != n {
			t.Errorf("%s ×%d, want %d", frag, c, n)
		}
	}
	if !regexp.MustCompile(`\(at 127 76\.2 0\)\s*\(unit 3\)`).MatchString(s) {
		t.Error("U9:3 not moved")
	}
	// refused edits leave the file untouched: an unplaced unit, a rename by
	// Reference field onto a placed part or to an invalid designator
	run(false, "place", "--symbol", "my:OPA2X", "--designator", "U2", "--x", "152.4", "--y", "50.8")
	src, _ = os.ReadFile(sheet)
	for _, bad := range [][]string{
		{"modify", "--id", "U9:99", "--patch", `{"name":"X"}`},
		{"modify", "--id", "U9", "--patch", `{"customAttributes":{"Reference":"U2"}}`},
		{"modify", "--id", "U9", "--patch", `{"otherProperty":{"Reference":"U9:1"}}`},
		{"modify", "--id", "U9", "--designator", "U 10"},
		{"modify", "--id", "U9", "--designator", "U10", "--patch", `{"customAttributes":{"Reference":"U11"}}`},
		{"place", "--symbol", "my:OPA2X", "--designator", "U3:2", "--x", "0", "--y", "0"},
	} {
		run(true, bad...)
		if now, _ := os.ReadFile(sheet); !bytes.Equal(now, src) {
			t.Fatalf("refused %v changed the sheet", bad)
		}
	}
	// a new unit never gets a field its placed units lack or disagree on
	run(false, "place", "--symbol", "my:OPA2", "--designator", "U4", "--value", "OPA2", "--x", "152.4", "--y", "101.6")
	src, _ = os.ReadFile(sheet)
	for _, bad := range [][]string{
		{"--lcsc", "C1"}, // absent on unit 1
		{"--footprint", "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm"}, // empty on unit 1
		{"--value", "TL072"}, // differs
	} {
		run(true, append([]string{"place", "--symbol", "my:OPA2", "--designator", "U4", "--unit", "2", "--x", "177.8", "--y", "101.6"}, bad...)...)
		if now, _ := os.ReadFile(sheet); !bytes.Equal(now, src) {
			t.Fatalf("refused place %v changed the sheet", bad)
		}
	}
	run(false, "place", "--symbol", "my:OPA2", "--designator", "U4", "--unit", "2", "--value", "OPA2", "--x", "177.8", "--y", "101.6")
	run(false, "prim-delete", "--ids", "U2,U4:1,U4")

	run(false, "prim-delete", "--ids", "U9:3")
	e, _ := kicad.OpenSchematicFile(sheet)
	if u := e.SymbolUnits("U9"); len(u) != 2 {
		t.Fatalf("after deleting U9:3: units %v", u)
	}
	if _, err := kicad.KicadCLI(); err == nil {
		nl, err := kicad.ExportSchNetlist(sheet)
		if err != nil {
			t.Fatal(err)
		}
		pn := nl.PinNets()
		if pn["U9.1"] == "" || pn["U9.1"] != pn["U9.5"] {
			t.Errorf("unit 1 pin 1 / unit 2 pin 5 not joined: %v", pn)
		}
	}
	run(false, "prim-delete", "--ids", "U9")
	src, _ = os.ReadFile(sheet)
	if strings.Contains(string(src), `(lib_id "my:OPA2X")`) {
		t.Error("a unit of U9 survived the delete")
	}
}

// TestSchBuildMultiUnitLive builds a spec with KiCad's own derived
// multi-unit symbols (LM358: two op-amps + power unit; 74HC00: four gates +
// power unit): every unit is placed, the netlist gate passes, ERC sees no
// missing unit, sch-read reports the units and sch-edit set_value reaches
// every unit.
func TestSchBuildMultiUnitLive(t *testing.T) {
	sbNeedKicad(t)
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	out := filepath.Join(t.TempDir(), "mu")
	spec := &sbSpec{Name: "mu",
		Parts: []sbPart{
			{Ref: "U1", Symbol: "Amplifier_Operational:LM358", Footprint: "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm"},
			{Ref: "U2", Symbol: "74xx:74HC00", Footprint: "Package_SO:SOIC-14_3.9x8.7mm_P1.27mm"},
			{Ref: "R1", Value: "10k", Symbol: "Device:R"},
			{Ref: "R2", Value: "10k", Symbol: "Device:R"},
		},
		Nets: []sbNet{
			{Name: "VCC", Kind: "power", Pins: []string{"U1:8", "U2:14", "R1:1"}},
			{Name: "GND", Kind: "ground", Pins: []string{"U1:4", "U2:7", "R2:2"}},
			{Name: "IN_A", Pins: []string{"U1:3", "R1:2"}},
			{Name: "FB_A", Pins: []string{"U1:1", "U1:2"}},
			{Name: "IN_B", Pins: []string{"U1:5", "R2:1"}},
			{Name: "OUT_B", Pins: []string{"U1:7", "U1:6", "U2:9", "U2:10"}},
			{Name: "NAND3", Pins: []string{"U2:8", "U2:1", "U2:2"}},
		}}
	res, err := runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, Jobs: 4, PlannerBudget: 20000}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if s := sbGateStatus(res, "netlist"); s != "pass" {
		t.Fatalf("netlist gate %s: %+v", s, res.Gates)
	}
	sch, _ := os.ReadFile(filepath.Join(out, "mu.kicad_sch"))
	for lib, want := range map[string]int{`(lib_id "Amplifier_Operational:LM358")`: 3, `(lib_id "74xx:74HC00")`: 5} {
		if n := strings.Count(string(sch), lib); n != want {
			t.Errorf("%s placed %d times, want %d", lib, n, want)
		}
	}
	erc, _ := os.ReadFile(filepath.Join(out, "erc.json"))
	for _, bad := range []string{"missing_unit", "missing_power_pin", "missing_input_pin"} {
		if bytes.Contains(erc, []byte(bad)) {
			t.Errorf("ERC reports %s", bad)
		}
	}
	rd, err := readSbSpec(out, "")
	if err != nil {
		t.Fatal(err)
	}
	units := map[string]int{}
	for _, p := range rd.Parts {
		units[p.Ref] = len(p.Units)
	}
	if units["U1"] != 0 || units["U2"] != 0 || units["R1"] != 0 { // fully placed: no units list
		t.Errorf("sch-read units %v", units)
	}
	er, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, Jobs: 4, PlannerBudget: 20000},
		[]sbOp{{Op: "set_value", Ref: "U1", Value: "LM358B"}, {Op: "set_field", Ref: "U1", Name: "MPN", Value: "LM358BIDR"}})
	if err != nil || sbGateStatus(er, "netlist") != "pass" {
		t.Fatalf("edit: %v %+v", err, er)
	}
	sch, _ = os.ReadFile(filepath.Join(out, "mu.kicad_sch"))
	for frag, n := range map[string]int{`(property "Value" "LM358B"`: 3, `(property "MPN" "LM358BIDR"`: 3} {
		if c := strings.Count(string(sch), frag); c != n {
			t.Errorf("%s on %d units, want %d", frag, c, n)
		}
	}
}

// A triple gate whose supply pins (7, 14) are common to every unit (unit 0).
// They are passive: KiCad's ERC reports common power_in pins of an unplaced
// unit as missing_power_pin (an error), so the ERC gate refuses a partly
// placed part of that kind — correctly.
const testTripleLib = `(kicad_symbol_lib (version 20251024) (generator "test")
	(symbol "TRI"
		(exclude_from_sim no) (in_bom yes) (on_board yes)
		(property "Reference" "U" (at 0 7.62 0) (effects (font (size 1.27 1.27))))
		(property "Value" "TRI" (at 0 -7.62 0) (effects (font (size 1.27 1.27))))
		(symbol "TRI_0_1"
			(pin passive line (at 0 7.62 270) (length 2.54) (name "VCC" (effects (font (size 1.27 1.27)))) (number "14" (effects (font (size 1.27 1.27)))))
			(pin passive line (at 0 -7.62 90) (length 2.54) (name "GND" (effects (font (size 1.27 1.27)))) (number "7" (effects (font (size 1.27 1.27))))))
		(symbol "TRI_1_1" (rectangle (start -2.54 2.54) (end 2.54 -2.54) (stroke (width 0.254) (type default)) (fill (type none)))
			(pin input line (at -5.08 0 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
			(pin output line (at 5.08 0 180) (length 2.54) (name "Y" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27))))))
		(symbol "TRI_2_1" (rectangle (start -2.54 2.54) (end 2.54 -2.54) (stroke (width 0.254) (type default)) (fill (type none)))
			(pin input line (at -5.08 0 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "3" (effects (font (size 1.27 1.27)))))
			(pin output line (at 5.08 0 180) (length 2.54) (name "Y" (effects (font (size 1.27 1.27)))) (number "4" (effects (font (size 1.27 1.27))))))
		(symbol "TRI_3_1" (rectangle (start -2.54 2.54) (end 2.54 -2.54) (stroke (width 0.254) (type default)) (fill (type none)))
			(pin input line (at -5.08 0 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "5" (effects (font (size 1.27 1.27)))))
			(pin output line (at 5.08 0 180) (length 2.54) (name "Y" (effects (font (size 1.27 1.27)))) (number "6" (effects (font (size 1.27 1.27))))))
	)
)`

// TestSchBuildPartialUnitsLive: a spec units list places only those units;
// sch-read of the partly placed part writes the list back and a rebuild from
// it places the same units, common (unit 0) supply pins still connected.
func TestSchBuildPartialUnitsLive(t *testing.T) {
	sbNeedKicad(t)
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	libDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(libDir, "my.kicad_sym"), []byte(testTripleLib), 0o644); err != nil {
		t.Fatal(err)
	}
	spec := func(units []int, extra ...string) *sbSpec {
		return &sbSpec{Name: "pu", LibDirs: []string{libDir},
			Parts: []sbPart{{Ref: "U1", Symbol: "my:TRI", Units: units}, {Ref: "R1", Value: "1k", Symbol: "Device:R"}},
			Nets: []sbNet{
				{Name: "VCC", Kind: "power", Pins: []string{"U1:14", "R1:1"}},
				{Name: "GND", Kind: "ground", Pins: []string{"U1:7"}},
				{Name: "SIG", Pins: append([]string{"U1:2", "U1:5", "R1:2"}, extra...)},
			}}
	}
	if _, err := runSchBuild(spec([]int{1, 4}), sbOptions{OutDir: filepath.Join(t.TempDir(), "bad"), NoIntent: true, Jobs: 2}, nil); err == nil || !strings.Contains(err.Error(), "UNIT_NOT_ON_SYMBOL") {
		t.Errorf("unit 4 of a 3-unit symbol: %v", err)
	}
	if _, err := runSchBuild(spec([]int{1, 3}, "U1:3"), sbOptions{OutDir: filepath.Join(t.TempDir(), "bad2"), NoIntent: true, Jobs: 2}, nil); err == nil {
		t.Error("a pin of an unplaced unit accepted")
	}
	out := filepath.Join(t.TempDir(), "pu")
	check := func(dir, what string, res *sbReport, err error) {
		t.Helper()
		if err != nil || sbGateStatus(res, "netlist") != "pass" {
			t.Fatalf("%s: %v %+v", what, err, res)
		}
		sch, rerr := os.ReadFile(filepath.Join(dir, "pu.kicad_sch"))
		if rerr != nil {
			t.Fatalf("%s: %v (ok=%v %+v)", what, rerr, res.OK, res.Error)
		}
		if n := strings.Count(string(sch), `(lib_id "my:TRI")`); n != 2 {
			t.Errorf("%s: %d units of U1 placed, want 2", what, n)
		}
		e, _ := kicad.OpenSchematicFile(filepath.Join(dir, "pu.kicad_sch"))
		if u := e.SymbolUnits("U1"); len(u) != 2 || u[0] != 1 || u[1] != 3 {
			t.Errorf("%s: units %v", what, u)
		}
	}
	res, err := runSchBuild(spec([]int{1, 3}), sbOptions{OutDir: out, NoIntent: true, Jobs: 2, PlannerBudget: 20000}, nil)
	check(out, "build", res, err)
	rd, err := readSbSpec(out, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rd.Parts {
		if p.Ref == "U1" && (len(p.Units) != 2 || p.Units[0] != 1 || p.Units[1] != 3) {
			t.Errorf("sch-read units %v, want [1 3]", p.Units)
		}
	}
	pinNet := map[string]string{}
	for _, n := range rd.Nets {
		for _, p := range n.Pins {
			pinNet[p] = n.Name
		}
	}
	if pinNet["U1:14"] != "VCC" || pinNet["U1:7"] != "GND" || pinNet["U1:5"] != "SIG" {
		t.Errorf("sch-read nets %v", pinNet)
	}
	for _, p := range append(rd.NoConnect, "") {
		if p == "U1:3" || p == "U1:4" {
			t.Errorf("pin %s of the unplaced unit 2 read back", p)
		}
	}
	copyDir := filepath.Join(t.TempDir(), "copy")
	rd.Name = "pu"
	res2, err := runSchBuild(rd, sbOptions{OutDir: copyDir, NoIntent: true, Jobs: 2, PlannerBudget: 20000}, nil)
	check(copyDir, "rebuild from sch-read", res2, err)
}

// TestSchReadReusedSheetPartialUnits: on KiCad's complex_hierarchy demo the
// sheet ampli_ht.kicad_sch is used twice (U201 and U301 are one LM358N
// symbol). With its unit 2 deleted, sch-read gives both references the units
// list [1], and a build from that spec places unit 1 only, for both.
func TestSchReadReusedSheetPartialUnits(t *testing.T) {
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip("kicad-cli not installed")
	}
	sd := kicad.StockSymbolDir()
	src := filepath.Join(sd, "..", "demos", "complex_hierarchy")
	if sd == "" || !fileExists(filepath.Join(src, "complex_hierarchy.kicad_pro")) {
		t.Skip("KiCad complex_hierarchy demo not installed")
	}
	dir := t.TempDir()
	files, _ := filepath.Glob(filepath.Join(src, "*"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			_ = os.WriteFile(filepath.Join(dir, filepath.Base(f)), b, 0o644)
		}
	}
	ampli := filepath.Join(dir, "ampli_ht.kicad_sch")
	e, err := kicad.OpenSchematicFile(ampli)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.DeleteSymbol("U201:2"); err != nil {
		t.Fatal(err)
	}
	text, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(ampli, []byte(text), 0o644)
	rd, err := readSbSpec(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]int{}
	for _, p := range rd.Parts {
		got[p.Ref] = p.Units
	}
	for _, ref := range []string{"U201", "U301"} {
		if u := got[ref]; len(u) != 1 || u[0] != 1 {
			t.Errorf("sch-read %s units %v, want [1]", ref, u)
		}
	}
	d, _, err := buildDesign(rd, sbResolveOpts{OutDir: t.TempDir(), Offline: true, Jobs: 2})
	if err != nil {
		t.Fatalf("design from sch-read: %v", err)
	}
	for _, ref := range []string{"U201", "U301"} {
		if p := d.ByRef[ref]; p == nil || len(sbUnits(p)) != 1 || sbUnits(p)[0] != 1 {
			t.Errorf("rebuild would place %s units %v, want [1]", ref, sbUnits(p))
		}
	}
}
