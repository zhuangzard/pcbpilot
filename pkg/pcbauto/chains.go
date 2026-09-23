package pcbauto

import (
	"math"
	"sort"
)

// Interface signal chains.
//
// A signal entering through a connector must meet its protection first:
// connector → shunt protection (ESD/TVS, on the connector-side net, on the
// trace, not at the end of a stub) → common-mode choke → series resistor /
// AC-coupling cap → IC. A TVS placed after the series resistor, or hung off
// a stub, lets the surge reach the IC before it clamps. Net half-perimeter
// wirelength cannot see order; the chain cost can: it is the length of the
// path walked in the required order, so any inversion shows up as doubling
// back.
//
// Differential pairs add symmetry: the P and N parts at the same chain step
// sit side by side, so the pair stays coupled through the part.

// ChainNode is one part along a chain.
type ChainNode struct {
	Ref    string `json:"ref"`
	Shunt  bool   `json:"shunt,omitempty"` // shunt to ground/rail (protection, filter cap)
	In     string `json:"in"`              // pad key on the connector side
	Out    string `json:"out,omitempty"`   // pad key toward the IC (series parts)
	OutNet string `json:"-"`

	in, out *Pad // resolved once; pads move with their parts
}

// SignalChain is the ordered path from a connector pin to the IC pin it feeds.
type SignalChain struct {
	Connector string       `json:"connector"` // connector pad key
	Nets      []string     `json:"nets"`      // net at each step, connector side first
	Nodes     []ChainNode  `json:"nodes"`     // in required order
	IC        string       `json:"ic,omitempty"`
	Pair      *SignalChain `json:"-"` // the other half of a differential pair
	Weight    float64      `json:"weight"`

	conn, ic *Pad
}

// Seq renders the chain for reports: J1.3 → ESD1 → R5 → U1.12.
func (s *SignalChain) Seq() []string {
	out := []string{s.Connector}
	for _, n := range s.Nodes {
		out = append(out, n.Ref)
	}
	if s.IC != "" {
		out = append(out, s.IC)
	}
	return out
}

