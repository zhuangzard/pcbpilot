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
	Unit       int
	At         Pt
	Rot        float64
	Mirror     string
	Power      bool // power symbol (#PWR…)
	Box        Box  // body + pins; zero when the library symbol is unknown
	HasBox     bool
	Pins       []ScenePin
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
	TitleBlock *Box // nil when the paper size is unknown
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
			}
		}
	}
	libs := map[string]Box{}
	for id, s := range e.libs {
		libs[id] = libBox(s)
	}
	for _, n := range e.root.list {
		switch n.head() {
		case "symbol":
			s, err := e.sceneSymbol(n, libs)
			if err != nil {
				return nil, err
			}
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

// SymPose is a symbol position and rotation (degrees, counter-clockwise).
type SymPose struct {
	At  Pt
	Rot float64
}

// DragResult counts what DragSymbols changed.
type DragResult struct {
	Symbols    int `json:"symbols"`
	WirePoints int `json:"wirePoints"`
	Labels     int `json:"labels"`
	Powers     int `json:"powerSymbols"`
	Markers    int `json:"markers"` // no-connects and junctions
}

const dragEps = 1e-3

func samePt(a, b Pt) bool { return math.Abs(a.X-b.X) < dragEps && math.Abs(a.Y-b.Y) < dragEps }

// DragSymbols moves the symbols (by reference; mirror kept) to their poses.
// Everything sitting on a moved pin follows it: wire end points, labels,
// no-connect flags, junctions and power symbols. Multi-unit references and
// two moved pins that touch each other are refused.
func (e *SchEditor) DragSymbols(poses map[string]SymPose) (DragResult, error) {
	var res DragResult
	type move struct{ from, to Pt }
	var moves []move
	insts := map[string][]*sexp{}
	for _, n := range e.root.list {
		if n.head() == "symbol" {
			insts[symRef(n)] = append(insts[symRef(n)], n)
		}
	}
	movedNode := map[*sexp]bool{}
	for ref, pose := range poses {
		ns := insts[ref]
		switch {
		case len(ns) == 0:
			return res, fmt.Errorf("no symbol %s on this sheet", ref)
		case len(ns) > 1:
			return res, fmt.Errorf("%s has %d units on this sheet; moving multi-unit symbols is not supported", ref, len(ns))
		}
		n := ns[0]
		s, err := e.sceneSymbol(n, nil)
		if err != nil {
			return res, err
		}
		np, err := e.PinsAt(s.LibID, s.Unit, pose.At, pose.Rot, s.Mirror)
		if err != nil {
			return res, err
		}
		to := map[string]Pt{}
		for _, p := range np {
			to[p.Number] = p.At
		}
		for _, p := range s.Pins {
			moves = append(moves, move{p.At, to[p.Number]})
		}
		e.repl = append(e.repl, symbolMoveEdits(n, s.At, s.Rot, pose.At, pose.Rot)...)
		movedNode[n] = true
		res.Symbols++
	}
	for i := range moves {
		for j := i + 1; j < len(moves); j++ {
			if samePt(moves[i].from, moves[j].from) && !samePt(moves[i].to, moves[j].to) {
				return res, fmt.Errorf("pins at (%s, %s) touch directly and would be torn apart; wire them first", F(moves[i].from.X), F(moves[i].from.Y))
			}
		}
	}
	target := func(p Pt) (Pt, bool) {
		for _, m := range moves {
			if samePt(p, m.from) {
				return m.to, true
			}
		}
		return Pt{}, false
	}
	for _, n := range e.root.list {
		switch h := n.head(); h {
		case "wire":
			if pts := n.child("pts"); pts != nil {
				for _, p := range pts.list {
					if p.head() != "xy" {
						continue
					}
					if t, ok := target(Pt{p.num(1), p.num(2)}); ok {
						e.repl = append(e.repl, textEdit{p.beg, p.end, fmt.Sprintf("(xy %s %s)", F(t.X), F(t.Y))})
						res.WirePoints++
					}
				}
			}
		case LabelLocal, LabelGlobal, LabelHier, "no_connect", "junction":
			at := n.child("at")
			if at == nil {
				continue
			}
			t, ok := target(Pt{at.num(1), at.num(2)})
			if !ok {
				continue
			}
			ang := ""
			if len(at.list) > 3 {
				ang = " " + F(at.num(3))
			}
			e.repl = append(e.repl, textEdit{at.beg, at.end, fmt.Sprintf("(at %s %s%s)", F(t.X), F(t.Y), ang)})
			if h == "no_connect" || h == "junction" {
				res.Markers++
			} else {
				res.Labels++
			}
		case "symbol":
			if movedNode[n] {
				continue
			}
			s, err := e.sceneSymbol(n, nil)
			if err != nil || !s.Power || len(s.Pins) == 0 {
				continue
			}
			t, ok := target(s.Pins[0].At)
			if !ok {
				continue
			}
			d := Pt{t.X - s.Pins[0].At.X, t.Y - s.Pins[0].At.Y}
			e.repl = append(e.repl, symbolMoveEdits(n, s.At, s.Rot, Pt{s.At.X + d.X, s.At.Y + d.Y}, s.Rot)...)
			res.Powers++
		}
	}
	return res, nil
}

// symbolMoveEdits moves a symbol instance from (at, rot) to (to, toRot);
// field positions turn with it about the anchor (their text angle is kept).
func symbolMoveEdits(n *sexp, at Pt, rot float64, to Pt, toRot float64) []textEdit {
	a := n.child("at")
	out := []textEdit{{a.beg, a.end, fmt.Sprintf("(at %s %s %s)", F(to.X), F(to.Y), F(math.Mod(toRot+360, 360)))}}
	d := (toRot - rot) * math.Pi / 180
	c, s := math.Round(math.Cos(d)), math.Round(math.Sin(d))
	for _, p := range n.list {
		if p.head() != "property" {
			continue
		}
		pa := p.child("at")
		if pa == nil {
			continue
		}
		x, y := pa.num(1)-at.X, pa.num(2)-at.Y
		x, y = x*c+y*s, -x*s+y*c // counter-clockwise on screen (y down)
		out = append(out, textEdit{pa.beg, pa.end, fmt.Sprintf("(at %s %s %s)", F(to.X+x), F(to.Y+y), F(pa.num(3)))})
	}
	return out
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
