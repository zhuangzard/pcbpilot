package app

import (
	"fmt"
	"github.com/zhuangzard/pcbpilot/internal/schguard"
	"math"
	"sort"
)

func libPinSide(p powerLayoutPin, b layoutBBox) (string, error) {
	if p.Rotation != nil {
		x, y, ok := schguard.CardinalOutward(*p.Rotation)
		if !ok {
			return "", fmt.Errorf("pin %s has invalid measured outward rotation", p.Number)
		}
		switch {
		case x > 0:
			return "right", nil
		case x < 0:
			return "left", nil
		case y > 0:
			return "up", nil
		default:
			return "down", nil
		}
	}
	var found []string
	for _, d := range []string{"left", "right", "up", "down"} {
		if schTerminalPointsOutward(p, b, d) {
			found = append(found, d)
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("pin %s has ambiguous/unknown measured outward side", p.Number)
	}
	return found[0], nil
}

func libPinOutwardRotation(p powerLayoutPin, b layoutBBox) (float64, error) {
	side, err := libPinSide(p, b)
	if err != nil {
		return 0, err
	}
	return map[string]float64{"right": 0, "up": 90, "left": 180, "down": 270}[side], nil
}

func libRouteRotation(q powerLayoutPin, plans []*powerLayoutPlan) (float64, bool) {
	if q.Rotation != nil {
		_, _, ok := schguard.CardinalOutward(*q.Rotation)
		return *q.Rotation, ok
	}
	for _, p := range plans {
		if p == nil {
			continue
		}
		for _, c := range p.Placements {
			for _, pin := range c.Pins {
				if pin.Number == q.Number && pin.X == q.X && pin.Y == q.Y {
					r, e := libPinOutwardRotation(pin, c.BBox)
					return r, e == nil
				}
			}
		}
	}
	return 0, false
}

func libRouteOutward(route []powerLayoutWire, a, b powerLayoutPin, ar, br float64, ak, bk bool) bool {
	for _, w := range route {
		for i := 1; i < len(w.Points); i++ {
			for _, q := range []struct {
				p     powerLayoutPin
				r     float64
				known bool
			}{{a, ar, ak}, {b, br, bk}} {
				if !q.known || !plOnSegment([2]float64{q.p.X, q.p.Y}, w.Points[i-1], w.Points[i]) {
					continue
				}
				for _, e := range [][2]float64{w.Points[i-1], w.Points[i]} {
					if e == [2]float64{q.p.X, q.p.Y} {
						continue
					}
					if !schguard.PinRayOutward(q.r, schguard.Point{X: q.p.X, Y: q.p.Y}, schguard.Point{X: e[0], Y: e[1]}) {
						return false
					}
				}
			}
		}
	}
	return true
}

func libPointsRoute(net string, points ...[2]float64) []powerLayoutWire {
	points = plNormalizeWirePoints(points)
	var route []powerLayoutWire
	for i := 1; i < len(points); i++ {
		if points[i-1] != points[i] {
			route = append(route, powerLayoutWire{Net: net, Points: [][2]float64{points[i-1], points[i]}})
		}
	}
	return route
}

// Both endpoints first escape along their OWN outward axes; only the free
// corridor bends. This admits same-facing and perpendicular pins without using
// illegal sideways stubs. Fixed bounded lengths keep candidate work finite.
func libEscapeRoutes(a, b powerLayoutPin, ar, br float64, steps []float64) [][]powerLayoutWire {
	ax, ay, _ := schguard.CardinalOutward(ar)
	bx, by, _ := schguard.CardinalOutward(br)
	s, t := [2]float64{a.X, a.Y}, [2]float64{b.X, b.Y}
	var routes [][]powerLayoutWire
	for _, d := range steps {
		x, y := [2]float64{a.X + ax*d, a.Y + ay*d}, [2]float64{b.X + bx*d, b.Y + by*d}
		for _, bend := range [][2]float64{{x[0], y[1]}, {y[0], x[1]}} {
			r := libPointsRoute(a.Net, s, x, bend, y, t)
			if libRouteOutward(r, a, b, ar, br, true, true) {
				routes = append(routes, r)
			}
		}
		// Oppositely facing-away pins need a free middle corridor; either
		// one-bend middle route otherwise passes through the OTHER pin again.
		for _, cy := range []float64{plFloor((a.Y + b.Y) / 2), math.Max(a.Y, b.Y) + d, math.Min(a.Y, b.Y) - d} {
			r := libPointsRoute(a.Net, s, x, [2]float64{x[0], cy}, [2]float64{y[0], cy}, y, t)
			if libRouteOutward(r, a, b, ar, br, true, true) {
				routes = append(routes, r)
			}
		}
		for _, cx := range []float64{plFloor((a.X + b.X) / 2), math.Max(a.X, b.X) + d, math.Min(a.X, b.X) - d} {
			r := libPointsRoute(a.Net, s, x, [2]float64{cx, x[1]}, [2]float64{cx, y[1]}, y, t)
			if libRouteOutward(r, a, b, ar, br, true, true) {
				routes = append(routes, r)
			}
		}
	}
	return routes
}
func libPin(c powerLayoutPlacement, n string) (powerLayoutPin, bool) {
	for _, p := range c.Pins {
		if p.Number == n {
			return p, true
		}
	}
	return powerLayoutPin{}, false
}

// Bounded route alternatives: straight, then the two one-bend Manhattan paths.
func libRoutes(a, b powerLayoutPin, plans ...*powerLayoutPlan) [][]powerLayoutWire {
	if a.Net == "" || a.Net != b.Net {
		return nil
	}
	if a.X == b.X && a.Y == b.Y {
		return [][]powerLayoutWire{{}}
	}
	wire := func(x, y [2]float64) powerLayoutWire { return powerLayoutWire{Net: a.Net, Points: [][2]float64{x, y}} }
	s, t := [2]float64{a.X, a.Y}, [2]float64{b.X, b.Y}
	var routes [][]powerLayoutWire
	if a.X == b.X || a.Y == b.Y {
		routes = append(routes, []powerLayoutWire{wire(s, t)})
	} else {
		m1, m2 := [2]float64{b.X, a.Y}, [2]float64{a.X, b.Y}
		routes = append(routes, []powerLayoutWire{wire(s, m1), wire(m1, t)}, []powerLayoutWire{wire(s, m2), wire(m2, t)})
	}
	ar, ak := libRouteRotation(a, plans)
	br, bk := libRouteRotation(b, plans)
	var valid [][]powerLayoutWire
	for _, r := range routes {
		if libRouteOutward(r, a, b, ar, br, ak, bk) {
			valid = append(valid, r)
		}
	}
	if ak && bk {
		valid = append(valid, libEscapeRoutes(a, b, ar, br, []float64{5, 10, 20})...)
	}
	return valid
}

// Bounded two-bend alternatives; the full geometry validator rejects inward paths.
func libDetourRoutes(a, b powerLayoutPin, plans ...*powerLayoutPlan) [][]powerLayoutWire {
	if a.Net == "" || a.Net != b.Net || (a.X == b.X && a.Y == b.Y) {
		return nil
	}
	var routes [][]powerLayoutWire
	add := func(points ...[2]float64) {
		var route []powerLayoutWire
		for i := 1; i < len(points); i++ {
			if points[i-1] != points[i] {
				route = append(route, powerLayoutWire{Net: a.Net, Points: [][2]float64{points[i-1], points[i]}})
			}
		}
		routes = append(routes, route)
	}
	s, t := [2]float64{a.X, a.Y}, [2]float64{b.X, b.Y}
	for d := 5.0; d <= 80; d += 5 {
		for _, x := range []float64{math.Min(a.X, b.X) - d, math.Max(a.X, b.X) + d} {
			add(s, [2]float64{x, a.Y}, [2]float64{x, b.Y}, t)
		}
		for _, y := range []float64{math.Min(a.Y, b.Y) - d, math.Max(a.Y, b.Y) + d} {
			add(s, [2]float64{a.X, y}, [2]float64{b.X, y}, t)
		}
	}
	ar, ak := libRouteRotation(a, plans)
	br, bk := libRouteRotation(b, plans)
	var valid [][]powerLayoutWire
	for _, r := range routes {
		if libRouteOutward(r, a, b, ar, br, ak, bk) {
			valid = append(valid, r)
		}
	}
	if ak && bk {
		valid = append(valid, libEscapeRoutes(a, b, ar, br, []float64{25, 30, 40, 50, 60, 80})...)
	}
	return valid
}

// Marker leads may branch off the already connected same-net tree.
func libSpendNamingBudget(budget []*int) bool {
	if len(budget) == 0 {
		return true
	}
	if *budget[0] <= 0 {
		return false
	}
	*budget[0]--
	return true
}

func libPlaceMarker(p *powerLayoutPlan, q powerLayoutPin, kind string, budget ...*int) bool {
	return libVisitMarker(p, q, kind, func(candidate *powerLayoutPlan) bool {
		*p = *candidate
		return true
	}, budget...)
}

// Visit complete, geometrically legal leads without publishing a rejected
// choice. The visitor lets the terminal solver reconsider an earlier island
// when a later island cannot be named.
func libVisitMarker(p *powerLayoutPlan, q powerLayoutPin, kind string, visit func(*powerLayoutPlan) bool, budget ...*int) bool {
	return libVisitMarkerLimited(p, q, kind, 0, visit, budget...)
}

func libVisitMarkerLimited(p *powerLayoutPlan, q powerLayoutPin, kind string, straightLimit int, visit func(*powerLayoutPlan) bool, budget ...*int) bool {
	var body layoutBBox
	for _, c := range p.Placements {
		for _, cp := range c.Pins {
			if cp == q {
				body = c.BBox
			}
		}
	}
	side, e := libPinSide(q, body)
	if e != nil {
		return false
	}
	directions := []string{side, "up", "down", "left", "right"}
	if kind == "ground" {
		directions = []string{"down", side, "left", "right", "up"}
	}
	if kind == "power" {
		directions = []string{"up", side, "left", "right", "down"}
	}
	cap := libMarkerOffsetCap(p, q.Net, kind)
	straightChoices, accepted := 0, false
	straightVisit := func(candidate *powerLayoutPlan) bool {
		straightChoices++
		accepted = visit(candidate)
		return accepted || (straightLimit > 0 && straightChoices >= straightLimit)
	}
	libVisitMarkerAt(p, q, kind, []string{side}, cap, straightVisit, budget...)
	if accepted {
		return true
	}
	// A straight lead can be trapped by an adjacent pin's marker. Escape
	// outward before turning; never relax the full electrical/geometry gate.
	for shell := 10.0; shell <= 100; shell += 5 {
		for outward := 5.0; outward < shell && outward <= 50; outward += 5 {
			lateral := shell - outward
			if lateral > 50 {
				continue
			}
			for _, sign := range []float64{1, -1} {
				if !libSpendNamingBudget(budget) {
					return false
				}
				x, y := endpointFor(q.X, q.Y, outward, side)
				bend := [2]float64{x, y}
				if side == "left" || side == "right" {
					y += sign * lateral
				} else {
					x += sign * lateral
				}
				trial := *p
				trial.Wires = libAppendRoute(p.Wires, []powerLayoutWire{
					{Net: q.Net, Points: [][2]float64{{q.X, q.Y}, bend}},
					{Net: q.Net, Points: [][2]float64{bend, {x, y}}},
				})
				if validateLibGeometry(&trial) != nil {
					continue
				}
				tap := powerLayoutPin{Net: q.Net, X: x, Y: y}
				if libVisitMarkerAt(&trial, tap, kind, directions, math.Min(50, cap-shell), visit, budget...) {
					return true
				}
			}
		}
	}
	return false
}

func libMarkerOffsetCap(p *powerLayoutPlan, net, kind string) float64 {
	cap := 300.0
	if isNetPortKind(kind) {
		span := acPortTotalLen(net)
		for _, f := range p.Flags {
			if isNetPortKind(f.Kind) {
				span = math.Max(span, acPortTotalLen(f.Net))
			}
		}
		cap = math.Min(cap, plCeil(10+2*span))
	}
	return cap
}

func libPlaceMarkerAt(p *powerLayoutPlan, q powerLayoutPin, kind string, directions []string, cap float64, budget ...*int) bool {
	return libVisitMarkerAt(p, q, kind, directions, cap, func(candidate *powerLayoutPlan) bool {
		*p = *candidate
		return true
	}, budget...)
}

func libVisitMarkerAt(p *powerLayoutPlan, q powerLayoutPin, kind string, directions []string, cap float64, visit func(*powerLayoutPlan) bool, budget ...*int) bool {
	segments, e := schTerminalSegments(p)
	if e != nil {
		return false
	}
	for offset := 10.0; offset <= cap; offset += 5 {
		seen := map[string]bool{}
		for _, direction := range directions {
			if seen[direction] {
				continue
			}
			seen[direction] = true
			if !libSpendNamingBudget(budget) {
				return false
			}
			f := powerLayoutFlag{Net: q.Net, Kind: kind, PinX: q.X, PinY: q.Y, Direction: direction, Offset: offset}
			if libMarkerRetraces(f, segments) {
				continue
			}
			if schTerminalCandidate(p, f, segments) == nil {
				candidate := *p
				candidate.Flags = append(append([]powerLayoutFlag(nil), p.Flags...), f)
				if validateLibGeometry(&candidate) == nil {
					if visit(&candidate) {
						return true
					}
				}
			}
		}
	}
	return false
}

// A naming branch can meet a wire at one point, but must not redraw a
// positive-length part of it (otherwise the marker hides the real junction).
func libMarkerRetraces(f powerLayoutFlag, segments []powerLayoutWire) bool {
	x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
	a, b := [2]float64{f.PinX, f.PinY}, [2]float64{x, y}
	for _, w := range segments {
		if w.Net != f.Net {
			continue
		}
		for i := 1; i < len(w.Points); i++ {
			c, d := w.Points[i-1], w.Points[i]
			axis := -1
			if a[1] == b[1] && a[1] == c[1] && a[1] == d[1] {
				axis = 0
			}
			if a[0] == b[0] && a[0] == c[0] && a[0] == d[0] {
				axis = 1
			}
			if axis >= 0 && math.Max(math.Min(a[axis], b[axis]), math.Min(c[axis], d[axis])) < math.Min(math.Max(a[axis], b[axis]), math.Max(c[axis], d[axis])) {
				return true
			}
		}
	}
	return false
}

// Report whether two real pins face one another on the same axis. Symbol pin
// count is intentionally irrelevant: future tree branching is a net property.
func libFacingPins(p *powerLayoutPlan, a, b powerLayoutPin) bool {
	var ac, bc *powerLayoutPlacement
	for i := range p.Placements {
		c := &p.Placements[i]
		for _, q := range c.Pins {
			if libSamePhysicalPin(q, a) {
				ac = c
			}
			if libSamePhysicalPin(q, b) {
				bc = c
			}
		}
	}
	if ac == nil || bc == nil || ac == bc || a.Net == "" || a.Net != b.Net {
		return false
	}
	as, _ := libPinSide(a, ac.BBox)
	bs, _ := libPinSide(b, bc.BBox)
	return (a.X == b.X && ((a.Y < b.Y && as == "up" && bs == "down") || (a.Y > b.Y && as == "down" && bs == "up"))) || (a.Y == b.Y && ((a.X < b.X && as == "right" && bs == "left") || (a.X > b.X && as == "left" && bs == "right")))
}

func libPlanNetPinCount(p *powerLayoutPlan, net string) int {
	count := 0
	for _, component := range p.Placements {
		for _, pin := range component.Pins {
			if pin.Net == net {
				count++
			}
		}
	}
	return count
}

func libPlaceMidpointMarker(p *powerLayoutPlan, island libIsland, kind string, budget ...*int) bool {
	return libVisitMidpointMarker(p, island, kind, func(candidate *powerLayoutPlan) bool {
		*p = *candidate
		return true
	}, budget...)
}

func libVisitMidpointMarker(p *powerLayoutPlan, island libIsland, kind string, visit func(*powerLayoutPlan) bool, budget ...*int) bool {
	if kind != "net_port_bi" {
		return false
	}
	for i, a := range p.Placements {
		if len(a.Pins) != 2 {
			continue
		}
		for _, b := range p.Placements[:i] {
			if len(b.Pins) != 2 {
				continue
			}
			for _, ap := range a.Pins {
				for _, bp := range b.Pins {
					if ap.Net != island.net || bp.Net != island.net {
						continue
					}
					memberA, memberB := false, false
					for _, q := range island.pins {
						memberA = memberA || q == ap
						memberB = memberB || q == bp
					}
					if !memberA || !memberB {
						continue
					}
					as, _ := libPinSide(ap, a.BBox)
					bs, _ := libPinSide(bp, b.BBox)
					vertical := ap.X == bp.X && ap.Y != bp.Y && ((ap.Y < bp.Y && as == "up" && bs == "down") || (ap.Y > bp.Y && as == "down" && bs == "up"))
					horizontal := ap.Y == bp.Y && ap.X != bp.X && ((ap.X < bp.X && as == "right" && bs == "left") || (ap.X > bp.X && as == "left" && bs == "right"))
					if !vertical && !horizontal {
						continue
					}
					q := powerLayoutPin{Net: island.net, X: (ap.X + bp.X) / 2, Y: (ap.Y + bp.Y) / 2}
					if !plGrid(q.X) || !plGrid(q.Y) {
						continue
					}
					connected := false
					for _, w := range p.Wires {
						if w.Net == island.net && plOnSegment([2]float64{ap.X, ap.Y}, w.Points[0], w.Points[1]) && plOnSegment([2]float64{bp.X, bp.Y}, w.Points[0], w.Points[1]) {
							connected = true
						}
					}
					if !connected {
						continue
					}
					dirs := []string{"right", "left"}
					if horizontal {
						dirs = []string{"up", "down"}
					}
					if libVisitMarkerAt(p, q, kind, dirs, 80, visit, budget...) {
						return true
					}
				}
			}
		}
	}
	return false
}

type libWireTreeMarkerCandidate struct {
	point      [2]float64
	directions []string
	rank       int
}

// libPlaceWireTreeMarker names an already-connected physical island from its
// real wire geometry. It deliberately considers only net ports: local power
// and ground keep their symbol-specific marker semantics. A perpendicular lead
// may start at a segment midpoint, endpoint, or existing T/contact node, but it
// must still pass libMarkerRetraces, schTerminalCandidate, and the complete
// geometry validator in libPlaceMarkerAt.
func libPlaceWireTreeMarker(p *powerLayoutPlan, island libIsland, kind string, budget ...*int) bool {
	return libVisitWireTreeMarker(p, island, kind, func(candidate *powerLayoutPlan) bool {
		*p = *candidate
		return true
	}, budget...)
}

func libVisitWireTreeMarker(p *powerLayoutPlan, island libIsland, kind string, visit func(*powerLayoutPlan) bool, budget ...*int) bool {
	if !isNetPortKind(kind) || len(island.wireIndices) == 0 {
		return false
	}
	contacts := libWireContactNodes(p.Wires)
	candidates := map[[2]float64]*libWireTreeMarkerCandidate{}
	add := func(point [2]float64, directions []string, rank int) {
		if !plGrid(point[0]) || !plGrid(point[1]) {
			return
		}
		candidate, ok := candidates[point]
		if !ok {
			candidate = &libWireTreeMarkerCandidate{point: point, rank: rank}
			candidates[point] = candidate
		}
		if rank < candidate.rank {
			candidate.rank = rank
		}
		seen := map[string]bool{}
		for _, direction := range candidate.directions {
			seen[direction] = true
		}
		for _, direction := range directions {
			if !seen[direction] {
				candidate.directions = append(candidate.directions, direction)
				seen[direction] = true
			}
		}
	}
	for _, index := range island.wireIndices {
		if index < 0 || index >= len(p.Wires) {
			continue
		}
		wire := p.Wires[index]
		if wire.Net != island.net || len(wire.Points) != 2 {
			continue
		}
		a, b := wire.Points[0], wire.Points[1]
		var directions []string
		switch {
		case a[1] == b[1] && a[0] != b[0]:
			directions = []string{"up", "down"}
		case a[0] == b[0] && a[1] != b[1]:
			directions = []string{"right", "left"}
		default:
			continue
		}
		// Prefer the vacant middle of a segment, then established electrical
		// junctions, then bends/endpoints. All are exact points on this island;
		// map merging preserves both perpendicular choices at bends.
		add([2]float64{(a[0] + b[0]) / 2, (a[1] + b[1]) / 2}, directions, 0)
		for point := range contacts {
			if plOnSegment(point, a, b) {
				add(point, directions, 1)
			}
		}
		add(a, directions, 2)
		add(b, directions, 2)
	}
	ordered := make([]libWireTreeMarkerCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, *candidate)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].rank != ordered[j].rank {
			return ordered[i].rank < ordered[j].rank
		}
		if ordered[i].point[1] != ordered[j].point[1] {
			return ordered[i].point[1] > ordered[j].point[1]
		}
		return ordered[i].point[0] < ordered[j].point[0]
	})
	cap := libMarkerOffsetCap(p, island.net, kind)
	for _, candidate := range ordered {
		q := powerLayoutPin{Net: island.net, X: candidate.point[0], Y: candidate.point[1]}
		if libVisitMarkerAt(p, q, kind, candidate.directions, cap, visit, budget...) {
			return true
		}
	}
	return false
}

