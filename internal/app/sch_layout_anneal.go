package app

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
)

// Annealing zone placer.
//
// The candidate search (sch_layout_repair.go) places one peripheral at a time
// and learns about wiring and label space only after every part is down; on
// dense zones it then backtracks chronologically until the budget runs out
// (a 10-part WROOM zone: 200 000 candidates, 66 s, "+3V3 has no safe naming
// lead"; the v1.6.0 live acceptance hit the same wall at 1 000 000). Most
// terminal failures are one thing: peripherals crowding a pin whose net needs
// a label or ground symbol there.
//
// This placer treats the zone as a whole:
//   - every peripheral gets a discrete set of slots in front of its host pin
//     (distance × lateral offset along the host pin's outward direction);
//   - a greedy seed, then simulated annealing over slot choices with a cheap
//     placement-only cost: overlap of bodies/designators, a clear outward lead
//     for every connected pin (longer where a label or ground symbol must
//     go), and estimated wire length — no routing in the loop;
//   - the best distinct legal placements go through the unchanged terminal
//     gate libFinishSchematicLayoutRegenerate (maze routing, naming,
//     validateLibGeometry, validateSchCompositionNets);
//   - a naming/route conflict lengthens the leads of the pins involved and
//     re-anneals; if nothing passes, the caller falls back to the candidate
//     search with the remaining budget.
//
// It is deterministic (seeded from the input) and debits the shared budget:
// one candidate per cost evaluation, plus whatever the terminal gate spends.

// annealMinPeripherals: smaller zones keep the candidate search, which solves
// them quickly and whose exact results many fixtures pin.
const annealMinPeripherals = 6

const annealEvalsPerCandidate = 8

// annealFallbackMinPeripherals: zones this size and up (below
// annealMinPeripherals) get the annealer as a fallback after the search.
// Two peripherals qualify: a pin-dense core with two parts (AT32 TYPE_C,
// USB_UART) needed 200k/50k candidates in the search alone; the fallback
// solves both within 20k (stress L1, 2026-09-24).
const annealFallbackMinPeripherals = 2

// annealFallbackMinBudget: below this the budget is too small to split, and
// the search keeps it whole (its bounded-failure reporting is pinned).
const annealFallbackMinBudget = 4000

const (
	annealRounds           = 3
	annealFailuresPerRound = 2
)

// Slot grid in front of the host pin (raw): distance 15..reach, lateral
// -lateral..lateral, 5-raw steps.
var annealSlotReach, annealSlotLateral = 120.0, 60.0

// annealLabelCost / annealOrderCost: boundary-labelling and river-routing cost
// terms. OFF by default - stress L1 (six affected zones, min budget):
//
//	label: LCD 200k->20k, MICRO_SD 800k->50k, ESP32-V3 MCU 800k->200k, but
//	       CAN 20k->200k, ESP32-V4 MCU 200k->800k, AT32 MCU 200k->unsolved;
//	order: no gains, CAN 20k->200k.
//
// A global label term helps label-dense attachments and hurts the rest; it is
// kept for the staged solver, which applies it only to the dense stage.
var annealLabelCost, annealOrderCost = false, false

// annealFinishAttemptCap bounds one terminal-gate attempt of the annealer.
var annealFinishAttemptCap = 25000

var schematicAnnealDisabled bool

// annealAttemptHook observes each terminal-gate attempt (diagnostics).
var annealAttemptHook func(round, spent int, err error)

// annealCandidateHook observes each placement handed to the terminal gate
// (diagnostics only).
var annealCandidateHook func(p powerLayoutPlan)

type annealPart struct {
	id       string
	measured powerLayoutPlacement
	hostID   string
	hostPin  string
	ownPin   string
	depth    int
	slots    [][2]float64 // (distance, lateral) from the host pin
	rots     []int        // allowed quarter turns relative to the measurement (0 first)
}

// A state entry encodes (rotation, slot) as rot*len(slots)+slot.
func (p *annealPart) choices() int { return len(p.rots) * len(p.slots) }

type annealState []int // slot index per part (aligned with parts)

