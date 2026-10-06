// Package boardmanual builds a human-readable board USER MANUAL (one
// self-contained HTML file, inline CSS and SVG) from offline files: a board
// dump (`pcb dump --include-copper`), optionally intent.json, sim.json and a
// project notes.json with the human text. It does no I/O except reading its
// embedded template; the CLI (`pcbpilot report manual`) reads the inputs and
// writes the file.
package boardmanual

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// MilToMM converts dump units (mil) to millimetres.
const MilToMM = 0.0254

// Board is the subset of a `pcb dump` document the manual reads (mil, y up).
type Board struct {
	Components []Part `json:"components"`
	Outline    struct {
		BBox   BBox        `json:"bbox"`
		Points [][]float64 `json:"points"`
	} `json:"outline"`
	CopperLayers int `json:"copperLayers"`
	Copper       struct {
		Fills   []Fill   `json:"fills"`
		Regions []Region `json:"regions"`
		Vias    []Via    `json:"vias"`
		Lines   []struct {
			LineWidth float64 `json:"lineWidth"`
		} `json:"lines"`
	} `json:"copper"`
	Rules struct {
		ClearanceMil     float64 `json:"clearanceMil"`
		TrackTrackMil    float64 `json:"clearanceTrackTrackMil"`
		TrackWidthMinMil float64 `json:"trackWidthMinMil"`
	} `json:"rules"`
	FootprintHoles []Hole `json:"footprintHoles"`
	CapturedAt     string `json:"capturedAt"`
	SemanticSHA256 string `json:"semanticSha256"`
}

// BBox is an axis-aligned box (mil).
type BBox struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
}

func (b BBox) W() float64  { return b.MaxX - b.MinX }
func (b BBox) H() float64  { return b.MaxY - b.MinY }
func (b BBox) CX() float64 { return (b.MinX + b.MaxX) / 2 }
func (b BBox) CY() float64 { return (b.MinY + b.MaxY) / 2 }
func (b BBox) valid() bool { return b.MaxX > b.MinX && b.MaxY > b.MinY }

// ext accumulates a bbox.
type ext struct {
	bb BBox
	ok bool
}

func (e *ext) add(x, y float64) {
	if !e.ok {
		e.bb, e.ok = BBox{x, y, x, y}, true
		return
	}
	b := &e.bb
	b.MinX, b.MinY = math.Min(b.MinX, x), math.Min(b.MinY, y)
	b.MaxX, b.MaxY = math.Max(b.MaxX, x), math.Max(b.MaxY, y)
}

// Part is one placed component.
type Part struct {
	Designator string  `json:"designator"`
	Device     string  `json:"device"`
	Layer      int     `json:"layer"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Rotation   float64 `json:"rotation"`
	BBox       *BBox   `json:"bbox"`
	Pads       []Pad   `json:"pads"`
}

// Pad is one pad. Width/Height are the absolute (already rotated) extents.
type Pad struct {
	PadNumber string            `json:"padNumber"`
	Net       string            `json:"net"`
	Layer     int               `json:"layer"`
	X         float64           `json:"x"`
	Y         float64           `json:"y"`
	Width     float64           `json:"width"`
	Height    float64           `json:"height"`
	Rotation  float64           `json:"rotation"`
	Shape     []json.RawMessage `json:"shape"`
}

// ShapeKind is RECT, ELLIPSE, OVAL, POLYGON … (upper case; "" unknown).
func (p Pad) ShapeKind() string {
	if len(p.Shape) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(p.Shape[0], &s) != nil {
		return ""
	}
	return strings.ToUpper(s)
}

// Hole is a footprint or board hole.
type Hole struct {
	Owner string  `json:"owner"`
	Shape string  `json:"shape"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Dia   float64 `json:"dia"`
}

// Fill is a copper fill of the dump (only its box, layer and net are read).
type Fill struct {
	BBox  BBox    `json:"bbox"`
	Layer int     `json:"layer"`
	Net   *string `json:"net"`
}

// Region is a rule region (keep-out) of the dump.
type Region struct {
	BBox          BBox     `json:"bbox"`
	Layer         int      `json:"layer"`
	RuleTypeNames []string `json:"ruleTypeNames"`
}

// Via is one via of the dump (mil).
type Via struct {
	X            float64 `json:"x"`
	Y            float64 `json:"y"`
	Diameter     float64 `json:"diameter"`
	HoleDiameter float64 `json:"holeDiameter"`
	Net          string  `json:"net"`
}

// MountHole is a mounting hole: a footprint hole with no owner / an H* part,
// or a net-less round multi-layer (layer 12) fill of 1.5–8 mm.
type MountHole struct {
	X, Y, Dia float64 // mil
	From      string
}

