package kicad

// schwrite.go — the .kicad_sch write backend (KiCad parity, user decision
// 2026-10-09). KiCad 10's schematic editor has no Python/IPC write API, so
// pcbpilot edits the S-expression file directly: place library symbol
// instances, wires, junctions, labels, power symbols, no-connects, texts and
// set fields. Edits are spliced into the original text (everything else is
// kept byte for byte) and the result re-parses; `kicad-cli sch export
// netlist` reads the change.
//
// Coordinates are KiCad sheet millimetres (y down). Library symbol
// coordinates are KiCad's (y up).

import (
	"crypto/rand"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SchFileVersion is the format version written into new sheets (KiCad 10).
const SchFileVersion = "20260306"

// Pt is a sheet point in mm.
type Pt struct{ X, Y float64 }

// NewUUID returns a random RFC 4122 v4 UUID.
func NewUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Q quotes s as a KiCad string.
func Q(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

// F formats a millimetre value (4 decimals, trailing zeros trimmed).
func F(v float64) string {
	if math.Abs(v) < 5e-5 {
		return "0"
	}
	s := strconv.FormatFloat(v, 'f', 4, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// NewSchematicText is an empty sheet.
func NewSchematicText(paper, uuid string, root bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "(kicad_sch\n\t(version %s)\n\t(generator \"pcbpilot\")\n\t(generator_version \"0.9\")\n\t(uuid %s)\n\t(paper %s)\n\t(lib_symbols)\n", SchFileVersion, Q(uuid), Q(paper))
	if root {
		b.WriteString("\t(sheet_instances\n\t\t(path \"/\"\n\t\t\t(page \"1\")\n\t\t)\n\t)\n")
	}
	b.WriteString("\t(embedded_fonts no)\n)\n")
	return b.String()
}

// SchEditor accumulates edits on one .kicad_sch text.
type SchEditor struct {
	src  string
	root *sx
	// InstancePath is the hierarchical path of this sheet ("/root-uuid" for
	// the root, "/root-uuid/sheet-symbol-uuid" for a sub-sheet); Project the
	// project name used in symbol instances.
	InstancePath, Project string
	libs                  map[string]*sx // existing lib_symbols by lib_id
	newLibs               []string
	newLibIDs             map[string]string
	items                 []string
	repl                  []textEdit
	pwrNext               int
}

type textEdit struct {
	beg, end int
	text     string
}

// OpenSchematic parses src for editing.
func OpenSchematic(src string) (*SchEditor, error) {
	root, err := parseSx(src)
	if err != nil {
		return nil, err
	}
	if root.head() != "kicad_sch" {
		return nil, fmt.Errorf("not a kicad_sch file")
	}
	e := &SchEditor{src: src, root: root, libs: map[string]*sx{}, newLibIDs: map[string]string{}, pwrNext: 1}
	if ls := root.child("lib_symbols"); ls != nil {
		for _, s := range ls.list {
			if s.head() == "symbol" && len(s.list) > 1 {
				e.libs[s.list[1].atom] = s
			}
		}
	}
	for _, s := range root.list {
		if s.head() != "symbol" {
			continue
		}
		if e.InstancePath == "" {
			if p := find(s, "instances", "project"); p != nil && len(p.list) > 1 {
				e.Project = p.list[1].atom
				if pa := p.child("path"); pa != nil && len(pa.list) > 1 {
					e.InstancePath = pa.list[1].atom
				}
			}
		}
		if r := symRef(s); strings.HasPrefix(r, "#PWR") {
			if n, err := strconv.Atoi(strings.TrimLeft(r[4:], "0")); err == nil && n >= e.pwrNext {
				e.pwrNext = n + 1
			}
		}
	}
	if e.InstancePath == "" && root.child("sheet_instances") != nil {
		e.InstancePath = "/" + e.UUID()
	}
	return e, nil
}

// OpenSchematicFile opens path and resolves its instance path and project
// from the files around it when the sheet has no symbols yet: a root sheet
// is "/<uuid>"; a sub-sheet gets "/<root uuid>/<sheet symbol uuid>" from a
// root .kicad_sch in the same directory that references it.
func OpenSchematicFile(path string) (*SchEditor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	e, err := OpenSchematic(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if e.Project == "" {
		pros, _ := filepath.Glob(filepath.Join(dir, "*.kicad_pro"))
		if len(pros) == 1 {
			e.Project = strings.TrimSuffix(filepath.Base(pros[0]), ".kicad_pro")
		} else {
			e.Project = strings.TrimSuffix(filepath.Base(path), ".kicad_sch")
		}
	}
	others, _ := filepath.Glob(filepath.Join(dir, "*.kicad_sch"))
	for _, o := range others {
		if o == path {
			continue
		}
		ob, err := os.ReadFile(o)
		if err != nil {
			continue
		}
		r, err := parseSx(string(ob))
		if err != nil {
			continue
		}
		for _, s := range r.list { // power numbering across the project
			if s.head() == "symbol" {
				if ref := symRef(s); strings.HasPrefix(ref, "#PWR") {
					if n, err := strconv.Atoi(strings.TrimLeft(ref[4:], "0")); err == nil && n >= e.pwrNext {
						e.pwrNext = n + 1
					}
				}
			}
		}
		if e.InstancePath != "" || r.child("sheet_instances") == nil {
			continue
		}
		ru := ""
		if u := r.child("uuid"); u != nil && len(u.list) > 1 {
			ru = u.list[1].atom
		}
		for _, s := range r.list {
			if s.head() == "sheet" && propVal(s, "Sheetfile") == filepath.Base(path) {
				if u := s.child("uuid"); u != nil && len(u.list) > 1 {
					e.InstancePath = "/" + ru + "/" + u.list[1].atom
				}
			}
		}
	}
	if e.InstancePath == "" {
		e.InstancePath = "/" + e.UUID()
	}
	return e, nil
}

// UUID is the sheet file's uuid.
func (e *SchEditor) UUID() string {
	if u := e.root.child("uuid"); u != nil && len(u.list) > 1 {
		return u.list[1].atom
	}
	return ""
}

func find(n *sx, path ...string) *sx {
	for _, h := range path {
		if n = n.child(h); n == nil {
			return nil
		}
	}
	return n
}

func propVal(n *sx, name string) string {
	for _, c := range n.list {
		if c.head() == "property" && len(c.list) > 2 && c.list[1].atom == name {
			return c.list[2].atom
		}
	}
	return ""
}

func symRef(s *sx) string {
	if p := find(s, "instances", "project", "path", "reference"); p != nil && len(p.list) > 1 {
		return p.list[1].atom
	}
	return propVal(s, "Reference")
}

// HasLibSymbol reports whether lib_symbols already holds id.
func (e *SchEditor) HasLibSymbol(id string) bool {
	_, ok := e.libs[id]
	_, ok2 := e.newLibIDs[id]
	return ok || ok2
}

var symNameRe = regexp.MustCompile(`^\(symbol\s+"((?:[^"\\]|\\.)*)"`)

// AddLibSymbol embeds a library symbol under lib_id id. sym is a
// `(symbol "Name" …)` as found in a .kicad_sym; the top name is replaced by
// id (unit sub-symbols keep the bare name, as KiCad writes them).
func (e *SchEditor) AddLibSymbol(id, sym string) error {
	if e.HasLibSymbol(id) {
		return nil
	}
	text, err := RenameLibSymbol(sym, id)
	if err != nil {
		return err
	}
	n, err := parseSx(text)
	if err != nil {
		return fmt.Errorf("library symbol %s: %w", id, err)
	}
	e.newLibs = append(e.newLibs, text)
	e.newLibIDs[id] = text
	e.libs[id] = n
	return nil
}

// RenameLibSymbol renames a `(symbol "Name" …)` text to id ("Lib:Name" in
// a sheet's lib_symbols, a bare name in a .kicad_sym); unit sub-symbols are
// named <bare name>_<unit>_<style>.
func RenameLibSymbol(sym, id string) (string, error) {
	sym = strings.TrimSpace(sym)
	m := symNameRe.FindStringSubmatch(sym)
	if m == nil {
		return "", fmt.Errorf("library symbol text must start with (symbol \"Name\"")
	}
	oldName, _ := strconv.Unquote(`"` + m[1] + `"`)
	bare := id
	if i := strings.LastIndex(id, ":"); i >= 0 {
		bare = id[i+1:]
	}
	body := sym[len(m[0]):]
	if oldName != bare {
		body = strings.ReplaceAll(body, "(symbol "+strings.TrimSuffix(Q(oldName+"_"), `"`), "(symbol "+strings.TrimSuffix(Q(bare+"_"), `"`))
	}
	return "(symbol " + Q(id) + body, nil
}

// LibSymbolFromFile returns the `(symbol "name" …)` text of name in a
// .kicad_sym library (symbols that `extends` another are not supported).
func LibSymbolFromFile(path, name string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	src := string(b)
	r, err := parseSx(src)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	for _, s := range r.list {
		if s.head() == "symbol" && len(s.list) > 1 && s.list[1].atom == name {
			if s.child("extends") != nil {
				return "", fmt.Errorf("%s:%s extends another symbol (not supported; flatten it in the symbol editor)", path, name)
			}
			return src[s.beg:s.end], nil
		}
	}
	return "", fmt.Errorf("symbol %q not in %s", name, path)
}

// LibPin is a library pin of one unit (unit 0 = common to all units).
type LibPin struct {
	Number, Name, Type string
	Unit               int
	At                 Pt // y up
	Angle, Length      float64
}

// LibPins lists the pins of an embedded library symbol.
func (e *SchEditor) LibPins(id string) ([]LibPin, error) {
	s, ok := e.libs[id]
	if !ok {
		return nil, fmt.Errorf("library symbol %s is not in the sheet", id)
	}
	var out []LibPin
	for _, u := range s.list {
		if u.head() != "symbol" || len(u.list) < 2 {
			continue
		}
		parts := strings.Split(u.list[1].atom, "_")
		unit := 0
		if len(parts) >= 3 {
			unit, _ = strconv.Atoi(parts[len(parts)-2])
		}
		for _, p := range u.list {
			if p.head() != "pin" {
				continue
			}
			lp := LibPin{Unit: unit}
			if len(p.list) > 1 {
				lp.Type = p.list[1].atom
			}
			if at := p.child("at"); at != nil {
				lp.At, lp.Angle = Pt{at.num(1), at.num(2)}, at.num(3)
			}
			if l := p.child("length"); l != nil {
				lp.Length = l.num(1)
			}
			if n := p.child("name"); n != nil && len(n.list) > 1 {
				lp.Name = n.list[1].atom
			}
			if n := p.child("number"); n != nil && len(n.list) > 1 {
				lp.Number = n.list[1].atom
			}
			out = append(out, lp)
		}
	}
	return out, nil
}

// SymbolXform maps a library point (y up) of a symbol placed at at with
// rotation rot (degrees, counter-clockwise on screen) and mirror ("", "x" or
// "y") to sheet mm (y down) — KiCad's transform: rotate, then mirror
// (checked against kicad-cli in TestSchEditorKicadNetlist).
func SymbolXform(p Pt, at Pt, rot float64, mirror string) Pt {
	x, y := p.X, -p.Y // to screen frame
	a := rot * math.Pi / 180
	c, s := math.Round(math.Cos(a)), math.Round(math.Sin(a))
	x, y = x*c+y*s, -x*s+y*c
	switch mirror {
	case "x":
		y = -y
	case "y":
		x = -x
	}
	return Pt{round4mm(at.X + x), round4mm(at.Y + y)}
}

func round4mm(v float64) float64 { return math.Round(v*1e4) / 1e4 }

// Field is one symbol property.
type Field struct {
	Name, Value string
	At          *Pt // nil: at the symbol origin
	Angle       float64
	Hide        bool
}

// SymbolInstance is a library symbol placement.
type SymbolInstance struct {
	LibID     string
	Ref       string
	Unit      int
	At        Pt
	Rot       float64
	Mirror    string // "", "x", "y"
	Value     string
	Footprint string
	Fields    []Field // extra fields (LCSC, MPN, …); Reference/Value/Footprint positions may be given here too
	InBOM     *bool
	UUID      string
}

// PlaceSymbol adds a symbol instance; the library symbol must be embedded
// already (AddLibSymbol). Returns the instance uuid.
func (e *SchEditor) PlaceSymbol(si SymbolInstance) (string, error) {
	lib, ok := e.libs[si.LibID]
	if !ok {
		return "", fmt.Errorf("library symbol %s is not in the sheet (embed it first)", si.LibID)
	}
	if si.Unit == 0 {
		si.Unit = 1
	}
	if si.UUID == "" {
		si.UUID = NewUUID()
	}
	power := lib.child("power") != nil
	inBOM := !power
	if si.InBOM != nil {
		inBOM = *si.InBOM
	}
	yn := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "(symbol\n\t\t(lib_id %s)\n\t\t(at %s %s %s)\n", Q(si.LibID), F(si.At.X), F(si.At.Y), F(si.Rot))
	if si.Mirror != "" {
		fmt.Fprintf(&b, "\t\t(mirror %s)\n", si.Mirror)
	}
	fmt.Fprintf(&b, "\t\t(unit %d)\n\t\t(exclude_from_sim no)\n\t\t(in_bom %s)\n\t\t(on_board yes)\n\t\t(dnp no)\n\t\t(uuid %s)\n", si.Unit, yn(inBOM), Q(si.UUID))
	given := map[string]Field{}
	for _, f := range si.Fields {
		given[f.Name] = f
	}
	std := []Field{{Name: "Reference", Value: si.Ref}, {Name: "Value", Value: si.Value},
		{Name: "Footprint", Value: si.Footprint, Hide: true}, {Name: "Datasheet", Hide: true}, {Name: "Description", Hide: true}}
	if power {
		std[0].Hide = true
	}
	var fields []Field
	for _, f := range std {
		if g, ok := given[f.Name]; ok {
			if g.Value == "" {
				g.Value = f.Value
			}
			f = g
			delete(given, f.Name)
		}
		fields = append(fields, f)
	}
	for _, f := range si.Fields {
		if _, ok := given[f.Name]; ok {
			fields = append(fields, f)
		}
	}
	for _, f := range fields {
		at, ang := si.At, f.Angle
		if f.At != nil {
			at = *f.At
		} else if lp := libProp(lib, f.Name); lp != nil {
			// the library's field position, carried through the placement
			if la := lp.child("at"); la != nil {
				at = SymbolXform(Pt{la.num(1), la.num(2)}, si.At, si.Rot, si.Mirror)
				ang = la.num(3)
			}
		}
		f.Angle = ang
		b.WriteString("\t\t" + propertyText(f.Name, f.Value, at, ang, f.Hide) + "\n")
	}
	pins, _ := e.LibPins(si.LibID)
	seen := map[string]bool{}
	for _, p := range pins {
		if (p.Unit == 0 || p.Unit == si.Unit) && !seen[p.Number] {
			seen[p.Number] = true
			fmt.Fprintf(&b, "\t\t(pin %s\n\t\t\t(uuid %s)\n\t\t)\n", Q(p.Number), Q(NewUUID()))
		}
	}
	fmt.Fprintf(&b, "\t\t(instances\n\t\t\t(project %s\n\t\t\t\t(path %s\n\t\t\t\t\t(reference %s)\n\t\t\t\t\t(unit %d)\n\t\t\t\t)\n\t\t\t)\n\t\t)\n\t)",
		Q(e.Project), Q(e.InstancePath), Q(si.Ref), si.Unit)
	e.items = append(e.items, b.String())
	return si.UUID, nil
}

func libProp(lib *sx, name string) *sx {
	for _, c := range lib.list {
		if c.head() == "property" && len(c.list) > 2 && c.list[1].atom == name {
			return c
		}
	}
	return nil
}

func propertyText(name, value string, at Pt, angle float64, hide bool) string {
	h := ""
	if hide {
		h = "\n\t\t\t(hide yes)"
	}
	return fmt.Sprintf("(property %s %s\n\t\t\t(at %s %s %s)%s\n\t\t\t(effects\n\t\t\t\t(font\n\t\t\t\t\t(size 1.27 1.27)\n\t\t\t\t)\n\t\t\t)\n\t\t)",
		Q(name), Q(value), F(at.X), F(at.Y), F(angle), h)
}

// SymbolPinPositions returns pin number → sheet point for a placed
// instance (by reference) — both existing and newly placed symbols.
func (e *SchEditor) SymbolPinPositions(ref string) (map[string]Pt, error) {
	var inst *sx
	for _, s := range e.symbols() {
		if symRef(s) == ref {
			inst = s
			break
		}
	}
	if inst == nil {
		return nil, fmt.Errorf("no symbol %s on this sheet", ref)
	}
	lid := find(inst, "lib_id")
	at := inst.child("at")
	if lid == nil || at == nil {
		return nil, fmt.Errorf("symbol %s has no lib_id/at", ref)
	}
	pins, err := e.LibPins(lid.list[1].atom)
	if err != nil {
		return nil, err
	}
	unit := 1
	if u := inst.child("unit"); u != nil {
		unit = int(u.num(1))
	}
	mir := ""
	if m := inst.child("mirror"); m != nil && len(m.list) > 1 {
		mir = m.list[1].atom
	}
	out := map[string]Pt{}
	for _, p := range pins {
		if p.Unit == 0 || p.Unit == unit {
			out[p.Number] = SymbolXform(p.At, Pt{at.num(1), at.num(2)}, at.num(3), mir)
		}
	}
	return out, nil
}

// SymbolAt returns the position and rotation of the symbol ref.
func (e *SchEditor) SymbolAt(ref string) (x, y, rot float64, err error) {
	for _, s := range e.symbols() {
		if symRef(s) == ref {
			if at := s.child("at"); at != nil {
				return at.num(1), at.num(2), at.num(3), nil
			}
		}
	}
	return 0, 0, 0, fmt.Errorf("no symbol %s on this sheet", ref)
}

// symbols returns the parsed symbol instances, pending ones included.
func (e *SchEditor) symbols() []*sx {
	var out []*sx
	for _, s := range e.root.list {
		if s.head() == "symbol" {
			out = append(out, s)
		}
	}
	for _, it := range e.items {
		if strings.HasPrefix(it, "(symbol") {
			if n, err := parseSx(it); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

// AddWire adds one wire per consecutive point pair.
func (e *SchEditor) AddWire(pts ...Pt) {
	for i := 0; i+1 < len(pts); i++ {
		a, b := pts[i], pts[i+1]
		if a == b {
			continue
		}
		e.items = append(e.items, fmt.Sprintf("(wire\n\t\t(pts\n\t\t\t(xy %s %s) (xy %s %s)\n\t\t)\n\t\t(stroke\n\t\t\t(width 0)\n\t\t\t(type default)\n\t\t)\n\t\t(uuid %s)\n\t)",
			F(a.X), F(a.Y), F(b.X), F(b.Y), Q(NewUUID())))
	}
}

// AddJunction adds a junction dot.
func (e *SchEditor) AddJunction(p Pt) {
	e.items = append(e.items, fmt.Sprintf("(junction\n\t\t(at %s %s)\n\t\t(diameter 0)\n\t\t(color 0 0 0 0)\n\t\t(uuid %s)\n\t)", F(p.X), F(p.Y), Q(NewUUID())))
}

// AddNoConnect adds a no-connect flag.
func (e *SchEditor) AddNoConnect(p Pt) {
	e.items = append(e.items, fmt.Sprintf("(no_connect\n\t\t(at %s %s)\n\t\t(uuid %s)\n\t)", F(p.X), F(p.Y), Q(NewUUID())))
}

// Label kinds.
const (
	LabelLocal  = "label"
	LabelGlobal = "global_label"
	LabelHier   = "hierarchical_label"
)

// AddLabel adds a net label. shape (global/hierarchical only): input,
// output, bidirectional, tri_state, passive.
func (e *SchEditor) AddLabel(kind, name string, at Pt, angle float64, shape string) error {
	switch kind {
	case LabelLocal, LabelGlobal, LabelHier:
	default:
		return fmt.Errorf("unknown label kind %q", kind)
	}
	if name == "" {
		return fmt.Errorf("label name is empty")
	}
	just := "left bottom"
	if angle == 180 || angle == 270 {
		just = "right bottom"
	}
	sh := ""
	if kind != LabelLocal {
		if shape == "" {
			shape = "passive"
		}
		sh = "\n\t\t(shape " + shape + ")"
		just = "left"
		if angle == 180 || angle == 270 {
			just = "right"
		}
	}
	e.items = append(e.items, fmt.Sprintf("(%s %s%s\n\t\t(at %s %s %s)\n\t\t(effects\n\t\t\t(font\n\t\t\t\t(size 1.27 1.27)\n\t\t\t)\n\t\t\t(justify %s)\n\t\t)\n\t\t(uuid %s)\n\t)",
		kind, Q(name), sh, F(at.X), F(at.Y), F(angle), just, Q(NewUUID())))
	return nil
}

// AddText adds a free text.
func (e *SchEditor) AddText(text string, at Pt, angle, size float64) {
	if size <= 0 {
		size = 1.27
	}
	e.items = append(e.items, fmt.Sprintf("(text %s\n\t\t(exclude_from_sim no)\n\t\t(at %s %s %s)\n\t\t(effects\n\t\t\t(font\n\t\t\t\t(size %s %s)\n\t\t\t)\n\t\t\t(justify left bottom)\n\t\t)\n\t\t(uuid %s)\n\t)",
		Q(text), F(at.X), F(at.Y), F(angle), F(size), F(size), Q(NewUUID())))
}

// AddRect adds a graphic rectangle (dashed outline).
func (e *SchEditor) AddRect(a, b Pt) {
	e.items = append(e.items, fmt.Sprintf("(rectangle\n\t\t(start %s %s)\n\t\t(end %s %s)\n\t\t(stroke\n\t\t\t(width 0)\n\t\t\t(type dash)\n\t\t)\n\t\t(fill\n\t\t\t(type none)\n\t\t)\n\t\t(uuid %s)\n\t)",
		F(a.X), F(a.Y), F(b.X), F(b.Y), Q(NewUUID())))
}

// AddRaw adds a pre-formatted top-level item (e.g. a hierarchical sheet).
func (e *SchEditor) AddRaw(item string) { e.items = append(e.items, item) }

// PowerLibID is the lib_id pcbpilot uses for a power net symbol.
func PowerLibID(net string) string { return "pcbpilot_power:" + net }

// PowerSymbolText is a global power symbol for net: ground style (bars
// below the pin) when ground, otherwise a bar above the pin.
func PowerSymbolText(net string, ground bool) string {
	gfx := "(polyline (pts (xy 0 0) (xy 0 1.27) (xy -0.762 1.27) (xy 0.762 1.27)) (stroke (width 0) (type default)) (fill (type none)))"
	pinAng, valY := "90", "3.556"
	if ground {
		gfx = "(polyline (pts (xy 0 0) (xy 0 -1.27) (xy 1.27 -1.27) (xy 0 -2.54) (xy -1.27 -1.27) (xy 0 -1.27)) (stroke (width 0) (type default)) (fill (type none)))"
		pinAng, valY = "270", "-3.81"
	}
	n := "PWR"
	return fmt.Sprintf(`(symbol %s
		(power global)
		(pin_numbers (hide yes))
		(pin_names (offset 0) (hide yes))
		(exclude_from_sim no)
		(in_bom no)
		(on_board yes)
		(property "Reference" "#PWR" (at 0 -6.35 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Value" %s (at 0 %s 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Datasheet" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Description" %s (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(symbol "%s_0_1" %s)
		(symbol "%s_1_1" (pin power_in line (at 0 0 %s) (length 0) (hide yes) (name %s (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))
	)`, Q(n), Q(net), valY, Q("Power symbol creates a global label with name \""+net+"\""), n, gfx, n, pinAng, Q(net))
}

// IsGroundNet guesses the ground style from the net name.
func IsGroundNet(net string) bool {
	u := strings.ToUpper(net)
	return strings.Contains(u, "GND") || strings.HasPrefix(u, "VSS") || u == "0V" || strings.Contains(u, "EARTH")
}

// AddPower places a power symbol for net at p (its pin) and returns the
// reference it got.
func (e *SchEditor) AddPower(net string, p Pt, rot float64, ground bool) (string, error) {
	id := PowerLibID(net)
	if err := e.AddLibSymbol(id, PowerSymbolText(net, ground)); err != nil {
		return "", err
	}
	ref := fmt.Sprintf("#PWR%04d", e.pwrNext)
	e.pwrNext++
	vy := 3.556
	if ground {
		vy = -3.81
	}
	va := SymbolXform(Pt{0, vy}, p, rot, "")
	_, err := e.PlaceSymbol(SymbolInstance{LibID: id, Ref: ref, At: p, Rot: rot, Value: net,
		Fields: []Field{{Name: "Value", Value: net, At: &va}}})
	return ref, err
}

// SetField sets (or adds) field name on the symbol with reference ref.
// Setting "Reference" renames the symbol (its instance reference too).
func (e *SchEditor) SetField(ref, name, value string) error {
	var inst *sx
	for _, s := range e.root.list {
		if s.head() == "symbol" && symRef(s) == ref {
			inst = s
			break
		}
	}
	if inst == nil {
		for i, it := range e.items { // pending symbol: edit its text
			if strings.HasPrefix(it, "(symbol") {
				if n, err := parseSx(it); err == nil && symRef(n) == ref {
					sub := &SchEditor{src: it, root: &sx{list: []*sx{n}}}
					if err := sub.setFieldOn(n, name, value); err != nil {
						return err
					}
					e.items[i] = sub.apply()
					return nil
				}
			}
		}
		return fmt.Errorf("no symbol %s on this sheet", ref)
	}
	return e.setFieldOn(inst, name, value)
}

func (e *SchEditor) setFieldOn(inst *sx, name, value string) error {
	for _, c := range inst.list {
		if c.head() == "property" && len(c.list) > 2 && c.list[1].atom == name {
			v := c.list[2]
			// the value atom has no offsets: re-emit the property head.
			head := e.src[c.beg:c.end]
			re := regexp.MustCompile(`^\(property\s+"(?:[^"\\]|\\.)*"\s+"(?:[^"\\]|\\.)*"`)
			loc := re.FindStringIndex(head)
			if loc == nil {
				return fmt.Errorf("cannot rewrite property %s", name)
			}
			_ = v
			e.repl = append(e.repl, textEdit{c.beg, c.beg + loc[1], "(property " + Q(name) + " " + Q(value)})
			if name == "Reference" {
				if r := find(inst, "instances", "project", "path", "reference"); r != nil {
					e.repl = append(e.repl, textEdit{r.beg, r.end, "(reference " + Q(value) + ")"})
				}
			}
			return nil
		}
	}
	at := inst.child("at")
	p := Pt{}
	if at != nil {
		p = Pt{at.num(1), at.num(2)}
	}
	// insert the new property after the last existing one
	pos := -1
	for _, c := range inst.list {
		if c.head() == "property" {
			pos = c.end
		}
	}
	if pos < 0 {
		return fmt.Errorf("symbol has no properties")
	}
	e.repl = append(e.repl, textEdit{pos, pos, "\n\t\t" + propertyText(name, value, p, 0, true)})
	return nil
}

// MoveSymbol moves the symbol ref to at with rotation rot; its fields move
// by the same offset.
func (e *SchEditor) MoveSymbol(ref string, to Pt, rot float64) error {
	for _, s := range e.root.list {
		if s.head() != "symbol" || symRef(s) != ref {
			continue
		}
		at := s.child("at")
		if at == nil {
			return fmt.Errorf("symbol %s has no position", ref)
		}
		dx, dy := to.X-at.num(1), to.Y-at.num(2)
		e.repl = append(e.repl, textEdit{at.beg, at.end, fmt.Sprintf("(at %s %s %s)", F(to.X), F(to.Y), F(rot))})
		for _, c := range s.list {
			if c.head() == "property" {
				if pa := c.child("at"); pa != nil {
					e.repl = append(e.repl, textEdit{pa.beg, pa.end, fmt.Sprintf("(at %s %s %s)", F(pa.num(1)+dx), F(pa.num(2)+dy), F(pa.num(3)))})
				}
			}
		}
		return nil
	}
	return fmt.Errorf("no symbol %s on this sheet (pending symbols cannot be moved)", ref)
}

func (e *SchEditor) apply() string {
	edits := append([]textEdit(nil), e.repl...)
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].beg > edits[j].beg })
	s := e.src
	for _, ed := range edits {
		s = s[:ed.beg] + ed.text + s[ed.end:]
	}
	return s
}

// Render returns the edited text; it re-parses before returning.
func (e *SchEditor) Render() (string, error) {
	edits := append([]textEdit(nil), e.repl...)
	if len(e.newLibs) > 0 {
		ls := e.root.child("lib_symbols")
		if ls == nil {
			return "", fmt.Errorf("sheet has no lib_symbols")
		}
		edits = append(edits, textEdit{ls.end - 1, ls.end - 1, "\n\t\t" + strings.Join(e.newLibs, "\n\t\t") + "\n\t"})
	}
	if len(e.items) > 0 {
		pos := e.root.end - 1
		for _, c := range e.root.list {
			if h := c.head(); h == "sheet_instances" || h == "symbol_instances" || h == "embedded_fonts" {
				pos = c.beg
				break
			}
		}
		edits = append(edits, textEdit{pos, pos, strings.Join(e.items, "\n\t") + "\n\t"})
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].beg > edits[j].beg })
	s := e.src
	for _, ed := range edits {
		s = s[:ed.beg] + ed.text + s[ed.end:]
	}
	if _, err := parseSx(s); err != nil {
		return "", fmt.Errorf("edited sheet does not re-parse: %w", err)
	}
	return s, nil
}
