package kicad

// render.go — deterministic previews of KiCad schematics and boards for
// visual regression (`pcbpilot kicad render`).
//
// kicad-cli plots a temp copy of the project and its dependency closure
// (render_deps.go; kicad-cli drops .kicad_prl / lock files next to its
// input) with the isolated KiCad config of kicadenv.go, so no global library
// table is loaded and the colour theme is KiCad's default. Every page of the
// hierarchy must come out as a parseable SVG that draws something, and
// kicad-cli's own messages are kept: a missing sheet, symbol, footprint or
// library in them makes the render cannot-check. Each SVG is then
// normalised (render_svg.go). The new render is assembled in a staging
// directory and replaces the previous one in --out only when complete.
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
	"errors"
	"fmt"
	"image/png"
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
// directory. A re-render into the same directory replaces exactly the files
// it lists; it lists only plain file names the renderer writes.
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
	// ReportName (a plain *.json file name) is published inside OutDir with
	// the render, listed in render.json as kind "report". Report builds its
	// bytes from the finished manifest and the staged directory (holding
	// the SVGs and render.json) before anything in OutDir changes.
	ReportName string
	Report     func(m *RenderManifest, staged string) ([]byte, error)
}

// RenderFile is one written preview.
type RenderFile struct {
	File     string `json:"file"` // plain file name inside the output directory
	Kind     string `json:"kind"` // sch-page | pcb-composite | pcb-layer | pcb-3d | report
	Page     string `json:"page,omitempty"`
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
	ReadInPlace   []string     `json:"readInPlace,omitempty"` // libraries kicad-cli read outside the copy
	Warnings      []string     `json:"warnings,omitempty"`    // kicad-cli messages and notes
}

// RenderError is a render failure with a code and a fix hint.
type RenderError struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func (e *RenderError) Error() string { return e.Code + ": " + e.Detail }

// Usage reports an error in the request itself (bad flags, unusable
// --out), as opposed to a render that could not be checked.
func (e *RenderError) Usage() bool {
	switch e.Code {
	case "RENDER_BAD_INPUT", "RENDER_INPUT_MISSING", "RENDER_OUT_NOT_EMPTY", "RENDER_OUT_UNSAFE", "RENDER_OUT_BAD_MANIFEST", "RENDER_OUT_PARTIAL":
		return true
	}
	return false
}

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

var (
	renderLayerRE = regexp.MustCompile(`^[A-Za-z0-9_.]+$`)
	// renderNameRE is every file name a render writes (and a manifest may list).
	renderNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+()=, -]*\.(svg|png)$`)
	renderKinds  = map[string]bool{"sch-page": true, "pcb-composite": true, "pcb-layer": true, "pcb-3d": true}
	// renderReportNameRE is the only form --report takes: a plain .json file
	// name, written inside --out.
	renderReportNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.+()=, -]*\.json$`)
)

// ValidRenderReportName reports whether name can be a --report file.
func ValidRenderReportName(name string) bool {
	return renderReportNameRE.MatchString(name) && !strings.Contains(name, "..") && !strings.EqualFold(name, RenderManifestName)
}

