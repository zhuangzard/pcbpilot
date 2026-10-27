package kicad

import (
	"encoding/json"
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
    <sheet number="1" name="/" tstamps="/"/>
    <sheet number="2" name="/pic_sockets/" tstamps="/a/"/>
    <sheet number="3" name="/i2c_a/" tstamps="/b/"/>
    <sheet number="4" name="/i2c_b/" tstamps="/c/"/>
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

func TestSchNetlistCanonicalNetIdentity(t *testing.T) {
	nl, err := ParseSchNetlist([]byte(hierNetXML))
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
