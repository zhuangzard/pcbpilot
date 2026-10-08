// Package kicad — fab.go: KiCad → JLCPCB manufacturing output.
//
// `pcbpilot kicad fab` turns a .kicad_pcb (plus, optionally, its root
// .kicad_sch) into the file set JLCPCB asks for in its KiCad help articles
// ("How to generate Gerber and Drill files in KiCad 9", "How To Export BOM and
// CPL Files From KiCad 10", jlcpcb.com/help, both last updated 2026-09):
//
//   - Gerber RS-274X, Protel filename extensions, 4.6 mm coordinates, X2 +
//     netlist attributes, soldermask subtracted from silkscreen, zones refilled;
//     layers F.Cu, every inner copper layer in stackup order, B.Cu, F/B.Mask,
//     F/B.Silkscreen, F/B.Paste, Edge.Cuts;
//   - Excellon drill, millimetres, decimal zeros, absolute origin, alternate
//     drill mode for oval holes, PTH and NPTH in separate files;
//   - one zip holding the Gerbers + drill files;
//   - CPL CSV: Designator, Mid X, Mid Y, Layer, Rotation (mm);
//   - BOM CSV: Comment, Designator, Footprint, LCSC Part #.
//
// The Gerber/drill/position geometry always comes from kicad-cli, never from a
// re-implementation here; this file only picks the arguments, reads the board's
// layer table and footprint attributes, and reshapes kicad-cli's CSV into JLC's
// column names.
package kicad

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ── tool discovery ─────────────────────────────────────────────────────────
// Reconciled at merge: internal/kicad/kicad.go (other branch) owns the general
// KiCad bridge; these fab-local resolvers keep this branch self-contained.

const (
	envFabCLI    = "PCBPILOT_KICAD_CLI"
	envFabPython = "PCBPILOT_KICAD_PYTHON"
)

var fabCLICandidates = []string{
	"/Applications/KiCad/KiCad.app/Contents/MacOS/kicad-cli",
	"/usr/bin/kicad-cli",
	"/usr/local/bin/kicad-cli",
	`C:\Program Files\KiCad\10.0\bin\kicad-cli.exe`,
}

var fabPythonCandidates = []string{
	// Globbed: the framework version (3.9 in KiCad 10.0.6) changes with upgrades.
	"/Applications/KiCad/KiCad.app/Contents/Frameworks/Python.framework/Versions/*/bin/python3",
	`C:\Program Files\KiCad\10.0\bin\python.exe`,
	"/usr/bin/python3", // Linux distro KiCad installs pcbnew into the system python
}

// FabTools are the KiCad executables fab/lcsc shell out to.
type FabTools struct {
	CLI    string // kicad-cli
	Python string // a python3 that can `import pcbnew` (only needed for --set)
}

func firstExisting(env string, cands []string, lookName string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		if _, err := os.Stat(v); err != nil {
			return "", fmt.Errorf("%s=%s: %w", env, v, err)
		}
		return v, nil
	}
	for _, c := range cands {
		matches := []string{c}
		if strings.Contains(c, "*") {
			matches, _ = filepath.Glob(c)
			// Prefer "Current", then the highest version.
			sort.Slice(matches, func(a, b int) bool {
				ca, cb := strings.Contains(matches[a], "/Current/"), strings.Contains(matches[b], "/Current/")
				if ca != cb {
					return ca
				}
				return matches[a] > matches[b]
			})
		}
		for _, m := range matches {
			if st, err := os.Stat(m); err == nil && !st.IsDir() {
				return m, nil
			}
		}
	}
	if lookName != "" {
		if p, err := exec.LookPath(lookName); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("not found (tried %s, %s); set %s", env, strings.Join(cands, ", "), env)
}

// ResolveFabCLI locates kicad-cli ($PCBPILOT_KICAD_CLI, standard installs, $PATH).
func ResolveFabCLI() (string, error) {
	p, err := firstExisting(envFabCLI, fabCLICandidates, "kicad-cli")
	if err != nil {
		return "", fmt.Errorf("kicad-cli %w", err)
	}
	return p, nil
}

