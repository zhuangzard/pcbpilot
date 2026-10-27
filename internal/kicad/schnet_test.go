package kicad

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hierarchical kicadxml netlist as kicad-cli writes it (names unescaped):
// a root-sheet local label "VPP/MCLR", a sub-sheet local "VCC_PIC", two
// sub-sheets with the same local label "SDA", an unconnected pin whose pin
// name has a '/', and a pinless mounting hole.
const hierNetXML = `<?xml version="1.0" encoding="utf-8"?>
<export version="E">
  <design>
    <source>ROOT</source>
    <sheet number="1" name="/" tstamps="/"><title_block><source>root.kicad_sch</source></title_block></sheet>
    <sheet number="2" name="/pic_sockets/" tstamps="/a/"><title_block><source>sockets.kicad_sch</source></title_block></sheet>
    <sheet number="3" name="/i2c_a/" tstamps="/b/"><title_block><source>i2c.kicad_sch</source></title_block></sheet>
    <sheet number="4" name="/i2c_b/" tstamps="/c/"><title_block><source>i2c.kicad_sch</source></title_block></sheet>
  </design>
  <components>
    <comp ref="R1"><value>1k</value><libsource lib="Device" part="R"/><sheetpath names="/" tstamps="/"/></comp>
    <comp ref="U1"><value>X</value><libsource lib="X" part="U"/><sheetpath names="/pic_sockets/" tstamps="/a/"/></comp>
    <comp ref="R2"><value>1k</value><libsource lib="Device" part="R"/><sheetpath names="/i2c_a/" tstamps="/b/"/></comp>
    <comp ref="R3"><value>1k</value><libsource lib="Device" part="R"/><sheetpath names="/i2c_b/" tstamps="/c/"/></comp>
    <comp ref="H1"><value>MountingHole</value><libsource lib="Mechanical" part="MountingHole"/><sheetpath names="/" tstamps="/"/></comp>
  </components>
  <libparts>
    <libpart lib="Device" part="R"><pins><pin num="1" name="~" type="passive"/><pin num="2" name="~" type="passive"/></pins></libpart>
    <libpart lib="X" part="U"><pins><pin num="1" name="MCLR" type="input"/><pin num="2" name="VDD" type="power_in"/><pin num="4" name="S/S" type="input"/></pins></libpart>
  </libparts>
  <nets>
    <net code="1" name="/VPP/MCLR"><node ref="R1" pin="1"/><node ref="U1" pin="1"/></net>
    <net code="2" name="/pic_sockets/VCC_PIC"><node ref="U1" pin="2"/><node ref="R1" pin="2"/></net>
    <net code="3" name="/i2c_a/SDA"><node ref="R2" pin="1"/><node ref="R2" pin="2"/></net>
    <net code="4" name="/i2c_b/SDA"><node ref="R3" pin="1"/><node ref="R3" pin="2"/></net>
    <net code="5" name="unconnected-(U1-S/S-Pad4)"><node ref="U1" pin="4" pinfunction="S/S" pintype="input"/></net>
  </nets>
</export>`

