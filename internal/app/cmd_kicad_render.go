package app

// cmd_kicad_render.go — `pcbpilot kicad render`: deterministic SVG previews of
// a KiCad schematic or board (kicad.Render) and a structural compare against
// an earlier render (kicad.CompareRenders) for visual regression.

import (
	"encoding/json"
	"errors"
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
sha256, element counts). A re-render into a directory with render.json replaces
exactly the files it lists; any other non-empty directory is refused.

--compare BASELINE_DIR diffs the SVGs of --out against an earlier render (after
rendering, when --sch/--pcb is given; else the existing --out). Per file:
identical (same normalised bytes), changed (elementsAdded / elementsRemoved /
changedElements from a structural SVG diff: each drawn element with its sorted
attributes, text and enclosing group style, compared as a multiset; a pure
reorder is "reordered"), added, removed, unreadable. verdict: pass = all
identical, fail = anything differs, cannot-check = a side has no SVG or a file
does not parse. Exit 0 pass, 6 fail, 5 cannot-check; 1 when rendering fails.

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
				return errQuiet
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
				rep.Render, rep.Warnings = m, m.Warnings
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
	f.StringVar(&report, "report", "", "also write the JSON report to this file")
	return c
}

func sameDir(a, b string) bool {
	x, err1 := filepath.Abs(a)
	y, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && strings.EqualFold(filepath.Clean(x), filepath.Clean(y))
}

func writeRenderReport(stdout io.Writer, path string, rep kicadRenderReport) error {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if path != "" {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			return err
		}
	}
	_, err = stdout.Write(b)
	return err
}
