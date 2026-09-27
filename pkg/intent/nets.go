package intent

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// ifaceNames maps the engine's HS class (pcbauto.ClassifyHSName — the one
// table the router, the SI check and the rule push use) to the interface
// name intent.json writes.
var ifaceNames = map[string]string{
	"USB2": "USB", "USB3": "USB3", "PCIe": "PCIE", "SATA": "SATA", "HDMI": "HDMI", "MIPI D-PHY": "MIPI",
	"LVDS": "LVDS", "DDR": "DDR", "Ethernet": "ETH", "CAN/RS-485": "CAN/RS485", "diff": "DIFF",
}

var ifaceNotes = map[string]string{
	"USB":       "USB 2.0 high-speed: 90 Ω ±10 % differential",
	"USB3":      "USB 3.x SuperSpeed: 90 Ω ±10 % differential, ≤ 5 mil intra-pair skew",
	"PCIE":      "PCIe: 85 Ω differential (PCI-SIG CEM), ≤ 5 mil intra-pair skew",
	"SATA":      "SATA: 100 Ω differential",
	"HDMI":      "HDMI TMDS: 100 Ω differential, pairs length-matched to each other",
	"MIPI":      "MIPI D-PHY: 100 Ω differential, lanes matched to the clock lane",
	"LVDS":      "LVDS: 100 Ω differential",
	"DDR":       "DDR: 100 Ω differential strobe/clock, byte lanes length-matched",
	"ETH":       "Ethernet MDI: 100 Ω differential",
	"CAN/RS485": "CAN / RS-485: 120 Ω line (terminated); routing impedance uncritical at these edge rates",
	"DIFF":      "generic differential pair: 100 Ω default",
}

// hsOf is the recognised interface of a differential net: its intent name,
// target impedance, declared length group, the engine class and a note.
type hsOf struct {
	name  string
	ohm   float64
	group string
	class *pcbauto.HSClass
	note  string
	spec  *SpecHS
}

func (c *ctx) ifaceOf(net string) hsOf {
	for i := range c.spec.HSInterfaces {
		hs := &c.spec.HSInterfaces[i]
		for _, p := range hs.Pairs {
			if p[0] == net || p[1] == net {
				hc := pcbauto.ClassifyHSName(hs.Name, net)
				ohm := hs.DiffOhm
				if ohm == 0 {
					ohm = hc.DiffOhm
				}
				return hsOf{name: hs.Name, ohm: ohm, group: hs.LengthGroup, class: hc, note: "declared in spec.hsInterfaces (" + hs.Name + ")", spec: hs}
			}
		}
	}
	hc := pcbauto.ClassifyHSName("", net)
	name := ifaceNames[hc.Name]
	if name == "" {
		name = "DIFF"
	}
	return hsOf{name: name, ohm: hc.DiffOhm, class: hc, note: ifaceNotes[name]}
}

// reLane splits a lane of a multi-pair interface: HDMI_0P, HDMI_CP,
// MIPI_DSI_TX0_D1P, CSI0_CLK_N → prefix + lane + polarity.
var reLane = regexp.MustCompile(`^(.*?)[_-]?(D?[0-9]+|CLK|CK|C)[_-]?(P|N|\+|-)$`)

// laneGroup is the port prefix of a lane of a multi-lane interface whose
// lanes are matched to each other (HDMI TMDS, MIPI D-PHY, LVDS), else "".
func laneGroup(net, iface string) string {
	switch iface {
	case "HDMI", "MIPI", "LVDS":
	default:
		return ""
	}
	m := reLane.FindStringSubmatch(strings.ToUpper(net))
	if m == nil || m[1] == "" {
		return ""
	}
	return m[1] + "_LANES"
}

