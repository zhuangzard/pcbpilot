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
	c.floatingIslands()
	c.domOfPcb = map[string]*Domain{}
	c.domSpec = map[string]SpecDomain{}
	declared := map[*Domain]SpecDomain{}
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
				continue
			}
			v := c.volts[n]
			rms = math.Max(rms, math.Max(math.Abs(v.Max), math.Abs(v.Min)))
			peak = math.Max(peak, v.Peak)
		}
		switch {
		case mains || pd.ID == "MAINS":
			d.Kind = "mains"
			line := c.mainsVrms()
			d.Why = append(d.Why, fmt.Sprintf("AC line nets present: %s Vrms (%s V peak)", trimFloat(line, 0), trimFloat(line*math.Sqrt2, 0)))
			if rms > line || peak > line*math.Sqrt2 {
				// The rectified primary of an off-line converter is the same
				// circuit as the line: its bulk (DC) and drain (peak) set the
				// working voltage to the other side of the barrier.
				d.Why = append(d.Why, fmt.Sprintf("mains-connected primary: bulk/DC up to %s V, peak %s V (rectifier-tied nets of this domain)", trimFloat(rms, 1), trimFloat(peak, 1)))
			}
			rms, peak = math.Max(line, rms), math.Max(line*math.Sqrt2, peak)
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
					declared[d] = sd
					break
				}
			}
		}
		d.WorkingVrms, d.WorkingVpeak = round(rms, 3), round(peak, 3)
		prefix := map[string]string{"SELV": "SELV", "hazardous": "HAZ", "mains": "MAINS", "floating": "FLOAT", "isolated-secondary": "ISO", "patient": "PATIENT"}[d.Kind]
		label := voltLabel(rms)
		if d.Kind == "mains" {
			if mains {
				label = voltLabel(c.mainsVrms()) // named after the line, not the bulk
			}
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
		if sd, ok := declared[d]; ok {
			c.domSpec[id] = sd
		}
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
	// A net belongs to ONE domain: a bridging part (a Y capacitor from the
	// primary ground to the secondary ground) put the secondary ground into
	// the primary's net list too, and consumers that read Domains[].nets
	// (board-edge bands) gave the SELV ground the mains edge distance.
	for _, d := range c.out.Domains {
		kept := d.Nets[:0]
		for _, n := range d.Nets {
			if o := c.domOfNet[n]; o == "" || o == d.ID {
				kept = append(kept, n)
			}
		}
		d.Nets = kept
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
// bridges, and between every hazardous and touchable domain even when no part
// bridges them (their copper must still keep the distance anywhere on the
// board). The distances come from SafetyDistances.
func (c *ctx) buildPairs() {
	seen := map[[2]string]bool{}
	key := func(a, b string) [2]string {
		if a > b {
			a, b = b, a
		}
		return [2]string{a, b}
	}
	for _, br := range c.circ.Barriers {
		a, b := c.domOfPcb[br.A], c.domOfPcb[br.B]
		if a == nil || b == nil || a == b || seen[key(a.ID, b.ID)] {
			continue
		}
		seen[key(a.ID, b.ID)] = true
		c.out.Pairs = append(c.out.Pairs, c.pairFor(a, b, sortRefs(append([]string(nil), br.Bridges...)), ""))
	}
	for i, a := range c.out.Domains {
		for _, b := range c.out.Domains[i+1:] {
			if seen[key(a.ID, b.ID)] || a.Kind == "floating" || b.Kind == "floating" || hazardKind(a.Kind) == hazardKind(b.Kind) {
				continue
			}
			seen[key(a.ID, b.ID)] = true
			note := fmt.Sprintf("no isolation part bridges %s and %s, but hazardous copper must keep the insulation distance from touchable copper anywhere on the board", a.ID, b.ID)
			c.out.Pairs = append(c.out.Pairs, c.pairFor(a, b, nil, note))
		}
	}
	sort.SliceStable(c.out.Pairs, func(i, j int) bool { return c.out.Pairs[i].A+c.out.Pairs[i].B < c.out.Pairs[j].A+c.out.Pairs[j].B })
}

// pairFor states one insulation pair.
func (c *ctx) pairFor(a, b *Domain, bridges []string, note string) *Pair {
	st := c.out.Standard
	specInsul := c.spec.Standard != nil && c.spec.Standard.Insulation != ""
	// Every part with pins in both domains bridges the pair — a Y capacitor
	// or a sense resistor is not an isolation part, but its body spans the
	// barrier all the same (pcb auto slots / checks it like one).
	p := &Pair{A: "domain:" + a.ID, B: "domain:" + b.ID, Bridges: sortRefs(uniq(append(append([]string(nil), bridges...), c.spanning(a.ID, b.ID)...)))}
	if note != "" {
		p.Why = append(p.Why, note)
	}
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
		if specInsul && st.Insulation != "functional" {
			p.Insulation = st.Insulation
		}
		p.Why = append(p.Why, fmt.Sprintf("SELV ↔ patient circuit: %s insulation (means of patient protection)", p.Insulation))
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
	if st.MOP != "" && p.Insulation != "functional" {
		p.MOP = st.MOP
		// The product declares how many means of protection its barriers
		// provide (2 × MOPP for a type BF/CF applied part at mains potential);
		// both-hazardous operational insulation is never a means of protection.
		p.MOPCount = 1
		switch {
		case ha && hb:
		case st.MOPCount > 0:
			p.MOPCount = st.MOPCount
		case p.Insulation == "reinforced" || p.Insulation == "double":
			p.MOPCount = 2
		}
		if p.MOPCount >= 2 && p.Insulation != "reinforced" {
			p.Insulation = "double"
		} else if p.MOPCount == 1 && (p.Insulation == "reinforced" || p.Insulation == "double") {
			p.Insulation = "basic"
		}
		p.Why = append(p.Why, fmt.Sprintf("%d × %s declared by spec.standard (mopCount)", p.MOPCount, p.MOP))
	}
	var tw string
	p.Transient, p.MainsVrms, tw = c.pairTransient(a, b)
	if tw != "" {
		p.Why = append(p.Why, tw)
	}
	// The single call site for the distance numbers (pkg/safety plugs in via SafetyProvider).
	cl, cr, slot, slotW, ref, why := SafetyDistances(*p, st)
	p.ClearanceMm, p.CreepageMm, p.SlotRequired, p.SlotWidthMm, p.StandardRef = cl, cr, slot, slotW, ref
	p.Why = append(p.Why, why...)
	by := "no part (implicit pair)"
	if len(p.Bridges) > 0 {
		by = strings.Join(p.Bridges, ", ")
	}
	p.Why = append(p.Why, fmt.Sprintf("working voltage %s Vrms / %s V peak = the higher of the two domains; bridged by %s", trimFloat(p.WorkingVrms, 2), trimFloat(p.WorkingVpeak, 2), by))
	return p
}

// pairTransient resolves the transient regime of a pair (clearance
// procedure 2) and the nominal system voltage it is taken from: a
// spec.domains declaration wins; a mains-kind side is mains-connected; in a
// design with a mains domain a hazardous side is a primary circuit (mains
// transients reach it through the rectifier) and two touchable sides sit
// behind the isolating transformer; otherwise the standard's rule infers it.
func (c *ctx) pairTransient(a, b *Domain) (string, float64, string) {
	rank := map[string]int{"none": 1, "secondary": 2, "mains": 3}
	best, v, who := "", 0.0, ""
	for _, d := range []*Domain{a, b} {
		sd, ok := c.domSpec[d.ID]
		if !ok || sd.Transient == "" || rank[sd.Transient] <= rank[best] {
			continue
		}
		best, v, who = sd.Transient, sd.RatedVrms, d.ID
		if v <= 0 {
			v = d.WorkingVrms
		}
	}
	if best != "" {
		return best, v, fmt.Sprintf("transient %s at %s V nominal declared in spec.domains (%s)", best, trimFloat(v, 0), who)
	}
	for _, d := range []*Domain{a, b} {
		if d.Kind == "mains" {
			v := c.mainsNominal(d)
			return "mains", v, fmt.Sprintf("%s is connected to the supply network: mains transient at %s Vrms (overvoltage category %s)", d.ID, trimFloat(v, 0), c.out.Standard.OvervoltageCategory)
		}
	}
	var mains *Domain
	for _, d := range c.out.Domains {
		if d.Kind == "mains" && (mains == nil || c.mainsNominal(d) > c.mainsNominal(mains)) {
			mains = d
		}
	}
	if mains == nil {
		return "", 0, ""
	}
	v = c.mainsNominal(mains)
	if hazardKind(a.Kind) || hazardKind(b.Kind) {
		return "mains", v, fmt.Sprintf("primary circuit: galvanically connected to %s through the rectifier, so the mains transient at %s Vrms applies", mains.ID, trimFloat(v, 0))
	}
	return "secondary", v, fmt.Sprintf("both sides behind the isolating barrier from %s: secondary-circuit transient (one overvoltage category lower) at %s Vrms", mains.ID, trimFloat(v, 0))
}

// mainsNominal is the supply-network voltage a mains-kind domain carries:
// the line voltage when it holds line nets (not its rectified bulk), else
// its declared working voltage (a CAT III measuring circuit at 600 V).
func (c *ctx) mainsNominal(d *Domain) float64 {
	for _, n := range d.Nets {
		if c.mains[n] {
			return c.mainsVrms()
		}
	}
	return d.WorkingVrms
}

// spanning lists the parts with pins in both domains: besides the isolation
// part itself, the Y / chassis capacitors and resistors that cross the
// barrier (an Ethernet chassis-to-GND 1 nF/2 kV cap) — each needs the
// insulation's voltage rating and sits in the barrier's creepage path.
func (c *ctx) spanning(a, b string) []string {
	var out []string
	for _, p := range c.d.Parts {
		ina, inb := false, false
		for _, n := range c.partNets(p.Ref) {
			switch c.domOfNet[n] {
			case a:
				ina = true
			case b:
				inb = true
			}
		}
		if ina && inb {
			out = append(out, p.Ref)
		}
	}
	return out
}
