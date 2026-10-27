package app

// kicad_sch_build_paginate.go — automatic pagination: a page whose zones do
// not fit the largest paper (A0) is split into more pages, zones kept whole,
// in the zones' reading order.
//
// A multi-page build is a KiCad hierarchy (root sheet with one sheet symbol
// per page, a sheet pin per cross-page signal net), so a new page only needs
// zone.Page / part.PageID / NetPages updated; the hierarchy, its sheet pins
// and hierarchical labels follow from NetPages. Zones whose cross-page signal
// nets changed are laid out again (a cross-page net is drawn with a larger
// hierarchical label).

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbUndrivenSupplies is the set of supply / ground nets with no power
// output pin: each gets one PWR_FLAG (ERC).
func (d *sbDesign) sbUndrivenSupplies() map[string]bool {
	driven := map[string]bool{}
	for _, p := range d.Parts {
		for _, num := range p.PowerPins {
			if n, ok := d.PinNet[sbPinKey(p.Ref, num)]; ok {
				driven[n] = true
			}
		}
	}
	out := map[string]bool{}
	for _, n := range d.Nets {
		if k := d.NetKind[n.Name]; (k == "power" || k == "ground") && !driven[n.Name] {
			out[n.Name] = true
		}
	}
	return out
}

// sbFlagPages maps each page to the nets whose PWR_FLAG it draws: the first
// page (in d.Pages order) that carries the net.
func (d *sbDesign) sbFlagPages() map[string][]string {
	out := map[string][]string{}
	undriven := d.sbUndrivenSupplies()
	for _, n := range d.Nets {
		if !undriven[n.Name] {
			continue
		}
		for _, pg := range d.Pages {
			if d.NetPages[n.Name][pg.ID] {
				out[pg.ID] = append(out[pg.ID], n.Name)
				break
			}
		}
	}
	for _, ns := range out {
		sort.Strings(ns)
	}
	return out
}

// recomputeNetPages rebuilds net → pages from the parts' page ids.
func (d *sbDesign) recomputeNetPages() {
	d.NetPages = map[string]map[string]bool{}
	for _, n := range d.Nets {
		d.NetPages[n.Name] = map[string]bool{}
		for _, k := range n.Pins {
			ref, _, _ := strings.Cut(k, ".")
			if p := d.ByRef[ref]; p != nil {
				d.NetPages[n.Name][p.PageID] = true
			}
		}
	}
}

// sbZoneCrossSig fingerprints which of a zone's signal nets cross pages —
// the only page-dependent input of a zone layout.
func sbZoneCrossSig(d *sbDesign, zi *sbZoneIn) string {
	seen := map[string]bool{}
	var xs []string
	for _, p := range zi.Parts {
		for _, q := range p.LPins {
			n, ok := d.PinNet[sbPinKey(p.Ref, q.Number)]
			if !ok || seen[n] || d.NetKind[n] == "power" || d.NetKind[n] == "ground" {
				continue
			}
			seen[n] = true
			if len(d.NetPages[n]) > 1 {
				xs = append(xs, n)
			}
		}
	}
	sort.Strings(xs)
	return strings.Join(xs, "\x00")
}

// sbPageBases records, for every zone, the spec page it started on, and for
// every spec page its id and title. Pagination always regroups from these
// originals, so repeated rounds never produce "P-2-2" ids or "(1/2) (1/3)"
// titles (Codex design review 2026-10-26).
type sbPageBases struct {
	order     []sbPage          // the spec pages, in order
	zoneOrder []string          // reading order of the zones, frozen at the start
	zoneBase  map[string]string // zone id → base page id
	pageBase  map[string]string // any page id → base page id
	baseTitle map[string]string
	files     map[string]bool // sheet file names of the base pages and the root (fixed)
	// frag: zone id → index of the fragment of its base page it is on (0 =
	// the base page itself). It only ever grows: see sbPaginate.
	frag map[string]int
}

