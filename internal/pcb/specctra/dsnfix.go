package specctra

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// FixOptions selects the EasyEDA DSN export repairs. The zero value applies
// the always-safe fixes (class-name quoting, missing inner layers, padstack
// inner shapes) and nothing else.
type FixOptions struct {
	// CopperLayers is the board's copper layer count. 0 = infer from the
	// highest InnerN the DSN mentions.
	CopperLayers int
	// EdgeOuterMil / EdgeInnerMil add board-edge copper keep-out bands of this
	// width on the outer (Top/Bottom) and inner layers. 0 = none.
	EdgeOuterMil float64
	EdgeInnerMil float64
	// EdgeExempt are boxes (DSN units: minX, minY, maxX, maxY) the edge bands
	// leave open: the pads of edge-mounted connectors grown by their escape
	// margin (EdgeBandsExcept). Everything else keeps the band.
	EdgeExempt [][4]float64
	// PlaneNet, when set, declares the inner layers EasyEDA left out of the
	// export as power layers carrying a plane of this net (the router then
	// connects that net with vias to the plane). Empty = add them as signal
	// layers and route the net as traces.
	PlaneNet string
	// Escapes are pre-routed (type fix) stubs, each optionally ending in a via,
	// for pins the router cannot escape on its own.
	Escapes []Escape
}

// Escape is one fixed escape: a polyline on Layer, optionally ending in a via.
type Escape struct {
	Net      string       `json:"net"`
	Layer    string       `json:"layer"`
	WidthMil float64      `json:"widthMil"`
	Path     [][2]float64 `json:"path"`
	Via      bool         `json:"via"`
}

// FixReport says what FixDSN changed.
type FixReport struct {
	QuotedClasses    int      `json:"quotedClasses"`
	AddedLayers      []string `json:"addedLayers,omitempty"`
	LayerOrder       []string `json:"layerOrder"`
	PatchedPadstacks int      `json:"patchedPadstacks"`
	EdgeKeepouts     int      `json:"edgeKeepouts"`
	// EdgeWindows: band windows left open for edge-mounted connector pads.
	EdgeWindows int    `json:"edgeWindows,omitempty"`
	PlaneNet    string `json:"planeNet,omitempty"`
	Escapes     int    `json:"escapes"`
}

var (
	reClassQuoted = regexp.MustCompile(`'([^'\s()]+)'`)
	reLayerBlock  = regexp.MustCompile(`(?m)^[ \t]*\(layer[ \t]+(\S+)[ \t]*\r?\n[ \t]*\(type[ \t]+(\w+)\)[ \t]*\r?\n[ \t]*\)[ \t]*\r?\n`)
	reInnerName   = regexp.MustCompile(`\bInner(\d+)\b`)
	reShapeLine   = regexp.MustCompile(`^[ \t]*\(shape[ \t]*\([ \t]*\w+[ \t]+(\S+)`)
	rePadstack    = regexp.MustCompile(`^[ \t]*\(padstack[ \t]+\S+`)
	reBoundary    = regexp.MustCompile(`\(boundary[ \t]*\([ \t]*path[ \t]+\S+[ \t]+\S+((?:[ \t]+-?[0-9.]+)+)`)
	reViaPadstack = regexp.MustCompile(`\(via[ \t]+(\S+)`)
)

