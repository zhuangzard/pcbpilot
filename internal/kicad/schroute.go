package kicad

// schroute.go — moving symbols on a .kicad_sch without leaving diagonal
// wires (DragSymbols). Connectivity is reasoned about per wire island (the
// wires joined end to end, or end to middle at a junction — KiCad 10 joins
// nothing else: a pin or wire end on a wire's middle without a junction
// stays unconnected, checked with kicad-cli):
//
//   - an island whose pins all belong to symbols that move by one rigid
//     transform (a group move, or a symbol's own stubs) moves with them —
//     wires, labels, power symbols, junctions and no-connects keep their
//     shape relative to the symbols;
//   - any other island touching a moved pin is re-routed: its wires and
//     junctions are dropped and the terminals (pins, labels, power symbols,
//     sheet pins) are joined again by a Manhattan tree on the 1.27 mm grid
//     that avoids symbol bodies, fields, labels and texts, never runs along
//     or ends on a foreign wire, never touches a foreign pin or wire end,
//     prefers no crossings and few bends, leaves every pin along its own
//     outward axis and puts a junction on each T;
//   - when no clean path exists the island falls back to net labels on its
//     pins (power symbols when the net had one).
//
// Callers verify the result with kicad-cli's netlist before writing.

import (
	"container/heap"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// SchGrid is KiCad's default schematic connection grid (50 mil).
const SchGrid = 1.27

type gp struct{ X, Y int }

func toGP(p Pt) (gp, bool) {
	x, y := math.Round(p.X/SchGrid), math.Round(p.Y/SchGrid)
	ok := math.Abs(x*SchGrid-p.X) < 2e-3 && math.Abs(y*SchGrid-p.Y) < 2e-3
	return gp{int(x), int(y)}, ok
}

func (g gp) pt() Pt { return Pt{round4mm(float64(g.X) * SchGrid), round4mm(float64(g.Y) * SchGrid)} }

// OnGrid reports whether p lies on the 1.27 mm grid.
func OnGrid(p Pt) bool { _, ok := toGP(p); return ok }

// ---- rigid transforms ---------------------------------------------------------

// xform is an affine sheet map p' = (A x + B y + TX, C x + D y + TY).
type xform struct{ A, B, C, D, TX, TY float64 }

func (t xform) apply(p Pt) Pt {
	return Pt{round4mm(t.A*p.X + t.B*p.Y + t.TX), round4mm(t.C*p.X + t.D*p.Y + t.TY)}
}

func (t xform) equal(u xform) bool {
	d := func(a, b float64) bool { return math.Abs(a-b) < 1e-6 }
	return d(t.A, u.A) && d(t.B, u.B) && d(t.C, u.C) && d(t.D, u.D) && math.Abs(t.TX-u.TX) < 1e-3 && math.Abs(t.TY-u.TY) < 1e-3
}

// angle is the counter-clockwise screen rotation of the linear part (the
// part is always a rotation: the mirror of a moved symbol is kept).
func (t xform) angle() float64 {
	x, y := t.A, t.C // image of (1, 0)
	return normAngle(math.Round(math.Atan2(-y, x)*180/math.Pi/90) * 90)
}

func normAngle(a float64) float64 { return math.Mod(math.Mod(a, 360)+360, 360) }

// symbolInverse maps a sheet point back to library coordinates (y up).
func symbolInverse(p, at Pt, rot float64, mirror string) Pt {
	x, y := p.X-at.X, p.Y-at.Y
	switch mirror {
	case "x":
		y = -y
	case "y":
		x = -x
	}
	a := rot * math.Pi / 180
	c, s := math.Round(math.Cos(a)), math.Round(math.Sin(a))
	x, y = x*c-y*s, x*s+y*c
	return Pt{x, -y}
}

func symbolXformRaw(p, at Pt, rot float64, mirror string) Pt {
	x, y := p.X, -p.Y
	a := rot * math.Pi / 180
	c, s := math.Round(math.Cos(a)), math.Round(math.Sin(a))
	x, y = x*c+y*s, -x*s+y*c
	switch mirror {
	case "x":
		y = -y
	case "y":
		x = -x
	}
	return Pt{at.X + x, at.Y + y}
}

// moveXform is the sheet map that carries a symbol (mirror kept) from
// (at, rot) to (to, toRot).
func moveXform(at Pt, rot float64, mirror string, to Pt, toRot float64) xform {
	f := func(p Pt) Pt { return symbolXformRaw(symbolInverse(p, at, rot, mirror), to, toRot, mirror) }
	o, ex, ey := f(Pt{0, 0}), f(Pt{1, 0}), f(Pt{0, 1})
	r := func(v float64) float64 { return math.Round(v*1e6) / 1e6 }
	return xform{A: r(ex.X - o.X), C: r(ex.Y - o.Y), B: r(ey.X - o.X), D: r(ey.Y - o.Y), TX: o.X, TY: o.Y}
}

// ---- sheet connection model ---------------------------------------------------

// connItem is something that connects at a point: a pin (of a part or a
// power symbol), a label, a junction, a no-connect, a sheet pin or a bus
// entry end.
type connItem struct {
	kind    string // pin, power, label, junction, nc, sheetpin, busentry
	node    *sexp
	ref     string // pins: "REF.PIN"
	at      Pt
	outward float64 // degrees; -1 unknown
}

type wireNode struct {
	node *sexp
	pts  []Pt
}

func onSegInterior(p, a, b Pt) bool {
	if samePt(p, a) || samePt(p, b) {
		return false
	}
	cross := (b.X-a.X)*(p.Y-a.Y) - (b.Y-a.Y)*(p.X-a.X)
	if math.Abs(cross) > 1e-3*math.Max(1, math.Hypot(b.X-a.X, b.Y-a.Y)) {
		return false
	}
	return p.X >= math.Min(a.X, b.X)-dragEps && p.X <= math.Max(a.X, b.X)+dragEps &&
		p.Y >= math.Min(a.Y, b.Y)-dragEps && p.Y <= math.Max(a.Y, b.Y)+dragEps
}

func onWire(p Pt, w wireNode) (end, interior bool) {
	for i, q := range w.pts {
		if samePt(p, q) && (i == 0 || i == len(w.pts)-1) {
			return true, false
		}
	}
	for i := 0; i+1 < len(w.pts); i++ {
		if samePt(p, w.pts[i]) || samePt(p, w.pts[i+1]) || onSegInterior(p, w.pts[i], w.pts[i+1]) {
			return false, true
		}
	}
	return false, false
}

// instKey names a symbol instance: "REF", or "REF:UNIT" when the reference
// has several units on the sheet.
func instKeys(root *sexp) map[*sexp]string {
	count := map[string]int{}
	for _, n := range root.list {
		if n.head() == "symbol" {
			count[symRef(n)]++
		}
	}
	out := map[*sexp]string{}
	for _, n := range root.list {
		if n.head() != "symbol" {
			continue
		}
		r := symRef(n)
		if count[r] > 1 {
			u := 1
			if c := n.child("unit"); c != nil {
				u = int(c.num(1))
			}
			r = fmt.Sprintf("%s:%d", r, u)
		}
		out[n] = r
	}
	return out
}

func labelAngleOf(n *sexp) float64 {
	if at := n.child("at"); at != nil {
		return at.num(3)
	}
	return 0
}

func (e *SchEditor) connItems() []connItem {
	var out []connItem
	for _, n := range e.root.list {
		switch h := n.head(); h {
		case "symbol":
			s, err := e.sceneSymbol(n, nil)
			if err != nil {
				continue
			}
			kind := "pin"
			if s.Power {
				kind = "power"
			}
			for _, p := range s.Pins {
				out = append(out, connItem{kind: kind, node: n, ref: s.Ref + "." + p.Number, at: p.At, outward: p.Outward})
			}
		case LabelLocal, LabelGlobal, LabelHier:
			if at := n.child("at"); at != nil {
				out = append(out, connItem{kind: "label", node: n, at: Pt{at.num(1), at.num(2)}, outward: normAngle(at.num(3) + 180)})
			}
		case "junction", "no_connect":
			if at := n.child("at"); at != nil {
				k := "junction"
				if h == "no_connect" {
					k = "nc"
				}
				out = append(out, connItem{kind: k, node: n, at: Pt{at.num(1), at.num(2)}, outward: -1})
			}
		case "sheet":
			for _, c := range n.list {
				if c.head() == "pin" {
					if at := c.child("at"); at != nil {
						out = append(out, connItem{kind: "sheetpin", node: n, at: Pt{at.num(1), at.num(2)}, outward: normAngle(at.num(3))})
					}
				}
			}
		case "bus_entry":
			if at, sz := n.child("at"), n.child("size"); at != nil && sz != nil {
				a := Pt{at.num(1), at.num(2)}
				out = append(out, connItem{kind: "busentry", node: n, at: a, outward: -1},
					connItem{kind: "busentry", node: n, at: Pt{a.X + sz.num(1), a.Y + sz.num(2)}, outward: -1})
			}
		}
	}
	return out
}

func (e *SchEditor) wireNodes() []wireNode {
	var out []wireNode
	for _, n := range e.root.list {
		if n.head() == "wire" {
			out = append(out, wireNode{node: n, pts: wirePts(n)})
		}
	}
	return out
}

// island is a set of wires connected end to end (or at a junction) and the
// items connected to them.
type island struct {
	wires []int
	items []int
}

func buildIslands(wires []wireNode, items []connItem) (isl []island, itemIsland []int) {
	parent := make([]int, len(wires))
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
	var junctions []Pt
	for _, it := range items {
		if it.kind == "junction" {
			junctions = append(junctions, it.at)
		}
	}
	isJunction := func(p Pt) bool {
		for _, j := range junctions {
			if samePt(j, p) {
				return true
			}
		}
		return false
	}
	for i := range wires {
		for j := i + 1; j < len(wires); j++ {
			for _, end := range []Pt{wires[i].pts[0], wires[i].pts[len(wires[i].pts)-1]} {
				if e, in := onWire(end, wires[j]); e || (in && isJunction(end)) {
					union(i, j)
				}
			}
			for _, end := range []Pt{wires[j].pts[0], wires[j].pts[len(wires[j].pts)-1]} {
				if _, in := onWire(end, wires[i]); in && isJunction(end) {
					union(i, j)
				}
			}
		}
	}
	idx := map[int]int{}
	for i := range wires {
		r := find(i)
		k, ok := idx[r]
		if !ok {
			k = len(isl)
			idx[r] = k
			isl = append(isl, island{})
		}
		isl[k].wires = append(isl[k].wires, i)
	}
	itemIsland = make([]int, len(items))
	for i, it := range items {
		itemIsland[i] = -1
		for k := range isl {
			for _, w := range isl[k].wires {
				e, in := onWire(it.at, wires[w])
				if e || (in && (it.kind == "label" || it.kind == "junction")) {
					itemIsland[i] = k
					break
				}
			}
			if itemIsland[i] >= 0 {
				isl[k].items = append(isl[k].items, i)
				break
			}
		}
	}
	return isl, itemIsland
}

// ---- DragSymbols -------------------------------------------------------------

// SymPose is a symbol position and rotation (degrees, counter-clockwise).
type SymPose struct {
	At  Pt
	Rot float64
}

// DragOptions tunes DragSymbols.
type DragOptions struct {
	// PinNets maps "REF.PIN" to the KiCad net name before the edit; it names
	// the labels of an island that cannot be re-routed (nil: such an island
	// fails the drag).
	PinNets map[string]string
	// NoReroute keeps wire ends following the pins (wires may turn
	// diagonal) instead of moving or re-routing the islands.
	NoReroute bool
}

// DragResult counts what DragSymbols changed.
type DragResult struct {
	Symbols    int      `json:"symbols"`
	WirePoints int      `json:"wirePoints"`
	Labels     int      `json:"labels"`
	Powers     int      `json:"powerSymbols"`
	Markers    int      `json:"markers"`                 // no-connects and junctions
	RigidNets  int      `json:"rigidNets"`               // wire islands moved with their symbols
	Rerouted   int      `json:"reroutedNets"`            // islands re-routed orthogonally
	NewWires   int      `json:"newWires"`                // wire segments drawn by the router
	Fallback   []string `json:"labelFallback,omitempty"` // islands replaced by net labels on their pins
	Renamed    []string `json:"renamedNets,omitempty"`   // unnamed nets that got a label name
}

const dragEps = 1e-3

func samePt(a, b Pt) bool { return math.Abs(a.X-b.X) < dragEps && math.Abs(a.Y-b.Y) < dragEps }

// DragSymbols moves symbol instances (by key: "REF", or "REF:UNIT" when the
// reference has several units on the sheet; mirror kept) and keeps their
// connections drawn: see the file comment.
func (e *SchEditor) DragSymbols(poses map[string]SymPose) (DragResult, error) {
	return e.DragSymbolsOpt(poses, DragOptions{})
}

type movedInst struct {
	key string
	t   xform
	to  SymPose
	sym SceneSymbol
}

// DragSymbolsOpt is DragSymbols with options.
func (e *SchEditor) DragSymbolsOpt(poses map[string]SymPose, opt DragOptions) (DragResult, error) {
	var res DragResult
	keys := instKeys(e.root)
	byKey := map[string]*sexp{}
	units := map[string][]string{}
	for n, k := range keys {
		byKey[k] = n
		if r := symRef(n); r != k {
			units[r] = append(units[r], k)
		}
	}
	moved := map[*sexp]*movedInst{}
	for key, pose := range poses {
		n := byKey[key]
		if n == nil {
			if r, u, ok := strings.Cut(key, ":"); ok && len(units[r]) == 0 && u == "1" {
				n = byKey[r] // "REF:1" of a single-unit symbol
			}
		}
		if n == nil {
			if us := units[key]; len(us) > 0 {
				sort.Strings(us)
				return res, fmt.Errorf("%s has %d units on this sheet; name the unit (%s)", key, len(us), strings.Join(us, ", "))
			}
			return res, fmt.Errorf("no symbol %s on this sheet", key)
		}
		s, err := e.sceneSymbol(n, nil)
		if err != nil {
			return res, err
		}
		t := moveXform(s.At, s.Rot, s.Mirror, pose.At, pose.Rot)
		if t.equal(xform{A: 1, D: 1}) && normAngle(s.Rot) == normAngle(pose.Rot) {
			continue // already there
		}
		moved[n] = &movedInst{key: key, to: pose, sym: s, t: t}
		res.Symbols++
	}
	items := e.connItems()
	wires := e.wireNodes()
	isl, itemIsl := buildIslands(wires, items)
	edited := map[*sexp]bool{}
	for n, m := range moved {
		e.repl = append(e.repl, symbolMoveEdits(n, m.sym.At, m.sym.Rot, m.to.At, m.to.Rot, m.t)...)
		edited[n] = true
	}
	// where each moved pin goes
	pinT := map[int]xform{}
	for i, it := range items {
		if m := moved[it.node]; m != nil && it.kind == "pin" {
			pinT[i] = m.t
		}
	}
	movedAt := func(p Pt) (xform, bool) {
		for i, t := range pinT {
			if samePt(items[i].at, p) {
				return t, true
			}
		}
		return xform{}, false
	}
	// direct contacts (no wire): markers on a moved pin go with it; a pin
	// on a pin must move the same way.
	for i, it := range items {
		if itemIsl[i] >= 0 || moved[it.node] != nil {
			continue
		}
		t, ok := movedAt(it.at)
		if !ok {
			continue
		}
		if it.kind == "pin" || it.kind == "sheetpin" || it.kind == "busentry" {
			return res, fmt.Errorf("pins at (%s, %s) touch directly and would be torn apart; wire them first", F(it.at.X), F(it.at.Y))
		}
		if !edited[it.node] {
			e.moveMarker(it, t, &res)
			edited[it.node] = true
		}
	}
	for i := range pinT {
		for j := range pinT {
			if i < j && samePt(items[i].at, items[j].at) && !pinT[i].equal(pinT[j]) && itemIsl[i] < 0 {
				return res, fmt.Errorf("pins at (%s, %s) touch directly and would be torn apart; wire them first", F(items[i].at.X), F(items[i].at.Y))
			}
		}
	}
	var reroute []int
	for k, is := range isl {
		var ts []xform
		fixedPin, other := false, false
		for _, ii := range is.items {
			it := items[ii]
			switch it.kind {
			case "pin":
				if t, ok := pinT[ii]; ok {
					ts = append(ts, t)
				} else {
					fixedPin = true
				}
			case "sheetpin", "busentry":
				other = true
			}
		}
		if len(ts) == 0 {
			continue
		}
		rigid := !fixedPin && !other
		for _, t := range ts[1:] {
			rigid = rigid && t.equal(ts[0])
		}
		switch {
		case opt.NoReroute:
			e.followEnds(is, items, wires, pinT, edited, &res)
		case rigid:
			t := ts[0]
			for _, w := range is.wires {
				for _, p := range wires[w].node.child("pts").list {
					if p.head() == "xy" {
						q := t.apply(Pt{p.num(1), p.num(2)})
						e.repl = append(e.repl, textEdit{p.beg, p.end, fmt.Sprintf("(xy %s %s)", F(q.X), F(q.Y))})
						res.WirePoints++
					}
				}
			}
			for _, ii := range is.items {
				if it := items[ii]; it.kind != "pin" && !edited[it.node] {
					e.moveMarker(it, t, &res)
					edited[it.node] = true
				}
			}
			res.RigidNets++
		case other:
			// bus entries / sheet pins with moved pins: ends follow only
			e.followEnds(is, items, wires, pinT, edited, &res)
		default:
			reroute = append(reroute, k)
		}
	}
	if len(reroute) == 0 {
		return res, nil
	}
	return res, e.rerouteIslands(reroute, isl, items, wires, moved, pinT, edited, opt, &res)
}

// followEnds is the plain drag: wire ends and markers on moved pins follow.
func (e *SchEditor) followEnds(is island, items []connItem, wires []wireNode, pinT map[int]xform, edited map[*sexp]bool, res *DragResult) {
	at := func(p Pt) (Pt, bool) {
		for i, t := range pinT {
			if samePt(items[i].at, p) {
				return t.apply(p), true
			}
		}
		return Pt{}, false
	}
	for _, w := range is.wires {
		for _, p := range wires[w].node.child("pts").list {
			if p.head() != "xy" {
				continue
			}
			if q, ok := at(Pt{p.num(1), p.num(2)}); ok {
				e.repl = append(e.repl, textEdit{p.beg, p.end, fmt.Sprintf("(xy %s %s)", F(q.X), F(q.Y))})
				res.WirePoints++
			}
		}
	}
	for _, ii := range is.items {
		it := items[ii]
		if it.kind == "pin" || edited[it.node] {
			continue
		}
		for i, t := range pinT {
			if samePt(items[i].at, it.at) {
				e.moveMarker(it, t, res)
				edited[it.node] = true
				break
			}
		}
	}
}

var justifyRe = regexp.MustCompile(`\(justify ([a-z ]+)\)`)

// moveMarker applies t to a label, power symbol, junction or no-connect.
func (e *SchEditor) moveMarker(it connItem, t xform, res *DragResult) {
	n := it.node
	at := n.child("at")
	if at == nil {
		return
	}
	p := t.apply(Pt{at.num(1), at.num(2)})
	switch it.kind {
	case "power":
		s, err := e.sceneSymbol(n, nil)
		if err != nil {
			return
		}
		e.repl = append(e.repl, symbolMoveEdits(n, s.At, s.Rot, p, normAngle(s.Rot+t.angle()), t)...)
		res.Powers++
	case "label":
		ang := normAngle(at.num(3) + t.angle())
		e.repl = append(e.repl, textEdit{at.beg, at.end, fmt.Sprintf("(at %s %s %s)", F(p.X), F(p.Y), F(ang))})
		if j := findDeep(n, "justify"); j != nil {
			old := e.src[j.beg:j.end]
			side := "left"
			if ang == 180 || ang == 270 {
				side = "right"
			}
			nw := strings.NewReplacer("left", side, "right", side).Replace(old)
			if nw != old {
				e.repl = append(e.repl, textEdit{j.beg, j.end, nw})
			}
		}
		res.Labels++
	default:
		e.repl = append(e.repl, textEdit{at.beg, at.end, fmt.Sprintf("(at %s %s)", F(p.X), F(p.Y))})
		res.Markers++
	}
}

func findDeep(n *sexp, head string) *sexp {
	for _, c := range n.list {
		if c.head() == head {
			return c
		}
		if c.atom == "" {
			if f := findDeep(c, head); f != nil {
				return f
			}
		}
	}
	return nil
}

// symbolMoveEdits moves a symbol instance from (at, rot) to (to, toRot);
// its fields go with it (map t; their text angle is kept).
func symbolMoveEdits(n *sexp, at Pt, rot float64, to Pt, toRot float64, t xform) []textEdit {
	a := n.child("at")
	out := []textEdit{{a.beg, a.end, fmt.Sprintf("(at %s %s %s)", F(to.X), F(to.Y), F(normAngle(toRot)))}}
	for _, p := range n.list {
		if p.head() != "property" {
			continue
		}
		pa := p.child("at")
		if pa == nil {
			continue
		}
		q := t.apply(Pt{pa.num(1), pa.num(2)})
		out = append(out, textEdit{pa.beg, pa.end, fmt.Sprintf("(at %s %s %s)", F(q.X), F(q.Y), F(pa.num(3)))})
	}
	return out
}

// deleteNode removes a top-level item and the whitespace before it.
func (e *SchEditor) deleteNode(n *sexp) {
	beg := n.beg
	for beg > 0 && strings.ContainsRune(" \t\r\n", rune(e.src[beg-1])) {
		beg--
	}
	e.repl = append(e.repl, textEdit{beg, n.end, ""})
}

// ---- obstacle map + router ----------------------------------------------------

const (
	cBlocked   = 1 << iota // inside a body, label, field, text or the title block
	cForbidden             // a foreign connection point (pin, wire end, label anchor, junction…)
	cH                     // on a foreign horizontal wire
	cV                     // on a foreign vertical wire
	cNear                  // next to a blocked cell (small cost: keep air around bodies)
)

type routeGrid struct {
	x0, y0, w, h int
	f            []uint8
}

func newRouteGrid(b Box) *routeGrid {
	x0, y0 := int(math.Floor(b.MinX/SchGrid)), int(math.Floor(b.MinY/SchGrid))
	x1, y1 := int(math.Ceil(b.MaxX/SchGrid)), int(math.Ceil(b.MaxY/SchGrid))
	g := &routeGrid{x0: x0, y0: y0, w: x1 - x0 + 1, h: y1 - y0 + 1}
	g.f = make([]uint8, g.w*g.h)
	return g
}

func (g *routeGrid) in(p gp) bool {
	return p.X >= g.x0 && p.Y >= g.y0 && p.X < g.x0+g.w && p.Y < g.y0+g.h
}

func (g *routeGrid) get(p gp) uint8 {
	if !g.in(p) {
		return cBlocked
	}
	return g.f[(p.Y-g.y0)*g.w+p.X-g.x0]
}

func (g *routeGrid) set(p gp, f uint8) {
	if g.in(p) {
		g.f[(p.Y-g.y0)*g.w+p.X-g.x0] |= f
	}
}

// block marks the grid points strictly inside b.
func (g *routeGrid) block(b Box) {
	x0, x1 := int(math.Floor(b.MinX/SchGrid)), int(math.Ceil(b.MaxX/SchGrid))
	y0, y1 := int(math.Floor(b.MinY/SchGrid)), int(math.Ceil(b.MaxY/SchGrid))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			px, py := float64(x)*SchGrid, float64(y)*SchGrid
			if px > b.MinX+1e-6 && px < b.MaxX-1e-6 && py > b.MinY+1e-6 && py < b.MaxY-1e-6 {
				g.set(gp{x, y}, cBlocked)
			}
		}
	}
}

