package pcbauto

// Board-edge safety distance (板边安全距离 / edge clearance).
//
// Copper that reaches the routed or V-cut board edge is exposed (the cutter
// runs through it), burrs on depaneling, and becomes an accessible live
// surface: leakage to a grounded chassis, a finger, a metal standoff. The
// fab's floor (JLC: 0.2 mm routed, 0.4 mm V-cut) is what the machine can
// hold, not a design value. Every design gets the defaults below unless the
// intent (intent.json "edge", derived from spec.json "edge") says otherwise;
// a hazardous / mains / patient domain additionally keeps its insulation
// distance (clearance and creepage, pkg/safety) to the edge and to metal
// mounting holes, which are accessible surfaces.
//
// Sources (docs/pcb-design-rules.md → Skill references/pcb-design-rules.md §5.4):
//   - JLCPCB PCB capabilities: copper to routed outline ≥ 0.2 mm, to a V-cut
//     line ≥ 0.4 mm (references/fab-rules-jlcpcb.json copperToEdgeMil).
//   - Fab DFM guidance (routed edge 0.25–0.5 mm, inner-plane pull-back
//     0.5–1.0 mm) and IPC-2221B's guidance that conductors keep clear of the
//     board edge by the fabricator's tolerance plus a margin: outer copper
//     20 mil (0.5 mm), inner planes 30 mil (0.76 mm) — a plane is the widest
//     copper on the board and the one the router cannot see.
//   - V-cut: 0.5 mm outer / 0.8 mm inner (the V groove and the snap leave a
//     rougher edge than a router).

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

// Engineering defaults (mil).
const (
	EdgeOuterMil     = 20.0 // 0.5 mm: outer copper (tracks, pads, vias, pours) to a routed edge
	EdgeInnerMil     = 30.0 // 0.76 mm: inner planes / pours (plane pull-back)
	EdgeVcutOuterMil = 20.0 // 0.5 mm: outer copper to a V-cut line (JLC floor 0.4 mm)
	EdgeVcutInnerMil = 32.0 // 0.8 mm (31.5 mil rounded up): inner copper to a V-cut line
	EdgeFabRoutedMil = 8.0  // JLC floor, routed outline (0.2 mm)
	EdgeFabVcutMil   = 16.0 // JLC floor, V-cut (0.4 mm)
)

// Edge kinds.
const (
	EdgeRouted = "routed"
	EdgeVcut   = "vcut"
	EdgeMixed  = "mixed"
)

// EdgeDomain is the edge distance one insulated domain needs.
type EdgeDomain struct {
	Mil         float64  `json:"mil"`
	ClearanceMm float64  `json:"clearanceMm,omitempty"`
	CreepageMm  float64  `json:"creepageMm,omitempty"`
	Insulation  string   `json:"insulation,omitempty"`
	Why         []string `json:"why,omitempty"`
}

// IntentEdge mirrors intent.json "edge" (additive field).
type IntentEdge struct {
	OuterMil float64                `json:"outerMil"`
	InnerMil float64                `json:"innerMil"`
	VcutMil  float64                `json:"vcutMil"`
	EdgeKind string                 `json:"edgeKind"`
	ByDomain map[string]*EdgeDomain `json:"byDomain,omitempty"`
	Why      []string               `json:"why,omitempty"`
}

// EdgePolicy is the resolved board-edge distance of one board.
type EdgePolicy struct {
	Kind      string                 `json:"edgeKind"`
	OuterMil  float64                `json:"outerMil"`
	InnerMil  float64                `json:"innerMil"`
	VcutMil   float64                `json:"vcutMil,omitempty"`
	FabMinMil float64                `json:"fabMinMil"`
	RuleMil   float64                `json:"boardRuleMil,omitempty"` // live Board Outline rule (a floor, never lowers the policy)
	ByDomain  map[string]*EdgeDomain `json:"byDomain,omitempty"`
	Source    string                 `json:"source"` // default | intent
	Why       []string               `json:"why,omitempty"`
	// netDomain maps a net to its insulated domain id (only domains listed
	// in ByDomain).
	netDomain map[string]string
}

// NormEdgeKind maps spellings to routed | vcut | mixed ("" = routed).
func NormEdgeKind(k string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(k, "-", ""))) {
	case "", "routed", "route", "milled":
		return EdgeRouted, nil
	case "vcut", "v":
		return EdgeVcut, nil
	case "mixed", "both":
		return EdgeMixed, nil
	}
	return "", fmt.Errorf("edgeKind %q: want routed|vcut|mixed", k)
}

// EdgeDefaults returns the default outer/inner/V-cut distances and the fab
// floor for an edge kind. A V-cut or mixed board applies the V-cut values
// to every edge: the outline does not say which edge is scored.
func EdgeDefaults(kind string) (outer, inner, vcut, fab float64, why []string) {
	k, err := NormEdgeKind(kind)
	if err != nil {
		k = EdgeRouted
	}
	outer, inner, fab = EdgeOuterMil, EdgeInnerMil, EdgeFabRoutedMil
	why = append(why, "routed edge: outer copper ≥ 20 mil (0.5 mm), inner planes ≥ 30 mil (0.76 mm) — engineering default above the JLC 0.2 mm floor (docs/pcb-design-rules.md §5.4)")
	if k != EdgeRouted {
		vcut = EdgeVcutOuterMil
		outer, inner, fab = math.Max(outer, EdgeVcutOuterMil), math.Max(inner, EdgeVcutInnerMil), EdgeFabVcutMil
		why = append(why, "V-cut edge: outer ≥ 0.5 mm, inner ≥ 0.8 mm (JLC V-cut floor 0.4 mm); applied to every edge because the outline does not mark the scored ones")
	}
	return outer, inner, vcut, fab, why
}

// DefaultEdgePolicy is the policy of a board without an intent.
func DefaultEdgePolicy(kind string) *EdgePolicy {
	k, err := NormEdgeKind(kind)
	if err != nil {
		k = EdgeRouted
	}
	o, i, v, f, why := EdgeDefaults(k)
	return &EdgePolicy{Kind: k, OuterMil: o, InnerMil: i, VcutMil: v, FabMinMil: f, Source: "default", Why: why}
}

// EdgeAccessible is the pseudo-domain the board edge and metal mounting
// holes stand for in an insulation pair.
const EdgeAccessible = "edge:accessible"

