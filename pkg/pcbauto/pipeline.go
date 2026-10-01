package pcbauto

import (
	"context"
	"math"
	"strings"
	"time"
)

// Options configure the end-to-end pipeline.
type Options struct {
	Power PowerSpec    `json:"power"`
	Stack StackOptions `json:"stack"`
	Route RouteOptions `json:"route"`
	// NoEscalate keeps the first stackup even when routing is incomplete.
	NoEscalate bool `json:"noEscalate,omitempty"`
}

// Result is everything the pipeline decided and produced.
type Result struct {
	Analysis *Analysis    `json:"analysis"`
	Stackup  *Stackup     `json:"stackup"`
	Route    *RouteResult `json:"route"`
	DRC      *DRCReport   `json:"drc"`
	Attempts []Attempt    `json:"attempts"`
	// Isolation is the intent insulation outcome: pairs, milled slots,
	// no-pour regions and the creepage/clearance check of the routed copper.
	Isolation *IsolationReport `json:"isolation,omitempty"`
	// Edge is the board-edge safety distance: the policy and the measured
	// copper-to-edge minimum per layer of the plan.
	Edge *EdgeCheck `json:"edge,omitempty"`
	// Blockers are the safety and electrical reasons the result is not
	// deliverable whatever its completion and DRC (verdict.go): isolation
	// findings, board-edge ERRORs, via arrays short of their current, IR
	// drop over budget. Empty when none.
	Blockers []string `json:"blockers,omitempty"`
}

// Attempt records one stackup variant tried by the pipeline.
type Attempt struct {
	Stack      string  `json:"stack"`
	Completion float64 `json:"completion"`
	Vias       int     `json:"vias"`
	Violations int     `json:"violations"`
	Millis     int64   `json:"millis"`
}

func stackLabel(s *Stackup) string {
	out := ""
	for i, l := range s.Stack {
		if i > 0 {
			out += "/"
		}
		out += l.Name
	}
	return out
}

