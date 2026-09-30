package pcbauto

// Golden baselines of the Phase A aesthetics analyser
// (docs/reviews/2026-09-routing-aesthetics/baseline.md). Report-only
// metrics: a change here must be deliberate — update baseline.md with it.

import (
	"math"
	"os"
	"testing"
)

type aesGolden struct {
	file                      string
	score, placement, routing float64
	symmetry                  int
	metrics                   map[string][2]float64 // id → {value, score}
}

var aesGoldens = []aesGolden{
	{"testdata/esp32-v05-fixed.routed.json", 43.222, 36.98, 49.463, 0, map[string][2]float64{"P1": {0.733, 0}, "P3": {0.625, 0}, "P5": {0.692, 75.1}, "P7": {0.875, 72.5}, "P9": {0.533, 37.3}, "R1": {0.182, 0}, "R2": {0.242, 24.2}, "R3": {0.698, 30.2}, "R4": {3.891, 100}, "R5": {0.712, 64.8}, "R7": {0, 0}, "R8": {1.853, 76.5}, "R9": {0, 100}}},
	{"testdata/esp32-v05-live.reload.json", 43.259, 36.98, 49.537, 0, map[string][2]float64{"P1": {0.733, 0}, "P3": {0.625, 0}, "P5": {0.692, 75.1}, "P7": {0.875, 72.5}, "P9": {0.533, 37.3}, "R1": {0.182, 0}, "R2": {0.247, 24.7}, "R3": {0.698, 30.2}, "R4": {3.891, 100}, "R5": {0.712, 64.8}, "R7": {0, 0}, "R8": {1.851, 76.6}, "R9": {0, 100}}},
	{"../../internal/app/testdata/boards/lckfb-mipi-3in1-adapter.json", 46.638, 46.638, 0, 0, map[string][2]float64{"P1": {0, 100}, "P2": {0.067, 64.1}, "P3": {1, 88.8}, "P5": {0.693, 67.4}, "P6": {0.299, 0.5}, "P7": {1.349, 25.1}, "P8": {0.62, 15.1}, "P9": {0.172, 12.1}}},
	{"../../internal/app/testdata/boards/bbclaw-ai-voice-terminal.json", 53.478, 53.478, 0, 1, map[string][2]float64{"P1": {0.303, 59.4}, "P2": {0, 100}, "P3": {0.75, 37.5}, "P4": {0.712, 0}, "P5": {0.604, 51.3}, "P6": {0.212, 40.5}, "P7": {1.08, 52}, "P8": {0.83, 75}, "P9": {0.889, 65.6}}},
	{"../../internal/app/testdata/boards/lckfb-szpi-esp32s3.json", 42.5, 42.5, 0, 7, map[string][2]float64{"P1": {0.171, 85.7}, "P2": {0.163, 0}, "P3": {0.562, 0}, "P4": {0.12, 77.8}, "P5": {0.695, 69.4}, "P6": {0.525, 0}, "P7": {0.673, 92.7}, "P8": {0.785, 56.9}, "P9": {0, 0}}},
	{"../../internal/app/testdata/boards/lckfb-rk3568-4layer.json", 39.033, 39.033, 0, 19, map[string][2]float64{"P1": {0.352, 49.6}, "P2": {0.153, 0}, "P3": {0.551, 0}, "P4": {0.228, 59.3}, "P5": {0.776, 83.2}, "P6": {0.28, 19.6}, "P7": {0.706, 89.4}, "P8": {0.694, 50}, "P9": {0.003, 0.2}}},
	{"../../internal/app/testdata/boards/lckfb-k230-canmv.json", 53.322, 53.322, 0, 13, map[string][2]float64{"P1": {0.089, 100}, "P2": {0.329, 0}, "P3": {0.685, 6.7}, "P4": {0.054, 92.2}, "P5": {0.751, 77.5}, "P6": {0.403, 6.2}, "P7": {0.528, 100}, "P8": {0.727, 46}, "P9": {0.721, 51.3}}},
}