// Render plots o.Sch or o.PCB into o.OutDir and writes its manifest. The
// previous render in o.OutDir is replaced only after the new one is
// complete; on any error it is left as it was.
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
	m := &RenderManifest{SchemaVersion: 1}
	in := o.Sch
	if in == "" {
		in = o.PCB
		m.Kind, m.Side, m.Layers = "pcb", o.Side, o.Layers
		if len(m.Layers) == 0 {
			m.Layers = DefaultRenderLayers(o.Side)
		}
		for _, l := range m.Layers {
			if !renderLayerRE.MatchString(l) {
				return nil, renderErr("RENDER_BAD_INPUT", "use KiCad's untranslated layer names, e.g. F.Cu,F.Silkscreen", "bad layer name %q", l)
			}
		}
	} else {
		m.Kind = "sch"
		if len(o.Layers) > 0 {
			m.Warnings = append(m.Warnings, "layers: ignored for a schematic")
		}
	}
	m.Source = filepath.Base(in)
	if o.ReportName != "" && !ValidRenderReportName(o.ReportName) {
		return nil, renderErr("RENDER_BAD_INPUT", "--report takes a plain file name such as report.json (written inside --out)", "bad report name %q", o.ReportName)
	}
	if st, err := os.Stat(in); err != nil || st.IsDir() {
		return nil, renderErr("RENDER_INPUT_MISSING", "check the path", "%s is not a file (%v)", in, err)
	}
	out, err := checkRenderOut(o.OutDir)
	if err != nil {
		return nil, err
	}
	cli, err := KicadCLI()
	if err != nil {
		return nil, renderErr("RENDER_NO_KICAD", "install KiCad 10 or set KICAD_CLI", "%v", err)
	}
	deps, err := collectRenderDeps(in)
	if err != nil {
		return nil, err
	}
	if len(deps.Unresolved) > 0 {
		return nil, renderErr("RENDER_DEPENDENCY_UNRESOLVED", "make the named sheets / libraries / drawing sheets reachable (relative to the project, or defined variables), then render again",
			"%s", strings.Join(deps.Unresolved, "; "))
	}
	m.ReadInPlace = deps.InPlace
	tmp, err := os.MkdirTemp("", "pcbpilot-render-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	proj, err := copyRenderDeps(deps, filepath.Join(tmp, "m"), o.PNG && o.PCB != "")
	if err != nil {
		return nil, err
	}
	copied := filepath.Join(proj, filepath.Base(in))
	stage := filepath.Join(tmp, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return nil, err
	}
	// Paths in the drawing read as the user's own directories, not the copy.
	from, to := filepath.Join(tmp, "m"), deps.Root
	if m.Kind == "sch" {
		if o.PNG {
			m.Warnings = append(m.Warnings, "png: not available for schematics (kicad-cli has no raster schematic export and pcbpilot has no SVG rasterizer); SVG only")
		}
		files, warns, err := RenderSchSVG(cli, copied, stage, from, to)
		if err != nil {
			return nil, err
		}
		m.Files, m.Warnings = files, append(m.Warnings, warns...)
	} else {
		files, warns, err := renderPCB(cli, copied, o, m.Layers, stage, from, to)
		if err != nil {
			return nil, err
		}
		m.Files, m.Warnings = files, append(m.Warnings, warns...)
	}
	if !o.PNG && o.DPI != 0 {
		m.Warnings = append(m.Warnings, "dpi: ignored without --png")
	}
	if o.ReportName != "" {
		for _, f := range m.Files {
			if strings.EqualFold(f.File, o.ReportName) {
				return nil, renderErr("RENDER_BAD_INPUT", "pick another --report name", "--report %q is also a render file", o.ReportName)
			}
		}
		m.Files = append(m.Files, RenderFile{File: o.ReportName, Kind: "report"})
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].File < m.Files[j].File })
	if err := writeManifest(stage, m); err != nil {
		return nil, err
	}
	if o.ReportName != "" {
		var b []byte
		if o.Report != nil {
			if b, err = o.Report(m, stage); err != nil {
				return nil, err
			}
		}
		if err := os.WriteFile(filepath.Join(stage, o.ReportName), b, 0o644); err != nil {
			return nil, err
		}
	}
	if err := publishRender(stage, out, m); err != nil {
		return nil, err
	}
	return m, nil
}

