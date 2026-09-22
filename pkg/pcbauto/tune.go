package pcbauto

import (
	"math"
	"sort"
)

func trackLen(ts []Track) float64 {
	s := 0.0
	for _, t := range ts {
		s += t.A.Dist(t.B)
	}
	return s
}

// tuneLengths equalises differential pairs whose intra-pair skew exceeds
// their interface limit by inserting a serpentine on the shorter member.
func (r *router) tuneLengths(outs map[*rnet]*netOut, res *RouteResult) {
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
		ln, lp := trackLen(outs[n].tracks), trackLen(outs[p].tracks)
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
				ln, lp, trackLen(outs[n].tracks), trackLen(outs[p].tracks), hc.MaxSkewMil))
		} else {
			res.Notes = append(res.Notes, sprintf("tuned %s/%s only partly: %.0f of %.0f mil added (limit %.0f): not enough straight runs with room", n.name, p.name, need-rem, need, hc.MaxSkewMil))
		}
	}
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
