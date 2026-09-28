package analogsim

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

var (
	reResetPin = regexp.MustCompile(`(?i)^(N?_?RST|N?RESET|RESET_?N|NRST|EN|CHIP_?PU|CHIP_?EN|MCLR|RSTN)$`)
	reXtalRef  = regexp.MustCompile(`(?i)^(Y|X|XT|XTAL|OSC)[0-9]`)
	reXtalDesc = regexp.MustCompile(`(?i)crystal|晶振|谐振器|resonator`)
	reSwNode   = regexp.MustCompile(`(?i)^(LX|SW|PH|PHASE|SW[0-9])$`)
	reADCNet   = regexp.MustCompile(`(?i)(ADC|AIN|VBAT_?SENSE|V_?SENSE|_SENSE$|SENSE|NTC|POT|ANALOG|_MON$|VMON|IMON)`)
)

// adcAt returns the ADC sample model of an ADC input pin on net n (nil = none).
func (c *circuit) adcAt(n string) *adcLoad {
	for _, pr := range c.netPins[n] {
		p := c.parts[pr.Ref]
		if p == nil {
			continue
		}
		m := c.lib.adc(p)
		if m == nil || m.pinRe == nil || !m.pinRe.MatchString(normPin(pr.Name)) && !m.pinRe.MatchString(strings.TrimSpace(pr.Name)) {
			continue
		}
		// An MCU GPIO is an ADC input only when the net is analog-ish.
		if m.Bits > 0 && !strings.HasPrefix(strings.ToUpper(m.ID), "MCP") && !reADCNet.MatchString(n) && !c.analogDriven(n) {
			continue
		}
		a := &adcLoad{Net: n, Model: *m, Owner: pr.Ref, Pin: pr.Pin}
		a.VrefV, a.VrefSrc = c.adcVref(p, m)
		return a
	}
	return nil
}

// analogDriven: net n is fed through ≤ 2 passives from an op-amp output or a divider.
func (c *circuit) analogDriven(n string) bool {
	for _, e := range c.edges[n] {
		if e.Kind != "R" {
			continue
		}
		for _, pr := range c.netPins[e.Other] {
			if r, _ := opampPinRole(pr.Name); r == "out" {
				return true
			}
		}
	}
	// divider: R to a rail and R to another net
	hasRail, hasOther := false, false
	for _, e := range c.edges[n] {
		if e.Kind == "R" && c.isRail(e.Other) && !c.ground[e.Other] {
			hasRail = true
		}
		if e.Kind == "R" && c.ground[e.Other] {
			hasOther = true
		}
	}
	return hasRail && hasOther
}

// adcVref finds the ADC reference voltage: the VREF pin's net (rail /
// reference), else the ADC's supply.
func (c *circuit) adcVref(p *powersim.Part, m *ADCModel) (float64, string) {
	if m.vrefRe != nil {
		for _, pin := range p.Pins {
			if m.vrefRe.MatchString(normPin(pin.Name)) || m.vrefRe.MatchString(pin.Name) {
				if v, ok := c.railV[pin.Net]; ok {
					return v, "VREF pin on " + pin.Net
				}
			}
		}
	}
	if v, ok := c.icSupply(p.Ref); ok {
		return v, "supply of " + p.Ref
	}
	return 3.3, "assumed"
}