// Run analyses the board, decides the stackup, routes, and — when a 4+ layer
// board does not complete on its outer layers — escalates the power plane to
// a mixed signal+pour layer and keeps the better result.
func Run(ctx context.Context, b *Board, opt Options) (*Result, error) {
	pre := Analyze(b, opt.Power, nil)
	// Milled slots under bridge parts are planned on the final placement
	// and become owned holes the router keeps copper away from.
	isoSlots, isoNotes, isoBad := PlanIsoSlotsDetail(b, pre.Iso)
	st := DecideStackup(b, pre, opt.Stack)
	res := &Result{}
	route1 := func(st *Stackup, ro RouteOptions, label string) (*Analysis, *RouteResult, *DRCReport, error) {
		an := Analyze(b, opt.Power, st)
		start := time.Now()
		rr, err := Route(ctx, b, st, an, ro)
		if err != nil {
			return nil, nil, nil, err
		}
		if n := MicroFix(b, an, st, rr); n > 0 {
			rr.Notes = append(rr.Notes, sprintf("micro-fix: %d sub-0.25 mil clearance shortfall(s) cleared by shifting or narrowing a track", n))
		}
		rr.Power = powerIntegrity(b, an, st, rr)
		drc := CheckDRCStrict(b, an, st, rr.Tracks, rr.Vias)
		res.Attempts = append(res.Attempts, Attempt{Stack: stackLabel(st) + label, Completion: rr.Stats.Completion,
			Vias: rr.Stats.Vias + rr.Stats.FanoutVias, Violations: len(drc.Violations), Millis: attemptMillis(start, rr)})
		return an, rr, drc, nil
	}
	try := func(st *Stackup) (*Analysis, *RouteResult, *DRCReport, error) {
		an, rr, drc, err := route1(st, opt.Route, "")
		if err != nil || opt.Route.NoPairUnit || !hasUnitPairs(an) || !timeLeft(ctx, opt.Route.Timeout) {
			return an, rr, drc, err
		}
		// Declared pairs were routed as units. Completion ranks above pair
		// coupling only as far as the unit costs connections: route the
		// same stack leg by leg too and keep the units only when they
		// complete as much, with no more DRC violations (usb3-2layer: the
		// units cost 25 % of the board's connections).
		lo := opt.Route
		lo.NoPairUnit = true
		an2, rr2, drc2, err := route1(st, lo, " (pairs leg by leg)")
		if err != nil {
			return nil, nil, nil, err
		}
		if pairUnitsWorse(b, an, st, rr, drc, an2, rr2, drc2) {
			rr2.Notes = append(rr2.Notes, sprintf("differential pairs: leg-by-leg routing kept (units routed %.1f%%, DRC %d; leg by leg %.1f%%, DRC %d)", rr.Stats.Completion, len(drc.Violations), rr2.Stats.Completion, len(drc2.Violations)))
			opt.Route.NoPairUnit = true // the retries below follow the kept mode
			return an2, rr2, drc2, nil
		}
		rr.Notes = append(rr.Notes, sprintf("differential pairs: routed as units (%.1f%%, DRC %d; leg by leg %.1f%%, DRC %d)", rr.Stats.Completion, len(drc.Violations), rr2.Stats.Completion, len(drc2.Violations)))
		return an, rr, drc, nil
	}
	an, rr, drc, err := try(st)
	if err != nil {
		return nil, err
	}
	res.Analysis, res.Stackup, res.Route, res.DRC = an, st, rr, drc
	if !opt.NoEscalate && rr.Stats.Completion < 97 && st.Layers >= 4 && timeLeft(ctx, opt.Route.Timeout) {
		alt := *st
		alt.Stack = append([]StackLayer(nil), st.Stack...)
		alt.Reasons = append([]string(nil), st.Reasons...)
		if alt.MixedPower() {
			an2, rr2, drc2, err := try(&alt)
			if err != nil {
				return nil, err
			}
			if rr2.Stats.Completion > rr.Stats.Completion {
				res.Analysis, res.Stackup, res.Route, res.DRC = an2, &alt, rr2, drc2
			}
		}
	}
	// A quick board whose high-speed nets came out with skew, plane-split
	// crossings or extra vias gets up to three passes on finer grids: the pair
	// geometry depends on the grid phase, and on the ESP32 mini (2026-09-25)
	// the default 3.2 mil grid put USB_DM across the IN2 power split 16 times
	// while 2.5 mil routed it clean — the E2E agent had to find that by hand.
	// "Quick" is the board's own routing time — the leg-by-leg routing of
	// the first stack when the pairs were also routed as units (the unit
	// pass is slower or faster depending on the pairs, and must not by
	// itself switch the retries on or off).
	quick := len(res.Attempts) > 0 && res.Attempts[0].Millis < 20000
	if len(res.Attempts) > 1 && strings.HasSuffix(res.Attempts[1].Stack, " (pairs leg by leg)") {
		quick = res.Attempts[1].Millis < 20000
	}
	// Where the first stack routed its declared pairs as units, each finer
	// grid is also tried leg by leg (usb3-2layer: the units reached 96.4 %
	// on the finer grids where leg by leg reached 100 %); the unit result
	// survives only where it completes as much (pairUnitsWorse).
	unitMode := hasUnitPairs(res.Analysis) && !opt.Route.NoPairUnit
	if !opt.NoEscalate && opt.Route.GridMil <= 0 && quick {
		for _, k := range []float64{0.9, 0.8, 0.7} {
			if hsFindings(CheckSI(b, res.Analysis, res.Stackup, res.Route)) == 0 && !(unitMode && pairDefects(CheckSI(b, res.Analysis, res.Stackup, res.Route)) > 0) || !timeLeft(ctx, opt.Route.Timeout) {
				break
			}
			modes := []bool{opt.Route.NoPairUnit}
			if unitMode {
				modes = append(modes, true)
			}
			for _, legacy := range modes {
				fine := opt
				fine.Route.GridMil = k * defaultGrid(b.Rules)
				fine.Route.NoPairUnit = legacy
				label := sprintf(" grid %.2f", fine.Route.GridMil)
				if legacy && unitMode {
					label += " (pairs leg by leg)"
				}
				an2, rr2, drc2, err := func() (*Analysis, *RouteResult, *DRCReport, error) {
					an := Analyze(b, opt.Power, res.Stackup)
					start := time.Now()
					rr, err := Route(ctx, b, res.Stackup, an, fine.Route)
					if err != nil {
						return nil, nil, nil, err
					}
					if n := MicroFix(b, an, res.Stackup, rr); n > 0 {
						rr.Notes = append(rr.Notes, sprintf("micro-fix: %d sub-0.25 mil clearance shortfall(s) cleared by shifting or narrowing a track", n))
					}
					rr.Power = powerIntegrity(b, an, res.Stackup, rr)
					drc := CheckDRCStrict(b, an, res.Stackup, rr.Tracks, rr.Vias)
					res.Attempts = append(res.Attempts, Attempt{Stack: stackLabel(res.Stackup) + label, Completion: rr.Stats.Completion,
						Vias: rr.Stats.Vias + rr.Stats.FanoutVias, Violations: len(drc.Violations), Millis: attemptMillis(start, rr)})
					return an, rr, drc, nil
				}()
				if err != nil {
					return nil, err
				}
				keep := betterHS(b, rr2, drc2, an2, res)
				if unitMode {
					keep = pairUnitsWorse(b, res.Analysis, res.Stackup, res.Route, res.DRC, an2, rr2, drc2)
				}
				if keep {
					res.Analysis, res.Route, res.DRC = an2, rr2, drc2
					res.Route.Notes = append(res.Route.Notes, sprintf("high-speed retry: finer %.2f mil grid kept%s (fewer skew/split/via findings)", fine.Route.GridMil, map[bool]string{true: ", pairs leg by leg", false: ""}[legacy && unitMode]))
				}
			}
		}
	}
	if err := irReroute(ctx, b, opt, res); err != nil {
		return nil, err
	}
	isoNotes = append(isoNotes, clipPlanesToIso(b, res.Analysis, res.Route)...)
	res.Isolation = isolationReport(b, res.Analysis, res.Route, isoSlots, isoNotes, isoBad)
	if res.Route != nil {
		res.Route.Notes = append(res.Route.Notes, enforcePlaneEdge(b, res.Analysis.edgePolicy(b), res.Route.Planes)...)
	}
	res.Edge = planEdgeCheck(b, res.Analysis, res.Stackup, res.Route)
	sg, eb := deliveryBlockers(res.Isolation, res.Edge, res.Route)
	res.Blockers = append(sg, eb...)
	return res, nil
}

