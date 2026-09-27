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

var (
	reCurRating = regexp.MustCompile(`(?i)(?:Current Rating|Rated Current|Current - Rectified|Current - Collector\s*\(Ic\)|Contact Current|Current - Output|Output Current|Forward Current|Current - Saturation\s*\(Isat\))\s*(?:\(Max\))?:\s*([0-9.]+)\s*(m?A)`)
	rePowRating = regexp.MustCompile(`(?i)Power\s*\(Watts\):\s*([0-9.]+)\s*(m?W)`)
	reVRating   = regexp.MustCompile(`(?i)Voltage Rating(?:\s*\(Max\))?:\s*([0-9.]+)\s*V`)
	reVinRange  = regexp.MustCompile(`(?i)Voltage - (?:Supply|Input)(?:\s*\(Max\))?:\s*([0-9.]+)\s*V\s*[~-]\s*([0-9.]+)\s*V`)
)

func unitScale(u string) float64 {
	if strings.HasPrefix(strings.ToLower(u), "m") {
		return 1e-3
	}
	return 1
}

// currentRating is the part's rated current (A): library model maxA, else
// the LCSC description. ok=false when unknown.
func (c *ctx) currentRating(ref string) (float64, string, bool) {
	if m := c.model[ref]; m != nil && m.MaxA > 0 {
		return m.MaxA, "power model " + m.ID, true
	}
	if p := c.parts[ref]; p != nil {
		if m := reCurRating.FindStringSubmatch(p.Description); m != nil {
			var v float64
			fmt.Sscanf(m[1], "%g", &v)
			if v > 0 {
				return v * unitScale(m[2]), "description", true
			}
		}
	}
	return 0, "", false
}

func (c *ctx) add(sev, kind, msg string, refs, nets []string, suggestion string) {
	if refs == nil {
		refs = []string{}
	}
	if nets == nil {
		nets = []string{}
	}
	for _, f := range c.out.Findings {
		if f.Kind == kind && f.Message == msg {
			return
		}
	}
	c.out.Findings = append(c.out.Findings, &Finding{Severity: sev, Kind: kind, Message: msg, Refs: refs, Nets: nets, Suggestion: suggestion})
}

// maxPinCurrent is the largest worst-case current through any pin of ref.
func (c *ctx) maxPinCurrent(ref string) (float64, string, string) {
	best, net, pin := 0.0, "", ""
	for _, n := range c.partNets(ref) {
		nr := c.netW(n)
		if nr == nil {
			continue
		}
		for _, p := range nr.Pins {
			if p.Ref == ref && p.CurrentA > best {
				best, net, pin = p.CurrentA, n, p.Pin
			}
		}
	}
	return best, net, pin
}

func (c *ctx) buildFindings() {
	c.findSim()
	c.findRegulators()
	c.findInductors()
	c.findDiodes()
	c.findRatings()
	c.findBulk()
	c.findUSB()
	c.findNets()
	c.findSafety()
	sev := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.SliceStable(c.out.Findings, func(i, j int) bool {
		a, b := c.out.Findings[i], c.out.Findings[j]
		if sev[a.Severity] != sev[b.Severity] {
			return sev[a.Severity] < sev[b.Severity]
		}
		return a.Kind < b.Kind
	})
}

