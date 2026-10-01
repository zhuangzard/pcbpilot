package schaes

import (
	"math"
	"sort"
)

// Wiring topology with EasyEDA Pro contact semantics (schematic-data.md
// 数据驱动架构基准, measured on 3.2.186): wire endpoints that coincide connect;
// an endpoint landing on another segment's interior is a T contact (the host
// splits the segment); a strict interior X crossing does NOT connect;
// collinear overlap is treated as contact. Pins attach where a wire endpoint
// or segment touches the pin point; markers attach at their anchor.

const eps = 0.01

type seg struct {
	i      int // index in topo.segs
	wire   int
	net    string
	a, b   Pt
	horiz  bool
	vert   bool
	length float64
}

func (s *seg) has(p Pt) bool { return near(s.a, p) || near(s.b, p) }

// interior reports whether p lies strictly inside s (not at an endpoint).
func (s *seg) interior(p Pt) bool {
	if s.has(p) {
		return false
	}
	return onSegment(s.a, s.b, p)
}

func near(a, b Pt) bool { return math.Abs(a.X-b.X) < eps && math.Abs(a.Y-b.Y) < eps }

func onSegment(a, b, p Pt) bool {
	cross := (b.X-a.X)*(p.Y-a.Y) - (b.Y-a.Y)*(p.X-a.X)
	l := math.Hypot(b.X-a.X, b.Y-a.Y)
	if l == 0 || math.Abs(cross)/l > eps {
		return false
	}
	return p.X >= math.Min(a.X, b.X)-eps && p.X <= math.Max(a.X, b.X)+eps &&
		p.Y >= math.Min(a.Y, b.Y)-eps && p.Y <= math.Max(a.Y, b.Y)+eps
}

// crossPoint returns the strict interior intersection of two non-parallel
// segments (both interiors), ok=false otherwise.
func crossPoint(s, t *seg) (Pt, bool) {
	d := (s.b.X-s.a.X)*(t.b.Y-t.a.Y) - (s.b.Y-s.a.Y)*(t.b.X-t.a.X)
	if math.Abs(d) < 1e-9 {
		return Pt{}, false
	}
	u := ((t.a.X-s.a.X)*(t.b.Y-t.a.Y) - (t.a.Y-s.a.Y)*(t.b.X-t.a.X)) / d
	v := ((t.a.X-s.a.X)*(s.b.Y-s.a.Y) - (t.a.Y-s.a.Y)*(s.b.X-s.a.X)) / d
	if u <= 1e-6 || u >= 1-1e-6 || v <= 1e-6 || v >= 1-1e-6 {
		return Pt{}, false
	}
	p := Pt{s.a.X + u*(s.b.X-s.a.X), s.a.Y + u*(s.b.Y-s.a.Y)}
	return p, true
}

// collinearOverlap returns the overlap length of two collinear axis segments.
func collinearOverlap(s, t *seg) float64 {
	switch {
	case s.horiz && t.horiz && math.Abs(s.a.Y-t.a.Y) < eps:
		lo := math.Max(math.Min(s.a.X, s.b.X), math.Min(t.a.X, t.b.X))
		hi := math.Min(math.Max(s.a.X, s.b.X), math.Max(t.a.X, t.b.X))
		return hi - lo
	case s.vert && t.vert && math.Abs(s.a.X-t.a.X) < eps:
		lo := math.Max(math.Min(s.a.Y, s.b.Y), math.Min(t.a.Y, t.b.Y))
		hi := math.Min(math.Max(s.a.Y, s.b.Y), math.Max(t.a.Y, t.b.Y))
		return hi - lo
	}
	return 0
}

type terminal struct {
	kind string // pin | marker
	ref  string // part ref or marker net
	pin  string
	at   Pt
	mk   int // marker index (kind marker)
	part int // part index (kind pin)
	pi   int // pin index (kind pin)
}

// island is one physical wire tree.
type island struct {
	id        int
	segs      []int
	nets      map[string]bool
	terms     []terminal
	length    float64
	crossings int // strict X crossings with other islands (any net)
}

