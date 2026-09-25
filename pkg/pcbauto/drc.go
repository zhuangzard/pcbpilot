package pcbauto

import (
	"math"
	"sort"
	"strings"
)

// Violation is one exact-geometry DRC finding.
type Violation struct {
	Kind     string  `json:"kind"` // track-track | track-pad | via-* | edge | keepout | hole
	NetA     string  `json:"netA"`
	NetB     string  `json:"netB,omitempty"`
	Layer    int     `json:"layer,omitempty"`
	At       Point   `json:"at"`
	Gap      float64 `json:"gapMil"`
	Required float64 `json:"requiredMil"`

	// The two parties (rb.kind < 0 for a single-object finding), so a
	// repair moves the object that violates, not whatever lies near At.
	ra, rb drcRef
}

// drcRef names one party of a violation: kind 0 pad, 1 track, 2 via; idx is
// the index into the checked tracks or vias.
type drcRef struct {
	kind int
	idx  int
	pad  *Pad
}

var noRef = drcRef{kind: -1}

// DRCReport is the independent check of a routed board.
type DRCReport struct {
	Violations   []Violation    `json:"violations"`
	ByKind       map[string]int `json:"byKind"`
	Disconnected []Unrouted     `json:"disconnected,omitempty"`
	Checked      int            `json:"checkedItems"`
}

type drcItem struct {
	kind  int // 0 pad, 1 track, 2 via
	net   string
	layer int // copper layer id or LayerMulti
	pad   *Pad
	t     Track
	v     Via
	bb    Rect
	id    int
	idx   int // index into the checked tracks / vias
}

func (it *drcItem) ref() drcRef { return drcRef{kind: it.kind, idx: it.idx, pad: it.pad} }

func (it *drcItem) onLayer(l int) bool {
	return it.layer == LayerMulti || it.layer == l
}

func (it *drcItem) sharesLayer(o *drcItem) (int, bool) {
	switch {
	case it.layer == LayerMulti && o.layer == LayerMulti:
		return LayerTop, true
	case it.layer == LayerMulti:
		return o.layer, true
	case o.layer == LayerMulti || it.layer == o.layer:
		return it.layer, true
	}
	return 0, false
}

// gap returns the copper-to-copper distance between two items.
func gapOf(a, b *drcItem) (float64, Point) {
	if a.kind > b.kind {
		a, b = b, a
	}
	switch {
	case a.kind == 0 && b.kind == 0:
		// Approximate pad-pad by the far box against the near box's centre
		// ring; pads rarely matter here (footprint-defined).
		d := b.pad.Box.Dist(a.pad.Box.C) - math.Min(a.pad.Box.W, a.pad.Box.H)/2
		return math.Max(d, 0), a.pad.Box.C
	case a.kind == 0 && b.kind == 1:
		return a.pad.Box.SegDist(b.t.A, b.t.B) - b.t.Width/2, b.t.A
	case a.kind == 0 && b.kind == 2:
		return a.pad.Box.Dist(b.v.C) - b.v.Dia/2, b.v.C
	case a.kind == 1 && b.kind == 1:
		return SegSegDist(a.t.A, a.t.B, b.t.A, b.t.B) - a.t.Width/2 - b.t.Width/2, a.t.A
	case a.kind == 1 && b.kind == 2:
		return PointSegDist(b.v.C, a.t.A, a.t.B) - a.t.Width/2 - b.v.Dia/2, b.v.C
	default:
		return a.v.C.Dist(b.v.C) - a.v.Dia/2 - b.v.Dia/2, a.v.C
	}
}

var kindNames = [3]string{"pad", "track", "via"}

// CheckDRC verifies tracks and vias against pads, each other, the board edge,
// keepouts and holes, then checks per-net connectivity of routed nets.
// an supplies per-net clearance (nil = board rule for all nets).
// CheckDRC is the routing-loop check (0.1 mil tolerance for float noise).
func CheckDRC(b *Board, an *Analysis, st *Stackup, tracks []Track, vias []Via) *DRCReport {
	return checkDRCTol(b, an, st, tracks, vias, 0.1)
}

// CheckDRCStrict judges the delivered copper at 0.01 mil: the host DRC flagged
// a track 0.06 mil inside a 5.98 mil rule that the loop tolerance passed.
func CheckDRCStrict(b *Board, an *Analysis, st *Stackup, tracks []Track, vias []Via) *DRCReport {
	return checkDRCTol(b, an, st, tracks, vias, 0.01)
}

