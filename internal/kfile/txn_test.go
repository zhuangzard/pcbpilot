package kfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
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

func mustPlan(t *testing.T, tx *Txn) Plan {
	t.Helper()
	p, err := tx.Plan(fakeEnv())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// stagedTxn stages a.kicad_pcb=A2, a.kicad_pro=P2, new.kicad_dru=D1; they
// apply in that (path) order.
func stagedTxn(t *testing.T, dir string) *Txn {
	t.Helper()
	tx := NewTxn(dir)
	for n, body := range map[string]string{"a.kicad_pcb": "A2", "a.kicad_pro": "P2", "new.kicad_dru": "D1"} {
		if err := tx.Stage(filepath.Join(dir, n), []byte(body), nil); err != nil {
			t.Fatal(err)
		}
	}
	return tx
}

// clean asserts that no journal, backup or temp file is left.
func clean(t *testing.T, dir string) {
	t.Helper()
	if Pending(dir) != nil {
		t.Error("journal left")
	}
	ents, _ := os.ReadDir(txnDir(dir))
	for _, e := range ents {
		t.Errorf("left in txn dir: %s", e.Name())
	}
	noTemps(t, dir)
}

func onBase(name string, fn func(from, to string) error) func(from, to string) error {
	return func(from, to string) error {
		if filepath.Base(to) == name {
			return fn(from, to)
		}
		return platformRename(from, to)
	}
}

func TestTxnApply(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1", "old.kicad_prl": "L1"})
	tx := stagedTxn(t, dir)
	if err := tx.Delete(filepath.Join(dir, "old.kicad_prl"), nil); err != nil {
		t.Fatal(err)
	}
	plan := mustPlan(t, tx)
	kinds := map[string]Kind{}
	for _, c := range plan.Changes {
		kinds[filepath.Base(c.Path)] = c.Kind
	}
	if kinds["a.kicad_pcb"] != KindReplace || kinds["new.kicad_dru"] != KindCreate || kinds["old.kicad_prl"] != KindDelete {
		t.Fatalf("kinds %v", kinds)
	}
	got, err := tx.Apply(plan.Revision, Opts{Env: fakeEnv()})
	if err != nil || got.Revision != plan.Revision || !got.Applied {
		t.Fatal(err, got.Applied)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A2" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P2" ||
		read(t, filepath.Join(dir, "new.kicad_dru")) != "D1" || read(t, filepath.Join(dir, "old.kicad_prl")) != "<missing>" {
		t.Fatal("files not applied")
	}
	clean(t, dir)
}

func TestTxnRevisionDeterministic(t *testing.T) {
	files := map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"}
	d1, d2 := project(t, files), project(t, files)
	t1, t2 := stagedTxn(t, d1), stagedTxn(t, d2)
	r1, r2 := mustPlan(t, t1).Revision, mustPlan(t, t2).Revision
	t1.Abort()
	t2.Abort()
	if r1 != r2 || len(r1) != 64 {
		t.Fatalf("revision differs across identical projects: %s %s", r1, r2)
	}
	tx := NewTxn(d1)
	tx.Stage(filepath.Join(d1, "new.kicad_dru"), []byte("D1"), nil)
	tx.Stage(filepath.Join(d1, "a.kicad_pro"), []byte("P2"), nil)
	tx.Stage(filepath.Join(d1, "a.kicad_pcb"), []byte("A2"), nil)
	if mustPlan(t, tx).Revision != r1 {
		t.Fatal("revision depends on staging order")
	}
	tx.Stage(filepath.Join(d1, "a.kicad_pcb"), []byte("A3"), nil)
	if mustPlan(t, tx).Revision == r1 {
		t.Fatal("revision ignores new content")
	}
	tx.Abort()
	noTemps(t, d1)
}

func TestTxnStaleRefused(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	tx := stagedTxn(t, dir)
	rev := mustPlan(t, tx).Revision
	if _, err := tx.Apply(strings.Repeat("0", 64), Opts{Env: fakeEnv()}); CodeOf(err) != CodeRevision || ExitCode(err) != 4 {
		t.Fatalf("revision: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "a.kicad_pcb"), []byte("edited in KiCad"), 0o644)
	if _, err := tx.Apply(rev, Opts{Env: fakeEnv()}); CodeOf(err) != CodeChanged {
		t.Fatalf("changed: %v", err)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "edited in KiCad" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" {
		t.Fatal("a refused txn wrote files")
	}
	tx2 := stagedTxn(t, dir)
	writeLock(t, filepath.Join(dir, "a.kicad_pro"), lockBody)
	if _, err := tx2.Apply("", Opts{Env: fakeEnv("kicad.exe")}); CodeOf(err) != CodeLocked {
		t.Fatalf("locked: %v", err)
	}
	tx.Abort()
	tx2.Abort()
	clean(t, dir)
}

func TestTxnRollbackOnSecondRename(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1\r\nbinary\x00", "a.kicad_pro": "P1"})
	tx := stagedTxn(t, dir)
	injectRename(t, onBase("a.kicad_pro", func(string, string) error { return errors.New("injected failure") }))
	plan, err := tx.Apply("", Opts{Env: fakeEnv()})
	if err == nil || !strings.Contains(err.Error(), "injected failure") || CodeOf(err) == CodeTxnPending || plan.Applied {
		t.Fatalf("err = %v", err)
	}
	if got := read(t, filepath.Join(dir, "a.kicad_pcb")); got != "A1\r\nbinary\x00" {
		t.Fatalf("first file not restored byte-for-byte: %q", got)
	}
	if read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" || read(t, filepath.Join(dir, "new.kicad_dru")) != "<missing>" {
		t.Fatal("other files touched")
	}
	clean(t, dir)
}

// A rename or delete that takes effect and then reports an error is rolled
// back too.
func TestTxnRollbackIncludesFailedOp(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	once := false
	injectRename(t, onBase("a.kicad_pro", func(from, to string) error {
		if err := platformRename(from, to); err != nil || once {
			return err
		}
		once = true
		return errors.New("reported failure after the rename landed")
	}))
	if _, err := stagedTxn(t, dir).Apply("", Opts{Env: fakeEnv()}); err == nil {
		t.Fatal("no error")
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" {
		t.Fatalf("not rolled back: %q %q", read(t, filepath.Join(dir, "a.kicad_pcb")), read(t, filepath.Join(dir, "a.kicad_pro")))
	}
	clean(t, dir)

	// The same for a delete.
	dir = project(t, map[string]string{"a.kicad_pcb": "A1", "z.kicad_prl": "L1"})
	tx := NewTxn(dir)
	tx.Stage(filepath.Join(dir, "a.kicad_pcb"), []byte("A2"), nil)
	tx.Delete(filepath.Join(dir, "z.kicad_prl"), nil)
	old := removeFile
	removeFile = func(p string) error {
		if filepath.Base(p) == "z.kicad_prl" {
			if err := os.Remove(p); err != nil {
				return err
			}
			return errors.New("reported failure after the delete landed")
		}
		return os.Remove(p)
	}
	_, err := tx.Apply("", Opts{Env: fakeEnv()})
	removeFile = old
	if err == nil || read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, filepath.Join(dir, "z.kicad_prl")) != "L1" {
		t.Fatalf("delete not rolled back: %v", err)
	}
	clean(t, dir)
}

// Locks and base hashes are re-checked right before each file is touched.
func TestTxnMidApplyEditOrLock(t *testing.T) {
	for _, mode := range []string{"edit", "lock"} {
		dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
		pro := filepath.Join(dir, "a.kicad_pro")
		beforeMutate = func(p string) {
			if p != pro {
				return
			}
			if mode == "edit" {
				os.WriteFile(pro, []byte("saved by KiCad"), 0o644)
			} else {
				writeLock(t, pro, lockBody)
			}
		}
		_, err := stagedTxn(t, dir).Apply("", Opts{Env: fakeEnv("kicad.exe")})
		beforeMutate = nil
		if mode == "edit" {
			if CodeOf(err) != CodeChanged || read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, pro) != "saved by KiCad" {
				t.Errorf("edit: %v, pcb %q pro %q", err, read(t, filepath.Join(dir, "a.kicad_pcb")), read(t, pro))
			}
			clean(t, dir)
			continue
		}
		// The project lock also blocks restoring a.kicad_pcb: nothing more is
		// changed, the journal stays, and recover finishes once KiCad closes.
		var le *Error
		if CodeOf(err) != CodeTxnPending || !errors.As(errors.Unwrap(err), &le) || read(t, filepath.Join(dir, "a.kicad_pcb")) != "A2" {
			t.Fatalf("lock: %v, pcb %q", err, read(t, filepath.Join(dir, "a.kicad_pcb")))
		}
		os.Remove(LockPath(pro))
		if rec, err := Recover(dir, RecoverOpts{Env: fakeEnv()}); err != nil || rec.Action != "rolled-back" {
			t.Fatalf("recover after unlock: %+v %v", rec, err)
		}
		if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, pro) != "P1" {
			t.Fatal("lock: not recovered")
		}
		clean(t, dir)
	}
}