func (is *island) net() string {
	for _, n := range sortedKeys(is.nets) {
		if n != "" {
			return n
		}
	}
	return ""
}

type node struct {
	at   Pt
	arms []Pt // unit-ish direction vectors leaving the node
	segs []int
	pin  bool
	mark bool
}

type topo struct {
	snap    *Snapshot
	segs    []*seg
	islands []*island
	segIsl  []int
	nodes   map[[2]int64]*node // keyed by quantized point, per island via nodeIsl
	nodeIsl map[[2]int64]int
	// marker index → island (−1 none); pin (part,pin) → island
	markerIsl []int
	pinIsl    map[[2]int]int
}

func qkey(p Pt) [2]int64 { return [2]int64{int64(math.Round(p.X * 100)), int64(math.Round(p.Y * 100))} }

func buildTopo(s *Snapshot) *topo {
	t := &topo{snap: s, nodes: map[[2]int64]*node{}, nodeIsl: map[[2]int64]int{}, pinIsl: map[[2]int]int{}}
	for wi, w := range s.Wires {
		for k := 0; k+1 < len(w.Pts); k++ {
			a, b := w.Pts[k], w.Pts[k+1]
			l := math.Hypot(b.X-a.X, b.Y-a.Y)
			if l < eps {
				continue
			}
			t.segs = append(t.segs, &seg{i: len(t.segs), wire: wi, net: w.Net, a: a, b: b, length: l,
				horiz: math.Abs(a.Y-b.Y) < eps, vert: math.Abs(a.X-b.X) < eps})
		}
	}
	n := len(t.segs)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) { parent[find(a)] = find(b) }
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			s1, s2 := t.segs[i], t.segs[j]
			if s1.has(s2.a) || s1.has(s2.b) || onSegment(s1.a, s1.b, s2.a) || onSegment(s1.a, s1.b, s2.b) ||
				onSegment(s2.a, s2.b, s1.a) || onSegment(s2.a, s2.b, s1.b) || collinearOverlap(s1, s2) > eps {
				union(i, j)
			}
		}
	}
	root := map[int]int{}
	t.segIsl = make([]int, n)
	for i := 0; i < n; i++ {
		r := find(i)
		id, ok := root[r]
		if !ok {
			id = len(t.islands)
			root[r] = id
			t.islands = append(t.islands, &island{id: id, nets: map[string]bool{}})
		}
		is := t.islands[id]
		is.segs = append(is.segs, i)
		is.nets[t.segs[i].net] = true
		is.length += t.segs[i].length
		t.segIsl[i] = id
	}
	// terminals
	touch := func(p Pt) int {
		for _, sg := range t.segs {
			if sg.has(p) || onSegment(sg.a, sg.b, p) {
				return t.segIsl[sg.i]
			}
		}
		return -1
	}
	for pi, p := range s.Parts {
		for qi, q := range p.Pins {
			if is := touch(Pt{q.X, q.Y}); is >= 0 {
				t.pinIsl[[2]int{pi, qi}] = is
				t.islands[is].terms = append(t.islands[is].terms, terminal{kind: "pin", ref: p.Ref, pin: q.Number, at: Pt{q.X, q.Y}, part: pi, pi: qi})
				if q.Net != "" {
					t.islands[is].nets[q.Net] = true
				}
			}
		}
	}
	t.markerIsl = make([]int, len(s.Markers))
	for mi, m := range s.Markers {
		t.markerIsl[mi] = touch(m.Anchor)
		if is := t.markerIsl[mi]; is >= 0 {
			t.islands[is].terms = append(t.islands[is].terms, terminal{kind: "marker", ref: m.Net, at: m.Anchor, mk: mi})
			t.islands[is].nets[m.Net] = true
		}
	}
	// nodes: every segment endpoint plus interior T points
	addArm := func(p Pt, dir Pt, si int) {
		k := qkey(p)
		nd := t.nodes[k]
		if nd == nil {
			nd = &node{at: p}
			t.nodes[k] = nd
			t.nodeIsl[k] = t.segIsl[si]
		}
		nd.arms = append(nd.arms, dir)
		nd.segs = append(nd.segs, si)
	}
	unit := func(a, b Pt) Pt {
		l := math.Hypot(b.X-a.X, b.Y-a.Y)
		return Pt{(b.X - a.X) / l, (b.Y - a.Y) / l}
	}
	for _, sg := range t.segs {
		addArm(sg.a, unit(sg.a, sg.b), sg.i)
		addArm(sg.b, unit(sg.b, sg.a), sg.i)
	}
	// T points: an endpoint on another segment's interior adds two arms
	keys := make([][2]int64, 0, len(t.nodes))
	for k := range t.nodes {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		nd := t.nodes[k]
		for _, sg := range t.segs {
			if sg.interior(nd.at) && t.segIsl[sg.i] == t.nodeIsl[k] {
				nd.arms = append(nd.arms, unit(nd.at, sg.a), unit(nd.at, sg.b))
				nd.segs = append(nd.segs, sg.i, sg.i)
			}
		}
	}
	for _, is := range t.islands {
		for _, tm := range is.terms {
			if nd := t.nodes[qkey(tm.at)]; nd != nil {
				if tm.kind == "pin" {
					nd.pin = true
				} else {
					nd.mark = true
				}
			}
		}
	}
	return t
}

