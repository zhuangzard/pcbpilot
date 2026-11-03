package app

import (
	"math"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

var stitchTestBounds = layoutBBox{MinX: 0, MinY: 0, MaxX: 2000, MaxY: 2000}

func stitchPad(id, net string, x, y float64) boardPad {
	return boardPad{ID: id, Net: net, Layer: 1, X: x, Y: y, W: 20, H: 20}
}

func TestPlanStitchesKeepsClear(t *testing.T) {
	open := []boardPad{stitchPad("a", "P", 1000, 1000)}
	other := stitchPad("b", "S", 1060, 1000)
	pads := append([]boardPad{other}, open...)
	tracks := []specctra.Track{{Net: "S", Layer: 1, X1: 900, Y1: 1040, X2: 1100, Y2: 1040, Width: 8}}
	size := map[string]stitchSize{"P": {Dia: 24, Drill: 12, Track: 10}}
	got, none := planStitches(open, pads, tracks, nil, size, 8, stitchTestBounds, map[string]bool{})
	if len(got) != 1 || len(none) != 0 {
		t.Fatalf("got %+v none %v", got, none)
	}
	v := got[0].Via
	if d := math.Hypot(math.Max(math.Abs(v.X-other.X)-10, 0), math.Max(math.Abs(v.Y-other.Y)-10, 0)); d-12 < 8 {
		t.Errorf("via %v too near the other pad: %.1f", v, d)
	}
	if d := math.Abs(v.Y-1040) - 4 - 12; d < 8 && v.X > 900 && v.X < 1100 {
		t.Errorf("via %v too near the other track: %.1f", v, d)
	}
	// No via in its own pad.
	if math.Abs(v.X-1000) < 10+12+8-1e-6 && math.Abs(v.Y-1000) < 10+12+8-1e-6 {
		t.Errorf("via %v on its own pad", v)
	}
	if got[0].Track.Width != 10 || got[0].Track.Layer != 1 || got[0].Drill != 12 {
		t.Errorf("track %+v drill %v", got[0].Track, got[0].Drill)
	}
}

func TestPlanStitchesSecondKeepsClearOfFirst(t *testing.T) {
	open := []boardPad{stitchPad("a", "P", 1000, 1000), stitchPad("b", "Q", 1050, 1000)}
	size := map[string]stitchSize{"P": {Dia: 24, Drill: 12, Track: 10}, "Q": {Dia: 24, Drill: 12, Track: 10}}
	got, none := planStitches(open, open, nil, nil, size, 8, stitchTestBounds, map[string]bool{})
	if len(got) != 2 || len(none) != 0 {
		t.Fatalf("got %+v none %v", got, none)
	}
	if d := math.Hypot(got[0].Via.X-got[1].Via.X, got[0].Via.Y-got[1].Via.Y) - 24; d < 8 {
		t.Errorf("vias %.1f apart at the barrels", d)
	}
}

func TestPlanStitchesNoSite(t *testing.T) {
	open := []boardPad{stitchPad("a", "P", 1000, 1000)}
	// Other-net copper over the whole neighbourhood.
	var tracks []specctra.Track
	for y := 880.0; y <= 1120; y += 20 {
		if y > 985 && y < 1015 {
			continue
		}
		tracks = append(tracks, specctra.Track{Net: "S", Layer: 2, X1: 800, Y1: y, X2: 1200, Y2: y, Width: 10})
	}
	size := map[string]stitchSize{"P": {Dia: 24, Drill: 12, Track: 10}}
	got, none := planStitches(open, open, tracks, nil, size, 8, stitchTestBounds, map[string]bool{})
	// Directly left or right of the pad there is a free lane; block them too.
	if len(got) == 1 {
		if got[0].Via.Y < 985 || got[0].Via.Y > 1015 {
			t.Fatalf("via outside the free lane: %+v", got[0].Via)
		}
		tried := map[string]bool{got[0].Site: true}
		for i := 0; i < 400; i++ {
			next, _ := planStitches(open, open, tracks, nil, size, 8, stitchTestBounds, tried)
			if len(next) == 0 {
				return
			}
			tried[next[0].Site] = true
		}
		t.Fatal("tried sites do not run out")
	}
	if len(none) != 1 {
		t.Fatalf("got %+v none %v", got, none)
	}
	// A net without a via size has no site at all.
	if got, none := planStitches(open, open, nil, nil, nil, 8, stitchTestBounds, nil); len(got) != 0 || len(none) != 1 {
		t.Fatalf("no size: %+v %v", got, none)
	}
}

func TestOpenPadsOf(t *testing.T) {
	open := openPadsOf(map[string][]kicad.PadGroup{
		"GND":  {{Pads: []string{"a", "b", "c"}, Zone: true}, {Pads: []string{"d"}}},
		"+3V3": {{Pads: []string{"e"}}, {Pads: []string{"f"}}},
		// Three pads on a track, two on the pour: the pour's group is the main one.
		"P": {{Pads: []string{"g", "h", "i"}}, {Pads: []string{"j", "k"}, Zone: true}},
	})
	for _, id := range []string{"d", "e", "f", "g", "h", "i"} {
		if !open[id] {
			t.Errorf("%s not open", id)
		}
	}
	for _, id := range []string{"a", "b", "c", "j", "k"} {
		if open[id] {
			t.Errorf("%s open", id)
		}
	}
}

func TestNewViolations(t *testing.T) {
	x, y := 1.0, 2.0
	base := &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "clearance", Message: "m"}}}
	cur := &kicad.DRCReport{Violations: []kicad.Violation{
		{Rule: "clearance", Message: "m"},
		{Rule: "clearance", Message: "m", X: &x, Y: &y},
		{Rule: "unconnected_items", Message: "open"},
		{Rule: "shorting_items", Message: "s"},
	}}
	got := newViolations(base, cur)
	if len(got) != 2 || got[0].Rule != "clearance" || got[1].Rule != "shorting_items" {
		t.Fatalf("%+v", got)
	}
}

