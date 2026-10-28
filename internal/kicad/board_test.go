package kicad

import (
	"encoding/json"
	"math"
	"os/exec"
	"strings"
	"testing"
)

func boardCodes(is []BoardIssue) []string {
	var out []string
	for _, i := range is {
		out = append(out, i.Code+"@"+i.Field)
	}
	return out
}

func boardHasCode(is []BoardIssue, code, field string) bool {
	for _, i := range is {
		if i.Code == code && strings.HasPrefix(i.Field, field) {
			if i.Fix == "" {
				return false // every refusal carries a fix
			}
			return true
		}
	}
	return false
}

func TestBoardShapeValidate(t *testing.T) {
	cases := []struct {
		name  string
		s     BoardShape
		code  string
		field string
	}{
		{"none", BoardShape{}, ErrBoardArg, "outline"},
		{"two", BoardShape{Rect: &BoardRect{Width: 1, Height: 1}, Circle: &BoardCircle{Diameter: 1}}, ErrBoardArg, "outline"},
		{"zero width", BoardShape{Rect: &BoardRect{Width: 0, Height: 5}}, ErrBoardArg, "outline.rect.width"},
		{"nan", BoardShape{Rect: &BoardRect{X: math.NaN(), Width: 1, Height: 5}}, ErrBoardArg, "outline.rect.x"},
		{"huge", BoardShape{Rect: &BoardRect{Width: 5000, Height: 5}}, ErrBoardArg, "outline.rect.width"},
		{"radius", BoardShape{Rect: &BoardRect{Width: 10, Height: 4, CornerRadius: 2.5}}, ErrBoardGeometry, "outline.rect.cornerRadius"},
		{"2 points", BoardShape{Polygon: []XY{{0, 0}, {1, 1}}}, ErrBoardGeometry, "outline.polygon"},
		{"repeat", BoardShape{Polygon: []XY{{0, 0}, {1, 0}, {1, 0}, {0, 1}}}, ErrBoardGeometry, "outline.polygon"},
		{"collinear", BoardShape{Polygon: []XY{{0, 0}, {1, 0}, {2, 0}}}, ErrBoardGeometry, "outline.polygon"},
		{"bowtie", BoardShape{Polygon: []XY{{0, 0}, {10, 10}, {10, 0}, {0, 10}}}, ErrBoardGeometry, "outline.polygon"},
		{"slot not allowed", BoardShape{Slot: &BoardSlot{X2: 1, Width: 1}}, ErrBoardArg, "outline"},
	}
	for _, c := range cases {
		bad := c.s.Validate("outline", "rect", "polygon", "circle")
		if !boardHasCode(bad, c.code, c.field) {
			t.Errorf("%s: want %s@%s, got %v", c.name, c.code, c.field, boardCodes(bad))
		}
	}
	ok := []BoardShape{
		{Rect: &BoardRect{Width: 10, Height: 4, CornerRadius: 2}}, // stadium
		{Polygon: []XY{{0, 0}, {80, 0}, {80, 50}, {20, 50}, {0, 30}}},
		{Circle: &BoardCircle{X: 5, Y: 5, Diameter: 30}},
	}
	for _, s := range ok {
		if bad := s.Validate("outline", "rect", "polygon", "circle"); len(bad) > 0 {
			t.Errorf("%+v: %v", s, boardCodes(bad))
		}
	}
}

// primsClosed: consecutive primitives share end points (a closed chain).
func primsClosed(t *testing.T, ps []BoardPrim) {
	t.Helper()
	for i, p := range ps {
		q := ps[(i+1)%len(ps)]
		if bdDist(*p.End, *q.Start) > 1e-6 {
			t.Fatalf("prim %d ends at %v, prim %d starts at %v", i, *p.End, (i+1)%len(ps), *q.Start)
		}
	}
}

