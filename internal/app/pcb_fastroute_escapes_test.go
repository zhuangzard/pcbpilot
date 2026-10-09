package app

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Left side of a TQFP (0.5 mm pitch, 59x11 mil pads) plus the opposite side,
// so the pad centroid lies to the right (+x) of the left column.
func tqfpPads() []pcbPadP {
	var pads []pcbPadP
	for i := 0; i < 5; i++ {
		y := 1505.9 + float64(i)*19.685
		net := "SIG" + string(rune('A'+i))
		if i == 2 {
			net = "GND"
		}
		pads = append(pads, pcbPadP{Designator: "U8", Number: string(rune('1' + i)), Net: net, Layer: 1, X: 1537.4, Y: y, W: 59, H: 11})
		pads = append(pads, pcbPadP{Designator: "U8", Number: string(rune('a' + i)), Net: "SIGR", Layer: 1, X: 2337.4, Y: y, W: 59, H: 11})
	}
	return pads
}

func TestPlanInwardEscapes(t *testing.T) {
	pads := tqfpPads()
	gnd := pads[4] // i == 2
	blocked := []frEndpoint{{Net: "GND", Mil: [2]float64{gnd.X, gnd.Y}, Layer: "TopLayer"}, {Net: "SIGA", Mil: [2]float64{1537.4, 1505.9}}}
	esc, skipped := planInwardEscapes(blocked, pads, nil, isGndNetName, 24, 6, 10)
	if len(esc) != 1 || len(skipped) != 0 {
		t.Fatalf("esc=%+v skipped=%v", esc, skipped)
	}
	e := esc[0]
	if e.Net != "GND" || e.Layer != "TopLayer" || !e.Via || e.WidthMil != 10 {
		t.Fatalf("escape = %+v", e)
	}
	// 1537.4 + 59/2 + 30 = 1596.9, same y, pointing into the body.
	if e.Path[0] != [2]float64{gnd.X, gnd.Y} || math.Abs(e.Path[1][0]-1596.9) > 1e-9 || e.Path[1][1] != gnd.Y {
		t.Fatalf("path = %v", e.Path)
	}

	// An other-net pad where the via lands: skipped, with the reason.
	blockedPads := append(tqfpPads(), pcbPadP{Designator: "C9", Number: "1", Net: "SIGX", Layer: 1, X: 1600, Y: gnd.Y, W: 20, H: 20})
	esc, skipped = planInwardEscapes(blocked[:1], blockedPads, nil, isGndNetName, 24, 6, 10)
	if len(esc) != 0 || len(skipped) != 1 {
		t.Fatalf("collision not detected: esc=%+v skipped=%v", esc, skipped)
	}

	// Coarse pitch (no fine-pitch neighbour): no escape.
	coarse := []pcbPadP{{Designator: "J1", Number: "1", Net: "GND", Layer: 1, X: 0, Y: 0, W: 60, H: 60}, {Designator: "J1", Number: "2", Net: "SIG", Layer: 1, X: 100, Y: 0, W: 60, H: 60}}
	if esc, _ := planInwardEscapes([]frEndpoint{{Net: "GND"}}, coarse, nil, isGndNetName, 24, 6, 10); len(esc) != 0 {
		t.Fatalf("coarse pitch escaped: %+v", esc)
	}
}

// Trimmed from the Gas Module V5 B round-5 report (fastroute 0.1.7).
func TestReadFastrouteBlocked(t *testing.T) {
	p := filepath.Join(t.TempDir(), "r.json")
	report := `{"stats":{"unrouted":2},"unrouted":[
 {"net":"GND","from":{"kind":"pin","layers":["TopLayer","TopLayer"]},"from_xy":[1.5374,-1.5453],
  "to":{"kind":"pin","layers":["TopLayer","TopLayer"]},"to_xy":[1.5374,-1.4075],"diagnosis":{"class":"blocked"}},
 {"net":"SIG","from":{"layers":["TopLayer"]},"from_xy":[1,-1],"to":{"layers":["TopLayer"]},"to_xy":[2,-2],"diagnosis":{"class":"congestion"}}]}`
	if err := os.WriteFile(p, []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFastrouteBlocked(p, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Net != "GND" || math.Abs(got[0].Mil[0]-1537.4) > 1e-9 || math.Abs(got[0].Mil[1]-1545.3) > 1e-9 || got[1].Mil[1] != 1407.5 {
		t.Fatalf("endpoints = %+v", got)
	}
}

// A 72-pin 0.5 mm part (4 sides × 18) with two adjacent GND pins on the left
// side: both are escaped before routing; the second via is staggered 40 mil
// deeper because the first one sits 19.7 mil away.
func qfp72(gndIdx ...int) []pcbPadP {
	var pads []pcbPadP
	n := 0
	add := func(x, y, w, h float64) {
		n++
		net := "S" + string(rune('A'+n%26)) + string(rune('a'+n/26))
		for _, g := range gndIdx {
			if n == g {
				net = "GND"
			}
		}
		pads = append(pads, pcbPadP{Designator: "U8", Number: string(rune(n + 32)), Net: net, Layer: 1, X: x, Y: y, W: w, H: h})
	}
	for i := 0; i < 18; i++ {
		y := 1000 + float64(i)*19.685
		add(1000, y, 59, 11)
	}
	for i := 0; i < 18; i++ {
		x := 1100 + float64(i)*19.685
		add(x, 1450, 11, 59)
	}
	for i := 0; i < 18; i++ {
		y := 1000 + float64(i)*19.685
		add(1500, y, 59, 11)
	}
	for i := 0; i < 18; i++ {
		x := 1100 + float64(i)*19.685
		add(x, 900, 11, 59)
	}
	return pads
}

func TestPlanPreEscapesStaggers(t *testing.T) {
	pads := qfp72(5, 6)
	esc, skipped := planPreEscapes(pads, nil, isGndNetName, 24, 6, 10)
	if len(esc) != 2 || len(skipped) != 0 {
		t.Fatalf("esc=%+v skipped=%v", esc, skipped)
	}
	d0 := esc[0].Path[1][0] - esc[0].Path[0][0]
	d1 := esc[1].Path[1][0] - esc[1].Path[0][0]
	if math.Abs(d0-59.5) > 1e-9 || math.Abs(d1-99.5) > 1e-9 {
		t.Fatalf("stub lengths %v %v, want 59.5 and 99.5 (staggered)", d0, d1)
	}
	// Escapes already in the DSN are respected: nothing is planned twice.
	again, _ := planPreEscapes(pads, esc, isGndNetName, 24, 6, 10)
	if len(again) != 0 {
		t.Fatalf("re-planned %+v", again)
	}
	// A small fine-pitch part (≤ 64 pins) gets no pre-escapes.
	if e, _ := planPreEscapes(tqfpPads(), nil, isGndNetName, 24, 6, 10); len(e) != 0 {
		t.Fatalf("small part pre-escaped: %+v", e)
	}
}
