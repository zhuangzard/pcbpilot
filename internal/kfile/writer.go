package kfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// writerLock is Root/.pcbpilot/writer.lock.
type writerLock struct {
	PID  int       `json:"pid"`
	Host string    `json:"host"`
	User string    `json:"user,omitempty"`
	Time time.Time `json:"time"`
}

// WriterLockPath is the pcbpilot writer lock of a project directory.
func WriterLockPath(root string) string { return filepath.Join(root, ".pcbpilot", "writer.lock") }

// AcquireWriter makes this process the only pcbpilot writer of the project
// directory root until release is called. A held lock is taken over only
// when it names this host and its pid is not running (pid reuse can make a
// dead holder look alive; that errs on the side of refusing). An empty or
// unreadable lock file is treated as held.
func AcquireWriter(root string, env *Env) (release func(), err error) {
	env = envOr(env)
	p := WriterLockPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	me := writerLock{PID: os.Getpid(), Host: env.Host, User: env.User, Time: time.Now().UTC()}
	body, _ := json.Marshal(me)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, werr := f.Write(body)
			if cerr := f.Close(); werr == nil {
				werr = cerr
			}
			if werr != nil {
				os.Remove(p)
				return nil, werr
			}
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
		b, _ := os.ReadFile(p)
		var held writerLock
		parsed := json.Unmarshal(b, &held) == nil && held.PID > 0
		if parsed && sameHost(held.Host, env.Host) && held.PID != me.PID && !pidAlive(env.Procs, held.PID) {
			os.Remove(p) // the holder died; take over
			continue
		}
		detail := "unreadable lock file"
		if parsed {
			detail = fmt.Sprintf("pid %d on %s since %s", held.PID, held.Host, held.Time.Format(time.RFC3339))
		}
		return nil, &Error{Code: CodeWriterBusy, Path: p,
			Detail: "another pcbpilot command is writing this project: " + detail,
			Fix:    "wait for it to finish; if no pcbpilot command is running, delete " + p}
	}
	return nil, &Error{Code: CodeWriterBusy, Path: p, Detail: "the writer lock keeps reappearing",
		Fix: "wait for the other pcbpilot command to finish"}
}