// interrupt applies tx so that the first file lands and everything after it,
// including the rollback, fails: the journal is left as after a crash.
func interrupt(t *testing.T, tx *Txn) {
	t.Helper()
	landed := map[string]bool{}
	old := renameFile
	renameFile = func(from, to string) error {
		base := filepath.Base(to)
		if base == "journal.json" || (base == "a.kicad_pcb" || base == "a.del" || base == "a.aro") && !landed[base] {
			landed[base] = true
			return platformRename(from, to)
		}
		return errors.New("power cut")
	}
	oldRm := removeFile
	removeFile = func(p string) error {
		base := filepath.Base(p)
		if (base == "a.del" || base == "a.aro") && !landed[base] {
			landed[base] = true
			return os.Remove(p)
		}
		return errors.New("power cut")
	}
	defer func() { renameFile, removeFile = old, oldRm }()
	if _, err := tx.Apply("", Opts{Env: fakeEnv()}); CodeOf(err) != CodeTxnPending {
		t.Fatalf("interrupted apply: %v", err)
	}
}

func TestTxnPendingThenRecover(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	interrupt(t, stagedTxn(t, dir))
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A2" {
		t.Fatal("setup: first rename should have landed")
	}
	blocked := stagedTxn(t, dir)
	if _, err := blocked.Apply("", Opts{Env: fakeEnv()}); CodeOf(err) != CodeTxnPending {
		t.Fatalf("pending not refused: %v", err)
	}
	blocked.Abort()
	var ke *Error
	if err := Pending(dir); !errors.As(err, &ke) || !strings.Contains(ke.Fix, "txn recover") {
		t.Fatalf("Pending = %v", err)
	}
	rec, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
	if err != nil || rec.Action != "rolled-back" || len(rec.Restored) != 1 || filepath.Base(rec.Restored[0]) != "a.kicad_pcb" {
		t.Fatalf("recover %+v %v", rec, err)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" {
		t.Fatal("not recovered")
	}
	clean(t, dir)
	if rec, err := Recover(dir, RecoverOpts{Env: fakeEnv()}); err != nil || rec.Action != "none" {
		t.Fatalf("second recover: %+v %v", rec, err)
	}
	if _, err := stagedTxn(t, dir).Apply("", Opts{Env: fakeEnv()}); err != nil {
		t.Fatal(err)
	}
}