// FixDSN applies the EasyEDA export repairs to src.
func FixDSN(src string, opt FixOptions) (string, FixReport, error) {
	var rep FixReport
	if !strings.Contains(src, "(structure") {
		return "", rep, fmt.Errorf("not a Specctra DSN: no (structure) section")
	}

	// 1a. EasyEDA writes class net names as 'NET'. Specctra strings use '"',
	// so routers read the quotes as part of the name, the class matches no net
	// and every net-class width/clearance is silently ignored. The empty
	// default class ('') is left as exported.
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), "(class ") && strings.Contains(ln, "'") {
			fixed := reClassQuoted.ReplaceAllString(ln, `"$1"`)
			if fixed != ln {
				lines[i] = fixed
				rep.QuotedClasses++
			}
		}
	}
	src = strings.Join(lines, "\n")

	// 1b. Layers: EasyEDA omits some inner layers (seen: Inner1 of a 4-layer
	// board whose IN1 carries a GND pour) and lists the rest out of stack
	// order. Rebuild the declarations Top, Inner1..N, Bottom.
	inner := opt.CopperLayers - 2
	for _, m := range reInnerName.FindAllStringSubmatch(src, -1) {
		if k, _ := strconv.Atoi(m[1]); k > inner {
			inner = k
		}
	}
	if inner < 0 {
		inner = 0
	}
	blocks := reLayerBlock.FindAllStringSubmatchIndex(src, -1)
	if len(blocks) == 0 {
		return "", rep, fmt.Errorf("DSN has no (layer ...) declarations")
	}
	types := map[string]string{}
	for _, b := range blocks {
		types[src[b[2]:b[3]]] = src[b[4]:b[5]]
	}
	order := []string{"TopLayer"}
	for k := 1; k <= inner; k++ {
		order = append(order, fmt.Sprintf("Inner%d", k))
	}
	order = append(order, "BottomLayer")
	var missing []string
	for _, l := range order {
		if _, ok := types[l]; !ok {
			missing = append(missing, l)
			types[l] = "signal"
			if opt.PlaneNet != "" && strings.HasPrefix(l, "Inner") {
				types[l] = "power"
			}
		}
	}
	for l := range types {
		if !slices.Contains(order, l) {
			return "", rep, fmt.Errorf("unexpected layer %q in DSN (expected TopLayer/InnerN/BottomLayer)", l)
		}
	}
	rep.AddedLayers = missing
	rep.LayerOrder = order

	boundary, err := parseBoundary(src)
	if err != nil {
		return "", rep, err
	}

	var decl strings.Builder
	for _, l := range order {
		fmt.Fprintf(&decl, "    (layer %s\n      (type %s)\n    )\n", l, types[l])
	}
	if opt.PlaneNet != "" {
		for _, l := range missing {
			if strings.HasPrefix(l, "Inner") {
				fmt.Fprintf(&decl, "    (plane %s (polygon %s 0 %s))\n", quoteIfNeeded(opt.PlaneNet), l, formatCoords(boundary))
			}
		}
		if len(missing) > 0 {
			rep.PlaneNet = opt.PlaneNet
		}
	}
	// 1c. Board-edge copper clearance as keep-out bands, one per outline
	// edge and layer. getDsnFile does not carry EasyEDA's board-outline
	// clearance rule, so without these the router places copper the native
	// DRC then flags.
	for _, l := range order {
		w := opt.EdgeInnerMil
		if l == "TopLayer" || l == "BottomLayer" {
			w = opt.EdgeOuterMil
		}
		if w <= 0 {
			continue
		}
		qs, cut := EdgeBandsExcept(boundary, w, opt.EdgeExempt)
		rep.EdgeWindows += cut
		for i, q := range qs {
			fmt.Fprintf(&decl, "    (keepout \"pcbpilot_edge_%s_%d\" (polygon %s 0 %s))\n", l, i, l, formatCoords(q))
			rep.EdgeKeepouts++
		}
	}
	first, last := blocks[0][0], blocks[len(blocks)-1][1]
	src = src[:first] + decl.String() + src[last:]

	// Padstacks: through-hole stacks (Top and Bottom shapes) get a shape on
	// every inner layer they lack, copied from an existing inner shape (or
	// the Top shape). Without it the router treats the pin as absent on that
	// layer and routes other nets straight through the barrel.
	src, rep.PatchedPadstacks = patchPadstacks(src, order)

	// 1e. Fixed escapes.
	if len(opt.Escapes) > 0 {
		via := ""
		if m := reViaPadstack.FindStringSubmatch(src[strings.Index(src, "(structure"):]); m != nil {
			via = m[1]
		}
		var w strings.Builder
		for i, e := range opt.Escapes {
			if len(e.Path) < 2 || e.Net == "" || e.WidthMil <= 0 {
				return "", rep, fmt.Errorf("escape %d: need net, widthMil > 0 and at least two path points", i)
			}
			if !slices.Contains(order, e.Layer) {
				return "", rep, fmt.Errorf("escape %d: layer %q is not one of %v", i, e.Layer, order)
			}
			fmt.Fprintf(&w, "    (wire (path %s %s %s) (net %s) (type fix))\n", e.Layer, fnum(e.WidthMil), formatCoords(e.Path), quoteIfNeeded(e.Net))
			if e.Via {
				if via == "" {
					return "", rep, fmt.Errorf("escape %d asks for a via but the DSN declares no via padstack", i)
				}
				end := e.Path[len(e.Path)-1]
				fmt.Fprintf(&w, "    (via %s %s %s (net %s) (type fix))\n", via, fnum(end[0]), fnum(end[1]), quoteIfNeeded(e.Net))
			}
			rep.Escapes++
		}
		src, err = appendWiring(src, w.String())
		if err != nil {
			return "", rep, err
		}
	}
	return src, rep, nil
}

