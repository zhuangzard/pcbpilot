package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func tarGz(t *testing.T, files map[string]string) []byte {
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

func mcpArchive(t *testing.T, version string) []byte {
	return tarGz(t, map[string]string{
		"mcp/src/server.mjs": "// server " + version,
		"mcp/package.json":   `{"name":"pcbpilot-mcp"}`,
		"mcp/VERSION":        version + "\n",
		"mcp/node_modules/@modelcontextprotocol/sdk/package.json": `{"version":"1.30.0"}`,
	})
}

func eext(t *testing.T, version string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("extension.json")
	fmt.Fprintf(w, `{"version":%q}`, version)
	w, _ = zw.Create("dist/index.js")
	w.Write([]byte("compiled"))
	zw.Close()
	return buf.Bytes()
}

// localRelease writes a local asset dir for version and returns its source.
func localRelease(t *testing.T, version string, withMCP bool) AssetSource {
	t.Helper()
	dir := t.TempDir()
	assets := map[string][]byte{
		"skills.tar.gz":           tarGz(t, map[string]string{"pcbpilot/SKILL.md": skillDocument(version, "body")}),
		"pcbpilot-connector.eext": eext(t, version),
	}
	if withMCP {
		assets[MCPAsset] = mcpArchive(t, version)
	}
	var sums strings.Builder
	for name, data := range assets {
		os.WriteFile(filepath.Join(dir, name), data, 0o644)
		fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(data), name)
	}
	os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(sums.String()), 0o644)
	src, err := LocalAssets(dir)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestMCPInstallSwitchesCurrentKeepsPreviousAndPrunes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("junction path covered on Windows CI")
	}
	isolatedSkillHome(t)
	ctx := context.Background()
	for _, v := range []string{"0.6.0", "0.6.1", "0.6.2"} {
		out, err := InstallMCP(ctx, localRelease(t, v, true), false)
		if err != nil || (out.Status != "installed" && out.Status != "updated") {
			t.Fatalf("%s: %+v %v", v, out, err)
		}
		if MCPInstalledVersion() != v || MCPCurrentTarget() != v {
			t.Fatalf("current should be %s: %s / %s", v, MCPInstalledVersion(), MCPCurrentTarget())
		}
	}
	if _, err := os.Stat(filepath.Join(MCPRoot(), "0.6.1")); err != nil {
		t.Fatal("the version current replaced must be kept for rollback")
	}
	if _, err := os.Stat(filepath.Join(MCPRoot(), "0.6.0")); err == nil {
		t.Fatal("older versions must be pruned")
	}
	if out, _ := InstallMCP(ctx, localRelease(t, "0.6.2", true), false); out.Status != "up-to-date" {
		t.Fatalf("same version must be a no-op: %+v", out)
	}
	// A release that predates mcp.tar.gz is skipped, never an error.
	out, err := InstallMCP(ctx, localRelease(t, "0.6.3", false), false)
	if err != nil || out.Status != "skipped" {
		t.Fatalf("missing asset must be skipped: %+v %v", out, err)
	}
	if err := SetMCPCurrent("0.6.1"); err != nil || MCPInstalledVersion() != "0.6.1" {
		t.Fatalf("rollback of the link: %v %s", err, MCPInstalledVersion())
	}
}

func TestMCPArchiveRejectsTraversalAndWrongVersion(t *testing.T) {
	isolatedSkillHome(t)
	if _, err := installMCPArchive(tarGz(t, map[string]string{"mcp/../evil": "x"}), "0.6.1"); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := installMCPArchive(mcpArchive(t, "0.6.0"), "0.6.1"); err == nil || !strings.Contains(err.Error(), "VERSION") {
		t.Fatalf("wrong VERSION accepted: %v", err)
	}
}

