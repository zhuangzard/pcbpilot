package app

// pcb_silk_tight.go — designators next to their own footprint, never
// shrunk, judged on the readback (pcb.silk.list), and the "silkscreen" gate.
//
// Why: on Gas Module V5 the connector's slot search (15 mil × 1.5 drift)
// left designators far from their parts and on top of each other. The plan
// is computed here from the board dump so it can be tested offline; the
// verdict always comes from the real rendered boxes read back afterwards.
//
// Order of resolution (user decision 2026-10-06): the nearest free slot on
// any of the four sides (label horizontal), then the label turned 90° on the
// four sides, then a dense cluster (decap row, resistor array) gets its
// labels as an ordered row / column beside the group, each label on its own
// part. Text is never shrunk. What still has no slot is reported with a
// suggested group label ("C21–C24"); group labels and leader lines are not
// drawn automatically.

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// silkTightOpts are the placement and gate limits (mil).
type silkTightOpts struct {
	Gap        float64 // first gap to the own footprint box
	MaxDist    float64 // farthest label box ↔ own footprint box (gate: above fails)
	PadClear   float64 // label ↔ pad / via / hole (fab silk-to-pad, JLC 0.15 mm)
	LabelClear float64 // label ↔ label
	EdgeClear  float64 // label ↔ board edge
	FontSize   float64 // project designator height; 0 = most common on the board
	LineWidth  float64 // project stroke; 0 = leave as is
	FabMinLine float64 // fab minimum stroke (JLC 0.15 mm = 5.9 mil)
	ViaOpening bool    // gate: vias are soldermask openings (not tented)
	// Group labels for what has no own slot: members' footprints within
	// GroupLink of each other form a group; its one text sits within
	// GroupMaxDist of the group's box; NoGroups leaves them unresolved.
	GroupLink, GroupMaxDist float64
	NoGroups                bool
}

func defaultSilkTightOpts() silkTightOpts {
	return silkTightOpts{Gap: 5, MaxDist: 30, PadClear: 6, LabelClear: 2, EdgeClear: 8, FabMinLine: 5.9, GroupLink: 150, GroupMaxDist: 80}
}

type silkBox struct{ MinX, MinY, MaxX, MaxY float64 }

func (b silkBox) w() float64  { return b.MaxX - b.MinX }
func (b silkBox) h() float64  { return b.MaxY - b.MinY }
func (b silkBox) cx() float64 { return (b.MinX + b.MaxX) / 2 }
func (b silkBox) cy() float64 { return (b.MinY + b.MaxY) / 2 }
func (b silkBox) grow(d float64) silkBox {
	return silkBox{b.MinX - d, b.MinY - d, b.MaxX + d, b.MaxY + d}
}

// overlaps: positive-area intersection (touching is not an overlap).
func (b silkBox) overlaps(o silkBox) bool {
	return math.Min(b.MaxX, o.MaxX)-math.Max(b.MinX, o.MinX) > 1e-6 && math.Min(b.MaxY, o.MaxY)-math.Max(b.MinY, o.MinY) > 1e-6
}

// dist is the gap between two boxes (0 when they touch or overlap).
func (b silkBox) dist(o silkBox) float64 {
	dx := math.Max(0, math.Max(o.MinX-b.MaxX, b.MinX-o.MaxX))
	dy := math.Max(0, math.Max(o.MinY-b.MaxY, b.MinY-o.MaxY))
	return math.Hypot(dx, dy)
}

func silkBoxAt(cx, cy, w, h float64) silkBox { return silkBox{cx - w/2, cy - h/2, cx + w/2, cy + h/2} }

// normRot folds a host rotation (it accumulates: 450, -809.99…) to 0/90/180/270.
func normRot(r float64) int {
	q := int(math.Round(r/90)) % 4
	if q < 0 {
		q += 4
	}
	return q * 90
}

// padBox is a pad's axis-aligned extent (any rotation).
func padBox(p boardPad) silkBox {
	a := p.Rotation * math.Pi / 180
	c, s := math.Abs(math.Cos(a)), math.Abs(math.Sin(a))
	hw, hh := (p.W*c+p.H*s)/2, (p.W*s+p.H*c)/2
	return silkBox{p.X - hw, p.Y - hh, p.X + hw, p.Y + hh}
}

// silkLabel is one designator to place.
type silkLabel struct {
	ID, Ref  string
	Layer    int     // silk layer
	Own      silkBox // own footprint box (connector bbox, designator excluded)
	Len, Hgt float64 // text extent along / across its baseline at the target size
	Cur      silkBox // current rendered box
	Rot      int     // current rotation 0/90/180/270
	Font     float64 // current font size
	Fixed    bool    // not moved (no own footprint known)
}

// silkPlaced is a label's planned slot.
type silkPlaced struct {
	ID, Ref string
	Box     silkBox
	Rot     int    // 0 or 90
	How     string // side / rotated / group-row / group-col / unresolved
	Moved   bool
}

// silkScene is everything a label must keep off, per silk layer.
type silkScene struct {
	Board   *boardOutline
	Hard    map[int][]silkBox // pads, vias, holes (grown by PadClear), per silk layer
	Bodies  map[int][]silkBox // footprint boxes, per silk layer, with owner ref
	BodyRef map[int][]string
	Fixed   map[int][]silkBox // free strings and labels not moved
}

var refPrefixRe = regexp.MustCompile(`^([A-Za-z]+)(\d+)`)

