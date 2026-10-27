package app

// kicad_sch_build_guard.go — refuses to regenerate a project over edits made
// outside pcbpilot. sch-edit and a sch-build into an existing --out redraw
// every sheet from the stored spec; a line drawn in the KiCad GUI after the
// last build was silently lost (field report 2026-10-26).
//
// KiCad does not honour pcbpilot's lock, so the check is two-phase:
//  1. before generation: snapshot every project file + KiCad lock in --out
//     (the baseline) and compare it with what pcbpilot last wrote (the
//     state manifest, else a checkpoint) — fail fast;
//  2. right before commit: re-snapshot; any difference from the baseline
//     (a GUI save, a new lock) aborts with nothing replaced; then the
//     complete write/delete set is checked against the record, and the
//     manifest of this commit is hashed from the staged bytes, never from
//     the live files after publication.
//
// --discard-external-edits overwrites conflicts after checkpointing every
// project file of the baseline. A window of milliseconds between phase 2
// and the file copies remains (KiCad offers no lock pcbpilot could take).

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// test seams: I/O errors (fail closed) and saves at the racy points
var (
	sbReadFile         = os.ReadFile
	sbReadDir          = os.ReadDir
	sbTestBeforeCommit func(out string) // after generation and gates, before phase 2
	sbTestAfterPublish func(out string) // after the files are copied into --out
)

// sbExternalEdits is what changed in --out since pcbpilot last wrote it.
type sbExternalEdits struct {
	Basis   string   `json:"basis"` // manifest | checkpoint N | none
	Changed []string `json:"changed,omitempty"`
	Added   []string `json:"added,omitempty"`
	Removed []string `json:"removed,omitempty"`
	// Overwrites: existing files pcbpilot did not write that this build
	// would replace or delete (e.g. a user library that becomes an output).
	Overwrites []string `json:"overwrites,omitempty"`
	Locks      []string `json:"kicadLocks,omitempty"`
	// Checkpoint holds every project file as it was before
	// --discard-external-edits overwrote them.
	Checkpoint int `json:"checkpoint,omitempty"`
}

func (x *sbExternalEdits) any() bool {
	return len(x.Changed)+len(x.Added)+len(x.Removed)+len(x.Overwrites) > 0
}

