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
//  2. at commit: the complete write/delete set is checked against the
//     record, the --discard-external-edits checkpoint (every baseline
//     project file) is taken and the manifest of this commit is hashed from
//     the staged bytes, never from the live files after publication; as
//     the very last step a re-snapshot that differs from the baseline in
//     any file or lock (a GUI save, KiCad opening the project) aborts with
//     nothing replaced.
//
// Paths are compared case-insensitively where the file system is (Windows,
// macOS). A window of milliseconds between the re-snapshot and the file
// copies remains (KiCad offers no lock pcbpilot could take).

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// test seams: I/O errors (fail closed) and saves at the racy points
var (
	sbReadFile          = os.ReadFile
	sbReadDir           = os.ReadDir
	sbTestBeforeCommit  func(out string) // after generation and gates, before phase 2
	sbTestAfterPublish  func(out string) // after the files are copied into --out
	sbTestScanFile      func(rel string) // after each file a snapshot read
	sbTestDuringPrepare func(out string) // after the discard checkpoint and staged hashing, before the live re-check
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
	name = sbFoldName(name)
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
	if strings.HasSuffix(sbFoldName(rel), ".kicad_pro") {
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
			ln, lrel := sbFoldName(n), sbFoldName(rel) // classification ignores case where the FS does
			r := n
			if rel != "" {
				r = rel + "/" + n
			}
			if e.IsDir() {
				if strings.HasPrefix(ln, ".") || strings.HasSuffix(ln, "-backups") {
					continue
				}
				if err := walk(r); err != nil {
					return err
				}
				continue
			}
			if sbKicadLock(ln) {
				s.Locks = append(s.Locks, r)
				continue
			}
			mod := strings.HasSuffix(ln, ".kicad_mod") && strings.HasSuffix(lrel, ".pretty")
			if !sbProjectFile(n) && !mod {
				continue
			}
			b, err := sbReadFile(filepath.Join(dir, n))
			if err != nil {
				return fmt.Errorf("read %s: %w", filepath.Join(dir, n), err)
			}
			s.Files[r] = sbContentHash(r, b)
			if sbTestScanFile != nil {
				sbTestScanFile(r)
			}
			if strings.HasSuffix(ln, ".kicad_sch") {
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

// sbFoldCase: the file system ignores case (Windows, macOS default), so
// MYLIB.kicad_sym and a generated mylib.kicad_sym are the same file.
var sbFoldCase = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// sbFoldName lower-cases a name for classification where the FS ignores case.
func sbFoldName(n string) string {
	if sbFoldCase {
		return strings.ToLower(n)
	}
	return n
}

// sbPathKey is a path's identity on this file system; reports keep the
// original spelling.
func sbPathKey(p string) string {
	if sbFoldCase {
		return strings.ToLower(p)
	}
	return p
}

// sbKeyed re-keys a path → hash map by identity, remembering the spelling.
func sbKeyed(m map[string]string) (hashes, names map[string]string) {
	hashes, names = map[string]string{}, map[string]string{}
	for f, h := range m {
		k := sbPathKey(f)
		hashes[k], names[k] = h, f
	}
	return hashes, names
}

// sbBasis is the record of what pcbpilot last wrote.
type sbBasis struct {
	Name   string
	Hashes map[string]string
	Owned  []string
	// NoRecord: --out holds no pcbpilot build at all; only files the build
	// would overwrite conflict.
	NoRecord bool
	// LegacyReports: the record predates report tracking (sbState.Reports);
	// pcbpilot's own report files count as owned.
	LegacyReports bool
}

// sbReportNames are the reports sch-build writes into --out besides the
// project (render/*.svg too).
var sbReportNames = []string{"erc.json", "connectivity.json", "values.json", "intent-spec.json", "intent.json", "intent.md"}

func sbIsReport(f string) bool {
	f = sbFoldName(f)
	return sbContains(sbReportNames, f) || strings.HasPrefix(f, "render/")
}

// sbKicadLock: KiCad's lock files are ~<file>.lck; another *.lck is not one.
func sbKicadLock(name string) bool {
	name = sbFoldName(name)
	return strings.HasPrefix(name, "~") && strings.HasSuffix(name, ".lck")
}

// sbGuardCheckpoints lists the checkpoints for the comparison; unlike
// listSbCheckpoints it fails on anything it cannot read.
func sbGuardCheckpoints(out string) ([]sbCheckpointMeta, error) {
	ents, err := sbReadDir(sbCheckpointDir(out))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []sbCheckpointMeta
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(sbCheckpointDir(out), e.Name(), "checkpoint.json")
		b, err := sbReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("checkpoint %s: %w", e.Name(), err)
		}
		var m sbCheckpointMeta
		if err := json.Unmarshal(b, &m); err != nil || m.N <= 0 {
			return nil, fmt.Errorf("checkpoint %s: unreadable %s", e.Name(), p)
		}
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].N < list[j].N })
	return list, nil
}

