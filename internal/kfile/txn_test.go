package kfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// project makes a synthetic project directory with the given files.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func stagedTxn(t *testing.T, dir string) *Txn {
	t.Helper()
	tx := NewTxn(dir)
	if err := tx.Stage(filepath.Join(dir, "a.kicad_pcb"), []byte("A2"), nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Stage(filepath.Join(dir, "a.kicad_pro"), []byte("P2"), nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Stage(filepath.Join(dir, "new.kicad_dru"), []byte("D1"), nil); err != nil {
		t.Fatal(err)
	}
	return tx
}

func TestTxnApply(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1", "old.kicad_prl": "L1"})
	tx := stagedTxn(t, dir)
	if err := tx.Delete(filepath.Join(dir, "old.kicad_prl"), nil); err != nil {
		t.Fatal(err)
	}
	plan := tx.Plan(fakeEnv())
	kinds := map[string]Kind{}
	for _, c := range plan.Changes {
		kinds[filepath.Base(c.Path)] = c.Kind
	}
	if kinds["a.kicad_pcb"] != KindReplace || kinds["new.kicad_dru"] != KindCreate || kinds["old.kicad_prl"] != KindDelete {
		t.Fatalf("kinds %v", kinds)
	}
	got, err := tx.Apply(plan.Revision, Opts{Env: fakeEnv()})
	if err != nil || got.Revision != plan.Revision {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A2" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P2" ||
		read(t, filepath.Join(dir, "new.kicad_dru")) != "D1" || read(t, filepath.Join(dir, "old.kicad_prl")) != "<missing>" {
		t.Fatal("files not applied")
	}
	if _, err := os.Stat(txnDir(dir)); err == nil {
		t.Error("txn dir (journal/backups) left after success")
	}
	noTemps(t, dir)
}

func TestTxnRevisionDeterministic(t *testing.T) {
	files := map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"}
	d1, d2 := project(t, files), project(t, files)
	t1, t2 := stagedTxn(t, d1), stagedTxn(t, d2)
	r1, r2 := t1.Plan(fakeEnv()).Revision, t2.Plan(fakeEnv()).Revision
	t1.Abort()
	t2.Abort()
	if r1 != r2 || len(r1) != 64 {
		t.Fatalf("revision differs across identical projects: %s %s", r1, r2)
	}
	// Staging order does not matter; content does.
	tx := NewTxn(d1)
	tx.Stage(filepath.Join(d1, "new.kicad_dru"), []byte("D1"), nil)
	tx.Stage(filepath.Join(d1, "a.kicad_pro"), []byte("P2"), nil)
	tx.Stage(filepath.Join(d1, "a.kicad_pcb"), []byte("A2"), nil)
	if tx.Plan(fakeEnv()).Revision != r1 {
		t.Fatal("revision depends on staging order")
	}
	tx.Stage(filepath.Join(d1, "a.kicad_pcb"), []byte("A3"), nil)
	if tx.Plan(fakeEnv()).Revision == r1 {
		t.Fatal("revision ignores new content")
	}
	tx.Abort()
	noTemps(t, d1)
}

func TestTxnStaleRefused(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	tx := stagedTxn(t, dir)
	rev := tx.Plan(fakeEnv()).Revision

	// A plan made earlier from other bytes is a different revision.
	_, err := tx.Apply(strings.Repeat("0", 64), Opts{Env: fakeEnv()})
	if CodeOf(err) != CodeRevision || ExitCode(err) != 4 {
		t.Fatalf("revision: %v", err)
	}
	// The board changes after staging (base check).
	os.WriteFile(filepath.Join(dir, "a.kicad_pcb"), []byte("edited in KiCad"), 0o644)
	_, err = tx.Apply(rev, Opts{Env: fakeEnv()})
	if CodeOf(err) != CodeChanged {
		t.Fatalf("changed: %v", err)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "edited in KiCad" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" {
		t.Fatal("a refused txn wrote files")
	}
	// A KiCad lock on the project refuses the whole txn.
	tx2 := stagedTxn(t, dir)
	writeLock(t, filepath.Join(dir, "a.kicad_pro"), lockBody)
	if _, err := tx2.Apply("", Opts{Env: fakeEnv("kicad.exe")}); CodeOf(err) != CodeLocked {
		t.Fatalf("locked: %v", err)
	}
	tx.Abort()
	tx2.Abort()
	noTemps(t, dir)
}

func TestTxnRollbackOnSecondRename(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1\r\nbinary\x00", "a.kicad_pro": "P1"})
	tx := stagedTxn(t, dir)
	// Changes apply in path order: a.kicad_pcb, a.kicad_pro, new.kicad_dru.
	injectRename(t, func(from, to string) error {
		if filepath.Base(to) == "a.kicad_pro" {
			return errors.New("injected failure")
		}
		return platformRename(from, to)
	})
	_, err := tx.Apply("", Opts{Env: fakeEnv()})
	if err == nil || !strings.Contains(err.Error(), "injected failure") || CodeOf(err) == CodeTxnPending {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, filepath.Join(dir, "a.kicad_pcb")); got != "A1\r\nbinary\x00" {
		t.Fatalf("first file not restored byte-for-byte: %q", got)
	}
	if read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" || read(t, filepath.Join(dir, "new.kicad_dru")) != "<missing>" {
		t.Fatal("other files touched")
	}
	if Pending(dir) != nil {
		t.Fatal("journal left after a clean rollback")
	}
	noTemps(t, dir)
}

// interrupt applies tx with the 2nd rename failing and rollback failing too,
// which leaves the journal like a crash would.
func interrupt(t *testing.T, tx *Txn) {
	t.Helper()
	boardDone := false
	old := renameFile
	renameFile = func(from, to string) error {
		switch base := filepath.Base(to); {
		case base == "journal.json":
		case base == "a.kicad_pcb" && !boardDone:
			boardDone = true
		default:
			return errors.New("power cut")
		}
		return platformRename(from, to)
	}
	defer func() { renameFile = old }()
	_, err := tx.Apply("", Opts{Env: fakeEnv()})
	if CodeOf(err) != CodeTxnPending {
		t.Fatalf("interrupted apply: %v", err)
	}
}

func TestTxnPendingThenRecover(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	interrupt(t, stagedTxn(t, dir))
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A2" {
		t.Fatal("setup: first rename should have landed")
	}
	// Further writes are refused.
	blocked := stagedTxn(t, dir)
	if _, err := blocked.Apply("", Opts{Env: fakeEnv()}); CodeOf(err) != CodeTxnPending {
		t.Fatalf("pending not refused: %v", err)
	}
	blocked.Abort() // a refused Apply leaves the staging to the caller
	var ke *Error
	if err := Pending(dir); !errors.As(err, &ke) || !strings.Contains(ke.Fix, "txn recover") {
		t.Fatalf("Pending = %v", err)
	}
	// The journal's pid (this process) is excluded from the "still running" check.
	restored, err := Recover(dir, fakeEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || filepath.Base(restored[0]) != "a.kicad_pcb" {
		t.Fatalf("restored %v", restored)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" {
		t.Fatal("not recovered")
	}
	if Pending(dir) != nil {
		t.Fatal("journal left after recover")
	}
	noTemps(t, dir)
	if r, err := Recover(dir, fakeEnv()); err != nil || r != nil {
		t.Fatalf("second recover: %v %v", r, err)
	}
	// After recovery the next transaction goes through.
	if _, err := stagedTxn(t, dir).Apply("", Opts{Env: fakeEnv()}); err != nil {
		t.Fatal(err)
	}
}

func TestTxnDiscardKeepsFiles(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	interrupt(t, stagedTxn(t, dir))
	if err := Discard(dir, fakeEnv()); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A2" || Pending(dir) != nil {
		t.Fatal("discard")
	}
	noTemps(t, dir)
}

