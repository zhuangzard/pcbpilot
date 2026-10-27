//go:build !windows && !darwin && !linux && !freebsd && !netbsd && !openbsd && !dragonfly

package kicad

import (
	"fmt"
	"os"
)

// lockFabOut falls back to an O_EXCL lock file where flock is unavailable;
// a crashed run leaves it behind and the error names it.
func lockFabOut(outDir string) (unlock func(), err error) {
	p := fabLockPath(outDir)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if os.IsExist(err) {
		return nil, fmt.Errorf("%w: %s (delete it if no fab run is active)", ErrFabBusy, p)
	}
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", p, err)
	}
	fmt.Fprintf(f, "pid %d\n", os.Getpid())
	return func() { f.Close(); os.Remove(p) }, nil
}
