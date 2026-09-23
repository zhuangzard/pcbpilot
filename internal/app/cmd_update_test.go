package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
	"github.com/zhuangzard/pcbpilot/internal/version"
)

func TestCheckCLIVerdicts(t *testing.T) {
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })

	cases := []struct {
		name, cur, target, want string
		force                   bool
	}{
		{name: "behind", cur: "v0.25.1", target: "0.26.0", want: "behind"},
		{name: "current", cur: "v0.26.0", target: "0.26.0", want: "up-to-date"},
		{name: "ahead of a pinned older release", cur: "v0.27.0", target: "0.26.0", want: "ahead"},
		{name: "dev build", cur: "v0.25.1-19-gabc-dirty", target: "0.26.0", want: "skipped"},
		{name: "dev build with --force", cur: "dev", target: "0.26.0", want: "behind", force: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			version.Version = c.cur
			got := checkCLI(c.target, c.force)
			if got.Status != c.want {
				t.Errorf("checkCLI(%q→%q, force=%v)=%q want %q", c.cur, c.target, c.force, got.Status, c.want)
			}
		})
	}
}

func TestCheckSkillsReadsVersionMarkers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	claude := filepath.Join(home, ".claude", "skills", "pcbpilot")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, ".version"), []byte("0.25.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// codex dir deliberately absent.

	rows := checkSkills("0.26.0", nil)
	byClient := map[string]updateSkillRow{}
	for _, r := range rows {
		byClient[r.Client] = r
	}
	if got := byClient["claude"].Status; got != "behind" {
		t.Errorf("claude status=%q want behind", got)
	}
	if got := byClient["codex"].Status; got != "not-installed" {
		t.Errorf("codex status=%q want not-installed", got)
	}
	// A client filter must narrow the report.
	if rows := checkSkills("0.26.0", []string{"claude"}); len(rows) != 1 || rows[0].Client != "claude" {
		t.Errorf("--client claude should report exactly the claude dir, got %+v", rows)
	}
}

func TestCheckSkillsRequiresExactVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	codex := filepath.Join(home, ".codex", "skills", "pcbpilot")
	if err := os.MkdirAll(codex, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codex, ".version"), []byte("0.27.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := checkSkills("0.26.0", []string{"codex"})
	if len(rows) != 1 || rows[0].Status != "ahead" {
		t.Fatalf("a non-target Skill must not be reported current: %+v", rows)
	}
}

func TestCountBehindCountsIncompatibleConnectorButNotDevSkip(t *testing.T) {
	rep := updateReport{
		CLI:       &selfupdate.CLIOutcome{Status: "skipped"},
		Skills:    []updateSkillRow{{Status: "behind"}, {Status: "current"}, {Status: "not-installed"}},
		Connector: &connectorReport{Status: "behind"},
	}
	// dev-build skip is intentional (not "behind"); a stale connector counts
	// because the user still has to act on it.
	if got := countBehind(rep); got != 2 {
		t.Errorf("countBehind=%d want 2 (skill + connector)", got)
	}
}

func TestUpdateNotesSurfaceConnectorAndDaemonRestart(t *testing.T) {
	rep := updateReport{
		Target:    "0.26.0",
		CLI:       &selfupdate.CLIOutcome{Status: "updated"},
		Connector: &connectorReport{DaemonRunning: true, DaemonStatus: "mismatch", Status: "behind", Versions: []string{"0.25.1"}},
		Skills:    []updateSkillRow{{Client: "codex", Status: "updated"}},
	}
	notes := strings.Join(updateNotes(rep), "\n")
	if !strings.Contains(notes, "restart") {
		t.Errorf("a replaced binary with a live daemon must tell the user to restart it: %q", notes)
	}
	if !strings.Contains(notes, ".eext") {
		t.Errorf("a stale connector note must point at the .eext re-import: %q", notes)
	}
	if strings.Contains(notes, "new session") || strings.Contains(notes, "stop this task") {
		t.Errorf("an update must not act as a session authorization gate: %q", notes)
	}
}

// fakeDaemon serves a /health payload on a real port so probeConnector exercises
// the same scan path the CLI uses.
func fakeDaemon(t *testing.T, payload string) (host string, port int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, payload)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return u.Hostname(), p
}

