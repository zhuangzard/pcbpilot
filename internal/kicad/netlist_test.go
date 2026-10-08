package kicad

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNetlistToConnectivity(t *testing.T) {
	data, err := os.ReadFile("testdata/netlist.xml")
	if err != nil {
		t.Fatal(err)
	}
	nl, err := ParseNetlistXML(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(nl.Components) != 2 || nl.Components[0].LCSC != "C25804" || nl.Components[0].MPN != "RC1206FR-0710KL" || nl.Components[1].LCSC != "C5446" {
		t.Fatalf("components: %+v", nl.Components)
	}
	doc, err := nl.ToConnectivity("tiny")
	if err != nil {
		t.Fatal(err)
	}
	if doc.SchemaVersion != "1.4" || len(doc.Nets) != 3 || len(doc.Connections) != 5 {
		t.Fatalf("doc: %d nets %d connections", len(doc.Nets), len(doc.Connections))
	}
	u1 := doc.Components[1]
	if u1.Ref != "U1" || u1.ID != "22222222-2222-2222-2222-222222222222" || u1.Device.SupplierID != "C5446" || len(u1.Pins) != 5 {
		t.Fatalf("U1: %+v", u1)
	}
	pins := map[string]string{}
	for _, p := range u1.Pins {
		pins[p.Number] = p.Name + "|" + p.ConnectionState
		if p.Number == "4" && !p.NoConnected {
			t.Fatal("U1.4 (no-connect flag) must be noConnected")
		}
	}
	if pins["1"] != "VIN|unconnected" || pins["5"] != "VOUT|" {
		t.Fatalf("pins %v", pins)
	}
	// The JSON is the sch connectivity shape (powersim.ParseConnectivity keys).
	b, _ := json.Marshal(doc)
	for _, k := range []string{`"schemaVersion":"1.4"`, `"componentId"`, `"pinNumber"`, `"netId"`, `"supplierId":"C25804"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("missing %s", k)
		}
	}
	vals := nl.Values()["parts"].(map[string]any)
	if v := vals["R1"].(map[string]string); v["lcsc"] != "C25804" || v["mpn"] != "RC1206FR-0710KL" || v["value"] != "10k" {
		t.Fatalf("values %v", v)
	}

	d := nl.DiffPads(map[[2]string]string{{"R1", "1"}: "VCC", {"R1", "2"}: "/EN", {"U1", "5"}: "VCC", {"U1", "2"}: "GND", {"U1", "3"}: "/EN"})
	if !d.Passed || d.Pins != 5 {
		t.Fatalf("clean diff: %+v", d)
	}
	d = nl.DiffPads(map[[2]string]string{{"R1", "1"}: "VCC", {"R1", "2"}: "GND", {"U1", "5"}: "VCC", {"U1", "2"}: "GND", {"U1", "1"}: "VIN_X"})
	want := []string{`R1.2: board net "GND", schematic net "/EN"`, "U1.3: on net /EN in the schematic, no such pad on the board", `U1.1: board net "VIN_X", not connected in the schematic`}
	for _, w := range want {
		found := false
		for _, x := range d.Diffs {
			found = found || x == w
		}
		if !found {
			t.Errorf("missing diff %q in %v", w, d.Diffs)
		}
	}
}
