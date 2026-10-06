package dsn

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// Read parses a DSN design into bb (nets, pads, fixed copper, keep-outs) and
// rb (layers, classes, rule scopes, vias). Constructs it does not support are
// skipped; ReadWarnings lists them.
func Read(src []byte, bb board.Builder, rb rules.Builder) error {
	_, err := ReadWarnings(src, bb, rb)
	return err
}

// ReadWarnings is Read that also returns one line per skipped construct
// (spec 04 §5: unknown clearance types and the like are warned, never fatal).
func ReadWarnings(src []byte, bb board.Builder, rb rules.Builder) ([]string, error) {
	root, err := specctra.ParseExpr(string(src))
	if err != nil {
		return nil, fmt.Errorf("dsn: %w", err)
	}
	if h := strings.ToLower(root.Head()); h != "pcb" {
		return nil, fmt.Errorf("dsn: not a Specctra design: top level is (%s ...)", root.Head())
	}
	r := &reader{bb: bb, rb: rb, nets: map[string]geom.NetID{}, layerIdx: map[string]geom.LayerID{},
		padstacks: map[string]*specctra.Expr{}, pinNet: map[string]geom.NetID{}}
	if err := r.read(root); err != nil {
		return r.warns, fmt.Errorf("dsn: %w", err)
	}
	return r.warns, nil
}

// reader is the state of one Read. Everything is emitted in file order, so
// the same file always gives the same builder calls.
type reader struct {
	bb        board.Builder
	rb        rules.Builder
	scale     float64 // nm per DSN unit
	layers    []rules.Layer
	layerIdx  map[string]geom.LayerID
	nets      map[string]geom.NetID
	padstacks map[string]*specctra.Expr
	pinNet    map[string]geom.NetID // "comp-pin" → net
	warns     []string
}

func (r *reader) warnf(format string, a ...any) { r.warns = append(r.warns, fmt.Sprintf(format, a...)) }

func (r *reader) read(root *specctra.Expr) error {
	unit := "mil" // Specctra's default unit
	if res := root.Child("resolution"); res != nil && len(res.Atoms()) > 0 {
		unit = res.Atoms()[0]
	}
	if u := root.Child("unit"); u != nil && len(u.Atoms()) > 0 {
		unit = u.Atoms()[0]
	}
	var ok bool
	if r.scale, ok = nmPerUnit(unit); !ok {
		return fmt.Errorf("unsupported unit %q", unit)
	}
	st := root.Child("structure")
	if st == nil {
		return fmt.Errorf("no (structure) section")
	}
	r.readLayers(st)
	if lib := root.Child("library"); lib != nil {
		for _, ps := range lib.Children("padstack") {
			if a := ps.Atoms(); len(a) > 0 {
				r.padstacks[a[0]] = ps
			}
		}
	}
	network := root.Child("network")
	if network != nil {
		for _, n := range network.Children("net") {
			a := n.Atoms()
			if len(a) == 0 {
				continue
			}
			id := r.net(a[0])
			if pins := n.Child("pins"); pins != nil {
				for _, p := range pins.Atoms() {
					r.pinNet[p] = id
				}
			}
		}
	}
	if err := r.readStructure(st); err != nil {
		return err
	}
	if err := r.readPlacement(root); err != nil {
		return err
	}
	if w := root.Child("wiring"); w != nil {
		if err := r.readWiring(w); err != nil {
			return err
		}
	}
	return r.readRules(st, network)
}

// nmPerUnit converts a Specctra unit name to nanometres.
func nmPerUnit(u string) (float64, bool) {
	switch strings.ToLower(u) {
	case "mil":
		return 25400, true
	case "inch":
		return 25400000, true
	case "mm":
		return 1e6, true
	case "cm":
		return 1e7, true
	case "um":
		return 1000, true
	}
	return 0, false
}

// num parses a DSN number in design units.
func num(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("bad number %q", s)
	}
	return v, nil
}

// nm converts a length in design units to nanometres.
func (r *reader) nm(v float64) int64 { return int64(math.Round(v * r.scale)) }

func (r *reader) net(name string) geom.NetID {
	if id, ok := r.nets[name]; ok {
		return id
	}
	id := r.bb.AddNet(name)
	r.nets[name] = id
	return id
}

