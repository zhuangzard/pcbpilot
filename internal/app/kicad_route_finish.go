package app

// kicad_route_finish.go — the signal finisher: after the router, a signal net
// whose copper is still in more than one group is joined by our own bounded
// grid search, group to group, on the layers the layer plan leaves free (the
// poured layers only near the pads joined). Router copper is never moved or
// removed. A join is planned by geometry and proven by the board: refilled,
// its groups must have merged in KiCad's connectivity with no new DRC or
// insulation error at its copper and no pad cut off a pour; what fails comes
// out again and is reported. A partial result never makes route-complete pass.

import (
	"container/heap"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

const (
	finishStepMil   = 5.0
	finishViaCost   = 60.0  // mil of track one via is worth
	finishEscapeMil = 150.0 // how far from a joined pad a poured layer may be used
	finishMarginMil = 300.0 // search window around the two groups
	finishBucketMil = 100.0
	// finishSlack: a grid step checked at its two ends is clear along its whole
	// length if both ends clear by half the longest step more (distance is
	// 1-Lipschitz).
	finishSlack      = finishStepMil * math.Sqrt2 / 2
	finishJoinNodes  = 2000000   // expansions one join may use
	finishTotalNodes = 150000000 // and all joins together (about 4 min per 40 M)
	finishMaxPasses  = 2
	// finishMaxRounds: edits to settle one pass. Dropping a join can expose
	// another's error, and a large pass sheds a few per round.
	finishMaxRounds = 6
	finishNearMil   = 30.0  // a new DRC or insulation error this close is the join's
	finishFarMil    = 200.0 // and this close when its items name no join's net
)

// finishSize is the track, via and clearance a net's joins use (mil).
type finishSize struct {
	Track, Clr, Dia, Drill float64
}

// finishObj is one piece of copper: a pad box (a, b the corners), a track or
// a through via (a == b).
type finishObj struct {
	net   string
	layer int // pcbLayerMulti = every layer
	pad   bool
	a, b  [2]float64
	half  float64
	tag   int // the group it belongs to, in a search
	// A pad's box is its bounding box, so it is a safe obstacle but not a safe
	// place to end a track: core and r (r < 0 = none) are the stadium inside the
	// real shape (rectangle, oval, circle or rounded rectangle) a track may end in.
	core [2][2]float64
	r    float64
}

func (o *finishObj) on(layer int) bool { return o.layer == layer || o.layer == pcbLayerMulti }

func (o *finishObj) via() bool { return !o.pad && o.layer == pcbLayerMulti }

// dist is the gap from q to the copper (0 inside a pad, negative inside a
// track or via).
func (o *finishObj) dist(q [2]float64) float64 {
	if o.pad {
		return rectPtDist(o.a[0], o.a[1], o.b[0], o.b[1], q[0], q[1])
	}
	return segDist(q, o.a, o.b) - o.half
}

func (o *finishObj) segGap(a, b [2]float64) float64 {
	if o.pad {
		return rectSegDist(o.a[0], o.a[1], o.b[0], o.b[1], a[0], a[1], b[0], b[1])
	}
	return segSegDist(a[0], a[1], b[0], b[1], o.a[0], o.a[1], o.b[0], o.b[1]) - o.half
}

// inside: a track ending at q would overlap this copper.
func (o *finishObj) inside(q [2]float64) bool {
	if o.pad {
		return o.r > 0 && segDist(q, o.core[0], o.core[1]) <= o.r
	}
	return o.dist(q) <= -0.5
}

func (o *finishObj) box() ([2]float64, [2]float64) {
	if o.pad {
		return o.a, o.b
	}
	return [2]float64{math.Min(o.a[0], o.b[0]) - o.half, math.Min(o.a[1], o.b[1]) - o.half},
		[2]float64{math.Max(o.a[0], o.b[0]) + o.half, math.Max(o.a[1], o.b[1]) + o.half}
}

func padObj(p boardPad) finishObj {
	o := finishObj{net: p.Net, layer: p.Layer, pad: true, a: [2]float64{p.X - p.W/2, p.Y - p.H/2}, b: [2]float64{p.X + p.W/2, p.Y + p.H/2}, r: -1}
	c := [2]float64{p.X, p.Y}
	o.core = [2][2]float64{c, c}
	kind, w, h := "", 0.0, 0.0
	if s, ok := p.Shape.([]any); ok && len(s) == 3 {
		kind, _ = s[0].(string)
		w, _ = s[1].(float64)
		h, _ = s[2].(float64)
	}
	rot := math.Mod(p.Rotation, 360)
	if kind == "" { // no shape read back: the box is taken as the pad
		kind, w, h, rot = "RECT", p.W, p.H, 0
	}
	if kind == "POLYGON" || w <= 0 || h <= 0 {
		return o // a custom shape need not hold its own centre
	}
	if rot < 0 {
		rot += 360
	}
	if math.Mod(rot, 90) > 1e-6 && 90-math.Mod(rot, 90) > 1e-6 {
		o.r = math.Min(w, h)/2 - 0.5 // turned off the axes: the inscribed circle only
		return o
	}
	if math.Abs(rot-90) < 1e-6 || math.Abs(rot-270) < 1e-6 {
		w, h = h, w
	}
	if w >= h {
		o.core = [2][2]float64{{p.X - (w-h)/2, p.Y}, {p.X + (w-h)/2, p.Y}}
	} else {
		o.core = [2][2]float64{{p.X, p.Y - (h-w)/2}, {p.X, p.Y + (h-w)/2}}
	}
	o.r = math.Min(w, h)/2 - 0.5
	return o
}

// finishIndex buckets copper by area.
type finishIndex struct {
	objs  []finishObj
	cells map[[2]int][]int
	seen  []int
	stamp int
}

func newFinishIndex() *finishIndex { return &finishIndex{cells: map[[2]int][]int{}} }

func finishCell(v float64) int { return int(math.Floor(v / finishBucketMil)) }

func (x *finishIndex) add(o finishObj) {
	x.objs = append(x.objs, o)
	x.seen = append(x.seen, 0)
	i := len(x.objs) - 1
	lo, hi := o.box()
	for cx := finishCell(lo[0]); cx <= finishCell(hi[0]); cx++ {
		for cy := finishCell(lo[1]); cy <= finishCell(hi[1]); cy++ {
			k := [2]int{cx, cy}
			x.cells[k] = append(x.cells[k], i)
		}
	}
}

// each calls fn on every object whose bucket meets the box, once, until fn
// returns false; it reports whether it ran to the end.
func (x *finishIndex) each(lo, hi [2]float64, fn func(o *finishObj) bool) bool {
	x.stamp++
	for cx := finishCell(lo[0]); cx <= finishCell(hi[0]); cx++ {
		for cy := finishCell(lo[1]); cy <= finishCell(hi[1]); cy++ {
			for _, i := range x.cells[[2]int{cx, cy}] {
				if x.seen[i] == x.stamp {
					continue
				}
				x.seen[i] = x.stamp
				if !fn(&x.objs[i]) {
					return false
				}
			}
		}
	}
	return true
}

// finishField is a coarse distance map to the copper a search must reach: one
// value per bucket of the window, the chamfer distance from the buckets that
// hold that copper. It guides the search; it bounds nothing.
type finishField struct {
	x0, y0, w, h int
	d            []float64
}

func newFinishField(dst *finishIndex, lo, hi [2]float64) *finishField {
	ff := &finishField{x0: finishCell(lo[0]), y0: finishCell(lo[1])}
	ff.w, ff.h = finishCell(hi[0])-ff.x0+1, finishCell(hi[1])-ff.y0+1
	ff.d = make([]float64, ff.w*ff.h)
	for i := range ff.d {
		ff.d[i] = math.Inf(1)
	}
	for k := range dst.cells {
		if x, y := k[0]-ff.x0, k[1]-ff.y0; x >= 0 && x < ff.w && y >= 0 && y < ff.h {
			ff.d[y*ff.w+x] = 0
		}
	}
	step := func(x, y, dx, dy int) {
		nx, ny := x+dx, y+dy
		if nx < 0 || nx >= ff.w || ny < 0 || ny >= ff.h {
			return
		}
		c := finishBucketMil
		if dx != 0 && dy != 0 {
			c *= math.Sqrt2
		}
		if v := ff.d[ny*ff.w+nx] + c; v < ff.d[y*ff.w+x] {
			ff.d[y*ff.w+x] = v
		}
	}
	for y := 0; y < ff.h; y++ {
		for x := 0; x < ff.w; x++ {
			step(x, y, -1, 0)
			step(x, y, -1, -1)
			step(x, y, 0, -1)
			step(x, y, 1, -1)
		}
	}
	for y := ff.h - 1; y >= 0; y-- {
		for x := ff.w - 1; x >= 0; x-- {
			step(x, y, 1, 0)
			step(x, y, 1, 1)
			step(x, y, 0, 1)
			step(x, y, -1, 1)
		}
	}
	return ff
}

// at is the field at q less one bucket, never below 0.
func (ff *finishField) at(q [2]float64) float64 {
	x, y := finishCell(q[0])-ff.x0, finishCell(q[1])-ff.y0
	if x < 0 || x >= ff.w || y < 0 || y >= ff.h {
		return 0
	}
	return math.Max(ff.d[y*ff.w+x]-finishBucketMil, 0)
}

// finishCorners keeps the ends of a grid path and the points where it turns.
func finishCorners(pts [][2]float64) [][2]float64 {
	if len(pts) < 3 {
		return pts
	}
	out := [][2]float64{pts[0]}
	for i := 1; i+1 < len(pts); i++ {
		a, b, c := pts[i-1], pts[i], pts[i+1]
		ux, uy, vx, vy := b[0]-a[0], b[1]-a[1], c[0]-b[0], c[1]-b[1]
		if math.Abs(ux*vy-uy*vx) > 1e-6 || ux*vx+uy*vy <= 0 {
			out = append(out, b)
		}
	}
	return append(out, pts[len(pts)-1])
}

// finishJoin is one planned connection: the group it starts from, the group
// it reaches, and the copper between them.
type finishJoin struct {
	Net       string              `json:"net"`
	Pads      []string            `json:"pads"` // the group it starts from
	To        []string            `json:"to"`   // the group it reached
	Tracks    []specctra.NewTrack `json:"tracks"`
	Vias      []specctra.NewVia   `json:"vias,omitempty"`
	Drill     float64             `json:"drillMil,omitempty"`
	LengthMil float64             `json:"lengthMil"`
	Layers    []int               `json:"layers"`
}

func (j *finishJoin) key() string { return j.Net + ":" + strings.Join(j.Pads, ",") }

// near is the gap from q to the join's copper.
func (j *finishJoin) near(q [2]float64) float64 {
	d := math.Inf(1)
	for _, t := range j.Tracks {
		d = math.Min(d, segDist(q, [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}))
	}
	for _, v := range j.Vias {
		d = math.Min(d, math.Hypot(q[0]-v.X, q[1]-v.Y))
	}
	return d
}

// finisher plans joins over one board's copper.
type finisher struct {
	idx      *finishIndex
	clr      map[string]float64
	defClr   float64
	maxClr   float64
	bounds   layoutBBox
	layers   []int        // copper layers a join may use, in id order
	pour     map[int]bool // layers the layer plan pours
	pourMode string       // escape | on | off
	nodes    int          // expanded so far
}

func (f *finisher) clrOf(a, b string) float64 {
	ca, ok := f.clr[a]
	if !ok {
		ca = f.defClr
	}
	cb, ok := f.clr[b]
	if !ok {
		cb = f.defClr
	}
	return math.Max(ca, cb)
}

func (f *finisher) inBounds(q [2]float64, r float64) bool {
	return q[0]-r >= f.bounds.MinX && q[0]+r <= f.bounds.MaxX && q[1]-r >= f.bounds.MinY && q[1]+r <= f.bounds.MaxY
}

// layerOK applies the poured-layer policy at q: escape allows a poured layer
// only within finishEscapeMil of one of the pads being joined.
func (f *finisher) layerOK(l int, q [2]float64, ends []boardPad) bool {
	if !f.pour[l] {
		return true
	}
	switch f.pourMode {
	case "on":
		return true
	case "off":
		return false
	}
	// Half a sample short of the limit: segAllowed samples every grid step and
	// the distance to a pad changes by at most the distance moved.
	for _, p := range ends {
		if rectPtDist(p.X-p.W/2, p.Y-p.H/2, p.X+p.W/2, p.Y+p.H/2, q[0], q[1]) <= finishEscapeMil-finishStepMil/2 {
			return true
		}
	}
	return false
}

// segAllowed is layerOK along a straight track, every grid step.
func (f *finisher) segAllowed(l int, a, b [2]float64, ends []boardPad) bool {
	if !f.pour[l] || f.pourMode == "on" {
		return true
	}
	n := int(math.Hypot(b[0]-a[0], b[1]-a[1])/finishStepMil) + 1
	for k := 0; k <= n; k++ {
		t := float64(k) / float64(n)
		if !f.layerOK(l, [2]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t}, ends) {
			return false
		}
	}
	return true
}

