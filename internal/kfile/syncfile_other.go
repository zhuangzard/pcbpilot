//go:build !windows

package kfile

import (
	"errors"
	"io/fs"
	"os"
)

// platformChmodSync flushes path and, with setMode, gives it mode, through
// one descriptor opened before any chmod, so a restrictive final mode (even
// 0000) never locks pcbpilot out of its own fsync. fchmod and fsync need no
// write access, so a read-only descriptor serves. If the current mode
// already denies reading, the file is made owner-readable just long enough
// to open it, and its mode is put back through the descriptor.
func platformChmodSync(path string, mode os.FileMode, setMode bool) error {
	cur, err := os.Lstat(path)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrPermission) {
		if cerr := chmodFile(path, cur.Mode().Perm()|0o400); cerr != nil {
			return err
		}
		if f, err = os.Open(path); err == nil && !setMode {
			mode, setMode = cur.Mode().Perm(), true
		}
	}
	if err != nil {
		return err
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
