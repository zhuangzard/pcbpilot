package app

import (
	"math"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// v0.9.0's rule: a narrow track passes when ONE end lies within 50 mil of a
// same-net pad's bounding box. padEscape must never accept a track that rule
// rejects (the third-party review's non-monotonicity finding).
func legacyNearSameNetPad(t specctra.Track, pads []boardPad) bool {
	for _, p := range pads {
		if p.Layer != t.Layer && p.Layer != pcbLayerMulti {
			continue
		}
		for _, e := range [][2]float64{{t.X1, t.Y1}, {t.X2, t.Y2}} {
			d := math.Hypot(math.Max(math.Abs(e[0]-p.X)-p.W/2, 0), math.Max(math.Abs(e[1]-p.Y)-p.H/2, 0))
			if d <= 50 {
				return true
			}
		}
	}
	return false
}

// need=30 → reach 90 > 50: a 10 mil segment lying wholly 60..80 mil from the
// pad failed under v0.9.0 and must still fail.
func TestPadEscapeNeverWiderThanLegacy(t *testing.T) {
	pads := []boardPad{{Net: "N", Layer: 1, X: 0, Y: 0, W: 20, H: 20}}
	far := specctra.Track{Net: "N", Layer: 1, X1: 70, Y1: 0, X2: 90, Y2: 0, Width: 10} // 60..80 from the edge
	if padEscape(far, 30, pads) {
		t.Fatal("segment wholly beyond 50 mil accepted as pad escape")
	}
	if legacyNearSameNetPad(far, pads) {
		t.Fatal("test premise: legacy rule must reject it too")
	}
	// Sweep: every segment padEscape accepts is accepted by the legacy rule.
	for _, need := range []float64{6, 10, 15.75, 17, 30, 60} {
		for x1 := 0.0; x1 <= 140; x1 += 7 {
			for x2 := 0.0; x2 <= 140; x2 += 11 {
				tr := specctra.Track{Net: "N", Layer: 1, X1: x1, Y1: 3, X2: x2, Y2: -9, Width: 10}
				if padEscape(tr, need, pads) && !legacyNearSameNetPad(tr, pads) {
					t.Fatalf("need=%v track %+v accepted by padEscape but not by v0.9.0's rule", need, tr)
				}
			}
		}
	}
	// A router-produced stub (starts at the pad, ends inside reach) still passes.
	stub := specctra.Track{Net: "N", Layer: 1, X1: 10, Y1: 0, X2: 10 + 40, Y2: 0, Width: 10}
	if !padEscape(stub, 15.75, pads) {
		t.Fatal("pad-start stub inside the reach rejected")
	}
}

// A round pad's bounding box overstates its copper: a point at the box corner
// region is nearer to the box than to the circle.
func TestPadEscapeUsesRealPadCopper(t *testing.T) {
	round := boardPad{Net: "N", Layer: 1, X: 0, Y: 0, W: 100, H: 100, Shape: []any{"ELLIPSE", 100.0, 100.0}}
	// (70,70): 28.3 mil from the bbox corner, 49.5 from the circle.
	if d := padCopperDist(round, 70, 70); math.Abs(d-(math.Hypot(70, 70)-50)) > 1e-6 {
		t.Fatalf("ELLIPSE dist = %v", d)
	}
	tr := specctra.Track{Net: "N", Layer: 1, X1: 70, Y1: 70, X2: 72, Y2: 72, Width: 10}
	if padEscape(tr, 15.75, []boardPad{round}) { // reach 47.25 < 49.5
		t.Fatal("segment 49 mil from the circle's copper accepted")
	}
	// Rounded rect: corner cut away.
	rr := boardPad{Layer: 1, W: 40, H: 40, Shape: []any{"RECT", 40.0, 40.0, 20.0}}
	if d := padCopperDist(rr, 20, 20); d < math.Hypot(20, 20)-20-1e-6 {
		t.Fatalf("rounded corner dist = %v", d)
	}
	// Off-axis rotation: inscribed circle, never the bounding box.
	rot := boardPad{Layer: 1, W: 60, H: 60, Rotation: 45, Shape: []any{"RECT", 60.0, 10.0, 0.0}}
	if d := padCopperDist(rot, 0, 40); d < 40-5-1e-6 {
		t.Fatalf("rotated pad dist = %v", d)
	}
	// Polygon / special / unsized pads: centre distance, never nearer.
	if d := padCopperDist(boardPad{Layer: 1, W: 50, H: 50, Shape: []any{"POLYGON", nil}}, 30, 0); d != 30 {
		t.Fatalf("polygon dist = %v", d)
	}
	// Plain axis-aligned rectangle unchanged vs the bounding box.
	if d := padCopperDist(boardPad{Layer: 1, W: 20, H: 10}, 15, 0); math.Abs(d-5) > 1e-9 {
		t.Fatalf("rect dist = %v", d)
	}
}

// intent nets and board nets differing in case must still be checked.
func TestIntentWidthsNetNameCaseInsensitive(t *testing.T) {
	reqs := map[string]specctra.NetRequirement{"+3v3": {OuterMil: 15.75, InnerMil: 15.75, MinMil: 10}}
	tr := specctra.Track{ID: "x", Net: "+3V3", Layer: 1, X1: 300, Y1: 0, X2: 320, Y2: 0, Width: 8}
	if vs := checkIntentWidths([]specctra.Track{tr}, nil, reqs, nil); len(vs) != 1 {
		t.Fatalf("violations = %+v, want 1 (net name case must not skip the check)", vs)
	}
}
