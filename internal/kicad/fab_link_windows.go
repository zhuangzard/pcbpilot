//go:build windows

package kicad

import (
	"os"
	"syscall"
)

// Reparse tags of cloud-sync placeholders (OneDrive and other Cloud Files
// providers: IO_REPARSE_TAG_CLOUD, IO_REPARSE_TAG_CLOUD_1..F). Such a folder
// is the real directory, not a redirect, so it is accepted.
const (
	reparseTagCloudMask = 0xFFFF0FFF
	reparseTagCloud     = 0x9000001A
)

// isRedirectingReparsePoint: path carries FILE_ATTRIBUTE_REPARSE_POINT and
// its tag (WIN32_FIND_DATA.dwReserved0) is not a cloud placeholder. Symlinks
// and junctions are also caught by Go's Lstat mode bits; this catches any
// other redirecting tag. An unreadable tag counts as redirecting.
func isRedirectingReparsePoint(path string, st os.FileInfo) bool {
	d, ok := st.Sys().(*syscall.Win32FileAttributeData)
	if !ok || d.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return false
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return true
	}
	var fd syscall.Win32finddata
	h, err := syscall.FindFirstFile(p, &fd)
	if err != nil {
		return true
	}
	syscall.FindClose(h)
	return fd.Reserved0&reparseTagCloudMask != reparseTagCloud
}
