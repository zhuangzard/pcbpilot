package kicad

// board.go — `pcbpilot kicad board`: headless board setup of a .kicad_pcb
// (outline, stackup, mounting holes, rule areas, copper zones, board text)
// through KiCad's own Python (board.py, go:embed).
//
// Split of work: Go checks everything that does not need the board (units,
// numbers, geometry, the stackup's arithmetic) and turns every shape into
// KiCad primitives (segments, arcs, circles) and a sampled polygon; board.py
// checks what does (enabled layers, nets, an existing outline, items on a
// layer a stackup would remove, overlaps with pads), applies the change with
// pcbnew, saves, reloads and verifies. Both report problems as BoardIssue
// with a code and a fix; nothing is saved when there is one.
//
// Units: mm in KiCad's own frame (x right, y down, as the PCB editor shows
// coordinates). A spec file may say "units": "mm"; any other unit is refused
// rather than converted.

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed board.py
var boardSource []byte

// Board setup error codes (BoardIssue.Code).
const (
	ErrBoardArg           = "E_BOARD_ARG"            // a missing, malformed or out-of-range value
	ErrBoardUnits         = "E_BOARD_UNITS"          // a spec in a unit other than mm
	ErrBoardGeometry      = "E_BOARD_GEOMETRY"       // degenerate / self-intersecting shape, cutout outside the board, overlap
	ErrBoardLayer         = "E_BOARD_LAYER"          // unknown, disabled or wrong kind of layer
	ErrBoardNet           = "E_BOARD_NET"            // a net the board does not have
	ErrBoardOutlineExists = "E_BOARD_OUTLINE_EXISTS" // Edge.Cuts already has an outline (use --replace)
	ErrBoardNoOutline     = "E_BOARD_NO_OUTLINE"     // the op needs a closed board outline
	ErrBoardLayerInUse    = "E_BOARD_LAYER_IN_USE"   // fewer copper layers would drop items
	ErrBoardOverlap       = "E_BOARD_OVERLAP"        // a hole on an existing pad or hole
	ErrBoardRefExists     = "E_BOARD_REF_EXISTS"     // a designator already on the board
	ErrBoardOutExists     = "E_BOARD_OUT_EXISTS"     // --out names an existing board
	ErrBoardVerify        = "E_BOARD_VERIFY"         // the saved board does not read back as written
)

