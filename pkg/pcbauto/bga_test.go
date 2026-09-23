package pcbauto

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// bgaBoard: a 10×10, 0.65 mm-pitch BGA (11 mil balls) on a 4-layer board with
// K230's real rules (4 mil clearance, 4.7 mil track, 0.15/0.25 mm vias). Every
// third ball is ground; the others are signals fanned to header pins on the
// board edge. Inner signals cannot leave on top: they need dog-bones.
func bgaBoard() *Board {
	const pitch = 25.6
	rules := DefaultRules()
	rules.Clearance, rules.TrackWidth, rules.MinTrack, rules.ViaDrill, rules.ViaDia = 4, 4.7, 4, 6, 10
	b := &Board{Rules: rules, CopperLayers: 6, Outline: Rect{0, 0, 1400, 1400}.Corners()}
	u := &Part{Ref: "U1", Device: "SOC-BGA100", Pos: Point{700, 700}}
	// Four edge headers (N/E/S/W): every signal goes to the side its ball
	// faces, as on a real board — the test measures BGA escape, not how many
	// tracks one connector can swallow.
	hdr := map[int]*Part{}
	count := map[int]int{}
	names := []string{"JN", "JE", "JS", "JW"}
	for i, n := range names {
		hdr[i] = &Part{Ref: n, Device: "HEADER", Fixed: true}
	}
	pin := func(side, k int) Point {
		off := 250 + float64(k)*50
		switch side {
		case 0:
			return Point{off, 1300}
		case 1:
			return Point{1300, off}
		case 2:
			return Point{off, 100}
		}
		return Point{100, off}
	}
	k := 0
	for r := 0; r < 10; r++ {
		for c := 0; c < 10; c++ {
			x := 700 + (float64(c)-4.5)*pitch
			y := 700 + (float64(r)-4.5)*pitch
			net := "GND"
			if (r*10+c)%3 != 0 {
				net = fmt.Sprintf("S%d", k)
				k++
				dx, dy := x-700, y-700
				side := 3
				switch {
				case dy >= abs(dx):
					side = 0
				case dx >= abs(dy):
					side = 1
				case -dy >= abs(dx):
					side = 2
				}
				h := hdr[side]
				h.Pads = append(h.Pads, &Pad{Number: fmt.Sprint(count[side] + 1), Net: net, Layer: LayerMulti, Drill: 16,
					Box: OrientedBox{C: pin(side, count[side]), W: 28, H: 28, Round: true}})
				count[side]++
			}
			u.Pads = append(u.Pads, &Pad{Number: fmt.Sprintf("%c%d", 'A'+r, c+1), Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{x, y}, W: 11, H: 11, Round: true}})
		}
	}
	b.Parts = []*Part{u}
	for i := range names {
		h := hdr[i]
		h.Pads = append(h.Pads, &Pad{Number: "G", Net: "GND", Layer: LayerMulti, Drill: 16, Box: OrientedBox{C: pin(i, count[i]), W: 28, H: 28, Round: true}})
		b.Parts = append(b.Parts, h)
	}
	_ = b.Index()
	return b
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestDetectBGA(t *testing.T) {
	b := bgaBoard()
	gs := detectBGAs(b)
	if len(gs) != 1 || gs[0].part.Ref != "U1" {
		t.Fatalf("want U1 detected, got %d", len(gs))
	}
	g := gs[0]
	if g.pitch < 25 || g.pitch > 26.2 {
		t.Errorf("pitch %.2f", g.pitch)
	}
	if rings := topEscapeRings(g.pitch, 11, b.Rules); rings != 2 {
		t.Errorf("0.65 mm pitch, 11 mil ball, 4/4.7 rules: 2 rings escape on top, got %d", rings)
	}
	if d := g.depth[b.Part("U1").Pads[0]]; d != 0 {
		t.Errorf("corner ball depth %d", d)
	}
}

func TestBGADogboneRoutes(t *testing.T) {
	route := func(noBGA bool) *RouteResult {
		b := bgaBoard()
		out, err := Run(context.Background(), b, Options{Stack: StackOptions{Force: 6}, NoEscalate: true,
			Route: RouteOptions{Timeout: 60 * time.Second, BGA: !noBGA}})
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range out.Route.Notes {
			if len(n) > 3 && n[:3] == "BGA" {
				t.Log(n)
			}
		}
		reasons := map[string]int{}
		for _, u := range out.Route.Unrouted {
			reasons[u.Reason]++
		}
		t.Logf("noBGA=%v unrouted reasons %v", noBGA, reasons)
		return out.Route
	}
	base := route(true)
	t.Logf("baseline (no dog-bone): completion %.1f%%", base.Stats.Completion)
	rr := route(false)
	t.Logf("completion %.1f%%, vias %d+%d fan-out", rr.Stats.Completion, rr.Stats.Vias, rr.Stats.FanoutVias)
	if rr.Stats.Completion+2 < base.Stats.Completion {
		t.Errorf("dog-bone fan-out must not route worse than the generic fan-out: %.1f%% vs %.1f%%", rr.Stats.Completion, base.Stats.Completion)
	}
}
