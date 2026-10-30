package kicad

// fpauthor.go — datasheet-driven KiCad footprint authoring from a typed spec.
// Produces .kicad_mod S-expression text. IPC-7351 style margins for courtyard,
// fab, and silk outlines. Pad editing and property setting on existing footprints.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// FootprintSpec describes a footprint to author.
type FootprintSpec struct {
	Name        string    `json:"name"`        // footprint name (e.g. "SOIC-8_3.9x4.9mm_P1.27mm")
	Description string    `json:"description"`
	Tags        string    `json:"tags"`        // space-separated search tags
	Pads        []PadSpec `json:"pads"`        // explicit pad list
	Courtyard   float64   `json:"courtyard"`   // margin mm from bounding box; default 0.25
	SilkMargin  float64   `json:"silkMargin"`  // silk inset from courtyard; default 0.1
	Model3D     string    `json:"model3d"`     // 3D model path or ""
	Attr        string    `json:"attr"`        // "smd", "through_hole", or "" (auto-detect)
}

// PadSpec describes one pad.
type PadSpec struct {
	Number string     `json:"number"` // "1", "EP", "" (NPTH)
	Type   string     `json:"type"`   // smd, thru_hole, np_thru_hole
	Shape  string     `json:"shape"`  // roundrect, rect, circle, oval
	At     [3]float64 `json:"at"`     // x, y, angle (mm, degrees)
	Size   [2]float64 `json:"size"`   // w, h mm
	Drill  *DrillSpec `json:"drill"`  // nil for SMD
	Layers []string   `json:"layers"` // e.g. ["F.Cu","F.Paste","F.Mask"]
	RRatio float64    `json:"rratio"` // roundrect ratio; default 0.25 when shape=roundrect
}

// DrillSpec describes a pad drill.
type DrillSpec struct {
	Diameter float64 `json:"diameter"` // mm (circular drill)
	Width    float64 `json:"width"`    // mm (oval drill width; 0 = use Diameter)
	Height   float64 `json:"height"`   // mm (oval drill height; 0 = use Diameter)
}

const (
	fpFontSize      = 1.0    // mm
	fpSilkStroke    = 0.12   // mm
	fpFabStroke     = 0.1    // mm
	fpCrtYdStroke   = 0.05   // mm
	fpDefaultCrtYd  = 0.25   // mm
	fpDefaultSilkIn = 0.1    // mm
	fpDefaultRRatio = 0.25
	fpCrtYdSnap     = 0.01 // courtyard grid snap (mm)
)

// validPadLayers is the set of KiCad copper/mask/paste/silk/fab/courtyard layer
// names accepted for pad declarations (including wildcards).
var validPadLayers = map[string]bool{
	"F.Cu": true, "B.Cu": true, "*.Cu": true,
	"In1.Cu": true, "In2.Cu": true, "In3.Cu": true, "In4.Cu": true,
	"In5.Cu": true, "In6.Cu": true, "In7.Cu": true, "In8.Cu": true,
	"In9.Cu": true, "In10.Cu": true, "In11.Cu": true, "In12.Cu": true,
	"In13.Cu": true, "In14.Cu": true, "In15.Cu": true, "In16.Cu": true,
	"In17.Cu": true, "In18.Cu": true, "In19.Cu": true, "In20.Cu": true,
	"In21.Cu": true, "In22.Cu": true, "In23.Cu": true, "In24.Cu": true,
	"In25.Cu": true, "In26.Cu": true, "In27.Cu": true, "In28.Cu": true,
	"In29.Cu": true, "In30.Cu": true,
	"F.Paste": true, "B.Paste": true, "*.Paste": true,
	"F.Mask": true, "B.Mask": true, "*.Mask": true,
	"F.SilkS": true, "B.SilkS": true,
	"F.Fab": true, "B.Fab": true,
	"F.CrtYd": true, "B.CrtYd": true,
}

// ValidatePadLayer reports whether s is a valid KiCad pad layer name.
func ValidatePadLayer(s string) bool { return validPadLayers[s] }

// FPFileVersion is the format version for generated .kicad_mod files.
// KiCad 9 reads older versions; we use the widely-compatible KiCad 8 token.
const FPFileVersion = "20240108"