func TestStitchNearViolation(t *testing.T) {
	s := stitch{Via: specctra.NewVia{X: 100, Y: 0}, Track: specctra.NewTrack{X1: 0, Y1: 0, X2: 100, Y2: 0}}
	near, far := 20.0, 200.0
	zero := 0.0
	if !stitchNearViolation(kicad.Violation{X: &near, Y: &zero}, s) {
		t.Error("near the track")
	}
	if stitchNearViolation(kicad.Violation{X: &far, Y: &zero}, s) {
		t.Error("far away")
	}
	// The violation's own position is far away; its via object is at the stitch.
	if !stitchNearViolation(kicad.Violation{X: &far, Y: &far, Items: []kicad.DRCItem{{X: 900, Y: 900}, {X: 100, Y: 0}}}, s) {
		t.Error("an object of the violation at the stitch")
	}
	if stitchNearViolation(kicad.Violation{}, s) {
		t.Error("no position")
	}
}

func TestStitchNetsFollowTheProfile(t *testing.T) {
	ps := []kicad.Pour{{Net: "GND"}, {Net: "GND"}, {Net: "+3V3"}}
	r := &kicadRun{summary: map[string]any{}}
	r.o.stitchPours = "auto"
	if n := r.stitchNets(ps); n != nil {
		t.Fatalf("auto without the profile: %v", n)
	}
	r.summary["largeBoard"] = map[string]any{}
	if n := r.stitchNets(ps); len(n) != 2 || n[0] != "+3V3" {
		t.Fatalf("auto with the profile: %v", n)
	}
	r.o.stitchPours = "off"
	if n := r.stitchNets(ps); n != nil {
		t.Fatalf("off: %v", n)
	}
	r.o.stitchPours = "on"
	r.summary = map[string]any{}
	if n := r.stitchNets(ps); len(n) != 2 {
		t.Fatalf("on: %v", n)
	}
}

// KiCad lists the same two objects in either order between runs.
func TestNewViolationsIgnoresItemOrder(t *testing.T) {
	a, b := kicad.DRCItem{Description: "Pad 1"}, kicad.DRCItem{Description: "Pad 2"}
	base := &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "clearance", Message: "actual 0.1991 mm", Items: []kicad.DRCItem{a, b}}}}
	cur := &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "clearance", Message: "actual 0.1985 mm", Items: []kicad.DRCItem{b, a}}}}
	if got := newViolations(base, cur); len(got) != 0 {
		t.Fatalf("same objects reported as new: %+v", got)
	}
	cur.Violations[0].Items = []kicad.DRCItem{a, {Description: "Via"}}
	if got := newViolations(base, cur); len(got) != 1 {
		t.Fatal("different objects must be new")
	}
}

