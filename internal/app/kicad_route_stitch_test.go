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
	for y := 780.0; y <= 1220; y += 20 {
		if y > 985 && y < 1015 {
			continue
		}
		tracks = append(tracks, specctra.Track{Net: "S", Layer: 2, X1: 700, Y1: y, X2: 1300, Y2: y, Width: 10})
	}
	size := map[string]stitchSize{"P": {Dia: 24, Drill: 12, Track: 10}}
	got, none := planStitches(open, open, tracks, nil, size, 8, stitchTestBounds, map[string]bool{})
	// Directly left or right of the pad there is a free lane; block them too.
	if len(got) == 1 {
		if got[0].Via.Y < 985 || got[0].Via.Y > 1015 {
			t.Fatalf("via outside the free lane: %+v", got[0].Via)
		}
		tried := map[string]bool{got[0].Site: true}
		for i := 0; i < 600; i++ {
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

func TestViolationsAwayFrom(t *testing.T) {
	s := stitch{Via: specctra.NewVia{X: 100, Y: 0}, Track: specctra.NewTrack{X1: 0, Y1: 0, X2: 100, Y2: 0}}
	near, far, zero := 20.0, 9000.0, 0.0
	got := violationsAwayFrom([]kicad.Violation{{Rule: "a", X: &near, Y: &zero}, {Rule: "b", X: &far, Y: &zero}}, []stitch{s})
	if len(got) != 1 || got[0].Rule != "b" {
		t.Fatalf("%+v", got)
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

// Without a uuid, the same words at another place are another violation.
func TestNewViolationsByPlace(t *testing.T) {
	x1, x2, y := 1.0, 900.0, 2.0
	base := &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "r", Message: "m", X: &x1, Y: &y}}}
	cur := &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "r", Message: "m", X: &x2, Y: &y}}}
	if got := newViolations(base, cur); len(got) != 1 {
		t.Fatalf("itemless: %+v", got)
	}
	base = &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "r", Items: []kicad.DRCItem{{Description: "Via [GND]", X: 1, Y: 2}}}}}
	cur = &kicad.DRCReport{Violations: []kicad.Violation{{Rule: "r", Items: []kicad.DRCItem{{Description: "Via [GND]", X: 900, Y: 2}}}}}
	if got := newViolations(base, cur); len(got) != 1 {
		t.Fatalf("no uuid: %+v", got)
	}
}

