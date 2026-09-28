package analogsim

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// Options configure one Run.
type Options struct {
	// Ngspice is an explicit binary; "" searches PATH and the usual
	// locations; "none" disables simulation (analytic checks only).
	Ngspice string
	// WorkDir keeps every netlist / log / wrdata file; "" uses a temp dir
	// that is removed afterwards.
	WorkDir  string
	MCRuns   int
	Seed     int
	Optimise bool
	Spec     *Spec
	Stock    *StockLib
	PowerSim *powersim.Output
	// PowerLibs are the power-models.json libraries (regulator Vref, BJT
	// Gummel-Poon numbers).
	PowerLibs powersim.Libraries
	Inputs    *Inputs
}

// Run detects, simulates and optimises every analog block of the design.
func Run(d *powersim.Design, lib *Library, opt Options) (*Output, error) {
	if d == nil {
		return nil, fmt.Errorf("analogsim: no design")
	}
	if lib == nil {
		lib = EmptyLibrary()
	}
	if opt.Spec != nil && opt.Spec.StrayPF > 0 {
		lib.Defaults.StrayPF = opt.Spec.StrayPF
	}
	c := newCircuit(d, lib, opt.PowerSim, opt.PowerLibs)
	if opt.Spec != nil {
		for n, v := range opt.Spec.Rails {
			c.railV[n], c.railSrc[n] = v, "spec"
		}
	}
	out := &Output{SchemaVersion: SchemaVersion, Generator: Generator, Inputs: opt.Inputs, Blocks: []*Block{}, Findings: []Finding{}, Assumptions: []string{}, Warnings: []string{}}
	if opt.MCRuns == 0 {
		opt.MCRuns = lib.Defaults.MCRuns
		if opt.Spec != nil && opt.Spec.MCRuns > 0 {
			opt.MCRuns = opt.Spec.MCRuns
		}
	}
	if opt.Seed == 0 {
		opt.Seed = lib.Defaults.Seed
		if opt.Spec != nil && opt.Spec.Seed != 0 {
			opt.Seed = opt.Spec.Seed
		}
	}
	if opt.Spec != nil && opt.Spec.Optimise != nil {
		opt.Optimise = *opt.Spec.Optimise
	}
	out.Options = &RunOptionsJS{MCRuns: opt.MCRuns, Seed: opt.Seed, Optimise: opt.Optimise}
	r := &runner{c: c, lib: lib, stock: opt.Stock, spec: opt.Spec, opt: opt}
	r.railsFrom = "rail names"
	if opt.PowerSim != nil {
		r.railsFrom = "pcbpilot sim power (typical scenario)"
	}
	if opt.Ngspice != "none" {
		r.bin, r.version = FindNgspice(opt.Ngspice)
	}
	out.Ngspice = NgspiceInfo{Available: r.bin != "", Path: r.bin, Version: r.version}
	if r.bin == "" {
		out.Ngspice.Note = MissingNgspiceNote
	}
	blocks, notes := c.detect()
	out.Warnings = append(out.Warnings, notes...)
	out.Assumptions = append(out.Assumptions, c.assump...)
	if len(blocks) > 0 && r.bin != "" {
		if opt.WorkDir != "" {
			if err := os.MkdirAll(opt.WorkDir, 0o755); err != nil {
				return nil, err
			}
			r.dir, r.keep = opt.WorkDir, true
		} else {
			dir, err := os.MkdirTemp("", "pcbpilot-analog-*")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(dir)
			r.dir = dir
		}
		r.libPath = "pcbpilot-generic.lib"
		if err := os.WriteFile(filepath.Join(r.dir, r.libPath), []byte(GenericLib), 0o644); err != nil {
			return nil, err
		}
		if r.keep {
			r.artifacts = append(r.artifacts, Artifact{Kind: "library", Path: r.libPath})
		}
	}
	for _, b := range blocks {
		r.analyse(b)
		out.Blocks = append(out.Blocks, b)
	}
	out.Ngspice.Runs = r.runs
	out.Artifacts = r.artifacts
	if r.keep {
		out.WorkDir = opt.WorkDir
	}
	// Findings, plan, summary.
	var changes []Change
	for _, b := range out.Blocks {
		out.Findings = append(out.Findings, b.Findings...)
		if b.Optimise != nil && b.Optimise.Status == "improved" {
			changes = append(changes, b.Optimise.Changes...)
		}
	}
	if r.bin == "" && len(blocks) > 0 {
		out.Findings = append(out.Findings, Finding{Severity: "info", Kind: "analog-ngspice-missing", Refs: []string{}, Nets: []string{},
			Message:    fmt.Sprintf("%d analog block(s) found but ngspice is not installed: only analytic checks ran", len(blocks)),
			Suggestion: "run pcbpilot sim tools install, then re-run pcbpilot sim analog"})
	}
	sortFindings(out.Findings)
	out.Plan = BuildPlan(changes)
	out.Summary = summarise(out)
	out.Definitions = &Definitions{
		Metrics:  "every metric carries its method: ngspice-op/dc/ac/tran/loop (simulated with the netlist in artifacts[]) or analytic (closed-form cross-check); analytic{} holds the closed-form values of the same block",
		Targets:  "source spec = --spec file; inferred = design nominal (no spec) or a datasheet / guideline limit; a missed spec target is FAIL, a missed inferred target WARN",
		MC:       "Monte-Carlo: each passive drawn from a normal distribution with ±tol = 3σ (truncated), tolerance from the part description / MPN code or the assumed default; yield = share of runs meeting every targeted metric",
		Plan:     "value changes are SCHEMATIC edits: show before/after to the user and wait for an explicit yes, then compile against a fresh sch list and apply; re-run sim analog / intent derive / report afterwards",
		Analytic: "closed-form formulas: Sallen-Key/MFB f0 and Q, amplifier gain and GBW-limited bandwidth, RC poles, regulator Vref·(1+Rtop/Rbot), crystal CL = C1C2/(C1+C2)+stray, ADC hold-capacitor settling",
	}
	return out, nil
}

