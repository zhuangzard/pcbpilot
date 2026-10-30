package pcbauto

import "testing"

// cmcBoard: a 24 V inlet whose return line GND_J reaches the board ground
// GND through pins 2/3 of a 4-pin inductor L1.
func cmcBoard() *Board {
	return hvBoard(
		hvPart("J1", "KF2EDGV", Point{200, 1000}, thtPad("1", "VIN_J", 0, 100, 80), thtPad("2", "GND_J", 0, -100, 80)),
		hvPart("L1", "PM3700-40", Point{700, 1000}, smdPad("1", "VIN_RAW", 150, 150, 80, 80), smdPad("2", "GND", 150, -150, 80, 80),
			smdPad("3", "GND_J", -150, -150, 80, 80), smdPad("4", "VIN_J", -150, 150, 80, 80)),
		hvPart("C1", "100uF", Point{1100, 1000}, smdPad("1", "VIN_RAW", 0, 60, 60, 60), smdPad("2", "GND", 0, -60, 60, 60)),
	)
}

// A common-mode choke with DECLARED windings 1–4 / 2–3 carries the return
// line through 2–3: GND_J and GND are one circuit (a field board got a
// separate "SELV_24V" domain for the inlet nets with no insulation pair to
// the patient side).
func TestCMCReturnWindingIsOneDomain(t *testing.T) {
	b := cmcBoard()
	an := Analyze(b, PowerSpec{Windings: map[string][][2]string{"L1": {{"1", "4"}, {"2", "3"}}}}, nil)
	c := Understand(b, an)
	if c.DomainOf["J1"] == "" || c.DomainOf["J1"] != c.DomainOf["C1"] || c.DomainOf["L1"] != c.DomainOf["C1"] {
		t.Fatalf("inlet split from the board ground: %v", c.DomainOf)
	}
	if len(c.UnknownWindings) != 0 {
		t.Fatalf("declared windings reported unknown: %v", c.UnknownWindings)
	}
	// The intent's "windings" declare them too.
	an = Analyze(b, PowerSpec{Intent: &Intent{Windings: map[string][][2]string{"L1": {{"1", "4"}, {"2", "3"}}}}}, nil)
	if c := Understand(b, an); c.DomainOf["J1"] != c.DomainOf["C1"] {
		t.Fatalf("intent windings ignored: %v", c.DomainOf)
	}
}

// Undeclared, a 4-pad inductor never joins grounds: wired 1–2 / 3–4 (or a
// dual inductor) the same pads are two separate circuits, and merging them
// would suppress their insulation pair. It is reported instead.
func TestCMCUnknownOrOtherWindingsKeepGroundsApart(t *testing.T) {
	b := cmcBoard()
	for name, spec := range map[string]PowerSpec{
		"undeclared":         {},
		"declared 1–2 / 3–4": {Windings: map[string][][2]string{"L1": {{"1", "2"}, {"3", "4"}}}},
	} {
		c := Understand(b, Analyze(b, spec, nil))
		if c.DomainOf["J1"] == c.DomainOf["C1"] {
			t.Errorf("%s: GND_J and GND merged: %v", name, c.DomainOf)
		}
		if unknown := len(c.UnknownWindings) == 1 && c.UnknownWindings[0] == "L1"; unknown != (name == "undeclared") {
			t.Errorf("%s: unknown windings %v", name, c.UnknownWindings)
		}
	}
}