// readLayers numbers the copper layers in file order (Specctra stack order).
// The DSN carries no copper weight, so outer layers get 35 µm and inner ones
// 17.5 µm, the spec 04 §2 example values; MinWidth is left 0 (unknown).
func (r *reader) readLayers(st *specctra.Expr) {
	ls := st.Children("layer")
	for i, l := range ls {
		a := l.Atoms()
		if len(a) == 0 {
			continue
		}
		kind := rules.Signal
		if t := l.Child("type"); t != nil && len(t.Atoms()) > 0 {
			switch t.Atoms()[0] {
			case "power":
				kind = rules.Power
			case "mixed":
				kind = rules.Mixed
			}
		}
		outer := i == 0 || i == len(ls)-1
		cu := 17.5
		if outer {
			cu = 35
		}
		lay := rules.Layer{ID: geom.LayerID(len(r.layers)), Name: a[0], Kind: kind, Outer: outer, CopperUm: cu}
		r.layerIdx[a[0]] = lay.ID
		r.layers = append(r.layers, lay)
		r.rb.AddLayer(lay)
	}
}

// layersOf expands a layer name of a shape: "signal" and "pcb" mean every
// copper layer, "power" every power layer. Unknown names give nil.
func (r *reader) layersOf(name string) []geom.LayerID {
	var out []geom.LayerID
	switch name {
	case "signal", "pcb":
		for _, l := range r.layers {
			out = append(out, l.ID)
		}
	case "power":
		for _, l := range r.layers {
			if l.Kind == rules.Power {
				out = append(out, l.ID)
			}
		}
	default:
		if id, ok := r.layerIdx[name]; ok {
			out = append(out, id)
		}
	}
	return out
}

// areaLayer is the single LayerID of a keep-out, edge or zone: AllLayers for
// the all-layer names. ok is false for an unknown layer.
func (r *reader) areaLayer(name string) (geom.LayerID, bool) {
	switch name {
	case "signal", "pcb":
		return geom.AllLayers, true
	}
	id, ok := r.layerIdx[name]
	return id, ok
}

// xform maps design-unit coordinates: optional mirror in x, rotation, then a
// translation. Rotation is counter-clockwise in degrees; multiples of 90° use
// exact sines so placed pads stay on the integer grid.
type xform struct {
	ox, oy, c, s float64
	mirror       bool
}

func newXform(x, y, deg float64, mirror bool) xform {
	c, s := math.Cos(deg*math.Pi/180), math.Sin(deg*math.Pi/180)
	if q := deg / 90; q == math.Trunc(q) {
		k := ((int(q) % 4) + 4) % 4
		c, s = [4]float64{1, 0, -1, 0}[k], [4]float64{0, 1, 0, -1}[k]
	}
	return xform{ox: x, oy: y, c: c, s: s, mirror: mirror}
}

func (t xform) apply(x, y float64) (float64, float64) {
	if t.mirror {
		x = -x
	}
	return t.ox + t.c*x - t.s*y, t.oy + t.s*x + t.c*y
}

// shape converts one shape descriptor. f maps design-unit points to nm. A
// path becomes one capsule per segment; a polygon's aperture width is ignored.
func (r *reader) shape(e *specctra.Expr, f func(x, y float64) geom.Pt) (string, []geom.Shape, error) {
	a := e.Atoms()
	if len(a) == 0 {
		return "", nil, fmt.Errorf("(%s) without layer", e.Head())
	}
	vals := make([]float64, len(a)-1)
	for i, s := range a[1:] {
		v, err := num(s)
		if err != nil {
			return "", nil, fmt.Errorf("(%s %s ...): %w", e.Head(), a[0], err)
		}
		vals[i] = v
	}
	pts := func(v []float64) []geom.Pt {
		out := make([]geom.Pt, 0, len(v)/2)
		for i := 0; i+1 < len(v); i += 2 {
			out = append(out, f(v[i], v[i+1]))
		}
		return out
	}
	switch e.Head() {
	case "circle":
		if len(vals) < 1 {
			return "", nil, fmt.Errorf("(circle %s) without diameter", a[0])
		}
		var x, y float64
		if len(vals) >= 3 {
			x, y = vals[1], vals[2]
		}
		return a[0], []geom.Shape{geom.Circle{C: f(x, y), R: r.nm(vals[0]) / 2}}, nil
	case "rect":
		if len(vals) != 4 {
			return "", nil, fmt.Errorf("(rect %s) needs 4 numbers", a[0])
		}
		c := pts([]float64{vals[0], vals[1], vals[2], vals[1], vals[2], vals[3], vals[0], vals[3]})
		if c[0].X == c[1].X || c[0].Y == c[1].Y { // still axis-aligned
			// geom.Rect is half-open; the pad copper is closed.
			return a[0], []geom.Shape{geom.Rect{
				MinX: min(c[0].X, c[2].X), MinY: min(c[0].Y, c[2].Y),
				MaxX: max(c[0].X, c[2].X) + 1, MaxY: max(c[0].Y, c[2].Y) + 1}}, nil
		}
		return a[0], []geom.Shape{geom.Poly{Pts: c}}, nil
	case "polygon":
		if len(vals) < 7 || (len(vals)-1)%2 != 0 {
			return "", nil, fmt.Errorf("(polygon %s) malformed", a[0])
		}
		return a[0], []geom.Shape{geom.Poly{Pts: cleanRing(pts(vals[1:]))}}, nil
	case "path":
		if len(vals) < 3 || (len(vals)-1)%2 != 0 {
			return "", nil, fmt.Errorf("(path %s) malformed", a[0])
		}
		hw := r.nm(vals[0]) / 2
		p := pts(vals[1:])
		if len(p) == 1 {
			return a[0], []geom.Shape{geom.Circle{C: p[0], R: hw}}, nil
		}
		var out []geom.Shape
		for i := 0; i+1 < len(p); i++ {
			if p[i] != p[i+1] {
				out = append(out, geom.Seg{A: p[i], B: p[i+1], HalfW: hw})
			}
		}
		return a[0], out, nil
	}
	return a[0], nil, errUnsupportedShape
}

