package kfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Kind is what a Change does to its path.
type Kind string

const (
	KindCreate  Kind = "create"
	KindReplace Kind = "replace"
	KindDelete  Kind = "delete"
)

// Change is one file of a transaction.
type Change struct {
	Path    string      `json:"path"`
	Kind    Kind        `json:"kind"`
	Base    Snapshot    `json:"base"`
	NewSHA  string      `json:"newSha,omitempty"`
	staged  string      // temp file beside Path ("" for a delete)
	mode    os.FileMode // the file's mode before the transaction
	hasMode bool
}

// Plan is what Apply would do, identified by Revision.
type Plan struct {
	Revision string     `json:"revision"`
	Changes  []Change   `json:"changes"`
	Locks    []LockInfo `json:"locks,omitempty"`
	// Applied is set by Apply once every change has taken effect, even when
	// the cleanup afterwards reports an error.
	Applied bool `json:"applied,omitempty"`
}

// Txn is a set of file changes applied all-or-nothing. Root is the project
// directory: the writer lock, the backups and the journal live under
// Root/.pcbpilot/.
type Txn struct {
	root    string
	changes map[string]*Change
	created []string // directories Stage created
}

// NewTxn starts a transaction for the project directory root.
func NewTxn(root string) *Txn {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &Txn{root: root, changes: map[string]*Change{}}
}

// Stage records data as the new content of path. base is the snapshot the
// data was computed from (nil: the file as it is now). The bytes are written
// to a temp file beside path right away (creating path's directory if
// needed); staging a path again replaces the earlier content.
func (t *Txn) Stage(path string, data []byte, base *Snapshot) error {
	abs, b, err := t.base(path, base)
	if err != nil {
		return err
	}
	created, err := mkdirTracked(filepath.Dir(abs))
	if err != nil {
		return err
	}
	t.created = append(t.created, created...)
	perm, err := targetPerm(abs, 0, false)
	if err != nil {
		return err
	}
	f, err := createTemp(abs)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return werr
	}
	kind := KindCreate
	if b.Exists {
		kind = KindReplace
	}
	h := sha256.Sum256(data)
	t.put(&Change{Path: abs, Kind: kind, Base: b, NewSHA: hex.EncodeToString(h[:]), staged: f.Name(), mode: perm, hasMode: true})
	return nil
}

// StageFile stages a copy of src (for example a file a KiCad helper wrote to
// a stage directory) as the new content of path.
func (t *Txn) StageFile(path, src string, base *Snapshot) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return t.Stage(path, data, base)
}

// Delete records the removal of path.
func (t *Txn) Delete(path string, base *Snapshot) error {
	abs, b, err := t.base(path, base)
	if err != nil {
		return err
	}
	if !b.Exists {
		return fmt.Errorf("delete %s: file does not exist", abs)
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	t.put(&Change{Path: abs, Kind: KindDelete, Base: b, mode: st.Mode().Perm(), hasMode: true})
	return nil
}

func (t *Txn) base(path string, base *Snapshot) (string, Snapshot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", Snapshot{}, err
	}
	if t.reserved(abs) {
		return "", Snapshot{}, fmt.Errorf("%s is pcbpilot's own transaction state (writer lock, journal or backups) and cannot be part of a transaction", abs)
	}
	if err := checkRegular(abs); err != nil {
		return "", Snapshot{}, err
	}
	var b Snapshot
	if base != nil {
		b = *base
	} else if b, err = Stat(abs); err != nil {
		return "", Snapshot{}, err
	}
	b.Path = abs
	return abs, b, nil
}

// reserved is true for the writer lock and anything inside the txn
// directory, compared with the platform's case rules and, where the files
// exist, by identity (os.SameFile), so an alias cannot slip through.
func (t *Txn) reserved(abs string) bool {
	lock, tdir := WriterLockPath(t.root), txnDir(t.root)
	if samePath(abs, lock) || within(abs, tdir) {
		return true
	}
	if sameFile(abs, lock) {
		return true
	}
	for d := abs; ; {
		if sameFile(d, tdir) {
			return true
		}
		up := filepath.Dir(d)
		if up == d {
			return false
		}
		d = up
	}
}

