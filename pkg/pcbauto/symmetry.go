package pcbauto

// Symmetry detection for placement aesthetics P4 ([plan] §4 P4).
//
// An isomorphic sub-circuit is a set of parts whose part-type multiset AND
// pin-to-net connectivity pattern repeat elsewhere on the board: two LDO
// channels, a row of LED or button channels, the P and N legs of a
// differential chain, the decaps on either side of an IC. The pattern hash is
// a two-round Weisfeiler–Lehman relabelling of the part–net graph restricted
// to the instance: each part starts as kind|device|pads, each net as its role
// class (ground / power / local with the parts and pins it joins inside the
// instance, plus whether it leaves the instance). Net names never enter the
// hash, so VOUT1/VOUT2 or LED1_A/LED2_A still match.
//
// For every pair of instances the best-fit transform is chosen among a
// vertical mirror axis, a horizontal mirror axis, a 180° point reflection and
// a pure translation (channels repeated side by side are copies, not
// mirrors). Its parameters depend only on the instance centroids (the mean of
// the midpoints equals the midpoint of the means), so they are fitted without
// knowing the correspondence; parts are then matched within each label by
// nearest mapped position. The error is
//
//	err = mean‖p_b − T(p_a)‖ / module span + 0.2 × rotation-mismatch share
//
// where the module span is the larger instance's body-bbox diagonal (≥ 50
// mil) and a rotation matches when it equals the mirrored rotation (mod 180
// for symmetric passives and ≥3-pin packages that cannot be mirrored, mod
// 360 for polar two-pin parts: LEDs, diodes). Score = ramp(err, 0.05, 0.4)
// ([fable] P4); the 0.2 rotation weight is an initial value.

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

// SymmetryAxis is the best-fit transform between two instances.
type SymmetryAxis struct {
	Type   string  `json:"type"` // vertical | horizontal | point | translate
	Coord  float64 `json:"coord,omitempty"`
	Centre *Point  `json:"centre,omitempty"`
	Offset *Point  `json:"offset,omitempty"`
}

// SymmetryPair is one evaluated instance pair.
type SymmetryPair struct {
	A, B        int          `json:"-"`
	Axis        SymmetryAxis `json:"axis"`
	PosErrMil   float64      `json:"posErrMil"`
	SpanMil     float64      `json:"spanMil"`
	RotMismatch float64      `json:"rotMismatch"`
	Error       float64      `json:"error"`
	Match       [][2]string  `json:"match"`
}

// SymmetryGroup is a set of isomorphic sub-circuits.
type SymmetryGroup struct {
	Kind      string         `json:"kind"` // block | channel | diff-pair | decap-ring
	Signature string         `json:"signature"`
	Instances [][]string     `json:"instances"`
	Axis      SymmetryAxis   `json:"axis"` // of the first pair
	Error     float64        `json:"error"`
	Score     float64        `json:"score"`
	Pairs     []SymmetryPair `json:"pairs"`
	// labels are the instance parts' Weisfeiler–Lehman labels (ref →
	// label): the placer's symmetry seeding refits a pair with only the
	// transforms it can build (placer_aes.go).
	labels map[string]string
	// core is the IC of a decap ring (its centre is the ring's centre).
	core string
}

type symNode struct {
	ref   string
	label string
	pos   Point
	rot   float64
	mod   float64 // rotation compare modulus
	body  Rect
}

func h64(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%016x", h.Sum64())
}

