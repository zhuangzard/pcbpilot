package app

// kicad_sch_build_split.go — opt-in automatic zone split (sch-build
// --auto-split) for zones the layout cannot draw cleanly.
//
// A dense functional zone on a field board (LDO + load switch + inverter +
// transistor + 14 passives) had no grid or planner drawing that passes the
// quality / EG-06 gates: labels, texts and decoupling caps fight for the same
// room. Users worked around it by hand with one zone per IC cluster. This
// file does the same automatically: a zone whose best layout still has
// findings (or none at all) and that holds at least two ICs is split into one
// sub-zone per IC with the passives that belong to it, the sub-zones are laid
// out, and the split is kept only when it is strictly better.
//
// Off by default: on a 2605-part board it improved text-over-symbol and
// EG-06 but added EG-08 crossings, one more over-full page and 9x the layout
// time. A better acceptance rule (no new crossings, page fit) is open.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbZoneICs lists the zone's ICs (parts with three or more pins) in the
// zone's part order (core first).
func sbZoneICs(zi *sbZoneIn) []*sbRPart {
	var ics []*sbRPart
	for _, p := range zi.Parts {
		if len(p.LPins) >= 3 {
			ics = append(ics, p)
		}
	}
	return ics
}

// sbClusterZone assigns every part of a zone to one of its ICs: ref → IC ref.
// nil when the zone has fewer than two ICs (nothing to split).
//
// Staged, strongest evidence first (reviewed by Codex / Kimi 2026-10-26):
//  1. a `near` hint naming an IC of the zone;
//  2. the IC sharing the most DISTINCT signal nets with the part;
//  3. propagation along signal nets through already-assigned passives,
//     synchronous rounds until nothing changes (order-independent; covers
//     e.g. an LDO feedback divider reached through another resistor);
//  4. supply evidence last: an IC with pins on the part's supply AND ground
//     net (a decoupling cap's pair), else on its supply net only;
//  5. anything left goes to the first IC (the zone core).
//
// Ties always break by IC order. A shared-rail decap may land with either IC
// on that rail; EG-06 only needs an IC with a pin on the rail in its zone.
func sbClusterZone(d *sbDesign, zi *sbZoneIn) map[string]string {
	ics := sbZoneICs(zi)
	if len(ics) < 2 {
		return nil
	}
	order := map[string]int{}
	for i, ic := range ics {
		order[ic.Ref] = i
	}
	inZone := map[string]bool{}
	for _, p := range zi.Parts {
		inZone[p.Ref] = true
	}
	assign := map[string]string{}
	for _, ic := range ics {
		assign[ic.Ref] = ic.Ref
	}
	// distinct nets of a part, and distinct in-zone refs on a net
	netsOf := func(p *sbRPart) []string {
		seen := map[string]bool{}
		var ns []string
		for _, q := range p.LPins {
			if n, ok := d.PinNet[sbPinKey(p.Ref, q.Number)]; ok && !seen[n] {
				seen[n] = true
				ns = append(ns, n)
			}
		}
		return ns
	}
	refsOn := func(n string) []string {
		seen := map[string]bool{}
		var rs []string
		for _, k := range d.netPins(n) {
			r, _, _ := strings.Cut(k, ".")
			if inZone[r] && !seen[r] {
				seen[r] = true
				rs = append(rs, r)
			}
		}
		return rs
	}
	isSignal := func(n string) bool { k := d.NetKind[n]; return k != "power" && k != "ground" }
	pick := func(score map[string]int) string {
		best, bestScore := "", 0
		for r, sc := range score {
			if sc > bestScore || (sc == bestScore && sc > 0 && order[r] < order[best]) {
				best, bestScore = r, sc
			}
		}
		return best
	}
	unassigned := func() []*sbRPart {
		var out []*sbRPart
		for _, p := range zi.Parts {
			if _, ok := assign[p.Ref]; !ok {
				out = append(out, p)
			}
		}
		return out
	}
	// 1. near hint
	for _, p := range unassigned() {
		if p.Near == "" {
			continue
		}
		if r, _, ok := sbSplitPin(p.Near); ok {
			if _, isIC := order[r]; isIC {
				assign[p.Ref] = r
			}
		}
	}
	// 2. direct signal nets to an IC
	for _, p := range unassigned() {
		score := map[string]int{}
		for _, n := range netsOf(p) {
			if !isSignal(n) {
				continue
			}
			for _, r := range refsOn(n) {
				if _, isIC := order[r]; isIC {
					score[r]++
				}
			}
		}
		if ic := pick(score); ic != "" {
			assign[p.Ref] = ic
		}
	}
	// 3. signal-net propagation through assigned passives, synchronous rounds
	for {
		next := map[string]string{}
		for _, p := range unassigned() {
			score := map[string]int{}
			for _, n := range netsOf(p) {
				if !isSignal(n) {
					continue
				}
				for _, r := range refsOn(n) {
					if ic, ok := assign[r]; ok && r != p.Ref {
						score[ic]++
					}
				}
			}
			if ic := pick(score); ic != "" {
				next[p.Ref] = ic
			}
		}
		if len(next) == 0 {
			break
		}
		for r, ic := range next {
			assign[r] = ic
		}
	}
	// 4. supply evidence: supply+ground pair first, then supply alone
	icHasNet := func(ic, net string) bool {
		for _, q := range d.ByRef[ic].LPins {
			if d.PinNet[sbPinKey(ic, q.Number)] == net {
				return true
			}
		}
		return false
	}
	for _, p := range unassigned() {
		var supplies, grounds []string
		for _, n := range netsOf(p) {
			switch d.NetKind[n] {
			case "power":
				supplies = append(supplies, n)
			case "ground":
				grounds = append(grounds, n)
			}
		}
		score := map[string]int{}
		for _, ic := range ics {
			for _, sn := range supplies {
				if !icHasNet(ic.Ref, sn) {
					continue
				}
				score[ic.Ref] += 2
				for _, gn := range grounds {
					if icHasNet(ic.Ref, gn) {
						score[ic.Ref] += 3
					}
				}
			}
		}
		if ic := pick(score); ic != "" {
			assign[p.Ref] = ic
		}
	}
	// 5. the rest stays with the core
	for _, p := range unassigned() {
		assign[p.Ref] = ics[0].Ref
	}
	return assign
}

