package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// largeKicadSnapshot is a synthetic KiCad snapshot (mil, y-up, the board at
// negative y like KiCad's y-down) of a dense board with about `modules`×17
// parts: per module a 48-pin QFN with six decaps, eight series resistors and
// two filter caps, the ICs chained by a 4-bit bus, plus edge connectors and
// four mounting holes. The parts start in a pile in one corner, the way a
// freshly synced board has them; utilisation is about the real 1443-part
// board's (~45 % courtyard area). Deterministic for a given modules count.
func largeKicadSnapshot(modules int) []byte {
	rng := rand.New(rand.NewSource(int64(modules)))
	type pad = map[string]any
	const w, h = 10000.0, 7800.0
	var comps []any
	comp := func(ref, fp string, x, y float64, half [2]float64, pads []pad) {
		for _, p := range pads {
			p["x"] = x + p["x"].(float64)
			p["y"] = y + p["y"].(float64)
			if p["width"] == nil {
				p["width"], p["height"] = 24.0, 24.0
			}
			p["layer"] = 1
			p["shape"] = []any{"RECT", p["width"], p["height"]}
		}
		box := map[string]any{"minX": x - half[0], "minY": y - half[1], "maxX": x + half[0], "maxY": y + half[1]}
		comps = append(comps, map[string]any{"primitiveId": "id-" + ref, "designator": ref, "device": ref, "footprint": fp,
			"layer": 1, "x": x, "y": y, "rotation": 0.0, "locked": false, "bbox": box, "courtyard": box, "pads": pads})
	}
	two := func(n1, n2 string, pitch float64) []pad {
		return []pad{{"padNumber": "1", "net": n1, "x": -pitch / 2, "y": 0.0}, {"padNumber": "2", "net": n2, "x": pitch / 2, "y": 0.0}}
	}
	// The pile: every part starts on a coarse grid in the lower-left corner.
	pile := func() (float64, float64) {
		return 300 + rng.Float64()*2500, -h + 300 + rng.Float64()*2000
	}
	nr, nc := 0, 0
	for m := 0; m < modules; m++ {
		vcc := "VCC"
		if m%3 == 1 {
			vcc = "VDD_IO"
		}
		var ic []pad
		for i := 0; i < 48; i++ {
			side, k := i/12, float64(i%12)-5.5
			x, y := [4]float64{-150, k * 20, 150, -k * 20}[side], [4]float64{k * 20, -150, -k * 20, 150}[side]
			net := fmt.Sprintf("M%d_S%d", m, i%16)
			switch {
			case i%8 == 0:
				net = vcc
			case i%8 == 4:
				net = "GND"
			case i >= 40 && m+1 < modules:
				net = fmt.Sprintf("BUS%d_%d", m, i-40) // to the next module's IC
			case i >= 32 && i < 36 && m > 0:
				net = fmt.Sprintf("BUS%d_%d", m-1, i-32)
			}
			ic = append(ic, pad{"padNumber": fmt.Sprint(i + 1), "net": net, "x": x, "y": y, "width": 12.0, "height": 30.0})
		}
		x, y := pile()
		comp(fmt.Sprintf("U%d", m+1), "Package_DFN_QFN:QFN-48", x, y, [2]float64{170, 170}, ic)
		for d := 0; d < 6; d++ {
			nc++
			x, y := pile()
			comp(fmt.Sprintf("C%d", nc), "Capacitor_SMD:C_0402", x, y, [2]float64{40, 20}, two(vcc, "GND", 40))
		}
		for r := 0; r < 8; r++ {
			nr++
			x, y := pile()
			comp(fmt.Sprintf("R%d", nr), "Resistor_SMD:R_0603", x, y, [2]float64{60, 30}, two(fmt.Sprintf("M%d_S%d", m, r+1), fmt.Sprintf("M%d_O%d", m, r), 60))
		}
		for f := 0; f < 2; f++ {
			nc++
			x, y := pile()
			comp(fmt.Sprintf("C%d", nc), "Capacitor_SMD:C_0603", x, y, [2]float64{60, 30}, two(fmt.Sprintf("M%d_O%d", m, f), "GND", 60))
		}
	}
	for j := 0; j < 6; j++ {
		var pads []pad
		for i := 0; i < 10; i++ {
			net := fmt.Sprintf("M%d_O%d", (j*7)%max(modules, 1), 2+i%6)
			if i == 0 {
				net = "GND"
			}
			pads = append(pads, pad{"padNumber": fmt.Sprint(i + 1), "net": net, "x": -225 + float64(i)*50, "y": 0.0, "width": 40.0, "height": 70.0})
		}
		x, y := pile()
		comp(fmt.Sprintf("J%d", j+1), "Connector:Header_1x10", x, y, [2]float64{260, 60}, pads)
	}
	for i, at := range [][2]float64{{200, -200}, {w - 200, -200}, {200, -h + 200}, {w - 200, -h + 200}} {
		comp(fmt.Sprintf("H%d", i+1), "MountingHole:MountingHole_3.2mm_M3", at[0], at[1], [2]float64{70, 70}, nil)
	}
	var silk []any
	var holes []any
	for _, c := range comps {
		m := c.(map[string]any)
		x, y := m["x"].(float64), m["y"].(float64)
		silk = append(silk, map[string]any{"Kind": "attribute", "Key": "Designator", "Text": m["designator"], "Layer": 3,
			"FontSize": 31.5, "BBox": map[string]any{"MinX": x - 40, "MinY": y + 60, "MaxX": x + 40, "MaxY": y + 95}})
		if ref := m["designator"].(string); ref[0] == 'H' {
			holes = append(holes, map[string]any{"owner": ref, "sourceId": "pad1", "shape": "circle", "x": x, "y": y, "dia": 126})
		}
	}
	snap := map[string]any{
		"components":     comps,
		"outline":        map[string]any{"bbox": map[string]any{"minX": 0, "minY": -h, "maxX": w, "maxY": 0}, "points": [][2]float64{{0, 0}, {w, 0}, {w, -h}, {0, -h}}, "source": "polygon"},
		"silk":           silk,
		"copperLayers":   6,
		"rules":          map[string]any{"clearanceMil": 5, "trackWidthMil": 5, "copperToEdgeMil": 12},
		"footprintHoles": holes,
	}
	raw, _ := json.Marshal(snap)
	return raw
}

