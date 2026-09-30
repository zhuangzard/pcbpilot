package pcbauto

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"
)

// viaWindowBoard is a 2-layer board whose 2 A net PWR must change layer
// (source pad on TOP, load pad on BOTTOM) and may place vias only inside
// the given windows (a no-via keep-out covers the rest of the board).
func viaWindowBoard(windows []Rect) (*Board, *Analysis, *Stackup) {
	board := Rect{0, 0, 2000, 800}
	b := &Board{Rules: DefaultRules(), Outline: board.Corners(), CopperLayers: 2}
	b.Parts = []*Part{
		{Ref: "S", Device: "X", Pos: Point{250, 300}, Pads: []*Pad{{Number: "1", Net: "PWR", Layer: LayerTop, Box: OrientedBox{C: Point{250, 300}, W: 80, H: 80}}}},
		{Ref: "L", Device: "X", Pos: Point{1750, 300}, Side: LayerBottom, Pads: []*Pad{{Number: "1", Net: "PWR", Layer: LayerBottom, Box: OrientedBox{C: Point{1750, 300}, W: 80, H: 80}}}},
	}
	xs := []float64{board.MinX, board.MaxX}
	ys := []float64{board.MinY, board.MaxY}
	for _, w := range windows {
		xs = append(xs, w.MinX, w.MaxX)
		ys = append(ys, w.MinY, w.MaxY)
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	for i := 1; i < len(xs); i++ {
		for j := 1; j < len(ys); j++ {
			c := Rect{xs[i-1], ys[j-1], xs[i], ys[j]}
			if c.W() <= 0 || c.H() <= 0 {
				continue
			}
			in := false
			for _, w := range windows {
				if c.Center().X > w.MinX && c.Center().X < w.MaxX && c.Center().Y > w.MinY && c.Center().Y < w.MaxY {
					in = true
				}
			}
			if !in {
				b.Keepouts = append(b.Keepouts, &Keepout{Name: "no-via", Poly: c.Corners(), NoVias: true})
			}
		}
	}
	_ = b.Index()
	sim := (&SimFile{SchemaVersion: 1, Results: []SimResult{{Scenario: "worst", Nets: map[string]SimNet{
		"PWR": {Voltage: 5, Role: "power", Pins: []SimPin{{Ref: "S", Pin: "1", CurrentA: 2, Dir: "source"}, {Ref: "L", Pin: "1", CurrentA: 2, Dir: "sink"}}},
	}}}}).Resolve()
	st := twoLayer()
	an := Analyze(b, PowerSpec{Sim: sim}, st)
	return b, an, st
}

// A transition whose window holds one via: the router moves it to the
// window with room for the whole array (or the check stays clean).
func TestViaArrayMovesToRoom(t *testing.T) {
	if testing.Short() {
		t.Skip("routes a board")
	}
	// Tight window on the straight line; a roomy one 250 mil off it.
	b, an, st := viaWindowBoard([]Rect{{980, 280, 1020, 320}, {850, 500, 1150, 700}})
	np := an.ByNet["PWR"]
	if np == nil || np.ViasPerTransition < 2 {
		t.Fatalf("PWR needs a via array: %+v", np)
	}
	rr, err := Route(context.Background(), b, st, an, RouteOptions{Timeout: 20 * time.Second, WorkRate: 3e6})
	if err != nil {
		t.Fatal(err)
	}
	if rr.Stats.Completion < 100 {
		t.Fatalf("completion %.1f%%: %v", rr.Stats.Completion, rr.Unrouted)
	}
	vc := CheckViaCurrent(b, rr.Tracks, rr.Vias, intentFromAnalysis(an))
	for _, f := range vc.Findings {
		if f.Level == "ERROR" {
			t.Errorf("via-current ERROR left: %s (notes %v)", f.Message, rr.Notes)
		}
	}
	if len(rr.ViaShortfalls) > 0 {
		t.Errorf("room exists, yet shortfalls %+v", rr.ViaShortfalls)
	}
	t.Logf("%d vias (plan %d per transition); notes: %s", len(rr.Vias), np.ViasPerTransition, strings.Join(rr.Notes, " | "))
}

// Only a one-via window: no alternative completes the array — the router
// reports it (never silently), with every alternative it tried.
func TestViaArrayShortfallReported(t *testing.T) {
	if testing.Short() {
		t.Skip("routes a board")
	}
	b, an, st := viaWindowBoard([]Rect{{980, 280, 1020, 320}})
	rr, err := Route(context.Background(), b, st, an, RouteOptions{Timeout: 20 * time.Second, WorkRate: 3e6})
	if err != nil {
		t.Fatal(err)
	}
	if rr.Stats.Completion < 100 {
		t.Fatalf("completion %.1f%%: %v", rr.Stats.Completion, rr.Unrouted)
	}
	if len(rr.ViaShortfalls) != 1 || rr.ViaShortfalls[0].Net != "PWR" {
		t.Fatalf("want one PWR shortfall, got %+v (notes %v)", rr.ViaShortfalls, rr.Notes)
	}
	s := rr.ViaShortfalls[0]
	if len(s.Tried) < 3 || s.Reason == "" || len(s.At) == 0 {
		t.Errorf("shortfall without the alternatives/reason/site: %+v", s)
	}
	fb := viaShortFeedback(&Result{Route: rr})
	if len(fb) != 1 || fb[0].Kind != FBViaCurrent || fb[0].Severity != "high" {
		t.Errorf("feedback item: %+v", fb)
	}
	t.Logf("shortfall: %s; tried %v", s.Reason, s.Tried)
}

// intentFromAnalysis is a minimal intent carrying the analysed currents, so
// CheckViaCurrent rates the routed vias like pcb check --intent.
func intentFromAnalysis(an *Analysis) *Intent {
	in := &Intent{Nets: map[string]*IntentNet{}}
	for _, np := range an.Nets {
		if np.CurrentA > 0 {
			in.Nets[np.Net] = &IntentNet{CurrentA: np.CurrentA}
		}
	}
	return in
}