func TestBoardShapePrimsGeometry(t *testing.T) {
	r := BoardShape{Rect: &BoardRect{X: 10, Y: 20, Width: 80, Height: 50, CornerRadius: 3}}
	ps := r.Prims()
	if len(ps) != 8 {
		t.Fatalf("rounded rect: %d prims, want 4 segments + 4 arcs", len(ps))
	}
	primsClosed(t, ps)
	for _, p := range ps {
		if p.Kind == "arc" {
			// A quarter circle of radius 3: both half-chords are 2·3·sin(π/8).
			d1, d2 := bdDist(*p.Start, *p.Mid), bdDist(*p.Mid, *p.End)
			if math.Abs(d1-d2) > 1e-6 || math.Abs(d1-2*3*math.Sin(math.Pi/8)) > 1e-5 {
				t.Errorf("arc %v→%v→%v is not a quarter circle of radius 3", *p.Start, *p.Mid, *p.End)
			}
		}
	}
	b := bdBboxOf(r.Samples())
	if b != [4]float64{10, 20, 90, 70} {
		t.Errorf("rounded rect bbox %v", b)
	}
	// A radius of half the short side: no zero-length segments.
	st := BoardShape{Rect: &BoardRect{Width: 10, Height: 4, CornerRadius: 2}}.Prims()
	if len(st) != 6 {
		t.Errorf("stadium rect: %d prims, want 2 segments + 4 arcs", len(st))
	}
	primsClosed(t, st)

	sl := BoardShape{Slot: &BoardSlot{X1: 20, Y1: 25, X2: 40, Y2: 25, Width: 2}}
	sp := sl.Prims()
	if len(sp) != 4 || sp[1].Kind != "arc" || sp[3].Kind != "arc" {
		t.Fatalf("slot prims %+v", sp)
	}
	primsClosed(t, sp)
	// The end arcs bulge outward: past x2 and before x1.
	if sp[1].Mid[0] <= 40 || sp[3].Mid[0] >= 20 {
		t.Errorf("slot arcs bulge inward: mids %v %v", *sp[1].Mid, *sp[3].Mid)
	}
	if b := bdBboxOf(sl.Samples()); math.Abs(b[0]-19) > 1e-6 || math.Abs(b[2]-41) > 1e-6 || math.Abs(b[1]-24) > 1e-6 || math.Abs(b[3]-26) > 1e-6 {
		t.Errorf("slot bbox %v", b)
	}
	if p := (BoardShape{Slot: &BoardSlot{X1: 5, Y1: 5, X2: 5, Y2: 5, Width: 2}}).Prims(); len(p) != 1 || p[0].Kind != "circle" || p[0].Radius != 1 {
		t.Errorf("zero-length slot is a circle: %+v", p)
	}
	poly := BoardShape{Polygon: []XY{{0, 0}, {10, 0}, {5, 8}}}.Prims()
	if len(poly) != 3 {
		t.Errorf("triangle: %d segments", len(poly))
	}
	primsClosed(t, poly)
}