// clearAt: a track end of half width half at q on layer keeps every other
// net's clearance, with extra to spare.
func (f *finisher) clearAt(net string, layer int, q [2]float64, half, extra float64) bool {
	if !f.inBounds(q, half) {
		return false
	}
	r := half + f.maxClr + extra
	return f.idx.each([2]float64{q[0] - r, q[1] - r}, [2]float64{q[0] + r, q[1] + r}, func(o *finishObj) bool {
		return o.net == net || !o.on(layer) || o.dist(q)-half >= f.clrOf(net, o.net)+extra
	})
}

// segFits is clearAt for a whole straight track.
func (f *finisher) segFits(net string, layer int, a, b [2]float64, half float64) bool {
	if !f.inBounds(a, half) || !f.inBounds(b, half) {
		return false
	}
	r := half + f.maxClr
	lo := [2]float64{math.Min(a[0], b[0]) - r, math.Min(a[1], b[1]) - r}
	hi := [2]float64{math.Max(a[0], b[0]) + r, math.Max(a[1], b[1]) + r}
	return f.idx.each(lo, hi, func(o *finishObj) bool {
		return o.net == net || !o.on(layer) || o.segGap(a, b)-half >= f.clrOf(net, o.net)
	})
}

// viaAt: a through via fits at q: clear of every other net on every layer,
// out of every pad (its own net's too) and apart from its own net's vias.
func (f *finisher) viaAt(net string, q [2]float64, dia float64) bool {
	if !f.inBounds(q, dia/2) {
		return false
	}
	r := dia/2 + f.maxClr
	return f.idx.each([2]float64{q[0] - r, q[1] - r}, [2]float64{q[0] + r, q[1] + r}, func(o *finishObj) bool {
		if o.net == net && !o.pad && !o.via() {
			return true
		}
		return o.dist(q)-dia/2 >= f.clrOf(net, o.net)
	})
}