// Recover never overwrites work done after the interruption.
func TestRecoverConflictKeepsJournal(t *testing.T) {
	for _, edited := range []string{"a.kicad_pcb", "a.kicad_pro"} { // reached / never reached
		dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
		interrupt(t, stagedTxn(t, dir))
		p := filepath.Join(dir, edited)
		os.WriteFile(p, []byte("user work"), 0o644)
		rec, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
		if CodeOf(err) != CodeChanged || len(rec.Conflicts) != 1 || rec.Conflicts[0] != p {
			t.Fatalf("%s: %+v %v", edited, rec, err)
		}
		if read(t, p) != "user work" || Pending(dir) == nil {
			t.Fatalf("%s: user work lost or journal dropped", edited)
		}
		if edited == "a.kicad_pro" && read(t, filepath.Join(dir, "a.kicad_pcb")) != "A2" {
			t.Fatal("recover touched a file despite the conflict")
		}
		if rec, err := Discard(dir, RecoverOpts{Env: fakeEnv()}); err != nil || rec.Action != "discarded" {
			t.Fatalf("discard: %+v %v", rec, err)
		}
		if read(t, p) != "user work" {
			t.Fatal("discard changed a file")
		}
		clean(t, dir)
	}
}

// A crash after every change landed (journal not yet removed) only needs
// the cleanup.
func TestRecoverCompletesFinishedTxn(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	old := removeFile
	removeFile = func(p string) error {
		if filepath.Base(p) == "journal.json" {
			return errors.New("crash before the journal is removed")
		}
		return os.Remove(p)
	}
	plan, err := stagedTxn(t, dir).Apply("", Opts{Env: fakeEnv()})
	removeFile = old
	if CodeOf(err) != CodeTxnPending || !plan.Applied {
		t.Fatalf("apply: %v applied=%v", err, plan.Applied)
	}
	rec, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
	if err != nil || rec.Action != "completed" || len(rec.Restored) != 0 {
		t.Fatalf("%+v %v", rec, err)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A2" || read(t, filepath.Join(dir, "new.kicad_dru")) != "D1" {
		t.Fatal("completed txn undone")
	}
	clean(t, dir)
}

func TestRecoverLocksHostAndWriter(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	interrupt(t, stagedTxn(t, dir))

	writeLock(t, filepath.Join(dir, "a.kicad_pcb"), lockBody)
	if _, err := Recover(dir, RecoverOpts{Env: fakeEnv("pcbnew")}); CodeOf(err) != CodeLocked {
		t.Fatalf("locked: %v", err)
	}
	os.Remove(LockPath(filepath.Join(dir, "a.kicad_pcb")))

	w, err := AcquireWriter(dir, fakeEnv())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []func() (Recovery, error){
		func() (Recovery, error) { return Recover(dir, RecoverOpts{Env: fakeEnv()}) },
		func() (Recovery, error) { return Discard(dir, RecoverOpts{Env: fakeEnv()}) },
	} {
		if _, err := f(); CodeOf(err) != CodeWriterBusy {
			t.Fatalf("recover/discard beside a held writer lock: %v", err)
		}
	}

	far := &Env{Host: "far", User: "alice", Procs: fakeEnv().Procs}
	if _, err := Recover(dir, RecoverOpts{Env: far, Writer: w}); CodeOf(err) != CodeTxnPending {
		t.Fatalf("foreign host: %v", err)
	}
	if rec, err := Recover(dir, RecoverOpts{Env: far, Writer: w, Force: true}); err != nil || rec.Action != "rolled-back" {
		t.Fatalf("forced: %+v %v", rec, err)
	}
	w.Release()
	clean(t, dir)
}

