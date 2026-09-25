package app

// pcb_footprint_holes.go — footprint NPTH / slot regions as board geometry.
//
// Defect (E2E 2026-09-25, EasyEDA V3 3.2.149): native DRC failed with "Slot
// Region to Track/Via, Hole to Slot Region" around the USB-C J2. The slot
// regions are NOT pads: they are MULTI-layer (layerId 12) FILL primitives inside
// the footprint document (J2's two Ø0.75 mm locating holes). `pcb dump` exposed
// pads only, so pcbauto routed straight through them and `pcb check` could not
// see them. This file parses the footprint source (pcb.footprint.sources),
// extracts those FILLs in footprint-local mil, and places them on the board with
// the component pose — verified against the same footprint's pad positions.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// defaultSlotClearanceMil is the copper → NPTH/slot-region spacing used when the
// board's Safe Spacing matrix does not expose a Slot Region entry: 0.3 mm, the
// JLCEDA default and exactly what native DRC applied on ceshi (1.1811 in its
// 10-mil unit = 11.811 mil). Never below the board's track↔pad clearance.
const defaultSlotClearanceMil = 11.811

// boardFootprintHole is one NPTH / slot region from a placed footprint, in
// board coordinates (mil, y-up). It has no net: it removes board material on
// every layer, so all copper keeps the slot-clearance rule from its edge.
type boardFootprintHole struct {
	Owner         string       `json:"owner"` // designator
	ComponentID   string       `json:"componentId,omitempty"`
	FootprintUUID string       `json:"footprintUuid,omitempty"`
	SourceID      string       `json:"sourceId"`         // footprint primitive id (e45)
	Shape         string       `json:"shape"`            // circle | polygon
	X             float64      `json:"x"`                // circle centre / polygon bounds centre
	Y             float64      `json:"y"`                //
	Dia           float64      `json:"dia,omitempty"`    // circle only
	Points        [][2]float64 `json:"points,omitempty"` // polygon only (closed implicitly)
	// Envelope = the polygon is a conservative envelope of the source shape
	// (arcs → convex hull of both bulge candidates; R rect → symmetric box).
	Envelope bool `json:"envelope,omitempty"`
	// Transform records how footprint-local coordinates reached the board:
	// "pad-verified" (the same transform reproduces this footprint's pads in the
	// dump within 1 mil) or "assumed" (no pad evidence; top = rotate, bottom =
	// mirror X then rotate).
	Transform string `json:"transform"`
}

// fpLocalShape is a hole in footprint-local mil.
type fpLocalShape struct {
	ID       string
	Circle   bool
	C        [2]float64
	R        float64
	Poly     [][2]float64
	Envelope bool
}

// fpLocalPad is a footprint pad centre in footprint-local mil.
type fpLocalPad struct {
	ID, Num string
	X, Y    float64
}

// parseFootprintSourceHoles reads one footprint document (DOCHEAD-bounded
// `header||payload|` record stream) and returns its MULTI-layer FILL shapes and
// its pad centres. Shapes it cannot decode are reported, never dropped silently.
func parseFootprintSourceHoles(source string) (holes []fpLocalShape, pads []fpLocalPad, problems []string, err error) {
	heads := 0
	for _, raw := range strings.Split(source, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		i := strings.Index(line, "||")
		if i < 0 {
			return nil, nil, nil, fmt.Errorf("malformed footprint source row")
		}
		var header struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal([]byte(line[:i]), &header); err != nil {
			return nil, nil, nil, fmt.Errorf("footprint source header: %w", err)
		}
		body := strings.TrimSuffix(line[i+2:], "|")
		if strings.TrimSpace(body) == "" {
			continue
		}
		switch header.Type {
		case "DOCHEAD":
			var p struct {
				DocType string `json:"docType"`
			}
			if err := json.Unmarshal([]byte(body), &p); err != nil {
				return nil, nil, nil, fmt.Errorf("footprint DOCHEAD: %w", err)
			}
			heads++
			if heads > 1 || p.DocType != "FOOTPRINT" {
				return nil, nil, nil, fmt.Errorf("source is not exactly one FOOTPRINT document")
			}
		case "FILL":
			var p struct {
				LayerID int `json:"layerId"`
				Path    any `json:"path"`
			}
			if err := json.Unmarshal([]byte(body), &p); err != nil {
				return nil, nil, nil, fmt.Errorf("footprint FILL %s: %w", header.ID, err)
			}
			if p.LayerID != pcbLayerMulti {
				continue
			}
			sh, perr := parseFillPath(p.Path)
			if perr != nil {
				problems = append(problems, fmt.Sprintf("FILL %s: %v", header.ID, perr))
				continue
			}
			sh.ID = header.ID
			holes = append(holes, sh)
		case "PAD":
			var p struct {
				Num     string  `json:"num"`
				CenterX float64 `json:"centerX"`
				CenterY float64 `json:"centerY"`
			}
			if err := json.Unmarshal([]byte(body), &p); err != nil {
				continue
			}
			pads = append(pads, fpLocalPad{ID: header.ID, Num: p.Num, X: p.CenterX, Y: p.CenterY})
		}
	}
	if heads != 1 {
		return nil, nil, nil, fmt.Errorf("source has no FOOTPRINT DOCHEAD")
	}
	return holes, pads, problems, nil
}