// ampBlocks: fixed-gain current-sense / instrumentation amplifier ICs.
func (c *circuit) ampBlocks() ([]*Block, []string) {
	var out []*Block
	var notes []string
	for _, p := range c.d.Parts {
		m := c.lib.amplifier(p)
		if m == nil {
			continue
		}
		a := &ampInst{Ref: p.Ref, Model: *m}
		var rg []string
		for _, pin := range p.Pins {
			s := normPin(pin.Name)
			switch {
			case s == "IN+" || s == "+IN" || s == "VIN+" || s == "INP":
				a.Inp = pin.Net
			case s == "IN-" || s == "-IN" || s == "VIN-" || s == "INN":
				a.Inn = pin.Net
			case s == "OUT" || s == "VOUT" || s == "VO":
				a.Out = pin.Net
			case s == "REF" || s == "REF1" || s == "REF2":
				if a.RefN == "" {
					a.RefN = pin.Net
				}
			case s == "VS" || s == "V+" || s == "VCC" || s == "VDD" || s == "VS+" || s == "+VS":
				a.Vp = pin.Net
			case s == "GND" || s == "V-" || s == "VSS" || s == "VS-" || s == "-VS" || s == "VEE":
				a.Vn = pin.Net
			case strings.HasPrefix(s, "RG"):
				rg = append(rg, pin.Net)
			}
		}
		if a.Inp == "" || a.Inn == "" || a.Out == "" || a.Vp == "" {
			notes = append(notes, p.Ref+": amplifier pins not recognised — not analysed")
			continue
		}
		if a.Vn == "" {
			for n := range c.ground {
				a.Vn = n
				break
			}
		}
		b := newBlockCtx()
		b.amps = []*ampInst{a}
		blk := &Block{Core: p.Ref, c: b, Output: a.Out}
		for _, n := range []string{a.Vp, a.Vn} {
			if v, ok := c.railV[n]; ok {
				b.rails[n] = v
			}
		}
		if a.RefN != "" {
			if v, ok := c.railV[a.RefN]; ok {
				b.rails[a.RefN] = v
			}
		}
		switch m.Kind {
		case "instrumentation":
			blk.Class = ClassInstrument
			if len(rg) == 2 {
				for _, e := range c.between(rg[0], rg[1], "R") {
					a.RgRef = e.Ref
					b.setRole(e.Ref, "Rg")
					b.addPassives(e.Ref)
				}
			}
			mm := *m
			b.eval = func(v map[string]float64) map[string]float64 {
				g := mm.GainOffset
				if rgv := v[a.RgRef]; rgv > 0 {
					g += mm.GainK / rgv
				}
				bw := mm.BWHz
				if mm.GBWHz > 0 && mm.GBWHz/g < bw {
					bw = mm.GBWHz / g
				}
				return map[string]float64{"gain": g, "fcHz": bw}
			}
			a.Gain = b.eval(c.values(b.passives))["gain"]
			blk.Topology = fmt.Sprintf("instrumentation amplifier, G = %g + %g/RG", m.GainOffset, m.GainK)
			b.stim, b.stim2 = a.Inp, a.Inn
			b.resp = "amp"
			b.ac, b.op, b.step = true, true, true
		case "current-sense":
			blk.Class = ClassCurrentSense
			a.Gain = m.Gain
			// Shunt between IN+ and IN- (direct or through ≤ 100 Ω filter resistors).
			shunt := c.between(a.Inp, a.Inn, "R")
			if len(shunt) == 0 {
				for _, e := range c.edges[a.Inp] {
					if e.Kind == "R" && e.Value <= 100 {
						for _, e2 := range c.edges[a.Inn] {
							if e2.Kind == "R" && e2.Value <= 100 {
								shunt = append(shunt, c.between(e.Other, e2.Other, "R")...)
								b.addPassives(e.Ref, e2.Ref)
							}
						}
					}
				}
			}
			if len(shunt) > 0 {
				b.setRole(shunt[0].Ref, "Rshunt")
				b.addPassives(shunt[0].Ref)
				imax := c.shuntCurrent(shunt[0].Ref)
				b.loadI["_shunt"] = imax
			}
			mm := *m
			sref := b.roleRef["Rshunt"]
			b.eval = func(v map[string]float64) map[string]float64 {
				out := map[string]float64{"gain": mm.Gain, "fcHz": mm.BWHz}
				if r := v[sref]; r > 0 {
					out["transimpedanceVperA"] = mm.Gain * r
					if i := b.loadI["_shunt"]; i > 0 {
						out["voutAtImaxV"] = mm.Gain * r * i
					}
				}
				return out
			}
			blk.Topology = fmt.Sprintf("current-sense amplifier, gain %g V/V", m.Gain)
			b.stim, b.stim2 = a.Inp, a.Inn
			b.resp = "amp"
			b.ac, b.op = true, true
		}
		if a.Out != "" {
			if ad := c.adcAt(a.Out); ad != nil {
				b.adc = ad
			}
			loads, _ := c.outLoads(a.Out, map[string]bool{})
			b.addPassives(loads...)
		}
		b.out = a.Out
		blk.Input, blk.Input2 = b.stim, b.stim2
		blk.Parts = sortRefs(append([]string{p.Ref}, b.passives...))
		blk.Nets = uniq([]string{a.Inp, a.Inn, a.Out, a.Vp, a.Vn, a.RefN})
		blk.Title = titleFor(blk.Class, p.Ref, "", a.Out)
		b.mcMetrics = []string{"gain"}
		out = append(out, blk)
	}
	return out, notes
}

// shuntCurrent: the worst-case DC current through a shunt from the power sim.
func (c *circuit) shuntCurrent(ref string) float64 {
	if c.sim == nil {
		return 0
	}
	best := 0.0
	for _, r := range c.sim.Results {
		for _, nr := range r.Nets {
			for _, p := range nr.Pins {
				if p.Ref == ref && p.CurrentA > best {
					best = p.CurrentA
				}
			}
		}
	}
	return best
}

// values returns the current passive values by ref.
func (c *circuit) values(refs []string) map[string]float64 {
	out := map[string]float64{}
	for _, r := range refs {
		if p := c.passive[r]; p != nil {
			out[r] = p.Value
		}
	}
	return out
}

// referenceBlocks: shunt references biased from a rail (and marks their
// cathode net as a reference rail for the op-amp detection).
//
// An adjustable shunt (TL431) whose REF pin is NOT tied to its cathode is a
// shunt regulator / error amplifier (the REF divider of an opto-coupled
// flyback feedback): its cathode current is set by the loop, not by a bias
// resistor from a rail. It is not a reference block — simulating it as one
// left the cathode unbiased (0 A) and failed a fabricated Ik — so it is
// reported as a warning that asks for the loop's Ik,min check instead.
func (c *circuit) findReferences() ([]*refInst, []string) {
	var out []*refInst
	var notes []string
	for _, p := range c.d.Parts {
		m := c.lib.reference(p)
		if m == nil || m.Kind != "shunt" {
			continue
		}
		ri := &refInst{Ref: p.Ref, Model: *m}
		refNet := ""
		for _, pin := range p.Pins {
			s := normPin(pin.Name)
			switch {
			case s == "K" || s == "CATHODE" || s == "C" || s == "+":
				ri.K = pin.Net
			case s == "A" || s == "ANODE" || s == "-":
				ri.A = pin.Net
			case s == "REF" || s == "R" || s == "VREF" || s == "ADJ" || s == "FB":
				refNet = pin.Net
			}
		}
		if refNet != "" && ri.K != "" && refNet != ri.K {
			ik := ""
			if m.IkMinA > 0 {
				ik = fmt.Sprintf(" ≥ %s A", FormatSI(m.IkMinA))
			}
			msg := fmt.Sprintf("%s (%s): REF on %s, cathode on %s — a shunt regulator / error amplifier in a feedback loop, not a biased reference; not simulated", p.Ref, m.ID, refNet, ri.K)
			notes = append(notes, msg)
			c.loopFindings = append(c.loopFindings, Finding{Severity: "warn", Kind: "shunt-regulator-loop", Refs: []string{p.Ref}, Nets: []string{refNet, ri.K},
				Message:    msg + fmt.Sprintf("; its cathode current comes through the loop (opto LED / pull-up), so Ik%s is not verified", ik),
				Suggestion: fmt.Sprintf("check Ik%s at the loop's minimum drive (minimum COMP source current ÷ opto CTR); a bleeder resistor across the opto LED (≈ 1 kΩ for a 1 V LED drop) guarantees it", ik)})
			continue
		}
		if ri.K == "" || ri.A == "" {
			// 2-pin symbol with numeric pins: pin 1 = cathode (LM4040 SOT-23: 1 +, 2 −).
			for _, pin := range p.Pins {
				if c.ground[pin.Net] {
					ri.A = pin.Net
				} else if pin.Net != "" && ri.K == "" {
					ri.K = pin.Net
				}
			}
		}
		if ri.K == "" || ri.A == "" || ri.K == ri.A {
			continue
		}
		out = append(out, ri)
	}
	return out, notes
}