// ResolveFabPython locates KiCad's bundled python ($PCBPILOT_KICAD_PYTHON first).
func ResolveFabPython() (string, error) {
	p, err := firstExisting(envFabPython, fabPythonCandidates, "")
	if err != nil {
		return "", fmt.Errorf("KiCad python (with pcbnew) %w", err)
	}
	return p, nil
}

// ── minimal S-expression reader ────────────────────────────────────────────

// sx is one S-expression node: an atom (Atom set, Kids nil) or a list.
type sx struct {
	Atom   string
	Quoted bool
	Kids   []*sx
	isList bool
}

func (n *sx) head() string {
	if n == nil || !n.isList || len(n.Kids) == 0 {
		return ""
	}
	return n.Kids[0].Atom
}

// arg returns the i-th atom after the head ("" when absent or a list).
func (n *sx) arg(i int) string {
	if n == nil || i+1 >= len(n.Kids) || n.Kids[i+1].isList {
		return ""
	}
	return n.Kids[i+1].Atom
}

func (n *sx) child(name string) *sx {
	for _, k := range n.Kids {
		if k.head() == name {
			return k
		}
	}
	return nil
}

func (n *sx) children(name string) []*sx {
	var out []*sx
	for _, k := range n.Kids {
		if k.head() == name {
			out = append(out, k)
		}
	}
	return out
}

func parseSexpr(b []byte) (*sx, error) {
	pos := 0
	var parse func() (*sx, error)
	skip := func() {
		for pos < len(b) && (b[pos] == ' ' || b[pos] == '\t' || b[pos] == '\n' || b[pos] == '\r') {
			pos++
		}
	}
	parse = func() (*sx, error) {
		skip()
		if pos >= len(b) {
			return nil, io.ErrUnexpectedEOF
		}
		switch b[pos] {
		case '(':
			pos++
			n := &sx{isList: true}
			for {
				skip()
				if pos >= len(b) {
					return nil, io.ErrUnexpectedEOF
				}
				if b[pos] == ')' {
					pos++
					return n, nil
				}
				k, err := parse()
				if err != nil {
					return nil, err
				}
				n.Kids = append(n.Kids, k)
			}
		case ')':
			return nil, fmt.Errorf("unexpected ')' at byte %d", pos)
		case '"':
			pos++
			var sb strings.Builder
			for pos < len(b) && b[pos] != '"' {
				if b[pos] == '\\' && pos+1 < len(b) {
					pos++
					switch b[pos] {
					case 'n':
						sb.WriteByte('\n')
					case 't':
						sb.WriteByte('\t')
					default:
						sb.WriteByte(b[pos])
					}
				} else {
					sb.WriteByte(b[pos])
				}
				pos++
			}
			if pos >= len(b) {
				return nil, io.ErrUnexpectedEOF
			}
			pos++
			return &sx{Atom: sb.String(), Quoted: true}, nil
		default:
			start := pos
			for pos < len(b) && !strings.ContainsRune(" \t\r\n()\"", rune(b[pos])) {
				pos++
			}
			return &sx{Atom: string(b[start:pos])}, nil
		}
	}
	root, err := parse()
	if err != nil {
		return nil, fmt.Errorf("s-expression: %w", err)
	}
	return root, nil
}

// ── board model ────────────────────────────────────────────────────────────

// BoardLayer is one row of the .kicad_pcb (layers …) table.
type BoardLayer struct {
	ID        int
	Name      string // canonical (untranslated) name, e.g. "F.Cu", "F.SilkS"
	Type      string // signal | power | mixed | jumper | user | …
	UserName  string // optional display name, e.g. "F.Silkscreen"
	IsCopper  bool
	CopperIdx int // stackup order among copper layers (F.Cu = 0)
}

// Footprint is the part of a placed footprint fab needs.
type Footprint struct {
	Ref        string            `json:"ref"`
	Value      string            `json:"value"`
	LibID      string            `json:"footprint"` // "Lib:Name"
	Layer      string            `json:"layer"`     // F.Cu | B.Cu
	X, Y, Rot  float64           `json:"-"`
	Fields     map[string]string `json:"fields,omitempty"`
	DNP        bool              `json:"dnp,omitempty"`
	ExcludeBOM bool              `json:"excludeFromBom,omitempty"`
	ExcludePos bool              `json:"excludeFromPos,omitempty"`
}

