package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
)

func readJSONMap(t *testing.T, p string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func stepOf(res applyResult, comp string) stepRow {
	var out stepRow
	for _, s := range res.Steps {
		if s.Component == comp {
			out = s
		}
	}
	return out
}

func isRestart(line string) bool {
	return strings.HasPrefix(line, "launchctl bootstrap") || strings.HasPrefix(line, "launchctl kickstart") ||
		strings.HasPrefix(line, "systemctl --user restart") || strings.HasPrefix(line, "systemctl --user enable") ||
		strings.HasPrefix(line, "powershell.exe")
}

func allComps() map[string]bool {
	m := map[string]bool{}
	for _, c := range allComponents {
		m[c] = true
	}
	return m
}

// Full one-step upgrade of a fake v0.6.0 install from a local asset dir:
// CLI swap, 4 skill clients (one symlinked source dir left alone), MCP install
// + registration for claude/codex/zcode/agents, sim tools, daemon restart via
// the (fake) service manager, connector download + health comparison, verify.
func TestFullUpdateFromLocalDirUpgradesEveryComponent(t *testing.T) {
	skipOnWindows(t)
	oi := seedOldInstall(t, "0.6.0")
	dstate := &fakeDaemonState{}
	dstate.set(daemonProbe{Running: true, Version: "v0.6.0", Windows: []healthWindow{{WindowID: "w1", ConnectorVersion: "0.6.0"}}})
	rec := &recorder{onCall: func(line string) {
		if isRestart(line) {
			dstate.set(daemonProbe{Running: true, Version: "v0.6.1", Windows: []healthWindow{{WindowID: "w1", ConnectorVersion: "0.6.0"}}})
		}
	}}
	useRecorder(t, rec)
	deps := testUpdateDeps(t, oi, "0.6.0", rec, dstate)
	src, err := selfupdate.LocalAssets(writeLocalDist(t, "0.6.1"))
	if err != nil {
		t.Fatal(err)
	}
	eng := &updateEngine{deps: deps}
	res := eng.apply(context.Background(), updatePlan{src: src, target: "0.6.1", components: allComps()})
	if res.Failed {
		t.Fatalf("update failed: %+v", res)
	}

	// a. CLI swapped in place, snapshot kept.
	if b, _ := os.ReadFile(oi.bin); string(b) != string(fakeBinaryBytes("0.6.1")) {
		t.Fatalf("CLI not swapped: %q", b)
	}
	if _, err := os.Stat(filepath.Join(oi.home, ".pcbpilot", "rollback", "0.6.0", "bin", "pcbpilot")); err != nil {
		t.Fatal("rollback snapshot of the old binary missing")
	}
	// b. skills: 3 real dirs updated (retired file removed), the symlink untouched.
	for _, root := range []string{".claude", ".codex", ".zcode"} {
		dir := filepath.Join(oi.home, root, "skills", "pcbpilot")
		if v := strings.TrimSpace(readText(filepath.Join(dir, ".version"))); v != "0.6.1" {
			t.Fatalf("%s skill at %q", root, v)
		}
		if _, err := os.Stat(filepath.Join(dir, "references", "retired.md")); err == nil {
			t.Fatalf("%s kept a file the release removed", root)
		}
	}
	if s := stepOf(res, "skill:agents"); s.Status != "linked" || !strings.Contains(s.Detail, oi.checkout) {
		t.Fatalf("symlinked skill must be reported as linked to the checkout: %+v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(oi.checkout, "SKILL.md")); string(b) != "SOURCE CHECKOUT" {
		t.Fatal("the source checkout behind the symlink was overwritten")
	}
	// c. MCP installed + registered in every client, upstream retired.
	if selfupdate.MCPInstalledVersion() != "0.6.1" {
		t.Fatalf("mcp current = %q", selfupdate.MCPInstalledVersion())
	}
	server := selfupdate.MCPServerPath()
	claude := readJSONMap(t, filepath.Join(oi.home, ".claude.json"))
	cs := claude["mcpServers"].(map[string]any)
	if cs["easyeda-agent"] != nil || claude["numStartups"] != float64(3) {
		t.Fatalf("claude.json: upstream not removed or other keys lost: %v", claude)
	}
	if e := cs["pcbpilot"].(map[string]any); e["args"].([]any)[0] != server || e["command"] != "/fake/bin/node" {
		t.Fatalf("claude registration: %v", e)
	}
	toml := readText(filepath.Join(oi.home, ".codex", "config.toml"))
	if !strings.Contains(toml, "[mcp_servers.pcbpilot]") || !strings.Contains(toml, server) || strings.Contains(toml, "[mcp_servers.easyeda]") || !strings.Contains(toml, `model = "o3"`) {
		t.Fatalf("codex config.toml:\n%s", toml)
	}
	z := readJSONMap(t, filepath.Join(oi.home, ".zcode", "cli", "config.json"))
	if z["theme"] != "dark" || z["mcp"].(map[string]any)["servers"].(map[string]any)["pcbpilot"] == nil {
		t.Fatalf("zcode config: %v", z)
	}
	if readJSONMap(t, filepath.Join(oi.home, ".agents", "mcp.json"))["mcpServers"].(map[string]any)["pcbpilot"] == nil {
		t.Fatal("agents mcp.json not registered")
	}
	// d. tools: ngspice already ok (fake env) → current.
	if s := stepOf(res, "tools:ngspice"); s.Status != "current" {
		t.Fatalf("tools step: %+v", s)
	}
	// e. daemon restarted through the service manager (none installed → install).
	calls := rec.joined()
	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(calls, "launchctl bootstrap gui/") {
			t.Fatalf("expected launchd install+bootstrap, got:\n%s", calls)
		}
	case "linux":
		if !strings.Contains(calls, "systemctl --user restart pcbpilot-daemon.service") {
			t.Fatalf("expected systemd restart, got:\n%s", calls)
		}
	}
	if s := stepOf(res, compDaemon); s.Status != "restarted" {
		t.Fatalf("daemon step: %+v", s)
	}
	// f. connector: downloaded, still 0.6.0 in EasyEDA → pending with the 3 steps.
	eext := selfupdate.ConnectorPath("0.6.1")
	if _, err := os.Stat(eext); err != nil {
		t.Fatal("connector not downloaded")
	}
	if s := stepOf(res, compConnector); s.Status != "pending-import" || len(res.ImportNote) != 3 || !strings.Contains(res.ImportNote[1], eext) {
		t.Fatalf("connector report: %+v %v", s, res.ImportNote)
	}
	// g. verify all green; summary names the components and the connector file.
	for _, v := range res.Verify {
		if !v.OK && v.Required {
			t.Fatalf("verify %s failed: %s", v.Check, v.Detail)
		}
	}
	for _, want := range []string{"0.6.0 → 0.6.1 updated", "cli", "skill", "mcp", "daemon", "connector 0.6.1", eext} {
		if !strings.Contains(res.Summary, want) {
			t.Fatalf("summary %q missing %q", res.Summary, want)
		}
	}

	// Second run: nothing to do, the service is now installed → kickstart.
	deps.version = "v0.6.1"
	if runtime.GOOS == "darwin" {
		// the plist now exists and points at oi.bin
		rec.calls = nil
	}
	res2 := (&updateEngine{deps: deps}).apply(context.Background(), updatePlan{src: src, target: "0.6.1", components: allComps()})
	if res2.Failed {
		t.Fatalf("second run failed: %+v", res2)
	}
	for _, s := range res2.Steps {
		if s.Status == "updated" || s.Status == "installed" {
			t.Fatalf("second run must not change anything: %+v", s)
		}
	}
	if runtime.GOOS == "darwin" && !strings.Contains(rec.joined(), "launchctl kickstart -k gui/501/com.pcbpilot.daemon") {
		t.Fatalf("an installed service must be kickstarted:\n%s", rec.joined())
	}
}

func TestRestartDaemonServiceSelectsCommandPerOS(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, "bin", "pcbpilot")
	mustWrite(t, bin, []byte("x"), 0o755)
	cases := []struct {
		goos, seed, want string
	}{
		{"darwin", daemonServicePlist(bin, "/log"), "launchctl kickstart -k gui/501/com.pcbpilot.daemon"},
		{"linux", daemonServiceUnitFile(bin), "systemctl --user restart pcbpilot-daemon.service"},
		{"windows", "", "powershell.exe -NoProfile -Command Start-Process -WindowStyle Hidden -FilePath '" + bin + "' -ArgumentList 'daemon','start'"},
		{"darwin", daemonServicePlist("/elsewhere/pcbpilot", "/log"), "launchctl bootstrap gui/"}, // stale binary → reinstall
		{"linux", "", "systemctl --user enable --now pcbpilot-daemon.service"},                    // missing → install
	}
	for _, c := range cases {
		t.Run(c.goos+"/"+c.want[:12], func(t *testing.T) {
			h := t.TempDir()
			if c.seed != "" {
				mustWrite(t, daemonServicePath(c.goos, h), []byte(c.seed), 0o644)
			}
			rec := &recorder{}
			if c.goos == "windows" {
				// reg query succeeds with our binary: the Run key is installed.
				rec.fail = map[string]error{}
			}
			useRecorder(t, rec)
			if c.goos == "windows" {
				old := daemonServiceRunner
				daemonServiceRunner = func(name string, args ...string) (string, error) {
					if name == "reg" && len(args) > 0 && args[0] == "query" {
						rec.run(name, args...)
						return "    pcbpilot-daemon    REG_SZ    " + daemonServiceRunValue(bin), nil
					}
					return rec.run(name, args...)
				}
				t.Cleanup(func() { daemonServiceRunner = old })
			}
			deps := updateDeps{goos: c.goos, home: h, binPath: bin, uid: 501, runner: daemonServiceRunner,
				stopDaemon: func() error { rec.run("stop-daemon"); return nil }}
			if _, err := restartDaemonService(deps); err != nil {
				t.Fatalf("restart: %v\n%s", err, rec.joined())
			}
			if !strings.Contains(rec.joined(), c.want) {
				t.Fatalf("want %q in:\n%s", c.want, rec.joined())
			}
			if c.goos == "windows" && !strings.Contains(rec.joined(), "stop-daemon") {
				t.Fatal("windows must stop the running daemon before starting the new one")
			}
		})
	}
}

