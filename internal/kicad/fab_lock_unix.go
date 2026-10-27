//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package kicad

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockFabOut takes OUT's fab lock with flock(LOCK_EX|LOCK_NB) on the lock
// file, released by the kernel when the process dies. The file is left in
// place (unlinking a flock file races with a concurrent opener); a lock
// taken on an inode that another run has since replaced is refused.
func lockFabOut(outDir string) (unlock func(), err error) {
	p := fabLockPath(outDir)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", p, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrFabBusy, p)
		}
		return nil, fmt.Errorf("lock %s: %w", p, err)
	}
	held, err1 := f.Stat()
	now, err2 := os.Stat(p)
	if err1 != nil || err2 != nil || !os.SameFile(held, now) {
		f.Close()
		return nil, fmt.Errorf("%w: %s changed while locking", ErrFabBusy, p)
	}
	return func() { f.Close() }, nil
}
