package app

// pcb_edge_exempt.go — board-edge keep-out bands (DSN) must not wall off the
// pads of edge-mounted connectors: a USB / terminal footprint that sits on or
// over the outline has its pads inside the copper-to-edge band by design, and
// a full band left them unroutable. The parts are the copper-to-edge gate's
// edge-mounted parts (pkg/pcbauto checkEdge: a footprint box corner outside
// or within 1 mil of the outline); the gate still judges their pads (WARN
// above the fab floor, ERROR below it) and every other copper item.

import (
	"fmt"
)

// edgeMountedParts are the designators whose footprint box reaches the
// board outline.
func edgeMountedParts(snap *boardSnapshot) map[string]bool {
	out := map[string]bool{}
	if snap == nil || snap.Outline == nil {
		return out
	}
	for _, c := range snap.Components {
		if c.BBox == nil {
			continue
		}
		b := c.BBox
		for _, q := range [][2]float64{{b.MinX, b.MinY}, {b.MaxX, b.MinY}, {b.MaxX, b.MaxY}, {b.MinX, b.MaxY}} {
			if snap.Outline.distToEdge(q[0], q[1]) < 1 {
				out[c.Designator] = true
				break
			}
		}
	}
	return out
}

// boxInBand reports whether a pad box reaches into the band: a corner nearer
// the outline than band, or outside it (distToEdge is negative there). A pad
// clear of the band needs no window, and a window lets tracks reach the edge.
func boxInBand(o *boardOutline, b silkBox, band float64) bool {
	for _, q := range [][2]float64{{b.MinX, b.MinY}, {b.MaxX, b.MinY}, {b.MaxX, b.MaxY}, {b.MinX, b.MaxY}} {
		if o.distToEdge(q[0], q[1]) < band {
			return true
		}
	}
	return false
}

// edgeExemptBoxes returns the box of every pad of an edge-mounted part that
// reaches into band of the outline, grown by grow (the escape margin: the
// clearance a track leaving the pad keeps to the band), in the snapshot's
// frame (mil) — and "REF.PAD" names for the summary.
func edgeExemptBoxes(snap *boardSnapshot, band, grow float64) ([][4]float64, []string) {
	parts := edgeMountedParts(snap)
	var boxes [][4]float64
	var names []string
	for _, c := range snap.Components {
		if !parts[c.Designator] {
			continue
		}
		for _, p := range c.Pads {
			b := padBox(p)
			if !boxInBand(snap.Outline, b, band) {
				continue
			}
			b = b.grow(grow)
			boxes = append(boxes, [4]float64{b.MinX, b.MinY, b.MaxX, b.MaxY})
			names = append(names, fmt.Sprintf("%s.%s", c.Designator, p.Number))
		}
	}
	return boxes, names
}
