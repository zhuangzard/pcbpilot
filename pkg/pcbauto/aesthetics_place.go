package pcbauto

// Placement aesthetics P1–P9 ([plan] §4). See aesthetics.go for the source
// tags used in the threshold comments.

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// aesMechRe: mechanical / enclosure-anchored designators, excluded from the
// grid and alignment criteria ([tidy] pcbTidyMechRe, conventions §9.1).
var aesMechRe = regexp.MustCompile(`(?i)^(H|MH|MK|MARK|FID|LOGO)\d*$`)

type aPart struct {
	p      *Part
	kind   PartKind
	class  string // kind | pad count | footprint size signature
	c      Point  // body centre
	size   float64
	polar  bool
	mech   bool
	bodyWH [2]float64
}

func aesParts(b *Board, c *Circuit) []*aPart {
	var out []*aPart
	for _, p := range b.Parts {
		k := c.Kinds[p.Ref]
		if k == "" {
			k = ClassifyPart(p)
		}
		bd := p.Body()
		// footprint signature in the part's own frame (rotation-free)
		w, h := bd.W(), bd.H()
		if r := math.Mod(normDeg(p.Rotation), 180); math.Abs(r-90) < 1 {
			w, h = h, w
		}
		ap := &aPart{p: p, kind: k, c: bd.Center(), size: math.Max(bd.W(), bd.H()), bodyWH: [2]float64{w, h}}
		ap.class = fmt.Sprintf("%s|%d|%.0fx%.0f", k, len(p.Pads), math.Round(w/10)*10, math.Round(h/10)*10)
		ap.polar = !symmetricPassive(p)
		ap.mech = k == KindMechanical || aesMechRe.MatchString(p.Ref)
		out = append(out, ap)
	}
	return out
}

func aesPlacement(rep *AestheticsReport, b *Board, an *Analysis, c *Circuit, in AesInput) {
	parts := aesParts(b, c)
	byClass := map[string][]*aPart{}
	for _, ap := range parts {
		if !ap.mech {
			byClass[ap.class] = append(byClass[ap.class], ap)
		}
	}
	classes := make([]string, 0, len(byClass))
	for k := range byClass {
		classes = append(classes, k)
	}
	sort.Strings(classes)

	pr := in.Profile
	rep.Metrics = append(rep.Metrics, aesP1(byClass, classes, pr.AlignTolMil, pr.NearMissTolMil))
	rep.Metrics = append(rep.Metrics, aesP2(byClass, classes, pr.AlignTolMil))
	rep.Metrics = append(rep.Metrics, aesP3(byClass, classes))
	groups := DetectSymmetry(b, an, c)
	if groups == nil {
		groups = []SymmetryGroup{}
	}
	rep.Symmetry = groups
	rep.Metrics = append(rep.Metrics, aesP4(groups, pr.SymmetryRequired))
	rep.Metrics = append(rep.Metrics, aesP5(b, c))
	rep.Metrics = append(rep.Metrics, aesP6(b, parts))
	rep.Metrics = append(rep.Metrics, aesP7(b, parts))
	rep.Metrics = append(rep.Metrics, aesP8(b, in))
	rep.Metrics = append(rep.Metrics, aesP9(parts, pr.PlacementGridMil, pr.GridBlend))
}

// Alignment tolerances come from the style profile. Balanced: 2 mil is "on
// the same line" ([tidy] pcbTidyArrayAxisTolMil, conventions §9.2; [fable]
// P1 eps 2 mil); up to 15 mil off is the "almost aligned" band ([plan] P1
// eps 15 mil) — the near misses that read worst and that Phase B's snap
// will pull onto a line.

