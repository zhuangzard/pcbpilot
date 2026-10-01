package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// officialCLI* implement the read-only `officialCli` block of `pcbpilot health`:
// is JLC's official EasyEDA Pro client CLI (`easyeda-pro` / `lceda-pro`, built
// into the desktop client V4.1.60+) present, which version, and — only where it
// is documented as a pure diagnostic — what `doctor` reports.
//
// Safety contract (the editor must never be launched by health):
//   - the client binary IS the Electron editor executable. Running it on a
//     client older than 4.1.60 (no CLI) or with an undocumented argument can
//     open the editor, so the version is read from install metadata
//     (resources/app/package.json, Info.plist) WITHOUT executing anything;
//   - `doctor` is the only command ever run. The official docs
//     (easyeda-client-cli 699e686, "Verifying the Runtime Environment") show it
//     probing the endpoint and answering connected:false when no editor runs,
//     i.e. it does not start one — but that is documented for the Windows
//     client only. On macOS/Linux, or when the version is unknown/older, health
//     reports "present (not probed)" with the reason instead of running it;
//   - PCBPILOT_OFFICIAL_CLI_PROBE=0 disables execution everywhere;
//   - nothing here can fail health.
//
// The official CLI's `invoke --code` runs arbitrary JS in the editor; it is not
// a pcbpilot write path. Typed actions remain the only write path.

const (
	officialCLIMinVersion   = "4.1.60"
	officialCLIProbeTimeout = 2 * time.Second
	officialCLIWritePolicy  = "not a pcbpilot write path: the official CLI's invoke --code runs arbitrary JS; pcbpilot writes only through typed actions"
)

type officialCLIReport struct {
	Status          string                 `json:"status"` // not-installed | present-unsupported | present-not-probed | probed | probe-failed
	Summary         string                 `json:"summary"`
	MinVersion      string                 `json:"minVersion"`
	Version         string                 `json:"version,omitempty"`
	Candidates      []officialCLICandidate `json:"candidates"`
	Doctor          *officialCLIDoctor     `json:"doctor,omitempty"`
	NotProbedReason string                 `json:"notProbedReason,omitempty"`
	WritePath       string                 `json:"writePath"`
}

type officialCLICandidate struct {
	Command       string `json:"command"`
	Path          string `json:"path"`
	Found         string `json:"found"`      // PATH | default-location
	Provenance    string `json:"provenance"` // documented | observed | inferred (for default locations)
	Version       string `json:"version,omitempty"`
	VersionSource string `json:"versionSource,omitempty"`
}

type officialCLIDoctor struct {
	Command       string          `json:"command"`
	OK            bool            `json:"ok"`
	Endpoint      string          `json:"endpoint,omitempty"`
	Connected     *bool           `json:"connected,omitempty"`
	BridgeVersion any             `json:"bridgeVersion,omitempty"`
	VersionMatch  any             `json:"versionMatch,omitempty"`
	DurationMs    int64           `json:"durationMs"`
	Error         string          `json:"error,omitempty"`
	Value         json.RawMessage `json:"value,omitempty"`
}

// officialCLIEnv is the injectable host view (tests replace every field).
type officialCLIEnv struct {
	goos      string
	lookPath  func(string) (string, error)
	exists    func(string) bool
	readFile  func(string) ([]byte, error)
	resolve   func(string) (string, error)
	run       func(ctx context.Context, path string, args ...string) ([]byte, error)
	getenv    func(string) string
	home      string
	timeout   time.Duration
	locations []officialCLILocation
}

type officialCLILocation struct {
	command    string
	path       string
	provenance string
}

func defaultOfficialCLIEnv() officialCLIEnv {
	home, _ := os.UserHomeDir()
	env := officialCLIEnv{
		goos:     runtime.GOOS,
		lookPath: exec.LookPath,
		exists: func(p string) bool {
			_, err := os.Stat(p)
			return err == nil
		},
		readFile: os.ReadFile,
		resolve:  filepath.EvalSymlinks,
		run: func(ctx context.Context, path string, args ...string) ([]byte, error) {
			cmd := exec.CommandContext(ctx, path, args...)
			cmd.WaitDelay = 500 * time.Millisecond
			return cmd.Output()
		},
		getenv:  os.Getenv,
		home:    home,
		timeout: officialCLIProbeTimeout,
	}
	env.locations = officialCLIDefaultLocations(env.goos, env.home, os.Getenv)
	return env
}

