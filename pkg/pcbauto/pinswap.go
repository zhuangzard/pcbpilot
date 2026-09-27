package pcbauto

import (
	"math"
	"sort"
	"strings"
)

// Pin-swap search: for a part whose pins are functionally interchangeable
// (an MCU GPIO matrix, a plain header) choose which pin carries which signal
// net so that the ratsnest to the rest of the placed board is short and
// uncrossed. It is an assignment problem (nets → pins) with a pairwise term
// (crossings between moved nets): a Hungarian solve on the separable cost
// (length + crossings with fixed nets) seeds a swap/move local search on the
// full cost, and a final pass reverts every move that does not pay for
// itself, so the schematic change stays minimal.

// crossingMil is the ratsnest cost of one crossing, in mil of wire: two
// crossing connections cost a layer change (two vias) or a detour.
const crossingMil = 250

// PinSwap moves one net from a pin to another pin of the same part.
type PinSwap struct {
	Ref      string `json:"ref"`
	Net      string `json:"net"`
	FromPin  string `json:"fromPin"`
	FromName string `json:"fromName,omitempty"`
	ToPin    string `json:"toPin"`
	ToName   string `json:"toName,omitempty"`
	// ToWasFree: the target pin had no net (NC in the schematic) before.
	ToWasFree bool `json:"toWasFree"`
	// FromBecomesFree: nothing moves onto the old pin (mark it NC).
	FromBecomesFree bool   `json:"fromBecomesFree"`
	Caution         string `json:"caution,omitempty"`
}

// RatsMetrics is the ratsnest estimate of the signal nets.
type RatsMetrics struct {
	LengthMil float64 `json:"lengthMil"`
	Crossings int     `json:"crossings"`
}

func (m RatsMetrics) cost() float64 { return m.LengthMil + crossingMil*float64(m.Crossings) }

type seg2 [2]Point

// mstEdges returns the Euclidean MST of pts as segments (Prim).
func mstEdges(pts []Point) []seg2 {
	n := len(pts)
	if n < 2 {
		return nil
	}
	in := make([]bool, n)
	best := make([]float64, n)
	from := make([]int, n)
	for i := range best {
		best[i] = math.Inf(1)
	}
	best[0] = 0
	var out []seg2
	for k := 0; k < n; k++ {
		u := -1
		for i := 0; i < n; i++ {
			if !in[i] && (u < 0 || best[i] < best[u]) {
				u = i
			}
		}
		in[u] = true
		if k > 0 {
			out = append(out, seg2{pts[from[u]], pts[u]})
		}
		for i := 0; i < n; i++ {
			if !in[i] {
				if d := pts[u].Dist(pts[i]); d < best[i] {
					best[i], from[i] = d, u
				}
			}
		}
	}
	return out
}

func segLen(es []seg2) float64 {
	t := 0.0
	for _, e := range es {
		t += e[0].Dist(e[1])
	}
	return t
}

// crossCount counts proper intersections between two edge sets. Segments
// that share an endpoint (a common pad) do not cross.
func crossCount(a, b []seg2) int {
	n := 0
	for _, x := range a {
		for _, y := range b {
			if x[0] == y[0] || x[0] == y[1] || x[1] == y[0] || x[1] == y[1] {
				continue
			}
			if segsIntersect(x[0], x[1], y[0], y[1]) {
				n++
			}
		}
	}
	return n
}

// ratsNets returns the routed-signal nets (not ground, power or plane) and
// their pads — the connections that compete for routing channels.
func ratsNets(b *Board, an *Analysis) map[string][]*Pad {
	out := map[string][]*Pad{}
	for _, n := range b.Nets() {
		if len(n.Pads) < 2 {
			continue
		}
		np := an.Plan(n.Name, b.Rules)
		if np.Role == RoleGround || np.Role == RolePower || np.Plane {
			continue
		}
		out[n.Name] = n.Pads
	}
	return out
}

