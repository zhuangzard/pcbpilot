//go:build linux

package kfile

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// readProcFile reads /proc entries; replaceable in tests.
var readProcFile = os.ReadFile

// SystemProcs lists processes from /proc/<pid>/comm. A process that exits
// while being listed (ENOENT, ESRCH) is skipped; any other read error is
// returned, so a lock is never judged stale from an incomplete list.
func SystemProcs() ([]Proc, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := readProcFile("/proc/" + e.Name() + "/comm")
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
			continue // exited meanwhile
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Proc{PID: pid, Name: strings.TrimSpace(string(b))})
	}
	return out, nil
}