func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

// samePath compares cleaned paths, ignoring case where the usual
// filesystems do (Windows, macOS).
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func within(p, dir string) bool {
	if samePath(p, dir) {
		return true
	}
	rel := filepath.Clean(dir) + string(filepath.Separator)
	if len(p) < len(rel) {
		return false
	}
	return samePath(p[:len(rel)-1], dir) && os.IsPathSeparator(p[len(rel)-1])
}

func (t *Txn) put(c *Change) {
	if old := t.changes[c.Path]; old != nil && old.staged != "" {
		os.Remove(old.staged)
	}
	t.changes[c.Path] = c
}

func (t *Txn) sorted() []*Change {
	cs := make([]*Change, 0, len(t.changes))
	for _, c := range t.changes {
		cs = append(cs, c)
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Path < cs[j].Path })
	return cs
}

// Plan lists the changes in path order with the locks currently on them. The
// Revision is the sha256 of each (path relative to root, base sha, new sha),
// so the same edit of the same files always has the same revision. The error
// is a lock check that could not be completed.
func (t *Txn) Plan(env *Env) (Plan, error) {
	cs := t.sorted()
	p := Plan{Changes: make([]Change, len(cs))}
	h := sha256.New()
	paths := make([]string, len(cs))
	for i, c := range cs {
		p.Changes[i] = *c
		paths[i] = c.Path
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00", t.rel(c.Path), c.Base.SHA256, c.NewSHA)
	}
	p.Revision = hex.EncodeToString(h.Sum(nil))
	locks, err := CheckLocks(paths, env)
	p.Locks = locks
	return p, err
}

