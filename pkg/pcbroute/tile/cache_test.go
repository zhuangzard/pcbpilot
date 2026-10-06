package tile

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// fakeView is a minimal board.View over a slice of items; the board
// implementation arrives with M4.
type fakeView struct {
	nets  []string
	items []board.Item
}

func (v *fakeView) AddNet(name string) geom.NetID {
	v.nets = append(v.nets, name)
	return geom.NetID(len(v.nets))
}

func (v *fakeView) AddItem(it board.Item) board.ItemID {
	it.ID = board.ItemID(len(v.items) + 1)
	v.items = append(v.items, it)
	return it.ID
}

func (v *fakeView) Build(rules.Resolver) (board.DB, error) { return nil, nil }
func (v *fakeView) NumNets() int                           { return len(v.nets) }
func (v *fakeView) NetName(n geom.NetID) string            { return v.nets[n-1] }
func (v *fakeView) Item(id board.ItemID) (board.Item, bool) {
	if id == 0 || int(id) > len(v.items) {
		return board.Item{}, false
	}
	return v.items[id-1], true
}
func (v *fakeView) Items(fn func(board.Item) bool) {
	for _, it := range v.items {
		if !fn(it) {
			return
		}
	}
}
func (v *fakeView) Query(geom.Rect, geom.LayerID, func(board.Item) bool) {}
func (v *fakeView) Connections() []board.Connection                      { return nil }
func (v *fakeView) Connection(board.ConnID) (board.Connection, bool) {
	return board.Connection{}, false
}
func (v *fakeView) Connected(a, b board.ItemID) bool { return false }

func ptr(v int64) *int64 { return &v }

// testBoard is spec 01 §6's default: 2 signal layers and an inner plane
// layer, track 0.2 mm, clearance 0.2 mm, via 0.6/0.3 mm, a 20×15 mm outline.
// Net "PWR" is a class of its own with 0.5 mm tracks and 0.3 mm clearance.
func testBoard(t *testing.T) (*fakeView, rules.Resolver) {
	t.Helper()
	v := &fakeView{}
	a, b, pwr, gnd := v.AddNet("A"), v.AddNet("B"), v.AddNet("PWR"), v.AddNet("GND")
	rb := rules.NewBuilder()
	rb.AddLayer(rules.Layer{ID: 0, Name: "Top", Outer: true})
	rb.AddLayer(rules.Layer{ID: 1, Name: "In1", Kind: rules.Power})
	rb.AddLayer(rules.Layer{ID: 2, Name: "Bottom", Outer: true})
	rb.Set(rules.Scope{Kind: rules.ScopePCB}, rules.RuleSet{Width: ptr(200_000),
		Clearance: map[rules.ClrType]int64{rules.Generic: 200_000}, UseVia: []string{"V"}})
	rb.SetClass(pwr, 1)
	rb.Set(rules.Scope{Kind: rules.ScopeClass, Class: 1}, rules.RuleSet{Width: ptr(500_000),
		Clearance: map[rules.ClrType]int64{rules.Generic: 300_000}})
	rb.AddVia(rules.ViaType{Name: "V", Pad: 600_000, Drill: 300_000, From: 0, To: 2})
	rb.AddKeepout(rules.Keepout{Shape: geom.Rect{MinX: 15 * mm, MinY: 10 * mm, MaxX: 17 * mm, MaxY: 12 * mm}, Layer: geom.AllLayers, Kind: rules.KeepoutVia})
	rb.AddKeepout(rules.Keepout{Shape: geom.Rect{MinX: 3 * mm, MinY: 10 * mm, MaxX: 5 * mm, MaxY: 12 * mm}, Layer: 0, Kind: rules.KeepoutWire})
	rs, err := rb.Build()
	if err != nil {
		t.Fatal(err)
	}
	v.AddItem(board.Item{Kind: board.Edge, From: geom.AllLayers, To: geom.AllLayers, Fixed: true,
		Shape: geom.Poly{Pts: []geom.Pt{{}, {X: 20 * mm}, {X: 20 * mm, Y: 15 * mm}, {X: 2 * mm, Y: 15 * mm}, {X: 0, Y: 13 * mm}}}})
	pad := func(n geom.NetID, x, y int64, from, to geom.LayerID) {
		v.AddItem(board.Item{Kind: board.Pad, Net: n, From: from, To: to, Fixed: true,
			Shape: geom.Rect{MinX: x - 300_000, MinY: y - 200_000, MaxX: x + 300_000, MaxY: y + 200_000}})
	}
	pad(a, 4*mm, 4*mm, 0, 0)
	pad(a, 12*mm, 4*mm, 0, 0)
	pad(b, 8*mm, 4*mm, 0, 2)
	pad(pwr, 8*mm, 8*mm, 0, 0)
	pad(0, 10*mm, 8*mm, 0, 0)
	v.AddItem(board.Item{Kind: board.Track, Net: b, From: 2, To: 2,
		Shape: geom.Seg{A: geom.Pt{X: 8 * mm, Y: 4 * mm}, B: geom.Pt{X: 11 * mm, Y: 7 * mm}, HalfW: 100_000}})
	v.AddItem(board.Item{Kind: board.Zone, Net: gnd, From: 1, To: 1, Fixed: true,
		Shape: geom.Rect{MaxX: 20 * mm, MaxY: 15 * mm}})
	return v, rs
}

