package pcbauto

import (
	"strings"
	"testing"
)

// tieBoard: two supplies with their own grounds GND_A / GND_B, an
// optocoupler between them, and the given parts across the gap.
func tieBoard(across ...*Part) *Board {
	parts := []*Part{
		hvPart("U1", "MCU-A", Point{300, 500}, smdPad("1", "VCC_A", 0, 50, 40, 40), smdPad("2", "GND_A", 0, -50, 40, 40), smdPad("3", "SIG_A", 50, 0, 40, 40)),
		hvPart("C1", "100nF", Point{300, 800}, smdPad("1", "VCC_A", 0, 30, 40, 40), smdPad("2", "GND_A", 0, -30, 40, 40)),
		hvPart("U2", "MCU-B", Point{2300, 500}, smdPad("1", "VCC_B", 0, 50, 40, 40), smdPad("2", "GND_B", 0, -50, 40, 40), smdPad("3", "SIG_B", 50, 0, 40, 40)),
		hvPart("C2", "100nF", Point{2300, 800}, smdPad("1", "VCC_B", 0, 30, 40, 40), smdPad("2", "GND_B", 0, -30, 40, 40)),
		hvPart("U_ISO1", "PC817", Point{1300, 500}, smdPad("1", "SIG_A", -100, 50, 40, 40), smdPad("2", "GND_A", -100, -50, 40, 40),
			smdPad("3", "GND_B", 100, -50, 40, 40), smdPad("4", "SIG_B", 100, 50, 40, 40)),
	}
	return hvBoard(append(parts, across...)...)
}

func tiePart(ref, dev, value, a, b string) *Part {
	p := hvPart(ref, dev, Point{1300, 1200}, smdPad("1", a, -40, 0, 40, 40), smdPad("2", b, 40, 0, 40, 40))
	p.Value = value
	return p
}

func sameDomain(b *Board) (*Circuit, bool) {
	c := Understand(b, Analyze(b, PowerSpec{}, nil))
	return c, c.DomainOf["U1"] == c.DomainOf["U2"]
}

// A part between two grounds joins them only when it is a connection: a
// Y capacitor, a ≥ 100 kΩ bleeder, a spark gap or any other unrecognised
// two-pad part across a barrier is not (each used to merge GND_A and GND_B
// and with them the two sides of the board).
func TestDomainGroundTieOnlyThroughConnections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		part  *Part
		joins bool
	}{
		{"Y capacitor CY", tiePart("CY1", "2.2nF Y1", "", "GND_A", "GND_B"), false},
		{"X capacitor CX", tiePart("CX1", "100nF X2", "", "GND_A", "GND_B"), false},
		{"bleeder 1M (value)", tiePart("R1", "RC1206FR-071ML", "1M", "GND_A", "GND_B"), false},
		{"bleeder 4M7 (device)", tiePart("R1", "4M7", "", "GND_A", "GND_B"), false},
		{"bleeder 100k", tiePart("R1", "", "100k", "GND_A", "GND_B"), false},
		{"spark gap", tiePart("SG1", "SPARK-GAP", "", "GND_A", "GND_B"), false},
		{"unrecognised two-pad part", tiePart("Z1", "XYZ-123", "", "GND_A", "GND_B"), false},
		{"0R link", tiePart("R1", "", "0R", "GND_A", "GND_B"), true},
		{"10R", tiePart("R1", "", "10R", "GND_A", "GND_B"), true},
		{"resistor of unknown value", tiePart("R1", "RC0603FR-07XXX", "", "GND_A", "GND_B"), true},
		{"ferrite bead", tiePart("FB1", "BLM18", "", "GND_A", "GND_B"), true},
		{"net tie", tiePart("NT1", "NetTie_2", "", "GND_A", "GND_B"), true},
		{"solder jumper", tiePart("SJ1", "", "", "GND_A", "GND_B"), true},
	} {
		c, same := sameDomain(tieBoard(tc.part))
		if same != tc.joins {
			t.Errorf("%s: GND_A and GND_B joined = %v, want %v (%v)", tc.name, same, tc.joins, c.DomainOf)
		}
		if got := len(c.GroundTies) == 1 && c.GroundTies[0].Ref == tc.part.Ref; got != tc.joins {
			t.Errorf("%s: ground ties %+v", tc.name, c.GroundTies)
		}
		if tc.joins != (c.GroundDomain["GND_A"] == c.GroundDomain["GND_B"]) {
			t.Errorf("%s: GroundDomain %v", tc.name, c.GroundDomain)
		}
	}
	// Only a clearly meant tie is "sure"; an unknown-value resistor is not.
	c, _ := sameDomain(tieBoard(tiePart("R1", "", "0R", "GND_A", "GND_B")))
	if !c.GroundTies[0].Sure {
		t.Errorf("0R tie not sure: %+v", c.GroundTies)
	}
	c, _ = sameDomain(tieBoard(tiePart("R1", "RC0603FR-07XXX", "", "GND_A", "GND_B")))
	if c.GroundTies[0].Sure {
		t.Errorf("unknown-value tie is sure: %+v", c.GroundTies)
	}
}