// AuthorFootprint generates a .kicad_mod S-expression from a spec.
func AuthorFootprint(spec FootprintSpec) (string, error) {
	if spec.Name == "" {
		return "", fmt.Errorf("footprint name is required")
	}
	if len(spec.Pads) == 0 {
		return "", fmt.Errorf("footprint must have at least one pad")
	}

	// Validate pads.
	for i, p := range spec.Pads {
		switch p.Type {
		case "smd", "thru_hole", "np_thru_hole":
		case "":
			return "", fmt.Errorf("pad %d (%s): type is required", i, p.Number)
		default:
			return "", fmt.Errorf("pad %d (%s): unknown type %q", i, p.Number, p.Type)
		}
		switch p.Shape {
		case "roundrect", "rect", "circle", "oval":
		case "":
			return "", fmt.Errorf("pad %d (%s): shape is required", i, p.Number)
		case "custom":
			return "", fmt.Errorf("pad %d (%s): custom shape is not supported; use explicit primitives", i, p.Number)
		default:
			return "", fmt.Errorf("pad %d (%s): unknown shape %q", i, p.Number, p.Shape)
		}
		if p.Size[0] <= 0 || p.Size[1] <= 0 {
			return "", fmt.Errorf("pad %d (%s): size must be positive", i, p.Number)
		}
		if p.Type == "thru_hole" || p.Type == "np_thru_hole" {
			if p.Drill == nil {
				return "", fmt.Errorf("pad %d (%s): %s pad requires a drill", i, p.Number, p.Type)
			}
		}
		if p.Type == "smd" && p.Drill != nil {
			return "", fmt.Errorf("pad %d (%s): smd pad cannot have a drill", i, p.Number)
		}
		if p.Drill != nil {
			if err := validateDrill(p.Drill, i, p.Number); err != nil {
				return "", err
			}
		}
		if len(p.Layers) == 0 {
			return "", fmt.Errorf("pad %d (%s): layers is required", i, p.Number)
		}
		for _, l := range p.Layers {
			if !ValidatePadLayer(l) {
				return "", fmt.Errorf("pad %d (%s): unknown layer %q", i, p.Number, l)
			}
		}
	}

	// Range-validate margins and roundrect ratio. (Kimi finding #6)
	if spec.Courtyard < 0 {
		return "", fmt.Errorf("courtyard margin must be non-negative, got %s", F(spec.Courtyard))
	}
	if spec.SilkMargin < 0 {
		return "", fmt.Errorf("silk margin must be non-negative, got %s", F(spec.SilkMargin))
	}

	// Validate per-pad rratio.
	for i, p := range spec.Pads {
		if p.Shape == "roundrect" && p.RRatio < 0 {
			return "", fmt.Errorf("pad %d (%s): roundrect ratio must be non-negative, got %s", i, p.Number, F(p.RRatio))
		}
		if p.Shape == "roundrect" && p.RRatio > 0.5 {
			return "", fmt.Errorf("pad %d (%s): roundrect ratio must be at most 0.5, got %s", i, p.Number, F(p.RRatio))
		}
	}

	// Validate and auto-detect attr.
	attr := spec.Attr
	switch attr {
	case "", "smd", "through_hole":
	default:
		return "", fmt.Errorf("attr must be \"smd\", \"through_hole\", or empty; got %q", attr)
	}
	if attr == "" {
		hasSMD := false
		hasTHT := false
		for _, p := range spec.Pads {
			if p.Type == "smd" {
				hasSMD = true
			}
			if p.Type == "thru_hole" {
				hasTHT = true
			}
		}
		if hasSMD {
			attr = "smd"
		} else if hasTHT {
			attr = "through_hole"
		}
	}

	// Compute bounding box of all pads, accounting for pad rotation.
	var minX, minY, maxX, maxY float64
	for i, p := range spec.Pads {
		hw, hh := padRotatedExtent(p.Size[0], p.Size[1], p.At[2])
		px, py := p.At[0], p.At[1]
		if i == 0 {
			minX, maxX = px-hw, px+hw
			minY, maxY = py-hh, py+hh
		} else {
			minX = math.Min(minX, px-hw)
			maxX = math.Max(maxX, px+hw)
			minY = math.Min(minY, py-hh)
			maxY = math.Max(maxY, py+hh)
		}
	}

	// Courtyard from bounding box of all pads + margin.
	crtYd := spec.Courtyard
	if crtYd == 0 {
		crtYd = fpDefaultCrtYd
	}
	crtMinX := fpSnap(minX-crtYd, fpCrtYdSnap)
	crtMaxX := fpSnap(maxX+crtYd, fpCrtYdSnap)
	crtMinY := fpSnap(minY-crtYd, fpCrtYdSnap)
	crtMaxY := fpSnap(maxY+crtYd, fpCrtYdSnap)

	// Silk from courtyard - silk margin.
	silkIn := spec.SilkMargin
	if silkIn == 0 {
		silkIn = fpDefaultSilkIn
	}
	silkMinX := crtMinX + silkIn
	silkMaxX := crtMaxX - silkIn
	silkMinY := crtMinY + silkIn
	silkMaxY := crtMaxY - silkIn

	// Fab outline = bounding box of pads (body approximation).
	fabMinX, fabMaxX := minX, maxX
	fabMinY, fabMaxY := minY, maxY

	var b strings.Builder

	fmt.Fprintf(&b, "(footprint %s\n", Q(spec.Name))
	fmt.Fprintf(&b, "\t(version %s)\n", FPFileVersion)
	fmt.Fprintf(&b, "\t(generator \"pcbpilot\")\n")
	fmt.Fprintf(&b, "\t(layer \"F.Cu\")\n")

	if spec.Description != "" {
		fmt.Fprintf(&b, "\t(descr %s)\n", Q(spec.Description))
	}
	if spec.Tags != "" {
		fmt.Fprintf(&b, "\t(tags %s)\n", Q(spec.Tags))
	}

	if attr != "" {
		fmt.Fprintf(&b, "\t(attr %s)\n", attr)
	}

	// Properties: Reference on silk, Value on fab.
	fmt.Fprintf(&b, "\t(property \"Reference\" \"REF**\" (at 0 %s 0) (layer \"F.SilkS\") (effects (font (size %s %s) (thickness %s))))\n",
		F(crtMinY-fpFontSize), F(fpFontSize), F(fpFontSize), F(fpSilkStroke))
	fmt.Fprintf(&b, "\t(property \"Value\" %s (at 0 %s 0) (layer \"F.Fab\") (effects (font (size %s %s) (thickness %s))))\n",
		Q(spec.Name), F(crtMaxY+fpFontSize), F(fpFontSize), F(fpFontSize), F(fpFabStroke))
	fmt.Fprintf(&b, "\t(property \"Footprint\" \"\" (at 0 0 0) (layer \"F.Fab\") (hide yes) (effects (font (size %s %s) (thickness %s))))\n",
		F(fpFontSize), F(fpFontSize), F(fpFabStroke))
	fmt.Fprintf(&b, "\t(property \"Datasheet\" \"\" (at 0 0 0) (layer \"F.Fab\") (hide yes) (effects (font (size %s %s) (thickness %s))))\n",
		F(fpFontSize), F(fpFontSize), F(fpFabStroke))
	fmt.Fprintf(&b, "\t(property \"Description\" %s (at 0 0 0) (layer \"F.Fab\") (hide yes) (effects (font (size %s %s) (thickness %s))))\n",
		Q(spec.Description), F(fpFontSize), F(fpFontSize), F(fpFabStroke))

	// Fab outline (F.Fab) — body rectangle with pin-1 chamfer.
	chamfer := math.Min(0.5, math.Min(fabMaxX-fabMinX, fabMaxY-fabMinY)/4)
	// Chamfer the corner nearest pad "1"; default to top-left.
	chamCorner := fpChamferCorner(fpFindPad1(spec.Pads), fabMinX, fabMinY, fabMaxX, fabMaxY)
	fpWriteFabOutline(&b, fabMinX, fabMinY, fabMaxX, fabMaxY, chamfer, chamCorner, fpFabStroke)

	// Silk outline (F.SilkS) — rectangle (slightly inset from courtyard).
	fmt.Fprintf(&b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.SilkS\"))\n",
		F(silkMinX), F(silkMinY), F(silkMaxX), F(silkMinY), F(fpSilkStroke))
	fmt.Fprintf(&b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.SilkS\"))\n",
		F(silkMaxX), F(silkMinY), F(silkMaxX), F(silkMaxY), F(fpSilkStroke))
	fmt.Fprintf(&b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.SilkS\"))\n",
		F(silkMaxX), F(silkMaxY), F(silkMinX), F(silkMaxY), F(fpSilkStroke))
	fmt.Fprintf(&b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.SilkS\"))\n",
		F(silkMinX), F(silkMaxY), F(silkMinX), F(silkMinY), F(fpSilkStroke))

	// Pin 1 dot on silk — find the electrical pad numbered "1".
	if p1 := fpFindPad1(spec.Pads); p1 != nil {
		hw, _ := padRotatedExtent(p1.Size[0], p1.Size[1], p1.At[2])
		dotX := p1.At[0] - hw - 0.3
		dotY := p1.At[1]
		fmt.Fprintf(&b, "\t(fp_circle (center %s %s) (end %s %s) (stroke (width %s) (type solid)) (fill solid) (layer \"F.SilkS\"))\n",
			F(dotX), F(dotY), F(dotX+0.1), F(dotY), F(fpSilkStroke))
	}

	// Courtyard (F.CrtYd).
	fmt.Fprintf(&b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.CrtYd\"))\n",
		F(crtMinX), F(crtMinY), F(crtMaxX), F(crtMinY), F(fpCrtYdStroke))
	fmt.Fprintf(&b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.CrtYd\"))\n",
		F(crtMaxX), F(crtMinY), F(crtMaxX), F(crtMaxY), F(fpCrtYdStroke))
	fmt.Fprintf(&b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.CrtYd\"))\n",
		F(crtMaxX), F(crtMaxY), F(crtMinX), F(crtMaxY), F(fpCrtYdStroke))
	fmt.Fprintf(&b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.CrtYd\"))\n",
		F(crtMinX), F(crtMaxY), F(crtMinX), F(crtMinY), F(fpCrtYdStroke))

	// Pads.
	for _, p := range spec.Pads {
		fpWritePad(&b, p)
	}

	// 3D model.
	if spec.Model3D != "" {
		fmt.Fprintf(&b, "\t(model %s (offset (xyz 0 0 0)) (scale (xyz 1 1 1)) (rotate (xyz 0 0 0)))\n",
			Q(spec.Model3D))
	}

	fmt.Fprintf(&b, ")\n")
	return b.String(), nil
}

