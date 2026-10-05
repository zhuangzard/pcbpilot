package pcbauto

// Placement aesthetics, Phase B (docs/reviews/2026-09-routing-aesthetics
// README §4–5, baseline.md §5): the placer's last stage turns a legal,
// electrically placed board into one that looks designed — parts of a kind
// turned the same way, near-aligned rows and columns pulled onto one line,
// arrays at an even pitch, repeated sub-circuits laid out as mirror or
// translated copies, every origin on the grid.
//
// Aesthetics is the lowest tier of ConstraintPriority. Every move here is a
// transaction (aesTry) judged on the parts it touches, their neighbours and
// every part whose electrical term depends on them:
//
//	safety / legality   hard cost (overlap at the courtyard gap, zone, board
//	                    region, keep-outs, holes, height, domain edge band,
//	                    isolation and high-voltage pad gaps) — never higher
//	electrical          critical tethers (decap, hot loop, bootstrap, clock,
//	                    protection, power path, pin filter, power stage) and
//	                    feedback, converter hot-loop/feedback terms, signal
//	                    chain order and pair twist, pair corridors and flow,
//	                    port reserves and keep-apart — each never higher
//	efficiency          weighted HPWL plus the routing-comfort tethers may
//	                    grow only within the style profile's slack budget
//	                    (AesProfile.Slack.WirelengthPct of the placement's
//	                    own total)
//
// A rejected transaction is rolled back exactly. The routed check comes
// after: PlaceThenRoute / PlaceRoute route the board without this stage too
// and keep the aesthetic placement only when it routes no worse on every
// zero-tolerance count and lowers no electrical sub-score by more than the
// profile's ElectricalTol, adding at most PlacementViaAllowance vias
// (aesRoutedWorse, placeab.go; trades reported,
// aesthetics_tolerance.go); else the grid-only stage (functional
// profile, computed here from the same start) is tried, else the v0.7
// placement (tidyV07, placer.go) stands.
//
// Why v0.7's tidy() did nothing on the ESP32 sample (baseline.md §5): its
// 180° folds swap a two-pad part's pad nets (+2–60 % local wire, rejected at
// a 2 % limit); its grid snaps moved one part into its flush-packed
// neighbour (any overlap rejected, nobody made room); and it required the
// hard cost to be zero after the move, so a part already touching a
// neighbour could not move even 0.01 mil. Here a fold must be electrically
// equivalent (no worse in every term, wire included), the orientation vote is
// on the axis so a part keeps the direction its pad nets want, a snap shoves
// its neighbours onto the grid with it, and the judgement is "no worse", not
// "zero".

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

var aesDebug = os.Getenv("PCBPILOT_AES_DEBUG") != ""

// AesPlaceReport is what the aesthetics stage did to a placement.
type AesPlaceReport struct {
	Profile string   `json:"profile"`
	Auto    *AesAuto `json:"auto,omitempty"`
	// Passes are the stages the profile enables (orient, symmetry, align,
	// pitch, grid).
	Passes   []string       `json:"passes"`
	GridMil  float64        `json:"gridMil"`
	Accepted map[string]int `json:"accepted"`
	// Rejected counts, per pass, the parts / groups whose every candidate
	// was refused, by the first tier that refused the last candidate.
	Rejected map[string]int `json:"rejected,omitempty"`
	Moved    int            `json:"movedParts"`
	// WireBudgetMil is the slack the profile allows (weighted HPWL + comfort
	// tethers, mil); WireSpentMil what the accepted moves used (net).
	WireBudgetMil float64      `json:"wireBudgetMil"`
	WireSpentMil  float64      `json:"wireSpentMil"`
	Symmetry      []AesSymMove `json:"symmetry,omitempty"`
	// Guard is the routed check's decision (PlaceThenRoute / PlaceRoute):
	// "kept …", "rolled back …" or empty when the board was not routed here.
	Guard string `json:"routedGuard,omitempty"`
	// ElectricalTol is the guard's per-item electrical tolerance (the
	// profile's, aesthetics_tolerance.go); Trades every electrical item
	// (and in-budget raw IR drop) the kept placement made worse within it.
	ElectricalTol float64    `json:"electricalTolerance"`
	Trades        []AesTrade `json:"electricalTrades,omitempty"`
	// ViaAllowance is how many vias the kept placement may add over the
	// one without the stage (the profile's PlacementViaAllowance; only when
	// the electrical group does not drop; a used allowance is a trade).
	ViaAllowance int `json:"placementViaAllowance"`
	// Strict: the grid snap ran without its quantisation allowance.
	Strict bool     `json:"strict,omitempty"`
	Notes  []string `json:"notes,omitempty"`

	// The placement before the stage and after the grid-only stage, for
	// the routed guard's roll-back.
	raw   *aesPoses
	lites []*aesPoses
	// rerun repeats the stage from the pre-stage placement with extra
	// parts held still (the guard's targeted roll-back); nil after Place
	// when nothing moved.
	rerun func(hold map[*Part]bool) *aesPoses
}

// aesPoses is one placement the routed guard can fall back to.
type aesPoses struct {
	poses   map[*Part]savedPart
	outline []Point // the auto-sized outline of these poses (nil: fixed board)
	place   []Placement
	metrics PlaceMetrics
	rep     *AesPlaceReport // the stage that produced it (nil = no stage)
}

// apply puts the poses (and their outline) on the board.
func (a *aesPoses) apply(b *Board) {
	for p, ps := range a.poses {
		p.MoveTo(ps.pos, ps.rot)
	}
	if a.outline != nil {
		b.Outline = append([]Point(nil), a.outline...)
	}
	_ = b.Index()
}

// aesSnapshot records the current placement as a fall-back.
func (pl *placer) aesSnapshot(res *PlaceResult, rep *AesPlaceReport) *aesPoses {
	a := &aesPoses{poses: map[*Part]savedPart{}, rep: rep}
	for _, p := range pl.b.Parts {
		a.poses[p] = savedPart{p.Pos, p.Rotation}
		a.place = append(a.place, Placement{Ref: p.Ref, ID: p.ID, X: round2(p.Pos.X), Y: round2(p.Pos.Y),
			Rot: p.Rotation, Side: p.Side, Fixed: p.Fixed, Block: pl.c.BlockOf[p.Ref]})
	}
	if pl.m.AutoSize {
		a.outline = append([]Point(nil), pl.autosize()...)
	}
	var r PlaceResult
	r.Metrics = res.Metrics
	pl.metrics(&r)
	a.metrics = r.Metrics
	return a
}

// AesSymMove is one instance pair of a detected isomorphic group.
type AesSymMove struct {
	Kind        string  `json:"kind"`
	Signature   string  `json:"signature"`
	Reference   string  `json:"reference"`
	Moved       string  `json:"moved"`
	Transform   string  `json:"transform"`
	ErrorBefore float64 `json:"errorBefore"`
	ErrorAfter  float64 `json:"errorAfter"`
	Result      string  `json:"result"`
}

// aesState is the stage's working state.
type aesState struct {
	prof     AesProfile
	rep      *AesPlaceReport
	budget   float64 // remaining wire + comfort slack (mil)
	excluded map[*Part]bool
	rel      map[*Part][]*Part
	idx      map[*Part]int
	grid     float64
	isoReach float64
	moved    map[*Part]bool
	lastWhy  string
	// Looks already on the board (aesIndexLooks): class lists for P1 and
	// the arrays for P2.
	class    map[*Part]string
	byClass  map[string][]*Part
	rows     []aesRow
	p2S, p2W float64 // P2 totals: Σ steps × pitch CV, Σ steps
	// powerStage: switcher power-stage parts (moved by the symmetry pass only).
	powerStage map[*Part]bool
	// snapG is the grid the current snap / shove rounds to.
	snapG float64
	// sym are the detected isomorphic groups (P4), symOf a part's groups.
	sym   []SymmetryGroup
	symOf map[*Part][]int
}

