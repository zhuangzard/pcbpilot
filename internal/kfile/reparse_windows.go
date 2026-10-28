//go:build windows

package kfile

import (
	"os"
	"syscall"
)

// Reparse tags of cloud-sync placeholders: IO_REPARSE_TAG_CLOUD and its
// CLOUD_1..CLOUD_F variants (OneDrive and other Cloud Files providers), and
// the legacy IO_REPARSE_TAG_ONEDRIVE. Such a file is the real file, not a
// redirect, so projects in a OneDrive folder can be written. Same rule as
// internal/kicad/fab_link_windows.go.
const (
	reparseTagCloudMask = 0xFFFF0FFF
	reparseTagCloud     = 0x9000001A
	reparseTagOneDrive  = 0x80000021
)

func isCloudTag(tag uint32) bool {
	return tag&reparseTagCloudMask == reparseTagCloud || tag == reparseTagOneDrive
}

// reparseTag reads a path's reparse tag (WIN32_FIND_DATA.dwReserved0);
// replaceable in tests, since a real placeholder cannot be made here.
var reparseTag = func(path string) (uint32, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var fd syscall.Win32finddata
	h, err := syscall.FindFirstFile(p, &fd)
	if err != nil {
		return 0, err
	}
	syscall.FindClose(h)
	return fd.Reserved0, nil
}

// allowReparse: a non-regular Lstat result that is still a plain file — a
// non-directory reparse point whose tag is a cloud placeholder. Every other
// tag, and an unreadable one, is refused.
func allowReparse(path string, fi os.FileInfo) bool {
	if fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok || d.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		return false
	}
	tag, err := reparseTag(path)
	return err == nil && isCloudTag(tag)
}