type annealSolver struct {
	input     SchematicLayoutInput
	core      powerLayoutPlacement
	parts     []*annealPart
	policies  map[string]string
	budget    *int
	rng       *rand.Rand
	leadBoost map[string]float64 // component/pin key → extra lead length
	netWeight map[string]float64 // net → extra wire-length weight (route conflicts)
	allowed   map[string][]float64
	refs      map[string]string // designator → component id
	roles     map[string]string // peripheral-direct net roles
	netPins   map[string]int    // net → connected pins in this zone
	labelOn   bool              // boundary-label cost for this solver (staged dense pass)
	frozen    []bool            // parts fixed by an earlier stage (not mutated)
	// closest-to-legal state seen (diagnostics for "no overlap-free placement")
	bestIllegal    float64
	bestIllegalWhy string
	evals     int
}

func solveSchematicLayoutAnneal(input SchematicLayoutInput, measured map[string]powerLayoutPlacement, members []string, hints map[string]SchematicLayoutPeripheral, budget *int, routing *schematicRoutingContext) (*SchematicLayoutResult, error) {
	core := measured[input.CoreComponentID]
	core = plTranslate(core, -core.X, -core.Y)
	s := &annealSolver{input: input, core: core, policies: input.NetPolicies, budget: budget, leadBoost: map[string]float64{}, netWeight: map[string]float64{}, allowed: map[string][]float64{}}
	s.refs = map[string]string{}
	s.netPins = map[string]int{}
	for _, c := range input.Components {
		for _, q := range c.Measurement.Pins {
			if q.Net != "" {
				s.netPins[q.Net]++
			}
		}
		s.allowed[c.ID] = c.AllowedRotations
		s.refs[c.Measurement.Designator] = c.ID
	}
	probe := input
	probe.NetPolicies = map[string]string{}
	for k, v := range input.NetPolicies {
		probe.NetPolicies[k] = v
	}
	s.roles = schematicMandatoryPeripheralSignalPolicies(&probe)
	h := fnv.New64a()
	for _, id := range members {
		h.Write([]byte(id))
	}
	s.rng = rand.New(rand.NewSource(int64(h.Sum64() & 0x7fffffffffffffff)))
	if err := s.buildParts(measured, members, hints); err != nil {
		return nil, err
	}
	var lastErr error
	if annealStaged {
		if out, err := s.staged(routing); out != nil {
			return out, nil
		} else if err != nil {
			lastErr = err
		}
	}
	best := s.seed()
	learned := map[string]int{} // conflict fingerprint -> round it was learned in
	for round := 0; round < annealRounds; round++ {
		repeats := 0
		// A fixed schedule (~2 500 candidates) so the placement does not
		// depend on the budget; only tiny budgets shorten it.
		iters := 20000
		if *s.budget < 6000 {
			iters = max(2000, *s.budget*2)
		}
		finals := s.anneal(best, iters)
		if len(finals) == 0 {
			lastErr = fmt.Errorf("annealing found no overlap-free placement (closest: %s)", s.bestIllegalWhy)
			break
		}
		failed := 0
		for k, st := range finals {
			// Two failed terminal gates per round are enough to learn from;
			// re-annealing with the boosted leads beats grinding through the
			// rest of this round's near-identical alternatives.
			if failed >= annealFailuresPerRound && round < annealRounds-1 {
				break
			}
			if *s.budget <= 0 {
				return nil, fmt.Errorf("%w: annealing placer", errLibLayoutBudget)
			}
			p := s.plan(st)
			if validateLibGeometry(&p) != nil {
				continue
			}
			// The best-ranked placement is the likeliest to pass: it gets most
			// of what is left; the alternatives get smaller slices.
			slice := min(*s.budget, max(6000, *s.budget/3))
			if k == 0 {
				slice = min(*s.budget, max(6000, *s.budget*3/4))
			}
			// A terminal gate that fails keeps nothing, while the learned
			// next round needs budget too (ESP32 MCU zone on V3 geometry: one
			// round-0 attempt burned 54k and round 1, which passes in ~20k
			// evaluations, never ran). Cap a single attempt.
			slice = min(slice, max(6000, annealFinishAttemptCap))
			if annealCandidateHook != nil {
				annealCandidateHook(p)
			}
			spent := slice
			done, err := libFinishSchematicLayoutRegenerate(p, s.policies, &slice, routing)
			*s.budget -= spent - slice
			if err == nil {
				// The zone gate that runs after the solver: every owned
				// peripheral needs a physical non-ground path to the core.
				// Check it here so a candidate it would reject is not taken.
				tmp := &SchematicLayoutResult{Placements: done.Placements, Wires: done.Wires, Flags: done.Flags, ComponentIDs: s.refs}
				if perr := validateSchematicLayoutPeripheralDirect(tmp, s.input.CoreComponentID, s.roles); perr != nil {
					lastErr = perr
					if annealAttemptHook != nil {
						annealAttemptHook(round, spent-slice, perr)
					}
					s.pullOwnedPeripherals()
					continue
				}
				return &SchematicLayoutResult{Placements: done.Placements, Wires: done.Wires, Flags: done.Flags, Score: libCandidateScore(done),
					Search: &SchematicLayoutSearchDiagnostics{Strategy: fmt.Sprintf("anneal-v1 (round %d, %d evaluations)", round+1, s.evals), MovedComponents: []string{}}}, nil
			}
			lastErr = err
			failed++
			if annealAttemptHook != nil {
				annealAttemptHook(round, spent-slice, err)
			}
			if print := schematicConflictFingerprint(err); print != "" {
				if r, ok := learned[print]; ok && r < round {
					repeats++
				} else if !ok {
					learned[print] = round
				}
			}
			s.learn(err, p)
		}
		// Every failure of this round repeated a conflict already learned in
		// an earlier round: the lead boosts changed nothing, stop spending.
		if round > 0 && failed > 0 && repeats == failed {
			lastErr = fmt.Errorf("%w (annealer stopped: round %d repeated learned conflicts)", lastErr, round+1)
			break
		}
		best = finals[0]
	}
	if lastErr == nil {
		lastErr = errors.New("no candidate passed the terminal gate")
	}
	return nil, fmt.Errorf("annealing placer: %w", lastErr)
}