// P1: share of same-class parts that share no row/column with a same-class
// neighbour (within 3 body sizes — [agent] P3 neighbourhood).
func aesP1(byClass map[string][]*aPart, classes []string, aesAlignTol, aesNearMissTol float64) AesMetric {
	m := AesMetric{ID: "P1", Group: "placement", Name: "row/column collinearity", Unit: "unaligned share"}
	considered, unaligned, near := 0, 0, 0
	var worst []AesOffender
	for _, k := range classes {
		ps := byClass[k]
		if len(ps) < 2 {
			continue
		}
		for _, a := range ps {
			hasNb, aligned := false, false
			bestNear := math.Inf(1)
			nearRef := ""
			for _, q := range ps {
				if q == a || q.p.Side != a.p.Side {
					continue
				}
				if a.c.Dist(q.c) > 3*math.Max(a.size, q.size) {
					continue
				}
				hasNb = true
				dx, dy := math.Abs(a.c.X-q.c.X), math.Abs(a.c.Y-q.c.Y)
				if dx <= aesAlignTol || dy <= aesAlignTol {
					aligned = true
					break
				}
				for _, d := range []float64{dx, dy} {
					if d <= aesNearMissTol && d < bestNear {
						bestNear, nearRef = d, q.p.Ref
					}
				}
			}
			if !hasNb {
				continue
			}
			considered++
			if aligned {
				continue
			}
			unaligned++
			o := AesOffender{Ref: a.p.Ref, At: ptr(a.c), Value: 1, Note: "no same-class neighbour on a common row/column"}
			if nearRef != "" {
				near++
				o.Value = 2 + (aesNearMissTol-bestNear)/aesNearMissTol // near misses first
				o.Note = fmt.Sprintf("%.1f mil off %s's row/column (near miss)", bestNear, nearRef)
			}
			worst = append(worst, o)
		}
	}
	if considered == 0 {
		m.Skipped, m.Reason = true, "no same-class parts near each other"
		return m
	}
	r := float64(unaligned) / float64(considered)
	m.Value = round3(r)
	// ramp: unaligned 10 % → 100, 60 % → 0 ([agent] P3: aligned 90 % → 100,
	// 40 % → 0; [fable] P1 zero at r ≥ 0.5).
	m.Score = aesRound(aesRamp(r, 0.1, 0.6))
	m.Detail = fmt.Sprintf("%d/%d parts with a same-class neighbour share no row/column with one (%d near misses ≤ %.0f mil)", unaligned, considered, near, aesNearMissTol)
	m.Extra = map[string]float64{"considered": float64(considered), "unaligned": float64(unaligned), "nearMiss": float64(near)}
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}

type aRow struct {
	refs  []string
	steps []float64
	cv    float64
	axis  string
}

// aesRows finds rows (common y) and columns (common x) of ≥3 same-class
// parts with steps ≤ 500 mil ([tidy] pcbTidyArrayMaxStepMil).
func aesRows(byClass map[string][]*aPart, classes []string, aesAlignTol float64) []aRow {
	var rows []aRow
	for _, k := range classes {
		ps := byClass[k]
		if len(ps) < 3 {
			continue
		}
		for _, axis := range []string{"row", "column"} {
			key := func(a *aPart) (along, across float64) {
				if axis == "row" {
					return a.c.X, a.c.Y
				}
				return a.c.Y, a.c.X
			}
			sorted := append([]*aPart(nil), ps...)
			sort.Slice(sorted, func(i, j int) bool {
				_, ai := key(sorted[i])
				_, aj := key(sorted[j])
				if ai != aj {
					return ai < aj
				}
				return sorted[i].p.Ref < sorted[j].p.Ref
			})
			for i := 0; i < len(sorted); {
				j := i + 1
				_, a0 := key(sorted[i])
				for j < len(sorted) {
					_, aj := key(sorted[j])
					if aj-a0 > aesAlignTol {
						break
					}
					j++
				}
				line := append([]*aPart(nil), sorted[i:j]...)
				i = j
				if len(line) < 3 {
					continue
				}
				sort.Slice(line, func(x, y int) bool { lx, _ := key(line[x]); ly, _ := key(line[y]); return lx < ly })
				run := []*aPart{line[0]}
				flush := func() {
					if len(run) >= 3 {
						r := aRow{axis: axis}
						for q := range run {
							r.refs = append(r.refs, run[q].p.Ref)
							if q > 0 {
								l0, _ := key(run[q-1])
								l1, _ := key(run[q])
								r.steps = append(r.steps, l1-l0)
							}
						}
						mu, sd := meanSD(r.steps)
						if mu > 0 {
							r.cv = sd / mu
						}
						rows = append(rows, r)
					}
				}
				for q := 1; q < len(line); q++ {
					l0, _ := key(line[q-1])
					l1, _ := key(line[q])
					if l1-l0 > 500 || line[q].p.Side != line[q-1].p.Side {
						flush()
						run = nil
					}
					run = append(run, line[q])
				}
				flush()
			}
		}
	}
	return rows
}

