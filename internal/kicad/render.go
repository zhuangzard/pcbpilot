package kicad

// render.go — deterministic previews of KiCad schematics and boards for
// visual regression (`pcbpilot kicad render`).
//
// kicad-cli plots a temp copy of the project (it drops .kicad_prl / lock
// files next to its input) with the isolated KiCad config of kicadenv.go, so
// no global library table is loaded and the colour theme is KiCad's default
// rather than whatever the user picked. Every SVG is then normalised
// (NormalizeSVG): the only run-dependent bytes KiCad 10 writes are the plot
// timestamp in <title> and, where a field echoes it, the temp path. Paint
// order is kept: it is deterministic and meaningful in SVG; the structural
// diff (SVGElements) compares elements as a multiset, so a pure reorder is
// reported as such instead of as added + removed.
//
// The 3D raster (`kicad-cli pcb render`) is a preview only: KiCad's raytracer
// is not bit-reproducible between runs (measured on 10.0.6), so PNGs are
// listed with their hash but never compared. Schematics have no raster:
// kicad-cli cannot write PNG for them and pcbpilot carries no SVG rasterizer.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

// RenderManifestName is the manifest a render writes into its output
// directory (file list + options); a re-render into the same directory
// deletes exactly the files it lists before plotting.
const RenderManifestName = "render.json"

// RenderOptions selects what Render plots.
type RenderOptions struct {
	Sch    string   // root .kicad_sch (whole hierarchy, one SVG per page)
	PCB    string   // .kicad_pcb
	Layers []string // PCB layers; default by Side
	Side   string   // "top" (default) or "bottom" (mirrored)
	OutDir string
	PNG    bool // PCB only: also a 3D raster from kicad-cli pcb render
	DPI    int  // PNG pixels per inch of board size (default 150)
}

// RenderFile is one written preview.
type RenderFile struct {
	File     string `json:"file"` // relative to the output directory
	Kind     string `json:"kind"` // sch-page | pcb-composite | pcb-layer | pcb-3d
	Layer    string `json:"layer,omitempty"`
	SHA256   string `json:"sha256"`
	Bytes    int    `json:"bytes"`
	Elements int    `json:"elements,omitempty"` // drawn SVG elements
	Width    int    `json:"widthPx,omitempty"`
	Height   int    `json:"heightPx,omitempty"`
}

// RenderManifest is <out>/render.json.
type RenderManifest struct {
	SchemaVersion int          `json:"schemaVersion"`
	Source        string       `json:"source"` // input file name (no directory)
	Kind          string       `json:"kind"`   // sch | pcb
	Side          string       `json:"side,omitempty"`
	Layers        []string     `json:"layers,omitempty"`
	Files         []RenderFile `json:"files"`
	Warnings      []string     `json:"warnings,omitempty"`
}

// RenderError is a render failure with a code and a fix hint.
type RenderError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func (e *RenderError) Error() string { return e.Code + ": " + e.Detail }

func renderErr(code, fix, format string, a ...any) *RenderError {
	return &RenderError{Code: code, Detail: fmt.Sprintf(format, a...), Fix: fix}
}

// DefaultRenderLayers are the PCB layers plotted per side.
func DefaultRenderLayers(side string) []string {
	if side == "bottom" {
		return []string{"B.Cu", "B.Mask", "B.Silkscreen", "Edge.Cuts"}
	}
	return []string{"F.Cu", "F.Mask", "F.Silkscreen", "Edge.Cuts"}
}

var renderLayerRE = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)

