package kfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		want, proWant := CodeChanged, "saved by KiCad"
		if mode == "lock" {
			want, proWant = CodeLocked, "P1"
			os.Remove(LockPath(pro))
		}
		if CodeOf(err) != want {
			t.Errorf("%s: err %v", mode, err)
		}
		if read(t, filepath.Join(dir, "a.kicad_pcb")) != "A1" || read(t, pro) != proWant {
			t.Errorf("%s: pcb %q pro %q", mode, read(t, filepath.Join(dir, "a.kicad_pcb")), read(t, pro))
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
		if base == "journal.json" || (base == "a.kicad_pcb" || base == "a.del") && !landed[base] {
			landed[base] = true
			return platformRename(from, to)
		}
		return errors.New("power cut")
	}
	oldRm := removeFile
	removeFile = func(p string) error {
		base := filepath.Base(p)
		if base == "a.del" && !landed[base] {
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
