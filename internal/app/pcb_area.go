package app

// pcb_area.go — resolve a closed-polygon area (for a keep-out region or a fill)
// from a friendly shorthand instead of hand-typed polygon coordinates. Three
// mutually-exclusive sources: --points (explicit JSON), --rect "x0,y0,x1,y1", or
// --ref <designator> (a placed component's rendered bbox). --margin expands the
// rect/ref box outward. This is what makes an antenna keep-out expressible as
// "under U1 + 40mil" — the #28 antenna-clearance ergonomics.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// rectCorners returns the 4 CCW corners of an axis-aligned rectangle (normalized).
func rectCorners(x0, y0, x1, y1 float64) [][]float64 {
	if x1 < x0 {
		x0, x1 = x1, x0
	}
	if y1 < y0 {
		y0, y1 = y1, y0
	}
	return [][]float64{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
}

// parseRectSpec parses "x0,y0,x1,y1" (mil) into two opposite corners.
func parseRectSpec(s string) (x0, y0, x1, y1 float64, err error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, fmt.Errorf("--rect must be 'x0,y0,x1,y1' (4 comma-separated mil values), got %q", s)
	}
	var v [4]float64
	for i, p := range parts {
		f, e := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if e != nil {
			return 0, 0, 0, 0, fmt.Errorf("--rect value %q is not a number", strings.TrimSpace(p))
		}
		v[i] = f
	}
	return v[0], v[1], v[2], v[3], nil
}

// componentBBox reads a placed component's rendered extent {minX,minY,maxX,maxY}
// (mil) from pcb.components.list --include-bbox, matched by designator.
func componentBBox(cfg *appConfig, window, ref string) (minX, minY, maxX, maxY float64, err error) {
	res, err := requestAction(cfg, "pcb.components.list", window, map[string]any{"includeBBox": true})
	if err != nil || res == nil {
		return 0, 0, 0, 0, fmt.Errorf("fetch PCB components for --ref %q: %v", ref, err)
	}
	comps, ok := mnav(res.Result, "components").([]any)
	if !ok {
		return 0, 0, 0, 0, fmt.Errorf("PCB component list has no 'components' array")
	}
	for _, c := range comps {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if s, _ := cm["designator"].(string); s != ref {
			continue
		}
		bb, ok := cm["bbox"].(map[string]any)
		if !ok {
			return 0, 0, 0, 0, fmt.Errorf("component %q has no bbox (unplaced?)", ref)
		}
		minX, _ = asFloatOK(bb["minX"])
		minY, _ = asFloatOK(bb["minY"])
		maxX, _ = asFloatOK(bb["maxX"])
		maxY, _ = asFloatOK(bb["maxY"])
		return minX, minY, maxX, maxY, nil
	}
	return 0, 0, 0, 0, fmt.Errorf("component %q not found on the PCB (check `pcbpilot pcb list`)", ref)
}

// areaPointsFrom resolves closed-polygon points from exactly one of --points /
// --rect / --ref. --margin (mil) expands the rect/ref box outward — a positive
// antenna clearance; it is ignored for --points (already an explicit polygon).
func areaPointsFrom(cfg *appConfig, window, pointsJSON, rectSpec, ref string, margin float64) ([][]float64, error) {
	n := 0
	for _, s := range []string{pointsJSON, rectSpec, ref} {
		if s != "" {
			n++
		}
	}
	if n == 0 {
		return nil, fmt.Errorf("one of --points / --rect / --ref is required")
	}
	if n > 1 {
		return nil, fmt.Errorf("--points, --rect and --ref are mutually exclusive")
	}
	switch {
	case pointsJSON != "":
		var pts [][]float64
		if err := json.Unmarshal([]byte(pointsJSON), &pts); err != nil {
			return nil, fmt.Errorf("invalid --points json (expected [[x,y],...]): %w", err)
		}
		if len(pts) < 3 {
			return nil, fmt.Errorf("--points needs >= 3 [x,y] pairs")
		}
		return pts, nil
	case rectSpec != "":
		x0, y0, x1, y1, err := parseRectSpec(rectSpec)
		if err != nil {
			return nil, err
		}
		return rectCorners(x0-margin, y0-margin, x1+margin, y1+margin), nil
	default:
		minX, minY, maxX, maxY, err := componentBBox(cfg, window, ref)
		if err != nil {
			return nil, err
		}
		return rectCorners(minX-margin, minY-margin, maxX+margin, maxY+margin), nil
	}
}
