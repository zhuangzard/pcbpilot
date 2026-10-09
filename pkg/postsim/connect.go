package postsim

import (
	"math"
	"sort"
)

// Copper connectivity of a board dump, independent of any router report:
// the route-complete gate for every router (fastroute, an external
// --router command, the built-in router, hand routing). Per net, pads,
// tracks (and thermal spokes), vias and real area copper (poured fills,
// static fills, planes — not unpoured pour outlines) are joined when their
// exact geometry touches on a shared layer (the same rules as buildNet's
// overlap seeding). A net whose pads end up in more than one island has
// islands-1 unrouted connections.

// OpenNet is a net whose pads are not all joined by copper.
type OpenNet struct {
	Net string `json:"net"`
	// Islands lists the pads (REF.PIN) of each copper island, largest first.
	Islands  [][]string `json:"islands"`
	Unrouted int        `json:"unrouted"` // len(Islands) - 1
}

// CopperConnectivity returns every open net of b, sorted by name.
func CopperConnectivity(b *Board) []OpenNet {
	type item struct {
		kind  int // 0 pad, 1 track, 2 via, 3 area
		pad   *Pad
		name  string
		track Track
		via   Via
		area  *Area
	}
	byNet := map[string][]item{}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net != "" {
				byNet[pd.Net] = append(byNet[pd.Net], item{kind: 0, pad: pd, name: p.Ref + "." + pd.Number})
			}
		}
	}
	for _, t := range b.Tracks {
		if t.Net != "" {
			byNet[t.Net] = append(byNet[t.Net], item{kind: 1, track: t})
		}
	}
	for _, v := range b.Vias {
		if v.Net != "" {
			byNet[v.Net] = append(byNet[v.Net], item{kind: 2, via: v})
		}
	}
	for i := range b.Areas {
		a := &b.Areas[i]
		if a.Net != "" && a.Kind != "pour" {
			byNet[a.Net] = append(byNet[a.Net], item{kind: 3, area: a})
		}
	}
	onLayer := func(it item, l int) bool {
		switch it.kind {
		case 0:
			return it.pad.OnLayer(l)
		case 1:
			return it.track.Layer == l
		case 2:
			return true
		}
		return it.area.Layer == l
	}
	layerOf := func(it item) int {
		switch it.kind {
		case 0:
			return it.pad.Layer
		case 1:
			return it.track.Layer
		case 2:
			return LayerMulti
		}
		return it.area.Layer
	}
	inArea := func(a *Area, p Point) bool {
		return p.X >= a.Box.MinX && p.X <= a.Box.MaxX && p.Y >= a.Box.MinY && p.Y <= a.Box.MaxY && insideEvenOdd(p, a.Contours)
	}
	ring := func(c Point, r float64) []Point {
		out := []Point{c}
		for k := 0; k < 8; k++ {
			th := float64(k) * math.Pi / 4
			out = append(out, Point{c.X + r*math.Cos(th), c.Y + r*math.Sin(th)})
		}
		return out
	}
	touch := func(x, y item) bool {
		// shared layer
		lx, ly := layerOf(x), layerOf(y)
		var layer int
		switch {
		case lx == LayerMulti && ly == LayerMulti:
			if x.kind == 2 || y.kind == 2 || (x.kind == 0 && y.kind == 0) {
				layer = LayerMulti
			}
		case lx == LayerMulti:
			layer = ly
		case ly == LayerMulti:
			layer = lx
		case lx == ly:
			layer = lx
		default:
			return false
		}
		if layer != LayerMulti && (!onLayer(x, layer) || !onLayer(y, layer)) {
			return false
		}
		if x.kind > y.kind {
			x, y = y, x
		}
		switch {
		case x.kind == 0 && y.kind == 0:
			return x.pad.dist(y.pad.C) <= touchEps || y.pad.dist(x.pad.C) <= touchEps
		case x.kind == 0 && y.kind == 1:
			return x.pad.segDist(y.track.A, y.track.B) <= y.track.W/2+touchEps
		case x.kind == 0 && y.kind == 2:
			return x.pad.dist(y.via.C) <= y.via.Dia/2+touchEps
		case x.kind == 0 && y.kind == 3:
			for _, q := range ring(x.pad.C, math.Min(x.pad.W, x.pad.H)/2*0.9) {
				if inArea(y.area, q) {
					return true
				}
			}
		case x.kind == 1 && y.kind == 1:
			return segSegDist(x.track.A, x.track.B, y.track.A, y.track.B) <= (x.track.W+y.track.W)/2+touchEps
		case x.kind == 1 && y.kind == 2:
			return segDist(y.via.C, x.track.A, x.track.B) <= x.track.W/2+y.via.Dia/2+touchEps
		case x.kind == 1 && y.kind == 3:
			return inArea(y.area, x.track.A) || inArea(y.area, x.track.B)
		case x.kind == 2 && y.kind == 2:
			return dist(x.via.C, y.via.C) <= (x.via.Dia+y.via.Dia)/2+touchEps
		case x.kind == 2 && y.kind == 3:
			for _, q := range ring(x.via.C, x.via.Dia/2*0.9) {
				if inArea(y.area, q) {
					return true
				}
			}
		case x.kind == 3 && y.kind == 3:
			for _, c := range x.area.Contours {
				for _, q := range c {
					if inArea(y.area, q) {
						return true
					}
				}
			}
		}
		return false
	}
	nets := make([]string, 0, len(byNet))
	for n := range byNet {
		nets = append(nets, n)
	}
	sort.Strings(nets)
	var out []OpenNet
	for _, net := range nets {
		its := byNet[net]
		pads := 0
		for _, it := range its {
			if it.kind == 0 {
				pads++
			}
		}
		if pads < 2 {
			continue
		}
		uf := make(unionFind, 0, len(its))
		for range its {
			uf.add()
		}
		for i := range its {
			for j := i + 1; j < len(its); j++ {
				if uf.find(i) != uf.find(j) && touch(its[i], its[j]) {
					uf.union(i, j)
				}
			}
		}
		groups := map[int][]string{}
		for i, it := range its {
			if it.kind == 0 {
				r := uf.find(i)
				groups[r] = append(groups[r], it.name)
			}
		}
		if len(groups) < 2 {
			continue
		}
		on := OpenNet{Net: net}
		for _, g := range groups {
			sort.Strings(g)
			on.Islands = append(on.Islands, g)
		}
		sort.Slice(on.Islands, func(i, j int) bool {
			if len(on.Islands[i]) != len(on.Islands[j]) {
				return len(on.Islands[i]) > len(on.Islands[j])
			}
			return on.Islands[i][0] < on.Islands[j][0]
		})
		on.Unrouted = len(on.Islands) - 1
		out = append(out, on)
	}
	return out
}
