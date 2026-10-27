//go:build windows

package kfile

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8

	errAccessDenied     = syscall.Errno(5)
	errSharingViolation = syscall.Errno(32)
	errLockViolation    = syscall.Errno(33)

	fileRenameInfoEx              = 22 // FILE_INFO_BY_HANDLE_CLASS FileRenameInfoEx
	fileRenameFlagReplaceIfExists = 0x1
	fileRenameFlagPosixSemantics  = 0x2
	deleteAccess                  = 0x00010000 // DELETE
)

// kernel32 is a KnownDLL, so NewLazyDLL cannot be redirected to a planted copy.
var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procMoveFileExW                = kernel32.NewProc("MoveFileExW")
	procSetFileInformationByHandle = kernel32.NewProc("SetFileInformationByHandle")
)

// platformRename replaces to with from. MoveFileExW with WRITE_THROUGH
// returns only after the rename is on disk, so no directory fsync is needed.
// When the target is open in another process, MoveFileExW is denied even if
// that process allowed FILE_SHARE_DELETE; a POSIX-semantics rename (Windows
// 10 1809+, NTFS) succeeds in that case, so it is tried before the first
// error is reported. Without FILE_SHARE_DELETE both fail and the caller
// retries.
func platformRename(from, to string) error {
	err := moveFileEx(from, to)
	if err == nil || !isRetryable(err) {
		return err
	}
	perr := posixRename(from, to)
	var renamed *renamedError
	switch {
	case perr == nil:
		return nil
	case errors.As(perr, &renamed):
		return perr // the rename happened; only its flush failed
	}
	return err // the fallback did not rename either: report the first error
}

// flushHandle is FlushFileBuffers, replaceable in tests.
var flushHandle = syscall.FlushFileBuffers

func moveFileEx(from, to string) error {
	f, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	t, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: err}
	}
	r, _, e := procMoveFileExW.Call(uintptr(unsafe.Pointer(f)), uintptr(unsafe.Pointer(t)),
		moveFileReplaceExisting|moveFileWriteThrough)
	if r != 0 {
		return nil
	}
	if errno, ok := e.(syscall.Errno); !ok || errno == 0 {
		e = syscall.EINVAL
	}
	return &os.LinkError{Op: "rename", Old: from, New: to, Err: e}
}

// fileRenameInfo is FILE_RENAME_INFO_EX; FileName runs past the struct.
type fileRenameInfo struct {
	Flags          uint32
	RootDirectory  syscall.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

func posixRename(from, to string) error {
	if err := procSetFileInformationByHandle.Find(); err != nil {
		return err
	}
	src, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	h, err := syscall.CreateFile(src, deleteAccess|syscall.SYNCHRONIZE|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil,
		syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	name, err := syscall.UTF16FromString(verbatim(to))
	if err != nil {
		return err
	}
	name = name[:len(name)-1] // FileNameLength excludes the NUL
	head := unsafe.Offsetof(fileRenameInfo{}.FileName)
	size := head + uintptr(len(name)+1)*2
	buf := make([]uint64, (size+7)/8) // zeroed, 8-byte aligned
	info := (*fileRenameInfo)(unsafe.Pointer(&buf[0]))
	info.Flags = fileRenameFlagReplaceIfExists | fileRenameFlagPosixSemantics
	info.FileNameLength = uint32(len(name) * 2)
	copy(unsafe.Slice(&info.FileName[0], len(name)), name)
	r, _, e := procSetFileInformationByHandle.Call(uintptr(h), fileRenameInfoEx, uintptr(unsafe.Pointer(info)), size)
	if r == 0 {
		return e
	}
	// This path has no WRITE_THROUGH; flush the renamed file (its data and
	// the metadata NTFS associates with it) before reporting success.
	if err := flushHandle(h); err != nil {
		return &renamedError{path: to, err: err}
	}
	return nil
}

// verbatim is the \\?\ form of an absolute path.
func verbatim(p string) string {
	switch {
	case strings.HasPrefix(p, `\\?\`):
		return p
	case strings.HasPrefix(p, `\\`):
		return `\\?\UNC\` + p[2:]
	}
	return `\\?\` + p
}

// isRetryable: another process has the file open without FILE_SHARE_DELETE
// (or holds a byte-range lock). A read-only target also reports
// ACCESS_DENIED; writers refuse that case before renaming.
func isRetryable(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == errSharingViolation || errno == errLockViolation || errno == errAccessDenied
}

// platformSyncDir is a no-op: Windows cannot flush a directory handle. Durability of
// directory entries rests on NTFS metadata journaling, MoveFileExW
// WRITE_THROUGH for renames, and the journal ordering in Txn (see the
// package doc).
func platformSyncDir(string) error { return nil }
