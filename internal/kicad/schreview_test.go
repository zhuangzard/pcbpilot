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
	// a connector on a bus two on-board devices share does not excuse the
	// on-board pull-up: still an error
	b = bus()
	b.part("J1", "Connector:Conn_01x02", "", "1:Pin_1:passive", "2:Pin_2:passive")
	b.addNode("/SDA", "J1.1")
	b.addNode("/SCL", "J1.2")
	c = want(t, b.review(t, ReviewHints{}), RvI2CPullup, "fail", "error", "error")
	if !strings.Contains(strings.Join(c.Findings[0].Evidence, " "), "on-board devices") {
		t.Fatal(c.Findings[0])
	}
	// one on-board device, the bus completed off-board: the pull-up may be
	// there → info
	one := rvBase().ic("U1", "+3V3", "3:SDA:bidirectional", "4:SCL:bidirectional").cap("C1", "10u", "+3V3", "GND")
	one.part("J1", "Connector:Conn_01x02", "", "1:Pin_1:passive", "2:Pin_2:passive")
	one.net("/SDA", "U1.3", "J1.1").net("/SCL", "U1.4", "J1.2")
	want(t, one.review(t, ReviewHints{}), RvI2CPullup, "pass", "info", "info")
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
	// two LEDs sharing one resistor before the branches: no individual
	// limiter for either
	b = rvFed().res("R1", "330", "+3V3", "/LA")
	led(b, "D1", "/LA", "GND")
	led(b, "D2", "/LA", "GND")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error", "error")
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
	// one supply terminal, no recognisable ground pin: likely, not proven
	c := want(t, b.review(t, ReviewHints{}), RvICPowerGround, "pass", "warning")
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
	// a power_out pin alone does not prove a regulator: likely warning
	r := regDesign(false, true).review(t, ReviewHints{})
	c := want(t, r, RvRegOutputCap, "pass", "warning")
	if c.Findings[0].Certainty != "likely" || len(c.Findings[0].Missing) == 0 {
		t.Fatal(c.Findings[0])
	}
	// with an intent ldo block it is a proven error
	ldo := ReviewHints{Regulators: map[string]string{"U5": "intent block ldo"}}
	r = regDesign(false, true).review(t, ldo)
	want(t, r, RvRegOutputCap, "fail", "error")
	want(t, r, RvRegInputCap, "pass")
	r = regDesign(true, false).review(t, ldo)
	want(t, r, RvRegOutputCap, "pass")
	want(t, r, RvRegInputCap, "fail", "error")
	// capless-stable declared → exempt
	h := ReviewHints{Capless: map[string]string{"U5": "intent review.caplessRegulators"}}
	want(t, regDesign(false, true).review(t, h), RvRegOutputCap, "pass")
	// a regulator intent names, with passive pins: VOUT found by name; the
	// intent block is the regulator evidence → proven
	b := rvBase().cap("C2", "10u", "/VIN", "GND")
	b.part("U7", "Synth:LDO", "", "1:GND:passive", "2:VOUT:passive", "3:VIN:passive")
	b.addNode("GND", "U7.1")
	b.addNode("+3V3", "U7.2")
	b.addNode("/VIN", "U7.3")
	want(t, b.review(t, ReviewHints{Regulators: map[string]string{"U7": "intent block ldo"}}), RvRegOutputCap, "fail", "error")
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
		t.Fatalf("%s", r.SummaryLine())
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
	t.Logf("400 ICs, %d nets: %s in %v", len(b.nl.Nets), r.SummaryLine(), el)
	// one rail for 400 ICs: every IC's decoupling set is shared (warning)
	if r.Errors != 0 || r.Status != gate.Pass || rvCheckOf(t, r, RvDecoupling).Coverage.Findings != r.Warnings || r.Warnings != 400 {
		t.Fatalf("%s: %v", r.SummaryLine(), r.Items()[:3])
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
	// supply terminals only, between two rails: no ground pin to check,
	// whatever the rail voltages (bipolar, two positive, floating)
	c := want(t, b.review(t, ReviewHints{}), RvICPowerGround, "pass")
	if len(c.Exempt) != 1 {
		t.Fatal(c.Exempt)
	}
	hv := ReviewHints{NetVoltage: map[string]float64{"+3V3": 5, "-5V": 2.5}, VoltageSource: map[string]string{"+3V3": "sim s", "-5V": "sim s"}}
	want(t, b.review(t, hv), RvICPowerGround, "pass")
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
	want(t, l.review(t, ReviewHints{Regulators: map[string]string{"U5": "intent block ldo"}}), RvRegOutputCap, "fail", "error")
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

// TestReviewRound3: push-pull GPIOs contend, buck switch node traced
// through its inductor, a load switch is no proven regulator, a declared
// passive receiver is pulled up beyond its maximum, LED path polarity and
// clamps, commonPins by number and by name, the grouped summary.
func TestReviewRound3(t *testing.T) {
	// two GPIOs intent declares push-pull, shorted together
	g := rvBase().ic("U1", "+3V3", "3:PA1:bidirectional").ic("U2", "+3V3", "3:PB1:bidirectional").cap("C1", "10u", "+3V3", "GND")
	g.net("/SIG", "U1.3", "U2.3")
	want(t, g.review(t, ReviewHints{}), RvOutputConflict, "not-applicable")
	pp := ReviewHints{PushPull: map[string]string{"U1.3": "intent review.pushPullOutputs", "U2.3": "intent review.pushPullOutputs"}}
	want(t, g.review(t, pp), RvOutputConflict, "fail", "error")

	// a conventional buck: SW → L1 → +3V3 with its output cap there
	buck := func(outCap bool) *rvT {
		b := rvFed().cap("C2", "10u", "+3V3", "GND")
		b.net("+12V")
		b.part("U4", "Synth:BUCK", "", "1:VIN:power_in", "2:GND:power_in", "3:SW:power_out", "4:FB:input")
		b.addNode("+12V", "U4.1")
		b.addNode("GND", "U4.2")
		b.cap("C3", "10u", "+12V", "GND")
		b.part("L1", "Device:L", "4.7u", "1:1:passive", "2:2:passive")
		b.net("/SW", "U4.3", "L1.1")
		b.addNode("/VBUCK", "L1.2")
		b.res("R1", "100k", "/VBUCK", "/FB").res("R2", "22k", "/FB", "GND")
		b.addNode("/FB", "U4.4")
		if outCap {
			b.cap("C4", "22u", "/VBUCK", "GND")
		}
		return b
	}
	bk := ReviewHints{Regulators: map[string]string{"U4": "intent block buck"}}
	want(t, buck(true).review(t, bk), RvRegOutputCap, "pass")
	c := want(t, buck(false).review(t, bk), RvRegOutputCap, "fail", "error")
	if c.Findings[0].Nets[0] != "VBUCK" || !strings.Contains(c.Findings[0].Fix, "VBUCK") || strings.Contains(c.Findings[0].Fix, "SW") {
		t.Fatalf("cap asked on the switch node: %+v", c.Findings[0])
	}

	// a load switch (power_out, no regulator evidence) without an output cap
	ls := rvFed().cap("C1", "10u", "+3V3", "GND")
	ls.part("U6", "Synth:LOADSW", "", "1:VIN:power_in", "2:GND:power_in", "3:VOUT:power_out", "4:EN:input")
	ls.addNode("+3V3", "U6.1")
	ls.addNode("GND", "U6.2")
	ls.net("/VSW", "U6.3")
	ls.ic("U7", "/VSW").cap("C7", "100n", "/VSW", "GND")
	ls.res("R9", "10k", "/EN", "+3V3")
	ls.addNode("/EN", "U6.4")
	r := ls.review(t, ReviewHints{})
	if c := rvCheckOf(t, r, RvRegOutputCap); c.Status == "fail" {
		t.Fatalf("load switch proven as regulator: %+v", c)
	}

	// a passive FPGA pin intent declares an input, max 1.8 V, pulled to 5 V
	fp := rvBase().cap("C1", "10u", "+3V3", "GND")
	fp.net("+5V")
	fp.ic("U9", "+5V").cap("C9", "10u", "+5V", "GND")
	fp.ic("U1", "+3V3", "3:IO7:passive")
	fp.net("/CFG", "U1.3").res("R1", "4k7", "/CFG", "+5V")
	h := ReviewHints{InputPin: map[string]string{"U1.3": "intent review.inputPins"}, PinMaxV: map[string]float64{"U1.3": 1.8},
		NetVoltage: map[string]float64{"+5V": 5, "+3V3": 3.3}, VoltageSource: map[string]string{"+5V": "intent"}}
	want(t, fp.review(t, h), RvPullupVoltage, "fail", "error")

	// LED path: a series diode reversed to the current, and a TVS, do not
	// complete it; a diode of unknown polarity is unresolved
	led := func(b *rvT, ref, a, k string) {
		b.part(ref, "Device:LED", "LED", "1:K:passive", "2:A:passive")
		b.addNode(k, ref+".1")
		b.addNode(a, ref+".2")
	}
	rv := rvFed().res("R1", "1k", "/LA", "/M")
	rv.part("D5", "Device:D", "D", "1:K:passive", "2:A:passive")
	rv.addNode("/M", "D5.2")
	rv.addNode("+3V3", "D5.1")
	led(rv, "D1", "/LA", "GND")
	want(t, rv.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	fw := rvFed().res("R1", "1k", "/LA", "/M")
	fw.part("D5", "Device:D", "D", "1:K:passive", "2:A:passive")
	fw.addNode("/M", "D5.1")
	fw.addNode("+3V3", "D5.2")
	led(fw, "D1", "/LA", "GND")
	want(t, fw.review(t, ReviewHints{}), RvLEDLimiter, "pass")
	tv := rvFed().res("R1", "1k", "/LA", "/M")
	tv.part("D5", "Synth:D_TVS", "TVS", "1:K:passive", "2:A:passive")
	tv.addNode("/M", "D5.1")
	tv.addNode("+3V3", "D5.2")
	led(tv, "D1", "/LA", "GND")
	want(t, tv.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	uk := rvFed().res("R1", "1k", "/LA", "/M")
	uk.part("D5", "Device:D", "D", "1:1:passive", "2:2:passive")
	uk.addNode("/M", "D5.1")
	uk.addNode("+3V3", "D5.2")
	led(uk, "D1", "/LA", "GND")
	want(t, uk.review(t, ReviewHints{}), RvLEDLimiter, "cannot-check")

	// commonPins by pin number and by pin name; an ambiguous name is a note
	op := rvBase().ic("U1", "+3V3", "3:VOUT:output", "4:VOUT2:output", "5:OUT:output", "6:OUT:output").cap("C1", "10u", "+3V3", "GND")
	op.net("/X", "U1.3", "U1.4")
	want(t, op.review(t, ReviewHints{}), RvOutputConflict, "fail", "error")
	want(t, op.review(t, ReviewHints{CommonPins: [][]string{{"U1.3", "U1.4"}}}), RvOutputConflict, "pass")
	want(t, op.review(t, ReviewHints{CommonPins: [][]string{{"U1.VOUT", "U1.VOUT2"}}}), RvOutputConflict, "pass")
	r = op.review(t, ReviewHints{CommonPins: [][]string{{"U1.OUT", "U1.3"}}})
	if !strings.Contains(strings.Join(r.Notes, " "), "ambiguous") {
		t.Fatalf("notes %v", r.Notes)
	}

	// grouped summary: errors in full, warnings grouped, every finding kept
	b := rvBase().cap("C1", "10u", "+3V3", "GND")
	for i := 1; i <= 6; i++ {
		u := fmt.Sprintf("U%d", i)
		b.ic(u, "+3V3", "3:EN:input")
		b.ic(fmt.Sprintf("U%d", 10+i), "+3V3", "3:IO:bidirectional")
		b.net(fmt.Sprintf("/E%d", i), u+".3", fmt.Sprintf("U%d.3", 10+i))
	}
	r = b.review(t, ReviewHints{})
	sm := r.Summary
	if sm == nil || sm.Findings != r.Errors+r.Warnings+r.Infos+r.Waived || len(sm.Groups) >= r.Warnings {
		t.Fatalf("summary %+v (warnings %d)", sm, r.Warnings)
	}
	if lines := r.Lines(); len(lines) != len(sm.Errors)+len(sm.CannotCheck)+len(sm.Groups) {
		t.Fatal(lines)
	}
}

// TestReviewLEDDieNumbers: a bicolour LED numbered GA/GK/RA/RK (names
// 1–4) has known polarity per die.
func TestReviewLEDDieNumbers(t *testing.T) {
	b := rvFed().res("R1", "1k", "+3V3", "/G").res("R2", "1k", "+3V3", "/R")
	b.part("D1", "Synth:LED_DUAL", "LED", "GA:1:passive", "GK:2:passive", "RA:3:passive", "RK:4:passive")
	b.addNode("/G", "D1.GA")
	b.addNode("GND", "D1.GK")
	b.addNode("/R", "D1.RA")
	b.addNode("GND", "D1.RK")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "pass")
}

// TestReviewRound4: every designated ground pin individually, both LED
// sides resolved, boost output traced through its rectifier, per-pin
// decoupling, declared receivers of any part kind.
func TestReviewRound4(t *testing.T) {
	// GND connected, VSS and EP on other nets → error naming both
	m := rvFed().cap("C1", "10u", "+3V3", "GND")
	m.part("U1", "Synth:MCU", "", "1:VDD:power_in", "2:GND:power_in", "3:VSS:power_in", "4:EP:passive")
	m.addNode("+3V3", "U1.1")
	m.addNode("GND", "U1.2")
	m.net("/X1", "U1.3")
	m.net("/X2", "U1.4")
	c := want(t, m.review(t, ReviewHints{}), RvICPowerGround, "fail", "error")
	if len(c.Findings[0].Pins) != 2 || !strings.Contains(c.Findings[0].Message, "U1.3") || !strings.Contains(c.Findings[0].Message, "U1.4") {
		t.Fatal(c.Findings[0])
	}

	led := func(b *rvT, ref, a, k string) {
		b.part(ref, "Device:LED", "LED", "1:K:passive", "2:A:passive")
		b.addNode(k, ref+".1")
		b.addNode(a, ref+".2")
	}
	// supply → R → LED → transistor: the limiter does not settle the other side
	b := rvFed().res("R1", "1k", "+3V3", "/LA")
	b.part("Q1", "Transistor_FET:Q_NMOS_GDS", "", "1:G:input", "2:D:passive", "3:S:passive")
	b.addNode("/LK", "Q1.2")
	b.addNode("GND", "Q1.3")
	b.net("/G", "Q1.1")
	led(b, "D1", "/LA", "/LK")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "cannot-check")
	// … or an unknown-polarity diode on the far side
	b = rvFed().res("R1", "1k", "+3V3", "/LA")
	b.part("D9", "Device:D", "D", "1:1:passive", "2:2:passive")
	b.addNode("/LK", "D9.1")
	b.addNode("GND", "D9.2")
	led(b, "D1", "/LA", "/LK")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "cannot-check")

	// boost: VIN — L1 — SW — D1 — VOUT; input cap present, output cap missing
	boost := func(outCap bool) *rvT {
		b := rvFed().cap("C1", "10u", "+3V3", "GND")
		b.part("U4", "Synth:BOOST", "", "1:VIN:power_in", "2:GND:power_in", "3:SW:power_out")
		b.addNode("+3V3", "U4.1")
		b.addNode("GND", "U4.2")
		b.part("L1", "Device:L", "10u", "1:1:passive", "2:2:passive")
		b.addNode("+3V3", "L1.1")
		b.net("/SW", "U4.3", "L1.2")
		b.part("D1", "Device:D_Schottky", "SS14", "1:K:passive", "2:A:passive")
		b.addNode("/SW", "D1.2")
		b.net("+12V", "D1.1")
		b.ic("U8", "+12V").cap("C8", "100n", "+12V", "GND")
		if outCap {
			b.cap("C4", "22u", "+12V", "GND")
		}
		return b
	}
	bo := ReviewHints{Regulators: map[string]string{"U4": "intent block boost"}}
	c = want(t, boost(false).review(t, bo), RvRegOutputCap, "pass") // C8 (100 nF) is on +12V: a cap is there
	_ = c
	b = boost(false)
	b.nl.Components = b.nl.Components[:len(b.nl.Components)-2] // drop U8 and C8: +12V has no cap
	for i := range b.nl.Nets {
		var keep []SchNetNode
		for _, n := range b.nl.Nets[i].Nodes {
			if n.Ref != "U8" && n.Ref != "C8" {
				keep = append(keep, n)
			}
		}
		b.nl.Nets[i].Nodes = keep
	}
	c = want(t, b.review(t, bo), RvRegOutputCap, "fail", "error")
	if c.Findings[0].Nets[0] != "+12V" {
		t.Fatal(c.Findings[0])
	}
	want(t, boost(true).review(t, bo), RvRegOutputCap, "pass")
	// the same part declared a buck: the inductor leads to its input → unresolved
	want(t, b.review(t, ReviewHints{Regulators: map[string]string{"U4": "intent block buck"}}), RvRegOutputCap, "cannot-check")
	// no declared topology: unresolved, never the input cap
	want(t, b.review(t, ReviewHints{}), RvRegOutputCap, "cannot-check")

	// VDD1 / VDD2 of one package on one cap: each pin is shared
	d := rvFed().cap("C1", "100n", "+3V3", "GND")
	d.part("U1", "Synth:IC", "", "1:VDD1:power_in", "2:VDD2:power_in", "3:GND:power_in")
	d.addNode("+3V3", "U1.1")
	d.addNode("+3V3", "U1.2")
	d.addNode("GND", "U1.3")
	c = want(t, d.review(t, ReviewHints{}), RvDecoupling, "pass", "warning", "warning")
	if c.Coverage.Candidates != 2 {
		t.Fatal(c.Coverage)
	}

	// a declared receiver on a part of kind "other" (X1) is checked
	x := rvBase().cap("C1", "10u", "+3V3", "GND")
	x.net("+5V")
	x.ic("U9", "+5V").cap("C9", "10u", "+5V", "GND")
	x.part("X1", "Synth:MODULE", "", "1:CFG:passive")
	x.net("/CFG", "X1.1").res("R1", "4k7", "/CFG", "+5V")
	h := ReviewHints{InputPin: map[string]string{"X1.1": "intent review.inputPins"}, PinMaxV: map[string]float64{"X1.1": 1.8},
		NetVoltage: map[string]float64{"+5V": 5}, VoltageSource: map[string]string{"+5V": "intent"}}
	want(t, x.review(t, h), RvPullupVoltage, "fail", "error")
}

