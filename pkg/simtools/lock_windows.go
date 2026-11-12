//go:build windows

package simtools

import "syscall"

// tryLock takes the install lock: the lock file opened with no sharing, so a
// second open fails while the first handle lives. The system closes the
// handle — and, with delete-on-close, removes the file — when the process
// ends, however it ends: there is no stale lock to take over.
func tryLock(path string) (release func(), busy bool, err error) {
	const (
		deleteAccess        = 0x00010000
		flagDeleteOnClose   = 0x04000000
		errSharingViolation = syscall.Errno(32)
		errAccessDenied     = syscall.Errno(5) // also: delete pending
	)
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_WRITE|deleteAccess, 0, nil, syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL|flagDeleteOnClose, 0)
	if err != nil {
		if err == errSharingViolation || err == errAccessDenied {
			return nil, true, nil
		}
		return nil, false, err
	}
	return func() { syscall.CloseHandle(h) }, false, nil
}