func meanSD(v []float64) (float64, float64) {
	if len(v) == 0 {
		return 0, 0
	}
	mu := mean(v)
	s := 0.0
	for _, x := range v {
		s += (x - mu) * (x - mu)
	}
	return mu, math.Sqrt(s / float64(len(v)))
}

// P2: pitch CV of rows/columns of ≥3 same-class parts.
func aesP2(byClass map[string][]*aPart, classes []string, alignTol float64) AesMetric {
	m := AesMetric{ID: "P2", Group: "placement", Name: "array pitch regularity", Unit: "step-weighted pitch CV"}
	rows := aesRows(byClass, classes, alignTol)
	if len(rows) == 0 {
		m.Skipped, m.Reason = true, "no row/column of ≥3 aligned same-class parts"
		return m
	}
	var sw, s float64
	var worst []AesOffender
	for _, r := range rows {
		w := float64(len(r.steps))
		sw += w
		s += w * r.cv
		if r.cv > 0.005 {
			worst = append(worst, AesOffender{Ref: strings.Join(r.refs, ","), Value: round3(r.cv),
				Note: fmt.Sprintf("%s steps %s mil", r.axis, fmtSteps(r.steps))})
		}
	}
	cv := s / sw
	m.Value = round3(cv)
	// ramp: CV 0.02 → 100, 0.15 → 0 ([agent] P4, [fable] P2).
	m.Score = aesRound(aesRamp(cv, 0.02, 0.15))
	m.Detail = fmt.Sprintf("%d rows/columns of ≥3 same-class parts, weighted pitch CV %.3f", len(rows), cv)
	m.Extra = map[string]float64{"rows": float64(len(rows))}
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}

func fmtSteps(v []float64) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = fmt.Sprintf("%.1f", x)
	}
	return strings.Join(s, "/")
}

// rotKey is the orientation class: mod 180 for symmetric passives, mod 360
// otherwise, snapped to 90° within ±1° ([tidy] pcbTidyRotSnapDeg).
// silkRot normalises a silkscreen rotation. Dumps carry degrees (360,
// 1080) and, on some boards, tenths of a degree (K230: −900 = −90°): a
// value beyond ±360 whose tenth is a multiple of 90 is read as tenths.
func silkRot(r float64) float64 {
	if math.Abs(r) > 360 && math.Mod(r/10, 90) == 0 {
		r /= 10
	}
	return normDeg(r)
}

func rotKey(a *aPart) float64 {
	r := normDeg(a.p.Rotation)
	if s := 90 * math.Round(r/90); math.Abs(r-s) <= 1 {
		r = normDeg(s)
	}
	if !a.polar {
		r = math.Mod(r, 180)
	}
	return r
}

