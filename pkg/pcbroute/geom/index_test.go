package geom

import (
	"math/rand/v2"
	"slices"
	"testing"
)

func randBox(rng *rand.Rand, span, size int64) Rect {
	x, y := rng.Int64N(span)-span/2, rng.Int64N(span)-span/2
	return Rect{x, y, x + 1 + rng.Int64N(size), y + 1 + rng.Int64N(size)}
}

func TestRTreeEqualsLinearScan(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	for _, n := range []int{0, 1, 15, 16, 17, 300, 5000} {
		boxes := make([]Rect, n)
		for i := range boxes {
			boxes[i] = randBox(rng, 1_000_000, 20_000)
		}
		tr := NewRTree(boxes)
		for q := 0; q < 300; q++ {
			r := randBox(rng, 1_200_000, 100_000)
			var got, want []int32
			tr.Search(r, func(i int32) bool { got = append(got, i); return true })
			for i, b := range boxes {
				if b.Intersects(r) {
					want = append(want, int32(i))
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("n=%d query %v: tree %v, scan %v", n, r, got, want)
			}
		}
	}
}

func TestGridEqualsLinearScan(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	g := NewGrid(Rect{0, 0, 1_000_000, 800_000}, 50_000)
	live := map[int32]Rect{}
	for step := 0; step < 20_000; step++ {
		if len(live) > 0 && rng.IntN(3) == 0 {
			keys := make([]int32, 0, len(live))
			for s := range live {
				keys = append(keys, s)
			}
			slices.Sort(keys)
			s := keys[rng.IntN(len(keys))]
			g.Delete(s)
			delete(live, s)
		} else {
			// Some boxes stick out of the grid's area.
			b := randBox(rng, 1_400_000, 120_000)
			b.MinX += 500_000
			b.MaxX += 500_000
			s := g.Insert(b)
			if _, dup := live[s]; dup {
				t.Fatalf("slot %d handed out twice", s)
			}
			live[s] = b
		}
		if step%50 != 0 {
			continue
		}
		r := randBox(rng, 1_600_000, 300_000)
		r.MinX += 500_000
		r.MaxX += 500_000
		var got, want []int32
		g.Search(r, func(s int32) bool { got = append(got, s); return true })
		for s, b := range live {
			if b.Intersects(r) {
				want = append(want, s)
			}
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("step %d query %v: grid %v, scan %v", step, r, got, want)
		}
	}
}

func TestIndexCollidesNetFilter(t *testing.T) {
	pad := Entry{ID: 1, Net: 7, Layer: 0, Shape: Rect{0, 0, 1000, 1000}}
	th := Entry{ID: 2, Net: 8, Layer: AllLayers, Shape: Circle{C: Pt{5000, 0}, R: 500}}
	ko := Entry{ID: 3, Net: 0, Layer: 1, Shape: Rect{0, 3000, 1000, 4000}}
	ix := NewIndex([]Entry{pad, th, ko}, Rect{-10_000, -10_000, 10_000, 10_000}, 2_000)
	ix.Insert(Entry{ID: 10, Net: 9, Layer: 1, Shape: Seg{A: Pt{-5000, -2000}, B: Pt{5000, -2000}, HalfW: 100}})

	probe := Seg{A: Pt{1200, -500}, B: Pt{1200, 500}, HalfW: 100} // 101 nm right of the pad
	cases := []struct {
		s     Shape
		layer LayerID
		net   NetID
		r     int64
		want  bool
	}{
		{probe, 0, 9, 102, true},                             // foreign pad closer than 102
		{probe, 0, 9, 101, false},                            // exactly 101 away is clear
		{probe, 0, 7, 500, false},                            // own net is never an obstacle
		{probe, 1, 9, 500, false},                            // the pad is on layer 0 only
		{probe, 0, 0, 102, true},                             // net 0 query sees everything
		{Circle{C: Pt{5000, 700}, R: 100}, 3, 9, 101, true},  // through-hole seen from any layer
		{Circle{C: Pt{5000, 700}, R: 100}, 3, 8, 101, false}, // ... but not by its own net
		{Circle{C: Pt{500, 2800}, R: 100}, 1, 7, 101, true},  // net 0 keep-out blocks every net
		{Circle{C: Pt{0, -1800}, R: 50}, 1, 7, 51, true},     // routed copper
		{Circle{C: Pt{0, -1800}, R: 50}, 1, 9, 51, false},
		{Circle{C: Pt{0, -1800}, R: 50}, AllLayers, 7, 51, true}, // an all-layer query sees layer 1
	}
	for i, c := range cases {
		if got := ix.Collides(c.s, c.layer, c.net, c.r); got != c.want {
			t.Errorf("case %d: Collides(%v, layer %d, net %d, %d) = %v, want %v", i, c.s, c.layer, c.net, c.r, got, c.want)
		}
	}
	// Pairwise clearance: the keep-out needs more than the default.
	clr := func(e Entry) int64 {
		if e.ID == 3 {
			return 400
		}
		return 50
	}
	if ix.Clear(Circle{C: Pt{500, 2700}, R: 0}, 1, 7, 400, clr) {
		t.Error("Clear ignored the larger pair clearance")
	}
	if !ix.Clear(Circle{C: Pt{500, 2700}, R: 0}, 1, 7, 400, func(Entry) int64 { return 300 }) {
		t.Error("Clear with clearance 300 at distance 300")
	}
	if !ix.Delete(10, 1) || ix.Delete(10, 1) {
		t.Error("Delete of routed copper")
	}
	if ix.Collides(Circle{C: Pt{0, -1800}, R: 50}, 1, 7, 51) {
		t.Error("deleted copper still collides")
	}
}

// Random boards: Collides equals a brute-force scan over all entries, through
// inserts and deletes, and Nearby returns every entry exactly once.
func TestIndexEqualsBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 14))
	const layers = 3
	var fixed []Entry
	for i := 0; i < 2000; i++ {
		l := LayerID(rng.IntN(layers+1)) - 1
		fixed = append(fixed, Entry{ID: uint32(i + 1), Net: NetID(rng.IntN(20)), Layer: l,
			Shape: randShape(rng, Pt{rng.Int64N(50_000_000), rng.Int64N(40_000_000)}, 400_000, rng.IntN(5))})
	}
	ix := NewIndex(fixed, Rect{0, 0, 50_000_000, 40_000_000}, 1_600_000)
	all := slices.Clone(fixed)
	next := uint32(10_000)
	steps := 3000
	if testing.Short() {
		steps = 600
	}
	for step := 0; step < steps; step++ {
		if rng.IntN(4) > 0 {
			e := Entry{ID: next, Net: NetID(1 + rng.IntN(20)), Layer: LayerID(rng.IntN(layers)),
				Shape: randShape(rng, Pt{rng.Int64N(52_000_000), rng.Int64N(42_000_000)}, 300_000, 1+rng.IntN(2))}
			next++
			ix.Insert(e)
			all = append(all, e)
		} else if len(all) > len(fixed) {
			k := len(fixed) + rng.IntN(len(all)-len(fixed))
			if !ix.Delete(all[k].ID, all[k].Layer) {
				t.Fatal("Delete of a live entry failed")
			}
			all = slices.Delete(all, k, k+1)
		}
		s := randShape(rng, Pt{rng.Int64N(50_000_000), rng.Int64N(40_000_000)}, 300_000, rng.IntN(5))
		layer := LayerID(rng.IntN(layers))
		net := NetID(rng.IntN(20))
		r := 1 + rng.Int64N(500_000)
		want := false
		for _, e := range all {
			if (e.Layer == layer || e.Layer == AllLayers) && (net == 0 || e.Net != net) && Within(s, e.Shape, r) {
				want = true
				break
			}
		}
		if got := ix.Collides(s, layer, net, r); got != want {
			t.Fatalf("step %d: Collides(%v, %d, %d, %d) = %v, brute force %v", step, s, layer, net, r, got, want)
		}
		q := s.Bounds().Grow(r)
		seen := map[[2]int64]int{}
		ix.Nearby(q, layer, func(e Entry) bool { seen[[2]int64{int64(e.ID), int64(e.Layer)}]++; return true })
		n := 0
		for _, e := range all {
			if (e.Layer == layer || e.Layer == AllLayers) && e.Shape.Bounds().Intersects(q) {
				n++
				if seen[[2]int64{int64(e.ID), int64(e.Layer)}] != 1 {
					t.Fatalf("step %d: Nearby reported entry %d %d times", step, e.ID, seen[[2]int64{int64(e.ID), int64(e.Layer)}])
				}
			}
		}
		if n != len(seen) {
			t.Fatalf("step %d: Nearby reported %d entries, want %d", step, len(seen), n)
		}
	}
}