// buildParts resolves each peripheral's host pin (explicit attachTo, else the
// first placed same-net pin as the candidate search would pick) and its slots.
func (s *annealSolver) buildParts(measured map[string]powerLayoutPlacement, members []string, hints map[string]SchematicLayoutPeripheral) error {
	placed := map[string]powerLayoutPlacement{s.input.CoreComponentID: s.core}
	order := []string{s.input.CoreComponentID}
	depth := map[string]int{s.input.CoreComponentID: 0}
	pending := []string{}
	for _, id := range members {
		if id != s.input.CoreComponentID {
			pending = append(pending, id)
		}
	}
	for len(pending) > 0 {
		progress := false
		for i := 0; i < len(pending); i++ {
			id := pending[i]
			own := measured[id]
			pairs, err := libAttachmentPairs(id, own, hints[id], placed, order, s.policies)
			if err != nil {
				return err
			}
			if len(pairs) == 0 {
				continue
			}
			pair := pairs[0]
			hostID := ""
			for hid, hp := range placed {
				for _, pin := range hp.Pins {
					if pin == pair.host && hp.Designator != "" {
						hostID = hid
					}
				}
			}
			if hostID == "" {
				continue
			}
			part := &annealPart{id: id, measured: own, hostID: hostID, hostPin: pair.host.Number, ownPin: pair.own.Number, depth: depth[hostID] + 1, rots: []int{0}}
			for _, a := range s.allowed[id] {
				q := int(math.Round((a-own.Rotation)/90)) % 4
				if q < 0 {
					q += 4
				}
				if q != 0 && !slicesContainsInt(part.rots, q) {
					part.rots = append(part.rots, q)
				}
			}
			for d := 15.0; d <= annealSlotReach; d += 5 {
				for lat := -annealSlotLateral; lat <= annealSlotLateral; lat += 5 {
					part.slots = append(part.slots, [2]float64{d, lat})
				}
			}
			s.parts = append(s.parts, part)
			// Hosts are placed at their measured pose for pin lookup only; the
			// real position comes from the state.
			placed[id] = own
			order = append(order, id)
			depth[id] = part.depth
			pending = append(pending[:i], pending[i+1:]...)
			i--
			progress = true
		}
		if !progress {
			return fmt.Errorf("annealing placer: %d peripherals have no connected host (%v)", len(pending), pending)
		}
	}
	return nil
}