// newSbPageBases freezes the spec pages, the zones' base pages and their
// reading order (zoneInputs ranks zones by page-dependent facts, so taking
// the order again after a split could reorder groups and oscillate — Codex /
// Kimi review 2026-10-26), and records them on d for the saved spec.
func newSbPageBases(d *sbDesign, zis []*sbZoneIn, rootFile string) *sbPageBases {
	b := &sbPageBases{zoneBase: map[string]string{}, pageBase: map[string]string{}, baseTitle: map[string]string{}, files: map[string]bool{rootFile: true}, frag: map[string]int{}}
	for _, zi := range zis {
		b.zoneOrder = append(b.zoneOrder, zi.Zone.ID)
	}
	if d.SpecPages == nil {
		d.SpecPages = append([]sbPage(nil), d.Pages...)
		d.ZoneSpecPage = map[string]string{}
		for _, z := range d.Zones {
			d.ZoneSpecPage[z.ID] = z.Page
		}
	}
	for _, pg := range d.Pages {
		b.files[sbSheetFile(pg.ID)] = true
		b.order = append(b.order, pg)
		b.pageBase[pg.ID] = pg.ID
		t := pg.Title
		if t == "" {
			t = pg.ID
		}
		b.baseTitle[pg.ID] = t
	}
	for _, z := range d.Zones {
		b.zoneBase[z.ID] = b.pageBase[z.Page]
	}
	return b
}

// pageID allocates the id of fragment k (2, 3…) of base page base: unique
// as a page id AND as a sheet file name (ids are sanitised into file names,
// and the root sheet has one too). ids / files hold what this round already
// handed out; the same inputs always give the same id, so a stable grouping
// keeps its ids round after round.
func (b *sbPageBases) pageID(base string, k int, ids, files map[string]bool) string {
	id := fmt.Sprintf("%s-%d", base, k)
	for n := 2; ids[id] || files[sbSheetFile(id)]; n++ {
		id = fmt.Sprintf("%s-%d_%d", base, k, n)
	}
	return id
}

