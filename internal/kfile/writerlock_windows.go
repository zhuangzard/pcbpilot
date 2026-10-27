//go:build windows

package kfile

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

// lockExclusive opens path so that no other handle can write to it: the
// share mode allows readers (to show the holder) but not writers. A second
// open for writing fails with ERROR_SHARING_VIOLATION until this handle is
// closed, which the kernel does when the process exits.
//
// FILE_FLAG_OPEN_REPARSE_POINT opens a link itself instead of its target;
// a directory or a reparse point other than a cloud-sync placeholder is
// then refused before anything is written.
func lockExclusive(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if errors.Is(err, errSharingViolation) || errors.Is(err, errLockViolation) {
		return nil, errBusy
	}
	if err != nil {
		return nil, err
	}
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &info); err != nil {
		syscall.CloseHandle(h)
		return nil, err
	}
	switch {
	case info.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0:
		syscall.CloseHandle(h)
		return nil, notRegularLock(path, "a directory")
	case info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0:
		if tag, err := reparseTag(path); err != nil || !isCloudTag(tag) {
			syscall.CloseHandle(h)
			return nil, notRegularLock(path, "a reparse point (link)")
		}
	}
	return os.NewFile(uintptr(h), path), nil
}

func sameDir(a, b string) bool {
	if abs, err := absPath(b); err == nil {
		b = abs
	}
	return strings.EqualFold(a, b)
}
