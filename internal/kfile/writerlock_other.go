//go:build !windows && !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package kfile

import (
	"errors"
	"os"
	"runtime"
)

func lockExclusive(string) (*os.File, error) {
	return nil, errors.New("project writer lock is not supported on " + runtime.GOOS)
}

func sameDir(a, b string) bool {
	if abs, err := absPath(b); err == nil {
		b = abs
	}
	return a == b
}
