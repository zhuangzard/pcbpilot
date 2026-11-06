package app

import (
	"math"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
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

// A track may end only inside a pad's real shape: not in the corner of a round
// pad's box, and along the long axis of an oval turned 90 degrees.
func TestFinishPadInside(t *testing.T) {
	round := padObj(boardPad{X: 0, Y: 0, W: 40, H: 40, Shape: []any{"ELLIPSE", 40.0, 40.0}})
	if !round.inside([2]float64{0, 0}) || round.inside([2]float64{18, 18}) {
		t.Error("round pad")
	}
	oval := padObj(boardPad{X: 0, Y: 0, W: 20, H: 60, Rotation: 90, Shape: []any{"OVAL", 60.0, 20.0}})
	if !oval.inside([2]float64{0, 25}) || oval.inside([2]float64{9, 29}) {
		t.Error("turned oval")
	}
	skew := padObj(boardPad{X: 0, Y: 0, W: 50, H: 50, Rotation: 45, Shape: []any{"RECT", 40.0, 20.0}})
	if !skew.inside([2]float64{0, 9}) || skew.inside([2]float64{0, 12}) {
		t.Error("skewed pad: inscribed circle only")
	}
	if poly := padObj(boardPad{X: 0, Y: 0, W: 50, H: 50, Shape: []any{"POLYGON", 50.0, 50.0}}); poly.inside([2]float64{0, 0}) {
		t.Error("custom pad")
	}
}

// Copper touching two KiCad groups (here through the corner of a round pad's
// box) never merges them.
func TestFinishComponentsKeepKiCadGroups(t *testing.T) {
	a := boardPad{ID: "a", Net: "S", Layer: 1, X: 0, Y: 0, W: 40, H: 40, Shape: []any{"ELLIPSE", 40.0, 40.0}}
	b := boardPad{ID: "b", Net: "S", Layer: 1, X: 100, Y: 0, W: 40, H: 40}
	tr := []specctra.Track{{Net: "S", Layer: 1, X1: 19, Y1: 19, X2: 100, Y2: 0, Width: 2}}
	cs := finishComponents([]kicad.PadGroup{{Pads: []string{"a"}}, {Pads: []string{"b"}}}, map[string]boardPad{"a": a, "b": b}, tr, nil)
	if len(cs) != 2 {
		t.Fatalf("%d components", len(cs))
	}
}

// In escape mode a straight track on a poured layer must stay near a joined
// pad all along, not just at its ends.
func TestFinishSegAllowed(t *testing.T) {
	f := &finisher{pour: map[int]bool{15: true}, pourMode: "escape"}
	ends := []boardPad{{X: 0, Y: 0, W: 20, H: 20}, {X: 1000, Y: 0, W: 20, H: 20}}
	if !f.segAllowed(15, [2]float64{50, 0}, [2]float64{120, 0}, ends) || f.segAllowed(15, [2]float64{100, 0}, [2]float64{900, 0}, ends) {
		t.Error("escape along a segment")
	}
	if !f.segAllowed(18, [2]float64{100, 0}, [2]float64{900, 0}, ends) {
		t.Error("free layer")
	}
}

func TestFinishJudge(t *testing.T) {
	j1 := finishJoin{Net: "A", Pads: []string{"a1"}, To: []string{"a2"}, Tracks: []specctra.NewTrack{{Net: "A", Layer: 18, X1: 0, Y1: 0, X2: 1000, Y2: 0}}}
	j2 := finishJoin{Net: "B", Pads: []string{"b1"}, To: []string{"b2"}, Tracks: []specctra.NewTrack{{Net: "B", Layer: 18, X1: 0, Y1: 3000, X2: 1000, Y2: 3000}}}
	j3 := finishJoin{Net: "C", Pads: []string{"c1"}, To: []string{"c2"}, Tracks: []specctra.NewTrack{{Net: "C", Layer: 18, X1: 0, Y1: 6000, X2: 1000, Y2: 6000}}}
	plan := []finishJoin{j1, j2, j3}
	after := map[string][]kicad.PadGroup{
		"A": {{Pads: []string{"a1", "a2"}}},
		"B": {{Pads: []string{"b1", "b2"}}},
		"C": {{Pads: []string{"c1"}}, {Pads: []string{"c2"}}},
	}
	// Near j1; far from everything but naming net B; nowhere near any join.
	errs := []finishErr{
		{what: "new DRC clearance", at: [][2]float64{{500, 20}}},
		{what: "new DRC clearance", at: [][2]float64{{9000, 9000}}, nets: []string{"X", "B"}},
		{what: "new DRC zone", at: [][2]float64{{20000, 20000}}, nets: []string{"GND"}},
	}
	good, reasons, away := finishJudge(plan, after, after, errs, nil)
	if len(good) != 0 || reasons[j1.key()] == "" || reasons[j2.key()] == "" || reasons[j3.key()] != "groups still apart" {
		t.Errorf("good %v reasons %v", good, reasons)
	}
	if len(away) != 1 {
		t.Errorf("away %v", away)
	}
	// A pad cut off its group with no join near fails the pass.
	before := map[string][]kicad.PadGroup{"G": {{Pads: []string{"g1", "g2"}, Zone: true}}}
	cut := map[string][]kicad.PadGroup{"G": {{Pads: []string{"g1"}, Zone: true}, {Pads: []string{"g2"}}}, "A": after["A"]}
	_, reasons, _ = finishJudge(plan[:1], before, cut, nil, map[string][2]float64{"g2": {50000, 50000}})
	if reasons["board"] == "" {
		t.Errorf("cut pad: %v", reasons)
	}
	_, reasons, _ = finishJudge(plan[:1], before, cut, nil, map[string][2]float64{"g2": {500, 100}})
	if reasons[j1.key()] != "near a pad cut off its group" {
		t.Errorf("cut pad near j1: %v", reasons)
	}
}

// Insulation findings compare as a multiset: a second finding between the same
// items at another place is new.
func TestNewIsoFindings(t *testing.T) {
	f := pcbauto.IsoFinding{Kind: "iso-clearance", ItemA: "J1.3", ItemB: "H1.1", At: pcbauto.Point{X: 1, Y: 2}}
	g := f
	g.At = pcbauto.Point{X: 50, Y: 2}
	if n := newIsoFindings([]pcbauto.IsoFinding{f}, []pcbauto.IsoFinding{f, g}); len(n) != 1 || n[0].At.X != 50 {
		t.Errorf("new %v", n)
	}
	if n := newIsoFindings([]pcbauto.IsoFinding{f}, []pcbauto.IsoFinding{f, f}); len(n) != 1 {
		t.Errorf("duplicate %v", n)
	}
}

// An error near no join fails the pass unless it lies between other nets'
// tracks, vias and pads alone.
func TestFinishJudgeAway(t *testing.T) {
	j := finishJoin{Net: "A", Pads: []string{"a1"}, To: []string{"a2"}, Tracks: []specctra.NewTrack{{Net: "A", Layer: 18, X1: 0, Y1: 0, X2: 1000, Y2: 0}}}
	after := map[string][]kicad.PadGroup{"A": {{Pads: []string{"a1", "a2"}}}}
	far := [][2]float64{{20000, 20000}}
	good, reasons, away := finishJudge([]finishJoin{j}, after, after, []finishErr{{what: "new DRC clearance", at: far, nets: []string{"X", "Y"}, copperOnly: true}}, nil)
	if len(good) != 1 || len(reasons) != 0 || len(away) != 1 {
		t.Errorf("copper only: good %v reasons %v away %v", good, reasons, away)
	}
	_, reasons, _ = finishJudge([]finishJoin{j}, after, after, []finishErr{{what: "new DRC clearance", at: far, nets: []string{"GND"}}}, nil)
	if reasons["board"] == "" {
		t.Errorf("zone error: %v", reasons)
	}
	_, reasons, _ = finishJudge([]finishJoin{j}, after, after, []finishErr{{what: "new iso-clearance", at: far, nets: []string{"GND", "CHASSIS"}}}, nil)
	if reasons["board"] == "" {
		t.Errorf("insulation: %v", reasons)
	}
}

// Findings a fraction of a mil apart are still two findings.
func TestNewIsoFindingsExact(t *testing.T) {
	f := pcbauto.IsoFinding{Kind: "iso-clearance", ItemA: "J1.3", ItemB: "H1.1", At: pcbauto.Point{X: 1, Y: 2}, GapMil: 300}
	g := f
	g.At.X = 1.04
	if n := newIsoFindings([]pcbauto.IsoFinding{f}, []pcbauto.IsoFinding{f, g}); len(n) != 1 {
		t.Errorf("new %v", n)
	}
}

// A copper-only item list is told from one with a zone.
func TestCopperItem(t *testing.T) {
	for d, want := range map[string]bool{"Track [A] on In2.Cu, length 1 mm": true, "Via [A] on F.Cu - B.Cu": true, "SMD pad 1 [A] of C1": true, "PTH pad 2 [A] of J1": true, "Zone [GND] on F.Cu": false, "Footprint C1": false} {
		if reCopperItem.MatchString(d) != want {
			t.Errorf("%q", d)
		}
	}
}