// P3: majority orientation share in same-class groups of ≥3 ([tidy]
// pcbTidyMinGroup); polar parts are judged mod 360, symmetric passives mod
// 180 with a light penalty for 180/270 ([tidy] pcbTidyOddRotFactor 0.3).
func aesP3(byClass map[string][]*aPart, classes []string) AesMetric {
	m := AesMetric{ID: "P3", Group: "placement", Name: "orientation consistency", Unit: "majority orientation share"}
	var nTot, majTot, pN, pMaj, sN, sMaj, odd int
	var worst []AesOffender
	for _, k := range classes {
		ps := byClass[k]
		if len(ps) < 3 {
			continue
		}
		cnt := map[float64]int{}
		for _, a := range ps {
			cnt[rotKey(a)]++
		}
		major, best := 0.0, -1
		keys := make([]float64, 0, len(cnt))
		for r := range cnt {
			keys = append(keys, r)
		}
		sort.Float64s(keys)
		for _, r := range keys {
			if cnt[r] > best {
				major, best = r, cnt[r]
			}
		}
		nTot += len(ps)
		majTot += best
		if ps[0].polar {
			pN += len(ps)
			pMaj += best
		} else {
			sN += len(ps)
			sMaj += best
		}
		for _, a := range ps {
			if !a.polar && normDeg(a.p.Rotation) >= 180-1 {
				odd++
			}
			if rotKey(a) != major {
				worst = append(worst, AesOffender{Ref: a.p.Ref, At: ptr(a.c), Value: float64(len(ps)),
					Note: fmt.Sprintf("rotation %.0f vs group majority %.0f (%s, n=%d)", a.p.Rotation, major, k, len(ps))})
			}
		}
	}
	if nTot == 0 {
		m.Skipped, m.Reason = true, "no same-class group of ≥3 parts"
		return m
	}
	share := float64(majTot) / float64(nTot)
	m.Value = round3(share)
	// ramp: majority share 100 % → 100, 60 % → 0 (initial; [agent] P2 /
	// [tidy] rotation-inconsistent); 180/270 on symmetric passives costs
	// 30 points × their share ([tidy] pcbTidyOddRotFactor).
	sc := aesRamp(-share, -1, -0.6)
	if sN > 0 {
		sc -= 30 * float64(odd) / float64(sN)
	}
	m.Score = aesRound(clamp(sc, 0, 100))
	ratio := func(a, b int) float64 {
		if b == 0 {
			return 1
		}
		return round3(float64(a) / float64(b))
	}
	m.Detail = fmt.Sprintf("majority orientation %d/%d (polar %d/%d mod 360, symmetric passives %d/%d mod 180); %d symmetric passives at 180/270",
		majTot, nTot, pMaj, pN, sMaj, sN, odd)
	m.Extra = map[string]float64{"polarShare": ratio(pMaj, pN), "symmetricShare": ratio(sMaj, sN), "oddRotations": float64(odd)}
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}

// P4: symmetry of the detected isomorphic sub-circuits.
func aesP4(groups []SymmetryGroup, required bool) AesMetric {
	m := AesMetric{ID: "P4", Group: "placement", Name: "symmetry of isomorphic sub-circuits", Unit: "mean normalised mirror error"}
	if len(groups) == 0 {
		m.Skipped, m.Reason = true, "no isomorphic sub-circuits detected"
		return m
	}
	var sw, se, ss float64
	var worst []AesOffender
	for _, g := range groups {
		w := float64(len(g.Instances[0]))
		sw += w
		se += w * g.Error
		ss += w * g.Score
		worst = append(worst, AesOffender{Ref: strings.Join(flatten(g.Instances), ","), Value: round3(g.Error),
			Note: fmt.Sprintf("%s %s, best %s", g.Kind, g.Signature, g.Axis.Type)})
	}
	m.Value = round3(se / sw)
	m.Score = aesRound(ss / sw)
	m.Detail = fmt.Sprintf("%d isomorphic groups; mean mirror error %.3f of module span", len(groups), se/sw)
	if required {
		// Profile requires symmetry wherever it was detected: the worst
		// group governs instead of the mean.
		min := 100.0
		for _, g := range groups {
			min = math.Min(min, g.Score)
		}
		m.Score = aesRound(min)
		m.Detail += "; symmetry required: worst group governs"
	}
	m.Extra = map[string]float64{"groups": float64(len(groups))}
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}

func flatten(v [][]string) []string {
	var out []string
	for _, x := range v {
		out = append(out, strings.Join(x, "+"))
	}
	return out
}

