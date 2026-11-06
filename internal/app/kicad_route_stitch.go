package app

// kicad_route_stitch.go — stitch vias: a pad of a poured net that its pours did
// not join (a surface pad with its net's pour only on an inner layer) gets one
// via and one short track to it. A site is planned by geometry but proven by
// the board: after the fill, the pad must be in its net's main copper group and
// DRC must show nothing new at the stitch. What fails is taken out again and
// reported; route-complete keeps judging by pcbnew's count alone.

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

const (
	stitchMaxPasses = 3
	stitchMaxRounds = 3  // edits to settle one pass
	stitchMinGain   = 10 // a pass keeping fewer ends the search: each costs minutes
	stitchWorseMm   = 0.002
	stitchCutMil    = 500.0 // how near a stitch must be to answer for a cut plane
	// stitchReachMil: how far from the pad edge a via may sit. Within the pad
	// escape zone the intent-widths gate allows for a track narrower than its
	// class.
	stitchReachMil = 45.0
	// stitchFarReachMil: further still, with a track of the full stitch width only.
	stitchFarReachMil = 150.0
	stitchNearMil     = 30.0 // a new DRC error this close to a stitch is its own
	// One barrel's current limit: 25 um plating, 10 C rise.
	stitchPlatingMil = 1.0
	stitchTempRiseC  = 10.0
)

// stitchSize is the via and track a net's stitches use (mil).
type stitchSize struct {
	Dia, Drill, Track float64
}

// stitch is one planned via and track for one open pad.
type stitch struct {
	PadID string            `json:"pad"`
	Net   string            `json:"net"`
	Via   specctra.NewVia   `json:"via"`
	Drill float64           `json:"drillMil"`
	Track specctra.NewTrack `json:"track"`
	// More is the rest of a searched path (kicad_route_stitch_path.go), and
	// PathMil its whole length.
	More    []specctra.NewTrack `json:"more,omitempty"`
	PathMil float64             `json:"pathMil,omitempty"`
	Site    string              `json:"-"` // tried-site key
}

// planStitches finds a site for each open pad, nearest first, and keeps every
// planned stitch clear of the ones before it. A pad with no site comes back in
// the second list. tried holds sites an earlier pass already failed with.
func planStitches(open, pads []boardPad, tracks []specctra.Track, vias []widenVia, size map[string]stitchSize, clr float64, bounds layoutBBox, tried map[string]bool) ([]stitch, []string) {
	return planStitchPaths(open, pads, tracks, vias, size, clr, bounds, tried, 0, nil)
}

