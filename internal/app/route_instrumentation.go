package app

// route_instrumentation.go — D5: structured reporting of routing outcomes.
// Pure reporting; no behavioral changes to routing.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// RouteInstrumentation is the structured routing outcome report written to
// summary["routeInstrumentation"].
type RouteInstrumentation struct {
	Connections int `json:"connections"`
	// Completed and Unrouted are absent when not known: a partial result (the
	// router stopped before its final count) or a count that does not add up.
	Completed       *int    `json:"completed,omitempty"`
	Unrouted        *int    `json:"unrouted,omitempty"`
	Partial         bool    `json:"partial,omitempty"`
	RouterWallTimeS float64 `json:"routerWallTimeS"`
	RouterVersion   string  `json:"routerVersion,omitempty"`
	InputSHA256     string  `json:"inputSha256,omitempty"`
	// Per-class breakdown.
	ByNetClass []NetClassStats `json:"byNetClass,omitempty"`
	// Per-domain breakdown.
	ByDomain []DomainStats `json:"byDomain,omitempty"`
	// Per-region breakdown (board quadrants).
	ByRegion []RegionStats `json:"byRegion,omitempty"`
	// Periodic checkpoints from the router's progress output.
	Checkpoints []RouteCheckpoint `json:"checkpoints,omitempty"`
}

// NetClassStats counts connections in one netclass. Total and Completed are
// absent when they are not known (no input snapshot, a partial router result).
type NetClassStats struct {
	Name      string `json:"name"`
	Total     *int   `json:"total,omitempty"`
	Completed *int   `json:"completed,omitempty"`
	Unrouted  int    `json:"unrouted"`
}

// DomainStats counts connections in one insulation domain; Total and Completed
// as in NetClassStats.
type DomainStats struct {
	Domain    string `json:"domain"`
	Total     *int   `json:"total,omitempty"`
	Completed *int   `json:"completed,omitempty"`
	Unrouted  int    `json:"unrouted"`
}

// RegionStats counts unrouted connections in one coarse board region
// (quadrant). Only the router's unrouted list has endpoints, so the region of
// a routed connection is not known and there is no total here.
type RegionStats struct {
	Region   string `json:"region"` // "NW" | "NE" | "SW" | "SE"
	Unrouted int    `json:"unrouted"`
}

// RouteCheckpoint is a periodic progress snapshot from the router.
type RouteCheckpoint struct {
	ElapsedS   float64 `json:"elapsedS"`
	Unrouted   int     `json:"unrouted"`
	Violations int     `json:"violations"`
}

// buildRouteInstrumentation constructs the instrumentation report from the
// routing result and the input context. It is called after the router finishes
// and the result is selected, before the post-route pipeline.
func buildRouteInstrumentation(
	res *routeResult,
	connections int,
	dsnPath string,
	classes []kicad.NetClass,
	snap *boardSnapshot,
) *RouteInstrumentation {
	if res == nil {
		return nil
	}
	ri := &RouteInstrumentation{
		Connections:     connections,
		Partial:         res.Partial,
		RouterWallTimeS: res.Seconds,
		RouterVersion:   res.Version,
		InputSHA256:     filePathSHA256(dsnPath),
	}
	if u := res.UnroutedCount; unroutedListComplete(res) && u <= connections {
		completed := connections - u
		ri.Unrouted, ri.Completed = &u, &completed
	}
	ri.ByNetClass = netClassBreakdown(res, classes, snap)
	ri.ByDomain = domainBreakdown(res, classes, snap)
	ri.ByRegion = regionBreakdown(res, snap)
	return ri
}

