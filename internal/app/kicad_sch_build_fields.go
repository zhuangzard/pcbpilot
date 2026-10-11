package app

import (
	"math"
	"sort"
	"strconv"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbFieldShift moves a part's Reference / Value text off the default spot of
// sbFields (mm, zone-local; rigid with the zone).
type sbFieldShift struct {
	Ref kicad.Pt `json:"ref"`
	Val kicad.Pt `json:"val"`
}

func sbFieldKey(ref string, unit int) string { return ref + "/" + strconv.Itoa(max(unit, 1)) }

// sbRecomputeBox is the zone box after its texts moved.
func sbRecomputeBox(d *sbDesign, zi *sbZoneIn, zl *sbZoneLayout, m *sbMeasure) kicad.Box {
	return zoneBox(zl, zi, map[string]kicad.SceneSymbol{}, m)
}

func sbShiftBox(b kicad.Box, d kicad.Pt) kicad.Box {
	return kicad.Box{MinX: b.MinX + d.X, MinY: b.MinY + d.Y, MaxX: b.MaxX + d.X, MaxY: b.MaxY + d.Y}
}

// fieldPlan is sbFields with the zone's repaired positions applied.
func (zl *sbZoneLayout) fieldPlan(s kicad.SceneSymbol, pose sbPose, ref, value string) sbFieldPlan {
	f := sbFields(s, pose, ref, value)
	if sh, ok := zl.Fields[sbFieldKey(ref, pose.Unit)]; ok {
		f.Ref = kicad.Pt{X: sbRound4(f.Ref.X + sh.Ref.X), Y: sbRound4(f.Ref.Y + sh.Ref.Y)}
		f.Val = kicad.Pt{X: sbRound4(f.Val.X + sh.Val.X), Y: sbRound4(f.Val.Y + sh.Val.Y)}
		f.RefBox, f.VBox = sbShiftBox(f.RefBox, sh.Ref), sbShiftBox(f.VBox, sh.Val)
	}
	return f
}

// sbRepairFields moves the Reference / Value texts that clash with another
// drawn thing (EG-14) to the nearest clear spot around their part, judged on
// the zone rendered alone with the engineer-grade geometry. Parts, wires and
// markers do not move; a text with no clear spot stays (the gate reports it).
func sbRepairFields(d *sbDesign, zi *sbZoneIn, zl *sbZoneLayout) {
	for iter := 0; iter < 6; iter++ {
		sc, t, ok := sbZoneScene(d, zi, zl)
		if !ok {
			return
		}
		items := sbSceneItems(sc)
		moved := map[int]bool{}
		for i := range items {
			for j := i + 1; j < len(items); j++ {
				if !sbClashes(items[i], items[j]) {
					continue
				}
				switch {
				case items[j].field != "" && !moved[j]:
					moved[j] = true
				case items[i].field != "" && !moved[i]:
					moved[i] = true
				}
			}
		}
		if len(moved) == 0 {
			return
		}
		var idx []int
		for i := range moved {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		progress := false
		for _, i := range idx {
			it := items[i]
			s := sc.Symbols[it.sym]
			nb, ok := sbClearSpot(sc, items, i, s)
			if !ok {
				continue
			}
			cur := kicad.Pt{X: (it.box.MinX + it.box.MaxX) / 2, Y: (it.box.MinY + it.box.MaxY) / 2}
			nc := kicad.Pt{X: (nb.MinX + nb.MaxX) / 2, Y: (nb.MinY + nb.MaxY) / 2}
			key := sbFieldKey(s.Ref, max(s.Unit, 1))
			if zl.Fields == nil {
				zl.Fields = map[string]sbFieldShift{}
			}
			sh := zl.Fields[key]
			dv := kicad.Pt{X: nc.X - cur.X, Y: nc.Y - cur.Y}
			if it.field == "Reference" {
				sh.Ref = kicad.Pt{X: sh.Ref.X + dv.X, Y: sh.Ref.Y + dv.Y}
			} else {
				sh.Val = kicad.Pt{X: sh.Val.X + dv.X, Y: sh.Val.Y + dv.Y}
			}
			zl.Fields[key] = sh
			items[i].box = nb
			progress = true
		}
		_ = t
		if !progress {
			return
		}
	}
}

// sbClearSpot finds the nearest candidate box for field item i that clashes
// with no other item, touches no wire, and stays off its own part.
func sbClearSpot(sc *kicad.SchScene, items []sbItem, i int, s kicad.SceneSymbol) (kicad.Box, bool) {
	it := items[i]
	w, h := it.box.W(), it.box.H()
	cur := kicad.Pt{X: (it.box.MinX + it.box.MaxX) / 2, Y: (it.box.MinY + it.box.MaxY) / 2}
	body := s.Box
	if s.HasBox {
		body = kicad.Box{MinX: math.Min(s.Box.MinX, s.Body.MinX), MinY: math.Min(s.Box.MinY, s.Body.MinY), MaxX: math.Max(s.Box.MaxX, s.Body.MaxX), MaxY: math.Max(s.Box.MaxY, s.Body.MaxY)}
	}
	bcx, bcy := (body.MinX+body.MaxX)/2, (body.MinY+body.MaxY)/2
	var cands []kicad.Pt
	const gap, step = 0.9, 0.635
	for k := 0; k < 5; k++ {
		off := float64(k) * 1.0
		ys := []float64{body.MinY - gap - h/2 - off, body.MaxY + gap + h/2 + off}
		for _, y := range ys {
			for _, x := range []float64{bcx, body.MinX + w/2, body.MaxX - w/2, body.MinX - w/2, body.MaxX + w/2} {
				cands = append(cands, kicad.Pt{X: x, Y: y})
			}
		}
		xs := []float64{body.MinX - gap - w/2 - off, body.MaxX + gap + w/2 + off}
		for _, x := range xs {
			for _, y := range []float64{bcy, bcy - 1.5, bcy + 1.5, body.MinY + h/2, body.MaxY - h/2} {
				cands = append(cands, kicad.Pt{X: x, Y: y})
			}
		}
	}
	snap := func(v float64) float64 { return math.Round(v/step) * step }
	best, bestD, found := kicad.Box{}, math.Inf(1), false
	for _, c := range cands {
		c = kicad.Pt{X: snap(c.X), Y: snap(c.Y)}
		b := kicad.Box{MinX: c.X - w/2, MinY: c.Y - h/2, MaxX: c.X + w/2, MaxY: c.Y + h/2}
		if sbOverlap(sbGrow(b, 0.3), body) {
			continue
		}
		trial := it
		trial.box = b
		clear := true
		for j, o := range items {
			if j != i && sbClashes(trial, o) {
				clear = false
				break
			}
		}
		if !clear {
			continue
		}
		for _, wr := range sc.Wires {
			sb := kicad.Box{MinX: math.Min(wr[0].X, wr[1].X), MinY: math.Min(wr[0].Y, wr[1].Y), MaxX: math.Max(wr[0].X, wr[1].X), MaxY: math.Max(wr[0].Y, wr[1].Y)}
			if sbOverlap(sbGrow(sb, 0.05), sbGrow(b, 0.05)) {
				clear = false
				break
			}
		}
		if !clear {
			continue
		}
		if dd := math.Hypot(c.X-cur.X, c.Y-cur.Y); dd < bestD {
			best, bestD, found = b, dd, true
		}
	}
	return best, found
}
