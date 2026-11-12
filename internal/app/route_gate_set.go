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

type routeGateCategory struct {
	category string
	names    []string
}

// mandatoryRouteGates: category → the gate names that satisfy it (the DRC
// gate is named after its host).
var mandatoryRouteGates = []routeGateCategory{
	{"design-review", []string{"design-review"}},
	{"drc", []string{"native-drc", "kicad-drc"}},
	{"pad-net-diff", []string{"pad-net-diff"}},
	{"intent-rules", []string{"intent-rules"}},
	{"intent-widths", []string{"intent-widths"}},
	{"intent-lengths", []string{"intent-lengths"}},
	{"copper-to-edge", []string{"copper-to-edge"}},
	{"isolation", []string{"isolation"}},
	{"via-current", []string{"via-current"}},
	{"pour-layer-mismatch", []string{"pour-layer-mismatch"}},
	{"post-layout-sim", []string{"post-layout-sim"}},
	{"analog-sim", []string{"analog-sim"}},
	{"route-complete", []string{"route-complete"}},
	{"silkscreen", []string{"silkscreen"}},
	{"board-manual", []string{"board-manual"}},
	{"design-report", []string{"design-report"}},
	{"signoff", []string{"signoff"}},
}

// kicadOnlyRouteGates: gates only kicad route can run, on top of the set every
// path shares (via-audit needs KiCad's rules readback and the final board).
var kicadOnlyRouteGates = []routeGateCategory{
	{"via-audit", []string{"via-audit"}},
}

// missingRouteGates lists the mandatory categories gates does not cover.
func missingRouteGates(gates []gateResult) []string {
	return missingGatesOf(mandatoryRouteGates, gates)
}

func missingGatesOf(set []routeGateCategory, gates []gateResult) []string {
	have := map[string]bool{}
	for _, g := range gates {
		have[g.Gate] = true
	}
	var miss []string
	for _, m := range set {
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

// gateSetGate fails when any mandatory gate did not run on this path. Its
// detail also names the gates that ran and did not pass.
func gateSetGate(gates []gateResult) gateResult {
	return gateSetGateOf(mandatoryRouteGates, gates)
}

// kicadGateSetGate is gateSetGate plus the gates only kicad route can run.
func kicadGateSetGate(gates []gateResult) gateResult {
	return gateSetGateOf(append(append([]routeGateCategory{}, mandatoryRouteGates...), kicadOnlyRouteGates...), gates)
}

func gateSetGateOf(set []routeGateCategory, gates []gateResult) gateResult {
	miss := missingGatesOf(set, gates)
	g := gateResult{Gate: "gate-set", Pass: len(miss) == 0,
		Detail: fmt.Sprintf("%d of %d mandatory gate(s) ran", len(set)-len(miss), len(set))}
	for _, m := range miss {
		g.Items = append(g.Items, "mandatory gate not run: "+m)
	}
	if len(miss) > 0 {
		g.Detail += " — missing: " + strings.Join(miss, ", ")
	}
	// "ran" is all this gate judges; say what the gates that ran concluded,
	// so the line cannot be read as "every mandatory gate passed".
	var bad []string
	for _, x := range gates {
		switch {
		case x.NotRun:
			bad = append(bad, x.Gate+" (not run)")
		case !x.Pass:
			bad = append(bad, x.Gate+" (fail)")
		case len(x.Waived) > 0:
			bad = append(bad, x.Gate+" (waived)")
		}
	}
	if len(bad) > 0 {
		g.Detail += fmt.Sprintf("; %d did NOT pass: %s", len(bad), strings.Join(bad, ", "))
		g.Info = append(g.Info, "gate-set only checks that every mandatory gate ran; see each gate for its result")
	} else {
		g.Detail += ", all passed"
	}
	return g
}
