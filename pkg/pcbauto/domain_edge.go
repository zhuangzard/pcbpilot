package pcbauto

// domain_edge.go — domain zones against the connectors. The default layout gives
// each domain a full-height strip, ordered by the mean x of its fixed parts, so
// a domain's connector on the top edge often sits outside its own strip and its
// nets cross another domain. Here each small domain gets a rectangle against
// the edge of its connector, and the largest domain the largest rectangle left.
// It only plans: the zones go through the same Mechanics.Zones as a hand-written
// --mech. A layout that does not fit is refused, with no fallback to strips.

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	edgeZoneFill       = 0.6  // room a rectangle is sized for: parts / area
	edgeZoneMaxFill    = 0.85 // above this the largest domain's rectangle is refused
	edgeZoneAspect     = 2.5  // width along the edge / depth into the board: shallow leaves the largest domain room
	edgeZoneDefGap     = 40.0 // mil between two domains with no barrier between them
	edgeZoneMinOutline = 0.98 // outline / bounding box: rounded corners pass, cutouts do not
)

// EdgeZonePlan is a domain layout and what the report says about it.
type EdgeZonePlan struct {
	Zones []Zone   `json:"zones"`
	Notes []string `json:"notes,omitempty"`
}

type edgeDomain struct {
	id     string
	edge   string
	anchor Rect // the fixed part with most of the domain's pads on that edge
	area   float64
	rect   Rect
}

// edgeOfPoint is the board edge nearest q.
func edgeOfPoint(q Point, b Rect) string {
	best, name := q.X-b.MinX, "left"
	if d := b.MaxX - q.X; d < best {
		best, name = d, "right"
	}
	if d := q.Y - b.MinY; d < best {
		best, name = d, "bottom"
	}
	if d := b.MaxY - q.Y; d < best {
		name = "top"
	}
	return name
}

// edgeFixedBody is where a fixed part's body will be. An edge part only reaches
// its edge inside Place, so its along-edge centre comes from the spec.
func edgeFixedBody(p *Part, m *Mechanics, bb Rect) (Rect, string) {
	body := p.Body()
	if e, ok := m.Edge[p.Ref]; ok {
		w, h := body.W(), body.H()
		along := e.At
		switch e.Edge {
		case "top", "bottom":
			c := bb.Center().X
			if along >= 0 {
				c = bb.MinX + along
			}
			if e.Edge == "top" {
				return Rect{c - w/2, bb.MaxY - h, c + w/2, bb.MaxY}, "top"
			}
			return Rect{c - w/2, bb.MinY, c + w/2, bb.MinY + h}, "bottom"
		default:
			c := bb.Center().Y
			if along >= 0 {
				c = bb.MinY + along
			}
			if e.Edge == "right" {
				return Rect{bb.MaxX - w, c - h/2, bb.MaxX, c + h/2}, "right"
			}
			return Rect{bb.MinX, c - h/2, bb.MinX + w, c + h/2}, "left"
		}
	}
	return body, edgeOfPoint(body.Center(), bb)
}