func (c *ctx) findSim() {
	for _, r := range c.sim.Results {
		if !r.Converged {
			c.add("error", "sim-not-converged", "power simulation scenario "+r.Scenario+" did not converge — every number derived from it is suspect", nil, nil, "fix the model/topology warnings, re-run pcbpilot sim power")
		}
	}
	if si := c.out.Simulation; si != nil {
		for _, w := range si.Warnings {
			if strings.Contains(w, "exceeds") || strings.Contains(w, "deviates") {
				c.add("warn", "sim-warning", w, refsIn(w, c.parts), nil, "see pcbpilot sim power --report")
			}
		}
	}
	for _, m := range c.sim.Models {
		if m.Kind != powersim.KindLoad && m.Kind != powersim.KindICSmall {
			continue
		}
		switch {
		case strings.HasPrefix(m.ModelID, "generic-"):
			c.add("warn", "unknown-model", fmt.Sprintf("%s has no power model: its supply current is ASSUMED (generic %s)", m.Ref, m.Kind), []string{m.Ref}, nil,
				"add the part (typ/peak supply current from the datasheet) to references/power-models.json and re-derive")
		case m.Confidence == "assumed":
			c.add("info", "assumed-model", fmt.Sprintf("%s model %s is marked assumed", m.Ref, m.ModelID), []string{m.Ref}, nil, "replace with datasheet numbers")
		}
	}
}

func refsIn(s string, parts map[string]*powersim.Part) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ':' || r == ',' || r == '(' || r == ')' }) {
		if parts[f] != nil {
			out = append(out, f)
		}
	}
	return uniq(out)
}

