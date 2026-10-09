package kicad

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/auto.kicad_sch: R1 (50.8,50.8) and R2 (127,50.8) joined pin 1 to
// pin 1 by a wire labelled SIG, pin 2 of each on a GND power symbol; R3
// (88.9,88.9) unconnected. Made with `sch place/wire/netflag --backend kicad`.
func openAuto(t *testing.T) *SchEditor {
	t.Helper()
	e, err := OpenSchematicFile("testdata/auto.kicad_sch")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestSchScene(t *testing.T) {
	sc, err := openAuto(t).Scene()
	if err != nil {
		t.Fatal(err)
	}
	if sc.TitleBlock == nil || sc.TitleBlock.MaxX != 287 || sc.TitleBlock.MaxY != 200 {
		t.Fatalf("A4 title block: %+v", sc.TitleBlock)
	}
	if len(sc.Wires) != 3 || len(sc.Labels) != 1 || sc.Labels[0].Name != "SIG" {
		t.Fatalf("wires %d labels %+v", len(sc.Wires), sc.Labels)
	}
	powers := 0
	for _, s := range sc.Symbols {
		if s.Power {
			powers++
			continue
		}
		if s.Ref != "R3" {
			continue
		}
		if !s.HasBox || s.Box.MinY > 85.09 || s.Box.MaxY < 92.71 {
			t.Errorf("R3 box %+v", s.Box)
		}
		want := map[string]ScenePin{"1": {At: Pt{88.9, 85.09}, Outward: 90}, "2": {At: Pt{88.9, 92.71}, Outward: 270}}
		for _, p := range s.Pins {
			w := want[p.Number]
			if !samePt(p.At, w.At) || p.Outward != w.Outward {
				t.Errorf("R3 pin %s: %+v, want %+v", p.Number, p, w)
			}
		}
	}
	if powers != 2 {
		t.Errorf("%d power symbols, want 2", powers)
	}
	// rotated 90° counter-clockwise, pin 1 (top) points left
	pins, err := openAuto(t).PinsAt("my:R", 1, Pt{0, 0}, 90, "")
	if err != nil || pins[0].Number != "1" || !samePt(pins[0].At, Pt{-3.81, 0}) || pins[0].Outward != 180 {
		t.Fatalf("rotated pins %+v %v", pins, err)
	}
	if r, err := RootSheetFor("testdata/auto.kicad_sch"); err != nil || r != "testdata/auto.kicad_sch" {
		t.Fatalf("root %q %v", r, err)
	}
}

func TestDragSymbols(t *testing.T) {
	e := openAuto(t)
	res, err := e.DragSymbolsOpt(map[string]SymPose{"R2": {At: Pt{62.23, 49.53}, Rot: 90}, "R1": {At: Pt{50.8, 50.8}}}, DragOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// R1 stays; the SIG wire between R1 and the moved R2 is re-routed, R2's
	// GND symbol (directly on pin 2) turns with it.
	if res.Symbols != 1 || res.Rerouted != 1 || res.Powers != 1 || res.NewWires == 0 || len(res.Fallback) != 0 {
		t.Fatalf("%+v", res)
	}
	text, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	e2, err := OpenSchematic(text)
	if err != nil {
		t.Fatal(err)
	}
	sc, _ := e2.Scene()
	ends := map[Pt]bool{}
	for _, w := range sc.Wires {
		ends[w[0]], ends[w[1]] = true, true
		if w[0].X != w[1].X && w[0].Y != w[1].Y {
			t.Errorf("diagonal wire %v", w)
		}
		if !OnGrid(w[0]) || !OnGrid(w[1]) {
			t.Errorf("off-grid wire %v", w)
		}
	}
	pins, _ := e2.SymbolPinPositions("R2")
	if !samePt(pins["1"], Pt{58.42, 49.53}) || !ends[pins["1"]] {
		t.Fatalf("R2 pin 1 %v not on a wire end %v", pins["1"], ends)
	}
	for _, s := range sc.Symbols {
		if s.Power && samePt(s.At, Pt{127, 54.61}) {
			t.Error("R2's GND symbol stayed behind")
		}
	}
	if !strings.Contains(text, "(at 62.23 49.53 90)") {
		t.Error("R2 not rotated")
	}
	if fs := CheckSchematic(text, CheckOptions{}); len(fs) > 0 {
		t.Errorf("quality findings after the drag: %+v", fs)
	}
	if _, err := openAuto(t).DragSymbols(map[string]SymPose{"R9": {}}); err == nil {
		t.Error("unknown reference accepted")
	}
	if _, err := KicadCLI(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.kicad_sch"), filepath.Join(dir, "b.kicad_sch")
	src, _ := os.ReadFile("testdata/auto.kicad_sch")
	_ = os.WriteFile(a, src, 0o644)
	_ = os.WriteFile(b, []byte(text), 0o644)
	na, err := ExportSchNetlist(a)
	if err != nil {
		t.Fatal(err)
	}
	nb, err := ExportSchNetlist(b)
	if err != nil {
		t.Fatal(err)
	}
	if cmp := ComparePinNets(na.PinNets(), nb.PinNets(), nil); !cmp.Equal || cmp.NamesEqual != cmp.NetsA {
		t.Fatalf("drag changed the netlist: %+v", cmp)
	}
}

func TestSetTitleBlock(t *testing.T) {
	e := openAuto(t)
	if err := e.SetTitleBlock(TitleBlock{Title: "Gas Module", Rev: "B", Date: "2026-10-09", Company: "ACME", Comments: map[int]string{2: "checked"}}); err != nil {
		t.Fatal(err)
	}
	text, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := OpenSchematic(text)
	if err := e2.SetTitleBlock(TitleBlock{Rev: "C"}); err != nil {
		t.Fatal(err)
	}
	text, _ = e2.Render()
	e3, _ := OpenSchematic(text)
	tb := e3.TitleBlock()
	if tb.Title != "Gas Module" || tb.Rev != "C" || tb.Date != "2026-10-09" || tb.Company != "ACME" || tb.Comments[2] != "checked" {
		t.Fatalf("%+v", tb)
	}
	if strings.Count(text, "(title_block") != 1 {
		t.Fatal("title block duplicated")
	}
	if e.SetTitleBlock(TitleBlock{Comments: map[int]string{10: "x"}}) == nil {
		t.Fatal("comment 10 accepted")
	}
}

func TestDestaggerFixesOverlap(t *testing.T) {
	// R3's pin 1 gets a long label pointing right over R2… no: a label whose
	// text runs onto R3's own Reference field; destagger must turn it.
	e := openAuto(t)
	e.AddWire(Pt{88.9, 85.09}, Pt{88.9, 82.55})
	if err := e.AddLabel(LabelLocal, "A_LONG_LABEL_NAME", Pt{88.9, 82.55}, 0, ""); err != nil {
		t.Fatal(err)
	}
	e.AddWire(Pt{88.9, 92.71}, Pt{88.9, 95.25})
	if _, err := e.AddPower("GND", Pt{88.9, 95.25}, 0, true); err != nil {
		t.Fatal(err)
	}
	// a second label right of R3 that the first one runs into
	e.AddWire(Pt{101.6, 82.55}, Pt{101.6, 80.01})
	text, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	fs := CheckSchematic(text, CheckOptions{Ignore: []string{FDiagonalWire}})
	if len(fs) == 0 {
		t.Fatal("fixture has no finding to fix")
	}
	r, err := Destagger(text, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Moves) == 0 || r.After >= r.Before {
		t.Fatalf("%+v (findings %s)", r, Summary(fs, 5))
	}
	t.Logf("%+v", r.Moves)
}
