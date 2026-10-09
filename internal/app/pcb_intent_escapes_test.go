package app

import (
	"math"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// qfnRow is the top row of a 0.4 mm QFN (PicoRick RP2040 U303 pads: 7.874 ×
// 34.45 mil, 15.75 mil pitch) plus one pad of the opposite row, so the
// part's pad centroid lies below the row; nets[i] is pad i+1's net.
func escQFNRow(nets ...string) []pcbPadP {
	var pads []pcbPadP
	for i, n := range nets {
		pads = append(pads, pcbPadP{Designator: "U1", Number: string(rune('1' + i)), Net: n, Layer: 1, X: float64(i) * 15.75, Y: 0, W: 7.874, H: 34.45})
	}
	pads = append(pads, pcbPadP{Designator: "U1", Number: "9", Net: "GND", Layer: 1, X: 31.5, Y: -270, W: 7.874, H: 34.45})
	return pads
}

func picoReqs(minMil float64) map[string]specctra.NetRequirement {
	return map[string]specctra.NetRequirement{"+3V3": {OuterMil: 15.75, InnerMil: 15.75, MinMil: minMil, ClearanceMil: 5.9}}
}

func TestPlanIntentEscapesFinePitch(t *testing.T) {
	env := &escapeEnv{pads: escQFNRow("SIG1", "SIG2", "+3V3", "SIG3", "SIG4"), clr: 5.9055 + 0.2, margin: 0.2,
		court: map[string]layoutBBox{"U1": {MinX: -20, MinY: -300, MaxX: 85, MaxY: 29}}}
	esc, skipped := planIntentEscapes(env, picoReqs(10), nil)
	if len(esc) != 1 || len(skipped) != 0 {
		t.Fatalf("escapes = %+v, skipped = %v", esc, skipped)
	}
	e := esc[0]
	// Widest width the 0.4 mm pitch allows: 2·(15.75 − 3.937 − 6.1055) = 11.41.
	if e.Pad != "U1.3" || e.WidthMil < 10 || e.WidthMil > 11.42 || math.Abs(e.WidthMil-11.41) > 0.02 {
		t.Fatalf("stub width %v (want ≈ 11.41, ≥ widthMil.min 10)", e)
	}
	// From inside the outer tip (w/2 back) straight out (+Y) to 1 mil past the
	// first point where a full-width trace end (a 15.75 mil square) clears
	// the neighbours: 17.225 + 6.1055 + 7.875 ≈ 31.2 mil from the centre.
	if e.Kind != "outward" || e.From[0] != 31.5 || math.Abs(e.From[1]-(17.225-e.WidthMil/2)) > 0.01 || e.To[0] != 31.5 {
		t.Fatalf("stub must leave the outer tip straight out (+Y): %+v", e)
	}
	if e.To[1] < 31.2 || e.To[1] > 33 || !e.FullFitsAtEnd || e.To[1]-17.225 > padEscapeReachMil(15.75) {
		t.Fatalf("stub end %v (full fits at end: %v)", e.To, e.FullFitsAtEnd)
	}
	// Clearance with the DRC's geometry: stub capsule vs neighbour pads.
	for _, q := range env.pads {
		if q.Net == "+3V3" {
			continue
		}
		d := rectSegDist(q.X-q.W/2, q.Y-q.H/2, q.X+q.W/2, q.Y+q.H/2, e.From[0], e.From[1], e.To[0], e.To[1]) - e.WidthMil/2
		if d < 5.9055+0.2-1e-6 {
			t.Fatalf("stub %.3f mil from %s.%s", d, q.Designator, q.Number)
		}
	}
	// The intent-widths gate accepts the stub as a pad escape.
	pads := []boardPad{{Number: "3", Net: "+3V3", Layer: 1, X: 31.5, Y: 0, W: 7.874, H: 34.45}}
	stub := specctra.Track{ID: "stub", Net: "+3V3", Layer: 1, X1: e.From[0], Y1: e.From[1], X2: e.To[0], Y2: e.To[1], Width: e.WidthMil}
	if vs := checkIntentWidths([]specctra.Track{stub}, pads, picoReqs(10), nil); len(vs) != 0 {
		t.Fatalf("gate rejected the escape stub: %+v", vs)
	}
}

func TestPlanIntentEscapesSkips(t *testing.T) {
	env := &escapeEnv{pads: escQFNRow("SIG1", "SIG2", "+3V3", "SIG3", "SIG4"), clr: 6.1055, margin: 0.2}
	// widthMil.min above what the pitch allows: no stub, reported.
	esc, skipped := planIntentEscapes(env, picoReqs(12), nil)
	if len(esc) != 0 || len(skipped) != 1 || !strings.Contains(skipped[0], "U1.3") {
		t.Fatalf("escapes = %+v, skipped = %v", esc, skipped)
	}
	// No neck-down allowed (min = outer): nothing planned.
	if esc, skipped := planIntentEscapes(env, picoReqs(15.75), nil); len(esc)+len(skipped) != 0 {
		t.Fatalf("min = outer planned %v %v", esc, skipped)
	}
	// Wide pitch (1.27 mm): the full width leaves the pad, no stub.
	wide := escQFNRow("SIG1", "+3V3", "SIG2")
	for i := range wide[:3] {
		wide[i].X = float64(i) * 50
	}
	if esc, _ := planIntentEscapes(&escapeEnv{pads: wide, clr: 6.1055}, picoReqs(10), nil); len(esc) != 0 {
		t.Fatalf("wide pitch got a stub: %+v", esc)
	}
	// A pad that already has an escape (ground pre-escape, --escapes) is left alone.
	seeded := escapeSeeded([]specctra.Escape{{Net: "+3V3", Path: [][2]float64{{31.5, 0}, {31.5, -40}}}})
	if esc, _ := planIntentEscapes(&escapeEnv{pads: escQFNRow("SIG1", "SIG2", "+3V3", "SIG3", "SIG4"), clr: 6.1055}, picoReqs(10), seeded); len(esc) != 0 {
		t.Fatalf("seeded pad escaped again: %+v", esc)
	}
}

// The pad-escape rule of intent-widths: a narrow segment (≥ widthMil.min)
// wholly within max(3 widths, 30 mil) of its own pad passes; one that starts
// at the pad but runs on past that zone, or lies away from every pad, fails.
func TestIntentWidthsPadEscapeZone(t *testing.T) {
	reqs := picoReqs(10)
	pads := []boardPad{{Number: "3", Net: "+3V3", Layer: 1, X: 0, Y: 0, W: 7.874, H: 34.45}}
	reach := padEscapeReachMil(15.75) // 47.25
	tracks := []specctra.Track{
		{ID: "in-zone", Net: "+3V3", Layer: 1, X1: 0, Y1: 0, X2: 0, Y2: 17.225 + reach - 1, Width: 11.41},
		{ID: "past-zone", Net: "+3V3", Layer: 1, X1: 0, Y1: 0, X2: 0, Y2: 17.225 + reach + 5, Width: 11.41},
		{ID: "away", Net: "+3V3", Layer: 1, X1: 300, Y1: 0, X2: 320, Y2: 0, Width: 11.41},
		{ID: "other-layer", Net: "+3V3", Layer: 2, X1: 0, Y1: 0, X2: 0, Y2: 30, Width: 11.41},
		{ID: "below-min", Net: "+3V3", Layer: 1, X1: 0, Y1: 0, X2: 0, Y2: 25, Width: 8},
	}
	got := map[string]string{}
	for _, v := range checkIntentWidths(tracks, pads, reqs, nil) {
		got[v.ID] = v.Reason
	}
	if _, bad := got["in-zone"]; bad || got["past-zone"] == "" || got["away"] == "" || got["other-layer"] == "" || !strings.Contains(got["below-min"], "minimum") {
		t.Fatalf("violations = %v", got)
	}
	if !strings.Contains(got["past-zone"], "pad-escape zone") {
		t.Fatalf("reason = %q", got["past-zone"])
	}
	// 30 mil floor for thin nets: 3 × 6 = 18 < 30.
	if padEscapeReachMil(6) != 30 {
		t.Fatal("reach floor")
	}
}

func TestPlacementHints(t *testing.T) {
	pads := escQFNRow("SIG1", "SIG2", "+3V3", "SIG3", "SIG4")
	// C5 sits 20 mil above U1.3's pad end, in its escape corridor.
	pads = append(pads,
		pcbPadP{Designator: "C5", Number: "1", Net: "SIG9", Layer: 1, X: 31.5, Y: 37.2 + 10, W: 20, H: 20},
		pcbPadP{Designator: "C5", Number: "2", Net: "GND", Layer: 1, X: 71.5, Y: 47.2, W: 20, H: 20},
		pcbPadP{Designator: "C7", Number: "1", Net: "+3V3", Layer: 1, X: 120, Y: 150, W: 20, H: 20},
		pcbPadP{Designator: "C7", Number: "2", Net: "GND", Layer: 1, X: 160, Y: 150, W: 20, H: 20})
	env := &escapeEnv{pads: pads, clr: 6.1055}
	hs := placementHints([]routeConn{{Net: "+3V3", From: "C7.1", To: "U1.3"}}, env, picoReqs(10))
	if len(hs) != 1 || hs[0].Pad != "U1.3" || hs[0].Move != "C5" || hs[0].Direction != "up (+Y)" || hs[0].ByMil <= 0 {
		t.Fatalf("hints = %+v", hs)
	}
	// Corridor free: the other end's part is brought to the escape exit.
	env.pads = pads[:len(pads)-4]
	env.pads = append(env.pads, pads[len(pads)-2:]...)
	hs = placementHints([]routeConn{{Net: "+3V3", From: "C7.1", To: "U1.3"}}, env, picoReqs(10))
	if len(hs) != 1 || hs[0].Move != "C7" || !strings.Contains(hs[0].Direction, "down") {
		t.Fatalf("hints = %+v", hs)
	}
	// A far other end: no single part move is invented.
	far := append(append([]pcbPadP{}, env.pads[:len(env.pads)-2]...),
		pcbPadP{Designator: "C9", Number: "1", Net: "+3V3", Layer: 1, X: 2000, Y: 2000, W: 20, H: 20},
		pcbPadP{Designator: "C9", Number: "2", Net: "GND", Layer: 1, X: 2040, Y: 2000, W: 20, H: 20})
	hs = placementHints([]routeConn{{Net: "+3V3", From: "C9.1", To: "U1.3"}}, &escapeEnv{pads: far, clr: 6.1055}, picoReqs(10))
	if len(hs) != 1 || hs[0].Move != "" || !strings.Contains(hs[0].Why, "far") {
		t.Fatalf("far hint = %+v", hs)
	}
	// Two pins of one part: nothing to move, said so.
	hs = placementHints([]routeConn{{Net: "+3V3", From: "U1.3", To: "U1.1"}}, env, picoReqs(10))
	if len(hs) != 1 || hs[0].Move != "" || !strings.Contains(hs[0].Why, "same-net") {
		t.Fatalf("hints = %+v", hs)
	}
	g := routeCompleteGate(&routeResult{Router: "fastroute", UnroutedCount: 1, Blocked: []routeConn{{Net: "+3V3", From: "C7.1", To: "U1.3"}}, PlacementHints: hs})
	if g.Pass || !strings.Contains(strings.Join(g.Items, "\n"), "placement hint: ") {
		t.Fatalf("route-complete = %+v", g)
	}
}

// RP2040 rows: +3V3 next to +1V1 at 0.4 mm cannot both leave outward at
// widthMil.min 10 (2·(15.75 − 6.1) = 19.3 < 20). A +3V3 pad with a +3V3 row
// neighbour bridges to it across the inner ends; an isolated one escapes
// inward to a via; the +1V1 pad keeps the outward escape.
func TestPlanIntentEscapesBridgeAndVia(t *testing.T) {
	reqs := map[string]specctra.NetRequirement{
		"+3V3": {OuterMil: 15.75, InnerMil: 15.75, MinMil: 10, ClearanceMil: 5.9},
		"+1V1": {OuterMil: 15.75, InnerMil: 15.75, MinMil: 10, ClearanceMil: 5.9},
	}
	// 43 44 45 46 of U303: +3V3 +3V3 +1V1 USB, then XOUT +3V3 +1V1 SWCLK.
	pads := escQFNRow("SIG0", "+3V3", "+3V3", "+1V1", "USB", "XOUT", "+3V3", "+1V1", "SWCLK")
	env := &escapeEnv{pads: pads, clr: 6.1055, margin: 0.2, viaDia: 23.62, viaDrill: 11.81}
	esc, skipped := planIntentEscapes(env, reqs, nil)
	kinds := map[string]intentEscape{}
	for _, e := range esc {
		kinds[e.Pad] = e
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped %v (planned %+v)", skipped, esc)
	}
	if kinds["U1.3"].Kind != "bridge" || kinds["U1.4"].Kind != "outward" || kinds["U1.8"].Kind != "outward" || kinds["U1.7"].Kind != "inward-via" {
		t.Fatalf("escapes = %+v", esc)
	}
	for _, e := range esc {
		if e.WidthMil+1e-9 < 10 {
			t.Fatalf("%s below widthMil.min: %+v", e.Pad, e)
		}
	}
	// The bridge joins the inner ends of U1.3 and U1.2; the via sits inward
	// (−Y), clear of every pad.
	b := kinds["U1.3"]
	if b.From[1] >= 0 || b.To[0] != 15.75 || b.From[0] != 31.5 {
		t.Fatalf("bridge %+v", b)
	}
	v := kinds["U1.7"]
	if !v.Via || v.To[1] >= -17.225 || v.ViaDiaMil != 23.62 {
		t.Fatalf("inward via %+v", v)
	}
	// The via keeps clearance from its own SMD pad too (via-at-SMD).
	if d := -17.225 - v.To[1] - 23.62/2; d < 6.1055-1e-6 || v.To[1]+17.225 < -padEscapeReachMil(15.75) {
		t.Fatalf("via %.3f mil from its own pad, %v", d, v.To)
	}
	// Every escape clears every other net's pad and escape (DRC geometry).
	for _, e := range esc {
		for _, q := range pads {
			if q.Net == e.Net {
				continue
			}
			if d := rectSegDist(q.X-q.W/2, q.Y-q.H/2, q.X+q.W/2, q.Y+q.H/2, e.From[0], e.From[1], e.To[0], e.To[1]) - e.WidthMil/2; d < 6.1055-1e-6 {
				t.Fatalf("%s %.3f mil from %s.%s", e.Pad, d, q.Designator, q.Number)
			}
		}
		for _, o := range esc {
			if o.Net == e.Net {
				continue
			}
			if d := segSegDist(e.From[0], e.From[1], e.To[0], e.To[1], o.From[0], o.From[1], o.To[0], o.To[1]) - e.WidthMil/2 - o.WidthMil/2; d < 6.1055-1e-6 {
				t.Fatalf("%s %.3f mil from escape %s", e.Pad, d, o.Pad)
			}
		}
	}
	// The gate accepts all of them as pad escapes.
	var bp []boardPad
	for _, q := range pads {
		bp = append(bp, boardPad{Number: q.Number, Net: q.Net, Layer: q.Layer, X: q.X, Y: q.Y, W: q.W, H: q.H})
	}
	var tracks []specctra.Track
	for _, e := range esc {
		tracks = append(tracks, specctra.Track{ID: e.Pad, Net: e.Net, Layer: 1, X1: e.From[0], Y1: e.From[1], X2: e.To[0], Y2: e.To[1], Width: e.WidthMil})
	}
	if vs := checkIntentWidths(tracks, bp, reqs, nil); len(vs) != 0 {
		t.Fatalf("gate rejected escapes: %+v", vs)
	}
}
