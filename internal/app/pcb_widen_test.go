package app

import (
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