// filePathSHA256 computes the sha256 hex digest of a file.
func filePathSHA256(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// unroutedListComplete: the result is final and its list and its count agree.
// Everything derived from the two needs that.
func unroutedListComplete(res *routeResult) bool {
	return !res.Partial && res.UnroutedCount >= 0 && len(res.Unrouted) == res.UnroutedCount
}

// connGroup is one group's connection counts; total < 0 means not known.
type connGroup struct {
	name            string
	total, unrouted int
}

// connGroups counts connections per group of nets. Totals use the input board's
// pads per net (as boardConnections) and need a result whose unrouted list is
// complete; otherwise only the groups with listed unrouted connections come
// back, with total -1. A group with more unrouted than connections is not known
// either.
func connGroups(res *routeResult, snap *boardSnapshot, groupOf func(net string) string) []connGroup {
	totals := map[string]int{}
	known := snap != nil && unroutedListComplete(res)
	if known {
		for net, n := range boardNetConnections(snap) {
			totals[groupOf(net)] += n
		}
	}
	unrouted := map[string]int{}
	for _, u := range res.Unrouted {
		unrouted[groupOf(u.Net)]++
	}
	names := make([]string, 0, len(totals))
	for g := range totals {
		names = append(names, g)
	}
	for g := range unrouted {
		if _, ok := totals[g]; !ok {
			names = append(names, g)
		}
	}
	sort.Strings(names)
	out := make([]connGroup, 0, len(names))
	for _, g := range names {
		c := connGroup{name: g, total: -1, unrouted: unrouted[g]}
		if known && totals[g] >= unrouted[g] {
			c.total = totals[g]
		}
		out = append(out, c)
	}
	return out
}

// counts are the Total and Completed fields of c (nil when not known).
func (c connGroup) counts() (total, completed *int) {
	if c.total < 0 {
		return nil, nil
	}
	t, d := c.total, c.total-c.unrouted
	return &t, &d
}

// netClassBreakdown classifies connections by their net's netclass.
func netClassBreakdown(res *routeResult, classes []kicad.NetClass, snap *boardSnapshot) []NetClassStats {
	if res == nil || len(classes) == 0 {
		return nil
	}
	netToClass := map[string]string{}
	for _, c := range classes {
		for _, n := range c.Nets {
			netToClass[strings.ToUpper(n)] = c.Name
		}
	}
	var out []NetClassStats
	for _, c := range connGroups(res, snap, func(net string) string {
		if cn := netToClass[strings.ToUpper(net)]; cn != "" {
			return cn
		}
		return "Default"
	}) {
		total, completed := c.counts()
		out = append(out, NetClassStats{Name: c.name, Total: total, Completed: completed, Unrouted: c.unrouted})
	}
	return out
}

// domainBreakdown classifies connections by insulation domain.
func domainBreakdown(res *routeResult, classes []kicad.NetClass, snap *boardSnapshot) []DomainStats {
	if res == nil || len(classes) == 0 {
		return nil
	}
	// Insulation domain classes are PPD_<domain>.
	netToDomain := map[string]string{}
	for _, c := range classes {
		if strings.HasPrefix(c.Name, "PPD_") {
			domain := strings.TrimPrefix(c.Name, "PPD_")
			for _, n := range c.Nets {
				netToDomain[strings.ToUpper(n)] = domain
			}
		}
	}
	if len(netToDomain) == 0 {
		return nil
	}
	var out []DomainStats
	for _, c := range connGroups(res, snap, func(net string) string {
		if d := netToDomain[strings.ToUpper(net)]; d != "" {
			return d
		}
		return "(none)"
	}) {
		total, completed := c.counts()
		out = append(out, DomainStats{Domain: c.name, Total: total, Completed: completed, Unrouted: c.unrouted})
	}
	return out
}

// regionBreakdown classifies unrouted connections by board quadrant.
func regionBreakdown(res *routeResult, snap *boardSnapshot) []RegionStats {
	if res == nil || snap == nil || snap.Outline == nil || len(res.Unrouted) == 0 || !unroutedListComplete(res) {
		return nil
	}
	bb := snap.Outline.BBox
	cx := (bb.MinX + bb.MaxX) / 2
	cy := (bb.MinY + bb.MaxY) / 2
	// Build pad location lookup.
	padLoc := map[string][2]float64{} // "REF.PIN" → {x, y}
	for _, comp := range snap.Components {
		for _, p := range comp.Pads {
			key := fmt.Sprintf("%s.%s", comp.Designator, p.Number)
			padLoc[key] = [2]float64{p.X, p.Y}
		}
	}
	byRegion := map[string]int{}
	for _, u := range res.Unrouted {
		loc, ok := padLoc[u.From]
		if !ok {
			loc, ok = padLoc[u.To]
		}
		if !ok {
			return nil // partial region counts would read as complete
		}
		region := "SW"
		switch {
		case loc[0] >= cx && loc[1] >= cy:
			region = "NE"
		case loc[0] >= cx && loc[1] < cy:
			region = "SE"
		case loc[0] < cx && loc[1] >= cy:
			region = "NW"
		}
		byRegion[region]++
	}
	var out []RegionStats
	for _, r := range []string{"NW", "NE", "SW", "SE"} {
		if n, ok := byRegion[r]; ok {
			out = append(out, RegionStats{Region: r, Unrouted: n})
		}
	}
	return out
}