func (t *Txn) rel(p string) string {
	if r, err := filepath.Rel(t.root, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(p)
}

// Abort removes the staged temp files.
func (t *Txn) Abort() {
	for _, c := range t.changes {
		if c.staged != "" {
			os.Remove(c.staged)
		}
	}
	t.changes = map[string]*Change{}
}

// journal is Root/.pcbpilot/txn/journal.json while a transaction runs.
type journal struct {
	Schema   string         `json:"schema"`
	Revision string         `json:"revision"`
	Phase    string         `json:"phase"` // prepare: no target touched yet | commit
	PID      int            `json:"pid"`
	Host     string         `json:"host"`
	Created  time.Time      `json:"created"`
	Changes  []journalEntry `json:"changes"`
}

type journalEntry struct {
	Path    string      `json:"path"`
	Kind    Kind        `json:"kind"`
	Existed bool        `json:"existed"`
	Mode    os.FileMode `json:"mode"`
	HasMode bool        `json:"hasMode,omitempty"` // Mode is known (0 is a real mode then)
	BaseSHA string      `json:"baseSha,omitempty"`
	NewSHA  string      `json:"newSha,omitempty"`
	Backup  string      `json:"backup,omitempty"` // relative to the txn dir
	Staged  string      `json:"staged,omitempty"`
}

func (e journalEntry) base() Snapshot {
	return Snapshot{Path: e.Path, Exists: e.Existed, SHA256: e.BaseSHA}
}

const journalSchema = "pcbpilot.kfile.txn/1"

func txnDir(root string) string      { return filepath.Join(root, ".pcbpilot", "txn") }
func journalPath(root string) string { return filepath.Join(txnDir(root), "journal.json") }

// Test hooks: beforeMutate runs just before each target is touched,
// beforeBackup before each backup copy, beforeRestore before a rollback or
// recover step changes a file.
var beforeMutate, beforeBackup, beforeRestore func(path string)

// Apply commits the staged changes.
//
// It holds the project writer lock while it runs (o.Writer when the caller
// already holds it; otherwise it acquires one). wantRev, when not empty,
// must equal the current Plan().Revision (E_REVISION_MISMATCH otherwise). It
// refuses with E_TXN_PENDING while a journal exists. Immediately before each
// file is touched — and before every retry — the KiCad locks (o.Locks,
// o.NoLockCheck, o.Env) and the file's base hash are checked again
// (E_KICAD_LOCKED, E_FILE_CHANGED). o.Base and o.Perm are unused.
//
// Order on disk: journal (prepare) → backups → fsync of backups and their
// directories → journal (commit) → each target in path order, each followed
// by a directory fsync → journal removed → backups removed. When a step
// fails, every target already touched, including the one that failed, is
// restored from its backup if it still holds what this transaction wrote.
// When even that fails the journal is kept and the error is E_TXN_PENDING.
func (t *Txn) Apply(wantRev string, o Opts) (Plan, error) {
	plan, err := t.Plan(o.Env)
	if err != nil {
		return plan, err
	}
	if wantRev != "" && wantRev != plan.Revision {
		return plan, &Error{Code: CodeRevision, Path: t.root,
			Detail: fmt.Sprintf("plan revision %s, but the files now give %s", short(wantRev), short(plan.Revision)),
			Fix:    "the files changed after the plan was made; make a new plan and review it"}
	}
	cs := t.sorted()
	if len(cs) == 0 {
		return plan, nil
	}
	release, err := writerFor(o.Writer, t.root, o.Env)
	if err != nil {
		return plan, err
	}
	defer release()
	if err := Pending(t.root); err != nil {
		return plan, err
	}

	check := func(e journalEntry) func() error {
		return func() error {
			if !o.NoLockCheck {
				locks, err := Guard([]string{e.Path}, o.Locks, o.Env)
				plan.Locks = mergeLocks(plan.Locks, locks)
				if err != nil {
					return err
				}
			}
			if err := checkRegular(e.Path); err != nil {
				return err
			}
			if err := e.base().Check(); err != nil {
				return err
			}
			return stagedOK(e)
		}
	}
	dir := txnDir(t.root)
	bdir := filepath.Join(dir, plan.Revision[:16])
	j := journal{Schema: journalSchema, Revision: plan.Revision, Phase: "prepare", PID: os.Getpid(),
		Host: envOr(o.Env).Host, Created: time.Now().UTC()}
	for i, c := range cs {
		e := journalEntry{Path: c.Path, Kind: c.Kind, Existed: c.Base.Exists, Mode: c.mode, HasMode: c.hasMode,
			BaseSHA: c.Base.SHA256, NewSHA: c.NewSHA, Staged: c.staged}
		if c.Base.Exists {
			e.Backup = filepath.ToSlash(filepath.Join(plan.Revision[:16], fmt.Sprintf("%03d-%s", i, filepath.Base(c.Path))))
		}
		if err := check(e)(); err != nil {
			return plan, err
		}
		j.Changes = append(j.Changes, e)
	}

	// Prepare: nothing outside .pcbpilot/txn is touched until the journal
	// says "commit", and it says so only after the backups are durable.
	// Backups may hold private files: the txn tree is 0700, files 0600.
	if err := os.MkdirAll(bdir, 0o700); err != nil {
		return plan, err
	}
	for _, d := range []string{dir, bdir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return plan, err
		}
	}
	if err := createJournal(t.root, j); err != nil {
		return plan, err
	}
	abandon := func(err error) (Plan, error) {
		if cerr := finish(t.root, &j); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return plan, err
	}
	for _, e := range j.Changes {
		if e.Backup == "" {
			continue
		}
		if beforeBackup != nil {
			beforeBackup(e.Path)
		}
		sha, err := copyFile(filepath.Join(dir, filepath.FromSlash(e.Backup)), e.Path)
		if err != nil {
			return abandon(wrapf(err, "back up %s", e.Path))
		}
		if sha != e.BaseSHA {
			return abandon(changedErr(e.Path, fmt.Sprintf("changed while it was being backed up (sha256 %s → %s)", short(e.BaseSHA), short(sha))))
		}
	}
	syncs := []string{bdir, dir, filepath.Dir(dir), t.root}
	for _, e := range j.Changes {
		syncs = append(syncs, filepath.Dir(e.Path)) // staged temp entries
	}
	// Directories Stage created: each one's entry lives in its parent,
	// flushed bottom-up to the first directory that already existed.
	created := append([]string(nil), t.created...)
	sort.SliceStable(created, func(a, b int) bool { return len(created[a]) > len(created[b]) })
	for _, d := range created {
		syncs = append(syncs, filepath.Dir(d))
	}
	if err := syncDirs(syncs...); err != nil {
		return abandon(err)
	}
	for _, e := range j.Changes {
		if err := stagedOK(e); err != nil {
			return abandon(err)
		}
	}
	j.Phase = "commit"
	if err := writeJournal(t.root, j); err != nil {
		return abandon(err)
	}

	for i, e := range j.Changes {
		if beforeMutate != nil {
			beforeMutate(e.Path)
		}
		var err error
		if e.Kind == KindDelete {
			err = removeRetry(e.Path, check(e))
		} else {
			// The staged file gets the target's mode only now; then
			// chmod, flush, rename, flush the directory.
			if err = chmodFile(e.Staged, e.Mode); err == nil {
				if err = syncFile(e.Staged); err == nil {
					err = renameRetry(e.Staged, e.Path, check(e))
				}
			}
		}
		if err == nil {
			err = syncDir(filepath.Dir(e.Path))
		}
		if err == nil {
			continue
		}
		if _, rerr := undoAll(t.root, j.Changes[:i+1], true, guardOpts{o.Locks, o.NoLockCheck, o.Env}); rerr != nil {
			return plan, &Error{Code: CodeTxnPending, Path: e.Path, Err: errors.Join(err, rerr),
				Detail: fmt.Sprintf("apply failed (%v) and rollback failed (%v); the journal is kept", err, rerr),
				Fix:    "close KiCad and other programs using the project, then run `pcbpilot kicad txn recover` (or `discard` to keep the files as they are)"}
		}
		t.Abort()
		if ferr := settle(j.Changes[:i+1], guardOpts{o.Locks, o.NoLockCheck, o.Env}); ferr != nil {
			return plan, keepJournal(t.root, errors.Join(err, ferr))
		}
		return abandon(wrapf(err, "apply %s (all files restored)", e.Path))
	}
	plan.Applied = true
	t.changes = map[string]*Change{}
	return plan, finish(t.root, &j)
}