func TestTxnModeRestored(t *testing.T) {
	setup := func() string {
		dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
		os.WriteFile(filepath.Join(dir, "a.del"), []byte("secret"), 0o600)
		return dir
	}
	txFor := func(dir string) *Txn {
		tx := stagedTxn(t, dir)
		if err := tx.Delete(filepath.Join(dir, "a.del"), nil); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	check := func(dir, how string) {
		t.Helper()
		st, err := os.Stat(filepath.Join(dir, "a.del"))
		want, _ := os.Stat(filepath.Join(setup(), "a.del"))
		if err != nil || st.Mode().Perm() != want.Mode().Perm() || read(t, filepath.Join(dir, "a.del")) != "secret" {
			t.Fatalf("%s: deleted file not restored with its mode: %v %v", how, err, st)
		}
	}
	// Rollback: a.del (deleted first) is restored when a.kicad_pro fails.
	dir := setup()
	injectRename(t, onBase("a.kicad_pro", func(string, string) error { return errors.New("fail") }))
	if _, err := txFor(dir).Apply("", Opts{Env: fakeEnv()}); err == nil {
		t.Fatal("no error")
	}
	renameFile = platformRename
	check(dir, "rollback")
	clean(t, dir)
	// Recover after a crash that left a.del deleted.
	dir = setup()
	interrupt(t, txFor(dir))
	if read(t, filepath.Join(dir, "a.del")) != "<missing>" {
		t.Fatal("setup: a.del should be deleted")
	}
	if _, err := Recover(dir, RecoverOpts{Env: fakeEnv()}); err != nil {
		t.Fatal(err)
	}
	check(dir, "recover")
	clean(t, dir)
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

func TestApplyUsesWriterLock(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	w, err := AcquireWriter(dir, fakeEnv())
	if err != nil {
		t.Fatal(err)
	}
	tx := stagedTxn(t, dir)
	if _, err := tx.Apply("", Opts{Env: fakeEnv()}); CodeOf(err) != CodeWriterBusy {
		t.Fatalf("without the held lock: %v", err)
	}
	other, _ := AcquireWriter(t.TempDir(), fakeEnv())
	if _, err := tx.Apply("", Opts{Env: fakeEnv(), Writer: other}); err == nil {
		t.Fatal("accepted a writer lock of another project")
	}
	other.Release()
	if _, err := tx.Apply("", Opts{Env: fakeEnv(), Writer: w}); err != nil {
		t.Fatal(err)
	}
	w.Release()
	clean(t, dir)
}

func TestAcquireWriter(t *testing.T) {
	dir := t.TempDir()
	w, err := AcquireWriter(dir, fakeEnv())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWriter(dir, fakeEnv()); CodeOf(err) != CodeWriterBusy || ExitCode(err) != 4 ||
		!strings.Contains(err.Error(), "pid") {
		t.Fatalf("second writer: %v", err)
	}
	if w.Release() != nil || w.Release() != nil {
		t.Fatal("release")
	}
	// A lock file left by a crashed holder carries no OS lock: free.
	os.WriteFile(WriterLockPath(dir), []byte(`{"pid":4242,"host":"box"}`), 0o644)
	w, err = AcquireWriter(dir, fakeEnv())
	if err != nil {
		t.Fatalf("leftover file: %v", err)
	}
	w.Release()

	// Simultaneous contenders: exactly one wins.
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	var won []*Writer
	busy := 0
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w, err := AcquireWriter(dir, fakeEnv())
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won = append(won, w)
			} else if CodeOf(err) == CodeWriterBusy {
				busy++
			} else {
				t.Error(err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if len(won) != 1 || busy != n-1 {
		t.Fatalf("%d winners, %d busy", len(won), busy)
	}
	won[0].Release()
}

// A lock that appears after Recover's preflight stops the restore.
func TestRestoreBlockedByLateLock(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	interrupt(t, stagedTxn(t, dir))
	pcb := filepath.Join(dir, "a.kicad_pcb")
	beforeRestore = func(p string) { writeLock(t, p, lockBody) }
	_, err := Recover(dir, RecoverOpts{Env: fakeEnv("eeschema")})
	beforeRestore = nil
	if CodeOf(err) != CodeTxnPending || !strings.Contains(err.Error(), "E_KICAD_LOCKED") {
		t.Fatalf("late lock: %v", err)
	}
	if read(t, pcb) != "A2" || Pending(dir) == nil {
		t.Fatal("restored through a lock, or journal dropped")
	}
	os.Remove(LockPath(pcb))
	if _, err := Recover(dir, RecoverOpts{Env: fakeEnv()}); err != nil || read(t, pcb) != "A1" {
		t.Fatalf("recover after unlock: %v", err)
	}
	clean(t, dir)
}

// If the journal's removal cannot be flushed, the backups are kept.
func TestFinishKeepsBackupsWhenFlushFails(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	tx := stagedTxn(t, dir)
	rev := mustPlan(t, tx).Revision
	removed := false
	old := removeFile
	removeFile = func(p string) error {
		if filepath.Base(p) == "journal.json" {
			removed = true
		}
		return os.Remove(p)
	}
	oldSync := syncDir
	syncDir = func(d string) error {
		if removed && d == txnDir(dir) {
			return errors.New("flush failed")
		}
		return oldSync(d)
	}
	plan, err := tx.Apply("", Opts{Env: fakeEnv()})
	removeFile, syncDir = old, oldSync
	if err == nil || !plan.Applied || !strings.Contains(err.Error(), "backups kept") {
		t.Fatalf("err %v applied %v", err, plan.Applied)
	}
	if _, err := os.Stat(filepath.Join(txnDir(dir), rev[:16])); err != nil {
		t.Fatal("backups removed although the journal removal was not flushed")
	}
}

// New directories made by Stage are flushed in their parents, bottom-up,
// before the commit; a failed flush aborts with nothing written.
func TestCreatedDirsFlushed(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pro": "P1"})
	target := filepath.Join(dir, "new", "sub", "x.kicad_sch")
	var synced []string
	oldSync := syncDir
	syncDir = func(d string) error { synced = append(synced, d); return oldSync(d) }
	tx := NewTxn(dir)
	if err := tx.Stage(target, []byte("S"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Apply("", Opts{Env: fakeEnv()}); err != nil {
		t.Fatal(err)
	}
	syncDir = oldSync
	idx := func(d string) int {
		for i, s := range synced {
			if s == d {
				return i
			}
		}
		return -1
	}
	sub, newd := idx(filepath.Join(dir, "new")), idx(dir)
	if sub < 0 || newd < 0 {
		t.Fatalf("parents of created dirs not flushed: %v", synced)
	}

	// Injected failure on the flush of "new" (parent of "sub").
	dir2 := project(t, map[string]string{"a.kicad_pro": "P1"})
	target2 := filepath.Join(dir2, "new", "sub", "x.kicad_sch")
	syncDir = func(d string) error {
		if d == filepath.Join(dir2, "new") {
			return errors.New("flush failed")
		}
		return oldSync(d)
	}
	tx2 := NewTxn(dir2)
	tx2.Stage(target2, []byte("S"), nil)
	_, err := tx2.Apply("", Opts{Env: fakeEnv()})
	syncDir = oldSync
	if err == nil || read(t, target2) != "<missing>" {
		t.Fatalf("flush failure: %v, target %q", err, read(t, target2))
	}
	tx2.Abort()
	clean(t, dir2)
}

// A file modified while its backup is being copied aborts in prepare.
func TestBackupChangedDuringCopy(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	pro := filepath.Join(dir, "a.kicad_pro")
	beforeBackup = func(p string) {
		if p == pro {
			os.WriteFile(pro, []byte("saved meanwhile"), 0o644)
		}
	}
	tx := stagedTxn(t, dir)
	_, err := tx.Apply("", Opts{Env: fakeEnv()})
	beforeBackup = nil
	if CodeOf(err) != CodeChanged {
		t.Fatalf("err %v", err)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, pro) != "saved meanwhile" {
		t.Fatal("a target was touched")
	}
	tx.Abort()
	clean(t, dir)
}

// Operations sharing one Writer run one after the other, and Release waits
// for the running one.
func TestWriterLeaseSerializes(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "b.kicad_pcb": "B1"})
	w, err := AcquireWriter(dir, fakeEnv())
	if err != nil {
		t.Fatal(err)
	}
	var inside, maxInside int32
	var mu sync.Mutex
	entered := make(chan struct{}, 4)
	beforeMutate = func(string) {
		entered <- struct{}{}
		mu.Lock()
		inside++
		if inside > maxInside {
			maxInside = inside
		}
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		inside--
		mu.Unlock()
	}
	defer func() { beforeMutate = nil }()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	var txs []*Txn
	for i, n := range []string{"a.kicad_pcb", "b.kicad_pcb"} {
		tx := NewTxn(dir)
		txs = append(txs, tx)
		tx.Stage(filepath.Join(dir, n), []byte("new"), nil)
		wg.Add(1)
		go func(i int, tx *Txn) {
			defer wg.Done()
			_, errs[i] = tx.Apply("", Opts{Env: fakeEnv(), Writer: w})
		}(i, tx)
	}
	<-entered             // one operation is running
	relErr := w.Release() // must wait for it
	wg.Wait()
	if errs[0] != nil && errs[1] != nil {
		t.Fatalf("Release did not wait for the running operation: %v / %v", errs[0], errs[1])
	}
	for i, e := range errs {
		// The one that got the lease after Release finds the writer gone.
		if e != nil && !strings.Contains(e.Error(), "released") {
			t.Errorf("apply %d: %v", i, e)
		}
	}
	for _, tx := range txs {
		tx.Abort() // the refused one keeps its staging until the caller aborts
	}
	if maxInside != 1 || relErr != nil {
		t.Fatalf("overlapping operations: %d, release %v", maxInside, relErr)
	}
	clean(t, dir)
}

// crashAfterLastMutation applies tx and "dies" (panics) in the directory
// fsync that follows the last mutation.
func crashAfterLastMutation(t *testing.T, tx *Txn, dir, last string) {
	t.Helper()
	lastDone := false
	beforeMutate = func(p string) { lastDone = filepath.Base(p) == last }
	oldSync := syncDir
	syncDir = func(d string) error {
		if lastDone && d == dir {
			panic("power cut")
		}
		return oldSync(d)
	}
	defer func() {
		beforeMutate, syncDir = nil, oldSync
		if r := recover(); r != "power cut" {
			t.Fatalf("no crash: %v", r)
		}
	}()
	tx.Apply("", Opts{Env: fakeEnv()})
}

// Recover makes the targets durable before it drops the journal.
func TestRecoverFlushesBeforeCleanup(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	tx := stagedTxn(t, dir)
	rev := mustPlan(t, tx).Revision
	crashAfterLastMutation(t, tx, dir, "new.kicad_dru")
	if Pending(dir) == nil || read(t, filepath.Join(dir, "new.kicad_dru")) != "D1" {
		t.Fatal("setup: expected every change landed and the journal left")
	}
	backups := filepath.Join(txnDir(dir), rev[:16])

	oldSync := syncDir
	syncDir = func(d string) error {
		if d == dir {
			return errors.New("flush failed")
		}
		return oldSync(d)
	}
	_, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
	syncDir = oldSync
	if CodeOf(err) != CodeTxnPending || Pending(dir) == nil {
		t.Fatalf("flush failure: %v", err)
	}
	if _, serr := os.Stat(backups); serr != nil {
		t.Fatal("backups dropped although the targets are not durable")
	}

	var order []string
	syncDir = func(d string) error { order = append(order, "sync "+d); return oldSync(d) }
	old := removeFile
	removeFile = func(p string) error { order = append(order, "rm "+filepath.Base(p)); return os.Remove(p) }
	rec, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
	syncDir, removeFile = oldSync, old
	if err != nil || rec.Action != "completed" {
		t.Fatalf("%+v %v", rec, err)
	}
	si, ri := -1, -1
	for i, o := range order {
		if o == "sync "+dir && si < 0 {
			si = i
		}
		if o == "rm journal.json" {
			ri = i
		}
	}
	if si < 0 || ri < 0 || si > ri {
		t.Fatalf("target dir not flushed before the journal was removed: %v", order)
	}
	clean(t, dir)
}

// A restore whose flush fails keeps the journal; the next recover finishes.
func TestRecoverRestoreFlushFailure(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	interrupt(t, stagedTxn(t, dir))
	old := syncFile
	syncFile = func(string) error { return errors.New("flush failed") }
	_, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
	syncFile = old
	if CodeOf(err) != CodeTxnPending || Pending(dir) == nil {
		t.Fatalf("flush failure: %v", err)
	}
	rec, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
	if err != nil || rec.Action != "rolled-back" || read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" {
		t.Fatalf("%+v %v", rec, err)
	}
	clean(t, dir)
}

// A crash between restoring a file's bytes and its chmod is repaired.
func TestRecoverRepairsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no 0600/0644 distinction")
	}
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	del := filepath.Join(dir, "a.del")
	os.WriteFile(del, []byte("secret"), 0o600)
	tx := stagedTxn(t, dir)
	tx.Delete(del, nil)
	interrupt(t, tx)
	os.WriteFile(del, []byte("secret"), 0o644) // bytes back, mode not
	os.Chmod(del, 0o644)
	if _, err := Recover(dir, RecoverOpts{Env: fakeEnv()}); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(del); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	clean(t, dir)
}