// PlanEdgeZones lays out the domains against their connectors, inside region.
func PlanEdgeZones(b *Board, c *Circuit, m *Mechanics, region Rect, spacing float64) (*EdgeZonePlan, error) {
	if m == nil {
		m = &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}}
	}
	// Zones are rectangles: on a board that is not one they could leave it.
	if bbox := b.Bounds(); len(b.Outline) >= 3 && polygonArea(b.Outline) < edgeZoneMinOutline*bbox.Area() {
		return nil, fmt.Errorf("domain zones: the board outline is not a rectangle (%.0f %% of its bounding box); give zones with --mech", 100*polygonArea(b.Outline)/bbox.Area())
	}
	netDomain := map[string]string{}
	var doms []*Domain
	barred := map[string]bool{}
	for _, br := range c.Barriers {
		barred[br.A], barred[br.B] = true, true
	}
	for _, d := range c.Domains {
		// A domain in no insulation pair needs no zone of its own.
		if d.ID == "UNREFERENCED" || !barred[d.ID] {
			continue
		}
		doms = append(doms, d)
		for _, n := range d.Nets {
			netDomain[n] = d.ID
		}
	}
	if len(doms) < 2 {
		return nil, fmt.Errorf("domain zones: %d domain(s), nothing to separate", len(doms))
	}
	area := map[string]float64{}
	for _, d := range doms {
		for _, ref := range d.Parts {
			if p := b.Part(ref); p != nil && !c.IsBridge(ref) {
				area[d.ID] += p.Body().Expand(spacing / 2).Area()
			}
		}
	}
	sort.SliceStable(doms, func(i, j int) bool {
		if area[doms[i].ID] != area[doms[j].ID] {
			return area[doms[i].ID] > area[doms[j].ID]
		}
		return doms[i].ID < doms[j].ID
	})
	big := doms[0]
	bb := b.Bounds()
	plan := &EdgeZonePlan{}

	gap := func(a, b string) float64 {
		for _, br := range c.Barriers {
			if br.A == a && br.B == b || br.A == b && br.B == a {
				return math.Max(br.CreepageMil, br.ClearanceMil)
			}
		}
		return edgeZoneDefGap
	}

	// Anchors, pad by pad: a shared connector counts for each domain with pads
	// on it, and the fixed part holding most of a domain's pads anchors it.
	type anchor struct {
		ref  string
		body Rect
		edge string
		pads int
	}
	byDomain := map[string]map[string]*anchor{}
	for _, p := range b.Parts {
		if !(p.Fixed || m.Fixed[p.Ref]) {
			continue
		}
		body, edge := edgeFixedBody(p, m, bb)
		// The circuit's own assignment holds where intent net names and board
		// net names differ, which kicad place does not resolve.
		if d := c.DomainOf[p.Ref]; d != "" && d != big.ID && d != "UNREFERENCED" {
			if byDomain[d] == nil {
				byDomain[d] = map[string]*anchor{}
			}
			byDomain[d][p.Ref] = &anchor{ref: p.Ref, body: body, edge: edge}
		}
		for _, pd := range p.Pads {
			d := netDomain[pd.Net]
			if d == "" || d == big.ID {
				continue
			}
			if byDomain[d] == nil {
				byDomain[d] = map[string]*anchor{}
			}
			if byDomain[d][p.Ref] == nil {
				byDomain[d][p.Ref] = &anchor{ref: p.Ref, body: body, edge: edge}
			}
			byDomain[d][p.Ref].pads++
		}
	}

	var small []*edgeDomain
	for _, d := range doms[1:] {
		as := byDomain[d.ID]
		if len(as) == 0 {
			return nil, fmt.Errorf("domain zones: domain %s has no fixed part to sit against", d.ID)
		}
		var list []*anchor
		for _, a := range as {
			list = append(list, a)
		}
		// A part the spec puts on an edge outranks any other fixed part: that
		// is the connector the domain has to reach.
		sort.Slice(list, func(i, j int) bool {
			_, ei := m.Edge[list[i].ref]
			_, ej := m.Edge[list[j].ref]
			if ei != ej {
				return ei
			}
			// The connector the circuit assigns to this domain outranks one it
			// merely has pads on.
			oi, oj := c.DomainOf[list[i].ref] == d.ID, c.DomainOf[list[j].ref] == d.ID
			if oi != oj {
				return oi
			}
			if list[i].pads != list[j].pads {
				return list[i].pads > list[j].pads
			}
			return list[i].ref < list[j].ref
		})
		main := list[0]
		for _, a := range list[1:] {
			plan.Notes = append(plan.Notes, fmt.Sprintf("domain %s: %d pad(s) on %s stay outside its zone (it sits against %s)", d.ID, a.pads, a.ref, main.ref))
		}
		small = append(small, &edgeDomain{id: d.ID, edge: main.edge, anchor: main.body, area: area[d.ID] / edgeZoneFill})
	}

	// A rectangle against its edge, centred on its anchor.
	for _, e := range small {
		along := math.Sqrt(e.area * edgeZoneAspect)
		horizontal := e.edge == "top" || e.edge == "bottom"
		span := e.anchor.H()
		if horizontal {
			span = e.anchor.W()
		}
		// Stay clear of the next connector on this edge: narrower and deeper
		// rather than pushed off its own.
		mid := func(x *edgeDomain) float64 {
			if horizontal {
				return x.anchor.Center().X
			}
			return x.anchor.Center().Y
		}
		for _, o := range small {
			if o != e && o.edge == e.edge {
				if room := math.Abs(mid(o)-mid(e)) - gap(e.id, o.id); room < along {
					along = room
				}
			}
		}
		if along < span+2*spacing {
			along = span + 2*spacing
		}
		depth := e.area / along
		c := e.anchor.Center()
		switch e.edge {
		case "top":
			e.rect = Rect{c.X - along/2, region.MaxY - depth, c.X + along/2, region.MaxY}
		case "bottom":
			e.rect = Rect{c.X - along/2, region.MinY, c.X + along/2, region.MinY + depth}
		case "left":
			e.rect = Rect{region.MinX, c.Y - along/2, region.MinX + depth, c.Y + along/2}
		default:
			e.rect = Rect{region.MaxX - depth, c.Y - along/2, region.MaxX, c.Y + along/2}
		}
	}

	// Along each edge, in order: first keep inside the board, then push apart by
	// the pair's gap, always away from the board's centre of the row.
	for _, name := range []string{"top", "bottom", "left", "right"} {
		var row []*edgeDomain
		for _, e := range small {
			if e.edge == name {
				row = append(row, e)
			}
		}
		if len(row) == 0 {
			continue
		}
		horizontal := name == "top" || name == "bottom"
		lo := func(r Rect) float64 {
			if horizontal {
				return r.MinX
			}
			return r.MinY
		}
		hi := func(r Rect) float64 {
			if horizontal {
				return r.MaxX
			}
			return r.MaxY
		}
		shift := func(r *Rect, d float64) {
			if horizontal {
				r.MinX += d
				r.MaxX += d
			} else {
				r.MinY += d
				r.MaxY += d
			}
		}
		sort.Slice(row, func(i, j int) bool { return lo(row[i].rect) < lo(row[j].rect) })
		start, end := region.MinY, region.MaxY
		if horizontal {
			start, end = region.MinX, region.MaxX
		}
		pos := start
		for i, e := range row {
			if lo(e.rect) < pos {
				shift(&e.rect, pos-lo(e.rect))
			}
			if i+1 < len(row) {
				pos = hi(e.rect) + gap(e.id, row[i+1].id)
			}
		}
		pos = end
		for i := len(row) - 1; i >= 0; i-- {
			e := row[i]
			if hi(e.rect) > pos {
				shift(&e.rect, pos-hi(e.rect))
			}
			if i > 0 {
				pos = lo(e.rect) - gap(e.id, row[i-1].id)
			}
		}
		for i, e := range row {
			if lo(e.rect) < start-1e-6 {
				return nil, fmt.Errorf("domain zones: the %s edge is too short for its domains (%s does not fit)", name, e.id)
			}
			if i > 0 && lo(e.rect) < hi(row[i-1].rect)+gap(e.id, row[i-1].id)-1e-6 {
				return nil, fmt.Errorf("domain zones: the %s edge is too short for domains %s and %s", name, row[i-1].id, e.id)
			}
			if !rectHolds(e.rect.Expand(spacing), e.anchor, horizontal) {
				return nil, fmt.Errorf("domain zones: domain %s was pushed off its connector on the %s edge (zone %+v, connector %+v, row %s)", e.id, name, e.rect, e.anchor, edgeRowText(row))
			}
		}
	}

	// A foreign fixed part too near a rectangle: trim the side that loses least,
	// if the anchor stays inside and the parts still fit.
	for _, e := range small {
		for _, d := range c.Domains {
			if d.ID == e.id {
				continue
			}
			for _, ref := range d.Parts {
				p := b.Part(ref)
				if p == nil || !(p.Fixed || m.Fixed[ref]) || c.IsBridge(ref) || byDomain[e.id][ref] != nil {
					continue
				}
				body, _ := edgeFixedBody(p, m, bb)
				if t, ok := trimClear(e.rect, body, gapIf(c, d.ID, e.id), e.anchor, spacing, e.edge == "top" || e.edge == "bottom"); ok && t.Area()*edgeZoneMaxFill >= area[e.id] {
					e.rect = t
				}
			}
		}
	}

	// Depth into the board: a rectangle deeper than the board is not a layout.
	for _, e := range small {
		if e.rect.MinX < region.MinX-1e-6 || e.rect.MaxX > region.MaxX+1e-6 || e.rect.MinY < region.MinY-1e-6 || e.rect.MaxY > region.MaxY+1e-6 {
			return nil, fmt.Errorf("domain zones: domain %s does not fit the board against the %s edge", e.id, e.edge)
		}
	}
	// Every pair, including ones on different edges: corners and neighbours.
	for i, x := range small {
		for _, y := range small[i+1:] {
			if rectGap(x.rect, y.rect) < gap(x.id, y.id)-1e-6 {
				return nil, fmt.Errorf("domain zones: domains %s and %s are %.0f mil apart, they need %.0f", x.id, y.id, rectGap(x.rect, y.rect), gap(x.id, y.id))
			}
		}
	}

	// The largest domain: the largest rectangle clear of the others by its gaps.
	var holes []Rect
	for _, e := range small {
		holes = append(holes, e.rect.Expand(gap(big.ID, e.id)))
	}
	rest, ok := largestFreeRect(region, holes)
	if !ok {
		return nil, fmt.Errorf("domain zones: no room left for domain %s", big.ID)
	}
	if fill := area[big.ID] / rest.Area(); fill > edgeZoneMaxFill {
		return nil, fmt.Errorf("domain zones: domain %s needs %.0f mil² in a rectangle of %.0f (fill %.2f, limit %.2f)", big.ID, area[big.ID], rest.Area(), fill, edgeZoneMaxFill)
	}
	// No fixed part may end up in another domain's rectangle: it cannot move out.
	zoneOf := map[string]Rect{big.ID: rest}
	for _, e := range small {
		zoneOf[e.id] = e.rect
	}
	ids := make([]string, 0, len(zoneOf))
	for id := range zoneOf {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	// Every domain's parts, those in no pair too.
	for _, d := range c.Domains {
		for _, ref := range d.Parts {
			p := b.Part(ref)
			if p == nil || !(p.Fixed || m.Fixed[ref]) || c.IsBridge(ref) {
				continue
			}
			body, _ := edgeFixedBody(p, m, bb)
			for _, id := range ids {
				// A part with pads of that domain belongs there too: a
				// connector shared by two domains is not foreign to either.
				if id == d.ID || byDomain[id][ref] != nil {
					continue
				}
				need := 0.0
				if barred[d.ID] {
					need = gapIf(c, d.ID, id)
				}
				if g := rectGap(body, zoneOf[id]); g <= 0 || g < need-1e-6 {
					// A small domain's fixed part away from any edge (a mounting
					// hole on a chassis net): the design's question, not the
					// layout's. The isolation gate judges its copper.
					if _, onEdge := m.Edge[ref]; !onEdge && d.ID != big.ID {
						plan.Notes = append(plan.Notes, fmt.Sprintf("%s of domain %s sits %.0f mil from the zone of domain %s", ref, d.ID, g, id))
						continue
					}
					return nil, fmt.Errorf("domain zones: %s of domain %s lies inside the zone of domain %s or too near it, %.0f mil from it, %.0f needed (part %+v, zone %+v)", ref, d.ID, id, g, need, body, zoneOf[id])
				}
			}
		}
	}
	plan.Zones = append(plan.Zones, Zone{Domain: big.ID, Rect: rest})
	for _, e := range small {
		plan.Zones = append(plan.Zones, Zone{Domain: e.id, Rect: e.rect})
	}
	plan.Notes = append(plan.Notes, "domain zones against the connectors: bridges are placed freely, no isolation strips (KiCad's rules and the router's pair clearances hold the gaps)")
	return plan, nil
}