type finishNode struct{ l, x, y int }

type finishItem struct {
	n    finishNode
	g, f float64
}

type finishQueue []finishItem

func (q finishQueue) Len() int           { return len(q) }
func (q finishQueue) Less(i, j int) bool { return q[i].f < q[j].f }
func (q finishQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *finishQueue) Push(x any)        { *q = append(*q, x.(finishItem)) }
func (q *finishQueue) Pop() any {
	o := *q
	n := o[len(o)-1]
	*q = o[:len(o)-1]
	return n
}

type finishPoint struct {
	layer int
	q     [2]float64
}

// search finds a path from the src copper to the dst copper of net, of cost
// at most maxCost, within budget expansions. It returns the points, the tag
// of the dst object reached, and why it failed ("" when it did not).
func (f *finisher) search(net string, sz finishSize, src, dst []finishObj, ends []boardPad, maxCost float64, budget int) ([]finishPoint, int, string) {
	if len(src) == 0 || len(dst) == 0 {
		return nil, 0, "nothing to join"
	}
	half := sz.Track / 2
	dIdx := newFinishIndex()
	lo, hi := src[0].box()
	grow := func(o finishObj) {
		a, b := o.box()
		lo = [2]float64{math.Min(lo[0], a[0]), math.Min(lo[1], a[1])}
		hi = [2]float64{math.Max(hi[0], b[0]), math.Max(hi[1], b[1])}
	}
	for _, o := range src {
		grow(o)
	}
	for _, o := range dst {
		grow(o)
		dIdx.add(o)
	}
	lo = [2]float64{lo[0] - finishMarginMil, lo[1] - finishMarginMil}
	hi = [2]float64{hi[0] + finishMarginMil, hi[1] + finishMarginMil}
	// The grid sits on the first source object, so a pad's centre line is on it.
	origin := src[0].a
	if src[0].pad {
		origin = [2]float64{(src[0].core[0][0] + src[0].core[1][0]) / 2, (src[0].core[0][1] + src[0].core[1][1]) / 2}
	}
	at := func(n finishNode) [2]float64 {
		return [2]float64{math.Round((origin[0]+float64(n.x)*finishStepMil)*100) / 100, math.Round((origin[1]+float64(n.y)*finishStepMil)*100) / 100}
	}
	inWin := func(q [2]float64) bool { return q[0] >= lo[0] && q[0] <= hi[0] && q[1] >= lo[1] && q[1] <= hi[1] }
	mult := func(l int) float64 {
		if f.pour[l] && f.pourMode == "on" {
			return 2
		}
		return 1
	}
	open := map[finishNode]bool{}
	nodeOK := func(n finishNode) bool {
		if v, ok := open[n]; ok {
			return v
		}
		q, l := at(n), f.layers[n.l]
		ok := inWin(q) && f.layerOK(l, q, ends) && f.clearAt(net, l, q, half, finishSlack)
		open[n] = ok
		return ok
	}
	viaOK := map[[2]int]bool{}
	canVia := func(n finishNode) bool {
		k := [2]int{n.x, n.y}
		if v, ok := viaOK[k]; ok {
			return v
		}
		v := f.viaAt(net, at(n), sz.Dia)
		viaOK[k] = v
		return v
	}
	goal := func(n finishNode) (int, bool) {
		q, l := at(n), f.layers[n.l]
		tag, hit := 0, false
		dIdx.each(q, q, func(o *finishObj) bool {
			if o.on(l) && o.inside(q) {
				tag, hit = o.tag, true
				return false
			}
			return true
		})
		return tag, hit
	}
	field := newFinishField(dIdx, lo, hi)
	h := func(n finishNode) float64 { return field.at(at(n)) }
	best := map[finishNode]float64{}
	came := map[finishNode]finishNode{}
	q := &finishQueue{}
	start := func(n finishNode) {
		if _, seen := best[n]; seen || !nodeOK(n) {
			return
		}
		best[n] = 0
		heap.Push(q, finishItem{n, 0, h(n)})
	}
	// Seeds: the grid points inside the source copper, found along each piece
	// (a track's line, not its box), and paid for from the budget.
	expanded := 0
	seed := func(o *finishObj, p [2]float64) {
		gx, gy := int(math.Floor((p[0]-origin[0])/finishStepMil)), int(math.Floor((p[1]-origin[1])/finishStepMil))
		for x := gx - 1; x <= gx+2; x++ {
			for y := gy - 1; y <= gy+2; y++ {
				for li, l := range f.layers {
					if n := (finishNode{li, x, y}); o.on(l) && o.inside(at(n)) {
						expanded++
						start(n)
					}
				}
			}
		}
	}
	for i := range src {
		o := &src[i]
		a, b := o.a, o.b
		if o.pad {
			a, b = o.core[0], o.core[1]
		}
		steps := int(math.Hypot(b[0]-a[0], b[1]-a[1])/finishStepMil) + 1
		for k := 0; k <= steps && expanded < budget; k++ {
			t := float64(k) / float64(steps)
			seed(o, [2]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t})
		}
	}
	f.nodes += expanded
	for q.Len() > 0 {
		it := heap.Pop(q).(finishItem)
		if it.g > best[it.n] {
			continue
		}
		if tag, ok := goal(it.n); ok && it.g > 0 {
			var path []finishPoint
			for c := it.n; ; {
				path = append([]finishPoint{{f.layers[c.l], at(c)}}, path...)
				p, ok := came[c]
				if !ok {
					break
				}
				c = p
			}
			return path, tag, ""
		}
		expanded++
		f.nodes++
		if expanded > budget {
			return nil, 0, "search budget"
		}
		try := func(m finishNode, c float64) {
			if c > maxCost {
				return
			}
			if b, seen := best[m]; seen && b <= c {
				return
			}
			if !nodeOK(m) {
				return
			}
			best[m], came[m] = c, it.n
			heap.Push(q, finishItem{m, c, c + h(m)})
		}
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				if dx != 0 || dy != 0 {
					try(finishNode{it.n.l, it.n.x + dx, it.n.y + dy}, it.g+math.Hypot(float64(dx), float64(dy))*finishStepMil*mult(f.layers[it.n.l]))
				}
			}
		}
		if len(f.layers) > 1 && canVia(it.n) {
			for li := range f.layers {
				if li != it.n.l {
					try(finishNode{li, it.n.x, it.n.y}, it.g+finishViaCost)
				}
			}
		}
	}
	return nil, 0, "no path"
}