// planStitchPaths is planStitches with a path search of up to pathMil for a pad
// no straight track can serve (0 = none). inPour, when given, must accept a
// via site: a via outside its net's fill joins nothing.
func planStitchPaths(open, pads []boardPad, tracks []specctra.Track, vias []widenVia, size map[string]stitchSize, clr float64, bounds layoutBBox, tried map[string]bool, pathMil float64, inPour func(net string, q [2]float64, dia float64) bool) ([]stitch, []string) {
	type seg struct {
		net       string
		layer     int // 0 = every layer
		a, b      [2]float64
		halfWidth float64
	}
	var cu []seg
	for _, t := range tracks {
		cu = append(cu, seg{t.Net, t.Layer, [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}, t.Width / 2})
	}
	for _, v := range vias {
		cu = append(cu, seg{v.Net, 0, [2]float64{v.X, v.Y}, [2]float64{v.X, v.Y}, v.Diameter / 2})
	}
	same := func(a, b string) bool { return strings.EqualFold(a, b) }
	padDist := func(p boardPad, q [2]float64) float64 {
		return math.Hypot(math.Max(math.Abs(q[0]-p.X)-p.W/2, 0), math.Max(math.Abs(q[1]-p.Y)-p.H/2, 0))
	}
	viaFits := func(net string, q [2]float64, dia float64) bool {
		if q[0]-dia/2 < bounds.MinX || q[0]+dia/2 > bounds.MaxX || q[1]-dia/2 < bounds.MinY || q[1]+dia/2 > bounds.MaxY {
			return false
		}
		if inPour != nil && !inPour(net, q, dia) {
			return false
		}
		for _, c := range cu {
			if !same(c.net, net) && segDist(q, c.a, c.b)-c.halfWidth-dia/2 < clr {
				return false
			}
			// A via on top of same-net copper would not need this stitch's
			// proof to be wrong, but two barrels too close are a drill error.
			if same(c.net, net) && c.layer == 0 && segDist(q, c.a, c.b)-c.halfWidth-dia/2 < clr {
				return false
			}
		}
		for _, p := range pads { // no via in any pad, its own included
			if padDist(p, q)-dia/2 < clr {
				return false
			}
		}
		return true
	}
	trackFits := func(own boardPad, layer int, a, b [2]float64, half float64) bool {
		for _, c := range cu {
			if same(c.net, own.Net) || (c.layer != layer && c.layer != 0) {
				continue
			}
			if segSegDist(a[0], a[1], b[0], b[1], c.a[0], c.a[1], c.b[0], c.b[1])-c.halfWidth-half < clr {
				return false
			}
		}
		for _, p := range pads {
			if same(p.Net, own.Net) || (p.Layer != layer && p.Layer != pcbLayerMulti) {
				continue
			}
			if rectSegDist(p.X-p.W/2, p.Y-p.H/2, p.X+p.W/2, p.Y+p.H/2, a[0], a[1], b[0], b[1])-half < clr {
				return false
			}
		}
		return true
	}
	// A searched path keeps the full stitch width, and sees only the copper
	// within its reach: the search asks thousands of times per pad.
	pathLeft := stitchPathPassNodes
	pathStitch := func(p boardPad, layer int, from [2]float64, sz stitchSize) *stitch {
		allCu, allPads := cu, pads
		defer func() { cu, pads = allCu, allPads }()
		r := pathMil + sz.Dia + sz.Track + clr
		cu, pads = nil, nil
		for _, c := range allCu {
			if segDist(from, c.a, c.b)-c.halfWidth <= r {
				cu = append(cu, c)
			}
		}
		for _, o := range allPads {
			if math.Hypot(o.X-from[0], o.Y-from[1])-math.Hypot(o.W, o.H)/2 <= r {
				pads = append(pads, o)
			}
		}
		half := sz.Track / 2
		var site string
		pts := stitchPathSearchBudget(from, pathMil, func(a, b [2]float64) bool {
			return trackFits(p, layer, a, b, half)
		}, func(q [2]float64) bool {
			site = fmt.Sprintf("%s@%.2f,%.2f", p.ID, q[0], q[1])
			return !tried[site] && viaFits(p.Net, q, sz.Dia)
		}, &pathLeft)
		if len(pts) < 2 {
			return nil
		}
		end := pts[len(pts)-1]
		st := &stitch{PadID: p.ID, Net: p.Net, Site: site, Drill: sz.Drill, Via: specctra.NewVia{Net: p.Net, X: end[0], Y: end[1], DiameterMil: sz.Dia}}
		for i := 0; i+1 < len(pts); i++ {
			t := specctra.NewTrack{Net: p.Net, Layer: layer, X1: pts[i][0], Y1: pts[i][1], X2: pts[i+1][0], Y2: pts[i+1][1], Width: sz.Track}
			if i == 0 {
				st.Track = t
			} else {
				st.More = append(st.More, t)
			}
			st.PathMil += math.Hypot(t.X2-t.X1, t.Y2-t.Y1)
		}
		return st
	}
	order := append([]boardPad(nil), open...)
	sort.Slice(order, func(i, j int) bool {
		if order[i].Net != order[j].Net {
			return order[i].Net < order[j].Net
		}
		if order[i].X != order[j].X {
			return order[i].X < order[j].X
		}
		if order[i].Y != order[j].Y {
			return order[i].Y < order[j].Y
		}
		return order[i].ID < order[j].ID
	})
	var out []stitch
	var noSite []string
	for _, p := range order {
		sz, ok := size[p.Net]
		if !ok || sz.Dia <= 0 {
			noSite = append(noSite, p.ID)
			continue
		}
		layer := p.Layer
		if layer == pcbLayerMulti {
			layer = 1
		}
		half := math.Max(math.Min(sz.Track, math.Min(p.W, p.H)), 1) / 2
		from := [2]float64{p.X, p.Y}
		// Rings outward from the pad edge, 16 directions on each.
		base := sz.Dia/2 + clr
		var placed *stitch
		maxReach := stitchReachMil
		if half*2 >= sz.Track {
			maxReach = stitchFarReachMil
		}
	search:
		for reach := 0.0; reach <= maxReach; reach += 5 {
			for k := 0; k < 16; k++ {
				th := float64(k) * math.Pi / 8
				dx, dy := math.Cos(th), math.Sin(th)
				// From the centre to just past the pad edge in this direction.
				edge := math.Min(p.W/2/math.Max(math.Abs(dx), 1e-9), p.H/2/math.Max(math.Abs(dy), 1e-9))
				d := edge + base + reach
				q := [2]float64{math.Round((p.X+dx*d)*100) / 100, math.Round((p.Y+dy*d)*100) / 100}
				key := fmt.Sprintf("%s@%.2f,%.2f", p.ID, q[0], q[1])
				if tried[key] || !viaFits(p.Net, q, sz.Dia) || !trackFits(p, layer, from, q, half) {
					continue
				}
				placed = &stitch{PadID: p.ID, Net: p.Net, Site: key, Drill: sz.Drill,
					Via:   specctra.NewVia{Net: p.Net, X: q[0], Y: q[1], DiameterMil: sz.Dia},
					Track: specctra.NewTrack{Net: p.Net, Layer: layer, X1: from[0], Y1: from[1], X2: q[0], Y2: q[1], Width: half * 2}}
				break search
			}
		}
		if placed == nil && pathMil > 0 {
			placed = pathStitch(p, layer, from, sz)
		}
		if placed == nil {
			noSite = append(noSite, p.ID)
			continue
		}
		q := [2]float64{placed.Via.X, placed.Via.Y}
		cu = append(cu, seg{p.Net, 0, q, q, sz.Dia / 2})
		for _, t := range append([]specctra.NewTrack{placed.Track}, placed.More...) {
			cu = append(cu, seg{p.Net, layer, [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}, t.Width / 2})
		}
		out = append(out, *placed)
	}
	return out, noSite
}