// The line ties a ground through a conducting part, not through the bleeder
// or the Y capacitor that hangs on it.
func TestDomainLineTieSkipsBleederAndYCap(t *testing.T) {
	inlet := hvPart("J1", "TERM", Point{200, 1500}, thtPad("1", "AC_L", 0, 100, 80), thtPad("2", "AC_N", 0, -100, 80))
	for _, tc := range []struct {
		name string
		part *Part
		tied bool
	}{
		{"bleeder 2M2", tiePart("R1", "", "2M2", "AC_N", "GND_B"), false},
		{"Y capacitor", tiePart("CY1", "2.2nF", "", "AC_N", "GND_B"), false},
		{"10R", tiePart("R1", "", "10R", "AC_N", "GND_B"), true},
		{"resistor of unknown value", tiePart("R1", "RC0603FR-07XXX", "", "AC_N", "GND_B"), false},
		{"diode", tiePart("D1", "1N4007", "", "AC_N", "GND_B"), true},
	} {
		b := tieBoard(inlet, tc.part)
		c := Understand(b, Analyze(b, PowerSpec{}, nil))
		if tied := c.DomainOf["U2"] == c.DomainOf["J1"]; tied != tc.tied {
			t.Errorf("%s: GND_B on the line = %v, want %v (%v)", tc.name, tied, tc.tied, c.DomainOf)
		}
	}
}