func TestAesGoldenBaselines(t *testing.T) {
	near := func(a, b, tol float64) bool { return math.Abs(a-b) <= tol }
	for _, g := range aesGoldens {
		raw, err := os.ReadFile(g.file)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := AestheticsFromSnapshot(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !near(rep.Score, g.score, 0.01) || !near(rep.Placement, g.placement, 0.01) || !near(rep.Routing, g.routing, 0.01) {
			t.Errorf("%s: score %.3f/%.3f/%.3f, golden %.3f/%.3f/%.3f", g.file, rep.Score, rep.Placement, rep.Routing, g.score, g.placement, g.routing)
		}
		if len(rep.Symmetry) != g.symmetry {
			t.Errorf("%s: %d symmetry groups, golden %d", g.file, len(rep.Symmetry), g.symmetry)
		}
		seen := 0
		for _, m := range rep.Metrics {
			if m.Skipped {
				continue
			}
			seen++
			want, ok := g.metrics[m.ID]
			if !ok {
				t.Errorf("%s %s: measured (%.3f/%.1f) but golden has it skipped", g.file, m.ID, m.Value, m.Score)
				continue
			}
			if !near(m.Value, want[0], 0.0015) || !near(m.Score, want[1], 0.05) {
				t.Errorf("%s %s: value %.4f score %.1f, golden %.4f / %.1f", g.file, m.ID, m.Value, m.Score, want[0], want[1])
			}
		}
		if seen != len(g.metrics) {
			t.Errorf("%s: %d measured metrics, golden %d", g.file, seen, len(g.metrics))
		}
	}
}

// TestAesReviewCrossCheck pins the NoExemptions numbers of the ESP32 plan
// fixture against the 2026-09-28 review baseline (raw/scripts/aesmetrics.py).
// Differences are explained in baseline.md: the script read pad width/height
// (axis-aligned extents) as the pad-frame size, double-rotating 90°/270°
// pads, which moves a few vertices in or out of pads.
func TestAesReviewCrossCheck(t *testing.T) {
	raw, err := os.ReadFile("testdata/esp32-v05-fixed.routed.json")
	if err != nil {
		t.Fatal(err)
	}
	in, err := AesInputFromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	in.NoExemptions = true
	rep := Aesthetics(in)
	x := func(id, k string) float64 { return rep.Metric(id).Extra[k] }
	checks := []struct {
		what      string
		got, want float64
	}{
		{"R1 off-octilinear share incl. pad-internal (review 17.6 %)", x("R1", "shareInclPadInternal"), 0.177},
		{"R1 pad-end off-octilinear length ≥ pad edge (review 1823 mil / 48 stubs ≥20 mil)", x("R1", "lenMil"), 1822.8},
		{"R4 bends (review 55)", x("R4", "bends"), 55},
		{"R4 redundant collinear vertices (review 16)", x("R4", "collinear"), 16},
		{"R3 S-jogs (review 9; D3.6 vertex is inside the rotated pad)", x("R3", "count"), 8},
		{"R2 entries (review 114)", x("R2", "entries"), 115},
		{"R2 axial (review 41)", x("R2", "axial"), 41},
		{"R2 45° (review 25)", x("R2", "diag"), 26},
		{"R2 skew (review 48)", x("R2", "skew"), 48},
		{"R2 corner exits (review 26)", x("R2", "cornerExits"), 28},
		{"R5 TOP h share (review 30.8 %)", x("R5", "L1.prefShare"), 0.308},
		{"R5 BOTTOM v share (review 92.3 %)", x("R5", "L2.prefShare"), 0.923},
		{"R7 vias on 5 mil (review 0/69)", x("R7", "viasOn"), 0},
		{"R7 vias (review 69)", x("R7", "vias"), 69},
		{"R7 all track vertices (review 0/492)", x("R7", "allVert"), 492},
	}
	for _, c := range checks {
		if math.Abs(c.got-c.want) > 1e-6+0.0005*math.Abs(c.want) {
			t.Errorf("%s: got %v want %v", c.what, c.got, c.want)
		}
	}
}
