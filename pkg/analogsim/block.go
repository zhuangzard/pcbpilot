package analogsim

import (
	"math"
	"sort"
)

// blockCtx is the simulation recipe of one block (not serialised).
type blockCtx struct {
	passives  []string          // refs in the netlist
	roles     map[string]string // ref → role
	roleRef   map[string]string // role → ref
	opamps    []*opampInst
	amps      []*ampInst
	refs      []*refInst
	bjts      []*bjtInst
	fets      []*fetInst
	xtal      *xtalInst
	rails     map[string]float64
	stim      string  // AC/step stimulus net ("" = none)
	stim2     string  // second (anti-phase) stimulus for difference inputs
	stimBias  float64 // DC bias of the stimulus (set by the DC sweep)
	stimSet   bool
	out       string
	adc       *adcLoad
	loadR     map[string]float64 // net → resistive load to ground
	loadI     map[string]float64 // net → DC current sink
	openPorts []string           // ports tied to ground through 1 GΩ
	// analyses to run.
	dcSweep, ac, loop, step, op, mc, sample, ramp, switching bool
	// kind of filter for fc measurement: lp | hp | bp | amp | "".
	resp string
	// analytic evaluator over role → value.
	eval func(v map[string]float64) map[string]float64
	// metric names the MC reports.
	mcMetrics []string
	// DC-sweep range of the stimulus.
	sweepLo, sweepHi float64
	// expected small-signal gain magnitude (for step sizing).
	gainGuess float64
	// ramp (comparator): triangle over [rampLo, rampHi].
	rampLo, rampHi float64
	// switching (transistor): base/gate drive net and level.
	driveNet  string
	driveHigh float64
	emitNet   string
	// sample (ADC): source level.
	sampleV float64
	// time scale hints.
	tauGuess float64
	// resetRC: supply rail stepped at t=0.
	supplyStep string
	vih        float64
	// regulator feedback: the error amplifier forces V(regFB) = regVref.
	regFB   string
	regVref float64
}

type ampInst struct {
	Ref                 string
	Inp, Inn, Out, RefN string
	Vp, Vn              string
	Model               AmpModel
	Gain                float64
	RgRef               string
}

type refInst struct {
	Ref   string
	K, A  string
	Model RefModel
}

type bjtInst struct {
	Ref        string
	B, C, E    string
	NPN        bool
	IS, BF, BR float64
	Sw         *BJTModel
	ModelID    string
	Confidence string
	Source     string
}

type fetInst struct {
	Ref     string
	G, D, S string
	N       bool
	Model   FETModel
}

type xtalInst struct {
	Ref        string
	A, B       string
	FHz, CLPF  float64
	C0PF, CmFF float64
	ESR        float64
}

type adcLoad struct {
	Net     string
	Model   ADCModel
	Owner   string
	Pin     string
	VrefV   float64
	VrefSrc string
}

func newBlockCtx() *blockCtx {
	return &blockCtx{roles: map[string]string{}, roleRef: map[string]string{}, rails: map[string]float64{}, loadR: map[string]float64{}, loadI: map[string]float64{}}
}

func (b *blockCtx) setRole(ref, role string) {
	if ref == "" {
		return
	}
	b.roles[ref] = role
	b.roleRef[role] = ref
}

func (b *blockCtx) addPassives(refs ...string) {
	seen := map[string]bool{}
	for _, r := range b.passives {
		seen[r] = true
	}
	for _, r := range refs {
		if r != "" && !seen[r] {
			seen[r] = true
			b.passives = append(b.passives, r)
		}
	}
	sort.Strings(b.passives)
}

// ---------------------------------------------------------------------------
// Analytic evaluators (role → value maps). They are the "first guess" of the
// optimiser and the cross-check column of the report; ngspice is the truth.

func par(a, b float64) float64 {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return a * b / (a + b)
}

func twoPi() float64 { return 2 * math.Pi }

// skLowPass: R1 (input), R2 (to +in), C1 (X → out), C2 (+in → gnd), gain K.
func skLowPass(v map[string]float64) map[string]float64 {
	r1, r2, c1, c2 := v["R1"], v["R2"], v["C1"], v["C2"]
	k := 1.0
	if v["Rf"] > 0 && v["Rg"] > 0 {
		k = 1 + v["Rf"]/v["Rg"]
	}
	w0 := 1 / math.Sqrt(r1*r2*c1*c2)
	den := c2*(r1+r2) + r1*c1*(1-k)
	q := math.Sqrt(r1*r2*c1*c2) / den
	return map[string]float64{"f0Hz": w0 / twoPi(), "q": q, "gain": k, "fcHz": fcLowPass2(w0/twoPi(), q)}
}

// skHighPass: C1 (input), C2 (to +in), R1 (X → out), R2 (+in → ref), gain K.
func skHighPass(v map[string]float64) map[string]float64 {
	r1, r2, c1, c2 := v["R1"], v["R2"], v["C1"], v["C2"]
	k := 1.0
	if v["Rf"] > 0 && v["Rg"] > 0 {
		k = 1 + v["Rf"]/v["Rg"]
	}
	w0 := 1 / math.Sqrt(r1*r2*c1*c2)
	den := r1*(c1+c2) + r2*c2*(1-k)
	q := math.Sqrt(r1*r2*c1*c2) / den
	return map[string]float64{"f0Hz": w0 / twoPi(), "q": q, "gain": k, "fcHz": fcHighPass2(w0/twoPi(), q)}
}

// mfbLowPass: R1 input, R2 X→out, R3 X→-in, C1 X→gnd, C2 -in→out.
func mfbLowPass(v map[string]float64) map[string]float64 {
	r1, r2, r3, c1, c2 := v["R1"], v["R2"], v["R3"], v["C1"], v["C2"]
	w0 := 1 / math.Sqrt(r2*r3*c1*c2)
	// ω0/Q = (1/C1)(1/R1+1/R2+1/R3) → Q = ω0·C1 / (1/R1+1/R2+1/R3)
	q := w0 * c1 / (1/r1 + 1/r2 + 1/r3)
	return map[string]float64{"f0Hz": w0 / twoPi(), "q": q, "gain": r2 / r1, "fcHz": fcLowPass2(w0/twoPi(), q)}
}

// mfbHighPass: C1 input, C2 X→out, C3 X→-in, R1 X→gnd, R2 -in→out.
func mfbHighPass(v map[string]float64) map[string]float64 {
	c1, c2, c3, r1, r2 := v["C1"], v["C2"], v["C3"], v["R1"], v["R2"]
	w0 := 1 / math.Sqrt(r1*r2*c2*c3)
	// ω0/Q = (C1+C2+C3)/(R2·C2·C3)
	q := w0 * r2 * c2 * c3 / (c1 + c2 + c3)
	return map[string]float64{"f0Hz": w0 / twoPi(), "q": q, "gain": c1 / c2, "fcHz": fcHighPass2(w0/twoPi(), q)}
}

// fcLowPass2 is the -3 dB frequency of a 2nd-order low-pass (f0, Q).
func fcLowPass2(f0, q float64) float64 {
	a := 1 - 1/(2*q*q)
	return f0 * math.Sqrt(a+math.Sqrt(a*a+1))
}

func fcHighPass2(f0, q float64) float64 {
	return f0 * f0 / fcLowPass2(f0, q)
}

// gbwBandwidth: closed-loop -3 dB of a single-pole amp with noise gain Gn.
func gbwBandwidth(gbw, noiseGain float64) float64 {
	if noiseGain < 1 {
		noiseGain = 1
	}
	return gbw / noiseGain
}
