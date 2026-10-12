package app

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Pre-route bridging of same-net neighbour pads on fine-pitch ICs (v0.9.2
// phase 2). On a 0.4 mm QFN the pad gap (0.2 mm) is narrower than a track plus
// two clearances, so no router can join two adjacent pads of one net in the
// gap: each pad needs its own escape and the second one is often the
// connection that stays unrouted. The planner lays a short copper bridge from
// pad centre to pad centre before routing; the router then sees one pad group.

// prebridgeParams steers the planner (mil).
type prebridgeParams struct {
	// MaxPitchMil is the largest centre distance of two bridged pads.
	MaxPitchMil float64
	// Refs restricts the ICs (designators); empty = every IC with a fine row.
	Refs []string
	// Place is where along the pad's long axis the bridge sits: centre, inner
	// (toward the part centre) or outer.
	Place string
	// DefaultClearanceMil / DefaultWidthMil / DefaultMinMil apply to nets
	// without an intent entry (board rules).
	DefaultClearanceMil float64
	DefaultWidthMil     float64
	DefaultMinMil       float64
}

// prebridge is one planned copper bridge.
type prebridge struct {
	Ref        string  `json:"ref"`
	Net        string  `json:"net"`
	PadA       string  `json:"padA"`
	PadB       string  `json:"padB"`
	Layer      int     `json:"layer"`
	StartX     float64 `json:"startX"`
	StartY     float64 `json:"startY"`
	EndX       float64 `json:"endX"`
	EndY       float64 `json:"endY"`
	WidthMil   float64 `json:"widthMil"`
	IntentMil  float64 `json:"intentWidthMil"`
	MinMil     float64 `json:"minWidthMil"`
	MaxFitMil  float64 `json:"maxFitMil"`
	ClearMil   float64 `json:"clearanceMil"`
	PitchMil   float64 `json:"pitchMil"`
	Necked     bool    `json:"necked,omitempty"`
	NeckReason string  `json:"neckReason,omitempty"`
}

// prebridgeSkip names a candidate pair that was not bridged and why.
type prebridgeSkip struct {
	Ref    string `json:"ref"`
	Net    string `json:"net"`
	PadA   string `json:"padA"`
	PadB   string `json:"padB"`
	Reason string `json:"reason"`
}

type prebridgePlan struct {
	Params  prebridgeParams `json:"params"`
	Bridges []prebridge     `json:"bridges"`
	Skipped []prebridgeSkip `json:"skipped,omitempty"`
}

// prebridgeNet is the width / clearance intent of one net.
type prebridgeNet struct{ Width, Min, Clearance float64 }

type pbRect struct{ x0, y0, x1, y1 float64 }

// obstacle is foreign copper a bridge must keep clear of.
type pbObstacle struct {
	net   string
	layer int // 0 = every layer (through hole)
	rect  pbRect
	// seg: a track (capsule); half is the half width. Otherwise rect is used.
	seg        bool
	ax, ay     float64
	bx, by     float64
	half       float64
	circle     bool
	cx, cy, rr float64
	what       string
}

// planPrebridges finds the same-net adjacent pad pairs of fine-pitch rows and
// sizes a bridge for each. nets maps net name → intent.
func planPrebridges(s *boardSnapshot, nets map[string]prebridgeNet, p prebridgeParams) prebridgePlan {
	if p.MaxPitchMil <= 0 {
		p.MaxPitchMil = 19.7 // 0.5 mm
	}
	if p.Place == "" {
		p.Place = "center"
	}
	plan := prebridgePlan{Params: p}
	want := map[string]bool{}
	for _, r := range p.Refs {
		want[strings.TrimSpace(r)] = true
	}
	obs := prebridgeObstacles(s)
	for _, c := range s.Components {
		if len(want) > 0 && !want[c.Designator] {
			continue
		}
		cx, cy := padCentroid(c.Pads)
		pads := make([]boardPad, 0, len(c.Pads))
		for _, q := range c.Pads {
			if q.Net == "" || q.W <= 0 || q.H <= 0 || q.Layer == pcbLayerMulti || q.PadType != 0 {
				continue
			}
			pads = append(pads, q)
		}
		for i := 0; i < len(pads); i++ {
			for j := i + 1; j < len(pads); j++ {
				a, b := pads[i], pads[j]
				if a.Net != b.Net || a.Layer != b.Layer {
					continue
				}
				br, why := bridgeFor(c.Designator, a, b, cx, cy, nets, obs, p)
				switch {
				case why == "":
					plan.Bridges = append(plan.Bridges, *br)
				case why != "not-adjacent":
					plan.Skipped = append(plan.Skipped, prebridgeSkip{Ref: c.Designator, Net: a.Net, PadA: a.Number, PadB: b.Number, Reason: why})
				}
			}
		}
	}
	sort.Slice(plan.Bridges, func(i, j int) bool {
		a, b := plan.Bridges[i], plan.Bridges[j]
		if a.Ref != b.Ref {
			return a.Ref < b.Ref
		}
		return a.PadA < b.PadA
	})
	return plan
}

func padCentroid(ps []boardPad) (x, y float64) {
	if len(ps) == 0 {
		return 0, 0
	}
	for _, q := range ps {
		x += q.X
		y += q.Y
	}
	return x / float64(len(ps)), y / float64(len(ps))
}

// bridgeFor sizes the bridge between two same-net pads; why = "not-adjacent"
// means the pair is no candidate at all (silently dropped).
func bridgeFor(ref string, a, b boardPad, cx, cy float64, nets map[string]prebridgeNet, obs []pbObstacle, p prebridgeParams) (*prebridge, string) {
	const tol = 1.0 // mil
	// Rows of equal pads side by side: same size, equal long-axis coordinate,
	// offset along the short axis only.
	if math.Abs(a.W-b.W) > 0.05*a.W || math.Abs(a.H-b.H) > 0.05*a.H {
		return nil, "not-adjacent"
	}
	dx, dy := b.X-a.X, b.Y-a.Y
	var horizontal bool // bridge runs along x (pads are tall)
	switch {
	case a.H > a.W && math.Abs(dy) <= tol:
		horizontal = true
	case a.W > a.H && math.Abs(dx) <= tol:
		horizontal = false
	default:
		return nil, "not-adjacent"
	}
	pitch := math.Hypot(dx, dy)
	short, long := a.W, a.H
	if !horizontal {
		short, long = a.H, a.W
	}
	if pitch > p.MaxPitchMil || pitch <= short {
		return nil, "not-adjacent"
	}
	// A pad between them (a third pad centred in between) would make them
	// non-adjacent; same-pitch rows have none, but an odd footprint might.
	mx, my := (a.X+b.X)/2, (a.Y+b.Y)/2
	rep := &prebridge{Ref: ref, Net: a.Net, PadA: a.Number, PadB: b.Number, Layer: a.Layer, PitchMil: pbRound(pitch)}
	if _, ok := nets[a.Net]; !ok {
		nets = map[string]prebridgeNet{a.Net: {Width: p.DefaultWidthMil, Min: p.DefaultMinMil, Clearance: p.DefaultClearanceMil}}
	}
	n := nets[a.Net]
	if n.Clearance <= 0 {
		n.Clearance = p.DefaultClearanceMil
	}
	if n.Width <= 0 {
		n.Width = p.DefaultWidthMil
	}
	if n.Min <= 0 || n.Min > n.Width {
		n.Min = math.Min(p.DefaultMinMil, n.Width)
	}
	rep.IntentMil, rep.MinMil, rep.ClearMil = n.Width, n.Min, n.Clearance

	// Position along the long axis.
	off := 0.0
	if p.Place == "inner" || p.Place == "outer" {
		room := long/2 - n.Min/2
		if room < 0 {
			room = 0
		}
		ctr := cx - mx // long axis is x for a column of pads
		if horizontal {
			ctr = cy - my // long axis is y
		}
		sign := 1.0
		if ctr < 0 {
			sign = -1
		}
		if p.Place == "outer" {
			sign = -sign
		}
		off = sign * room * 0.5
	}
	sx, sy, ex, ey := a.X, a.Y, b.X, b.Y
	if horizontal {
		sy, ey = a.Y+off, b.Y+off
	} else {
		sx, ex = a.X+off, b.X+off
	}
	rep.StartX, rep.StartY, rep.EndX, rep.EndY = pbRound(sx), pbRound(sy), pbRound(ex), pbRound(ey)

	// Largest width that keeps every foreign obstacle at the clearance.
	foreign := make([]pbObstacle, 0, 16)
	for _, o := range obs {
		if o.net == a.Net || (o.layer != 0 && o.layer != a.Layer) {
			continue
		}
		// bounding pre-filter
		if o.reach(mx, my) > pitch+long+4*n.Clearance+100 {
			continue
		}
		foreign = append(foreign, o)
	}
	lo, hi := 0.0, math.Max(n.Width, short)
	for it := 0; it < 40; it++ {
		w := (lo + hi) / 2
		if bridgeClear(sx, sy, ex, ey, w, n.Clearance, foreign) {
			lo = w
		} else {
			hi = w
		}
	}
	maxFit := math.Floor(lo*100) / 100
	if lo >= n.Width-1e-6 {
		maxFit = n.Width
	}
	rep.MaxFitMil = maxFit
	w := n.Width
	if w > maxFit {
		w = maxFit
		rep.Necked = true
		rep.NeckReason = fmt.Sprintf("clearance %.2f mil to foreign copper allows %.2f mil", n.Clearance, maxFit)
	}
	if w < n.Min {
		return nil, fmt.Sprintf("no width fits: max %.2f mil < intent minimum %.2f mil at clearance %.2f", maxFit, n.Min, n.Clearance)
	}
	if w < 1 {
		return nil, "no width fits"
	}
	rep.WidthMil = pbRound(w)
	return rep, ""
}

func pbRound(v float64) float64 { return math.Round(v*100) / 100 }

// bridgeClear reports whether a capsule (segment, width w) keeps ≥ clr to all
// obstacles.
func bridgeClear(sx, sy, ex, ey, w, clr float64, obs []pbObstacle) bool {
	for _, o := range obs {
		if o.dist(sx, sy, ex, ey)-w/2 < clr-1e-6 {
			return false
		}
	}
	return true
}

// dist is the smallest distance (mil) from segment (sx,sy)-(ex,ey) to the
// obstacle's copper outline.
func (o pbObstacle) dist(sx, sy, ex, ey float64) float64 {
	switch {
	case o.circle:
		return math.Max(0, pbSegPoint(sx, sy, ex, ey, o.cx, o.cy)-o.rr)
	case o.seg:
		return math.Max(0, pbSegSeg(sx, sy, ex, ey, o.ax, o.ay, o.bx, o.by)-o.half)
	}
	return segRectDist(sx, sy, ex, ey, o.rect)
}

// reach is a cheap upper bound proxy of the distance from (x,y) to the obstacle.
func (o pbObstacle) reach(x, y float64) float64 {
	switch {
	case o.circle:
		return math.Hypot(o.cx-x, o.cy-y) - o.rr
	case o.seg:
		return pbSegPoint(o.ax, o.ay, o.bx, o.by, x, y)
	}
	dx := math.Max(math.Max(o.rect.x0-x, 0), x-o.rect.x1)
	dy := math.Max(math.Max(o.rect.y0-y, 0), y-o.rect.y1)
	return math.Hypot(dx, dy)
}

func pbSegPoint(sx, sy, ex, ey, px, py float64) float64 {
	vx, vy := ex-sx, ey-sy
	l2 := vx*vx + vy*vy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((px-sx)*vx+(py-sy)*vy)/l2))
	}
	return math.Hypot(sx+t*vx-px, sy+t*vy-py)
}

