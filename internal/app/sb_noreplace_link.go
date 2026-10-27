package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// errSbNoHardLinks: the file system of --out has no hard links, so a file
// cannot be installed without the risk of replacing a concurrent save.
var errSbNoHardLinks = errors.New("no hard links")

var sbLink = os.Link // test seam

// sbLinkNoReplace moves src to dst and fails (fs.ErrExist) when dst exists:
// link(2) never replaces. Without hard links it fails closed rather than
// check-then-rename, which could replace a racing save.
func sbLinkNoReplace(src, dst string) error {
	err := sbLink(src, dst)
	if err == nil {
		return os.Remove(src)
	}
	if errors.Is(err, fs.ErrExist) {
		return err
	}
	if _, serr := os.Lstat(dst); serr == nil {
		return &os.LinkError{Op: "link", Old: src, New: dst, Err: fs.ErrExist}
	}
	return fmt.Errorf("the file system holding %s cannot hard-link (%v): %w", filepath.Dir(dst), err, errSbNoHardLinks)
}

// sbInstall puts a staged file in place (test seam over sbRenameNoReplace).
var sbInstall = func(src, dst string) error { return sbRenameNoReplace(src, dst) }
