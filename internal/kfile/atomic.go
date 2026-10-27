package kfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Opts controls one write.
type Opts struct {
	// Base, when set, is the snapshot the new content was computed from; the
	// write fails with E_FILE_CHANGED if the target no longer matches it.
	Base *Snapshot
	// Locks says which KiCad locks may be removed (default BreakNone).
	Locks BreakPolicy
	// NoLockCheck skips the KiCad lock check: for files KiCad never opens
	// (pcbpilot's own reports and state).
	NoLockCheck bool
	// Perm is the mode of a new file (default 0o644); an existing file keeps
	// its mode.
	Perm os.FileMode
	// Env is the identity locks are compared against (default SystemEnv()).
	Env *Env
	// Writer is the project writer lock when the caller already holds it
	// (Txn.Apply only; without it Apply acquires the lock itself).
	Writer *Writer
}

// Result describes a committed write.
type Result struct {
	New   Snapshot   `json:"new"`
	Locks []LockInfo `json:"locks,omitempty"` // every lock seen, Broke on the removed ones
}

// Fault injection points for tests.
var (
	renameFile  = platformRename
	removeFile  = os.Remove
	syncDir     = platformSyncDir
	retryBudget = 3 * time.Second
)

// WriteFile atomically replaces path with data (see the package doc).
func WriteFile(path string, data []byte, o Opts) (Result, error) {
	f, err := Create(path, o)
	if err != nil {
		return Result{}, err
	}
	if _, err := f.Write(data); err != nil {
		f.Abort()
		return Result{}, err
	}
	return f.Commit()
}

// File is a pending atomic write: bytes go to a temp file beside the target
// and reach the target only on Commit.
type File struct {
	path  string
	f     *os.File
	perm  os.FileMode
	o     Opts
	locks []LockInfo
	h     hash.Hash
	size  int64
	done  bool
}

// Create starts an atomic write of path. The KiCad locks are checked now (to
// fail before expensive work) and again on Commit.
func Create(path string, o Opts) (*File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	perm, err := targetPerm(abs, o.Perm)
	if err != nil {
		return nil, err
	}
	w := &File{path: abs, perm: perm, o: o, h: sha256.New()}
	if !o.NoLockCheck {
		locks, err := Guard([]string{abs}, o.Locks, o.Env)
		w.locks = locks
		if err != nil {
			return nil, err
		}
	}
	tf, err := createTemp(abs)
	if err != nil {
		return nil, err
	}
	w.f = tf
	return w, nil
}

// targetPerm is the mode for the new content; a read-only target is refused.
func targetPerm(path string, perm os.FileMode) (os.FileMode, error) {
	st, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if perm == 0 {
			perm = 0o644
		}
		return perm, nil
	case err != nil:
		return 0, err
	case st.IsDir():
		return 0, fmt.Errorf("%s is a directory", path)
	case st.Mode().Perm()&0o200 == 0:
		return 0, &Error{Code: CodeReadOnly, Path: path, Detail: "the file is read-only",
			Fix: "clear the read-only attribute (or check it out of version control) and retry"}
	}
	return st.Mode().Perm(), nil
}

func createTemp(target string) (*os.File, error) {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, "."+filepath.Base(target)+".pcbpilot-*.tmp")
	if errors.Is(err, fs.ErrPermission) {
		return nil, &Error{Code: CodeReadOnly, Path: dir, Detail: "cannot create a file in the directory", Err: err,
			Fix: "make the directory writable and retry"}
	}
	return f, err
}

// Write appends to the pending content.
func (w *File) Write(p []byte) (int, error) {
	if w.done {
		return 0, fs.ErrClosed
	}
	n, err := w.f.Write(p)
	w.h.Write(p[:n])
	w.size += int64(n)
	return n, err
}

// Path is the target path (absolute).
func (w *File) Path() string { return w.path }