// openPadsOf picks the pads outside each net's main group: of the groups holding
// zone copper, the one with the most pads. Pads a net's pours do not reach are
// all open, however many a track joins.
func openPadsOf(groups map[string][]kicad.PadGroup) map[string]bool {
	open := map[string]bool{}
	for _, gs := range groups {
		main, most := -1, 0
		for i, g := range gs {
			if g.Zone && len(g.Pads) > most {
				main, most = i, len(g.Pads)
			}
		}
		for i, g := range gs {
			if i == main {
				continue
			}
			for _, id := range g.Pads {
				open[id] = true
			}
		}
	}
	return open
}

// stitchNearViolation says whether a violation sits at one of the stitches.
func stitchNearViolation(v kicad.Violation, s stitch) bool {
	// Any object of it: an insulation error names the via and a track whose
	// reported position can be millimetres away.
	near := func(q [2]float64) bool {
		for _, t := range append([]specctra.NewTrack{s.Track}, s.More...) {
			if segDist(q, [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}) <= stitchNearMil {
				return true
			}
		}
		return false
	}
	for _, it := range v.Items {
		if near([2]float64{it.X, it.Y}) {
			return true
		}
	}
	return v.X != nil && v.Y != nil && near([2]float64{*v.X, *v.Y})
}

var reDRCActual = regexp.MustCompile(`actual ([0-9.]+) mm`)

// violationKey names a violation by its rule and objects, in any order: KiCad
// lists the same two either way round from run to run. Objects count by uuid,
// since two vias of a net have the same description.
func violationKey(v kicad.Violation) string {
	if len(v.Items) == 0 {
		at := ""
		if v.X != nil && v.Y != nil {
			at = fmt.Sprintf("@%.1f,%.1f", *v.X, *v.Y)
		}
		return v.Rule + "|" + v.Message + at
	}
	items := make([]string, 0, len(v.Items))
	for _, it := range v.Items {
		if it.UUID != "" {
			items = append(items, it.UUID)
		} else {
			// With no uuid only the place tells two like objects apart.
			items = append(items, fmt.Sprintf("%s@%.1f,%.1f", it.Description, it.X, it.Y))
		}
	}
	sort.Strings(items)
	return v.Rule + "|" + strings.Join(items, "|")
}

func violationActual(v kicad.Violation) (float64, bool) {
	m := reDRCActual.FindStringSubmatch(v.Message)
	if m == nil {
		return 0, false
	}
	a, err := strconv.ParseFloat(m[1], 64)
	return a, err == nil
}