// entryState is how a journalled path compares with the transaction.
type entryState int

const (
	stateOurs  entryState = iota // holds what the transaction wrote (or removed)
	stateBase                    // still as before the transaction
	stateOther                   // changed by someone else
)

func entryStateOf(e journalEntry) (entryState, Snapshot, error) {
	cur, err := Stat(e.Path)
	if err != nil {
		return stateOther, cur, err
	}
	switch {
	case e.Kind == KindDelete && !cur.Exists:
		return stateOurs, cur, nil
	case e.Kind != KindDelete && cur.Exists && cur.SHA256 == e.NewSHA:
		return stateOurs, cur, nil
	case cur.Exists == e.Existed && (!cur.Exists || cur.SHA256 == e.BaseSHA):
		return stateBase, cur, nil
	}
	return stateOther, cur, nil
}

// guardOpts are the KiCad lock rules for rollback and recover writes.
type guardOpts struct {
	locks  BreakPolicy
	noLock bool
	env    *Env
}

func (g guardOpts) check(path string) error {
	if g.noLock {
		return nil
	}
	_, err := Guard([]string{path}, g.locks, g.env)
	return err
}

// undo returns e.Path to its pre-transaction content and mode, but only when
// it still holds what the transaction wrote; a file already at its base is
// left alone and anything else is a conflict — unless failed is set: the
// entry whose own step just failed may hold someone else's edit that made
// it fail (the step was refused before touching it), which is left alone.
// KiCad locks are checked before every attempt to change the file.
func undo(root string, e journalEntry, failed bool, g guardOpts) (bool, error) {
	st, cur, err := entryStateOf(e)
	if err != nil {
		return false, err
	}
	switch st {
	case stateBase:
		return fixMode(e, g)
	case stateOther:
		if failed {
			return false, nil
		}
		return false, changedErr(e.Path, fmt.Sprintf("holds content (sha256 %s) that is neither the transaction's nor the original; left as is", short(cur.SHA256)))
	}
	if beforeRestore != nil {
		beforeRestore(e.Path)
	}
	if err := checkRegular(e.Path); err != nil {
		return false, err
	}
	if !e.Existed {
		ours := func() error {
			if err := g.check(e.Path); err != nil {
				return err
			}
			if s, _, err := entryStateOf(e); err != nil || s != stateOurs {
				return changedErr(e.Path, "changed during rollback")
			}
			return nil
		}
		if err := removeRetry(e.Path, ours); err != nil {
			return false, err
		}
		return true, syncDir(filepath.Dir(e.Path))
	}
	b, err := os.ReadFile(filepath.Join(txnDir(root), filepath.FromSlash(e.Backup)))
	if err != nil {
		return false, wrapf(err, "restore %s", e.Path)
	}
	if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != e.BaseSHA {
		return false, fmt.Errorf("restore %s: backup %s does not match the recorded hash", e.Path, e.Backup)
	}
	// WriteFile checks the locks before creating its temp file and before
	// every rename attempt.
	if _, err := WriteFile(e.Path, b, Opts{NoLockCheck: g.noLock, Locks: g.locks, Env: g.env, Base: &cur, Perm: e.Mode, PermExact: e.HasMode}); err != nil {
		return false, err
	}
	_, err = fixMode(e, g)
	return true, err
}