// parseFillPath decodes a FILL path. Forms seen in the wild:
//
//	["CIRCLE",x,y,r]            [["CIRCLE",x,y,r]]
//	[x,y,"L",x2,y2,…]           [[x,y,"L",x2,y2,…], …inner contours]
//	[x,y,"ARC",angle,x2,y2,…]   ["R",x,y,w,h,rot,round]
//
// Nested paths: the first contour is the outer boundary (inner contours are
// islands of board material inside the cutout — ignoring them is conservative).
func parseFillPath(v any) (fpLocalShape, error) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return fpLocalShape{}, fmt.Errorf("empty path")
	}
	if inner, ok := arr[0].([]any); ok {
		return parseFillPath(inner)
	}
	nums := func(from, n int) ([]float64, bool) {
		if len(arr) < from+n {
			return nil, false
		}
		out := make([]float64, n)
		for i := 0; i < n; i++ {
			f, ok := arr[from+i].(float64)
			if !ok {
				return nil, false
			}
			out[i] = f
		}
		return out, true
	}
	if cmd, ok := arr[0].(string); ok {
		switch strings.ToUpper(cmd) {
		case "CIRCLE":
			f, ok := nums(1, 3)
			if !ok || f[2] <= 0 {
				return fpLocalShape{}, fmt.Errorf("bad CIRCLE")
			}
			return fpLocalShape{Circle: true, C: [2]float64{f[0], f[1]}, R: f[2]}, nil
		case "R":
			f, ok := nums(1, 4)
			if !ok || f[2] <= 0 || f[3] <= 0 {
				return fpLocalShape{}, fmt.Errorf("bad R")
			}
			// The anchor convention of R (corner vs centre) is not verified on
			// a live footprint: take the symmetric box that contains every
			// reading at any rotation — conservative, flagged as envelope.
			r := math.Hypot(f[2], f[3])
			x, y := f[0], f[1]
			return fpLocalShape{Poly: [][2]float64{{x - r, y - r}, {x + r, y - r}, {x + r, y + r}, {x - r, y + r}}, Envelope: true}, nil
		}
		return fpLocalShape{}, fmt.Errorf("unsupported path command %q", cmd)
	}
	// Polyline with L / ARC / CARC commands.
	var pts [][2]float64
	hasArc := false
	i := 0
	readPt := func() ([2]float64, bool) {
		if i+1 >= len(arr) {
			return [2]float64{}, false
		}
		x, ok1 := arr[i].(float64)
		y, ok2 := arr[i+1].(float64)
		if !ok1 || !ok2 {
			return [2]float64{}, false
		}
		i += 2
		return [2]float64{x, y}, true
	}
	p0, ok := readPt()
	if !ok {
		return fpLocalShape{}, fmt.Errorf("polygon has no start point")
	}
	pts = append(pts, p0)
	mode := "L"
	for i < len(arr) {
		if s, ok := arr[i].(string); ok {
			mode = strings.ToUpper(s)
			i++
			continue
		}
		switch mode {
		case "L":
			p, ok := readPt()
			if !ok {
				return fpLocalShape{}, fmt.Errorf("bad L segment")
			}
			pts = append(pts, p)
		case "ARC", "CARC":
			if i+2 >= len(arr) {
				return fpLocalShape{}, fmt.Errorf("bad ARC segment")
			}
			ang, ok := arr[i].(float64)
			if !ok {
				return fpLocalShape{}, fmt.Errorf("bad ARC angle")
			}
			i++
			p, ok := readPt()
			if !ok {
				return fpLocalShape{}, fmt.Errorf("bad ARC end")
			}
			// The sweep direction convention is unverified: sample the arc on
			// both sides of the chord; the convex hull below then contains the
			// true boundary whichever side it bulges to.
			pts = append(pts, arcSamples(pts[len(pts)-1], p, ang)...)
			pts = append(pts, p)
			hasArc = true
		default:
			return fpLocalShape{}, fmt.Errorf("unsupported path command %q", mode)
		}
	}
	if len(pts) >= 2 && pts[0] == pts[len(pts)-1] {
		pts = pts[:len(pts)-1]
	}
	if len(pts) < 3 {
		return fpLocalShape{}, fmt.Errorf("polygon has %d vertices", len(pts))
	}
	if hasArc {
		return fpLocalShape{Poly: convexHull2(pts), Envelope: true}, nil
	}
	return fpLocalShape{Poly: pts}, nil
}

