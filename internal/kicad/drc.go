package kicad

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	// Excluded: the board's drc_exclusions cover the item (kicad-cli
	// reports it only with --severity-all / --severity-exclusions);
	// Comment is the exclusion's note.
	Excluded bool   `json:"excluded,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// DRCReport is a parsed `kicad-cli pcb drc --format json` report.
type DRCReport struct {
	KiCadVersion string         `json:"kicadVersion,omitempty"`
	Units        string         `json:"coordinateUnits,omitempty"`
	Total        int            `json:"total"`
	Counts       map[string]int `json:"counts"`
	Violations   []Violation    `json:"violations"`
	// ExcludedItems are the items of any section the project's
	// drc_exclusions cover: never in Violations, Total or Counts; callers
	// judge them by the project's DRCPolicy.
	ExcludedItems []Violation `json:"excludedItems,omitempty"`
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
	Excluded    bool          `json:"excluded"`
	Comment     string        `json:"comment"`
}

var (
	reItemNet   = regexp.MustCompile(`\[([^\]]*)\]`)
	reItemLayer = regexp.MustCompile(` on ([A-Za-z0-9_.]+\.Cu)`)
	reItemKind  = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9 ]*?)(?: \[| of | on |,|$)`)
)

// ErrDRCReport marks a DRC report that cannot be checked: empty, truncated,
// not JSON, or not the kicad-cli schema. Callers must treat it as "DRC did
// not run", never as a clean board.
var ErrDRCReport = errors.New("cannot check kicad-cli DRC report")

// DRCParseOptions names the sections a report must carry beyond the two
// kicad-cli always writes (violations, unconnected_items).
type DRCParseOptions struct {
	SchematicParity bool // the run passed --schematic-parity
}

// ParseDRC reads a kicad-cli DRC JSON report (any severities it holds).
// It is ParseDRCWith without --schematic-parity.
func ParseDRC(data []byte) (*DRCReport, error) { return ParseDRCWith(data, DRCParseOptions{}) }

