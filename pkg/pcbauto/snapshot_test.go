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
