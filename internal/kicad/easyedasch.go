package kicad

// easyedasch.go — EasyEDA Pro schematic → KiCad (.epro project export →
// hierarchical .kicad_sch). KiCad 10 has an EasyEDA Pro importer, but only
// inside the eeschema GUI (kicad-cli cannot load .epro and eeschema has no
// scripting API), so pcbpilot converts the project itself:
//
//   - one sub-sheet per EasyEDA page under a root sheet;
//   - symbols from SYMBOL/*.esym (pins with number, name, position,
//     orientation, length; body polylines, rectangles, circles, ellipses,
//     arcs, texts; multi-part symbols become KiCad units);
//   - every part with reference, value, footprint (the KiCad board's
//     footprint of that reference when a board is given, else the EasyEDA
//     footprint title) and the LCSC number ("Supplier Part") plus MPN and
//     manufacturer fields;
//   - wires (split at T-points so every connection is an end-point one;
//     junction dots where three or more wires meet), power flags as KiCad
//     global power symbols, net ports as global labels, no-connect flags;
//   - EasyEDA wires carry their net name (global across pages): a wire
//     cluster whose name is not already given by a power flag or net port
//     gets a label — global when the name is used on another page, local
//     otherwise — so KiCad's connectivity equals EasyEDA's.
//
// EasyEDA schematic units are 10 mil (0.254 mm), y up; the page maps to
// KiCad y-down millimetres. Reference connectivity is derived from the
// EasyEDA data independently (Result.EasyedaPinNets) for the
// net-by-net comparison with `kicad-cli sch export netlist`.

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const eeUnit = 0.254 // mm per EasyEDA schematic unit

// EasyedaSchOptions selects the board and the output.
type EasyedaSchOptions struct {
	Board  string // project.json boards key; empty: the only board
	OutDir string
	Name   string // root file base name (Name.kicad_sch); default: the board name
	PCB    string // optional .kicad_pcb: footprint names by reference
}

// EasyedaSchResult summarises a conversion.
type EasyedaSchResult struct {
	Root       string            `json:"root"`
	Sheets     []string          `json:"sheets"`
	Parts      int               `json:"parts"`
	Power      int               `json:"powerSymbols"`
	Ports      int               `json:"globalLabelsFromPorts"`
	Labels     int               `json:"labelsFromWireNames"`
	Wires      int               `json:"wires"`
	Junctions  int               `json:"junctions"`
	NoConnects int               `json:"noConnects"`
	Warnings   []string          `json:"warnings,omitempty"`
	PinNets    map[string]string `json:"-"` // EasyEDA reference connectivity "REF.PIN" → net
}

type eeRec []any

func (r eeRec) s(i int) string {
	if i < len(r) {
		if v, ok := r[i].(string); ok {
			return v
		}
	}
	return ""
}

func (r eeRec) f(i int) float64 {
	if i < len(r) {
		if v, ok := r[i].(float64); ok {
			return v
		}
	}
	return 0
}

func (r eeRec) isNum(i int) bool {
	if i < len(r) {
		_, ok := r[i].(float64)
		return ok
	}
	return false
}

func parseEeLines(data []byte) ([]eeRec, error) {
	var out []eeRec
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var r eeRec
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("line %d: %w", i+1, err)
		}
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return out, nil
}

// ---- symbols --------------------------------------------------------------

type eePin struct {
	id, num, name, typ string
	x, y, length, rot  float64
	hidden             bool
	nameVis, numVis    bool
}

type eePart struct {
	name string
	pins []eePin
	gfx  []eeRec
}

type eeSymbol struct {
	uuid, title, kind string // kind: Symbol attr ("Ground-GND", "Netport-BI", …)
	symType           int
	prefix            string
	parts             []*eePart
}

func parseEsym(uuid string, data []byte) (*eeSymbol, error) {
	recs, err := parseEeLines(data)
	if err != nil {
		return nil, err
	}
	s := &eeSymbol{uuid: uuid}
	var cur *eePart
	pins := map[string]*eePin{}
	for _, r := range recs {
		switch r.s(0) {
		case "HEAD":
			if m, ok := r[1].(map[string]any); ok {
				if v, ok := m["symbolType"].(float64); ok {
					s.symType = int(v)
				}
			}
		case "PART":
			cur = &eePart{name: r.s(1)}
			s.parts = append(s.parts, cur)
		case "PIN":
			if cur == nil {
				cur = &eePart{}
				s.parts = append(s.parts, cur)
			}
			p := eePin{id: r.s(1), x: r.f(4), y: r.f(5), length: r.f(6), rot: r.f(7), hidden: r.isNum(2) && r.f(2) == 0}
			cur.pins = append(cur.pins, p)
			pins[p.id] = &cur.pins[len(cur.pins)-1]
		case "ATTR":
			parent, key, val := r.s(2), r.s(3), r.s(4)
			if parent == "" {
				switch key {
				case "Symbol":
					s.kind = val
				case "Designator":
					s.prefix = strings.TrimRight(val, "?")
				}
				continue
			}
			// pins are appended to slices: resolve through the part again
			for _, pt := range s.parts {
				for i := range pt.pins {
					if pt.pins[i].id != parent {
						continue
					}
					p := &pt.pins[i]
					vis := r.isNum(6) && r.f(6) == 1
					switch key {
					case "NAME":
						p.name, p.nameVis = val, vis
					case "NUMBER":
						p.num, p.numVis = val, vis
					case "Pin Type":
						p.typ = val
					}
				}
			}
		case "POLY", "RECT", "CIRCLE", "ELLIPSE", "ARC", "TEXT":
			if cur != nil {
				cur.gfx = append(cur.gfx, r)
			}
		}
	}
	_ = pins
	return s, nil
}

