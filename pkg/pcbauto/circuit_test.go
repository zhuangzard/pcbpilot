package pcbauto

import (
	"testing"
)

// isoBoard builds a tiny mains-to-SELV board: a mains connector and fuse on
// the MAINS side, an optocoupler bridging to a 3V3 MCU side.
func isoBoard() *Board {
	pad := func(n, net string, x, y float64) *Pad {
		return &Pad{Number: n, Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{x, y}, W: 40, H: 60}}
	}
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 3000, 2000}.Corners()}
	b.Parts = []*Part{
		{Ref: "J1", Device: "KF301-2P", Pads: []*Pad{pad("1", "AC_L", 100, 1000), pad("2", "AC_N", 100, 1200)}},
		{Ref: "F1", Device: "FUSE", Pads: []*Pad{pad("1", "AC_L", 300, 1000), pad("2", "L_F", 400, 1000)}},
		{Ref: "R1", Device: "100k", Pads: []*Pad{pad("1", "L_F", 600, 1000), pad("2", "LED_A", 700, 1000)}},
		{Ref: "U1", Device: "PC817", Pads: []*Pad{
			pad("1", "LED_A", 1000, 1000), pad("2", "AC_N", 1000, 900),
			pad("3", "GND", 1300, 900), pad("4", "ZC", 1300, 1000)}},
		{Ref: "R2", Device: "10k", Pads: []*Pad{pad("1", "ZC", 1500, 1000), pad("2", "+3V3", 1600, 1000)}},
		{Ref: "U2", Device: "STM32G030", Pads: []*Pad{
			pad("1", "+3V3", 2000, 1000), pad("2", "GND", 2000, 900), pad("3", "ZC", 2000, 800),
			pad("4", "SDA", 2100, 1000), pad("5", "SCL", 2100, 900), pad("6", "NRST", 2100, 800)}},
		{Ref: "C1", Device: "100nF", Pads: []*Pad{pad("1", "+3V3", 1900, 1100), pad("2", "GND", 1900, 1200)}},
	}
	_ = b.Index()
	return b
}

func TestUnderstandIsolation(t *testing.T) {
	b := isoBoard()
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	if c.Kinds["U1"] != KindOpto {
		t.Fatalf("U1 kind %s", c.Kinds["U1"])
	}
	if len(c.Domains) != 2 {
		for _, d := range c.Domains {
			t.Logf("domain %+v", d)
		}
		t.Fatalf("want 2 domains, got %d", len(c.Domains))
	}
	var mains *Domain
	for _, d := range c.Domains {
		if d.Mains {
			mains = d
		}
	}
	if mains == nil || !mains.Hazardous || !containsStr(mains.Parts, "R1") || containsStr(mains.Parts, "U2") {
		t.Fatalf("mains domain wrong: %+v", mains)
	}
	if len(c.Barriers) != 1 || c.Barriers[0].Insulation != "reinforced" {
		t.Fatalf("barriers %+v", c.Barriers)
	}
	br := c.Barriers[0]
	if br.CreepageMil < 190 || br.ClearanceMil < 150 {
		t.Fatalf("reinforced 250V distances too small: %+v", br)
	}
	if !containsStr(br.Bridges, "U1") {
		t.Fatalf("bridge %v", br.Bridges)
	}
	if c.BlockOf["C1"] != "B-U2" {
		t.Fatalf("decap C1 should belong to U2, got %s", c.BlockOf["C1"])
	}
	t.Logf("barrier %+v", br)
}

func TestInsulationTable(t *testing.T) {
	cr, cl := InsulationDistances(250, "basic")
	if cr != 2.5 || cl != 2.0 {
		t.Fatalf("basic 250V: %v %v", cr, cl)
	}
	cr, cl = InsulationDistances(250, "reinforced")
	if cr != 5 || cl != 4 {
		t.Fatalf("reinforced 250V: %v %v", cr, cl)
	}
}

func TestUnderstandRealBoard(t *testing.T) {
	b := loadFixture(t, "lckfb-szpi-esp32s3.json")
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	t.Logf("%d blocks, %d links, %d domains", len(c.Blocks), len(c.Links), len(c.Domains))
	for i, bl := range c.Blocks {
		if i < 10 {
			t.Logf("block %s kind=%s domain=%s parts=%d %v", bl.ID, bl.Kind, bl.Domain, len(bl.Parts), bl.Parts)
		}
	}
	for i, l := range c.Links {
		if i < 8 {
			t.Logf("link %s→%s %v (%d nets)", l.From, l.To, l.Kinds, len(l.Nets))
		}
	}
	for _, d := range c.Domains {
		t.Logf("domain %s ground=%s maxV=%.1f parts=%d", d.ID, d.Ground, d.MaxVoltage, len(d.Parts))
	}
	if len(c.Blocks) < 5 {
		t.Fatalf("too few blocks")
	}
}

func TestDiffPairNames(t *testing.T) {
	names := map[string]bool{"USB_D+": true, "USB_D-": true, "D+": true, "D-": true, "BACK_LED+": true, "BACK_LED-": true,
		"MIPI_DSI_0P": true, "MIPI_DSI_0N": true, "I2C_DSI_VCC_LED+": true, "I2C_DSI_VCC_LED-": true, "USB1_DP": true, "USB1_DM": true}
	want := map[string]bool{"USB_D+": true, "D+": true, "MIPI_DSI_0P": true, "USB1_DP": true}
	for n := range names {
		got := looksDiffName(n) && pairPartner(n, names) != ""
		if want[n] && !got {
			t.Errorf("%s should pair", n)
		}
		if !want[n] && got && (n == "BACK_LED+" || n == "I2C_DSI_VCC_LED+" || n == "BACK_LED-" || n == "I2C_DSI_VCC_LED-") {
			t.Errorf("%s is polarity, not a pair", n)
		}
	}
}