// TestReviewRound5: NC ground and power pins, pinMaxV before the
// own-supply match, LED polarity on either side, common-node LEDs.
func TestReviewRound5(t *testing.T) {
	// GND grounded, VSS / EP flagged no-connect → error
	m := rvFed().cap("C1", "10u", "+3V3", "GND")
	m.part("U1", "Synth:MCU", "", "1:VDD:power_in", "2:GND:power_in", "3:VSS:power_in", "4:EP:passive")
	m.addNode("+3V3", "U1.1")
	m.addNode("GND", "U1.2")
	m.typ["U1.3"], m.typ["U1.4"] = "power_in+no_connect", "passive+no_connect"
	m.net("unconnected-(U1-VSS-Pad3)", "U1.3")
	m.net("unconnected-(U1-EP-Pad4)", "U1.4")
	c := want(t, m.review(t, ReviewHints{}), RvICPowerGround, "fail", "error")
	if len(c.Findings[0].Pins) != 2 {
		t.Fatal(c.Findings[0])
	}
	// dual VDD, VDD2 no-connect (or unconnected) → error naming VDD2
	for _, nc := range []bool{true, false} {
		d := rvFed().cap("C1", "10u", "+3V3", "GND")
		d.part("U2", "Synth:IC", "", "1:VDD1:power_in", "2:VDD2:power_in", "3:GND:power_in")
		d.addNode("+3V3", "U2.1")
		d.addNode("GND", "U2.3")
		if nc {
			d.typ["U2.2"] = "power_in+no_connect"
		}
		d.net("unconnected-(U2-VDD2-Pad2)", "U2.2")
		c = want(t, d.review(t, ReviewHints{}), RvICPowerGround, "fail", "error")
		if !strings.Contains(c.Findings[0].Message, "U2.2") {
			t.Fatal(c.Findings[0])
		}
	}
	// a 3.3 V IC input with pinMaxV 1.8 pulled up to its own 3.3 V supply
	p := rvBase().ic("U1", "+3V3", "3:CFG:input").cap("C1", "10u", "+3V3", "GND")
	p.net("/CFG", "U1.3").res("R1", "10k", "/CFG", "+3V3")
	h := ReviewHints{PinMaxV: map[string]float64{"U1.3": 1.8}, NetVoltage: map[string]float64{"+3V3": 3.3}, VoltageSource: map[string]string{"+3V3": "intent"}}
	want(t, p.review(t, h), RvPullupVoltage, "fail", "error")
	want(t, p.review(t, ReviewHints{}), RvPullupVoltage, "pass") // no maximum: own supply matches

	// unknown polarity on the anode side, then on the cathode side, each
	// with the other side on a rail
	for _, names := range [][2]string{{"1:K:passive", "2:X:passive"}, {"1:X:passive", "2:A:passive"}} {
		b := rvFed().res("R1", "1k", "+3V3", "/LA")
		b.part("D1", "Device:LED", "LED", names[0], names[1])
		b.addNode("GND", "D1.1")
		b.addNode("/LA", "D1.2")
		want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "cannot-check")
	}

	led := func(b *rvT, ref, a, k string) {
		b.part(ref, "Device:LED", "LED", "1:K:passive", "2:A:passive")
		b.addNode(k, ref+".1")
		b.addNode(a, ref+".2")
	}
	// common anode node driven by one pin, each cathode with its own resistor
	ca := rvFed().ic("U1", "+3V3", "3:PA0:output").cap("C1", "10u", "+3V3", "GND")
	ca.net("/CA", "U1.3")
	for i := 1; i <= 3; i++ {
		d, k := fmt.Sprintf("D%d", i), fmt.Sprintf("/K%d", i)
		led(ca, d, "/CA", k)
		ca.res(fmt.Sprintf("R%d", i), "1k", k, "GND")
	}
	want(t, ca.review(t, ReviewHints{}), RvLEDLimiter, "pass")
	// a branch inside an individual resistor path stays cannot-check
	ca.res("R9", "1k", "/K1", "+3V3")
	c = want(t, ca.review(t, ReviewHints{}), RvLEDLimiter, "cannot-check")
	if !strings.Contains(strings.Join(c.Unverified, " "), "D1") {
		t.Fatal(c.Unverified)
	}

	// an antiparallel bicolour LED family on a common driven node (GA/GK/RA/RK)
	bc := rvFed().ic("U1", "+3V3", "3:LEDC:output").cap("C1", "10u", "+3V3", "GND")
	bc.net("/LC", "U1.3")
	for i := 1; i <= 2; i++ {
		d, n := fmt.Sprintf("D%d", i), fmt.Sprintf("/N%d", i)
		bc.part(d, "Synth:LED_DUAL", "LED", "GA:1:passive", "GK:2:passive", "RA:3:passive", "RK:4:passive")
		bc.addNode(n, d+".GA")
		bc.addNode("/LC", d+".GK")
		bc.addNode("/LC", d+".RA")
		bc.addNode(n, d+".RK")
		bc.res(fmt.Sprintf("R%d", i), "470", n, fmt.Sprintf("/IO%d", i))
		bc.ic(fmt.Sprintf("U%d", 10+i), "+3V3", "3:IO:output")
		bc.addNode(fmt.Sprintf("/IO%d", i), fmt.Sprintf("U%d.3", 10+i))
	}
	// limited per die; both ends are driver pins, so the bias depends on
	// the drivers (likely warning per die), never an error
	c = want(t, bc.review(t, ReviewHints{}), RvLEDLimiter, "pass", "warning", "warning", "warning", "warning")
	if !strings.Contains(c.Findings[0].Message, "bias") {
		t.Fatal(c.Findings[0])
	}
}