// irReroute closes the IR-drop loop: a simulated power net that misses its
// drop budget with every segment on its worst path already at routed width
// is re-routed with a wider net width (and extra fan-out vias when the vias
// dominate the drop). At most two passes; a pass is kept only when it
// routes no worse (completion, DRC) and lowers the IR violations or the
// worst drop/budget ratio.
func irReroute(ctx context.Context, b *Board, opt Options, res *Result) error {
	if opt.Power.Sim == nil || res.Route == nil || res.Route.Power == nil {
		return nil
	}
	boost := map[string]simBoost{}
	for k, v := range opt.Power.boost {
		boost[k] = v
	}
	for pass := 1; pass <= 2; pass++ {
		changed := false
		for _, n := range res.Route.Power.Nets {
			if n.Reroute == nil {
				continue
			}
			boost[n.Net] = *n.Reroute
			changed = true
		}
		if !changed {
			return nil
		}
		if !timeLeft(ctx, opt.Route.Timeout) {
			res.Route.Notes = append(res.Route.Notes, "IR-drop re-route skipped: not enough of the time budget left for another routing pass")
			return nil
		}
		o2 := opt
		o2.Power.boost = map[string]simBoost{}
		for k, v := range boost {
			o2.Power.boost[k] = v
		}
		o2.Route.GridMil = res.Route.Stats.GridMil
		an := Analyze(b, o2.Power, res.Stackup)
		start := time.Now()
		rr, err := Route(ctx, b, res.Stackup, an, o2.Route)
		if err != nil {
			return err
		}
		if n := MicroFix(b, an, res.Stackup, rr); n > 0 {
			rr.Notes = append(rr.Notes, sprintf("micro-fix: %d sub-0.25 mil clearance shortfall(s) cleared by shifting or narrowing a track", n))
		}
		rr.Power = powerIntegrity(b, an, res.Stackup, rr)
		drc := CheckDRCStrict(b, an, res.Stackup, rr.Tracks, rr.Vias)
		res.Attempts = append(res.Attempts, Attempt{Stack: stackLabel(res.Stackup) + sprintf(" IR re-route %d", pass), Completion: rr.Stats.Completion,
			Vias: rr.Stats.Vias + rr.Stats.FanoutVias, Violations: len(drc.Violations), Millis: attemptMillis(start, rr)})
		old := res.Route.Power
		better := rr.Stats.Completion >= res.Route.Stats.Completion && len(drc.Violations) <= len(res.DRC.Violations) &&
			(rr.Power.Violations() < old.Violations() || rr.Power.WorstRatio() < old.WorstRatio()-1e-6)
		if !better {
			res.Route.Notes = append(res.Route.Notes, sprintf("IR re-route %d: not kept (routed %.1f%%, DRC %d, IR violations %d, worst %.2f× budget)", pass, rr.Stats.Completion, len(drc.Violations), rr.Power.Violations(), rr.Power.WorstRatio()))
			return nil
		}
		rr.Power.Passes += old.Passes
		for _, n := range res.Route.Notes {
			if strings.HasPrefix(n, "IR re-route") {
				rr.Notes = append(rr.Notes, n)
			}
		}
		res.Analysis, res.Route, res.DRC = an, rr, drc
		res.Route.Notes = append(res.Route.Notes, sprintf("IR re-route %d kept: IR violations %d → %d, worst %.2f× → %.2f× budget", pass, old.Violations(), rr.Power.Violations(), old.WorstRatio(), rr.Power.WorstRatio()))
	}
	return nil
}

