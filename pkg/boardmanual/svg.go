package boardmanual

import (
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
)

// Role colours of pads (power red, ground black, signal blue, NC grey).
var roleColor = map[string]string{"power": "#d62728", "ground": "#222222", "signal": "#1f5fbf", "nc": "#a8a8a8"}

// ledColor maps a notes colour word to a fill.
func ledColor(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "green") || strings.Contains(s, "绿"):
		return "#2ca02c"
	case strings.Contains(l, "yellow") || strings.Contains(l, "amber") || strings.Contains(s, "黄"):
		return "#e6b800"
	case strings.Contains(l, "red") || strings.Contains(s, "红"):
		return "#d62728"
	case strings.Contains(l, "blue") || strings.Contains(s, "蓝"):
		return "#1f77b4"
	case strings.Contains(l, "white") || strings.Contains(s, "白"):
		return "#f0f0f0"
	}
	return "#bbbbbb"
}

func esc(s string) string { return html.EscapeString(s) }

func f2s(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}

// textW estimates a text width (CJK = 1 em, other = 0.6 em).
func textW(s string, size float64) float64 {
	w := 0.0
	for _, r := range s {
		if r < 0x2E80 {
			w += 0.6
		} else {
			w += 1.0
		}
	}
	return w * size
}

// view maps board mil (y up) to SVG mm (y down) with a margin.
type view struct {
	ob         BBox
	left, top  float64 // margins (mm)
	wmm, hmm   float64 // board size (mm)
	vbW, vbH   float64 // full viewBox (mm)
	bottom, rt float64
}

func newView(ob BBox, left, top, right, bottom float64) view {
	v := view{ob: ob, left: left, top: top, rt: right, bottom: bottom}
	v.wmm, v.hmm = ob.W()*MilToMM, ob.H()*MilToMM
	v.vbW, v.vbH = left+v.wmm+right, top+v.hmm+bottom
	return v
}

func (v view) x(mil float64) float64 { return v.left + (mil-v.ob.MinX)*MilToMM }
func (v view) y(mil float64) float64 { return v.top + (v.ob.MaxY-mil)*MilToMM }

// boardBase draws the outline, holes and every part bbox (light), with the
// connectors highlighted and their pin 1 marked.
func (c *ctx) boardBase(v view, w *strings.Builder, faintConns bool) {
	b := c.in.Board
	// outline
	if len(b.Outline.Points) >= 3 {
		w.WriteString(`<path class="outline" d="`)
		for i, p := range b.Outline.Points {
			if len(p) < 2 {
				continue
			}
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			fmt.Fprintf(w, "%s%s %s ", cmd, f2s(v.x(p[0])), f2s(v.y(p[1])))
		}
		w.WriteString(`Z"/>`)
	} else {
		fmt.Fprintf(w, `<rect class="outline" x="%s" y="%s" width="%s" height="%s"/>`, f2s(v.left), f2s(v.top), f2s(v.wmm), f2s(v.hmm))
	}
	isConn := map[string]bool{}
	for _, cc := range c.m.Connectors {
		isConn[cc.Ref] = true
	}
	refs := make([]string, 0, len(b.Components))
	for _, p := range b.Components {
		refs = append(refs, p.Designator)
	}
	sortNat(refs)
	for _, ref := range refs {
		p := b.Part(ref)
		if isConn[ref] {
			continue
		}
		bb := p.Box()
		fmt.Fprintf(w, `<rect class="part" x="%s" y="%s" width="%s" height="%s"/>`,
			f2s(v.x(bb.MinX)), f2s(v.y(bb.MaxY)), f2s(bb.W()*MilToMM), f2s(bb.H()*MilToMM))
		if bb.W()*MilToMM > 2.2 && bb.H()*MilToMM > 1.2 {
			fmt.Fprintf(w, `<text class="pref" x="%s" y="%s">%s</text>`, f2s(v.x(bb.CX())), f2s(v.y(bb.CY())+0.3), esc(ref))
		}
	}
	for _, h := range b.FootprintHoles {
		fmt.Fprintf(w, `<circle class="hole" cx="%s" cy="%s" r="%s"/>`, f2s(v.x(h.X)), f2s(v.y(h.Y)), f2s(math.Max(h.Dia*MilToMM/2, 0.3)))
	}
	for _, h := range b.MountHoles() {
		if h.From == "hole" {
			continue // drawn above
		}
		fmt.Fprintf(w, `<circle class="mhole" cx="%s" cy="%s" r="%s"/>`, f2s(v.x(h.X)), f2s(v.y(h.Y)), f2s(h.Dia*MilToMM/2))
	}
	cls := "conn"
	if faintConns {
		cls = "conn faint"
	}
	for _, cc := range c.m.Connectors {
		p := cc.part
		bb := p.Box()
		fmt.Fprintf(w, `<rect class="%s" x="%s" y="%s" width="%s" height="%s"/>`, cls,
			f2s(v.x(bb.MinX)), f2s(v.y(bb.MaxY)), f2s(bb.W()*MilToMM), f2s(bb.H()*MilToMM))
		for _, pd := range p.Pads {
			r := math.Max(math.Min(pd.Width, pd.Height)*MilToMM/2, 0.25)
			fmt.Fprintf(w, `<circle class="cpad" cx="%s" cy="%s" r="%s"/>`, f2s(v.x(pd.X)), f2s(v.y(pd.Y)), f2s(r))
		}
		if pd := p.Pad("1"); pd != nil && !faintConns {
			fmt.Fprintf(w, `<circle class="pin1" cx="%s" cy="%s" r="0.55"/>`, f2s(v.x(pd.X)), f2s(v.y(pd.Y)))
		}
	}
}

