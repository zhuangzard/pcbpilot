// Package kicad drives KiCad files for pcbpilot (engine-neutral design in
// KiCad, EasyEDA only for submission — user decision 2026-10-09).
package kicad

// schfit.go — automatic schematic page size (user request 2026-10-09: "自动
// 调整 Schematic 的页面大小"). The content box of a .kicad_sch sheet
// (symbol bodies and pins from their library graphics, wires, buses, labels,
// texts, sheets, graphics) is fitted to the smallest ISO landscape sheet
// A4…A0 whose drawing area holds it without touching the title block; the
// content is shifted into the drawing area when it lies outside it. Pure
// S-expression edit: the (paper "…") token and, when shifted, every
// top-level coordinate.

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Sheet geometry (mm). KiCad's default page layout: 10 mm border, title
// block in the bottom-right corner (about 110 × 32 mm).
const (
	schBorder     = 10.0
	schGap        = 5.0
	schTitleW     = 112.0
	schTitleH     = 34.0
	schShiftQuant = 1.27 // keep the 50-mil grid when shifting
)

type paper struct {
	Name string
	W, H float64
}

// Papers is the size ladder, landscape, smallest first.
var Papers = []paper{{"A4", 297, 210}, {"A3", 420, 297}, {"A2", 594, 420}, {"A1", 841, 594}, {"A0", 1189, 841}}

// Box is a min/max rectangle in sheet mm (y down).
type Box struct{ MinX, MinY, MaxX, MaxY float64 }

func (b Box) W() float64 { return b.MaxX - b.MinX }
func (b Box) H() float64 { return b.MaxY - b.MinY }
func (b *Box) add(x, y float64) {
	b.MinX, b.MinY = math.Min(b.MinX, x), math.Min(b.MinY, y)
	b.MaxX, b.MaxY = math.Max(b.MaxX, x), math.Max(b.MaxY, y)
}
func emptyBox() Box { return Box{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)} }

// ---- minimal S-expression ---------------------------------------------------

type sx struct {
	atom string
	list []*sx
	beg  int // byte offsets of this list in the source (lists only)
	end  int
}

func (n *sx) head() string {
	if n == nil || len(n.list) == 0 {
		return ""
	}
	return n.list[0].atom
}

func (n *sx) child(h string) *sx {
	for _, c := range n.list {
		if c.head() == h {
			return c
		}
	}
	return nil
}

func (n *sx) num(i int) float64 {
	if i < len(n.list) {
		v, _ := strconv.ParseFloat(n.list[i].atom, 64)
		return v
	}
	return 0
}

func parseSx(src string) (*sx, error) {
	pos := 0
	var parse func() (*sx, error)
	parse = func() (*sx, error) {
		for pos < len(src) && strings.ContainsRune(" \t\r\n", rune(src[pos])) {
			pos++
		}
		if pos >= len(src) {
			return nil, fmt.Errorf("unexpected end")
		}
		switch src[pos] {
		case '(':
			n := &sx{beg: pos}
			pos++
			for {
				for pos < len(src) && strings.ContainsRune(" \t\r\n", rune(src[pos])) {
					pos++
				}
				if pos >= len(src) {
					return nil, fmt.Errorf("unclosed list at %d", n.beg)
				}
				if src[pos] == ')' {
					pos++
					n.end = pos
					return n, nil
				}
				c, err := parse()
				if err != nil {
					return nil, err
				}
				n.list = append(n.list, c)
			}
		case '"':
			start := pos
			pos++
			for pos < len(src) && src[pos] != '"' {
				if src[pos] == '\\' {
					pos++
				}
				pos++
			}
			pos++
			s, _ := strconv.Unquote(src[start:pos])
			return &sx{atom: s}, nil
		default:
			start := pos
			for pos < len(src) && !strings.ContainsRune(" \t\r\n()", rune(src[pos])) {
				pos++
			}
			return &sx{atom: src[start:pos]}, nil
		}
	}
	return parse()
}

// ---- content box ---------------------------------------------------------------

// libBox is a library symbol's extent in its own frame (y up in KiCad
// symbol coordinates), pins included with their length.
func libBox(sym *sx) Box {
	b := emptyBox()
	var walk func(n *sx)
	walk = func(n *sx) {
		switch n.head() {
		case "xy", "start", "end", "center", "mid":
			b.add(n.num(1), n.num(2))
		case "circle":
			if c, r := n.child("center"), n.child("radius"); c != nil && r != nil {
				b.add(c.num(1)-r.num(1), c.num(2)-r.num(1))
				b.add(c.num(1)+r.num(1), c.num(2)+r.num(1))
			}
		case "pin":
			if at := n.child("at"); at != nil {
				x, y, a := at.num(1), at.num(2), at.num(3)*math.Pi/180
				l := 0.0
				if ln := n.child("length"); ln != nil {
					l = ln.num(1)
				}
				b.add(x, y)
				b.add(x+l*math.Cos(a), y+l*math.Sin(a))
			}
			return
		case "property":
			return // fields are placed per instance
		}
		for _, c := range n.list {
			if c.atom == "" {
				walk(c)
			}
		}
	}
	walk(sym)
	return b
}

