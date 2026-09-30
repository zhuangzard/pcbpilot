package pcbauto

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Completing the via arrays of current-carrying nets.
//
// viaArray places a transition's k−1 extra vias round the single via the
// search chose; the search itself never asked whether k vias fit there. A
// transition hemmed in by other copper or pads (HV flyback VOUT_RAW,
// 2026-09-30: one side of the transition blocked, the array got 2 of the
// intent's 3 vias → pcb check via-current ERROR, margin −10.9 %) was only a
// report note. Here every short transition of a net with a sized current
// gets, in order, until the array is complete and the exact DRC is no worse:
//
//  1. a larger drill from the JLC ladder, when fewer vias of it carry the
//     current (same route, same transition);
//  2. another transition location: first the layer change slid along the
//     same route to a column with room for the whole array (only the
//     stretch between the old and the new site changes layer), then the
//     connection searched again with the short sites banned for vias (up to
//     viaFixMoves times);
//  3. staying on the layer: the route is searched again without vias;
//  4. the larger drill again, with a new route.
//
// A transition none of these completes is reported as a ViaShortfall (plan
// notes, feedback.json) with every alternative tried and why it failed —
// never silently.

// ViaShortfall is a layer transition of a current-carrying net whose via
// array stayed short of its sizing after every alternative.
type ViaShortfall struct {
	Net      string   `json:"net"`
	At       []Point  `json:"at"`
	Planned  int      `json:"planned"` // vias per transition (sizing)
	Short    int      `json:"short"`   // vias missing over the net's transitions
	DrillMil float64  `json:"drillMil"`
	DiaMil   float64  `json:"diaMil"`
	CurrentA float64  `json:"currentA"`
	Tried    []string `json:"tried"`
	Reason   string   `json:"reason"`
}

const (
	viaFixMoves   = 2               // transition relocations tried
	viaFixAttempt = 5 * time.Second // search budget of one re-route
	viaFixBudget  = 30 * time.Second
)

// viaRated reports a net whose transitions carry a sized current (intent or
// simulated currents): its via arrays are electrical requirements.
func (r *router) viaRated(n *rnet) bool {
	return r.an.rated && n.plan != nil && n.plan.Via != nil && n.plan.Via.CurrentA > 0 && n.viaCount() > 1
}

// completeViaArrays runs the alternatives on every short, rated net.
func (r *router) completeViaArrays(outs map[*rnet]*netOut, collect func() ([]Track, []Via)) {
	stop := r.now().Add(viaFixBudget)
	for _, n := range r.nets {
		if n.viaShort == 0 || len(n.paths) == 0 || !r.viaRated(n) || n.viaFix == viaFixFailed {
			continue
		}
		if r.now().After(stop) || r.now().After(r.deadline) {
			n.viaTried = append(n.viaTried, "no time left in the routing budget for alternatives")
			continue
		}
		r.fixViaArray(n, outs, collect)
	}
}

// drcOf counts the exact-DRC violations n is a party of.
func (r *router) drcOf(n *rnet, collect func() ([]Track, []Via)) int {
	ts, vs := collect()
	c := 0
	for _, v := range CheckDRC(r.b, r.an, r.st, ts, vs).Violations {
		if v.NetA == n.name || v.NetB == n.name {
			c++
		}
	}
	return c
}

// ladderUp returns the ladder sizes with a larger drill than n's via, each
// with the count that carries n's current, keeping only those needing fewer
// vias than the plan.
func (r *router) ladderUp(n *rnet) []ViaPlan {
	vp := n.plan.Via
	q := ViaSizing{CurrentA: vp.CurrentA, TempRiseC: r.an.TempRiseC, PlatingMil: vp.PlatingMil, MarginPct: vp.RequiredMarginPct,
		LengthMil: vp.LengthMil, Clearance: r.b.Rules.Clearance, HoleGap: r.b.Rules.HoleGap}
	if q.MarginPct == 0 {
		q.MarginPct = -1 // 0 declared: no margin (ViaSizing reads 0 as the default)
	}
	var out []ViaPlan
	for _, v := range JLCViaLadder() {
		if v.DrillMil <= n.viaDrill+1e-6 {
			continue
		}
		per := ViaAmpacity(v.DrillMil, vp.PlatingMil, r.an.TempRiseC)
		if per <= 0 {
			continue
		}
		k := max(1, int(math.Ceil(vp.CurrentA*(1+vp.RequiredMarginPct/100)/per-1e-9)))
		if k >= n.viaCount() {
			continue
		}
		out = append(out, EvalVias(q, v, k))
	}
	return out
}