// newViolations lists what cur has and base did not, by rule and objects, and
// what got worse: a gap shrinking by more than stitchWorseMm between the same
// objects. Open connections are judged by pad groups instead.
func newViolations(base, cur *kicad.DRCReport) []kicad.Violation {
	type entry struct {
		n      int
		actual float64
		known  bool
	}
	seen := map[string]*entry{}
	for _, v := range base.Violations {
		k := violationKey(v)
		e := seen[k]
		if e == nil {
			e = &entry{}
			seen[k] = e
		}
		e.n++
		e.actual, e.known = violationActual(v)
	}
	var out []kicad.Violation
	for _, v := range cur.Violations {
		if v.Rule == "unconnected_items" {
			continue
		}
		e := seen[violationKey(v)]
		if e == nil || e.n == 0 {
			out = append(out, v)
			continue
		}
		e.n--
		if a, ok := violationActual(v); ok && e.known && a < e.actual-stitchWorseMm {
			out = append(out, v)
		}
	}
	return out
}

// stitchPours joins the open pads of the poured nets, in up to stitchMaxPasses
// passes. Each pass adds all its candidates, keeps those the board proves, and
// redoes the edit with those alone. It never returns a board it did not check;
// a failed check leaves the board as it was before that pass.
func (r *kicadRun) stitchPours(board string, nets []string) (string, error) {
	rep := map[string]any{"nets": nets}
	r.summary["stitchPours"] = rep
	cannot := func(why string) {
		fmt.Fprintf(r.stderr, "stitch: cannot check: %s\n", why)
		rep["error"] = why
		r.stepGates = append(r.stepGates, gateResult{Gate: "stitch-pours", Detail: "cannot check: " + why, CannotCheck: true})
	}
	rb, err := r.kt.RulesRead(board)
	if err != nil {
		cannot("KiCad's class readback failed: " + err.Error())
		return board, nil
	}
	size := map[string]stitchSize{}
	reqs := intentRequirements(r.in)
	const nmPerMil = 25400.0
	for _, n := range nets {
		e, ok := rb.Nets[n]
		if !ok || e.ViaDiameter <= 0 || e.ViaDrill <= 0 {
			continue
		}
		sz := stitchSize{Dia: float64(e.ViaDiameter) / nmPerMil, Drill: float64(e.ViaDrill) / nmPerMil}
		if q, ok := reqs[n]; ok {
			sz.Track = q.OuterMil
		}
		if sz.Track <= 0 {
			sz.Track = 10
		}
		size[n] = sz
	}
	base, err := r.kt.DRC(board, filepath.Join(r.work, fmt.Sprintf("%02d-stitch-base-drc.json", r.step)))
	if err != nil {
		cannot("DRC before stitching: " + err.Error())
		return board, nil
	}
	tried := map[string]bool{}
	var kept []stitch
	var passes []map[string]any
	stop := fmt.Sprintf("%d passes", stitchMaxPasses)
	cur := board
	for pass := 1; pass <= stitchMaxPasses; pass++ {
		groups, err := r.kt.PadGroups(cur, nets)
		if err != nil {
			cannot("pad groups: " + err.Error())
			return cur, nil
		}
		openIDs := openPadsOf(groups)
		s, _, err := r.snapshot(cur)
		if err != nil || s.Outline == nil {
			cannot(fmt.Sprintf("board snapshot: %v", err))
			return cur, nil
		}
		tracks, vias, pads, err := copperOf(s)
		if err != nil {
			cannot("board copper: " + err.Error())
			return cur, nil
		}
		padAt := map[string][2]float64{}
		for _, p := range pads {
			padAt[p.ID] = [2]float64{p.X, p.Y}
		}
		var open []boardPad
		refused := map[string]string{}
		pinOf := map[string]stitchPin{}
		for i := range s.Components {
			for _, p := range s.Components[i].Pads {
				pinOf[p.ID] = stitchPin{s.Components[i].Designator, p.Number}
			}
		}
		// Each pad's group: what a via on it would feed.
		groupOf := map[string][]stitchPin{}
		for _, gs := range groups {
			for _, g := range gs {
				pins := make([]stitchPin, 0, len(g.Pads))
				for _, id := range g.Pads {
					pins = append(pins, pinOf[id])
				}
				for _, id := range g.Pads {
					groupOf[id] = pins
				}
			}
		}
		for _, p := range pads {
			if !openIDs[p.ID] {
				continue
			}
			sz := size[p.Net]
			if why := stitchRefusal(r.in, p.Net, groupOf[p.ID], sz.Drill); why != "" {
				refused[pinOf[p.ID].Ref+"."+p.Number] = why
				continue
			}
			open = append(open, p)
		}
		info := map[string]any{"pass": pass, "open": len(open), "refused": refused}
		passes = append(passes, info)
		if len(open) == 0 {
			stop = "no open pads"
			break
		}
		edge := 20.0
		if s.Rules != nil && s.Rules.CopperToEdgeMil > edge {
			edge = s.Rules.CopperToEdgeMil
		}
		b := s.Outline.BBox
		bounds := layoutBBox{MinX: b.MinX + edge, MinY: b.MinY + edge, MaxX: b.MaxX - edge, MaxY: b.MaxY - edge}
		// A site must lie wholly inside a fill of its net on some layer, as
		// filled now: a via the fill misses joins nothing (most stitches the
		// proof dropped on the test board sat outside every fill).
		fills := map[string][]pcbCopperArea{}
		if areas, err := parseCopperAreaObstacles(nil, s.Copper.Poured); err == nil {
			for _, a := range areas {
				fills[a.Net] = append(fills[a.Net], a)
			}
		} else {
			fmt.Fprintf(r.stderr, "stitch: pour fills unreadable (%v); sites are not checked against them\n", err)
			fills = nil
		}
		inPour := func(net string, q [2]float64, dia float64) bool {
			if fills == nil {
				return true
			}
			for _, a := range fills[net] {
				if compoundWinding(a.Contours, q) != 0 && stitchFillMargin(a, q) >= dia/2 {
					return true
				}
			}
			return false
		}
		plan, noSite := planStitchPaths(open, pads, tracks, vias, size, clearanceOf(s)+kicad.ClearanceMarginMil, bounds, tried, r.o.stitchPathMil, inPour)
		info["planned"], info["noSite"] = len(plan), len(noSite)
		if len(plan) == 0 {
			stop = "no site for the pads left"
			break
		}
		trial := r.next("stitch-trial")
		if _, err := r.kt.Edit(cur, stitchOps(plan), trial); err != nil {
			cannot("stitch edit: " + err.Error())
			return cur, nil
		}
		good, reasons, err := r.stitchProve(trial, plan, nets, base, groups, padAt, pass, true)
		if err != nil {
			cannot(err.Error())
			return cur, nil
		}
		info["rejected"] = reasons
		for _, c := range plan {
			tried[c.Site] = true
		}
		if len(good) == 0 {
			stop = "no stitch held"
			continue
		}
		// Only what held goes on the board, and that board is checked again:
		// taking one out can change another's reach. Each round drops what
		// failed; a pass that cannot settle changes nothing.
		next := ""
		settled := false
		for round := 0; round < stitchMaxRounds && len(good) > 0; round++ {
			next = r.next("stitch")
			if _, err := r.kt.Edit(cur, stitchOps(good), next); err != nil {
				cannot("stitch edit: " + err.Error())
				return cur, nil
			}
			held, again, err := r.stitchProve(next, good, nets, base, groups, padAt, pass, false)
			if err != nil {
				cannot(err.Error())
				return cur, nil
			}
			if len(again) == 0 {
				settled = true
				break
			}
			info[fmt.Sprintf("dropped%d", round+1)] = again
			if _, away := again["board"]; away && len(held) == len(good) {
				break // a new error at no stitch: nothing left to drop
			}
			good = held
		}
		if !settled {
			stop = "a pass did not settle after its failures came out"
			break
		}
		cur = next
		kept = append(kept, good...)
		info["kept"] = len(good)
		if len(good) < stitchMinGain {
			stop = fmt.Sprintf("a pass kept under %d", stitchMinGain)
			break
		}
	}
	byNet := map[string]int{}
	paths, longest := 0, 0.0
	for _, c := range kept {
		byNet[c.Net]++
		if c.PathMil > 0 {
			paths++
			longest = math.Max(longest, c.PathMil)
		}
	}
	rep["pathStitches"], rep["longestPathMil"] = paths, longest
	rep["stitched"], rep["passes"], rep["stop"] = byNet, passes, stop
	rep["newDrcAwayFromStitches"] = r.stitchAway
	rep["stitches"] = kept
	fmt.Fprintf(r.stderr, "stitch: %d via(s) kept on %d net(s), stopped: %s\n", len(kept), len(byNet), stop)
	return cur, nil
}

