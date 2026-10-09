package kicad

// schscene.go — read-side geometry of one .kicad_sch for the planners that
// used to read it from the EasyEDA connector (sch autoconnect / connect,
// sch layout-plan apply): symbol bodies, pins with their outward direction,
// wires, labels, power symbols, texts and the title block; and DragSymbols,
// which moves symbols while wire ends, labels, no-connects, junctions and
// power symbols on their pins follow (one text edit, connectivity kept).

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// ScenePin is a placed pin: sheet point and the direction pointing away
// from its body, in degrees counter-clockwise on screen (0 right, 90 up).
type ScenePin struct {
	Number, Name string
	At           Pt
	Outward      float64
}

// SceneSymbol is one placed symbol instance (unit).
type SceneSymbol struct {
	Ref, LibID string
	Key        string // instance key: Ref, or "Ref:Unit" when the reference has several units here
	Unit       int
	At         Pt
	Rot        float64
	Mirror     string
	Power      bool // power symbol (#PWR…)
	Box        Box  // body + pins; zero when the library symbol is unknown
	HasBox     bool
	Body       Box // graphics only (no pins); valid when HasBox
	Pins       []ScenePin
	Fields     []SceneField // visible, non-empty fields
}

// SceneField is a visible symbol field with an estimated text box.
type SceneField struct {
	Name, Value string
	Box         Box
}

// SceneLabel is a net label of any kind (label, global_label,
// hierarchical_label) with an estimated text box.
type SceneLabel struct {
	Kind, Name string
	At         Pt
	Angle      float64
	Box        Box
}

// SchScene is the geometry of one sheet (mm, y down).
type SchScene struct {
	Paper      string
	Symbols    []SceneSymbol
	Wires      [][2]Pt
	Labels     []SceneLabel
	Texts      []Box
	Junctions  []Pt
	NoConnects []Pt
	TitleBlock *Box // nil when the paper size is unknown
	Page       *Box // the drawing area inside the border; nil when unknown
}

// Scene reads the sheet's existing items (pending edits are not included).
func (e *SchEditor) Scene() (*SchScene, error) {
	sc := &SchScene{}
	if p := e.root.child("paper"); p != nil && len(p.list) > 1 {
		sc.Paper = p.list[1].atom
		for _, pp := range Papers {
			if pp.Name == sc.Paper {
				portrait := len(p.list) > 2 && p.list[2].atom == "portrait"
				w, h := pp.W, pp.H
				if portrait {
					w, h = h, w
				}
				sc.TitleBlock = &Box{w - schBorder - schTitleW, h - schBorder - schTitleH, w - schBorder, h - schBorder}
				sc.Page = &Box{schBorder, schBorder, w - schBorder, h - schBorder}
			}
		}
	}
	libs := map[string]Box{}
	for id, s := range e.libs {
		libs[id] = libBox(s)
	}
	e.bodies = map[string]Box{}
	for id, s := range e.libs {
		e.bodies[id] = libBodyBox(s)
	}
	keys := instKeys(e.root)
	for _, n := range e.root.list {
		switch n.head() {
		case "symbol":
			s, err := e.sceneSymbol(n, libs)
			if err != nil {
				return nil, err
			}
			s.Key = keys[n]
			sc.Symbols = append(sc.Symbols, s)
		case "wire":
			pts := wirePts(n)
			for i := 0; i+1 < len(pts); i++ {
				sc.Wires = append(sc.Wires, [2]Pt{pts[i], pts[i+1]})
			}
		case LabelLocal, LabelGlobal, LabelHier:
			at := n.child("at")
			if at == nil || len(n.list) < 2 {
				continue
			}
			l := SceneLabel{Kind: n.head(), Name: n.list[1].atom, At: Pt{at.num(1), at.num(2)}, Angle: at.num(3)}
			l.Box = textBox(l.At, l.Angle, len([]rune(l.Name)))
			sc.Labels = append(sc.Labels, l)
		case "junction", "no_connect":
			if at := n.child("at"); at != nil {
				if n.head() == "junction" {
					sc.Junctions = append(sc.Junctions, Pt{at.num(1), at.num(2)})
				} else {
					sc.NoConnects = append(sc.NoConnects, Pt{at.num(1), at.num(2)})
				}
			}
		case "text", "text_box":
			if at := n.child("at"); at != nil && len(n.list) > 1 {
				lines := strings.Split(n.list[1].atom, "\n")
				w := 0
				for _, s := range lines {
					w = max(w, len([]rune(s)))
				}
				b := textBox(Pt{at.num(1), at.num(2)}, at.num(3), w)
				b.MinY -= 2.54 * float64(len(lines)-1)
				sc.Texts = append(sc.Texts, b)
			}
		}
	}
	return sc, nil
}

