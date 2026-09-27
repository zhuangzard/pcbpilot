package intent

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// SELV limits (IEC 61140 / 62368-1 ES1): 60 V DC, 30 Vrms / 42.4 V peak AC.
const (
	selvDC    = 60.0
	selvACrms = 30.0
)

func (c *ctx) mainsVrms() float64 {
	if c.spec.Mains != nil && c.spec.Mains.Vrms > 0 {
		return c.spec.Mains.Vrms
	}
	return 230
}

func (c *ctx) hasHazard() bool {
	for _, d := range c.out.Domains {
		if d.Kind == "mains" || d.Kind == "hazardous" {
			return true
		}
	}
	return false
}

// buildDomains maps pcbauto's reference domains (one per ground, joined by
// net-ties; MAINS for line nets; UNREFERENCED for parts without a ground) to
// intent domains with a kind and working voltage.
func (c *ctx) buildDomains() {
	c.volts = map[string]Voltage{}
	for _, net := range c.d.Nets() {
		v, _ := c.netVoltageEnvelope(net)
		c.volts[net] = v
	}
	c.domOfPcb = map[string]*Domain{}
	main := ""
	mainN := -1
	for _, pd := range c.circ.Domains {
		if pd.ID != "MAINS" && pd.ID != "UNREFERENCED" && len(pd.Parts) > mainN {
			main, mainN = pd.ID, len(pd.Parts)
		}
	}
	isoFromMain := map[string]bool{}
	for _, br := range c.circ.Barriers {
		if br.A == main {
			isoFromMain[br.B] = true
		}
		if br.B == main {
			isoFromMain[br.A] = true
		}
	}
	used := map[string]bool{}
	for _, pd := range c.circ.Domains {
		d := &Domain{Nets: append([]string(nil), pd.Nets...), Reference: pd.Ground, Parts: sortRefs(append([]string(nil), pd.Parts...))}
		sort.Strings(d.Nets)
		mains := false
		rms, peak := 0.0, 0.0
		for _, n := range pd.Nets {
			if c.mains[n] {
				mains = true
			}
			v := c.volts[n]
			rms = math.Max(rms, math.Max(math.Abs(v.Max), math.Abs(v.Min)))
			peak = math.Max(peak, v.Peak)
		}
		switch {
		case mains || pd.ID == "MAINS":
			d.Kind = "mains"
			rms, peak = c.mainsVrms(), c.mainsVrms()*math.Sqrt2
			d.Why = append(d.Why, fmt.Sprintf("AC line nets present: %s Vrms (%s V peak)", trimFloat(rms, 0), trimFloat(peak, 0)))
		case peak > selvDC:
			d.Kind = "hazardous"
			d.Why = append(d.Why, fmt.Sprintf("peak %s V exceeds the SELV limit %.0f V DC", trimFloat(peak, 2), selvDC))
		case pd.ID == "UNREFERENCED":
			d.Kind = "floating"
			d.Why = append(d.Why, "parts with no ground reference of their own")
		case pd.ID != main && isoFromMain[pd.ID]:
			d.Kind = "isolated-secondary"
			d.Why = append(d.Why, fmt.Sprintf("separate ground %s reached from %s only through an isolation part", pd.Ground, main))
		default:
			d.Kind = "SELV"
			d.Why = append(d.Why, fmt.Sprintf("≤ %.0f V DC / %.0f Vrms: SELV (IEC 61140 / 62368-1 ES1)", selvDC, selvACrms))
		}
		for _, sd := range c.spec.Domains {
			for _, n := range sd.Nets {
				if has(d.Nets, n) {
					d.Kind = sd.Kind
					if sd.WorkingVrms > 0 {
						rms, peak = sd.WorkingVrms, math.Max(peak, sd.WorkingVrms*math.Sqrt2)
					}
					d.Why = append(d.Why, "kind "+sd.Kind+" declared in spec.domains (net "+n+")")
					break
				}
			}
		}
		d.WorkingVrms, d.WorkingVpeak = round(rms, 3), round(peak, 3)
		prefix := map[string]string{"SELV": "SELV", "hazardous": "HAZ", "mains": "MAINS", "floating": "FLOAT", "isolated-secondary": "ISO", "patient": "PATIENT"}[d.Kind]
		label := voltLabel(rms)
		if d.Kind == "mains" {
			label += "AC"
		}
		id := prefix + "_" + label
		if d.Kind == "floating" {
			id = prefix
		}
		if used[id] {
			id += "_" + sanitizeID(firstNonEmpty(pd.Ground, pd.ID))
		}
		used[id] = true
		d.ID = id
		c.domOfPcb[pd.ID] = d
		c.out.Domains = append(c.out.Domains, d)
	}
	// Net → domain: the domain with the most parts on the net.
	c.domOfNet = map[string]string{}
	for _, net := range c.d.Nets() {
		count := map[string]int{}
		for _, ref := range c.partsOn(net) {
			if d := c.domOfPcb[c.circ.DomainOf[ref]]; d != nil {
				count[d.ID]++
			}
		}
		best, bestN := "", 0
		for _, id := range sortedKeys(count) {
			if count[id] > bestN {
				best, bestN = id, count[id]
			}
		}
		c.domOfNet[net] = best
	}
}

