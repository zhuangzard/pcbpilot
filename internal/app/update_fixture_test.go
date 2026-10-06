package app

// Shared fixtures for the update/self-update tests: a temp HOME with an
// "older install", a fake release (local asset dir or HTTP server), a fake
// service manager, a fake daemon and a fake Node.js. Nothing here touches the
// real HOME, the real login service or the network.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/daemon"
	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
	"github.com/zhuangzard/pcbpilot/pkg/simtools"
)

func TestMain(m *testing.M) {
	// Safety net: no test may drive the real launchd/systemd/registry. Tests
	// that exercise service commands install their own recorder.
	daemonServiceRunner = func(name string, args ...string) (string, error) {
		return "", fmt.Errorf("test: refusing to run %s %v (install a fake daemonServiceRunner)", name, args)
	}
	os.Setenv("PCBPILOT_UPDATE_NOTICE", "0")
	// `pcb auto run --router auto` must not switch to an installed fastroute.
	os.Setenv("FASTROUTE_BIN", "")
	fastrouteLookPath = func(string) (string, error) { return "", fmt.Errorf("test: fastroute lookup disabled") }
	os.Setenv(selfupdate.GitHubProxyEnv, "off")
	// …and no test may write ~/.pcbpilot (update.log, state, snapshots) or a
	// client config of the real user: the whole package runs in a temp HOME.
	home, err := os.MkdirTemp("", "pcbpilot-app-test-home-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func skipOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixtures use POSIX shell scripts as fake binaries")
	}
}

// fakeBinary is a shell script that answers `--version` / `version`.
func fakeBinaryBytes(version string) []byte {
	return []byte("#!/bin/sh\necho \"pcbpilot v" + version + "\"\n")
}

func tarGzFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func skillDoc(version string) string {
	return "---\nname: pcbpilot\nmetadata:\n  version: \"" + version + "\"\n---\n# pcbpilot " + version + "\n"
}

// releaseAssets returns the asset files of a fake release.
func releaseAssets(t *testing.T, version string) map[string][]byte {
	t.Helper()
	asset, err := selfupdate.AssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Skip(err)
	}
	var eext bytes.Buffer
	zw := zip.NewWriter(&eext)
	w, _ := zw.Create("extension.json")
	fmt.Fprintf(w, `{"version":%q}`, version)
	w, _ = zw.Create("dist/index.js")
	w.Write([]byte("compiled"))
	zw.Close()
	return map[string][]byte{
		asset:           fakeBinaryBytes(version),
		"skills.tar.gz": tarGzFiles(t, map[string]string{"pcbpilot/SKILL.md": skillDoc(version), "pcbpilot/references/new.md": "new in " + version}),
		"mcp.tar.gz": tarGzFiles(t, map[string]string{
			"mcp/src/server.mjs": "// " + version, "mcp/package.json": "{}", "mcp/VERSION": version + "\n",
			"mcp/node_modules/@modelcontextprotocol/sdk/package.json": "{}",
		}),
		"pcbpilot-connector.eext": eext.Bytes(),
	}
}

func checksumsFor(assets map[string][]byte) []byte {
	var b strings.Builder
	for name, data := range assets {
		fmt.Fprintf(&b, "%x  %s\n", sha256.Sum256(data), name)
	}
	return []byte(b.String())
}

