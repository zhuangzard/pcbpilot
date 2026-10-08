package app

// cmd_kicad_fab.go — `pcbpilot kicad fab`: JLCPCB Gerber/drill zip + BOM + CPL.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func newKiCadFabCmd(stdout, stderr io.Writer) *cobra.Command {
	var pcb, sch, out, overrides string
	var noAssembly, verbose bool
	c := &cobra.Command{
		Use:   "fab",
		Short: "Export JLCPCB manufacturing files (Gerber+drill zip, BOM, CPL) with kicad-cli",
		Args:  cobra.NoArgs,
		Long: `Export the file set JLCPCB asks for from a KiCad board (settings from JLC's
KiCad 9 Gerber/drill and KiCad 10 BOM/CPL help articles):

  OUT/gerber/            Gerbers (Protel ext., X2, netlist attrs, mask subtracted
                         from silk, zones refilled) for every copper layer in
                         stackup order + F/B mask, silk, paste + Edge.Cuts;
                         Excellon drill (mm, decimal, absolute, alternate oval
                         mode, PTH and NPTH separate) + Gerber drill map
  OUT/<board>-gerber.zip Gerbers + .drl only (upload this for the PCB)
  OUT/<board>-bom.csv    Comment, Designator, Footprint, LCSC Part #
  OUT/<board>-cpl.csv    Designator, Mid X, Mid Y, Layer, Rotation (mm)
  OUT/fab-report.json    what was written, warnings, applied overrides

LCSC numbers are read from the fields "LCSC Part #", "LCSC", "LCSC Part",
"JLCPCB Part #" on the board footprints and, with --sch, the schematic symbols
(schematic wins). Every placed part that is not DNP, not excluded from BOM and
not excluded from position files must have one (C + digits): otherwise the
command lists them and exits non-zero without writing anything. Fill them with
'pcbpilot kicad lcsc --set', or use --no-assembly for a bare-board order.

CPL rotation caveat: ` + kicad.RotationNote + `
Override file (JSON), designator entries win over footprint entries:
  {"refs": {"U3": {"rotate": 180}},
   "footprints": {"Package_TO_SOT_SMD:SOT-23": {"rotate": 180}, "SOT-223-3_TabPin2": {"rotate": -90, "dx": 0, "dy": 0}}}`,
		Example: `  pcbpilot kicad fab --pcb board.kicad_pcb --sch board.kicad_sch --out fab/
  pcbpilot kicad fab --pcb board.kicad_pcb --out fab/ --cpl-overrides jlc-rot.json
  pcbpilot kicad fab --pcb board.kicad_pcb --out fab/ --no-assembly`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if pcb == "" || out == "" {
				return fmt.Errorf("--pcb and --out are required")
			}
			cli, err := kicad.ResolveFabCLI()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			var log io.Writer
			if verbose {
				log = stderr
			}
			rep, err := kicad.RunFab(kicad.FabOptions{PCB: pcb, Sch: sch, OutDir: out,
				CPLOverrides: overrides, NoAssembly: noAssembly,
				Tools: kicad.FabTools{CLI: cli}, Log: log})
			if rep != nil {
				for _, w := range rep.Warnings {
					fmt.Fprintln(stderr, "warning:", w)
				}
			}
			if errors.Is(err, kicad.ErrMissingLCSC) {
				printLCSCProblems(stderr, rep.Missing, rep.Invalid)
				fmt.Fprintln(stderr, "fix: pcbpilot kicad lcsc --pcb", pcb, "--set REF=Cxxxx …  (search: pcbpilot kicad lcsc --search \"…\")")
				fmt.Fprintln(stderr, "     or mark the part DNP / exclude from BOM / exclude from position files; --no-assembly for bare boards")
				return err
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "layers: %s\n", strings.Join(rep.Layers, ", "))
			fmt.Fprintf(stdout, "gerber zip: %s (%d files)\n", rep.Zip, len(rep.ZipFiles))
			if rep.BOM != "" {
				fmt.Fprintf(stdout, "bom: %s (%d lines)\ncpl: %s (%d parts)\n", rep.BOM, rep.BOMLines, rep.CPL, rep.CPLParts)
				for _, a := range rep.Overrides {
					fmt.Fprintln(stdout, "cpl override:", a)
				}
				fmt.Fprintln(stdout, "note:", kicad.RotationNote)
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&pcb, "pcb", "", "board file (.kicad_pcb, required)")
	f.StringVar(&sch, "sch", "", "root schematic (.kicad_sch); its LCSC/DNP/BOM fields take precedence")
	f.StringVar(&out, "out", "", "output directory (required)")
	f.StringVar(&overrides, "cpl-overrides", "", "JSON file of CPL rotation/offset corrections (see help)")
	f.BoolVar(&noAssembly, "no-assembly", false, "bare board: Gerber/drill zip only, no BOM/CPL and no LCSC check")
	f.BoolVar(&verbose, "verbose", false, "show kicad-cli output")
	return c
}

func printLCSCProblems(w io.Writer, missing, invalid []kicad.MissingLCSC) {
	if len(missing) > 0 {
		fmt.Fprintf(w, "%d placed part(s) without an LCSC part number:\n", len(missing))
		for _, m := range missing {
			fmt.Fprintf(w, "  %-8s %-20s %s\n", m.Ref, m.Value, m.Footprint)
		}
	}
	if len(invalid) > 0 {
		fmt.Fprintf(w, "%d part(s) with an invalid LCSC value (want C + digits):\n", len(invalid))
		for _, m := range invalid {
			fmt.Fprintf(w, "  %-8s %-20s %-14q %s\n", m.Ref, m.Value, m.LCSC, m.Footprint)
		}
	}
}
