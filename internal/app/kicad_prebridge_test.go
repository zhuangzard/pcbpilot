package app

import (
	"strings"
	"testing"
)

// pbQFNRow is a 0.4 mm-pitch column-of-pads row: 7.874 x 34.449 mil pads on a
// 15.748 mil pitch (the PicoRick U303 top row).
func pbQFNRow(nets ...string) boardComp {
	c := boardComp{Designator: "U1", Layer: 1}
	for i, n := range nets {
		c.Pads = append(c.Pads, boardPad{Number: string(rune('1' + i)), Net: n, Layer: 1,
			X: 100 + float64(i)*15.748, Y: 0, W: 7.874, H: 34.449})
	}
	return c
}

func TestPlanPrebridgesAdjacentSameNet(t *testing.T) {
	s := &boardSnapshot{Components: []boardComp{pbQFNRow("A", "+3V3", "+3V3", "B", "+3V3")}}
	nets := map[string]prebridgeNet{"+3V3": {Width: 15.75, Min: 10, Clearance: 5.9}}
	plan := planPrebridges(s, nets, prebridgeParams{DefaultClearanceMil: 5.9, DefaultWidthMil: 7.874, DefaultMinMil: 5.9})
	if len(plan.Bridges) != 1 {
		t.Fatalf("bridges = %+v skipped = %+v, want exactly pads 2-3", plan.Bridges, plan.Skipped)
	}
	b := plan.Bridges[0]
	if b.PadA != "2" || b.PadB != "3" || b.StartY != 0 || b.EndY != 0 {
		t.Fatalf("bridge %+v", b)
	}
	// cap reach: pad half width 3.937 + room to the neighbour pad edge
	// (15.748 - 3.937 - w/2) must stay >= 5.9 → w <= 11.82
	if !b.Necked || b.WidthMil < 10 || b.WidthMil > 11.82 {
		t.Fatalf("width %.2f necked=%v, want 10..11.82 necked", b.WidthMil, b.Necked)
	}
	ops := prebridgeOps(plan)
	if tr := ops["addTracks"].([]map[string]any); len(tr) != 1 || tr[0]["locked"] != true {
		t.Fatalf("ops %+v", ops)
	}
}

func TestPlanPrebridgesSkips(t *testing.T) {
	nets := map[string]prebridgeNet{"N": {Width: 15.75, Min: 12, Clearance: 5.9}}
	s := &boardSnapshot{Components: []boardComp{pbQFNRow("A", "N", "N", "B")}}
	plan := planPrebridges(s, nets, prebridgeParams{DefaultClearanceMil: 5.9, DefaultWidthMil: 7.874, DefaultMinMil: 5.9})
	if len(plan.Bridges) != 0 || len(plan.Skipped) != 1 || !strings.Contains(plan.Skipped[0].Reason, "no width fits") {
		t.Fatalf("minimum 12 mil does not fit at 5.9 mil clearance: %+v / %+v", plan.Bridges, plan.Skipped)
	}
	// Coarse pitch (1.27 mm) is no fine row.
	c := pbQFNRow("N", "N")
	c.Pads[1].X = c.Pads[0].X + 50
	plan = planPrebridges(&boardSnapshot{Components: []boardComp{c}}, nets, prebridgeParams{})
	if len(plan.Bridges)+len(plan.Skipped) != 0 {
		t.Fatalf("coarse pitch must not be a candidate: %+v", plan)
	}
	// Non-adjacent same-net pads (another net between) are never bridged.
	plan = planPrebridges(&boardSnapshot{Components: []boardComp{pbQFNRow("N", "B", "N")}}, nets, prebridgeParams{})
	if len(plan.Bridges)+len(plan.Skipped) != 0 {
		t.Fatalf("pads 1 and 3 are not adjacent: %+v", plan)
	}
}

func TestPlanPrebridgesForeignTrackKeepsClearance(t *testing.T) {
	s := &boardSnapshot{Components: []boardComp{pbQFNRow("N", "N")}}
	nets := map[string]prebridgeNet{"N": {Width: 7.874, Min: 5.9, Clearance: 5.9}}
	// A foreign track 11 mil above the bridge centreline, parallel: only
	// width 2*(11-5.9) = 10.2 would fit, so the intent 7.874 stays.
	s.Copper = &boardCopperSnapshot{Lines: []any{map[string]any{"net": "X", "layer": 1, "startX": 90.0, "startY": 11.0, "endX": 140.0, "endY": 11.0, "lineWidth": 0.0}}}
	plan := planPrebridges(s, nets, prebridgeParams{})
	if len(plan.Bridges) != 1 || plan.Bridges[0].Necked {
		t.Fatalf("%+v", plan)
	}
	s.Copper.Lines[0].(map[string]any)["startY"], s.Copper.Lines[0].(map[string]any)["endY"] = 8.0, 8.0
	plan = planPrebridges(s, nets, prebridgeParams{})
	if len(plan.Bridges) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("track at 8 mil allows 4.2 mil < minimum 5.9 → skipped: %+v", plan)
	}
}
