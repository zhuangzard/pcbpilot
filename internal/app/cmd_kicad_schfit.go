package app

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func newKicadSchFitCmd(stdout, stderr io.Writer) *cobra.Command {
	var sch string
	var dry bool
	c := &cobra.Command{
		Use:   "sch-fit",
		Short: "Size every schematic sheet to its content (smallest A4…A0 that holds it clear of the title block)",
		Long: `For the root .kicad_sch and every hierarchical sub-sheet it references:
measures the content (symbol bodies and pins from their library graphics,
wires, buses, labels, texts, sheets, graphics), picks the smallest landscape
ISO sheet A4, A3, A2, A1, A0 whose drawing area (10 mm border + 5 mm gap)
holds it without entering the bottom-right title block, shrinking as well as
growing, and shifts the content onto the drawing area (1.27 mm grid) when it
lies outside it. A sheet that does not fit even A0 is reported "too big —
split the sheet" and left unchanged (non-zero exit).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			files, err := kicad.SheetFiles(sch)
			if err != nil {
				return err
			}
			type row struct {
				File string `json:"file"`
				kicad.FitResult
			}
			var rows []row
			tooBig := 0
			for _, f := range files {
				src, err := os.ReadFile(f)
				if err != nil {
					return err
				}
				out, r, err := kicad.FitSheet(string(src))
				if err != nil {
					return fmt.Errorf("%s: %w", f, err)
				}
				if r.TooBig {
					tooBig++
					fmt.Fprintf(stderr, "%s: content %.0f×%.0f mm does not fit A0 — split the sheet\n", f, r.Content.W(), r.Content.H())
				} else if r.Changed {
					fmt.Fprintf(stderr, "%s: %s → %s (shift %.2f, %.2f mm)\n", f, r.From, r.To, r.ShiftX, r.ShiftY)
					if !dry {
						if err := os.WriteFile(f, []byte(out), 0o644); err != nil {
							return err
						}
					}
				}
				rows = append(rows, row{f, r})
			}
			if err := writeJSON(stdout, rows); err != nil {
				return err
			}
			if tooBig > 0 {
				return fmt.Errorf("%d sheet(s) too big for A0", tooBig)
			}
			return nil
		},
	}
	c.Flags().StringVar(&sch, "sch", "", "root .kicad_sch (required)")
	c.Flags().BoolVar(&dry, "dry-run", false, "report only, change nothing")
	_ = c.MarkFlagRequired("sch")
	return c
}

// newKicadCmd is the `pcbpilot kicad` parent (KiCad is the design EDA since
// 2026-10-09; EasyEDA only receives the finished project).
func newKicadCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	k := &cobra.Command{Use: "kicad", Short: "Design in KiCad: schematic sheets, placement, routing, gates, fab output"}
	k.AddCommand(newKicadSchFitCmd(stdout, stderr))
	return k
}