func TestOutlineCutouts(t *testing.T) {
	outer := BoardShape{Rect: &BoardRect{Width: 80, Height: 50, CornerRadius: 3}}
	good := OutlineSpec{Outer: outer, Cutouts: []BoardShape{
		{Slot: &BoardSlot{X1: 20, Y1: 25, X2: 40, Y2: 25, Width: 2}},
		{Circle: &BoardCircle{X: 70, Y: 40, Diameter: 4}},
	}}
	if bad := good.Validate(); len(bad) > 0 {
		t.Fatalf("good outline: %v", boardCodes(bad))
	}
	cases := map[string]OutlineSpec{
		"outside":  {Outer: outer, Cutouts: []BoardShape{{Circle: &BoardCircle{X: 79, Y: 25, Diameter: 4}}}},
		"corner":   {Outer: outer, Cutouts: []BoardShape{{Circle: &BoardCircle{X: 0.6, Y: 0.6, Diameter: 1}}}}, // in the rounded-off corner
		"overlap":  {Outer: outer, Cutouts: []BoardShape{{Circle: &BoardCircle{X: 30, Y: 25, Diameter: 4}}, {Slot: &BoardSlot{X1: 20, Y1: 25, X2: 40, Y2: 25, Width: 2}}}},
		"enclosed": {Outer: outer, Cutouts: []BoardShape{{Rect: &BoardRect{X: 10, Y: 10, Width: 20, Height: 20}}, {Circle: &BoardCircle{X: 20, Y: 20, Diameter: 2}}}},
	}
	for name, o := range cases {
		if !boardHasCode(o.Validate(), ErrBoardGeometry, "cutouts[") {
			t.Errorf("%s: want E_BOARD_GEOMETRY, got %v", name, boardCodes(o.Validate()))
		}
	}
	if !boardHasCode(OutlineSpec{}.Validate(), ErrBoardArg, "outline") {
		t.Error("empty outline accepted")
	}
	if !boardHasCode(OutlineSpec{Replace: true, Cutouts: good.Cutouts}.Validate(), ErrBoardArg, "replace") {
		t.Error("--replace without an outline accepted")
	}
	if !boardHasCode(OutlineSpec{Units: "mil", Outer: outer}.Validate(), ErrBoardUnits, "units") {
		t.Error("mil spec accepted")
	}
	// Cutouts alone (added to the board's outline) validate without an outer shape.
	if bad := (OutlineSpec{Cutouts: good.Cutouts}).Validate(); len(bad) > 0 {
		t.Errorf("cutouts only: %v", boardCodes(bad))
	}
}

func TestOutlineSpecJSON(t *testing.T) {
	var o OutlineSpec
	err := ParseBoardSpec("outline", []byte(`{"units":"mm","rect":{"x":0,"y":0,"width":80,"height":50,"cornerRadius":3},"cutouts":[{"slot":{"x1":20,"y1":25,"x2":40,"y2":25,"width":2}}],"replace":true}`), &o)
	if err != nil || o.Outer.Rect == nil || o.Outer.Rect.CornerRadius != 3 || len(o.Cutouts) != 1 || !o.Replace {
		t.Fatalf("parse: %v %+v", err, o)
	}
	for _, bad := range []string{`{"rect":{"w":1}}`, `{"rectangle":{}}`, `{"rect":{"width":1,"height":1}} {}`} {
		var x OutlineSpec
		err := ParseBoardSpec("outline", []byte(bad), &x)
		var be *BoardError
		if err == nil || !boardAsError(err, &be) || be.Issues[0].Code != ErrBoardArg {
			t.Errorf("%s: want E_BOARD_ARG, got %v", bad, err)
		}
	}
}

func boardAsError(err error, out **BoardError) bool {
	be, ok := err.(*BoardError)
	*out = be
	return ok
}

