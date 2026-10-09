package postsim

import "testing"

// A pre-routed pad escape (a short narrower stub at the load pad, the rest
// of the net at full width) is board copper like any track: the post-layout
// model carries its narrower section, so the drop to the pad grows by the
// stub's extra resistance — the IR budget sees the neck at the pin.
func TestEscapeStubNarrowsIRPath(t *testing.T) {
	drop := func(stubW float64) float64 {
		b := newTB(2000, 300, 2)
		b.part("S", 1, pad("1", "VIN", 1, 200, 150, 40, 40))
		b.part("L", 1, pad("1", "VIN", 1, 1800, 150, 8, 34))
		b.line("VIN", 1, 200, 150, 1740, 150, 40)
		b.line("VIN", 1, 1740, 150, 1800, 150, stubW) // the escape stub
		sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 2.0}, {"L", "1", "sink", 2.0}}}, nil, nil)
		r, err := Run(b.json(), sim, DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		n := netOf(t, r, "VIN")
		if n.Status == "open" {
			t.Fatalf("stub %.1f mil: VIN open", stubW)
		}
		return n.WorstMV
	}
	full, stub := drop(40), drop(11.4)
	if stub <= full*1.02 {
		t.Fatalf("drop with an 11.4 mil escape stub %.3f mV, full width %.3f mV: the stub's narrower section is not in the model", stub, full)
	}
	t.Logf("drop: full width %.3f mV, with 11.4 mil stub %.3f mV", full, stub)
}