// writeLocalDist writes a local asset dir (update --local-dir).
func writeLocalDist(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	assets := releaseAssets(t, version)
	for name, data := range assets {
		os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
	os.WriteFile(filepath.Join(dir, "checksums.txt"), checksumsFor(assets), 0o644)
	return dir
}

// fakeRelease serves {base}/latest and {base}/download/vX/<asset>. The first
// failLatest /latest requests fail (offline simulation).
type fakeRelease struct {
	srv        *httptest.Server
	version    string
	failLatest atomic.Int32
	latestHits atomic.Int32
}

func serveFakeRelease(t *testing.T, version string) *fakeRelease {
	t.Helper()
	fr := &fakeRelease{version: version}
	assets := releaseAssets(t, version)
	sums := checksumsFor(assets)
	fr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest" {
			fr.latestHits.Add(1)
			if fr.failLatest.Load() > 0 {
				fr.failLatest.Add(-1)
				http.Error(w, "offline fixture", http.StatusServiceUnavailable)
				return
			}
			fmt.Fprintf(w, `{"tag_name":"v%s"}`, fr.version)
			return
		}
		prefix := "/download/v" + version + "/"
		name, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if name == "checksums.txt" {
			w.Write(sums)
			return
		}
		if data, ok := assets[name]; ok {
			w.Write(data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fr.srv.Close)
	t.Setenv(selfupdate.ReleaseBaseEnv, fr.srv.URL)
	return fr
}

// oldInstall seeds a temp HOME with a v<old> install: binary, skill dirs for
// claude, codex, zcode (real dirs) and agents (a symlink into a "checkout"),
// and client configs with upstream leftovers.
type oldInstall struct {
	home, bin, checkout string
}

func seedOldInstall(t *testing.T, old string) oldInstall {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv(selfupdate.AutoUpdateEnv, "")
	t.Setenv(daemon.AllowVersionSkewEnv, "")
	oi := oldInstall{home: home, bin: filepath.Join(home, "bin", "pcbpilot"), checkout: filepath.Join(home, "src", "pcbpilot", ".agents", "skills", "pcbpilot")}
	mustWrite(t, oi.bin, fakeBinaryBytes(old), 0o755)
	for _, root := range []string{".claude", ".codex", ".zcode"} {
		dir := filepath.Join(home, root, "skills", "pcbpilot")
		mustWrite(t, filepath.Join(dir, "SKILL.md"), []byte(skillDoc(old)), 0o644)
		mustWrite(t, filepath.Join(dir, "references", "retired.md"), []byte("gone in the new release"), 0o644)
		mustWrite(t, filepath.Join(dir, ".version"), []byte(old+"\n"), 0o644)
	}
	mustWrite(t, filepath.Join(oi.checkout, "SKILL.md"), []byte("SOURCE CHECKOUT"), 0o644)
	os.MkdirAll(filepath.Join(home, ".agents", "skills"), 0o755)
	if err := os.Symlink(oi.checkout, filepath.Join(home, ".agents", "skills", "pcbpilot")); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(home, ".claude.json"), []byte(`{"numStartups":3,"mcpServers":{"easyeda-agent":{"command":"node","args":["/old/server.mjs"]}}}`), 0o600)
	mustWrite(t, filepath.Join(home, ".codex", "config.toml"), []byte("model = \"o3\"\n\n[mcp_servers.easyeda]\ncommand = \"old\"\n"), 0o644)
	mustWrite(t, filepath.Join(home, ".zcode", "cli", "config.json"), []byte(`{"theme":"dark"}`), 0o644)
	mustWrite(t, filepath.Join(home, ".agents", "mcp.json"), []byte(`{"mcpServers":{}}`), 0o644)
	return oi
}

func mustWrite(t *testing.T, p string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, mode); err != nil {
		t.Fatal(err)
	}
}

// recorder is a fake service manager / command runner.
type recorder struct {
	mu     sync.Mutex
	calls  []string
	fail   map[string]error  // prefix → error
	onCall func(line string) // e.g. the fake daemon comes back on the new version
}

func (r *recorder) run(name string, args ...string) (string, error) {
	line := strings.TrimSpace(filepath.Base(name) + " " + strings.Join(args, " "))
	r.mu.Lock()
	r.calls = append(r.calls, line)
	hook := r.onCall
	r.mu.Unlock()
	if hook != nil {
		hook(line)
	}
	if strings.HasSuffix(name, "node") && len(args) == 1 && args[0] == "--version" {
		return "v22.11.0\n", nil
	}
	for prefix, err := range r.fail {
		if strings.HasPrefix(line, prefix) {
			return "", err
		}
	}
	if strings.HasPrefix(line, "launchctl print") || strings.HasPrefix(line, "reg query") {
		return "", errors.New("not loaded")
	}
	return "", nil
}

func (r *recorder) joined() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.calls, "\n")
}

func useRecorder(t *testing.T, r *recorder) {
	t.Helper()
	old := daemonServiceRunner
	daemonServiceRunner = r.run
	t.Cleanup(func() { daemonServiceRunner = old })
}

// fakeDaemonState is what the fake daemon probe returns (mutable).
type fakeDaemonState struct {
	mu sync.Mutex
	p  daemonProbe
}

func (f *fakeDaemonState) set(p daemonProbe) { f.mu.Lock(); f.p = p; f.mu.Unlock() }
func (f *fakeDaemonState) get(context.Context) daemonProbe {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.p
}

func testUpdateDeps(t *testing.T, oi oldInstall, version string, rec *recorder, dstate *fakeDaemonState) updateDeps {
	t.Helper()
	var ran [][]string
	return updateDeps{
		goos:    runtime.GOOS,
		home:    oi.home,
		binPath: oi.bin,
		version: "v" + version,
		runner:  rec.run,
		lookPath: func(n string) (string, error) {
			if n == "node" {
				return "/fake/bin/node", nil
			}
			return "", exec.ErrNotFound
		},
		simEnv: fakeSimEnv(map[string]string{"brew": "/b/brew", "ngspice": "/b/ngspice"}, &ran),
		probe:  dstate.get,
		handshake: func(_ context.Context, node, server, bin string) (selfupdate.MCPHandshakeResult, error) {
			v := strings.TrimSpace(readText(filepath.Join(filepath.Dir(filepath.Dir(server)), "VERSION")))
			return selfupdate.MCPHandshakeResult{ServerVersion: v, Tools: []string{"pcbpilot_health"}}, nil
		},
		stopDaemon: func() error { rec.run("stop-daemon"); return nil },
		sleep:      func(time.Duration) {},
		now:        time.Now,
		uid:        501,
		logf:       func(string, ...any) {},
	}
}

var _ = simtools.StatusOK