func TestStackupSpec(t *testing.T) {
	s := StackupSpec{Layers: 4, Thickness: 1.6, Finish: "enig"}
	if bad := s.Validate(); len(bad) > 0 {
		t.Fatal(boardCodes(bad))
	}
	if got := s.TotalThickness(); got != 1.6 {
		t.Errorf("4-layer 1.6 mm adds up to %v", got)
	}
	rows := s.Rows()
	var cu, diel []string
	for _, r := range rows {
		switch r.Type {
		case "copper":
			cu = append(cu, r.Name)
		case "core", "prepreg":
			diel = append(diel, r.Type)
		}
	}
	if strings.Join(cu, ",") != "F.Cu,In1.Cu,In2.Cu,B.Cu" || strings.Join(diel, ",") != "prepreg,core,prepreg" {
		t.Errorf("4-layer stack: copper %v dielectrics %v", cu, diel)
	}
	blk := s.StackupBlock()
	for _, want := range []string{`(stackup`, `(layer "In2.Cu"`, `(copper_finish "ENIG")`, `(material "FR4")`, `(dielectric_constraints no)`} {
		if !strings.Contains(blk, want) {
			t.Errorf("block lacks %s:\n%s", want, blk)
		}
	}
	if strings.Count(blk, "(") != strings.Count(blk, ")") {
		t.Error("unbalanced stackup block")
	}
	for _, n := range []int{2, 6} {
		x := StackupSpec{Layers: n, Thickness: 1.2}
		if bad := x.Validate(); len(bad) > 0 || x.TotalThickness() != 1.2 {
			t.Errorf("%d layers 1.2 mm: %v total %v", n, boardCodes(bad), x.TotalThickness())
		}
	}
	// The demo 2-layer stack: 0.01 + 0.035 + 1.51 + 0.035 + 0.01.
	d := StackupSpec{Layers: 2, Dielectrics: []Dielectric{{Thickness: 1.51}}}
	if bad := d.Validate(); len(bad) > 0 || d.TotalThickness() != 1.6 {
		t.Errorf("explicit dielectric: %v total %v", boardCodes(bad), d.TotalThickness())
	}
	bad := map[string]StackupSpec{
		"layers":      {Layers: 3},
		"finish":      {Layers: 2, Finish: "gold leaf"},
		"dielectrics": {Layers: 4, Dielectrics: []Dielectric{{Thickness: 0.2}}},
		"thickness":   {Layers: 6, Thickness: 0.3},
		"outerCopper": {Layers: 2, OuterCopper: -1},
		"material":    {Layers: 2, Material: `FR4") (x`},
		"units":       {Layers: 2, Units: "in"},
	}
	for field, sp := range bad {
		is := sp.Validate()
		if len(is) == 0 || !strings.HasPrefix(is[0].Field, field) || is[0].Fix == "" {
			t.Errorf("%s: want an issue on it, got %v", field, boardCodes(is))
		}
	}
	// Explicit dielectrics that do not add up to --thickness.
	if is := (StackupSpec{Layers: 2, Thickness: 1.0, Dielectrics: []Dielectric{{Thickness: 1.51}}}).Validate(); !boardHasCode(is, ErrBoardArg, "thickness") {
		t.Errorf("mismatched thickness accepted: %v", boardCodes(is))
	}
	if FinishName("lead-free HASL") != "HAL lead-free" || FinishName("") != "None" || FinishName("osp") != "OSP" {
		t.Error("finish aliases")
	}
}

func TestHolesSpec(t *testing.T) {
	ok := HolesSpec{Holes: []HoleSpec{{X: 4, Y: 4, Drill: 3.2}, {X: 76, Y: 4, Drill: 3.2, Pad: 6.4, Type: "pth", Net: "GND", Ref: "H9"}}}
	if bad := ok.Validate(); len(bad) > 0 {
		t.Fatal(boardCodes(bad))
	}
	cases := []struct {
		h     HolesSpec
		code  string
		field string
	}{
		{HolesSpec{}, ErrBoardArg, "holes"},
		{HolesSpec{Holes: []HoleSpec{{Drill: 0}}}, ErrBoardArg, "holes[0].drill"},
		{HolesSpec{Holes: []HoleSpec{{Drill: 3, Net: "GND"}}}, ErrBoardArg, "holes[0].net"},
		{HolesSpec{Holes: []HoleSpec{{Drill: 3, Pad: 5}}}, ErrBoardArg, "holes[0].pad"},
		{HolesSpec{Holes: []HoleSpec{{Drill: 3, Type: "pth"}}}, ErrBoardArg, "holes[0].pad"},
		{HolesSpec{Holes: []HoleSpec{{Drill: 3, Pad: 3.05, Type: "pth"}}}, ErrBoardArg, "holes[0].pad"},
		{HolesSpec{Holes: []HoleSpec{{Drill: 3, Type: "via"}}}, ErrBoardArg, "holes[0].type"},
		{HolesSpec{Holes: []HoleSpec{{Drill: 3, Ref: "H 1"}}}, ErrBoardArg, "holes[0].ref"},
		{HolesSpec{Holes: []HoleSpec{{Drill: 3, Ref: "H1"}, {X: 20, Drill: 3, Ref: "H1"}}}, ErrBoardRefExists, "holes[1].ref"},
		{HolesSpec{Holes: []HoleSpec{{X: 10, Drill: 3}, {X: 12, Drill: 3}}}, ErrBoardOverlap, "holes[1]"},
	}
	for _, c := range cases {
		if bad := c.h.Validate(); !boardHasCode(bad, c.code, c.field) {
			t.Errorf("%+v: want %s@%s, got %v", c.h, c.code, c.field, boardCodes(bad))
		}
	}
	p := ok.payload()["holes"].([]map[string]any)
	if p[0]["type"] != "npth" || p[0]["pad"] != 3.2 {
		t.Errorf("npth payload: pad is the drill, type npth: %v", p[0])
	}
}

