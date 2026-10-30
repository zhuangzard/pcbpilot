package kicad

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/population"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// One population rule end to end (pkg/population): KiCad's DNP flag
// decides in the netlist IR, sim power, kicad lcsc and fab alike. R1 has the
// flag (and a note field), R2 only a field named "DNP" with a note — it is
// fitted everywhere, and every tool gives the same warning; R3 is plain.
func TestPopulationRuleSharedByNetlistSimAndFab(t *testing.T) {
	xml := `<?xml version="1.0" encoding="utf-8"?>
<export version="E">
  <design><source>root.kicad_sch</source><sheet number="1" name="/" tstamps="/"/></design>
  <components>
    <comp ref="R1"><value>0</value><fields><field name="DNP">option A</field></fields><libsource lib="Device" part="R"/><sheetpath names="/" tstamps="/"/><property name="dnp"/></comp>
    <comp ref="R2"><value>0</value><fields><field name="DNP">backup footprint</field></fields><libsource lib="Device" part="R"/><sheetpath names="/" tstamps="/"/></comp>
    <comp ref="R3"><value>1k</value><libsource lib="Device" part="R"/><sheetpath names="/" tstamps="/"/></comp>
  </components>
  <libparts>
    <libpart lib="Device" part="R"><pins><pin num="1" name="~" type="passive"/><pin num="2" name="~" type="passive"/></pins></libpart>
  </libparts>
  <nets>
    <net code="1" name="/VIN"><node ref="R1" pin="1"/><node ref="R2" pin="1"/><node ref="R3" pin="1"/></net>
    <net code="2" name="/VOUT"><node ref="R1" pin="2"/><node ref="R2" pin="2"/><node ref="R3" pin="2"/></net>
  </nets>
</export>`
	nl, err := ParseSchNetlist([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	want := population.Conflict("R2", "backup footprint", false)

	// Netlist IR.
	doc, err := nl.ToConnectivityDoc("p")
	if err != nil {
		t.Fatal(err)
	}
	ir := map[string][2]any{}
	for _, c := range doc.Components {
		ir[c.Ref] = [2]any{c.DNP, c.DNPField}
	}
	if ir["R1"] != [2]any{true, ""} || ir["R2"] != [2]any{false, "backup footprint"} || ir["R3"] != [2]any{false, ""} {
		t.Fatalf("IR population %v", ir)
	}

	// sim power reads the IR.
	blob, _ := json.Marshal(doc)
	cd, err := powersim.ParseConnectivity(blob)
	if err != nil {
		t.Fatal(err)
	}
	// A values file that marks R3 dnp does not override the flag.
	d, simWarn, err := powersim.BuildDesign([]*powersim.ConnDoc{cd}, map[string]powersim.PartValues{"R3": {Value: "1k", DNP: true}})
	if err != nil {
		t.Fatal(err)
	}
	simulated := map[string]bool{}
	for _, p := range d.Parts {
		simulated[p.Ref] = true
	}
	if simulated["R1"] || !simulated["R2"] || !simulated["R3"] {
		t.Fatalf("simulated %v", simulated)
	}
	if !strings.Contains(strings.Join(simWarn, "\n"), want) {
		t.Fatalf("sim power warnings %v lack %q", simWarn, want)
	}
	if w := population.ValuesConflict("R3", true, false); !strings.Contains(strings.Join(simWarn, "\n"), w) {
		t.Fatalf("sim power warnings %v lack %q", simWarn, w)
	}

	// lcsc / fab: the BOM's ${DNP} column is shadowed by the field text.
	csv := "\"Reference\",\"Value\",\"Footprint\",\"DNP\",\"EXCLUDE_FROM_BOM\",\"LCSC\"\n" +
		"\"R1\",\"0\",\"R:R\",\"option A\",\"\",\"\"\n" +
		"\"R2\",\"0\",\"R:R\",\"backup footprint\",\"\",\"\"\n" +
		"\"R3\",\"1k\",\"R:R\",\"\",\"\",\"C1\"\n"
	rows, err := ParseSchBOM([]byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	rows = ApplyNetlistPopulation(rows, nl)
	bd := &Board{Footprints: []Footprint{{Ref: "R1"}, {Ref: "R2"}, {Ref: "R3"}}}
	parts, warns := MergeParts(bd, rows, true)
	assembled := map[string]bool{}
	for _, p := range parts {
		assembled[p.Ref] = p.Assembled()
	}
	if assembled["R1"] || !assembled["R2"] || !assembled["R3"] {
		t.Fatalf("assembled %v", assembled)
	}
	if strings.Join(warns, "\n") != want {
		t.Fatalf("fab/lcsc warnings %v, want exactly %q", warns, want)
	}
}
