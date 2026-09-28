package pcbauto

import "testing"

// nodeCong at the grid edge must ignore off-grid cells instead of indexing
// past the layer (panic seen routing the ESP32 v05 board with --intent --sim).
func TestNodeCongAtGridEdge(t *testing.T) {
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 400, 300}.Corners()}
	st := &Stackup{Layers: 2, Stack: []StackLayer{{ID: LayerTop, Kind: KindSignal}, {ID: LayerBottom, Kind: KindSignal}}}
	gr, err := newGrid(b, st, 3)
	if err != nil {
		t.Fatal(err)
	}
	// One claim in the top-right corner block of each layer, so the
	// empty-neighbourhood shortcut does not return early.
	for l := 0; l < len(gr.layers); l++ {
		x, y := gr.W-1, gr.H-1
		gr.use[gr.idx(l, x, y)] = 1
		gr.bUse[(l*gr.bH+y/blockCells)*gr.bW+x/blockCells] = 1
	}
	r := &router{gr: gr}
	rad := 6.0 // small disk: the per-offset path
	if len(gr.disk(rad)) > diskSpanMin {
		t.Fatalf("test needs a small disk, got %d cells", len(gr.disk(rad)))
	}
	for l := 0; l < len(gr.layers); l++ {
		occ, _ := r.nodeCong(l, gr.W-1, gr.H-1, rad)
		if occ != 1 {
			t.Fatalf("layer %d: occ = %v, want the one claim", l, occ)
		}
		if occ, _ := r.nodeCong(l, 0, 0, rad); occ != 0 {
			t.Fatalf("layer %d origin: occ = %v", l, occ)
		}
	}
}
