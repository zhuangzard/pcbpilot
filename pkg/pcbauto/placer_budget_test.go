package pcbauto

import (
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// A deadline already past: the placement still returns every part posed,
// skips the aesthetics stage and names the first phase it cut short.
func TestPlaceDeadlineFlagsBestSoFar(t *testing.T) {
	b := loadFixture(t, "reference-4stage-compact.json")
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	var phases []string
	r, err := Place(b, an, c, nil, PlaceOptions{Seed: 1, Moves: 200, Deadline: time.Now().Add(-time.Second),
		Progress: func(_ int64, ph string) { phases = append(phases, ph) }})
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.TimeBudgetHit != "legalise" {
		t.Fatalf("timeBudgetHit %q, want legalise (phases %v)", r.Metrics.TimeBudgetHit, phases)
	}
	if r.Aesthetics != nil {
		t.Fatal("aesthetics ran past the deadline")
	}
	if len(r.Placements) != len(b.Parts) {
		t.Fatalf("%d placements for %d parts", len(r.Placements), len(b.Parts))
	}
	note := strings.Join(r.Notes, "\n")
	if !strings.Contains(note, "time budget reached during legalise") || !strings.Contains(note, "aesthetics stage skipped") {
		t.Fatalf("notes:\n%s", note)
	}
	want := []string{"setup", "construct", "legalise", "anneal", "legalise-2", "polish", "rescue", "tidy", "resolve", "aesthetics", "metrics", "done"}
	if strings.Join(phases, ",") != strings.Join(want, ",") {
		t.Fatalf("phases %v, want %v", phases, want)
	}
}

// No deadline, the default: nothing is flagged.
func TestPlaceNoDeadlineNoFlag(t *testing.T) {
	b := loadFixture(t, "degraded-4stage-compact.json")
	an := Analyze(b, PowerSpec{}, nil)
	r, err := Place(b, an, Understand(b, an), nil, PlaceOptions{Seed: 1, Moves: 200})
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.TimeBudgetHit != "" {
		t.Fatalf("timeBudgetHit %q without a deadline", r.Metrics.TimeBudgetHit)
	}
}

// Legalisation that outlasts Timeout used to leave the anneal with a
// deadline in the past, which disabled its clock: it ran every move. The
// budget shortening the anneal is reported only once the anneal stops on it.
func TestAnnealDeadlineAfterSlowLegalise(t *testing.T) {
	pl := &placer{opt: PlaceOptions{Timeout: 90 * time.Second}}
	now := time.Now()
	if d, short := pl.annealDeadline(now); d.Sub(now) < 89*time.Second || d.Sub(now) > 91*time.Second || short {
		t.Fatalf("fresh start: anneal deadline %s after start (short %v), want Timeout", d.Sub(now), short)
	}
	if d, _ := pl.annealDeadline(now.Add(-10 * time.Minute)); d.Sub(now) < 29*time.Second || d.Sub(now) > 31*time.Second {
		t.Fatalf("slow legalise: anneal deadline %s from now, want Timeout/3", d.Sub(now))
	}
	pl.opt.Deadline = now.Add(20 * time.Second)
	if d, short := pl.annealDeadline(now); d.Sub(now) > 16*time.Second || !short {
		t.Fatalf("budget: anneal deadline %s from now (short %v), want 3/4 of the remaining 20 s", d.Sub(now), short)
	}
	if pl.budgetHit != "" {
		t.Fatalf("budgetHit %q set by planning the deadline alone", pl.budgetHit)
	}
}

// A budget that shortens the anneal's deadline but is never reached (the
// moves end first) flags nothing.
func TestPlaceShortBudgetThatFinishes(t *testing.T) {
	b := loadFixture(t, "reference-4stage-compact.json")
	an := Analyze(b, PowerSpec{}, nil)
	r, err := Place(b, an, Understand(b, an), nil, PlaceOptions{Seed: 1, Moves: 200, Deadline: time.Now().Add(60 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.TimeBudgetHit != "" {
		t.Fatalf("timeBudgetHit %q for a run that finished in %d ms", r.Metrics.TimeBudgetHit, r.Metrics.Millis)
	}
}

// The spiral's reach is a circle: a zone diagonally inside it keeps the
// spiral at the part (the old origin), one beyond it moves the origin.
func TestSpiralCentreEuclidean(t *testing.T) {
	const step = 10.0
	z := Rect{0, 0, 100, 100}
	// 2500 mil off in x and y: Manhattan 5000 > 4000, Euclidean 3536 < 4000.
	if c := spiralCentre(Point{2600, 2600}, z, step); c != (Point{2600, 2600}) {
		t.Fatalf("diagonal part inside the reach recentred to %v", c)
	}
	if c := spiralCentre(Point{4200, 50}, z, step); c != (Point{100, 50}) {
		t.Fatalf("part beyond the reach: centre %v, want the zone's nearest point", c)
	}
	if c := spiralCentre(Point{50, 50}, z, step); c != (Point{50, 50}) {
		t.Fatalf("part inside its zone recentred to %v", c)
	}
}

func widenFixture(t *testing.T) (*placer, []*Domain) {
	b := loadFixture(t, "reference-4stage-compact.json")
	var doms []*Domain
	for _, p := range b.Parts {
		if !p.Fixed && len(doms) < 3 {
			doms = append(doms, &Domain{ID: "D" + p.Ref, Parts: []string{p.Ref}})
		}
	}
	if len(doms) < 3 {
		t.Skip("fixture lacks three movable parts")
	}
	return &placer{b: b, c: &Circuit{}, spacing: 10}, doms
}

// The widths' sum never changes: a shortfall bigger than the others' slack
// is shared out and reported, not granted beyond the region.
func TestWidenZonesKeepsTheTotal(t *testing.T) {
	pl, doms := widenFixture(t)
	need := func(d *Domain) float64 {
		bx := pl.box(pl.b.Part(d.Parts[0]))
		return math.Min(bx.W(), bx.H()) + 1
	}
	sum := func(ws []float64) float64 { return ws[0] + ws[1] + ws[2] }
	// Feasible: one short strip, plenty of slack elsewhere.
	ws := []float64{need(doms[0]) / 2, need(doms[1]) + 500, need(doms[2]) + 500}
	total := sum(ws)
	if note := pl.widenZones(doms, ws); note != "" || math.Abs(sum(ws)-total) > 1e-6 || ws[0] < need(doms[0])-1e-6 {
		t.Fatalf("feasible: widths %v (sum %.3f of %.3f), note %q", ws, sum(ws), total, note)
	}
	// Deficit > slack: shared, total kept, reported.
	ws = []float64{need(doms[0]) / 4, need(doms[1]) / 4, need(doms[2]) + 1}
	total = sum(ws)
	note := pl.widenZones(doms, ws)
	if note == "" || math.Abs(sum(ws)-total) > 1e-6 || ws[2] < need(doms[2])-1e-6 {
		t.Fatalf("deficit > slack: widths %v (sum %.3f of %.3f), note %q", ws, sum(ws), total, note)
	}
	// No slack at all: nothing moves, reported.
	ws = []float64{need(doms[0]) / 2, need(doms[1]), need(doms[2])}
	want := append([]float64(nil), ws...)
	note = pl.widenZones(doms, ws)
	if note == "" || ws[0] != want[0] || ws[1] != want[1] || ws[2] != want[2] {
		t.Fatalf("no slack: widths %v (want %v), note %q", ws, want, note)
	}
}

func testPlacer(t *testing.T, name string) *placer {
	b := loadFixture(t, name)
	an := Analyze(b, PowerSpec{}, nil)
	pl := &placer{b: b, an: an, c: Understand(b, an), m: &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}},
		opt: PlaceOptions{SpacingMil: 12, Timeout: time.Minute}, partNet: map[*Part][]int{}, zoneOf: map[*Part]Rect{}, decap: map[*Part]*Pad{}, spacing: 12}
	pl.setup(&PlaceResult{})
	pl.rebuildBuckets()
	return pl
}

// Past the deadline the decap rescue gives up at once: the decap is back
// where it was, filed in exactly its own buckets, the phase named.
func TestRescueStopsAtTheDeadline(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	var d *Part
	for _, p := range pl.movable {
		if pl.decap[p] != nil {
			d = p
			break
		}
	}
	if d == nil {
		t.Skip("fixture has no decap")
	}
	pin := pl.decap[d].Box.C
	movePartCentre(d, Point{pin.X + 1500, pin.Y}, d.Rotation)
	pl.rebuildBuckets()
	pos, rot := d.Pos, d.Rotation
	pl.opt.Deadline = time.Now().Add(-time.Second)
	pl.phase("rescue")
	start := time.Now()
	pl.rescuePart(d, []float64{0, 90, 180, 270})
	if el := time.Since(start); el > time.Second {
		t.Fatalf("rescue past its deadline took %s", el)
	}
	if d.Pos != pos || d.Rotation != rot {
		t.Fatalf("decap moved to %v/%v, want back at %v/%v", d.Pos, d.Rotation, pos, rot)
	}
	if pl.budgetHit != "rescue" {
		t.Fatalf("budgetHit %q, want rescue", pl.budgetHit)
	}
	if pl.boxes[d] != pl.box(d) {
		t.Fatal("box out of date")
	}
	x0, y0, x1, y1 := bucketRange(pl.boxes[d])
	want, got := (x1-x0+1)*(y1-y0+1), 0
	for x := x0 - 3; x <= x1+3; x++ {
		for y := y0 - 3; y <= y1+3; y++ {
			for _, e := range pl.bucket.at(x, y) {
				if e.p == d {
					got++
					if x < x0 || x > x1 || y < y0 || y > y1 {
						t.Fatalf("decap filed in bucket %d,%d outside its box", x, y)
					}
				}
			}
		}
	}
	if got != want {
		t.Fatalf("decap in %d buckets, want %d", got, want)
	}
}

// hardBound never exceeds the hard cost, and a pose whose cost is below the
// limit is never pruned — also with the limit a hair above the cost.
func TestHardBoundNeverPrunesAnAcceptedPose(t *testing.T) {
	pl := testPlacer(t, "lckfb-mipi-3in1-adapter.json")
	rng := rand.New(rand.NewSource(7))
	n := 0
	for _, p := range pl.movable {
		pl.bucketOp(p, false)
		sc := pl.newSpiralCtx(p)
		halo := pl.spacing/2 + pl.opt.Halo[p.Ref]
		c0, r0 := p.Body().Center(), p.Rotation
		for k := 0; k < 40; k++ {
			c := c0.Add(Point{(rng.Float64()*2 - 1) * 400, (rng.Float64()*2 - 1) * 400})
			movePartCentre(p, c, r0)
			pl.boxes[p] = pl.box(p)
			body := p.Body()
			hc := pl.hardCost(p)
			if lb := pl.hardBound(sc, body.Expand(halo), body, math.Inf(1)); lb > hc {
				t.Fatalf("%s: bound %v above the cost %v", p.Ref, lb, hc)
			}
			for _, lim := range []float64{math.Nextafter(hc, math.Inf(1)), hc + 1e-12, hc * (1 + 1e-15)} {
				if hc < lim && boundRejects(pl.hardBound(sc, body.Expand(halo), body, lim), lim) {
					t.Fatalf("%s: cost %v < limit %v pruned", p.Ref, hc, lim)
				}
			}
			n++
		}
		movePartCentre(p, c0, r0)
		pl.boxes[p] = pl.box(p)
		pl.bucketOp(p, true)
	}
	if n == 0 {
		t.Skip("no movable part")
	}
}

// A fixed part inside a strip takes that strip's room: the strip widens,
// the widths' sum stays, and both strips end at about the same fill.
func TestSizeZonesCountsFixedPartsInside(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	var mov []*Part
	for _, p := range pl.movable {
		if !pl.c.IsBridge(p.Ref) {
			mov = append(mov, p)
		}
	}
	if len(mov) < 4 {
		t.Skip("fixture lacks movable parts")
	}
	half := len(mov) / 2
	da, db := &Domain{ID: "A"}, &Domain{ID: "B"}
	for i, p := range mov {
		if i < half {
			da.Parts = append(da.Parts, p.Ref)
		} else {
			db.Parts = append(db.Parts, p.Ref)
		}
	}
	// Equal split, then park one big fixed part in the left strip only.
	ws := []float64{pl.region.W() / 2, pl.region.W() / 2}
	big := mov[0]
	big.Fixed = true
	pl.movable = pl.movable[:0]
	for _, p := range mov[1:] {
		pl.movable = append(pl.movable, p)
	}
	movePartCentre(big, Point{pl.region.MinX + ws[0]/2, pl.region.Center().Y}, big.Rotation)
	before := append([]float64(nil), ws...)
	pl.sizeZonesByOccupancy([]*Domain{da, db}, ws)
	if math.Abs(ws[0]+ws[1]-before[0]-before[1]) > 1e-6 {
		t.Fatalf("widths %v changed the total %v", ws, before[0]+before[1])
	}
	movable := map[*Part]bool{}
	for _, p := range pl.movable {
		movable[p] = true
	}
	occA := pl.zoneOccupied(da, Rect{pl.region.MinX, pl.region.MinY, pl.region.MinX + ws[0], pl.region.MaxY}, movable)
	occB := pl.zoneOccupied(db, Rect{pl.region.MinX + ws[0], pl.region.MinY, pl.region.MaxX, pl.region.MaxY}, movable)
	if fa, fb := occA/ws[0], occB/ws[1]; math.Abs(fa-fb) > 0.05*math.Max(fa, fb) {
		t.Fatalf("strips %v hold %.0f / %.0f mil² — fills per mil %.1f vs %.1f, want about equal", ws, occA, occB, fa, fb)
	}
}
