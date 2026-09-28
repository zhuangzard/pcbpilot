package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/daemon"
	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
)

// ── source installs ──────────────────────────────────────────────────────

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sourceRepos: origin (bare), work (the "installed" checkout), other (a
// second clone that pushes new commits).
func sourceRepos(t *testing.T) (work, other string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	git(t, root, "init", "-q", "--bare", origin)
	work, other = filepath.Join(root, "work"), filepath.Join(root, "other")
	git(t, root, "clone", "-q", origin, work)
	mustWrite(t, filepath.Join(work, "README"), []byte("v1"), 0o644)
	mustWrite(t, filepath.Join(work, "scripts", "setup-agent.sh"), []byte("#!/bin/sh\ntouch \"$(dirname \"$0\")/../SETUP-RAN\"\n"), 0o755)
	git(t, work, "add", ".")
	git(t, work, "commit", "-qm", "one")
	git(t, work, "push", "-q", "origin", "HEAD")
	git(t, root, "clone", "-q", origin, other)
	return work, other
}

func pushUpstream(t *testing.T, other, content string) {
	mustWrite(t, filepath.Join(other, "README"), []byte(content), 0o644)
	git(t, other, "commit", "-qam", content)
	git(t, other, "push", "-q", "origin", "HEAD")
}

func TestSourceUpdateRefusesDirtyAndDivergedPullsWhenClean(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work, other := sourceRepos(t)
	var setups atomic.Int32
	old := sourceSetupCommand
	sourceSetupCommand = func(_ context.Context, repo string, _ io.Writer, detached bool) error {
		setups.Add(1)
		return nil
	}
	t.Cleanup(func() { sourceSetupCommand = old })
	ctx := context.Background()

	// Up to date: nothing to do, setup not run.
	if rows, err := runSourceUpdate(ctx, work, false, false, io.Discard); err != nil || rows[0].Status != "current" || setups.Load() != 0 {
		t.Fatalf("current checkout: %+v %v", rows, err)
	}
	pushUpstream(t, other, "v2")

	// Dirty: refused, nothing pulled.
	mustWrite(t, filepath.Join(work, "README"), []byte("local edit"), 0o644)
	rows, err := runSourceUpdate(ctx, work, false, false, io.Discard)
	if !errors.Is(err, errSourceRefused) || !strings.Contains(rows[0].Detail, "uncommitted") || setups.Load() != 0 {
		t.Fatalf("dirty checkout must be refused: %+v %v", rows, err)
	}
	git(t, work, "checkout", "--", "README")

	// Diverged: refused.
	mustWrite(t, filepath.Join(work, "LOCAL"), []byte("x"), 0o644)
	git(t, work, "add", "LOCAL")
	git(t, work, "commit", "-qm", "local")
	rows, err = runSourceUpdate(ctx, work, false, false, io.Discard)
	if !errors.Is(err, errSourceRefused) || !strings.Contains(rows[0].Detail, "diverged") {
		t.Fatalf("diverged checkout must be refused: %+v %v", rows, err)
	}
	git(t, work, "reset", "-q", "--hard", "HEAD~1")

	// Clean and behind: ff pull, then setup-agent.sh.
	rows, err = runSourceUpdate(ctx, work, false, false, io.Discard)
	if err != nil || setups.Load() != 1 {
		t.Fatalf("clean pull: %+v %v", rows, err)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "README")); string(b) != "v2" {
		t.Fatalf("not fast-forwarded: %q", b)
	}
}

func TestSourceInstallDetectionFromInstallJSON(t *testing.T) {
	work, _ := sourceRepos(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	if _, ok := sourceInstall(); ok {
		t.Fatal("no install.json → not a source install")
	}
	// Pre-install.json machines: a skill dir linked into <checkout>/.agents/skills/pcbpilot.
	linked := filepath.Join(work, ".agents", "skills", "pcbpilot")
	mustWrite(t, filepath.Join(linked, "SKILL.md"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(home, ".claude", "skills"), 0o755)
	if err := os.Symlink(linked, filepath.Join(home, ".claude", "skills", "pcbpilot")); err != nil {
		t.Fatal(err)
	}
	if repo, ok := sourceInstall(); !ok || !samePath(repo, work) {
		t.Fatalf("linked skill must identify the checkout: %q %v", repo, ok)
	}
	selfupdate.WriteJSON(selfupdate.InstallInfoPath(), selfupdate.InstallInfo{Kind: "release"})
	if _, ok := sourceInstall(); ok {
		t.Fatal("install.json kind=release wins over a stray link")
	}
	selfupdate.WriteJSON(selfupdate.InstallInfoPath(), selfupdate.InstallInfo{Kind: "source", Repo: work})
	if repo, ok := sourceInstall(); !ok || repo != work {
		t.Fatalf("source install not detected: %q %v", repo, ok)
	}
}

// ── daemon self-updater ──────────────────────────────────────────────────

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestUpdater(t *testing.T, oi oldInstall, version string, rec *recorder, act *daemon.Activity) (*selfUpdater, *fakeClock, *atomic.Int32) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)}
	deps := testUpdateDeps(t, oi, version, rec, &fakeDaemonState{})
	deps.now = clock.now
	restarts := &atomic.Int32{}
	u := newSelfUpdater(deps, "v"+version, func() daemon.Activity { return *act }, func() { restarts.Add(1) })
	return u, clock, restarts
}

