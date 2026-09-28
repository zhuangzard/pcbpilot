package analogsim

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

var (
	reConnRef  = regexp.MustCompile(`(?i)^(J|P|CN|CON|X|TP|H)[0-9]`)
	reDrivePin = regexp.MustCompile(`(?i)^(\d?V?OUT[A-D0-9]?|OUT.*|TX.*|DO|DOUT|SDO|MISO)$`)
)

// ownPins returns a predicate for the pins of op-amp channel o.
func ownPins(ref string, pins map[string]string) func(pinRef) bool {
	set := map[string]bool{}
	for _, p := range pins {
		set[p] = true
	}
	return func(pr pinRef) bool { return pr.Ref == ref && set[pr.Pin] }
}

// regionOwn grows through passives from seeds; pins matching own are not
// foreign. stop nets become ports without expansion.
func (c *circuit) regionOwn(seeds []string, own func(pinRef) bool, stop map[string]bool) (parts, nets, ports []string) {
	seenNet, seenPart, isPort := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var queue []string
	for _, s := range seeds {
		if s != "" && !seenNet[s] {
			seenNet[s] = true
			queue = append(queue, s)
		}
	}
	seed := map[string]bool{}
	for _, s := range seeds {
		seed[s] = true
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if c.isRail(n) || stop[n] {
			isPort[n] = true
			continue
		}
		foreign := false
		for _, p := range c.netPins[n] {
			if _, ok := c.passive[p.Ref]; ok || (own != nil && own(p)) {
				continue
			}
			foreign = true
		}
		if foreign {
			isPort[n] = true
			if !seed[n] {
				// Shunt elements to rails still load a port node.
				for _, e := range c.edges[n] {
					if c.isRail(e.Other) && !seenPart[e.Ref] {
						seenPart[e.Ref] = true
						parts = append(parts, e.Ref)
						if !seenNet[e.Other] {
							seenNet[e.Other] = true
							isPort[e.Other] = true
						}
					}
				}
				continue
			}
		}
		for _, e := range c.edges[n] {
			if seenPart[e.Ref] {
				continue
			}
			seenPart[e.Ref] = true
			parts = append(parts, e.Ref)
			if !seenNet[e.Other] {
				seenNet[e.Other] = true
				queue = append(queue, e.Other)
			}
		}
		if len(seenPart) > 60 {
			break
		}
	}
	for n := range seenNet {
		if isPort[n] {
			ports = append(ports, n)
		} else {
			nets = append(nets, n)
		}
	}
	return sortRefs(parts), sortStrings(nets), sortStrings(ports)
}