func (c *ctx) findRegulators() {
	for _, p := range c.d.Parts {
		k := c.simKind[p.Ref]
		if k != powersim.KindBuck && k != powersim.KindLDO {
			continue
		}
		m := c.model[p.Ref]
		in, out := c.regInRail(p.Ref), c.regOutRail(p.Ref)
		minVin, maxIout, maxDuty := math.Inf(1), 0.0, 0.0
		for _, r := range c.scens {
			pr := r.Parts[p.Ref]
			if pr == nil {
				continue
			}
			switch {
			case pr.Mode == "dropout":
				c.add("error", "regulator-dropout", fmt.Sprintf("%s is in dropout in scenario %s (Vin %s V)", p.Ref, r.Scenario, trimFloat(pr.VinV, 3)), []string{p.Ref}, []string{in, out},
					"raise the input voltage or pick a lower-dropout / buck-boost part")
				continue
			case pr.Mode == "off" && pr.VinV > 0.5:
				c.add("warn", "regulator-off", fmt.Sprintf("%s is off in scenario %s although its input is at %s V (enable low or under-voltage lockout)", p.Ref, r.Scenario, trimFloat(pr.VinV, 3)), []string{p.Ref}, []string{in, out},
					"check EN and the minimum input voltage")
				continue
			case pr.Mode == "off":
				continue // input unpowered in this scenario (its source is disconnected)
			}
			minVin = math.Min(minVin, pr.VinV)
			maxIout = math.Max(maxIout, pr.OutputA)
			if k == powersim.KindBuck && pr.VinV > 0 && pr.Efficiency > 0 {
				maxDuty = math.Max(maxDuty, pr.VoutV/(pr.Efficiency*pr.VinV))
			}
		}
		if math.IsInf(minVin, 1) {
			continue
		}
		vout := c.netVoltage(out)
		switch k {
		case powersim.KindLDO:
			drop := 1.1
			if m != nil && m.DropoutV > 0 {
				drop = m.DropoutV
			}
			head := minVin - vout
			switch {
			case head < drop:
				c.add("error", "regulator-headroom", fmt.Sprintf("%s LDO headroom %s V (Vin min %s V − Vout %s V) is below its %s V dropout", p.Ref, trimFloat(head, 3), trimFloat(minVin, 3), trimFloat(vout, 3), trimFloat(drop, 2)), []string{p.Ref}, []string{in, out},
					"use a lower-dropout LDO or a buck, or raise Vin")
			case head < drop+0.3:
				c.add("warn", "regulator-headroom", fmt.Sprintf("%s LDO headroom %s V is within 0.3 V of its %s V dropout", p.Ref, trimFloat(head, 3), trimFloat(drop, 2)), []string{p.Ref}, []string{in, out},
					"check dropout at the peak load and temperature")
			default:
				c.add("info", "regulator-headroom", fmt.Sprintf("%s LDO headroom %s V ≥ dropout %s V; dissipation (Vin−Vout)·I", p.Ref, trimFloat(head, 3), trimFloat(drop, 2)), []string{p.Ref}, []string{in, out}, "")
			}
		case powersim.KindBuck:
			if m != nil && m.VinMinV > 0 && minVin < m.VinMinV+0.2 {
				c.add("error", "regulator-headroom", fmt.Sprintf("%s minimum Vin %s V is at/below its %s V UVLO/minimum", p.Ref, trimFloat(minVin, 3), trimFloat(m.VinMinV, 2)), []string{p.Ref}, []string{in}, "raise the input or pick a lower-Vin converter")
			}
			switch {
			case maxDuty > 0.95:
				c.add("warn", "regulator-headroom", fmt.Sprintf("%s buck duty reaches %.0f%% (Vin min %s V → %s V): little headroom, near 100%% duty", p.Ref, maxDuty*100, trimFloat(minVin, 3), trimFloat(vout, 3)), []string{p.Ref}, []string{in, out},
					"check the part's max duty / dropout; reduce the input diode drop")
			default:
				c.add("info", "regulator-headroom", fmt.Sprintf("%s buck: Vin min %s V → %s V, duty ≤ %.0f%%", p.Ref, trimFloat(minVin, 3), trimFloat(vout, 3), maxDuty*100), []string{p.Ref}, []string{in, out}, "")
			}
		}
		// Input voltage rating.
		if mm := reVinRange.FindStringSubmatch(p.Description); mm != nil {
			var vmax float64
			fmt.Sscanf(mm[2], "%g", &vmax)
			pk := c.volts[in].Peak
			switch {
			case vmax > 0 && pk > vmax:
				c.add("error", "regulator-vin-max", fmt.Sprintf("%s input %s reaches %s V > rated %s V", p.Ref, in, trimFloat(pk, 3), trimFloat(vmax, 2)), []string{p.Ref}, []string{in}, "clamp the input (TVS) or choose a higher-Vin part")
			case vmax > 0 && pk > 0.9*vmax:
				c.add("warn", "regulator-vin-max", fmt.Sprintf("%s input %s reaches %s V, %.0f%% of rated %s V", p.Ref, in, trimFloat(pk, 3), pk/vmax*100, trimFloat(vmax, 2)), []string{p.Ref}, []string{in}, "hot-plug overshoot can exceed the rating: add input TVS/bulk")
			}
		}
		if rating, src, ok := c.currentRating(p.Ref); ok && maxIout > 0 {
			ratio := maxIout / rating
			switch {
			case ratio > 1:
				c.add("error", "regulator-current", fmt.Sprintf("%s output %s exceeds its %s rating (%s)", p.Ref, fmtA(maxIout), fmtA(rating), src), []string{p.Ref}, []string{out}, "pick a higher-current regulator or split the load")
			case ratio > 0.8:
				c.add("warn", "regulator-current", fmt.Sprintf("%s output %s is %.0f%% of its %s rating", p.Ref, fmtA(maxIout), ratio*100, fmtA(rating)), []string{p.Ref}, []string{out}, "keep ≥ 20 % margin for transients")
			}
		}
	}
}

func (c *ctx) findInductors() {
	if c.worst == nil {
		return
	}
	for _, ref := range sortedKeys(c.worst.Ripple) {
		rp := c.worst.Ripple[ref]
		if c.kind(ref) != pcbauto.KindInductor || rp.IPeakA == 0 {
			continue
		}
		rating, src, ok := c.currentRating(ref)
		nets := c.partNets(ref)
		if !ok {
			c.add("warn", "inductor-rating", fmt.Sprintf("%s carries Ipk %s / Irms %s but its current rating is unknown", ref, fmtA(rp.IPeakA), fmtA(rp.IRmsA)), []string{ref, rp.Regulator}, nets,
				"add maxA (min of Irms and Isat rating) to the inductor's power model")
			continue
		}
		margin := (rating - rp.IPeakA) / rating
		msg := fmt.Sprintf("%s (regulator %s) peak %s / RMS %s vs rated %s (%s): %.0f%% margin on the peak", ref, rp.Regulator, fmtA(rp.IPeakA), fmtA(rp.IRmsA), fmtA(rating), src, margin*100)
		switch {
		case rp.IRmsA > rating || rp.IPeakA > rating:
			c.add("error", "inductor-rating", msg+" — EXCEEDED", []string{ref, rp.Regulator}, nets, "pick an inductor with Isat and Irms ≥ 1.3× the peak")
		case margin < 0.2:
			c.add("warn", "inductor-rating", msg, []string{ref, rp.Regulator}, nets, "choose an inductor rated ≥ 1.3× Ipk (saturation), or confirm Isat separately from the thermal Irms rating")
		default:
			c.add("info", "inductor-rating", msg, []string{ref, rp.Regulator}, nets, "")
		}
	}
}