func (c *Circuit) buildChains(b *Board, an *Analysis) {
	role := func(n string) NetRole { return an.Plan(n, b.Rules).Role }
	global := func(n string) bool { r := role(n); return r == RoleGround || r == RolePower }
	padsOn := map[string][]*Pad{}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net != "" {
				padsOn[pd.Net] = append(padsOn[pd.Net], pd)
			}
		}
	}
	isIC := func(ref string) bool { k := c.Kinds[ref]; return k == KindIC || k == KindModule }
	// Chains end where power conversion begins: switch nodes, converter
	// outputs and the converter's own parts are not interface signal path.
	stopNet, stopPart := map[string]bool{}, map[string]bool{}
	for _, cv := range c.Converters {
		stopNet[cv.SwitchNet], stopNet[cv.OutRail], stopNet[cv.InRail] = true, true, true
		for _, r := range append([]string{cv.Inductor, cv.Diode, cv.HotCap, cv.Bootstrap}, cv.Feedback...) {
			stopPart[r] = true
		}
	}
	// A net with many pads is a bus or an unrecognised rail, not a chain.
	const maxChainPads = 8
	byNet := map[string]*SignalChain{}
	for _, conn := range b.Parts {
		port := c.Kinds[conn.Ref]
		if port != KindConnector && port != KindAntenna {
			continue
		}
		for _, cp := range conn.Pads {
			if cp.Net == "" || global(cp.Net) || byNet[cp.Net] != nil || stopNet[cp.Net] || len(padsOn[cp.Net]) > maxChainPads {
				continue
			}
			ch := &SignalChain{Connector: cp.Key(), Weight: 1}
			if port == KindAntenna || reRFConn.MatchString(conn.Device) {
				// The matching network sets the antenna's tuning; every
				// mil of feed between antenna and match detunes and radiates.
				ch.Weight = 3
			}
			net := cp.Net
			seen := map[string]bool{conn.Ref: true}
			for step := 0; step < 5 && net != ""; step++ {
				ch.Nets = append(ch.Nets, net)
				if r := role(net); r == RoleDiff || r == RoleClock || r == RoleRF {
					ch.Weight = math.Max(ch.Weight, 2)
				}
				var shunts []ChainNode
				var candidates []*ChainNode
				ic := ""
				here := map[string]bool{}
				for _, pd := range padsOn[net] {
					if seen[pd.Part] || here[pd.Part] || pd.Part == conn.Ref || stopPart[pd.Part] {
						continue
					}
					here[pd.Part] = true // flow-through ESD lands twice on one net
					p := b.Part(pd.Part)
					if isIC(p.Ref) || isCore(c.Kinds[p.Ref]) {
						if ic == "" && isIC(p.Ref) {
							ic = pd.Key()
						}
						continue
					}
					out := partnerPad(p, pd)
					if isProtectionPart(c, p) && c.Kinds[p.Ref] != KindFuse {
						out = nil // ESD arrays shunt every line they touch; fuses are series
					}
					switch {
					case out == nil || out.Net == "" || global(out.Net):
						shunts = append(shunts, ChainNode{Ref: p.Ref, Shunt: true, In: pd.Key()})
					case len(padsOn[out.Net]) > 1 && !stopNet[out.Net]:
						candidates = append(candidates, &ChainNode{Ref: p.Ref, In: pd.Key(), Out: out.Key(), OutNet: out.Net})
					}
				}
				// Several series parts leave this net: the chain follows the one
				// that reaches an IC; the rest are branches (jumper options,
				// other connectors) and carry no order constraint.
				var series *ChainNode
				for _, cand := range candidates {
					reaches := false
					for _, q := range padsOn[cand.OutNet] {
						if isIC(q.Part) {
							reaches = true
						}
					}
					if series == nil || reaches && !seriesReaches(series, padsOn, isIC) {
						series = cand
					}
				}
				sort.SliceStable(shunts, func(i, j int) bool {
					// Protection first, then filter parts.
					pi, pj := isProtectionPart(c, b.Part(shunts[i].Ref)), isProtectionPart(c, b.Part(shunts[j].Ref))
					if pi != pj {
						return pi
					}
					return shunts[i].Ref < shunts[j].Ref
				})
				for _, s := range shunts {
					seen[s.Ref] = true
				}
				ch.Nodes = append(ch.Nodes, shunts...)
				if ic != "" {
					// Reached the IC this port serves: series parts leaving
					// this net belong to other branches (a second antenna
					// path, a debug header).
					series = nil
				}
				if series != nil {
					seen[series.Ref] = true
					ch.Nodes = append(ch.Nodes, *series)
					net = series.OutNet
					continue
				}
				ch.IC = ic
				break
			}
			if !chainMatters(c, b, ch) {
				continue // nothing whose order can be wrong
			}
			byNet[cp.Net] = ch
			c.Chains = append(c.Chains, ch)
		}
	}
	// Antenna / RF-connector feed nets are RF whatever their names: the
	// router must give them controlled impedance and ground guarding.
	for _, ch := range c.Chains {
		if ch.Weight < 3 {
			continue
		}
		for _, n := range ch.Nets {
			if np := an.ByNet[n]; np != nil && (np.Role == RoleSignal || np.Role == "") {
				np.Role = RoleRF
				np.Why = append(np.Why, "on the feed chain from "+ch.Connector)
			}
		}
	}
	// The IC at the end of an antenna / RF-connector chain is a radio.
	for _, ch := range c.Chains {
		if ch.Weight >= 3 && ch.IC != "" {
			if ic := padAt(b, ch.IC); ic != nil {
				for _, bl := range c.Blocks {
					if bl.Core == ic.Part {
						bl.Kind = "rf"
					}
				}
			}
		}
	}
	for _, ch := range c.Chains {
		ch.conn, ch.ic = padAt(b, ch.Connector), padAt(b, ch.IC)
		for i := range ch.Nodes {
			ch.Nodes[i].in, ch.Nodes[i].out = padAt(b, ch.Nodes[i].In), padAt(b, ch.Nodes[i].Out)
		}
	}
	// Pair the P and N chains of differential pairs.
	for n, ch := range byNet {
		if pw := an.Plan(n, b.Rules).PairWith; pw != "" {
			if o := byNet[pw]; o != nil {
				ch.Pair, o.Pair = o, ch
			}
		}
	}
}

func seriesReaches(n *ChainNode, padsOn map[string][]*Pad, isIC func(string) bool) bool {
	for _, q := range padsOn[n.OutNet] {
		if isIC(q.Part) {
			return true
		}
	}
	return false
}

// chainMatters keeps chains with a series part, or with protection in front
// of an IC; a lone filter cap on a connector pin has no order to get wrong.
func chainMatters(c *Circuit, b *Board, ch *SignalChain) bool {
	prot := false
	for _, n := range ch.Nodes {
		if !n.Shunt {
			return true
		}
		if isProtectionPart(c, b.Part(n.Ref)) {
			prot = true
		}
	}
	return prot && ch.IC != ""
}

