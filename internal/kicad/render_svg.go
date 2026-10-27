package kicad

// render_svg.go — normalising, parsing and comparing the SVGs of
// `kicad render` (render.go).

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

var (
	svgTitleDateRE = regexp.MustCompile(`(<title>[^<]*?) date [0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+(Z|[+-][0-9:]+)?\s*(</title>)`)
	svgTitleWSRE   = regexp.MustCompile(`\s+</title>`)
	// Numeric attributes and path data: the only place "-0" is rewritten.
	svgNumAttrRE = regexp.MustCompile(`(\s(?:d|points|x|y|x1|y1|x2|y2|cx|cy|r|rx|ry|width|height|viewBox|transform|textLength|font-size|stroke-width)=")([^"]*)(")`)
	svgNegZeroRE = regexp.MustCompile(`(^|[^0-9.eE])-(0(?:\.0+)?)([^0-9.]|$)`)
	// The generated header: the <title> that opens the root <svg> and the
	// <desc> that directly follows it.
	svgRootRE   = regexp.MustCompile(`<svg[\s>]`)
	svgHeaderRE = regexp.MustCompile(`^\s*<title>[^<]*</title>(\s*<desc>[^<]*</desc>)?`)
)

// NormalizeSVG makes a kicad-cli SVG byte-identical across runs without
// touching what it draws:
//   - LF line ends;
//   - the plot date dropped from <title>, and every temp directory removed
//     from the generated header (<title> and the <desc> after it);
//   - in the drawing itself, temp directory from is shown as to (the
//     directory the user knows), so a path field reads as it would in KiCad;
//   - "-0" written "0" in numeric attributes and path data only.
//
// <text> content is never rewritten except for the from → to path mapping.
func NormalizeSVG(b []byte, from []string, to string) []byte {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	forms := pathForms(from)
	s = mapHeader(s, func(h string) string {
		h = svgTitleDateRE.ReplaceAllString(h, "$1$3")
		h = svgTitleWSRE.ReplaceAllString(h, "</title>")
		for _, f := range forms {
			for _, sep := range []string{`\\`, `\`, `/`} {
				h = strings.ReplaceAll(h, f+sep, "")
			}
			h = strings.ReplaceAll(h, f, ".")
		}
		return h
	})
	if to != "" {
		for _, f := range forms {
			s = strings.ReplaceAll(s, f, to)
		}
	}
	return []byte(mapTags(s, func(tag string) string {
		return svgNumAttrRE.ReplaceAllStringFunc(tag, func(a string) string {
			m := svgNumAttrRE.FindStringSubmatch(a)
			v := m[2]
			for range 2 { // a match eats its right neighbour: twice catches "-0 -0"
				v = svgNegZeroRE.ReplaceAllString(v, "$1$2$3")
			}
			return m[1] + v + m[3]
		})
	}))
}

// mapTags applies f to every element start tag of an XML text ("<name …>",
// quote-aware) and leaves character data, comments, CDATA, processing
// instructions, declarations and end tags as they are.
func mapTags(s string, f func(string) string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		j := strings.IndexByte(s[i:], '<')
		if j < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+j])
		i += j
		rest := s[i:]
		skipTo := func(end string) {
			k := strings.Index(rest, end)
			if k < 0 {
				k = len(rest) - len(end)
			}
			b.WriteString(rest[:k+len(end)])
			i += k + len(end)
		}
		switch {
		case strings.HasPrefix(rest, "<!--"):
			skipTo("-->")
			continue
		case strings.HasPrefix(rest, "<![CDATA["):
			skipTo("]]>")
			continue
		case strings.HasPrefix(rest, "<?"):
			skipTo("?>")
			continue
		}
		// A tag runs to the first '>' outside quotes.
		end, quote := -1, byte(0)
		for k := 1; k < len(rest); k++ {
			c := rest[k]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
			} else if c == '"' || c == '\'' {
				quote = c
			} else if c == '>' {
				end = k
				break
			}
		}
		if end < 0 {
			b.WriteString(rest)
			break
		}
		tag := rest[:end+1]
		if len(tag) > 1 && tag[1] != '/' && tag[1] != '!' {
			tag = f(tag)
		}
		b.WriteString(tag)
		i += end + 1
	}
	return b.String()
}

// mapHeader applies f to the generated header only: a <title> (and the
// <desc> after it) standing first inside the root <svg>. Titles anywhere
// else are drawing content and are left alone.
func mapHeader(s string, f func(string) string) string {
	loc := svgRootRE.FindStringIndex(s)
	if loc == nil {
		return s
	}
	end := strings.IndexByte(s[loc[0]:], '>')
	if end < 0 {
		return s
	}
	at := loc[0] + end + 1
	m := svgHeaderRE.FindStringIndex(s[at:])
	if m == nil {
		return s
	}
	return s[:at] + f(s[at:at+m[1]]) + s[at+m[1]:]
}

// pathForms are the spellings of each directory KiCad may print (native,
// forward-slash, JSON-escaped), longest first; bare roots are skipped.
func pathForms(dirs []string) []string {
	seen := map[string]bool{}
	var forms []string
	for _, d := range dirs {
		if d = strings.TrimRight(d, `\/`); len(d) <= 3 {
			continue
		}
		for _, f := range []string{d, filepath.ToSlash(d), strings.ReplaceAll(d, `\`, `\\`), strings.ReplaceAll(d, `/`, `\`)} {
			if !seen[f] {
				seen[f] = true
				forms = append(forms, f)
			}
		}
	}
	sort.SliceStable(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return forms
}

// svgDrawn are the SVG elements that put ink on the page.
var svgDrawn = map[string]bool{"svg": true, "path": true, "circle": true, "rect": true, "line": true, "polyline": true,
	"polygon": true, "ellipse": true, "text": true, "image": true, "use": true}

var svgSpaceRE = regexp.MustCompile(`\s+`)

// svgHidden are containers whose children are not drawn where they stand.
var svgHidden = map[string]bool{"defs": true, "symbol": true, "clipPath": true, "mask": true, "marker": true, "pattern": true}

var svgNumRE = regexp.MustCompile(`[-+]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][-+]?[0-9]+)?`)

// svgGeometry reports whether one element, by its own attributes and text,
// has something to draw: a path with coordinates, a line of non-zero
// length, a rect / circle / ellipse with size, a poly with two points, text
// with content, an image with a source.
func svgGeometry(name string, attr map[string]string, text string) bool {
	num := func(k string) float64 {
		v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(attr[k]), "mm"), 64)
		if err != nil {
			return 0
		}
		return v
	}
	switch name {
	case "path":
		return svgPathDraws(attr["d"])
	case "line":
		return num("x1") != num("x2") || num("y1") != num("y2")
	case "rect":
		return num("width") > 0 && num("height") > 0
	case "circle":
		return num("r") > 0
	case "ellipse":
		return num("rx") > 0 && num("ry") > 0
	case "polyline", "polygon":
		return len(svgNumRE.FindAllString(attr["points"], 4)) == 4
	case "text":
		return strings.TrimSpace(text) != ""
	case "image":
		return strings.TrimSpace(attr["href"]) != ""
	}
	return false
}

// svgFillArea reports whether a fill can show for this element: it must
// be able to enclose area — a path with a curve / arc or three
// non-collinear points, a polygon / polyline with three non-collinear
// points, a sized rect / circle / ellipse, text glyphs or an image. A
// straight path, a two-point polygon or a line have none.
func svgFillArea(name string, attr map[string]string) bool {
	switch name {
	case "rect", "circle", "ellipse", "text", "image":
		return true // svgGeometry already required a non-zero size / content
	case "polygon", "polyline":
		var pts [][2]float64
		nums := svgNumRE.FindAllString(attr["points"], -1)
		for i := 0; i+1 < len(nums); i += 2 {
			x, _ := strconv.ParseFloat(nums[i], 64)
			y, _ := strconv.ParseFloat(nums[i+1], 64)
			pts = append(pts, [2]float64{x, y})
		}
		return svgNonCollinear(pts)
	case "path":
		pts, curve := svgPathPoints(attr["d"])
		return curve || svgNonCollinear(pts)
	}
	return false
}

// svgNonCollinear: some three of pts span a non-zero area.
func svgNonCollinear(pts [][2]float64) bool {
	if len(pts) < 3 {
		return false
	}
	a := pts[0]
	for i := 1; i < len(pts); i++ {
		b := pts[i]
		if b == a {
			continue
		}
		for _, c := range pts[i+1:] {
			if cross := (b[0]-a[0])*(c[1]-a[1]) - (b[1]-a[1])*(c[0]-a[0]); cross > 1e-12 || cross < -1e-12 {
				return true
			}
		}
	}
	return false
}

// svgPathPoints follows path data (absolute and relative M/L/H/V/Z) and
// returns the points it visits, and whether it has a curve or arc
// (C/S/Q/T/A), which encloses area of its own.
func svgPathPoints(d string) ([][2]float64, bool) {
	var pts [][2]float64
	var cmd byte
	var cx, cy, sx, sy float64
	var args []float64
	curve := false
	flush := func() {
		rel := cmd >= 'a'
		up := cmd &^ 0x20
		switch up {
		case 'M', 'L':
			for i := 0; i+1 < len(args); i += 2 {
				x, y := args[i], args[i+1]
				if rel {
					x, y = cx+x, cy+y
				}
				cx, cy = x, y
				if up == 'M' && i == 0 {
					sx, sy = x, y
				}
				pts = append(pts, [2]float64{x, y})
			}
		case 'H':
			for _, x := range args {
				if rel {
					x += cx
				}
				cx = x
				pts = append(pts, [2]float64{cx, cy})
			}
		case 'V':
			for _, y := range args {
				if rel {
					y += cy
				}
				cy = y
				pts = append(pts, [2]float64{cx, cy})
			}
		case 'Z':
			cx, cy = sx, sy
		case 'C', 'S', 'Q', 'T', 'A':
			curve = true
		}
		args = args[:0]
	}
	for _, t := range svgPathTokRE.FindAllString(d, -1) {
		if c := t[0]; (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			if cmd != 0 {
				flush()
			}
			cmd = c
			continue
		}
		f, _ := strconv.ParseFloat(t, 64)
		args = append(args, f)
	}
	if cmd != 0 {
		flush()
	}
	return pts, curve
}

var (
	svgPathTokRE = regexp.MustCompile(`[A-Za-z]|[-+]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][-+]?[0-9]+)?`)
	svgPathSepRE = regexp.MustCompile(`^[\s,]*$`)
	// Arguments per repetition of each path command.
	svgPathArgs = map[byte]int{'M': 2, 'L': 2, 'T': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'A': 7, 'Z': 0}
)

// svgPathDraws parses path data and reports whether it is well-formed
// (starts with a move, every command has whole argument groups, nothing
// but numbers, commands and separators) and holds at least one drawing
// segment: L/H/V/C/S/Q/T/A, or extra coordinate pairs after a move (implicit
// lines). A move alone, or a move closed with Z, draws nothing.
func svgPathDraws(d string) bool {
	locs := svgPathTokRE.FindAllStringIndex(d, -1)
	last := 0
	for _, l := range locs {
		if !svgPathSepRE.MatchString(d[last:l[0]]) {
			return false // stray characters
		}
		last = l[1]
	}
	if !svgPathSepRE.MatchString(d[last:]) {
		return false
	}
	draws := false
	var cmd byte
	n := 0
	flush := func() bool {
		if cmd == 0 {
			return true
		}
		want := svgPathArgs[cmd]
		if want == 0 {
			return n == 0
		}
		if n == 0 || n%want != 0 {
			return false
		}
		if cmd != 'M' || n > 2 {
			draws = true
		}
		return true
	}
	for _, l := range locs {
		t := d[l[0]:l[1]]
		c := t[0]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			if !flush() {
				return false
			}
			up := c &^ 0x20
			if _, ok := svgPathArgs[up]; !ok {
				return false
			}
			if cmd == 0 && up != 'M' {
				return false
			}
			cmd, n = up, 0
			continue
		}
		if cmd == 0 {
			return false
		}
		n++
	}
	return flush() && draws
}

// svgPaint is the inherited paint state that decides whether geometry is
// visible: display="none" and opacity 0 hide a whole subtree; visibility,
// fill, stroke, their opacities and stroke-width inherit and a descendant
// may override them. SVG's defaults: fill black, no stroke, width 1.
type svgPaint struct {
	none, opZero, visHidden bool
	fillNone, strokeNone    bool
	fillOp, strokeOp, width float64
}

var svgDefaultPaint = svgPaint{fillOp: 1, strokeOp: 1, strokeNone: true, width: 1}

// svgProps are an element's presentation properties: attributes, then the
// style attribute (which wins).
func svgProps(attr map[string]string) map[string]string {
	p := map[string]string{}
	for _, k := range []string{"display", "visibility", "opacity", "fill", "stroke", "fill-opacity", "stroke-opacity", "stroke-width"} {
		if v, ok := attr[k]; ok {
			p[k] = strings.TrimSpace(v)
		}
	}
	for _, decl := range strings.Split(attr["style"], ";") {
		if k, v, ok := strings.Cut(decl, ":"); ok {
			p[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "!important"))
		}
	}
	return p
}

func svgNoPaint(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "none" || v == "transparent"
}

func (s svgPaint) child(attr map[string]string) svgPaint {
	p := svgProps(attr)
	num := func(v string) (float64, bool) {
		v = strings.TrimSpace(v)
		pct := strings.HasSuffix(v, "%")
		f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(v, "%"), "px"), "mm"), 64)
		if pct {
			f /= 100
		}
		return f, err == nil
	}
	if strings.EqualFold(p["display"], "none") {
		s.none = true
	}
	switch strings.ToLower(p["visibility"]) {
	case "hidden", "collapse":
		s.visHidden = true
	case "visible":
		s.visHidden = false
	}
	if f, ok := num(p["opacity"]); ok && f <= 0 {
		s.opZero = true
	}
	if v, ok := p["fill"]; ok && !strings.EqualFold(v, "inherit") {
		s.fillNone = svgNoPaint(v)
	}
	if v, ok := p["stroke"]; ok && !strings.EqualFold(v, "inherit") {
		s.strokeNone = svgNoPaint(v)
	}
	if f, ok := num(p["fill-opacity"]); ok {
		s.fillOp = f
	}
	if f, ok := num(p["stroke-opacity"]); ok {
		s.strokeOp = f
	}
	if f, ok := num(p["stroke-width"]); ok {
		s.width = f
	}
	return s
}

// hidden: nothing below is drawn whatever its paint.
func (s svgPaint) hidden() bool { return s.none || s.opZero || s.visHidden }

// paints reports whether an element is drawn with visible ink: a fill on
// a shape that can enclose area (svgFillArea), or a stroke of some width.
func (s svgPaint) paints(name string, attr map[string]string) bool {
	if s.hidden() {
		return false
	}
	fill := !s.fillNone && s.fillOp > 0 && svgFillArea(name, attr)
	stroke := !s.strokeNone && s.strokeOp > 0 && s.width > 0
	return fill || stroke || name == "image"
}

// SVGElements is the structural form of an SVG: one canonical string per
// drawn element (tag, sorted attributes with collapsed whitespace, text, and
// the attributes of every enclosing <g>, which carry colour and transform),
// in document order, the <svg> root first. Two SVGs draw the same thing iff
// the multisets match. It fails unless the document is well-formed, has
// exactly one root <svg> and really draws something: an element with
// geometry (svgGeometry) outside defs / symbol / clipPath / mask / marker /
// pattern, painted with a visible fill or stroke (svgPaint: display,
// visibility, opacity, fill, stroke, their opacities and stroke-width from
// attributes or style, inherited), or a <use> there whose target holds such
// an element when painted with the <use>'s inherited state.
func SVGElements(b []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	type frame struct {
		name, ctx, id string
		attr          map[string]string
		text          strings.Builder
		key           int  // index in out, -1 if not a drawn element
		hidden        bool // inside a non-rendering container
		paint         svgPaint
		parent        *frame
	}
	// A geometry element below an id'd element: the attributes from that
	// element down to it, re-applied to a <use>'s paint.
	type geom struct {
		name  string
		attr  map[string]string
		chain []map[string]string // from the id'd element down to it
	}
	type use struct {
		target string
		paint  svgPaint
	}
	var stack []*frame
	var out []string
	idGeoms := map[string][]geom{}
	var uses []use // <use> elements outside hidden containers
	visible := 0
	root := ""
	closed := false // the root element has ended
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 {
				if closed {
					return nil, fmt.Errorf("element <%s> after the root <svg>", t.Name.Local)
				}
				if root = t.Name.Local; root != "svg" {
					return nil, fmt.Errorf("root element is <%s>, not <svg>", root)
				}
			}
			f := &frame{name: t.Name.Local, attr: map[string]string{}, key: -1}
			attrs := make([]string, 0, len(t.Attr))
			for _, a := range t.Attr {
				name := a.Name.Local
				if a.Name.Space != "" {
					name = a.Name.Space + ":" + name
				}
				f.attr[a.Name.Local] = a.Value
				attrs = append(attrs, name+"="+strings.TrimSpace(svgSpaceRE.ReplaceAllString(a.Value, " ")))
			}
			sort.Strings(attrs)
			f.id = f.attr["id"]
			parent, paint := "", svgDefaultPaint
			if n := len(stack); n > 0 {
				f.parent = stack[n-1]
				parent, f.hidden, paint = f.parent.ctx, f.parent.hidden, f.parent.paint
				if svgHidden[f.parent.name] {
					paint = svgDefaultPaint // drawn where it is used, with that paint
				}
			}
			f.hidden = f.hidden || svgHidden[f.name]
			f.paint = paint.child(f.attr)
			f.ctx = parent
			if f.name == "g" {
				f.ctx = parent + "g{" + strings.Join(attrs, ";") + "}"
			}
			if svgDrawn[f.name] {
				out = append(out, parent+"|"+f.name+"{"+strings.Join(attrs, ";")+"}")
				f.key = len(out) - 1
			}
			if f.name == "use" && !f.hidden {
				uses = append(uses, use{strings.TrimPrefix(f.attr["href"], "#"), f.paint})
			}
			stack = append(stack, f)
		case xml.CharData:
			if n := len(stack); n > 0 && stack[n-1].name == "text" {
				stack[n-1].text.Write(t)
			} else if n == 0 && strings.TrimSpace(string(t)) != "" {
				return nil, fmt.Errorf("text outside the root <svg>")
			}
		case xml.EndElement:
			n := len(stack)
			if n == 0 {
				continue
			}
			f := stack[n-1]
			stack = stack[:n-1]
			closed = closed || n == 1
			txt := strings.TrimSpace(f.text.String())
			if f.key >= 0 && f.name == "text" && txt != "" {
				out[f.key] += "=" + txt
			}
			if !svgGeometry(f.name, f.attr, txt) {
				continue
			}
			if !f.hidden && f.paint.paints(f.name, f.attr) {
				visible++
			}
			// Record it for every id'd ancestor (and itself) a <use> may name.
			var chain []map[string]string
			for a := f; a != nil; a = a.parent {
				chain = append([]map[string]string{a.attr}, chain...)
				if a.id != "" {
					idGeoms[a.id] = append(idGeoms[a.id], geom{f.name, f.attr, append([]map[string]string(nil), chain...)})
				}
			}
		}
	}
	if root == "" {
		return nil, fmt.Errorf("no root element")
	}
	for _, u := range uses {
		for _, g := range idGeoms[u.target] {
			p := u.paint
			for _, a := range g.chain {
				p = p.child(a)
			}
			if p.paints(g.name, g.attr) {
				visible++
			}
		}
	}
	if visible == 0 {
		return nil, fmt.Errorf("draws nothing (no visibly painted geometry outside defs)")
	}
	return out, nil
}

// RenderFileDiff is one file's compare verdict.
type RenderFileDiff struct {
	File             string   `json:"file"`
	Status           string   `json:"status"` // identical | changed | added | removed | unreadable
	BaselineSHA256   string   `json:"baselineSha256,omitempty"`
	CurrentSHA256    string   `json:"currentSha256,omitempty"`
	ElementsBaseline int      `json:"elementsBaseline,omitempty"`
	ElementsCurrent  int      `json:"elementsCurrent,omitempty"`
	ElementsAdded    int      `json:"elementsAdded,omitempty"`
	ElementsRemoved  int      `json:"elementsRemoved,omitempty"`
	ChangedElements  int      `json:"changedElements,omitempty"` // added + removed
	Reordered        bool     `json:"reordered,omitempty"`       // same elements, other paint order
	Samples          []string `json:"samples,omitempty"`         // a few differing elements, clipped
	Detail           string   `json:"detail,omitempty"`
}

// RenderCompare is the --compare report.
type RenderCompare struct {
	Verdict  gate.Status      `json:"verdict"`
	Baseline string           `json:"baseline"`
	Current  string           `json:"current"`
	Counts   map[string]int   `json:"counts"`
	Files    []RenderFileDiff `json:"files"`
	Ignored  []string         `json:"ignored,omitempty"` // PNG previews, not compared
	Notes    []string         `json:"notes,omitempty"`
	Fix      string           `json:"fix,omitempty"`
}

// renderSide is one side of a compare: its SVGs (relative slash path →
// file), its PNGs, and the SVGs its render.json lists (nil without one).
type renderSide struct {
	svgs   map[string]string
	pngs   []string
	listed map[string]bool
	bad    map[string]string // non-regular .svg entries and linked dirs → why
}

func readRenderSide(dir string) (*renderSide, error) {
	s := &renderSide{svgs: map[string]string{}, bad: map[string]string{}}
	if _, err := os.Lstat(filepath.Join(dir, RenderBackupName)); err == nil {
		return nil, fmt.Errorf("%s holds %s: a publish was interrupted", dir, RenderBackupName)
	}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			return nil // real directories are walked
		}
		if !d.Type().IsRegular() {
			// Links are never followed: a linked directory or a non-regular
			// .svg is a file this side cannot vouch for.
			isSVG := strings.EqualFold(filepath.Ext(p), ".svg")
			if st, err := os.Stat(p); err == nil && st.IsDir() {
				s.bad[rel+"/"] = "linked directory (not followed)"
			} else if isSVG {
				s.bad[rel] = "not a regular file (link or special file)"
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".svg":
			s.svgs[rel] = p
		case ".png":
			s.pngs = append(s.pngs, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(dir, RenderManifestName)); err == nil {
		m, err := ReadRenderManifest(dir)
		if err != nil {
			return nil, err
		}
		s.listed = map[string]bool{}
		for _, f := range m.Files {
			if strings.EqualFold(filepath.Ext(f.File), ".svg") {
				s.listed[f.File] = true
			}
		}
	}
	return s, nil
}

// CompareRenders diffs the SVGs of current against baseline: identical
// (both parse and have the same normalised bytes), changed (counted from the
// structural diff), added, removed, or unreadable (does not parse, draws
// nothing, or is listed in its side's render.json but missing). Verdict:
// pass when every file is identical, fail when any differs, cannot-check
// when a side has no SVG, a render.json is invalid, or a file is unreadable
// and nothing differs.
func CompareRenders(baseline, current string) *RenderCompare {
	r := &RenderCompare{Baseline: baseline, Current: current, Counts: map[string]int{}}
	base, berr := readRenderSide(baseline)
	cur, cerr := readRenderSide(current)
	switch {
	case berr != nil || len(base.svgs)+len(base.listed)+len(base.bad) == 0:
		r.Verdict, r.Fix = gate.CannotCheck, "point --compare at the --out directory of an earlier kicad render"
		r.Notes = append(r.Notes, fmt.Sprintf("baseline %s has no usable render (%v)", baseline, berr))
		return r
	case cerr != nil || len(cur.svgs)+len(cur.listed)+len(cur.bad) == 0:
		r.Verdict, r.Fix = gate.CannotCheck, "render first (--sch or --pcb) or point --out at a rendered directory"
		r.Notes = append(r.Notes, fmt.Sprintf("current %s has no usable render (%v)", current, cerr))
		return r
	}
	seen := map[string]bool{}
	for _, p := range append(base.pngs, cur.pngs...) {
		if !seen[p] {
			seen[p] = true
			r.Ignored = append(r.Ignored, p)
		}
	}
	sort.Strings(r.Ignored)
	names := map[string]bool{}
	for _, m := range []map[string]string{base.svgs, cur.svgs} {
		for n := range m {
			names[n] = true
		}
	}
	for _, m := range []map[string]bool{base.listed, cur.listed} {
		for n := range m {
			names[n] = true
		}
	}
	for _, m := range []map[string]string{base.bad, cur.bad} {
		for n := range m {
			names[n] = true
		}
	}
	order := make([]string, 0, len(names))
	for n := range names {
		order = append(order, n)
	}
	sort.Strings(order)
	var results []gate.Result
	for _, n := range order {
		var d RenderFileDiff
		bp, cp := base.svgs[n], cur.svgs[n]
		switch {
		case base.bad[n] != "":
			d = RenderFileDiff{File: n, Status: "unreadable", Detail: "baseline: " + base.bad[n]}
		case cur.bad[n] != "":
			d = RenderFileDiff{File: n, Status: "unreadable", Detail: "current: " + cur.bad[n]}
		case bp == "" && base.listed[n], cp == "" && cur.listed[n]:
			d = RenderFileDiff{File: n, Status: "unreadable", Detail: "listed in render.json but missing"}
		default:
			d = diffSVGFile(n, bp, cp)
		}
		r.Files = append(r.Files, d)
		r.Counts[d.Status]++
		st := gate.Fail
		switch d.Status {
		case "identical":
			st = gate.Pass
		case "unreadable":
			st = gate.CannotCheck
		}
		results = append(results, gate.Result{Name: n, Status: st})
	}
	r.Verdict = gate.Aggregate(results...)
	if r.Verdict == gate.CannotCheck {
		r.Fix = "re-render both sides with kicad render; an unreadable file cannot be compared"
	}
	bm, _ := ReadRenderManifest(baseline)
	cm, _ := ReadRenderManifest(current)
	if bm != nil && cm != nil && (bm.Kind != cm.Kind || bm.Side != cm.Side || strings.Join(bm.Layers, ",") != strings.Join(cm.Layers, ",")) {
		r.Notes = append(r.Notes, fmt.Sprintf("render options differ: baseline %s %s %v, current %s %s %v", bm.Kind, bm.Side, bm.Layers, cm.Kind, cm.Side, cm.Layers))
	}
	return r
}

func fileSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func diffSVGFile(name, bp, cp string) RenderFileDiff {
	d := RenderFileDiff{File: name}
	if bp == "" {
		d.Status = "added"
		return d
	}
	if cp == "" {
		d.Status = "removed"
		return d
	}
	bb, err1 := os.ReadFile(bp)
	cb, err2 := os.ReadFile(cp)
	if err1 != nil || err2 != nil {
		d.Status, d.Detail = "unreadable", fmt.Sprintf("read: baseline %v, current %v", err1, err2)
		return d
	}
	// Normalise both sides again: a baseline from an older render (or
	// sch-build before this command) may still carry its plot date.
	bb, cb = NormalizeSVG(bb, nil, ""), NormalizeSVG(cb, nil, "")
	d.BaselineSHA256, d.CurrentSHA256 = fileSHA(bb), fileSHA(cb)
	// Both must be real drawings before equal bytes can mean "identical":
	// two empty or broken files are not a passing compare.
	be, err1 := SVGElements(bb)
	ce, err2 := SVGElements(cb)
	if err1 != nil || err2 != nil {
		d.Status, d.Detail = "unreadable", fmt.Sprintf("parse: baseline %v, current %v", err1, err2)
		return d
	}
	d.ElementsBaseline, d.ElementsCurrent = len(be), len(ce)
	if d.BaselineSHA256 == d.CurrentSHA256 {
		d.Status = "identical"
		return d
	}
	count := map[string]int{}
	for _, e := range be {
		count[e]++
	}
	for _, e := range ce {
		count[e]--
	}
	var keys []string
	for k, v := range count {
		switch {
		case v > 0:
			d.ElementsRemoved += v
		case v < 0:
			d.ElementsAdded -= v
		default:
			continue
		}
		keys = append(keys, k)
	}
	d.ChangedElements = d.ElementsAdded + d.ElementsRemoved
	d.Status = "changed"
	if d.ChangedElements == 0 {
		d.Reordered = true
		d.Detail = "same elements in another paint order (or a non-drawing byte differs)"
		return d
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(d.Samples) == 4 {
			break
		}
		sign := "+"
		if count[k] > 0 {
			sign = "-"
		}
		if i := strings.LastIndex(k, "|"); i >= 0 {
			k = k[i+1:] // drop the group context from the sample
		}
		if len(k) > 160 {
			k = k[:160] + "…"
		}
		d.Samples = append(d.Samples, sign+" "+k)
	}
	return d
}
