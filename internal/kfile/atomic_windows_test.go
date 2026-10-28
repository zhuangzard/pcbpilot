//go:build windows

package kfile

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// retryableErr is an error isRetryable accepts on this platform.
var retryableErr error = errSharingViolation

// openShared holds path open with the given share mode, like a viewer or an
// antivirus scanner would.
func openShared(t *testing.T, path string, share uint32) syscall.Handle {
	t.Helper()
	p, _ := syscall.UTF16PtrFromString(path)
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, share, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestWindowsRealHandleNoShareDelete(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.kicad_pcb")
	os.WriteFile(p, []byte("orig"), 0o644)
	setBudget(t, 600*time.Millisecond)
	calls := 0
	injectRename(t, func(from, to string) error { calls++; return platformRename(from, to) })

	h := openShared(t, p, 0)
	start := time.Now()
	_, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv()})
	syscall.CloseHandle(h)
	if CodeOf(err) != CodeSharing || calls < 3 {
		t.Fatalf("err %v after %d renames in %s", err, calls, time.Since(start))
	}
	if b, _ := os.ReadFile(p); string(b) != "orig" {
		t.Fatalf("target changed: %q", b)
	}
	noTemps(t, dir)

	// The holder lets go while we retry: the write succeeds.
	setBudget(t, 3*time.Second)
	calls = 0
	h = openShared(t, p, syscall.FILE_SHARE_READ)
	injectRename(t, func(from, to string) error {
		calls++
		err := platformRename(from, to)
		if calls == 1 {
			if !isRetryable(err) {
				t.Errorf("first rename with the handle open: %v", err)
			}
			syscall.CloseHandle(h) // released between retries
		}
		return err
	})
	if _, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv()}); err != nil || calls != 2 {
		t.Fatalf("after release: %v (%d renames)", err, calls)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
}

func TestWindowsRealHandleShareDelete(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.kicad_pcb")
	os.WriteFile(p, []byte("orig"), 0o644)
	h := openShared(t, p, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE)
	defer syscall.CloseHandle(h)
	if _, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv()}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
	noTemps(t, dir)
}

// The POSIX fallback renamed the file but could not flush it: that is
// reported as such at once, not retried into a misleading error.
func TestWindowsFallbackFlushFailure(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.kicad_pcb")
	os.WriteFile(p, []byte("orig"), 0o644)
	h := openShared(t, p, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE)
	defer syscall.CloseHandle(h)
	old := flushHandle
	flushHandle = func(syscall.Handle) error { return errors.New("flush failed") }
	defer func() { flushHandle = old }()
	calls := 0
	injectRename(t, func(from, to string) error { calls++; return platformRename(from, to) })
	_, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv()})
	var re *renamedError
	if !errors.As(err, &re) || calls != 1 {
		t.Fatalf("err %v after %d renames", err, calls)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" {
		t.Fatalf("content %q", b)
	}
	noTemps(t, dir)
}

// A directory junction (a reparse point any user can create) is refused.
func TestWindowsReparsePointRefused(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "realdir")
	os.Mkdir(real, 0o755)
	j := filepath.Join(dir, "j.kicad_pcb")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", j, real).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	tx := NewTxn(dir)
	if err := tx.Stage(j, []byte("x"), nil); CodeOf(err) != CodeUnsupported {
		t.Errorf("Stage: %v", err)
	}
	if _, err := WriteFile(j, []byte("x"), Opts{Env: fakeEnv()}); CodeOf(err) != CodeUnsupported {
		t.Errorf("WriteFile: %v", err)
	}
	noTemps(t, dir)
}

// fakeInfo is an Lstat result for a reparse point with the given mode.
type fakeInfo struct {
	mode os.FileMode
	attr uint32
}

func (f fakeInfo) Name() string       { return "f" }
func (f fakeInfo) Size() int64        { return 1 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return &syscall.Win32FileAttributeData{FileAttributes: f.attr} }

// Cloud-sync placeholders (OneDrive) are written like regular files; every
// other reparse tag is refused. The tag is injected: a real placeholder
// needs a sync provider.
func TestWindowsCloudPlaceholderAllowed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.kicad_pcb")
	oldL, oldT := lstatFile, reparseTag
	t.Cleanup(func() { lstatFile, reparseTag = oldL, oldT })
	cases := []struct {
		name string
		mode os.FileMode
		attr uint32
		tag  uint32
		err  error
		ok   bool
	}{
		{"cloud", os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0x9000001A, nil, true},
		{"cloud_1", os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0x9000101A, nil, true},
		{"cloud_f", os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0x9000F01A, nil, true},
		{"onedrive legacy", os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0x80000021, nil, true},
		{"symlink tag", os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0xA000000C, nil, false},
		{"mount point tag", os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0xA0000003, nil, false},
		{"app exec link", os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0x8000001B, nil, false},
		{"dedup", os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0x80000013, nil, false},
		{"unreadable tag", os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0, errors.New("denied"), false},
		{"cloud tag on a symlink", os.ModeSymlink, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0x9000001A, nil, false},
		{"cloud tag on a directory", os.ModeDir | os.ModeIrregular, syscall.FILE_ATTRIBUTE_REPARSE_POINT, 0x9000001A, nil, false},
		{"irregular, no reparse attribute", os.ModeIrregular, 0, 0x9000001A, nil, false},
	}
	for _, c := range cases {
		lstatFile = func(string) (os.FileInfo, error) { return fakeInfo{c.mode, c.attr}, nil }
		reparseTag = func(string) (uint32, error) { return c.tag, c.err }
		err := checkRegular(p)
		if c.ok && err != nil || !c.ok && CodeOf(err) != CodeUnsupported {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// A junction at writer.lock (creatable without privileges) is refused and
// its target left alone.
func TestWindowsWriterLockJunctionRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	os.Mkdir(target, 0o755)
	os.WriteFile(filepath.Join(target, "keep"), []byte("keep me"), 0o644)
	os.MkdirAll(filepath.Dir(WriterLockPath(dir)), 0o755)
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", WriterLockPath(dir), target).CombinedOutput(); err != nil {
		t.Skipf("mklink /J: %v %s", err, out)
	}
	if w, err := AcquireWriter(dir, fakeEnv()); err == nil {
		w.Release()
		t.Fatal("acquired a junction as the writer lock")
	}
	ents, _ := os.ReadDir(target)
	if b, _ := os.ReadFile(filepath.Join(target, "keep")); len(ents) != 1 || string(b) != "keep me" {
		t.Fatal("junction target changed")
	}
}
