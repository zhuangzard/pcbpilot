// Package pcbauto is an offline, deterministic PCB automation engine: it reads
// a measured board (parts with absolute pads and nets, outline, rules), a
// mechanical specification and optional power budget, and produces
//
//   - an electrical analysis (net voltage/current → IPC width, clearance, vias),
//   - a layer-count and stackup decision with plane/split-plane assignment,
//   - a mechanically constrained, cluster-aware placement,
//   - plane fan-out plus negotiated-congestion multi-layer routing,
//   - an independent geometric DRC of everything it produced.
//
// It has no editor, CLI, daemon or filesystem dependency. Units are mil, y-up.
// Results are proposals: host DRC after applying them remains the arbiter.
package pcbauto

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// EasyEDA copper layer ids. Inner layers start at 15.
const (
	LayerTop    = 1
	LayerBottom = 2
	LayerMulti  = 12 // through-hole pads: present on every copper layer
	LayerInner1 = 15
)

// Pad is a copper pad in absolute board coordinates.
type Pad struct {
	Part   string      `json:"part"`
	Number string      `json:"number"`
	Net    string      `json:"net,omitempty"`
	Layer  int         `json:"layer"` // 1, 2 or 12 (all layers)
	Box    OrientedBox `json:"box"`
	Drill  float64     `json:"drill,omitempty"`

	rel    Point   // centre relative to part anchor, in the part's unrotated frame
	relRot float64 // pad rotation relative to the part
}

// Key identifies a pad as REF.PIN.
func (p *Pad) Key() string { return p.Part + "." + p.Number }

// OnLayer reports whether the pad has copper on copper layer id.
func (p *Pad) OnLayer(id int) bool { return p.Layer == LayerMulti || p.Layer == id }

// Part is a placed footprint.
type Part struct {
	Ref      string  `json:"ref"`
	ID       string  `json:"id,omitempty"`
	Device   string  `json:"device,omitempty"`
	Pos      Point   `json:"pos"` // anchor
	Rotation float64 `json:"rotation"`
	Side     int     `json:"side"` // 1 top, 2 bottom
	Pads     []*Pad  `json:"pads"`
	Fixed    bool    `json:"fixed,omitempty"` // mechanically constrained; placer must not move it
	Height   float64 `json:"height,omitempty"`

	body    Rect // body bounds relative to anchor at rotation 0
	hasBody bool

	// Body() cache. A 636-ball BGA re-derived its bounds from every pad on
	// each call and legalisation spent 90% of its time doing it. The key is
	// the pose plus the first pad's centre and the pad count, so moves and
	// direct pad edits both invalidate it.
	bodyKey   bodyKey
	bodyCache Rect
	// Rigid-body shortcut: bounds relative to the anchor, per rotation. A
	// part is rigid, so a move is a translation of this rect — the spiral
	// search tries hundreds of poses per part.
	relBody map[float64]Rect
}

type bodyKey struct {
	pos, pad0 Point
	rot       float64
	n         int
	hasBody   bool
	valid     bool
}

// Body returns the absolute courtyard of the part (pads ∪ declared body).
func (p *Part) Body() Rect {
	k := bodyKey{pos: p.Pos, rot: p.Rotation, n: len(p.Pads), hasBody: p.hasBody, valid: true}
	if len(p.Pads) > 0 {
		k.pad0 = p.Pads[0].Box.C
	}
	if k == p.bodyKey {
		return p.bodyCache
	}
	var r Rect
	if p.rigid() {
		rel, ok := p.relBody[p.Rotation]
		if !ok {
			rel = p.body0().Translate(Point{-p.Pos.X, -p.Pos.Y})
			if p.relBody == nil {
				p.relBody = map[float64]Rect{}
			}
			p.relBody[p.Rotation] = rel
		}
		r = rel.Translate(p.Pos)
	} else {
		r = p.body0()
	}
	p.bodyKey, p.bodyCache = k, r
	return r
}

// rigid reports whether the pads sit where MoveTo would put them (the part
// was frozen and nobody edited a pad since), so bounds can be translated.
func (p *Part) rigid() bool {
	if len(p.Pads) == 0 {
		return false
	}
	pd := p.Pads[0]
	if pd.rel == (Point{}) && pd.Box.C != p.Pos {
		return false // never frozen
	}
	want := p.Pos.Add(pd.rel.Rotate(p.Rotation))
	return math.Abs(want.X-pd.Box.C.X) < 1e-6 && math.Abs(want.Y-pd.Box.C.Y) < 1e-6
}

