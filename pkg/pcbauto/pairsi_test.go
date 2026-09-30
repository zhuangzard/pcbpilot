package pcbauto

import (
	"os"
	"testing"
)

// siFromSnapshot runs CheckSI on a routed snapshot with an intent.
func siFromSnapshot(t *testing.T, snap, intentPath string) *SIReport {
	t.Helper()
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	in, err := AesInputFromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	ir, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	it, err := ParseIntent(ir)
	if err != nil {
		t.Fatal(err)
	}
	b := in.Board
	spec := PowerSpec{Intent: it}
	st := DecideStackup(b, Analyze(b, spec, nil), StackOptions{Force: b.CopperLayers})
	an := Analyze(b, spec, st)
	return CheckSI(b, an, st, &RouteResult{Tracks: in.Tracks, Vias: in.Vias})
}

// The ESP32 mini USB pair as the v0.5 E2E routed it (baseline.md §6):
// USB_DP 0 vias all TOP, USB_DM 2 vias TOP+BOTTOM, ≈ 65 mil coupled of
// ≈ 870. Skew passes (14 mil < 100); the pair checks must not.
func TestPairSIFlagsUncoupledESP32USB(t *testing.T) {
	for _, snap := range []string{"testdata/esp32-v05-fixed.routed.json", "testdata/esp32-v05-live.reload.json"} {
		si := siFromSnapshot(t, snap, "../../internal/app/testdata/esp32-v05/intent.json")
		var pr *SIPair
		for i := range si.Pairs {
			if si.Pairs[i].P == "USB_DP" || si.Pairs[i].N == "USB_DP" {
				pr = &si.Pairs[i]
			}
		}
		if pr == nil || pr.Coupling == nil {
			t.Fatalf("%s: USB pair not measured: %+v", snap, si.Pairs)
		}
		c := pr.Coupling
		t.Logf("%s: skew %.0f/%.0f, body coupled %.1f %% of %.0f mil (whole legs %.0f/%.0f mil), uncoupled at an end %.0f (budget %.0f, %d ends), vias %d/%d, layers %v/%v",
			snap, pr.SkewMil, pr.LimitMil, c.CoupledPct, c.BodyMil, c.CoupledPMil, c.CoupledNMil, c.UncoupledMil, c.BreakoutMil, c.Ends, c.ViasP, c.ViasN, c.LayersP, c.LayersN)
		kinds := map[string]bool{}
		for _, f := range si.Findings {
			kinds[f.Kind] = true
		}
		if kinds["skew"] {
			t.Errorf("%s: skew is within the USB2 limit, got a skew finding", snap)
		}
		// The pair lies within the breakout zones of J2+D3 and U2 (no body):
		// the legs never join, so the ends' budgets fail.
		for _, k := range []string{"uncoupled", "via-asymmetry", "layer-asymmetry"} {
			if !kinds[k] {
				t.Errorf("%s: want a %s finding, got %v", snap, k, si.Findings)
			}
		}
		if c.CoupledPMil+c.CoupledNMil > 0.15*(870*2) {
			t.Errorf("%s: coupled %.0f/%.0f mil, the routed pair is essentially uncoupled", snap, c.CoupledPMil, c.CoupledNMil)
		}
	}
}

// Two straight parallel legs at the target gap are fully coupled and clean;
// the same legs split onto two layers are not.
func TestPairCouplingSynthetic(t *testing.T) {
	hc := ClassifyHSName("USB", "USB_DP")
	w, gap := 11.7, 6.0
	p := []Track{{Net: "P", Layer: LayerTop, A: Point{0, 0}, B: Point{1000, 0}, Width: w}}
	n := []Track{{Net: "N", Layer: LayerTop, A: Point{0, w + gap}, B: Point{1000, w + gap}, Width: w}}
	pads := []*Pad{{Box: OrientedBox{C: Point{0, 0}, W: 10, H: 10}}, {Box: OrientedBox{C: Point{1000, 0}, W: 10, H: 10}}}
	pc := MeasurePairCoupling(p, n, 0, 0, pads, gap, hc)
	if pc.CoupledPct < 99 || len(pairFindings("P/N", pc, hc)) != 0 {
		t.Fatalf("parallel pair: %+v %v", pc, pairFindings("P/N", pc, hc))
	}
	n[0].Layer = LayerBottom
	pc = MeasurePairCoupling(p, n, 0, 2, pads, gap, hc)
	kinds := map[string]bool{}
	for _, f := range pairFindings("P/N", pc, hc) {
		kinds[f.Kind] = true
	}
	for _, k := range []string{"coupling", "uncoupled", "via-asymmetry", "layer-asymmetry"} {
		if !kinds[k] {
			t.Errorf("split pair: want %s, got %v (%+v)", k, kinds, pc)
		}
	}
}