// sbPaginate regroups every base page's zones next-fit in zis order (the
// reading order): zones are added to the current group while the packer
// still fits them on the largest paper. The first group keeps the base id,
// the others get "<id>-2", "<id>-3"… (never an id of another base page) and
// follow it. It rewrites d.Pages, zone pages, part page ids and NetPages and
// reports whether the assignment changed.
//
// The grouping is monotone: a zone never goes back to an earlier fragment
// than the one an earlier round put it on (b.frag). A zone re-laid out for
// its new cross-page labels can come back smaller (another shape); a plain
// next-fit then merged the page again, the labels went back to local ones,
// the zones grew back, the page split again — a two-round cycle until the
// round guard gave up ("pagination did not settle", field board 2026-10-27).
// With fragments that only grow, every round that changes the assignment
// moves a zone to a later fragment, so the loop ends. A page whose zones fit
// A0 is one group in round 1, none of its nets changes its cross-page status,
// and it is never split or re-laid out.
func sbPaginate(d *sbDesign, layouts map[string]*sbZoneLayout, b *sbPageBases) (bool, []string) {
	taken, files := map[string]bool{}, map[string]bool{} // handed out this round
	for _, pg := range b.order {
		taken[pg.ID] = true
	}
	for f := range b.files {
		files[f] = true
	}
	oldPage := map[string]string{}
	for _, z := range d.Zones {
		oldPage[z.ID] = z.Page
	}
	newPage := map[string]string{}
	var pages []sbPage
	var notes []string
	// PWR_FLAGs: each undriven supply / ground net gets one, on the first
	// page carrying it (writeSbProject), in a row under that page's zones
	undriven := d.sbUndrivenSupplies()
	zoneFlags := map[string][]string{}
	for _, p := range d.Parts {
		for _, q := range p.LPins {
			if n, ok := d.PinNet[sbPinKey(p.Ref, q.Number)]; ok && undriven[n] {
				zoneFlags[p.ZoneID] = append(zoneFlags[p.ZoneID], n)
			}
		}
	}
	seen := map[string]bool{} // flag nets of the pages before the current group
	flagW := func(g []string) float64 {
		own := map[string]bool{}
		var nets []string
		for _, zid := range g {
			for _, n := range zoneFlags[zid] {
				if !seen[n] && !own[n] {
					own[n] = true
					nets = append(nets, n)
				}
			}
		}
		return sbFlagRowW(nets)
	}
	closeGroup := func(g []string) {
		for _, zid := range g {
			for _, n := range zoneFlags[zid] {
				seen[n] = true
			}
		}
	}
	for _, base := range b.order {
		var ids []string
		for _, zid := range b.zoneOrder {
			if b.zoneBase[zid] == base.ID && layouts[zid] != nil {
				ids = append(ids, zid)
			}
		}
		var groups [][]string
		var groupFlagW []float64 // each group's PWR_FLAG row, taken before it closes
		var cur []string
		for _, id := range ids {
			// cur is group len(groups). A zone an earlier round put on a later
			// fragment opens the next group: fragments of consecutive zones
			// differ by at most one, so one new group always reaches it.
			cand := append(append([]string(nil), cur...), id)
			if len(cur) == 0 || (b.frag[id] <= len(groups) && sbPackFresh(cand, layouts, flagW(cand)) != nil) {
				cur = cand
				b.frag[id] = len(groups)
				continue
			}
			groups = append(groups, cur)
			groupFlagW = append(groupFlagW, flagW(cur))
			closeGroup(cur)
			cur = []string{id}
			b.frag[id] = len(groups)
		}
		if len(cur) > 0 || len(groups) == 0 {
			groups = append(groups, cur)
			groupFlagW = append(groupFlagW, flagW(cur))
			closeGroup(cur)
		}
		for gi, g := range groups {
			np := base
			if len(groups) > 1 {
				if gi > 0 {
					np.ID = b.pageID(base.ID, gi+1, taken, files)
					taken[np.ID] = true
					files[sbSheetFile(np.ID)] = true
				}
				np.Title = fmt.Sprintf("%s (%d/%d)", b.baseTitle[base.ID], gi+1, len(groups))
			}
			b.pageBase[np.ID] = base.ID
			pages = append(pages, np)
			for _, zid := range g {
				newPage[zid] = np.ID
			}
			if len(g) == 1 && sbPackFresh(g, layouts, 0) == nil {
				notes = append(notes, fmt.Sprintf("page %s: zone %s alone does not fit A0 — split the zone in the spec (or try --auto-split)", np.ID, g[0]))
			} else if len(g) == 1 && groupFlagW[gi] > 0 && sbPackFresh(g, layouts, groupFlagW[gi]) == nil {
				// the zone fits, but not with the PWR_FLAG row its undriven
				// rails need under it: no page split can fix that
				notes = append(notes, fmt.Sprintf("page %s: zone %s does not fit A0 together with its PWR_FLAG row — split the zone in the spec (or drive those rails)", np.ID, g[0]))
			}
		}
		if len(groups) > 1 {
			var gids []string
			for gi := range groups {
				gids = append(gids, pages[len(pages)-len(groups)+gi].ID)
			}
			notes = append(notes, fmt.Sprintf("page %s does not fit A0: split into %d pages (%s)", base.ID, len(groups), strings.Join(gids, ", ")))
		}
	}
	changed := len(pages) != len(d.Pages)
	for i := 0; !changed && i < len(pages); i++ {
		changed = pages[i].ID != d.Pages[i].ID || pages[i].Title != d.Pages[i].Title
	}
	for id, pg := range newPage {
		changed = changed || oldPage[id] != pg
	}
	d.Pages = pages
	for i := range d.Zones {
		if pg, ok := newPage[d.Zones[i].ID]; ok {
			d.Zones[i].Page = pg
		}
	}
	zonePage := map[string]string{}
	for _, z := range d.Zones {
		zonePage[z.ID] = z.Page
	}
	for _, p := range d.Parts {
		p.PageID = zonePage[p.ZoneID]
	}
	d.recomputeNetPages()
	return changed, notes
}

// sbPaginateRoundLimit bounds the paginate ↔ re-layout loop. Fragments only
// grow (sbPaginate), a zone's fragment is below the zone count of its base
// page, and every round but the last moves at least one zone to a later
// fragment or loses a zone whose re-layout failed (once per zone), so
// 2 + Σ(zones per page − 1) + zones rounds always suffice. Running out is a
// bug and an error, never a reason to ship a layout drawn for the wrong
// labels.
func sbPaginateRoundLimit(b *sbPageBases) int {
	n := 2 + len(b.zoneOrder)
	per := map[string]int{}
	for _, zid := range b.zoneOrder {
		per[b.zoneBase[zid]]++
	}
	for _, c := range per {
		n += c - 1
	}
	return n
}

