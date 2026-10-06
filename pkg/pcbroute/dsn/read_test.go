package dsn

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

const mil = 25400 // nm

func readSmall(t *testing.T) (*recBoard, *recRules, []string) {
	t.Helper()
	src, err := os.ReadFile("testdata/small.dsn")
	if err != nil {
		t.Fatal(err)
	}
	b, r, w, err := load(src)
	if err != nil {
		t.Fatal(err)
	}
	return b, r, w
}

// pads returns the Pad items of one pin reference.
func pads(b *recBoard, ref string) []board.Item {
	var out []board.Item
	for _, it := range b.items {
		if it.Kind == board.Pad && it.Ref == ref {
			out = append(out, it)
		}
	}
	return out
}

func TestReadNetsLayersPads(t *testing.T) {
	b, r, _ := readSmall(t)
	if want := []string{"GND", "SIG", "NET 3"}; !slices.Equal(b.nets, want) {
		t.Fatalf("nets %q, want %q", b.nets, want)
	}
	var names []string
	for i, l := range r.layers {
		names = append(names, l.Name)
		if l.ID != geom.LayerID(i) || l.Outer != (i == 0 || i == 3) {
			t.Errorf("layer %d = %+v", i, l)
		}
	}
	if !slices.Equal(names, []string{"TopLayer", "Inner1", "Inner2", "BottomLayer"}) || r.layers[1].Kind != rules.Power {
		t.Fatalf("layers %+v", r.layers)
	}

	cases := []struct {
		ref  string
		want []board.Item
	}{
		{"U1-1", []board.Item{{Kind: board.Pad, Net: 1, From: 0, To: 3, Shape: geom.Circle{C: geom.Pt{X: 100 * mil, Y: 200 * mil}, R: 30 * mil}}}},
		// (rotate 90) turns the 40×20 rect upright.
		{"U1-2", []board.Item{{Kind: board.Pad, Net: 2, Shape: geom.Rect{MinX: 140 * mil, MinY: 180 * mil, MaxX: 160*mil + 1, MaxY: 220*mil + 1}}}},
		{"U1-3", []board.Item{{Kind: board.Pad, Net: 3, Shape: geom.Seg{A: geom.Pt{X: 90 * mil, Y: 250 * mil}, B: geom.Pt{X: 110 * mil, Y: 250 * mil}, HalfW: 10 * mil}}}},
		// Equal adjacent layers share one item.
		{"U1-4", []board.Item{
			{Kind: board.Pad, Net: 1, From: 0, To: 0, Shape: geom.Circle{C: geom.Pt{X: 50 * mil, Y: 200 * mil}, R: 30 * mil}},
			{Kind: board.Pad, Net: 1, From: 1, To: 2, Shape: geom.Circle{C: geom.Pt{X: 50 * mil, Y: 200 * mil}, R: 20 * mil}},
			{Kind: board.Pad, Net: 1, From: 3, To: 3, Shape: geom.Circle{C: geom.Pt{X: 50 * mil, Y: 200 * mil}, R: 30 * mil}},
		}},
		// Repeated and closing polygon vertices are dropped; no net.
		{"U1-5", []board.Item{{Kind: board.Pad, Shape: geom.Poly{Pts: []geom.Pt{{X: 90 * mil, Y: 140 * mil}, {X: 110 * mil, Y: 140 * mil}, {X: 110 * mil, Y: 160 * mil}, {X: 90 * mil, Y: 160 * mil}}}}}},
		// Back side, rotation 90: mirrored, rotated, moved to BottomLayer.
		{"U2-2", []board.Item{{Kind: board.Pad, Net: 2, From: 3, To: 3, Shape: geom.Rect{MinX: 480 * mil, MinY: 140 * mil, MaxX: 520*mil + 1, MaxY: 160*mil + 1}}}},
	}
	for _, c := range cases {
		got := pads(b, c.ref)
		for i := range got {
			got[i].ID = 0
		}
		for i := range c.want {
			c.want[i].Fixed, c.want[i].Ref = true, c.ref
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", c.ref, got, c.want)
		}
	}
	if pins, nets, conns := counts(b); pins != 10 || nets != 3 || conns != 2+1+1+1 { // GND 3 pins + plane
		t.Errorf("counts pins=%d nets=%d conns=%d", pins, nets, conns)
	}
}