func (c *ctx) findDiodes() {
	for _, p := range c.d.Parts {
		if c.simKind[p.Ref] != powersim.KindDiode {
			continue
		}
		ia, _, _ := c.maxPinCurrent(p.Ref)
		if ia < 0.05 {
			continue
		}
		// Forward drop in the scenario of the largest current.
		drop, scen := 0.0, ""
		for _, r := range c.scens {
			var va, vk float64
			var cur float64
			for _, pin := range p.Pins {
				nr := r.Nets[pin.Net]
				if nr == nil {
					continue
				}
				for _, pp := range nr.Pins {
					if pp.Ref != p.Ref || pp.Pin != pin.Number {
						continue
					}
					if pp.Dir == "sink" {
						va, cur = nr.Voltage, pp.CurrentA
					} else if pp.Dir == "source" {
						vk = nr.Voltage
					}
				}
			}
			if cur >= ia*0.999 && va > vk {
				drop, scen = va-vk, r.Scenario
			}
		}
		loss := 0.0
		if pr := c.partW(p.Ref); pr != nil {
			loss = pr.PowerW
		}
		msg := fmt.Sprintf("%s (%s) conducts %s: Vf ≈ %s V, loss %s", p.Ref, firstNonEmpty(p.MPN, c.modelID[p.Ref]), fmtA(ia), trimFloat(drop, 3), fmtW(loss))
		if scen != "" {
			msg += " (" + scen + ")"
		}
		sug := ""
		sev := "info"
		if drop > 0.3 && ia >= 0.3 {
			sug = "if the downstream headroom matters, an ideal-diode controller or P-MOSFET OR-ing removes most of this drop"
		}
		if loss > 0.5 {
			sev = "warn"
			sug = "diode dissipates > 0.5 W: check its thermal pad / use an ideal diode"
		}
		c.add(sev, "diode-loss", msg, []string{p.Ref}, c.partNets(p.Ref), sug)
	}
}