func TestSelfUpdaterOfflineBacksOffQuietlyThenFindsRelease(t *testing.T) {
	skipOnWindows(t)
	oi := seedOldInstall(t, "0.6.0")
	fr := serveFakeRelease(t, "0.6.1")
	fr.failLatest.Store(6)                  // three checks (API + web fallback each)
	t.Setenv(selfupdate.AutoUpdateEnv, "0") // notify only: this test is about checking
	act := &daemon.Activity{}
	u, clock, restarts := newTestUpdater(t, oi, "0.6.0", &recorder{}, act)
	ctx := context.Background()

	u.tick(ctx)
	h, avail := u.Health(nil)
	hu := h.(healthUpdates)
	if hu.State != "offline-retrying" || hu.LastError == "" || hu.NextRetryAt == nil || avail != nil {
		t.Fatalf("offline state: %+v", hu)
	}
	if got := hu.NextRetryAt.Sub(clock.now()); got != time.Minute {
		t.Fatalf("first retry after 1m, got %s", got)
	}
	if len(hu.Notices) != 1 || !strings.Contains(hu.Notices[0], "offline") {
		t.Fatalf("offline must be ONE short notice: %v", hu.Notices)
	}
	// Within the backoff window nothing is retried, even when woken.
	hits := fr.latestHits.Load() // API + web fallback both hit /latest
	u.OnAction(time.Hour)
	u.tick(ctx)
	if fr.latestHits.Load() != hits {
		t.Fatalf("retried inside the backoff window: %d → %d hits", hits, fr.latestHits.Load())
	}
	clock.advance(time.Minute)
	u.tick(ctx) // fail #2 → 2m
	if c := selfupdate.ReadCheckCache(); c.Failures != 2 || c.NextRetryAt.Sub(clock.now()) != 2*time.Minute {
		t.Fatalf("backoff not doubling: %+v", c)
	}
	clock.advance(2 * time.Minute)
	u.tick(ctx) // fail #3 → 4m
	clock.advance(4 * time.Minute)
	u.tick(ctx) // succeeds
	h, avail = u.Health(nil)
	hu = h.(healthUpdates)
	if hu.State != "available" || hu.Latest != "0.6.1" || avail == nil || hu.LastError != "" {
		t.Fatalf("back online: %+v", hu)
	}
	if restarts.Load() != 0 {
		t.Fatal("auto off must never apply")
	}
	if !strings.Contains(strings.Join(hu.Notices, "\n"), "auto-update off") {
		t.Fatalf("auto-off notice: %v", hu.Notices)
	}
	// Next periodic check only after 6h.
	if u.checkDue() {
		t.Fatal("a fresh successful check must not be repeated immediately")
	}
	clock.advance(6 * time.Hour)
	if !u.checkDue() {
		t.Fatal("6h later a check is due")
	}
}

func TestSelfUpdaterDefersWhileBusyAndNotifiesOnDevBuilds(t *testing.T) {
	skipOnWindows(t)
	oi := seedOldInstall(t, "0.6.0")
	serveFakeRelease(t, "0.6.1")
	act := &daemon.Activity{InFlight: 1}
	u, clock, restarts := newTestUpdater(t, oi, "0.6.0", &recorder{}, act)
	ctx := context.Background()
	u.tick(ctx)
	if hu, _ := u.Health(nil); hu.(healthUpdates).State != "deferred" || restarts.Load() != 0 {
		t.Fatalf("in-flight action must defer: %+v", hu)
	}
	*act = daemon.Activity{UnsavedWindows: []string{"w1"}}
	u.tick(ctx)
	if hu, _ := u.Health(nil); !strings.Contains(hu.(healthUpdates).Reason, "unsaved") || restarts.Load() != 0 {
		t.Fatalf("unsaved edits must defer: %+v", hu)
	}
	*act = daemon.Activity{LastActionAt: clock.now().Add(-30 * time.Second)}
	u.tick(ctx)
	if hu, _ := u.Health(nil); hu.(healthUpdates).State != "deferred" || restarts.Load() != 0 {
		t.Fatalf("recent activity must defer: %+v", hu)
	}
	if b, _ := os.ReadFile(oi.bin); string(b) != string(fakeBinaryBytes("0.6.0")) {
		t.Fatal("nothing may be touched while deferred")
	}

	// Dev build: notify only, never apply.
	dev, _, devRestarts := newTestUpdater(t, oi, "0.6.0-4-gabc123-dirty", &recorder{}, &daemon.Activity{})
	dev.version = "v0.6.0-4-gabc123-dirty"
	dev.tick(ctx)
	hu, _ := dev.Health(nil)
	if hu.(healthUpdates).State != "dev-notify" || devRestarts.Load() != 0 {
		t.Fatalf("dev build must only notify: %+v", hu)
	}
}

