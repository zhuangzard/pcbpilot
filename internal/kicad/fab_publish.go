package kicad

// fab_publish.go — moving a staged `kicad fab` run into OUT without touching
// files pcbpilot does not own, and without losing the previous good set when
// a step fails half-way.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	fabReportName   = "fab-report.json"
	fabManifestName = ".pcbpilot-fab-manifest.json"
)

// ErrFabCollision is returned (wrapped) when a file this run would write
// already exists in OUT and the previous run's manifest does not list it.
var ErrFabCollision = errors.New("fab output would overwrite files pcbpilot does not own")

// fabRename is os.Rename; tests replace it to inject failures. Publishing
// only ever renames onto a path that does not exist (owned old files are
// moved aside first), so Windows' rename-over-existing semantics never matter.
var fabRename = os.Rename

// fabManifest lists what the last successful run wrote into OUT (Files,
// base names) and OUT/gerber (Gerbers, base names). Only these paths are
// ever replaced or removed by a later run; anything else in OUT is the
// user's and is left alone.
type fabManifest struct {
	Files   []string `json:"files"`
	Gerbers []string `json:"gerbers"`
}

// safeBase accepts a manifest entry only when it is a plain file name, so a
// hand-edited manifest cannot point a removal outside OUT.
func safeBase(name string) bool {
	return name != "" && name != "." && name != ".." && name != fabManifestName &&
		filepath.Base(name) == name && !strings.ContainsAny(name, `/\:`)
}

// readFabManifest reads OUT's manifest (absent → empty). Unsafe entries are
// dropped; a manifest that exists but cannot be read is an error, because
// without it pcbpilot cannot tell its files from the user's.
func readFabManifest(outDir string) (fabManifest, error) {
	var m, clean fabManifest
	p := filepath.Join(outDir, fabManifestName)
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return clean, nil
	}
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil {
		return clean, fmt.Errorf("%s: %w (fix: delete it, and the outputs of the run that wrote it, then rerun)", p, err)
	}
	for _, n := range m.Files {
		if safeBase(n) {
			clean.Files = append(clean.Files, n)
		}
	}
	for _, n := range m.Gerbers {
		if safeBase(n) {
			clean.Gerbers = append(clean.Gerbers, n)
		}
	}
	return clean, nil
}

// isLinkedDir reports a directory entry that redirects elsewhere: a symlink,
// a Windows junction / mount point (Go reports those as ModeIrregular), or
// another redirecting reparse point (fab_link_windows.go).
func isLinkedDir(path string, st os.FileInfo) bool {
	return st.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || isRedirectingReparsePoint(path, st)
}

// checkFabOutDirs refuses an OUT or OUT/gerber that is a link: removing
// "owned" files through it would delete files outside OUT.
func checkFabOutDirs(outDir string) error {
	for _, d := range []string{outDir, filepath.Join(outDir, "gerber")} {
		st, err := os.Lstat(d)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if isLinkedDir(d, st) {
			return fmt.Errorf("%s is a symlink/junction; pcbpilot only writes into a real directory (fix: pass the link's target as --out, or remove the link)", d)
		}
		if !st.IsDir() {
			return fmt.Errorf("%w: %s exists and is not a directory (fix: move it away or choose another --out)", ErrFabCollision, d)
		}
	}
	return nil
}

func isRegular(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode().IsRegular()
}

func nameSet(list []string) map[string]bool {
	m := map[string]bool{}
	for _, n := range list {
		m[n] = true
	}
	return m
}