// undoAll undoes entries in reverse order and returns the paths restored.
// lastFailed marks the last entry as the one whose step failed. A KiCad
// lock stops it at once: nothing more is changed and the journal stays.
func undoAll(root string, entries []journalEntry, lastFailed bool, g guardOpts) ([]string, error) {
	var restored []string
	var errs []error
	for i := len(entries) - 1; i >= 0; i-- {
		did, err := undo(root, entries[i], lastFailed && i == len(entries)-1, g)
		if did {
			restored = append(restored, entries[i].Path)
		}
		if err != nil {
			errs = append(errs, err)
			if CodeOf(err) == CodeLocked {
				break
			}
		}
	}
	sort.Strings(restored)
	return restored, errors.Join(errs...)
}

// fixMode gives an existing entry its recorded mode when the file is in
// the transaction's state or its original state (in both the recorded mode
// is the right final mode) — e.g. after a crash between restoring the bytes
// and the chmod, or a lost read-only attribute. It re-checks the KiCad lock
// and the file's state first, flushes after the chmod and verifies the
// result. An entry without a recorded mode is left alone.
func fixMode(e journalEntry, g guardOpts) (bool, error) {
	if !e.HasMode {
		return false, nil
	}
	st0, cur, err := entryStateOf(e)
	if err != nil || st0 == stateOther || !cur.Exists {
		return false, err
	}
	fi, err := os.Lstat(e.Path)
	if err != nil || modeEq(fi.Mode(), e.Mode) {
		return false, err
	}
	if err := g.check(e.Path); err != nil {
		return false, err
	}
	if err := checkRegular(e.Path); err != nil {
		return false, err
	}
	if st1, _, err := entryStateOf(e); err != nil || st1 != st0 {
		return false, changedErr(e.Path, "changed while its mode was being restored")
	}
	if err := chmodFile(e.Path, e.Mode); err != nil {
		return false, err
	}
	if err := syncFile(e.Path); err != nil {
		return true, err
	}
	if fi, err := os.Lstat(e.Path); err != nil || !modeEq(fi.Mode(), e.Mode) {
		return true, fmt.Errorf("%s: mode is still not %v", e.Path, e.Mode)
	}
	return true, nil
}

