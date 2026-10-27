//go:build windows

package kfile

import "os"

// platformSyncFile flushes path. FlushFileBuffers needs a handle with write
// access, which a read-only file refuses, so the read-only attribute is
// cleared for the flush and set again afterwards. Callers hold the project
// writer lock and have checked the KiCad locks.
func platformSyncFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	ro := st.Mode().Perm()&0o200 == 0
	if ro {
		if err := os.Chmod(path, 0o666); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err == nil {
		err = f.Sync()
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if ro {
		if cerr := os.Chmod(path, 0o444); err == nil {
			err = cerr
		}
	}
	return err
}
