package pcbauto

import (
	"fmt"
	"math"
	"regexp"
	"sort"
)

// Switching converters.
//
// The single most important layout fact of a switcher is its hot loop: the
// path that carries the chopped current with di/dt of amps per nanosecond.
// Its inductance (∝ loop area) sets the switch-node ringing and most of the
// radiated EMI; nothing done later in routing can fix a large hot loop.
//
//	buck   hot loop = C_in → VIN pin → high-side switch → (low-side switch or
//	       catch diode) → GND → C_in. The INPUT cap is the critical part.
//	boost  hot loop = SW → (rectifier diode or sync switch) → C_out → GND →
//	       low-side switch. The OUTPUT cap is the critical part.
//
// Around it: the inductor sits on the SW pin with the smallest SW copper; the
// bootstrap cap hugs BST; the feedback divider hugs FB and stays away from
// the inductor and switch node (FB is a high-impedance node that picks up
// dv/dt), sensing VOUT at the output cap.

// Converter is one recognised switching regulator.
type Converter struct {
	Core      string   `json:"core"`
	Topology  string   `json:"topology"` // buck | boost
	SwitchNet string   `json:"switchNet"`
	Inductor  string   `json:"inductor"`
	Diode     string   `json:"diode,omitempty"` // catch / rectifier; empty = synchronous
	InRail    string   `json:"inRail,omitempty"`
	OutRail   string   `json:"outRail,omitempty"`
	HotCap    string   `json:"hotCap,omitempty"` // the cap that closes the hot loop
	Bootstrap string   `json:"bootstrap,omitempty"`
	FBPin     string   `json:"fbPin,omitempty"`
	Feedback  []string `json:"feedback,omitempty"` // divider resistors (and feed-forward cap)
	// Confidence: "certain" when a rectifier diode fixes the topology,
	// "likely" when it rests on names, inductor count or the buck prior.
	Confidence string   `json:"confidence"`
	Why        []string `json:"why,omitempty"`
}

// reInputRail names nets that feed a converter rather than leave it.
var reInputRail = regexp.MustCompile(`(?i)(VBAT|BATT?|VIN|V_IN|VBUS|VSYS|VCC_SYS|DCIN|VCHG)`)

