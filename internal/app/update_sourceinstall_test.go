package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
)

// A source checkout built at a release tag (clean vX.Y.Z stamp) must stay a
// source install: the daemon may not re-point MCP clients at a release
// tarball, linked skills report their SKILL.md version (not a stale .version
// marker), and an MCP registered at the checkout reads as "linked".
func TestSourceInstallAtReleaseTag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	repo := filepath.Join(home, "src", "pcbpilot")
	skill := filepath.Join(repo, ".agents", "skills", "pcbpilot")
	for _, d := range []string{filepath.Join(repo, ".git"), skill, filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".pcbpilot")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: pcbpilot\nmetadata:\n  version: 0.6.1\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Stale marker left over from an old copy install.
	if err := os.WriteFile(filepath.Join(skill, ".version"), []byte("0.2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(skill, filepath.Join(home, ".claude", "skills", "pcbpilot")); err != nil {
		t.Fatal(err)
	}
	if err := selfupdate.WriteJSON(selfupdate.InstallInfoPath(), selfupdate.InstallInfo{Kind: "source", Repo: repo, Version: "v0.6.1"}); err != nil {
		t.Fatal(err)
	}

	for _, tg := range selfupdate.Targets(true) {
		if tg.Client == "claude" && (tg.Linked == "" || tg.Installed != "0.6.1") {
			t.Fatalf("linked claude skill: %+v, want Installed 0.6.1", tg)
		}
	}
	u := &selfUpdater{version: "v0.6.1"}
	if u.release() {
		t.Fatal("source install at a release tag treated as a release install")
	}
	server := filepath.Join(repo, "mcp", "src", "server.mjs")
	if got, ok := sourceMCP([]selfupdate.Registration{{Client: "claude", Status: "stale", Server: server}}); !ok || got != server {
		t.Fatalf("sourceMCP = %q %v", got, ok)
	}
	if _, ok := sourceMCP([]selfupdate.Registration{{Client: "claude", Status: "stale", Server: "/elsewhere/server.mjs"}}); ok {
		t.Fatal("MCP registered elsewhere reported as the checkout's")
	}
}
