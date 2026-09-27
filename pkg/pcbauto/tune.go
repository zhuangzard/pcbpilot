package pcbauto

import (
	"math"
	"sort"
	"strings"
	"time"
)

func trackLen(ts []Track) float64 {
	s := 0.0
	for _, t := range ts {
		s += t.A.Dist(t.B)
	}
	return s
}

// netCopperLen is the length of all of n's copper the SI check measures:
// routed tracks plus fan-out, shared-pad and BGA escape stubs. Tuning on the
// routed tracks alone left a pair "matched" whose escapes differed.
func (r *router) netCopperLen(n *rnet, out *netOut) float64 {
	return trackLen(out.tracks) + trackLen(n.fanTracks) + trackLen(n.shareTracks) + trackLen(escTracks(n))
}

// tuneLengths equalises differential pairs whose intra-pair skew exceeds
// their interface limit by inserting a serpentine on the shorter member,
// then matches declared length groups (intent lengthGroup: HDMI TMDS
// inter-pair, DDR byte lanes, …).
func (r *router) tuneLengths(outs map[*rnet]*netOut, res *RouteResult) {
	r.recouple(outs, res)
	done := map[*rnet]bool{}
	for _, n := range r.nets {
		p := r.byName[n.plan.PairWith]
		if p == nil || done[n] || len(n.paths) == 0 || len(p.paths) == 0 {
			continue
		}
		done[n], done[p] = true, true
		hc := ClassifyHS(n.plan)
		if hc == nil || hc.MaxSkewMil <= 0 {
			continue
		}
		ln, lp := r.netCopperLen(n, outs[n]), r.netCopperLen(p, outs[p])
		short := n
		if ln > lp {
			short = p
		}
		delta := math.Abs(ln - lp)
		if delta <= hc.MaxSkewMil {
			continue
		}
		need := delta - hc.MaxSkewMil/4 // aim inside the budget, not at its edge
		// Spread the added length over several straight runs when no single
		// run has room: largest chunk that fits first, halving on failure.
		rem := need
		for attempt := 0; attempt < 8 && rem > hc.MaxSkewMil*0.75; attempt++ {
			chunk := rem
			for chunk >= 15 && !r.meander(short, outs[short], chunk) {
				chunk /= 2
			}
			if chunk < 15 {
				break
			}
			rem -= chunk
		}
		if rem <= hc.MaxSkewMil*0.75 {
			res.Stats.Tuned++
			res.Notes = append(res.Notes, sprintf("length-tuned %s +%.0f mil (pair %s/%s %.0f/%.0f → %.0f/%.0f mil, limit %.0f mil)", short.name, need-rem, n.name, p.name,
				ln, lp, r.netCopperLen(n, outs[n]), r.netCopperLen(p, outs[p]), hc.MaxSkewMil))
		} else {
			res.Notes = append(res.Notes, sprintf("tuned %s/%s only partly: %.0f of %.0f mil added (limit %.0f): not enough straight runs with room", n.name, p.name, need-rem, need, hc.MaxSkewMil))
		}
	}
	r.tuneGroups(outs, res)
}

// tuneUnit is one member of a length group: a single net, or both nets of
// a differential pair (lengthened together so the intra-pair skew holds).
type tuneUnit struct {
	nets []*rnet
}

func (r *router) unitLen(u tuneUnit, outs map[*rnet]*netOut) float64 {
	s := 0.0
	for _, n := range u.nets {
		s += r.netCopperLen(n, outs[n])
	}
	return s / float64(len(u.nets))
}

func (u tuneUnit) name() string {
	var s []string
	for _, n := range u.nets {
		s = append(s, n.name)
	}
	return strings.Join(s, "/")
}

// lengthGroups collects the routed units of every declared length group
// with at least two units (a group that is just one pair is the intra-pair
// match above).
func (r *router) lengthGroups() (map[string][]tuneUnit, []string) {
	members := map[string][]*rnet{}
	for _, n := range r.nets {
		if g := n.plan.LengthGroup; g != "" && len(n.paths) > 0 {
			members[g] = append(members[g], n)
		}
	}
	out := map[string][]tuneUnit{}
	var names []string
	for g, ns := range members {
		in := map[*rnet]bool{}
		for _, n := range ns {
			in[n] = true
		}
		seen := map[*rnet]bool{}
		var units []tuneUnit
		for _, n := range ns {
			if seen[n] {
				continue
			}
			seen[n] = true
			u := tuneUnit{nets: []*rnet{n}}
			if p := r.byName[n.plan.PairWith]; p != nil && in[p] && !seen[p] {
				seen[p] = true
				u.nets = append(u.nets, p)
			}
			units = append(units, u)
		}
		if len(units) >= 2 {
			sort.Slice(units, func(i, j int) bool { return units[i].nets[0].name < units[j].nets[0].name })
			out[g] = units
			names = append(names, g)
		}
	}
	sort.Strings(names)
	return out, names
}

