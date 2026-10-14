package pcbauto

import "testing"

// The return line of a 24 V inlet runs through one winding of a 4-pin
// common-mode choke (GND_J on the connector, GND on the board). It is one
// circuit: JDB-1 got a separate "SELV_24V" domain for the inlet nets with
// no insulation pair to the patient side, so its copper was never held to
// the 2 × MOPP creepage.
func TestCMCReturnWindingIsOneDomain(t *testing.T) {
	b := hvBoard(
		hvPart("J1", "KF2EDGV", Point{200, 1000}, thtPad("1", "VIN_J", 0, 100, 80), thtPad("2", "GND_J", 0, -100, 80)),
		hvPart("L1", "PM3700-40", Point{700, 1000}, smdPad("1", "VIN_RAW", 150, 150, 80, 80), smdPad("2", "GND", 150, -150, 80, 80),
			smdPad("3", "GND_J", -150, -150, 80, 80), smdPad("4", "VIN_J", -150, 150, 80, 80)),
		hvPart("C1", "100uF", Point{1100, 1000}, smdPad("1", "VIN_RAW", 0, 60, 60, 60), smdPad("2", "GND", 0, -60, 60, 60)),
	)
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	if c.DomainOf["J1"] == "" || c.DomainOf["J1"] != c.DomainOf["C1"] || c.DomainOf["L1"] != c.DomainOf["C1"] {
		t.Fatalf("inlet split from the board ground: %v", c.DomainOf)
	}
}