// partnerPad is the pad a series part passes the signal to: the other pad
// of a two-pad part; for four-pad parts (common-mode chokes, resistor
// arrays) the pad straight across — neither the row neighbour (nearest) nor
// the diagonal (farthest).
func partnerPad(p *Part, in *Pad) *Pad {
	var others []*Pad
	for _, pd := range p.Pads {
		if pd != in {
			others = append(others, pd)
		}
	}
	switch len(others) {
	case 1:
		return others[0]
	case 3:
		sort.SliceStable(others, func(i, j int) bool {
			return others[i].Box.C.Dist(in.Box.C) < others[j].Box.C.Dist(in.Box.C)
		})
		return others[1]
	}
	return nil
}

// ChainCost prices ch on the current placement. The trunk runs connector
// pad → each series part (in → out) → IC pin; its excess over the straight
// connector→IC line is the doubling back. Every shunt adds its stub: its
// distance from the trunk segment of its own net. Protection additionally
// pays for how far along that segment it sits from the connector end, so it
// clamps before anything else sees the surge.
func ChainCost(b *Board, c *Circuit, ch *SignalChain) (excess, protectMil float64, ok bool) {
	if ch.conn == nil {
		return 0, 0, false
	}
	start := ch.conn.Box.C
	type seg struct{ a, b Point }
	var segs []seg
	var shuntsOf [][]ChainNode
	cur := start
	walked := 0.0
	var pending []ChainNode
	for _, n := range ch.Nodes {
		if n.Shunt {
			pending = append(pending, n)
			continue
		}
		if n.in == nil || n.out == nil {
			return 0, 0, false
		}
		in := n.in.Box.C
		segs = append(segs, seg{cur, in})
		shuntsOf = append(shuntsOf, pending)
		pending = nil
		walked += cur.Dist(in)
		cur = n.out.Box.C
	}
	end := cur
	if ch.ic != nil {
		end = ch.ic.Box.C
	}
	segs = append(segs, seg{cur, end})
	shuntsOf = append(shuntsOf, pending)
	walked += cur.Dist(end)
	excess = math.Max(0, walked-start.Dist(end))
	if ch.Weight >= 3 {
		// RF feed: the whole length is loss and detuning, not just detours.
		excess = walked
	}
	protectMil = -1
	for i, sg := range segs {
		for _, n := range shuntsOf[i] {
			if n.in == nil {
				continue
			}
			pt := n.in.Box.C
			stub, along := segDist(pt, sg.a, sg.b)
			excess += stub
			if isProtectionPart(c, b.Part(n.Ref)) {
				excess += along
				if protectMil < 0 {
					protectMil = start.Dist(pt)
				}
			}
		}
	}
	if protectMil < 0 {
		protectMil = 0
	}
	return excess, protectMil, true
}

// segDist returns the distance from p to segment ab and how far from a
// along it the closest point lies.
func segDist(p, a, b Point) (dist, along float64) {
	ab := b.Sub(a)
	l2 := ab.X*ab.X + ab.Y*ab.Y
	if l2 < 1e-9 {
		return p.Dist(a), 0
	}
	t := ((p.X-a.X)*ab.X + (p.Y-a.Y)*ab.Y) / l2
	t = math.Max(0, math.Min(1, t))
	q := Point{a.X + t*ab.X, a.Y + t*ab.Y}
	return p.Dist(q), t * math.Sqrt(l2)
}

// ChainStats counts chains whose protection is not the first thing the
// signal meets (a series part sits nearer the connector than the first
// shunt protection part) and sums the detour excess.
func ChainStats(b *Board, c *Circuit) (inversions int, excessMil float64, n int) {
	for _, ch := range c.Chains {
		ex, _, ok := ChainCost(b, c, ch)
		if !ok {
			continue
		}
		n++
		excessMil += ex
		if chainInverted(b, c, ch) {
			inversions++
		}
	}
	return
}

// chainInverted: a series part that the schematic puts after a protection
// part sits nearer the connector than that protection part — the surge would
// pass the series part (and its trace) before being clamped.
func chainInverted(b *Board, c *Circuit, ch *SignalChain) bool {
	if ch.conn == nil {
		return false
	}
	o := ch.conn.Box.C
	prot := math.Inf(1)
	for _, n := range ch.Nodes {
		if n.in == nil {
			continue
		}
		d := o.Dist(n.in.Box.C)
		if n.Shunt {
			if isProtectionPart(c, b.Part(n.Ref)) && math.IsInf(prot, 1) {
				prot = d
			}
			continue
		}
		if !math.IsInf(prot, 1) && d < prot {
			return true
		}
	}
	return false
}

func padAt(b *Board, key string) *Pad {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '.' {
			if p := b.Part(key[:i]); p != nil {
				for _, pd := range p.Pads {
					if pd.Number == key[i+1:] {
						return pd
					}
				}
			}
			return nil
		}
	}
	return nil
}
