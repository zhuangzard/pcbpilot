package analogsim

import (
	"fmt"
	"math"
)

// findings turns metric/target misses into designer findings.
func (r *runner) findings(blk *Block) {
	b := blk.c
	for _, m := range blk.Metrics {
		if m.Target == nil || m.Status == StatusPass {
			continue
		}
		sev := "warn"
		if m.Status == StatusFail {
			sev = "error"
		}
		kind, sugg := "analog-target-miss", "adjust the component values (see the optimisation plan) or revise the spec"
		switch m.Name {
		case "phaseMarginDeg":
			kind = "analog-phase-margin"
			sugg = "add an isolation resistor (10–100 Ω) between the op-amp output and the capacitive load, reduce the load capacitance, add a feedback capacitor, or use a part with more phase margin at this noise gain"
		case "outMaxV", "outMinV":
			kind = "analog-clipping"
			sugg = "use a rail-to-rail-output op-amp, raise its supply, or scale the signal / ADC reference so the used range stays inside the output swing"
		case "settleErrorLsb":
			kind = "analog-adc-source-impedance"
			sugg = "lower the source impedance (buffer the node), add a charge-reservoir capacitor at the ADC pin (≥ 2^(N+1)·Csh) with its R·C settled before each sample, or lengthen the ADC sampling time"
		case "clPF":
			kind = "analog-crystal-cl"
			sugg = "choose C1 = C2 ≈ 2·(CL − Cstray); see the optimisation plan for stocked values"
		case "ikA":
			kind = "analog-ref-bias"
			sugg = "resize the bias resistor so the cathode current stays inside the datasheet window at minimum supply and maximum load"
		case "delayS":
			kind = "analog-reset-delay"
			sugg = "increase the RC (the datasheet recommendation is listed in the model) or use a supervisor"
		case "overdrive":
			kind = "analog-bjt-saturation"
			sugg = "reduce the base resistor so Ib·hFE(min) ≥ 2·Ic (hard saturation)"
		case "icPeakA":
			kind = "analog-transient-current"
			sev = "warn"
			sugg = "the peak is a short capacitor-discharge pulse; add a series resistor on the collector/capacitor path or confirm the pulse rating in the datasheet"
		case "peakingDB":
			kind = "analog-lc-peaking"
			sugg = "damp the LC resonance: a series R (0.5–2 Ω) with the output capacitor, an electrolytic/tantalum bulk capacitor with ESR, or a lossier ferrite"
		case "voutV":
			kind = "analog-regulator-vout"
			sugg = "re-pick the feedback divider (see the optimisation plan)"
		case "vgsOnV":
			kind = "analog-gate-drive"
			sugg = "the low-side supply is too close to the MOSFET threshold: choose a lower-VGS(th) FET"
		}
		msg := fmt.Sprintf("%s %s outside the target %s (%s)", m.Label, fmtMetric(m.Value, m.Unit), fmtTarget(m.Target, m.Unit), m.Target.Source)
		r.addFinding(blk, sev, kind, msg, sugg)
	}
	// Tolerance spread.
	if t := blk.Tolerance; t != nil && t.YieldPc != nil && *t.YieldPc < 95 && failing(blk) == "" {
		r.addFinding(blk, "warn", "analog-tolerance-spread", fmt.Sprintf("only %.0f %% of %d Monte-Carlo runs meet every target (component tolerances)", *t.YieldPc, t.Runs),
			"use tighter parts (1 % resistors, C0G/NP0 ±5 % capacitors) or widen the target window")
	}
	// Output current.
	if m, ok := blk.metric("ioutPeakA"); ok && len(b.opamps) > 0 && b.opamps[0].Model.IoutMaxA > 0 && m.Value > b.opamps[0].Model.IoutMaxA {
		r.addFinding(blk, "warn", "analog-output-current", fmt.Sprintf("op-amp output current peaks at %s A on the test step (rating %s A)", FormatSI(m.Value), FormatSI(b.opamps[0].Model.IoutMaxA)),
			"the step drives a capacitive load hard; add an isolation resistor or limit the slew")
	}
	// Large overshoot on plain amplifiers.
	if m, ok := blk.metric("overshootPct"); ok && m.Value > 25 && !isSecondOrder(blk.Class) {
		r.addFinding(blk, "warn", "analog-overshoot", fmt.Sprintf("small-signal step overshoots %.0f %% — low phase margin or an unintended resonance", m.Value),
			"see the loop-gain plot; add isolation / compensation")
	}
	// ADC: static charge-sharing error on a large external capacitor with a slow source.
	if blk.Class == ClassADCInput {
		if rs, ok := blk.Analytic["sourceOhm"]; ok {
			if rmax, ok2 := blk.Analytic["rsMaxOhm"]; ok2 && rs > rmax && blk.Analytic["cextF"] == 0 {
				r.addFinding(blk, "warn", "analog-adc-source-impedance", fmt.Sprintf("source impedance %sΩ exceeds %sΩ for settling in the sampling time and there is no reservoir capacitor", FormatSI(rs), FormatSI(math.Max(rmax, 0))),
					"add a capacitor at the ADC pin or buffer the source")
			}
		}
	}
	// Crystal ppm.
	if m, ok := blk.metric("freqErrorPpm"); ok && math.Abs(m.Value) > 20 {
		r.addFinding(blk, "warn", "analog-crystal-pulling", fmt.Sprintf("load mismatch pulls the frequency by %.1f ppm (assumed C0/C1 motional values)", m.Value), "match CL (see the plan)")
	}
	// Reset: info on the recommended RC.
	if blk.Class == ClassResetRC && blk.Model != nil && blk.Model.Params["minDelayS"] > 0 {
		if m, ok := blk.metric("delayS"); ok && m.Status == StatusPass {
			r.addFinding(blk, "info", "analog-reset-ok", fmt.Sprintf("%s rises to VIH %s after the rail is stable (≥ %s required)", blk.Output, fmtMetric(m.Value, "s"), FormatSI(blk.Model.Params["minDelayS"])+"s"), "")
		}
	}
}

func fmtMetric(v float64, unit string) string {
	switch unit {
	case "V/V":
		return fmt.Sprintf("%.4g V/V", v)
	case "%", "°", "dB", "ppm", "LSB", "×", "":
		return fmt.Sprintf("%.3g%s", v, map[bool]string{true: " " + unit, false: ""}[unit != ""])
	}
	return FormatSI(v) + unit
}

func fmtTarget(t *Target, unit string) string {
	switch {
	case t.Value != nil:
		return fmt.Sprintf("%s ± %g %%", fmtMetric(*t.Value, unit), t.TolPct)
	case t.Min != nil && t.Max != nil:
		return fmt.Sprintf("%s … %s", fmtMetric(*t.Min, unit), fmtMetric(*t.Max, unit))
	case t.Min != nil:
		return "≥ " + fmtMetric(*t.Min, unit)
	case t.Max != nil:
		return "≤ " + fmtMetric(*t.Max, unit)
	}
	return "?"
}

// FormatMetric / FormatTarget are exported for the report.
func FormatMetric(v float64, unit string) string { return fmtMetric(v, unit) }
func FormatTarget(t *Target, unit string) string { return fmtTarget(t, unit) }