func checkDRCTol(b *Board, an *Analysis, st *Stackup, tracks []Track, vias []Via, tol float64) *DRCReport {
	rep := &DRCReport{ByKind: map[string]int{}}
	clr := func(net string) float64 {
		if an != nil {
			if p := an.ByNet[net]; p != nil && p.ClearanceMil > 0 {
				return p.ClearanceMil
			}
		}
		return b.Rules.Clearance
	}
	var items []*drcItem
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			items = append(items, &drcItem{kind: 0, net: pd.Net, layer: pd.Layer, pad: pd, bb: pd.Box.Bounds()})
		}
	}
	for i, t := range tracks {
		bb := EmptyRect().AddPoint(t.A).AddPoint(t.B).Expand(t.Width / 2)
		items = append(items, &drcItem{kind: 1, net: t.Net, layer: t.Layer, t: t, bb: bb, idx: i})
	}
	for i, v := range vias {
		items = append(items, &drcItem{kind: 2, net: v.Net, layer: LayerMulti, v: v, bb: Rect{v.C.X, v.C.Y, v.C.X, v.C.Y}.Expand(v.Dia / 2), idx: i})
	}
	for i, it := range items {
		it.id = i
	}
	rep.Checked = len(items)
	// Spatial hash.
	const cell = 100.0
	maxClr := b.Rules.Clearance
	if an != nil {
		for _, p := range an.Nets {
			maxClr = math.Max(maxClr, p.ClearanceMil)
		}
	}
	buckets := map[[2]int][]*drcItem{}
	key := func(x, y float64) [2]int { return [2]int{int(math.Floor(x / cell)), int(math.Floor(y / cell))} }
	for _, it := range items {
		bb := it.bb.Expand(maxClr / 2)
		k0, k1 := key(bb.MinX, bb.MinY), key(bb.MaxX, bb.MaxY)
		for x := k0[0]; x <= k1[0]; x++ {
			for y := k0[1]; y <= k1[1]; y++ {
				buckets[[2]int{x, y}] = append(buckets[[2]int{x, y}], it)
			}
		}
	}
	seen := map[[2]int]bool{}
	for _, bucket := range buckets {
		for i := 0; i < len(bucket); i++ {
			a := bucket[i]
			for j := i + 1; j < len(bucket); j++ {
				c := bucket[j]
				if a.kind == 0 && c.kind == 0 {
					continue // footprint geometry is not ours to judge
				}
				if a.net != "" && a.net == c.net {
					continue
				}
				if a.kind == 0 && c.kind == 0 && a.pad.Part == c.pad.Part {
					continue
				}
				layer, ok := a.sharesLayer(c)
				if !ok {
					continue
				}
				pair := [2]int{min(a.id, c.id), max(a.id, c.id)}
				if seen[pair] {
					continue
				}
				req := math.Max(clr(a.net), clr(c.net))
				if !a.bb.Expand(req).Overlaps(c.bb) {
					continue
				}
				seen[pair] = true
				g, at := gapOf(a, c)
				if g < req-tol {
					k := kindNames[min(a.kind, c.kind)] + "-" + kindNames[max(a.kind, c.kind)]
					if a.kind > c.kind {
						a, c = c, a
					}
					rep.Violations = append(rep.Violations, Violation{Kind: k, NetA: a.net, NetB: c.net, Layer: layer, At: at, Gap: round2(g), Required: req, ra: a.ref(), rb: c.ref()})
				}
			}
		}
	}
	// Hole to hole (drill edge to drill edge), same net included: vias of
	// one net may overlap in copper but the fab cannot drill them that close.
	holeCell := map[[2]int][]int{}
	for i, v := range vias {
		if holeGap(b) <= 0 {
			break
		}
		k := [2]int{int(math.Floor(v.C.X / 50)), int(math.Floor(v.C.Y / 50))}
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				for _, j := range holeCell[[2]int{k[0] + dx, k[1] + dy}] {
					o := vias[j]
					if g := v.C.Dist(o.C) - v.Drill/2 - o.Drill/2; g < holeGap(b)-0.01 {
						rep.Violations = append(rep.Violations, Violation{Kind: "hole-hole", NetA: v.Net, NetB: o.Net, At: v.C, Gap: round2(g), Required: holeGap(b), ra: drcRef{kind: 2, idx: i}, rb: drcRef{kind: 2, idx: j}})
					}
				}
			}
		}
		holeCell[k] = append(holeCell[k], i)
	}
	// Edge, keepouts, holes.
	for _, it := range items {
		if it.kind == 0 {
			continue
		}
		var d float64
		var at Point
		if it.kind == 1 {
			at = it.t.A
			d = math.Inf(1)
			n := len(b.Outline)
			for k := 0; k < n && n >= 3; k++ {
				d = math.Min(d, SegSegDist(it.t.A, it.t.B, b.Outline[k], b.Outline[(k+1)%n]))
			}
			d -= it.t.Width / 2
			if n >= 3 && (!PolyContains(b.Outline, it.t.A) || !PolyContains(b.Outline, it.t.B)) {
				d = -1
			}
		} else {
			at = it.v.C
			d = PolyEdgeDist(b.Outline, it.v.C) - it.v.Dia/2
			if len(b.Outline) >= 3 && !PolyContains(b.Outline, it.v.C) {
				d = -1
			}
		}
		if len(b.Outline) >= 3 && d < b.Rules.EdgeClearance-0.1 {
			rep.Violations = append(rep.Violations, Violation{Kind: "edge", NetA: it.net, Layer: it.layer, At: at, Gap: round2(d), Required: b.Rules.EdgeClearance, ra: it.ref(), rb: noRef})
		}
		for _, k := range b.Keepouts {
			if !k.NoCopper && !(k.NoVias && it.kind == 2) {
				continue
			}
			if it.kind == 1 && !k.onLayer(it.layer) {
				continue
			}
			hit := false
			if it.kind == 2 {
				hit = PolyContains(k.Poly, it.v.C) || PolyEdgeDist(k.Poly, it.v.C) < it.v.Dia/2
			} else {
				hit = PolyContains(k.Poly, it.t.A) || PolyContains(k.Poly, it.t.B)
				for e := 0; e < len(k.Poly) && !hit; e++ {
					if SegSegDist(it.t.A, it.t.B, k.Poly[e], k.Poly[(e+1)%len(k.Poly)]) < it.t.Width/2 {
						hit = true
					}
				}
			}
			if hit {
				rep.Violations = append(rep.Violations, Violation{Kind: "keepout", NetA: it.net, Layer: it.layer, At: at, ra: it.ref(), rb: noRef})
			}
		}
		for _, h := range b.Holes {
			var g float64
			if it.kind == 1 {
				g = PointSegDist(h.C, it.t.A, it.t.B) - it.t.Width/2 - h.Dia/2
			} else {
				g = h.C.Dist(it.v.C) - it.v.Dia/2 - h.Dia/2
			}
			if g < h.Keep+b.Rules.Clearance-0.1 {
				rep.Violations = append(rep.Violations, Violation{Kind: "hole", NetA: it.net, Layer: it.layer, At: at, Gap: round2(g), Required: h.Keep + b.Rules.Clearance, ra: it.ref(), rb: noRef})
			}
		}
	}
	for _, v := range rep.Violations {
		rep.ByKind[v.Kind]++
	}
	// Total order (bucket iteration is map-ordered): the repair loop consumes
	// this list, and runs must be reproducible.
	sort.Slice(rep.Violations, func(i, j int) bool {
		a, b := rep.Violations[i], rep.Violations[j]
		switch {
		case a.Gap != b.Gap:
			return a.Gap < b.Gap
		case a.Kind != b.Kind:
			return a.Kind < b.Kind
		case a.NetA != b.NetA:
			return a.NetA < b.NetA
		case a.NetB != b.NetB:
			return a.NetB < b.NetB
		case a.At.X != b.At.X:
			return a.At.X < b.At.X
		}
		return a.At.Y < b.At.Y
	})
	rep.Disconnected = checkConnectivity(b, st, items)
	return rep
}

