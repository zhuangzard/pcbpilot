package pcbauto

import (
	"os"
	"testing"
)

// The user-confirmed ESP32 mini layout: the USBLC6 (D3) sits with D- left of
// D+ while the CH340C wants D- below D+, so the pair crosses between them.
// Turning D3 by 180° about its centre untwists it.
func TestPairTwistESP32USB(t *testing.T) {
	raw, err := os.ReadFile("testdata/esp32-mini-layout.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	var dm *SignalChain
	for _, ch := range c.Chains {
		for _, n := range ch.Nets {
			if n == "USB_DM" {
				dm = ch
			}
		}
	}
	if dm == nil || dm.Pair == nil {
		t.Fatal("USB_DM chain with its pair not found")
	}
	if got := PairTwist(b, dm); got == 0 {
		t.Fatalf("confirmed layout: want the D3 twist detected, got 0")
	}
	d3 := b.Part("D3")
	movePartCentre(d3, d3.Body().Center(), d3.Rotation+180)
	if got := PairTwist(b, dm); got != 0 {
		t.Fatalf("D3 turned 180°: want 0 crossings, got %d", got)
	}
}

func TestDedupTracksReversed(t *testing.T) {
	ts := []Track{
		{Net: "USB_DP", Layer: 1, A: Point{886.89, 273.26}, B: Point{885.8, 268.3}, Width: 10.7},
		{Net: "USB_DP", Layer: 1, A: Point{885.8, 268.3}, B: Point{886.89, 273.26}, Width: 5},
		{Net: "USB_DP", Layer: 2, A: Point{885.8, 268.3}, B: Point{886.89, 273.26}, Width: 5},
	}
	out := dedupTracks(ts)
	if len(out) != 2 || out[0].Width != 10.7 {
		t.Fatalf("dedup = %+v", out)
	}
}

// The ESP32 board's U0RXD neck (5 mil, the process minimum) ran 0.05 mil
// inside the clearance to U3's NC pad; the host DRC flagged it. MicroFix
// shifts the vertex instead of narrowing below the minimum.
func TestMicroFixShiftsNeckVertex(t *testing.T) {
	b := &Board{Rules: DefaultRules()}
	b.Rules.Clearance = 5.98
	pad := &Pad{Part: "U3", Number: "35", Layer: 1, Box: OrientedBox{C: Point{0, 0}, W: 10, H: 10}}
	b.Parts = []*Part{{Ref: "U3", Pads: []*Pad{pad}}}
	pad.Part = "U3"
	// A 5 mil track whose edge sits 5.93 mil from the pad edge at a vertex.
	vx := Point{5 + 5.93 + 2.5, 0}
	rr := &RouteResult{Tracks: []Track{
		{Net: "SIG", Layer: 1, A: Point{vx.X, -40}, B: vx, Width: 5, Kind: "route"},
		{Net: "SIG", Layer: 1, A: vx, B: Point{vx.X + 30, 30}, Width: 5, Kind: "route"},
	}}
	if len(CheckDRCStrict(b, nil, nil, rr.Tracks, nil).Violations) == 0 {
		t.Fatal("setup: expected a strict violation")
	}
	if MicroFix(b, nil, nil, rr) == 0 {
		t.Fatal("micro-fix did nothing")
	}
	if v := CheckDRCStrict(b, nil, nil, rr.Tracks, nil).Violations; len(v) != 0 {
		t.Fatalf("still violating: %+v", v)
	}
	if rr.Tracks[0].B != rr.Tracks[1].A || rr.Tracks[0].Width != 5 {
		t.Fatalf("connectivity/width broken: %+v", rr.Tracks)
	}
}
