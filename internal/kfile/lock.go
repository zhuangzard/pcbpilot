package kfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// KiCad lock files.
//
// What was verified (KiCad 10.0 on Windows): the lock of a document is
// "~<file name>.lck" in the document's directory (e.g. ~board.kicad_pcb.lck),
// the project has its own "~<project>.kicad_pro.lck", KiCad logs "Detected
// stale lock file, removing it", and the GUI does not keep the document open
// while editing. kicad-cli is not lock-free: `kicad-cli pcb drc` (10.0.6)
// holds "~<project>.kicad_pro.lck" while it runs and removes it when done,
// so a write next to a board being checked is refused like one next to an
// open GUI. The content observed from that lock is a compact JSON object
// {"hostname":…,"username":…} with no pid (testdata/locks/kicad10-real.lck,
// names neutralised). parseLock accepts exactly that (plus the short keys
// "host"/"user") and classifies anything else as LockUnreadable, which
// blocks like a live lock.

// LockState classifies a lock file found next to a target.
type LockState string

const (
	// LockLive: held by this user on this machine with KiCad running, or by
	// another user here. Blocks.
	LockLive LockState = "live"
	// LockStaleCandidate: this user on this machine, and no kicad / pcbnew /
	// eeschema / kicad-cli process is running. Probably left by a crash.
	// Blocks unless the policy is BreakStale or BreakAny.
	LockStaleCandidate LockState = "stale-candidate"
	// LockForeign: written by another host (shared or synced folder). Its
	// liveness cannot be checked, so it blocks like a live lock.
	LockForeign LockState = "foreign"
	// LockUnreadable: empty or not in the expected format. Blocks.
	LockUnreadable LockState = "unreadable"
)

// LockInfo is one lock file and what it says.
type LockInfo struct {
	Path   string    `json:"path"`   // the ~….lck file
	Target string    `json:"target"` // the file it locks
	Kind   string    `json:"kind"`   // file | project | root-sheet
	User   string    `json:"user,omitempty"`
	Host   string    `json:"host,omitempty"`
	Mod    time.Time `json:"mod"`
	State  LockState `json:"state"`
	Broke  bool      `json:"broke,omitempty"` // removed under the BreakPolicy
	raw    []byte    // content when classified, to detect a lock rewritten before removal
	size   int64
}

// BreakPolicy says which locks a writer may remove. The zero value is
// BreakNone.
type BreakPolicy string

const (
	BreakNone  BreakPolicy = "none"
	BreakStale BreakPolicy = "stale" // remove LockStaleCandidate only
	BreakAny   BreakPolicy = "any"   // remove every lock (the user's explicit decision)
)

// ParseBreakPolicy parses a --break-lock value.
func ParseBreakPolicy(s string) (BreakPolicy, error) {
	switch BreakPolicy(strings.ToLower(strings.TrimSpace(s))) {
	case "", BreakNone:
		return BreakNone, nil
	case BreakStale:
		return BreakStale, nil
	case BreakAny:
		return BreakAny, nil
	}
	return BreakNone, fmt.Errorf("--break-lock %q: want none, stale or any", s)
}

func (p BreakPolicy) allows(s LockState) bool {
	switch p {
	case BreakAny:
		return true
	case BreakStale:
		return s == LockStaleCandidate
	}
	return false
}

// Proc is one running process.
type Proc struct {
	PID  int
	Name string // executable name; a path is allowed
}

// ProcLister lists running processes (SystemProcs, or a fake in tests).
type ProcLister func() ([]Proc, error)

// Env is the identity locks are compared against.
type Env struct {
	Host  string
	User  string
	Procs ProcLister
}

var systemEnv = sync.OnceValue(func() *Env {
	h, _ := os.Hostname()
	u := os.Getenv("USER")
	if runtime.GOOS == "windows" {
		u = os.Getenv("USERNAME")
	}
	if u == "" {
		if cu, err := user.Current(); err == nil {
			u = cu.Username
		}
	}
	return &Env{Host: h, User: u, Procs: SystemProcs}
})

// SystemEnv is this machine's host name, user and process list.
func SystemEnv() *Env { return systemEnv() }

func envOr(e *Env) *Env {
	if e == nil {
		return SystemEnv()
	}
	return e
}

// LockPath is the KiCad lock file of path: "~<base>.lck" beside it.
func LockPath(path string) string {
	return filepath.Join(filepath.Dir(path), "~"+filepath.Base(path)+".lck")
}

