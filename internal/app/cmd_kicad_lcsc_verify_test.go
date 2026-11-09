package app

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func TestLcscValue(t *testing.T) {
	for in, want := range map[string]float64{
		"10k": 10e3, "4k7": 4.7e3, "2R2": 2.2, "100nF": 100e-9, "0.1u": 0.1e-6, "10uF": 10e-6,
		"1µF": 1e-6, "4.7uH": 4.7e-6, "10kΩ": 10e3, "0R": 0, "4n7": 4.7e-9, "1M": 1e6, "49.9": 49.9,
		"10K 1%": 10e3, "100nF/50V": 100e-9, "600R@100MHz": 600, "2.2uF": 2.2e-6,
	} {
		got, ok := lcscValue(in)
		if !ok || (want == 0 && got != 0) || (want != 0 && (got/want < 0.999 || got/want > 1.001)) {
			t.Errorf("%q: %g %v, want %g", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "DNP", "NTC"} {
		if _, ok := lcscValue(in); ok {
			t.Errorf("%q read as a value", in)
		}
	}
	for kind, h := range map[string]kicad.EasyEDAPart{
		"R": {Value: "10kΩ"},
		"C": {Description: "100nF (104) ±10% 16V"},
		"L": {Description: "4.7uH ±20% 1.2A SMD Power Inductor"},
	} {
		v, ok := lcscRecordValue(kind, h)
		want := map[string]float64{"R": 10e3, "C": 100e-9, "L": 4.7e-6}[kind]
		if !ok || v/want < 0.999 || v/want > 1.001 {
			t.Errorf("%s: %g %v", kind, v, ok)
		}
	}
	if v, ok := lcscRecordValue("L", kicad.EasyEDAPart{Description: "600Ω@100MHz ferrite bead"}); !ok || v != 600 {
		t.Errorf("ferrite: %g %v", v, ok)
	}
}

func TestLcscChipSize(t *testing.T) {
	for in, want := range map[string]string{
		"R_0402_1005Metric": "0402", "C_0805_2012Metric_Pad1.18x1.45mm": "0805", "0603": "0603", "R0402": "0402",
		"SOT-23-5": "", "LQFP-144_20x20mm_P0.5mm": "", "QFN-20-1EP_4x4mm": "", "R_1206_3216Metric": "1206",
		"R_1005Metric": "0402", "C_0603Metric": "0201", "Custom2010Sensor_R": "", "Conn_P0.2010mm": "",
		"https://lcsc.com/product-detail/Light-Emitting-Diodes-LED_Red-LED-0603_C2286.html": "0603",
		"https://lcsc.com/product-detail/LEDs_XYZ_C2010.html":                               "",
		"0402WGF1002TCE": "",
	} {
		if got := lcscChipSize(in); got != want {
			t.Errorf("%q: %q want %q", in, got, want)
		}
	}
}

func TestLcscVerify(t *testing.T) {
	cat := map[string]kicad.EasyEDAPart{
		"C25744": {LCSC: "C25744", Package: "R0402", Value: "10kΩ", Description: "10KΩ (1002) ±1%", Stock: 1000},
		"C17168": {LCSC: "C17168", Package: "R0402", Value: "0Ω", Stock: 1000},
		"C1525":  {LCSC: "C1525", Package: "C0402", Description: "100nF (104) ±10% 16V", Stock: 1000},
		"C6186":  {LCSC: "C6186", Package: "SOT-223-3_L6.5-W3.4-P2.30-LS7.0-BR", Stock: 0},
		"C2286":  {LCSC: "C2286", Package: "LED-SMD_L1.6-W0.8-R-RD", URL: "https://lcsc.com/product-detail/Light-Emitting-Diodes-LED_Red-LED-0603_C2286.html", Stock: 9},
		"C9999":  {LCSC: "C9999", Package: "LED-SMD_L1.6-W0.8", Stock: 9},
		"C8888":  {LCSC: "C8888", Package: "R-SMD", Value: "10kΩ", Stock: 9},
	}
	parts := []kicad.Part{
		{Ref: "R1", Value: "10k", Footprint: "Resistor_SMD:R_0402_1005Metric", LCSC: "C25744"},  // ok
		{Ref: "R2", Value: "4k7", Footprint: "Resistor_SMD:R_0402_1005Metric", LCSC: "C25744"},  // value
		{Ref: "C1", Value: "100n", Footprint: "Capacitor_SMD:C_0603_1608Metric", LCSC: "C1525"}, // size
		{Ref: "U1", Value: "AMS1117-3.3", Footprint: "Package_TO_SOT_SMD:SOT-223-3_TabPin2", LCSC: "C6186"},
		{Ref: "R3", Value: "1k", Footprint: "R_0402", LCSC: "C999999999"},            // no record
		{Ref: "R4", Value: "1k", Footprint: "R_0402", LCSC: "C999999999", DNP: true}, // not assembled
		{Ref: "D1", Value: "RED", Footprint: "LED_SMD:LED_0603_1608Metric", LCSC: "C2286"},
		{Ref: "D2", Value: "RED", Footprint: "LED_SMD:LED_0805_2012Metric", LCSC: "C2286"},
		{Ref: "R5", Value: "0R", Footprint: "R_0402_1005Metric", LCSC: "C25744"},     // zero vs 10k
		{Ref: "R6", Value: "0R", Footprint: "R_0402_1005Metric", LCSC: "C17168"},     // ok
		{Ref: "R7", Value: "10K 1%", Footprint: "R_0402_1005Metric", LCSC: "C25744"}, // ok
		{Ref: "R8", Value: "100n", Footprint: "R_0402_1005Metric", LCSC: "C1525"},    // a capacitor on a resistor
		{Ref: "R9", Value: "NTC", Footprint: "R_0402_1005Metric", LCSC: "C25744"},    // unreadable value
		{Ref: "D3", Value: "RED", Footprint: "LED_0603_1608Metric", LCSC: "C9999"},   // no size: warning
		{Ref: "R10", Value: "10k", Footprint: "R_0402_1005Metric", LCSC: "C8888"},    // no size: error
	}
	got := map[string]string{}
	for _, f := range lcscVerify(parts, cat) {
		got[f.Ref] += f.Level + ":" + f.What + ";"
	}
	for _, ref := range []string{"R1", "D1", "R6", "R7", "R4"} {
		if g, ok := got[ref]; ok {
			t.Errorf("%s flagged: %v", ref, g)
		}
	}
	for ref, want := range map[string]string{
		"R2": "error:value 4k7", "C1": "error:footprint size 0603, record package 0402", "D2": "error:footprint size 0805",
		"U1": "warning:LCSC stock 0", "R3": "error:LCSC number has no record", "R5": "error:value 0R",
		"R8": "error:the record states no resistance", "R9": "error:value \"NTC\" cannot be read",
		"D3": "warning:footprint size 0603, the record names no chip size", "R10": "error:footprint size 0402, the record names no chip size",
	} {
		if !strings.Contains(got[ref], want) {
			t.Errorf("%s: %q, want %q", ref, got[ref], want)
		}
	}
}

func TestLcscLookup(t *testing.T) {
	ctx := context.Background()
	cat, err := lcscLookup(ctx, []string{"C1", "C2", "C3"}, func(_ context.Context, n string) (kicad.EasyEDAPart, bool, error) {
		return kicad.EasyEDAPart{LCSC: n, MPN: "m" + n}, n != "C3", nil
	})
	if err != nil || len(cat) != 2 || cat["C2"].MPN != "mC2" {
		t.Fatalf("%v %v", cat, err)
	}
	// The first error cancels the rest and returns nothing partial.
	var calls atomic.Int32
	cat, err = lcscLookup(ctx, []string{"C1", "C2", "C3", "C4", "C5", "C6"}, func(c context.Context, n string) (kicad.EasyEDAPart, bool, error) {
		calls.Add(1)
		if n == "C1" {
			return kicad.EasyEDAPart{}, false, fmt.Errorf("offline")
		}
		<-c.Done()
		return kicad.EasyEDAPart{}, false, c.Err()
	})
	if err == nil || cat != nil || calls.Load() > 3 {
		t.Fatalf("cat %v err %v calls %d", cat, err, calls.Load())
	}
}
