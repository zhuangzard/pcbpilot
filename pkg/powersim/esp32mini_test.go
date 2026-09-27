package powersim

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const esp32Dir = "testdata/esp32mini"

func loadESP32Mini(t *testing.T) *Design {
	t.Helper()
	var pages []*ConnDoc
	for _, f := range []string{"sch-905bb85957eaf435.json", "sch-950ae6609e91d753.json"} {
		b, err := os.ReadFile(filepath.Join(esp32Dir, f))
		if err != nil {
			t.Fatal(err)
		}
		d, err := ParseConnectivity(b)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, d)
	}
	vb, err := os.ReadFile(filepath.Join(esp32Dir, "values.json"))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := ParseValues(vb)
	if err != nil {
		t.Fatal(err)
	}
	d, warns, err := BuildDesign(pages, vals)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) > 0 {
		t.Logf("design warnings: %v", warns)
	}
	return d
}

func skillLibrary(t *testing.T) Libraries {
	t.Helper()
	lib, err := LoadLibrary("../../.agents/skills/pcbpilot/references/power-models.json")
	if err != nil {
		t.Fatal(err)
	}
	return Libraries{lib}
}

// TestESP32MiniReplay replays the real ceshi E2E schematic (2 pages) with the
// skill's power-model library and checks the numbers against hand formulas.
func TestESP32MiniReplay(t *testing.T) {
	d := loadESP32Mini(t)
	out, eng, err := Simulate(d, skillLibrary(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out.Scenarios, ","); got != "typical,peak,buttons-pressed,terminal-only,usb-only,worst" {
		t.Fatalf("scenarios = %s", got)
	}
	for _, res := range out.Results {
		if !res.Converged {
			t.Fatalf("%s did not converge", res.Scenario)
		}
		if res.Scenario != "worst" {
			assertKCL(t, &res)
		}
	}
	vout := 0.6 * (1 + 45.3/10)
	for _, name := range []string{"typical", "peak", "terminal-only", "usb-only"} {
		res := findResult(t, out, name)
		near(t, name+" +3V3", res.Nets["+3V3"].Voltage, vout, 2e-3)
		u4 := res.Parts["U4"]
		if u4.Mode != "regulating" {
			t.Fatalf("%s U4 %s", name, u4.Mode)
		}
		// Bus current into the buck: Iin = V(LX)·Iout/(η·Vin) + Iq, and within
		// 3 % of the hand estimate 3.3·I3V3/(0.9·(5−Vf)).
		i3v3 := res.Nets["+3V3"].CurrentA
		// +5V also feeds the reverse leakage of the idle OR diode (≤ 0.2 mA).
		iBus := pinCurrent(t, res, "+5V", "U4", "4").CurrentA
		near(t, name+" I(+5V) ≈ U4.IN", res.Nets["+5V"].CurrentA, iBus, 2e-4)
		near(t, name+" Iin formula", iBus, res.Nets["SW"].Voltage*i3v3/(0.9*u4.VinV)+5e-5, 3e-5) // V rounded to 0.1 mV
		hand := 3.3 * i3v3 / (0.9 * (5 - 0.45))
		if math.Abs(iBus-hand)/hand > 0.03 {
			t.Fatalf("%s I(+5V)=%.4f vs hand %.4f", name, iBus, hand)
		}
	}
	peak := findResult(t, out, "peak")
	typ := findResult(t, out, "typical")
	// ESP32 3V3 pin = model current + LED GPIO current; CH340C on VCC only.
	led := pinCurrent(t, typ, "LED_A", "LED1", "1").CurrentA
	near(t, "U1.3V3 peak", pinCurrent(t, peak, "+3V3", "U1", "2").CurrentA, 0.5+pinCurrent(t, peak, "LED_A", "LED1", "1").CurrentA, 1e-6)
	near(t, "U1.3V3 typical", pinCurrent(t, typ, "+3V3", "U1", "2").CurrentA, 0.1+led, 1e-6)
	near(t, "U3.VCC peak", pinCurrent(t, peak, "+3V3", "U3", "16").CurrentA, 0.02, 1e-9)
	near(t, "U3.V3", pinCurrent(t, peak, "+3V3", "U3", "4").CurrentA, 0, 1e-9)
	// Return split across the module's three ground pads.
	for _, pin := range []string{"1", "40", "41"} {
		near(t, "U1 GND pin "+pin, pinCurrent(t, peak, "GND", "U1", pin).CurrentA, 0.5/3, 1e-6)
	}
	// LED: IO2 → R9 (1 kΩ) → LED1 (yellow) with 40 Ω GPIO output resistance.
	vA := typ.Nets["LED_A"].Voltage
	near(t, "I(LED) = (V(3V3)−V(LED_A))/(1k+40Ω)", led, (typ.Nets["+3V3"].Voltage-vA)/1040, 1e-7)
	if led < 1.2e-3 || led > 1.6e-3 || vA < 1.8 || vA > 2.0 {
		t.Fatalf("LED current %.3f mA at Vf %.3f V out of the yellow-LED range", led*1e3, vA)
	}
	// USB-C VBUS pads share the bus current; in usb-only D2 carries all of it.
	usb := findResult(t, out, "usb-only")
	a, b := pinCurrent(t, usb, "VBUS", "J2", "A4B9"), pinCurrent(t, usb, "VBUS", "J2", "B4A9")
	near(t, "VBUS split", a.CurrentA, b.CurrentA, 1e-9)
	near(t, "D2 carries the bus in usb-only", pinCurrent(t, usb, "+5V", "D2", "1").CurrentA, usb.Nets["+5V"].CurrentA, 1e-7)
	if a.Dir != "source" || pinCurrent(t, usb, "+5V", "D1", "1").CurrentA > 1e-3 {
		t.Fatalf("usb-only: J2 must source and D1 must be off")
	}
	term := findResult(t, out, "terminal-only")
	if pinCurrent(t, term, "VBUS", "D2", "2").CurrentA > 1e-3 || pinCurrent(t, term, "5V_TERM", "J1", "1").CurrentA < 0.4 {
		t.Fatalf("terminal-only: J1 must carry the board")
	}
	// Worst = per-pin max over scenarios.
	w := findResult(t, out, "worst")
	for _, net := range []string{"+5V", "VBUS", "5V_TERM", "+3V3", "GND", "SW"} {
		for _, p := range w.Nets[net].Pins {
			max := 0.0
			for _, res := range out.Results {
				if res.Scenario == "worst" {
					continue
				}
				max = math.Max(max, pinCurrent(t, &res, net, p.Ref, p.Pin).CurrentA)
			}
			near(t, "worst "+p.Ref+"."+p.Pin, p.CurrentA, max, 1e-12)
		}
	}
	if w.Nets["SW"].Role != "switch" || w.Nets["5V_TERM"].Role != "power" || w.Nets["USB_DP"].Role != "signal" {
		t.Fatalf("roles SW=%s 5V_TERM=%s USB_DP=%s", w.Nets["SW"].Role, w.Nets["5V_TERM"].Role, w.Nets["USB_DP"].Role)
	}
	// Buck ripple entries exist for the switch node, inductor and caps.
	for _, k := range []string{"SW", "L1", "C1", "C2"} {
		if peak.Ripple[k] == nil || peak.Ripple[k].IRmsA <= 0 {
			t.Fatalf("ripple %s missing", k)
		}
	}
	if len(peak.Warnings) != 0 {
		t.Fatalf("peak warnings: %v", peak.Warnings)
	}
	// Cross-check against ngspice when it is installed.
	if ck := eng.CheckSPICE("peak", 1e-3); ck.Ran && !ck.Pass {
		t.Fatalf("ngspice mismatch: %+v", ck)
	} else {
		t.Logf("spice check: %+v", ck)
	}
}