// P5: block rectangularity = convex-hull area / bounding-box area of each
// functional block's part bodies ([plan] P5; ramp ≥0.75 → 100, ≤0.45 → 0
// from [fable] P5). Blocks of ≥3 parts, the catch-all misc block excluded.
func aesP5(b *Board, c *Circuit) AesMetric {
	m := AesMetric{ID: "P5", Group: "placement", Name: "block rectangularity", Unit: "mean hull/bbox area ratio"}
	var vals []float64
	var worst []AesOffender
	for _, bl := range c.Blocks {
		if bl.Kind == "misc" || len(bl.Parts) < 3 {
			continue
		}
		var pts []Point
		bb := EmptyRect()
		var fill float64
		for _, ref := range bl.Parts {
			p := b.Part(ref)
			if p == nil {
				continue
			}
			r := p.Body()
			pts = append(pts, r.Corners()...)
			bb = bb.Union(r)
			fill += r.Area()
		}
		if bb.Area() <= 0 || len(pts) < 3 {
			continue
		}
		hull := convexHull(pts)
		ratio := PolyArea(hull) / bb.Area()
		vals = append(vals, ratio)
		worst = append(worst, AesOffender{Ref: bl.Core, Value: round3(1 - ratio),
			Note: fmt.Sprintf("block %s (%d parts) hull/bbox %.2f, body fill %.2f", bl.ID, len(bl.Parts), ratio, fill/PolyArea(hull))})
	}
	if len(vals) == 0 {
		m.Skipped, m.Reason = true, "no functional block of ≥3 parts"
		return m
	}
	mu := mean(vals)
	m.Value = round3(mu)
	sc := 0.0
	for _, v := range vals {
		sc += aesRamp(-v, -0.75, -0.45)
	}
	m.Score = aesRound(sc / float64(len(vals)))
	m.Detail = fmt.Sprintf("%d blocks, mean hull/bbox %.2f", len(vals), mu)
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}

// aesEdgeBand: a part whose body lies within 150 mil (≈3.8 mm) of the
// outline reads as "on the edge" (initial, to be calibrated).
const aesEdgeBand = 150.0

// aesEdgeRow: only the first row along an edge is judged — parts within
// 60 mil of the closest part on that edge (≈ one 0603 body plus spacing);
// the second row behind it is not an edge alignment.
const aesEdgeRow = 60.0

// P6: CV of the body-to-edge distance of the first row of parts along each
// outline edge ([plan] P6; ramp CV 0.05 → 100, 0.3 → 0 from [fable] P6). The
// mean is floored at 10 mil so flush connectors do not blow the CV up.
func aesP6(b *Board, parts []*aPart) AesMetric {
	m := AesMetric{ID: "P6", Group: "placement", Name: "edge-margin consistency", Unit: "mean per-edge margin CV"}
	if len(b.Outline) < 3 {
		m.Skipped, m.Reason = true, "no board outline"
		return m
	}
	type em struct {
		ref string
		d   float64
	}
	per := map[int][]em{}
	for _, ap := range parts {
		if ap.mech || !PolyContains(b.Outline, ap.p.Body().Center()) {
			continue
		}
		bd := ap.p.Body()
		best, bi := math.Inf(1), -1
		for i := range b.Outline {
			a, c := b.Outline[i], b.Outline[(i+1)%len(b.Outline)]
			d := math.Inf(1)
			for _, q := range bd.Corners() {
				d = math.Min(d, PointSegDist(q, a, c))
			}
			if d < best {
				best, bi = d, i
			}
		}
		if bi >= 0 && best <= aesEdgeBand {
			per[bi] = append(per[bi], em{ap.p.Ref, best})
		}
	}
	var cvs []float64
	var worst []AesOffender
	edges := make([]int, 0, len(per))
	for e := range per {
		edges = append(edges, e)
	}
	sort.Ints(edges)
	for _, e := range edges {
		lo := math.Inf(1)
		for _, x := range per[e] {
			lo = math.Min(lo, x.d)
		}
		var v []float64
		var refs []string
		for _, x := range per[e] {
			if x.d <= lo+aesEdgeRow {
				v = append(v, x.d)
				refs = append(refs, x.ref)
			}
		}
		if len(v) < 2 {
			continue
		}
		mu, sd := meanSD(v)
		cv := sd / math.Max(mu, 10)
		cvs = append(cvs, cv)
		worst = append(worst, AesOffender{Ref: strings.Join(refs, ","), Value: round3(cv), Note: fmt.Sprintf("edge %d margins %s mil", e, fmtSteps(v))})
	}
	if len(cvs) == 0 {
		m.Skipped, m.Reason = true, "no outline edge with ≥2 parts in its first row"
		return m
	}
	mu := mean(cvs)
	m.Value = round3(mu)
	sc := 0.0
	for _, cv := range cvs {
		sc += aesRamp(cv, 0.05, 0.3)
	}
	m.Score = aesRound(sc / float64(len(cvs)))
	m.Detail = fmt.Sprintf("%d edges with ≥2 first-row parts, mean margin CV %.3f", len(cvs), mu)
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}

