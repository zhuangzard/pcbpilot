//go:build !windows

package kfile

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func platformRename(from, to string) error { return os.Rename(from, to) }

// isRetryable: Unix rename does not fail on open files; EBUSY (a mount point
// or a file busy on a network filesystem) is the only transient case.
func isRetryable(err error) bool { return errors.Is(err, syscall.EBUSY) }

// syncDir fsyncs a directory so the entries created, renamed or removed in
// it survive a power loss.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("fsync directory %s: %w", dir, err)
	}
	return nil
}
