//go:build linux

package kfile

import (
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
)

// A /proc read error other than "exited" must fail the listing, so a live
// KiCad is never missed and its lock never judged stale.
func TestSystemProcsFailsClosed(t *testing.T) {
	old := readProcFile
	defer func() { readProcFile = old }()

	readProcFile = func(p string) ([]byte, error) {
		if strings.HasSuffix(p, "/comm") {
			return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.EMFILE}
		}
		return os.ReadFile(p)
	}
	if _, err := SystemProcs(); err == nil {
		t.Fatal("EMFILE ignored")
	}
	if kicadRunning(SystemProcs) != true {
		t.Fatal("stale check did not fail closed")
	}

	readProcFile = func(p string) ([]byte, error) {
		return nil, &fs.PathError{Op: "open", Path: p, Err: syscall.ENOENT}
	}
	if ps, err := SystemProcs(); err != nil || len(ps) != 0 {
		t.Fatalf("exited processes: %v %v", ps, err)
	}
}