type aesMove struct {
	p   *Part
	pos Point
	rot float64
}

// aesCost is one evaluation of a transaction scope, by tier.
type aesCost struct {
	hard, crit, conv, chain, flow, res, comfort, wire float64
}

func (c aesCost) charge() float64 { return c.wire + c.comfort }

// resolveAesProfile turns PlaceOptions.Aesthetics into a preset (nil =
// DefaultAesProfile, Name "auto" = chosen from the board).
func (pl *placer) resolveAesProfile() AesProfile {
	p := pl.opt.Aesthetics
	switch {
	case p == nil:
		d, _ := AesProfileByName(DefaultAesProfile)
		return d
	case p.Name == "auto":
		return AutoAesProfile(pl.b, pl.an, pl.b.CopperLayers)
	}
	return p.clone()
}

// aesPasses are the stages a profile runs. Functional ("nearly off"): only
// the grid snap and electrically equivalent folds, with no slack. A profile
// with slack runs everything.
func aesPasses(p AesProfile) []string {
	if p.Slack.WirelengthPct <= 0 {
		return []string{"fold", "grid"}
	}
	return []string{"fold", "symmetry", "orient", "align", "pitch", "grid"}
}

// aesthetics runs the stage on the current (legal) placement.
func (pl *placer) aesthetics(res *PlaceResult, prof AesProfile) {
	rep := &AesPlaceReport{Profile: prof.Name, Auto: prof.Auto, ElectricalTol: prof.ElectricalTol, ViaAllowance: prof.PlacementViaAllowance, Accepted: map[string]int{}, Rejected: map[string]int{}}
	res.Aesthetics = rep
	pl.rebuildBuckets()
	st := &aesState{prof: prof, rep: rep, excluded: map[*Part]bool{}, idx: map[*Part]int{}, moved: map[*Part]bool{}}
	for i, p := range pl.b.Parts {
		st.idx[p] = i
	}
	movable := map[*Part]bool{}
	for _, p := range pl.movable {
		movable[p] = true
	}
	for p := range pl.aesHold {
		st.excluded[p] = true
	}
	for _, p := range pl.b.Parts {
		// Never moved for looks: fixed and mechanical parts, isolation
		// bridges (their slot and the strip opening follow them) and every
		// part of an intent pair's corridor (connector, ESD, series parts,
		// IC: their pose is the pair's routing).
		if !movable[p] || p.Fixed || pl.c.Kinds[p.Ref] == KindMechanical || pl.c.IsBridge(p.Ref) {
			st.excluded[p] = true
		} else if pl.corridorShare[p] > 0 {
			// A pair-corridor part stays put: on the ESP32 sample a 1.9 mil
			// snap of the USB ESD array alone cost the routed hot loop 2.7
			// points (route-only bisection, baseline.md §11).
			st.excluded[p] = true
		}
	}
	// A switcher's power stage (IC, inductor, hot-loop cap, catch diode,
	// bootstrap) moves only to copy a repeated converter (the symmetry
	// pass): on the ESP32 sample 1–3 mil snaps of the buck's IC and
	// inductor cost the routed hot loop 2 points although the placement
	// hot-loop term did not move.
	st.powerStage = map[*Part]bool{}
	for _, cv := range pl.c.Converters {
		for _, r := range []string{cv.Core, cv.Inductor, cv.HotCap, cv.Diode, cv.Bootstrap} {
			if p := pl.b.Part(r); p != nil {
				st.powerStage[p] = true
			}
		}
	}
	if pl.an != nil && pl.an.Iso != nil {
		st.isoReach = pl.an.Iso.MaxClearanceMil()
	}
	st.grid = 5
	if prof.GridBlend >= 0.5 && prof.PlacementGridMil > 0 {
		st.grid = prof.PlacementGridMil
	}
	rep.GridMil = st.grid
	st.snapG = st.grid
	st.rel = pl.aesRelations(st.idx)

	pl.aes = st
	defer func() { pl.aes = nil }()

	total := pl.wirelength()
	for _, p := range pl.b.Parts { // board order: a reproducible float sum
		if t := pl.tether[p]; t != nil && !criticalRoles[t.role] && t.role != "feedback" {
			total += pl.tetherCost(p)
		}
	}
	st.budget = total * prof.Slack.WirelengthPct / 100
	rep.WireBudgetMil = round2(st.budget)
	rep.Passes = aesPasses(prof)
	// Budget shares: what a pass leaves carries to the next one; the grid
	// snap (tiny moves, the most visible win) always gets the rest.
	share := map[string]float64{"orient": 0.25, "symmetry": 0.35, "align": 0.5, "pitch": 0.5, "fold": 0, "grid": 1}
	for _, pass := range rep.Passes {
		pl.aesIndexLooks()
		cap := st.budget * share[pass]
		switch pass {
		case "fold":
			pl.aesFold()
		case "orient":
			pl.aesOrient(cap)
		case "symmetry":
			pl.aesSymmetry(cap)
		case "align":
			pl.aesAlign(cap)
		case "pitch":
			pl.aesPitch(cap)
		case "grid":
			pl.aesGrid()
		}
	}
	rep.Moved = len(st.moved)
	rep.WireSpentMil = round2(rep.WireSpentMil)
	if len(rep.Rejected) == 0 {
		rep.Rejected = nil
	}
	res.Notes = append(res.Notes, fmt.Sprintf("aesthetics (%s): %d part(s) moved — %s; wire slack %.0f of %.0f mil", prof.Name, rep.Moved,
		aesCounts(rep.Accepted), rep.WireSpentMil, rep.WireBudgetMil))
}

func aesCounts(m map[string]int) string {
	if len(m) == 0 {
		return "nothing accepted"
	}
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var out []string
	for _, k := range ks {
		out = append(out, fmt.Sprintf("%s %d", k, m[k]))
	}
	return strings.Join(out, ", ")
}

// aesRelations links every part to the parts whose electrical terms read
// its position: tether owners and their cores, converter members, signal
// chain members, diff-pair partners, keep-apart pairs.
func (pl *placer) aesRelations(idx map[*Part]int) map[*Part][]*Part {
	set := map[*Part]map[*Part]bool{}
	link := func(a, b *Part) {
		if a == nil || b == nil || a == b {
			return
		}
		for _, x := range [2][2]*Part{{a, b}, {b, a}} {
			if set[x[0]] == nil {
				set[x[0]] = map[*Part]bool{}
			}
			set[x[0]][x[1]] = true
		}
	}
	partOfKey := func(key string) *Part {
		if pd := padAt(pl.b, key); pd != nil {
			return pl.b.Part(pd.Part)
		}
		return pl.b.Part(key)
	}
	for d, t := range pl.tether {
		for _, pd := range t.pads {
			link(d, pl.b.Part(pd.Part))
		}
	}
	for _, cv := range pl.c.Converters {
		var ms []*Part
		for _, r := range append([]string{cv.Core, cv.Inductor, cv.Diode, cv.HotCap, cv.Bootstrap}, cv.Feedback...) {
			if p := pl.b.Part(r); p != nil {
				ms = append(ms, p)
			}
		}
		for i := range ms {
			for j := i + 1; j < len(ms); j++ {
				link(ms[i], ms[j])
			}
		}
	}
	for p, chs := range pl.chains {
		for _, ch := range chs {
			for _, c := range []*SignalChain{ch, ch.Pair} {
				if c == nil {
					continue
				}
				link(p, partOfKey(c.Connector))
				link(p, partOfKey(c.IC))
				for _, n := range c.Nodes {
					link(p, pl.b.Part(n.Ref))
				}
			}
		}
	}
	for p, q := range pl.pairOf {
		link(p, q)
	}
	for _, pr := range pl.apart {
		link(pr[0], pr[1])
	}
	out := map[*Part][]*Part{}
	for p, s := range set {
		l := make([]*Part, 0, len(s))
		for q := range s {
			l = append(l, q)
		}
		sort.Slice(l, func(i, j int) bool { return idx[l[i]] < idx[l[j]] })
		out[p] = l
	}
	return out
}

