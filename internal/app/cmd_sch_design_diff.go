package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

func newSchDesignDiffCmd(stdout, stderr io.Writer) *cobra.Command {
	var exitCode bool
	var playbookPath, beforePath string
	c := &cobra.Command{
		Use:   "design-diff <before.json> <after.json>",
		Short: "Compare local schematic identity, connectivity, geometry and complete compose plans",
		Long: `Compare strict 1.4 canonical JSON or the connectivity in a compose source/plan.
Two complete compose plans also compare sheet, keepouts, placements, wires, flags,
frames, title styling and modeled occupancy. Plans are local intent, not proof of
actual EDA drawing. A plan versus canonical readback compares canonical data only.
Omitted readback modules and netlist-only evidence for a requested drawing kind
are unverified/incomplete, not module deletion or proof of a different marker.

With --playbook, the first input is the BEFORE baseline plan and the second is
the AFTER desired plan. --before requires a fresh target sch list snapshot with
device identity, bbox, pins and wires. Only existing owned frame/title changes
can compile to Apply: no frame additions/deletions/reordering and no electrical,
component, wire, marker or planning-diagnostic changes. Execution verifies the
baseline circuit and owned frames before writes, changes only differing frames,
then checks the desired result and saves. Equal plans compile read-only checks.

Stable component/pin/net IDs determine matches; inventory ordering is ignored.
Module reading order and wire path shape remain meaningful; reversing an entire
wire path is equivalent. Derived issues and runtime primitiveId are ignored. Missing coordinate evidence remains unverified, never an observed zero.
Single-page documentId supplies an omitted component pageId. Numbers use nine
decimal places for both comparison and hashing, not drawing-grid snapping.
Hashes cover normalized content within coverage.scope; only equal covered data
is "synced". Missing drawing evidence is always reported, even if canonical data
matches. Use fresh EDA readback and frame check/export-image to verify the editor.

Exit codes: 0 comparison completed; 2 different with --exit-code; 3 wrong target
or incomplete canonical evidence; 1 malformed input or operational error.`,
		Example: "  pcbpilot sch design-diff target-plan.json observed-connectivity.json --exit-code\n  pcbpilot sch design-diff before-plan.json after-plan.json --before fresh.json --playbook frame-diff-apply.json",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (playbookPath == "") != (beforePath == "") {
				return fmt.Errorf("--playbook and fresh --before must be supplied together")
			}
			var inputs [2]schDesignInput
			var rawInputs [2][]byte
			for i, path := range args {
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				inputs[i], err = decodeSchDesignInput(raw)
				rawInputs[i] = raw
				if err != nil {
					var incomplete *connectivity.IncompleteDesignError
					if errors.As(err, &incomplete) {
						fmt.Fprintf(stderr, "%s: %v\n", path, err)
						return exitCodeError{3}
					}
					return fmt.Errorf("read %s: %w", path, err)
				}
			}
			diff, err := compareSchDesignInputs(inputs[0], inputs[1])
			if err != nil {
				return err
			}
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			if err = encoder.Encode(diff); err != nil {
				return err
			}
			if diff.Status == "wrong-target" || diff.Status == "incomplete" {
				return exitCodeError{3}
			}
			if playbookPath != "" {
				if err := writeSchDesignDiffPlaybook(rawInputs, args, beforePath, playbookPath); err != nil {
					return err
				}
			}
			if exitCode && diff.Status == "different" {
				return exitCodeError{2}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&exitCode, "exit-code", false, "exit 2 when compared design states differ (incomplete evidence always exits 3)")
	c.Flags().StringVar(&playbookPath, "playbook", "", "compile only owned frame/title changes from first BEFORE plan to second AFTER plan")
	c.Flags().StringVar(&beforePath, "before", "", "fresh target sch list snapshot with --include-device-identity --include-bbox --include-pins --include-wires")
	return c
}