func TestRecoverRefusesLiveWriter(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	interrupt(t, stagedTxn(t, dir))
	// Rewrite the journal as if pid 4242 on this host wrote it.
	j, err := readJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	j.PID = 4242
	if err := writeJournal(dir, *j); err != nil {
		t.Fatal(err)
	}
	alive := &Env{Host: "box", User: "alice", Procs: func() ([]Proc, error) { return []Proc{{PID: 4242, Name: "pcbpilot"}}, nil }}
	if _, err := Recover(dir, alive); CodeOf(err) != CodeTxnPending {
		t.Fatalf("live writer: %v", err)
	}
	if _, err := Recover(dir, fakeEnv()); err != nil {
		t.Fatalf("dead writer: %v", err)
	}
}

func TestTxnStageFileAndEmpty(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1"})
	stage := t.TempDir()
	src := filepath.Join(stage, "a.kicad_pcb")
	os.WriteFile(src, []byte("from bridge"), 0o644)
	tx := NewTxn(dir)
	if err := tx.StageFile(filepath.Join(dir, "a.kicad_pcb"), src, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Apply("", Opts{Env: fakeEnv()}); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "from bridge" {
		t.Fatal("StageFile")
	}
	if _, err := NewTxn(dir).Apply("", Opts{Env: fakeEnv()}); err != nil {
		t.Fatalf("empty txn: %v", err)
	}
}

func TestAcquireWriter(t *testing.T) {
	dir := t.TempDir()
	rel, err := AcquireWriter(dir, fakeEnv())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWriter(dir, fakeEnv()); CodeOf(err) != CodeWriterBusy || ExitCode(err) != 4 {
		t.Fatalf("second writer: %v", err)
	}
	rel()
	rel, err = AcquireWriter(dir, fakeEnv())
	if err != nil {
		t.Fatal(err)
	}
	rel()

	// A dead holder on this host is taken over; a live one or another host is not.
	write := func(body string) { os.WriteFile(WriterLockPath(dir), []byte(body), 0o644) }
	write(`{"pid":4242,"host":"box","time":"2026-01-01T00:00:00Z"}`)
	if rel, err := AcquireWriter(dir, fakeEnv()); err != nil {
		t.Fatalf("dead holder: %v", err)
	} else {
		rel()
	}
	alive := &Env{Host: "box", User: "alice", Procs: func() ([]Proc, error) { return []Proc{{PID: 4242}}, nil }}
	write(`{"pid":4242,"host":"box"}`)
	if _, err := AcquireWriter(dir, alive); CodeOf(err) != CodeWriterBusy {
		t.Fatalf("live holder: %v", err)
	}
	write(`{"pid":4242,"host":"far"}`)
	if _, err := AcquireWriter(dir, fakeEnv()); CodeOf(err) != CodeWriterBusy {
		t.Fatalf("other host: %v", err)
	}
	write(``)
	if _, err := AcquireWriter(dir, fakeEnv()); CodeOf(err) != CodeWriterBusy {
		t.Fatalf("unreadable: %v", err)
	}
}