// Ratsnest measures the signal ratsnest of a board.
func Ratsnest(b *Board, an *Analysis) RatsMetrics {
	nets := ratsNets(b, an)
	names := make([]string, 0, len(nets))
	for k := range nets {
		names = append(names, k)
	}
	sort.Strings(names)
	edges := make([][]seg2, len(names))
	var m RatsMetrics
	for i, n := range names {
		pts := make([]Point, len(nets[n]))
		for j, pd := range nets[n] {
			pts[j] = pd.Box.C
		}
		edges[i] = mstEdges(pts)
		m.LengthMil += segLen(edges[i])
	}
	for i := range edges {
		for j := i + 1; j < len(edges); j++ {
			m.Crossings += crossCount(edges[i], edges[j])
		}
	}
	return m
}

// swapProblem is one part's assignment instance.
type swapProblem struct {
	part  *Part
	kind  string // mcu-pin-swap | connector-pin-swap
	caps  *PartCaps
	nets  []string // movable nets
	slots []*Pad   // candidate pins (current pins of the nets ∪ free pins)
	cur   []int    // current slot of each net
	ok    [][]bool // ok[n][s]: slot s may carry net n
	// per net, per slot: MST edges, their length and crossings with every
	// fixed net.
	edges  [][][]seg2
	length [][]float64
	static [][]int
	pairM  map[[4]int]int
}

func (sp *swapProblem) pair(n, s, m, t int) int {
	if n > m {
		n, s, m, t = m, t, n, s
	}
	k := [4]int{n, s, m, t}
	if v, ok := sp.pairM[k]; ok {
		return v
	}
	v := crossCount(sp.edges[n][s], sp.edges[m][t])
	sp.pairM[k] = v
	return v
}

// eval is the full cost of an assignment (slot per net).
func (sp *swapProblem) eval(a []int) (RatsMetrics, float64) {
	var m RatsMetrics
	for n, s := range a {
		m.LengthMil += sp.length[n][s]
		m.Crossings += sp.static[n][s]
		for k := n + 1; k < len(a); k++ {
			m.Crossings += sp.pair(n, s, k, a[k])
		}
	}
	return m, m.cost()
}

