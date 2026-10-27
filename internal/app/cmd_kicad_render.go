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

--report NAME (a plain file name such as report.json, no directory) writes this
JSON report inside --out as part of the same staged publish, listed in
render.json as kind "report" and ignored by --compare. It needs --out and a
render (--sch / --pcb); otherwise, and whenever the render fails, the report goes
to stdout only.

A changed render shows that the drawing moved, not whether the design is right:
connectivity is judged from netlists (kicad netlist / sch-read), not pictures.`,
		Example: `  pcbpilot kicad render --sch board.kicad_sch --out review/render-v1
  pcbpilot kicad render --pcb board.kicad_pcb --side bottom --png --out review/pcb-bottom
  pcbpilot kicad render --sch board.kicad_sch --out review/render-v2 --compare review/render-v1
  pcbpilot kicad render --out build/render --compare old/render   # compare only`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			outGiven := o.OutDir != ""
			if !outGiven {
				o.OutDir = "pcbpilot-render"
			}
			rep := kicadRenderReport{Out: o.OutDir, Verdict: gate.Pass}
			fail := func(err error) error {
				var re *kicad.RenderError
				if !errors.As(err, &re) {
					re = &kicad.RenderError{Code: "RENDER_FAILED", Detail: err.Error()}
				}
				rep.OK, rep.Error, rep.Verdict = false, re, gate.CannotCheck
				if report != "" {
					rep.Warnings = append(rep.Warnings, "report: "+report+" not written (no render was published); stdout only")
				}
				_ = writeRenderReport(stdout, rep)
				if kicad.RenderVerdict(re) == gate.CannotCheck {
					return exitCodeError{gate.CannotCheck.ExitCode()}
				}
				return errQuiet
			}
			if report != "" && !kicad.ValidRenderReportName(report) {
				bad := report
				report = ""
				return fail(&kicad.RenderError{Code: "RENDER_BAD_INPUT", Detail: fmt.Sprintf("--report %q is not a plain file name", bad),
					Fix: "pass a file name such as report.json; it is written inside --out"})
			}
			if o.Sch == "" && o.PCB == "" && compare == "" {
				return fail(&kicad.RenderError{Code: "RENDER_BAD_INPUT", Detail: "nothing to do", Fix: "pass --sch, --pcb or --compare"})
			}
			if compare != "" && sameDir(compare, o.OutDir) {
				return fail(&kicad.RenderError{Code: "RENDER_BAD_INPUT", Detail: "--compare names the --out directory", Fix: "render into a new --out and compare it with the old one"})
			}
			if report != "" && (!outGiven || (o.Sch == "" && o.PCB == "")) {
				rep.Warnings = append(rep.Warnings, "report: "+report+" is written only with --out and a render (--sch / --pcb); stdout only")
				report = ""
			}
			// finish completes the report once the render is in place (staged
			// or published); compare reads current, shown as --out.
			finish := func(m *kicad.RenderManifest, current string) {
				rep.OK = true
				if m != nil {
					rep.Render, rep.Warnings = m, append(rep.Warnings, m.Warnings...)
				}
				if compare != "" {
					rep.Compare = kicad.CompareRenders(compare, current)
					rep.Compare.Current = o.OutDir
					rep.Verdict = rep.Compare.Verdict
				}
			}
			if o.Sch != "" || o.PCB != "" {
				if report != "" {
					// The report travels in the staged publish: built from the
					// staged render, listed in render.json, never written alone.
					o.ReportName = report
					o.Report = func(m *kicad.RenderManifest, staged string) ([]byte, error) {
						finish(m, staged)
						return renderReportJSON(rep)
					}
				}
				m, err := kicad.Render(o)
				if err != nil {
					rep.Render, rep.Compare, rep.Verdict = nil, nil, gate.CannotCheck
					return fail(err)
				}
				if report == "" {
					finish(m, o.OutDir)
				}
			} else {
				finish(nil, o.OutDir)
			}
			if err := writeRenderReport(stdout, rep); err != nil {
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
	f.StringVar(&report, "report", "", "also publish the JSON report as this plain file name inside --out (listed in render.json)")
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

func renderReportJSON(rep kicadRenderReport) ([]byte, error) {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func writeRenderReport(stdout io.Writer, rep kicadRenderReport) error {
	b, err := renderReportJSON(rep)
	if err != nil {
		return err
	}
	_, err = stdout.Write(b)
	return err
}