// Render plots o.Sch or o.PCB into o.OutDir and writes its manifest.
func Render(o RenderOptions) (*RenderManifest, error) {
	if (o.Sch == "") == (o.PCB == "") {
		return nil, renderErr("RENDER_BAD_INPUT", "pass exactly one of --sch ROOT.kicad_sch or --pcb BOARD.kicad_pcb", "need exactly one input")
	}
	if o.OutDir == "" {
		return nil, renderErr("RENDER_BAD_INPUT", "pass --out DIR", "no output directory")
	}
	if o.Side == "" {
		o.Side = "top"
	}
	if o.Side != "top" && o.Side != "bottom" {
		return nil, renderErr("RENDER_BAD_INPUT", "--side top or --side bottom", "unknown side %q", o.Side)
	}
	in := o.Sch
	if in == "" {
		in = o.PCB
	}
	if _, err := os.Stat(in); err != nil {
		return nil, renderErr("RENDER_INPUT_MISSING", "check the path", "%v", err)
	}
	if err := prepareRenderOut(o.OutDir); err != nil {
		return nil, err
	}
	cli, err := KicadCLI()
	if err != nil {
		return nil, renderErr("RENDER_NO_KICAD", "install KiCad 10 or set KICAD_CLI", "%v", err)
	}
	tmp, err := os.MkdirTemp("", "pcbpilot-render-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	copied, err := copyRenderProject(in, filepath.Join(tmp, "p"), o.PNG && o.PCB != "")
	if err != nil {
		return nil, err
	}
	m := &RenderManifest{SchemaVersion: 1, Source: filepath.Base(in)}
	if o.Sch != "" {
		m.Kind = "sch"
		if o.PNG {
			m.Warnings = append(m.Warnings, "png: not available for schematics (kicad-cli has no raster schematic export and pcbpilot has no SVG rasterizer); SVG only")
		}
		files, err := RenderSchSVG(cli, copied, o.OutDir, tmp)
		if err != nil {
			return nil, err
		}
		m.Files = files
	} else {
		m.Kind, m.Side = "pcb", o.Side
		m.Layers = o.Layers
		if len(m.Layers) == 0 {
			m.Layers = DefaultRenderLayers(o.Side)
		}
		for _, l := range m.Layers {
			if !renderLayerRE.MatchString(l) {
				return nil, renderErr("RENDER_BAD_INPUT", "use KiCad's untranslated layer names, e.g. F.Cu,F.Silkscreen", "bad layer name %q", l)
			}
		}
		files, warns, err := renderPCB(cli, copied, o, m.Layers, tmp)
		if err != nil {
			return nil, err
		}
		m.Files, m.Warnings = files, append(m.Warnings, warns...)
	}
	if !o.PNG && o.DPI != 0 {
		m.Warnings = append(m.Warnings, "dpi: ignored without --png")
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].File < m.Files[j].File })
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(o.OutDir, RenderManifestName), append(b, '\n'), 0o644); err != nil {
		return nil, err
	}
	return m, nil
}

// prepareRenderOut creates dir, or empties the files a previous render
// listed in its manifest. A non-empty directory without a manifest is
// refused: its files would mix into a later --compare.
func prepareRenderOut(dir string) error {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return err
	}
	if len(ents) == 0 {
		return nil
	}
	old, err := ReadRenderManifest(dir)
	if err != nil {
		return renderErr("RENDER_OUT_NOT_EMPTY", "use an empty or new --out directory (or one written by kicad render)",
			"%s is not empty and holds no %s", dir, RenderManifestName)
	}
	for _, f := range old.Files {
		if p, ok := renderJoin(dir, f.File); ok {
			_ = os.Remove(p)
		}
	}
	return os.Remove(filepath.Join(dir, RenderManifestName))
}

// ReadRenderManifest reads <dir>/render.json.
func ReadRenderManifest(dir string) (*RenderManifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, RenderManifestName))
	if err != nil {
		return nil, err
	}
	var m RenderManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// renderJoin joins a manifest entry below dir, refusing escapes.
func renderJoin(dir, rel string) (string, bool) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") || strings.ContainsAny(rel, `\:`) {
		return "", false
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), true
}

// renderCopyExt are the files kicad-cli needs to plot a project; 3D models
// travel only for the raster.
var renderCopyExt = map[string]bool{".kicad_sch": true, ".kicad_pcb": true, ".kicad_pro": true, ".kicad_wks": true,
	".kicad_dru": true, ".kicad_sym": true, ".kicad_mod": true}
var renderModelExt = map[string]bool{".step": true, ".stp": true, ".wrl": true, ".wrz": true, ".stpz": true}

const renderCopyLimit = 1 << 30

// copyRenderProject copies the project directory of in (its plot inputs
// only, skipping backups and hidden directories) to dst and returns in's
// path inside it.
func copyRenderProject(in, dst string, models bool) (string, error) {
	src := filepath.Dir(in)
	var total int64
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != src && (strings.HasPrefix(name, ".") || strings.HasSuffix(name, "-backups")) {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !renderCopyExt[ext] && !(models && renderModelExt[ext]) && name != "sym-lib-table" && name != "fp-lib-table" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if total += info.Size(); total > renderCopyLimit {
			return renderErr("RENDER_PROJECT_TOO_BIG", "render a directory holding only the project", "%s holds more than 1 GiB of KiCad files", src)
		}
		rel, _ := filepath.Rel(src, p)
		to := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return copyFile(p, to)
	})
	if err != nil {
		return "", err
	}
	return filepath.Join(dst, filepath.Base(in)), nil
}