// joinOf turns a searched path into tracks and through vias, every straight
// piece as long as the exact check allows.
func (f *finisher) joinOf(net string, sz finishSize, path []finishPoint, ends []boardPad) (finishJoin, bool) {
	j := finishJoin{Net: net, Drill: sz.Drill}
	half := sz.Track / 2
	layers := map[int]bool{}
	for i := 0; i < len(path); {
		k := i
		for k+1 < len(path) && path[k+1].layer == path[i].layer {
			k++
		}
		l := path[i].layer
		var pts [][2]float64
		for _, p := range path[i : k+1] {
			pts = append(pts, p.q)
		}
		if len(pts) > 1 {
			// Every grid step is clear (finishSlack), so a straight run is too.
			pts = stitchPathShorten(finishCorners(pts), func(a, b [2]float64) bool { return f.segFits(net, l, a, b, half) && f.segAllowed(l, a, b, ends) })
			for m := 0; m+1 < len(pts); m++ {
				if !f.segFits(net, l, pts[m], pts[m+1], half) || !f.segAllowed(l, pts[m], pts[m+1], ends) {
					return j, false
				}
				j.Tracks = append(j.Tracks, specctra.NewTrack{Net: net, Layer: l, X1: pts[m][0], Y1: pts[m][1], X2: pts[m+1][0], Y2: pts[m+1][1], Width: sz.Track})
				j.LengthMil += math.Hypot(pts[m+1][0]-pts[m][0], pts[m+1][1]-pts[m][1])
			}
			layers[l] = true
		}
		if k+1 < len(path) { // a layer change at the same point
			q := path[k].q
			if n := len(j.Vias); n == 0 || j.Vias[n-1].X != q[0] || j.Vias[n-1].Y != q[1] {
				if !f.viaAt(net, q, sz.Dia) {
					return j, false
				}
				j.Vias = append(j.Vias, specctra.NewVia{Net: net, X: q[0], Y: q[1], DiameterMil: sz.Dia})
			}
		}
		i = k + 1
	}
	for l := range layers {
		j.Layers = append(j.Layers, l)
	}
	sort.Ints(j.Layers)
	return j, len(j.Tracks) > 0 || len(j.Vias) > 0
}

// addJoin makes a planned join copper every later join must clear.
func (f *finisher) addJoin(j finishJoin) {
	for _, t := range j.Tracks {
		f.idx.add(finishObj{net: j.Net, layer: t.Layer, a: [2]float64{t.X1, t.Y1}, b: [2]float64{t.X2, t.Y2}, half: t.Width / 2})
	}
	for _, v := range j.Vias {
		q := [2]float64{v.X, v.Y}
		f.idx.add(finishObj{net: j.Net, layer: pcbLayerMulti, a: q, b: q, half: v.DiameterMil / 2})
	}
}

// finishComp is one connected group of a net: its pads and all its copper.
type finishComp struct {
	pads []boardPad
	objs []finishObj
}