// textBox estimates a 1.27 mm text run from at along angle (≈1 mm/char).
func textBox(at Pt, angle float64, chars int) Box {
	w := 1.0 + 1.0*float64(chars)
	switch math.Mod(math.Mod(angle, 360)+360, 360) {
	case 90:
		return Box{at.X - 1.5, at.Y - w, at.X + 1.5, at.Y + 1}
	case 180:
		return Box{at.X - w, at.Y - 1.5, at.X + 1, at.Y + 1.5}
	case 270:
		return Box{at.X - 1.5, at.Y - 1, at.X + 1.5, at.Y + w}
	}
	return Box{at.X - 1, at.Y - 1.5, at.X + w, at.Y + 1.5}
}

func wirePts(n *sexp) []Pt {
	var out []Pt
	if pts := n.child("pts"); pts != nil {
		for _, p := range pts.list {
			if p.head() == "xy" {
				out = append(out, Pt{p.num(1), p.num(2)})
			}
		}
	}
	return out
}

func (e *SchEditor) sceneSymbol(n *sexp, libs map[string]Box) (SceneSymbol, error) {
	s := SceneSymbol{Ref: symRef(n), Unit: 1}
	if id := n.child("lib_id"); id != nil && len(id.list) > 1 {
		s.LibID = id.list[1].atom
	}
	at := n.child("at")
	if at == nil {
		return s, fmt.Errorf("symbol %s has no position", s.Ref)
	}
	s.At, s.Rot = Pt{at.num(1), at.num(2)}, at.num(3)
	if u := n.child("unit"); u != nil {
		s.Unit = int(u.num(1))
	}
	if m := n.child("mirror"); m != nil && len(m.list) > 1 {
		s.Mirror = m.list[1].atom
	}
	if lib, ok := e.libs[s.LibID]; ok {
		s.Power = lib.child("power") != nil
	}
	s.Box, s.HasBox = instanceBox(n, libs)
	if s.HasBox && e.bodies != nil {
		if bb, ok := e.bodies[s.LibID]; ok && !math.IsInf(bb.MinX, 0) {
			s.Body = boxAt(bb, s.At, s.Rot, s.Mirror)
		} else {
			s.Body = s.Box
		}
	}
	for _, p := range n.list {
		if p.head() != "property" || len(p.list) < 3 || p.list[2].atom == "" || propertyHidden(p) {
			continue
		}
		pa := p.child("at")
		if pa == nil {
			continue
		}
		size := 1.27
		if f := findDeep(p, "size"); f != nil && f.num(1) > 0 {
			size = f.num(1)
		}
		just := ""
		if j := findDeep(p, "justify"); j != nil && len(j.list) > 1 {
			just = j.list[1].atom
		}
		vertical := math.Mod(normAngle(pa.num(3)+s.Rot), 180) == 90
		s.Fields = append(s.Fields, SceneField{Name: p.list[1].atom, Value: p.list[2].atom,
			Box: fieldBox(Pt{pa.num(1), pa.num(2)}, vertical, len([]rune(p.list[2].atom)), size, just)})
	}
	pins, err := e.LibPins(s.LibID)
	if err != nil {
		return s, nil // unknown library symbol: no pins
	}
	s.Pins = placePins(pins, s.Unit, s.At, s.Rot, s.Mirror)
	return s, nil
}

