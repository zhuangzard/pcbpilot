package app

// kicad_sch_build_watchdog.go — the layout-quality gate of `kicad sch-build`
// (the layout watchdog). A drawing that passes every correctness gate can
// still be useless to read: zones whose parts are joined by labels only,
// passives floating away from the pins they serve, sheets mostly blank. Such a
// build used to be reported as a success. The watchdog measures every zone,
// page and the whole board after layout; layoutZones already retries the
// other drawings of a zone that misses a zone limit (the legacy grid
// variants), and what still misses a limit fails the build with
// LAYOUT_QUALITY: per zone / page the metric, its value, the limit and an
// executable fix (near hints, a zone split, a page move). --layout-quality
// advisory only reports, and needs a signed waiver like --no-review.

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbLQLimits are the watchdog's limits, in one place.
//
// Calibrated on the seven field boards (135–804
// parts, 2026-11-12), --no-planner, measured by scripts/schmetrics on the
// sheets and by sbZoneQualityOf on the layouts (the same counts):
//
//	                         v0.12.8 (before)        compact grid (round 2)
//	zone label share ≥ 0.9   8 zones on 4 boards     0
//	zone attached < 0.2      13 zones on 5 boards    0
//	longest wire             max 64.8 mm             max 62.2 mm
//	page fill                min 0.29 (board C)          min 0.35 (board C, A3)
//	board label share        0.41–0.78               0.22–0.42
//	board attached share     0.27–0.75               0.76–0.97
//
// Every limit sits between the two columns, closer to the drawings people
// rejected ("too scattered, mostly blank, everything a label"). Round 2
// raised the page fill limit 0.30 -> 0.33 (the v0.12.7 field outputs have
// pages at 0.20-0.31).
type sbLQLimits struct {
	// a zone with at least ZoneMinConns in-zone signal connections fails
	// when this share of them is made by labels (not wires): "everything a
	// label"
	ZoneLabelShareMax float64
	ZoneMinConns      int
	// a zone with at least ZoneMinSmall small parts (≤ 3 pins, on a signal
	// net) fails when less than this share is wired to another part
	ZoneAttachedMin float64
	ZoneMinSmall    int
	// no single wire segment longer than this (a part far from its pin)
	LongestWireMax float64
	// a sheet larger than A4 whose content box covers less than this share
	// of the drawing area of its paper (the engineer-grade fill ratio) is
	// mostly blank — unless it holds one zone, or one of its zones alone
	// needs that paper
	PageFillMin float64
	// whole board, in-zone connections: label share at most, attached
	// share at least
	BoardLabelShareMax float64
	BoardAttachedMin   float64
	// the board limits apply from this many in-zone signal connections /
	// small parts on (a handful of parts proves nothing either way)
	BoardMinConns, BoardMinSmall int
}

var sbLayoutQualityLimits = sbLQLimits{
	ZoneLabelShareMax:  0.90,
	ZoneMinConns:       8,
	ZoneAttachedMin:    0.20,
	ZoneMinSmall:       8,
	LongestWireMax:     100,
	PageFillMin:        0.33,
	BoardLabelShareMax: 0.55,
	BoardAttachedMin:   0.60,
	BoardMinConns:      20,
	BoardMinSmall:      20,
}

// sbLQIssue is one missed limit.
type sbLQIssue struct {
	Scope  string  `json:"scope"` // zone | page | board
	ID     string  `json:"id"`
	Page   string  `json:"page,omitempty"`
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Limit  float64 `json:"limit"`
	Fix    string  `json:"fix"`
}

func (i sbLQIssue) String() string {
	cmp := "<"
	switch i.Metric {
	case "signalLabelShare", "longestWireMm":
		cmp = ">"
	}
	return fmt.Sprintf("%s %s: %s %.2f %s %.2f", i.Scope, i.ID, i.Metric, i.Value, cmp, i.Limit)
}

// sbZoneLQ is one zone's watchdog record.
type sbZoneLQ struct {
	Zone       string  `json:"zone"`
	Page       string  `json:"page"`
	Variant    string  `json:"variant,omitempty"`
	LabelShare float64 `json:"signalLabelShare"`
	Attached   float64 `json:"attachedShare"`
	SigConns   int     `json:"signalConnections"`
	Small      int     `json:"smallParts"`
	LongestMM  float64 `json:"longestWireMm"`
	Ms         float64 `json:"ms,omitempty"`
}

