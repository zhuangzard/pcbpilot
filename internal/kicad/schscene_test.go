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
	res, err := e.DragSymbols(map[string]SymPose{"R2": {At: Pt{62.23, 49.53}, Rot: 90}, "R1": {At: Pt{50.8, 50.8}}})
	if err != nil {
		t.Fatal(err)
	}
	// R1 stays (its label, wire end and power symbol too); R2's pin 1 wire end
	// and pin 2 power symbol follow it.
	if res.Symbols != 2 || res.WirePoints != 2 || res.Labels != 1 || res.Powers != 2 {
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
