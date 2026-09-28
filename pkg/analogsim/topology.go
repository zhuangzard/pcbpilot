package analogsim

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// edge is one two-pin passive seen from a net.
type edge struct {
	Ref   string
	Kind  string // R | C | L
	Value float64
	Other string // net on the other pin
	Text  string
}

// circuit is the analysable view of a design.
type circuit struct {
	d       *powersim.Design
	lib     *Library
	parts   map[string]*powersim.Part
	passive map[string]*passive // two-pin R/C/L by ref
	netPins map[string][]pinRef
	edges   map[string][]edge // net → passives touching it
	ground  map[string]bool
	railV   map[string]float64 // DC rails (incl. ground = 0)
	railSrc map[string]string
	sim     *powersim.Output
	simV    map[string]float64 // typical-scenario net voltages
	pmodels map[string]*powersim.Model
	assump  []string
}

type passive struct {
	Ref   string
	Kind  string
	Value float64
	Text  string
	Nets  [2]string
	Part  *powersim.Part
}

type pinRef struct {
	Ref, Pin, Name string
}

var (
	reGround = regexp.MustCompile(`(?i)^(A|D|P|S|E|C)?GND[A-Z0-9_]*$|^VSS[A-Z]*$|^0V$|^GND_`)
	reRail   = regexp.MustCompile(`(?i)^[+-]?\d+V\d*|^V(CC|DD|BUS|IN|BAT|SYS|REF|DDA|CCA|S)\b|^\+|^(AVDD|DVDD|VDDA|VDDIO|VMOT|VM)$|^P?\d+V\d*`)
	reVolt   = regexp.MustCompile(`(?i)([+-]?)(\d+)V(\d*)`)
	reVoltD  = regexp.MustCompile(`(?i)([+-]?)(\d+\.\d+)V`)
)

// voltFromName parses "+3V3" → 3.3, "5V_TERM" → 5, "-12V" → -12, "1.8V" → 1.8.
func voltFromName(n string) (float64, bool) {
	if m := reVoltD.FindStringSubmatch(n); m != nil {
		v, _ := strconv.ParseFloat(m[2], 64)
		if m[1] == "-" {
			v = -v
		}
		return v, true
	}
	if m := reVolt.FindStringSubmatch(n); m != nil {
		s := m[2]
		if m[3] != "" {
			s += "." + m[3]
		}
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, false
		}
		if m[1] == "-" || strings.HasPrefix(strings.ToUpper(n), "N") && strings.Contains(strings.ToUpper(n), "NEG") {
			v = -v
		}
		return v, true
	}
	return 0, false
}

func passiveKind(p *powersim.Part) string {
	if len(p.Pins) != 2 {
		return ""
	}
	ref := strings.ToUpper(p.Ref)
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		i++
	}
	switch ref[:i] {
	case "R", "RS", "RSH":
		return "R"
	case "C", "CE", "CT":
		return "C"
	case "L":
		return "L"
	case "FB":
		return "L"
	}
	return ""
}

func newCircuit(d *powersim.Design, lib *Library, sim *powersim.Output, plibs powersim.Libraries) *circuit {
	c := &circuit{d: d, lib: lib, parts: map[string]*powersim.Part{}, passive: map[string]*passive{},
		netPins: map[string][]pinRef{}, edges: map[string][]edge{}, ground: map[string]bool{},
		railV: map[string]float64{}, railSrc: map[string]string{}, sim: sim, simV: map[string]float64{}, pmodels: map[string]*powersim.Model{}}
	for _, p := range d.Parts {
		c.parts[p.Ref] = p
		for _, pin := range p.Pins {
			if pin.Net != "" {
				c.netPins[pin.Net] = append(c.netPins[pin.Net], pinRef{p.Ref, pin.Number, pin.Name})
			}
		}
		if k := passiveKind(p); k != "" && p.Pins[0].Net != "" && p.Pins[1].Net != "" {
			v, text, ok := partValue(p, k)
			if !ok && k == "L" && strings.HasPrefix(strings.ToUpper(p.Ref), "FB") {
				v, text, ok = 1e-6, "ferrite (1 µH assumed)", true
			}
			if !ok {
				continue
			}
			if k == "R" && v == 0 {
				v = 1e-3 // 0 Ω link
			}
			pv := &passive{Ref: p.Ref, Kind: k, Value: v, Text: text, Nets: [2]string{p.Pins[0].Net, p.Pins[1].Net}, Part: p}
			c.passive[p.Ref] = pv
			if pv.Nets[0] != pv.Nets[1] {
				c.edges[pv.Nets[0]] = append(c.edges[pv.Nets[0]], edge{p.Ref, k, v, pv.Nets[1], text})
				c.edges[pv.Nets[1]] = append(c.edges[pv.Nets[1]], edge{p.Ref, k, v, pv.Nets[0], text})
			}
		}
		if plibs != nil {
			if m := plibs.Match(p); m != nil {
				c.pmodels[p.Ref] = m.Model
			}
		}
	}
	if sim != nil {
		var res *powersim.Result
		for i := range sim.Results {
			if sim.Results[i].Scenario == "typical" {
				res = &sim.Results[i]
			}
		}
		if res == nil && len(sim.Results) > 0 {
			res = &sim.Results[0]
		}
		if res != nil {
			for n, nr := range res.Nets {
				if !nr.Floating {
					c.simV[n] = nr.Voltage
				}
				if nr.Role == "ground" {
					c.ground[n] = true
				}
			}
		}
	}
	for _, n := range d.Nets() {
		role := d.NetRole[n]
		if role == "ground" || reGround.MatchString(n) {
			c.ground[n] = true
		}
		if c.ground[n] {
			c.railV[n], c.railSrc[n] = 0, "ground"
			continue
		}
		if role == "power" || reRail.MatchString(n) || c.isSimRail(n) {
			if v, ok := c.simV[n]; ok && (role == "power" || math.Abs(v) > 0.05) && c.isSimRail(n) {
				c.railV[n], c.railSrc[n] = v, "power-sim"
				continue
			}
			if v, ok := voltFromName(n); ok {
				c.railV[n], c.railSrc[n] = v, "name"
				continue
			}
			if role == "power" {
				c.railV[n], c.railSrc[n] = 3.3, "assumed"
				c.assump = append(c.assump, "rail "+n+" has no simulated or named voltage — 3.3 V assumed")
			}
		}
	}
	return c
}

