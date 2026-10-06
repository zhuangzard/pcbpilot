package pcbauto

// placer_label.go — every part keeps room for its own designator.
//
// Why: on Gas Module V5 A and B the silk step (pcb silk-align --tight)
// found no legal slot within 30 mil for 18–25 designators in dense spots
// (decaps around the CPLD, RC filters), and the user forbids shrinking the
// text. Room for the label has to be made while placing: a part whose
// label has no free slot on any side pays labelCost, so the annealer and
// polish move it (or its neighbours) apart just enough.

import "math"

// LabelSpec sizes the designator each part must keep room for (mil). The
// app derives it from the board's silk (most common designator height,
// average character width); nil = no label term.
type LabelSpec struct {
	Height float64 `json:"height"` // text height across the baseline
	CharW  float64 `json:"charW"`  // advance per character along the baseline
	Gap    float64 `json:"gap"`    // label ↔ own footprint
	Clear  float64 `json:"clear"`  // label ↔ other parts' bodies (pads inside)
}

// labelWeight prices a blocked label like an overlap of a quarter of the
// label's area (overlaps cost 8 × area): strong enough to make room, weaker
// than any legality term.
const labelWeight = 2.0

// labelSize is p's label box along / across its baseline.
func (ls *LabelSpec) labelSize(p *Part) (float64, float64) {
	return ls.CharW * float64(len([]rune(p.Ref))), ls.Height
}

// labelSlots lists p's candidate label boxes: the four sides of its body at
// Gap, lying (w×h) and standing (h×w), centred and flush with either end.
func (ls *LabelSpec) labelSlots(p *Part) []Rect {
	bd := p.Body()
	ln, ht := ls.labelSize(p)
	var out []Rect
	for _, wh := range [2][2]float64{{ln, ht}, {ht, ln}} {
		w, h := wh[0], wh[1]
		for _, x := range []float64{bd.Center().X - w/2, bd.MinX, bd.MaxX - w} {
			out = append(out,
				Rect{x, bd.MaxY + ls.Gap, x + w, bd.MaxY + ls.Gap + h},
				Rect{x, bd.MinY - ls.Gap - h, x + w, bd.MinY - ls.Gap})
		}
		for _, y := range []float64{bd.Center().Y - h/2, bd.MinY, bd.MaxY - h} {
			out = append(out,
				Rect{bd.MinX - ls.Gap - w, y, bd.MinX - ls.Gap, y + h},
				Rect{bd.MaxX + ls.Gap, y, bd.MaxX + ls.Gap + w, y + h})
		}
	}
	return out
}

// labelFree reports whether some slot of p's label is inside the placement
// region and clear of every other part's body (grown by Clear).
func (pl *placer) labelFree(p *Part) bool {
	ls := pl.opt.Labels
	for _, s := range ls.labelSlots(p) {
		if s.MinX < pl.region.MinX || s.MinY < pl.region.MinY || s.MaxX > pl.region.MaxX || s.MaxY > pl.region.MaxY {
			continue
		}
		g := s.Expand(ls.Clear)
		free := true
		pl.forBuckets(g, func(q *Part) {
			if free && q != p && q.Body().OverlapArea(g) > 1e-6 {
				free = false
			}
		})
		if free {
			return true
		}
	}
	return false
}

// labelCost is p's share for a blocked label (0 when a slot is free or the
// spec is off).
func (pl *placer) labelCost(p *Part) float64 {
	if pl.opt.Labels == nil || p.Ref == "" {
		return 0
	}
	if pl.labelFree(p) {
		return 0
	}
	w, h := pl.opt.Labels.labelSize(p)
	return labelWeight * w * h
}

// labelBlocked counts the movable parts whose label has no free slot.
func (pl *placer) labelBlocked() int {
	if pl.opt.Labels == nil {
		return 0
	}
	n := 0
	for _, p := range pl.b.Parts {
		if pl.c.Kinds[p.Ref] == KindMechanical || p.Ref == "" {
			continue
		}
		if !pl.labelFree(p) {
			n++
		}
	}
	return n
}

// labelSpecValid guards a spec read from the board.
func labelSpecValid(ls *LabelSpec) bool {
	return ls != nil && ls.Height > 0 && ls.CharW > 0 && !math.IsNaN(ls.Height+ls.CharW)
}
