package pcbauto

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// MechSpec is the mechanical contract of the board: what the enclosure,
// connectors and fixings force, in the user's units (default mm, origin at
// the board's lower-left corner, y-up).
//
//	{
//	  "units": "mm",
//	  "board": {"width": 50, "height": 40, "cornerRadius": 2},
//	  "holes": [{"x": 3.5, "y": 3.5, "dia": 3.2, "keepout": 6}],
//	  "cornerHoles": {"size": "M3", "inset": 3.5},
//	  "fixed": [{"ref": "U1", "x": 25, "y": 20, "rot": 0}],
//	  "edge":  [{"ref": "J1", "edge": "left", "at": 20, "overhang": 0.5}],
//	  "keepouts": [{"name": "antenna", "rect": [40, 30, 50, 40], "noCopper": true, "noParts": true}],
//	  "heightZones": [{"rect": [0, 0, 10, 40], "maxHeight": 3}],
//	  "zones": [{"domain": "MAINS", "rect": [0, 0, 20, 40]}]
//	}
type MechSpec struct {
	Units string `json:"units,omitempty"` // mm (default) | mil
	Board struct {
		Width        float64      `json:"width"`
		Height       float64      `json:"height"`
		CornerRadius float64      `json:"cornerRadius,omitempty"`
		Outline      [][2]float64 `json:"outline,omitempty"` // polygon overrides width/height
		Thickness    float64      `json:"thickness,omitempty"`
		// AutoSize lets the placer shrink the board to the placement (plus
		// Margin) instead of honouring width/height.
		AutoSize bool    `json:"autoSize,omitempty"`
		Margin   float64 `json:"margin,omitempty"`
	} `json:"board"`
	Holes       []MechHole `json:"holes,omitempty"`
	CornerHoles *struct {
		Size  string  `json:"size"` // M2 | M2.5 | M3 | M4
		Inset float64 `json:"inset"`
	} `json:"cornerHoles,omitempty"`
	Fixed       []MechFixed   `json:"fixed,omitempty"`
	Edge        []MechEdge    `json:"edge,omitempty"`
	Keepouts    []MechKeepout `json:"keepouts,omitempty"`
	HeightZones []struct {
		Rect      [4]float64 `json:"rect"`
		MaxHeight float64    `json:"maxHeight"`
	} `json:"heightZones,omitempty"`
	Zones []MechZone `json:"zones,omitempty"`
}

// MechHole is a mounting hole.
type MechHole struct {
	Name    string  `json:"name,omitempty"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Dia     float64 `json:"dia"`
	Keepout float64 `json:"keepout,omitempty"` // diameter of the copper/part-free disc (screw head)
}

// MechFixed pins a part to an absolute pose.
type MechFixed struct {
	Ref  string  `json:"ref"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Rot  float64 `json:"rot"`
	Side string  `json:"side,omitempty"` // top | bottom
}

// MechEdge puts a connector on a board edge, opening outward.
type MechEdge struct {
	Ref      string  `json:"ref"`
	Edge     string  `json:"edge"`               // left | right | top | bottom
	At       float64 `json:"at"`                 // position along the edge (from its low end); <0 = centre
	Overhang float64 `json:"overhang,omitempty"` // body beyond the edge (mm), e.g. USB receptacles
}

// MechKeepout forbids parts and/or copper in a rectangle or polygon.
type MechKeepout struct {
	Name     string       `json:"name,omitempty"`
	Rect     *[4]float64  `json:"rect,omitempty"`
	Poly     [][2]float64 `json:"poly,omitempty"`
	Layers   []int        `json:"layers,omitempty"`
	NoCopper bool         `json:"noCopper"`
	NoParts  bool         `json:"noParts"`
}

// MechZone reserves an area for a voltage domain or functional block.
type MechZone struct {
	Domain string     `json:"domain,omitempty"`
	Block  string     `json:"block,omitempty"`
	Rect   [4]float64 `json:"rect"`
}

// Zone is a MechZone in board coordinates (mil).
type Zone struct {
	Domain string `json:"domain,omitempty"`
	Block  string `json:"block,omitempty"`
	Rect   Rect   `json:"rect"`
}

// HeightZone limits component height (mil).
type HeightZone struct {
	Rect      Rect    `json:"rect"`
	MaxHeight float64 `json:"maxHeight"`
}

// Mechanics is the resolved mechanical constraint set in mil.
type Mechanics struct {
	Edge        map[string]MechEdge `json:"edge"`
	Fixed       map[string]bool     `json:"fixed"`
	Zones       []Zone              `json:"zones,omitempty"`
	HeightZones []HeightZone        `json:"heightZones,omitempty"`
	AutoSize    bool                `json:"autoSize"`
	MarginMil   float64             `json:"marginMil"`
	Notes       []string            `json:"notes,omitempty"`
}

// ParseMech decodes a mechanical spec.
func ParseMech(raw []byte) (*MechSpec, error) {
	var m MechSpec
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("mech spec: %w", err)
	}
	return &m, nil
}

