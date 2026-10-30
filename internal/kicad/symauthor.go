package kicad

// symauthor.go — datasheet-driven KiCad symbol authoring from a typed spec.
// Builds a complete (symbol ...) S-expression on the 1.27 mm (50 mil) grid,
// suitable for SymbolLibText(). Multi-unit, power pin hiding, 4-side layout.

import (
	"fmt"
	"math"
	"strings"
)

// SymbolSpec describes a symbol to author.
type SymbolSpec struct {
	Name           string      `json:"name"`
	Reference      string      `json:"reference"`      // e.g. "U", "R", "C"
	Value          string      `json:"value"`           // e.g. "ATmega328P"
	Footprint      string      `json:"footprint"`       // e.g. "Package_QFP:TQFP-32"
	Datasheet      string      `json:"datasheet"`       // URL or ""
	Description    string      `json:"description"`     // component description
	LCSC           string      `json:"lcsc"`            // LCSC C-number or ""
	Pins           []SymbolPin `json:"pins"`            // pin table
	Units          int         `json:"units"`           // multi-unit count (0 or 1 = single)
	HidePinNames   bool        `json:"hidePinNames"`    // hide pin names
	PinNamesOffset float64     `json:"pinNamesOffset"`  // mm; 0 = default 1.016
	InBOM          *bool       `json:"inBom"`           // default true
	OnBoard        *bool       `json:"onBoard"`         // default true
}

// SymbolPin is one pin of a symbol spec.
type SymbolPin struct {
	Number string  `json:"number"` // pad number: "1", "A1", ...
	Name   string  `json:"name"`   // functional name
	Type   string  `json:"type"`   // KiCad electrical type
	Side   string  `json:"side"`   // left, right, top, bottom
	Unit   int     `json:"unit"`   // for multi-unit: 1-based unit (0 = common)
	Length float64 `json:"length"` // mm; 0 = default 2.54
}

const (
	symGrid     = 2.54  // mm, standard KiCad symbol grid
	symPinLen   = 2.54  // default pin length
	symFontSize = 1.27  // standard text size
	symStroke   = 0.254 // body outline stroke
)

// symLayout is the body rectangle edges for one unit or the overall symbol.
type symLayout struct {
	left, right, top, bottom float64 // body edges in mm
}

// symPinGroup collects pins by side for one unit.
type symPinGroup struct {
	left, right, top, bottom []SymbolPin
}

// symLayoutForGroup computes the body rectangle from pin counts and name lengths.
func symLayoutForGroup(g *symPinGroup) symLayout {
	nLeft := len(g.left)
	nRight := len(g.right)
	nTop := len(g.top)
	nBottom := len(g.bottom)

	vPins := max(nLeft, nRight)
	if vPins < 1 {
		vPins = 1
	}
	hPins := max(nTop, nBottom)
	if hPins < 1 {
		hPins = 1
	}

	maxNameLen := 0
	for _, pins := range [][]SymbolPin{g.left, g.right} {
		for _, p := range pins {
			if l := len([]rune(p.Name)); l > maxNameLen {
				maxNameLen = l
			}
		}
	}
	halfW := math.Ceil((float64(maxNameLen)*symFontSize+symGrid)/symGrid) * symGrid
	if halfW < 5.08 {
		halfW = 5.08
	}
	hWidth := float64(hPins+1) * symGrid
	if hWidth > halfW*2 {
		halfW = math.Ceil(hWidth/2/symGrid) * symGrid
	}

	halfH := math.Ceil(float64(vPins)/2) * symGrid
	if halfH < symGrid {
		halfH = symGrid
	}

	return symLayout{
		left:   -halfW,
		right:  halfW,
		top:    halfH,
		bottom: -halfH,
	}
}

