package app

// kicad_route_via_repair.go — growing a via to its class via, only as an
// explicit repair: per via, in one transaction, followed by a geometry check
// before the write and KiCad DRC after it, compared with the DRC before it. A
// via that fails keeps its size and carries its evidence. Nothing is ripped up
// and nothing loops: at most one retry without the vias DRC rejected, and a new
// violation that names no grown via drops the transaction; otherwise the board
// is untouched.

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

type viaGrow struct {
	v               routeVia
	classDia, drill int64 // nm
}

// viaRepairResult: grown via ids; evidence for every via rejected on a known
// reason; unchecked for every via whose repair could not be judged; info for
// every via that was grown.
type viaRepairResult struct {
	grown     []string
	evidence  []string
	unchecked []string
	info      []string
}

// viaGrowCandidates: the router vias shrinking cannot bring to their class via.
func viaGrowCandidates(vias []routeVia, old map[string]bool, eff func(string) (int64, int64, bool)) []viaGrow {
	var out []viaGrow
	for _, v := range vias {
		if old[v.ID] {
			continue
		}
		cd, ch, ok := eff(v.Net)
		if !ok || cd <= 0 || ch <= 0 || ch >= cd {
			continue
		}
		if viaNotReachable(int64(math.Round(v.Dia*nmPerMil)), int64(math.Round(v.Drill*nmPerMil)), cd, ch) {
			out = append(out, viaGrow{v: v, classDia: cd, drill: ch})
		}
	}
	return out
}

func (g viaGrow) describe() string {
	return fmt.Sprintf("%s via %s: router via %.2f/%.2f mil cannot be shrunk to the class via %.2f/%.2f mil", g.v.Net, short12(g.v.ID), g.v.Dia, g.v.Drill, float64(g.classDia)/nmPerMil, float64(g.drill)/nmPerMil)
}

// viaLayerOrder is a copper layer's place in the stack: the bridge's ids are F.Cu
// 1, B.Cu 2, inner layers from 15 down the stack.
func viaLayerOrder(id int) int {
	switch id {
	case 1:
		return 0
	case 2:
		return math.MaxInt
	}
	return id
}

// viaSpanOverlap: do the layer ranges a-b and c-d share copper? A missing id
// (0) is not known not to, so it counts as overlapping.
func viaSpanOverlap(a, b, c, d int) bool {
	if a == 0 || b == 0 || c == 0 || d == 0 {
		return true
	}
	a, b, c, d = viaLayerOrder(a), viaLayerOrder(b), viaLayerOrder(c), viaLayerOrder(d)
	return max(min(a, b), min(c, d)) <= min(max(a, b), max(c, d))
}

// viaGrowConflict is why a grown via would break clearance on the board, ""
// when nothing does; cannotCheck says a rule or a size to judge it is missing.
// Pads are taken at their circumscribed circle: conservative, DRC has the rest.
// Only copper on a layer the via spans counts.
// grownAt holds the diameters the other candidates will have.
func viaGrowConflict(g viaGrow, tracks []specctra.Track, vias []widenVia, pads []boardPad, rb *kicad.RulesReadback, grownAt map[[2]float64]float64) (why string, cannotCheck bool) {
	own, ok := rb.Nets[g.v.Net]
	if !ok || own.Clearance <= 0 {
		return fmt.Sprintf("no clearance rule resolved for net %s", g.v.Net), true
	}
	need := func(net string) float64 {
		c := own.Clearance
		if e, ok := rb.Nets[net]; ok && e.Clearance > c {
			c = e.Clearance
		}
		if rb.Minimums.Clearance > c {
			c = rb.Minimums.Clearance
		}
		return float64(c) / nmPerMil
	}
	r := float64(g.classDia) / nmPerMil / 2
	for _, t := range tracks {
		if t.Net == g.v.Net || !viaSpanOverlap(g.v.From, g.v.To, t.Layer, t.Layer) {
			continue
		}
		if gap := pointSegDist(t.X1, t.Y1, t.X2, t.Y2, g.v.X, g.v.Y) - t.Width/2 - r; gap < need(t.Net) {
			return fmt.Sprintf("%.2f mil to a track of %s on layer %d, needs %.2f", gap, t.Net, t.Layer, need(t.Net)), false
		}
	}
	for _, v := range vias {
		if v.Net == g.v.Net || !viaSpanOverlap(g.v.From, g.v.To, v.From, v.To) {
			continue
		}
		d := v.Diameter
		if o, ok := grownAt[[2]float64{v.X, v.Y}]; ok {
			d = o
		}
		if gap := math.Hypot(v.X-g.v.X, v.Y-g.v.Y) - d/2 - r; gap < need(v.Net) {
			return fmt.Sprintf("%.2f mil to a via of %s, needs %.2f", gap, v.Net, need(v.Net)), false
		}
	}
	for _, p := range pads {
		if p.Net == g.v.Net || !p.isThroughHole() && !viaSpanOverlap(g.v.From, g.v.To, p.Layer, p.Layer) {
			continue
		}
		if p.W <= 0 || p.H <= 0 {
			if math.Hypot(p.X-g.v.X, p.Y-g.v.Y) < 200 {
				return fmt.Sprintf("pad %s of net %s nearby has no size", p.Number, p.Net), true
			}
			continue
		}
		if gap := math.Hypot(p.X-g.v.X, p.Y-g.v.Y) - math.Hypot(p.W, p.H)/2 - r; gap < need(p.Net) {
			return fmt.Sprintf("%.2f mil to a pad of %s (circumscribed), needs %.2f", gap, p.Net, need(p.Net)), false
		}
	}
	return "", false
}