// fixViaArray tries the alternatives on n; the first that completes every
// transition without a new DRC violation is kept, else n is restored.
func (r *router) fixViaArray(n *rnet, outs map[*rnet]*netOut, collect func() ([]Track, []Via)) {
	base := r.drcOf(n, collect)
	saved, savedOut := r.saveNet(n), outs[n]
	savedDrill, savedDia, savedR, savedK := n.viaDrill, n.viaDia, n.viaR, n.viaK
	short0, shortAt0 := n.viaShort, append([]Point(nil), n.shortAt...)
	bans := append([]Point(nil), shortAt0...)
	nBans := -1
	restore := func() {
		n.viaDrill, n.viaDia, n.viaR, n.viaK = savedDrill, savedDia, savedR, savedK
		n.viaBan, n.noVias = nil, false
		r.clearNet(n)
		r.restoreNet(n, saved)
		outs[n] = savedOut
		n.viaShort, n.shortAt = short0, append([]Point(nil), shortAt0...)
	}
	accept := func() (bool, string) {
		switch {
		case len(n.paths) == 0 || len(n.failed) > len(saved.failed):
			return false, "the net no longer connects"
		case n.viaShort > 0:
			return false, sprintf("still %d via(s) short", n.viaShort)
		}
		if d := r.drcOf(n, collect); d > base {
			return false, sprintf("%d new DRC violation(s)", d-base)
		}
		return true, ""
	}
	reroute := func() {
		deadline, strict := r.deadline, r.strict
		r.deadline, r.strict, n.viaFixing = minTime(r.deadline, r.now().Add(viaFixAttempt)), true, true
		r.rerouteTransitions(n, shortAt0)
		r.deadline, r.strict, n.viaFixing = deadline, strict, false
		outs[n] = r.emitNet(n)
	}
	setSize := func(p ViaPlan) {
		n.viaDrill, n.viaDia, n.viaK = p.DrillMil, p.DiaMil, p.Count
		n.viaR = p.DiaMil/2 + n.share
	}
	try := func(label string, run func()) bool {
		run()
		ok, why := accept()
		if ok {
			n.viaFix = viaFixKept
			n.viaTried = append(n.viaTried, label+": kept")
			r.fixNotes = append(r.fixNotes, sprintf("via array: %s completed by %s (%d × %.1f/%.1f mil per transition)", n.name, label, n.viaCount(), n.viaDrill, n.viaDia))
			return true
		}
		n.viaTried = append(n.viaTried, label+": "+why)
		restore()
		return false
	}
	// 1. larger drill, same route.
	for _, p := range r.ladderUp(n) {
		if try(sprintf("larger drill %d × %.1f/%.1f mil in place", p.Count, p.DrillMil, p.DiaMil), func() {
			setSize(p)
			outs[n] = r.emitNet(n)
		}) {
			return
		}
	}
	// 2a. the same route with its layer change slid along it to a spot
	// with room for the whole array.
	if try("transition slid along the route", func() {
		r.slideTransitions(n, shortAt0)
		outs[n] = r.emitNet(n)
	}) {
		return
	}
	// 2b. another transition location (a round that found no new short
	// site has nothing new to ban: stop).
	for k := 0; k < viaFixMoves; k++ {
		if k > 0 && len(bans) == nBans {
			break
		}
		nBans = len(bans)
		ban := append([]Point(nil), bans...)
		if try(sprintf("transition moved away from %s", fmtPoints(ban)), func() {
			n.viaBan = ban
			reroute()
			for _, q := range n.shortAt {
				if !containsPoint(bans, q) {
					bans = append(bans, q)
				}
			}
		}) {
			n.viaBan = nil
			return
		}
	}
	// 3. stay on the layer.
	if try("no layer change (single-layer route)", func() {
		n.noVias = true
		reroute()
	}) {
		n.noVias = false
		return
	}
	// 4. larger drill with a new route.
	for _, p := range r.ladderUp(n) {
		if try(sprintf("larger drill %d × %.1f/%.1f mil re-routed", p.Count, p.DrillMil, p.DiaMil), func() {
			setSize(p)
			reroute()
		}) {
			return
		}
	}
	if len(r.ladderUp(n)) == 0 {
		n.viaTried = append(n.viaTried, sprintf("larger drill: no JLC ladder size above %.1f mil carries %.2f A with fewer than %d vias", n.viaDrill, n.plan.Via.CurrentA, n.viaCount()))
	}
	n.viaFix = viaFixFailed
}