// checkRenderOut validates --out before anything is plotted and returns its
// resolved path: a missing or empty directory, or one holding exactly a
// valid render.json and regular files it lists. Anything else — foreign
// files, subdirectories, links, a manifest naming paths the renderer never
// writes — is refused.
func checkRenderOut(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if os.IsNotExist(err) {
		return abs, nil
	}
	if err != nil {
		return "", err
	}
	if !st.IsDir() {
		return "", renderErr("RENDER_OUT_UNSAFE", "pass a directory as --out", "%s is not a directory", dir)
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return "", err
	}
	if len(ents) == 0 {
		return abs, nil
	}
	if _, err := os.Lstat(filepath.Join(abs, RenderBackupName)); err == nil {
		return "", renderErr("RENDER_OUT_PARTIAL", "a previous publish was interrupted: restore "+RenderBackupName+" by hand or use a new --out",
			"%s holds %s", dir, RenderBackupName)
	}
	m, err := readStrictManifest(abs)
	if err != nil {
		return "", err
	}
	listed := map[string]bool{RenderManifestName: true}
	for _, f := range m.Files {
		listed[f.File] = true
	}
	for _, e := range ents {
		if e.Name() == RenderBackupName {
			return "", renderErr("RENDER_OUT_PARTIAL", "a previous publish was interrupted: restore "+RenderBackupName+" by hand or use a new --out",
				"%s holds %s", dir, RenderBackupName)
		}
		if !listed[e.Name()] {
			return "", renderErr("RENDER_OUT_NOT_EMPTY", "use an empty or new --out directory (or one written by kicad render, without other files)",
				"%s holds %q, which no render wrote", dir, e.Name())
		}
		info, err := os.Lstat(filepath.Join(abs, e.Name()))
		if err != nil || !info.Mode().IsRegular() {
			return "", renderErr("RENDER_OUT_UNSAFE", "remove the link / directory from --out or use a new --out",
				"%s/%s is not a regular file", dir, e.Name())
		}
	}
	return abs, nil
}

// readStrictManifest reads <dir>/render.json and accepts only what Render
// writes: schema 1, known kinds, unique plain file names (.svg / .png).
func readStrictManifest(dir string) (*RenderManifest, error) {
	bad := func(format string, a ...any) error {
		return renderErr("RENDER_OUT_BAD_MANIFEST", "use a new --out directory; render.json there was not written by kicad render",
			"%s: "+format, append([]any{filepath.Join(dir, RenderManifestName)}, a...)...)
	}
	info, err := os.Lstat(filepath.Join(dir, RenderManifestName))
	if os.IsNotExist(err) {
		return nil, renderErr("RENDER_OUT_NOT_EMPTY", "use an empty or new --out directory (or one written by kicad render)",
			"%s is not empty and holds no %s", dir, RenderManifestName)
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, bad("not a regular file")
	}
	m, err := ReadRenderManifest(dir)
	if err != nil {
		return nil, bad("%v", err)
	}
	if m.SchemaVersion != 1 || (m.Kind != "sch" && m.Kind != "pcb") {
		return nil, bad("schemaVersion %d kind %q", m.SchemaVersion, m.Kind)
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		okName := renderNameRE.MatchString(f.File) && renderKinds[f.Kind]
		if f.Kind == "report" {
			okName = ValidRenderReportName(f.File)
		}
		if !okName || strings.Contains(f.File, "..") || seen[strings.ToLower(f.File)] {
			return nil, bad("entry %q (kind %q) is not a file kicad render writes", f.File, f.Kind)
		}
		seen[strings.ToLower(f.File)] = true
	}
	return m, nil
}

// ReadRenderManifest reads <dir>/render.json (no validation).
func ReadRenderManifest(dir string) (*RenderManifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, RenderManifestName))
	if err != nil {
		return nil, err
	}
	var m RenderManifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// publishRender replaces the render in out with the staged one: out is
