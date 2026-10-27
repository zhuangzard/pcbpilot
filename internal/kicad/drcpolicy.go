package kicad

// drcpolicy.go — how pcbpilot judges what a project tells KiCad's DRC not to
// report (its .kicad_pro drc_exclusions and rule_severities). One table and
// one evaluation shared by the kicad route kicad-drc gate, `kicad drc` and
// `kicad fab --preflight`.

import (
	"fmt"
	"strings"
)

// drcHardRules are the electrical and fabrication DRC rules a project may
// not silence on a routed board (exclusion, "ignore", or a downgrade of an
// error to "warning"): shorts, the clearance family, unconnected items,
// schematic parity, copper / drill size rules, thermals, zones, solder mask
// bridges, custom disallow rules and the outline / layer / hole checks a
// fab builds from. Silencing one fails the gate unless a signed waiver
// matches.
var drcHardRules = map[string]bool{
	"shorting_items": true, "tracks_crossing": true,
	"clearance": true, "hole_clearance": true, "copper_edge_clearance": true, "creepage": true, "hole_to_hole": true, "edge_clearance": true,
	"unconnected_items": true,
	"schematic_parity":  true, "missing_footprint": true, "extra_footprint": true, "duplicate_footprints": true,
	"net_conflict": true, "footprint_symbol_mismatch": true,
	"track_width": true, "via_diameter": true, "drill_out_of_range": true, "annular_width": true, "connection_width": true,
	"microvia_drill_out_of_range": true,
	"starved_thermal":             true, "zones_intersect": true, "solder_mask_bridge": true,
	"items_not_allowed": true, "through_hole_pad_without_hole": true, "invalid_outline": true,
	"item_on_disabled_layer": true, "track_on_post_machined_layer": true,
	// Warnings by KiCad's default, but evidence a fab or the parity check
	// needs: they gate at warning severity, so silencing them fails too.
	"copper_sliver": true, "holes_co_located": true, "footprint_symbol_field_mismatch": true,
}

// drcSoftRules are the other rules KiCad 10 reports as errors by default
// (placement, courtyard, length / skew tuning, library): silencing one is
// listed as "silenced" but does not fail the gate.
var drcSoftRules = map[string]bool{
	"courtyards_overlap": true, "diff_pair_gap_out_of_range": true, "diff_pair_uncoupled_length_too_long": true,
	"footprint": true, "length_out_of_range": true, "malformed_courtyard": true, "npth_inside_courtyard": true,
	"pth_inside_courtyard": true, "overlapping_pads": true, "skew_out_of_range": true, "text_on_edge_cuts": true,
	"too_many_vias": true, "track_angle": true, "track_segment_length": true, "unresolved_variable": true,
}

// kicadWarningRules are the rules KiCad 10 does not report as errors by
// default (warning or ignore in a fresh project's rule_severities). Those
// not in drcHardRules are cosmetic or advisory: silencing them is info.
var kicadWarningRules = map[string]bool{
	"connection_width": true, "copper_sliver": true, "footprint_symbol_field_mismatch": true, "hole_to_hole": true,
	"holes_co_located": true, "isolated_copper": true, "lib_footprint_issues": true, "lib_footprint_mismatch": true,
	"mirrored_text_on_front_layer": true, "missing_tuning_profile": true, "nonmirrored_text_on_back_layer": true,
	"padstack": true, "silk_edge_clearance": true, "silk_over_copper": true, "silk_overlap": true, "text_height": true,
	"text_thickness": true, "track_dangling": true, "via_dangling": true,
	"footprint_filters_mismatch": true, "footprint_type_mismatch": true, "missing_courtyard": true,
	"track_not_centered_on_via": true, "tuning_profile_track_geometries": true,
	// Parity checks: warnings by KiCad's default (and hard rules here).
	"missing_footprint": true, "extra_footprint": true, "duplicate_footprints": true, "net_conflict": true,
	"footprint_symbol_mismatch": true,
}

// DRCRuleClass: "hard" (fails unless waived), "soft" (listed as silenced),
// or "info". A rule in none of the tables is hard (fail closed).
func DRCRuleClass(rule string) string {
	switch {
	case drcHardRules[rule]:
		return "hard"
	case drcSoftRules[rule]:
		return "soft"
	case kicadWarningRules[rule]:
		return "info"
	}
	return "hard"
}

// DRCGatesWarning: a warning-severity violation of rule still gates — the
// electrical / fabrication rules (DRCRuleClass "hard": copper slivers and
// co-located holes included, unknown rules too — fail closed). The same
// class decides the policy, so a rule that gates as a warning also fails
// when the project excludes or ignores it.
func DRCGatesWarning(rule string) bool { return DRCRuleClass(rule) == "hard" }