// finishComponents gives each KiCad pad group of a net the tracks and vias that
// touch it, directly or through each other. Copper touching no pad is left out.
func finishComponents(groups []kicad.PadGroup, padByID map[string]boardPad, tracks []specctra.Track, vias []widenVia) []finishComp {
	var objs []finishObj
	var owner []int // group index, -1 for copper
	for gi, g := range groups {
		for _, id := range g.Pads {
			if p, ok := padByID[id]; ok {
				objs = append(objs, padObj(p))
				owner = append(owner, gi)
			}
		}
	}
	for _, t := range tracks {
		objs = append(objs, finishObj{net: t.Net, layer: t.Layer, a: [2]float64{t.X1, t.Y1}, b: [2]float64{t.X2, t.Y2}, half: t.Width / 2})
		owner = append(owner, -1)
	}
	for _, v := range vias {
		q := [2]float64{v.X, v.Y}
		objs = append(objs, finishObj{net: v.Net, layer: pcbLayerMulti, a: q, b: q, half: v.Diameter / 2})
		owner = append(owner, -1)
	}
	parent := make([]int, len(objs))
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
	// KiCad's groups are the truth: geometry may hang copper on a group but
	// never merge two (a pad box is larger than a round pad).
	grp := append([]int(nil), owner...)
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra == rb || grp[ra] >= 0 && grp[rb] >= 0 && grp[ra] != grp[rb] {
			return
		}
		parent[ra] = rb
		if grp[rb] < 0 {
			grp[rb] = grp[ra]
		}
	}
	first := map[int]int{}
	for i, g := range owner {
		if g < 0 {
			continue
		}
		if f, ok := first[g]; ok {
			union(i, f)
		} else {
			first[g] = i
		}
	}
	touch := func(a, b *finishObj) bool {
		if !(a.on(b.layer) || b.on(a.layer)) {
			return false
		}
		switch {
		case a.pad && b.pad:
			return false // pads join through their KiCad group
		case a.pad:
			return rectSegDist(a.a[0], a.a[1], a.b[0], a.b[1], b.a[0], b.a[1], b.b[0], b.b[1])-b.half <= 0
		case b.pad:
			return rectSegDist(b.a[0], b.a[1], b.b[0], b.b[1], a.a[0], a.a[1], a.b[0], a.b[1])-a.half <= 0
		}
		return segSegDist(a.a[0], a.a[1], a.b[0], a.b[1], b.a[0], b.a[1], b.b[0], b.b[1])-a.half-b.half <= 0.01
	}
	for i := range objs {
		for k := i + 1; k < len(objs); k++ {
			if (owner[i] < 0 || owner[k] < 0) && find(i) != find(k) && touch(&objs[i], &objs[k]) {
				union(i, k)
			}
		}
	}
	byRoot := map[int]*finishComp{}
	var order []int
	for gi := range groups {
		if f, ok := first[gi]; ok {
			r := find(f)
			if _, ok := byRoot[r]; !ok {
				byRoot[r] = &finishComp{}
				order = append(order, r)
			}
		}
	}
	for i := range objs {
		if c, ok := byRoot[find(i)]; ok {
			c.objs = append(c.objs, objs[i])
		}
	}
	for gi, g := range groups {
		f, ok := first[gi]
		if !ok {
			continue
		}
		c := byRoot[find(f)]
		for _, id := range g.Pads {
			if p, ok := padByID[id]; ok {
				c.pads = append(c.pads, p)
			}
		}
	}
	out := make([]finishComp, 0, len(order))
	for _, r := range order {
		out = append(out, *byRoot[r])
	}
	return out
}

// plan joins the groups of every net in nets, nearest first. A net's largest
// group is the one the others join; each planned join is copper for the next.
func (f *finisher) plan(nets []string, groups map[string][]kicad.PadGroup, padByID map[string]boardPad, tracks []specctra.Track, vias []widenVia, size map[string]finishSize, tried map[string]bool) ([]finishJoin, map[string]string) {
	trackOf := map[string][]specctra.Track{}
	for _, t := range tracks {
		trackOf[t.Net] = append(trackOf[t.Net], t)
	}
	viaOf := map[string][]widenVia{}
	for _, v := range vias {
		viaOf[v.Net] = append(viaOf[v.Net], v)
	}
	type netState struct {
		comps []finishComp
		set   []int
		main  int
	}
	state := map[string]*netState{}
	type cand struct {
		net  string
		comp int
		est  float64
	}
	var cands []cand
	padDist := func(a, b []boardPad) float64 {
		d := math.Inf(1)
		for _, p := range a {
			for _, q := range b {
				d = math.Min(d, math.Hypot(p.X-q.X, p.Y-q.Y))
			}
		}
		return d
	}
	for _, n := range nets {
		cs := finishComponents(groups[n], padByID, trackOf[n], viaOf[n])
		if len(cs) < 2 {
			continue
		}
		st := &netState{comps: cs, set: make([]int, len(cs))}
		for i := range cs {
			st.set[i] = i
			if len(cs[i].pads) > len(cs[st.main].pads) {
				st.main = i
			}
		}
		state[n] = st
		for i := range cs {
			if i == st.main {
				continue
			}
			var others []boardPad
			for k := range cs {
				if k != i {
					others = append(others, cs[k].pads...)
				}
			}
			cands = append(cands, cand{n, i, padDist(cs[i].pads, others)})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].est != cands[j].est {
			return cands[i].est < cands[j].est
		}
		if cands[i].net != cands[j].net {
			return cands[i].net < cands[j].net
		}
		return cands[i].comp < cands[j].comp
	})
	var out []finishJoin
	fails := map[string]string{}
	for _, c := range cands {
		st := state[c.net]
		find := func(i int) int {
			for st.set[i] != i {
				i = st.set[i]
			}
			return i
		}
		root := find(c.comp)
		if root == find(st.main) {
			continue // joined on the way
		}
		var src, dst []finishObj
		var srcPads, ends []boardPad
		for k, cp := range st.comps {
			if find(k) == root {
				src = append(src, cp.objs...)
				srcPads = append(srcPads, cp.pads...)
				continue
			}
			for _, o := range cp.objs {
				o.tag = k
				dst = append(dst, o)
			}
			ends = append(ends, cp.pads...)
		}
		ends = append(ends, srcPads...)
		ids := make([]string, 0, len(srcPads))
		for _, p := range srcPads {
			ids = append(ids, p.ID)
		}
		sort.Strings(ids)
		key := c.net + ":" + strings.Join(ids, ",")
		if tried[key] {
			fails[key] = "failed its proof in an earlier pass"
			continue
		}
		sz, ok := size[c.net]
		if !ok || sz.Track <= 0 || sz.Dia <= 0 {
			fails[key] = "no track or via size for the net"
			continue
		}
		if f.nodes >= finishTotalNodes {
			fails[key] = "total search budget spent"
			continue
		}
		maxCost := 2*c.est + 400
		path, tag, why := f.search(c.net, sz, src, dst, ends, maxCost, min(finishJoinNodes, finishTotalNodes-f.nodes))
		if why != "" {
			fails[key] = why
			continue
		}
		// The escape allowance belongs to the two groups joined: the path is
		// rebuilt and checked against their pads alone.
		joined := append([]boardPad(nil), srcPads...)
		for k := range st.comps {
			if find(k) == find(tag) {
				joined = append(joined, st.comps[k].pads...)
			}
		}
		j, ok := f.joinOf(c.net, sz, path, joined)
		if !ok {
			fails[key] = "path failed the exact clearance check"
			continue
		}
		j.Pads = ids
		for k := range st.comps {
			if find(k) == find(tag) {
				for _, p := range st.comps[k].pads {
					j.To = append(j.To, p.ID)
				}
			}
		}
		sort.Strings(j.To)
		f.addJoin(j)
		out = append(out, j)
		st.set[root] = find(tag)
	}
	return out, fails
}