// BoardIssue is one reason a board op was refused.
type BoardIssue struct {
	Code   string `json:"code"`
	Field  string `json:"field,omitempty"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// BoardError is a refused board op: every issue found in one pass. Nothing
// was written.
type BoardError struct {
	Op     string       `json:"op"`
	Issues []BoardIssue `json:"issues"`
}

func (e *BoardError) Error() string {
	var s []string
	for i, is := range e.Issues {
		if i == 12 {
			s = append(s, fmt.Sprintf("… %d more", len(e.Issues)-12))
			break
		}
		m := is.Code + ": "
		if is.Field != "" {
			m += is.Field + ": "
		}
		s = append(s, m+is.Detail)
	}
	return fmt.Sprintf("kicad board %s: %d problem(s): %s", e.Op, len(e.Issues), strings.Join(s, "; "))
}

func bdIssue(code, field, fix, format string, a ...any) BoardIssue {
	return BoardIssue{Code: code, Field: field, Detail: fmt.Sprintf(format, a...), Fix: fix}
}

// XY is a point in mm, KiCad frame (y down).
type XY [2]float64

// BoardRect is an axis-aligned rectangle: top-left corner X,Y (mm), size,
// optional corner radius.
type BoardRect struct {
	X            float64 `json:"x"`
	Y            float64 `json:"y"`
	Width        float64 `json:"width"`
	Height       float64 `json:"height"`
	CornerRadius float64 `json:"cornerRadius,omitempty"`
}

// BoardCircle is a circle by centre and diameter (mm).
type BoardCircle struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Diameter float64 `json:"diameter"`
}

// BoardSlot is a stadium: the centre line X1,Y1 → X2,Y2 and the width (mm).
type BoardSlot struct {
	X1    float64 `json:"x1"`
	Y1    float64 `json:"y1"`
	X2    float64 `json:"x2"`
	Y2    float64 `json:"y2"`
	Width float64 `json:"width"`
}

// BoardShape is one closed shape: exactly one of the fields.
type BoardShape struct {
	Rect    *BoardRect   `json:"rect,omitempty"`
	Polygon []XY         `json:"polygon,omitempty"`
	Circle  *BoardCircle `json:"circle,omitempty"`
	Slot    *BoardSlot   `json:"slot,omitempty"`
}

// BoardPrim is one KiCad graphic primitive (mm): a segment Start→End, an
// arc Start→Mid→End, or a circle Center+Radius.
type BoardPrim struct {
	Kind   string  `json:"kind"` // segment | arc | circle
	Start  *XY     `json:"start,omitempty"`
	Mid    *XY     `json:"mid,omitempty"`
	End    *XY     `json:"end,omitempty"`
	Center *XY     `json:"center,omitempty"`
	Radius float64 `json:"radius,omitempty"`
}

// bdMaxCoordMm bounds every coordinate and size: KiCad stores nm in 32-bit
// integers (±2147 mm); real boards are far inside 1 m.
const bdMaxCoordMm = 1000

// bdArcSamples is the number of polygon points per full circle used for the
// geometric checks (not for the board: KiCad gets true arcs).
const bdArcSamples = 72

func bdFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// checkCoord: a finite coordinate within ±bdMaxCoordMm.
func bdCheckCoord(field string, v float64) []BoardIssue {
	if !bdFinite(v) || math.Abs(v) > bdMaxCoordMm {
		return []BoardIssue{bdIssue(ErrBoardArg, field, "give a coordinate in mm within ±1000",
			"%v is not a coordinate in mm within ±%d", v, bdMaxCoordMm)}
	}
	return nil
}

// checkSize: a finite size in (0, bdMaxCoordMm] (or [0, …] when zero is allowed).
func bdCheckSize(field string, v float64, zeroOK bool) []BoardIssue {
	if !bdFinite(v) || v < 0 || (v == 0 && !zeroOK) || v > bdMaxCoordMm {
		want := "a positive size in mm"
		if zeroOK {
			want = "a size in mm, 0 or more"
		}
		return []BoardIssue{bdIssue(ErrBoardArg, field, "give "+want+" (at most 1000)", "%v is not %s", v, want)}
	}
	return nil
}

// kind names the field that is set ("" when none, "multiple" when several).
func (s BoardShape) kind() string {
	var k []string
	if s.Rect != nil {
		k = append(k, "rect")
	}
	if s.Polygon != nil {
		k = append(k, "polygon")
	}
	if s.Circle != nil {
		k = append(k, "circle")
	}
	if s.Slot != nil {
		k = append(k, "slot")
	}
	switch len(k) {
	case 0:
		return ""
	case 1:
		return k[0]
	}
	return "multiple"
}

// Validate checks one shape; allowed are the kinds the op accepts.
func (s BoardShape) Validate(field string, allowed ...string) []BoardIssue {
	k := s.kind()
	switch {
	case k == "":
		return []BoardIssue{bdIssue(ErrBoardArg, field, "give one of "+strings.Join(allowed, ", "), "no shape")}
	case k == "multiple":
		return []BoardIssue{bdIssue(ErrBoardArg, field, "give exactly one of "+strings.Join(allowed, ", "), "more than one shape in one entry")}
	}
	ok := false
	for _, a := range allowed {
		ok = ok || a == k
	}
	if !ok {
		return []BoardIssue{bdIssue(ErrBoardArg, field, "use one of "+strings.Join(allowed, ", "), "a %s is not accepted here", k)}
	}
	var bad []BoardIssue
	switch k {
	case "rect":
		r := s.Rect
		bad = append(bad, bdCheckCoord(field+".rect.x", r.X)...)
		bad = append(bad, bdCheckCoord(field+".rect.y", r.Y)...)
		bad = append(bad, bdCheckSize(field+".rect.width", r.Width, false)...)
		bad = append(bad, bdCheckSize(field+".rect.height", r.Height, false)...)
		bad = append(bad, bdCheckSize(field+".rect.cornerRadius", r.CornerRadius, true)...)
		if len(bad) == 0 && r.CornerRadius > math.Min(r.Width, r.Height)/2+1e-9 {
			bad = append(bad, bdIssue(ErrBoardGeometry, field+".rect.cornerRadius", "use a radius of at most half the shorter side",
				"corner radius %g mm is more than half the shorter side (%g mm)", r.CornerRadius, math.Min(r.Width, r.Height)))
		}
	case "circle":
		c := s.Circle
		bad = append(bad, bdCheckCoord(field+".circle.x", c.X)...)
		bad = append(bad, bdCheckCoord(field+".circle.y", c.Y)...)
		bad = append(bad, bdCheckSize(field+".circle.diameter", c.Diameter, false)...)
	case "slot":
		sl := s.Slot
		for _, p := range []struct {
			n string
			v float64
		}{{"x1", sl.X1}, {"y1", sl.Y1}, {"x2", sl.X2}, {"y2", sl.Y2}} {
			bad = append(bad, bdCheckCoord(field+".slot."+p.n, p.v)...)
		}
		bad = append(bad, bdCheckSize(field+".slot.width", sl.Width, false)...)
	case "polygon":
		bad = append(bad, bdCheckPolygon(field+".polygon", s.Polygon)...)
	}
	return bad
}

// checkPolygon: ≥ 3 distinct vertices, finite, no repeated consecutive
// vertex, non-zero area, no self-intersection.
func bdCheckPolygon(field string, pts []XY) []BoardIssue {
	if len(pts) < 3 {
		return []BoardIssue{bdIssue(ErrBoardGeometry, field, "give at least 3 points \"x,y x,y x,y\"", "%d point(s): a polygon needs at least 3", len(pts))}
	}
	var bad []BoardIssue
	for i, p := range pts {
		bad = append(bad, bdCheckCoord(fmt.Sprintf("%s[%d].x", field, i), p[0])...)
		bad = append(bad, bdCheckCoord(fmt.Sprintf("%s[%d].y", field, i), p[1])...)
	}
	if len(bad) > 0 {
		return bad
	}
	for i := range pts {
		if bdDist(pts[i], pts[(i+1)%len(pts)]) < 1e-6 {
			return []BoardIssue{bdIssue(ErrBoardGeometry, field, "drop the repeated point (the polygon closes itself)",
				"points %d and %d are the same (%g, %g)", i, (i+1)%len(pts), pts[i][0], pts[i][1])}
		}
	}
	if math.Abs(bdPolyArea(pts)) < 1e-6 {
		return []BoardIssue{bdIssue(ErrBoardGeometry, field, "give the corners of a shape with an area", "the polygon has no area (collinear points)")}
	}
	if i, j, ok := bdSelfIntersection(pts); ok {
		return []BoardIssue{bdIssue(ErrBoardGeometry, field, "list the corners in order around the shape",
			"edges %d and %d cross: the polygon intersects itself", i, j)}
	}
	return nil
}

func bdDist(a, b XY) float64 { return math.Hypot(a[0]-b[0], a[1]-b[1]) }

func bdPolyArea(pts []XY) float64 {
	a := 0.0
	for i := range pts {
		p, q := pts[i], pts[(i+1)%len(pts)]
		a += p[0]*q[1] - q[0]*p[1]
	}
	return a / 2
}

// selfIntersection finds two non-adjacent edges that touch.
func bdSelfIntersection(pts []XY) (int, int, bool) {
	n := len(pts)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if j == i+1 || (i == 0 && j == n-1) {
				continue // adjacent edges share a vertex
			}
			if bdSegsTouch(pts[i], pts[(i+1)%n], pts[j], pts[(j+1)%n]) {
				return i, j, true
			}
		}
	}
	return 0, 0, false
}

func bdCross(o, a, b XY) float64 { return (a[0]-o[0])*(b[1]-o[1]) - (a[1]-o[1])*(b[0]-o[0]) }

func bdOnSeg(a, b, p XY) bool {
	return math.Min(a[0], b[0])-1e-9 <= p[0] && p[0] <= math.Max(a[0], b[0])+1e-9 &&
		math.Min(a[1], b[1])-1e-9 <= p[1] && p[1] <= math.Max(a[1], b[1])+1e-9
}

// segsTouch: segments ab and cd intersect or touch.
func bdSegsTouch(a, b, c, d XY) bool {
	d1, d2, d3, d4 := bdCross(c, d, a), bdCross(c, d, b), bdCross(a, b, c), bdCross(a, b, d)
	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}
	const eps = 1e-12
	return (math.Abs(d1) < eps && bdOnSeg(c, d, a)) || (math.Abs(d2) < eps && bdOnSeg(c, d, b)) ||
		(math.Abs(d3) < eps && bdOnSeg(a, b, c)) || (math.Abs(d4) < eps && bdOnSeg(a, b, d))
}

// pointInPoly: even-odd test (a point on an edge counts as outside).
func bdPointInPoly(pts []XY, p XY) bool {
	in := false
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		a, b := pts[i], pts[j]
		if (a[1] > p[1]) != (b[1] > p[1]) && p[0] < (b[0]-a[0])*(p[1]-a[1])/(b[1]-a[1])+a[0] {
			in = !in
		}
	}
	return in
}

// arcPoints samples the arc around c of radius r from angle a0 sweeping
// sweep radians (KiCad frame), end points included.
func bdArcPoints(c XY, r, a0, sweep float64) []XY {
	n := int(math.Ceil(math.Abs(sweep) / (2 * math.Pi) * bdArcSamples))
	if n < 1 {
		n = 1
	}
	out := make([]XY, 0, n+1)
	for i := 0; i <= n; i++ {
		a := a0 + sweep*float64(i)/float64(n)
		out = append(out, XY{c[0] + r*math.Cos(a), c[1] + r*math.Sin(a)})
	}
	return out
}

// Samples is the shape as a polygon (arcs sampled) for the geometric checks.
func (s BoardShape) Samples() []XY {
	switch s.kind() {
	case "rect":
		r := s.Rect
		x0, y0, x1, y1, rad := r.X, r.Y, r.X+r.Width, r.Y+r.Height, r.CornerRadius
		if rad <= 0 {
			return []XY{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
		}
		var out []XY
		// Corners clockwise on screen (y down): top-right, bottom-right, …
		for _, c := range []struct {
			c  XY
			a0 float64
		}{{XY{x1 - rad, y0 + rad}, -math.Pi / 2}, {XY{x1 - rad, y1 - rad}, 0}, {XY{x0 + rad, y1 - rad}, math.Pi / 2}, {XY{x0 + rad, y0 + rad}, math.Pi}} {
			out = append(out, bdArcPoints(c.c, rad, c.a0, math.Pi/2)...)
		}
		return bdDedup(out)
	case "circle":
		c := s.Circle
		pts := bdArcPoints(XY{c.X, c.Y}, c.Diameter/2, 0, 2*math.Pi)
		return pts[:len(pts)-1]
	case "slot":
		sl := s.Slot
		a, b := XY{sl.X1, sl.Y1}, XY{sl.X2, sl.Y2}
		r := sl.Width / 2
		if bdDist(a, b) < 1e-9 {
			pts := bdArcPoints(a, r, 0, 2*math.Pi)
			return pts[:len(pts)-1]
		}
		ang := math.Atan2(b[1]-a[1], b[0]-a[0])
		out := bdArcPoints(b, r, ang-math.Pi/2, math.Pi)
		return append(out, bdArcPoints(a, r, ang+math.Pi/2, math.Pi)...)
	case "polygon":
		return append([]XY(nil), s.Polygon...)
	}
	return nil
}

func bdDedup(pts []XY) []XY {
	out := pts[:0:0]
	for _, p := range pts {
		if len(out) == 0 || bdDist(out[len(out)-1], p) > 1e-9 {
			out = append(out, p)
		}
	}
	if len(out) > 1 && bdDist(out[0], out[len(out)-1]) <= 1e-9 {
		out = out[:len(out)-1]
	}
	return out
}

func bdXyp(x, y float64) *XY { p := XY{bdRound6(x), bdRound6(y)}; return &p }

// round6 keeps nm resolution (KiCad's internal unit) and drops float noise.
func bdRound6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

func bdSeg(a, b XY) BoardPrim {
	return BoardPrim{Kind: "segment", Start: bdXyp(a[0], a[1]), End: bdXyp(b[0], b[1])}
}

// arcPrim is the arc around c from angle a0 sweeping sweep (radians).
func bdArcPrim(c XY, r, a0, sweep float64) BoardPrim {
	at := func(a float64) *XY { return bdXyp(c[0]+r*math.Cos(a), c[1]+r*math.Sin(a)) }
	return BoardPrim{Kind: "arc", Start: at(a0), Mid: at(a0 + sweep/2), End: at(a0 + sweep)}
}

// Prims is the shape as KiCad graphic primitives (true arcs and circles).
func (s BoardShape) Prims() []BoardPrim {
	switch s.kind() {
	case "rect":
		r := s.Rect
		x0, y0, x1, y1, rad := r.X, r.Y, r.X+r.Width, r.Y+r.Height, r.CornerRadius
		if rad <= 0 {
			c := []XY{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
			return []BoardPrim{bdSeg(c[0], c[1]), bdSeg(c[1], c[2]), bdSeg(c[2], c[3]), bdSeg(c[3], c[0])}
		}
		var out []BoardPrim
		add := func(a, b XY) {
			if bdDist(a, b) > 1e-9 { // a radius of half a side leaves no straight edge
				out = append(out, bdSeg(a, b))
			}
		}
		add(XY{x0 + rad, y0}, XY{x1 - rad, y0})
		out = append(out, bdArcPrim(XY{x1 - rad, y0 + rad}, rad, -math.Pi/2, math.Pi/2))
		add(XY{x1, y0 + rad}, XY{x1, y1 - rad})
		out = append(out, bdArcPrim(XY{x1 - rad, y1 - rad}, rad, 0, math.Pi/2))
		add(XY{x1 - rad, y1}, XY{x0 + rad, y1})
		out = append(out, bdArcPrim(XY{x0 + rad, y1 - rad}, rad, math.Pi/2, math.Pi/2))
		add(XY{x0, y1 - rad}, XY{x0, y0 + rad})
		out = append(out, bdArcPrim(XY{x0 + rad, y0 + rad}, rad, math.Pi, math.Pi/2))
		return out
	case "circle":
		c := s.Circle
		return []BoardPrim{{Kind: "circle", Center: bdXyp(c.X, c.Y), Radius: bdRound6(c.Diameter / 2)}}
	case "slot":
		sl := s.Slot
		a, b := XY{sl.X1, sl.Y1}, XY{sl.X2, sl.Y2}
		r := sl.Width / 2
		if bdDist(a, b) < 1e-9 {
			return []BoardPrim{{Kind: "circle", Center: bdXyp(a[0], a[1]), Radius: bdRound6(r)}}
		}
		ang := math.Atan2(b[1]-a[1], b[0]-a[0])
		nx, ny := -math.Sin(ang)*r, math.Cos(ang)*r // left normal × r
		return []BoardPrim{
			bdSeg(XY{a[0] + nx, a[1] + ny}, XY{b[0] + nx, b[1] + ny}),
			bdArcPrim(b, r, ang+math.Pi/2, -math.Pi),
			bdSeg(XY{b[0] - nx, b[1] - ny}, XY{a[0] - nx, a[1] - ny}),
			bdArcPrim(a, r, ang-math.Pi/2, -math.Pi),
		}
	case "polygon":
		p := s.Polygon
		out := make([]BoardPrim, 0, len(p))
		for i := range p {
			out = append(out, bdSeg(p[i], p[(i+1)%len(p)]))
		}
		return out
	}
	return nil
}

// bboxOf is [minX, minY, maxX, maxY] of points.
func bdBboxOf(pts []XY) [4]float64 {
	b := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for _, p := range pts {
		b[0], b[1] = math.Min(b[0], p[0]), math.Min(b[1], p[1])
		b[2], b[3] = math.Max(b[2], p[0]), math.Max(b[3], p[1])
	}
	return b
}

// checkUnits: a spec's "units" must be empty or mm.
func bdCheckUnits(u string) []BoardIssue {
	if u == "" || strings.EqualFold(u, "mm") {
		return nil
	}
	return []BoardIssue{bdIssue(ErrBoardUnits, "units", "convert the spec to mm (1 mil = 0.0254 mm) and set \"units\": \"mm\"",
		"units %q: kicad board takes mm only", u)}
}

// ── outline ─────────────────────────────────────────────────────────────────

// OutlineSpec is the board outline on Edge.Cuts: one outer shape (rect,
// polygon or circle) and cutouts (any shape) inside it. Without an outer
// shape the cutouts are added to the board's existing outline.
type OutlineSpec struct {
	Units   string `json:"units,omitempty"`
	Outer   BoardShape
	Cutouts []BoardShape `json:"cutouts,omitempty"`
	// Replace removes every board-level Edge.Cuts drawing first; without
	// it an existing outline refuses the op.
	Replace bool `json:"replace,omitempty"`
	// LineWidth of the Edge.Cuts drawings (mm; 0 = 0.05).
	LineWidth float64 `json:"lineWidth,omitempty"`
}

// outlineJSON is OutlineSpec's file form: the outer shape's field inline.
type outlineJSON struct {
	Units     string       `json:"units,omitempty"`
	Rect      *BoardRect   `json:"rect,omitempty"`
	Polygon   []XY         `json:"polygon,omitempty"`
	Circle    *BoardCircle `json:"circle,omitempty"`
	Cutouts   []BoardShape `json:"cutouts,omitempty"`
	Replace   bool         `json:"replace,omitempty"`
	LineWidth float64      `json:"lineWidth,omitempty"`
}

func (o *OutlineSpec) UnmarshalJSON(b []byte) error {
	var j outlineJSON
	if err := bdStrictJSON(b, &j); err != nil {
		return err
	}
	*o = OutlineSpec{Units: j.Units, Outer: BoardShape{Rect: j.Rect, Polygon: j.Polygon, Circle: j.Circle},
		Cutouts: j.Cutouts, Replace: j.Replace, LineWidth: j.LineWidth}
	return nil
}

func (o OutlineSpec) MarshalJSON() ([]byte, error) {
	return json.Marshal(outlineJSON{Units: o.Units, Rect: o.Outer.Rect, Polygon: o.Outer.Polygon, Circle: o.Outer.Circle,
		Cutouts: o.Cutouts, Replace: o.Replace, LineWidth: o.LineWidth})
}

// Validate checks the outline without the board: shapes, cutouts strictly
// inside the outer shape and apart from each other.
func (o OutlineSpec) Validate() []BoardIssue {
	bad := bdCheckUnits(o.Units)
	hasOuter := o.Outer.kind() != ""
	if !hasOuter && len(o.Cutouts) == 0 {
		return append(bad, bdIssue(ErrBoardArg, "outline", "give --rect W,H (with --origin / --corner-radius), --polygon or --circle, and/or cutouts",
			"no outline and no cutouts"))
	}
	if hasOuter {
		bad = append(bad, o.Outer.Validate("outline", "rect", "polygon", "circle")...)
	} else if o.Replace {
		bad = append(bad, bdIssue(ErrBoardArg, "replace", "give the new outline with --replace, or drop --replace to add cutouts", "--replace without an outline"))
	}
	bad = append(bad, bdCheckSize("lineWidth", o.LineWidth, true)...)
	for i, c := range o.Cutouts {
		bad = append(bad, c.Validate(fmt.Sprintf("cutouts[%d]", i), "rect", "polygon", "circle", "slot")...)
	}
	if len(bad) > 0 {
		return bad
	}
	for i, c := range o.Cutouts {
		if hasOuter && !BoardShapeInside(c, o.Outer) {
			bad = append(bad, bdIssue(ErrBoardGeometry, fmt.Sprintf("cutouts[%d]", i), "move or shrink the cutout so it lies wholly inside the outline (a notch in the edge is part of the outline polygon)",
				"the %s cutout is not strictly inside the outline (its edge must stay more than %g mm inside)", c.kind(), BoardTol))
		}
		for j := 0; j < i; j++ {
			if BoardShapesOverlap(c, o.Cutouts[j]) {
				bad = append(bad, bdIssue(ErrBoardGeometry, fmt.Sprintf("cutouts[%d]", i), "merge overlapping cutouts into one polygon or move them apart",
					"overlaps or touches cutouts[%d]", j))
			}
		}
	}
	return bad
}

// boardPayload is one shape as board.py receives it.
// Prims are what pcbnew draws, Edges and Shape the exact boundary and
// shape for board.py's containment checks, Samples a polygon for zones and
// rule areas (which KiCad stores as polygons).
type boardPayload struct {
	Kind    string                 `json:"kind"`
	Shape   BoardShape             `json:"shape"`
	Prims   []BoardPrim            `json:"prims"`
	Edges   []map[string][]float64 `json:"edges"`
	Samples []XY                   `json:"samples"`
	BBox    [4]float64             `json:"bbox"`
}

func bdPayloadOf(s BoardShape) boardPayload {
	pts := s.Samples()
	return boardPayload{Kind: s.kind(), Shape: s, Prims: s.Prims(), Edges: s.edgeJSON(), Samples: pts, BBox: bdBboxOf(pts)}
}

func (o OutlineSpec) payload() map[string]any {
	w := o.LineWidth
	if w == 0 {
		w = 0.05
	}
	m := map[string]any{"replace": o.Replace, "lineWidth": w}
	if o.Outer.kind() != "" {
		m["outer"] = bdPayloadOf(o.Outer)
	}
	var cs []boardPayload
	for _, c := range o.Cutouts {
		cs = append(cs, bdPayloadOf(c))
	}
	m["cutouts"] = cs
	return m
}

// ── stackup ─────────────────────────────────────────────────────────────────

// Dielectric is one dielectric layer of a stackup.
type Dielectric struct {
	Type        string  `json:"type,omitempty"` // core | prepreg
	Thickness   float64 `json:"thickness"`      // mm
	Material    string  `json:"material,omitempty"`
	EpsilonR    float64 `json:"epsilonR,omitempty"`
	LossTangent float64 `json:"lossTangent,omitempty"`
}

// StackupSpec is the physical stackup KiCad keeps in (setup (stackup …)).
// The board thickness is the sum of every layer (solder masks included, as
// KiCad sums it). Give either Thickness (dielectrics split it evenly) or
// every dielectric's thickness.
type StackupSpec struct {
	Units       string       `json:"units,omitempty"`
	Layers      int          `json:"layers"`                // copper layers: 2, 4 or 6
	Thickness   float64      `json:"thickness,omitempty"`   // board, mm (default 1.6)
	OuterCopper float64      `json:"outerCopper,omitempty"` // mm (default 0.035 = 1 oz)
	InnerCopper float64      `json:"innerCopper,omitempty"` // mm (default 0.0175 = 0.5 oz)
	Dielectrics []Dielectric `json:"dielectrics,omitempty"`
	Material    string       `json:"material,omitempty"` // default dielectric material (FR4)
	EpsilonR    float64      `json:"epsilonR,omitempty"` // default 4.5
	LossTangent float64      `json:"lossTangent,omitempty"`
	Finish      string       `json:"finish,omitempty"`    // copper finish (default None)
	MaskColor   string       `json:"maskColor,omitempty"` // default Green
	SilkColor   string       `json:"silkColor,omitempty"` // default White
	MaskThick   float64      `json:"maskThickness,omitempty"`
}

// bdStackupMask is KiCad's default solder mask thickness (mm).
const bdStackupMask = 0.01

// KiCad's copper finish names (the stackup editor's list), and the
// spellings accepted for them.
var bdFinishNames = []string{"None", "HAL SnPb", "HAL lead-free", "ENIG", "ENEPIG", "Hard gold", "Immersion tin",
	"Immersion nickel", "Immersion silver", "Immersion gold", "HT_OSP", "OSP", "User defined"}

var bdFinishAliases = map[string]string{
	"hasl": "HAL SnPb", "hasl-lead": "HAL SnPb", "hal": "HAL SnPb",
	"hasl-lead-free": "HAL lead-free", "lead-free-hasl": "HAL lead-free", "lf-hasl": "HAL lead-free", "hasl-lf": "HAL lead-free",
	"hard-gold": "Hard gold", "immersion-tin": "Immersion tin", "immersion-nickel": "Immersion nickel",
	"immersion-silver": "Immersion silver", "immersion-gold": "Immersion gold", "ht-osp": "HT_OSP",
}

// FinishName is KiCad's spelling of a copper finish ("" if unknown).
func FinishName(s string) string {
	if s == "" {
		return "None"
	}
	for _, n := range bdFinishNames {
		if strings.EqualFold(n, s) {
			return n
		}
	}
	return bdFinishAliases[strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "-"))]
}

// stackupLayer is one row of the generated stackup.
type stackupLayer struct {
	Name, Type        string
	Thickness         float64 // mm, 0 = none written
	Material, Color   string
	EpsilonR, LossTan float64
	Dielectric        bool
}

// stackup is the spec with defaults applied (after Validate).
func (s StackupSpec) withDefaults() StackupSpec {
	if s.OuterCopper == 0 {
		s.OuterCopper = 0.035
	}
	if s.InnerCopper == 0 {
		s.InnerCopper = 0.0175
	}
	if s.Material == "" {
		s.Material = "FR4"
	}
	if s.EpsilonR == 0 {
		s.EpsilonR = 4.5
	}
	if s.LossTangent == 0 {
		s.LossTangent = 0.02
	}
	if s.MaskColor == "" {
		s.MaskColor = "Green"
	}
	if s.SilkColor == "" {
		s.SilkColor = "White"
	}
	if s.MaskThick == 0 {
		s.MaskThick = bdStackupMask
	}
	s.Finish = FinishName(s.Finish)
	return s
}

func (s StackupSpec) copperSum() float64 {
	return 2*s.OuterCopper + float64(s.Layers-2)*s.InnerCopper
}

// Validate checks the stackup's numbers and that the layers add up.
func (s StackupSpec) Validate() []BoardIssue {
	bad := bdCheckUnits(s.Units)
	if s.Layers != 2 && s.Layers != 4 && s.Layers != 6 {
		bad = append(bad, bdIssue(ErrBoardArg, "layers", "use --layers 2, 4 or 6", "%d copper layers: kicad board stackup builds 2, 4 or 6", s.Layers))
	}
	for _, f := range []struct {
		n     string
		v, hi float64
	}{{"thickness", s.Thickness, 10}, {"outerCopper", s.OuterCopper, 0.5}, {"innerCopper", s.InnerCopper, 0.5}, {"maskThickness", s.MaskThick, 0.2}} {
		if !bdFinite(f.v) || f.v < 0 || f.v > f.hi {
			bad = append(bad, bdIssue(ErrBoardArg, f.n, fmt.Sprintf("give mm between 0 and %g (0 = default)", f.hi), "%v mm is out of range", f.v))
		}
	}
	if !bdFinite(s.EpsilonR) || s.EpsilonR < 0 || s.EpsilonR > 20 {
		bad = append(bad, bdIssue(ErrBoardArg, "epsilonR", "give a relative permittivity between 1 and 20 (FR4 ≈ 4.5)", "%v is out of range", s.EpsilonR))
	}
	if !bdFinite(s.LossTangent) || s.LossTangent < 0 || s.LossTangent > 1 {
		bad = append(bad, bdIssue(ErrBoardArg, "lossTangent", "give a loss tangent between 0 and 1 (FR4 ≈ 0.02)", "%v is out of range", s.LossTangent))
	}
	if s.Finish != "" && FinishName(s.Finish) == "" {
		bad = append(bad, bdIssue(ErrBoardArg, "finish", "use one of: "+strings.Join(bdFinishNames, ", ")+" (or hasl, lead-free-hasl, enig, osp)",
			"unknown copper finish %q", s.Finish))
	}
	for _, f := range []struct{ n, v string }{{"material", s.Material}, {"maskColor", s.MaskColor}, {"silkColor", s.SilkColor}} {
		if strings.ContainsAny(f.v, "\"\\()\n\r") {
			bad = append(bad, bdIssue(ErrBoardArg, f.n, "use plain text without quotes, backslashes or parentheses", "%q", f.v))
		}
	}
	if len(bad) > 0 {
		return bad
	}
	d := s.withDefaults()
	if len(s.Dielectrics) > 0 {
		if len(s.Dielectrics) != s.Layers-1 {
			return append(bad, bdIssue(ErrBoardArg, "dielectrics", fmt.Sprintf("give %d dielectric thicknesses for %d copper layers", s.Layers-1, s.Layers),
				"%d dielectric(s) for %d copper layers", len(s.Dielectrics), s.Layers))
		}
		sum := 0.0
		for i, de := range s.Dielectrics {
			f := fmt.Sprintf("dielectrics[%d]", i)
			if !bdFinite(de.Thickness) || de.Thickness < 0.025 || de.Thickness > 5 {
				bad = append(bad, bdIssue(ErrBoardArg, f+".thickness", "give mm between 0.025 and 5", "%v mm", de.Thickness))
			}
			if de.Type != "" && de.Type != "core" && de.Type != "prepreg" {
				bad = append(bad, bdIssue(ErrBoardArg, f+".type", "use core or prepreg", "type %q", de.Type))
			}
			if strings.ContainsAny(de.Material, "\"\\()\n\r") {
				bad = append(bad, bdIssue(ErrBoardArg, f+".material", "use plain text", "%q", de.Material))
			}
			if !bdFinite(de.EpsilonR) || de.EpsilonR < 0 || de.EpsilonR > 20 || !bdFinite(de.LossTangent) || de.LossTangent < 0 || de.LossTangent > 1 {
				bad = append(bad, bdIssue(ErrBoardArg, f, "epsilonR 1–20, lossTangent 0–1", "dielectric constants out of range"))
			}
			sum += de.Thickness
		}
		total := sum + d.copperSum() + 2*d.MaskThick
		if len(bad) == 0 && s.Thickness > 0 && math.Abs(total-s.Thickness) > 0.001 {
			bad = append(bad, bdIssue(ErrBoardArg, "thickness", "drop --thickness (it follows from the layers) or change the dielectrics",
				"the layers add up to %.4f mm, not %.4f mm", total, s.Thickness))
		}
		return bad
	}
	total := s.Thickness
	if total == 0 {
		total = 1.6
	}
	if each := (total - d.copperSum() - 2*d.MaskThick) / float64(s.Layers-1); each < 0.05 {
		bad = append(bad, bdIssue(ErrBoardArg, "thickness", "give a thicker board or thinner copper",
			"%.3f mm leaves %.3f mm per dielectric after copper and mask (at least 0.05 mm)", total, each))
	}
	return bad
}

// copperName is KiCad's name of copper layer i (0 = F.Cu) of n.
func bdCopperName(i, n int) string {
	switch i {
	case 0:
		return "F.Cu"
	case n - 1:
		return "B.Cu"
	}
	return fmt.Sprintf("In%d.Cu", i)
}

// layers is the full stackup top to bottom (after Validate).
func (s StackupSpec) layers() []stackupLayer {
	d := s.withDefaults()
	diel := append([]Dielectric(nil), d.Dielectrics...)
	if len(diel) == 0 {
		total := d.Thickness
		if total == 0 {
			total = 1.6
		}
		left := total - d.copperSum() - 2*d.MaskThick
		each := math.Round(left/float64(d.Layers-1)*1e6) / 1e6
		for i := 0; i < d.Layers-1; i++ {
			diel = append(diel, Dielectric{Thickness: each})
		}
		// The middle dielectric (the core) takes the rounding remainder, so
		// the layers add up to the asked thickness to the nm.
		mid := (d.Layers - 1) / 2
		diel[mid].Thickness = math.Round((left-each*float64(d.Layers-2))*1e6) / 1e6
	}
	for i := range diel {
		if diel[i].Type == "" {
			diel[i].Type = "core"
			if d.Layers > 2 && i%2 == 0 {
				diel[i].Type = "prepreg" // F.Cu–In1 and In(n-2)–B.Cu are pressed prepreg on a core stack
			}
		}
		if diel[i].Material == "" {
			diel[i].Material = d.Material
		}
		if diel[i].EpsilonR == 0 {
			diel[i].EpsilonR = d.EpsilonR
		}
		if diel[i].LossTangent == 0 {
			diel[i].LossTangent = d.LossTangent
		}
	}
	out := []stackupLayer{
		{Name: "F.SilkS", Type: "Top Silk Screen", Color: d.SilkColor},
		{Name: "F.Paste", Type: "Top Solder Paste"},
		{Name: "F.Mask", Type: "Top Solder Mask", Color: d.MaskColor, Thickness: d.MaskThick},
	}
	for i := 0; i < d.Layers; i++ {
		cu := d.InnerCopper
		if i == 0 || i == d.Layers-1 {
			cu = d.OuterCopper
		}
		out = append(out, stackupLayer{Name: bdCopperName(i, d.Layers), Type: "copper", Thickness: cu})
		if i < d.Layers-1 {
			de := diel[i]
			out = append(out, stackupLayer{Name: fmt.Sprintf("dielectric %d", i+1), Type: de.Type, Thickness: de.Thickness,
				Material: de.Material, EpsilonR: de.EpsilonR, LossTan: de.LossTangent, Dielectric: true})
		}
	}
	return append(out,
		stackupLayer{Name: "B.Mask", Type: "Bottom Solder Mask", Color: d.MaskColor, Thickness: d.MaskThick},
		stackupLayer{Name: "B.Paste", Type: "Bottom Solder Paste"},
		stackupLayer{Name: "B.SilkS", Type: "Bottom Silk Screen", Color: d.SilkColor})
}

// TotalThickness is the board thickness the stackup adds up to (mm).
func (s StackupSpec) TotalThickness() float64 {
	t := 0.0
	for _, l := range s.layers() {
		t += l.Thickness
	}
	return math.Round(t*1e6) / 1e6
}

func bdFmtMm(v float64) string { return strconv.FormatFloat(math.Round(v*1e6)/1e6, 'f', -1, 64) }

// StackupBlock is the (stackup …) s-expression KiCad 10 keeps in the
// board's (setup …) section, as pcbnew writes it.
func (s StackupSpec) StackupBlock() string {
	var b strings.Builder
	b.WriteString("(stackup\n")
	for _, l := range s.layers() {
		fmt.Fprintf(&b, "\t(layer %q\n\t\t(type %q)\n", l.Name, l.Type)
		if l.Color != "" {
			fmt.Fprintf(&b, "\t\t(color %q)\n", l.Color)
		}
		if l.Thickness > 0 {
			fmt.Fprintf(&b, "\t\t(thickness %s)\n", bdFmtMm(l.Thickness))
		}
		if l.Dielectric {
			fmt.Fprintf(&b, "\t\t(material %q)\n\t\t(epsilon_r %s)\n\t\t(loss_tangent %s)\n", l.Material, bdFmtMm(l.EpsilonR), bdFmtMm(l.LossTan))
		}
		b.WriteString("\t)\n")
	}
	fmt.Fprintf(&b, "\t(copper_finish %q)\n\t(dielectric_constraints no)\n)", s.withDefaults().Finish)
	return b.String()
}

// StackupRow is one stackup layer in the report.
type StackupRow struct {
	Name      string  `json:"name"`
	Type      string  `json:"type"`
	Thickness float64 `json:"thickness,omitempty"`
	Material  string  `json:"material,omitempty"`
}

// Rows is the stackup for the report.
func (s StackupSpec) Rows() []StackupRow {
	var out []StackupRow
	for _, l := range s.layers() {
		out = append(out, StackupRow{Name: l.Name, Type: l.Type, Thickness: l.Thickness, Material: l.Material})
	}
	return out
}

func (s StackupSpec) payload() map[string]any {
	d := s.withDefaults()
	var rows []map[string]any
	for _, l := range s.layers() {
		r := map[string]any{"name": l.Name, "type": l.Type, "thickness": bdRound6(l.Thickness)}
		if l.Dielectric {
			r["material"], r["epsilonR"], r["lossTangent"] = l.Material, l.EpsilonR, l.LossTan
		}
		rows = append(rows, r)
	}
	return map[string]any{"layers": d.Layers, "thickness": s.TotalThickness(), "block": s.StackupBlock(), "finish": d.Finish, "rows": rows}
}

// ── holes ───────────────────────────────────────────────────────────────────

// HoleSpec is one mounting hole: a locked, board-only footprint with one pad.
type HoleSpec struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Drill float64 `json:"drill"`
	// Pad is the copper ring diameter of a PTH (required for pth; an NPTH
	// has none).
	Pad  float64 `json:"pad,omitempty"`
	Type string  `json:"type,omitempty"` // npth (default) | pth
	Net  string  `json:"net,omitempty"`  // pth only
	Ref  string  `json:"ref,omitempty"`  // default: next free H<n>
}

// HolesSpec is a batch of mounting holes.
type HolesSpec struct {
	Units string     `json:"units,omitempty"`
	Holes []HoleSpec `json:"holes"`
}

// Validate checks each hole and that the holes do not overlap each other.
func (h HolesSpec) Validate() []BoardIssue {
	bad := bdCheckUnits(h.Units)
	if len(h.Holes) == 0 {
		return append(bad, bdIssue(ErrBoardArg, "holes", "give --at X,Y (repeatable) with --drill", "no holes"))
	}
	refs := map[string]int{}
	for i, x := range h.Holes {
		f := fmt.Sprintf("holes[%d]", i)
		bad = append(bad, bdCheckCoord(f+".x", x.X)...)
		bad = append(bad, bdCheckCoord(f+".y", x.Y)...)
		bad = append(bad, bdCheckSize(f+".drill", x.Drill, false)...)
		bad = append(bad, bdCheckSize(f+".pad", x.Pad, true)...)
		switch x.Type {
		case "", "npth":
			if x.Net != "" {
				bad = append(bad, bdIssue(ErrBoardArg, f+".net", "use --type pth (a plated hole with a ring) for a hole on a net", "an NPTH has no copper and cannot be on net %s", x.Net))
			}
			if x.Pad != 0 && math.Abs(x.Pad-x.Drill) > 1e-9 {
				bad = append(bad, bdIssue(ErrBoardArg, f+".pad", "drop --pad for an NPTH, or use --type pth", "an NPTH has no copper ring (pad %g mm, drill %g mm)", x.Pad, x.Drill))
			}
		case "pth":
			if x.Pad == 0 {
				bad = append(bad, bdIssue(ErrBoardArg, f+".pad", "give --pad (KiCad's M3 mounting pad is 6.4 mm for a 3.2 mm drill)", "a PTH needs a pad diameter"))
			} else if x.Pad < x.Drill+0.1 {
				bad = append(bad, bdIssue(ErrBoardArg, f+".pad", "make the pad at least 0.1 mm larger than the drill (an annular ring of 0.05 mm or more)",
					"pad %g mm leaves no annular ring around drill %g mm", x.Pad, x.Drill))
			}
		default:
			bad = append(bad, bdIssue(ErrBoardArg, f+".type", "use npth or pth", "type %q", x.Type))
		}
		if x.Ref != "" {
			if strings.ContainsAny(x.Ref, " \t\r\n\"\\()") {
				bad = append(bad, bdIssue(ErrBoardArg, f+".ref", "use a designator like H5", "designator %q has spaces, quotes or parentheses", x.Ref))
			} else if j, dup := refs[x.Ref]; dup {
				bad = append(bad, bdIssue(ErrBoardRefExists, f+".ref", "give each hole its own designator", "%s is also holes[%d]", x.Ref, j))
			}
			refs[x.Ref] = i
		}
	}
	if len(bad) > 0 {
		return bad
	}
	for i, a := range h.Holes {
		for j := 0; j < i; j++ {
			b := h.Holes[j]
			if math.Hypot(a.X-b.X, a.Y-b.Y) < (a.outer()+b.outer())/2 {
				bad = append(bad, bdIssue(ErrBoardOverlap, fmt.Sprintf("holes[%d]", i), "move the holes apart",
					"overlaps holes[%d] at (%g, %g)", j, b.X, b.Y))
			}
		}
	}
	return bad
}

// outer is the hole's copper / drill diameter.
func (h HoleSpec) outer() float64 { return math.Max(h.Drill, h.Pad) }

func (h HolesSpec) payload() map[string]any {
	var hs []map[string]any
	for _, x := range h.Holes {
		t := x.Type
		if t == "" {
			t = "npth"
		}
		pad := x.Pad
		if t == "npth" {
			pad = x.Drill
		}
		c := BoardShape{Circle: &BoardCircle{X: x.X, Y: x.Y, Diameter: pad}}
		hs = append(hs, map[string]any{"x": x.X, "y": x.Y, "drill": x.Drill, "pad": pad, "type": t, "net": x.Net, "ref": x.Ref,
			"shape": c, "edges": c.edgeJSON()})
	}
	return map[string]any{"holes": hs}
}

// ── keepout ─────────────────────────────────────────────────────────────────

// bdKeepoutRules are what a rule area can forbid, in KiCad's terms.
var bdKeepoutRules = []string{"tracks", "vias", "pads", "pours", "footprints"}

// KeepoutSpec is a rule area: a shape on copper layers that forbids some
// of tracks, vias, pads, pours, footprints (all of them by default).
type KeepoutSpec struct {
	Units  string     `json:"units,omitempty"`
	Shape  BoardShape `json:"shape"`
	Layers []string   `json:"layers,omitempty"` // copper layer names; default every copper layer ("*.Cu")
	Forbid []string   `json:"forbid,omitempty"` // default all
	Name   string     `json:"name,omitempty"`
}

func (k KeepoutSpec) forbid() []string {
	if len(k.Forbid) == 0 {
		return bdKeepoutRules
	}
	return k.Forbid
}

// Validate checks the shape and the rule names.
func (k KeepoutSpec) Validate() []BoardIssue {
	bad := bdCheckUnits(k.Units)
	bad = append(bad, k.Shape.Validate("shape", "rect", "polygon", "circle")...)
	seen := map[string]bool{}
	for i, f := range k.Forbid {
		ok := false
		for _, r := range bdKeepoutRules {
			ok = ok || r == f
		}
		if !ok {
			bad = append(bad, bdIssue(ErrBoardArg, fmt.Sprintf("forbid[%d]", i), "use "+strings.Join(bdKeepoutRules, ", "), "unknown rule %q", f))
		} else if seen[f] {
			bad = append(bad, bdIssue(ErrBoardArg, fmt.Sprintf("forbid[%d]", i), "name each rule once", "%s twice", f))
		}
		seen[f] = true
	}
	bad = append(bad, bdCheckName("name", k.Name)...)
	bad = append(bad, bdCheckLayerNames("layers", k.Layers)...)
	return bad
}

func bdCheckName(field, s string) []BoardIssue {
	if strings.ContainsAny(s, "\r\n") || len(s) > 200 {
		return []BoardIssue{bdIssue(ErrBoardArg, field, "use one line of at most 200 characters", "%q", s)}
	}
	return nil
}

// checkLayerNames: non-empty, no repeats (the board checks they exist).
func bdCheckLayerNames(field string, ls []string) []BoardIssue {
	var bad []BoardIssue
	seen := map[string]bool{}
	for i, l := range ls {
		if strings.TrimSpace(l) == "" {
			bad = append(bad, bdIssue(ErrBoardLayer, fmt.Sprintf("%s[%d]", field, i), "name a layer like F.Cu", "empty layer name"))
		} else if seen[l] {
			bad = append(bad, bdIssue(ErrBoardLayer, fmt.Sprintf("%s[%d]", field, i), "name each layer once", "%s twice", l))
		}
		seen[l] = true
	}
	return bad
}

func (k KeepoutSpec) payload() map[string]any {
	ls := k.Layers
	if len(ls) == 0 {
		ls = []string{"*.Cu"}
	}
	return map[string]any{"shape": bdPayloadOf(k.Shape), "layers": ls, "forbid": k.forbid(), "name": k.Name}
}

// ── zone ────────────────────────────────────────────────────────────────────

// Zone pad connections (KiCad's ZONE_CONNECTION).
var bdZoneConnections = []string{"thermal", "solid", "none", "tht-thermal"}

// ZoneSpec is a copper pour.
type ZoneSpec struct {
	Units string `json:"units,omitempty"`
	// Shape, or BoardOutline: the board's outline (its largest closed
	// Edge.Cuts polygon).
	Shape        BoardShape `json:"shape,omitempty"`
	BoardOutline bool       `json:"boardOutline,omitempty"`
	Net          string     `json:"net"`
	Layers       []string   `json:"layers"`
	Clearance    float64    `json:"clearance,omitempty"`  // mm, default 0.25
	MinWidth     float64    `json:"minWidth,omitempty"`   // mm, default 0.25
	ThermalGap   float64    `json:"thermalGap,omitempty"` // mm, default 0.5
	SpokeWidth   float64    `json:"spokeWidth,omitempty"` // mm, default 0.5
	Connection   string     `json:"connection,omitempty"` // thermal (default) | solid | none | tht-thermal
	Priority     int        `json:"priority,omitempty"`   // higher fills first
	Fill         *bool      `json:"fill,omitempty"`       // default true: fill every zone with pcbnew
	Name         string     `json:"name,omitempty"`
}

// Validate checks the zone without the board.
func (z ZoneSpec) Validate() []BoardIssue {
	bad := bdCheckUnits(z.Units)
	switch {
	case z.BoardOutline && z.Shape.kind() != "":
		bad = append(bad, bdIssue(ErrBoardArg, "shape", "give either --board-outline or a shape", "both a shape and the board outline"))
	case !z.BoardOutline:
		bad = append(bad, z.Shape.Validate("shape", "rect", "polygon", "circle")...)
	}
	if strings.TrimSpace(z.Net) == "" {
		bad = append(bad, bdIssue(ErrBoardNet, "net", "give --net NAME (a net on the board, e.g. GND)", "a copper zone needs a net"))
	}
	if len(z.Layers) == 0 {
		bad = append(bad, bdIssue(ErrBoardLayer, "layers", "give --layers, e.g. F.Cu or F.Cu,B.Cu", "no copper layer"))
	}
	bad = append(bad, bdCheckLayerNames("layers", z.Layers)...)
	for _, f := range []struct {
		n string
		v float64
	}{{"clearance", z.Clearance}, {"minWidth", z.MinWidth}, {"thermalGap", z.ThermalGap}, {"spokeWidth", z.SpokeWidth}} {
		if !bdFinite(f.v) || f.v < 0 || f.v > 10 {
			bad = append(bad, bdIssue(ErrBoardArg, f.n, "give mm between 0 and 10 (0 = default)", "%v mm", f.v))
		}
	}
	if z.Connection != "" {
		ok := false
		for _, c := range bdZoneConnections {
			ok = ok || c == z.Connection
		}
		if !ok {
			bad = append(bad, bdIssue(ErrBoardArg, "connection", "use "+strings.Join(bdZoneConnections, ", "), "pad connection %q", z.Connection))
		}
	}
	if z.Priority < 0 || z.Priority > 1000 {
		bad = append(bad, bdIssue(ErrBoardArg, "priority", "give a priority between 0 and 1000", "%d", z.Priority))
	}
	return append(bad, bdCheckName("name", z.Name)...)
}

func (z ZoneSpec) payload() map[string]any {
	def := func(v, d float64) float64 {
		if v == 0 {
			return d
		}
		return v
	}
	conn := z.Connection
	if conn == "" {
		conn = "thermal"
	}
	fill := z.Fill == nil || *z.Fill
	m := map[string]any{"net": z.Net, "layers": z.Layers, "clearance": def(z.Clearance, 0.25), "minWidth": def(z.MinWidth, 0.25),
		"thermalGap": def(z.ThermalGap, 0.5), "spokeWidth": def(z.SpokeWidth, 0.5), "connection": conn,
		"priority": z.Priority, "fill": fill, "name": z.Name, "boardOutline": z.BoardOutline}
	if !z.BoardOutline {
		m["shape"] = bdPayloadOf(z.Shape)
	}
	return m
}

// ── text ────────────────────────────────────────────────────────────────────

// TextSpec is one board text on a silkscreen or fab layer.
type TextSpec struct {
	Units     string  `json:"units,omitempty"`
	Text      string  `json:"text"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Layer     string  `json:"layer,omitempty"`     // default F.Silkscreen
	Size      float64 `json:"size,omitempty"`      // glyph height mm (default 1)
	Thickness float64 `json:"thickness,omitempty"` // stroke mm (default 0.15)
	Rotation  float64 `json:"rotation,omitempty"`  // degrees CCW
	Justify   string  `json:"justify,omitempty"`   // left | center (default) | right
	// Mirror: nil = mirrored on a bottom layer (KiCad's default), else as given.
	Mirror *bool `json:"mirror,omitempty"`
}

