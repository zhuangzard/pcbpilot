package pcbauto

import (
	"context"
	"math"
	"testing"
)

// E2E 2026-09-25 (EasyEDA V3 3.2.149): native DRC failed "Slot Region to
// Track (3), Slot Region to Via (1)" at the USB-C J2's NPTH locating holes —
// MULTI-layer FILLs inside the footprint that `pcb dump` never exposed, so the
// router went straight through them. pcb dump now emits footprintHoles[];
// these tests pin that the engine models them (owned, moving, slot rule).
const fpHoleSnap = `{
 "outline": {"bbox": {"minX": 0, "minY": 0, "maxX": 1000, "maxY": 600}, "points": [[0,0],[1000,0],[1000,600],[0,600]], "source": "polygon"},
 "rules": {"clearanceMil": 5.98, "trackWidthMil": 10, "trackWidthMinMil": 5, "viaDrillMil": 12, "viaDiameterMil": 24, "copperToEdgeMil": 10},
 "copperLayers": 2,
 "components": [
  {"designator": "A1", "layer": 1, "x": 150, "y": 300, "rotation": 0, "locked": true,
   "pads": [{"padNumber": "1", "net": "SIG", "layer": 1, "x": 150, "y": 300, "width": 30, "height": 30}]},
  {"designator": "B1", "layer": 1, "x": 850, "y": 300, "rotation": 0, "locked": true,
   "pads": [{"padNumber": "1", "net": "SIG", "layer": 1, "x": 850, "y": 300, "width": 30, "height": 30}]},
  {"designator": "J2", "layer": 1, "x": 500, "y": 300, "rotation": 0, "locked": true,
   "pads": [{"padNumber": "S1", "net": "", "layer": 1, "x": 500, "y": 520, "width": 30, "height": 30}]}
 ],
 "footprintHoles": [
  {"owner": "J2", "sourceId": "e45", "shape": "circle", "x": 500, "y": 300, "dia": 29.53, "transform": "pad-verified"},
  {"owner": "J2", "sourceId": "e50", "shape": "polygon", "x": 650, "y": 300, "points": [[640,240],[660,240],[660,360],[640,360]], "transform": "pad-verified"}
 ]
}`

func TestSnapshotFootprintHolesAreOwnedSlotObstacles(t *testing.T) {
	b, err := FromSnapshot([]byte(fpHoleSnap))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Holes) != 2 {
		t.Fatalf("want 2 footprint holes, got %d", len(b.Holes))
	}
	h := b.Holes[0]
	if h.Owner != "J2" || h.Name != "J2:e45" || h.Clr != DefaultSlotClearance || h.Dia != 29.53 {
		t.Fatalf("hole not modelled as an owned slot obstacle: %+v", h)
	}
	if req := h.Required(b.Rules); math.Abs(req-11.811) > 1e-9 {
		t.Fatalf("slot rule %.3f, want 11.811 (native Slot Region spacing)", req)
	}
	// The hole is part of its footprint: it moves and turns with J2 …
	cl := b.Clone()
	j := b.Part("J2")
	j.MoveTo(Point{400, 300}, 90)
	if h.C.Dist(Point{400, 300}) > 1e-6 {
		t.Fatalf("circle did not follow J2: %+v", h.C)
	}
	slot := b.Holes[1]
	if want := (Point{400, 450}); slot.Bounds().Center().Dist(want) > 1e-6 {
		t.Fatalf("slot did not rotate with J2: centre %+v want %+v", slot.Bounds().Center(), want)
	}
	// … and a clone keeps its own copy.
	if cl.Holes[0].C != (Point{500, 300}) || cl.Holes[1].Poly[0] != (Point{640, 240}) {
		t.Fatalf("clone shares hole geometry with the original: %+v %+v", cl.Holes[0].C, cl.Holes[1].Poly)
	}
}

func TestRouteKeepsCopperOffFootprintNPTH(t *testing.T) {
	b, err := FromSnapshot([]byte(fpHoleSnap))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), b, Options{NoEscalate: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Route.Stats.Completion < 100 {
		t.Fatalf("SIG not routed: %.1f%%", res.Route.Stats.Completion)
	}
	for _, v := range res.DRC.Violations {
		if v.Kind == "hole" {
			t.Errorf("DRC hole violation: %+v", v)
		}
	}
	// Independent geometry, from the snapshot literal (not the model): every
	// track/via edge ≥ 11.811 mil from both footprint NPTH/slot edges (native
	// DRC's threshold, 0.1 mil tolerance).
	want := []*Hole{
		{Name: "J2:e45", C: Point{500, 300}, Dia: 29.53, Clr: 11.811},
		{Name: "J2:e50", Poly: []Point{{640, 240}, {660, 240}, {660, 360}, {640, 360}}, Clr: 11.811},
	}
	for _, h := range want {
		for _, tr := range res.Route.Tracks {
			if g := h.SegDist(tr.A, tr.B) - tr.Width/2; g < h.Clr-0.1 {
				t.Errorf("track %v–%v (%s L%d) %.2f mil from %s, need %.2f", tr.A, tr.B, tr.Net, tr.Layer, g, h.Name, h.Clr)
			}
		}
		for _, v := range res.Route.Vias {
			if g := h.Dist(v.C) - v.Dia/2; g < h.Clr-0.1 {
				t.Errorf("via %v %.2f mil from %s, need %.2f", v.C, g, h.Name, h.Clr)
			}
		}
	}
}

func TestStrictDRCSeesTrackThroughFootprintSlot(t *testing.T) {
	b, err := FromSnapshot([]byte(fpHoleSnap))
	if err != nil {
		t.Fatal(err)
	}
	straight := []Track{{Net: "SIG", Layer: 1, A: Point{150, 300}, B: Point{850, 300}, Width: 10, Kind: "route"}}
	n := 0
	for _, v := range CheckDRCStrict(b, nil, nil, straight, nil).Violations {
		if v.Kind == "hole" {
			n++
			if v.Gap >= 0 {
				t.Errorf("crossing copper must report a negative gap: %+v", v)
			}
		}
	}
	if n != 2 {
		t.Fatalf("want the circle and the slot flagged, got %d hole violations", n)
	}
}

// A footprint's own locating holes sit inside its body; they must not make
// the placer or the frame check think the part collides with itself.
func TestOwnFootprintHoleIsNotACollision(t *testing.T) {
	b, err := FromSnapshot([]byte(fpHoleSnap))
	if err != nil {
		t.Fatal(err)
	}
	j := b.Part("J2")
	j.SetBody(Rect{450, 250, 700, 550})
	if got := frameFault(b, &PlaceResult{}); got != "" {
		t.Fatalf("owner's own hole reported as a collision: %s", got)
	}
}