// finishHoleBox is the box around a footprint hole (no net, every layer).
func finishHoleBox(h boardFootprintHole) ([2]float64, [2]float64, bool) {
	if h.Shape == "circle" && h.Dia > 0 {
		return [2]float64{h.X - h.Dia/2, h.Y - h.Dia/2}, [2]float64{h.X + h.Dia/2, h.Y + h.Dia/2}, true
	}
	if len(h.Points) == 0 {
		return [2]float64{}, [2]float64{}, false
	}
	lo, hi := h.Points[0], h.Points[0]
	for _, p := range h.Points[1:] {
		lo = [2]float64{math.Min(lo[0], p[0]), math.Min(lo[1], p[1])}
		hi = [2]float64{math.Max(hi[0], p[0]), math.Max(hi[1], p[1])}
	}
	return lo, hi, true
}

// finishOps is the bridge edit of a set of joins.
func finishOps(js []finishJoin) map[string]any {
	av, at := []map[string]any{}, []map[string]any{}
	for _, j := range js {
		for _, v := range j.Vias {
			av = append(av, map[string]any{"net": v.Net, "x": v.X, "y": v.Y, "diameter": v.DiameterMil, "drill": j.Drill})
		}
		for _, t := range j.Tracks {
			at = append(at, map[string]any{"net": t.Net, "layer": t.Layer, "startX": t.X1, "startY": t.Y1, "endX": t.X2, "endY": t.Y2, "lineWidth": t.Width})
		}
	}
	return map[string]any{"addVias": av, "addTracks": at}
}

// finishExcluded says why a net is not the finisher's to join ("" = it is).
func finishExcluded(in *designIntent, net string) string {
	if in == nil {
		return ""
	}
	n := in.Nets[net]
	if n == nil {
		return ""
	}
	switch {
	case n.DiffPair != "":
		return "differential pair"
	case n.ImpedanceOhm > 0:
		return "controlled impedance"
	case n.LengthGroup != "" || n.MaxSkewMil > 0 || n.LengthTolMil > 0:
		return "length matched"
	case n.MaxVias > 0:
		return "via count limited"
	case n.Interface != "":
		return "high-speed interface " + n.Interface
	}
	return ""
}