// checked again, the old listed files are removed (regular files only; a
// name is a plain file name, so nothing outside out is reachable), the new
// files are copied in and the manifest is written last.
func publishRender(stage, out string, m *RenderManifest) (err error) {
	if _, err := checkRenderOut(out); err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for _, f := range m.Files {
		if !renderNameRE.MatchString(f.File) && !(f.Kind == "report" && ValidRenderReportName(f.File)) {
			return renderErr("RENDER_FAILED", "", "refusing to write %q", f.File)
		}
	}
	// 1. Park the old render (its listed files and render.json) in the
	// backup directory; its presence marks a publish in progress.
	backup := filepath.Join(out, RenderBackupName)
	if err := os.Mkdir(backup, 0o755); err != nil {
		return renderErr("RENDER_OUT_PARTIAL", "a previous publish was interrupted: restore "+RenderBackupName+" by hand or use a new --out", "%v", err)
	}
	var parked []string
	if old, err := readStrictManifest(out); err == nil {
		for _, f := range append(old.Files, RenderFile{File: RenderManifestName}) {
			p := filepath.Join(out, f.File)
			if info, err := os.Lstat(p); err != nil || !info.Mode().IsRegular() {
				continue
			}
			if err := os.Rename(p, filepath.Join(backup, f.File)); err != nil {
				restoreRender(out, backup, parked, nil)
				return err
			}
			parked = append(parked, f.File)
		}
	}
	// 2. Copy the new files, render.json last; on any failure remove what
	// was written and move the old render back.
	var written []string
	defer func() {
		if err != nil {
			if rerr := restoreRender(out, backup, parked, written); rerr != nil {
				err = renderErr("RENDER_OUT_PARTIAL", "restore "+filepath.Join(out, RenderBackupName)+" by hand or use a new --out",
					"publish failed (%v) and the previous render could not be restored: %v", err, rerr)
			}
		}
	}()
	for _, f := range m.Files {
		if err := renderPublishHook("copy", f.File); err != nil {
			return err
		}
		p := filepath.Join(out, f.File)
		if info, err := os.Lstat(p); err == nil && !info.Mode().IsRegular() {
			return renderErr("RENDER_OUT_UNSAFE", "remove it from --out", "%s is not a regular file", p)
		}
		written = append(written, f.File)
		if err := copyFile(filepath.Join(stage, f.File), p); err != nil {
			return err
		}
	}
	written = append(written, RenderManifestName)
	if err := writeManifest(out, m); err != nil {
		return err
	}
	// 3. Done: drop the parked old render.
	return os.RemoveAll(backup)
}

// RenderBackupName is the directory inside --out that holds the previous
// render while a new one is published; compare refuses a directory that
// has one (an interrupted publish).
const RenderBackupName = ".render-backup"

// renderPublishHook lets tests fail a publish step ("copy", "restore").
var renderPublishHook = func(step, name string) error { return nil }

// restoreRender undoes a failed publish: removes the new files, moves the
// parked old ones back and removes the backup directory. On error the
// backup directory stays, so the output is recognisably incomplete.
func restoreRender(out, backup string, parked, written []string) error {
	for _, f := range written {
		if err := os.Remove(filepath.Join(out, f)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	for _, f := range parked {
		if err := renderPublishHook("restore", f); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(backup, f), filepath.Join(out, f)); err != nil {
			return err
		}
	}
	return os.Remove(backup)
}

func writeManifest(dir string, m *RenderManifest) error {
	b, _ := json.MarshalIndent(m, "", "  ")
	return os.WriteFile(filepath.Join(dir, RenderManifestName), append(b, '\n'), 0o644)
}

// renderCopyExt are the files kicad-cli needs to plot a project; 3D models
// travel only for the raster.
var renderCopyExt = map[string]bool{".kicad_sch": true, ".kicad_pcb": true, ".kicad_pro": true, ".kicad_wks": true,
	".kicad_dru": true, ".kicad_sym": true, ".kicad_mod": true}
var renderModelExt = map[string]bool{".step": true, ".stp": true, ".wrl": true, ".wrz": true, ".stpz": true}

const renderCopyLimit = 1 << 30

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

// renderModelMsgRE matches kicad-cli's messages about an optional 3D model
// it could not load, matched narrowly: "could not / cannot / unable to /
// failed to  load|find|open  [the] 3D model …" at the start of the line.
var renderModelMsgRE = regexp.MustCompile(`(?i)^(warning: |error: )?(could not|cannot|unable to|failed to) (load|find|open) (the )?3d[ -]?model\b`)

var (
	// kicad-cli progress lines that carry no finding.
	renderQuietRE = regexp.MustCompile(`^(Plotted to |Done\.|Loading|Build |Create |Simplif|Calculating|Load Raytracing|Loading 3D|Reload time|Rendering|Successfully created)`)
	// A message about something the plot could not load.
	// Any failure wording blocks (checked before the progress filter).
	renderFailRE = regexp.MustCompile(`(?i)(fail|error|cannot|can't|unable|permission denied|not found|missing|could not|does not exist)`)
	// Known-harmless lines that may carry failure wording: Fontconfig's
	// config noise about system fonts (seen under go test on Windows).
	// Plain progress lines name only the file kicad-cli wrote (whose name
	// may itself contain such a word).
	renderHarmlessRE = regexp.MustCompile(`^(Fontconfig (error|warning):|Plotted to '[^']*'\.?$|Done\.$)`)
)

// runRenderCLI runs kicad-cli with the isolated config and timeout and
// returns its messages (progress lines dropped). A message with failure
// wording turns into a RENDER_KICAD_DIAGNOSTIC error even when kicad-cli
// exits 0, unless exempt (non-nil) matches it: the only exemption is the
// 3D raster's optional models (renderModelMsgRE).
func runRenderCLI(cli string, exempt *regexp.Regexp, args ...string) ([]string, error) {
	cmd, cancel := SchCLICommand(cli, args...)
	defer cancel()
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	what := "kicad-cli " + strings.Join(args[:min(3, len(args))], " ")
	var msgs, missing []string
	for _, l := range strings.Split(strings.ReplaceAll(buf.String(), "\r", ""), "\n") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		// Classify the whole original line first; a progress prefix
		// ("Loading…") never hides a missing object. Fontconfig's own
		// config noise (seen under go test on Windows) is about system
		// fonts, not the design: kept as a warning only.
		miss := renderFailRE.MatchString(l) && !renderHarmlessRE.MatchString(l)
		if !miss && renderQuietRE.MatchString(l) {
			continue
		}
		shown := l // only what is displayed is shortened
		if r := []rune(shown); len(r) > 300 {
			shown = string(r[:300]) + "…"
		}
		msgs = append(msgs, what+": "+shown)
		if miss && !(exempt != nil && exempt.MatchString(l)) {
			missing = append(missing, shown)
		}
	}
	if err != nil {
		return msgs, renderErr("RENDER_KICAD_FAILED", "open the file in KiCad 10 once to check it loads; for a hang keep the default isolated config",
			"%s: %v: %s", what, err, strings.Join(msgs, " | "))
	}
	if len(missing) > 0 {
		return msgs, renderErr("RENDER_KICAD_DIAGNOSTIC", "fix what kicad-cli could not load (or make it reachable from the project) and render again",
			"%s: %s", what, strings.Join(missing, " | "))
	}
	return msgs, nil
}

