//go:build windows

package kfile

import (
	"os"
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
	setBudget(t, 300*time.Millisecond)
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
