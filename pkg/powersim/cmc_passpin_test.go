package powersim

import "testing"

// A 24 V inlet through a 4-pin common-mode choke, an eFuse (pass element
// with control pins) and a P-FET into a 1 A load. Before 2026-10-14 the
// 4-pin choke was left open (two-terminal handling only) and a switch
// with more than two nets was skipped: a field board simulated every rail at 0 V.
const cmcLib = `{"schemaVersion":1,"models":[
 {"id":"dc","kind":"connector-source","match":{"mpn":["DC24"]},"supplyPins":["1"],"returnPins":["2"],"voltageV":24,"rsOhm":0.05,"sourceName":"dc"},
 {"id":"cmc","kind":"inductor","match":{"mpn":["CMC"]},"windings":[["1","4"],["2","3"]],"dcrOhm":0.03},
 {"id":"cmc2","kind":"inductor","match":{"mpn":["CMC-DEFAULT"]},"dcrOhm":0.04},
 {"id":"efuse","kind":"switch","match":{"mpn":["EFUSE"]},"momentary":false,"pins":{"in":["IN"],"out":["OUT"]},"ronOhm":0.15},
 {"id":"pfet","kind":"switch","match":{"mpn":["PFET"]},"momentary":false,"pins":{"in":["S"],"out":["D"]},"ronOhm":0.014},
 {"id":"ld","kind":"load","match":{"mpn":["LOAD"]},"supplyPins":["1"],"returnPins":["2"],"typA":1,"peakA":1}
]}`

func TestCMCWindingsAndPassPins(t *testing.T) {
	d := design(
		part("J1", "", "DC24", "1:1:VIN_J", "2:2:RTN_J"),
		part("L1", "", "CMC", "1:1:VIN_RAW", "2:2:GND", "3:3:RTN_J", "4:4:VIN_J"),
		part("U1", "", "EFUSE", "1:IN:VIN_RAW", "2:IN:VIN_RAW", "3:UVLO:EF_UV", "4:GND:GND", "5:OUT:V24", "6:OUT:V24"),
		part("R1", "100kΩ", "", "1:1:VIN_RAW", "2:2:EF_UV"),
		part("R2", "10kΩ", "", "1:1:EF_UV", "2:2:GND"),
		part("Q1", "", "PFET", "1:S:V24", "2:S:V24", "3:G:QG", "4:D:V24_SW", "5:D:V24_SW"),
		part("R3", "100kΩ", "", "1:1:QG", "2:2:GND"),
		part("L2", "", "CMC-DEFAULT", "1:1:V24_SW", "2:2:GND", "3:3:RTN_L", "4:4:OUT_L"),
		part("J2", "", "LOAD", "1:1:OUT_L", "2:2:RTN_L"),
	)
	d.NetRole = map[string]string{"GND": "ground"}
	out, _, err := Simulate(d, testLib(t, cmcLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := findResult(t, out, "typical")
	assertKCL(t, res)
	// 1 A (+ divider µA) through 0.05 + 0.03 + 0.15 + 0.014 + 0.04 Ω.
	for _, n := range []string{"VIN_J", "RTN_J", "VIN_RAW", "V24", "V24_SW", "OUT_L", "RTN_L"} {
		t.Logf("%s %.4f V %.4f A", n, res.Nets[n].Voltage, res.Nets[n].CurrentA)
	}
	near(t, "V(OUT_L)-V(RTN_L)", res.Nets["OUT_L"].Voltage-res.Nets["RTN_L"].Voltage, 24-1.0*(0.05+2*0.03+0.15+0.014+2*0.04), 0.01)
	// The return winding 2–3 carries the load current back to the inlet.
	near(t, "I(L1.3)", pinCurrent(t, res, "RTN_J", "L1", "3").CurrentA, 1.0, 0.01)
	near(t, "I(L1.4)", pinCurrent(t, res, "VIN_J", "L1", "4").CurrentA, 1.0, 0.01)
	near(t, "I(U1 OUT)", res.Nets["V24"].CurrentA, 1.0, 0.01)
	// The eFuse control pin carries only the divider current, not the load.
	if a := pinCurrent(t, res, "EF_UV", "U1", "3").CurrentA; a > 1e-6 {
		t.Fatalf("eFuse UVLO pin carries %g A, want 0", a)
	}
}

// The CMC's return winding between two GROUND nets (GND_J on the inlet,
// GND on the board): every ground net used to be the one reference node,
// so the return current and the winding's DCR vanished. The connector-side
// ground is now its own node: the drop over the return winding is solved.
func TestCMCReturnWindingBetweenGrounds(t *testing.T) {
	d := design(
		part("J1", "", "DC24", "1:1:VIN_J", "2:2:GND_J"),
		part("L1", "", "CMC", "1:1:VIN_RAW", "2:2:GND", "3:3:GND_J", "4:4:VIN_J"),
		part("J2", "", "LOAD", "1:1:VIN_RAW", "2:2:GND"),
	)
	d.NetRole = map[string]string{"GND": "ground", "GND_J": "ground"}
	out, _, err := Simulate(d, testLib(t, cmcLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := findResult(t, out, "typical")
	assertKCL(t, res)
	// 1 A out through the 1–4 winding, back through 2–3: the inlet ground
	// sits 0.03 V below the board ground, the load sees 24 − 1·(0.05 + 2·0.03).
	near(t, "V(GND_J)", res.Nets["GND_J"].Voltage, -0.03, 0.002)
	near(t, "V(VIN_RAW)", res.Nets["VIN_RAW"].Voltage, 24-1.0*(0.05+0.03)-0.03, 0.01)
	near(t, "I(L1.3)", pinCurrent(t, res, "GND_J", "L1", "3").CurrentA, 1.0, 0.01)
}

// Two chokes in a chain of grounds GND_J –L1– GND_M –L2– GND, the middle
// one with the fewest pins: choosing a far ground per winding floated
// GND_M twice and left GND_J and GND both on the reference, shorting the
// chain. One reference per joined group (GND, the most pins): both
// return windings carry the current and drop their DCR.
func TestCMCChainedReturnWindings(t *testing.T) {
	d := design(
		part("J1", "", "DC24", "1:1:VIN_J", "2:2:GND_J"),
		part("C9", "", "", "1:1:VIN_J", "2:2:GND_J"),
		part("L1", "", "CMC", "1:1:VIN_M", "2:2:GND_M", "3:3:GND_J", "4:4:VIN_J"),
		part("L2", "", "CMC", "1:1:VIN_RAW", "2:2:GND", "3:3:GND_M", "4:4:VIN_M"),
		part("J2", "", "LOAD", "1:1:VIN_RAW", "2:2:GND"),
		part("C1", "", "", "1:1:VIN_RAW", "2:2:GND"),
		part("C2", "", "", "1:1:VIN_RAW", "2:2:GND"),
	)
	d.NetRole = map[string]string{"GND": "ground", "GND_J": "ground", "GND_M": "ground"}
	out, _, err := Simulate(d, testLib(t, cmcLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := findResult(t, out, "typical")
	assertKCL(t, res)
	near(t, "V(GND_M)", res.Nets["GND_M"].Voltage, -0.03, 0.002)
	near(t, "V(GND_J)", res.Nets["GND_J"].Voltage, -0.06, 0.002)
	near(t, "I(L1.3)", pinCurrent(t, res, "GND_J", "L1", "3").CurrentA, 1.0, 0.01)
	near(t, "I(L2.3)", pinCurrent(t, res, "GND_M", "L2", "3").CurrentA, 1.0, 0.01)
}