// sbCheckpointHashes hashes checkpoint n's copies; a copy that is missing,
// unreadable or names an unsafe path fails: a partial checkpoint would make
// the files it lacks look unchanged.
func sbCheckpointHashes(out string, meta sbCheckpointMeta) (map[string]string, error) {
	dir := filepath.Join(sbCheckpointDir(out), strconv.Itoa(meta.N))
	m := map[string]string{}
	for _, f := range meta.Files {
		p, err := sbSafeJoin(dir, f)
		if err != nil {
			return nil, fmt.Errorf("checkpoint %d: %w", meta.N, err)
		}
		b, err := sbReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("checkpoint %d: %w", meta.N, err)
		}
		m[f] = sbContentHash(f, b)
	}
	return m, nil
}

// sbLoadBasis: the state manifest, else (a project built before manifests)
// the newest checkpoint the files still match — a restored older
// checkpoint is not an edit — else the latest checkpoint, else nothing.
func sbLoadBasis(out string, st *sbState, snap *sbSnapshot) (*sbBasis, error) {
	if st != nil && st.Manifest != nil {
		return &sbBasis{Name: "manifest", Hashes: st.Manifest, Owned: append(append([]string(nil), st.Files...), st.Reports...), LegacyReports: st.Reports == nil}, nil
	}
	list, err := sbGuardCheckpoints(out)
	if err != nil {
		return nil, err
	}
	var latest *sbBasis
	for i := len(list) - 1; i >= 0; i-- {
		h, err := sbCheckpointHashes(out, list[i])
		if err != nil {
			return nil, err
		}
		b := &sbBasis{Name: "checkpoint " + strconv.Itoa(list[i].N), Hashes: h, Owned: list[i].Files, LegacyReports: true}
		if latest == nil {
			latest = b
		}
		if !sbDiff(snap, b, nil).any() {
			return b, nil
		}
	}
	if latest != nil {
		return latest, nil
	}
	b := &sbBasis{Name: "none", Hashes: map[string]string{}, NoRecord: st == nil, LegacyReports: st != nil}
	if st != nil {
		b.Owned = append(append([]string(nil), st.Files...), st.Reports...)
	}
	return b, nil
}

