package analogsim

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// optimise searches E-series / stocked values that bring the block's
// evaluable metrics onto their targets, verifies the candidate with ngspice
// and keeps it only when the simulated cost improves.
func (r *runner) optimise(blk *Block, ts targetSet) {
	b := blk.c
	opt := &Optimisation{Before: map[string]float64{}, Verified: "analytic"}
	blk.Optimise = opt
	if sb := r.specFor(blk); sb != nil && sb.Optimise != nil && !*sb.Optimise {
		opt.Status, opt.Reason = "not-needed", "optimisation disabled by the spec for this block"
		return
	}
	vals := r.values(blk)
	if b.eval == nil || len(vals) == 0 {
		opt.Status, opt.Reason = "unsupported", "no analytic model of this block's values"
		if failing(blk) == "" {
			opt.Status, opt.Reason = "not-needed", "every target is met"
		}
		return
	}
	base := b.eval(vals)
	goals := targetSet{}
	for name, t := range ts {
		if _, ok := base[name]; !ok {
			continue
		}
		m, measured := blk.metric(name)
		if t.Source == "spec" || (measured && m.Status != StatusPass) || (!measured && !passes(t, base[name])) {
			goals[name] = t
		}
	}
	// Spec targets that already pass in simulation need no change.
	anyFail := false
	for name, t := range goals {
		if m, ok := blk.metric(name); ok {
			if in, _ := t.within(m.Value); !in {
				anyFail = true
			}
		} else if !passes(t, base[name]) {
			anyFail = true
		}
	}
	for _, m := range blk.Metrics {
		opt.Before[m.Name] = m.Value
	}
	if !anyFail {
		if f := failing(blk); f != "" {
			opt.Status, opt.Reason = "unsupported", "failing metric "+f+" is not a function of the component values in the analytic model — see the finding's suggestion"
		} else {
			opt.Status, opt.Reason = "not-needed", "every target is met"
		}
		return
	}
	fixed := map[string]bool{}
	if sb := r.specFor(blk); sb != nil {
		for _, f := range sb.Fixed {
			fixed[f] = true
		}
	}
	// Free components: those the goal metrics are sensitive to.
	var free []string
	for _, comp := range blk.Components {
		if fixed[comp.Ref] {
			continue
		}
		v2 := copyVals(vals)
		v2[comp.Ref] *= 1.1
		m2 := b.eval(v2)
		for name := range goals {
			if math.Abs(m2[name]-base[name]) > 1e-9*math.Max(1, math.Abs(base[name])) {
				free = append(free, comp.Ref)
				break
			}
		}
	}
	sort.Strings(free)
	if len(free) == 0 {
		opt.Status, opt.Reason = "unsupported", "no free component affects the failing metrics"
		return
	}
	comp := map[string]Component{}
	for _, cpt := range blk.Components {
		comp[cpt.Ref] = cpt
	}
	cost := func(v map[string]float64) float64 {
		m := b.eval(v)
		c := 0.0
		for name, t := range goals {
			x, ok := m[name]
			if !ok || math.IsNaN(x) || math.IsInf(x, 0) {
				return math.Inf(1)
			}
			_, e := t.within(x)
			if t.Value == nil && e == 0 {
				continue
			}
			c += e * e
		}
		for _, ref := range free {
			if math.Abs(v[ref]-vals[ref])/vals[ref] > 1e-9 {
				// Practicality: every change costs, a part that is not stocked
				// (standard-parts.json) costs more, E96-only resistors a little.
				c += 0.05
				cp := comp[ref]
				if r.stock != nil && !r.stock.Has(cp.Kind, v[ref], cp.Package) {
					c += 0.1
				}
				if cp.Kind == "R" && !InSeries(v[ref], "E24") {
					c += 0.02
				}
				if cp.Kind == "R" && (v[ref] < 100 || v[ref] > 2e6) {
					c += 1
				}
				if cp.Kind == "C" && v[ref] < 10e-12 {
					c += 1
				}
			}
		}
		return c
	}
	cands := func(ref string, cur float64) []float64 {
		cp := comp[ref]
		var out []float64
		switch cp.Kind {
		case "R":
			out = append(out, SeriesValues("E96", cur/4, cur*4)...)
			out = append(out, SeriesValues("E24", cur/4, cur*4)...)
			out = append(out, r.stock.Values("R", cur/4, cur*4)...)
		case "C":
			out = append(out, SeriesValues("E12", cur/20, cur*20)...)
			out = append(out, r.stock.Values("C", cur/20, cur*20)...)
		case "L":
			out = append(out, SeriesValues("E12", cur/5, cur*5)...)
		}
		return out
	}
	starts := []map[string]float64{copyVals(vals)}
	starts = append(starts, r.closedForm(blk, goals, vals, free)...)
	best, bestCost := copyVals(vals), cost(vals)
	iters := 0
	for _, st := range starts {
		cur, cc := st, cost(st)
		for it := 0; it < 40; it++ {
			iters++
			moved := false
			var bestRef string
			bestVal, bestC := 0.0, cc
			for _, ref := range free {
				for _, v := range cands(ref, cur[ref]) {
					old := cur[ref]
					cur[ref] = v
					if c := cost(cur); c < bestC-1e-12 {
						bestC, bestRef, bestVal = c, ref, v
					}
					cur[ref] = old
				}
			}
			if bestRef != "" {
				cur[bestRef], cc, moved = bestVal, bestC, true
			}
			if !moved {
				break
			}
		}
		if cc < bestCost-1e-12 {
			best, bestCost = copyVals(cur), cc
		}
	}
	opt.Iterations = iters
	opt.CostBefore = round6(cost(vals))
	changed := false
	for _, ref := range free {
		if math.Abs(best[ref]-vals[ref])/vals[ref] > 1e-9 {
			changed = true
		}
	}
	if !changed {
		opt.Status, opt.Reason = "no-gain", "no E-series / stocked combination improves the targets"
		return
	}
	after := b.eval(best)
	// Verify with ngspice: same analyses with the new values.
	simCost := func(m map[string]float64) float64 {
		c := 0.0
		for name, t := range goals {
			x, ok := m[name]
			if !ok {
				continue
			}
			_, e := t.within(x)
			if t.Value == nil && e == 0 {
				continue
			}
			c += e * e
		}
		return c
	}
	afterM := map[string]float64{}
	for k, v := range after {
		afterM[k] = round6(v)
	}
	beforeM := map[string]float64{}
	for name := range goals {
		if m, ok := blk.metric(name); ok {
			beforeM[name] = m.Value
		} else {
			beforeM[name] = round6(base[name])
		}
	}
	if blk.Simulated && r.bin != "" {
		ms, err := r.simulate(blk, best, "after")
		if err != nil {
			opt.Status, opt.Reason = "failed", "ngspice verification failed: "+err.Error()
			return
		}
		afterM = map[string]float64{}
		for k, m := range ms {
			afterM[k] = round6(m.Value)
		}
		for k, v := range after {
			if _, ok := afterM[k]; !ok {
				afterM[k] = round6(v)
			}
		}
		opt.Verified = "ngspice"
		if simCost(afterM) >= simCost(beforeM)-1e-9 {
			opt.Status, opt.Reason = "no-gain", fmt.Sprintf("the analytic optimum did not improve the ngspice result (cost %.3g → %.3g)", simCost(beforeM), simCost(afterM))
			opt.After = afterM
			return
		}
	}
	opt.After = afterM
	opt.CostAfter = round6(bestCost)
	opt.Status = "improved"
	if b.mc {
		opt.ToleranceAfter = r.monteCarlo(blk, best, ts, afterM, "after")
		if ta := opt.ToleranceAfter; ta != nil && ta.YieldPc != nil && *ta.YieldPc < 95 {
			r.addFinding(blk, "info", "analog-tolerance-after", fmt.Sprintf("with the proposed values %.0f %% of %d Monte-Carlo runs meet every target", *ta.YieldPc, ta.Runs),
				"tighten the tolerance of the frequency-setting parts (C0G/NP0 ±5 % capacitors, 1 % or 0.5 % resistors) or widen the target window")
		}
	}
	// Before/after of the goal metrics only.
	bm, am := map[string]float64{}, map[string]float64{}
	var why []string
	for _, name := range sortedKeys(goals) {
		bm[name], am[name] = beforeM[name], afterM[name]
		l := metricLabels[name]
		why = append(why, fmt.Sprintf("%s %s → %s (target %s, %s)", nz(l[0], name), fmtMetric(beforeM[name], l[1]), fmtMetric(afterM[name], l[1]), fmtTarget(goals[name], l[1]), goals[name].Source))
	}
	for _, ref := range free {
		if math.Abs(best[ref]-vals[ref])/vals[ref] <= 1e-9 {
			continue
		}
		cp := comp[ref]
		ch := Change{Ref: ref, Block: blk.ID, Role: cp.Role, Kind: cp.Kind, From: cp.Text, To: FormatValue(best[ref], cp.Kind),
			FromValue: vals[ref], ToValue: cleanFloat(best[ref]), Series: SeriesOf(best[ref]), Package: cp.Package,
			Reason: strings.Join(why, "; "), Before: bm, After: am}
		if r.stock != nil {
			if sp := r.stock.Find(cp.Kind, best[ref], cp.Package); sp != nil {
				ch.Part, ch.Action = sp, "replace-lcsc"
			}
		}
		if ch.Part == nil {
			ch.Action, ch.NeedsPartSelection = "set-value", true
			tol := fmt.Sprintf("%g%%", cp.TolPct)
			ch.SearchHint = strings.TrimSpace(fmt.Sprintf("%s %s %s %s", cp.Package, ch.To, tol, map[string]string{"R": "resistor", "C": "capacitor", "L": "inductor"}[cp.Kind]))
		}
		opt.Changes = append(opt.Changes, ch)
	}
}