// Declared domains win: grounds declared in different domains are not
// joined by a part between them (nor through a third ground), and a ground
// declared outside the line is not tied to it.
func TestDomainDeclaredGroundsStayApart(t *testing.T) {
	decl := PowerSpec{Declared: []DeclaredDomain{{Nets: []string{"GND_A", "VCC_A"}}, {Nets: []string{"GND_B"}}}}
	b := tieBoard(tiePart("R1", "", "0R", "GND_A", "GND_B"))
	c := Understand(b, Analyze(b, decl, nil))
	if c.DomainOf["U1"] == c.DomainOf["U2"] || len(c.GroundTies) != 0 {
		t.Fatalf("declared grounds joined: %v %+v", c.DomainOf, c.GroundTies)
	}
	if !strings.Contains(strings.Join(c.Notes, "\n"), "declared in different domains") {
		t.Errorf("no note about the refused tie: %v", c.Notes)
	}
	// Two declarations on the line are two domains all the same.
	both := PowerSpec{Declared: []DeclaredDomain{{Nets: []string{"GND_A", "VCC_A"}, Line: true}, {Nets: []string{"GND_B"}, Line: true}}}
	if c := Understand(b, Analyze(b, both, nil)); c.DomainOf["U1"] == c.DomainOf["U2"] || len(c.GroundTies) != 0 {
		t.Fatalf("grounds of two line declarations joined: %v %+v", c.DomainOf, c.GroundTies)
	}
	// Through a third, undeclared ground.
	r2 := tiePart("R2", "", "0R", "GND_C", "GND_B")
	b = tieBoard(tiePart("R1", "", "0R", "GND_A", "GND_C"), r2)
	c = Understand(b, Analyze(b, decl, nil))
	if c.DomainOf["U1"] == c.DomainOf["U2"] {
		t.Fatalf("declared grounds joined through GND_C: %v", c.DomainOf)
	}
	// Undeclared, the same parts do join them.
	if c := Understand(b, Analyze(b, PowerSpec{}, nil)); c.DomainOf["U1"] != c.DomainOf["U2"] {
		t.Fatalf("0R chain did not join the grounds: %v", c.DomainOf)
	}
	// The line.
	inlet := hvPart("J1", "TERM", Point{200, 1500}, thtPad("1", "AC_L", 0, 100, 80), thtPad("2", "AC_N", 0, -100, 80))
	b = tieBoard(inlet, tiePart("D1", "1N4007", "", "AC_N", "GND_B"))
	c = Understand(b, Analyze(b, decl, nil))
	if c.DomainOf["U2"] == c.DomainOf["J1"] {
		t.Fatalf("ground declared outside the line was tied to it: %v", c.DomainOf)
	}
	line := PowerSpec{Declared: []DeclaredDomain{{Nets: []string{"GND_B"}, Line: true}}}
	if c := Understand(b, Analyze(b, line, nil)); c.DomainOf["U2"] != c.DomainOf["J1"] {
		t.Fatalf("ground declared on the line was not tied to it: %v", c.DomainOf)
	}
	// A net with the line's name, declared in a domain that is not the
	// line, ties no ground to it: the control side stays apart.
	d3 := hvPart("D3", "1N4148", Point{2300, 1200}, smdPad("1", "N", -40, 0, 40, 40), smdPad("2", "GND_B", 40, 0, 40, 40))
	b = tieBoard(inlet, tiePart("D1", "1N4007", "", "AC_N", "GND_A"), d3)
	sensor := PowerSpec{Declared: []DeclaredDomain{{Nets: []string{"N"}}}}
	c = Understand(b, Analyze(b, sensor, nil))
	if c.DomainOf["U2"] == c.DomainOf["J1"] || c.DomainOf["U2"] == c.DomainOf["U1"] || c.DomainOf["U1"] != c.DomainOf["J1"] {
		t.Fatalf("a net named N, declared off the line, tied the control ground to it: %v", c.DomainOf)
	}
	for _, d := range c.Domains {
		if d.ID == c.DomainOf["U2"] && d.Mains {
			t.Fatalf("the control domain is mains: %+v", d)
		}
	}
	if c := Understand(b, Analyze(b, PowerSpec{}, nil)); c.DomainOf["U2"] != c.DomainOf["J1"] {
		t.Fatalf("fixture: undeclared, N does not tie GND_B to the line: %v", c.DomainOf)
	}
}

func TestClassifySafetyCapsAndTransformerPrefixes(t *testing.T) {
	for ref, want := range map[string]PartKind{"CY1": KindCapacitor, "CX2": KindCapacitor, "C3": KindCapacitor, "TX1": KindTransformer, "TR1": KindTransformer, "T1": KindTransformer} {
		p := hvPart(ref, "", Point{}, smdPad("1", "A", 0, 0, 10, 10), smdPad("2", "B", 20, 0, 10, 10))
		if got := ClassifyPart(p); got != want {
			t.Errorf("%s: %s, want %s", ref, got, want)
		}
	}
}

func TestClassifyResistorOhms(t *testing.T) {
	for s, want := range map[string]float64{"0R": 0, "0": 0, "10": 10, "10R": 10, "4R7": 4.7, "4K7": 4700, "4k7": 4700, "100k": 1e5, "330kΩ": 3.3e5,
		"1M": 1e6, "2M2": 2.2e6, "1 MΩ": 1e6, "10k 1%": 1e4, "0.1R": 0.1, "50m": 0.05, "47 ohm": 47} {
		if got, ok := ResistorOhms(s); !ok || got < want*0.999-1e-9 || got > want*1.001+1e-9 {
			t.Errorf("%q: %v %v, want %v", s, got, ok, want)
		}
	}
	for _, s := range []string{"", "RC0603FR-0710KL", "0603WAF1002T5E", "NTC", "DNP", "10nF"} {
		if got, ok := ResistorOhms(s); ok {
			t.Errorf("%q read as %v Ω", s, got)
		}
	}
}