// newSwapProblem builds the instance for part p (nil when nothing can move).
func newSwapProblem(b *Board, an *Analysis, caps *PinCapTable, p *Part, rats map[string][]*Pad, fixedEdges map[string][]seg2) *swapProblem {
	pc := caps.For(p.Device)
	kind := "mcu-pin-swap"
	if pc == nil {
		if !caps.GenericConnector(p) {
			return nil
		}
		kind = "connector-pin-swap"
		pc = &PartCaps{ID: "generic-connector", Remap: "gpio-matrix"}
		count := map[string]int{}
		for _, pd := range p.Pads {
			count[pd.Number]++
		}
		for _, pd := range p.Pads {
			if count[pd.Number] != 1 || pc.Pin(pd.Number) != nil {
				continue
			}
			c := &PinCap{Pin: pd.Number, Name: pd.Number, Caps: []string{"gpio"}}
			if pd.Net != "" {
				if r := an.Plan(pd.Net, b.Rules).Role; r == RoleGround || r == RolePower {
					c.Fixed = string(r)
				}
			}
			pc.Pins = append(pc.Pins, c)
			pc.byPin = nil
		}
	}
	padsOf := map[string][]*Pad{}
	netPads := map[string]int{}
	for _, pd := range p.Pads {
		padsOf[pd.Number] = append(padsOf[pd.Number], pd)
		if pd.Net != "" {
			netPads[pd.Net]++
		}
	}
	sp := &swapProblem{part: p, kind: kind, caps: pc, pairM: map[[4]int]int{}}
	slotIdx := map[*Pad]int{}
	addSlot := func(pd *Pad) int {
		if i, ok := slotIdx[pd]; ok {
			return i
		}
		slotIdx[pd] = len(sp.slots)
		sp.slots = append(sp.slots, pd)
		return len(sp.slots) - 1
	}
	need := map[string]func(*PinCap) bool{}
	for _, pin := range pc.Pins {
		pads := padsOf[pin.Pin]
		if len(pads) != 1 || !pin.Slot() {
			continue
		}
		pd := pads[0]
		if pd.Net == "" {
			continue
		}
		if _, ok := rats[pd.Net]; !ok || netPads[pd.Net] != 1 {
			continue // plane/power/ground net, single-pad net, or a net on two pins of this part
		}
		switch an.Plan(pd.Net, b.Rules).Role {
		case RoleDiff, RoleRF, RoleClock, RoleSwitch:
			continue
		}
		nd := caps.Need(pd.Net)
		if nd != "" && !caps.Satisfies(pin, nd) {
			continue // the net needs something its own pin lacks: table or name is off, leave it
		}
		var fn func(*PinCap) bool
		switch {
		case pc.Remap == "af-table" && nd == "" && !plainDigital(pd.Net):
			// A peripheral net on an alternate-function MCU: the target must
			// carry every function of the current pin.
			req := []string{}
			for _, c := range pin.Caps {
				if c != "gpio" {
					req = append(req, c)
				}
			}
			fn = func(q *PinCap) bool {
				for _, c := range req {
					if !q.Has(c) {
						return false
					}
				}
				return q.Has("gpio")
			}
		default:
			fn = func(q *PinCap) bool { return caps.Satisfies(q, nd) }
		}
		need[pd.Net] = fn
		sp.nets = append(sp.nets, pd.Net)
		sp.cur = append(sp.cur, addSlot(pd))
	}
	if len(sp.nets) == 0 {
		return nil
	}
	for _, pin := range pc.Pins {
		pads := padsOf[pin.Pin]
		if len(pads) == 1 && pin.Slot() && pads[0].Net == "" {
			addSlot(pads[0])
		}
	}
	sp.ok = make([][]bool, len(sp.nets))
	sp.edges = make([][][]seg2, len(sp.nets))
	sp.length = make([][]float64, len(sp.nets))
	sp.static = make([][]int, len(sp.nets))
	moving := map[string]bool{}
	for _, n := range sp.nets {
		moving[n] = true
	}
	var fixed [][]seg2
	fixedNames := make([]string, 0, len(fixedEdges))
	for n := range fixedEdges {
		fixedNames = append(fixedNames, n)
	}
	sort.Strings(fixedNames)
	for _, n := range fixedNames {
		if !moving[n] {
			fixed = append(fixed, fixedEdges[n])
		}
	}
	for i, net := range sp.nets {
		others := []Point{}
		for _, pd := range rats[net] {
			if pd.Part != p.Ref {
				others = append(others, pd.Box.C)
			}
		}
		sp.ok[i] = make([]bool, len(sp.slots))
		sp.edges[i] = make([][]seg2, len(sp.slots))
		sp.length[i] = make([]float64, len(sp.slots))
		sp.static[i] = make([]int, len(sp.slots))
		for s, pd := range sp.slots {
			sp.ok[i][s] = s == sp.cur[i] || need[net](pc.Pin(pd.Number))
			if !sp.ok[i][s] {
				continue
			}
			es := mstEdges(append([]Point{pd.Box.C}, others...))
			sp.edges[i][s] = es
			sp.length[i][s] = segLen(es)
			for _, fe := range fixed {
				sp.static[i][s] += crossCount(es, fe)
			}
		}
	}
	return sp
}

