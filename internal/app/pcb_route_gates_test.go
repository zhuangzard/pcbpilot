package app

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

func TestCheckIntentWidths(t *testing.T) {
	reqs := map[string]specctra.NetRequirement{
		"+12V": {OuterMil: 21.65, InnerMil: 41.34, MinMil: 21.65},
		"GND":  {OuterMil: 21.65, InnerMil: 43.31, MinMil: 10},
	}
	pads := []boardPad{{Net: "GND", Layer: 1, X: 0, Y: 0, W: 11, H: 59}}
	tracks := []specctra.Track{
		{ID: "ok-outer", Net: "+12V", Layer: 1, X1: 1000, X2: 2000, Width: 21.65},
		{ID: "inner-thin", Net: "+12V", Layer: 15, X1: 1000, X2: 2000, Width: 21.65},           // 21.65 < 41.34 inner, below min? no: min 21.65 -> away from pins
		{ID: "neck-at-pin", Net: "GND", Layer: 1, X1: 0, Y1: 20, X2: 60, Y2: 20, Width: 11.02}, // starts on the pad
		{ID: "neck-far", Net: "GND", Layer: 1, X1: 500, Y1: 500, X2: 600, Y2: 500, Width: 11.02},
		{ID: "below-min", Net: "GND", Layer: 1, X1: 0, Y1: 20, X2: 30, Y2: 20, Width: 6},
		{ID: "unconstrained", Net: "SIG", Layer: 1, Width: 4},
	}
	vs := checkIntentWidths(tracks, pads, reqs, nil)
	got := map[string]string{}
	for _, v := range vs {
		got[v.ID] = v.Reason
	}
	if len(vs) != 3 || got["inner-thin"] == "" || got["neck-far"] == "" || !strings.Contains(got["below-min"], "minimum") {
		t.Fatalf("violations = %+v", vs)
	}
	if lines := summarizeWidthViolations(vs); len(lines) != 2 || !strings.HasPrefix(lines[1], "GND: 2 track(s), worst 6.00") {
		t.Fatalf("summary = %v", lines)
	}
}

func TestApplyWaivers(t *testing.T) {
	ws := []gateWaiver{{Gate: "post-layout-sim", Match: "SV1_DRV", Reason: "drain path inside 2 % of 12 V", By: "taisen 2026-10-06"}}
	g := gateResult{Gate: "post-layout-sim", Items: []string{"ir-drop SV1_DRV 158 mV > 30 mV"}}
	applyWaivers(&g, ws)
	if !g.Pass || len(g.Waived) != 1 {
		t.Fatalf("fully waived gate = %+v", g)
	}
	g = gateResult{Gate: "post-layout-sim", Items: []string{"ir-drop SV1_DRV 158 mV", "via-current +12V"}}
	applyWaivers(&g, ws)
	if g.Pass {
		t.Fatal("gate with an unwaived item passed")
	}
	g = gateResult{Gate: "native-drc", Detail: "3 violation(s)"}
	applyWaivers(&g, []gateWaiver{{Gate: "native-drc", Match: "", Reason: "x", By: "y"}})
	if g.Pass {
		t.Fatal("gate without items was waived")
	}
	g = gateResult{Gate: "intent-widths", Items: []string{"SV1_DRV: 1 track(s)"}}
	applyWaivers(&g, ws)
	if g.Pass {
		t.Fatal("waiver for another gate applied")
	}
}

// A thin GND track running inside the GND pour of its layer is carried by
// the pour; the same track outside it (or in a pour cut-out) is not.
func TestCheckIntentWidthsPourBacked(t *testing.T) {
	reqs := map[string]specctra.NetRequirement{"GND": {OuterMil: 21.65, InnerMil: 43.31, MinMil: 10}}
	poured := []any{map[string]any{"net": "GND", "layer": "1", "fills": []any{map[string]any{"source": []any{
		[]any{0.0, 0.0, "L", 1000.0, 0.0, 1000.0, 1000.0, 0.0, 1000.0},
		[]any{400.0, 400.0, "L", 600.0, 400.0, 600.0, 600.0, 400.0, 600.0}, // cut-out
	}}}}}
	tracks := []specctra.Track{
		{ID: "in-pour", Net: "GND", Layer: 1, X1: 100, Y1: 100, X2: 300, Y2: 100, Width: 16.24},
		{ID: "through-cutout", Net: "GND", Layer: 1, X1: 300, Y1: 500, X2: 700, Y2: 500, Width: 16.24},
		{ID: "other-layer", Net: "GND", Layer: 2, X1: 100, Y1: 100, X2: 300, Y2: 100, Width: 16.24},
	}
	vs := checkIntentWidths(tracks, nil, reqs, pouredLookup(poured))
	ids := []string{}
	for _, v := range vs {
		ids = append(ids, v.ID)
	}
	if strings.Join(ids, ",") != "other-layer,through-cutout" {
		t.Fatalf("violations = %v", ids)
	}
}