// Two vias of one net read the same; a new one must not pass for an old one, and
// the same objects closer together are worse.
func TestNewViolationsByObjectAndGap(t *testing.T) {
	via := func(id string) kicad.DRCItem { return kicad.DRCItem{Description: "Via [GND]", UUID: id} }
	pad := kicad.DRCItem{Description: "Pad 1", UUID: "p"}
	base := &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "clearance", Message: "actual 0.190 mm", Items: []kicad.DRCItem{via("old"), pad}}}}
	cur := &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "clearance", Message: "actual 0.190 mm", Items: []kicad.DRCItem{via("new"), pad}}}}
	if got := newViolations(base, cur); len(got) != 1 {
		t.Fatalf("a new via hid behind an old one: %+v", got)
	}
	cur.Violations[0].Items = []kicad.DRCItem{via("old"), pad}
	if got := newViolations(base, cur); len(got) != 0 {
		t.Fatalf("same objects, same gap: %+v", got)
	}
	cur.Violations[0].Message = "actual 0.150 mm"
	if got := newViolations(base, cur); len(got) != 1 {
		t.Fatalf("a worse gap passed: %+v", got)
	}
}

func TestStitchRefusal(t *testing.T) {
	low, high := 0.02, 5.0
	in := &designIntent{Nets: map[string]*intentNet{"P": {Pins: []intentPin{{Ref: "C1", Pin: "1", CurrentA: &low}, {Ref: "U1", Pin: "3", CurrentA: &high}, {Ref: "C2", Pin: "1"}}}}}
	if why := stitchRefusal(in, "P", "C2", "1", 12); why == "" {
		t.Error("a pin with no current value accepted")
	}
	if why := stitchRefusal(nil, "P", "C1", "1", 12); why == "" {
		t.Error("no intent accepted")
	}
	if why := stitchRefusal(in, "P", "C1", "1", 12); why != "" {
		t.Errorf("low current refused: %s", why)
	}
	if why := stitchRefusal(in, "P", "U1", "3", 12); why == "" {
		t.Error("5 A through one via accepted")
	}
	if why := stitchRefusal(in, "P", "C9", "1", 12); why == "" {
		t.Error("a pad with no current accepted")
	}
	if why := stitchRefusal(in, "Q", "C1", "1", 12); why == "" {
		t.Error("a net with no intent accepted")
	}
}

func TestPadsSplit(t *testing.T) {
	before := map[string][]kicad.PadGroup{"P": {{Pads: []string{"a", "b", "c"}, Zone: true}, {Pads: []string{"d"}}}}
	// d joins: nothing is lost.
	grown := map[string][]kicad.PadGroup{"P": {{Pads: []string{"a", "b", "c", "d"}, Zone: true}}}
	if got := padsSplit(before, grown); len(got) != 0 {
		t.Fatalf("growth: %v", got)
	}
	// The main group moves to other copper: a lone pad cannot be cut off.
	moved := map[string][]kicad.PadGroup{"P": {{Pads: []string{"a", "b", "c"}}, {Pads: []string{"d", "e", "f", "g"}, Zone: true}}}
	if got := padsSplit(before, moved); len(got) != 0 {
		t.Fatalf("main group moved: %v", got)
	}
	// c is cut off from a and b.
	cut := map[string][]kicad.PadGroup{"P": {{Pads: []string{"a", "b"}, Zone: true}, {Pads: []string{"c"}}, {Pads: []string{"d"}}}}
	if got := padsSplit(before, cut); len(got) != 1 || !got["c"] {
		t.Fatalf("cut: %v", got)
	}
}

func TestStitchCutters(t *testing.T) {
	near := stitch{PadID: "near", Via: specctra.NewVia{X: 100, Y: 0}}
	far := stitch{PadID: "far", Via: specctra.NewVia{X: 5000, Y: 0}}
	at := map[string][2]float64{"lost": {0, 0}, "away": {9000, 9000}}
	cut := stitchCutters(map[string]bool{"lost": true}, []stitch{near, far}, at)
	if !cut.stitches["near"] || cut.stitches["far"] || !cut.named["lost"] {
		t.Fatalf("%+v", cut)
	}
	// A lost pad with no stitch around stays unnamed: the pass fails on it.
	cut = stitchCutters(map[string]bool{"away": true}, []stitch{near, far}, at)
	if cut.named["away"] || len(cut.stitches) != 0 {
		t.Fatalf("%+v", cut)
	}
}