func (s *eeSymbol) part(name string) (*eePart, int) {
	for i, p := range s.parts {
		if p.name == name {
			return p, i + 1
		}
	}
	if len(s.parts) > 0 {
		return s.parts[0], 1
	}
	return &eePart{}, 1
}

var kicadNameBad = regexp.MustCompile(`[^A-Za-z0-9._+\-()#=,]+`)

func kicadItemName(s string) string {
	s = kicadNameBad.ReplaceAllString(strings.TrimSpace(s), "_")
	if s == "" {
		s = "symbol"
	}
	return s
}

func eePinType(t string) string {
	switch strings.ToUpper(t) {
	case "OUT":
		return "output"
	case "BI":
		return "bidirectional"
	case "POWER":
		return "power_in"
	case "OPEN COLLECTOR", "OC":
		return "open_collector"
	}
	return "passive" // IN / Undefined: EasyEDA library defaults, not a real direction
}

func mm(v float64) string { return F(v * eeUnit) }

// kicadLibSymbol renders the symbol (all parts as units) with bare name.
func (s *eeSymbol) kicadLibSymbol(name string) string {
	var b strings.Builder
	nameVis, numVis := false, false
	for _, p := range s.parts {
		for _, pin := range p.pins {
			nameVis = nameVis || pin.nameVis
			numVis = numVis || pin.numVis
		}
	}
	fmt.Fprintf(&b, "(symbol %s\n", Q(name))
	if !numVis {
		b.WriteString("\t\t\t(pin_numbers (hide yes))\n")
	}
	if nameVis {
		b.WriteString("\t\t\t(pin_names (offset 0.254))\n")
	} else {
		b.WriteString("\t\t\t(pin_names (offset 0.254) (hide yes))\n")
	}
	b.WriteString("\t\t\t(exclude_from_sim no) (in_bom yes) (on_board yes)\n")
	prefix := s.prefix
	if prefix == "" {
		prefix = "U"
	}
	for _, f := range [][2]string{{"Reference", prefix}, {"Value", name}, {"Footprint", ""}, {"Datasheet", ""}, {"Description", ""}} {
		hide := ""
		if f[0] != "Reference" && f[0] != "Value" {
			hide = " (hide yes)"
		}
		fmt.Fprintf(&b, "\t\t\t(property %s %s (at 0 0 0)%s (effects (font (size 1.27 1.27))))\n", Q(f[0]), Q(f[1]), hide)
	}
	for ui, p := range s.parts {
		fmt.Fprintf(&b, "\t\t\t(symbol %s\n", Q(fmt.Sprintf("%s_%d_1", name, ui+1)))
		for _, g := range p.gfx {
			if t := eeGfx(g); t != "" {
				b.WriteString("\t\t\t\t" + t + "\n")
			}
		}
		for _, pin := range p.pins {
			hide := ""
			if pin.hidden {
				hide = " (hide yes)"
			}
			fmt.Fprintf(&b, "\t\t\t\t(pin %s line (at %s %s %s) (length %s)%s (name %s (effects (font (size 1.016 1.016)))) (number %s (effects (font (size 1.016 1.016)))))\n",
				eePinType(pin.typ), mm(pin.x), mm(pin.y), F(math.Mod(pin.rot+360, 360)), mm(pin.length), hide, Q(pin.name), Q(pin.num))
		}
		b.WriteString("\t\t\t)\n")
	}
	b.WriteString("\t\t)")
	return b.String()
}

const eeStroke = "(stroke (width 0) (type default))"

func eeGfx(g eeRec) string {
	switch g.s(0) {
	case "POLY":
		pts, _ := g[2].([]any)
		closed, _ := g[3].(bool)
		var xy []string
		for i := 0; i+1 < len(pts); i += 2 {
			x, _ := pts[i].(float64)
			y, _ := pts[i+1].(float64)
			xy = append(xy, fmt.Sprintf("(xy %s %s)", mm(x), mm(y)))
		}
		if len(xy) < 2 {
			return ""
		}
		if closed && xy[0] != xy[len(xy)-1] {
			xy = append(xy, xy[0])
		}
		return fmt.Sprintf("(polyline (pts %s) %s (fill (type none)))", strings.Join(xy, " "), eeStroke)
	case "RECT":
		return fmt.Sprintf("(rectangle (start %s %s) (end %s %s) %s (fill (type background)))", mm(g.f(2)), mm(g.f(3)), mm(g.f(4)), mm(g.f(5)), eeStroke)
	case "CIRCLE":
		return fmt.Sprintf("(circle (center %s %s) (radius %s) %s (fill (type none)))", mm(g.f(2)), mm(g.f(3)), mm(g.f(4)), eeStroke)
	case "ELLIPSE":
		cx, cy, rx, ry := g.f(2), g.f(3), g.f(4), g.f(5)
		if math.Abs(rx-ry) < 1e-6 {
			return fmt.Sprintf("(circle (center %s %s) (radius %s) %s (fill (type none)))", mm(cx), mm(cy), mm(rx), eeStroke)
		}
		var xy []string
		for i := 0; i <= 32; i++ {
			a := 2 * math.Pi * float64(i) / 32
			xy = append(xy, fmt.Sprintf("(xy %s %s)", mm(cx+rx*math.Cos(a)), mm(cy+ry*math.Sin(a))))
		}
		return fmt.Sprintf("(polyline (pts %s) %s (fill (type none)))", strings.Join(xy, " "), eeStroke)
	case "ARC":
		return fmt.Sprintf("(arc (start %s %s) (mid %s %s) (end %s %s) %s (fill (type none)))",
			mm(g.f(2)), mm(g.f(3)), mm(g.f(4)), mm(g.f(5)), mm(g.f(6)), mm(g.f(7)), eeStroke)
	case "TEXT":
		if g.s(5) == "" {
			return ""
		}
		return fmt.Sprintf("(text %s (at %s %s %s) (effects (font (size 1.016 1.016)) (justify left bottom)))", Q(g.s(5)), mm(g.f(2)), mm(g.f(3)), F(g.f(4)*10))
	}
	return ""
}

