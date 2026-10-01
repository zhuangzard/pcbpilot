package schaes

import (
	"math"
	"testing"
)

// Baselines (docs/reviews/2026-10-schematic-aesthetics/baseline.md). A change
// to any metric, threshold or profile moves these on purpose: update the table
// AND the baseline document together, with the reason in the commit.
func TestGoldenBaselines(t *testing.T) {
	mcu, pwr := loadSnap(t, esp32MCU), loadSnap(t, esp32PWR)
	fx := map[string]*Snapshot{
		"ams1117-lib-layout":       loadSnap(t, "testdata/ams1117-lib-layout.json"),
		"esp32-mcu-canonical":      mcu,
		"esp32-pwr-canonical":      pwr,
		"esp32-mcu-drafted":        drafted(mcu),
		"esp32-mcu-stub-label":     stubLabel(mcu),
		"esp32-mcu-point-to-point": pointToPoint(mcu),
		"esp32-pwr-drafted":        drafted(pwr),
		"esp32-pwr-stub-label":     stubLabel(pwr),
		"esp32-pwr-point-to-point": pointToPoint(pwr),
	}
	want := []struct {
		fixture, profile  string
		score, wired      float64
		measured, skipped int
	}{
		{"ams1117-lib-layout", "functional", 85.1, 1.000, 14, 4},
		{"ams1117-lib-layout", "balanced", 81.8, 1.000, 14, 4},
		{"ams1117-lib-layout", "precision", 79.8, 1.000, 14, 4},
		{"esp32-mcu-canonical", "functional", 82.0, 1.000, 3, 15},
		{"esp32-mcu-canonical", "balanced", 64.6, 1.000, 3, 15},
		{"esp32-mcu-canonical", "precision", 47.9, 1.000, 3, 15},
		{"esp32-pwr-canonical", "functional", 86.2, 1.000, 5, 13},
		{"esp32-pwr-canonical", "balanced", 67.1, 1.000, 4, 14},
		{"esp32-pwr-canonical", "precision", 47.6, 1.000, 4, 14},
		{"esp32-mcu-drafted", "functional", 77.9, 0.649, 15, 3},
		{"esp32-mcu-drafted", "balanced", 74.9, 0.649, 15, 3},
		{"esp32-mcu-drafted", "precision", 74.1, 0.649, 15, 3},
		{"esp32-mcu-stub-label", "functional", 57.6, 1.000, 16, 2},
		{"esp32-mcu-stub-label", "balanced", 59.1, 1.000, 16, 2},
		{"esp32-mcu-stub-label", "precision", 62.8, 1.000, 16, 2},
		{"esp32-mcu-point-to-point", "functional", 33.5, 1.000, 14, 4},
		{"esp32-mcu-point-to-point", "balanced", 34.5, 1.000, 14, 4},
		{"esp32-mcu-point-to-point", "precision", 36.4, 1.000, 14, 4},
		{"esp32-pwr-drafted", "functional", 64.7, 0.586, 17, 1},
		{"esp32-pwr-drafted", "balanced", 60.1, 0.586, 16, 2},
		{"esp32-pwr-drafted", "precision", 57.4, 0.586, 16, 2},
		{"esp32-pwr-stub-label", "functional", 57.1, 1.000, 17, 1},
		{"esp32-pwr-stub-label", "balanced", 56.1, 1.000, 16, 2},
		{"esp32-pwr-stub-label", "precision", 57.8, 1.000, 16, 2},
		{"esp32-pwr-point-to-point", "functional", 44.3, 0.966, 15, 3},
		{"esp32-pwr-point-to-point", "balanced", 44.4, 0.966, 14, 4},
		{"esp32-pwr-point-to-point", "precision", 46.3, 0.966, 14, 4},
	}
	for _, w := range want {
		p, _ := ProfileByName(w.profile)
		r := Analyze(fx[w.fixture], &p)
		if math.Abs(r.Score-w.score) > 0.05 || math.Abs(r.WiredShare-w.wired) > 0.0005 || r.Measured != w.measured || r.Skipped != w.skipped {
			t.Errorf("%s/%s: got score %.1f wired %.3f measured %d skipped %d; want %.1f %.3f %d %d",
				w.fixture, w.profile, r.Score, r.WiredShare, r.Measured, r.Skipped, w.score, w.wired, w.measured, w.skipped)
		}
	}
}