var screwHoles = map[string][2]float64{ // drill, keepout diameter (mm)
	"M2": {2.2, 4.5}, "M2.5": {2.7, 5.5}, "M3": {3.2, 6.5}, "M4": {4.3, 8.5},
}

// ApplyMech writes the mechanical spec into the board: outline, holes,
// keepouts, fixed poses and edge connectors. It returns the constraint set
// the placer must keep.
func ApplyMech(b *Board, m *MechSpec) (*Mechanics, error) {
	k := 1 / 0.0254 // mm → mil
	if strings.EqualFold(m.Units, "mil") {
		k = 1
	}
	out := &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}, AutoSize: m.Board.AutoSize, MarginMil: m.Board.Margin * k}
	if out.MarginMil == 0 {
		out.MarginMil = 2 * k
		if k == 1 {
			out.MarginMil = 80
		}
	}
	// Outline.
	switch {
	case len(m.Board.Outline) >= 3:
		b.Outline = nil
		for _, p := range m.Board.Outline {
			b.Outline = append(b.Outline, Point{p[0] * k, p[1] * k})
		}
	case m.Board.Width > 0 && m.Board.Height > 0:
		b.Outline = RoundedRect(Rect{0, 0, m.Board.Width * k, m.Board.Height * k}, m.Board.CornerRadius*k)
	case !m.Board.AutoSize && len(b.Outline) < 3:
		return nil, fmt.Errorf("mech spec needs board.width/height, an outline, or autoSize")
	}
	if m.Board.Thickness > 0 {
		b.Rules.BoardThickMil = m.Board.Thickness * k
	}
	bb := b.Bounds()
	// Holes.
	holes := append([]MechHole(nil), m.Holes...)
	if ch := m.CornerHoles; ch != nil {
		spec, ok := screwHoles[strings.ToUpper(ch.Size)]
		if !ok {
			return nil, fmt.Errorf("cornerHoles.size %q: want M2|M2.5|M3|M4", ch.Size)
		}
		in := ch.Inset
		if in <= 0 {
			in = spec[1]/2 + 0.5
		}
		w, h := bb.W()/k, bb.H()/k
		for i, c := range [][2]float64{{in, in}, {w - in, in}, {w - in, h - in}, {in, h - in}} {
			holes = append(holes, MechHole{Name: fmt.Sprintf("MH%d", i+1), X: c[0], Y: c[1], Dia: spec[0], Keepout: spec[1]})
		}
		if k == 1 {
			return nil, fmt.Errorf("cornerHoles requires mm units")
		}
	}
	for _, h := range holes {
		keep := h.Keepout
		if keep < h.Dia {
			keep = h.Dia + 2
		}
		b.Holes = append(b.Holes, &Hole{Name: h.Name, C: Point{bb.MinX + h.X*k, bb.MinY + h.Y*k}, Dia: h.Dia * k, Keep: (keep - h.Dia) / 2 * k})
	}
	// Keepouts.
	for _, ko := range m.Keepouts {
		kp := &Keepout{Name: ko.Name, Layers: ko.Layers, NoCopper: ko.NoCopper, NoParts: ko.NoParts, NoVias: ko.NoCopper}
		switch {
		case ko.Rect != nil:
			r := ko.Rect
			kp.Poly = Rect{bb.MinX + r[0]*k, bb.MinY + r[1]*k, bb.MinX + r[2]*k, bb.MinY + r[3]*k}.Corners()
		case len(ko.Poly) >= 3:
			for _, p := range ko.Poly {
				kp.Poly = append(kp.Poly, Point{bb.MinX + p[0]*k, bb.MinY + p[1]*k})
			}
		default:
			return nil, fmt.Errorf("keepout %q needs rect or poly", ko.Name)
		}
		b.Keepouts = append(b.Keepouts, kp)
	}
	for _, hz := range m.HeightZones {
		r := hz.Rect
		out.HeightZones = append(out.HeightZones, HeightZone{Rect: Rect{bb.MinX + r[0]*k, bb.MinY + r[1]*k, bb.MinX + r[2]*k, bb.MinY + r[3]*k}, MaxHeight: hz.MaxHeight * k})
	}
	for _, z := range m.Zones {
		r := z.Rect
		out.Zones = append(out.Zones, Zone{Domain: z.Domain, Block: z.Block, Rect: Rect{bb.MinX + r[0]*k, bb.MinY + r[1]*k, bb.MinX + r[2]*k, bb.MinY + r[3]*k}})
	}
	// Fixed parts.
	for _, f := range m.Fixed {
		p := b.Part(f.Ref)
		if p == nil {
			return nil, fmt.Errorf("fixed part %s not on the board", f.Ref)
		}
		if f.Side == "bottom" && p.Side != LayerBottom {
			out.Notes = append(out.Notes, fmt.Sprintf("%s: side change to bottom must be done in EasyEDA (flip), pose applied on its current side", f.Ref))
		}
		movePartCentre(p, Point{bb.MinX + f.X*k, bb.MinY + f.Y*k}, f.Rot)
		p.Fixed = true
		out.Fixed[f.Ref] = true
	}
	// Edge connectors.
	for _, e := range m.Edge {
		p := b.Part(e.Ref)
		if p == nil {
			return nil, fmt.Errorf("edge part %s not on the board", e.Ref)
		}
		e.Overhang *= k
		if e.At >= 0 {
			e.At *= k
		}
		if err := PlaceOnEdge(b, p, e); err != nil {
			return nil, err
		}
		p.Fixed = true
		out.Edge[e.Ref] = e
		out.Fixed[e.Ref] = true
	}
	return out, nil
}