// EdgeDomainDistance is the edge distance an insulated domain needs: the
// insulation clearance and creepage (pkg/safety) between the domain and an
// accessible (touchable or earthed) surface — the board edge and metal
// mounting hardware — never less than the layer defaults. insulation ""
// = reinforced for a hazardous/mains domain, basic for a patient domain.
func EdgeDomainDistance(kind string, vrms, vpeak float64, st safety.Standard, insulation, transient string, mainsVrms float64) (*EdgeDomain, bool) {
	hazard := kind == "mains" || kind == "hazardous"
	if !hazard && kind != "patient" {
		return nil, false
	}
	ins := insulation
	if ins == "" {
		ins = "reinforced"
		if !hazard {
			ins = "basic"
		}
	}
	p := safety.Pair{A: "domain", B: EdgeAccessible, WorkingVrms: vrms, WorkingVpeak: vpeak, Insulation: ins, Transient: transient, MainsVrms: mainsVrms}
	if st.MOP != "" {
		p.MOP, p.MOPCount = st.MOP, 1
		if ins == "reinforced" || ins == "double" {
			p.MOPCount = 2
		}
	}
	if safety.NormStandard(st.Name) == safety.IPC2221B {
		// IPC-2221B spacing is functional; a touchable edge needs an
		// insulation standard. IEC 62368-1 is the default for hazardous
		// domains (as in intent derive).
		st.Name = safety.IEC62368
	}
	r := safety.Distances(p, st)
	d := &EdgeDomain{ClearanceMm: r.ClearanceMm, CreepageMm: r.CreepageMm, Insulation: r.Insulation}
	d.Mil = math.Ceil(math.Max(r.ClearanceMm, r.CreepageMm)/0.0254*10) / 10
	d.Why = append(d.Why, fmt.Sprintf("%s domain to the board edge / metal mounting holes (accessible or earthed surface): %s insulation — clearance %.2f mm, creepage %.2f mm (%s); the straight copper-to-edge distance is both the air gap and the surface path, so it must meet the larger",
		kind, r.Insulation, r.ClearanceMm, r.CreepageMm, r.Ref))
	d.Why = append(d.Why, safety.Caveat)
	return d, true
}

// EdgeFromIntent resolves the edge policy of a board: intent.json "edge"
// when present, else the defaults; hazardous domains of an intent without
// an "edge" field are computed here from the domains and the standard.
// The board rule (the live Board Outline spacing, b.Rules.EdgeClearance)
// and the fab floor are floors, never ceilings.
func EdgeFromIntent(in *Intent, b *Board) *EdgePolicy {
	var p *EdgePolicy
	if in != nil && in.Edge != nil {
		e := in.Edge
		p = DefaultEdgePolicy(e.EdgeKind)
		p.Source = "intent"
		if e.OuterMil > 0 {
			p.OuterMil = e.OuterMil
		}
		if e.InnerMil > 0 {
			p.InnerMil = e.InnerMil
		}
		if e.VcutMil > 0 {
			p.VcutMil = e.VcutMil
		}
		if len(e.Why) > 0 {
			p.Why = append([]string(nil), e.Why...)
		}
		for id, d := range e.ByDomain {
			if d == nil || d.Mil <= 0 {
				continue
			}
			if p.ByDomain == nil {
				p.ByDomain = map[string]*EdgeDomain{}
			}
			dd := *d
			p.ByDomain[id] = &dd
		}
	} else {
		p = DefaultEdgePolicy(EdgeRouted)
	}
	if in != nil {
		if p.ByDomain == nil && (in.Edge == nil || in.Edge.ByDomain == nil) {
			for _, d := range in.Domains {
				ed, ok := EdgeDomainDistance(d.Kind, d.WorkingVrms, d.WorkingVpeak, in.Standard, "", "", 0)
				if !ok {
					continue
				}
				if p.ByDomain == nil {
					p.ByDomain = map[string]*EdgeDomain{}
				}
				p.ByDomain[d.ID] = ed
			}
		}
		for _, d := range in.Domains {
			if p.ByDomain[d.ID] == nil {
				continue
			}
			for _, n := range d.Nets {
				if p.netDomain == nil {
					p.netDomain = map[string]string{}
				}
				p.netDomain[n] = d.ID
			}
		}
		// The per-net domain is authoritative (as for isolation, intent.go):
		// a net listed under two domains takes its own, and a net whose own
		// domain has no edge band (SELV) drops a band inherited from a
		// neighbour's list.
		known := map[string]bool{}
		for _, d := range in.Domains {
			known[d.ID] = true
		}
		for n, np := range in.Nets {
			if np == nil || !known[np.Domain] {
				continue
			}
			if p.ByDomain[np.Domain] != nil {
				if p.netDomain == nil {
					p.netDomain = map[string]string{}
				}
				p.netDomain[n] = np.Domain
			} else {
				delete(p.netDomain, n)
			}
		}
	}
	p.OuterMil = math.Max(p.OuterMil, p.FabMinMil)
	p.InnerMil = math.Max(p.InnerMil, p.FabMinMil)
	if b != nil && b.Rules.EdgeClearance > 0 {
		p.RuleMil = b.Rules.EdgeClearance
	}
	return p
}

// IsOuterLayer reports whether a copper layer id is an outer layer.
func IsOuterLayer(layer int) bool { return layer == LayerTop || layer == LayerBottom }

// LayerReq is the edge distance of a copper layer (outer or inner class).
// LayerMulti (through copper) takes the inner value on a multilayer board.
func (p *EdgePolicy) LayerReq(layer int) float64 {
	if p == nil {
		return EdgeOuterMil
	}
	v := p.InnerMil
	if IsOuterLayer(layer) {
		v = p.OuterMil
	}
	return math.Max(math.Max(v, p.FabMinMil), p.RuleMil)
}

// DomainOf returns the insulated domain of a net ("" = SELV / none).
func (p *EdgePolicy) DomainOf(net string) string {
	if p == nil {
		return ""
	}
	return p.netDomain[net]
}

// NetReq is the domain edge distance of a net (0 = the layer default).
func (p *EdgePolicy) NetReq(net string) float64 {
	if p == nil {
		return 0
	}
	if d := p.ByDomain[p.netDomain[net]]; d != nil {
		return d.Mil
	}
	return 0
}

// Req is the edge distance of copper of net on a layer.
func (p *EdgePolicy) Req(layer int, net string) float64 {
	return math.Max(p.LayerReq(layer), p.NetReq(net))
}

// MaxReq is the largest distance any copper on the board needs.
func (p *EdgePolicy) MaxReq() float64 {
	m := math.Max(p.LayerReq(LayerTop), p.LayerReq(LayerInner1))
	for _, d := range p.ByDomain {
		m = math.Max(m, d.Mil)
	}
	return m
}

// ---- geometry ------------------------------------------------------------------

// orientCCW returns poly counter-clockwise.
func orientCCW(poly []Point) []Point {
	if signedArea(poly) >= 0 {
		return poly
	}
	out := make([]Point, len(poly))
	for i := range poly {
		out[i] = poly[len(poly)-1-i]
	}
	return out
}

// cleanPoly drops repeated and collinear vertices.
func cleanPoly(poly []Point) []Point {
	var out []Point
	for _, p := range poly {
		if len(out) > 0 && out[len(out)-1].Dist(p) < 1e-6 {
			continue
		}
		out = append(out, p)
	}
	if len(out) > 1 && out[0].Dist(out[len(out)-1]) < 1e-6 {
		out = out[:len(out)-1]
	}
	for changed := true; changed && len(out) >= 3; {
		changed = false
		for i := 0; i < len(out) && len(out) >= 3; i++ {
			a, b, c := out[(i+len(out)-1)%len(out)], out[i], out[(i+1)%len(out)]
			if math.Abs((b.X-a.X)*(c.Y-a.Y)-(b.Y-a.Y)*(c.X-a.X)) < 1e-9*math.Max(1, a.Dist(c)) {
				out = append(out[:i], out[i+1:]...)
				changed = true
			}
		}
	}
	return out
}

// lineIntersect intersects the lines p+t·r and q+u·s.
func lineIntersect(p, r, q, s Point) (Point, bool) {
	den := r.X*s.Y - r.Y*s.X
	if math.Abs(den) < 1e-12 {
		return Point{}, false
	}
	t := ((q.X-p.X)*s.Y - (q.Y-p.Y)*s.X) / den
	return Point{p.X + t*r.X, p.Y + t*r.Y}, true
}

