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
	Connections     int     `json:"connections"`
	Completed       int     `json:"completed"`
	Unrouted        int     `json:"unrouted"`
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

// NetClassStats counts connections in one netclass.
type NetClassStats struct {
	Name      string `json:"name"`
	Total     int    `json:"total"`
	Completed int    `json:"completed"`
	Unrouted  int    `json:"unrouted"`
}

// DomainStats counts connections in one insulation domain.
type DomainStats struct {
	Domain    string `json:"domain"`
	Total     int    `json:"total"`
	Completed int    `json:"completed"`
	Unrouted  int    `json:"unrouted"`
}

// RegionStats counts connections in one coarse board region (quadrant).
type RegionStats struct {
	Region    string `json:"region"` // "NW" | "NE" | "SW" | "SE"
	Total     int    `json:"total"`
	Completed int    `json:"completed"`
	Unrouted  int    `json:"unrouted"`
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
	completed := connections - res.UnroutedCount
	if res.UnroutedCount < 0 || completed < 0 || completed > connections {
		completed = 0 // partial: unrouted count may be unknown (-1)
	}
	ri := &RouteInstrumentation{
		Connections:     connections,
		Unrouted:        res.UnroutedCount,
		Completed:       completed,
		RouterWallTimeS: res.Seconds,
		RouterVersion:   res.Version,
		InputSHA256:     filePathSHA256(dsnPath),
	}
	ri.ByNetClass = netClassBreakdown(res, classes)
	ri.ByDomain = domainBreakdown(res, classes)
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

// netClassBreakdown classifies unrouted connections by their net's netclass.
func netClassBreakdown(res *routeResult, classes []kicad.NetClass) []NetClassStats {
	if res == nil || len(classes) == 0 {
		return nil
	}
	// Build net → class name lookup.
	netToClass := map[string]string{}
	for _, c := range classes {
		for _, n := range c.Nets {
			netToClass[strings.ToUpper(n)] = c.Name
		}
	}
	type counts struct{ total, unrouted int }
	byClass := map[string]*counts{}
	// Count unrouted per class.
	for _, u := range res.Unrouted {
		cn := netToClass[strings.ToUpper(u.Net)]
		if cn == "" {
			cn = "Default"
		}
		c := byClass[cn]
		if c == nil {
			c = &counts{}
			byClass[cn] = c
		}
		c.unrouted++
	}
	// We don't have the total connections per class, so report only the
	// unrouted breakdown. Total is set to unrouted (we know at least this many).
	names := make([]string, 0, len(byClass))
	for name := range byClass {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []NetClassStats
	for _, name := range names {
		c := byClass[name]
		out = append(out, NetClassStats{Name: name, Unrouted: c.unrouted, Total: c.unrouted, Completed: 0})
	}
	return out
}

// domainBreakdown classifies unrouted connections by insulation domain.
func domainBreakdown(res *routeResult, classes []kicad.NetClass) []DomainStats {
	if res == nil || len(classes) == 0 || len(res.Unrouted) == 0 {
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
	type counts struct{ total, unrouted int }
	byDomain := map[string]*counts{}
	for _, u := range res.Unrouted {
		d := netToDomain[strings.ToUpper(u.Net)]
		if d == "" {
			d = "(none)"
		}
		c := byDomain[d]
		if c == nil {
			c = &counts{}
			byDomain[d] = c
		}
		c.unrouted++
	}
	domains := make([]string, 0, len(byDomain))
	for d := range byDomain {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	var out []DomainStats
	for _, d := range domains {
		c := byDomain[d]
		out = append(out, DomainStats{Domain: d, Unrouted: c.unrouted, Total: c.unrouted})
	}
	return out
}

// regionBreakdown classifies unrouted connections by board quadrant.
func regionBreakdown(res *routeResult, snap *boardSnapshot) []RegionStats {
	if res == nil || snap == nil || snap.Outline == nil || len(res.Unrouted) == 0 {
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
	type counts struct{ total, unrouted int }
	byRegion := map[string]*counts{}
	for _, u := range res.Unrouted {
		loc, ok := padLoc[u.From]
		if !ok {
			loc, ok = padLoc[u.To]
		}
		if !ok {
			continue
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
		c := byRegion[region]
		if c == nil {
			c = &counts{}
			byRegion[region] = c
		}
		c.unrouted++
	}
	var out []RegionStats
	for _, r := range []string{"NW", "NE", "SW", "SE"} {
		if c, ok := byRegion[r]; ok {
			out = append(out, RegionStats{Region: r, Unrouted: c.unrouted, Total: c.unrouted})
		}
	}
	return out
}
