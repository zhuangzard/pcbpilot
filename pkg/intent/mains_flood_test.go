package intent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

func synPart(ref, mpn, value string, nets ...string) *powersim.Part {
	p := &powersim.Part{Ref: ref, MPN: mpn, Value: value}
	for i, n := range nets {
		name := fmt.Sprint(i + 1)
		if k := strings.IndexByte(n, '='); k > 0 { // "VDD=VCC_B": pin name = net
			name, n = n[:k], n[k+1:]
		}
		p.Pins = append(p.Pins, powersim.Pin{Number: fmt.Sprint(i + 1), Name: name, Net: n})
	}
	return p
}

// offlineDesign: an AC inlet through a fuse and a choke into a bridge
// rectifier (primary ground GND_A, bulk HV_BUS, a primary controller U3),
// an optocoupler to a control side with its own supply and ground (VCC_B /
// GND_B, MCU U1), and whatever extra parts a case puts across the gap.
func offlineDesign(extra ...*powersim.Part) *powersim.Design {
	d := &powersim.Design{NetRole: map[string]string{"GND_A": "ground", "GND_B": "ground"}}
	d.Parts = append(d.Parts,
		synPart("J1", "TERM-2P", "", "AC_L", "AC_N"),
		synPart("F1", "FUSE-T1A", "1A", "AC_L", "L_F"),
		synPart("L1", "CHOKE-1mH", "1mH", "L_F", "L_CH"),
		synPart("DB1", "MB6S", "", "L_CH", "AC_N", "HV_BUS", "GND_A"),
		synPart("C1", "", "10uF", "HV_BUS", "GND_A"),
		synPart("U3", "PRIMARY-CTRL", "", "VDD=VCC_A", "GND=GND_A", "FB=SIG_A", "HV=HV_BUS"),
		synPart("C3", "", "100nF", "VCC_A", "GND_A"),
		synPart("U_ISO1", "PC817", "", "SIG_A", "GND_A", "GND_B", "SIG_B"),
		synPart("J2", "TERM-2P", "", "VCC_B", "GND_B"),
		synPart("U1", "GENERIC-MCU", "", "VDD=VCC_B", "GND=GND_B", "ADC=ADC_IN", "IO=SIG_B", "IO2=LED_K"),
		synPart("C2", "", "100nF", "VCC_B", "GND_B"),
		synPart("R20", "", "1k", "VCC_B", "LED_A"),
		synPart("D2", "LED-RED", "", "LED_A", "LED_K"),
	)
	d.Parts = append(d.Parts, extra...)
	return d
}

// divider: a resistor string from the choke output down to the MCU's ADC
// pin, referenced to the control ground.
func divider(top, bottom string) []*powersim.Part {
	return []*powersim.Part{
		synPart("R1", "", top, "L_CH", "DIV1"),
		synPart("R2", "", top, "DIV1", "DIV2"),
		synPart("R3", "", top, "DIV2", "ADC_IN"),
		synPart("R4", "", bottom, "ADC_IN", "GND_B"),
	}
}