// settle verifies and repairs the recorded final mode of every entry, then
// makes every entry durable: the last step before a journal may go.
func settle(entries []journalEntry, g guardOpts) error {
	for _, e := range entries {
		if _, err := fixMode(e, g); err != nil {
			return err
		}
	}
	return flushTargets(entries)
}

// flushTargets makes every journalled path durable as it is now (file
// data and metadata, then its directory) before a journal is dropped.
func flushTargets(entries []journalEntry) error {
	var dirs []string
	for _, e := range entries {
		_, err := os.Stat(e.Path)
		switch {
		case err == nil:
			if err := syncFile(e.Path); err != nil {
				return wrapf(err, "flush %s", e.Path)
			}
		case !errors.Is(err, fs.ErrNotExist):
			return err
		}
		dirs = append(dirs, filepath.Dir(e.Path))
	}
	return syncDirs(dirs...)
}

// keepJournal is the error for a transaction whose files could not be made
// durable: the journal and backups stay for the next recover.
func keepJournal(root string, err error) error {
	return &Error{Code: CodeTxnPending, Path: journalPath(root), Err: err,
		Detail: "the files could not be flushed, so the journal and backups are kept: " + err.Error(),
		Fix:    "run `pcbpilot kicad txn recover` again"}
}

// syncFile flushes a file's data and metadata; it works on read-only files.
var syncFile = platformSyncFile

// stagedOK re-hashes a staged file: it must still hold the bytes that were
// staged (nothing replaced it while it waited in the target directory).
func stagedOK(e journalEntry) error {
	if e.Kind == KindDelete {
		return nil
	}
	b, err := os.ReadFile(e.Staged)
	if err != nil {
		return wrapf(err, "staged content for %s", e.Path)
	}
	if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != e.NewSHA {
		return changedErr(e.Path, fmt.Sprintf("its staged content %s was modified (sha256 %s → %s)",
			filepath.Base(e.Staged), short(e.NewSHA), short(hex.EncodeToString(h[:]))))
	}
	return nil
}

// mkdirTracked is os.MkdirAll that returns the directories it had to
// create, deepest first.
func mkdirTracked(dir string) ([]string, error) {
	var missing []string
	for d := dir; ; {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, d)
		up := filepath.Dir(d)
		if up == d {
			break
		}
		d = up
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return missing, nil
}

// finish ends a transaction on disk: staged leftovers, then the journal (so
// a crash from here on leaves no journal pointing at missing backups), then
// the backups. A journal that cannot be removed is E_TXN_PENDING.
func finish(root string, j *journal) error {
	var errs []error
	for _, e := range j.Changes {
		if e.Staged != "" {
			if err := os.Remove(e.Staged); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	if err := removeRetry(journalPath(root), nil); err != nil {
		return &Error{Code: CodeTxnPending, Path: journalPath(root), Err: err,
			Detail: "the transaction finished but its journal could not be removed: " + err.Error(),
			Fix:    "run `pcbpilot kicad txn recover`; it recognises a finished transaction and only cleans up"}
	}
	// The journal's removal must be durable before its backups go; if that
	// cannot be confirmed the backups stay (orphaned backups are harmless).
	if err := syncDir(txnDir(root)); err != nil {
		return wrapf(errors.Join(append(errs, err)...), "transaction cleanup (backups kept)")
	}
	if len(j.Revision) >= 16 {
		if err := os.RemoveAll(filepath.Join(txnDir(root), j.Revision[:16])); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return wrapf(err, "transaction cleanup")
	}
	return nil
}

func createJournal(root string, j journal) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(journalPath(root), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return Pending(root)
	}
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if serr := f.Sync(); werr == nil {
		werr = serr
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(journalPath(root))
	}
	return werr
}

func writeJournal(root string, j journal) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	_, err = WriteFile(journalPath(root), b, Opts{NoLockCheck: true})
	return err
}

