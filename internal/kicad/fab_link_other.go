//go:build !windows

package kicad

import "os"

// isRedirectingReparsePoint: reparse points are Windows-only; symlinks are
// caught by the Lstat mode bits.
func isRedirectingReparsePoint(string, os.FileInfo) bool { return false }
