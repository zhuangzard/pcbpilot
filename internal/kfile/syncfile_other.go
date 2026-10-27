//go:build !windows

package kfile

import "os"

// platformSyncFile flushes path through a read-only descriptor (fsync does
// not need write access), so read-only files can be flushed too.
func platformSyncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
