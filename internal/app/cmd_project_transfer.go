package app

// Fixed, typed compatibility adapters for released connectors. These call only
// official APIs; callers cannot supply JavaScript or select another operation.
import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const projectArchiveLimit = 16 * 1024 * 1024

func projectTransferCode(operation, uuid, pageUUID string) string {
	target, _ := json.Marshal(uuid)
	page, _ := json.Marshal(pageUUID)
	prefix := `const target = ` + string(target) + `;
const pageUUID = ` + string(page) + `;
async function assertProject() {
 const p = await eda.dmt_Project.getCurrentProjectInfo();
 if (!p || p.uuid !== target) throw new Error("Active project does not match requested UUID");
 return p;
}
`
	if operation == "open" {
		return prefix + `
const opened = await eda.dmt_Project.openProject(target);
if (!opened) throw new Error("Official openProject returned false; inspect current state before retrying");
let project;
for (let i = 0; i < 40; i++) {
 try { project = await assertProject(); break; } catch (e) { if (i === 39) throw e; }
 await new Promise(resolve => setTimeout(resolve, 250));
}
let document;
if (pageUUID) {
 // Project identity settles before its document tree. Wait for the requested
 // page to exist; do not repeatedly invoke an opening action while loading.
 let found = false;
 for (let i=0;i<40;i++) {
  await assertProject();
  const pages = await eda.dmt_Schematic.getAllSchematicPagesInfo();
  if (Array.isArray(pages) && pages.some(p => p.uuid === pageUUID)) { found=true; break; }
  await new Promise(resolve => setTimeout(resolve,250));
 }
 if (!found) throw new Error("Project opened but requested schematic page is unavailable; do not repeat project creation");
 const tab = await eda.dmt_EditorControl.openDocument(pageUUID);
 if (!tab) throw new Error("Project opened but page open failed; inspect state before retrying");
 for (let i=0;i<40;i++) {
  await assertProject();
  document = await eda.dmt_SelectControl.getCurrentDocumentInfo();
  if (document?.uuid === pageUUID && document?.parentProjectUuid === target) break;
  if (i===39) throw new Error("Project opened but page identity did not settle");
  await new Promise(resolve => setTimeout(resolve,250));
 }
}
return {uuid: project.uuid, friendlyName: project.friendlyName, opened: true, verified: true,
 ...(document ? {documentUuid: document.uuid, documentVerified: true} : {})};`
	}
	return prefix + `
await assertProject();
const file = await eda.sys_FileManager.getProjectFile("project.epro2", undefined, "epro2");
if (!file || typeof file.arrayBuffer !== "function") throw new Error("Official project export returned no file");
if (!file.size || file.size > 16777216) throw new Error("Project archive empty or exceeds 16 MiB transport limit");
const bytes = new Uint8Array(await file.arrayBuffer());
await assertProject();
let binary = "";
for (let i=0;i<bytes.length;i+=8192) binary += String.fromCharCode(...bytes.subarray(i,i+8192));
return {uuid: target, format: "epro2", size: bytes.length, base64: btoa(binary)};`
}

func projectTransferRequest(cfg *appConfig, window, operation, uuid, pageUUID string) (*actionResult, map[string]any, error) {
	if strings.TrimSpace(window) == "" || strings.TrimSpace(uuid) == "" {
		return nil, nil, fmt.Errorf("explicit --window and --project-uuid are required")
	}
	if cfg.project != "" || cfg.doc != "" {
		return nil, nil, fmt.Errorf("project transfer uses --project-uuid and --window; omit global --project/--doc routing")
	}
	res, err := requestActionTimed(cfg, "debug.exec_js", window, map[string]any{"code": projectTransferCode(operation, uuid, pageUUID)}, 60*time.Second)
	if err != nil {
		return res, nil, err
	}
	value, ok := res.Result["value"].(map[string]any)
	if !ok || value["uuid"] != uuid {
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
