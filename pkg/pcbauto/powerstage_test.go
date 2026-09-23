package pcbauto

import (
	"testing"
)

// buckBoard: 12 V → 3V3 asynchronous buck (catch diode), bootstrap cap,
// feedback divider, plus a small MCU load. Parts start scattered.
func buckBoard() *Board {
	pad := func(n, net string, x, y float64) *Pad {
		return &Pad{Number: n, Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{x, y}, W: 30, H: 40}}
	}
	two := func(ref, dev, a, bnet string, x, y float64) *Part {
		return &Part{Ref: ref, Device: dev, Pads: []*Pad{pad("1", a, x-30, y), pad("2", bnet, x+30, y)}}
	}
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 2000, 1500}.Corners()}
	b.Parts = []*Part{
		{Ref: "U1", Device: "MP2359", Pads: []*Pad{
			pad("1", "BST", 1000, 800), pad("2", "GND", 1000, 750), pad("3", "FB", 1000, 700),
			pad("4", "EN", 1100, 700), pad("5", "+12V", 1100, 750), pad("6", "SW", 1100, 800)}},
		two("L1", "4.7uH", "SW", "+3V3", 200, 200),
		two("D1", "SS34", "SW", "GND", 1800, 200),
		two("C1", "100nF", "+12V", "GND", 200, 1300),
		two("C2", "10uF", "+12V", "GND", 1800, 1300),
		two("C3", "100nF", "SW", "BST", 400, 700),
		two("R1", "100k", "+3V3", "FB", 1600, 700),
		two("R2", "22k", "FB", "GND", 1600, 900),
		two("C4", "22uF", "+3V3", "GND", 600, 1100),
		{Ref: "U2", Device: "STM32G030", Pads: []*Pad{
			pad("1", "+3V3", 500, 400), pad("2", "GND", 500, 350), pad("3", "SDA", 600, 400), pad("4", "SCL", 600, 350)}},
	}
	_ = b.Index()
	return b
}

func TestBuckRecognition(t *testing.T) {
	b := buckBoard()
	c := Understand(b, Analyze(b, PowerSpec{}, nil))
	if len(c.Converters) != 1 {
		t.Fatalf("want 1 converter, got %d", len(c.Converters))
	}
	cv := c.Converters[0]
	if cv.Topology != "buck" || cv.Confidence != "certain" || cv.Diode != "D1" {
		t.Errorf("topology %+v", cv)
	}
	if cv.InRail != "+12V" || cv.OutRail != "+3V3" {
		t.Errorf("rails in=%s out=%s", cv.InRail, cv.OutRail)
	}
	if cv.HotCap != "C1" {
		t.Errorf("hot cap should be the smallest input ceramic C1, got %s", cv.HotCap)
	}
	if cv.Bootstrap != "C3" || cv.FBPin != "U1.3" || len(cv.Feedback) < 2 {
		t.Errorf("bootstrap %s fb %s %v", cv.Bootstrap, cv.FBPin, cv.Feedback)
	}
}

// A synchronous boost from a battery: no diode, the inductor hangs off an
// input-named rail that also supplies the IC.
func TestSyncBoostByInputName(t *testing.T) {
	pad := func(n, net string, x float64) *Pad {
		return &Pad{Number: n, Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{x, 100}, W: 30, H: 40}}
	}
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 2000, 1500}.Corners()}
	b.Parts = []*Part{
		{Ref: "U1", Device: "TPS61023", Pads: []*Pad{pad("1", "VBAT", 100), pad("2", "LX", 150), pad("3", "GND", 200), pad("4", "+5V", 250), pad("5", "FB", 300), pad("6", "EN", 350)}},
		{Ref: "L1", Device: "1uH", Pads: []*Pad{pad("1", "VBAT", 500), pad("2", "LX", 560)}},
		{Ref: "C1", Device: "22uF", Pads: []*Pad{pad("1", "+5V", 700), pad("2", "GND", 760)}},
		{Ref: "C2", Device: "10uF", Pads: []*Pad{pad("1", "VBAT", 900), pad("2", "GND", 960)}},
	}
	_ = b.Index()
	c := Understand(b, Analyze(b, PowerSpec{}, nil))
	if len(c.Converters) != 1 || c.Converters[0].Topology != "boost" || c.Converters[0].HotCap != "C1" {
		t.Fatalf("want boost with output cap C1 in the hot loop: %+v", c.Converters)
	}
}

// Placement must pull the buck's hot loop tight and keep the feedback
// divider off the switch node.
func TestPlaceBuckHotLoop(t *testing.T) {
	b := buckBoard()
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	cv := c.Converters[0]
	_, before, _, _ := HotLoop(b, an, cv)
	if _, err := Place(b, an, c, nil, PlaceOptions{Seed: 1, Moves: 800}); err != nil {
		t.Fatal(err)
	}
	_, perim, area, ok := HotLoop(b, an, cv)
	if !ok {
		t.Fatal("hot loop not traceable")
	}
	t.Logf("hot loop perimeter %.0f → %.0f mil, area %.0f mil²", before, perim, area)
	if perim > 450 {
		t.Errorf("hot loop too large: %.0f mil", perim)
	}
	l := b.Part("L1")
	for _, r := range cv.Feedback {
		if d := rectDist(b.Part(r).Body(), l.Body()); d < 60 {
			t.Errorf("%s only %.0f mil from the inductor", r, d)
		}
	}
}
