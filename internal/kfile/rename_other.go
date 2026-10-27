//go:build !windows

package kfile

import (
	"errors"
	"os"
	"syscall"
)

func platformRename(from, to string) error { return os.Rename(from, to) }

// isRetryable: Unix rename does not fail on open files; EBUSY (a mount point
// or a file busy on a network filesystem) is the only transient case.
func isRetryable(err error) bool { return errors.Is(err, syscall.EBUSY) }

// syncDir makes a rename durable. Best effort: a filesystem that cannot fsync
// a directory still has the rename, only without the durability promise.
func syncDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}