// netVoltageEnvelope computes nom/min/max/peak of a net.
func (c *ctx) netVoltageEnvelope(net string) (Voltage, []string) {
	var why []string
	if c.mains[net] {
		rms := c.mainsVrms()
		pk := rms * math.Sqrt2
		return Voltage{Nom: rms, Min: round(-pk, 3), Max: round(pk, 3), Peak: round(pk, 3)},
			[]string{fmt.Sprintf("AC line: nom = %s Vrms, peak = √2·Vrms = %s V (spec.mains or 230 V default)", trimFloat(rms, 0), trimFloat(pk, 1))}
	}
	var v Voltage
	vs, typ, hasTyp := c.scenarioVoltages(net)
	if c.isPowerNet(net) && len(vs) > 1 {
		// A rail whose source is disconnected in a <source>-only scenario
		// reads ~0 V there: the envelope is taken while the rail is powered.
		hi := 0.0
		for _, x := range vs {
			hi = math.Max(hi, math.Abs(x))
		}
		var on []float64
		for _, x := range vs {
			if math.Abs(x) >= 0.1*hi {
				on = append(on, x)
			}
		}
		if len(on) < len(vs) {
			why = append(why, fmt.Sprintf("%d scenario(s) with the rail unpowered (< 10 %% of its maximum) left out of min/max", len(vs)-len(on)))
			vs = on
		}
	}
	floating := len(vs) == 0
	if !floating {
		v.Min, v.Max = vs[0], vs[0]
		for _, x := range vs {
			v.Min, v.Max = math.Min(v.Min, x), math.Max(v.Max, x)
		}
		v.Nom = vs[0]
		if hasTyp {
			v.Nom = typ
		}
		v.Peak = math.Max(math.Abs(v.Min), math.Abs(v.Max))
		src := "typical"
		if !hasTyp {
			src = c.scens[0].Scenario
		}
		why = append([]string{fmt.Sprintf("sim: nom %s V (%s), envelope %s…%s V over %d scenario(s)", trimFloat(v.Nom, 3), src, trimFloat(v.Min, 3), trimFloat(v.Max, 3), len(vs))}, why...)
	}
	// Switch node: swings between ground and Vin (buck) / Vout (boost).
	if reg := c.switchRegulator(net); reg != "" {
		pk := 0.0
		for _, r := range c.scens {
			if pr := r.Parts[reg]; pr != nil {
				pk = math.Max(pk, math.Max(pr.VinV, pr.VoutV))
			}
		}
		if pk > v.Peak {
			v.Peak = pk
			why = append(why, fmt.Sprintf("switch node of %s: swings 0…Vin, peak %s V (DC value is the average)", reg, trimFloat(pk, 3)))
		}
	}
	if floating {
		bound := 0.0
		for _, ref := range c.partsOn(net) {
			for _, n := range c.partNets(ref) {
				if n == net || !c.isPowerNet(n) {
					continue
				}
				_, hi := c.envelope(n)
				bound = math.Max(bound, hi)
			}
		}
		if bound == 0 {
			bound = pcbauto.InferVoltage(net)
		}
		v.Max, v.Peak = round(bound, 3), round(bound, 3)
		why = append(why, fmt.Sprintf("no DC path in the simulation (floating signal): bounded by the supply rails of the connected parts, ≤ %s V", trimFloat(bound, 3)))
	}
	for _, r := range c.spec.Rails {
		if !strings.EqualFold(r.Net, net) {
			continue
		}
		if r.Voltage != 0 && floating {
			v.Nom, v.Min, v.Max = r.Voltage, r.Voltage, r.Voltage
			v.Peak = math.Max(v.Peak, math.Abs(r.Voltage))
			why = append(why, fmt.Sprintf("declared %s V (spec.rails)", trimFloat(r.Voltage, 3)))
		}
		if r.PeakV > v.Peak {
			v.Peak = r.PeakV
			why = append(why, fmt.Sprintf("declared peak %s V (spec.rails.peakV)", trimFloat(r.PeakV, 3)))
		}
	}
	v.Nom, v.Min, v.Max, v.Peak = round(v.Nom, 4), round(v.Min, 4), round(v.Max, 4), round(v.Peak, 4)
	return v, why
}

// switchRegulator returns the regulator whose switch node net is.
func (c *ctx) switchRegulator(net string) string {
	for _, cv := range c.circ.Converters {
		if cv.SwitchNet == net {
			return cv.Core
		}
	}
	if c.worst != nil {
		if rp, ok := c.worst.Ripple[net]; ok && rp.Regulator != "" {
			return rp.Regulator
		}
	}
	return ""
}

