package app

// Typed project actions run in the connector; native archive validation and
// filesystem delivery remain in the CLI.
import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const projectArchiveLimit = 16 * 1024 * 1024

func projectTransferRequest(cfg *appConfig, window, operation, uuid, pageUUID string) (*actionResult, map[string]any, error) {
	if strings.TrimSpace(window) == "" || strings.TrimSpace(uuid) == "" {
		return nil, nil, fmt.Errorf("explicit --window and --project-uuid are required")
	}
	if cfg.project != "" || cfg.doc != "" {
		return nil, nil, fmt.Errorf("project transfer uses --project-uuid and --window; omit global --project/--doc routing")
	}
	if operation != "open" && operation != "export" {
		return nil, nil, fmt.Errorf("unsupported project transfer operation %q", operation)
	}
	payload := map[string]any{"projectUuid": uuid}
	if operation == "open" {
		payload["allowDiscardUnsaved"] = true // caller has checked the explicit CLI flag
		if pageUUID != "" {
			payload["pageUuid"] = pageUUID
		}
	}
	res, err := requestActionTimed(cfg, "project."+operation, window, payload, 60*time.Second)
	if err != nil {
		return res, nil, err
	}
	value := res.Result
	if value["uuid"] != uuid {
		return res, nil, fmt.Errorf("project transfer returned missing or mismatched identity")
	}
	return res, value, nil
}

func writeProjectArchive(value map[string]any, out string) (map[string]any, error) {
	encoded, ok := value["base64"].(string)
	if !ok || len(encoded) > base64.StdEncoding.EncodedLen(projectArchiveLimit) {
		return nil, fmt.Errorf("invalid or oversized project archive envelope")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid project archive base64: %w", err)
	}
	size, ok := value["size"].(float64)
	if !ok || size != float64(len(data)) || len(data) == 0 || len(data) > projectArchiveLimit || value["format"] != "epro2" {
		return nil, fmt.Errorf("invalid project archive size/format")
	}
	// Validate the native ZIP container and CRCs before delivering a file. Do not
	// extract user-controlled paths. Bound expanded data to avoid archive bombs.
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid native project ZIP: %w", err)
	}
	var expanded uint64
	native := false
	for _, f := range z.File {
		if f.UncompressedSize64 > 128*1024*1024-expanded {
			return nil, fmt.Errorf("project archive expanded size exceeds 128 MiB")
		}
		expanded += f.UncompressedSize64
		if strings.HasSuffix(strings.ToLower(f.Name), ".epru") {
			native = true
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		_, e = io.Copy(io.Discard, io.LimitReader(r, int64(f.UncompressedSize64)+1))
		r.Close()
		if e != nil {
			return nil, fmt.Errorf("project ZIP integrity: %w", e)
		}
	}
	if !native {
		return nil, fmt.Errorf("native epro2 archive contains no epru project data")
	}
	if err = os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	absolute, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	return map[string]any{"uuid": value["uuid"], "format": "epro2", "path": absolute, "bytes": len(data), "sha256": fmt.Sprintf("%x", sha256.Sum256(data)), "zipIntegrityVerified": true, "restoreVerified": false}, nil
}

func newProjectExportCmd(cfg *appConfig, window *string, stdout io.Writer) *cobra.Command {
	var uuid, out string
	c := &cobra.Command{Use: "export", Short: "Export the active project as a native epro2 archive (no overwrite)", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if out == "" || !strings.EqualFold(filepath.Ext(out), ".epro2") {
			return fmt.Errorf("--out must name a new .epro2 file")
		}
		if _, err := os.Stat(out); err == nil {
			return fmt.Errorf("output already exists: %s", out)
		} else if !os.IsNotExist(err) {
			return err
		}
		res, value, err := projectTransferRequest(cfg, *window, "export", uuid, "")
		if err != nil {
			return err
		}
		report, err := writeProjectArchive(value, out)
		if err != nil {
			return err
		}
		return encodeResultEnvelope(res, report, stdout)
	}}
	c.Flags().StringVar(&uuid, "project-uuid", "", "expected active project UUID (required)")
	c.Flags().StringVar(&out, "out", "", "new .epro2 output path (required); save documents before exporting")
	return c
}