// TestReviewBusConnector: BUS1 (footprint BUSPCI) with power_in pins is
// no connector by name: its unconnected power input is an error; declared
// a connector its power inputs are checked all the same.
func TestReviewBusConnector(t *testing.T) {
	b := rvFed().cap("C1", "10u", "+3V3", "GND")
	b.part("BUS1", "Synth:BUSPCI_5V", "BUSPCI_5V", "1:+12V:power_in", "2:+5V:power_in", "3:GND:power_in")
	b.nl.Components[len(b.nl.Components)-1].Footprint = "footprints:BUSPCI"
	b.net("unconnected-(BUS1-+12V-Pad1)", "BUS1.1")
	b.addNode("+3V3", "BUS1.2")
	b.addNode("GND", "BUS1.3")
	// not a KiCad connector library, no intent role: an on-board part whose
	// unconnected power input is an error
	c := want(t, b.review(t, ReviewHints{}), RvICPowerGround, "fail", "error")
	if !strings.Contains(c.Findings[0].Message, "BUS1.1") {
		t.Fatal(c.Findings[0])
	}
	// declared a connector: its power inputs are still checked
	want(t, b.review(t, ReviewHints{Connector: map[string]string{"BUS1": "intent review.connectors"}}), RvICPowerGround, "fail", "error")
}