// plainDigital recognises nets that are plain GPIO on any MCU.
func plainDigital(net string) bool {
	n := upper(net)
	for _, k := range []string{"LED", "KEY", "BTN", "BUTTON", "SW_", "RELAY", "BUZZ", "INT", "IRQ", "_EN", "EN_", "RST", "RESET", "CS", "SEL", "GPIO", "IO"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	return false
}

// hungarian solves the rectangular assignment rows→cols (rows ≤ cols)
// minimising cost; math.Inf entries are forbidden. Returns col per row.
func hungarian(cost [][]float64) []int {
	n := len(cost)
	if n == 0 {
		return nil
	}
	m := len(cost[0])
	const big = 1e12
	c := func(i, j int) float64 {
		if math.IsInf(cost[i][j], 1) {
			return big
		}
		return cost[i][j]
	}
	u := make([]float64, n+1)
	v := make([]float64, m+1)
	p := make([]int, m+1)
	way := make([]int, m+1)
	for i := 1; i <= n; i++ {
		p[0] = i
		j0 := 0
		minv := make([]float64, m+1)
		used := make([]bool, m+1)
		for j := range minv {
			minv[j] = math.Inf(1)
		}
		for {
			used[j0] = true
			i0, delta, j1 := p[j0], math.Inf(1), 0
			for j := 1; j <= m; j++ {
				if used[j] {
					continue
				}
				cur := c(i0-1, j-1) - u[i0] - v[j]
				if cur < minv[j] {
					minv[j], way[j] = cur, j0
				}
				if minv[j] < delta {
					delta, j1 = minv[j], j
				}
			}
			for j := 0; j <= m; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}
		for {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
			if j0 == 0 {
				break
			}
		}
	}
	out := make([]int, n)
	for j := 1; j <= m; j++ {
		if p[j] > 0 {
			out[p[j]-1] = j - 1
		}
	}
	return out
}

// improve runs swap/move local search on the full cost.
func (sp *swapProblem) improve(a []int) ([]int, float64) {
	a = append([]int(nil), a...)
	_, best := sp.eval(a)
	used := func() map[int]int {
		u := map[int]int{}
		for n, s := range a {
			u[s] = n
		}
		return u
	}
	for round := 0; round < 100; round++ {
		improved := false
		for n := range a {
			u := used()
			for s := range sp.slots {
				if s == a[n] || !sp.ok[n][s] {
					continue
				}
				old := a[n]
				if m, taken := u[s]; taken {
					if !sp.ok[m][old] {
						continue
					}
					a[n], a[m] = s, old
					if _, c := sp.eval(a); c < best-1e-6 {
						best, improved = c, true
						u = used()
						continue
					}
					a[n], a[m] = old, s
				} else {
					a[n] = s
					if _, c := sp.eval(a); c < best-1e-6 {
						best, improved = c, true
						u = used()
						continue
					}
					a[n] = old
				}
			}
		}
		if !improved {
			break
		}
	}
	return a, best
}

// minimise reverts moves that do not pay for themselves (≤ 25 mil each and
// no crossing): the smallest schematic edit with most of the gain.
func (sp *swapProblem) minimise(a []int) []int {
	a = append([]int(nil), a...)
	for changed := true; changed; {
		changed = false
		mBest, cBest := sp.eval(a)
		for n := range a {
			if a[n] == sp.cur[n] {
				continue
			}
			trial := append([]int(nil), a...)
			// put n back; whoever sits on n's original slot takes n's slot
			for m := range trial {
				if m != n && trial[m] == sp.cur[n] {
					if !sp.ok[m][trial[n]] {
						trial = nil
						break
					}
					trial[m] = trial[n]
				}
			}
			if trial == nil {
				continue
			}
			trial[n] = sp.cur[n]
			mT, cT := sp.eval(trial)
			if mT.Crossings <= mBest.Crossings && cT <= cBest+25 {
				a, changed = trial, true
				break
			}
		}
	}
	return a
}

// solve returns the best assignment found and its metrics.
func (sp *swapProblem) solve() ([]int, RatsMetrics) {
	cost := make([][]float64, len(sp.nets))
	for n := range sp.nets {
		cost[n] = make([]float64, len(sp.slots))
		for s := range sp.slots {
			if !sp.ok[n][s] {
				cost[n][s] = math.Inf(1)
				continue
			}
			cost[n][s] = sp.length[n][s] + crossingMil*float64(sp.static[n][s])
		}
	}
	h := hungarian(cost)
	valid := len(h) == len(sp.nets)
	for n, s := range h {
		if !sp.ok[n][s] {
			valid = false
		}
	}
	best, bc := sp.improve(sp.cur)
	if valid {
		if a, c := sp.improve(h); c < bc-1e-6 {
			best = a
		}
	}
	best = sp.minimise(best)
	m, _ := sp.eval(best)
	return best, m
}

// swapCandidate is one part's proposed pin permutation.
type swapCandidate struct {
	ref     string
	kind    string
	swaps   []PinSwap
	before  RatsMetrics // the moved nets' own ratsnest (length, crossings)
	after   RatsMetrics
	board   RatsMetrics // whole-board ratsnest before
	boardTo RatsMetrics // whole-board ratsnest after
	caps    *PartCaps
}

func (c *swapCandidate) key() string {
	parts := make([]string, len(c.swaps))
	for i, s := range c.swaps {
		parts[i] = s.Net + "@" + s.ToPin
	}
	sort.Strings(parts)
	return c.ref + ":" + strings.Join(parts, ",")
}

// SearchPinSwaps finds, per remappable part, the pin permutation that most
// reduces the signal ratsnest (length + crossings). Only candidates whose
// estimated gain is material are returned, best first.
func SearchPinSwaps(b *Board, an *Analysis, caps *PinCapTable) []*swapCandidate {
	if caps == nil {
		return nil
	}
	rats := ratsNets(b, an)
	edges := map[string][]seg2{}
	for n, pads := range rats {
		pts := make([]Point, len(pads))
		for i, pd := range pads {
			pts[i] = pd.Box.C
		}
		edges[n] = mstEdges(pts)
	}
	whole := Ratsnest(b, an)
	var out []*swapCandidate
	for _, p := range b.Parts {
		sp := newSwapProblem(b, an, caps, p, rats, edges)
		if sp == nil {
			continue
		}
		a, after := sp.solve()
		before, _ := sp.eval(sp.cur)
		gain := before.cost() - after.cost()
		if gain < math.Max(100, 0.03*before.cost()) && after.Crossings >= before.Crossings {
			continue
		}
		if after.Crossings > before.Crossings {
			continue
		}
		c := &swapCandidate{ref: p.Ref, kind: sp.kind, before: before, after: after, board: whole, caps: sp.caps}
		occupied := map[int]bool{}
		for _, s := range a {
			occupied[s] = true
		}
		for n, s := range a {
			if s == sp.cur[n] {
				continue
			}
			from, to := sp.slots[sp.cur[n]], sp.slots[s]
			ps := PinSwap{Ref: p.Ref, Net: sp.nets[n], FromPin: from.Number, ToPin: to.Number,
				ToWasFree: to.Net == "", FromBecomesFree: !occupied[sp.cur[n]]}
			if pin := sp.caps.Pin(from.Number); pin != nil {
				ps.FromName = pin.Name
			}
			if pin := sp.caps.Pin(to.Number); pin != nil {
				ps.ToName, ps.Caution = pin.Name, pin.Caution
			}
			c.swaps = append(c.swaps, ps)
		}
		if len(c.swaps) == 0 {
			continue
		}
		sort.Slice(c.swaps, func(i, j int) bool { return c.swaps[i].Net < c.swaps[j].Net })
		nb := ApplyPinSwaps(b, c.swaps)
		c.boardTo = Ratsnest(nb, an)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].before.cost()-out[i].after.cost() > out[j].before.cost()-out[j].after.cost()
	})
	return out
}

// ApplyPinSwaps returns a copy of the board with the pad nets permuted.
// A pin whose net moved away and received none is left without a net.
func ApplyPinSwaps(b *Board, swaps []PinSwap) *Board {
	nb := b.Clone()
	type key struct{ ref, pin string }
	next := map[key]string{}
	cleared := map[key]bool{}
	for _, s := range swaps {
		cleared[key{s.Ref, s.FromPin}] = true
		next[key{s.Ref, s.ToPin}] = s.Net
	}
	for _, p := range nb.Parts {
		for _, pd := range p.Pads {
			k := key{p.Ref, pd.Number}
			if n, ok := next[k]; ok {
				pd.Net = n
			} else if cleared[k] {
				pd.Net = ""
			}
		}
	}
	return nb
}
