package pcbauto

import (
	"strings"
	"testing"
)

// An overlap the legaliser left, with room around it: resolve moves one
// part out, inside the region, and no courtyard overlaps afterwards.
func TestResolveOverlapsClearsWithinTheZone(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	// Start from a legal placement, then stack one part on another.
	pl.legalise()
	if n := len(pl.overlapPairs()); n != 0 {
		t.Skipf("fixture legalises to %d overlaps", n)
	}
	var a, b *Part
	for _, p := range pl.movable {
		if len(p.Pads) == 2 {
			if a == nil {
				a = p
			} else if b == nil && collide(a, p) {
				b = p
			}
		}
	}
	if a == nil || b == nil {
		t.Skip("fixture lacks two movable two-pad parts")
	}
	movePartCentre(a, b.Body().Center(), a.Rotation)
	pl.rebuildBuckets()
	if len(pl.overlapPairs()) == 0 {
		t.Fatal("setup: no overlap")
	}
	if n := pl.resolveOverlaps(); n == 0 {
		t.Fatal("resolve cleared nothing")
	}
	if ps := pl.overlapPairs(); len(ps) != 0 {
		t.Fatalf("overlaps left: %v", pl.illegalList(ps))
	}
	for _, p := range []*Part{a, b} {
		if outside(p.Body(), pl.region) > 1 {
			t.Fatalf("%s pushed out of the placement region", p.Ref)
		}
	}
}

// A zone smaller than its parts is reported over-full, by name.
func TestZoneFillReportsAnOverFullZone(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	c := pl.region.Center()
	tiny := Rect{c.X - 50, c.Y - 50, c.X + 50, c.Y + 50}
	for _, p := range pl.movable {
		pl.zoneOf[p] = tiny
	}
	res := &PlaceResult{Zones: []Zone{{Domain: "TINY", Rect: tiny}}}
	zs := pl.zoneFill(res, nil)
	if len(zs) != 1 || zs[0].Domain != "TINY" || zs[0].Fill <= 1 || zs[0].NeedMm2 <= zs[0].OfferMm2 {
		t.Fatalf("zone fill %+v, want TINY over-full", zs)
	}
	if !strings.Contains(strings.ToUpper(zs[0].Domain), "TINY") {
		t.Fatal("zone not named")
	}
}
