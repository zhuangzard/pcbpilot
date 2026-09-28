package powersim

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Generator is written into every output.
const Generator = "pcbpilot sim power"

// Options selects scenarios and overrides.
type Options struct {
	// Scenarios restricts the run (names from ScenarioNames); empty = all.
	Scenarios []string
	// Switches forces a switch state by ref (true = closed) in every scenario.
	Switches map[string]bool
}

// Engine binds models to a design and runs scenarios.
type Engine struct {
	d           *Design
	libs        Libraries
	def         Defaults
	opt         Options
	binds       []*binding
	byRef       map[string]*binding
	sources     []*binding
	warnings    []string
	assumptions []string
	runs        map[string]*run
}

// NewEngine binds models for d.
func NewEngine(d *Design, libs Libraries, opt Options) *Engine {
	e := &Engine{d: d, libs: libs, def: libs.defaults(), opt: opt, runs: map[string]*run{}}
	if len(libs) == 0 {
		e.warnf("no power-model library loaded — every IC uses the generic assumed load")
	}
	e.bindAll()
	return e
}

type scenario struct {
	name, desc string
	peak       bool
	buttons    bool   // momentary switches pressed
	only       string // source name, "" = all sources on
}

// ScenarioNames lists the scenarios this design supports (without "worst").
func (e *Engine) ScenarioNames() []string {
	var out []string
	for _, s := range e.scenarios() {
		out = append(out, s.name)
	}
	return out
}

func (e *Engine) scenarios() []scenario {
	out := []scenario{
		{name: "typical", desc: "typical loads, every input source on, momentary switches open"},
		{name: "peak", desc: "peak loads, every input source on, momentary switches released", peak: true},
	}
	for _, b := range e.binds {
		if b.kind != KindSwitch {
			continue
		}
		if _, forced := e.opt.Switches[b.part.Ref]; forced {
			continue
		}
		if b.model.Momentary == nil || *b.model.Momentary {
			out = append(out, scenario{name: "buttons-pressed", desc: "typical loads, every input source on, every momentary switch pressed (pull-up currents)", buttons: true})
			break
		}
	}
	if len(e.sources) > 1 {
		for _, s := range e.sources {
			out = append(out, scenario{name: s.sourceName + "-only", desc: fmt.Sprintf("peak loads, only %s (%s) supplies the board", s.part.Ref, s.sourceName), peak: true, only: s.sourceName})
		}
	}
	return out
}

// run is one solved scenario.
type run struct {
	sc     scenario
	c      *Circuit
	sol    *Solution
	iters  int
	conv   bool
	err    error
	owner  map[element]*binding
	regs   map[string]*regulator
	pinI   map[PinRef]float64
	warn   []string
	assume []string
	extra  map[element]string // element label for SPICE comments
}

func (r *run) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, w := range r.warn {
		if w == msg {
			return
		}
	}
	r.warn = append(r.warn, msg)
}

func (r *run) assumef(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, w := range r.assume {
		if w == msg {
			return
		}
	}
	r.assume = append(r.assume, msg)
}

func (e *Engine) node(c *Circuit, net string) int {
	if e.isGround(net) {
		return Ground
	}
	return c.Node(net)
}

func pinRefs(ref string, pins []Pin) []PinRef {
	out := make([]PinRef, 0, len(pins))
	for _, p := range pins {
		out = append(out, PinRef{Ref: ref, Pin: p.Number})
	}
	return out
}

// term builds a terminal for same-net pins; ok=false when empty, unconnected
// or spread over several (non-ground) nets.
func (e *Engine) term(c *Circuit, ref string, pins []Pin) (Terminal, bool) {
	if len(pins) == 0 {
		return Terminal{}, false
	}
	net := pins[0].Net
	for _, p := range pins {
		if p.Net == "" {
			return Terminal{}, false
		}
		if p.Net != net && !(e.isGround(p.Net) && e.isGround(net)) {
			return Terminal{}, false
		}
	}
	return Terminal{Node: e.node(c, net), Pins: pinRefs(ref, pins)}, true
}