// fpWritePad emits one pad S-expression.
func fpWritePad(b *strings.Builder, p PadSpec) {
	sorted := layerList(p.Layers)
	quoted := make([]string, len(sorted))
	for i, l := range sorted {
		quoted[i] = Q(l)
	}
	layers := strings.Join(quoted, " ")
	atStr := fmt.Sprintf("%s %s", F(p.At[0]), F(p.At[1]))
	if p.At[2] != 0 {
		atStr += " " + F(p.At[2])
	}

	fmt.Fprintf(b, "\t(pad %s %s %s (at %s) (size %s %s)",
		Q(p.Number), p.Type, p.Shape, atStr, F(p.Size[0]), F(p.Size[1]))

	if p.Drill != nil {
		d := p.Drill
		if d.Width > 0 && d.Height > 0 && d.Width != d.Height {
			fmt.Fprintf(b, " (drill oval %s %s)", F(d.Width), F(d.Height))
		} else {
			dia := d.Diameter
			if dia == 0 {
				dia = d.Width
			}
			fmt.Fprintf(b, " (drill %s)", F(dia))
		}
	}

	fmt.Fprintf(b, " (layers %s)", layers)

	if p.Shape == "roundrect" {
		rr := p.RRatio
		if rr == 0 {
			rr = fpDefaultRRatio
		}
		fmt.Fprintf(b, " (roundrect_rratio %s)", F(rr))
	}

	fmt.Fprintf(b, ")\n")
}