func commonPrefix(a, b string) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return strings.TrimRight(a[:i], "_-")
}

// buildNets writes the per-net plan.
func (c *ctx) buildNets() {
	coated := c.spec.Standard != nil && c.spec.Standard.Coated
	t := 1.378 * c.outerOz
	hsSingle := map[string]SpecHS{}
	for _, hs := range c.spec.HSInterfaces {
		for _, n := range hs.Nets {
			hsSingle[n] = hs
		}
	}
	c.laneGroups = map[string]string{}
	laneNets := map[string][]string{}
	for _, net := range c.d.Nets() {
		if pp := c.an.Plan(net, c.rules); pp.Role == pcbauto.RoleDiff {
			if hs := c.ifaceOf(net); hs.spec == nil {
				if lg := laneGroup(net, hs.name); lg != "" {
					laneNets[lg] = append(laneNets[lg], net)
				}
			}
		}
	}
	for lg, nets := range laneNets {
		if len(nets) >= 4 { // at least two pairs of one port
			for _, n := range nets {
				c.laneGroups[n] = lg
			}
		}
	}
	for _, net := range c.d.Nets() {
		pp := c.an.Plan(net, c.rules)
		np := &NetPlan{Domain: c.domOfNet[net], Block: c.netBlock[net], Pins: []PinCurrent{}, Why: []string{}, PadCount: pp.PadCount, Priority: pp.Priority}
		v, vwhy := c.netVoltageEnvelope(net)
		np.Voltage = v
		np.Why = append(np.Why, vwhy...)
		np.Role = string(pp.Role)
		np.CurrentA = round(pp.CurrentA, 5)
		switch pp.Source {
		case "simulated", "declared":
			np.CurrentSource = pp.Source
		default:
			np.CurrentSource = "heuristic"
		}
		if pp.SimCurrentA > 0 && math.Abs(pp.SimCurrentA-pp.CurrentA) > 1e-6 {
			np.DCCurrentA = round(pp.SimCurrentA, 5)
		}
		np.PeakA = round(pp.PeakA, 5)
		np.Scenario = pp.SimScenario
		for _, w := range pp.Why {
			// Impedance lines are re-derived below per interface; a zero-current
			// IPC line says nothing.
			if strings.Contains(w, "Ω diff over") || strings.Contains(w, "Ω microstrip") || strings.Contains(w, "IPC-2221/2152 0.00A") {
				continue
			}
			np.Why = append(np.Why, w)
		}
		np.Warnings = append(np.Warnings, pp.Warnings...)
		maxPin := 0.0
		if nr := c.netW(net); nr != nil {
			np.Floating = nr.Floating
			for _, p := range nr.Pins {
				np.Pins = append(np.Pins, PinCurrent{Ref: p.Ref, Pin: p.Pin, CurrentA: p.CurrentA, Dir: p.Dir, Name: p.Name, Scenario: p.Scenario})
				maxPin = math.Max(maxPin, p.CurrentA)
			}
		} else {
			for _, pr := range c.netPins[net] {
				np.Pins = append(np.Pins, PinCurrent{Ref: pr.Ref, Pin: pr.Pin, Dir: "pass", Name: pr.Name})
			}
		}
		for _, r := range c.spec.Rails {
			if strings.EqualFold(r.Net, net) && r.RippleMvpp > 0 {
				np.RippleMvpp = r.RippleMvpp
				np.Why = append(np.Why, fmt.Sprintf("ripple budget %s mVpp (spec.rails)", trimFloat(r.RippleMvpp, 1)))
			}
		}
		// A "power"-named net whose only simulated sources are resistors is
		// a divider / sense node (VBUS_DET, VIN_SENSE), not a rail: it must
		// not become a power-plane island that high-speed pairs then cross.
		if pp.Role == pcbauto.RolePower && pp.Source != "declared" && c.resistorFed(net) {
			pp = &pcbauto.NetPlan{Net: pp.Net, Role: pcbauto.RoleSignal, WidthMil: c.rules.TrackWidth, InnerWidthMil: c.rules.TrackWidth, PadCount: pp.PadCount, Priority: 5, ViasPerTransition: 1}
			np.Role = "signal"
			np.Why = append(np.Why, "named like a rail but fed only through a resistor in the simulation: a divider/sense signal, routed as a signal (not a plane island)")
		}
		// Widths.
		np.WidthMil = Width{Outer: pp.WidthMil, Inner: pp.InnerWidthMil}
		floor := c.rules.TrackWidth
		switch pp.Role {
		case pcbauto.RolePower, pcbauto.RoleGround:
			floor = math.Max(floor, 10)
		case pcbauto.RoleSwitch:
			floor = math.Max(floor, 20)
		}
		np.WidthMil.Min = floor
		if maxPin > 0 {
			if w := pcbauto.TraceWidthForCurrent(maxPin, c.tempRise, c.outerOz, false); w > floor {
				np.WidthMil.Min = metricRoundMil(w)
			}
		}
		np.WidthMil.Min = math.Min(np.WidthMil.Min, np.WidthMil.Outer)
		if np.WidthMil.Min < floor {
			np.WidthMil.Min = math.Min(floor, np.WidthMil.Outer)
		}
		// AC line nets: the DC simulation carries no line current. Size the
		// copper for the fuse that protects it (else 1 A) unless declared.
		if c.mains[net] {
			np.Role = "power"
			if np.CurrentSource != "declared" {
				ia, src := c.mainsCurrent()
				np.CurrentA, np.CurrentSource = ia, "heuristic"
				np.Why = append(np.Why, fmt.Sprintf("AC line current not simulated: sized for %s (%s) — declare it in spec.rails", fmtA(ia), src))
				floor := math.Max(c.rules.TrackWidth, 10)
				np.WidthMil.Outer = math.Max(floor, metricRoundMil(pcbauto.TraceWidthForCurrent(ia, c.tempRise, c.outerOz, false)))
				np.WidthMil.Inner = math.Max(floor, metricRoundMil(pcbauto.TraceWidthForCurrent(ia, c.tempRise, c.innerOz, false)))
				np.WidthMil.Min = np.WidthMil.Outer
				np.ViasPerTransition = int(math.Max(1, math.Ceil(ia/pcbauto.ViaCurrent(c.rules.ViaDrill, c.tempRise))))
			}
		}
		// Impedance-controlled nets.
		switch {
		case pp.Role == pcbauto.RoleDiff:
			hs := c.ifaceOf(net)
			name, ohm, group, note := hs.name, hs.ohm, hs.group, hs.note
			np.Interface, np.ImpedanceOhm, np.DiffPair = name, ohm, pp.PairWith
			np.MaxSkewMil, np.MaxVias = hs.class.MaxSkewMil, hs.class.MaxVias
			if sp := hs.spec; sp != nil {
				if sp.MaxSkewMil > 0 {
					np.MaxSkewMil = sp.MaxSkewMil
				}
				if sp.MaxVias > 0 {
					np.MaxVias = sp.MaxVias
				}
				np.LengthTolMil = sp.LengthTolMil
			}
			switch {
			case group != "":
				if np.LengthTolMil == 0 {
					np.LengthTolMil = hs.class.GroupSkewMil
				}
			default:
				if dg := c.ddrGroup[net]; dg != "" {
					group, np.LengthTolMil = dg, hs.class.GroupSkewMil
					if strings.HasSuffix(dg, "_ADDR") {
						np.LengthTolMil = ddrAddrTolMil
					}
					np.Why = append(np.Why, fmt.Sprintf("DDR %s recognised from the net names: length group %s, tolerance %.0f mil", map[bool]string{true: "clock (fly-by address/command group)", false: "strobe (byte lane)"}[strings.HasSuffix(dg, "_ADDR")], dg, np.LengthTolMil))
				} else if lg := c.laneGroups[net]; lg != "" {
					group, np.LengthTolMil = lg, hs.class.GroupSkewMil
					np.Why = append(np.Why, fmt.Sprintf("%s lanes of one port are length-matched to each other: group %s, tolerance %.0f mil (%s default)", name, lg, np.LengthTolMil, hs.class.Name))
				} else if group = commonPrefix(net, pp.PairWith); group == "" {
					group = name
				}
			}
			np.LengthGroup = group
			np.Why = append(np.Why, fmt.Sprintf("%s: intra-pair skew ≤ %.0f mil, ≤ %d vias per net (%s)", name, np.MaxSkewMil, np.MaxVias, map[bool]string{true: "spec.hsInterfaces", false: hs.class.Name + " design-guide default"}[hs.spec != nil && (hs.spec.MaxSkewMil > 0 || hs.spec.MaxVias > 0)]))
			w, s := pcbauto.SolveDiff(ohm, c.refH, t, c.er, c.rules.Clearance)
			if c.layers <= 2 || w > 25 {
				np.Why = append(np.Why, fmt.Sprintf("%s: %.0f Ω needs w=%.0f mil over h=%.0f mil — not controllable without an adjacent plane; routed as a tightly coupled %.0f/%.0f mil pair", note, ohm, w, c.refH, c.rules.TrackWidth, c.rules.Clearance))
				w, s = c.rules.TrackWidth, c.rules.Clearance
				c.uncontrolled = append(c.uncontrolled, net)
				if hs.class.RateGbps >= 1 {
					c.noReference = append(c.noReference, net)
				}
			} else {
				np.Why = append(np.Why, fmt.Sprintf("%s: edge-coupled microstrip over h=%.2f mil, εr=%.2f, t=%.2f mil → w=%.1f mil, gap=%.1f mil (pcbauto.SolveDiff on %s)", note, c.refH, c.er, t, w, s, c.stackName))
			}
			w = math.Ceil(w*10) / 10
			np.WidthMil = Width{Outer: w, Inner: w, Min: w}
			np.PairGapMil = math.Ceil(s*2) / 2
			np.Role = "diff"
		case pp.Role == pcbauto.RoleRF:
			np.ImpedanceOhm = 50
			np.Why = append(np.Why, "RF: 50 Ω single-ended (coplanar or microstrip over an unbroken reference)")
			np.WidthMil.Min = np.WidthMil.Outer
		}
		if hs, ok := hsSingle[net]; ok && pp.Role != pcbauto.RoleDiff {
			ohm := hs.SingleOhm
			if ohm == 0 {
				ohm = 50
			}
			w := pcbauto.SolveWidthForZ0(ohm, c.refH, t, c.er)
			if c.layers <= 2 || w > 40 {
				np.Why = append(np.Why, fmt.Sprintf("%s: %.0f Ω single-ended not controllable on this stackup (w=%.0f mil)", hs.Name, ohm, w))
				w = c.rules.TrackWidth
			} else {
				np.Why = append(np.Why, fmt.Sprintf("%s: %.0f Ω microstrip → w=%.1f mil (pcbauto.SolveWidthForZ0)", hs.Name, ohm, w))
			}
			w = math.Ceil(w*10) / 10
			np.Role, np.Interface, np.ImpedanceOhm, np.LengthGroup = "hs", hs.Name, ohm, firstNonEmpty(hs.LengthGroup, hs.Name)
			np.WidthMil = Width{Outer: w, Inner: w, Min: w}
			np.LengthTolMil, np.MaxVias = hs.LengthTolMil, hs.MaxVias
			if hc := pcbauto.HSClassForInterface(hs.Name); hc != nil {
				if np.LengthTolMil == 0 {
					np.LengthTolMil = hc.GroupSkewMil
				}
				if np.MaxVias == 0 {
					np.MaxVias = hc.MaxVias
				}
			}
			if np.LengthTolMil > 0 {
				np.Why = append(np.Why, fmt.Sprintf("length group %s: matched within %.0f mil", np.LengthGroup, np.LengthTolMil))
			}
		}
		if dg := c.ddrGroup[net]; dg != "" && np.LengthGroup == "" && np.Role != "diff" {
			// Single-ended DDR data/address: grouped and via-budgeted; the
			// width stays the routing width (a 50 Ω microstrip on a 4-layer
			// JLC stack is ~15 mil, unroutable under the SoC BGA) — declare
			// the interface in spec.hsInterfaces to solve the SE impedance.
			hc := pcbauto.HSClassForInterface("DDR")
			np.Interface, np.LengthGroup, np.LengthTolMil, np.MaxVias = "DDR", dg, hc.GroupSkewMil, hc.MaxVias
			if strings.HasSuffix(dg, "_ADDR") {
				np.LengthTolMil = ddrAddrTolMil
			}
			np.Why = append(np.Why, fmt.Sprintf("DDR %s recognised from the net name: length group %s matched within %.0f mil, ≤ %d vias", map[bool]string{true: "address/command/control", false: "data (byte lane)"}[strings.HasSuffix(dg, "_ADDR")], dg, np.LengthTolMil, np.MaxVias))
		}
		// Clearance from the peak voltage to the net's own reference.
		np.ClearanceMil = c.rules.Clearance
		if ipc := pcbauto.ClearanceForVoltage(v.Peak, true, coated); ipc > np.ClearanceMil {
			np.ClearanceMil = math.Ceil(ipc*10) / 10
			np.Why = append(np.Why, fmt.Sprintf("IPC-2221B %s %s V peak → clearance %.1f mil", map[bool]string{true: "B4 (coated)", false: "B2 (external, uncoated)"}[coated], trimFloat(v.Peak, 1), np.ClearanceMil))
		} else if v.Peak > 0 {
			np.Why = append(np.Why, fmt.Sprintf("IPC-2221B %s V peak needs ≤ %.1f mil: fab clearance %.1f mil governs", trimFloat(v.Peak, 1), ipc, c.rules.Clearance))
		}
		if !c.mains[net] || np.CurrentSource == "declared" {
			np.ViasPerTransition = pp.ViasPerTransition
		}
		if np.ViasPerTransition > 1 {
			np.Why = append(np.Why, fmt.Sprintf("%d vias per layer change: one %.0f mil via carries %.2f A at ΔT %.0f °C (pcbauto.ViaCurrent)", np.ViasPerTransition, c.rules.ViaDrill, pcbauto.ViaCurrent(c.rules.ViaDrill, c.tempRise), c.tempRise))
		}
		np.NetClass = c.classOf(net, np)
		c.out.Nets[net] = np
	}
}

