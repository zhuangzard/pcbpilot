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
	sbFixJogs(zl, pinNet, nil)
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
	sbFixJogs(zl, pinNet, nil)
	if zl.Markers[0].JogLeft {
		t.Fatal("a free right side must keep the jog on the right")
	}
	// a ground stub going down needs no jog at all
	if _, ok := (sbMarker{Kind: "ground", Net: "GND", Dir: "down"}).sbJogs(); ok {
		t.Fatal("a ground stub arriving downwards does not jog")
	}
}

// Two jogged symbols side by side must not jog onto each other, and a
// symbol with no free side is reported, never drawn onto foreign copper.
func TestSbFixJogsSeesEarlierJogs(t *testing.T) {
	a, b := kicad.Pt{X: 0, Y: -5.08}, kicad.Pt{X: 5.08, Y: -5.08}
	pins := map[kicad.Pt]string{{X: 0, Y: 0}: "GND#J1.1", {X: 5.08, Y: 0}: "AGND#J1.3"}
	mk := func() *sbZoneLayout {
		return &sbZoneLayout{
			Wires: [][]kicad.Pt{{{X: 0, Y: 0}, a}, {{X: 5.08, Y: 0}, b}},
			Markers: []sbMarker{
				{Kind: "ground", Net: "GND", At: a, Dir: "up"},
				{Kind: "ground", Net: "AGND", At: b, Dir: "up"},
			},
		}
	}
	// nothing in the way: nothing to report, and the two symbols end on
	// different points
	zl := mk()
	bad := sbFixJogs(zl, pins, nil)
	da, _ := zl.Markers[0].sbJogs()
	db, _ := zl.Markers[1].sbJogs()
	if len(bad) != 0 || a.X+da == b.X+db {
		t.Fatalf("free sides: bad=%v markers=%+v", bad, zl.Markers)
	}
	// a wire right of b: b goes left only while a's jog does not end there
	zl = mk()
	zl.Wires = append(zl.Wires, []kicad.Pt{{X: 7.62, Y: -20}, {X: 7.62, Y: 20}})
	bad = sbFixJogs(zl, pins, nil)
	da, _ = zl.Markers[0].sbJogs()
	db, _ = zl.Markers[1].sbJogs()
	if len(bad) != 0 || db != -2.54 || a.X+da == b.X+db {
		t.Fatalf("wire on the right: bad=%v markers=%+v", bad, zl.Markers)
	}
	// wires on both sides of b: no side is free, and that is reported —
	// the symbol is never drawn onto foreign copper silently
	zl = mk()
	zl.Wires = append(zl.Wires, []kicad.Pt{{X: 7.62, Y: -20}, {X: 7.62, Y: 20}}, []kicad.Pt{{X: 2.54, Y: -20}, {X: 2.54, Y: 20}})
	bad = sbFixJogs(zl, pins, nil)
	if !zl.Markers[0].JogLeft {
		t.Fatal("the first symbol must leave the wire on its right alone")
	}
	if len(bad) != 1 || bad[0].A != b {
		t.Fatalf("the second symbol has no free side and must be reported: %v", bad)
	}
}

// Both sides are electrically free: the symbol goes where it covers no part
// body and no other marker.
func TestSbFixJogsPrefersTheVisuallyFreeSide(t *testing.T) {
	end := kicad.Pt{X: 0, Y: -5.08}
	zl := &sbZoneLayout{
		Wires:   [][]kicad.Pt{{{X: 0, Y: 0}, end}},
		Markers: []sbMarker{{Kind: "ground", Net: "GND", At: end, Dir: "up"}},
	}
	pins := map[kicad.Pt]string{{X: 0, Y: 0}: "GND#U1.4"}
	body := kicad.Box{MinX: 2, MinY: -6, MaxX: 12, MaxY: 4} // right of the stub
	if bad := sbFixJogs(zl, pins, []kicad.Box{body}); len(bad) != 0 || !zl.Markers[0].JogLeft {
		t.Fatalf("a body on the right must send the symbol left: bad=%v left=%v", bad, zl.Markers[0].JogLeft)
	}
	// bodies on both sides: the usual side
	other := kicad.Box{MinX: -12, MinY: -6, MaxX: -2, MaxY: 4}
	if sbFixJogs(zl, pins, []kicad.Box{body, other}); zl.Markers[0].JogLeft {
		t.Fatal("no side is visually better: keep right")
	}
	// another marker's text on the right
	zl.Markers = append(zl.Markers, sbMarker{Kind: "power", Net: "+3V3", At: kicad.Pt{X: 3.81, Y: 2.54}, Dir: "up"})
	if sbFixJogs(zl, pins, nil); !zl.Markers[0].JogLeft {
		t.Fatal("another marker on the right must send the symbol left")
	}
}