// MountHoles lists the board's mounting holes.
func (b *Board) MountHoles() []MountHole {
	var out []MountHole
	for _, h := range b.FootprintHoles {
		if h.Owner == "" || refPrefix(h.Owner) == "H" || refPrefix(h.Owner) == "MH" {
			out = append(out, MountHole{h.X, h.Y, h.Dia, "hole"})
		}
	}
	for _, p := range b.Components {
		if pf := refPrefix(p.Designator); (pf == "H" || pf == "MH") && len(p.Pads) <= 1 {
			d := 125.0
			if len(p.Pads) == 1 {
				d = math.Max(p.Pads[0].Width, p.Pads[0].Height)
			}
			out = append(out, MountHole{p.X, p.Y, d, p.Designator})
		}
	}
	for _, f := range b.Copper.Fills {
		w, h := f.BBox.W(), f.BBox.H()
		if f.Layer != 12 || (f.Net != nil && *f.Net != "") || w <= 0 || math.Abs(w-h) > 0.02*w {
			continue
		}
		if mm := w * MilToMM; mm >= 1.5 && mm <= 8 {
			out = append(out, MountHole{f.BBox.CX(), f.BBox.CY(), w, "fill"})
		}
	}
	return out
}

// ParseBoard reads a `pcb dump` JSON (bare or inside {"result":…}).
func ParseBoard(raw []byte) (*Board, error) {
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Result) > 0 && env.Result[0] == '{' {
		raw = env.Result
	}
	var b Board
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	if len(b.Components) == 0 {
		return nil, fmt.Errorf("no components[] — not a pcb dump")
	}
	return &b, nil
}

// Part returns the component with a designator (nil when absent).
func (b *Board) Part(ref string) *Part {
	for i := range b.Components {
		if b.Components[i].Designator == ref {
			return &b.Components[i]
		}
	}
	return nil
}

// OutlineBox is the board outline bbox (from the points, else the dump bbox).
func (b *Board) OutlineBox() BBox {
	var e ext
	for _, p := range b.Outline.Points {
		if len(p) >= 2 {
			e.add(p[0], p[1])
		}
	}
	if e.ok && e.bb.valid() {
		return e.bb
	}
	return b.Outline.BBox
}

// Pad returns pad `num` of the part.
func (p *Part) Pad(num string) *Pad {
	for i := range p.Pads {
		if p.Pads[i].PadNumber == num {
			return &p.Pads[i]
		}
	}
	return nil
}

// Box is the part bbox (dump bbox, else the pad extents, else a point).
func (p *Part) Box() BBox {
	if p.BBox != nil && p.BBox.valid() {
		return *p.BBox
	}
	return p.PadBox()
}

// PadBox is the extent of all pads (copper, mil).
func (p *Part) PadBox() BBox {
	var e ext
	for _, pd := range p.Pads {
		e.add(pd.X-pd.Width/2, pd.Y-pd.Height/2)
		e.add(pd.X+pd.Width/2, pd.Y+pd.Height/2)
	}
	if !e.ok || !e.bb.valid() {
		return BBox{p.X - 10, p.Y - 10, p.X + 10, p.Y + 10}
	}
	return e.bb
}

// Nets is the set of pad nets of the part.
func (p *Part) Nets() map[string]bool {
	m := map[string]bool{}
	for _, pd := range p.Pads {
		if pd.Net != "" {
			m[pd.Net] = true
		}
	}
	return m
}

// natLess compares strings with embedded numbers naturally (J2 < J10, 2 < 10).
func natLess(a, b string) bool {
	ca, cb := chunks(a), chunks(b)
	for i := 0; i < len(ca) && i < len(cb); i++ {
		x, y := ca[i], cb[i]
		xn, ex := strconv.Atoi(x)
		yn, ey := strconv.Atoi(y)
		switch {
		case ex == nil && ey == nil:
			if xn != yn {
				return xn < yn
			}
		case x != y:
			return x < y
		}
	}
	return len(ca) < len(cb)
}

func chunks(s string) []string {
	var out []string
	cur := ""
	digit := false
	for i, r := range s {
		d := unicode.IsDigit(r)
		if i > 0 && d != digit {
			out = append(out, cur)
			cur = ""
		}
		digit = d
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func sortNat(s []string) { sort.SliceStable(s, func(i, j int) bool { return natLess(s[i], s[j]) }) }

// refPrefix is the letter prefix of a designator (J6 → J, CN1 → CN).
func refPrefix(ref string) string {
	i := 0
	for i < len(ref) && unicode.IsLetter(rune(ref[i])) {
		i++
	}
	return strings.ToUpper(ref[:i])
}