// sbSubZoneID names the sub-zone of IC ref inside zone id.
func sbSubZoneID(zone, ic string) string { return zone + "_" + ic }

// sbSubZoneIDs allocates one sub-zone id per IC of zone, never reusing the id
// of another zone of the design (a spec may already have "psu_U1").
func sbSubZoneIDs(zones []sbZone, zone string, ics []*sbRPart) map[string]string {
	taken := map[string]bool{}
	for _, z := range zones {
		if z.ID != zone {
			taken[z.ID] = true
		}
	}
	ids := map[string]string{}
	for _, ic := range ics {
		id := sbSubZoneID(zone, ic.Ref)
		for n := 2; taken[id]; n++ {
			id = fmt.Sprintf("%s_%d", sbSubZoneID(zone, ic.Ref), n)
		}
		taken[id] = true
		ids[ic.Ref] = id
	}
	return ids
}

// sbAutoSplitZones splits every zone whose layout is missing or has findings
// into per-IC sub-zones, lays those out and keeps the split only when every
// sub-zone was drawn and the findings strictly drop. It edits d (zones, part
// zone ids) for the kept splits and returns the new zone inputs, the layouts
// and report notes.
func sbAutoSplitZones(d *sbDesign, zis []*sbZoneIn, layouts map[string]*sbZoneLayout, reuse map[string]*sbZoneLayout, lo sbLayoutOpts) ([]*sbZoneIn, []string) {
	var notes []string
	changed := false
	for _, zi := range zis {
		l := layouts[zi.Zone.ID]
		if l != nil && len(l.Findings) == 0 {
			continue
		}
		if zi.Zone.Core != "" {
			// the spec fixed this zone's core: respect the author's grouping
			notes = append(notes, fmt.Sprintf("zone %s: not auto-split (the spec fixes its core %s)", zi.Zone.ID, zi.Zone.Core))
			continue
		}
		assign := sbClusterZone(d, zi)
		if assign == nil {
			continue
		}
		before := -1 // a zone that could not be drawn at all: any drawing wins
		if l != nil {
			before = len(l.Findings)
		}
		ics := sbZoneICs(zi)
		subID := sbSubZoneIDs(d.Zones, zi.Zone.ID, ics)
		var subs []sbZone
		for _, ic := range ics {
			subs = append(subs, sbZone{ID: subID[ic.Ref], Title: zi.Zone.Title + " — " + ic.Ref, Page: zi.Zone.Page, Core: ic.Ref})
		}
		// apply the split to the design (reverted below unless it is kept)
		savedZones := append([]sbZone(nil), d.Zones...)
		savedIDs := map[string][2]string{}
		var zones []sbZone
		for _, z := range d.Zones {
			if z.ID == zi.Zone.ID {
				zones = append(zones, subs...)
			} else {
				zones = append(zones, z)
			}
		}
		d.Zones = zones
		for ref, ic := range assign {
			p := d.ByRef[ref]
			savedIDs[ref] = [2]string{p.ZoneID, p.Zone}
			p.ZoneID, p.Zone = subID[ic], subID[ic]
		}
		want := map[string]bool{}
		for _, s := range subs {
			want[s.ID] = true
		}
		var subZis []*sbZoneIn
		for _, z := range d.zoneInputs() {
			if want[z.Zone.ID] {
				subZis = append(subZis, z)
			}
		}
		subLayouts, subNotes := layoutZones(d, subZis, reuse, lo)
		after, drawn := 0, len(subLayouts) == len(subZis)
		for _, sz := range subZis {
			if sl := subLayouts[sz.Zone.ID]; sl != nil {
				after += len(sl.Findings)
			} else {
				drawn = false
			}
		}
		if drawn && (before < 0 || after < before) {
			delete(layouts, zi.Zone.ID)
			var ids []string
			for id, sl := range subLayouts {
				layouts[id] = sl
				ids = append(ids, id)
			}
			sort.Strings(ids)
			notes = append(notes, fmt.Sprintf("zone %s auto-split into %d IC sub-zones (%s): findings %s → %d",
				zi.Zone.ID, len(ids), strings.Join(ids, ", "), sbFindingCount(before), after))
			notes = append(notes, subNotes...)
			changed = true
			continue
		}
		// not better: restore the zone exactly as it was
		d.Zones = savedZones
		for ref, v := range savedIDs {
			d.ByRef[ref].ZoneID, d.ByRef[ref].Zone = v[0], v[1]
		}
		notes = append(notes, fmt.Sprintf("zone %s: auto-split tried (findings %s → %d, drawn=%v) and not kept", zi.Zone.ID, sbFindingCount(before), after, drawn))
		notes = append(notes, subNotes...)
	}
	if !changed {
		return zis, notes
	}
	return d.zoneInputs(), notes
}