// finishSignals joins the signal nets the router left in pieces, in up to
// finishMaxPasses passes, each proven like the stitch passes. It never
// returns a board it did not check.
func (r *kicadRun) finishSignals(board string, pours []kicad.Pour) (string, error) {
	rep := map[string]any{"pourLayers": r.o.finishPourLayers}
	r.summary["finishSignals"] = rep
	cannot := func(why string) {
		fmt.Fprintf(r.stderr, "finish: cannot check: %s\n", why)
		rep["error"] = why
		r.stepGates = append(r.stepGates, gateResult{Gate: "finish-signals", Detail: "cannot check: " + why, CannotCheck: true})
	}
	rb, err := r.kt.RulesRead(board)
	if err != nil {
		cannot("KiCad's class readback failed: " + err.Error())
		return board, nil
	}
	poured := map[string]bool{}
	pourLayer := map[int]bool{}
	for _, p := range pours {
		poured[p.Net] = true
		pourLayer[p.Layer] = true
	}
	s0, _, err := r.snapshot(board)
	if err != nil || s0.Copper == nil {
		cannot(fmt.Sprintf("board snapshot: %v", err))
		return board, nil
	}
	for _, z := range s0.Copper.Pours { // zones already on the board count too
		if m, ok := z.(map[string]any); ok {
			if n, _ := m["net"].(string); n != "" {
				poured[n] = true
				if l, ok := m["layer"].(float64); ok {
					pourLayer[int(l)] = true
				}
			}
		}
	}
	const nmPerMil = 25400.0
	size := map[string]finishSize{}
	clr := map[string]float64{}
	for n, e := range rb.Nets {
		c := float64(e.Clearance)/nmPerMil + kicad.ClearanceMarginMil
		clr[n] = c
		size[n] = finishSize{Track: float64(e.Track) / nmPerMil, Clr: c, Dia: float64(e.ViaDiameter) / nmPerMil, Drill: float64(e.ViaDrill) / nmPerMil}
	}
	s, raw, err := r.snapshot(board)
	if err != nil || s.Outline == nil {
		cannot(fmt.Sprintf("board snapshot: %v", err))
		return board, nil
	}
	defClr := clearanceOf(s) + kicad.ClearanceMarginMil
	maxClr := defClr
	for _, c := range clr {
		maxClr = math.Max(maxClr, c)
	}
	edge := 20.0
	if s.Rules != nil && s.Rules.CopperToEdgeMil > edge {
		edge = s.Rules.CopperToEdgeMil
	}
	b := s.Outline.BBox
	bounds := layoutBBox{MinX: b.MinX + edge, MinY: b.MinY + edge, MaxX: b.MaxX - edge, MaxY: b.MaxY - edge}
	layers := []int{1, 2}
	for i := 0; i < s.CopperLayers-2; i++ {
		layers = append(layers, 15+i)
	}
	sort.Ints(layers)
	padCount := map[string]int{}
	for i := range s.Components {
		for _, p := range s.Components[i].Pads {
			padCount[p.Net]++
		}
	}
	var nets, all []string
	excluded := map[string]string{}
	for n, c := range padCount {
		if n == "" || c < 2 || strings.HasPrefix(n, "unconnected") {
			continue
		}
		all = append(all, n)
		if poured[n] {
			continue
		}
		if why := finishExcluded(r.in, n); why != "" {
			excluded[n] = why
			continue
		}
		nets = append(nets, n)
	}
	sort.Strings(nets)
	sort.Strings(all)
	rep["excluded"] = excluded
	var isoIntent *pcbauto.Intent
	if len(r.in.Pairs) > 0 {
		ib, err := os.ReadFile(r.o.intent)
		if err == nil {
			isoIntent, err = pcbauto.ParseIntent(ib)
		}
		if err != nil {
			cannot("intent for the insulation check: " + err.Error())
			return board, nil
		}
	}
	isoOf := func(raw []byte) ([]pcbauto.IsoFinding, error) {
		if isoIntent == nil {
			return nil, nil
		}
		chk, err := pcbauto.CheckIsolationSnapshot(raw, isoIntent)
		if err != nil {
			return nil, err
		}
		return chk.Findings, nil
	}
	isoBase, err := isoOf(raw)
	if err != nil {
		cannot("insulation check before finishing: " + err.Error())
		return board, nil
	}
	base, err := r.kt.DRC(board, filepath.Join(r.work, fmt.Sprintf("%02d-finish-base-drc.json", r.step)))
	if err != nil {
		cannot("DRC before finishing: " + err.Error())
		return board, nil
	}
	tried := map[string]bool{}
	var kept []finishJoin
	var passes []map[string]any
	stop := fmt.Sprintf("%d passes", finishMaxPasses)
	cur := board
	nodes := 0
	for pass := 1; pass <= finishMaxPasses; pass++ {
		groups, err := r.kt.PadGroups(cur, all)
		if err != nil {
			cannot("pad groups: " + err.Error())
			return cur, nil
		}
		var split []string
		for _, n := range nets {
			if len(groups[n]) > 1 {
				split = append(split, n)
			}
		}
		info := map[string]any{"pass": pass, "splitNets": len(split)}
		passes = append(passes, info)
		if len(split) == 0 {
			stop = "no signal net in pieces"
			break
		}
		s, _, err := r.snapshot(cur)
		if err != nil {
			cannot(fmt.Sprintf("board snapshot: %v", err))
			return cur, nil
		}
		tracks, vias, pads, err := copperOf(s)
		if err != nil {
			cannot("board copper: " + err.Error())
			return cur, nil
		}
		// One search budget for all passes.
		f := &finisher{idx: newFinishIndex(), clr: clr, defClr: defClr, maxClr: maxClr, bounds: bounds, layers: layers, pour: pourLayer, pourMode: r.o.finishPourLayers, nodes: nodes}
		padByID := map[string]boardPad{}
		for _, p := range pads {
			padByID[p.ID] = p
			f.idx.add(padObj(p))
		}
		for _, t := range tracks {
			f.idx.add(finishObj{net: t.Net, layer: t.Layer, a: [2]float64{t.X1, t.Y1}, b: [2]float64{t.X2, t.Y2}, half: t.Width / 2})
		}
		for _, v := range vias {
			q := [2]float64{v.X, v.Y}
			f.idx.add(finishObj{net: v.Net, layer: pcbLayerMulti, a: q, b: q, half: v.Diameter / 2})
		}
		for _, h := range s.FootprintHoles {
			if lo, hi, ok := finishHoleBox(h); ok {
				f.idx.add(finishObj{net: "\x00hole", layer: pcbLayerMulti, pad: true, a: lo, b: hi})
			}
		}
		plan, fails := f.plan(split, groups, padByID, tracks, vias, size, tried)
		info["nodes"] = f.nodes - nodes
		nodes = f.nodes
		reasons := map[string]int{}
		for _, why := range fails {
			reasons[why]++
		}
		info["planned"], info["failed"] = len(plan), reasons
		if len(plan) == 0 {
			stop = "no join found"
			break
		}
		for _, j := range plan {
			tried[j.key()] = true
		}
		// Each round drops what failed and checks the rest again: taking one
		// out can change another's result. A pass that cannot settle changes
		// nothing.
		good := plan
		next := ""
		settled := false
		for round := 0; round < finishMaxRounds && len(good) > 0; round++ {
			next = r.next("finish")
			if _, err := r.kt.Edit(cur, finishOps(good), next); err != nil {
				cannot("finish edit: " + err.Error())
				return cur, nil
			}
			held, again, away, err := r.finishProve(next, good, all, base, groups, isoBase, isoOf, pass)
			if err != nil {
				cannot(err.Error())
				return cur, nil
			}
			info["newErrorsAwayFromJoins"] = away
			if len(again) == 0 {
				settled = true
				break
			}
			info[fmt.Sprintf("dropped%d", round+1)] = again
			if _, away := again["board"]; away && len(held) == len(good) {
				break
			}
			good = held
		}
		if !settled || len(good) == 0 {
			stop = "a pass did not settle after its failures came out"
			break
		}
		cur = next
		kept = append(kept, good...)
		info["kept"] = len(good)
	}
	var length float64
	vias := 0
	byLayer := map[int]float64{}
	r.finishedNets = map[string]bool{}
	for _, j := range kept {
		r.finishedNets[j.Net] = true
		length += j.LengthMil
		vias += len(j.Vias)
		for _, t := range j.Tracks {
			byLayer[t.Layer] += math.Hypot(t.X2-t.X1, t.Y2-t.Y1)
		}
	}
	rep["joined"], rep["passes"], rep["stop"], rep["nodes"] = len(kept), passes, stop, nodes
	rep["lengthMil"], rep["vias"], rep["lengthMilByLayer"] = math.Round(length), vias, byLayer
	rep["joins"] = kept
	fmt.Fprintf(r.stderr, "finish: %d signal join(s) kept, %.0f mil, %d via(s), stopped: %s\n", len(kept), length, vias, stop)
	return cur, nil
}

// finishErr is a new DRC or insulation error: where it is and the nets its
// items name.
type finishErr struct {
	what string
	at   [][2]float64
	nets []string
	// copperOnly: every item is a track, via or pad (no zone): with none of
	// them a join's net, none is the join's copper, and their geometry did
	// not change.
	copperOnly bool
}

// finishGeomRules are the DRC rules whose result between two tracks, vias or
// pads depends on their geometry alone.
var finishGeomRules = map[string]bool{"clearance": true, "hole_clearance": true, "hole_to_hole": true, "copper_edge_clearance": true, "solder_mask_bridge": true}

