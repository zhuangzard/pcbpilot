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
//
// The open does not follow a symlink (O_NOFOLLOW), and the opened file must
// be a regular file before anything is written to it.
func lockExclusive(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o644)
	if errors.Is(err, syscall.ELOOP) {
		return nil, notRegularLock(path, "a symlink")
	}
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, notRegularLock(path, st.Mode().Type().String())
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
