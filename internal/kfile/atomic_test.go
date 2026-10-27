package kfile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// noTemps fails when a pcbpilot temp file is left in dir.
func noTemps(t *testing.T, dir string) {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".pcbpilot-") {
			t.Errorf("temp file left: %s", e.Name())
		}
	}
}

// injectRename replaces the rename primitive for one test.
func injectRename(t *testing.T, fn func(from, to string) error) {
	t.Helper()
	old := renameFile
	renameFile = fn
	t.Cleanup(func() { renameFile = old })
}

func setBudget(t *testing.T, d time.Duration) {
	t.Helper()
	old := retryBudget
	retryBudget = d
	t.Cleanup(func() { retryBudget = old })
}

func TestWriteFileReplacesAndKeepsMode(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "board.kicad_pcb")
	os.WriteFile(p, []byte("old"), 0o600)
	before, _ := os.Stat(p)
	res, err := WriteFile(p, []byte("new content"), Opts{Env: fakeEnv()})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	after, _ := os.Stat(p)
	if string(b) != "new content" || after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("content %q mode %v → %v", b, before.Mode().Perm(), after.Mode().Perm())
	}
	want, _ := Stat(p)
	if res.New.SHA256 != want.SHA256 || res.New.Size != 11 || !res.New.Exists {
		t.Fatalf("result %+v, want %+v", res.New, want)
	}
	noTemps(t, dir)

	// A new file gets Opts.Perm.
	np := filepath.Join(dir, "new.csv")
	if _, err := WriteFile(np, []byte("a"), Opts{NoLockCheck: true, Perm: 0o640}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(np); st.Mode().Perm() != 0o640 {
			t.Errorf("new file mode %v", st.Mode().Perm())
		}
	}
}

func TestWriteFileErrorLeavesTargetAndNoTemp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.kicad_sch")
	os.WriteFile(p, []byte("orig"), 0o644)
	injectRename(t, func(from, to string) error { return errors.New("disk on fire") })
	if _, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv()}); err == nil {
		t.Fatal("no error")
	}
	if b, _ := os.ReadFile(p); string(b) != "orig" {
		t.Fatalf("target changed: %q", b)
	}
	noTemps(t, dir)
}

func TestRenameRetriesSharingViolation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.kicad_pcb")
	os.WriteFile(p, []byte("orig"), 0o644)
	calls := 0
	injectRename(t, func(from, to string) error {
		calls++
		if calls <= 3 {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: retryableErr}
		}
		return platformRename(from, to)
	})
	if _, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv()}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new" || calls != 4 {
		t.Fatalf("content %q after %d calls", b, calls)
	}
}

func TestRenamePersistentSharingViolation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.kicad_pcb")
	os.WriteFile(p, []byte("orig"), 0o644)
	setBudget(t, 600*time.Millisecond)
	calls := 0
	injectRename(t, func(from, to string) error {
		calls++
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: retryableErr}
	})
	start := time.Now()
	_, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv()})
	if CodeOf(err) != CodeSharing || calls < 2 || time.Since(start) > 5*time.Second {
		t.Fatalf("err %v after %d calls in %s", err, calls, time.Since(start))
	}
	if ExitCode(err) != 1 {
		t.Errorf("exit code %d", ExitCode(err))
	}
	if b, _ := os.ReadFile(p); string(b) != "orig" {
		t.Fatalf("target changed: %q", b)
	}
	noTemps(t, dir)
}

func TestWriteFileReadOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lib.kicad_sym")
	os.WriteFile(p, []byte("orig"), 0o444)
	t.Cleanup(func() { os.Chmod(p, 0o644) })
	calls := 0
	injectRename(t, func(from, to string) error { calls++; return platformRename(from, to) })
	start := time.Now()
	_, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv()})
	if CodeOf(err) != CodeReadOnly || calls != 0 || time.Since(start) > time.Second {
		t.Fatalf("err %v, %d renames", err, calls)
	}
	noTemps(t, dir)
}

func TestWriteFileChangedSinceRead(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.kicad_sch")
	os.WriteFile(p, []byte("v1"), 0o644)
	_, snap, err := Read(p)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("v2 from KiCad"), 0o644)
	_, err = WriteFile(p, []byte("ours"), Opts{Base: &snap, Env: fakeEnv()})
	var ke *Error
	if !errors.As(err, &ke) || ke.Code != CodeChanged || ExitCode(err) != 4 || !strings.Contains(ke.Detail, snap.SHA256[:12]) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "v2 from KiCad" {
		t.Fatalf("overwrote: %q", b)
	}
	noTemps(t, dir)

	// Base says "absent", but the file appeared.
	np := filepath.Join(dir, "new.kicad_pcb")
	absent, _ := Stat(np)
	os.WriteFile(np, []byte("someone"), 0o644)
	if _, err := WriteFile(np, []byte("ours"), Opts{Base: &absent, Env: fakeEnv()}); CodeOf(err) != CodeChanged {
		t.Fatalf("created-since: %v", err)
	}
	// An unchanged base passes.
	_, snap, _ = Read(p)
	if _, err := WriteFile(p, []byte("ours"), Opts{Base: &snap, Env: fakeEnv()}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteFileLocked(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.kicad_pcb")
	os.WriteFile(p, []byte("orig"), 0o644)
	writeLock(t, p, lockBody)
	_, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv("pcbnew.exe")})
	if CodeOf(err) != CodeLocked {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "orig" {
		t.Fatal("wrote through a live lock")
	}
	noTemps(t, dir)
	// Stale lock removed under BreakStale and recorded.
	res, err := WriteFile(p, []byte("new"), Opts{Env: fakeEnv(), Locks: BreakStale})
	if err != nil || len(res.Locks) != 1 || !res.Locks[0].Broke {
		t.Fatalf("break stale: %v %+v", err, res.Locks)
	}
	// NoLockCheck ignores a live lock (pcbpilot-owned files).
	writeLock(t, p, lockBody)
	if _, err := WriteFile(p, []byte("x"), Opts{Env: fakeEnv("kicad"), NoLockCheck: true}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateCommitAbort(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fab.zip")
	f, err := Create(p, Opts{NoLockCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("PK"))
	f.Write(bytes.Repeat([]byte{1}, 1000))
	if _, err := os.Stat(p); err == nil {
		t.Fatal("target visible before Commit")
	}
	res, err := f.Commit()
	if err != nil || res.New.Size != 1002 {
		t.Fatal(err, res)
	}
	if f.Abort() != nil {
		t.Error("Abort after Commit")
	}
	if _, err := f.Commit(); err == nil {
		t.Error("second Commit")
	}

	g, _ := Create(p, Opts{NoLockCheck: true})
	g.Write([]byte("discarded"))
	if err := g.Abort(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); len(b) != 1002 {
		t.Fatalf("Abort touched the target: %d bytes", len(b))
	}
	noTemps(t, dir)
}

func TestExitCodeAndCodeOf(t *testing.T) {
	if ExitCode(nil) != 0 || ExitCode(errors.New("x")) != 1 {
		t.Fatal("nil/plain")
	}
	for c, want := range map[Code]int{CodeLocked: 4, CodeChanged: 4, CodeRevision: 4, CodeWriterBusy: 4,
		CodeSharing: 1, CodeTxnPending: 1, CodeReadOnly: 1} {
		err := wrapf(&Error{Code: c, Detail: "d"}, "outer")
		if ExitCode(err) != want || CodeOf(err) != c {
			t.Errorf("%s: exit %d code %s", c, ExitCode(err), CodeOf(err))
		}
	}
}
