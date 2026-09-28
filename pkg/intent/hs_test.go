package intent

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// hsDesign builds a small design: a 5 V terminal + AMS1117 3V3 supply and
// one IC whose pins carry the given nets (a second IC mirrors them so every
// net has two pads).
func hsDesign(nets ...string) *powersim.Design {
	d := &powersim.Design{NetRole: map[string]string{"GND": "ground"}}
	d.Parts = append(d.Parts,
		&powersim.Part{Ref: "J9", MPN: "KF301-5.0-2P", Pins: []powersim.Pin{{Number: "1", Name: "1", Net: "5V_IN"}, {Number: "2", Name: "2", Net: "GND"}}},
		&powersim.Part{Ref: "U9", MPN: "AMS1117-3.3", Pins: []powersim.Pin{{Number: "1", Name: "GND", Net: "GND"}, {Number: "2", Name: "VOUT", Net: "+3V3"}, {Number: "3", Name: "VIN", Net: "5V_IN"}}},
	)
	for _, ref := range []string{"U1", "U2"} {
		p := &powersim.Part{Ref: ref, MPN: "GENERIC-" + ref, Pins: []powersim.Pin{{Number: "1", Name: "VDD", Net: "+3V3"}, {Number: "2", Name: "GND", Net: "GND"}}}
		for i, n := range nets {
			p.Pins = append(p.Pins, powersim.Pin{Number: string(rune('a' + i)), Name: n, Net: n})
		}
		d.Parts = append(d.Parts, p)
	}
	return d
}