// AuthorSymbol generates a (symbol ...) S-expression from a spec.
func AuthorSymbol(spec SymbolSpec) (string, error) {
	if spec.Name == "" {
		return "", fmt.Errorf("symbol name is required")
	}
	if len(spec.Pins) == 0 {
		return "", fmt.Errorf("symbol must have at least one pin")
	}
	if spec.Reference == "" {
		spec.Reference = "U"
	}
	if spec.Value == "" {
		spec.Value = spec.Name
	}
	units := spec.Units
	if units < 1 {
		units = 1
	}

	// Validate pins.
	for i, p := range spec.Pins {
		if p.Number == "" {
			return "", fmt.Errorf("pin %d: number is required", i)
		}
		if !ValidPinType(p.Type) && p.Type != "" {
			return "", fmt.Errorf("pin %d (%s): unknown type %q", i, p.Number, p.Type)
		}
		switch p.Side {
		case "", "left", "right", "top", "bottom":
		default:
			return "", fmt.Errorf("pin %d (%s): unknown side %q", i, p.Number, p.Side)
		}
		if p.Unit < 0 || p.Unit > units {
			return "", fmt.Errorf("pin %d (%s): unit %d out of range [0..%d]", i, p.Number, p.Unit, units)
		}
	}

	// Build per-unit pin groups.
	groups := make(map[int]*symPinGroup)
	for _, p := range spec.Pins {
		g, ok := groups[p.Unit]
		if !ok {
			g = &symPinGroup{}
			groups[p.Unit] = g
		}
		side := p.Side
		if side == "" {
			side = "left"
		}
		switch side {
		case "left":
			g.left = append(g.left, p)
		case "right":
			g.right = append(g.right, p)
		case "top":
			g.top = append(g.top, p)
		case "bottom":
			g.bottom = append(g.bottom, p)
		}
	}

	boolYN := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}

	inBOM := true
	if spec.InBOM != nil {
		inBOM = *spec.InBOM
	}
	onBoard := true
	if spec.OnBoard != nil {
		onBoard = *spec.OnBoard
	}

	offset := spec.PinNamesOffset
	if offset == 0 {
		offset = 1.016
	}

	// Compute the overall layout from all pins (for property positions).
	allG := &symPinGroup{}
	for _, p := range spec.Pins {
		side := p.Side
		if side == "" {
			side = "left"
		}
		switch side {
		case "left":
			allG.left = append(allG.left, p)
		case "right":
			allG.right = append(allG.right, p)
		case "top":
			allG.top = append(allG.top, p)
		case "bottom":
			allG.bottom = append(allG.bottom, p)
		}
	}
	overallLayout := symLayoutForGroup(allG)

	propY := func(above bool, layout symLayout) float64 {
		if above {
			return layout.top + symFontSize
		}
		return layout.bottom - symFontSize
	}

	var b strings.Builder

	fmt.Fprintf(&b, "(symbol %s\n", Q(spec.Name))

	if spec.HidePinNames {
		fmt.Fprintf(&b, "\t\t(pin_names (offset %s) (hide yes))\n", F(offset))
	} else {
		fmt.Fprintf(&b, "\t\t(pin_names (offset %s))\n", F(offset))
	}

	fmt.Fprintf(&b, "\t\t(exclude_from_sim no)\n")
	fmt.Fprintf(&b, "\t\t(in_bom %s)\n", boolYN(inBOM))
	fmt.Fprintf(&b, "\t\t(on_board %s)\n", boolYN(onBoard))

	fmt.Fprintf(&b, "\t\t(property \"Reference\" %s (at 0 %s 0) (effects (font (size %s %s))))\n",
		Q(spec.Reference), F(propY(true, overallLayout)), F(symFontSize), F(symFontSize))
	fmt.Fprintf(&b, "\t\t(property \"Value\" %s (at 0 %s 0) (effects (font (size %s %s))))\n",
		Q(spec.Value), F(propY(false, overallLayout)), F(symFontSize), F(symFontSize))
	fmt.Fprintf(&b, "\t\t(property \"Footprint\" %s (at 0 0 0) (hide yes) (effects (font (size %s %s))))\n",
		Q(spec.Footprint), F(symFontSize), F(symFontSize))
	fmt.Fprintf(&b, "\t\t(property \"Datasheet\" %s (at 0 0 0) (hide yes) (effects (font (size %s %s))))\n",
		Q(spec.Datasheet), F(symFontSize), F(symFontSize))
	fmt.Fprintf(&b, "\t\t(property \"Description\" %s (at 0 0 0) (hide yes) (effects (font (size %s %s))))\n",
		Q(spec.Description), F(symFontSize), F(symFontSize))

	if spec.LCSC != "" {
		fmt.Fprintf(&b, "\t\t(property \"LCSC Part #\" %s (at 0 0 0) (hide yes) (effects (font (size %s %s))))\n",
			Q(spec.LCSC), F(symFontSize), F(symFontSize))
	}

	// Separate pins by unit.
	unitPins := make(map[int][]SymbolPin)
	for _, p := range spec.Pins {
		unitPins[p.Unit] = append(unitPins[p.Unit], p)
	}

	// KiCad symbol convention — one child per (unit, body-style) pair:
	// - (symbol "Name_0_1" ...) = common graphics + common pins (unit 0)
	// - (symbol "Name_U_1" ...) = body + pins for unit U (1-based)
	// No _0_2 alternate body style; body and pins share a single child.

	if units == 1 {
		// Single-unit: _0_1 = body only, _1_1 = body + all pins.
		fmt.Fprintf(&b, "\t\t%s\n", symBuildUnitChild(spec.Name, 0, nil, nil, overallLayout))
		fmt.Fprintf(&b, "\t\t%s\n", symBuildUnitChild(spec.Name, 1, spec.Pins, nil, overallLayout))
	} else {
		// Multi-unit: _0_1 = common body + common pins, _U_1 = body + unit pins.
		// Common pins reserve positions in every unit so per-unit pins never
		// coincide with common pin coordinates (Codex+Kimi finding #1).
		// Every declared unit gets a child even if it has no unit-specific pins
		// (Codex+Kimi finding #2).
		commonPins := unitPins[0]
		fmt.Fprintf(&b, "\t\t%s\n", symBuildUnitChild(spec.Name, 0, commonPins, nil, overallLayout))
		for u := 1; u <= units; u++ {
			uPins := unitPins[u]
			fmt.Fprintf(&b, "\t\t%s\n", symBuildUnitChild(spec.Name, u, uPins, commonPins, overallLayout))
		}
	}

	fmt.Fprintf(&b, "\t)")

	return b.String(), nil
}

