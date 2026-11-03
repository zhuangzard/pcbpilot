package pcbauto

import (
	"strings"
	"testing"
)

// A board 10000 x 8000 mil: domain BIG with many movable parts, domain A with a
// connector on the top edge, domain B with one on the right edge.
func edgeZoneBoard(t *testing.T) (*Board, *Circuit, *Mechanics, Rect) {
	t.Helper()
	bb := Rect{0, 0, 10000, 8000}
	b := &Board{Outline: bb.Corners()}
	part := func(ref string, x, y, w, h float64, fixed bool, nets ...string) {
		p := &Part{Ref: ref, Pos: Point{x, y}, Fixed: fixed}
		for i, n := range nets {
			p.Pads = append(p.Pads, &Pad{Part: ref, Number: string(rune('1' + i)), Net: n, Layer: 1,
				Box: OrientedBox{C: Point{x - w/2 + float64(i)*w/float64(len(nets)), y}, W: 20, H: 20}})
		}
		p.SetBody(Rect{x - w/2, y - h/2, x + w/2, y + h/2})
		b.Parts = append(b.Parts, p)
	}
	part("JA", 2000, 7900, 800, 200, true, "A1", "A2", "A3")
	part("JB", 9900, 2000, 200, 600, true, "B1", "B2")
	part("JM", 100, 1000, 200, 500, true, "M1", "M2")
	c := &Circuit{DomainOf: map[string]string{}}
	big := &Domain{ID: "BIG", Nets: []string{"M1", "M2"}, Parts: []string{"JM"}}
	for i := 0; i < 200; i++ {
		ref := "R" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26))
		part(ref, 3000+float64(i), 3000, 200, 150, false, "M1")
		big.Parts = append(big.Parts, ref)
	}
	da := &Domain{ID: "A", Nets: []string{"A1", "A2", "A3"}, Parts: []string{"JA"}}
	db := &Domain{ID: "B", Nets: []string{"B1", "B2"}, Parts: []string{"JB"}}
	for i := 0; i < 20; i++ {
		ref := "CA" + string(rune('a'+i))
		part(ref, 2000, 7000, 100, 100, false, "A1")
		da.Parts = append(da.Parts, ref)
		ref = "CB" + string(rune('a'+i))
		part(ref, 9000, 2000, 100, 100, false, "B1")
		db.Parts = append(db.Parts, ref)
	}
	c.Domains = []*Domain{big, da, db}
	c.Barriers = []*Barrier{{A: "BIG", B: "A", CreepageMil: 315}, {A: "BIG", B: "B", CreepageMil: 250}, {A: "A", B: "B", CreepageMil: 100}}
	m := &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{"JA": true, "JB": true, "JM": true}}
	return b, c, m, bb.Expand(-80)
}

func zoneOf(t *testing.T, p *EdgeZonePlan, id string) Rect {
	t.Helper()
	for _, z := range p.Zones {
		if z.Domain == id {
			return z.Rect
		}
	}
	t.Fatalf("no zone for %s: %+v", id, p.Zones)
	return Rect{}
}

func TestPlanEdgeZonesAgainstConnectors(t *testing.T) {
	b, c, m, region := edgeZoneBoard(t)
	p, err := PlanEdgeZones(b, c, m, region, 20)
	if err != nil {
		t.Fatal(err)
	}
	a, bz, big := zoneOf(t, p, "A"), zoneOf(t, p, "B"), zoneOf(t, p, "BIG")
	if a.MaxY != region.MaxY || a.MinX > 1600 || a.MaxX < 2400 {
		t.Errorf("A is not against its connector on the top edge: %+v", a)
	}
	if bz.MaxX != region.MaxX || bz.MinY > 1700 || bz.MaxY < 2300 {
		t.Errorf("B is not against its connector on the right edge: %+v", bz)
	}
	if g := rectGap(a, big); g < 315-1e-6 {
		t.Errorf("A to BIG %.0f mil", g)
	}
	if g := rectGap(bz, big); g < 250-1e-6 {
		t.Errorf("B to BIG %.0f mil", g)
	}
	if big.Area() < 0.5*region.Area() {
		t.Errorf("BIG got too little: %+v", big)
	}
	q, err := PlanEdgeZones(b, c, m, region, 20)
	if err != nil || len(q.Zones) != len(p.Zones) {
		t.Fatalf("second run: %v", err)
	}
	for i := range p.Zones {
		if p.Zones[i] != q.Zones[i] {
			t.Fatalf("not deterministic: %+v %+v", p.Zones[i], q.Zones[i])
		}
	}
}

