package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newSchZoneReviewCmd(stdout io.Writer) *cobra.Command {
	var from, report string
	c := &cobra.Command{Use: "zone-review", Short: "只读复核源 JSON 的 zone 归属，提示 AI 决定是否拆分", Args: cobra.NoArgs,
		Long: `Review the same measured source JSON as layout-plan --zones, offline.
Reports multiple >=4-pin members, subgraphs detached from the core after excluding
declared local_power/local_ground nets, and rail-only attachments. These are
advisory hints, not functional classification or physical connectivity proof.
AI decides whether to retain ownership or edit the source JSON and rerun planning.
No source edits, splitting, layout solving, daemon calls or Apply.
Warnings exit 0; malformed input/invalid ownership exits nonzero. No findings
does not prove correct semantics. JSON goes to stdout or a distinct --report file.`,
		Example: "  pcbpilot sch zone-review --from zones.json --report review.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" {
				return fmt.Errorf("--from is required")
			}
			if err := schLayoutReportPaths(from, "", report); err != nil {
				return err
			}
			raw, err := os.ReadFile(from)
			if err != nil {
				return err
			}
			review, reviewErr := reviewSchematicZonesJSON(raw)
			encoded, err := json.MarshalIndent(review, "", "  ")
			if err != nil {
				return err
			}
			encoded = append(encoded, '\n')
			if report == "" {
				_, err = stdout.Write(encoded)
			} else {
				err = os.WriteFile(report, encoded, 0644)
			}
			if err != nil {
				return err
			}
			printSchematicZoneReview(cmd.ErrOrStderr(), review)
			return reviewErr
		}}
	c.Flags().StringVar(&from, "from", "", "layout-plan --zones source JSON (read-only)")
	c.Flags().StringVar(&report, "report", "", "separate JSON report; defaults to stdout")
	return c
}

func printSchematicZoneReview(w io.Writer, r *SchematicZoneReview) {
	for _, f := range r.Findings {
		refs := []string{}
		for _, c := range f.Components {
			refs = append(refs, c.Ref+"["+c.ID+"]")
		}
		fmt.Fprintf(w, "WARN zone-review %s zone=%s components=%s: %s %s\n", f.Rule, f.ZoneID, strings.Join(refs, ","), f.Message, f.Suggestion)
	}
	if len(r.Findings) > 0 {
		fmt.Fprintln(w, "zone-review: 请 AI 逐条复核并说明保留或修改源 JSON 的依据；提示不会自动拆区，硬门禁继续执行。")
	}
}