func sortFindings(fs []Finding) {
	sev := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.SliceStable(fs, func(i, j int) bool {
		if sev[fs[i].Severity] != sev[fs[j].Severity] {
			return sev[fs[i].Severity] < sev[fs[j].Severity]
		}
		if fs[i].Block != fs[j].Block {
			return blockLess(fs[i].Block, fs[j].Block)
		}
		return fs[i].Kind < fs[j].Kind
	})
}

func blockLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func summarise(out *Output) Summary {
	s := Summary{Blocks: len(out.Blocks), ByClass: map[string]int{}, Status: StatusPass}
	if len(out.Blocks) == 0 {
		s.Status = StatusInfo
	}
	for _, b := range out.Blocks {
		s.ByClass[b.Class]++
		if b.Simulated {
			s.Simulated++
		}
		for _, m := range b.Metrics {
			if m.Target == nil {
				continue
			}
			s.Targets++
			if m.Status == StatusPass {
				s.Met++
			} else {
				s.Failing++
			}
		}
		s.Status = worse(s.Status, b.Status)
	}
	if out.Plan != nil {
		s.Changes = len(out.Plan.Changes)
	}
	return s
}

func worse(a, b string) string {
	rank := map[string]int{StatusInfo: 0, StatusPass: 1, StatusWarn: 2, StatusFail: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// ---------------------------------------------------------------------------

func (r *runner) values(b *Block) map[string]float64 { return r.c.values(b.c.passives) }

func (r *runner) specFor(b *Block) *SpecBlock { return r.spec.forBlock(b) }

// analyse runs the per-class analyses of one block.
func (r *runner) analyse(blk *Block) {
	b := blk.c
	vals := r.values(blk)
	if b.eval != nil {
		blk.Analytic = roundMap(b.eval(vals))
	}
	r.modelRef(blk)
	targets := r.targets(blk)
	var metrics map[string]Metric
	if r.bin == "" {
		blk.Skipped = MissingNgspiceNote
		metrics = r.analyticMetrics(blk, vals)
	} else {
		var err error
		metrics, err = r.simulate(blk, vals, "")
		if err != nil {
			blk.Skipped = "ngspice: " + err.Error()
			r.addFinding(blk, "warn", "analog-sim-failed", "ngspice run failed: "+err.Error(), "check the netlist in the work dir (--work-dir); analytic values are reported instead")
			metrics = r.analyticMetrics(blk, vals)
		} else {
			blk.Simulated = true
		}
	}
	for name, m := range metrics {
		if a, ok := blk.Analytic[name]; ok && m.Method != "analytic" {
			av := a
			m.Analytic = &av
		}
		metrics[name] = m
	}
	r.applyTargets(blk, metrics, targets)
	// Tolerance.
	if b.mc && len(b.passives) > 0 {
		nom := map[string]float64{}
		for _, m := range blk.Metrics {
			nom[m.Name] = m.Value
		}
		blk.Tolerance = r.monteCarlo(blk, vals, targets, nom, "")
	}
	r.findings(blk)
	if r.opt.Optimise {
		r.optimise(blk, targets)
	}
	sortCurves(blk.Curves)
	blk.Status = StatusPass
	for _, m := range blk.Metrics {
		if m.Status == StatusFail {
			blk.Status = worse(blk.Status, StatusFail)
		} else if m.Status == StatusWarn {
			blk.Status = worse(blk.Status, StatusWarn)
		}
	}
	for _, f := range blk.Findings {
		switch f.Severity {
		case "error":
			blk.Status = worse(blk.Status, StatusFail)
		case "warn":
			blk.Status = worse(blk.Status, StatusWarn)
		}
	}
}

func roundMap(m map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range m {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			out[k] = round6(v)
		}
	}
	return out
}

func (r *runner) modelRef(blk *Block) {
	b := blk.c
	if blk.Model != nil {
		return
	}
	switch {
	case len(b.opamps) > 0:
		o := b.opamps[0]
		m := o.Model
		mr := &ModelRef{Ref: o.Ref, ID: nz(m.ID, "generic-opamp"), Kind: "opamp", Confidence: m.Confidence, Source: m.Source,
			Params: map[string]float64{"gbwHz": m.GBWHz, "slewVPerUs": m.SlewVPerUs, "aolDB": m.AolDB, "vosV": m.VosV, "p2Hz": m.P2Hz,
				"headroomHighV": m.HeadroomHighV, "headroomLowV": m.HeadroomLowV, "zoutOhm": m.ZoutOhm, "ioutMaxA": m.IoutMaxA}}
		if o.Comparator {
			mr.Kind = "comparator"
			mr.Params = map[string]float64{"vosV": m.VosV, "tpdS": m.TpdS, "headroomHighV": m.HeadroomHighV, "headroomLowV": m.HeadroomLowV}
		}
		if o.Vendor != nil {
			mr.Confidence, mr.Vendor = "vendor", o.Vendor.File
		}
		if !o.FromLibrary {
			r.addFinding(blk, "info", "analog-assumed-model", fmt.Sprintf("%s has no entry in analog-models.json — generic %s model (assumed)", o.Ref, mr.Kind),
				"add the part (GBW, slew, Vos, headroom, pin-out) to spice-models/analog-models.json or drop its vendor model into spice-models/vendor/")
		}
		if o.PinoutFromLib {
			blk.Notes = append(blk.Notes, o.Ref+": numeric pin names — roles taken from the library pin-out (verify against the symbol)")
		}
		blk.Model = mr
	case len(b.amps) > 0:
		a := b.amps[0]
		blk.Model = &ModelRef{Ref: a.Ref, ID: a.Model.ID, Kind: a.Model.Kind, Confidence: nz(a.Model.Confidence, "approx"), Source: a.Model.Source,
			Params: map[string]float64{"gain": a.Gain, "bwHz": a.Model.BWHz}}
	case len(b.refs) > 0:
		m := b.refs[0].Model
		blk.Model = &ModelRef{Ref: b.refs[0].Ref, ID: m.ID, Kind: "reference", Confidence: nz(m.Confidence, "approx"), Source: m.Source,
			Params: map[string]float64{"vzV": m.VzV, "tolPct": m.TolPct, "ikMinA": m.IkMinA, "ikMaxA": m.IkMaxA}}
	case len(b.bjts) > 0:
		q := b.bjts[0]
		p := map[string]float64{"is": q.IS, "bf": q.BF, "br": q.BR}
		src := q.Source
		if q.Sw != nil {
			p["hfeMin"], p["icMaxA"], p["vceSatV"] = q.Sw.HfeMin, q.Sw.IcMaxA, q.Sw.VceSatV
			src += "; " + q.Sw.Source
		}
		blk.Model = &ModelRef{Ref: q.Ref, ID: nz(q.ModelID, "generic-npn"), Kind: "bjt", Confidence: q.Confidence, Source: src, Params: p}
	case len(b.fets) > 0:
		f := b.fets[0]
		blk.Model = &ModelRef{Ref: f.Ref, ID: nz(f.Model.ID, "generic-nmos"), Kind: "mosfet", Confidence: nz(f.Model.Confidence, "assumed"), Source: f.Model.Source,
			Params: map[string]float64{"vthV": f.Model.VthV, "vthMaxV": f.Model.VthMaxV, "rdsonOhm": f.Model.RdsonOhm}}
	}
}

func (r *runner) addFinding(blk *Block, sev, kind, msg, sugg string) {
	for _, f := range blk.Findings {
		if f.Kind == kind && f.Message == msg {
			return
		}
	}
	refs := append([]string{}, blk.Parts...)
	nets := []string{}
	if blk.Output != "" {
		nets = append(nets, blk.Output)
	}
	blk.Findings = append(blk.Findings, Finding{Severity: sev, Kind: kind, Block: blk.ID, Message: blk.ID + " " + blk.Core + ": " + msg, Refs: refs, Nets: nets, Suggestion: sugg})
}

// ---------------------------------------------------------------------------
// Targets.

type targetSet map[string]*Target

func fptr(v float64) *float64 { return &v }

func (r *runner) targets(blk *Block) targetSet {
	ts := targetSet{}
	b := blk.c
	d := r.lib.Defaults
	tol := func(metric string) float64 {
		if t := d.TargetTolPct[metric]; t > 0 {
			return t
		}
		return 5
	}
	nominal := func(metric string) {
		if v, ok := blk.Analytic[metric]; ok && v != 0 {
			ts[metric] = &Target{Metric: metric, Value: fptr(v), TolPct: tol(metric), Source: "inferred", Why: "design nominal (no spec target)"}
		}
	}
	pm := d.PhaseMarginMinDeg
	if r.spec != nil && r.spec.PhaseMarginMinDeg > 0 {
		pm = r.spec.PhaseMarginMinDeg
	}
	switch blk.Class {
	case ClassSKLowPass, ClassSKHighPass, ClassMFBLowPass, ClassMFBHighPass:
		nominal("fcHz")
		nominal("q")
		nominal("gain")
	case ClassNonInverting, ClassInverting, ClassDifference, ClassFollower, ClassInstrument, ClassCurrentSense:
		nominal("gain")
		if b.resp == "lp" {
			nominal("fcHz")
		}
	case ClassRCLowPass:
		nominal("fcHz")
	case ClassRegulatorFB:
		if v, ok := voltFromName(blk.Output); ok && v > 0 {
			ts["voutV"] = &Target{Metric: "voutV", Value: fptr(v), TolPct: tol("voutV"), Source: "inferred", Why: "rail name " + blk.Output}
		} else if v, ok := r.c.railV[blk.Output]; ok {
			ts["voutV"] = &Target{Metric: "voutV", Value: fptr(v), TolPct: tol("voutV"), Source: "inferred", Why: "rail voltage"}
		}
	case ClassCrystal:
		x := b.xtal
		if r.spec != nil {
			if sc, ok := r.spec.Crystals[strings.Split(blk.Core, ".")[0]]; ok {
				if sc.CLPF > 0 {
					x.CLPF = sc.CLPF
				}
				if sc.FHz > 0 {
					x.FHz = sc.FHz
				}
			}
		}
		if x.CLPF > 0 {
			ts["clPF"] = &Target{Metric: "clPF", Value: fptr(x.CLPF), TolPct: tol("clPF"), Source: "datasheet", Why: "crystal load capacitance"}
		}
	case ClassReference:
		m := b.refs[0].Model
		if m.IkMinA > 0 {
			ts["ikA"] = &Target{Metric: "ikA", Min: fptr(m.IkMinA), Source: "datasheet", Why: "minimum operating (cathode) current"}
			if m.IkMaxA > 0 {
				ts["ikA"].Max = fptr(m.IkMaxA)
			}
		}
	case ClassResetRC:
		if blk.Model != nil && blk.Model.Params["minDelayS"] > 0 {
			ts["delayS"] = &Target{Metric: "delayS", Min: fptr(blk.Model.Params["minDelayS"]), Source: "datasheet", Why: "reset/enable must rise after the rail is stable"}
		}
	case ClassTransistorSw:
		if len(b.bjts) > 0 && b.roleRef["Rload"] != "" {
			ts["overdrive"] = &Target{Metric: "overdrive", Min: fptr(2), Source: "inferred", Why: "saturation margin: Ib·hFE(min) ≥ 2·Ic"}
		}
		if len(b.bjts) > 0 && b.bjts[0].Sw != nil && b.bjts[0].Sw.IcMaxA > 0 {
			ts["icPeakA"] = &Target{Metric: "icPeakA", Max: fptr(b.bjts[0].Sw.IcMaxA), Source: "inferred", Why: "continuous IC rating of the datasheet (a µs capacitor-discharge pulse is judged against it as a warning)"}
		}
	case ClassLevelShifter:
		if len(b.fets) > 0 {
			ts["vgsOnV"] = &Target{Metric: "vgsOnV", Min: fptr(b.fets[0].Model.VthMaxV + 0.5), Source: "datasheet", Why: "gate drive above VGS(th) max + 0.5 V"}
		}
	case ClassLCFilter:
		ts["peakingDB"] = &Target{Metric: "peakingDB", Max: fptr(6), Source: "inferred", Why: "undamped LC resonance amplifies ripple / load steps"}
	case ClassADCInput:
		ts["settleErrorLsb"] = &Target{Metric: "settleErrorLsb", Max: fptr(0.5), Source: "inferred", Why: "hold capacitor must settle to ½ LSB within the sampling time"}
	}
	if len(b.opamps) > 0 && !b.opamps[0].Comparator && b.loop {
		ts["phaseMarginDeg"] = &Target{Metric: "phaseMarginDeg", Min: fptr(pm), Source: "inferred", Why: "stability guideline"}
	}
	if b.adc != nil && blk.Class != ClassADCInput {
		ts["settleErrorLsb"] = &Target{Metric: "settleErrorLsb", Max: fptr(0.5), Source: "inferred", Why: "ADC hold capacitor settling"}
		if b.adc.VrefV > 0 && len(b.opamps) > 0 {
			ts["outMaxV"] = &Target{Metric: "outMaxV", Min: fptr(b.adc.VrefV * 0.98), Source: "inferred", Why: fmt.Sprintf("reach the ADC full scale (%s = %.3g V)", b.adc.VrefSrc, b.adc.VrefV)}
		}
	}
	// Spec overrides.
	if sb := r.specFor(blk); sb != nil {
		for m, v := range sb.Targets {
			t := &Target{Metric: m, Value: fptr(v), TolPct: tol(m), Source: "spec"}
			if tp, ok := sb.TolPct[m]; ok {
				t.TolPct = tp
			}
			ts[m] = t
		}
		for m, v := range sb.Min {
			t := ts[m]
			if t == nil || t.Source != "spec" {
				t = &Target{Metric: m, Source: "spec"}
				ts[m] = t
			}
			t.Min = fptr(v)
		}
		for m, v := range sb.Max {
			t := ts[m]
			if t == nil || t.Source != "spec" {
				t = &Target{Metric: m, Source: "spec"}
				ts[m] = t
			}
			t.Max = fptr(v)
		}
	}
	return ts
}

// within reports whether v meets t, and the normalised error (0 = centre, 1 = edge).
func (t *Target) within(v float64) (bool, float64) {
	if t.Value != nil {
		tol := t.TolPct
		if tol <= 0 {
			tol = 5
		}
		e := math.Abs(v-*t.Value) / math.Abs(*t.Value) * 100 / tol
		return e <= 1, e
	}
	ok := true
	e := 0.0
	if t.Min != nil && v < *t.Min {
		ok = false
		e = 1 + (*t.Min-v)/math.Max(math.Abs(*t.Min), 1e-12)*10
	}
	if t.Max != nil && v > *t.Max {
		ok = false
		e = 1 + (v-*t.Max)/math.Max(math.Abs(*t.Max), 1e-12)*10
	}
	return ok, e
}

func (r *runner) applyTargets(blk *Block, metrics map[string]Metric, ts targetSet) {
	names := sortedKeys(metrics)
	order := []string{"gain", "gainDB", "fcHz", "f0Hz", "q", "peakingDB", "phaseMarginDeg", "crossoverHz", "gainMarginDB", "overshootPct", "riseTimeS", "settlingTimeS"}
	rank := map[string]int{}
	for i, n := range order {
		rank[n] = i + 1
	}
	sort.SliceStable(names, func(i, j int) bool {
		ri, rj := rank[names[i]], rank[names[j]]
		if ri == 0 {
			ri = 100
		}
		if rj == 0 {
			rj = 100
		}
		if ri != rj {
			return ri < rj
		}
		return names[i] < names[j]
	})
	blk.Metrics = nil
	for _, n := range names {
		m := metrics[n]
		m.Value = round6(m.Value)
		if t, ok := ts[n]; ok {
			m.Target = t
			if in, _ := t.within(m.Value); in {
				m.Status = StatusPass
			} else if t.Source == "spec" || t.Source == "datasheet" {
				m.Status = StatusFail
			} else {
				m.Status = StatusWarn
			}
		}
		blk.Metrics = append(blk.Metrics, m)
	}
	// Targets without a metric are reported as not measured.
	for _, n := range sortedKeys(ts) {
		if _, ok := metrics[n]; !ok {
			blk.Notes = append(blk.Notes, fmt.Sprintf("target %s not measured in this run", n))
		}
	}
}

func (blk *Block) metric(name string) (Metric, bool) {
	for _, m := range blk.Metrics {
		if m.Name == name {
			return m, true
		}
	}
	return Metric{}, false
}

var metricLabels = map[string][2]string{
	"gain": {"增益", "V/V"}, "gainDB": {"增益", "dB"}, "fcHz": {"-3 dB 频率", "Hz"}, "f0Hz": {"自然频率 f0", "Hz"}, "q": {"品质因数 Q", ""},
	"peakingDB": {"谐振峰", "dB"}, "phaseMarginDeg": {"相位裕度", "°"}, "crossoverHz": {"环路穿越频率", "Hz"}, "gainMarginDB": {"增益裕度", "dB"},
	"overshootPct": {"阶跃过冲", "%"}, "riseTimeS": {"上升时间 10–90%", "s"}, "settlingTimeS": {"建立时间 2%", "s"},
	"outMaxV": {"输出上摆极限", "V"}, "outMinV": {"输出下摆极限", "V"}, "biasInV": {"直流偏置输入", "V"}, "outDcV": {"输出直流", "V"},
	"vcmV": {"输入共模", "V"}, "ioutPeakA": {"输出峰值电流", "A"}, "settleErrorLsb": {"ADC 采样建立误差", "LSB"}, "firstSampleErrorLsb": {"首次采样误差", "LSB"}, "vdsOnV": {"导通 VDS", "V"}, "sourceOhm": {"ADC 源阻抗", "Ω"},
	"rsMaxOhm": {"允许最大源阻抗", "Ω"}, "voutV": {"输出电压", "V"}, "ikA": {"基准阴极电流", "A"}, "clPF": {"有效负载电容 CL", "pF"},
	"freqErrorPpm": {"频率偏差", "ppm"}, "delayS": {"复位释放延时", "s"}, "tauS": {"RC 时间常数", "s"}, "ibA": {"基极电流", "A"}, "icA": {"集电极电流", "A"},
	"forcedBeta": {"强制 β", ""}, "overdrive": {"饱和过驱倍数", "×"}, "vceSatV": {"导通压降", "V"}, "icPeakA": {"集电极峰值电流", "A"},
	"fallTimeS": {"拉低时间", "s"}, "releaseTimeS": {"释放到 VIH", "s"}, "vgsOnV": {"导通 VGS", "V"}, "thresholdRiseV": {"上升阈值", "V"},
	"thresholdFallV": {"下降阈值", "V"}, "hysteresisV": {"回差", "V"}, "thresholdLowOutV": {"输出低时阈值", "V"}, "thresholdHighOutV": {"输出高时阈值", "V"},
	"pullPpm": {"频率牵引", "ppm"}, "chargeShareLsb": {"电荷分享误差", "LSB"}, "rcExtS": {"外部 RC", "s"}, "cextF": {"引脚外接电容", "F"},
	"inputPoleHz": {"输入 RC 极点", "Hz"}, "feedbackPoleHz": {"反馈极点", "Hz"}, "unityHz": {"单位增益频率", "Hz"}, "dividerCurrentA": {"分压器电流", "A"},
	"ffZeroHz": {"前馈零点", "Hz"}, "transimpedanceVperA": {"跨阻", "V/A"}, "voutAtImaxV": {"最大电流时输出", "V"}, "loadA": {"基准负载电流", "A"},
	"pdW": {"基准功耗", "W"}, "ratioMismatchPct": {"电阻比失配", "%"}, "loopGainDcDB": {"直流环路增益", "dB"}, "finalV": {"终值", "V"},
}

func newMetric(name string, v float64, method string) Metric {
	l := metricLabels[name]
	label := l[0]
	if label == "" {
		label = name
	}
	return Metric{Name: name, Label: label, Unit: l[1], Value: v, Method: method}
}

// analyticMetrics is the no-ngspice fallback.
func (r *runner) analyticMetrics(blk *Block, vals map[string]float64) map[string]Metric {
	out := map[string]Metric{}
	if blk.c.eval == nil {
		return out
	}
	for k, v := range blk.c.eval(vals) {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		out[k] = newMetric(k, v, "analytic")
	}
	return out
}

// ---------------------------------------------------------------------------
// Monte-Carlo.

func (r *runner) mcSets(blk *Block, vals map[string]float64, n int) ([]map[string]float64, []string) {
	rng := rand.New(rand.NewSource(int64(r.opt.Seed) + int64(len(blk.ID))*7919 + int64(blk.ID[len(blk.ID)-1])))
	var refs []string
	tol := map[string]float64{}
	for _, comp := range blk.Components {
		refs = append(refs, comp.Ref)
		tol[comp.Ref] = comp.TolPct
	}
	sort.Strings(refs)
	sets := make([]map[string]float64, n)
	for i := 0; i < n; i++ {
		set := map[string]float64{}
		for _, ref := range refs {
			sigma := tol[ref] / 100 / 3
			z := rng.NormFloat64()
			if z > 3 {
				z = 3
			}
			if z < -3 {
				z = -3
			}
			base := vals[ref]
			set[ref] = base * (1 + z*sigma)
		}
		sets[i] = set
	}
	return sets, refs
}

func (r *runner) monteCarlo(blk *Block, vals map[string]float64, ts targetSet, nominal map[string]float64, tag string) *MCResult {
	b := blk.c
	n := r.opt.MCRuns
	if n <= 0 {
		return nil
	}
	sets, refs := r.mcSets(blk, vals, n)
	res := &MCResult{Runs: n, Seed: r.opt.Seed, Dist: "normal, ±tol = 3σ (truncated)", Stats: map[string]MCStats{}, Varied: refs}
	per := make([]map[string]float64, 0, n)
	switch {
	case r.bin != "" && b.ac && b.stim != "" && blk.Simulated && len(b.opamps)+len(b.amps) > 0 || r.bin != "" && blk.Class == ClassRCLowPass && blk.Simulated:
		res.Method = "ngspice-ac-mc"
		s := r.acSetup(blk, vals)
		s.mode, s.mcSets, s.mcAC, s.bias = modeMC, withBase(sets, vals), true, b.stimBias
		s.ptsDec = 40
		sim, err := r.run(blk, s, tag)
		if err != nil {
			res.Method = "analytic-mc"
			break
		}
		second := isSecondOrder(blk.Class)
		for i := range sets {
			t := sim.data[fmt.Sprintf("mc%03d", i)]
			if t == nil {
				continue
			}
			per = append(per, acMetrics(bodeFrom(t, false), b.resp, second))
		}
	case r.bin != "" && (blk.Class == ClassRegulatorFB || blk.Class == ClassReference) && blk.Simulated:
		res.Method = "ngspice-op-mc"
		s := spiceSetup{mode: modeMC, mcSets: withBase(sets, vals), vals: vals}
		sim, err := r.run(blk, s, tag)
		if err != nil {
			res.Method = "analytic-mc"
			break
		}
		for _, op := range sim.ops {
			per = append(per, r.opMetrics(blk, op))
		}
	default:
		res.Method = "analytic-mc"
	}
	if res.Method == "analytic-mc" {
		if b.eval == nil {
			return nil
		}
		per = per[:0]
		for _, set := range sets {
			v := map[string]float64{}
			for k, x := range vals {
				v[k] = x
			}
			for k, x := range set {
				v[k] = x
			}
			per = append(per, b.eval(v))
		}
	}
	if len(per) == 0 {
		return nil
	}
	names := b.mcMetrics
	if len(names) == 0 {
		for k := range per[0] {
			names = append(names, k)
		}
		sort.Strings(names)
	}
	okRuns := 0
	targeted := 0
	for _, name := range names {
		var xs []float64
		for _, p := range per {
			if v, ok := p[name]; ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
				xs = append(xs, v)
			}
		}
		if len(xs) == 0 {
			continue
		}
		st := stats(xs)
		st.Nominal = round6(nominal[name])
		if t, ok := ts[name]; ok {
			in := 0
			for _, x := range xs {
				if ok, _ := t.within(x); ok {
					in++
				}
			}
			st.InSpec = fptr(round6(float64(in) / float64(len(xs)) * 100))
		}
		res.Stats[name] = st
	}
	for _, p := range per {
		all := true
		any := false
		for _, name := range names {
			t, ok := ts[name]
			if !ok {
				continue
			}
			v, ok := p[name]
			if !ok {
				continue
			}
			any = true
			if in, _ := t.within(v); !in {
				all = false
			}
		}
		if any {
			targeted++
			if all {
				okRuns++
			}
		}
	}
	if targeted > 0 {
		res.YieldPc = fptr(round6(float64(okRuns) / float64(targeted) * 100))
	}
	return res
}