// DetectSymmetry finds isomorphic sub-circuits and fits their mirror axes.
func DetectSymmetry(b *Board, an *Analysis, c *Circuit) []SymmetryGroup {
	if b.byRef == nil {
		_ = b.Index()
	}
	kind := func(p *Part) PartKind {
		if k := c.Kinds[p.Ref]; k != "" {
			return k
		}
		return ClassifyPart(p)
	}
	role := func(net string) string {
		switch an.Plan(net, b.Rules).Role {
		case RoleGround:
			return "G"
		case RolePower:
			return "P"
		}
		return "S"
	}
	padsOn := map[string][]*Pad{}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net != "" {
				padsOn[pd.Net] = append(padsOn[pd.Net], pd)
			}
		}
	}
	base := func(p *Part) string {
		dev := strings.TrimSpace(p.Device)
		if dev == "" {
			bd := p.Body()
			dev = fmt.Sprintf("~%.0f", math.Round(bd.W()*bd.H()/100))
		}
		return fmt.Sprintf("%s|%s|%d", kind(p), dev, len(p.Pads))
	}
	// wl labels the parts of one instance.
	wl := func(refs []string) map[string]string {
		in := map[string]bool{}
		for _, r := range refs {
			in[r] = true
		}
		lab := map[string]string{}
		for _, r := range refs {
			lab[r] = base(b.Part(r))
		}
		for round := 0; round < 2; round++ {
			next := map[string]string{}
			for _, r := range refs {
				p := b.Part(r)
				var desc []string
				for _, pd := range p.Pads {
					nd := "-"
					if pd.Net != "" {
						rl := role(pd.Net)
						if rl == "S" {
							var inside []string
							ext := false
							for _, q := range padsOn[pd.Net] {
								if q == pd {
									continue
								}
								if in[q.Part] {
									inside = append(inside, lab[q.Part]+"."+q.Number)
								} else {
									ext = true
								}
							}
							sort.Strings(inside)
							nd = fmt.Sprintf("S[%s]x%v", strings.Join(inside, ","), ext)
						} else {
							nd = rl
						}
					}
					desc = append(desc, pd.Number+"="+nd)
				}
				sort.Strings(desc)
				next[r] = h64(lab[r] + "|" + strings.Join(desc, ";"))
			}
			lab = next
		}
		return lab
	}
	sig := func(lab map[string]string) string {
		var v []string
		for _, l := range lab {
			v = append(v, l)
		}
		sort.Strings(v)
		return h64(strings.Join(v, ","))
	}
	node := func(ref, label string) symNode { return symNodeOf(b.Part(ref), label) }

	type inst struct {
		refs []string
		lab  map[string]string
	}
	var groups []SymmetryGroup
	seen := map[string]bool{}
	key := func(is []inst) string {
		var v []string
		for _, x := range is {
			r := append([]string(nil), x.refs...)
			sort.Strings(r)
			v = append(v, strings.Join(r, "+"))
		}
		sort.Strings(v)
		return strings.Join(v, "|")
	}
	// blockInst: ref → (block group, instance) of an emitted block group; a
	// channel group living entirely inside such instances repeats it.
	blockInst := map[string][2]int{}
	emit := func(kind, signature string, is []inst) {
		if len(is) < 2 {
			return
		}
		k := key(is)
		if seen[k] {
			return
		}
		seen[k] = true
		if kind == "channel" {
			used := map[[2]int]bool{}
			inside := true
			for _, x := range is {
				bi, ok := blockInst[x.refs[0]]
				for _, r := range x.refs {
					if b2, ok2 := blockInst[r]; !ok2 || b2 != bi {
						ok = false
					}
				}
				if !ok || used[bi] {
					inside = false
					break
				}
				used[bi] = true
			}
			if inside {
				return
			}
		}
		// order instances by centroid (x, then y) and fit consecutive pairs
		cent := func(x inst) Point {
			var s Point
			for _, r := range x.refs {
				s = s.Add(b.Part(r).Body().Center())
			}
			return s.Scale(1 / float64(len(x.refs)))
		}
		sort.SliceStable(is, func(i, j int) bool {
			ci, cj := cent(is[i]), cent(is[j])
			if math.Abs(ci.X-cj.X) > 1 {
				return ci.X < cj.X
			}
			return ci.Y < cj.Y
		})
		g := SymmetryGroup{Kind: kind, Signature: signature, labels: map[string]string{}}
		for _, x := range is {
			for r, l := range x.lab {
				g.labels[r] = l
			}
			r := append([]string(nil), x.refs...)
			sort.Strings(r)
			g.Instances = append(g.Instances, r)
		}
		for i := 0; i+1 < len(is); i++ {
			var A, B []symNode
			for _, r := range is[i].refs {
				A = append(A, node(r, is[i].lab[r]))
			}
			for _, r := range is[i+1].refs {
				B = append(B, node(r, is[i+1].lab[r]))
			}
			sp := fitSymmetry(A, B, nil)
			sp.A, sp.B = i, i+1
			g.Pairs = append(g.Pairs, sp)
		}
		es := 0.0
		for _, p := range g.Pairs {
			es += p.Error
		}
		g.Error = round3(es / float64(len(g.Pairs)))
		g.Axis = g.Pairs[0].Axis
		g.Score = aesRound(aesRamp(g.Error, 0.05, 0.4))
		if kind == "block" {
			for ii, x := range is {
				for _, r := range x.refs {
					blockInst[r] = [2]int{len(groups), ii}
				}
			}
		}
		groups = append(groups, g)
	}

	// (a) functional blocks with the same core device and pattern.
	{
		bySig := map[string][]inst{}
		var order []string
		for _, bl := range c.Blocks {
			if bl.Kind == "misc" || len(bl.Parts) < 2 {
				continue
			}
			lab := wl(bl.Parts)
			s := base(b.Part(bl.Core)) + "#" + sig(lab)
			if _, ok := bySig[s]; !ok {
				order = append(order, s)
			}
			bySig[s] = append(bySig[s], inst{bl.Parts, lab})
		}
		for _, s := range order {
			emit("block", strings.SplitN(s, "#", 2)[0], bySig[s])
		}
	}

	// (b) channels: connected components of small parts joined by local
	// signal nets (≤ 6 pads, not ground/power); ICs, modules and connectors
	// are the boundary, not members.
	{
		member := func(p *Part) bool {
			switch kind(p) {
			case KindIC, KindModule, KindConnector, KindMechanical, KindTestPoint:
				return false
			}
			return len(p.Pads) <= 4
		}
		par := map[string]string{}
		var find func(string) string
		find = func(a string) string {
			if _, ok := par[a]; !ok {
				par[a] = a
			}
			for par[a] != a {
				par[a] = par[par[a]]
				a = par[a]
			}
			return a
		}
		nets := make([]string, 0, len(padsOn))
		for n := range padsOn {
			nets = append(nets, n)
		}
		sort.Strings(nets)
		for _, net := range nets {
			if role(net) != "S" || len(padsOn[net]) > 6 {
				continue
			}
			var ms []string
			for _, pd := range padsOn[net] {
				if p := b.Part(pd.Part); p != nil && member(p) {
					ms = append(ms, p.Ref)
				}
			}
			for i := 1; i < len(ms); i++ {
				par[find(ms[i])] = find(ms[0])
			}
		}
		comp := map[string][]string{}
		for _, p := range b.Parts {
			if member(p) {
				comp[find(p.Ref)] = append(comp[find(p.Ref)], p.Ref)
			}
		}
		bySig := map[string][]inst{}
		var order []string
		roots := make([]string, 0, len(comp))
		for r := range comp {
			roots = append(roots, r)
		}
		sort.Strings(roots)
		for _, r := range roots {
			refs := comp[r]
			if len(refs) < 2 || len(refs) > 8 {
				continue
			}
			sort.Strings(refs)
			lab := wl(refs)
			s := sig(lab)
			if _, ok := bySig[s]; !ok {
				order = append(order, s)
			}
			bySig[s] = append(bySig[s], inst{refs, lab})
		}
		for _, s := range order {
			is := bySig[s]
			var kinds []string
			for _, r := range is[0].refs {
				kinds = append(kinds, string(kind(b.Part(r))))
			}
			sort.Strings(kinds)
			emit("channel", strings.Join(kinds, "+"), is)
		}
	}

	// (c) differential P/N legs: the small parts touching each member of a
	// pair (not both), with equal part-type multisets.
	{
		done := map[string]bool{}
		for _, np := range an.Nets {
			if np.PairWith == "" || done[np.Net] {
				continue
			}
			pn, nn := np.Net, np.PairWith
			done[pn], done[nn] = true, true
			side := func(net, other string) []string {
				var out []string
				for _, pd := range padsOn[net] {
					p := b.Part(pd.Part)
					if p == nil || len(p.Pads) > 3 {
						continue
					}
					both := false
					for _, q := range p.Pads {
						if q.Net == other {
							both = true
						}
					}
					if !both && !containsStr(out, p.Ref) {
						out = append(out, p.Ref)
					}
				}
				sort.Strings(out)
				return out
			}
			sp, sn := side(pn, nn), side(nn, pn)
			if len(sp) == 0 || len(sp) != len(sn) {
				continue
			}
			ms := func(refs []string) (string, map[string]string) {
				lab := map[string]string{}
				var v []string
				for _, r := range refs {
					lab[r] = base(b.Part(r))
					v = append(v, lab[r])
				}
				sort.Strings(v)
				return strings.Join(v, ","), lab
			}
			a, la := ms(sp)
			bb, lb := ms(sn)
			if a != bb {
				continue
			}
			emit("diff-pair", pn+"/"+nn, []inst{{sp, la}, {sn, lb}})
		}
	}

	// (d) decaps of one IC on opposite sides: mirrored through the IC centre.
	{
		for _, bl := range c.Blocks {
			core := b.Part(bl.Core)
			if core == nil {
				continue
			}
			byKey := map[string][]string{}
			var keys []string
			for _, m := range bl.Members {
				if m.Role != "decap" {
					continue
				}
				p := b.Part(m.Ref)
				if p == nil {
					continue
				}
				rail := ""
				for _, pd := range p.Pads {
					if role(pd.Net) != "G" {
						rail = pd.Net
					}
				}
				k := base(p) + "@" + rail
				if _, ok := byKey[k]; !ok {
					keys = append(keys, k)
				}
				byKey[k] = append(byKey[k], m.Ref)
			}
			sort.Strings(keys)
			cc := core.Body().Center()
			for _, k := range keys {
				refs := byKey[k]
				if len(refs) < 2 {
					continue
				}
				// needs a real opposite pair about the core centre
				opp := false
				for i := range refs {
					for j := i + 1; j < len(refs); j++ {
						pi, pj := b.Part(refs[i]).Body().Center().Sub(cc), b.Part(refs[j]).Body().Center().Sub(cc)
						if pi.X*pj.X < 0 && math.Abs(pi.X) > math.Abs(pi.Y) && math.Abs(pj.X) > math.Abs(pj.Y) ||
							pi.Y*pj.Y < 0 && math.Abs(pi.Y) > math.Abs(pi.X) && math.Abs(pj.Y) > math.Abs(pj.X) {
							opp = true
						}
					}
				}
				if !opp {
					continue
				}
				sort.Strings(refs)
				gk := "decap:" + strings.Join(refs, "+")
				if seen[gk] {
					continue
				}
				seen[gk] = true
				var ns []symNode
				for _, r := range refs {
					ns = append(ns, node(r, "decap"))
				}
				sp := fitRing(ns, cc, core.Body())
				g := SymmetryGroup{Kind: "decap-ring", Signature: bl.Core + " " + strings.SplitN(k, "@", 2)[1], Axis: sp.Axis, Error: sp.Error,
					Score: aesRound(aesRamp(sp.Error, 0.05, 0.4)), Pairs: []SymmetryPair{sp}, core: bl.Core}
				for _, r := range refs {
					g.Instances = append(g.Instances, []string{r})
				}
				groups = append(groups, g)
			}
		}
	}
	return groups
}