func byNet(pins []Pin) ([]string, map[string][]Pin) {
	m := map[string][]Pin{}
	var order []string
	for _, p := range pins {
		if p.Net == "" {
			continue
		}
		if _, ok := m[p.Net]; !ok {
			order = append(order, p.Net)
		}
		m[p.Net] = append(m[p.Net], p)
	}
	return order, m
}

func (e *Engine) switchClosed(b *binding, sc scenario) (bool, string) {
	if v, ok := e.opt.Switches[b.part.Ref]; ok {
		return v, "--switch override"
	}
	momentary := true
	if b.model.Momentary != nil {
		momentary = *b.model.Momentary
	}
	if !momentary {
		return true, "latching switch assumed closed (on)"
	}
	if sc.buttons {
		return true, "momentary switch pressed in " + sc.name
	}
	return false, "momentary switch released in " + sc.name
}

func (e *Engine) loadKnee(net string) float64 {
	k := e.def.LoadKneeV
	if v := pcbauto.InferVoltage(net); v > 0 && 0.6*v < k {
		k = 0.6 * v
	}
	return k
}

// build constructs the circuit of one scenario.
func (e *Engine) build(sc scenario) *run {
	c := NewCircuit()
	r := &run{sc: sc, c: c, owner: map[element]*binding{}, regs: map[string]*regulator{}, extra: map[element]string{}}
	add := func(b *binding, el element, label string) {
		c.add(el)
		r.owner[el] = b
		r.extra[el] = label
	}
	two := func(b *binding) (Terminal, Terminal, bool) {
		p := b.part
		if len(p.Pins) != 2 {
			return Terminal{}, Terminal{}, false
		}
		t0, ok0 := e.term(c, p.Ref, p.Pins[:1])
		t1, ok1 := e.term(c, p.Ref, p.Pins[1:])
		return t0, t1, ok0 && ok1
	}
	for _, b := range e.binds {
		p, m := b.part, b.model
		switch b.kind {
		case KindResistor:
			t0, t1, ok := two(b)
			if !ok {
				continue
			}
			v, found := partValue(p, "resistance")
			if !found {
				r.warnf("%s: resistor value %q not parseable — left open", p.Ref, p.Value)
				continue
			}
			if v <= 0 {
				v = e.def.ZeroOhm
			}
			add(b, &resistor{t: [2]Terminal{t0, t1}, r: v}, fmt.Sprintf("%s %g", p.Ref, v))
		case KindInductor, KindFerrite, KindFuse:
			t0, t1, ok := two(b)
			if !ok {
				continue
			}
			rr := m.DCROhm
			if rr == 0 {
				rr = m.RsOhm
			}
			if rr == 0 && b.kind == KindInductor {
				if v, ok := valueFromDescription(p.Description, "dcr"); ok && v > 0 {
					rr = v
					e.assumef("%s: DCR %.3g Ω from the part description", p.Ref, rr)
				}
			}
			if rr == 0 {
				switch b.kind {
				case KindInductor:
					rr = e.def.InductorDCROhm
				case KindFerrite:
					rr = e.def.FerriteOhm
				default:
					rr = e.def.FuseOhm
				}
				e.assumef("%s: %s DCR %.3g Ω assumed (no model)", p.Ref, b.kind, rr)
			}
			add(b, &resistor{t: [2]Terminal{t0, t1}, r: rr}, fmt.Sprintf("%s DCR", p.Ref))
		case KindSwitch:
			nets, groups := byNet(p.Pins)
			if len(nets) != 2 {
				continue
			}
			closed, why := e.switchClosed(b, sc)
			if closed {
				r.assumef("%s closed: %s", p.Ref, why)
			} else {
				if strings.HasPrefix(why, "--switch") {
					r.assumef("%s open: %s", p.Ref, why)
				} else {
					e.assumef("momentary switches are released (open) except in buttons-pressed")
				}
				continue
			}
			t0, ok0 := e.term(c, p.Ref, groups[nets[0]])
			t1, ok1 := e.term(c, p.Ref, groups[nets[1]])
			if !ok0 || !ok1 {
				continue
			}
			ron := m.RonOhm
			if ron == 0 {
				ron = e.def.SwitchRonOhm
			}
			add(b, &resistor{t: [2]Terminal{t0, t1}, r: ron}, p.Ref+" closed")
		case KindDiode, KindLED:
			an := p.pinsNamed(roleNames(m, "anode", defAnode...))
			ca := p.pinsNamed(roleNames(m, "cathode", defCathode...))
			if len(an) == 0 || len(ca) == 0 {
				if len(p.Pins) == 2 {
					// SMA/SOD convention: pin 1 = cathode.
					ca, an = p.Pins[:1], p.Pins[1:]
					e.assumef("%s: pin 1 taken as cathode (no A/K pin names)", p.Ref)
				} else {
					r.warnf("%s: cannot identify anode/cathode — skipped", p.Ref)
					continue
				}
			}
			ta, oka := e.term(c, p.Ref, an)
			tk, okk := e.term(c, p.Ref, ca)
			if !oka || !okk {
				continue
			}
			var is, n float64
			if b.kind == KindLED {
				vf := m.VfV
				ifA := m.IfA
				if ifA == 0 {
					ifA = e.def.LEDIfA
				}
				if vf == 0 && len(m.VfPoints) == 0 {
					color, how := ledColor(m, p)
					if v, ok := e.def.LEDVf[color]; ok {
						vf = v
						e.assumef("%s: %s LED (colour from %s) Vf %.2f V @ %.0f mA", p.Ref, color, how, vf, ifA*1e3)
					} else {
						vf = 2.0
						e.warnf("%s: LED colour unknown — Vf 2.0 V @ %.0f mA assumed", p.Ref, ifA*1e3)
					}
				}
				is, n = diodeParams(m, vf, ifA, e.def.LEDN)
			} else {
				vf, ifA := m.VfV, m.IfA
				if vf == 0 {
					vf, ifA = e.def.DiodeVfV, e.def.DiodeIfA
				}
				if ifA == 0 {
					ifA = e.def.DiodeIfA
				}
				is, n = diodeParams(m, vf, ifA, 1.5)
			}
			anode := ta
			if m.RsOhm > 0 {
				mid := c.Internal(p.Ref + ".rs")
				add(b, &resistor{t: [2]Terminal{ta, {Node: mid}}, r: m.RsOhm}, p.Ref+" Rs")
				anode = Terminal{Node: mid}
			}
			add(b, &diode{t: [2]Terminal{anode, tk}, is: is, n: n}, p.Ref)
		case KindBridge:
			// Four diodes: each AC pin → +, − → each AC pin.
			ac := p.pinsNamed(roleNames(m, "ac", defBridgeAC...))
			pos := p.pinsNamed(roleNames(m, "plus", defBridgePlus...))
			neg := p.pinsNamed(roleNames(m, "minus", defBridgeMinus...))
			tp, okp := e.term(c, p.Ref, pos)
			tn, okn := e.term(c, p.Ref, neg)
			if len(ac) == 0 || !okp || !okn {
				r.warnf("%s: bridge rectifier pins ~/+/− not identified (ac=%d +=%d −=%d) — left open", p.Ref, len(ac), len(pos), len(neg))
				continue
			}
			vf, ifA := m.VfV, m.IfA
			if vf == 0 {
				vf, ifA = e.def.DiodeVfV, e.def.DiodeIfA
			}
			if ifA == 0 {
				ifA = e.def.DiodeIfA
			}
			is, n := diodeParams(m, vf, ifA, 1.5)
			for _, pin := range ac {
				ta, ok := e.term(c, p.Ref, []Pin{pin})
				if !ok {
					continue
				}
				add(b, &diode{t: [2]Terminal{ta, tp}, is: is, n: n}, p.Ref+" "+pin.Name+"→+")
				add(b, &diode{t: [2]Terminal{tn, ta}, is: is, n: n}, p.Ref+" −→"+pin.Name)
			}
		case KindBJT:
			pb := p.pinsNamed(roleNames(m, "b", "B", "BASE"))
			pc := p.pinsNamed(roleNames(m, "c", "C", "COLLECTOR"))
			pe := p.pinsNamed(roleNames(m, "e", "E", "EMITTER"))
			if len(pb) == 0 || len(pc) == 0 || len(pe) == 0 {
				if len(p.Pins) == 3 {
					pb, pe, pc = p.pinsNamed([]string{"1"}), p.pinsNamed([]string{"2"}), p.pinsNamed([]string{"3"})
					e.assumef("%s: SOT-23 pinout 1=B 2=E 3=C assumed", p.Ref)
				}
			}
			tb, okb := e.term(c, p.Ref, pb)
			tc, okc := e.term(c, p.Ref, pc)
			te, oke := e.term(c, p.Ref, pe)
			if !okb || !okc || !oke {
				continue
			}
			beta := m.Beta
			if beta == 0 {
				beta = e.def.BJTBeta
			}
			br := m.BetaR
			if br == 0 {
				br = 1
			}
			is := m.IsA
			if is == 0 {
				is = 1e-14
			}
			add(b, &bjt{t: [3]Terminal{tc, tb, te}, pnp: strings.EqualFold(m.Polarity, "pnp"), is: is, bf: beta, br: br}, p.Ref)
		case KindESD:
			sup := p.pinsNamed(roleNames(m, "line", m.SupplyPins...))
			if len(sup) == 0 {
				sup = p.pinsNamed(defCathode)
			}
			ret := p.pinsNamed(m.ReturnPins)
			if len(ret) == 0 {
				ret = e.groundPins(p)
			}
			if len(ret) == 0 {
				ret = p.pinsNamed(defAnode)
			}
			leak := m.LeakTypA
			if sc.peak && m.LeakPeakA > 0 {
				leak = m.LeakPeakA
			}
			if leak == 0 {
				leak = e.def.ESDLeakA
			}
			tr, okr := e.term(c, p.Ref, ret)
			nets, groups := byNet(sup)
			for _, net := range nets {
				if e.isGround(net) {
					continue
				}
				ts, ok := e.term(c, p.Ref, groups[net])
				if !ok || !okr {
					continue
				}
				add(b, &load{t: [2]Terminal{ts, tr}, inom: leak, knee: e.loadKnee(net)}, p.Ref+" leakage")
			}
		case KindLoad:
			e.buildLoad(r, b, sc, add)
		case KindLDO, KindBuck:
			e.buildRegulator(r, b, add)
		case KindSource:
			if sc.only != "" && sc.only != b.sourceName {
				r.assumef("%s (%s) disconnected in %s", p.Ref, b.sourceName, sc.name)
				continue
			}
			if len(m.Outputs) > 0 {
				for i, o := range m.Outputs {
					name := o.Name
					if name == "" {
						name = fmt.Sprintf("output %d", i+1)
					}
					ts, oks := e.term(c, p.Ref, p.pinsNamed(o.Pins))
					tr, okr := e.term(c, p.Ref, p.pinsNamed(o.ReturnPins))
					if !oks || !okr || o.VoltageV == 0 {
						r.warnf("%s: source %s pins %v/%v not connected or no voltage — not used", p.Ref, name, o.Pins, o.ReturnPins)
						continue
					}
					rs := o.RsOhm
					if rs == 0 {
						rs = e.def.SourceRsOhm
					}
					e.assumef("%s: %s = %.2f V (model %s) with %.3g Ω series resistance", p.Ref, name, o.VoltageV, m.ID, rs)
					add(b, &vsource{t: [2]Terminal{ts, tr}, e: o.VoltageV, rs: rs, active: true}, p.Ref+" "+name)
				}
				// An isolated module draws its input power from the other
				// side: rails[] are its input load (returnPins = input return).
				if len(m.Rails) > 0 {
					e.buildLoad(r, b, sc, add)
				}
				continue
			}
			power := p.pinsNamed(roleNames(m, "power", m.SupplyPins...))
			if len(power) == 0 {
				for _, pin := range p.Pins {
					if e.powerLike(pin.Net) {
						power = append(power, pin)
					}
				}
			}
			ret := p.pinsNamed(m.ReturnPins)
			if len(ret) == 0 {
				ret = e.groundPins(p)
			}
			tr, okr := e.term(c, p.Ref, ret)
			if !okr {
				r.warnf("%s: supply connector has no ground pin — not used as a source", p.Ref)
				continue
			}
			nets, groups := byNet(power)
			for _, net := range nets {
				if e.isGround(net) {
					continue
				}
				ts, ok := e.term(c, p.Ref, groups[net])
				if !ok {
					continue
				}
				v := m.VoltageV
				how := "model"
				if v == 0 {
					if v = pcbauto.InferVoltage(net); v > 0 {
						how = "net name"
					} else {
						v, how = e.def.SourceVoltageV, "default (assumed)"
					}
				}
				rs := m.RsOhm
				if rs == 0 {
					rs = e.def.SourceRsOhm
				}
				e.assumef("%s: input source %s = %.2f V (%s) with %.3g Ω series resistance", p.Ref, net, v, how, rs)
				add(b, &vsource{t: [2]Terminal{ts, tr}, e: v, rs: rs, active: true}, p.Ref+" source")
			}
		}
	}
	e.buildGPIODrives(r, add)
	return r
}