// P7: whitespace evenness — CV of the part-body occupancy of an 8×8 grid of
// board cells (cells at least half inside the outline), per side ([plan] P7,
// [agent] P6), sides weighted by part area. Ramp CV 0.6 → 100, 1.6 → 0:
// initial values (to be calibrated in Phase E).
func aesP7(b *Board, parts []*aPart) AesMetric {
	m := AesMetric{ID: "P7", Group: "placement", Name: "whitespace evenness", Unit: "occupancy CV (8×8 cells)"}
	bb := b.Bounds()
	if bb.Empty() || bb.Area() <= 0 {
		m.Skipped, m.Reason = true, "no board extent"
		return m
	}
	const n = 8
	cw, ch := bb.W()/n, bb.H()/n
	var cvs, ws []float64
	var worst []AesOffender
	for _, side := range []int{LayerTop, LayerBottom} {
		occ := make([]float64, n*n)
		area := 0.0
		populated := false
		for _, ap := range parts {
			if ap.p.Side != side || ap.mech {
				continue
			}
			populated = true
			r := ap.p.Body()
			area += r.Area()
			for i := 0; i < n; i++ {
				for j := 0; j < n; j++ {
					cell := Rect{bb.MinX + float64(i)*cw, bb.MinY + float64(j)*ch, bb.MinX + float64(i+1)*cw, bb.MinY + float64(j+1)*ch}
					occ[i*n+j] += r.OverlapArea(cell)
				}
			}
		}
		if !populated {
			continue
		}
		var v []float64
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				cell := Rect{bb.MinX + float64(i)*cw, bb.MinY + float64(j)*ch, bb.MinX + float64(i+1)*cw, bb.MinY + float64(j+1)*ch}
				if len(b.Outline) >= 3 && !PolyContains(b.Outline, cell.Center()) {
					continue
				}
				v = append(v, math.Min(1, occ[i*n+j]/cell.Area()))
			}
		}
		mu, sd := meanSD(v)
		if mu <= 0 {
			continue
		}
		cv := sd / mu
		cvs = append(cvs, cv)
		ws = append(ws, area)
		worst = append(worst, AesOffender{Ref: map[int]string{LayerTop: "TOP", LayerBottom: "BOTTOM"}[side], Value: round3(cv),
			Note: fmt.Sprintf("mean occupancy %.2f, CV %.2f over %d cells", mu, cv, len(v))})
	}
	if len(cvs) == 0 {
		m.Skipped, m.Reason = true, "no placed parts"
		return m
	}
	// sides weighted by their part-body area: a bottom side with three
	// parts must not outweigh the populated top
	mu, wt := 0.0, 0.0
	for i := range cvs {
		mu += ws[i] * cvs[i]
		wt += ws[i]
	}
	mu /= wt
	m.Value = round3(mu)
	m.Score = aesRound(aesRamp(mu, 0.6, 1.6))
	m.Detail = fmt.Sprintf("occupancy CV per populated side: %s", fmtSteps(cvs))
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}

