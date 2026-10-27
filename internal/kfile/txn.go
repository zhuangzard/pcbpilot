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
	Path   string   `json:"path"`
	Kind   Kind     `json:"kind"`
	Base   Snapshot `json:"base"`
	NewSHA string   `json:"newSha,omitempty"`
	staged string   // temp file beside Path ("" for a delete)
}

// Plan is what Apply would do, identified by Revision.
type Plan struct {
	Revision string     `json:"revision"`
	Changes  []Change   `json:"changes"`
	Locks    []LockInfo `json:"locks,omitempty"`
}

// Txn is a set of file changes applied all-or-nothing. Root is the project
// directory: the backups and the journal live under Root/.pcbpilot/txn/.
type Txn struct {
	root    string
	changes map[string]*Change
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
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	perm, err := targetPerm(abs, 0)
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
	if werr == nil {
		werr = os.Chmod(f.Name(), perm)
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
	t.put(&Change{Path: abs, Kind: kind, Base: b, NewSHA: hex.EncodeToString(h[:]), staged: f.Name()})
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
	t.put(&Change{Path: abs, Kind: KindDelete, Base: b})
	return nil
}

func (t *Txn) base(path string, base *Snapshot) (string, Snapshot, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
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
// so the same edit of the same files always has the same revision.
func (t *Txn) Plan(env *Env) Plan {
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
	p.Locks = CheckLocks(paths, env)
	return p
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
	Phase    string         `json:"phase"` // backup: nothing renamed yet | commit
	PID      int            `json:"pid"`
	Host     string         `json:"host"`
	Created  time.Time      `json:"created"`
	Changes  []journalEntry `json:"changes"`
}

type journalEntry struct {
	Path    string `json:"path"`
	Kind    Kind   `json:"kind"`
	Existed bool   `json:"existed"`
	BaseSHA string `json:"baseSha,omitempty"`
	NewSHA  string `json:"newSha,omitempty"`
	Backup  string `json:"backup,omitempty"` // relative to the txn dir
	Staged  string `json:"staged,omitempty"`
}

const journalSchema = "pcbpilot.kfile.txn/1"

func txnDir(root string) string      { return filepath.Join(root, ".pcbpilot", "txn") }
func journalPath(root string) string { return filepath.Join(txnDir(root), "journal.json") }

// Apply commits the staged changes. wantRev, when not empty, must equal the
// current Plan().Revision (E_REVISION_MISMATCH otherwise). It refuses with
// E_TXN_PENDING while a journal exists, with E_KICAD_LOCKED while a lock
// blocks (o.Locks, o.NoLockCheck, o.Env apply; o.Base and o.Perm are unused)
// and with E_FILE_CHANGED when a file no longer matches its base. If a rename
// fails, the files already replaced are restored from the backups. On
// success the staged files are consumed and the backups removed.
func (t *Txn) Apply(wantRev string, o Opts) (Plan, error) {
	plan := t.Plan(o.Env)
	if wantRev != "" && wantRev != plan.Revision {
		return plan, &Error{Code: CodeRevision, Path: t.root,
			Detail: fmt.Sprintf("plan revision %s, but the files now give %s", short(wantRev), short(plan.Revision)),
			Fix:    "the files changed after the plan was made; make a new plan and review it"}
	}
	if err := Pending(t.root); err != nil {
		return plan, err
	}
	cs := t.sorted()
	if len(cs) == 0 {
		return plan, nil
	}
	if !o.NoLockCheck {
		paths := make([]string, len(cs))
		for i, c := range cs {
			paths[i] = c.Path
		}
		locks, err := Guard(paths, o.Locks, o.Env)
		plan.Locks = locks
		if err != nil {
			return plan, err
		}
	}
	for _, c := range cs {
		if err := c.Base.Check(); err != nil {
			return plan, err
		}
	}

	// The journal is created exclusively: two writers of one project cannot
	// both get here.
	dir := txnDir(t.root)
	bdir := filepath.Join(dir, plan.Revision[:16])
	if err := os.MkdirAll(bdir, 0o755); err != nil {
		return plan, err
	}
	j := journal{Schema: journalSchema, Revision: plan.Revision, Phase: "backup", PID: os.Getpid(),
		Host: envOr(o.Env).Host, Created: time.Now().UTC()}
	for i, c := range cs {
		e := journalEntry{Path: c.Path, Kind: c.Kind, Existed: c.Base.Exists, BaseSHA: c.Base.SHA256,
			NewSHA: c.NewSHA, Staged: c.staged}
		if c.Base.Exists {
			e.Backup = filepath.ToSlash(filepath.Join(plan.Revision[:16], fmt.Sprintf("%03d-%s", i, filepath.Base(c.Path))))
		}
		j.Changes = append(j.Changes, e)
	}
	if err := createJournal(t.root, j); err != nil {
		return plan, err
	}
	cleanup := func() {
		os.RemoveAll(bdir)
		os.Remove(journalPath(t.root))
		os.Remove(dir) // only when empty
	}
	for _, e := range j.Changes {
		if e.Backup == "" {
			continue
		}
		if err := copyFile(filepath.Join(dir, filepath.FromSlash(e.Backup)), e.Path); err != nil {
			cleanup()
			return plan, wrapf(err, "back up %s", e.Path)
		}
	}
	j.Phase = "commit"
	if err := writeJournal(t.root, j); err != nil {
		cleanup()
		return plan, err
	}

	for i, e := range j.Changes {
		var err error
		if e.Kind == KindDelete {
			err = removeRetry(e.Path)
		} else {
			err = renameRetry(e.Staged, e.Path)
		}
		if err == nil {
			continue
		}
		if rerr := rollback(t.root, j.Changes[:i]); rerr != nil {
			return plan, &Error{Code: CodeTxnPending, Path: e.Path, Err: err,
				Detail: fmt.Sprintf("apply failed (%v) and rollback failed (%v); the journal is kept", err, rerr),
				Fix:    "close KiCad and other programs using the project, then run `pcbpilot kicad txn recover` (or `discard` to keep the files as they are)"}
		}
		t.Abort()
		cleanup()
		return plan, wrapf(err, "apply %s (all files restored)", e.Path)
	}
	for _, e := range j.Changes {
		syncDir(filepath.Dir(e.Path))
	}
	t.changes = map[string]*Change{}
	cleanup()
	return plan, nil
}

// rollback restores done (in reverse) to their state before the transaction.
func rollback(root string, done []journalEntry) error {
	var errs []error
	for i := len(done) - 1; i >= 0; i-- {
		if err := restore(root, done[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// restore puts e.Path back to its pre-transaction content: the backup when it
// existed; otherwise it is removed if it still holds what the transaction
// wrote.
func restore(root string, e journalEntry) error {
	if !e.Existed {
		now, err := Stat(e.Path)
		if err != nil || !now.Exists || now.SHA256 != e.NewSHA {
			return err
		}
		return removeRetry(e.Path)
	}
	if now, err := Stat(e.Path); err == nil && now.Exists && now.SHA256 == e.BaseSHA {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(txnDir(root), filepath.FromSlash(e.Backup)))
	if err != nil {
		return wrapf(err, "restore %s", e.Path)
	}
	_, err = WriteFile(e.Path, b, Opts{NoLockCheck: true})
	return err
}

func createJournal(root string, j journal) error {
	b, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(journalPath(root), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
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
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, &Error{Code: CodeTxnPending, Path: journalPath(root), Err: err,
			Detail: "the transaction journal is unreadable: " + err.Error(),
			Fix:    "inspect the files listed under .pcbpilot/txn, then run `pcbpilot kicad txn discard`"}
	}
	return &j, nil
}

// Pending returns E_TXN_PENDING when root has a transaction journal: another
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

// Recover undoes an interrupted transaction of root: every file it touched
// gets its content from before the transaction. It returns the paths
// restored. It refuses while the process that wrote the journal still runs
// on this machine. A root with no journal is a no-op.
func Recover(root string, env *Env) ([]string, error) {
	j, err := pendingJournal(root, env)
	if j == nil || err != nil {
		return nil, err
	}
	var restored []string
	if j.Phase == "commit" {
		var errs []error
		for i := len(j.Changes) - 1; i >= 0; i-- {
			e := j.Changes[i]
			before, _ := Stat(e.Path)
			if err := restore(root, e); err != nil {
				errs = append(errs, err)
				continue
			}
			if after, _ := Stat(e.Path); after != before {
				restored = append(restored, e.Path)
			}
		}
		if err := errors.Join(errs...); err != nil {
			return restored, &Error{Code: CodeTxnPending, Path: journalPath(root), Err: err,
				Detail: "recover incomplete: " + err.Error(), Fix: "close the programs holding the files and run recover again"}
		}
	}
	sort.Strings(restored)
	clearJournal(root, j)
	return restored, nil
}

// Discard drops an interrupted transaction's journal and backups and keeps
// the files as they are now.
func Discard(root string, env *Env) error {
	j, err := pendingJournal(root, env)
	if j == nil || err != nil {
		return err
	}
	clearJournal(root, j)
	return nil
}

func pendingJournal(root string, env *Env) (*journal, error) {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	j, err := readJournal(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	env = envOr(env)
	if j.PID != os.Getpid() && sameHost(j.Host, env.Host) && pidAlive(env.Procs, j.PID) {
		return nil, &Error{Code: CodeTxnPending, Path: journalPath(root),
			Detail: fmt.Sprintf("pid %d is still applying this transaction", j.PID),
			Fix:    "wait for that pcbpilot process to finish"}
	}
	return j, nil
}

func clearJournal(root string, j *journal) {
	for _, e := range j.Changes {
		if e.Staged != "" {
			os.Remove(e.Staged)
		}
	}
	if len(j.Revision) >= 16 {
		os.RemoveAll(filepath.Join(txnDir(root), j.Revision[:16]))
	}
	os.Remove(journalPath(root))
	os.Remove(txnDir(root))
}

// pidAlive is true unless the process list proves pid is gone.
func pidAlive(list ProcLister, pid int) bool {
	if list == nil {
		return true
	}
	ps, err := list()
	if err != nil {
		return true
	}
	for _, p := range ps {
		if p.PID == pid {
			return true
		}
	}
	return false
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if serr := out.Sync(); err == nil {
		err = serr
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
