package kicad

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

// rvT builds a synthetic netlist: parts with typed library pins, nets as
// lists of "REF.PIN". All names are synthetic.
type rvT struct {
	nl  SchNetlist
	typ map[string]string
}

func newRvT() *rvT { return &rvT{typ: map[string]string{}} }

// part adds a component; pins are "NUM:NAME:TYPE".
func (b *rvT) part(ref, lib, value string, pins ...string) *rvT {
	libName, part, _ := strings.Cut(lib, ":")
	c := SchNetComp{Ref: ref, Value: value, Lib: libName, Part: part, Sheet: "/", Fields: map[string]string{}}
	for _, p := range pins {
		f := strings.SplitN(p, ":", 3)
		c.Pins = append(c.Pins, SchNetPin{Number: f[0], Name: f[1], Type: f[2]})
		b.typ[ref+"."+f[0]] = f[2]
	}
	b.nl.Components = append(b.nl.Components, c)
	return b
}

func (b *rvT) field(ref, k, v string) *rvT {
	for i := range b.nl.Components {
		if b.nl.Components[i].Ref == ref {
			b.nl.Components[i].Fields[k] = v
		}
	}
	return b
}

func (b *rvT) net(name string, nodes ...string) *rvT {
	n := SchNet{Code: fmt.Sprint(len(b.nl.Nets) + 1), Name: name}
	for _, x := range nodes {
		ref, pin, _ := strings.Cut(x, ".")
		fn := ""
		for _, c := range b.nl.Components {
			if c.Ref == ref {
				for _, p := range c.Pins {
					if p.Number == pin {
						fn = p.Name
					}
				}
			}
		}
		n.Nodes = append(n.Nodes, SchNetNode{Ref: ref, Pin: pin, Function: fn, Type: b.typ[x]})
	}
	b.nl.Nets = append(b.nl.Nets, n)
	return b
}

// mcu adds an IC powered from rail with a ground pin and the given extra
// pins; its supply is decoupled by a 10 µF cap unless bare.
func (b *rvT) ic(ref, rail string, pins ...string) *rvT {
	all := append([]string{"1:VDD:power_in", "2:GND:power_in"}, pins...)
	b.part(ref, "Synth:IC", "SYN-IC", all...)
	b.addNode(rail, ref+".1")
	b.addNode("GND", ref+".2")
	return b
}

func (b *rvT) addNode(net, node string) {
	for i := range b.nl.Nets {
		if b.nl.Nets[i].Name == net {
			ref, pin, _ := strings.Cut(node, ".")
			b.nl.Nets[i].Nodes = append(b.nl.Nets[i].Nodes, SchNetNode{Ref: ref, Pin: pin, Type: b.typ[node]})
			return
		}
	}
	b.net(net, node)
}

func (b *rvT) cap(ref, value, a, c string) *rvT {
	b.part(ref, "Device:C", value, "1:~:passive", "2:~:passive")
	b.addNode(a, ref+".1")
	b.addNode(c, ref+".2")
	return b
}

func (b *rvT) res(ref, value, a, c string) *rvT {
	b.part(ref, "Device:R", value, "1:~:passive", "2:~:passive")
	b.addNode(a, ref+".1")
	b.addNode(c, ref+".2")
	return b
}

