package kicad

// schquality.go — pcbpilot's own aesthetic/structural checks of one
// .kicad_sch (the part ERC does not look at): overlapping symbols, wires
// through bodies, diagonal / off-grid / overlapping wires, labels and field
// texts on top of things, pins or wire ends on a wire's middle (KiCad does
// not connect them), and anything in the title block or off the page.
// Used as the strict gate before every multi-object KiCad edit is written
// (the planned page must pass) and by `pcbpilot kicad sch-check`.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Finding is one quality problem. Subject names the objects (no
// coordinates), so Key() is stable when things move.
type Finding struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Subject  string `json:"subject"`
	Message  string `json:"message"`
	At       *Pt    `json:"at,omitempty"`
}

// Key identifies a finding across edits.
func (f Finding) Key() string { return f.Kind + ":" + f.Subject }

// CheckOptions tunes CheckSchematic.
type CheckOptions struct {
	Ignore []string // finding kinds to skip
}

// Finding kinds.
const (
	FSymbolOverlap   = "symbol-overlap"
	FWireThroughBody = "wire-through-body"
	FDiagonalWire    = "diagonal-wire"
	FOffGrid         = "off-grid"
	FLabelOverlap    = "label-overlap"
	FLabelOnWire     = "label-on-wire"
	FPinInLabel      = "pin-in-label"
	FTextOverSymbol  = "text-over-symbol"
	FTextOverlap     = "text-overlap"
	FTitleBlock      = "title-block"
	FOffPage         = "off-page"
	FPinOnWire       = "pin-on-wire"
	FWireEndOnWire   = "wire-end-on-wire"
	FWireOverlap     = "wire-overlap"
)

const qEps = 0.05

func overlaps(a, b Box) bool {
	return math.Min(a.MaxX, b.MaxX)-math.Max(a.MinX, b.MinX) > qEps && math.Min(a.MaxY, b.MaxY)-math.Max(a.MinY, b.MinY) > qEps
}

func inside(p Pt, b Box) bool {
	return p.X > b.MinX+qEps && p.X < b.MaxX-qEps && p.Y > b.MinY+qEps && p.Y < b.MaxY-qEps
}

func segThroughBox(a, b Pt, bx Box) bool {
	if a.X == b.X || a.Y == b.Y {
		s := Box{math.Min(a.X, b.X), math.Min(a.Y, b.Y), math.Max(a.X, b.X), math.Max(a.Y, b.Y)}
		if a.X == b.X {
			return a.X > bx.MinX+qEps && a.X < bx.MaxX-qEps && math.Min(s.MaxY, bx.MaxY)-math.Max(s.MinY, bx.MinY) > qEps
		}
		return a.Y > bx.MinY+qEps && a.Y < bx.MaxY-qEps && math.Min(s.MaxX, bx.MaxX)-math.Max(s.MinX, bx.MinX) > qEps
	}
	n := int(math.Hypot(b.X-a.X, b.Y-a.Y)/0.1) + 1
	for i := 0; i <= n; i++ {
		if inside(Pt{a.X + (b.X-a.X)*float64(i)/float64(n), a.Y + (b.Y-a.Y)*float64(i)/float64(n)}, bx) {
			return true
		}
	}
	return false
}

func segBox(a, b Pt) Box {
	return Box{math.Min(a.X, b.X), math.Min(a.Y, b.Y), math.Max(a.X, b.X), math.Max(a.Y, b.Y)}
}

