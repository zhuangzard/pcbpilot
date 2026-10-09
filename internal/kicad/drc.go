package kicad

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Violation is one kicad-cli DRC item in pcbpilot's flat shape (the fields
// of internal/app's drcFlatViolation): position in mil, y-up.
type Violation struct {
	Rule     string   `json:"rule"`              // KiCad type, e.g. "clearance", "unconnected_items"
	ObjType  string   `json:"objType,omitempty"` // e.g. "Track to Via"
	Severity string   `json:"severity,omitempty"`
	Net      string   `json:"net,omitempty"`
	X        *float64 `json:"x,omitempty"`
	Y        *float64 `json:"y,omitempty"`
	Layer    string   `json:"layer,omitempty"`
	Objs     []string `json:"objs,omitempty"` // item uuids
	Message  string   `json:"message,omitempty"`
	Section  string   `json:"section"` // violations | unconnected_items | schematic_parity
}

// DRCReport is a parsed `kicad-cli pcb drc --format json` report.
type DRCReport struct {
	KiCadVersion string         `json:"kicadVersion,omitempty"`
	Units        string         `json:"coordinateUnits,omitempty"`
	Total        int            `json:"total"`
	Counts       map[string]int `json:"counts"`
	Violations   []Violation    `json:"violations"`
}

type drcJSONItem struct {
	Description string `json:"description"`
	Pos         *struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	} `json:"pos"`
	UUID string `json:"uuid"`
}

type drcJSONViolation struct {
	Description string        `json:"description"`
	Severity    string        `json:"severity"`
	Type        string        `json:"type"`
	Items       []drcJSONItem `json:"items"`
}

var (
	reItemNet   = regexp.MustCompile(`\[([^\]]*)\]`)
	reItemLayer = regexp.MustCompile(` on ([A-Za-z0-9_.]+\.Cu)`)
	reItemKind  = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9 ]*?)(?: \[| of | on |,|$)`)
)

// ParseDRC reads a kicad-cli DRC JSON report (any severities it holds).
func ParseDRC(data []byte) (*DRCReport, error) {
	var r struct {
		Version     string             `json:"kicad_version"`
		Units       string             `json:"coordinate_units"`
		Violations  []drcJSONViolation `json:"violations"`
		Unconnected []drcJSONViolation `json:"unconnected_items"`
		Parity      []drcJSONViolation `json:"schematic_parity"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parse kicad DRC report: %w", err)
	}
	scale := 1 / 0.0254 // mm → mil
	switch strings.ToLower(r.Units) {
	case "mils", "mil":
		scale = 1
	case "in", "inch", "inches":
		scale = 1000
	}
	rep := &DRCReport{KiCadVersion: r.Version, Units: r.Units, Counts: map[string]int{}}
	add := func(section string, vs []drcJSONViolation) {
		for _, v := range vs {
			fv := Violation{Rule: v.Type, Severity: v.Severity, Message: v.Description, Section: section}
			if fv.Rule == "" {
				fv.Rule = section
			}
			var kinds, nets []string
			for _, it := range v.Items {
				if it.UUID != "" {
					fv.Objs = append(fv.Objs, it.UUID)
				}
				if fv.X == nil && it.Pos != nil {
					x, y := it.Pos.X*scale, -it.Pos.Y*scale
					fv.X, fv.Y = &x, &y
				}
				if m := reItemKind.FindStringSubmatch(it.Description); m != nil {
					kinds = append(kinds, strings.TrimSpace(m[1]))
				}
				if m := reItemNet.FindStringSubmatch(it.Description); m != nil && m[1] != "" && !contains(nets, m[1]) {
					nets = append(nets, m[1])
				}
				if fv.Layer == "" {
					if m := reItemLayer.FindStringSubmatch(it.Description); m != nil {
						fv.Layer = m[1]
					}
				}
			}
			fv.ObjType = strings.Join(kinds, " to ")
			fv.Net = strings.Join(nets, " / ")
			// The item descriptions carry what the summary line does not.
			for _, it := range v.Items {
				fv.Message += "; " + it.Description
			}
			rep.Violations = append(rep.Violations, fv)
			rep.Counts[fv.Rule]++
		}
	}
	add("violations", r.Violations)
	add("unconnected_items", r.Unconnected)
	add("schematic_parity", r.Parity)
	rep.Total = len(rep.Violations)
	return rep, nil
}

// SortedCounts lists "rule: n" lines in name order.
func (r *DRCReport) SortedCounts() []string {
	out := make([]string, 0, len(r.Counts))
	for k, v := range r.Counts {
		out = append(out, fmt.Sprintf("%s: %d", k, v))
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// SplitErrors returns the error-severity part of a report (the gate) and
// the warning counts by rule (reported, not gated).
func (r *DRCReport) SplitErrors() (*DRCReport, map[string]int, int) {
	errs := &DRCReport{KiCadVersion: r.KiCadVersion, Units: r.Units, Counts: map[string]int{}}
	warn := map[string]int{}
	n := 0
	for _, v := range r.Violations {
		if v.Severity == "" || v.Severity == "error" {
			errs.Violations = append(errs.Violations, v)
			errs.Counts[v.Rule]++
			continue
		}
		warn[v.Rule]++
		n++
	}
	errs.Total = len(errs.Violations)
	return errs, warn, n
}
