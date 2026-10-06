package tile

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// smallShape is a random shape on a small lattice, so every lattice point of
// the plane can be checked.
func smallShape(rng *rand.Rand) geom.Shape {
	c := geom.Pt{X: 20 + rng.Int64N(80), Y: 20 + rng.Int64N(80)}
	d := func() int64 { return 1 + rng.Int64N(25) }
	switch rng.IntN(7) {
	case 0:
		return geom.Rect{MinX: c.X, MinY: c.Y, MaxX: c.X + d(), MaxY: c.Y + d()}
	case 1:
		return geom.Circle{C: c, R: rng.Int64N(12)}
	case 2:
		return geom.Seg{A: c, B: geom.Pt{X: c.X + d(), Y: c.Y}, HalfW: rng.Int64N(8)}
	case 3:
		n := d()
		return geom.Seg{A: c, B: geom.Pt{X: c.X + n, Y: c.Y - n}, HalfW: rng.Int64N(8)}
	case 4:
		return geom.Seg{A: c, B: geom.Pt{X: c.X + d(), Y: c.Y + d()}, HalfW: rng.Int64N(8)}
	case 5:
		w, h := d(), d()
		return geom.Poly{Pts: []geom.Pt{c, {X: c.X + w, Y: c.Y}, {X: c.X + w, Y: c.Y + h}, {X: c.X, Y: c.Y + h}}}
	}
	return geom.Poly{Pts: []geom.Pt{c, {X: c.X + d(), Y: c.Y + d()/2}, {X: c.X + d()/2, Y: c.Y + d()}, {X: c.X - d()/3, Y: c.Y + d()/2}}}
}

// TestExhaustiveConservative checks every lattice point of a small plane
// after random inserts, deletes and refinements: a point closer than R to a
// live obstacle must lie in a tile that the obstacle's net owns.
func TestExhaustiveConservative(t *testing.T) {
	for seed := range uint64(30) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 11))
			b := geom.Rect{MaxX: 120, MaxY: 120}
			p := NewPlane(b, 1+rng.Int64N(9))
			var live []Obstacle
			for i := range 25 {
				switch {
				case len(live) > 0 && rng.IntN(4) == 0:
					k := rng.IntN(len(live))
					p.Delete(live[k].ID)
					live = slices.Delete(live, k, k+1)
				case rng.IntN(5) == 0:
					x, y := rng.Int64N(110), rng.Int64N(110)
					p.Refine(geom.Rect{MinX: x, MinY: y, MaxX: x + 10, MaxY: y + 10})
				default:
					o := Obstacle{ID: uint32(i + 1), Net: geom.NetID(rng.IntN(3)), Shape: smallShape(rng), R: rng.Int64N(15)}
					p.Insert(o)
					live = append(live, o)
				}
			}
			checkPlane(t, p, rng)
			for y := b.MinY; y < b.MaxY; y++ {
				for x := b.MinX; x < b.MaxX; x++ {
					q := geom.Pt{X: x, Y: y}
					tl := p.Locate(q)
					for _, o := range live {
						if geom.Within(geom.Circle{C: q}, o.Shape, o.R) && !containsNet(tl.Owners, o.Net) {
							t.Fatalf("%v closer than %d to %v (net %d) but tile owners %v", q, o.R, o.Shape, o.Net, tl.Owners)
						}
					}
				}
			}
		})
	}
}

func containsNet(s []geom.NetID, n geom.NetID) bool {
	for _, v := range s {
		if v == n {
			return true
		}
	}
	return false
}

// TestExhaustiveOutline checks every lattice point around random simple
// (possibly non-convex) outlines: outside or closer than R to the outline
// must be Solid.
func TestExhaustiveOutline(t *testing.T) {
	for seed := range uint64(30) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 5))
			// A star-shaped polygon around (60,60) is simple.
			n := 3 + rng.IntN(8)
			var pts []geom.Pt
			for i := range n {
				a := float64(i) / float64(n) * 6.283185307179586
				rad := 15 + rng.Float64()*35
				pts = append(pts, geom.Pt{X: 60 + int64(rad*cos(a)), Y: 60 + int64(rad*sin(a))})
			}
			pts = dedup(pts)
			if len(pts) < 3 {
				t.Skip()
			}
			b := geom.Rect{MaxX: 120, MaxY: 120}
			p := NewPlane(b, 1+rng.Int64N(6))
			R := rng.Int64N(8)
			p.Insert(Obstacle{ID: 1, Shape: geom.Poly{Pts: pts}, R: R, Outline: true})
			for y := b.MinY; y < b.MaxY; y++ {
				for x := b.MinX; x < b.MaxX; x++ {
					q := geom.Pt{X: x, Y: y}
					bad := !geom.InPoly(q, pts) || nearOutline(q, pts, R)
					if bad && p.Locate(q).Kind == Space {
						t.Fatalf("%v outside or within %d of outline %v but Space", q, R, pts)
					}
				}
			}
		})
	}
}

