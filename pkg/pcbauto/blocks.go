package pcbauto

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Core vs auxiliary.
//
// A core is a part that owns a function: an IC or module, a connector (the
// board's port to the world), or an isolation device. Every other part is an
// auxiliary that exists to serve one core — and usually one specific core
// pin. Knowing *which* pin and *why* is what lets the placer put a decap at
// the power pin it decouples, a TVS at the connector pin it clamps and a load
// capacitor at the crystal, instead of merely "near the chip".
//
// Assignment runs in priority order; the first rule that matches wins:
//
//  1. protection  — ESD/TVS/varistor/fuse on a connector net      → that connector pin
//  2. decap       — capacitor between a rail and ground            → an IC/module power pin on that rail
//  3. clock       — crystal/oscillator                              → the IC on its clock nets
//     clock-load  — capacitor from a crystal net to ground          → same IC
//  4. power-stage — inductor/diode/cap on a switch node, or an inductor
//                   on a regulator pin                              → that regulator
//  5. pull        — resistor from a signal net to a rail or ground  → the IC pin on that signal
//  6. signal      — anything else sharing signal nets with cores; ICs count
//                   double a connector (the driver owns its series parts)
//  7. chain       — reached only through another auxiliary (RC filters, dividers)

var reProtectDev = regexp.MustCompile(`(?i)(ESD|TVS|PESD|SMAJ|SMBJ|SMCJ|SMF\d|P6KE|USBLC|SRV0|LESD|ULC\d|PRTR|RCLAMP|TPD\d|VARISTOR|MOV|FUSE|PTC|SMD\d{3,4}P)`)

func isProtectionPart(c *Circuit, p *Part) bool {
	if c.Kinds[p.Ref] == KindFuse {
		return true
	}
	ref := upper(p.Ref)
	if strings.HasPrefix(ref, "TVS") || strings.HasPrefix(ref, "ESD") || strings.HasPrefix(ref, "RV") {
		return true
	}
	return c.Kinds[p.Ref] == KindDiode && reProtectDev.MatchString(p.Device)
}

func isCore(k PartKind) bool {
	switch k {
	case KindIC, KindModule, KindConnector, KindAntenna, KindOpto, KindIsolator, KindIsoPower, KindTransformer, KindRelay:
		return true
	}
	return false
}

