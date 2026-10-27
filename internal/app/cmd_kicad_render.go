package app

// cmd_kicad_render.go — `pcbpilot kicad render`: deterministic SVG previews of
// a KiCad schematic or board (kicad.Render) and a structural compare against
// an earlier render (kicad.CompareRenders) for visual regression.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

type kicadRenderReport struct {
	OK       bool                  `json:"ok"`
	Out      string                `json:"out"`
	Render   *kicad.RenderManifest `json:"render,omitempty"`
	Compare  *kicad.RenderCompare  `json:"compare,omitempty"`
	Verdict  gate.Status           `json:"verdict"`
	Error    *kicad.RenderError    `json:"error,omitempty"`
	Warnings []string              `json:"warnings,omitempty"`
}

func newKicadRenderCmd(stdout io.Writer) *cobra.Command {
	var o kicad.RenderOptions
	var compare, report string
	c := &cobra.Command{
		Use:   "render",
		Short: "Deterministic SVG previews of a KiCad schematic / board, and a structural compare against a baseline render",
		Long: `Plots a temp copy of the project with kicad-cli (the input is never touched) under
pcbpilot's isolated KiCad config (empty global library tables, default theme), then
normalises every SVG so the same input gives byte-identical files: the plot date and
temp paths KiCad embeds are removed; paint order is kept.

  --sch ROOT.kicad_sch   one SVG per page of the hierarchy (<base>.svg, <base>-<sheet>.svg)
  --pcb BOARD.kicad_pcb  <base>-pcb-<side>.svg (all --layers, board area) plus one
                         <base>-pcb-<Layer>.svg per layer with Edge.Cuts; --side bottom mirrors.
                         Default layers: F.Cu,F.Mask,F.Silkscreen,Edge.Cuts (B.* for bottom).
  --png                  PCB only: also <base>-3d-<side>.png from kicad-cli pcb render, sized
                         by --dpi (default 150) over the board area. It is a raytraced
                         preview, not bit-reproducible, so --compare lists it but never
                         compares it. Schematics have no PNG: kicad-cli cannot rasterise
                         them and pcbpilot ships no SVG rasterizer.

--out DIR (default ./pcbpilot-render) gets the files plus render.json (file list,
sha256, element counts). The new render is assembled aside and replaces the files
the old render.json lists only once complete. A directory holding anything else
(other files, subdirectories, links, a render.json naming other paths) is refused.

The temp copy holds the project directory's KiCad files plus every sheet,
library and drawing sheet reached by a relative or ${KIPRJMOD} path (at the
same relative place, so nothing is rewritten); libraries named by an absolute
path or a KiCad variable are read in place (listed as readInPlace).

--compare BASELINE_DIR diffs the SVGs of --out against an earlier render (after
rendering, when --sch/--pcb is given; else the existing --out). Per file:
identical (same normalised bytes), changed (elementsAdded / elementsRemoved /
changedElements from a structural SVG diff: each drawn element with its sorted
attributes, text and enclosing group style, compared as a multiset; a pure
reorder is "reordered"), added, removed, unreadable. verdict: pass = all
identical, fail = anything differs, cannot-check = a side has no SVG or a file
does not parse, draws nothing, or is listed in render.json but missing.
Exit 0 pass, 6 fail, 5 cannot-check; 1 for a bad request (flags, unusable --out).

A render is cannot-check (exit 5, the previous render in --out left untouched)
when a sheet / library / drawing sheet the project references cannot be
resolved (absolute sheet paths, undefined path variables, missing files),
when kicad-cli reports something it could not load, or when any page of the
hierarchy is missing or not a drawing. kicad-cli's other messages go to
render.json "warnings".

A changed render shows that the drawing moved, not whether the design is right:
connectivity is judged from netlists (kicad netlist / sch-read), not pictures.`,
		Example: `  pcbpilot kicad render --sch board.kicad_sch --out review/render-v1
  pcbpilot kicad render --pcb board.kicad_pcb --side bottom --png --out review/pcb-bottom
  pcbpilot kicad render --sch board.kicad_sch --out review/render-v2 --compare review/render-v1
  pcbpilot kicad render --out build/render --compare old/render   # compare only`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.OutDir == "" {
				o.OutDir = "pcbpilot-render"
			}
			rep := kicadRenderReport{Out: o.OutDir, Verdict: gate.Pass}
			fail := func(err error) error {
				var re *kicad.RenderError
				if !errors.As(err, &re) {
					re = &kicad.RenderError{Code: "RENDER_FAILED", Detail: err.Error()}
				}
				rep.OK, rep.Error, rep.Verdict = false, re, gate.CannotCheck
				_ = writeRenderReport(stdout, report, rep)
				if kicad.RenderVerdict(re) == gate.CannotCheck {
					return exitCodeError{gate.CannotCheck.ExitCode()}
				}
				return errQuiet
			}
			// --report is checked before anything (even an error report) is
			// written: it must not land on an input, a dependency, or in
			// --out / --compare.
			if report != "" {
				var files, dirs []string
				complete := true
				for _, in := range []string{o.Sch, o.PCB} {
					if in == "" {
						continue
					}
					abs, _ := filepath.Abs(in)
					files, dirs = append(files, abs), append(dirs, filepath.Dir(abs))
					f, d, ok, err := kicad.RenderDependencies(in)
					files, dirs = append(files, f...), append(dirs, d...)
					complete = complete && ok && err == nil
				}
				dirs = append(dirs, o.OutDir)
				if compare != "" {
					dirs = append(dirs, compare)
				}
				if err := checkReportPath(report, files, dirs); err != nil {
					report = ""
					return fail(err)
				}
				if !complete {
					// With an incomplete closure the protected set is
					// incomplete too: the report goes to stdout only.
					rep.Warnings = append(rep.Warnings, "report: not written to "+report+" (the project's dependencies could not all be resolved); stdout only")
					report = ""
				}
			}
			if o.Sch == "" && o.PCB == "" && compare == "" {
				return fail(&kicad.RenderError{Code: "RENDER_BAD_INPUT", Detail: "nothing to do", Fix: "pass --sch, --pcb or --compare"})
			}
			if compare != "" && sameDir(compare, o.OutDir) {
				return fail(&kicad.RenderError{Code: "RENDER_BAD_INPUT", Detail: "--compare names the --out directory", Fix: "render into a new --out and compare it with the old one"})
			}
			if o.Sch != "" || o.PCB != "" {
				m, err := kicad.Render(o)
				if err != nil {
					return fail(err)
				}
				rep.Render, rep.Warnings = m, append(rep.Warnings, m.Warnings...)
			}
			rep.OK = true
			if compare != "" {
				rep.Compare = kicad.CompareRenders(compare, o.OutDir)
				rep.Verdict = rep.Compare.Verdict
			}
			if err := writeRenderReport(stdout, report, rep); err != nil {
				return err
			}
			if code := rep.Verdict.ExitCode(); code != 0 {
				return exitCodeError{code}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&o.Sch, "sch", "", "root .kicad_sch: one SVG per page of its hierarchy")
	f.StringVar(&o.PCB, "pcb", "", ".kicad_pcb: composite + per-layer SVGs")
	f.StringSliceVar(&o.Layers, "layers", nil, "PCB layers (untranslated names, comma separated); default by --side")
	f.StringVar(&o.Side, "side", "top", "PCB side: top | bottom (bottom is mirrored)")
	f.StringVar(&o.OutDir, "out", "", "output directory (default ./pcbpilot-render)")
	f.BoolVar(&o.PNG, "png", false, "PCB only: also a 3D raster preview (kicad-cli pcb render; not compared)")
	f.IntVar(&o.DPI, "dpi", 0, "PNG pixels per inch over the board area (default 150, with --png)")
	f.StringVar(&compare, "compare", "", "baseline render directory to diff --out against")
	f.StringVar(&report, "report", "", "also write the JSON report to this file (outside the project, --out and --compare)")
	return c
}

// sameDir: a and b are one directory, also through links / junctions or
// case-insensitive spellings.
func sameDir(a, b string) bool {
	x, err1 := os.Stat(a)
	y, err2 := os.Stat(b)
	if err1 == nil && err2 == nil {
		return os.SameFile(x, y)
	}
	p, err1 := filepath.Abs(a)
	q, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && strings.EqualFold(filepath.Clean(p), filepath.Clean(q))
}

// checkReportPath refuses a --report path that is, or could overwrite, any
// of files (inputs and dependencies) or lies inside any of dirs (project
// and dependency directories, --out, --compare): lexically (case-insensitive)
// and by identity — os.SameFile on every existing ancestor, and an existing
// report path against the files and every file in dirs, so links and hard
// links count.
func checkReportPath(report string, files, dirs []string) error {
	abs, err := filepath.Abs(report)
	if err != nil {
		return err
	}
	refuse := func(what string) error {
		return &kicad.RenderError{Code: "RENDER_BAD_INPUT", Detail: fmt.Sprintf("--report %s would write over %s", report, what),
			Fix: "write the report to a new file outside the project, its dependencies, --out and --compare"}
	}
	existing, statErr := os.Stat(abs)
	if info, err := os.Lstat(abs); err == nil && !info.Mode().IsRegular() {
		return &kicad.RenderError{Code: "RENDER_BAD_INPUT", Detail: "--report " + report + " is not a regular file", Fix: "name a new file"}
	}
	same := func(p string) bool {
		if strings.EqualFold(filepath.Clean(p), abs) {
			return true
		}
		if statErr != nil {
			return false
		}
		st, err := os.Stat(p)
		return err == nil && os.SameFile(st, existing)
	}
	for _, f := range files {
		if same(f) {
			return refuse(f)
		}
	}
	for _, dir := range dirs {
		d, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(strings.ToLower(d), strings.ToLower(abs)); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return refuse(dir)
		}
		dst, err := os.Stat(d)
		if err != nil {
			continue
		}
		for p := filepath.Dir(abs); ; {
			if st, err := os.Stat(p); err == nil && os.SameFile(st, dst) {
				return refuse(dir)
			}
			up := filepath.Dir(p)
			if up == p {
				break
			}
			p = up
		}
		if statErr == nil { // a hard link elsewhere to a file in dir
			hit := ""
			_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
				if err != nil || hit != "" {
					return filepath.SkipDir
				}
				if e.IsDir() {
					return nil
				}
				// Stat follows links: a linked file is compared by the
				// identity of the file it points to.
				if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && same(p) {
					hit = p
				}
				return nil
			})
			if hit != "" {
				return refuse(hit)
			}
		}
	}
	return nil
}

func writeRenderReport(stdout io.Writer, path string, rep kicadRenderReport) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if path != "" {
		// temp + rename: an existing report is replaced, never truncated in
		// place (a hard link to it keeps its old contents).
		tmp, err := os.CreateTemp(filepath.Dir(path), ".render-report-*.tmp")
		if err != nil {
			return err
		}
		_, werr := tmp.Write(b)
		cerr := tmp.Close()
		if werr == nil && cerr == nil {
			werr = os.Rename(tmp.Name(), path)
		}
		if werr != nil || cerr != nil {
			_ = os.Remove(tmp.Name())
			if werr == nil {
				werr = cerr
			}
			return werr
		}
	}
	_, err = stdout.Write(b)
	return err
}