// A rename that happened but whose flush failed is not retried and is
// rolled back with the rest.
func TestRenamedButNotFlushed(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	calls := 0
	injectRename(t, onBase("a.kicad_pro", func(from, to string) error {
		calls++
		if err := platformRename(from, to); err != nil || calls > 1 {
			return err
		}
		return &renamedError{path: to, err: errors.New("flush failed")}
	}))
	_, err := stagedTxn(t, dir).Apply("", Opts{Env: fakeEnv()})
	var re *renamedError
	if !errors.As(err, &re) || CodeOf(err) == CodeChanged {
		t.Fatalf("err %v", err)
	}
	if calls != 2 { // the failed one, then the rollback's restore
		t.Fatalf("%d renames of a.kicad_pro: retried?", calls)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" {
		t.Fatal("not rolled back")
	}
	clean(t, dir)
}

// readOnly makes a 0444 (Windows: read-only attribute) file and makes sure
// the test directory can still be removed.
func readOnly(t *testing.T, p, body string) {
	t.Helper()
	os.WriteFile(p, []byte(body), 0o644)
	os.Chmod(p, 0o444)
	t.Cleanup(func() { os.Chmod(p, 0o644) })
}

func isReadOnly(t *testing.T, p string) bool {
	t.Helper()
	st, err := os.Stat(p)
	return err == nil && st.Mode().Perm()&0o200 == 0
}

