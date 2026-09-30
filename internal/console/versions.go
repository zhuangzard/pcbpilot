package console

import (
	"regexp"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
)

// Component is one piece of the installed tool chain with its version and an
// alignment verdict against the daemon.
type Component struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Where   string `json:"where,omitempty"`
	// Align: aligned | drift (patch differs) | stale (major.minor differs) |
	// dev (non-release build, no verdict) | missing | linked (source checkout,
	// updated by git pull)
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

// skillClientLabel names selfupdate's skill clients for the table.
func skillClientLabel(client string) string {
	switch client {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "zcode":
		return "ZCode"
	case "agents":
		return "~/.agents"
	}
	return client
}

// components lists CLI/daemon, Skill installs, MCP registration and each
// connected window's connector.
func components(daemonVersion, userHome string, windows []map[string]any) []Component {
	out := []Component{
		{Name: "daemon", Version: daemonVersion, Align: "aligned", Detail: "running process"},
		{Name: "CLI", Version: daemonVersion, Align: "aligned", Detail: "same binary as the daemon (make dev refreshes both)"},
	}
	// Same client list and version source as the self-updater and
	// `pcbpilot update --check` (selfupdate.Targets): a copied install reads its
	// .version marker, a symlinked source checkout its SKILL.md metadata.version.
	targets := selfupdate.Targets(true)
	for _, t := range targets {
		c := Component{Name: "Skill (" + skillClientLabel(t.Client) + ")", Version: t.Installed, Where: t.Dir, Align: alignOf(t.Installed, daemonVersion)}
		if t.Linked != "" {
			c.Align, c.Detail = "linked", "linked (source) → "+t.Linked+" — updated by git pull"
		}
		out = append(out, c)
	}
	if len(targets) == 0 {
		out = append(out, Component{Name: "Skill", Align: "missing", Detail: "no installed skill dir found (run scripts/setup-agent.sh or `pcbpilot update`)"})
	}
	out = append(out, mcpComponent(daemonVersion, userHome))
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

// mcpComponent reports the MCP server the AI clients are registered with,
// consistent with `pcbpilot update --check`: a source install whose clients
// all run the checkout's server is "linked (source)"; otherwise the version is
// the release stamp in ~/.pcbpilot/mcp/current/VERSION (since v0.6.1 the
// release mcp.tar.gz carries the release version, not mcp/package.json's).
func mcpComponent(daemonVersion, userHome string) Component {
	var regs []selfupdate.Registration
	var clients []string
	for _, r := range selfupdate.MCPRegistrations(selfupdate.ClientEnv{Home: userHome}, selfupdate.MCPEntry{}) {
		if r.Status == "current" { // zero want: "current" = registered
			regs = append(regs, r)
			clients = append(clients, r.Client)
		}
	}
	c := Component{Name: "MCP", Align: "missing"}
	if len(regs) == 0 {
		c.Detail = "no pcbpilot MCP registration found (Claude Code / Codex / ZCode / ~/.agents) — run `pcbpilot update`"
		return c
	}
	registered := "registered for " + strings.Join(clients, ", ")
	if server, ok := selfupdate.SourceMCP(regs); ok {
		c.Version, c.Where, c.Align = "linked (source)", server, "linked"
		c.Detail = registered + "; source checkout — updated by git pull"
		return c
	}
	installed := selfupdate.MCPInstalledVersion()
	c.Where = selfupdate.MCPServerPath()
	if installed == "" {
		c.Detail = registered + ", but ~/.pcbpilot/mcp/current has no VERSION — run `pcbpilot update`"
		return c
	}
	c.Version, c.Align, c.Detail = installed, alignOf(installed, daemonVersion), registered+" (release-stamped)"
	var elsewhere []string
	for _, r := range regs {
		if r.Server != "" && r.Server != c.Where {
			elsewhere = append(elsewhere, r.Client+" → "+r.Server)
		}
	}
	if len(elsewhere) > 0 {
		c.Align = "stale"
		c.Detail += "; not pointing at ~/.pcbpilot/mcp/current: " + strings.Join(elsewhere, ", ") + " — run `pcbpilot update`"
	}
	return c
}
