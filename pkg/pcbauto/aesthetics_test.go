package pcbauto

import (
	"encoding/json"
	"testing"
)

// ---- synthetic board builder ------------------------------------------------

type tpad struct {
	num, net string
	dx, dy   float64
	w, h     float64
}

func twoPad(n1, n2 string) []tpad {
	return []tpad{{"1", n1, -30, 0, 25, 30}, {"2", n2, 30, 0, 25, 30}}
}

// synthPart places a part with pads given at rotation 0, then turns it.
func synthPart(b *Board, ref, dev string, pos Point, rot float64, pads []tpad) *Part {
	p := &Part{Ref: ref, Device: dev, Pos: pos, Side: LayerTop}
	for _, t := range pads {
		p.Pads = append(p.Pads, &Pad{Number: t.num, Net: t.net, Layer: LayerTop,
			Box: OrientedBox{C: Point{pos.X + t.dx, pos.Y + t.dy}, W: t.w, H: t.h}})
	}
	b.Parts = append(b.Parts, p)
	return p
}

func synthBoard(w, h float64) *Board {
	return &Board{Outline: Rect{0, 0, w, h}.Corners(), Rules: DefaultRules(), CopperLayers: 2}
}

func finish(t *testing.T, b *Board, rots map[string]float64) {
	t.Helper()
	if err := b.Index(); err != nil {
		t.Fatal(err)
	}
	for ref, r := range rots {
		p := b.Part(ref)
		p.MoveTo(p.Pos, r)
	}
}

func metric(t *testing.T, r *AestheticsReport, id string) AesMetric {
	t.Helper()
	m := r.Metric(id)
	if m == nil {
		t.Fatalf("metric %s missing", id)
	}
	if m.Skipped {
		t.Fatalf("metric %s skipped: %s", id, m.Reason)
	}
	return *m
}

// strictlyLower asserts the degraded score is strictly below the base one.
func strictlyLower(t *testing.T, id string, base, worse AesMetric) {
	t.Helper()
	if !(worse.Score < base.Score) {
		t.Errorf("%s: degraded score %.2f (value %.4f) is not below base %.2f (value %.4f)", id, worse.Score, worse.Value, base.Score, base.Value)
	}
}

// ---- routing: a straight two-resistor link ------------------------------------

func linkBoard(t *testing.T) *Board {
	b := synthBoard(1400, 400)
	synthPart(b, "R1", "10k", Point{100, 200}, 0, twoPad("A", "N1"))
	synthPart(b, "R2", "10k", Point{1230, 200}, 0, twoPad("N1", "B"))
	finish(t, b, nil)
	return b
}

func aesRun(b *Board, tracks []Track, vias []Via) *AestheticsReport {
	return Aesthetics(AesInput{Board: b, Tracks: tracks, Vias: vias, Copper: true})
}

func TestAesMonotoneSJogs(t *testing.T) {
	b := linkBoard(t)
	a, z := Point{130, 200}, Point{1200, 200}
	base := aesRun(b, []Track{{Net: "N1", Layer: 1, A: a, B: z, Width: 6}}, nil)
	pts := []Point{a, {500, 200}, {506.4, 206.4}, {700, 206.4}, {706.4, 200}, z}
	var jog []Track
	for i := 0; i+1 < len(pts); i++ {
		jog = append(jog, Track{Net: "N1", Layer: 1, A: pts[i], B: pts[i+1], Width: 6})
	}
	worse := aesRun(b, jog, nil)
	if n := metric(t, worse, "R3").Extra["count"]; n != 2 {
		t.Fatalf("injected 2 S-jogs, measured %v", n)
	}
	strictlyLower(t, "R3", metric(t, base, "R3"), metric(t, worse, "R3"))
	if !(metric(t, worse, "R4").Value > metric(t, base, "R4").Value) {
		t.Errorf("R4 bend density did not rise with injected jogs")
	}
}

func TestAesMonotonePadEntryRotated30(t *testing.T) {
	b := linkBoard(t)
	a, z := Point{130, 200}, Point{1200, 200}
	base := aesRun(b, []Track{{Net: "N1", Layer: 1, A: a, B: z, Width: 6}}, nil)
	// Enter R2.1 at 30° instead of along its axis.
	mid := Point{1200 - 60*0.8660254, 200 - 60*0.5}
	worse := aesRun(b, []Track{
		{Net: "N1", Layer: 1, A: a, B: Point{mid.X, 200}, Width: 6},
		{Net: "N1", Layer: 1, A: Point{mid.X, 200}, B: mid, Width: 6},
		{Net: "N1", Layer: 1, A: mid, B: z, Width: 6},
	}, nil)
	strictlyLower(t, "R2", metric(t, base, "R2"), metric(t, worse, "R2"))
	strictlyLower(t, "R1", metric(t, base, "R1"), metric(t, worse, "R1"))
	if metric(t, worse, "R2").Extra["skew"] < 1 {
		t.Errorf("30° entry not classed as skew: %+v", metric(t, worse, "R2").Extra)
	}
}