// tuneGroups lengthens every unit of a length group that is shorter than
// the group's longest by more than the tolerance. A pair gets the same
// serpentine length on both members; when either member cannot take it the
// pair is restored, never left with a new intra-pair skew.
func (r *router) tuneGroups(outs map[*rnet]*netOut, res *RouteResult) {
	groups, names := r.lengthGroups()
	for _, g := range names {
		units := groups[g]
		hc := ClassifyHS(units[0].nets[0].plan)
		if hc == nil || hc.GroupSkewMil <= 0 {
			continue
		}
		tol := hc.GroupSkewMil
		target := 0.0
		for _, u := range units {
			target = math.Max(target, r.unitLen(u, outs))
		}
		tuned, short := 0, 0
		for _, u := range units {
			l := r.unitLen(u, outs)
			if target-l <= tol {
				continue
			}
			need := target - l - tol/4
			rem := need
			for attempt := 0; attempt < 8 && rem > tol*0.75; attempt++ {
				chunk := rem
				for chunk >= 15 && !r.meanderUnit(u, outs, chunk) {
					chunk /= 2
				}
				if chunk < 15 {
					break
				}
				rem -= chunk
			}
			if rem <= tol*0.75 {
				tuned++
				res.Stats.Tuned += len(u.nets)
			} else {
				short++
				res.Notes = append(res.Notes, sprintf("length group %s: %s only partly tuned, %.0f of %.0f mil added (group tolerance %.0f mil): not enough straight runs with room", g, u.name(), need-rem, need, tol))
			}
		}
		if tuned > 0 {
			res.Notes = append(res.Notes, sprintf("length group %s: %d unit(s) tuned toward %.0f mil (tolerance %.0f mil)", g, tuned, target, tol))
		}
	}
}

// meanderUnit adds need mil to every net of u, all or nothing.
func (r *router) meanderUnit(u tuneUnit, outs map[*rnet]*netOut, need float64) bool {
	saved := make([][]Track, len(u.nets))
	for i, n := range u.nets {
		saved[i] = append([]Track(nil), outs[n].tracks...)
	}
	for i, n := range u.nets {
		if r.meander(n, outs[n], need) {
			continue
		}
		for j := 0; j < i; j++ {
			m := u.nets[j]
			r.applyClaims(m.claims, -1)
			outs[m].tracks = saved[j]
			r.reclaim(m, outs[m])
		}
		return false
	}
	return true
}