// supplyGroups returns the load's supply pins grouped by net with the current
// for each group.
func (e *Engine) buildLoad(r *run, b *binding, sc scenario, add func(*binding, element, string)) {
	p, m := b.part, b.model
	ret := p.pinsNamed(m.ReturnPins)
	if len(ret) == 0 {
		ret = e.groundPins(p)
	}
	tr, okr := e.term(r.c, p.Ref, ret)
	type group struct {
		pins []Pin
		amps float64
		ret  []string
	}
	var groups []group
	pick := func(typ, peak float64) float64 {
		if sc.peak && peak > 0 {
			return peak
		}
		if typ == 0 {
			return peak
		}
		return typ
	}
	if len(m.Rails) > 0 {
		for _, rail := range m.Rails {
			groups = append(groups, group{pins: p.pinsNamed(rail.Pins), amps: pick(rail.TypA, rail.PeakA), ret: rail.ReturnPins})
		}
	} else {
		var sup []Pin
		if len(m.SupplyPins) > 0 {
			sup = p.pinsNamed(m.SupplyPins)
		} else {
			for _, pin := range p.Pins {
				if e.powerLike(pin.Net) {
					sup = append(sup, pin)
				}
			}
		}
		total := pick(m.TypA, m.PeakA)
		if total == 0 {
			total = e.def.UnknownICLoadA * float64(len(sup))
			if len(sup) > 0 {
				e.warnf("%s (%s): no power model — assumed %.0f mA per supply pin (%d pins, %.0f mA total); add it to power-models.json", p.Ref, firstNonEmpty(p.MPN, p.Value, p.DeviceName, "unknown part"), e.def.UnknownICLoadA*1e3, len(sup), total*1e3)
			}
		}
		nets, byN := byNet(sup)
		n := 0
		for _, net := range nets {
			n += len(byN[net])
		}
		for _, net := range nets {
			groups = append(groups, group{pins: byN[net], amps: total * float64(len(byN[net])) / float64(n)})
		}
		if len(nets) > 1 {
			e.assumef("%s: load split across rails %s by supply-pin count", p.Ref, strings.Join(nets, ", "))
		}
	}
	if len(groups) == 0 {
		if b.kind == KindLoad && b.matched != nil {
			r.warnf("%s: no supply pin found for model %s", p.Ref, m.ID)
		}
		return
	}
	perRail := false
	for _, g := range groups {
		perRail = perRail || len(g.ret) > 0
	}
	if !okr && !perRail {
		r.warnf("%s: load has no ground/return pin — skipped", p.Ref)
		return
	}
	for _, g := range groups {
		if len(g.pins) == 0 || g.amps == 0 {
			continue
		}
		ts, ok := e.term(r.c, p.Ref, g.pins)
		if !ok || e.isGround(g.pins[0].Net) {
			continue
		}
		gr, okg := tr, okr
		if len(g.ret) > 0 {
			gr, okg = e.term(r.c, p.Ref, p.pinsNamed(g.ret))
		}
		if !okg {
			r.warnf("%s: rail %s has no return pin — skipped", p.Ref, g.pins[0].Net)
			continue
		}
		add(b, &load{t: [2]Terminal{ts, gr}, inom: g.amps, knee: e.loadKnee(g.pins[0].Net)}, fmt.Sprintf("%s load %s", p.Ref, g.pins[0].Net))
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func (e *Engine) buildRegulator(r *run, b *binding, add func(*binding, element, string)) {
	p, m := b.part, b.model
	c := r.c
	buck := b.kind == KindBuck
	in := p.pinsNamed(roleNames(m, "in", defIn...))
	var out []Pin
	if buck {
		out = p.pinsNamed(roleNames(m, "out", defLX...))
	} else {
		out = p.pinsNamed(roleNames(m, "out", defLDOOut...))
	}
	gnd := p.pinsNamed(roleNames(m, "gnd", defGnd...))
	if len(gnd) == 0 {
		gnd = e.groundPins(p)
	}
	var gndOnGround []Pin
	for _, g := range gnd {
		if e.isGround(g.Net) {
			gndOnGround = append(gndOnGround, g)
		}
	}
	if len(gndOnGround) > 0 {
		gnd = gndOnGround
	}
	tin, okIn := e.term(c, p.Ref, in)
	tout, okOut := e.term(c, p.Ref, out)
	tg, okG := e.term(c, p.Ref, gnd)
	if !okIn || !okOut || !okG {
		r.warnf("%s: regulator pins in/out/gnd not all connected (in=%d out=%d gnd=%d) — skipped", p.Ref, len(in), len(out), len(gnd))
		return
	}
	reg := &regulator{t: [6]Terminal{tin, tout, tg}, buck: buck, iq: m.IqA, mode: modeOn}
	// Regulation constraint.
	var plus, minus []Pin
	switch {
	case m.Regulate != nil:
		plus, minus = p.pinsNamed([]string{m.Regulate.Plus}), p.pinsNamed([]string{m.Regulate.Minus})
		reg.vreg = m.Regulate.Volts
	case buck:
		plus, minus = p.pinsNamed(roleNames(m, "fb", defFB...)), gnd
		reg.vreg = m.Vref
		if reg.vreg == 0 && m.Vout > 0 {
			reg.vreg = m.Vout
		}
		if reg.vreg == 0 {
			reg.vreg = e.def.BuckVref
			e.warnf("%s: buck reference voltage unknown — %.2f V assumed", p.Ref, reg.vreg)
		}
	default:
		plus, minus = out, gnd
		reg.vreg = m.Vout
		if adj := p.pinsNamed(roleNames(m, "adj", "ADJ")); m.Vout == 0 && len(adj) > 0 && m.Vref > 0 {
			minus = adj
			reg.vreg = m.Vref
		}
	}
	tp, okP := e.term(c, p.Ref, plus)
	tm, okM := e.term(c, p.Ref, minus)
	if !okP || !okM || reg.vreg <= 0 {
		r.warnf("%s: regulation pins (sense) not connected or reference unknown — regulator skipped", p.Ref)
		return
	}
	// The sense terminals carry no current; drop their pin lists so FB/GND
	// pins are not double counted.
	reg.t[3], reg.t[4] = Terminal{Node: tp.Node}, Terminal{Node: tm.Node}
	reg.t[5] = Terminal{Node: Ground}
	if en := p.pinsNamed(roleNames(m, "en", defEN...)); len(en) > 0 {
		if te, ok := e.term(c, p.Ref, en[:1]); ok {
			reg.t[5] = Terminal{Node: te.Node}
			reg.enUsed = true
		} else {
			e.assumef("%s: EN pin unconnected — assumed enabled", p.Ref)
		}
	}
	reg.enThresh = m.EnThreshV
	if reg.enThresh == 0 {
		reg.enThresh = e.def.EnThreshV
	}
	if buck {
		reg.eta = m.Eta
		if reg.eta == 0 {
			reg.eta = e.def.BuckEta
			e.assumef("%s: buck efficiency η=%.2f assumed", p.Ref, reg.eta)
		}
		reg.vinMin = m.VinMinV
		if reg.vinMin == 0 {
			reg.vinMin = 2.0
		}
		reg.kIn = 0.7
	} else {
		reg.kIn = 1
		reg.vdrop = m.DropoutV
		if reg.vdrop == 0 {
			reg.vdrop = e.def.LDODropoutV
			e.assumef("%s: LDO dropout %.2f V assumed", p.Ref, reg.vdrop)
		}
		reg.vinMin = m.VinMinV
		if reg.vinMin == 0 {
			reg.vinMin = reg.vdrop + 0.5
		}
	}
	add(b, reg, p.Ref+" regulator")
	r.regs[p.Ref] = reg
}

var defaultGPIO = regexp.MustCompile(`(?i)^(GPIO|IO|P[A-K])[0-9]+`)

// buildGPIODrives turns IC GPIOs that drive an LED (directly or through one
// resistor) into push-pull outputs: high = Rout from the IC's supply pin, low
// = Rout to its ground pin.
func (e *Engine) buildGPIODrives(r *run, add func(*binding, element, string)) {
	c := r.c
	netPins := map[string][]struct {
		b   *binding
		pin Pin
	}{}
	for _, b := range e.binds {
		for _, pin := range b.part.Pins {
			if pin.Net != "" {
				netPins[pin.Net] = append(netPins[pin.Net], struct {
					b   *binding
					pin Pin
				}{b, pin})
			}
		}
	}
	// reach returns net itself plus nets one resistor away.
	reach := func(net string) []string {
		out := []string{net}
		for _, np := range netPins[net] {
			if np.b.kind != KindResistor || len(np.b.part.Pins) != 2 {
				continue
			}
			for _, other := range np.b.part.Pins {
				if other.Number != np.pin.Number && other.Net != "" && other.Net != net {
					out = append(out, other.Net)
				}
			}
		}
		return out
	}
	driven := map[PinRef]bool{}
	for _, led := range e.binds {
		if led.kind != KindLED {
			continue
		}
		an := led.part.pinsNamed(roleNames(led.model, "anode", defAnode...))
		ca := led.part.pinsNamed(roleNames(led.model, "cathode", defCathode...))
		if len(an) == 0 || len(ca) == 0 {
			if len(led.part.Pins) != 2 {
				continue
			}
			ca, an = led.part.Pins[:1], led.part.Pins[1:]
		}
		for _, sp := range []struct {
			side string
			pins []Pin
		}{{"high", an}, {"low", ca}} {
			side, pins := sp.side, sp.pins
			if pins[0].Net == "" || e.isGround(pins[0].Net) || (side == "low" && e.powerLike(pins[0].Net)) || (side == "high" && e.powerLike(pins[0].Net)) {
				continue
			}
			for _, net := range reach(pins[0].Net) {
				for _, np := range netPins[net] {
					ic := np.b
					if ic.kind != KindLoad {
						continue
					}
					re := defaultGPIO
					if ic.model.GpioPins != "" {
						if cre, err := regexp.Compile(ic.model.GpioPins); err == nil {
							re = cre
						}
					}
					if !re.MatchString(np.pin.Name) {
						continue
					}
					key := PinRef{ic.part.Ref, np.pin.Number}
					if driven[key] {
						continue
					}
					var rail []Pin
					if side == "high" {
						if len(ic.model.SupplyPins) > 0 {
							rail = ic.part.pinsNamed(ic.model.SupplyPins)
						} else {
							for _, pin := range ic.part.Pins {
								if e.powerLike(pin.Net) {
									rail = append(rail, pin)
								}
							}
						}
					} else {
						rail = ic.part.pinsNamed(ic.model.ReturnPins)
						if len(rail) == 0 {
							rail = e.groundPins(ic.part)
						}
					}
					nets, groups := byNet(rail)
					if len(nets) == 0 {
						continue
					}
					tr, ok1 := e.term(c, ic.part.Ref, groups[nets[0]])
					tg, ok2 := e.term(c, ic.part.Ref, []Pin{np.pin})
					if !ok1 || !ok2 {
						continue
					}
					rout := ic.model.GpioRoutOhm
					if rout == 0 {
						rout = e.def.GpioRoutOhm
					}
					driven[key] = true
					add(ic, &resistor{t: [2]Terminal{tr, tg}, r: rout}, fmt.Sprintf("%s.%s GPIO %s", ic.part.Ref, np.pin.Name, side))
					r.assumef("%s.%s (%s) assumed driven %s (%s on) through %.0f Ω output resistance", ic.part.Ref, np.pin.Name, net, side, led.part.Ref, rout)
				}
			}
		}
	}
}

// Run solves every requested scenario and returns the output document.
func (e *Engine) Run() (*Output, error) {
	want := map[string]bool{}
	for _, s := range e.opt.Scenarios {
		for _, part := range strings.Split(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				want[part] = true
			}
		}
	}
	all := e.scenarios()
	known := map[string]bool{"worst": true}
	for _, s := range all {
		known[s.name] = true
	}
	for w := range want {
		if !known[w] {
			return nil, fmt.Errorf("unknown scenario %q (have %s, worst)", w, strings.Join(e.ScenarioNames(), ", "))
		}
	}
	out := &Output{SchemaVersion: 1, Generator: Generator}
	var results []*Result
	wantWorst := len(want) == 0 || want["worst"]
	for _, sc := range all {
		// worst needs every scenario even when only worst was requested.
		if len(want) > 0 && !want[sc.name] && !want["worst"] {
			continue
		}
		rr := e.solve(sc)
		e.runs[sc.name] = rr
		res := e.result(rr)
		// Steady-state heat needs the time-averaged operating point: a peak
		// scenario gets a twin solve with the same sources and switches but
		// every load at its average current (attachThermal).
		var avg *Result
		if sc.peak {
			twin := sc
			twin.peak = false
			avg = e.result(e.solve(twin))
		}
		attachThermal(res, avg)
		if len(want) == 0 || want[sc.name] {
			out.Scenarios = append(out.Scenarios, sc.name)
			out.Results = append(out.Results, *res)
		}
		results = append(results, res)
	}
	if wantWorst && len(results) > 0 {
		w := worst(results)
		out.Scenarios = append(out.Scenarios, "worst")
		out.Results = append(out.Results, *w)
	}
	return out, nil
}

func (e *Engine) solve(sc scenario) *run {
	r := e.build(sc)
	sol, iters, conv, err := r.c.Solve()
	r.sol, r.iters, r.conv, r.err = sol, iters, conv, err
	r.pinI = map[PinRef]float64{}
	if err != nil {
		r.warnf("solver failed: %v", err)
		return r
	}
	if !conv {
		r.warnf("operating point did not converge after %d Newton iterations — results are the last iterate", iters)
	}
	for _, el := range r.c.elems {
		cur := el.currents(sol.X)
		for i, t := range el.terminals() {
			if len(t.Pins) == 0 {
				continue
			}
			share := cur[i] / float64(len(t.Pins))
			for _, pr := range t.Pins {
				r.pinI[pr] += share
			}
		}
	}
	return r
}

// voltage of a net in a run (0 for ground, ok=false when not in the circuit).
func (r *run) voltage(net string, e *Engine) (float64, bool) {
	if e.isGround(net) {
		return 0, true
	}
	i, ok := r.c.index[net]
	if !ok || r.sol == nil {
		return 0, false
	}
	return r.sol.X[i], true
}

func round(v float64, digits int) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	p := math.Pow(10, float64(digits))
	out := math.Round(v*p) / p
	if out == 0 {
		return 0 // avoid -0
	}
	return out
}

const pinEps = 1e-9

func dirOf(i float64) string {
	switch {
	case i > pinEps:
		return "sink"
	case i < -pinEps:
		return "source"
	}
	return "pass"
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