// stitchFillMargin is the distance from q to the nearest edge of a fill.
func stitchFillMargin(a pcbCopperArea, q [2]float64) float64 {
	best := math.Inf(1)
	for _, c := range a.Contours {
		for i, p := range c {
			n := c[(i+1)%len(c)]
			best = math.Min(best, segPtDist(q[0], q[1], p[0], p[1], n[0], n[1]))
		}
	}
	return best
}

// stitchOps is the bridge edit of a set of stitches.
func stitchOps(ss []stitch) map[string]any {
	var av, at []map[string]any
	for _, c := range ss {
		av = append(av, map[string]any{"net": c.Via.Net, "x": c.Via.X, "y": c.Via.Y, "diameter": c.Via.DiameterMil, "drill": c.Drill})
		for _, t := range append([]specctra.NewTrack{c.Track}, c.More...) {
			at = append(at, map[string]any{"net": t.Net, "layer": t.Layer, "startX": t.X1, "startY": t.Y1, "endX": t.X2, "endY": t.Y2, "lineWidth": t.Width})
		}
	}
	return map[string]any{"addVias": av, "addTracks": at}
}

// stitchProve checks a filled board: which stitches joined their pad to the
// net's main group with no new DRC error at them. A new error at no stitch at
// all rejects nothing here but is an error of the pass: the caller keeps the
// board from before it. On a trial a cut plane fails only the nearest stitch:
// the checks that follow fail all those around it if the cut is still there.
func (r *kicadRun) stitchProve(board string, plan []stitch, nets []string, base *kicad.DRCReport, before map[string][]kicad.PadGroup, at map[string][2]float64, pass int, trial bool) ([]stitch, map[string]string, error) {
	rep, err := r.kt.DRC(board, filepath.Join(r.work, fmt.Sprintf("%02d-stitch-drc%d.json", r.step, pass)))
	if err != nil {
		return nil, nil, fmt.Errorf("DRC after stitching: %w", err)
	}
	groups, err := r.kt.PadGroups(board, nets)
	if err != nil {
		return nil, nil, fmt.Errorf("pad groups after stitching: %w", err)
	}
	open := openPadsOf(groups)
	fresh := newViolations(base, rep)
	reasons := map[string]string{}
	// A stitch can cut a plane in two at its neck: pads together before and apart
	// now fail the stitches around them. With none near, nothing can be named
	// and the pass fails.
	lost := padsSplit(before, groups)
	cut := stitchCutters(lost, plan, at, trial)
	for _, id := range sortedKeysOf(lost) {
		if !cut.named[id] {
			reasons["board"] = "pad " + id + " was cut off with no stitch near it"
			break
		}
	}
	var good []stitch
	for _, c := range plan {
		bad := ""
		if open[c.PadID] {
			bad = "pad still outside its net's main group"
		}
		if cut.stitches[c.PadID] {
			bad = "near a pad that lost its pour"
		}
		for _, v := range fresh {
			if bad == "" && stitchNearViolation(v, c) {
				bad = "new DRC " + v.Rule
			}
		}
		if bad != "" {
			reasons[c.PadID] = bad
			continue
		}
		good = append(good, c)
	}
	away := violationsAwayFrom(fresh, plan)
	r.stitchAway = len(away)
	if len(away) > 0 {
		// A stitch only adds copper of its own, so these are not its doing: KiCad
		// can report an old error against another of the same track's neighbours.
		// The final DRC still counts them.
		fmt.Fprintf(r.stderr, "stitch: %d new DRC error(s) away from any stitch, first: %s %s\n", len(away), away[0].Rule, away[0].Message)
	}
	return good, reasons, nil
}