// KiCadWarningRule: KiCad 10 does not report rule as an error by default.
func KiCadWarningRule(rule string) bool { return kicadWarningRules[rule] }

// DRCSilenced is one error-level check the project silences without
// failing the gate.
type DRCSilenced struct {
	Rule  string `json:"rule"`
	How   string `json:"how"` // excluded | ignored | downgraded
	Where string `json:"where"`
}

// DRCPolicyResult is a project's DRC policy as a gate sees it.
type DRCPolicyResult struct {
	// Hard: silenced electrical / fabrication checks, one gate item each
	// ("EXCLUDED <key>…", "IGNORED <rule>…", "DOWNGRADED <rule>…"); only
	// a signed kicad-drc waiver accepts one.
	Hard []string `json:"hard,omitempty"`
	// Silenced: soft rules the project silences (warned, not failing).
	Silenced []DRCSilenced `json:"silenced,omitempty"`
	// Info: "SILENCED …" lines of the soft rules and the warning-level
	// rules' lines, in policy order.
	Info []string `json:"info,omitempty"`
}

// EvalDRCPolicy judges pol: an exclusion, an "ignore" or a downgrade of a
// KiCad error to "warning" of a hard rule is a Hard item; of a soft rule it
// is Silenced (and an info line); of a warning-level rule it is info.
// excluded are the report's excluded items (kicad-cli --severity-all), for
// their descriptions. A nil pol is nil.
func EvalDRCPolicy(pol *DRCPolicy, excluded []Violation) *DRCPolicyResult {
	if pol == nil {
		return nil
	}
	where := map[string]string{} // rule|first uuid -> description
	for _, v := range excluded {
		if len(v.Objs) > 0 {
			where[v.Rule+"|"+v.Objs[0]] = v.Message
		}
	}
	res := &DRCPolicyResult{}
	put := func(rule, how, at, it string) {
		switch DRCRuleClass(rule) {
		case "hard":
			res.Hard = append(res.Hard, it)
		case "soft":
			res.Silenced = append(res.Silenced, DRCSilenced{Rule: rule, How: how, Where: at})
			res.Info = append(res.Info, "SILENCED "+it)
		default:
			res.Info = append(res.Info, it)
		}
	}
	for _, e := range pol.Exclusions {
		desc := ""
		if parts := strings.Split(e.Key, "|"); len(parts) >= 4 {
			desc = where[e.Rule+"|"+parts[3]]
		}
		it := "EXCLUDED " + e.Key
		if e.Comment != "" {
			it += fmt.Sprintf(" (comment: %q)", e.Comment)
		}
		at := e.Key
		if desc != "" {
			it += ": " + desc
			at += ": " + desc
		}
		put(e.Rule, "excluded", at, it+" (.kicad_pro drc_exclusions)")
	}
	for _, rule := range pol.Ignored {
		put(rule, "ignored", ".kicad_pro rule_severities", fmt.Sprintf("IGNORED %s (.kicad_pro rule_severities: ignore; KiCad's DRC does not report it at all)", rule))
	}
	for _, rule := range pol.Downgraded {
		// Only a rule KiCad itself reports as an error: "warning" is the
		// default of the warning-level rules (parity checks included).
		if !kicadWarningRules[rule] {
			put(rule, "downgraded", ".kicad_pro rule_severities", fmt.Sprintf("DOWNGRADED %s (.kicad_pro rule_severities: warning; the gate only counts errors)", rule))
		}
	}
	return res
}

// HardDetail is the gate-detail sentence for the Hard items ("" if none).
func (r *DRCPolicyResult) HardDetail() string {
	if r == nil || len(r.Hard) == 0 {
		return ""
	}
	return fmt.Sprintf("%d electrical / fabrication DRC check(s) excluded, ignored or downgraded by the project (sign a kicad-drc waiver to accept one)", len(r.Hard))
}

// SilencedDetail is the gate-detail warning for the Silenced rules.
func (r *DRCPolicyResult) SilencedDetail() string {
	if r == nil || len(r.Silenced) == 0 {
		return ""
	}
	var rules []string
	seen := map[string]bool{}
	for _, s := range r.Silenced {
		if k := s.Rule + " (" + s.How + ")"; !seen[k] {
			seen[k] = true
			rules = append(rules, k)
		}
	}
	return fmt.Sprintf("WARNING: %d other error-level check(s) silenced by the project: %s", len(r.Silenced), strings.Join(rules, ", "))
}

// Ignores reports whether the policy turns rule off ("ignore"): KiCad's DRC
// then never runs it, so a clean report says nothing about it.
func (p *DRCPolicy) Ignores(rule string) bool {
	if p == nil {
		return false
	}
	return contains(p.Ignored, rule)
}
