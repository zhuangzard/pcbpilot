//go:build !windows

package kfile

import "os"

// allowReparse: Unix has no reparse points; every non-regular file is
// refused.
func allowReparse(string, os.FileInfo) bool { return false }
