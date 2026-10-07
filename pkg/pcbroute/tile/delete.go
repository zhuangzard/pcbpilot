package tile

import "github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"

// Delete removes the obstacle id and reports whether it existed. The area of
// its staircase is cleared and every other obstacle there is painted again,
// so owners stay the union of the remaining obstacles (spec 01 §3.3: after a
// rip-up only the affected area is updated).
func (p *Plane) Delete(id uint32) bool {
	s, ok := p.slots[id]
	if !ok {
		return false
	}
	old := p.obs[s].rects
	p.grid.Delete(s)
	p.obs[s] = obstacle{}
	delete(p.slots, id)
	p.reset(old)
	return true
}

// Refine re-covers every obstacle whose staircase meets window with half the
// step and twice the corner steps (spec 01 §5: a gap closed by the staircase
// approximation is reopened locally; at most 2 refinements per obstacle). It
// returns how many obstacles changed.
func (p *Plane) Refine(window geom.Rect) int {
	var hit []int32
	p.grid.Search(window, func(s int32) bool {
		if p.obs[s].level < maxRefine && touches(p.obs[s].rects, window) {
			hit = append(hit, s)
		}
		return true
	})
	for _, s := range hit {
		ob := p.obs[s]
		old := ob.rects
		ob.level++
		ob.rects = p.cover(&ob)
		p.grid.Delete(s)
		delete(p.slots, ob.ID)
		p.obs[s] = obstacle{}
		p.register(ob)
		p.reset(old)
		p.paint(ob.rects, func(o []geom.NetID) []geom.NetID { return p.with(o, ob.Net) })
	}
	return len(hit)
}

func touches(rs []geom.Rect, w geom.Rect) bool {
	for _, r := range rs {
		if r.Intersects(w) {
			return true
		}
	}
	return false
}

// reset clears rs and paints back the parts of every registered obstacle
// that fall inside them.
func (p *Plane) reset(rs []geom.Rect) {
	p.paint(rs, func([]geom.NetID) []geom.NetID { return nil })
	var part []geom.Rect
	for _, r := range rs {
		p.grid.Search(r, func(s int32) bool {
			ob := &p.obs[s]
			part = part[:0]
			for _, q := range ob.rects {
				if c := clip(q, r); !c.Empty() {
					part = append(part, c)
				}
			}
			p.paint(part, func(o []geom.NetID) []geom.NetID { return p.with(o, ob.Net) })
			return true
		})
	}
}
