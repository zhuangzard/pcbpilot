package app

// route_gate_set.go — one mandatory gate set for every route path (kicad
// route; pcb auto route with fastroute, an external --router command or the
// built-in router). Each path ends with gateSetGate: a path that skipped a
// gate (or a future change that drops one) fails the run instead of passing
// with fewer checks.

import (
	"fmt"
	"strings"
)

// mandatoryRouteGates: category → the gate names that satisfy it (the DRC
// gate is named after its host).
var mandatoryRouteGates = []struct {
	category string
	names    []string
}{
	{"design-review", []string{"design-review"}},
	{"drc", []string{"native-drc", "kicad-drc"}},
	{"pad-net-diff", []string{"pad-net-diff"}},
	{"intent-rules", []string{"intent-rules"}},
	{"intent-widths", []string{"intent-widths"}},
	{"intent-lengths", []string{"intent-lengths"}},
	{"copper-to-edge", []string{"copper-to-edge"}},
	{"isolation", []string{"isolation"}},
	{"via-current", []string{"via-current"}},
	{"post-layout-sim", []string{"post-layout-sim"}},
	{"route-complete", []string{"route-complete"}},
	{"silkscreen", []string{"silkscreen"}},
	{"board-manual", []string{"board-manual"}},
	{"design-report", []string{"design-report"}},
	{"signoff", []string{"signoff"}},
}

// missingRouteGates lists the mandatory categories gates does not cover.
func missingRouteGates(gates []gateResult) []string {
	have := map[string]bool{}
	for _, g := range gates {
		have[g.Gate] = true
	}
	var miss []string
	for _, m := range mandatoryRouteGates {
		ok := false
		for _, n := range m.names {
			ok = ok || have[n]
		}
		if !ok {
			miss = append(miss, m.category)
		}
	}
	return miss
}

// gateSetGate fails when any mandatory gate did not run on this path.
func gateSetGate(gates []gateResult) gateResult {
	miss := missingRouteGates(gates)
	g := gateResult{Gate: "gate-set", Pass: len(miss) == 0,
		Detail: fmt.Sprintf("%d of %d mandatory gate(s) ran", len(mandatoryRouteGates)-len(miss), len(mandatoryRouteGates))}
	for _, m := range miss {
		g.Items = append(g.Items, "mandatory gate not run: "+m)
	}
	if len(miss) > 0 {
		g.Detail += " — missing: " + strings.Join(miss, ", ")
	}
	return g
}