// placePins maps the library pins of unit to the sheet.
func placePins(pins []LibPin, unit int, at Pt, rot float64, mirror string) []ScenePin {
	var out []ScenePin
	for _, p := range pins {
		if p.Unit != 0 && p.Unit != unit {
			continue
		}
		a := p.Angle * math.Pi / 180
		// the pin runs from its connection point into the body along its
		// angle; outward is the opposite way.
		q := Pt{p.At.X - math.Cos(a), p.At.Y - math.Sin(a)}
		pa, qa := SymbolXform(p.At, at, rot, mirror), SymbolXform(q, at, rot, mirror)
		out = append(out, ScenePin{Number: p.Number, Name: p.Name, At: pa,
			Outward: math.Mod(math.Atan2(-(qa.Y-pa.Y), qa.X-pa.X)*180/math.Pi+360, 360)})
	}
	return out
}

// PinsAt returns the sheet pins of library symbol libID placed with unit,
// position, rotation and mirror.
func (e *SchEditor) PinsAt(libID string, unit int, at Pt, rot float64, mirror string) ([]ScenePin, error) {
	pins, err := e.LibPins(libID)
	if err != nil {
		return nil, err
	}
	return placePins(pins, unit, at, rot, mirror), nil
}

// RootSheetFor returns the root sheet of the hierarchy holding path: path
// itself when it is a root sheet, else the root .kicad_sch in its directory
// whose hierarchy includes it.
func RootSheetFor(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	r, err := parseSx(string(b))
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	if r.child("sheet_instances") != nil {
		return path, nil
	}
	abs, _ := filepath.Abs(path)
	others, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.kicad_sch"))
	for _, o := range others {
		ob, err := os.ReadFile(o)
		if err != nil || !strings.Contains(string(ob), "(sheet_instances") {
			continue
		}
		files, err := SheetFiles(o)
		if err != nil {
			continue
		}
		for _, f := range files {
			if fa, _ := filepath.Abs(f); fa == abs {
				return o, nil
			}
		}
	}
	return "", fmt.Errorf("%s is a sub-sheet and no root .kicad_sch next to it references it", path)
}

// libBodyBox is a library symbol's graphics extent (no pins, no fields) in
// its own frame (y up); empty (Inf) when it draws nothing.
func libBodyBox(sym *sexp) Box {
	b := emptyBox()
	var walk func(n *sexp)
	walk = func(n *sexp) {
		switch n.head() {
		case "xy", "start", "end", "center", "mid":
			b.add(n.num(1), n.num(2))
		case "circle":
			if c, r := n.child("center"), n.child("radius"); c != nil && r != nil {
				b.add(c.num(1)-r.num(1), c.num(2)-r.num(1))
				b.add(c.num(1)+r.num(1), c.num(2)+r.num(1))
			}
			return
		case "pin", "property", "text":
			return
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

// boxAt places a library-frame box (y up) at a symbol pose (sheet, y down).
func boxAt(lb Box, at Pt, rot float64, mirror string) Box {
	sb := emptyBox()
	for _, c := range []Pt{{lb.MinX, lb.MinY}, {lb.MaxX, lb.MinY}, {lb.MinX, lb.MaxY}, {lb.MaxX, lb.MaxY}} {
		q := symbolXformRaw(c, at, rot, mirror)
		sb.add(q.X, q.Y)
	}
	return sb
}

// fieldBox estimates the text box of a field (KiCad stroke font, ≈0.8 em
// per character); fields are centred unless justified.
func fieldBox(at Pt, vertical bool, chars int, size float64, just string) Box {
	w, hh := float64(chars)*size*0.8+0.3, size*0.6
	lo, hi := -w/2, w/2
	switch just {
	case "left":
		lo, hi = 0, w
	case "right":
		lo, hi = -w, 0
	}
	if vertical { // reads bottom to top
		return Box{at.X - hh, at.Y - hi, at.X + hh, at.Y - lo}
	}
	return Box{at.X + lo, at.Y - hh, at.X + hi, at.Y + hh}
}

// SymbolBoxAt is the body+pins box of library symbol libID placed at a pose.
func (e *SchEditor) SymbolBoxAt(libID string, at Pt, rot float64, mirror string) (Box, bool) {
	lib, ok := e.libs[libID]
	if !ok {
		return Box{}, false
	}
	lb := libBox(lib)
	if math.IsInf(lb.MinX, 0) {
		return Box{}, false
	}
	return boxAt(lb, at, rot, mirror), true
}