// ContentBox returns the sheet content extent (mm, y down) and whether any
// content was found.
func ContentBox(root *sx) (Box, bool) {
	b, _, ok := contentItems(root)
	return b, ok
}

// contentItems returns the content extent and one box per drawn item, so the
// title-block test can look at the items instead of their common bounding
// box (content usually wraps the title block in an L).
func contentItems(root *sx) (Box, []Box, bool) {
	libs := map[string]Box{}
	if ls := root.child("lib_symbols"); ls != nil {
		for _, s := range ls.list {
			if s.head() == "symbol" && len(s.list) > 1 {
				libs[s.list[1].atom] = libBox(s)
			}
		}
	}
	b := emptyBox()
	found := false
	var items []Box
	item := func(ib Box) {
		items = append(items, ib)
		b.add(ib.MinX, ib.MinY)
		b.add(ib.MaxX, ib.MaxY)
		found = true
	}
	pt := func(x, y float64) { item(Box{x - 1, y - 1, x + 1, y + 1}) }
	addPts := func(n *sx) {
		if pts := n.child("pts"); pts != nil {
			var prev *sx
			for _, p := range pts.list {
				if p.head() != "xy" {
					continue
				}
				if prev != nil {
					seg := emptyBox()
					seg.add(prev.num(1), prev.num(2))
					seg.add(p.num(1), p.num(2))
					item(seg)
				} else {
					pt(p.num(1), p.num(2))
				}
				prev = p
			}
		}
	}
	for _, n := range root.list {
		switch n.head() {
		case "symbol":
			at := n.child("at")
			if at == nil {
				continue
			}
			x, y, rot := at.num(1), at.num(2), at.num(3)
			lb, ok := Box{}, false
			if id := n.child("lib_id"); id != nil && len(id.list) > 1 {
				lb, ok = libs[id.list[1].atom]
			}
			if !ok || math.IsInf(lb.MinX, 0) {
				pt(x, y)
				continue
			}
			mx, my := 1.0, 1.0
			if m := n.child("mirror"); m != nil && len(m.list) > 1 {
				if m.list[1].atom == "x" {
					my = -1
				} else {
					mx = -1
				}
			}
			a := -rot * math.Pi / 180 // symbol frame is y-up
			sb := emptyBox()
			for _, c := range [][2]float64{{lb.MinX, lb.MinY}, {lb.MaxX, lb.MinY}, {lb.MinX, lb.MaxY}, {lb.MaxX, lb.MaxY}} {
				px, py := c[0]*mx, -c[1]*my
				sb.add(x+px*math.Cos(a)-py*math.Sin(a), y+px*math.Sin(a)+py*math.Cos(a))
			}
			item(sb)
			for _, p := range n.list {
				if p.head() == "property" && p.child("hide") == nil && len(p.list) > 2 && p.list[2].atom != "" {
					if pat := p.child("at"); pat != nil && !propertyHidden(p) {
						pt(pat.num(1), pat.num(2))
					}
				}
			}
		case "wire", "bus", "polyline", "bus_entry":
			addPts(n)
			if n.head() == "bus_entry" {
				if at := n.child("at"); at != nil {
					pt(at.num(1), at.num(2))
				}
			}
		case "label", "global_label", "hierarchical_label", "text", "junction", "no_connect", "netclass_flag":
			if at := n.child("at"); at != nil {
				x, y := at.num(1), at.num(2)
				// labels and texts run to the right of their anchor
				w := 1.0
				if len(n.list) > 1 && n.list[1].atom != "" {
					w = 1.0 + 1.0*float64(len([]rune(n.list[1].atom)))
				}
				item(Box{x - 1, y - 1.5, x + w, y + 1.5})
			}
		case "sheet":
			if at, sz := n.child("at"), n.child("size"); at != nil && sz != nil {
				item(Box{at.num(1), at.num(2), at.num(1) + sz.num(1), at.num(2) + sz.num(2)})
			}
		case "rectangle", "text_box", "image", "circle", "arc":
			ib := emptyBox()
			for _, k := range []string{"start", "end", "at", "center", "mid"} {
				if c := n.child(k); c != nil {
					ib.add(c.num(1), c.num(2))
				}
			}
			if !math.IsInf(ib.MinX, 0) {
				item(ib)
			}
		}
	}
	return b, items, found
}