// RoundedRect returns a rectangle outline with corner arcs approximated by
// 8 segments each (r = 0 gives the plain rectangle).
func RoundedRect(r Rect, radius float64) []Point {
	if radius <= 0 {
		return r.Corners()
	}
	radius = math.Min(radius, math.Min(r.W(), r.H())/2)
	var out []Point
	centres := []Point{{r.MaxX - radius, r.MinY + radius}, {r.MaxX - radius, r.MaxY - radius}, {r.MinX + radius, r.MaxY - radius}, {r.MinX + radius, r.MinY + radius}}
	starts := []float64{-90, 0, 90, 180}
	for i, c := range centres {
		for s := 0; s <= 8; s++ {
			a := (starts[i] + float64(s)*90/8) * math.Pi / 180
			out = append(out, Point{c.X + radius*math.Cos(a), c.Y + radius*math.Sin(a)})
		}
	}
	return out
}

// centreOffset is the body centre relative to the anchor at the part's pose.
func centreOffset(p *Part) Point {
	return p.Body().Center().Sub(p.Pos)
}

// movePartCentre poses a part so its body centre lands at c with rotation rot.
func movePartCentre(p *Part, c Point, rot float64) {
	p.MoveTo(p.Pos, rot)
	off := centreOffset(p)
	p.MoveTo(c.Sub(off), rot)
}

// Facing returns the unit direction a connector's mating side points to in
// its current pose: from the pad centroid towards the body centre (SMD
// receptacles solder at the back, the opening is at the front). Symmetric
// through-hole headers return the zero vector (any orientation mates).
func Facing(p *Part) Point {
	if len(p.Pads) == 0 {
		return Point{}
	}
	var pc Point
	for _, pd := range p.Pads {
		pc = pc.Add(pd.Box.C)
	}
	pc = pc.Scale(1 / float64(len(p.Pads)))
	v := p.Body().Center().Sub(pc)
	l := math.Hypot(v.X, v.Y)
	body := p.Body()
	if l < 0.08*math.Max(body.W(), body.H()) {
		return Point{}
	}
	return v.Scale(1 / l)
}

// PlaceOnEdge rotates a connector so its opening faces out of the given edge
// and slides its body flush with the edge (plus overhang).
func PlaceOnEdge(b *Board, p *Part, e MechEdge) error {
	bb := b.Bounds()
	var out Point
	switch strings.ToLower(e.Edge) {
	case "left":
		out = Point{-1, 0}
	case "right":
		out = Point{1, 0}
	case "top":
		out = Point{0, 1}
	case "bottom":
		out = Point{0, -1}
	default:
		return fmt.Errorf("edge %q for %s: want left|right|top|bottom", e.Edge, p.Ref)
	}
	// Try the four right-angle rotations; keep the one whose facing is
	// closest to the outward normal (or the long side along the edge for
	// symmetric headers).
	best, bestScore := p.Rotation, math.Inf(-1)
	for _, rot := range []float64{0, 90, 180, 270} {
		p.MoveTo(p.Pos, rot)
		f := Facing(p)
		body := p.Body()
		score := f.X*out.X + f.Y*out.Y
		if f.X == 0 && f.Y == 0 {
			along := body.W()
			if out.X != 0 {
				along = body.H()
			}
			score = along / math.Max(body.W(), body.H())
		}
		if score > bestScore+1e-9 {
			best, bestScore = rot, score
		}
	}
	p.MoveTo(p.Pos, best)
	body := p.Body()
	c := body.Center()
	at := e.At
	switch {
	case out.X != 0:
		if at < 0 {
			at = bb.H() / 2
		}
		c.Y = bb.MinY + at
		if out.X < 0 {
			c.X = bb.MinX - e.Overhang + body.W()/2
		} else {
			c.X = bb.MaxX + e.Overhang - body.W()/2
		}
	default:
		if at < 0 {
			at = bb.W() / 2
		}
		c.X = bb.MinX + at
		if out.Y < 0 {
			c.Y = bb.MinY - e.Overhang + body.H()/2
		} else {
			c.Y = bb.MaxY + e.Overhang - body.H()/2
		}
	}
	movePartCentre(p, c, best)
	return nil
}