// Board is the parsed subset of a .kicad_pcb.
type Board struct {
	Layers     []BoardLayer
	Footprints []Footprint
}

// ReadBoard parses a .kicad_pcb file.
func ReadBoard(path string) (*Board, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseBoard(b)
}

// ParseBoard parses .kicad_pcb text.
func ParseBoard(b []byte) (*Board, error) {
	root, err := parseSexpr(b)
	if err != nil {
		return nil, err
	}
	if root.head() != "kicad_pcb" {
		return nil, fmt.Errorf("not a kicad_pcb file (root %q)", root.head())
	}
	bd := &Board{}
	if lt := root.child("layers"); lt != nil {
		for _, l := range lt.Kids[1:] {
			if !l.isList || len(l.Kids) < 3 {
				continue
			}
			id, _ := strconv.Atoi(l.Kids[0].Atom)
			bl := BoardLayer{ID: id, Name: l.Kids[1].Atom, Type: l.Kids[2].Atom}
			if len(l.Kids) > 3 {
				bl.UserName = l.Kids[3].Atom
			}
			bl.IsCopper = strings.HasSuffix(bl.Name, ".Cu")
			bd.Layers = append(bd.Layers, bl)
		}
	}
	// Copper stackup order: F.Cu, In1..InN (by number), B.Cu. The numeric ids
	// changed between KiCad versions (B.Cu was 31, is 2 since KiCad 9), so the
	// order is derived from the names, not the ids.
	cuRank := func(name string) int {
		switch {
		case name == "F.Cu":
			return 0
		case name == "B.Cu":
			return 1 << 20
		case strings.HasPrefix(name, "In"):
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "In"), ".Cu"))
			return n
		}
		return 1 << 19
	}
	var cu []int
	for i := range bd.Layers {
		if bd.Layers[i].IsCopper {
			cu = append(cu, i)
		}
	}
	sort.SliceStable(cu, func(a, b int) bool { return cuRank(bd.Layers[cu[a]].Name) < cuRank(bd.Layers[cu[b]].Name) })
	for k, i := range cu {
		bd.Layers[i].CopperIdx = k
	}

	for _, fp := range root.children("footprint") {
		f := Footprint{LibID: fp.arg(0), Fields: map[string]string{}}
		if l := fp.child("layer"); l != nil {
			f.Layer = l.arg(0)
		}
		if at := fp.child("at"); at != nil {
			f.X, _ = strconv.ParseFloat(at.arg(0), 64)
			f.Y, _ = strconv.ParseFloat(at.arg(1), 64)
			f.Rot, _ = strconv.ParseFloat(at.arg(2), 64)
		}
		for _, p := range fp.children("property") {
			f.Fields[p.arg(0)] = p.arg(1)
		}
		// KiCad 6 stored ref/value as (fp_text reference "R1" …).
		for _, t := range fp.children("fp_text") {
			switch t.arg(0) {
			case "reference":
				if _, ok := f.Fields["Reference"]; !ok {
					f.Fields["Reference"] = t.arg(1)
				}
			case "value":
				if _, ok := f.Fields["Value"]; !ok {
					f.Fields["Value"] = t.arg(1)
				}
			}
		}
		f.Ref, f.Value = f.Fields["Reference"], f.Fields["Value"]
		if a := fp.child("attr"); a != nil {
			for _, k := range a.Kids[1:] {
				switch k.Atom {
				case "dnp":
					f.DNP = true
				case "exclude_from_bom":
					f.ExcludeBOM = true
				case "exclude_from_pos_files":
					f.ExcludePos = true
				}
			}
		}
		// KiCad 8+ also writes flags as their own nodes: (dnp yes).
		for name, dst := range map[string]*bool{"dnp": &f.DNP, "exclude_from_bom": &f.ExcludeBOM, "exclude_from_pos_files": &f.ExcludePos} {
			if n := fp.child(name); n != nil && n.arg(0) != "no" {
				*dst = true
			}
		}
		bd.Footprints = append(bd.Footprints, f)
	}
	return bd, nil
}