// labelCheckBox is the text box of a label: a local label's text sits on
// the reading side of its anchor line (KiCad's bottom justification, text
// never upside down), a global/hierarchical label's shape is centred on it.
func labelCheckBox(l SceneLabel) Box {
	w := 0.4 + 1.27*0.8*float64(len([]rune(l.Name)))
	if l.Kind != LabelLocal {
		w += 2.0 // the shape
		const h = 1.1
		switch dirOf(l.Angle) {
		case 1:
			return Box{l.At.X - h, l.At.Y - w, l.At.X + h, l.At.Y}
		case 2:
			return Box{l.At.X - w, l.At.Y - h, l.At.X, l.At.Y + h}
		case 3:
			return Box{l.At.X - h, l.At.Y, l.At.X + h, l.At.Y + w}
		}
		return Box{l.At.X, l.At.Y - h, l.At.X + w, l.At.Y + h}
	}
	const lo, hi = 0.15, 1.5 // from the anchor line to the top of the text
	switch dirOf(l.Angle) {
	case 1:
		return Box{l.At.X - hi, l.At.Y - w, l.At.X - lo, l.At.Y}
	case 2:
		return Box{l.At.X - w, l.At.Y - hi, l.At.X, l.At.Y - lo}
	case 3:
		return Box{l.At.X - hi, l.At.Y, l.At.X - lo, l.At.Y + w}
	}
	return Box{l.At.X, l.At.Y - hi, l.At.X + w, l.At.Y - lo}
}

func symName(s SceneSymbol) string {
	if s.Power {
		return "power " + powerValue(s)
	}
	return s.Ref
}

func powerValue(s SceneSymbol) string {
	for _, f := range s.Fields {
		if f.Name == "Value" {
			return f.Value
		}
	}
	return strings.TrimPrefix(s.LibID, "pcbpilot_power:")
}

func pair(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + " × " + b
}

// CheckSchematic runs the quality checks on one sheet's text.
func CheckSchematic(text string, opt CheckOptions) []Finding {
	e, err := OpenSchematic(text)
	if err != nil {
		return []Finding{{Kind: "parse", Severity: "error", Message: err.Error()}}
	}
	sc, err := e.Scene()
	if err != nil {
		return []Finding{{Kind: "parse", Severity: "error", Message: err.Error()}}
	}
	return sc.Check(opt)
}