var errUnsupportedShape = errors.New("unsupported shape")

// cleanRing drops repeated consecutive vertices and the closing vertex.
func cleanRing(p []geom.Pt) []geom.Pt {
	out := p[:0:0]
	for _, q := range p {
		if len(out) == 0 || out[len(out)-1] != q {
			out = append(out, q)
		}
	}
	for len(out) > 1 && out[0] == out[len(out)-1] {
		out = out[:len(out)-1]
	}
	return out
}

// firstList returns the first sub-list after the head.
func firstList(e *specctra.Expr) *specctra.Expr {
	for _, c := range e.List[1:] {
		if c.IsList {
			return c
		}
	}
	return nil
}

func (r *reader) absolute(x, y float64) geom.Pt { return geom.Pt{X: r.nm(x), Y: r.nm(y)} }

// readStructure loads the board outline (Edge), planes (Zone) and keep-outs.
func (r *reader) readStructure(st *specctra.Expr) error {
	for _, c := range st.List[1:] {
		if !c.IsList {
			continue
		}
		switch c.Head() {
		case "boundary":
			if err := r.area(c, func(l geom.LayerID, s geom.Shape) {
				r.bb.AddItem(board.Item{Kind: board.Edge, From: l, To: l, Shape: s, Fixed: true})
			}); err != nil {
				return err
			}
		case "plane":
			a := c.Atoms()
			if len(a) == 0 {
				return fmt.Errorf("(plane) without net")
			}
			net := r.net(a[0])
			if err := r.area(c, func(l geom.LayerID, s geom.Shape) {
				r.bb.AddItem(board.Item{Kind: board.Zone, Net: net, From: l, To: l, Shape: s, Fixed: true})
			}); err != nil {
				return err
			}
		case "keepout", "via_keepout", "wire_keepout":
			if err := r.keepout(c, func(x, y float64) geom.Pt { return r.absolute(x, y) }); err != nil {
				return err
			}
		}
	}
	return nil
}

// area reads the shape of a boundary, plane or keep-out in board coordinates
// and calls add once per resulting shape. A boundary or area path traces the
// outline, so it becomes one polygon, not a chain of capsules.
func (r *reader) area(c *specctra.Expr, add func(geom.LayerID, geom.Shape)) error {
	return r.areaIn(c, func(x, y float64) geom.Pt { return r.absolute(x, y) }, add)
}

func (r *reader) areaIn(c *specctra.Expr, f func(x, y float64) geom.Pt, add func(geom.LayerID, geom.Shape)) error {
	sh := firstList(c)
	if sh == nil {
		return fmt.Errorf("(%s) without shape", c.Head())
	}
	layer, shapes, err := r.shape(sh, f)
	if err == errUnsupportedShape {
		r.warnf("(%s): shape (%s) not supported, skipped", c.Head(), sh.Head())
		return nil
	}
	if err != nil {
		return fmt.Errorf("(%s): %w", c.Head(), err)
	}
	l, ok := r.areaLayer(layer)
	if !ok {
		r.warnf("(%s): unknown layer %q, skipped", c.Head(), layer)
		return nil
	}
	if sh.Head() == "path" {
		var p []geom.Pt
		for _, s := range shapes {
			if sg, ok := s.(geom.Seg); ok {
				if len(p) == 0 {
					p = append(p, sg.A)
				}
				p = append(p, sg.B)
			}
		}
		if len(p) > 0 {
			shapes = []geom.Shape{geom.Poly{Pts: cleanRing(p)}}
		}
	}
	for _, s := range shapes {
		add(l, s)
	}
	return nil
}

