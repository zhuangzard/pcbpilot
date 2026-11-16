package intent

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
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
		mains := c.lineTied(pd)
		rms, peak := 0.0, 0.0
		for _, n := range pd.Nets {
			if c.mains[n] {
				continue // the line's own voltage: counted only by a domain on it
			}
			v := c.volts[n]
			rms = math.Max(rms, math.Max(math.Abs(v.Max), math.Abs(v.Min)))
			peak = math.Max(peak, v.Peak)
		}
		switch {
		case mains:
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
				if g, ok := c.circ.GroundDomain[n]; ok && g != pd.ID {
					// A declared ground names its own domain, not one a part
					// across the barrier (Y capacitor, bleeder) reaches it from.
					continue
				}
				if g, ok := c.circ.DeclaredNet[n]; ok && g != pd.ID {
					// Likewise a net of a declared domain without a ground.
					continue
				}
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
	c.applyDeclared(p, a, b)
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

// declaredFor is the largest clearance / creepage the spec declares for
// the pair a|b (spec.domains[] of either side, spec.isolation[] naming
// both), with the declarations that gave them.
func (c *ctx) declaredFor(a, b *Domain) (clr, creep float64, from []string) {
	in := func(d *Domain, ref string) bool {
		ref = strings.TrimSpace(ref)
		if id, ok := strings.CutPrefix(ref, "domain:"); ok {
			return strings.TrimSpace(id) == d.ID
		}
		return has(d.Nets, ref)
	}
	for i, sd := range c.spec.Domains {
		if sd.ClearanceMm <= 0 && sd.CreepageMm <= 0 {
			continue
		}
		for _, n := range sd.Nets {
			if has(a.Nets, n) || has(b.Nets, n) {
				clr, creep = math.Max(clr, sd.ClearanceMm), math.Max(creep, sd.CreepageMm)
				from = append(from, fmt.Sprintf("spec.domains[%d] (%s)", i, n))
				if c.domUsed == nil {
					c.domUsed = map[int]bool{}
				}
				c.domUsed[i] = true
				break
			}
		}
	}
	for i, si := range c.spec.Isolation {
		if in(a, si.A) && in(b, si.B) || in(a, si.B) && in(b, si.A) {
			clr, creep = math.Max(clr, si.ClearanceMm), math.Max(creep, si.CreepageMm)
			w := fmt.Sprintf("spec.isolation[%d] (%s|%s)", i, si.A, si.B)
			if si.Why != "" {
				w += ": " + si.Why
			}
			from = append(from, w)
			if c.isoUsed == nil {
				c.isoUsed = map[int]bool{}
			}
			c.isoUsed[i] = true
		}
	}
	return clr, creep, from
}

// applyDeclared raises the pair to the declared distances: clearance and
// creepage are max(derived, declared) (creepage never below the
// clearance), each with its source. Without a declaration the pair is
// unchanged (no new fields).
func (c *ctx) applyDeclared(p *Pair, a, b *Domain) {
	dc, dr, from := c.declaredFor(a, b)
	if dc <= 0 && dr <= 0 {
		return
	}
	p.DeclaredClearanceMm, p.DeclaredCreepageMm = dc, dr
	p.DerivedClearanceMm, p.DerivedCreepageMm = p.ClearanceMm, p.CreepageMm
	p.ClearanceSource, p.CreepageSource = "derived", "derived"
	if dc > p.ClearanceMm {
		p.ClearanceMm, p.ClearanceSource = dc, "declared"
	}
	if want := math.Max(dr, p.ClearanceMm); want > p.CreepageMm {
		p.CreepageMm, p.CreepageSource = want, "declared"
	}
	p.Why = append(p.Why, fmt.Sprintf("declared distance %s: clearance %s mm (derived %s mm, declared %s mm → %s), creepage %s mm (derived %s mm, declared %s mm → %s; never below the clearance)",
		strings.Join(from, ", "), trimFloat(p.ClearanceMm, 3), trimFloat(p.DerivedClearanceMm, 3), trimFloat(dc, 3), p.ClearanceSource,
		trimFloat(p.CreepageMm, 3), trimFloat(p.DerivedCreepageMm, 3), trimFloat(dr, 3), p.CreepageSource))
}

// findDeclaredIsolation: a spec.isolation entry that matches no insulation
// pair raises nothing — it is a warning, never a silent no-op.
func (c *ctx) findDeclaredIsolation() {
	for i, si := range c.spec.Isolation {
		if c.isoUsed[i] {
			continue
		}
		c.add("warn", "declared-isolation-unmatched", fmt.Sprintf("spec.isolation[%d] (%s|%s) matches no insulation pair: its declared distance is not applied", i, si.A, si.B), nil, []string{si.A, si.B},
			"name a net of each of two domains that an isolation part bridges (or domain:<id> from intent.json domains[]); the pairs are in intent.json pairs[]")
	}
	for i, sd := range c.spec.Domains {
		if sd.ClearanceMm <= 0 && sd.CreepageMm <= 0 {
			continue
		}
		if !c.domUsed[i] {
			c.add("warn", "declared-isolation-unmatched", fmt.Sprintf("spec.domains[%d] declares clearance %s / creepage %s mm but its domain is in no insulation pair: not applied", i, trimFloat(sd.ClearanceMm, 3), trimFloat(sd.CreepageMm, 3)), nil, sd.Nets,
				"check that the domain's nets are on the barrier an isolation part bridges")
		}
	}
}

// lineKind reports whether a declared domain kind may be on the AC line.
func lineKind(kind string) bool { return kind == "mains" || kind == "hazardous" }

// lineTied reports whether pcbauto domain pd is galvanically on the AC line:
// it is the line itself, or a conducting part of it sits on a line net. A
// capacitor (Y capacitor), an isolation part and a resistor that is not
// known to be < 100 kΩ (sense divider, bleeder) reach the line without
// making their domain part of it. (Before 2026-11-16 one line net among a
// domain's nets — the far end of a divider — made the whole domain mains.)
func (c *ctx) lineTied(pd *pcbauto.Domain) bool {
	if pd.ID == "MAINS" {
		return true
	}
	for _, ref := range pd.Parts {
		p := c.parts[ref]
		k := c.circ.Kinds[ref]
		if p == nil || k.Bridges() || k == pcbauto.KindCapacitor {
			continue
		}
		if v, ok := pcbauto.ResistorOhms(p.Value); k == pcbauto.KindResistor && (!ok || v >= pcbauto.BleederOhms) {
			continue
		}
		for _, pin := range p.Pins {
			if c.mains[pin.Net] {
				return true
			}
		}
	}
	return false
}

// floodShare is the share of the board's nets above which one hazardous
// domain, on a board that carries isolation parts, is reported when it was
// assembled from several grounds.
const floodShare = 0.6

// checkDomainFlood warns when one derived mains / hazardous domain swallowed
// most of a board that has isolation parts, and names the parts that joined
// the grounds into it: nearly always a part across a barrier taken for a
// wire, or a signal net carrying a line name.
func (c *ctx) checkDomainFlood() {
	total := len(c.d.Nets())
	barrier := false
	for _, k := range c.circ.Kinds {
		barrier = barrier || k.Bridges()
	}
	if total == 0 || !barrier {
		return
	}
	for _, pd := range c.circ.Domains {
		d := c.domOfPcb[pd.ID]
		if d == nil || (d.Kind != "mains" && d.Kind != "hazardous") || float64(len(pd.Nets)) <= floodShare*float64(total) {
			continue
		}
		if _, declared := c.domSpec[d.ID]; declared {
			continue
		}
		grounds := map[string]bool{}
		for g, dom := range c.circ.GroundDomain {
			if dom == pd.ID {
				grounds[g] = true
			}
		}
		// A tie is doubtful unless it is clearly meant (0 Ω, net-tie, declared
		// winding); the first ground on the line is the board's own primary.
		doubtful, lineGrounds := 0, map[string]bool{}
		var ties []pcbauto.GroundTie
		for _, t := range c.circ.GroundTies {
			if !grounds[t.A] {
				continue
			}
			ties = append(ties, t)
			switch {
			case t.Line:
				lineGrounds[t.A] = true
			case !t.Sure:
				doubtful++
			}
		}
		if n := len(lineGrounds) - 1; n > 0 {
			doubtful += n
		}
		if len(grounds) < 2 || doubtful == 0 {
			continue
		}
		// Nets hanging on each ground: what a tie brought into the domain.
		reach := map[string]map[string]bool{}
		for _, p := range c.d.Parts {
			nets := distinctNets(p.Pins, nil)
			for _, g := range nets {
				if grounds[g] {
					if reach[g] == nil {
						reach[g] = map[string]bool{}
					}
					for _, n := range nets {
						reach[g][n] = true
					}
				}
			}
		}
		type via struct {
			ref, what string
			gain      int
		}
		best := map[string]*via{}
		for _, t := range ties {
			gain, what := len(reach[t.A]), fmt.Sprintf("ties %s to line net %s", t.A, t.B)
			if !t.Line {
				gain = min(gain, len(reach[t.B]))
				what = fmt.Sprintf("joins %s and %s", t.A, t.B)
				if p := c.parts[t.Ref]; p != nil && c.circ.Kinds[t.Ref] == pcbauto.KindResistor {
					if _, ok := pcbauto.ResistorOhms(p.Value); !ok {
						what += ", value unknown"
					}
				}
			}
			if v := best[t.Ref]; v == nil || gain > v.gain {
				best[t.Ref] = &via{t.Ref, what, gain}
			}
		}
		vias := make([]*via, 0, len(best))
		for _, v := range best {
			vias = append(vias, v)
		}
		sort.Slice(vias, func(i, j int) bool {
			if vias[i].gain != vias[j].gain {
				return vias[i].gain > vias[j].gain
			}
			return vias[i].ref < vias[j].ref
		})
		var refs, list []string
		for i, v := range vias {
			if i == 5 {
				list = append(list, fmt.Sprintf("… %d more", len(vias)-i))
				break
			}
			refs = append(refs, v.ref)
			list = append(list, fmt.Sprintf("%s (%s, ≈ %d nets)", v.ref, v.what, v.gain))
		}
		gs := make([]string, 0, len(grounds))
		for g := range grounds {
			gs = append(gs, g)
		}
		sort.Strings(gs)
		c.add("warn", "domain-flood", fmt.Sprintf("domain %s (%s) holds %d of the board's %d nets (%.0f %%) although the board has isolation parts: %d grounds were joined into it, through %s — check that each is a real connection and not a part across a barrier",
			d.ID, d.Kind, len(pd.Nets), total, 100*float64(len(pd.Nets))/float64(total), len(grounds), strings.Join(list, "; ")),
			refs, gs,
			`give a resistor between two grounds its value (≥ 100 kΩ is a bleeder, not a tie), name safety capacitors C… / CX… / CY…, rename a signal net that is called L / N / PE / LINE but is not the AC line, or declare the domains in the intent spec ("domains": [{"kind": "SELV", "nets": ["<its ground>"]}]): declared domains are never joined to another one or to the line`)
	}
}
