//go:build !windows

package simtools

import (
	"os"
	"syscall"
)

// tryLock takes the install lock: flock on the lock file. The kernel drops
// it when the process ends, however it ends: there is no stale lock to take
// over. The file is removed while the lock is still held; a waiter that
// locked the removed file sees that the path no longer names it and tries
// again.
func tryLock(path string) (release func(), busy bool, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EINTR {
			return nil, true, nil
		}
		return nil, false, err
	}
	held, e1 := f.Stat()
	named, e2 := os.Stat(path)
	if e1 != nil || e2 != nil || !os.SameFile(held, named) {
		f.Close()
		return nil, true, nil
	}
	return func() { os.Remove(path); f.Close() }, false, nil
}
