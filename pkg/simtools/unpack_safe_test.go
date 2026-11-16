package simtools

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// leftovers lists the staging / backup / lock entries beside dest.
func leftovers(t *testing.T, dest string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.Name() != filepath.Base(dest) {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestUnpackZipLeavesNoStagingBehind(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "elmer", "rel1")
	for i := 0; i < 2; i++ { // fresh install, then a replacement
		if err := UnpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect); err != nil {
			t.Fatal(err)
		}
		if l := leftovers(t, dest); len(l) != 0 {
			t.Fatalf("run %d left %v beside the install", i, l)
		}
	}
	if err := UnpackZip(writeZip(t, []zipEntry{{name: "top/readme.txt", body: "x"}, {name: "top/bin/x", body: "x"}}), dest, nil, elmerExpect); err == nil {
		t.Fatal("wrong archive accepted")
	}
	if l := leftovers(t, dest); len(l) != 0 {
		t.Fatalf("a refused archive left %v", l)
	}
}

// The new tree cannot take the old one's place (file lock, virus scanner):
// the old install must still be there, complete.
func TestUnpackZipRestoresTheOldInstallWhenPromotionFails(t *testing.T) {
	old := unpackRetryStep
	unpackRetryStep = time.Millisecond
	defer func() { unpackRetryStep = old }()

	dest := filepath.Join(t.TempDir(), "elmer", "rel1")
	if err := UnpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dest, "bin", "mine.txt")
	if err := os.WriteFile(marker, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	fail := func(from, to string) error {
		if strings.Contains(filepath.Base(from), ".stage-") {
			return errors.New("access denied (synthetic)")
		}
		return os.Rename(from, to)
	}
	err := unpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect, fail)
	if err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("err = %v", err)
	}
	if b, rerr := os.ReadFile(marker); rerr != nil || string(b) != "old" {
		t.Fatalf("the old install was not put back: %v %q", rerr, b)
	}
	if b, rerr := os.ReadFile(filepath.Join(dest, "bin", "ElmerSolver.exe")); rerr != nil || string(b) != "solver" {
		t.Fatalf("the old install is damaged: %v %q", rerr, b)
	}
	if l := leftovers(t, dest); len(l) != 0 {
		t.Fatalf("left %v", l)
	}

	// the old install cannot be moved aside (in use): nothing changes
	busy := func(from, to string) error {
		if from == dest {
			return errors.New("in use (synthetic)")
		}
		return os.Rename(from, to)
	}
	if err := unpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect, busy); err == nil || !strings.Contains(err.Error(), "cannot replace") {
		t.Fatalf("err = %v", err)
	}
	if b, rerr := os.ReadFile(marker); rerr != nil || string(b) != "old" {
		t.Fatalf("the old install changed: %v %q", rerr, b)
	}
	if l := leftovers(t, dest); len(l) != 0 {
		t.Fatalf("left %v", l)
	}
}

// Several installs of one release at once: each one finishes, the result is
// one complete tree, and nobody deletes anybody's staging folder.
func TestUnpackZipConcurrentInstalls(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "elmer", "rel1")
	archive := writeZip(t, fakeElmerZip())
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = UnpackZip(archive, dest, []string{"stripped_gfortran"}, elmerExpect)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}
	for _, f := range elmerExpect {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(f))); err != nil {
			t.Fatal(err)
		}
	}
	if l := leftovers(t, dest); len(l) != 0 {
		t.Fatalf("left %v", l)
	}
}

// A running install makes the second one wait and then say so; a lock file
// left behind by a killed process holds nobody up (the lock is the operating
// system's, not the file's existence).
func TestUnpackZipLock(t *testing.T) {
	oldWait := unpackLockWait
	defer func() { unpackLockWait = oldWait }()
	unpackLockWait = 300 * time.Millisecond
	dest := filepath.Join(t.TempDir(), "elmer", "rel1")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockDest(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := UnpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect); err == nil || !strings.Contains(err.Error(), "another install") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("installed without the lock")
	}
	unlock()
	// a leftover lock file nobody holds
	if err := os.WriteFile(filepath.Join(filepath.Dir(dest), ".lock-rel1"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UnpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect); err != nil {
		t.Fatal(err)
	}
	if l := leftovers(t, dest); len(l) != 0 {
		t.Fatalf("left %v", l)
	}
}