// Validate checks the text without the board.
func (t TextSpec) Validate() []BoardIssue {
	bad := bdCheckUnits(t.Units)
	if strings.TrimSpace(t.Text) == "" {
		bad = append(bad, bdIssue(ErrBoardArg, "text", "give --text", "empty text"))
	} else if len(t.Text) > 2000 {
		bad = append(bad, bdIssue(ErrBoardArg, "text", "keep board text under 2000 characters", "%d characters", len(t.Text)))
	}
	bad = append(bad, bdCheckCoord("x", t.X)...)
	bad = append(bad, bdCheckCoord("y", t.Y)...)
	for _, f := range []struct {
		n     string
		v, hi float64
	}{{"size", t.Size, 50}, {"thickness", t.Thickness, 5}} {
		if !bdFinite(f.v) || f.v < 0 || f.v > f.hi {
			bad = append(bad, bdIssue(ErrBoardArg, f.n, fmt.Sprintf("give mm between 0 and %g (0 = default)", f.hi), "%v mm", f.v))
		}
	}
	if !bdFinite(t.Rotation) {
		bad = append(bad, bdIssue(ErrBoardArg, "rotation", "give degrees", "%v", t.Rotation))
	}
	switch t.Justify {
	case "", "left", "center", "right":
	default:
		bad = append(bad, bdIssue(ErrBoardArg, "justify", "use left, center or right", "%q", t.Justify))
	}
	return bad
}

