package powersim

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestOutputContract pins the JSON field names consumed by the width planner.
func TestOutputContract(t *testing.T) {
	out, eng, err := Simulate(buckDesign(), testLib(t, buckLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["schemaVersion"].(float64) != 1 || doc["generator"] != "pcbpilot sim power" {
		t.Fatalf("header %v %v", doc["schemaVersion"], doc["generator"])
	}
	scen := doc["scenarios"].([]any)
	if scen[0] != "typical" || scen[1] != "peak" || scen[len(scen)-1] != "worst" {
		t.Fatalf("scenarios %v", scen)
	}
	res := doc["results"].([]any)[1].(map[string]any)
	for _, k := range []string{"scenario", "nets", "parts", "ripple", "warnings", "assumptions"} {
		if _, ok := res[k]; !ok {
			t.Fatalf("result lacks %q", k)
		}
	}
	net := res["nets"].(map[string]any)["+3V3"].(map[string]any)
	for _, k := range []string{"voltage", "currentA", "role", "pins"} {
		if _, ok := net[k]; !ok {
			t.Fatalf("net lacks %q", k)
		}
	}
	pin := net["pins"].([]any)[0].(map[string]any)
	for _, k := range []string{"ref", "pin", "name", "currentA", "dir"} {
		if _, ok := pin[k]; !ok {
			t.Fatalf("pin lacks %q", k)
		}
	}
	part := res["parts"].(map[string]any)["U1"].(map[string]any)
	for _, k := range []string{"model", "powerW", "notes"} {
		if _, ok := part[k]; !ok {
			t.Fatalf("part lacks %q", k)
		}
	}
	sw := res["ripple"].(map[string]any)["SW"].(map[string]any)
	if _, ok := sw["iPeakA"]; !ok {
		t.Fatal("ripple SW lacks iPeakA")
	}
	if _, ok := sw["iRmsA"]; !ok {
		t.Fatal("ripple SW lacks iRmsA")
	}
	// SPICE export carries every element class and the .op control block.
	var cir bytes.Buffer
	if err := eng.WriteSPICE(&cir, "peak"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"n_p3v3", "U1 regulator output", "U1 regulator input", "print all", "rshunt"} {
		if !strings.Contains(cir.String(), want) {
			t.Fatalf("netlist lacks %q:\n%s", want, cir.String())
		}
	}
	var md bytes.Buffer
	if err := WriteReport(&md, out); err != nil || !strings.Contains(md.String(), "| peak | U1 | regulating |") {
		t.Fatalf("report: %v\n%s", err, md.String())
	}
}

func TestParseValuesFormats(t *testing.T) {
	list := `{"id":"r","ok":true,"result":{"components":[
	 {"componentType":"sheet","designator":""},
	 {"designator":"R3","manufacturerId":"0402WGF4532TCE","supplierId":"0402WGF4532TCE.1","deviceResolution":{"lcsc":"C26980"},"otherProperty":{"Value":"45.3kΩ"}},
	 {"designator":"U4","name":"={Manufacturer Part}","manufacturerId":"","supplierId":"C479074","otherProperty":{"Manufacturer Part":"SY8089A1AAC"}}]}}`
	v, err := ParseValues([]byte(list))
	if err != nil {
		t.Fatal(err)
	}
	if v["R3"].Value != "45.3kΩ" || v["R3"].LCSC != "C26980" || v["R3"].MPN != "0402WGF4532TCE" {
		t.Fatalf("R3 %+v", v["R3"])
	}
	if v["U4"].MPN != "SY8089A1AAC" || v["U4"].LCSC != "C479074" {
		t.Fatalf("U4 %+v", v["U4"])
	}
	plain := `{"_provenance":"x","parts":{"R1":"10k","U1":{"mpn":"CH340C","lcsc":"C84681"}}}`
	v, err = ParseValues([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	if v["R1"].Value != "10k" || v["U1"].LCSC != "C84681" || len(v) != 2 {
		t.Fatalf("plain %+v", v)
	}
	if _, err := ParseConnectivity([]byte(`{"connectivity":{"components":[{"id":"a","ref":"R1","pins":[{"number":"1"}]}],"nets":[],"connections":[]}}`)); err != nil {
		t.Fatalf("wrapped connectivity: %v", err)
	}
}