// fabCollisions lists the destinations this run would write that exist and
// are not a regular file the previous manifest owns.
func fabCollisions(outDir string, prev fabManifest, files, gerbers []string) []string {
	var out []string
	check := func(dir string, owned map[string]bool, names []string) {
		for _, n := range names {
			p := filepath.Join(dir, n)
			if _, err := os.Lstat(p); err == nil && !(owned[n] && isRegular(p)) {
				out = append(out, p)
			}
		}
	}
	check(outDir, nameSet(prev.Files), files)
	check(filepath.Join(outDir, "gerber"), nameSet(prev.Gerbers), gerbers)
	if p := filepath.Join(outDir, fabManifestName); !isRegular(p) {
		if _, err := os.Lstat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// foreignFabFiles lists Gerber/drill-looking files in OUT/gerber that no
// earlier pcbpilot run recorded and this run does not write: user files, or
// leftovers of a pcbpilot version without a manifest. They stay, unzipped.
// (One this run would write is a collision, reported by fabCollisions.)
func foreignFabFiles(outDir string, prev fabManifest, gerbers []string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(outDir, "gerber"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	owned, mine := nameSet(prev.Gerbers), nameSet(gerbers)
	var out []string
	for _, e := range ents {
		if !e.IsDir() && !owned[e.Name()] && !mine[e.Name()] && isFabFile(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// publishFab installs the staged run as one transaction:
//  1. every existing file the previous manifest owns (including the
//     manifest) is moved into stage/backup — nothing is deleted yet;
//  2. the new files are moved into OUT and OUT/gerber (onto absent paths);
//  3. the new manifest is written in the stage and moved in last.
//
// On any failure the installed files go back to the stage and the backups
// back to OUT, so OUT holds the previous set again. The caller deletes the
// stage (and with it the backups) unless keep is true: a rollback that
// itself failed leaves the previous files there, named in the error.
// fabCollisions must have returned nothing for the same arguments.
func publishFab(outDir, stage string, prev fabManifest, files, gerbers []string) (keep bool, err error) {
	gdir := filepath.Join(outDir, "gerber")
	bak := filepath.Join(stage, "backup")
	for _, d := range []string{gdir, filepath.Join(bak, "gerber")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return false, err
		}
	}
	type move struct{ from, to string }
	var backedUp, installed []move
	defer func() {
		if err == nil {
			return
		}
		var failed []string
		for i := len(installed) - 1; i >= 0; i-- {
			if e := fabRename(installed[i].to, installed[i].from); e != nil {
				failed = append(failed, e.Error())
			}
		}
		for i := len(backedUp) - 1; i >= 0; i-- {
			if e := fabRename(backedUp[i].to, backedUp[i].from); e != nil {
				failed = append(failed, e.Error())
			}
		}
		if len(failed) > 0 {
			keep = true
			err = fmt.Errorf("%w; restoring the previous files failed (%s) — they are in %s", err, strings.Join(failed, "; "), bak)
		} else {
			err = fmt.Errorf("%w (the previous files in %s were restored)", err, outDir)
		}
	}()
	mv := func(list *[]move, from, to string) error {
		if err := fabRename(from, to); err != nil {
			return err
		}
		*list = append(*list, move{from, to})
		return nil
	}
	for _, n := range prev.Gerbers {
		if p := filepath.Join(gdir, n); isRegular(p) {
			if err := mv(&backedUp, p, filepath.Join(bak, "gerber", n)); err != nil {
				return false, err
			}
		}
	}
	for _, n := range append(append([]string{}, prev.Files...), fabManifestName) {
		if p := filepath.Join(outDir, n); isRegular(p) {
			if err := mv(&backedUp, p, filepath.Join(bak, n)); err != nil {
				return false, err
			}
		}
	}
	for _, n := range gerbers {
		if err := mv(&installed, filepath.Join(stage, "gerber", n), filepath.Join(gdir, n)); err != nil {
			return false, err
		}
	}
	for _, n := range files {
		if err := mv(&installed, filepath.Join(stage, n), filepath.Join(outDir, n)); err != nil {
			return false, err
		}
	}
	b, err := json.MarshalIndent(fabManifest{Files: files, Gerbers: gerbers}, "", "  ")
	if err != nil {
		return false, err
	}
	staged := filepath.Join(stage, fabManifestName)
	if err := os.WriteFile(staged, append(b, '\n'), 0o644); err != nil {
		return false, err
	}
	if err := mv(&installed, staged, filepath.Join(outDir, fabManifestName)); err != nil {
		return false, err
	}
	return false, nil
}
