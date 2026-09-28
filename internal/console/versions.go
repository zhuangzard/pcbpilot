package console

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Component is one piece of the installed tool chain with its version and an
// alignment verdict against the daemon.
type Component struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Where   string `json:"where,omitempty"`
	// Align: aligned | drift (patch differs) | stale (major.minor differs) |
	// dev (non-release build, no verdict) | missing | info (independently versioned)
	Align  string `json:"align"`
	Detail string `json:"detail,omitempty"`
}

var semverRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$`)

// alignOf compares a component version with the daemon's.
func alignOf(v, daemon string) string {
	if v == "" {
		return "missing"
	}
	a, b := semverRe.FindStringSubmatch(v), semverRe.FindStringSubmatch(daemon)
	if a == nil || b == nil || strings.Contains(daemon, "-g") || strings.Contains(daemon, "dirty") || daemon == "dev" {
		return "dev"
	}
	if a[1] != b[1] || a[2] != b[2] {
		return "stale"
	}
	if a[3] != b[3] {
		return "drift"
	}
	return "aligned"
}

var skillVersionRe = regexp.MustCompile(`(?m)^\s*version:\s*"?([^"\s]+)"?`)

// skillDirs are where setup-agent.sh links the Skill.
func skillDirs(userHome string) []struct{ client, dir string } {
	claude := os.Getenv("CLAUDE_CONFIG_DIR")
	if claude == "" {
		claude = filepath.Join(userHome, ".claude")
	}
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(userHome, ".codex")
	}
	return []struct{ client, dir string }{
		{"Claude Code", filepath.Join(claude, "skills", "pcbpilot")},
		{"Codex", filepath.Join(codex, "skills", "pcbpilot")},
		{"~/.agents", filepath.Join(userHome, ".agents", "skills", "pcbpilot")},
	}
}

// components lists CLI/daemon, Skill installs, MCP registration and each
// connected window's connector.
func components(daemonVersion, userHome string, windows []map[string]any) []Component {
	out := []Component{
		{Name: "daemon", Version: daemonVersion, Align: "aligned", Detail: "running process"},
		{Name: "CLI", Version: daemonVersion, Align: "aligned", Detail: "same binary as the daemon (make dev refreshes both)"},
	}
	found := false
	for _, s := range skillDirs(userHome) {
		b, err := os.ReadFile(filepath.Join(s.dir, "SKILL.md"))
		if err != nil {
			continue
		}
		found = true
		v := ""
		if m := skillVersionRe.FindSubmatch(b); m != nil {
			v = string(m[1])
		}
		out = append(out, Component{Name: "Skill (" + s.client + ")", Version: v, Where: s.dir, Align: alignOf(v, daemonVersion)})
	}
	if !found {
		out = append(out, Component{Name: "Skill", Align: "missing", Detail: "no installed skill dir found (run scripts/setup-agent.sh)"})
	}
	out = append(out, mcpComponent(userHome))
	for _, w := range windows {
		cv, _ := w["connectorVersion"].(string)
		id, _ := w["windowId"].(string)
		c := Component{Name: "connector " + shortID(id), Version: cv, Align: alignOf(cv, daemonVersion)}
		if ok, isBool := w["connectorVersionOk"].(bool); isBool && !ok {
			c.Align = "stale"
			c.Detail = "daemon health flags this connector as stale — re-import the .eext and reload the page"
		}
		if ctx, _ := w["context"].(map[string]any); ctx != nil {
			if n, _ := ctx["projectName"].(string); n != "" {
				c.Where = n
			}
		}
		out = append(out, c)
	}
	return out
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// mcpComponent finds the pcbpilot MCP registration in Claude Code's user
// config and reads its package version. The MCP adapter is versioned on its
// own track, so it gets an "info" verdict, not an alignment one.
func mcpComponent(userHome string) Component {
	c := Component{Name: "MCP", Align: "missing", Detail: "no pcbpilot MCP registration found in ~/.claude.json"}
	b, err := os.ReadFile(filepath.Join(userHome, ".claude.json"))
	if err != nil {
		return c
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &cfg) != nil {
		return c
	}
	srv, ok := cfg.MCPServers["pcbpilot"]
	if !ok {
		return c
	}
	c.Align, c.Detail = "info", "registered (independently versioned adapter)"
	for _, a := range append([]string{srv.Command}, srv.Args...) {
		if strings.HasSuffix(a, "server.mjs") {
			c.Where = a
			pkg := filepath.Join(filepath.Dir(filepath.Dir(a)), "package.json")
			if pb, err := os.ReadFile(pkg); err == nil {
				var p struct{ Version string }
				if json.Unmarshal(pb, &p) == nil {
					c.Version = p.Version
				}
			}
		}
	}
	return c
}
