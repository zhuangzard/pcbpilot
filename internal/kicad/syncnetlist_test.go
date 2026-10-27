package kicad

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Fixtures in testdata/pcbsync are real `kicad-cli sch export netlist`
// output (KiCad 10.0.6) from copies of KiCad's shipped demos, with the
// machine-specific <source> path and the export date replaced:
//   - ecc83-pp-flags.*: demos/ecc83 (U1 = 3-unit ECC83) with three symbols
//     edited before export: R1 (dnp yes), R2 (in_bom no), R4 (on_board no).
//   - complex_hierarchy.*.gz: demos/complex_hierarchy (ampli_ht.kicad_sch
//     used by two sheet instances), title block, libparts, libraries and
//     ki_* properties dropped, gzip-compressed.
func readNetFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "pcbsync", name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(name, ".gz") {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		if b, err = io.ReadAll(zr); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func parseNetFixture(t *testing.T, name, format string) *SyncNetlist {
	t.Helper()
	nl, err := ParseSyncNetlist(readNetFixture(t, name), format)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return nl
}

func TestSyncNetlistXMLAndSexprAgree(t *testing.T) {
	for _, base := range []string{"ecc83-pp-flags.%s.net", "complex_hierarchy.%s.net.gz"} {
		xmlName := strings.Replace(base, "%s", NetlistKicadXML, 1)
		sexName := strings.Replace(base, "%s", NetlistKicadSexpr, 1)
		x := parseNetFixture(t, xmlName, NetlistKicadXML)
		s := parseNetFixture(t, sexName, NetlistKicadSexpr)
		if len(x.Components) == 0 || len(x.Nets) == 0 {
			t.Fatalf("%s: empty parse", xmlName)
		}
		// kicadsexpr leaves exclude_from_board symbols out; kicadxml flags them.
		if !reflect.DeepEqual(s.OnBoard(), s) {
			t.Errorf("%s: kicadsexpr holds exclude_from_board symbols", sexName)
		}
		full := x
		x = x.OnBoard()
		if !reflect.DeepEqual(x, s) {
			for i := range x.Components {
				if i < len(s.Components) && !reflect.DeepEqual(x.Components[i], s.Components[i]) {
					t.Errorf("%s comp %d:\n xml   %+v\n sexpr %+v", base, i, x.Components[i], s.Components[i])
					break
				}
			}
			for i := range x.Nets {
				if i < len(s.Nets) && !reflect.DeepEqual(x.Nets[i], s.Nets[i]) {
					t.Errorf("%s net %d:\n xml   %+v\n sexpr %+v", base, i, x.Nets[i], s.Nets[i])
					break
				}
			}
			t.Errorf("%s: kicadxml and kicadsexpr parse differently (sheets %v vs %v)", base, x.Sheets, s.Sheets)
		}
		if base == "ecc83-pp-flags.%s.net" && (len(full.Components) != len(x.Components)+1 || full.Component("R4") == nil) {
			t.Errorf("ecc83 kicadxml must still list the exclude_from_board R4")
		}
		x = full
		// Format auto-detection gives the same result.
		if a, err := ParseSyncNetlist(readNetFixture(t, sexName), ""); err != nil || !reflect.DeepEqual(a, s) {
			t.Errorf("%s: auto-detect differs (%v)", sexName, err)
		}
		if a, err := ParseSyncNetlist(readNetFixture(t, xmlName), ""); err != nil || !reflect.DeepEqual(a, x) {
			t.Errorf("%s: auto-detect differs (%v)", xmlName, err)
		}
	}
}

func TestSyncNetlistMultiUnitAndFlags(t *testing.T) {
	for _, format := range []string{NetlistKicadXML, NetlistKicadSexpr} {
		nl := parseNetFixture(t, "ecc83-pp-flags."+format+".net", format)
		if nl.Tool != "Eeschema 10.0.6" || nl.Source != "ecc83-pp.kicad_sch" {
			t.Errorf("%s design: %q %q", format, nl.Tool, nl.Source)
		}
		wantComps := map[string]int{NetlistKicadXML: 15, NetlistKicadSexpr: 14}[format] // sexpr omits R4
		if len(nl.Components) != wantComps || len(nl.Sheets) != 1 || nl.Sheets[0].Tstamps != "/" {
			t.Errorf("%s: %d comps, sheets %+v", format, len(nl.Components), nl.Sheets)
		}
		u1 := nl.Component("U1")
		if u1 == nil {
			t.Fatalf("%s: no U1", format)
		}
		// The board's U1 footprint carries the first unit UUID (ecc83-pp.kicad_pcb).
		wantKeys := []string{"/00000000-0000-0000-0000-000048b4f266", "/00000000-0000-0000-0000-000048b4f263",
			"/00000000-0000-0000-0000-000048b4f256"}
		if !reflect.DeepEqual(u1.Keys(), wantKeys) {
			t.Errorf("%s U1 keys %v, want %v", format, u1.Keys(), wantKeys)
		}
		wantUnits := []SyncUnit{{"A", []string{"7", "8", "6"}}, {"B", []string{"2", "3", "1"}}, {"C", []string{"4", "9", "5"}}}
		if !reflect.DeepEqual(u1.Units, wantUnits) {
			t.Errorf("%s U1 units %+v", format, u1.Units)
		}
		if u1.Footprint != "Footprints:Valve_ECC-83-1" || u1.Value != "ECC83" || u1.LibID() != "ecc83-pp:ECC83" ||
			u1.Fields["Footprint"] != u1.Footprint || u1.Sheetname() != "ecc83-pp" || u1.Sheetfile() != "ecc83-pp.kicad_sch" {
			t.Errorf("%s U1: %+v", format, u1)
		}
		flags := map[string][3]bool{ // dnp, exclude_from_board, exclude_from_bom
			"R1": {true, false, false}, "R2": {false, false, true}, "R4": {false, true, false}, "R3": {}, "C1": {}, "U1": {},
		}
		for ref, want := range flags {
			c := nl.Component(ref)
			if c == nil && ref == "R4" && format == NetlistKicadSexpr {
				continue
			}
			if got := [3]bool{c.DNP, c.ExcludeFromBoard, c.ExcludeFromBOM}; got != want {
				t.Errorf("%s %s flags %v, want %v", format, ref, got, want)
			}
		}
		var gnd *SyncNet
		for i := range nl.Nets {
			if nl.Nets[i].Name == "GND" {
				gnd = &nl.Nets[i]
			}
		}
		if gnd == nil || gnd.Code != "1" || gnd.Class != "Default" || len(gnd.Nodes) != map[string]int{NetlistKicadXML: 7, NetlistKicadSexpr: 6}[format] ||
			gnd.Nodes[1] != (SyncNode{"P1", "1", "P1_1", "passive"}) || gnd.Nodes[0] != (SyncNode{"C1", "2", "", "passive"}) {
			t.Errorf("%s GND: %+v", format, gnd)
		}
	}
}

func TestSyncNetlistMultiInstanceSheets(t *testing.T) {
	nl := parseNetFixture(t, "complex_hierarchy.kicadxml.net.gz", NetlistKicadXML)
	if len(nl.Components) != 68 || len(nl.Sheets) != 3 {
		t.Fatalf("%d comps, %d sheets", len(nl.Components), len(nl.Sheets))
	}
	keys := map[string]string{}
	bySym := map[string][]SyncComp{}
	for _, c := range nl.Components {
		if len(c.Tstamps) == 0 {
			t.Fatalf("%s: no tstamps", c.Ref)
		}
		for _, k := range c.Keys() {
			if prev, dup := keys[k]; dup {
				t.Errorf("key %s shared by %s and %s", k, prev, c.Ref)
			}
			keys[k] = c.Ref
		}
		bySym[c.Tstamps[0]] = append(bySym[c.Tstamps[0]], c)
	}
	// The two instances of ampli_ht.kicad_sch: same symbol UUID, distinct
	// sheet paths, refs and keys (board paths /<sheet>/<sym>).
	pairs := 0
	for _, cs := range bySym {
		if len(cs) == 2 {
			pairs++
			if cs[0].SheetTstamps == cs[1].SheetTstamps || cs[0].Ref == cs[1].Ref || cs[0].Sheetfile() != "ampli_ht.kicad_sch" {
				t.Errorf("instance pair %s/%s: %q %q", cs[0].Ref, cs[1].Ref, cs[0].SheetTstamps, cs[1].SheetTstamps)
			}
		}
	}
	if pairs == 0 {
		t.Error("no symbol shared by the two sheet instances")
	}
	if _, ok := keys["/00000000-0000-0000-0000-00004b3a1333/00000000-0000-0000-0000-00004b3a1357"]; !ok {
		t.Error("missing key of a footprint path in complex_hierarchy.kicad_pcb")
	}
	// U201/U301: a 2-unit symbol inside each sheet instance; the board
	// footprint path is the sheet path + the FIRST unit UUID.
	for ref, want := range map[string]string{
		"U201": "/00000000-0000-0000-0000-00004b3a1333/00000000-0000-0000-0000-00004b3a1368",
		"U301": "/00000000-0000-0000-0000-00004b3a13a4/00000000-0000-0000-0000-00004b3a1368",
	} {
		if c := nl.Component(ref); c == nil || len(c.Keys()) != 2 || c.Keys()[0] != want {
			t.Errorf("%s keys: %+v, want first %s", ref, c, want)
		}
	}
	classes := map[string]string{}
	for _, n := range nl.Nets {
		classes[n.Name] = n.Class
	}
	if classes["-VAA"] != "power" || classes["+12V"] != "Default" {
		t.Errorf("net classes: -VAA %q +12V %q", classes["-VAA"], classes["+12V"])
	}
}

// SyncNetlist is a superset of ParseSchNetlist: same components and nets.
func TestSyncNetlistMatchesSchNetlist(t *testing.T) {
	data := readNetFixture(t, "ecc83-pp-flags.kicadxml.net")
	old, err := ParseSchNetlist(data)
	if err != nil {
		t.Fatal(err)
	}
	nl, err := ParseSyncNetlist(data, NetlistKicadXML)
	if err != nil {
		t.Fatal(err)
	}
	if len(old.Components) != len(nl.Components) || len(old.Nets) != len(nl.Nets) {
		t.Fatalf("counts differ")
	}
	for i, c := range old.Components {
		n := nl.Components[i]
		if c.Ref != n.Ref || c.Value != n.Value || c.Footprint != n.Footprint || c.Sheet != n.SheetNames {
			t.Errorf("comp %d: %+v vs %+v", i, c, n)
		}
	}
	for i, n := range old.Nets {
		if n.Name != nl.Nets[i].Name || len(n.Nodes) != len(nl.Nets[i].Nodes) {
			t.Errorf("net %d: %s vs %s", i, n.Name, nl.Nets[i].Name)
		}
	}
}

func TestSyncNetlistBadInput(t *testing.T) {
	for _, c := range []struct{ data, format string }{
		{"<export><components>", NetlistKicadXML},
		{"(export (components", NetlistKicadSexpr},
		{"(fp_lib_table)", NetlistKicadSexpr},
		{"(export)", "orcadpcb2"},
	} {
		if _, err := ParseSyncNetlist([]byte(c.data), c.format); err == nil {
			t.Errorf("%q as %s: want error", c.data, c.format)
		}
	}
	// Optional sections absent: empty, not a panic.
	nl, err := ParseSyncNetlist([]byte(`(export (version "E") (components (comp (ref "R1"))))`), "")
	if err != nil || len(nl.Components) != 1 || nl.Components[0].Ref != "R1" || len(nl.Nets) != 0 {
		t.Errorf("minimal sexpr: %+v %v", nl, err)
	}
}