func svgOpen(w *strings.Builder, x, y, wd, ht float64, title string) {
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="%s %s %s %s" role="img" aria-label="%s">`,
		f2s(x), f2s(y), f2s(wd), f2s(ht), esc(title))
	w.WriteString(`<style>.outline{fill:#eef5ea;stroke:#2f5d3a;stroke-width:.4}` +
		`.part{fill:none;stroke:#c9c9c9;stroke-width:.15}.pref{font:.9px sans-serif;fill:#a0a0a0;text-anchor:middle}` +
		`.hole{fill:#fff;stroke:#555;stroke-width:.25}.mhole{fill:#fff;stroke:#555;stroke-width:.35}.conn{fill:#ffe3b8;stroke:#e07b00;stroke-width:.35}` +
		`.conn.faint{fill:#fff1dc;stroke:#efb366;stroke-width:.2}.cpad{fill:#8a6d1e}.pin1{fill:#e00000}` +
		`.lead{stroke:#e07b00;stroke-width:.25;fill:none}.badge{fill:#e07b00}.bnum{font:bold 2.4px sans-serif;fill:#fff;text-anchor:middle}` +
		`.clab{font:2.6px sans-serif;fill:#222}.edge{font:2.4px sans-serif;fill:#777;text-anchor:middle}` +
		`.scale{stroke:#222;stroke-width:.35}.stext{font:2.2px sans-serif;fill:#222}` +
		`.probe{fill:#0a8f3c;stroke:#fff;stroke-width:.2}.pnum{font:bold 1.9px sans-serif;fill:#fff;text-anchor:middle}` +
		`.lled{stroke:#333;stroke-width:.15}.llab{font:1.6px sans-serif;fill:#222}` +
		`.mtitle{font:bold 3.6px sans-serif;fill:#111;text-anchor:middle}.ctitle{font:bold 3px sans-serif}.csub{font:2.1px sans-serif;fill:#333}` +
		`.p1{font:bold 1.5px sans-serif;fill:#222}.hl{fill:#e8d38a;fill-opacity:.6;stroke:#b8962e;stroke-width:.3}` +
		`.hltext{font:bold 2.6px sans-serif;fill:#5a4500;text-anchor:middle}.ledlead{stroke:#b8962e;stroke-width:.2}</style>`)
}

// calloutItem is one label placed in a margin.
type calloutItem struct {
	cc      *Conn
	side    string
	want    float64 // desired coordinate along the side (mm, SVG)
	extent  float64 // size along the side
	pos     float64
	ax, ay  float64 // leader anchor (connector)
	label   string
	textLen float64
}