// A failed required verify (MCP handshake) restores the snapshot: binary,
// skills and the MCP link all go back to the previous version.
func TestUpdateRollsBackOnFailedVerify(t *testing.T) {
	skipOnWindows(t)
	oi := seedOldInstall(t, "0.6.0")
	rec := &recorder{}
	useRecorder(t, rec)
	dstate := &fakeDaemonState{}
	deps := testUpdateDeps(t, oi, "0.6.0", rec, dstate)
	// Start from an installed MCP 0.6.0 so the link has somewhere to go back to.
	old, _ := selfupdate.LocalAssets(writeLocalDist(t, "0.6.0"))
	if _, err := selfupdate.InstallMCP(context.Background(), old, false); err != nil {
		t.Fatal(err)
	}
	deps.handshake = func(context.Context, string, string, string) (selfupdate.MCPHandshakeResult, error) {
		return selfupdate.MCPHandshakeResult{}, errors.New("server crashed on start")
	}
	src, _ := selfupdate.LocalAssets(writeLocalDist(t, "0.6.1"))
	res := (&updateEngine{deps: deps}).apply(context.Background(), updatePlan{src: src, target: "0.6.1",
		components: map[string]bool{compCLI: true, compSkill: true, compMCP: true}})
	if !res.Failed || len(res.RolledBack) == 0 {
		t.Fatalf("a failed handshake must fail and roll back: %+v", res)
	}
	if b, _ := os.ReadFile(oi.bin); string(b) != string(fakeBinaryBytes("0.6.0")) {
		t.Fatalf("binary not rolled back: %q", b)
	}
	if v := strings.TrimSpace(readText(filepath.Join(oi.home, ".claude", "skills", "pcbpilot", ".version"))); v != "0.6.0" {
		t.Fatalf("skill not rolled back: %q", v)
	}
	if selfupdate.MCPInstalledVersion() != "0.6.0" {
		t.Fatalf("mcp link not rolled back: %q", selfupdate.MCPInstalledVersion())
	}
	if !strings.Contains(res.Summary, "rolled back") {
		t.Fatalf("summary must say so: %q", res.Summary)
	}
}

