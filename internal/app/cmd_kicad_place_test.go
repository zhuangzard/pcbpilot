package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// tinyKicadSnapshot is a synthetic KiCad snapshot (mil, y-up, so KiCad's
// y-down board sits at negative y): an 8-pin IC with decaps and resistors,
// a bottom-side resistor, a locked connector and a mounting hole.
func tinyKicadSnapshot() []byte {
	type pad = map[string]any
	comp := func(ref, fp string, x, y, rot float64, layer int, locked bool, half [2]float64, pads []pad) map[string]any {
		for _, p := range pads {
			p["x"] = x + p["x"].(float64)
			p["y"] = y + p["y"].(float64)
			p["width"], p["height"], p["layer"] = 30.0, 30.0, layer
			p["shape"] = []any{"RECT", 30.0, 30.0}
		}
		box := map[string]any{"minX": x - half[0], "minY": y - half[1], "maxX": x + half[0], "maxY": y + half[1]}
		return map[string]any{"primitiveId": "id-" + ref, "designator": ref, "device": ref, "footprint": fp,
			"layer": layer, "x": x, "y": y, "rotation": rot, "locked": locked, "bbox": box, "courtyard": box, "pads": pads}
	}
	two := func(n1, n2 string) []pad {
		return []pad{{"padNumber": "1", "net": n1, "x": -30.0, "y": 0.0}, {"padNumber": "2", "net": n2, "x": 30.0, "y": 0.0}}
	}
	var ic []pad
	nets := []string{"VCC", "A", "B", "GND", "C", "D", "VCC", "GND"}
	for i, n := range nets {
		x, y := -100.0, 75-float64(i%4)*50
		if i >= 4 {
			x = 100
		}
		ic = append(ic, pad{"padNumber": fmt.Sprint(i + 1), "net": n, "x": x, "y": y})
	}
	comps := []any{
		comp("U1", "Package_SO:SOIC-8", 1000, -800, 0, 1, false, [2]float64{140, 110}, ic),
		comp("C1", "Capacitor_SMD:C_0603", 400, -300, 0, 1, false, [2]float64{60, 30}, two("VCC", "GND")),
		comp("C2", "Capacitor_SMD:C_0603", 1600, -300, 90, 1, false, [2]float64{60, 30}, two("VCC", "GND")),
		comp("R1", "Resistor_SMD:R_0603", 400, -1300, 0, 1, false, [2]float64{60, 30}, two("A", "C")),
		comp("R2", "Resistor_SMD:R_0603", 1600, -1300, 0, 1, false, [2]float64{60, 30}, two("B", "D")),
		comp("R3", "Resistor_SMD:R_0603", 1000, -300, 180, 2, false, [2]float64{60, 30}, two("A", "GND")),
		comp("J1", "Connector:Header_1x02", 100, -800, 90, 1, true, [2]float64{60, 110}, []pad{
			{"padNumber": "1", "net": "VCC", "x": 0.0, "y": 50.0}, {"padNumber": "2", "net": "GND", "x": 0.0, "y": -50.0}}),
		comp("H1", "MountingHole:MountingHole_3.2mm_M3", 1850, -150, 0, 1, false, [2]float64{70, 70}, nil),
	}
	var silk []any
	for _, c := range comps {
		m := c.(map[string]any)
		x, y := m["x"].(float64), m["y"].(float64)+150
		silk = append(silk, map[string]any{"Kind": "attribute", "Key": "Designator", "Text": m["designator"], "Layer": 3,
			"FontSize": 39.37, "BBox": map[string]any{"MinX": x - 40, "MinY": y - 22, "MaxX": x + 40, "MaxY": y + 22}})
	}
	snap := map[string]any{
		"components":     comps,
		"outline":        map[string]any{"bbox": map[string]any{"minX": 0, "minY": -1600, "maxX": 2000, "maxY": 0}, "points": [][2]float64{{0, 0}, {2000, 0}, {2000, -1600}, {0, -1600}}, "source": "polygon"},
		"silk":           silk,
		"copperLayers":   2,
		"rules":          map[string]any{"clearanceMil": 6, "trackWidthMil": 8, "copperToEdgeMil": 20},
		"footprintHoles": []any{map[string]any{"owner": "H1", "sourceId": "pad1", "shape": "circle", "x": 1850, "y": -150, "dia": 126}},
	}
	raw, _ := json.Marshal(snap)
	return raw
}

