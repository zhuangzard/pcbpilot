package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Contract: every route path — kicad route; pcb auto route with fastroute,
// an external --router command or --router internal — produces the same
// mandatory gate set (mandatoryRouteGates) and ends with gate-set, which
// fails the run when one is missing. Never weaken this to make a change
// pass: a path that drops a gate must fail here or at run time.
func TestRouteGateSetContract(t *testing.T) {
	// 1. The gate set itself: every category is required.
	all := []gateResult{}
	for _, m := range mandatoryRouteGates {
		all = append(all, gateResult{Gate: m.names[0]})
	}
	if g := gateSetGate(all); !g.Pass {
		t.Fatalf("complete set fails: %+v", g)
	}
	for i := range all {
		less := append(append([]gateResult{}, all[:i]...), all[i+1:]...)
		if g := gateSetGate(less); g.Pass || !strings.Contains(g.Detail, mandatoryRouteGates[i].category) {
			t.Fatalf("without %s: %+v", all[i].Gate, g)
		}
	}

	// 2. The snapshot gates shared by every path, executed offline on the
	// KiCad fixture: with a fastroute session (routeChecked) and without a
	// router report (external command, built-in router) — both must judge
	// route-complete, silkscreen, the manual and every intent / safety gate.
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	intent := filepath.Join(dir, "intent.json")
	_ = os.WriteFile(intent, []byte(`{"nets":{"VCC":{"role":"power","widthMil":{"outer":20,"inner":20,"min":8}}}}`), 0o644)
	boardPath := filepath.Join(dir, "board-final.json")
	_ = os.WriteFile(boardPath, raw, 0o644)
	in, err := loadDesignIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := loadBoardSnapshotPath(boardPath)
	if err != nil {
		t.Fatal(err)
	}
	waivers := []gateWaiver{{Gate: "board-manual", Match: noManualMatch, Reason: "contract test", By: "test"}}
	for _, routeChecked := range []bool{true, false} {
		var gates []gateResult
		snapshotIntentGates(snap, in, intent, "", nil, nil, nil, true, func(g gateResult) { gates = append(gates, g) })
		qo := qualityGateOpts{intent: intent, outDir: dir, waivers: waivers, silk: defaultSilkTightOpts(), noManual: true, routeChecked: routeChecked}
		gates = append(gates, tailGates(snap, boardPath, "", qo, "contract", "", map[string]any{}, io.Discard)...)
		// The host / run gates every path adds around them (checked in the
		// sources below).
		for _, n := range []string{"design-review", "native-drc", "pad-net-diff", "intent-rules", "design-report", "signoff"} {
			gates = append(gates, gateResult{Gate: n})
		}
		if miss := missingRouteGates(gates); len(miss) != 0 {
			t.Fatalf("routeChecked=%v: snapshot gates miss %v", routeChecked, miss)
		}
		var rc *gateResult
		for i := range gates {
			if gates[i].Gate == "route-complete" {
				rc = &gates[i]
			}
		}
		if !routeChecked && !strings.Contains(rc.Detail, "board copper connectivity") {
			t.Fatalf("no router report: route-complete must come from the board: %+v", rc)
		}
	}

	// 3. Each route command wires the run gates and ends with gate-set.
	for file, want := range map[string][]string{
		"cmd_kicad_route.go":    {`r.review("design"`, `r.review("layout"`, "kicadDRCGate(", "padNetDiffGate(", "kicadIntentRulesGate(", "snapshotIntentGates(", "tailGates(", "r.designReport(", "signoffGate(", "gateSetGate(r.gates)"},
		"cmd_pcb_auto_route.go": {`designReviewGate("design"`, `designReviewGate("layout"`, "runQualityGates(", "autoRouteDesignReport(", "signoffGate(", "gateSetGate(gates)", `o.routerCmd == "internal"`},
		"pcb_route_gates.go":    {`Gate: "native-drc"`, `Gate: "pad-net-diff"`, `Gate: "intent-rules"`, "postRouteGates(", "tailGates(", "boardRouteCompleteGate("},
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(src), w) {
				t.Errorf("%s no longer contains %q: a route path dropped a mandatory gate", file, w)
			}
		}
	}
}