// symNodeOf is a part as the symmetry fit sees it: body centre, rotation
// and the modulus its rotation is compared with (180 for symmetric passives
// and ≥3-pin packages that cannot be mirrored, 360 for polar two-pin parts).
func symNodeOf(p *Part, label string) symNode {
	bd := p.Body()
	n := symNode{ref: p.Ref, label: label, pos: bd.Center(), rot: normDeg(p.Rotation), mod: 360, body: bd}
	if symmetricPassive(p) || len(p.Pads) >= 3 {
		n.mod = 180
	}
	return n
}

// groupError re-measures a detected group's mean error on the current
// placement (the same fits DetectSymmetry made).
func (g *SymmetryGroup) groupError(b *Board) float64 {
	nodes := func(refs []string, label func(string) string) []symNode {
		var out []symNode
		for _, r := range refs {
			if p := b.Part(r); p != nil {
				out = append(out, symNodeOf(p, label(r)))
			}
		}
		return out
	}
	if g.Kind == "decap-ring" {
		core := b.Part(g.core)
		if core == nil {
			return g.Error
		}
		var refs []string
		for _, in := range g.Instances {
			refs = append(refs, in...)
		}
		return fitRing(nodes(refs, func(string) string { return "decap" }), core.Body().Center(), core.Body()).Error
	}
	lab := func(r string) string { return g.labels[r] }
	sum, n := 0.0, 0
	for i := 0; i+1 < len(g.Instances); i++ {
		sum += fitSymmetry(nodes(g.Instances[i], lab), nodes(g.Instances[i+1], lab), nil).Error
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func symMirror(t string, p Point, prm Point) Point {
	switch t {
	case "vertical":
		return Point{2*prm.X - p.X, p.Y}
	case "horizontal":
		return Point{p.X, 2*prm.Y - p.Y}
	case "point":
		return Point{2*prm.X - p.X, 2*prm.Y - p.Y}
	}
	return p.Add(prm) // translate
}

func symRot(t string, r float64) float64 {
	switch t {
	case "vertical":
		return normDeg(180 - r)
	case "horizontal":
		return normDeg(-r)
	case "point":
		return normDeg(r + 180)
	}
	return r
}

func rotMatch(a, want, mod float64) bool {
	d := math.Mod(math.Abs(normDeg(a)-normDeg(want)), mod)
	return math.Min(d, mod-d) < 1
}

// fitSymmetry picks the best transform mapping instance A onto B.
func fitSymmetry(A, B []symNode, only []string) SymmetryPair {
	ca, cb := Point{}, Point{}
	ra, rb := EmptyRect(), EmptyRect()
	for _, n := range A {
		ca = ca.Add(n.pos)
		ra = ra.Union(n.body)
	}
	for _, n := range B {
		cb = cb.Add(n.pos)
		rb = rb.Union(n.body)
	}
	ca, cb = ca.Scale(1/float64(len(A))), cb.Scale(1/float64(len(B)))
	span := math.Max(50, math.Max(math.Hypot(ra.W(), ra.H()), math.Hypot(rb.W(), rb.H())))
	types := []string{"vertical", "horizontal", "point", "translate"}
	if only != nil {
		types = only
	}
	best := SymmetryPair{Error: math.Inf(1)}
	for _, t := range types {
		mid := ca.Add(cb).Scale(0.5)
		prm := mid
		if t == "translate" {
			prm = cb.Sub(ca)
		}
		// greedy match within labels by mapped distance
		type cand struct {
			i, j int
			d    float64
		}
		var cs []cand
		for i, a := range A {
			ma := symMirror(t, a.pos, prm)
			for j, bn := range B {
				if a.label == bn.label {
					cs = append(cs, cand{i, j, ma.Dist(bn.pos)})
				}
			}
		}
		sort.Slice(cs, func(x, y int) bool {
			if cs[x].d != cs[y].d {
				return cs[x].d < cs[y].d
			}
			if cs[x].i != cs[y].i {
				return cs[x].i < cs[y].i
			}
			return cs[x].j < cs[y].j
		})
		ua, ub := map[int]bool{}, map[int]bool{}
		var match [][2]string
		sum, mis, n := 0.0, 0, 0
		for _, c := range cs {
			if ua[c.i] || ub[c.j] {
				continue
			}
			ua[c.i], ub[c.j] = true, true
			sum += c.d
			n++
			if !rotMatch(B[c.j].rot, symRot(t, A[c.i].rot), A[c.i].mod) {
				mis++
			}
			match = append(match, [2]string{A[c.i].ref, B[c.j].ref})
		}
		if n == 0 {
			continue
		}
		pe := sum / float64(n)
		rm := float64(mis) / float64(n)
		err := pe/span + 0.2*rm
		if err < best.Error-1e-12 {
			ax := SymmetryAxis{Type: t}
			switch t {
			case "vertical":
				ax.Coord = round2(mid.X)
			case "horizontal":
				ax.Coord = round2(mid.Y)
			case "point":
				ax.Centre = ptr(mid)
			default:
				ax.Offset = ptr(prm)
			}
			best = SymmetryPair{Axis: ax, PosErrMil: round2(pe), SpanMil: round2(span), RotMismatch: round3(rm), Error: round3(err), Match: match}
		}
	}
	return best
}

// fitRing mirrors a set of decaps through the IC centre lines / centre.
func fitRing(ns []symNode, cc Point, core Rect) SymmetryPair {
	span := math.Max(50, math.Hypot(core.W(), core.H()))
	best := SymmetryPair{Error: math.Inf(1)}
	for _, t := range []string{"vertical", "horizontal", "point"} {
		sum, mis := 0.0, 0
		var match [][2]string
		for _, a := range ns {
			ma := symMirror(t, a.pos, cc)
			bj, bd := 0, math.Inf(1)
			for j, bn := range ns {
				if d := ma.Dist(bn.pos); d < bd {
					bj, bd = j, d
				}
			}
			sum += bd
			if !rotMatch(ns[bj].rot, symRot(t, a.rot), a.mod) {
				mis++
			}
			match = append(match, [2]string{a.ref, ns[bj].ref})
		}
		pe := sum / float64(len(ns))
		rm := float64(mis) / float64(len(ns))
		err := pe/span + 0.2*rm
		if err < best.Error-1e-12 {
			ax := SymmetryAxis{Type: t}
			switch t {
			case "vertical":
				ax.Coord = round2(cc.X)
			case "horizontal":
				ax.Coord = round2(cc.Y)
			default:
				ax.Centre = ptr(cc)
			}
			best = SymmetryPair{Axis: ax, PosErrMil: round2(pe), SpanMil: round2(span), RotMismatch: round3(rm), Error: round3(err), Match: match}
		}
	}
	return best
}