func (c *ctx) findRatings() {
	for _, p := range c.d.Parts {
		k := c.simKind[p.Ref]
		if k == powersim.KindBuck || k == powersim.KindLDO || c.kind(p.Ref) == pcbauto.KindInductor {
			continue
		}
		// Current through the part (per pin; a connector's rating is per contact).
		if rating, src, ok := c.currentRating(p.Ref); ok {
			ia, net, pin := c.maxPinCurrent(p.Ref)
			if ia > 0 {
				ratio := ia / rating
				what := fmt.Sprintf("%s pin %s (%s) carries %s vs rated %s (%s)", p.Ref, pin, net, fmtA(ia), fmtA(rating), src)
				switch {
				case ratio > 1:
					c.add("error", "part-rating", what+" — EXCEEDED", []string{p.Ref}, []string{net}, "choose a higher-rated part or split the current over more pins")
				case ratio > 0.8:
					c.add("warn", "part-rating", fmt.Sprintf("%s: %.0f%% of rating", what, ratio*100), []string{p.Ref}, []string{net}, "keep ≥ 20 % margin")
				}
			}
		}
		// Resistor / LED power rating.
		if m := rePowRating.FindStringSubmatch(p.Description); m != nil {
			var pw float64
			fmt.Sscanf(m[1], "%g", &pw)
			pw *= unitScale(m[2])
			if pr := c.partW(p.Ref); pr != nil && pw > 0 && pr.PowerW > 0 {
				ratio := pr.PowerW / pw
				switch {
				case ratio > 1:
					c.add("error", "power-rating", fmt.Sprintf("%s dissipates %s > rated %s", p.Ref, fmtW(pr.PowerW), fmtW(pw)), []string{p.Ref}, c.partNets(p.Ref), "use a larger package / higher-power part")
				case ratio > 0.5:
					c.add("warn", "power-rating", fmt.Sprintf("%s dissipates %s = %.0f%% of rated %s (derate to ≤ 50 %%)", p.Ref, fmtW(pr.PowerW), ratio*100, fmtW(pw)), []string{p.Ref}, c.partNets(p.Ref), "use the next package size up")
				}
			}
		}
		// Capacitor voltage rating vs the net's peak.
		if c.kind(p.Ref) == pcbauto.KindCapacitor {
			if m := reVRating.FindStringSubmatch(p.Description); m != nil {
				var vr float64
				fmt.Sscanf(m[1], "%g", &vr)
				pk := 0.0
				var hot []string
				ac := false
				for _, n := range c.partNets(p.Ref) {
					if !c.isGround(n) {
						v := c.volts[n].Peak
						if c.mains[n] {
							v, ac = c.mainsVrms(), true // X/Y capacitors are rated in Vrms AC
						}
						pk = math.Max(pk, v)
						hot = append(hot, n)
					}
				}
				switch {
				case vr > 0 && pk > vr:
					c.add("error", "cap-voltage", fmt.Sprintf("%s rated %s V sits on %s at %s V%s", p.Ref, trimFloat(vr, 1), strings.Join(hot, "/"), trimFloat(pk, 2), map[bool]string{true: " rms (AC line)", false: " peak"}[ac]), []string{p.Ref}, hot, "use a higher-voltage capacitor (X2/Y-rated on the line)")
				case vr > 0 && pk > 0.8*vr && !ac:
					c.add("warn", "cap-voltage", fmt.Sprintf("%s rated %s V at %s V peak (%.0f%%): MLCC DC-bias loses most of its capacitance", p.Ref, trimFloat(vr, 1), trimFloat(pk, 2), pk/vr*100), []string{p.Ref}, hot, "use ≥ 1.5–2× the working voltage for MLCCs")
				}
			}
		}
	}
}

func (c *ctx) findBulk() {
	check := func(net, who string, minF float64, refs []string) {
		if net == "" {
			return
		}
		caps, total, unknown := c.capsOn(net)
		if total+1e-12 < minF && unknown > 0 {
			c.add("info", "bulk-cap-unknown", fmt.Sprintf("%s: %s capacitance cannot be verified (%d of %s without a value; ≥ %s needed)", who, net, unknown, strings.Join(caps, ", "), fmtF(minF)), append(refs, caps...), []string{net},
				"supply part values (--values / sch list) to check bulk capacitance")
			return
		}
		if total+1e-12 < minF {
			msg := fmt.Sprintf("%s: %s has %s of capacitance to ground (%s), below the %s %s needs", who, net, fmtF(total), strings.Join(caps, ", "), fmtF(minF), who)
			if len(caps) == 0 {
				msg = fmt.Sprintf("%s: no capacitor between %s and ground (%s needs ≥ %s)", who, net, who, fmtF(minF))
			}
			c.add("warn", "bulk-cap", msg, append(refs, caps...), []string{net}, fmt.Sprintf("add ≥ %s low-ESR ceramic/bulk at the pins", fmtF(minF)))
		}
	}
	for _, p := range c.d.Parts {
		switch c.simKind[p.Ref] {
		case powersim.KindBuck:
			check(c.regInRail(p.Ref), p.Ref+" buck input", 10e-6, []string{p.Ref})
			check(c.regOutRail(p.Ref), p.Ref+" buck output", 10e-6, []string{p.Ref})
		case powersim.KindLDO:
			check(c.regInRail(p.Ref), p.Ref+" LDO input", 1e-6, []string{p.Ref})
			check(c.regOutRail(p.Ref), p.Ref+" LDO output", 1e-6, []string{p.Ref})
		}
	}
	for _, bl := range c.out.Blocks {
		if bl.Function != "mcu" && bl.Function != "rf-module" {
			continue
		}
		rail, ia := c.supplyOf(bl.Core)
		if ia >= 0.3 {
			check(rail, bl.Core+" ("+fmtA(ia)+" peak)", 10e-6, []string{bl.Core})
		}
	}
}

