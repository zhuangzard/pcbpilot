package pcbauto

import (
	"context"
	"math"
	"testing"
	"time"
)

// Aesthetics phase B (beautify.go, plane.go fan-out rays): synthetic
// boards with one defect each.

// bfyBoard is a 2-layer board with two-pad parts at the given pad centres
// (pad 1 on net, pad 2 on a dummy net 80 mil below) plus extra parts.
func bfyBoard(net string, at []Point, extra ...*Part) *Board {
	var parts []*Part
	for i, c := range at {
		ref := string(rune('A'+i)) + "1"
		parts = append(parts, hvPart(ref, "0805", c,
			smdPad("1", net, 0, 0, 40, 20), smdPad("2", "", 0, -80, 40, 20)))
	}
	return hvBoard(append(parts, extra...)...)
}

func bfyTracks(net string, w float64, pts ...Point) []Track {
	var ts []Track
	for i := 1; i < len(pts); i++ {
		ts = append(ts, Track{Net: net, Layer: LayerTop, A: pts[i-1], B: pts[i], Width: w, Kind: "route"})
	}
	return ts
}

func bfyRun(t *testing.T, b *Board, ts []Track, vs []Via) ([]Track, []Via, *BeautifyStats, *Analysis, *Stackup) {
	t.Helper()
	st := DecideStackup(b, Analyze(b, PowerSpec{}, nil), StackOptions{Force: 2})
	an := Analyze(b, PowerSpec{}, st)
	rr := &RouteResult{Tracks: ts, Vias: vs}
	if v := CheckDRCStrict(b, an, st, ts, vs).Violations; len(v) != 0 {
		t.Fatalf("fixture not DRC-clean: %+v", v)
	}
	nt, nv, stats := beautify(b, an, Understand(b, an), st, rr, false)
	if v := CheckDRCStrict(b, an, st, nt, nv).Violations; len(v) != 0 {
		t.Fatalf("beautify made DRC violations: %+v", v)
	}
	if d := CheckDRCStrict(b, an, st, nt, nv).Disconnected; len(d) != 0 {
		t.Fatalf("beautify disconnected: %+v", d)
	}
	return nt, nv, stats, an, st
}

