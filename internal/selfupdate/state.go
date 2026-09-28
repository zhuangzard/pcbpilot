package selfupdate

// Persistent update state under ~/.pcbpilot. Every file is small JSON written
// atomically (temp + rename); readers treat a missing or corrupt file as empty.
//
//	install.json       how this machine was installed (release | source + repo)
//	config.json        user settings: autoUpdate on|off|source
//	update-check.json  last release check (daemon: startup + every 6h, backoff offline)
//	update-state.json  last applied update: phase, from/to, components, rollback
//	session.json       CLI session marker (first command after 30 min idle)
//	update.log         append-only log of every check / apply / rollback
//	update.lock        held while an update is being applied
//	connector/         downloaded pcbpilot-connector-vX.Y.Z.eext
//	mcp/<ver>, mcp/current  installed MCP server package
//	rollback/<ver>/    previous binary + skill dirs + MCP link

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Home returns ~/.pcbpilot (the state root).
func Home() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return ""
	}
	return filepath.Join(h, ".pcbpilot")
}

func statePath(name string) string {
	if h := Home(); h != "" {
		return filepath.Join(h, name)
	}
	return ""
}

// ReadJSON loads path into v; a missing file leaves v untouched and returns nil.
func ReadJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// WriteJSON writes v atomically (same-dir temp + rename).
func WriteJSON(path string, v any) error {
	if path == "" {
		return fmt.Errorf("no state path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ── install.json ──────────────────────────────────────────────────────────

// InstallInfo is written by the installers. Kind "source" + Repo marks a git
// checkout install (scripts/setup-agent.sh): `pcbpilot update` then pulls the
// checkout and re-runs setup instead of downloading release assets.
type InstallInfo struct {
	Kind        string `json:"kind"` // release | source
	Repo        string `json:"repo,omitempty"`
	Bin         string `json:"bin,omitempty"`
	Version     string `json:"version,omitempty"`
	InstalledAt string `json:"installedAt,omitempty"`
}

func InstallInfoPath() string { return statePath("install.json") }

func ReadInstallInfo() InstallInfo {
	var i InstallInfo
	_ = ReadJSON(InstallInfoPath(), &i)
	return i
}

// ── config.json (auto update) ─────────────────────────────────────────────

const (
	AutoOn     = "on"     // daemon applies releases when idle (default)
	AutoOff    = "off"    // check and notify only
	AutoSource = "source" // source checkout: fast-forward pulls on a clean tree
	// AutoUpdateEnv overrides config.json for the process that reads it
	// (0/off/false → off, 1/on → on, source). The login service does not see
	// your shell env: use `pcbpilot update --auto off` for the daemon.
	AutoUpdateEnv = "PCBPILOT_AUTO_UPDATE"
)

type Config struct {
	AutoUpdate string `json:"autoUpdate,omitempty"`
}

func ConfigPath() string { return statePath("config.json") }

func ReadConfig() Config {
	var c Config
	_ = ReadJSON(ConfigPath(), &c)
	return c
}

// AutoMode resolves the effective auto-update mode and where it came from.
func AutoMode() (mode, source string) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(AutoUpdateEnv))) {
	case "0", "off", "false", "no":
		return AutoOff, "env " + AutoUpdateEnv
	case "1", "on", "true", "yes":
		return AutoOn, "env " + AutoUpdateEnv
	case "source":
		return AutoSource, "env " + AutoUpdateEnv
	}
	switch c := ReadConfig(); c.AutoUpdate {
	case AutoOff, AutoOn, AutoSource:
		return c.AutoUpdate, ConfigPath()
	}
	return AutoOn, "default"
}

func SetAutoMode(mode string) error {
	switch mode {
	case AutoOn, AutoOff, AutoSource:
	default:
		return fmt.Errorf("--auto wants on, off, source or status, got %q", mode)
	}
	c := ReadConfig()
	c.AutoUpdate = mode
	return WriteJSON(ConfigPath(), c)
}

// ── update-check.json ─────────────────────────────────────────────────────

// CheckCache is the last release check. Offline, Failures counts consecutive
// failed checks and NextRetryAt holds the exponential backoff (1m → 30m cap).
type CheckCache struct {
	CheckedAt   time.Time `json:"checkedAt"`             // last attempt
	SucceededAt time.Time `json:"succeededAt,omitempty"` // last successful answer
	Current     string    `json:"current,omitempty"`
	Latest      string    `json:"latest,omitempty"`
	ReleaseURL  string    `json:"releaseUrl,omitempty"`
	LastError   string    `json:"lastError,omitempty"`
	Failures    int       `json:"failures,omitempty"`
	NextRetryAt time.Time `json:"nextRetryAt,omitempty"`
}

func CheckCachePath() string { return statePath("update-check.json") }

