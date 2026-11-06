package app

import (
	"math"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

func finishTestRig(tracks []specctra.Track, pads []boardPad, layers []int, pour map[int]bool, mode string) *finisher {
	f := &finisher{idx: newFinishIndex(), clr: map[string]float64{}, defClr: 6, maxClr: 6,
		bounds: layoutBBox{MinX: 0, MinY: 0, MaxX: 4000, MaxY: 4000}, layers: layers, pour: pour, pourMode: mode}
	for _, p := range pads {
		f.idx.add(padObj(p))
	}
	for _, t := range tracks {
		f.idx.add(finishObj{net: t.Net, layer: t.Layer, a: [2]float64{t.X1, t.Y1}, b: [2]float64{t.X2, t.Y2}, half: t.Width / 2})
	}
	return f
}

// Two pads of one net on the top layer with another net's wall between them
// all the way across: the join must change layer, and every piece of it must
// keep the clearance the exact checks ask for.
func TestFinishJoinsAcrossAWall(t *testing.T) {
	a := boardPad{ID: "a", Net: "S", Layer: 1, X: 1000, Y: 2000, W: 30, H: 30}
	b := boardPad{ID: "b", Net: "S", Layer: 1, X: 2000, Y: 2000, W: 30, H: 30}
	wall := specctra.Track{Net: "W", Layer: 1, X1: 1500, Y1: 10, X2: 1500, Y2: 3990, Width: 10}
	pads := []boardPad{a, b}
	padByID := map[string]boardPad{"a": a, "b": b}
	groups := map[string][]kicad.PadGroup{"S": {{Pads: []string{"a"}}, {Pads: []string{"b"}}}}
	size := map[string]finishSize{"S": {Track: 8, Clr: 6, Dia: 24, Drill: 12}}

	f := finishTestRig([]specctra.Track{wall}, pads, []int{1}, nil, "escape")
	if js, fails := f.plan([]string{"S"}, groups, padByID, []specctra.Track{wall}, nil, size, map[string]bool{}); len(js) != 0 || len(fails) != 1 {
		t.Fatalf("one layer: joins %+v fails %v", js, fails)
	}

	f = finishTestRig([]specctra.Track{wall}, pads, []int{1, 15}, nil, "escape")
	js, fails := f.plan([]string{"S"}, groups, padByID, []specctra.Track{wall}, nil, size, map[string]bool{})
	if len(js) != 1 || len(fails) != 0 {
		t.Fatalf("two layers: joins %+v fails %v", js, fails)
	}
	j := js[0]
	if len(j.Vias) < 2 || len(j.Pads) != 1 || len(j.To) != 1 || j.Pads[0] == j.To[0] {
		t.Fatalf("join %+v", j)
	}
	if j.LengthMil < 950 || j.LengthMil > 1400 {
		t.Errorf("length %.0f", j.LengthMil)
	}
	for _, tr := range j.Tracks {
		if tr.Layer == 1 {
			if d := segSegDist(tr.X1, tr.Y1, tr.X2, tr.Y2, wall.X1, wall.Y1, wall.X2, wall.Y2) - 5 - 4; d < 6 {
				t.Errorf("track %+v is %.2f from the wall", tr, d)
			}
		}
	}
	for _, v := range j.Vias {
		if d := math.Abs(v.X-1500) - 5 - 12; d < 6 {
			t.Errorf("via %+v is %.2f from the wall", v, d)
		}
	}
	ops := finishOps(js)
	if n := len(ops["addVias"].([]map[string]any)); n != len(j.Vias) {
		t.Errorf("edit has %d vias", n)
	}
}

// A poured layer is usable only near the pads joined in escape mode: with the
// only other layer poured, a join that would need it far from both pads fails.
func TestFinishPourEscape(t *testing.T) {
	a := boardPad{ID: "a", Net: "S", Layer: 1, X: 1000, Y: 2000, W: 30, H: 30}
	b := boardPad{ID: "b", Net: "S", Layer: 1, X: 2000, Y: 2000, W: 30, H: 30}
	wall := specctra.Track{Net: "W", Layer: 1, X1: 1500, Y1: 10, X2: 1500, Y2: 3990, Width: 10}
	padByID := map[string]boardPad{"a": a, "b": b}
	groups := map[string][]kicad.PadGroup{"S": {{Pads: []string{"a"}}, {Pads: []string{"b"}}}}
	size := map[string]finishSize{"S": {Track: 8, Clr: 6, Dia: 24, Drill: 12}}
	for mode, want := range map[string]int{"escape": 0, "off": 0, "on": 1} {
		f := finishTestRig([]specctra.Track{wall}, []boardPad{a, b}, []int{1, 15}, map[int]bool{15: true}, mode)
		js, _ := f.plan([]string{"S"}, groups, padByID, []specctra.Track{wall}, nil, size, map[string]bool{})
		if len(js) != want {
			t.Errorf("%s: %d joins, want %d", mode, len(js), want)
		}
	}
}

// Tracks and vias join the pad group they touch; copper touching no pad is
// left out.
func TestFinishComponents(t *testing.T) {
	a := boardPad{ID: "a", Net: "S", Layer: 1, X: 0, Y: 0, W: 20, H: 20}
	b := boardPad{ID: "b", Net: "S", Layer: 1, X: 1000, Y: 0, W: 20, H: 20}
	tracks := []specctra.Track{
		{Net: "S", Layer: 1, X1: 0, Y1: 0, X2: 300, Y2: 0, Width: 8},
		{Net: "S", Layer: 15, X1: 300, Y1: 0, X2: 600, Y2: 0, Width: 8},
		{Net: "S", Layer: 15, X1: 700, Y1: 500, X2: 800, Y2: 500, Width: 8},
	}
	vias := []widenVia{{Net: "S", X: 300, Y: 0, Diameter: 24}}
	cs := finishComponents([]kicad.PadGroup{{Pads: []string{"a"}}, {Pads: []string{"b"}}}, map[string]boardPad{"a": a, "b": b}, tracks, vias)
	if len(cs) != 2 {
		t.Fatalf("%d components", len(cs))
	}
	if len(cs[0].objs) != 4 || len(cs[1].objs) != 1 || cs[0].pads[0].ID != "a" {
		t.Errorf("components %+v", cs)
	}
}

func TestFinishExcluded(t *testing.T) {
	in := &designIntent{Nets: map[string]*intentNet{"D+": {DiffPair: "USB"}, "CLK": {LengthGroup: "g"}, "IO": {}}}
	if finishExcluded(in, "D+") == "" || finishExcluded(in, "CLK") == "" || finishExcluded(in, "IO") != "" || finishExcluded(in, "X") != "" {
		t.Error("exclusions")
	}
}
