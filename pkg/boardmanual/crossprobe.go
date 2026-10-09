package boardmanual

import (
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Schematic ↔ PCB cross-probe (section 4, KiCad projects): the schematic
// pages and the PCB layers as pictures plus a ref map; the template's
// compositor shows the part's PCB region in a lens next to the symbol
// (and the symbol next to the footprint), with the part's gate results.
//
// Every coordinate of the map is KiCad page millimetres (y down), the frame
// of both kicad-cli SVG plots: board dump mil (y up) → x·0.0254, −y·0.0254.

// CrossProbeInput is what the CLI read from the KiCad project.
type CrossProbeInput struct {
	Pages    []CPPage   // schematic sheet instances, root first
	Symbols  []CPSymbol // one per symbol unit
	Layers   []CPLayer  // PCB layer plots, bottom first
	NotInBOM map[string]bool
}

// CPPage is one plotted schematic page.
type CPPage struct {
	Name string // sheet path, "" = root
	SVG  []byte // nil = kicad-cli plotted no page for it
}

// CPSymbol is one placed symbol unit (page mm, y down).
type CPSymbol struct {
	Ref, Value     string
	Page           int
	MinX, MinY     float64
	MaxX, MaxY     float64
	Rot            float64
	InBOM, OnBoard bool
}

// CPLayer is one PCB layer plot.
type CPLayer struct {
	Name string
	SVG  []byte
}

// CrossProbe is the map the template embeds (JSON, read by the compositor).
type CrossProbe struct {
	Pages  []XPicture `json:"pages"`
	Layers []XPicture `json:"layers"`
	// Board is the board outline box (page mm) the PCB view shows.
	Board [4]float64 `json:"board"`
	Parts []XPart    `json:"parts"`
	// Missing lists every BOM part without both ends (gate items).
	Missing []string `json:"missing"`
}

// XPicture is one embedded SVG and its page size (mm).
type XPicture struct {
	Name  string  `json:"name"`
	URI   string  `json:"uri"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Color string  `json:"color,omitempty"`
}

// XPart is one ref of the map. Boxes are [x, y, w, h] page mm.
type XPart struct {
	Ref       string       `json:"ref"`
	Value     string       `json:"value,omitempty"`
	Footprint string       `json:"footprint,omitempty"`
	LCSC      string       `json:"lcsc,omitempty"`
	Side      string       `json:"side,omitempty"`
	SchPage   int          `json:"schPage"`
	SchBoxes  [][4]float64 `json:"schBoxes"`
	SchRot    float64      `json:"schRot"`
	PCBBox    *[4]float64  `json:"pcbBox"`
	PCBRot    float64      `json:"pcbRot"`
	// Rotation turns the PCB region so the footprint stands as the symbol
	// does (SVG degrees, clockwise): pcbRot − schRot.
	Rotation float64    `json:"rotation"`
	Nets     []XNet     `json:"nets"`
	Findings []XFinding `json:"findings"`
}

// XNet is one net of the part with its intent and IR-drop results.
type XNet struct {
	Name     string  `json:"name"`
	Pins     string  `json:"pins"`
	Role     string  `json:"role,omitempty"`
	WidthMM  float64 `json:"widthMm,omitempty"` // intent outer width
	MinMM    float64 `json:"minMm,omitempty"`
	CurrentA float64 `json:"currentA,omitempty"`
	IRmV     float64 `json:"irMv,omitempty"`
	BudgetMV float64 `json:"budgetMv,omitempty"`
	IRStatus string  `json:"irStatus,omitempty"`
}

// XFinding is one intent / post-layout finding that names the part.
type XFinding struct {
	Source   string `json:"source"`
	Severity string `json:"severity"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
}

// crossProbeColors tints the black-and-white layer plots.
var crossProbeColors = map[string]string{
	"B.Cu": "#4f86e0", "F.Cu": "#e2583e", "F.Silkscreen": "#f4f4f0", "Edge.Cuts": "#f2c94c",
	"B.Silkscreen": "#b9a7e8",
}

var (
	svgViewBoxRe = regexp.MustCompile(`viewBox="\s*([-\d.]+)[\s,]+([-\d.]+)[\s,]+([-\d.]+)[\s,]+([-\d.]+)\s*"`)
	svgTitleRe   = regexp.MustCompile(`(?s)<title>.*?</title>`)
)

// svgPicture embeds one kicad-cli plot (its <title> carries the plot time:
// dropped, so the manual is reproducible).
func svgPicture(name string, svg []byte) (XPicture, error) {
	m := svgViewBoxRe.FindSubmatch(svg)
	if m == nil {
		return XPicture{}, fmt.Errorf("%s: SVG without a viewBox", name)
	}
	w, _ := strconv.ParseFloat(string(m[3]), 64)
	h, _ := strconv.ParseFloat(string(m[4]), 64)
	svg = svgTitleRe.ReplaceAll(svg, nil)
	return XPicture{Name: name, W: w, H: h, URI: "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(svg)}, nil
}

// pageBox converts a dump bbox (mil, y up) to [x, y, w, h] page mm.
func pageBox(b BBox) [4]float64 {
	return [4]float64{round3(b.MinX * MilToMM), round3(-b.MaxY * MilToMM), round3(b.W() * MilToMM), round3(b.H() * MilToMM)}
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// buildCrossProbe fills m.CrossProbe (nil without KiCad input).
func (c *ctx) buildCrossProbe() {
	in := c.in.CrossProbe
	if in == nil {
		return
	}
	xp := &CrossProbe{Board: pageBox(c.ob), Missing: []string{}}
	c.m.CrossProbe = xp
	for _, p := range in.Pages {
		name := p.Name
		if name == "" {
			name = c.t("xp.root")
		}
		if p.SVG == nil {
			xp.Missing = append(xp.Missing, fmt.Sprintf("schematic page %q has no plot", name))
			xp.Pages = append(xp.Pages, XPicture{Name: name})
			continue
		}
		pic, err := svgPicture(name, p.SVG)
		if err != nil {
			xp.Missing = append(xp.Missing, err.Error())
		}
		pic.Name = name
		xp.Pages = append(xp.Pages, pic)
	}
	for _, l := range in.Layers {
		pic, err := svgPicture(l.Name, l.SVG)
		if err != nil {
			xp.Missing = append(xp.Missing, err.Error())
			continue
		}
		pic.Color = crossProbeColors[l.Name]
		xp.Layers = append(xp.Layers, pic)
	}

	parts := map[string]*XPart{}
	get := func(ref string) *XPart {
		p := parts[ref]
		if p == nil {
			p = &XPart{Ref: ref, SchPage: -1, SchBoxes: [][4]float64{}, Nets: []XNet{}, Findings: []XFinding{}}
			parts[ref] = p
		}
		return p
	}
	bom := map[string]bool{}
	for _, s := range in.Symbols {
		p := get(s.Ref)
		if p.SchPage < 0 { // first unit decides the page
			p.SchPage, p.SchRot, p.Value = s.Page, s.Rot, s.Value
		}
		if s.Page == p.SchPage {
			p.SchBoxes = append(p.SchBoxes, [4]float64{round3(s.MinX), round3(s.MinY), round3(s.MaxX - s.MinX), round3(s.MaxY - s.MinY)})
		}
		if s.InBOM && s.OnBoard {
			bom[s.Ref] = true
		}
	}
	b := c.in.Board
	for i := range b.Components {
		bp := &b.Components[i]
		ref := bp.Designator
		if ref == "" || strings.HasPrefix(ref, "#") {
			continue
		}
		// A footprint outside the BOM, or one without a copper pad (a
		// mounting hole placed on the PCB only: NPTH), needs no symbol.
		if !in.NotInBOM[ref] && len(bp.Pads) > 0 {
			bom[ref] = true
		}
		p := get(ref)
		bx := pageBox(bp.Box())
		p.PCBBox, p.PCBRot = &bx, bp.Rotation
		p.Footprint, p.LCSC, p.Side = bp.Footprint, bp.LCSC, bp.Side
		if p.Value == "" {
			p.Value = firstNonEmpty(bp.Value, bp.Device)
		}
		p.Nets = c.partNets(bp)
	}
	refs := make([]string, 0, len(parts))
	for r := range parts {
		refs = append(refs, r)
	}
	sort.Slice(refs, func(i, j int) bool { return natLess(refs[i], refs[j]) })
	for _, r := range refs {
		p := parts[r]
		p.Rotation = math.Mod(p.PCBRot-p.SchRot+720, 360)
		p.Findings = c.partFindings(r, p.Nets)
		if bom[r] {
			switch {
			case p.SchPage < 0:
				xp.Missing = append(xp.Missing, fmt.Sprintf("%s is on the PCB but has no schematic symbol", r))
			case p.PCBBox == nil:
				xp.Missing = append(xp.Missing, fmt.Sprintf("%s is in the schematic BOM but has no footprint on the PCB", r))
			}
		}
		xp.Parts = append(xp.Parts, *p)
	}
}

// partNets lists the part's nets with the pins on each and their intent
// width / current and post-layout IR drop.
func (c *ctx) partNets(bp *Part) []XNet {
	pins := map[string][]string{}
	for _, pd := range bp.Pads {
		if pd.Net != "" {
			pins[pd.Net] = append(pins[pd.Net], pd.PadNumber)
		}
	}
	names := make([]string, 0, len(pins))
	for n := range pins {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return natLess(names[i], names[j]) })
	out := []XNet{}
	for _, n := range names {
		ps := pins[n]
		sort.Slice(ps, func(i, j int) bool { return natLess(ps[i], ps[j]) })
		x := XNet{Name: n, Pins: strings.Join(ps, ",")}
		if it := c.in.Intent; it != nil {
			if np := it.Nets[n]; np != nil {
				x.Role, x.CurrentA = np.Role, round3(np.CurrentA)
				x.WidthMM, x.MinMM = round3(np.WidthMil.Outer*MilToMM), round3(np.WidthMil.Min*MilToMM)
			}
		}
		if po := c.in.Post; po != nil {
			for _, nr := range po.Nets {
				if nr.Net == n {
					x.IRmV, x.BudgetMV, x.IRStatus = round3(nr.WorstMV), round3(nr.BudgetMV), nr.Status
					if x.Role == "" {
						x.Role = nr.Role
					}
				}
			}
		}
		out = append(out, x)
	}
	return out
}

// partFindings are the intent (safety, creepage, ratings …) and
// post-layout findings that name the part, or one of its nets when the
// finding names no part.
func (c *ctx) partFindings(ref string, nets []XNet) []XFinding {
	onNet := map[string]bool{}
	for _, n := range nets {
		onNet[n.Name] = true
	}
	hit := func(refs, ns []string) bool {
		for _, r := range refs {
			if r == ref {
				return true
			}
		}
		if len(refs) > 0 {
			return false
		}
		for _, n := range ns {
			if onNet[n] {
				return true
			}
		}
		return false
	}
	out := []XFinding{}
	if it := c.in.Intent; it != nil {
		for _, f := range it.Findings {
			if f != nil && hit(f.Refs, f.Nets) {
				out = append(out, XFinding{"intent", f.Severity, f.Kind, f.Message})
			}
		}
	}
	if po := c.in.Post; po != nil {
		for _, f := range po.Findings {
			if hit(f.Refs, f.Nets) {
				out = append(out, XFinding{"post-layout", f.Severity, f.Kind, f.Message})
			}
		}
	}
	return out
}