func copyFile(src, dst string) error {
	r, err := os.Open(src)
	if err != nil {
		return err
	}
	defer r.Close()
	w, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// runRenderCLI runs kicad-cli with the isolated config and timeout.
func runRenderCLI(cli string, args ...string) error {
	cmd, cancel := SchCLICommand(cli, args...)
	defer cancel()
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(buf.String())
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		return renderErr("RENDER_KICAD_FAILED", "open the file in KiCad 10 once to check it loads; for a hang keep the default isolated config",
			"kicad-cli %s: %v: %s", strings.Join(args[:min(3, len(args))], " "), err, msg)
	}
	return nil
}

// RenderSchSVG plots every page of the hierarchy rooted at sch into outDir
// (kicad-cli's names: <base>.svg, <base>-<sheet path>.svg), normalised.
// strip are extra directory prefixes to remove from the SVG text (the
// directory sch lives in is always removed). sch-build --render uses it.
func RenderSchSVG(cli, sch, outDir string, strip ...string) ([]RenderFile, error) {
	raw, err := os.MkdirTemp("", "pcbpilot-sch-svg-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(raw)
	if err := runRenderCLI(cli, "sch", "export", "svg", "--output", raw, sch); err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(raw)
	if err != nil {
		return nil, err
	}
	strip = append(strip, filepath.Dir(sch), raw)
	var files []RenderFile
	for _, e := range ents {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".svg") {
			continue
		}
		f, err := writeNormalizedSVG(filepath.Join(raw, e.Name()), outDir, e.Name(), "sch-page", "", strip)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, renderErr("RENDER_KICAD_FAILED", "open the schematic in KiCad 10 once to check it loads", "kicad-cli plotted no page of %s", filepath.Base(sch))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].File < files[j].File })
	return files, nil
}

func writeNormalizedSVG(src, outDir, name, kind, layer string, strip []string) (RenderFile, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return RenderFile{}, err
	}
	b = NormalizeSVG(b, strip...)
	n := 0
	if els, err := SVGElements(b); err == nil {
		n = len(els)
	}
	if err := os.WriteFile(filepath.Join(outDir, name), b, 0o644); err != nil {
		return RenderFile{}, err
	}
	sum := sha256.Sum256(b)
	return RenderFile{File: name, Kind: kind, Layer: layer, SHA256: hex.EncodeToString(sum[:]), Bytes: len(b), Elements: n}, nil
}

func renderPCB(cli, pcb string, o RenderOptions, layers []string, tmp string) ([]RenderFile, []string, error) {
	base := strings.TrimSuffix(filepath.Base(pcb), filepath.Ext(pcb))
	raw := filepath.Join(tmp, "raw")
	if err := os.MkdirAll(raw, 0o755); err != nil {
		return nil, nil, err
	}
	strip := []string{filepath.Dir(pcb), raw, tmp}
	plot := func(name string, ls []string) error {
		args := []string{"pcb", "export", "svg", "--mode-single", "-l", strings.Join(ls, ","), "--page-size-mode", "2",
			"--exclude-drawing-sheet", "--drill-shape-opt", "2", "-o", filepath.Join(raw, name)}
		if o.Side == "bottom" {
			args = append(args, "--mirror")
		}
		return runRenderCLI(cli, append(args, pcb)...)
	}
	var files []RenderFile
	composite := base + "-pcb-" + o.Side + ".svg"
	if err := plot(composite, layers); err != nil {
		return nil, nil, err
	}
	f, err := writeNormalizedSVG(filepath.Join(raw, composite), o.OutDir, composite, "pcb-composite", "", strip)
	if err != nil {
		return nil, nil, err
	}
	files = append(files, f)
	for _, l := range layers {
		ls := []string{l}
		if l != "Edge.Cuts" {
			ls = append(ls, "Edge.Cuts") // the outline gives each layer its place
		}
		name := base + "-pcb-" + strings.ReplaceAll(l, ".", "_") + ".svg"
		if err := plot(name, ls); err != nil {
			return nil, nil, err
		}
		f, err := writeNormalizedSVG(filepath.Join(raw, name), o.OutDir, name, "pcb-layer", l, strip)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, f)
	}
	if !o.PNG {
		return files, nil, nil
	}
	var warns []string
	dpi := o.DPI
	if dpi <= 0 {
		dpi = 150
	}
	wmm, hmm := svgSizeMM(filepath.Join(o.OutDir, composite))
	w, h := renderPixels(wmm, dpi), renderPixels(hmm, dpi)
	if w == 0 || h == 0 {
		w, h = 1600, 900
		warns = append(warns, "png: board size not readable from the SVG; 1600x900")
	}
	name := base + "-3d-" + o.Side + ".png"
	out := filepath.Join(o.OutDir, name)
	if err := runRenderCLI(cli, "pcb", "render", "--side", o.Side, "-w", strconv.Itoa(w), "-h", strconv.Itoa(h),
		"--quality", "basic", "-o", out, pcb); err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, nil, renderErr("RENDER_KICAD_FAILED", "check kicad-cli pcb render on this board", "no PNG written: %v", err)
	}
	sum := sha256.Sum256(b)
	files = append(files, RenderFile{File: name, Kind: "pcb-3d", SHA256: hex.EncodeToString(sum[:]), Bytes: len(b), Width: w, Height: h})
	warns = append(warns, "png: 3D raytrace preview, not bit-reproducible; --compare ignores it")
	return files, warns, nil
}

