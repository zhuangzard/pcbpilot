package rules

import (
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

func wire(n geom.NetID) Obj { return Obj{Kind: Wire, Net: n} }

func TestClearanceScopes(t *testing.T) {
	b := fourLayer()
	b.SetClass(1, 1)
	b.SetClass(2, 2)
	b.SetClass(3, 1)
	b.Set(Scope{Kind: ScopeClass, Class: 1}, RuleSet{Clearance: map[ClrType]int64{Generic: 200_000, {A: Via, B: Wire}: 220_000}})
	b.Set(Scope{Kind: ScopeClass, Class: 2}, RuleSet{Clearance: map[ClrType]int64{Generic: 300_000}})
	b.Set(Scope{Kind: ScopeClassLayer, Class: 2, Layer: 1}, RuleSet{Clearance: map[ClrType]int64{Generic: 350_000}})
	b.Set(Scope{Kind: ScopeNet, Net: 3}, RuleSet{Clearance: map[ClrType]int64{Generic: 400_000}})
	b.Set(Scope{Kind: ScopeLayer, Layer: 3}, RuleSet{Clearance: map[ClrType]int64{{A: SMD, B: Wire}: 120_000}})
	b.Set(Scope{Kind: ScopeClassClass, Class: 2, Class2: 1}, RuleSet{Clearance: map[ClrType]int64{{A: Wire, B: Wire}: 500_000}})
	b.Set(Scope{Kind: ScopeClassClassLayer, Class: 1, Class2: 2, Layer: 2}, RuleSet{Clearance: map[ClrType]int64{Generic: 600_000}})
	b.AddVia(ViaType{Name: "v"})
	b.Set(Scope{Kind: ScopeNet, Net: 4}, RuleSet{Clearance: map[ClrType]int64{{Special: SameNetViaVia}: 250_000}})
	r := build(t, b)
	cases := []struct {
		name  string
		a, b  Obj
		layer geom.LayerID
		want  int64
	}{
		{"pcb generic", wire(5), wire(6), 0, 150_000},
		{"layer typed", Obj{Kind: SMD, Net: 5}, wire(6), 3, 120_000},
		{"layer typed, other type", Obj{Kind: Via, Net: 5}, wire(6), 3, 150_000},
		{"class vs pcb: max of sides", wire(1), wire(5), 0, 200_000},
		{"class typed beats class generic", Obj{Kind: Via, Net: 1}, wire(5), 0, 220_000},
		{"class+layer", wire(2), wire(5), 1, 350_000},
		{"net beats class", wire(3), wire(5), 0, 400_000},
		{"same class, net side larger", wire(3), wire(1), 0, 400_000},
		{"class_class typed", wire(1), wire(2), 0, 500_000},
		{"class_class generic for other types", Obj{Kind: Via, Net: 1}, wire(2), 0, 300_000},
		{"class_class+layer", Obj{Kind: Pin, Net: 2}, Obj{Kind: Via, Net: 1}, 2, 600_000},
		{"same net wire", wire(1), Obj{Kind: Via, Net: 1}, 0, 0},
		{"same net via-via", Obj{Kind: Via, Net: 4}, Obj{Kind: Via, Net: 4}, 0, 250_000},
		{"same net smd-via unset", Obj{Kind: SMD, Net: 4}, Obj{Kind: Via, Net: 4}, 0, 0},
		{"net 0 is foreign to net 0", wire(0), wire(0), 0, 150_000},
	}
	for _, c := range cases {
		for _, swap := range []bool{false, true} {
			a, bb := c.a, c.b
			if swap {
				a, bb = bb, a
			}
			if got := r.Clearance(a, bb, c.layer); got != c.want {
				t.Errorf("%s (swap %v): %d, want %d", c.name, swap, got, c.want)
			}
		}
	}
	if got := r.Clearance(wire(2), wire(5), geom.AllLayers); got != 350_000 {
		t.Errorf("AllLayers: %d, want the layer maximum 350000", got)
	}
}

// 04-T10: a 230 VAC net next to a 3.3 V net.
func TestVoltageClearanceT10(t *testing.T) {
	b := fourLayer()
	b.SetIntent(1, Intent{VoltageV: 325, VoltageKnown: true, Mains: true}) // 230 VAC peak
	b.SetIntent(2, Intent{VoltageV: 3.3, VoltageKnown: true})
	b.SetIntent(3, Intent{VoltageV: 230, VoltageKnown: true, Mains: true})
	r := build(t, b)
	// |325 − 3.3| = 321.7 V: the 301–500 V band.
	if got := r.Clearance(wire(1), wire(2), 0); got != 2_500_000 {
		t.Errorf("outer 325 V vs 3.3 V: %d, want B2 2.5 mm", got)
	}
	// 226.7 V: the 151–300 V band, B2 1.25 mm outer, B1 0.2 mm inner.
	if got := r.Clearance(wire(3), wire(2), 0); got != 1_250_000 {
		t.Errorf("outer 230 V vs 3.3 V: %d, want 1.25 mm", got)
	}
	if got := r.Clearance(wire(3), wire(2), 1); got != 200_000 {
		t.Errorf("inner 230 V vs 3.3 V: %d, want B1 0.2 mm", got)
	}
	// Unknown voltage on the other side: max(|Va|, |Vb|).
	if got := r.Clearance(wire(3), wire(9), 0); got != 1_250_000 {
		t.Errorf("230 V vs unknown: %d", got)
	}
	// Two 3.3 V nets: 0 V apart, the board rule stays.
	b.SetIntent(4, Intent{VoltageV: 3.3, VoltageKnown: true})
	r = build(t, b)
	if got := r.Clearance(wire(4), wire(2), 0); got != 150_000 {
		t.Errorf("3.3 V vs 3.3 V: %d", got)
	}
	// Keep-outs are not conductors: no voltage floor.
	if got := r.Clearance(wire(3), Obj{Kind: Area}, 0); got != 150_000 {
		t.Errorf("230 V vs area: %d", got)
	}
	flagged := 0
	for _, w := range b.Warnings() {
		if strings.Contains(w, "review") {
			flagged++
		}
	}
	if flagged != 2 {
		t.Errorf("want 2 mains nets flagged, got %q", b.Warnings())
	}
	b.SetIntent(3, Intent{VoltageV: 230, VoltageKnown: true, Coated: true})
	if got := build(t, b).Clearance(wire(3), wire(2), 0); got != 400_000 {
		t.Errorf("coated 230 V: %d, want B4 0.4 mm", got)
	}
}

func TestRegionOverride(t *testing.T) {
	b := fourLayer()
	b.SetClass(2, 7)
	b.Set(Scope{Kind: ScopeRegion, Region: geom.Rect{MinX: 0, MinY: 0, MaxX: mm, MaxY: mm}},
		RuleSet{Clearance: map[ClrType]int64{Generic: 100_000}})
	b.Set(Scope{Kind: ScopeRegion, Region: geom.Circle{C: geom.Pt{X: 5 * mm, Y: 5 * mm}, R: mm}, Class: 7},
		RuleSet{Clearance: map[ClrType]int64{Generic: 900_000}})
	r := build(t, b)
	in := Obj{Kind: Wire, Net: 1, At: geom.Pt{X: mm / 2, Y: mm / 2}}
	out := Obj{Kind: Wire, Net: 3, At: geom.Pt{X: 3 * mm, Y: 3 * mm}}
	if got := r.Clearance(in, out, 0); got != 100_000 {
		t.Errorf("one object inside the region: %d", got)
	}
	if got := r.Clearance(out, Obj{Kind: Wire, Net: 4, At: geom.Pt{X: 2 * mm}}, 0); got != 150_000 {
		t.Errorf("outside: %d", got)
	}
	c2 := Obj{Kind: Wire, Net: 2, At: geom.Pt{X: 5 * mm, Y: 5 * mm}}
	if got := r.Clearance(c2, out, 0); got != 900_000 {
		t.Errorf("class region: %d", got)
	}
	if got := r.Clearance(Obj{Kind: Wire, Net: 8, At: c2.At}, out, 0); got != 150_000 {
		t.Errorf("class region must not apply to another class: %d", got)
	}
}

func TestContains(t *testing.T) {
	sq := geom.Poly{Pts: []geom.Pt{{X: 0, Y: 0}, {X: 10, Y: 0}, {X: 10, Y: 10}, {X: 0, Y: 10}}}
	for _, c := range []struct {
		s    geom.Shape
		p    geom.Pt
		want bool
	}{
		{sq, geom.Pt{X: 5, Y: 5}, true}, {sq, geom.Pt{X: 10, Y: 5}, true}, {sq, geom.Pt{X: 11, Y: 5}, false},
		{geom.Seg{A: geom.Pt{}, B: geom.Pt{X: 10}, HalfW: 2}, geom.Pt{X: 5, Y: 2}, true},
		{geom.Seg{A: geom.Pt{}, B: geom.Pt{X: 10}, HalfW: 2}, geom.Pt{X: 13, Y: 0}, false},
		{geom.Rect{MaxX: 10, MaxY: 10}, geom.Pt{X: 10, Y: 0}, false},
	} {
		if got := contains(c.s, c.p); got != c.want {
			t.Errorf("contains(%v, %v) = %v", c.s, c.p, got)
		}
	}
}

// The cache returns what the uncached computation does, also under
// concurrent readers (run with -race).
func TestClearanceCacheMatchesCompute(t *testing.T) {
	b := fourLayer()
	rng := rand.New(rand.NewSource(2))
	for n := geom.NetID(1); n <= 40; n++ {
		b.SetClass(n, ClassID(rng.Intn(4)))
		if rng.Intn(4) == 0 {
			b.Set(Scope{Kind: ScopeNet, Net: n}, RuleSet{Clearance: map[ClrType]int64{Generic: int64(rng.Intn(4)) * 100_000}})
		}
		if rng.Intn(4) == 0 {
			b.SetIntent(n, Intent{VoltageV: float64(rng.Intn(400)), VoltageKnown: rng.Intn(2) == 0})
		} else if rng.Intn(3) == 0 {
			b.SetIntent(n, Intent{Coated: true}) // coating alone changes the floor against a known voltage
		}
	}
	b.Set(Scope{Kind: ScopeClassClass, Class: 1, Class2: 3}, RuleSet{Clearance: map[ClrType]int64{{A: Pin, B: Via}: 333_000}})
	r := build(t, b)
	type q struct {
		a, b Obj
		l    geom.LayerID
	}
	qs := make([]q, 2000)
	for i := range qs {
		qs[i] = q{Obj{Kind: ObjKind(rng.Intn(numKinds)), Net: geom.NetID(rng.Intn(45))},
			Obj{Kind: ObjKind(rng.Intn(numKinds)), Net: geom.NetID(rng.Intn(45))}, geom.LayerID(rng.Intn(4))}
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, x := range qs {
				got := r.Clearance(x.a, x.b, x.l)
				ct := clrType(x.a.Kind, x.b.Kind, x.a.Net != 0 && x.a.Net == x.b.Net)
				want := int64(0)
				if !(x.a.Net != 0 && x.a.Net == x.b.Net) || ct.Special != SpecialNone {
					want = r.compute(x.a, x.b, r.info(x.a.Net), r.info(x.b.Net), ct, x.l)
				}
				if got != want {
					t.Errorf("%+v: cached %d, computed %d", x, got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// Cached lookups must take < 50 ns (04 §6).
func BenchmarkClearanceCached(b *testing.B) {
	bk := fourLayer()
	for n := geom.NetID(1); n <= 400; n++ {
		bk.SetClass(n, ClassID(n%30))
		if n%7 == 0 {
			bk.SetIntent(n, Intent{VoltageV: float64(n), VoltageKnown: true})
		}
	}
	r := build(b, bk)
	objs := make([]Obj, 1024)
	for i := range objs {
		objs[i] = Obj{Kind: ObjKind(i % 4), Net: geom.NetID(i%400 + 1)}
	}
	for i := 0; i < len(objs); i++ { // warm up
		for l := range geom.LayerID(4) {
			r.Clearance(objs[i], objs[(i*7+3)%len(objs)], l)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		j := i & 1023
		r.Clearance(objs[j], objs[(j*7+3)&1023], geom.LayerID(i&3))
	}
}

func BenchmarkWidth(b *testing.B) {
	bk := fourLayer()
	for n := geom.NetID(1); n <= 400; n++ {
		bk.SetIntent(n, Intent{CurrentA: float64(n) / 100})
	}
	r := build(b, bk)
	for i := 0; i < b.N; i++ {
		r.Width(geom.NetID(i%400+1), geom.LayerID(i&3))
	}
}
