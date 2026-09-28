package analogsim

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const plotPoints = 240

// acSetup returns the frequency range / step timing of a block.
func (r *runner) acSetup(blk *Block, vals map[string]float64) spiceSetup {
	b := blk.c
	fc := 0.0
	var a map[string]float64
	if b.eval != nil {
		a = b.eval(vals)
		for _, k := range []string{"fcHz", "f0Hz", "unityHz"} {
			if v := a[k]; v > 0 && !math.IsInf(v, 0) {
				fc = v
				break
			}
		}
	}
	gbw := 0.0
	if len(b.opamps) > 0 {
		gbw = b.opamps[0].Model.GBWHz
	}
	for _, am := range b.amps {
		gbw = math.Max(gbw, am.Model.BWHz*10)
	}
	if fc <= 0 {
		if gbw > 0 {
			fc = gbw / math.Max(1, math.Abs(b.gainGuess))
		} else {
			fc = 1e3
		}
	}
	s := spiceSetup{vals: vals, ptsDec: 50}
	s.f1 = math.Max(0.01, fc/1e3)
	s.f2 = fc * 1e3
	if gbw > 0 {
		s.f2 = math.Max(s.f2, gbw*20)
	}
	if s.f2 > 1e9 {
		s.f2 = 1e9
	}
	if s.f1 > s.f2/1e4 {
		s.f1 = s.f2 / 1e4
	}
	tau := 1 / (2 * math.Pi * fc)
	if q := a["q"]; q > 1 {
		tau *= q
	}
	s.tstop = 40 * tau
	if len(b.opamps) > 0 && b.opamps[0].Model.SlewVPerUs > 0 {
		span := 0.0
		for _, v := range b.rails {
			span = math.Max(span, math.Abs(v))
		}
		s.tstop = math.Max(s.tstop, 20*0.05*span/(b.opamps[0].Model.SlewVPerUs*1e6))
	}
	s.t0 = s.tstop * 0.1
	s.tstop += s.t0
	s.tstep = s.tstop / 4000
	return s
}

