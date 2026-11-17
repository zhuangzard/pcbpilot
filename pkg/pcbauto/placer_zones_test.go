package pcbauto

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestUnionAreaCountsOverlapsOnce(t *testing.T) {
	in := Rect{0, 0, 100, 100}
	rs := []Rect{{0, 0, 60, 60}, {40, 40, 100, 100}, {-50, -50, 10, 10}, {200, 200, 300, 300}}
	// 3600 + 3600 - 400 (shared 20 x 20); the third lies inside the first
	// once clipped, the fourth outside.
	if a := unionArea(in, rs); math.Abs(a-6800) > 1e-6 {
		t.Fatalf("union %v, want 6800", a)
	}
	if a := unionArea(in, nil); a != 0 {
		t.Fatalf("union of nothing %v", a)
	}
}

// A keep-out that names the top side leaves the bottom side to surface-
// mount parts; a through-hole part's pads still meet a copper keep-out of
// the other side. A keep-out without layers reaches everything.
func TestKeepoutReachesOnlyItsSide(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	var smd *Part
	for _, p := range pl.movable {
		if !hasTHT(p) && !pl.c.IsBridge(p.Ref) {
			smd = p
			break
		}
	}
	if smd == nil {
		t.Skip("fixture has no surface-mount part")
	}
	box := smd.Body().Expand(20).Corners()
	topParts := &Keepout{Name: "top", Poly: box, Layers: []int{LayerTop}, NoParts: true}
	topCopper := &Keepout{Name: "topcu", Poly: box, Layers: []int{LayerTop}, NoCopper: true}
	all := &Keepout{Name: "all", Poly: box, NoParts: true}

	smd.Side = LayerTop
	if !pl.repelsPart(topParts, smd) || !noPartsOn(topParts, smd) {
		t.Fatal("a top-side no-parts area does not hold a top part off")
	}
	smd.Side = LayerBottom
	if pl.repelsPart(topParts, smd) || noPartsOn(topParts, smd) {
		t.Fatal("a top-side no-parts area blocks a bottom surface-mount part")
	}
	if pl.repelsPart(topCopper, smd) {
		t.Fatal("a top-side copper keep-out blocks a bottom surface-mount part")
	}
	if !pl.repelsPart(all, smd) || !noPartsOn(all, smd) {
		t.Fatal("an all-layer keep-out lets a bottom part in")
	}
	// The same checks the placer runs: cost, the report's list.
	pl.b.Keepouts = append(pl.b.Keepouts, topParts)
	pl.boxes[smd] = pl.box(smd)
	if list := strings.Join(pl.illegalList(nil), ";"); strings.Contains(list, "inside a keep-out "+smd.Ref) {
		t.Fatalf("bottom part reported inside a top-side keep-out: %s", list)
	}
	smd.Side = LayerTop
	if list := strings.Join(pl.illegalList(nil), ";"); !strings.Contains(list, "inside a keep-out "+smd.Ref) {
		t.Fatalf("top part not reported inside the top-side keep-out: %s", list)
	}
	// Through-hole pads go through every layer.
	tht := &Part{Ref: "JX", Side: LayerBottom, Pads: []*Pad{{Number: "1", Layer: LayerMulti}}}
	if !pl.repelsPart(topCopper, tht) {
		t.Fatal("a top-side copper keep-out lets a through-hole part in")
	}
	if pl.repelsPart(topParts, tht) {
		t.Fatal("a top-side no-parts area (courtyard rule) blocks a bottom through-hole part")
	}
}