// P8: designator silkscreen — same side of the body, ≤2 reading directions,
// one font size, no overlap with pads / vias / board edge ([plan] P8,
// [tidy] silk-side/silk-style, [agent] S1).
func aesP8(b *Board, in AesInput) AesMetric {
	m := AesMetric{ID: "P8", Group: "placement", Name: "designator silkscreen", Unit: "mean of 4 sub-scores"}
	if !in.SilkKnown || len(in.Silk) == 0 {
		m.Skipped, m.Reason = true, "no silkscreen designators in the input (pcb dump silk[])"
		return m
	}
	side := map[string]int{}
	rot := map[float64]int{}
	font := map[float64]int{}
	n, nb, overlap := 0, 0, 0
	var worst []AesOffender
	pix := newPadIndex(b)
	for _, t := range in.Silk {
		p := b.Part(t.Ref)
		if p == nil || !t.HasBBox && t.X == 0 && t.Y == 0 {
			continue // unmatched, or a hidden attribute without a position
		}
		n++
		c := p.Body().Center()
		at := Point{t.X, t.Y} // anchor when the dump has no text box
		if t.HasBBox {
			at = t.BBox.Center()
		}
		d := at.Sub(c)
		switch {
		case math.Hypot(d.X, d.Y) < 1: // [tidy] pcbTidySideDeadzoneMil
			side["centre"]++
		case math.Abs(d.X) >= math.Abs(d.Y) && d.X < 0:
			side["left"]++
		case math.Abs(d.X) >= math.Abs(d.Y):
			side["right"]++
		case d.Y > 0:
			side["top"]++
		default:
			side["bottom"]++
		}
		rot[math.Mod(90*math.Round(silkRot(t.Rotation)/90), 360)]++
		font[math.Round(t.FontSize*2)/2]++ // [tidy] pcbTidyFontTolMil 0.5
		// overlap: pad copper on the text's side, a via or the board edge
		// under the text box (> 1 mil² of overlap: touching is not over)
		if !t.HasBBox {
			continue
		}
		nb++
		padSide := LayerTop
		if t.Layer == 4 { // bottom silkscreen
			padSide = LayerBottom
		}
		hit := ""
		for x := int(math.Floor(t.BBox.MinX / pix.cell)); x <= int(math.Floor(t.BBox.MaxX/pix.cell)) && hit == ""; x++ {
			for y := int(math.Floor(t.BBox.MinY / pix.cell)); y <= int(math.Floor(t.BBox.MaxY/pix.cell)) && hit == ""; y++ {
				for _, pd := range pix.cells[[2]int{x, y}] {
					if pd.OnLayer(padSide) && pd.Box.Bounds().OverlapArea(t.BBox) > 1 {
						hit = "pad " + pd.Key()
						break
					}
				}
			}
		}
		for _, v := range in.Vias {
			if hit == "" && t.BBox.Expand(v.Dia/2).Contains(v.C) {
				hit = "via"
			}
		}
		if hit == "" && len(b.Outline) >= 3 {
			for _, q := range t.BBox.Corners() {
				if !PolyContains(b.Outline, q) {
					hit = "board edge"
					break
				}
			}
		}
		if hit != "" {
			overlap++
			worst = append(worst, AesOffender{Ref: t.Ref, At: ptr(t.BBox.Center()), Value: 1, Note: "designator over " + hit})
		}
	}
	if n == 0 {
		m.Skipped, m.Reason = true, "no designator matched to a part"
		return m
	}
	maj := func(mp map[string]int) (string, int) {
		k, best := "", -1
		ks := make([]string, 0, len(mp))
		for x := range mp {
			ks = append(ks, x)
		}
		sort.Strings(ks)
		for _, x := range ks {
			if mp[x] > best {
				k, best = x, mp[x]
			}
		}
		return k, best
	}
	_, sideMaj := maj(side)
	rs := make([]int, 0, len(rot))
	for _, v := range rot {
		rs = append(rs, v)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(rs)))
	top2 := 0
	for i := 0; i < len(rs) && i < 2; i++ {
		top2 += rs[i]
	}
	fs := make([]int, 0, len(font))
	for _, v := range font {
		fs = append(fs, v)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(fs)))
	sideShare := float64(sideMaj) / float64(n)
	rotShare := float64(top2) / float64(n)
	fontShare := float64(fs[0]) / float64(n)
	ovShare := 0.0
	if nb > 0 {
		ovShare = float64(overlap) / float64(nb)
	}
	// sub-ramps (initial): side / reading direction / font share 100 % →
	// 100, 60 % / 80 % / 80 % → 0; overlap 0 → 100, 10 % → 0.
	s1 := aesRamp(-sideShare, -1, -0.6)
	s2 := aesRamp(-rotShare, -1, -0.8)
	s3 := aesRamp(-fontShare, -1, -0.8)
	s4 := aesRamp(ovShare, 0, 0.1)
	m.Value = round3((sideShare + rotShare + fontShare + (1 - ovShare)) / 4)
	m.Score = aesRound((s1 + s2 + s3 + s4) / 4)
	m.Detail = fmt.Sprintf("%d designators: majority side %.0f%%, top-2 reading directions %.0f%%, majority font %.0f%%, %d/%d boxed over pad/via/edge",
		n, 100*sideShare, 100*rotShare, 100*fontShare, overlap, nb)
	m.Extra = map[string]float64{"designators": float64(n), "boxed": float64(nb), "sideShare": round3(sideShare), "readShare": round3(rotShare),
		"fontShare": round3(fontShare), "overlaps": float64(overlap), "directions": float64(len(rot))}
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}

