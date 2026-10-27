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
func lockExclusive(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if errors.Is(err, errSharingViolation) || errors.Is(err, errLockViolation) {
		return nil, errBusy
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), path), nil
}

func sameDir(a, b string) bool {
	if abs, err := absPath(b); err == nil {
		b = abs
	}
	return strings.EqualFold(a, b)
}
