package kicad

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
		// Required sections missing.
		{"<export/>", NetlistKicadXML},
		{"<export><components/></export>", NetlistKicadXML},
		{"<export><nets/></export>", NetlistKicadXML},
		{"(export)", NetlistKicadSexpr},
		{`(export (version "E") (components (comp (ref "R1"))))`, NetlistKicadSexpr},
		{"(export (nets))", NetlistKicadSexpr},
	} {
		if _, err := ParseSyncNetlist([]byte(c.data), c.format); err == nil {
			t.Errorf("%q as %s: want error", c.data, c.format)
		}
	}
	// Empty component and net lists are a valid (empty) design.
	for _, data := range []string{"<export><components/><nets/></export>", "(export (components) (nets))"} {
		nl, err := ParseSyncNetlist([]byte(data), "")
		if err != nil || len(nl.Components) != 0 || len(nl.Nets) != 0 || len(nl.Validate()) != 0 {
			t.Errorf("%q: %+v %v %v", data, nl, err, nl.Validate())
		}
	}
	// Optional parts of a component absent: parsed, not a panic.
	nl, err := ParseSyncNetlist([]byte(`(export (version "E") (components (comp (ref "R1"))) (nets))`), "")
	if err != nil || len(nl.Components) != 1 || nl.Components[0].Ref != "R1" || len(nl.Nets) != 0 {
		t.Errorf("minimal sexpr: %+v %v", nl, err)
	}
}

// replaceFirst replaces the first match of re in s.
func replaceFirst(t *testing.T, s, re, repl string) string {
	t.Helper()
	loc := regexp.MustCompile(re).FindStringIndex(s)
	if loc == nil {
		t.Fatalf("fixture edit: %s not found", re)
	}
	return s[:loc[0]] + repl + s[loc[1]:]
}

func TestSyncNetlistValidate(t *testing.T) {
	for _, name := range []string{"ecc83-pp-flags.kicadxml.net", "ecc83-pp-flags.kicadsexpr.net",
		"complex_hierarchy.kicadxml.net.gz", "complex_hierarchy.kicadsexpr.net.gz"} {
		if issues := parseNetFixture(t, name, "").Validate(); len(issues) != 0 {
			t.Errorf("%s: real export must validate, got %+v", name, issues)
		}
	}
	// Per format: regexps locating C2's ref, C2's UUID, C1's sheetpath and
	// C2's tstamps element (C1, C2 are the first two components).
	type edits struct{ ref, c2uuid, c1sheet, c2tstamps string }
	forms := map[string]edits{
		"ecc83-pp-flags.kicadxml.net": {`ref="C2"`, `<tstamps>00000000-0000-0000-0000-00004549f3be</tstamps>`,
			`<sheetpath names="/" tstamps="/"/>`, `<tstamps>00000000-0000-0000-0000-00004549f3be</tstamps>`},
		"ecc83-pp-flags.kicadsexpr.net": {`\(ref "C2"\)`, `\(tstamps "00000000-0000-0000-0000-00004549f3be"\)`,
			`\(sheetpath\s*\(names "/"\)\s*\(tstamps "/"\)\s*\)`, `\(tstamps "00000000-0000-0000-0000-00004549f3be"\)`},
	}
	for name, e := range forms {
		base := string(readNetFixture(t, name))
		xmlForm := strings.HasSuffix(name, "kicadxml.net")
		pick := func(xmlText, sexText string) string {
			if xmlForm {
				return xmlText
			}
			return sexText
		}
		cases := []struct {
			what, text, code, ref string
		}{
			{"duplicate ref", replaceFirst(t, base, e.ref, pick(`ref="C1"`, `(ref "C1")`)), ErrDupRef, "C1"},
			{"unannotated", replaceFirst(t, base, e.ref, pick(`ref="C?"`, `(ref "C?")`)), ErrUnannotated, "C?"},
			{"empty ref", replaceFirst(t, base, e.ref, pick(`ref=""`, `(ref "")`)), ErrNetlistInvalid, ""},
			{"duplicate key", replaceFirst(t, base, e.c2uuid,
				pick(`<tstamps>00000000-0000-0000-0000-00004549f4be</tstamps>`, `(tstamps "00000000-0000-0000-0000-00004549f4be")`)), ErrDupKey, "C2"},
			{"no sheet path", replaceFirst(t, base, e.c1sheet, ""), ErrNetlistInvalid, "C1"},
			{"no UUID", replaceFirst(t, base, e.c2tstamps, ""), ErrNetlistInvalid, "C2"},
		}
		for _, c := range cases {
			nl, err := ParseSyncNetlist([]byte(c.text), "")
			if err != nil {
				t.Fatalf("%s %s: %v", name, c.what, err)
			}
			found := false
			for _, is := range nl.Validate() {
				found = found || (is.Code == c.code && is.Ref == c.ref)
			}
			if !found {
				t.Errorf("%s %s: want %s on %q, got %+v", name, c.what, c.code, c.ref, nl.Validate())
			}
			if c.what == "duplicate ref" && nl.Component("C1") != nil {
				t.Errorf("%s: Component must not pick one of two C1s", name)
			}
		}
	}
}