// symBuildUnitChild builds one child sub-symbol "(symbol Name_U_1 ...)" that
// contains both the body rectangle and the pins. This is the correct KiCad
// structure: one child per (unit, body-style) pair, never two siblings with
// the same unit number.
//
// commonPins (non-nil for per-unit children of multi-unit symbols) lists
// unit-0 pins whose grid slots must be reserved so that per-unit pins are
// placed below (or after) the common-pin region rather than overlapping.
func symBuildUnitChild(name string, unit int, pins, commonPins []SymbolPin, layout symLayout) string {
	var left, right, top, bottom []SymbolPin
	for _, p := range pins {
		side := p.Side
		if side == "" {
			side = "left"
		}
		switch side {
		case "left":
			left = append(left, p)
		case "right":
			right = append(right, p)
		case "top":
			top = append(top, p)
		case "bottom":
			bottom = append(bottom, p)
		}
	}

	// Count common-pin slots to reserve per side so unit pins start after them.
	var reserveLeft, reserveRight, reserveTop, reserveBottom int
	for _, p := range commonPins {
		side := p.Side
		if side == "" {
			side = "left"
		}
		switch side {
		case "left":
			reserveLeft++
		case "right":
			reserveRight++
		case "top":
			reserveTop++
		case "bottom":
			reserveBottom++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "(symbol %s\n", Q(fmt.Sprintf("%s_%d_1", name, unit)))

	// Body rectangle: always for unit 0, and always for unit > 0
	// (every declared unit emits a child).
	fill := "background"
	fmt.Fprintf(&b, "\t\t\t(rectangle (start %s %s) (end %s %s) (stroke (width %s) (type default)) (fill (type %s)))\n",
		F(layout.left), F(layout.top), F(layout.right), F(layout.bottom), F(symStroke), fill)

	// Pins in the same child (not a separate sibling).
	if len(pins) > 0 {
		symWritePins(&b, left, right, top, bottom, layout,
			reserveLeft, reserveRight, reserveTop, reserveBottom)
	}

	fmt.Fprintf(&b, "\t\t)")
	return b.String()
}

// symWritePins emits pin S-expressions for all four sides.
// reserveLeft/Right/Top/Bottom = how many common-pin grid slots to skip
// at the start of each side so per-unit pins don't overlap common pins.
func symWritePins(b *strings.Builder, left, right, top, bottom []SymbolPin, layout symLayout, reserveLeft, reserveRight, reserveTop, reserveBottom int) {
	font := fmt.Sprintf("(effects (font (size %s %s)))", F(symFontSize), F(symFontSize))

	for i, p := range left {
		y := layout.top - symGrid - float64(i+reserveLeft)*symGrid
		x := layout.left - pinLength(p)
		symWritePin(b, p, x, y, 0, pinLength(p), font)
	}

	for i, p := range right {
		y := layout.top - symGrid - float64(i+reserveRight)*symGrid
		x := layout.right + pinLength(p)
		symWritePin(b, p, x, y, 180, pinLength(p), font)
	}

	for i, p := range top {
		x := layout.left + symGrid + float64(i+reserveTop)*symGrid
		y := layout.top + pinLength(p)
		symWritePin(b, p, x, y, 270, pinLength(p), font)
	}

	for i, p := range bottom {
		x := layout.left + symGrid + float64(i+reserveBottom)*symGrid
		y := layout.bottom - pinLength(p)
		symWritePin(b, p, x, y, 90, pinLength(p), font)
	}
}

func pinLength(p SymbolPin) float64 {
	if p.Length > 0 {
		return p.Length
	}
	return symPinLen
}

func symWritePin(b *strings.Builder, p SymbolPin, x, y, angle, length float64, font string) {
	t := p.Type
	if !ValidPinType(t) {
		t = "passive"
	}
	nm := p.Name
	if nm == "" {
		nm = "~"
	}
	fmt.Fprintf(b, "\t\t\t(pin %s line (at %s %s %s) (length %s) (name %s %s) (number %s %s))\n",
		t, F(x), F(y), F(angle), F(length), Q(nm), font, Q(p.Number), font)
}

// AuthorSymbolLib writes a .kicad_sym file with one or more authored symbols.
func AuthorSymbolLib(symbols map[string]string) string {
	return SymbolLibText(symbols)
}