func patchPadstacks(src string, order []string) (string, int) {
	lines := strings.Split(src, "\n")
	var out []string
	patched := 0
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		if !rePadstack.MatchString(lines[i]) {
			continue
		}
		// Collect the shape lines that follow the padstack header.
		j := i + 1
		var shapes []string
		have := map[string]bool{}
		for ; j < len(lines); j++ {
			m := reShapeLine.FindStringSubmatch(lines[j])
			if m == nil {
				break
			}
			shapes = append(shapes, lines[j])
			have[m[1]] = true
		}
		out = append(out, shapes...)
		i = j - 1
		if !have["TopLayer"] || !have["BottomLayer"] {
			continue
		}
		tmpl, tmplLayer := "", ""
		for _, s := range shapes {
			l := reShapeLine.FindStringSubmatch(s)[1]
			if strings.HasPrefix(l, "Inner") {
				tmpl, tmplLayer = s, l
				break
			}
		}
		if tmpl == "" {
			tmpl, tmplLayer = shapes[0], reShapeLine.FindStringSubmatch(shapes[0])[1]
		}
		added := false
		for _, l := range order {
			if strings.HasPrefix(l, "Inner") && !have[l] {
				out = append(out, strings.Replace(tmpl, " "+tmplLayer+" ", " "+l+" ", 1))
				added = true
			}
		}
		if added {
			patched++
		}
	}
	return strings.Join(out, "\n"), patched
}

// appendWiring adds wire/via lines to the (wiring ...) section, creating it
// before the final ')' when the export has none.
func appendWiring(src, body string) (string, error) {
	if i := strings.Index(src, "(wiring"); i >= 0 {
		nl := strings.IndexByte(src[i:], '\n')
		if nl < 0 {
			return "", fmt.Errorf("malformed (wiring) section")
		}
		at := i + nl + 1
		return src[:at] + body + src[at:], nil
	}
	end := strings.LastIndexByte(src, ')')
	if end < 0 {
		return "", fmt.Errorf("malformed DSN: no closing ')'")
	}
	return src[:end] + "  (wiring\n" + body + "  )\n" + src[end:], nil
}

func parseBoundary(src string) ([][2]float64, error) {
	m := reBoundary.FindStringSubmatch(src)
	if m == nil {
		return nil, fmt.Errorf("DSN has no (boundary (path ...)) outline")
	}
	f := strings.Fields(m[1])
	if len(f)%2 != 0 || len(f) < 6 {
		return nil, fmt.Errorf("board boundary has %d coordinates, want an even number >= 6", len(f))
	}
	var pts [][2]float64
	for i := 0; i < len(f); i += 2 {
		x, err1 := strconv.ParseFloat(f[i], 64)
		y, err2 := strconv.ParseFloat(f[i+1], 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("bad boundary coordinate %q %q", f[i], f[i+1])
		}
		if len(pts) > 0 && math.Hypot(x-pts[len(pts)-1][0], y-pts[len(pts)-1][1]) < 1e-6 {
			continue
		}
		pts = append(pts, [2]float64{x, y})
	}
	return pts, nil
}