// sbSlowZone is a zone that ran past the per-zone layout time limit.
type sbSlowZone struct {
	Zone    string  `json:"zone"`
	Ms      float64 `json:"ms"`
	LimitMs float64 `json:"limitMs"`
	Kept    string  `json:"kept"` // the drawing kept: method / grid variant
	Skipped int     `json:"skippedVariants"`
}

// sbZoneLQIssues checks one zone against the zone limits.
func sbZoneLQIssues(q sbZoneQuality, lim sbLQLimits) []string {
	var out []string
	if q.SigConns >= lim.ZoneMinConns && q.LabelShare() >= lim.ZoneLabelShareMax {
		out = append(out, "signalLabelShare")
	}
	if q.Small >= lim.ZoneMinSmall && q.AttachedShare() < lim.ZoneAttachedMin {
		out = append(out, "attachedShare")
	}
	if q.LongestMM > lim.LongestWireMax {
		out = append(out, "longestWireMm")
	}
	return out
}

// sbNearHints suggests a `near` hint for each small part of the zone that
// no wire joins to another part: a pin of a bigger part of the zone on one
// of its signal nets ("R12 near U3:PA1").
func sbNearHints(d *sbDesign, zi *sbZoneIn, unwired []string, max int) []string {
	var out []string
	for _, ref := range unwired {
		p := d.ByRef[ref]
		if p == nil {
			continue
		}
		hint := ""
		for _, q := range p.LPins {
			n, ok := d.PinNet[sbPinKey(ref, q.Number)]
			if !ok || d.NetKind[n] == "power" || d.NetKind[n] == "ground" {
				continue
			}
			for _, o := range zi.Parts {
				if o.Ref == ref || len(o.LPins) <= 3 {
					continue
				}
				for _, oq := range o.LPins {
					if d.PinNet[sbPinKey(o.Ref, oq.Number)] == n {
						name := oq.Name
						if name == "" || name == "~" {
							name = oq.Number
						}
						hint = fmt.Sprintf("%s near %s:%s", ref, o.Ref, name)
						break
					}
				}
				if hint != "" {
					break
				}
			}
			if hint != "" {
				break
			}
		}
		if hint != "" {
			out = append(out, hint)
		}
		if len(out) >= max {
			break
		}
	}
	return out
}

