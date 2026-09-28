package powersim

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Output is the `pcbpilot sim power` document (schemaVersion 1). Field names
// are a fixed contract consumed by the trace-width planner: add, never rename.
type Output struct {
	SchemaVersion int          `json:"schemaVersion"`
	Generator     string       `json:"generator"`
	Scenarios     []string     `json:"scenarios"`
	Results       []Result     `json:"results"`
	Inputs        *Inputs      `json:"inputs,omitempty"`
	SpiceCheck    *SpiceCheck  `json:"spiceCheck,omitempty"`
	Models        []ModelUse   `json:"models,omitempty"`
	Defs          *Definitions `json:"definitions,omitempty"`
}

// Definitions documents the sign conventions inside the document.
type Definitions struct {
	PinCurrentA string `json:"pinCurrentA"`
	Dir         string `json:"dir"`
	NetCurrentA string `json:"netCurrentA"`
	Worst       string `json:"worst"`
}

// Inputs records provenance.
type Inputs struct {
	Connectivity []string `json:"connectivity,omitempty"`
	Values       []string `json:"values,omitempty"`
	Libraries    []string `json:"libraries,omitempty"`
	Live         string   `json:"live,omitempty"`
}

// ModelUse is the model bound to one part (same for every scenario).
type ModelUse struct {
	Ref        string `json:"ref"`
	Kind       string `json:"kind"`
	ModelID    string `json:"modelId"`
	Match      string `json:"match"`
	Confidence string `json:"confidence"`
	Source     string `json:"source,omitempty"`
}

// Result is one scenario.
type Result struct {
	Scenario    string                  `json:"scenario"`
	Description string                  `json:"description,omitempty"`
	Converged   bool                    `json:"converged"`
	Iterations  int                     `json:"iterations,omitempty"`
	Nets        map[string]*NetResult   `json:"nets"`
	Parts       map[string]*PartResult  `json:"parts"`
	Ripple      map[string]*RippleEntry `json:"ripple"`
	Warnings    []string                `json:"warnings"`
	Assumptions []string                `json:"assumptions"`
	// ThermalBasis is set when parts[].thermalW and nets[].thermalCurrentA
	// are present: ThermalAverage (the scenario's own loads are already the
	// time-averaged ones) or ThermalAverageBound (a peak scenario: the
	// thermal values come from a twin solve at average loads — see
	// attachThermal). Empty in files written before v0.6.1.
	ThermalBasis string `json:"thermalBasis,omitempty"`
}

// Thermal bases of Result.ThermalBasis.
const (
	ThermalAverage      = "average"
	ThermalAverageBound = "average-bound"
)

// NetResult is one net's DC state.
type NetResult struct {
	Voltage    float64     `json:"voltage"`
	CurrentA   float64     `json:"currentA"`
	Role       string      `json:"role"`
	Pins       []PinResult `json:"pins"`
	KCLErrorA  float64     `json:"kclErrorA,omitempty"`
	Floating   bool        `json:"floating,omitempty"`
	VoltageMin *float64    `json:"voltageMin,omitempty"`
	VoltageMax *float64    `json:"voltageMax,omitempty"`
	Scenario   string      `json:"scenario,omitempty"` // worst: scenario of the max current
	// ThermalCurrentA is the RMS current bound for steady-state copper
	// heating (see Result.ThermalBasis): CurrentA in an average scenario,
	// √(I_avg·I_peak) in a peak scenario.
	ThermalCurrentA float64 `json:"thermalCurrentA,omitempty"`
}

// PinResult is the DC current through one pad.
type PinResult struct {
	Ref      string  `json:"ref"`
	Pin      string  `json:"pin"`
	Name     string  `json:"name"`
	CurrentA float64 `json:"currentA"`
	Dir      string  `json:"dir"`
	Kind     string  `json:"kind,omitempty"`     // model kind of the part
	Scenario string  `json:"scenario,omitempty"` // worst: scenario of the max
}

