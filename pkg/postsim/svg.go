package postsim

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Heat maps: one SVG per copper layer for temperature (°C) and, where the
// layer carries simulated current, current density (A/mm²). The raster is an
// embedded PNG (nearest-neighbour, one block per cell); board outline, part
// outlines + designators, the hottest point and a legend are vector.

var infernoStops = []color.NRGBA{{0, 0, 4, 255}, {87, 16, 110, 255}, {188, 55, 84, 255}, {249, 142, 9, 255}, {252, 255, 164, 255}}
var viridisStops = []color.NRGBA{{68, 1, 84, 255}, {59, 82, 139, 255}, {33, 145, 140, 255}, {94, 201, 98, 255}, {253, 231, 37, 255}}

func ramp(stops []color.NRGBA, t float64) color.NRGBA {
	t = math.Max(0, math.Min(1, t))
	f := t * float64(len(stops)-1)
	i := int(f)
	if i >= len(stops)-1 {
		return stops[len(stops)-1]
	}
	u := f - float64(i)
	a, b := stops[i], stops[i+1]
	mix := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*u)) }
	return color.NRGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 255}
}

func hex(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// WriteMaps writes the heat maps of res into dir and records them in
// res.Maps (paths relative to dir).
func (res *Result) WriteMaps(dir string) error {
	if res.grid == nil || res.TempMap == nil {
		return fmt.Errorf("no rasters (run the simulation first)")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	res.Maps = nil
	res.MapsDir = filepath.ToSlash(dir)
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, m := range res.TempMap {
		for _, t := range m {
			if !math.IsNaN(t) {
				lo, hi = math.Min(lo, t), math.Max(hi, t)
			}
		}
	}
	if hi-lo < 0.1 {
		hi = lo + 0.1
	}
	jmax := 0.0
	for _, m := range res.JMap {
		for _, j := range m {
			jmax = math.Max(jmax, j)
		}
	}
	for k, l := range res.Stackup.Layers {
		title := fmt.Sprintf("%s — temperature, scenario %s (max %.1f °C)", l.Name, res.Thermal.Scenario, res.Thermal.Layers[k].MaxC)
		svg := res.mapSVG(k, res.TempMap[k], lo, hi, infernoStops, "°C", title, false)
		name := fmt.Sprintf("temp-%s.svg", l.Name)
		if err := os.WriteFile(filepath.Join(dir, name), svg, 0o644); err != nil {
			return err
		}
		res.Maps = append(res.Maps, MapFile{Kind: "temperature", Layer: l.Name, File: name, Min: round(lo, 2), Max: round(hi, 2), Unit: "°C"})
	}
	if jmax > 0 {
		for k, l := range res.Stackup.Layers {
			lmax := 0.0
			for _, j := range res.JMap[k] {
				lmax = math.Max(lmax, j)
			}
			if lmax == 0 {
				continue
			}
			title := fmt.Sprintf("%s — current density, worst case per net (max %.1f A/mm², log scale)", l.Name, lmax)
			svg := res.mapSVG(k, res.JMap[k], jmax/1000, jmax, viridisStops, "A/mm²", title, true)
			name := fmt.Sprintf("current-%s.svg", l.Name)
			if err := os.WriteFile(filepath.Join(dir, name), svg, 0o644); err != nil {
				return err
			}
			res.Maps = append(res.Maps, MapFile{Kind: "current-density", Layer: l.Name, File: name, Min: round(jmax/1000, 4), Max: round(jmax, 2), Unit: "A/mm²", Log: true})
		}
	}
	return nil
}

// mapSVG renders one layer raster. logScale: log colour scale over [lo, hi],
// cells below lo are left uncoloured (no significant current).
func (res *Result) mapSVG(k int, vals []float64, lo, hi float64, stops []color.NRGBA, unit, title string, logScale bool) []byte {
	frac := func(v float64) float64 { return (v - lo) / (hi - lo) }
	if logScale {
		frac = func(v float64) float64 { return math.Log(v/lo) / math.Log(hi/lo) }
	}
	g, b := res.grid, res.board
	px := int(math.Max(2, math.Round(640/float64(max(g.NX, g.NY)))))
	img := image.NewNRGBA(image.Rect(0, 0, g.NX*px, g.NY*px))
	for c, v := range vals {
		if math.IsNaN(v) || !g.Inside[c] || logScale && v < lo {
			continue
		}
		col := ramp(stops, frac(v))
		ix, iy := c%g.NX, g.NY-1-c/g.NX
		for y := iy * px; y < (iy+1)*px; y++ {
			for x := ix * px; x < (ix+1)*px; x++ {
				img.SetNRGBA(x, y, col)
			}
		}
	}
	var pb bytes.Buffer
	_ = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&pb, img)

	W, H := float64(g.NX*px), float64(g.NY*px)
	s := W / (float64(g.NX) * g.Cell) // svg px per mil
	X := func(x float64) float64 { return (x - g.X0) * s }
	Y := func(y float64) float64 { return H - (y-g.Y0)*s }
	const legendW, top = 110.0, 34.0
	var w strings.Builder
	p := func(f string, a ...any) { fmt.Fprintf(&w, f, a...) }
	p(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" font-family="Helvetica,Arial,sans-serif">`+"\n",
		W+legendW+20, H+top+14, W+legendW+20, H+top+14)
	p(`<rect width="100%%" height="100%%" fill="#ffffff"/>` + "\n")
	p(`<text x="10" y="22" font-size="15" fill="#111">%s</text>`+"\n", esc(title))
	p(`<g transform="translate(10,%.0f)">`+"\n", top)
	p(`<rect width="%.0f" height="%.0f" fill="#e8e8e8"/>`+"\n", W, H)
	p(`<image width="%.0f" height="%.0f" style="image-rendering:pixelated" preserveAspectRatio="none" xlink:href="data:image/png;base64,%s" href="data:image/png;base64,%s"/>`+"\n",
		W, H, base64.StdEncoding.EncodeToString(pb.Bytes()), base64.StdEncoding.EncodeToString(pb.Bytes()))
	var pts []string
	for _, q := range b.Outline {
		pts = append(pts, fmt.Sprintf("%.1f,%.1f", X(q.X), Y(q.Y)))
	}
	p(`<polygon points="%s" fill="none" stroke="#111" stroke-width="1.5"/>`+"\n", strings.Join(pts, " "))
	for _, h := range b.Holes {
		for _, c := range h.Contours {
			var hp []string
			for _, q := range c {
				hp = append(hp, fmt.Sprintf("%.1f,%.1f", X(q.X), Y(q.Y)))
			}
			p(`<polygon points="%s" fill="#fff" stroke="#111" stroke-width="0.8"/>`+"\n", strings.Join(hp, " "))
		}
	}
	lid := res.Stackup.Layers[k].ID
	fs := math.Max(8, math.Min(13, W/60))
	for _, part := range b.Parts {
		if !part.BBox.valid() {
			continue
		}
		own := part.Side == lid
		stroke, dash, op := "#ffffff", "", "0.9"
		if !own {
			stroke, dash, op = "#ffffff", ` stroke-dasharray="3 3"`, "0.45"
		}
		x0, y0 := X(part.BBox.MinX), Y(part.BBox.MaxY)
		p(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="none" stroke="%s" stroke-opacity="%s" stroke-width="1"%s/>`+"\n",
			x0, y0, (part.BBox.MaxX-part.BBox.MinX)*s, (part.BBox.MaxY-part.BBox.MinY)*s, stroke, op, dash)
		if own || k == 0 {
			cx, cy := X((part.BBox.MinX+part.BBox.MaxX)/2), Y((part.BBox.MinY+part.BBox.MaxY)/2)
			p(`<text x="%.1f" y="%.1f" font-size="%.1f" text-anchor="middle" dominant-baseline="middle" fill="#fff" stroke="#000" stroke-width="2.2" paint-order="stroke" fill-opacity="%s">%s</text>`+"\n",
				cx, cy, fs, op, esc(part.Ref))
		}
	}
	// Hottest / densest cell of this layer.
	best, bc := math.Inf(-1), -1
	for c, v := range vals {
		if !math.IsNaN(v) && g.Inside[c] && v > best {
			best, bc = v, c
		}
	}
	if bc >= 0 && best > 0 {
		q := g.cellCentre(bc)
		p(`<circle cx="%.1f" cy="%.1f" r="7" fill="none" stroke="#00e5ff" stroke-width="2.5"/>`+"\n", X(q.X), Y(q.Y))
		p(`<text x="%.1f" y="%.1f" font-size="%.1f" fill="#00e5ff" stroke="#000" stroke-width="2.2" paint-order="stroke">max %.1f %s</text>`+"\n",
			X(q.X)+10, Y(q.Y)-8, fs+1, best, unit)
	}
	p("</g>\n")
	// Legend.
	lx, ly, lh := W+30, top+10, math.Min(H-20, 320)
	p(`<defs><linearGradient id="lg%d%s" x1="0" y1="1" x2="0" y2="0">`, k, strings.ReplaceAll(unit, "/", ""))
	for i, c := range stops {
		p(`<stop offset="%.2f" stop-color="%s"/>`, float64(i)/float64(len(stops)-1), hex(c))
	}
	p("</linearGradient></defs>\n")
	p(`<rect x="%.0f" y="%.0f" width="22" height="%.0f" fill="url(#lg%d%s)" stroke="#333"/>`+"\n", lx, ly, lh, k, strings.ReplaceAll(unit, "/", ""))
	for i := 0; i <= 5; i++ {
		v := lo + (hi-lo)*float64(i)/5
		if logScale {
			v = lo * math.Pow(hi/lo, float64(i)/5)
		}
		yy := ly + lh - lh*float64(i)/5
		p(`<line x1="%.0f" y1="%.1f" x2="%.0f" y2="%.1f" stroke="#333"/>`, lx+22, yy, lx+27, yy)
		p(`<text x="%.0f" y="%.1f" font-size="12" dominant-baseline="middle" fill="#111">%s</text>`+"\n", lx+30, yy, legendNum(v))
	}
	p(`<text x="%.0f" y="%.0f" font-size="12" fill="#111">%s</text>`+"\n", lx, ly+lh+18, esc(unit))
	p("</svg>\n")
	return []byte(w.String())
}

func legendNum(v float64) string {
	switch a := math.Abs(v); {
	case a >= 100:
		return fmt.Sprintf("%.0f", v)
	case a >= 10:
		return fmt.Sprintf("%.1f", v)
	default:
		return fmt.Sprintf("%.2f", v)
	}
}

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