func (c *ctx) domain(id string) *Domain {
	for _, d := range c.out.Domains {
		if d.ID == id {
			return d
		}
	}
	return nil
}

func (c *ctx) assignDomainsToBlocks() {
	for _, bl := range c.out.Blocks {
		if d := c.domOfPcb[c.circ.DomainOf[bl.Core]]; d != nil {
			bl.Domain = d.ID
		} else {
			for _, ref := range bl.Parts {
				if d := c.domOfPcb[c.circ.DomainOf[ref]]; d != nil {
					bl.Domain = d.ID
					break
				}
			}
		}
	}
}

func hazardKind(k string) bool { return k == "mains" || k == "hazardous" }

// declaredIsolation is the largest spec.domains isolationVrms declared for
// either domain of a pair (by any of its nets).
func (c *ctx) declaredIsolation(a, b *Domain) float64 {
	best := 0.0
	for _, sd := range c.spec.Domains {
		if sd.IsolationVrms <= 0 {
			continue
		}
		for _, n := range sd.Nets {
			if has(a.Nets, n) || has(b.Nets, n) {
				best = math.Max(best, sd.IsolationVrms)
			}
		}
	}
	return best
}

// buildPairs states the insulation between every pair of domains an
// isolation part (optocoupler, isolator, isolated DC/DC, transformer, relay)
// bridges. The distances come from SafetyDistances.
func (c *ctx) buildPairs() {
	st := c.out.Standard
	specInsul := c.spec.Standard != nil && c.spec.Standard.Insulation != ""
	for _, br := range c.circ.Barriers {
		a, b := c.domOfPcb[br.A], c.domOfPcb[br.B]
		if a == nil || b == nil {
			continue
		}
		p := &Pair{A: "domain:" + a.ID, B: "domain:" + b.ID, Bridges: sortRefs(append([]string(nil), br.Bridges...))}
		p.WorkingVrms = math.Max(a.WorkingVrms, b.WorkingVrms)
		p.WorkingVpeak = math.Max(a.WorkingVpeak, b.WorkingVpeak)
		ha, hb := hazardKind(a.Kind), hazardKind(b.Kind)
		patient := a.Kind == "patient" || b.Kind == "patient"
		switch {
		case ha != hb:
			p.Insulation = "reinforced"
			if specInsul {
				p.Insulation = st.Insulation
			}
			p.Why = append(p.Why, fmt.Sprintf("%s (%s) ↔ %s (%s): hazardous to touchable circuit needs %s insulation", a.ID, a.Kind, b.ID, b.Kind, p.Insulation))
		case ha && hb:
			p.Insulation = "basic"
			p.Why = append(p.Why, "both sides hazardous: basic (operational) insulation")
		case patient:
			p.Insulation = "basic"
			p.Why = append(p.Why, "SELV ↔ patient circuit: 1 means of patient protection")
		default:
			p.Insulation = "functional"
			p.Why = append(p.Why, "both sides SELV: functional isolation (noise / ground loop), not a safety barrier")
		}
		if iso := c.declaredIsolation(a, b); iso > 0 {
			p.RequiredWithstandV = round(iso*math.Sqrt2, 1)
			if p.Insulation == "functional" {
				p.Insulation = "basic"
			}
			p.Why = append(p.Why, fmt.Sprintf("spec.domains isolationVrms %s Vrms (electric strength, e.g. IEEE 802.3 MDI): dimensioned as %s insulation for a %s V peak withstand (IEC 60664-1 procedure 2)", trimFloat(iso, 0), p.Insulation, trimFloat(p.RequiredWithstandV, 0)))
		}
		if st.MOP != "" {
			p.MOP = st.MOP
			p.MOPCount = 1
			if ha != hb && (p.Insulation == "reinforced" || p.Insulation == "double") {
				p.MOPCount = 2
			}
			if p.Insulation == "functional" {
				p.MOPCount = 0
			}
		}
		// The single call site for the distance numbers (pkg/safety plugs in via SafetyProvider).
		cl, cr, slot, slotW, ref, why := SafetyDistances(*p, st)
		p.ClearanceMm, p.CreepageMm, p.SlotRequired, p.SlotWidthMm, p.StandardRef = cl, cr, slot, slotW, ref
		p.Why = append(p.Why, why...)
		p.Why = append(p.Why, fmt.Sprintf("working voltage %s Vrms / %s V peak = the higher of the two domains; bridged by %s", trimFloat(p.WorkingVrms, 2), trimFloat(p.WorkingVpeak, 2), strings.Join(p.Bridges, ", ")))
		c.out.Pairs = append(c.out.Pairs, p)
	}
	sort.SliceStable(c.out.Pairs, func(i, j int) bool { return c.out.Pairs[i].A+c.out.Pairs[i].B < c.out.Pairs[j].A+c.out.Pairs[j].B })
}