func metricRoundMil(mil float64) float64 {
	mm := math.Ceil(mil*0.0254/0.05-1e-9) * 0.05
	return math.Round(mm/0.0254*100) / 100
}

func (c *ctx) classOf(net string, np *NetPlan) string {
	if d := c.domain(np.Domain); np.Voltage.Peak > selvDC || c.mains[net] || d != nil && hazardKind(d.Kind) {
		id := np.Domain
		if id == "" {
			id = "NET"
		}
		return "HV_" + id
	}
	switch np.Role {
	case "ground":
		return "GND"
	case "power":
		if np.CurrentA > 1.0 {
			return "POWER_HI"
		}
		return "POWER"
	case "switch":
		return "SWITCH"
	case "diff":
		return "HS_DIFF"
	case "hs":
		return "HS"
	case "rf":
		return "RF"
	}
	return "SIGNAL"
}

var classOrder = []string{"GND", "POWER", "POWER_HI", "SWITCH", "HS_DIFF", "HS", "RF", "SIGNAL"}

// buildNetClasses aggregates nets into pushable rule classes.
func (c *ctx) buildNetClasses() {
	// Several diff impedances → one class per impedance.
	ohms := map[float64]bool{}
	for _, np := range c.out.Nets {
		if np.NetClass == "HS_DIFF" {
			ohms[np.ImpedanceOhm] = true
		}
	}
	if len(ohms) > 1 {
		for _, np := range c.out.Nets {
			if np.NetClass == "HS_DIFF" {
				np.NetClass = fmt.Sprintf("HS_DIFF_%.0f", np.ImpedanceOhm)
			}
		}
	}
	byName := map[string]*NetClass{}
	for _, net := range sortedKeys(c.out.Nets) {
		np := c.out.Nets[net]
		nc := byName[np.NetClass]
		if nc == nil {
			nc = &NetClass{Name: np.NetClass, ViaDrillMil: c.rules.ViaDrill, ViaDiaMil: c.rules.ViaDia}
			byName[np.NetClass] = nc
		}
		nc.Nets = append(nc.Nets, net)
		nc.TrackMil = math.Max(nc.TrackMil, np.WidthMil.Outer)
		nc.InnerTrackMil = math.Max(nc.InnerTrackMil, np.WidthMil.Inner)
		// minTrackMil is the narrowest width allowed anywhere on the class
		// — the router's neck-down at fine-pitch pads, i.e. the fabrication
		// minimum — not the width the body of the trace needs (that is
		// widthMil.min / trackMil). Pushing the body width as the class
		// minimum made host DRC flag every legal neck (live 2026-09-27).
		nc.MinTrackMil = c.rules.MinTrack
		nc.ClearanceMil = math.Max(nc.ClearanceMil, np.ClearanceMil)
		nc.DiffGapMil = math.Max(nc.DiffGapMil, np.PairGapMil)
		nc.ImpedanceOhm = math.Max(nc.ImpedanceOhm, np.ImpedanceOhm)
	}
	for _, nc := range byName {
		switch {
		case nc.Name == "SIGNAL":
			nc.TrackMil = math.Max(c.rules.TrackWidth, nc.TrackMil)
			nc.Why = append(nc.Why, "default signal class: fab track/clearance")
		case strings.HasPrefix(nc.Name, "HS_DIFF"):
			nc.Why = append(nc.Why, fmt.Sprintf("impedance-controlled pairs %.0f Ω: width %.1f mil, gap %.1f mil", nc.ImpedanceOhm, nc.TrackMil, nc.DiffGapMil))
		case strings.HasPrefix(nc.Name, "HV_"):
			nc.Why = append(nc.Why, fmt.Sprintf("hazardous/mains nets: clearance %.1f mil from IPC-2221B at the class peak voltage; insulation to other domains is in pairs[]", nc.ClearanceMil))
		default:
			nc.Why = append(nc.Why, fmt.Sprintf("widest member width %.1f mil outer / %.1f mil inner (IPC-2221/2152 at ΔT %.0f °C), largest member clearance", nc.TrackMil, nc.InnerTrackMil, c.tempRise))
		}
		c.out.NetClasses = append(c.out.NetClasses, nc)
	}
	rank := func(n string) int {
		for i, o := range classOrder {
			if o == n {
				return i
			}
		}
		if strings.HasPrefix(n, "HS_DIFF") {
			return 4
		}
		return len(classOrder) - 1 // HV_* before SIGNAL
	}
	sort.SliceStable(c.out.NetClasses, func(i, j int) bool {
		ri, rj := rank(c.out.NetClasses[i].Name), rank(c.out.NetClasses[j].Name)
		if ri != rj {
			return ri < rj
		}
		return c.out.NetClasses[i].Name < c.out.NetClasses[j].Name
	})
}

