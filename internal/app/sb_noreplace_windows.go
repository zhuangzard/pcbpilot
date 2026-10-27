//go:build windows

package app

import "syscall"

// sbRenameNoReplace moves src to dst and fails (fs.ErrExist) when dst
// exists: MoveFileW without MOVEFILE_REPLACE_EXISTING. os.Rename would
// replace a file KiCad just saved there.
func sbRenameNoReplace(src, dst string) error {
	from, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	return syscall.MoveFile(from, to)
}
