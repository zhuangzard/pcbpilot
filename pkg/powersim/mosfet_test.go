package powersim

import (
	"strings"
	"testing"
)

// Two pass FETs with one MPN: a reverse-polarity P-FET (on by default) and
// an over-voltage N-FET the project declares off, bound by designator; an
// MPN model of the same part (a generic file) loses to the ref model.
const fetLib = `{"schemaVersion":1,"models":[
 {"id":"in","kind":"connector-source","match":{"mpn":["IN"]},"supplyPins":["1"],"returnPins":["2"],"voltageV":12,"rsOhm":0},
 {"id":"q2-off","kind":"mosfet","match":{"refs":["Q2"]},"on":false},
 {"id":"q1-pass","kind":"mosfet","match":{"refs":["q1"]},"ronOhm":0.02},
 {"id":"fet-mpn","kind":"open","match":{"mpn":["FET-X"]}}
]}`

func TestMOSFETSwitchByRef(t *testing.T) {
	d := design(
		part("J1", "", "IN", "1:1:VIN", "2:2:GND"),
		part("Q1", "", "FET-X", "1:G:GND", "2:S:VPROT", "3:D:VIN", "4:D:VIN"),
		part("R1", "100Ω", "", "1:1:VPROT", "2:2:GND"),
		part("Q2", "", "FET-X", "1:G:GND", "2:S:VOV", "3:D:VPROT"),
		part("R2", "100Ω", "", "1:1:VOV", "2:2:GND"),
	)
	out, _, err := Simulate(d, testLib(t, fetLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := findResult(t, out, "typical")
	assertKCL(t, res)
	// Q1 on: 12 V (50 mΩ default source) across 20 mΩ + 100 Ω.
	near(t, "V(VPROT)", res.Nets["VPROT"].Voltage, 12*100/100.07, 1e-4)
	// Q2 off: nothing reaches VOV.
	if v := res.Nets["VOV"]; v != nil && v.Voltage > 1e-6 {
		t.Fatalf("V(VOV) = %.6f V with Q2 declared off", v.Voltage)
	}
	why := map[string]string{}
	for _, m := range out.Models {
		why[m.Ref] = m.ModelID + " by " + m.Match
	}
	if !strings.HasPrefix(why["Q1"], "q1-pass by ref") || !strings.HasPrefix(why["Q2"], "q2-off by ref") {
		t.Fatalf("models: %v", why)
	}

	// --switch overrides the model's state.
	out, _, err = Simulate(d, testLib(t, fetLib), Options{Switches: map[string]bool{"Q1": false, "Q2": true}})
	if err != nil {
		t.Fatal(err)
	}
	res = findResult(t, out, "typical")
	if v := res.Nets["VPROT"]; v != nil && v.Voltage > 1e-6 {
		t.Fatalf("V(VPROT) = %.6f V with Q1 forced open", v.Voltage)
	}
}

// Without a model a MOSFET stays open (old model files keep their meaning)
// and the warning says how to model it.
func TestMOSFETUnmodelledStaysOpen(t *testing.T) {
	d := design(
		part("J1", "", "IN", "1:1:VIN", "2:2:GND"),
		part("Q5", "", "FET-Y", "1:G:GND", "2:S:VPROT", "3:D:VIN"),
		part("R1", "100Ω", "", "1:1:VPROT", "2:2:GND"),
	)
	d.Parts[1].Description = "P-channel MOSFET"
	out, _, err := Simulate(d, testLib(t, fetLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := findResult(t, out, "typical")
	if v := res.Nets["VPROT"]; v != nil && v.Voltage > 1e-6 {
		t.Fatalf("unmodelled MOSFET conducts: V(VPROT) = %.6f", v.Voltage)
	}
	found := false
	for _, w := range res.Warnings {
		found = found || strings.Contains(w, `"kind":"mosfet"`)
	}
	if !found {
		t.Fatalf("no modelling hint: %v", res.Warnings)
	}
}
