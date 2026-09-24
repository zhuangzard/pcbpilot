package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const maxProjectSourceBytes = 8 << 20

func newProjectExportSourceCmd(cfg *appConfig, stdout, stderr io.Writer, window *string) *cobra.Command {
	var uuid, out string
	c := &cobra.Command{
		Use:   "export-source",
		Short: "Export the current project's original .epro2 file through the official read-only API",
		Long:  "Export the native project archive as an unmodified artifact. The expected current project UUID is checked before and after export. This command does not interpret the archive or claim to measure a schematic sheet's inner border or title block. The official getter requires project-download permission. Files over 8 MiB are refused before transfer.",
		Args:  cobra.NoArgs,
		Example: `  pcbpilot project export-source --uuid <current-project-uuid> --window <window-id>
  pcbpilot project export-source --uuid <current-project-uuid> --out project.epro2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if uuid == "" {
				return fmt.Errorf("--uuid is required")
			}
			res, err := requestActionTimed(cfg, "project.export_source", *window,
				map[string]any{"uuid": uuid}, 60*time.Second)
			if err != nil {
				return err
			}
			if err := verifyProjectSourceArtifact(res, uuid); err != nil {
				return err
			}
			if out != "" {
				if err := saveFirstArtifact(res, out, stderr); err != nil {
					return err
				}
			}
			res.Result["artifactSha256"] = res.Artifacts[0].SHA256
			res.Result["artifactSize"] = res.Artifacts[0].Size
			return encodeResultEnvelope(res, res.Result, stdout)
		},
	}
	c.Flags().StringVar(&uuid, "uuid", "", "exact UUID of the current project (required)")
	c.Flags().StringVar(&out, "out", "", "also copy the original archive to this path")
	return c
}

func verifyProjectSourceArtifact(res *actionResult, uuid string) error {
	if res == nil || !res.OK || len(res.Artifacts) != 1 || res.Result == nil {
		return fmt.Errorf("project source export returned no single persisted artifact")
	}
	if res.Result["uuid"] != uuid || res.Result["fileType"] != "epro2" {
		return fmt.Errorf("project source export identity or format differs from the request")
	}
	a := res.Artifacts[0]
	if a.ID == "" || res.Result["artifactId"] != a.ID || a.Path == "" || a.Size <= 0 || a.Size > maxProjectSourceBytes || len(a.SHA256) != 64 {
		return fmt.Errorf("project source artifact is missing, oversized, or lacks persisted integrity evidence")
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return fmt.Errorf("read project source artifact: %w", err)
	}
	if int64(len(data)) != a.Size || res.Result["size"] != float64(len(data)) {
		return fmt.Errorf("project source artifact size does not match the official getter and daemon")
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), a.SHA256) {
		return fmt.Errorf("project source artifact SHA-256 does not match persisted bytes")
	}
	return nil
}