// officialCLIDefaultLocations lists install locations per OS. Provenance:
// documented = in the official CLI docs; observed = seen on a real install
// (2026-10-01, EasyEDA Pro 3.2.149 on macOS); inferred = naming pattern, not
// verified.
func officialCLIDefaultLocations(goos, home string, getenv func(string) string) []officialCLILocation {
	switch goos {
	case "darwin":
		locs := []officialCLILocation{
			{"easyeda-pro", "/Applications/EasyEDA-Pro.app", "observed"},
			{"lceda-pro", "/Applications/LCEDA-Pro.app", "inferred"},
		}
		if home != "" {
			locs = append(locs,
				officialCLILocation{"easyeda-pro", filepath.Join(home, "Applications", "EasyEDA-Pro.app"), "inferred"},
				officialCLILocation{"lceda-pro", filepath.Join(home, "Applications", "LCEDA-Pro.app"), "inferred"})
		}
		return locs
	case "windows":
		pf := getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return []officialCLILocation{
			{"easyeda-pro", filepath.Join(pf, "EasyEDA-Pro", "easyeda-pro.exe"), "documented"},
			{"lceda-pro", filepath.Join(pf, "LCEDA-Pro", "lceda-pro.exe"), "inferred"},
		}
	default:
		return []officialCLILocation{
			{"easyeda-pro", "/opt/easyeda-pro/easyeda-pro", "inferred"},
			{"lceda-pro", "/opt/lceda-pro/lceda-pro", "inferred"},
		}
	}
}

func detectOfficialCLI(ctx context.Context, env officialCLIEnv) (rep *officialCLIReport) {
	rep = &officialCLIReport{MinVersion: officialCLIMinVersion, WritePath: officialCLIWritePolicy, Candidates: []officialCLICandidate{}}
	defer func() {
		// Never fail health: a panic in detection degrades to a note.
		if r := recover(); r != nil {
			rep.Status = "present-not-probed"
			rep.Summary = fmt.Sprintf("detection error: %v", r)
		}
	}()
	seen := map[string]bool{}
	add := func(c officialCLICandidate) {
		key := c.Path
		if r, err := env.resolve(c.Path); err == nil {
			key = r
		}
		if seen[key] {
			return
		}
		seen[key] = true
		c.Version, c.VersionSource = officialCLIVersion(env, c.Path)
		rep.Candidates = append(rep.Candidates, c)
	}
	for _, name := range []string{"easyeda-pro", "lceda-pro"} {
		if p, err := env.lookPath(name); err == nil {
			add(officialCLICandidate{Command: name, Path: p, Found: "PATH"})
		}
	}
	for _, l := range env.locations {
		if env.exists(l.path) {
			add(officialCLICandidate{Command: l.command, Path: l.path, Found: "default-location", Provenance: l.provenance})
		}
	}
	if len(rep.Candidates) == 0 {
		rep.Status = "not-installed"
		rep.Summary = "not installed (requires EasyEDA Pro desktop V" + officialCLIMinVersion + "+)"
		return rep
	}
	// Prefer a supported candidate, then any with a known version.
	rank := func(c officialCLICandidate) int {
		switch {
		case versionAtLeast(c.Version, officialCLIMinVersion):
			return 2
		case c.Version != "":
			return 1
		}
		return 0
	}
	best := rep.Candidates[0]
	for _, c := range rep.Candidates[1:] {
		if rank(c) > rank(best) {
			best = c
		}
	}
	rep.Version = best.Version
	switch {
	case best.Version == "":
		rep.Status = "present-not-probed"
		rep.Summary = "present (not probed): " + best.Path
		rep.NotProbedReason = "client version unreadable from install metadata; executing an unknown client binary may open the editor"
		return rep
	case !versionAtLeast(best.Version, officialCLIMinVersion):
		rep.Status = "present-unsupported"
		rep.Summary = fmt.Sprintf("EasyEDA Pro desktop %s found at %s; the official CLI requires V%s+ (not executed)", best.Version, best.Path, officialCLIMinVersion)
		rep.NotProbedReason = "clients older than " + officialCLIMinVersion + " have no CLI: running the binary would open the editor"
		return rep
	}
	reason := ""
	switch {
	case env.getenv("PCBPILOT_OFFICIAL_CLI_PROBE") == "0":
		reason = "PCBPILOT_OFFICIAL_CLI_PROBE=0"
	case env.goos != "windows":
		reason = "the official docs document `doctor` as a non-launching endpoint probe for the Windows client only; on " + env.goos + " the client binary is the editor executable and its CLI behaviour is unverified, so health does not run it"
	case strings.HasSuffix(strings.ToLower(best.Path), ".app"):
		reason = "app bundle without a CLI executable on PATH"
	}
	if reason != "" {
		rep.Status = "present-not-probed"
		rep.Summary = fmt.Sprintf("present (not probed): EasyEDA Pro desktop %s at %s", best.Version, best.Path)
		rep.NotProbedReason = reason
		return rep
	}
	rep.Doctor = runOfficialDoctor(ctx, env, best.Path)
	if rep.Doctor.OK {
		rep.Status = "probed"
		conn := "unknown"
		if rep.Doctor.Connected != nil {
			conn = strconv.FormatBool(*rep.Doctor.Connected)
		}
		rep.Summary = fmt.Sprintf("EasyEDA Pro %s CLI present; doctor connected=%s", best.Version, conn)
	} else {
		rep.Status = "probe-failed"
		rep.Summary = fmt.Sprintf("EasyEDA Pro %s CLI present; doctor failed: %s", best.Version, rep.Doctor.Error)
	}
	return rep
}

