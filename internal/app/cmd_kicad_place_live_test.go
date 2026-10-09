package app

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Live: needs KiCad (PCBPILOT_KICAD_LIVE=1). Runs `kicad place` on the
// synthetic internal/kicad/testdata/tiny.kicad_pcb and checks that what
// pcbnew wrote is what the placer modelled: every pad of every footprint
// (bottom side included) sits where the model's rigid move to the written
// pose puts it.
func TestKicadPlaceLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 (needs KiCad's python)")
	}
	dir := t.TempDir()
	src, err := os.ReadFile("../kicad/testdata/tiny.kicad_pcb")
	if err != nil {
		t.Fatal(err)
	}
	pcb := filepath.Join(dir, "tiny.kicad_pcb")
	if err := os.WriteFile(pcb, src, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out", "tiny.kicad_pcb")
	rep, err := runKicadPlace(kicadPlaceOpts{pcb: pcb, out: out, placeSeeds: 2, candidates: 2, seed: 1},
		kicadPlaceDeps{snapshot: kicad.Snapshot, place: kicad.Place}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range rep.Seeds {
		if s.File == out && (s.Write == nil || s.Write.RemovedTracks != 1) {
			t.Fatalf("best write %+v, want the fixture's one track removed", s.Write)
		}
	}
	raw0, err := kicad.Snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	raw1, err := kicad.Snapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	b0, _, err := kicadBoard(raw0)
	if err != nil {
		t.Fatal(err)
	}
	b1, err := pcbauto.FromSnapshot(raw1)
	if err != nil {
		t.Fatal(err)
	}
	moved := 0
	for _, p1 := range b1.Parts {
		p0 := b0.Part(p1.Ref)
		if p0 == nil || len(p0.Pads) != len(p1.Pads) || p0.Side != p1.Side {
			t.Fatalf("%s: part changed shape or side", p1.Ref)
		}
		if p0.Pos.Dist(p1.Pos) > 0.5 || p0.Rotation != p1.Rotation {
			moved++
		}
		if (p1.Ref == "J1" || p1.Ref == "H1") && p0.Pos.Dist(p1.Pos) > 0.01 {
			t.Fatalf("fixed %s moved", p1.Ref)
		}
		p0.MoveTo(p1.Pos, p1.Rotation)
		for i, pd := range p1.Pads {
			if d := p0.Pads[i].Box.C.Dist(pd.Box.C); d > 0.5 {
				t.Fatalf("%s pad %s: written %v, model %v (%.2f mil apart)", p1.Ref, pd.Number, pd.Box.C, p0.Pads[i].Box.C, d)
			}
		}
	}
	if moved == 0 {
		t.Fatal("no part moved")
	}
	if b1.Part("R3").Side != pcbauto.LayerBottom {
		t.Fatal("R3 left the bottom")
	}

	// A side change flips the footprint, then takes the asked pose.
	flipped := filepath.Join(dir, "flip.kicad_pcb")
	res, err := kicad.Place(pcb, map[string]kicad.Pose{"R1": {XMil: 600, YMil: 700, RotationDeg: 90, Side: "bottom"}}, flipped)
	if err != nil || res.Flipped != 1 {
		t.Fatalf("flip: %+v %v", res, err)
	}
	raw2, err := kicad.Snapshot(flipped)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := pcbauto.FromSnapshot(raw2)
	if err != nil {
		t.Fatal(err)
	}
	r1 := b2.Part("R1")
	if r1.Side != pcbauto.LayerBottom || math.Abs(r1.Pos.X-600) > 0.01 || math.Abs(r1.Pos.Y+700) > 0.01 || r1.Rotation != 90 {
		t.Fatalf("flipped R1: side %d pos %v rot %v", r1.Side, r1.Pos, r1.Rotation)
	}
	for _, pd := range r1.Pads {
		if pd.Layer != pcbauto.LayerBottom {
			t.Fatalf("flipped R1 pad %s on layer %d", pd.Number, pd.Layer)
		}
	}
}
