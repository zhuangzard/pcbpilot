package postsim

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// EasyEDA copper layer ids: 1 top, 2 bottom, 15… inner, 12 = all layers
// (through-hole pads, board cutouts).
const (
	LayerTop    = 1
	LayerBottom = 2
	LayerMulti  = 12
	LayerInner1 = 15
)

// Pad is one copper pad in board coordinates.
type Pad struct {
	Part   string
	Number string
	Net    string
	Layer  int // 1, 2 or 12
	C      Point
	W, H   float64 // pad frame size (mil)
	Rot    float64 // degrees
	Round  bool    // ELLIPSE / OVAL: stadium
	Drill  float64 // plated hole (mil), THT only
}

// contains reports whether p is on the pad, grown by margin (mil).
func (p *Pad) contains(q Point, margin float64) bool {
	a := -p.Rot * math.Pi / 180
	dx, dy := q.X-p.C.X, q.Y-p.C.Y
	x := dx*math.Cos(a) - dy*math.Sin(a)
	y := dx*math.Sin(a) + dy*math.Cos(a)
	hw, hh := p.W/2+margin, p.H/2+margin
	if !p.Round {
		return math.Abs(x) <= hw && math.Abs(y) <= hh
	}
	// Stadium: a segment along the long axis, radius = half the short side.
	if hw >= hh {
		l := hw - hh
		cx := math.Max(-l, math.Min(l, x))
		return math.Hypot(x-cx, y) <= hh
	}
	l := hh - hw
	cy := math.Max(-l, math.Min(l, y))
	return math.Hypot(x, y-cy) <= hw
}

func (p *Pad) bounds() Rect {
	r := math.Hypot(p.W, p.H) / 2
	return Rect{p.C.X - r, p.C.Y - r, p.C.X + r, p.C.Y + r}
}

// area in mil².
func (p *Pad) area() float64 {
	if p.Round {
		s := math.Min(p.W, p.H)
		return s*math.Abs(p.W-p.H) + math.Pi*s*s/4
	}
	return p.W * p.H
}

// OnLayer reports whether the pad has copper on layer id.
func (p *Pad) OnLayer(id int) bool { return p.Layer == LayerMulti || p.Layer == id }

// Part is a placed footprint.
type Part struct {
	Ref    string
	Device string
	Side   int // 1 top, 2 bottom
	BBox   Rect
	Pads   []*Pad
}

// Track is one straight copper segment.
type Track struct {
	ID    string
	Net   string
	Layer int
	A, B  Point
	W     float64
}

// Via is a plated through via.
type Via struct {
	ID    string
	Net   string
	C     Point
	Dia   float64
	Drill float64
}

// Area is filled copper of one net on one layer (poured fill, unpoured pour
// boundary, static fill or a plane): even-odd contours.
type Area struct {
	ID       string
	Net      string
	Layer    int
	Kind     string // poured | pour | fill | plane
	Contours [][]Point
	Box      Rect
	// Carve: other nets' copper (plus clearance) is cut out when rasterising
	// (unpoured pour outlines and negative planes, whose real copper the
	// host computes but the dump does not carry).
	Carve bool
}

// Hole is a non-plated board cutout (mounting hole, footprint NPTH/slot).
type Hole struct {
	Contours [][]Point
	Box      Rect
}

// Rules are the dump's live DRC values used here (mil).
type Rules struct {
	ClearanceMil    float64
	CopperToEdgeMil float64
	ViaDrillMil     float64
	ViaDiameterMil  float64
}

// Board is the copper model read from a `pcb dump --include-copper`.
type Board struct {
	Outline   []Point
	Box       Rect
	Layers    []int // copper layer ids in stack order, top first
	Parts     []*Part
	Tracks    []Track
	Vias      []Via
	Areas     []Area
	Holes     []Hole
	Rules     Rules
	Copper    bool     // copper was captured
	Notes     []string // parse-time assumptions
	Semantic  string   // semanticSha256 of the dump
	Project   string
	spokes    int
	byRefPin  map[string]*Pad
	layerHas  map[int]int // copper objects per layer
	netLayers map[string]map[int]bool
}