// The daemon self-update path end to end: fake release server → check →
// idle gate → apply (CLI, skills, MCP, tools, connector) → restart request →
// post-restart verify on the "new" daemon → health notice. Then the failure
// variant: verify fails → rollback, skipVersion, restart into the old binary.
func TestDaemonSelfUpdateAppliesVerifiesAndRollsBack(t *testing.T) {
	skipOnWindows(t)
	for _, failVerify := range []bool{false, true} {
		t.Run(fmt.Sprintf("failVerify=%v", failVerify), func(t *testing.T) {
			oi := seedOldInstall(t, "0.6.0")
			serveFakeRelease(t, "0.6.1")
			rec := &recorder{}
			useRecorder(t, rec)
			act := &daemon.Activity{}
			u, clock, restarts := newTestUpdater(t, oi, "0.6.0", rec, act)
			ctx := context.Background()
			u.tick(ctx)
			if restarts.Load() != 1 {
				hu, _ := u.Health(nil)
				t.Fatalf("idle daemon with a newer release must apply and restart: %+v", hu)
			}
			st := selfupdate.ReadUpdateState()
			if st.Phase != selfupdate.PhasePendingVerify || st.To != "0.6.1" || st.By != "daemon" {
				t.Fatalf("state after apply: %+v", st)
			}
			for _, c := range []string{"cli", "skill", "mcp"} {
				if !hasString(st.Components, c) {
					t.Fatalf("components %v missing %s", st.Components, c)
				}
			}
			if b, _ := os.ReadFile(oi.bin); string(b) != string(fakeBinaryBytes("0.6.1")) {
				t.Fatal("daemon did not replace its binary")
			}
			if _, err := os.Stat(selfupdate.ConnectorPath("0.6.1")); err != nil {
				t.Fatal("daemon did not download the connector")
			}
			if strings.Contains(rec.joined(), "launchctl") || strings.Contains(rec.joined(), "systemctl") {
				t.Fatalf("the daemon restarts by exiting, not through the service manager:\n%s", rec.joined())
			}

			// "Restart": a new updater running the new version verifies.
			nu, _, nrestarts := newTestUpdater(t, oi, "0.6.1", rec, act)
			nu.deps.now = clock.now
			if failVerify {
				nu.deps.handshake = func(context.Context, string, string, string) (selfupdate.MCPHandshakeResult, error) {
					return selfupdate.MCPHandshakeResult{}, errors.New("MCP server exits at startup")
				}
			}
			rolledBack := nu.startupVerify(ctx)
			st = selfupdate.ReadUpdateState()
			if !failVerify {
				if rolledBack || st.Phase != selfupdate.PhaseDone || nrestarts.Load() != 0 {
					t.Fatalf("verify ok: %+v", st)
				}
				hu, _ := nu.Health(nil)
				if !strings.Contains(strings.Join(hu.(healthUpdates).Notices, "\n"), "upgraded 0.6.0 → 0.6.1: cli, skill") {
					t.Fatalf("health must relay the upgrade: %v", hu.(healthUpdates).Notices)
				}
				return
			}
			if !rolledBack || st.Phase != selfupdate.PhaseRolledBack || st.SkipVersion != "0.6.1" || nrestarts.Load() != 1 {
				t.Fatalf("failed verify must roll back and restart: rolledBack=%v %+v restarts=%d", rolledBack, st, nrestarts.Load())
			}
			if b, _ := os.ReadFile(oi.bin); string(b) != string(fakeBinaryBytes("0.6.0")) {
				t.Fatal("binary not restored")
			}
			if v := strings.TrimSpace(readText(filepath.Join(oi.home, ".claude", "skills", "pcbpilot", ".version"))); v != "0.6.0" {
				t.Fatalf("skill not restored: %q", v)
			}
			// The restored 0.6.0 daemon never re-applies the bad release.
			again, _, againRestarts := newTestUpdater(t, oi, "0.6.0", rec, act)
			again.tick(ctx)
			if againRestarts.Load() != 0 {
				t.Fatal("a rolled-back release must not be applied again")
			}
			hu, _ := again.Health(nil)
			if !strings.Contains(strings.Join(hu.(healthUpdates).Notices, "\n"), "rolled back") {
				t.Fatalf("health must relay the rollback: %v", hu.(healthUpdates).Notices)
			}
		})
	}
}

