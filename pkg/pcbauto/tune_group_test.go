package pcbauto

import (
	"context"
	"testing"
	"time"
)

// groupBoard: U1 drives two HDMI-style pairs to U2 (near) and U3 (far).
// Routed straight, pair B is ~400 mil longer than pair A; they share the
// length group G (tolerance 50 mil), so tuning must lengthen pair A by the
// same amount on both members, keeping its intra-pair skew.
func groupBoard(t *testing.T) (*Board, *Analysis, *Stackup) {
	t.Helper()
	b := &Board{Rules: DefaultRules(), CopperLayers: 4, Outline: Rect{0, 0, 2400, 1400}.Corners()}
	pad := func(ref, num, net string, x, y float64) *Pad {
		return &Pad{Part: ref, Number: num, Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{x, y}, W: 24, H: 14}}
	}
	u1 := &Part{Ref: "U1", Device: "HDMI-TX", Pos: Point{300, 700}, Side: LayerTop}
	for i, n := range []string{"A_P", "A_N", "B_P", "B_N"} {
		u1.Pads = append(u1.Pads, pad("U1", string(rune('1'+i)), n, 320, 760-float64(i)*40))
	}
	u1.Pads = append(u1.Pads, pad("U1", "9", "GND", 280, 700))
	u2 := &Part{Ref: "U2", Device: "CONN-A", Pos: Point{1300, 1000}, Side: LayerTop}
	u2.Pads = append(u2.Pads, pad("U2", "1", "A_P", 1300, 1020), pad("U2", "2", "A_N", 1300, 980), pad("U2", "3", "GND", 1340, 1000))
	u3 := &Part{Ref: "U3", Device: "CONN-B", Pos: Point{1700, 400}, Side: LayerTop}
	u3.Pads = append(u3.Pads, pad("U3", "1", "B_P", 1700, 420), pad("U3", "2", "B_N", 1700, 380), pad("U3", "3", "GND", 1740, 400))
	b.Parts = []*Part{u1, u2, u3}
	if err := b.Index(); err != nil {
		t.Fatal(err)
	}
	spec := PowerSpec{DiffPairs: [][2]string{{"A_P", "A_N"}, {"B_P", "B_N"}}}
	st := DecideStackup(b, Analyze(b, spec, nil), StackOptions{Force: 4})
	an := Analyze(b, spec, st)
	for _, n := range []string{"A_P", "A_N", "B_P", "B_N"} {
		np := an.ByNet[n]
		np.Interface, np.LengthGroup, np.LengthTolMil = "HDMI", "G", 50
	}
	return b, an, st
}

func TestTuneLengthGroupPairsTogether(t *testing.T) {
	b, an, st := groupBoard(t)
	rr, err := Route(context.Background(), b, st, an, RouteOptions{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if rr.Stats.Completion < 100 {
		t.Fatalf("completion %.1f%%: %+v", rr.Stats.Completion, rr.Unrouted)
	}
	si := CheckSI(b, an, st, rr)
	if len(si.Groups) != 1 {
		t.Fatalf("groups %+v", si.Groups)
	}
	g := si.Groups[0]
	for _, n := range rr.Notes {
		t.Log(n)
	}
	if g.SpreadMil > 50 {
		t.Fatalf("group spread %.0f mil > 50 after tuning (%.0f…%.0f)", g.SpreadMil, g.MinMil, g.MaxMil)
	}
	for _, p := range si.Pairs {
		if p.SkewMil > p.LimitMil {
			t.Fatalf("pair %s/%s skew %.0f > %.0f after group tuning", p.P, p.N, p.SkewMil, p.LimitMil)
		}
	}
}
