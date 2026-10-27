package app

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// Live tests of `kicad board` against KiCad's own python and kicad-cli:
// PCBPILOT_KICAD_LIVE=1 go test ./internal/app -run TestKicadBoardLive
// (demo boards from $PCBPILOT_KICAD_DEMOS, default the KiCad install's
// share/kicad/demos).

func boardLiveTools(t *testing.T) *kicad.Tools {
	t.Helper()
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 (needs KiCad's python and kicad-cli)")
	}
	tools, err := kicad.Locate()
	if err != nil {
		t.Skip(err)
	}
	return tools
}

// boardLiveOp runs one op in place with --drc and fails the test unless it
// was written with every check passing.
func boardLiveOp(t *testing.T, tools *kicad.Tools, op, pcb string, spec kicad.BoardOpSpec) *kicadBoardReport {
	t.Helper()
	rep := &kicadBoardReport{Op: op}
	o := kicadBoardOpts{pcb: pcb, inPlace: true, drc: true}
	if op == "new" {
		o = kicadBoardOpts{out: pcb, drc: true}
	}
	runKicadBoard(rep, op, spec, o, tools)
	if !rep.OK {
		blob, _ := json.MarshalIndent(rep, "", "  ")
		t.Fatalf("%s: not written (exit %d):\n%s", op, rep.exit, blob)
	}
	for _, c := range rep.Checks {
		if !c.Status.OK() {
			t.Fatalf("%s: check %s %s: %s", op, c.Name, c.Status, c.Detail)
		}
	}
	return rep
}