func dedup(pts []geom.Pt) []geom.Pt {
	var out []geom.Pt
	for _, q := range pts {
		if len(out) == 0 || out[len(out)-1] != q {
			out = append(out, q)
		}
	}
	if len(out) > 1 && out[0] == out[len(out)-1] {
		out = out[:len(out)-1]
	}
	return out
}

func cos(a float64) float64 { return math.Cos(a) }
func sin(a float64) float64 { return math.Sin(a) }

// TestExhaustiveCircleOutline covers a round board outline (a DSN boundary
// may be a circle): outside the circle or closer than R to it is Solid.
func TestExhaustiveCircleOutline(t *testing.T) {
	for seed := range uint64(20) {
		rng := rand.New(rand.NewPCG(seed, 9))
		c := geom.Circle{C: geom.Pt{X: 60 + rng.Int64N(5), Y: 60 - rng.Int64N(5)}, R: 3 + rng.Int64N(50)}
		b := geom.Rect{MaxX: 120, MaxY: 120}
		p := NewPlane(b, 1+rng.Int64N(6))
		R := rng.Int64N(8)
		p.Insert(Obstacle{ID: 1, Shape: c, R: R, Outline: true})
		checkPlane(t, p, rng)
		for y := b.MinY; y < b.MaxY; y++ {
			for x := b.MinX; x < b.MaxX; x++ {
				q := geom.Pt{X: x, Y: y}
				dx, dy := q.X-c.C.X, q.Y-c.C.Y
				outside := dx*dx+dy*dy > c.R*c.R
				// Closer than R to the circle line: |√(dx²+dy²) - C.R| < R.
				d := math.Sqrt(float64(dx*dx + dy*dy))
				if (outside || math.Abs(d-float64(c.R)) < float64(R)) && p.Locate(q).Kind == Space {
					t.Fatalf("seed %d: %v outside or within %d of circle %v but Space", seed, q, R, c)
				}
			}
		}
	}
	// A large round board keeps most of its inside free.
	p := NewPlane(geom.Rect{MinX: -60 * mm, MinY: -60 * mm, MaxX: 60 * mm, MaxY: 60 * mm}, 100_000)
	p.Insert(Obstacle{ID: 1, Shape: geom.Circle{R: 50 * mm}, R: 400_000, Outline: true})
	for _, q := range []geom.Pt{{}, {X: 49 * mm}, {Y: -49 * mm}, {X: 34 * mm, Y: 34 * mm}} {
		if p.Locate(q).Kind != Space {
			t.Fatalf("%v inside the round outline is Solid", q)
		}
	}
}

// TestViaPlaneKeepoutLayerClearance: an all-layer via keep-out in a via plane
// is inflated by the largest Area clearance over the via's span, not only by
// the first layer's.
func TestViaPlaneKeepoutLayerClearance(t *testing.T) {
	v := &fakeView{}
	a := v.AddNet("A")
	rb := rules.NewBuilder()
	rb.AddLayer(rules.Layer{ID: 0, Name: "Top", Outer: true})
	rb.AddLayer(rules.Layer{ID: 1, Name: "Bottom", Outer: true})
	rb.Set(rules.Scope{Kind: rules.ScopePCB}, rules.RuleSet{Width: ptr(200_000),
		Clearance: map[rules.ClrType]int64{rules.Generic: 200_000}, UseVia: []string{"V"}})
	rb.Set(rules.Scope{Kind: rules.ScopeLayer, Layer: 1}, rules.RuleSet{
		Clearance: map[rules.ClrType]int64{rules.Generic: 900_000}})
	rb.AddVia(rules.ViaType{Name: "V", Pad: 600_000, Drill: 300_000, From: 0, To: 1})
	ko := geom.Rect{MinX: 5 * mm, MinY: 5 * mm, MaxX: 6 * mm, MaxY: 6 * mm}
	rb.AddKeepout(rules.Keepout{Shape: ko, Layer: geom.AllLayers, Kind: rules.KeepoutVia})
	rs, err := rb.Build()
	if err != nil {
		t.Fatal(err)
	}
	v.AddItem(board.Item{Kind: board.Pad, Net: a, From: 0, To: 0, Fixed: true,
		Shape: geom.Rect{MinX: 10 * mm, MinY: 10 * mm, MaxX: 11 * mm, MaxY: 11 * mm}})
	s := NewSet(v, rs)
	via := rs.Vias(a)[0]
	p := s.ViaPlane(via, s.ViaKey(a, via))
	c := rs.Clearance(rules.Obj{Kind: rules.Via, Net: a}, rules.Obj{Kind: rules.Area}, 1)
	if c <= 200_000 {
		t.Skipf("layer rule did not raise the Area clearance (%d)", c)
	}
	// Just inside the bottom layer's inflation, right of the keep-out.
	q := geom.Pt{X: ko.MaxX - 1 + half(via.Pad) + c - 1, Y: 5*mm + 500_000}
	if p.Locate(q).Passable(a) {
		t.Fatalf("%v is closer than %d to the via keep-out on Bottom but is a via site", q, half(via.Pad)+c)
	}
}