// withBase fills unchanged refs so every alter set is complete.
func withBase(sets []map[string]float64, vals map[string]float64) []map[string]float64 {
	out := make([]map[string]float64, len(sets))
	for i, s := range sets {
		m := map[string]float64{}
		for k, v := range vals {
			m[k] = v
		}
		for k, v := range s {
			m[k] = v
		}
		out[i] = m
	}
	return out
}

func stats(xs []float64) MCStats {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	mean := 0.0
	for _, x := range s {
		mean += x
	}
	mean /= float64(len(s))
	v := 0.0
	for _, x := range s {
		v += (x - mean) * (x - mean)
	}
	std := 0.0
	if len(s) > 1 {
		std = math.Sqrt(v / float64(len(s)-1))
	}
	pct := func(p float64) float64 {
		i := int(math.Round(p / 100 * float64(len(s)-1)))
		return s[i]
	}
	return MCStats{Min: round6(s[0]), Max: round6(s[len(s)-1]), Mean: round6(mean), Std: round6(std), P1: round6(pct(1)), P99: round6(pct(99))}
}

func isSecondOrder(class string) bool {
	switch class {
	case ClassSKLowPass, ClassSKHighPass, ClassMFBLowPass, ClassMFBHighPass, ClassLCFilter:
		return true
	}
	return false
}