var (
	reItemNet    = regexp.MustCompile(`\[([^\]]+)\]`)
	reCopperItem = regexp.MustCompile(`^(Track|Via|Arc|(PTH |SMD |NPTH )?[Pp]ad) `)
)

// finishProve checks a filled board (finishJudge decides).
func (r *kicadRun) finishProve(board string, plan []finishJoin, nets []string, base *kicad.DRCReport, before map[string][]kicad.PadGroup, isoBase []pcbauto.IsoFinding, isoOf func([]byte) ([]pcbauto.IsoFinding, error), pass int) ([]finishJoin, map[string]string, []string, error) {
	rep, err := r.kt.DRC(board, filepath.Join(r.work, fmt.Sprintf("%02d-finish-drc%d.json", r.step, pass)))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("DRC after finishing: %w", err)
	}
	groups, err := r.kt.PadGroups(board, nets)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("pad groups after finishing: %w", err)
	}
	s, raw, err := r.snapshot(board)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("board snapshot after finishing: %w", err)
	}
	iso, err := isoOf(raw)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("insulation check after finishing: %w", err)
	}
	var errs []finishErr
	for _, v := range newViolations(base, rep) {
		// Only rules of fixed geometry: a refill can change what is connected.
		e := finishErr{what: "new DRC " + v.Rule, copperOnly: len(v.Items) > 0 && finishGeomRules[v.Rule]}
		if v.Net != "" {
			e.nets = append(e.nets, v.Net)
		}
		for _, it := range v.Items {
			e.at = append(e.at, [2]float64{it.X, it.Y})
			if !reCopperItem.MatchString(it.Description) {
				e.copperOnly = false
			}
			for _, m := range reItemNet.FindAllStringSubmatch(it.Description, -1) {
				e.nets = append(e.nets, m[1])
			}
		}
		if v.X != nil && v.Y != nil {
			e.at = append(e.at, [2]float64{*v.X, *v.Y})
		}
		errs = append(errs, e)
	}
	for _, f := range newIsoFindings(isoBase, iso) {
		errs = append(errs, finishErr{what: "new " + f.Kind, at: [][2]float64{{f.At.X, f.At.Y}}, nets: []string{f.NetA, f.NetB}})
	}
	padAt := map[string][2]float64{}
	for i := range s.Components {
		for _, p := range s.Components[i].Pads {
			padAt[p.ID] = [2]float64{p.X, p.Y}
		}
	}
	good, reasons, away := finishJudge(plan, before, groups, errs, padAt)
	return good, reasons, away, nil
}

// newIsoFindings are the findings of cur beyond those of base, compared as a
// multiset of exact fingerprints (kind, items, nets, layer, position).
func newIsoFindings(base, cur []pcbauto.IsoFinding) []pcbauto.IsoFinding {
	key := func(f pcbauto.IsoFinding) string {
		return fmt.Sprintf("%s|%s|%s|%s|%s|%d|%g|%g|%g|%g|%g", f.Kind, f.ItemA, f.ItemB, f.NetA, f.NetB, f.Layer, f.At.X, f.At.Y, f.GapMil, f.PathMil, f.RequiredMil)
	}
	seen := map[string]int{}
	for _, f := range base {
		seen[key(f)]++
	}
	var out []pcbauto.IsoFinding
	for _, f := range cur {
		if k := key(f); seen[k] > 0 {
			seen[k]--
		} else {
			out = append(out, f)
		}
	}
	return out
}

// finishJudge decides which joins hold: each must have merged its own two
// groups. A new error belongs to the join within finishNearMil of it, else to
// the nearest join of a net its items name, else to a join within
// finishFarMil. One none of these reach is returned as away; it fails the
// pass ("board") unless it lies between other nets' tracks, vias and pads
// alone, which no added copper changes (KiCad reports an old error again). A
// pad cut off its group fails the nearest join within stitchCutMil; with none,
// the pass fails.
func finishJudge(plan []finishJoin, before, after map[string][]kicad.PadGroup, errs []finishErr, padAt map[string][2]float64) ([]finishJoin, map[string]string, []string) {
	reasons := map[string]string{}
	bad := map[int]string{}
	nearest := func(q [2]float64, within float64, net string) int {
		best, bi := within, -1
		for i := range plan {
			if net != "" && plan[i].Net != net {
				continue
			}
			if d := plan[i].near(q); d <= best {
				best, bi = d, i
			}
		}
		return bi
	}
	blame := func(i int, why string) {
		if _, ok := bad[i]; !ok {
			bad[i] = why
		}
	}
	var away []string
	for _, e := range errs {
		k := -1
		for _, q := range e.at {
			if k = nearest(q, finishNearMil, ""); k >= 0 {
				break
			}
		}
		if k < 0 {
		names:
			for _, n := range e.nets {
				for i := range plan {
					if plan[i].Net != n {
						continue
					}
					k = i
					for _, q := range e.at {
						if m := nearest(q, math.Inf(1), n); m >= 0 {
							k = m
							break
						}
					}
					break names
				}
			}
		}
		if k < 0 {
			for _, q := range e.at {
				if k = nearest(q, finishFarMil, ""); k >= 0 {
					break
				}
			}
		}
		if k < 0 {
			if !e.copperOnly {
				reasons["board"] = e.what + " near no join"
			}
			away = append(away, e.what)
			continue
		}
		blame(k, e.what)
	}
	for _, id := range sortedKeysOf(padsSplit(before, after)) {
		k := -1
		if q, ok := padAt[id]; ok {
			k = nearest(q, stitchCutMil, "")
		}
		if k < 0 {
			reasons["board"] = "pad " + id + " was cut off with no join near it"
			continue
		}
		blame(k, "near a pad cut off its group")
	}
	var good []finishJoin
	for i, j := range plan {
		where := map[string]int{}
		for gi, g := range after[j.Net] {
			for _, id := range g.Pads {
				where[id] = gi
			}
		}
		joined := false
		if w, ok := where[j.Pads[0]]; ok {
			for _, id := range j.To {
				if v, ok := where[id]; ok && v == w {
					joined = true
					break
				}
			}
		}
		why := bad[i]
		if !joined {
			why = "groups still apart"
		}
		if why != "" {
			reasons[j.key()] = why
			continue
		}
		good = append(good, j)
	}
	return good, reasons, away
}
