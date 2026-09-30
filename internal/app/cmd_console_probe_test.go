package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/console"
	"github.com/zhuangzard/pcbpilot/internal/daemon"
	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
)

// Live regression (v0.7): after setup-agent.sh --upgrade reloaded the launchd
// service, `launchctl print` blocked and /api/status stayed pending for
// minutes while /health, /api/projects and /api/events answered. End to end
// through a real daemon: a service probe that sleeps 30 s (ignoring its
// context, like a wedged child) must not delay /api/status past 1 s, and
// /health must stay fast while the self-updater is stuck in its startup work.
func TestDaemonConsoleStatusAnswersWhileServiceProbeAndSelfUpdaterHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launchd/systemd probe path")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PCBPILOT_HOME", "")
	t.Setenv(daemon.EnvAuditDir, filepath.Join(home, ".pcbpilot", "audit"))
	// A login-service file, so the probe reaches launchctl/systemctl.
	for _, goos := range []string{"darwin", "linux"} {
		p := daemonServicePath(goos, home)
		mustWrite(t, p, []byte("<array><string>/nonexistent/pcbpilot</string></array>\nExecStart=/nonexistent/pcbpilot daemon start\n"), 0o644)
	}
	release, returned := make(chan struct{}), make(chan struct{}, 8)
	hang := func() {
		select {
		case <-time.After(30 * time.Second):
		case <-release:
		}
	}
	oldRun := daemonServiceProbeRunner
	daemonServiceProbeRunner = func(ctx context.Context, name string, args ...string) (string, error) {
		defer func() { returned <- struct{}{} }()
		hang() // ignores ctx on purpose
		return "", nil
	}
	t.Cleanup(func() {
		close(release)
		<-returned // the single in-flight probe; then restoring the var is race-free
		daemonServiceProbeRunner = oldRun
	})

	// A self-updater wedged in its startup Skill sync and its release check.
	u := &selfUpdater{version: "v0.6.1-13-gabc", deps: updateDeps{now: time.Now}, wake: make(chan struct{}, 1),
		interval: 6 * time.Hour, poll: time.Minute, retryApply: time.Hour, quiet: time.Minute, idleRecheck: time.Hour}
	u.latest = func(ctx context.Context) (string, error) { hang(); return "", errors.New("offline") }
	u.startupSkills = func(ctx context.Context) { hang() }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go u.Run(ctx)

	port := freePort(t)
	srv := daemon.New(daemon.Options{Host: "127.0.0.1", PortStart: port, PortEnd: port, Version: "v0.6.1-13-gabc", Updates: u.Health})
	c := mountConsole(srv, "127.0.0.1", io.Discard)
	if c == nil {
		t.Fatal("console not mounted")
	}
	t.Cleanup(c.Stop)
	go func() { _ = srv.Run(ctx, io.Discard) }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitHTTP(t, base+"/health")

	get := func(path string) (map[string]any, time.Duration) {
		t.Helper()
		req, _ := http.NewRequest("GET", base+path, nil)
		req.Header.Set(console.TokenHeader, c.Token())
		start := time.Now()
		resp, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return m, time.Since(start)
	}
	h, d := get("/health")
	if d >= time.Second || h["updates"] == nil {
		t.Fatalf("/health took %s with a wedged self-updater (updates=%v)", d, h["updates"])
	}
	for i := 0; i < 2; i++ {
		st, d := get("/api/status")
		if d >= time.Second {
			t.Fatalf("/api/status #%d took %s with a hung service probe", i, d)
		}
		dm, _ := st["daemon"].(map[string]any)
		probe, _ := dm["serviceProbe"].(map[string]any)
		if probe["state"] != "checking" || st["health"] == nil {
			t.Fatalf("status #%d: serviceProbe=%v healthError=%v", i, probe, st["healthError"])
		}
	}
}

func TestReadDaemonServiceStatusCtxReportsDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("launchd/systemd probe path")
	}
	home := t.TempDir()
	mustWrite(t, daemonServicePath(runtime.GOOS, home), []byte("x"), 0o644)
	oldRun := daemonServiceProbeRunner
	daemonServiceProbeRunner = func(ctx context.Context, name string, args ...string) (string, error) {
		<-ctx.Done()
		return "", fmt.Errorf("%s: %w", name, ctx.Err())
	}
	t.Cleanup(func() { daemonServiceProbeRunner = oldRun })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	st, err := readDaemonServiceStatusCtx(ctx, runtime.GOOS, home)
	if !errors.Is(err, context.DeadlineExceeded) || !st.Installed || time.Since(start) > time.Second {
		t.Fatalf("deadline must surface as an error, not Loaded=false: %+v %v", st, err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(url); err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never answered", url)
}


// Health (the /health "updates" block) only copies state under u.mu; the
// release check, align, source git and apply never hold it while they work.
func TestSelfUpdaterHealthDoesNotWaitForAHungCheck(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(selfupdate.AutoUpdateEnv, "off")
	release, finished := make(chan struct{}), make(chan struct{})
	defer func() { close(release); <-finished }()
	entered := make(chan struct{})
	u := &selfUpdater{version: "v0.6.1", deps: updateDeps{now: time.Now}, wake: make(chan struct{}, 1), aligned: true,
		interval: 6 * time.Hour, poll: time.Minute, retryApply: time.Hour, quiet: time.Minute, idleRecheck: time.Hour}
	u.latest = func(ctx context.Context) (string, error) {
		close(entered)
		<-release
		return "", errors.New("offline")
	}
	go func() { defer close(finished); u.tick(context.Background()) }()
	<-entered
	start := time.Now()
	h, _ := u.Health(nil)
	if d := time.Since(start); d >= time.Second || h.(healthUpdates).Current != "0.6.1" {
		t.Fatalf("Health took %s while the release check hangs: %+v", d, h)
	}
}

// The real probe runner kills a hung tool at the deadline even when its
// children keep the output pipe open (a wrapper script around `sleep`).
func TestDaemonServiceProbeRunnerKillsHungTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := daemonServiceProbeRunner(ctx, "sh", "-c", "sleep 30; echo late")
	if d := time.Since(start); d > 3*time.Second || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe runner returned after %s: %v", d, err)
	}
}