// keepout records a keep-out in the rules (with its kind) and, when it forbids
// all copper, as a board Keepout item so spatial queries meet it.
func (r *reader) keepout(c *specctra.Expr, f func(x, y float64) geom.Pt) error {
	kind := map[string]rules.KeepoutKind{"keepout": rules.KeepoutAll, "via_keepout": rules.KeepoutVia,
		"wire_keepout": rules.KeepoutWire}[c.Head()]
	return r.areaIn(c, f, func(l geom.LayerID, s geom.Shape) {
		r.rb.AddKeepout(rules.Keepout{Shape: s, Layer: l, Kind: kind})
		if kind == rules.KeepoutAll {
			r.bb.AddItem(board.Item{Kind: board.Keepout, From: l, To: l, Shape: s, Fixed: true})
		}
	})
}

// readPlacement places every component's image pins as Pad items.
func (r *reader) readPlacement(root *specctra.Expr) error {
	images := map[string]*specctra.Expr{}
	if lib := root.Child("library"); lib != nil {
		for _, im := range lib.Children("image") {
			if a := im.Atoms(); len(a) > 0 {
				images[a[0]] = im
			}
		}
	}
	pl := root.Child("placement")
	if pl == nil {
		return nil
	}
	for _, comp := range pl.Children("component") {
		a := comp.Atoms()
		if len(a) == 0 {
			continue
		}
		im := images[a[0]]
		if im == nil {
			return fmt.Errorf("component image %q not in library", a[0])
		}
		for _, p := range comp.Children("place") {
			if err := r.place(p, im); err != nil {
				return err
			}
		}
	}
	return nil
}

// place emits the pads and image keep-outs of one placed component.
func (r *reader) place(p, im *specctra.Expr) error {
	a := p.Atoms()
	if len(a) < 3 {
		r.warnf("component %v not placed, skipped", a)
		return nil
	}
	x, err1 := num(a[1])
	y, err2 := num(a[2])
	if err1 != nil || err2 != nil {
		return fmt.Errorf("place %s: bad coordinate", a[0])
	}
	back := len(a) > 3 && a[3] == "back"
	var rot float64
	if len(a) > 4 {
		if rot, err1 = num(a[4]); err1 != nil {
			return fmt.Errorf("place %s: %w", a[0], err1)
		}
	}
	comp := newXform(x, y, rot, back)
	for _, c := range im.List[1:] {
		if !c.IsList {
			continue
		}
		switch c.Head() {
		case "pin":
			if err := r.pin(a[0], c, comp, back); err != nil {
				return err
			}
		case "keepout", "via_keepout", "wire_keepout":
			if err := r.keepout(c, func(px, py float64) geom.Pt { return r.absolute(comp.apply(px, py)) }); err != nil {
				return err
			}
		}
	}
	return nil
}

// pin emits the Pad items of one pin: one item per shape and per run of
// adjacent layers whose shapes are equal. Every item of the pin has the same
// Ref ("comp-pin"). A back-side component's layers are mirrored.
func (r *reader) pin(compID string, c *specctra.Expr, comp xform, back bool) error {
	a := c.Atoms() // padstack pin_id x y; (rotate r) is a sub-list
	if len(a) < 4 {
		return fmt.Errorf("component %s: malformed (pin)", compID)
	}
	ps := r.padstacks[a[0]]
	if ps == nil {
		return fmt.Errorf("component %s: padstack %q not in library", compID, a[0])
	}
	px, err1 := num(a[2])
	py, err2 := num(a[3])
	if err1 != nil || err2 != nil {
		return fmt.Errorf("component %s pin %s: bad coordinate", compID, a[1])
	}
	var prot float64
	if rt := c.Child("rotate"); rt != nil && len(rt.Atoms()) > 0 {
		var err error
		if prot, err = num(rt.Atoms()[0]); err != nil {
			return fmt.Errorf("component %s pin %s: %w", compID, a[1], err)
		}
	}
	local := newXform(px, py, prot, false)
	f := func(x, y float64) geom.Pt { return r.absolute(comp.apply(local.apply(x, y))) }
	per := make([][]geom.Shape, len(r.layers))
	for _, sh := range ps.Children("shape") {
		d := firstList(sh)
		if d == nil {
			continue
		}
		layer, shapes, err := r.shape(d, f)
		if err == errUnsupportedShape {
			r.warnf("padstack %s: shape (%s) not supported, skipped", a[0], d.Head())
			continue
		}
		if err != nil {
			return fmt.Errorf("padstack %s: %w", a[0], err)
		}
		for _, l := range r.layersOf(layer) {
			if back {
				l = geom.LayerID(len(r.layers)-1) - l
			}
			per[l] = append(per[l], shapes...)
		}
	}
	ref := compID + "-" + a[1]
	net := r.pinNet[ref]
	for from := 0; from < len(per); {
		to := from
		for to+1 < len(per) && reflect.DeepEqual(per[to+1], per[from]) {
			to++
		}
		for _, s := range per[from] {
			r.bb.AddItem(board.Item{Kind: board.Pad, Net: net, From: geom.LayerID(from), To: geom.LayerID(to),
				Shape: s, Fixed: true, Ref: ref})
		}
		from = to + 1
	}
	return nil
}