// renderPixels is mm at dpi, clamped to 64..8000 px (0 for no size).
func renderPixels(mm float64, dpi int) int {
	if !(mm > 0) {
		return 0
	}
	return int(math.Max(64, math.Min(8000, math.Round(mm*float64(dpi)/25.4))))
}

var svgSizeRE = regexp.MustCompile(`<svg[^>]*?\swidth="([0-9.]+)mm"\s+height="([0-9.]+)mm"`)

func svgSizeMM(path string) (float64, float64) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, 0
	}
	if len(b) > 4096 {
		b = b[:4096]
	}
	m := svgSizeRE.FindSubmatch(b)
	if m == nil {
		return 0, 0
	}
	w, _ := strconv.ParseFloat(string(m[1]), 64)
	h, _ := strconv.ParseFloat(string(m[2]), 64)
	return w, h
}

var (
	svgTitleDateRE = regexp.MustCompile(`(<title>[^<]*?) date [0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+(Z|[+-][0-9:]+)?\s*(</title>)`)
	svgNegZeroRE   = regexp.MustCompile(`(^|[^0-9.eE])-(0(?:\.0+)?)([^0-9.]|$)`)
	svgTrailWSRE   = regexp.MustCompile(`[ \t]+\n`)
	svgTitleWSRE   = regexp.MustCompile(`\s+</title>`)
)