func TestKeepoutZoneTextSpecs(t *testing.T) {
	sq := BoardShape{Rect: &BoardRect{X: 1, Y: 1, Width: 5, Height: 5}}
	if bad := (KeepoutSpec{Shape: sq}).Validate(); len(bad) > 0 {
		t.Fatal(boardCodes(bad))
	}
	if f := (KeepoutSpec{Shape: sq}).payload()["forbid"].([]string); len(f) != 5 {
		t.Errorf("default forbid: %v", f)
	}
	if !boardHasCode(KeepoutSpec{Shape: sq, Forbid: []string{"tracks", "copper"}}.Validate(), ErrBoardArg, "forbid[1]") {
		t.Error("unknown rule accepted")
	}
	if !boardHasCode(KeepoutSpec{Shape: sq, Layers: []string{"F.Cu", "F.Cu"}}.Validate(), ErrBoardLayer, "layers[1]") {
		t.Error("repeated layer accepted")
	}
	if !boardHasCode(KeepoutSpec{}.Validate(), ErrBoardArg, "shape") {
		t.Error("keepout without shape accepted")
	}

	z := ZoneSpec{BoardOutline: true, Net: "GND", Layers: []string{"B.Cu"}}
	if bad := z.Validate(); len(bad) > 0 {
		t.Fatal(boardCodes(bad))
	}
	if p := z.payload(); p["fill"] != true || p["connection"] != "thermal" || p["clearance"] != 0.25 {
		t.Errorf("zone defaults: %v", p)
	}
	for field, zz := range map[string]ZoneSpec{
		"net":        {BoardOutline: true, Layers: []string{"F.Cu"}},
		"layers":     {BoardOutline: true, Net: "GND"},
		"shape":      {BoardOutline: true, Shape: sq, Net: "GND", Layers: []string{"F.Cu"}},
		"connection": {Shape: sq, Net: "GND", Layers: []string{"F.Cu"}, Connection: "spokes"},
		"priority":   {Shape: sq, Net: "GND", Layers: []string{"F.Cu"}, Priority: -1},
		"clearance":  {Shape: sq, Net: "GND", Layers: []string{"F.Cu"}, Clearance: math.Inf(1)},
	} {
		bad := zz.Validate()
		found := false
		for _, b := range bad {
			found = found || strings.HasPrefix(b.Field, field)
		}
		if !found {
			t.Errorf("zone %s: want an issue on it, got %v", field, boardCodes(bad))
		}
	}

	if bad := (TextSpec{Text: "rev A", X: 1, Y: 2}).Validate(); len(bad) > 0 {
		t.Fatal(boardCodes(bad))
	}
	if p := (TextSpec{Text: "x"}).payload(); p["layer"] != "F.Silkscreen" || p["size"] != 1.0 || p["justify"] != "center" {
		t.Errorf("text defaults %v", p)
	}
	for field, tt := range map[string]TextSpec{"text": {Text: " "}, "justify": {Text: "a", Justify: "top"}, "size": {Text: "a", Size: -1}, "x": {Text: "a", X: 1e9}} {
		if bad := tt.Validate(); len(bad) == 0 || bad[0].Field != field {
			t.Errorf("text %s: got %v", field, boardCodes(bad))
		}
	}
	if !boardHasCode(NewBoardSpec{Layers: 8}.Validate(), ErrBoardArg, "layers") {
		t.Error("8-layer new board accepted")
	}
}

