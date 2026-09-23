package pcbauto

import (
	"math"
	"testing"
)

func TestCapFarads(t *testing.T) {
	cases := map[string]float64{
		"100nF": 100e-9, "0.1uF": 100e-9, "4u7": 4.7e-6, "22pF": 22e-12, "10uF 25V": 10e-6,
		"CL05B104KO5NNNC": 100e-9, "CL10A106KP8NNNC": 10e-6, "GRM155R71C104KA88D": 100e-9,
		"CC0402KRX7R9BB104": 100e-9, "LM358": 0,
	}
	for in, want := range cases {
		if got := CapFarads(in); math.Abs(got-want) > want*1e-6+1e-18 {
			t.Errorf("%s: got %g want %g", in, got, want)
		}
	}
}