// NormalizeSVG makes a kicad-cli SVG byte-identical across runs: LF line
// ends, the plot date dropped from <title>, every strip directory (in its
// native, forward-slash and escaped spellings) removed, "-0" written "0",
// trailing blanks trimmed. Drawing content and paint order are untouched.
func NormalizeSVG(b []byte, strip ...string) []byte {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = svgTitleDateRE.ReplaceAllString(s, "$1$3")
	s = svgTitleWSRE.ReplaceAllString(s, "</title>")
	var forms []string
	for _, d := range strip {
		if d = strings.TrimRight(d, `\/`); d == "" {
			continue
		}
		forms = append(forms, d, filepath.ToSlash(d), strings.ReplaceAll(d, `\`, `\\`), strings.ReplaceAll(d, `/`, `\`))
	}
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) }) // longest first
	for _, f := range forms {
		if len(f) > 3 { // never strip a bare drive or root
			s = strings.ReplaceAll(s, f+`\\`, "")
			s = strings.ReplaceAll(s, f+`\`, "")
			s = strings.ReplaceAll(s, f+`/`, "")
			s = strings.ReplaceAll(s, f, ".")
		}
	}
	for range 2 { // a match eats its right neighbour: twice catches "-0 -0"
		s = svgNegZeroRE.ReplaceAllString(s, "$1$2$3")
	}
	s = svgTrailWSRE.ReplaceAllString(s, "\n")
	return []byte(s)
}

// svgDrawn are the SVG elements that put ink on the page.
var svgDrawn = map[string]bool{"svg": true, "path": true, "circle": true, "rect": true, "line": true, "polyline": true,
	"polygon": true, "ellipse": true, "text": true, "image": true, "use": true}

var svgSpaceRE = regexp.MustCompile(`\s+`)

// SVGElements is the structural form of an SVG: one canonical string per
// drawn element (tag, sorted attributes with collapsed whitespace, text, and
// the attributes of every enclosing <g>, which carry colour and transform),
// in document order. Two SVGs draw the same thing iff the multisets match.
func SVGElements(b []byte) ([]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	var ctx []string // per open element: its group context ("" for non-g)
	var stack []string
	var out []string
	cur := -1 // index in out of an open text element
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			attrs := make([]string, 0, len(t.Attr))
			for _, a := range t.Attr {
				name := a.Name.Local
				if a.Name.Space != "" {
					name = a.Name.Space + ":" + name
				}
				attrs = append(attrs, name+"="+strings.TrimSpace(svgSpaceRE.ReplaceAllString(a.Value, " ")))
			}
			sort.Strings(attrs)
			parent := ""
			if len(ctx) > 0 {
				parent = ctx[len(ctx)-1]
			}
			here := parent
			if t.Name.Local == "g" {
				here = parent + "g{" + strings.Join(attrs, ";") + "}"
			}
			ctx, stack = append(ctx, here), append(stack, t.Name.Local)
			if svgDrawn[t.Name.Local] {
				out = append(out, parent+"|"+t.Name.Local+"{"+strings.Join(attrs, ";")+"}")
				if t.Name.Local == "text" {
					cur = len(out) - 1
				}
			}
		case xml.EndElement:
			if len(stack) > 0 {
				if stack[len(stack)-1] == "text" {
					cur = -1
				}
				ctx, stack = ctx[:len(ctx)-1], stack[:len(stack)-1]
			}
		case xml.CharData:
			if cur >= 0 {
				if s := strings.TrimSpace(string(t)); s != "" {
					out[cur] += "=" + s
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no SVG element")
	}
	return out, nil
}

// RenderFileDiff is one file's compare verdict.
type RenderFileDiff struct {
	File             string   `json:"file"`
	Status           string   `json:"status"` // identical | changed | added | removed | unreadable
	BaselineSHA256   string   `json:"baselineSha256,omitempty"`
	CurrentSHA256    string   `json:"currentSha256,omitempty"`
	ElementsBaseline int      `json:"elementsBaseline,omitempty"`
	ElementsCurrent  int      `json:"elementsCurrent,omitempty"`
	ElementsAdded    int      `json:"elementsAdded,omitempty"`
	ElementsRemoved  int      `json:"elementsRemoved,omitempty"`
	ChangedElements  int      `json:"changedElements,omitempty"` // added + removed
	Reordered        bool     `json:"reordered,omitempty"`       // same elements, other paint order
	Samples          []string `json:"samples,omitempty"`         // a few differing elements, clipped
	Detail           string   `json:"detail,omitempty"`
}

// RenderCompare is the --compare report.
type RenderCompare struct {
	Verdict  gate.Status      `json:"verdict"`
	Baseline string           `json:"baseline"`
	Current  string           `json:"current"`
	Counts   map[string]int   `json:"counts"`
	Files    []RenderFileDiff `json:"files"`
	Ignored  []string         `json:"ignored,omitempty"` // PNG previews, not compared
	Notes    []string         `json:"notes,omitempty"`
	Fix      string           `json:"fix,omitempty"`
}

// listSVGs maps relative slash paths of every .svg below dir (and lists the
// .png previews separately).
func listSVGs(dir string) (map[string]string, []string, error) {
	svgs := map[string]string{}
	var pngs []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		switch strings.ToLower(filepath.Ext(p)) {
		case ".svg":
			svgs[rel] = p
		case ".png":
			pngs = append(pngs, rel)
		}
		return nil
	})
	return svgs, pngs, err
}

// CompareRenders diffs the SVGs of current against baseline: identical
// (same normalised bytes), changed (counted from the structural diff),
// added, removed, or unreadable. Verdict: pass when every file is
// identical, fail when any differs, cannot-check when a side has no SVG or
// a file cannot be parsed and nothing differs.
func CompareRenders(baseline, current string) *RenderCompare {
	r := &RenderCompare{Baseline: baseline, Current: current, Counts: map[string]int{}}
	base, bpng, berr := listSVGs(baseline)
	cur, cpng, cerr := listSVGs(current)
	switch {
	case berr != nil || len(base) == 0:
		r.Verdict, r.Fix = gate.CannotCheck, "point --compare at the --out directory of an earlier kicad render"
		r.Notes = append(r.Notes, fmt.Sprintf("baseline %s has no SVG (%v)", baseline, berr))
		return r
	case cerr != nil || len(cur) == 0:
		r.Verdict, r.Fix = gate.CannotCheck, "render first (--sch or --pcb) or point --out at a rendered directory"
		r.Notes = append(r.Notes, fmt.Sprintf("current %s has no SVG (%v)", current, cerr))
		return r
	}
	seen := map[string]bool{}
	for _, p := range append(bpng, cpng...) {
		if !seen[p] {
			seen[p] = true
			r.Ignored = append(r.Ignored, p)
		}
	}
	sort.Strings(r.Ignored)
	names := map[string]bool{}
	for n := range base {
		names[n] = true
	}
	for n := range cur {
		names[n] = true
	}
	var order []string
	for n := range names {
		order = append(order, n)
	}
	sort.Strings(order)
	var results []gate.Result
	for _, n := range order {
		d := diffSVGFile(n, base[n], cur[n])
		r.Files = append(r.Files, d)
		r.Counts[d.Status]++
		st := gate.Fail
		switch d.Status {
		case "identical":
			st = gate.Pass
		case "unreadable":
			st = gate.CannotCheck
		}
		results = append(results, gate.Result{Name: n, Status: st})
	}
	r.Verdict = gate.Aggregate(results...)
	bm, _ := ReadRenderManifest(baseline)
	cm, _ := ReadRenderManifest(current)
	if bm != nil && cm != nil && (bm.Kind != cm.Kind || bm.Side != cm.Side || strings.Join(bm.Layers, ",") != strings.Join(cm.Layers, ",")) {
		r.Notes = append(r.Notes, fmt.Sprintf("render options differ: baseline %s %s %v, current %s %s %v", bm.Kind, bm.Side, bm.Layers, cm.Kind, cm.Side, cm.Layers))
	}
	return r
}

func fileSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func diffSVGFile(name, bp, cp string) RenderFileDiff {
	d := RenderFileDiff{File: name}
	if bp == "" {
		d.Status = "added"
		return d
	}
	if cp == "" {
		d.Status = "removed"
		return d
	}
	bb, err1 := os.ReadFile(bp)
	cb, err2 := os.ReadFile(cp)
	if err1 != nil || err2 != nil {
		d.Status, d.Detail = "unreadable", fmt.Sprintf("read: %v %v", err1, err2)
		return d
	}
	// Normalise both sides again: a baseline from an older render (or
	// sch-build before this command) may still carry its plot date.
	bb, cb = NormalizeSVG(bb), NormalizeSVG(cb)
	d.BaselineSHA256, d.CurrentSHA256 = fileSHA(bb), fileSHA(cb)
	if d.BaselineSHA256 == d.CurrentSHA256 {
		d.Status = "identical"
		return d
	}
	be, err1 := SVGElements(bb)
	ce, err2 := SVGElements(cb)
	if err1 != nil || err2 != nil {
		d.Status, d.Detail = "unreadable", fmt.Sprintf("parse: baseline %v, current %v", err1, err2)
		return d
	}
	d.ElementsBaseline, d.ElementsCurrent = len(be), len(ce)
	count := map[string]int{}
	for _, e := range be {
		count[e]++
	}
	for _, e := range ce {
		count[e]--
	}
	var keys []string
	for k, v := range count {
		switch {
		case v > 0:
			d.ElementsRemoved += v
		case v < 0:
			d.ElementsAdded -= v
		default:
			continue
		}
		keys = append(keys, k)
	}
	d.ChangedElements = d.ElementsAdded + d.ElementsRemoved
	d.Status = "changed"
	if d.ChangedElements == 0 {
		d.Reordered = true
		d.Detail = "same elements in another paint order"
		return d
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(d.Samples) == 4 {
			break
		}
		sign := "+"
		if count[k] > 0 {
			sign = "-"
		}
		if i := strings.LastIndex(k, "|"); i >= 0 {
			k = k[i+1:] // drop the group context from the sample
		}
		if len(k) > 160 {
			k = k[:160] + "…"
		}
		d.Samples = append(d.Samples, sign+" "+k)
	}
	return d
}
