package app

// CLI side of the update design: the CLI never checks or applies on its own.
// On the first command of a session (no CLI command for 30 min) it reads the
// daemon's /health "updates" block and relays it on stderr, once:
//
//	pcbpilot: pcbpilot upgraded 0.6.0 → 0.6.1: cli, skill, mcp — restart your AI client …
//	pcbpilot: connector ≠ pcbpilot v0.6.1: design actions are paused … (+ the 3 steps)
//
// Two repairs happen here because only the CLI can see them:
//   - daemon↔CLI skew: the login service's binary is this CLI but the running
//     daemon reports another release (binary replaced, daemon not restarted)
//     → restart it through the service;
//   - no daemon: a single cached release check (<1 h cache, 3 s timeout) and a
//     one-line hint to fix the service. Apply logic is never duplicated here.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
	"github.com/zhuangzard/pcbpilot/internal/version"
)

// sessionHookEnabled is set by Main (the real binary); tests calling Run
// directly never touch ~/.pcbpilot or the network.
var sessionHookEnabled bool

// sessionNoticePrinted: the hook already relayed the notices in this process,
// so `pcbpilot health` does not print them a second time.
var sessionNoticePrinted bool

// Main is the binary entry point: Run plus the session-start notice.
func Main(args []string, stdout, stderr io.Writer) int {
	sessionHookEnabled = true
	return Run(args, stdout, stderr)
}

// noticeExempt: commands that manage the installation themselves, run as the
// service, or are pure introspection.
func noticeExempt(args []string) bool {
	if len(args) == 0 {
		return true
	}
	for _, a := range args {
		switch a {
		case "-h", "--help", "-v", "--version":
			return true
		}
	}
	switch args[0] {
	case "help", "version", "completion", "update", "upgrade", "self-update", "daemon", "mcp", "skill", "actions":
		return true
	}
	return strings.HasPrefix(args[0], "__complete")
}

// noticeDeps are the hook's effects (swapped in tests).
type noticeDeps struct {
	now     func() time.Time
	probe   func(ctx context.Context) daemonProbe
	latest  func(ctx context.Context) (string, error)
	restart func() (string, error) // restart the daemon via the service
	// serviceRunsThis reports whether the login service runs this CLI binary.
	serviceRunsThis func() bool
	version         string
}

func realNoticeDeps(cfg *appConfig) noticeDeps {
	deps := realUpdateDeps(cfg, nil)
	return noticeDeps{
		now:    time.Now,
		probe:  func(ctx context.Context) daemonProbe { return probeDaemon(ctx, cfg) },
		latest: selfupdate.LatestReleaseVersion,
		restart: func() (string, error) {
			return restartDaemonService(deps)
		},
		serviceRunsThis: func() bool {
			st := readDaemonServiceStatus(deps.goos, deps.home)
			return st.Installed && st.Binary != "" && samePath(st.Binary, deps.binPath)
		},
		version: version.Version,
	}
}

// sessionStartNotice runs the hook for one CLI invocation.
func sessionStartNotice(cfg *appConfig, args []string, stderr io.Writer) {
	if !sessionHookEnabled || noticeExempt(args) || os.Getenv("PCBPILOT_UPDATE_NOTICE") == "0" {
		return
	}
	// Honour --host/--ports so a command aimed at another daemon (or at none,
	// e.g. release-smoke's --ports 1-1) never probes the default one.
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		if name != "--host" && name != "--ports" {
			continue
		}
		if !hasVal && i+1 < len(args) {
			val = args[i+1]
			i++
		}
		if name == "--host" {
			cfg.host = val
		} else {
			cfg.ports = val
		}
	}
	fresh, err := selfupdate.TouchSession(time.Now())
	if err != nil || !fresh {
		return
	}
	runSessionNotice(context.Background(), realNoticeDeps(cfg), stderr)
}

func runSessionNotice(ctx context.Context, d noticeDeps, stderr io.Writer) {
	p := d.probe(ctx)
	if !p.Running {
		fallbackNotice(ctx, d, stderr)
		return
	}
	// daemon↔CLI skew: both clean releases, different versions, and the
	// service runs this very binary → the daemon simply was not restarted.
	if selfupdate.IsCleanRelease(p.Version) && selfupdate.IsCleanRelease(d.version) &&
		normVersion(p.Version) != normVersion(d.version) && d.serviceRunsThis != nil && d.serviceRunsThis() {
		how, err := d.restart()
		if err != nil {
			fmt.Fprintf(stderr, "pcbpilot: daemon runs v%s but this CLI is v%s; restart failed (%v) — run `pcbpilot daemon service install`\n", normVersion(p.Version), normVersion(d.version), err)
		} else {
			fmt.Fprintf(stderr, "pcbpilot: daemon ran v%s, restarted on v%s (%s)\n", normVersion(p.Version), normVersion(d.version), how)
			selfupdate.AppendLog("cli: restarted daemon v%s → v%s (%s)", normVersion(p.Version), normVersion(d.version), how)
			for i := 0; i < 20; i++ {
				time.Sleep(250 * time.Millisecond)
				if p = d.probe(ctx); p.Running && normVersion(p.Version) == normVersion(d.version) {
					break
				}
			}
		}
	}
	for _, line := range healthNoticeLines(p.Raw, true) {
		fmt.Fprintln(stderr, line)
		sessionNoticePrinted = true
	}
}

// healthNoticeLines renders /health "updates" notices (plus the connector
// steps when misaligned) as "pcbpilot: …" lines.
func healthNoticeLines(raw json.RawMessage, withSteps bool) []string {
	u := parseHealthUpdates(raw)
	if u == nil {
		return nil
	}
	var out []string
	for _, n := range u.Notices {
		out = append(out, "pcbpilot: "+n)
	}
	if withSteps && u.Connector != nil && len(u.Connector.Misaligned) > 0 {
		for i, s := range u.Connector.Steps {
			out = append(out, fmt.Sprintf("  %d. %s", i+1, s))
		}
	}
	return out
}

func parseHealthUpdates(raw json.RawMessage) *healthUpdates {
	if len(raw) == 0 {
		return nil
	}
	var h struct {
		Updates *healthUpdates `json:"updates"`
	}
	if json.Unmarshal(raw, &h) != nil {
		return nil
	}
	return h.Updates
}

// fallbackNotice: the daemon is unreachable. One cached check and one line.
func fallbackNotice(ctx context.Context, d noticeDeps, stderr io.Writer) {
	c := selfupdate.ReadCheckCache()
	now := d.now()
	fresh := !c.CheckedAt.IsZero() && now.Sub(c.CheckedAt) < time.Hour
	if !fresh && d.latest != nil {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		latest, err := d.latest(cctx)
		cancel()
		c = selfupdate.RecordCheck(c, now, normVersion(d.version), latest, err)
		_ = selfupdate.WriteJSON(selfupdate.CheckCachePath(), c)
	}
	line := "pcbpilot: the daemon is not running, so updates and EasyEDA actions are unavailable — run `pcbpilot daemon service install`"
	if c.Latest != "" && selfupdate.SemverLess(selfupdate.SemverCore(d.version), c.Latest) {
		line += fmt.Sprintf(" (v%s is available; the daemon installs it once it runs)", c.Latest)
	}
	fmt.Fprintln(stderr, line)
}