func span(b *blockCtx) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, o := range b.opamps {
		for _, n := range []string{o.Vp, o.Vn} {
			v := b.rails[n]
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	for _, a := range b.amps {
		for _, n := range []string{a.Vp, a.Vn} {
			v := b.rails[n]
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	if math.IsInf(lo, 0) {
		for _, v := range b.rails {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	if math.IsInf(lo, 0) {
		lo, hi = 0, 3.3
	}
	return
}

// simulate runs the class analyses; tag "" = baseline (curves kept),
// "after" = optimised values (curves overlaid).
func (r *runner) simulate(blk *Block, vals map[string]float64, tag string) (map[string]Metric, error) {
	b := blk.c
	switch {
	case len(b.opamps) > 0 && b.opamps[0].Comparator:
		return r.simComparator(blk, vals, tag)
	case len(b.opamps) > 0 || len(b.amps) > 0 || blk.Class == ClassRCLowPass || blk.Class == ClassLCFilter:
		return r.simAmp(blk, vals, tag)
	case blk.Class == ClassRegulatorFB || blk.Class == ClassReference:
		return r.simOp(blk, vals, tag)
	case blk.Class == ClassCrystal:
		return r.simXtal(blk, vals, tag)
	case blk.Class == ClassResetRC:
		return r.simReset(blk, vals, tag)
	case blk.Class == ClassTransistorSw || blk.Class == ClassLevelShifter:
		return r.simSwitch(blk, vals, tag)
	case blk.Class == ClassADCInput:
		return r.simADC(blk, vals, tag)
	}
	return r.analyticMetrics(blk, vals), nil
}

func (r *runner) addCurve(blk *Block, cv Curve, tag string) {
	if tag == "" {
		for i := range blk.Curves {
			if blk.Curves[i].Name == cv.Name {
				blk.Curves[i] = cv
				return
			}
		}
		blk.Curves = append(blk.Curves, cv)
		return
	}
	for i := range blk.Curves {
		old := &blk.Curves[i]
		if old.Name != cv.Name {
			continue
		}
		// Overlay the "after" traces on the baseline x grid.
		for j := range old.Series {
			if old.Series[j].Style == "" {
				old.Series[j].Style = "before"
			}
		}
		for i, s := range cv.Series {
			if s.Axis == "right" || i > 0 {
				continue // overlay the main trace only
			}
			y := make([]float64, len(old.X))
			for k, x := range old.X {
				y[k] = round6(valueAt(cv.X, s.Y, x))
			}
			old.Series = append(old.Series, Series{Name: s.Name + "（优化后）", Unit: s.Unit, Axis: s.Axis, Y: y, Style: "after"})
		}
		return
	}
}

func roundSlice(xs []float64) []float64 {
	out := make([]float64, len(xs))
	for i, x := range xs {
		out[i] = round6(x)
	}
	return out
}

func bodeCurve(name, title string, bd *bode) Curve {
	x, ys := decimate(bd.f, [][]float64{bd.db, bd.phase}, plotPoints, true)
	return Curve{Name: name, Title: title, XLabel: "频率", XUnit: "Hz", LogX: true, X: roundSlice(x),
		Series: []Series{{Name: "幅度", Unit: "dB", Y: roundSlice(ys[0])}, {Name: "相位", Unit: "°", Axis: "right", Y: roundSlice(ys[1])}}}
}

func timeCurve(name, title string, t []float64, series []Series) Curve {
	var ys [][]float64
	for _, s := range series {
		ys = append(ys, s.Y)
	}
	x, yd := decimate(t, ys, plotPoints, false)
	for i := range series {
		series[i].Y = roundSlice(yd[i])
	}
	return Curve{Name: name, Title: title, XLabel: "时间", XUnit: "s", X: roundSlice(x), Series: series}
}

// simAmp: DC sweep (bias, swing, Vcm) → .op/.ac/.tran → loop gain → ADC sample.
func (r *runner) simAmp(blk *Block, vals map[string]float64, tag string) (map[string]Metric, error) {
	b := blk.c
	out := map[string]Metric{}
	put := func(name string, v float64, method string) {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			out[name] = newMetric(name, v, method)
		}
	}
	s := r.acSetup(blk, vals)
	lo, hi := span(b)
	sp := hi - lo
	g := 1.0
	if b.eval != nil {
		if v := b.eval(vals)["gain"]; v > 0 {
			g = v
		}
	} else if b.gainGuess > 0 {
		g = b.gainGuess
	}
	bias := 0.0
	if b.stim != "" && r.c.isRail(b.stim) {
		bias = r.c.railV[b.stim]
	}
	var outMax, outMin, slope float64
	haveDC := false
	if b.dcSweep && b.stim != "" && len(b.opamps)+len(b.amps) > 0 {
		A := 1.5 * (math.Abs(hi) + math.Abs(lo) + sp) / g
		ds := s
		ds.mode, ds.sweepLo, ds.sweepHi, ds.sweepSt = modeDC, -A, A, 2*A/2000
		sim, err := r.run(blk, ds, tag)
		if err != nil {
			return nil, err
		}
		t := sim.data["dc"]
		if t != nil && len(t.rows) > 10 {
			haveDC = true
			vin, vout := t.col(0), t.col(1)
			outMax, outMin = math.Inf(-1), math.Inf(1)
			for _, v := range vout {
				outMax, outMin = math.Max(outMax, v), math.Min(outMin, v)
			}
			target := (outMax + outMin) / 2
			if b.adc != nil && b.adc.VrefV > 0 {
				target = math.Min(outMax-0.05*sp, math.Max(outMin+0.05*sp, b.adc.VrefV/2))
			}
			up, down := crossings(vin, vout, target)
			x := math.NaN()
			if len(up) > 0 {
				x = up[0]
			} else if len(down) > 0 {
				x = down[0]
			}
			if !math.IsNaN(x) {
				bias = x
				d := 2 * A / 2000 * 3
				slope = (valueAt(vin, vout, x+d) - valueAt(vin, vout, x-d)) / (2 * d)
			}
			put("outMaxV", outMax, "ngspice-dc")
			put("outMinV", outMin, "ngspice-dc")
			put("biasInV", bias, "ngspice-dc")
			// Common-mode of each op-amp at the bias point.
			for i, o := range b.opamps {
				if 2+2*i < len(t.rows[0]) {
					vcm := valueAt(vin, t.col(2+2*i), bias)
					put("vcmV", vcm, "ngspice-dc")
					if tag == "" {
						r.checkVcm(blk, o, vcm)
					}
				}
			}
			if tag == "" {
				cv := timeCurve("transfer", "直流传输特性（输入扫描）", vin, []Series{{Name: "输出", Unit: "V", Y: vout}})
				cv.XLabel, cv.XUnit = "输入", "V"
				cv.Marks = append(cv.Marks, Mark{Label: "偏置点", X: round6(bias), Y: round6(target), Kind: "point"})
				r.addCurve(blk, cv, tag)
			}
			if tag == "" {
				r.checkInputRange(blk, vin, vout, bias, slope)
			}
		}
	}
	if !haveDC && b.stimSet {
		bias = b.stimBias
	}
	if tag == "" {
		b.stimBias, b.stimSet = bias, true
	}
	// Main: .op, .ac, .tran step.
	ms := s
	ms.mode, ms.bias = modeMain, bias
	ms.stepAmp = 0.05 * sp / g
	if len(b.opamps)+len(b.amps) == 0 {
		ms.stepAmp = 1
	}
	if b.resp == "hp" {
		ms.tstop = 0
	}
	sim, err := r.run(blk, ms, tag)
	if err != nil {
		return nil, err
	}
	if t := sim.data["ac"]; t != nil {
		bd := bodeFrom(t, false)
		for k, v := range acMetrics(bd, b.resp, isSecondOrder(blk.Class)) {
			if k == "q" && !isSecondOrder(blk.Class) {
				continue
			}
			put(k, v, "ngspice-ac")
		}
		r.addCurve(blk, bodeCurve("bode", "频率响应（Bode）", bd), tag)
	}
	if t := sim.data["tran"]; t != nil && len(t.rows) > 10 {
		tt, vo := t.col(0), t.col(1)
		for k, v := range stepMetrics(tt, vo, ms.t0) {
			if k == "finalV" {
				continue
			}
			put(k, v, "ngspice-tran")
		}
		if len(t.rows[0]) > 3 {
			ip := 0.0
			for _, x := range t.col(3) {
				ip = math.Max(ip, math.Abs(x))
			}
			put("ioutPeakA", ip, "ngspice-tran")
		}
		r.addCurve(blk, timeCurve("step", "小信号阶跃响应", tt, []Series{{Name: "输出", Unit: "V", Y: vo}, {Name: "输入", Unit: "V", Y: t.col(2), Axis: stepAxis(g)}}), tag)
	}
	if op := sim.op; op != nil {
		if v, ok := op["v("+nodeOf(r.c, b.out)+")"]; ok {
			put("outDcV", v, "ngspice-op")
		}
	}
	// Loop gain.
	if b.loop && len(b.opamps) > 0 && b.stim != "" {
		ls := s
		ls.mode, ls.bias, ls.ptsDec = modeLoop, bias, 100
		ls.f1 = 1
		ls.f2 = math.Min(1e9, b.opamps[0].Model.GBWHz*100)
		sim, err := r.run(blk, ls, tag)
		if err == nil {
			if t := sim.data["loop"]; t != nil {
				bd := bodeFrom(t, true)
				for k, v := range loopMetrics(bd) {
					put(k, v, "ngspice-loop")
				}
				r.addCurve(blk, bodeCurve("loop", "环路增益 T(f)（输出端电压注入）", bd), tag)
			}
		}
	} else if len(b.opamps) > 0 && b.opamps[0].Vendor != nil {
		if m, ok := out["overshootPct"]; ok {
			put("phaseMarginDeg", pmFromOvershoot(m.Value), "ngspice-tran")
			blk.Notes = append(blk.Notes, "vendor model: phase margin estimated from the step overshoot (2nd-order equivalent)")
		}
	}
	// ADC sampling on the output load node.
	if b.adc != nil {
		level := 0.9 * b.adc.VrefV
		sbias := bias
		if haveDC && slope != 0 {
			lvl := math.Min(level, outMax-0.02*sp)
			sbias = bias + (lvl-(outMax+outMin)/2)/slope
			if b.adc.VrefV > 0 {
				sbias = bias + (lvl-math.Min(outMax-0.05*sp, math.Max(outMin+0.05*sp, b.adc.VrefV/2)))/slope
			}
		}
		if v, ok := r.sampleRun(blk, vals, sbias, tag); ok {
			for k, x := range v {
				put(k, x, "ngspice-tran")
			}
		}
	}
	return out, nil
}

func nodeOf(c *circuit, net string) string { return newNamer(c).node(net) }

// sampleRun simulates the ADC hold capacitor over 4 conversions.
func (r *runner) sampleRun(blk *Block, vals map[string]float64, bias float64, tag string) (map[string]float64, bool) {
	a := blk.c.adc
	m := a.Model
	if sa := r.specADC(); sa != nil {
		if sa.TsampleS > 0 {
			m.TsampleS = sa.TsampleS
		}
		if sa.Bits > 0 {
			m.Bits = sa.Bits
		}
		if sa.CshF > 0 {
			m.CshF = sa.CshF
		}
		if sa.RadcOhm > 0 {
			m.RadcOhm = sa.RadcOhm
		}
	}
	saved := a.Model
	a.Model = m
	defer func() { a.Model = saved }()
	per := math.Max(10*m.TsampleS, 1e-6)
	if sa := r.specADC(); sa != nil && sa.SampleRateHz > 0 {
		per = 1 / sa.SampleRateHz
	}
	s := spiceSetup{mode: modeSample, vals: vals, bias: bias, period: per}
	s.tstop = 4 * per
	s.tstep = math.Min(m.TsampleS/200, per/2000)
	sim, err := r.run(blk, s, tag)
	if err != nil {
		return nil, false
	}
	t := sim.data["sample"]
	if t == nil || len(t.rows) < 10 {
		return nil, false
	}
	tt, vin, vh := t.col(0), t.col(1), t.col(2)
	worst, first := 0.0, 0.0
	vref := a.VrefV
	if vref <= 0 {
		vref = 3.3
	}
	for k := 1; k <= 4; k++ {
		tEnd := float64(k)*per - 1e-9
		tStart := float64(k)*per - m.TsampleS - per/4 - 2e-9
		target := valueAt(tt, vin, tStart)
		got := valueAt(tt, vh, tEnd)
		e := math.Abs(target-got) / vref * math.Pow(2, float64(m.Bits))
		if k == 1 {
			first = e
		}
		worst = math.Max(worst, e)
	}
	if tag == "" {
		r.addCurve(blk, timeCurve("sample", fmt.Sprintf("ADC 采样保持（%s，每 %s 一次，Csh 预放电到 0 V）", m.ID, FormatSI(per)+"s"), tt,
			[]Series{{Name: "ADC 引脚", Unit: "V", Y: vin}, {Name: "保持电容", Unit: "V", Y: vh}}), tag)
	}
	return map[string]float64{"settleErrorLsb": worst, "firstSampleErrorLsb": first}, true
}

func (r *runner) specADC() *SpecADC {
	if r.spec == nil {
		return nil
	}
	return r.spec.ADC
}

func (r *runner) checkVcm(blk *Block, o *opampInst, vcm float64) {
	m := o.Model
	vp, vn := blk.c.rails[o.Vp], blk.c.rails[o.Vn]
	lo, hi := vn-m.VcmHeadroomLowV, vp-m.VcmHeadroomHighV
	if vcm < lo-1e-3 || vcm > hi+1e-3 {
		r.addFinding(blk, "error", "analog-vcm-range", fmt.Sprintf("input common mode %.3f V at the bias point is outside %s's range %.2f … %.2f V", vcm, nz(m.ID, o.Ref), lo, hi),
			"re-bias the input (divider to mid-rail) or use a rail-to-rail-input op-amp")
	}
	if m.SupplyMaxV > 0 && vp-vn > m.SupplyMaxV+1e-6 {
		r.addFinding(blk, "error", "analog-supply-range", fmt.Sprintf("supply span %.2f V exceeds %s maximum %.1f V", vp-vn, m.ID, m.SupplyMaxV), "lower the supply or choose a higher-voltage part")
	}
	if m.SupplyMinV > 0 && vp-vn < m.SupplyMinV-1e-6 {
		r.addFinding(blk, "error", "analog-supply-range", fmt.Sprintf("supply span %.2f V is below %s minimum %.1f V", vp-vn, m.ID, m.SupplyMinV), "raise the supply or choose a low-voltage part")
	}
}

// checkInputRange flags clipping for the spec inputRange.
func (r *runner) checkInputRange(blk *Block, vin, vout []float64, bias, slope float64) {
	sb := r.specFor(blk)
	if sb == nil || len(sb.InputRange) != 2 || slope == 0 {
		return
	}
	b0 := valueAt(vin, vout, bias)
	for _, x := range sb.InputRange {
		lin := b0 + slope*(x-bias)
		got := valueAt(vin, vout, x)
		lo, hi := span(blk.c)
		if math.Abs(got-lin) > 0.01*(hi-lo) {
			r.addFinding(blk, "error", "analog-clipping", fmt.Sprintf("input %.3g V (spec inputRange) clips: output %.3f V instead of %.3f V", x, got, lin),
				"reduce the gain/attenuate the input, re-bias, or use a rail-to-rail-output part / wider supply")
		}
	}
}

func pmFromOvershoot(os float64) float64 {
	if os <= 0 {
		return 76
	}
	l := math.Log(os / 100)
	z := -l / math.Sqrt(math.Pi*math.Pi+l*l)
	return math.Atan(2*z/math.Sqrt(math.Sqrt(1+4*z*z*z*z)-2*z*z)) * 180 / math.Pi
}

// simComparator: triangle input, thresholds from the output toggles.
func (r *runner) simComparator(blk *Block, vals map[string]float64, tag string) (map[string]Metric, error) {
	b := blk.c
	out := map[string]Metric{}
	if b.stim == "" {
		return r.analyticMetrics(blk, vals), nil
	}
	o := b.opamps[0]
	lo, hi := span(b)
	a := b.eval(vals)
	rlo, rhi := lo, hi
	if x, ok := a["thresholdLowOutV"]; ok {
		y := a["thresholdHighOutV"]
		m := math.Abs(x-y) + 0.1*(hi-lo)
		rlo, rhi = math.Min(x, y)-m, math.Max(x, y)+m
	}
	s := spiceSetup{mode: modeRamp, vals: vals, sweepLo: rlo, sweepHi: rhi, rampT: 2e-3}
	s.tstop, s.tstep = s.rampT, s.rampT/20000
	sim, err := r.run(blk, s, tag)
	if err != nil {
		return nil, err
	}
	t := sim.data["ramp"]
	if t == nil {
		return out, fmt.Errorf("no ramp data")
	}
	tt, vin, vo := t.col(0), t.col(1), t.col(2)
	mid := (hi + lo) / 2
	up, down := crossings(tt, vo, mid)
	var tog []float64
	tog = append(tog, up...)
	tog = append(tog, down...)
	var rise, fall []float64
	for _, x := range tog {
		v := valueAt(tt, vin, x)
		if x < s.rampT/2 {
			rise = append(rise, v)
		} else {
			fall = append(fall, v)
		}
	}
	if len(rise) > 0 {
		out["thresholdRiseV"] = newMetric("thresholdRiseV", rise[0], "ngspice-tran")
	}
	if len(fall) > 0 {
		out["thresholdFallV"] = newMetric("thresholdFallV", fall[0], "ngspice-tran")
	}
	if len(rise) > 0 && len(fall) > 0 {
		out["hysteresisV"] = newMetric("hysteresisV", math.Abs(rise[0]-fall[0]), "ngspice-tran")
	}
	if len(rise) > 1 || len(fall) > 1 {
		r.addFinding(blk, "warn", "analog-comparator-chatter", fmt.Sprintf("output toggles %d times on one ramp — insufficient hysteresis", len(rise)+len(fall)), "add/increase positive feedback (hysteresis resistor)")
	}
	// Map the analytic thresholds onto rise/fall for the cross-check column.
	if x, ok := a["thresholdLowOutV"]; ok {
		y := a["thresholdHighOutV"]
		d0 := r.c.dcSolve(b.passives, vals, map[string]float64{b.stim: rlo, o.Out: 0})
		d1 := r.c.dcSolve(b.passives, vals, map[string]float64{b.stim: rhi, o.Out: 0})
		nonInv := d1[o.Inp]-d1[o.Inn] > d0[o.Inp]-d0[o.Inn]
		if !nonInv {
			x, y = y, x
		}
		blk.Analytic["thresholdRiseV"], blk.Analytic["thresholdFallV"] = round6(x), round6(y)
	}
	if tag == "" {
		r.addCurve(blk, timeCurve("ramp", "三角波输入下的比较器翻转", tt, []Series{{Name: "输入", Unit: "V", Y: vin}, {Name: "输出", Unit: "V", Y: vo}}), tag)
	}
	return out, nil
}

// simOp: regulator feedback / reference operating point.
func (r *runner) simOp(blk *Block, vals map[string]float64, tag string) (map[string]Metric, error) {
	s := spiceSetup{mode: modeOp, vals: vals}
	sim, err := r.run(blk, s, tag)
	if err != nil {
		return nil, err
	}
	out := map[string]Metric{}
	for k, v := range r.opMetrics(blk, sim.op) {
		out[k] = newMetric(k, v, "ngspice-op")
	}
	for k, v := range blk.c.eval(vals) {
		if _, ok := out[k]; !ok {
			out[k] = newMetric(k, v, "analytic")
		}
	}
	return out, nil
}

func (r *runner) opMetrics(blk *Block, op map[string]float64) map[string]float64 {
	b := blk.c
	out := map[string]float64{}
	if op == nil {
		return out
	}
	vout, ok := op["v("+nodeOf(r.c, b.out)+")"]
	if ok {
		out["voutV"] = vout
	}
	for _, ri := range b.refs {
		if i, ok := op["i(v_ik_"+strings.ToLower(ri.Ref)+")"]; ok {
			out["ikA"] = i
			out["pdW"] = i * vout
		}
	}
	return out
}

// simXtal: crystal BVD model with the actual CL; frequency error vs nominal.
func (r *runner) simXtal(blk *Block, vals map[string]float64, tag string) (map[string]Metric, error) {
	b := blk.c
	a := b.eval(vals)
	out := map[string]Metric{}
	for k, v := range a {
		out[k] = newMetric(k, v, "analytic")
	}
	x := b.xtal
	if x.FHz <= 0 {
		blk.Notes = append(blk.Notes, "crystal frequency unknown — load-resonance simulation skipped")
		return out, nil
	}
	s := spiceSetup{mode: modeXtal, vals: vals, xtalCL: a["clPF"] * 1e-12}
	sim, err := r.run(blk, s, tag)
	if err != nil {
		return nil, err
	}
	t := sim.data["xtal"]
	if t == nil {
		return out, nil
	}
	bd := bodeFrom(t, false)
	best, bi := 0.0, 0
	for i, m := range bd.mag {
		if m > best {
			best, bi = m, i
		}
	}
	f := bd.f[bi]
	if bi > 0 && bi < len(bd.f)-1 {
		// parabolic peak refinement
		y0, y1, y2 := bd.mag[bi-1], bd.mag[bi], bd.mag[bi+1]
		d := 0.5 * (y0 - y2) / (y0 - 2*y1 + y2)
		f += d * (bd.f[bi+1] - bd.f[bi])
	}
	ppm := (f - x.FHz) / x.FHz * 1e6
	if x.CLPF <= 0 {
		blk.Notes = append(blk.Notes, "crystal CL unknown: the BVD model is tuned to the nominal frequency at the actual CL, so ppm is 0 by construction")
	}
	out["freqErrorPpm"] = newMetric("freqErrorPpm", ppm, "ngspice-ac")
	if tag == "" {
		cv := bodeCurve("xtal", "晶体 + 负载电容 串联谐振（电流幅度）", bd)
		cv.Series = cv.Series[:1]
		cv.Series[0].Name = "|I| (dB)"
		cv.LogX = false
		r.addCurve(blk, cv, tag)
	}
	return out, nil
}

// simReset: supply ramp then the RC charges the enable pin.
func (r *runner) simReset(blk *Block, vals map[string]float64, tag string) (map[string]Metric, error) {
	b := blk.c
	a := b.eval(vals)
	ramp := r.lib.Defaults.RampS
	tau := a["tauS"]
	s := spiceSetup{mode: modeRStep, vals: vals}
	s.tstop = ramp + 8*tau
	s.tstep = s.tstop / 20000
	sim, err := r.run(blk, s, tag)
	if err != nil {
		return nil, err
	}
	out := map[string]Metric{"tauS": newMetric("tauS", tau, "analytic")}
	t := sim.data["rstep"]
	if t == nil {
		return out, fmt.Errorf("no reset data")
	}
	tt, ve, vs := t.col(0), t.col(1), t.col(2)
	up, _ := crossings(tt, ve, b.vih)
	if len(up) > 0 {
		out["delayS"] = newMetric("delayS", up[0]-ramp, "ngspice-tran")
		out["delayS"] = withNote(out["delayS"], fmt.Sprintf("time from the rail reaching its final value (%s ramp) to %s ≥ VIH %.3g V", FormatSI(ramp)+"s", blk.Output, b.vih))
	}
	if tag == "" {
		cv := timeCurve("rstep", "上电后复位/使能引脚充电", tt, []Series{{Name: blk.Output, Unit: "V", Y: ve}, {Name: "电源", Unit: "V", Y: vs}})
		cv.Marks = append(cv.Marks, Mark{Label: "VIH", Y: round6(b.vih), Kind: "y"})
		r.addCurve(blk, cv, tag)
	}
	return out, nil
}

func withNote(m Metric, note string) Metric { m.Note = note; return m }

// simSwitch: on-state .op (saturation) and an on/off transient of the load.
func (r *runner) simSwitch(blk *Block, vals map[string]float64, tag string) (map[string]Metric, error) {
	b := blk.c
	out := map[string]Metric{}
	put := func(name string, v float64, method string) {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			out[name] = newMetric(name, v, method)
		}
	}
	sim, err := r.run(blk, spiceSetup{mode: modeOp, vals: vals}, tag)
	if err != nil {
		return nil, err
	}
	op := sim.op
	for _, q := range b.bjts {
		ref := strings.ToLower(q.Ref)
		vc := op["v(ic_"+ref+")"]
		ve := op["v("+nodeOf(r.c, q.E)+")"]
		vb := op["v("+nodeOf(r.c, q.B)+")"]
		ic := op["i(v_ic_"+ref+")"]
		put("vceSatV", vc-ve, "ngspice-op")
		put("icA", ic, "ngspice-op")
		if rd := b.roleRef["Rdrive"]; rd != "" {
			ib := (b.driveHigh - vb) / vals[rd]
			if rpd := b.roleRef["Rpd"]; rpd != "" {
				ib -= vb / vals[rpd]
			}
			put("ibA", ib, "ngspice-op")
			if math.Abs(ic) > 1e-7 && ib > 0 {
				put("forcedBeta", math.Abs(ic)/ib, "ngspice-op")
				if q.Sw != nil {
					put("overdrive", ib*q.Sw.HfeMin/math.Abs(ic), "ngspice-op")
				}
			}
		}
	}
	for _, f := range b.fets {
		ref := strings.ToLower(f.Ref)
		vg := op["v("+nodeOf(r.c, f.G)+")"]
		vs := op["v("+nodeOf(r.c, f.S)+")"]
		put("vgsOnV", vg-vs, "ngspice-op")
		put("vdsOnV", op["v(id_"+ref+")"]-vs, "ngspice-op")
	}
	if b.stim != "" || b.driveNet != "" {
		tau := 10e-6
		if rl, cl := b.roleRef["Rload"], b.roleRef["Cload"]; rl != "" && cl != "" {
			tau = vals[rl] * vals[cl]
		}
		s := spiceSetup{mode: modeSwitch, vals: vals}
		s.tstop = math.Max(12*tau, 20e-6)
		s.t0 = s.tstop * 0.05
		s.tstop += s.t0
		s.tstep = s.tstop / 100000 // resolves the µs capacitor discharge inside a ms-long window
		sim, err := r.run(blk, s, tag)
		if err == nil {
			if t := sim.data["switch"]; t != nil && len(t.rows) > 10 {
				tt, vo := t.col(0), t.col(1)
				vhi := valueAt(tt, vo, s.t0*0.9)
				t1 := s.t0
				_, down := crossings(tt, vo, 0.1*vhi)
				for _, d := range down {
					if d >= t1 {
						put("fallTimeS", d-t1, "ngspice-tran")
						break
					}
				}
				offAt := s.t0 + s.tstop/2
				up, _ := crossings(tt, vo, b.vih)
				for _, u := range up {
					if u >= offAt {
						put("releaseTimeS", u-offAt, "ngspice-tran")
						break
					}
				}
				if len(t.rows[0]) > 3 {
					ip := 0.0
					for _, x := range t.col(3) {
						ip = math.Max(ip, math.Abs(x))
					}
					put("icPeakA", ip, "ngspice-tran")
				}
				if tag == "" {
					series := []Series{{Name: blk.Output, Unit: "V", Y: vo}, {Name: "驱动", Unit: "V", Y: t.col(2)}}
					if len(t.rows[0]) > 3 {
						series = append(series, Series{Name: "集电极/漏极电流", Unit: "A", Axis: "right", Y: t.col(3)})
					}
					cv := timeCurve("switch", "开关导通/关断瞬态", tt, series)
					if b.vih > 0 {
						cv.Marks = append(cv.Marks, Mark{Label: "VIH", Y: round6(b.vih), Kind: "y"})
					}
					r.addCurve(blk, cv, tag)
				}
			}
		}
	}
	return out, nil
}

// simADC: a passive network (divider / RC) into an ADC pin.
func (r *runner) simADC(blk *Block, vals map[string]float64, tag string) (map[string]Metric, error) {
	b := blk.c
	out := map[string]Metric{}
	for k, v := range b.eval(vals) {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			out[k] = newMetric(k, v, "analytic")
		}
	}
	bias := 0.0
	if b.stim != "" {
		fixed := map[string]float64{}
		for n := range b.rails {
			fixed[n] = b.rails[n]
		}
		fixed[b.stim] = 1
		sol := r.c.dcSolve(b.passives, vals, fixed)
		g := sol[b.out]
		fixed[b.stim] = 0
		off := r.c.dcSolve(b.passives, vals, fixed)[b.out]
		if g-off > 1e-6 {
			bias = (0.9*b.adc.VrefV - off) / (g - off)
		}
	}
	v, ok := r.sampleRun(blk, vals, bias, tag)
	if !ok {
		return out, fmt.Errorf("ADC sample simulation failed")
	}
	for k, x := range v {
		out[k] = newMetric(k, x, "ngspice-tran")
	}
	return out, nil
}

// stepAxis puts the step input on its own axis when the block attenuates or
// amplifies (the two traces would not share a readable scale).
func stepAxis(g float64) string {
	if g < 0.5 || g > 2 {
		return "right"
	}
	return ""
}

var curveRank = map[string]int{"bode": 1, "loop": 2, "step": 3, "transfer": 4, "sample": 5, "ramp": 6, "switch": 7, "rstep": 8, "xtal": 9}

func sortCurves(cs []Curve) {
	sort.SliceStable(cs, func(i, j int) bool { return curveRank[cs[i].Name] < curveRank[cs[j].Name] })
}