// arcSamples returns points on both circular arcs of sweep |angle| degrees
// through a and b (one bulging to each side of the chord).
func arcSamples(a, b [2]float64, angle float64) [][2]float64 {
	sweep := math.Abs(angle) * math.Pi / 180
	chord := math.Hypot(b[0]-a[0], b[1]-a[1])
	if sweep < 1e-6 || chord < 1e-9 || sweep >= 2*math.Pi {
		return nil
	}
	r := chord / (2 * math.Sin(sweep/2))
	mx, my := (a[0]+b[0])/2, (a[1]+b[1])/2
	nx, ny := -(b[1]-a[1])/chord, (b[0]-a[0])/chord
	h := r * math.Cos(sweep/2) // centre offset from the chord midpoint
	var out [][2]float64
	for _, side := range []float64{1, -1} {
		cx, cy := mx+side*h*nx, my+side*h*ny
		a0 := math.Atan2(a[1]-cy, a[0]-cx)
		a1 := math.Atan2(b[1]-cy, b[0]-cx)
		d := a1 - a0
		// Take the sweep that matches |angle| (short vs long way round).
		for d <= -math.Pi {
			d += 2 * math.Pi
		}
		for d > math.Pi {
			d -= 2 * math.Pi
		}
		if math.Abs(math.Abs(d)-sweep) > 1e-6 {
			if d > 0 {
				d -= 2 * math.Pi
			} else {
				d += 2 * math.Pi
			}
		}
		const n = 16
		for k := 1; k < n; k++ {
			t := a0 + d*float64(k)/n
			out = append(out, [2]float64{cx + r*math.Cos(t), cy + r*math.Sin(t)})
		}
	}
	return out
}

// convexHull2 is Andrew's monotone chain (CCW, no repeated end point).
func convexHull2(pts [][2]float64) [][2]float64 {
	p := append([][2]float64(nil), pts...)
	sort.Slice(p, func(i, j int) bool {
		if p[i][0] != p[j][0] {
			return p[i][0] < p[j][0]
		}
		return p[i][1] < p[j][1]
	})
	cross := func(o, a, b [2]float64) float64 {
		return (a[0]-o[0])*(b[1]-o[1]) - (a[1]-o[1])*(b[0]-o[0])
	}
	var h [][2]float64
	for _, q := range p {
		for len(h) >= 2 && cross(h[len(h)-2], h[len(h)-1], q) <= 0 {
			h = h[:len(h)-1]
		}
		h = append(h, q)
	}
	lo := len(h) + 1
	for i := len(p) - 2; i >= 0; i-- {
		q := p[i]
		for len(h) >= lo && cross(h[len(h)-2], h[len(h)-1], q) <= 0 {
			h = h[:len(h)-1]
		}
		h = append(h, q)
	}
	return h[:len(h)-1]
}

// fpTransform maps footprint-local mil to board mil: optional X mirror, then a
// CCW rotation by Rot degrees, then translation to the component anchor.
// Verified on ceshi (EasyEDA V3 3.2.149): all 31 top-side parts at 0/90/180/
// 270° reproduce their dump pad centres with (mirror=false, rot=component rot).
type fpTransform struct {
	X, Y, Rot float64
	MirrorX   bool
}