func (t TextSpec) payload() map[string]any {
	d := func(v, def float64) float64 {
		if v == 0 {
			return def
		}
		return v
	}
	layer, j := t.Layer, t.Justify
	if layer == "" {
		layer = "F.Silkscreen"
	}
	if j == "" {
		j = "center"
	}
	m := map[string]any{"text": t.Text, "x": t.X, "y": t.Y, "layer": layer, "size": d(t.Size, 1), "thickness": d(t.Thickness, 0.15),
		"rotation": t.Rotation, "justify": j}
	if t.Mirror != nil {
		m["mirror"] = *t.Mirror
	}
	return m
}

// ── new board ───────────────────────────────────────────────────────────────

// NewBoardSpec is an empty board.
type NewBoardSpec struct {
	Layers int `json:"layers"` // 2 (default), 4 or 6
}

func (n NewBoardSpec) Validate() []BoardIssue {
	if n.Layers != 0 && n.Layers != 2 && n.Layers != 4 && n.Layers != 6 {
		return []BoardIssue{bdIssue(ErrBoardArg, "layers", "use --layers 2, 4 or 6", "%d copper layers", n.Layers)}
	}
	return nil
}

func (n NewBoardSpec) payload() map[string]any {
	l := n.Layers
	if l == 0 {
		l = 2
	}
	return map[string]any{"layers": l}
}