// Merge same-net collinear intervals, including a new segment that bridges two
// old ones. Rebuild slices so searching a candidate cannot mutate its parent.
func libAppendRoute(existing, route []powerLayoutWire) []powerLayoutWire {
	return libAppendRouteWithNodes(existing, route, libWireContactNodes(existing))
}

// existingNodes is computed once for a fixed placement-search parent. Only
// contacts involving the candidate route are incremental; this preserves the
// exact T/endpoint evidence of libWireContactNodes without rescanning the fixed
// existing forest for every XY proposal.
func libAppendRouteWithNodes(existing, route []powerLayoutWire, existingNodes map[[2]float64]bool) []powerLayoutWire {
	// An independently authored segment endpoint at a crossing is a real
	// contact, including an invalid foreign-net one. Never erase that evidence
	// while collapsing collinear geometry before the validator sees it.
	nodes := make(map[[2]float64]bool, len(existingNodes))
	for point := range existingNodes {
		nodes[point] = true
	}
	libAddWireContactNodes(nodes, route, existing)
	libAddWireContactNodes(nodes, route, route)
	out := make([]powerLayoutWire, len(existing))
	for i, w := range existing {
		out[i] = w
		out[i].Points = append([][2]float64(nil), w.Points...)
	}
	for _, w := range route {
		w.Points = append([][2]float64(nil), w.Points...)
		for i := 0; i < len(out); {
			old := out[i]
			if old.Net == w.Net && len(w.Points) == 2 && len(old.Points) == 2 {
				a, b, c, d := w.Points[0], w.Points[1], old.Points[0], old.Points[1]
				horizontal := a[1] == b[1] && a[1] == c[1] && a[1] == d[1]
				vertical := a[0] == b[0] && a[0] == c[0] && a[0] == d[0]
				if (horizontal || vertical) && plSegmentsMeet(a, b, c, d) {
					axis := 0
					if vertical {
						axis = 1
					}
					lo, hi := a, a
					for _, point := range [][2]float64{b, c, d} {
						if point[axis] < lo[axis] {
							lo = point
						}
						if point[axis] > hi[axis] {
							hi = point
						}
					}
					w.Points = [][2]float64{lo, hi}
					out = append(out[:i], out[i+1:]...)
					i = 0 // The extended interval may now reach an earlier segment.
					continue
				}
			}
			i++
		}
		out = append(out, w)
	}
	// Collinear union removes redundant positive-length overlaps, then restores
	// the original real contact vertices. Thus merging cannot silently turn a
	// true X junction (or a foreign-net T short) into a noncontact crossing.
	var split []powerLayoutWire
	for _, w := range out {
		if len(w.Points) != 2 {
			split = append(split, w)
			continue
		}
		a, b := w.Points[0], w.Points[1]
		points := [][2]float64{a, b}
		for p := range nodes {
			if !plOnSegment(p, a, b) {
				continue
			}
			duplicate := false
			for _, q := range points {
				if math.Abs(p[0]-q[0]) <= 1e-6 && math.Abs(p[1]-q[1]) <= 1e-6 {
					duplicate = true
					break
				}
			}
			if !duplicate {
				points = append(points, p)
			}
		}
		axis := 0
		if a[0] == b[0] {
			axis = 1
		}
		sort.Slice(points, func(i, j int) bool {
			if a[axis] < b[axis] {
				return points[i][axis] < points[j][axis]
			}
			return points[i][axis] > points[j][axis]
		})
		for i := 1; i < len(points); i++ {
			split = append(split, powerLayoutWire{Net: w.Net, Points: [][2]float64{points[i-1], points[i]}})
		}
	}
	return split
}

