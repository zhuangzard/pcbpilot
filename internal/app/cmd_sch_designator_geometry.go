package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const schDesignatorBBoxSource = "eda.sch_PrimitiveAttribute.getAll(parentId)+eda.sch_Primitive.getPrimitivesBBox([attributeId])"

type schDesignatorGeometryExport struct {
	SchemaVersion int                     `json:"schemaVersion"`
	ProjectID     string                  `json:"projectId,omitempty"`
	DocumentID    string                  `json:"documentId"`
	Source        string                  `json:"source"`
	Count         int                     `json:"count"`
	Designators   []schDesignatorGeometry `json:"designators"`
}

// Validate both the connector's complete per-parent read and its serialized
// response before letting a planner treat the text bboxes as source evidence.
func parseSchDesignatorGeometryExport(value map[string]any, documentID string) (*schDesignatorGeometryExport, error) {
	actualDoc, _ := value["documentId"].(string)
	if actualDoc == "" || actualDoc != documentID {
		return nil, fmt.Errorf("Designator measurement page mismatch: expected %q, got %q", documentID, actualDoc)
	}
	rows, err := parseSchDesignatorGeometry(value)
	if err != nil {
		return nil, err
	}
	count, ok := value["count"].(float64)
	if !ok || count != float64(len(rows)) {
		return nil, fmt.Errorf("Designator measurement count missing or inconsistent with inventory")
	}
	seenID, seenParent, seenValue := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		if row.Key != "Designator" || strings.TrimSpace(row.ID) == "" || strings.TrimSpace(row.ParentID) == "" || strings.TrimSpace(row.Value) == "" || row.Visible == nil || !*row.Visible || !schPositiveBBox(row.BBox) || row.Source != schDesignatorBBoxSource {
			return nil, fmt.Errorf("Designator %q has missing/invalid identity, visibility, official bbox or measurement source", row.Value)
		}
		if seenID[row.ID] || seenParent[row.ParentID] || seenValue[row.Value] {
			return nil, fmt.Errorf("Designator %q has duplicate attribute, parent or value", row.Value)
		}
		seenID[row.ID], seenParent[row.ParentID], seenValue[row.Value] = true, true, true
	}
	return &schDesignatorGeometryExport{SchemaVersion: 1, DocumentID: actualDoc, Source: "official-current-page-attribute-bbox", Count: len(rows), Designators: rows}, nil
}

func newSchDesignatorGeometryCmd(cfg *appConfig, window *string, stdout io.Writer) *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "designator-geometry",
		Short: "Export measured current-page Designator text bboxes (read-only)",
		Long: `Read every part on the selected schematic page and require exactly one visible
Designator attribute whose value matches its parent part's reference. Each bbox
comes from the official per-attribute primitive measurement. Missing, duplicate,
hidden, mismatched or invalid geometry fails the whole export before writing --out.
The designators[].bbox values can be copied by parentId into source
measurement.textBboxes; preserve this raw export as measurement provenance.
No other component attributes, arbitrary JavaScript, or EDA mutations are used.`,
		Example: `  pcbpilot sch designator-geometry --project ceshi --doc <page-uuid> --out designators.json
  pcbpilot sch designator-geometry --project ceshi --doc <page-uuid>`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned, win, doc, err := pinZonePage(cfg, *window)
			if err != nil {
				return err
			}
			res, err := requestAction(pinned, "schematic.designators.list", win, nil)
			if err != nil {
				return err
			}
			if res.Context != nil && res.Context.DocumentUUID != "" && res.Context.DocumentUUID != doc {
				return fmt.Errorf("Designator measurement response came from %q, expected %q", res.Context.DocumentUUID, doc)
			}
			export, err := parseSchDesignatorGeometryExport(res.Result, doc)
			if err != nil {
				return err
			}
			if res.Context != nil {
				export.ProjectID = res.Context.ProjectUUID
			}
			b, err := json.MarshalIndent(export, "", "  ")
			if err != nil {
				return err
			}
			b = append(b, '\n')
			if out != "" {
				return os.WriteFile(out, b, 0644)
			}
			_, err = stdout.Write(b)
			return err
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "write complete measured JSON to file; default stdout")
	return cmd
}
