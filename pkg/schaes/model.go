// Package schaes measures schematic aesthetics (Phase A: measurement only).
//
// It is the schematic counterpart of pcbauto's `pcb aesthetics`: a pure,
// I/O-free scorer over one page snapshot (parts with bbox + pins, wires,
// net markers, texts, frames, buses). Every score is report-only: it never
// gates, never feeds layout-score / layout-lint / sch check, and it can never
// trade connectivity for looks. Priority order (docs/reviews/2026-10-schematic-
// aesthetics/README.md §4): connectivity correctness > readability > aesthetics.
//
// Coordinates are EasyEDA Pro schematic canvas units (0.01 in, y-UP: +y renders
// upward). Directions are page directions: "up" = +y.
package schaes

import (
	"math"
	"sort"
	"strings"
)

// Pt is a point in canvas units.
type Pt struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Box is an axis-aligned rectangle.
type Box struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
}

func (b Box) W() float64    { return b.MaxX - b.MinX }
func (b Box) H() float64    { return b.MaxY - b.MinY }
func (b Box) C() Pt         { return Pt{(b.MinX + b.MaxX) / 2, (b.MinY + b.MaxY) / 2} }
func (b Box) Area() float64 { return math.Max(0, b.W()) * math.Max(0, b.H()) }
func (b Box) Valid() bool   { return b.MaxX > b.MinX && b.MaxY > b.MinY }
func (b Box) Grow(d float64) Box {
	return Box{b.MinX - d, b.MinY - d, b.MaxX + d, b.MaxY + d}
}
func (b Box) Contains(p Pt) bool {
	return p.X >= b.MinX && p.X <= b.MaxX && p.Y >= b.MinY && p.Y <= b.MaxY
}
func (b Box) Union(o Box) Box {
	if !b.Valid() {
		return o
	}
	if !o.Valid() {
		return b
	}
	return Box{math.Min(b.MinX, o.MinX), math.Min(b.MinY, o.MinY), math.Max(b.MaxX, o.MaxX), math.Max(b.MaxY, o.MaxY)}
}

// overlapArea is the intersection area of two boxes.
func overlapArea(a, b Box) float64 {
	w := math.Min(a.MaxX, b.MaxX) - math.Max(a.MinX, b.MinX)
	h := math.Min(a.MaxY, b.MaxY) - math.Max(a.MinY, b.MinY)
	if w <= 0 || h <= 0 {
		return 0
	}
	return w * h
}

// Pin is one component pin. Dir is the official outward direction
// (up/down/left/right) when known, "" otherwise.
type Pin struct {
	Number string  `json:"number"`
	Name   string  `json:"name,omitempty"`
	Net    string  `json:"net,omitempty"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Dir    string  `json:"dir,omitempty"`
	NC     bool    `json:"nc,omitempty"`
}

// Part is a placed component (componentType "part").
type Part struct {
	ID     string  `json:"id,omitempty"`
	Ref    string  `json:"ref"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Rot    float64 `json:"rotation"`
	Box    Box     `json:"bbox"`
	HasBox bool    `json:"-"`
	Pins   []Pin   `json:"pins,omitempty"`
}

// Wire is one polyline (or one official flat segment when len(Pts)==2).
type Wire struct {
	ID       string `json:"id,omitempty"`
	Net      string `json:"net,omitempty"`
	Pts      []Pt   `json:"points"`
	Implicit bool   `json:"implicit,omitempty"` // synthesized marker stub of a layout flag
}

// Marker kinds.
const (
	KindPower    = "power"
	KindGround   = "ground"
	KindNetPort  = "netport"
	KindNetLabel = "netlabel"
)

// Marker is a net flag / net port / net label. Anchor is the connection point;
// Dir is the direction the marker body points (away from its wire), "" unknown.
type Marker struct {
	ID        string `json:"id,omitempty"`
	Kind      string `json:"kind"`
	Net       string `json:"net"`
	Anchor    Pt     `json:"anchor"`
	Dir       string `json:"dir,omitempty"`
	PortIO    string `json:"portIO,omitempty"` // IN | OUT | BI when known
	Box       Box    `json:"bbox"`
	Estimated bool   `json:"estimated,omitempty"` // box predicted, not measured
}