func TestParseStats(t *testing.T) {
	s, err := ParseStats([]byte(`{"board":{"has_outline":true,"width":"80.0000 mm","height":"50.0000 mm","board_thickness":"1.6000 mm"},"pads":{"through_hole":2,"npth":4}}`))
	if err != nil || !s.HasOutline || s.Width != 80 || s.Thickness != 1.6 || s.NPTH != 4 || s.PTH != 2 {
		t.Fatalf("%v %+v", err, s)
	}
	if _, err := ParseStats([]byte(`{"board":{"width":"3 in"}}`)); err == nil {
		t.Error("inch value accepted")
	}
	if _, err := ParseStats([]byte(`{}`)); err == nil {
		t.Error("report without board accepted")
	}
	// KiCad 10 writes null sizes for a board without an outline.
	s, err = ParseStats([]byte(`{"board":{"has_outline":false,"width":null,"height":null,"board_thickness":"1.6000 mm"},"pads":{}}`))
	if err != nil || s.HasOutline || s.Width != 0 || s.Thickness != 1.6 {
		t.Fatalf("board without outline: %v %+v", err, s)
	}
}

func TestBoardPayloadJSON(t *testing.T) {
	// Everything board.py reads must survive JSON.
	o := OutlineSpec{Outer: BoardShape{Circle: &BoardCircle{X: 1, Y: 2, Diameter: 10}}}
	blob, err := json.Marshal(o.payload())
	if err != nil || !strings.Contains(string(blob), `"kind":"circle"`) || !strings.Contains(string(blob), `"radius":5`) {
		t.Fatalf("%v %s", err, blob)
	}
}

// TestBoardPyUnit runs board_test.py (the stackup splice and the s-expression
// scanner) against a fake pcbnew, without KiCad.
func TestBoardPyUnit(t *testing.T) {
	out, err := exec.Command(plainPython(t), "-B", "board_test.py").CombinedOutput()
	if err != nil {
		t.Fatalf("board_test.py: %v\n%s", err, out)
	}
}

