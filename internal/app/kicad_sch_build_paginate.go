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
	zoneBase  map[string]string // zone id → base page id
	pageBase  map[string]string // any page id → base page id
	baseTitle map[string]string
}

func newSbPageBases(d *sbDesign) *sbPageBases {
	b := &sbPageBases{zoneBase: map[string]string{}, pageBase: map[string]string{}, baseTitle: map[string]string{}}
	for _, pg := range d.Pages {
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

// sbPaginate regroups every base page's zones next-fit in zis order (the
// reading order): zones are added to the current group while the packer
// still fits them on the largest paper. The first group keeps the base id,
// the others get "<id>-2", "<id>-3"… (never an id of another base page) and
// follow it. It rewrites d.Pages, zone pages, part page ids and NetPages and
// reports whether the assignment changed.
func sbPaginate(d *sbDesign, zis []*sbZoneIn, layouts map[string]*sbZoneLayout, b *sbPageBases) (bool, []string) {
	reserved := map[string]bool{}
	for _, pg := range b.order {
		reserved[pg.ID] = true
	}
	oldPage := map[string]string{}
	for _, z := range d.Zones {
		oldPage[z.ID] = z.Page
	}
	newPage := map[string]string{}
	var pages []sbPage
	var notes []string
	for _, base := range b.order {
		var ids []string
		for _, zi := range zis {
			if b.zoneBase[zi.Zone.ID] == base.ID && layouts[zi.Zone.ID] != nil {
				ids = append(ids, zi.Zone.ID)
			}
		}
		var groups [][]string
		var cur []string
		for _, id := range ids {
			if len(cur) == 0 || sbPackFresh(append(append([]string(nil), cur...), id), layouts) != nil {
				cur = append(cur, id)
				continue
			}
			groups = append(groups, cur)
			cur = []string{id}
		}
		if len(cur) > 0 || len(groups) == 0 {
			groups = append(groups, cur)
		}
		for gi, g := range groups {
			np := base
			if len(groups) > 1 {
				if gi > 0 {
					id := fmt.Sprintf("%s-%d", base.ID, gi+1)
					for n := 2; reserved[id]; n++ {
						id = fmt.Sprintf("%s-%d_%d", base.ID, gi+1, n)
					}
					np.ID = id
				}
				np.Title = fmt.Sprintf("%s (%d/%d)", b.baseTitle[base.ID], gi+1, len(groups))
			}
			b.pageBase[np.ID] = base.ID
			pages = append(pages, np)
			for _, zid := range g {
				newPage[zid] = np.ID
			}
			if len(g) == 1 && sbPackFresh(g, layouts) == nil {
				notes = append(notes, fmt.Sprintf("page %s: zone %s alone does not fit A0 — split the zone in the spec (or try --auto-split)", np.ID, g[0]))
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

// sbPaginateMaxRounds bounds the paginate ↔ re-layout loop. Every round can
// only add cross-page labels (zones grow, pages split further), so a real
// design settles in a few rounds; running out is an error, never a reason to
// ship a layout drawn for the wrong labels.
const sbPaginateMaxRounds = 8

// sbPaginateAndRelayout paginates until the page assignment is stable,
// re-laying out every zone whose cross-page signal nets changed after each
// round. It returns the zone inputs for the final pages and the notes.
func sbPaginateAndRelayout(d *sbDesign, zis []*sbZoneIn, layouts map[string]*sbZoneLayout, lo sbLayoutOpts) ([]*sbZoneIn, []string, error) {
	b := newSbPageBases(d)
	var notes, last []string
	for round := 1; ; round++ {
		if round > sbPaginateMaxRounds {
			return zis, notes, fmt.Errorf("pagination did not settle in %d rounds (zones keep growing with their cross-page labels)", sbPaginateMaxRounds)
		}
		before := map[string]string{}
		for _, zi := range zis {
			before[zi.Zone.ID] = sbZoneCrossSig(d, zi)
		}
		changed, n := sbPaginate(d, zis, layouts, b)
		last = n
		zis = d.zoneInputs()
		var redo []*sbZoneIn
		for _, zi := range zis {
			if sbZoneCrossSig(d, zi) != before[zi.Zone.ID] {
				redo = append(redo, zi)
			}
		}
		if len(redo) > 0 {
			fresh, ln := layoutZones(d, redo, nil, lo)
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
		for k, i := range order {
			lr, w, rr, h := size(i)
			fw := lr + w + rr
			if k > 0 && x+fw > maxX {
				x, y, rowH, row = left, y+rowH+gapY, 0, row+1
			}
			setX(i, x+lr)
			sy[i], rowOf[i] = y, row
			x += fw + gapX
			rowH = math.Max(rowH, h+5.08) // + the Sheetfile field below the symbol
		}
		return y+rowH <= maxY
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