// violationsAwayFrom lists the violations near none of the stitches.
func violationsAwayFrom(vs []kicad.Violation, plan []stitch) []kicad.Violation {
	var away []kicad.Violation
	for _, v := range vs {
		near := false
		for _, c := range plan {
			near = near || stitchNearViolation(v, c)
		}
		if !near {
			away = append(away, v)
		}
	}
	return away
}

// padsSplit lists the pads of each earlier group that are no longer with the
// largest part of that group, or have lost the zone that group held. A growing
// group splits nothing, and a net's main group changing from one piece of copper
// to another is not a cut. A pour cut in two whose pads a track still joins does
// not show here; the post-layout simulation sees that copper.
func padsSplit(before, after map[string][]kicad.PadGroup) map[string]bool {
	lost := map[string]bool{}
	for net, gs := range before {
		where := map[string]int{}
		for i, g := range after[net] {
			for _, id := range g.Pads {
				where[id] = i
			}
		}
		for _, g := range gs {
			parts := map[int][]string{}
			for _, id := range g.Pads {
				if w, ok := where[id]; ok {
					parts[w] = append(parts[w], id)
				}
			}
			// Copper that held a zone and no longer does was cut off it, even
			// a single pad with no group to leave.
			if g.Zone {
				for w, ids := range parts {
					if !after[net][w].Zone {
						for _, id := range ids {
							lost[id] = true
						}
					}
				}
			}
			if len(parts) < 2 {
				continue
			}
			keep, most := -1, 0
			for w, ids := range parts {
				if len(ids) > most || len(ids) == most && w < keep {
					keep, most = w, len(ids)
				}
			}
			for w, ids := range parts {
				if w != keep {
					for _, id := range ids {
						lost[id] = true
					}
				}
			}
		}
	}
	return lost
}