// InsetPolygon offsets a simple polygon inward by d (mil) with mitred
// corners: every point of the result is ≥ d from the input boundary
// (exactly d along straight edges; a reflex corner is cut deeper). Edges
// that collapse (a rounded corner tighter than d) are removed and the
// neighbours re-intersected. Returns nil when the polygon vanishes or the
// result fails the independent check (inside, ≥ d − 0.01 from the
// boundary, no self-intersection).
func InsetPolygon(poly []Point, d float64) []Point {
	poly = cleanPoly(orientCCW(append([]Point(nil), poly...)))
	if len(poly) < 3 {
		return nil
	}
	if d <= 0 {
		return poly
	}
	type line struct{ p, dir Point }
	var lines []line
	for i := range poly {
		a, b := poly[i], poly[(i+1)%len(poly)]
		dv := b.Sub(a)
		l := math.Hypot(dv.X, dv.Y)
		if l < 1e-9 {
			continue
		}
		u := dv.Scale(1 / l)
		n := Point{-u.Y, u.X} // inward normal of a CCW polygon
		lines = append(lines, line{a.Add(n.Scale(d)), u})
	}
	for iter := 0; iter < 4*len(poly)+8 && len(lines) >= 3; iter++ {
		m := len(lines)
		verts := make([]Point, m)
		ok := make([]bool, m)
		for i := 0; i < m; i++ {
			a, b := lines[(i+m-1)%m], lines[i]
			verts[i], ok[i] = lineIntersect(a.p, a.dir, b.p, b.dir)
		}
		drop := -1
		for i := 0; i < m; i++ {
			j := (i + 1) % m
			if !ok[i] || !ok[j] {
				// parallel neighbours (collinear after cleaning is rare):
				// drop the shorter-lived one
				drop = i
				break
			}
			seg := verts[j].Sub(verts[i])
			if seg.X*lines[i].dir.X+seg.Y*lines[i].dir.Y <= 1e-9 {
				drop = i
				break
			}
		}
		if drop < 0 {
			out := cleanPoly(verts)
			if !insetValid(poly, out, d) {
				return nil
			}
			return out
		}
		lines = append(lines[:drop], lines[drop+1:]...)
	}
	return nil
}

// insetValid checks an inset result independently of how it was made.
func insetValid(outer, in []Point, d float64) bool {
	if len(in) < 3 || signedArea(in) <= 0 {
		return false
	}
	for _, p := range in {
		if !PolyContains(outer, p) || PolyEdgeDist(outer, p) < d-0.01 {
			return false
		}
	}
	// outer vertices must not come closer than d to the inset edges
	for _, v := range outer {
		if PolyContains(in, v) || PolyEdgeDist(in, v) < d-0.01 {
			return false
		}
	}
	n := len(in)
	for i := 0; i < n; i++ {
		for j := i + 2; j < n; j++ {
			if i == 0 && j == n-1 {
				continue
			}
			if segsIntersect(in[i], in[(i+1)%n], in[j], in[(j+1)%n]) {
				return false
			}
		}
	}
	return true
}

// isConvex reports whether a polygon is convex.
func isConvex(poly []Point) bool {
	n := len(poly)
	if n < 3 {
		return false
	}
	sign := 0.0
	for i := 0; i < n; i++ {
		a, b, c := poly[i], poly[(i+1)%n], poly[(i+2)%n]
		cr := (b.X-a.X)*(c.Y-b.Y) - (b.Y-a.Y)*(c.X-b.X)
		if math.Abs(cr) < 1e-9 {
			continue
		}
		if sign == 0 {
			sign = math.Copysign(1, cr)
		} else if cr*sign < 0 {
			return false
		}
	}
	return true
}

// ClipConvex clips subject to a convex polygon (Sutherland–Hodgman).
// Returns nil when nothing is left or clip is not convex.
func ClipConvex(subject, clip []Point) []Point {
	clip = cleanPoly(orientCCW(append([]Point(nil), clip...)))
	if !isConvex(clip) {
		return nil
	}
	out := append([]Point(nil), subject...)
	for i := range clip {
		if len(out) == 0 {
			return nil
		}
		a, b := clip[i], clip[(i+1)%len(clip)]
		inside := func(p Point) bool { return (b.X-a.X)*(p.Y-a.Y)-(b.Y-a.Y)*(p.X-a.X) >= -1e-9 }
		in := out
		out = nil
		for j := range in {
			cur, prev := in[j], in[(j+len(in)-1)%len(in)]
			ci, pi := inside(cur), inside(prev)
			if ci != pi {
				if x, ok := lineIntersect(prev, cur.Sub(prev), a, b.Sub(a)); ok {
					out = append(out, x)
				}
			}
			if ci {
				out = append(out, cur)
			}
		}
	}
	out = cleanPoly(out)
	if len(out) < 3 {
		return nil
	}
	return out
}

// EdgeBand returns polygons covering the band within d of the outline
// (inside it): one rectangle per outline edge, plus a circumscribed disc at
// every reflex corner (there the nearest boundary point is the corner
// itself). Consecutive edges are merged into one quadrilateral chain while
// the union stays a simple polygon, so a rounded rectangle becomes 4
// regions. Used as "no inner electrical layer" regions: an EasyEDA
// negative plane is not a pour and ignores the pour polygon.
func EdgeBand(outline []Point, d float64) [][]Point {
	poly := cleanPoly(orientCCW(append([]Point(nil), outline...)))
	n := len(poly)
	if n < 3 || d <= 0 {
		return nil
	}
	normal := func(i int) Point {
		a, b := poly[i], poly[(i+1)%n]
		u := b.Sub(a)
		l := math.Hypot(u.X, u.Y)
		return Point{-u.Y / l, u.X / l}
	}
	reflex := func(i int) bool { // vertex i between edges i-1 and i
		a, b, c := poly[(i+n-1)%n], poly[i], poly[(i+1)%n]
		return (b.X-a.X)*(c.Y-b.Y)-(b.Y-a.Y)*(c.X-b.X) < -1e-9
	}
	// Start a chain at a vertex where the direction turns most (a real
	// corner), so chains break at corners, not in the middle of an arc.
	start := 0
	var out [][]Point
	chainPoly := func(i0, k int) []Point { // edges i0..i0+k-1
		var outerPts, innerPts []Point
		for e := 0; e <= k; e++ {
			v := (i0 + e) % n
			outerPts = append(outerPts, poly[v])
			var q Point
			switch {
			case e == 0:
				q = poly[v].Add(normal(v).Scale(d))
			case e == k:
				q = poly[v].Add(normal((v + n - 1) % n).Scale(d))
			default:
				na, nb := normal((v+n-1)%n), normal(v)
				bis := na.Add(nb)
				l := math.Hypot(bis.X, bis.Y)
				cosHalf := l / 2
				if l < 1e-9 || cosHalf < 0.2 {
					return nil
				}
				q = poly[v].Add(bis.Scale(d / (l * cosHalf)))
			}
			innerPts = append(innerPts, q)
		}
		res := append([]Point(nil), outerPts...)
		for i := len(innerPts) - 1; i >= 0; i-- {
			res = append(res, innerPts[i])
		}
		if !simplePoly(res) {
			return nil
		}
		return res
	}
	turn := func(v int) float64 {
		na, nb := normal((v+n-1)%n), normal(v)
		return math.Acos(math.Max(-1, math.Min(1, na.X*nb.X+na.Y*nb.Y)))
	}
	best := -1.0
	for v := 0; v < n; v++ {
		if t := turn(v); t > best {
			best, start = t, v
		}
	}
	for e := 0; e < n; {
		i0 := (start + e) % n
		k := 1
		acc := 0.0
		for e+k < n {
			v := (i0 + k) % n
			if reflex(v) || acc+turn(v) > math.Pi/2+1e-6 || chainPoly(i0, k+1) == nil {
				break
			}
			acc += turn(v)
			k++
		}
		if p := chainPoly(i0, k); p != nil {
			out = append(out, p)
		} else {
			a, b := poly[i0], poly[(i0+1)%n]
			nn := normal(i0).Scale(d)
			out = append(out, []Point{a, b, b.Add(nn), a.Add(nn)})
			k = 1
		}
		e += k
	}
	for v := 0; v < n; v++ {
		if reflex(v) {
			out = append(out, circlePoly(poly[v], d))
		}
	}
	return out
}

