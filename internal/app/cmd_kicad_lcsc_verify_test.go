package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func TestLcscValue(t *testing.T) {
	for in, want := range map[string]float64{
		"10k": 10e3, "4k7": 4.7e3, "2R2": 2.2, "100nF": 100e-9, "0.1u": 0.1e-6, "10uF": 10e-6,
		"1µF": 1e-6, "4.7uH": 4.7e-6, "10kΩ": 10e3, "0R": 0, "4n7": 4.7e-9, "1M": 1e6, "49.9": 49.9,
	} {
		got, ok := lcscValue(in)
		if !ok || (want == 0 && got != 0) || (want != 0 && (got/want < 0.999 || got/want > 1.001)) {
			t.Errorf("%q: %g %v, want %g", in, got, ok, want)
		}
	}
	for kind, d := range map[string]string{
		"R": "-55℃~+155℃ 10kΩ 50V 62.5mW Thick Film Resistor ±1% 0402",
		"C": "50V 100nF X7R ±10% 0402 Multilayer Ceramic Capacitors MLCC",
		"L": "4.7uH ±20% 1.2A SMD Power Inductor",
	} {
		v, ok := lcscDescValue(kind, d)
		want := map[string]float64{"R": 10e3, "C": 100e-9, "L": 4.7e-6}[kind]
		if !ok || v/want < 0.999 || v/want > 1.001 {
			t.Errorf("%s: %g %v", kind, v, ok)
		}
	}
}

func TestLcscChipSize(t *testing.T) {
	for in, want := range map[string]string{
		"R_0402_1005Metric": "0402", "C_0805_2012Metric_Pad1.18x1.45mm": "0805", "0603": "0603",
		"SOT-23-5": "", "LQFP-144_20x20mm_P0.5mm": "", "QFN-20-1EP_4x4mm": "", "R_1206_3216Metric": "1206",
	} {
		if got := lcscChipSize(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
}

func TestLcscVerify(t *testing.T) {
	cat := map[string]kicad.EasyEDAPart{
		"C25744": {LCSC: "C25744", Package: "R0402", Value: "10kΩ", Description: "10KΩ (1002) ±1%", Stock: 1000},
		"C1525":  {LCSC: "C1525", Package: "C0402", Description: "100nF (104) ±10% 16V", Stock: 1000},
		"C6186":  {LCSC: "C6186", Package: "SOT-223-3_L6.5-W3.4-P2.30-LS7.0-BR", Stock: 0},
		"C2286":  {LCSC: "C2286", Package: "LED-SMD_L1.6-W0.8-R-RD", URL: "https://lcsc.com/product-detail/Light-Emitting-Diodes-LED_Red-LED-0603_C2286.html", Stock: 9},
	}
	parts := []kicad.Part{
		{Ref: "R1", Value: "10k", Footprint: "Resistor_SMD:R_0402_1005Metric", LCSC: "C25744"},  // ok
		{Ref: "R2", Value: "4k7", Footprint: "Resistor_SMD:R_0402_1005Metric", LCSC: "C25744"},  // value
		{Ref: "C1", Value: "100n", Footprint: "Capacitor_SMD:C_0603_1608Metric", LCSC: "C1525"}, // size
		{Ref: "U1", Value: "AMS1117-3.3", Footprint: "Package_TO_SOT_SMD:SOT-223-3_TabPin2", LCSC: "C6186"},
		{Ref: "R3", Value: "1k", Footprint: "R_0402", LCSC: "C999999999"},                  // not found
		{Ref: "R4", Value: "1k", Footprint: "R_0402", LCSC: "C999999999", DNP: true},       // not assembled
		{Ref: "D1", Value: "RED", Footprint: "LED_SMD:LED_0603_1608Metric", LCSC: "C2286"}, // size from the URL, ok
		{Ref: "D2", Value: "RED", Footprint: "LED_SMD:LED_0805_2012Metric", LCSC: "C2286"}, // size
	}
	got := map[string]string{}
	for _, f := range lcscVerify(parts, cat) {
		got[f.Ref] += f.Level + ":" + f.What + ";"
	}
	if _, ok := got["R1"]; ok {
		t.Errorf("R1 flagged: %v", got["R1"])
	}
	if _, ok := got["D1"]; ok {
		t.Errorf("D1 flagged: %v", got["D1"])
	}
	for ref, want := range map[string]string{"R2": "error:value 4k7", "C1": "error:footprint size 0603, catalog package 0402", "D2": "error:footprint size 0805", "U1": "warning:LCSC stock 0", "R3": "error:LCSC number not found"} {
		if !strings.Contains(got[ref], want) {
			t.Errorf("%s: %q, want %q", ref, got[ref], want)
		}
	}
	if _, ok := got["R4"]; ok {
		t.Error("a DNP part was verified")
	}
}

func TestLcscLookup(t *testing.T) {
	cat, err := lcscLookup([]string{"C1", "C2", "C3"}, func(n string) (kicad.EasyEDAPart, bool, error) {
		return kicad.EasyEDAPart{LCSC: n, MPN: "m" + n}, n != "C3", nil
	})
	if err != nil || len(cat) != 2 || cat["C2"].MPN != "mC2" {
		t.Fatalf("%v %v", cat, err)
	}
	if _, err := lcscLookup([]string{"C1"}, func(string) (kicad.EasyEDAPart, bool, error) {
		return kicad.EasyEDAPart{}, false, fmt.Errorf("offline")
	}); err == nil {
		t.Fatal("lookup error lost")
	}
}