func (c *Circuit) buildConverters(b *Board, an *Analysis) {
	role := func(net string) NetRole { return an.Plan(net, b.Rules).Role }
	isGnd := func(net string) bool { return role(net) == RoleGround }
	isRail := func(net string) bool { return role(net) == RolePower }
	partsOn := map[string][]*Pad{}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net != "" {
				partsOn[pd.Net] = append(partsOn[pd.Net], pd)
			}
		}
	}
	icPin := func(core *Part, net string) *Pad {
		for _, pd := range core.Pads {
			if pd.Net == net {
				return pd
			}
		}
		return nil
	}
	used := map[string]bool{}
	capUsed := map[string]bool{}
	for _, core := range b.Parts {
		if k := c.Kinds[core.Ref]; k != KindIC {
			continue
		}
		for _, l := range b.Parts {
			if c.Kinds[l.Ref] != KindInductor || len(l.Pads) != 2 || used[l.Ref] {
				continue
			}
			// SW pad: the inductor pad on a non-rail, non-ground net that
			// also lands on this IC.
			sw, other := -1, -1
			for i, pd := range l.Pads {
				if pd.Net != "" && !isRail(pd.Net) && !isGnd(pd.Net) && icPin(core, pd.Net) != nil {
					sw, other = i, 1-i
				}
			}
			if sw < 0 || l.Pads[other].Net == "" {
				continue
			}
			swNet, x := l.Pads[sw].Net, l.Pads[other].Net
			cv := &Converter{Core: core.Ref, SwitchNet: swNet, Inductor: l.Ref}
			// 1. A rectifier on the switch node decides it.
			for _, pd := range partsOn[swNet] {
				d := b.Part(pd.Part)
				if d == nil || c.Kinds[d.Ref] != KindDiode || len(d.Pads) != 2 {
					continue
				}
				o := d.Pads[0].Net
				if o == swNet {
					o = d.Pads[1].Net
				}
				switch {
				case isGnd(o):
					cv.Topology, cv.Diode, cv.Confidence = "buck", d.Ref, "certain"
					cv.Why = append(cv.Why, d.Ref+" catches "+swNet+" to ground: asynchronous buck")
				case o != "":
					cv.Topology, cv.Diode, cv.Confidence, cv.OutRail = "boost", d.Ref, "certain", o
					cv.Why = append(cv.Why, d.Ref+" rectifies "+swNet+" into "+o+": asynchronous boost")
				}
			}
			if cv.Topology == "" {
				inductors := 0
				for _, q := range b.Parts {
					if c.Kinds[q.Ref] != KindInductor {
						continue
					}
					for _, pd := range q.Pads {
						if pd.Net != "" && !isRail(pd.Net) && !isGnd(pd.Net) && icPin(core, pd.Net) != nil {
							inductors++
							break
						}
					}
				}
				cv.Confidence = "likely"
				switch {
				case inductors > 1:
					cv.Topology = "buck"
					cv.Why = append(cv.Why, fmt.Sprintf("%s drives %d inductors: PMIC / multi-rail buck", core.Ref, inductors))
				case icPin(core, x) != nil && reInputRail.MatchString(x):
					cv.Topology = "boost"
					cv.Why = append(cv.Why, "inductor feeds from input-named rail "+x+": synchronous boost")
				default:
					cv.Topology = "buck"
					cv.Why = append(cv.Why, "no rectifier and no input-named rail on the inductor: synchronous buck (the common case)")
				}
			}
			switch cv.Topology {
			case "boost":
				cv.InRail = x
			case "buck":
				cv.OutRail = x
			}
			if cv.Topology == "boost" && cv.OutRail == "" {
				// Synchronous boost: the output is the IC's other rail pin.
				for _, pd := range core.Pads {
					if isRail(pd.Net) && pd.Net != cv.InRail {
						cv.OutRail = pd.Net
					}
				}
			}
			if cv.Topology == "buck" {
				// Input rail: among the IC's highest-voltage supply pins, the one
				// nearest its SW pin inside the package. Converter ICs put VIN
				// beside LX to shrink the hot loop, and a PMIC gives each
				// channel its own VINx next to its LXx. Distances within one
				// footprint do not depend on where the part is placed.
				swPad := icPin(core, swNet)
				top := -1.0
				for _, pd := range core.Pads {
					if isRail(pd.Net) && pd.Net != cv.OutRail {
						top = math.Max(top, an.Plan(pd.Net, b.Rules).Voltage)
					}
				}
				bestD := math.Inf(1)
				for _, pd := range core.Pads {
					if !isRail(pd.Net) || pd.Net == cv.OutRail || an.Plan(pd.Net, b.Rules).Voltage < top {
						continue
					}
					if d := pd.Box.C.Dist(swPad.Box.C); d < bestD-1e-6 {
						bestD, cv.InRail = d, pd.Net
					}
				}
				if cv.InRail == "" {
					// Unnamed/unrecognised supply: the IC net with the most
					// ceramic caps to ground is its input.
					most := 0
					for _, pd := range core.Pads {
						if pd.Net == "" || pd.Net == swNet || pd.Net == cv.OutRail || isGnd(pd.Net) {
							continue
						}
						n := 0
						for _, q := range partsOn[pd.Net] {
							cp := b.Part(q.Part)
							if cp != nil && c.Kinds[cp.Ref] == KindCapacitor && len(cp.Pads) == 2 && (isGnd(cp.Pads[0].Net) || isGnd(cp.Pads[1].Net)) {
								n++
							}
						}
						if n > most || n == most && n > 0 && pd.Net < cv.InRail {
							most, cv.InRail = n, pd.Net
						}
					}
					if cv.InRail != "" {
						cv.Why = append(cv.Why, "input rail "+cv.InRail+" inferred from its ground caps")
					}
				}
			}
			// Hot-loop cap: the smallest ceramic across the critical rail and
			// ground (smallest = lowest ESL = the one that carries the edges).
			hotRail := cv.InRail
			if cv.Topology == "boost" {
				hotRail = cv.OutRail
			}
			var caps []*Part
			for _, pd := range partsOn[hotRail] {
				p := b.Part(pd.Part)
				if p != nil && !capUsed[p.Ref] && c.Kinds[p.Ref] == KindCapacitor && len(p.Pads) == 2 && (isGnd(p.Pads[0].Net) || isGnd(p.Pads[1].Net)) {
					caps = append(caps, p)
				}
			}
			sort.SliceStable(caps, func(i, j int) bool {
				ci, cj := capRank(caps[i]), capRank(caps[j])
				// Prefer caps already serving this IC, then the smallest.
				oi, oj := c.BlockOf[caps[i].Ref] == "B-"+core.Ref, c.BlockOf[caps[j].Ref] == "B-"+core.Ref
				if oi != oj {
					return oi
				}
				if ci != cj {
					return ci < cj
				}
				return caps[i].Ref < caps[j].Ref
			})
			if len(caps) > 0 {
				cv.HotCap = caps[0].Ref
				capUsed[cv.HotCap] = true
				cv.Why = append(cv.Why, cv.HotCap+" closes the hot loop on "+hotRail)
			}
			// Bootstrap: a cap from the switch node to another IC pin.
			for _, pd := range partsOn[swNet] {
				p := b.Part(pd.Part)
				if p == nil || c.Kinds[p.Ref] != KindCapacitor || len(p.Pads) != 2 {
					continue
				}
				o := p.Pads[0].Net
				if o == swNet {
					o = p.Pads[1].Net
				}
				if o != "" && !isRail(o) && !isGnd(o) && icPin(core, o) != nil {
					cv.Bootstrap = p.Ref
				}
			}
			// Feedback divider: an IC pin net carrying one resistor to the
			// output and one to ground.
			for _, pd := range core.Pads {
				if pd.Net == "" || isRail(pd.Net) || isGnd(pd.Net) || pd.Net == swNet {
					continue
				}
				var top, bot string
				var ff []string
				for _, q := range partsOn[pd.Net] {
					r := b.Part(q.Part)
					if r == nil || r == core || len(r.Pads) != 2 {
						continue
					}
					o := r.Pads[0].Net
					if o == pd.Net {
						o = r.Pads[1].Net
					}
					switch {
					case c.Kinds[r.Ref] == KindResistor && isGnd(o):
						bot = r.Ref
					case c.Kinds[r.Ref] == KindResistor && o == cv.OutRail:
						top = r.Ref
					case c.Kinds[r.Ref] == KindCapacitor && o == cv.OutRail:
						ff = append(ff, r.Ref)
					}
				}
				if top != "" && bot != "" {
					cv.FBPin = pd.Key()
					cv.Feedback = append([]string{top, bot}, ff...)
					cv.Why = append(cv.Why, "feedback divider "+top+"/"+bot+" on "+pd.Key())
					break
				}
			}
			used[l.Ref] = true
			c.Converters = append(c.Converters, cv)
		}
	}
	// Re-role the members: these relations outrank the generic ones.
	for _, cv := range c.Converters {
		core := b.Part(cv.Core)
		pinOn := func(net string) string {
			if pd := icPin(core, net); pd != nil {
				return pd.Key()
			}
			return ""
		}
		if cv.HotCap != "" {
			pin := pinOn(cv.InRail)
			if cv.Topology == "boost" {
				pin = pinOn(cv.OutRail)
				if cv.Diode != "" {
					pin = "" // loop closes through the diode, not an IC pin
				}
			}
			c.reassign(cv.HotCap, cv.Core, "hot-loop", pin, "closes the "+cv.Topology+" hot loop")
		}
		if cv.Diode != "" {
			c.reassign(cv.Diode, cv.Core, "hot-loop", pinOn(cv.SwitchNet), "rectifier in the "+cv.Topology+" hot loop")
		}
		c.reassign(cv.Inductor, cv.Core, "power-stage", pinOn(cv.SwitchNet), "inductor on "+cv.SwitchNet)
		if cv.Bootstrap != "" {
			bst := ""
			bp := b.Part(cv.Bootstrap)
			for _, pd := range bp.Pads {
				if pd.Net != cv.SwitchNet {
					bst = pinOn(pd.Net)
				}
			}
			c.reassign(cv.Bootstrap, cv.Core, "bootstrap", bst, "bootstrap cap between SW and BST")
		}
		for _, r := range cv.Feedback {
			c.reassign(r, cv.Core, "feedback", cv.FBPin, "feedback divider on "+cv.FBPin)
		}
		for _, bl := range c.Blocks {
			if bl.Core == cv.Core {
				// A converter IC is a power block; an SoC or radio with an
				// integrated DC-DC keeps its identity (it is a victim of its
				// own switcher, not primarily a noise source).
				if bl.Kind == "logic" || bl.Kind == "power" || len(core.Pads) <= 24 {
					bl.Kind = "power"
				}
				sortMembers(bl)
			}
		}
	}
}