func (c *Circuit) buildBlocks(b *Board, an *Analysis) {
	role := func(net string) NetRole { return an.Plan(net, b.Rules).Role }
	global := func(net string) bool { r := role(net); return r == RoleGround || r == RolePower }

	var cores []*Part
	for _, p := range b.Parts {
		if isCore(c.Kinds[p.Ref]) {
			cores = append(cores, p)
		}
	}
	sort.SliceStable(cores, func(i, j int) bool {
		if len(cores[i].Pads) != len(cores[j].Pads) {
			return len(cores[i].Pads) > len(cores[j].Pads)
		}
		return cores[i].Ref < cores[j].Ref
	})
	byCore := map[string]*Block{}
	for _, p := range cores {
		bl := &Block{ID: "B-" + p.Ref, Core: p.Ref, Parts: []string{p.Ref}, Domain: c.DomainOf[p.Ref], Kind: c.coreKind(b, an, p)}
		byCore[p.Ref] = bl
		c.Blocks = append(c.Blocks, bl)
		c.BlockOf[p.Ref] = bl.ID
	}
	isIC := func(p *Part) bool { k := c.Kinds[p.Ref]; return k == KindIC || k == KindModule }
	// Pads of cores per net.
	corePads := map[string][]*Pad{}
	for _, p := range cores {
		for _, pd := range p.Pads {
			if pd.Net != "" {
				corePads[pd.Net] = append(corePads[pd.Net], pd)
			}
		}
	}
	assign := func(p *Part, core, roleName string, pin *Pad, why string) {
		bl := byCore[core]
		m := Member{Ref: p.Ref, Role: roleName, Why: why}
		if pin != nil {
			m.Pin = pin.Key()
		}
		bl.Parts = append(bl.Parts, p.Ref)
		bl.Members = append(bl.Members, m)
		c.BlockOf[p.Ref] = bl.ID
	}
	crystalNets := map[string]string{} // net → owning IC
	// Switch nodes: a non-rail net joining an IC pin and an inductor pad is the
	// converter's SW/LX node even when the schematic left it unnamed.
	swOwner := map[string]*Pad{}
	for _, p := range b.Parts {
		if c.Kinds[p.Ref] != KindInductor {
			continue
		}
		for _, pd := range p.Pads {
			if pd.Net == "" || global(pd.Net) {
				continue
			}
			for _, cp := range corePads[pd.Net] {
				if isIC(b.Part(cp.Part)) {
					swOwner[pd.Net] = cp
				}
			}
		}
	}
	decapLoad := map[string]int{} // core → decaps given
	var pending []*Part

	// Crystals first: their nets define clock-load caps.
	for _, p := range b.Parts {
		if isCore(c.Kinds[p.Ref]) || c.Kinds[p.Ref] != KindCrystal {
			continue
		}
		best, bestPin, bestN := "", (*Pad)(nil), 0
		counts := map[string]int{}
		pins := map[string]*Pad{}
		for _, pd := range p.Pads {
			if pd.Net == "" || global(pd.Net) {
				continue
			}
			for _, cp := range corePads[pd.Net] {
				if isIC(b.Part(cp.Part)) {
					counts[cp.Part]++
					pins[cp.Part] = cp
				}
			}
		}
		for core, n := range counts {
			if n > bestN || n == bestN && core < best {
				best, bestN, bestPin = core, n, pins[core]
			}
		}
		if best == "" {
			pending = append(pending, p)
			continue
		}
		for _, pd := range p.Pads {
			if pd.Net != "" && !global(pd.Net) {
				crystalNets[pd.Net] = best
			}
		}
		assign(p, best, "clock", bestPin, "crystal on "+best+" clock pins")
	}

	for _, p := range b.Parts {
		if isCore(c.Kinds[p.Ref]) || c.BlockOf[p.Ref] != "" {
			continue
		}
		// Sorted, not a map: several rules below keep the first/last match,
		// and map order made the same board place differently run to run.
		var nets []string
		seenNet := map[string]bool{}
		for _, pd := range p.Pads {
			if pd.Net != "" && !seenNet[pd.Net] {
				seenNet[pd.Net] = true
				nets = append(nets, pd.Net)
			}
		}
		sort.Strings(nets)
		// 1. protection
		if isProtectionPart(c, p) {
			var pin *Pad
			for _, pd := range p.Pads {
				if pd.Net == "" || role(pd.Net) == RoleGround {
					continue
				}
				for _, cp := range corePads[pd.Net] {
					if c.Kinds[cp.Part] == KindConnector {
						pin = cp
						break
					}
				}
				if pin != nil {
					break
				}
			}
			if pin != nil {
				assign(p, pin.Part, "protection", pin, "clamps "+pin.Key()+" ("+pin.Net+")")
				continue
			}
		}
		// 2. decap (IC/module power pins only; spread by power-pin count)
		if c.isDecap(b, an, p) {
			rail := c.decapRail(b, an, p)
			best, bestRatio := "", math.Inf(1)
			var pinsOf []*Pad
			for _, cp := range corePads[rail] {
				if !isIC(b.Part(cp.Part)) {
					continue
				}
				n := 0
				for _, q := range corePads[rail] {
					if q.Part == cp.Part {
						n++
					}
				}
				ratio := float64(decapLoad[cp.Part]) / float64(n)
				if ratio < bestRatio || ratio == bestRatio && cp.Part < best {
					best, bestRatio = cp.Part, ratio
				}
			}
			if best != "" {
				for _, cp := range corePads[rail] {
					if cp.Part == best {
						pinsOf = append(pinsOf, cp)
					}
				}
				pin := pinsOf[decapLoad[best]%len(pinsOf)]
				decapLoad[best]++
				assign(p, best, "decap", pin, "decouples "+rail+" at "+pin.Key())
				continue
			}
		}
		// 3. clock-load cap
		if c.Kinds[p.Ref] == KindCapacitor {
			done := false
			for _, n := range nets {
				if ic := crystalNets[n]; ic != "" {
					assign(p, ic, "clock-load", nil, "load capacitor on crystal net "+n)
					done = true
					break
				}
			}
			if done {
				continue
			}
		}
		// 4. power stage: switch node (named or inferred) or inductor on a regulator pin
		if pin := stageOwner(p, swOwner); pin != nil {
			assign(p, pin.Part, "power-stage", pin, "on the switch node of "+pin.Part+" ("+pin.Net+")")
			continue
		}
		if sw := switchNet(an, b, p); sw != "" {
			for _, cp := range corePads[sw] {
				if isIC(b.Part(cp.Part)) {
					assign(p, cp.Part, "power-stage", cp, "on switch node "+sw)
					break
				}
			}
			if c.BlockOf[p.Ref] != "" {
				continue
			}
		}
		if c.Kinds[p.Ref] == KindInductor {
			var pin *Pad
			for _, n := range nets {
				for _, cp := range corePads[n] {
					if isIC(b.Part(cp.Part)) && c.coreKind(b, an, b.Part(cp.Part)) == "power" {
						pin = cp
					}
				}
			}
			if pin != nil {
				assign(p, pin.Part, "power-stage", pin, "inductor of regulator "+pin.Part)
				continue
			}
		}
		// 4b. pin filter: a capacitor from an IC signal pin to ground/rail
		// (reset RC, ADC/BOOT debounce) must sit at that pin — as a plain
		// "signal" member the ESP32 EN cap landed 10 mm from EN.
		if c.Kinds[p.Ref] == KindCapacitor && len(p.Pads) == 2 {
			a, bb := p.Pads[0].Net, p.Pads[1].Net
			sig := ""
			switch {
			case global(a) && bb != "" && !global(bb):
				sig = bb
			case global(bb) && a != "" && !global(a):
				sig = a
			}
			if sig != "" && crystalNets[sig] == "" {
				var pin *Pad
				for _, cp := range corePads[sig] {
					if isIC(b.Part(cp.Part)) {
						pin = cp
						break
					}
				}
				if pin != nil {
					assign(p, pin.Part, "pin-filter", pin, "filters "+pin.Key()+" ("+sig+")")
					continue
				}
			}
		}
		// 5. pull-up / pull-down
		if c.Kinds[p.Ref] == KindResistor && len(p.Pads) == 2 {
			a, bb := p.Pads[0].Net, p.Pads[1].Net
			sig := ""
			switch {
			case global(a) && bb != "" && !global(bb):
				sig = bb
			case global(bb) && a != "" && !global(a):
				sig = a
			}
			if sig != "" {
				var pin *Pad
				for _, cp := range corePads[sig] {
					if isIC(b.Part(cp.Part)) {
						pin = cp
						break
					}
				}
				if pin != nil {
					assign(p, pin.Part, "pull", pin, "pull on "+pin.Key()+" ("+sig+")")
					continue
				}
			}
		}
		// 6. signal affinity (ICs outweigh connectors)
		score := map[string]float64{}
		pinOf := map[string]*Pad{}
		for _, n := range nets {
			if global(n) {
				continue
			}
			for _, cp := range corePads[n] {
				w := 0.5
				if isIC(b.Part(cp.Part)) {
					w = 1
				}
				score[cp.Part] += w
				pinOf[cp.Part] = cp
			}
		}
		best, bestS := "", 0.0
		for core, sc := range score {
			if sc > bestS || sc == bestS && core < best {
				best, bestS = core, sc
			}
		}
		if best != "" {
			role := "signal"
			if c.Kinds[p.Ref] == KindTestPoint {
				role = "test" // probe access only: loose tether
			}
			assign(p, best, role, pinOf[best], "shares signal nets with "+best)
			continue
		}
		pending = append(pending, p)
	}

	// 7. chains through already-assigned auxiliaries.
	for pass := 0; pass < 3 && len(pending) > 0; pass++ {
		var rest []*Part
		for _, p := range pending {
			found := ""
			for _, pd := range p.Pads {
				if pd.Net == "" || global(pd.Net) {
					continue
				}
				for _, q := range b.Parts {
					if q == p || c.BlockOf[q.Ref] == "" {
						continue
					}
					for _, qd := range q.Pads {
						if qd.Net == pd.Net {
							found = q.Ref
						}
					}
				}
			}
			if found == "" {
				rest = append(rest, p)
				continue
			}
			core := coreOf(c, found)
			assign(p, core, "chain", nil, "connected through "+found)
		}
		pending = rest
	}
	if len(pending) > 0 {
		misc := &Block{ID: "B-MISC", Kind: "misc"}
		for _, p := range pending {
			misc.Parts = append(misc.Parts, p.Ref)
			misc.Members = append(misc.Members, Member{Ref: p.Ref, Role: "unassigned", Why: "no signal relation to any core"})
			c.BlockOf[p.Ref] = misc.ID
		}
		c.Blocks = append(c.Blocks, misc)
	}
	for _, bl := range c.Blocks {
		if len(bl.Parts) > 1 {
			sort.Strings(bl.Parts[1:])
		}
		sortMembers(bl)
		// An IC driving an inductor through a switch node is a switching
		// regulator whatever its pin mix says: it is the noise source.
		for _, m := range bl.Members {
			if m.Role == "power-stage" && (bl.Kind == "logic" || bl.Kind == "mcu") && len(b.Part(bl.Core).Pads) <= 24 {
				bl.Kind = "power"
			}
		}
	}
}

// switchNet returns the switch-node net a part touches, if any.
func switchNet(an *Analysis, b *Board, p *Part) string {
	for _, pd := range p.Pads {
		if pd.Net != "" && an.Plan(pd.Net, b.Rules).Role == RoleSwitch {
			return pd.Net
		}
	}
	return ""
}

// stageOwner returns the IC pad whose inferred switch node this part touches.
func stageOwner(p *Part, swOwner map[string]*Pad) *Pad {
	for _, pd := range p.Pads {
		if pin := swOwner[pd.Net]; pin != nil && pin.Part != p.Ref {
			return pin
		}
	}
	return nil
}

// sortMembers orders a block's members by role strength, then reference.
func sortMembers(bl *Block) {
	sort.SliceStable(bl.Members, func(i, j int) bool {
		ri, rj := roleRank(bl.Members[i].Role), roleRank(bl.Members[j].Role)
		if ri != rj {
			return ri < rj
		}
		return bl.Members[i].Ref < bl.Members[j].Ref
	})
}
