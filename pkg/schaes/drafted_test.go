package schaes

import "math"

// drafted is the "careful drafter" reference wiring for a real placement:
//   - a signal net whose pins all lie within 120 units is drawn as a direct
//     wire tree (no labels — schematic.md: adjacent peripherals use real wires);
//   - other signal pins get an outward stub + net port;
//   - power flags point up and ground flags point down (an outward stub turns
//     once when the pin faces sideways);
//   - every stub tries offsets 20/30/40/50/10 and takes the first that touches
//     no foreign pin or wire and whose marker box clears bodies and markers.
func drafted(base *Snapshot) *Snapshot {
	s := cloneSnap(base)
	s.Source, s.HasWires, s.HasMarkers, s.Notes = "synthetic-drafted", true, true, nil
	type pinRef struct {
		p   Pt
		dir string
	}
	byNet := map[string][]pinRef{}
	var allPins []Pt
	for _, p := range s.Parts {
		for _, q := range p.Pins {
			allPins = append(allPins, Pt{q.X, q.Y})
			if q.Net == "" || q.NC || q.Dir == "" {
				continue
			}
			byNet[q.Net] = append(byNet[q.Net], pinRef{Pt{q.X, q.Y}, q.Dir})
		}
	}
	// segHits: does [a,b] touch a pin other than the allowed terminals, touch
	// or cross any existing wire anywhere but an allowed terminal, or run
	// through a body?
	segHits := func(a, b Pt, allowed ...Pt) bool {
		ok := func(p Pt) bool {
			for _, q := range allowed {
				if near(p, q) {
					return true
				}
			}
			return false
		}
		sg := &seg{a: a, b: b, length: math.Hypot(b.X-a.X, b.Y-a.Y)}
		for _, q := range allPins {
			if !ok(q) && onSegment(a, b, q) {
				return true
			}
		}
		for _, w := range s.Wires {
			for k := 0; k+1 < len(w.Pts); k++ {
				o := &seg{a: w.Pts[k], b: w.Pts[k+1]}
				o.length = math.Hypot(o.b.X-o.a.X, o.b.Y-o.a.Y)
				for _, c := range []Pt{a, b} {
					if onSegment(o.a, o.b, c) && !ok(c) {
						return true
					}
				}
				for _, c := range []Pt{o.a, o.b} {
					if onSegment(a, b, c) && !ok(c) {
						return true
					}
				}
				if _, x := crossPoint(sg, o); x {
					return true
				}
				if collinearOverlap(sg, o) > eps {
					return true
				}
			}
		}
		for _, p := range s.Parts {
			if segClipLen(sg, p.Box.Grow(-0.5)) > 1 {
				return true
			}
		}
		for _, m := range s.Markers {
			if segClipLen(sg, m.Box.Grow(-0.5)) > 1 {
				return true
			}
		}
		return false
	}
	boxClear := func(b Box) bool {
		for _, p := range s.Parts {
			if overlapArea(b, p.Box) > 1 {
				return false
			}
		}
		for _, m := range s.Markers {
			if overlapArea(b, m.Box) > 1 {
				return false
			}
		}
		return true
	}
	place := func(net string, pr pinRef, kind string) {
		v := dirVec[pr.dir]
		want := pr.dir
		switch kind {
		case KindPower:
			if pr.dir != "down" {
				want = "up"
			}
		case KindGround:
			if pr.dir != "up" {
				want = "down"
			}
		}
		var bestPts []Pt
		var bestA Pt
		for _, off := range []float64{20, 30, 40, 50, 10} {
			pts := []Pt{pr.p}
			end := Pt{pr.p.X + off*v.X, pr.p.Y + off*v.Y}
			if want != pr.dir {
				mid := Pt{pr.p.X + 10*v.X, pr.p.Y + 10*v.Y}
				w := dirVec[want]
				end = Pt{mid.X + off*w.X, mid.Y + off*w.Y}
				pts = append(pts, mid)
			}
			pts = append(pts, end)
			ok := true
			for k := 0; k+1 < len(pts); k++ {
				if segHits(pts[k], pts[k+1], pr.p) {
					ok = false
				}
			}
			if ok && boxClear(predictMarkerBox(kind, net, end, want)) {
				bestPts, bestA = pts, end
				break
			}
		}
		if bestPts == nil {
			// no clean stub: leave the pin unwired rather than fabricate a short
			s.Notes = append(s.Notes, "drafted: no clean stub for "+net)
			return
		}
		s.Wires = append(s.Wires, Wire{Net: net, Pts: bestPts})
		s.Markers = append(s.Markers, Marker{Kind: kind, Net: net, Anchor: bestA, Dir: want, Box: predictMarkerBox(kind, net, bestA, want), Estimated: true})
	}
	for _, net := range sortedKeys(byNet) {
		prs := byNet[net]
		kind := KindNetPort
		switch {
		case IsGroundNet(net):
			kind = KindGround
		case IsPowerNet(net):
			kind = KindPower
		}
		if kind == KindNetPort && len(prs) >= 2 {
			var b Box
			for _, pr := range prs {
				b = b.Union(Box{pr.p.X, pr.p.Y, pr.p.X + 1e-6, pr.p.Y + 1e-6})
			}
			if b.W()+b.H() < 120 {
				var tree []Wire
				free := true
				for i := 1; i < len(prs) && free; i++ {
					a, c := prs[i-1].p, prs[i].p
					pts := []Pt{a, c}
					if a.X != c.X && a.Y != c.Y {
						pts = []Pt{a, {c.X, a.Y}, c}
					}
					for k := 0; k+1 < len(pts); k++ {
						if segHits(pts[k], pts[k+1], a, c) {
							free = false
						}
					}
					tree = append(tree, Wire{Net: net, Pts: pts})
				}
				if free {
					s.Wires = append(s.Wires, tree...)
					continue
				}
			}
		}
		for _, pr := range prs {
			place(net, pr, kind)
		}
	}
	return s
}