func TestAesMonotoneOffGridVias(t *testing.T) {
	b := linkBoard(t)
	a := Point{130, 200}
	mk := func(v Point) *AestheticsReport {
		return aesRun(b, []Track{
			{Net: "N1", Layer: 1, A: a, B: Point{v.X, 200}, Width: 6},
			{Net: "N1", Layer: 1, A: Point{v.X, 200}, B: v, Width: 6},
			{Net: "N1", Layer: 2, A: v, B: Point{v.X + 300, v.Y}, Width: 6},
			{Net: "N1", Layer: 2, A: Point{v.X + 300, v.Y}, B: Point{v.X + 300, 200}, Width: 6},
		}, []Via{{Net: "N1", C: v, Dia: 24, Drill: 12}})
	}
	base := mk(Point{600, 300})
	worse := mk(Point{601.3, 301.3})
	strictlyLower(t, "R7", metric(t, base, "R7"), metric(t, worse, "R7"))
}

// Routing less must never look better: an open connection counts as 0.
func TestAesUnroutedNeverHelps(t *testing.T) {
	b := linkBoard(t)
	pts := []Point{{130, 200}, {500, 200}, {506.4, 206.4}, {700, 206.4}, {706.4, 200}, {1200, 200}}
	var jog []Track
	for i := 0; i+1 < len(pts); i++ {
		jog = append(jog, Track{Net: "N1", Layer: 1, A: pts[i], B: pts[i+1], Width: 6})
	}
	full := aesRun(b, jog, nil)
	half := aesRun(b, jog[:2], nil) // the connection is left open
	if !(half.RoutedShare < full.RoutedShare) {
		t.Fatalf("routed share %.3f not below %.3f", half.RoutedShare, full.RoutedShare)
	}
	if half.Routing > full.Routing {
		t.Errorf("dropping copper raised the routing group: %.2f > %.2f", half.Routing, full.Routing)
	}
}

// Electrical priority: a differential pair is exempt, not penalised.
func TestAesDiffPairExempt(t *testing.T) {
	b := synthBoard(1400, 600)
	synthPart(b, "R1", "22R", Point{100, 200}, 0, twoPad("A", "USB_DP"))
	synthPart(b, "R2", "22R", Point{1230, 200}, 0, twoPad("USB_DP", "B"))
	finish(t, b, nil)
	pts := []Point{{130, 200}, {500, 200}, {506.4, 206.4}, {700, 206.4}, {706.4, 200}, {1200, 200}}
	var jog []Track
	for i := 0; i+1 < len(pts); i++ {
		jog = append(jog, Track{Net: "USB_DP", Layer: 1, A: pts[i], B: pts[i+1], Width: 6})
	}
	// USB_DM exists so the pair is recognised.
	synthPart(b, "R3", "22R", Point{100, 400}, 0, twoPad("C", "USB_DM"))
	synthPart(b, "R4", "22R", Point{1230, 400}, 0, twoPad("USB_DM", "D"))
	finish(t, b, nil)
	r := aesRun(b, append(jog, Track{Net: "USB_DM", Layer: 1, A: Point{130, 400}, B: Point{1200, 400}, Width: 6}), nil)
	if n := metric(t, r, "R3").Extra["count"]; n != 0 {
		t.Errorf("diff-pair jogs were penalised (%v S-jogs)", n)
	}
	found := false
	for _, e := range r.Exemptions {
		found = found || e.Kind == "diff-pair"
	}
	if !found {
		t.Errorf("no diff-pair exemption recorded: %+v", r.Exemptions)
	}
}

// ---- placement ---------------------------------------------------------------

func rowBoard(t *testing.T, dy2, dx3 float64, rot map[string]float64) *AestheticsReport {
	b := synthBoard(1000, 600)
	synthPart(b, "R1", "10k", Point{100, 300}, 0, twoPad("N1", "N2"))
	synthPart(b, "R2", "10k", Point{200, 300 + dy2}, 0, twoPad("N2", "N3"))
	synthPart(b, "R3", "10k", Point{300 + dx3, 300}, 0, twoPad("N3", "N4"))
	synthPart(b, "R4", "10k", Point{400, 300}, 0, twoPad("N4", "N5"))
	finish(t, b, rot)
	return Aesthetics(AesInput{Board: b})
}

func TestAesMonotonePerturbedRow(t *testing.T) {
	base := rowBoard(t, 0, 0, nil)
	if v := metric(t, base, "P1").Value; v != 0 {
		t.Fatalf("aligned row reported %v unaligned", v)
	}
	strictlyLower(t, "P1", metric(t, base, "P1"), metric(t, rowBoard(t, 7, 0, nil), "P1"))
	strictlyLower(t, "P2", metric(t, base, "P2"), metric(t, rowBoard(t, 0, 23, nil), "P2"))
}