// boardLiveFinal confirms the finished board: pcbnew snapshot, kicad-cli
// stats and DRC, an SVG render, and what kicad place reads from it.
func boardLiveFinal(t *testing.T, tools *kicad.Tools, pcb string, layers int, thickness float64, npth int) {
	t.Helper()
	st, err := tools.Stats(pcb)
	if err != nil {
		t.Fatal(err)
	}
	if !st.HasOutline || math.Abs(st.Thickness-thickness) > 0.001 || st.NPTH < npth {
		t.Errorf("stats %+v: want an outline, %.3f mm, ≥ %d NPTH", st, thickness, npth)
	}
	raw, err := kicad.Snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		CopperLayers int `json:"copperLayers"`
		Outline      *struct {
			Source string `json:"source"`
		} `json:"outline"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil || snap.CopperLayers != layers || snap.Outline == nil || snap.Outline.Source != "polygon" {
		t.Errorf("snapshot after reload: %v layers %d outline %+v", err, snap.CopperLayers, snap.Outline)
	}
	b, fixed, _, err := kicadBoard(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Outline) < 4 {
		t.Errorf("kicad place sees no board outline: %v", b.Outline)
	}
	holes := 0
	for _, f := range fixed {
		if strings.HasPrefix(f, "H") {
			holes++
		}
	}
	if holes < npth {
		t.Errorf("kicad place keeps %d mounting holes fixed (%v), want ≥ %d", holes, fixed, npth)
	}
	svg := filepath.Join(filepath.Dir(pcb), "render.svg")
	cmd := exec.Command(tools.CLI, "pcb", "export", "svg", "--mode-single", "--exclude-drawing-sheet", "--page-size-mode", "2",
		"--layers", "Edge.Cuts,F.Cu,B.Cu,F.Silkscreen,B.Silkscreen,F.Fab", "-o", svg, pcb)
	cmd.Env = kicad.SchCLIEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("export svg: %v\n%s", err, out)
	}
	data, err := os.ReadFile(svg)
	if err != nil || !strings.Contains(string(data), "<svg") || len(data) < 1000 {
		t.Fatalf("svg %s: %v (%d bytes)", svg, err, len(data))
	}
	t.Logf("%s: %+v, svg %d bytes", filepath.Base(pcb), *st, len(data))
}

// seedNet adds a one-pad through-hole test point on net to pcb (what a
// schematic sync would bring; an empty board has no nets to pour).
func seedNet(t *testing.T, tools *kicad.Tools, pcb, net string, x, y float64) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "seed.py")
	src := `import sys, pcbnew
b = pcbnew.LoadBoard(sys.argv[1])
ni = pcbnew.NETINFO_ITEM(b, sys.argv[2]); b.Add(ni)
fp = pcbnew.FOOTPRINT(b); fp.SetReference("TP1"); fp.SetValue("TestPoint")
at = pcbnew.VECTOR2I(pcbnew.FromMM(float(sys.argv[3])), pcbnew.FromMM(float(sys.argv[4])))
fp.SetPosition(at)
p = pcbnew.PAD(fp); p.SetAttribute(pcbnew.PAD_ATTRIB_PTH); p.SetShape(pcbnew.PAD_SHAPE_CIRCLE)
p.SetSize(pcbnew.VECTOR2I(pcbnew.FromMM(2), pcbnew.FromMM(2))); p.SetDrillSize(pcbnew.VECTOR2I(pcbnew.FromMM(1), pcbnew.FromMM(1)))
p.SetLayerSet(pcbnew.PAD.PTHMask()); p.SetNumber("1"); p.SetPosition(at); p.SetNetCode(ni.GetNetCode())
fp.Add(p); fp.SetAttributes(pcbnew.FP_BOARD_ONLY); b.Add(fp)
pcbnew.SaveBoard(sys.argv[1], b)
`
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(tools.Python, script, pcb, net, ftoa(x), ftoa(y)).CombinedOutput(); err != nil {
		t.Fatalf("seed net: %v\n%s", err, out)
	}
}

func ftoa(v float64) string { b, _ := json.Marshal(v); return string(b) }

// TestKicadBoardLiveEmpty builds a board from nothing: new → outline with a
// rounded corner, a slot and a round cutout → 4-layer ENIG stackup → four
// GND pour → NPTH holes + a GND PTH → keepout → silk text, each op with
// --drc (no new DRC error), then reload, stats, svg and the place model.
func TestKicadBoardLiveEmpty(t *testing.T) {
	tools := boardLiveTools(t)
	pcb := filepath.Join(t.TempDir(), "empty.kicad_pcb")
	boardLiveOp(t, tools, "new", pcb, &kicad.NewBoardSpec{Layers: 2})
	seedNet(t, tools, pcb, "GND", 40, 15)
	boardLiveOp(t, tools, "outline", pcb, &kicad.OutlineSpec{
		Outer: kicad.BoardShape{Rect: &kicad.BoardRect{X: 0, Y: 0, Width: 80, Height: 50, CornerRadius: 3}},
		Cutouts: []kicad.BoardShape{
			{Slot: &kicad.BoardSlot{X1: 20, Y1: 30, X2: 40, Y2: 30, Width: 2}},
			{Circle: &kicad.BoardCircle{X: 65, Y: 30, Diameter: 4}},
		}})
	boardLiveOp(t, tools, "stackup", pcb, &kicad.StackupSpec{Layers: 4, Thickness: 1.6, Finish: "ENIG"})
	// The pour first: the GND PTH below connects through it (holes refill).
	z := boardLiveOp(t, tools, "zone", pcb, &kicad.ZoneSpec{BoardOutline: true, Net: "GND", Layers: []string{"In1.Cu", "B.Cu"}, Clearance: 0.3, Priority: 1})
	if a, _ := z.Summary["filledAreaMm2"].(float64); a < 1000 {
		t.Errorf("GND pour filled %.1f mm², want most of the 80 x 50 board", a)
	}
	boardLiveOp(t, tools, "holes", pcb, &kicad.HolesSpec{Holes: []kicad.HoleSpec{
		{X: 4, Y: 4, Drill: 3.2}, {X: 76, Y: 4, Drill: 3.2}, {X: 4, Y: 46, Drill: 3.2}, {X: 76, Y: 46, Drill: 3.2},
		{X: 40, Y: 6, Drill: 2.2, Pad: 4, Type: "pth", Net: "GND"}}})
	boardLiveOp(t, tools, "keepout", pcb, &kicad.KeepoutSpec{Shape: kicad.BoardShape{Rect: &kicad.BoardRect{X: 55, Y: 5, Width: 12, Height: 8}}, Name: "antenna"})
	boardLiveOp(t, tools, "text", pcb, &kicad.TextSpec{Text: "pcbpilot board rev A", X: 40, Y: 44, Size: 1.2, Thickness: 0.18})
	boardLiveFinal(t, tools, pcb, 4, 1.6, 4)
	// The place model keeps the slot as its polygon, not a 2 mm circle.
	raw, err := kicad.Snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := kicadBoard(raw)
	if err != nil {
		t.Fatal(err)
	}
	slot := false
	for _, h := range b.Holes {
		if bb := h.Bounds(); len(h.Poly) >= 3 && bb.W() > 700 { // 22 mm = 866 mil
			slot = true
		}
	}
	if !slot {
		t.Errorf("kicad place model has no slot polygon: %+v", b.Holes)
	}
}

// TestKicadBoardLiveDemos runs every op on copies of KiCad demo boards: the
// outline is replaced by one 8 mm larger on every side, the new objects go
// in that margin, and each op must add no DRC error.
func TestKicadBoardLiveDemos(t *testing.T) {
	tools := boardLiveTools(t)
	demos := os.Getenv("PCBPILOT_KICAD_DEMOS")
	if demos == "" {
		demos = filepath.Join(filepath.Dir(filepath.Dir(tools.CLI)), "share", "kicad", "demos")
	}
	for _, d := range []struct{ dir, name string }{{"ecc83", "ecc83-pp"}, {"pic_programmer", "pic_programmer"}} {
		t.Run(d.name, func(t *testing.T) {
			src := filepath.Join(demos, d.dir, d.name+".kicad_pcb")
			if _, err := os.Stat(src); err != nil {
				t.Skip(err)
			}
			pcb := filepath.Join(t.TempDir(), d.name+".kicad_pcb")
			if _, err := copyKicadBoard(src, pcb); err != nil {
				t.Fatal(err)
			}
			raw, err := kicad.Snapshot(pcb)
			if err != nil {
				t.Fatal(err)
			}
			var snap struct {
				CopperLayers int `json:"copperLayers"`
				Outline      struct {
					BBox struct{ MinX, MinY, MaxX, MaxY float64 } `json:"bbox"`
				} `json:"outline"`
			}
			if err := json.Unmarshal(raw, &snap); err != nil {
				t.Fatal(err)
			}
			const mmPerMil = 0.0254
			x0, x1 := snap.Outline.BBox.MinX*mmPerMil, snap.Outline.BBox.MaxX*mmPerMil
			y0, y1 := -snap.Outline.BBox.MaxY*mmPerMil, -snap.Outline.BBox.MinY*mmPerMil // y-up mil → KiCad mm
			if x1-x0 < 20 || y1-y0 < 20 {
				t.Fatalf("demo outline bbox %v", snap.Outline.BBox)
			}
			const m = 8.0
			boardLiveOp(t, tools, "outline", pcb, &kicad.OutlineSpec{Replace: true,
				Outer: kicad.BoardShape{Rect: &kicad.BoardRect{X: x0 - m, Y: y0 - m, Width: x1 - x0 + 2*m, Height: y1 - y0 + 2*m, CornerRadius: 2}}})
			layers := snap.CopperLayers + 2
			if layers > 6 {
				layers = snap.CopperLayers
			}
			boardLiveOp(t, tools, "stackup", pcb, &kicad.StackupSpec{Layers: layers, Thickness: 1.6, Finish: "lead-free-hasl"})
			boardLiveOp(t, tools, "zone", pcb, &kicad.ZoneSpec{BoardOutline: true, Net: "GND", Layers: []string{"In1.Cu"}, Connection: "tht-thermal"})
			cx := (x0 + x1) / 2
			boardLiveOp(t, tools, "holes", pcb, &kicad.HolesSpec{Holes: []kicad.HoleSpec{
				{X: x0 - m/2, Y: y0 - m/2, Drill: 3.2}, {X: x1 + m/2, Y: y0 - m/2, Drill: 3.2},
				{X: x0 - m/2, Y: y1 + m/2, Drill: 3.2}, {X: x1 + m/2, Y: y1 + m/2, Drill: 3.2},
				{X: cx, Y: y0 - m/2, Drill: 2.2, Pad: 3.5, Type: "pth", Net: "GND"}}})
			boardLiveOp(t, tools, "keepout", pcb, &kicad.KeepoutSpec{Shape: kicad.BoardShape{Rect: &kicad.BoardRect{X: x0 + 5, Y: y1 + 1.5, Width: 10, Height: 5}},
				Forbid: []string{"tracks", "vias", "footprints"}, Name: "edge keepout"})
			boardLiveOp(t, tools, "text", pcb, &kicad.TextSpec{Text: "pcbpilot " + d.name, X: cx, Y: y1 + m/2, Size: 1.2, Thickness: 0.18})
			boardLiveFinal(t, tools, pcb, layers, 1.6, 4)
		})
	}
}

// boardLiveRefused runs one op that must be refused (or fail a check):
// nothing written, the board unchanged, the issue code as expected.
func boardLiveRefused(t *testing.T, tools *kicad.Tools, op string, o kicadBoardOpts, spec kicad.BoardOpSpec, exit int, code string) *kicadBoardReport {
	t.Helper()
	before, _ := os.ReadFile(o.pcb)
	rep := &kicadBoardReport{Op: op}
	runKicadBoard(rep, op, spec, o, tools)
	found := false
	for _, is := range rep.Issues {
		found = found || is.Code == code
	}
	if rep.OK || rep.exit != exit || !found || len(rep.Written) != 0 {
		blob, _ := json.MarshalIndent(rep, "", "  ")
		t.Fatalf("%s: want exit %d with %s, nothing written:\n%s", op, exit, code, blob)
	}
	if after, _ := os.ReadFile(o.pcb); string(after) != string(before) {
		t.Fatalf("%s: the refused op changed %s", op, o.pcb)
	}
	return rep
}

// seedVia adds a through via on net at (x, y) mm.
func seedVia(t *testing.T, tools *kicad.Tools, pcb, net string, x, y float64) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "via.py")
	src := `import sys, pcbnew
b = pcbnew.LoadBoard(sys.argv[1])
v = pcbnew.PCB_VIA(b); v.SetPosition(pcbnew.VECTOR2I(pcbnew.FromMM(float(sys.argv[3])), pcbnew.FromMM(float(sys.argv[4]))))
v.SetWidth(pcbnew.FromMM(0.8)); v.SetDrill(pcbnew.FromMM(0.4)); v.SetNet(b.FindNet(sys.argv[2])); b.Add(v)
pcbnew.SaveBoard(sys.argv[1], b)
`
	if err := os.WriteFile(script, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(tools.Python, script, pcb, net, ftoa(x), ftoa(y)).CombinedOutput(); err != nil {
		t.Fatalf("seed via: %v\n%s", err, out)
	}
}

// TestKicadBoardLiveRefusals: holes without an outline, holes on a via or
// too close to its drill, and a custom rule of the input's .kicad_dru that
// only the result violates (the post-write DRC must see the rules).
func TestKicadBoardLiveRefusals(t *testing.T) {
	tools := boardLiveTools(t)
	dir := t.TempDir()
	pcb := filepath.Join(dir, "r.kicad_pcb")
	boardLiveOp(t, tools, "new", pcb, &kicad.NewBoardSpec{Layers: 2})
	seedNet(t, tools, pcb, "GND", 40, 15)
	in := kicadBoardOpts{pcb: pcb, inPlace: true}
	hole := &kicad.HolesSpec{Holes: []kicad.HoleSpec{{X: 10, Y: 10, Drill: 3.2}}}
	boardLiveRefused(t, tools, "holes", in, hole, 2, kicad.ErrBoardNoOutline)

	boardLiveOp(t, tools, "outline", pcb, &kicad.OutlineSpec{Outer: kicad.BoardShape{Rect: &kicad.BoardRect{Width: 80, Height: 50}},
		Cutouts: []kicad.BoardShape{{Rect: &kicad.BoardRect{X: 30, Y: 30, Width: 2, Height: 2}}}})
	// A hole enclosing the cutout: no boundary of it is crossed.
	boardLiveRefused(t, tools, "holes", in, &kicad.HolesSpec{Holes: []kicad.HoleSpec{{X: 31, Y: 31, Drill: 8}}}, 2, kicad.ErrBoardGeometry)
	// A cutout that only touches the edge (0.0005 mm) is refused, one 0.05 mm in fits.
	boardLiveRefused(t, tools, "outline", in, &kicad.OutlineSpec{Cutouts: []kicad.BoardShape{{Circle: &kicad.BoardCircle{X: 10, Y: 1.0005, Diameter: 2}}}}, 2, kicad.ErrBoardGeometry)

	seedVia(t, tools, pcb, "GND", 60, 25)
	boardLiveRefused(t, tools, "holes", in, &kicad.HolesSpec{Holes: []kicad.HoleSpec{{X: 61, Y: 25, Drill: 2.2, Pad: 3.5, Type: "pth", Net: "GND"}}}, 2, kicad.ErrBoardOverlap)
	boardLiveRefused(t, tools, "holes", in, &kicad.HolesSpec{Holes: []kicad.HoleSpec{{X: 61.5, Y: 25, Drill: 2}}}, 2, kicad.ErrBoardOverlap)
	boardLiveOp(t, tools, "holes", pcb, &kicad.HolesSpec{Holes: []kicad.HoleSpec{{X: 70, Y: 25, Drill: 2}}})

	// A rule only the result breaks: no board text on B.Silkscreen.
	dru := []byte("(version 1)\n(rule \"pcbpilot test: no back text\"\n  (constraint disallow text)\n  (condition \"A.Layer == 'B.Silkscreen'\"))\n")
	if err := os.WriteFile(filepath.Join(dir, "r.kicad_dru"), dru, 0o644); err != nil {
		t.Fatal(err)
	}
	back := &kicad.TextSpec{Text: "back", X: 40, Y: 40, Layer: "B.Silkscreen"}
	rep := boardLiveRefused(t, tools, "text", kicadBoardOpts{pcb: pcb, inPlace: true, drc: true}, back, 6, kicad.ErrBoardVerify)
	if rep.DRC == nil || len(rep.DRC.New) == 0 || !rep.DRC.New[0].Ours {
		t.Fatalf("custom rule violation not reported as ours: %+v", rep.DRC)
	}
	out := filepath.Join(dir, "o", "r2.kicad_pcb")
	rep = boardLiveRefused(t, tools, "text", kicadBoardOpts{pcb: pcb, out: out, drc: true}, back, 6, kicad.ErrBoardVerify)
	// Front text is allowed; the --out result carries the verified project and rules.
	w := &kicadBoardReport{}
	runKicadBoard(w, "text", &kicad.TextSpec{Text: "front", X: 40, Y: 40}, kicadBoardOpts{pcb: pcb, out: out, drc: true}, tools)
	if !w.OK {
		t.Fatalf("front text: %+v", w)
	}
	for _, ext := range []string{".kicad_dru", ".kicad_pro"} {
		src, _ := os.ReadFile(strings.TrimSuffix(pcb, ".kicad_pcb") + ext)
		got, err := os.ReadFile(strings.TrimSuffix(out, ".kicad_pcb") + ext)
		if err != nil || string(got) != string(src) {
			t.Errorf("--out %s differs from the source (%v)", ext, err)
		}
	}
}