// edgeBands returns, for every outline edge, the quad of width w lying on the
// board side of that edge. For a convex outline (incl. rounded rectangles)
// the bands cover every point within w of the edge.
func edgeBands(poly [][2]float64, w float64) [][][2]float64 {
	n := len(poly)
	closed := n > 1 && poly[0] == poly[n-1]
	if closed {
		n--
	}
	area := 0.0
	for i := 0; i < n; i++ {
		a, b := poly[i], poly[(i+1)%n]
		area += a[0]*b[1] - b[0]*a[1]
	}
	sign := 1.0 // CCW: inward normal is the left normal
	if area < 0 {
		sign = -1
	}
	var out [][][2]float64
	for i := 0; i < n; i++ {
		a, b := poly[i], poly[(i+1)%n]
		dx, dy := b[0]-a[0], b[1]-a[1]
		l := math.Hypot(dx, dy)
		if l < 1e-6 {
			continue
		}
		nx, ny := -dy/l*sign*w, dx/l*sign*w
		out = append(out, [][2]float64{a, b, {b[0] + nx, b[1] + ny}, {a[0] + nx, a[1] + ny}, a})
	}
	return out
}

// EdgeBandsExcept is edgeBands (w < 0: the outer side, for a cut-out) with
// a window cut wherever an exempt box reaches into a band: the band is split
// along its edge around the box's extent, over the full band depth, so an
// edge-mounted connector pad and its straight escape inward stay routable.
// Returns the quads and the number of windows cut.
func EdgeBandsExcept(poly [][2]float64, w float64, exempt [][4]float64) ([][][2]float64, int) {
	full := edgeBands(poly, w)
	if len(exempt) == 0 {
		return full, 0
	}
	var out [][][2]float64
	cuts := 0
	for _, q := range full {
		a, b, c := q[0], q[1], q[3] // c = a + normal·|w|
		L := math.Hypot(b[0]-a[0], b[1]-a[1])
		ux, uy := (b[0]-a[0])/L, (b[1]-a[1])/L
		depth := math.Hypot(c[0]-a[0], c[1]-a[1])
		nx, ny := (c[0]-a[0])/depth, (c[1]-a[1])/depth
		type iv struct{ lo, hi float64 }
		var ivs []iv
		for _, e := range exempt {
			tlo, thi, slo, shi := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
			for _, p := range [][2]float64{{e[0], e[1]}, {e[2], e[1]}, {e[2], e[3]}, {e[0], e[3]}} {
				t := (p[0]-a[0])*ux + (p[1]-a[1])*uy
				sv := (p[0]-a[0])*nx + (p[1]-a[1])*ny
				tlo, thi, slo, shi = math.Min(tlo, t), math.Max(thi, t), math.Min(slo, sv), math.Max(shi, sv)
			}
			if shi <= 0 || slo >= depth || thi <= 0 || tlo >= L {
				continue
			}
			ivs = append(ivs, iv{math.Max(0, tlo), math.Min(L, thi)})
		}
		if len(ivs) == 0 {
			out = append(out, q)
			continue
		}
		sort.Slice(ivs, func(i, j int) bool { return ivs[i].lo < ivs[j].lo })
		merged := ivs[:1]
		for _, v := range ivs[1:] {
			if last := &merged[len(merged)-1]; v.lo <= last.hi {
				last.hi = math.Max(last.hi, v.hi)
			} else {
				merged = append(merged, v)
			}
		}
		cuts += len(merged)
		at := func(t, d float64) [2]float64 { return [2]float64{a[0] + ux*t + nx*d, a[1] + uy*t + ny*d} }
		pos := 0.0
		for _, v := range append(merged, iv{L, L}) {
			if v.lo > pos+1e-6 {
				out = append(out, [][2]float64{at(pos, 0), at(v.lo, 0), at(v.lo, depth), at(pos, depth), at(pos, 0)})
			}
			pos = math.Max(pos, v.hi)
		}
	}
	return out, cuts
}

func formatCoords(pts [][2]float64) string {
	parts := make([]string, 0, 2*len(pts))
	for _, p := range pts {
		parts = append(parts, fnum(p[0]), fnum(p[1]))
	}
	return strings.Join(parts, " ")
}

func fnum(v float64) string {
	return strconv.FormatFloat(math.Round(v*1000)/1000, 'f', -1, 64)
}

var reBareToken = regexp.MustCompile(`^[A-Za-z0-9_+\-.]+$`)

func quoteIfNeeded(s string) string {
	if reBareToken.MatchString(s) {
		return s
	}
	return strconv.Quote(s)
}