// writeSheets writes sheet files holding the given s-expression lines and
// returns the XML with its root <source> pointing at them.
func writeSheets(t *testing.T, xml string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("(kicad_sch\n"+body+"\n)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return strings.Replace(xml, "ROOT", filepath.Join(dir, "root.kicad_sch"), 1)
}

func TestSchNetlistCanonicalNetIdentity(t *testing.T) {
	x := writeSheets(t, hierNetXML, map[string]string{
		"root.kicad_sch":    `(label "VPP{slash}MCLR" (at 0 0 0))`,
		"sockets.kicad_sch": `(hierarchical_label "VCC_PIC" (shape input))`,
		"i2c.kicad_sch":     `(label "SDA" (at 0 0 0))`,
	})
	nl, err := ParseSchNetlist([]byte(x))
	if err != nil {
		t.Fatal(err)
	}
	pn := nl.PinNets()
	for pin, want := range map[string]string{
		"U1.1": "/VPP{slash}MCLR", // as on the board: never "MCLR"
		"U1.2": "/pic_sockets/VCC_PIC",
		"R2.1": "/i2c_a/SDA",
		"U1.4": "unconnected-(U1-S{slash}S-Pad4)",
	} {
		if pn[pin] != want {
			t.Errorf("PinNets[%s] = %q, want %q", pin, pn[pin], want)
		}
	}
	doc, err := nl.ToConnectivityDoc("t")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, n := range doc.Nets {
		got[n.FullName] = n.Name
	}
	for full, name := range map[string]string{
		"/VPP{slash}MCLR":      "VPP/MCLR",
		"/pic_sockets/VCC_PIC": "VCC_PIC",
		"/i2c_a/SDA":           "/i2c_a/SDA", // ambiguous display name: the identity
		"/i2c_b/SDA":           "/i2c_b/SDA",
	} {
		if got[full] != name {
			t.Errorf("net %s: name %q, want %q (all: %v)", full, got[full], name, got)
		}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"pins":null`) {
		t.Errorf("a pinless component must carry \"pins\": [] (pad-net-diff iterates it): %s", b)
	}
	for _, c := range doc.Components {
		if c.Ref == "U1" {
			for _, p := range c.Pins {
				if p.Number == "4" && p.ConnectionState != "unconnected" {
					t.Errorf("U1.4 is an unconnected-(…) pin: %+v", p)
				}
			}
		}
	}
}

// Two different nets that kicad-cli's XML writes with one name: the root
// label "pic_sockets/VCC_PIC" and the sub-sheet label "VCC_PIC" both read
// "/pic_sockets/VCC_PIC". They are never collapsed into one identity (that
// would hide a short) and never told apart by where their pins sit (a net
// crosses the hierarchy through ports, so pins prove nothing): with both
// labels present the netlist is ErrNetAmbiguous, however the pins lie.
func TestSchNetlistSameXMLNameTwoNets(t *testing.T) {
	const tmpl = `<?xml version="1.0" encoding="utf-8"?>
<export version="E">
  <design>
    <source>ROOT</source>
    <sheet number="1" name="/" tstamps="/"><title_block><source>root.kicad_sch</source></title_block></sheet>
    <sheet number="2" name="/pic_sockets/" tstamps="/a/"><title_block><source>sockets.kicad_sch</source></title_block></sheet>
  </design>
  <components>
    <comp ref="R1"><value>1k</value><libsource lib="Device" part="R"/><sheetpath names="/" tstamps="/"/></comp>
    <comp ref="R2"><value>1k</value><libsource lib="Device" part="R"/><sheetpath names="/pic_sockets/" tstamps="/a/"/></comp>
  </components>
  <libparts>
    <libpart lib="Device" part="R"><pins><pin num="1" name="~" type="passive"/><pin num="2" name="~" type="passive"/></pins></libpart>
  </libparts>
  <nets>
    <net code="1" name="/pic_sockets/VCC_PIC">NODES1</net>
    <net code="2" name="/pic_sockets/VCC_PIC">NODES2</net>
  </nets>
</export>`
	both := map[string]string{
		"root.kicad_sch":    `(label "pic_sockets/VCC_PIC" (at 0 0 0))`,
		"sockets.kicad_sch": `(label "VCC_PIC" (at 0 0 0))`,
	}
	for name, nodes := range map[string][2]string{
		// pins of each net on "its" sheet — still not proof of where it was named
		"pins apart": {`<node ref="R1" pin="1"/><node ref="R1" pin="2"/>`, `<node ref="R2" pin="1"/><node ref="R2" pin="2"/>`},
		// the root-label net reaches only sub-sheet pins and vice versa (ports)
		"pins swapped": {`<node ref="R2" pin="1"/><node ref="R2" pin="2"/>`, `<node ref="R1" pin="1"/><node ref="R1" pin="2"/>`},
		// both nets cross the hierarchy
		"both cross": {`<node ref="R1" pin="1"/><node ref="R2" pin="1"/>`, `<node ref="R1" pin="2"/><node ref="R2" pin="2"/>`},
	} {
		x := strings.NewReplacer("NODES1", nodes[0], "NODES2", nodes[1]).Replace(writeSheets(t, tmpl, both))
		if _, err := ParseSchNetlist([]byte(x)); !errors.Is(err, ErrNetAmbiguous) || !strings.Contains(err.Error(), "fix:") {
			t.Errorf("%s: want ErrNetAmbiguous with a fix hint, got %v", name, err)
		}
	}
	// Only the sub-sheet label exists: one net, certain, even if it crosses.
	one := strings.Replace(tmpl, `<net code="2" name="/pic_sockets/VCC_PIC">NODES2</net>`, "", 1)
	x := strings.Replace(writeSheets(t, one, map[string]string{"root.kicad_sch": `(label "OTHER" (at 0 0 0))`, "sockets.kicad_sch": `(label "VCC_PIC" (at 0 0 0))`}),
		"NODES1", `<node ref="R1" pin="1"/><node ref="R2" pin="1"/>`, 1)
	nl, err := ParseSchNetlist([]byte(x))
	if err != nil || nl.Nets[0].Name != "/pic_sockets/VCC_PIC" {
		t.Fatalf("single label: %v %+v", err, nl)
	}
	// No sheet files (no label evidence): an error.
	x = strings.NewReplacer("ROOT", "", "NODES1", `<node ref="R1" pin="1"/>`).Replace(one)
	if _, err := ParseSchNetlist([]byte(x)); !errors.Is(err, ErrNetAmbiguous) {
		t.Fatalf("no evidence: want ErrNetAmbiguous, got %v", err)
	}
}