func (pl *placer) aesApply(p *Part, pos Point, rot float64) {
	pl.bucketOp(p, false)
	p.MoveTo(pos, rot)
	pl.boxes[p] = pl.box(p)
	pl.bucketOp(p, true)
}

// aesScope is every part whose cost a transaction can change: the moved
// parts, their electrical relations, and everything within reach of their
// old or new courtyard (overlap, isolation and high-voltage pad gaps). The
// nets are the moved parts' placement nets.
func (pl *placer) aesScope(parts []*Part, oldBox map[*Part]Rect) ([]*Part, []int) {
	st := pl.aes
	in := map[*Part]bool{}
	reach := math.Max(pl.hvReach, st.isoReach) + 1
	for _, p := range parts {
		in[p] = true
		for _, q := range st.rel[p] {
			in[q] = true
		}
		for _, r := range []Rect{oldBox[p], pl.boxes[p]} {
			pl.forBuckets(r.Expand(reach), func(q *Part) { in[q] = true })
		}
	}
	scope := make([]*Part, 0, len(in))
	for q := range in {
		scope = append(scope, q)
	}
	sort.Slice(scope, func(i, j int) bool { return st.idx[scope[i]] < st.idx[scope[j]] })
	var nets []int
	for _, p := range parts {
		nets = append(nets, pl.partNet[p]...)
	}
	sort.Ints(nets)
	u := nets[:0]
	for k, i := range nets {
		if k == 0 || i != nets[k-1] {
			u = append(u, i)
		}
	}
	return scope, u
}

func (pl *placer) aesEval(scope []*Part, nets []int) aesCost {
	var c aesCost
	for _, q := range scope {
		c.hard += pl.hardCost(q)
		if t := pl.tether[q]; t != nil {
			v := pl.tetherCost(q)
			if criticalRoles[t.role] || t.role == "feedback" {
				c.crit += v
			} else {
				c.comfort += v
			}
		}
		c.conv += pl.converterCost(q)
		c.chain += pl.chainCost(q)
		c.flow += pl.corridorCost(q) + pl.pairFlowCost(q)
		c.res += pl.reserveCost(q)
		for _, pr := range pl.apart {
			if pr[0] == q || pr[1] == q {
				d := pr[0].Body().Center().Dist(pr[1].Body().Center())
				c.res += 3 * math.Max(0, keepApartMil-d)
			}
		}
	}
	for _, i := range nets {
		c.wire += pl.netCost(i)
	}
	return c
}

// aesWorse names the first tier a transaction makes worse ("" = none);
// allow is the efficiency slack it may spend (mil); q is the grid
// quantisation allowance of a snap (aesQuant), zero for every other move.
func aesWorse(before, after aesCost, allow float64, q aesCost) string {
	tol := func(v float64) float64 { return 1e-6 + 1e-9*math.Abs(v) }
	switch {
	case after.hard > before.hard+tol(before.hard):
		return "safety/legality"
	case after.crit > before.crit+q.crit+tol(before.crit):
		return "critical tether"
	case after.conv > before.conv+q.conv+tol(before.conv):
		return "converter loop"
	case after.chain > before.chain+q.chain+tol(before.chain):
		return "signal chain"
	case after.flow > before.flow+q.flow+tol(before.flow):
		return "pair corridor"
	case after.res > before.res+tol(before.res):
		return "port reserve / keep-apart"
	case after.charge()-before.charge() > math.Max(0, allow)+tol(before.charge()):
		return "wire slack"
	}
	return ""
}

// aesTry applies mv as one transaction and keeps it only when no tier
// gets worse and the efficiency cost stays within allow (and veto, run on
// the moved state, returns ""). Returns whether it was kept.
func (pl *placer) aesTry(kind string, mv []aesMove, allow float64, veto func() string) bool {
	st := pl.aes
	seen := map[*Part]bool{}
	var parts []*Part
	changed := false
	snap := strings.HasPrefix(kind, "grid")
	for _, m := range mv {
		if m.p == nil || st.excluded[m.p] || seen[m.p] {
			st.lastWhy = "excluded part"
			return false
		}
		if st.powerStage[m.p] && !strings.HasPrefix(kind, "symmetry") {
			st.lastWhy = "converter power stage"
			return false
		}
		if pl.opt.NoRotate && normDeg(m.rot) != m.p.Rotation {
			st.lastWhy = "rotation disabled"
			return false
		}
		seen[m.p] = true
		parts = append(parts, m.p)
		if m.pos.Dist(m.p.Pos) > 1e-9 || normDeg(m.rot) != m.p.Rotation {
			changed = true
		}
	}
	if !changed {
		st.lastWhy = "no change"
		return false
	}
	old := make([]aesMove, len(mv))
	oldBox := map[*Part]Rect{}
	for i, m := range mv {
		old[i] = aesMove{m.p, m.p.Pos, m.p.Rotation}
		oldBox[m.p] = pl.boxes[m.p]
	}
	for _, m := range mv {
		pl.aesApply(m.p, m.pos, normDeg(m.rot))
	}
	scope, nets := pl.aesScope(parts, oldBox)
	after := pl.aesEval(scope, nets)
	why := ""
	if veto != nil {
		why = veto()
	}
	for i := len(old) - 1; i >= 0; i-- {
		pl.aesApply(old[i].p, old[i].pos, old[i].rot)
	}
	if why == "" {
		before := pl.aesEval(scope, nets)
		var q aesCost
		if strings.HasPrefix(kind, "grid") && !pl.aesStrict {
			q = pl.aesQuant(parts, scope)
		}
		why = aesWorse(before, after, allow, q)
		if aesDebug {
			var rs []string
			for _, m := range mv {
				rs = append(rs, fmt.Sprintf("%s(%.2f,%.2f@%.0f→%.2f,%.2f@%.0f)", m.p.Ref, m.p.Pos.X, m.p.Pos.Y, m.p.Rotation, m.pos.X, m.pos.Y, m.rot))
			}
			fmt.Fprintf(os.Stderr, "AES %s %s why=%q before=%+v after=%+v allow=%.1f\n", kind, strings.Join(rs, " "), why, before, after, allow)
		}
		if why == "" {
			// Looks: a move may not undo looks the board already has — a
			// part leaving the row/column it shared with a class neighbour
			// (P1) or an array losing its even pitch (P2).
			nb := pl.aesNeighbours(parts)
			for _, m := range mv {
				pl.aesApply(m.p, m.pos, normDeg(m.rot))
			}
			nb = append(nb, pl.aesNeighbours(parts)...)
			alA, sA, wA := pl.aesLooks(nb, parts)
			symA := pl.aesSymLooks(parts)
			alB, sB, wB := pl.aesLooksAt(nb, parts, old)
			symB := pl.aesSymLooksAt(parts, old)
			p2B := (st.p2S - sB + sB) / math.Max(st.p2W, 1e-9)
			p2A := (st.p2S - sB + sA) / math.Max(st.p2W-wB+wA, 1e-9)
			switch {
			case alA < alB:
				why = "looks: row/column alignment lost"
			case p2A > p2B+1e-6 && kind != "pitch":
				why = "looks: array pitch"
			case symWorse(symB, symA, snap) && !strings.HasPrefix(kind, "symmetry"):
				why = "looks: symmetry"
			}
			if why != "" {
				for i := len(old) - 1; i >= 0; i-- {
					pl.aesApply(old[i].p, old[i].pos, old[i].rot)
				}
				st.lastWhy = why
				return false
			}
			for _, m := range mv {
				st.moved[m.p] = true
			}
			st.p2S += sA - sB
			st.p2W += wA - wB
			d := after.charge() - before.charge()
			st.budget -= d
			st.rep.WireSpentMil += d
			st.rep.Accepted[kind]++
			st.lastWhy = ""
			return true
		}
	}
	st.lastWhy = why
	return false
}

