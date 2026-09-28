package app

// pcb_pour_edge.go — the board-edge safety distance for the pour commands
// (pour-fit, power-pour, power-planes). Their boundary used to be the
// outline BBOX (stroke-inclusive: +5 mil on a 10 mil outline) inset by the
// host's 10 mil rule — ESP32 v0.5 live: TOP GND poured 14.1 mil from the
// routed edge, and the rectangle cut across the rounded corners. Now the
// boundary is the outline CENTRE-LINE polygon inset (mitred) by the edge
// distance of that layer class: 20 mil outer / 30 mil inner by default,
// intent.json "edge" with --intent, never below the live Board Outline rule
// or the fab floor; --inset overrides (clamped to the fab floor).

import (
	"fmt"
	"io"
	"math"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// pourEdgeOpts are the shared --inset / --intent / --edge-kind flags.
type pourEdgeOpts struct {
	inset      float64
	insetSet   bool
	intentPath string
	edgeKind   string
}

// edgePolicy resolves the policy: intent (or defaults) + the live rule.
func (o pourEdgeOpts) edgePolicy(cfg *appConfig, window string) (*pcbauto.EdgePolicy, error) {
	in, err := loadEdgeIntent(o.intentPath)
	if err != nil {
		return nil, fmt.Errorf("intent: %w", err)
	}
	pol := pcbauto.EdgeFromIntent(in, nil)
	if o.edgeKind != "" {
		k, kerr := pcbauto.NormEdgeKind(o.edgeKind)
		if kerr != nil {
			return nil, kerr
		}
		oo, ii, v, f, _ := pcbauto.EdgeDefaults(k)
		pol.Kind, pol.OuterMil, pol.InnerMil, pol.VcutMil, pol.FabMinMil = k, math.Max(pol.OuterMil, oo), math.Max(pol.InnerMil, ii), v, f
	}
	if cfg != nil {
		pol.RuleMil = fetchPcbRules(cfg, window).copperToEdgeMil
	}
	return pol, nil
}

// insetFor returns the boundary inset of a pour of net on layer and why.
func (o pourEdgeOpts) insetFor(pol *pcbauto.EdgePolicy, layer int, net string, stderr io.Writer) float64 {
	if o.insetSet {
		v := o.inset
		if v < pol.FabMinMil {
			fmt.Fprintf(stderr, "warning: --inset %.1f mil is below the fab floor %.1f mil (%s edge) — using the floor\n", v, pol.FabMinMil, pol.Kind)
			v = pol.FabMinMil
		}
		if req := pol.Req(layer, net); v < req {
			fmt.Fprintf(stderr, "warning: --inset %.1f mil is below the board-edge safety distance %.1f mil for layer %d (pcb check copper-to-edge will flag it)\n", v, req, layer)
		}
		return v
	}
	return pol.Req(layer, net)
}

// pourBoundary reads the board outline and returns it inset by inset mil:
// the centre-line polygon (the real cut) offset inward, or the centre-line
// rectangle when the connector gives no polygon. Never the rendered bbox.
func pourBoundary(cfg *appConfig, window string, inset float64) ([][]float64, [4]float64, error) {
	res, err := requestAction(cfg, "pcb.outline.get", window, nil)
	if err != nil || res == nil {
		return nil, [4]float64{}, fmt.Errorf("outline.get failed: %v", err)
	}
	o := parseBoardOutline(res.Result)
	var poly []pcbauto.Point
	if o != nil && len(o.Points) >= 3 {
		for _, p := range o.Points {
			poly = append(poly, pcbauto.Point{X: p[0], Y: p[1]})
		}
	} else if r, rerr := currentOutlineCenterlineRect(cfg, window); rerr == nil {
		poly = []pcbauto.Point{{X: r[0], Y: r[1]}, {X: r[2], Y: r[1]}, {X: r[2], Y: r[3]}, {X: r[0], Y: r[3]}}
	} else if o != nil && o.BBox.MaxX > o.BBox.MinX {
		// Older connector: only the rendered (stroke-inclusive) bbox. The
		// cut line lies half a stroke inside it; take the default outline
		// stroke off so the inset is measured from the cut, not the ink.
		h := boardOutlineLineWidthMil / 2
		b := o.BBox
		poly = []pcbauto.Point{{X: b.MinX + h, Y: b.MinY + h}, {X: b.MaxX - h, Y: b.MinY + h}, {X: b.MaxX - h, Y: b.MaxY - h}, {X: b.MinX + h, Y: b.MaxY - h}}
	} else {
		return nil, [4]float64{}, fmt.Errorf("no board outline polygon or bounds — set one with `pcb outline-set` (%v)", rerr)
	}
	return insetBoundary(poly, inset)
}

// insetBoundary is the pure half of pourBoundary.
func insetBoundary(outline []pcbauto.Point, inset float64) ([][]float64, [4]float64, error) {
	in := pcbauto.InsetPolygon(outline, inset+0.05) // +0.05: coordinates are rounded to 0.01 mil
	if in == nil {
		return nil, [4]float64{}, fmt.Errorf("inset %.1f mil leaves no copper area inside the board outline", inset)
	}
	pts := make([][]float64, len(in))
	bb := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for i, p := range in {
		x, y := math.Round(p.X*100)/100, math.Round(p.Y*100)/100
		pts[i] = []float64{x, y}
		bb = [4]float64{math.Min(bb[0], x), math.Min(bb[1], y), math.Max(bb[2], x), math.Max(bb[3], y)}
	}
	return pts, bb, nil
}

// clipToBoundary clips a rectangle to a (convex) inset boundary; a
// non-convex boundary keeps the rectangle clamped to its bounds.
func clipToBoundary(rect [][]float64, boundary [][]float64) [][]float64 {
	var subj, clip []pcbauto.Point
	for _, p := range rect {
		subj = append(subj, pcbauto.Point{X: p[0], Y: p[1]})
	}
	for _, p := range boundary {
		clip = append(clip, pcbauto.Point{X: p[0], Y: p[1]})
	}
	out := pcbauto.ClipConvex(subj, clip)
	if out == nil {
		return rect
	}
	res := make([][]float64, len(out))
	for i, p := range out {
		res[i] = []float64{math.Round(p.X*100) / 100, math.Round(p.Y*100) / 100}
	}
	return res
}
