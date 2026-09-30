package pcbauto

import (
	"math"
	"sort"
)

// Differential-pair coupling (CheckSI, the pair router's acceptance test).
//
// A pair is a transmission line only where its two legs run side by side at
// the target gap on the same layer; skew alone (the old check) passes a
// pair whose legs take different routes, layers and via counts but happen
// to be equally long (ESP32 mini USB_DP/DM, 2026-09: 0 vs 2 vias, no
// stretch at the 6 mil gap, reported clean). The measures, per leg:
//
//   - a sample of the leg is coupled when its nearest partner copper on the
//     same layer lies at the pair pitch (width/2 + gap + width/2) within a
//     tolerance;
//   - the breakout zone of an end is the disc of the class breakout budget
//     round the pads of that end (pads of both legs within 150 mil form one
//     end: a connector, an ESD array, a package — or several of them side by
//     side, each with its own breakout: the budget of an end is the class
//     budget × the parts it holds);
//   - coupled share: coupled / length over the body — the leg outside every
//     breakout zone; the pair's share is the smaller leg's;
//   - uncoupled at the ends: the uncoupled length of a leg inside one end's
//     zone, against the class breakout budget (the legs must join within the
//     budget of the pins);
//   - via symmetry: both legs change layers through the same number of vias;
//   - layer symmetry: both legs use the same set of layers.
//
// Intra-pair skew stays in CheckSI (ClassifyHS tolerance).

// PairCoupling is the measured coupling of one differential pair.
type PairCoupling struct {
	GapMil float64 `json:"gapMil"` // target edge-to-edge gap
	TolMil float64 `json:"tolMil"` // ± tolerance on the gap counted as coupled
	// CoupledPct is the smaller leg's coupled share of its body (outside the
	// breakout zones); BodyMil the longer body (0 = all breakout, not judged).
	CoupledPct float64 `json:"coupledPct"`
	BodyMil    float64 `json:"bodyMil"`
	// CoupledPMil / CoupledNMil are each leg's coupled length (whole leg).
	CoupledPMil float64 `json:"coupledPMil"`
	CoupledNMil float64 `json:"coupledNMil"`
	// UncoupledMil is the uncoupled length of a leg inside the end's
	// breakout zone that is worst against its budget, BreakoutMil that
	// end's budget (class budget × parts at the end).
	UncoupledMil float64 `json:"uncoupledMil"`
	BreakoutMil  float64 `json:"breakoutMil"`
	Ends         int     `json:"ends"`
	ViasP        int     `json:"viasP"`
	ViasN        int     `json:"viasN"`
	LayersP      []int   `json:"layersP,omitempty"`
	LayersN      []int   `json:"layersN,omitempty"`
}

// pairGapTol is the gap deviation still counted as coupled: 40 % of the
// gap, at least 2 mil (a grid router places the partner within ±0.7 cell of
// the pitch). This decides whether a stretch is coupled at all; the
// impedance target itself is the gap (stackup / intent).
func pairGapTol(gap float64) float64 { return math.Max(0.4*gap, 2) }

// pairGap is the target edge-to-edge gap of a pair plan.
func pairGap(np *NetPlan, ru Rules) float64 {
	if np.PairGapMil > 0 {
		return np.PairGapMil
	}
	return ru.Clearance
}

// pairMinBodyMil is the shortest body the coupled share is judged on.
const pairMinBodyMil = 50

// legStats is one leg measured against its partner.
type legStats struct {
	length, coupled   float64
	body, bodyCoupled float64
	uncAtEnd          []float64 // uncoupled length inside each end's zone
}

// measureLeg samples legA every ≤ 4 mil against legB.
func measureLeg(legA, legB []Track, gap, tol float64, ends [][]*Pad, zone float64) legStats {
	byLayer := map[int][]Track{}
	for _, t := range legB {
		byLayer[t.Layer] = append(byLayer[t.Layer], t)
	}
	st := legStats{uncAtEnd: make([]float64, len(ends))}
	endOf := func(q Point) int {
		best, bi := math.Inf(1), -1
		for e, ps := range ends {
			for _, pd := range ps {
				if d := pd.Box.Dist(q); d < best {
					best, bi = d, e
				}
			}
		}
		if best <= zone {
			return bi
		}
		return -1
	}
	for _, t := range legA {
		L := t.A.Dist(t.B)
		if L < 1e-6 {
			continue
		}
		bs := byLayer[t.Layer]
		k := int(math.Ceil(L / 4))
		step := L / float64(k)
		for s := 0; s < k; s++ {
			q := t.A.Add(t.B.Sub(t.A).Scale((float64(s) + 0.5) / float64(k)))
			coupled := false
			if len(bs) > 0 {
				best, bw := math.Inf(1), 0.0
				for _, o := range bs {
					if d := PointSegDist(q, o.A, o.B); d < best {
						best, bw = d, o.Width
					}
				}
				coupled = math.Abs(best-((t.Width+bw)/2+gap)) <= tol
			}
			st.length += step
			e := endOf(q)
			if coupled {
				st.coupled += step
			}
			switch {
			case e < 0:
				st.body += step
				if coupled {
					st.bodyCoupled += step
				}
			case !coupled:
				st.uncAtEnd[e] += step
			}
		}
	}
	return st
}