// Commit makes the written bytes the content of the target.
func (w *File) Commit() (Result, error) {
	if w.done {
		return Result{}, fs.ErrClosed
	}
	w.done = true
	tmp := w.f.Name()
	fail := func(err error) (Result, error) {
		os.Remove(tmp)
		return Result{Locks: w.locks}, err
	}
	if err := w.f.Sync(); err != nil {
		w.f.Close()
		return fail(err)
	}
	if err := w.f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(tmp, w.perm); err != nil {
		return fail(err)
	}
	// Locks and base are re-checked before every rename attempt, so a retry
	// loop never lands on a file KiCad opened or changed meanwhile.
	check := func() error {
		if !w.o.NoLockCheck {
			locks, err := Guard([]string{w.path}, w.o.Locks, w.o.Env)
			w.locks = mergeLocks(w.locks, locks)
			if err != nil {
				return err
			}
		}
		if w.o.Base != nil {
			b := *w.o.Base
			b.Path = w.path
			return b.Check()
		}
		return nil
	}
	if err := renameRetry(tmp, w.path, check); err != nil {
		return fail(err)
	}
	res := Result{
		New:   Snapshot{Path: w.path, Exists: true, Size: w.size, SHA256: hex.EncodeToString(w.h.Sum(nil))},
		Locks: w.locks,
	}
	if err := syncDir(filepath.Dir(w.path)); err != nil {
		return res, wrapf(err, "%s was replaced, but its directory could not be flushed", w.path)
	}
	return res, nil
}

// Abort discards the pending content. Safe after Commit (no-op).
func (w *File) Abort() error {
	if w.done {
		return nil
	}
	w.done = true
	w.f.Close()
	return os.Remove(w.f.Name())
}

// renamedError: the rename took effect but flushing it failed. It is a
// post-mutation error: never retried (the source is gone), and a
// transaction rolls the target back.
type renamedError struct {
	path string
	err  error
}

func (e *renamedError) Error() string {
	return "renamed into " + e.path + " but could not flush it: " + e.err.Error()
}

// renameRetry renames, retrying sharing violations with backoff (10 ms
// doubling, 500 ms cap) for retryBudget in total, but at least 3 attempts
// (the per-attempt checks can be slow on a loaded machine). check, when not nil, runs
// before every attempt and its error ends the loop.
func renameRetry(from, to string, check func() error) error {
	deadline := time.Now().Add(retryBudget)
	delay := 10 * time.Millisecond
	tries := 0
	for {
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		err := renameFile(from, to)
		tries++
		if err == nil || !isRetryable(err) {
			return err
		}
		if tries >= 3 && time.Now().Add(delay).After(deadline) {
			return &Error{Code: CodeSharing, Path: to, Err: err,
				Detail: fmt.Sprintf("another program holds the file open (%d attempts over %s): %v", tries, retryBudget, err),
				Fix:    "close the program holding the file (KiCad, a viewer, antivirus or a sync client) and retry"}
		}
		time.Sleep(delay)
		if delay *= 2; delay > 500*time.Millisecond {
			delay = 500 * time.Millisecond
		}
	}
}

// removeRetry deletes path with the same retry and check rule; a missing
// file is fine.
func removeRetry(path string, check func() error) error {
	deadline := time.Now().Add(retryBudget)
	delay := 10 * time.Millisecond
	for {
		if check != nil {
			if err := check(); err != nil {
				return err
			}
		}
		err := removeFile(path)
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if !isRetryable(err) {
			return err
		}
		if time.Now().Add(delay).After(deadline) {
			return &Error{Code: CodeSharing, Path: path, Err: err, Detail: "cannot delete: " + err.Error(),
				Fix: "close the program holding the file and retry"}
		}
		time.Sleep(delay)
		if delay *= 2; delay > 500*time.Millisecond {
			delay = 500 * time.Millisecond
		}
	}
}

func mergeLocks(a, b []LockInfo) []LockInfo {
	idx := map[string]int{}
	for i, l := range a {
		idx[l.Path] = i
	}
	for _, l := range b {
		if i, ok := idx[l.Path]; ok {
			a[i].State = l.State
			a[i].Broke = a[i].Broke || l.Broke
			continue
		}
		idx[l.Path] = len(a)
		a = append(a, l)
	}
	return a
}