// sbPaginateAndRelayout paginates until the page assignment is stable,
// re-laying out every zone whose cross-page signal nets changed after each
// round. It returns the zone inputs for the final pages and the notes.
func sbPaginateAndRelayout(d *sbDesign, zis []*sbZoneIn, layouts, reuse map[string]*sbZoneLayout, lo sbLayoutOpts, rootFile string) ([]*sbZoneIn, []string, error) {
	return sbPaginateLoop(d, zis, layouts, rootFile, func(redo []*sbZoneIn) (map[string]*sbZoneLayout, []string) {
		// the saved drawing of the previous build carries the final
		// cross-page flags in its hash: reuse it instead of re-planning
		return layoutZones(d, redo, reuse, lo)
	})
}

// sbPaginateLoop is sbPaginateAndRelayout with the re-layout step passed in
// (tests swap in a stand-in that resizes zones by their cross-page nets).
func sbPaginateLoop(d *sbDesign, zis []*sbZoneIn, layouts map[string]*sbZoneLayout, rootFile string, relayout func([]*sbZoneIn) (map[string]*sbZoneLayout, []string)) ([]*sbZoneIn, []string, error) {
	b := newSbPageBases(d, zis, rootFile)
	limit := sbPaginateRoundLimit(b)
	var notes, last []string
	for round := 1; ; round++ {
		if round > limit {
			return zis, notes, fmt.Errorf("pagination did not settle in %d rounds (zones keep growing with their cross-page labels)", limit)
		}
		before := map[string]string{}
		for _, zi := range zis {
			before[zi.Zone.ID] = sbZoneCrossSig(d, zi)
		}
		changed, n := sbPaginate(d, layouts, b)
		last = n
		zis = d.zoneInputs()
		var redo []*sbZoneIn
		for _, zi := range zis {
			if sbZoneCrossSig(d, zi) != before[zi.Zone.ID] {
				redo = append(redo, zi)
			}
		}
		if len(redo) > 0 {
			fresh, ln := relayout(redo)
			for _, zi := range redo {
				if l := fresh[zi.Zone.ID]; l != nil {
					layouts[zi.Zone.ID] = l
				} else {
					delete(layouts, zi.Zone.ID)
				}
			}
			notes = append(notes, ln...)
			notes = append(notes, fmt.Sprintf("pagination round %d: %d zone(s) re-laid out for their new cross-page labels", round, len(redo)))
		}
		if !changed && len(redo) == 0 {
			break
		}
	}
	return zis, append(notes, last...), nil
}

// sbRootRows lays the root sheet's sheet symbols out in rows inside the A0
// drawing area. size(i) gives the left label room, symbol width, right label
// room and symbol height of sheet i; setX receives the symbol's x. Rows follow
// the reading order; when those rows are too tall for A0, shelves by
// decreasing height are used instead (sheet numbers still give the order).
// Returns each sheet's top y, its row and the placement order.
func sbRootRows(n int, size func(int) (lroom, w, rroom, h float64), setX func(int, float64)) ([]float64, []int, []int) {
	a0 := kicad.Papers[len(kicad.Papers)-1]
	const left, top, gapX, gapY = 25.4, 38.1, 25.4, 22.86
	maxX, maxY := a0.W-10-5.08, a0.H-10-5.08-34 // border, margin, title block band
	sy, rowOf := make([]float64, n), make([]int, n)
	place := func(order []int) bool {
		x, y, rowH, row := left, top, 0.0, 0
		fits := true
		for k, i := range order {
			lr, w, rr, h := size(i)
			fw := lr + w + rr
			if k > 0 && x+fw > maxX {
				x, y, rowH, row = left, y+rowH+gapY, 0, row+1
			}
			if x+fw > maxX {
				fits = false // wider than a whole row (Codex review)
			}
			setX(i, x+lr)
			sy[i], rowOf[i] = y, row
			x += fw + gapX
			rowH = math.Max(rowH, h+5.08) // + the Sheetfile field below the symbol
		}
		return fits && y+rowH <= maxY
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	if place(order) {
		return sy, rowOf, order
	}
	byH := append([]int(nil), order...)
	sort.SliceStable(byH, func(a, b int) bool {
		_, _, _, ha := size(byH[a])
		_, _, _, hb := size(byH[b])
		return ha > hb
	})
	if place(byH) {
		return sy, rowOf, byH
	}
	place(order) // no fit either way: keep the reading order; page-fit reports it
	return sy, rowOf, order
}