// Text is a designator or free text box.
type Text struct {
	Kind      string `json:"kind"` // designator | free
	Content   string `json:"content"`
	Owner     string `json:"owner,omitempty"` // designator → part ref
	Box       Box    `json:"bbox"`
	Estimated bool   `json:"estimated,omitempty"`
}

// Frame is a module frame (dashed rectangle) with an optional title.
type Frame struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title,omitempty"`
	Box   Box    `json:"bbox"`
}

// Bus is a native bus primitive (sch_PrimitiveBus).
type Bus struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	Pts  [][]Pt `json:"line"`
}

// Snapshot is one schematic page normalized from any supported source.
type Snapshot struct {
	Source  string   `json:"source"` // components-list | layout | lib-layout | layout-render | canonical
	Parts   []Part   `json:"parts"`
	Wires   []Wire   `json:"wires"`
	Markers []Marker `json:"markers"`
	Texts   []Text   `json:"texts,omitempty"`
	Frames  []Frame  `json:"frames,omitempty"`
	Buses   []Bus    `json:"buses,omitempty"`
	Sheet   *Box     `json:"sheet,omitempty"`
	// Coverage flags: false = the source did not carry that object class, so
	// metrics that need it are skipped ("not measured" ≠ "measured perfect").
	HasWires   bool     `json:"hasWires"`
	HasMarkers bool     `json:"hasMarkers"`
	HasPins    bool     `json:"hasPins"`
	HasTexts   bool     `json:"hasTexts"`
	HasFrames  bool     `json:"hasFrames"`
	HasBuses   bool     `json:"hasBuses"`
	Notes      []string `json:"notes,omitempty"`
}

// ---------------------------------------------------------------------------
// direction helpers

var dirVec = map[string]Pt{"up": {0, 1}, "down": {0, -1}, "left": {-1, 0}, "right": {1, 0}}

func opposite(d string) string {
	switch d {
	case "up":
		return "down"
	case "down":
		return "up"
	case "left":
		return "right"
	case "right":
		return "left"
	}
	return ""
}

// dirOfAngle maps an official world pin rotation (0 right, 90 up, 180 left,
// 270 down; y-up page) to a direction.
func dirOfAngle(rot float64) string {
	r := math.Mod(math.Mod(rot, 360)+360, 360)
	switch {
	case math.Abs(r) < 1 || math.Abs(r-360) < 1:
		return "right"
	case math.Abs(r-90) < 1:
		return "up"
	case math.Abs(r-180) < 1:
		return "left"
	case math.Abs(r-270) < 1:
		return "down"
	}
	return ""
}

// dirFromBox infers a pin's outward direction from the nearest bbox edge.
func dirFromBox(p Pt, b Box) string {
	if !b.Valid() {
		return ""
	}
	d := map[string]float64{
		"left": math.Abs(p.X - b.MinX), "right": math.Abs(p.X - b.MaxX),
		"down": math.Abs(p.Y - b.MinY), "up": math.Abs(p.Y - b.MaxY),
	}
	best, bv := "", math.Inf(1)
	for _, k := range []string{"left", "right", "down", "up"} {
		if d[k] < bv {
			best, bv = k, d[k]
		}
	}
	return best
}

// flagBodyRotation is the stored-rotation truth table of
// .agents/skills/pcbpilot/references/orientation.json (frozenTable). A test
// asserts the copy has not drifted.
var flagBodyRotation = map[string]map[string]float64{
	"power":  {"up": 0, "left": 90, "down": 180, "right": 270},
	"ground": {"up": 180, "left": 270, "down": 0, "right": 90},
	"port":   {"up": 90, "left": 180, "down": 270, "right": 0},
}

