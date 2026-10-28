package console

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// symlinkUnprivileged reports an os.Symlink failure on Windows without the
// symlink privilege (Developer Mode off, not elevated):
// ERROR_PRIVILEGE_NOT_HELD.
func symlinkUnprivileged(err error) bool {
	return runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314))
}

// symlinkOrSkip creates newname -> oldname, or skips the calling (sub)test
// when Windows refuses for lack of the symlink privilege.
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if symlinkUnprivileged(err) {
			t.Skipf("symlinks need Developer Mode or elevation on Windows: %v", err)
		}
		t.Fatal(err)
	}
}

// linkDirForTest links a directory the way the installers do: a symlink, or
// on unprivileged Windows a directory junction (selfupdate.linkDir).
func linkDirForTest(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err != nil && symlinkUnprivileged(err) {
		sh := os.Getenv("ComSpec")
		if sh == "" {
			sh = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		}
		if out, jerr := exec.Command(sh, "/c", "mklink", "/J", link, target).CombinedOutput(); jerr != nil {
			t.Fatalf("mklink /J %s: %v: %s", link, jerr, out)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
}
