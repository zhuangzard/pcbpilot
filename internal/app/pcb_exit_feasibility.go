package app

// pcb_exit_feasibility.go — read-only per-pad exit-feasibility report for
// intent nets (router-diagnosis.md §5): for each SMD pad, the widest straight
// outward exit the exact geometry allows (segment-to-copper distances, the
// DRC's capsule test, not a router or a grid), and whether any legal escape
// of at least widthMil.min exists. It writes nothing.

import (
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// Failure taxonomy. Only geometry can be told from the board alone; the other
// two need a routing run (search-blocked: legal exits exist but the router did
// not find the connection; bug-blocked: the engine's own log contradicts the
// geometry, e.g. an obstacle-cache false positive).
const (
	failGeometry = "geometry-blocked"
	failSearch   = "search-blocked"
	failBug      = "bug-blocked"
)

type exitRow struct {
	Pin   string  `json:"pin"` // REF.PIN
	Net   string  `json:"net"`
	Layer int     `json:"layer"`
	PadW  float64 `json:"padWMil"`
	PadH  float64 `json:"padHMil"`
	// OuterMil / MinMil: the net's intent widths.
	OuterMil float64 `json:"outerMil"`
	MinMil   float64 `json:"minMil"`
	// WMaxOutwardMil: widest straight exit (capped at OuterMil, 0 = none);
	// LimiterOutward: what sets it.
	WMaxOutwardMil float64 `json:"wmaxOutwardMil"`
	LimiterOutward string  `json:"limiterOutward,omitempty"`
	// Escape: the escape the planner would pick (outward | bridge |
	// inward-via) and its width; empty when none reaches widthMil.min.
	Escape         string  `json:"escape,omitempty"`
	EscapeWidthMil float64 `json:"escapeWidthMil,omitempty"`
	// Class: full (the full width exits) | neck (only a narrower escape
	// >= widthMil.min) | IMPOSSIBLE.
	Class string `json:"class"`
	// ShortfallMil: widthMil.min minus the best straight exit (IMPOSSIBLE).
	ShortfallMil float64 `json:"shortfallMil,omitempty"`
	Why          string  `json:"why,omitempty"`
	// Failure: failGeometry for IMPOSSIBLE pads; "" otherwise (a routing
	// run decides between failSearch and failBug).
	Failure string `json:"failure,omitempty"`
	// PlannerConservative: the escape exists only because an own-net via at
	// an SMD pad is accepted (the engine accepts it, the default planner rule
	// does not).
	PlannerConservative bool `json:"plannerConservative,omitempty"`
}

// exitFeasibility reports every SMD pad of an intent net, sorted by part and
// pin number.
func exitFeasibility(env *escapeEnv, reqs map[string]specctra.NetRequirement) []exitRow {
	byRef := map[string][]pcbPadP{}
	for _, q := range env.pads {
		byRef[q.Designator] = append(byRef[q.Designator], q)
	}
	var rows []exitRow
	for _, pad := range env.pads {
		r, ok := reqs[pad.Net]
		if !ok || pad.Layer != 1 && pad.Layer != 2 || r.OuterMil <= 0 {
			continue
		}
		minW := r.MinMil
		if minW <= 0 || minW > r.OuterMil {
			minW = r.OuterMil
		}
		part := byRef[pad.Designator]
		dir, half, ok := outward(pad, part)
		if !ok {
			continue
		}
		c := escapeCand{pad: pad, part: part, dir: dir, half: half, full: r.OuterMil, min: minW}
		edge := c.along(half)
		c.tCourt = half + math.Max(math.Min(exitDist(env.courtyardOf(pad.Designator, part), edge, dir)+1, padEscapeReachMil(c.full)-1), 1)
		c.mate = rowMate(pad, part, dir)
		local := env.near(c)
		row := exitRow{Pin: c.key(), Net: pad.Net, Layer: pad.Layer, PadW: pad.W, PadH: pad.H, OuterMil: c.full, MinMil: minW}
		from, end := c.outwardSpan(local, minW)
		g, who := local.lineGap(pad.Net, pad.Layer, from, end)
		row.WMaxOutwardMil = math.Max(0, math.Floor(math.Min(c.full, 2*g)*100)/100)
		row.LimiterOutward = who
		e, why := local.escapeOne(c)
		if why == "" {
			row.Escape, row.EscapeWidthMil = e.Kind, e.WidthMil
		}
		switch {
		case row.WMaxOutwardMil+specctraEps >= c.full:
			row.Class = "full"
		case why == "":
			row.Class = "neck"
		default:
			row.Class, row.Why, row.Failure = "IMPOSSIBLE", why, failGeometry
			row.ShortfallMil = math.Round((minW-row.WMaxOutwardMil)*100) / 100
		}
		if !env.ownViaAtSMD && row.Class == "IMPOSSIBLE" {
			relaxed := *local
			relaxed.ownViaAtSMD = true
			if _, w2 := relaxed.escapeOne(c); w2 == "" {
				row.PlannerConservative = true
			}
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := strings.SplitN(rows[i].Pin, ".", 2), strings.SplitN(rows[j].Pin, ".", 2)
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		return naturalLess(a[1], b[1])
	})
	return rows
}
