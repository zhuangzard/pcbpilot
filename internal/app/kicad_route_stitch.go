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
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

const (
	stitchMaxPasses = 3
	// stitchReachMil: how far from the pad edge a via may sit. Within the pad
	// escape zone the intent-widths gate allows for a track narrower than its
	// class.
	stitchReachMil = 45.0
	stitchNearMil  = 30.0 // a new DRC error this close to a stitch is its own
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
	Site  string            `json:"-"` // tried-site key
}

// planStitches finds a site for each open pad, nearest first, and keeps every
// planned stitch clear of the ones before it. A pad with no site comes back in
// the second list. tried holds sites an earlier pass already failed with.
func planStitches(open, pads []boardPad, tracks []specctra.Track, vias []widenVia, size map[string]stitchSize, clr float64, bounds layoutBBox, tried map[string]bool) ([]stitch, []string) {
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
	search:
		for reach := 0.0; reach <= stitchReachMil; reach += 5 {
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
		if placed == nil {
			noSite = append(noSite, p.ID)
			continue
		}
		q := [2]float64{placed.Via.X, placed.Via.Y}
		cu = append(cu, seg{p.Net, 0, q, q, sz.Dia / 2}, seg{p.Net, layer, from, q, half})
		out = append(out, *placed)
	}
	return out, noSite
}

// openPadsOf picks the pads outside each net's main group, the group with the
// most pads. A net whose groups are all single pads has no main group yet: all
// of them are open.
func openPadsOf(groups map[string][][]string) map[string]bool {
	open := map[string]bool{}
	for _, gs := range groups {
		main, most := -1, 1
		for i, g := range gs {
			if len(g) > most {
				main, most = i, len(g)
			}
		}
		for i, g := range gs {
			if i == main {
				continue
			}
			for _, id := range g {
				open[id] = true
			}
		}
	}
	return open
}

// stitchNearViolation says whether a violation sits at one of the stitches.
func stitchNearViolation(v kicad.Violation, s stitch) bool {
	if v.X == nil || v.Y == nil {
		return false
	}
	q := [2]float64{*v.X, *v.Y}
	a := [2]float64{s.Track.X1, s.Track.Y1}
	b := [2]float64{s.Via.X, s.Via.Y}
	return segDist(q, a, b) <= stitchNearMil
}

// newViolations lists what cur has and base did not, by rule and message.
func newViolations(base, cur *kicad.DRCReport) []kicad.Violation {
	seen := map[string]int{}
	for _, v := range base.Violations {
		seen[v.Rule+"|"+v.Message]++
	}
	var out []kicad.Violation
	for _, v := range cur.Violations {
		k := v.Rule + "|" + v.Message
		if seen[k] > 0 {
			seen[k]--
			continue
		}
		if v.Rule != "unconnected_items" {
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
		var open []boardPad
		for _, p := range pads {
			if openIDs[p.ID] {
				open = append(open, p)
			}
		}
		info := map[string]any{"pass": pass, "open": len(open)}
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
		plan, noSite := planStitches(open, pads, tracks, vias, size, clearanceOf(s)+kicad.ClearanceMarginMil, bounds, tried)
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
		good, reasons, err := r.stitchProve(trial, plan, nets, base, pass)
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
		// taking one out can change another's reach.
		next := r.next("stitch")
		if _, err := r.kt.Edit(cur, stitchOps(good), next); err != nil {
			cannot("stitch edit: " + err.Error())
			return cur, nil
		}
		held, again, err := r.stitchProve(next, good, nets, base, pass)
		if err != nil {
			cannot(err.Error())
			return cur, nil
		}
		if len(again) > 0 || len(held) != len(good) {
			info["discarded"] = again
			stop = "a pass did not hold after its failures came out"
			break
		}
		cur = next
		kept = append(kept, good...)
		info["kept"] = len(good)
		// The new board's own errors are the baseline only for what was already
		// there; a kept stitch has none by the check above.
	}
	byNet := map[string]int{}
	for _, c := range kept {
		byNet[c.Net]++
	}
	rep["stitched"], rep["passes"], rep["stop"] = byNet, passes, stop
	rep["stitches"] = kept
	fmt.Fprintf(r.stderr, "stitch: %d via(s) kept on %d net(s), stopped: %s\n", len(kept), len(byNet), stop)
	return cur, nil
}

// stitchOps is the bridge edit of a set of stitches.
func stitchOps(ss []stitch) map[string]any {
	var av, at []map[string]any
	for _, c := range ss {
		av = append(av, map[string]any{"net": c.Via.Net, "x": c.Via.X, "y": c.Via.Y, "diameter": c.Via.DiameterMil, "drill": c.Drill})
		at = append(at, map[string]any{"net": c.Track.Net, "layer": c.Track.Layer, "startX": c.Track.X1, "startY": c.Track.Y1, "endX": c.Track.X2, "endY": c.Track.Y2, "lineWidth": c.Track.Width})
	}
	return map[string]any{"addVias": av, "addTracks": at}
}

// stitchProve checks a filled board: which stitches joined their pad to the
// net's main group with no new DRC error at them. A new error at no stitch at
// all rejects nothing here but is an error of the pass: the caller keeps the
// board from before it.
func (r *kicadRun) stitchProve(board string, plan []stitch, nets []string, base *kicad.DRCReport, pass int) ([]stitch, map[string]string, error) {
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
	var good []stitch
	claimed := make([]bool, len(fresh))
	for _, c := range plan {
		bad := ""
		if open[c.PadID] {
			bad = "pad still outside its net's main group"
		}
		for i, v := range fresh {
			if stitchNearViolation(v, c) {
				claimed[i] = true
				if bad == "" {
					bad = "new DRC " + v.Rule
				}
			}
		}
		if bad != "" {
			reasons[c.PadID] = bad
			continue
		}
		good = append(good, c)
	}
	for i, v := range fresh {
		if !claimed[i] {
			reasons["board"] = "new DRC " + v.Rule + " away from any stitch"
			break
		}
	}
	return good, reasons, nil
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