func TestAesMonotoneMixedRotations(t *testing.T) {
	base := rowBoard(t, 0, 0, nil)
	worse := rowBoard(t, 0, 0, map[string]float64{"R3": 90})
	strictlyLower(t, "P3", metric(t, base, "P3"), metric(t, worse, "P3"))
	folded := rowBoard(t, 0, 0, map[string]float64{"R3": 180}) // mod 180 same axis, still a tidy defect
	strictlyLower(t, "P3", metric(t, base, "P3"), metric(t, folded, "P3"))
}

func TestAesMonotoneOffGridOrigins(t *testing.T) {
	b := synthBoard(1000, 600)
	synthPart(b, "R1", "10k", Point{100, 300}, 0, twoPad("N1", "N2"))
	synthPart(b, "R2", "10k", Point{300, 300}, 0, twoPad("N2", "N3"))
	finish(t, b, nil)
	base := Aesthetics(AesInput{Board: b})
	b.Part("R2").MoveTo(Point{301.7, 300}, 0)
	worse := Aesthetics(AesInput{Board: b})
	strictlyLower(t, "P9", metric(t, base, "P9"), metric(t, worse, "P9"))
}

// dualChannel builds two identical LDO channels (regulator, in/out caps,
// LED indicator) mirrored about x = 1000.
func dualChannel(t *testing.T, mut func(b *Board)) *AestheticsReport {
	b := synthBoard(2000, 1000)
	ldo := []tpad{{"1", "GND", -90, -100, 30, 60}, {"2", "OUT", 0, -100, 30, 60}, {"3", "IN", 90, -100, 30, 60}}
	sub := func(pads []tpad, m map[string]string) []tpad {
		out := append([]tpad(nil), pads...)
		for i := range out {
			if v, ok := m[out[i].net]; ok {
				out[i].net = v
			}
		}
		return out
	}
	synthPart(b, "J1", "HDR-1x4", Point{1000, 120}, 0, []tpad{{"1", "VIN_A", -60, 0, 40, 60}, {"2", "GND", -20, 0, 40, 60}, {"3", "GND", 20, 0, 40, 60}, {"4", "VIN_B", 60, 0, 40, 60}})
	chA := map[string]string{"IN": "VIN_A", "OUT": "+3V3"}
	chB := map[string]string{"IN": "VIN_B", "OUT": "+2V5"}
	// channel A (left)
	synthPart(b, "U1", "AMS1117-3.3", Point{700, 500}, 0, sub(ldo, chA))
	synthPart(b, "C1", "10uF", Point{550, 420}, 0, twoPad("VIN_A", "GND"))
	synthPart(b, "C2", "10uF", Point{850, 420}, 0, twoPad("+3V3", "GND"))
	synthPart(b, "R1", "1k", Point{850, 700}, 0, twoPad("+3V3", "LED_A"))
	synthPart(b, "LED1", "LED-0603-G", Point{700, 700}, 0, twoPad("GND", "LED_A"))
	// channel B (right): positions mirrored, rotations mirrored (LED 180)
	synthPart(b, "U2", "AMS1117-3.3", Point{1300, 500}, 0, sub(ldo, chB))
	synthPart(b, "C3", "10uF", Point{1450, 420}, 0, twoPad("VIN_B", "GND"))
	synthPart(b, "C4", "10uF", Point{1150, 420}, 0, twoPad("+2V5", "GND"))
	synthPart(b, "R2", "1k", Point{1150, 700}, 0, twoPad("+2V5", "LED_B"))
	synthPart(b, "LED2", "LED-0603-G", Point{1300, 700}, 0, twoPad("GND", "LED_B"))
	finish(t, b, map[string]float64{"LED2": 180})
	if mut != nil {
		mut(b)
	}
	return Aesthetics(AesInput{Board: b})
}

func TestAesSymmetryDualChannel(t *testing.T) {
	base := dualChannel(t, nil)
	if len(base.Symmetry) == 0 {
		t.Fatal("dual-channel board: no isomorphic group detected")
	}
	pairOK := false
	for _, g := range base.Symmetry {
		js, _ := json.Marshal(g)
		t.Logf("group %s", js)
		for _, p := range g.Pairs {
			for _, m := range p.Match {
				if m == [2]string{"LED1", "LED2"} || m == [2]string{"U1", "U2"} {
					pairOK = true
				}
			}
		}
		if g.Axis.Type != "vertical" || g.Axis.Coord != 1000 {
			t.Errorf("group %s: axis %+v, want vertical x=1000", g.Signature, g.Axis)
		}
	}
	if !pairOK {
		t.Error("channels were not put in correspondence (LED1↔LED2 / U1↔U2)")
	}
	p4 := metric(t, base, "P4")
	if p4.Score < 90 {
		t.Errorf("mirrored dual channel scores P4 %.1f, want ≥ 90", p4.Score)
	}
	broken := dualChannel(t, func(b *Board) {
		p := b.Part("C3")
		p.MoveTo(p.Pos.Add(Point{70, 90}), 0)
		b.Part("LED2").MoveTo(b.Part("LED2").Pos, 0) // rotation no longer mirrored
	})
	strictlyLower(t, "P4", p4, metric(t, broken, "P4"))
}
