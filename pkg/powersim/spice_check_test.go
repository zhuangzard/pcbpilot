package powersim

import "testing"

// A node held only by a reverse-biased diode (a relay coil node with its
// driver off and the coil off-board) has a leakage-set voltage: a mismatch
// there is a warning, while the same mismatch on a resistor-held node fails.
func TestSpiceCompareHighZ(t *testing.T) {
	c := NewCircuit()
	vcc, mid, coil := c.Node("VCC"), c.Node("MID"), c.Node("COIL_N")
	c.add(&vsource{t: [2]Terminal{T(vcc), T(Ground)}, e: 12})
	c.add(&resistor{t: [2]Terminal{T(vcc), T(mid)}, r: 1000})
	c.add(&resistor{t: [2]Terminal{T(mid), T(Ground)}, r: 1000})
	c.add(&diode{t: [2]Terminal{T(coil), T(vcc)}, is: 1e-14, n: 1}) // flyback diode, reverse-biased
	x := solveOK(t, c)
	c.Node("LOOKED_UP_LATER") // result reporting may add names after the solve
	sn := c.spiceNames()
	spice := map[string]float64{}
	for s, net := range sn.net {
		if c.index[net] >= len(x)-c.nb {
			continue
		}
		spice[s] = x[c.index[net]]
	}
	at := func(net string) string {
		for s, n := range sn.net {
			if n == net {
				return s
			}
		}
		t.Fatalf("no spice node for %s", net)
		return ""
	}

	spice[at("COIL_N")] += 0.011
	ck := &SpiceCheck{TolV: 1e-3, HighZOhm: spiceHighZOhm}
	ck.compare(c, x, sn, spice, 1e-3)
	if !ck.Pass || len(ck.Diffs) != 0 {
		t.Fatalf("high-Z mismatch failed the check: %+v", ck)
	}
	h, ok := ck.HighZ["COIL_N"]
	if !ok || h.ZOhm < spiceHighZOhm || h.DiffV != 0.011 {
		t.Fatalf("high-Z warning: %+v", ck.HighZ)
	}

	spice[at("MID")] += 0.011
	ck = &SpiceCheck{TolV: 1e-3, HighZOhm: spiceHighZOhm}
	ck.compare(c, x, sn, spice, 1e-3)
	if ck.Pass || ck.Diffs["MID"] != 0.011 {
		t.Fatalf("a 500 Ω node's 11 mV mismatch must fail: %+v", ck)
	}
}
