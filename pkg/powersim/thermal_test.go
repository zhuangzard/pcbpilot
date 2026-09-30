package powersim

import (
	"math"
	"testing"
)

// TestThermalAverageBound: heat is integrated over time, so a peak scenario
// must hand the thermal solve its time-averaged power, while current (the
// basis of IR drop, track width and via count) stays at the peak. Regression
// for the offline E2E that reported a 192 °C board because the ESP32's 0.5 A
// Wi-Fi TX burst was treated as a continuous 1.66 W.
func TestThermalAverageBound(t *testing.T) {
	out, _, err := Simulate(loadESP32Mini(t), skillLibrary(t), Options{})
	if err != nil {
		t.Fatal(err)
	}
	typ, peak, w := findResult(t, out, "typical"), findResult(t, out, "peak"), findResult(t, out, "worst")
	if typ.ThermalBasis != ThermalAverage || peak.ThermalBasis != ThermalAverageBound || w.ThermalBasis != ThermalAverageBound {
		t.Fatalf("bases typical=%q peak=%q worst=%q", typ.ThermalBasis, peak.ThermalBasis, w.ThermalBasis)
	}
	for _, r := range out.Results {
		if r.Scenario == "terminal-only" || r.Scenario == "usb-only" {
			if r.ThermalBasis != ThermalAverageBound {
				t.Fatalf("%s is a peak scenario: basis %q", r.Scenario, r.ThermalBasis)
			}
		}
	}
	// Average scenario: thermal = operating point.
	for ref, p := range typ.Parts {
		if p.ThermalW != p.PowerW {
			t.Fatalf("typical %s thermalW %.6f ≠ powerW %.6f", ref, p.ThermalW, p.PowerW)
		}
	}
	// The ESP32 (U1 here) is a load: peak power stays 3.3 V × 0.5 A (+LED
	// GPIO), thermal is its average 3.3 V × 0.1 A (+LED GPIO).
	u1 := peak.Parts["U1"]
	v := peak.Nets["+3V3"].Voltage
	if math.Abs(u1.PowerW-v*0.5)/(v*0.5) > 0.02 {
		t.Fatalf("U1 peak power %.4f W, want ≈ %.4f", u1.PowerW, v*0.5)
	}
	if math.Abs(u1.ThermalW-typ.Parts["U1"].PowerW) > 1e-6 || math.Abs(u1.ThermalW-v*0.1)/(v*0.1) > 0.05 {
		t.Fatalf("U1 thermal %.4f W, want the average ≈ %.4f (typical %.4f)", u1.ThermalW, v*0.1, typ.Parts["U1"].PowerW)
	}
	if u1.MPN != "ESP32-S3-WROOM-1" {
		t.Fatalf("U1 mpn %q", u1.MPN)
	}
	// Buck (linear in load): average. Inductor DCR (I²R): √(P_avg·P_peak).
	u4 := peak.Parts["U4"]
	if u4.ThermalW >= u4.PowerW || u4.ThermalW <= 0 {
		t.Fatalf("U4 thermal %.4f vs peak %.4f", u4.ThermalW, u4.PowerW)
	}
	l1a, l1p := typ.Parts["L1"].PowerW, peak.Parts["L1"].PowerW
	if got, want := peak.Parts["L1"].ThermalW, math.Sqrt(l1a*l1p); math.Abs(got-want) > 1e-6 || got < l1a || got > l1p {
		t.Fatalf("L1 thermal %.6f, want √(%.6f·%.6f)=%.6f", got, l1a, l1p, want)
	}
	// Copper: current keeps the peak; the thermal RMS bound is √(I_avg·I_peak).
	ia, ip := typ.Nets["+3V3"].CurrentA, peak.Nets["+3V3"].CurrentA
	if ip < 0.5 {
		t.Fatalf("+3V3 peak current %.3f A must stay the peak (current capacity)", ip)
	}
	if got := peak.Nets["+3V3"].ThermalCurrentA; math.Abs(got-math.Sqrt(ia*ip)) > 1e-6 {
		t.Fatalf("+3V3 thermal current %.4f, want %.4f", got, math.Sqrt(ia*ip))
	}
	// Board heat: the average total is a fraction of the peak one.
	sum := func(r *Result, th bool) float64 {
		s := 0.0
		for _, p := range r.Parts {
			x := p.PowerW
			if th {
				x = p.ThermalW
			}
			s += math.Max(x, 0)
		}
		return s
	}
	if pk, th := sum(peak, false), sum(peak, true); th > 0.35*pk || th < sum(typ, true) {
		t.Fatalf("peak heat %.3f W, thermal %.3f W (typical %.3f W)", pk, th, sum(typ, true))
	}
	// Worst folds the thermal maxima too.
	for ref, p := range w.Parts {
		m := 0.0
		for _, r := range out.Results {
			if r.Scenario != "worst" && r.Parts[ref] != nil {
				m = math.Max(m, r.Parts[ref].ThermalW)
			}
		}
		if math.Abs(p.ThermalW-m) > 1e-12 {
			t.Fatalf("worst %s thermalW %.6f, want max %.6f", ref, p.ThermalW, m)
		}
	}
}

func TestBoundedRMS(t *testing.T) {
	for _, c := range []struct{ a, p, want float64 }{
		{0.1, 0.5, math.Sqrt(0.05)}, {0.5, 0.5, 0.5}, {0.6, 0.5, 0.6}, {0, 0.5, 0}, {0.1, 0, 0.1}, {-1, 1, 0},
	} {
		if got := boundedRMS(c.a, c.p); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("boundedRMS(%g,%g) = %g, want %g", c.a, c.p, got, c.want)
		}
	}
}