// PadByPin returns the pad REF.PIN.
func (b *Board) PadByPin(ref, pin string) *Pad { return b.byRefPin[ref+"."+pin] }

// StackLayers returns the copper layer ids for a count, top first:
// 1, 15, 16, …, 2.
func StackLayers(n int) []int {
	if n <= 1 {
		return []int{LayerTop}
	}
	out := []int{LayerTop}
	for i := 0; i < n-2; i++ {
		out = append(out, LayerInner1+i)
	}
	return append(out, LayerBottom)
}

// LayerName is the conventional name of a copper layer id.
func LayerName(id int) string {
	switch id {
	case LayerTop:
		return "TOP"
	case LayerBottom:
		return "BOTTOM"
	}
	if id >= LayerInner1 {
		return "IN" + strconv.Itoa(id-LayerInner1+1)
	}
	return "L" + strconv.Itoa(id)
}

type dumpDoc struct {
	Components []struct {
		ID         string         `json:"primitiveId"`
		Designator string         `json:"designator"`
		Device     string         `json:"device"`
		Layer      int            `json:"layer"`
		BBox       map[string]any `json:"bbox"`
		Pads       []struct {
			Number   string  `json:"padNumber"`
			Net      string  `json:"net"`
			Layer    int     `json:"layer"`
			X        float64 `json:"x"`
			Y        float64 `json:"y"`
			W        float64 `json:"width"`
			H        float64 `json:"height"`
			Rotation float64 `json:"rotation"`
			Shape    any     `json:"shape"`
			Hole     any     `json:"hole"`
			Drill    float64 `json:"holeDiameter"`
		} `json:"pads"`
	} `json:"components"`
	Outline *struct {
		Points [][2]float64   `json:"points"`
		BBox   map[string]any `json:"bbox"`
	} `json:"outline"`
	CopperLayers int `json:"copperLayers"`
	Rules        *struct {
		ClearanceMil    float64 `json:"clearanceMil"`
		CopperToEdgeMil float64 `json:"copperToEdgeMil"`
		ViaDrillMil     float64 `json:"viaDrillMil"`
		ViaDiameterMil  float64 `json:"viaDiameterMil"`
	} `json:"rules"`
	Copper *struct {
		Lines   []map[string]any `json:"lines"`
		Arcs    []map[string]any `json:"arcs"`
		Vias    []map[string]any `json:"vias"`
		Pours   []map[string]any `json:"pours"`
		Poured  []map[string]any `json:"poured"`
		Fills   []map[string]any `json:"fills"`
		Regions []map[string]any `json:"regions"`
	} `json:"copper"`
	FootprintHoles []struct {
		Owner  string       `json:"owner"`
		Shape  string       `json:"shape"`
		X      float64      `json:"x"`
		Y      float64      `json:"y"`
		Dia    float64      `json:"dia"`
		Points [][2]float64 `json:"points"`
	} `json:"footprintHoles"`
	SemanticSHA256 string `json:"semanticSha256"`
	Project        string `json:"project"`
}