func (t fpTransform) apply(p [2]float64) [2]float64 {
	x, y := p[0], p[1]
	if t.MirrorX {
		x = -x
	}
	s, c := math.Sincos(t.Rot * math.Pi / 180)
	return [2]float64{t.X + x*c - y*s, t.Y + x*s + y*c}
}

// padVerifiedTransform picks the pose transform that reproduces the
// component's dumped pad centres from the footprint's local pads. Candidates
// are tried layer-default first (top: rotate; bottom: mirror X then rotate),
// so symmetric pad sets keep the default. ok=false → no pad evidence.
func padVerifiedTransform(c boardComp, pads []fpLocalPad) (fpTransform, bool) {
	bottom := c.Layer == pcbSideBottom
	cands := []fpTransform{
		{c.X, c.Y, c.Rotation, bottom},
		{c.X, c.Y, c.Rotation, !bottom},
		{c.X, c.Y, -c.Rotation, bottom},
		{c.X, c.Y, -c.Rotation, !bottom},
	}
	byID := map[string]fpLocalPad{}
	byNum := map[string][]fpLocalPad{}
	for _, p := range pads {
		byID[p.ID] = p
		byNum[p.Num] = append(byNum[p.Num], p)
	}
	type pair struct{ local, board [2]float64 }
	var pairs []pair
	for _, bp := range c.Pads {
		var lp fpLocalPad
		found := false
		if c.ID != "" && strings.HasPrefix(bp.ID, c.ID) {
			lp, found = byID[strings.TrimPrefix(bp.ID, c.ID)]
		}
		if !found && len(byNum[bp.Number]) == 1 {
			lp, found = byNum[bp.Number][0], true
		}
		if found {
			pairs = append(pairs, pair{[2]float64{lp.X, lp.Y}, [2]float64{bp.X, bp.Y}})
		}
	}
	if len(pairs) == 0 {
		return cands[0], false
	}
	const tol = 1.0 // dump pad centres are rounded to 0.1 mil
	for _, t := range cands {
		worst := 0.0
		for _, pr := range pairs {
			q := t.apply(pr.local)
			worst = math.Max(worst, math.Hypot(q[0]-pr.board[0], q[1]-pr.board[1]))
		}
		if worst <= tol {
			return t, true
		}
	}
	return cands[0], false
}

// placeFootprintHoles converts one component's local hole shapes to board
// geometry. verified=false when no candidate transform reproduced the pads.
func placeFootprintHoles(c boardComp, holes []fpLocalShape, pads []fpLocalPad) (out []boardFootprintHole, verified bool) {
	t, verified := padVerifiedTransform(c, pads)
	how := "pad-verified"
	if !verified {
		how = "assumed"
	}
	for _, h := range holes {
		fh := boardFootprintHole{
			Owner: c.Designator, ComponentID: c.ID, FootprintUUID: c.FootprintUUID,
			SourceID: h.ID, Transform: how, Envelope: h.Envelope,
		}
		if h.Circle {
			q := t.apply(h.C)
			fh.Shape, fh.X, fh.Y, fh.Dia = "circle", round2(q[0]), round2(q[1]), round2(2*h.R)
		} else {
			fh.Shape = "polygon"
			minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for _, p := range h.Poly {
				q := t.apply(p)
				q = [2]float64{round2(q[0]), round2(q[1])}
				fh.Points = append(fh.Points, q)
				minX, minY = math.Min(minX, q[0]), math.Min(minY, q[1])
				maxX, maxY = math.Max(maxX, q[0]), math.Max(maxY, q[1])
			}
			fh.X, fh.Y = round2((minX+maxX)/2), round2((minY+maxY)/2)
		}
		out = append(out, fh)
	}
	return out, verified
}