func pbSegSeg(ax, ay, bx, by, cx, cy, dx, dy float64) float64 {
	if pbSegsCross(ax, ay, bx, by, cx, cy, dx, dy) {
		return 0
	}
	return math.Min(math.Min(pbSegPoint(ax, ay, bx, by, cx, cy), pbSegPoint(ax, ay, bx, by, dx, dy)),
		math.Min(pbSegPoint(cx, cy, dx, dy, ax, ay), pbSegPoint(cx, cy, dx, dy, bx, by)))
}

func pbSegsCross(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	o := func(px, py, qx, qy, rx, ry float64) float64 { return (qx-px)*(ry-py) - (qy-py)*(rx-px) }
	d1, d2 := o(ax, ay, bx, by, cx, cy), o(ax, ay, bx, by, dx, dy)
	d3, d4 := o(cx, cy, dx, dy, ax, ay), o(cx, cy, dx, dy, bx, by)
	return d1*d2 < 0 && d3*d4 < 0
}

// segRectDist: distance of a segment to an axis-aligned rectangle (0 when they
// touch). The distance of a point on the segment to a convex set is convex in
// the parameter, so a ternary search is exact enough (1e-9 mil).
func segRectDist(sx, sy, ex, ey float64, r pbRect) float64 {
	f := func(t float64) float64 {
		x, y := sx+t*(ex-sx), sy+t*(ey-sy)
		dx := math.Max(math.Max(r.x0-x, 0), x-r.x1)
		dy := math.Max(math.Max(r.y0-y, 0), y-r.y1)
		return math.Hypot(dx, dy)
	}
	lo, hi := 0.0, 1.0
	for i := 0; i < 80; i++ {
		m1, m2 := lo+(hi-lo)/3, hi-(hi-lo)/3
		if f(m1) < f(m2) {
			hi = m2
		} else {
			lo = m1
		}
	}
	return f((lo + hi) / 2)
}