// layerList returns a sorted copy of layers for deterministic output.
func layerList(layers []string) []string {
	out := make([]string, len(layers))
	copy(out, layers)
	sort.Strings(out)
	return out
}

// padRotatedExtent returns the half-width and half-height of the axis-aligned
// bounding box for a rectangle of size (w, h) rotated by angleDeg degrees.
func padRotatedExtent(w, h, angleDeg float64) (halfW, halfH float64) {
	if angleDeg == 0 {
		return w / 2, h / 2
	}
	rad := angleDeg * math.Pi / 180
	cosA := math.Abs(math.Cos(rad))
	sinA := math.Abs(math.Sin(rad))
	halfW = (w*cosA + h*sinA) / 2
	halfH = (w*sinA + h*cosA) / 2
	return
}

// fpSnap rounds v outward to the nearest multiple of grid.
func fpSnap(v, grid float64) float64 {
	if v >= 0 {
		return math.Ceil(v/grid) * grid
	}
	return math.Floor(v/grid) * grid
}

// ---- Footprint pad editing ----

// PadEdit describes an edit to apply to one pad of an existing footprint.
type PadEdit struct {
	Number    string      `json:"number"`            // pad to find (required unless Index is set)
	Index     int         `json:"index"`             // 0-based pad index (used when numbers are ambiguous)
	UseIndex  bool        `json:"useIndex"`          // if true, select by Index instead of Number
	NewNumber *string     `json:"newNumber"`         // rename pad number; nil = no change
	Size      *[2]float64 `json:"size"`              // new size; nil = no change
	Shape     *string     `json:"shape"`             // new shape; nil = no change
	Drill     *DrillSpec  `json:"drill"`             // new drill; nil = no change
}

