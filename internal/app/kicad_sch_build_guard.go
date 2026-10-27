package app

// kicad_sch_build_guard.go — refuses to regenerate a project over edits made
// outside pcbpilot. sch-edit and a sch-build into an existing --out redraw
// every sheet from the stored spec; a line drawn in the KiCad GUI after the
// last build was silently lost (field report 2026-10-26). Every commit now
// records sha256 of the project files in --out (state.json "manifest"); the
// next in-place run compares the files on disk with it and refuses on any
// difference unless --discard-external-edits is given, which checkpoints the
// current files first.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// sbExternalEdits is what changed in --out since pcbpilot last wrote it.
type sbExternalEdits struct {
	Basis   string   `json:"basis"` // manifest | checkpoint N | none
	Changed []string `json:"changed,omitempty"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Locks   []string `json:"kicadLocks,omitempty"`
	// Checkpoint holds the files as they were before --discard-external-edits
	// overwrote them.
	Checkpoint int `json:"checkpoint,omitempty"`
}

func (x *sbExternalEdits) any() bool {
	return len(x.Changed)+len(x.Added)+len(x.Removed) > 0
}

func (x *sbExternalEdits) summary() string {
	var parts []string
	for _, g := range []struct {
		k string
		v []string
	}{{"changed", x.Changed}, {"added", x.Added}, {"removed", x.Removed}} {
		if len(g.v) > 0 {
			parts = append(parts, g.k+" "+strings.Join(g.v, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

// sbProjectFile reports whether a top-level name in --out is a KiCad project
// file an edit could live in. KiCad's side files are not: .kicad_prl (view
// state), ~*.lck (open-file locks), _autosave-*, *-bak, the -backups dir.
func sbProjectFile(name string) bool {
	if strings.HasPrefix(name, "~") || strings.HasPrefix(name, "_autosave-") || strings.HasPrefix(name, "#") {
		return false
	}
	switch name {
	case "sym-lib-table", "fp-lib-table":
		return true
	}
	switch filepath.Ext(name) {
	case ".kicad_sch", ".kicad_pro", ".kicad_sym":
		return true
	}
	return false
}

// sbScanProject lists the project files in --out (slash paths: top-level
// sheets, project, libraries and lib tables, footprints in top-level *.pretty)
// and KiCad's lock files.
func sbScanProject(out string) (files, locks []string) {
	ents, err := os.ReadDir(out)
	if err != nil {
		return nil, nil
	}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() {
			if strings.HasSuffix(n, ".pretty") {
				mods, _ := os.ReadDir(filepath.Join(out, n))
				for _, m := range mods {
					if !m.IsDir() && strings.HasSuffix(m.Name(), ".kicad_mod") {
						files = append(files, n+"/"+m.Name())
					}
				}
			}
			continue
		}
		if strings.HasSuffix(n, ".lck") {
			locks = append(locks, n)
			continue
		}
		if sbProjectFile(n) {
			files = append(files, n)
		}
	}
	sort.Strings(files)
	sort.Strings(locks)
	return files, locks
}

func sbFileSHA(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), true
}

// sbMakeManifest hashes the project files in --out right after a commit:
// the files this build owns plus every other project file present (an
// existing file is then not reported as "added" by the next run).
func sbMakeManifest(out string, owned []string) map[string]string {
	m := map[string]string{}
	scanned, _ := sbScanProject(out)
	for _, f := range append(append([]string(nil), owned...), scanned...) {
		if p, err := sbSafeJoin(out, f); err == nil {
			if h, ok := sbFileSHA(p); ok {
				m[f] = h
			}
		}
	}
	return m
}

// sbDiffAgainst compares --out with a recorded set of hashes. Only owned
// files count as changed or removed (a build rewrites or recreates them); a
// project file the record does not know counts as added.
func sbDiffAgainst(out string, owned []string, hashes map[string]string, scanned []string, x *sbExternalEdits) {
	for _, f := range owned {
		want, ok := hashes[f]
		if !ok {
			continue
		}
		p, err := sbSafeJoin(out, f)
		if err != nil {
			continue
		}
		if got, ok := sbFileSHA(p); !ok {
			x.Removed = append(x.Removed, f)
		} else if got != want {
			x.Changed = append(x.Changed, f)
		}
	}
	for _, f := range scanned {
		if _, ok := hashes[f]; !ok {
			x.Added = append(x.Added, f)
		}
	}
	sort.Strings(x.Changed)
	sort.Strings(x.Removed)
}

// sbCheckpointHashes hashes checkpoint n's copies of its files.
func sbCheckpointHashes(out string, meta sbCheckpointMeta) map[string]string {
	dir := filepath.Join(sbCheckpointDir(out), strconv.Itoa(meta.N))
	m := map[string]string{}
	for _, f := range meta.Files {
		if p, err := sbSafeJoin(dir, f); err == nil {
			if h, ok := sbFileSHA(p); ok {
				m[f] = h
			}
		}
	}
	return m
}

// sbDetectExternalEdits compares --out with what pcbpilot last wrote there:
// the state manifest, else (a project built before manifests) the newest
// checkpoint whose files still match — a restored older checkpoint is not
// an edit — else nothing to compare with (basis "none": every project file
// present is reported as added). nil: --out holds no project yet.
func sbDetectExternalEdits(out string, st *sbState) *sbExternalEdits {
	scanned, locks := sbScanProject(out)
	list, _ := listSbCheckpoints(out)
	if st == nil && len(list) == 0 && len(scanned) == 0 {
		return nil
	}
	x := &sbExternalEdits{Locks: locks}
	if st != nil && st.Manifest != nil {
		x.Basis = "manifest"
		sbDiffAgainst(out, st.Files, st.Manifest, scanned, x)
		return x
	}
	for i := len(list) - 1; i >= 0; i-- {
		var y sbExternalEdits
		sbDiffAgainst(out, list[i].Files, sbCheckpointHashes(out, list[i]), scanned, &y)
		if !y.any() || i == len(list)-1 {
			x.Basis = "checkpoint " + strconv.Itoa(list[i].N)
			x.Changed, x.Added, x.Removed = y.Changed, y.Added, y.Removed
		}
		if !y.any() {
			break
		}
	}
	if x.Basis == "" {
		x.Basis = "none"
		x.Added = scanned
		if st != nil {
			for _, f := range st.Files { // owned but gone
				if p, err := sbSafeJoin(out, f); err == nil && !fileExists(p) {
					x.Removed = append(x.Removed, f)
				}
			}
		}
	}
	return x
}

// sbGuardExternalEdits runs before an in-place build writes anything. It
// returns a refusal, or nil to go on; with --discard-external-edits it
// records what will be overwritten and checkpoints the current files first.
func sbGuardExternalEdits(out string, st *sbState, o sbOptions, rep *sbReport) *sbFailure {
	x := sbDetectExternalEdits(out, st)
	if x == nil {
		return nil
	}
	if len(x.Locks) > 0 && o.DryRun {
		rep.Warnings = append(rep.Warnings, "KiCad has the project open (lock "+strings.Join(x.Locks, ", ")+"): a real run is refused until it is closed")
	} else if len(x.Locks) > 0 {
		rep.External = x
		return &sbFailure{Code: "KICAD_OPEN",
			Message: fmt.Sprintf("KiCad has %s open (lock file %s)", out, strings.Join(x.Locks, ", ")),
			Fix:     "nothing was written; save and close the project in KiCad, then re-run (a lock file left by a KiCad that is no longer running can be deleted)"}
	}
	if !x.any() {
		return nil
	}
	rep.External = x
	what := x.summary()
	if x.Basis == "none" {
		what = "no record of what pcbpilot wrote here (no manifest, no checkpoint); project files present: " + what
	}
	if o.DryRun {
		rep.Warnings = append(rep.Warnings, "external edits ("+x.Basis+"): "+what+" — a real run is refused without --discard-external-edits")
		return nil
	}
	if !o.DiscardExternal {
		return &sbFailure{Code: "EXTERNAL_EDIT",
			Message: fmt.Sprintf("%s was edited outside pcbpilot since its last build (compared with %s): %s", out, x.Basis, what),
			Fix: "nothing was written. Keep the edits: `pcbpilot kicad sch-read --project " + out + " --out edited.json` imports them into a spec you can build into a new --out; " +
				"or build the stored spec into a new --out. To overwrite them, re-run with --discard-external-edits (the current files are checkpointed first)"}
	}
	files := append([]string(nil), x.Added...)
	if st != nil {
		for _, f := range st.Files {
			if !sbContains(files, f) {
				files = append(files, f)
			}
		}
	}
	sort.Strings(files)
	n, err := saveSbCheckpoint(out, files, "before-discard-external-edits")
	if err != nil {
		return &sbFailure{Code: "CHECKPOINT_FAILED", Message: "checkpoint before discarding external edits: " + err.Error(),
			Fix: "nothing was written; free disk space or fix permissions on " + sbCheckpointDir(out) + " and re-run"}
	}
	x.Checkpoint = n
	rep.Warnings = append(rep.Warnings, fmt.Sprintf("--discard-external-edits: overwriting edits made outside pcbpilot (%s); changed and removed files are rewritten from the spec, added files stay unless the build writes the same name. The files before this run are checkpoint %d: pcbpilot kicad sch-checkpoint restore %d --project %s",
		what, n, n, out))
	return nil
}
