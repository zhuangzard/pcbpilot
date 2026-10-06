package rules

import (
	"math/rand"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

const mm = 1_000_000 // nm

// fourLayer is a 4-layer book: 35 µm outer, 17.5 µm inner, 0.1 mm fab
// minimum, pcb width 0.2 mm and clearance 0.15 mm.
func fourLayer() *Book {
	b := NewBuilder()
	for i, name := range []string{"TOP", "IN1", "IN2", "BOTTOM"} {
		outer := i == 0 || i == 3
		cu := 17.5
		if outer {
			cu = 35
		}
		b.AddLayer(Layer{ID: geom.LayerID(i), Name: name, Outer: outer, CopperUm: cu, MinWidth: mm / 10})
	}
	b.Set(Scope{Kind: ScopePCB}, RuleSet{Width: ptr(mm / 5), Clearance: map[ClrType]int64{Generic: 150_000}})
	return b
}

func build(t testing.TB, b *Book) *table {
	t.Helper()
	r, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return r.(*table)
}

// 04-T12: pcb width 0.2, class 0.15, class layer_rule inner 0.25, net
// override 0.3.
func TestDSNPrecedenceT12(t *testing.T) {
	b := fourLayer()
	const cls ClassID = 1
	b.SetClass(1, cls)
	b.SetClass(2, cls)
	b.Set(Scope{Kind: ScopeClass, Class: cls}, RuleSet{Width: ptr(150_000)})
	for _, l := range []geom.LayerID{1, 2} {
		b.Set(Scope{Kind: ScopeClassLayer, Class: cls, Layer: l}, RuleSet{Width: ptr(250_000)})
	}
	b.Set(Scope{Kind: ScopeNet, Net: 1}, RuleSet{Width: ptr(300_000)})
	r := build(t, b)
	for l := geom.LayerID(0); l < 4; l++ {
		if w := r.Width(1, l); w != 300_000 {
			t.Errorf("net override on layer %d: %d, want 300000", l, w)
		}
		want := int64(150_000)
		if l == 1 || l == 2 {
			want = 250_000
		}
		if w := r.Width(2, l); w != want {
			t.Errorf("class member on layer %d: %d, want %d", l, w, want)
		}
		if w := r.Width(3, l); w != 200_000 {
			t.Errorf("unclassed net on layer %d: %d, want pcb 200000", l, w)
		}
	}
}

func TestLayerMinWidthFloor(t *testing.T) {
	b := fourLayer()
	b.Set(Scope{Kind: ScopeNet, Net: 1}, RuleSet{Width: ptr(50_000)})
	if w := build(t, b).Width(1, 0); w != mm/10 {
		t.Fatalf("width %d below the fab minimum", w)
	}
}

func TestBuildErrors(t *testing.T) {
	cases := map[string]func(*Book){
		"no layers": func(b *Book) { b.layers = nil },
		"no width":  func(b *Book) { b.scopes[scopeKey{kind: ScopePCB}].Width = nil },
		"no generic clearance": func(b *Book) {
			b.scopes[scopeKey{kind: ScopePCB}].Clearance = map[ClrType]int64{{A: Wire, B: Wire}: 1}
		},
		"duplicate layer": func(b *Book) { b.AddLayer(Layer{ID: 0}) },
		"unknown via": func(b *Book) {
			b.Set(Scope{Kind: ScopeNet, Net: 1}, RuleSet{UseVia: []string{"nope"}})
		},
		"duplicate via": func(b *Book) { b.AddVia(ViaType{Name: "v"}); b.AddVia(ViaType{Name: "v"}) },
	}
	for name, mod := range cases {
		b := fourLayer()
		mod(b)
		if _, err := b.Build(); err == nil {
			t.Errorf("%s: Build succeeded", name)
		}
	}
}

func TestBuildFreezesBook(t *testing.T) {
	b := fourLayer()
	r := build(t, b)
	b.Set(Scope{Kind: ScopePCB}, RuleSet{Width: ptr(mm)})
	if w := r.Width(1, 0); w != mm/5 {
		t.Fatalf("resolver changed with the book: %d", w)
	}
}

// 04-T8 (width part): a 3 A net on 35 µm outer / 17.5 µm inner copper.
func TestIntentWidthT8(t *testing.T) {
	b := fourLayer()
	b.Set(Scope{Kind: ScopeNet, Net: 1}, RuleSet{Width: ptr(250_000), MinWidth: ptr(150_000)})
	b.SetIntent(1, Intent{CurrentA: 3})
	r := build(t, b)
	outer, inner := r.Width(1, 0), r.Width(1, 1)
	if want := IntentWidth(3, 10, 35, true); outer != want {
		t.Errorf("outer width %d, want IPC %d", outer, want)
	}
	if want := IntentWidth(3, 10, 17.5, false); inner != want || inner <= outer {
		t.Errorf("inner width %d, want IPC %d (> outer %d)", inner, want, outer)
	}
	if f := r.IntentFloors(1, 0); f.Width != outer || f.Clearance != 0 {
		t.Errorf("floors %+v", f)
	}
	if w := b.Warnings(); len(w) != 1 || !strings.Contains(w[0], "intent floor") {
		t.Errorf("want one conflict warning, got %q", w)
	}
	n := r.Neck(1, 0, PadSize{Long: 600_000, Narrow: 300_000})
	if n.MinWidth < IntentWidth(3, 20, 35, true) || n.MinWidth > outer || n.Zone != 600_000 {
		t.Errorf("neck %+v (outer width %d)", n, outer)
	}
}

// An intent floor never lowers an explicit rule.
func TestIntentFloorNeverLowers(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := range 200 {
		b := fourLayer()
		w := int64(rng.Intn(5*mm) + mm/10)
		c := int64(rng.Intn(3*mm) + mm/10)
		in := Intent{CurrentA: rng.Float64() * 5, VoltageV: rng.Float64() * 600, VoltageKnown: rng.Intn(2) == 0}
		b.Set(Scope{Kind: ScopeNet, Net: 1}, RuleSet{Width: ptr(w), Clearance: map[ClrType]int64{Generic: c}})
		b.SetIntent(1, in)
		r := build(t, b)
		for l := range geom.LayerID(4) {
			if got := r.Width(1, l); got != max(w, r.IntentFloors(1, l).Width) {
				t.Fatalf("case %d layer %d: width %d, explicit %d, floor %d", i, l, got, w, r.IntentFloors(1, l).Width)
			}
			got := r.Clearance(Obj{Kind: Wire, Net: 1}, Obj{Kind: Wire, Net: 2}, l)
			if got < c || got < r.IntentFloors(1, l).Clearance {
				t.Fatalf("case %d layer %d: clearance %d < explicit %d or floor %d", i, l, got, c, r.IntentFloors(1, l).Clearance)
			}
		}
	}
}

func TestIntentScopeIsFloor(t *testing.T) {
	b := fourLayer()
	b.Set(Scope{Kind: ScopeNet, Net: 1}, RuleSet{Width: ptr(mm)})
	b.Set(Scope{Kind: ScopeIntent, Net: 1, Layer: geom.AllLayers}, RuleSet{Width: ptr(mm / 2)})
	b.Set(Scope{Kind: ScopeIntent, Net: 1, Layer: 3}, RuleSet{Width: ptr(2 * mm)})
	r := build(t, b)
	if r.Width(1, 0) != mm || r.Width(1, 3) != 2*mm {
		t.Fatalf("widths %d %d", r.Width(1, 0), r.Width(1, 3))
	}
}

func TestVias(t *testing.T) {
	b := fourLayer()
	small := ViaType{Name: "small", Pad: 450_000, Drill: 200_000, To: 3}
	big := ViaType{Name: "big", Pad: 800_000, Drill: 400_000, To: 3}
	b.AddVia(small)
	b.AddVia(big)
	b.Set(Scope{Kind: ScopeClass, Class: 1}, RuleSet{UseVia: []string{"small", "big"}})
	b.SetClass(1, 1)
	b.SetClass(2, 1)
	b.SetIntent(2, Intent{CurrentA: 5})
	r := build(t, b)
	if v := r.Vias(3); len(v) != 2 || v[0] != small {
		t.Errorf("default catalogue %v", v)
	}
	if v := r.Vias(1); len(v) != 2 || v[0] != small || v[1] != big {
		t.Errorf("use_via order %v", v)
	}
	if v := r.Vias(2); len(v) != 2 || v[0] != big {
		t.Errorf("high-current net should try the largest via first: %v", v)
	}
}

func TestEdgeAndKeepouts(t *testing.T) {
	b := fourLayer()
	if r := build(t, b); r.Edge() != DefaultEdge {
		t.Errorf("default edge %d", r.Edge())
	}
	b.SetEdge(508_000)
	k1 := Keepout{Shape: geom.Rect{MaxX: 1, MaxY: 1}, Layer: geom.AllLayers}
	k2 := Keepout{Shape: geom.Rect{MaxX: 2, MaxY: 2}, Layer: 3, Kind: KeepoutVia}
	b.AddKeepout(k1)
	b.AddKeepout(k2)
	r := build(t, b)
	if r.Edge() != 508_000 {
		t.Errorf("edge %d", r.Edge())
	}
	if len(r.Keepouts(0)) != 1 || len(r.Keepouts(3)) != 2 || len(r.Keepouts(geom.AllLayers)) != 2 {
		t.Errorf("keepouts %v %v", r.Keepouts(0), r.Keepouts(3))
	}
}