// ParseDRCWith parses strictly: a required section that is missing, null or
// not an array, an entry that is not an object, or unknown coordinate units
// is an ErrDRCReport — an empty list is the only way a section reads clean.
func ParseDRCWith(data []byte, o DRCParseOptions) (*DRCReport, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s (fix: rerun kicad-cli pcb drc --format json; do not edit the report)", ErrDRCReport, fmt.Sprintf(format, a...))
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, bad("empty file")
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, bad("not a JSON object: %v", err)
	}
	if top == nil {
		return nil, bad("not a JSON object: null")
	}
	need := []string{"violations", "unconnected_items"}
	if o.SchematicParity {
		need = append(need, "schematic_parity")
	}
	sections := map[string][]drcJSONViolation{}
	for _, key := range []string{"violations", "unconnected_items", "schematic_parity"} {
		raw, ok := top[key]
		if !ok {
			if contains(need, key) {
				return nil, bad("no %q section", key)
			}
			continue
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '[' {
			return nil, bad("%q is %s, not an array", key, jsonKind(raw))
		}
		vs, err := decodeDRCEntries(raw)
		if err != nil {
			return nil, bad("%q %v", key, err)
		}
		sections[key] = vs
	}
	var r struct {
		Version string `json:"kicad_version"`
		Units   string `json:"coordinate_units"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, bad("header: %v", err)
	}
	scale := 1 / 0.0254 // mm → mil
	switch strings.ToLower(r.Units) {
	case "", "mm":
	case "mils", "mil":
		scale = 1
	case "in", "inch", "inches":
		scale = 1000
	default:
		return nil, bad("unknown coordinate_units %q", r.Units)
	}
	rep := &DRCReport{KiCadVersion: r.Version, Units: r.Units, Counts: map[string]int{}}
	add := func(section string, vs []drcJSONViolation) {
		for _, v := range vs {
			fv := Violation{Rule: v.Type, Severity: v.Severity, Message: v.Description, Section: section,
				Excluded: v.Excluded || v.Severity == "exclusion", Comment: v.Comment}
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
			if fv.Excluded {
				rep.ExcludedItems = append(rep.ExcludedItems, fv)
				continue
			}
			rep.Violations = append(rep.Violations, fv)
			rep.Counts[fv.Rule]++
		}
	}
	add("violations", sections["violations"])
	add("unconnected_items", sections["unconnected_items"])
	add("schematic_parity", sections["schematic_parity"])
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

// decodeDRCEntries decodes one section, entry by entry: each must be an
// object with string type, description and severity and an items array of
// objects (kicad-cli's drc.v1 schema). A null or partial entry would decode
// as a blank violation and could be miscounted, so it is rejected instead.
func decodeDRCEntries(raw []byte) ([]drcJSONViolation, error) {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("entries: %v", err)
	}
	out := make([]drcJSONViolation, 0, len(entries))
	for i, e := range entries {
		e = bytes.TrimSpace(e)
		if len(e) == 0 || e[0] != '{' {
			return nil, fmt.Errorf("entry %d is %s, not an object", i, jsonKind(e))
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(e, &fields); err != nil {
			return nil, fmt.Errorf("entry %d: %v", i, err)
		}
		for _, k := range []string{"type", "description", "severity"} {
			if v := bytes.TrimSpace(fields[k]); len(v) == 0 || v[0] != '"' {
				return nil, fmt.Errorf("entry %d: %q is %s, not a string", i, k, jsonKind(v))
			}
		}
		var items []json.RawMessage
		if v := bytes.TrimSpace(fields["items"]); len(v) == 0 || v[0] != '[' {
			return nil, fmt.Errorf("entry %d: %q is %s, not an array", i, "items", jsonKind(v))
		} else if err := json.Unmarshal(v, &items); err != nil {
			return nil, fmt.Errorf("entry %d items: %v", i, err)
		}
		for j, it := range items {
			if err := checkDRCItem(it); err != nil {
				return nil, fmt.Errorf("entry %d item %d %v", i, j, err)
			}
		}
		var v drcJSONViolation
		if err := json.Unmarshal(e, &v); err != nil {
			return nil, fmt.Errorf("entry %d: %v", i, err)
		}
		if !drcSeverities[v.Severity] {
			return nil, fmt.Errorf("entry %d: unknown severity %q", i, v.Severity)
		}
		out = append(out, v)
	}
	return out, nil
}

// drcSeverities are the severities kicad-cli 10 writes into a DRC report
// (the values of its --severity-error/-warning/-exclusion flags, and of the
// report's included_severities).
var drcSeverities = map[string]bool{"error": true, "warning": true, "exclusion": true}

// checkDRCItem: an item is an object with a string description and uuid and
// a pos object with numeric x and y, as kicad-cli 10 writes every item.
func checkDRCItem(raw json.RawMessage) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return fmt.Errorf("is %s, not an object", jsonKind(raw))
	}
	var it map[string]json.RawMessage
	if err := json.Unmarshal(raw, &it); err != nil {
		return err
	}
	for _, k := range []string{"description", "uuid"} {
		if v := bytes.TrimSpace(it[k]); len(v) == 0 || v[0] != '"' {
			return fmt.Errorf("%q is %s, not a string", k, jsonKind(v))
		}
	}
	var pos map[string]json.RawMessage
	if v := bytes.TrimSpace(it["pos"]); len(v) == 0 || v[0] != '{' {
		return fmt.Errorf("%q is %s, not an object", "pos", jsonKind(v))
	} else if err := json.Unmarshal(v, &pos); err != nil {
		return err
	}
	for _, k := range []string{"x", "y"} {
		if v := bytes.TrimSpace(pos[k]); jsonKind(v) != "a number" {
			return fmt.Errorf("pos %q is %s, not a number", k, jsonKind(v))
		}
	}
	return nil
}

// jsonKind names a raw JSON value's type for an error message.
func jsonKind(raw []byte) string {
	if len(raw) == 0 {
		return "empty"
	}
	switch raw[0] {
	case '{':
		return "an object"
	case '"':
		return "a string"
	case 't', 'f':
		return "a boolean"
	case 'n':
		return "null"
	case '[':
		return "an array"
	}
	return "a number"
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

// Excluded lists the report's excluded items, from every section.
func (r *DRCReport) Excluded() []Violation { return r.ExcludedItems }

// DRCExclusion is one board.design_settings.drc_exclusions entry of a
// .kicad_pro: "<rule>|<x>|<y>|<uuid>|<uuid>" and its comment.
type DRCExclusion struct {
	Rule    string `json:"rule"`
	Key     string `json:"key"`
	Comment string `json:"comment,omitempty"`
}

// DRCPolicy is what a project tells KiCad's DRC not to report: its
// exclusions and the rules whose severity is "ignore".
type DRCPolicy struct {
	Exclusions []DRCExclusion `json:"exclusions,omitempty"`
	Ignored    []string       `json:"ignored,omitempty"`
	// Downgraded: rules the project sets to "warning".
	Downgraded []string `json:"downgraded,omitempty"`
}

// ReadDRCPolicy reads the DRC policy of a .kicad_pro. drc_exclusions is a
// list of keys (KiCad 6–8) or of [key, comment] pairs (KiCad 9+).
func ReadDRCPolicy(pro string) (*DRCPolicy, error) {
	data, err := os.ReadFile(pro)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Board struct {
			DesignSettings struct {
				Exclusions []json.RawMessage `json:"drc_exclusions"`
				Severities map[string]string `json:"rule_severities"`
			} `json:"design_settings"`
		} `json:"board"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", pro, err)
	}
	p := &DRCPolicy{}
	for i, e := range raw.Board.DesignSettings.Exclusions {
		var key, comment string
		var pair []string
		if err := json.Unmarshal(e, &key); err != nil {
			if err := json.Unmarshal(e, &pair); err != nil || len(pair) == 0 {
				return nil, fmt.Errorf("%s: drc_exclusions[%d] is neither a key nor a [key, comment] pair", pro, i)
			}
			key = pair[0]
			if len(pair) > 1 {
				comment = pair[1]
			}
		}
		rule, _, _ := strings.Cut(key, "|")
		p.Exclusions = append(p.Exclusions, DRCExclusion{Rule: rule, Key: key, Comment: comment})
	}
	for rule, sev := range raw.Board.DesignSettings.Severities {
		switch strings.ToLower(sev) {
		case "ignore":
			p.Ignored = append(p.Ignored, rule)
		case "warning":
			p.Downgraded = append(p.Downgraded, rule)
		}
	}
	sort.Strings(p.Ignored)
	sort.Strings(p.Downgraded)
	return p, nil
}