func TestReadAreasAndWiring(t *testing.T) {
	b, r, _ := readSmall(t)
	var kinds []board.Kind
	var tracks, vias []board.Item
	for _, it := range b.items {
		switch it.Kind {
		case board.Pad:
			continue
		case board.Track:
			tracks = append(tracks, it)
		case board.Via:
			vias = append(vias, it)
		}
		kinds = append(kinds, it.Kind)
	}
	want := []board.Kind{board.Edge, board.Zone, board.Keepout, board.Track, board.Track, board.Track, board.Track, board.Via}
	if !slices.Equal(kinds, want) {
		t.Fatalf("non-pad kinds %v, want %v", kinds, want)
	}
	edge := b.items[0]
	if edge.From != geom.AllLayers || !reflect.DeepEqual(edge.Shape, geom.Poly{Pts: []geom.Pt{{X: 0, Y: 0}, {X: 1000 * mil, Y: 0}, {X: 1000 * mil, Y: 500 * mil}, {X: 0, Y: 500 * mil}}}) {
		t.Errorf("edge %+v", edge)
	}
	if z := b.items[1]; z.Net != 1 || z.From != 1 || z.To != 1 {
		t.Errorf("plane zone %+v", z)
	}
	fixed := []bool{true, true, true, false} // fix, fix, protect, route
	for i, tr := range tracks {
		if tr.Fixed != fixed[i] {
			t.Errorf("track %d Fixed=%v, want %v", i, tr.Fixed, fixed[i])
		}
	}
	if s := tracks[3].Shape.(geom.Seg); tracks[3].From != 2 || s.HalfW != 4*mil || tracks[3].Net != 2 {
		t.Errorf("route track %+v", tracks[3])
	}
	if v := vias[0]; !v.Fixed || v.From != 0 || v.To != 3 || v.Shape != (geom.Circle{C: geom.Pt{X: 150 * mil, Y: 250 * mil}, R: 12 * mil}) {
		t.Errorf("via %+v", v)
	}
	if len(r.keepouts) != 2 || r.keepouts[0].Kind != rules.KeepoutAll || r.keepouts[0].Layer != geom.AllLayers ||
		r.keepouts[1].Kind != rules.KeepoutVia || r.keepouts[1].Layer != 0 {
		t.Errorf("keepouts %+v", r.keepouts)
	}
	if !reflect.DeepEqual(r.vias, []rules.ViaType{{Name: "via0", Pad: 24 * mil, From: 0, To: 3}}) {
		t.Errorf("via catalogue %+v", r.vias)
	}
}

func i64(v int64) *int64 { return &v }

