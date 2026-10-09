package postsim

import "testing"

// A via whose ring only just overlaps a pad (0.5 mil) is connected to it —
// KiCad's connectivity says so — even though no raster cell sees the overlap
// and the via centre is outside the pad.
func TestViaRingEdgeOverlapsPad(t *testing.T) {
	for _, overlap := range []float64{0.5, 0.05} {
		b := newTB(1400, 300, 2)
		b.part("S", 1, pad("1", "VIN", 1, 200, 150, 10, 10))
		b.line("VIN", 1, 200, 150, 600, 150, 10)
		b.via("VIN", 600, 150, 24, 12)
		// Load pad on BOTTOM: its left edge reaches overlap mil into the via ring.
		b.part("L", 2, pad("1", "VIN", 2, 612-overlap+10, 150, 20, 20))
		sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 1.0}, {"L", "1", "sink", 1.0}}}, nil, nil)
		r, err := Run(b.json(), sim, DefaultOptions())
		if err != nil {
			t.Fatal(err)
		}
		if n := netOf(t, r, "VIN"); n.Status == "open" {
			t.Errorf("overlap %.2f mil: VIN reported open (no copper path), want connected: %+v", overlap, n.Pads)
		}
	}
}

// A track whose end cap overlaps a pad edge (centre outside the pad) and a
// track ending on another track's edge are connected.
func TestTrackCapOverlapsPadAndTrack(t *testing.T) {
	b := newTB(1400, 400, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 150, 10, 10))
	b.line("VIN", 1, 200, 150, 600, 150, 10)
	// Second track starts 9 mil above the first one's centreline: its cap
	// (r = 5) overlaps the first track's body (half width 5) by 1 mil.
	b.line("VIN", 1, 500, 159, 500, 300, 10)
	// It ends 4 mil left of the load pad's edge: its cap overlaps by 1 mil.
	b.part("L", 1, pad("1", "VIN", 1, 500+4+10, 300, 20, 20))
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 1.0}, {"L", "1", "sink", 1.0}}}, nil, nil)
	r, err := Run(b.json(), sim, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if n := netOf(t, r, "VIN"); n.Status == "open" {
		t.Errorf("VIN reported open, want connected: %+v", n.Pads)
	}
}

// Copper that does not touch (0.5 mil gap) stays open.
func TestViaRingGapStaysOpen(t *testing.T) {
	b := newTB(1400, 300, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 150, 10, 10))
	b.line("VIN", 1, 200, 150, 600, 150, 10)
	b.via("VIN", 600, 150, 24, 12)
	b.part("L", 2, pad("1", "VIN", 2, 612+0.5+10, 150, 20, 20))
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 1.0}, {"L", "1", "sink", 1.0}}}, nil, nil)
	r, err := Run(b.json(), sim, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if n := netOf(t, r, "VIN"); n.Status != "open" {
		t.Errorf("0.5 mil gap: VIN status %s, want open", n.Status)
	}
}
