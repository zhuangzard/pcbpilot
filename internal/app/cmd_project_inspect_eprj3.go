package app

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/eprj3"
)

// newProjectInspectEprj3Cmd is the offline, read-only `.eprj3` reader. It
// needs no daemon, connector or editor and never writes the input.
func newProjectInspectEprj3Cmd(stdout io.Writer) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "inspect-eprj3 <project-dir|index.eprj3|document-file>",
		Short: "Read an EasyEDA Pro V4 local .eprj3 project offline (inventory, counts, units, version markers, schema findings)",
		Long: `Offline, read-only parser for EasyEDA Pro V4 folder projects (.eprj3 index plus
.esch2 / .epcb2 / .epan2 / .ecfg / .evar documents). No daemon, connector or editor;
the input is never modified.

Reports:
  - project / document inventory (index profile ↔ files, uuid cross-check);
  - per-document live counts (eventual-consistency winners, deletions excluded):
    components, wires, nets, tracks (copper LINE/ARC), vias, pours;
  - documented units and axis conventions (schematic 10 mil Y-up, PCB mil) with
    the file's own CANVAS unit and yAxisDirection markers;
  - version markers (DOCHEAD editVersion, V3/V4 evidence);
  - schema findings against the official easyeda-format-skill JSON Schemas
    (vendored subset, MIT). Schema findings are advisory: editor-written files
    deviate from the generator schemas. Structural problems are findings.

Exit status is non-zero only when an error-level finding exists (malformed line,
unreadable index, empty document). Warnings do not fail the command.

This is a reading tool. It is not a write path: generating or editing .eprj3
files and letting the editor reload them is not an accepted pcbpilot workflow.`,
		Args: cobra.ExactArgs(1),
		Example: `  pcbpilot project inspect-eprj3 ~/Documents/EasyEDA-Pro/projects/MyBoard
  pcbpilot project inspect-eprj3 MyBoard/MyBoard.eprj3 --json
  pcbpilot project inspect-eprj3 MyBoard/pcb/PCB1.epcb2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := eprj3.Inspect(args[0])
			if err != nil {
				return err
			}
			if asJSON {
				if err := writeJSON(stdout, r); err != nil {
					return err
				}
			} else {
				printEprj3Report(stdout, r)
			}
			if !r.OK() {
				return errQuiet
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit the full report as JSON")
	return c
}

func printEprj3Report(w io.Writer, r *eprj3.Report) {
	fmt.Fprintf(w, "input: %s (%s)\n", r.Input, r.Mode)
	if inv := r.Inventory; inv != nil {
		fmt.Fprintf(w, "project: %s  format=%s\n", inv.Name, inv.Format)
		fmt.Fprintf(w, "  boards=%s  schematics=%s\n", eprj3List(inv.Boards), eprj3List(inv.Schematics))
		fmt.Fprintf(w, "  sheets=%s  pcbs=%s  panels=%s\n", eprj3List(inv.Sheets), eprj3List(inv.PCBs), eprj3List(inv.Panels))
	}
	fmt.Fprintln(w, "documents:")
	for _, f := range r.Files {
		if f.Main == nil {
			fmt.Fprintf(w, "  %-34s %s (no main document)\n", f.Path, f.Kind)
			continue
		}
		m := f.Main
		line := fmt.Sprintf("  %-34s %-9s %q records=%d live=%d deleted=%d superseded=%d", f.Path, m.DocType, m.Title, m.Records, m.LiveRecords, m.Deleted, m.Superseded)
		if c := m.Counts; c != nil {
			line += fmt.Sprintf("\n      components=%d wires=%d nets=%d tracks=%d vias=%d pours=%d", c.Components, c.Wires, c.Nets, c.Tracks, c.Vias, c.Pours)
		}
		if co := m.Coordinates; co != nil {
			line += fmt.Sprintf("\n      unit=%s axis=%s", co.Unit, co.Axis)
			if co.CanvasUnit != "" {
				line += " canvas=" + co.CanvasUnit
			}
			if len(co.YAxisDirection) > 0 {
				line += " yAxisDirection=" + strings.Join(co.YAxisDirection, ",")
			}
		}
		if len(f.LibraryDocTypes) > 0 {
			parts := []string{}
			for _, k := range eprj3SortedKeys(f.LibraryDocTypes) {
				parts = append(parts, fmt.Sprintf("%s×%d", k, f.LibraryDocTypes[k]))
			}
			line += "\n      embedded library docs: " + strings.Join(parts, " ")
		}
		fmt.Fprintln(w, line)
	}
	t := r.Totals
	fmt.Fprintf(w, "totals: components=%d wires=%d nets=%d tracks=%d vias=%d pours=%d\n", t.Components, t.Wires, t.Nets, t.Tracks, t.Vias, t.Pours)
	fmt.Fprintf(w, "version: generation=%s editVersion=%s\n", r.VersionMarkers.Generation, eprj3List(r.VersionMarkers.EditVersions))
	for _, e := range r.VersionMarkers.Evidence {
		fmt.Fprintf(w, "  - %s\n", e)
	}
	s := r.Schema
	fmt.Fprintf(w, "schema (advisory, %s): validated=%d violations=%d documentedDeviations=%d\n", s.Source, s.Validated, s.Violations, s.DocumentedDeviation)
	for i, f := range s.Findings {
		if i == 10 {
			fmt.Fprintf(w, "  … %d more (use --json)\n", len(s.Findings)-10)
			break
		}
		fmt.Fprintf(w, "  %4d× %s %s %s: %s (first %s:%d)\n", f.Count, f.DocType, f.RecordType, f.Path, f.Message, f.FirstFile, f.FirstLine)
	}
	fmt.Fprintf(w, "findings: %d error(s), %d warning(s)\n", r.Errors, r.Warnings)
	for _, f := range r.Findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(w, "  [%s] %s %s %s\n", f.Severity, f.Code, loc, f.Message)
	}
}

func eprj3List(xs []string) string {
	if len(xs) == 0 {
		return "-"
	}
	return strings.Join(xs, ",")
}

func eprj3SortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
