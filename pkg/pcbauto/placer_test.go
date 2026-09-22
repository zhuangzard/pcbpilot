package pcbauto

import (
	"context"
	"math/rand"
	"os"
	"testing"
	"time"
)

// scramble moves every unlocked part to a random spot/rotation, like a fresh
// schematic import, keeping the outline.
func scramble(b *Board, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	bb := b.Bounds()
	for _, p := range b.Parts {
		if p.Fixed {
			continue
		}
		c := Point{bb.MinX + rng.Float64()*bb.W(), bb.MinY + rng.Float64()*bb.H()}
		movePartCentre(p, c, float64(90*rng.Intn(4)))
	}
}

func TestPlaceIsolationZones(t *testing.T) {
	b := isoBoard()
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	spec, err := ParseMech([]byte(`{"units":"mm","board":{"width":76.2,"height":50.8},
		"edge":[{"ref":"J1","edge":"left","at":25}]}`))
	if err != nil {
		t.Fatal(err)
	}
	mech, err := ApplyMech(b, spec)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Place(b, an, c, mech, PlaceOptions{Seed: 1, Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("metrics %+v notes %v", res.Metrics, res.Notes)
	if res.Metrics.Overlaps != 0 || res.Metrics.OutOfBoard != 0 || res.Metrics.OutOfZone != 0 {
		t.Fatalf("placement violations: %+v", res.Metrics)
	}
	if len(res.Barriers) != 1 {
		t.Fatalf("want one isolation strip, got %d", len(res.Barriers))
	}
	strip := PolyBounds(res.Barriers[0].Poly)
	// Every mains part left of the strip, every SELV part right of it,
	// the optocoupler straddling it.
	for _, ref := range []string{"F1", "R1"} {
		if b.Part(ref).Body().MaxX > strip.MinX {
			t.Errorf("%s (mains) crosses into the strip: %+v vs %+v", ref, b.Part(ref).Body(), strip)
		}
	}
	for _, ref := range []string{"U2", "R2", "C1"} {
		if b.Part(ref).Body().MinX < strip.MaxX {
			t.Errorf("%s (SELV) crosses into the strip", ref)
		}
	}
	u1 := b.Part("U1").Body()
	if !(u1.MinX < strip.MinX && u1.MaxX > strip.MaxX) && !(u1.Center().X > strip.MinX && u1.Center().X < strip.MaxX) {
		t.Errorf("optocoupler does not straddle the strip: %+v vs %+v", u1, strip)
	}
	if strip.W() < 190 {
		t.Errorf("strip %.0f mil narrower than 5 mm reinforced creepage", strip.W())
	}
	// J1 must sit on the left edge.
	if j := b.Part("J1").Body(); j.MinX > b.Bounds().MinX+1 {
		t.Errorf("J1 not flush with the left edge: %+v", j)
	}
}

// TestPlaceAndRouteScrambled is the end-to-end check: a real board with its
// placement destroyed is placed and routed by the engine.
func TestPlaceAndRouteScrambled(t *testing.T) {
	if testing.Short() {
		t.Skip("long")
	}
	name := "lckfb-mipi-3in1-adapter.json"
	if v := os.Getenv("PCBAUTO_BOARD"); v != "" {
		name = v
	}
	human := loadFixture(t, name)
	anH := Analyze(human, PowerSpec{}, nil)
	cH := Understand(human, anH)
	humanWL := (&placer{b: human, an: anH, c: cH, partNet: map[*Part][]int{}, zoneOf: map[*Part]Rect{}, decap: map[*Part]*Pad{},
		m: &Mechanics{}, opt: PlaceOptions{SpacingMil: 12}}).weightedWL()

	b := loadFixture(t, name)
	// Connectors stay where the enclosure put them (mechanical contract).
	for _, p := range b.Parts {
		p.Fixed = ClassifyPart(p) == KindConnector
	}
	scramble(b, 7)
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	res, err := Place(b, an, c, nil, PlaceOptions{Seed: 1, Timeout: 60 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("placement %+v", res.Metrics)
	t.Logf("human weighted wirelength %.1f in, engine %.1f in", humanWL/1000, res.Metrics.WirelengthIn)
	if res.Metrics.Overlaps > 0 || res.Metrics.OutOfBoard > 0 {
		t.Errorf("illegal placement: %+v", res.Metrics)
	}
	out, err := Run(context.Background(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, Route: RouteOptions{Timeout: 3 * time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("route %+v attempts %+v drc %v", out.Route.Stats, out.Attempts, out.DRC.ByKind)
}

// weightedWL is the placer's net-length metric for an existing placement.
func (pl *placer) weightedWL() float64 {
	pl.setup(&PlaceResult{})
	return pl.wirelength()
}