func (c *circuit) referenceBlock(ri *refInst) *Block {
	b := newBlockCtx()
	b.refs = []*refInst{ri}
	blk := &Block{Core: ri.Ref, Class: ClassReference, c: b, Output: ri.K}
	var bias *edge
	for _, e := range c.edges[ri.K] {
		e := e
		if e.Kind == "R" && c.isRail(e.Other) && !c.ground[e.Other] && c.railSrc[e.Other] != "reference" {
			bias = &e
		}
	}
	for n, v := range map[string]float64{ri.A: 0} {
		b.rails[n] = v
	}
	if bias != nil {
		b.setRole(bias.Ref, "Rbias")
		b.addPassives(bias.Ref)
		b.rails[bias.Other] = c.railV[bias.Other]
	}
	// Loads on the reference net: resistors to ground, ADC VREF pins.
	iload := 0.0
	var loadNotes []string
	for _, e := range c.edges[ri.K] {
		if bias != nil && e.Ref == bias.Ref {
			continue
		}
		if c.ground[e.Other] {
			b.addPassives(e.Ref)
		}
	}
	for _, pr := range c.nonPassivePins(ri.K, ri.Ref) {
		p := c.parts[pr.Ref]
		if m := c.lib.adc(p); m != nil && m.vrefRe != nil && (m.vrefRe.MatchString(normPin(pr.Name)) || m.vrefRe.MatchString(pr.Name)) {
			iload += m.VrefInputA
			loadNotes = append(loadNotes, fmt.Sprintf("%s.%s VREF input %s A (%s)", pr.Ref, pr.Name, FormatSI(m.VrefInputA), m.ID))
		} else {
			loadNotes = append(loadNotes, fmt.Sprintf("%s.%s (input current not modelled)", pr.Ref, pr.Name))
		}
	}
	b.loadI[ri.K] = iload
	b.out = ri.K
	b.op, b.mc = true, bias != nil
	blk.Notes = loadNotes
	rb := b.roleRef["Rbias"]
	vsup := 0.0
	if bias != nil {
		vsup = c.railV[bias.Other]
	}
	m := ri.Model
	b.eval = func(v map[string]float64) map[string]float64 {
		out := map[string]float64{"voutV": m.VzV}
		if r := v[rb]; r > 0 {
			// Loads to ground on the reference node.
			il := iload
			for _, ref := range b.passives {
				if ref != rb {
					if pv := c.passive[ref]; pv != nil && pv.Kind == "R" {
						il += m.VzV / v[ref]
					}
				}
			}
			ik := (vsup-m.VzV)/r - il
			out["ikA"] = ik
			out["loadA"] = il
			out["pdW"] = ik * m.VzV
		}
		return out
	}
	b.mcMetrics = []string{"ikA"}
	blk.Topology = fmt.Sprintf("shunt reference %s (%g V)", m.ID, m.VzV)
	if bias != nil {
		blk.Topology += " biased by " + bias.Ref + " from " + bias.Other
	}
	blk.Parts = sortRefs(append([]string{ri.Ref}, b.passives...))
	blk.Nets = uniq([]string{ri.K, ri.A})
	blk.Title = titleFor(ClassReference, ri.Ref, "", ri.K)
	return blk
}

// regulatorBlocks: feedback dividers of regulators with a Vref in power-models.
func (c *circuit) regulatorBlocks() []*Block {
	var out []*Block
	for _, p := range c.d.Parts {
		pm := c.pmodels[p.Ref]
		if pm == nil || pm.Vref <= 0 {
			continue
		}
		fbNames := pm.Pins["fb"]
		var fb string
		for _, pin := range p.Pins {
			for _, n := range append(fbNames, "FB", "ADJ", "VFB") {
				if strings.EqualFold(pin.Name, n) {
					fb = pin.Net
				}
			}
		}
		if fb == "" {
			continue
		}
		var top, bot *edge
		for _, e := range c.edges[fb] {
			e := e
			if e.Kind != "R" {
				continue
			}
			if c.ground[e.Other] {
				bot = &e
			} else if c.isRail(e.Other) {
				top = &e
			}
		}
		if top == nil || bot == nil {
			continue
		}
		b := newBlockCtx()
		b.setRole(top.Ref, "Rtop")
		b.setRole(bot.Ref, "Rbot")
		b.addPassives(top.Ref, bot.Ref)
		// Feed-forward capacitor across Rtop.
		for _, e := range c.between(fb, top.Other, "C") {
			b.setRole(e.Ref, "Cff")
			b.addPassives(e.Ref)
		}
		vref := pm.Vref
		b.rails[bot.Other] = 0
		b.out = top.Other
		b.regFB, b.regVref = fb, vref
		b.op, b.mc = true, true
		rt, rbb := top.Ref, bot.Ref
		b.eval = func(v map[string]float64) map[string]float64 {
			o := map[string]float64{"voutV": vref * (1 + v[rt]/v[rbb]), "dividerCurrentA": vref / v[rbb]}
			if cff := v[b.roleRef["Cff"]]; cff > 0 {
				o["ffZeroHz"] = 1 / (twoPi() * v[rt] * cff)
			}
			return o
		}
		b.mcMetrics = []string{"voutV"}
		blk := &Block{Core: p.Ref, Class: ClassRegulatorFB, c: b, Output: top.Other,
			Topology: fmt.Sprintf("%s %s feedback divider: Vout = %g V·(1 + %s/%s)", pm.Kind, pm.ID, vref, top.Ref, bot.Ref)}
		blk.Model = &ModelRef{Ref: p.Ref, ID: pm.ID, Kind: pm.Kind, Confidence: nz(pm.Confidence, "approx"), Source: pm.Source, Params: map[string]float64{"vrefV": vref}}
		blk.Parts = sortRefs([]string{p.Ref, top.Ref, bot.Ref})
		blk.Nets = uniq([]string{fb, top.Other, bot.Other})
		blk.Title = titleFor(ClassRegulatorFB, p.Ref, "", top.Other)
		blk.Notes = append(blk.Notes, "loop compensation not modelled (no regulator loop model in power-models.json)",
			"tolerance spread covers the divider resistors only; add the Vref accuracy of the datasheet (typically ±1–2 %) on top")
		out = append(out, blk)
	}
	return out
}

