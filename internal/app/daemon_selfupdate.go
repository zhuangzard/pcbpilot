package app

// The daemon is the single place that checks for and applies updates.
//
//	check   on startup, every 6 h, and on the first /action after >30 min idle.
//	        Offline: retried quietly with exponential backoff (1m → 30m cap);
//	        health.updates says state "offline-retrying" with lastError/nextRetryAt.
//	apply   auto mode on (default) + clean release build + a newer release, when
//	        EasyEDA is idle: nothing in flight, no unsaved edits (autosave state),
//	        no action for 2 min. Snapshot → CLI → Skill → MCP → sim tools
//	        (ngspice only) → connector download → exit so launchd/systemd starts
//	        the new binary (Windows / no supervisor: spawn a replacement).
//	verify  the restarted daemon checks its own version, Skill markers and an
//	        MCP handshake; failure restores the snapshot, marks the release
//	        skipVersion (never auto-applied again) and restarts the old binary.
//	align   on startup: an installed MCP (and the downloaded connector) at a
//	        different version is re-synced to this daemon's version — skills are
//	        aligned by StartupSync.
//
// Dev builds and source checkouts only notify, unless `update --auto source`
// (fast-forward pull on a clean checkout + setup-agent.sh, run detached).
// State: ~/.pcbpilot/update-check.json, update-state.json, update.log.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/daemon"
	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
)

type selfUpdater struct {
	deps     updateDeps
	activity func() daemon.Activity
	version  string

	latest func(ctx context.Context) (string, error)
	source func(version string) selfupdate.AssetSource

	interval    time.Duration // periodic check
	quiet       time.Duration // no action for this long before applying
	idleRecheck time.Duration // /action after this much idle forces a check
	poll        time.Duration // re-evaluate the idle gate while an apply is pending
	retryApply  time.Duration // after a failed apply

	restart func() // graceful exit so the supervisor starts the new binary

	mu        sync.Mutex
	cache     selfupdate.CheckCache
	runState  string // current | available | deferred | applying | offline-retrying | dev-notify | ...
	deferWhy  string
	lastFail  time.Time
	wake      chan struct{}
	forceNext bool
	aligned   bool // MCP + connector file match this daemon's release

	// startupSkills runs the Skill StartupSync before the first check so it
	// can never race an apply (set from daemon start --auto-update-skill).
	startupSkills func(ctx context.Context)
}

func newSelfUpdater(deps updateDeps, ver string, activity func() daemon.Activity, restart func()) *selfUpdater {
	return &selfUpdater{
		deps:        deps,
		activity:    activity,
		version:     ver,
		latest:      selfupdate.LatestReleaseVersion,
		source:      selfupdate.ReleaseAssets,
		interval:    6 * time.Hour,
		quiet:       2 * time.Minute,
		idleRecheck: 30 * time.Minute,
		poll:        30 * time.Second,
		retryApply:  time.Hour,
		restart:     restart,
		cache:       selfupdate.ReadCheckCache(),
		wake:        make(chan struct{}, 1),
	}
}

func (u *selfUpdater) now() time.Time { return u.deps.now() }

func (u *selfUpdater) release() bool { return selfupdate.IsCleanRelease(u.version) }

func (u *selfUpdater) own() string { return normVersion(u.version) }