func (b *rvT) review(t *testing.T, h ReviewHints, mod ...func(*ReviewInput)) *SchReview {
	t.Helper()
	in := ReviewInput{Netlist: &b.nl, ERC: &ERCReport{}, HierRan: true, Hints: h}
	for _, m := range mod {
		m(&in)
	}
	r, err := Review(in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rvCheckOf(t *testing.T, r *SchReview, id string) ReviewCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s", id)
	return ReviewCheck{}
}

// want asserts a check's status and the severities of its findings.
func want(t *testing.T, r *SchReview, id, status string, sev ...string) ReviewCheck {
	t.Helper()
	c := rvCheckOf(t, r, id)
	var got []string
	for _, f := range c.Findings {
		got = append(got, f.Severity)
	}
	if c.Status != status || strings.Join(got, ",") != strings.Join(sev, ",") {
		t.Fatalf("%s: status %s findings %v, want %s %v\n%+v", id, c.Status, got, status, sev, c)
	}
	return c
}

func intentHints() ReviewHints { return ReviewHints{Intent: "intent.json"} }

// completeHints: intent that declares its internal-pull list complete —
// the explicit evidence an unlisted pin has no internal pull.
func completeHints() ReviewHints {
	return ReviewHints{Intent: "intent.json", PullsComplete: "intent review.internalPullsComplete"}
}

// base: a decoupled 3.3 V rail, so the power checks stay quiet.
func rvBase() *rvT {
	b := newRvT()
	b.net("+3V3")
	b.net("GND")
	return b
}

func TestReviewI2C(t *testing.T) {
	bus := func() *rvT {
		b := rvBase().ic("U1", "+3V3", "3:SDA:bidirectional", "4:SCL:bidirectional").ic("U2", "+3V3", "3:SDA:bidirectional", "4:SCL:bidirectional")
		b.cap("C1", "10u", "+3V3", "GND").cap("C2", "100n", "+3V3", "GND")
		b.net("/SDA", "U1.3", "U2.3").net("/SCL", "U1.4", "U2.4")
		return b
	}
	// positive: no pull-ups — proven by pin names
	r := bus().review(t, ReviewHints{})
	c := want(t, r, RvI2CPullup, "fail", "error", "error")
	if c.Findings[0].Certainty != "proven" || !strings.Contains(strings.Join(c.Findings[0].Evidence, " "), "pin name") {
		t.Fatalf("evidence %+v", c.Findings[0])
	}
	if r.Status != gate.Fail {
		t.Fatalf("status %s", r.Status)
	}
	// negative: pull-ups, one through a 0 Ω link
	b := bus().res("R1", "4k7", "/SDA", "+3V3").res("R9", "0R", "/SCL", "/SCL_B").res("R2", "4k7", "/SCL_B", "+3V3")
	want(t, b.review(t, ReviewHints{}), RvI2CPullup, "pass")
	// off-board: leaves through a connector → info
	b = bus()
	b.part("J1", "Connector:Conn_01x02", "", "1:Pin_1:passive", "2:Pin_2:passive")
	b.addNode("/SDA", "J1.1")
	b.addNode("/SCL", "J1.2")
	want(t, b.review(t, ReviewHints{}), RvI2CPullup, "pass", "info", "info")
	// heuristic: recognised by the net name only → warning, not error
	b = rvBase().ic("U1", "+3V3", "3:IO1:bidirectional").ic("U2", "+3V3", "3:IO1:bidirectional").cap("C1", "10u", "+3V3", "GND")
	b.net("/I2C_SDA", "U1.3", "U2.3")
	c = want(t, b.review(t, ReviewHints{}), RvI2CPullup, "pass", "warning")
	if c.Findings[0].Certainty != "heuristic" {
		t.Fatal(c.Findings[0])
	}
	// SCLK (SPI) is not I2C
	b = rvBase().ic("U1", "+3V3", "3:SCLK:output").ic("U2", "+3V3", "3:SCLK:input").cap("C1", "10u", "+3V3", "GND")
	b.net("/SPI_SCLK", "U1.3", "U2.3")
	want(t, b.review(t, ReviewHints{}), RvI2CPullup, "not-applicable")
}

func odDesign() *rvT {
	b := rvBase().ic("U1", "+3V3", "3:~{INT}:open_collector").ic("U2", "+3V3", "3:PA0:input").cap("C1", "10u", "+3V3", "GND")
	b.net("/SENSOR_INT", "U1.3", "U2.3")
	return b
}

func TestReviewOpenDrain(t *testing.T) {
	// no intent: internal pull-ups unknown → a likely warning that names
	// the missing evidence; the review passes with warnings, never "pass"
	r := odDesign().review(t, ReviewHints{})
	c := want(t, r, RvOpenDrain, "pass", "warning")
	if c.Findings[0].Certainty != "likely" || len(c.Findings[0].Missing) == 0 || r.Verdict != "pass-with-warnings" || r.Status.ExitCode() != 0 {
		t.Fatalf("%+v %s", c, r.Verdict)
	}
	// an intent.json is no evidence about internal pulls: legacy (no
	// review{}) and unrelated review notes stay a likely warning
	want(t, odDesign().review(t, intentHints()), RvOpenDrain, "pass", "warning")
	unrelated := intentHints()
	unrelated.PullUp = map[string]string{"U9.1": "intent review.internalPullups"}
	want(t, odDesign().review(t, unrelated), RvOpenDrain, "pass", "warning")
	// explicit evidence: the pull list is complete, or the pins have none
	want(t, odDesign().review(t, completeHints()), RvOpenDrain, "fail", "error")
	none := intentHints()
	none.PullNone = map[string]string{"U1.3": "intent review.noInternalPulls", "U2.3": "intent review.noInternalPulls"}
	want(t, odDesign().review(t, none), RvOpenDrain, "fail", "error")
	// internal pull-up declared in intent → exempt, source named
	h := intentHints()
	h.PullUp = map[string]string{"U2.PA0": "intent review.internalPullups"}
	c = want(t, odDesign().review(t, h), RvOpenDrain, "pass")
	if len(c.Exempt) != 1 || c.Exempt[0].Source == "" {
		t.Fatal(c.Exempt)
	}
	// internal pull-up from a component field (library evidence)
	c = want(t, odDesign().field("U2", "InternalPullup", "3").review(t, ReviewHints{}), RvOpenDrain, "pass")
	if !strings.Contains(c.Exempt[0].Source, "component field") {
		t.Fatal(c.Exempt)
	}
	// external pull-up → pass
	want(t, odDesign().res("R1", "10k", "/SENSOR_INT", "+3V3").review(t, ReviewHints{}), RvOpenDrain, "pass")
	// an open-drain interrupt into an MCU GPIO: its direction is unknown —
	// not a push-pull driver; a likely warning, an error with pull
	// evidence, exempt only when intent declares the GPIO push-pull
	g := rvBase().ic("U1", "+3V3", "3:~{INT}:open_collector").ic("U2", "+3V3", "3:PB3:bidirectional").cap("C1", "10u", "+3V3", "GND")
	g.net("/ALERT", "U1.3", "U2.3")
	want(t, g.review(t, intentHints()), RvOpenDrain, "pass", "warning")
	want(t, g.review(t, completeHints()), RvOpenDrain, "fail", "error")
	pp := completeHints()
	pp.PushPull = map[string]string{"U2.PB3": "intent review.pushPullOutputs"}
	want(t, g.review(t, pp), RvOpenDrain, "pass")
	// a push-pull output on the net → not flagged
	b := odDesign()
	b.part("U3", "Synth:SUP", "SYN-SUP", "1:RST:output")
	b.addNode("/SENSOR_INT", "U3.1")
	c = want(t, b.review(t, intentHints()), RvOpenDrain, "pass")
	if len(c.Exempt) != 1 || !strings.Contains(c.Exempt[0].Reason, "push-pull") {
		t.Fatal(c.Exempt)
	}
	// an open-drain pin sinking an LED: the LED (and its resistor) is the load
	b = odDesign()
	b.part("D1", "Device:LED", "LED", "1:K:passive", "2:A:passive")
	b.addNode("/SENSOR_INT", "D1.1")
	b.res("R5", "1k", "/LA", "+3V3")
	b.addNode("/LA", "D1.2")
	want(t, b.review(t, completeHints()), RvOpenDrain, "pass")
	// a diode whose far end goes to ground is no pull-up
	b = odDesign()
	b.part("D2", "Device:D", "1N4148", "1:K:passive", "2:A:passive")
	b.addNode("/SENSOR_INT", "D2.1")
	b.addNode("GND", "D2.2")
	want(t, b.review(t, completeHints()), RvOpenDrain, "fail", "error")
	// an LED path that is reverse-biased toward the supply (cathode at the
	// resistor) does not pull the line up
	b = odDesign()
	b.part("D1", "Device:LED", "LED", "1:K:passive", "2:A:passive")
	b.addNode("/SENSOR_INT", "D1.2")
	b.res("R5", "1k", "/LK", "+3V3")
	b.addNode("/LK", "D1.1")
	want(t, b.review(t, completeHints()), RvOpenDrain, "fail", "error")
	// a diode whose terminals cannot be told is unresolved topology
	b = odDesign()
	b.part("D3", "Device:D", "D", "1:1:passive", "2:2:passive")
	b.addNode("/SENSOR_INT", "D3.1")
	b.res("R6", "1k", "/DK", "+3V3")
	b.addNode("/DK", "D3.2")
	want(t, b.review(t, completeHints()), RvOpenDrain, "cannot-check")
	// unknown jumper on the path → cannot-check
	b = odDesign()
	b.part("JP1", "Jumper:Jumper_2_Open", "", "1:A:passive", "2:B:passive")
	b.addNode("/SENSOR_INT", "JP1.1")
	b.addNode("/X", "JP1.2")
	want(t, b.review(t, intentHints()), RvOpenDrain, "cannot-check")
}

// TestReviewPushPullReset: a reset line driven by a push-pull supervisor
// output is no finding in any connection check.
func TestReviewPushPullReset(t *testing.T) {
	b := rvBase().ic("U1", "+3V3", "3:NRST:input").cap("C1", "10u", "+3V3", "GND")
	b.part("U2", "Synth:SUP", "SYN-SUP", "1:VDD:power_in", "2:GND:power_in", "3:RST:output")
	b.addNode("+3V3", "U2.1")
	b.addNode("GND", "U2.2")
	b.net("/NRST", "U1.3", "U2.3")
	r := b.review(t, ReviewHints{})
	for _, id := range []string{RvOpenDrain, RvFloatingInput, RvOutputConflict} {
		if c := rvCheckOf(t, r, id); len(c.Findings) > 0 || c.Status == "fail" || c.Status == "cannot-check" {
			t.Fatalf("%s: %+v", id, c)
		}
	}
}

func TestReviewPullupVoltage(t *testing.T) {
	mk := func() *rvT {
		b := rvBase().ic("U1", "+3V3", "3:SDA:bidirectional").cap("C1", "10u", "+3V3", "GND")
		b.net("+5V")
		b.ic("U9", "+5V").cap("C9", "10u", "+5V", "GND")
		b.ic("U2", "+5V", "3:SDA:bidirectional")
		b.net("/SDA", "U1.3", "U2.3").res("R1", "4k7", "/SDA", "+5V")
		return b
	}
	h := ReviewHints{Intent: "i", NetVoltage: map[string]float64{"+3V3": 3.3, "+5V": 5}, VoltageSource: map[string]string{"+5V": "intent i"}}
	// the pin's abs-max unknown → likely (warning), not proven
	c := want(t, mk().review(t, h), RvPullupVoltage, "pass", "warning")
	if !strings.Contains(c.Findings[0].Message, "U1") || c.Findings[0].Certainty != "likely" {
		t.Fatal(c.Findings[0])
	}
	// a declared maximum makes it proven
	h.PinMaxV = map[string]float64{"U1.SDA": 3.6}
	want(t, mk().review(t, h), RvPullupVoltage, "fail", "error")
	h.PinMaxV = nil
	// a multi-rail part: the pin's domain must be declared
	m := mk()
	m.part("U3", "Synth:MCU", "", "1:VDD:power_in", "2:VIO:power_in", "3:GND:power_in", "4:SDA:bidirectional")
	m.addNode("+3V3", "U3.1")
	m.addNode("+5V", "U3.2")
	m.addNode("GND", "U3.3")
	m.addNode("/SDA", "U3.4")
	// multi-rail part, domain undeclared: overvoltage not evaluated (likely)
	c = want(t, m.review(t, h), RvPullupVoltage, "pass", "warning", "warning")
	// pulled up to its own declared supply: structurally matched, no numbers
	hd := h
	hd.PinDomain = map[string]string{"U3.4": "+5V"}
	want(t, m.review(t, hd), RvPullupVoltage, "pass", "warning")
	// a pull-up to the pin's own supply needs no voltages at all
	own := rvBase().ic("U1", "+3V3", "3:SDA:bidirectional").ic("U2", "+3V3", "3:SDA:bidirectional").cap("C1", "10u", "+3V3", "GND")
	own.net("/SDA", "U1.3", "U2.3").res("R1", "4k7", "/SDA", "+3V3")
	want(t, own.review(t, ReviewHints{}), RvPullupVoltage, "pass")
	h.Tolerant = map[string]string{"U1.3": "intent review.tolerantPins"}
	want(t, mk().review(t, h), RvPullupVoltage, "pass")
	// different domain, no voltages → likely warning "not evaluated"
	c = want(t, mk().review(t, ReviewHints{}), RvPullupVoltage, "pass", "warning")
	if !strings.Contains(c.Findings[0].Message, "not evaluated") || len(c.Findings[0].Missing) == 0 {
		t.Fatal(c.Findings[0])
	}
}

func TestReviewLED(t *testing.T) {
	led := func(b *rvT, ref, a, k string) {
		b.part(ref, "Device:LED", "LED", "1:K:passive", "2:A:passive")
		b.addNode(k, ref+".1")
		b.addNode(a, ref+".2")
	}
	// directly across the rail → error
	b := rvBase()
	led(b, "D1", "+3V3", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	// series resistor → pass; a 0 Ω does not count
	b = rvBase().res("R1", "1k", "+3V3", "/LA")
	led(b, "D1", "/LA", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "pass")
	b = rvBase().res("R1", "0", "+3V3", "/LA")
	led(b, "D1", "/LA", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	// two LEDs in series, one resistor → both pass
	b = rvBase().res("R1", "330", "+3V3", "/LA")
	led(b, "D1", "/LA", "/LM")
	led(b, "D2", "/LM", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "pass")
	// switched by a transistor with no resistor → ambiguous
	b = rvBase()
	b.part("Q1", "Transistor_FET:Q_NMOS_GDS", "", "1:G:input", "2:D:passive", "3:S:passive")
	b.addNode("/LK", "Q1.2")
	b.addNode("GND", "Q1.3")
	led(b, "D1", "+3V3", "/LK")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "cannot-check")
	// a pin only named like a current driver → likely warning; an intent
	// review.currentDrivers role → pass
	b = rvBase().ic("U1", "+3V3", "3:LED1:open_collector").cap("C1", "10u", "+3V3", "GND")
	led(b, "D1", "+3V3", "/LK")
	b.addNode("/LK", "U1.3")
	c := want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "pass", "warning")
	if c.Findings[0].Certainty != "likely" {
		t.Fatal(c.Findings[0])
	}
	want(t, b.review(t, ReviewHints{CurrentDriver: map[string]string{"U1.3": "intent review.currentDrivers"}}), RvLEDLimiter, "pass")
	// a series resistor whose far end goes nowhere → no complete path
	b = rvFed().res("R1", "1k", "/LA", "/NOWHERE")
	led(b, "D1", "/LA", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	// a series resistor whose far end branches → ambiguous
	b = rvFed().res("R1", "1k", "/LA", "/MID").res("R2", "1k", "/MID", "+3V3").res("R3", "1k", "/MID", "GND")
	led(b, "D1", "/LA", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "cannot-check")
	// off-board: to a connector → info
	b = rvBase()
	b.part("J1", "Connector:Conn_01x01", "", "1:Pin_1:passive")
	b.net("/LK", "J1.1")
	led(b, "D1", "+3V3", "/LK")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "pass", "info")
	// a connector feeding the rail is the source, not an off-board limiter
	b = rvFed()
	led(b, "D1", "+3V3", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	// straight across the rail with an unrelated bleeder and pull-up on it:
	// they are not in the LED's path
	b = rvFed().res("R7", "10k", "+3V3", "GND").res("R8", "4k7", "+3V3", "/SOME_PU")
	led(b, "D1", "+3V3", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	// a bleeder on the LED's own node: the node branches → ambiguous
	b = rvFed().res("R1", "1k", "+3V3", "/LA").res("R9", "100k", "/LA", "GND")
	led(b, "D1", "/LA", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "cannot-check")
	// parallel branches: D1 has its resistor, D2 sits straight across the
	// rail and does not inherit it
	b = rvFed().res("R1", "1k", "+3V3", "/LA")
	led(b, "D1", "/LA", "GND")
	led(b, "D2", "+3V3", "GND")
	c = want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	if c.Findings[0].Refs[0] != "D2" {
		t.Fatal(c.Findings[0])
	}
	// two LEDs sharing one resistor: the shared node branches → ambiguous
	b = rvFed().res("R1", "330", "+3V3", "/LA")
	led(b, "D1", "/LA", "GND")
	led(b, "D2", "/LA", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "cannot-check")
	// an LED driven straight from a GPIO pin → error
	b = rvFed().ic("U1", "+3V3", "3:PA5:bidirectional").cap("C1", "10u", "+3V3", "GND")
	b.net("/LED", "U1.3")
	led(b, "D1", "/LED", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
}

func TestReviewFloatingInput(t *testing.T) {
	mk := func() *rvT {
		b := rvBase().ic("U1", "+3V3", "3:EN:input").ic("U2", "+3V3", "3:CE:input").cap("C1", "10u", "+3V3", "GND")
		b.net("/EN", "U1.3", "U2.3")
		return b
	}
	// no intent, legacy intent: internal pulls unknown → likely warning
	want(t, mk().review(t, ReviewHints{}), RvFloatingInput, "pass", "warning")
	want(t, mk().review(t, intentHints()), RvFloatingInput, "pass", "warning")
	want(t, mk().review(t, completeHints()), RvFloatingInput, "fail", "error")
	h := completeHints()
	h.PullDown = map[string]string{"U1.EN": "intent review.internalPulldowns"}
	want(t, mk().review(t, h), RvFloatingInput, "pass")
	// a resistor to a rail drives it
	want(t, mk().res("R1", "10k", "/EN", "+3V3").review(t, completeHints()), RvFloatingInput, "pass")
	// passives that do not drive: a dangling resistor, a capacitor to ground
	want(t, mk().res("R1", "10k", "/EN", "/NOWHERE").review(t, completeHints()), RvFloatingInput, "fail", "error")
	want(t, mk().cap("C7", "100n", "/EN", "GND").review(t, completeHints()), RvFloatingInput, "fail", "error")
	// a 0 Ω link between two inputs joins them; still nothing drives them
	z := rvBase().ic("U1", "+3V3", "3:EN:input").ic("U2", "+3V3", "3:CE:input").cap("C1", "10u", "+3V3", "GND")
	z.net("/EN_A", "U1.3").net("/EN_B", "U2.3").res("R0", "0", "/EN_A", "/EN_B")
	c := want(t, z.review(t, completeHints()), RvFloatingInput, "fail", "error")
	if len(c.Findings[0].Pins) != 2 {
		t.Fatal(c.Findings[0])
	}
	// a GPIO of undeclared direction: drive not verified (likely); declared
	// push-pull → driven
	gp := rvBase().ic("U1", "+3V3", "3:EN:input").ic("U2", "+3V3", "3:PB0:bidirectional").cap("C1", "10u", "+3V3", "GND")
	gp.net("/EN", "U1.3", "U2.3")
	c = want(t, gp.review(t, completeHints()), RvFloatingInput, "pass", "warning")
	if !strings.Contains(c.Findings[0].Message, "not verified") {
		t.Fatal(c.Findings[0])
	}
	pp := completeHints()
	pp.PushPull = map[string]string{"U2.3": "intent review.pushPullOutputs"}
	want(t, gp.review(t, pp), RvFloatingInput, "pass")
	// a reverse-biased diode from +3V3 (anode at the input) does not drive it
	rd := mk()
	rd.part("D1", "Device:D", "D", "1:K:passive", "2:A:passive")
	rd.addNode("+3V3", "D1.1")
	rd.addNode("/EN", "D1.2")
	want(t, rd.review(t, completeHints()), RvFloatingInput, "fail", "error")
	// forward-biased from +3V3 (cathode at the input) it does
	fd := mk()
	fd.part("D1", "Device:D", "D", "1:K:passive", "2:A:passive")
	fd.addNode("/EN", "D1.1")
	fd.addNode("+3V3", "D1.2")
	want(t, fd.review(t, completeHints()), RvFloatingInput, "pass")
	// passive pins (an FPGA pin typed passive) are no receivers
	pv := rvBase().ic("U1", "+3V3", "3:IO:passive").ic("U2", "+3V3", "3:IO:passive").cap("C1", "10u", "+3V3", "GND")
	pv.net("/X", "U1.3", "U2.3")
	want(t, pv.review(t, completeHints()), RvFloatingInput, "not-applicable")
	ip := completeHints()
	ip.InputPin = map[string]string{"U1.3": "intent review.inputPins"}
	want(t, pv.review(t, ip), RvFloatingInput, "pass", "warning")
}

func TestReviewOutputConflict(t *testing.T) {
	b := rvBase().ic("U1", "+3V3", "3:Y:output").ic("U2", "+3V3", "3:Y:output", "4:Y2:output").cap("C1", "10u", "+3V3", "GND")
	b.net("/CLK", "U1.3", "U2.3")
	want(t, b.review(t, ReviewHints{}), RvOutputConflict, "fail", "error")
	// two outputs that only share a name (a dual op-amp's OUT A and OUT B)
	// contend; pins the symbol stacks, or intent declares common, do not
	b = rvBase().ic("U1", "+3V3", "3:OUT:output", "4:OUT:output").cap("C1", "10u", "+3V3", "GND")
	b.net("/CLK", "U1.3", "U1.4")
	want(t, b.review(t, ReviewHints{}), RvOutputConflict, "fail", "error")
	want(t, b.review(t, ReviewHints{}, func(in *ReviewInput) { in.Stacked = map[string][][]string{"Synth:IC": {{"3", "4"}}} }), RvOutputConflict, "pass")
	want(t, b.review(t, ReviewHints{CommonPins: [][]string{{"U1.3", "U1.4"}}}), RvOutputConflict, "pass")
	// two independent outputs of one package (1Y, 2Y of a buffer) contend
	b = rvBase().ic("U1", "+3V3", "3:1Y:output", "4:2Y:output").cap("C1", "10u", "+3V3", "GND")
	b.net("/CLK", "U1.3", "U1.4")
	want(t, b.review(t, ReviewHints{}), RvOutputConflict, "fail", "error")
	// PWR_FLAG on a regulator output is virtual: no contention
	b = rvBase().cap("C1", "10u", "+3V3", "GND").cap("C2", "10u", "/VIN", "GND")
	b.part("U5", "Regulator_Linear:SYN-LDO", "SYN-LDO", "1:GND:power_in", "2:VO:power_out", "3:VI:power_in")
	b.part("#FLG01", "power:PWR_FLAG", "PWR_FLAG", "1:pwr:power_out")
	b.addNode("GND", "U5.1")
	b.addNode("+3V3", "U5.2")
	b.addNode("+3V3", "#FLG01.1")
	b.addNode("/VIN", "U5.3")
	r := b.review(t, ReviewHints{})
	want(t, r, RvOutputConflict, "pass")
	want(t, r, RvRegOutputCap, "pass")
}

func TestReviewICPowerGround(t *testing.T) {
	b := rvBase().cap("C1", "10u", "+3V3", "GND")
	b.part("U1", "Synth:IC", "", "1:VDD:power_in", "2:A:input", "3:B:output")
	b.addNode("+3V3", "U1.1")
	b.net("/A", "U1.2")
	b.net("/B", "U1.3")
	c := want(t, b.review(t, ReviewHints{}), RvICPowerGround, "fail", "error")
	if !strings.Contains(c.Findings[0].Message, "ground") {
		t.Fatal(c.Findings)
	}
	want(t, rvBase().ic("U1", "+3V3").cap("C1", "10u", "+3V3", "GND").review(t, ReviewHints{}), RvICPowerGround, "pass")
	// a ground pin typed input in its library is still the ground pin
	g := rvBase().cap("C1", "10u", "+3V3", "GND")
	g.part("U2", "Synth:REG", "", "5:Vin:power_in", "7:GND:input")
	g.addNode("+3V3", "U2.5")
	g.addNode("GND", "U2.7")
	want(t, g.review(t, ReviewHints{}), RvICPowerGround, "pass")
	// a TVS whose library name merely contains "led" (Filled) is no LED
	tv := rvFed()
	tv.part("D9", "Synth:D_TVS_Filled", "5V", "1:A1:passive", "2:A2:passive")
	tv.addNode("+3V3", "D9.1")
	tv.addNode("GND", "D9.2")
	want(t, tv.review(t, ReviewHints{}), RvLEDLimiter, "not-applicable")
}

func TestReviewDecoupling(t *testing.T) {
	// no capacitor → error
	r := rvBase().ic("U1", "+3V3").review(t, ReviewHints{})
	want(t, r, RvDecoupling, "fail", "error")
	// one cap → pass, the set is named in coverage (no finding)
	want(t, rvBase().ic("U1", "+3V3").cap("C1", "100n", "+3V3", "GND").cap("C2", "10u", "+3V3", "GND").review(t, ReviewHints{}), RvDecoupling, "pass")
	// one cap shared by two ICs → warning (shared coverage)
	b := rvBase().ic("U1", "+3V3").ic("U2", "+3V3").cap("C1", "10u", "+3V3", "GND")
	c := want(t, b.review(t, ReviewHints{}), RvDecoupling, "pass", "warning", "warning")
	if !strings.Contains(c.Findings[0].Message, "C1") {
		t.Fatal(c.Findings[0])
	}
	// reference rail declared → exempt with source; low current alone does
	// not exempt (no hint for it exists)
	b = rvBase().ic("U1", "+3V3")
	b.net("/VREF")
	b.part("U2", "Synth:ADC", "", "1:REF:power_in", "2:GND:power_in")
	b.addNode("/VREF", "U2.1")
	b.addNode("GND", "U2.2")
	b.cap("C1", "10u", "+3V3", "GND")
	b.res("R1", "10k", "+3V3", "/VREF")
	r = b.review(t, ReviewHints{})
	want(t, r, RvDecoupling, "fail", "error")
	h := ReviewHints{ReferenceRail: map[string]string{"/VREF": "intent review.referenceRails"}}
	c = want(t, b.review(t, h), RvDecoupling, "pass")
	if len(c.Exempt) != 1 || c.Exempt[0].Source != "intent review.referenceRails" {
		t.Fatal(c.Exempt)
	}
	// module with integrated decoupling (component field)
	m := rvBase().ic("U1", "+3V3").field("U1", "ReviewRole", "module")
	want(t, m.review(t, ReviewHints{}), RvDecoupling, "pass")
}

func regDesign(outCap, inCap bool) *rvT {
	b := rvBase().ic("U1", "+3V3")
	b.net("+5V")
	b.part("U5", "Regulator_Linear:SYN-LDO", "SYN-LDO", "1:GND:power_in", "2:VO:power_out", "3:VI:power_in")
	b.addNode("GND", "U5.1")
	b.addNode("+3V3", "U5.2")
	b.addNode("+5V", "U5.3")
	b.part("J1", "Connector:Conn_01x02", "", "1:Pin_1:passive", "2:Pin_2:passive")
	b.addNode("+5V", "J1.1")
	b.addNode("GND", "J1.2")
	if outCap {
		b.cap("C1", "10u", "+3V3", "GND")
	}
	if inCap {
		b.cap("C2", "10u", "+5V", "GND")
	}
	return b
}

func TestReviewRegulatorCaps(t *testing.T) {
	r := regDesign(false, true).review(t, ReviewHints{})
	want(t, r, RvRegOutputCap, "fail", "error")
	want(t, r, RvRegInputCap, "pass")
	r = regDesign(true, false).review(t, ReviewHints{})
	want(t, r, RvRegOutputCap, "pass")
	want(t, r, RvRegInputCap, "fail", "error")
	// capless-stable declared → exempt
	h := ReviewHints{Capless: map[string]string{"U5": "intent review.caplessRegulators"}}
	want(t, regDesign(false, true).review(t, h), RvRegOutputCap, "pass")
	// a regulator known only from intent with passive pins: VOUT by name → likely (warning)
	b := rvBase().cap("C2", "10u", "/VIN", "GND")
	b.part("U7", "Synth:LDO", "", "1:GND:passive", "2:VOUT:passive", "3:VIN:passive")
	b.addNode("GND", "U7.1")
	b.addNode("+3V3", "U7.2")
	b.addNode("/VIN", "U7.3")
	c := want(t, b.review(t, ReviewHints{Regulators: map[string]string{"U7": "intent block ldo"}}), RvRegOutputCap, "pass", "warning")
	if c.Findings[0].Certainty != "likely" {
		t.Fatal(c.Findings[0])
	}
}

func TestReviewBulk(t *testing.T) {
	want(t, rvBase().ic("U1", "+3V3").cap("C1", "100n", "+3V3", "GND").review(t, ReviewHints{}), RvBulkCap, "pass", "warning")
	want(t, rvBase().ic("U1", "+3V3").cap("C1", "100n", "+3V3", "GND").cap("C2", "4.7u", "+3V3", "GND").review(t, ReviewHints{}), RvBulkCap, "pass")
	want(t, rvBase().ic("U1", "+3V3").cap("C1", "100n", "+3V3", "GND").cap("C2", "see note", "+3V3", "GND").review(t, ReviewHints{}), RvBulkCap, "pass", "warning")
	// no capacitor at all is below the floor too
	want(t, rvFed().ic("U1", "+3V3").review(t, ReviewHints{}), RvBulkCap, "pass", "warning")
}

func TestReviewMergedNets(t *testing.T) {
	erc := &ERCReport{Violations: []ERCViolation{{Sheet: "/Power/", Type: "multiple_net_names", Severity: "warning",
		Description: "Both '+3V3' and 'VDD_A' are attached to the same items; '+3V3' will be used in the netlist",
		Items:       []ERCItem{{Description: "Label 'VDD_A'"}, {Description: "Power symbol '+3V3'"}}}}}
	b := rvBase().ic("U1", "+3V3").cap("C1", "10u", "+3V3", "GND")
	c := want(t, b.review(t, ReviewHints{}, func(in *ReviewInput) { in.ERC = erc }), RvMergedNets, "fail", "error")
	if c.Findings[0].Sheet != "/Power/" || !strings.HasPrefix(c.Findings[0].Fingerprint, "sr2:"+RvMergedNets+":") {
		t.Fatal(c.Findings[0])
	}
	// two violations on one sheet with different items differ
	erc2 := &ERCReport{Violations: append(append([]ERCViolation{}, erc.Violations...), ERCViolation{Sheet: "/Power/", Type: "multiple_net_names", Severity: "warning",
		Description: "Both 'A' and 'B' are attached", Items: []ERCItem{{Description: "Label 'A'", UUID: "u1"}, {Description: "Label 'B'", UUID: "u2"}}})}
	c = want(t, b.review(t, ReviewHints{}, func(in *ReviewInput) { in.ERC = erc2 }), RvMergedNets, "fail", "error", "error")
	if c.Findings[0].Fingerprint == c.Findings[1].Fingerprint {
		t.Fatal("same fingerprint")
	}
	// items that do not name two labels: unverified, never matched to an alias
	bad := &ERCReport{Violations: []ERCViolation{{Sheet: "/", Type: "multiple_net_names", Description: "names conflict", Items: []ERCItem{{Description: "Wire"}}}}}
	want(t, b.review(t, ReviewHints{Aliases: [][]string{{"X", "Y"}}}, func(in *ReviewInput) { in.ERC = bad }), RvMergedNets, "cannot-check")
	// intentional alias
	want(t, b.review(t, ReviewHints{Aliases: [][]string{{"+3V3", "VDD_A"}}}, func(in *ReviewInput) { in.ERC = erc }), RvMergedNets, "pass")
	// no ERC, or the rule ignored → cannot-check
	want(t, b.review(t, ReviewHints{}, func(in *ReviewInput) { in.ERC = nil }), RvMergedNets, "cannot-check")
	want(t, b.review(t, ReviewHints{}, func(in *ReviewInput) { in.ERCIgnored = map[string]bool{"multiple_net_names": true} }), RvMergedNets, "cannot-check")
}

func TestReviewSinglePin(t *testing.T) {
	mk := func() *rvT {
		return rvBase().ic("U1", "+3V3", "3:PA1:bidirectional", "4:PA2:bidirectional", "5:PA3:bidirectional").cap("C1", "10u", "+3V3", "GND")
	}
	// a label reaching one pin → error
	b := mk()
	b.net("/SPARE_IO", "U1.3")
	want(t, b.review(t, ReviewHints{}), RvSinglePinNet, "fail", "error")
	// a dangling wire (auto name) → warning
	b = mk()
	b.net("Net-(U1-PA1)", "U1.3")
	want(t, b.review(t, ReviewHints{}), RvSinglePinNet, "pass", "warning")
	// KiCad's unconnected pins and no-connect flags are not nets
	b = mk()
	b.net("unconnected-(U1-PA1-Pad3)", "U1.3")
	want(t, b.review(t, ReviewHints{}), RvSinglePinNet, "not-applicable")
	// test point, connector (info), intent-declared net
	b = mk()
	b.part("TP1", "Connector:TestPoint", "", "1:1:passive")
	b.part("TP2", "Synth:PAD2", "", "1:1:passive", "2:2:passive")
	b.part("J1", "Connector:Conn_01x02", "", "1:Pin_1:passive", "2:Pin_2:passive")
	b.net("/TP_A", "TP1.1").net("/TP_B", "TP2.1").net("/SPARE", "J1.1").net("/DBG", "U1.3")
	h := ReviewHints{TestPoint: map[string]string{"TP2": "intent review.testPoints"}, SinglePinNet: map[string]string{"/DBG": "intent review.singlePinNets"}}
	c := want(t, b.review(t, h), RvSinglePinNet, "pass", "info")
	if len(c.Exempt) != 3 {
		t.Fatal(c.Exempt)
	}
}

func TestReviewHierMismatch(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "top.kicad_sch")
	_ = os.WriteFile(root, []byte(`(kicad_sch (version 20250114) (uuid "11111111-1111-1111-1111-111111111111")
  (sheet (at 10 10) (size 20 20) (uuid "22222222-2222-2222-2222-222222222222")
    (property "Sheetname" "Sub" (at 0 0 0)) (property "Sheetfile" "sub.kicad_sch" (at 0 0 0))
    (pin "DATA" input (at 10 12 180) (uuid "33333333-3333-3333-3333-333333333333"))
    (pin "CLK" input (at 10 14 180) (uuid "44444444-4444-4444-4444-444444444444")))
  (sheet (at 50 10) (size 20 20) (uuid "55555555-5555-5555-5555-555555555555")
    (property "Sheetname" "Sub2" (at 0 0 0)) (property "Sheetfile" "sub.kicad_sch" (at 0 0 0))
    (pin "DATA" input (at 50 12 180) (uuid "66666666-6666-6666-6666-666666666666"))
    (pin "CLK" input (at 50 14 180) (uuid "77777777-7777-7777-7777-777777777777"))
    (pin "EN" input (at 50 16 180) (uuid "88888888-8888-8888-8888-888888888888"))))`), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "sub.kicad_sch"), []byte(`(kicad_sch (version 20250114) (uuid "99999999-9999-9999-9999-999999999999")
  (hierarchical_label "DATA" (shape input) (at 0 0 0))
  (hierarchical_label "CLK" (shape input) (at 0 2 0))
  (hierarchical_label "RDY" (shape output) (at 0 4 0)))`), 0o644)
	ms, n, err := SheetHierarchyMismatches(root)
	if err != nil || n != 2 {
		t.Fatal(err, n)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.Path+" "+m.Kind+" "+m.Name)
	}
	if strings.Join(got, "; ") != "/Sub/ label-without-pin RDY; /Sub2/ pin-without-label EN; /Sub2/ label-without-pin RDY" {
		t.Fatalf("%v", got)
	}
	b := rvBase().ic("U1", "+3V3").cap("C1", "10u", "+3V3", "GND")
	r := b.review(t, ReviewHints{}, func(in *ReviewInput) { in.Hier, in.HierSheets = ms, n })
	c := want(t, r, RvHierMismatch, "fail", "error", "error", "error")
	// repeated sheet: each instance path is its own finding
	fps := map[string]bool{}
	for _, f := range c.Findings {
		fps[f.Fingerprint] = true
	}
	if len(fps) != 3 {
		t.Fatal(fps)
	}
	// not readable → cannot-check
	want(t, b.review(t, ReviewHints{}, func(in *ReviewInput) { in.HierErr = os.ErrNotExist }), RvHierMismatch, "cannot-check")
}

// rvFed is rvBase with the rails fed through a connector.
func rvFed() *rvT {
	b := rvBase()
	b.part("J9", "Connector:Conn_01x02", "", "1:Pin_1:passive", "2:Pin_2:passive")
	b.addNode("+3V3", "J9.1")
	b.addNode("GND", "J9.2")
	return b
}

func TestReviewWaivers(t *testing.T) {
	r := rvFed().ic("U1", "+3V3").review(t, ReviewHints{})
	c := want(t, r, RvDecoupling, "fail", "error")
	fp := c.Findings[0].Fingerprint
	if fp != ReviewFingerprint(RvDecoupling, []string{"U1"}, []string{"U1.1"}, []string{"+3V3"}) || !strings.HasPrefix(fp, "sr2:decoupling:") {
		t.Fatalf("fingerprint %q", fp)
	}
	bulk := rvCheckOf(t, r, RvBulkCap).Findings[0].Fingerprint
	r.Judge(func(s string) (string, bool) { return "waived: reviewed (test)", s == fp })
	if r.Status != gate.Pass || r.Verdict != "pass-with-warnings" || r.Waived != 1 || rvCheckOf(t, r, RvDecoupling).Findings[0].Waived == "" {
		t.Fatalf("%s %s %d", r.Status, r.Verdict, r.Waived)
	}
	r.Judge(func(s string) (string, bool) { return "waived: reviewed (test)", s == fp || s == bulk })
	if r.Verdict != "pass-with-waivers" || r.Waived != 2 {
		t.Fatalf("%s %d", r.Verdict, r.Waived)
	}
	// a waiver never decides a cannot-check
	j := odDesign()
	j.part("JP1", "Jumper:Jumper_2_Open", "", "1:A:passive", "2:B:passive")
	j.addNode("/SENSOR_INT", "JP1.1")
	j.addNode("/X", "JP1.2")
	r = j.review(t, completeHints())
	r.Judge(func(string) (string, bool) { return "waived: all", true })
	if r.Status != gate.CannotCheck {
		t.Fatal(r.Status)
	}
	// the fingerprint does not depend on the message or the node order
	b := newRvT()
	b.net("GND")
	b.part("U1", "Synth:IC", "", "2:GND:power_in", "1:VDD:power_in")
	b.part("J9", "Connector:Conn_01x02", "", "1:Pin_1:passive", "2:Pin_2:passive")
	b.net("+3V3", "J9.1", "U1.1")
	b.addNode("GND", "U1.2")
	b.addNode("GND", "J9.2")
	r2 := b.review(t, ReviewHints{})
	if got := rvCheckOf(t, r2, RvDecoupling).Findings[0].Fingerprint; got != fp {
		t.Fatalf("%q != %q", got, fp)
	}
}

// TestReviewCleanDesign: a correct small design passes every check (no
// false positives) with intent given.
func TestReviewCleanDesign(t *testing.T) {
	b := regDesign(true, true)
	b.ic("U2", "+3V3", "3:SDA:bidirectional", "4:SCL:bidirectional", "5:~{INT}:open_collector", "6:LED:bidirectional").cap("C3", "100n", "+3V3", "GND")
	b.cap("C4", "100n", "+3V3", "GND")
	b.ic("U3", "+3V3", "3:SDA:bidirectional", "4:SCL:bidirectional", "5:IRQ:input")
	b.net("/SDA", "U2.3", "U3.3").net("/SCL", "U2.4", "U3.4").net("/INT", "U2.5", "U3.5")
	b.res("R1", "4k7", "/SDA", "+3V3").res("R2", "4k7", "/SCL", "+3V3").res("R3", "10k", "/INT", "+3V3")
	b.res("R4", "1k", "/LED_A", "/LED")
	b.part("D1", "Device:LED", "LED", "1:K:passive", "2:A:passive")
	b.net("/LED", "U2.6").addNode("/LED_A", "D1.2")
	b.addNode("GND", "D1.1")
	h := ReviewHints{Intent: "i", NetVoltage: map[string]float64{"+3V3": 3.3, "+5V": 5}, VoltageSource: map[string]string{"+3V3": "intent i"}}
	r := b.review(t, h)
	for _, c := range r.Checks {
		if c.Status == "fail" || c.Status == "cannot-check" {
			t.Errorf("%s: %s %+v %v", c.ID, c.Status, c.Findings, c.Unverified)
		}
	}
	if r.Status != gate.Pass {
		t.Fatalf("%s", r.Summary())
	}
}

// TestReviewLargeGenerated: counts and runtime on a generated 400-IC
// board (every IC decoupled, I2C pulled up): no errors, only the shared
// decoupling warnings, well under a second.
func TestReviewLargeGenerated(t *testing.T) {
	b := rvBase()
	b.cap("C0", "22u", "+3V3", "GND")
	b.net("/SDA").net("/SCL")
	b.res("R901", "4k7", "/SDA", "+3V3").res("R902", "4k7", "/SCL", "+3V3")
	for i := 1; i <= 400; i++ {
		u := fmt.Sprintf("U%d", i)
		b.ic(u, "+3V3", "3:SDA:bidirectional", "4:SCL:bidirectional", "5:OUT:output", "6:IN:input")
		b.cap(fmt.Sprintf("C%d", i), "100n", "+3V3", "GND")
		b.addNode("/SDA", u+".3")
		b.addNode("/SCL", u+".4")
		b.net(fmt.Sprintf("/SIG%d", i), u+".5")
		if i > 1 {
			b.addNode(fmt.Sprintf("/SIG%d", i-1), u+".6")
		} else {
			b.res("R0", "10k", "/SIG0", "GND")
			b.addNode("/SIG0", u+".6")
		}
	}
	b.res("R401", "10k", "/SIG400", "GND")
	t0 := time.Now()
	h := completeHints()
	h.NetVoltage, h.VoltageSource = map[string]float64{"+3V3": 3.3}, map[string]string{"+3V3": "intent"}
	r := b.review(t, h)
	el := time.Since(t0)
	t.Logf("400 ICs, %d nets: %s in %v", len(b.nl.Nets), r.Summary(), el)
	// one rail for 400 ICs: every IC's decoupling set is shared (warning)
	if r.Errors != 0 || r.Status != gate.Pass || rvCheckOf(t, r, RvDecoupling).Coverage.Findings != r.Warnings || r.Warnings != 400 {
		t.Fatalf("%s: %v", r.Summary(), r.Items()[:3])
	}
	if !testing.Short() && el > 5*time.Second {
		t.Fatalf("too slow: %v", el)
	}
}

// TestReviewFingerprintCanonical: names are never joined unescaped, and a
// floating-input waiver names its receivers.
func TestReviewFingerprintCanonical(t *testing.T) {
	if ReviewFingerprint("r", nil, nil, []string{"A,B", "C"}) == ReviewFingerprint("r", nil, nil, []string{"A", "B,C"}) {
		t.Fatal("comma collision")
	}
	if ReviewFingerprint("r", []string{"A|B"}, nil, nil) == ReviewFingerprint("r", []string{"A"}, []string{"B"}, nil) {
		t.Fatal("pipe collision")
	}
	if ReviewFingerprint("r", nil, nil, []string{"B", "A"}) != ReviewFingerprint("r", nil, nil, []string{"A", "B"}) {
		t.Fatal("order matters")
	}
	mk := func(rx string) *rvT {
		b := rvBase().ic("U1", "+3V3", "3:EN:input").ic(rx, "+3V3", "3:CE:input").cap("C1", "10u", "+3V3", "GND")
		b.net("/EN", "U1.3", rx+".3")
		return b
	}
	fp := func(b *rvT) string {
		return rvCheckOf(t, b.review(t, completeHints()), RvFloatingInput).Findings[0].Fingerprint
	}
	if fp(mk("U2")) == fp(mk("U7")) {
		t.Fatal("replacing the receiver keeps the waiver")
	}
}

// TestReviewDualSupplyAndAltI2C: a bipolar-supply op-amp has no ground pin
// by design (proven only with rail voltages); an alternate-function SDA pin
// only suggests I2C.
func TestReviewDualSupplyAndAltI2C(t *testing.T) {
	b := rvFed()
	b.net("-5V")
	b.part("U1", "Amplifier_Operational:SYN-OPA", "", "1:V+:power_in", "2:V-:power_in", "3:OUT:output", "4:IN:input")
	b.addNode("+3V3", "U1.1")
	b.addNode("-5V", "U1.2")
	b.cap("C1", "10u", "+3V3", "GND").cap("C2", "10u", "-5V", "GND")
	b.net("/O", "U1.3", "U1.4")
	want(t, b.review(t, ReviewHints{}), RvICPowerGround, "pass", "warning") // bipolar assumed, not verified
	hv := ReviewHints{NetVoltage: map[string]float64{"+3V3": 3.3, "-5V": -5}, VoltageSource: map[string]string{"+3V3": "sim s", "-5V": "sim s"}}
	c := want(t, b.review(t, hv), RvICPowerGround, "pass")
	if len(c.Exempt) != 1 {
		t.Fatal(c.Exempt)
	}
	// two positive rails are no bipolar supply
	hv.NetVoltage["-5V"] = 5
	want(t, b.review(t, hv), RvICPowerGround, "fail", "error")
	// an MCU with VDD and VIO whose GND pin is left on its own net, a
	// signal pin tied to ground: no ground
	m := rvFed().cap("C1", "10u", "+3V3", "GND")
	m.net("+1V8")
	m.part("U1", "Synth:MCU", "", "1:VDD:power_in", "2:VIO:power_in", "3:GND:power_in", "4:MODE:input")
	m.addNode("+3V3", "U1.1")
	m.addNode("+1V8", "U1.2")
	m.net("/SPARE_G", "U1.3")
	m.addNode("GND", "U1.4")
	c = want(t, m.review(t, ReviewHints{}), RvICPowerGround, "fail", "error")
	if !strings.Contains(c.Findings[0].Message, "U1.3") {
		t.Fatal(c.Findings[0])
	}
	b = rvFed().ic("U1", "+3V3", "3:RB4/SDA:bidirectional").ic("U2", "+3V3", "3:IO:bidirectional").cap("C1", "10u", "+3V3", "GND").cap("C2", "1u", "+3V3", "GND")
	b.net("Net-(U1-RB4{slash}SDA)", "U1.3", "U2.3")
	c = want(t, b.review(t, ReviewHints{}), RvI2CPullup, "pass", "warning")
	if c.Findings[0].Certainty != "heuristic" {
		t.Fatal(c.Findings[0])
	}
	// the same pin on a net named for another use (CANRX-style) is not I2C
	b = rvFed().ic("U1", "+3V3", "3:SDA/CANRX:bidirectional").ic("U2", "+3V3", "3:IO:bidirectional").cap("C1", "10u", "+3V3", "GND").cap("C2", "1u", "+3V3", "GND")
	b.net("/CANRX", "U1.3", "U2.3")
	want(t, b.review(t, ReviewHints{}), RvI2CPullup, "not-applicable")
	// a charge-pump output capacitor to another rail is its AC return
	b = rvFed().ic("U3", "+3V3", "4:V+:power_out", "5:C1+:passive", "6:C1-:passive").cap("C1", "10u", "+3V3", "GND")
	b.cap("C5", "100n", "/VP", "+3V3")
	b.addNode("/VP", "U3.4")
	b.cap("C6", "100n", "/CF1", "/CF2")
	b.addNode("/CF1", "U3.5")
	b.addNode("/CF2", "U3.6")
	c = want(t, b.review(t, ReviewHints{}), RvRegOutputCap, "pass")
	if len(c.Exempt) != 1 || !strings.Contains(c.Exempt[0].Source, "flying") {
		t.Fatal(c.Exempt)
	}
	// an LDO whose only output cap goes to its input rail is not exempt
	l := rvFed().cap("C1", "10u", "+3V3", "GND")
	l.net("+1V8")
	l.part("U5", "Regulator_Linear:SYN-LDO", "SYN-LDO", "1:GND:power_in", "2:VO:power_out", "3:VI:power_in")
	l.addNode("GND", "U5.1")
	l.addNode("+1V8", "U5.2")
	l.addNode("+3V3", "U5.3")
	l.cap("C9", "1u", "+1V8", "+3V3")
	want(t, l.review(t, ReviewHints{}), RvRegOutputCap, "fail", "error")
	// a resistor between two rails is not a pull-up
	b = rvFed().ic("U1", "+3V3").cap("C1", "10u", "+3V3", "GND")
	b.net("+3V3_OUT")
	b.ic("U2", "+3V3_OUT").cap("C2", "10u", "+3V3_OUT", "GND").res("R1", "3.3", "+3V3", "+3V3_OUT")
	want(t, b.review(t, ReviewHints{}), RvPullupVoltage, "not-applicable")
}

// TestReviewLEDUnknownDriver: a series resistor to an IC pin typed passive
// is a complete path whose driver is not verified → likely warning.
func TestReviewLEDUnknownDriver(t *testing.T) {
	b := rvFed().ic("U1", "+3V3", "3:IO28:passive").cap("C1", "10u", "+3V3", "GND")
	b.res("R1", "1k", "/LA", "/IO")
	b.addNode("/IO", "U1.3")
	b.part("D1", "Device:LED", "LED", "1:K:passive", "2:A:passive")
	b.addNode("GND", "D1.1")
	b.addNode("/LA", "D1.2")
	c := want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "pass", "warning")
	if len(c.Findings[0].Missing) == 0 {
		t.Fatal(c.Findings[0])
	}
}