// Via-array completion state of a net.
const (
	viaFixNone = iota
	viaFixKept
	viaFixFailed
)

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func fmtPoints(ps []Point) string {
	s := ""
	for i, p := range ps {
		if i > 0 {
			s += " "
		}
		s += fmt.Sprintf("(%.0f,%.0f)", p.X, p.Y)
	}
	return s
}

// viaBanned reports a via site of n within one via-array span of a
// transition that could not hold its array.
func (r *router) viaBanned(n *rnet, c Point) bool {
	if n.noVias {
		return true
	}
	if len(n.viaBan) == 0 {
		return false
	}
	rad := math.Max(n.width, viaTransitionSpanMil)/2 + viaPitch(ViaSize{n.viaDrill, n.viaDia}, r.b.Rules.Clearance, r.b.Rules.HoleGap)
	for _, b := range n.viaBan {
		if c.Dist(b) < rad {
			return true
		}
	}
	return false
}

// viaShortfalls lists the rated nets still short after the alternatives.
func (r *router) viaShortfalls() []ViaShortfall {
	var out []ViaShortfall
	for _, n := range r.nets {
		if n.viaShort == 0 || len(n.paths) == 0 || !r.viaRated(n) {
			continue
		}
		at := append([]Point(nil), n.shortAt...)
		sort.Slice(at, func(i, j int) bool { return at[i].X < at[j].X || at[i].X == at[j].X && at[i].Y < at[j].Y })
		for i := range at {
			at[i] = Point{round2(at[i].X), round2(at[i].Y)}
		}
		s := ViaShortfall{Net: n.name, At: at, Planned: n.viaCount(), Short: n.viaShort, DrillMil: n.viaDrill, DiaMil: n.viaDia,
			CurrentA: n.plan.Via.CurrentA, Tried: append([]string(nil), n.viaTried...)}
		s.Reason = sprintf("%d via(s) of %d × %.1f/%.1f mil per transition found no site (other copper, pads or hole gaps round the transition); every alternative failed — the transition carries less than the sized %.2f A", n.viaShort, s.Planned, s.DrillMil, s.DiaMil, s.CurrentA)
		if len(s.Tried) == 0 {
			s.Reason += " (no alternative was tried)"
		}
		out = append(out, s)
	}
	return out
}

func joinSemi(xs []string) string {
	s := ""
	for i, x := range xs {
		if i > 0 {
			s += "; "
		}
		s += x
	}
	return s
}

// viaShortFeedback turns unresolved via shortfalls into feedback items: the
// transition carries less than its sizing, the router tried every
// alternative, the fix is a layout or design change.
func viaShortFeedback(res *Result) []*FeedbackItem {
	if res == nil || res.Route == nil {
		return nil
	}
	var out []*FeedbackItem
	for _, s := range res.Route.ViaShortfalls {
		it := &FeedbackItem{Kind: FBViaCurrent, Severity: "high", Confidence: 0.7, Applyable: false,
			Title: fmt.Sprintf("%s 换层过孔阵列不足：按 %.2f A 需每处 %d × %.1f/%.1f mil，缺 %d 个", s.Net, s.CurrentA, s.Planned, s.DrillMil, s.DiaMil, s.Short),
			Evidence: FeedbackEvidence{Nets: []string{s.Net}, Metrics: map[string]float64{"currentA": s.CurrentA, "planned": float64(s.Planned), "short": float64(s.Short)},
				Detail: append([]string{s.Reason}, s.Tried...)},
			Proposal: FeedbackProposal{Summary: "布线器已尝试更大钻孔、换层位置与不换层均未成功：需要布局/设计改动",
				Actions: []string{"在换层处腾出放置过孔阵列的空间（挪开相邻器件/走线），或让该路径整段留在一层",
					"改用铺铜/平面承载该电流（过孔阵列随铺铜分散）",
					"复核 intent 的电流与过孔规格（pcb check --intent 的 via-current 给出所需数量）"}},
			Caveats: []string{"未解决前 pcb check via-current 会报 ERROR；不得放宽 intent 的过孔要求来消除"}}
		for _, p := range s.At {
			it.Evidence.Points = append(it.Evidence.Points, FeedbackPoint{Label: "transition", X: math.Round(p.X), Y: math.Round(p.Y)})
		}
		out = append(out, it)
	}
	return out
}