// PartResult is one part's operating state.
type PartResult struct {
	Model      string `json:"model"`
	ModelID    string `json:"modelId,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	MPN        string `json:"mpn,omitempty"`
	// OffBoard: the power this part draws is dissipated by an external load
	// behind it (Model.OffBoard); it is excluded from board heat.
	OffBoard bool    `json:"offBoard,omitempty"`
	PowerW   float64 `json:"powerW"`
	// ThermalW is the time-averaged dissipation for steady-state thermal
	// (see Result.ThermalBasis); PowerW stays the scenario's operating-point
	// power used for current capacity and ratings.
	ThermalW   float64  `json:"thermalW,omitempty"`
	SuppliedW  float64  `json:"suppliedW,omitempty"`
	Mode       string   `json:"mode,omitempty"`
	VinV       float64  `json:"vinV,omitempty"`
	VoutV      float64  `json:"voutV,omitempty"`
	InputA     float64  `json:"inputA,omitempty"`
	OutputA    float64  `json:"outputA,omitempty"`
	Efficiency float64  `json:"efficiency,omitempty"`
	Notes      []string `json:"notes,omitempty"`
	Scenario   string   `json:"scenario,omitempty"`
}

// RippleEntry is a switching-current estimate for a net, inductor or capacitor.
type RippleEntry struct {
	IPeakA    float64 `json:"iPeakA,omitempty"`
	IRmsA     float64 `json:"iRmsA,omitempty"`
	IAvgA     float64 `json:"iAvgA,omitempty"`
	DeltaIA   float64 `json:"deltaIA,omitempty"`
	Duty      float64 `json:"duty,omitempty"`
	Regulator string  `json:"regulator,omitempty"`
	Scenario  string  `json:"scenario,omitempty"`
}

// ModelUses lists the bound models.
func (e *Engine) ModelUses() []ModelUse {
	var out []ModelUse
	for _, b := range e.binds {
		id := b.model.ID
		if id == "" {
			id = "generic-" + b.kind
		}
		out = append(out, ModelUse{Ref: b.part.Ref, Kind: b.kind, ModelID: id, Match: b.why, Confidence: b.confidence, Source: b.model.Source})
	}
	return out
}

// staticNetRoles computes power/switch nets from the bound models.
func (e *Engine) staticNetRoles() (power, sw map[string]bool) {
	power, sw = map[string]bool{}, map[string]bool{}
	for _, b := range e.binds {
		p, m := b.part, b.model
		mark := func(pins []Pin, set map[string]bool) {
			for _, pin := range pins {
				if pin.Net != "" && !e.isGround(pin.Net) {
					set[pin.Net] = true
				}
			}
		}
		switch b.kind {
		case KindBuck:
			mark(p.pinsNamed(roleNames(m, "in", defIn...)), power)
			mark(p.pinsNamed(roleNames(m, "out", defLX...)), sw)
		case KindLDO:
			mark(p.pinsNamed(roleNames(m, "in", defIn...)), power)
			mark(p.pinsNamed(roleNames(m, "out", defLDOOut...)), power)
		case KindSource:
			for _, pin := range p.Pins {
				if e.powerLike(pin.Net) {
					power[pin.Net] = true
				}
			}
		case KindLoad:
			if len(m.SupplyPins) > 0 {
				mark(p.pinsNamed(m.SupplyPins), power)
			}
			for _, r := range m.Rails {
				mark(p.pinsNamed(r.Pins), power)
			}
		}
	}
	for net, role := range e.d.NetRole {
		if role == "power" {
			power[net] = true
		}
	}
	return power, sw
}

func (e *Engine) result(r *run) *Result {
	res := &Result{Scenario: r.sc.name, Description: r.sc.desc, Converged: r.conv && r.err == nil, Iterations: r.iters,
		Nets: map[string]*NetResult{}, Parts: map[string]*PartResult{}, Ripple: map[string]*RippleEntry{}}
	powerNets, swNets := e.staticNetRoles()
	pinName := map[PinRef]string{}
	for _, b := range e.binds {
		for _, pin := range b.part.Pins {
			pinName[PinRef{b.part.Ref, pin.Number}] = pin.Name
		}
	}
	for _, net := range e.d.Nets() {
		v, inCircuit := r.voltage(net, e)
		nr := &NetResult{Voltage: round(v, 4), Floating: !inCircuit}
		var src, snk float64
		for _, b := range e.binds {
			for _, pin := range b.part.Pins {
				if pin.Net != net {
					continue
				}
				i := r.pinI[PinRef{b.part.Ref, pin.Number}]
				if i > 0 {
					snk += i
				} else {
					src -= i
				}
				nr.Pins = append(nr.Pins, PinResult{Ref: b.part.Ref, Pin: pin.Number, Name: pin.Name, CurrentA: round(math.Abs(i), 7), Dir: dirOf(i), Kind: b.kind})
			}
		}
		nr.CurrentA = round(math.Max(src, snk), 7)
		if d := math.Abs(src - snk); d > 1e-7 {
			nr.KCLErrorA = round(d, 9)
		}
		switch {
		case e.isGround(net):
			nr.Role = "ground"
		case swNets[net]:
			nr.Role = "switch"
		case powerNets[net] || nr.CurrentA >= 0.05:
			nr.Role = "power"
		default:
			nr.Role = "signal"
		}
		res.Nets[net] = nr
	}
	// Parts.
	for _, b := range e.binds {
		if b.kind == KindIgnore {
			continue
		}
		pr := &PartResult{Model: b.kind, ModelID: b.model.ID, Confidence: b.confidence, MPN: b.part.MPN, OffBoard: e.offBoard(b)}
		pw := 0.0
		for _, pin := range b.part.Pins {
			v, _ := r.voltage(pin.Net, e)
			pw += v * r.pinI[PinRef{b.part.Ref, pin.Number}]
		}
		pr.PowerW = round(pw, 6)
		if b.kind == KindSource && pw < 0 {
			pr.SuppliedW = round(-pw, 6)
		}
		res.Parts[b.part.Ref] = pr
	}
	if r.sol != nil {
		e.regulatorResults(r, res)
		e.checks(r, res)
	}
	res.Warnings = append(append([]string{}, e.warnings...), r.warn...)
	res.Assumptions = append([]string{
		"DC operating point (MNA + Newton-Raphson): capacitors open, inductors = DCR, switching regulators averaged by power balance; not a transient simulation",
		"IC signal pins are high-impedance (no DC drive) unless a GPIO drives an LED network",
		"loads are constant-current above a knee voltage and resistive below it (unpowered rails draw nothing)",
	}, e.assumptions...)
	res.Assumptions = append(res.Assumptions, r.assume...)
	if res.Warnings == nil {
		res.Warnings = []string{}
	}
	return res
}

// dividerNote explains V(FB) regulation through a resistor divider.
func (e *Engine) dividerNote(fbNet string, vref float64) (string, float64, bool) {
	var top, bot []*binding
	var topNet string
	for _, b := range e.binds {
		if b.kind != KindResistor || len(b.part.Pins) != 2 {
			continue
		}
		p0, p1 := b.part.Pins[0], b.part.Pins[1]
		var other string
		switch fbNet {
		case p0.Net:
			other = p1.Net
		case p1.Net:
			other = p0.Net
		default:
			continue
		}
		if e.isGround(other) {
			bot = append(bot, b)
		} else {
			top = append(top, b)
			topNet = other
		}
	}
	if len(top) != 1 || len(bot) != 1 {
		return "", 0, false
	}
	rt, ok1 := partValue(top[0].part, "resistance")
	rb, ok2 := partValue(bot[0].part, "resistance")
	if !ok1 || !ok2 || rb == 0 {
		return "", 0, false
	}
	v := vref * (1 + rt/rb)
	return fmt.Sprintf("Vout=%.3g*(1+%s/%s)=%.3fV on %s (%s=%s, %s=%s)", vref, top[0].part.Ref, bot[0].part.Ref, v, topNet,
		top[0].part.Ref, fmtOhm(rt), bot[0].part.Ref, fmtOhm(rb)), v, true
}

func fmtOhm(v float64) string {
	switch {
	case v >= 1e6:
		return fmt.Sprintf("%gMΩ", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%gkΩ", v/1e3)
	}
	return fmt.Sprintf("%gΩ", v)
}

func (e *Engine) regulatorResults(r *run, res *Result) {
	x := r.sol.X
	for _, ref := range sortedKeys(r.regs) {
		reg := r.regs[ref]
		b := e.byRef[ref]
		pr := res.Parts[ref]
		vin := volt(x, reg.t[0].Node) - volt(x, reg.t[2].Node)
		vo := volt(x, reg.t[1].Node) - volt(x, reg.t[2].Node)
		iout, iin := reg.outCurrent(x), reg.inCurrent(x)
		pr.Mode, pr.VinV, pr.VoutV = reg.mode, round(vin, 4), round(vo, 4)
		pr.InputA, pr.OutputA = round(iin, 7), round(iout, 7)
		if vin*iin > 1e-12 {
			pr.Efficiency = round(vo*iout/(vin*iin), 4)
		}
		if reg.mode != modeOn {
			r.warnf("%s: regulator %s (Vin=%.2f V)", ref, reg.mode, vin)
		}
		fbNet := ""
		for _, pin := range b.part.Pins {
			if pin.Net != "" && e.node(r.c, pin.Net) == reg.t[3].Node && !e.isGround(pin.Net) {
				fbNet = pin.Net
			}
		}
		if note, _, ok := e.dividerNote(fbNet, reg.vreg); ok && reg.buck {
			pr.Notes = append(pr.Notes, note)
		} else if reg.buck {
			pr.Notes = append(pr.Notes, fmt.Sprintf("V(%s)=%.3g V regulated", fbNet, reg.vreg))
		} else {
			pr.Notes = append(pr.Notes, fmt.Sprintf("Vout regulated to %.3g V; Iin=Iout+Iq", reg.vreg))
		}
		if !reg.buck || reg.mode == modeOff {
			continue
		}
		// Find the inductor on the switch node and the output rail behind it.
		lxNet := ""
		for _, pin := range b.part.Pins {
			if pin.Net != "" && e.node(r.c, pin.Net) == reg.t[1].Node {
				lxNet = pin.Net
			}
		}
		var ind *binding
		outNet := ""
		for _, ib := range e.binds {
			if ib.kind != KindInductor || len(ib.part.Pins) != 2 {
				continue
			}
			if ib.part.Pins[0].Net == lxNet {
				ind, outNet = ib, ib.part.Pins[1].Net
			} else if ib.part.Pins[1].Net == lxNet {
				ind, outNet = ib, ib.part.Pins[0].Net
			}
		}
		vout := vo
		if outNet != "" {
			if v, ok := r.voltage(outNet, e); ok {
				vout = v
			}
		}
		pr.Notes = append(pr.Notes, fmt.Sprintf("Iin=Vout·Iout/(η·Vin)+Iq=%.3f·%.3f/(%.2f·%.3f)+%.2gA=%.4fA", vo, iout, reg.eta, vin, reg.iq, iin))
		fsw := b.model.FswHz
		if fsw == 0 {
			fsw = e.def.BuckFswHz
			r.assumef("%s: switching frequency %.3g MHz assumed", ref, fsw/1e6)
		}
		if ind == nil || vin <= 0 {
			r.warnf("%s: no inductor found on switch net %s — ripple not estimated", ref, lxNet)
			continue
		}
		lval, ok := partValue(ind.part, "inductance")
		if !ok || lval <= 0 {
			r.warnf("%s: inductance %q not parseable — ripple not estimated", ind.part.Ref, ind.part.Value)
			continue
		}
		d := math.Min(1, vout/(reg.eta*vin))
		dI := (vin - vout) * d / (lval * fsw)
		if dI < 0 {
			dI = 0
		}
		ipk := iout + dI/2
		irms := math.Sqrt(iout*iout + dI*dI/12)
		pr.Notes = append(pr.Notes, fmt.Sprintf("D=Vout/(η·Vin)=%.3f; ΔI=(Vin−Vout)·D/(L·fsw)=(%.3f−%.3f)·%.3f/(%s·%.3gMHz)=%.3fA; Ipk=%.3fA Irms=%.3fA",
			d, vin, vout, d, fmtHenry(lval), fsw/1e6, dI, ipk, irms))
		if b.model.MaxA > 0 && ipk > b.model.MaxA*1.5 {
			r.warnf("%s: switch peak current %.2f A well above rated %.2g A", ref, ipk, b.model.MaxA)
		}
		if ind.model.MaxA > 0 && ipk > ind.model.MaxA {
			r.warnf("%s: switch-node peak %.3f A exceeds the inductor rating %.3g A", ind.part.Ref, ipk, ind.model.MaxA)
		}
		entry := &RippleEntry{IPeakA: round(ipk, 5), IRmsA: round(irms, 5), IAvgA: round(iout, 5), DeltaIA: round(dI, 5), Duty: round(d, 4), Regulator: ref}
		res.Ripple[lxNet] = entry
		cp := *entry
		res.Ripple[ind.part.Ref] = &cp
		inNet := ""
		for _, pin := range b.part.Pins {
			if pin.Net != "" && e.node(r.c, pin.Net) == reg.t[0].Node {
				inNet = pin.Net
			}
		}
		icin := iout * math.Sqrt(d*(1-d))
		icout := dI / (2 * math.Sqrt(3))
		for _, cb := range e.binds {
			if cb.kind != KindCapacitor || len(cb.part.Pins) != 2 {
				continue
			}
			n0, n1 := cb.part.Pins[0].Net, cb.part.Pins[1].Net
			hasGnd := e.isGround(n0) || e.isGround(n1)
			if !hasGnd {
				continue
			}
			switch {
			case n0 == inNet || n1 == inNet:
				res.Ripple[cb.part.Ref] = &RippleEntry{IRmsA: round(icin, 5), Regulator: ref}
			case outNet != "" && (n0 == outNet || n1 == outNet):
				res.Ripple[cb.part.Ref] = &RippleEntry{IRmsA: round(icout, 5), Regulator: ref}
			}
		}
		pr.Notes = append(pr.Notes, fmt.Sprintf("input cap Irms=Iout·√(D(1−D))=%.3fA (full value to each cap on %s); output cap Irms=ΔI/(2√3)=%.4fA (each cap on %s)", icin, inNet, icout, outNet))
		inPin := "IN"
		if len(reg.t[0].Pins) > 0 {
			inPin = reg.t[0].Pins[0].Ref + "." + pinNameOf(b.part, reg.t[0].Pins[0].Pin)
		}
		pr.Notes = append(pr.Notes, fmt.Sprintf("%s (input pin) pulsed current Irms≈Iout·√D=%.3fA, peak %.3fA", inPin, iout*math.Sqrt(d), ipk))
	}
}

func pinNameOf(p *Part, number string) string {
	for _, pin := range p.Pins {
		if pin.Number == number {
			return pin.Name
		}
	}
	return number
}

func fmtHenry(v float64) string {
	switch {
	case v < 1e-6:
		return fmt.Sprintf("%gnH", round(v*1e9, 4))
	case v < 1e-3:
		return fmt.Sprintf("%gµH", round(v*1e6, 4))
	}
	return fmt.Sprintf("%gmH", round(v*1e3, 4))
}

// checks adds rating and voltage sanity warnings.
func (e *Engine) checks(r *run, res *Result) {
	for _, b := range e.binds {
		m := b.model
		var into float64
		for _, pin := range b.part.Pins {
			if i := r.pinI[PinRef{b.part.Ref, pin.Number}]; i > 0 {
				into += i
			}
		}
		if m.MaxA > 0 && b.kind != KindBuck && b.kind != KindLDO && into > m.MaxA {
			r.warnf("%s: %.3f A exceeds rated %.3g A (%s)", b.part.Ref, into, m.MaxA, m.ID)
		}
		if reg, ok := r.regs[b.part.Ref]; ok && m.MaxA > 0 {
			if io := reg.outCurrent(r.sol.X); io > m.MaxA {
				r.warnf("%s: output %.3f A exceeds rated %.3g A", b.part.Ref, io, m.MaxA)
			}
		}
	}
	for _, el := range r.c.elems {
		rs, ok := el.(*resistor)
		if !ok || !strings.Contains(r.extra[el], "GPIO") {
			continue
		}
		b := r.owner[el]
		i := math.Abs(rs.currents(r.sol.X)[0])
		lim := b.model.GpioMaxA
		if lim == 0 {
			lim = e.def.GpioMaxA
		}
		if i > lim {
			r.warnf("%s: %.1f mA exceeds the GPIO drive limit %.0f mA", r.extra[el], i*1e3, lim*1e3)
		}
	}
	for net, nr := range res.Nets {
		if nr.Role != "power" || nr.Floating {
			continue
		}
		if vn := pcbauto.InferVoltage(net); vn > 0 && nr.CurrentA > 1e-3 {
			if math.Abs(nr.Voltage-vn)/vn > 0.1 {
				r.warnf("%s: %.3f V deviates more than 10%% from the %.3g V implied by its name", net, nr.Voltage, vn)
			}
		}
	}
}

// worst folds scenarios: per-pin max current (with its scenario), per-net max
// current, min/max voltage, per-part max |power|, per-field max ripple.
func worst(results []*Result) *Result {
	w := &Result{Scenario: "worst", Description: "per-pin maximum over " + scenarioList(results) + " (KCL does not hold across pins of different scenarios)",
		Converged: true, Nets: map[string]*NetResult{}, Parts: map[string]*PartResult{}, Ripple: map[string]*RippleEntry{}}
	w.ThermalBasis = ThermalAverage
	for _, res := range results {
		switch res.ThermalBasis {
		case "":
			w.ThermalBasis = ""
		case ThermalAverageBound:
			if w.ThermalBasis != "" {
				w.ThermalBasis = ThermalAverageBound
			}
		}
	}
	seenW, seenA := map[string]bool{}, map[string]bool{}
	for _, res := range results {
		w.Converged = w.Converged && res.Converged
		w.Iterations += res.Iterations
		for _, s := range res.Warnings {
			if !seenW[s] {
				seenW[s] = true
				w.Warnings = append(w.Warnings, s)
			}
		}
		for _, s := range res.Assumptions {
			if !seenA[s] {
				seenA[s] = true
				w.Assumptions = append(w.Assumptions, s)
			}
		}
		for net, nr := range res.Nets {
			wn := w.Nets[net]
			if wn == nil {
				cp := *nr
				cp.Pins = make([]PinResult, len(nr.Pins))
				copy(cp.Pins, nr.Pins)
				for i := range cp.Pins {
					cp.Pins[i].Scenario = res.Scenario
				}
				cp.Scenario = res.Scenario
				vmin, vmax := nr.Voltage, nr.Voltage
				cp.VoltageMin, cp.VoltageMax = &vmin, &vmax
				cp.KCLErrorA = 0
				w.Nets[net] = &cp
				continue
			}
			if nr.CurrentA > wn.CurrentA {
				wn.CurrentA, wn.Voltage, wn.Scenario = nr.CurrentA, nr.Voltage, res.Scenario
			}
			wn.ThermalCurrentA = math.Max(wn.ThermalCurrentA, nr.ThermalCurrentA)
			if !nr.Floating {
				if wn.Floating {
					*wn.VoltageMin, *wn.VoltageMax, wn.Floating = nr.Voltage, nr.Voltage, false
				}
				*wn.VoltageMin = math.Min(*wn.VoltageMin, nr.Voltage)
				*wn.VoltageMax = math.Max(*wn.VoltageMax, nr.Voltage)
			}
			if rank(nr.Role) > rank(wn.Role) {
				wn.Role = nr.Role
			}
			for i, p := range nr.Pins {
				if i < len(wn.Pins) && wn.Pins[i].Ref == p.Ref && wn.Pins[i].Pin == p.Pin && p.CurrentA > wn.Pins[i].CurrentA {
					wn.Pins[i].CurrentA, wn.Pins[i].Dir, wn.Pins[i].Scenario = p.CurrentA, p.Dir, res.Scenario
				}
			}
		}
		for ref, pr := range res.Parts {
			wp := w.Parts[ref]
			if wp == nil || math.Abs(pr.PowerW) > math.Abs(wp.PowerW) {
				cp := *pr
				cp.Scenario = res.Scenario
				if wp != nil {
					cp.ThermalW = math.Max(cp.ThermalW, wp.ThermalW)
				}
				w.Parts[ref] = &cp
			} else {
				wp.ThermalW = math.Max(wp.ThermalW, pr.ThermalW)
			}
		}
		for k, re := range res.Ripple {
			wr := w.Ripple[k]
			if wr == nil {
				cp := *re
				cp.Scenario = res.Scenario
				w.Ripple[k] = &cp
				continue
			}
			if re.IRmsA > wr.IRmsA {
				wr.IRmsA, wr.Scenario = re.IRmsA, res.Scenario
			}
			wr.IPeakA = math.Max(wr.IPeakA, re.IPeakA)
			wr.IAvgA = math.Max(wr.IAvgA, re.IAvgA)
			wr.DeltaIA = math.Max(wr.DeltaIA, re.DeltaIA)
			wr.Duty = math.Max(wr.Duty, re.Duty)
		}
	}
	if w.Warnings == nil {
		w.Warnings = []string{}
	}
	return w
}

// attachThermal fills the steady-state thermal fields of res.
//
// Current capacity (IR drop, track width, via count, ratings) must see the
// peak operating point, but heat is integrated over time: a burst load (an
// ESP32 Wi-Fi TX at 0.5 A with a 0.1 A average) dissipates its AVERAGE
// power, not its peak. avg is the twin operating point of the same scenario
// (same sources and switches) with every load at its model's average
// current (typA); nil when res is itself an average scenario.
//
//   - parts whose dissipation is linear in their current (loads, ICs, LED,
//     LDO (Vin−Vout)·I, buck (1/η−1)·Pout, sources): thermalW = P_avg (exact
//     for a constant-voltage load).
//   - I²R-type parts (resistors, inductors, ferrites, fuses, diodes, BJTs,
//     ESD, bridges) and copper: a current bounded by [0, I_peak] with mean
//     I_avg has E[I²] ≤ I_avg·I_peak (the bang-bang waveform), so
//     thermalW = √(P_avg·P_peak) and thermalCurrentA = √(I_avg·I_peak) —
//     an upper bound on the time-averaged heat that stays conservative for
//     any duty cycle the models do not state.
func attachThermal(res, avg *Result) {
	defer func() {
		for _, pr := range res.Parts {
			if pr.OffBoard {
				pr.ThermalW = 0 // the external load behind the connector dissipates it
			}
		}
	}()
	if avg == nil {
		res.ThermalBasis = ThermalAverage
		for _, nr := range res.Nets {
			nr.ThermalCurrentA = nr.CurrentA
		}
		for _, pr := range res.Parts {
			pr.ThermalW = pr.PowerW
		}
		return
	}
	res.ThermalBasis = ThermalAverageBound
	for net, nr := range res.Nets {
		a := 0.0
		if an := avg.Nets[net]; an != nil {
			a = an.CurrentA
		}
		nr.ThermalCurrentA = round(boundedRMS(a, nr.CurrentA), 7)
	}
	for ref, pr := range res.Parts {
		a := 0.0
		if ap := avg.Parts[ref]; ap != nil {
			a = ap.PowerW
		}
		switch pr.Model {
		case KindResistor, KindInductor, KindFerrite, KindFuse, KindDiode, KindBJT, KindESD, KindBridge, KindOpen, KindCapacitor:
			pr.ThermalW = round(boundedRMS(a, pr.PowerW), 6)
		default:
			pr.ThermalW = a
		}
	}
}

var reConnectorRef = regexp.MustCompile(`^(J|P|CN|CON|X|XS|XP|USB|TB)\d`)

// offBoard reports whether a bound part's power is drawn by an external load
// (Model.OffBoard, default: a load model on a connector designator).
func (e *Engine) offBoard(b *binding) bool {
	if b.kind != KindLoad {
		return false
	}
	if b.model.OffBoard != nil {
		return *b.model.OffBoard
	}
	if reConnectorRef.MatchString(strings.ToUpper(b.part.Ref)) {
		e.assumef("%s: load model %s on a connector — the power it draws is the external load's (off the board), not board heat (set offBoard:false in the model if the load sits on the board)", b.part.Ref, b.model.ID)
		return true
	}
	return false
}

// boundedRMS is √(avg·peak) clamped to [avg, peak] (both ≥ 0); it returns
// avg when the average point is not below the peak one.
func boundedRMS(avg, peak float64) float64 {
	if avg <= 0 || peak <= 0 {
		return math.Max(avg, 0)
	}
	if avg >= peak {
		return avg
	}
	return math.Sqrt(avg * peak)
}

func rank(role string) int {
	switch role {
	case "switch":
		return 3
	case "power":
		return 2
	case "ground":
		return 1
	}
	return 0
}

func scenarioList(rs []*Result) string {
	var names []string
	for _, r := range rs {
		names = append(names, r.Scenario)
	}
	return strings.Join(names, ", ")
}