func sortStrings(xs []string) []string {
	out := append([]string(nil), xs...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// outLoads: passives hanging on an op-amp output — shunts to rails and a
// series R into a load node whose other elements are shunts (R-C isolation,
// ADC kickback filter).
func (c *circuit) outLoads(out string, taken map[string]bool) (refs []string, loadNets []string) {
	for _, e := range c.edges[out] {
		if taken[e.Ref] {
			continue
		}
		if c.isRail(e.Other) {
			refs = append(refs, e.Ref)
			continue
		}
		if e.Kind != "R" && e.Kind != "L" {
			continue
		}
		m := e.Other
		ok := true
		var sh []string
		for _, e2 := range c.edges[m] {
			if e2.Ref == e.Ref {
				continue
			}
			if !c.isRail(e2.Other) {
				ok = false
				break
			}
			sh = append(sh, e2.Ref)
		}
		if ok {
			refs = append(refs, e.Ref)
			refs = append(refs, sh...)
			loadNets = append(loadNets, m)
		}
	}
	return refs, loadNets
}

// pickStimulus chooses the signal-source port among ports.
func (c *circuit) pickStimulus(ports []string, exclude map[string]bool) (string, []string) {
	var cands []string
	for _, p := range ports {
		if c.isRail(p) || exclude[p] {
			continue
		}
		cands = append(cands, p)
	}
	if len(cands) <= 1 {
		if len(cands) == 1 {
			return cands[0], nil
		}
		return "", nil
	}
	score := func(n string) int {
		s := 0
		for _, pr := range c.netPins[n] {
			if _, ok := c.passive[pr.Ref]; ok {
				continue
			}
			if reConnRef.MatchString(pr.Ref) {
				s += 4
			}
			if r, _ := opampPinRole(pr.Name); r == "out" || reDrivePin.MatchString(pr.Name) {
				s += 3
			}
		}
		if regexp.MustCompile(`(?i)IN|SIG|SENS|AIN|MIC|SRC`).MatchString(n) {
			s++
		}
		return s
	}
	best, bs := cands[0], -1
	for _, n := range cands {
		if s := score(n); s > bs {
			best, bs = n, s
		}
	}
	var rest []string
	for _, n := range cands {
		if n != best {
			rest = append(rest, n)
		}
	}
	return best, rest
}

// opampBlock classifies one op-amp / comparator channel.
func (c *circuit) opampBlock(o *opampInst) (*Block, string) {
	b := newBlockCtx()
	b.opamps = []*opampInst{o}
	own := ownPins(o.Ref, o.Pins)
	blk := &Block{Core: o.id(), Channel: o.Ch, Output: o.Out, c: b}
	b.out = o.Out
	railV := func(n string) (float64, bool) { v, ok := c.railV[n]; return v, ok }
	vp, okp := railV(o.Vp)
	vn, okn := railV(o.Vn)
	if !okp || !okn {
		return nil, fmt.Sprintf("%s: supply pins not on known rails (%s / %s) — not analysed", o.id(), o.Vp, o.Vn)
	}
	b.rails[o.Vp], b.rails[o.Vn] = vp, vn
	if vp-vn < 0.5 {
		return nil, fmt.Sprintf("%s: supply span %.2f V — not analysed", o.id(), vp-vn)
	}
	fbR := c.between(o.Out, o.Inn, "R")
	fbC := c.between(o.Out, o.Inn, "C")
	stop := map[string]bool{o.Out: true}
	class, topo := "", ""
	var nin, nin2 string
	gainGuess := 1.0
	var eval func(map[string]float64) map[string]float64

	roleVals := func(v map[string]float64) map[string]float64 {
		out := map[string]float64{}
		for role, ref := range b.roleRef {
			out[role] = v[ref]
		}
		return out
	}
	withGain := func(n string) (rf, rg edge, ok bool) {
		if len(fbR) != 1 {
			return
		}
		for _, e := range c.edges[n] {
			if e.Kind == "R" && e.Ref != fbR[0].Ref && c.isRail(e.Other) {
				return fbR[0], e, true
			}
		}
		return
	}
	switch {
	case o.Comparator || (o.Inn != o.Out && len(fbR) == 0 && len(fbC) == 0 && !c.hasSeriesPath(o.Inn, o.Out, o.Inp)):
		class = ClassComparator
		topo = "open loop (no negative feedback)"
		hyst := c.between(o.Out, o.Inp, "R")
		if len(hyst) > 0 {
			b.setRole(hyst[0].Ref, "Rh")
			topo = "comparator with positive-feedback hysteresis (" + hyst[0].Ref + ")"
		}
		b.ramp = true
	case o.Inn == o.Out:
		if sk, ok := c.matchSK(o.Inp, o.Out); ok {
			b.setRole(sk.r1, "R1")
			b.setRole(sk.r2, "R2")
			b.setRole(sk.c1, "C1")
			b.setRole(sk.c2, "C2")
			nin = sk.nin
			stop[nin] = true
			if sk.lp {
				class, topo, eval = ClassSKLowPass, "unity-gain Sallen-Key low-pass", func(v map[string]float64) map[string]float64 { return skLowPass(roleVals(v)) }
				b.resp = "lp"
			} else {
				class, topo, eval = ClassSKHighPass, "unity-gain Sallen-Key high-pass", func(v map[string]float64) map[string]float64 { return skHighPass(roleVals(v)) }
				b.resp = "hp"
			}
		} else {
			class, topo = ClassFollower, "voltage follower (buffer)"
			b.resp = "amp"
		}
	default:
		rf, rg, gainNet := withGain(o.Inn)
		rin := c.inputEdges(o.Inn, o.Out, fbR, fbC)
		switch {
		case gainNet && len(rin) == 0:
			b.setRole(rf.Ref, "Rf")
			b.setRole(rg.Ref, "Rg")
			if len(fbC) > 0 {
				b.setRole(fbC[0].Ref, "Cf")
			}
			if sk, ok := c.matchSK(o.Inp, o.Out); ok {
				b.setRole(sk.r1, "R1")
				b.setRole(sk.r2, "R2")
				b.setRole(sk.c1, "C1")
				b.setRole(sk.c2, "C2")
				nin = sk.nin
				stop[nin] = true
				if sk.lp {
					class, topo, eval = ClassSKLowPass, "Sallen-Key low-pass with gain 1+Rf/Rg", func(v map[string]float64) map[string]float64 { return skLowPass(roleVals(v)) }
					b.resp = "lp"
				} else {
					class, topo, eval = ClassSKHighPass, "Sallen-Key high-pass with gain 1+Rf/Rg", func(v map[string]float64) map[string]float64 { return skHighPass(roleVals(v)) }
					b.resp = "hp"
				}
			} else {
				class, topo = ClassNonInverting, "non-inverting amplifier, gain 1+Rf/Rg"
				gainGuess = 1 + rf.Value/rg.Value
				b.resp = "amp"
			}
		case len(fbC) == 1 && len(fbR) == 0 && len(rin) == 1 && rin[0].Kind == "R" && c.isMFBLowPass(o, rin[0], fbC[0], b):
			nin = b.roleRef["_nin"]
			delete(b.roleRef, "_nin")
			stop[nin] = true
			class, topo, eval = ClassMFBLowPass, "multiple-feedback low-pass (inverting)", func(v map[string]float64) map[string]float64 { return mfbLowPass(roleVals(v)) }
			b.resp = "lp"
		case len(fbR) == 1 && len(rin) == 1 && rin[0].Kind == "C" && c.isMFBHighPass(o, rin[0], fbR[0], b):
			nin = b.roleRef["_nin"]
			delete(b.roleRef, "_nin")
			stop[nin] = true
			class, topo, eval = ClassMFBHighPass, "multiple-feedback high-pass (inverting)", func(v map[string]float64) map[string]float64 { return mfbHighPass(roleVals(v)) }
			b.resp = "hp"
		case len(fbR) == 1 && len(rin) == 1 && rin[0].Kind == "R":
			b.setRole(fbR[0].Ref, "Rf")
			b.setRole(rin[0].Ref, "Rin")
			if len(fbC) > 0 {
				b.setRole(fbC[0].Ref, "Cf")
			}
			nin = rin[0].Other
			stop[nin] = true
			gainGuess = fbR[0].Value / rin[0].Value
			// Difference amplifier: +in fed through R3 from a signal net and R4 to a rail.
			var r3, r4 *edge
			for _, e := range c.edges[o.Inp] {
				e := e
				if e.Kind != "R" {
					continue
				}
				if c.isRail(e.Other) {
					r4 = &e
				} else if e.Other != o.Out {
					r3 = &e
				}
			}
			if r3 != nil && r4 != nil {
				b.setRole(r3.Ref, "R3")
				b.setRole(r4.Ref, "R4")
				nin2 = r3.Other
				stop[nin2] = true
				class, topo = ClassDifference, "difference amplifier, gain Rf/Rin (R4/R3 matched)"
				b.resp = "amp"
			} else {
				class, topo = ClassInverting, "inverting amplifier, gain −Rf/Rin"
				if len(fbC) > 0 {
					topo += " with Cf (first-order low-pass)"
					b.resp = "lp"
				} else {
					b.resp = "amp"
				}
			}
		case len(fbC) == 1 && len(fbR) == 0 && len(rin) == 1 && rin[0].Kind == "R":
			b.setRole(fbC[0].Ref, "Cf")
			b.setRole(rin[0].Ref, "Rin")
			nin = rin[0].Other
			stop[nin] = true
			class, topo = ClassIntegrator, "inverting integrator"
			b.resp = "lp"
		default:
			class, topo = ClassOpampOther, "op-amp with an unrecognised feedback network"
			b.resp = "amp"
		}
	}
	blk.Class, blk.Topology = class, topo
	// Region (passives of the block) and ports.
	parts, nets, ports := c.regionOwn([]string{o.Inp, o.Inn}, own, stop)
	taken := map[string]bool{}
	for _, p := range parts {
		taken[p] = true
	}
	loads, loadNets := c.outLoads(o.Out, taken)
	b.addPassives(parts...)
	b.addPassives(loads...)
	nets = append(nets, o.Out)
	nets = append(nets, loadNets...)
	for _, ln := range loadNets {
		if a := c.adcAt(ln); a != nil {
			b.adc = a
		}
	}
	if a := c.adcAt(o.Out); a != nil && b.adc == nil {
		b.adc = a
	}
	for _, p := range ports {
		if v, ok := c.railV[p]; ok {
			b.rails[p] = v
		}
	}
	exclude := map[string]bool{o.Out: true}
	if nin == "" {
		var rest []string
		nin, rest = c.pickStimulus(ports, exclude)
		b.openPorts = append(b.openPorts, rest...)
	} else {
		for _, p := range ports {
			if !c.isRail(p) && p != nin && p != nin2 && p != o.Out {
				b.openPorts = append(b.openPorts, p)
			}
		}
	}
	for _, ln := range loadNets {
		for _, pr := range c.nonPassivePins(ln) {
			_ = pr
		}
	}
	b.stim, b.stim2 = nin, nin2
	blk.Input, blk.Input2 = nin, nin2
	blk.Nets = uniq(append(nets, ports...))
	blk.Parts = sortRefs(append([]string{o.Ref}, b.passives...))
	b.gainGuess = gainGuess
	if eval == nil {
		eval = c.genericAmpEval(o, b, class)
	}
	b.eval = eval
	// Analyses.
	switch class {
	case ClassComparator:
		b.ramp = nin != ""
		b.op = true
	default:
		b.op = true
		b.dcSweep = nin != "" && b.resp != "hp"
		b.ac = nin != ""
		b.step = nin != ""
		b.loop = o.Vendor == nil
		b.mc = nin != "" && len(b.passives) > 0
	}
	b.mcMetrics = []string{"gain", "fcHz"}
	if b.resp == "lp" || b.resp == "hp" {
		if class == ClassSKLowPass || class == ClassSKHighPass || class == ClassMFBLowPass || class == ClassMFBHighPass {
			b.mcMetrics = []string{"gain", "fcHz", "f0Hz", "q"}
		}
	}
	blk.Title = titleFor(class, o.id(), nin, o.Out)
	return blk, ""
}

// hasSeriesPath: a passive path from a to b not through c (MFB detection guard).
func (c *circuit) hasSeriesPath(a, b, avoid string) bool {
	for _, e := range c.edges[a] {
		if e.Other == b {
			return true
		}
		if c.isRail(e.Other) || e.Other == avoid {
			continue
		}
		for _, e2 := range c.edges[e.Other] {
			if e2.Other == b && e2.Ref != e.Ref {
				return true
			}
		}
	}
	return false
}

// inputEdges: passives at the inverting input other than feedback and gain-to-rail.
func (c *circuit) inputEdges(n, out string, fbR, fbC []edge) []edge {
	fb := map[string]bool{}
	for _, e := range fbR {
		fb[e.Ref] = true
	}
	for _, e := range fbC {
		fb[e.Ref] = true
	}
	var in []edge
	for _, e := range c.edges[n] {
		if fb[e.Ref] || c.isRail(e.Other) || e.Other == out {
			continue
		}
		in = append(in, e)
	}
	return in
}

type skMatch struct {
	lp                     bool
	r1, r2, c1, c2, nin, x string
}

// matchSK recognises the Sallen-Key input network at +in (p).
func (c *circuit) matchSK(p, out string) (skMatch, bool) {
	for _, e := range c.edges[p] {
		x := e.Other
		if c.isRail(x) || x == out {
			continue
		}
		switch e.Kind {
		case "R": // low-pass: R2 p–X, C2 p–gnd, C1 X–out, R1 Nin–X
			var c2 string
			for _, g := range c.edges[p] {
				if g.Kind == "C" && c.isRail(g.Other) {
					c2 = g.Ref
				}
			}
			if c2 == "" {
				continue
			}
			var c1, r1, nin string
			for _, xe := range c.edges[x] {
				if xe.Ref == e.Ref {
					continue
				}
				if xe.Kind == "C" && xe.Other == out {
					c1 = xe.Ref
				}
				if xe.Kind == "R" && xe.Other != p && xe.Other != out && !c.isRail(xe.Other) {
					r1, nin = xe.Ref, xe.Other
				}
			}
			if c1 != "" && r1 != "" {
				return skMatch{lp: true, r1: r1, r2: e.Ref, c1: c1, c2: e.Ref, nin: nin, x: x}.withC2(c2), true
			}
		case "C": // high-pass: C2 p–X, R2 p–ref, R1 X–out, C1 Nin–X
			var r2 string
			for _, g := range c.edges[p] {
				if g.Kind == "R" && g.Other != x {
					r2 = g.Ref
				}
			}
			if r2 == "" {
				continue
			}
			var r1, c1, nin string
			for _, xe := range c.edges[x] {
				if xe.Ref == e.Ref {
					continue
				}
				if xe.Kind == "R" && xe.Other == out {
					r1 = xe.Ref
				}
				if xe.Kind == "C" && xe.Other != p && xe.Other != out && !c.isRail(xe.Other) {
					c1, nin = xe.Ref, xe.Other
				}
			}
			if r1 != "" && c1 != "" {
				return skMatch{lp: false, r1: r1, r2: r2, c1: c1, c2: e.Ref, nin: nin, x: x}, true
			}
		}
	}
	return skMatch{}, false
}

func (m skMatch) withC2(c2 string) skMatch { m.c2 = c2; return m }

// isMFBLowPass: -in has R3 to X and C2 to out; X has R2 to out, C1 to rail, R1 to Nin.
func (c *circuit) isMFBLowPass(o *opampInst, r3 edge, c2 edge, b *blockCtx) bool {
	x := r3.Other
	var r2, c1, r1, nin string
	for _, e := range c.edges[x] {
		switch {
		case e.Ref == r3.Ref:
		case e.Kind == "R" && e.Other == o.Out:
			r2 = e.Ref
		case e.Kind == "C" && c.isRail(e.Other):
			c1 = e.Ref
		case e.Kind == "R" && !c.isRail(e.Other) && e.Other != o.Inn:
			r1, nin = e.Ref, e.Other
		}
	}
	if r2 == "" || c1 == "" || r1 == "" {
		return false
	}
	b.setRole(r1, "R1")
	b.setRole(r2, "R2")
	b.setRole(r3.Ref, "R3")
	b.setRole(c1, "C1")
	b.setRole(c2.Ref, "C2")
	b.roleRef["_nin"] = nin
	return true
}

// isMFBHighPass: -in has C3 to X and R2 to out; X has C2 to out, R1 to rail, C1 to Nin.
func (c *circuit) isMFBHighPass(o *opampInst, c3 edge, r2 edge, b *blockCtx) bool {
	x := c3.Other
	var c2, r1, c1, nin string
	for _, e := range c.edges[x] {
		switch {
		case e.Ref == c3.Ref:
		case e.Kind == "C" && e.Other == o.Out:
			c2 = e.Ref
		case e.Kind == "R" && c.isRail(e.Other):
			r1 = e.Ref
		case e.Kind == "C" && !c.isRail(e.Other) && e.Other != o.Inn:
			c1, nin = e.Ref, e.Other
		}
	}
	if c2 == "" || r1 == "" || c1 == "" {
		return false
	}
	b.setRole(c1, "C1")
	b.setRole(c2, "C2")
	b.setRole(c3.Ref, "C3")
	b.setRole(r1, "R1")
	b.setRole(r2.Ref, "R2")
	b.roleRef["_nin"] = nin
	return true
}

// genericAmpEval: DC gain from the resistor network (input network × amplifier
// gain), bandwidth from GBW and the dominant input RC.
func (c *circuit) genericAmpEval(o *opampInst, b *blockCtx, class string) func(map[string]float64) map[string]float64 {
	return func(v map[string]float64) map[string]float64 {
		out := map[string]float64{}
		if b.stim == "" {
			return out
		}
		fixed := map[string]float64{}
		for n := range b.rails {
			fixed[n] = 0
		}
		for _, n := range b.openPorts {
			_ = n
		}
		gbw := o.Model.GBWHz
		switch class {
		case ClassFollower, ClassNonInverting, ClassOpampOther:
			fixed[b.stim] = 1
			if o.Inp != b.stim {
				delete(fixed, o.Inp)
			}
			vs := c.dcSolve(b.passives, v, fixed)
			gin := vs[o.Inp]
			k := 1.0
			if rf, rg := v[b.roleRef["Rf"]], v[b.roleRef["Rg"]]; rf > 0 && rg > 0 {
				k = 1 + rf/rg
			}
			out["gain"] = gin * k
			bw := gbwBandwidth(gbw, k)
			// Input RC pole at +in.
			fixedT := map[string]float64{b.stim: 0}
			for n := range b.rails {
				fixedT[n] = 0
			}
			var rails []string
			for n := range fixedT {
				rails = append(rails, n)
			}
			if cp := c.capTo(b.passives, v, o.Inp); cp > 0 {
				rth := c.theveninR(b.passives, v, o.Inp, rails)
				if !math.IsInf(rth, 0) {
					fin := 1 / (twoPi() * rth * cp)
					out["inputPoleHz"] = fin
					bw = 1 / math.Sqrt(1/(bw*bw)+1/(fin*fin))
				}
			}
			if cf := v[b.roleRef["Cf"]]; cf > 0 && v[b.roleRef["Rf"]] > 0 {
				fcf := 1 / (twoPi() * v[b.roleRef["Rf"]] * cf)
				out["feedbackPoleHz"] = fcf
				bw = 1 / math.Sqrt(1/(bw*bw)+1/(fcf*fcf))
			}
			out["fcHz"] = bw
		case ClassInverting, ClassDifference:
			rf, rin := v[b.roleRef["Rf"]], v[b.roleRef["Rin"]]
			if rf > 0 && rin > 0 {
				out["gain"] = rf / rin
				bw := gbwBandwidth(gbw, 1+rf/rin)
				if cf := v[b.roleRef["Cf"]]; cf > 0 {
					fcf := 1 / (twoPi() * rf * cf)
					out["feedbackPoleHz"] = fcf
					bw = 1 / math.Sqrt(1/(bw*bw)+1/(fcf*fcf))
				}
				out["fcHz"] = bw
			}
			if class == ClassDifference {
				r3, r4 := v[b.roleRef["R3"]], v[b.roleRef["R4"]]
				if r3 > 0 && r4 > 0 && rin > 0 && rf > 0 {
					out["ratioMismatchPct"] = (r4/r3/(rf/rin) - 1) * 100
				}
			}
		case ClassIntegrator:
			rin, cf := v[b.roleRef["Rin"]], v[b.roleRef["Cf"]]
			if rin > 0 && cf > 0 {
				out["unityHz"] = 1 / (twoPi() * rin * cf)
			}
		case ClassComparator:
			th := c.comparatorThresholds(o, b, v)
			for k, x := range th {
				out[k] = x
			}
		}
		return out
	}
}

// comparatorThresholds solves the switching thresholds of the signal input
// with the output at its low and high level (resistor network, caps open).
func (c *circuit) comparatorThresholds(o *opampInst, b *blockCtx, v map[string]float64) map[string]float64 {
	out := map[string]float64{}
	if b.stim == "" {
		return out
	}
	vp, vn := b.rails[o.Vp], b.rails[o.Vn]
	voh, vol := vp-o.Model.HeadroomHighV, vn+o.Model.HeadroomLowV
	if o.OpenDrain {
		vol = vn + 0.01 // saturated output switch into the pull-up
	}
	// Solve V(+in) - V(-in) as a function of the stimulus; find the zero by bisection.
	diff := func(vs float64, outHigh bool) float64 {
		fixed := map[string]float64{}
		for n, x := range b.rails {
			fixed[n] = x
		}
		fixed[b.stim] = vs
		if outHigh {
			if !o.OpenDrain {
				fixed[o.Out] = voh
			}
		} else {
			fixed[o.Out] = vol
		}
		sol := c.dcSolve(b.passives, v, fixed)
		a, ok1 := sol[o.Inp]
		bb, ok2 := sol[o.Inn]
		if !ok1 {
			a = fixed[o.Inp]
		}
		if !ok2 {
			bb = fixed[o.Inn]
		}
		return a - bb
	}
	lo, hi := vn-1, vp+1
	for _, st := range []struct {
		name string
		high bool
	}{{"thresholdLowOutV", false}, {"thresholdHighOutV", true}} {
		a, bb := lo, hi
		fa := diff(a, st.high)
		fb := diff(bb, st.high)
		if fa*fb > 0 {
			continue
		}
		for i := 0; i < 60; i++ {
			m := (a + bb) / 2
			fm := diff(m, st.high)
			if fm*fa <= 0 {
				bb = m
			} else {
				a, fa = m, fm
			}
		}
		out[st.name] = (a + bb) / 2
	}
	if x, ok1 := out["thresholdLowOutV"]; ok1 {
		if y, ok2 := out["thresholdHighOutV"]; ok2 {
			out["hysteresisV"] = math.Abs(x - y)
		}
	}
	return out
}

func titleFor(class, core, in, out string) string {
	zh := map[string]string{
		ClassFollower: "电压跟随器", ClassNonInverting: "同相放大", ClassInverting: "反相放大", ClassDifference: "差分放大",
		ClassIntegrator: "积分器", ClassSKLowPass: "Sallen-Key 低通", ClassSKHighPass: "Sallen-Key 高通", ClassMFBLowPass: "MFB 低通",
		ClassMFBHighPass: "MFB 高通", ClassComparator: "比较器", ClassInstrument: "仪表放大器", ClassCurrentSense: "电流检测放大器",
		ClassRCLowPass: "RC 低通", ClassLCFilter: "LC 滤波", ClassADCInput: "ADC 输入采样",
		ClassReference: "电压基准", ClassRegulatorFB: "稳压器反馈分压", ClassCrystal: "晶振负载电容", ClassTransistorSw: "晶体管开关",
		ClassResetRC: "复位 RC 延时", ClassOpampOther: "运放（未识别拓扑）", ClassLevelShifter: "MOSFET 电平转换",
	}[class]
	s := zh + " " + core
	if in != "" || out != "" {
		s += "（" + strings.Trim(in+" → "+out, " →") + "）"
	}
	return s
}
