package boardmanual

import (
	"encoding/base64"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
)

// MechSection is "机械尺寸与安装孔": computed from the dump's outline, holes,
// vias and rules; heights, plating and mating directions from the notes.
type MechSection struct {
	WidthMM, HeightMM float64
	Corners           string // corner radius / chamfer text
	Pitch             string // mounting-hole pitch text
	Holes             []HoleRow
	Vias              []ViaRow
	MinTrackMil       float64
	MinSpaceMil       float64
	RuleTrackMinMil   float64
	ConnPos           []ConnPosRow
	Heights           []HeightNote
	Thickness         string
	Layers            int
	Notes             []string
	SVG               string
	Scale             string
	SVGDataURI        string
	CSVDataURI        string
}

// HoleRow is one row of the hole table (mm).
type HoleRow struct {
	Ref, Function     string
	X, Y, Drill, Ring float64
	Plated, Net       string
	KeepOut           float64
	Screw             string
	xm, ym            float64 // mil
}

// ViaRow is one via type.
type ViaRow struct {
	DrillMM, PadMM float64
	Count          int
}

// ConnPosRow is one connector position (mm from the lower-left corner).
type ConnPosRow struct {
	Ref                          string
	Pin1X, Pin1Y, BodyX, BodyY   float64
	Rotation                     float64
	Edge                         string
	EdgeDist, Pin1Dist, Overhang float64
	Mating                       string
}