// ── specs and running ──────────────────────────────────────────────────────

// BoardOpSpec is any of the op specs.
type BoardOpSpec interface {
	Validate() []BoardIssue
	payload() map[string]any
}

// strictJSON decodes b into v refusing unknown fields and trailing data.
func bdStrictJSON(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return fmt.Errorf("trailing data after the JSON object")
	}
	return nil
}

// ParseBoardSpec reads a spec file for op into v (unknown fields refused).
func ParseBoardSpec(op string, data []byte, v any) error {
	if err := bdStrictJSON(data, v); err != nil {
		return &BoardError{Op: op, Issues: []BoardIssue{bdIssue(ErrBoardArg, "spec", "fix the spec JSON (see `pcbpilot kicad board "+op+" --help` for the fields, all in mm)",
			"%v", err)}}
	}
	return nil
}

// BoardResult is board.py's answer to an op that was applied and saved.
type BoardResult struct {
	OK       bool              `json:"ok"`
	Op       string            `json:"op"`
	Created  []BoardItem       `json:"created,omitempty"`
	Removed  []BoardItem       `json:"removed,omitempty"`
	Warnings []string          `json:"warnings,omitempty"`
	Summary  map[string]any    `json:"summary,omitempty"`
	Verify   BoardVerification `json:"verify"`
	Issues   []BoardIssue      `json:"problems,omitempty"`
	Error    string            `json:"error,omitempty"`
	KiCad    string            `json:"kicadVersion,omitempty"`
}