func libAddWireContactNodes(nodes map[[2]float64]bool, aWires, bWires []powerLayoutWire) {
	for _, aWire := range aWires {
		for ai := 1; ai < len(aWire.Points); ai++ {
			a, b := aWire.Points[ai-1], aWire.Points[ai]
			for _, bWire := range bWires {
				for bi := 1; bi < len(bWire.Points); bi++ {
					c, d := bWire.Points[bi-1], bWire.Points[bi]
					if (b[0]-a[0])*(d[1]-c[1]) == (b[1]-a[1])*(d[0]-c[0]) {
						continue
					}
					for _, point := range [][2]float64{a, b} {
						if plOnSegment(point, c, d) {
							nodes[point] = true
						}
					}
					for _, point := range [][2]float64{c, d} {
						if plOnSegment(point, a, b) {
							nodes[point] = true
						}
					}
				}
			}
		}
	}
}

func libWireContactNodes(wires []powerLayoutWire) map[[2]float64]bool {
	nodes := map[[2]float64]bool{}
	for _, w := range wires {
		for i := 1; i < len(w.Points); i++ {
			a, b := w.Points[i-1], w.Points[i]
			for _, v := range wires {
				for j := 1; j < len(v.Points); j++ {
					c, d := v.Points[j-1], v.Points[j]
					if (b[0]-a[0])*(d[1]-c[1]) == (b[1]-a[1])*(d[0]-c[0]) {
						continue
					}
					for _, p := range [][2]float64{a, b} {
						if plOnSegment(p, c, d) {
							nodes[p] = true
						}
					}
				}
			}
		}
	}
	return nodes
}