// isFixedType reports whether a wiring (type ...) marks user copper the
// router must keep (spec 03 §4.7: protect and fix).
func isFixedType(c *specctra.Expr) bool {
	t := c.Child("type")
	if t == nil || len(t.Atoms()) == 0 {
		return false
	}
	switch t.Atoms()[0] {
	case "fix", "protect":
		return true
	}
	return false
}

func childAtom(c *specctra.Expr, name string) string {
	if n := c.Child(name); n != nil && len(n.Atoms()) > 0 {
		return n.Atoms()[0]
	}
	return ""
}

// readWiring loads pre-routed wires and vias. (type fix) and (type protect)
// copper is Fixed; other wiring is loaded as ordinary routed copper.
func (r *reader) readWiring(w *specctra.Expr) error {
	for _, c := range w.List[1:] {
		if !c.IsList {
			continue
		}
		var net geom.NetID
		if n := childAtom(c, "net"); n != "" {
			net = r.net(n)
		}
		fixed := isFixedType(c)
		switch c.Head() {
		case "wire":
			sh := firstList(c)
			if sh == nil || sh.Head() != "path" {
				r.warnf("wiring: wire without (path), skipped")
				continue
			}
			layer, shapes, err := r.shape(sh, r.absolute)
			if err != nil {
				return fmt.Errorf("wiring: %w", err)
			}
			l, ok := r.layerIdx[layer]
			if !ok {
				r.warnf("wiring: unknown layer %q, wire skipped", layer)
				continue
			}
			for _, s := range shapes {
				if _, isSeg := s.(geom.Seg); !isSeg {
					continue // a one-point path is no track
				}
				r.bb.AddItem(board.Item{Kind: board.Track, Net: net, From: l, To: l, Shape: s, Fixed: fixed})
			}
		case "via":
			a := c.Atoms()
			if len(a) < 3 {
				return fmt.Errorf("wiring: malformed (via)")
			}
			x, err1 := num(a[1])
			y, err2 := num(a[2])
			if err1 != nil || err2 != nil {
				return fmt.Errorf("wiring: via %s: bad coordinate", a[0])
			}
			land, from, to, err := r.viaLand(a[0])
			if err != nil {
				return fmt.Errorf("wiring: %w", err)
			}
			r.bb.AddItem(board.Item{Kind: board.Via, Net: net, From: from, To: to,
				Shape: geom.Circle{C: r.absolute(x, y), R: land}, Fixed: fixed})
		}
	}
	return nil
}

// viaLand returns a via padstack's land radius (the largest circle, or half
// the largest extent of another shape) and its layer span.
func (r *reader) viaLand(name string) (int64, geom.LayerID, geom.LayerID, error) {
	ps := r.padstacks[name]
	if ps == nil {
		return 0, 0, 0, fmt.Errorf("via padstack %q not in library", name)
	}
	var land int64
	from, to := geom.LayerID(math.MaxInt16), geom.LayerID(-1)
	for _, sh := range ps.Children("shape") {
		d := firstList(sh)
		if d == nil {
			continue
		}
		layer, shapes, err := r.shape(d, r.absolute)
		if err == errUnsupportedShape {
			continue
		}
		if err != nil {
			return 0, 0, 0, fmt.Errorf("padstack %s: %w", name, err)
		}
		for _, s := range shapes {
			if c, ok := s.(geom.Circle); ok {
				land = max(land, c.R)
				continue
			}
			b := s.Bounds()
			land = max(land, (b.MaxX-b.MinX)/2, (b.MaxY-b.MinY)/2)
		}
		for _, l := range r.layersOf(layer) {
			from, to = min(from, l), max(to, l)
		}
	}
	if to < 0 {
		return 0, 0, 0, fmt.Errorf("via padstack %q has no copper shape", name)
	}
	return land, from, to, nil
}