// The same operations give the same result order (spec 01 §5 determinism).
func TestIndexDeterministicOrder(t *testing.T) {
	build := func() []uint32 {
		rng := rand.New(rand.NewPCG(15, 16))
		var fixed []Entry
		for i := 0; i < 500; i++ {
			fixed = append(fixed, Entry{ID: uint32(i), Net: 1, Layer: LayerID(rng.IntN(2)),
				Shape: randShape(rng, Pt{rng.Int64N(10_000_000), rng.Int64N(10_000_000)}, 200_000, rng.IntN(5))})
		}
		ix := NewIndex(fixed, Rect{}, 500_000)
		for i := 0; i < 500; i++ {
			ix.Insert(Entry{ID: uint32(1000 + i), Net: 2, Layer: LayerID(rng.IntN(2)),
				Shape: randShape(rng, Pt{rng.Int64N(10_000_000), rng.Int64N(10_000_000)}, 200_000, 1)})
			if i%3 == 0 {
				ix.Delete(uint32(1000+i/2), LayerID(0))
			}
		}
		var ids []uint32
		ix.Nearby(Rect{0, 0, 10_000_000, 10_000_000}, AllLayers, func(e Entry) bool { ids = append(ids, e.ID); return true })
		return ids
	}
	a, b := build(), build()
	if !slices.Equal(a, b) {
		t.Fatal("Nearby order differs between identical runs")
	}
}