// Check runs the quality checks on a scene.
func (sc *SchScene) Check(opt CheckOptions) []Finding {
	skip := map[string]bool{}
	for _, k := range opt.Ignore {
		skip[k] = true
	}
	var out []Finding
	add := func(kind, subject, msg string, at *Pt) {
		if !skip[kind] {
			out = append(out, Finding{Kind: kind, Severity: "error", Subject: subject, Message: msg, At: at})
		}
	}
	syms := sc.Symbols
	// symbols on symbols
	for i := range syms {
		for j := i + 1; j < len(syms); j++ {
			a, b := syms[i], syms[j]
			if a.HasBox && b.HasBox && a.Ref != b.Ref && overlaps(a.Body, b.Body) {
				add(FSymbolOverlap, pair(symName(a), symName(b)), fmt.Sprintf("%s and %s overlap", symName(a), symName(b)), nil)
			}
		}
	}
	// wires
	for i, w := range sc.Wires {
		a, b := w[0], w[1]
		if a.X != b.X && a.Y != b.Y {
			p := a
			add(FDiagonalWire, "", fmt.Sprintf("wire (%s,%s)–(%s,%s) is diagonal", F(a.X), F(a.Y), F(b.X), F(b.Y)), &p)
		}
		for _, p := range []Pt{a, b} {
			if !OnGrid(p) {
				q := p
				add(FOffGrid, "wire", fmt.Sprintf("wire end (%s,%s) is off the 1.27 mm grid", F(p.X), F(p.Y)), &q)
			}
		}
		for _, s := range syms {
			if s.HasBox && segThroughBox(a, b, s.Body) {
				add(FWireThroughBody, symName(s), fmt.Sprintf("a wire runs through %s", symName(s)), &Pt{a.X, a.Y})
			}
		}
		for j := i + 1; j < len(sc.Wires); j++ {
			c, d := sc.Wires[j][0], sc.Wires[j][1]
			if collinearOverlap(a, b, c, d) {
				p := a
				add(FWireOverlap, "", fmt.Sprintf("wires (%s,%s)–(%s,%s) and (%s,%s)–(%s,%s) overlap", F(a.X), F(a.Y), F(b.X), F(b.Y), F(c.X), F(c.Y), F(d.X), F(d.Y)), &p)
			}
		}
	}
	isJunction := func(p Pt) bool {
		for _, j := range sc.Junctions {
			if samePt(j, p) {
				return true
			}
		}
		return false
	}
	for i, w := range sc.Wires {
		for j, v := range sc.Wires {
			if i == j {
				continue
			}
			for _, p := range []Pt{v[0], v[1]} {
				if onSegInterior(p, w[0], w[1]) && !isJunction(p) {
					q := p
					add(FWireEndOnWire, "", fmt.Sprintf("a wire ends on the middle of another at (%s,%s) without a junction (not connected)", F(p.X), F(p.Y)), &q)
				}
			}
		}
	}
	// pins
	for _, s := range syms {
		for _, p := range s.Pins {
			if !s.Power && !OnGrid(p.At) {
				q := p.At
				add(FOffGrid, s.Ref, fmt.Sprintf("pin %s.%s is off the 1.27 mm grid", s.Ref, p.Number), &q)
			}
			for _, w := range sc.Wires {
				if onSegInterior(p.At, w[0], w[1]) {
					q := p.At
					add(FPinOnWire, s.Ref+"."+p.Number, fmt.Sprintf("pin %s.%s lies on the middle of a wire (KiCad does not connect it)", symName(s), p.Number), &q)
				}
			}
		}
	}
	// labels
	lb := make([]Box, len(sc.Labels))
	for i, l := range sc.Labels {
		lb[i] = labelCheckBox(l)
		if !OnGrid(l.At) {
			q := l.At
			add(FOffGrid, "label "+l.Name, fmt.Sprintf("label %s is off the 1.27 mm grid", l.Name), &q)
		}
	}
	for i, l := range sc.Labels {
		for _, w := range sc.Wires {
			if segThroughBox(w[0], w[1], lb[i]) {
				add(FLabelOnWire, "label "+l.Name, fmt.Sprintf("a wire runs through the text of label %s", l.Name), &Pt{w[0].X, w[0].Y})
				break
			}
		}
		for j := i + 1; j < len(sc.Labels); j++ {
			if overlaps(lb[i], lb[j]) {
				add(FLabelOverlap, pair("label "+l.Name, "label "+sc.Labels[j].Name), fmt.Sprintf("labels %s and %s overlap", l.Name, sc.Labels[j].Name), nil)
			}
		}
		for _, s := range syms {
			if s.HasBox && overlaps(lb[i], s.Body) {
				add(FLabelOverlap, pair("label "+l.Name, symName(s)), fmt.Sprintf("label %s overlaps %s", l.Name, symName(s)), nil)
			}
			for _, f := range s.Fields {
				if overlaps(lb[i], f.Box) {
					add(FLabelOverlap, pair("label "+l.Name, symName(s)+" "+f.Name), fmt.Sprintf("label %s overlaps the %s text of %s", l.Name, f.Name, symName(s)), nil)
				}
			}
			for _, p := range s.Pins {
				if !samePt(p.At, l.At) && inside(p.At, lb[i]) {
					add(FPinInLabel, symName(s)+"."+p.Number+" in "+l.Name, fmt.Sprintf("pin %s.%s lies under label %s", symName(s), p.Number, l.Name), nil)
				}
			}
		}
	}
	for _, j := range sc.Junctions {
		if !OnGrid(j) {
			q := j
			add(FOffGrid, "junction", "junction off the 1.27 mm grid", &q)
		}
	}
	// field texts
	type ftext struct {
		owner, name string
		box         Box
	}
	var fts []ftext
	for _, s := range syms {
		for _, f := range s.Fields {
			fts = append(fts, ftext{symName(s), f.Name, f.Box})
		}
	}
	for i, f := range fts {
		for _, s := range syms {
			if s.HasBox && symName(s) != f.owner && overlaps(f.box, s.Body) {
				add(FTextOverSymbol, f.owner+" "+f.name+" on "+symName(s), fmt.Sprintf("the %s text of %s lies on %s", f.name, f.owner, symName(s)), nil)
			}
		}
		for j := i + 1; j < len(fts); j++ {
			if overlaps(f.box, fts[j].box) {
				add(FTextOverlap, pair(f.owner+" "+f.name, fts[j].owner+" "+fts[j].name), fmt.Sprintf("texts %s %s and %s %s overlap", f.owner, f.name, fts[j].owner, fts[j].name), nil)
			}
		}
	}
	// title block and page
	type item struct {
		name string
		box  Box
	}
	var all []item
	for _, s := range syms {
		if s.HasBox {
			all = append(all, item{symName(s), s.Box})
		}
	}
	for _, f := range fts {
		all = append(all, item{f.owner + " " + f.name, f.box})
	}
	for i, l := range sc.Labels {
		all = append(all, item{"label " + l.Name, lb[i]})
	}
	for _, w := range sc.Wires {
		all = append(all, item{"wire", segBox(w[0], w[1])})
	}
	for _, t := range sc.Texts {
		all = append(all, item{"text", t})
	}
	if sc.TitleBlock != nil {
		for _, it := range all {
			b := it.box
			if math.Min(b.MaxX, sc.TitleBlock.MaxX)-math.Max(b.MinX, sc.TitleBlock.MinX) > qEps && math.Min(b.MaxY, sc.TitleBlock.MaxY)-math.Max(b.MinY, sc.TitleBlock.MinY) > qEps {
				add(FTitleBlock, it.name, it.name+" enters the title block", nil)
			}
		}
	}
	if sc.Page != nil {
		for _, it := range all {
			b := it.box
			if b.MinX < sc.Page.MinX-qEps || b.MinY < sc.Page.MinY-qEps || b.MaxX > sc.Page.MaxX+qEps || b.MaxY > sc.Page.MaxY+qEps {
				add(FOffPage, it.name, it.name+" lies outside the drawing area", nil)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Subject < out[j].Subject
	})
	return out
}

func collinearOverlap(a, b, c, d Pt) bool {
	switch {
	case a.Y == b.Y && c.Y == d.Y && math.Abs(a.Y-c.Y) < dragEps && a.X != b.X && c.X != d.X:
		return math.Min(math.Max(a.X, b.X), math.Max(c.X, d.X))-math.Max(math.Min(a.X, b.X), math.Min(c.X, d.X)) > qEps
	case a.X == b.X && c.X == d.X && math.Abs(a.X-c.X) < dragEps && a.Y != b.Y && c.Y != d.Y:
		return math.Min(math.Max(a.Y, b.Y), math.Max(c.Y, d.Y))-math.Max(math.Min(a.Y, b.Y), math.Min(c.Y, d.Y)) > qEps
	}
	return false
}

// GateResult compares the findings of a planned page with the page before.
type GateResult struct {
	OK          bool      `json:"ok"`
	New         []Finding `json:"new,omitempty"`         // introduced by the edit: block the write
	Preexisting []Finding `json:"preexisting,omitempty"` // already on the page before the edit
}

// GateSchematic is the strict gate for an edit: every finding of the
// planned page must already have been on the page before (counted per
// key); a finding the edit introduces blocks the write.
func GateSchematic(before, planned string, opt CheckOptions) GateResult {
	was := map[string]int{}
	for _, f := range CheckSchematic(before, opt) {
		was[f.Key()]++
	}
	var r GateResult
	for _, f := range CheckSchematic(planned, opt) {
		if was[f.Key()] > 0 {
			was[f.Key()]--
			r.Preexisting = append(r.Preexisting, f)
			continue
		}
		r.New = append(r.New, f)
	}
	r.OK = len(r.New) == 0
	return r
}

// Summary is a one-line list of findings for error messages.
func Summary(fs []Finding, n int) string {
	var s []string
	for i, f := range fs {
		if i == n {
			s = append(s, fmt.Sprintf("… %d more", len(fs)-n))
			break
		}
		s = append(s, f.Message)
	}
	return strings.Join(s, "; ")
}
