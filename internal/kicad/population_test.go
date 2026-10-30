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
	bd := &Board{Footprints: []Footprint{{Ref: "R1", DNP: true}, {Ref: "R2"}, {Ref: "R3"}}} // synced with the schematic
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

// With schematic data the schematic's DNP flag decides in lcsc / fab, as
// in sim power: a stale board DNP attribute is reported, both ways, never
// OR-ed; a board-only run keeps the board's attribute.
func TestPopulationBoardFlagMismatch(t *testing.T) {
	rows := []SchPart{{Ref: "R1"}, {Ref: "R2", DNP: true}, {Ref: "R3"}}
	bd := &Board{Footprints: []Footprint{{Ref: "R1", DNP: true}, {Ref: "R2"}, {Ref: "R3"}}}
	parts, warns := MergeParts(bd, rows, true)
	fitted := map[string]bool{}
	for _, p := range parts {
		fitted[p.Ref] = !p.DNP
	}
	if !fitted["R1"] || fitted["R2"] || !fitted["R3"] {
		t.Fatalf("fitted %v", fitted)
	}
	got := strings.Join(warns, "\n")
	for _, w := range []string{population.BoardConflict("R1", true, false), population.BoardConflict("R2", false, true)} {
		if !strings.Contains(got, w) {
			t.Fatalf("warnings %v lack %q", warns, w)
		}
	}
	if strings.Contains(got, "R3") {
		t.Fatalf("agreeing part warned: %v", warns)
	}
	// No schematic: the board's attribute is all there is.
	parts, warns = MergeParts(bd, nil, false)
	if !parts[0].DNP || parts[1].DNP || len(warns) != 0 {
		t.Fatalf("board-only %+v %v", parts, warns)
	}
}

// A symbol excluded from the BOM is missing from kicad-cli's BOM export but
// not from the netlist: its schematic flags still decide. Here it is DNP in
// the schematic while its stale board footprint has neither flag — sim
// power leaves it out, and lcsc / fab must too (and say the board is stale)
// instead of falling back to the board-only rule and assembling it.
func TestPopulationExcludedFromBOMKeepsSchematicFlag(t *testing.T) {
	xml := `<?xml version="1.0" encoding="utf-8"?>
<export version="E">
  <design><source>root.kicad_sch</source><sheet number="1" name="/" tstamps="/"/></design>
  <components>
    <comp ref="R1"><value>1k</value><libsource lib="Device" part="R"/><sheetpath names="/" tstamps="/"/></comp>
    <comp ref="R7"><value>0</value><libsource lib="Device" part="R"/><sheetpath names="/" tstamps="/"/><property name="dnp"/><property name="exclude_from_bom"/></comp>
  </components>
  <libparts>
    <libpart lib="Device" part="R"><pins><pin num="1" name="~" type="passive"/><pin num="2" name="~" type="passive"/></pins></libpart>
  </libparts>
  <nets>
    <net code="1" name="/A"><node ref="R1" pin="1"/><node ref="R7" pin="1"/></net>
    <net code="2" name="/B"><node ref="R1" pin="2"/><node ref="R7" pin="2"/></net>
  </nets>
</export>`
	nl, err := ParseSchNetlist([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	// sim power: R7 is not fitted.
	doc, err := nl.ToConnectivityDoc("p")
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(doc)
	cd, err := powersim.ParseConnectivity(blob)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := powersim.BuildDesign([]*powersim.ConnDoc{cd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Parts {
		if p.Ref == "R7" {
			t.Fatal("sim power simulated the DNP part R7")
		}
	}
	// lcsc / fab: the BOM export has no R7 row; the board footprint is stale.
	rows, err := ParseSchBOM([]byte("\"Reference\",\"Value\",\"Footprint\",\"DNP\",\"EXCLUDE_FROM_BOM\",\"LCSC\"\n\"R1\",\"1k\",\"R:R\",\"\",\"\",\"C1\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	rows = ApplyNetlistPopulation(rows, nl)
	bd := &Board{Footprints: []Footprint{{Ref: "R1"}, {Ref: "R7"}}}
	parts, warns := MergeParts(bd, rows, true)
	for _, p := range parts {
		if p.Ref == "R7" && (p.Assembled() || !p.DNP) {
			t.Fatalf("R7 assembled by lcsc/fab: %+v", p)
		}
	}
	got := strings.Join(warns, "\n")
	if !strings.Contains(got, population.BoardConflict("R7", false, true)) || strings.Contains(got, "not in the schematic BOM") {
		t.Fatalf("warnings %v", warns)
	}
}