// sbLayoutQualityGate measures the drawn zones and pages against the limits.
// egPages: the engineer-grade gate's pages (fill); papers: sheet file →
// paper; mode: strict (a missed limit fails) | advisory (warn).
func sbLayoutQualityGate(d *sbDesign, zis []*sbZoneIn, layouts map[string]*sbZoneLayout, egPages []sbEGPage, papers map[string]string, mode string, zoneLimit time.Duration, lim sbLQLimits) (sbGate, []sbLQIssue) {
	m := &sbMeasure{cache: map[string]kicad.SceneSymbol{}}
	var issues []sbLQIssue
	var zones []sbZoneLQ
	var slow []sbSlowZone
	var board sbZoneQuality
	type zoneLoad struct {
		id     string
		labels int
	}
	var loads []zoneLoad
	pageZones := map[string][]string{}
	for _, zi := range zis {
		zl := layouts[zi.Zone.ID]
		if zl == nil {
			continue
		}
		pageZones[zi.Zone.Page] = append(pageZones[zi.Zone.Page], zi.Zone.ID)
		q := sbZoneQualityOf(d, zi, zl, m)
		board.SigConns += q.SigConns
		board.SigWired += q.SigWired
		board.Small += q.Small
		board.SmallWired += q.SmallWired
		loads = append(loads, zoneLoad{zi.Zone.ID, q.SigConns - q.SigWired + q.Small - q.SmallWired})
		zones = append(zones, sbZoneLQ{Zone: zi.Zone.ID, Page: zi.Zone.Page, Variant: sbFirst(zl.Variant, zl.Method),
			LabelShare: sbRound2(q.LabelShare()), Attached: sbRound2(q.AttachedShare()), SigConns: q.SigConns, Small: q.Small,
			LongestMM: sbRound2(q.LongestMM), Ms: zl.Ms})
		if zl.TimedOut {
			slow = append(slow, sbSlowZone{Zone: zi.Zone.ID, Ms: zl.Ms, LimitMs: float64(zoneLimit.Milliseconds()), Kept: sbFirst(zl.Variant, zl.Method), Skipped: zl.Skipped})
		}
		hints := sbNearHints(d, zi, q.Unwired, 8)
		split := fmt.Sprintf("split zone %s into one zone per IC cluster in the spec (zones[] + parts[].zone), or try --auto-split", zi.Zone.ID)
		nearFix := split
		if len(hints) > 0 {
			nearFix = "add near hints in parts[]: " + strings.Join(hints, ", ") + "; or " + split
		}
		for _, metric := range sbZoneLQIssues(q, lim) {
			is := sbLQIssue{Scope: "zone", ID: zi.Zone.ID, Page: zi.Zone.Page, Metric: metric}
			switch metric {
			case "signalLabelShare":
				is.Value, is.Limit, is.Fix = q.LabelShare(), lim.ZoneLabelShareMax, nearFix
			case "attachedShare":
				is.Value, is.Limit, is.Fix = q.AttachedShare(), lim.ZoneAttachedMin, nearFix
			case "longestWireMm":
				is.Value, is.Limit = q.LongestMM, lim.LongestWireMax
				is.Fix = fmt.Sprintf("a part sits far from the pin it is wired to: give it a near hint to that pin, or split zone %s", zi.Zone.ID)
			}
			is.Value, is.Limit = sbRound2(is.Value), sbRound2(is.Limit)
			issues = append(issues, is)
		}
	}
	// pages
	for _, pg := range egPages {
		// A4 is the smallest paper: a small circuit on it cannot fill it,
		// and no other paper would hold it with less blank
		if pg.FillRatio >= lim.PageFillMin || papers[pg.Sheet] == "" || papers[pg.Sheet] == kicad.Papers[0].Name {
			continue
		}
		id := strings.TrimSuffix(pg.Sheet, ".kicad_sch")
		for _, p := range d.Pages {
			if sbSheetFile(p.ID) == pg.Sheet {
				id = p.ID
			}
		}
		if len(d.Pages) == 1 { // a single page is drawn on the root sheet
			id = d.Pages[0].ID
		}
		zs := pageZones[id]
		sort.Strings(zs)
		if sbPagePaperForced(zs, layouts, papers[pg.Sheet]) {
			continue // its biggest zone alone needs this paper: no repack helps
		}
		issues = append(issues, sbLQIssue{Scope: "page", ID: id, Metric: "fillRatio", Value: pg.FillRatio, Limit: lim.PageFillMin,
			Fix: fmt.Sprintf("sheet %s (%s) is mostly blank with zones %s: move them onto a neighbouring page (zones[].page) or move more zones onto this page; a page of one small zone wastes a sheet",
				pg.Sheet, papers[pg.Sheet], strings.Join(zs, ", "))})
	}
	// board
	sort.SliceStable(loads, func(i, j int) bool { return loads[i].labels > loads[j].labels })
	var worst []string
	for _, l := range loads {
		if l.labels > 0 && len(worst) < 5 {
			worst = append(worst, fmt.Sprintf("%s (%d)", l.id, l.labels))
		}
	}
	boardFix := "the zones with the most label joins / loose small parts: " + strings.Join(worst, ", ") +
		" — give their passives near hints to the IC pins they serve, or split those zones per IC cluster"
	if board.SigConns >= lim.BoardMinConns && board.LabelShare() > lim.BoardLabelShareMax {
		issues = append(issues, sbLQIssue{Scope: "board", ID: d.Spec.Name, Metric: "signalLabelShare", Value: sbRound2(board.LabelShare()), Limit: lim.BoardLabelShareMax, Fix: boardFix})
	}
	if board.Small >= lim.BoardMinSmall && board.AttachedShare() < lim.BoardAttachedMin {
		issues = append(issues, sbLQIssue{Scope: "board", ID: d.Spec.Name, Metric: "attachedShare", Value: sbRound2(board.AttachedShare()), Limit: lim.BoardAttachedMin, Fix: boardFix})
	}
	data := map[string]any{
		"mode":   mode,
		"limits": lim,
		"board": map[string]any{"signalLabelShare": sbRound2(board.LabelShare()), "attachedShare": sbRound2(board.AttachedShare()),
			"signalConnections": board.SigConns, "smallParts": board.Small},
		"zones":  zones,
		"issues": issues,
	}
	if len(slow) > 0 {
		data["slowZones"] = slow
	}
	var slowMsg []string
	for _, s := range slow {
		slowMsg = append(slowMsg, fmt.Sprintf("zone %s took %.1f s (limit %.0f s): kept %s, %d variant(s) not tried", s.Zone, s.Ms/1000, s.LimitMs/1000, s.Kept, s.Skipped))
	}
	head := fmt.Sprintf("board label share %.2f (≤ %.2f), attached %.2f (≥ %.2f), %d zone(s), %d page(s)",
		board.LabelShare(), lim.BoardLabelShareMax, board.AttachedShare(), lim.BoardAttachedMin, len(zones), len(egPages))
	if len(issues) == 0 {
		detail := head
		if len(slowMsg) > 0 {
			detail += "; " + strings.Join(slowMsg, "; ")
		}
		return sbGate{Name: "layout-quality", Status: "pass", Detail: detail, Data: data}, nil
	}
	var msgs []string
	for _, is := range issues {
		msgs = append(msgs, is.String())
	}
	msgs = append(msgs, slowMsg...)
	status := "fail"
	if mode == "advisory" {
		status = "warn"
	}
	return sbGate{Name: "layout-quality", Status: status, Detail: fmt.Sprintf("%d limit(s) missed: %s", len(issues), strings.Join(msgs, "; ")), Data: data}, issues
}

