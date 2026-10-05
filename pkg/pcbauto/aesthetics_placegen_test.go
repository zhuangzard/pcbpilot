package pcbauto

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Phase B placement aesthetics (placer_aes.go): the stage must improve the
// look without touching any harder tier.

// aesPlaceBoard is a small board with the defects the stage exists for:
// a row of five 0603 resistors off the grid, near-aligned and unevenly
// pitched, one turned 90°, and two identical LED channels laid out
// carelessly. Nothing in it is electrically tight.
func aesPlaceBoard() *Board {
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 2000, 1400}.Corners(), CopperLayers: 2}
	smd := func(n, net string, dx, dy float64) *Pad {
		return &Pad{Number: n, Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{dx, dy}, W: 30, H: 35}}
	}
	mk := func(ref, dev string, at Point, rot float64, pads ...*Pad) *Part {
		p := &Part{Ref: ref, Device: dev, Pos: at}
		for _, pd := range pads {
			pd.Box.C = pd.Box.C.Add(at)
		}
		p.Pads = pads
		return p
	}
	ic := []*Pad{}
	for i := 0; i < 8; i++ {
		ic = append(ic, smd(fmt.Sprint(i+1), fmt.Sprintf("S%d", i), -100+float64(i%4)*60, map[bool]float64{true: -80, false: 80}[i < 4]))
	}
	b.Parts = []*Part{
		mk("U1", "MCU", Point{1000, 700}, 0, ic...),
		mk("R1", "10k", Point{600.7, 1001.3}, 0, smd("1", "S0", -30, 0), smd("2", "+3V3", 30, 0)),
		mk("R2", "10k", Point{713.2, 1004.9}, 0, smd("1", "S1", -30, 0), smd("2", "+3V3", 30, 0)),
		mk("R3", "10k", Point{842.6, 998.1}, 0, smd("1", "S2", -30, 0), smd("2", "+3V3", 30, 0)),
		mk("R4", "10k", Point{931.9, 1006.6}, 0, smd("1", "S3", -30, 0), smd("2", "+3V3", 30, 0)),
		mk("D1", "LED", Point{1402.3, 302.6}, 0, smd("1", "LA1", -30, 0), smd("2", "GND", 30, 0)),
		mk("R5", "1k", Point{1398.1, 401.7}, 0, smd("1", "S4", -30, 0), smd("2", "LA1", 30, 0)),
		mk("D2", "LED", Point{1603.8, 296.4}, 0, smd("1", "LA2", -30, 0), smd("2", "GND", 30, 0)),
		mk("R6", "1k", Point{1611.4, 418.2}, 0, smd("1", "S5", -30, 0), smd("2", "LA2", 30, 0)),
	}
	if err := b.Index(); err != nil {
		panic(err)
	}
	return b
}

func aesMetric(r *AestheticsReport, id string) AesMetric {
	if m := r.Metric(id); m != nil {
		return *m
	}
	return AesMetric{ID: id, Skipped: true}
}

