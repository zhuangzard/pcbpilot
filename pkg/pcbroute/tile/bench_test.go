package tile

import (
	"math/rand/v2"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/dsn"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// PLAN.md M6 micro-bench on the Gas Module V5 design (commercial, never
// committed): PCBPILOT_BENCH_DSN=<file.dsn>. Builds the track planes of every
// layer and every inflation key (≤ 300 ms total), then inserts single routed
// tracks into a built plane (≤ 50 µs median).
func TestBenchGasPlanes(t *testing.T) {
	path := os.Getenv("PCBPILOT_BENCH_DSN")
	if path == "" {
		t.Skip("PCBPILOT_BENCH_DSN not set")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	v, rb := &fakeView{}, rules.NewBuilder()
	if err := dsn.Read(src, v, rb); err != nil {
		t.Fatal(err)
	}
	// The M3 reader expands a plain (clearance) into every typed pair and
	// never sets rules.Generic, which M2's Build requires at the pcb scope.
	// Every pair already has a typed pcb value, so the Generic value is never
	// read; 0 only satisfies the check (open M2/M3 integration issue).
	rb.Set(rules.Scope{Kind: rules.ScopePCB}, rules.RuleSet{Clearance: map[rules.ClrType]int64{rules.Generic: 0}})
	rs, err := rb.Build()
	if err != nil {
		t.Fatal(err)
	}

	// Best of 3 builds, each from a fresh Set.
	var s *Set
	build := time.Duration(1 << 62)
	planes, tiles := 0, 0
	for range 3 {
		s = NewSet(v, rs)
		planes, tiles = 0, 0
		start := time.Now()
		for _, l := range rs.Layers() {
			for _, k := range s.Keys(l.ID) {
				tiles += s.Plane(l.ID, k).Len()
				planes++
			}
		}
		build = min(build, time.Since(start))
	}
	start := time.Now()
	viaPlanes, viaTiles := 0, 0
	seen := map[planeKey]bool{}
	for n := 1; n <= v.NumNets(); n++ {
		for _, via := range rs.Vias(geom.NetID(n)) {
			k := s.ViaKey(geom.NetID(n), via)
			if pk := (planeKey{Layer: via.From, Via: via.Name, Key: k}); !seen[pk] {
				seen[pk] = true
				viaTiles += s.ViaPlane(via, k).Len()
				viaPlanes++
			}
		}
	}
	viaBuild := time.Since(start)
	t.Logf("items=%d nets=%d layers=%d: %d track planes, %d tiles, build %v; %d via planes, %d tiles, build %v",
		len(v.items), v.NumNets(), len(rs.Layers()), planes, tiles, build, viaPlanes, viaTiles, viaBuild)

	// Incremental insert of one routed track of the default width into the
	// top-layer plane of the most common key, at random places on the board.
	net := geom.NetID(1)
	k := s.KeyOf(net, 0)
	p := s.Plane(0, k)
	rng := rand.New(rand.NewPCG(1, 6))
	b := s.Bounds().Grow(-2 * mm)
	var one, all []time.Duration
	for i := range 2000 {
		a := randPt(rng, b)
		d := geom.Dir8(rng.IntN(8))
		e := a.Step(d, 500_000+rng.Int64N(4*mm))
		if !b.Contains(e) {
			continue
		}
		id := uint32(1<<30 + i)
		o := Obstacle{ID: id, Net: 2, Shape: geom.Seg{A: a, B: e, HalfW: k.HalfWidth}, R: k.HalfWidth + s.clearance(s.planes[planeKey{Layer: 0, Key: k}], 2, rules.Wire, 0)}
		start := time.Now()
		p.Insert(o)
		one = append(one, time.Since(start))
		p.Delete(id)
		it := board.Item{ID: board.ItemID(id), Kind: board.Track, Net: 2, From: 0, To: 0, Shape: o.Shape}
		start = time.Now()
		s.Add(it)
		all = append(all, time.Since(start))
		s.Remove(it.ID)
	}
	med := func(ds []time.Duration) time.Duration {
		slices.Sort(ds)
		return ds[len(ds)/2]
	}
	t.Logf("insert one track: median %v (one plane, %d samples), median %v (every built plane of the layer, Set.Add)",
		med(one), len(one), med(all))
	if build > 300*time.Millisecond {
		t.Errorf("plane build %v > 300 ms", build)
	}
	if m := med(one); m > 50*time.Microsecond {
		t.Errorf("median insert %v > 50 µs", m)
	}
}