// footprintHolesFromSources is the pure core of the snapshot step: given the
// components and a footprintUuid → source map, return every NPTH/slot region
// plus human-readable degradation notes.
func footprintHolesFromSources(comps []boardComp, sources map[string]string) ([]boardFootprintHole, []string) {
	var out []boardFootprintHole
	var notes []string
	missingRef, missingSrc := 0, 0
	for _, c := range comps {
		if c.FootprintUUID == "" {
			missingRef++
			continue
		}
		src, ok := sources[c.FootprintUUID]
		if !ok {
			missingSrc++
			continue
		}
		holes, pads, problems, err := parseFootprintSourceHoles(src)
		if err != nil {
			notes = append(notes, fmt.Sprintf("footprint source of %s unreadable (%v) — its NPTH/slot regions are unknown", c.Designator, err))
			continue
		}
		for _, p := range problems {
			notes = append(notes, fmt.Sprintf("%s footprint: %s — that NPTH/slot region is not modelled", c.Designator, p))
		}
		if len(holes) == 0 {
			continue
		}
		placed, verified := placeFootprintHoles(c, holes, pads)
		if !verified {
			notes = append(notes, fmt.Sprintf("%s footprint holes placed with an assumed transform (pads did not verify it)", c.Designator))
		}
		out = append(out, placed...)
	}
	if missingRef > 0 {
		notes = append(notes, fmt.Sprintf("%d component(s) report no footprint instance uuid (connector predates components[].footprint) — their NPTH/slot regions are unknown", missingRef))
	}
	if missingSrc > 0 {
		notes = append(notes, fmt.Sprintf("%d component(s) have no footprint source in pcb.footprint.sources — their NPTH/slot regions are unknown", missingSrc))
	}
	return out, notes
}

// fetchFootprintHoles reads pcb.footprint.sources for the snapshot's parts and
// records the placed NPTH/slot regions. Failures degrade into Partial notes.
func (s *boardSnapshot) fetchFootprintHoles(cfg *appConfig, window string) {
	var uuids []string
	seen := map[string]bool{}
	for _, c := range s.Components {
		if c.FootprintUUID != "" && !seen[c.FootprintUUID] {
			seen[c.FootprintUUID] = true
			uuids = append(uuids, c.FootprintUUID)
		}
	}
	sort.Strings(uuids)
	sources := map[string]string{}
	if len(uuids) > 0 {
		res, err := requestAction(cfg, "pcb.footprint.sources", window, map[string]any{"footprintUuids": uuids})
		if err != nil || res == nil {
			s.note("footprint NPTH/slot regions unreadable (pcb.footprint.sources: %v) — routing/check cannot see footprint holes; re-import the connector", err)
			return
		}
		for _, raw := range mnavSlice(res.Result, "footprints") {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if id, src := asString(m["footprintUuid"]), asString(m["documentSource"]); id != "" && src != "" {
				sources[id] = src
			}
		}
	}
	holes, notes := footprintHolesFromSources(s.Components, sources)
	s.FootprintHoles = holes
	for _, n := range notes {
		s.note("%s", n)
	}
}

// slotClearance is the copper → footprint NPTH/slot spacing to enforce.
func slotClearance(r pcbRules) (float64, string) {
	if r.slotClearanceMil > 0 {
		return math.Max(r.slotClearanceMil, r.clearanceMil), "live rule"
	}
	return math.Max(defaultSlotClearanceMil, r.clearanceMil), "default 0.3mm (board rule has no Slot Region entry)"
}

// fpHoleDist is the distance from segment a–b to the hole region (0 inside).
func fpHoleDist(h boardFootprintHole, ax, ay, bx, by float64) float64 {
	if h.Shape == "circle" {
		return math.Max(segPtDist(h.X, h.Y, ax, ay, bx, by)-h.Dia/2, 0)
	}
	n := len(h.Points)
	if n < 3 {
		return math.Inf(1)
	}
	if polyContains2(h.Points, ax, ay) || polyContains2(h.Points, bx, by) {
		return 0
	}
	d := math.Inf(1)
	for i := 0; i < n; i++ {
		p, q := h.Points[i], h.Points[(i+1)%n]
		d = math.Min(d, segSegDist2(ax, ay, bx, by, p[0], p[1], q[0], q[1]))
	}
	return d
}

func polyContains2(poly [][2]float64, x, y float64) bool {
	in := false
	for i, j := 0, len(poly)-1; i < len(poly); j, i = i, i+1 {
		a, b := poly[i], poly[j]
		if (a[1] > y) != (b[1] > y) && x < (b[0]-a[0])*(y-a[1])/(b[1]-a[1])+a[0] {
			in = !in
		}
	}
	return in
}

