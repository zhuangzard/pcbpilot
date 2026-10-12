package app

import (
	"math"
	"strings"
	"testing"
)

// stubQFN is a 12-pad two-row 0.4 mm part (pads 1-6 left column, 7-12 right),
// 7.874 x 34.449 mil pads lying horizontally, with a via rule.
func stubQFN(nets []string, extra ...pcbPadP) *escapeEnv {
	env := &escapeEnv{court: map[string]layoutBBox{}, netClr: map[string]float64{}, clr: 5.9, viaDia: 23.622, viaDrill: 11.8}
	for i, n := range nets {
		env.pads = append(env.pads, pcbPadP{Designator: "U1", Number: string(rune('A' + i)), Net: n, Layer: 1,
			X: 0, Y: float64(i) * 15.748, W: 34.449, H: 7.874})
	}
	// a second pad per net far away so every net has two pads
	for i, n := range nets {
		env.pads = append(env.pads, pcbPadP{Designator: "R1", Number: string(rune('a' + i)), Net: n, Layer: 1, X: 400, Y: float64(i) * 40, W: 20, H: 20})
	}
	env.pads = append(env.pads, extra...)
	return env
}

func TestEscapeStubsRoomyPadGetsViaClear(t *testing.T) {
	nets := make([]string, 12)
	for i := range nets {
		nets[i] = "N" + string(rune('A'+i))
	}
	env := stubQFN(nets)
	plan := env.planEscapeStubs(map[string]prebridgeNet{}, escapeStubParams{MaxViaDirs: 8, DefaultWidthMil: 7.874, DefaultMinMil: 5.9}, defaultChannelParams(env))
	if len(plan.Stubs) == 0 {
		t.Fatalf("no stub planned, skipped = %+v", plan.Skipped)
	}
	// every planned via clears every other net's pad and every other via
	for _, s := range plan.Stubs {
		for _, q := range env.pads {
			if q.Net == s.Net {
				continue
			}
			if d := rectPtDist(q.X-q.W/2, q.Y-q.H/2, q.X+q.W/2, q.Y+q.H/2, s.Via[0], s.Via[1]) - s.ViaDia/2; d < 5.9-1e-6 {
				t.Fatalf("%s via %.2f mil from %s.%s, clearance 5.9", s.Pad, d, q.Designator, q.Number)
			}
		}
		for _, o := range plan.Stubs {
			if o.Pad != s.Pad && math.Hypot(o.Via[0]-s.Via[0], o.Via[1]-s.Via[1]) < s.ViaDia {
				t.Fatalf("vias of %s and %s overlap", s.Pad, o.Pad)
			}
		}
		if s.WidthMil < 5.9 || s.WidthMil > 7.874+1e-6 {
			t.Fatalf("%s width %.2f", s.Pad, s.WidthMil)
		}
	}
	ops := escapeStubOps(plan)
	tr, vi := ops["addTracks"].([]map[string]any), ops["addVias"].([]map[string]any)
	if len(tr) != len(plan.Stubs) || len(vi) != len(plan.Stubs) || tr[0]["locked"] != true || vi[0]["locked"] != true {
		t.Fatalf("ops %+v", ops)
	}
}

func TestEscapeStubsSkipsWhenWalledIn(t *testing.T) {
	// one pad boxed in by foreign copper on every side: no via fits
	env := stubQFN([]string{"A", "B"})
	env.pads = env.pads[:0]
	env.pads = append(env.pads,
		pcbPadP{Designator: "U1", Number: "1", Net: "A", Layer: 1, X: 0, Y: 0, W: 34.449, H: 7.874},
		pcbPadP{Designator: "R1", Number: "1", Net: "A", Layer: 1, X: 400, Y: 0, W: 20, H: 20},
		pcbPadP{Designator: "X1", Number: "1", Net: "B", Layer: 1, X: 0, Y: 0, W: 400, H: 400})
	// 12 pads needed for a dense IC: pad the part
	for i := 0; i < 12; i++ {
		env.pads = append(env.pads, pcbPadP{Designator: "U1", Number: "p" + string(rune('a'+i)), Net: "", Layer: 1, X: float64(i) * 15.748, Y: 1000, W: 7.874, H: 34})
	}
	_, _, why := env.stubFor(env.pads[0], 0, 0, prebridgeNet{Width: 7.874, Min: 5.9}, escapeStubParams{ReachMil: 90}, defaultChannelParams(env))
	if why == "" || !strings.Contains(why, "no stub+via fits") {
		t.Fatalf("walled-in pad must be skipped with a reason, got %q", why)
	}
}

func TestEscapeStubsNecksButNotBelowMin(t *testing.T) {
	// a lone pad in the open keeps its full intent width
	env := stubQFN([]string{"A"})
	nets := map[string]prebridgeNet{"A": {Width: 15.75, Min: 10}}
	st, _, why := env.stubFor(env.pads[0], 0, 0, nets["A"], escapeStubParams{ReachMil: 90}, defaultChannelParams(env))
	if st == nil {
		t.Fatalf("open pad: %s", why)
	}
	if st.WidthMil != 15.75 || st.Necked {
		t.Fatalf("free pad keeps intent width, got %.2f necked=%v", st.WidthMil, st.Necked)
	}
}