// gapIf is the pair's insulation distance, 0 when they are no pair.
func gapIf(c *Circuit, a, b string) float64 {
	for _, br := range c.Barriers {
		if br.A == a && br.B == b || br.A == b && br.B == a {
			return math.Max(br.CreepageMil, br.ClearanceMil)
		}
	}
	return 0
}

// polygonArea is the area of a closed outline.
func polygonArea(ps []Point) float64 {
	sum := 0.0
	for i, p := range ps {
		q := ps[(i+1)%len(ps)]
		sum += p.X*q.Y - q.X*p.Y
	}
	return math.Abs(sum) / 2
}

// trimClear moves one side of r in so that it clears body by need, losing as
// little area as it can and keeping anchor inside. Nothing to do or no way to do
// it returns false.
func trimClear(r, body Rect, need float64, anchor Rect, spacing float64, horizontal bool) (Rect, bool) {
	if rectGap(r, body) >= need && rectGap(r, body) > 0 {
		return r, false
	}
	var cands []Rect
	if t := (Rect{body.MaxX + need, r.MinY, r.MaxX, r.MaxY}); t.MinX > r.MinX {
		cands = append(cands, t)
	}
	if t := (Rect{r.MinX, r.MinY, body.MinX - need, r.MaxY}); t.MaxX < r.MaxX {
		cands = append(cands, t)
	}
	if t := (Rect{r.MinX, body.MaxY + need, r.MaxX, r.MaxY}); t.MinY > r.MinY {
		cands = append(cands, t)
	}
	if t := (Rect{r.MinX, r.MinY, r.MaxX, body.MinY - need}); t.MaxY < r.MaxY {
		cands = append(cands, t)
	}
	best, found := Rect{}, false
	for _, t := range cands {
		// Along the edge only: the connector itself reaches past the region.
		held := rectHolds(t.Expand(spacing), anchor, horizontal)
		if t.W() > 0 && t.H() > 0 && held && (!found || t.Area() > best.Area()) {
			best, found = t, true
		}
	}
	return best, found
}