func TestAesPlaceImprovesLooksOnly(t *testing.T) {
	b := aesPlaceBoard()
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	before := Aesthetics(AesInput{Board: b, Analysis: an, Circuit: c})
	wire0 := WeightedWirelength(b, an, c)
	pr, err := Place(b, an, c, nil, PlaceOptions{Seed: 1, TidyOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	after := Aesthetics(AesInput{Board: b, Analysis: an, Circuit: c})
	ar := pr.Aesthetics
	if ar == nil || ar.Moved == 0 {
		t.Fatalf("stage moved nothing: %+v", ar)
	}
	t.Logf("accepted %v rejected %v spent %.1f/%.1f", ar.Accepted, ar.Rejected, ar.WireSpentMil, ar.WireBudgetMil)
	for _, id := range []string{"P1", "P2", "P3", "P4", "P9"} {
		mb, ma := aesMetric(before, id), aesMetric(after, id)
		t.Logf("%s %.3f/%.1f → %.3f/%.1f %s", id, mb.Value, mb.Score, ma.Value, ma.Score, ma.Detail)
		if !mb.Skipped && !ma.Skipped && ma.Score < mb.Score-1e-6 {
			t.Errorf("%s got worse: %.1f → %.1f", id, mb.Score, ma.Score)
		}
	}
	if p9 := aesMetric(after, "P9"); p9.Value < 1 {
		t.Errorf("P9 %.3f: every movable origin should land on the grid here", p9.Value)
	}
	if p1b, p1a := aesMetric(before, "P1"), aesMetric(after, "P1"); p1a.Value >= p1b.Value {
		t.Errorf("P1 unaligned share %.3f → %.3f: the near-aligned row should snap onto one line", p1b.Value, p1a.Value)
	}
	if pr.Metrics.Overlaps != 0 {
		t.Errorf("overlaps %d", pr.Metrics.Overlaps)
	}
	if w := WeightedWirelength(b, an, c); w > wire0*1.02+1 {
		t.Errorf("wire %.0f → %.0f mil: beyond the balanced 2 %% slack", wire0, w)
	}
}

// The functional profile is "nearly off": no slack, only free snaps.
func TestAesPlaceFunctionalSpendsNothing(t *testing.T) {
	b := aesPlaceBoard()
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	wire0 := WeightedWirelength(b, an, c)
	prof, _ := AesProfileByName("functional")
	pr, err := Place(b, an, c, nil, PlaceOptions{Seed: 1, TidyOnly: true, Aesthetics: &prof})
	if err != nil {
		t.Fatal(err)
	}
	ar := pr.Aesthetics
	if ar.WireBudgetMil != 0 || ar.WireSpentMil > 1e-6 {
		t.Errorf("functional spent %.2f of %.2f mil", ar.WireSpentMil, ar.WireBudgetMil)
	}
	for k := range ar.Accepted {
		if !strings.HasPrefix(k, "grid") && !strings.HasPrefix(k, "fold") {
			t.Errorf("functional ran %s", k)
		}
	}
	if w := WeightedWirelength(b, an, c); w > wire0+1e-6 {
		t.Errorf("wire %.3f → %.3f", wire0, w)
	}
}

// A decoupling cap at its pin keeps its pin side: the stage may not fold
// it (pad swap) or drag it away from the pin, whatever the looks gain.
func TestAesPlaceNeverLoosensATether(t *testing.T) {
	b := aesPlaceBoard()
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	pl := &placer{b: b, an: an, c: c, m: &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}},
		opt: PlaceOptions{SpacingMil: 12}, partNet: map[*Part][]int{}, zoneOf: map[*Part]Rect{}, decap: map[*Part]*Pad{}, spacing: 12}
	pl.setup(&PlaceResult{})
	crit := func() float64 {
		s := 0.0
		for p, tt := range pl.tether {
			if criticalRoles[tt.role] {
				s += pl.tetherCost(p)
			}
		}
		return s
	}
	c0 := crit()
	if _, err := Place(b, an, c, nil, PlaceOptions{Seed: 1, TidyOnly: true}); err != nil {
		t.Fatal(err)
	}
	if c1 := crit(); c1 > c0+1e-6 {
		t.Errorf("critical tether excess %.2f → %.2f", c0, c1)
	}
}

// aesWorse: every hard tier blocks, slack is only for efficiency, and the
// grid quantisation allowance never covers safety.
func TestAesWorseTiers(t *testing.T) {
	base := aesCost{hard: 10, crit: 5, conv: 3, chain: 2, flow: 1, res: 1, comfort: 4, wire: 100}
	for name, after := range map[string]aesCost{
		"safety/legality":           {hard: 10.1, crit: 5, conv: 3, chain: 2, flow: 1, res: 1, comfort: 4, wire: 100},
		"critical tether":           {hard: 10, crit: 5.1, conv: 3, chain: 2, flow: 1, res: 1, comfort: 4, wire: 100},
		"converter loop":            {hard: 10, crit: 5, conv: 3.1, chain: 2, flow: 1, res: 1, comfort: 4, wire: 100},
		"signal chain":              {hard: 10, crit: 5, conv: 3, chain: 2.1, flow: 1, res: 1, comfort: 4, wire: 100},
		"pair corridor":             {hard: 10, crit: 5, conv: 3, chain: 2, flow: 1.1, res: 1, comfort: 4, wire: 100},
		"port reserve / keep-apart": {hard: 10, crit: 5, conv: 3, chain: 2, flow: 1, res: 1.1, comfort: 4, wire: 100},
		"wire slack":                {hard: 10, crit: 5, conv: 3, chain: 2, flow: 1, res: 1, comfort: 4, wire: 106},
	} {
		if got := aesWorse(base, after, 5, aesCost{}); got != name {
			t.Errorf("%s: got %q", name, got)
		}
	}
	if got := aesWorse(base, aesCost{hard: 10, crit: 5, conv: 3, chain: 2, flow: 1, res: 1, comfort: 4, wire: 104}, 5, aesCost{}); got != "" {
		t.Errorf("within slack: %q", got)
	}
	q := aesCost{crit: 1, conv: 1, chain: 1, flow: 1}
	if got := aesWorse(base, aesCost{hard: 10, crit: 5.5, conv: 3.5, chain: 2.5, flow: 1.5, res: 1, comfort: 4, wire: 100}, 0, q); got != "" {
		t.Errorf("quantisation allowance: %q", got)
	}
	if got := aesWorse(base, aesCost{hard: 10.5, crit: 5, conv: 3, chain: 2, flow: 1, res: 1, comfort: 4, wire: 100}, 0, aesCost{hard: 1, crit: 1}); got != "safety/legality" {
		t.Errorf("safety must get no allowance: %q", got)
	}
}