func annealDir(side string) (float64, float64) {
	switch side {
	case "left":
		return -1, 0
	case "right":
		return 1, 0
	case "up":
		return 0, 1
	}
	return 0, -1
}

func annealSnap(v float64) float64 { return math.Round(v/5) * 5 }

// plan materialises a state: parts in host-before-child order.
func (s *annealSolver) plan(st annealState) powerLayoutPlan {
	at := map[string]powerLayoutPlacement{s.input.CoreComponentID: s.core}
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{s.core}}
	for i, part := range s.parts {
		host := at[part.hostID]
		hp, _ := libPin(host, part.hostPin)
		side, _ := libPinSide(hp, host.BBox)
		ux, uy := annealDir(side)
		slot := part.slots[st[i]%len(part.slots)]
		body := part.measured
		if q := part.rots[st[i]/len(part.slots)]; q != 0 {
			body = plRotate(body, q)
		}
		tx := annealSnap(hp.X + ux*slot[0] - uy*slot[1])
		ty := annealSnap(hp.Y + uy*slot[0] + ux*slot[1])
		op, _ := libPin(body, part.ownPin)
		c := plTranslate(body, tx-op.X, ty-op.Y)
		at[part.id] = c
		p.Placements = append(p.Placements, c)
	}
	return p
}

func annealRects(c powerLayoutPlacement, pad float64) []layoutBBox {
	out := []layoutBBox{{c.BBox.MinX - pad, c.BBox.MinY - pad, c.BBox.MaxX + pad, c.BBox.MaxY + pad}}
	for _, t := range c.TextBBoxes {
		out = append(out, layoutBBox{t.MinX - pad, t.MinY - pad, t.MaxX + pad, t.MaxY + pad})
	}
	return out
}

