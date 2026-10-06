package dsn

import (
	"bytes"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// fixtureBoard is a 4-layer pcbauto board with every pad form the writer
// handles: round, rect at 0°/90°/30°, stadium, through-hole, bottom, inner,
// and a repeated pin number.
func fixtureBoard() *pcbauto.Board {
	pt := func(x, y float64) pcbauto.Point { return pcbauto.Point{X: x, Y: y} }
	pad := func(part, num, net string, layer int, c pcbauto.Point, w, h, rot float64, round bool) *pcbauto.Pad {
		return &pcbauto.Pad{Part: part, Number: num, Net: net, Layer: layer,
			Box: pcbauto.OrientedBox{C: c, W: w, H: h, Rot: rot, Round: round}}
	}
	return &pcbauto.Board{
		Outline:      []pcbauto.Point{pt(0, 0), pt(1000, 0), pt(1000, 1000), pt(0, 1000)},
		CopperLayers: 4,
		Rules:        pcbauto.Rules{Clearance: 6, TrackWidth: 8, ViaDrill: 12, ViaDia: 24},
		Parts: []*pcbauto.Part{
			{Ref: "R1", Pads: []*pcbauto.Pad{
				pad("R1", "1", "GND", pcbauto.LayerMulti, pt(100, 100), 24, 24, 0, true),
				pad("R1", "2", "SIG", pcbauto.LayerTop, pt(150, 100), 20, 30, 0, false),
			}},
			{Ref: "U1", Pads: []*pcbauto.Pad{
				pad("U1", "1", "SIG", pcbauto.LayerTop, pt(300, 200), 20, 10, 90, false),
				pad("U1", "2", "N3", pcbauto.LayerTop, pt(350, 200), 20, 10, 30, false),
				pad("U1", "3", "N3", pcbauto.LayerBottom, pt(400, 200), 40, 20, 0, true),
				pad("U1", "4", "GND", pcbauto.LayerInner1, pt(450, 200), 20, 20, 0, true),
				pad("U1", "5", "GND", pcbauto.LayerTop, pt(500, 200.0004), 10, 10, 0, false),
				pad("U1", "5", "GND", pcbauto.LayerTop, pt(520, 200), 10, 10, 0, false),
				pad("U1", "6", "", pcbauto.LayerTop, pt(540, 200), 10, 10, 0, false),
			}},
		},
		Keepouts: []*pcbauto.Keepout{
			{Poly: []pcbauto.Point{pt(600, 600), pt(700, 600), pt(700, 700)}, NoCopper: true},
			{Poly: []pcbauto.Point{pt(600, 100), pt(700, 100), pt(700, 150)}, Layers: []int{pcbauto.LayerTop}, NoVias: true},
			{Poly: []pcbauto.Point{pt(10, 10), pt(20, 10), pt(20, 20)}, NoParts: true},
		},
		Holes: []*pcbauto.Hole{
			{C: pt(800, 800), Dia: 100, Keep: 10},
			{Poly: []pcbauto.Point{pt(850, 100), pt(900, 100), pt(900, 300), pt(850, 300)}},
		},
	}
}

func writeDSN(t *testing.T, b *pcbauto.Board) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteDSN(&buf, b); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func milPt(x, y float64) geom.Pt {
	return geom.Pt{X: int64(math.Round(x * mil)), Y: int64(math.Round(y * mil))}
}

func TestWriteDSNRead(t *testing.T) {
	b := fixtureBoard()
	src := writeDSN(t, b)
	if !bytes.Equal(src, writeDSN(t, b)) {
		t.Fatal("two writes differ")
	}
	rb, rr, warns, err := load(src)
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	if len(warns) > 0 {
		t.Errorf("warnings %q", warns)
	}
	if pins, nets, conns := counts(rb); pins != 9 || nets != 3 || conns != 3+1+1 {
		t.Errorf("pins=%d nets=%d conns=%d", pins, nets, conns)
	}
	if len(rr.layers) != 4 || rr.layers[1].Name != "Inner1" {
		t.Errorf("layers %+v", rr.layers)
	}
	want := map[string]board.Item{
		"R1-1": {From: 0, To: 3, Shape: geom.Circle{C: milPt(100, 100), R: 12 * mil}},
		"U1-1": {Shape: geom.Rect{MinX: 295 * mil, MinY: 190 * mil, MaxX: 305*mil + 1, MaxY: 210*mil + 1}},
		"U1-3": {From: 3, To: 3, Shape: geom.Seg{A: milPt(390, 200), B: milPt(410, 200), HalfW: 10 * mil}},
		"U1-4": {From: 1, To: 1, Shape: geom.Circle{C: milPt(450, 200), R: 10 * mil}},
		"U1-5": {Shape: geom.Rect{MinX: 495 * mil, MinY: 195 * mil, MaxX: 505*mil + 1, MaxY: 205*mil + 1}},
		// The second pad "5" was renamed.
		"U1-5_2": {Shape: geom.Rect{MinX: 515 * mil, MinY: 195 * mil, MaxX: 525*mil + 1, MaxY: 205*mil + 1}},
	}
	for ref, w := range want {
		got := pads(rb, ref)
		if len(got) != 1 || got[0].From != w.From || got[0].To != w.To || !reflect.DeepEqual(got[0].Shape, w.Shape) {
			t.Errorf("%s = %+v, want %+v", ref, got, w)
		}
	}
	// 30° rect: corners within the 0.001 mil (25.4 nm) resolution.
	poly := pads(rb, "U1-2")[0].Shape.(geom.Poly)
	s, c := math.Sincos(30 * math.Pi / 180)
	for i, k := range [][2]float64{{-10, -5}, {10, -5}, {10, 5}, {-10, 5}} {
		w := milPt(350+k[0]*c-k[1]*s, 200+k[0]*s+k[1]*c)
		if d := max(abs(poly.Pts[i].X-w.X), abs(poly.Pts[i].Y-w.Y)); d > 13 {
			t.Errorf("30° corner %d = %v, want %v", i, poly.Pts[i], w)
		}
	}
	if len(rr.keepouts) != 4 || rr.keepouts[1].Kind != rules.KeepoutVia || rr.keepouts[1].Layer != 0 {
		t.Errorf("keep-outs %+v", rr.keepouts)
	}
	// Hole: drill 100 plus 2 × (keep 10 + clearance 6 − clearance 6).
	if h := rr.keepouts[2].Shape; h != (geom.Circle{C: milPt(800, 800), R: 60 * mil}) {
		t.Errorf("hole keep-out %+v", h)
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// TestWriteDSNBenchPipeline runs the bench preparation of gap §4.2 (A) on the
// written file: dsn-fix and net requirements, then reads it.
func TestWriteDSNBenchPipeline(t *testing.T) {
	src := string(writeDSN(t, fixtureBoard()))
	fixed, rep, err := specctra.FixDSN(src, specctra.FixOptions{CopperLayers: 4, EdgeOuterMil: 20, EdgeInnerMil: 30})
	if err != nil {
		t.Fatal(err)
	}
	if rep.EdgeKeepouts != 16 || rep.PatchedPadstacks != 0 || len(rep.AddedLayers) != 0 {
		t.Errorf("FixDSN report %+v", rep)
	}
	fixed, _, err = specctra.ApplyNetRequirements(fixed, map[string]specctra.NetRequirement{
		"GND": {OuterMil: 20, InnerMil: 30, MinMil: 20, ClearanceMil: 8}})
	if err != nil {
		t.Fatal(err)
	}
	b0, _, _, err := load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	b1, r1, _, err := load([]byte(fixed))
	if err != nil {
		t.Fatalf("%v\n%s", err, fixed)
	}
	if !reflect.DeepEqual(padItems(b0), padItems(b1)) {
		t.Error("dsn-fix changed the pads")
	}
	var class rules.ClassID
	for _, s := range r1.sets {
		if s.s.Kind == rules.ScopeClass && s.rs.Width != nil && *s.rs.Width == 20*mil {
			class = s.s.Class
		}
	}
	if class == 0 || r1.class[1] != class {
		t.Errorf("GND requirement class not applied: sets %+v classes %v", r1.sets, r1.class)
	}
}

func padItems(b *recBoard) []board.Item {
	var out []board.Item
	for _, it := range b.items {
		if it.Kind == board.Pad {
			it.ID = 0
			out = append(out, it)
		}
	}
	return out
}

// boardFromItems rebuilds a pcbauto board from read pad items, the inverse of
// WriteDSN for the pad forms it writes.
func boardFromItems(t *testing.T, rb *recBoard, proto *pcbauto.Board) *pcbauto.Board {
	t.Helper()
	m := func(v int64) float64 { return float64(v) / mil }
	out := &pcbauto.Board{Outline: proto.Outline, CopperLayers: proto.CopperLayers, Rules: proto.Rules}
	parts := map[string]*pcbauto.Part{}
	byRef := map[string][]board.Item{}
	var refs []string
	for _, it := range padItems(rb) {
		if byRef[it.Ref] == nil {
			refs = append(refs, it.Ref)
		}
		byRef[it.Ref] = append(byRef[it.Ref], it)
	}
	for _, ref := range refs {
		its := byRef[ref]
		if len(its) != 1 {
			t.Fatalf("%s: %d items", ref, len(its))
		}
		it := its[0]
		part, num, _ := strings.Cut(ref, "-")
		pd := &pcbauto.Pad{Part: part, Number: num, Layer: pcbauto.LayerMulti}
		if it.Net != 0 {
			pd.Net = rb.nets[it.Net-1]
		}
		switch {
		case it.From == 0 && it.To == 0:
			pd.Layer = pcbauto.LayerTop
		case it.From == 3 && it.To == 3:
			pd.Layer = pcbauto.LayerBottom
		case it.From == it.To:
			pd.Layer = pcbauto.LayerInner1 + int(it.From) - 1
		}
		switch s := it.Shape.(type) {
		case geom.Circle:
			pd.Box = pcbauto.OrientedBox{C: pcbauto.Point{X: m(s.C.X), Y: m(s.C.Y)}, W: m(2 * s.R), H: m(2 * s.R), Round: true}
		case geom.Rect:
			pd.Box = pcbauto.OrientedBox{C: pcbauto.Point{X: m(s.MinX+s.MaxX-1) / 2, Y: m(s.MinY+s.MaxY-1) / 2},
				W: m(s.MaxX - 1 - s.MinX), H: m(s.MaxY - 1 - s.MinY)}
		case geom.Seg:
			dx, dy := m(s.B.X-s.A.X), m(s.B.Y-s.A.Y)
			pd.Box = pcbauto.OrientedBox{C: pcbauto.Point{X: m(s.A.X+s.B.X) / 2, Y: m(s.A.Y+s.B.Y) / 2},
				W: math.Hypot(dx, dy) + m(2*s.HalfW), H: m(2 * s.HalfW), Rot: math.Atan2(dy, dx) * 180 / math.Pi, Round: true}
		case geom.Poly:
			p := s.Pts
			var cx, cy float64
			for _, q := range p {
				cx, cy = cx+m(q.X)/4, cy+m(q.Y)/4
			}
			pd.Box = pcbauto.OrientedBox{C: pcbauto.Point{X: cx, Y: cy},
				W: math.Hypot(m(p[1].X-p[0].X), m(p[1].Y-p[0].Y)), H: math.Hypot(m(p[2].X-p[1].X), m(p[2].Y-p[1].Y)),
				Rot: math.Round(math.Atan2(m(p[1].Y-p[0].Y), m(p[1].X-p[0].X))*180/math.Pi*1000) / 1000}
		}
		if parts[part] == nil {
			parts[part] = &pcbauto.Part{Ref: part}
			out.Parts = append(out.Parts, parts[part])
		}
		parts[part].Pads = append(parts[part].Pads, pd)
	}
	sort.SliceStable(out.Parts, func(i, j int) bool { return out.Parts[i].Ref < out.Parts[j].Ref })
	return out
}

// TestDSNFixpoint is the PLAN M3 test DSN → board → DSN → board: reading,
// rebuilding and rewriting reproduces the pads within the DSN resolution.
func TestDSNFixpoint(t *testing.T) {
	proto := fixtureBoard()
	d1 := writeDSN(t, proto)
	b1, _, _, err := load(d1)
	if err != nil {
		t.Fatal(err)
	}
	d2 := writeDSN(t, boardFromItems(t, b1, proto))
	b2, _, _, err := load(d2)
	if err != nil {
		t.Fatal(err)
	}
	p1, p2 := padItems(b1), padItems(b2)
	if len(p1) != len(p2) || !reflect.DeepEqual(b1.nets, b2.nets) {
		t.Fatalf("pads %d → %d, nets %v → %v", len(p1), len(p2), b1.nets, b2.nets)
	}
	for i := range p1 {
		a, b := p1[i], p2[i]
		pa, pb := a.Shape, b.Shape
		a.Shape, b.Shape = nil, nil
		if a != b {
			t.Errorf("pad %d: %+v → %+v", i, a, b)
		}
		if qa, ok := pa.(geom.Poly); ok {
			qb := pb.(geom.Poly)
			for k := range qa.Pts {
				if d := max(abs(qa.Pts[k].X-qb.Pts[k].X), abs(qa.Pts[k].Y-qb.Pts[k].Y)); d > 26 {
					t.Errorf("pad %d corner %d moved %d nm", i, k, d)
				}
			}
		} else if !reflect.DeepEqual(pa, pb) {
			t.Errorf("pad %d (%s): %+v → %+v", i, a.Ref, pa, pb)
		}
	}
	// A third pass changes nothing at all.
	b3, _, _, err := load(writeDSN(t, boardFromItems(t, b2, proto)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(padItems(b2), padItems(b3)) {
		t.Error("second rewrite is not a fixpoint")
	}
}

func TestWriteDSNErrors(t *testing.T) {
	b := fixtureBoard()
	b.Outline = nil
	if err := WriteDSN(&bytes.Buffer{}, b); err == nil {
		t.Error("no error without an outline")
	}
	b = fixtureBoard()
	b.CopperLayers = 2
	if err := WriteDSN(&bytes.Buffer{}, b); err == nil {
		t.Error("no error for an inner pad on a 2-layer board")
	}
}
