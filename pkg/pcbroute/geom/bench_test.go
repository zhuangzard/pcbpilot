package geom

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"testing"
	"time"
)

// PLAN.md M1 micro-bench: load all copper of a board, build the static index
// (≤ 50 ms) and run 10⁶ Collides queries on one core (≤ 1 s). The board is a
// JSON dump given by PCBPILOT_GEOM_BENCH_JSON (Gas V5 is commercial and not in
// the repository; the M3 DSN reader replaces the dump):
//
//	{"layers": 4, "bounds": [x0, y0, x1, y1],
//	 "entries": [{"l": layer (-1 all), "n": net, "k": "p"|"c"|"s", "v": [...]}]}
//
// with polygon vertices, circle x y r, or segment ax ay bx by halfWidth, in nm.
// Without the variable a synthetic board of the same size is used.

type benchBoard struct {
	layers  int
	bounds  Rect
	entries []Entry
}

func loadBenchBoard(t testing.TB, path string) benchBoard {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Layers  int      `json:"layers"`
		Bounds  [4]int64 `json:"bounds"`
		Entries []struct {
			L int     `json:"l"`
			N int     `json:"n"`
			K string  `json:"k"`
			V []int64 `json:"v"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	b := benchBoard{layers: doc.Layers, bounds: Rect{doc.Bounds[0], doc.Bounds[1], doc.Bounds[2], doc.Bounds[3]}}
	for i, e := range doc.Entries {
		var s Shape
		switch v := e.V; e.K {
		case "p":
			pts := make([]Pt, len(v)/2)
			for j := range pts {
				pts[j] = Pt{v[2*j], v[2*j+1]}
			}
			s = Poly{Pts: pts}
		case "c":
			s = Circle{C: Pt{v[0], v[1]}, R: v[2]}
		case "s":
			s = Seg{A: Pt{v[0], v[1]}, B: Pt{v[2], v[3]}, HalfW: v[4]}
		default:
			t.Fatalf("entry %d: unknown kind %q", i, e.K)
		}
		b.entries = append(b.entries, Entry{ID: uint32(i + 1), Net: NetID(e.N), Layer: LayerID(e.L), Shape: s})
	}
	return b
}

// syntheticBoard is 100 × 80 mm, 4 layers, about 1 100 pads, vias and keep-outs.
func syntheticBoard() benchBoard {
	rng := rand.New(rand.NewPCG(21, 22))
	b := benchBoard{layers: 4, bounds: Rect{0, 0, 100_000_000, 80_000_000}}
	add := func(l LayerID, n NetID, s Shape) {
		b.entries = append(b.entries, Entry{ID: uint32(len(b.entries) + 1), Net: n, Layer: l, Shape: s})
	}
	for i := 0; i < 800; i++ {
		x, y := 2_000_000+rng.Int64N(96_000_000), 2_000_000+rng.Int64N(76_000_000)
		add(0, NetID(1+rng.IntN(150)), Poly{Pts: []Pt{{x - 400_000, y - 450_000}, {x + 400_000, y - 450_000}, {x + 400_000, y + 450_000}, {x - 400_000, y + 450_000}}})
	}
	for i := 0; i < 65; i++ {
		c := Pt{2_000_000 + rng.Int64N(96_000_000), 2_000_000 + rng.Int64N(76_000_000)}
		for l := LayerID(0); l < 4; l++ {
			add(l, NetID(1+rng.IntN(150)), Circle{C: c, R: 300_000})
		}
	}
	for i := 0; i < 20; i++ {
		x := rng.Int64N(99_000_000)
		add(AllLayers, 0, Poly{Pts: []Pt{{x, 0}, {x + 500_000, 0}, {x + 500_000, 500_000}}})
	}
	return b
}

func benchBoardFor(t testing.TB) (benchBoard, string) {
	if p := os.Getenv("PCBPILOT_GEOM_BENCH_JSON"); p != "" {
		return loadBenchBoard(t, p), p
	}
	return syntheticBoard(), "synthetic"
}

type benchQuery struct {
	s     Seg
	layer LayerID
	net   NetID
}

// benchQueries are octilinear 0.254 mm tracks up to 3 mm long anywhere on the
// board; Collides runs them with a 0.2 mm clearance.
func benchQueries(b benchBoard, n int) []benchQuery {
	rng := rand.New(rand.NewPCG(23, 24))
	q := make([]benchQuery, n)
	w, h := b.bounds.MaxX-b.bounds.MinX, b.bounds.MaxY-b.bounds.MinY
	for i := range q {
		a := Pt{b.bounds.MinX + rng.Int64N(w), b.bounds.MinY + rng.Int64N(h)}
		q[i] = benchQuery{
			s:     Seg{A: a, B: a.Step(Dir8(rng.IntN(8)), rng.Int64N(3_000_000)), HalfW: 127_000},
			layer: LayerID(rng.IntN(b.layers)),
			net:   NetID(rng.IntN(150)),
		}
	}
	return q
}

const benchCell = 1_600_000 // 4 × (0.2 mm track + 0.2 mm clearance)

func TestBenchAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("micro-bench")
	}
	b, src := benchBoardFor(t)
	build := time.Duration(1 << 62)
	var ix *Index
	for i := 0; i < 5; i++ { // best of 5, to drop GC and cold-cache noise
		t0 := time.Now()
		ix = NewIndex(b.entries, b.bounds, benchCell)
		build = min(build, time.Since(t0))
	}
	const n = 1_000_000
	qs := benchQueries(b, n)
	t0 := time.Now()
	hits := 0
	for _, q := range qs {
		if ix.Collides(q.s, q.layer, q.net, 200_000) {
			hits++
		}
	}
	query := time.Since(t0)
	t.Logf("board %s: %d entries; static build %v; %d Collides in %v (%.0f ns/query, %d hits)",
		src, len(b.entries), build, n, query, float64(query.Nanoseconds())/n, hits)
	if build > 50*time.Millisecond {
		t.Errorf("static index build %v > 50 ms", build)
	}
	if query > time.Second {
		t.Errorf("10⁶ Collides took %v > 1 s", query)
	}
}

func BenchmarkIndexBuild(bm *testing.B) {
	b, _ := benchBoardFor(bm)
	for bm.Loop() {
		NewIndex(b.entries, b.bounds, benchCell)
	}
}

func BenchmarkCollides(bm *testing.B) {
	b, _ := benchBoardFor(bm)
	ix := NewIndex(b.entries, b.bounds, benchCell)
	qs := benchQueries(b, 1<<16)
	i := 0
	for bm.Loop() {
		q := qs[i&(len(qs)-1)]
		ix.Collides(q.s, q.layer, q.net, 200_000)
		i++
	}
}