// silkTightInput builds labels and the scene from a board dump (components
// with pads and bbox, silk, vias, footprint holes, outline).
func silkTightInput(snap *boardSnapshot, opt silkTightOpts) ([]silkLabel, silkScene, float64) {
	sc := silkScene{Board: snap.Outline, Hard: map[int][]silkBox{}, Bodies: map[int][]silkBox{}, BodyRef: map[int][]string{}, Fixed: map[int][]silkBox{}}
	sideSilk := func(side int) int {
		if side == pcbSideBottom {
			return silkBottomLayer
		}
		return silkTopLayer
	}
	comps := map[string]boardComp{}
	for _, c := range snap.Components {
		comps[c.ID] = c
		for _, p := range c.Pads {
			b := padBox(p).grow(opt.PadClear)
			switch p.Layer {
			case pcbSideTop:
				sc.Hard[silkTopLayer] = append(sc.Hard[silkTopLayer], b)
			case pcbSideBottom:
				sc.Hard[silkBottomLayer] = append(sc.Hard[silkBottomLayer], b)
			default:
				sc.Hard[silkTopLayer] = append(sc.Hard[silkTopLayer], b)
				sc.Hard[silkBottomLayer] = append(sc.Hard[silkBottomLayer], b)
			}
		}
		if c.BBox != nil {
			l := sideSilk(c.Layer)
			sc.Bodies[l] = append(sc.Bodies[l], silkBox{c.BBox.MinX, c.BBox.MinY, c.BBox.MaxX, c.BBox.MaxY})
			sc.BodyRef[l] = append(sc.BodyRef[l], c.Designator)
		}
	}
	both := func(b silkBox) {
		sc.Hard[silkTopLayer] = append(sc.Hard[silkTopLayer], b)
		sc.Hard[silkBottomLayer] = append(sc.Hard[silkBottomLayer], b)
	}
	if snap.Copper != nil {
		var vias []widenVia
		if decodeAny(snap.Copper.Vias, &vias) == nil {
			for _, v := range vias {
				both(silkBoxAt(v.X, v.Y, v.Diameter, v.Diameter).grow(opt.PadClear))
			}
		}
	}
	for _, h := range snap.FootprintHoles {
		if h.Shape == "circle" {
			both(silkBoxAt(h.X, h.Y, h.Dia, h.Dia).grow(opt.PadClear))
			continue
		}
		if len(h.Points) > 0 {
			b := silkBox{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
			for _, p := range h.Points {
				b = silkBox{math.Min(b.MinX, p[0]), math.Min(b.MinY, p[1]), math.Max(b.MaxX, p[0]), math.Max(b.MaxY, p[1])}
			}
			both(b.grow(opt.PadClear))
		}
	}
	font := opt.FontSize
	if font <= 0 {
		font = projectDesignatorFont(snap.Silk)
	}
	var labels []silkLabel
	for _, t := range snap.Silk {
		if !isVisibleDesignator(t) {
			if !t.Hidden && t.BBox != nil && (t.Layer == silkTopLayer || t.Layer == silkBottomLayer) {
				sc.Fixed[t.Layer] = append(sc.Fixed[t.Layer], silkBox(*t.BBox))
			}
			continue
		}
		cur := silkBox(*t.BBox)
		rot := normRot(t.Rotation)
		ln, hg := cur.w(), cur.h()
		if rot == 90 || rot == 270 {
			ln, hg = hg, ln
		}
		if t.FontSize > 0 && t.FontSize < font {
			ln, hg = ln*font/t.FontSize, hg*font/t.FontSize // never shrink: only grow to the project size
		}
		l := silkLabel{ID: t.ID, Ref: t.Text, Layer: t.Layer, Len: ln, Hgt: hg, Cur: cur, Rot: rot, Font: t.FontSize}
		if c, ok := comps[t.CompID]; ok && c.BBox != nil {
			l.Own = silkBox{c.BBox.MinX, c.BBox.MinY, c.BBox.MaxX, c.BBox.MaxY}
		} else {
			l.Fixed = true
			sc.Fixed[t.Layer] = append(sc.Fixed[t.Layer], cur)
			continue
		}
		labels = append(labels, l)
	}
	return labels, sc, font
}

// projectDesignatorFont is the most common visible designator height.
func projectDesignatorFont(silk []pcbSilkText) float64 {
	n := map[float64]int{}
	best, bestN := 0.0, 0
	for _, t := range silk {
		if isVisibleDesignator(t) && t.FontSize > 0 {
			f := math.Round(t.FontSize*10) / 10
			n[f]++
			if n[f] > bestN || (n[f] == bestN && f > best) {
				best, bestN = f, n[f]
			}
		}
	}
	return best
}

// legal reports whether box b (a label of ref on layer) keeps off every
// obstacle and every label already placed.
func (sc *silkScene) legal(b silkBox, ref string, layer int, own silkBox, placed []silkPlaced, layerOf map[string]int, opt silkTightOpts) bool {
	if sc.Board != nil {
		in := b.grow(opt.EdgeClear)
		for _, p := range [][2]float64{{in.MinX, in.MinY}, {in.MaxX, in.MinY}, {in.MinX, in.MaxY}, {in.MaxX, in.MaxY}} {
			if !sc.Board.containsPoint(p[0], p[1]) {
				return false
			}
		}
	}
	if b.overlaps(own) {
		return false
	}
	for _, o := range sc.Hard[layer] {
		if b.overlaps(o) {
			return false
		}
	}
	for i, o := range sc.Bodies[layer] {
		if sc.BodyRef[layer][i] != ref && b.overlaps(o) {
			return false
		}
	}
	g := b.grow(opt.LabelClear)
	for _, o := range sc.Fixed[layer] {
		if g.overlaps(o) {
			return false
		}
	}
	for _, p := range placed {
		if p.Ref != ref && layerOf[p.ID] == layer && g.overlaps(p.Box) {
			return false
		}
	}
	return true
}

// sideSlots lists the label boxes on the four sides of own at gap g for a
// label of size w×h: centred first, then slid along the side towards both
// ends, so the nearest free spot on that side is found.
func sideSlots(own silkBox, w, h, g float64) []struct {
	side string
	box  silkBox
} {
	type slot = struct {
		side string
		box  silkBox
	}
	var out []slot
	slide := func(lo, hi, size float64) []float64 {
		c := (lo + hi) / 2
		span := math.Max(0, (hi-lo+size)/2)
		vs := []float64{c}
		for d := 5.0; d <= span+1e-9; d += 5 {
			vs = append(vs, c-d, c+d)
		}
		return vs
	}
	for _, x := range slide(own.MinX, own.MaxX, w) {
		out = append(out, slot{"top", silkBoxAt(x, own.MaxY+g+h/2, w, h)})
	}
	for _, x := range slide(own.MinX, own.MaxX, w) {
		out = append(out, slot{"bottom", silkBoxAt(x, own.MinY-g-h/2, w, h)})
	}
	for _, y := range slide(own.MinY, own.MaxY, h) {
		out = append(out, slot{"left", silkBoxAt(own.MinX-g-w/2, y, w, h)})
	}
	for _, y := range slide(own.MinY, own.MaxY, h) {
		out = append(out, slot{"right", silkBoxAt(own.MaxX+g+w/2, y, w, h)})
	}
	return out
}

// planSilkTight places every label. Labels already legal and within reach
// stay; the rest are placed most-constrained first.
func planSilkTight(labels []silkLabel, sc silkScene, opt silkTightOpts) ([]silkPlaced, []string) {
	layerOf := map[string]int{}
	for _, l := range labels {
		layerOf[l.ID] = l.Layer
	}
	var placed []silkPlaced
	todo := []silkLabel{}
	// Keep a label that already sits legally next to its part at the target
	// size (no churn): judged against the others' current boxes.
	cur := make([]silkPlaced, len(labels))
	for i, l := range labels {
		cur[i] = silkPlaced{ID: l.ID, Ref: l.Ref, Box: l.Cur, Rot: l.Rot}
	}
	for i, l := range labels {
		others := append(append([]silkPlaced{}, cur[:i]...), cur[i+1:]...)
		sized := l.Cur.w()+l.Cur.h() >= l.Len+l.Hgt-0.5
		if sized && (l.Rot == 0 || l.Rot == 90) && l.Cur.dist(l.Own) <= opt.MaxDist && sc.legal(l.Cur, l.Ref, l.Layer, l.Own, others, layerOf, opt) {
			placed = append(placed, silkPlaced{ID: l.ID, Ref: l.Ref, Box: l.Cur, Rot: l.Rot, How: "kept"})
			continue
		}
		todo = append(todo, l)
	}
	// Most constrained first: fewest legal slots against the static scene.
	free := map[string]int{}
	for _, l := range todo {
		n := 0
		for g := opt.Gap; g <= opt.MaxDist; g += 2 {
			for _, s := range append(sideSlots(l.Own, l.Len, l.Hgt, g), sideSlots(l.Own, l.Hgt, l.Len, g)...) {
				if sc.legal(s.box, l.Ref, l.Layer, l.Own, nil, layerOf, opt) {
					n++
				}
			}
		}
		free[l.ID] = n
	}
	sort.SliceStable(todo, func(i, j int) bool {
		if free[todo[i].ID] != free[todo[j].ID] {
			return free[todo[i].ID] < free[todo[j].ID]
		}
		return todo[i].Ref < todo[j].Ref
	})
	var failed []silkLabel
	byID := map[string]silkLabel{}
	for _, l := range labels {
		byID[l.ID] = l
	}
	for _, l := range todo {
		if p, ok := placeOne(l, sc, placed, layerOf, opt); ok {
			placed = append(placed, p)
		} else if np, ok := placeEvicting(l, byID, sc, placed, layerOf, opt); ok {
			placed = np
		} else {
			failed = append(failed, l)
		}
	}
	// Clusters: re-place a failed label's whole row / column together.
	var notes []string
	done := map[string]bool{}
	for _, l := range failed {
		if done[l.ID] {
			continue
		}
		members := silkCluster(l, labels)
		if len(members) < 2 {
			continue
		}
		ids := map[string]bool{}
		for _, m := range members {
			ids[m.ID] = true
		}
		rest := placed[:0:0]
		for _, p := range placed {
			if !ids[p.ID] {
				rest = append(rest, p)
			}
		}
		if grp, ok := placeGroup(members, sc, rest, layerOf, opt); ok {
			placed = append(rest, grp...)
			for _, m := range members {
				done[m.ID] = true
			}
		} else {
			notes = append(notes, fmt.Sprintf("cluster %s: no aligned row/column fits; suggested group label %q (not drawn)", refsOf(members), groupLabel(members)))
		}
	}
	for _, l := range failed {
		if !done[l.ID] {
			notes = append(notes, fmt.Sprintf("%s: no legal slot within %.0f mil (label left at (%.1f,%.1f)); leader line not drawn", l.Ref, opt.MaxDist, l.Cur.cx(), l.Cur.cy()))
			placed = append(placed, silkPlaced{ID: l.ID, Ref: l.Ref, Box: l.Cur, Rot: l.Rot, How: "unresolved"})
		}
	}
	sort.SliceStable(placed, func(i, j int) bool { return placed[i].Ref < placed[j].Ref })
	return placed, notes
}

// placeOne: nearest gap first; at each gap the four sides horizontal, then
// the four sides turned 90° (reads from the right).
func placeOne(l silkLabel, sc silkScene, placed []silkPlaced, layerOf map[string]int, opt silkTightOpts) (silkPlaced, bool) {
	for g := opt.Gap; g <= opt.MaxDist; g += 2 {
		for _, rot := range []int{0, 90} {
			w, h := l.Len, l.Hgt
			if rot == 90 {
				w, h = h, w
			}
			for _, s := range sideSlots(l.Own, w, h, g) {
				if s.box.dist(l.Own) <= opt.MaxDist && sc.legal(s.box, l.Ref, l.Layer, l.Own, placed, layerOf, opt) {
					how := s.side
					if rot == 90 {
						how += "+rotated"
					}
					return silkPlaced{ID: l.ID, Ref: l.Ref, Box: s.box, Rot: rot, How: how, Moved: true}, true
				}
			}
		}
	}
	return silkPlaced{}, false
}

// placeEvicting finds a slot of l that only one placed label blocks and
// moves that label to another legal slot of its own (one level deep).
func placeEvicting(l silkLabel, byID map[string]silkLabel, sc silkScene, placed []silkPlaced, layerOf map[string]int, opt silkTightOpts) ([]silkPlaced, bool) {
	for g := opt.Gap; g <= opt.MaxDist; g += 2 {
		for _, rot := range []int{0, 90} {
			w, h := l.Len, l.Hgt
			if rot == 90 {
				w, h = h, w
			}
			for _, s := range sideSlots(l.Own, w, h, g) {
				if s.box.dist(l.Own) > opt.MaxDist || !sc.legal(s.box, l.Ref, l.Layer, l.Own, nil, layerOf, opt) {
					continue
				}
				block := -1
				gb := s.box.grow(opt.LabelClear)
				for i, p := range placed {
					if layerOf[p.ID] == l.Layer && gb.overlaps(p.Box) {
						if block >= 0 {
							block = -2
							break
						}
						block = i
					}
				}
				if block < 0 {
					continue
				}
				v, ok := byID[placed[block].ID]
				if !ok {
					continue
				}
				how := s.side
				if rot == 90 {
					how += "+rotated"
				}
				mine := silkPlaced{ID: l.ID, Ref: l.Ref, Box: s.box, Rot: rot, How: how, Moved: true}
				rest := append(append(append([]silkPlaced{}, placed[:block]...), placed[block+1:]...), mine)
				if p, ok := placeOne(v, sc, rest, layerOf, opt); ok {
					return append(rest, p), true
				}
			}
		}
	}
	return nil, false
}

// silkCluster is l's row or column: same prefix, same layer, neighbouring
// footprints of similar size whose centres line up, chained.
func silkCluster(l silkLabel, all []silkLabel) []silkLabel {
	pre := func(s string) string {
		if m := refPrefixRe.FindStringSubmatch(s); m != nil {
			return strings.ToUpper(m[1])
		}
		return s
	}
	near := func(a, b silkLabel, row bool) bool {
		if a.Layer != b.Layer || pre(a.Ref) != pre(b.Ref) {
			return false
		}
		if row {
			pitch := math.Max(a.Own.w(), b.Own.w()) * 2.2
			return math.Abs(a.Own.cy()-b.Own.cy()) <= 0.25*math.Max(a.Own.h(), b.Own.h()) && math.Abs(a.Own.cx()-b.Own.cx()) <= pitch
		}
		pitch := math.Max(a.Own.h(), b.Own.h()) * 2.2
		return math.Abs(a.Own.cx()-b.Own.cx()) <= 0.25*math.Max(a.Own.w(), b.Own.w()) && math.Abs(a.Own.cy()-b.Own.cy()) <= pitch
	}
	grow := func(row bool) []silkLabel {
		in := map[string]bool{l.ID: true}
		out := []silkLabel{l}
		for changed := true; changed; {
			changed = false
			for _, c := range all {
				if in[c.ID] || c.Fixed {
					continue
				}
				for _, m := range out {
					if near(m, c, row) {
						in[c.ID], out, changed = true, append(out, c), true
						break
					}
				}
			}
		}
		return out
	}
	r, c := grow(true), grow(false)
	if len(c) > len(r) {
		sort.SliceStable(c, func(i, j int) bool { return c[i].Own.cy() > c[j].Own.cy() })
		return c
	}
	sort.SliceStable(r, func(i, j int) bool { return r[i].Own.cx() < r[j].Own.cx() })
	return r
}

// placeGroup lines the members' labels up beside the cluster, each on its
// own part (row: above / below, column: left / right), horizontal first,
// then turned 90°.
func placeGroup(ms []silkLabel, sc silkScene, placed []silkPlaced, layerOf map[string]int, opt silkTightOpts) ([]silkPlaced, bool) {
	cl := ms[0].Own
	for _, m := range ms[1:] {
		cl = silkBox{math.Min(cl.MinX, m.Own.MinX), math.Min(cl.MinY, m.Own.MinY), math.Max(cl.MaxX, m.Own.MaxX), math.Max(cl.MaxY, m.Own.MaxY)}
	}
	row := cl.w() >= cl.h()
	sides := []string{"top", "bottom"}
	if !row {
		sides = []string{"left", "right"}
	}
	for g := opt.Gap; g <= opt.MaxDist; g += 2 {
		for _, rot := range []int{0, 90} {
			for _, side := range sides {
				var out []silkPlaced
				ok := true
				for _, m := range ms {
					w, h := m.Len, m.Hgt
					if rot == 90 {
						w, h = h, w
					}
					var b silkBox
					switch side {
					case "top":
						b = silkBoxAt(m.Own.cx(), cl.MaxY+g+h/2, w, h)
					case "bottom":
						b = silkBoxAt(m.Own.cx(), cl.MinY-g-h/2, w, h)
					case "left":
						b = silkBoxAt(cl.MinX-g-w/2, m.Own.cy(), w, h)
					default:
						b = silkBoxAt(cl.MaxX+g+w/2, m.Own.cy(), w, h)
					}
					if b.dist(m.Own) > opt.MaxDist || !sc.legal(b, m.Ref, m.Layer, m.Own, append(placed, out...), layerOf, opt) {
						ok = false
						break
					}
					how := "group-row"
					if !row {
						how = "group-col"
					}
					out = append(out, silkPlaced{ID: m.ID, Ref: m.Ref, Box: b, Rot: rot, How: how, Moved: true})
				}
				if ok {
					return out, true
				}
			}
		}
	}
	return nil, false
}

func refsOf(ms []silkLabel) string {
	var s []string
	for _, m := range ms {
		s = append(s, m.Ref)
	}
	return strings.Join(s, ",")
}

// groupLabel suggests the group text of ms (see groupText).
func groupLabel(ms []silkLabel) string {
	var refs []string
	for _, m := range ms {
		refs = append(refs, m.Ref)
	}
	return groupText(refs)
}

// groupText is "C21–C24" when the refs are one prefix with consecutive
// numbers, else every ref sorted (prefix, number) joined by "/".
func groupText(refs []string) string {
	type rn struct {
		p string
		n int
		s string
	}
	var rs []rn
	for _, r := range refs {
		if m := refPrefixRe.FindStringSubmatch(r); m != nil && m[0] == r {
			n, _ := strconv.Atoi(m[2])
			rs = append(rs, rn{strings.ToUpper(m[1]), n, r})
		} else {
			rs = append(rs, rn{r, -1, r})
		}
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].p != rs[j].p {
			return rs[i].p < rs[j].p
		}
		return rs[i].n < rs[j].n
	})
	consec := len(rs) > 1
	for i := range rs {
		if rs[i].n < 0 || rs[i].p != rs[0].p || (i > 0 && rs[i].n != rs[i-1].n+1) {
			consec = false
		}
	}
	if consec {
		return rs[0].s + "–" + rs[len(rs)-1].s
	}
	var out []string
	for _, r := range rs {
		out = append(out, r.s)
	}
	return strings.Join(out, "/")
}