// reassign moves ref into core's block with a new role.
func (c *Circuit) reassign(ref, core, role, pin, why string) {
	target := "B-" + core
	var dst *Block
	for _, bl := range c.Blocks {
		if bl.ID == target {
			dst = bl
		}
		if bl.ID == c.BlockOf[ref] {
			for i, m := range bl.Members {
				if m.Ref == ref {
					bl.Members = append(bl.Members[:i], bl.Members[i+1:]...)
					break
				}
			}
			for i, r := range bl.Parts {
				if r == ref && bl.Core != ref {
					bl.Parts = append(bl.Parts[:i], bl.Parts[i+1:]...)
					break
				}
			}
		}
	}
	if dst == nil {
		return
	}
	dst.Parts = append(dst.Parts, ref)
	dst.Members = append(dst.Members, Member{Ref: ref, Role: role, Pin: pin, Why: why})
	c.BlockOf[ref] = target
}

// ConverterOf returns the converter whose hot loop, feedback or bootstrap
// includes ref.
func (c *Circuit) ConverterOf(ref string) *Converter {
	for _, cv := range c.Converters {
		if cv.HotCap == ref || cv.Diode == ref || cv.Inductor == ref || cv.Bootstrap == ref {
			return cv
		}
		for _, r := range cv.Feedback {
			if r == ref {
				return cv
			}
		}
	}
	return nil
}

