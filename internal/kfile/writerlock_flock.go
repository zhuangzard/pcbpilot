//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package kfile

import (
	"errors"
	"os"
	"syscall"
)

// lockExclusive takes a non-blocking flock on path. flock belongs to the
// open file description, so a second open in the same process conflicts too,
// and the kernel drops it when the process exits.
func lockExclusive(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errBusy
		}
		return nil, err
	}
	return f, nil
}

func sameDir(a, b string) bool {
	if abs, err := absPath(b); err == nil {
		b = abs
	}
	return a == b
}