func TestKicadBoardCourtyardAndMountingHoles(t *testing.T) {
	b, fixed, err := kicadBoard(tinyKicadSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(fixed, ",") != "J1,H1" {
		t.Fatalf("fixed = %v, want the locked J1 and the mounting hole H1", fixed)
	}
	// The courtyard is the body exactly (FromSnapshot alone shrinks the
	// bbox toward the pads).
	if got := b.Part("U1").Body(); math.Abs(got.W()-280) > 1e-6 || math.Abs(got.H()-220) > 1e-6 {
		t.Fatalf("U1 body %+v, want the 280×220 courtyard", got)
	}
	if b.Part("R3").Side != pcbauto.LayerBottom {
		t.Fatal("R3 must stay on the bottom")
	}
}

func TestPosesFromBoardFlipsYKeepsAngleAndSide(t *testing.T) {
	b, _, err := kicadBoard(tinyKicadSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	b.Part("R3").MoveTo(pcbauto.Point{X: 700, Y: -900}, 270)
	p := posesFromBoard(b)
	if got := p["R3"]; got != (kicad.Pose{XMil: 700, YMil: 900, RotationDeg: 270, Side: "bottom"}) {
		t.Fatalf("R3 pose %+v", got)
	}
	if got := p["U1"]; got.YMil != 800 || got.Side != "top" {
		t.Fatalf("U1 pose %+v", got)
	}
}

func TestRunKicadPlaceWritesBestAndRunnersUp(t *testing.T) {
	dir := t.TempDir()
	pcb := filepath.Join(dir, "in.kicad_pcb")
	if err := os.WriteFile(strings.TrimSuffix(pcb, ".kicad_pcb")+".kicad_pro", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{"VCC":{"role":"power","currentA":1.5}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	written := map[string]map[string]kicad.Pose{}
	deps := kicadPlaceDeps{
		snapshot: func(string) ([]byte, error) { return tinyKicadSnapshot(), nil },
		place: func(src string, poses map[string]kicad.Pose, out string) (*kicad.PlaceResult, error) {
			if src != pcb {
				t.Fatalf("placed from %s", src)
			}
			written[out] = poses
			return &kicad.PlaceResult{Moved: len(poses)}, nil
		},
	}
	out := filepath.Join(dir, "out", "board.kicad_pcb")
	rep, err := runKicadPlace(kicadPlaceOpts{pcb: pcb, out: out, intent: intent, placeSeeds: 3, candidates: 2, seed: 1}, deps, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 2 || written[out] == nil {
		t.Fatalf("wrote %v, want the best at --out plus one runner-up", kicadPlaceKeys(written))
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "board.kicad_pro")); err != nil {
		t.Fatal("the .kicad_pro was not copied next to the best board")
	}
	best := written[out]
	// Fixed parts keep their KiCad pose (y back to y-down).
	if j := best["J1"]; j.XMil != 100 || j.YMil != 800 || j.RotationDeg != 90 {
		t.Fatalf("locked J1 moved: %+v", j)
	}
	if h := best["H1"]; h.XMil != 1850 || h.YMil != 150 {
		t.Fatalf("mounting hole H1 moved: %+v", h)
	}
	if best["R3"].Side != "bottom" {
		t.Fatalf("R3 side %q", best["R3"].Side)
	}
	for ref, p := range best {
		if p.XMil < 0 || p.XMil > 2000 || p.YMil < 0 || p.YMil > 1600 {
			t.Fatalf("%s placed off the board: %+v", ref, p)
		}
	}
	if len(rep.Seeds) != 3 || rep.Labels == nil {
		t.Fatalf("report: %d seeds, labels %v", len(rep.Seeds), rep.Labels)
	}
	files := 0
	for _, s := range rep.Seeds {
		if s.Err != "" {
			t.Fatalf("seed %d: %s", s.Seed, s.Err)
		}
		if s.LoopIR == nil || *s.LoopIR <= 0 {
			t.Fatalf("seed %d: loopIR %v, want > 0 with the 1.5 A VCC intent", s.Seed, s.LoopIR)
		}
		if s.File != "" {
			files++
		}
		if s.Seed == rep.BestSeed && s.File != out {
			t.Fatalf("best seed %d written to %q", s.Seed, s.File)
		}
	}
	if files != 2 {
		t.Fatalf("%d seeds carry a file, want 2", files)
	}
	var onDisk kicadPlaceReport
	raw, err := os.ReadFile(filepath.Join(dir, "out", "board.report.json"))
	if err != nil || json.Unmarshal(raw, &onDisk) != nil || onDisk.BestSeed != rep.BestSeed {
		t.Fatalf("report JSON not written: %v", err)
	}
}

func TestRunKicadPlaceRefusesToOverwriteInput(t *testing.T) {
	_, err := runKicadPlace(kicadPlaceOpts{pcb: "a.kicad_pcb", out: "./a.kicad_pcb"}, kicadPlaceDeps{}, io.Discard)
	if err == nil {
		t.Fatal("writing over the input must fail")
	}
}

func kicadPlaceKeys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
