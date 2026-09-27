package pcbauto

import (
	"math/rand"
	"testing"
)

// The distance-transform static map must equal the ring scan exactly.
func TestStaticByDistanceMatchesScan(t *testing.T) {
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 400, 300}.Corners()}
	st := &Stackup{Layers: 2, Stack: []StackLayer{{ID: LayerTop, Kind: KindSignal}, {ID: LayerBottom, Kind: KindSignal}}}
	gr, err := newGrid(b, st, 3)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(7))
	for i := range gr.flags {
		switch v := rng.Intn(400); {
		case v == 0:
			gr.flags[i] |= flagHard
		case v < 4:
			gr.pad[i] = int32(rng.Intn(5))
		case v == 4:
			gr.pad[i] = -2
		}
	}
	for _, rad := range []float64{2, 7.5, 13, 21, 40, 66.6} {
		scan := &router{gr: gr}
		dist := &router{gr: gr}
		old := staticEDTMinOffsets
		staticEDTMinOffsets = 1 << 30
		a := scan.staticSlow(rad)
		staticEDTMinOffsets = 0
		c := dist.staticSlow(rad)
		staticEDTMinOffsets = old
		for i := range a {
			if a[i] != c[i] {
				l, x, y := gr.xy(i)
				t.Fatalf("rad %.1f cell (%d,%d,%d): scan %d, distance %d", rad, l, x, y, a[i], c[i])
			}
		}
	}
}