func passes(t *Target, v float64) bool { ok, _ := t.within(v); return ok }

func failing(blk *Block) string {
	for _, m := range blk.Metrics {
		if m.Target != nil && m.Status != StatusPass {
			return m.Name
		}
	}
	return ""
}

func copyVals(v map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(v))
	for k, x := range v {
		out[k] = x
	}
	return out
}

// closedForm proposes analytic starting points (Sallen-Key capacitor-ratio
// design, crystal 2·(CL − Cs), regulator Rtop, reference Rbias, BJT Rb).
func (r *runner) closedForm(blk *Block, goals targetSet, vals map[string]float64, free []string) []map[string]float64 {
	b := blk.c
	var out []map[string]float64
	isFree := map[string]bool{}
	for _, f := range free {
		isFree[f] = true
	}
	tv := func(name string) (float64, bool) {
		if t, ok := goals[name]; ok && t.Value != nil {
			return *t.Value, true
		}
		return 0, false
	}
	switch blk.Class {
	case ClassSKLowPass, ClassSKHighPass:
		q, okq := tv("q")
		if !okq {
			q = b.eval(vals)["q"]
		}
		f0, ok := tv("f0Hz")
		if fc, okc := tv("fcHz"); okc && !ok {
			if blk.Class == ClassSKLowPass {
				f0 = fc / fcLowPass2(1, q)
			} else {
				f0 = fc / fcHighPass2(1, q)
			}
			ok = true
		}
		if !ok || q <= 0 {
			return nil
		}
		w0 := 2 * math.Pi * f0
		r1, r2, c1, c2 := b.roleRef["R1"], b.roleRef["R2"], b.roleRef["C1"], b.roleRef["C2"]
		if b.roleRef["Rf"] != "" {
			return nil // with gain: coordinate descent only
		}
		for _, cg := range SeriesValues("E12", 100e-12, 2.2e-6) {
			start := copyVals(vals)
			if blk.Class == ClassSKLowPass {
				// C2 (to ground) = cg; C1 (feedback) ≥ 4Q²·C2; R1+R2 = √(R1R2C1C2)/(Q·C2).
				cf := 0.0
				for _, x := range SeriesValues("E12", 4*q*q*cg, 4*q*q*cg*2) {
					cf = x
					break
				}
				if cf == 0 {
					continue
				}
				P := 1 / (w0 * w0 * cf * cg)
				S := math.Sqrt(P*cf*cg) / (q * cg)
				d := S*S - 4*P
				if d < 0 {
					continue
				}
				ra, rb := (S+math.Sqrt(d))/2, (S-math.Sqrt(d))/2
				start[c1], start[c2] = cf, cg
				start[r1], start[r2] = Snap(ra, "E96"), Snap(rb, "E96")
			} else {
				// Equal capacitors: R1 = 1/(2Q·ω0·C), R2 = 4Q²·R1.
				start[c1], start[c2] = cg, cg
				ra := 1 / (2 * q * w0 * cg)
				start[r1], start[r2] = Snap(ra, "E96"), Snap(4*q*q*ra, "E96")
			}
			ok := true
			for k := range start {
				if !isFree[k] && math.Abs(start[k]-vals[k]) > 1e-15 {
					ok = false
				}
			}
			if ok {
				out = append(out, start)
			}
		}
		// Keep the handful of closest starts.
		sort.Slice(out, func(i, j int) bool { return r.startCost(blk, goals, out[i]) < r.startCost(blk, goals, out[j]) })
		if len(out) > 4 {
			out = out[:4]
		}
	case ClassCrystal:
		if t, ok := goals["clPF"]; ok && t.Value != nil {
			cEach := 2 * (*t.Value - r.lib.Defaults.StrayPF) * 1e-12
			if cEach > 0 {
				start := copyVals(vals)
				for _, role := range []string{"C1", "C2"} {
					if ref := b.roleRef[role]; isFree[ref] {
						start[ref] = Snap(cEach, "E12")
					}
				}
				out = append(out, start)
			}
		}
	}
	return out
}

func (r *runner) startCost(blk *Block, goals targetSet, v map[string]float64) float64 {
	m := blk.c.eval(v)
	c := 0.0
	for name, t := range goals {
		if x, ok := m[name]; ok {
			_, e := t.within(x)
			c += e * e
		}
	}
	return c
}
