package selfupdate

// MCP server package (release asset mcp.tar.gz). The archive holds mcp/ with
// src/, package.json, VERSION and PRODUCTION node_modules (pure JS, no native
// addons, no .bin links), so installing needs only `node` — no npm, no network
// beyond the one verified download. ~3.5 MB compressed, ~24 MB on disk.
//
// Layout:  ~/.pcbpilot/mcp/<version>/src/server.mjs
//          ~/.pcbpilot/mcp/current → <version>   (symlink; junction on Windows)
// Clients are registered against current/src/server.mjs, so later updates only
// swap the link; the previous version is kept for rollback, older ones pruned.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const MCPAsset = "mcp.tar.gz"

func MCPRoot() string    { return statePath("mcp") }
func MCPCurrent() string { return filepath.Join(MCPRoot(), "current") }

// MCPServerPath is the stable server entry every client is registered with.
func MCPServerPath() string { return filepath.Join(MCPCurrent(), "src", "server.mjs") }

// MCPInstalledVersion reads current/VERSION ("" when not installed).
func MCPInstalledVersion() string {
	b, err := os.ReadFile(filepath.Join(MCPCurrent(), "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// SourceMCP reports whether this machine is a source install (install.json
// kind "source") whose every registered MCP client runs the checkout's
// mcp/src/server.mjs — updated by git pull, not the release tarball. Shared by
// `pcbpilot update --check` and the console's component table.
func SourceMCP(clients []Registration) (string, bool) {
	info := ReadInstallInfo()
	if info.Kind != "source" || info.Repo == "" || len(clients) == 0 {
		return "", false
	}
	server := filepath.Join(info.Repo, "mcp", "src", "server.mjs")
	for _, r := range clients {
		if !strings.Contains(r.Detail+" "+r.Server, server) {
			return "", false
		}
	}
	return server, true
}

// MCPCurrentTarget is the version dir current points at ("" if none).
func MCPCurrentTarget() string {
	t, err := os.Readlink(MCPCurrent())
	if err != nil {
		if r, e := filepath.EvalSymlinks(MCPCurrent()); e == nil {
			return filepath.Base(r)
		}
		return ""
	}
	return filepath.Base(t)
}

type MCPOutcome struct {
	From   string `json:"from,omitempty"`
	To     string `json:"to,omitempty"`
	Dir    string `json:"dir,omitempty"`
	Server string `json:"server,omitempty"`
	Status string `json:"status"` // updated | installed | up-to-date | skipped | error
	Reason string `json:"reason,omitempty"`
}

// InstallMCP installs the source's mcp.tar.gz and points current at it. A
// release without the asset is "skipped" (ErrAssetMissing), not an error.
func InstallMCP(ctx context.Context, src AssetSource, force bool) (MCPOutcome, error) {
	target := src.Version()
	out := MCPOutcome{From: MCPInstalledVersion(), To: target, Server: MCPServerPath()}
	if !force && out.From == target && fileExists(MCPServerPath()) {
		out.Status = "up-to-date"
		out.Dir = filepath.Join(MCPRoot(), target)
		return out, nil
	}
	archive, err := src.Fetch(ctx, MCPAsset, 64<<20)
	if errors.Is(err, ErrAssetMissing) {
		out.Status, out.Reason = "skipped", "release v"+target+" has no "+MCPAsset
		return out, nil
	}
	if err != nil {
		out.Status, out.Reason = "error", err.Error()
		return out, err
	}
	dir, err := installMCPArchive(archive, target)
	if err != nil {
		out.Status, out.Reason = "error", err.Error()
		return out, err
	}
	out.Dir = dir
	out.Status = "updated"
	if out.From == "" {
		out.Status = "installed"
	}
	return out, nil
}

var mcpVersionDir = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-dev\.[0-9]+)?$`)

func installMCPArchive(archive []byte, version string) (string, error) {
	root := MCPRoot()
	if root == "" {
		return "", fmt.Errorf("no home dir")
	}
	if !mcpVersionDir.MatchString(version) {
		return "", fmt.Errorf("bad MCP version %q", version)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".stage-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	if err := extractMCP(archive, stage); err != nil {
		return "", err
	}
	for _, need := range []string{"src/server.mjs", "package.json", "VERSION", "node_modules/@modelcontextprotocol/sdk/package.json"} {
		if !fileExists(filepath.Join(stage, need)) {
			return "", fmt.Errorf("%s is missing mcp/%s", MCPAsset, need)
		}
	}
	if got := strings.TrimSpace(readFile(filepath.Join(stage, "VERSION"))); got != version {
		return "", fmt.Errorf("%s declares VERSION %q, expected %s", MCPAsset, got, version)
	}
	prev := MCPCurrentTarget()
	dst := filepath.Join(root, version)
	if fileExists(dst) {
		// Re-install of the same version (force / repair): move the old copy aside.
		aside := filepath.Join(root, ".old-"+version)
		_ = os.RemoveAll(aside)
		if err := os.Rename(dst, aside); err != nil {
			return "", err
		}
		defer os.RemoveAll(aside)
	}
	if err := os.Rename(stage, dst); err != nil {
		return "", err
	}
	if err := SetMCPCurrent(version); err != nil {
		return "", err
	}
	pruneMCP(root, version, prev)
	return dst, nil
}

// SetMCPCurrent points current at <root>/<version> (rollback uses it too).
func SetMCPCurrent(version string) error {
	root := MCPRoot()
	target := filepath.Join(root, version)
	if !fileExists(target) {
		return fmt.Errorf("MCP %s is not installed at %s", version, target)
	}
	return linkDir(target, MCPCurrent())
}

// linkDir makes link point at target, replacing an existing link. Unix: a
// relative symlink swapped in by rename (atomic). Windows: a directory
// junction (no admin or developer mode needed).
func linkDir(target, link string) error {
	if runtime.GOOS == "windows" {
		_ = os.Remove(link) // removes the junction, never the target
		out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput()
		if err != nil {
			return fmt.Errorf("mklink /J %s: %v: %s", link, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	rel, err := filepath.Rel(filepath.Dir(link), target)
	if err != nil {
		rel = target
	}
	tmp := link + ".tmp"
	_ = os.Remove(tmp)
	if err := os.Symlink(rel, tmp); err != nil {
		return err
	}
	if fi, err := os.Lstat(link); err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
		// A real directory named current (never written by us): move it aside.
		_ = os.Rename(link, link+".replaced")
	}
	return os.Rename(tmp, link)
}

// pruneMCP keeps the new version and the one it replaced (for rollback).
func pruneMCP(root, keep, prev string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && mcpVersionDir.MatchString(e.Name()) && e.Name() != keep && e.Name() != prev {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		_ = os.RemoveAll(filepath.Join(root, n))
	}
}

func extractMCP(archive []byte, dst string) error {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.ToSlash(filepath.Clean(h.Name))
		rel, ok := strings.CutPrefix(name, "mcp/")
		if !ok || rel == "" || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") || filepath.IsAbs(rel) {
			if name == "mcp" {
				continue
			}
			return fmt.Errorf("unsafe %s entry %q", MCPAsset, h.Name)
		}
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if !strings.HasPrefix(p, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe %s entry %q", MCPAsset, h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			total += h.Size
			if h.Size < 0 || h.Size > 64<<20 || total > 256<<20 {
				return fmt.Errorf("%s extracted content exceeds size limit", MCPAsset)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(h.Mode)&0o755|0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, io.LimitReader(tr, h.Size)); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Symlinks/devices are never needed at runtime (node_modules/.bin is
			// excluded at pack time); skip rather than follow.
		}
	}
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func readFile(p string) string { b, _ := os.ReadFile(p); return string(b) }

// ── node ──────────────────────────────────────────────────────────────────

// MinNode is the MCP SDK's engines floor.
const MinNode = "20.17.0"

type NodeInfo struct {
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	OK      bool   `json:"ok"`
	Hint    string `json:"hint,omitempty"`
}

// NodeInstallHint is printed when node is missing or too old.
func NodeInstallHint(goos string) string {
	switch goos {
	case "darwin":
		return "install Node.js >= 20.17: brew install node  (or https://nodejs.org)"
	case "windows":
		return "install Node.js >= 20.17: winget install OpenJS.NodeJS.LTS  (or https://nodejs.org)"
	default:
		return "install Node.js >= 20.17 from your package manager or https://nodejs.org (e.g. via nvm)"
	}
}

// FindNode locates node and checks its version.
func FindNode(lookPath func(string) (string, error), output func(string, ...string) (string, error), goos string) NodeInfo {
	p, err := lookPath("node")
	if err != nil {
		return NodeInfo{Hint: NodeInstallHint(goos)}
	}
	out, err := output(p, "--version")
	v := strings.TrimPrefix(strings.TrimSpace(out), "v")
	info := NodeInfo{Path: p, Version: v}
	if err != nil || SemverCore(v) == "" || SemverLess(v, MinNode) {
		info.Hint = "Node.js " + orUnknown(v) + " is older than " + MinNode + " — " + NodeInstallHint(goos)
		return info
	}
	info.OK = true
	return info
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
