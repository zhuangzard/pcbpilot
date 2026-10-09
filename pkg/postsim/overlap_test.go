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

// An open load next to an over-budget one: both findings are reported (the
// open must not hide the drop the IR closure widens by).
func TestOpenDoesNotHideOverBudget(t *testing.T) {
	b := newTB(4000, 300, 2)
	b.part("S", 1, pad("1", "VIN", 1, 100, 150, 10, 10))
	b.part("L", 1, pad("1", "VIN", 1, 3900, 150, 10, 10))
	b.part("M", 1, pad("1", "VIN", 1, 2000, 280, 10, 10)) // no copper to it
	b.line("VIN", 1, 100, 150, 3900, 150, 4)
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 3.0}, {"L", "1", "sink", 2.0}, {"M", "1", "sink", 1.0}}}, nil, nil)
	r, err := Run(b.json(), sim, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	var open, drop bool
	for _, f := range r.Findings {
		open = open || f.Kind == "open"
		drop = drop || (f.Kind == "ir-drop" && f.Severity == "fail")
	}
	if !open || !drop {
		t.Fatalf("findings %+v: want both open and ir-drop", r.Findings)
	}
}

// CopperConnectivity: pads joined by a track, a via chain and a poured fill
// are one island; a pad with no copper to the rest is a second island.
func TestCopperConnectivity(t *testing.T) {
	b := newTB(2000, 1000, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 150, 10, 10))
	b.part("L", 1, pad("1", "VIN", 1, 1800, 150, 10, 10))
	b.part("M", 2, pad("1", "VIN", 2, 1000, 600, 20, 20))
	b.part("X", 1, pad("1", "VIN", 1, 1500, 900, 10, 10)) // isolated
	b.line("VIN", 1, 200, 150, 700, 150, 10)
	b.via("VIN", 700, 150, 24, 12)
	b.line("VIN", 2, 700, 150, 1000, 600, 10)
	b.rectFill("VIN", 1, 1300, 100, 1900, 200) // joins L, and the track end below
	b.line("VIN", 1, 700, 150, 1350, 150, 10)
	brd, err := ParseBoard(b.json())
	if err != nil {
		t.Fatal(err)
	}
	open := CopperConnectivity(brd)
	if len(open) != 1 || open[0].Net != "VIN" || open[0].Unrouted != 1 || len(open[0].Islands[1]) != 1 || open[0].Islands[1][0] != "X.1" {
		t.Fatalf("open %+v", open)
	}
	// A 0.5 mil gap breaks the chain.
	b2 := newTB(2000, 1000, 2)
	b2.part("S", 1, pad("1", "VIN", 1, 200, 150, 10, 10))
	b2.part("L", 1, pad("1", "VIN", 1, 1000, 150, 10, 10))
	b2.line("VIN", 1, 200, 150, 989.5, 150, 10) // cap ends at 994.5, pad edge 995
	brd2, _ := ParseBoard(b2.json())
	if o := CopperConnectivity(brd2); len(o) != 1 {
		t.Fatalf("gap: %+v", o)
	}
}