// hsFindings counts the SI findings the joint score treats as defects.
func hsFindings(si *SIReport) int {
	n := 0
	for _, f := range si.Findings {
		if siDefect(f.Kind) {
			n++
		}
	}
	return n
}

// siDefect reports the SI finding kinds the joint "high-speed" item and the
// finer-grid retries treat as defects: length/skew/via budgets and
// return-path splits. The pair coupling / symmetry findings (pairsi.go) of
// intent-declared pairs are scored by their own "diff-pair" item of the
// electrical group (joint.go) — the placer keeps those pairs' corridors
// clear and orients their in-line parts with the flow, so a pair that still
// does not couple is this layout's defect, not every candidate's.
func siDefect(kind string) bool {
	switch kind {
	case "skew", "split-crossing", "vias", "group-skew":
		return true
	}
	return false
}

// pairDefect reports the pair coupling / symmetry finding kinds.
func pairDefect(kind string) bool {
	switch kind {
	case "coupling", "uncoupled", "via-asymmetry", "layer-asymmetry":
		return true
	}
	return false
}

// betterHS keeps the retry only when it is no worse on completion and DRC
// and strictly better on high-speed findings.
func betterHS(b *Board, rr2 *RouteResult, drc2 *DRCReport, an2 *Analysis, res *Result) bool {
	c1, c2 := res.Route.Stats.Completion, rr2.Stats.Completion
	if c2 != c1 {
		return c2 > c1
	}
	if v1, v2 := len(res.DRC.Violations), len(drc2.Violations); v2 != v1 {
		return v2 < v1
	}
	return hsFindings(CheckSI(b, an2, res.Stackup, rr2)) < hsFindings(CheckSI(b, res.Analysis, res.Stackup, res.Route))
}