func (p *Part) body0() Rect {
	r := EmptyRect()
	for _, pd := range p.Pads {
		r = r.Union(pd.Box.Bounds())
	}
	if p.hasBody {
		for _, c := range p.body.Corners() {
			r = r.AddPoint(p.Pos.Add(c.Rotate(p.Rotation)))
		}
	}
	if r.Empty() {
		r = Rect{p.Pos.X - 10, p.Pos.Y - 10, p.Pos.X + 10, p.Pos.Y + 10}
	}
	return r
}

// SetBody records the body rect (absolute, at the part's current pose).
func (p *Part) SetBody(abs Rect) {
	r := EmptyRect()
	for _, c := range abs.Corners() {
		r = r.AddPoint(c.Sub(p.Pos).Rotate(-p.Rotation))
	}
	p.body, p.hasBody = r, true
	p.bodyKey, p.relBody = bodyKey{}, nil
}

// freeze stores each pad relative to the anchor so MoveTo can re-pose them.
func (p *Part) freeze() {
	p.bodyKey, p.relBody = bodyKey{}, nil
	for _, pd := range p.Pads {
		pd.rel = pd.Box.C.Sub(p.Pos).Rotate(-p.Rotation)
		pd.relRot = pd.Box.Rot - p.Rotation
	}
}

// MoveTo re-poses the part and all of its pads.
func (p *Part) MoveTo(pos Point, rot float64) {
	rot = normDeg(rot)
	p.Pos, p.Rotation = pos, rot
	for _, pd := range p.Pads {
		pd.Box.C = pos.Add(pd.rel.Rotate(rot))
		pd.Box.Rot = normDeg(pd.relRot + rot)
	}
}

// PinCount is the number of pads.
func (p *Part) PinCount() int { return len(p.Pads) }

func normDeg(d float64) float64 {
	for d < 0 {
		d += 360
	}
	for d >= 360 {
		d -= 360
	}
	return d
}

// Keepout forbids copper and/or parts inside a polygon.
type Keepout struct {
	Name     string  `json:"name,omitempty"`
	Poly     []Point `json:"poly"`
	Layers   []int   `json:"layers,omitempty"` // empty = all copper layers
	NoCopper bool    `json:"noCopper"`
	NoParts  bool    `json:"noParts"`
	NoVias   bool    `json:"noVias"`
}

func (k *Keepout) onLayer(id int) bool {
	if len(k.Layers) == 0 {
		return true
	}
	for _, l := range k.Layers {
		if l == id || l == LayerMulti {
			return true
		}
	}
	return false
}

// Hole is a non-plated mechanical hole (mounting hole, slot approximated).
type Hole struct {
	Name string  `json:"name,omitempty"`
	C    Point   `json:"c"`
	Dia  float64 `json:"dia"`
	// Keep is the copper-free annulus radius beyond the drill (screw head).
	Keep float64 `json:"keep,omitempty"`
}

// Rules are fabrication minimums and board defaults, all mil.
type Rules struct {
	Clearance     float64 `json:"clearance"`
	TrackWidth    float64 `json:"trackWidth"`
	MinTrack      float64 `json:"minTrack"`
	ViaDrill      float64 `json:"viaDrill"`
	ViaDia        float64 `json:"viaDia"`
	EdgeClearance float64 `json:"edgeClearance"`
	CopperOz      float64 `json:"copperOz"`      // outer copper weight
	InnerCopperOz float64 `json:"innerCopperOz"` // inner copper weight
	BoardThickMil float64 `json:"boardThickMil"`
}

// DefaultRules are conservative JLCPCB-class standard-process values.
func DefaultRules() Rules {
	return Rules{Clearance: 6, TrackWidth: 6, MinTrack: 5, ViaDrill: 12, ViaDia: 24,
		EdgeClearance: 12, CopperOz: 1, InnerCopperOz: 0.5, BoardThickMil: 62.99}
}

