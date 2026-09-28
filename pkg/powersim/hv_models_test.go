package powersim

import (
	"strings"
	"testing"
)

// Models the high-voltage stress fixtures need (testdata/stress/hv): a bridge
// rectifier, a transformer / isolated module as a multi-output source whose
// outputs return to their own references, an isolator whose two supply rails
// return to different grounds, and generic varistors / transformers /
// thermistors that used to be bound as 50 mA "unknown IC" loads.
const hvLib = `{"schemaVersion":1,"models":[
 {"id":"line","kind":"connector-source","match":{"mpn":["LINE"]},"supplyPins":["1"],"returnPins":["2"],"voltageV":325,"rsOhm":0.5,"sourceName":"mains"},
 {"id":"br","kind":"bridge","match":{"mpn":["MB10S"]},"pins":{"ac":["~1","~2"],"plus":["+"],"minus":["-"]},"vfV":0.9,"ifA":0.5},
 {"id":"fly","kind":"connector-source","match":{"mpn":["FLY"]},"sourceName":"flyback",
  "outputs":[{"name":"sec","pins":["S+"],"returnPins":["S-"],"voltageV":12.5,"rsOhm":0.01},
             {"name":"aux","pins":["A+"],"returnPins":["A-"],"voltageV":16,"rsOhm":1}]},
 {"id":"iso","kind":"connector-source","match":{"mpn":["ISODCDC"]},"sourceName":"bias",
  "outputs":[{"pins":["+VO"],"returnPins":["0V"],"voltageV":15,"rsOhm":0.5}],
  "rails":[{"pins":["+VIN"],"typA":0.05,"peakA":0.05,"returnPins":["-VIN"]}]},
 {"id":"drv","kind":"load","match":{"mpn":["DRV"]},"returnPins":["GND"],
  "rails":[{"pins":["VCCI"],"typA":0.002,"peakA":0.002,"returnPins":["GND"]},
           {"pins":["VDDA"],"typA":0.006,"peakA":0.006,"returnPins":["VSSA"]}]},
 {"id":"ld","kind":"load","match":{"mpn":["LOAD"]},"supplyPins":["1"],"returnPins":["2"],"typA":1,"peakA":1},
 {"id":"host","kind":"connector-source","match":{"mpn":["HOST"]},"supplyPins":["1"],"returnPins":["2"],"voltageV":5,"sourceName":"host"}
]}`

func TestHVModels(t *testing.T) {
	d := design(
		part("J1", "", "LINE", "1:L:L", "2:N:N"),
		part("RV1", "", "07D471K", "1:1:L", "2:2:N"),
		part("BR1", "", "MB10S", "1:+:HV_BULK", "2:-:PGND", "3:~1:L", "4:~2:N"),
		part("RT1", "1kΩ", "PTC-X", "1:1:HV_BULK", "2:2:HV_R"),
		part("R1", "1MΩ", "", "1:1:HV_R", "2:2:PGND"),
		part("T1", "", "FLY", "1:P+:HV_BULK", "2:P-:DRAIN", "3:A+:AUX", "4:A-:PGND", "6:S+:VOUT", "8:S-:GND_S"),
		part("T2", "", "EE-UNMODELLED", "1:1:HV_BULK", "2:2:DRAIN"),
		part("J2", "", "LOAD", "1:1:VOUT", "2:2:GND_S"),
		part("R2", "10kΩ", "", "1:1:AUX", "2:2:PGND"),
		part("J3", "", "HOST", "1:1:+5V", "2:2:GND"),
		part("U1", "", "ISODCDC", "1:+VIN:+5V", "2:-VIN:GND", "5:+VO:VDDA", "6:0V:KS"),
		part("U2", "", "DRV", "1:VCCI:+5V", "2:GND:GND", "3:VDDA:VDDA", "4:VSSA:KS"),
	)
	d.NetRole = map[string]string{"GND": "ground", "PGND": "ground", "GND_S": "ground"}
	out, _, err := Simulate(d, testLib(t, hvLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := findResult(t, out, "typical")
	assertKCL(t, res)
	// Bridge: two diode drops from the 325 V line to the bulk.
	if v := res.Nets["HV_BULK"].Voltage; v < 322 || v > 324.5 {
		t.Fatalf("V(HV_BULK) = %.2f, want 325 − 2 VF", v)
	}
	// PTC as a 1 kΩ resistor feeding the 1 MΩ bleeder (not a 50 mA load).
	near(t, "I(R1)", pinCurrent(t, res, "HV_R", "R1", "1").CurrentA, res.Nets["HV_BULK"].Voltage/1.001e6, 2e-6)
	// Transformer outputs: 12.5 V secondary loaded with 1 A, 16 V aux into 10 kΩ.
	near(t, "V(VOUT)", res.Nets["VOUT"].Voltage, 12.49, 0.005)
	near(t, "V(AUX)", res.Nets["AUX"].Voltage, 16.0*10000/10001, 0.01)
	// Isolated module: its output returns through its own 0 V pin; its input
	// draws the declared rail; the driver's output rail returns to VSSA.
	near(t, "V(VDDA)-V(KS)", res.Nets["VDDA"].Voltage-res.Nets["KS"].Voltage, 15-0.5*0.006, 1e-3)
	near(t, "U1 +VIN", pinCurrent(t, res, "+5V", "U1", "1").CurrentA, 0.05, 1e-6)
	near(t, "U2 VDDA", pinCurrent(t, res, "VDDA", "U2", "3").CurrentA, 0.006, 1e-6)
	near(t, "U2 VSSA", pinCurrent(t, res, "KS", "U2", "4").CurrentA, 0.006, 1e-6)
	kinds := map[string]string{}
	for _, m := range out.Models {
		kinds[m.Ref] = m.Kind
	}
	if kinds["RV1"] != KindOpen || kinds["T2"] != KindOpen || kinds["RT1"] != KindResistor || kinds["BR1"] != KindBridge {
		t.Fatalf("generic kinds %v", kinds)
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "T2: transformer") {
		t.Fatalf("unmodelled transformer not warned: %v", res.Warnings)
	}
	// J2 is the product's output terminal: the 12.5 W its "load" model draws
	// is the external load's, not board heat (the flyback E2E put 24 W on the
	// terminal's pads: 8374 °C). U2 (a load on the board) keeps its heat.
	if j2 := res.Parts["J2"]; !j2.OffBoard || j2.PowerW < 10 || j2.ThermalW != 0 {
		t.Fatalf("J2 %+v: an output terminal's load is off the board", j2)
	}
	if u2 := res.Parts["U2"]; u2.OffBoard || u2.ThermalW <= 0 {
		t.Fatalf("U2 %+v: an on-board load keeps its heat", u2)
	}
	// RV1 must draw nothing (it used to be a 50 mA "unknown IC" load).
	if p := pinCurrent(t, res, "L", "RV1", "1"); p.CurrentA > 1e-9 {
		t.Fatalf("varistor draws %.3g A", p.CurrentA)
	}
}
