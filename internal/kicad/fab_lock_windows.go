//go:build windows

package kicad

import (
	"errors"
	"fmt"
	"syscall"
)

const (
	fileFlagDeleteOnClose               = 0x04000000
	errSharingViolation   syscall.Errno = 32 // ERROR_SHARING_VIOLATION
)

// lockFabOut takes OUT's fab lock: the lock file is opened with no sharing,
// so a second opener (another process or goroutine) gets a sharing
// violation; it is deleted when the handle closes, even after a crash.
func lockFabOut(outDir string) (unlock func(), err error) {
	p := fabLockPath(outDir)
	name, err := syscall.UTF16PtrFromString(p)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(name, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL|fileFlagDeleteOnClose, 0)
	if errors.Is(err, errSharingViolation) || errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return nil, fmt.Errorf("%w: %s", ErrFabBusy, p)
	}
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", p, err)
	}
	return func() { syscall.CloseHandle(h) }, nil
}