// BoardItem is one board object an op made or removed.
type BoardItem struct {
	Kind string `json:"kind"`
	UUID string `json:"uuid"`
	Name string `json:"name,omitempty"`
}

// BoardVerification is the reload check board.py ran on the saved file.
type BoardVerification struct {
	Status string   `json:"status"` // pass | fail
	Detail []string `json:"detail,omitempty"`
}

// boardScript writes board.py to the user cache (content-addressed).
func boardScript() (string, error) {
	sum := sha256.Sum256(boardSource)
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "pcbpilot", "kicad")
	path := filepath.Join(dir, "board-"+hex.EncodeToString(sum[:6])+".py")
	if b, err := os.ReadFile(path); err == nil && bytes.Equal(b, boardSource) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, "board-*.tmp")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(boardSource); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		// Another process may have written the same content.
		if b, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(b, boardSource) {
			return path, nil
		}
		return "", err
	}
	return path, nil
}

// BoardOp validates spec, then runs board.py op on pcb ("" for new) and
// saves out. A refusal (Go or board.py) is a *BoardError and out is not
// written; out holds the verified board otherwise.
func (t *Tools) BoardOp(op string, spec BoardOpSpec, pcb, out string) (*BoardResult, error) {
	if bad := spec.Validate(); len(bad) > 0 {
		return nil, &BoardError{Op: op, Issues: bad}
	}
	script, err := boardScript()
	if err != nil {
		return nil, fmt.Errorf("kicad board: %w", err)
	}
	dir, err := os.MkdirTemp("", "pcbpilot-board-spec")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	blob, err := json.Marshal(spec.payload())
	if err != nil {
		return nil, err
	}
	sp := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(sp, blob, 0o644); err != nil {
		return nil, err
	}
	if pcb == "" {
		pcb = "-"
	}
	ctx, cancel := context.WithTimeout(context.Background(), BridgeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.Python, script, op, pcb, sp, out)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	runErr := cmd.Run()
	raw, perr := parseBridgeOutput(stdout.Bytes())
	if perr != nil {
		why := perr.Error()
		if runErr != nil {
			why = runErr.Error() + "; " + why
		}
		return nil, fmt.Errorf("kicad board %s: %s%s", op, why, stderrTail(stderr.Bytes()))
	}
	var r BoardResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("kicad board %s: %w", op, err)
	}
	if len(r.Issues) > 0 {
		return &r, &BoardError{Op: op, Issues: r.Issues}
	}
	if !r.OK {
		return &r, fmt.Errorf("kicad board %s: %s%s", op, r.Error, stderrTail(stderr.Bytes()))
	}
	return &r, nil
}