// isSimRail: the power simulation marks the net as a power rail.
func (c *circuit) isSimRail(n string) bool {
	if c.sim == nil {
		return false
	}
	for _, r := range c.sim.Results {
		if nr := r.Nets[n]; nr != nil {
			return nr.Role == "power"
		}
	}
	return false
}

func (c *circuit) isRail(n string) bool {
	_, ok := c.railV[n]
	return ok
}

// nonPassivePins lists pins on net n that are not two-pin passives (optionally excluding refs).
func (c *circuit) nonPassivePins(n string, exclude ...string) []pinRef {
	var out []pinRef
	for _, p := range c.netPins[n] {
		if _, ok := c.passive[p.Ref]; ok {
			continue
		}
		skip := false
		for _, e := range exclude {
			if e == p.Ref {
				skip = true
			}
		}
		if !skip {
			out = append(out, p)
		}
	}
	return out
}

// between lists passives connecting nets a and b.
func (c *circuit) between(a, b string, kinds string) []edge {
	var out []edge
	for _, e := range c.edges[a] {
		if e.Other == b && strings.Contains(kinds, e.Kind) {
			out = append(out, e)
		}
	}
	return out
}

// edgesOf lists passives of the given kinds on net a excluding refs.
func (c *circuit) edgesOf(a, kinds string, exclude map[string]bool) []edge {
	var out []edge
	for _, e := range c.edges[a] {
		if strings.Contains(kinds, e.Kind) && !exclude[e.Ref] {
			out = append(out, e)
		}
	}
	return out
}

// region grows from seed nets through passives, stopping at rails and at
// nets that carry pins of non-passive parts not listed in own. It returns
// the passives, the interior nets and the boundary (port) nets.
func (c *circuit) region(seeds []string, own map[string]bool, stopNets map[string]bool, maxParts int) (parts []string, nets []string, ports []string) {
	seenNet := map[string]bool{}
	seenPart := map[string]bool{}
	isPort := map[string]bool{}
	var queue []string
	for _, s := range seeds {
		if s != "" && !seenNet[s] {
			seenNet[s] = true
			queue = append(queue, s)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if c.isRail(n) || stopNets[n] {
			isPort[n] = true
			continue
		}
		foreign := false
		for _, p := range c.netPins[n] {
			if _, ok := c.passive[p.Ref]; ok || own[p.Ref] {
				continue
			}
			foreign = true
		}
		if foreign {
			isPort[n] = true
		}
		// A foreign pin makes the net a port but the passive network on it
		// still belongs to the block when the net is a seed (the block's own
		// input/output pin nets).
		isSeed := false
		for _, s := range seeds {
			if s == n {
				isSeed = true
			}
		}
		if foreign && !isSeed {
			continue
		}
		for _, e := range c.edges[n] {
			if seenPart[e.Ref] {
				continue
			}
			if maxParts > 0 && len(seenPart) >= maxParts {
				break
			}
			seenPart[e.Ref] = true
			parts = append(parts, e.Ref)
			if !seenNet[e.Other] {
				seenNet[e.Other] = true
				queue = append(queue, e.Other)
			}
		}
	}
	for n := range seenNet {
		if isPort[n] {
			ports = append(ports, n)
		} else {
			nets = append(nets, n)
		}
	}
	sort.Strings(parts)
	sort.Strings(nets)
	sort.Strings(ports)
	return
}

// icSupply is the supply voltage of the IC that owns pins on net n (its
// highest non-ground rail), used as the logic-high level of a driver.
func (c *circuit) icSupply(ref string) (float64, bool) {
	p := c.parts[ref]
	if p == nil {
		return 0, false
	}
	best, ok := 0.0, false
	for _, pin := range p.Pins {
		if v, isRail := c.railV[pin.Net]; isRail && !c.ground[pin.Net] && v > best {
			best, ok = v, true
		}
	}
	return best, ok
}

// driverHigh is the logic-high level of the IC driving net n.
func (c *circuit) driverHigh(n string, exclude ...string) (float64, string) {
	for _, pr := range c.nonPassivePins(n, exclude...) {
		if v, ok := c.icSupply(pr.Ref); ok {
			return v, pr.Ref
		}
	}
	return 3.3, ""
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var reRefSplit = regexp.MustCompile(`^([^0-9]*)([0-9]*)(.*)$`)

func refLess(a, b string) bool {
	ma, mb := reRefSplit.FindStringSubmatch(a), reRefSplit.FindStringSubmatch(b)
	if ma[1] != mb[1] {
		return ma[1] < mb[1]
	}
	if len(ma[2]) != len(mb[2]) {
		return len(ma[2]) < len(mb[2])
	}
	if ma[2] != mb[2] {
		return ma[2] < mb[2]
	}
	return ma[3] < mb[3]
}

func sortRefs(xs []string) []string {
	sort.Slice(xs, func(i, j int) bool { return refLess(xs[i], xs[j]) })
	return xs
}

func uniq(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
