package pcbauto

import (
	"encoding/json"
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
	if len(zs) != 1 || zs[0].Domain != "TINY" || !zs[0].OverFull || zs[0].Fill == nil || *zs[0].Fill <= 1 || zs[0].NeedMm2 <= zs[0].OfferMm2 {
		t.Fatalf("zone fill %+v, want TINY over-full", zs)
	}
	if !strings.Contains(strings.ToUpper(zs[0].Domain), "TINY") {
		t.Fatal("zone not named")
	}
}

// A zone a keep-out covers entirely offers nothing: over-full, fill null
// (never Inf in the JSON), and the note says so.
func TestZoneFillZeroOfferIsOverFull(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	c := pl.region.Center()
	z := Rect{c.X - 200, c.Y - 200, c.X + 200, c.Y + 200}
	for _, p := range pl.movable {
		pl.zoneOf[p] = z
	}
	pl.b.Keepouts = append(pl.b.Keepouts, &Keepout{Name: "cover", Poly: z.Expand(10).Corners(), NoParts: true})
	zs := pl.zoneFill(&PlaceResult{Zones: []Zone{{Domain: "COVERED", Rect: z}}}, nil)
	if len(zs) != 1 || !zs[0].OverFull || zs[0].Fill != nil || zs[0].OfferMm2 != 0 {
		t.Fatalf("zone fill %+v, want over-full with no fill", zs)
	}
	raw, err := json.Marshal(zs[0])
	if err != nil || !strings.Contains(string(raw), `"fill":null`) || !strings.Contains(string(raw), `"overFull":true`) {
		t.Fatalf("json %s (%v)", raw, err)
	}
}

// resolve may not trade one hard term for another: clearing a keep-out by
// leaving the zone, or closing one creepage shortfall by opening another.
func TestResolveCostNoTrades(t *testing.T) {
	a, b, c := &Pad{Number: "1"}, &Pad{Number: "2"}, &Pad{Number: "3"}
	before := resolveCost{keepouts: []float64{400}, gaps: map[resolveGap]float64{{a, b, false}: 5}}
	leaves := resolveCost{zoneBody: 50, keepouts: []float64{0}, gaps: map[resolveGap]float64{}}
	if leaves.noWorse(before) {
		t.Fatal("a keep-out cleared by leaving the zone was accepted")
	}
	swaps := resolveCost{keepouts: []float64{400}, gaps: map[resolveGap]float64{{a, c, false}: 1}}
	if swaps.noWorse(before) {
		t.Fatal("a new shortfall against another pad was accepted for closing an old one")
	}
	hv := resolveCost{keepouts: []float64{400}, gaps: map[resolveGap]float64{{a, b, true}: 1}}
	if hv.noWorse(before) {
		t.Fatal("an HV shortfall was accepted in place of a creepage one")
	}
	better := resolveCost{keepouts: []float64{100}, gaps: map[resolveGap]float64{{a, b, false}: 2}}
	if !better.noWorse(before) {
		t.Fatal("a pose better on every term was rejected")
	}
}

// illegalList names a part outside the board and one outside its zone
// even with no courtyard overlap anywhere.
func TestIllegalListNamesBoardAndZoneExits(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	pl.legalise()
	if ps := pl.overlapPairs(); len(ps) != 0 {
		t.Skipf("fixture legalises to %d overlaps", len(ps))
	}
	if len(pl.movable) < 2 || len(pl.b.Outline) < 3 {
		t.Skip("fixture lacks movable parts or an outline")
	}
	out, zoned := pl.movable[0], pl.movable[1]
	bb := pl.b.Bounds()
	movePartCentre(out, Point{bb.MaxX + 2000, bb.MaxY + 2000}, out.Rotation)
	pl.zoneOf[zoned] = Rect{bb.MaxX + 5000, bb.MaxY + 5000, bb.MaxX + 5100, bb.MaxY + 5100}
	list := strings.Join(pl.illegalList(pl.overlapPairs()), ";")
	if !strings.Contains(list, "outside the board "+out.Ref) || !strings.Contains(list, "outside its zone "+zoned.Ref) {
		t.Fatalf("illegal %q, want %s off the board and %s off its zone", list, out.Ref, zoned.Ref)
	}
}