// BoardStats is the part of `kicad-cli pcb export stats --format json` the
// board ops check.
type BoardStats struct {
	HasOutline bool    `json:"hasOutline"`
	Width      float64 `json:"width"`     // mm
	Height     float64 `json:"height"`    // mm
	Thickness  float64 `json:"thickness"` // mm
	NPTH       int     `json:"npth"`
	PTH        int     `json:"throughHolePads"`
}

// Stats runs kicad-cli pcb export stats on pcb.
func (t *Tools) Stats(pcb string) (*BoardStats, error) {
	dir, err := os.MkdirTemp("", "pcbpilot-stats")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "stats.json")
	ctx, cancel := context.WithTimeout(context.Background(), BridgeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.CLI, "pcb", "export", "stats", "--format", "json", "--units", "mm", "-o", out, pcb)
	cmd.Env = SchCLIEnv()
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kicad-cli pcb export stats: %v: %s", err, strings.TrimSpace(buf.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("kicad-cli pcb export stats wrote no report: %w", err)
	}
	return ParseStats(data)
}

// ParseStats reads a kicad-cli stats JSON report (values like "1.6000 mm").
func ParseStats(data []byte) (*BoardStats, error) {
	var raw struct {
		Board map[string]any `json:"board"`
		Pads  map[string]any `json:"pads"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("stats report: %w", err)
	}
	if raw.Board == nil {
		return nil, fmt.Errorf("stats report: no board section")
	}
	num := func(v any) (float64, error) {
		switch x := v.(type) {
		case float64:
			return x, nil
		case string:
			f := strings.Fields(x)
			if len(f) == 0 {
				return 0, fmt.Errorf("empty value")
			}
			if len(f) > 1 && f[1] != "mm" {
				return 0, fmt.Errorf("value %q is not in mm", x)
			}
			return strconv.ParseFloat(f[0], 64)
		}
		return 0, fmt.Errorf("value %v is not a number", v)
	}
	s := &BoardStats{}
	s.HasOutline, _ = raw.Board["has_outline"].(bool)
	var err error
	for k, dst := range map[string]*float64{"width": &s.Width, "height": &s.Height, "board_thickness": &s.Thickness} {
		v, ok := raw.Board[k]
		if !ok {
			continue // a board without an outline has no size
		}
		if *dst, err = num(v); err != nil {
			return nil, fmt.Errorf("stats report board.%s: %w", k, err)
		}
	}
	if v, ok := raw.Pads["npth"].(float64); ok {
		s.NPTH = int(v)
	}
	if v, ok := raw.Pads["through_hole"].(float64); ok {
		s.PTH = int(v)
	}
	return s, nil
}

// BoardDRC is kicad-cli's error-level DRC of pcb with the isolated KiCad
// config (empty global library tables, kicadenv.go): loading every stock
// footprint library for the library checks takes minutes, or hangs, on a
// Windows install, and the board ops compare two runs of the same board
// that differ only by the op's objects, so the library checks (warnings)
// do not take part.
func (t *Tools) BoardDRC(pcb, out string) (*DRCReport, error) {
	ctx, cancel := context.WithTimeout(context.Background(), BridgeTimeout)
	defer cancel()
	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: cannot remove the previous report: %v", ErrDRCReport, err)
	}
	cmd := exec.CommandContext(ctx, t.CLI, "pcb", "drc", "--format", "json", "--severity-error", "-o", out, pcb)
	cmd.Env = SchCLIEnv()
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	data, rerr := os.ReadFile(out)
	if err != nil || rerr != nil {
		if err == nil {
			err = rerr
		}
		return nil, fmt.Errorf("%w: kicad-cli pcb drc: %v: %s", ErrDRCReport, err, strings.TrimSpace(buf.String()))
	}
	return ParseDRC(data)
}