// largePlaceRun places the synthetic large board with stubbed KiCad.
func largePlaceRun(t *testing.T, modules, seeds int, budget time.Duration) (*kicadPlaceReport, string, time.Duration) {
	t.Helper()
	snap := largeKicadSnapshot(modules)
	dir := t.TempDir()
	o := kicadPlaceOpts{pcb: filepath.Join(dir, "in.kicad_pcb"), out: filepath.Join(dir, "out", "big.kicad_pcb"),
		placeSeeds: seeds, candidates: 1, seed: 1, timeBudget: budget}
	var stderr bytes.Buffer
	start := time.Now()
	rep, err := runKicadPlace(o, kicadPlaceDeps{
		snapshot: func(string) ([]byte, error) { return snap, nil },
		place: func(string, map[string]kicad.Pose, string) (*kicad.PlaceResult, error) {
			return &kicad.PlaceResult{}, nil
		},
	}, &syncWriter{w: &stderr})
	el := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	return rep, stderr.String(), el
}

func TestLargeKicadSnapshotShape(t *testing.T) {
	b, fixed, _, err := kicadBoard(largeKicadSnapshot(85))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(b.Parts); n < 1400 || n > 1500 {
		t.Fatalf("synthetic board has %d parts, want ~1450", n)
	}
	if len(fixed) != 4 {
		t.Fatalf("fixed %v, want the four mounting holes", fixed)
	}
}

// A 1450-part board with a short budget: every seed stops at the deadline
// and returns its best placement so far, flagged in the report and on
// stderr, with progress lines on the way.
func TestKicadPlaceLargeBoardTimeBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("places a 1450-part board")
	}
	budget := 20 * time.Second
	rep, stderr, el := largePlaceRun(t, 85, 2, budget)
	// Setup, construction and the final metrics run past the deadline; the
	// search phases must not.
	if el > budget+90*time.Second {
		t.Fatalf("placement took %s with a %s budget", el, budget)
	}
	if !rep.BudgetHit || rep.TimeBudget != budget.String() {
		t.Fatalf("report budgetHit %v timeBudget %q, want the cut flagged", rep.BudgetHit, rep.TimeBudget)
	}
	for _, s := range rep.Seeds {
		if s.Err != "" {
			t.Fatalf("seed %d: %s", s.Seed, s.Err)
		}
		if s.Metrics.TimeBudgetHit == "" {
			t.Errorf("seed %d finished within %s? metrics %+v", s.Seed, budget, s.Metrics)
		}
	}
	for _, want := range []string{"placement seed 1: legalise (elapsed", "placement seed 2: metrics (elapsed", "WARNING time budget 20s reached during"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if !slices.ContainsFunc(rep.Notes, func(n string) bool { return strings.Contains(n, "time budget 20s reached") }) {
		t.Errorf("notes %v lack the budget note", rep.Notes)
	}
}

// The default seeds and budget finish the 1450-part board: no seed is cut
// short and the best one is legal.
func TestKicadPlaceLargeBoardFinishes(t *testing.T) {
	if testing.Short() {
		t.Skip("places a 1450-part board six times")
	}
	rep, stderr, el := largePlaceRun(t, 85, 6, 8*time.Minute)
	t.Logf("6 seeds in %s", el.Round(time.Second))
	if rep.BudgetHit {
		t.Fatalf("budget hit in %s:\n%s", el, stderr)
	}
	best := rep.Seeds[0]
	for _, s := range rep.Seeds {
		if s.Seed == rep.BestSeed {
			best = s
		}
	}
	if best.Illegal != 0 {
		t.Fatalf("best seed %d has %d illegal placements: %+v", best.Seed, best.Illegal, best.Metrics)
	}
}

func TestPlaceProgressTicks(t *testing.T) {
	var buf bytes.Buffer
	pp := newPlaceProgress(&syncWriter{w: &buf}, 20*time.Millisecond)
	pp.phase(3, "legalise")
	time.Sleep(70 * time.Millisecond)
	pp.stop()
	out := buf.String()
	if !strings.Contains(out, "placement seed 3: legalise (elapsed 0s)") || !strings.Contains(out, "seed 3 legalise") {
		t.Fatalf("progress output:\n%s", out)
	}
}
