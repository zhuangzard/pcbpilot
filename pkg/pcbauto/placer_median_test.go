package pcbauto

import "testing"

func medianPad(part string, x, y float64) *Pad {
	return &Pad{Part: part, Box: OrientedBox{C: Point{x, y}}}
}

// The target is the median of the other parts' pads on the part's small nets:
// its own pads and a net too big to say anything count for nothing.
func TestMedianTarget(t *testing.T) {
	p := &Part{Ref: "R1"}
	big := &pnet{}
	for i := 0; i <= medianTargetMaxPads; i++ {
		big.pads = append(big.pads, medianPad("U1", 9000, 9000))
	}
	pl := &placer{
		partNet: map[*Part][]int{p: {0, 1}},
		nets: []*pnet{
			{pads: []*Pad{medianPad("R1", -500, -500), medianPad("C1", 100, 700), medianPad("C2", 300, 100), medianPad("C3", 200, 400)}},
			big,
		},
	}
	got, ok := pl.medianTarget(p)
	if !ok || got != (Point{200, 400}) {
		t.Fatalf("got %v %v, want (200,400)", got, ok)
	}
}

func TestMedianTargetNoSmallNet(t *testing.T) {
	p := &Part{Ref: "R1"}
	pl := &placer{partNet: map[*Part][]int{p: {0}}, nets: []*pnet{{pads: []*Pad{medianPad("R1", 1, 1)}}}}
	if _, ok := pl.medianTarget(p); ok {
		t.Fatal("a part alone on its net has no target")
	}
}