// ParseBoard reads a `pcb dump` snapshot. Without copper (no
// --include-copper) the result has pads only and Copper=false.
func ParseBoard(raw []byte) (*Board, error) {
	var d dumpDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("board dump: %w", err)
	}
	b := &Board{byRefPin: map[string]*Pad{}, layerHas: map[int]int{}, netLayers: map[string]map[int]bool{},
		Semantic: d.SemanticSHA256, Project: d.Project}
	b.Rules = Rules{ClearanceMil: 6, CopperToEdgeMil: 10, ViaDrillMil: 12, ViaDiameterMil: 24}
	if d.Rules != nil {
		if d.Rules.ClearanceMil > 0 {
			b.Rules.ClearanceMil = d.Rules.ClearanceMil
		}
		if d.Rules.CopperToEdgeMil > 0 {
			b.Rules.CopperToEdgeMil = d.Rules.CopperToEdgeMil
		}
		if d.Rules.ViaDrillMil > 0 {
			b.Rules.ViaDrillMil = d.Rules.ViaDrillMil
		}
		if d.Rules.ViaDiameterMil > 0 {
			b.Rules.ViaDiameterMil = d.Rules.ViaDiameterMil
		}
	}
	n := d.CopperLayers
	if n <= 0 {
		n = 2
		b.Notes = append(b.Notes, "copperLayers missing from the dump: assumed 2")
	}
	b.Layers = StackLayers(n)
	tht := 0
	for _, c := range d.Components {
		p := &Part{Ref: c.Designator, Device: c.Device, Side: LayerTop, BBox: emptyRect()}
		if c.Layer == LayerBottom {
			p.Side = LayerBottom
		}
		if bb, ok := anyRect(c.BBox); ok {
			p.BBox = bb
		}
		for _, sp := range c.Pads {
			pd := &Pad{Part: c.Designator, Number: sp.Number, Net: sp.Net, Layer: sp.Layer, C: Point{sp.X, sp.Y},
				W: sp.W, H: sp.H, Rot: sp.Rotation}
			if pd.Layer != LayerBottom && pd.Layer != LayerMulti {
				pd.Layer = LayerTop
			}
			if arr, ok := sp.Shape.([]any); ok && len(arr) >= 3 {
				kind := strings.ToUpper(str(arr[0]))
				if w, h := num(arr[1]), num(arr[2]); w > 0 && h > 0 {
					pd.W, pd.H = w, h
					pd.Round = kind == "ELLIPSE" || kind == "OVAL" || kind == "CIRCLE"
				}
			}
			if pd.W <= 0 || pd.H <= 0 {
				pd.W, pd.H, pd.Rot = math.Max(pd.W, 10), math.Max(pd.H, 10), 0
			}
			if pd.Layer == LayerMulti {
				pd.Drill = sp.Drill
				if pd.Drill <= 0 {
					if arr, ok := sp.Hole.([]any); ok && len(arr) >= 2 {
						pd.Drill = num(arr[1])
					}
				}
				if pd.Drill <= 0 {
					pd.Drill = 0.5 * math.Min(pd.W, pd.H)
					tht++
				}
			}
			p.Pads = append(p.Pads, pd)
			b.byRefPin[pd.Part+"."+pd.Number] = pd
			if !p.BBox.valid() {
				bb := pd.bounds()
				p.BBox.add(Point{bb.MinX, bb.MinY})
				p.BBox.add(Point{bb.MaxX, bb.MaxY})
			}
			b.touch(pd.Net, pd.Layer)
		}
		b.Parts = append(b.Parts, p)
	}
	if tht > 0 {
		b.Notes = append(b.Notes, fmt.Sprintf("%d through-hole pad(s) carry no drill in the dump: plated hole assumed = 0.5 × the smaller pad side", tht))
	}
	sort.Slice(b.Parts, func(i, j int) bool { return b.Parts[i].Ref < b.Parts[j].Ref })
	if d.Outline != nil && len(d.Outline.Points) >= 3 {
		for _, q := range d.Outline.Points {
			b.Outline = append(b.Outline, Point{q[0], q[1]})
		}
	} else if d.Outline != nil {
		if bb, ok := anyRect(d.Outline.BBox); ok {
			b.Outline = []Point{{bb.MinX, bb.MinY}, {bb.MaxX, bb.MinY}, {bb.MaxX, bb.MaxY}, {bb.MinX, bb.MaxY}}
			b.Notes = append(b.Notes, "board outline polygon missing: the outline bbox is used")
		}
	}
	if len(b.Outline) < 3 {
		r := emptyRect()
		for _, p := range b.Parts {
			if p.BBox.valid() {
				r.add(Point{p.BBox.MinX, p.BBox.MinY})
				r.add(Point{p.BBox.MaxX, p.BBox.MaxY})
			}
		}
		if !r.valid() {
			return nil, fmt.Errorf("board dump: no outline and no parts")
		}
		r = r.expand(40)
		b.Outline = []Point{{r.MinX, r.MinY}, {r.MaxX, r.MinY}, {r.MaxX, r.MaxY}, {r.MinX, r.MaxY}}
		b.Notes = append(b.Notes, "board outline missing: the parts' bounding box + 40 mil is used")
	}
	b.Box = polyBounds(b.Outline)
	for _, h := range d.FootprintHoles {
		var poly []Point
		if len(h.Points) >= 3 {
			for _, q := range h.Points {
				poly = append(poly, Point{q[0], q[1]})
			}
		} else if h.Dia > 0 {
			poly = circlePoly(Point{h.X, h.Y}, h.Dia/2, 24)
		}
		if len(poly) >= 3 {
			b.Holes = append(b.Holes, Hole{Contours: [][]Point{poly}, Box: polyBounds(poly)})
		}
	}
	if d.Copper == nil {
		b.Notes = append(b.Notes, "the dump has no copper (run pcb dump --include-copper): only pads are modelled")
		return b, nil
	}
	b.Copper = true
	for _, l := range d.Copper.Lines {
		t := Track{ID: str(l["primitiveId"]), Net: str(l["net"]), Layer: int(num(l["layer"])),
			A: Point{num(l["startX"]), num(l["startY"])}, B: Point{num(l["endX"]), num(l["endY"])}, W: num(l["lineWidth"])}
		if t.W <= 0 || t.Layer == 0 {
			continue
		}
		b.Tracks = append(b.Tracks, t)
		b.touch(t.Net, t.Layer)
	}
	for _, a := range d.Copper.Arcs {
		p0, p1 := Point{num(a["startX"]), num(a["startY"])}, Point{num(a["endX"]), num(a["endY"])}
		w, layer, net := num(a["lineWidth"]), int(num(a["layer"])), str(a["net"])
		if w <= 0 || layer == 0 {
			continue
		}
		pts := append([]Point{p0}, arcPoints(p0, p1, num(a["arcAngle"]))...)
		for i := 0; i+1 < len(pts); i++ {
			b.Tracks = append(b.Tracks, Track{ID: str(a["primitiveId"]), Net: net, Layer: layer, A: pts[i], B: pts[i+1], W: w})
		}
		b.touch(net, layer)
	}
	for _, v := range d.Copper.Vias {
		via := Via{ID: str(v["primitiveId"]), Net: str(v["net"]), C: Point{num(v["x"]), num(v["y"])},
			Dia: num(v["diameter"]), Drill: num(v["holeDiameter"])}
		if via.Dia <= 0 {
			via.Dia = b.Rules.ViaDiameterMil
		}
		if via.Drill <= 0 {
			via.Drill = b.Rules.ViaDrillMil
		}
		b.Vias = append(b.Vias, via)
		b.touch(via.Net, LayerMulti)
	}
	poured := map[string]bool{}
	for _, p := range d.Copper.Poured {
		layer, net := int(num(p["layer"])), str(p["net"])
		fills, _ := p["fills"].([]any)
		for fi, f := range fills {
			fm, _ := f.(map[string]any)
			if fm == nil {
				continue
			}
			if kind := str(fm["geometryKind"]); strings.Contains(kind, "stroke") || fm["fill"] == false {
				// Stroked path (thermal-relief spoke): copper of lineWidth.
				w := num(fm["lineWidth"])
				if w <= 0 {
					continue
				}
				for _, path := range parsePaths(fm["source"]) {
					for i := 0; i+1 < len(path); i++ {
						b.Tracks = append(b.Tracks, Track{ID: fmt.Sprintf("%s#spoke%d", str(p["pourPrimitiveId"]), fi), Net: net, Layer: layer, A: path[i], B: path[i+1], W: w})
					}
				}
				b.spokes++
				continue
			}
			cs := parseSource(fm["source"])
			if len(cs) == 0 {
				continue
			}
			b.Areas = append(b.Areas, Area{ID: fmt.Sprintf("%s#%d", str(p["pourPrimitiveId"]), fi), Net: net, Layer: layer,
				Kind: "poured", Contours: cs, Box: polyBounds(cs...)})
			b.touch(net, layer)
		}
		if id := str(p["pourPrimitiveId"]); id != "" && len(fills) > 0 {
			poured[id] = true
		}
	}
	if b.spokes > 0 {
		b.Notes = append(b.Notes, fmt.Sprintf("%d thermal-relief spoke(s) of the poured copper modelled as tracks of their stroke width", b.spokes))
	}
	unpoured := 0
	for _, p := range d.Copper.Pours {
		id, layer, net := str(p["primitiveId"]), int(num(p["layer"])), str(p["net"])
		if poured[id] || net == "" {
			continue
		}
		cs := parseSource(p["source"])
		if len(cs) == 0 {
			continue
		}
		unpoured++
		b.Areas = append(b.Areas, Area{ID: id, Net: net, Layer: layer, Kind: "pour", Contours: cs, Box: polyBounds(cs...), Carve: true})
		b.touch(net, layer)
	}
	if unpoured > 0 {
		b.Notes = append(b.Notes, fmt.Sprintf("%d pour(s) without materialised copper: the pour outline minus other nets' copper + clearance is used (approximates the host's pour)", unpoured))
	}
	for _, f := range d.Copper.Fills {
		layer, net := int(num(f["layer"])), str(f["net"])
		cs := parseSource(f["source"])
		if len(cs) == 0 {
			continue
		}
		if layer == LayerMulti {
			// MULTI-layer fill = board cutout (mounting hole / slot).
			b.Holes = append(b.Holes, Hole{Contours: cs, Box: polyBounds(cs...)})
			continue
		}
		if net == "" {
			continue // unconnected static copper: ignored electrically and thermally
		}
		b.Areas = append(b.Areas, Area{ID: str(f["primitiveId"]), Net: net, Layer: layer, Kind: "fill", Contours: cs, Box: polyBounds(cs...)})
		b.touch(net, layer)
	}
	return b, nil
}