// EditFootprintPads applies edits to pads of an existing .kicad_mod text.
// Returns the modified .kicad_mod text.
func EditFootprintPads(src string, edits []PadEdit) (string, error) {
	root, err := parseSx(strings.TrimSpace(src))
	if err != nil {
		return "", fmt.Errorf("parse footprint: %w", err)
	}
	if root.head() != "footprint" {
		return "", fmt.Errorf("not a footprint S-expression (got %q)", root.head())
	}

	// Collect pad nodes.
	var pads []*sexp
	for _, ch := range root.list {
		if ch.head() == "pad" {
			pads = append(pads, ch)
		}
	}

	// Build pad-number-to-count map for ambiguity detection.
	padNumCount := map[string]int{}
	for _, p := range pads {
		if len(p.list) > 1 {
			padNumCount[p.list[1].atom]++
		}
	}

	// Apply edits.
	for _, e := range edits {
		var target *sexp
		if e.UseIndex {
			if e.Index < 0 || e.Index >= len(pads) {
				return "", fmt.Errorf("pad index %d out of range [0..%d)", e.Index, len(pads))
			}
			target = pads[e.Index]
		} else {
			if padNumCount[e.Number] > 1 {
				return "", fmt.Errorf("pad %q is ambiguous (%d pads share this number); use index or --all", e.Number, padNumCount[e.Number])
			}
			for _, p := range pads {
				if len(p.list) > 1 && p.list[1].atom == e.Number {
					target = p
					break
				}
			}
			if target == nil {
				return "", fmt.Errorf("pad %q not found", e.Number)
			}
		}

		if e.NewNumber != nil {
			if len(target.list) > 1 {
				target.list[1].atom = *e.NewNumber
				// Rebuild ambiguity map after rename so subsequent edits
				// see the new pad numbers (Kimi R2 finding).
				padNumCount = map[string]int{}
				for _, p := range pads {
					if len(p.list) > 1 {
						padNumCount[p.list[1].atom]++
					}
				}
			}
		}
		if e.Size != nil {
			if e.Size[0] <= 0 || e.Size[1] <= 0 {
				return "", fmt.Errorf("pad size must be positive")
			}
			if err := fpEditSexpChild(target, "size", fmt.Sprintf("(size %s %s)", F(e.Size[0]), F(e.Size[1]))); err != nil {
				return "", err
			}
		}
		if e.Shape != nil {
			switch *e.Shape {
			case "roundrect", "rect", "circle", "oval":
			default:
				return "", fmt.Errorf("unknown pad shape %q", *e.Shape)
			}
			// Reject shape edits on custom pads: changing shape would leave
			// stale primitives child in place. (Kimi finding #4)
			if len(target.list) > 3 && target.list[3].atom == "custom" {
				return "", fmt.Errorf("cannot change shape of custom pad %q; remove primitives manually first", e.Number)
			}
			hasPrimitives := false
			for _, ch := range target.list {
				if ch.head() == "primitives" {
					hasPrimitives = true
					break
				}
			}
			if hasPrimitives {
				return "", fmt.Errorf("cannot change shape of pad %q with primitives child; remove primitives manually first", e.Number)
			}
			// Shape is the 3rd atom in (pad "num" type shape ...)
			if len(target.list) > 3 {
				target.list[3].atom = *e.Shape
			}
			// Manage shape-specific fields.
			if *e.Shape == "roundrect" {
				// Ensure roundrect_rratio exists.
				hasRR := false
				for _, ch := range target.list {
					if ch.head() == "roundrect_rratio" {
						hasRR = true
						break
					}
				}
				if !hasRR {
					if err := fpEditSexpChild(target, "roundrect_rratio", fmt.Sprintf("(roundrect_rratio %s)", F(fpDefaultRRatio))); err != nil {
						return "", err
					}
				}
			} else {
				// Remove roundrect_rratio if changing away from roundrect.
				var kept []*sexp
				for _, ch := range target.list {
					if ch.head() != "roundrect_rratio" {
						kept = append(kept, ch)
					}
				}
				target.list = kept
			}
		}
		if e.Drill != nil {
			// Validate the drill edit.
			if err := validateDrill(e.Drill, 0, e.Number); err != nil {
				return "", err
			}
			// Validate: THT/NPTH may have drill; SMD must not.
			padType := ""
			if len(target.list) > 2 {
				padType = target.list[2].atom
			}
			if padType == "smd" {
				return "", fmt.Errorf("cannot add drill to smd pad %q", e.Number)
			}
			d := e.Drill
			var drillText string
			if d.Width > 0 && d.Height > 0 && d.Width != d.Height {
				drillText = fmt.Sprintf("(drill oval %s %s)", F(d.Width), F(d.Height))
			} else {
				dia := d.Diameter
				if dia == 0 {
					dia = d.Width
				}
				drillText = fmt.Sprintf("(drill %s)", F(dia))
			}
			if err := fpEditSexpChild(target, "drill", drillText); err != nil {
				return "", err
			}
		}
	}

	return sexpSerialize(root), nil
}

