package app

import (
	"math"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

func TestPlanWiden(t *testing.T) {
	drv := specctra.Track{ID: "d", Net: "SV1_DRV", Layer: 1, X1: 0, Y1: 0, X2: 400, Y2: 0, Width: 10}
	nets := map[string]bool{"SV1_DRV": true}

	// Open board: capped at --max-mil.
	ops := planWiden([]specctra.Track{drv}, nil, nil, nets, 40, 8)
	if len(ops) != 1 || ops[0].NewWidth != 40 {
		t.Fatalf("open board: %+v", ops)
	}

	// Other-net track 30 mil away (edge at 30-5=25): 2*(25-8)=34.
	other := specctra.Track{ID: "o", Net: "GND", Layer: 1, X1: 0, Y1: 30, X2: 400, Y2: 30, Width: 10}
	ops = planWiden([]specctra.Track{drv, other}, nil, nil, nets, 40, 8)
	if len(ops) != 1 || ops[0].NewWidth != 34 {
		t.Fatalf("near track: %+v", ops)
	}

	// Same track on another layer does not limit.
	other.Layer = 2
	if ops = planWiden([]specctra.Track{drv, other}, nil, nil, nets, 40, 8); ops[0].NewWidth != 40 {
		t.Fatalf("other layer limited width: %+v", ops)
	}

	// Pad (MULTI) 16 mil away from the track centre line, 10x10: edge at 11 -> 2*(11-8)=6 < 12 -> no change.
	pad := boardPad{Net: "GND", Layer: pcbLayerMulti, X: 200, Y: 16, W: 10, H: 10}
	if ops = planWiden([]specctra.Track{drv}, nil, []boardPad{pad}, nets, 40, 8); len(ops) != 0 {
		t.Fatalf("blocked by pad but widened: %+v", ops)
	}

	// Via of another net 25 mil away, 24 mil diameter: 2*(13-8)=10 -> not > 12, unchanged.
	if ops = planWiden([]specctra.Track{drv}, []widenVia{{Net: "GND", X: 200, Y: 25, Diameter: 24}}, nil, nets, 40, 8); len(ops) != 0 {
		t.Fatalf("blocked by via but widened: %+v", ops)
	}

	// Locked and non-selected nets are left alone.
	drv.Locked = true
	if ops = planWiden([]specctra.Track{drv}, nil, nil, nets, 40, 8); len(ops) != 0 {
		t.Fatal("locked track widened")
	}
}

// Two drain nets side by side: the second sees the first one's new width.
func TestPlanWiden_AccountsForWidenedNeighbour(t *testing.T) {
	a := specctra.Track{ID: "a", Net: "SV1_DRV", Layer: 1, X1: 0, Y1: 0, X2: 400, Y2: 0, Width: 10}
	b := specctra.Track{ID: "b", Net: "SV2_DRV", Layer: 1, X1: 0, Y1: 60, X2: 400, Y2: 60, Width: 10}
	ops := planWiden([]specctra.Track{a, b}, nil, nil, map[string]bool{"SV1_DRV": true, "SV2_DRV": true}, 40, 8)
	// a: free 60-5=55 -> 40. b: free to widened a = 60-20=40 -> 2*(40-8)=64 -> 40; but to a's original edge 55 too.
	if len(ops) != 2 || ops[0].NewWidth != 40 {
		t.Fatalf("ops = %+v", ops)
	}
	// Gap check: edges 60 - 20 - 20 = 20 >= 8.
	if gap := 60 - ops[0].NewWidth/2 - ops[1].NewWidth/2; gap < 8 {
		t.Fatalf("widened neighbours too close: %v", gap)
	}
}

// Gas Module v11 A: a fastroute neck-down left mid-run (GND 10.82 mil, 84 mil
// from any pad) is grown back to the intent width where there is room.
func TestPlanWidenToIntent(t *testing.T) {
	reqs := map[string]specctra.NetRequirement{"GND": {OuterMil: 21.65, InnerMil: 43.31, MinMil: 10}}
	gnd := specctra.Track{ID: "g", Net: "GND", Layer: 1, X1: 1371.3, Y1: 1975.2, X2: 1355.9, Y2: 1975.2, Width: 10.82}
	ops := planWidenToIntent([]specctra.Track{gnd}, nil, nil, reqs, 6.2)
	if len(ops) != 1 || ops[0].NewWidth != 21.65 {
		t.Fatalf("open space: %+v", ops)
	}
	// Other-net copper 18 mil away (edge at 13): 2*(13-6.2)=13.6 — grows part way.
	sig := specctra.Track{Net: "SIG", Layer: 1, X1: 1300, Y1: 1993.2, X2: 1400, Y2: 1993.2, Width: 10}
	ops = planWidenToIntent([]specctra.Track{gnd, sig}, nil, nil, reqs, 6.2)
	if len(ops) != 1 || ops[0].NewWidth != 13.6 {
		t.Fatalf("limited: %+v", ops)
	}
	// Inner layers use the inner width; a track already wide enough is left.
	inner := specctra.Track{Net: "GND", Layer: 15, X2: 100, Width: 43.31}
	if ops := planWidenToIntent([]specctra.Track{inner}, nil, nil, reqs, 6.2); len(ops) != 0 {
		t.Fatalf("full-width inner track changed: %+v", ops)
	}
}

func TestWidenStepBack(t *testing.T) {
	orig := specctra.Track{ID: "old", Net: "GND", Layer: 1, X1: 0, X2: 100, Width: 10.82}
	ops := []widenOp{{Track: orig, NewWidth: 21.65}}
	live := []specctra.Track{
		{ID: "w1", Net: "GND", Layer: 1, X1: 100, X2: 0, Width: 21.65}, // reversed ends: same track
		{ID: "other", Net: "GND", Layer: 1, X1: 200, X2: 300, Width: 21.65},
	}
	bad := map[string]bool{"w1": true, "other": true}
	half := widenStepBack(ops, live, bad, false)
	if len(half) != 1 || half[0].Delete.ID != "w1" || half[0].Create[0].Width != 16.23 {
		t.Fatalf("half step = %+v", half)
	}
	full := widenStepBack(ops, live, bad, true)
	if len(full) != 1 || full[0].Create[0].Width != 10.82 {
		t.Fatalf("final step = %+v", full)
	}
	if none := widenStepBack(ops, live, map[string]bool{"other": true}, false); len(none) != 0 {
		t.Fatalf("unwidened track stepped back: %+v", none)
	}
}

// A pad rotated 45°: its corner reaches further than an axis-aligned box of
// the same W×H, so the widen limit must use the rotated shape.
func TestPlanWidenRotatedPad(t *testing.T) {
	drv := specctra.Track{ID: "d", Net: "SV1_DRV", Layer: 1, X1: 0, Y1: 0, X2: 400, Y2: 0, Width: 10}
	pad := boardPad{Net: "GND", Layer: 1, X: 200, Y: 40, W: 20, H: 20, Rotation: 45}
	// Rotated corner sits 40-14.14 = 25.86 mil from the centre line: 2*(25.86-8) = 35.7.
	ops := planWiden([]specctra.Track{drv}, nil, []boardPad{pad}, map[string]bool{"SV1_DRV": true}, 40, 8)
	if len(ops) != 1 || ops[0].NewWidth > 35.73 || ops[0].NewWidth < 35.6 {
		t.Fatalf("ops = %+v", ops)
	}
}

// Gas Module V5 B v17: SV1_DRV dropped 57.25 mV of 30 on a 10 mil inner
// track; the IR closure widens it by the ratio (+15 %), only when the drop
// is the sole failure.
func TestIROverBudgetWiden(t *testing.T) {
	sum := map[string]any{"gates": []gateResult{
		{Gate: "native-drc", Pass: true},
		{Gate: "post-layout-sim", Items: []string{"SV1_DRV drops 57.25 mV at Q2.3 (typical) — over the 30.0 mV budget"}},
	}}
	r := irOverBudget(sum)
	if math.Abs(r["SV1_DRV"]-57.25/30) > 1e-9 {
		t.Fatalf("ratios %v", r)
	}
	ops := planWidenIR([]specctra.Track{{ID: "a", Net: "SV1_DRV", Layer: 15, X1: 0, Y1: 0, X2: 1000, Y2: 0, Width: 10}}, nil, nil, r, 40, 6.2)
	if len(ops) != 1 || math.Abs(ops[0].NewWidth-10*57.25/30*1.15) > 0.05 {
		t.Fatalf("ops %+v", ops)
	}
	sum["gates"] = append(sum["gates"].([]gateResult), gateResult{Gate: "native-drc", Items: []string{"x"}})
	if irOverBudget(sum) != nil {
		t.Fatal("DRC failure must not trigger IR widening")
	}
}

func TestSetNarrowPads(t *testing.T) {
	reqs := map[string]specctra.NetRequirement{"+3V3": {OuterMil: 15, MinMil: 15}, "GND": {OuterMil: 20, MinMil: 10}}
	setNarrowPads([]boardPad{{Net: "+3V3", W: 11, H: 70.9}, {Net: "+3V3", W: 35, H: 31}, {Net: "SIG", W: 5, H: 5}}, reqs)
	if reqs["+3V3"].NarrowPadMil != 11 || reqs["GND"].NarrowPadMil != 0 {
		t.Fatalf("%+v", reqs)
	}
}