// touch records net copper on layer. Vias and through-hole pads (layer 12)
// do not count as layer content: a negative plane layer is pierced by them
// but otherwise empty in the dump.
func (b *Board) touch(net string, layer int) {
	if layer != LayerMulti {
		b.layerHas[layer]++
	}
	if net == "" {
		return
	}
	if b.netLayers[net] == nil {
		b.netLayers[net] = map[int]bool{}
	}
	b.netLayers[net][layer] = true
}

// EmptyInnerLayers lists inner copper layers that carry no copper object at
// all (candidates for a negative plane the dump cannot see).
func (b *Board) EmptyInnerLayers() []int {
	var out []int
	for _, l := range b.Layers {
		if l >= LayerInner1 && b.layerHas[l] == 0 {
			out = append(out, l)
		}
	}
	return out
}

// Nets returns every net that has copper.
func (b *Board) Nets() []string {
	var out []string
	for n := range b.netLayers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func anyRect(m map[string]any) (Rect, bool) {
	if m == nil {
		return Rect{}, false
	}
	r := Rect{num(m["minX"]), num(m["minY"]), num(m["maxX"]), num(m["maxY"])}
	return r, r.valid()
}

// AddPlane declares a negative plane: the whole outline (inset by the
// copper-to-edge rule) of net on layer, carved around other nets' vias and
// through-hole pads by their radius + clearance.
func (b *Board) AddPlane(layer int, net string) {
	b.Areas = append(b.Areas, Area{ID: "plane-" + LayerName(layer), Net: net, Layer: layer, Kind: "plane",
		Contours: [][]Point{b.Outline}, Box: b.Box, Carve: true})
	b.touch(net, layer)
}
