package pcbauto

import (
	"math"
	"testing"
)

// kicad place keeps the KiCad outline: a board drawn at y-down 0..120 mm is
// y-up −120..0 in the snapshot, and the mech frame must start at its
// lower-left corner (a field board, 2026-10-14: every part was written 120 mm above
// the board because the model outline moved to the origin).
func TestApplyMechKeepsOutlineFrame(t *testing.T) {
	spec, err := ParseMech([]byte(`{"units":"mm","board":{"width":76.2,"height":50.8},
		"zones":[{"domain":"A","rect":[0,0,10,50.8]}],
		"edge":[{"ref":"J1","edge":"left","at":25}]}`))
	if err != nil {
		t.Fatal(err)
	}
	b := isoBoard()
	// Shift the board (outline and parts) down by 2000 mil, like KiCad's y-down frame negated.
	const dy = -2000.0
	for i := range b.Outline {
		b.Outline[i].Y += dy
	}
	for _, p := range b.Parts {
		p.Pos.Y += dy
		for _, pd := range p.Pads {
			pd.Box.C.Y += dy
		}
	}
	if err := b.Index(); err != nil {
		t.Fatal(err)
	}
	before := b.Bounds()
	mc, err := ApplyMech(b, spec)
	if err != nil {
		t.Fatal(err)
	}
	after := b.Bounds()
	if math.Abs(after.MinX-before.MinX) > 0.01 || math.Abs(after.MinY-before.MinY) > 0.01 {
		t.Fatalf("outline moved: lower-left %v,%v → %v,%v", before.MinX, before.MinY, after.MinX, after.MinY)
	}
	if z := mc.Zones[0].Rect; math.Abs(z.MinY-before.MinY) > 0.01 {
		t.Fatalf("zone not anchored at the outline: %+v (outline minY %v)", z, before.MinY)
	}
	j1 := b.Part("J1").Body().Center()
	if j1.Y < after.MinY || j1.Y > after.MaxY {
		t.Fatalf("edge part J1 off the board: %+v, board %+v", j1, after)
	}
}