func (r *Rules) sanitize() {
	d := DefaultRules()
	// Snapshot rules sometimes arrive in the wrong unit (mm*39.37 twice);
	// anything implausible for a standard process falls back to defaults.
	fix := func(v *float64, def, lo, hi float64) {
		if rec := math.Round(*v/39.37007874*100) / 100; *v > hi && rec >= lo && rec <= hi {
			// A mil value multiplied by 39.37 as if it were mm (projects
			// displayed in mil): recover it rather than drop the board's
			// real, often finer, rules.
			*v = rec
			return
		}
		if *v < lo || *v > hi {
			*v = def
		}
	}
	fix(&r.Clearance, d.Clearance, 2, 60)
	fix(&r.TrackWidth, d.TrackWidth, 2, 60)
	fix(&r.MinTrack, d.MinTrack, 2, 40)
	fix(&r.ViaDrill, d.ViaDrill, 6, 60)
	fix(&r.ViaDia, d.ViaDia, 10, 100)
	fix(&r.EdgeClearance, d.EdgeClearance, 4, 200)
	fix(&r.CopperOz, d.CopperOz, 0.25, 6)
	fix(&r.InnerCopperOz, d.InnerCopperOz, 0.25, 6)
	fix(&r.BoardThickMil, d.BoardThickMil, 10, 250)
	// JLC's minimum annular ring is 0.05 mm (2 mil a side): a 0.15/0.25 mm
	// via (6/10 mil) is legal and is what 0.65 mm-pitch BGAs fan out with.
	if r.ViaDia < r.ViaDrill+4 {
		r.ViaDia = r.ViaDrill + 8
	}
	if r.MinTrack > r.TrackWidth {
		r.MinTrack = r.TrackWidth
	}
}

// Board is the complete measured input.
type Board struct {
	Outline  []Point    `json:"outline"`
	Parts    []*Part    `json:"parts"`
	Keepouts []*Keepout `json:"keepouts,omitempty"`
	Holes    []*Hole    `json:"holes,omitempty"`
	Rules    Rules      `json:"rules"`
	// CopperLayers is the current layer count (0 = unknown).
	CopperLayers int `json:"copperLayers"`

	byRef map[string]*Part
}

// Index (re)builds lookup tables and relative pad poses. Call after edits
// that add parts or pads; MoveTo keeps it valid.
func (b *Board) Index() error {
	b.byRef = map[string]*Part{}
	for _, p := range b.Parts {
		if p.Ref == "" {
			return fmt.Errorf("part without designator")
		}
		if _, dup := b.byRef[p.Ref]; dup {
			return fmt.Errorf("duplicate designator %s", p.Ref)
		}
		b.byRef[p.Ref] = p
		if p.Side == 0 {
			p.Side = LayerTop
		}
		for _, pd := range p.Pads {
			pd.Part = p.Ref
		}
		p.freeze()
	}
	b.Rules.sanitize()
	return nil
}

// Part returns a part by designator.
func (b *Board) Part(ref string) *Part {
	if b.byRef == nil {
		_ = b.Index()
	}
	return b.byRef[ref]
}

// Bounds returns the outline bounds, or the part extent when no outline exists.
func (b *Board) Bounds() Rect {
	if len(b.Outline) >= 3 {
		return PolyBounds(b.Outline)
	}
	r := EmptyRect()
	for _, p := range b.Parts {
		r = r.Union(p.Body())
	}
	return r
}

// Area is the outline area in mil².
func (b *Board) Area() float64 {
	if len(b.Outline) >= 3 {
		return PolyArea(b.Outline)
	}
	return b.Bounds().Area()
}

// Net groups pads by net name.
type Net struct {
	Name string `json:"name"`
	Pads []*Pad `json:"-"`
}

// Nets returns every net with ≥1 pad, sorted by name for determinism.
func (b *Board) Nets() []*Net {
	m := map[string]*Net{}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net == "" {
				continue
			}
			n := m[pd.Net]
			if n == nil {
				n = &Net{Name: pd.Net}
				m[pd.Net] = n
			}
			n.Pads = append(n.Pads, pd)
		}
	}
	out := make([]*Net, 0, len(m))
	for _, n := range m {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Clone deep-copies the board so plans can be tried without mutating input.
func (b *Board) Clone() *Board {
	nb := *b
	nb.Outline = append([]Point(nil), b.Outline...)
	nb.Parts = make([]*Part, len(b.Parts))
	for i, p := range b.Parts {
		np := *p
		np.Pads = make([]*Pad, len(p.Pads))
		for j, pd := range p.Pads {
			c := *pd
			np.Pads[j] = &c
		}
		nb.Parts[i] = &np
	}
	nb.Keepouts = append([]*Keepout(nil), b.Keepouts...)
	nb.Holes = append([]*Hole(nil), b.Holes...)
	nb.byRef = nil
	_ = nb.Index()
	return &nb
}

func upper(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }
