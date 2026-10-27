//go:build !windows

package app

import (
	"errors"
	"io/fs"
	"os"
)

// sbRenameNoReplace moves src to dst and fails (fs.ErrExist) when dst
// exists: link(2) never replaces. On a file system without hard links it
// falls back to check-then-rename, which is not atomic.
func sbRenameNoReplace(src, dst string) error {
	err := os.Link(src, dst)
	if err == nil {
		return os.Remove(src)
	}
	if errors.Is(err, fs.ErrExist) {
		return err
	}
	if _, serr := os.Lstat(dst); serr == nil {
		return &os.LinkError{Op: "rename", Old: src, New: dst, Err: fs.ErrExist}
	}
	return os.Rename(src, dst)
}
