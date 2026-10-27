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
// the project has its own "~<project>.kicad_pro.lck", kicad-cli takes no lock,
// KiCad logs "Detected stale lock file, removing it", and the GUI does not
// keep the document open while editing. The lock content has not been
// observed from a real session: it is believed to be a JSON object with
// "hostname" and "username" and no pid. parseLock therefore accepts exactly
// that (plus the short keys "host"/"user") and classifies anything else as
// LockUnreadable, which blocks like a live lock.

// LockState classifies a lock file found next to a target.
type LockState string

const (
	// LockLive: held by this user on this machine with KiCad running, or by
	// another user here. Blocks.
	LockLive LockState = "live"
	// LockStaleCandidate: this user on this machine, and no kicad / pcbnew /
	// eeschema process is running. Probably left by a crash. Blocks unless the
	// policy is BreakStale or BreakAny.
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

// CheckLocks returns the lock files that guard targets: each target's own
// lock, every project lock (~*.kicad_pro.lck) in the nearest directory at or
// above the target that holds a .kicad_pro, and for a .kicad_sch target the
// root sheet lock (~<project>.kicad_sch.lck) of each project there. Sorted by
// lock path, deduplicated.
func CheckLocks(targets []string, env *Env) []LockInfo {
	env = envOr(env)
	seen := map[string]bool{}
	var out []LockInfo
	add := func(lck, target, kind string) {
		if seen[lck] {
			return
		}
		st, err := os.Stat(lck)
		if err != nil || st.IsDir() {
			return
		}
		seen[lck] = true
		out = append(out, classify(lck, target, kind, st.ModTime(), env))
	}
	for _, t := range targets {
		if abs, err := filepath.Abs(t); err == nil {
			t = abs
		}
		add(LockPath(t), t, "file")
		proj := projectDir(filepath.Dir(t))
		if proj == "" {
			continue
		}
		ents, _ := os.ReadDir(proj)
		for _, e := range ents {
			n := e.Name()
			if strings.HasPrefix(n, "~") && strings.HasSuffix(n, ".kicad_pro.lck") {
				add(filepath.Join(proj, n), filepath.Join(proj, strings.TrimSuffix(n[1:], ".lck")), "project")
			}
			if strings.HasSuffix(t, ".kicad_sch") && strings.HasSuffix(n, ".kicad_pro") && !e.IsDir() {
				root := filepath.Join(proj, strings.TrimSuffix(n, ".kicad_pro")+".kicad_sch")
				add(LockPath(root), root, "root-sheet")
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// projectDir is the nearest directory at or above dir (8 levels at most)
// holding a *.kicad_pro, or "".
func projectDir(dir string) string {
	for i := 0; i < 8; i++ {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".kicad_pro") {
				return dir
			}
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return ""
}

func classify(lck, target, kind string, mod time.Time, env *Env) LockInfo {
	li := LockInfo{Path: lck, Target: target, Kind: kind, Mod: mod, State: LockUnreadable}
	b, err := os.ReadFile(lck)
	if err != nil {
		return li
	}
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

func sameHost(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == b {
		return a != ""
	}
	// "box" and "box.example.lan" name the same machine.
	a, _, _ = strings.Cut(a, ".")
	b, _, _ = strings.Cut(b, ".")
	return a != "" && a == b
}

func sameUser(a, b string) bool {
	norm := func(s string) string {
		if i := strings.LastIndex(s, `\`); i >= 0 {
			s = s[i+1:] // DOMAIN\user
		}
		return strings.ToLower(s)
	}
	return norm(a) != "" && norm(a) == norm(b)
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

// isKiCadGUI: kicad, pcbnew, eeschema (any case, optional .exe, any path).
// kicad-cli takes no locks and does not count.
func isKiCadGUI(name string) bool {
	n := strings.ToLower(filepath.Base(strings.ReplaceAll(name, `\`, "/")))
	n = strings.TrimSuffix(n, ".exe")
	return n == "kicad" || n == "pcbnew" || n == "eeschema"
}

// Guard checks the locks of targets and removes those the policy allows.
// It returns every lock found (Broke set on the removed ones) and an
// E_KICAD_LOCKED *Error if any lock still blocks.
func Guard(targets []string, p BreakPolicy, env *Env) ([]LockInfo, error) {
	locks := CheckLocks(targets, env)
	var block []string
	var first string
	for i := range locks {
		l := &locks[i]
		if p.allows(l.State) {
			if err := os.Remove(l.Path); err == nil || errors.Is(err, fs.ErrNotExist) {
				l.Broke = true
				continue
			}
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
