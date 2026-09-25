package pcbauto

import (
	"math"
	"testing"
)

// F7 (ESP32 E2E 2026-09-25): a Ø24 route via 5.95 mil from the corner of a
// rotated rectangular pad of another net. The strict check must see it and
// MicroFix must move the via (tracks ending at it follow).
func TestMicroFixNudgesViaFromRotatedPad(t *testing.T) {
	b := &Board{Rules: DefaultRules()}
	b.Rules.Clearance = 5.98
	pad := &Pad{Part: "U1", Number: "36", Net: "RXD", Layer: 1, Box: OrientedBox{C: Point{1397.7, 1074.8}, W: 59.1, H: 35.4, Rot: 180}}
	b.Parts = []*Part{{Ref: "U1", Pads: []*Pad{pad}}}
	corner := Point{1397.7 + 59.1/2, 1074.8 + 35.4/2}
	dir := Point{1, 1}.Scale(1 / math.Sqrt2)
	c := corner.Add(dir.Scale(12.01 + 5.95))
	rr := &RouteResult{
		Vias:   []Via{{Net: "TXD", C: c, Drill: 12.01, Dia: 24.02, Kind: "route"}},
		Tracks: []Track{{Net: "TXD", Layer: 2, A: c, B: c.Add(Point{0, 60}), Width: 10, Kind: "route"}},
	}
	vs := CheckDRCStrict(b, nil, nil, rr.Tracks, rr.Vias).Violations
	if len(vs) != 1 || vs[0].Kind != "pad-via" {
		t.Fatalf("setup: want one pad-via violation, got %+v", vs)
	}
	if MicroFix(b, nil, nil, rr) == 0 {
		t.Fatal("micro-fix left the via")
	}
	if v := CheckDRCStrict(b, nil, nil, rr.Tracks, rr.Vias).Violations; len(v) != 0 {
		t.Fatalf("still violating: %+v", v)
	}
	if rr.Tracks[0].A != rr.Vias[0].C || rr.Vias[0].C.Dist(c) > 0.3 {
		t.Fatalf("via moved too far or track detached: via %+v track %+v", rr.Vias[0], rr.Tracks[0])
	}
}

// F6a: a min-width track grazes the long side of a tall pad; the violation's
// At is the track's start, far along the pad, so shifting away from the pad
// centre moved it mostly parallel to the edge and nothing was kept. The
// nudge must go away from the pad's nearest copper instead.
func TestMicroFixPadTrackAwayFromNearestEdge(t *testing.T) {
	b := &Board{Rules: DefaultRules()}
	b.Rules.Clearance = 5.98
	b.Rules.MinTrack = 5
	pad := &Pad{Part: "J2", Number: "A6", Net: "DP", Layer: 1, Box: OrientedBox{C: Point{0, 0}, W: 12, H: 80}}
	b.Parts = []*Part{{Ref: "J2", Pads: []*Pad{pad}}}
	x := 6 + 5.93 + 2.5
	rr := &RouteResult{Tracks: []Track{{Net: "DM", Layer: 1, A: Point{x, 35}, B: Point{x, 90}, Width: 5, Kind: "route"}}}
	if len(CheckDRCStrict(b, nil, nil, rr.Tracks, nil).Violations) != 1 {
		t.Fatal("setup: expected one strict violation")
	}
	if MicroFix(b, nil, nil, rr) == 0 {
		t.Fatal("micro-fix did nothing")
	}
	if v := CheckDRCStrict(b, nil, nil, rr.Tracks, nil).Violations; len(v) != 0 {
		t.Fatalf("still violating: %+v", v)
	}
	if rr.Tracks[0].Width != 5 {
		t.Fatalf("width changed: %+v", rr.Tracks[0])
	}
}

// F6: a mounting hole's grid band must hold the full clearance beyond the
// keep ring (a claim carries only half of it); an ESP32 +5V fan-out via sat
// 2.6 mil inside an M3 keep ring.
func TestMarkHoleKeepsFullClearance(t *testing.T) {
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 400, 400}.Corners()}
	b.Rules.Clearance = 5.98
	st := &Stackup{Stack: []StackLayer{{ID: 1, Kind: KindSignal}}}
	gr, err := newGrid(b, st, 2)
	if err != nil {
		t.Fatal(err)
	}
	h := &Hole{C: Point{200, 200}, Dia: 126, Keep: 20}
	gr.markHole(h, b.Rules)
	for y := 0; y < gr.H; y++ {
		for x := 0; x < gr.W; x++ {
			c := gr.center(x, y)
			d := c.Dist(h.C) - h.Dia/2 - h.Keep
			if gr.flags[gr.idx(0, x, y)]&flagHard == 0 && d < b.Rules.Clearance/2-1e-6 {
				t.Fatalf("cell %v is %.2f mil past the keep ring but not blocked (need %.2f)", c, d, b.Rules.Clearance/2)
			}
		}
	}
}

// F7 root cause: a route-only run must keep the measured poses — the
// playbook moves no part, so an edge snap in the model (U1 by 0.05 mil)
// routes against copper that is not on the board.
func TestApplyMechInPlaceKeepsPoses(t *testing.T) {
	spec, err := ParseMech([]byte(`{"units":"mm","board":{"width":76.2,"height":50.8},
		"edge":[{"ref":"J1","edge":"left","at":25}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b := isoBoard()
	before := savePose(b.Part("J1"))
	mc, err := ApplyMechInPlace(b, spec)
	if err != nil {
		t.Fatal(err)
	}
	j1 := b.Part("J1")
	if j1.Pos != before.pos || j1.Rotation != before.rot {
		t.Fatalf("J1 moved: %+v rot %v, was %+v rot %v", j1.Pos, j1.Rotation, before.pos, before.rot)
	}
	for i, pd := range j1.Pads {
		if pd.Box != before.pads[i] {
			t.Fatalf("pad %s moved: %+v, was %+v", pd.Key(), pd.Box, before.pads[i])
		}
	}
	if !mc.Fixed["J1"] || len(mc.Notes) == 0 {
		t.Fatalf("want J1 fixed and a note on the skipped move: %+v", mc)
	}
	b2 := isoBoard()
	if _, err := ApplyMech(b2, spec); err != nil {
		t.Fatal(err)
	}
	if b2.Part("J1").Pos == before.pos {
		t.Fatal("setup: ApplyMech should have moved J1")
	}
}