// A deleted read-only file is restored read-only by rollback and by recover,
// and recover can flush it (it does not stay pending).
func TestTxnReadOnlyOriginal(t *testing.T) {
	// Rollback.
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	ro := filepath.Join(dir, "a.aro")
	readOnly(t, ro, "frozen")
	tx := stagedTxn(t, dir)
	if err := tx.Delete(ro, nil); err != nil {
		t.Fatal(err)
	}
	injectRename(t, onBase("a.kicad_pro", func(string, string) error { return errors.New("fail") }))
	if _, err := tx.Apply("", Opts{Env: fakeEnv()}); err == nil || CodeOf(err) == CodeTxnPending {
		t.Fatalf("rollback: %v", err)
	}
	renameFile = platformRename
	if read(t, ro) != "frozen" || !isReadOnly(t, ro) {
		t.Fatalf("rollback: %q read-only=%v", read(t, ro), isReadOnly(t, ro))
	}
	clean(t, dir)

	// Recover after a crash that left it deleted.
	dir = project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	ro = filepath.Join(dir, "a.aro")
	readOnly(t, ro, "frozen")
	tx = stagedTxn(t, dir)
	tx.Delete(ro, nil)
	interrupt(t, tx)
	if read(t, ro) != "<missing>" {
		t.Fatal("setup: a.aro should be deleted")
	}
	rec, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
	if err != nil || rec.Action != "rolled-back" || read(t, ro) != "frozen" || !isReadOnly(t, ro) {
		t.Fatalf("recover: %+v %v read-only=%v", rec, err, isReadOnly(t, ro))
	}
	clean(t, dir)
}

// Backups and the journal are private to the user.
func TestTxnPrivatePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	checkPerms := func(dir, when string) {
		t.Helper()
		filepath.Walk(txnDir(dir), func(p string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			want := os.FileMode(0o600)
			if fi.IsDir() {
				want = 0o700
			}
			if fi.Mode().Perm() != want {
				t.Errorf("%s: %s is %v, want %v", when, p, fi.Mode().Perm(), want)
			}
			return nil
		})
	}
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	os.Chmod(filepath.Join(dir, "a.kicad_pcb"), 0o600)
	beforeMutate = func(string) { checkPerms(dir, "apply") }
	_, err := stagedTxn(t, dir).Apply("", Opts{Env: fakeEnv()})
	beforeMutate = nil
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(filepath.Join(dir, "a.kicad_pcb")); st.Mode().Perm() != 0o600 {
		t.Errorf("target mode %v", st.Mode().Perm())
	}

	dir = project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	interrupt(t, stagedTxn(t, dir))
	checkPerms(dir, "interrupted")
}

// The writer lock and the txn directory cannot be staged or deleted.
func TestTxnReservedPaths(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1"})
	w, err := AcquireWriter(dir, fakeEnv())
	if err != nil {
		t.Fatal(err)
	}
	w.Release()
	os.MkdirAll(txnDir(dir), 0o700)
	os.WriteFile(journalPath(dir)+".keep", []byte("x"), 0o600)
	tx := NewTxn(dir)
	reserved := []string{
		WriterLockPath(dir),
		journalPath(dir),
		filepath.Join(txnDir(dir), "0123456789abcdef", "000-a.kicad_pcb"),
		txnDir(dir),
	}
	// A case variant of the same path, where the platform or the filesystem
	// treats it as the same file.
	variant := filepath.Join(dir, ".PCBPILOT", "writer.lock")
	if samePath(variant, WriterLockPath(dir)) || sameFile(variant, WriterLockPath(dir)) {
		reserved = append(reserved, variant)
	}
	for _, p := range reserved {
		if err := tx.Stage(p, []byte("evil"), nil); err == nil || !strings.Contains(err.Error(), "transaction state") {
			t.Errorf("Stage(%s): %v", p, err)
		}
	}
	if err := tx.Delete(WriterLockPath(dir), nil); err == nil {
		t.Error("Delete(writer.lock) accepted")
	}
	for _, d := range []string{filepath.Join(dir, ".pcbpilot"), txnDir(dir)} {
		noTemps(t, d)
	}
	// A sibling under .pcbpilot that is not txn state is allowed.
	if err := tx.Stage(filepath.Join(dir, ".pcbpilot", "report.json"), []byte("{}"), nil); err != nil {
		t.Fatal(err)
	}
	tx.Abort()
}