// spread places items along one axis without overlap, inside [lo, hi].
func spread(items []*calloutItem, lo, hi, gap float64) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].want < items[j].want })
	for i, it := range items {
		it.pos = math.Max(it.want, lo+it.extent/2)
		if i > 0 {
			prev := items[i-1]
			it.pos = math.Max(it.pos, prev.pos+(prev.extent+it.extent)/2+gap)
		}
	}
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		limit := hi - it.extent/2
		if i < len(items)-1 {
			next := items[i+1]
			limit = math.Min(limit, next.pos-(next.extent+it.extent)/2-gap)
		}
		it.pos = math.Min(it.pos, limit)
	}
}

const (
	calloutFont = 2.6
	badgeR      = 2.2
)

const (
	labFont = 3.0
	subFont = 2.1
)

// boardSVG is the connector map: square canvas, every connector filled in
// its role colour with a "REF ROLE name" label and a subtitle, leader lines
// to the nearest board side, pin 1 marks, part labels, the LED row labelled
// below the board, mounting holes and a 10 mm scale bar.
func (c *ctx) boardSVG() string {
	ob := c.ob
	wmm, hmm := ob.W()*MilToMM, ob.H()*MilToMM
	items := map[string][]*calloutItem{}
	maxL, maxR := 0.0, 0.0
	type lines struct{ title, sub string }
	text := map[*calloutItem]lines{}
	for _, cc := range c.m.Connectors {
		bb := cc.part.Box()
		cx, cy := bb.CX(), bb.CY()
		d := map[string]float64{"L": cx - ob.MinX, "R": ob.MaxX - cx, "B": cy - ob.MinY, "T": ob.MaxY - cy}
		side := "L"
		for _, s := range []string{"R", "T", "B"} {
			if d[s] < d[side] {
				side = s
			}
		}
		title := strings.TrimSpace(cc.Ref + " " + cc.Role + " " + cc.Name)
		if cc.Role != "" && strings.Contains(strings.ToUpper(cc.Name), cc.Role) {
			title = strings.TrimSpace(cc.Ref + " " + cc.Name)
		}
		it := &calloutItem{cc: cc, side: side, label: title}
		it.textLen = math.Max(textW(title, labFont)+2*badgeR+1, textW(cc.Subtitle, subFont))
		text[it] = lines{title, cc.Subtitle}
		items[side] = append(items[side], it)
		switch side {
		case "L":
			maxL = math.Max(maxL, it.textLen)
		case "R":
			maxR = math.Max(maxR, it.textLen)
		}
	}
	// LEDs near the bottom edge are labelled below the board.
	type ledLab struct {
		x, y  float64 // mil
		label string
		color string
	}
	var bottomLEDs, inPlace []ledLab
	maxLED := 0.0
	for _, r := range c.m.LEDs {
		p := c.in.Board.Part(r.Ref)
		if p == nil {
			continue
		}
		bb := p.Box()
		l := ledLab{bb.CX(), bb.CY(), strings.TrimSpace(r.Ref + " " + r.Name), ledColor(r.Color)}
		if (bb.CY()-ob.MinY)*MilToMM < 0.15*hmm {
			bottomLEDs = append(bottomLEDs, l)
			maxLED = math.Max(maxLED, textW(l.label, 1.8))
		} else {
			inPlace = append(inPlace, l)
		}
	}
	top := 16.0 // title + top callouts
	if len(items["T"]) > 0 {
		top = 22
	}
	bottom := 22.0
	if len(bottomLEDs) > 0 {
		bottom += maxLED + 6
	}
	if len(items["B"]) > 0 {
		bottom += 10
	}
	left, right := math.Max(10, maxL+10), math.Max(10, maxR+10)
	// square canvas: centre the board in the larger dimension
	W, H := left+wmm+right, top+hmm+bottom
	if W > H {
		top += (W - H) / 2
		bottom += (W - H) / 2
	} else {
		d := (H - W) / 2
		left += d
		right += d
	}
	v := newView(ob, left, top, right, bottom)
	bl, br, bt, bbm := v.left, v.left+v.wmm, v.top, v.top+v.hmm
	ledBand := 0.0
	if len(bottomLEDs) > 0 {
		ledBand = maxLED + 6
	}
	for side, list := range items {
		for _, it := range list {
			bb := it.cc.part.Box()
			switch side {
			case "L":
				it.ax, it.ay = v.x(bb.CX()), v.y(bb.CY())
				it.want, it.extent = it.ay, 7.5
			case "R":
				it.ax, it.ay = v.x(bb.CX()), v.y(bb.CY())
				it.want, it.extent = it.ay, 7.5
			default:
				it.ax, it.ay = v.x(bb.CX()), v.y(bb.CY())
				it.want, it.extent = it.ax, it.textLen
			}
		}
		if side == "L" || side == "R" {
			spread(list, 2, v.vbH-2, 1)
		} else {
			spread(list, 2, v.vbW-2, 3)
		}
	}
	var w strings.Builder
	svgOpen(&w, 0, 0, v.vbW, v.vbH, "connector map")
	fmt.Fprintf(&w, `<rect x="0" y="0" width="%s" height="%s" fill="#fff"/>`, f2s(v.vbW), f2s(v.vbH))
	fmt.Fprintf(&w, `<text class="mtitle" x="%s" y="%s">%s</text>`, f2s(v.vbW/2), f2s(bt-17),
		esc(c.m.Title+" · "+fmt.Sprintf(c.t("mapTitle"), trimNum(c.m.WidthMM), trimNum(c.m.HeightMM))))
	c.boardBase(v, &w, true)
	// part labels (notes partLabels, else the part with the most pads)
	labelsMap := c.notes.PartLabels
	if len(labelsMap) == 0 {
		var big *Part
		for i := range c.in.Board.Components {
			p := &c.in.Board.Components[i]
			if !IsConnector(p) && (big == nil || len(p.Pads) > len(big.Pads)) {
				big = p
			}
		}
		if big != nil && len(big.Pads) >= 16 {
			labelsMap = map[string]string{big.Designator: ""}
		}
	}
	refs := make([]string, 0, len(labelsMap))
	for r := range labelsMap {
		refs = append(refs, r)
	}
	sortNat(refs)
	for _, ref := range refs {
		p := c.in.Board.Part(ref)
		if p == nil {
			continue
		}
		bb := p.Box()
		fmt.Fprintf(&w, `<rect class="hl" x="%s" y="%s" width="%s" height="%s"/>`, f2s(v.x(bb.MinX)), f2s(v.y(bb.MaxY)), f2s(bb.W()*MilToMM), f2s(bb.H()*MilToMM))
		fmt.Fprintf(&w, `<text class="hltext" x="%s" y="%s">%s</text>`, f2s(v.x(bb.CX())), f2s(v.y(bb.CY())+1), esc(strings.TrimSpace(ref+" "+labelsMap[ref])))
	}
	// connectors filled in role colour, pin 1 = white dot + "1"
	for _, cc := range c.m.Connectors {
		bb := cc.part.Box()
		fmt.Fprintf(&w, `<rect x="%s" y="%s" width="%s" height="%s" fill="%s" fill-opacity=".35" stroke="%s" stroke-width=".5"/>`,
			f2s(v.x(bb.MinX)), f2s(v.y(bb.MaxY)), f2s(bb.W()*MilToMM), f2s(bb.H()*MilToMM), cc.Color, cc.Color)
		if pd := cc.part.Pad("1"); pd != nil {
			x, y := v.x(pd.X), v.y(pd.Y)
			fmt.Fprintf(&w, `<circle cx="%s" cy="%s" r="0.75" fill="#fff" stroke="#222" stroke-width=".25"/>`, f2s(x), f2s(y))
			fmt.Fprintf(&w, `<text class="p1" x="%s" y="%s">1</text>`, f2s(x+0.9), f2s(y-0.9))
		}
	}
	// LEDs
	for _, l := range inPlace {
		x, y := v.x(l.x), v.y(l.y)
		fmt.Fprintf(&w, `<circle cx="%s" cy="%s" r="0.9" fill="%s" stroke="#333" stroke-width=".2"/>`, f2s(x), f2s(y), l.color)
		fmt.Fprintf(&w, `<text class="llab" x="%s" y="%s" text-anchor="middle">%s</text>`, f2s(x), f2s(y-1.4), esc(l.label))
	}
	for _, l := range bottomLEDs {
		x, y := v.x(l.x), v.y(l.y)
		fmt.Fprintf(&w, `<circle cx="%s" cy="%s" r="0.9" fill="%s" stroke="#333" stroke-width=".2"/>`, f2s(x), f2s(y), l.color)
		y2 := bbm + 3
		fmt.Fprintf(&w, `<path class="ledlead" d="M%s %s V%s"/>`, f2s(x), f2s(y+0.9), f2s(y2))
		fmt.Fprintf(&w, `<text class="llab" x="%s" y="%s" text-anchor="end" transform="rotate(-90 %s %s)">%s</text>`,
			f2s(x+0.55), f2s(y2+0.5), f2s(x+0.55), f2s(y2+0.5), esc(l.label))
	}
	// edge labels
	fmt.Fprintf(&w, `<text class="edge" x="%s" y="%s">%s</text>`, f2s(bl+v.wmm/2), f2s(bt-1.5), esc(c.t("edgeT")))
	fmt.Fprintf(&w, `<text class="edge" x="%s" y="%s">%s</text>`, f2s(bl+v.wmm*0.15), f2s(bbm+3.2), esc(c.t("edgeB")))
	fmt.Fprintf(&w, `<text class="edge" x="%s" y="%s">%s</text>`, f2s(bl-2), f2s(bt+4), esc(c.t("edgeL")))
	fmt.Fprintf(&w, `<text class="edge" x="%s" y="%s">%s</text>`, f2s(br+2), f2s(bt+4), esc(c.t("edgeR")))
	// callouts: leader from the connector centre to the label outside the outline
	for _, side := range []string{"L", "R", "T", "B"} {
		for _, it := range items[side] {
			t := text[it]
			col := it.cc.Color
			var lx, ly float64 // leader end
			var tx, ty float64
			anchor := "start"
			switch side {
			case "L":
				lx, ly = bl-3, it.pos
				tx, ty, anchor = lx-1, ly, "end"
			case "R":
				lx, ly = br+3, it.pos
				tx, ty = lx+1, ly
			case "T":
				lx, ly = it.pos, bt-5
				tx, ty, anchor = lx, ly-4.5, "middle"
			case "B":
				lx, ly = it.pos, bbm+ledBand+6
				tx, ty, anchor = lx, ly+4, "middle"
			}
			fmt.Fprintf(&w, `<path d="M%s %s L%s %s" stroke="%s" stroke-width=".35" fill="none"/>`, f2s(it.ax), f2s(it.ay), f2s(lx), f2s(ly), col)
			fmt.Fprintf(&w, `<circle cx="%s" cy="%s" r=".6" fill="%s"/>`, f2s(lx), f2s(ly), col)
			// badge + title
			titleW := textW(t.title, labFont)
			var bx float64
			switch anchor {
			case "end":
				bx = tx - titleW - badgeR - 0.8
			case "start":
				bx = tx + badgeR
				tx += 2*badgeR + 0.8
			default:
				bx = tx - titleW/2 - badgeR - 0.6
				tx += badgeR + 0.4
			}
			fmt.Fprintf(&w, `<circle cx="%s" cy="%s" r="%s" fill="%s"/>`, f2s(bx), f2s(ty-1), f2s(badgeR*0.85), col)
			fmt.Fprintf(&w, `<text class="bnum" x="%s" y="%s">%d</text>`, f2s(bx), f2s(ty-0.2), it.cc.Index)
			fmt.Fprintf(&w, `<text class="ctitle" x="%s" y="%s" text-anchor="%s" fill="%s">%s</text>`, f2s(tx), f2s(ty), anchor, col, esc(t.title))
			if t.sub != "" {
				sx := tx
				if anchor == "middle" {
					sx = lx
				}
				fmt.Fprintf(&w, `<text class="csub" x="%s" y="%s" text-anchor="%s">%s</text>`, f2s(sx), f2s(ty+2.9), anchor, esc(t.sub))
			}
		}
	}
	// LED summary line + scale bar
	var parts []string
	for _, r := range c.m.LEDs {
		s := strings.TrimSpace(r.Ref + " " + r.Name)
		if r.Hardware != "" {
			s += "（" + c.t("hwSuffix") + "）"
		}
		parts = append(parts, s)
	}
	yb := bbm + ledBand + 8
	if len(items["B"]) > 0 {
		yb += 10
	}
	if len(parts) > 0 {
		fmt.Fprintf(&w, `<text class="stext" x="%s" y="%s" text-anchor="middle">%s</text>`, f2s(v.vbW/2), f2s(yb), esc(fmt.Sprintf(c.t("ledRow"), strings.Join(parts, " · "))))
	}
	c.scaleBar(&w, bl, yb+6)
	w.WriteString(`</svg>`)
	return w.String()
}

