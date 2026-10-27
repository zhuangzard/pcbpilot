package kfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// writerInfo is what Root/.pcbpilot/writer.lock says about its holder. It is
// information for the "busy" message only; ownership is the OS lock.
type writerInfo struct {
	PID  int       `json:"pid"`
	Host string    `json:"host"`
	User string    `json:"user,omitempty"`
	Time time.Time `json:"time"`
}

// Writer is a held project writer lock. Operations that are given a Writer
// (Apply, Recover, Discard) hold its operation lease for their whole run, so
// two of them sharing one Writer run one after the other, and Release waits
// for the running one to finish.
type Writer struct {
	root string
	op   sync.Mutex // operation lease
	mu   sync.Mutex // guards f
	f    *os.File
}

// errBusy is returned by lockExclusive when another handle holds the lock.
var errBusy = errors.New("held by another writer")

var absPath = filepath.Abs

// WriterLockPath is the pcbpilot writer lock of a project directory.
func WriterLockPath(root string) string { return filepath.Join(root, ".pcbpilot", "writer.lock") }

// AcquireWriter makes the caller the only pcbpilot writer of the project
// directory root until Release. The lock is an operating-system lock on
// Root/.pcbpilot/writer.lock (Windows: a handle opened without write
// sharing; Unix: flock), so it is released when the holder exits or crashes
// and there is no stale-lock takeover to race on. It is exclusive between
// processes and between callers within one process. The file is never
// deleted (deleting it would let two writers lock different files); its JSON
// content only names the current holder.
func AcquireWriter(root string, env *Env) (*Writer, error) {
	env = envOr(env)
	abs, err := absPath(root)
	if err != nil {
		return nil, err
	}
	p := WriterLockPath(abs)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := lockExclusive(p)
	if errors.Is(err, errBusy) {
		detail := "unknown holder"
		var held writerInfo
		if b, rerr := os.ReadFile(p); rerr == nil && json.Unmarshal(b, &held) == nil && held.PID > 0 {
			detail = fmt.Sprintf("pid %d on %s since %s", held.PID, held.Host, held.Time.Format(time.RFC3339))
		}
		return nil, &Error{Code: CodeWriterBusy, Path: p,
			Detail: "another pcbpilot command is writing this project: " + detail,
			Fix:    "wait for it to finish (the lock is released automatically when that process exits)"}
	}
	if err != nil {
		return nil, wrapf(err, "lock %s", p)
	}
	body, _ := json.Marshal(writerInfo{PID: os.Getpid(), Host: env.Host, User: env.User, Time: time.Now().UTC()})
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt(body, 0) // informational; a failure does not weaken the lock
	}
	return &Writer{root: abs, f: f}, nil
}

// Root is the project directory the lock covers.
func (w *Writer) Root() string { return w.root }

// Release gives up the lock once no operation is using it. Calling it again
// is a no-op.
func (w *Writer) Release() error {
	if w == nil {
		return nil
	}
	w.op.Lock()
	defer w.op.Unlock()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close() // closing the handle releases the OS lock
	w.f = nil
	return err
}

func (w *Writer) covers(root string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f != nil && sameDir(w.root, root)
}

// writerFor takes the operation lease of w when w covers root (release
// returns the lease; the caller keeps owning w), or acquires the lock for
// root (release gives it up).
func writerFor(w *Writer, root string, env *Env) (release func(), err error) {
	if w != nil {
		w.op.Lock()
		if !w.covers(root) {
			w.op.Unlock()
			return nil, fmt.Errorf("the writer lock passed in is for %s (or released), not %s", w.root, root)
		}
		return w.op.Unlock, nil
	}
	nw, err := AcquireWriter(root, env)
	if err != nil {
		return nil, err
	}
	return func() { nw.Release() }, nil
}