// OnAction is wired to daemon.Options.OnAction.
func (u *selfUpdater) OnAction(idleFor time.Duration) {
	if idleFor < u.idleRecheck && idleFor >= 0 {
		return
	}
	u.mu.Lock()
	u.forceNext = true
	u.mu.Unlock()
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Run blocks until ctx ends.
func (u *selfUpdater) Run(ctx context.Context) {
	if u.startupVerify(ctx) {
		return // restarting into the restored binary
	}
	if u.startupSkills != nil {
		u.startupSkills(ctx)
	}
	for {
		u.tick(ctx)
		wait := u.nextWake()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-u.wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// tick runs one check-and-maybe-apply cycle.
func (u *selfUpdater) tick(ctx context.Context) {
	if u.checkDue() {
		u.check(ctx)
		// Alignment needs the network too: retried on the check schedule
		// (backoff while offline) until it succeeds once.
		if !u.aligned {
			u.aligned = u.align(ctx)
		}
	}
	u.maybeApply(ctx)
}

func (u *selfUpdater) checkDue() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	if u.cache.Failures > 0 {
		u.forceNext = false // backoff also bounds forced checks
		return !now.Before(u.cache.NextRetryAt)
	}
	if u.forceNext {
		u.forceNext = false
		return true
	}
	return u.cache.SucceededAt.IsZero() || now.Sub(u.cache.SucceededAt) >= u.interval || u.cache.Current != u.own()
}

func (u *selfUpdater) nextWake() time.Duration {
	u.mu.Lock()
	defer u.mu.Unlock()
	now := u.now()
	next := u.cache.SucceededAt.Add(u.interval)
	if u.cache.Failures > 0 {
		next = u.cache.NextRetryAt
	}
	d := next.Sub(now)
	if u.pendingLocked() != "" && (d > u.poll || d <= 0) {
		d = u.poll
	}
	if d < time.Second {
		d = time.Second
	}
	return d
}

// check asks GitHub for the latest release (quietly: one log line per state
// change, never an error to a user).
func (u *selfUpdater) check(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	latest, err := u.latest(cctx)
	u.mu.Lock()
	wasFailing := u.cache.Failures > 0
	u.cache = selfupdate.RecordCheck(u.cache, u.now(), u.own(), latest, err)
	c := u.cache
	u.mu.Unlock()
	_ = selfupdate.WriteJSON(selfupdate.CheckCachePath(), c)
	switch {
	case err != nil && !wasFailing:
		selfupdate.AppendLog("update check failed (%s); retrying quietly with backoff, next at %s", c.LastError, c.NextRetryAt.Format(time.RFC3339))
	case err != nil && c.Failures%5 == 0:
		selfupdate.AppendLog("update check still offline after %d attempts (%s); next at %s", c.Failures, c.LastError, c.NextRetryAt.Format(time.RFC3339))
	case err == nil && wasFailing:
		selfupdate.AppendLog("update check back online: latest v%s", latest)
	case err == nil && selfupdate.SemverLess(u.own(), latest):
		selfupdate.AppendLog("update available: v%s → v%s", u.own(), latest)
	}
}

// pendingLocked is the version the daemon wants to apply ("" = none).
func (u *selfUpdater) pendingLocked() string {
	if !u.release() || u.cache.Latest == "" || !selfupdate.SemverLess(u.own(), u.cache.Latest) {
		return ""
	}
	if st := selfupdate.ReadUpdateState(); st.SkipVersion == u.cache.Latest {
		return ""
	}
	return u.cache.Latest
}

func (u *selfUpdater) setState(s, why string) {
	u.mu.Lock()
	u.runState, u.deferWhy = s, why
	u.mu.Unlock()
}

// maybeApply applies a pending release when auto mode allows and EasyEDA is idle.
func (u *selfUpdater) maybeApply(ctx context.Context) {
	mode, _ := selfupdate.AutoMode()
	if !u.release() {
		u.maybeSourceUpdate(ctx, mode)
		return
	}
	u.mu.Lock()
	target := u.pendingLocked()
	lastFail := u.lastFail
	u.mu.Unlock()
	if target == "" {
		u.setState("", "")
		return
	}
	if mode == selfupdate.AutoOff {
		u.setState("available", "auto-update off")
		return
	}
	if !lastFail.IsZero() && u.now().Sub(lastFail) < u.retryApply {
		u.setState("available", "last attempt failed; retrying after "+lastFail.Add(u.retryApply).Format("15:04"))
		return
	}
	if why := u.busy(); why != "" {
		u.mu.Lock()
		changed := u.deferWhy != why
		u.mu.Unlock()
		if changed {
			selfupdate.AppendLog("update v%s deferred: %s", target, why)
		}
		u.setState("deferred", why)
		return
	}
	release, err := selfupdate.AcquireLock("daemon")
	if err != nil {
		u.setState("deferred", err.Error())
		return
	}
	defer release()
	u.setState("applying", "")
	res := u.applyRelease(ctx, target)
	if res.Failed {
		u.mu.Lock()
		u.lastFail = u.now()
		u.mu.Unlock()
		st := selfupdate.UpdateState{Phase: selfupdate.PhaseFailed, From: u.own(), To: target, At: u.now().UTC(), By: "daemon",
			Snapshot: res.Snapshot, Error: firstFailure(res), Summary: res.Summary}
		_ = selfupdate.WriteJSON(selfupdate.UpdateStatePath(), st)
		u.setState("failed", st.Error)
		return
	}
	st := selfupdate.UpdateState{Phase: selfupdate.PhasePendingVerify, From: u.own(), To: target, Components: res.Changed,
		Skipped: res.Skipped, At: u.now().UTC(), By: "daemon", Snapshot: res.Snapshot, Summary: res.Summary}
	if res.Connector != nil {
		st.Connector = res.Connector.Path
	}
	_ = selfupdate.WriteJSON(selfupdate.UpdateStatePath(), st)
	selfupdate.AppendLog("applied v%s (%s); restarting the daemon to load it", target, strings.Join(res.Changed, ", "))
	if u.restart != nil {
		u.restart()
	}
}

func (u *selfUpdater) applyRelease(ctx context.Context, target string) applyResult {
	eng := &updateEngine{deps: u.deps}
	plan := updatePlan{
		src:        u.source(target),
		target:     target,
		components: map[string]bool{compCLI: true, compSkill: true, compMCP: true, compTools: true, compConnector: true},
		daemonMode: true,
	}
	actx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	return eng.apply(actx, plan)
}

func firstFailure(res applyResult) string {
	for _, s := range res.Steps {
		if s.Status == "failed" {
			return s.Component + ": " + s.Detail
		}
	}
	for _, v := range res.Verify {
		if !v.OK && v.Required {
			return "verify " + v.Check + ": " + v.Detail
		}
	}
	return res.Summary
}

// busy explains why now is not a quiet moment ("" = idle).
func (u *selfUpdater) busy() string {
	if u.activity == nil {
		return ""
	}
	a := u.activity()
	switch {
	case a.InFlight > 0:
		return fmt.Sprintf("%d EasyEDA action(s) in flight", a.InFlight)
	case len(a.UnsavedWindows) > 0:
		return "unsaved edits in " + strings.Join(a.UnsavedWindows, ", ")
	case !a.LastActionAt.IsZero() && u.now().Sub(a.LastActionAt) < u.quiet:
		return "EasyEDA used within the last " + u.quiet.String()
	}
	return ""
}

// maybeSourceUpdate: dev builds notify only; `--auto source` fast-forwards a
// clean checkout and runs setup-agent.sh detached.
func (u *selfUpdater) maybeSourceUpdate(ctx context.Context, mode string) {
	repo, ok := sourceInstall()
	u.mu.Lock()
	latest := u.cache.Latest
	u.mu.Unlock()
	if !ok || mode != selfupdate.AutoSource {
		if latest != "" && selfupdate.SemverLess(selfupdate.SemverCore(u.version), latest) {
			u.setState("dev-notify", "")
		} else {
			u.setState("", "")
		}
		return
	}
	if why := u.busy(); why != "" {
		u.setState("deferred", why)
		return
	}
	u.mu.Lock()
	recent := !u.lastFail.IsZero() && u.now().Sub(u.lastFail) < u.retryApply
	u.mu.Unlock()
	if recent {
		return
	}
	st := inspectSource(ctx, repo, true)
	if st.Dirty || st.Error != "" || st.Behind == 0 || st.Ahead > 0 {
		reason := "checkout current"
		switch {
		case st.Dirty:
			reason = "checkout has uncommitted changes — not pulling"
		case st.Error != "":
			reason = st.Error
		case st.Ahead > 0 && st.Behind > 0:
			reason = "checkout diverged — not pulling"
		}
		u.setState("dev-notify", reason)
		return
	}
	release, err := selfupdate.AcquireLock("daemon-source")
	if err != nil {
		return
	}
	defer release()
	logf, _ := os.OpenFile(selfupdate.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if logf != nil {
		defer logf.Close()
	}
	if _, err := runSourceUpdate(ctx, repo, false, true, logf); err != nil {
		u.mu.Lock()
		u.lastFail = u.now()
		u.mu.Unlock()
		u.setState("failed", err.Error())
	}
}

// ── startup: post-restart verify + alignment ─────────────────────────────

// startupVerify completes an apply after the restart. Returns true when it
// rolled back and asked for a restart into the restored binary.
func (u *selfUpdater) startupVerify(ctx context.Context) bool {
	st := selfupdate.ReadUpdateState()
	if st.Phase != selfupdate.PhasePendingVerify {
		return false
	}
	var problems []string
	if u.own() != st.To {
		problems = append(problems, fmt.Sprintf("daemon runs v%s, expected v%s", u.own(), st.To))
	}
	for _, t := range selfupdate.Targets(true) {
		if t.Linked == "" && t.Installed != st.To {
			problems = append(problems, fmt.Sprintf("skill %s is %q", t.Client, t.Installed))
		}
	}
	if hasString(st.Components, compMCP) {
		if v := selfupdate.MCPInstalledVersion(); v != st.To {
			problems = append(problems, "mcp is "+orDash(v))
		} else if node := selfupdate.FindNode(u.deps.lookPath, u.deps.runner, u.deps.goos); node.OK {
			if hs, err := u.deps.handshake(ctx, node.Path, selfupdate.MCPServerPath(), u.deps.binPath); err != nil {
				problems = append(problems, "mcp handshake: "+err.Error())
			} else if hs.ServerVersion != "" && hs.ServerVersion != st.To {
				problems = append(problems, "mcp server reports v"+hs.ServerVersion)
			}
		}
	}
	if len(problems) == 0 {
		st.Phase = selfupdate.PhaseDone
		st.At = u.now().UTC()
		st.Summary = fmt.Sprintf("pcbpilot upgraded %s → %s: %s", orDash(st.From), st.To, strings.Join(st.Components, ", "))
		_ = selfupdate.WriteJSON(selfupdate.UpdateStatePath(), st)
		selfupdate.AppendLog("post-restart verify ok: %s", st.Summary)
		return false
	}
	selfupdate.AppendLog("post-restart verify FAILED for v%s: %s — rolling back", st.To, strings.Join(problems, "; "))
	snap, err := selfupdate.LatestSnapshot(st.From)
	restored := []string{}
	if err == nil {
		restored, err = selfupdate.RestoreSnapshot(ctx, snap)
	}
	st.Phase = selfupdate.PhaseRolledBack
	st.SkipVersion = st.To
	st.Error = strings.Join(problems, "; ")
	if err != nil {
		st.Error += "; rollback: " + err.Error()
	}
	st.At = u.now().UTC()
	st.Summary = fmt.Sprintf("pcbpilot update to %s failed verification and was rolled back to %s (%s)", st.To, orDash(st.From), strings.Join(restored, ", "))
	_ = selfupdate.WriteJSON(selfupdate.UpdateStatePath(), st)
	selfupdate.AppendLog("%s", st.Summary)
	if u.own() != st.From && hasString(restored, compCLI) && u.restart != nil {
		u.restart()
		return true
	}
	return false
}

// align re-syncs an installed MCP and the downloaded connector to this
// daemon's own release (Skill dirs: StartupSync). Returns true when nothing
// is left to retry.
func (u *selfUpdater) align(ctx context.Context) bool {
	if !u.release() {
		return true
	}
	ok := true
	own := u.own()
	actx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	src := u.source(own)
	if v := selfupdate.MCPInstalledVersion(); v != "" && v != own {
		if node := selfupdate.FindNode(u.deps.lookPath, u.deps.runner, u.deps.goos); node.OK {
			out, err := selfupdate.InstallMCP(actx, src, false)
			selfupdate.AppendLog("align: mcp %s → %s (%s) %v", v, own, out.Status, errOrNil(err))
			ok = ok && err == nil
		}
	}
	if selfupdate.MCPInstalledVersion() != "" {
		if node := selfupdate.FindNode(u.deps.lookPath, u.deps.runner, u.deps.goos); node.OK {
			eng := &updateEngine{deps: u.deps}
			if regs, err := selfupdate.RegisterMCP(eng.clientEnv(), eng.mcpEntry(node.Path), false); err != nil {
				selfupdate.AppendLog("align: mcp registration: %v", err)
			} else {
				for _, r := range regs {
					if r.Status == "registered" || r.Status == "updated" {
						selfupdate.AppendLog("align: mcp registered for %s", r.Client)
					}
				}
			}
		}
	}
	if _, err := os.Stat(selfupdate.ConnectorPath(own)); err != nil {
		out, err := selfupdate.DownloadConnector(actx, src)
		selfupdate.AppendLog("align: connector v%s %s %v", own, out.Status, errOrNil(err))
		ok = ok && err == nil
	}
	return ok
}

func errOrNil(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func hasString(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// ── /health "updates" ─────────────────────────────────────────────────────

type healthUpdates struct {
	Current     string                  `json:"current"`
	Latest      string                  `json:"latest,omitempty"`
	ReleaseURL  string                  `json:"releaseUrl,omitempty"`
	Available   bool                    `json:"available"`
	Auto        string                  `json:"auto"`
	AutoSource  string                  `json:"autoSource,omitempty"`
	State       string                  `json:"state"`
	Reason      string                  `json:"reason,omitempty"`
	LastCheckAt *time.Time              `json:"lastCheckAt,omitempty"`
	LastError   string                  `json:"lastError,omitempty"`
	NextRetryAt *time.Time              `json:"nextRetryAt,omitempty"`
	LastUpdate  *selfupdate.UpdateState `json:"lastUpdate,omitempty"`
	Connector   *connectorAlignment     `json:"connector,omitempty"`
	Notices     []string                `json:"notices,omitempty"`
}

type connectorAlignment struct {
	Required   string   `json:"required"`
	Path       string   `json:"path,omitempty"`
	Misaligned []string `json:"misaligned,omitempty"` // "windowId=version"
	Steps      []string `json:"steps,omitempty"`
}

type updateAvailable struct {
	Current    string `json:"current"`
	Latest     string `json:"latest"`
	ReleaseURL string `json:"releaseUrl"`
}

// Health builds the /health "updates" block.
func (u *selfUpdater) Health(windows []daemon.Window) (any, any) {
	u.mu.Lock()
	c := u.cache
	state, why := u.runState, u.deferWhy
	u.mu.Unlock()
	mode, from := selfupdate.AutoMode()
	h := healthUpdates{Current: u.own(), Latest: c.Latest, ReleaseURL: c.ReleaseURL, Auto: mode, AutoSource: from, Reason: why}
	if !c.CheckedAt.IsZero() {
		t := c.CheckedAt
		h.LastCheckAt = &t
	}
	h.Available = c.Latest != "" && selfupdate.SemverLess(selfupdate.SemverCore(u.version), c.Latest)
	switch {
	case c.Failures > 0:
		state = "offline-retrying"
		h.LastError = c.LastError
		t := c.NextRetryAt
		h.NextRetryAt = &t
	case state == "" && h.Available:
		state = "available"
	case state == "":
		state = "current"
	}
	h.State = state
	var notices []string
	if st := selfupdate.ReadUpdateState(); st.Phase != "" {
		stc := st
		h.LastUpdate = &stc
		recent := u.now().Sub(st.At) < 24*time.Hour
		switch {
		case st.Phase == selfupdate.PhaseDone && recent:
			notices = append(notices, st.Summary+" — restart your AI client to load the new Skill/MCP")
		case st.Phase == selfupdate.PhaseRolledBack && recent:
			notices = append(notices, st.Summary+" — details: ~/.pcbpilot/update.log")
		case st.Phase == selfupdate.PhaseFailed && recent:
			notices = append(notices, "pcbpilot update to "+st.To+" failed ("+st.Error+"); will retry — details: ~/.pcbpilot/update.log")
		}
	}
	switch state {
	case "offline-retrying":
		if c.Latest != "" && h.Available {
			notices = append(notices, fmt.Sprintf("pcbpilot %s available; update check offline, retrying quietly", c.Latest))
		} else {
			notices = append(notices, "pcbpilot update check offline — retrying quietly in the background")
		}
	case "available":
		if mode == selfupdate.AutoOff {
			notices = append(notices, fmt.Sprintf("pcbpilot %s available (auto-update off) — run `pcbpilot update`", c.Latest))
		} else {
			notices = append(notices, fmt.Sprintf("pcbpilot %s available — the daemon applies it when EasyEDA is idle", c.Latest))
		}
	case "deferred":
		notices = append(notices, fmt.Sprintf("pcbpilot %s will be applied when EasyEDA is idle (%s)", c.Latest, why))
	case "dev-notify":
		notices = append(notices, fmt.Sprintf("pcbpilot release %s is newer than this source/dev build %s — `pcbpilot update` pulls (ff-only) and re-runs setup", c.Latest, u.version))
	}
	// Connector alignment (the daemon refuses design actions while it differs).
	if u.release() {
		path := selfupdate.ConnectorPath(u.own())
		if _, err := os.Stat(path); err != nil {
			path = ""
		}
		ca := &connectorAlignment{Required: u.own(), Path: path}
		for _, w := range windows {
			if daemon.ConnectorMisaligned(w.ConnectorVersion, u.version) {
				ca.Misaligned = append(ca.Misaligned, w.WindowID+"="+w.ConnectorVersion)
			}
		}
		if len(ca.Misaligned) > 0 {
			ca.Steps = selfupdate.ConnectorImportSteps(path)
			where := path
			if where == "" {
				where = selfupdate.ReleaseAssetURL(u.own(), selfupdate.ConnectorAsset)
			}
			notices = append(notices, fmt.Sprintf("connector ≠ pcbpilot v%s: design actions are paused until the new connector is imported — %s (steps: updates.connector.steps)", u.own(), where))
		}
		h.Connector = ca
	}
	h.Notices = notices
	var avail any
	if h.Available {
		avail = updateAvailable{Current: u.own(), Latest: c.Latest, ReleaseURL: c.ReleaseURL}
	}
	return h, avail
}

// exitForSupervisor is the exit status that makes launchd (KeepAlive) and
// systemd (Restart=on-failure) start the daemon again — on the new binary.
const exitForSupervisor = 75

// supervisedByService reports whether this daemon runs under the login
// service's supervisor (launchd sets XPC_SERVICE_NAME to the job label,
// systemd sets INVOCATION_ID).
var supervisedByService = func() bool {
	return os.Getenv("XPC_SERVICE_NAME") == daemonServiceLabel || os.Getenv("INVOCATION_ID") != ""
}

// spawnReplacementDaemon starts `<bin> daemon start` detached (Windows / a
// daemon started by hand). Swapped in tests.
var spawnReplacementDaemon = func() error {
	bin, err := selfupdate.CurrentBinaryPath()
	if err != nil {
		return err
	}
	// Same arguments as this daemon (e.g. --ports): a replacement started with
	// defaults could take over a different daemon's port.
	args := os.Args[1:]
	if len(args) < 2 || args[0] != "daemon" {
		args = []string{"daemon", "start"}
	}
	cmd := exec.Command(bin, args...)
	detachProcess(cmd)
	if logf, err := os.OpenFile(filepath.Join(selfupdate.Home(), "daemon.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	return cmd.Start()
}

func restartAfterSelfUpdate(stdout io.Writer) error {
	if supervisedByService() {
		fmt.Fprintf(stdout, "%s daemon: update applied — exiting %d so the login service starts the new binary\n", daemon.Service, exitForSupervisor)
		return exitCodeError{code: exitForSupervisor}
	}
	if err := spawnReplacementDaemon(); err != nil {
		selfupdate.AppendLog("update applied but starting the new daemon failed: %v — run `pcbpilot daemon service install`", err)
		return fmt.Errorf("start updated daemon: %w", err)
	}
	fmt.Fprintf(stdout, "%s daemon: update applied — started the new daemon\n", daemon.Service)
	return nil
}