// A staged file modified before or during Apply is refused.
func TestStagedTampering(t *testing.T) {
	staged := func(tx *Txn, dir, name string) string {
		return tx.changes[filepath.Join(dir, name)].staged
	}
	// Before Apply: refused in prepare, nothing written.
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	tx := stagedTxn(t, dir)
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(staged(tx, dir, "a.kicad_pro")); st.Mode().Perm() != 0o600 {
			t.Errorf("staged mode %v, want 0600 until install", st.Mode().Perm())
		}
	}
	os.WriteFile(staged(tx, dir, "a.kicad_pro"), []byte("tampered"), 0o600)
	if _, err := tx.Apply("", Opts{Env: fakeEnv()}); CodeOf(err) != CodeChanged {
		t.Fatalf("before apply: %v", err)
	}
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" {
		t.Fatal("before apply: files written")
	}
	tx.Abort()
	clean(t, dir)

	// Just before its rename: the earlier file is rolled back.
	dir = project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	tx = stagedTxn(t, dir)
	pro := staged(tx, dir, "a.kicad_pro")
	beforeMutate = func(p string) {
		if filepath.Base(p) == "a.kicad_pro" {
			os.WriteFile(pro, []byte("tampered"), 0o600)
		}
	}
	_, err := tx.Apply("", Opts{Env: fakeEnv()})
	beforeMutate = nil
	if CodeOf(err) != CodeChanged || read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, filepath.Join(dir, "a.kicad_pro")) != "P1" {
		t.Fatalf("during apply: %v", err)
	}
	clean(t, dir)
}

