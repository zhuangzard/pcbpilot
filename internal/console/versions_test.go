package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func byName(cs []Component) map[string]Component {
	m := map[string]Component{}
	for _, c := range cs {
		m[c.Name] = c
	}
	return m
}

// The component table uses selfupdate's client list (ZCode included) and its
// version sources: a copied Skill's .version marker, a linked one's SKILL.md
// metadata.version; the MCP row shows the release stamp or "linked (source)"
// like `pcbpilot update --check`.
func TestComponentsFollowSelfUpdateTargetsAndMCPStamp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	// Source checkout with a linked Skill for ZCode and Claude Code; a copied
	// release Skill for Codex.
	repo := filepath.Join(t.TempDir(), "pcbpilot")
	writeFile(t, filepath.Join(repo, ".agents", "skills", "pcbpilot", "SKILL.md"),
		"---\nname: pcbpilot\nmetadata:\n  version: \"0.6.1\"\n---\n# pcbpilot\n")
	writeFile(t, filepath.Join(repo, ".agents", "skills", "pcbpilot", ".version"), "0.5.0\n") // stale marker from an old copy
	for _, client := range []string{".zcode", ".claude"} {
		if err := os.MkdirAll(filepath.Join(home, client, "skills"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(repo, ".agents", "skills", "pcbpilot"), filepath.Join(home, client, "skills", "pcbpilot")); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(home, ".codex", "skills", "pcbpilot", "SKILL.md"), "---\nname: pcbpilot\n---\n")
	writeFile(t, filepath.Join(home, ".codex", "skills", "pcbpilot", ".version"), "0.6.1\n")

	// Release-stamped MCP (mcp/package.json says 0.18.3, VERSION says 0.6.1).
	cur := filepath.Join(home, ".pcbpilot", "mcp", "0.6.1")
	writeFile(t, filepath.Join(cur, "VERSION"), "0.6.1\n")
	writeFile(t, filepath.Join(cur, "package.json"), `{"version":"0.18.3"}`)
	writeFile(t, filepath.Join(cur, "src", "server.mjs"), "")
	if err := os.Symlink(cur, filepath.Join(home, ".pcbpilot", "mcp", "current")); err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(home, ".pcbpilot", "mcp", "current", "src", "server.mjs")
	claudeJSON := func(server string) {
		b, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{"pcbpilot": map[string]any{"command": "node", "args": []string{server}}}})
		writeFile(t, filepath.Join(home, ".claude.json"), string(b))
	}
	claudeJSON(server)

	m := byName(components("v0.6.1", home, nil))
	if z, ok := m["Skill (ZCode)"]; !ok || z.Version != "0.6.1" || z.Align != "linked" || !strings.Contains(z.Detail, "linked (source)") {
		t.Fatalf("ZCode skill row: %+v (all: %+v)", z, m)
	}
	if c := m["Skill (Claude Code)"]; c.Version != "0.6.1" || c.Align != "linked" {
		t.Fatalf("linked skill must read SKILL.md metadata.version, not the stale marker: %+v", c)
	}
	if c := m["Skill (Codex)"]; c.Version != "0.6.1" || c.Align != "aligned" {
		t.Fatalf("copied skill: %+v", c)
	}
	if _, ok := m["Skill (~/.agents)"]; ok {
		t.Fatal("absent client dirs are not listed")
	}
	if c := m["MCP"]; c.Version != "0.6.1" || c.Align != "aligned" || c.Where != server || !strings.Contains(c.Detail, "release-stamped") {
		t.Fatalf("release MCP row must show the release stamp: %+v", c)
	}

	// Source install: every client runs the checkout's server.
	writeFile(t, filepath.Join(home, ".pcbpilot", "install.json"), `{"kind":"source","repo":"`+repo+`"}`)
	claudeJSON(filepath.Join(repo, "mcp", "src", "server.mjs"))
	if c := byName(components("v0.6.1-13-gabc", home, nil))["MCP"]; c.Version != "linked (source)" || c.Align != "linked" {
		t.Fatalf("source MCP row: %+v", c)
	}

	// No registration at all.
	os.Remove(filepath.Join(home, ".claude.json"))
	if c := byName(components("v0.6.1", home, nil))["MCP"]; c.Align != "missing" {
		t.Fatalf("unregistered MCP: %+v", c)
	}
}