// RenderSchSVG plots every page of the hierarchy rooted at sch into outDir
// (kicad-cli's names: <base>.svg, <base>-<sheet path>.svg). Every page the
// hierarchy reaches must come out as an SVG that parses and draws; nothing
// is written to outDir unless all do. Paths under from print as to in the
// drawing (sch-build: stage → --out). sch-build --render uses it too.
func RenderSchSVG(cli, sch, outDir, from, to string) ([]RenderFile, []string, error) {
	pages, _, err := ParseSchHierarchy(sch)
	if err != nil {
		return nil, nil, renderErr("RENDER_DEPENDENCY_UNRESOLVED", "check every sheet of the hierarchy loads", "%v", err)
	}
	base := strings.TrimSuffix(filepath.Base(sch), filepath.Ext(sch))
	want, err := schPageFiles(base, pages)
	if err != nil {
		return nil, nil, err
	}
	raw, err := os.MkdirTemp("", "pcbpilot-sch-svg-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(raw)
	warns, err := runRenderCLI(cli, nil, "sch", "export", "svg", "--output", raw, sch)
	if err != nil {
		return nil, warns, err
	}
	ents, err := os.ReadDir(raw)
	if err != nil {
		return nil, warns, err
	}
	got := map[string]bool{}
	for _, e := range ents {
		if e.Type().IsRegular() && strings.EqualFold(filepath.Ext(e.Name()), ".svg") {
			got[e.Name()] = true
		}
	}
	var missing []string
	for f, page := range want {
		if !got[f] {
			if page == "" {
				page = "(root)"
			}
			missing = append(missing, fmt.Sprintf("%s → %s", page, f))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, warns, renderErr("RENDER_INCOMPLETE", "check the named sheets load in KiCad 10 (sheet names with characters kicad-cli rewrites cannot be matched)",
			"kicad-cli plotted %d file(s) but these pages of the hierarchy have none: %s", len(got), strings.Join(missing, ", "))
	}
	var files []RenderFile
	var data [][]byte
	for name := range got {
		f, b, err := normalizedSVG(filepath.Join(raw, name), name, "sch-page", []string{filepath.Dir(sch), raw, from}, from, to)
		if err != nil {
			return nil, warns, err
		}
		f.Page = want[name]
		files, data = append(files, f), append(data, b)
	}
	for i, f := range files {
		if !renderNameRE.MatchString(f.File) {
			return nil, warns, renderErr("RENDER_INCOMPLETE", "rename the sheet to letters, digits and -_.", "page file name %q is not one kicad render can manage", f.File)
		}
		if err := os.WriteFile(filepath.Join(outDir, f.File), data[i], 0o644); err != nil {
			return nil, warns, err
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].File < files[j].File })
	return files, warns, nil
}

// schPageFiles maps each page of the hierarchy to the file kicad-cli plots
// it to (<base>.svg, <base>-<sheet path, "/" → "-">.svg). Two pages that
// flatten to one name ("a/b" and "a-b") would let one file stand for both,
// so that is refused before plotting.
func schPageFiles(base string, pages []SchPage) (map[string]string, error) {
	want := map[string]string{}
	used := map[string]string{} // case-folded file name → page that claimed it
	for _, p := range pages {
		f := base
		if p.Name != "" {
			f += "-" + strings.ReplaceAll(p.Name, "/", "-")
		}
		f += ".svg"
		key := strings.ToLower(f) // one file on a case-insensitive disk
		if page, dup := used[key]; dup {
			return nil, renderErr("RENDER_INCOMPLETE", "give every sheet a distinct name (also after \"/\" → \"-\" and ignoring case)",
				"pages %q and %q would both plot to %s", page, p.Name, f)
		}
		used[key], want[f] = p.Name, p.Name
	}
	return want, nil
}

// normalizedSVG reads and normalises one plotted SVG and checks it draws.
func normalizedSVG(src, name, kind string, temp []string, from, to string) (RenderFile, []byte, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return RenderFile{}, nil, err
	}
	if to == "" {
		from = ""
	}
	mapped := []string{}
	if from != "" {
		mapped = append(mapped, from)
	}
	// Header paths: every temp directory; drawing paths: from → to.
	b = NormalizeSVG(NormalizeSVG(b, mapped, to), temp, "")
	els, err := SVGElements(b)
	if err != nil {
		return RenderFile{}, nil, renderErr("RENDER_BAD_SVG", "check the file opens in KiCad 10; an empty page cannot be compared", "%s: %v", name, err)
	}
	sum := sha256.Sum256(b)
	return RenderFile{File: name, Kind: kind, SHA256: hex.EncodeToString(sum[:]), Bytes: len(b), Elements: len(els)}, b, nil
}