func TestSelfUpdaterHealthConnectorAlignmentNotice(t *testing.T) {
	skipOnWindows(t)
	oi := seedOldInstall(t, "0.6.1")
	u, _, _ := newTestUpdater(t, oi, "0.6.1", &recorder{}, &daemon.Activity{})
	h, _ := u.Health([]daemon.Window{{WindowID: "w1", ConnectorVersion: "0.6.0"}, {WindowID: "w2", ConnectorVersion: "0.6.1"}})
	hu := h.(healthUpdates)
	if hu.Connector == nil || len(hu.Connector.Misaligned) != 1 || len(hu.Connector.Steps) != 3 {
		t.Fatalf("misaligned connector must carry the 3 steps: %+v", hu.Connector)
	}
	if !strings.Contains(strings.Join(hu.Notices, "\n"), "design actions are paused") {
		t.Fatalf("notice: %v", hu.Notices)
	}
}

func TestRestartAfterSelfUpdateSupervisedExitsOtherwiseSpawns(t *testing.T) {
	oldSup, oldSpawn := supervisedByService, spawnReplacementDaemon
	t.Cleanup(func() { supervisedByService, spawnReplacementDaemon = oldSup, oldSpawn })
	spawned := 0
	spawnReplacementDaemon = func() error { spawned++; return nil }
	supervisedByService = func() bool { return true }
	var ec exitCodeError
	if err := restartAfterSelfUpdate(io.Discard); !errors.As(err, &ec) || ec.code != exitForSupervisor || spawned != 0 {
		t.Fatalf("supervised daemon must exit %d: %v", exitForSupervisor, err)
	}
	supervisedByService = func() bool { return false }
	if err := restartAfterSelfUpdate(io.Discard); err != nil || spawned != 1 {
		t.Fatalf("unsupervised daemon must spawn its replacement: %v %d", err, spawned)
	}
}

// ── CLI session notice ───────────────────────────────────────────────────

func healthRaw(t *testing.T, version string, notices ...string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"version": version, "updates": healthUpdates{Current: version, Notices: notices,
		Connector: &connectorAlignment{Required: version, Misaligned: []string{"w1=0.6.0"}, Steps: []string{"s1", "s2", "s3"}}}})
	return raw
}

func TestSessionNoticeRelaysDaemonStateAndFixesSkew(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restarted := 0
	probes := 0
	d := noticeDeps{
		now: time.Now,
		probe: func(context.Context) daemonProbe {
			probes++
			v := "v0.6.0"
			if restarted > 0 {
				v = "v0.6.1"
			}
			return daemonProbe{Running: true, Version: v, Raw: healthRaw(t, v, "pcbpilot upgraded 0.6.0 → 0.6.1: cli, skill, mcp")}
		},
		latest: func(context.Context) (string, error) {
			t.Fatal("no network check while the daemon runs")
			return "", nil
		},
		restart:         func() (string, error) { restarted++; return "launchctl kickstart -k", nil },
		serviceRunsThis: func() bool { return true },
		version:         "v0.6.1",
	}
	var out bytes.Buffer
	runSessionNotice(context.Background(), d, &out)
	s := out.String()
	if restarted != 1 || !strings.Contains(s, "daemon ran v0.6.0, restarted on v0.6.1") {
		t.Fatalf("daemon↔CLI skew must restart via the service: %q", s)
	}
	if !strings.Contains(s, "pcbpilot: pcbpilot upgraded 0.6.0 → 0.6.1") || !strings.Contains(s, "  3. s3") {
		t.Fatalf("notices + connector steps must be relayed: %q", s)
	}
	// Service runs another binary → no restart.
	restarted = 0
	d.serviceRunsThis = func() bool { return false }
	out.Reset()
	runSessionNotice(context.Background(), d, &out)
	if restarted != 0 {
		t.Fatal("must not restart a service that runs another binary")
	}
}