// A detected pair of identical channels is laid out as a translated copy
// (rotations corresponding), and the routed guard keeps it only when it
// routes no worse.
func TestAesPlaceSymmetryCopies(t *testing.T) {
	b := aesPlaceBoard()
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	var errBefore float64
	for _, g := range DetectSymmetry(b, an, c) {
		if strings.Contains(strings.Join(flatten(g.Instances), ","), "D1") {
			errBefore = g.Error
		}
	}
	pr, err := Place(b, an, c, nil, PlaceOptions{Seed: 1, TidyOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	errAfter := math.Inf(1)
	for _, g := range DetectSymmetry(b, an, c) {
		if strings.Contains(strings.Join(flatten(g.Instances), ","), "D1") {
			errAfter = g.Error
		}
	}
	t.Logf("LED channel symmetry error %.3f → %.3f (%+v)", errBefore, errAfter, pr.Aesthetics.Symmetry)
	if errBefore <= 0 || errAfter > errBefore/2 {
		t.Errorf("LED channel symmetry error %.3f → %.3f: want at least halved", errBefore, errAfter)
	}
}

func TestAesGuardRollsBack(t *testing.T) {
	mk := func(comp float64, drc int, el float64) *abVariant {
		rr := &RouteResult{}
		rr.Stats.Completion = comp
		d := &DRCReport{}
		for i := 0; i < drc; i++ {
			d.Violations = append(d.Violations, Violation{})
		}
		return &abVariant{out: &Result{Route: rr, DRC: d}, js: &JointScore{Items: []JointItem{{Group: "electrical", ID: "decap-loop", Score: el}}}}
	}
	worse := func(a, b *abVariant) string {
		w, _ := aesRoutedWorse(a, b, 0, 0)
		return w
	}
	if w := worse(mk(100, 0, 80), mk(100, 0, 80)); w != "" {
		t.Errorf("equal: %q", w)
	}
	if w := worse(mk(99, 0, 90), mk(100, 0, 80)); !strings.Contains(w, "completion") {
		t.Errorf("completion: %q", w)
	}
	if w := worse(mk(100, 1, 90), mk(100, 0, 80)); !strings.Contains(w, "DRC") {
		t.Errorf("drc: %q", w)
	}
	if w := worse(mk(100, 0, 79.9), mk(100, 0, 80)); !strings.Contains(w, "decap-loop") {
		t.Errorf("electrical: %q", w)
	}
}

// TestAesPlaceBench is the Phase B measurement harness (not a regression
// gate): PCBPILOT_AES_BENCH=1 runs the stage on the human placements of the
// fixture boards (TidyOnly, profile PCBPILOT_AES_STYLE, default balanced),
// logs P1–P9 and every symmetry group before / after, writes placement
// previews to PCBPILOT_AES_OUT, and with PCBPILOT_AES_ROUTE=1 routes both
// placements (PCBPILOT_BENCH_WORK for the deterministic clock) through the
// routed guard.
func TestAesPlaceBench(t *testing.T) {
	if os.Getenv("PCBPILOT_AES_BENCH") == "" {
		t.Skip("set PCBPILOT_AES_BENCH=1")
	}
	style := os.Getenv("PCBPILOT_AES_STYLE")
	prof, err := AesProfileByName(style)
	if style == "auto" {
		prof, err = AesProfile{Name: "auto"}, nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out := os.Getenv("PCBPILOT_AES_OUT")
	boards := []string{"lckfb-mipi-3in1-adapter.json", "bbclaw-ai-voice-terminal.json", "lckfb-szpi-esp32s3.json", "lckfb-rk3568-4layer.json", "lckfb-k230-canmv.json"}
	if only := os.Getenv("PCBAUTO_BOARD"); only != "" {
		boards = []string{only}
	}
	for _, name := range boards {
		t.Run(name, func(t *testing.T) {
			b := loadFixture(t, name)
			an := Analyze(b, PowerSpec{}, nil)
			c := Understand(b, an)
			in := AesInput{Board: b, Analysis: an, Circuit: c, Profile: &prof}
			before := Aesthetics(in)
			svg := func(tag string) {
				if out == "" {
					return
				}
				f, err := os.Create(filepath.Join(out, strings.TrimSuffix(name, ".json")+"-"+tag+".svg"))
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				_ = RenderSVG(f, b, c, nil, nil)
			}
			svg("before")
			start := time.Now()
			pr, err := Place(b, an, c, nil, PlaceOptions{Seed: 1, TidyOnly: true, Aesthetics: &prof})
			if err != nil {
				t.Fatal(err)
			}
			after := Aesthetics(in)
			svg("after")
			ar := pr.Aesthetics
			t.Logf("%s: profile %s, %d parts moved in %.1fs; accepted %v; rejected %v; wire %.0f/%.0f mil",
				name, ar.Profile, ar.Moved, time.Since(start).Seconds(), ar.Accepted, ar.Rejected, ar.WireSpentMil, ar.WireBudgetMil)
			var row []string
			for _, id := range []string{"P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8", "P9"} {
				mb, ma := aesMetric(before, id), aesMetric(after, id)
				if mb.Skipped && ma.Skipped {
					row = append(row, id+" skip")
					continue
				}
				row = append(row, fmt.Sprintf("%s %.3f/%.1f→%.3f/%.1f", id, mb.Value, mb.Score, ma.Value, ma.Score))
			}
			t.Logf("AESROW %s placement %.1f→%.1f | %s", name, before.Placement, after.Placement, strings.Join(row, " | "))
			for _, id := range []string{"P1", "P2", "P3", "P9"} {
				t.Logf("AESDETAIL %s %s: %s → %s", name, id, aesMetric(before, id).Detail, aesMetric(after, id).Detail)
			}
			for _, s := range ar.Symmetry {
				t.Logf("AESSYM %s %s %s [%s → %s] %s err %.3f→%.3f: %s", name, s.Kind, s.Signature, s.Reference, s.Moved, s.Transform, s.ErrorBefore, s.ErrorAfter, s.Result)
			}
			bs := map[string]float64{}
			for _, g := range before.Symmetry {
				bs[strings.Join(flatten(g.Instances), " / ")] = g.Score
			}
			var keys []string
			for _, g := range after.Symmetry {
				keys = append(keys, fmt.Sprintf("AESGRP %s %s %s %s: %.1f → %.1f", name, g.Kind, g.Signature, strings.Join(flatten(g.Instances), " / "), bs[strings.Join(flatten(g.Instances), " / ")], g.Score))
			}
			sort.Strings(keys)
			for _, k := range keys {
				t.Log(k)
			}
			if os.Getenv("PCBPILOT_AES_ROUTE") == "" {
				return
			}
			ropt := Options{Stack: StackOptions{Force: b.CopperLayers}, Route: RouteOptions{Timeout: 4 * time.Minute, WorkRate: benchWorkRate(t)}}
			v, err := routeVariant(context.Background(), b, an, c, pr, ropt)
			if err != nil {
				t.Fatal(err)
			}
			kept := aesGuard(context.Background(), b, an, c, v, ropt)
			t.Logf("AESROUTE %s guard: %s", name, ar.Guard)
			_ = kept
		})
	}
}
