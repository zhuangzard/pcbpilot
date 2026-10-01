package schaes

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The synthetic pages are also committed as normalized snapshots so the CLI
// (`pcbpilot sch aesthetics --snapshot …`) can be run on them. Regenerate with
// SCHAES_WRITE_FIXTURES=1 go test ./pkg/schaes -run TestSyntheticFixturesCommitted
func TestSyntheticFixturesCommitted(t *testing.T) {
	mcu, pwr := loadSnap(t, esp32MCU), loadSnap(t, esp32PWR)
	gen := map[string]*Snapshot{
		"esp32-mcu-drafted.json":        drafted(mcu),
		"esp32-mcu-point-to-point.json": pointToPoint(mcu),
		"esp32-pwr-drafted.json":        drafted(pwr),
	}
	for name, s := range gen {
		path := filepath.Join("testdata", "synthetic", name)
		want, _ := json.MarshalIndent(s, "", " ")
		want = append(want, '\n')
		if os.Getenv("SCHAES_WRITE_FIXTURES") == "1" {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (regenerate with SCHAES_WRITE_FIXTURES=1)", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s drifted from its generator (regenerate with SCHAES_WRITE_FIXTURES=1)", path)
		}
		// round trip through Parse scores identically
		back, err := Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := Analyze(back, nil).Score, Analyze(s, nil).Score; a != b {
			t.Fatalf("%s: parsed fixture scores %.1f, generator %.1f", name, a, b)
		}
	}
}
