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
	var pcb, sch, out, overrides, contract, waiverPath string
	var noAssembly, verbose, preflight bool
	var step, stepBoardOnly, stepNoDNP, stepSubst, ipc356 bool
	c := &cobra.Command{
		Use:   "fab",
		Short: "Export JLCPCB manufacturing files (Gerber+drill zip, BOM, CPL; optional STEP, IPC-D-356) with kicad-cli",
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
  OUT/<board>.step       --step: STEP 3D model of the board (not in the zip)
  OUT/<board>.d356       --ipc356: IPC-D-356A netlist for flying-probe test (not in the zip)

--step and --ipc356 add outputs to the same run (same staging, manifest and
rollback); without them the output is unchanged. A STEP that kicad-cli wrote
empty or truncated, or an IPC-D-356 without test records, fails the run and
nothing is published. fab-report.json gets "step" (sha256, args, footprints
without a 3D model: no (model …) entry or only hidden ones) and "ipc356"
(sha256, test-record count). --step-board-only exports the bare board body
(the footprint model check is then reported as skipped); --step-no-dnp leaves
out DNP parts; --step-subst-models lets KiCad swap same-name STEP models for
VRML ones (off by default: it can hide a missing model). For STEP without the
LCSC gate use --no-assembly.

Every file is generated into a fresh staging directory and the zip holds only
that run's output. A rerun replaces the files the previous run recorded in
OUT/.pcbpilot-fab-manifest.json (so Gerbers of layers the board no longer has
are removed) and leaves every other file in OUT alone; Gerber-looking files
it does not own are reported, never zipped. A file this run would write that
exists but is not in the manifest (a user's file, or an output of a pcbpilot
without manifests) stops the run before anything is touched; a symlinked or
junctioned OUT or OUT/gerber is refused. If installing fails part-way, the
previous run's files are put back. Runs into one OUT are serialized by
OUT/.pcbpilot-fab.lock: a second concurrent run is refused.

The BOM and CPL must list the same designators, each exactly once: a part in
one but not the other, a duplicate or an empty designator fails the run
(codes CPL_NOT_IN_BOM, BOM_NOT_IN_CPL, CPL_DUPLICATE_REF, BOM_DUPLICATE_REF,
CPL_EMPTY_REF, each with a fix) and nothing is written.

LCSC numbers are read from the fields "LCSC Part #", "LCSC", "LCSC Part",
"JLCPCB Part #" on the board footprints and, with --sch, the schematic symbols
(schematic wins). Every placed part that is not DNP, not excluded from BOM and
not excluded from position files must have one (C + digits): otherwise the
command lists them and exits non-zero without writing anything. Fill them with
'pcbpilot kicad lcsc --set', or use --no-assembly for a bare-board order.

CPL rotation caveat: ` + kicad.RotationNote + `
--preflight checks instead of exporting and writes nothing (not to --out, not to the
project): DRC on a copy (kicad drc: kicad-drc, kicad-drc-policy, schematic-parity
against the project root <project>.kicad_sch; a --sch must be a sheet of that
hierarchy: schematic-root), board-outline (Edge.Cuts closed, from KiCad's outline test), contract (--contract
order file, see the pcbpilot-fab skill), layer-count, min-track / min-space / min-drill /
min-annular (the smallest value the effective rules allow — .kicad_pro constraints and
netclass clearances, .kicad_dru custom rules, local clearance overrides — vs the
contract limits), finish, assembly-sides (CPL sides vs contract), lcsc (coverage) and
bom-cpl (the same designator checks as an export). Every check is pass / fail /
cannot-check; one verdict and the JSON report on stdout; exit 0 pass, 6 fail, 5 not
verified (a missing contract is cannot-check: no contract, no "ready to order").

Override file (JSON), designator entries win over footprint entries:
  {"refs": {"U3": {"rotate": 180}},
   "footprints": {"Package_TO_SOT_SMD:SOT-23": {"rotate": 180}, "SOT-223-3_TabPin2": {"rotate": -90, "dx": 0, "dy": 0}}}`,
		Example: `  pcbpilot kicad fab --pcb board.kicad_pcb --sch board.kicad_sch --out fab/
  pcbpilot kicad fab --pcb board.kicad_pcb --out fab/ --cpl-overrides jlc-rot.json
  pcbpilot kicad fab --pcb board.kicad_pcb --out fab/ --no-assembly
  pcbpilot kicad fab --pcb board.kicad_pcb --out fab/ --no-assembly --step --ipc356
  pcbpilot kicad fab --preflight --pcb board.kicad_pcb --sch board.kicad_sch --contract fab/order-contract.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if preflight {
				if step || ipc356 || stepBoardOnly || stepNoDNP || stepSubst {
					return fmt.Errorf("--step and --ipc356 write files; --preflight writes nothing")
				}
				if pcb == "" {
					return fmt.Errorf("--pcb is required")
				}
				ws, err := loadWaivers(waiverPath)
				if err != nil {
					return err
				}
				cli, err := kicad.ResolveFabCLI()
				if err != nil {
					return err
				}
				rep := runFabPreflight(fabPreflightOptions{pcb: pcb, sch: sch, contract: contract, overrides: overrides,
					cli: cli, noAssembly: noAssembly, waivers: ws})
				if out != "" {
					rep.Notes = append(rep.Notes, "--preflight wrote nothing to "+out)
				}
				if err := writeJSON(stdout, rep); err != nil {
					return err
				}
				printDRCSummary(stderr, "kicad fab --preflight", rep.Verdict, rep.Checks, nil)
				if code := rep.Verdict.ExitCode(); code != 0 {
					return exitCodeError{code}
				}
				return nil
			}
			if contract != "" || waiverPath != "" {
				return fmt.Errorf("--contract and --waivers are --preflight flags")
			}
			if !step && (stepBoardOnly || stepNoDNP || stepSubst) {
				return fmt.Errorf("--step-board-only, --step-no-dnp and --step-subst-models need --step")
			}
			if stepBoardOnly && stepNoDNP {
				return fmt.Errorf("--step-no-dnp has no effect with --step-board-only (no component models are exported)")
			}
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
				Step: step, StepBoardOnly: stepBoardOnly, StepNoDNP: stepNoDNP, StepSubst: stepSubst, IPC356: ipc356,
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
			if errors.Is(err, kicad.ErrBOMCPLMismatch) {
				printFabProblems(stderr, rep.Problems)
				return err
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "layers: %s\n", strings.Join(rep.Layers, ", "))
			fmt.Fprintf(stdout, "gerber zip: %s (%d files)\n", rep.Zip, len(rep.ZipFiles))
			if rep.Step != nil {
				fmt.Fprintf(stdout, "step: %s (%d bytes, sha256 %s)\n", rep.Step.File, rep.Step.Bytes, rep.Step.SHA256)
				if n := len(rep.Step.FootprintsNoModel); n > 0 {
					fmt.Fprintf(stdout, "step: %d footprint(s) without a 3D model:", n)
					for _, m := range rep.Step.FootprintsNoModel {
						fmt.Fprintf(stdout, " %s", m.Ref)
					}
					fmt.Fprintln(stdout)
				} else if rep.Step.BoardOnly {
					fmt.Fprintln(stdout, "step: board only; footprint model check skipped")
				}
			}
			if rep.IPC356 != nil {
				fmt.Fprintf(stdout, "ipc-d-356: %s (%d test records, sha256 %s)\n", rep.IPC356.File, rep.IPC356.Records, rep.IPC356.SHA256)
			}
			if rep.BOM != "" {
				fmt.Fprintf(stdout, "bom: %s (%d lines)\ncpl: %s (%d parts)\n", rep.BOM, rep.BOMLines, rep.CPL, rep.CPLParts)
				if len(rep.CPLOmitted) > 0 {
					fmt.Fprintf(stdout, "cpl: left out %d part(s) not assembled (DNP / excluded from BOM): %s\n", len(rep.CPLOmitted), strings.Join(rep.CPLOmitted, ", "))
				}
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
	f.BoolVar(&step, "step", false, "also export a STEP 3D model of the board")
	f.BoolVar(&stepBoardOnly, "step-board-only", false, "--step: bare board body, no component models")
	f.BoolVar(&stepNoDNP, "step-no-dnp", false, "--step: leave out the models of DNP parts")
	f.BoolVar(&stepSubst, "step-subst-models", false, "--step: use same-name STEP models in place of VRML ones")
	f.BoolVar(&ipc356, "ipc356", false, "also export an IPC-D-356A netlist (flying-probe test)")
	f.BoolVar(&verbose, "verbose", false, "show kicad-cli output")
	f.BoolVar(&preflight, "preflight", false, "read-only: DRC + fab checks against --contract, one verdict, no outputs written")
	f.StringVar(&contract, "contract", "", "--preflight: order contract JSON (vendor, layers, finish, assembly.sides, limits, sources)")
	f.StringVar(&waiverPath, "waivers", "", "--preflight: signed waiver file for the DRC checks (JSON list of {gate, match, reason, by})")
	return c
}

func printFabProblems(w io.Writer, probs []kicad.FabProblem) {
	fmt.Fprintf(w, "%d BOM/CPL consistency problem(s):\n", len(probs))
	for _, p := range probs {
		fmt.Fprintf(w, "  %-18s %s\n  %-18s fix: %s\n", p.Code, p.Message, "", p.Fix)
	}
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