// expectR is the inflation of item it in the track plane of net on layer.
func expectR(rs rules.Resolver, net geom.NetID, it board.Item, layer geom.LayerID) int64 {
	if it.Kind == board.Edge {
		return half(rs.Width(net, layer)) + rs.Edge()
	}
	return half(rs.Width(net, layer)) + rs.Clearance(rules.Obj{Kind: rules.Wire, Net: net}, rules.Obj{Kind: objKind(it), Net: it.Net}, layer)
}

func TestSetTrackPlanes(t *testing.T) {
	v, rs := testBoard(t)
	s := NewSet(v, rs)
	if ks := s.Keys(0); len(ks) != 2 || ks[0] != (InflationKey{100_000, 0}) || ks[1] != (InflationKey{250_000, 1}) {
		t.Fatalf("keys %v", ks)
	}
	rng := rand.New(rand.NewPCG(2, 2))
	for _, net := range []geom.NetID{1, 3} {
		k := s.KeyOf(net, 0)
		p := s.Plane(0, k)
		if s.Plane(0, k) != p {
			t.Fatal("plane not cached")
		}
		checkPlane(t, p, rng)
		// Every point a centreline of net may not reach is impassable for it.
		for _, x := range tilesOf(p) {
			if !x.Passable(net) {
				continue
			}
			for range 3 {
				q := randPt(rng, x.Rect)
				for _, it := range v.items {
					if !spans(it, 0) || it.Net == net || it.Kind == board.Zone {
						continue
					}
					if it.Kind == board.Edge {
						pts := it.Shape.(geom.Poly).Pts
						if !geom.InPoly(q, pts) || nearOutline(q, pts, expectR(rs, net, it, 0)) {
							t.Fatalf("net %d: %v passable but outside or near the outline", net, q)
						}
						continue
					}
					if geom.Within(geom.Circle{C: q}, it.Shape, expectR(rs, net, it, 0)) {
						t.Fatalf("net %d: %v passable but too close to item %d", net, q, it.ID)
					}
				}
			}
		}
	}
	// Own pads are passable for their net only; the wire keep-out blocks tracks.
	pa := s.Plane(0, s.KeyOf(1, 0))
	if x := pa.Locate(geom.Pt{X: 4 * mm, Y: 4 * mm}); !x.Passable(1) || x.Passable(2) {
		t.Fatal("own pad not passable or foreign pad passable")
	}
	if pa.Locate(geom.Pt{X: 4 * mm, Y: 11 * mm}).Passable(1) {
		t.Fatal("wire keep-out passable")
	}
	if !pa.Locate(geom.Pt{X: 16 * mm, Y: 11 * mm}).Passable(1) {
		t.Fatal("via keep-out blocks tracks")
	}
	if pa.Locate(geom.Pt{X: 300_000, Y: 14 * mm}).Passable(1) {
		t.Fatal("outside the chamfered outline is passable")
	}
}

// nearOutline reports whether q lies closer than r to an edge of pts.
func nearOutline(q geom.Pt, pts []geom.Pt, r int64) bool {
	for i := range pts {
		if geom.Within(geom.Circle{C: q}, geom.Seg{A: pts[i], B: pts[(i+1)%len(pts)]}, r) {
			return true
		}
	}
	return false
}

func TestSetAddRemove(t *testing.T) {
	v, rs := testBoard(t)
	s := NewSet(v, rs)
	p0 := s.Plane(0, s.KeyOf(1, 0))
	p2 := s.Plane(2, s.KeyOf(1, 2))
	before := p0.Len()
	tr := board.Item{ID: 100, Kind: board.Track, Net: 2, From: 0, To: 0,
		Shape: geom.Seg{A: geom.Pt{X: 6 * mm, Y: 12 * mm}, B: geom.Pt{X: 9 * mm, Y: 12 * mm}, HalfW: 100_000}}
	s.Add(tr)
	if p0.Locate(geom.Pt{X: 7 * mm, Y: 12 * mm}).Passable(1) {
		t.Fatal("added track not in the plane of its layer")
	}
	if !p2.Locate(geom.Pt{X: 7 * mm, Y: 12 * mm}).Passable(1) {
		t.Fatal("added track in the plane of another layer")
	}
	s.Remove(100)
	if p0.Len() != before || !p0.Locate(geom.Pt{X: 7 * mm, Y: 12 * mm}).Passable(1) {
		t.Fatal("remove did not restore the plane")
	}
}