// simplePoly reports whether no two non-adjacent edges intersect.
func simplePoly(p []Point) bool {
	n := len(p)
	if n < 3 {
		return false
	}
	for i := 0; i < n; i++ {
		for j := i + 2; j < n; j++ {
			if i == 0 && j == n-1 {
				continue
			}
			if segsIntersect(p[i], p[(i+1)%n], p[j], p[(j+1)%n]) {
				return false
			}
		}
	}
	return true
}

// flattenArc returns the points of an EasyEDA ARC from a to b with the
// signed sweep (degrees, positive = counter-clockwise), start excluded,
// with a chord error ≤ maxErr.
func flattenArc(a, b Point, sweepDeg, maxErr float64) []Point {
	abs := math.Abs(sweepDeg)
	chord := a.Dist(b)
	if abs < 1e-6 || abs >= 360 || chord < 1e-9 {
		return []Point{b}
	}
	half := abs * math.Pi / 360
	r := chord / (2 * math.Sin(half))
	mid := Point{(a.X + b.X) / 2, (a.Y + b.Y) / 2}
	off := math.Sqrt(math.Max(0, r*r-chord*chord/4))
	u := b.Sub(a).Scale(1 / chord)
	nrm := Point{-u.Y, u.X}
	// The centre lies left of a→b for a CCW sweep under 180°, right for
	// a CW one; past 180° the side flips.
	side := 1.0
	if sweepDeg < 0 {
		side = -1
	}
	if abs > 180 {
		side = -side
	}
	c := mid.Add(nrm.Scale(side * off))
	a0 := math.Atan2(a.Y-c.Y, a.X-c.X)
	step := 2 * math.Acos(math.Max(-1, math.Min(1, 1-maxErr/math.Max(r, 1e-9))))
	k := int(math.Ceil(abs * math.Pi / 180 / math.Max(step, 1e-3)))
	k = max(1, min(k, 720))
	var out []Point
	for i := 1; i < k; i++ {
		t := a0 + sweepDeg*math.Pi/180*float64(i)/float64(k)
		out = append(out, Point{c.X + r*math.Cos(t), c.Y + r*math.Sin(t)})
	}
	return append(out, b)
}

// SourceContours reads an EasyEDA polygon / complex-polygon source
// ([x,y,"L",x,y,…,"ARC",sweep,x,y,…] or nested arrays of those) with arcs
// flattened (chord error ≤ 0.1 mil). Other shapes return nil (the caller
// reports them as unmeasured).
func SourceContours(v any) [][]Point {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil
	}
	if _, nested := arr[0].([]any); nested {
		var out [][]Point
		for _, it := range arr {
			out = append(out, SourceContours(it)...)
		}
		return out
	}
	if len(arr) < 2 {
		return nil
	}
	fx, ok1 := arr[0].(float64)
	fy, ok2 := arr[1].(float64)
	if !ok1 || !ok2 {
		return nil
	}
	pts := []Point{{fx, fy}}
	for i := 2; i < len(arr); {
		cmd, isCmd := arr[i].(string)
		if !isCmd {
			// implicit continuation of the previous L
			if i+1 < len(arr) {
				x, xok := arr[i].(float64)
				y, yok := arr[i+1].(float64)
				if xok && yok {
					pts = append(pts, Point{x, y})
				}
			}
			i += 2
			continue
		}
		switch strings.ToUpper(cmd) {
		case "L":
			i++
			for i+1 < len(arr) {
				x, xok := arr[i].(float64)
				y, yok := arr[i+1].(float64)
				if !xok || !yok {
					break
				}
				pts = append(pts, Point{x, y})
				i += 2
			}
		case "ARC", "CARC":
			if i+3 >= len(arr) {
				i = len(arr)
				break
			}
			sw, x, y := num(arr[i+1]), num(arr[i+2]), num(arr[i+3])
			pts = append(pts, flattenArc(pts[len(pts)-1], Point{x, y}, sw, 0.1)...)
			i += 4
		default:
			i++
		}
	}
	pts = cleanPoly(pts)
	if len(pts) < 3 {
		return nil
	}
	return [][]Point{pts}
}

// ---- the check -----------------------------------------------------------------

// EdgeLayer is the measured minimum copper-to-edge distance of one layer.
type EdgeLayer struct {
	Layer       int     `json:"layer"`
	Class       string  `json:"class"` // outer | inner
	RequiredMil float64 `json:"requiredMil"`
	// MinMil is the smallest measured distance (-1 = copper outside the
	// outline; nil-equivalent: Measured=false).
	MinMil   float64 `json:"minMil"`
	Measured bool    `json:"measured"`
	Item     string  `json:"item,omitempty"`
	Kind     string  `json:"kind,omitempty"`
	Net      string  `json:"net,omitempty"`
	At       Point   `json:"at"`
	Items    int     `json:"items"`
	Note     string  `json:"note,omitempty"`
}

// EdgeFinding is one violation (aggregated per layer, net and kind).
type EdgeFinding struct {
	Rule        string  `json:"rule"` // copper-to-edge | copper-to-hole | plane-pullback
	Level       string  `json:"level"`
	Layer       int     `json:"layer"`
	Net         string  `json:"net,omitempty"`
	Kind        string  `json:"kind"` // track | via | pad | pour | fill | plane
	Item        string  `json:"item,omitempty"`
	Count       int     `json:"count"`
	GapMil      float64 `json:"gapMil"`
	RequiredMil float64 `json:"requiredMil"`
	At          Point   `json:"at"`
	Message     string  `json:"message"`
}

// EdgeCheck is the board-edge safety distance check of a board.
type EdgeCheck struct {
	Policy   *EdgePolicy   `json:"policy"`
	Layers   []EdgeLayer   `json:"layers"`
	HoleMin  *EdgeLayer    `json:"holeMin,omitempty"`
	Findings []EdgeFinding `json:"findings"`
	Notes    []string      `json:"notes,omitempty"`
}

// Errors counts ERROR findings.
func (c *EdgeCheck) Errors() int {
	n := 0
	for _, f := range c.Findings {
		if f.Level == "ERROR" {
			n++
		}
	}
	return n
}

// edgeItem is one piece of copper.
type edgeItem struct {
	kind, net, id string
	layers        []int
	polys         [][]Point // filled copper (pour fills, pads)
	a, b          Point     // track centre line
	hw            float64   // track half width / via radius
	seg, circle   bool
	boundary      bool // a planned pour outline, not flooded copper
	part          string
}