// Several rectangles for one domain: the parts are shared out, none is
// filled past what it can hold while another has room, and a functional
// block stays in one rectangle.
func TestShareRectsFillsEveryRectangle(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	var parts []*Part
	need := 0.0
	for _, p := range pl.movable {
		// (Not the one part as large as the rest together: no rectangle
		// sized for a share of the parts could hold it.)
		if a := p.Body().Expand(pl.spacing / 2).Area(); !pl.c.IsBridge(p.Ref) && !hasTHT(p) && a < 100000 {
			parts = append(parts, p)
			need += a
		}
	}
	if len(parts) < 6 {
		t.Skip("fixture too small")
	}
	// Three empty rectangles far from every fixed part, each good for 45 %
	// of the parts.
	side := math.Sqrt(0.45 * need / ZoneTightFill)
	x0, y0 := pl.region.MaxX+5000, pl.region.MaxY+5000
	pl.region = Rect{pl.region.MinX, pl.region.MinY, x0 + 10*side, y0 + 10*side}
	rs := []Rect{{x0, y0, x0 + side, y0 + side}, {x0 + 2*side, y0, x0 + 3*side, y0 + side}, {x0 + 4*side, y0, x0 + 5*side, y0 + side}}
	counts := pl.shareRects(rs, parts, true)
	total := 0
	for i, n := range counts {
		total += n
		if n == 0 {
			t.Fatalf("rectangle %d got no part: %v", i+1, counts)
		}
	}
	if total != len(parts) {
		t.Fatalf("shared %d of %d parts", total, len(parts))
	}
	got := make([]float64, len(rs))
	blockRect := map[string]Rect{}
	split := map[string]bool{}
	for _, p := range parts {
		z := pl.zoneOf[p]
		at := -1
		for i, r := range rs {
			if r == z {
				at = i
			}
		}
		if at < 0 {
			t.Fatalf("%s got zone %v, none of its domain's", p.Ref, z)
		}
		got[at] += p.Body().Expand(pl.spacing / 2).Area()
		if id := pl.c.BlockOf[p.Ref]; id != "" {
			if r, ok := blockRect[id]; ok && r != z {
				split[id] = true
			}
			blockRect[id] = z
		}
	}
	for i, a := range got {
		if a > rs[i].Area() {
			t.Fatalf("rectangle %d holds %.0f mil² of parts in %.0f", i+1, a, rs[i].Area())
		}
	}
	// A block may only be split when no rectangle could hold it whole.
	for id := range split {
		a := 0.0
		for _, p := range parts {
			if pl.c.BlockOf[p.Ref] == id {
				a += p.Body().Expand(pl.spacing / 2).Area()
			}
		}
		if a <= ZoneTightFill*rs[0].Area() {
			t.Fatalf("block %s (%.0f mil²) fits one rectangle (%.0f) and was split", id, a, ZoneTightFill*rs[0].Area())
		}
	}
}

// --mech zones naming one domain twice: both rectangles are used (the
// second no longer replaces the first) and the result says how.
func TestZonesUsesEveryRectangleOfADomain(t *testing.T) {
	b := loadFixture(t, "reference-4stage-compact.json")
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	// The domain with the most parts.
	var dom *Domain
	for _, d := range c.Domains {
		if d.ID != "UNREFERENCED" && (dom == nil || len(d.Parts) > len(dom.Parts)) {
			dom = d
		}
	}
	if dom == nil {
		t.Skip("fixture has no domain")
	}
	m := &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}}
	pl := &placer{b: b, an: an, c: c, m: m, opt: PlaceOptions{SpacingMil: 12, Timeout: time.Minute},
		partNet: map[*Part][]int{}, zoneOf: map[*Part]Rect{}, decap: map[*Part]*Pad{}, spacing: 12}
	bb := b.Bounds()
	need := 0.0
	for _, ref := range dom.Parts {
		if p := b.Part(ref); p != nil {
			need += p.Body().Expand(6).Area()
		}
	}
	// Two rectangles, each good for 60 % of the domain.
	h := bb.MaxY - bb.MinY
	w := 0.6 * need / ZoneTightFill / h
	left := Rect{bb.MinX, bb.MinY, bb.MinX + w, bb.MaxY}
	right := Rect{bb.MaxX - w, bb.MinY, bb.MaxX, bb.MaxY}
	if left.MaxX >= right.MinX {
		t.Skip("fixture board too small for two rectangles")
	}
	m.Zones = []Zone{{Domain: dom.ID, Rect: left}, {Domain: dom.ID, Rect: right}}
	res := &PlaceResult{}
	pl.setup(res)
	n := map[Rect]int{}
	for _, p := range pl.movable {
		if c.DomainOf[p.Ref] == dom.ID && !c.IsBridge(p.Ref) {
			z := pl.zoneOf[p]
			if z != left && z != right {
				t.Fatalf("%s got zone %v, want one of the domain's two", p.Ref, z)
			}
			n[z]++
		}
	}
	if n[left] == 0 || n[right] == 0 {
		t.Fatalf("parts per rectangle %d / %d: one rectangle is unused", n[left], n[right])
	}
	found := false
	for _, note := range res.Notes {
		found = found || strings.Contains(note, "shared between its 2 zone rectangles")
	}
	if !found {
		t.Fatalf("no note about the shared rectangles: %v", res.Notes)
	}
	// The report numbers the two rectangles.
	zs := pl.zoneFill(res, nil)
	seen := map[int]bool{}
	for _, z := range zs {
		if z.Domain == dom.ID {
			seen[z.Zone] = true
		}
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("zone fill does not number the rectangles: %+v", zs)
	}
}