func TestPlanEdgeZonesSharedConnector(t *testing.T) {
	b, c, m, region := edgeZoneBoard(t)
	// A pad of domain B on the connector of A: B keeps its own, and it is said.
	b.Part("JA").Pads = append(b.Part("JA").Pads, &Pad{Part: "JA", Number: "9", Net: "B2", Layer: 1, Box: OrientedBox{C: Point{2000, 7900}, W: 20, H: 20}})
	p, err := PlanEdgeZones(b, c, m, region, 20)
	if err != nil {
		t.Fatal(err)
	}
	if bz := zoneOf(t, p, "B"); bz.MaxX != region.MaxX {
		t.Errorf("B left its connector: %+v", bz)
	}
	if !strings.Contains(strings.Join(p.Notes, "\n"), "JA") {
		t.Errorf("the outside pad is not reported: %v", p.Notes)
	}
}

func TestPlanEdgeZonesRefuses(t *testing.T) {
	// No fixed part for a small domain.
	b, c, m, region := edgeZoneBoard(t)
	delete(m.Fixed, "JB")
	b.Part("JB").Fixed = false
	if _, err := PlanEdgeZones(b, c, m, region, 20); err == nil || !strings.Contains(err.Error(), "no fixed part") {
		t.Errorf("no anchor: %v", err)
	}
	// One domain only.
	b, c, m, region = edgeZoneBoard(t)
	c.Domains = c.Domains[:1]
	if _, err := PlanEdgeZones(b, c, m, region, 20); err == nil {
		t.Error("one domain must be refused")
	}
	// The largest domain does not fit what is left.
	b, c, m, region = edgeZoneBoard(t)
	for _, p := range b.Parts {
		if strings.HasPrefix(p.Ref, "R") {
			p.SetBody(Rect{p.Pos.X - 400, p.Pos.Y - 300, p.Pos.X + 400, p.Pos.Y + 300})
		}
	}
	if _, err := PlanEdgeZones(b, c, m, region, 20); err == nil || !strings.Contains(err.Error(), "fill") {
		t.Errorf("no room: %v", err)
	}
	// Two connectors too close along one edge for their domains' pair gap.
	b, c, m, region = edgeZoneBoard(t)
	jb := b.Part("JB")
	jb.Pos = Point{2100, 7900}
	for i := range jb.Pads {
		jb.Pads[i].Box.C = Point{2100, 7900}
	}
	jb.SetBody(Rect{1700, 7800, 2500, 8000})
	if _, err := PlanEdgeZones(b, c, m, region, 20); err == nil || !strings.Contains(err.Error(), "pushed off") {
		t.Errorf("competing connectors: %v", err)
	}
	// A fixed part of the largest domain inside a small domain's rectangle.
	b, c, m, region = edgeZoneBoard(t)
	jm := b.Part("JM")
	jm.Pos = Point{2000, 7600}
	for i := range jm.Pads {
		jm.Pads[i].Box.C = Point{2000, 7600}
	}
	jm.SetBody(Rect{1900, 7500, 2100, 7700})
	if _, err := PlanEdgeZones(b, c, m, region, 20); err == nil || !strings.Contains(err.Error(), "lies inside") {
		t.Errorf("fixed part in another zone: %v", err)
	}
}

func TestLargestFreeRect(t *testing.T) {
	r, ok := largestFreeRect(Rect{0, 0, 100, 100}, []Rect{{80, 0, 100, 100}})
	if !ok || r != (Rect{0, 0, 80, 100}) {
		t.Fatalf("%+v %v", r, ok)
	}
	if _, ok := largestFreeRect(Rect{0, 0, 100, 100}, []Rect{{-1, -1, 101, 101}}); ok {
		t.Fatal("no free rectangle")
	}
}

func TestIntentRenameNets(t *testing.T) {
	in := &Intent{
		Domains:    []IntentDomain{{ID: "A", Nets: []string{"N"}}},
		Nets:       map[string]*IntentNet{"N": {Domain: "A", DiffPair: "M"}, "M": {}},
		NetClasses: []IntentNetClass{{Name: "c", Nets: []string{"N"}}},
	}
	in.RenameNets(func(n string) string { return "/s/" + n })
	if in.Domains[0].Nets[0] != "/s/N" || in.NetClasses[0].Nets[0] != "/s/N" {
		t.Fatalf("%+v", in)
	}
	n := in.Nets["/s/N"]
	if n == nil || n.DiffPair != "/s/M" || in.Nets["/s/M"] == nil || in.Nets["N"] != nil {
		t.Fatalf("%+v", in.Nets)
	}
	var none *Intent
	none.RenameNets(func(s string) string { return s })
}