// TestSyncNetlistValidateIdentityAndNets pins the UUID, sheet-path and net
// rules on a small hand-built netlist.
func TestSyncNetlistValidateIdentityAndNets(t *testing.T) {
	const u1, u2, sh = "00000000-0000-0000-0000-00004549f4be", "4d7b2a6e-31c2-4f0a-9d1e-5b7c8a9e0f12", "00000000-0000-0000-0000-00004b3a1333"
	base := func() *SyncNetlist {
		return &SyncNetlist{
			Components: []SyncComp{
				{Ref: "R1", SheetTstamps: "/", Tstamps: []string{u1}},
				{Ref: "R2", SheetTstamps: "/" + sh + "/", Tstamps: []string{u2}},
			},
			Nets: []SyncNet{
				{Code: "1", Name: "A", Nodes: []SyncNode{{Ref: "R1", Pin: "1"}, {Ref: "R2", Pin: "1"}}},
				{Code: "2", Name: "B", Nodes: []SyncNode{{Ref: "R1", Pin: "2"}, {Ref: "R2", Pin: "2"}}},
			},
		}
	}
	if issues := base().Validate(); len(issues) != 0 {
		t.Fatalf("base must validate: %+v", issues)
	}
	cases := []struct {
		what   string
		edit   func(*SyncNetlist)
		detail string
	}{
		{"garbage sheet path", func(n *SyncNetlist) { n.Components[1].SheetTstamps = "/garbage/" }, "sheet path tstamps"},
		{"sheet path without trailing slash", func(n *SyncNetlist) { n.Components[1].SheetTstamps = "/" + sh }, "sheet path tstamps"},
		{"empty sheet segment", func(n *SyncNetlist) { n.Components[1].SheetTstamps = "/" + sh + "//" }, "sheet path tstamps"},
		{"not a UUID", func(n *SyncNetlist) { n.Components[0].Tstamps = []string{"not-a-uuid"} }, "not a KiCad UUID"},
		{"empty UUID", func(n *SyncNetlist) { n.Components[0].Tstamps = []string{""} }, "not a KiCad UUID"},
		{"upper-case UUID", func(n *SyncNetlist) { n.Components[0].Tstamps = []string{strings.ToUpper(u1)} }, "not a KiCad UUID"},
		{"bare 8-hex timestamp", func(n *SyncNetlist) { n.Components[0].Tstamps = []string{"4549F4BE"} }, "not a KiCad UUID"},
		{"second unit bad", func(n *SyncNetlist) { n.Components[0].Tstamps = []string{u1, "x"} }, "not a KiCad UUID"},
		{"empty net code", func(n *SyncNetlist) { n.Nets[1].Code = "" }, "has no code"},
		{"duplicate net code", func(n *SyncNetlist) { n.Nets[1].Code = "1" }, "net code 1 is used by both"},
		{"empty net name", func(n *SyncNetlist) { n.Nets[1].Name = "" }, "has no name"},
		{"node without pin", func(n *SyncNetlist) { n.Nets[1].Nodes[0].Pin = "" }, "without a pin"},
		{"pin on two nets", func(n *SyncNetlist) { n.Nets[1].Nodes[0].Pin = "1" }, "is on both net"},
		{"pin on two nets of the same name", func(n *SyncNetlist) {
			n.Nets[1].Name = "A" // codes 1 and 2 both named "A"
			n.Nets[1].Nodes[0].Pin = "1"
		}, `is on both net "A" (code 1) and net "A" (code 2)`},
	}
	for _, c := range cases {
		nl := base()
		c.edit(nl)
		var got []string
		ok := false
		for _, is := range nl.Validate() {
			got = append(got, is.Detail)
			ok = ok || (is.Code == ErrNetlistInvalid && strings.Contains(is.Detail, c.detail))
		}
		if !ok {
			t.Errorf("%s: want %s %q, got %v", c.what, ErrNetlistInvalid, c.detail, got)
		}
	}
	// Two entries with the same code and the same pin: only the
	// duplicate-code issue, no spurious pin conflict.
	nl0 := base()
	nl0.Nets[1].Code = "1"
	nl0.Nets[1].Nodes[0].Pin = "1"
	if issues := nl0.Validate(); len(issues) != 1 || !strings.Contains(issues[0].Detail, "net code 1 is used by both") {
		t.Errorf("same code + same pin: want only the duplicate-code issue, got %+v", issues)
	}
	// The same pin listed twice on one net is not a conflict.
	nl := base()
	nl.Nets[0].Nodes = append(nl.Nets[0].Nodes, SyncNode{Ref: "R1", Pin: "1"})
	if issues := nl.Validate(); len(issues) != 0 {
		t.Errorf("repeated node on one net: %+v", issues)
	}
}