func ReadCheckCache() CheckCache {
	var c CheckCache
	_ = ReadJSON(CheckCachePath(), &c)
	return c
}

// Backoff is the delay after the n-th consecutive failure: 1m, 2m, 4m … 30m.
func Backoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := time.Minute
	for i := 1; i < n && d < 30*time.Minute; i++ {
		d *= 2
	}
	if d > 30*time.Minute {
		d = 30 * time.Minute
	}
	return d
}

// RecordCheck folds one check result into the cache and returns it.
func RecordCheck(c CheckCache, now time.Time, current, latest string, err error) CheckCache {
	c.CheckedAt = now
	c.Current = current
	if err != nil {
		c.Failures++
		c.LastError = shortErr(err)
		c.NextRetryAt = now.Add(Backoff(c.Failures))
		return c
	}
	c.SucceededAt = now
	c.Latest = latest
	c.ReleaseURL = ReleasePageURL(latest)
	c.LastError, c.Failures, c.NextRetryAt = "", 0, time.Time{}
	return c
}

func shortErr(err error) string {
	s := strings.TrimSpace(err.Error())
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ── update-state.json ─────────────────────────────────────────────────────

const (
	PhaseIdle          = "idle"
	PhasePendingVerify = "applied-pending-restart" // new files in place, waiting for the restarted daemon to verify
	PhaseDone          = "updated"
	PhaseRolledBack    = "rolled-back"
	PhaseFailed        = "failed"
	PhaseDeferred      = "deferred"
)

// UpdateState records the last apply so the restarted daemon can verify it,
// and so health/CLI can relay "upgraded 0.6.0→0.6.1: cli, skill, mcp".
type UpdateState struct {
	Phase       string    `json:"phase"`
	From        string    `json:"from,omitempty"`
	To          string    `json:"to,omitempty"`
	Components  []string  `json:"components,omitempty"` // what changed
	Skipped     []string  `json:"skipped,omitempty"`    // "tools: needs sudo" etc
	At          time.Time `json:"at"`
	By          string    `json:"by,omitempty"` // daemon | cli
	Snapshot    string    `json:"snapshot,omitempty"`
	Connector   string    `json:"connector,omitempty"` // downloaded .eext path
	Error       string    `json:"error,omitempty"`
	SkipVersion string    `json:"skipVersion,omitempty"` // a release that failed verify: never auto-apply it again
	Summary     string    `json:"summary,omitempty"`
}

func UpdateStatePath() string { return statePath("update-state.json") }

func ReadUpdateState() UpdateState {
	var s UpdateState
	_ = ReadJSON(UpdateStatePath(), &s)
	return s
}

// ── session.json ──────────────────────────────────────────────────────────

type Session struct {
	LastCommandAt time.Time `json:"lastCommandAt"`
	StartedAt     time.Time `json:"startedAt"`
}

func SessionPath() string { return statePath("session.json") }

// SessionIdle is how long without a CLI command starts a new session.
const SessionIdle = 30 * time.Minute

// TouchSession records a command and reports whether it opened a new session.
func TouchSession(now time.Time) (bool, error) {
	var s Session
	_ = ReadJSON(SessionPath(), &s)
	fresh := s.LastCommandAt.IsZero() || now.Sub(s.LastCommandAt) > SessionIdle
	if fresh {
		s.StartedAt = now
	}
	s.LastCommandAt = now
	return fresh, WriteJSON(SessionPath(), s)
}

// ── update.log ────────────────────────────────────────────────────────────

func LogPath() string { return statePath("update.log") }

// AppendLog writes one timestamped line to ~/.pcbpilot/update.log (best effort).
func AppendLog(format string, a ...any) {
	p := LogPath()
	if p == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
}

// ── update.lock ───────────────────────────────────────────────────────────

// AcquireLock takes ~/.pcbpilot/update.lock (O_EXCL). A lock older than 30
// minutes is considered abandoned (a crashed updater) and taken over.
func AcquireLock(owner string) (release func(), err error) {
	p := statePath("update.lock")
	if p == "" {
		return nil, fmt.Errorf("no home dir")
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%s pid=%d %s\n", owner, os.Getpid(), time.Now().Format(time.RFC3339))
			f.Close()
			return func() { _ = os.Remove(p) }, nil
		}
		fi, serr := os.Stat(p)
		if serr == nil && time.Since(fi.ModTime()) > 30*time.Minute {
			_ = os.Remove(p)
			continue
		}
		raw, _ := os.ReadFile(p)
		return nil, fmt.Errorf("another update is running (%s)", strings.TrimSpace(string(raw)))
	}
	return nil, fmt.Errorf("could not take %s", p)
}

// Atoi is a tolerant strconv.Atoi (0 on error).
func Atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