// scaleBar draws a 10 mm bar (5 mm ticks) at (x, y).
func (c *ctx) scaleBar(w *strings.Builder, x, y float64) {
	fmt.Fprintf(w, `<path class="scale" d="M%s %s h10 M%s %s v-1.2 M%s %s v-0.8 M%s %s v-1.2"/>`,
		f2s(x), f2s(y), f2s(x), f2s(y), f2s(x+5), f2s(y), f2s(x+10), f2s(y))
	fmt.Fprintf(w, `<text class="stext" x="%s" y="%s">0</text>`, f2s(x-0.6), f2s(y+2.6))
	fmt.Fprintf(w, `<text class="stext" x="%s" y="%s">10 mm</text>`, f2s(x+9), f2s(y+2.6))
	fmt.Fprintf(w, `<text class="stext" x="%s" y="%s">%s · %.1f × %.1f mm</text>`, f2s(x+20), f2s(y+0.8), esc(c.t("scaleBar")), c.m.WidthMM, c.m.HeightMM)
}

// probeSVG marks the numbered probe locations on the board.
func (c *ctx) probeSVG() string {
	all := append(append([]ProbeRow(nil), c.m.RailProbes...), c.m.SigProbes...)
	type mark struct {
		x, y float64
		nums []string
	}
	var marks []*mark
	for _, p := range all {
		if !p.Found {
			continue
		}
		var mk *mark
		for _, m := range marks {
			if math.Abs(m.x-p.x) < 1 && math.Abs(m.y-p.y) < 1 {
				mk = m
			}
		}
		if mk == nil {
			mk = &mark{x: p.x, y: p.y}
			marks = append(marks, mk)
		}
		mk.nums = append(mk.nums, fmt.Sprint(p.N))
	}
	if len(marks) == 0 {
		return ""
	}
	v := newView(c.ob, 8, 8, 8, 22)
	var w strings.Builder
	svgOpen(&w, 0, 0, v.vbW, v.vbH, "probe locations")
	c.boardBase(v, &w, true)
	for _, m := range marks {
		label := strings.Join(m.nums, ",")
		r := math.Max(1.6, textW(label, 1.9)/2+0.5)
		fmt.Fprintf(&w, `<circle class="probe" cx="%s" cy="%s" r="%s"/>`, f2s(v.x(m.x)), f2s(v.y(m.y)), f2s(r))
		fmt.Fprintf(&w, `<text class="pnum" x="%s" y="%s">%s</text>`, f2s(v.x(m.x)), f2s(v.y(m.y)+0.68), esc(label))
	}
	fmt.Fprintf(&w, `<text class="edge" x="%s" y="%s">%s</text>`, f2s(v.left+v.wmm/2), f2s(v.top-2.2), esc(c.t("edgeT")))
	fmt.Fprintf(&w, `<text class="edge" x="%s" y="%s">%s</text>`, f2s(v.left+v.wmm/2), f2s(v.top+v.hmm+4), esc(c.t("edgeB")))
	c.scaleBar(&w, v.left, v.top+v.hmm+14)
	w.WriteString(`</svg>`)
	return w.String()
}

