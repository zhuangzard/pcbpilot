package pcbauto

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Calibration: every fixture board was built at its layer count, so its
// raw utilisation there (demand / (capacity/η)) bounds η from below. Logged
// for inspection; the assertion is that the chosen η never judges a built
// board "insufficient" at its own layer count.
func TestFeasibilityCalibration(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(fixtureDir, "*.json"))
	for _, f := range files {
		name := filepath.Base(f)
		if strings.Contains(name, ".expect.") || strings.Contains(name, ".spec.") {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			continue
		}
		b := loadFixture(t, name)
		an := Analyze(b, PowerSpec{}, nil)
		fe := AssessFeasibility(b, an, 8)
		var at *StackOption
		for i := range fe.Options {
			// The better of the stackups at the built layer count.
			if o := &fe.Options[i]; o.Layers == b.CopperLayers && (at == nil || verdictRank(o.Verdict) < verdictRank(at.Verdict)) {
				at = o
			}
		}
		if at == nil {
			continue
		}
		raw := at.Utilisation * fe.Efficiency
		t.Logf("%-34s built %d layers: raw %.3f → util %.2f (%s); stage-0 choice %d mixed %v; BGAs %d; placed %v top %.0f%% bot %.0f%%",
			name, b.CopperLayers, raw, at.Utilisation, at.Verdict, fe.Choice, fe.ChoiceMixed, len(fe.BGAs), fe.Placed, 100*fe.TopOccupancy, 100*fe.BotOccupancy)
		for _, g := range fe.BGAs {
			t.Logf("    BGA %s pitch %.1f ball %.1f signal %d rings %d top %d inner %d → %d signal layers, min %d layers: %s",
				g.Ref, g.PitchMil, g.BallMil, g.SignalBalls, g.EscapeRings, g.TopRings, g.InnerRings, g.SignalLayers, g.MinLayers, g.ViaTech)
		}
		if at.Verdict == "insufficient" {
			t.Errorf("%s was built on %d layers but stage 0 calls that insufficient (util %.2f)", name, b.CopperLayers, at.Utilisation)
		}
	}
}

func verdictRank(v string) int {
	return map[string]int{"ok": 0, "tight": 1, "insufficient": 2, "excluded": 3}[v]
}

// A board squeezed below what its circuit needs gets a quantified request to
// enlarge it, not a long failed routing run.
func TestFeasibilityNegotiatesArea(t *testing.T) {
	b := loadFixture(t, "lckfb-szpi-esp32s3.json")
	bb := EmptyRect()
	for _, p := range b.Outline {
		bb = bb.AddPoint(p)
	}
	c := bb.Center()
	var shrunk []Point
	for _, p := range b.Outline {
		shrunk = append(shrunk, c.Add(p.Sub(c).Scale(0.55)))
	}
	b.Outline = shrunk
	fe := AssessFeasibility(b, Analyze(b, PowerSpec{}, nil), 4)
	for _, n := range fe.Negotiation {
		t.Log(n)
	}
	if len(fe.Negotiation) == 0 || !strings.Contains(fe.Negotiation[0], "放大到") {
		t.Fatalf("a board at 55%% size must ask to be enlarged: %v", fe.Negotiation)
	}
}

// Calibration of the unplaced wirelength estimate: per connection,
// k·√area against the real placement's Manhattan MST.
func TestDonathCalibration(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(fixtureDir, "*.json"))
	for _, f := range files {
		name := filepath.Base(f)
		if strings.Contains(name, ".expect.") || strings.Contains(name, ".spec.") {
			continue
		}
		b := loadFixture(t, name)
		an := Analyze(b, PowerSpec{}, nil)
		area := b.Area()
		var mst float64
		conns := 0
		for _, n := range b.Nets() {
			if len(n.Pads) < 2 || an.Plan(n.Name, b.Rules).Role == RoleGround {
				continue
			}
			pts := make([]Point, len(n.Pads))
			for i, pd := range n.Pads {
				pts[i] = pd.Box.C
			}
			mst += mstLength(pts)
			conns += len(n.Pads) - 1
		}
		if conns == 0 || area == 0 {
			continue
		}
		k := mst / float64(conns) / math.Sqrt(area)
		t.Logf("%-34s conns %4d  mean MST/conn %6.0f mil  √area %5.0f  → k = %.3f", name, conns, mst/float64(conns), math.Sqrt(area), k)
	}
}
