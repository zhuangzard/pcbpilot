package kicad

// scherc_pin.go — the sheet geometry an ERC report is checked against: the
// item points of a sheet (to tell the report's units on an unknown KiCad
// version) and the exact pin an ERC pin item names (a PWR_FLAG is wired
// there, not at the report's rounded position).

import (
	"fmt"
	"math"
	"strings"
)

// SheetAnchors lists the points of a sheet file an ERC item can sit on:
// symbol origins and pins, wire ends, labels, junctions, no-connects, bus
// entries, sheet origins and sheet pins. An unreadable sheet has none.
func SheetAnchors(file string) []Pt {
	e, err := OpenSchematicFile(file)
	if err != nil {
		return nil
	}
	var out []Pt
	at := func(n *sexp) {
		if a := n.child("at"); a != nil {
			out = append(out, Pt{a.num(1), a.num(2)})
		}
	}
	for _, n := range e.root.list {
		switch n.head() {
		case "symbol":
			at(n)
			pins, _ := e.instPins(n)
			for _, p := range pins {
				out = append(out, p.At)
			}
		case "wire", "bus":
			out = append(out, wirePts(n)...)
		case LabelLocal, LabelGlobal, LabelHier, "junction", "no_connect", "bus_entry", "netclass_flag":
			at(n)
		case "sheet":
			at(n)
			for _, c := range n.list {
				if c.head() == "pin" {
					at(c)
				}
			}
		}
	}
	return out
}

// instPins is the pins of a placed symbol's unit at their sheet points.
func (e *SchEditor) instPins(inst *sexp) ([]LibPin, error) {
	lid := find(inst, "lib_id")
	a := inst.child("at")
	if lid == nil || len(lid.list) < 2 || a == nil {
		return nil, fmt.Errorf("symbol %s has no lib_id/at", symRef(inst))
	}
	pins, err := e.LibPins(lid.list[1].atom)
	if err != nil {
		return nil, err
	}
	unit := 1
	if u := inst.child("unit"); u != nil {
		unit = int(u.num(1))
	}
	mir := ""
	if m := inst.child("mirror"); m != nil && len(m.list) > 1 {
		mir = m.list[1].atom
	}
	var out []LibPin
	for _, p := range pins {
		if p.Unit == 0 || p.Unit == unit {
			p.At = SymbolXform(p.At, Pt{a.num(1), a.num(2)}, a.num(3), mir)
			out = append(out, p)
		}
	}
	return out, nil
}

// instRefs maps each sheet instance path of a placed symbol to its
// reference (the instances block; a repeated sheet gives each instance its
// own).
func instRefs(inst *sexp) map[string]string {
	out := map[string]string{}
	in := inst.child("instances")
	if in == nil {
		return out
	}
	for _, pr := range in.list {
		if pr.head() != "project" {
			continue
		}
		for _, p := range pr.list {
			if p.head() != "path" || len(p.list) < 2 {
				continue
			}
			if r := p.child("reference"); r != nil && len(r.list) > 1 {
				out[p.list[1].atom] = r.list[1].atom
			}
		}
	}
	return out
}

// ERCPinAt is a pin an ERC item names, resolved on its sheet file.
type ERCPinAt struct {
	At Pt
	By string // "uuid" or "reference"
}

// ERCPinPoint finds the pin an ERC pin item names on this sheet. First by
// the item's uuid, which KiCad stores on every placed pin ((pin "3" (uuid
// …))) and which every instance of a repeated sheet shares; then by the
// reference the instance at sheetPath (the violation's uuid path) carries,
// and pin. Several pins with that number (stacked, or a reference on
// several symbols) are told apart by near (the report's position, mm;
// within 1.27 mm), or the pin is ambiguous. ref/pin come from ERCPin.
func (e *SchEditor) ERCPinPoint(uuid, sheetPath, ref, pin string, near *Pt) (ERCPinAt, error) {
	if uuid != "" {
		for _, n := range e.root.list {
			if n.head() != "symbol" {
				continue
			}
			for _, c := range n.list {
				if c.head() != "pin" || len(c.list) < 2 {
					continue
				}
				if u := c.child("uuid"); u == nil || len(u.list) < 2 || !strings.EqualFold(u.list[1].atom, uuid) {
					continue
				}
				pts, err := e.pinPoints(n, c.list[1].atom)
				if err != nil {
					return ERCPinAt{}, err
				}
				p, err := pickPin(pts, near, fmt.Sprintf("pin uuid %s", uuid))
				return ERCPinAt{At: p, By: "uuid"}, err
			}
		}
	}
	var pts []Pt
	found := false
	for _, n := range e.root.list {
		if n.head() != "symbol" {
			continue
		}
		refs := instRefs(n)
		r, ok := refs[sheetPath]
		if !ok && len(refs) == 0 {
			r, ok = propVal(n, "Reference"), true
		}
		if !ok || r != ref {
			continue
		}
		found = true
		p, err := e.pinPoints(n, pin)
		if err != nil {
			continue
		}
		pts = append(pts, p...)
	}
	what := fmt.Sprintf("%s pin %s on sheet instance %s", ref, pin, sheetPath)
	if !found {
		return ERCPinAt{}, fmt.Errorf("no symbol %s", what)
	}
	p, err := pickPin(pts, near, what)
	return ERCPinAt{At: p, By: "reference"}, err
}

// pinPoints is where the pins numbered pin of a placed symbol are.
func (e *SchEditor) pinPoints(inst *sexp, pin string) ([]Pt, error) {
	pins, err := e.instPins(inst)
	if err != nil {
		return nil, err
	}
	var out []Pt
	for _, p := range pins {
		if p.Number == pin {
			out = append(out, p.At)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("symbol %s has no pin %s", symRef(inst), pin)
	}
	return out, nil
}

// pickPin is the one distinct point of pts, or the one within 1.27 mm of
// near when there are several.
func pickPin(pts []Pt, near *Pt, what string) (Pt, error) {
	var uniq []Pt
	for _, p := range pts {
		dup := false
		for _, q := range uniq {
			dup = dup || (math.Abs(p.X-q.X) < 1e-4 && math.Abs(p.Y-q.Y) < 1e-4)
		}
		if !dup {
			uniq = append(uniq, p)
		}
	}
	switch {
	case len(uniq) == 0:
		return Pt{}, fmt.Errorf("%s: no such pin", what)
	case len(uniq) == 1:
		return uniq[0], nil
	case near == nil:
		return Pt{}, fmt.Errorf("%s is ambiguous: %d pins and no usable ERC position", what, len(uniq))
	}
	var hit []Pt
	for _, p := range uniq {
		if math.Hypot(p.X-near.X, p.Y-near.Y) <= 1.27 {
			hit = append(hit, p)
		}
	}
	if len(hit) != 1 {
		return Pt{}, fmt.Errorf("%s is ambiguous: %d pins, %d within 1.27 mm of the ERC position (%s, %s)", what, len(uniq), len(hit), F(near.X), F(near.Y))
	}
	return hit[0], nil
}