func segSegDist2(ax, ay, bx, by, cx, cy, dx, dy float64) float64 {
	o := func(px, py, qx, qy, rx, ry float64) float64 { return (qx-px)*(ry-py) - (qy-py)*(rx-px) }
	d1, d2 := o(ax, ay, bx, by, cx, cy), o(ax, ay, bx, by, dx, dy)
	d3, d4 := o(cx, cy, dx, dy, ax, ay), o(cx, cy, dx, dy, bx, by)
	if ((d1 > 0) != (d2 > 0)) && ((d3 > 0) != (d4 > 0)) && d1 != 0 && d2 != 0 && d3 != 0 && d4 != 0 {
		return 0
	}
	return math.Min(math.Min(segPtDist(ax, ay, cx, cy, dx, dy), segPtDist(bx, by, cx, cy, dx, dy)),
		math.Min(segPtDist(cx, cy, ax, ay, bx, by), segPtDist(dx, dy, ax, ay, bx, by)))
}

// findFootprintHoleClearance flags copper closer than the slot rule to a
// footprint NPTH/slot region: tracks, vias and pads of OTHER parts (a part's
// own pads beside its own locating holes are the footprint author's call).
// ERROR: native DRC fails "Slot Region to Track/Via" at the same threshold.
func findFootprintHoleClearance(holes []boardFootprintHole, tracks []pcbTrack, vias []pcbViaP, pads []pcbPadP, clr float64) []pcbCheckFinding {
	var out []pcbCheckFinding
	const tol = 0.05
	label := func(h boardFootprintHole) string { return h.Owner + " " + h.SourceID }
	for _, h := range holes {
		for _, t := range tracks {
			if d := fpHoleDist(h, t.X1, t.Y1, t.X2, t.Y2) - t.Width/2; d < clr-tol {
				out = append(out, pcbCheckFinding{
					Type: "footprint-hole-clearance", Level: "ERROR", Net: t.Net, Layer: t.Layer, Designator: h.Owner,
					Primitives: []string{t.ID, h.ComponentID + h.SourceID}, At: &pcbXY{round2((t.X1 + t.X2) / 2), round2((t.Y1 + t.Y2) / 2)},
					Message: fmt.Sprintf("track (net %s) runs %.1fmil from footprint NPTH/slot %s — native DRC \"Slot Region to Track\" wants ≥ %.1fmil", t.Net, math.Max(d, 0), label(h), clr),
				})
			}
		}
		for _, v := range vias {
			if d := fpHoleDist(h, v.X, v.Y, v.X, v.Y) - v.Dia/2; d < clr-tol {
				out = append(out, pcbCheckFinding{
					Type: "footprint-hole-clearance", Level: "ERROR", Net: v.Net, Designator: h.Owner,
					Primitives: []string{v.ID, h.ComponentID + h.SourceID}, At: &pcbXY{round2(v.X), round2(v.Y)},
					Message: fmt.Sprintf("via (net %s) sits %.1fmil from footprint NPTH/slot %s — native DRC \"Slot Region to Via\" wants ≥ %.1fmil", v.Net, math.Max(d, 0), label(h), clr),
				})
			}
		}
		for _, p := range pads {
			if p.Designator == h.Owner {
				continue
			}
			d := fpHoleDist(h, p.X, p.Y, p.X, p.Y) - p.halfExt()
			if d < clr-tol {
				out = append(out, pcbCheckFinding{
					Type: "footprint-hole-clearance", Level: "ERROR", Net: p.Net, Designator: p.Designator,
					Primitives: []string{p.ID, h.ComponentID + h.SourceID}, At: &pcbXY{round2(p.X), round2(p.Y)},
					Message: fmt.Sprintf("pad %s.%s sits ~%.1fmil from footprint NPTH/slot %s — under the %.1fmil slot rule", p.Designator, p.Number, math.Max(d, 0), label(h), clr),
				})
			}
		}
	}
	return out
}

// reportFootprintHoles adds the footprint NPTH/slot findings to a check report.
func reportFootprintHoles(rep *pcbCheckReport, holes []boardFootprintHole, tracks []pcbTrack, vias []pcbViaP, pads []pcbPadP, rules pcbRules, stderr io.Writer) {
	clr, why := slotClearance(rules)
	fs := findFootprintHoleClearance(holes, tracks, vias, pads, clr)
	if len(fs) > 0 && stderr != nil {
		fmt.Fprintf(stderr, "footprint NPTH/slot clearance: %.1fmil (%s)\n", clr, why)
	}
	for _, f := range fs {
		rep.Findings = append(rep.Findings, f)
		rep.Summary.FootprintHoleClearance++
		rep.Summary.Errors++
		rep.Summary.Total++
	}
	rep.Passed = rep.Summary.Total == 0
}