// prebridgeObstacles lists all pads (axis-aligned boxes), tracks and vias.
func prebridgeObstacles(s *boardSnapshot) []pbObstacle {
	var out []pbObstacle
	for _, c := range s.Components {
		for _, q := range c.Pads {
			if q.W <= 0 || q.H <= 0 {
				continue
			}
			layer := q.Layer
			if layer == pcbLayerMulti {
				layer = 0
			}
			out = append(out, pbObstacle{net: q.Net, layer: layer, what: c.Designator + "." + q.Number,
				rect: pbRect{q.X - q.W/2, q.Y - q.H/2, q.X + q.W/2, q.Y + q.H/2}})
		}
	}
	if s.Copper == nil {
		return out
	}
	for _, l := range s.Copper.Lines {
		var t struct {
			Net    string  `json:"net"`
			Layer  int     `json:"layer"`
			StartX float64 `json:"startX"`
			StartY float64 `json:"startY"`
			EndX   float64 `json:"endX"`
			EndY   float64 `json:"endY"`
			Width  float64 `json:"lineWidth"`
		}
		if b, err := json.Marshal(l); err != nil || json.Unmarshal(b, &t) != nil {
			continue
		}
		out = append(out, pbObstacle{net: t.Net, layer: t.Layer, seg: true, ax: t.StartX, ay: t.StartY, bx: t.EndX, by: t.EndY, half: t.Width / 2, what: "track"})
	}
	for _, v := range s.Copper.Vias {
		var t struct {
			Net      string  `json:"net"`
			X        float64 `json:"x"`
			Y        float64 `json:"y"`
			Diameter float64 `json:"diameter"`
		}
		if b, err := json.Marshal(v); err != nil || json.Unmarshal(b, &t) != nil {
			continue
		}
		out = append(out, pbObstacle{net: t.Net, layer: 0, circle: true, cx: t.X, cy: t.Y, rr: t.Diameter / 2, what: "via"})
	}
	return out
}

// prebridgeOps turns a plan into bridge.py edit ops (locked copper: a router
// keeps it as fixed wiring and rip-up leaves it).
func prebridgeOps(plan prebridgePlan) map[string]any {
	tracks := make([]map[string]any, 0, len(plan.Bridges))
	for _, b := range plan.Bridges {
		tracks = append(tracks, map[string]any{
			"net": b.Net, "layer": b.Layer, "locked": true,
			"startX": b.StartX, "startY": b.StartY, "endX": b.EndX, "endY": b.EndY,
			"lineWidth": b.WidthMil,
		})
	}
	return map[string]any{"addTracks": tracks}
}