func sbFindingCount(n int) string {
	if n < 0 {
		return "not drawn"
	}
	return fmt.Sprint(n)
}

// sbSheetShorts finds wire islands of a rendered sheet that join two
// different nets: spec nets of the symbol pins on them, power symbol values
// and net label names. A zone candidate with a short is never used: a
// planner drawing of a split sub-zone once rendered +5V onto a ground net
// (full-board run 2026-10-26; the KiCad netlist gate caught it at the end of
// a 30-minute build instead of the layout stage picking another drawing).
func sbSheetShorts(text string, d *sbDesign) []string {
	e, err := kicad.OpenSchematic(text)
	if err != nil {
		return nil
	}
	sc, err := e.Scene()
	if err != nil {
		return nil
	}
	W := sc.Wires
	par := make([]int, len(W))
	for i := range par {
		par[i] = i
	}
	var find func(int) int
	find = func(a int) int {
		for par[a] != a {
			par[a] = par[par[a]]
			a = par[a]
		}
		return a
	}
	const eps = 1e-4
	onSeg := func(p kicad.Pt, w [2]kicad.Pt) bool {
		a, b := w[0], w[1]
		if math.Abs(a.X-b.X) < eps && math.Abs(p.X-a.X) < eps {
			return p.Y >= math.Min(a.Y, b.Y)-eps && p.Y <= math.Max(a.Y, b.Y)+eps
		}
		if math.Abs(a.Y-b.Y) < eps && math.Abs(p.Y-a.Y) < eps {
			return p.X >= math.Min(a.X, b.X)-eps && p.X <= math.Max(a.X, b.X)+eps
		}
		return math.Hypot(p.X-a.X, p.Y-a.Y) < eps || math.Hypot(p.X-b.X, p.Y-b.Y) < eps
	}
	for i := range W {
		for j := range W {
			if i != j && (onSeg(W[i][0], W[j]) || onSeg(W[i][1], W[j])) {
				par[find(i)] = find(j)
			}
		}
	}
	nets := map[int]map[string]string{}
	attach := func(p kicad.Pt, net, why string) {
		if net == "" {
			return
		}
		for i, w := range W {
			if onSeg(p, w) {
				r := find(i)
				if nets[r] == nil {
					nets[r] = map[string]string{}
				}
				nets[r][net] = why
				return // all wires through p are one island already
			}
		}
	}
	for _, s := range sc.Symbols {
		if s.Power {
			val := ""
			for _, f := range s.Fields {
				if f.Name == "Value" {
					val = f.Value
				}
			}
			if val == "" || val == "PWR_FLAG" {
				continue
			}
			for _, q := range s.Pins {
				attach(q.At, val, "power "+s.Ref)
			}
			continue
		}
		for _, q := range s.Pins {
			attach(q.At, d.PinNet[sbPinKey(s.Ref, q.Number)], s.Ref+"."+q.Number)
		}
	}
	for _, l := range sc.Labels {
		attach(l.At, l.Name, "label")
	}
	var out []string
	for _, ns := range nets {
		if len(ns) < 2 {
			continue
		}
		var parts []string
		for n, why := range ns {
			parts = append(parts, n+" ("+why+")")
		}
		sort.Strings(parts)
		out = append(out, "short: one wire island joins "+strings.Join(parts, " + "))
	}
	sort.Strings(out)
	return out
}
