package app

import (
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// A teed cross-page label that a later wire crosses moves to the free side
// of its wire; one that is clear stays where it is.
func TestSbFixTeesMovesACrossedLabel(t *testing.T) {
	host := []kicad.Pt{{X: 0, Y: 0}, {X: 17.78, Y: 0}}
	tee := []kicad.Pt{{X: 15.24, Y: 0}, {X: 15.24, Y: -2.54}}
	above := []kicad.Pt{{X: 0, Y: -7.62}, {X: 17.78, Y: -7.62}} // drawn after the tee
	pins := map[kicad.Pt]string{{X: 0, Y: 0}: "SIG_ACROSS#U1.1", {X: 17.78, Y: 0}: "SIG_ACROSS#R1.1", {X: 0, Y: -7.62}: "OTHER#U1.2"}
	all := func(string) bool { return true }

	zl := &sbZoneLayout{
		Wires:   [][]kicad.Pt{host, tee, above},
		Markers: []sbMarker{{Kind: "label", Net: "SIG_ACROSS", At: tee[1], Dir: "up"}},
	}
	sbFixTees(zl, pins, nil, all)
	mk := zl.Markers[0]
	if mk.Dir != "down" || mk.At != (kicad.Pt{X: 15.24, Y: 2.54}) {
		t.Fatalf("label at %v %s, want teed down at (15.24, 2.54)", mk.At, mk.Dir)
	}
	if w := zl.Wires[1]; w[0] != (kicad.Pt{X: 15.24, Y: 0}) || w[1] != mk.At {
		t.Errorf("tee wire %v does not join the host to the label", w)
	}

	// nothing crosses it: untouched
	zl = &sbZoneLayout{
		Wires:   [][]kicad.Pt{host, tee},
		Markers: []sbMarker{{Kind: "label", Net: "SIG_ACROSS", At: tee[1], Dir: "up"}},
	}
	sbFixTees(zl, pins, nil, all)
	if mk := zl.Markers[0]; mk.Dir != "up" || mk.At != tee[1] {
		t.Errorf("a clear label moved to %v %s", mk.At, mk.Dir)
	}

	// both sides crossed: it stays (the drawing keeps its finding)
	below := []kicad.Pt{{X: 0, Y: 7.62}, {X: 17.78, Y: 7.62}}
	zl = &sbZoneLayout{
		Wires:   [][]kicad.Pt{host, tee, above, below},
		Markers: []sbMarker{{Kind: "label", Net: "SIG_ACROSS", At: tee[1], Dir: "up"}},
	}
	sbFixTees(zl, pins, nil, all)
	if mk := zl.Markers[0]; mk.Dir != "up" || mk.At != tee[1] {
		t.Errorf("a label with no free place moved to %v %s", mk.At, mk.Dir)
	}
}
