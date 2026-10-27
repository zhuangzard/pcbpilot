package kfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fakeEnv(procs ...string) *Env {
	return &Env{Host: "box", User: "alice", Procs: func() ([]Proc, error) {
		out := []Proc{{PID: 1, Name: "init"}}
		for i, n := range procs {
			out = append(out, Proc{PID: 100 + i, Name: n})
		}
		return out, nil
	}}
}

func writeLock(t *testing.T, target, body string) string {
	t.Helper()
	p := LockPath(target)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// lockBody is the believed KiCad 10 content (see the note in lock.go).
const lockBody = `{"hostname":"BOX","username":"alice"}`

func TestLockPath(t *testing.T) {
	got := LockPath(filepath.Join("a", "b", "ecc83-pp.kicad_pcb"))
	if want := filepath.Join("a", "b", "~ecc83-pp.kicad_pcb.lck"); got != want {
		t.Fatalf("LockPath = %q, want %q", got, want)
	}
}

func TestParseLock(t *testing.T) {
	cases := []struct {
		name, body string
		ok         bool
	}{
		{"kicad json", lockBody, true},
		{"bom and spaces", "\xef\xbb\xbf  {\"hostname\": \"box\", \"username\": \"alice\"}\n", true},
		{"short keys", `{"host":"box","user":"alice"}`, true},
		{"empty", "", false},
		{"whitespace", "  \n", false},
		{"truncated", `{"hostname":"box","user`, false},
		{"garbage", "\x00\x01lock", false},
		{"plain text", "alice@box", false},
		{"missing user", `{"hostname":"box"}`, false},
		{"non-string", `{"hostname":1,"username":2}`, false},
		{"array", `["box","alice"]`, false},
	}
	for _, c := range cases {
		h, u, ok := parseLock([]byte(c.body))
		if ok != c.ok {
			t.Errorf("%s: ok = %v (host %q user %q)", c.name, ok, h, u)
		}
	}
}

func TestLockStates(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.kicad_pcb")
	cases := []struct {
		name, body string
		env        *Env
		want       LockState
	}{
		{"same user, no kicad", lockBody, fakeEnv("explorer.exe", "kicad-cli.exe", "python.exe"), LockStaleCandidate},
		{"same user, pcbnew running", lockBody, fakeEnv("PCBNEW.EXE"), LockLive},
		{"same user, kicad running (path)", lockBody, fakeEnv("/Applications/KiCad/KiCad.app/Contents/MacOS/kicad"), LockLive},
		{"same user, eeschema running", lockBody, fakeEnv("eeschema"), LockLive},
		{"same user, no process list", lockBody, &Env{Host: "box", User: "alice", Procs: func() ([]Proc, error) { return nil, errors.New("denied") }}, LockLive},
		{"domain user and fqdn host", `{"hostname":"box.lab.lan","username":"CORP\\Alice"}`, fakeEnv(), LockStaleCandidate},
		{"other user here", `{"hostname":"box","username":"bob"}`, fakeEnv(), LockLive},
		{"other host", `{"hostname":"far","username":"alice"}`, fakeEnv(), LockForeign},
		{"unreadable", "garbage", fakeEnv(), LockUnreadable},
	}
	for _, c := range cases {
		writeLock(t, target, c.body)
		ls := CheckLocks([]string{target}, c.env)
		if len(ls) != 1 || ls[0].State != c.want || ls[0].Kind != "file" || ls[0].Target != target {
			t.Errorf("%s: got %+v, want one %s file lock", c.name, ls, c.want)
		}
	}
}

func TestGuardPolicies(t *testing.T) {
	type tc struct {
		state  string
		policy BreakPolicy
		broke  bool
	}
	bodies := map[string]string{
		"stale":      lockBody,
		"live":       `{"hostname":"box","username":"bob"}`,
		"foreign":    `{"hostname":"far","username":"alice"}`,
		"unreadable": "",
	}
	cases := []tc{
		{"stale", BreakNone, false}, {"stale", BreakStale, true}, {"stale", BreakAny, true},
		{"live", BreakStale, false}, {"live", BreakAny, true},
		{"foreign", BreakStale, false}, {"foreign", BreakAny, true},
		{"unreadable", BreakStale, false}, {"unreadable", BreakAny, true},
	}
	for _, c := range cases {
		dir := t.TempDir()
		target := filepath.Join(dir, "x.kicad_sch")
		lck := writeLock(t, target, bodies[c.state])
		locks, err := Guard([]string{target}, c.policy, fakeEnv())
		if len(locks) != 1 || locks[0].Broke != c.broke {
			t.Errorf("%s/%s: locks %+v", c.state, c.policy, locks)
		}
		_, statErr := os.Stat(lck)
		if c.broke {
			if err != nil || statErr == nil {
				t.Errorf("%s/%s: err %v, lock still there: %v", c.state, c.policy, err, statErr == nil)
			}
			continue
		}
		var ke *Error
		if !errors.As(err, &ke) || ke.Code != CodeLocked || ke.Fix == "" || len(ke.Locks) != 1 || ExitCode(err) != 4 {
			t.Errorf("%s/%s: err = %v", c.state, c.policy, err)
		}
		if statErr != nil {
			t.Errorf("%s/%s: lock removed without permission", c.state, c.policy)
		}
	}
	if locks, err := Guard([]string{filepath.Join(t.TempDir(), "free.kicad_pcb")}, BreakNone, fakeEnv()); err != nil || len(locks) != 0 {
		t.Errorf("no lock: %v %v", locks, err)
	}
}

func TestProjectAndRootSheetLocks(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"demo.kicad_pro", "demo.kicad_sch", "demo.kicad_pcb"} {
		os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644)
	}
	sub := filepath.Join(dir, "sheets")
	os.Mkdir(sub, 0o755)
	child := filepath.Join(sub, "power.kicad_sch")
	os.WriteFile(child, []byte("x"), 0o644)

	writeLock(t, filepath.Join(dir, "demo.kicad_pro"), lockBody)
	writeLock(t, filepath.Join(dir, "demo.kicad_sch"), lockBody)

	ls := CheckLocks([]string{child}, fakeEnv("eeschema.exe"))
	kinds := map[string]string{}
	for _, l := range ls {
		kinds[filepath.Base(l.Path)] = l.Kind
		if l.State != LockLive {
			t.Errorf("%s: %s", l.Path, l.State)
		}
	}
	if kinds["~demo.kicad_pro.lck"] != "project" || kinds["~demo.kicad_sch.lck"] != "root-sheet" || len(ls) != 2 {
		t.Fatalf("child sheet locks = %+v", ls)
	}
	// A board gets the project lock but not the root sheet lock.
	ls = CheckLocks([]string{filepath.Join(dir, "demo.kicad_pcb")}, fakeEnv())
	if len(ls) != 1 || ls[0].Kind != "project" {
		t.Fatalf("board locks = %+v", ls)
	}
	// The root sheet's own lock is reported once.
	ls = CheckLocks([]string{filepath.Join(dir, "demo.kicad_sch"), child}, fakeEnv())
	if len(ls) != 2 {
		t.Fatalf("dedupe: %+v", ls)
	}
}

func TestParseBreakPolicy(t *testing.T) {
	for in, want := range map[string]BreakPolicy{"": BreakNone, "none": BreakNone, "Stale": BreakStale, "any": BreakAny} {
		if got, err := ParseBreakPolicy(in); err != nil || got != want {
			t.Errorf("ParseBreakPolicy(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseBreakPolicy("all"); err == nil {
		t.Error("ParseBreakPolicy(all) accepted")
	}
}

func TestSystemProcsSeesSelf(t *testing.T) {
	ps, err := SystemProcs()
	if err != nil {
		t.Skipf("process list unavailable: %v", err)
	}
	for _, p := range ps {
		if p.PID == os.Getpid() {
			return
		}
	}
	t.Fatalf("own pid %d not in %d processes", os.Getpid(), len(ps))
}