// fpEditSexpChild replaces or adds a child node by name in a parent sexp.
func fpEditSexpChild(parent *sexp, name, text string) error {
	for i, ch := range parent.list {
		if ch.head() == name {
			n, err := parseSx(text)
			if err != nil {
				return fmt.Errorf("parse %s replacement: %w", name, err)
			}
			parent.list[i] = n
			return nil
		}
	}
	// Not found; append.
	n, err := parseSx(text)
	if err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}
	parent.list = append(parent.list, n)
	return nil
}

// SetFootprintProperty sets or adds a property value in a .kicad_mod text.
// Preserves all other child nodes of the property (at, layer, effects, uuid).
func SetFootprintProperty(src, name, value string) (string, error) {
	root, err := parseSx(strings.TrimSpace(src))
	if err != nil {
		return "", fmt.Errorf("parse footprint: %w", err)
	}
	if root.head() != "footprint" {
		return "", fmt.Errorf("not a footprint S-expression (got %q)", root.head())
	}

	found := false
	for _, ch := range root.list {
		if ch.head() == "property" && len(ch.list) > 2 && ch.list[1].atom == name {
			ch.list[2].atom = value
			found = true
			break
		}
	}

	if !found {
		propText := fmt.Sprintf("(property %s %s (at 0 0 0) (layer \"F.Fab\") (hide yes) (effects (font (size %s %s) (thickness %s))))",
			Q(name), Q(value), F(fpFontSize), F(fpFontSize), F(fpFabStroke))
		n, perr := parseSx(propText)
		if perr != nil {
			return "", perr
		}
		root.list = append(root.list, n)
	}

	return sexpSerialize(root), nil
}

// sexpSerialize renders a parsed sexp tree back to text.
// This is a simple re-serializer; it does not preserve original formatting
// but preserves the quoted flag from the parser.
func sexpSerialize(n *sexp) string {
	if n == nil {
		return ""
	}
	if len(n.list) == 0 {
		if n.isList {
			return "()"
		}
		// Preserve quoting: if the atom was originally quoted, re-quote it.
		if n.quoted || strings.ContainsAny(n.atom, " \t\n\r\"\\()") || n.atom == "" {
			return Q(n.atom)
		}
		return n.atom
	}
	var b strings.Builder
	b.WriteByte('(')
	for i, ch := range n.list {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(sexpSerialize(ch))
	}
	b.WriteByte(')')
	return b.String()
}