// Recover verifies and repairs every entry's final mode before it drops the
// journal: after a failed attribute restore, and on the completed path.
func TestRecoverSettlesModes(t *testing.T) {
	// The read-only attribute of a restored file is lost during its flush.
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	ro := filepath.Join(dir, "a.aro")
	readOnly(t, ro, "frozen")
	tx := stagedTxn(t, dir)
	tx.Delete(ro, nil)
	interrupt(t, tx)
	old := syncFile
	syncFile = func(p string) error {
		err := old(p)
		if filepath.Base(p) == "a.aro" {
			os.Chmod(p, 0o666)
			return errors.New("attribute restore failed")
		}
		return err
	}
	_, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
	syncFile = old
	if CodeOf(err) != CodeTxnPending || isReadOnly(t, ro) {
		t.Fatalf("setup: %v read-only=%v", err, isReadOnly(t, ro))
	}
	if _, err := Recover(dir, RecoverOpts{Env: fakeEnv()}); err != nil || !isReadOnly(t, ro) {
		t.Fatalf("retry: %v read-only=%v", err, isReadOnly(t, ro))
	}
	clean(t, dir)

	// Completed path: every change landed, one created file lost its mode.
	dir = project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	crashAfterLastMutation(t, stagedTxn(t, dir), dir, "new.kicad_dru")
	dru := filepath.Join(dir, "new.kicad_dru")
	os.Chmod(dru, 0o444)
	t.Cleanup(func() { os.Chmod(dru, 0o644) })
	rec, err := Recover(dir, RecoverOpts{Env: fakeEnv()})
	if err != nil || rec.Action != "completed" || isReadOnly(t, dru) {
		t.Fatalf("completed: %+v %v read-only=%v", rec, err, isReadOnly(t, dru))
	}
	clean(t, dir)
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestSymlinkTargetsRefused(t *testing.T) {
	dir := project(t, map[string]string{"real.kicad_pcb": "R1", "a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	link := filepath.Join(dir, "link.kicad_pcb")
	symlinkOrSkip(t, filepath.Join(dir, "real.kicad_pcb"), link)
	tx := NewTxn(dir)
	if err := tx.Stage(link, []byte("x"), nil); CodeOf(err) != CodeUnsupported {
		t.Errorf("Stage: %v", err)
	}
	if err := tx.Delete(link, nil); CodeOf(err) != CodeUnsupported {
		t.Errorf("Delete: %v", err)
	}
	if _, err := WriteFile(link, []byte("x"), Opts{Env: fakeEnv()}); CodeOf(err) != CodeUnsupported || ExitCode(err) != 1 {
		t.Errorf("WriteFile: %v", err)
	}
	if read(t, filepath.Join(dir, "real.kicad_pcb")) != "R1" {
		t.Fatal("referent written")
	}
	noTemps(t, dir)

	// Replaced by a link just before its rename: refused, earlier file rolled back.
	tx = stagedTxn(t, dir)
	pro := filepath.Join(dir, "a.kicad_pro")
	beforeMutate = func(p string) {
		if p == pro {
			os.Remove(pro)
			os.Symlink(filepath.Join(dir, "real.kicad_pcb"), pro)
		}
	}
	_, err := tx.Apply("", Opts{Env: fakeEnv()})
	beforeMutate = nil
	if CodeOf(err) != CodeUnsupported || read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" {
		t.Fatalf("mid-apply link: %v", err)
	}
	if fi, _ := os.Lstat(pro); fi.Mode()&os.ModeSymlink == 0 || read(t, filepath.Join(dir, "real.kicad_pcb")) != "R1" {
		t.Fatal("link or referent touched")
	}
	clean(t, dir)
}

// opLog records chmod, file flush, rename and directory flush in order.
func opLog(t *testing.T, failRenameTo string) *[]string {
	t.Helper()
	var log []string
	oc, of, or, od := chmodFile, syncFile, renameFile, syncDir
	chmodFile = func(p string, m os.FileMode) error { log = append(log, "chmod "+filepath.Base(p)); return oc(p, m) }
	syncFile = func(p string) error { log = append(log, "fsync "+filepath.Base(p)); return of(p) }
	renameFile = func(from, to string) error {
		if filepath.Base(to) == failRenameTo {
			return errors.New("fail")
		}
		log = append(log, "rename "+filepath.Base(from)+">"+filepath.Base(to))
		return or(from, to)
	}
	syncDir = func(d string) error { log = append(log, "dirsync "+d); return od(d) }
	t.Cleanup(func() { chmodFile, syncFile, renameFile, syncDir = oc, of, or, od })
	return &log
}

// assertInstallOrder checks chmod → fsync → rename → dir fsync for the temp
// file tmp installed as target in dir.
func assertInstallOrder(t *testing.T, log []string, tmp, target, dir string) {
	t.Helper()
	at := func(op string, from int) int {
		for i := from; i < len(log); i++ {
			if log[i] == op {
				return i
			}
		}
		return -1
	}
	c := at("chmod "+tmp, 0)
	f := at("fsync "+tmp, c+1)
	r := at("rename "+tmp+">"+target, f+1)
	d := at("dirsync "+dir, r+1)
	if c < 0 || f < 0 || r < 0 || d < 0 {
		t.Fatalf("order for %s: chmod %d fsync %d rename %d dirsync %d in %v", tmp, c, f, r, d, log)
	}
}

func TestInstallOrder(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	tx := stagedTxn(t, dir)
	staged := filepath.Base(tx.changes[filepath.Join(dir, "a.kicad_pcb")].staged)
	log := opLog(t, "a.kicad_pro")
	if _, err := tx.Apply("", Opts{Env: fakeEnv()}); err == nil {
		t.Fatal("no error")
	}
	assertInstallOrder(t, *log, staged, "a.kicad_pcb", dir)
	// The rollback restore of a.kicad_pcb uses a fresh temp file.
	var restoreTmp string
	for _, op := range *log {
		if strings.HasPrefix(op, "rename .a.kicad_pcb.pcbpilot-") && strings.HasSuffix(op, ">a.kicad_pcb") && !strings.Contains(op, staged) {
			restoreTmp = strings.TrimSuffix(strings.TrimPrefix(op, "rename "), ">a.kicad_pcb")
		}
	}
	if restoreTmp == "" {
		t.Fatalf("no restore rename in %v", *log)
	}
	assertInstallOrder(t, *log, restoreTmp, "a.kicad_pcb", dir)
	if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" {
		t.Fatal("not rolled back")
	}
}

// Discard flushes the files it keeps before the journal goes.
func TestDiscardFlushes(t *testing.T) {
	dir := project(t, map[string]string{"a.kicad_pcb": "A1", "a.kicad_pro": "P1"})
	interrupt(t, stagedTxn(t, dir))
	old := syncFile
	syncFile = func(string) error { return errors.New("flush failed") }
	_, err := Discard(dir, RecoverOpts{Env: fakeEnv()})
	syncFile = old
	if CodeOf(err) != CodeTxnPending || Pending(dir) == nil {
		t.Fatalf("flush failure: %v", err)
	}
	if rec, err := Discard(dir, RecoverOpts{Env: fakeEnv()}); err != nil || rec.Action != "discarded" {
		t.Fatalf("%+v %v", rec, err)
	}
	clean(t, dir)
}

// Mode 0 is a real mode once it is known.
func TestExactZeroMode(t *testing.T) {
	dir := t.TempDir()
	if p, _ := targetPerm(filepath.Join(dir, "n1"), 0, false); p != 0o644 {
		t.Errorf("default perm %v", p)
	}
	if p, _ := targetPerm(filepath.Join(dir, "n2"), 0, true); p != 0 {
		t.Errorf("exact zero perm %v", p)
	}
	nf := filepath.Join(dir, "zero.kicad_pcb")
	if _, err := WriteFile(nf, []byte("z"), Opts{NoLockCheck: true, Perm: 0, PermExact: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(nf, 0o644) })
	if st, _ := os.Stat(nf); !modeEq(st.Mode(), 0) {
		t.Errorf("new file mode %v, want 0000", st.Mode().Perm())
	}

	p := filepath.Join(dir, "f.kicad_pcb")
	os.WriteFile(p, []byte("x"), 0o644)
	t.Cleanup(func() { os.Chmod(p, 0o644) })
	snap, _ := Stat(p)
	e := journalEntry{Path: p, Kind: KindReplace, Existed: true, BaseSHA: snap.SHA256, NewSHA: "other", Mode: 0}
	if did, err := fixMode(e, guardOpts{noLock: true}); did || err != nil {
		t.Fatalf("unknown mode touched: %v %v", did, err)
	}
	e.HasMode = true
	if did, err := fixMode(e, guardOpts{noLock: true}); !did || err != nil {
		t.Fatalf("exact 0: %v %v", did, err)
	}
	if st, _ := os.Stat(p); !modeEq(st.Mode(), 0) {
		t.Errorf("mode %v, want 0000", st.Mode().Perm())
	}
}