// HotLoop returns the hot-loop polygon (pad centres in loop order) of cv on
// the current placement, and its perimeter and area. ok is false when the
// loop cannot be traced (missing cap).
func HotLoop(b *Board, an *Analysis, cv *Converter) (pts []Point, perim, area float64, ok bool) {
	core, capP := b.Part(cv.Core), b.Part(cv.HotCap)
	if core == nil || capP == nil {
		return nil, 0, 0, false
	}
	isGnd := func(net string) bool { return an.Plan(net, b.Rules).Role == RoleGround }
	var capHot, capGnd *Pad
	for _, pd := range capP.Pads {
		if isGnd(pd.Net) {
			capGnd = pd
		} else {
			capHot = pd
		}
	}
	if capHot == nil || capGnd == nil {
		return nil, 0, 0, false
	}
	nearest := func(part *Part, match func(*Pad) bool, to Point) *Pad {
		var best *Pad
		for _, pd := range part.Pads {
			if match(pd) && (best == nil || pd.Box.C.Dist(to) < best.Box.C.Dist(to)) {
				best = pd
			}
		}
		return best
	}
	gndPin := nearest(core, func(pd *Pad) bool { return isGnd(pd.Net) }, capGnd.Box.C)
	swPin := nearest(core, func(pd *Pad) bool { return pd.Net == cv.SwitchNet }, capHot.Box.C)
	if gndPin == nil || swPin == nil {
		return nil, 0, 0, false
	}
	var d *Part
	if cv.Diode != "" {
		d = b.Part(cv.Diode)
	}
	diodePads := func() (sw, o *Pad) {
		for _, pd := range d.Pads {
			if pd.Net == cv.SwitchNet {
				sw = pd
			} else {
				o = pd
			}
		}
		return
	}
	switch cv.Topology {
	case "buck":
		vin := nearest(core, func(pd *Pad) bool { return pd.Net == capHot.Net }, capHot.Box.C)
		if vin == nil {
			return nil, 0, 0, false
		}
		if d != nil {
			dsw, dg := diodePads()
			if dsw == nil || dg == nil {
				return nil, 0, 0, false
			}
			pts = []Point{capHot.Box.C, vin.Box.C, swPin.Box.C, dsw.Box.C, dg.Box.C, capGnd.Box.C}
		} else {
			pts = []Point{capHot.Box.C, vin.Box.C, gndPin.Box.C, capGnd.Box.C}
		}
	case "boost":
		if d != nil {
			dsw, dout := diodePads()
			if dsw == nil || dout == nil {
				return nil, 0, 0, false
			}
			pts = []Point{swPin.Box.C, dsw.Box.C, dout.Box.C, capHot.Box.C, capGnd.Box.C, gndPin.Box.C}
		} else {
			vout := nearest(core, func(pd *Pad) bool { return pd.Net == capHot.Net }, capHot.Box.C)
			if vout == nil {
				return nil, 0, 0, false
			}
			pts = []Point{vout.Box.C, capHot.Box.C, capGnd.Box.C, gndPin.Box.C}
		}
	default:
		return nil, 0, 0, false
	}
	for i := range pts {
		j := (i + 1) % len(pts)
		perim += pts[i].Dist(pts[j])
		area += pts[i].X*pts[j].Y - pts[j].X*pts[i].Y
	}
	return pts, perim, math.Abs(area) / 2, true
}

// HotLoopStats is the mean hot-loop perimeter (mil) over the converters
// whose loop can be traced, and how many that is.
func HotLoopStats(b *Board, an *Analysis, c *Circuit) (float64, int) {
	sum, n := 0.0, 0
	for _, cv := range c.Converters {
		if _, perim, _, ok := HotLoop(b, an, cv); ok {
			sum += perim
			n++
		}
	}
	if n == 0 {
		return 0, 0
	}
	return sum / float64(n), n
}