func readJSONFile(t *testing.T, p string) map[string]any {
	t.Helper()
	m, err := loadObj(p)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRegisterMCPEditsEveryClientCleansUpstreamAndIsIdempotent(t *testing.T) {
	home := isolatedSkillHome(t)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(home, ".codex"), 0o755))
	must(os.MkdirAll(filepath.Join(home, ".zcode", "cli"), 0o755))
	must(os.MkdirAll(filepath.Join(home, ".agents"), 0o755))
	must(os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"numStartups":7,"mcpServers":{"easyeda-agent":{"command":"x"},"other":{"command":"keep"}}}`), 0o600))
	must(os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"o3\"\n\n[mcp_servers.easyeda]\ncommand = \"old\"\n\n[mcp_servers.easyeda.env]\nX = \"1\"\n\n[mcp_servers.keep]\ncommand = \"k\"\n"), 0o644))
	must(os.WriteFile(filepath.Join(home, ".zcode", "cli", "config.json"), []byte(`{"theme":"dark"}`), 0o644))
	must(os.WriteFile(filepath.Join(home, ".agents", "mcp.json"), []byte(`{"mcpServers":{}}`), 0o644))
	env := ClientEnv{Home: home, LookPath: func(string) (string, error) { return "", os.ErrNotExist }}

	bk := filepath.Join(home, "backup")
	removed, err := CleanUpstreamMCP(env, bk, false)
	if err != nil || len(removed) != 2 {
		t.Fatalf("upstream cleanup: %v %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(bk, "RESTORE.txt")); err != nil {
		t.Fatal("cleanup must leave RESTORE.txt next to the backups")
	}
	want := MCPEntry{Node: "/opt/node", Server: filepath.Join(home, ".pcbpilot", "mcp", "current", "src", "server.mjs"), Bin: `C:\Users\a "b"\pcbpilot.exe`}
	regs, err := RegisterMCP(env, want, false)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, r := range regs {
		status[r.Client] = r.Status
	}
	for _, c := range []string{"claude", "codex", "zcode", "agents"} {
		if status[c] != "registered" {
			t.Fatalf("%s: %v", c, status)
		}
	}
	claude := readJSONFile(t, filepath.Join(home, ".claude.json"))
	servers := claude["mcpServers"].(map[string]any)
	if claude["numStartups"] != float64(7) || servers["other"] == nil || servers["easyeda-agent"] != nil || servers[MCPName] == nil {
		t.Fatalf("claude.json must keep other keys, drop upstream, add ours: %v", claude)
	}
	toml, _ := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	if !strings.Contains(string(toml), "[mcp_servers.keep]") || strings.Contains(string(toml), "mcp_servers.easyeda]") {
		t.Fatalf("codex config lost a section or kept upstream:\n%s", toml)
	}
	if j, ok := tomlEntry(string(toml), MCPName); !ok || !want.matches(j) {
		t.Fatalf("codex entry does not round-trip (Windows path quoting): %+v\n%s", j, toml)
	}
	z := readJSONFile(t, filepath.Join(home, ".zcode", "cli", "config.json"))
	if z["theme"] != "dark" || peekMap(z, "mcp", "servers")[MCPName] == nil {
		t.Fatalf("zcode config: %v", z)
	}
	if peekMap(readJSONFile(t, filepath.Join(home, ".agents", "mcp.json")), "mcpServers")[MCPName] == nil {
		t.Fatal("agents mcp.json not registered")
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json.bak-pcbpilot")); err != nil {
		t.Fatal("first write must keep a .bak-pcbpilot copy")
	}
	// Second run: every client is current and no file is rewritten.
	before, _ := os.Stat(filepath.Join(home, ".codex", "config.toml"))
	time.Sleep(10 * time.Millisecond)
	regs, err = RegisterMCP(env, want, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range regs {
		if r.Status != "current" {
			t.Fatalf("second registration must be a no-op: %+v", regs)
		}
	}
	after, _ := os.Stat(filepath.Join(home, ".codex", "config.toml"))
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("an up-to-date registration must not rewrite the config")
	}
	// A moved node binary makes the entries stale → updated.
	want.Node = "/usr/local/bin/node"
	regs, _ = RegisterMCP(env, want, false)
	for _, r := range regs {
		if r.Status != "updated" {
			t.Fatalf("stale registration must be updated: %+v", regs)
		}
	}
}

func TestRegisterMCPUsesClaudeCLIWhenPresent(t *testing.T) {
	home := isolatedSkillHome(t)
	var calls []string
	env := ClientEnv{Home: home,
		LookPath: func(n string) (string, error) {
			if n == "claude" {
				return "/bin/claude", nil
			}
			return "", os.ErrNotExist
		},
		Run: func(name string, args ...string) (string, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			return "", nil
		}}
	if _, err := RegisterMCP(env, MCPEntry{Node: "/n", Server: "/s.mjs", Bin: "/b"}, false); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "claude mcp add pcbpilot --scope user --env PCBPILOT_BIN=/b -- /n /s.mjs") {
		t.Fatalf("claude CLI registration not used: %s", joined)
	}
}

func TestConnectorDownloadVerifiesManifestAndReuses(t *testing.T) {
	isolatedSkillHome(t)
	src := localRelease(t, "0.6.1", false)
	out, err := DownloadConnector(context.Background(), src)
	if err != nil || out.Status != "downloaded" || out.Path != ConnectorPath("0.6.1") {
		t.Fatalf("%+v %v", out, err)
	}
	if out, _ := DownloadConnector(context.Background(), src); out.Status != "present" {
		t.Fatalf("existing verified file must be reused: %+v", out)
	}
	if steps := ConnectorImportSteps(out.Path); len(steps) != 3 || !strings.Contains(steps[1], out.Path) {
		t.Fatalf("import steps must name the file: %v", steps)
	}
}

func TestSnapshotRestoreBinarySkillsAndMCPLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix symlink semantics")
	}
	home := isolatedSkillHome(t)
	bin := filepath.Join(home, "bin", "pcbpilot")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, []byte("OLD-BINARY"), 0o755)
	dir := seedSkill(t, "claude")
	if _, err := InstallMCP(context.Background(), localRelease(t, "0.6.0", true), false); err != nil {
		t.Fatal(err)
	}
	snap, err := TakeSnapshot("v0.6.0", bin)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(bin, []byte("NEW-BINARY"), 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("NEW"), 0o644)
	InstallMCP(context.Background(), localRelease(t, "0.6.1", true), false)

	got, err := LatestSnapshot("")
	if err != nil || got.Version != "0.6.0" {
		t.Fatalf("latest snapshot: %+v %v", got, err)
	}
	restored, err := RestoreSnapshot(context.Background(), snap)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(bin); string(b) != "OLD-BINARY" {
		t.Fatalf("binary not restored: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "SKILL.md")); string(b) != "OLD" {
		t.Fatalf("skill not restored: %q (%v)", b, restored)
	}
	if MCPInstalledVersion() != "0.6.0" {
		t.Fatalf("mcp link not restored: %s", MCPInstalledVersion())
	}
}

func TestRecordCheckBackoffAndRecovery(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	var c CheckCache
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		c = RecordCheck(c, now, "0.6.0", "", fmt.Errorf("dial tcp: no route to host"))
		if c.Failures != i+1 || c.NextRetryAt.Sub(now) != w || c.LastError == "" {
			t.Fatalf("failure %d: %+v (want delay %s)", i+1, c, w)
		}
	}
	c = RecordCheck(c, now, "0.6.0", "0.6.1", nil)
	if c.Failures != 0 || c.LastError != "" || !c.NextRetryAt.IsZero() || c.Latest != "0.6.1" || c.ReleaseURL == "" {
		t.Fatalf("recovery must clear the backoff: %+v", c)
	}
}

func TestAutoModeEnvOverridesConfigDefaultOn(t *testing.T) {
	isolatedSkillHome(t)
	t.Setenv(AutoUpdateEnv, "")
	if m, src := AutoMode(); m != AutoOn || src != "default" {
		t.Fatalf("default must be on: %s %s", m, src)
	}
	if err := SetAutoMode(AutoOff); err != nil {
		t.Fatal(err)
	}
	if m, _ := AutoMode(); m != AutoOff {
		t.Fatal("config off not honoured")
	}
	t.Setenv(AutoUpdateEnv, "1")
	if m, _ := AutoMode(); m != AutoOn {
		t.Fatal("env must override config")
	}
	t.Setenv(AutoUpdateEnv, "0")
	if m, _ := AutoMode(); m != AutoOff {
		t.Fatal("PCBPILOT_AUTO_UPDATE=0 must opt out")
	}
	if err := SetAutoMode("sometimes"); err == nil {
		t.Fatal("bad mode accepted")
	}
}

func TestSkillSyncLeavesLinkedSourceDirAloneAndCoversZCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink")
	}
	home := isolatedSkillHome(t)
	checkout := filepath.Join(home, "repo", ".agents", "skills", "pcbpilot")
	os.MkdirAll(checkout, 0o755)
	os.WriteFile(filepath.Join(checkout, "SKILL.md"), []byte("SOURCE"), 0o644)
	os.MkdirAll(filepath.Join(home, ".agents", "skills"), 0o755)
	if err := os.Symlink(checkout, skillDir("agents")); err != nil {
		t.Fatal(err)
	}
	seedSkill(t, "zcode")
	src := localRelease(t, "0.6.1", false)
	res, err := SyncSkills(context.Background(), SyncOptions{TargetVersion: "0.6.1", Assets: src}, nil)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]TargetOutcome{}
	for _, o := range res.Outcomes {
		by[o.Client] = o
	}
	if by["agents"].Status != "linked" || by["agents"].Linked == "" {
		t.Fatalf("symlinked skill must be reported linked: %+v", by["agents"])
	}
	if b, _ := os.ReadFile(filepath.Join(checkout, "SKILL.md")); string(b) != "SOURCE" {
		t.Fatal("the git checkout behind a linked skill was written")
	}
	if by["zcode"].Status != "updated" || readMarker(skillDir("zcode")) != "0.6.1" {
		t.Fatalf("zcode skill not updated: %+v", by["zcode"])
	}
	var tg []SkillTarget
	for _, x := range Targets(true) {
		tg = append(tg, x)
	}
	raw, _ := json.Marshal(tg)
	if !strings.Contains(string(raw), `"linked"`) {
		t.Fatalf("Targets must expose the link: %s", raw)
	}
}