// ledSVG is a zoomed view of the LEDs with their labels.
func (c *ctx) ledSVG() string {
	var e ext
	type led struct {
		p   *Part
		row LEDRow
	}
	var leds []led
	for _, r := range c.m.LEDs {
		p := c.in.Board.Part(r.Ref)
		if p == nil {
			continue
		}
		bb := p.Box()
		e.add(bb.MinX, bb.MinY)
		e.add(bb.MaxX, bb.MaxY)
		leds = append(leds, led{p, r})
	}
	if len(leds) == 0 {
		return ""
	}
	maxLab := 0.0
	labs := make([]string, len(leds))
	for i, l := range leds {
		labs[i] = strings.TrimSpace(l.row.Ref + " " + l.row.Name)
		maxLab = math.Max(maxLab, textW(labs[i], 1.5))
	}
	v := newView(c.ob, 6, 6, 6, 6)
	x0 := v.x(e.bb.MinX) - 4
	x1 := v.x(e.bb.MaxX) + 4
	y0 := v.y(e.bb.MaxY) - maxLab - 3
	y1 := v.y(e.bb.MinY) + 4
	// include the nearest board edge when it is close
	if bottom := v.top + v.hmm; bottom-y1 < 6 && bottom > y1 {
		y1 = bottom + 2
	}
	if top := v.top; y0-top < 6 && y0 > top {
		y0 = top - 2
	}
	var w strings.Builder
	svgOpen(&w, x0, y0, x1-x0, y1-y0, "LED locations")
	c.boardBase(v, &w, true)
	for i, l := range leds {
		bb := l.p.Box()
		fmt.Fprintf(&w, `<rect class="lled" x="%s" y="%s" width="%s" height="%s" fill="%s"/>`,
			f2s(v.x(bb.MinX)), f2s(v.y(bb.MaxY)), f2s(bb.W()*MilToMM), f2s(bb.H()*MilToMM), ledColor(l.row.Color))
		tx, ty := v.x(bb.CX())+0.5, v.y(bb.MaxY)-0.8
		fmt.Fprintf(&w, `<text class="llab" x="%s" y="%s" transform="rotate(-90 %s %s)">%s</text>`,
			f2s(tx), f2s(ty), f2s(tx), f2s(ty), esc(labs[i]))
	}
	w.WriteString(`</svg>`)
	return w.String()
}