// holeGap is the board's drill-edge to drill-edge rule (Rules.HoleGap);
// 0 when the snapshot carries none — then it is not enforced.
func holeGap(b *Board) float64 {
	return b.Rules.HoleGap
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// checkConnectivity unions same-net copper that touches; plane/pour nets are
// excluded (their delivery is by flooded copper, simulated by the router).
func checkConnectivity(b *Board, st *Stackup, items []*drcItem) []Unrouted {
	planeNet := map[string]bool{}
	if st != nil {
		for _, l := range st.Stack {
			for _, n := range l.Nets {
				planeNet[n] = true
			}
			for _, n := range l.PourNets {
				planeNet[n] = true
			}
		}
	}
	byNet := map[string][]*drcItem{}
	for _, it := range items {
		if it.net != "" && !planeNet[it.net] {
			byNet[it.net] = append(byNet[it.net], it)
		}
	}
	var out []Unrouted
	names := make([]string, 0, len(byNet))
	for n := range byNet {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		its := byNet[name]
		pads := 0
		for _, it := range its {
			if it.kind == 0 {
				pads++
			}
		}
		if pads < 2 {
			continue
		}
		parent := make([]int, len(its))
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
		for i := range its {
			for j := i + 1; j < len(its); j++ {
				a, c := its[i], its[j]
				if a.kind == 0 && c.kind == 0 {
					continue
				}
				if _, ok := a.sharesLayer(c); !ok {
					continue
				}
				if !a.bb.Expand(0.5).Overlaps(c.bb) {
					continue
				}
				if g, _ := gapOf(a, c); g <= 0.05 {
					parent[find(i)] = find(j)
				}
			}
		}
		groups := map[int][]string{}
		for i, it := range its {
			if it.kind == 0 {
				r := find(i)
				groups[r] = append(groups[r], it.pad.Key())
			}
		}
		if len(groups) > 1 {
			var rest []string
			roots := make([]int, 0, len(groups))
			for r := range groups {
				roots = append(roots, r)
			}
			sort.Ints(roots)
			for _, r := range roots[1:] {
				rest = append(rest, groups[r]...)
			}
			sort.Strings(rest)
			out = append(out, Unrouted{Net: name, Pads: rest, Reason: sprintf("%d islands", len(groups))})
		}
	}
	return out
}

// microFixMaxShort is the largest clearance shortfall (mil) settled by
// narrowing a track instead of re-routing: grid-discretised near misses.
const microFixMaxShort = 0.25

// MicroFix settles strict-check clearance misses of at most
// microFixMaxShort: it first shifts the offending track vertex/segment or via
// away from the other party (keeping widths and connections), then narrows
// tracks, keeping them at or above the process minimum. Ripping such
// connections in repair cost bbclaw 11 % completion; a 10 mil track 0.14 mil
// narrower is electrically the same. It returns how many shortfalls it fixed.
func MicroFix(b *Board, an *Analysis, st *Stackup, rr *RouteResult) int {
	fixed := 0
	for pass := 0; pass < 3; pass++ {
		changed := false
		vs := CheckDRCStrict(b, an, st, rr.Tracks, rr.Vias).Violations
		cur := len(vs)
		for _, v := range vs {
			short := v.Required - v.Gap
			if short <= 0 || short > microFixMaxShort || v.ra.kind < 0 || v.rb.kind < 0 || v.Kind == "hole-hole" {
				continue
			}
			// First shift the violating track or via away from the other
			// party; narrow nearby tracks of either net only if that fails.
			if n := nudgeAway(b, an, st, rr, v, short, cur); n >= 0 {
				cur = n
				fixed++
				changed = true
				continue
			}
			if !strings.Contains(v.Kind, "track") {
				continue
			}
			hit := false
			for i, t := range rr.Tracks {
				if t.Net != v.NetA && t.Net != v.NetB || v.Layer != 0 && t.Layer != v.Layer {
					continue
				}
				if PointSegDist(v.At, t.A, t.B)-t.Width/2 > v.Gap+1 {
					continue
				}
				w := t.Width - 2*short - 0.04
				if w < b.Rules.MinTrack {
					continue
				}
				rr.Tracks[i].Width = math.Floor(w*100) / 100
				hit = true
			}
			if !hit {
				continue
			}
			fixed++
			changed = true
		}
		if !changed {
			break
		}
	}
	return fixed
}

// shapeOf returns a violation party's distance function, nearest-point
// function and centre (pad copper, track centreline, via centre).
func shapeOf(r drcRef, rr *RouteResult) (func(Point) float64, func(Point) Point, Point) {
	switch r.kind {
	case 0:
		return r.pad.Box.Dist, r.pad.Box.Nearest, r.pad.Box.C
	case 1:
		t := rr.Tracks[r.idx]
		return func(p Point) float64 { return PointSegDist(p, t.A, t.B) },
			func(p Point) Point { return segNearest(p, t.A, t.B) }, t.A.Add(t.B).Scale(0.5)
	}
	c := rr.Vias[r.idx].C
	return func(p Point) float64 { return p.Dist(c) }, func(Point) Point { return c }, c
}

// nudgeAway moves one routed party of violation v — a track segment or
// vertex, else a via — by the shortfall (+0.03 mil) straight away from the
// other party's nearest copper, dragging every same-net track end that
// coincides with a moved point (a via drags the tracks ending at it on every
// layer). Track ends on a pad or via of the net are anchors and never move.
// A move is kept only if the strict check then has fewer than cur
// violations; it returns the new count, or -1 when nothing was kept.
func nudgeAway(b *Board, an *Analysis, st *Stackup, rr *RouteResult, v Violation, short float64, cur int) int {
	near := func(a, c Point) bool { return a.Dist(c) <= 0.01 }
	try := func(net string, move []Point, d Point, via int) int {
		savedT := append([]Track(nil), rr.Tracks...)
		savedV := append([]Via(nil), rr.Vias...)
		if via >= 0 {
			rr.Vias[via].C = rr.Vias[via].C.Add(d)
		}
		for i := range rr.Tracks {
			t := &rr.Tracks[i]
			if t.Net != net {
				continue
			}
			for _, e := range []*Point{&t.A, &t.B} {
				for _, m := range move {
					if near(*e, m) {
						*e = e.Add(d)
						break
					}
				}
			}
		}
		if n := len(CheckDRCStrict(b, an, st, rr.Tracks, rr.Vias).Violations); n < cur {
			return n
		}
		copy(rr.Tracks, savedT)
		copy(rr.Vias, savedV)
		return -1
	}
	away := func(p Point, other drcRef, mag float64) (Point, bool) {
		_, nearest, c := shapeOf(other, rr)
		dir := p.Sub(nearest(p))
		if math.Hypot(dir.X, dir.Y) < 1e-9 {
			dir = p.Sub(c)
		}
		l := math.Hypot(dir.X, dir.Y)
		if l < 1e-9 {
			return Point{}, false
		}
		return dir.Scale(mag / l), true
	}
	mag := short + 0.03
	for _, pr := range [][2]drcRef{{v.rb, v.ra}, {v.ra, v.rb}} {
		self, other := pr[0], pr[1]
		switch self.kind {
		case 1:
			t := rr.Tracks[self.idx]
			anchored := func(p Point) bool {
				for _, via := range rr.Vias {
					if via.Net == t.Net && near(via.C, p) {
						return true
					}
				}
				for _, part := range b.Parts {
					for _, q := range part.Pads {
						if q.Net == t.Net && q.Box.Dist(p) == 0 {
							return true
						}
					}
				}
				return false
			}
			dist, _, _ := shapeOf(other, rr)
			p := segClosest(t.A, t.B, dist)
			d, ok := away(p, other, mag)
			if !ok {
				continue
			}
			// Candidate moves: the vertex at the hot spot, else the whole
			// segment; with one end anchored, swing the free end so the hot
			// spot still clears.
			var moves [][]Point
			var ds []Point
			switch {
			case p.Dist(t.A) <= 0.5:
				moves, ds = append(moves, []Point{t.A}), append(ds, d)
			case p.Dist(t.B) <= 0.5:
				moves, ds = append(moves, []Point{t.B}), append(ds, d)
			}
			moves, ds = append(moves, []Point{t.A, t.B}), append(ds, d)
			for _, end := range [][2]Point{{t.A, t.B}, {t.B, t.A}} {
				free, pivot := end[0], end[1]
				if !anchored(pivot) || anchored(free) || p.Dist(pivot) < 0.5 {
					continue
				}
				k := math.Min(free.Dist(pivot)/p.Dist(pivot), 4)
				moves, ds = append(moves, []Point{free}), append(ds, d.Scale(k))
			}
			for i, m := range moves {
				blocked := false
				for _, q := range m {
					if anchored(q) {
						blocked = true
					}
				}
				if blocked {
					continue
				}
				if n := try(t.Net, m, ds[i], -1); n >= 0 {
					return n
				}
			}
		case 2:
			via := rr.Vias[self.idx]
			d, ok := away(via.C, other, mag)
			if !ok {
				continue
			}
			if n := try(via.Net, []Point{via.C}, d, self.idx); n >= 0 {
				return n
			}
		}
	}
	return -1
}