// propertyHidden reports a symbol field hidden by (hide yes) or (effects … hide).
func propertyHidden(p *sx) bool {
	var hidden bool
	var walk func(n *sx)
	walk = func(n *sx) {
		if n.head() == "hide" && (len(n.list) == 1 || n.list[1].atom == "yes") {
			hidden = true
		}
		for _, c := range n.list {
			if c.atom == "hide" {
				hidden = true
			}
			if c.atom == "" {
				walk(c)
			}
		}
	}
	walk(p)
	return hidden
}

// fits reports whether the content (already shifted by dx, dy) lies in
// paper p's drawing area with no item entering the title block.
func fits(b Box, items []Box, dx, dy float64, p paper) bool {
	minX, minY := schBorder+schGap, schBorder+schGap
	maxX, maxY := p.W-schBorder-schGap, p.H-schBorder-schGap
	if b.MinX+dx < minX || b.MinY+dy < minY || b.MaxX+dx > maxX || b.MaxY+dy > maxY {
		return false
	}
	tbX, tbY := p.W-schBorder-schTitleW-schGap, p.H-schBorder-schTitleH-schGap
	for _, it := range items {
		if it.MaxX+dx > tbX && it.MaxY+dy > tbY {
			return false
		}
	}
	return true
}

// FitResult is the outcome for one sheet.
type FitResult struct {
	From, To     string
	Content      Box
	ShiftX       float64 `json:"shiftX"`
	ShiftY       float64 `json:"shiftY"`
	TooBig       bool    `json:"tooBig"`
	Changed      bool    `json:"changed"`
	ContentFound bool    `json:"contentFound"`
}

// FitSheet chooses the paper for one .kicad_sch source and returns the
// edited source. Content is shifted into the drawing area (top-left, on the
// 1.27 mm grid) only when it does not fit where it is.
func FitSheet(src string) (string, FitResult, error) {
	root, err := parseSx(src)
	if err != nil {
		return "", FitResult{}, err
	}
	res := FitResult{}
	if p := root.child("paper"); p != nil && len(p.list) > 1 {
		res.From = p.list[1].atom
	}
	box, items, ok := contentItems(root)
	res.Content, res.ContentFound = box, ok
	if !ok {
		res.To = res.From
		return src, res, nil
	}
	for _, p := range Papers {
		if fits(box, items, 0, 0, p) {
			res.To = p.Name
			break
		}
		// Shifted to the top-left of the drawing area?
		sx := math.Ceil((schBorder+schGap-box.MinX)/schShiftQuant) * schShiftQuant
		sy := math.Ceil((schBorder+schGap-box.MinY)/schShiftQuant) * schShiftQuant
		if fits(box, items, sx, sy, p) {
			res.To, res.ShiftX, res.ShiftY = p.Name, sx, sy
			break
		}
	}
	if res.To == "" {
		res.TooBig, res.To = true, res.From
		return src, res, nil
	}
	out := src
	if res.ShiftX != 0 || res.ShiftY != 0 {
		out = shiftTopLevel(src, root, res.ShiftX, res.ShiftY)
	}
	if res.To != res.From {
		re := regexp.MustCompile(`\(paper "[^"]*"( portrait)?\)`)
		if re.MatchString(out) {
			out = re.ReplaceAllString(out, fmt.Sprintf(`(paper "%s")`, res.To))
		}
	}
	res.Changed = out != src
	return out, res, nil
}

// shiftTopLevel moves every coordinate of the top-level drawing items
// (not lib_symbols, not the sheet header) by dx, dy.
var coordRe = regexp.MustCompile(`\((at|xy|start|end|center|mid) (-?[0-9.]+) (-?[0-9.]+)`)

func shiftTopLevel(src string, root *sx, dx, dy float64) string {
	moved := map[string]bool{"symbol": true, "wire": true, "bus": true, "polyline": true, "bus_entry": true, "label": true,
		"global_label": true, "hierarchical_label": true, "text": true, "junction": true, "no_connect": true, "sheet": true,
		"rectangle": true, "text_box": true, "image": true, "circle": true, "arc": true, "netclass_flag": true}
	var b strings.Builder
	last := 0
	for _, n := range root.list {
		if !moved[n.head()] || n.end == 0 {
			continue
		}
		b.WriteString(src[last:n.beg])
		seg := coordRe.ReplaceAllStringFunc(src[n.beg:n.end], func(m string) string {
			p := coordRe.FindStringSubmatch(m)
			x, _ := strconv.ParseFloat(p[2], 64)
			y, _ := strconv.ParseFloat(p[3], 64)
			return fmt.Sprintf("(%s %s %s", p[1], fmtMM(x+dx), fmtMM(y+dy))
		})
		b.WriteString(seg)
		last = n.end
	}
	b.WriteString(src[last:])
	return b.String()
}

func fmtMM(v float64) string {
	s := strconv.FormatFloat(math.Round(v*10000)/10000, 'f', -1, 64)
	if s == "-0" {
		return "0"
	}
	return s
}