func (c *ctx) findUSB() {
	budget := c.spec.USBBudgetA
	if budget <= 0 {
		budget = 0.5
	}
	for _, p := range c.d.Parts {
		if c.simKind[p.Ref] != powersim.KindSource {
			continue
		}
		m := c.model[p.Ref]
		usb := m != nil && strings.EqualFold(m.SourceName, "usb")
		if !usb {
			for _, n := range c.partNets(p.Ref) {
				usb = usb || strings.Contains(strings.ToUpper(n), "VBUS")
			}
		}
		if !usb {
			continue
		}
		// Source current: sum of the connector's source pins, max over scenarios.
		best, scen, net := 0.0, "", ""
		for _, r := range c.scens {
			sum := 0.0
			for _, n := range c.partNets(p.Ref) {
				nr := r.Nets[n]
				if nr == nil || c.isGround(n) {
					continue
				}
				for _, pp := range nr.Pins {
					if pp.Ref == p.Ref && pp.Dir == "source" {
						sum += pp.CurrentA
						net = n
					}
				}
			}
			if sum > best {
				best, scen = sum, r.Scenario
			}
		}
		ratio := best / budget
		msg := fmt.Sprintf("%s draws %s from USB (%s) vs the %s budget (%.0f%%)", p.Ref, fmtA(best), scen, fmtA(budget), ratio*100)
		switch {
		case ratio > 1:
			c.add("error", "usb-budget", msg, []string{p.Ref}, []string{net}, "USB 2.0 default power is 500 mA: negotiate more (USB-C 1.5/3 A advertisement or PD) or cut the load")
		case ratio > 0.8:
			c.add("warn", "usb-budget", msg+" — little margin for inrush/radio bursts", []string{p.Ref}, []string{net}, "a host port may current-limit or brown out; declare usbBudgetA in the spec if the source advertises more")
		default:
			c.add("info", "usb-budget", msg, []string{p.Ref}, []string{net}, "")
		}
	}
}