// sbPagePaperForced reports whether one zone frame of the page alone does
// not fit the drawing area of the next smaller paper: then the page cannot
// come down a size by packing, and its blank is the zone's own shape.
func sbPagePaperForced(zones []string, layouts map[string]*sbZoneLayout, paper string) bool {
	if len(zones) <= 1 {
		return true // one zone: its frame + PWR_FLAG row + title block set the paper
	}
	idx := -1
	for i, p := range kicad.Papers {
		if p.Name == paper {
			idx = i
		}
	}
	if idx <= 0 {
		return idx == 0
	}
	sm := kicad.Papers[idx-1]
	for _, z := range zones {
		if l := layouts[z]; l != nil {
			f := sbFootprint(l.Box, kicad.Pt{})
			if f.W() > sm.W-20-2*sbPackMargin || f.H() > sm.H-20-2*sbPackMargin {
				return true
			}
		}
	}
	return false
}

// sbLQFix is the error fix of a build the watchdog stopped.
func sbLQFix(issues []sbLQIssue) string {
	var out []string
	seen := map[string]bool{}
	for _, is := range issues {
		f := is.Scope + " " + is.ID + ": " + is.Fix
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return strings.Join(out, " | ")
}

func sbFirst(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// sbLayoutQualityMode checks --layout-quality: strict (default) or
// advisory, which needs a signed waiver {"gate":"layout-quality","match":
// "--layout-quality advisory"}.
func sbLayoutQualityMode(mode string, waivers []gateWaiver) (string, *sbFailure) {
	switch mode {
	case "", "strict":
		return "strict", nil
	case "advisory":
		if !hasWaiver(waivers, "layout-quality", sbLQAdvisoryMatch) {
			return "", &sbFailure{Code: "LAYOUT_QUALITY_WAIVER_REQUIRED", Message: "--layout-quality advisory reports a poor layout instead of failing the build",
				Fix: `add a signed waiver to --waivers: {"gate":"layout-quality","match":"--layout-quality advisory","reason":…,"by":…}, or drop --layout-quality advisory`}
		}
		return "advisory", nil
	}
	return "", &sbFailure{Code: "BAD_FLAG", Message: "--layout-quality must be strict or advisory, got " + mode, Fix: "use --layout-quality strict (default) or advisory (with a signed waiver)"}
}

const sbLQAdvisoryMatch = "--layout-quality advisory"