func TestBoardExactContainment(t *testing.T) {
	outer := BoardShape{Rect: &BoardRect{Width: 80, Height: 50, CornerRadius: 3}}
	// A cutout enclosing another: overlap, though no boundaries cross.
	big := BoardShape{Rect: &BoardRect{X: 10, Y: 10, Width: 20, Height: 20}}
	small := BoardShape{Circle: &BoardCircle{X: 20, Y: 20, Diameter: 2}}
	if !BoardShapesOverlap(big, small) || !BoardShapesOverlap(small, big) {
		t.Error("enclosed cutout not seen as overlapping")
	}
	// Concave outline (a U): every corner of the cutout is on the board, its
	// edges cross the notch.
	u := BoardShape{Polygon: []XY{{0, 0}, {60, 0}, {60, 40}, {35, 40}, {35, 10}, {25, 10}, {25, 40}, {0, 40}}}
	across := BoardShape{Rect: &BoardRect{X: 20, Y: 30, Width: 20, Height: 4}}
	if BoardShapeInside(across, u) {
		t.Error("cutout across a concave notch counted as inside")
	}
	if !BoardShapeInside(BoardShape{Rect: &BoardRect{X: 5, Y: 30, Width: 15, Height: 4}}, u) {
		t.Error("cutout in one arm of the U not inside")
	}
	// Tangency between the 72 sample points of a round board (2.5° = half
	// a sample step): a 0.0005 mm gap touches, a 0.01 mm gap fits although
	// the sampled chord there lies 0.019 mm inside the circle.
	round := BoardShape{Circle: &BoardCircle{Diameter: 40}}
	at := func(rc float64) BoardShape {
		a := 2.5 * math.Pi / 180
		return BoardShape{Circle: &BoardCircle{X: rc * math.Cos(a), Y: rc * math.Sin(a), Diameter: 2}}
	}
	if BoardShapeInside(at(18.9995), round) {
		t.Error("cutout 0.0005 mm from a round outline counted as inside")
	}
	if !BoardShapeInside(at(18.99), round) {
		t.Error("cutout 0.01 mm inside a round outline refused (sampled check)")
	}
	if g := BoardShapeGap(at(18.99), round); math.Abs(g-0.01) > 1e-9 {
		t.Errorf("gap %v, want 0.01", g)
	}
	// Arc against segment: a circle near a rounded corner, inside the
	// corner's square but outside its arc.
	corner := BoardShape{Circle: &BoardCircle{X: 0.9, Y: 0.9, Diameter: 0.4}}
	if BoardShapeInside(corner, outer) {
		t.Error("cutout in the rounded-off corner counted as inside")
	}
	// Slot end arc tangent to a circle: 0.0005 mm apart touches.
	sl := BoardShape{Slot: &BoardSlot{X1: 10, Y1: 10, X2: 20, Y2: 10, Width: 2}}
	if !BoardShapesOverlap(sl, BoardShape{Circle: &BoardCircle{X: 23.0005, Y: 10, Diameter: 4}}) {
		t.Error("slot end touching a circle not seen")
	}
	if BoardShapesOverlap(sl, BoardShape{Circle: &BoardCircle{X: 23.01, Y: 10, Diameter: 4}}) {
		t.Error("slot 0.01 mm from a circle seen as overlapping")
	}
	// Arc–arc whose closest points are mid-arc (the line of centres).
	a1 := bdArcEdge(XY{0, 0}, 5, -math.Pi/4, math.Pi/2)
	a2 := bdArcEdge(XY{12, 0}, 5, 3*math.Pi/4, math.Pi/2)
	if d := bdEdgeDist(a1, a2); math.Abs(d-2) > 1e-9 {
		t.Errorf("arc-arc distance %v, want 2", d)
	}
	// Segment passing a full circle at 0.0005 mm.
	if d := bdEdgeDist(bdSegEdge(XY{-5, 1.0005}, XY{5, 1.0005}), bdArcEdge(XY{0, 0}, 1, 0, 2*math.Pi)); math.Abs(d-0.0005) > 1e-9 {
		t.Errorf("seg-circle distance %v", d)
	}
	o := OutlineSpec{Outer: u, Cutouts: []BoardShape{across}}
	if !boardHasCode(o.Validate(), ErrBoardGeometry, "cutouts[0]") {
		t.Error("outline validation missed the concave crossing")
	}
	o = OutlineSpec{Outer: outer, Cutouts: []BoardShape{big, small}}
	if !boardHasCode(o.Validate(), ErrBoardGeometry, "cutouts[1]") {
		t.Error("outline validation missed the enclosed cutout")
	}
}

func TestStackupRowsCarryColors(t *testing.T) {
	rows := StackupSpec{Layers: 2, MaskColor: "Black", SilkColor: "Yellow"}.payload()["rows"].([]map[string]any)
	want := map[string]string{"F.SilkS": "Yellow", "F.Mask": "Black", "B.Mask": "Black", "B.SilkS": "Yellow"}
	for _, r := range rows {
		if c, ok := want[r["name"].(string)]; ok && r["color"] != c {
			t.Errorf("%s colour %v, want %s", r["name"], r["color"], c)
		} else if !ok && r["color"] != nil {
			t.Errorf("%s has colour %v", r["name"], r["color"])
		}
	}
}

func TestHolesSameNetDrillVsRing(t *testing.T) {
	gnd := func(x float64) HoleSpec { return HoleSpec{X: x, Drill: 1, Pad: 4, Type: "pth", Net: "GND"} }
	// 1.5 mm apart: each drill cuts the other's 4 mm ring.
	if bad := (HolesSpec{Holes: []HoleSpec{gnd(0), gnd(1.5)}}).Validate(); !boardHasCode(bad, ErrBoardOverlap, "holes[1]") {
		t.Errorf("same-net drill in the other ring accepted: %v", boardCodes(bad))
	}
	// 3 mm apart: rings overlap, drills clear of the other's copper.
	if bad := (HolesSpec{Holes: []HoleSpec{gnd(0), gnd(3)}}).Validate(); len(bad) > 0 {
		t.Errorf("same-net ring contact refused: %v", boardCodes(bad))
	}
}