func renderPCB(cli, pcb string, o RenderOptions, layers []string, stage, from, to string) ([]RenderFile, []string, error) {
	base := strings.TrimSuffix(filepath.Base(pcb), filepath.Ext(pcb))
	raw := filepath.Join(filepath.Dir(stage), "raw")
	if err := os.MkdirAll(raw, 0o755); err != nil {
		return nil, nil, err
	}
	var warns []string
	temp := []string{filepath.Dir(pcb), raw, from}
	plot := func(name, kind, layer string, ls []string) (RenderFile, error) {
		args := []string{"pcb", "export", "svg", "--mode-single", "-l", strings.Join(ls, ","), "--page-size-mode", "2",
			"--exclude-drawing-sheet", "--drill-shape-opt", "2", "-o", filepath.Join(raw, name)}
		if o.Side == "bottom" {
			args = append(args, "--mirror")
		}
		msgs, err := runRenderCLI(cli, nil, append(args, pcb)...)
		warns = append(warns, msgs...)
		if err != nil {
			return RenderFile{}, err
		}
		f, b, err := normalizedSVG(filepath.Join(raw, name), name, kind, temp, from, to)
		if err != nil {
			return RenderFile{}, err
		}
		f.Layer = layer
		return f, os.WriteFile(filepath.Join(stage, name), b, 0o644)
	}
	composite := base + "-pcb-" + o.Side + ".svg"
	// One file per plot: a layer whose name flattens onto another layer's or
	// onto the composite ("F.Cu" / "F_Cu", a layer called "top") is refused.
	names := map[string]string{strings.ToLower(composite): "the " + o.Side + " composite"}
	for _, l := range layers {
		key := strings.ToLower(base + "-pcb-" + strings.ReplaceAll(l, ".", "_") + ".svg")
		if other, dup := names[key]; dup {
			return nil, nil, renderErr("RENDER_INCOMPLETE", "list each layer once, by its KiCad name (F.Cu, not F_Cu)",
				"layer %q and %s would both plot to %s", l, other, key)
		}
		names[key] = fmt.Sprintf("layer %q", l)
	}
	var files []RenderFile
	f, err := plot(composite, "pcb-composite", "", layers)
	if err != nil {
		return nil, warns, err
	}
	files = append(files, f)
	for _, l := range layers {
		ls := []string{l}
		if l != "Edge.Cuts" {
			ls = append(ls, "Edge.Cuts") // the outline gives each layer its place
		}
		f, err := plot(base+"-pcb-"+strings.ReplaceAll(l, ".", "_")+".svg", "pcb-layer", l, ls)
		if err != nil {
			return nil, warns, err
		}
		files = append(files, f)
	}
	for _, f := range files {
		if !renderNameRE.MatchString(f.File) {
			return nil, warns, renderErr("RENDER_BAD_INPUT", "rename the board file to letters, digits and -_.", "output name %q is not one kicad render can manage", f.File)
		}
	}
	if !o.PNG {
		return files, warns, nil
	}
	dpi := o.DPI
	if dpi <= 0 {
		dpi = 150
	}
	wmm, hmm := svgSizeMM(filepath.Join(stage, composite))
	w, h := renderPixels(wmm, dpi), renderPixels(hmm, dpi)
	if w == 0 || h == 0 {
		w, h = 1600, 900
		warns = append(warns, "png: board size not readable from the SVG; 1600x900")
	}
	name := base + "-3d-" + o.Side + ".png"
	out := filepath.Join(stage, name)
	// A missing optional 3D model only blanks a part of the preview: that
	// message alone is listed, not blocking; every other one still blocks.
	msgs, err := runRenderCLI(cli, renderModelMsgRE, "pcb", "render", "--side", o.Side, "-w", strconv.Itoa(w), "-h", strconv.Itoa(h),
		"--quality", "basic", "-o", out, pcb)
	warns = append(warns, msgs...)
	if err != nil {
		return nil, warns, err
	}
	b, err := os.ReadFile(out)
	if err != nil || len(b) == 0 {
		return nil, warns, renderErr("RENDER_KICAD_FAILED", "check kicad-cli pcb render on this board", "no PNG written: %v", err)
	}
	// kicad-cli 10 trims a few pixels off the requested size (measured:
	// 640x400 -> 616x368, 1600x900 -> 1568x872), so the PNG must decode,
	// be non-empty and fall within a small margin below the request. The
	// whole image is decoded: a valid header over truncated data is no PNG.
	var pw, ph int
	img, err := png.Decode(bytes.NewReader(b))
	if err == nil {
		pw, ph = img.Bounds().Dx(), img.Bounds().Dy()
	}
	near := func(got, want int) bool { return got > 0 && got <= want && want-got <= 48 }
	if err != nil || !near(pw, w) || !near(ph, h) {
		return nil, warns, renderErr("RENDER_BAD_PNG", "check kicad-cli pcb render on this board",
			"%s: want a PNG of about %dx%d, got %dx%d (%v)", name, w, h, pw, ph, err)
	}
	sum := sha256.Sum256(b)
	files = append(files, RenderFile{File: name, Kind: "pcb-3d", SHA256: hex.EncodeToString(sum[:]), Bytes: len(b), Width: pw, Height: ph})
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

// RenderVerdict is the gate status of a render error: a request error has
// none (the caller exits 1); everything else could not be checked.
func RenderVerdict(err error) gate.Status {
	var re *RenderError
	if errors.As(err, &re) && re.Usage() {
		return ""
	}
	return gate.CannotCheck
}
