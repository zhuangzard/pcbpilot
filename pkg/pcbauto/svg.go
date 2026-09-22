package pcbauto

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// RenderSVG draws the board for human review: outline, holes, keepouts and
// isolation strips, plane regions, part bodies coloured by voltage domain,
// pads, tracks per layer, vias and unrouted connections as dashed lines.
// Any of c, st, rr may be nil.
func RenderSVG(w io.Writer, b *Board, c *Circuit, st *Stackup, rr *RouteResult) error {
	bb := b.Bounds().Expand(60)
	scale := 900 / math.Max(bb.W(), bb.H())
	W, H := bb.W()*scale, bb.H()*scale
	x := func(v float64) float64 { return (v - bb.MinX) * scale }
	y := func(v float64) float64 { return (bb.MaxY - v) * scale } // y-up → SVG y-down
	pts := func(ps []Point) string {
		var sb strings.Builder
		for i, p := range ps {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(&sb, "%.1f,%.1f", x(p.X), y(p.Y))
		}
		return sb.String()
	}
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	p(`<svg xmlns="http://www.w3.org/2000/svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" font-family="sans-serif">`+"\n", W, H+40, W, H+40)
	p(`<rect width="100%%" height="100%%" fill="#10141a"/>` + "\n")
	if len(b.Outline) >= 3 {
		p(`<polygon points="%s" fill="#1d3b2a" stroke="#e5d27a" stroke-width="2"/>`+"\n", pts(b.Outline))
	}
	layerColour := map[int]string{LayerTop: "#e05a47", LayerBottom: "#4a8fe0", 15: "#c9a227", 16: "#9b59b6", 17: "#2fb39a", 18: "#e08a2f"}
	colour := func(l int) string {
		if c, ok := layerColour[l]; ok {
			return c
		}
		return "#bbbbbb"
	}
	// Plane regions (faint).
	if rr != nil {
		for _, pr := range rr.Planes {
			for _, poly := range pr.Polys {
				p(`<polygon points="%s" fill="%s" fill-opacity="0.10" stroke="%s" stroke-opacity="0.5" stroke-dasharray="4 3"><title>%s L%d</title></polygon>`+"\n",
					pts(poly), colour(pr.Layer), colour(pr.Layer), esc(pr.Net), pr.Layer)
			}
		}
	}
	for _, k := range b.Keepouts {
		fill := "#ff3b3b"
		if strings.HasPrefix(k.Name, "isolation") {
			fill = "#ffae00"
		}
		p(`<polygon points="%s" fill="%s" fill-opacity="0.18" stroke="%s" stroke-dasharray="6 3"><title>%s</title></polygon>`+"\n", pts(k.Poly), fill, fill, esc(k.Name))
	}
	for _, h := range b.Holes {
		p(`<circle cx="%.1f" cy="%.1f" r="%.1f" fill="#000" stroke="#aaa"/>`+"\n", x(h.C.X), y(h.C.Y), h.Dia/2*scale)
		if h.Keep > 0 {
			p(`<circle cx="%.1f" cy="%.1f" r="%.1f" fill="none" stroke="#aaa" stroke-dasharray="2 2"/>`+"\n", x(h.C.X), y(h.C.Y), (h.Dia/2+h.Keep)*scale)
		}
	}
	// Part bodies by domain.
	domColour := map[string]string{}
	palette := []string{"#3a7bd5", "#d53a3a", "#3ad58a", "#d5a53a", "#a53ad5", "#3ad5d5"}
	if c != nil {
		ids := make([]string, 0, len(c.Domains))
		for _, d := range c.Domains {
			ids = append(ids, d.ID)
		}
		sort.Strings(ids)
		for i, id := range ids {
			domColour[id] = palette[i%len(palette)]
		}
		for _, d := range c.Domains {
			if d.Hazardous {
				domColour[d.ID] = "#ff4040"
			}
		}
	}
	for _, part := range b.Parts {
		r := part.Body()
		col := "#8899aa"
		if c != nil {
			if dc, ok := domColour[c.DomainOf[part.Ref]]; ok {
				col = dc
			}
		}
		p(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s" fill-opacity="0.15" stroke="%s" stroke-width="1"><title>%s %s</title></rect>`+"\n",
			x(r.MinX), y(r.MaxY), r.W()*scale, r.H()*scale, col, col, esc(part.Ref), esc(part.Device))
		for _, pd := range part.Pads {
			pb := pd.Box.Bounds()
			p(`<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s" fill-opacity="0.8"><title>%s %s</title></rect>`+"\n",
				x(pb.MinX), y(pb.MaxY), pb.W()*scale, pb.H()*scale, "#d8b36a", esc(pd.Key()), esc(pd.Net))
		}
		if fs := math.Min(r.W(), r.H()) * scale * 0.5; fs > 5 {
			c := r.Center()
			p(`<text x="%.1f" y="%.1f" font-size="%.1f" fill="#fff" text-anchor="middle" dominant-baseline="middle">%s</text>`+"\n", x(c.X), y(c.Y), math.Min(fs, 14), esc(part.Ref))
		}
	}
	if rr != nil {
		// Bottom first so top copper draws over it.
		order := append([]Track(nil), rr.Tracks...)
		sort.SliceStable(order, func(i, j int) bool { return order[i].Layer == LayerTop && order[j].Layer != LayerTop })
		for i := len(order) - 1; i >= 0; i-- {
			t := order[i]
			p(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="%.2f" stroke-linecap="round" stroke-opacity="0.85"><title>%s L%d w=%.1f</title></line>`+"\n",
				x(t.A.X), y(t.A.Y), x(t.B.X), y(t.B.Y), colour(t.Layer), math.Max(t.Width*scale, 0.6), esc(t.Net), t.Layer, t.Width)
		}
		for _, v := range rr.Vias {
			p(`<circle cx="%.1f" cy="%.1f" r="%.2f" fill="#ddd" stroke="#555" stroke-width="0.5"><title>via %s</title></circle>`+"\n", x(v.C.X), y(v.C.Y), math.Max(v.Dia/2*scale, 1), esc(v.Net))
		}
		pads := map[string]Point{}
		for _, part := range b.Parts {
			for _, pd := range part.Pads {
				pads[pd.Key()] = pd.Box.C
			}
		}
		for _, u := range rr.Unrouted {
			var prev *Point
			for _, k := range u.Pads {
				if q, ok := pads[k]; ok {
					if prev != nil {
						p(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#ff0" stroke-width="1" stroke-dasharray="3 3"><title>unrouted %s (%s)</title></line>`+"\n",
							x(prev.X), y(prev.Y), x(q.X), y(q.Y), esc(u.Net), esc(u.Reason))
					}
					qq := q
					prev = &qq
				}
			}
		}
	}
	// Legend.
	lx := 10.0
	p(`<g font-size="12" fill="#ddd">`)
	if st != nil {
		for _, l := range st.Stack {
			p(`<rect x="%.0f" y="%.0f" width="12" height="12" fill="%s"/><text x="%.0f" y="%.0f">%s</text>`, lx, H+14, colour(l.ID), lx+16, H+24, esc(l.Name))
			lx += 110
		}
	}
	if rr != nil {
		p(`<text x="%.0f" y="%.0f">routed %.1f%%  vias %d  unrouted %d</text>`, lx, H+24, rr.Stats.Completion, rr.Stats.Vias+rr.Stats.FanoutVias, len(rr.Unrouted))
	}
	p("</g>\n</svg>\n")
	return nil
}

func esc(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}