// aesNeighbours is the moved parts and their same-class neighbours (P1's
// neighbourhood: same side, ≤ 3 body sizes) in the current state.
func (pl *placer) aesNeighbours(parts []*Part) []*Part {
	st := pl.aes
	var out []*Part
	for _, p := range parts {
		out = append(out, p)
		pc, ps := p.Body().Center(), aesSize(p)
		for _, q := range st.byClass[st.class[p]] {
			if q != p && pc.Dist(q.Body().Center()) <= 3*math.Max(ps, aesSize(q)) {
				out = append(out, q)
			}
		}
	}
	return out
}

func aesSize(p *Part) float64 {
	bd := p.Body()
	return math.Max(bd.W(), bd.H())
}

// aesLooks counts the parts of set that share a row or column with a
// same-class neighbour (P1 on that set), and returns the P2 terms (Σ
// steps × pitch CV, Σ steps) of the arrays of the moved parts' classes, in
// the current state.
func (pl *placer) aesLooks(set, moved []*Part) (int, float64, float64) {
	st := pl.aes
	tol := st.prof.AlignTolMil
	seen := map[*Part]bool{}
	aligned := 0
	for _, p := range set {
		if seen[p] {
			continue
		}
		seen[p] = true
		pc, ps := p.Body().Center(), aesSize(p)
		for _, q := range st.byClass[st.class[p]] {
			if q == p {
				continue
			}
			qc := q.Body().Center()
			if pc.Dist(qc) > 3*math.Max(ps, aesSize(q)) {
				continue
			}
			if math.Abs(pc.X-qc.X) <= tol || math.Abs(pc.Y-qc.Y) <= tol {
				aligned++
				break
			}
		}
	}
	ks := map[string]bool{}
	var s, w float64
	for _, p := range moved {
		k := st.class[p]
		if k == "" || ks[k] {
			continue
		}
		ks[k] = true
		cs, cw := pl.aesClassP2(k)
		s, w = s+cs, w+cw
	}
	return aligned, s, w
}

// aesLooksAt is aesLooks with the old poses (the state before the
// transaction); the new poses are put back afterwards.
func (pl *placer) aesLooksAt(set, moved []*Part, old []aesMove) (int, float64, float64) {
	cur := make([]aesMove, len(old))
	for i, m := range old {
		cur[i] = aesMove{m.p, m.p.Pos, m.p.Rotation}
	}
	for i := len(old) - 1; i >= 0; i-- {
		pl.aesApply(old[i].p, old[i].pos, old[i].rot)
	}
	a, s, w := pl.aesLooks(set, moved)
	for _, m := range cur {
		pl.aesApply(m.p, m.pos, m.rot)
	}
	return a, s, w
}

// aesSymLooks re-measures every detected symmetry group that holds a
// moved part (P4), in the current state.
func (pl *placer) aesSymLooks(moved []*Part) []float64 {
	st := pl.aes
	var gs []int
	seen := map[int]bool{}
	for _, p := range moved {
		for _, gi := range st.symOf[p] {
			if !seen[gi] {
				seen[gi] = true
				gs = append(gs, gi)
			}
		}
	}
	sort.Ints(gs)
	out := make([]float64, 0, 2*len(gs))
	for _, gi := range gs {
		out = append(out, float64(gi), st.sym[gi].groupError(pl.b))
	}
	return out
}

func (pl *placer) aesSymLooksAt(moved []*Part, old []aesMove) []float64 {
	cur := make([]aesMove, len(old))
	for i, m := range old {
		cur[i] = aesMove{m.p, m.p.Pos, m.p.Rotation}
	}
	for i := len(old) - 1; i >= 0; i-- {
		pl.aesApply(old[i].p, old[i].pos, old[i].rot)
	}
	v := pl.aesSymLooks(moved)
	for _, m := range cur {
		pl.aesApply(m.p, m.pos, m.rot)
	}
	return v
}

// symWorse: some group's error grew (pairs of group index, error). A grid
// snap may move a group's error within the "exact" band (≤ 0.02 of its span:
// a 2.5 mil step on a 125 mil module), never out of it or further above it.
func symWorse(before, after []float64, snap bool) bool {
	for i := 1; i < len(before) && i < len(after); i += 2 {
		lim := before[i] + 1e-4
		if snap {
			lim = math.Max(lim, 0.02)
		}
		if after[i] > lim {
			return true
		}
	}
	return false
}

// aesClassP2 is one class's share of P2: Σ steps × CV and Σ steps over its
// rows / columns of ≥ 3 aligned parts (aesRows).
func (pl *placer) aesClassP2(k string) (float64, float64) {
	st := pl.aes
	var aps []*aPart
	for _, p := range st.byClass[k] {
		aps = append(aps, aesPartOf(p, pl.c))
	}
	var s, w float64
	for _, r := range aesRows(map[string][]*aPart{k: aps}, []string{k}, st.prof.AlignTolMil) {
		n := float64(len(r.steps))
		s += n * r.cv
		w += n
	}
	return s, w
}

// aesRow is a row / column of ≥ 3 aligned same-class parts (P2).
type aesRow struct {
	parts []*Part
	axis  int // along: 0 = x (a row), 1 = y (a column)
}

// aesIndexLooks (re)builds the class lists, the arrays and the P2 totals of
// the current placement.
func (pl *placer) aesIndexLooks() {
	st := pl.aes
	st.class, st.byClass = map[*Part]string{}, map[string][]*Part{}
	by, keys := pl.aesClasses()
	for _, k := range keys {
		for _, a := range by[k] {
			st.class[a.p] = k
			st.byClass[k] = append(st.byClass[k], a.p)
		}
	}
	st.sym, st.symOf = DetectSymmetry(pl.b, pl.an, pl.c), map[*Part][]int{}
	for gi, g := range st.sym {
		for _, in := range g.Instances {
			for _, r := range in {
				if p := pl.b.Part(r); p != nil {
					st.symOf[p] = append(st.symOf[p], gi)
				}
			}
		}
	}
	st.rows = nil
	st.p2S, st.p2W = 0, 0
	for _, r := range aesRows(by, keys, st.prof.AlignTolMil) {
		row := aesRow{axis: 0}
		if r.axis == "column" {
			row.axis = 1
		}
		for _, ref := range r.refs {
			row.parts = append(row.parts, pl.b.Part(ref))
		}
		st.rows = append(st.rows, row)
		n := float64(len(r.steps))
		st.p2S += n * r.cv
		st.p2W += n
	}
}

// aesQuant is the grid quantisation allowance of a snap: a part on a 5 mil
// grid sits up to one grid step from any spot, so a snap may take a
// tethered auxiliary (or the core it serves) up to one step further from
// its pin — weight × step per affected critical tether — a chain node two
// steps of detour and a corridor one step of intrusion. A switcher's hot
// loop gets none: on the ESP32 sample a snap of its loop parts cost the
// routed hot loop 2 points.
// Safety / legality never gets an allowance. Only
// the grid pass gets it; every other move must be no worse at all. The
// routed guard (placeab.go) still judges the electrical items on copper
// without any allowance.
func (pl *placer) aesQuant(parts, scope []*Part) aesCost {
	g := 5.0 // the audit grid's step, whatever the target grid
	moved := map[*Part]bool{}
	for _, p := range parts {
		moved[p] = true
	}
	var q aesCost
	for _, x := range scope {
		t := pl.tether[x]
		if t == nil || !criticalRoles[t.role] && t.role != "feedback" {
			continue
		}
		touch := moved[x]
		for _, pd := range t.pads {
			if moved[pl.b.Part(pd.Part)] {
				touch = true
			}
		}
		if touch {
			q.crit += t.w * g
		}
	}
	for _, p := range parts {
		// Chain detour: a node one step off moves its two hops ≤ 2 steps
		// (a pair twist costs 400 mil of detour and still fails).
		for _, ch := range pl.chains[p] {
			q.chain += 1.5 * ch.Weight * 2 * g
		}
		// Corridor: a part one step off reaches ≤ one step further into a
		// corridor; a corridor part moves its corridor by as much.
		if pl.corridorShare[p] > 0 {
			q.flow += 4 * corridorWeight * g
		} else if len(pl.corridors) > 0 {
			q.flow += corridorWeight * g
		}
	}
	return q
}