func (g *routeGrid) markNear() {
	var near []gp
	for y := g.y0; y < g.y0+g.h; y++ {
		for x := g.x0; x < g.x0+g.w; x++ {
			p := gp{x, y}
			if g.get(p)&cBlocked != 0 {
				continue
			}
			for _, d := range dirs {
				if g.in(gp{x + d.X, y + d.Y}) && g.get(gp{x + d.X, y + d.Y})&cBlocked != 0 {
					near = append(near, p)
					break
				}
			}
		}
	}
	for _, p := range near {
		g.set(p, cNear)
	}
}

// addWire marks a foreign wire: its ends are forbidden points, its run is
// occupied on its axis (diagonal wires block the cells they cross).
func (g *routeGrid) addWire(a, b Pt) {
	ga, oka := toGP(a)
	gb, okb := toGP(b)
	if oka {
		g.set(ga, cForbidden)
	}
	if okb {
		g.set(gb, cForbidden)
	}
	switch {
	case oka && okb && ga.Y == gb.Y:
		for x := min(ga.X, gb.X); x <= max(ga.X, gb.X); x++ {
			g.set(gp{x, ga.Y}, cH)
		}
	case oka && okb && ga.X == gb.X:
		for y := min(ga.Y, gb.Y); y <= max(ga.Y, gb.Y); y++ {
			g.set(gp{ga.X, y}, cV)
		}
	default:
		n := int(math.Ceil(math.Hypot(b.X-a.X, b.Y-a.Y)/(SchGrid/4))) + 1
		for i := 0; i <= n; i++ {
			p := Pt{a.X + (b.X-a.X)*float64(i)/float64(n), a.Y + (b.Y-a.Y)*float64(i)/float64(n)}
			q := gp{int(math.Round(p.X / SchGrid)), int(math.Round(p.Y / SchGrid))}
			g.set(q, cBlocked|cForbidden)
		}
	}
}

