package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

const maxSymbolSourceBytes = 8 << 20

func newLibrarySymbolExportSourceCmd(cfg *appConfig, stdout, stderr io.Writer, window *string) *cobra.Command {
	var uuid, libraryUUID, out string
	c := &cobra.Command{
		Use:   "export-source",
		Short: "Export a symbol's original .elibz2 file through the official read-only API",
		Long:  "Export the native symbol file as an unmodified artifact. This command does not decode the file or claim to measure a schematic sheet's inner border or title block. The official getter may require library-download permission. Files over 8 MiB are refused before transfer.",
		Args:  cobra.NoArgs,
		Example: `  pcbpilot lib symbol export-source --uuid <sheet-symbol-uuid> --library <library-uuid>
  pcbpilot lib symbol export-source --uuid <sheet-symbol-uuid> --library <library-uuid> --out sheet-symbol.elibz2`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if uuid == "" || libraryUUID == "" {
				return fmt.Errorf("--uuid and --library are required")
			}
			res, err := requestAction(cfg, "library.symbol.export_source", *window,
				map[string]any{"uuid": uuid, "libraryUuid": libraryUUID})
			if err != nil {
				return err
			}
			if err := verifyLibrarySymbolSourceArtifact(res, uuid, libraryUUID); err != nil {
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
	c.Flags().StringVar(&uuid, "uuid", "", "exact symbol UUID from the sheet component's symbol field (required)")
	c.Flags().StringVar(&libraryUUID, "library", "", "exact symbol library UUID (required)")
	c.Flags().StringVar(&out, "out", "", "also copy the original archive to this path")
	return c
}

func verifyLibrarySymbolSourceArtifact(res *actionResult, uuid, libraryUUID string) error {
	if res == nil || !res.OK || len(res.Artifacts) != 1 || res.Result == nil {
		return fmt.Errorf("symbol source export returned no single persisted artifact")
	}
	if res.Result["uuid"] != uuid || res.Result["libraryUuid"] != libraryUUID || res.Result["fileType"] != "elibz2" {
		return fmt.Errorf("symbol source export identity or format differs from the request")
	}
	a := res.Artifacts[0]
	if a.ID == "" || res.Result["artifactId"] != a.ID || a.Path == "" || a.Size <= 0 || a.Size > maxSymbolSourceBytes || len(a.SHA256) != 64 {
		return fmt.Errorf("symbol source artifact is missing, oversized, or lacks persisted integrity evidence")
	}
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return fmt.Errorf("read symbol source artifact: %w", err)
	}
	if int64(len(data)) != a.Size || res.Result["size"] != float64(len(data)) {
		return fmt.Errorf("symbol source artifact size does not match the official getter and daemon")
	}
	sum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), a.SHA256) {
		return fmt.Errorf("symbol source artifact SHA-256 does not match persisted bytes")
	}
	return nil
}