// ---- project ---------------------------------------------------------------

type eproProject struct {
	Boards     map[string]struct{ Schematic, PCB string } `json:"boards"`
	Symbols    map[string]struct{ Title string }          `json:"symbols"`
	Footprints map[string]struct{ Title string }          `json:"footprints"`
	Schematics map[string]struct {
		Name   string `json:"name"`
		Sheets []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"sheets"`
	} `json:"schematics"`
}

func readZip(path string) (map[string][]byte, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !(strings.HasPrefix(f.Name, "SHEET/") || strings.HasPrefix(f.Name, "SYMBOL/") || f.Name == "project.json") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, 64<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		out[f.Name] = b
	}
	return out, nil
}

// ---- sheet model -------------------------------------------------------------

type eeComp struct {
	id, part   string
	x, y, rot  float64
	mirror     bool
	attrs      map[string]eeRec
	sym        *eeSymbol
	unit       *eePart
	unitN      int
	ref        string
	pinPos     map[string][2]float64 // pin number → EasyEDA world point
	pinPosByID map[string][2]float64
}

func (c *eeComp) attr(k string) string {
	if r, ok := c.attrs[k]; ok {
		return r.s(4)
	}
	return ""
}

func eeRot(x, y, deg float64, mirror bool) (float64, float64) {
	if mirror {
		x = -x
	}
	a := deg * math.Pi / 180
	cs, sn := math.Cos(a), math.Sin(a)
	return round6(x*cs - y*sn), round6(x*sn + y*cs)
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

type eeSeg struct{ a, b [2]float64 }

type eeSheet struct {
	idx      int
	name     string
	comps    []*eeComp
	segs     []eeSeg
	wireNets []struct {
		pts [][2]float64
		net string
	}
	ncs   []eeRec // NO_CONNECT attrs
	texts []eeRec
	rects []eeRec
	attrs map[string]map[string]eeRec // parent → key → attr (all)
}

func parseEsch(data []byte) (*eeSheet, error) {
	recs, err := parseEeLines(data)
	if err != nil {
		return nil, err
	}
	sh := &eeSheet{attrs: map[string]map[string]eeRec{}}
	comps := map[string]*eeComp{}
	wires := map[string][][2]float64{}
	var wireOrder []string
	for _, r := range recs {
		switch r.s(0) {
		case "COMPONENT":
			c := &eeComp{id: r.s(1), part: r.s(2), x: r.f(3), y: r.f(4), rot: r.f(5), mirror: r.f(6) != 0, attrs: map[string]eeRec{}}
			comps[c.id] = c
			sh.comps = append(sh.comps, c)
		case "ATTR":
			p := r.s(2)
			if sh.attrs[p] == nil {
				sh.attrs[p] = map[string]eeRec{}
			}
			sh.attrs[p][r.s(3)] = r
			if c, ok := comps[p]; ok {
				c.attrs[r.s(3)] = r
			}
			if r.s(3) == "NO_CONNECT" && r.s(4) == "yes" {
				sh.ncs = append(sh.ncs, r)
			}
		case "WIRE":
			lines, _ := r[2].([]any)
			var pts [][2]float64
			for _, l := range lines {
				nums, _ := l.([]any)
				var prev *[2]float64
				for i := 0; i+1 < len(nums); i += 2 {
					x, _ := nums[i].(float64)
					y, _ := nums[i+1].(float64)
					p := [2]float64{x, y}
					pts = append(pts, p)
					if prev != nil && *prev != p {
						sh.segs = append(sh.segs, eeSeg{*prev, p})
					}
					pp := p
					prev = &pp
				}
			}
			wires[r.s(1)] = pts
			wireOrder = append(wireOrder, r.s(1))
		case "TEXT":
			sh.texts = append(sh.texts, r)
		case "RECT":
			sh.rects = append(sh.rects, r)
		}
	}
	for _, id := range wireOrder {
		net := ""
		if a := sh.attrs[id]["NET"]; a != nil {
			net = a.s(4)
		}
		sh.wireNets = append(sh.wireNets, struct {
			pts [][2]float64
			net string
		}{wires[id], net})
	}
	return sh, nil
}

// ---- union-find ---------------------------------------------------------------

type uf map[string]string

func (u uf) find(k string) string {
	if _, ok := u[k]; !ok {
		u[k] = k
	}
	for u[k] != k {
		u[k] = u[u[k]]
		k = u[k]
	}
	return k
}

func (u uf) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		if ra < rb {
			u[rb] = ra
		} else {
			u[ra] = rb
		}
	}
}

func ptKey(sheet int, p [2]float64) string {
	return fmt.Sprintf("s%d:%g,%g", sheet, round6(p[0]), round6(p[1]))
}

func onSeg(p [2]float64, s eeSeg) bool {
	if p == s.a || p == s.b {
		return false
	}
	cross := (s.b[0]-s.a[0])*(p[1]-s.a[1]) - (s.b[1]-s.a[1])*(p[0]-s.a[0])
	if math.Abs(cross) > 1e-6 {
		return false
	}
	return p[0] >= math.Min(s.a[0], s.b[0])-1e-9 && p[0] <= math.Max(s.a[0], s.b[0])+1e-9 &&
		p[1] >= math.Min(s.a[1], s.b[1])-1e-9 && p[1] <= math.Max(s.a[1], s.b[1])+1e-9
}

// splitSegs splits every segment at the points of interest lying inside
// it and drops duplicates.
func splitSegs(segs []eeSeg, poi map[[2]float64]bool) []eeSeg {
	var out []eeSeg
	seen := map[[4]float64]bool{}
	for _, s := range segs {
		var cut [][2]float64
		for p := range poi {
			if onSeg(p, s) {
				cut = append(cut, p)
			}
		}
		d := func(p [2]float64) float64 { return math.Hypot(p[0]-s.a[0], p[1]-s.a[1]) }
		sort.Slice(cut, func(i, j int) bool { return d(cut[i]) < d(cut[j]) })
		pts := append([][2]float64{s.a}, cut...)
		pts = append(pts, s.b)
		for i := 0; i+1 < len(pts); i++ {
			a, b := pts[i], pts[i+1]
			if a == b {
				continue
			}
			k := [4]float64{a[0], a[1], b[0], b[1]}
			if a[0] > b[0] || (a[0] == b[0] && a[1] > b[1]) {
				k = [4]float64{b[0], b[1], a[0], a[1]}
			}
			if !seen[k] {
				seen[k] = true
				out = append(out, eeSeg{a, b})
			}
		}
	}
	return out
}

// ---- conversion ---------------------------------------------------------------

// ConvertEasyedaSchematic converts one board's schematic of an EasyEDA Pro
// .epro export into Name.kicad_sch (root) + one sub-sheet per page in
// OutDir, and writes Name.kicad_pro (when missing), easyeda.kicad_sym and a
// sym-lib-table naming it (when missing).
func ConvertEasyedaSchematic(eproPath string, opt EasyedaSchOptions) (*EasyedaSchResult, error) {
	files, err := readZip(eproPath)
	if err != nil {
		return nil, err
	}
	var proj eproProject
	if err := json.Unmarshal(files["project.json"], &proj); err != nil {
		return nil, fmt.Errorf("project.json: %w", err)
	}
	board := opt.Board
	if board == "" {
		if len(proj.Boards) != 1 {
			var names []string
			for k := range proj.Boards {
				names = append(names, k)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("the project has %d boards — choose one with --board: %s", len(names), strings.Join(names, ", "))
		}
		for k := range proj.Boards {
			board = k
		}
	}
	bd, ok := proj.Boards[board]
	if !ok {
		return nil, fmt.Errorf("board %q not in project.json", board)
	}
	schm, ok := proj.Schematics[bd.Schematic]
	if !ok {
		return nil, fmt.Errorf("schematic %s of board %s not in project.json", bd.Schematic, board)
	}
	name := opt.Name
	if name == "" {
		name = kicadItemName(board)
	}
	fpByRef := map[string]string{}
	if opt.PCB != "" {
		fpByRef, err = PCBFootprintsByRef(opt.PCB)
		if err != nil {
			return nil, err
		}
	}
	res := &EasyedaSchResult{PinNets: map[string]string{}}
	warn := func(f string, a ...any) { res.Warnings = append(res.Warnings, fmt.Sprintf(f, a...)) }

	// symbols
	syms := map[string]*eeSymbol{}
	symbol := func(uuid string) *eeSymbol {
		if s, ok := syms[uuid]; ok {
			return s
		}
		data, ok := files["SYMBOL/"+uuid+".esym"]
		if !ok {
			syms[uuid] = nil
			return nil
		}
		s, err := parseEsym(uuid, data)
		if err != nil {
			warn("symbol %s: %v", uuid, err)
			syms[uuid] = nil
			return nil
		}
		s.title = proj.Symbols[uuid].Title
		syms[uuid] = s
		return s
	}

	// pages, in page order
	pages := append([]struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}(nil), schm.Sheets...)
	sort.Slice(pages, func(i, j int) bool { return pages[i].ID < pages[j].ID })
	var sheets []*eeSheet
	for i, pg := range pages {
		data, ok := files[fmt.Sprintf("SHEET/%s/%d.esch", bd.Schematic, pg.ID)]
		if !ok {
			return nil, fmt.Errorf("page %s (%d) missing from the export", pg.Name, pg.ID)
		}
		sh, err := parseEsch(data)
		if err != nil {
			return nil, fmt.Errorf("page %s: %w", pg.Name, err)
		}
		sh.idx, sh.name = i, pg.Name
		sheets = append(sheets, sh)
	}

	// resolve components, pin world positions, global name use per sheet
	nameSheets := map[string]map[int]bool{}
	useName := func(n string, si int) {
		if n == "" {
			return
		}
		if nameSheets[n] == nil {
			nameSheets[n] = map[int]bool{}
		}
		nameSheets[n][si] = true
	}
	for _, sh := range sheets {
		for _, c := range sh.comps {
			c.sym = symbol(c.attr("Symbol"))
			if c.sym == nil {
				continue
			}
			c.unit, c.unitN = c.sym.part(c.part)
			c.ref = c.attr("Designator")
			c.pinPos, c.pinPosByID = map[string][2]float64{}, map[string][2]float64{}
			for _, p := range c.unit.pins {
				dx, dy := eeRot(p.x, p.y, c.rot, c.mirror)
				w := [2]float64{round6(c.x + dx), round6(c.y + dy)}
				c.pinPosByID[p.id] = w
				if _, dup := c.pinPos[p.num]; !dup {
					c.pinPos[p.num] = w
				}
			}
			switch c.sym.symType {
			case 18:
				useName(c.attr("Global Net Name"), sh.idx)
			case 19:
				useName(c.attr("Name"), sh.idx)
			}
		}
		for _, w := range sh.wireNets {
			useName(w.net, sh.idx)
		}
	}

	// EasyEDA reference connectivity
	u := uf{}
	partPins := map[string]bool{}
	for _, sh := range sheets {
		poi := map[[2]float64]bool{}
		for _, s := range sh.segs {
			poi[s.a], poi[s.b] = true, true
		}
		for _, c := range sh.comps {
			for _, p := range c.pinPos {
				poi[p] = true
			}
		}
		for _, s := range splitSegs(sh.segs, poi) {
			u.union(ptKey(sh.idx, s.a), ptKey(sh.idx, s.b))
		}
		for _, w := range sh.wireNets {
			if w.net == "" {
				continue
			}
			for _, p := range w.pts {
				u.union(ptKey(sh.idx, p), "N:"+w.net)
			}
		}
		for _, c := range sh.comps {
			if c.sym == nil {
				continue
			}
			switch c.sym.symType {
			case 18, 19:
				n := c.attr("Global Net Name")
				if c.sym.symType == 19 {
					n = c.attr("Name")
				}
				for _, p := range c.pinPos {
					if n != "" {
						u.union(ptKey(sh.idx, p), "N:"+n)
					}
				}
			case 20:
			default:
				if c.ref == "" {
					continue
				}
				for num, p := range c.pinPos {
					k := c.ref + "." + num
					partPins[k] = true
					u.union(ptKey(sh.idx, p), "P:"+k)
				}
			}
		}
	}
	groupName := map[string]string{}
	for k := range u {
		if strings.HasPrefix(k, "N:") {
			r := u.find(k)
			if old, ok := groupName[r]; !ok || k[2:] < old {
				groupName[r] = k[2:]
			}
		}
	}
	groupSize := map[string]int{}
	for k := range partPins {
		groupSize[u.find("P:"+k)]++
	}
	for k := range partPins {
		r := u.find("P:" + k)
		n, ok := groupName[r]
		if !ok {
			if groupSize[r] == 1 {
				n = "unconnected-(" + k + ")"
			} else {
				n = "$" + r
			}
		}
		res.PinNets[k] = n
	}

	// ---- write KiCad -------------------------------------------------------------
	if err := os.MkdirAll(opt.OutDir, 0o755); err != nil {
		return nil, err
	}
	rootUUID := NewUUID()
	libNames := map[string]string{} // symbol uuid → bare KiCad name
	usedNames := map[string]bool{}
	libText := map[string]string{}
	pwr := 1
	powerNets := map[string]bool{} // net → ground style
	type sheetOut struct{ file, name, symUUID string }
	var outs []sheetOut
	for _, sh := range sheets {
		H := 0.0
		if fr := frameHeight(sh); fr > 0 {
			H = math.Ceil(fr/5) * 5
		} else {
			H = 1655
		}
		paper := "A2"
		for _, c := range sh.comps {
			if c.sym != nil && c.sym.symType == 20 {
				if v := c.attr("Page Size"); v == "A4" || v == "A3" || v == "A2" || v == "A1" || v == "A0" {
					paper = v
				}
			}
		}
		toK := func(p [2]float64) Pt { return Pt{round6(p[0] * eeUnit), round6((H - p[1]) * eeUnit)} }
		file := fmt.Sprintf("%s_%s.kicad_sch", name, kicadItemName(sh.name))
		so := sheetOut{file: file, name: sh.name, symUUID: NewUUID()}
		outs = append(outs, so)
		e, err := OpenSchematic(NewSchematicText(paper, NewUUID(), false))
		if err != nil {
			return nil, err
		}
		e.Project, e.InstancePath, e.pwrNext = name, "/"+rootUUID+"/"+so.symUUID, pwr

		// points of interest for wire splitting and clusters
		poi := map[[2]float64]bool{}
		for _, s := range sh.segs {
			poi[s.a], poi[s.b] = true, true
		}
		pinAt := map[[2]float64]int{}
		for _, c := range sh.comps {
			if c.sym == nil || c.sym.symType == 20 {
				continue
			}
			for _, p := range c.pinPos {
				poi[p] = true
				pinAt[p]++
			}
		}
		segs := splitSegs(sh.segs, poi)
		cu := uf{}
		ends := map[[2]float64]int{}
		for _, s := range segs {
			e.AddWire(toK(s.a), toK(s.b))
			res.Wires++
			ends[s.a]++
			ends[s.b]++
			cu.union(ptKey(0, s.a), ptKey(0, s.b))
		}
		for p, n := range ends {
			if n+pinAt[p] >= 3 && n >= 2 {
				e.AddJunction(toK(p))
				res.Junctions++
			}
		}
		covered := map[string]map[string]bool{} // cluster → names given by flags/ports
		cover := func(p [2]float64, n string) {
			r := cu.find(ptKey(0, p))
			if covered[r] == nil {
				covered[r] = map[string]bool{}
			}
			covered[r][n] = true
		}

		for _, c := range sh.comps {
			if c.sym == nil {
				if c.attr("Symbol") != "" {
					warn("page %s: component %s uses symbol %s missing from the export — skipped", sh.name, c.id, c.attr("Symbol"))
				}
				continue
			}
			switch c.sym.symType {
			case 20: // page frame
				continue
			case 18: // power / ground flag
				net := c.attr("Global Net Name")
				if net == "" {
					warn("page %s: power flag %s without a net name — skipped", sh.name, c.id)
					continue
				}
				ground := strings.HasPrefix(strings.ToLower(c.sym.kind), "ground") || IsGroundNet(net) && !strings.HasPrefix(strings.ToLower(c.sym.kind), "power")
				for _, p := range c.pinPos {
					powerNets[net] = ground
					if _, err := e.AddPower(net, toK(p), c.rot, ground); err != nil {
						return nil, err
					}
					cover(p, net)
					res.Power++
					break
				}
				continue
			case 19: // net port → global label
				net := c.attr("Name")
				shape := "bidirectional"
				switch k := strings.ToUpper(c.sym.kind); {
				case strings.HasSuffix(k, "-IN"):
					shape = "input"
				case strings.HasSuffix(k, "-OUT"):
					shape = "output"
				}
				for _, p := range c.pinPos {
					if err := e.AddLabel(LabelGlobal, net, toK(p), math.Mod(c.rot+360, 360), shape); err != nil {
						warn("page %s: net port %s: %v", sh.name, c.id, err)
					}
					cover(p, net)
					res.Ports++
					break
				}
				continue
			}
			if c.ref == "" {
				warn("page %s: part %s (%s) has no designator — skipped", sh.name, c.id, c.part)
				continue
			}
			bare, ok := libNames[c.sym.uuid]
			if !ok {
				base := kicadItemName(c.sym.title)
				bare = base
				for i := 2; usedNames[bare]; i++ {
					bare = fmt.Sprintf("%s_%d", base, i)
				}
				usedNames[bare] = true
				libNames[c.sym.uuid] = bare
				libText[bare] = c.sym.kicadLibSymbol(bare)
			}
			libID := "easyeda:" + bare
			if err := e.AddLibSymbol(libID, libText[bare]); err != nil {
				return nil, err
			}
			// orientation: the KiCad rotation/mirror that puts every pin
			// where EasyEDA has it.
			at := toK([2]float64{c.x, c.y})
			rot, mir, okO := 0.0, "", false
			for _, m := range []string{"", "y"} {
				for _, r := range []float64{0, 90, 180, 270} {
					good := true
					for _, p := range c.unit.pins {
						k := SymbolXform(Pt{p.x * eeUnit, p.y * eeUnit}, at, r, m)
						w := toK(c.pinPosByID[p.id])
						if math.Abs(k.X-w.X) > 1e-4 || math.Abs(k.Y-w.Y) > 1e-4 {
							good = false
							break
						}
					}
					if good {
						rot, mir, okO = r, m, true
						break
					}
				}
				if okO {
					break
				}
			}
			if !okO {
				warn("%s: no KiCad orientation reproduces the EasyEDA pin positions (rotation %v, mirror %v)", c.ref, c.rot, c.mirror)
			}
			value := c.attr("Value")
			if value == "" {
				value = c.attr("Name")
				if strings.HasPrefix(value, "={") && strings.HasSuffix(value, "}") {
					value = c.attr(value[2 : len(value)-1])
				}
			}
			if value == "" {
				value = c.attr("Manufacturer Part")
			}
			fp := fpByRef[c.ref]
			if fp == "" {
				fp = proj.Footprints[c.attr("Footprint")].Title
			}
			// field angles are in the symbol frame: keep EasyEDA's horizontal
			// text on symbols turned by 90/270.
			fa := 0.0
			if rot == 90 || rot == 270 {
				fa = 90
			}
			fields := []Field{}
			if a := c.attrs["Designator"]; a != nil && a.isNum(7) {
				p := toK([2]float64{a.f(7), a.f(8)})
				fields = append(fields, Field{Name: "Reference", Value: c.ref, At: &p, Angle: fa, Hide: !(a.isNum(6) && a.f(6) == 1)})
			}
			vis := false
			for _, k := range []string{"Value", "Name"} {
				if a := c.attrs[k]; a != nil && a.isNum(7) {
					p := toK([2]float64{a.f(7), a.f(8)})
					vis = a.isNum(6) && a.f(6) == 1
					fields = append(fields, Field{Name: "Value", Value: value, At: &p, Angle: fa, Hide: !vis})
					break
				}
			}
			for _, kv := range [][2]string{{"LCSC", "Supplier Part"}, {"MPN", "Manufacturer Part"}, {"Manufacturer", "Manufacturer"}, {"Datasheet", "Datasheet"}} {
				if v := c.attr(kv[1]); v != "" {
					fields = append(fields, Field{Name: kv[0], Value: v, Hide: true})
				}
			}
			inBOM := c.attr("Add into BOM") != "no"
			if _, err := e.PlaceSymbol(SymbolInstance{LibID: libID, Ref: c.ref, Unit: c.unitN, At: at, Rot: rot, Mirror: mir,
				Value: value, Footprint: fp, Fields: fields, InBOM: &inBOM}); err != nil {
				return nil, err
			}
			res.Parts++
		}

		// no-connects: parent is <component id><pin id>
		for _, a := range sh.ncs {
			parent := a.s(2)
			placed := false
			for _, c := range sh.comps {
				if c.pinPosByID == nil || !strings.HasPrefix(parent, c.id) {
					continue
				}
				if p, ok := c.pinPosByID[parent[len(c.id):]]; ok {
					e.AddNoConnect(toK(p))
					res.NoConnects++
					placed = true
					break
				}
			}
			if !placed {
				warn("page %s: no-connect %s matches no pin", sh.name, parent)
			}
		}

		// wire names → labels
		clusterNames := map[string]map[string]bool{}
		clusterPts := map[string][][2]float64{}
		for _, w := range sh.wireNets {
			if w.net == "" || len(w.pts) == 0 {
				continue
			}
			r := cu.find(ptKey(0, w.pts[0]))
			if clusterNames[r] == nil {
				clusterNames[r] = map[string]bool{}
			}
			clusterNames[r][w.net] = true
			clusterPts[r] = append(clusterPts[r], w.pts...)
		}
		var roots []string
		for r := range clusterNames {
			roots = append(roots, r)
		}
		sort.Strings(roots)
		for _, r := range roots {
			var names []string
			for n := range clusterNames[r] {
				if !covered[r][n] {
					names = append(names, n)
				}
			}
			sort.Strings(names)
			if len(clusterNames[r])+len(covered[r]) > 1 {
				all := map[string]bool{}
				for n := range clusterNames[r] {
					all[n] = true
				}
				for n := range covered[r] {
					all[n] = true
				}
				if len(all) > 1 {
					var l []string
					for n := range all {
						l = append(l, n)
					}
					sort.Strings(l)
					warn("page %s: one wire cluster carries several net names %v (EasyEDA merges them)", sh.name, l)
				}
			}
			if len(names) == 0 {
				continue
			}
			// a free wire end (no pin) reads best; else any wire end
			pts := clusterPts[r]
			best := pts[0]
			for _, p := range pts {
				if ends[p] == 1 && pinAt[p] == 0 {
					best = p
					break
				}
			}
			for _, n := range names {
				kind := LabelLocal
				if len(nameSheets[n]) > 1 {
					kind = LabelGlobal
				}
				if err := e.AddLabel(kind, n, toK(best), 0, ""); err != nil {
					return nil, err
				}
				res.Labels++
			}
		}

		// page graphics: block frames and notes
		for _, t := range sh.texts {
			if t.s(5) != "" {
				e.AddText(t.s(5), toK([2]float64{t.f(2), t.f(3)}), t.f(4), 1.524)
			}
		}
		for _, rc := range sh.rects {
			e.AddRect(toK([2]float64{rc.f(2), rc.f(3)}), toK([2]float64{rc.f(4), rc.f(5)}))
		}
		pwr = e.pwrNext
		text, err := e.Render()
		if err != nil {
			return nil, fmt.Errorf("page %s: %w", sh.name, err)
		}
		if err := os.WriteFile(filepath.Join(opt.OutDir, file), []byte(text), 0o644); err != nil {
			return nil, err
		}
		res.Sheets = append(res.Sheets, filepath.Join(opt.OutDir, file))
	}

	// root sheet with one sheet symbol per page
	rootText := strings.Replace(NewSchematicText("A4", rootUUID, true), "(paper \"A4\")",
		"(paper \"A4\")\n\t(title_block\n\t\t(title "+Q(board)+")\n\t)", 1)
	re, err := OpenSchematic(rootText)
	if err != nil {
		return nil, err
	}
	for i, so := range outs {
		x, y := 25.4+float64(i%4)*63.5, 38.1+float64(i/4)*30.48
		re.AddRaw(fmt.Sprintf(`(sheet
		(at %s %s)
		(size 50.8 15.24)
		(exclude_from_sim no) (in_bom yes) (on_board yes) (dnp no)
		(stroke (width 0.1524) (type solid))
		(fill (color 0 0 0 0.0000))
		(uuid %s)
		(property "Sheetname" %s (at %s %s 0) (effects (font (size 1.27 1.27)) (justify left bottom)))
		(property "Sheetfile" %s (at %s %s 0) (effects (font (size 1.27 1.27)) (justify left top)))
		(instances (project %s (path %s (page %s))))
	)`, F(x), F(y), Q(so.symUUID), Q(so.name), F(x), F(y-0.7), Q(so.file), F(x), F(y+15.84), Q(name), Q("/"+rootUUID), Q(strconv.Itoa(i+2))))
	}
	rt, err := re.Render()
	if err != nil {
		return nil, err
	}
	res.Root = filepath.Join(opt.OutDir, name+".kicad_sch")
	if err := os.WriteFile(res.Root, []byte(rt), 0o644); err != nil {
		return nil, err
	}

	// project file, symbol library and its table (kept when present)
	pro := filepath.Join(opt.OutDir, name+".kicad_pro")
	if _, err := os.Stat(pro); os.IsNotExist(err) {
		b, _ := json.MarshalIndent(map[string]any{"meta": map[string]any{"filename": name + ".kicad_pro", "version": 3}}, "", "  ")
		if err := os.WriteFile(pro, append(b, '\n'), 0o644); err != nil {
			return nil, err
		}
	}
	var lb strings.Builder
	lb.WriteString("(kicad_symbol_lib\n\t(version 20251024)\n\t(generator \"pcbpilot\")\n")
	var bares []string
	for b := range libText {
		bares = append(bares, b)
	}
	sort.Strings(bares)
	for _, b := range bares {
		lb.WriteString("\t" + libText[b] + "\n")
	}
	lb.WriteString(")\n")
	if err := os.WriteFile(filepath.Join(opt.OutDir, "easyeda.kicad_sym"), []byte(lb.String()), 0o644); err != nil {
		return nil, err
	}
	var pb strings.Builder
	pb.WriteString("(kicad_symbol_lib\n\t(version 20251024)\n\t(generator \"pcbpilot\")\n")
	var pnets []string
	for n := range powerNets {
		pnets = append(pnets, n)
	}
	sort.Strings(pnets)
	for _, n := range pnets {
		t, _ := RenameLibSymbol(PowerSymbolText(n, powerNets[n]), n)
		pb.WriteString("\t" + t + "\n")
	}
	pb.WriteString(")\n")
	if err := os.WriteFile(filepath.Join(opt.OutDir, "pcbpilot_power.kicad_sym"), []byte(pb.String()), 0o644); err != nil {
		return nil, err
	}
	slt := filepath.Join(opt.OutDir, "sym-lib-table")
	if _, err := os.Stat(slt); os.IsNotExist(err) {
		_ = os.WriteFile(slt, []byte("(sym_lib_table\n\t(version 7)\n"+
			"\t(lib (name \"easyeda\") (type \"KiCad\") (uri \"${KIPRJMOD}/easyeda.kicad_sym\") (options \"\") (descr \"converted from EasyEDA Pro by pcbpilot\"))\n"+
			"\t(lib (name \"pcbpilot_power\") (type \"KiCad\") (uri \"${KIPRJMOD}/pcbpilot_power.kicad_sym\") (options \"\") (descr \"pcbpilot power symbols\"))\n)\n"), 0o644)
	} else if b, _ := os.ReadFile(slt); !strings.Contains(string(b), `"easyeda"`) || !strings.Contains(string(b), `"pcbpilot_power"`) {
		warn("sym-lib-table exists without \"easyeda\"/\"pcbpilot_power\" entries: add ${KIPRJMOD}/easyeda.kicad_sym and ${KIPRJMOD}/pcbpilot_power.kicad_sym")
	}
	return res, nil
}

// frameHeight is the EasyEDA page height (frame "Height" attr, units).
func frameHeight(sh *eeSheet) float64 {
	for _, c := range sh.comps {
		if v := c.attr("Height"); v != "" {
			if h, err := strconv.ParseFloat(v, 64); err == nil {
				return h
			}
		}
	}
	return 0
}

// PCBFootprintsByRef reads footprint lib ids by reference from a
// .kicad_pcb (for the schematic Footprint field).
func PCBFootprintsByRef(path string) (map[string]string, error) {
	fps, err := pcbFootprints(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range fps {
		if f.ref != "" {
			out[f.ref] = f.lib
		}
	}
	return out, nil
}

// PCBPadNets reads "REF.PAD" → net name from a .kicad_pcb (pads without a
// net are left out).
func PCBPadNets(path string) (map[string]string, error) {
	fps, err := pcbFootprints(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, f := range fps {
		for pad, net := range f.pads {
			if f.ref != "" && pad != "" && net != "" {
				out[f.ref+"."+pad] = net
			}
		}
	}
	return out, nil
}

type pcbFP struct {
	ref, lib string
	pads     map[string]string
}

func pcbFootprints(path string) ([]pcbFP, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := parseSx(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	netNames := map[string]string{}
	for _, n := range root.list {
		if n.head() == "net" && len(n.list) > 2 {
			netNames[n.list[1].atom] = n.list[2].atom
		}
	}
	var out []pcbFP
	for _, f := range root.list {
		if f.head() != "footprint" || len(f.list) < 2 {
			continue
		}
		fp := pcbFP{lib: f.list[1].atom, ref: propVal(f, "Reference"), pads: map[string]string{}}
		if fp.ref == "" {
			for _, c := range f.list {
				if c.head() == "fp_text" && len(c.list) > 2 && c.list[1].atom == "reference" {
					fp.ref = c.list[2].atom
				}
			}
		}
		for _, p := range f.list {
			if p.head() != "pad" || len(p.list) < 2 {
				continue
			}
			if n := p.child("net"); n != nil && len(n.list) > 1 {
				name := n.list[len(n.list)-1].atom
				if len(n.list) == 2 { // (net "name") or (net 3)
					if v, ok := netNames[name]; ok {
						name = v
					}
				}
				fp.pads[p.list[1].atom] = name
			}
		}
		out = append(out, fp)
	}
	return out, nil
}