func (c *ctx) mm(x, y float64) (float64, float64) {
	return round2((x - c.ob.MinX) * MilToMM), round2((y - c.ob.MinY) * MilToMM)
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// screwFor suggests the metric screw of a clearance hole (ISO 273 fine–medium).
func screwFor(d float64) string {
	switch {
	case d >= 2.2 && d <= 2.5:
		return "M2"
	case d >= 2.7 && d <= 3.0:
		return "M2.5"
	case d >= 3.2 && d <= 3.6:
		return "M3"
	case d >= 4.3 && d <= 4.8:
		return "M4"
	case d >= 5.3 && d <= 5.8:
		return "M5"
	}
	return "—"
}

// corners describes the outline corners: radius or chamfer of the lower-left one.
func (c *ctx) corners() string {
	pts := c.in.Board.Outline.Points
	if len(pts) < 3 {
		return "—"
	}
	ob := c.ob
	// distance along the bottom edge from the corner to the first point on it
	best := math.Inf(1)
	inner := 0
	for _, p := range pts {
		if len(p) < 2 {
			continue
		}
		if math.Abs(p[1]-ob.MinY) < 0.5 {
			best = math.Min(best, p[0]-ob.MinX)
		}
		if p[0]-ob.MinX < 200 && p[1]-ob.MinY < 200 && p[0]-ob.MinX > 0.5 && p[1]-ob.MinY > 0.5 {
			inner++
		}
	}
	r := best * MilToMM
	switch {
	case math.IsInf(best, 1) || r < 0.05:
		return c.t("cornerSharp")
	case inner >= 2:
		return fmt.Sprintf(c.t("cornerRadius"), trimNum(round2(r)))
	default:
		return fmt.Sprintf(c.t("cornerChamfer"), trimNum(round2(r)))
	}
}

var thickRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*mm`)

func (c *ctx) buildMech() {
	b := c.in.Board
	ms := &c.m.Mech
	mn := c.notes.Mechanical
	if mn == nil {
		mn = &MechNotes{}
	}
	ms.WidthMM, ms.HeightMM = round2(c.ob.W()*MilToMM), round2(c.ob.H()*MilToMM)
	ms.Corners = c.corners()
	ms.Layers = b.CopperLayers
	ms.Heights = mn.Heights
	ms.Notes = mn.Notes
	switch {
	case mn.ThicknessMm > 0:
		ms.Thickness = trimNum(mn.ThicknessMm) + " mm"
	case c.in.Intent != nil && c.in.Intent.Copper != nil:
		if m := thickRe.FindStringSubmatch(c.in.Intent.Copper.Stackup); m != nil {
			ms.Thickness = m[1] + " mm（intent: " + c.in.Intent.Copper.Stackup + "）"
		}
	}
	// holes: mounting holes, then footprint holes of parts
	hn := mn.MountingHole
	if hn == nil {
		hn = &HoleNote{}
	}
	keep := func(x, y float64) float64 {
		for _, r := range b.Copper.Regions {
			if r.BBox.MinX <= x && x <= r.BBox.MaxX && r.BBox.MinY <= y && y <= r.BBox.MaxY {
				for _, n := range r.RuleTypeNames {
					if n == "no-components" {
						return round1(math.Min(r.BBox.W(), r.BBox.H()) * MilToMM)
					}
				}
			}
		}
		return 0
	}
	mh := b.MountHoles()
	sort.SliceStable(mh, func(i, j int) bool {
		if math.Abs(mh[i].Y-mh[j].Y) > 1 {
			return mh[i].Y < mh[j].Y
		}
		return mh[i].X < mh[j].X
	})
	for i, h := range mh {
		x, y := c.mm(h.X, h.Y)
		d := round1(h.Dia * MilToMM)
		row := HoleRow{Ref: fmt.Sprintf("MH%d", i+1), Function: firstNonEmpty(hn.Function, c.t("mountHole")), X: x, Y: y, Drill: d,
			Plated: firstNonEmpty(hn.Plated, c.t("platedUnknown")), Net: "—", KeepOut: keep(h.X, h.Y), Screw: firstNonEmpty(hn.Screw, screwFor(d)), xm: h.X, ym: h.Y}
		if h.From == "fill" {
			row.Ring = d
		}
		if h.From != "fill" && h.From != "hole" {
			row.Ref = h.From
		}
		ms.Holes = append(ms.Holes, row)
	}
	for _, h := range b.FootprintHoles {
		if h.Owner == "" || refPrefix(h.Owner) == "H" || refPrefix(h.Owner) == "MH" {
			continue
		}
		x, y := c.mm(h.X, h.Y)
		ms.Holes = append(ms.Holes, HoleRow{Ref: h.Owner, Function: c.t("pegHole"), X: x, Y: y, Drill: round1(h.Dia * MilToMM),
			Plated: c.t("platedUnknown"), Net: "—", Screw: "—", xm: h.X, ym: h.Y})
	}
	// vias
	vt := map[[2]float64]int{}
	for _, v := range b.Copper.Vias {
		vt[[2]float64{round2(v.HoleDiameter * MilToMM), round2(v.Diameter * MilToMM)}]++
	}
	for k, n := range vt {
		ms.Vias = append(ms.Vias, ViaRow{DrillMM: k[0], PadMM: k[1], Count: n})
	}
	sort.Slice(ms.Vias, func(i, j int) bool { return ms.Vias[i].Count > ms.Vias[j].Count })
	ms.MinTrackMil = math.Inf(1)
	for _, l := range b.Copper.Lines {
		if l.LineWidth > 0 {
			ms.MinTrackMil = math.Min(ms.MinTrackMil, l.LineWidth)
		}
	}
	if math.IsInf(ms.MinTrackMil, 1) {
		ms.MinTrackMil = 0
	}
	ms.MinSpaceMil = math.Min(b.Rules.ClearanceMil, nonZero(b.Rules.TrackTrackMil, b.Rules.ClearanceMil))
	ms.RuleTrackMinMil = b.Rules.TrackWidthMinMil
	// connector positions
	for _, cc := range c.m.Connectors {
		p := cc.part
		bb := p.Box()
		row := ConnPosRow{Ref: cc.Ref, Rotation: p.Rotation, Mating: mn.Mating[cc.Ref]}
		row.BodyX, row.BodyY = c.mm(bb.CX(), bb.CY())
		d := map[string]float64{"L": bb.MinX - c.ob.MinX, "R": c.ob.MaxX - bb.MaxX, "B": bb.MinY - c.ob.MinY, "T": c.ob.MaxY - bb.MaxY}
		edge := "L"
		for _, s := range []string{"R", "T", "B"} {
			if d[s] < d[edge] {
				edge = s
			}
		}
		row.Edge = c.t(map[string]string{"L": "edgeL", "R": "edgeR", "T": "edgeT", "B": "edgeB"}[edge])
		row.EdgeDist = round2(d[edge] * MilToMM)
		if row.EdgeDist < 0 {
			row.Overhang = -row.EdgeDist
		}
		if pd := p.Pad("1"); pd != nil {
			row.Pin1X, row.Pin1Y = c.mm(pd.X, pd.Y)
			pe := map[string]float64{"L": pd.X - c.ob.MinX, "R": c.ob.MaxX - pd.X, "B": pd.Y - c.ob.MinY, "T": c.ob.MaxY - pd.Y}[edge]
			row.Pin1Dist = round2(pe * MilToMM)
		}
		ms.ConnPos = append(ms.ConnPos, row)
	}
	ms.SVG, ms.Scale = c.mechSVG()
	ms.SVGDataURI = "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(`<?xml version="1.0" encoding="UTF-8"?>`+"\n"+ms.SVG))
	ms.CSVDataURI = "data:text/csv;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(c.mechCSV()))
}

func nonZero(v, d float64) float64 {
	if v > 0 {
		return v
	}
	return d
}

// mechCSV is the hole and connector tables for mechanical engineers.
func (c *ctx) mechCSV() string {
	var w strings.Builder
	w.WriteString("\ufefftable,ref,function,x_mm,y_mm,drill_mm,ring_mm,plated,net,keepout_mm,screw\n")
	for _, h := range c.m.Mech.Holes {
		fmt.Fprintf(&w, "hole,%s,%q,%s,%s,%s,%s,%q,%s,%s,%s\n", h.Ref, h.Function, trimNum(h.X), trimNum(h.Y), trimNum(h.Drill), trimNum(h.Ring), h.Plated, h.Net, trimNum(h.KeepOut), h.Screw)
	}
	w.WriteString("table,ref,pin1_x_mm,pin1_y_mm,body_x_mm,body_y_mm,rotation,edge,edge_dist_mm,pin1_edge_mm,overhang_mm,mating\n")
	for _, r := range c.m.Mech.ConnPos {
		fmt.Fprintf(&w, "connector,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%q\n", r.Ref, trimNum(r.Pin1X), trimNum(r.Pin1Y), trimNum(r.BodyX), trimNum(r.BodyY), trimNum(r.Rotation), r.Edge, trimNum(r.EdgeDist), trimNum(r.Pin1Dist), trimNum(r.Overhang), r.Mating)
	}
	return w.String()
}

// mechSVG is the dimensioned outline drawing in real millimetres (width /
// height attributes in mm, so it prints 1:1 when it fits A4).
func (c *ctx) mechSVG() (string, string) {
	ms := &c.m.Mech
	ob := c.ob
	conns := c.m.Connectors
	const dimGap = 6.0
	left, right, top, bottom := 22.0, 22.0, 16.0, 24.0
	v := newView(ob, left, top, right, bottom)
	scale := "1:1"
	const a4W = 186.0 // printable width of A4 portrait, 12 mm margins
	if v.vbW > a4W {
		scale = fmt.Sprintf("1:%.2g", v.vbW/a4W)
	}
	var w strings.Builder
	fmt.Fprintf(&w, `<svg xmlns="http://www.w3.org/2000/svg" width="%smm" height="%smm" viewBox="0 0 %s %s" font-family="sans-serif">`,
		f2s(v.vbW), f2s(v.vbH), f2s(v.vbW), f2s(v.vbH))
	w.WriteString(`<style>.o{fill:#f7faf5;stroke:#000;stroke-width:.3}.d{stroke:#c00;stroke-width:.15;fill:none}.e{stroke:#c00;stroke-width:.1;stroke-dasharray:.6 .4;fill:none}` +
		`.t{font-size:2px;fill:#c00}.tc{font-size:2px;fill:#c00;text-anchor:middle}.h{fill:#fff;stroke:#000;stroke-width:.2}.k{fill:none;stroke:#888;stroke-width:.12;stroke-dasharray:.5 .4}` +
		`.c{fill:#ffe9c7;stroke:#d07000;stroke-width:.2}.cl{font-size:1.8px;fill:#7a4100}.p1{fill:#fff;stroke:#000;stroke-width:.15}.s{stroke:#000;stroke-width:.35}.st{font-size:2px;fill:#000}</style>`)
	bl, br, bt, bb := v.left, v.left+v.wmm, v.top, v.top+v.hmm
	// outline
	if pts := c.in.Board.Outline.Points; len(pts) >= 3 {
		w.WriteString(`<path class="o" d="`)
		for i, p := range pts {
			if len(p) < 2 {
				continue
			}
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			fmt.Fprintf(&w, "%s%s %s ", cmd, f2s(v.x(p[0])), f2s(v.y(p[1])))
		}
		w.WriteString(`Z"/>`)
	}
	// connectors
	for _, cc := range conns {
		bx := cc.part.Box()
		fmt.Fprintf(&w, `<rect class="c" x="%s" y="%s" width="%s" height="%s"/>`, f2s(v.x(bx.MinX)), f2s(v.y(bx.MaxY)), f2s(bx.W()*MilToMM), f2s(bx.H()*MilToMM))
		fmt.Fprintf(&w, `<text class="cl" x="%s" y="%s" text-anchor="middle">%s</text>`, f2s(v.x(bx.CX())), f2s(v.y(bx.CY())+0.6), esc(cc.Ref))
		if pd := cc.part.Pad("1"); pd != nil {
			fmt.Fprintf(&w, `<circle class="p1" cx="%s" cy="%s" r=".45"/>`, f2s(v.x(pd.X)), f2s(v.y(pd.Y)))
		}
	}
	// holes + keep-outs
	for _, h := range ms.Holes {
		fmt.Fprintf(&w, `<circle class="h" cx="%s" cy="%s" r="%s"/>`, f2s(v.x(h.xm)), f2s(v.y(h.ym)), f2s(h.Drill/2))
		if h.KeepOut > 0 {
			fmt.Fprintf(&w, `<circle class="k" cx="%s" cy="%s" r="%s"/>`, f2s(v.x(h.xm)), f2s(v.y(h.ym)), f2s(h.KeepOut/2))
		}
	}
	hdim := func(x1, x2, y float64, label string, ext1, ext2 float64) {
		fmt.Fprintf(&w, `<path class="e" d="M%s %s V%s M%s %s V%s"/>`, f2s(x1), f2s(ext1), f2s(y), f2s(x2), f2s(ext2), f2s(y))
		fmt.Fprintf(&w, `<path class="d" d="M%s %s H%s M%s %s l1 -.5 v1 z M%s %s l-1 -.5 v1 z"/>`, f2s(x1), f2s(y), f2s(x2), f2s(x1), f2s(y), f2s(x2), f2s(y))
		fmt.Fprintf(&w, `<text class="tc" x="%s" y="%s">%s</text>`, f2s((x1+x2)/2), f2s(y-0.6), esc(label))
	}
	vdim := func(y1, y2, x float64, label string, ext1, ext2 float64) {
		fmt.Fprintf(&w, `<path class="e" d="M%s %s H%s M%s %s H%s"/>`, f2s(ext1), f2s(y1), f2s(x), f2s(ext2), f2s(y2), f2s(x))
		fmt.Fprintf(&w, `<path class="d" d="M%s %s V%s M%s %s l-.5 1 h1 z M%s %s l-.5 -1 h1 z"/>`, f2s(x), f2s(y1), f2s(y2), f2s(x), f2s(y1), f2s(x), f2s(y2))
		tx, ty := x-0.6, (y1+y2)/2
		fmt.Fprintf(&w, `<text class="tc" x="%s" y="%s" transform="rotate(-90 %s %s)">%s</text>`, f2s(tx), f2s(ty), f2s(tx), f2s(ty), esc(label))
	}
	// overall size
	hdim(bl, br, bb+dimGap*2.2, trimNum(ms.WidthMM)+" mm", bb, bb)
	vdim(bt, bb, bl-dimGap*2.2, trimNum(ms.HeightMM)+" mm", bl, bl)
	// mounting-hole positions (from the origin) and pitch
	var mount []HoleRow
	for _, h := range ms.Holes {
		if h.Function == firstNonEmpty(c.notesHoleFunc(), c.t("mountHole")) {
			mount = append(mount, h)
		}
	}
	xs, ys := map[float64]bool{}, map[float64]bool{}
	for _, h := range mount {
		hx, hy := v.x(h.xm), v.y(h.ym)
		if !xs[h.X] {
			xs[h.X] = true
			hdim(bl, hx, bb+dimGap, trimNum(h.X), bb, hy+h.Drill/2)
		}
		if !ys[h.Y] {
			ys[h.Y] = true
			vdim(hy, bb, bl-dimGap, trimNum(h.Y), hx-h.Drill/2, bl)
		}
	}
	// hole-to-hole pitch
	if len(mount) >= 2 {
		minX, maxX, minY, maxY := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
		var a, b2, cc, d HoleRow
		for _, h := range mount {
			if h.X < minX {
				minX, a = h.X, h
			}
			if h.X > maxX {
				maxX, b2 = h.X, h
			}
			if h.Y < minY {
				minY, cc = h.Y, h
			}
			if h.Y > maxY {
				maxY, d = h.Y, h
			}
		}
		var parts []string
		if maxX-minX > 0.5 {
			hdim(v.x(a.xm), v.x(b2.xm), bb+dimGap*1.6, c.t("pitch")+" "+trimNum(round2(maxX-minX)), v.y(a.ym)+a.Drill/2, v.y(b2.ym)+b2.Drill/2)
			parts = append(parts, "X "+trimNum(round2(maxX-minX))+" mm")
		}
		if maxY-minY > 0.5 {
			vdim(v.y(d.ym), v.y(cc.ym), bl-dimGap*1.6, c.t("pitch")+" "+trimNum(round2(maxY-minY)), v.x(d.xm)-d.Drill/2, v.x(cc.xm)-cc.Drill/2)
			parts = append(parts, "Y "+trimNum(round2(maxY-minY))+" mm")
		}
		ms.Pitch = strings.Join(parts, "，")
	}
	// connector body distance to its nearest edge
	for _, r := range ms.ConnPos {
		var cc *Conn
		for _, k := range conns {
			if k.Ref == r.Ref {
				cc = k
			}
		}
		bx := cc.part.Box()
		lab := trimNum(r.EdgeDist)
		if r.Overhang > 0 {
			lab = "+" + trimNum(r.Overhang) + c.t("overhangShort")
		}
		switch r.Edge {
		case c.t("edgeL"):
			y := v.y(bx.CY())
			fmt.Fprintf(&w, `<path class="d" d="M%s %s H%s"/><text class="t" x="%s" y="%s" text-anchor="end">%s %s</text>`, f2s(bl), f2s(y), f2s(v.x(bx.MinX)), f2s(bl-1), f2s(y+0.7), esc(r.Ref), esc(lab))
		case c.t("edgeR"):
			y := v.y(bx.CY())
			fmt.Fprintf(&w, `<path class="d" d="M%s %s H%s"/><text class="t" x="%s" y="%s">%s %s</text>`, f2s(v.x(bx.MaxX)), f2s(y), f2s(br), f2s(br+1), f2s(y+0.7), esc(r.Ref), esc(lab))
		case c.t("edgeT"):
			x := v.x(bx.CX())
			fmt.Fprintf(&w, `<path class="d" d="M%s %s V%s"/><text class="tc" x="%s" y="%s">%s %s</text>`, f2s(x), f2s(bt), f2s(v.y(bx.MaxY)), f2s(x), f2s(bt-1.5), esc(r.Ref), esc(lab))
		default:
			x := v.x(bx.CX())
			fmt.Fprintf(&w, `<path class="d" d="M%s %s V%s"/><text class="tc" x="%s" y="%s">%s %s</text>`, f2s(x), f2s(v.y(bx.MinY)), f2s(bb), f2s(x), f2s(bb+3), esc(r.Ref), esc(lab))
		}
	}
	// origin, axes, scale bar, notes
	fmt.Fprintf(&w, `<circle cx="%s" cy="%s" r=".7" fill="#000"/><path class="s" d="M%s %s h5 M%s %s v-5"/>`, f2s(bl), f2s(bb), f2s(bl), f2s(bb), f2s(bl), f2s(bb))
	fmt.Fprintf(&w, `<text class="st" x="%s" y="%s">X →</text><text class="st" x="%s" y="%s">Y ↑</text><text class="st" x="%s" y="%s">(0,0)</text>`,
		f2s(bl+5.5), f2s(bb+0.7), f2s(bl+0.6), f2s(bb-5.5), f2s(bl-6), f2s(bb+3.2))
	sy := bb + dimGap*2.2 + 6
	fmt.Fprintf(&w, `<path class="s" d="M%s %s h10 M%s %s v-1 M%s %s v-.7 M%s %s v-1"/><text class="st" x="%s" y="%s">10 mm</text>`,
		f2s(bl), f2s(sy), f2s(bl), f2s(sy), f2s(bl+5), f2s(sy), f2s(bl+10), f2s(sy), f2s(bl+11), f2s(sy+0.7))
	fmt.Fprintf(&w, `<text class="st" x="%s" y="%s">%s</text>`, f2s(bl+24), f2s(sy+0.7),
		esc(fmt.Sprintf(c.t("mechCaption"), trimNum(ms.WidthMM), trimNum(ms.HeightMM), ms.Corners, scale)))
	w.WriteString(`</svg>`)
	return w.String(), scale
}

func (c *ctx) notesHoleFunc() string {
	if mn := c.notes.Mechanical; mn != nil && mn.MountingHole != nil {
		return mn.MountingHole.Function
	}
	return ""
}
