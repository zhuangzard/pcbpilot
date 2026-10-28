//go:build windows

package kfile

import "os"

// platformChmodSync flushes path and, with setMode, gives it mode, through
// one handle. FlushFileBuffers needs write access, which a read-only file
// refuses to open with, so the read-only attribute is cleared before the
// open and the final attribute (the old one, or mode) is set through the
// open handle before the flush. Callers hold the project writer lock and
// have checked the KiCad locks.
func platformChmodSync(path string, mode os.FileMode, setMode bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	ro := st.Mode().Perm()&0o200 == 0
	if ro {
		if err := chmodFile(path, 0o666); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if ro {
			chmodFile(path, 0o444)
		}
		return err
	}
	if !setMode && ro {
		mode, setMode = 0o444, true
	}
	if setMode {
		if err := chmodHandle(f, mode); err != nil {
			f.Close()
			return err
		}
	}
	err = syncHandle(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
