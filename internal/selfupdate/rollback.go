package selfupdate

// Rollback snapshots: before any apply, the current binary, every present
// (non-linked) skill dir — each public Skill of each client — and the MCP
// current link are copied to
// ~/.pcbpilot/rollback/<version>/. `pcbpilot update --rollback` and the
// daemon's failed post-restart verify restore from it. The two newest
// snapshots are kept.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

type SnapshotSkill struct {
	Client string `json:"client"`
	Skill  string `json:"skill,omitempty"` // "" in snapshots from before focused Skills: the router
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
		rel := filepath.Join("skills", t.Client, t.Skill)
		if err := copyTree(t.Dir, filepath.Join(dir, rel), false, false); err != nil {
			return s, fmt.Errorf("snapshot skill %s: %w", SkillLabel(t.Client, t.Skill), err)
		}
		s.Skills = append(s.Skills, SnapshotSkill{Client: t.Client, Skill: t.Skill, Dir: t.Dir, Saved: rel})
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

// JournalEntry is one Skill destination a sync changed: created (did not
// exist), replaced, or removed (a retired Skill). Saved names the previous
// tree inside the journal dir.
type JournalEntry struct {
	Dir    string `json:"dir"`
	Action string `json:"action"`
	Saved  string `json:"saved,omitempty"`
	// Version is the marker a created dir was written with; rollback removes
	// the dir only while it is still that pcbpilot-owned install.
	Version string `json:"version,omitempty"`
}

// SkillJournalDir is where an update's skill sync journals its changes.
func SkillJournalDir(snapshotDir string) string { return filepath.Join(snapshotDir, "skill-journal") }

func journalFile(dir string) string { return filepath.Join(dir, "journal.json") }

func appendJournal(dir string, entries []JournalEntry) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var all []JournalEntry
	if err := ReadJSON(journalFile(dir), &all); err != nil && !os.IsNotExist(err) {
		return err
	}
	return WriteJSON(journalFile(dir), append(append([]JournalEntry{}, all...), entries...))
}

// RestoreSnapshot puts the saved binary, skill dirs and MCP link back. When the
// update journaled its skill sync, only the journaled destinations are
// undone: created dirs are removed, replaced and removed (retired) dirs get
// their previous tree back — including Skills the initiating CLI did not know.
// Without a journal (older snapshots), a public Skill the snapshot did not hold
// for a client it covered is removed when pcbpilot installed it.
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
	var journal []JournalEntry
	jdir := SkillJournalDir(s.Dir)
	_, statErr := os.Stat(journalFile(jdir))
	haveJournal := statErr == nil
	if haveJournal {
		if err := ReadJSON(journalFile(jdir), &journal); err != nil {
			errs = append(errs, "skill journal: "+err.Error())
		}
	}
	for i := len(journal) - 1; i >= 0; i-- {
		e := journal[i]
		label := "skill:" + e.Dir
		var err error
		switch {
		case e.Action == "created":
			if _, statErr := os.Lstat(e.Dir); statErr != nil {
				break // already gone
			}
			if !ownedSkillDir(e.Dir) || e.Version == "" || readMarker(e.Dir) != e.Version {
				// Something else lives there now (a user dir, a file, another
				// install): never delete it.
				err = fmt.Errorf("conflict: no longer the %q install this update created; left in place", e.Version)
				break
			}
			err = os.RemoveAll(e.Dir)
			label += " removed"
		case e.Saved != "":
			err = materializeTree(filepath.Join(jdir, e.Saved), e.Dir)
		default:
			err = fmt.Errorf("previous tree was not journaled")
		}
		if err != nil {
			errs = append(errs, label+": "+err.Error())
		} else {
			restored = append(restored, label)
		}
	}
	saved := map[string]bool{}
	for _, sk := range s.Skills {
		saved[filepath.Clean(sk.Dir)] = true
		if haveJournal {
			// The journal is the exact record of what the update changed;
			// anything it does not list (another client, a later edit) stays.
			continue
		}
		label := SkillLabel(sk.Client, sk.Skill)
		if err := materializeTree(filepath.Join(s.Dir, sk.Saved), sk.Dir); err != nil {
			errs = append(errs, "skill "+label+": "+err.Error())
		} else {
			restored = append(restored, "skill:"+label)
		}
	}
	for _, sk := range s.Skills {
		if haveJournal {
			break
		}
		if sk.Skill != "" && sk.Skill != SkillName {
			continue // one sweep per client, anchored at its router dir
		}
		base := filepath.Dir(sk.Dir)
		known := PublicSkills()
		for _, name := range slices.Concat(known.Skills, known.Retired) {
			dir := filepath.Join(base, name)
			if name == SkillName || saved[filepath.Clean(dir)] || !ownedSkillDir(dir) {
				continue
			}
			label := SkillLabel(sk.Client, name)
			if err := removeOwnedSkill(dir); err != nil {
				errs = append(errs, "skill "+label+": "+err.Error())
			} else {
				restored = append(restored, "skill:"+label+" removed")
			}
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