// validateDrill checks a drill spec for validity. It requires either a
// positive circular diameter OR two positive oval dimensions, rejects
// negative/zero values, and rejects conflicting specs (diameter + oval dims).
func validateDrill(d *DrillSpec, padIdx int, padNum string) error {
	hasCircular := d.Diameter != 0
	hasOval := d.Width != 0 || d.Height != 0
	if hasCircular && hasOval {
		return fmt.Errorf("pad %d (%s): drill must specify either diameter (circular) or width+height (oval), not both", padIdx, padNum)
	}
	if !hasCircular && !hasOval {
		return fmt.Errorf("pad %d (%s): drill diameter must be positive", padIdx, padNum)
	}
	if hasCircular {
		if d.Diameter < 0 {
			return fmt.Errorf("pad %d (%s): drill diameter must be positive, got %s", padIdx, padNum, F(d.Diameter))
		}
	}
	if hasOval {
		if d.Width <= 0 || d.Height <= 0 {
			return fmt.Errorf("pad %d (%s): oval drill requires positive width and height", padIdx, padNum)
		}
	}
	return nil
}

// fpFindPad1 finds the first pad numbered "1" (the electrical pin-1 pad).
// Returns nil when no pad "1" exists.
func fpFindPad1(pads []PadSpec) *PadSpec {
	for i := range pads {
		if pads[i].Number == "1" {
			return &pads[i]
		}
	}
	return nil
}

// fpChamferCorner determines which corner of the fab outline to chamfer,
// based on the position of the pin-1 pad. Returns "tl", "tr", "bl", "br".
func fpChamferCorner(p1 *PadSpec, minX, minY, maxX, maxY float64) string {
	if p1 == nil {
		return "tl" // default
	}
	cx := (minX + maxX) / 2
	cy := (minY + maxY) / 2
	if p1.At[0] <= cx {
		if p1.At[1] <= cy {
			return "tl"
		}
		return "bl"
	}
	if p1.At[1] <= cy {
		return "tr"
	}
	return "br"
}

// fpWriteFabOutline writes the F.Fab body rectangle with a chamfer at the
// specified corner (tl/tr/bl/br).
func fpWriteFabOutline(b *strings.Builder, minX, minY, maxX, maxY, chamfer float64, corner string, stroke float64) {
	// Each corner has 2 segments (edge + chamfer). The non-chamfered corners
	// just get straight edges. We emit lines clockwise from top-left.
	switch corner {
	case "tl":
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX+chamfer), F(minY), F(maxX), F(minY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX), F(minY), F(maxX), F(maxY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX), F(maxY), F(minX), F(maxY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX), F(maxY), F(minX), F(minY+chamfer), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX), F(minY+chamfer), F(minX+chamfer), F(minY), F(stroke))
	case "tr":
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX), F(minY), F(maxX-chamfer), F(minY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX-chamfer), F(minY), F(maxX), F(minY+chamfer), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX), F(minY+chamfer), F(maxX), F(maxY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX), F(maxY), F(minX), F(maxY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX), F(maxY), F(minX), F(minY), F(stroke))
	case "bl":
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX), F(minY), F(maxX), F(minY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX), F(minY), F(maxX), F(maxY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX), F(maxY), F(minX+chamfer), F(maxY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX+chamfer), F(maxY), F(minX), F(maxY-chamfer), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX), F(maxY-chamfer), F(minX), F(minY), F(stroke))
	case "br":
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX), F(minY), F(maxX), F(minY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX), F(minY), F(maxX), F(maxY-chamfer), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX), F(maxY-chamfer), F(maxX-chamfer), F(maxY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(maxX-chamfer), F(maxY), F(minX), F(maxY), F(stroke))
		fmt.Fprintf(b, "\t(fp_line (start %s %s) (end %s %s) (stroke (width %s) (type default)) (layer \"F.Fab\"))\n", F(minX), F(maxY), F(minX), F(minY), F(stroke))
	}
}

// FootprintPadNumbers extracts pad numbers from a .kicad_mod text.
func FootprintPadNumbers(src string) ([]string, error) {
	root, err := parseSx(strings.TrimSpace(src))
	if err != nil {
		return nil, fmt.Errorf("parse footprint: %w", err)
	}
	var nums []string
	for _, ch := range root.list {
		if ch.head() == "pad" && len(ch.list) > 1 {
			nums = append(nums, ch.list[1].atom)
		}
	}
	return nums, nil
}