func (x *sbExternalEdits) summary() string {
	var parts []string
	for _, g := range []struct {
		k string
		v []string
	}{{"changed", x.Changed}, {"added", x.Added}, {"removed", x.Removed}, {"not written by pcbpilot, would be overwritten", x.Overwrites}} {
		if len(g.v) > 0 {
			parts = append(parts, g.k+" "+strings.Join(g.v, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

// onlyProjectFile: the one conflict is a .kicad_pro edit.
func (x *sbExternalEdits) onlyProjectFile() bool {
	return len(x.Added)+len(x.Removed)+len(x.Overwrites) == 0 && len(x.Changed) == 1 && strings.HasSuffix(x.Changed[0], ".kicad_pro")
}

// sbProjectFile reports whether a file name is a KiCad project file an edit
// could live in. KiCad's side files are not: .kicad_prl (view state, also
// written by kicad-cli), ~*.lck (open-file locks), _autosave-*, *-bak.
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

// sbProjectVolatile are .kicad_pro fields KiCad rewrites as UI state; they
// are left out of its hash. Verified on KiCad 10.0.6: kicad-cli (netlist,
// ERC, SVG, sch upgrade) leaves .kicad_pro byte-identical (it writes only
// .kicad_prl); a pcbnew load+save re-serializes it and adds every default
// section, among them these export-dialog paths and viewport lists. The
// defaults it adds (net classes, board setup …) stay protected: they cannot
// be told apart from settings a user changed.
var sbProjectVolatile = map[string][]string{"pcbnew": {"last_paths"}, "board": {"viewports", "3dviewports"}}

// sbContentHash hashes a project file; .kicad_pro as canonical JSON (key
// order and whitespace do not count) without the volatile fields.
func sbContentHash(rel string, b []byte) string {
	if strings.HasSuffix(rel, ".kicad_pro") {
		var v map[string]any
		if json.Unmarshal(b, &v) == nil {
			for sec, keys := range sbProjectVolatile {
				if m, ok := v[sec].(map[string]any); ok {
					for _, k := range keys {
						delete(m, k)
					}
					if len(m) == 0 {
						delete(v, sec)
					}
				}
			}
			if c, err := json.Marshal(v); err == nil {
				b = c
			}
		}
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// sbSnapshot is the project files (slash path → content hash) and KiCad lock
// files found in --out.
type sbSnapshot struct {
	Files map[string]string
	Locks []string
}

var sbSheetFileRe = regexp.MustCompile(`\(property\s+"Sheetfile"\s+"([^"]+)"`)

// sbTakeSnapshot walks --out recursively (hierarchical sheets and libraries
// may live in sub-directories), skipping dot directories (.pcbpilot, .git,
// KiCad's .history) and KiCad's *-backups. Locks are also looked for beside
// every sheet referenced from outside --out. Any read error fails closed.
func sbTakeSnapshot(out string) (*sbSnapshot, error) {
	s := &sbSnapshot{Files: map[string]string{}}
	if _, err := os.Stat(out); os.IsNotExist(err) {
		return s, nil
	}
	var refs []string // absolute paths of sheets referenced by sheets
	var walk func(rel string) error
	walk = func(rel string) error {
		dir := filepath.Join(out, filepath.FromSlash(rel))
		ents, err := sbReadDir(dir)
		if err != nil {
			return fmt.Errorf("read %s: %w", dir, err)
		}
		for _, e := range ents {
			n := e.Name()
			r := n
			if rel != "" {
				r = rel + "/" + n
			}
			if e.IsDir() {
				if strings.HasPrefix(n, ".") || strings.HasSuffix(n, "-backups") {
					continue
				}
				if err := walk(r); err != nil {
					return err
				}
				continue
			}
			if strings.HasSuffix(n, ".lck") {
				s.Locks = append(s.Locks, r)
				continue
			}
			mod := strings.HasSuffix(n, ".kicad_mod") && strings.HasSuffix(rel, ".pretty")
			if !sbProjectFile(n) && !mod {
				continue
			}
			b, err := sbReadFile(filepath.Join(dir, n))
			if err != nil {
				return fmt.Errorf("read %s: %w", filepath.Join(dir, n), err)
			}
			s.Files[r] = sbContentHash(r, b)
			if strings.HasSuffix(n, ".kicad_sch") {
				for _, m := range sbSheetFileRe.FindAllSubmatch(b, -1) {
					refs = append(refs, filepath.Join(dir, filepath.FromSlash(string(m[1]))))
				}
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	for _, p := range refs {
		if r, err := filepath.Rel(out, p); err == nil && !strings.HasPrefix(r, "..") && !filepath.IsAbs(r) {
			continue // inside --out: already walked
		}
		lck := filepath.Join(filepath.Dir(p), "~"+filepath.Base(p)+".lck")
		if _, err := os.Stat(lck); err == nil {
			s.Locks = append(s.Locks, lck)
		}
	}
	sort.Strings(s.Locks)
	return s, nil
}

func (s *sbSnapshot) paths() []string {
	var ps []string
	for p := range s.Files {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}

// sbBasis is the record of what pcbpilot last wrote.
type sbBasis struct {
	Name   string
	Hashes map[string]string
	Owned  []string
	// NoRecord: --out holds no pcbpilot build at all; only files the build
	// would overwrite conflict.
	NoRecord bool
}

func sbCheckpointHashes(out string, meta sbCheckpointMeta) map[string]string {
	dir := filepath.Join(sbCheckpointDir(out), strconv.Itoa(meta.N))
	m := map[string]string{}
	for _, f := range meta.Files {
		if p, err := sbSafeJoin(dir, f); err == nil {
			if b, err := sbReadFile(p); err == nil {
				m[f] = sbContentHash(f, b)
			}
		}
	}
	return m
}

// sbLoadBasis: the state manifest, else (a project built before manifests)
// the newest checkpoint the files still match — a restored older
// checkpoint is not an edit — else the latest checkpoint, else nothing.
func sbLoadBasis(out string, st *sbState, snap *sbSnapshot) *sbBasis {
	if st != nil && st.Manifest != nil {
		return &sbBasis{Name: "manifest", Hashes: st.Manifest, Owned: st.Files}
	}
	list, _ := listSbCheckpoints(out)
	var latest *sbBasis
	for i := len(list) - 1; i >= 0; i-- {
		b := &sbBasis{Name: "checkpoint " + strconv.Itoa(list[i].N), Hashes: sbCheckpointHashes(out, list[i]), Owned: list[i].Files}
		if latest == nil {
			latest = b
		}
		if !sbDiff(snap, b, nil).any() {
			return b
		}
	}
	if latest != nil {
		return latest
	}
	b := &sbBasis{Name: "none", Hashes: map[string]string{}, NoRecord: st == nil}
	if st != nil {
		b.Owned = st.Files
	}
	return b
}

// sbDiff compares a snapshot with the record. touch is the complete set this
// build writes or deletes (nil before generation: the files last owned).
func sbDiff(snap *sbSnapshot, b *sbBasis, touch []string) *sbExternalEdits {
	x := &sbExternalEdits{Basis: b.Name, Locks: snap.Locks}
	owned := map[string]bool{}
	for _, f := range b.Owned {
		owned[f] = true
	}
	seen := map[string]bool{}
	check := func(f string) {
		if seen[f] {
			return
		}
		seen[f] = true
		cur, has := snap.Files[f]
		rec, inRec := b.Hashes[f]
		switch {
		case has && !owned[f]:
			x.Overwrites = append(x.Overwrites, f)
		case has && inRec && cur != rec:
			x.Changed = append(x.Changed, f)
		case !has && inRec && owned[f]:
			x.Removed = append(x.Removed, f)
		}
	}
	for _, f := range touch {
		check(f)
	}
	for _, f := range b.Owned {
		check(f) // rewritten, or deleted when no longer generated
	}
	if !b.NoRecord {
		for _, f := range snap.paths() {
			if _, ok := b.Hashes[f]; !ok && !seen[f] {
				x.Added = append(x.Added, f)
			}
		}
		if b.Name == "none" { // a pcbpilot state with nothing to compare with
			x.Added = append(x.Added, x.Overwrites...)
			x.Overwrites = nil
			for f := range snap.Files {
				if owned[f] && !sbContains(x.Added, f) {
					x.Added = append(x.Added, f)
				}
			}
		}
	}
	for _, l := range []*[]string{&x.Changed, &x.Added, &x.Removed, &x.Overwrites} {
		sort.Strings(*l)
	}
	return x
}

// sbGuard carries the baseline from before generation to the commit.
type sbGuard struct {
	out      string
	o        sbOptions
	basis    *sbBasis
	baseline *sbSnapshot
}

func sbUnreadable(out string, err error) *sbFailure {
	return &sbFailure{Code: "PROJECT_UNREADABLE", Message: "cannot check " + out + " for edits made outside pcbpilot: " + err.Error(),
		Fix: "nothing was written; fix the file permissions (or close the program holding the file) and re-run"}
}

func sbLockedByKicad(out string, locks []string) *sbFailure {
	return &sbFailure{Code: "KICAD_OPEN",
		Message: fmt.Sprintf("KiCad has %s open (lock file %s)", out, strings.Join(locks, ", ")),
		Fix:     "nothing was written; save and close the project in KiCad, then re-run (a lock file left by a KiCad that is no longer running can be deleted)"}
}

func (g *sbGuard) refuse(x *sbExternalEdits) *sbFailure {
	what := x.summary()
	if x.Basis == "none" {
		what = "no record of what pcbpilot wrote here (no manifest, no checkpoint); project files present: " + what
	}
	fix := "nothing was written. Keep the edits: `pcbpilot kicad sch-read --project " + g.out + " --out edited.json` imports them into a spec you can build into a new --out; " +
		"or build the stored spec into a new --out. To overwrite them, re-run with --discard-external-edits (every project file is checkpointed first)"
	if x.onlyProjectFile() {
		fix = "nothing was written. Only " + x.Changed[0] + " changed: KiCad may rewrite it with all its default settings when it saves the project (a board save does). " +
			"If you changed no project settings in KiCad (net classes, text variables, board setup), --discard-external-edits is safe (the file is checkpointed first); " +
			"otherwise import the edits with `pcbpilot kicad sch-read --project " + g.out + "` into a new --out"
	}
	return &sbFailure{Code: "EXTERNAL_EDIT",
		Message: fmt.Sprintf("%s was edited outside pcbpilot since its last build (compared with %s): %s", g.out, x.Basis, what), Fix: fix}
}

// sbGuardStart is phase 1 (under sbLock, before generation): the baseline
// and a fail-fast comparison with the record.
func sbGuardStart(out string, st *sbState, o sbOptions, rep *sbReport) (*sbGuard, *sbFailure) {
	snap, err := sbTakeSnapshot(out)
	if err != nil {
		return nil, sbUnreadable(out, err)
	}
	g := &sbGuard{out: out, o: o, baseline: snap, basis: sbLoadBasis(out, st, snap)}
	x := sbDiff(snap, g.basis, nil)
	if len(x.Locks) > 0 {
		if o.DryRun {
			rep.Warnings = append(rep.Warnings, "KiCad has the project open (lock "+strings.Join(x.Locks, ", ")+"): a real run is refused until it is closed")
		} else {
			rep.External = x
			return nil, sbLockedByKicad(out, x.Locks)
		}
	}
	if !x.any() {
		return g, nil
	}
	rep.External = x
	switch {
	case o.DryRun:
		rep.Warnings = append(rep.Warnings, "external edits ("+x.Basis+"): "+x.summary()+" — a real run is refused without --discard-external-edits")
	case !o.DiscardExternal:
		return nil, g.refuse(x)
	}
	return g, nil
}

// beforeCommit is phase 2. files are the staged outputs (in stage), deletes
// the files of the last build this one no longer writes. It returns the
// manifest to record after the commit.
func (g *sbGuard) beforeCommit(stage string, files, deletes []string, rep *sbReport) (map[string]string, *sbFailure) {
	if sbTestBeforeCommit != nil {
		sbTestBeforeCommit(g.out)
	}
	now, err := sbTakeSnapshot(g.out)
	if err != nil {
		return nil, sbUnreadable(g.out, err)
	}
	if len(now.Locks) > 0 {
		return nil, sbLockedByKicad(g.out, now.Locks)
	}
	var moved []string
	for f, h := range now.Files {
		if g.baseline.Files[f] != h {
			moved = append(moved, f)
		}
	}
	for f := range g.baseline.Files {
		if _, ok := now.Files[f]; !ok {
			moved = append(moved, f)
		}
	}
	if len(moved) > 0 {
		sort.Strings(moved)
		return nil, &sbFailure{Code: "CONCURRENT_CHANGE", Message: g.out + " changed while sch-build ran: " + strings.Join(moved, ", "),
			Fix: "nothing was written; if KiCad has the project open, save and close it there, then re-run"}
	}
	touch := append(append([]string(nil), files...), deletes...)
	x := sbDiff(g.baseline, g.basis, touch)
	x.Locks = nil
	if x.any() {
		rep.External = x
		if !g.o.DiscardExternal {
			return nil, g.refuse(x)
		}
		n, err := saveSbCheckpoint(g.out, g.baseline.paths(), "before-discard-external-edits")
		if err != nil {
			return nil, &sbFailure{Code: "CHECKPOINT_FAILED", Message: "checkpoint before discarding external edits: " + err.Error(),
				Fix: "nothing was written; free disk space or fix permissions on " + sbCheckpointDir(g.out) + " and re-run"}
		}
		x.Checkpoint = n
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("--discard-external-edits: overwriting edits made outside pcbpilot (%s); changed, removed and overwritten files are rewritten from the spec, added files stay unless the build writes the same name. Every project file before this run is checkpoint %d: pcbpilot kicad sch-checkpoint restore %d --project %s",
			x.summary(), n, n, g.out))
	}
	// the manifest comes from the staged bytes: a save after publication is
	// an edit the next run sees
	man := map[string]string{}
	for _, f := range files {
		p, err := sbSafeJoin(stage, f)
		if err != nil {
			return nil, sbUnreadable(stage, err)
		}
		b, err := sbReadFile(p)
		if err != nil {
			return nil, sbUnreadable(stage, err)
		}
		man[f] = sbContentHash(f, b)
	}
	for f, h := range g.baseline.Files {
		if _, ok := man[f]; !ok && !sbContains(deletes, f) {
			man[f] = h
		}
	}
	return man, nil
}