func (t *topo) sortedNodes() []*node {
	out := make([]*node, 0, len(t.nodes))
	for _, n := range t.nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].at.X != out[j].at.X {
			return out[i].at.X < out[j].at.X
		}
		return out[i].at.Y < out[j].at.Y
	})
	return out
}

// bendAt reports whether a two-arm node changes direction.
func bendAt(n *node) bool {
	if len(n.arms) != 2 {
		return false
	}
	a, b := n.arms[0], n.arms[1]
	return a.X*b.X+a.Y*b.Y > -0.999
}

// connections is Σ max(terminals−1, 0) over islands.
func (t *topo) connections() int {
	c := 0
	for _, is := range t.islands {
		if len(is.terms) > 1 {
			c += len(is.terms) - 1
		}
	}
	return c
}

// crossing is a strict interior X between two islands.
type crossing struct {
	at         Pt
	a, b       int // island ids
	netA, netB string
}

func (t *topo) crossings() []crossing {
	var out []crossing
	for i := 0; i < len(t.segs); i++ {
		for j := i + 1; j < len(t.segs); j++ {
			s1, s2 := t.segs[i], t.segs[j]
			if t.segIsl[i] == t.segIsl[j] {
				continue
			}
			if p, ok := crossPoint(s1, s2); ok {
				out = append(out, crossing{at: p, a: t.segIsl[i], b: t.segIsl[j],
					netA: t.islands[t.segIsl[i]].net(), netB: t.islands[t.segIsl[j]].net()})
			}
		}
	}
	return out
}

// segClipLen is the length of s strictly inside box b.
func segClipLen(s *seg, b Box) float64 {
	// Liang–Barsky
	x0, y0, dx, dy := s.a.X, s.a.Y, s.b.X-s.a.X, s.b.Y-s.a.Y
	u0, u1 := 0.0, 1.0
	for _, c := range [][2]float64{{-dx, x0 - b.MinX}, {dx, b.MaxX - x0}, {-dy, y0 - b.MinY}, {dy, b.MaxY - y0}} {
		p, q := c[0], c[1]
		if math.Abs(p) < 1e-12 {
			if q < 0 {
				return 0
			}
			continue
		}
		r := q / p
		if p < 0 {
			if r > u1 {
				return 0
			}
			if r > u0 {
				u0 = r
			}
		} else {
			if r < u0 {
				return 0
			}
			if r < u1 {
				u1 = r
			}
		}
	}
	if u1 <= u0 {
		return 0
	}
	return (u1 - u0) * s.length
}