func (c *ctx) findNets() {
	var mainsNets []string
	defer func() {
		if len(mainsNets) > 0 {
			ia, src := c.mainsCurrent()
			c.add("warn", "mains-current", fmt.Sprintf("line current of %s is not simulated: copper sized for %s (%s)", strings.Join(mainsNets, ", "), fmtA(ia), src), nil, mainsNets,
				"declare the real line/load current per net in spec.rails (a relay-switched load can exceed the module's input current)")
		}
	}()
	for _, net := range sortedKeys(c.out.Nets) {
		np := c.out.Nets[net]
		for _, w := range np.Warnings {
			switch {
			case strings.Contains(w, "floating"):
				c.add("error", "floating-supply", net+": "+w, nil, []string{net}, "the supply has no DC source in the simulation: check connectivity/models")
			case strings.Contains(w, "declared"):
				c.add("warn", "declared-below-sim", net+": "+w, nil, []string{net}, "raise the declared budget or confirm the simulated load")
			default:
				c.add("warn", "net-warning", net+": "+w, nil, []string{net}, "")
			}
		}
		if c.mains[net] && np.CurrentSource == "heuristic" {
			mainsNets = append(mainsNets, net)
			continue
		}
		if (np.Role == "power" || np.Role == "switch") && np.CurrentSource == "heuristic" {
			c.add("warn", "heuristic-current", fmt.Sprintf("%s is sized from a name heuristic (%s), not simulated or declared", net, fmtA(np.CurrentA)), nil, []string{net}, "add a power model / declare the rail in spec.rails")
		}
	}
	// USB data lines at a connector need ESD protection.
	for _, net := range sortedKeys(c.out.Nets) {
		np := c.out.Nets[net]
		if np.Interface != "USB" {
			continue
		}
		conn, prot := "", false
		for _, ref := range c.partsOn(net) {
			if c.kind(ref) == pcbauto.KindConnector {
				conn = ref
			}
			if c.simKind[ref] == powersim.KindESD || c.blockOfPart[ref] != "" && c.blockByID[c.blockOfPart[ref]].Function == "esd" {
				prot = true
			}
		}
		if conn != "" && !prot {
			c.add("warn", "usb-esd", fmt.Sprintf("%s leaves the board at %s without an ESD clamp", net, conn), []string{conn}, []string{net}, "add a low-capacitance USB ESD array (e.g. USBLC6-2SC6) next to the connector")
		}
	}
	if len(c.uncontrolled) > 0 {
		c.add("warn", "impedance-uncontrolled", fmt.Sprintf("%d-layer stackup cannot hold the target impedance of %s", c.layers, strings.Join(c.uncontrolled, ", ")), nil, c.uncontrolled,
			"use a 4-layer stackup with an adjacent reference plane, or keep the pair short (USB full-speed tolerates it)")
	}
	var hs []string
	for _, net := range sortedKeys(c.out.Nets) {
		if np := c.out.Nets[net]; np.ImpedanceOhm > 0 && !has(c.uncontrolled, net) {
			hs = append(hs, fmt.Sprintf("%s %.0f Ω", net, np.ImpedanceOhm))
		}
	}
	if len(hs) > 0 {
		c.add("info", "impedance", "impedance-controlled nets: "+strings.Join(hs, ", ")+" — order the board with the stackup the widths were solved for ("+c.stackName+")", nil, nil, "")
	}
}

func (c *ctx) findSafety() {
	st := c.out.Standard
	if c.hasHazard() && has(st.Defaulted, "name") {
		c.add("warn", "standard-defaulted", "hazardous/mains voltage present but no product standard in the spec: distances use "+st.Name+" "+st.Insulation+" engineering defaults", nil, nil,
			"declare spec.standard (IEC62368-1 / IEC60601-1 / IEC61010-1, insulation, pollution degree, altitude)")
	}
	for _, p := range c.out.Pairs {
		if p.SlotRequired {
			c.add("warn", "insulation-slot", fmt.Sprintf("%s ↔ %s (%s, %s Vrms): creepage %.2f mm needs a %.1f mm slot under %s", p.A, p.B, p.Insulation, trimFloat(p.WorkingVrms, 0), p.CreepageMm, p.SlotWidthMm, strings.Join(p.Bridges, ", ")), p.Bridges, nil,
				"mill the slot in the board outline under the isolation parts and keep copper ≥ clearance from it")
		} else if p.Insulation != "functional" {
			c.add("info", "insulation", fmt.Sprintf("%s ↔ %s: %s insulation, clearance %.2f mm / creepage %.2f mm", p.A, p.B, p.Insulation, p.ClearanceMm, p.CreepageMm), p.Bridges, nil, "keep every copper feature of the two domains apart by these distances")
		}
	}
	for _, d := range c.out.Domains {
		if d.Kind == "floating" {
			c.add("warn", "floating-domain", fmt.Sprintf("parts %s have no ground reference", strings.Join(d.Parts, ", ")), d.Parts, nil, "check that the part is connected, or declare the domain in spec.domains")
		}
	}
}