type stitchCut struct {
	stitches map[string]bool // by the pad each stitch serves
	named    map[string]bool // lost pads with a stitch near them
}

// stitchCutters finds the stitches that may have cut a pad off: those with any
// copper within stitchCutMil of it, or only the nearest of them.
func stitchCutters(lost map[string]bool, plan []stitch, at map[string][2]float64, nearestOnly bool) stitchCut {
	cut := stitchCut{stitches: map[string]bool{}, named: map[string]bool{}}
	for id := range lost {
		q, ok := at[id]
		if !ok {
			continue
		}
		nearest, best := "", math.Inf(1)
		for _, c := range plan {
			d := math.Hypot(c.Via.X-q[0], c.Via.Y-q[1])
			for _, t := range append([]specctra.NewTrack{c.Track}, c.More...) {
				d = math.Min(d, segDist(q, [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}))
			}
			if d > stitchCutMil {
				continue
			}
			cut.named[id] = true
			if !nearestOnly {
				cut.stitches[c.PadID] = true
			} else if d < best || (d == best && c.PadID < nearest) {
				nearest, best = c.PadID, d
			}
		}
		if nearest != "" {
			cut.stitches[nearest] = true
		}
	}
	return cut
}

// stitchPin names a pad for the current lookup.
type stitchPin struct{ Ref, Pin string }

// stitchRefusal says why one via may not feed this copper: the pads a track
// already joins to the pad all draw through it, so their currents add, and none
// may be unknown. Empty when it may.
func stitchRefusal(in *designIntent, net string, pins []stitchPin, drillMil float64) string {
	if in == nil || in.Nets[net] == nil {
		return "no simulated current for the net"
	}
	current := map[stitchPin]*float64{}
	for _, p := range in.Nets[net].Pins {
		current[stitchPin{p.Ref, p.Pin}] = p.CurrentA
	}
	sum := 0.0
	for _, pin := range pins {
		i := current[pin]
		if i == nil {
			return fmt.Sprintf("no simulated current for %s.%s", pin.Ref, pin.Pin)
		}
		sum += math.Abs(*i)
	}
	if limit := pcbauto.ViaAmpacity(drillMil, stitchPlatingMil, stitchTempRiseC); sum > limit {
		return fmt.Sprintf("%.3f A is above one via's %.3f A", sum, limit)
	}
	return ""
}

// stitchNets are the nets stitching works on: those with a planned pour, when
// --stitch-pours says so. auto follows the large-board profile.
func (r *kicadRun) stitchNets(ps []kicad.Pour) []string {
	switch r.o.stitchPours {
	case "off":
		return nil
	case "auto":
		if r.summary["largeBoard"] == nil {
			return nil
		}
	}
	seen := map[string]bool{}
	var nets []string
	for _, p := range ps {
		if !seen[p.Net] {
			seen[p.Net] = true
			nets = append(nets, p.Net)
		}
	}
	sort.Strings(nets)
	return nets
}