// mainsCurrent is the line current the copper is sized for: the largest
// fuse rating on a line net (the fuse protects that copper), else 1 A.
func (c *ctx) mainsCurrent() (float64, string) {
	best, src := 0.0, ""
	for _, p := range c.d.Parts {
		if c.kind(p.Ref) != pcbauto.KindFuse {
			continue
		}
		onLine := false
		for _, n := range c.partNets(p.Ref) {
			onLine = onLine || c.mains[n]
		}
		if !onLine {
			continue
		}
		r, _, ok := c.currentRating(p.Ref)
		if !ok {
			if v, ok2 := powersim.ParseValue(p.Value); ok2 && v > 0 && v < 100 {
				r, ok = v, true
			}
		}
		if ok && r > best {
			best, src = r, "fuse "+p.Ref+" rating"
		}
	}
	if best == 0 {
		return 1, "no line fuse found: 1 A default"
	}
	return best, src
}

// resistorFed reports a net whose simulated source pins (worst case) all
// belong to resistors and that carries under 1 mA: a divider or sense node.
func (c *ctx) resistorFed(net string) bool {
	nr := c.netW(net)
	if nr == nil || nr.Floating {
		return false
	}
	src, total := 0, 0.0
	for _, p := range nr.Pins {
		if p.Dir != "source" {
			continue
		}
		if c.kind(p.Ref) != pcbauto.KindResistor {
			return false
		}
		src++
		total += p.CurrentA
	}
	return src > 0 && total < 1e-3
}
