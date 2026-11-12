package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// Package text on every symbol and the BOM sheet that follows every
// schematic page (sch-build, both on by default).
//
// A reader of the printed schematic must be able to tell what each part is
// without the project open: the package next to the value ("10k 0603"), and
// after every page a table of that page's parts with value, package, order
// numbers, ratings / requirements and where the part comes from. A BOM sheet
// holds a table and nothing else — no symbol, wire or label — so the
// netlist, ERC and the PCB links never see it, and the page gates (page-fit,
// engineer-grade, layout-quality) measure schematic pages only.

// ── package text ────────────────────────────────────────────────────────────

var (
	sbPkgMetricRE = regexp.MustCompile(`^(?:[A-Za-z]+_)?(\d{4})_\d{4}Metric`)
	sbPkgChipRE   = regexp.MustCompile(`^(?:R|C|L|LED|D|F|FB)(0201|0402|0603|0805|1206|1210|1806|1812|2010|2512)(?:$|[-_])`)
	sbPkgArrayRE  = regexp.MustCompile(`(?:^|_)(\d+x(?:0201|0402|0603|0805|1206))(?:$|_)`) // R_Array_Convex_4x0603
	sbPkgFamilyRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-\d`)
	sbPkgEPRE     = regexp.MustCompile(`-\d+EP$`)
	sbPkgDimRE    = regexp.MustCompile(`mm|^P\d|^[LW]\d+\.\d|^\d+(\.\d+)?x\d+(\.\d+)?$`)
)

// sbPkgMax is the longest package text drawn next to a symbol.
const sbPkgMax = 16

// sbPackageOf is the short package name a footprint ("Lib:Name" or "Name")
// stands for: the chip size of a passive (R_0603_1608Metric → 0603), the
// family and pin count of an IC (SOIC-8_3.9x4.9mm_P1.27mm → SOIC-8,
// QFN-32-1EP_5x5mm_P0.5mm_EP3.1x3.1mm → QFN-32), else the footprint name's
// leading words up to sbPkgMax characters. "" for no footprint.
func sbPackageOf(footprint string) string {
	name := strings.TrimSpace(footprint)
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return ""
	}
	if m := sbPkgMetricRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if m := sbPkgChipRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	if m := sbPkgArrayRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	// "D_SOD-123", "Q_SOT-23": the part class in front of the package
	if i := strings.Index(name, "_"); i > 0 && i <= 3 && sbPkgFamilyRE.MatchString(name[i+1:]) {
		name = name[i+1:]
	}
	tok := strings.Split(name, "_")
	if sbPkgFamilyRE.MatchString(tok[0]) {
		return sbCutRunes(sbPkgEPRE.ReplaceAllString(tok[0], ""), sbPkgMax)
	}
	out := tok[0]
	for _, t := range tok[1:] {
		if t == "" || sbPkgDimRE.MatchString(t) || len([]rune(out))+1+len([]rune(t)) > sbPkgMax {
			break
		}
		out += "_" + t
	}
	return sbCutRunes(out, sbPkgMax)
}

func sbCutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// sbFieldCI reads a part field by name, ignoring case (the first of names
// that is set and not blank).
func sbFieldCI(fields map[string]string, names ...string) string {
	for _, n := range names {
		var keys []string
		for k := range fields {
			if strings.EqualFold(k, n) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v := strings.TrimSpace(fields[k]); v != "" {
				return v
			}
		}
	}
	return ""
}

// sbPackageText is the part's package: fields.Package when the spec gives
// one, else what its footprint stands for.
func sbPackageText(p *sbRPart) string {
	if v := sbFieldCI(p.Fields, "Package"); v != "" {
		return strings.Join(strings.Fields(v), " ")
	}
	return sbPackageOf(p.Footprint)
}

// ── BOM rows ────────────────────────────────────────────────────────────────

// sbBomRow is one line of a page's BOM: identical parts grouped.
type sbBomRow struct {
	Refs     []string `json:"refs"`
	RefText  string   `json:"refText"`
	Qty      int      `json:"qty"`
	Value    string   `json:"value"`
	Package  string   `json:"package"`
	MPN      string   `json:"mpn"`
	LCSC     string   `json:"lcsc"`
	Spec     string   `json:"spec"`
	Sourcing string   `json:"sourcing"`
	DNP      bool     `json:"dnp,omitempty"`
}

// sbBomMPNKeys / sbBomLCSCKeys are the field names a spec (or a schematic
// read back from another tool) keeps a part's identity under when it is not
// in the part's own mpn / lcsc. The BOM reads them; the symbol source does
// not change (only "lcsc" imports a symbol).
var sbBomMPNKeys = []string{"MPN", "Mfr Part", "Mfr Part #", "Manufacturer Part", "Manufacturer Part Number", "Part Number"}
var sbBomLCSCKeys = []string{"LCSC", "LCSC Part", "LCSC Part #", "LCSC Part Number", "JLCPCB Part", "JLCPCB Part #"}

// sbBomOwnMPN is the manufacturer part number the design itself names: mpn,
// else a field.
func sbBomOwnMPN(p *sbRPart) string {
	if v := strings.TrimSpace(p.MPN); v != "" {
		return v
	}
	return strings.Join(strings.Fields(sbFieldCI(p.Fields, sbBomMPNKeys...)), " ")
}

// sbBomMPN is the MPN column: the design's own, else the catalogue's for the
// part's LCSC number.
func sbBomMPN(p *sbRPart) string {
	if v := sbBomOwnMPN(p); v != "" {
		return v
	}
	if p.cat != nil {
		return strings.Join(strings.Fields(p.cat.MPN), " ")
	}
	return ""
}

// sbBomCatText is what the catalogue says the part's LCSC number is
// (manufacturer, description), marked as such: it describes what would be
// bought, not what the design requires.
func sbBomCatText(p *sbRPart) string {
	if p.cat == nil {
		return ""
	}
	var t []string
	for _, v := range []string{p.cat.Manufacturer, p.cat.Description} {
		if v = strings.Join(strings.Fields(v), " "); v != "" {
			t = append(t, v)
		}
	}
	if len(t) == 0 {
		return ""
	}
	return "[LCSC catalogue] " + strings.Join(t, ", ")
}

// sbCatalogLookup asks the live catalogue (replaced in tests).
var sbCatalogLookup = kicad.LookupEasyEDAPart

// sbBomCatalog attaches the cached catalogue record to every part with an
// LCSC number; with fetch (--bom-catalog) it first asks the catalogue for
// the numbers not cached yet: two at a time, 25 s per number, 4 min in all.
// A number that could not be read is listed in Failed — the BOM is then
// only less complete, the build does not fail.
func sbBomCatalog(parts []*sbRPart, fetch bool) *sbBomCatalogStat {
	st := &sbBomCatalogStat{Failed: []string{}, Unknown: []string{}}
	byNum := map[string][]*sbRPart{}
	var nums []string
	for _, p := range parts {
		p.cat = nil
		n := sbBomLCSC(p)
		if n == "" {
			continue
		}
		if byNum[n] == nil {
			nums = append(nums, n)
		}
		byNum[n] = append(byNum[n], p)
	}
	sort.Strings(nums)
	st.Numbers = len(nums)
	recs := map[string]kicad.EasyEDAPart{}
	var todo []string
	for _, n := range nums {
		if c, ok := kicad.CatalogCached(n); ok {
			recs[n] = c
		} else {
			todo = append(todo, n)
		}
	}
	if fetch && len(todo) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 2)
		for _, n := range todo {
			wg.Add(1)
			go func(n string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				pctx, pcancel := context.WithTimeout(ctx, 25*time.Second)
				defer pcancel()
				c, found, err := sbCatalogLookup(pctx, n)
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err != nil:
					st.Failed = append(st.Failed, n)
				case !found:
					st.Unknown = append(st.Unknown, n)
				default:
					recs[n] = c
					st.Fetched++
					_ = kicad.CatalogStore(c)
				}
			}(n)
		}
		wg.Wait()
		sort.Strings(st.Failed)
		sort.Strings(st.Unknown)
	}
	for n, c := range recs {
		c := c
		for _, p := range byNum[n] {
			p.cat = &c
		}
	}
	st.Known = len(recs)
	return st
}

// sbBomLCSC is the part's LCSC number: lcsc, else a field that holds one.
func sbBomLCSC(p *sbRPart) string {
	if v := strings.TrimSpace(p.LCSC); v != "" {
		return v
	}
	if v := strings.ToUpper(sbFieldCI(p.Fields, sbBomLCSCKeys...)); kicad.ValidLCSC(v) {
		return v
	}
	return ""
}

var sbBomCustomRE = regexp.MustCompile(`(?i)\b(custom|self[- ]?wound|self[- ]?sourced)\b|定制|自制|自购|自绕`)

// sbBomCustom: the part's own text says it is made or sourced by the owner.
func sbBomCustom(p *sbRPart) bool {
	return sbBomCustomRE.MatchString(p.Value + " " + sbFieldCI(p.Fields, "Description", "Spec", "Requirements", "Note", "Notes"))
}

// sbBomSpecKeys are the fields the "Spec / requirements" column shows, in
// this order; every other field that is not bookkeeping follows as
// "key: value".
var sbBomSpecKeys = []string{"Description", "Rating", "Power", "Voltage", "Current", "Tolerance", "Dielectric", "Note", "Notes", "Spec", "Requirements"}

// sbBomSkipField: fields with a column of their own, or tool bookkeeping.
func sbBomSkipField(k string) bool {
	l := strings.ToLower(k)
	switch l {
	case "package", "sourcing", "reference", "value", "footprint", "datasheet", "dnp":
		return true
	}
	for _, n := range append(append([]string(nil), sbBomMPNKeys...), sbBomLCSCKeys...) {
		if strings.EqualFold(k, n) {
			return true
		}
	}
	return strings.HasPrefix(l, "pcbpilot_") || strings.HasPrefix(l, "sim.") || strings.HasPrefix(l, "ki_")
}

// sbBomSpec is the "Spec / requirements" cell of a part: the description
// first, then the ratings, then every other field.
func sbBomSpec(p *sbRPart) string {
	var out []string
	described := false
	used := map[string]bool{}
	for _, name := range sbBomSpecKeys {
		var keys []string
		for k := range p.Fields {
			if strings.EqualFold(k, name) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			used[k] = true
			v := strings.Join(strings.Fields(p.Fields[k]), " ")
			if v == "" {
				continue
			}
			if name == "Description" {
				out = append(out, v)
				described = true
			} else {
				out = append(out, name+": "+v)
			}
		}
	}
	var rest []string
	for k := range p.Fields {
		if !used[k] && !sbBomSkipField(k) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		if v := strings.Join(strings.Fields(p.Fields[k]), " "); v != "" {
			out = append(out, k+": "+v)
		}
	}
	if c := sbBomCatText(p); c != "" && !described {
		out = append([]string{c}, out...)
	}
	return strings.Join(out, "; ")
}

// sbBomSourcing: fields.Sourcing, else "LCSC" for a part with an LCSC
// number, else a dash.
func sbBomSourcing(p *sbRPart) string {
	if v := sbFieldCI(p.Fields, "Sourcing"); v != "" {
		return strings.Join(strings.Fields(v), " ")
	}
	if sbBomLCSC(p) != "" {
		return "LCSC"
	}
	if sbBomCustom(p) {
		return "custom / self-sourced"
	}
	return "—"
}

// sbBomRows groups parts that are the same line of a BOM (value, footprint,
// MPN, LCSC, spec, sourcing, DNP) and orders the lines by their first
// reference.
func sbBomRows(parts []*sbRPart) []sbBomRow {
	idx := map[string]int{}
	var rows []sbBomRow
	sorted := append([]*sbRPart(nil), parts...)
	sort.SliceStable(sorted, func(a, b int) bool { return sbRefLess(sorted[a].Ref, sorted[b].Ref) })
	for _, p := range sorted {
		r := sbBomRow{Value: p.Value, Package: p.Package, MPN: sbBomMPN(p), LCSC: sbBomLCSC(p), Spec: sbBomSpec(p), Sourcing: sbBomSourcing(p), DNP: p.DNP}
		key := strings.Join([]string{r.Value, p.Footprint, r.Package, r.MPN, r.LCSC, r.Spec, r.Sourcing, strconv.FormatBool(r.DNP)}, "\x00")
		i, ok := idx[key]
		if !ok {
			i = len(rows)
			idx[key] = i
			rows = append(rows, r)
		}
		rows[i].Refs = append(rows[i].Refs, p.Ref)
	}
	for i := range rows {
		rows[i].Qty = len(rows[i].Refs)
		rows[i].RefText = sbCompressRefs(rows[i].Refs)
		if rows[i].DNP {
			rows[i].RefText += " (DNP)"
		}
	}
	return rows
}

var sbRefNumRE = regexp.MustCompile(`^(.*?)(\d+)$`)

// sbRefLess orders references the way a BOM reads: by prefix, then number.
func sbRefLess(a, b string) bool {
	ma, mb := sbRefNumRE.FindStringSubmatch(a), sbRefNumRE.FindStringSubmatch(b)
	if ma == nil || mb == nil || ma[1] != mb[1] {
		pa, pb := a, b
		if ma != nil {
			pa = ma[1]
		}
		if mb != nil {
			pb = mb[1]
		}
		if pa != pb {
			return pa < pb
		}
		return a < b
	}
	na, _ := strconv.Atoi(ma[2])
	nb, _ := strconv.Atoi(mb[2])
	if na != nb {
		return na < nb
	}
	return a < b
}

// sbCompressRefs writes a reference list with runs of three or more
// consecutive numbers as a range: R1-R4, R7.
func sbCompressRefs(refs []string) string {
	rs := append([]string(nil), refs...)
	sort.SliceStable(rs, func(a, b int) bool { return sbRefLess(rs[a], rs[b]) })
	var out []string
	for i := 0; i < len(rs); {
		j := i
		m := sbRefNumRE.FindStringSubmatch(rs[i])
		if m != nil && !strings.HasPrefix(m[2], "0") {
			n, _ := strconv.Atoi(m[2])
			for j+1 < len(rs) && rs[j+1] == m[1]+strconv.Itoa(n+(j+1-i)) {
				j++
			}
		}
		if j-i >= 2 {
			out = append(out, rs[i]+"-"+rs[j])
		} else {
			out = append(out, rs[i:j+1]...)
		}
		i = j + 1
	}
	return strings.Join(out, ", ")
}

// ── completeness ────────────────────────────────────────────────────────────

// sbBomMissing is a part the reader of the BOM cannot identify.
type sbBomMissing struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
	Fix    string `json:"fix"`
}

// sbBomJellybean: a part its value and package describe completely.
func sbBomJellybean(p *sbRPart) bool {
	switch sbRefPrefix(p.Ref) {
	case "R", "C":
		return true
	}
	return false
}

// sbBomNoOrder: board features nobody orders (test points, mounting holes,
// fiducials, solder jumpers, net ties, logos).
func sbBomNoOrder(p *sbRPart) bool {
	switch sbRefPrefix(p.Ref) {
	case "TP", "H", "MH", "FID", "JP", "SJ", "NT", "LOGO", "G":
		return true
	}
	return false
}

// sbBomCheck lists what the BOM lacks: parts with no package, parts with an
// empty spec cell that are not plain passives, and (missing) the parts a
// reader cannot identify at all.
func sbBomCheck(parts []*sbRPart) (noPackage, noSpec []string, missing []sbBomMissing) {
	sorted := append([]*sbRPart(nil), parts...)
	sort.SliceStable(sorted, func(a, b int) bool { return sbRefLess(sorted[a].Ref, sorted[b].Ref) })
	for _, p := range sorted {
		if p.Package == "" {
			noPackage = append(noPackage, p.Ref)
			missing = append(missing, sbBomMissing{Ref: p.Ref, Reason: "no footprint, so no package",
				Fix: fmt.Sprintf("add footprint (or lcsc, or fields.Package) to %s", p.Ref)})
		}
		if sbBomJellybean(p) || sbBomNoOrder(p) {
			continue
		}
		described := sbFieldCI(p.Fields, "Description", "Spec", "Requirements") != ""
		if !described && sbBomSpec(p) == "" {
			noSpec = append(noSpec, p.Ref)
		}
		if sbBomMPN(p) == "" && sbBomLCSC(p) == "" && !described && sbBomCustom(p) {
			missing = append(missing, sbBomMissing{Ref: p.Ref, Reason: "a custom part with no requirements: nobody can make or buy it from this BOM",
				Fix: fmt.Sprintf("add fields.Description to %s with what it must meet (a transformer or inductor: turns ratio, inductance, saturation current, isolation voltage, core; a connector or mechanical part: drawing or mating part)", p.Ref)})
			continue
		}
		if sbBomMPN(p) == "" && sbBomLCSC(p) == "" && !described {
			missing = append(missing, sbBomMissing{Ref: p.Ref, Reason: "no MPN, no LCSC number and no description: the BOM does not say what this part is",
				Fix: fmt.Sprintf("add fields.Description and fields.Sourcing to %s (or mpn / lcsc)", p.Ref)})
		}
	}
	return noPackage, noSpec, missing
}

// ── table layout ────────────────────────────────────────────────────────────

var sbBomHeader = []string{"Refs", "Qty", "Value", "Package", "MPN", "LCSC", "Spec / requirements", "Sourcing"}

// sbBomColW are the column widths (mm) on A4 landscape: together the
// drawing area between the borders. Larger papers scale them.
var sbBomColW = []float64{38, 10, 34, 26, 34, 16, 76, 32}

const (
	sbBomLeft   = 16.51  // border + gap, and the heading's measured box (kicad sch-fit) inside it
	sbBomTop    = 24.0   // under the heading line
	sbBomMargin = 0.9525 // cell margin (KiCad default)
	sbBomLine   = 2.1    // line pitch of 1.27 mm text
	sbBomCharW  = 1.1    // as sbTextW
	sbBomTitleH = 34.0 + 5
	sbBomBorder = 10.0 + 5
)

// sbBomPapers are the papers a BOM sheet may use, smallest first.
var sbBomPapers = []string{"A4", "A3", "A2"}

func sbPaperSize(name string) (w, h float64) {
	for _, p := range kicad.Papers {
		if p.Name == name {
			return p.W, p.H
		}
	}
	return 297, 210
}

// sbWrap breaks text into lines of at most n characters at spaces (and
// after , ; / -), splitting a longer word.
func sbWrap(text string, n int) []string {
	if n < 1 {
		n = 1
	}
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		r := []rune(strings.TrimSpace(para))
		if len(r) == 0 {
			lines = append(lines, "")
			continue
		}
		for len(r) > n {
			cut := -1
			for i := n; i > n/3; i-- { // the last space that still fits
				if r[i] == ' ' {
					cut = i
					break
				}
			}
			for i := n; cut < 0 && i > n/3; i-- { // else after punctuation
				if c := r[i-1]; c == ',' || c == ';' || c == '/' || c == '-' {
					cut = i
				}
			}
			if cut <= 0 {
				cut = n
			}
			lines = append(lines, strings.TrimRight(string(r[:cut]), " "))
			r = []rune(strings.TrimLeft(string(r[cut:]), " "))
		}
		if len(r) > 0 {
			lines = append(lines, string(r))
		}
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// sbBomSheet is one BOM sheet: its paper, column widths and wrapped cells.
type sbBomSheet struct {
	Paper string
	ColW  []float64
	Cells [][]string // header first; each cell already wrapped ("\n")
	RowH  []float64
	Rows  int // BOM lines on the sheet (header not counted)
}

func (r sbBomRow) cells() []string {
	return []string{r.RefText, strconv.Itoa(r.Qty), r.Value, r.Package, r.MPN, r.LCSC, r.Spec, r.Sourcing}
}

// sbBomLayout puts the rows of one page on as few sheets as possible: the
// smallest paper (A4 … A2, landscape) that holds all of them, else A2
// sheets, each continuing the one before.
func sbBomLayout(rows []sbBomRow) []sbBomSheet {
	type laid struct {
		colW  []float64
		cells [][]string
		h     []float64
	}
	lay := func(paper string) (laid, float64) {
		pw, ph := sbPaperSize(paper)
		scale := math.Min((pw-2*sbBomBorder)/(297-2*sbBomBorder), 1.6)
		var l laid
		for _, w := range sbBomColW {
			l.colW = append(l.colW, math.Round(w*scale/1.27)*1.27)
		}
		// the table ends inside the drawing area: the spec column gives
		sum := 0.0
		for _, w := range l.colW {
			sum += w
		}
		if over := sum - (pw - sbBomLeft - sbBomBorder); over > 0 {
			l.colW[6] -= math.Ceil(over/1.27) * 1.27
		}
		maxLines := max(4, int((ph-sbBomTop-sbBomBorder-sbBomTitleH)/2/sbBomLine))
		wrapRow := func(cells []string) {
			var out []string
			n := 1
			for i, c := range cells {
				ls := sbWrap(c, int((l.colW[i]-2*sbBomMargin)/sbBomCharW))
				// a cell taller than half a sheet is cut: a row is never
				// split, and one that cannot fit would print off the paper
				if len(ls) > maxLines {
					ls = append(ls[:maxLines-1:maxLines-1], "… (cut: see the spec)")
				}
				n = max(n, len(ls))
				out = append(out, strings.Join(ls, "\n"))
			}
			l.cells = append(l.cells, out)
			l.h = append(l.h, math.Ceil((float64(n)*sbBomLine+2*sbBomMargin)/1.27)*1.27)
		}
		wrapRow(sbBomHeader)
		for _, r := range rows {
			wrapRow(r.cells())
		}
		return l, ph - sbBomTop - sbBomBorder - sbBomTitleH
	}
	var l laid
	var room float64
	paper := ""
	for _, p := range sbBomPapers {
		l, room = lay(p)
		paper = p
		total := 0.0
		for _, h := range l.h {
			total += h
		}
		if total <= room {
			break
		}
	}
	var sheets []sbBomSheet
	newSheet := func() *sbBomSheet {
		sheets = append(sheets, sbBomSheet{Paper: paper, ColW: l.colW, Cells: [][]string{l.cells[0]}, RowH: []float64{l.h[0]}})
		return &sheets[len(sheets)-1]
	}
	cur := newSheet()
	used := l.h[0]
	for i := 1; i < len(l.cells); i++ {
		if used+l.h[i] > room && cur.Rows > 0 {
			cur = newSheet()
			used = l.h[0]
		}
		cur.Cells, cur.RowH = append(cur.Cells, l.cells[i]), append(cur.RowH, l.h[i])
		cur.Rows++
		used += l.h[i]
	}
	return sheets
}

// sbBomUUID is a stable id for a table cell: a rebuild of the same BOM
// writes the same file.
func sbBomUUID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	b := h[:16]
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// sbBomTable is the KiCad table item of one BOM sheet.
func sbBomTable(sh sbBomSheet, sheetUUID string) string {
	var b strings.Builder
	nums := func(v []float64) string {
		var s []string
		for _, x := range v {
			s = append(s, kicad.F(x))
		}
		return strings.Join(s, " ")
	}
	fmt.Fprintf(&b, "(table\n\t\t(column_count %d)\n\t\t(border (external yes) (header yes) (stroke (width 0) (type solid)))\n\t\t(separators (rows yes) (cols yes) (stroke (width 0) (type solid)))\n\t\t(column_widths %s)\n\t\t(row_heights %s)\n\t\t(cells",
		len(sh.ColW), nums(sh.ColW), nums(sh.RowH))
	y := sbBomTop
	for r, row := range sh.Cells {
		x := sbBomLeft
		for c, text := range row {
			font := "(size 1.27 1.27)"
			if r == 0 {
				font += " (bold yes)"
			}
			fmt.Fprintf(&b, "\n\t\t\t(table_cell %s (exclude_from_sim no) (at %s %s 0) (size %s %s) (margins %s %s %s %s) (span 1 1) (fill (type none)) (effects (font %s) (justify left top)) (uuid %s))",
				kicad.Q(text), kicad.F(x), kicad.F(y), kicad.F(sh.ColW[c]), kicad.F(sh.RowH[r]),
				kicad.F(sbBomMargin), kicad.F(sbBomMargin), kicad.F(sbBomMargin), kicad.F(sbBomMargin), font,
				kicad.Q(sbBomUUID(sheetUUID, strconv.Itoa(r), strconv.Itoa(c))))
			x += sh.ColW[c]
		}
		y += sh.RowH[r]
	}
	b.WriteString("\n\t\t)\n\t)")
	return b.String()
}

// sbBomHeading is the text line over the table.
func sbBomHeading(sheetUUID, text string) string {
	return fmt.Sprintf("(text %s\n\t\t(exclude_from_sim no)\n\t\t(at %s %s 0)\n\t\t(effects\n\t\t\t(font\n\t\t\t\t(size 2 2)\n\t\t\t\t(bold yes)\n\t\t\t)\n\t\t\t(justify left bottom)\n\t\t)\n\t\t(uuid %s)\n\t)",
		kicad.Q(text), kicad.F(sbBomLeft), kicad.F(sbBomTop-2.54), kicad.Q(sbBomUUID(sheetUUID, "heading")))
}

// ── report ──────────────────────────────────────────────────────────────────

type sbBomPage struct {
	Page  string `json:"page"`
	File  string `json:"file"`
	Sheet string `json:"sheet"` // sheet name
	Paper string `json:"paper"`
	No    int    `json:"pageNumber"`
	Rows  int    `json:"rows"`
}

// sbBomReport is the report's "bom" section.
type sbBomReport struct {
	// Sheets: BOM follow-up sheets were written (false: --no-bom-pages).
	Sheets      bool           `json:"sheets"`
	PackageText bool           `json:"packageText"`
	Pages       []sbBomPage    `json:"pages"`
	Rows        int            `json:"rows"`
	Parts       int            `json:"parts"`
	NoPackage   []string       `json:"noPackage"`
	NoSpec      []string       `json:"noSpec"`
	Missing     []sbBomMissing `json:"missing"`
	// Catalog: how many of the design's LCSC numbers have a catalogue
	// record the BOM could show.
	Catalog *sbBomCatalogStat `json:"catalog,omitempty"`
}

// sbBomCatalogStat: numbers = distinct LCSC numbers in the design, known =
// those with a catalogue record (cached or just fetched), fetched = read
// from the network in this build, failed = the catalogue did not answer,
// unknown = the catalogue has no such number.
type sbBomCatalogStat struct {
	Numbers int      `json:"numbers"`
	Known   int      `json:"known"`
	Fetched int      `json:"fetched"`
	Failed  []string `json:"failed"`
	Unknown []string `json:"unknown"`
}

// sbBomFile is the file of a page's n-th BOM sheet (1 = first), next to the
// page's own file; taken avoids the name of a schematic page.
func sbBomFile(pageFile string, n int, taken map[string]bool) string {
	stem := strings.TrimSuffix(pageFile, ".kicad_sch") + "-bom"
	if n > 1 {
		stem += "-" + strconv.Itoa(n)
	}
	for taken[strings.ToLower(stem+".kicad_sch")] {
		stem += "_"
	}
	return stem + ".kicad_sch"
}

// sbBomSheetName is the sheet name of a page's n-th BOM sheet.
func sbBomSheetName(pageTitle string, n int) string {
	name := strings.TrimSpace(pageTitle + " BOM")
	if n > 1 {
		name += " " + strconv.Itoa(n)
	}
	return name
}

// BOM sheet symbols (on the root): a small box per BOM sheet under its
// page's symbol; on a one-page project next to the drawing.
const (
	sbBomSymH     = 5.08
	sbBomSymGap   = 6.35  // page symbol bottom (and its Sheetfile text) → first BOM symbol
	sbBomSymPitch = 11.43 // box + Sheetname above + Sheetfile below
)

func sbBomSymW(name, file string) float64 {
	return math.Max(30.48, math.Ceil(math.Max(sbTextW(name), sbTextW(file))/2.54)*2.54)
}

// sbBomSymSpot is where the BOM sheet symbols of a one-page project go
// (top-left of the first, sheet mm before the fit shift): under the drawing
// when the page's paper has the room there, else right of it, else under it
// (the paper grows).
func sbBomSymSpot(fr kicad.FitResult, name, file string, n int) (x, y float64) {
	c := fr.Content
	if !fr.ContentFound {
		return 25.4, 25.4
	}
	up := func(v float64) float64 { return math.Ceil(v/1.27) * 1.27 }
	w := sbBomSymW(name, file)
	h := float64(n-1)*sbBomSymPitch + sbBomSymH + 2.54 // + the Sheetfile text
	bx, by := up(c.MinX), up(c.MaxY+5.08)
	if fr.TooBig {
		return bx, by
	}
	pw, ph := sbPaperSize(fr.To)
	clear := func(x0, y0 float64) bool { // box x0,y0,w,h (shifted) inside the paper, off the title block
		x0, y0 = x0+fr.ShiftX, y0+fr.ShiftY
		if x0 < sbBomBorder || y0-2.54 < sbBomBorder || x0+w > pw-sbBomBorder || y0+h > ph-sbBomBorder {
			return false
		}
		return x0+w <= pw-10-112-5 || y0+h <= ph-10-34-5
	}
	if clear(bx, by) {
		return bx, by
	}
	if rx, ry := up(c.MaxX+5.08), up(c.MinY+2.54); clear(rx, ry) {
		return rx, ry
	}
	return bx, by
}

// sbBomSummary starts the report's "bom" section from the resolved design
// (the sheets are added when the project is written) and returns the
// warnings a build prints about it.
func sbBomSummary(d *sbDesign, o sbOptions) (*sbBomReport, []string) {
	b := &sbBomReport{Sheets: !o.NoBomPages, PackageText: !o.NoPackageText, Parts: len(d.Parts),
		Pages: []sbBomPage{}, NoPackage: []string{}, NoSpec: []string{}, Missing: []sbBomMissing{}}
	b.Catalog = sbBomCatalog(d.Parts, o.BomCatalog && !o.Offline)
	np, ns, miss := sbBomCheck(d.Parts)
	b.NoPackage, b.NoSpec, b.Missing = append(b.NoPackage, np...), append(b.NoSpec, ns...), append(b.Missing, miss...)
	var warn []string
	if len(np) > 0 {
		warn = append(warn, fmt.Sprintf("no package text for %s: no footprint (add footprint, lcsc or fields.Package)", sbShortList(np, 12)))
	}
	if c := b.Catalog; len(c.Failed) > 0 {
		warn = append(warn, fmt.Sprintf("BOM: the LCSC catalogue did not answer for %s — those rows show only what the spec says; run again with --bom-catalog", sbShortList(c.Failed, 12)))
	}
	if c := b.Catalog; len(c.Unknown) > 0 {
		warn = append(warn, fmt.Sprintf("BOM: the LCSC catalogue has no record of %s — check the number (kicad lcsc --verify)", sbShortList(c.Unknown, 12)))
	}
	if c := b.Catalog; !o.BomCatalog && c.Numbers > c.Known {
		warn = append(warn, fmt.Sprintf("BOM: %d of %d LCSC numbers have no cached catalogue record, so their MPN / description are not filled in — build with --bom-catalog (network) to fetch them", c.Numbers-c.Known, c.Numbers))
	}
	if len(miss) > 0 {
		refs := map[string]bool{}
		var list []string
		for _, m := range miss {
			if !refs[m.Ref] {
				refs[m.Ref] = true
				list = append(list, m.Ref)
			}
		}
		warn = append(warn, fmt.Sprintf("BOM: %d part(s) a reader cannot identify: %s — see bom.missing in the report for the fix of each (it must be empty before the schematic is handed over)", len(list), sbShortList(list, 12)))
	}
	return b, warn
}

func sbShortList(refs []string, n int) string {
	if len(refs) <= n {
		return strings.Join(refs, ", ")
	}
	return strings.Join(refs[:n], ", ") + fmt.Sprintf(" … (+%d)", len(refs)-n)
}