// arrayMissFac prices each array via that has no room at a transition
// column (a step-cost multiplier on the via, ≈ 600 mil of track per missing
// via at the default via cost): the search prefers a transition where the
// whole array fits, even a few hundred mil away, and still takes a short one
// when nothing else connects (completion; the shortfall is then reported).
const arrayMissFac = 10.0

// arrayRoom counts the extra array vias that fit round column (x, y)
// against the static obstacles (other nets' pads, keepouts, board edge,
// fan-out holes), picked like viaArray: ≥ one via pitch from the transition
// and from each other, within the transition span. Memoised per net.
func (r *router) arrayRoom(n *rnet, x, y int) int {
	gr := r.gr
	col := int32(y*gr.W + x)
	// Strict searches (legalisation, the via fix) also see the other nets'
	// routed copper; those verdicts change as nets move and are not kept.
	memo := n.viaK == 0 && !r.strict
	if v, ok := n.roomMemo[col]; ok && memo {
		return int(v)
	}
	need := n.viaCount() - 1
	P := gr.center(x, y)
	pitch := viaPitch(ViaSize{n.viaDrill, n.viaDia}, r.b.Rules.Clearance, r.b.Rules.HoleGap)
	span := math.Max(n.width, viaTransitionSpanMil)/2 + pitch
	rc := int(math.Ceil(span / gr.g))
	type cand struct {
		x, y int
		c    Point
		d    float64
	}
	var cs []cand
	for dy := -rc; dy <= rc; dy++ {
		for dx := -rc; dx <= rc; dx++ {
			xx, yy := x+dx, y+dy
			if !gr.in(xx, yy) {
				continue
			}
			c := gr.center(xx, yy)
			if d := c.Dist(P); d >= pitch-1e-6 && d <= span {
				cs = append(cs, cand{xx, yy, c, d})
			}
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].d != cs[j].d {
			return cs[i].d < cs[j].d
		}
		return cs[i].y*gr.W+cs[i].x < cs[j].y*gr.W+cs[j].x
	})
	placed := []Point{P}
	got := 0
	for _, c := range cs {
		if got >= need {
			break
		}
		ok := !gr.noVia[c.y*gr.W+c.x] && !r.holeClash(c.c, n.viaDrill) && !(r.holeBlk != nil && r.holeBlk[c.y*gr.W+c.x])
		for _, q := range placed {
			if ok && q.Dist(c.c) < pitch-1e-6 {
				ok = false
			}
		}
		for l := range gr.layers {
			if ok && !r.nodeOK(n, l, c.x, c.y, n.viaR) {
				ok = false
			}
			if ok && r.strict && gr.routable[l] {
				if occ, _ := r.nodeCong(l, c.x, c.y, n.viaR); occ > 0 {
					ok = false
				}
			}
			// viaArray ties an array via off the runs back to the transition
			// with a stub of the net's width: the stub must fit too.
			if ok && gr.routable[l] && !r.segmentOK(n, l, c.c, P, n.width, true) {
				ok = false
			}
		}
		if ok {
			placed = append(placed, c.c)
			got++
		}
	}
	if memo {
		if n.roomMemo == nil {
			n.roomMemo = map[int32]int8{}
		}
		n.roomMemo[col] = int8(got)
	}
	return got
}

// rerouteTransitions re-routes only the connections of n whose layer change
// lies at one of the short transitions at (the rest of the net keeps its
// copper and its complete arrays); the whole net when none matches.
func (r *router) rerouteTransitions(n *rnet, at []Point) {
	gr := r.gr
	tol := 1.5 * gr.g
	hits := func(p rpath) bool {
		prev := -1
		for _, node := range p.nodes {
			l, x, y := gr.xy(int(node))
			if prev >= 0 && l != prev {
				c := gr.center(x, y)
				for _, q := range at {
					if c.Dist(q) <= tol {
						return true
					}
				}
			}
			prev = l
		}
		return false
	}
	var keep []rpath
	for _, p := range n.paths {
		if !hits(p) {
			keep = append(keep, p)
		}
	}
	if len(keep) == len(n.paths) || len(keep) == 0 {
		r.routeNet(n)
		return
	}
	r.applyClaims(n.claims, -1)
	n.claims, n.paths = nil, keep
	groups := r.pathGroups(n)
	if len(groups) < 2 {
		r.routeNet(n)
		return
	}
	saved := n.groups
	n.groups = groups
	r.routeNetKeep(n, true)
	n.groups = saved
}