// pairEndClusters groups the pads of a pair (both legs, single link at
// 150 mil): each group is one end with its own breakout budget.
func pairEndClusters(pads []*Pad) [][]*Pad {
	parent := make([]int, len(pads))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	for i := range pads {
		for j := i + 1; j < len(pads); j++ {
			if pads[i].Box.C.Dist(pads[j].Box.C) <= 150 {
				parent[find(i)] = find(j)
			}
		}
	}
	idx := map[int]int{}
	var out [][]*Pad
	for i, pd := range pads {
		r := find(i)
		k, ok := idx[r]
		if !ok {
			k = len(out)
			idx[r] = k
			out = append(out, nil)
		}
		out[k] = append(out[k], pd)
	}
	return out
}

// layersOf is the sorted set of layers carrying ≥ 10 mil of a leg (a
// stub inside a pad is not a layer change).
func layersOf(ts []Track) []int {
	per := map[int]float64{}
	for _, t := range ts {
		per[t.Layer] += t.A.Dist(t.B)
	}
	var out []int
	for l, v := range per {
		if v >= 10 {
			out = append(out, l)
		}
	}
	sort.Ints(out)
	return out
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// MeasurePairCoupling measures the pair routed as tracks tp/tn with vp/vn
// vias, on pads (both legs' pads), against the target gap and the class's
// breakout budget (hc nil: the sub-Gb/s default).
func MeasurePairCoupling(tp, tn []Track, vp, vn int, pads []*Pad, gap float64, hc *HSClass) PairCoupling {
	budget := float64(breakoutSlow)
	if hc != nil && hc.BreakoutMil > 0 {
		budget = hc.BreakoutMil
	}
	ends := pairEndClusters(pads)
	tol := pairGapTol(gap)
	pc := PairCoupling{GapMil: round2(gap), TolMil: round2(tol), ViasP: vp, ViasN: vn, BreakoutMil: budget,
		LayersP: layersOf(tp), LayersN: layersOf(tn), Ends: len(ends)}
	endBudget := make([]float64, len(ends))
	for e, ps := range ends {
		parts := map[string]bool{}
		for _, pd := range ps {
			parts[pd.Part] = true
		}
		endBudget[e] = budget * float64(max(len(parts), 1))
	}
	sp, sn := measureLeg(tp, tn, gap, tol, ends, budget), measureLeg(tn, tp, gap, tol, ends, budget)
	pc.CoupledPMil, pc.CoupledNMil = math.Round(sp.coupled), math.Round(sn.coupled)
	pc.BodyMil = math.Round(math.Max(sp.body, sn.body))
	share, worst := 1.0, -1.0
	for _, s := range []legStats{sp, sn} {
		if s.body >= pairMinBodyMil {
			share = math.Min(share, s.bodyCoupled/s.body)
		}
		for e, u := range s.uncAtEnd {
			if u/endBudget[e] > worst {
				worst, pc.UncoupledMil, pc.BreakoutMil = u/endBudget[e], u, endBudget[e]
			}
		}
	}
	if pc.BodyMil < pairMinBodyMil {
		share = 1
	}
	pc.CoupledPct = round2(100 * share)
	pc.UncoupledMil = math.Round(pc.UncoupledMil)
	return pc
}

// pairFindings turns a measured pair into SI findings.
func pairFindings(name string, pc PairCoupling, hc *HSClass) []SIFinding {
	var out []SIFinding
	if pc.ViasP != pc.ViasN {
		out = append(out, SIFinding{Net: name, Kind: "via-asymmetry", Value: math.Abs(float64(pc.ViasP - pc.ViasN)), Limit: 0,
			Fix: sprintf("the legs change layers through %d and %d vias: route the pair as a unit — the vias in a symmetric pair at the same transition, or both legs on one layer", pc.ViasP, pc.ViasN)})
	}
	if !sameInts(pc.LayersP, pc.LayersN) {
		out = append(out, SIFinding{Net: name, Kind: "layer-asymmetry", Value: float64(len(pc.LayersP) - len(pc.LayersN)), Limit: 0,
			Fix: sprintf("the legs run on layers %v and %v: keep both on the same layer sequence", pc.LayersP, pc.LayersN)})
	}
	if hc == nil || hc.MinCoupledPct <= 0 {
		return out
	}
	if pc.BodyMil >= pairMinBodyMil && pc.CoupledPct < hc.MinCoupledPct {
		out = append(out, SIFinding{Net: name, Kind: "coupling", Value: pc.CoupledPct, Limit: hc.MinCoupledPct,
			Fix: sprintf("only %.0f %% of the pair's %.0f mil body (outside the breakout zones) runs coupled at the %.1f mil gap (±%.1f): route the legs side by side — clear the corridor between the endpoints in placement", pc.CoupledPct, pc.BodyMil, pc.GapMil, pc.TolMil)})
	}
	if pc.UncoupledMil > pc.BreakoutMil {
		out = append(out, SIFinding{Net: name, Kind: "uncoupled", Value: pc.UncoupledMil, Limit: pc.BreakoutMil,
			Fix: sprintf("a leg runs %.0f mil uncoupled near one end, its breakout budget is %.0f mil (%.0f mil per part at that end): join the legs right after the pins", pc.UncoupledMil, pc.BreakoutMil, hc.BreakoutMil)})
	}
	return out
}
