package selfupdate

// Rollback snapshots: before any apply, the current binary, every present
// (non-linked) skill dir and the MCP current link are copied to
// ~/.pcbpilot/rollback/<version>/. `pcbpilot update --rollback` and the
// daemon's failed post-restart verify restore from it. The two newest
// snapshots are kept.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SnapshotSkill struct {
	Client string `json:"client"`
	Dir    string `json:"dir"`
	Saved  string `json:"saved"` // relative to the snapshot dir
}

type Snapshot struct {
	Version    string          `json:"version"`
	Bin        string          `json:"bin"`
	SavedBin   string          `json:"savedBin"`
	Skills     []SnapshotSkill `json:"skills,omitempty"`
	MCPCurrent string          `json:"mcpCurrent,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
	Dir        string          `json:"-"`
}

func RollbackRoot() string { return statePath("rollback") }

// TakeSnapshot saves what an update is about to replace.
func TakeSnapshot(version, bin string) (Snapshot, error) {
	version = strings.TrimPrefix(version, "v")
	if version == "" {
		version = "unknown"
	}
	root := RollbackRoot()
	dir := filepath.Join(root, strings.NewReplacer("/", "_", `\`, "_").Replace(version))
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{Version: version, Bin: bin, CreatedAt: time.Now().UTC(), Dir: dir, MCPCurrent: MCPCurrentTarget()}
	if bin != "" {
		s.SavedBin = filepath.Join("bin", filepath.Base(bin))
		if err := copyFile(bin, filepath.Join(dir, s.SavedBin), 0o755); err != nil {
			return s, fmt.Errorf("snapshot binary: %w", err)
		}
	}
	for _, t := range Targets(true) {
		if t.Linked != "" {
			continue
		}
		rel := filepath.Join("skills", t.Client)
		if err := copyTree(t.Dir, filepath.Join(dir, rel), false, false); err != nil {
			return s, fmt.Errorf("snapshot skill %s: %w", t.Client, err)
		}
		s.Skills = append(s.Skills, SnapshotSkill{Client: t.Client, Dir: t.Dir, Saved: rel})
	}
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), s); err != nil {
		return s, err
	}
	pruneSnapshots(root, 2)
	return s, nil
}

// LatestSnapshot returns the newest snapshot (or the one for version).
func LatestSnapshot(version string) (Snapshot, error) {
	root := RollbackRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("no rollback snapshot in %s", root)
	}
	var best Snapshot
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var s Snapshot
		if ReadJSON(filepath.Join(root, e.Name(), "manifest.json"), &s) != nil || s.Version == "" {
			continue
		}
		s.Dir = filepath.Join(root, e.Name())
		if version != "" {
			if s.Version == strings.TrimPrefix(version, "v") {
				return s, nil
			}
			continue
		}
		if s.CreatedAt.After(best.CreatedAt) {
			best = s
		}
	}
	if best.Version == "" {
		return best, fmt.Errorf("no rollback snapshot in %s", root)
	}
	return best, nil
}

// RestoreSnapshot puts the saved binary, skill dirs and MCP link back.
func RestoreSnapshot(_ context.Context, s Snapshot) ([]string, error) {
	var restored, errs []string
	if s.Bin != "" && s.SavedBin != "" {
		src := filepath.Join(s.Dir, s.SavedBin)
		tmp, err := os.CreateTemp(filepath.Dir(s.Bin), ".pcbpilot-rollback-*")
		if err == nil {
			tmp.Close()
			if err = copyFile(src, tmp.Name(), 0o755); err == nil {
				_ = os.Chmod(tmp.Name(), 0o755)
				err = replaceBinary(tmp.Name(), s.Bin)
			}
			_ = os.Remove(tmp.Name())
		}
		if err != nil {
			errs = append(errs, "binary: "+err.Error())
		} else {
			restored = append(restored, "cli")
		}
	}
	for _, sk := range s.Skills {
		if err := materializeTree(filepath.Join(s.Dir, sk.Saved), sk.Dir); err != nil {
			errs = append(errs, "skill "+sk.Client+": "+err.Error())
		} else {
			restored = append(restored, "skill:"+sk.Client)
		}
	}
	if s.MCPCurrent != "" && s.MCPCurrent != MCPCurrentTarget() {
		if err := SetMCPCurrent(s.MCPCurrent); err != nil {
			errs = append(errs, "mcp: "+err.Error())
		} else {
			restored = append(restored, "mcp")
		}
	}
	if len(errs) > 0 {
		return restored, fmt.Errorf("rollback: %s", strings.Join(errs, "; "))
	}
	return restored, nil
}

// materializeTree replaces dst with an exact copy of src (stage + swap).
func materializeTree(src, dst string) error {
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".pcbpilot-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := copyTree(src, stage, false, false); err != nil {
		return err
	}
	_ = os.Chmod(stage, 0o755)
	aside := dst + ".pcbpilot-rollback-old"
	_ = os.RemoveAll(aside)
	if isDir(dst) {
		if err := os.Rename(dst, aside); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, dst); err != nil {
		_ = os.Rename(aside, dst)
		return err
	}
	return os.RemoveAll(aside)
}

func pruneSnapshots(root string, keep int) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type snap struct {
		dir string
		at  time.Time
	}
	var all []snap
	for _, e := range entries {
		var s Snapshot
		p := filepath.Join(root, e.Name())
		if e.IsDir() && ReadJSON(filepath.Join(p, "manifest.json"), &s) == nil {
			all = append(all, snap{p, s.CreatedAt})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
	for i := keep; i < len(all); i++ {
		_ = os.RemoveAll(all[i].dir)
	}
}

// InstallBinaryBytes stages data next to path, checks that it runs and reports
// wantVersion, then atomically replaces path.
func InstallBinaryBytes(ctx context.Context, data []byte, path, wantVersion string) error {
	dir := filepath.Dir(path)
	if err := checkWritable(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pcbpilot-update-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	if err := verifyBinary(ctx, tmp, strings.TrimPrefix(wantVersion, "v")); err != nil {
		return err
	}
	return replaceBinary(tmp, path)
}