// meander adds `need` mil to net n by replacing one straight segment with a
// serpentine of equal-height bumps, trying both sides, all copper checked.
func (r *router) meander(n *rnet, out *netOut, need float64) bool {
	r.applyClaims(n.claims, -1)
	idx := make([]int, 0, len(out.tracks))
	for i, t := range out.tracks {
		if t.Kind == "route" {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(a, b int) bool {
		return out.tracks[idx[a]].A.Dist(out.tracks[idx[a]].B) > out.tracks[idx[b]].A.Dist(out.tracks[idx[b]].B)
	})
	for _, i := range idx {
		t := out.tracks[i]
		li := r.gr.layerIndex(t.Layer)
		L := t.A.Dist(t.B)
		pitch := t.Width + math.Max(r.b.Rules.Clearance, n.plan.ClearanceMil) + r.gr.g
		leg := 2 * pitch // spacing between parallel serpentine legs
		ampMax := math.Min(10*pitch, 250)
		u := t.B.Sub(t.A).Scale(1 / L)
		// Small corrections: one 45° trapezoid bump adds 2h(√2−1) ≈ 0.83h
		// and needs far less room than a full serpentine.
		if h := need / (2 * (math.Sqrt2 - 1)); need < 2*(t.Width+pitch) && h <= ampMax {
			top := 2 * t.Width
			span := 2*h + top
			if span+2*pitch <= L {
				for _, side := range []float64{1, -1} {
					v := Point{-u.Y, u.X}.Scale(side)
					s := t.A.Add(u.Scale((L - span) / 2))
					p1 := s.Add(u.Scale(h)).Add(v.Scale(h))
					p2 := p1.Add(u.Scale(top))
					e := p2.Add(u.Scale(h)).Sub(v.Scale(h))
					if r.tryReplace(n, out, i, li, []Point{t.A, s, p1, p2, e, t.B}) {
						return true
					}
				}
			}
			continue
		}
		periods := int(math.Ceil(need / (2 * ampMax)))
		for ; periods <= 20; periods++ {
			amp := need / (2 * float64(periods))
			if amp < t.Width+pitch {
				break
			}
			used := float64(periods) * 2 * leg
			if used+2*pitch > L {
				break
			}
			for _, side := range []float64{1, -1} {
				v := Point{-u.Y, u.X}.Scale(side)
				pts := []Point{t.A}
				cur := t.A.Add(u.Scale((L - used) / 2))
				pts = append(pts, cur)
				for k := 0; k < periods; k++ {
					p1 := cur.Add(v.Scale(amp))
					p2 := p1.Add(u.Scale(leg))
					p3 := p2.Sub(v.Scale(amp))
					cur = p3.Add(u.Scale(leg))
					pts = append(pts, p1, p2, p3, cur)
				}
				pts = append(pts, t.B)
				if r.tryReplace(n, out, i, li, pts) {
					return true
				}
			}
		}
	}
	r.applyClaims(n.claims, +1)
	return false
}

// tryReplace swaps track i of out for the polyline pts when every new
// segment clears everything (n's own claims are already lifted); on success
// n is re-claimed from its new copper.
func (r *router) tryReplace(n *rnet, out *netOut, i, li int, pts []Point) bool {
	t := out.tracks[i]
	for q := 1; q < len(pts); q++ {
		if !r.segmentOK(n, li, pts[q-1], pts[q], t.Width, true) {
			return false
		}
	}
	var nt []Track
	nt = append(nt, out.tracks[:i]...)
	for q := 1; q < len(pts); q++ {
		if pts[q].Dist(pts[q-1]) > 1e-6 {
			nt = append(nt, Track{Net: t.Net, Layer: t.Layer, A: pts[q-1], B: pts[q], Width: t.Width, Kind: "route"})
		}
	}
	nt = append(nt, out.tracks[i+1:]...)
	out.tracks = nt
	r.reclaim(n, out)
	return true
}

// reclaim recomputes n's routed claims from its emitted copper.
func (r *router) reclaim(n *rnet, out *netOut) {
	var cl []int32
	for _, t := range out.tracks {
		cl = r.claimSegment(n, r.gr.layerIndex(t.Layer), t.A, t.B, t.Width, cl)
	}
	for _, v := range out.vias {
		x, y := r.gr.cellOf(v.C)
		cl = r.claimVia(n, int32(r.gr.idx(0, x, y)), cl)
	}
	n.claims = r.minusFixed(n, dedup(cl))
	r.applyClaims(n.claims, +1)
}

// recoupleSkew is the intra-pair mismatch (in multiples of the interface
// limit, at least recoupleMinMil) past which a serpentine is the wrong fix:
// the members took different routes and one is re-routed along the other.
const (
	recoupleFactor = 4
	recoupleMinMil = 40
	recoupleFac    = 0.25 // pair-field cost factor of the re-route (default 0.55)
	recoupleBudget = 3 * time.Second
)

// recouple re-routes the longer member of an intent high-speed pair whose
// members diverged (skew far past the limit — one detoured, took another
// layer or went round a part) strictly along its partner, with a stronger
// pair field. The new route is kept only when it connects everything, adds
// no via and cuts the skew; otherwise the old copper is restored exactly.
// Pairs without intent (no interface) are left as routed.
func (r *router) recouple(outs map[*rnet]*netOut, res *RouteResult) {
	done := map[*rnet]bool{}
	for _, n := range r.nets {
		p := r.byName[n.plan.PairWith]
		if p == nil || done[n] || len(n.paths) == 0 || len(p.paths) == 0 || n.plan.Interface == "" {
			continue
		}
		done[n], done[p] = true, true
		hc := ClassifyHS(n.plan)
		if hc == nil || hc.MaxSkewMil <= 0 {
			continue
		}
		ln, lp := r.netCopperLen(n, outs[n]), r.netCopperLen(p, outs[p])
		skew := math.Abs(ln - lp)
		if skew <= math.Max(recoupleFactor*hc.MaxSkewMil, recoupleMinMil) {
			continue
		}
		long, short := n, p
		if lp > ln {
			long, short = p, n
		}
		lshort := r.netCopperLen(short, outs[short])
		savedPaths, savedClaims, savedOut, savedFailed := long.paths, long.claims, outs[long], long.failed
		oldVias := len(savedOut.vias)
		deadline, strict, fac := r.deadline, r.strict, r.pairFac
		r.deadline, r.strict, r.pairFac = time.Now().Add(recoupleBudget), true, recoupleFac
		ok := r.routeNet(long) && len(long.failed) == 0
		var out *netOut
		if ok {
			out = r.emitNet(long)
		}
		r.deadline, r.strict, r.pairFac = deadline, strict, fac
		if ok {
			nl := r.netCopperLen(long, out)
			if ns := math.Abs(nl - lshort); ns < skew-1 && len(out.vias) <= oldVias {
				outs[long] = out
				res.Notes = append(res.Notes, sprintf("re-coupled %s along %s: skew %.0f → %.0f mil (limit %.0f), vias %d → %d", long.name, short.name, skew, ns, hc.MaxSkewMil, oldVias, len(out.vias)))
				continue
			}
		}
		// Restore the previous copper exactly.
		r.applyClaims(long.claims, -1)
		long.paths, long.claims, long.failed = savedPaths, savedClaims, savedFailed
		r.applyClaims(long.claims, +1)
		outs[long] = savedOut
	}
}