func TestSelectComponentsOnlySkipAndLegacyFlags(t *testing.T) {
	c, err := selectComponents(false, false, []string{"cli", "skills"}, nil)
	if err != nil || len(c) != 2 || !c[compCLI] || !c[compSkill] {
		t.Fatalf("--only: %v %v", c, err)
	}
	c, _ = selectComponents(false, false, nil, []string{"tools", "daemon"})
	if c[compTools] || c[compDaemon] || !c[compConnector] || len(c) != 4 {
		t.Fatalf("--skip: %v", c)
	}
	c, _ = selectComponents(true, false, nil, nil)
	if len(c) != 1 || !c[compCLI] {
		t.Fatalf("--cli-only: %v", c)
	}
	if _, err := selectComponents(false, false, []string{"firmware"}, nil); err == nil {
		t.Fatal("unknown component accepted")
	}
	if _, err := selectComponents(false, false, []string{"cli"}, []string{"cli"}); err == nil {
		t.Fatal("empty selection accepted")
	}
}

// `update --check` reports every component: CLI, 4 skill clients (linked
// ones marked), MCP, sim tools, daemon and connector.
func TestUpdateCheckReportsMCPToolsLinkedAndZCode(t *testing.T) {
	skipOnWindows(t)
	seedOldInstall(t, "0.6.0")
	var ran [][]string
	oldSim := simToolsEnv
	simToolsEnv = fakeSimEnv(map[string]string{"brew": "/b/brew"}, &ran) // ngspice missing
	t.Cleanup(func() { simToolsEnv = oldSim })
	var stdout, stderr strings.Builder
	code := Run([]string{"update", "--check", "--json", "--version", "0.6.1", "--ports", "1-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("check exit %d: %s", code, stderr.String())
	}
	var rep updateReport
	if err := json.Unmarshal([]byte(stdout.String()), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.MCP == nil || len(rep.Tools) == 0 {
		t.Fatalf("check must report mcp and tools: %+v", rep)
	}
	found := false
	for _, s := range rep.Skills {
		if s.Client == "agents" && s.Status == "linked" {
			found = true
		}
		if s.Client == "zcode" && s.Status != "behind" {
			t.Fatalf("zcode skill must be checked: %+v", s)
		}
	}
	if !found {
		t.Fatalf("linked skill must be reported: %+v", rep.Skills)
	}
	if rep.Behind == 0 || rep.Ready {
		t.Fatalf("0.6.0 install is behind 0.6.1: %+v", rep)
	}
}