// groupRefs expands a group label text back to its refs ("" when the text
// is not one: every part must be a ref or a ref range).
func groupRefs(text string) []string {
	var out []string
	for _, part := range strings.Split(text, "/") {
		part = strings.TrimSpace(part)
		if a, b, ok := strings.Cut(strings.ReplaceAll(part, "–", "-"), "-"); ok {
			ma, mb := refPrefixRe.FindStringSubmatch(a), refPrefixRe.FindStringSubmatch(b)
			if ma == nil || mb == nil || ma[0] != a || mb[0] != b || !strings.EqualFold(ma[1], mb[1]) {
				return nil
			}
			x, _ := strconv.Atoi(ma[2])
			y, _ := strconv.Atoi(mb[2])
			if y < x || y-x > 200 {
				return nil
			}
			for n := x; n <= y; n++ {
				out = append(out, fmt.Sprintf("%s%d", ma[1], n))
			}
			continue
		}
		if m := refPrefixRe.FindStringSubmatch(part); m == nil || m[0] != part {
			return nil
		}
		out = append(out, part)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// silkGroup is one group label to draw: its text, where, and the
// designators it replaces (hidden, never deleted).
type silkGroup struct {
	Text    string   `json:"text"`
	Refs    []string `json:"refs"`
	IDs     []string `json:"ids"`
	Layer   int      `json:"layer"`
	Box     silkBox  `json:"box"`
	Rot     int      `json:"rotation"`
	Placed  bool     `json:"placed"`
	Members silkBox  `json:"members"`
}

// planSilkGroups groups the unresolved labels whose footprints lie within
// GroupLink of each other and finds each group's text a slot within
// GroupMaxDist of the group's box (nearest gap first, horizontal then
// turned 90°). The members' own labels leave the board (hidden).
func planSilkGroups(labels []silkLabel, sc silkScene, placed []silkPlaced, opt silkTightOpts) ([]silkGroup, []silkPlaced, []string) {
	un := map[string]bool{}
	for _, p := range placed {
		if p.How == "unresolved" {
			un[p.ID] = true
		}
	}
	var ls []silkLabel
	charW, chars, hgt := 0.0, 0, 0.0
	for _, l := range labels {
		charW += l.Len
		chars += len([]rune(l.Ref))
		hgt = math.Max(hgt, l.Hgt)
		if un[l.ID] {
			ls = append(ls, l)
		}
	}
	if len(ls) == 0 {
		return nil, placed, nil
	}
	charW /= float64(max(chars, 1))
	// Union the unresolved labels by footprint distance.
	parent := make([]int, len(ls))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	for i := range ls {
		for j := i + 1; j < len(ls); j++ {
			if ls[i].Layer == ls[j].Layer && ls[i].Own.dist(ls[j].Own) <= opt.GroupLink {
				parent[find(i)] = find(j)
			}
		}
	}
	byRoot := map[int][]silkLabel{}
	var roots []int
	for i := range ls {
		r := find(i)
		if byRoot[r] == nil {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], ls[i])
	}
	// The hidden labels no longer block anything.
	var keep []silkPlaced
	for _, p := range placed {
		if !un[p.ID] {
			keep = append(keep, p)
		}
	}
	layerOf := map[string]int{}
	for _, l := range labels {
		layerOf[l.ID] = l.Layer
	}
	var groups []silkGroup
	var notes []string
	var back []silkPlaced // single unresolved labels stay as they were
	single := func(m silkLabel) {
		back = append(back, silkPlaced{ID: m.ID, Ref: m.Ref, Box: m.Cur, Rot: m.Rot, How: "unresolved"})
		notes = append(notes, fmt.Sprintf("%s: no legal slot within %.0f mil and no group label fits (label left at (%.1f,%.1f)); leader line not drawn", m.Ref, opt.MaxDist, m.Cur.cx(), m.Cur.cy()))
	}
	// try places one group label; a group that does not fit, or whose text
	// is longer than maxGroupChars, is split along its long axis and retried.
	var try func(ms []silkLabel)
	try = func(ms []silkLabel) {
		if len(ms) < 2 {
			for _, m := range ms {
				single(m)
			}
			return
		}
		g := silkGroup{Text: groupLabel(ms), Layer: ms[0].Layer, Members: ms[0].Own}
		for _, m := range ms {
			g.Refs = append(g.Refs, m.Ref)
			g.IDs = append(g.IDs, m.ID)
			g.Members = silkBox{math.Min(g.Members.MinX, m.Own.MinX), math.Min(g.Members.MinY, m.Own.MinY), math.Max(g.Members.MaxX, m.Own.MaxX), math.Max(g.Members.MaxY, m.Own.MaxY)}
		}
		if len([]rune(g.Text)) <= maxGroupChars {
			ln := charW * float64(len([]rune(g.Text)))
		search:
			for d := opt.Gap; d <= opt.GroupMaxDist; d += 2 {
				for _, rot := range []int{0, 90} {
					w, h := ln, hgt
					if rot == 90 {
						w, h = h, w
					}
					for _, s := range sideSlots(g.Members, w, h, d) {
						if s.box.dist(g.Members) <= opt.GroupMaxDist && sc.legal(s.box, "", g.Layer, silkBox{}, append(keep, groupPlaced(groups)...), layerOf, opt) {
							g.Box, g.Rot, g.Placed = s.box, rot, true
							break search
						}
					}
				}
			}
		}
		if g.Placed {
			layerOf[fmt.Sprintf("group-%d", len(groups))] = g.Layer
			groups = append(groups, g)
			notes = append(notes, fmt.Sprintf("group %q drawn at (%.1f,%.1f); %d designator(s) hidden", g.Text, g.Box.cx(), g.Box.cy(), len(ms)))
			return
		}
		if len(ms) <= 2 {
			notes = append(notes, fmt.Sprintf("group %q: no slot within %.0f mil of its parts", g.Text, opt.GroupMaxDist))
			for _, m := range ms {
				single(m)
			}
			return
		}
		row := g.Members.w() >= g.Members.h()
		sort.SliceStable(ms, func(i, j int) bool {
			if row {
				return ms[i].Own.cx() < ms[j].Own.cx()
			}
			return ms[i].Own.cy() < ms[j].Own.cy()
		})
		h := len(ms) / 2
		try(append([]silkLabel{}, ms[:h]...))
		try(append([]silkLabel{}, ms[h:]...))
	}
	for _, r := range roots {
		try(byRoot[r])
	}
	return groups, append(keep, back...), notes
}

// maxGroupChars bounds a group label's text ("C27/C46/C72/R60" is 15).
const maxGroupChars = 24

// groupPlaced turns placed groups into obstacles for the next ones.
func groupPlaced(gs []silkGroup) []silkPlaced {
	var out []silkPlaced
	for i, g := range gs {
		out = append(out, silkPlaced{ID: fmt.Sprintf("group-%d", i), Ref: g.Text, Box: g.Box})
	}
	return out
}

// silkGate judges the READBACK: every visible designator must keep off
// other silk text, pads (and vias when they are openings), holes and the
// board edge, stay inside the board, sit within MaxDist of its own
// footprint, and be at least the project size and the fab stroke.
func silkGate(snap *boardSnapshot, font float64, opt silkTightOpts) gateResult {
	type bad struct {
		sev  float64
		text string
	}
	var bads []bad
	add := func(sev float64, f string, a ...any) { bads = append(bads, bad{sev, fmt.Sprintf(f, a...)}) }
	comps := map[string]boardComp{}
	byRef := map[string]boardComp{}
	var pads []struct {
		b     silkBox
		layer int
		ref   string
	}
	for _, c := range snap.Components {
		comps[c.ID] = c
		byRef[c.Designator] = c
		for _, p := range c.Pads {
			pads = append(pads, struct {
				b     silkBox
				layer int
				ref   string
			}{padBox(p).grow(2), p.Layer, c.Designator}) // 2 mil soldermask expansion
		}
	}
	var holes []silkBox
	for _, h := range snap.FootprintHoles {
		if h.Shape == "circle" {
			holes = append(holes, silkBoxAt(h.X, h.Y, h.Dia, h.Dia))
		}
	}
	if opt.ViaOpening && snap.Copper != nil {
		var vias []widenVia
		if decodeAny(snap.Copper.Vias, &vias) == nil {
			for _, v := range vias {
				holes = append(holes, silkBoxAt(v.X, v.Y, v.Diameter, v.Diameter))
			}
		}
	}
	var texts []pcbSilkText
	for _, t := range snap.Silk {
		if !t.Hidden && t.BBox != nil && (t.Layer == silkTopLayer || t.Layer == silkBottomLayer) {
			texts = append(texts, t)
		}
	}
	// Group labels ("C21–C24", "R62/R63/R65"): free strings naming refs.
	type grp struct {
		text    string
		box     silkBox
		members silkBox
		ok      bool
	}
	groupOf := map[string][]grp{}
	isGroup := map[int]grp{}
	for i, t := range texts {
		if t.Kind != "string" {
			continue
		}
		refs := groupRefs(t.Text)
		if refs == nil {
			continue
		}
		g := grp{text: t.Text, box: silkBox(*t.BBox)}
		for _, r := range refs {
			if c, ok := byRef[r]; ok && c.BBox != nil {
				cb := silkBox{c.BBox.MinX, c.BBox.MinY, c.BBox.MaxX, c.BBox.MaxY}
				if !g.ok {
					g.members, g.ok = cb, true
				} else {
					g.members = silkBox{math.Min(g.members.MinX, cb.MinX), math.Min(g.members.MinY, cb.MinY), math.Max(g.members.MaxX, cb.MaxX), math.Max(g.members.MaxY, cb.MaxY)}
				}
			}
		}
		isGroup[i] = g
		for _, r := range refs {
			groupOf[r] = append(groupOf[r], g)
		}
	}
	var info []string
	// A hidden designator of a part with pads must be named by a group
	// label within GroupMaxDist of its group's parts (hidden, never deleted).
	for _, t := range snap.Silk {
		if !t.Hidden || t.Kind != "attribute" || !strings.EqualFold(t.Key, "Designator") || strings.TrimSpace(t.Text) == "" {
			continue
		}
		c, ok := comps[t.CompID]
		if !ok || len(c.Pads) == 0 || c.BBox == nil {
			continue
		}
		cb := silkBox{c.BBox.MinX, c.BBox.MinY, c.BBox.MaxX, c.BBox.MaxY}
		covered := ""
		for _, g := range groupOf[t.Text] {
			if g.ok && g.box.dist(g.members) <= opt.GroupMaxDist {
				covered = g.text
				break
			}
		}
		if covered == "" {
			add(85, "%s (%.1f,%.1f) designator hidden and named by no group label within %.0f mil", t.Text, cb.cx(), cb.cy(), opt.GroupMaxDist)
		} else {
			info = append(info, fmt.Sprintf("%s hidden, named by group label %q", t.Text, covered))
		}
	}
	n := 0
	for i, t := range texts {
		g, group := isGroup[i]
		if !isVisibleDesignator(t) && !group {
			continue
		}
		n++
		b := silkBox(*t.BBox)
		at := fmt.Sprintf("(%.1f,%.1f)", b.cx(), b.cy())
		for j, o := range texts {
			if j != i && o.Layer == t.Layer && b.overlaps(silkBox(*o.BBox)) && (j > i || !isVisibleDesignator(o)) {
				add(100, "%s %s overlaps silk %q", t.Text, at, o.Text)
			}
		}
		side := pcbSideTop
		if t.Layer == silkBottomLayer {
			side = pcbSideBottom
		}
		for _, p := range pads {
			if (p.layer == side || p.layer == pcbLayerMulti) && b.overlaps(p.b) {
				add(90, "%s %s overlaps a pad/soldermask opening of %s", t.Text, at, p.ref)
				break
			}
		}
		for _, h := range holes {
			if b.overlaps(h) {
				add(90, "%s %s overlaps a hole/opening at (%.1f,%.1f)", t.Text, at, h.cx(), h.cy())
				break
			}
		}
		if snap.Outline != nil {
			out := 0
			for _, p := range [][2]float64{{b.MinX, b.MinY}, {b.MaxX, b.MinY}, {b.MinX, b.MaxY}, {b.MaxX, b.MaxY}} {
				if !snap.Outline.containsPoint(p[0], p[1]) {
					out++
				}
			}
			switch {
			case out == 4:
				add(95, "%s %s is outside the board", t.Text, at)
			case out > 0:
				add(95, "%s %s crosses the board edge", t.Text, at)
			}
		}
		if group {
			if !g.ok {
				add(70, "group label %q %s names no part on the board", t.Text, at)
			} else if d := b.dist(g.members); d > opt.GroupMaxDist {
				add(50+d/10, "group label %q %s is %.1f mil from its parts (max %.0f)", t.Text, at, d, opt.GroupMaxDist)
			}
		} else if c, ok := comps[t.CompID]; ok && c.BBox != nil {
			if d := b.dist(silkBox{c.BBox.MinX, c.BBox.MinY, c.BBox.MaxX, c.BBox.MaxY}); d > opt.MaxDist {
				add(50+d/10, "%s %s is %.1f mil from its footprint (max %.0f)", t.Text, at, d, opt.MaxDist)
			}
		}
		if font > 0 && t.FontSize > 0 && t.FontSize < font-0.05 {
			add(80, "%s %s text %.1f mil below the project size %.1f", t.Text, at, t.FontSize, font)
		}
		if t.LineWidth > 0 && t.LineWidth < opt.FabMinLine-0.05 {
			add(80, "%s %s stroke %.2f mil below the fab minimum %.2f", t.Text, at, t.LineWidth, opt.FabMinLine)
		}
	}
	sort.SliceStable(bads, func(i, j int) bool { return bads[i].sev > bads[j].sev })
	g := gateResult{Gate: "silkscreen", Pass: len(bads) == 0, Info: info}
	g.Detail = fmt.Sprintf("%d designator / group label(s) checked on the readback, %d hidden designator(s) named by group labels, %d problem(s); project size %.1f mil, max %.0f mil from the footprint, fab stroke ≥ %.2f mil", n, len(info), len(bads), font, opt.MaxDist, opt.FabMinLine)
	if n == 0 {
		g.Pass = false
		g.Detail = "no designator silk read back (pcb.silk.list unavailable?)"
	}
	for i, b := range bads {
		if i == 40 {
			g.Items = append(g.Items, fmt.Sprintf("… %d more", len(bads)-40))
			break
		}
		g.Items = append(g.Items, b.text)
	}
	return g
}

// silkTightReport is the result of runSilkTight.
type silkTightReport struct {
	FontSize float64      `json:"fontSize"`
	Rounds   int          `json:"rounds"`
	Moved    int          `json:"moved"`
	Placed   []silkPlaced `json:"placed,omitempty"`
	Notes    []string     `json:"notes,omitempty"`
	// Groups are the group labels drawn (their members' designators hidden).
	Groups     []silkGroup `json:"groups,omitempty"`
	GroupNotes []string    `json:"groupNotes,omitempty"`
	Gate       gateResult  `json:"gate"`
}

// runSilkTight plans on the live board dump, applies the plan in two steps
// (rotation / size first, then the position from each label's own new
// anchor offset, which the host decides), reads the silk back and repeats
// for the labels the readback still fails, up to rounds. The verdict is
// silkGate on the final readback.
func runSilkTight(cfg *appConfig, window string, opt silkTightOpts, rounds int, dryRun bool, stderr io.Writer) (*silkTightReport, error) {
	rep := &silkTightReport{}
	read := func() (*boardSnapshot, error) {
		return fetchBoardSnapshot(cfg, window, boardSnapshotOpts{withSilk: true, withCopper: true, withFootprintHoles: true})
	}
	snap, err := read()
	if err != nil {
		return nil, err
	}
	for round := 1; round <= max(rounds, 1); round++ {
		labels, sc, font := silkTightInput(snap, opt)
		rep.FontSize = font
		placed, notes := planSilkTight(labels, sc, opt)
		var groups []silkGroup
		if !opt.NoGroups {
			var gnotes []string
			groups, placed, gnotes = planSilkGroups(labels, sc, placed, opt)
			rep.GroupNotes = append(rep.GroupNotes, gnotes...)
		}
		rep.Placed, rep.Notes, rep.Rounds = placed, notes, round
		byID := map[string]silkLabel{}
		for _, l := range labels {
			byID[l.ID] = l
		}
		var moves []silkPlaced
		for _, p := range placed {
			if p.Moved {
				moves = append(moves, p)
			}
		}
		fmt.Fprintf(stderr, "silk tight round %d: %d of %d designator(s) to move, %d group label(s), %d note(s)\n", round, len(moves), len(labels), len(groups), len(notes))
		if dryRun {
			rep.Groups = groups
			break
		}
		if len(moves) == 0 && len(groups) == 0 {
			break
		}
		// Step 1: rotation, size and stroke, batched by identical props.
		batch := map[string][]string{}
		props := map[string]map[string]any{}
		for _, p := range moves {
			l := byID[p.ID]
			pr := map[string]any{}
			if l.Rot != p.Rot {
				pr["rotation"] = p.Rot
			}
			if l.Font > 0 && l.Font < font {
				pr["fontSize"] = font
			}
			if opt.LineWidth > 0 {
				pr["lineWidth"] = opt.LineWidth
			}
			if len(pr) == 0 {
				continue
			}
			k := fmt.Sprint(pr)
			batch[k] = append(batch[k], p.ID)
			props[k] = pr
		}
		for k, ids := range batch {
			pl := map[string]any{"primitiveIds": ids}
			for kk, v := range props[k] {
				pl[kk] = v
			}
			if _, err := requestAction(cfg, "pcb.silk.set", window, pl); err != nil {
				return rep, fmt.Errorf("silk rotate/size: %w", err)
			}
		}
		// Step 2: position from the new rendered box and anchor.
		cur, err := fetchPcbSilk(cfg, window)
		if err != nil {
			return rep, err
		}
		now := map[string]pcbSilkText{}
		for _, t := range cur {
			now[t.ID] = t
		}
		for _, p := range moves {
			t, ok := now[p.ID]
			if !ok || t.BBox == nil {
				continue
			}
			x := p.Box.cx() - (t.BBox.MaxX-t.BBox.MinX)/2 + (t.X - t.BBox.MinX)
			y := p.Box.cy() - (t.BBox.MaxY-t.BBox.MinY)/2 + (t.Y - t.BBox.MinY)
			if _, err := requestAction(cfg, "pcb.silk.set", window, map[string]any{"primitiveIds": []string{p.ID}, "x": x, "y": y}); err != nil {
				return rep, fmt.Errorf("silk move %s: %w", p.Ref, err)
			}
			rep.Moved++
		}
		// Group labels: one string each, then the members' designators
		// hidden (the attribute, BOM and placement data stay).
		for _, g := range groups {
			lw := opt.LineWidth
			if lw <= 0 {
				lw = 6
			}
			res, err := requestAction(cfg, "pcb.silk.add", window, map[string]any{"text": g.Text, "x": g.Box.MinX, "y": g.Box.MinY,
				"layer": g.Layer, "fontSize": font, "lineWidth": lw, "rotation": g.Rot})
			if err != nil {
				return rep, fmt.Errorf("group label %q: %w", g.Text, err)
			}
			id, _ := res.Result["primitiveId"].(string)
			if bm, ok := res.Result["bbox"].(map[string]any); ok && id != "" {
				minX, _ := asFloatOK(bm["minX"])
				minY, _ := asFloatOK(bm["minY"])
				maxX, _ := asFloatOK(bm["maxX"])
				maxY, _ := asFloatOK(bm["maxY"])
				ax, _ := asFloatOK(res.Result["x"])
				ay, _ := asFloatOK(res.Result["y"])
				if maxX > minX && maxY > minY {
					x := g.Box.cx() - (maxX-minX)/2 + (ax - minX)
					y := g.Box.cy() - (maxY-minY)/2 + (ay - minY)
					if _, err := requestAction(cfg, "pcb.silk.set", window, map[string]any{"primitiveIds": []string{id}, "x": x, "y": y}); err != nil {
						return rep, fmt.Errorf("group label %q move: %w", g.Text, err)
					}
				}
			}
			if _, err := requestAction(cfg, "pcb.silk.set", window, map[string]any{"primitiveIds": g.IDs, "valueVisible": false}); err != nil {
				return rep, fmt.Errorf("hide %v under group label %q (the connector needs valueVisible support: rebuild and import it, 'make eext'): %w", g.Refs, g.Text, err)
			}
			rep.Groups = append(rep.Groups, g)
		}
		if snap, err = read(); err != nil {
			return rep, err
		}
		if g := silkGate(snap, font, opt); g.Pass {
			break
		}
	}
	_, _, font := silkTightInput(snap, opt)
	rep.Gate = silkGate(snap, font, opt)
	return rep, nil
}

// addSilkTightFlags registers the placement / gate limits; prefix keeps
// them apart from other flags ("silk-" on pcb auto route / pcb gate).
func addSilkTightFlags(c *cobra.Command, o *silkTightOpts, prefix string) {
	c.Flags().Float64Var(&o.Gap, prefix+"gap", o.Gap, "first gap between a designator and its own footprint box (mil)")
	c.Flags().Float64Var(&o.MaxDist, prefix+"max-dist", o.MaxDist, "farthest a designator may sit from its own footprint (mil); the silkscreen gate fails above it")
	c.Flags().Float64Var(&o.FontSize, prefix+"font-size", 0, "project designator height (mil); 0 = the most common on the board. Never shrunk; the gate fails below it")
	c.Flags().Float64Var(&o.LineWidth, prefix+"line-width", 0, "set moved designators to this stroke (mil); 0 = leave")
	c.Flags().Float64Var(&o.FabMinLine, prefix+"fab-min-line", o.FabMinLine, "fab minimum silk stroke (mil; JLC 0.15 mm)")
	c.Flags().Float64Var(&o.PadClear, prefix+"pad-clear", o.PadClear, "designator to pad / via / hole clearance (mil; JLC silk-to-pad 0.15 mm)")
	c.Flags().BoolVar(&o.ViaOpening, prefix+"via-openings", false, "gate: vias are soldermask openings (not tented)")
	c.Flags().Float64Var(&o.GroupMaxDist, prefix+"group-max-dist", o.GroupMaxDist, "a group label (\"C21–C24\", \"R62/R63/R65\") sits within this of its parts' box (mil)")
	c.Flags().Float64Var(&o.GroupLink, prefix+"group-link", o.GroupLink, "unplaceable designators whose footprints are within this of each other share one group label (mil)")
	c.Flags().BoolVar(&o.NoGroups, prefix+"no-groups", false, "never draw group labels / hide designators: report what has no slot")
}