// timeLeft reports whether ctx leaves room for another routing pass of
// budget d (always true without a deadline or a route timeout). The optional
// retries (layer escalation, finer-grid high-speed passes, IR-drop re-route)
// each cost a full pass: on slow boards (high-voltage clearances) they ran
// the whole command past its context and `pcb auto run --place` failed with
// "context deadline exceeded" instead of returning the routed board.
func timeLeft(ctx context.Context, d time.Duration) bool {
	dl, ok := ctx.Deadline()
	if !ok || d <= 0 || VirtualClock() {
		// On the virtual clock a wall deadline must not decide which passes
		// run (the result would depend on the machine again).
		return true
	}
	return time.Until(dl) > d+d/4
}

// attemptMillis is an attempt's duration: wall time, or the router's
// virtual time under a work budget (RouteOptions.WorkRate), which the
// finer-grid retry test reads — a wall-clock figure there would make the
// deterministic bench depend on machine load again.
func attemptMillis(start time.Time, rr *RouteResult) int64 {
	if rr != nil && rr.virtual {
		return rr.Stats.Millis
	}
	return time.Since(start).Milliseconds()
}

// pairDefects counts the pair coupling / symmetry findings of an SI report.
func pairDefects(si *SIReport) int {
	n := 0
	for _, f := range si.Findings {
		if pairDefect(f.Kind) {
			n++
		}
	}
	return n
}

// hasUnitPairs reports an analysis with a differential pair the router
// routes as a unit (both legs declared by an intent interface).
func hasUnitPairs(an *Analysis) bool {
	for _, np := range an.Nets {
		if np.PairWith == "" || np.Interface == "" {
			continue
		}
		if p := an.ByNet[np.PairWith]; p != nil && p.Interface != "" {
			return true
		}
	}
	return false
}

// pairUnitsWorse reports that the leg-by-leg routing (2) beats the unit
// routing (1): more connections, else fewer open plane/ground connections,
// else fewer DRC violations, else a better
// electrical group of the joint score (ESD stub, decoupling and hot loops,
// IR drop, skew/split/via budgets — a pair routed as a unit must not buy
// its coupling with a protection device pushed off the line: ESP32 mini
// USB, 68 → 115 mil ESD stub), else fewer high-speed defects counting the
// pair coupling / symmetry findings.
func pairUnitsWorse(b *Board, an1 *Analysis, st *Stackup, rr1 *RouteResult, drc1 *DRCReport, an2 *Analysis, rr2 *RouteResult, drc2 *DRCReport) bool {
	if c1, c2 := rr1.Stats.Completion, rr2.Stats.Completion; c1 != c2 {
		return c2 > c1
	}
	j1 := Joint(b, an1, Understand(b, an1), st, rr1, drc1, JointOptions{PlacementScore: -1})
	j2 := Joint(b, an2, Understand(b, an2), st, rr2, drc2, JointOptions{PlacementScore: -1})
	// Plane and ground connections are connections too: signal completion
	// alone kept a unit result with a GND pad cut off its plane over a leg-by-
	// leg one with none (hdmi-tx stress, once pair coupling scored in the
	// electrical group).
	if j1.PlaneOpen != j2.PlaneOpen {
		return j2.PlaneOpen < j1.PlaneOpen
	}
	if v1, v2 := len(drc1.Violations), len(drc2.Violations); v1 != v2 {
		return v2 < v1
	}
	if e1, e2 := j1.Groups["electrical"], j2.Groups["electrical"]; math.Abs(e1-e2) > 0.5 {
		return e2 > e1
	}
	bad := func(an *Analysis, rr *RouteResult) int {
		n := 0
		for _, f := range CheckSI(b, an, st, rr).Findings {
			if siDefect(f.Kind) || pairDefect(f.Kind) {
				n++
			}
		}
		return n
	}
	return bad(an2, rr2) < bad(an1, rr1)
}