// TestReviewRound6: package-shared resistors, reversed LEDs, per-die
// findings, positive supply evidence per power pin.
func TestReviewRound6(t *testing.T) {
	// common-anode RGB: one resistor from the common anode to +3V3, the
	// cathodes straight to driver pins → every die unlimited
	ca := rvFed().res("R1", "330", "+3V3", "/CA").ic("U1", "+3V3", "3:PR:output", "4:PG:output", "5:PB:output").cap("C1", "10u", "+3V3", "GND")
	ca.part("D1", "Synth:LED_RGB_CA", "LED", "1:A:passive", "2:RK:passive", "3:GK:passive", "4:BK:passive")
	ca.addNode("/CA", "D1.1")
	for i, n := range []string{"/R", "/G", "/B"} {
		ca.net(n, fmt.Sprintf("D1.%d", i+2), fmt.Sprintf("U1.%d", i+3))
	}
	c := want(t, ca.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error", "error", "error")
	if !strings.Contains(c.Findings[0].Message, "die") {
		t.Fatal(c.Findings[0])
	}
	// common-cathode: one resistor from the common cathode to GND
	cc := rvFed().res("R1", "330", "/CK", "GND").ic("U1", "+3V3", "3:PR:output", "4:PG:output").cap("C1", "10u", "+3V3", "GND")
	cc.part("D1", "Synth:LED_RG_CC", "LED", "1:K:passive", "2:RA:passive", "3:GA:passive")
	cc.addNode("/CK", "D1.1")
	cc.net("/R", "D1.2", "U1.3").net("/G", "D1.3", "U1.4")
	want(t, cc.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error", "error")
	// … with a resistor per die it passes
	ok := rvFed().ic("U1", "+3V3", "3:PR:output", "4:PG:output").cap("C1", "10u", "+3V3", "GND")
	ok.part("D1", "Synth:LED_RG_CC", "LED", "1:K:passive", "2:RA:passive", "3:GA:passive")
	ok.addNode("GND", "D1.1")
	ok.res("R1", "330", "/R", "/RR").res("R2", "330", "/G", "/GG")
	ok.addNode("/RR", "D1.2")
	ok.addNode("/GG", "D1.3")
	ok.addNode("/R", "U1.3")
	ok.addNode("/G", "U1.4")
	want(t, ok.review(t, ReviewHints{}), RvLEDLimiter, "pass")

	// reversed: anode on GND, cathode through 1 kΩ to +3V3
	rv := rvFed().res("R1", "1k", "/K", "+3V3")
	rv.part("D1", "Device:LED", "LED", "1:K:passive", "2:A:passive")
	rv.addNode("GND", "D1.2")
	rv.addNode("/K", "D1.1")
	c = want(t, rv.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	if !strings.Contains(c.Findings[0].Message, "reverse") {
		t.Fatal(c.Findings[0])
	}
	// endpoint levels unknown (two unnamed rails without voltages) → cannot-check
	un := rvFed().res("R1", "1k", "/K", "VX1")
	un.part("D1", "Device:LED", "LED", "1:K:passive", "2:A:passive")
	un.net("VX2")
	un.addNode("VX2", "D1.2")
	un.addNode("/K", "D1.1")
	un.ic("U7", "VX1").ic("U8", "VX2").cap("C7", "1u", "VX1", "GND").cap("C8", "1u", "VX2", "GND")
	want(t, un.review(t, ReviewHints{NetRole: map[string]string{"VX1": "power", "VX2": "power"}}), RvLEDLimiter, "pass", "warning")

	// per die: die R unlimited (error), die G through a transistor (cannot-check)
	pd := rvFed().ic("U1", "+3V3", "3:PR:output").cap("C1", "10u", "+3V3", "GND")
	pd.part("D1", "Synth:LED_RG", "LED", "1:RA:passive", "2:RK:passive", "3:GA:passive", "4:GK:passive")
	pd.net("/R", "D1.1", "U1.3")
	pd.addNode("GND", "D1.2")
	pd.res("R2", "330", "+3V3", "/GA")
	pd.addNode("/GA", "D1.3")
	pd.part("Q1", "Transistor_FET:Q_NMOS_GDS", "", "1:G:input", "2:D:passive", "3:S:passive")
	pd.net("/GK", "D1.4", "Q1.2")
	pd.addNode("GND", "Q1.3")
	pd.net("/QG", "Q1.1")
	c = want(t, pd.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	if len(c.Unverified) != 1 || !strings.Contains(c.Findings[0].Message, "die R") {
		t.Fatalf("%+v", c)
	}

	// a VDD pin tied to ground → error; declared → exempt
	g := rvFed().cap("C1", "10u", "+3V3", "GND")
	g.part("U1", "Synth:IC", "", "1:VDD:power_in", "2:VBAT:power_in", "3:GND:power_in")
	g.addNode("+3V3", "U1.1")
	g.addNode("GND", "U1.2")
	g.addNode("GND", "U1.3")
	want(t, g.review(t, ReviewHints{}), RvICPowerGround, "fail", "error")
	want(t, g.review(t, ReviewHints{GroundedPin: map[string]string{"U1.2": "intent review.groundedPowerPins"}}), RvICPowerGround, "pass")
	// a V− pin on ground: single-supply op-amp, fine
	op := rvFed().cap("C1", "10u", "+3V3", "GND")
	op.part("U2", "Synth:OPA", "", "1:V+:power_in", "2:V-:power_in", "3:OUT:output", "4:IN:input")
	op.addNode("+3V3", "U2.1")
	op.addNode("GND", "U2.2")
	op.net("/O", "U2.3", "U2.4")
	want(t, op.review(t, ReviewHints{}), RvICPowerGround, "pass")
	// a VDD on a populated local net with no supply source: likely warning
	ns := rvFed().cap("C1", "10u", "+3V3", "GND")
	ns.part("U3", "Synth:IC", "", "1:VDD:power_in", "2:GND:power_in")
	ns.net("/VLOC", "U3.1")
	ns.addNode("GND", "U3.2")
	ns.cap("C3", "100n", "/VLOC", "GND")
	c = want(t, ns.review(t, ReviewHints{}), RvICPowerGround, "pass", "warning")
	if len(c.Findings[0].Missing) == 0 {
		t.Fatal(c.Findings[0])
	}
	// … fed through a ferrite from +3V3: evidence behind the inductor
	ns.part("FB1", "Device:FerriteBead", "", "1:1:passive", "2:2:passive")
	ns.addNode("+3V3", "FB1.1")
	ns.addNode("/VLOC", "FB1.2")
	want(t, ns.review(t, ReviewHints{}), RvICPowerGround, "pass")
}

// TestReviewI2CBitBanged: an I2C line driven by a tri-state buffer through
// a series resistor (a programmer bit-banging the bus): the pull-up need
// is not proven → likely warning, not error.
func TestReviewI2CBitBanged(t *testing.T) {
	b := rvBase().ic("U1", "+3V3", "3:SDA:bidirectional").ic("U2", "+3V3", "3:SDA:bidirectional").ic("U3", "+3V3", "3:1Y:tri_state").cap("C1", "10u", "+3V3", "GND")
	b.net("/SDA", "U1.3", "U2.3").res("R1", "100", "/SDA", "/DRV")
	b.addNode("/DRV", "U3.3")
	want(t, b.review(t, ReviewHints{}), RvI2CPullup, "pass", "warning")
}

// TestReviewRound7: LED bias by numeric / name-spelled levels and driver
// pins, antiparallel LEDs on one resistor, V- on ground needs a positive
// supply, rails are no single-pin nets.
func TestReviewRound7(t *testing.T) {
	led := func(b *rvT, ref, a, k string) {
		b.part(ref, "Device:LED", "LED", "1:K:passive", "2:A:passive")
		b.addNode(k, ref+".1")
		b.addNode(a, ref+".2")
	}
	// +12V → R → LED → +5V: name-spelled voltages order the rails
	b := rvFed().res("R1", "1k", "+12V", "/LA").cap("C1", "10u", "+3V3", "GND")
	b.net("+5V")
	b.ic("U1", "+12V").ic("U2", "+5V").cap("C2", "10u", "+12V", "GND").cap("C3", "10u", "+5V", "GND")
	led(b, "D1", "/LA", "+5V")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "pass")
	// the other way round: reversed
	b = rvFed().res("R1", "1k", "+5V", "/LA").cap("C1", "10u", "+3V3", "GND")
	b.net("+12V")
	b.ic("U1", "+12V").ic("U2", "+5V").cap("C2", "10u", "+12V", "GND").cap("C3", "10u", "+5V", "GND")
	led(b, "D1", "/LA", "+12V")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
	// two positive rails without a spelled voltage: likely warning
	b = rvFed().res("R1", "1k", "VCC", "/LA").cap("C1", "10u", "+3V3", "GND")
	b.net("VDD_AUX")
	b.ic("U1", "VCC").ic("U2", "VDD_AUX").cap("C2", "10u", "VCC", "GND").cap("C3", "10u", "VDD_AUX", "GND")
	led(b, "D1", "/LA", "VDD_AUX")
	want(t, b.review(t, ReviewHints{}), RvLEDLimiter, "pass", "warning")

	// GPIO → anode, cathode → R → +3V3: reversed unless the driver's domain
	// exceeds the rail; domain 3V3 known → error, unknown → warning
	g := rvFed().cap("C1", "10u", "+3V3", "GND")
	g.part("U1", "Synth:MCU", "", "1:VDD:power_in", "2:GND:power_in", "3:PA0:output")
	g.addNode("+3V3", "U1.1")
	g.addNode("GND", "U1.2")
	g.net("/IO", "U1.3")
	led(g, "D1", "/IO", "/K")
	g.res("R1", "1k", "/K", "+3V3")
	want(t, g.review(t, ReviewHints{NetVoltage: map[string]float64{"+3V3": 3.3}, VoltageSource: map[string]string{"+3V3": "intent"}}), RvLEDLimiter, "fail", "error")
	g2 := rvFed().cap("C1", "10u", "+3V3", "GND")
	g2.part("U1", "Synth:MCU", "", "1:VDD:power_in", "2:VIO:power_in", "3:GND:power_in", "4:PA0:output")
	g2.addNode("+3V3", "U1.1")
	g2.net("VX9")
	g2.addNode("VX9", "U1.2")
	g2.addNode("GND", "U1.3")
	g2.net("/IO", "U1.4")
	led(g2, "D1", "/IO", "/K")
	g2.res("R1", "1k", "/K", "+3V3")
	want(t, g2.review(t, ReviewHints{NetRole: map[string]string{"VX9": "power"}}), RvLEDLimiter, "pass", "warning")
	// GPIO → R → anode, cathode → GND: ok
	ok := rvFed().ic("U1", "+3V3", "3:PA0:output").cap("C1", "10u", "+3V3", "GND")
	ok.net("/IO", "U1.3").res("R1", "1k", "/IO", "/LA")
	led(ok, "D1", "/LA", "GND")
	want(t, ok.review(t, ReviewHints{}), RvLEDLimiter, "pass")
	// two GPIOs drive the LED (R in series): bias depends on the drivers
	two := rvFed().ic("U1", "+3V3", "3:PA0:output", "4:PA1:output").cap("C1", "10u", "+3V3", "GND")
	two.net("/IO", "U1.3").res("R1", "1k", "/IO", "/LA")
	two.net("/IO2", "U1.4")
	led(two, "D1", "/LA", "/IO2")
	want(t, two.review(t, ReviewHints{}), RvLEDLimiter, "pass", "warning")

	// an antiparallel pair of LEDs on one resistor: the resistor is
	// exclusive to whichever conducts — no "individual limiter" error
	ap := rvFed().ic("U1", "+3V3", "3:PA0:output", "4:PA1:output").cap("C1", "10u", "+3V3", "GND")
	ap.net("/IO", "U1.3").res("R1", "330", "/IO", "/N")
	ap.net("/M", "U1.4")
	led(ap, "D1", "/N", "/M")
	led(ap, "D2", "/M", "/N")
	c := want(t, ap.review(t, ReviewHints{}), RvLEDLimiter, "pass", "warning", "warning")
	for _, f := range c.Findings {
		if strings.Contains(f.Message, "individual") {
			t.Fatal(f)
		}
	}

	// V+ on −5V and V− on GND: no positive supply → V− on ground is an error
	op := rvFed().cap("C1", "10u", "+3V3", "GND")
	op.net("-5V")
	op.ic("U9", "-5V")
	op.cap("C9", "10u", "-5V", "GND")
	op.part("U2", "Synth:OPA", "", "1:V+:power_in", "2:V-:power_in", "3:OUT:output", "4:IN:input")
	op.addNode("-5V", "U2.1")
	op.addNode("GND", "U2.2")
	op.net("/O", "U2.3", "U2.4")
	c = want(t, op.review(t, ReviewHints{}), RvICPowerGround, "fail", "error")
	if !strings.Contains(c.Findings[0].Message, "U2.2") {
		t.Fatal(c.Findings[0])
	}

	// a module with integrated decoupling, only power symbols on VCC/GND:
	// VCC and GND are rails, not single-pin nets
	m := newRvT()
	m.part("U1", "Synth:MODULE", "", "1:VCC:power_in", "2:GND:power_in")
	m.net("VCC", "U1.1").net("GND", "U1.2")
	want(t, m.review(t, ReviewHints{}), RvSinglePinNet, "not-applicable")
}

// TestReviewRound8: two GPIOs with no resistor keep the missing-limiter
// error beside the bias warning; the driver domain voltage is read from
// the rail name.
func TestReviewRound8(t *testing.T) {
	led := func(b *rvT, ref, a, k string) {
		b.part(ref, "Device:LED", "LED", "1:K:passive", "2:A:passive")
		b.addNode(k, ref+".1")
		b.addNode(a, ref+".2")
	}
	two := rvFed().ic("U1", "+3V3", "3:PA0:output", "4:PA1:output").cap("C1", "10u", "+3V3", "GND")
	two.net("/IO", "U1.3").net("/IO2", "U1.4")
	led(two, "D1", "/IO", "/IO2")
	c := rvCheckOf(t, two.review(t, ReviewHints{}), RvLEDLimiter)
	sev := map[string]bool{}
	for _, f := range c.Findings {
		sev[f.Severity] = true
	}
	if c.Status != "fail" || len(c.Findings) != 2 || !sev["error"] || !sev["warning"] || c.Findings[0].Fingerprint == c.Findings[1].Fingerprint {
		t.Fatalf("%+v", c)
	}
	withR := rvFed().ic("U1", "+3V3", "3:PA0:output", "4:PA1:output").cap("C1", "10u", "+3V3", "GND")
	withR.net("/IO", "U1.3").res("R1", "1k", "/IO", "/LA").net("/IO2", "U1.4")
	led(withR, "D1", "/LA", "/IO2")
	want(t, withR.review(t, ReviewHints{}), RvLEDLimiter, "pass", "warning")

	// MCU on +3V3 drives the anode, cathode through R to +5V: names give
	// 3.3 V < 5 V → reversed, proven without intent / sim
	m := rvFed().cap("C1", "10u", "+3V3", "GND")
	m.net("+5V")
	m.ic("U9", "+5V").cap("C9", "10u", "+5V", "GND")
	m.part("U1", "Synth:MCU", "", "1:VDD:power_in", "2:GND:power_in", "3:PA0:output")
	m.addNode("+3V3", "U1.1")
	m.addNode("GND", "U1.2")
	m.net("/IO", "U1.3")
	led(m, "D1", "/IO", "/K")
	m.res("R1", "1k", "/K", "+5V")
	want(t, m.review(t, ReviewHints{}), RvLEDLimiter, "fail", "error")
}
