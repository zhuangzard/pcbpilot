package app

import (
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// Two pins 2.54 mm apart, both leaving upwards: the left one is ground, the
// right one a signal. The ground symbol is drawn upright beside its stub
// end; jogging right would end on the signal's wire and join the two nets.
func TestSbFixJogsAvoidsTheNeighbourWire(t *testing.T) {
	g, s := kicad.Pt{X: 0, Y: 0}, kicad.Pt{X: 2.54, Y: 0}
	gEnd := kicad.Pt{X: 0, Y: -5.08}
	zl := &sbZoneLayout{
		Wires: [][]kicad.Pt{
			{g, gEnd},
			{s, {X: 2.54, Y: -10.16}},
		},
		Markers: []sbMarker{
			{Kind: "ground", Net: "GND", At: gEnd, Dir: "up"},
			{Kind: "label", Net: "SIG", At: kicad.Pt{X: 2.54, Y: -10.16}, Dir: "up"},
		},
	}
	pinNet := map[kicad.Pt]string{g: "GND#RV1.4", s: "SIG#RV1.3"}
	sbFixJogs(zl, pinNet)
	if !zl.Markers[0].JogLeft {
		t.Fatal("the ground symbol jogged right onto the neighbouring signal wire")
	}
	if dx, ok := zl.Markers[0].sbJogs(); !ok || dx != -2.54 {
		t.Fatalf("jog = %v %v, want -2.54", dx, ok)
	}
	if b := sbMarkerBox(zl.Markers[0]); b.MaxX+b.MinX != 2*-2.54 {
		t.Fatalf("marker box %+v is not centred on the left jog", b)
	}
	// nothing in the way: the jog keeps its usual side
	zl.Wires = zl.Wires[:1]
	delete(pinNet, s)
	sbFixJogs(zl, pinNet)
	if zl.Markers[0].JogLeft {
		t.Fatal("a free right side must keep the jog on the right")
	}
	// a ground stub going down needs no jog at all
	if _, ok := (sbMarker{Kind: "ground", Net: "GND", Dir: "down"}).sbJogs(); ok {
		t.Fatal("a ground stub arriving downwards does not jog")
	}
}

// Two jogged symbols side by side must not jog onto each other.
func TestSbFixJogsSeesEarlierJogs(t *testing.T) {
	a, b := kicad.Pt{X: 0, Y: -5.08}, kicad.Pt{X: 5.08, Y: -5.08}
	zl := &sbZoneLayout{
		Wires: [][]kicad.Pt{{{X: 0, Y: 0}, a}, {{X: 5.08, Y: 0}, b}},
		Markers: []sbMarker{
			{Kind: "ground", Net: "GND", At: a, Dir: "up"},
			{Kind: "ground", Net: "AGND", At: b, Dir: "up"},
		},
	}
	// a wire right of b, so b would like to go left — where a's jog ends
	zl.Wires = append(zl.Wires, []kicad.Pt{{X: 7.62, Y: -20}, {X: 7.62, Y: 20}})
	sbFixJogs(zl, map[kicad.Pt]string{{X: 0, Y: 0}: "GND#J1.1", {X: 5.08, Y: 0}: "AGND#J1.3"})
	if zl.Markers[0].JogLeft {
		t.Fatal("first marker has a free right side")
	}
	if zl.Markers[1].JogLeft {
		t.Fatal("second marker jogged left onto the first marker's jog end")
	}
}