func flagDirOf(family string, rot float64) string {
	t := flagBodyRotation[family]
	r := math.Mod(math.Mod(rot, 360)+360, 360)
	for d, v := range t {
		if math.Abs(v-r) < 1 {
			return d
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// marker box prediction (same constants as connect_pin / sch check:
// schematic-wiring.md "标签 stagger 用真实 marker bbox 预测")

func netTextLen(net string) float64 { return 6 * float64(len([]rune(net))) }

// predictMarkerBox returns symbol ∪ text band of a marker whose anchor is at
// a and whose body points dir.
func predictMarkerBox(kind, net string, a Pt, dir string) Box {
	if dir == "" {
		dir = "right"
	}
	var along, across float64 // body length along dir, width across
	switch kind {
	case KindGround:
		along, across = 21, 10
	case KindPower:
		along, across = 11, 6
	case KindNetPort:
		along, across = math.Max(31, netTextLen(net)+8), 11
	default: // net label: text sits on the wire, starting at the anchor
		along, across = math.Max(12, netTextLen(net)), 12
	}
	v := dirVec[dir]
	var b Box
	if v.X != 0 {
		x0, x1 := a.X, a.X+v.X*along
		b = Box{math.Min(x0, x1), a.Y - across/2, math.Max(x0, x1), a.Y + across/2}
		if kind == KindNetLabel {
			b = Box{math.Min(x0, x1), a.Y, math.Max(x0, x1), a.Y + across}
		}
	} else {
		y0, y1 := a.Y, a.Y+v.Y*along
		b = Box{a.X - across/2, math.Min(y0, y1), a.X + across/2, math.Max(y0, y1)}
	}
	if kind == KindPower || kind == KindGround {
		// net name band: 6/char × 12 beyond the symbol end, centred
		tl := netTextLen(net)
		end := Pt{a.X + v.X*along, a.Y + v.Y*along}
		var t Box
		if v.X != 0 {
			t = Box{math.Min(end.X, end.X+v.X*tl), end.Y - 6, math.Max(end.X, end.X+v.X*tl), end.Y + 6}
		} else {
			t = Box{end.X - tl/2, math.Min(end.Y, end.Y+v.Y*12), end.X + tl/2, math.Max(end.Y, end.Y+v.Y*12)}
		}
		b = b.Union(t)
	}
	return b
}

// ---------------------------------------------------------------------------
// net classification

// IsGroundNet / IsPowerNet classify a net by name (soft; used for flow and
// label-policy exemptions only, never for connectivity).
func IsGroundNet(n string) bool {
	u := strings.ToUpper(strings.TrimSpace(n))
	return u == "GND" || u == "VSS" || u == "AGND" || u == "DGND" || u == "PGND" || u == "GNDA" || u == "GNDD" ||
		strings.HasPrefix(u, "GND_") || strings.HasSuffix(u, "_GND") || u == "EARTH" || u == "PE" || u == "0V"
}

func IsPowerNet(n string) bool {
	u := strings.ToUpper(strings.TrimSpace(n))
	if u == "" || IsGroundNet(u) {
		return false
	}
	if strings.HasPrefix(u, "+") || strings.HasPrefix(u, "-") {
		return true
	}
	for _, p := range []string{"VCC", "VDD", "VBUS", "VSYS", "VBAT", "VIN", "VOUT", "VREF", "AVDD", "DVDD", "VEE", "VPP", "V3V3", "3V3", "5V", "1V8", "12V", "24V"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// small numeric helpers

func ramp(v, good, bad float64) float64 {
	if good == bad {
		if v <= good {
			return 100
		}
		return 0
	}
	t := (v - good) / (bad - good)
	if t <= 0 {
		return 100
	}
	if t >= 1 {
		return 0
	}
	return 100 * (1 - t)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func onGrid(v, g float64) bool {
	if g <= 0 {
		return true
	}
	return math.Abs(v-math.Round(v/g)*g) < 1e-3
}

func meanStd(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	m := s / float64(len(xs))
	v := 0.0
	for _, x := range xs {
		v += (x - m) * (x - m)
	}
	return m, math.Sqrt(v / float64(len(xs)))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func manhattan(a, b Pt) float64 { return math.Abs(a.X-b.X) + math.Abs(a.Y-b.Y) }

// rmstManhattan is the rectilinear minimum spanning tree length of pts
// (Prim, O(n²)); RSMT ≥ 2/3·RMST (Hwang 1976), so ratios against it are a
// conservative detour measure.
func rmstManhattan(pts []Pt) float64 {
	n := len(pts)
	if n < 2 {
		return 0
	}
	in := make([]bool, n)
	d := make([]float64, n)
	for i := range d {
		d[i] = math.Inf(1)
	}
	d[0] = 0
	total := 0.0
	for k := 0; k < n; k++ {
		u := -1
		for i := 0; i < n; i++ {
			if !in[i] && (u < 0 || d[i] < d[u]) {
				u = i
			}
		}
		in[u] = true
		total += d[u]
		for i := 0; i < n; i++ {
			if !in[i] {
				if m := manhattan(pts[u], pts[i]); m < d[i] {
					d[i] = m
				}
			}
		}
	}
	return total
}