// Many waiters on one lock: never two holders at once.
func TestLockDestIsExclusive(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "rel1")
	var mu sync.Mutex
	holders, worst := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 5; k++ {
				unlock, err := lockDest(dest)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				holders++
				if holders > worst {
					worst = holders
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				holders--
				mu.Unlock()
				unlock()
			}
		}()
	}
	wg.Wait()
	if worst != 1 {
		t.Fatalf("%d installs held the lock at once", worst)
	}
}

// The new tree cannot be promoted and the old one is briefly held too: it is
// put back on a later attempt.
func TestUnpackZipRetriesTheRestore(t *testing.T) {
	old := unpackRetryStep
	unpackRetryStep = time.Millisecond
	defer func() { unpackRetryStep = old }()
	dest := filepath.Join(t.TempDir(), "elmer", "rel1")
	if err := UnpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect); err != nil {
		t.Fatal(err)
	}
	restores := 0
	flaky := func(from, to string) error {
		if strings.Contains(filepath.Base(from), ".stage-") {
			return errors.New("access denied (synthetic)")
		}
		if to == dest {
			if restores++; restores < 3 {
				return errors.New("held by a scanner (synthetic)")
			}
		}
		return os.Rename(from, to)
	}
	if err := unpackZip(writeZip(t, fakeElmerZip()), dest, nil, elmerExpect, flaky); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin", "ElmerSolver.exe")); err != nil {
		t.Fatalf("the old install was not put back: %v", err)
	}
	if l := leftovers(t, dest); len(l) != 0 {
		t.Fatalf("left %v", l)
	}
}

// Windows resolves "..  " and "..." like "..", and NUL / COM1.txt are
// devices: such entries are refused on every platform.
func TestCleanZipNameRefusesWin32Aliases(t *testing.T) {
	for _, n := range []string{"top/.. /x", "top/.../x", "top/..  ./x", "top/dir./x", "top/dir /x", "top/NUL", "top/nul.txt", "top/COM1.log", "top/a/LPT9", "top/CONIN$", "top/conout$.txt", "top/COM¹", `top\.. \x`} {
		if _, err := cleanZipName(n); err == nil {
			t.Errorf("%q accepted", n)
		}
	}
	for _, n := range []string{"top/bin/ElmerSolver.exe", "top/./bin/x", "top/share/a.b.c", "top/console.txt", "top/com10.txt", "top/.hidden/x"} {
		if _, err := cleanZipName(n); err != nil {
			t.Errorf("%q refused: %v", n, err)
		}
	}
}

// A staging or moved-aside folder is never taken for an install.
func TestLocateIgnoresUnfinishedInstalls(t *testing.T) {
	home := t.TempDir()
	e, _ := newFake("windows")
	e.Home = func() (string, error) { return home, nil }
	e.Glob = filepath.Glob
	el := tool(t, "elmer")
	root := e.UserToolDir(el)
	for _, d := range []string{".stage-rel9-123", ".old-rel9-456", "rel9.partial"} {
		bin := filepath.Join(root, d, "bin")
		if err := os.MkdirAll(bin, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bin, "ElmerSolver.exe"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := e.Locate(el, "ElmerSolver"); p != "" {
		t.Fatalf("an unfinished install was located: %s", p)
	}
	bin := filepath.Join(root, "rel8", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ElmerSolver.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.Locate(el, "ElmerSolver"); p != filepath.Join(bin, "ElmerSolver.exe") {
		t.Fatalf("located %q", p)
	}
}