func deriveHS(t *testing.T, d *powersim.Design, spec *Spec) *Intent {
	t.Helper()
	it, err := Derive(Input{Design: d, Libs: libs(t), Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

// PCIe "TXP" is not Ethernet, a USB3 connector's D+/D- pair is USB 2.0 and
// CH340_D+ is USB (2026-09-27: intent had PCIE20_TXP as ETH 100 Ω and
// USB3_OTG0_DP as a 5 mil USB3 pair).
func TestIntentHSClassificationOrder(t *testing.T) {
	it := deriveHS(t, hsDesign("PCIE20_TXP", "PCIE20_TXN", "USB3_OTG0_DP", "USB3_OTG0_DM", "USB3_SSTX_P", "USB3_SSTX_N",
		"CH340_D+", "CH340_D-", "ETH_MDI0_P", "ETH_MDI0_N", "HDMI_D0_P", "HDMI_D0_N"), nil)
	want := map[string][2]any{"PCIE20_TXP": {"PCIE", 85.0}, "USB3_OTG0_DP": {"USB", 90.0}, "USB3_SSTX_P": {"USB3", 90.0},
		"CH340_D+": {"USB", 90.0}, "ETH_MDI0_P": {"ETH", 100.0}, "HDMI_D0_P": {"HDMI", 100.0}}
	for net, w := range want {
		np := it.Nets[net]
		if np == nil || np.Role != "diff" || np.Interface != w[0] || np.ImpedanceOhm != w[1] {
			t.Errorf("%s: got %+v, want %v", net, np, w)
		}
	}
	if s := it.Nets["USB3_SSTX_P"]; s.MaxSkewMil != 5 || s.MaxVias != 2 {
		t.Errorf("USB3 limits %v/%v", s.MaxSkewMil, s.MaxVias)
	}
	if s := it.Nets["USB3_OTG0_DP"]; s.MaxSkewMil != 100 {
		t.Errorf("USB2 on a USB3 connector: skew %v, want 100", s.MaxSkewMil)
	}
}

// Widths are solved on the engine's own stackup (pcbauto.StackupReference):
// 90 Ω on JLC04161H-7628 (8.28 mil, εr 4.4, s = 6 mil) is 10.8 mil.
func TestIntentHSWidthOnEngineStackup(t *testing.T) {
	it := deriveHS(t, hsDesign("USB_DP", "USB_DM"), nil)
	if w := it.Nets["USB_DP"].WidthMil.Outer; w != 10.8 {
		t.Fatalf("USB 90 Ω width %.1f, want 10.8 (stackup 8.28 mil / εr 4.4)", w)
	}
	if !strings.Contains(it.Copper.Stackup, "JLC04161H-7628") || it.Copper.RefHeightMil != 8.28 || it.Copper.Er != 4.4 {
		t.Fatalf("copper %+v", it.Copper)
	}
}

// HDMI TMDS lanes of one port form one length group; DDR strobes/clocks are
// pairs and the byte lanes / fly-by bus are groups — all from the names.
func TestIntentHSAutoGroups(t *testing.T) {
	nets := []string{"HDMI_0P", "HDMI_0N", "HDMI_1P", "HDMI_1N", "HDMI_CP", "HDMI_CN",
		"DDR_A_DQS0P", "DDR_A_DQS0N", "DDR_DQ0_A", "DDR_DQ1_A", "DDR_DQ7_A", "DDR_DM0_A",
		"DDR_A_CLKP", "DDR_A_CLKN", "DDR_A0_A", "DDR_A1_A", "DDR_A_CS0", "DDR_A_ODTCA", "DDR_B_ODTCA", "DDR_B_CS0", "DDR_A0_B"}
	it := deriveHS(t, hsDesign(nets...), nil)
	for net, want := range map[string][2]any{
		"HDMI_0P": {"HDMI_LANES", 100.0}, "HDMI_CN": {"HDMI_LANES", 100.0},
		"DDR_A_DQS0P": {"DDR_A_BYTE0", 25.0}, "DDR_DQ7_A": {"DDR_A_BYTE0", 25.0}, "DDR_DM0_A": {"DDR_A_BYTE0", 25.0},
		"DDR_A_CLKN": {"DDR_A_ADDR", 100.0}, "DDR_A0_A": {"DDR_A_ADDR", 100.0}, "DDR_A_ODTCA": {"DDR_A_ADDR", 100.0},
		"DDR_B_ODTCA": {"DDR_B_ADDR", 100.0},
	} {
		np := it.Nets[net]
		if np == nil || np.LengthGroup != want[0] || np.LengthTolMil != want[1] {
			t.Errorf("%s: group %q ±%v, want %v", net, np.LengthGroup, np.LengthTolMil, want)
		}
	}
	if np := it.Nets["DDR_A_DQS0P"]; np.Role != "diff" || np.DiffPair != "DDR_A_DQS0N" || np.Interface != "DDR" {
		t.Errorf("DQS pair: %+v", np)
	}
	if np := it.Nets["DDR_DQ0_A"]; np.WidthMil.Outer != 6 {
		t.Errorf("auto DDR data keeps the routing width, got %.1f", np.WidthMil.Outer)
	}
}

// A declared interface sets the tolerance, skew and via budget.
func TestIntentHSSpecDeclaredLimits(t *testing.T) {
	spec := &Spec{HSInterfaces: []SpecHS{{Name: "DDR", Pairs: [][2]string{{"DQS0_P", "DQS0_N"}}, Nets: []string{"DQ0", "DQ1", "DQ2"},
		LengthGroup: "BYTE0", LengthTolMil: 20, MaxSkewMil: 3, MaxVias: 3, SingleOhm: 50}}}
	it := deriveHS(t, hsDesign("DQS0_P", "DQS0_N", "DQ0", "DQ1", "DQ2"), spec)
	if np := it.Nets["DQS0_P"]; np.MaxSkewMil != 3 || np.MaxVias != 3 || np.LengthTolMil != 20 || np.LengthGroup != "BYTE0" || np.Interface != "DDR" {
		t.Errorf("DQS0_P %+v", np)
	}
	if np := it.Nets["DQ1"]; np.Role != "hs" || np.LengthTolMil != 20 || np.MaxVias != 3 || np.ImpedanceOhm != 50 {
		t.Errorf("DQ1 %+v", np)
	}
}

// A 2-layer board cannot hold a ≥ 1 Gb/s pair: error, not only the
// impedance warning USB 2.0 gets.
func TestIntentHSTwoLayerReferenceMissing(t *testing.T) {
	it := deriveHS(t, hsDesign("USB3_SSTX_P", "USB3_SSTX_N", "USB_DP", "USB_DM"), &Spec{Layers: 2})
	var ref, unc *Finding
	for _, f := range it.Findings {
		switch f.Kind {
		case "reference-plane-missing":
			ref = f
		case "impedance-uncontrolled":
			unc = f
		}
	}
	if ref == nil || ref.Severity != "error" || !has(ref.Nets, "USB3_SSTX_P") || has(ref.Nets, "USB_DP") {
		t.Fatalf("reference-plane-missing: %+v", ref)
	}
	if unc == nil || !has(unc.Nets, "USB_DP") {
		t.Fatalf("impedance-uncontrolled: %+v", unc)
	}
}

// USB3/PCIe TX pairs need series AC caps; a missing pair of caps is a
// warning, a value outside 75–265 nF too.
func TestIntentHSACCoupling(t *testing.T) {
	d := hsDesign("PCIE_TX_P", "PCIE_TX_N")
	it := deriveHS(t, d, nil)
	found := false
	for _, f := range it.Findings {
		found = found || f.Kind == "ac-coupling-missing"
	}
	if !found {
		t.Fatal("no ac-coupling-missing for PCIe TX without caps")
	}
	// Move U2 behind 10 nF series caps: wrong value.
	for i := range d.Parts[3].Pins {
		switch d.Parts[3].Pins[i].Net {
		case "PCIE_TX_P":
			d.Parts[3].Pins[i].Net = "PCIE_TXC_P"
		case "PCIE_TX_N":
			d.Parts[3].Pins[i].Net = "PCIE_TXC_N"
		}
	}
	d.Parts = append(d.Parts,
		&powersim.Part{Ref: "C1", Value: "10nF", Pins: []powersim.Pin{{Number: "1", Net: "PCIE_TX_P"}, {Number: "2", Net: "PCIE_TXC_P"}}},
		&powersim.Part{Ref: "C2", Value: "10nF", Pins: []powersim.Pin{{Number: "1", Net: "PCIE_TX_N"}, {Number: "2", Net: "PCIE_TXC_N"}}})
	it = deriveHS(t, d, nil)
	kinds := map[string]bool{}
	for _, f := range it.Findings {
		kinds[f.Kind] = true
	}
	if kinds["ac-coupling-missing"] || !kinds["ac-coupling-value"] || !kinds["ac-coupling"] {
		t.Fatalf("findings %v", kinds)
	}
}

// A rail-named net fed only by a divider resistor is a sense signal.
func TestIntentResistorFedRailIsSignal(t *testing.T) {
	d := hsDesign()
	d.Parts = append(d.Parts, &powersim.Part{Ref: "R1", Value: "10k", Pins: []powersim.Pin{{Number: "1", Net: "5V_IN"}, {Number: "2", Net: "VBUS_DET"}}},
		&powersim.Part{Ref: "R2", Value: "10k", Pins: []powersim.Pin{{Number: "1", Net: "VBUS_DET"}, {Number: "2", Net: "GND"}}})
	d.Parts[2].Pins = append(d.Parts[2].Pins, powersim.Pin{Number: "z", Name: "VBUS_DET", Net: "VBUS_DET"})
	it := deriveHS(t, d, nil)
	if np := it.Nets["VBUS_DET"]; np.Role != "signal" || np.NetClass != "SIGNAL" {
		t.Fatalf("VBUS_DET %s / %s", np.Role, np.NetClass)
	}
	if np := it.Nets["+3V3"]; np.Role != "power" {
		t.Fatalf("+3V3 %s", np.Role)
	}
}