func readJournal(root string) (*journal, error) {
	b, err := os.ReadFile(journalPath(root))
	if err != nil {
		return nil, err
	}
	var j journal
	if err := json.Unmarshal(b, &j); err != nil || j.Schema != journalSchema {
		if err == nil {
			err = fmt.Errorf("schema %q", j.Schema)
		}
		return nil, &Error{Code: CodeTxnPending, Path: journalPath(root), Err: err,
			Detail: "the transaction journal is unreadable: " + err.Error(),
			Fix:    "inspect the project files and .pcbpilot/txn, then run `pcbpilot kicad txn discard --force`"}
	}
	return &j, nil
}

// Pending returns E_TXN_PENDING when root has a transaction journal: a
// pcbpilot process is applying, or one was interrupted.
func Pending(root string) error {
	p := journalPath(root)
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var j journal
	_ = json.Unmarshal(b, &j)
	return &Error{Code: CodeTxnPending, Path: p,
		Detail: fmt.Sprintf("a transaction (revision %s, pid %d on %s, %s) is running or was interrupted", short(j.Revision), j.PID, j.Host, j.Created.Format(time.RFC3339)),
		Fix:    "run `pcbpilot kicad txn recover` to restore the files from before it, or `pcbpilot kicad txn discard` to keep them as they are"}
}

// RecoverOpts controls Recover and Discard.
type RecoverOpts struct {
	// Writer is the project writer lock when the caller holds it; otherwise
	// it is acquired (so a transaction still running blocks recovery).
	Writer *Writer
	Env    *Env
	// Locks and NoLockCheck apply to the KiCad locks of the files restored.
	Locks       BreakPolicy
	NoLockCheck bool
	// Force accepts a journal written on another host, and lets Discard drop
	// an unreadable journal. The user's explicit decision.
	Force bool
}