// repairRouterVias grows the candidates that pass the geometry check, runs DRC
// on the result, and retries once without the vias DRC names. It returns the
// board the result describes: the input board unless a transaction passed.
func repairRouterVias(t routerViaTools, board string, cands []viaGrow, rb *kicad.RulesReadback, next func(string) string, stats map[string]any) (string, viaRepairResult) {
	var res viaRepairResult
	reject := func(g viaGrow, why string) {
		res.evidence = append(res.evidence, g.describe()+"; repair rejected: "+why)
	}
	unchecked := func(g viaGrow, why string) {
		res.unchecked = append(res.unchecked, g.describe()+"; repair rejected: cannot check: "+why)
	}
	allReject := func(gs []viaGrow, why string) {
		for _, g := range gs {
			reject(g, why)
		}
	}
	allUnchecked := func(gs []viaGrow, why string) {
		for _, g := range gs {
			unchecked(g, why)
		}
	}
	defer func() {
		sort.Strings(res.evidence)
		sort.Strings(res.unchecked)
		stats["grownToClass"], stats["repairRejected"] = len(res.grown), len(res.evidence)+len(res.unchecked)
	}()
	raw, err := t.Snapshot(board)
	if err != nil {
		allUnchecked(cands, "board snapshot: "+err.Error())
		return board, res
	}
	var s boardSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		allUnchecked(cands, "decode the board snapshot: "+err.Error())
		return board, res
	}
	tracks, vias, pads, err := copperOf(&s)
	if err != nil {
		allUnchecked(cands, "board copper: "+err.Error())
		return board, res
	}
	grownAt := map[[2]float64]float64{}
	for _, g := range cands {
		grownAt[[2]float64{g.v.X, g.v.Y}] = float64(g.classDia) / nmPerMil
	}
	var pass []viaGrow
	for _, g := range cands {
		if why, unknown := viaGrowConflict(g, tracks, vias, pads, rb, grownAt); why != "" {
			if unknown {
				unchecked(g, why)
			} else {
				reject(g, why)
			}
			continue
		}
		pass = append(pass, g)
	}
	if len(pass) == 0 {
		return board, res
	}
	// What the board already reports is not the repair's doing; the DRC of a
	// large board is slow, so it runs only when a repair will be written.
	base, err := t.DRC(board, board+".drc-baseline.json")
	if err != nil || base == nil {
		allUnchecked(pass, fmt.Sprintf("DRC of the board before the repair: %v", err))
		return board, res
	}
	// One transaction, one retry: each writes a new board from the input.
	for try := 0; try < 2 && len(pass) > 0; try++ {
		out := next("vias-repair")
		ops := make([]map[string]any, 0, len(pass))
		for _, g := range pass {
			ops = append(ops, map[string]any{"id": g.v.ID, "diameter": float64(g.classDia) / nmPerMil, "drill": float64(g.drill) / nmPerMil})
		}
		if _, err := t.Edit(board, map[string]any{"setVia": ops}, out); err != nil {
			allReject(pass, "the resize could not be written: "+err.Error())
			return board, res
		}
		rep, err := t.DRC(out, out+".drc.json")
		if err != nil || rep == nil {
			allUnchecked(pass, fmt.Sprintf("DRC of the repaired board: %v", err))
			return board, res
		}
		fresh := newDRCViolations(base, rep)
		bad := viasNamedByDRC(fresh, pass)
		if len(bad) == 0 && len(fresh) > 0 {
			// A violation on a neighbour only: no way to tell which via caused
			// it, so none of them is kept.
			allReject(pass, fmt.Sprintf("DRC: %d new violation(s) naming no grown via, first %s: transaction dropped", len(fresh), fresh[0]))
			return board, res
		}
		if len(bad) == 0 {
			for _, g := range pass {
				res.grown = append(res.grown, g.v.ID)
				res.info = append(res.info, fmt.Sprintf("%s via %s grown to its class via %.2f/%.2f mil", g.v.Net, short12(g.v.ID), float64(g.classDia)/nmPerMil, float64(g.drill)/nmPerMil))
			}
			return out, res
		}
		var keep []viaGrow
		for _, g := range pass {
			if why, ok := bad[g.v.ID]; ok {
				reject(g, "DRC: "+why)
			} else {
				keep = append(keep, g)
			}
		}
		pass = keep
	}
	allReject(pass, "DRC of the retried transaction still names a grown via: transaction dropped")
	return board, res
}