// aesTryShove is aesTry, and when the move collides, aesTry of the move
// with the colliding neighbours shoved aside.
func (pl *placer) aesTryShove(kind string, mv []aesMove, allow float64, veto func() string) bool {
	if pl.aesTry(kind, mv, allow, veto) {
		return true
	}
	if pl.aes.lastWhy != "safety/legality" {
		return false
	}
	why := pl.aes.lastWhy
	if sm := pl.aesShoveSet(mv, strings.HasPrefix(kind, "grid")); len(sm) > len(mv) && pl.aesTry(kind+"+shove", sm, allow, veto) {
		return true
	}
	pl.aes.lastWhy = why
	return false
}

func (pl *placer) aesReject(kind string) {
	why := pl.aes.lastWhy
	if why == "" {
		why = "no candidate"
	}
	pl.aes.rep.Rejected[kind+": "+why]++
}

// aesOffsets is a square lattice of offsets (step) within radius, nearest
// first (deterministic order).
func aesOffsets(radius, step float64) []Point {
	var out []Point
	n := int(math.Floor(radius / step))
	for dy := -n; dy <= n; dy++ {
		for dx := -n; dx <= n; dx++ {
			d := Point{float64(dx) * step, float64(dy) * step}
			if math.Hypot(d.X, d.Y) <= radius+1e-9 {
				out = append(out, d)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := math.Hypot(out[i].X, out[i].Y), math.Hypot(out[j].X, out[j].Y)
		if math.Abs(di-dj) > 1e-9 {
			return di < dj
		}
		if out[i].Y != out[j].Y {
			return out[i].Y < out[j].Y
		}
		return out[i].X < out[j].X
	})
	return out
}

// aesTurn tries p at each rotation (in order), its body centre kept or
// moved by the smallest lattice offset that is accepted.
// A turned part that collides with its neighbours tries the nearest few
// offsets again with them shoved aside (aesShove). The reported reason is
// the one of the unshifted turn.
func (pl *placer) aesTurn(kind string, p *Part, rots []float64, allow, radius float64) bool {
	c := p.Body().Center()
	first := ""
	for _, r := range rots {
		r = normDeg(r)
		if r == p.Rotation {
			continue
		}
		off := pl.offsetAt(p, r)
		base := c.Sub(off)
		for i, d := range aesOffsets(radius, 5) {
			if pl.aesTry(kind, []aesMove{{p, base.Add(d), r}}, allow, nil) {
				return true
			}
			if i == 0 && first == "" {
				first = pl.aes.lastWhy
			}
			if i < 9 && pl.aes.lastWhy == "safety/legality" {
				if mv := pl.aesShove(p, base.Add(d), r, false); len(mv) > 1 && pl.aesTry(kind+"+shove", mv, allow, nil) {
					return true
				}
			}
		}
	}
	if first != "" {
		pl.aes.lastWhy = first
	}
	return false
}

func (pl *placer) aesOrder() []*Part {
	var out []*Part
	for _, p := range pl.b.Parts {
		if !pl.aes.excluded[p] {
			out = append(out, p)
		}
	}
	return out
}

// aesFold turns symmetric passives at 180/270 to 0/90 where that is
// electrically equivalent: the turn swaps the two pads' sides, so it is kept
// only if no term — wire included — gets worse, possibly with a small shift
// that puts the swapped pads where their nets want them.
func (pl *placer) aesFold() {
	if pl.opt.NoRotate {
		return
	}
	for _, p := range pl.aesOrder() {
		if !symmetricPassive(p) {
			continue
		}
		r := normDeg(90 * math.Round(p.Rotation/90))
		if math.Abs(r-p.Rotation) > 1e-6 || r < 180 {
			continue
		}
		if !pl.aesTurn("fold", p, []float64{r - 180}, 0, 10) {
			pl.aesReject("fold")
		}
	}
}

// aesClasses groups parts by the P3/P1 class (kind, pad count, footprint
// size) and side; mechanical parts are left out.
func (pl *placer) aesClasses() (map[string][]*aPart, []string) {
	by := map[string][]*aPart{}
	for _, ap := range aesParts(pl.b, pl.c) {
		if ap.mech {
			continue
		}
		k := fmt.Sprintf("%s|%d", ap.class, ap.p.Side)
		by[k] = append(by[k], ap)
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return by, keys
}

// aesOrient turns the minority of every class of ≥3 parts to the class
// majority. Symmetric passives vote on the axis (mod 180) and each one keeps
// the direction its pad nets want; polar parts vote on the direction (mod
// 360) and turn to it only where the circuit allows (current direction: a
// reversed diode or LED fails the electrical tiers), else onto the majority
// axis.
func (pl *placer) aesOrient(cap float64) {
	if pl.opt.NoRotate {
		return
	}
	start := pl.aes.budget
	by, keys := pl.aesClasses()
	for _, k := range keys {
		ps := by[k]
		if len(ps) < 3 {
			continue
		}
		cnt := map[float64]int{}
		for _, a := range ps {
			cnt[rotKey(a)]++
		}
		major, best := 0.0, -1
		rs := make([]float64, 0, len(cnt))
		for r := range cnt {
			rs = append(rs, r)
		}
		sort.Float64s(rs)
		for _, r := range rs {
			if cnt[r] > best {
				major, best = r, cnt[r]
			}
		}
		if best*2 <= len(ps) && len(rs) > 1 {
			// No majority (a tie): nothing to unify toward.
			continue
		}
		for _, a := range ps {
			if pl.aes.excluded[a.p] || rotKey(a) == major {
				continue
			}
			allow := math.Min(pl.aes.budget, cap-(start-pl.aes.budget))
			rots := []float64{major, major + 180}
			if a.polar && len(a.p.Pads) >= 3 {
				rots = []float64{major}
			}
			if !pl.aesTurn("orient", a.p, rots, allow, 25) {
				pl.aesReject("orient")
			}
		}
	}
}

// aesAxis reads / writes the centre coordinate along axis 0 (x) or 1 (y).
func aesAxis(p Point, axis int) float64 {
	if axis == 0 {
		return p.X
	}
	return p.Y
}

func aesWithAxis(p Point, axis int, v float64) Point {
	if axis == 0 {
		p.X = v
	} else {
		p.Y = v
	}
	return p
}

// aesLine is the line through ref's centre along axis, nudged (≤ grid/2)
// so that ref's anchor lands on the grid: members with the same
// anchor-to-centre offset then keep the line through the grid snap.
func (pl *placer) aesLine(ref *Part, axis int) float64 {
	c := aesAxis(ref.Body().Center(), axis)
	if pl.aes.excluded[ref] {
		return c
	}
	off := c - aesAxis(ref.Pos, axis)
	g := pl.aes.grid
	return math.Round((c-off)/g)*g + off
}

// aesShift is the move that puts p's centre coordinate along axis at v.
func aesShift(p *Part, axis int, v float64) aesMove {
	d := v - aesAxis(p.Body().Center(), axis)
	return aesMove{p, aesWithAxis(p.Pos, axis, aesAxis(p.Pos, axis)+d), p.Rotation}
}

// aesAlign pulls near-aligned parts of a class onto one row / column
// (P1): 1-D clusters of centre coordinates within the profile's near-miss
// band among P1 neighbours (≤ 3 body sizes apart), snapped to the line that
// moves them least — a fixed member's line first, else the median member's.
func (pl *placer) aesAlign(cap float64) {
	st := pl.aes
	tol, near := st.prof.AlignTolMil, st.prof.NearMissTolMil
	start := st.budget
	for axis := 0; axis < 2; axis++ {
		by, keys := pl.aesClasses()
		for _, k := range keys {
			ps := by[k]
			if len(ps) < 2 {
				continue
			}
			par := make([]int, len(ps))
			for i := range par {
				par[i] = i
			}
			var find func(int) int
			find = func(i int) int {
				for par[i] != i {
					par[i] = par[par[i]]
					i = par[i]
				}
				return i
			}
			for i := range ps {
				for j := i + 1; j < len(ps); j++ {
					a, b := ps[i], ps[j]
					if math.Abs(aesAxis(a.c, axis)-aesAxis(b.c, axis)) <= near && a.c.Dist(b.c) <= 3*math.Max(a.size, b.size) {
						par[find(i)] = find(j)
					}
				}
			}
			clusters := map[int][]*aPart{}
			var roots []int
			for i := range ps {
				r := find(i)
				if _, ok := clusters[r]; !ok {
					roots = append(roots, r)
				}
				clusters[r] = append(clusters[r], ps[i])
			}
			sort.Ints(roots)
			for _, r := range roots {
				cl := clusters[r]
				if len(cl) < 2 {
					continue
				}
				lo, hi := math.Inf(1), math.Inf(-1)
				for _, a := range cl {
					v := aesAxis(a.p.Body().Center(), axis)
					lo, hi = math.Min(lo, v), math.Max(hi, v)
				}
				if hi-lo <= tol {
					continue
				}
				// Candidate reference members: fixed first, then by distance
				// to the cluster median.
				refs := append([]*aPart(nil), cl...)
				vals := make([]float64, len(cl))
				for i, a := range cl {
					vals[i] = aesAxis(a.p.Body().Center(), axis)
				}
				sort.Float64s(vals)
				med := vals[len(vals)/2]
				sort.SliceStable(refs, func(i, j int) bool {
					fi, fj := st.excluded[refs[i].p], st.excluded[refs[j].p]
					if fi != fj {
						return fi
					}
					di := math.Abs(aesAxis(refs[i].p.Body().Center(), axis) - med)
					dj := math.Abs(aesAxis(refs[j].p.Body().Center(), axis) - med)
					if math.Abs(di-dj) > 1e-9 {
						return di < dj
					}
					return refs[i].p.Ref < refs[j].p.Ref
				})
				done := false
				for _, ref := range refs {
					line := pl.aesLine(ref.p, axis)
					var mv []aesMove
					ok := true
					for _, a := range cl {
						if math.Abs(aesAxis(a.p.Body().Center(), axis)-line) <= 1e-6 {
							continue
						}
						if st.excluded[a.p] {
							ok = false
							break
						}
						mv = append(mv, aesShift(a.p, axis, line))
					}
					if !ok || len(mv) == 0 {
						continue
					}
					allow := math.Min(st.budget, cap-(start-st.budget))
					if pl.aesTryShove("align", mv, allow, nil) {
						done = true
						break
					}
				}
				if done {
					continue
				}
				// The whole cluster does not fit one line: pair each movable
				// member with its nearest member instead.
				any := false
				for _, a := range cl {
					if st.excluded[a.p] {
						continue
					}
					va := aesAxis(a.p.Body().Center(), axis)
					var bestQ *aPart
					bd := math.Inf(1)
					for _, q := range cl {
						if q == a {
							continue
						}
						if d := math.Abs(aesAxis(q.p.Body().Center(), axis) - va); d < bd-1e-9 {
							bd, bestQ = d, q
						}
					}
					if bestQ == nil || bd <= tol {
						continue
					}
					line := aesAxis(bestQ.p.Body().Center(), axis)
					allow := math.Min(st.budget, cap-(start-st.budget))
					if pl.aesTryShove("align", []aesMove{aesShift(a.p, axis, line)}, allow, nil) {
						any = true
					}
				}
				if !any {
					pl.aesReject("align")
				}
			}
		}
	}
}

// aesPitch evens the steps of every row / column of ≥3 aligned same-class
// parts (P2): first and last stay (the last moves ≤ half a grid per step so
// the pitch is a grid multiple), the inner members move along the row.
func (pl *placer) aesPitch(cap float64) {
	st := pl.aes
	start := st.budget
	by := map[string][]*aPart{}
	var classes []string
	{
		b2, keys := pl.aesClasses()
		for _, k := range keys {
			by[k] = b2[k]
			classes = append(classes, k)
		}
	}
	for _, row := range aesRows(by, classes, st.prof.AlignTolMil) {
		if row.cv <= 0.02 {
			continue
		}
		axis := 0
		if row.axis == "column" {
			axis = 1
		}
		var ps []*Part
		for _, r := range row.refs {
			ps = append(ps, pl.b.Part(r))
		}
		n := len(ps)
		first := aesAxis(ps[0].Body().Center(), axis)
		last := aesAxis(ps[n-1].Body().Center(), axis)
		pitch := (last - first) / float64(n-1)
		g := st.grid
		if gp := math.Round(pitch/g) * g; gp > 0 && !st.excluded[ps[n-1]] {
			pitch = gp
		}
		var mv []aesMove
		ok := true
		for i := 1; i < n; i++ {
			want := first + float64(i)*pitch
			if math.Abs(aesAxis(ps[i].Body().Center(), axis)-want) <= 1e-6 {
				continue
			}
			if st.excluded[ps[i]] {
				ok = false
				break
			}
			mv = append(mv, aesShift(ps[i], axis, want))
		}
		if !ok || len(mv) == 0 {
			continue
		}
		allow := math.Min(st.budget, cap-(start-st.budget))
		if !pl.aesTryShove("pitch", mv, allow, nil) {
			pl.aesReject("pitch")
		}
	}
}

// aesGrid snaps every origin to the grid (P9). A part packed flush against
// a neighbour cannot move alone: the neighbours it would overlap are pushed
// out of the way onto the grid too (four levels, ≤ 8 parts), and the whole
// set is judged as one transaction.
func (pl *placer) aesGrid() {
	st := pl.aes
	// The profile's grid first (precision: 25 mil), then the 5 mil audit
	// grid for what the coarse snap could not place.
	grids := []float64{st.grid}
	if st.grid > 5 {
		grids = append(grids, 5)
	}
	for _, g := range grids {
		st.snapG = g
		pl.aesGridAt(g)
	}
}

func (pl *placer) aesGridAt(g float64) {
	st := pl.aes
	// Repeated sub-circuits first: each instance moves rigidly, so a copy
	// stays a copy (P4).
	for _, sg := range st.sym {
		if sg.Kind != "block" && sg.Kind != "channel" {
			continue
		}
		for _, inst := range sg.Instances {
			var ps []*Part
			off, free := false, true
			for _, r := range inst {
				p := pl.b.Part(r)
				if p == nil || st.excluded[p] {
					free = false
					break
				}
				ps = append(ps, p)
				if !onGrid(p.Pos.X, g, 1e-3) || !onGrid(p.Pos.Y, g, 1e-3) {
					off = true
				}
			}
			if !free || !off {
				continue
			}
			pl.aesRigidSnap("grid-group", ps)
		}
	}
	// Arrays, as rigid sets: one shift for every member keeps the pitch
	// (P2); members land on the grid when the pitch is a multiple.
	for _, r := range st.rows {
		off, free := false, true
		for _, p := range r.parts {
			if st.excluded[p] {
				free = false
			}
			if !onGrid(p.Pos.X, g, 1e-3) || !onGrid(p.Pos.Y, g, 1e-3) {
				off = true
			}
		}
		if !off || !free {
			continue
		}
		pl.aesRigidSnap("grid-row", r.parts)
	}
	for _, p := range pl.aesOrder() {
		if onGrid(p.Pos.X, g, 1e-3) && onGrid(p.Pos.Y, g, 1e-3) {
			continue
		}
		// The grid points within 1.5 steps, nearest first: the cell's four
		// corners, then the ring around it.
		var cands []Point
		fx, fy := math.Floor(p.Pos.X/g)*g, math.Floor(p.Pos.Y/g)*g
		for dy := -1; dy <= 2; dy++ {
			for dx := -1; dx <= 2; dx++ {
				c := Point{fx + float64(dx)*g, fy + float64(dy)*g}
				if c.Dist(p.Pos) <= 1.5*g*math.Sqrt2 {
					cands = append(cands, c)
				}
			}
		}
		sort.SliceStable(cands, func(i, j int) bool { return cands[i].Dist(p.Pos) < cands[j].Dist(p.Pos)-1e-9 })
		ok := false
		tried := map[Point]bool{}
		for _, c := range cands {
			if tried[c] {
				continue
			}
			tried[c] = true
			allow := math.Max(0, st.budget)
			if pl.aesTry("grid", []aesMove{{p, c, p.Rotation}}, allow, nil) {
				ok = true
				break
			}
			if mv := pl.aesShove(p, c, p.Rotation, true); len(mv) > 1 && pl.aesTry("grid+shove", mv, allow, nil) {
				ok = true
				break
			}
		}
		if !ok {
			pl.aesReject("grid")
		}
	}
}

// aesRigidSnap shifts a set of parts by one vector — each member's own
// snap vector in turn — so the set keeps its shape and as many members as
// its geometry allows land on the grid.
func (pl *placer) aesRigidSnap(kind string, ps []*Part) bool {
	st := pl.aes
	g := st.snapG
	tried := map[Point]bool{}
	for _, ref := range ps {
		d := Point{math.Round(ref.Pos.X/g)*g - ref.Pos.X, math.Round(ref.Pos.Y/g)*g - ref.Pos.Y}
		k := Point{math.Round(d.X * 1000), math.Round(d.Y * 1000)}
		if tried[k] {
			continue
		}
		tried[k] = true
		var mv []aesMove
		for _, p := range ps {
			mv = append(mv, aesMove{p, p.Pos.Add(d), p.Rotation})
		}
		if pl.aesTryShove(kind, mv, math.Max(0, st.budget), nil) {
			return true
		}
	}
	return false
}

// aesShove is p at `at` plus the grid-aligned pushes that clear the
// courtyards it would overlap (nil when an excluded part is in the way).
func (pl *placer) aesShove(p *Part, at Point, rot float64, snap bool) []aesMove {
	return pl.aesShoveSet([]aesMove{{p, at, rot}}, snap)
}

// aesShoveSet is mv plus the grid-aligned pushes that clear the courtyards
// the moved parts would overlap: four levels, ≤ len(mv)+8 parts; nil when
// an excluded part is in the way. Nothing stays moved.
func (pl *placer) aesShoveSet(base []aesMove, snap bool) []aesMove {
	st := pl.aes
	g := st.snapG
	mv := append([]aesMove(nil), base...)
	in := map[*Part]bool{}
	var queue []*Part
	for _, m := range base {
		in[m.p] = true
		queue = append(queue, m.p)
	}
	limit := len(base) + 8
	var applied []aesMove
	apply := func(m aesMove) {
		applied = append(applied, aesMove{m.p, m.p.Pos, m.p.Rotation})
		pl.aesApply(m.p, m.pos, m.rot)
	}
	defer func() {
		for i := len(applied) - 1; i >= 0; i-- {
			pl.aesApply(applied[i].p, applied[i].pos, applied[i].rot)
		}
	}()
	for _, m := range base {
		apply(m)
	}
	for depth := 0; depth < 4 && len(queue) > 0; depth++ {
		var next []*Part
		for _, a := range queue {
			var hits []*Part
			seen := map[*Part]bool{}
			pl.forBuckets(pl.boxes[a], func(q *Part) {
				if q == a || in[q] || seen[q] || !collide(a, q) {
					return
				}
				seen[q] = true
				if pl.pairOverlap(a, q) > 0 {
					hits = append(hits, q)
				}
			})
			sort.Slice(hits, func(i, j int) bool { return st.idx[hits[i]] < st.idx[hits[j]] })
			for _, q := range hits {
				if st.excluded[q] {
					if aesDebug {
						fmt.Fprintf(os.Stderr, "AESSHOVE blocked by %s (moving %s)\n", q.Ref, a.Ref)
					}
					return nil
				}
				ba, bq := pl.boxes[a], pl.boxes[q]
				if pl.intimate[[2]*Part{a, q}] {
					ba, bq = a.Body().Expand(intimateGap/2), q.Body().Expand(intimateGap/2)
				}
				penX := math.Min(ba.MaxX-bq.MinX, bq.MaxX-ba.MinX)
				penY := math.Min(ba.MaxY-bq.MinY, bq.MaxY-ba.MinY)
				pos := q.Pos
				if penX <= penY {
					if bq.Center().X >= ba.Center().X {
						pos.X = math.Ceil((pos.X+penX)/g-1e-9) * g
					} else {
						pos.X = math.Floor((pos.X-penX)/g+1e-9) * g
					}
					pos.Y = math.Round(pos.Y/g) * g
				} else {
					if bq.Center().Y >= ba.Center().Y {
						pos.Y = math.Ceil((pos.Y+penY)/g-1e-9) * g
					} else {
						pos.Y = math.Floor((pos.Y-penY)/g+1e-9) * g
					}
					pos.X = math.Round(pos.X/g) * g
				}
				m := aesMove{q, pos, q.Rotation}
				mv = append(mv, m)
				in[q] = true
				apply(m)
				next = append(next, q)
				if len(mv) > limit {
					return nil
				}
			}
		}
		queue = next
	}
	return mv
}

// ---- symmetry -----------------------------------------------------------------

// symTarget is where a moved instance part goes.
type symTarget struct {
	p    *Part
	at   Point // body centre
	rot  float64
	from *Part
}

// aesSymNodes builds the fit nodes of an instance.
func (pl *placer) aesSymNodes(refs []string, labels map[string]string) []symNode {
	var out []symNode
	for _, r := range refs {
		if p := pl.b.Part(r); p != nil {
			out = append(out, symNodeOf(p, labels[r]))
		}
	}
	return out
}

// symCost is the soft symmetry term: the fitted error of the instance pair
// (mean mirror distance / span + 0.2 × rotation-mismatch share, symmetry.go)
// over the transforms the placer can build.
func (pl *placer) symCost(a, b []string, labels map[string]string, allowed []string) SymmetryPair {
	return fitSymmetry(pl.aesSymNodes(a, labels), pl.aesSymNodes(b, labels), allowed)
}

// symTargets maps every part of instance `mov` to the pose the transform of
// its matched part in `ref` asks for. Mirror axes and point centres are
// snapped to half the 5 mil grid, translations to the grid.
func (pl *placer) symTargets(ref, mov []string, labels map[string]string, allowed []string) ([]symTarget, SymmetryPair) {
	sp := pl.symCost(ref, mov, labels, allowed)
	g := 5.0 // the audit grid: a coarse target grid would throw the copy off by half its step
	half := func(v float64) float64 { return math.Round(v/(g/2)) * (g / 2) }
	var prm Point
	switch sp.Axis.Type {
	case "vertical":
		prm = Point{half(sp.Axis.Coord), 0}
	case "horizontal":
		prm = Point{0, half(sp.Axis.Coord)}
	case "point":
		if sp.Axis.Centre != nil {
			prm = Point{half(sp.Axis.Centre.X), half(sp.Axis.Centre.Y)}
		}
	default:
		if sp.Axis.Offset != nil {
			prm = Point{math.Round(sp.Axis.Offset.X/g) * g, math.Round(sp.Axis.Offset.Y/g) * g}
		}
	}
	var out []symTarget
	for _, m := range sp.Match {
		a, b := pl.b.Part(m[0]), pl.b.Part(m[1])
		if a == nil || b == nil {
			continue
		}
		out = append(out, symTarget{p: b, from: a, at: symMirror(sp.Axis.Type, a.Body().Center(), prm), rot: symRot(sp.Axis.Type, a.Rotation)})
	}
	return out, sp
}

// aesSymmetry lays out the isomorphic groups DetectSymmetry finds as true
// copies: for each consecutive instance pair the transform the placer can
// build (a translation or a 180° turn for anything with a ≥3-pin package —
// a footprint cannot be mirrored — and also the two mirror axes for groups
// of two-pin parts) is fitted, one instance is kept and the other is moved
// onto the transformed poses as a rigid set (rotations included: a 180°
// point copy turns every part 180°), shifted by the smallest lattice offset
// that every tier accepts. Where no rigid copy is accepted, a soft pass
// moves parts one at a time toward their targets, each kept only when the
// symmetry cost (symCost) falls and no tier gets worse.
func (pl *placer) aesSymmetry(cap float64) {
	st := pl.aes
	start := st.budget
	radius := 30.0
	if st.prof.SymmetryRequired {
		radius = 60
	}
	groups := DetectSymmetry(pl.b, pl.an, pl.c)
	for _, g := range groups {
		if g.Kind != "block" && g.Kind != "channel" {
			continue
		}
		mirrorable := true
		for _, inst := range g.Instances {
			for _, r := range inst {
				if p := pl.b.Part(r); p == nil || len(p.Pads) > 2 {
					mirrorable = false
				}
			}
		}
		allowed := []string{"translate", "point"}
		if mirrorable {
			allowed = []string{"vertical", "horizontal", "point", "translate"}
		}
		for i := 0; i+1 < len(g.Instances); i++ {
			A, B := g.Instances[i], g.Instances[i+1]
			before := pl.symCost(A, B, g.labels, allowed)
			mv := AesSymMove{Kind: g.Kind, Signature: g.Signature, ErrorBefore: round3(before.Error), Transform: before.Axis.Type}
			if before.Error <= 0.02 {
				mv.ErrorAfter, mv.Result = mv.ErrorBefore, "already symmetric"
				mv.Reference, mv.Moved = strings.Join(A, "+"), strings.Join(B, "+")
				st.rep.Symmetry = append(st.rep.Symmetry, mv)
				continue
			}
			dirs := [][2][]string{{A, B}, {B, A}}
			if len(g.Instances) > 2 {
				dirs = dirs[:1] // a chain of copies: each copies its predecessor
			}
			result := ""
			for _, d := range dirs {
				ref, mov := d[0], d[1]
				if !pl.aesAllFree(mov) {
					continue
				}
				mv.Reference, mv.Moved = strings.Join(ref, "+"), strings.Join(mov, "+")
				if pl.aesSymSeed(ref, mov, g.labels, allowed, radius, math.Min(st.budget, cap-(start-st.budget))) {
					result = "copied"
					break
				}
			}
			if result == "" {
				total := 0
				for _, d := range dirs {
					ref, mov := d[0], d[1]
					if !pl.aesAllFree(mov) {
						continue
					}
					mv.Reference, mv.Moved = strings.Join(ref, "+"), strings.Join(mov, "+")
					// Both instances may give way: each keeps the moves that
					// bring the pair closer to a copy.
					if n := pl.aesSymRefine(ref, mov, g.labels, allowed, func() float64 { return math.Min(st.budget, cap-(start-st.budget)) }); n > 0 {
						total += n
					}
				}
				if total > 0 {
					result = fmt.Sprintf("%d part move(s) toward the copy", total)
				}
			}
			if mv.Moved == "" {
				mv.Reference, mv.Moved = strings.Join(A, "+"), strings.Join(B, "+")
				result = "not movable (fixed, bridge or pair-corridor part)"
			}
			if result == "" {
				result = "kept: no copy passes the electrical/safety tiers (" + st.lastWhy + ")"
				pl.aesReject("symmetry")
			}
			after := pl.symCost(A, B, g.labels, allowed)
			mv.ErrorAfter, mv.Transform, mv.Result = round3(after.Error), after.Axis.Type, result
			st.rep.Symmetry = append(st.rep.Symmetry, mv)
		}
	}
}

func (pl *placer) aesAllFree(refs []string) bool {
	for _, r := range refs {
		p := pl.b.Part(r)
		if p == nil || pl.aes.excluded[p] {
			return false
		}
	}
	return true
}

// aesSymSeed moves instance mov rigidly onto the transform of ref.
func (pl *placer) aesSymSeed(ref, mov []string, labels map[string]string, allowed []string, radius, allow float64) bool {
	ts, sp := pl.symTargets(ref, mov, labels, allowed)
	if len(ts) != len(mov) {
		return false
	}
	before := pl.symCost(ref, mov, labels, allowed).Error
	veto := func() string {
		if pl.symCost(ref, mov, labels, allowed).Error >= before-0.01 {
			return "no symmetry gain"
		}
		return ""
	}
	_ = sp
	for i, d := range aesOffsets(radius, 5) {
		var mv []aesMove
		for _, t := range ts {
			off := pl.offsetAt(t.p, t.rot)
			mv = append(mv, aesMove{t.p, t.at.Add(d).Sub(off), t.rot})
		}
		if pl.aesTry("symmetry", mv, allow, veto) {
			return true
		}
		if i < 9 && pl.aes.lastWhy == "safety/legality" {
			if sm := pl.aesShoveSet(mv, false); len(sm) > len(mv) && pl.aesTry("symmetry+shove", sm, allow, veto) {
				return true
			}
		}
	}
	return false
}

// aesSymRefine is the soft symmetry pass: parts of mov, worst first, move
// toward (or onto) their target poses one at a time; a move is kept when
// symCost falls by ≥ 0.005 and every tier accepts it.
func (pl *placer) aesSymRefine(ref, mov []string, labels map[string]string, allowed []string, allow func() float64) int {
	moved := 0
	for round := 0; round < 3; round++ {
		n := 0
		// Targets follow the refitted transform: every accepted move shifts it.
		ts, _ := pl.symTargets(ref, mov, labels, allowed)
		sort.SliceStable(ts, func(i, j int) bool {
			return ts[i].p.Body().Center().Dist(ts[i].at) > ts[j].p.Body().Center().Dist(ts[j].at)
		})
		for _, t := range ts {
			cur := pl.symCost(ref, mov, labels, allowed).Error
			veto := func() string {
				if pl.symCost(ref, mov, labels, allowed).Error > cur-0.002 {
					return "no symmetry gain"
				}
				return ""
			}
			c := t.p.Body().Center()
			rots := []float64{t.rot}
			if normDeg(t.rot) != t.p.Rotation {
				rots = append(rots, t.p.Rotation)
			}
			var cands []aesMove
			for _, frac := range []float64{1, 0.75, 0.5, 0.25} {
				for _, rot := range rots {
					off := pl.offsetAt(t.p, rot)
					at := c.Add(t.at.Sub(c).Scale(frac))
					ds := []Point{{}}
					if frac == 1 {
						ds = aesOffsets(10, 5)
					}
					for _, d := range ds {
						cands = append(cands, aesMove{t.p, at.Add(d).Sub(off), rot})
					}
				}
			}
			for i, m := range cands {
				if pl.aesTry("symmetry", []aesMove{m}, allow(), veto) {
					n++
					break
				}
				if i < 6 && pl.aes.lastWhy == "safety/legality" {
					if sm := pl.aesShoveSet([]aesMove{m}, false); len(sm) > 1 && pl.aesTry("symmetry+shove", sm, allow(), veto) {
						n++
						break
					}
				}
			}
		}
		moved += n
		if n == 0 {
			break
		}
	}
	return moved
}