// mountHole is a metal mounting hole: a drilled wall (radius r) and the
// screw head / washer the keep-out ring stands for (radius head).
type mountHole struct {
	c       Point
	r, head float64
	id      string
}

func outlineSegDist(outline []Point, a, b Point) float64 {
	d := math.Inf(1)
	n := len(outline)
	for k := 0; k < n; k++ {
		d = math.Min(d, SegSegDist(a, b, outline[k], outline[(k+1)%n]))
	}
	return d
}

// dist returns the item's distance to the outline (negative-ish 0 → -1 when
// copper lies outside it) and to a point set (holes).
func (it *edgeItem) edgeDist(outline []Point) (float64, Point) {
	switch {
	case it.circle:
		if !PolyContains(outline, it.a) {
			return -1, it.a
		}
		return PolyEdgeDist(outline, it.a) - it.hw, it.a
	case it.seg:
		if !PolyContains(outline, it.a) || !PolyContains(outline, it.b) {
			return -1, it.a
		}
		d := outlineSegDist(outline, it.a, it.b) - it.hw
		at := it.a
		if PolyEdgeDist(outline, it.b) < PolyEdgeDist(outline, it.a) {
			at = it.b
		}
		return d, at
	}
	best, at := math.Inf(1), Point{}
	for _, poly := range it.polys {
		for i := range poly {
			p := poly[i]
			if !PolyContains(outline, p) && PolyEdgeDist(outline, p) > 0.01 {
				return -1, p
			}
			if d := outlineSegDist(outline, p, poly[(i+1)%len(poly)]); d < best {
				best, at = d, p
			}
		}
	}
	return best, at
}

func (it *edgeItem) pointDist(c Point) float64 {
	switch {
	case it.circle:
		return c.Dist(it.a) - it.hw
	case it.seg:
		return PointSegDist(c, it.a, it.b) - it.hw
	}
	// Filled copper is a complex polygon: the first contours are islands,
	// nested ones are cut-outs (even-odd).
	best, in := math.Inf(1), false
	for _, poly := range it.polys {
		if PolyContains(poly, c) {
			in = !in
		}
		best = math.Min(best, PolyEdgeDist(poly, c))
	}
	if in {
		return -best
	}
	return best
}

func (it *edgeItem) bounds() Rect {
	r := EmptyRect()
	switch {
	case it.circle:
		return Rect{it.a.X, it.a.Y, it.a.X, it.a.Y}.Expand(it.hw)
	case it.seg:
		return r.AddPoint(it.a).AddPoint(it.b).Expand(it.hw)
	}
	for _, p := range it.polys {
		r = r.Union(PolyBounds(p))
	}
	return r
}

// CopperLayerIDs lists the copper layer ids of a board with n layers
// (TOP, IN1…, BOTTOM).
func CopperLayerIDs(n int) []int { return copperLayerIDs(n) }

// copperLayerIDs lists the copper layers of a board with n layers.
func copperLayerIDs(n int) []int {
	if n < 2 {
		n = 2
	}
	ids := []int{LayerTop}
	for i := 0; i < n-2; i++ {
		ids = append(ids, LayerInner1+i)
	}
	return append(ids, LayerBottom)
}

// checkEdge measures every item against the outline and the mounting holes.
func checkEdge(outline []Point, holes []mountHole, layers []int, items []edgeItem, pol *EdgePolicy, edgeParts map[string]bool) *EdgeCheck {
	chk := &EdgeCheck{Policy: pol}
	if len(outline) < 3 {
		chk.Notes = append(chk.Notes, "no board outline: copper-to-edge not measured")
		return chk
	}
	type key struct {
		rule, net, kind string
		layer           int
	}
	agg := map[key]*EdgeFinding{}
	var order []key
	note := func(k key, gap, req float64, at Point, id, level, msg string) {
		f := agg[k]
		if f == nil {
			f = &EdgeFinding{Rule: k.rule, Level: level, Layer: k.layer, Net: k.net, Kind: k.kind, GapMil: math.Inf(1), RequiredMil: req}
			agg[k] = f
			order = append(order, k)
		}
		f.Count++
		if level == "ERROR" {
			f.Level = "ERROR"
		}
		f.RequiredMil = math.Max(f.RequiredMil, req)
		if gap < f.GapMil {
			f.GapMil, f.At, f.Item, f.Message = round2(gap), roundPt(at), id, msg
		}
	}
	lm := map[int]*EdgeLayer{}
	for _, l := range layers {
		cls := "inner"
		if IsOuterLayer(l) {
			cls = "outer"
		}
		lm[l] = &EdgeLayer{Layer: l, Class: cls, RequiredMil: pol.LayerReq(l), MinMil: math.Inf(1)}
	}
	var hmin *EdgeLayer
	for i := range items {
		it := &items[i]
		d, at := it.edgeDist(outline)
		for _, l := range it.layers {
			m := lm[l]
			if m == nil {
				continue
			}
			m.Items++
			if d < m.MinMil {
				m.MinMil, m.At, m.Item, m.Kind, m.Net, m.Measured = d, roundPt(at), it.id, it.kind, it.net, true
			}
			req := pol.Req(l, it.net)
			if d < req-0.05 {
				level := "ERROR"
				why := ""
				if it.kind == "pad" && edgeParts[it.part] && pol.NetReq(it.net) == 0 && d >= pol.FabMinMil {
					// An edge-mounted part (connector at / over the outline):
					// its footprint's own edge rule decides; still reported.
					level = "WARN"
					why = " (edge-mounted part " + it.part + ": confirm its footprint allows it)"
				}
				gapTxt := sprintf("%.1f mil", d)
				if d < 0 {
					gapTxt = "outside the outline"
				}
				dom := ""
				if dd := pol.DomainOf(it.net); dd != "" && pol.NetReq(it.net) >= req {
					dom = sprintf(" — %s domain %s insulation to an accessible edge", dd, pol.ByDomain[dd].Insulation)
				}
				note(key{"copper-to-edge", it.net, it.kind, l}, d, req, at, it.id, level,
					sprintf("layer %d %s %s of net %s is %s from the board edge; needs ≥ %.1f mil (%s)%s%s", l, it.kind, it.id, orDash(it.net), gapTxt, req, pol.classWord(l), dom, why))
			}
		}
		for _, h := range holes {
			if it.kind == "plane" || it.kind == "pour-boundary" || (it.kind == "pour" && it.boundary) {
				// A pour / plane outline is not copper: the host floods it
				// around the hole keep-out ring and the hole rule.
				continue
			}
			bb := it.bounds()
			reach := math.Max(pol.MaxReq(), h.head-h.r+pol.NetReq(it.net)) + h.r + 1
			if !bb.Expand(reach).Contains(h.c) {
				continue
			}
			wall := it.pointDist(h.c) - h.r
			if hmin == nil || wall < hmin.MinMil {
				if hmin == nil {
					hmin = &EdgeLayer{Class: "hole", MinMil: math.Inf(1)}
				}
				hmin.MinMil, hmin.At, hmin.Item, hmin.Kind, hmin.Net, hmin.Measured = wall, roundPt(h.c), it.id, it.kind, it.net, true
			}
			for _, l := range it.layers {
				req := pol.LayerReq(l)
				gap := wall
				what := "hole wall"
				if nr := pol.NetReq(it.net); nr > 0 && h.head > h.r {
					// Hazardous copper: the screw head / washer is the accessible surface.
					if g := it.pointDist(h.c) - h.head; g < nr {
						gap, req, what = g, nr, "screw head (keep-out ring)"
					}
				} else if nr > req {
					req = nr
				}
				if gap < req-0.05 {
					note(key{"copper-to-hole", it.net, it.kind, l}, gap, req, h.c, it.id, "ERROR",
						sprintf("layer %d %s %s of net %s is %.1f mil from mounting hole %s (%s); needs ≥ %.1f mil", l, it.kind, it.id, orDash(it.net), gap, h.id, what, req))
				}
			}
		}
	}
	for _, l := range layers {
		m := lm[l]
		if !m.Measured {
			m.MinMil = 0
		} else {
			m.MinMil = round2(m.MinMil)
		}
		chk.Layers = append(chk.Layers, *m)
	}
	if hmin != nil {
		hmin.MinMil = round2(hmin.MinMil)
		chk.HoleMin = hmin
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.layer != b.layer {
			return a.layer < b.layer
		}
		if a.rule != b.rule {
			return a.rule < b.rule
		}
		if a.net != b.net {
			return a.net < b.net
		}
		return a.kind < b.kind
	})
	for _, k := range order {
		f := agg[k]
		if f.Count > 1 {
			f.Message += sprintf(" [%d item(s)]", f.Count)
		}
		chk.Findings = append(chk.Findings, *f)
	}
	return chk
}