// Filesystem probes, replaceable in tests to inject I/O errors.
var (
	statFn    = os.Stat
	readDirFn = os.ReadDir
)

// CheckLocks returns the lock files that guard targets: each target's own
// lock, every project lock (~*.kicad_pro.lck) in the nearest directory at or
// above the target that holds a .kicad_pro, and for a .kicad_sch target the
// root sheet lock (~<project>.kicad_sch.lck) of each project there. Sorted by
// lock path, deduplicated.
//
// It fails closed: when the absence of a lock cannot be established (a stat
// or directory listing fails for a reason other than "does not exist", or a
// directory sits at a lock path) it returns an E_KICAD_LOCKED *Error.
func CheckLocks(targets []string, env *Env) ([]LockInfo, error) {
	env = envOr(env)
	seen := map[string]bool{}
	var out []LockInfo
	add := func(lck, target, kind string) error {
		if seen[lck] {
			return nil
		}
		st, err := statFn(lck)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return uncheckable(target, "cannot stat lock "+lck+": "+err.Error(), err)
		}
		if st.IsDir() {
			return uncheckable(target, "a directory sits at the lock path "+lck, nil)
		}
		seen[lck] = true
		out = append(out, classify(lck, target, kind, st.ModTime(), env))
		return nil
	}
	for _, t := range targets {
		if abs, err := filepath.Abs(t); err == nil {
			t = abs
		}
		if err := add(LockPath(t), t, "file"); err != nil {
			return out, err
		}
		proj, ents, err := projectDir(filepath.Dir(t))
		if err != nil {
			return out, uncheckable(t, err.Error(), err)
		}
		for _, e := range ents {
			n := e.Name()
			if strings.HasPrefix(n, "~") && strings.HasSuffix(n, ".kicad_pro.lck") {
				if err := add(filepath.Join(proj, n), filepath.Join(proj, strings.TrimSuffix(n[1:], ".lck")), "project"); err != nil {
					return out, err
				}
			}
			if strings.HasSuffix(t, ".kicad_sch") && strings.HasSuffix(n, ".kicad_pro") && !e.IsDir() {
				root := filepath.Join(proj, strings.TrimSuffix(n, ".kicad_pro")+".kicad_sch")
				if err := add(LockPath(root), root, "root-sheet"); err != nil {
					return out, err
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func uncheckable(target, detail string, err error) *Error {
	return &Error{Code: CodeLocked, Path: target, Detail: "cannot establish that the file is not open in KiCad: " + detail, Err: err,
		Fix: "fix the permissions or remove the entry named in the detail, then retry"}
}

// projectDir is the nearest directory at or above dir, up to the filesystem
// root, holding a *.kicad_pro, with its entries; "" when there is none. A
// directory that does not exist is skipped; any other listing error is
// returned.
func projectDir(dir string) (string, []fs.DirEntry, error) {
	for {
		ents, err := readDirFn(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("cannot list %s: %w", dir, err)
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".kicad_pro") {
				return dir, ents, nil
			}
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return "", nil, nil
}

func classify(lck, target, kind string, mod time.Time, env *Env) LockInfo {
	li := LockInfo{Path: lck, Target: target, Kind: kind, Mod: mod, State: LockUnreadable, size: -1}
	b, err := os.ReadFile(lck)
	if err != nil {
		return li
	}
	li.raw, li.size = b, int64(len(b))
	host, usr, ok := parseLock(b)
	if !ok {
		return li
	}
	li.Host, li.User = host, usr
	switch {
	case !sameHost(host, env.Host):
		li.State = LockForeign
	case !sameUser(usr, env.User):
		li.State = LockLive
	case kicadRunning(env.Procs):
		li.State = LockLive
	default:
		li.State = LockStaleCandidate
	}
	return li
}

// parseLock reads {"hostname":…, "username":…}; see the note at the top.
func parseLock(b []byte) (host, usr string, ok bool) {
	b = bytes.TrimSpace(bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")))
	var m map[string]any
	if len(b) == 0 || json.Unmarshal(b, &m) != nil {
		return "", "", false
	}
	str := func(keys ...string) string {
		for k, v := range m {
			for _, want := range keys {
				if s, isStr := v.(string); isStr && strings.EqualFold(k, want) {
					return strings.TrimSpace(s)
				}
			}
		}
		return ""
	}
	host, usr = str("hostname", "host"), str("username", "user")
	return host, usr, host != "" && usr != ""
}

// sameHost compares a recorded host name with local, this machine's name.
// Host names are case-insensitive. Different full names are different
// machines (box.a.example is not box.b.example); the one exception is a
// recorded short name that is local's own first label (KiCad may record
// "box" where os.Hostname gives "box.lab.lan"). Anything else is foreign.
func sameHost(recorded, local string) bool {
	r, l := strings.ToLower(strings.TrimSpace(recorded)), strings.ToLower(strings.TrimSpace(local))
	if r == "" || l == "" {
		return false
	}
	if r == l {
		return true
	}
	short, _, _ := strings.Cut(l, ".")
	return !strings.Contains(r, ".") && r == short
}

// sameUser compares full user names (DOMAIN\user included), ignoring case
// only on Windows, where account names are case-insensitive.
func sameUser(recorded, local string) bool {
	r, l := strings.TrimSpace(recorded), strings.TrimSpace(local)
	if r == "" || l == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(r, l)
	}
	return r == l
}

// kicadRunning is true when the GUI may hold the lock, or when the process
// list is unavailable (a lock is never called stale without proof).
func kicadRunning(list ProcLister) bool {
	if list == nil {
		return true
	}
	ps, err := list()
	if err != nil {
		return true
	}
	for _, p := range ps {
		if isKiCadGUI(p.Name) {
			return true
		}
	}
	return false
}

// isKiCadGUI: a KiCad process that may hold a lock — kicad, pcbnew,
// eeschema, and kicad-cli, which holds the project lock while `pcb drc`
// runs (10.0.6) — any case, optional .exe, any path.
func isKiCadGUI(name string) bool {
	n := strings.ToLower(filepath.Base(strings.ReplaceAll(name, `\`, "/")))
	n = strings.TrimSuffix(n, ".exe")
	return n == "kicad" || n == "pcbnew" || n == "eeschema" || n == "kicad-cli"
}

// beforeLockRemove, when set by a test, runs just before a lock is re-read
// for removal.
var beforeLockRemove func(path string)

// removeLock deletes a lock the policy allows to break, but only if it is
// still the lock that was classified: same size, mtime and content, and (for
// a stale candidate) still no KiCad process. A KiCad that starts and
// rewrites the lock between this re-check and the delete — microseconds —
// is the window that remains.
func removeLock(l LockInfo, env *Env) error {
	if beforeLockRemove != nil {
		beforeLockRemove(l.Path)
	}
	changed := func(why string) error {
		return &Error{Code: CodeLocked, Path: l.Target, Detail: "lock " + l.Path + " " + why + " while it was being removed",
			Fix: "KiCad may have just opened the file; close it and retry"}
	}
	st, err := statFn(l.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return uncheckable(l.Target, "cannot re-stat lock "+l.Path+": "+err.Error(), err)
	}
	b, rerr := os.ReadFile(l.Path)
	if rerr != nil || !st.ModTime().Equal(l.Mod) || st.Size() != l.size || !bytes.Equal(b, l.raw) {
		return changed("changed")
	}
	if l.State == LockStaleCandidate && kicadRunning(envOr(env).Procs) {
		return changed("became live (KiCad started)")
	}
	if err := os.Remove(l.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return uncheckable(l.Target, "cannot remove lock "+l.Path+": "+err.Error(), err)
	}
	return nil
}

// Guard checks the locks of targets and removes those the policy allows.
// It returns every lock found (Broke set on the removed ones) and an
// E_KICAD_LOCKED *Error if any lock still blocks, a lock changed while being
// removed, or the check itself failed.
func Guard(targets []string, p BreakPolicy, env *Env) ([]LockInfo, error) {
	locks, err := CheckLocks(targets, env)
	if err != nil {
		return locks, err
	}
	var block []string
	var first string
	for i := range locks {
		l := &locks[i]
		if p.allows(l.State) {
			if err := removeLock(*l, env); err != nil {
				return locks, err
			}
			l.Broke = true
			continue
		}
		if first == "" {
			first = l.Target
		}
		who := l.User + "@" + l.Host
		if l.State == LockUnreadable {
			who = "unknown owner"
		}
		block = append(block, fmt.Sprintf("%s (%s, %s)", filepath.Base(l.Path), l.State, who))
	}
	if len(block) == 0 {
		return locks, nil
	}
	return locks, &Error{
		Code:   CodeLocked,
		Path:   first,
		Detail: "open in KiCad: " + strings.Join(block, "; "),
		Fix:    "close the file in KiCad and retry; --break-lock stale removes a lock left by a crashed session on this machine, --break-lock any removes every lock",
		Locks:  locks,
	}
}