func nz(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// crystalBlocks: 2/4-pin crystals with a load capacitor to ground on each side.
func (c *circuit) crystalBlocks() []*Block {
	var out []*Block
	for _, p := range c.d.Parts {
		if !reXtalRef.MatchString(p.Ref) && !reXtalDesc.MatchString(p.Description+" "+p.DeviceName) {
			continue
		}
		if reXtalDesc.MatchString(p.Description) && regexp.MustCompile(`(?i)oscillator|有源`).MatchString(p.Description) {
			continue
		}
		var sig []string
		for _, pin := range p.Pins {
			if pin.Net != "" && !c.ground[pin.Net] && !c.isRail(pin.Net) {
				sig = append(sig, pin.Net)
			}
		}
		sig = uniq(sig)
		if len(sig) != 2 {
			continue
		}
		var c1, c2 *edge
		for i, n := range sig {
			for _, e := range c.edges[n] {
				e := e
				if e.Kind == "C" && c.ground[e.Other] {
					if i == 0 {
						c1 = &e
					} else {
						c2 = &e
					}
				}
			}
		}
		if c1 == nil || c2 == nil {
			continue
		}
		fHz, clPF := crystalSpec(p)
		d := c.lib.Defaults.Crystal
		x := &xtalInst{Ref: p.Ref, A: sig[0], B: sig[1], FHz: fHz, CLPF: clPF, C0PF: d.C0PF, CmFF: d.CmFF, ESR: d.ESROhm}
		b := newBlockCtx()
		b.xtal = x
		b.setRole(c1.Ref, "C1")
		b.setRole(c2.Ref, "C2")
		b.addPassives(c1.Ref, c2.Ref)
		b.rails[c1.Other] = 0
		stray := c.lib.Defaults.StrayPF
		b.eval = func(v map[string]float64) map[string]float64 {
			a, bb := v[b.roleRef["C1"]], v[b.roleRef["C2"]]
			cl := a*bb/(a+bb) + stray*1e-12
			o := map[string]float64{"clPF": cl * 1e12}
			if x.CLPF > 0 && x.FHz > 0 {
				cm, c0 := x.CmFF*1e-15, x.C0PF*1e-12
				o["pullPpm"] = cm / 2 * (1/(c0+cl) - 1/(c0+x.CLPF*1e-12)) * 1e6
			}
			return o
		}
		b.op = false
		b.ac = fHz > 0
		b.mc = true
		b.mcMetrics = []string{"clPF"}
		blk := &Block{Core: p.Ref, Class: ClassCrystal, c: b,
			Topology: fmt.Sprintf("Pierce load: CL = C1·C2/(C1+C2) + %g pF stray", stray)}
		blk.Parts = sortRefs([]string{p.Ref, c1.Ref, c2.Ref})
		blk.Nets = uniq(sig)
		blk.Title = titleFor(ClassCrystal, p.Ref, "", "")
		conf := "description"
		if clPF == 0 {
			conf = "unknown"
		}
		blk.Model = &ModelRef{Ref: p.Ref, ID: "crystal-bvd", Kind: "crystal", Confidence: d.Confidence, Source: d.Source,
			Params: map[string]float64{"fHz": fHz, "clSpecPF": clPF, "c0PF": x.C0PF, "cmFF": x.CmFF, "esrOhm": x.ESR}}
		blk.Notes = append(blk.Notes, "crystal CL from the part "+conf+"; oscillation margin (negative resistance vs ESR) needs the MCU oscillator gm — not modelled")
		out = append(out, blk)
	}
	return out
}

// resetBlocks: MCU reset / enable pins with an RC (R to rail, C to ground).
func (c *circuit) resetBlocks() []*Block {
	var out []*Block
	for _, p := range c.d.Parts {
		rm := c.lib.reset(p)
		for _, pin := range p.Pins {
			if pin.Net == "" {
				continue
			}
			name := strings.TrimSpace(pin.Name)
			if rm != nil {
				if !rm.pinRe.MatchString(name) {
					continue
				}
			} else if !reResetPin.MatchString(name) || len(p.Pins) < 8 {
				continue
			}
			var r, cc *edge
			for _, e := range c.edges[pin.Net] {
				e := e
				if e.Kind == "R" && c.isRail(e.Other) && !c.ground[e.Other] {
					r = &e
				}
				if e.Kind == "C" && c.ground[e.Other] {
					cc = &e
				}
			}
			if r == nil || cc == nil {
				continue
			}
			b := newBlockCtx()
			b.setRole(r.Ref, "R")
			b.setRole(cc.Ref, "C")
			b.addPassives(r.Ref, cc.Ref)
			vdd := c.railV[r.Other]
			b.rails[cc.Other] = 0
			b.supplyStep = r.Other
			b.rails[r.Other] = vdd
			b.out = pin.Net
			frac := 0.75
			if rm != nil && rm.VihFraction > 0 {
				frac = rm.VihFraction
			}
			b.vih = frac * vdd
			b.switching = false
			b.op = false
			b.step = true
			b.mc = true
			rr, cr := r.Ref, cc.Ref
			b.eval = func(v map[string]float64) map[string]float64 {
				tau := v[rr] * v[cr]
				return map[string]float64{"tauS": tau, "delayS": -tau * math.Log(1-frac)}
			}
			b.tauGuess = r.Value * cc.Value
			b.mcMetrics = []string{"delayS"}
			blk := &Block{Core: p.Ref + "." + name, Class: ClassResetRC, c: b, Output: pin.Net,
				Topology: fmt.Sprintf("%s pull-up %s to %s with %s to GND on %s.%s", "RC", r.Ref, r.Other, cc.Ref, p.Ref, name)}
			if rm != nil {
				blk.Model = &ModelRef{Ref: p.Ref, ID: rm.ID, Kind: "reset", Confidence: nz(rm.Confidence, "approx"), Source: rm.Source,
					Params: map[string]float64{"vihFraction": rm.VihFraction, "minDelayS": rm.MinDelayS, "minLowS": rm.MinLowS}}
			} else {
				blk.Model = &ModelRef{Ref: p.Ref, ID: "generic-reset", Kind: "reset", Confidence: "assumed",
					Source: "no reset timing in analog-models.json: VIH = 0.75·VDD assumed, no minimum delay", Params: map[string]float64{"vihFraction": 0.75}}
			}
			blk.Parts = sortRefs([]string{p.Ref, r.Ref, cc.Ref})
			blk.Nets = uniq([]string{pin.Net, r.Other, cc.Other})
			blk.Title = titleFor(ClassResetRC, p.Ref+"."+name, "", pin.Net)
			out = append(out, blk)
		}
	}
	return out
}

// switchBlocks: BJTs / MOSFETs driven through a base/gate resistor.
func (c *circuit) switchBlocks() []*Block {
	var out []*Block
	for _, p := range c.d.Parts {
		pm := c.pmodels[p.Ref]
		sw := c.lib.bjt(p)
		fm := c.lib.mosfet(p)
		isBJT := (pm != nil && pm.Kind == "bjt") || sw != nil
		roles := map[string]string{}
		for _, pin := range p.Pins {
			s := normPin(pin.Name)
			switch s {
			case "B", "BASE":
				roles["b"] = pin.Net
			case "C", "COLLECTOR":
				roles["c"] = pin.Net
			case "E", "EMITTER":
				roles["e"] = pin.Net
			case "G", "GATE":
				roles["g"] = pin.Net
			case "D", "DRAIN":
				roles["d"] = pin.Net
			case "S", "SOURCE":
				roles["s"] = pin.Net
			}
		}
		if roles["b"] != "" && roles["c"] != "" && roles["e"] != "" {
			isBJT = true
		} else if !(roles["g"] != "" && roles["d"] != "" && roles["s"] != "") {
			continue
		}
		if !isBJT && fm == nil && !strings.HasPrefix(strings.ToUpper(p.Ref), "Q") {
			continue
		}
		b := newBlockCtx()
		blk := &Block{Core: p.Ref, Class: ClassTransistorSw, c: b}
		var ctrl, load, ret string
		if isBJT {
			ctrl, load, ret = roles["b"], roles["c"], roles["e"]
			bi := &bjtInst{Ref: p.Ref, B: ctrl, C: load, E: ret, NPN: true, IS: 6.734e-15, BF: 200, BR: 0.74, Sw: sw, Confidence: "assumed", Source: "PCBPILOT_NPN generic"}
			if pm != nil && pm.Kind == "bjt" {
				bi.NPN = pm.Polarity != "pnp"
				if pm.IsA > 0 {
					bi.IS = pm.IsA
				}
				if pm.Beta > 0 {
					bi.BF = pm.Beta
				}
				if pm.BetaR > 0 {
					bi.BR = pm.BetaR
				}
				bi.Confidence, bi.Source, bi.ModelID = nz(pm.Confidence, "approx"), pm.Source, pm.ID
			} else if sw != nil {
				bi.NPN = sw.Polarity != "pnp"
				bi.Confidence, bi.Source, bi.ModelID = nz(sw.Confidence, "approx"), sw.Source, sw.ID
			}
			b.bjts = []*bjtInst{bi}
			if !bi.NPN {
				blk.Notes = append(blk.Notes, "PNP switch: analysed with the drive levels reversed")
			}
		} else {
			ctrl, load, ret = roles["g"], roles["d"], roles["s"]
			fi := &fetInst{Ref: p.Ref, G: ctrl, D: load, S: ret, N: true}
			if fm != nil {
				fi.Model = *fm
				fi.N = fm.Polarity != "p"
			} else {
				fi.Model = FETModel{ID: "generic-nmos", VthV: 1.5, VthMaxV: 2.5, RdsonOhm: 1, RdsonVgsV: 4.5, Confidence: "assumed", Source: "PCBPILOT_NMOS generic"}
			}
			b.fets = []*fetInst{fi}
		}
		// Drive: a resistor from a driver net into the base/gate (or the gate tied to a rail).
		var rdrv *edge
		for _, e := range c.edges[ctrl] {
			e := e
			if e.Kind == "R" && !c.ground[e.Other] {
				if rdrv == nil || !c.isRail(e.Other) {
					rdrv = &e
				}
			}
		}
		levelShift := !isBJT && c.isRail(ctrl) && !c.ground[ctrl]
		if rdrv == nil && !levelShift {
			continue
		}
		if rdrv != nil {
			b.setRole(rdrv.Ref, "Rdrive")
			b.addPassives(rdrv.Ref)
			b.driveNet = rdrv.Other
		} else {
			b.driveNet = ctrl
		}
		// Base/gate pull-down.
		for _, e := range c.edges[ctrl] {
			if e.Kind == "R" && c.ground[e.Other] {
				b.setRole(e.Ref, "Rpd")
				b.addPassives(e.Ref)
			}
		}
		// Load network on the collector/drain: pull-ups and caps (one hop).
		for _, e := range c.edges[load] {
			if c.isRail(e.Other) {
				role := "Rload"
				if e.Kind == "C" {
					role = "Cload"
				}
				b.setRole(e.Ref, role)
				b.addPassives(e.Ref)
				b.rails[e.Other] = c.railV[e.Other]
			}
		}
		for _, e := range c.edges[ret] {
			if c.isRail(e.Other) && e.Kind == "R" {
				b.setRole(e.Ref, "Rret")
				b.addPassives(e.Ref)
				b.rails[e.Other] = c.railV[e.Other]
			}
		}
		if levelShift {
			blk.Class = ClassLevelShifter
		}
		if b.roleRef["Rload"] == "" {
			// Load current comes from what the collector drives (an IC input pin, LED …).
			blk.Notes = append(blk.Notes, "no pull-up on "+load+" — collector/drain load is whatever the connected pins sink")
		}
		hi, drv := c.driverHigh(b.driveNet, p.Ref)
		if c.isRail(b.driveNet) {
			hi, drv = c.railV[b.driveNet], b.driveNet
		}
		b.driveHigh = hi
		if drv != "" {
			blk.Notes = append(blk.Notes, fmt.Sprintf("drive high level %.2f V (%s supply)", hi, drv))
		}
		b.emitNet = ret
		b.out = load
		b.switching = true
		b.op = true
		// Reset-pin load: the collector/drain drives an MCU reset → note VIH.
		for _, pr := range c.nonPassivePins(load, p.Ref) {
			if pp := c.parts[pr.Ref]; pp != nil {
				if rm := c.lib.reset(pp); rm != nil && rm.pinRe.MatchString(strings.TrimSpace(pr.Name)) {
					if v, ok := c.railV[b.roleRefNet(c, "Rload")]; ok {
						b.vih = rm.VihFraction * v
					}
				}
			}
		}
		if b.vih == 0 {
			for _, n := range sortedKeys(b.rails) {
				if b.rails[n] > b.vih {
					b.vih = 0.7 * b.rails[n]
				}
			}
		}
		for _, n := range []string{ret, ctrl} {
			if v, ok := c.railV[n]; ok {
				b.rails[n] = v
			}
		}
		rd, rl := b.roleRef["Rdrive"], b.roleRef["Rload"]
		var hfe, vbe float64 = 100, 0.7
		if sw != nil {
			hfe, vbe = sw.HfeMin, sw.VbeOnV
		}
		isB := isBJT
		b.eval = func(v map[string]float64) map[string]float64 {
			o := map[string]float64{}
			if isB && v[rd] > 0 {
				ib := (hi - vbe) / v[rd]
				o["ibA"] = ib
				if rl != "" && v[rl] > 0 {
					vl := c.railV[b.roleRefNet(c, "Rload")]
					ic := vl / v[rl]
					o["icA"] = ic
					o["forcedBeta"] = ic / ib
					o["overdrive"] = ib * hfe / ic
				}
			}
			return o
		}
		b.mcMetrics = nil
		blk.Topology = fmt.Sprintf("%s switch, drive %s → %s, load on %s, return %s", map[bool]string{true: "BJT", false: "MOSFET"}[isBJT], b.driveNet, ctrl, load, ret)
		blk.Output = load
		blk.Input = b.driveNet
		blk.Parts = sortRefs(append([]string{p.Ref}, b.passives...))
		blk.Nets = uniq([]string{ctrl, load, ret, b.driveNet})
		blk.Title = titleFor(blk.Class, p.Ref, b.driveNet, load)
		out = append(out, blk)
	}
	return out
}

// roleRefNet returns the rail net of the passive with the given role.
func (b *blockCtx) roleRefNet(c *circuit, role string) string {
	ref := b.roleRef[role]
	if p := c.passive[ref]; p != nil {
		for _, n := range p.Nets {
			if c.isRail(n) {
				return n
			}
		}
	}
	return ""
}

// lcBlocks: an inductor / ferrite between two non-switch nets with capacitors
// on the output side (π / L-C supply filters).
func (c *circuit) lcBlocks() []*Block {
	var out []*Block
	for _, ref := range sortedKeys(c.passive) {
		pv := c.passive[ref]
		if pv.Kind != "L" {
			continue
		}
		a, bnet := pv.Nets[0], pv.Nets[1]
		sw := false
		for _, n := range []string{a, bnet} {
			if reSwNode.MatchString(n) {
				sw = true
			}
			for _, pr := range c.netPins[n] {
				if reSwNode.MatchString(normPin(pr.Name)) {
					sw = true
				}
			}
		}
		if sw {
			continue
		}
		// Input side = the side with a source (connector / regulator output) or higher sim current.
		in, outN := a, bnet
		if c.capSum(bnet) < c.capSum(a) {
			in, outN = bnet, a
		}
		if c.capSum(outN) == 0 {
			continue
		}
		b := newBlockCtx()
		b.setRole(ref, "L")
		b.addPassives(ref)
		for _, side := range []string{in, outN} {
			for _, e := range c.edges[side] {
				if e.Kind == "C" && c.ground[e.Other] {
					b.addPassives(e.Ref)
					b.roles[e.Ref] = "C"
					b.rails[e.Other] = 0
				}
			}
		}
		// Load: simulated current of the output net.
		if c.sim != nil {
			for _, r := range c.sim.Results {
				if r.Scenario == "typical" {
					if nr := r.Nets[outN]; nr != nil && nr.CurrentA > 1e-6 && nr.Voltage > 0 {
						b.loadR[outN] = nr.Voltage / nr.CurrentA
					}
				}
			}
		}
		b.stim, b.out = in, outN
		b.ac, b.resp = true, "lp"
		cout := c.capSum(outN)
		lv := pv.Value
		b.eval = func(v map[string]float64) map[string]float64 {
			l := v[ref]
			if l == 0 {
				l = lv
			}
			return map[string]float64{"f0Hz": 1 / (twoPi() * math.Sqrt(l*cout))}
		}
		b.mcMetrics = nil
		blk := &Block{Core: ref, Class: ClassLCFilter, c: b, Input: in, Output: outN,
			Topology: fmt.Sprintf("%s %s from %s into %s with %s to GND", map[bool]string{true: "ferrite", false: "inductor"}[strings.HasPrefix(strings.ToUpper(ref), "FB")], ref, in, outN, FormatValue(cout, "C"))}
		blk.Parts = sortRefs(b.passives)
		blk.Nets = []string{in, outN}
		blk.Title = titleFor(ClassLCFilter, ref, in, outN)
		out = append(out, blk)
	}
	return out
}

func (c *circuit) capSum(n string) float64 {
	s := 0.0
	for _, e := range c.edges[n] {
		if e.Kind == "C" && c.ground[e.Other] {
			s += e.Value
		}
	}
	return s
}

// adcInputBlocks: every ADC input pin with its source network (settling
// check); passive-only networks also get an RC filter class.
func (c *circuit) adcInputBlocks() []*Block {
	var out []*Block
	seen := map[string]bool{}
	for _, n := range c.d.Nets() {
		a := c.adcAt(n)
		if a == nil || seen[n] {
			continue
		}
		seen[n] = true
		own := func(pr pinRef) bool { return pr.Ref == a.Owner && pr.Pin == a.Pin }
		parts, nets, ports := c.regionOwn([]string{n}, own, nil)
		b := newBlockCtx()
		b.adc = a
		b.addPassives(parts...)
		for _, p := range ports {
			if v, ok := c.railV[p]; ok {
				b.rails[p] = v
			}
		}
		src, rest := c.pickStimulus(ports, map[string]bool{})
		b.openPorts = rest
		b.stim = src
		b.out = n
		b.sample = true
		b.op = true
		blk := &Block{Core: a.Owner + "." + a.Pin, Class: ClassADCInput, c: b, Output: n, Input: src}
		// Source level: a divider from a rail → its DC value; else full scale.
		b.sampleV = a.VrefV
		if src == "" {
			fixed := map[string]float64{}
			for p, v := range b.rails {
				fixed[p] = v
			}
			sol := c.dcSolve(b.passives, nil, fixed)
			if v, ok := sol[n]; ok && v > 0 {
				b.sampleV = v
			}
		}
		adcM := a.Model
		fixedNets := append([]string{}, ports...)
		b.eval = func(v map[string]float64) map[string]float64 {
			rs := c.theveninR(b.passives, v, n, fixedNets)
			cext := c.capTo(b.passives, v, n)
			o := map[string]float64{"sourceOhm": rs, "cextF": cext}
			if math.IsInf(rs, 0) {
				o["sourceOhm"] = 0
				rs = 0
			}
			bits := float64(adcM.Bits)
			// Required settling of the hold capacitor (no external cap): e^{-t/τ} ≤ ½ LSB.
			k := math.Log(math.Pow(2, bits+1))
			o["rsMaxOhm"] = adcM.TsampleS/(adcM.CshF*k) - adcM.RadcOhm
			if cext > 0 {
				o["chargeShareLsb"] = adcM.CshF / (adcM.CshF + cext) * math.Pow(2, bits)
				o["rcExtS"] = rs * cext
			}
			return o
		}
		b.mcMetrics = nil
		kind := "driven by " + src
		if src == "" {
			kind = "divider from a rail"
		}
		blk.Topology = fmt.Sprintf("%s ADC input %s (%s, %d-bit, Csh %s, tS %s), %s", a.Owner, a.Pin, a.Model.ID, a.Model.Bits, FormatValue(a.Model.CshF, "C"), FormatSI(a.Model.TsampleS)+"s", kind)
		blk.Model = &ModelRef{Ref: a.Owner, ID: a.Model.ID, Kind: "adc", Confidence: nz(a.Model.Confidence, "approx"), Source: a.Model.Source,
			Params: map[string]float64{"bits": float64(a.Model.Bits), "cshF": a.Model.CshF, "radcOhm": a.Model.RadcOhm, "tsampleS": a.Model.TsampleS, "vrefV": a.VrefV}}
		blk.Parts = sortRefs(append([]string{a.Owner}, b.passives...))
		blk.Nets = uniq(append(nets, ports...))
		blk.Title = titleFor(ClassADCInput, a.Owner+"."+a.Pin, src, n)
		out = append(out, blk)
	}
	return out
}

// rcBlocks: a series R into a node with a C to ground that feeds an IC input
// (standalone first-order filter) not claimed by another block.
func (c *circuit) rcBlocks(claimed map[string]bool) []*Block {
	var out []*Block
	for _, n := range c.d.Nets() {
		if c.isRail(n) {
			continue
		}
		var rs, cs []edge
		for _, e := range c.edges[n] {
			if claimed[e.Ref] {
				continue
			}
			if e.Kind == "R" && !c.isRail(e.Other) {
				rs = append(rs, e)
			}
			if e.Kind == "C" && c.ground[e.Other] {
				cs = append(cs, e)
			}
		}
		if len(rs) != 1 || len(cs) != 1 || len(c.nonPassivePins(n)) == 0 {
			continue
		}
		// Source side must be driven (an IC / connector pin), not another RC.
		if len(c.nonPassivePins(rs[0].Other)) == 0 {
			continue
		}
		b := newBlockCtx()
		b.setRole(rs[0].Ref, "R")
		b.setRole(cs[0].Ref, "C")
		b.addPassives(rs[0].Ref, cs[0].Ref)
		b.rails[cs[0].Other] = 0
		b.stim, b.out = rs[0].Other, n
		b.ac, b.step, b.mc, b.resp = true, true, true, "lp"
		rr, cr := rs[0].Ref, cs[0].Ref
		b.eval = func(v map[string]float64) map[string]float64 {
			return map[string]float64{"fcHz": 1 / (twoPi() * v[rr] * v[cr]), "gain": 1}
		}
		b.mcMetrics = []string{"fcHz"}
		b.gainGuess = 1
		blk := &Block{Core: rs[0].Ref + "/" + cs[0].Ref, Class: ClassRCLowPass, c: b, Input: rs[0].Other, Output: n,
			Topology: fmt.Sprintf("first-order RC low-pass %s·%s into %s", rs[0].Ref, cs[0].Ref, n)}
		blk.Parts = sortRefs([]string{rs[0].Ref, cs[0].Ref})
		blk.Nets = []string{rs[0].Other, n}
		blk.Title = titleFor(ClassRCLowPass, blk.Core, rs[0].Other, n)
		out = append(out, blk)
		claimed[rs[0].Ref], claimed[cs[0].Ref] = true, true
	}
	return out
}

// Detect finds every analog block of the design.
func (c *circuit) detect() ([]*Block, []string) {
	var blocks []*Block
	var notes []string
	// References first: their cathode nets act as rails for bias networks.
	refs, refNotes := c.findReferences()
	notes = append(notes, refNotes...)
	for _, ri := range refs {
		if c.railSrc[ri.K] != "spec" {
			c.railV[ri.K], c.railSrc[ri.K] = ri.Model.VzV, "reference"
			var keep []string
			for _, a := range c.assump {
				if !strings.HasPrefix(a, "rail "+ri.K+" ") {
					keep = append(keep, a)
				}
			}
			c.assump = keep
		}
	}
	for _, ri := range refs {
		blocks = append(blocks, c.referenceBlock(ri))
	}
	ops, n := c.findOpamps()
	notes = append(notes, n...)
	for _, o := range ops {
		b, note := c.opampBlock(o)
		if note != "" {
			notes = append(notes, note)
		}
		if b != nil {
			blocks = append(blocks, b)
		}
	}
	ab, n2 := c.ampBlocks()
	notes = append(notes, n2...)
	blocks = append(blocks, ab...)
	claimedADC := map[string]bool{}
	for _, b := range blocks {
		if b.c.adc != nil {
			claimedADC[b.c.adc.Net] = true
		}
	}
	for _, b := range c.adcInputBlocks() {
		if !claimedADC[b.c.adc.Net] {
			blocks = append(blocks, b)
		}
	}
	blocks = append(blocks, c.regulatorBlocks()...)
	blocks = append(blocks, c.crystalBlocks()...)
	blocks = append(blocks, c.resetBlocks()...)
	blocks = append(blocks, c.switchBlocks()...)
	blocks = append(blocks, c.lcBlocks()...)
	claimed := map[string]bool{}
	for _, b := range blocks {
		for _, p := range b.Parts {
			claimed[p] = true
		}
	}
	blocks = append(blocks, c.rcBlocks(claimed)...)
	order := map[string]int{ClassReference: 0}
	sort.SliceStable(blocks, func(i, j int) bool {
		oi, oj := order[blocks[i].Class], order[blocks[j].Class]
		if blocks[i].Class != ClassReference {
			oi = 1
		}
		if blocks[j].Class != ClassReference {
			oj = 1
		}
		if oi != oj {
			return oi < oj
		}
		return refLess(blocks[i].Core, blocks[j].Core)
	})
	for i, b := range blocks {
		b.ID = fmt.Sprintf("A%d", i+1)
		b.Findings, b.Metrics, b.Components = []Finding{}, []Metric{}, []Component{}
		// Components.
		for _, ref := range b.c.passives {
			pv := c.passive[ref]
			if pv == nil {
				continue
			}
			tol, src := tolerance(pv.Part, pv.Kind, &c.lib.Defaults)
			role := b.c.roles[ref]
			if role == "" {
				role = pv.Kind
			}
			b.Components = append(b.Components, Component{Ref: ref, Role: role, Kind: pv.Kind, Value: pv.Value, Text: pv.Text,
				TolPct: tol, TolSource: src, Package: packageOf(pv.Part.MPN + " " + pv.Part.Description), Nets: pv.Nets})
		}
		for _, n := range sortedKeys(b.c.rails) {
			b.Rails = append(b.Rails, Rail{Net: n, Voltage: b.c.rails[n], Source: nz(c.railSrc[n], "block")})
		}
	}
	return blocks, notes
}