// P9: part origins on the 5 mil (audit) and 25 mil (target) grids ([tidy]
// §9.1: 5 mil catches sub-mil drift, 25 mil is the snap target). Score =
// 70 % × 5-mil share + 30 % × 25-mil share (initial).
func aesP9(parts []*aPart, grid, blend float64) AesMetric {
	m := AesMetric{ID: "P9", Group: "placement", Name: "grid landing", Unit: "share of origins on 5 mil"}
	n, on5, onT := 0, 0, 0
	var worst []AesOffender
	for _, ap := range parts {
		if ap.mech || ap.p.Fixed {
			continue
		}
		n++
		p := ap.p.Pos
		// [tidy] pcbTidyGridTolMil 0.001: only float noise is forgiven.
		a := onGrid(p.X, 5, 0.001) && onGrid(p.Y, 5, 0.001)
		if a {
			on5++
		} else {
			worst = append(worst, AesOffender{Ref: ap.p.Ref, At: ptr(p),
				Value: round3(math.Hypot(p.X-5*math.Round(p.X/5), p.Y-5*math.Round(p.Y/5))), Note: "origin off the 5 mil grid"})
		}
		if onGrid(p.X, grid, 0.001) && onGrid(p.Y, grid, 0.001) {
			onT++
		}
	}
	if n == 0 {
		m.Skipped, m.Reason = true, "no movable non-mechanical part"
		return m
	}
	s5, sT := float64(on5)/float64(n), float64(onT)/float64(n)
	m.Value = round3(s5)
	// Score = 100 × ((1−blend) × 5-mil audit share + blend × target-grid
	// share); balanced blend 0.3 on 25 mil ([tidy] §9.1 audit vs target).
	m.Score = aesRound(100 * ((1-blend)*s5 + blend*sT))
	m.Detail = fmt.Sprintf("origins on 5 mil %d/%d, on the %g mil target grid %d/%d", on5, n, grid, onT, n)
	m.Extra = map[string]float64{"parts": float64(n), "on5": float64(on5), "onTarget": float64(onT), "targetGridMil": grid}
	m.Worst = capWorst(worst, aesMaxWorst)
	return m
}