// dirs: right, up, left, down (counter-clockwise from 0°, screen y down).
var dirs = [4]gp{{1, 0}, {0, -1}, {-1, 0}, {0, 1}}

func dirOf(angle float64) int { return int(math.Round(normAngle(angle)/90)) % 4 }

type rTerm struct {
	at  gp
	out int // outward direction index, -1 free
}

type rState struct {
	p gp
	d int // arrival direction, 4 = start
}

type rNode struct {
	s    rState
	g, f int
	i    int
}

type rHeap []*rNode

func (h rHeap) Len() int { return len(h) }
func (h rHeap) Less(i, j int) bool {
	if h[i].f != h[j].f {
		return h[i].f < h[j].f
	}
	return h[i].g > h[j].g
}
func (h rHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i]; h[i].i, h[j].i = i, j }
func (h *rHeap) Push(x any)   { n := x.(*rNode); n.i = len(*h); *h = append(*h, n) }
func (h *rHeap) Pop() any     { o := *h; n := o[len(o)-1]; *h = o[:len(o)-1]; return n }

const (
	rStep   = 2
	rBend   = 5
	rCross  = 12
	rNearC  = 1
	rBudget = 400000
)

// route finds a Manhattan path from start (leaving along its outward axis)
// to any target point (arriving against a target's outward axis when it
// has one). allow lists cells usable despite being blocked/forbidden.
func (g *routeGrid) route(start rTerm, targets map[gp]int, allow map[gp]bool) ([]gp, bool) {
	if _, ok := targets[start.at]; ok {
		return []gp{start.at}, true
	}
	h := func(p gp) int {
		best := math.MaxInt
		for t := range targets {
			d := abs(t.X-p.X) + abs(t.Y-p.Y)
			if d < best {
				best = d
			}
		}
		return best * rStep
	}
	open := &rHeap{}
	best := map[rState]int{}
	parent := map[rState]rState{}
	s0 := rState{start.at, 4}
	heap.Push(open, &rNode{s: s0, g: 0, f: h(start.at)})
	best[s0] = 0
	expanded := 0
	for open.Len() > 0 && expanded < rBudget {
		cur := heap.Pop(open).(*rNode)
		if cur.g > best[cur.s] {
			continue
		}
		expanded++
		p, d := cur.s.p, cur.s.d
		if p != start.at {
			if want, ok := targets[p]; ok && (want < 0 || d == (want+2)%4) {
				var path []gp
				for s := cur.s; ; s = parent[s] {
					path = append(path, s.p)
					if s == s0 {
						break
					}
				}
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return path, true
			}
		}
		fl := g.get(p)
		crossing := p != start.at && !allow[p] && fl&(cH|cV) != 0
		for nd := 0; nd < 4; nd++ {
			if d < 4 && nd == (d+2)%4 {
				continue
			}
			if d == 4 && start.out >= 0 && nd != start.out {
				continue
			}
			if crossing && nd != d {
				continue // never turn on a foreign wire
			}
			q := gp{p.X + dirs[nd].X, p.Y + dirs[nd].Y}
			if !g.in(q) {
				continue
			}
			fq := g.get(q)
			_, isTarget := targets[q]
			if !allow[q] {
				if fq&cBlocked != 0 && !isTarget {
					continue
				}
				if fq&cForbidden != 0 && !isTarget {
					continue
				}
			}
			horiz := nd == 0 || nd == 2
			if horiz && fq&cH != 0 && (fl&cH != 0 || isTarget) && !allow[q] {
				continue // along a foreign horizontal wire
			}
			if !horiz && fq&cV != 0 && (fl&cV != 0 || isTarget) && !allow[q] {
				continue
			}
			cost := cur.g + rStep
			if d < 4 && nd != d {
				cost += rBend
			}
			if !allow[q] {
				if (horiz && fq&cV != 0) || (!horiz && fq&cH != 0) {
					cost += rCross
				}
				if fq&cNear != 0 {
					cost += rNearC
				}
			}
			ns := rState{q, nd}
			if b, ok := best[ns]; ok && b <= cost {
				continue
			}
			best[ns] = cost
			parent[ns] = cur.s
			heap.Push(open, &rNode{s: ns, g: cost, f: cost + h(q)})
		}
	}
	return nil, false
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// corners compresses a grid path to its end and corner points.
func corners(path []gp) []gp {
	if len(path) < 3 {
		return path
	}
	out := []gp{path[0]}
	for i := 1; i+1 < len(path); i++ {
		a, b, c := path[i-1], path[i], path[i+1]
		if (b.X-a.X != c.X-b.X) || (b.Y-a.Y != c.Y-b.Y) {
			out = append(out, b)
		}
	}
	return append(out, path[len(path)-1])
}

// ---- re-routing islands -------------------------------------------------------

// finalGeometry is the sheet after the moves (before re-routing): boxes to
// avoid, foreign wires and connection points.
func (e *SchEditor) finalGeometry(edits []textEdit) (*SchEditor, error) {
	// render the pending edits into a scratch editor and read it back
	s := e.src
	ed := append([]textEdit(nil), edits...)
	sort.SliceStable(ed, func(i, j int) bool { return ed[i].beg > ed[j].beg })
	for _, x := range ed {
		s = s[:x.beg] + x.text + s[x.end:]
	}
	return OpenSchematic(s)
}

func (e *SchEditor) rerouteIslands(reroute []int, isl []island, items []connItem, wires []wireNode, moved map[*sexp]*movedInst, pinT map[int]xform, edited map[*sexp]bool, opt DragOptions, res *DragResult) error {
	// terminals of each island at their final positions
	type term struct {
		at    Pt
		out   float64
		pin   string // "REF.PIN" for part pins
		isPin bool
	}
	gone := map[*sexp]bool{}
	terms := make([][]term, len(reroute))
	markers := make([][]int, len(reroute))
	floating := make([][]int, len(reroute))
	for k, ii := range reroute {
		is := isl[ii]
		for _, w := range is.wires {
			gone[wires[w].node] = true
		}
		for _, j := range is.items {
			it := items[j]
			switch it.kind {
			case "junction":
				gone[it.node] = true
				continue
			case "pin":
				p, out := it.at, it.outward
				if t, ok := pinT[j]; ok {
					p = t.apply(p)
					out = normAngle(out + t.angle())
				}
				terms[k] = append(terms[k], term{at: p, out: out, pin: it.ref, isPin: true})
				continue
			}
			// markers: those on a moved pin go with it
			p, out := it.at, it.outward
			onPin := false
			for _, q := range is.items {
				onPin = onPin || (items[q].kind == "pin" && samePt(items[q].at, it.at))
			}
			if it.kind == "label" && it.node.head() == LabelLocal && !onPin {
				// a local label on the wire only names the net: it is put
				// back on the new tree instead of pulling the tree to it
				gone[it.node] = true
				floating[k] = append(floating[k], j)
				continue
			}
			for pi, t := range pinT {
				if samePt(items[pi].at, it.at) {
					if !edited[it.node] {
						e.moveMarker(it, t, res)
						edited[it.node] = true
					}
					p = t.apply(p)
					if out >= 0 {
						out = normAngle(out + t.angle())
					}
					break
				}
			}
			if it.kind == "nc" {
				continue
			}
			markers[k] = append(markers[k], j)
			terms[k] = append(terms[k], term{at: p, out: out})
		}
	}
	for n := range gone {
		e.deleteNode(n)
	}
	// the sheet as it will be, minus the re-routed islands
	scratch, err := e.finalGeometry(e.repl)
	if err != nil {
		return err
	}
	sc, err := scratch.Scene()
	if err != nil {
		return err
	}
	area := Box{-50, -50, 1300, 900}
	if sc.TitleBlock != nil {
		area = Box{schBorder, schBorder, sc.TitleBlock.MaxX, sc.TitleBlock.MaxY}
	}
	g := newRouteGrid(area)
	for _, b := range sc.obstacleBoxes() {
		g.block(b)
	}
	if sc.TitleBlock != nil {
		tb := *sc.TitleBlock
		tb.MinX, tb.MinY = tb.MinX-SchGrid, tb.MinY-SchGrid
		g.block(tb)
	}
	g.markNear()
	for _, w := range sc.Wires {
		g.addWire(w[0], w[1])
	}
	for _, p := range sc.points() {
		if q, ok := toGP(p); ok {
			g.set(q, cForbidden)
		}
	}
	for k := range reroute {
		ts := terms[k]
		// dedupe terminals on one point (a label on a pin…): keep the pin
		sort.SliceStable(ts, func(i, j int) bool {
			if ts[i].isPin != ts[j].isPin {
				return ts[i].isPin
			}
			if ts[i].at.X != ts[j].at.X {
				return ts[i].at.X < ts[j].at.X
			}
			return ts[i].at.Y < ts[j].at.Y
		})
		var uniq []term
		for _, t := range ts {
			dup := false
			for _, u := range uniq {
				dup = dup || samePt(u.at, t.at)
			}
			if !dup {
				uniq = append(uniq, t)
			}
		}
		ok := len(uniq) > 0
		var gts []rTerm
		for _, t := range uniq {
			q, on := toGP(t.at)
			ok = ok && on
			o := -1
			if t.out >= 0 {
				o = dirOf(t.out)
			}
			gts = append(gts, rTerm{q, o})
		}
		var segs [][2]gp
		var junctions []gp
		if ok && len(gts) > 1 {
			// escape corridors: short pins end inside their own body box
			escape := make([][]gp, len(gts))
			for i, t := range gts {
				escape[i] = []gp{t.at}
				if t.out < 0 {
					continue
				}
				for k, p := 1, t.at; k < 24; k++ {
					p = gp{p.X + dirs[t.out].X, p.Y + dirs[t.out].Y}
					if g.get(p)&cBlocked == 0 {
						break
					}
					escape[i] = append(escape[i], p)
				}
			}
			tree := map[gp]int{gts[0].at: gts[0].out}
			ends := map[gp]int{}
			done := make([]bool, len(gts))
			done[0] = true
			for left := len(gts) - 1; left > 0 && ok; left-- {
				// Prim: the unconnected terminal closest to the tree
				bi, bd := -1, math.MaxInt
				for i, t := range gts {
					if done[i] {
						continue
					}
					for p := range tree {
						if d := abs(p.X-t.at.X) + abs(p.Y-t.at.Y); d < bd {
							bi, bd = i, d
						}
					}
				}
				allow := map[gp]bool{}
				for i := range gts {
					if done[i] || i == bi {
						for _, p := range escape[i] {
							allow[p] = true
						}
					}
				}
				path, found := g.route(gts[bi], tree, allow)
				if !found {
					ok = false
					break
				}
				done[bi] = true
				last := path[len(path)-1]
				if ends[last] == 0 && !isTerm(gts, last) {
					junctions = append(junctions, last) // T onto the middle of the tree
				}
				cs := corners(path)
				for i := 0; i+1 < len(cs); i++ {
					segs = append(segs, [2]gp{cs[i], cs[i+1]})
					ends[cs[i]]++
					ends[cs[i+1]]++
				}
				for _, p := range path {
					if _, has := tree[p]; !has && g.get(p)&(cH|cV) == 0 {
						tree[p] = -1 // free arrival anywhere on the new run (not on a crossing)
					}
				}
				tree[gts[bi].at] = gts[bi].out
			}
			for p, n := range ends {
				if n >= 3 && !isTerm(gts, p) {
					junctions = append(junctions, p)
				}
			}
		}
		if ok {
			e.replaceLabels(g, segs, floating[k], items)
			for _, s := range segs {
				e.AddWire(s[0].pt(), s[1].pt())
				g.addWire(s[0].pt(), s[1].pt())
				res.NewWires++
			}
			seen := map[gp]bool{}
			for _, j := range junctions {
				if !seen[j] {
					seen[j] = true
					e.AddJunction(j.pt())
				}
			}
			res.Rerouted++
			continue
		}

		// fallback: a net label (or power symbol) on every pin, the island's
		// other markers removed
		name, kind, err := e.fallbackName(isl[reroute[k]], items, opt, res)
		if err != nil {
			return err
		}
		for _, j := range markers[k] {
			if !gone[items[j].node] {
				e.deleteNode(items[j].node)
				gone[items[j].node] = true
			}
		}
		for _, t := range uniq {
			if !t.isPin {
				continue
			}
			if err := e.placeNetMarker(kind, name, t.at, t.out); err != nil {
				return err
			}
		}
		res.Fallback = append(res.Fallback, name)
	}
	return nil
}

func isTerm(ts []rTerm, p gp) bool {
	for _, t := range ts {
		if t.at == p {
			return true
		}
	}
	return false
}

// fallbackName picks the label name and kind for an island that could not
// be re-routed.
func (e *SchEditor) fallbackName(is island, items []connItem, opt DragOptions, res *DragResult) (string, string, error) {
	kind := LabelLocal
	var firstPin string
	for _, j := range is.items {
		it := items[j]
		switch {
		case it.kind == "power":
			kind = "power"
		case it.kind == "label" && kind != "power":
			if h := it.node.head(); h != LabelLocal {
				kind = h
			}
		case it.kind == "pin" && firstPin == "":
			firstPin = it.ref
		}
	}
	if kind == "power" {
		for _, j := range is.items {
			if items[j].kind == "power" {
				return propVal(items[j].node, "Value"), kind, nil
			}
		}
	}
	for _, j := range is.items {
		if items[j].kind == "label" && len(items[j].node.list) > 1 {
			return items[j].node.list[1].atom, kind, nil
		}
	}
	if opt.PinNets == nil || firstPin == "" {
		return "", "", fmt.Errorf("net of %s cannot be re-routed and its name is unknown (no netlist)", firstPin)
	}
	n := opt.PinNets[firstPin]
	if n == "" || strings.HasPrefix(n, "Net-(") || strings.HasPrefix(n, "unconnected-(") {
		name := "N_" + strings.NewReplacer(".", "_", ":", "_", " ", "_").Replace(firstPin)
		res.Renamed = append(res.Renamed, fmt.Sprintf("%s → %s", n, name))
		return name, kind, nil
	}
	if i := strings.LastIndex(n, "/"); i >= 0 {
		n = n[i+1:]
	}
	return n, kind, nil
}

// placeNetMarker puts a label (kind label/global_label/hierarchical_label)
// or a power symbol of net at a pin end, reading along out.
func (e *SchEditor) placeNetMarker(kind, net string, at Pt, out float64) error {
	d := dirOf(out)
	if kind == "power" {
		ground := IsGroundNet(net)
		// the symbol body points along the pin's outward axis
		idx := map[int]int{1: 0, 2: 1, 3: 2, 0: 3}[d] // up0 left1 down2 right3
		anchor := 0
		if ground {
			anchor = 2
		}
		_, err := e.AddPower(net, at, float64((idx-anchor+4)%4)*90, ground)
		return err
	}
	shape := ""
	if kind != LabelLocal {
		shape = "passive"
	}
	return e.AddLabel(kind, net, at, float64(d)*90, shape)
}

// ---- scene helpers used by the router and the quality checks ------------------

// obstacleBoxes are the boxes a wire must not enter: symbol bodies (with
// pins), visible fields, labels and texts.
func (sc *SchScene) obstacleBoxes() []Box {
	var out []Box
	for _, s := range sc.Symbols {
		if s.HasBox {
			out = append(out, s.Box)
		}
		for _, f := range s.Fields {
			out = append(out, f.Box)
		}
	}
	for _, l := range sc.Labels {
		out = append(out, l.Box)
	}
	return append(out, sc.Texts...)
}

// points are the connection points a foreign wire must not touch.
func (sc *SchScene) points() []Pt {
	var out []Pt
	for _, s := range sc.Symbols {
		for _, p := range s.Pins {
			out = append(out, p.At)
		}
	}
	for _, l := range sc.Labels {
		out = append(out, l.At)
	}
	out = append(out, sc.Junctions...)
	out = append(out, sc.NoConnects...)
	return out
}

// replaceLabels puts the island's floating local labels (one per name) back
// on the new tree: on a horizontal run reading right (else a vertical run
// reading up), two grid steps in from a run end, where the text covers no
// body, field, label, text or foreign wire.
func (e *SchEditor) replaceLabels(g *routeGrid, segs [][2]gp, floating []int, items []connItem) {
	seen := map[string]bool{}
	sorted := append([][2]gp(nil), segs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		hi, hj := sorted[i][0].Y == sorted[i][1].Y, sorted[j][0].Y == sorted[j][1].Y
		if hi != hj {
			return hi
		}
		li := abs(sorted[i][0].X-sorted[i][1].X) + abs(sorted[i][0].Y-sorted[i][1].Y)
		lj := abs(sorted[j][0].X-sorted[j][1].X) + abs(sorted[j][0].Y-sorted[j][1].Y)
		return li > lj
	})
	for _, j := range floating {
		name := items[j].node.list[1].atom
		if seen[name] {
			continue
		}
		seen[name] = true
		at, ang, found := Pt{}, 0.0, false
		for _, s := range sorted {
			a, b := s[0], s[1]
			horiz := a.Y == b.Y
			if horiz && a.X > b.X || !horiz && a.Y < b.Y {
				a, b = b, a // left to right / bottom to top
			}
			n := abs(b.X-a.X) + abs(b.Y-a.Y)
			for k := 2; k <= n-2 && !found; k++ {
				p := gp{a.X + k*sign(b.X-a.X), a.Y + k*sign(b.Y-a.Y)}
				l := SceneLabel{Kind: LabelLocal, Name: name, At: p.pt()}
				if !horiz {
					l.Angle = 90
				}
				if g.boxFree(labelCheckBox(l)) {
					at, ang, found = p.pt(), l.Angle, true
				}
			}
			if found {
				break
			}
		}
		if !found && len(sorted) > 0 { // somewhere on the longest run
			a, b := sorted[0][0], sorted[0][1]
			at = gp{(a.X + b.X) / 2, (a.Y + b.Y) / 2}.pt()
			if a.Y != b.Y {
				ang = 90
			}
		}
		_ = e.AddLabel(LabelLocal, name, at, ang, "")
	}
}

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// boxFree reports whether no grid point strictly inside b is blocked or on a
// foreign wire.
func (g *routeGrid) boxFree(b Box) bool {
	x0, x1 := int(math.Floor(b.MinX/SchGrid)), int(math.Ceil(b.MaxX/SchGrid))
	y0, y1 := int(math.Floor(b.MinY/SchGrid)), int(math.Ceil(b.MaxY/SchGrid))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			px, py := float64(x)*SchGrid, float64(y)*SchGrid
			if px > b.MinX && px < b.MaxX && py > b.MinY && py < b.MaxY && g.get(gp{x, y})&(cBlocked|cH|cV|cForbidden) != 0 {
				return false
			}
		}
	}
	return true
}
