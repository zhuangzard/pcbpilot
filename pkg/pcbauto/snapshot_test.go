package pcbauto

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExportPlacedSnapshotRoundTrip moves and turns parts, exports the
// snapshot, re-reads it and requires every pad to land where the engine put it.
func TestExportPlacedSnapshotRoundTrip(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir, "lckfb-mipi-3in1-adapter.json"))
	if err != nil {
		t.Skip(err)
	}
	b, err := FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range b.Parts {
		p.MoveTo(p.Pos.Add(Point{float64(i * 7), -float64(i * 3)}), p.Rotation+float64(90*(i%4)))
	}
	out, err := ExportPlacedSnapshot(raw, b)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := FromSnapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range b.Parts {
		q := b2.Part(p.Ref)
		if q == nil || len(q.Pads) != len(p.Pads) {
			t.Fatalf("%s lost in round trip", p.Ref)
		}
		if q.Pos.Dist(p.Pos) > 0.02 || normDeg(q.Rotation-p.Rotation) > 1e-6 {
			t.Fatalf("%s pose %v/%v != %v/%v", p.Ref, q.Pos, q.Rotation, p.Pos, p.Rotation)
		}
		for i := range p.Pads {
			if d := q.Pads[i].Box.C.Dist(p.Pads[i].Box.C); d > 0.02 {
				t.Fatalf("%s pad %s moved by %.3f mil in round trip", p.Ref, p.Pads[i].Number, d)
			}
		}
	}
}

// Snapshot rules multiplied by 39.37 once too often are recovered, not
// replaced by defaults: the BGA boards' 4 mil clearance and 10 mil vias
// decide whether dog-bone fan-out fits at 0.65 mm pitch.
func TestRulesRecoverDoubleConverted(t *testing.T) {
	r := Rules{Clearance: 157.48, TrackWidth: 185.04, MinTrack: 185.04, ViaDrill: 236.22, ViaDia: 393.7,
		EdgeClearance: 236.22, CopperOz: 1, InnerCopperOz: 0.5, BoardThickMil: 62.99}
	r.sanitize()
	near := func(a, b float64) bool { return a-b < 0.05 && b-a < 0.05 }
	if !near(r.Clearance, 4) || !near(r.TrackWidth, 4.7) || !near(r.ViaDrill, 6) || !near(r.ViaDia, 10) {
		t.Fatalf("not recovered: clear %.3f track %.3f drill %.3f dia %.3f", r.Clearance, r.TrackWidth, r.ViaDrill, r.ViaDia)
	}
}