// drcRelevant: a violation a via repair answers for (connectivity is not).
func drcRelevant(v kicad.Violation) bool {
	return !strings.HasPrefix(v.Rule, "unconnected") && v.Section != "unconnected_items"
}

// drcDelta is a violation the repair answers for: new, or worse than before.
type drcDelta struct {
	v   kicad.Violation
	was string // the message before, for one that got worse
}

func (d drcDelta) String() string {
	if d.was != "" {
		return d.v.Rule + ": " + d.v.Message + " (worse than: " + d.was + ")"
	}
	return d.v.Rule + ": " + d.v.Message
}

// newDRCViolations is what the repair added to the board's violations: those of
// rep that base does not have, matched per rule and object identities (a
// violation without objects has only its message to go by), plus those whose
// measurement got worse. An unchanged violation, or one whose text changed
// without its measurement getting worse, is the board's own.
func newDRCViolations(base, rep *kicad.DRCReport) []drcDelta {
	key := func(v kicad.Violation) string {
		objs := append([]string{}, v.Objs...)
		sort.Strings(objs)
		if len(objs) == 0 {
			return v.Rule + "||" + v.Message
		}
		return v.Rule + "|" + strings.Join(objs, ",")
	}
	had := map[string][]string{} // messages per key
	for _, v := range base.Violations {
		if drcRelevant(v) {
			had[key(v)] = append(had[key(v)], v.Message)
		}
	}
	// Same text first, so that of several on one key only a changed one pairs up.
	var changed []kicad.Violation
	for _, v := range rep.Violations {
		if !drcRelevant(v) {
			continue
		}
		k := key(v)
		if i := slices.Index(had[k], v.Message); i >= 0 {
			had[k] = slices.Delete(had[k], i, i+1)
		} else {
			changed = append(changed, v)
		}
	}
	var out []drcDelta
	for _, v := range changed {
		k := key(v)
		if len(had[k]) == 0 {
			out = append(out, drcDelta{v: v})
			continue
		}
		was := had[k][0]
		had[k] = had[k][1:]
		if drcWorsened(was, v.Message) {
			out = append(out, drcDelta{v: v, was: was})
		}
	}
	return out
}

// reDRCMeasure reads KiCad's "(clearance 0.2000 mm; actual 0.1500 mm)".
var reDRCMeasure = regexp.MustCompile(`([0-9]*\.?[0-9]+)\s*([a-zA-Zµ]*)\s*;\s*actual\s+([0-9]*\.?[0-9]+)\s*([a-zA-Zµ]*)`)

// drcWorsened compares two messages of one violation: worse when the measured
// value moved away from the limit. Fails closed: a limit or unit that changed,
// or a measurement in only one of them, cannot be shown not to be worse. Two
// messages without a measurement differ in words only.
func drcWorsened(was, now string) bool {
	a, b := reDRCMeasure.FindStringSubmatch(was), reDRCMeasure.FindStringSubmatch(now)
	if a == nil && b == nil {
		return false
	}
	if a == nil || b == nil || a[1] != b[1] && !sameNumber(a[1], b[1]) || a[2] != b[2] || a[4] != b[4] {
		return true
	}
	num := func(s string) float64 {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	limit := num(a[1])
	return math.Abs(num(b[3])-limit) > math.Abs(num(a[3])-limit)+1e-9
}

func sameNumber(a, b string) bool {
	x, e1 := strconv.ParseFloat(a, 64)
	y, e2 := strconv.ParseFloat(b, 64)
	return e1 == nil && e2 == nil && x == y
}

// viasNamedByDRC: the candidates those violations name (rule: message).
func viasNamedByDRC(ds []drcDelta, gs []viaGrow) map[string]string {
	ids := map[string]bool{}
	for _, g := range gs {
		ids[g.v.ID] = true
	}
	out := map[string]string{}
	for _, d := range ds {
		for _, o := range d.v.Objs {
			if ids[o] {
				out[o] = d.String()
			}
		}
	}
	return out
}
