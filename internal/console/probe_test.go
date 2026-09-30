package console

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Regression (v0.7 console, found live): right after setup-agent.sh --upgrade
// reloaded the launchd service, `launchctl print` blocked and /api/status —
// which ran the service probe synchronously under a mutex — stayed pending,
// so the landing page sat on "连接 daemon…". A probe that sleeps 30 s must not
// delay /api/status or the SSE hello past 1 s.
func TestStatusAnswersWhileServiceProbeHangs(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	slow := func(ctx context.Context) (any, error) {
		calls.Add(1)
		select { // ignores ctx on purpose: a hung child process does too
		case <-time.After(30 * time.Second):
		case <-release:
		}
		return map[string]any{"installed": true}, nil
	}
	c, srv := newProbeConsole(t, slow, nil, Options{ServiceTimeout: 700 * time.Millisecond})

	for i := 0; i < 3; i++ {
		start := time.Now()
		var st struct {
			Daemon     DaemonInfo  `json:"daemon"`
			Components []Component `json:"components"`
		}
		decode(t, req(t, srv, "GET", "/api/status", c.Token(), nil, nil), &st)
		if d := time.Since(start); d >= time.Second {
			t.Fatalf("/api/status #%d took %s with a hung service probe", i, d)
		}
		if st.Daemon.PID != 42 || len(st.Components) == 0 {
			t.Fatalf("status must still carry the daemon and components: %+v", st)
		}
		want := "checking"
		if i == 2 {
			time.Sleep(400 * time.Millisecond) // past ServiceTimeout
			want = "timeout"
			decode(t, req(t, srv, "GET", "/api/status", c.Token(), nil, nil), &st)
		}
		if st.Daemon.ServiceProbe == nil || st.Daemon.ServiceProbe.State != want || st.Daemon.Service != nil {
			t.Fatalf("service probe #%d: %+v (service %v), want %s", i, st.Daemon.ServiceProbe, st.Daemon.Service, want)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("single flight: %d concurrent probe runs, want 1", n)
	}

	// The SSE hello carries the same daemon info and must not wait either.
	start := time.Now()
	r, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	r.Header.Set(TokenHeader, c.Token())
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(line) == "event: hello" {
			break
		}
	}
	if d := time.Since(start); d >= time.Second {
		t.Fatalf("SSE hello took %s with a hung service probe", d)
	}
}

// A slow /health (the daemon's own endpoint) is bounded too, and once a
// value exists a slow refresh serves it marked stale instead of blocking.
func TestStatusServesStaleHealthAndServiceWhileRefreshing(t *testing.T) {
	var slowHealth, slowSvc atomic.Bool
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	block := func() {
		select {
		case <-time.After(30 * time.Second):
		case <-release:
		}
	}
	svc := func(ctx context.Context) (any, error) {
		if slowSvc.Load() {
			block()
		}
		return map[string]any{"installed": true, "loaded": true, "platform": "darwin"}, nil
	}
	health := func(ctx context.Context) (json.RawMessage, error) {
		if slowHealth.Load() {
			block()
		}
		return json.RawMessage(`{"version":"0.7.0-test","windows":[{"windowId":"w-1","connectorVersion":"0.7.0"}]}`), nil
	}
	c, srv := newProbeConsole(t, svc, health, Options{ServiceTTL: time.Nanosecond, HealthWait: 200 * time.Millisecond})

	type status struct {
		Daemon      DaemonInfo       `json:"daemon"`
		Windows     []map[string]any `json:"windows"`
		HealthError string           `json:"healthError"`
		HealthProbe ProbeState       `json:"healthProbe"`
	}
	var st status
	decode(t, req(t, srv, "GET", "/api/status", c.Token(), nil, nil), &st)
	if st.HealthProbe.State != "ok" || len(st.Windows) != 1 || st.Daemon.ServiceProbe == nil || st.Daemon.ServiceProbe.State != "ok" {
		t.Fatalf("fast probes: %+v", st)
	}

	slowHealth.Store(true)
	slowSvc.Store(true)
	start := time.Now()
	st = status{}
	decode(t, req(t, srv, "GET", "/api/status", c.Token(), nil, nil), &st)
	if d := time.Since(start); d >= time.Second {
		t.Fatalf("/api/status took %s while both probes hang", d)
	}
	if st.HealthError != "" || len(st.Windows) != 1 || st.HealthProbe.State != "stale" {
		t.Fatalf("slow health must serve the last good copy marked stale: %+v", st)
	}
	if st.Daemon.ServiceProbe.State != "stale" || st.Daemon.Service == nil {
		t.Fatalf("slow service refresh must serve the cached value marked stale: %+v", st.Daemon)
	}
}

func TestProbeRecordsErrorsAndRecovers(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	p := newProbe(func(ctx context.Context) (any, error) {
		if fail.Load() {
			return nil, errors.New("launchctl: context deadline exceeded")
		}
		return "ok", nil
	}, time.Hour, time.Second, nil)
	if v, st := p.get(time.Second); v != nil || st.State != "error" || !strings.Contains(st.Error, "deadline") {
		t.Fatalf("error: %v %+v", v, st)
	}
	fail.Store(false)
	if v, st := p.get(time.Second); v != nil || st.State != "error" {
		t.Fatalf("a failure is not retried within ttl: %v %+v", v, st)
	}
	p.ttl = time.Nanosecond
	if v, st := p.get(time.Second); v != "ok" || st.State != "ok" {
		t.Fatalf("recovery: %v %+v", v, st)
	}
	fail.Store(true)
	p.ttl = time.Hour
	if v, st := p.get(0); v != "ok" || st.State != "ok" {
		t.Fatalf("fresh value is served from cache: %v %+v", v, st)
	}
}

func newProbeConsole(t *testing.T, svc func(context.Context) (any, error), health func(context.Context) (json.RawMessage, error), o Options) (*Console, *httptest.Server) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PCBPILOT_HOME", home)
	t.Setenv("HOME", t.TempDir())
	if health == nil {
		health = func(ctx context.Context) (json.RawMessage, error) {
			return json.RawMessage(`{"version":"0.7.0-test","windows":[]}`), nil
		}
	}
	o.Home, o.UserHome, o.Version, o.Host, o.AuditDir = home, os.Getenv("HOME"), "0.7.0-test", "127.0.0.1", filepath.Join(home, "audit")
	o.Health, o.Service = health, svc
	o.Daemon = func() DaemonInfo { return DaemonInfo{PID: 42, Port: 61832, StartedAt: time.Now()} }
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	c.heartbeat = 50 * time.Millisecond
	srv := httptest.NewServer(c.Handler())
	t.Cleanup(func() { c.Stop(); srv.Close() })
	return c, srv
}