// rectHolds says whether outer covers inner along the edge's direction.
func rectHolds(outer, inner Rect, horizontal bool) bool {
	if horizontal {
		return outer.MinX <= inner.MinX && outer.MaxX >= inner.MaxX
	}
	return outer.MinY <= inner.MinY && outer.MaxY >= inner.MaxY
}

// rectGap is the distance between two rectangles, 0 when they touch or overlap.
func rectGap(a, b Rect) float64 {
	dx := math.Max(0, math.Max(a.MinX-b.MaxX, b.MinX-a.MaxX))
	dy := math.Max(0, math.Max(a.MinY-b.MaxY, b.MinY-a.MaxY))
	return math.Hypot(dx, dy)
}

// largestFreeRect is the largest rectangle of region that overlaps no hole. Its
// sides lie on region's or a hole's sides, so those are all that is tried.
func largestFreeRect(region Rect, holes []Rect) (Rect, bool) {
	xs := []float64{region.MinX, region.MaxX}
	ys := []float64{region.MinY, region.MaxY}
	for _, h := range holes {
		for _, x := range []float64{h.MinX, h.MaxX} {
			if x > region.MinX && x < region.MaxX {
				xs = append(xs, x)
			}
		}
		for _, y := range []float64{h.MinY, h.MaxY} {
			if y > region.MinY && y < region.MaxY {
				ys = append(ys, y)
			}
		}
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	var best Rect
	found := false
	bestArea := 0.0
	for i, x0 := range xs {
		for _, x1 := range xs[i+1:] {
			for j, y0 := range ys {
				for _, y1 := range ys[j+1:] {
					r := Rect{x0, y0, x1, y1}
					if a := r.Area(); a <= bestArea {
						continue
					}
					free := true
					for _, h := range holes {
						if r.MinX < h.MaxX && r.MaxX > h.MinX && r.MinY < h.MaxY && r.MaxY > h.MinY {
							free = false
							break
						}
					}
					if free {
						best, bestArea, found = r, r.Area(), true
					}
				}
			}
		}
	}
	return best, found
}

func edgeRowText(row []*edgeDomain) string {
	var out []string
	for _, e := range row {
		out = append(out, fmt.Sprintf("%s at %.0f..%.0f,%.0f..%.0f", e.id, e.rect.MinX, e.rect.MaxX, e.rect.MinY, e.rect.MaxY))
	}
	return strings.Join(out, "; ")
}