func TestProbeConnectorFlagsStaleConnector(t *testing.T) {
	host, port := fakeDaemon(t, `{"service":"pcbpilot","version":"v0.26.0","status":"ok",
	  "windows":[{"windowId":"w1","connectorVersion":"0.25.1"},{"windowId":"w2","connectorVersion":"0.26.0"}]}`)
	cfg := &appConfig{host: host, ports: fmt.Sprintf("%d-%d", port, port)}

	rep := probeConnector(cfg, "0.26.0")
	if !rep.DaemonRunning {
		t.Fatal("daemon should be detected")
	}
	if rep.Status != "behind" {
		t.Errorf("status=%q want behind (one window runs 0.25.1)", rep.Status)
	}
	if rep.DaemonVersion != "v0.26.0" || rep.Windows != 2 {
		t.Errorf("unexpected daemon report: %+v", rep)
	}
	if rep.DaemonStatus != "current" {
		t.Errorf("daemon status=%q want current", rep.DaemonStatus)
	}
}

func TestProbeConnectorAcceptsPatchDriftWithinCompatibilityLine(t *testing.T) {
	host, port := fakeDaemon(t, `{"service":"pcbpilot","version":"v1.4.8","status":"ok",
	  "windows":[{"windowId":"w1","connectorVersion":"1.4.6"},{"windowId":"w2","connectorVersion":"1.4.8"}]}`)
	cfg := &appConfig{host: host, ports: fmt.Sprintf("%d-%d", port, port)}

	rep := probeConnector(cfg, "1.4.8")
	if rep.Status != "compatible" {
		t.Fatalf("status=%q want compatible for same major.minor patch drift: %+v", rep.Status, rep)
	}
	gate := updateReport{
		Target: "1.4.8", CLI: &selfupdate.CLIOutcome{Status: "up-to-date"},
		Skills: []updateSkillRow{{Present: true, Installed: "1.4.8", Status: "current"}}, Connector: rep,
	}
	gate.Behind = countBehind(gate)
	gate.Mismatched, gate.Unverified = countVersionGateProblems(gate)
	gate.Ready = gate.Behind == 0 && gate.Mismatched == 0 && gate.Unverified == 0
	if !gate.Ready {
		t.Fatalf("same-major.minor connector must pass latest gate: %+v", gate)
	}
	if notes := strings.Join(updateNotes(gate), "\n"); strings.Contains(notes, ".eext") {
		t.Fatalf("patch drift must not request connector upgrade: %q", notes)
	}
}

func TestVersionGateBlocksMismatchedDaemonAndAheadConnector(t *testing.T) {
	host, port := fakeDaemon(t, `{"service":"pcbpilot","version":"v0.25.0","status":"ok",
	  "windows":[{"windowId":"w1","connectorVersion":"0.27.0"}]}`)
	cfg := &appConfig{host: host, ports: fmt.Sprintf("%d-%d", port, port)}
	rep := updateReport{
		Target:    "0.26.0",
		CLI:       &selfupdate.CLIOutcome{Status: "up-to-date"},
		Skills:    []updateSkillRow{{Present: true, Installed: "0.26.0", Status: "current"}},
		Connector: probeConnector(cfg, "0.26.0"),
	}
	rep.Behind = countBehind(rep)
	rep.Mismatched, rep.Unverified = countVersionGateProblems(rep)
	rep.Ready = rep.Behind == 0 && rep.Mismatched == 0 && rep.Unverified == 0
	if rep.Ready || rep.Mismatched != 2 {
		t.Fatalf("daemon exact drift and connector minor drift must block: %+v", rep)
	}
}

func TestVersionGateReadyOnlyForExactVerifiedRuntime(t *testing.T) {
	rep := updateReport{
		Target: "0.26.0",
		CLI:    &selfupdate.CLIOutcome{Status: "up-to-date"},
		Skills: []updateSkillRow{{Present: true, Installed: "0.26.0", Status: "current"}},
		Connector: &connectorReport{
			DaemonRunning: true,
			DaemonVersion: "v0.26.0",
			DaemonStatus:  "current",
			Versions:      []string{"0.26.0"},
			Windows:       1,
			Status:        "ok",
		},
	}
	rep.Behind = countBehind(rep)
	rep.Mismatched, rep.Unverified = countVersionGateProblems(rep)
	rep.Ready = rep.Behind == 0 && rep.Mismatched == 0 && rep.Unverified == 0
	if !rep.Ready {
		t.Fatalf("exact verified runtime should pass: %+v", rep)
	}
}

func TestProbeConnectorNoDaemonIsNotAnError(t *testing.T) {
	// A closed port range: probing must degrade to "no-daemon", never fail.
	cfg := &appConfig{host: "127.0.0.1", ports: "1-1"}
	rep := probeConnector(cfg, "0.26.0")
	if rep.Status != "no-daemon" || rep.DaemonStatus != "not-running" || rep.DaemonRunning {
		t.Errorf("unexpected report with no daemon: %+v", rep)
	}
}

func TestUpdateCheckExitCodeGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	claude := filepath.Join(home, ".claude", "skills", "pcbpilot")
	if err := os.MkdirAll(claude, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, ".version"), []byte("0.25.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// --version pins the target so the check stays offline; ports 1-1 has no daemon.
	args := []string{"update", "--check", "--json", "--version", "0.99.0", "--ports", "1-1"}
	var stdout, stderr bytes.Buffer
	if code := Run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("--check without --exit-code must exit 0, got %d: %s", code, stderr.String())
	}
	var rep updateReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("parse report: %v\n%s", err, stdout.String())
	}
	if rep.Mode != "check" || rep.Target != "0.99.0" {
		t.Fatalf("unexpected report head: %+v", rep)
	}
	if rep.Behind == 0 {
		t.Fatalf("a 0.25.1 skill dir is behind 0.99.0, report says nothing is: %+v", rep)
	}

	stdout.Reset()
	stderr.Reset()
	code := Run(append(args, "--exit-code"), &stdout, &stderr)
	if code != exitCodeUpdatesAvailable {
		t.Fatalf("--check --exit-code should exit %d when behind, got %d: %s",
			exitCodeUpdatesAvailable, code, stderr.String())
	}
	// The verdict must not print a bogus "exit status 10" error line.
	if strings.Contains(stderr.String(), "exit status") {
		t.Errorf("exit-code verdict leaked an error message: %q", stderr.String())
	}
}

func TestUpdateRejectsConflictingScopeFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"update", "--cli-only", "--skill-only"}, &stdout, &stderr); code != 1 {
		t.Fatalf("mutually exclusive flags must fail, got exit %d", code)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr should explain the conflict: %q", stderr.String())
	}
}

func TestUpdateFailedSkillSyncHasNonzeroExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"update", "--skill-only", "--client", "typo", "--version", "1.4.2", "--ports", "1-1", "--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("failed update returned %d: %s %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown skill client") {
		t.Fatalf("missing client diagnostic: %s", stderr.String())
	}
}

type updateFailureTransport struct{ paths []string }

func (r *updateFailureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.paths = append(r.paths, req.URL.Path)
	return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("offline fixture")), Request: req}, nil
}

func TestUpdateFailedCLIDoesNotUpgradeSkill(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	dir := filepath.Join(home, ".codex", "skills", "pcbpilot")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".version"), []byte("1.4.1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	transport := &updateFailureTransport{}
	oldClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: transport}
	oldVersion := version.Version
	version.Version = "v1.4.1"
	t.Cleanup(func() { http.DefaultClient = oldClient; version.Version = oldVersion })
	var stdout, stderr bytes.Buffer
	code := Run([]string{"update", "--version", "1.4.2", "--ports", "1-1", "--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("CLI failure returned %d: %s %s", code, stdout.String(), stderr.String())
	}
	var report updateReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.CLI == nil || report.CLI.Status != "error" {
		t.Fatalf("missing CLI failure: %+v", report)
	}
	for _, path := range transport.paths {
		if strings.HasSuffix(path, "skills.tar.gz") {
			t.Fatalf("downloaded Skill despite CLI failure: %v", transport.paths)
		}
	}
	marker, _ := os.ReadFile(filepath.Join(dir, ".version"))
	if string(marker) != "1.4.1\n" {
		t.Fatal("Skill changed on CLI failure")
	}
}

func TestPreservedSkillReportDoesNotClaimCurrent(t *testing.T) {
	report := updateReport{Target: "1.4.2", Skills: []updateSkillRow{{Client: "codex", Status: "preserved", Installed: "1.4.1"}}}
	if countBehind(report) != 1 {
		t.Fatal("preserved stale Skill was reported current")
	}
	notes := strings.Join(updateNotes(report), " ")
	if !strings.Contains(notes, "parity is not claimed") {
		t.Fatalf("missing preservation explanation: %s", notes)
	}
}

func TestUpdateCheckRejectsUnknownClientAndRelativeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	var stdout, stderr bytes.Buffer
	args := []string{"update", "--check", "--skill-only", "--version", "1.4.2", "--ports", "1-1", "--json", "--exit-code"}
	code := Run(append(args, "--client", "typo"), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "unknown skill client") {
		t.Fatalf("unknown client returned %d: %s", code, stderr.String())
	}
	t.Setenv("CODEX_HOME", "relative/codex")
	stdout.Reset()
	stderr.Reset()
	code = Run(append(args, "--client", "codex"), &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "absolute") {
		t.Fatalf("relative client home returned %d: %s", code, stderr.String())
	}
}