// Recovery reports what Recover or Discard did.
type Recovery struct {
	Revision string `json:"revision,omitempty"`
	Phase    string `json:"phase,omitempty"`
	// Action: none (no journal) | cleaned (interrupted before any file was
	// touched) | completed (every change had landed) | rolled-back | discarded.
	Action    string   `json:"action"`
	Restored  []string `json:"restored,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

// Recover resolves an interrupted transaction of root. If no file was
// touched, or every change had already landed, it only cleans up. Otherwise
// it restores each file that still holds what the transaction wrote; files
// still at their original content are left alone. If any file holds other
// content (edited after the interruption), nothing is changed, the journal
// is kept and the error is E_FILE_CHANGED listing the conflicts.
func Recover(root string, o RecoverOpts) (Recovery, error) {
	root, j, release, rec, err := openJournal(root, o, false)
	if j == nil || err != nil {
		return rec, err
	}
	defer release()
	if j.Phase != "commit" {
		rec.Action = "cleaned"
		return rec, finish(root, j)
	}
	all := true
	var paths []string
	for _, e := range j.Changes {
		st, _, err := entryStateOf(e)
		if err != nil {
			return rec, err
		}
		if st == stateOther {
			rec.Conflicts = append(rec.Conflicts, e.Path)
		}
		all = all && st == stateOurs
		paths = append(paths, e.Path)
	}
	if len(rec.Conflicts) > 0 {
		return rec, &Error{Code: CodeChanged, Path: rec.Conflicts[0],
			Detail: "changed after the interrupted transaction, so recover would destroy that work: " + strings.Join(rec.Conflicts, ", "),
			Fix:    "inspect those files; `pcbpilot kicad txn discard` keeps every file as it is now"}
	}
	g := guardOpts{o.Locks, o.NoLockCheck, o.Env}
	if all {
		rec.Action = "completed"
		if err := settle(j.Changes, g); err != nil {
			return rec, keepJournal(root, err)
		}
		return rec, finish(root, j)
	}
	if !o.NoLockCheck { // preflight; undo checks again before each file
		if _, err := Guard(paths, o.Locks, o.Env); err != nil {
			return rec, err
		}
	}
	restored, err := undoAll(root, j.Changes, false, g)
	rec.Restored = restored
	if err != nil {
		return rec, &Error{Code: CodeTxnPending, Path: journalPath(root), Err: err,
			Detail: "recover incomplete, the journal is kept: " + err.Error(),
			Fix:    "close the programs holding the files and run recover again"}
	}
	if err := settle(j.Changes, g); err != nil {
		return rec, keepJournal(root, err)
	}
	rec.Action = "rolled-back"
	return rec, finish(root, j)
}

// Discard drops an interrupted transaction's journal and backups and keeps
// every file as it is now.
func Discard(root string, o RecoverOpts) (Recovery, error) {
	root, j, release, rec, err := openJournal(root, o, true)
	if err != nil {
		return rec, err
	}
	if release != nil {
		defer release()
	}
	if j == nil {
		if rec.Action == "discarded" { // unreadable journal, Force
			err = os.RemoveAll(txnDir(root))
		}
		return rec, err
	}
	// The files are kept as they are; make them durable before the journal
	// that could still explain them goes.
	if err := flushTargets(j.Changes); err != nil {
		return rec, keepJournal(root, err)
	}
	rec.Action = "discarded"
	return rec, finish(root, j)
}

// openJournal takes the writer lock and reads the journal, refusing a
// foreign-host journal without Force. j is nil when there is nothing to do;
// for discard an unreadable journal with Force yields j == nil and
// Action "discarded".
func openJournal(root string, o RecoverOpts, discard bool) (string, *journal, func(), Recovery, error) {
	rec := Recovery{Action: "none"}
	abs, err := filepath.Abs(root)
	if err != nil {
		return root, nil, nil, rec, err
	}
	release, err := writerFor(o.Writer, abs, o.Env)
	if err != nil {
		return abs, nil, nil, rec, err
	}
	j, err := readJournal(abs)
	if errors.Is(err, fs.ErrNotExist) {
		release()
		return abs, nil, nil, rec, nil
	}
	if err != nil {
		if discard && o.Force {
			rec.Action = "discarded"
			return abs, nil, release, rec, nil
		}
		release()
		return abs, nil, nil, rec, err
	}
	rec.Revision, rec.Phase = j.Revision, j.Phase
	if !sameHost(j.Host, envOr(o.Env).Host) && !o.Force {
		release()
		return abs, nil, nil, rec, &Error{Code: CodeTxnPending, Path: journalPath(abs),
			Detail: fmt.Sprintf("the transaction was written on host %q; its process may still be running there", j.Host),
			Fix:    "run recover on that host, or pass --force once you are sure it is not running"}
	}
	return abs, j, release, rec, nil
}

func syncDirs(dirs ...string) error {
	seen := map[string]bool{}
	for _, d := range dirs {
		if seen[d] {
			continue
		}
		seen[d] = true
		if err := syncDir(d); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies src to dst, flushes dst and returns the sha256 of the
// bytes copied.
func copyFile(dst, src string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if err := out.Chmod(0o600); err != nil { // a leftover from an earlier run
		out.Close()
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, h), in)
	if serr := out.Sync(); err == nil {
		err = serr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return hex.EncodeToString(h.Sum(nil)), err
}