func deriveSyn(t *testing.T, d *powersim.Design, spec *Spec) *Intent {
	t.Helper()
	it, err := Derive(Input{Design: d, Libs: libs(t), Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func domainWith(it *Intent, ref string) *Domain {
	for _, d := range it.Domains {
		if has(d.Parts, ref) {
			return d
		}
	}
	return nil
}

func findingOf(it *Intent, kind string) *Finding {
	for _, f := range it.Findings {
		if f.Kind == kind {
			return f
		}
	}
	return nil
}

func isLine(it *Intent, net string) bool {
	np := it.Nets[net]
	return np != nil && strings.HasPrefix(np.NetClass, "HV_")
}

// controlApart checks that the control side (U1 on GND_B) is its own domain,
// not a mains one, with an insulation pair to the primary.
func controlApart(t *testing.T, name string, it *Intent) {
	t.Helper()
	pri, ctl := domainWith(it, "DB1"), domainWith(it, "U1")
	if pri == nil || ctl == nil || pri == ctl {
		t.Fatalf("%s: primary and control are not two domains: %+v", name, it.Domains)
	}
	if pri.Kind != "mains" || pri.Reference != "GND_A" {
		t.Errorf("%s: primary domain %+v", name, pri)
	}
	if ctl.Kind == "mains" || ctl.Kind == "hazardous" || ctl.Reference != "GND_B" || ctl.WorkingVpeak > selvDC {
		t.Errorf("%s: control domain %+v", name, ctl)
	}
	for _, n := range []string{"ADC_IN", "VCC_B", "GND_B", "SIG_B", "LED_A", "LED_K"} {
		if np := it.Nets[n]; np == nil || np.Domain != ctl.ID || isLine(it, n) {
			t.Errorf("%s: net %s: %+v, want domain %s and not a line net", name, n, np, ctl.ID)
		}
	}
	if len(it.Pairs) != 1 || !has(it.Pairs[0].Bridges, "U_ISO1") {
		t.Errorf("%s: pairs %+v", name, it.Pairs)
	}
	if f := findingOf(it, "domain-flood"); f != nil {
		t.Errorf("%s: flood warning on a board that did not flood: %s", name, f.Message)
	}
}

// (a) A sense divider from the line to a controller's ADC pin is not a path
// for the line: the control side behind the optocoupler stays its own
// low-voltage domain, whether or not the resistors carry a value. (A large
// mixed board derived one MAINS domain holding nearly every net: each R…
// part passed the line on through all its pins, and one line net among a
// domain's nets made the domain mains.)
func TestMainsStopsAtSenseDivider(t *testing.T) {
	for name, v := range map[string][2]string{"valued": {"1M", "10k"}, "no values": {"", ""}, "low bottom": {"330k", "4R7"}} {
		it := deriveSyn(t, offlineDesign(divider(v[0], v[1])...), nil)
		controlApart(t, name, it)
		for _, n := range []string{"DIV1", "DIV2", "ADC_IN"} {
			if isLine(it, n) {
				t.Errorf("%s: divider node %s became a line net", name, n)
			}
		}
	}
}

// (b) Genuine series parts still carry the line: fuse, two-terminal choke,
// a resistor known to be ≤ 10 Ω, both windings of a common-mode choke with
// two line conductors on it. A choke with one line conductor, a fuse into a
// net the spec declares SELV and a low resistor into a supply rail do not.
func TestMainsThroughSeriesParts(t *testing.T) {
	d := offlineDesign(
		synPart("R30", "", "4R7", "L_CH", "L_LIM"),
		synPart("CX1", "", "100nF", "L_LIM", "AC_N"),
		synPart("LF1", "CMC-10mH", "", "L_CH", "AC_N", "N_CM", "L_CM"),
		synPart("CX2", "", "100nF", "L_CM", "N_CM"),
		synPart("LF2", "CMC-DATA", "", "L_CH", "SENSE_P", "SENSE_N", "SENSE_Q"),
		synPart("R31", "", "10R", "L_CH", "SENSE_R"),
		synPart("R32", "", "0R", "L_CH", "VCC_B"),
		synPart("R33", "", "1k", "L_CH", "SENSE_T"),
	)
	it := deriveSyn(t, d, nil)
	for _, n := range []string{"AC_L", "AC_N", "L_F", "L_CH", "L_LIM", "L_CM", "N_CM", "SENSE_R"} {
		if !isLine(it, n) {
			t.Errorf("%s is not a line net: %+v", n, it.Nets[n])
		}
	}
	for _, n := range []string{"SENSE_P", "SENSE_N", "SENSE_Q", "SENSE_T", "VCC_B", "HV_BUS", "GND_A"} {
		if isLine(it, n) {
			t.Errorf("%s became a line net: %+v", n, it.Nets[n])
		}
	}
	if pri := domainWith(it, "DB1"); pri == nil || pri.Kind != "mains" || !has(pri.Parts, "F1") || !has(pri.Parts, "L1") {
		t.Errorf("primary domain %+v", pri)
	}
	// A net the spec declares in a SELV domain is not line, fuse or not.
	d = offlineDesign(synPart("F2", "FUSE-50mA", "", "L_CH", "SENSE_S"))
	it = deriveSyn(t, d, &Spec{Domains: []SpecDomain{{Kind: "SELV", Nets: []string{"SENSE_S"}}}})
	if isLine(it, "SENSE_S") || !isLine(it, "L_CH") {
		t.Errorf("declared SELV net behind a fuse: %+v", it.Nets["SENSE_S"])
	}
	// Nor is a net that only has the line's name, nor what a fuse behind it
	// reaches.
	d = offlineDesign(synPart("J3", "TERM-2P", "", "N", "GND_B"), synPart("F3", "FUSE-50mA", "", "N", "SENSE_U"), synPart("R34", "", "10k", "SENSE_U", "ADC_IN"))
	it = deriveSyn(t, d, &Spec{Domains: []SpecDomain{{Kind: "SELV", Nets: []string{"N"}}}})
	if isLine(it, "N") || isLine(it, "SENSE_U") || !isLine(it, "AC_N") {
		t.Errorf("declared SELV net named like the line: N %+v, SENSE_U %+v", it.Nets["N"], it.Nets["SENSE_U"])
	}
	// Declared windings: each winding carries its own conductor.
	d = offlineDesign(synPart("LF2", "CMC-DATA", "", "L_CH", "SENSE_P", "SENSE_N", "SENSE_Q"))
	it = deriveSyn(t, d, &Spec{Windings: map[string][][2]string{"LF2": {{"1", "4"}, {"2", "3"}}}})
	if !isLine(it, "SENSE_Q") || isLine(it, "SENSE_P") || isLine(it, "SENSE_N") {
		t.Errorf("declared windings: Q %v P %v N %v", isLine(it, "SENSE_Q"), isLine(it, "SENSE_P"), isLine(it, "SENSE_N"))
	}
}

// (c) A Y capacitor across the barrier — ground to ground, or line to the
// control ground — neither joins the grounds nor makes the control side
// mains, whatever prefix it carries.
func TestYCapacitorAcrossBarrierKeepsDomainsApart(t *testing.T) {
	for _, ref := range []string{"CY1", "C9"} {
		it := deriveSyn(t, offlineDesign(
			synPart(ref, "Y1-CAP", "2.2nF", "GND_A", "GND_B"),
			synPart("CY2", "Y1-CAP", "2.2nF", "AC_N", "GND_B"),
		), nil)
		controlApart(t, ref, it)
	}
}

// (d) A high-value bleeder across the barrier does not join the grounds, nor
// does one from the line to the control ground tie that ground to the line.
func TestBleederAcrossBarrierKeepsDomainsApart(t *testing.T) {
	it := deriveSyn(t, offlineDesign(
		synPart("R9", "", "4M7", "GND_A", "GND_B"),
		synPart("R10", "", "2M2", "AC_N", "GND_B"),
		synPart("R11", "", "100k", "HV_BUS", "GND_B"),
	), nil)
	controlApart(t, "bleeders", it)
}

// (e) When a doubtful tie does join the board into one mains domain although
// it carries an isolation part, a warning names the part; a 0 Ω link is a
// meant connection and raises none; a declared domain is never joined.
func TestDomainFloodWarning(t *testing.T) {
	flood := func() *powersim.Design {
		return offlineDesign(synPart("R9", "RES-0603-GENERIC", "", "GND_A", "GND_B"))
	}
	it := deriveSyn(t, flood(), nil)
	if domainWith(it, "U1") != domainWith(it, "DB1") {
		t.Fatalf("an unknown-value resistor between grounds no longer joins them: %+v", it.Domains)
	}
	f := findingOf(it, "domain-flood")
	if f == nil || f.Severity != "warn" || !has(f.Refs, "R9") || !has(f.Nets, "GND_A") || !has(f.Nets, "GND_B") {
		t.Fatalf("flood warning %+v", f)
	}
	for _, want := range []string{"R9 (joins GND_A and GND_B, value unknown", "2 grounds", "isolation parts"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("flood warning lacks %q: %s", want, f.Message)
		}
	}
	if !strings.Contains(f.Suggestion, "domains") {
		t.Errorf("suggestion %q", f.Suggestion)
	}
	// A second ground on the line (a signal net called AC_N on a control part).
	it = deriveSyn(t, offlineDesign(synPart("U5", "GENERIC-IC", "", "VDD=VCC_B", "GND=GND_B", "IN=AC_N")), nil)
	if f := findingOf(it, "domain-flood"); f == nil || !has(f.Refs, "U5") || !strings.Contains(f.Message, "ties GND_B to line net AC_N") {
		t.Errorf("flood through a second line-tied ground: %+v", f)
	}
	// A 0 Ω link: one circuit on purpose, no warning.
	it = deriveSyn(t, offlineDesign(synPart("R9", "", "0R", "GND_A", "GND_B")), nil)
	if domainWith(it, "U1") != domainWith(it, "DB1") {
		t.Fatalf("0R link does not join the grounds: %+v", it.Domains)
	}
	if f := findingOf(it, "domain-flood"); f != nil {
		t.Errorf("flood warning for a 0R link: %s", f.Message)
	}
	// Declared: the control ground is SELV, so nothing joins it to the primary.
	it = deriveSyn(t, flood(), &Spec{Domains: []SpecDomain{{Kind: "SELV", Nets: []string{"GND_B"}}}})
	pri, ctl := domainWith(it, "DB1"), domainWith(it, "U1")
	if pri == nil || ctl == nil || pri == ctl || ctl.Kind != "SELV" || pri.Kind != "mains" {
		t.Fatalf("declared SELV ground joined the primary: %+v / %+v", pri, ctl)
	}
	if f := findingOf(it, "domain-flood"); f != nil {
		t.Errorf("flood warning with declared domains: %s", f.Message)
	}
}

// groundlessDesign is offlineDesign with the primary's return on a bus net
// that is no ground (HV_RTN), and a Y capacitor from it to the control ground.
func groundlessDesign(extra ...*powersim.Part) *powersim.Design {
	d := offlineDesign(extra...)
	delete(d.NetRole, "GND_A")
	for _, p := range d.Parts {
		for i := range p.Pins {
			if p.Pins[i].Net == "GND_A" {
				p.Pins[i].Net = "HV_RTN"
			}
		}
	}
	d.Parts = append(d.Parts, synPart("C9", "Y1-CAP", "2.2nF", "HV_RTN", "GND_B"))
	return d
}

// (f) A declared domain whose return is no ground net is a domain of its
// own: the Y capacitor from its return to the control ground does not carry
// the control side's domain over the primary. (A large board declared its
// mains nets, none of them a ground; the derived mains domain then held
// nearly every net of the board, referenced to the secondary's ground.)
func TestDeclaredDomainWithoutGroundIsItsOwn(t *testing.T) {
	spec := &Spec{Domains: []SpecDomain{{Kind: "mains", Nets: []string{"AC_L", "AC_N", "HV_BUS", "HV_RTN"}}}}
	it := deriveSyn(t, groundlessDesign(), spec)
	pri, ctl := domainWith(it, "DB1"), domainWith(it, "U1")
	if pri == nil || ctl == nil || pri == ctl {
		t.Fatalf("primary and control are not two domains: %+v", it.Domains)
	}
	if pri.Kind != "mains" || has(pri.Nets, "GND_B") || has(pri.Nets, "VCC_B") || has(pri.Parts, "U1") {
		t.Errorf("primary domain %+v", pri)
	}
	if ctl.Kind == "mains" || ctl.Kind == "hazardous" || ctl.Reference != "GND_B" {
		t.Errorf("control domain %+v", ctl)
	}
	for _, n := range []string{"HV_RTN", "HV_BUS", "VCC_A", "SIG_A"} {
		if np := it.Nets[n]; np == nil || np.Domain != pri.ID {
			t.Errorf("net %s: %+v, want domain %s", n, np, pri.ID)
		}
	}
	for _, n := range []string{"ADC_IN", "VCC_B", "GND_B", "SIG_B"} {
		if np := it.Nets[n]; np == nil || np.Domain != ctl.ID || isLine(it, n) {
			t.Errorf("net %s: %+v, want domain %s and not a line net", n, np, ctl.ID)
		}
	}
	if len(it.Pairs) != 1 || !has(it.Pairs[0].Bridges, "U_ISO1") {
		t.Errorf("pairs %+v", it.Pairs)
	}
	// A rail declared to give a grounded domain its kind seeds nothing new.
	it = deriveSyn(t, offlineDesign(), &Spec{Domains: []SpecDomain{{Kind: "SELV", Nets: []string{"VCC_B"}}}})
	if ctl := domainWith(it, "U1"); ctl == nil || ctl.Kind != "SELV" || ctl.Reference != "GND_B" || !has(ctl.Parts, "C2") {
		t.Errorf("a declared rail of a grounded domain: %+v", ctl)
	}
	controlApartDeclared := domainWith(it, "DB1")
	if controlApartDeclared == nil || controlApartDeclared.Kind != "mains" || controlApartDeclared.Reference != "GND_A" {
		t.Errorf("primary beside a declared rail: %+v", controlApartDeclared)
	}
}