func TestSessionNoticeFallbackCheckUsesOneHourCacheAndSurvivesOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	calls := 0
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)
	d := noticeDeps{
		now:     func() time.Time { return now },
		probe:   func(context.Context) daemonProbe { return daemonProbe{} },
		latest:  func(context.Context) (string, error) { calls++; return "", errors.New("dial tcp: i/o timeout") },
		version: "v0.6.0",
	}
	var out bytes.Buffer
	runSessionNotice(context.Background(), d, &out)
	if calls != 1 || strings.Count(out.String(), "\n") != 1 || !strings.Contains(out.String(), "daemon service install") {
		t.Fatalf("offline fallback: one check, one line: %d %q", calls, out.String())
	}
	now = now.Add(30 * time.Minute)
	out.Reset()
	runSessionNotice(context.Background(), d, &out)
	if calls != 1 {
		t.Fatal("a check < 1h old must be served from the cache")
	}
	now = now.Add(31 * time.Minute)
	d.latest = func(context.Context) (string, error) { calls++; return "0.6.1", nil }
	out.Reset()
	runSessionNotice(context.Background(), d, &out)
	if calls != 2 || !strings.Contains(out.String(), "v0.6.1 is available") {
		t.Fatalf("stale cache → one new check: %d %q", calls, out.String())
	}
}

func TestSessionHookFirstCommandOnlyAndExemptions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	fresh, _ := selfupdate.TouchSession(now)
	again, _ := selfupdate.TouchSession(now.Add(10 * time.Minute))
	later, _ := selfupdate.TouchSession(now.Add(50 * time.Minute))
	if !fresh || again || !later {
		t.Fatalf("session = first command after 30 min idle: %v %v %v", fresh, again, later)
	}
	for _, args := range [][]string{nil, {"update"}, {"daemon", "start"}, {"mcp", "install"}, {"actions"}, {"health"}, {"--version"}} {
		if !noticeExempt(args) {
			t.Fatalf("%v must be exempt", args)
		}
	}
	if noticeExempt([]string{"pcb", "check"}) || noticeExempt([]string{"sch", "layout-lint"}) {
		t.Fatal("ordinary commands must get the notice")
	}
}

func TestHealthCommandCarriesUpdateNotices(t *testing.T) {
	payload := `{"service":"pcbpilot","version":"v0.6.1","status":"ok","windows":[],
	  "updates":{"current":"0.6.1","state":"updated","notices":["pcbpilot upgraded 0.6.0 → 0.6.1: cli, skill"]}}`
	host, port := fakeDaemon(t, payload)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"health", "--host", host, "--ports", fmt.Sprintf("%d-%d", port, port)}, &stdout, &stderr); code != 0 {
		t.Fatalf("health exit %d: %s", code, stderr.String())
	}
	var res struct {
		Notices []string `json:"notices"`
	}
	json.Unmarshal(stdout.Bytes(), &res)
	if len(res.Notices) != 1 || !strings.Contains(stderr.String(), "pcbpilot: pcbpilot upgraded") {
		t.Fatalf("health must expose + print notices: %s / %s", stdout.String(), stderr.String())
	}
}

// Startup alignment: a release daemon brings a missing/old MCP and the
// connector file to its own version (Skill dirs: StartupSync); auto off
// leaves a never-installed MCP alone.
func TestSelfUpdaterAlignInstallsMCPAndConnectorForOwnVersion(t *testing.T) {
	skipOnWindows(t)
	oi := seedOldInstall(t, "0.6.1")
	serveFakeRelease(t, "0.6.1")
	t.Setenv(selfupdate.AutoUpdateEnv, "0")
	u, _, _ := newTestUpdater(t, oi, "0.6.1", &recorder{}, &daemon.Activity{})
	if !u.align(context.Background()) || selfupdate.MCPInstalledVersion() != "" {
		t.Fatalf("auto off must not install a never-installed MCP (installed=%q)", selfupdate.MCPInstalledVersion())
	}
	os.Remove(selfupdate.ConnectorPath("0.6.1"))
	t.Setenv(selfupdate.AutoUpdateEnv, "")
	if !u.align(context.Background()) {
		t.Fatal("alignment must succeed against the release")
	}
	if selfupdate.MCPInstalledVersion() != "0.6.1" {
		t.Fatalf("mcp = %q", selfupdate.MCPInstalledVersion())
	}
	if _, err := os.Stat(selfupdate.ConnectorPath("0.6.1")); err != nil {
		t.Fatal("connector for the daemon's own version not downloaded")
	}
	if !strings.Contains(readText(filepath.Join(oi.home, ".codex", "config.toml")), selfupdate.MCPServerPath()) {
		t.Fatal("aligned MCP not registered")
	}
}