// A declared domain with no ground net among its nets is a reference domain
// of its own: a Y capacitor from its return to another circuit's ground does
// not carry that circuit's domain over it. A rail declared inside a grounded
// domain seeds nothing.
func TestDomainDeclaredWithoutGroundSeedsItsOwn(t *testing.T) {
	parts := []*Part{
		hvPart("U3", "CTRL-P", Point{300, 1500}, smdPad("1", "VCC_P", 0, 50, 40, 40), smdPad("2", "HV_RTN", 0, -50, 40, 40), smdPad("3", "SIG_P", 50, 0, 40, 40)),
		hvPart("C3", "100nF", Point{300, 1800}, smdPad("1", "VCC_P", 0, 30, 40, 40), smdPad("2", "HV_RTN", 0, -30, 40, 40)),
		hvPart("Q1", "NMOS", Point{600, 1500}, smdPad("1", "SIG_P", 0, 50, 40, 40), smdPad("2", "HV_POS", 0, -50, 40, 40), smdPad("3", "HV_RTN", 50, 0, 40, 40)),
		hvPart("U_ISO2", "PC817", Point{1300, 1500}, smdPad("1", "SIG_P", -100, 50, 40, 40), smdPad("2", "HV_RTN", -100, -50, 40, 40),
			smdPad("3", "GND_B", 100, -50, 40, 40), smdPad("4", "SIG_B", 100, 50, 40, 40)),
		tiePart("CY1", "2.2nF Y1", "", "HV_RTN", "GND_B"),
	}
	b := tieBoard(parts...)
	free := Understand(b, Analyze(b, PowerSpec{}, nil))
	if free.DomainOf["U3"] != free.DomainOf["U2"] {
		t.Fatalf("fixture: undeclared, the Y capacitor no longer carries GND_B over the bus side: %v", free.DomainOf)
	}
	decl := PowerSpec{Declared: []DeclaredDomain{{Nets: []string{"HV_POS", "HV_RTN", "NOT_ON_BOARD"}, Line: true}}}
	c := Understand(b, Analyze(b, decl, nil))
	own := c.DomainOf["U3"]
	if own == "" || own == c.DomainOf["U2"] || own == c.DomainOf["U1"] {
		t.Fatalf("the declared bus side is not its own domain: %v", c.DomainOf)
	}
	for _, ref := range []string{"C3", "Q1"} {
		if c.DomainOf[ref] != own {
			t.Errorf("%s in %q, want %q", ref, c.DomainOf[ref], own)
		}
	}
	if c.DomainOf["C2"] != c.DomainOf["U2"] || c.DomainOf["U2"] == own {
		t.Errorf("the grounded side changed: %v", c.DomainOf)
	}
	if c.DeclaredNet["HV_RTN"] != own || c.DeclaredNet["HV_POS"] != own {
		t.Errorf("DeclaredNet %v, want both bus nets in %q", c.DeclaredNet, own)
	}
	if _, ok := c.DeclaredNet["NOT_ON_BOARD"]; ok {
		t.Errorf("a declared net that is not on the board got a domain: %v", c.DeclaredNet)
	}
	if !strings.Contains(strings.Join(c.Notes, "\n"), "declared domain without a ground net") {
		t.Errorf("no note: %v", c.Notes)
	}
	// A rail of a grounded domain, declared: the domain is the ground's.
	rail := PowerSpec{Declared: []DeclaredDomain{{Nets: []string{"VCC_B"}}}}
	c = Understand(b, Analyze(b, rail, nil))
	if c.DeclaredNet["VCC_B"] != c.DomainOf["U2"] || c.DomainOf["C2"] != c.DomainOf["U2"] || c.DomainOf["U1"] == c.DomainOf["U2"] {
		t.Errorf("a declared rail of a grounded domain: %v %v", c.DeclaredNet, c.DomainOf)
	}
}