// TestReadRuleScopes checks the scope each DSN rule lands in (spec 04 §2).
func TestReadRuleScopes(t *testing.T) {
	_, r, warns := readSmall(t)
	if r.class[1] != 2 || r.class[2] != 3 || r.class[3] != 3 {
		t.Errorf("classes %v", r.class)
	}
	byKind := map[rules.ScopeKind][]setCall{}
	for _, s := range r.sets {
		byKind[s.s.Kind] = append(byKind[s.s.Kind], s)
	}
	pcb := byKind[rules.ScopePCB]
	if len(pcb) != 1 || *pcb[0].rs.Width != 10*mil || !slices.Equal(pcb[0].rs.UseVia, []string{"via0"}) {
		t.Fatalf("pcb scope %+v", pcb)
	}
	c := pcb[0].rs.Clearance
	for typ, want := range map[rules.ClrType]int64{
		{A: rules.Wire, B: rules.Wire}:                             6 * mil,
		{A: rules.Pin, B: rules.Via}:                               6 * mil,
		{A: rules.SMD, B: rules.Wire}:                              8 * mil, // default_smd beats the generic value
		{A: rules.Pin, B: rules.SMD}:                               8 * mil,
		{A: rules.SMD, B: rules.SMD}:                               10 * mil, // the exact pair beats default_smd
		{A: rules.SMD, B: rules.Via, Special: rules.SameNetSMDVia}: 3 * mil,
	} {
		if c[typ] != want {
			t.Errorf("pcb clearance %+v = %d, want %d", typ, c[typ], want)
		}
	}
	if len(c) != 22 {
		t.Errorf("pcb clearance has %d types, want 21 pairs + 1 special", len(c))
	}
	if !slices.ContainsFunc(warns, func(w string) bool { return strings.Contains(w, "buried_via_gap") }) {
		t.Errorf("no warning for buried_via_gap: %q", warns)
	}

	check := func(kind rules.ScopeKind, want []setCall) {
		t.Helper()
		got := byKind[kind]
		for i := range got {
			if len(got[i].rs.Clearance) == 0 {
				got[i].rs.Clearance = nil
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("scope %d:\n got %+v\nwant %+v", kind, got, want)
		}
	}
	all := func(v int64) map[rules.ClrType]int64 {
		var a clrAcc
		a.generic = &v
		return a.result()
	}
	check(rules.ScopeLayer, []setCall{{rules.Scope{Kind: rules.ScopeLayer, Layer: 1}, rules.RuleSet{Width: i64(12 * mil)}}})
	check(rules.ScopeClass, []setCall{
		{rules.Scope{Kind: rules.ScopeClass, Class: 1}, rules.RuleSet{Width: i64(10 * mil), Clearance: all(4 * mil), UseVia: []string{"via0"}}},
		{rules.Scope{Kind: rules.ScopeClass, Class: 2}, rules.RuleSet{Width: i64(20 * mil), Clearance: all(7 * mil), UseVia: []string{"via0"},
			ViaArray: &rules.ViaArraySpec{Template: "via0", Rows: 2, Cols: 3}}},
		{rules.Scope{Kind: rules.ScopeClass, Class: 3}, rules.RuleSet{Clearance: func() map[rules.ClrType]int64 {
			a := clrAcc{deflt: map[rules.ObjKind]int64{rules.SMD: 5 * mil}, pair: map[rules.ClrType]int64{{A: rules.Wire, B: rules.Wire}: 5 * mil}}
			return a.result()
		}()}},
	})
	check(rules.ScopeClassLayer, []setCall{
		{rules.Scope{Kind: rules.ScopeClassLayer, Class: 2, Layer: 1}, rules.RuleSet{Width: i64(40 * mil)}},
		{rules.Scope{Kind: rules.ScopeClassLayer, Class: 2, Layer: 2}, rules.RuleSet{Width: i64(40 * mil)}},
	})
	check(rules.ScopeNet, []setCall{{rules.Scope{Kind: rules.ScopeNet, Net: 1}, rules.RuleSet{Width: i64(25 * mil)}}})
	check(rules.ScopeNetLayer, []setCall{{rules.Scope{Kind: rules.ScopeNetLayer, Net: 1, Layer: 0}, rules.RuleSet{Width: i64(30 * mil)}}})
	check(rules.ScopeClassClass, []setCall{{rules.Scope{Kind: rules.ScopeClassClass, Class: 2, Class2: 3}, rules.RuleSet{Clearance: all(15 * mil)}}})
	region := rules.Scope{Kind: rules.ScopeRegion, Layer: geom.AllLayers, Net: 2,
		Region: geom.Rect{MinX: 500 * mil, MinY: 0, MaxX: 600*mil + 1, MaxY: 100*mil + 1}}
	check(rules.ScopeRegion, []setCall{{region, rules.RuleSet{Clearance: all(20 * mil)}}})
}

func TestReadDeterministic(t *testing.T) {
	b1, r1, w1 := readSmall(t)
	b2, r2, w2 := readSmall(t)
	if !reflect.DeepEqual(b1, b2) || !reflect.DeepEqual(r1, r2) || !slices.Equal(w1, w2) {
		t.Fatal("two reads of the same file differ")
	}
}

func TestReadErrors(t *testing.T) {
	for _, src := range []string{
		`(session x)`,
		`(PCB x (resolution parsec 1) (structure))`,
		`(PCB x)`,
		`(PCB x (structure (layer T (type signal))) (placement (component nope (place A 0 0 front 0))))`,
		`(PCB x (structure (layer T (type signal)) (rule (width abc))))`,
		`(PCB x (structure (layer T (type signal))) (wiring (via v 0 0)))`,
		`(PCB x (structure`,
	} {
		if _, _, _, err := load([]byte(src)); err == nil {
			t.Errorf("no error for %s", src)
		}
	}
}

// TestReadEasyEDAExport reads pcbpilot's recorded EasyEDA export, raw and
// after the bench's dsn-fix step.
func TestReadEasyEDAExport(t *testing.T) {
	raw, err := os.ReadFile("../../../internal/pcb/specctra/testdata/easyeda-export.dsn")
	if err != nil {
		t.Fatal(err)
	}
	fixed, _, err := specctra.FixDSN(string(raw), specctra.FixOptions{CopperLayers: 4, EdgeOuterMil: 20, EdgeInnerMil: 30})
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{"raw": string(raw), "fixed": fixed} {
		b, r, _, err := load([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if pins, nets, conns := counts(b); pins != 2 || nets != 1 || conns != 1 {
			t.Errorf("%s: pins=%d nets=%d conns=%d", name, pins, nets, conns)
		}
		if name == "fixed" && (len(r.layers) != 4 || r.layers[3].Name != "BottomLayer") {
			t.Errorf("fixed layers %+v", r.layers)
		}
	}
}

// TestBenchDSN is the M3 bench acceptance on a local design such as Gas
// Module V5 (never committed): PCBPILOT_BENCH_DSN=<file.dsn>. With
// PCBPILOT_BENCH_REPORT=<the reference router's JSON report> for the same
// file, the pin, net and connection counts must equal the report's
// (CLEANROOM §4).
func TestBenchDSN(t *testing.T) {
	path := os.Getenv("PCBPILOT_BENCH_DSN")
	if path == "" {
		t.Skip("PCBPILOT_BENCH_DSN not set")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b, r, warns, err := load(src)
	if err != nil {
		t.Fatal(err)
	}
	pins, nets, conns := counts(b)
	t.Logf("%s: layers=%d pins=%d nets=%d connections=%d items=%d scopes=%d warnings=%d",
		path, len(r.layers), pins, nets, conns, len(b.items), len(r.sets), len(warns))
	for _, w := range warns {
		t.Log("warning:", w)
	}
	if sp := os.Getenv("PCBPILOT_BENCH_SES"); sp != "" {
		benchSES(t, sp, b, r)
	}
	rp := os.Getenv("PCBPILOT_BENCH_REPORT")
	if rp == "" {
		return
	}
	raw, err := os.ReadFile(rp)
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Stats struct{ Layers, Nets, Pins, Connections int }
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	s := rep.Stats
	if s.Layers != len(r.layers) || s.Pins != pins || s.Nets != nets || s.Connections != conns {
		t.Errorf("reader layers/pins/nets/connections = %d/%d/%d/%d, report %d/%d/%d/%d",
			len(r.layers), pins, nets, conns, s.Layers, s.Pins, s.Nets, s.Connections)
	}
}

// benchSES loads a session routed on the bench design (PCBPILOT_BENCH_SES,
// another router's output used only as input, CLEANROOM §4), writes it as
// SES and checks that re-reading keeps every item longer than the SES step
// and that a second write is byte-identical.
func benchSES(t *testing.T, path string, b *recBoard, r *recRules) {
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := ReadSES(raw, b, r)
	if err != nil {
		t.Fatal(err)
	}
	routed := &recBoard{nets: b.nets}
	kept := 0 // tracks shorter than the SES step collapse and are dropped
	for _, it := range items {
		routed.AddItem(it)
		if s, ok := it.Shape.(geom.Seg); !ok || snapPt(s.A) != snapPt(s.B) {
			kept++
		}
	}
	out := writeSES(t, routed, r)
	again, err := ReadSES(out, b, r)
	if err != nil {
		t.Fatal(err)
	}
	reread := &recBoard{nets: b.nets}
	for _, it := range again {
		reread.AddItem(it)
	}
	if len(again) != kept || !bytes.Equal(out, writeSES(t, reread, r)) {
		t.Errorf("SES round trip: %d items (%d longer than the SES step) → %d, second write equal=%v",
			len(items), kept, len(again), bytes.Equal(out, writeSES(t, reread, r)))
	}
	t.Logf("%s: %d session items, %d kept, %d bytes rewritten", path, len(items), kept, len(out))
}