// inNoViaKeepout reports a via pad of radius rad at c touching a no-via
// keep-out (CheckDRC's test), for vias larger than the board default.
func (r *router) inNoViaKeepout(c Point, rad float64) bool {
	for _, k := range r.b.Keepouts {
		if (k.NoVias || k.NoCopper) && (PolyContains(k.Poly, c) || PolyEdgeDist(k.Poly, c) < rad) {
			return true
		}
	}
	return false
}

func containsPoint(ps []Point, q Point) bool {
	for _, p := range ps {
		if p.Dist(q) < 1e-6 {
			return true
		}
	}
	return false
}

// viaSlideCells bounds how far (grid cells along the path) a transition may
// slide.
const viaSlideCells = 80

// cellFree reports whether n may occupy layer-cell i with nothing else
// there (strict: the via fix runs after legalisation).
func (r *router) cellFree(n *rnet, i int32) bool {
	l, x, y := r.gr.xy(int(i))
	rad := r.claimRadius(n, l, x, y)
	if rad < 0 {
		return false
	}
	occ, _ := r.nodeCong(l, x, y, rad)
	return occ == 0
}

// slideTransitions moves each layer change of n at a short site along its
// path to the nearest column where a via is legal, the whole array has room
// and the stretch between the two sites is free on the other layer. The
// claims are rebuilt; emitNet places the arrays.
func (r *router) slideTransitions(n *rnet, at []Point) {
	gr := r.gr
	tol := 1.5 * gr.g
	need := n.viaCount() - 1
	r.applyClaims(n.claims, -1)
	defer func() {
		n.claims = n.claims[:0]
		for _, p := range n.paths {
			n.claims = r.claimNodes(n, p.nodes, n.claims)
		}
		n.claims = r.minusFixed(n, dedup(n.claims))
		r.applyClaims(n.claims, +1)
	}()
	short := func(c Point) bool {
		for _, q := range at {
			if c.Dist(q) <= tol {
				return true
			}
		}
		return false
	}
	strict := r.strict
	r.strict = true
	defer func() { r.strict = strict }()
	for pi := range n.paths {
		nodes := n.paths[pi].nodes
		for t := 1; t < len(nodes); t++ {
			la, xa, ya := gr.xy(int(nodes[t-1]))
			lb, xb, yb := gr.xy(int(nodes[t]))
			if la == lb || xa != xb || ya != yb || !short(gr.center(xa, ya)) {
				continue
			}
			ok := func(x, y int) bool {
				return !math.IsInf(r.viaCostR(n, x, y, n.viaR), 1) && r.arrayRoom(n, x, y) >= need
			}
			// Earlier: nodes[j..t-1] move to layer lb; later: nodes[t..j]
			// move to layer la.
			var repl []int32
			for d := 1; d <= viaSlideCells && repl == nil; d++ {
				if j := t - 1 - d; j >= 0 {
					if lj, xj, yj := gr.xy(int(nodes[j])); lj == la && ok(xj, yj) {
						moved := make([]int32, 0, t-j)
						free := true
						for k := j; k <= t-1 && free; k++ {
							_, x, y := gr.xy(int(nodes[k]))
							c := int32(gr.idx(lb, x, y))
							free = r.cellFree(n, c)
							moved = append(moved, c)
						}
						if free {
							repl = append(append(append([]int32(nil), nodes[:j+1]...), moved...), nodes[t+1:]...)
						}
					}
				}
				if j := t + d; repl == nil && j < len(nodes) {
					if lj, xj, yj := gr.xy(int(nodes[j])); lj == lb && ok(xj, yj) {
						moved := make([]int32, 0, j-t+1)
						free := true
						for k := t; k <= j && free; k++ {
							_, x, y := gr.xy(int(nodes[k]))
							c := int32(gr.idx(la, x, y))
							free = r.cellFree(n, c)
							moved = append(moved, c)
						}
						if free {
							repl = append(append(append([]int32(nil), nodes[:t-1]...), moved...), nodes[j:]...)
						}
					}
				}
			}
			if repl != nil {
				n.paths[pi].nodes = repl
				nodes = repl
			}
		}
	}
}