func annealOverlap(a, b layoutBBox) float64 {
	w := math.Min(a.MaxX, b.MaxX) - math.Max(a.MinX, b.MinX)
	h := math.Min(a.MaxY, b.MaxY) - math.Max(a.MinY, b.MinY)
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

func annealSegHits(x0, y0, x1, y1 float64, r layoutBBox) float64 {
	minX, maxX := math.Min(x0, x1), math.Max(x0, x1)
	minY, maxY := math.Min(y0, y1), math.Max(y0, y1)
	w := math.Min(maxX, r.MaxX) - math.Max(minX, r.MinX)
	h := math.Min(maxY, r.MaxY) - math.Max(minY, r.MinY)
	if w < 0 || h < 0 {
		return 0
	}
	return math.Max(w, h) + 1
}

// cost is the placement-only objective (lower is better); legal reports
// no overlap and no blocked lead.
func (s *annealSolver) cost(st annealState) (float64, bool) {
	// One candidate per annealEvalsPerCandidate evaluations: an evaluation is
	// placement geometry only (no validateLibGeometry, maze or naming probe),
	// roughly an order of magnitude cheaper than a search candidate.
	if s.evals%annealEvalsPerCandidate == 0 {
		*s.budget--
	}
	s.evals++
	p := s.plan(st)
	overlap, blocked, wire := 0.0, 0.0, 0.0
	rects := make([][]layoutBBox, len(p.Placements))
	for i, c := range p.Placements {
		rects[i] = annealRects(c, 5)
	}
	for i := range p.Placements {
		for j := i + 1; j < len(p.Placements); j++ {
			for _, a := range rects[i] {
				for _, b := range rects[j] {
					overlap += annealOverlap(a, b)
				}
			}
		}
	}
	// Leads: every connected pin keeps its outward stem clear of other parts;
	// pins whose net needs a label or ground symbol need a longer lead.
	for i, c := range p.Placements {
		for _, pin := range c.Pins {
			if pin.Net == "" {
				continue
			}
			l := 15.0
			switch s.policies[pin.Net] {
			case "module_port", "local_ground", "local_power":
				l = 35
			}
			l += s.leadBoost[c.Designator+"."+pin.Number]
			side, err := libPinSide(pin, c.BBox)
			if err != nil {
				continue
			}
			ux, uy := annealDir(side)
			x1, y1 := pin.X+ux*l, pin.Y+uy*l
			for j, rs := range rects {
				if j == i {
					continue
				}
				for _, r := range rs {
					blocked += annealSegHits(pin.X, pin.Y, x1, y1, r)
				}
			}
		}
	}
	at := map[string]powerLayoutPlacement{}
	for i, c := range p.Placements {
		if i == 0 {
			at[s.input.CoreComponentID] = c
			continue
		}
		at[s.parts[i-1].id] = c
	}
	// Labels (boundary labelling): a pin whose net has no other pin in this
	// zone must be named at that pin, so its port/flag body beyond the lead is
	// as real an obstacle as a part. Nets shared inside the zone can be named
	// anywhere on their tree and reserve nothing here.
	labelHits := 0.0
	if annealLabelCost || s.labelOn {
		for i, c := range p.Placements {
			for _, pin := range c.Pins {
				if pin.Net == "" || s.netPins[pin.Net] > 1 {
					continue
				}
				kind := ""
				switch s.policies[pin.Net] {
				case "module_port":
					kind = "net_port_bi"
				case "local_ground":
					kind = "ground"
				case "local_power":
					kind = "power"
				default:
					continue
				}
				side, err := libPinSide(pin, c.BBox)
				if err != nil {
					continue
				}
				ux, uy := annealDir(side)
				l := 35 + s.leadBoost[c.Designator+"."+pin.Number]
				label := predictedMarkerBBox(pin.X+ux*l, pin.Y+uy*l, kind, side, pin.Net)
				for j, rs := range rects {
					if j == i {
						continue
					}
					for _, r := range rs {
						labelHits += annealOverlap(label, r)
					}
				}
			}
		}
	}
	// River routing: a peripheral wired to two or more pins on one side of its
	// host must keep their order along that side, or the wires must cross.
	crossed := 0.0
	if annealOrderCost {
		for i, part := range s.parts[:len(p.Placements)-1] {
			c := p.Placements[i+1]
			host := at[part.hostID]
			type pair struct{ h, o powerLayoutPin }
			bySide := map[string][]pair{}
			for _, op := range c.Pins {
				if op.Net == "" || s.policies[op.Net] == "local_ground" || s.policies[op.Net] == "local_power" {
					continue
				}
				for _, hp := range host.Pins {
					if hp.Net == op.Net {
						if side, err := libPinSide(hp, host.BBox); err == nil {
							bySide[side] = append(bySide[side], pair{hp, op})
						}
					}
				}
			}
			for side, ps := range bySide {
				vertical := side == "left" || side == "right"
				for a := 0; a < len(ps); a++ {
					for b := a + 1; b < len(ps); b++ {
						dh, do := ps[a].h.X-ps[b].h.X, ps[a].o.X-ps[b].o.X
						if vertical {
							dh, do = ps[a].h.Y-ps[b].h.Y, ps[a].o.Y-ps[b].o.Y
						}
						if dh*do < 0 {
							crossed++
						}
					}
				}
			}
		}
	}
	// Facing: an attached pin should exit towards its host pin; otherwise the
	// wire has to wrap round the part's own body, which is what exhausts the
	// terminal router.
	facing := 0.0
	for i, part := range s.parts[:len(p.Placements)-1] {
		c := p.Placements[i+1]
		op, _ := libPin(c, part.ownPin)
		hp, _ := libPin(at[part.hostID], part.hostPin)
		side, err := libPinSide(op, c.BBox)
		if err != nil {
			continue
		}
		ex, ey := annealDir(side)
		if ex*(hp.X-op.X)+ey*(hp.Y-op.Y) <= 0 {
			facing++
		}
		hs, err := libPinSide(hp, at[part.hostID].BBox)
		if err == nil {
			hx, hy := annealDir(hs)
			if hx*(op.X-hp.X)+hy*(op.Y-hp.Y) <= 0 {
				facing++
			}
		}
	}
	// Wire estimate: HPWL of every routed net over all pins in the zone.
	nets := map[string]*layoutBBox{}
	for _, c := range p.Placements {
		for _, pin := range c.Pins {
			if pin.Net == "" || s.policies[pin.Net] == "local_ground" {
				continue
			}
			b, ok := nets[pin.Net]
			if !ok {
				nets[pin.Net] = &layoutBBox{pin.X, pin.Y, pin.X, pin.Y}
				continue
			}
			b.MinX, b.MinY = math.Min(b.MinX, pin.X), math.Min(b.MinY, pin.Y)
			b.MaxX, b.MaxY = math.Max(b.MaxX, pin.X), math.Max(b.MaxY, pin.Y)
		}
	}
	for net, b := range nets {
		w := 1.0 + s.netWeight[net]
		if s.policies[net] == "direct" {
			w += 1
		}
		wire += w * (b.MaxX - b.MinX + b.MaxY - b.MinY)
	}
	env := layoutBBox{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, c := range p.Placements {
		env.MinX, env.MinY = math.Min(env.MinX, c.BBox.MinX), math.Min(env.MinY, c.BBox.MinY)
		env.MaxX, env.MaxY = math.Max(env.MaxX, c.BBox.MaxX), math.Max(env.MaxY, c.BBox.MaxY)
	}
	area := (env.MaxX - env.MinX) * (env.MaxY - env.MinY)
	if overlap+blocked < s.bestIllegal || s.bestIllegal == 0 {
		s.bestIllegal, s.bestIllegalWhy = overlap+blocked, fmt.Sprintf("overlap %.1f, blocked leads %.1f", overlap, blocked)
	}
	return 50*overlap + 200*blocked + 150*facing + 100*labelHits + 150*crossed + wire + 0.002*area, overlap == 0 && blocked == 0
}

// seed places parts greedily in order, each at its cheapest slot given the
// parts already fixed (later parts at their first slot meanwhile).
func (s *annealSolver) seed() annealState {
	return s.seedFrom(make(annealState, len(s.parts)))
}

// seedFrom greedily seeds every part that is not frozen; frozen parts keep
// their preset slot.
func (s *annealSolver) seedFrom(st annealState) annealState {
	for i := range s.parts {
		if s.frozen != nil && s.frozen[i] {
			continue
		}
		bestK, bestC := 0, math.Inf(1)
		for k := 0; k < s.parts[i].choices(); k += 3 {
			st[i] = k
			if c, _ := s.costPrefix(st, i+1); c < bestC {
				bestK, bestC = k, c
			}
		}
		st[i] = bestK
	}
	return st
}

// costPrefix evaluates only the first n parts (the rest are not yet placed).
func (s *annealSolver) costPrefix(st annealState, n int) (float64, bool) {
	saved := s.parts
	s.parts = saved[:n]
	c, ok := s.cost(st[:n])
	s.parts = saved
	return c, ok
}

// anneal returns up to eight distinct legal states, best first.
func (s *annealSolver) anneal(start annealState, iters int) []annealState {
	cur := append(annealState(nil), start...)
	curC, _ := s.cost(cur)
	type found struct {
		st annealState
		c  float64
	}
	var legal []found
	seen := map[string]bool{}
	record := func(st annealState, c float64) {
		k := fmt.Sprint(st)
		if seen[k] {
			return
		}
		seen[k] = true
		legal = append(legal, found{append(annealState(nil), st...), c})
	}
	if _, ok := s.cost(cur); ok {
		record(cur, curC)
	}
	var mutable []int
	for i := range s.parts {
		if s.frozen == nil || !s.frozen[i] {
			mutable = append(mutable, i)
		}
	}
	if len(mutable) == 0 {
		return []annealState{cur}
	}
	t0, t1 := 400.0, 1.0
	for it := 0; it < iters && *s.budget > 0; it++ {
		t := t0 * math.Pow(t1/t0, float64(it)/float64(iters))
		i := mutable[s.rng.Intn(len(mutable))]
		old := cur[i]
		part := s.parts[i]
		n := len(part.slots)
		rot, slot := old/n, old%n
		switch r := s.rng.Float64(); {
		case r < 0.3:
			slot = s.rng.Intn(n)
		case r < 0.45 && len(part.rots) > 1:
			rot = s.rng.Intn(len(part.rots))
		default:
			// Neighbour slot: one step in distance or lateral offset.
			per := 25 // lateral steps per distance row (-60..60 by 5)
			d, l := slot/per, slot%per
			switch s.rng.Intn(4) {
			case 0:
				d++
			case 1:
				d--
			case 2:
				l++
			default:
				l--
			}
			if d < 0 || l < 0 || l >= per || d*per+l >= n {
				continue
			}
			slot = d*per + l
		}
		cur[i] = rot*n + slot
		if cur[i] == old {
			continue
		}
		c, ok := s.cost(cur)
		if c <= curC || s.rng.Float64() < math.Exp((curC-c)/t) {
			curC = c
			if ok {
				record(cur, c)
			}
		} else {
			cur[i] = old
		}
	}
	sort.SliceStable(legal, func(a, b int) bool { return legal[a].c < legal[b].c })
	var out []annealState
	for _, f := range legal {
		out = append(out, f.st)
		if len(out) == 8 {
			break
		}
	}
	return out
}

// learn lengthens the leads of the pins a terminal conflict names, so the
// next round keeps more room there.
func (s *annealSolver) learn(err error, p powerLayoutPlan) {
	boost := func(net string) {
		for _, c := range p.Placements {
			for _, pin := range c.Pins {
				k := c.Designator + "." + pin.Number
				if pin.Net == net && s.leadBoost[k] < 30 {
					s.leadBoost[k] += 10
				}
			}
		}
	}
	var nc *schematicNamingConflict
	if errors.As(err, &nc) {
		boost(nc.net)
		return
	}
	// A net that could not be routed: pull its pins together.
	var rc *schematicRouteConflict
	if errors.As(err, &rc) {
		s.netWeight[rc.net] += 3
		return
	}
}

// pullOwnedPeripherals raises the wire weight of every non-ground net a
// peripheral shares with its host, so the next round keeps owned parts close
// enough for a physical connection rather than a symbol at each end.
func (s *annealSolver) pullOwnedPeripherals() {
	for _, part := range s.parts {
		op, ok := libPin(part.measured, part.ownPin)
		if ok && op.Net != "" && s.policies[op.Net] != "local_ground" {
			s.netWeight[op.Net] += 2
		}
	}
}

func slicesContainsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// schematicAnnealHandlesRotations reports whether this zone goes through the
// annealing placer (directly or as the search's fallback), which then owns
// the choice of allowed rotations.
func schematicAnnealHandlesRotations(input SchematicLayoutInput, budget int) bool {
	if schematicAnnealDisabled {
		return false
	}
	peripherals := len(input.Components) - 1
	if peripherals >= annealMinPeripherals {
		return true
	}
	return peripherals >= annealFallbackMinPeripherals && budget >= annealFallbackMinBudget
}

// annealStaged: place the tight parts first, freeze them, then the rest
// (dense-first staging, 2026-09-24). Tight = hangs on a core pin with a
// same-side neighbour within one pitch that must carry its own label, or
// wires to two or more core signal pins (crystal, SWD header); their host
// chain comes along. Stage 1 anneals only those with the boundary-label cost
// on; stage 2 freezes them and anneals the rest with it off. Up to three
// stage-1 layouts are tried before the ordinary rounds run.
// OFF: stress L1 made it a net loss as-is (ESP32 MCU V3/V4 unsolved, BUZZER
// 20k -> 50k: stage-1 gates spend the budget the learned rounds need) and it
// cannot solve the dense-edge cases, whose root cause is the straight-lead-only
// naming (a pin between two port-labelled neighbours needs bent leaders).
var annealStaged = false

func (s *annealSolver) denseParts() []bool {
	dense := make([]bool, len(s.parts))
	index := map[string]int{}
	for i, part := range s.parts {
		index[part.id] = i
	}
	needsLabel := func(net string) bool {
		if net == "" || s.netPins[net] > 1 {
			return false
		}
		switch s.policies[net] {
		case "module_port", "local_ground", "local_power":
			return true
		}
		return false
	}
	signal := func(net string) bool {
		pol := s.policies[net]
		return net != "" && pol != "local_ground" && pol != "local_power"
	}
	for i, part := range s.parts {
		if part.hostID == s.input.CoreComponentID {
			if hp, ok := libPin(s.core, part.hostPin); ok {
				for _, n := range s.core.Pins {
					same := n.Rotation != nil && hp.Rotation != nil && *n.Rotation == *hp.Rotation
					if n.Number != hp.Number && same && needsLabel(n.Net) && math.Abs(n.X-hp.X)+math.Abs(n.Y-hp.Y) <= 10.5 {
						dense[i] = true
					}
				}
			}
		}
		links := 0
		for _, op := range part.measured.Pins {
			if !signal(op.Net) {
				continue
			}
			for _, cp := range s.core.Pins {
				if cp.Net == op.Net {
					links++
					break
				}
			}
		}
		if links >= 2 {
			dense[i] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for i, part := range s.parts {
			if j, ok := index[part.hostID]; ok && dense[i] && !dense[j] {
				dense[j], changed = true, true
			}
		}
	}
	return dense
}

func (s *annealSolver) staged(routing *schematicRoutingContext) (*SchematicLayoutResult, error) {
	dense := s.denseParts()
	var sub []*annealPart
	var at []int
	for i, d := range dense {
		if d {
			sub = append(sub, s.parts[i])
			at = append(at, i)
		}
	}
	if len(sub) == 0 || len(sub) == len(s.parts) {
		return nil, nil // nothing to stage, or everything is tight: ordinary rounds
	}
	first := *s
	first.parts, first.labelOn, first.frozen = sub, true, nil
	iters := 20000
	if *s.budget < 6000 {
		iters = max(2000, *s.budget*2)
	}
	stage1 := first.anneal(first.seed(), iters)
	s.evals = first.evals
	s.frozen = dense
	defer func() { s.frozen = nil }()
	var lastErr error
	for k, f1 := range stage1 {
		if k == annealStagedAlternatives || *s.budget <= 0 {
			break
		}
		preset := make(annealState, len(s.parts))
		for j, i := range at {
			preset[i] = f1[j]
		}
		finals := s.anneal(s.seedFrom(preset), iters)
		for n, st := range finals {
			if n == 2 {
				break
			}
			out, err := s.gate(st, n == 0, routing, fmt.Sprintf("anneal-v1 staged (dense %d/%d, stage-1 #%d)", len(sub), len(s.parts), k+1))
			if out != nil {
				return out, nil
			}
			if err != nil {
				lastErr = err
			}
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("staged: %w", lastErr)
	}
	return nil, nil
}

const annealStagedAlternatives = 3

// gate runs one terminal-gate attempt (routing, naming, peripheral-direct).
func (s *annealSolver) gate(st annealState, lead bool, routing *schematicRoutingContext, strategy string) (*SchematicLayoutResult, error) {
	p := s.plan(st)
	if err := validateLibGeometry(&p); err != nil {
		return nil, err
	}
	slice := min(*s.budget, max(6000, *s.budget/3))
	if lead {
		slice = min(*s.budget, max(6000, *s.budget*3/4))
	}
	slice = min(slice, max(6000, annealFinishAttemptCap))
	if annealCandidateHook != nil {
		annealCandidateHook(p)
	}
	spent := slice
	done, err := libFinishSchematicLayoutRegenerate(p, s.policies, &slice, routing)
	*s.budget -= spent - slice
	if annealAttemptHook != nil {
		annealAttemptHook(-1, spent-slice, err)
	}
	if err != nil {
		s.learn(err, p)
		return nil, err
	}
	tmp := &SchematicLayoutResult{Placements: done.Placements, Wires: done.Wires, Flags: done.Flags, ComponentIDs: s.refs}
	if perr := validateSchematicLayoutPeripheralDirect(tmp, s.input.CoreComponentID, s.roles); perr != nil {
		s.pullOwnedPeripherals()
		return nil, perr
	}
	return &SchematicLayoutResult{Placements: done.Placements, Wires: done.Wires, Flags: done.Flags, Score: libCandidateScore(done),
		Search: &SchematicLayoutSearchDiagnostics{Strategy: fmt.Sprintf("%s, %d evaluations", strategy, s.evals), MovedComponents: []string{}}}, nil
}