// JLCGerberLayers lists the layers to plot for JLCPCB in their help-article
// order: copper in stackup order, then mask, silkscreen, paste, Edge.Cuts.
// Canonical (untranslated) names are returned because kicad-cli --layers wants
// those. A technical layer missing from the board's table is skipped.
func JLCGerberLayers(bd *Board) []string {
	var cu []BoardLayer
	have := map[string]bool{}
	for _, l := range bd.Layers {
		have[l.Name] = true
		if l.IsCopper {
			cu = append(cu, l)
		}
	}
	sort.SliceStable(cu, func(a, b int) bool { return cu[a].CopperIdx < cu[b].CopperIdx })
	var out []string
	for _, l := range cu {
		out = append(out, l.Name)
	}
	for _, alts := range [][]string{
		{"F.Mask"}, {"B.Mask"},
		{"F.SilkS", "F.Silkscreen"}, {"B.SilkS", "B.Silkscreen"},
		{"F.Paste"}, {"B.Paste"},
		{"Edge.Cuts"},
	} {
		for _, n := range alts {
			if have[n] {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

// ── kicad-cli argument builders (exact JLC settings, unit-tested) ──────────

// GerberArgs: Protel extensions, X2 and netlist attributes and precision 6
// are kicad-cli's defaults, i.e. what JLC asks for, so only the deviations
// from default are passed.
func GerberArgs(pcb, outDir string, layers []string) []string {
	return []string{"pcb", "export", "gerbers",
		"--output", outDir + string(filepath.Separator),
		"--layers", strings.Join(layers, ","),
		"--subtract-soldermask",
		"--check-zones",
		"--precision", "6",
		pcb}
}

// DrillArgs: Excellon, mm, decimal, absolute, alternate oval mode, PTH and
// NPTH separate, plus a Gerber X2 drill map for human review (kept out of the
// zip — JLC's CAM does not need it).
func DrillArgs(pcb, outDir string) []string {
	return []string{"pcb", "export", "drill",
		"--output", outDir + string(filepath.Separator),
		"--format", "excellon",
		"--drill-origin", "absolute",
		"--excellon-zeros-format", "decimal",
		"--excellon-oval-format", "alternate",
		"--excellon-units", "mm",
		"--excellon-separate-th",
		"--generate-map", "--map-format", "gerberx2",
		pcb}
}

// PosArgs: CSV, mm, both sides, DNP excluded (exclude_from_pos footprints are
// always left out by KiCad itself).
func PosArgs(pcb, outFile string) []string {
	return []string{"pcb", "export", "pos",
		"--output", outFile,
		"--format", "csv", "--units", "mm", "--side", "both",
		"--exclude-dnp",
		pcb}
}

// ── CPL ────────────────────────────────────────────────────────────────────

// RotationOverride corrects one part's CPL entry. JLC's placement library
// defines its own zero orientation per package, which can differ from the
// KiCad footprint library's; the needed correction is only known after
// checking JLC's assembly preview, so pcbpilot ships no built-in table.
type RotationOverride struct {
	Rotate float64 `json:"rotate"` // degrees added to KiCad's rotation
	DX     float64 `json:"dx"`     // mm added to Mid X
	DY     float64 `json:"dy"`     // mm added to Mid Y
}

// CPLOverrides is the --cpl-overrides JSON file: per designator, per
// footprint (full "Lib:Name" or bare name), designator wins.
type CPLOverrides struct {
	Refs       map[string]RotationOverride `json:"refs"`
	Footprints map[string]RotationOverride `json:"footprints"`
}

// LoadCPLOverrides reads an override file ("" → no overrides).
func LoadCPLOverrides(path string) (*CPLOverrides, error) {
	if path == "" {
		return &CPLOverrides{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var o CPLOverrides
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &o, nil
}

func (o *CPLOverrides) lookup(ref, libID string) (RotationOverride, bool) {
	if o == nil {
		return RotationOverride{}, false
	}
	if v, ok := o.Refs[ref]; ok {
		return v, true
	}
	if v, ok := o.Footprints[libID]; ok {
		return v, true
	}
	if i := strings.LastIndexByte(libID, ':'); i >= 0 {
		if v, ok := o.Footprints[libID[i+1:]]; ok {
			return v, true
		}
	}
	return RotationOverride{}, false
}

func normDeg(d float64) float64 {
	d = math.Mod(d, 360)
	if d < 0 {
		d += 360
	}
	if math.Abs(d-360) < 1e-9 {
		d = 0
	}
	return d
}

func fmtNum(v float64) string {
	s := strconv.FormatFloat(v, 'f', 4, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" || s == "" {
		s = "0"
	}
	return s
}

// CPLHeader is JLC's CPL column set.
var CPLHeader = []string{"Designator", "Mid X", "Mid Y", "Layer", "Rotation"}

// ConvertPosToCPL reshapes kicad-cli's pos CSV (Ref,Val,Package,PosX,PosY,Rot,
// Side; mm) into JLC CPL rows. Coordinates and rotation are KiCad's, unchanged
// unless an override matches. fpLib maps ref → footprint "Lib:Name" for
// footprint-keyed overrides. applied lists the overrides used.
func ConvertPosToCPL(pos io.Reader, ov *CPLOverrides, fpLib map[string]string) (rows [][]string, applied []string, err error) {
	r := csv.NewReader(pos)
	recs, err := r.ReadAll()
	if err != nil {
		return nil, nil, fmt.Errorf("pos csv: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil, errors.New("pos csv: empty")
	}
	col := map[string]int{}
	for i, h := range recs[0] {
		col[strings.TrimSpace(h)] = i
	}
	for _, need := range []string{"Ref", "PosX", "PosY", "Rot", "Side"} {
		if _, ok := col[need]; !ok {
			return nil, nil, fmt.Errorf("pos csv: missing column %q (got %v)", need, recs[0])
		}
	}
	rows = append(rows, CPLHeader)
	for _, rec := range recs[1:] {
		ref := rec[col["Ref"]]
		x, e1 := strconv.ParseFloat(rec[col["PosX"]], 64)
		y, e2 := strconv.ParseFloat(rec[col["PosY"]], 64)
		rot, e3 := strconv.ParseFloat(rec[col["Rot"]], 64)
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, nil, fmt.Errorf("pos csv: bad number in row %v", rec)
		}
		layer := "Top"
		if strings.EqualFold(rec[col["Side"]], "bottom") {
			layer = "Bottom"
		}
		if o, ok := ov.lookup(ref, fpLib[ref]); ok {
			x += o.DX
			y += o.DY
			rot += o.Rotate
			applied = append(applied, fmt.Sprintf("%s rotate%+g dx%+g dy%+g", ref, o.Rotate, o.DX, o.DY))
		}
		rows = append(rows, []string{ref, fmtNum(x) + "mm", fmtNum(y) + "mm", layer, fmtNum(normDeg(rot))})
	}
	return rows, applied, nil
}

// ── BOM ────────────────────────────────────────────────────────────────────

// BOMHeader is JLC's BOM column set (JLC's KiCad 10 guide / sample BOM).
var BOMHeader = []string{"Comment", "Designator", "Footprint", "LCSC Part #"}

// BuildBOM groups assembled parts by (value, footprint, LCSC) into JLC rows.
func BuildBOM(parts []Part) [][]string {
	type key struct{ v, f, c string }
	groups := map[key][]string{}
	var order []key
	for _, p := range parts {
		if !p.Assembled() {
			continue
		}
		k := key{p.Value, footprintName(p.Footprint), p.LCSC}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], p.Ref)
	}
	for _, k := range order {
		sortRefs(groups[k])
	}
	sort.SliceStable(order, func(a, b int) bool { return refLess(groups[order[a]][0], groups[order[b]][0]) })
	rows := [][]string{BOMHeader}
	for _, k := range order {
		rows = append(rows, []string{k.v, strings.Join(groups[k], ","), k.f, k.c})
	}
	return rows
}

// footprintName strips the library nickname ("Resistor_SMD:R_0402" → "R_0402").
func footprintName(libID string) string {
	if i := strings.LastIndexByte(libID, ':'); i >= 0 {
		return libID[i+1:]
	}
	return libID
}

// refLess orders designators naturally: prefix, then number (R2 < R10).
func refLess(a, b string) bool {
	pa, na := splitRef(a)
	pb, nb := splitRef(b)
	if pa != pb {
		return pa < pb
	}
	if na != nb {
		return na < nb
	}
	return a < b
}

func splitRef(r string) (string, int) {
	i := len(r)
	for i > 0 && r[i-1] >= '0' && r[i-1] <= '9' {
		i--
	}
	n, _ := strconv.Atoi(r[i:])
	return r[:i], n
}

func sortRefs(refs []string) {
	sort.SliceStable(refs, func(a, b int) bool { return refLess(refs[a], refs[b]) })
}

// WriteCSV writes rows as RFC-4180 CSV (every field quoted is not needed; JLC
// accepts standard CSV).
func WriteCSV(path string, rows [][]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	if err := w.WriteAll(rows); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ZipFiles writes files (flat, base names) into zipPath.
func ZipFiles(zipPath string, files []string) error {
	out, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	for _, p := range files {
		w, err := zw.Create(filepath.Base(p))
		if err != nil {
			out.Close()
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			out.Close()
			return err
		}
		if _, err := w.Write(b); err != nil {
			out.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// isFabFile reports whether a kicad-cli output belongs in the JLC zip:
// Gerbers (Protel extensions .gtl/.g1/…/.gm1, or .gbr) and Excellon .drl —
// but not the drill map or the gerber job file.
func isFabFile(name string) bool {
	low := strings.ToLower(name)
	if strings.Contains(low, "-drl_map") || strings.HasSuffix(low, ".gbrjob") {
		return false
	}
	ext := filepath.Ext(low)
	if ext == ".drl" || ext == ".gbr" {
		return true
	}
	// Protel: .gtl .gbl .gts .gbs .gto .gbo .gtp .gbp .gm1 .g1 .. .g30
	if len(ext) >= 3 && ext[1] == 'g' {
		return true
	}
	return false
}

// ── orchestration ──────────────────────────────────────────────────────────

// FabOptions drives RunFab.
type FabOptions struct {
	PCB, Sch, OutDir string
	CPLOverrides     string
	NoAssembly       bool // bare board: skip BOM/CPL and the LCSC gate
	Tools            FabTools
	Log              io.Writer
}

// FabReport is written to OUT/fab-report.json and returned.
type FabReport struct {
	PCB          string        `json:"pcb"`
	Sch          string        `json:"sch,omitempty"`
	KiCadCLI     string        `json:"kicadCli"`
	Layers       []string      `json:"layers"`
	Zip          string        `json:"zip,omitempty"`
	ZipFiles     []string      `json:"zipFiles,omitempty"`
	BOM          string        `json:"bom,omitempty"`
	CPL          string        `json:"cpl,omitempty"`
	BOMLines     int           `json:"bomLines,omitempty"`
	CPLParts     int           `json:"cplParts,omitempty"`
	Overrides    []string      `json:"cplOverridesApplied,omitempty"`
	Missing      []MissingLCSC `json:"missingLcsc,omitempty"`
	Invalid      []MissingLCSC `json:"invalidLcsc,omitempty"`
	Warnings     []string      `json:"warnings,omitempty"`
	RotationNote string        `json:"rotationNote,omitempty"`
}

// ErrMissingLCSC is returned (wrapped) when assembled parts lack LCSC numbers.
var ErrMissingLCSC = errors.New("parts without a valid LCSC part number")

// RotationNote is the CPL caveat repeated in the report and the CLI help.
const RotationNote = "CPL rotation/position are KiCad's footprint values unchanged. JLC's assembly " +
	"library has its own zero orientation per package, so some parts (often SOT-23, " +
	"polarised parts, connectors, bottom-side parts) show rotated in JLC's placement " +
	"preview. Check every part there; correct with --cpl-overrides (refs/footprints → " +
	"rotate/dx/dy) — pcbpilot ships no per-part offset table."

func runCLI(cli string, args []string, log io.Writer) error {
	cmd := exec.Command(cli, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if log != nil && buf.Len() > 0 {
		_, _ = log.Write(buf.Bytes())
	}
	if err != nil {
		return fmt.Errorf("kicad-cli %s %s: %w\n%s", args[0], strings.Join(args[1:3], " "), err, buf.String())
	}
	return nil
}

// RunFab generates the JLC file set. On a missing/invalid LCSC number it
// still writes nothing for assembly and returns an error wrapping
// ErrMissingLCSC with the report filled in (Gerbers are produced first only
// when the LCSC gate passes, so a failed run never looks like a finished one).
func RunFab(o FabOptions) (*FabReport, error) {
	if o.Log == nil {
		o.Log = io.Discard
	}
	rep := &FabReport{PCB: o.PCB, Sch: o.Sch, KiCadCLI: o.Tools.CLI, RotationNote: RotationNote}
	bd, err := ReadBoard(o.PCB)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", o.PCB, err)
	}
	rep.Layers = JLCGerberLayers(bd)
	base := strings.TrimSuffix(filepath.Base(o.PCB), filepath.Ext(o.PCB))

	var parts []Part
	if !o.NoAssembly {
		var schRows []SchPart
		if o.Sch != "" {
			schRows, err = ReadSchematicParts(o.Tools.CLI, o.Sch)
			if err != nil {
				return nil, err
			}
		}
		var warns []string
		parts, warns = MergeParts(bd, schRows, o.Sch != "")
		rep.Warnings = append(rep.Warnings, warns...)
		rep.Missing, rep.Invalid = CheckLCSC(parts)
		if len(rep.Missing)+len(rep.Invalid) > 0 {
			return rep, fmt.Errorf("%w: %d missing, %d invalid", ErrMissingLCSC, len(rep.Missing), len(rep.Invalid))
		}
	}
	ov, err := LoadCPLOverrides(o.CPLOverrides)
	if err != nil {
		return nil, err
	}

	gdir := filepath.Join(o.OutDir, "gerber")
	if err := os.MkdirAll(gdir, 0o755); err != nil {
		return nil, err
	}
	if err := runCLI(o.Tools.CLI, GerberArgs(o.PCB, gdir, rep.Layers), o.Log); err != nil {
		return nil, err
	}
	if err := runCLI(o.Tools.CLI, DrillArgs(o.PCB, gdir), o.Log); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(gdir)
	if err != nil {
		return nil, err
	}
	var zipList []string
	for _, e := range ents {
		if !e.IsDir() && isFabFile(e.Name()) {
			zipList = append(zipList, filepath.Join(gdir, e.Name()))
			rep.ZipFiles = append(rep.ZipFiles, e.Name())
		}
	}
	if len(zipList) == 0 {
		return nil, fmt.Errorf("kicad-cli produced no Gerber/drill files in %s", gdir)
	}
	rep.Zip = filepath.Join(o.OutDir, base+"-gerber.zip")
	if err := ZipFiles(rep.Zip, zipList); err != nil {
		return nil, err
	}

	if !o.NoAssembly {
		posPath := filepath.Join(o.OutDir, base+"-kicad-pos.csv")
		if err := runCLI(o.Tools.CLI, PosArgs(o.PCB, posPath), o.Log); err != nil {
			return nil, err
		}
		pf, err := os.Open(posPath)
		if err != nil {
			return nil, err
		}
		fpLib := map[string]string{}
		for _, f := range bd.Footprints {
			fpLib[f.Ref] = f.LibID
		}
		rows, applied, err := ConvertPosToCPL(pf, ov, fpLib)
		pf.Close()
		if err != nil {
			return nil, err
		}
		rep.Overrides = applied
		rep.CPL = filepath.Join(o.OutDir, base+"-cpl.csv")
		rep.CPLParts = len(rows) - 1
		if err := WriteCSV(rep.CPL, rows); err != nil {
			return nil, err
		}
		bom := BuildBOM(parts)
		rep.BOM = filepath.Join(o.OutDir, base+"-bom.csv")
		rep.BOMLines = len(bom) - 1
		if err := WriteCSV(rep.BOM, bom); err != nil {
			return nil, err
		}
		// A BOM designator missing from the CPL makes JLC reject the order.
		inCPL := map[string]bool{}
		for _, r := range rows[1:] {
			inCPL[r[0]] = true
		}
		for _, p := range parts {
			if p.Assembled() && !inCPL[p.Ref] {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s is in the BOM but not in the CPL", p.Ref))
			}
		}
	}
	return rep, WriteReport(filepath.Join(o.OutDir, "fab-report.json"), rep)
}

// WriteReport writes rep as indented JSON.
func WriteReport(path string, rep *FabReport) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