func TestViaPlane(t *testing.T) {
	v, rs := testBoard(t)
	s := NewSet(v, rs)
	via := rs.Vias(1)[0]
	p := s.ViaPlane(via, s.ViaKey(1, via))
	checkPlane(t, p, rand.New(rand.NewPCG(4, 4)))
	if p.Locate(geom.Pt{X: 16 * mm, Y: 11 * mm}).Passable(1) {
		t.Fatal("via keep-out passable in the via plane")
	}
	if !p.Locate(geom.Pt{X: 4 * mm, Y: 11 * mm}).Passable(1) {
		t.Fatal("wire keep-out blocks vias")
	}
	// The GND pour on the plane layer is skipped; the bottom track of net B is not.
	if !p.Locate(geom.Pt{X: 15 * mm, Y: 4 * mm}).Passable(1) {
		t.Fatal("plane-layer pour blocks vias")
	}
	q := geom.Pt{X: 10 * mm, Y: 6*mm + 600_000}
	if p.Locate(q).Passable(1) {
		t.Fatal("bottom track does not block vias")
	}
	want := half(via.Pad) + rs.Clearance(rules.Obj{Kind: rules.Via, Net: 1}, rules.Obj{Kind: rules.Wire, Net: 2}, 2)
	seg := v.items[6].Shape
	for _, x := range tilesOf(p) {
		if x.Passable(1) && geom.Within(geom.Circle{C: geom.Pt{X: x.Rect.MinX, Y: x.Rect.MinY}}, seg, want) {
			t.Fatalf("via site %v too close to the bottom track", x.Rect)
		}
	}
	if !slices.Contains(p.Locate(geom.Pt{X: 8 * mm, Y: 4 * mm}).Owners, 2) {
		t.Fatal("through pad of net B missing from the via plane")
	}
}

func TestOutlineConservative(t *testing.T) {
	poly := geom.Poly{Pts: []geom.Pt{{X: 1 * mm, Y: 1 * mm}, {X: 9 * mm, Y: 1 * mm}, {X: 9 * mm, Y: 6 * mm},
		{X: 5 * mm, Y: 4*mm + 333_333}, {X: 1 * mm, Y: 8 * mm}}}
	b := geom.Rect{MaxX: 10 * mm, MaxY: 9 * mm}
	p := NewPlane(b, 100_000)
	const R = 350_000
	p.Insert(Obstacle{ID: 1, Shape: poly, R: R, Outline: true})
	rng := rand.New(rand.NewPCG(8, 8))
	checkPlane(t, p, rng)
	for range 20000 {
		q := randPt(rng, b)
		if (nearOutline(q, poly.Pts, R) || !geom.InPoly(q, poly.Pts)) && p.Locate(q).Kind == Space {
			t.Fatalf("%v is outside or near the outline but Space", q)
		}
	}
	if p.Locate(geom.Pt{X: 3 * mm, Y: 3 * mm}).Kind != Space {
		t.Fatal("inside the outline is not Space")
	}
}

func TestSetDeterministic(t *testing.T) {
	dump := func() string {
		v, rs := testBoard(t)
		s := NewSet(v, rs)
		out := ""
		for _, l := range []geom.LayerID{2, 0, 1} {
			for _, k := range s.Keys(l) {
				for _, x := range tilesOf(s.Plane(l, k)) {
					out += fmt.Sprint(x.Rect, x.Owners)
				}
			}
		}
		// A neck-down plane of the default row: same obstacles, narrower track.
		for _, x := range tilesOf(s.Plane(0, InflationKey{HalfWidth: 60_000, Class: s.KeyOf(1, 0).Class})) {
			out += fmt.Sprint(x.Rect, x.Owners)
		}
		return out
	}
	if dump() != dump() {
		t.Fatal("same board gives different planes")
	}
}

func TestNeckPlaneIsNarrower(t *testing.T) {
	v, rs := testBoard(t)
	s := NewSet(v, rs)
	k := s.KeyOf(1, 0)
	full, neck := s.Plane(0, k), s.Plane(0, InflationKey{HalfWidth: 60_000, Class: k.Class})
	// 0.27 mm right of net B's pad edge: closer than 0.1 + 0.2 mm (full
	// width plus clearance), farther than 0.06 + 0.2 mm (neck width).
	q := geom.Pt{X: 8*mm + 300_000 + 270_000, Y: 4 * mm}
	if full.Locate(q).Passable(1) || !neck.Locate(q).Passable(1) {
		t.Fatalf("full %v neck %v", full.Locate(q).Owners, neck.Locate(q).Owners)
	}
}