// connSVG draws one connector's pads at their true positions and shapes,
// coloured by role, with pin numbers and net names (mil units).
func connSVG(p *Part, rows []PinRow, title string) string {
	role := map[string]string{}
	for _, r := range rows {
		role[r.Pin] = r.Role
	}
	pb := p.PadBox()
	// pitch = smallest centre distance
	pitch := math.Inf(1)
	for i := range p.Pads {
		for j := i + 1; j < len(p.Pads); j++ {
			d := math.Hypot(p.Pads[i].X-p.Pads[j].X, p.Pads[i].Y-p.Pads[j].Y)
			if d > 1 {
				pitch = math.Min(pitch, d)
			}
		}
	}
	if math.IsInf(pitch, 1) {
		pitch = 100
	}
	font := math.Max(14, math.Min(pitch*0.34, 48))
	var sx, sy float64
	var mx, my float64
	for _, pd := range p.Pads {
		mx += pd.X
		my += pd.Y
	}
	mx /= float64(len(p.Pads))
	my /= float64(len(p.Pads))
	var xs, ys []float64
	for _, pd := range p.Pads {
		xs = append(xs, pd.X)
		ys = append(ys, pd.Y)
	}
	sx, sy = span(xs), span(ys)
	vertical := sy >= sx // labels left / right
	type lab struct {
		x, y   float64
		anchor string
		rot    bool
		text   string
	}
	var labs []lab
	maxLen := 0.0
	gap := font * 0.5
	for _, pd := range p.Pads {
		t := pd.Net
		if t == "" {
			t = "NC"
		}
		maxLen = math.Max(maxLen, textW(t, font))
		if vertical {
			if pd.X < mx-1 {
				labs = append(labs, lab{pd.X - pd.Width/2 - gap, pd.Y, "end", false, t})
			} else {
				labs = append(labs, lab{pd.X + pd.Width/2 + gap, pd.Y, "start", false, t})
			}
		} else {
			if pd.Y >= my-1 {
				labs = append(labs, lab{pd.X, pd.Y + pd.Height/2 + gap, "start", true, t})
			} else {
				labs = append(labs, lab{pd.X, pd.Y - pd.Height/2 - gap, "end", true, t})
			}
		}
	}
	pad := maxLen + gap*2 + font
	ring := font * 0.6
	for _, pd := range p.Pads {
		ring = math.Max(ring, math.Max(pd.Width, pd.Height)/2+font*0.5)
	}
	minX, maxX, minY, maxY := pb.MinX-ring, pb.MaxX+ring, pb.MinY-ring, pb.MaxY+ring
	if vertical {
		minX, maxX = pb.MinX-pad, pb.MaxX+pad
	} else {
		minY, maxY = pb.MinY-pad, pb.MaxY+pad
	}
	if bb := p.Box(); bb.valid() {
		minX, maxX = math.Min(minX, bb.MinX-font/2), math.Max(maxX, bb.MaxX+font/2)
		minY, maxY = math.Min(minY, bb.MinY-font/2), math.Max(maxY, bb.MaxY+font/2)
	}
	X := func(x float64) float64 { return x - minX }
	Y := func(y float64) float64 { return maxY - y }
	var w strings.Builder
	fmt.Fprintf(&w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %s %s" role="img" aria-label="%s" style="height:%smm">`,
		f2s(maxX-minX), f2s(maxY-minY), esc(title), f2s(math.Min(170, math.Max(70, (maxY-minY)*MilToMM*3))))
	if bb := p.Box(); bb.valid() {
		fmt.Fprintf(&w, `<rect x="%s" y="%s" width="%s" height="%s" fill="#fbf7ef" stroke="#c9a46a" stroke-width="%s" stroke-dasharray="%s"/>`,
			f2s(X(bb.MinX)), f2s(Y(bb.MaxY)), f2s(bb.W()), f2s(bb.H()), f2s(font*0.12), f2s(font*0.5))
	}
	pads := append([]Pad(nil), p.Pads...)
	sort.SliceStable(pads, func(i, j int) bool { return natLess(pads[i].PadNumber, pads[j].PadNumber) })
	for _, pd := range pads {
		col := roleColor[role[pd.PadNumber]]
		if col == "" {
			col = roleColor["signal"]
		}
		x, y, wd, ht := X(pd.X-pd.Width/2), Y(pd.Y+pd.Height/2), pd.Width, pd.Height
		switch pd.ShapeKind() {
		case "ELLIPSE", "CIRCLE":
			fmt.Fprintf(&w, `<ellipse data-pin="%s" cx="%s" cy="%s" rx="%s" ry="%s" fill="%s"/>`, esc(pd.PadNumber), f2s(X(pd.X)), f2s(Y(pd.Y)), f2s(wd/2), f2s(ht/2), col)
		case "OVAL":
			fmt.Fprintf(&w, `<rect data-pin="%s" x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"/>`, esc(pd.PadNumber), f2s(x), f2s(y), f2s(wd), f2s(ht), f2s(math.Min(wd, ht)/2), col)
		default:
			fmt.Fprintf(&w, `<rect data-pin="%s" x="%s" y="%s" width="%s" height="%s" fill="%s"/>`, esc(pd.PadNumber), f2s(x), f2s(y), f2s(wd), f2s(ht), col)
		}
		if pd.PadNumber == "1" {
			r := math.Max(wd, ht)/2 + font*0.3
			fmt.Fprintf(&w, `<circle class="pin1ring" cx="%s" cy="%s" r="%s" fill="none" stroke="#ff8c00" stroke-width="%s"/>`, f2s(X(pd.X)), f2s(Y(pd.Y)), f2s(r), f2s(font*0.18))
		}
		nf := math.Min(math.Min(wd, ht)*0.55, font*1.1)
		fmt.Fprintf(&w, `<text x="%s" y="%s" font-size="%s" font-family="sans-serif" font-weight="bold" fill="#fff" text-anchor="middle">%s</text>`,
			f2s(X(pd.X)), f2s(Y(pd.Y)+nf*0.36), f2s(nf), esc(pd.PadNumber))
	}
	for _, l := range labs {
		x, y := X(l.x), Y(l.y)
		if l.rot {
			fmt.Fprintf(&w, `<text x="%s" y="%s" font-size="%s" font-family="sans-serif" fill="#222" text-anchor="%s" transform="rotate(-90 %s %s)">%s</text>`,
				f2s(x+font*0.35), f2s(y), f2s(font), l.anchor, f2s(x+font*0.35), f2s(y), esc(l.text))
		} else {
			fmt.Fprintf(&w, `<text x="%s" y="%s" font-size="%s" font-family="sans-serif" fill="#222" text-anchor="%s">%s</text>`,
				f2s(x), f2s(y+font*0.35), f2s(font), l.anchor, esc(l.text))
		}
	}
	w.WriteString(`</svg>`)
	return w.String()
}

func span(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	lo, hi := v[0], v[0]
	for _, x := range v {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return hi - lo
}

func (c *ctx) buildSVGs() {
	c.m.BoardSVG = c.boardSVG()
	c.m.ProbeSVG = c.probeSVG()
	c.m.LEDSVG = c.ledSVG()
	for _, cc := range c.m.Connectors {
		cc.SVG = connSVG(cc.part, cc.Pins, cc.Ref)
	}
}
