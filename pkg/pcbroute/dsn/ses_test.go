package dsn

import (
	"bytes"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// sesBoard is a small routed 2-layer board with coordinates off the SES grid.
func sesBoard() (*recBoard, *recRules) {
	pt := func(x, y int64) geom.Pt { return geom.Pt{X: x, Y: y} }
	pad := func(net geom.NetID, ref string, c geom.Pt, from, to geom.LayerID) board.Item {
		return board.Item{Kind: board.Pad, Net: net, From: from, To: to, Shape: geom.Circle{C: c, R: 300_000}, Fixed: true, Ref: ref}
	}
	tr := func(net geom.NetID, l geom.LayerID, a, b geom.Pt) board.Item {
		return board.Item{Kind: board.Track, Net: net, From: l, To: l, Shape: geom.Seg{A: a, B: b, HalfW: 127_000}}
	}
	via := func(net geom.NetID, c geom.Pt, r int64) board.Item {
		return board.Item{Kind: board.Via, Net: net, From: 0, To: 1, Shape: geom.Circle{C: c, R: r}}
	}
	p1, p2 := pt(1_000_037, 2_000_049), pt(5_000_011, 2_000_000)
	p3, p4 := pt(1_000_000, 6_000_000), pt(9_000_000, 6_000_000)
	j, v1, v2 := pt(3_000_051, 2_000_049), pt(5_000_000, 6_000_000), pt(1_000_000, 8_000_000)
	b := &recBoard{nets: []string{"GND", "SIG A"}}
	for _, it := range []board.Item{
		pad(1, "U1-1", p1, 0, 1), pad(1, "U1-2", p2, 0, 0), pad(2, "U2-1", p3, 0, 0), pad(2, "U2-2", p4, 1, 1),
		tr(1, 0, p1, j), tr(1, 0, j, p2), tr(1, 0, j, pt(3_000_051, 4_000_000)), // T junction
		tr(2, 0, p3, v1), via(2, v1, 304_800), tr(2, 1, v1, p4),
		tr(2, 0, p3, v2), via(2, v2, 200_000), // via outside the catalogue
		{Kind: board.Track, Net: 1, From: 1, To: 1, Shape: geom.Seg{A: p1, B: pt(1_000_037, 9_000_000), HalfW: 127_000}, Fixed: true},
	} {
		b.AddItem(it)
	}
	r := &recRules{layers: []rules.Layer{{ID: 0, Name: "TopLayer", Outer: true}, {ID: 1, Name: "BottomLayer", Outer: true}},
		vias: []rules.ViaType{{Name: "via0", Pad: 609_600, From: 0, To: 1}}}
	return b, r
}

func writeSES(t *testing.T, v board.View, rs rules.Resolver) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteSES(&buf, v, rs); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// padGroups names each pad's copper-connected group by its first pad. Copper
// joins through
// coinciding track ends and through track ends, vias and pads that
// overlap on a shared layer.
func padGroups(pads, copper []board.Item) map[string]string {
	items := append(append([]board.Item{}, pads...), copper...)
	parent := make([]int, len(items))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	points := func(it board.Item) ([]geom.Pt, int64) {
		switch s := it.Shape.(type) {
		case geom.Seg:
			return []geom.Pt{s.A, s.B}, 0
		case geom.Circle:
			return []geom.Pt{s.C}, s.R
		}
		return nil, 0
	}
	near := func(a, b geom.Pt, r int64) bool {
		dx, dy := a.X-b.X, a.Y-b.Y
		return dx*dx+dy*dy <= r*r
	}
	for i, a := range items {
		for j, b := range items[:i] {
			if a.Net != b.Net || a.To < b.From || b.To < a.From {
				continue
			}
			pa, ra := points(a)
			pb, rb := points(b)
			for _, x := range pa {
				for _, y := range pb {
					if near(x, y, max(ra, rb)) {
						parent[find(i)] = find(j)
					}
				}
			}
		}
	}
	// Name each group by its first pad.
	first := map[int]string{}
	out := map[string]string{}
	for i, p := range pads {
		root := find(i)
		if _, ok := first[root]; !ok {
			first[root] = p.Ref
		}
		out[p.Ref] = first[root]
	}
	return out
}

// Test04T13SESRoundTrip is spec 04 T13: re-importing the SES gives identical
// connectivity, and the bytes are deterministic.
func Test04T13SESRoundTrip(t *testing.T) {
	b, rs := sesBoard()
	out := writeSES(t, b, rs)
	if again := writeSES(t, b, rs); !bytes.Equal(out, again) {
		t.Fatal("two writes differ")
	}
	shuffled := &recBoard{nets: b.nets, items: append([]board.Item{}, b.items...)}
	rand.New(rand.NewSource(1)).Shuffle(len(shuffled.items), func(i, j int) {
		shuffled.items[i], shuffled.items[j] = shuffled.items[j], shuffled.items[i]
	})
	if got := writeSES(t, shuffled, rs); !bytes.Equal(out, got) {
		t.Fatalf("item order changes the output:\n%s\n---\n%s", out, got)
	}

	items, err := ReadSES(out, b, rs)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var pads, before []board.Item
	for _, it := range b.items {
		switch {
		case it.Kind == board.Pad:
			pads = append(pads, it)
		case !it.Fixed:
			before = append(before, it)
		}
	}
	if len(items) != len(before) {
		t.Fatalf("read %d items, wrote %d", len(items), len(before))
	}
	if g0, g1 := padGroups(pads, before), padGroups(pads, items); !reflect.DeepEqual(g0, g1) {
		t.Fatalf("connectivity changed: %v → %v", g0, g1)
	}
	if g := padGroups(pads, items); g["U1-1"] != g["U1-2"] || g["U2-1"] != g["U2-2"] || g["U1-1"] == g["U2-1"] {
		t.Fatalf("unexpected groups %v", g)
	}
	// A second round trip is exact: snapped geometry is a fixpoint.
	again := &recBoard{nets: b.nets}
	for _, it := range items {
		again.AddItem(it)
	}
	if out2 := writeSES(t, again, rs); !bytes.Equal(out, out2) {
		t.Fatalf("SES → items → SES is not a fixpoint:\n%s\n---\n%s", out, out2)
	}

	s := string(out)
	for _, want := range []string{"(resolution um 10)", "(net \"SIG A\"", "(via via0 50000 60000)",
		"(padstack pcbroute_via_4000_0_1", "(via pcbroute_via_4000_0_1 10000 80000)"} {
		if !strings.Contains(s, want) {
			t.Errorf("SES lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "10000 90000") { // the fixed track's far end
		t.Errorf("fixed track written:\n%s", s)
	}
	// pcbpilot's existing importer parses it too.
	w, err := specctra.ParseSES(s)
	if err != nil || len(w.Segments) != len(before)-2 || len(w.Vias) != 2 {
		t.Fatalf("ParseSES: %v, %d segments, %d vias", err, len(w.Segments), len(w.Vias))
	}
}

func TestChains(t *testing.T) {
	p := func(x, y int64) sesPt { return sesPt{x, y} }
	segs := [][2]sesPt{
		{p(0, 0), p(1, 0)}, {p(2, 0), p(1, 0)}, {p(1, 0), p(1, 1)}, // T at (1,0)
		{p(5, 5), p(6, 5)}, {p(6, 5), p(6, 6)}, {p(6, 6), p(5, 5)}, // closed loop
		{p(9, 0), p(8, 0)}, {p(8, 0), p(7, 0)}, // one path, given backwards
	}
	want := [][]sesPt{
		{p(0, 0), p(1, 0)}, {p(1, 0), p(1, 1)}, {p(1, 0), p(2, 0)},
		{p(7, 0), p(8, 0), p(9, 0)},
		{p(5, 5), p(6, 5), p(6, 6), p(5, 5)},
	}
	if got := chains(segs); !reflect.DeepEqual(got, want) {
		t.Fatalf("chains = %v, want %v", got, want)
	}
	rev := make([][2]sesPt, len(segs))
	for i, s := range segs {
		rev[len(segs)-1-i] = [2]sesPt{s[1], s[0]}
	}
	if got := chains(rev); !reflect.DeepEqual(got, want) {
		t.Fatalf("chains depends on input order: %v", got)
	}
}

func TestSnap(t *testing.T) {
	for in, want := range map[int64]int64{0: 0, 49: 0, 50: 1, 149: 1, 150: 2, -49: 0, -50: -1, -150: -2} {
		if got := snap(in); got != want {
			t.Errorf("snap(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestReadSESErrors(t *testing.T) {
	b, rs := sesBoard()
	for _, src := range []string{
		`(PCB x)`,
		`(session x)`,
		`(session x (routes))`,
		`(session x (routes (resolution um 10) (network_out (net NOPE))))`,
		`(session x (routes (resolution um 10) (network_out (net GND (via nope 0 0)))))`,
		`(session x (routes (resolution um 10) (network_out (net GND (wire (path Inner9 10 0 0 1 1))))))`,
	} {
		if _, err := ReadSES([]byte(src), b, rs); err == nil {
			t.Errorf("no error for %s", src)
		}
	}
}