func runOfficialDoctor(ctx context.Context, env officialCLIEnv, path string) *officialCLIDoctor {
	d := &officialCLIDoctor{Command: path + " doctor"}
	timeout := env.timeout
	if timeout <= 0 {
		timeout = officialCLIProbeTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	out, err := env.run(cctx, path, "doctor")
	d.DurationMs = time.Since(start).Milliseconds()
	if cctx.Err() != nil {
		d.Error = fmt.Sprintf("timed out after %s", timeout)
		return d
	}
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) || len(out) == 0 {
			d.Error = err.Error()
			return d
		}
	}
	var resp struct {
		OK    bool            `json:"ok"`
		Value json.RawMessage `json:"value"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal(out, &resp); jerr != nil {
		d.Error = "doctor output is not the documented JSON envelope"
		return d
	}
	if !resp.OK {
		d.Error = "doctor returned ok=false"
		if resp.Error != nil {
			d.Error += ": " + resp.Error.Code + " " + resp.Error.Message
		}
		return d
	}
	var v struct {
		Endpoint      string `json:"endpoint"`
		Connected     *bool  `json:"connected"`
		BridgeVersion any    `json:"bridgeVersion"`
		VersionMatch  any    `json:"versionMatch"`
	}
	_ = json.Unmarshal(resp.Value, &v)
	d.OK = true
	d.Endpoint, d.Connected, d.BridgeVersion, d.VersionMatch = v.Endpoint, v.Connected, v.BridgeVersion, v.VersionMatch
	d.Value = resp.Value
	return d
}

var plistVersionRE = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)

// officialCLIVersion reads the client version from install metadata without
// executing anything: <bundle>.app/Contents/Resources/app/package.json (then
// Info.plist) on macOS, <exe dir>/resources/app/package.json elsewhere (the
// Electron layout; observed on macOS, inferred for Windows/Linux).
func officialCLIVersion(env officialCLIEnv, p string) (string, string) {
	resolved := p
	if r, err := env.resolve(p); err == nil {
		resolved = r
	}
	var pkgs []string
	bundle := ""
	if i := strings.Index(resolved, ".app"); i >= 0 && (len(resolved) == i+4 || resolved[i+4] == '/') {
		bundle = resolved[:i+4]
		pkgs = append(pkgs, filepath.Join(bundle, "Contents", "Resources", "app", "package.json"))
	} else {
		// Join with the TARGET OS separator (env.goos), so the Windows layout
		// is computed correctly regardless of where the logic is exercised.
		sep := "/"
		if env.goos == "windows" {
			sep = `\`
		}
		dir := resolved
		if i := strings.LastIndexAny(resolved, `/\`); i >= 0 {
			dir = resolved[:i]
		}
		pkgs = append(pkgs, strings.Join([]string{dir, "resources", "app", "package.json"}, sep))
	}
	for _, pkg := range pkgs {
		if b, err := env.readFile(pkg); err == nil {
			var meta struct {
				Version string `json:"version"`
			}
			if json.Unmarshal(b, &meta) == nil && meta.Version != "" {
				return meta.Version, pkg
			}
		}
	}
	if bundle != "" {
		plist := filepath.Join(bundle, "Contents", "Info.plist")
		if b, err := env.readFile(plist); err == nil {
			if m := plistVersionRE.FindSubmatch(b); m != nil {
				return strings.TrimSpace(string(m[1])), plist
			}
		}
	}
	return "", ""
}

// versionAtLeast compares the first three numeric components ("4.1.60.abc"
// ≥ "4.1.60"). An unparsable or empty version is never at least anything.
func versionAtLeast(v, min string) bool {
	a, ok1 := versionTriple(v)
	b, ok2 := versionTriple(min)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

func versionTriple(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	if len(parts) < 3 {
		return out, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