// The identity and net rules fire on real exports of both formats.
func TestSyncNetlistValidateFormats(t *testing.T) {
	for _, name := range []string{"ecc83-pp-flags.kicadxml.net", "ecc83-pp-flags.kicadsexpr.net"} {
		base := string(readNetFixture(t, name))
		xmlForm := strings.HasSuffix(name, "kicadxml.net")
		pick := func(x, s string) string {
			if xmlForm {
				return x
			}
			return s
		}
		for _, c := range []struct{ what, re, repl, detail string }{
			{"bad UUID", "00000000-0000-0000-0000-00004549f3be", "not-a-uuid", "not a KiCad UUID"},
			{"bad sheet path", pick(`tstamps="/"/>`, `\(names "/"\)\s*\(tstamps "/"\)`), pick(`tstamps="/garbage/"/>`, `(names "/") (tstamps "/garbage/")`), "sheet path tstamps"},
			{"duplicate net code", pick(`code="2"`, `\(code "2"\)`), pick(`code="1"`, `(code "1")`), "net code 1 is used by both"},
			{"empty pin", pick(`pin="2"`, `\(pin "2"\)`), pick(`pin=""`, `(pin "")`), "without a pin"},
		} {
			nl, err := ParseSyncNetlist([]byte(replaceFirst(t, base, c.re, c.repl)), "")
			if err != nil {
				t.Fatalf("%s %s: %v", name, c.what, err)
			}
			ok := false
			for _, is := range nl.Validate() {
				ok = ok || strings.Contains(is.Detail, c.detail)
			}
			if !ok {
				t.Errorf("%s %s: want %q, got %+v", name, c.what, c.detail, nl.Validate())
			}
		}
	}
}