func aesMetricOf(t *testing.T, b *Board, an *Analysis, st *Stackup, ts []Track, vs []Via, id string) AesMetric {
	t.Helper()
	rep := Aesthetics(AesInputFromResult(b, an, nil, st, &RouteResult{Tracks: ts, Vias: vs}, nil))
	for _, m := range rep.Metrics {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("metric %s missing", id)
	return AesMetric{}
}

// sJogRoute: pad → out → 45° → leg at y=1050 → a one-cell (3.2 mil) jog →
// leg at y=1053.2 → 45° → the far pad.
func sJogRoute(net string) []Track {
	return bfyTracks(net, 6, Point{500, 1000}, Point{600, 1000}, Point{650, 1050}, Point{800, 1050},
		Point{803.2, 1053.2}, Point{1100, 1053.2}, Point{1150, 1003.2}, Point{1500, 1003.2})
}

func TestBeautifyRemovesSJog(t *testing.T) {
	b := bfyBoard("SIG", []Point{{500, 1000}, {1500, 1003.2}})
	ts := sJogRoute("SIG")
	_, _, _, an, st := bfyRun(t, b, ts, nil)
	if m := aesMetricOf(t, b, an, st, ts, nil, "R3"); m.Extra["count"] != 1 {
		t.Fatalf("fixture: %v S-jogs, want 1 (%s)", m.Extra["count"], m.Detail)
	}
	nt, _, stats, _, _ := bfyRun(t, b, ts, nil)
	if stats.SJogs != 1 {
		t.Fatalf("stats %+v", *stats)
	}
	if m := aesMetricOf(t, b, an, st, nt, nil, "R3"); m.Extra["count"] != 0 {
		t.Fatalf("after: %v S-jogs (%s)", m.Extra["count"], m.Detail)
	}
	for _, tr := range nt {
		if odir(tr.A, tr.B) < 0 {
			t.Fatalf("non-octilinear segment %+v", tr)
		}
	}
}

// Blocked on both sides (the jog is what clears the obstacles): it stays,
// and the copper stays DRC-clean.
func TestBeautifyKeepsJogWhenBlocked(t *testing.T) {
	obs := hvPart("X1", "OBS", Point{0, 0},
		smdPad("1", "OBS1", 700, 1065, 10, 10), smdPad("2", "OBS2", 1000, 1039, 10, 10))
	b := bfyBoard("SIG", []Point{{500, 1000}, {1500, 1003.2}}, obs)
	ts := sJogRoute("SIG")
	nt, _, stats, an, st := bfyRun(t, b, ts, nil)
	if stats.SJogs != 0 {
		t.Fatalf("jog removed through an obstacle: %+v", *stats)
	}
	if m := aesMetricOf(t, b, an, st, nt, nil, "R3"); m.Extra["count"] != 1 {
		t.Fatalf("after: %v S-jogs", m.Extra["count"])
	}
}

// A skewed pad stub (centre → access node → 45° out of the pad side) is
// rebuilt: the leg leaving the pad starts at the centre and runs along a
// pad axis.
func TestBeautifyPadEntryAlongAxis(t *testing.T) {
	b := bfyBoard("SIG", []Point{{500, 1000}, {1200, 1049}})
	ts := bfyTracks("SIG", 6, Point{500, 1000}, Point{503.2, 1006.4}, Point{546, 1049.2}, Point{1200, 1049.2}, Point{1200, 1049})
	ts = ts[:3]
	ts = append(ts, Track{Net: "SIG", Layer: LayerTop, A: Point{1200, 1049.2}, B: Point{1200, 1049}, Width: 6, Kind: "route"})
	_, _, _, an, st := bfyRun(t, b, ts, nil)
	before := aesMetricOf(t, b, an, st, ts, nil, "R2")
	nt, _, stats, _, _ := bfyRun(t, b, ts, nil)
	after := aesMetricOf(t, b, an, st, nt, nil, "R2")
	if stats.PadEntries == 0 || after.Value <= before.Value {
		t.Fatalf("entry not improved: %+v; R2 %.3f → %.3f (%s → %s)", *stats, before.Value, after.Value, before.Detail, after.Detail)
	}
	if after.Extra["skew"] != 0 || after.Extra["cornerExits"] != 0 || after.Extra["offCentre"] != 0 {
		t.Fatalf("after: %s", after.Detail)
	}
}

// Free segments land on the 5 mil grid; the vertices between two moved
// segments are on grid points.
func TestBeautifySnapsToGrid(t *testing.T) {
	b := bfyBoard("SIG", []Point{{500.7, 1000.3}, {1300.7, 1000.3}})
	ts := bfyTracks("SIG", 6, Point{500.7, 1000.3}, Point{600.7, 1000.3}, Point{650.7, 1050.3}, Point{900.7, 1050.3},
		Point{950.7, 1000.3}, Point{1300.7, 1000.3})
	nt, _, stats, _, _ := bfyRun(t, b, ts, nil)
	if stats.LinesSnapped == 0 {
		t.Fatalf("stats %+v", *stats)
	}
	on := 0
	for _, tr := range nt {
		for _, p := range []Point{tr.A, tr.B} {
			if onGrid(p.X, 5, 0.01) && onGrid(p.Y, 5, 0.01) {
				on++
			}
		}
	}
	if on < 2 {
		t.Fatalf("no vertex on the 5 mil grid: %+v", nt)
	}
}

// Differential pairs keep their geometry: the pair's own rules shape it.
func TestBeautifyLeavesDiffPairs(t *testing.T) {
	b := bfyBoard("USB_DP", []Point{{500, 1000}, {1500, 1003.2}})
	b.Parts = append(b.Parts, hvPart("Z1", "0805", Point{500, 1300}, smdPad("1", "USB_DM", 0, 0, 40, 20)),
		hvPart("Z2", "0805", Point{1500, 1303.2}, smdPad("1", "USB_DM", 0, 0, 40, 20)))
	if err := b.Index(); err != nil {
		t.Fatal(err)
	}
	ts := sJogRoute("USB_DP")
	ts = append(ts, bfyTracks("USB_DM", 6, Point{500, 1300}, Point{1500, 1300}, Point{1500, 1303.2})...)
	nt, _, stats, _, _ := bfyRun(t, b, ts, nil)
	if stats.changes() != 0 || len(nt) != len(ts) {
		t.Fatalf("pair copper changed: %+v", *stats)
	}
}

// The gate refuses any figure that ranks above aesthetics getting worse.
func TestBeautifyGateFacts(t *testing.T) {
	base := routeFacts{drc: 0, vias: 10, completion: 100, electrical: 80, items: map[string]float64{"decap-loop": 50, "ir-drop": 100},
		irMV: map[string]float64{"VOUT": 12}, underWidth: 3}
	same := base
	same.items = map[string]float64{"decap-loop": 50, "ir-drop": 100}
	same.irMV = map[string]float64{"VOUT": 12}
	if w := same.worseThan(base); w != "" {
		t.Fatalf("equal facts judged worse: %s", w)
	}
	for name, f := range map[string]func(*routeFacts){
		"drc":        func(r *routeFacts) { r.drc++ },
		"vias":       func(r *routeFacts) { r.vias-- },
		"completion": func(r *routeFacts) { r.completion -= 0.1 },
		"electrical": func(r *routeFacts) { r.electrical -= 0.01 },
		"item":       func(r *routeFacts) { r.items = map[string]float64{"decap-loop": 49.9, "ir-drop": 100} },
		"si":         func(r *routeFacts) { r.si++ },
		"iso":        func(r *routeFacts) { r.iso++ },
		"plane":      func(r *routeFacts) { r.planeOpen++ },
		"disconn":    func(r *routeFacts) { r.disconnected++ },
		"irviol":     func(r *routeFacts) { r.irViol++ },
		"underwidth": func(r *routeFacts) { r.underWidth += 1 },
		"irmv":       func(r *routeFacts) { r.irMV = map[string]float64{"VOUT": 12.5} },
	} {
		w := base
		w.items = map[string]float64{"decap-loop": 50, "ir-drop": 100}
		w.irMV = map[string]float64{"VOUT": 12}
		f(&w)
		if w.worseThan(base) == "" {
			t.Errorf("%s: worse facts accepted", name)
		}
	}
}

// Fan-out vias sit on the pad centre's octilinear rays: every fan-out stub
// is a 0/45/90° line.
func TestFanoutStubsOctilinear(t *testing.T) {
	var parts []*Part
	for i := 0; i < 6; i++ {
		x := 600 + float64(i)*230
		parts = append(parts, hvPart("C"+string(rune('1'+i)), "0603", Point{x, 1000},
			smdPad("1", "VCC", 0, 31, 31, 28), smdPad("2", "GND", 0, -31, 31, 28)))
	}
	parts = append(parts, hvPart("U1", "LDO", Point{1300, 1400},
		smdPad("1", "VCC", -45, 0, 24, 50), smdPad("2", "GND", 0, 0, 24, 50), smdPad("3", "VIN", 45, 0, 24, 50)))
	b := hvBoard(parts...)
	b.CopperLayers = 4
	b.Outline = Rect{450, 800, 1850, 1550}.Corners()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, b, Options{Stack: StackOptions{Force: 4}, Route: RouteOptions{Timeout: 20 * time.Second, WorkRate: 3e6}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("attempts %+v stats %+v", res.Attempts, res.Route.Stats)
	stubs := 0
	for _, tr := range res.Route.Tracks {
		if tr.Kind != "fanout" {
			continue
		}
		stubs++
		if d := octiDev(angDeg(tr.A, tr.B)); d > aesAngTol {
			t.Errorf("fan-out stub %.1f° off octilinear: %+v", d, tr)
		}
	}
	if stubs == 0 || res.Route.Stats.FanoutVias == 0 {
		t.Fatalf("no fan-out: %+v", res.Route.Stats)
	}
	if len(res.DRC.Violations) != 0 {
		t.Fatalf("DRC %+v", res.DRC.Violations)
	}
	_ = math.Pi
}

// The rollback keeps every net but the one that makes a figure worse.
func TestBeautifyGateSearch(t *testing.T) {
	nets := []string{"A", "B", "C", "D", "E", "F", "G"}
	calls := 0
	keep := gateSearch(nets, func(set []string) bool {
		calls++
		for _, n := range set {
			if n == "E" {
				return false
			}
		}
		return true
	})
	if len(keep) != 6 {
		t.Fatalf("kept %v", keep)
	}
	for _, n := range keep {
		if n == "E" {
			t.Fatalf("kept the failing net: %v", keep)
		}
	}
	if calls > gateMaxEvals {
		t.Fatalf("%d gate evaluations", calls)
	}
}
