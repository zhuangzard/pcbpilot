package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ConnectorAsset = "pcbpilot-connector.eext"

func ConnectorDir() string { return statePath("connector") }

// ConnectorPath is where the downloaded .eext for version lives.
func ConnectorPath(version string) string {
	return filepath.Join(ConnectorDir(), "pcbpilot-connector-v"+strings.TrimPrefix(version, "v")+".eext")
}

// ConnectorOutcome is the result of DownloadConnector.
type ConnectorOutcome struct {
	Version string `json:"version"`
	Path    string `json:"path,omitempty"`
	Status  string `json:"status"` // downloaded | present | skipped | error
	Reason  string `json:"reason,omitempty"`
}

// DownloadConnector fetches the release .eext (checksum-verified by the
// source), checks its manifest version and stores it under
// ~/.pcbpilot/connector/. An existing file with the right manifest is reused.
// The connector itself can only be imported by a human (EasyEDA's extension
// manager has no API and GUI automation is not allowed).
func DownloadConnector(ctx context.Context, src AssetSource) (ConnectorOutcome, error) {
	v := src.Version()
	out := ConnectorOutcome{Version: v, Path: ConnectorPath(v)}
	if raw, err := os.ReadFile(out.Path); err == nil {
		if mv, err := ConnectorManifestVersion(raw); err == nil && mv == v {
			out.Status = "present"
			return out, nil
		}
	}
	body, err := src.Fetch(ctx, ConnectorAsset, 32<<20)
	if errors.Is(err, ErrAssetMissing) {
		out.Status, out.Reason, out.Path = "skipped", "release has no "+ConnectorAsset, ""
		return out, nil
	}
	if err != nil {
		out.Status, out.Reason = "error", err.Error()
		return out, err
	}
	mv, err := ConnectorManifestVersion(body)
	if err != nil || mv != v {
		out.Status = "error"
		out.Reason = fmt.Sprintf("connector manifest version %q (want %s): %v", mv, v, err)
		return out, errors.New(out.Reason)
	}
	if err := os.MkdirAll(ConnectorDir(), 0o755); err != nil {
		return out, err
	}
	tmp := out.Path + ".partial"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return out, err
	}
	if err := os.Rename(tmp, out.Path); err != nil {
		return out, err
	}
	out.Status = "downloaded"
	return out, nil
}

// ConnectorImportSteps is the human procedure, with the local file path.
func ConnectorImportSteps(path string) []string {
	if path == "" {
		path = "pcbpilot-connector.eext from https://github.com/" + Repo() + "/releases/latest"
	}
	return []string{
		`EasyEDA Pro → 高级/Advanced → 扩展管理器/Extension manager → 已安装/Installed: uninstall the old "PCB Pilot Connector"`,
		"Import " + path,
		`Select "PCB Pilot Connector" → Enabled → Config → tick 允许外部交互/Allow external interaction, then reload the editor (Web: refresh the page; desktop: restart EasyEDA)`,
	}
}