// sbDiff compares a snapshot with the record. touch is the complete set this
// build writes or deletes (nil before generation: the files last owned).
// Paths are compared by identity (sbPathKey).
func sbDiff(snap *sbSnapshot, b *sbBasis, touch []string) *sbExternalEdits {
	x := &sbExternalEdits{Basis: b.Name, Locks: snap.Locks}
	cur, names := sbKeyed(snap.Files)
	rec, _ := sbKeyed(b.Hashes)
	owned := map[string]bool{}
	for _, f := range b.Owned {
		owned[sbPathKey(f)] = true
	}
	spell := func(k, f string) string {
		if n, ok := names[k]; ok {
			return n
		}
		return f
	}
	seen := map[string]bool{}
	check := func(f string) {
		k := sbPathKey(f)
		if seen[k] {
			return
		}
		seen[k] = true
		c, has := cur[k]
		r, inRec := rec[k]
		if b.LegacyReports && sbIsReport(f) {
			owned[k] = true
		}
		switch {
		case has && !owned[k]:
			x.Overwrites = append(x.Overwrites, spell(k, f))
		case has && inRec && c != r:
			x.Changed = append(x.Changed, spell(k, f))
		case !has && inRec && owned[k] && !sbIsReport(f): // a deleted report is no edit
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
		for k, n := range names {
			if _, ok := rec[k]; !ok && !seen[k] {
				x.Added = append(x.Added, n)
			}
		}
		if b.Name == "none" { // a pcbpilot state with nothing to compare with
			x.Added = append(x.Added, x.Overwrites...)
			x.Overwrites = nil
			for k, n := range names {
				if owned[k] && !sbContains(x.Added, n) {
					x.Added = append(x.Added, n)
				}
			}
		}
	}
	for _, l := range []*[]string{&x.Changed, &x.Added, &x.Removed, &x.Overwrites} {
		sort.Strings(*l)
	}
	return x
}

// sbMoved lists what differs between two snapshots of the same --out.
func sbMoved(a, b *sbSnapshot) []string {
	ah, an := sbKeyed(a.Files)
	bh, bn := sbKeyed(b.Files)
	var moved []string
	for k, h := range bh {
		if ah[k] != h {
			moved = append(moved, bn[k])
		}
	}
	for k := range ah {
		if _, ok := bh[k]; !ok {
			moved = append(moved, an[k])
		}
	}
	sort.Strings(moved)
	return moved
}

// sbGuard carries the baseline from before generation to the commit.
type sbGuard struct {
	out      string
	o        sbOptions
	basis    *sbBasis
	baseline *sbSnapshot
	base     *sbSnapshot // baseline + existing write/delete targets (beforeCommit)
}

func sbUnreadable(out string, err error) *sbFailure {
	return &sbFailure{Code: "PROJECT_UNREADABLE", Message: "cannot check " + out + " for edits made outside pcbpilot: " + err.Error(),
		Fix: "nothing was written; fix the file permissions (or close the program holding the file), or move a damaged checkpoint out of .pcbpilot/checkpoints, and re-run"}
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
	basis, err := sbLoadBasis(out, st, snap)
	if err != nil {
		return nil, sbUnreadable(out, err)
	}
	g := &sbGuard{out: out, o: o, baseline: snap, basis: basis}
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
// the files of the last build this one no longer writes. The slow parts
// (the discard checkpoint, hashing the staged files) come first; the live
// re-check of every file and lock is the last step before publication. It
// returns the manifest to record after the commit.
func (g *sbGuard) beforeCommit(stage string, files, deletes []string, rep *sbReport) (map[string]string, *sbFailure) {
	if sbTestBeforeCommit != nil {
		sbTestBeforeCommit(g.out)
	}
	touch := append(append([]string(nil), files...), deletes...)
	// every write/delete target on its own, whatever the project-file
	// filter thinks of its name: an existing one joins the baseline
	extra, err := sbReadTargets(g.out, g.baseline, touch)
	if err != nil {
		return nil, sbUnreadable(g.out, err)
	}
	base := &sbSnapshot{Files: map[string]string{}, Locks: g.baseline.Locks}
	for f, h := range g.baseline.Files {
		base.Files[f] = h
	}
	for f, h := range extra {
		base.Files[f] = h
	}
	g.base = base
	x := sbDiff(base, g.basis, touch)
	x.Locks = nil
	if x.any() {
		rep.External = x
		if !g.o.DiscardExternal {
			return nil, g.refuse(x)
		}
		n, err := saveSbCheckpoint(g.out, base.paths(), "before-discard-external-edits")
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
	written := map[string]bool{}
	for _, f := range touch {
		written[sbPathKey(f)] = true
	}
	for f, h := range base.Files {
		if !written[sbPathKey(f)] {
			man[f] = h
		}
	}
	if sbTestDuringPrepare != nil {
		sbTestDuringPrepare(g.out)
	}
	// last step before publication: nothing live changed since the baseline
	now, err := sbTakeSnapshot(g.out)
	if err != nil {
		return nil, sbUnreadable(g.out, err)
	}
	if len(now.Locks) > 0 {
		return nil, sbLockedByKicad(g.out, now.Locks)
	}
	again, err := sbReadTargets(g.out, now, touch)
	if err != nil {
		return nil, sbUnreadable(g.out, err)
	}
	for f, h := range again {
		now.Files[f] = h
	}
	if moved := sbMoved(base, now); len(moved) > 0 {
		msg := g.out + " changed while sch-build ran: " + strings.Join(moved, ", ")
		if x.Checkpoint > 0 {
			msg += fmt.Sprintf(" (checkpoint %d was taken first and is kept)", x.Checkpoint)
		}
		return nil, &sbFailure{Code: "CONCURRENT_CHANGE", Message: msg,
			Fix: "nothing was written; if KiCad has the project open, save and close it there, then re-run"}
	}
	return man, nil
}

// sbReadTargets hashes the write/delete targets that exist in --out but are
// not in snap (a name the project-file filter does not know). An entry that
// cannot be read (a directory, no permission) fails closed.
func sbReadTargets(out string, snap *sbSnapshot, targets []string) (map[string]string, error) {
	known, _ := sbKeyed(snap.Files)
	extra := map[string]string{}
	for _, f := range targets {
		if _, ok := known[sbPathKey(f)]; ok {
			continue
		}
		p, err := sbSafeJoin(out, f)
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(p); os.IsNotExist(err) {
			continue
		}
		b, err := sbReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		extra[f] = sbContentHash(f, b)
	}
	return extra, nil
}

// publish replaces the targets with the staged files and removes deletes,
// recoverably: each existing target is first moved into a per-run backup
// under .pcbpilot and its hash compared with the checked baseline (a save
// after the final snapshot read that file). On a mismatch or an error
// everything already replaced is put back, the backup is kept and the
// commit aborts; after success the backup is removed.
func (g *sbGuard) publish(stage string, targets, deletes []string) *sbFailure {
	backup := filepath.Join(sbMetaDir(g.out), "publish-backup-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	base, _ := sbKeyed(g.base.Files)
	type done struct {
		f        string
		had, put bool
	}
	var log []done
	rollback := func(why string) *sbFailure {
		for i := len(log) - 1; i >= 0; i-- {
			d := log[i]
			dst, _ := sbSafeJoin(g.out, d.f)
			if d.put {
				_ = os.Remove(dst)
			}
			if d.had {
				src, _ := sbSafeJoin(backup, d.f)
				_ = os.Rename(src, dst)
			}
		}
		return &sbFailure{Code: "CONCURRENT_CHANGE", Message: g.out + " changed while sch-build published it: " + why + "; every file was put back",
			Fix: "nothing was written (a file that could not be put back stays in " + backup + "); if KiCad has the project open, save and close it there, then re-run"}
	}
	step := func(f string, write bool) *sbFailure {
		dst, err := sbSafeJoin(g.out, f)
		if err != nil {
			return rollback(err.Error())
		}
		d := done{f: f}
		want, inBase := base[sbPathKey(f)]
		if _, err := os.Lstat(dst); err == nil {
			bk, _ := sbSafeJoin(backup, f)
			if err := os.MkdirAll(filepath.Dir(bk), 0o755); err != nil {
				return rollback(err.Error())
			}
			if err := os.Rename(dst, bk); err != nil {
				return rollback(f + ": " + err.Error())
			}
			d.had = true
			log = append(log, d)
			b, err := sbReadFile(bk)
			if err != nil {
				return rollback(f + ": " + err.Error())
			}
			if !inBase || sbContentHash(f, b) != want {
				return rollback(f + " was saved after the final check")
			}
		} else if inBase {
			return rollback(f + " was removed after the final check")
		} else {
			log = append(log, d)
		}
		if !write {
			return nil
		}
		src, _ := sbSafeJoin(stage, f)
		b, err := os.ReadFile(src)
		if err != nil {
			return rollback(err.Error())
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return rollback(err.Error())
		}
		tmp := dst + ".pcbpilot-tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return rollback(err.Error())
		}
		log[len(log)-1].put = true
		if err := os.Rename(tmp, dst); err != nil {
			_ = os.Remove(tmp)
			return rollback(err.Error())
		}
		return nil
	}
	for _, f := range deletes {
		if fail := step(f, false); fail != nil {
			return fail
		}
	}
	for _, f := range targets {
		if fail := step(f, true); fail != nil {
			return fail
		}
	}
	_ = os.RemoveAll(backup)
	return nil
}