func (p *EdgePolicy) classWord(layer int) string {
	if IsOuterLayer(layer) {
		return sprintf("outer copper, %s edge", p.Kind)
	}
	return sprintf("inner copper / plane pull-back, %s edge", p.Kind)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// planeRuleFinding reports an inner layer the dump carries no copper for:
// an EasyEDA negative plane (内电层) is drawn by the host up to the Board
// Outline ↔ Copper/Plane Zone rule, so that rule IS its pull-back.
func planeRuleFinding(chk *EdgeCheck, layer int, pol *EdgePolicy, bandMil float64) {
	req := pol.LayerReq(layer)
	for i := range chk.Layers {
		if chk.Layers[i].Layer != layer {
			continue
		}
		pull := math.Max(pol.RuleMil, bandMil)
		chk.Layers[i].Note = sprintf("no copper primitives read on this inner layer: treated as a negative plane pulled back by the host rule %.1f mil (Board Outline ↔ Copper/Plane Zone)", pol.RuleMil)
		if bandMil > 0 {
			chk.Layers[i].Note += sprintf(" and a %.1f mil no-inner-electrical edge band", bandMil)
		}
		if pull <= 0 {
			chk.Notes = append(chk.Notes, sprintf("layer %d: plane pull-back unknown (no live rule in the dump) — run `pcb rules apply --intent` and re-check", layer))
			return
		}
		if !chk.Layers[i].Measured || pull < chk.Layers[i].MinMil {
			chk.Layers[i].MinMil, chk.Layers[i].Kind, chk.Layers[i].Item, chk.Layers[i].Net = round2(pull), "plane", "", ""
		}
		chk.Layers[i].Measured = true
		if pull < req-0.05 {
			chk.Findings = append(chk.Findings, EdgeFinding{Rule: "plane-pullback", Level: "ERROR", Layer: layer, Kind: "plane", Count: 1,
				GapMil: round2(pull), RequiredMil: req,
				Message: sprintf("layer %d negative plane: the host pulls it back only %.1f mil from the edge (Board Outline ↔ Copper/Plane Zone rule); needs ≥ %.1f mil — `pcb rules apply --intent` raises the rule, pcb auto adds a no-inner-electrical edge band", layer, pull, req)})
		}
	}
}

// ---- snapshot check (pcb check) -----------------------------------------------

// CheckEdgeSnapshot checks a `pcb dump --include-copper` document: tracks,
// arcs, vias, pads, materialized poured copper (exact contours, arcs
// flattened), net fills, and inner layers without copper as negative
// planes, against the outline and metal mounting holes. in may be nil
// (defaults); kind overrides the edge kind ("" = intent / routed).
func CheckEdgeSnapshot(raw []byte, in *Intent, kind string) (*EdgeCheck, error) {
	b, err := FromSnapshot(raw)
	if err != nil {
		return nil, err
	}
	var s struct {
		Components []struct {
			Designator string    `json:"designator"`
			BBox       *snapBBox `json:"bbox"`
		} `json:"components"`
		Copper      *snapCopperLite `json:"copper"`
		PlaneLayers []int           `json:"planeLayers"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	pol := EdgeFromIntent(in, b)
	if kind != "" {
		k, kerr := NormEdgeKind(kind)
		if kerr != nil {
			return nil, kerr
		}
		if k != pol.Kind {
			o, i, v, f, why := EdgeDefaults(k)
			pol.Kind, pol.OuterMil, pol.InnerMil, pol.VcutMil, pol.FabMinMil = k, math.Max(pol.OuterMil, o), math.Max(pol.InnerMil, i), v, f
			pol.Why = append(pol.Why, why...)
		}
	}
	layers := copperLayerIDs(b.CopperLayers)
	var items []edgeItem
	through := layers
	add := func(it edgeItem) {
		if it.layers == nil {
			return
		}
		items = append(items, it)
	}
	onLayer := func(l int) []int {
		if l == LayerMulti {
			return through
		}
		for _, x := range layers {
			if x == l {
				return []int{l}
			}
		}
		return nil
	}
	chk0 := &EdgeCheck{}
	perLayer := map[int]int{}
	if s.Copper == nil {
		chk0.Notes = append(chk0.Notes, "snapshot has no copper section (pcb dump --include-copper): pads only")
	} else {
		for _, l := range s.Copper.Lines {
			ly := int(num(l["layer"]))
			it := edgeItem{kind: "track", net: str(l["net"]), id: str(l["primitiveId"]), layers: onLayer(ly), seg: true,
				a: Point{num(l["startX"]), num(l["startY"])}, b: Point{num(l["endX"]), num(l["endY"])}, hw: num(l["lineWidth"]) / 2}
			if it.net == "" {
				continue // net-less lines: outline / mechanical
			}
			add(it)
			perLayer[ly]++
		}
		for _, a := range s.Copper.Arcs {
			if str(a["net"]) == "" {
				continue
			}
			ly := int(num(a["layer"]))
			start, end := Point{num(a["startX"]), num(a["startY"])}, Point{num(a["endX"]), num(a["endY"])}
			pts := append([]Point{start}, flattenArc(start, end, num(a["arcAngle"]), 0.1)...)
			for i := 0; i+1 < len(pts); i++ {
				add(edgeItem{kind: "track", net: str(a["net"]), id: str(a["primitiveId"]), layers: onLayer(ly), seg: true, a: pts[i], b: pts[i+1], hw: num(a["lineWidth"]) / 2})
			}
			perLayer[ly]++
		}
		for _, v := range s.Copper.Vias {
			add(edgeItem{kind: "via", net: str(v["net"]), id: str(v["primitiveId"]), layers: through, circle: true,
				a: Point{num(v["x"]), num(v["y"])}, hw: num(v["diameter"]) / 2})
		}
		pouredOf := map[string]bool{}
		for _, p := range s.Copper.Poured {
			ly := int(num(p["layer"]))
			id := str(p["pourPrimitiveId"])
			if id == "" {
				id = str(p["primitiveId"])
			}
			pouredOf[id] = true
			fills, _ := p["fills"].([]any)
			for j, f := range fills {
				fm, ok := f.(map[string]any)
				if !ok {
					continue
				}
				cs := SourceContours(fm["source"])
				fid := str(fm["id"])
				if len(cs) == 0 {
					chk0.Notes = append(chk0.Notes, sprintf("poured copper %s fill %s (layer %d): geometry unsupported/unreadable — NOT measured", id, fid, ly))
					continue
				}
				if fid == "" {
					fid = sprintf("%s:%d", id, j)
				}
				add(edgeItem{kind: "pour", net: str(p["net"]), id: fid, layers: onLayer(ly), polys: cs})
				perLayer[ly]++
			}
		}
		for _, p := range s.Copper.Pours {
			id := str(p["primitiveId"])
			if pouredOf[id] {
				continue
			}
			// Not materialized: the pour boundary bounds the copper it will flood.
			cs := SourceContours(p["source"])
			if len(cs) == 0 {
				continue
			}
			ly := int(num(p["layer"]))
			add(edgeItem{kind: "pour-boundary", net: str(p["net"]), id: id, layers: onLayer(ly), polys: cs})
			perLayer[ly]++
			chk0.Notes = append(chk0.Notes, sprintf("pour %s (layer %d) has no materialized copper in the dump: its boundary was measured (the flooded copper stays inside it)", id, ly))
		}
		for _, f := range s.Copper.Fills {
			ly := int(num(f["layer"]))
			if ly == LayerMulti || str(f["net"]) == "" {
				continue // MULTI fills are cutouts / holes; net-less fills on copper are mechanical
			}
			cs := SourceContours(f["source"])
			if len(cs) == 0 {
				chk0.Notes = append(chk0.Notes, sprintf("fill %s (layer %d): geometry unsupported/unreadable — NOT measured", str(f["primitiveId"]), ly))
				continue
			}
			add(edgeItem{kind: "fill", net: str(f["net"]), id: str(f["primitiveId"]), layers: onLayer(ly), polys: cs})
			perLayer[ly]++
		}
	}
	edgeParts := map[string]bool{}
	for _, c := range s.Components {
		if c.BBox == nil || len(b.Outline) < 3 {
			continue
		}
		bb := Rect{c.BBox.MinX, c.BBox.MinY, c.BBox.MaxX, c.BBox.MaxY}
		for _, q := range bb.Corners() {
			if !PolyContains(b.Outline, q) || PolyEdgeDist(b.Outline, q) < 1 {
				edgeParts[c.Designator] = true
				break
			}
		}
	}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			var ls []int
			if pd.Layer == LayerMulti {
				ls = through
			} else {
				ls = onLayer(pd.Layer)
			}
			add(edgeItem{kind: "pad", net: pd.Net, id: pd.Key(), part: p.Ref, layers: ls, polys: [][]Point{padPoly(pd)}})
		}
	}
	holes := snapshotMountHoles(s.Copper)
	chk := checkEdge(b.Outline, holes, layers, items, pol, edgeParts)
	chk.Notes = append(chk0.Notes, chk.Notes...)
	planeSet := map[int]bool{}
	for _, l := range s.PlaneLayers {
		planeSet[l] = true
	}
	for _, l := range layers {
		if IsOuterLayer(l) {
			continue
		}
		if planeSet[l] || (len(s.PlaneLayers) == 0 && perLayer[l] == 0 && s.Copper != nil) {
			planeRuleFinding(chk, l, pol, snapshotBandMil(s.Copper, b.Outline))
		}
	}
	if len(holes) > 0 {
		chk.Notes = append(chk.Notes, sprintf("%d metal mounting hole(s) measured (copper to the drilled wall ≥ the layer edge distance; hazardous copper to the screw-head keep-out ring ≥ its domain distance)", len(holes)))
	}
	return chk, nil
}

type snapCopperLite struct {
	Lines   []map[string]any `json:"lines"`
	Arcs    []map[string]any `json:"arcs"`
	Vias    []map[string]any `json:"vias"`
	Pours   []map[string]any `json:"pours"`
	Poured  []map[string]any `json:"poured"`
	Fills   []map[string]any `json:"fills"`
	Regions []map[string]any `json:"regions"`
}

// snapshotMountHoles returns the round MULTI-layer cutouts (mounting holes;
// milled isolation slots are not round and follow the Slot Region rule),
// with the screw-head radius from a concentric round keep-out region.
func snapshotMountHoles(c *snapCopperLite) []mountHole {
	if c == nil {
		return nil
	}
	var out []mountHole
	for _, f := range c.Fills {
		if int(num(f["layer"])) != LayerMulti {
			continue
		}
		cs := SourceContours(f["source"])
		var poly []Point
		if len(cs) > 0 {
			poly = cs[0]
		} else if bb, ok := anyBBox(f["bbox"]); ok {
			poly = bb.Corners()
		}
		if len(poly) < 3 {
			continue
		}
		bb := PolyBounds(poly)
		r := math.Min(bb.W(), bb.H()) / 2
		if r <= 0 || math.Abs(bb.W()-bb.H()) > 0.05*math.Max(bb.W(), bb.H()) || PolyArea(poly) < 0.85*math.Pi*r*r {
			continue
		}
		h := mountHole{c: bb.Center(), r: r, head: r, id: str(f["primitiveId"])}
		for _, rg := range c.Regions {
			rc := SourceContours(rg["source"])
			if len(rc) == 0 {
				continue
			}
			rb := PolyBounds(rc[0])
			rr := math.Min(rb.W(), rb.H()) / 2
			if rb.Center().Dist(h.c) < 1 && rr > h.head && math.Abs(rb.W()-rb.H()) < 0.05*rb.W() {
				h.head = rr
			}
		}
		out = append(out, h)
	}
	return out
}

// snapshotBandMil is the width of an existing no-inner-electrical edge band
// (regions with rule 8 touching the outline), 0 when there is none.
func snapshotBandMil(c *snapCopperLite, outline []Point) float64 {
	if c == nil || len(outline) < 3 {
		return 0
	}
	band := math.Inf(1)
	found := false
	for _, rg := range c.Regions {
		rules, _ := rg["ruleType"].([]any)
		has := false
		for _, v := range rules {
			if int(num(v)) == 8 {
				has = true
			}
		}
		if !has {
			continue
		}
		cs := SourceContours(rg["source"])
		if len(cs) == 0 {
			continue
		}
		// depth of the region = farthest vertex from the outline
		depth := 0.0
		touches := false
		for _, p := range cs[0] {
			d := PolyEdgeDist(outline, p)
			depth = math.Max(depth, d)
			if d < 1 {
				touches = true
			}
		}
		if touches {
			found = true
			band = math.Min(band, depth)
		}
	}
	if !found {
		return 0
	}
	return round2(band)
}

// ---- the engine (pcb auto) -------------------------------------------------------

// planEdgeCheck measures the planned copper (routed tracks and vias, pads,
// plane/pour regions as drawn) of a pcb auto result.
func planEdgeCheck(b *Board, an *Analysis, st *Stackup, rr *RouteResult) *EdgeCheck {
	pol := an.edgePolicy(b)
	var layers []int
	planeLayer := map[int]bool{}
	if st != nil {
		for _, l := range st.Stack {
			layers = append(layers, l.ID)
			if l.Kind == KindPlane {
				planeLayer[l.ID] = true
			}
		}
	}
	if len(layers) == 0 {
		layers = copperLayerIDs(b.CopperLayers)
	}
	var items []edgeItem
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			var ls []int
			for _, l := range layers {
				if pd.OnLayer(l) {
					ls = append(ls, l)
				}
			}
			items = append(items, edgeItem{kind: "pad", net: pd.Net, id: pd.Key(), part: p.Ref, layers: ls, polys: [][]Point{padPoly(pd)}})
		}
	}
	if rr != nil {
		for i, t := range rr.Tracks {
			items = append(items, edgeItem{kind: "track", net: t.Net, id: sprintf("track-%d", i+1), layers: []int{t.Layer}, seg: true, a: t.A, b: t.B, hw: t.Width / 2})
		}
		for i, v := range rr.Vias {
			items = append(items, edgeItem{kind: "via", net: v.Net, id: sprintf("via-%d", i+1), layers: layers, circle: true, a: v.C, hw: v.Dia / 2})
		}
		for _, pr := range rr.Planes {
			kind := "pour"
			if planeLayer[pr.Layer] {
				kind = "plane"
			}
			items = append(items, edgeItem{kind: kind, net: pr.Net, id: sprintf("%s@L%d", pr.Net, pr.Layer), layers: []int{pr.Layer}, polys: pr.Polys, boundary: true})
		}
	}
	edgeParts := map[string]bool{}
	for _, p := range b.Parts {
		for _, q := range p.Body().Corners() {
			if len(b.Outline) >= 3 && (!PolyContains(b.Outline, q) || PolyEdgeDist(b.Outline, q) < 1) {
				edgeParts[p.Ref] = true
				break
			}
		}
	}
	var holes []mountHole
	for _, h := range b.Holes {
		if h.Owner == "" && len(h.Poly) < 3 && h.Dia > 0 {
			holes = append(holes, mountHole{c: h.C, r: h.Dia / 2, head: h.Dia/2 + h.Keep, id: h.Name})
		}
	}
	chk := checkEdge(b.Outline, holes, layers, items, pol, edgeParts)
	if len(planeLayer) > 0 {
		chk.Notes = append(chk.Notes, sprintf("negative plane layers are pulled back by a %.1f mil no-inner-electrical edge band (playbook) and the Board Outline rule (`pcb rules apply --intent`)", PlaneBandMil(pol, st)))
	}
	return chk
}

// PlaneBandMil is the width of the no-inner-electrical edge band the
// playbook draws for the negative planes of a stackup (0 = none).
func PlaneBandMil(pol *EdgePolicy, st *Stackup) float64 {
	if st == nil {
		return 0
	}
	w := 0.0
	for _, l := range st.Stack {
		if l.Kind != KindPlane {
			continue
		}
		w = math.Max(w, pol.LayerReq(l.ID))
		for _, n := range l.Nets {
			w = math.Max(w, pol.NetReq(n))
		}
	}
	return w
}

// edgePolicy returns the analysis' edge policy (defaults when unset).
func (an *Analysis) edgePolicy(b *Board) *EdgePolicy {
	if an != nil && an.Edge != nil {
		return an.Edge
	}
	return EdgeFromIntent(nil, b)
}

// edgeCorner reports whether every corner of a g×g cell centred at c lies
// inside the outline at least req from it.
func edgeCellOK(outline []Point, c Point, half, req float64) bool {
	for _, q := range [4]Point{{c.X - half, c.Y - half}, {c.X + half, c.Y - half}, {c.X + half, c.Y + half}, {c.X - half, c.Y + half}} {
		if !PolyContains(outline, q) || PolyEdgeDist(outline, q) < req {
			return false
		}
	}
	return true
}

// insetRegion returns the pour polygon of a whole-layer region: the
// outline inset by req (nil when the inset fails — the caller rasterises).
func insetRegion(outline []Point, req float64) []Point {
	return InsetPolygon(outline, req)
}

// enforcePlaneEdge is the last guard on plane/pour regions: every polygon
// must keep its layer/net edge distance. A polygon that does not is
// re-rasterised (cells whose four corners keep the distance, merged into
// row rectangles). Returns notes.
func enforcePlaneEdge(b *Board, pol *EdgePolicy, planes []PlaneRegion) []string {
	outline := b.Outline
	if len(outline) < 3 {
		return nil
	}
	var notes []string
	for i := range planes {
		pr := &planes[i]
		req := pol.Req(pr.Layer, pr.Net)
		bad := false
		for _, poly := range pr.Polys {
			if polyEdgeGap(outline, poly) < req-0.01 {
				bad = true
				break
			}
		}
		if !bad {
			continue
		}
		pr.Polys = rasterClip(outline, pr.Polys, req, 5)
		notes = append(notes, sprintf("%s on layer %d re-cut to keep %.1f mil from the board edge (%d region(s))", pr.Net, pr.Layer, req, len(pr.Polys)))
	}
	return notes
}

// polyEdgeGap is the distance of a polygon (inside the outline) to the
// outline; -1 when a vertex lies outside.
func polyEdgeGap(outline, poly []Point) float64 {
	best := math.Inf(1)
	for i, p := range poly {
		if !PolyContains(outline, p) {
			return -1
		}
		best = math.Min(best, outlineSegDist(outline, p, poly[(i+1)%len(poly)]))
	}
	for _, v := range outline {
		if PolyContains(poly, v) {
			return -1
		}
	}
	return best
}

// rasterClip keeps the cells (size g) inside polys whose corners keep req
// from the outline, merged into row rectangles.
func rasterClip(outline []Point, polys [][]Point, req, g float64) [][]Point {
	bb := EmptyRect()
	for _, p := range polys {
		bb = bb.Union(PolyBounds(p))
	}
	if bb.Empty() {
		return nil
	}
	W, H := int(math.Ceil(bb.W()/g))+1, int(math.Ceil(bb.H()/g))+1
	keep := func(x, y int) bool {
		c := Point{bb.MinX + (float64(x)+0.5)*g, bb.MinY + (float64(y)+0.5)*g}
		in := false
		for _, p := range polys {
			if PolyContains(p, c) {
				in = true
				break
			}
		}
		return in && edgeCellOK(outline, c, g/2, req)
	}
	type run struct{ x0, x1, y0, y1 int }
	var rects []run
	open := map[[2]int]int{}
	for y := 0; y < H; y++ {
		next := map[[2]int]int{}
		for x := 0; x < W; {
			if !keep(x, y) {
				x++
				continue
			}
			x0 := x
			for x < W && keep(x, y) {
				x++
			}
			k := [2]int{x0, x - 1}
			if j, ok := open[k]; ok {
				rects[j].y1 = y
				next[k] = j
			} else {
				rects = append(rects, run{x0, x - 1, y, y})
				next[k] = len(rects) - 1
			}
		}
		open = next
	}
	var out [][]Point
	for _, r := range rects {
		lo := Point{bb.MinX + float64(r.x0)*g, bb.MinY + float64(r.y0)*g}
		hi := Point{bb.MinX + float64(r.x1+1)*g, bb.MinY + float64(r.y1+1)*g}
		out = append(out, Rect{lo.X, lo.Y, hi.X, hi.Y}.Corners())
	}
	return out
}
