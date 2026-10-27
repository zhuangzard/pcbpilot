//go:build !windows

package app

// sbRenameNoReplace moves src to dst and fails (fs.ErrExist) when dst
// exists (sb_noreplace_link.go).
func sbRenameNoReplace(src, dst string) error { return sbLinkNoReplace(src, dst) }