// Room is counted per side: parts on the bottom do not take the top's
// room, a top-side keep-out leaves the bottom's offer whole, and two
// overlapping keep-outs take their shared area once.
func TestZoneFillCountsEachSide(t *testing.T) {
	pl := testPlacer(t, "reference-4stage-compact.json")
	var parts []*Part
	need := 0.0
	for _, p := range pl.movable {
		if !pl.c.IsBridge(p.Ref) && !hasTHT(p) {
			parts = append(parts, p)
			need += p.Body().Expand(pl.spacing / 2).Area()
		}
	}
	if len(parts) < 4 {
		t.Skip("fixture too small")
	}
	// An empty zone off the fixed parts, 1.5 x the parts' area: over-full
	// on one side only once keep-outs take half of it.
	side := math.Sqrt(1.5 * need)
	x0, y0 := pl.region.MaxX+5000, pl.region.MaxY+5000
	pl.region = Rect{pl.region.MinX, pl.region.MinY, x0 + 2*side, y0 + 2*side}
	z := Rect{x0, y0, x0 + side, y0 + side}
	for _, p := range pl.movable {
		pl.zoneOf[p] = pl.region
	}
	for _, p := range parts {
		p.Side = LayerTop
		pl.zoneOf[p] = z
	}
	res := &PlaceResult{Zones: []Zone{{Domain: "DOM", Rect: z}}}
	pick := func() ZoneFill {
		for _, f := range pl.zoneFill(res, nil) {
			if f.Domain == "DOM" {
				return f
			}
		}
		t.Fatal("zone DOM not reported")
		return ZoneFill{}
	}
	f := pick()
	if f.OverFull || f.Fill == nil || math.Abs(*f.Fill-1/1.5) > 0.02 || len(f.Sides) != 0 {
		t.Fatalf("all on top, no keep-out: %+v, want fill 0.67 and no sides", f)
	}
	// Two overlapping top-side keep-outs over the left half (each 0.4 wide,
	// 0.3 shared): half the top is gone, counted once.
	ko := func(a, b float64) *Keepout {
		return &Keepout{Name: "k", Poly: Rect{x0 + a*side, y0, x0 + b*side, y0 + side}.Corners(), Layers: []int{LayerTop}, NoParts: true}
	}
	pl.b.Keepouts = append(pl.b.Keepouts, ko(0, 0.4), ko(0.1, 0.5))
	f = pick()
	if !f.OverFull || f.Side != "top" || len(f.Sides) != 2 {
		t.Fatalf("top half kept out: %+v, want over-full on the top side", f)
	}
	top, bot := f.Sides[0], f.Sides[1]
	if math.Abs(top.OfferMm2-0.5*z.Area()*mil2ToMm2) > 0.2 {
		t.Fatalf("top offer %.1f mm², want half the zone %.1f (overlap counted once)", top.OfferMm2, 0.5*z.Area()*mil2ToMm2)
	}
	if math.Abs(bot.OfferMm2-z.Area()*mil2ToMm2) > 0.2 || bot.Parts != 0 || bot.NeedMm2 != 0 {
		t.Fatalf("bottom side %+v, want the whole zone on offer and nothing needed", bot)
	}
	// Half the parts on the bottom: neither side is over-full.
	half := 0.0
	for _, p := range parts {
		if half < need/2 {
			p.Side = LayerBottom
			half += p.Body().Expand(pl.spacing / 2).Area()
		}
	}
	f = pick()
	if f.OverFull || len(f.Sides) != 2 || f.Sides[1].Parts == 0 {
		t.Fatalf("half on the bottom: %+v, want no side over-full", f)
	}
	if got := f.Sides[0].NeedMm2 + f.Sides[1].NeedMm2; math.Abs(got-need*mil2ToMm2) > 0.3 {
		t.Fatalf("sides need %.1f mm² together, want the parts' %.1f", got, need*mil2ToMm2)
	}
}

// A rule area on several layers but not all of them keeps to those layers:
// one on the top and an inner layer leaves the bottom side free.
func TestRegionKeepoutKeepsItsLayerList(t *testing.T) {
	rec := map[string]any{
		"layer": float64(LayerMulti), "layers": []any{float64(LayerTop), float64(15)},
		"ruleType": []any{float64(2)}, "bbox": map[string]any{"minX": 0.0, "minY": 0.0, "maxX": 100.0, "maxY": 100.0},
		"source": []any{0.0, 0.0, "L", 100.0, 0.0, 100.0, 100.0, 0.0, 100.0},
	}
	k := regionKeepout(rec)
	if k == nil {
		t.Fatal("no keep-out")
	}
	if !k.onLayer(LayerTop) || k.onLayer(LayerBottom) {
		t.Fatalf("layers %v: want the top, not the bottom", k.Layers)
	}
	delete(rec, "layers")
	if k := regionKeepout(rec); k == nil || !k.onLayer(LayerBottom) {
		t.Fatal("a rule area without a layer list reaches every layer")
	}
}