func TestStitchRefusal(t *testing.T) {
	low, high := 0.02, 5.0
	in := &designIntent{Nets: map[string]*intentNet{"P": {Pins: []intentPin{{Ref: "C1", Pin: "1", CurrentA: &low}, {Ref: "U1", Pin: "3", CurrentA: &high}, {Ref: "C2", Pin: "1"}}}}}
	c1 := stitchPin{"C1", "1"}
	if why := stitchRefusal(in, "P", []stitchPin{c1}, 12); why != "" {
		t.Errorf("low current refused: %s", why)
	}
	if why := stitchRefusal(in, "P", []stitchPin{{"U1", "3"}}, 12); why == "" {
		t.Error("5 A through one via accepted")
	}
	// A capacitor a track joins to a 5 A load feeds that load too.
	if why := stitchRefusal(in, "P", []stitchPin{c1, {"U1", "3"}}, 12); why == "" {
		t.Error("the group's current was not added up")
	}
	if why := stitchRefusal(in, "P", []stitchPin{{"C2", "1"}}, 12); why == "" {
		t.Error("a pin with no current value accepted")
	}
	if why := stitchRefusal(in, "P", []stitchPin{{"C9", "1"}}, 12); why == "" {
		t.Error("a pad with no record accepted")
	}
	if why := stitchRefusal(in, "Q", []stitchPin{c1}, 12); why == "" {
		t.Error("a net with no intent accepted")
	}
	if why := stitchRefusal(nil, "P", []stitchPin{c1}, 12); why == "" {
		t.Error("no intent accepted")
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
	moved := map[string][]kicad.PadGroup{"P": {{Pads: []string{"a", "b", "c"}, Zone: true}, {Pads: []string{"d", "e", "f", "g"}, Zone: true}}}
	if got := padsSplit(before, moved); len(got) != 0 {
		t.Fatalf("main group moved: %v", got)
	}
	// A lone pad on a zone loses it.
	lone := map[string][]kicad.PadGroup{"Q": {{Pads: []string{"x"}, Zone: true}}}
	if got := padsSplit(lone, map[string][]kicad.PadGroup{"Q": {{Pads: []string{"x"}}}}); !got["x"] {
		t.Fatalf("lone pad: %v", got)
	}
	// c is cut off from a and b.
	cut := map[string][]kicad.PadGroup{"P": {{Pads: []string{"a", "b"}, Zone: true}, {Pads: []string{"c"}}, {Pads: []string{"d"}}}}
	if got := padsSplit(before, cut); len(got) != 1 || !got["c"] {
		t.Fatalf("cut: %v", got)
	}
}

func TestStitchCutters(t *testing.T) {
	near := stitch{PadID: "near", Via: specctra.NewVia{X: 100, Y: 0}, Track: specctra.NewTrack{X1: 100, X2: 100}}
	far := stitch{PadID: "far", Via: specctra.NewVia{X: 5000, Y: 0}, Track: specctra.NewTrack{X1: 5000, X2: 5000}}
	at := map[string][2]float64{"lost": {0, 0}, "away": {9000, 9000}}
	cut := stitchCutters(map[string]bool{"lost": true}, []stitch{near, far}, at, false)
	if !cut.stitches["near"] || cut.stitches["far"] || !cut.named["lost"] {
		t.Fatalf("%+v", cut)
	}
	// A lost pad with no stitch around stays unnamed: the pass fails on it.
	cut = stitchCutters(map[string]bool{"away": true}, []stitch{near, far}, at, false)
	if cut.named["away"] || len(cut.stitches) != 0 {
		t.Fatalf("%+v", cut)
	}
}

// --power-extra-layers adds to --power-layer, on the same net, and nothing
// without the power pour it extends.
func TestKicadPourPlanExtraPowerLayers(t *testing.T) {
	in := &boardSnapshot{CopperLayers: 6, Components: []boardComp{{Pads: []boardPad{{Net: "GND"}, {Net: "VCC"}}}}}
	o := kicadRouteOpts{pours: "on", gndNet: "GND", powerNet: "VCC", powerLayer: -1, powerExtraLayers: []int{17}}
	var vcc []int
	for _, p := range kicadPourPlan(o, in) {
		if p.Net == "VCC" {
			vcc = append(vcc, p.Layer)
		}
	}
	if len(vcc) != 2 || vcc[0] != 16 || vcc[1] != 17 {
		t.Fatalf("VCC pours on %v, want [16 17]", vcc)
	}
	o.powerLayer = 0
	for _, p := range kicadPourPlan(o, in) {
		if p.Net == "VCC" {
			t.Fatalf("no power pour asked for, got one on %d", p.Layer)
		}
	}
}

// A via beyond the pad's escape zone must not hang on a track narrower than the
// net's: a narrow pad stays within the short reach, a wide one may go further.
func TestPlanStitchesFarReachNeedsFullWidth(t *testing.T) {
	var tracks []specctra.Track
	for y := 700.0; y <= 1300; y += 20 {
		if y < 1120 {
			tracks = append(tracks, specctra.Track{Net: "S", Layer: 2, X1: 700, Y1: y, X2: 1300, Y2: y, Width: 10})
		}
	}
	plan := func(w, h float64) []stitch {
		p := stitchPad("a", "P", 1000, 1000)
		p.W, p.H = w, h
		got, _ := planStitches([]boardPad{p}, []boardPad{p}, tracks, nil, map[string]stitchSize{"P": {Dia: 24, Drill: 12, Track: 30}}, 8, stitchTestBounds, map[string]bool{})
		return got
	}
	if got := plan(10, 10); len(got) != 0 {
		t.Fatalf("narrow pad reached %+v", got[0].Via)
	}
	if got := plan(40, 40); len(got) != 1 || got[0].Track.Width != 30 {
		t.Fatalf("wide pad got %+v", got)
	}
}

// A pad walled in on its layer but for one gap reaches a via site by a bent
// path, and only when the search is on.
func TestPlanStitchPathAroundWall(t *testing.T) {
	p := boardPad{ID: "p", Net: "P", Layer: 1, X: 1000, Y: 1000, W: 30, H: 30}
	open := []boardPad{p}
	var tracks []specctra.Track
	wall := func(x1, y1, x2, y2 float64) {
		tracks = append(tracks, specctra.Track{Net: "S", Layer: 1, X1: x1, Y1: y1, X2: x2, Y2: y2, Width: 10})
	}
	// A corridor on the pad's layer, closed on the left. Inner-layer tracks
	// leave no via site before its far end.
	wall(960, 960, 1200, 960)
	wall(960, 1040, 1200, 1040)
	wall(960, 960, 960, 1040)
	for y := 930.0; y <= 1070; y += 20 {
		tracks = append(tracks, specctra.Track{Net: "S", Layer: 15, X1: 900, Y1: y, X2: 1190, Y2: y, Width: 10})
	}
	size := map[string]stitchSize{"P": {Dia: 24, Drill: 12, Track: 8}}
	if got, none := planStitches(open, open, tracks, nil, size, 6, stitchTestBounds, map[string]bool{}); len(got) != 0 || len(none) != 1 {
		t.Fatalf("straight search found %+v", got)
	}
	if got, _ := planStitchPaths(open, open, tracks, nil, size, 6, stitchTestBounds, map[string]bool{}, 50, nil); len(got) != 0 {
		t.Fatalf("a 50 mil search found %+v", got)
	}
	got, none := planStitchPaths(open, open, tracks, nil, size, 6, stitchTestBounds, map[string]bool{}, 400, nil)
	if len(got) != 1 || len(none) != 0 {
		t.Fatalf("path search: %+v, none %v", got, none)
	}
	s := got[0]
	if s.Via.X < 1190 || s.PathMil < 190 || s.PathMil > 400 {
		t.Errorf("via %+v length %v", s.Via, s.PathMil)
	}
	all := append([]specctra.NewTrack{s.Track}, s.More...)
	last := all[len(all)-1]
	if s.Track.X1 != 1000 || s.Track.Y1 != 1000 || last.X2 != s.Via.X || last.Y2 != s.Via.Y || last.Width != 8 {
		t.Errorf("track ends %+v %+v via %+v", s.Track, last, s.Via)
	}
	for _, n := range all {
		for _, w := range tracks {
			if w.Layer != 1 {
				continue
			}
			if d := segSegDist(n.X1, n.Y1, n.X2, n.Y2, w.X1, w.Y1, w.X2, w.Y2) - n.Width/2 - w.Width/2; d < 6 {
				t.Errorf("segment %+v is %.2f from %+v", n, d, w)
			}
		}
	}
	if at := stitchOps(got)["addTracks"].([]map[string]any); len(at) != len(all) {
		t.Errorf("edit has %d tracks, the path %d", len(at), len(all))
	}
}

// On a trial a cut plane fails only the stitch nearest the lost pad, measured to
// its track as well as its via.
func TestStitchCuttersNearestOnly(t *testing.T) {
	at := map[string][2]float64{"lost": {1000, 1000}}
	viaNear := stitch{PadID: "a", Via: specctra.NewVia{X: 1100, Y: 1000}}
	trackNear := stitch{PadID: "b", Via: specctra.NewVia{X: 1300, Y: 1000},
		Track: specctra.NewTrack{X1: 1000, Y1: 1020, X2: 1300, Y2: 1020}}
	plan := []stitch{viaNear, trackNear}
	viaNear.Track = specctra.NewTrack{X1: 1100, Y1: 1000, X2: 1100, Y2: 1000}
	plan[0] = viaNear
	lost := map[string]bool{"lost": true}
	cut := stitchCutters(lost, plan, at, true)
	if !cut.stitches["b"] || cut.stitches["a"] || !cut.named["lost"] {
		t.Errorf("nearest only: %+v", cut)
	}
	cut = stitchCutters(lost, plan, at, false)
	if !cut.stitches["a"] || !cut.stitches["b"] {
		t.Errorf("all near: %+v", cut)
	}
}

// With fills known, a site must lie wholly inside one of its net's: here the
// only fill sits far from the pad, so the near site is refused.
func TestPlanStitchInPour(t *testing.T) {
	p := boardPad{ID: "p", Net: "P", Layer: 1, X: 1000, Y: 1000, W: 30, H: 30}
	size := map[string]stitchSize{"P": {Dia: 24, Drill: 12, Track: 8}}
	fill := pcbCopperArea{Net: "P", Layer: 16, Contours: [][][2]float64{{{2000, 2000}, {3000, 2000}, {3000, 3000}, {2000, 3000}}}}
	inPour := func(net string, q [2]float64, dia float64) bool {
		return net == fill.Net && compoundWinding(fill.Contours, q) != 0 && stitchFillMargin(fill, q) >= dia/2
	}
	if got, none := planStitchPaths([]boardPad{p}, []boardPad{p}, nil, nil, size, 6, stitchTestBounds, map[string]bool{}, 0, nil); len(got) != 1 || len(none) != 0 {
		t.Fatalf("no fill check: %+v", got)
	}
	if got, none := planStitchPaths([]boardPad{p}, []boardPad{p}, nil, nil, size, 6, stitchTestBounds, map[string]bool{}, 0, inPour); len(got) != 0 || len(none) != 1 {
		t.Fatalf("fill check: %+v", got)
	}
	if !inPour("P", [2]float64{2500, 2500}, 24) || inPour("P", [2]float64{2005, 2500}, 24) {
		t.Error("margin")
	}
}

// The KiCad snapshot names a poured zone pourPrimitiveId; its fills must parse.
func TestParseKicadPouredFills(t *testing.T) {
	poured := []any{map[string]any{"pourPrimitiveId": "z-gnd", "net": "GND", "layer": 2.0, "fills": []any{
		map[string]any{"source": []any{[]any{20.0, -20.0, "L", 1550.0, -20.0, 1550.0, -1160.0, 20.0, -1160.0}}},
	}}}
	areas, err := parseCopperAreaObstacles(nil, poured)
	if err != nil || len(areas) != 1 || areas[0].Net != "GND" || areas[0].Layer != 2 {
		t.Fatalf("areas %+v err %v", areas, err)
	}
}
