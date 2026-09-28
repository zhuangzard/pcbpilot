package selfupdate

// MCP client registration — the Go port of scripts/agent_clients.py
// register/clean-upstream, so release installs need no python:
//
//	claude  ~/.claude.json "mcpServers"  (`claude mcp add --scope user` when the CLI exists)
//	codex   $CODEX_HOME/config.toml      [mcp_servers.pcbpilot] + .env table
//	zcode   ~/.zcode/cli/config.json     "mcp": {"servers": {...}}
//	agents  ~/.agents/mcp.json           "mcpServers" (only when the file exists)
//
// A client is touched only when its config root exists. The first write to an
// existing file keeps <file>.bak-pcbpilot; an entry that already matches is
// left alone (no rewrite on every update).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const MCPName = "pcbpilot"

// UpstreamMCPNames are the retired upstream easyeda-agent registrations.
var UpstreamMCPNames = []string{"easyeda-agent", "easyeda"}

// ClientEnv abstracts the machine for registration (tests fake it).
type ClientEnv struct {
	Home     string
	LookPath func(string) (string, error)
	Run      func(name string, args ...string) (string, error)
}

func (e ClientEnv) claudeJSON() string { return filepath.Join(e.Home, ".claude.json") }
func (e ClientEnv) claudeDir() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(e.Home, ".claude")
}
func (e ClientEnv) codexDir() string {
	if v := os.Getenv("CODEX_HOME"); filepath.IsAbs(v) {
		return v
	}
	return filepath.Join(e.Home, ".codex")
}
func (e ClientEnv) codexTOML() string  { return filepath.Join(e.codexDir(), "config.toml") }
func (e ClientEnv) zcodeJSON() string  { return filepath.Join(e.Home, ".zcode", "cli", "config.json") }
func (e ClientEnv) agentsJSON() string { return filepath.Join(e.Home, ".agents", "mcp.json") }
func (e ClientEnv) hasClaudeCLI() bool {
	if e.LookPath == nil {
		return false
	}
	_, err := e.LookPath("claude")
	return err == nil
}

// MCPEntry is the desired registration.
type MCPEntry struct {
	Node, Server, Bin string
}

// Registration is one client's registration state.
type Registration struct {
	Client string `json:"client"`
	Config string `json:"config"`
	Status string `json:"status"` // current | registered | updated | stale | missing | absent | error | would-register
	Server string `json:"server,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type jsonEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func (e MCPEntry) matches(j jsonEntry) bool {
	return j.Command == e.Node && len(j.Args) == 1 && j.Args[0] == e.Server && j.Env["PCBPILOT_BIN"] == e.Bin
}

// ── JSON helpers (keep every other key as-is) ──────────────────────────────

func loadObj(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}

func saveObj(path string, m map[string]any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp-pcbpilot"
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.WriteFile(tmp, buf.Bytes(), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func backupOnce(path string) {
	dst := path + ".bak-pcbpilot"
	if _, err := os.Stat(dst); err == nil {
		return
	}
	if raw, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(dst, raw, 0o600)
	}
}

func subMap(m map[string]any, keys ...string) map[string]any {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[k] = next
		}
		cur = next
	}
	return cur
}

func peekMap(m map[string]any, keys ...string) map[string]any {
	cur := m
	for _, k := range keys {
		next, ok := cur[k].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func toEntry(v any) (jsonEntry, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return jsonEntry{}, false
	}
	var j jsonEntry
	if json.Unmarshal(raw, &j) != nil {
		return jsonEntry{}, false
	}
	return j, true
}

// ── Codex TOML (line based, like agent_clients.py) ─────────────────────────

var tomlSection = regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)

func tomlDrop(text, name string) string {
	lines := strings.SplitAfter(text, "\n")
	out := make([]string, 0, len(lines))
	drop := false
	for _, l := range lines {
		if m := tomlSection.FindStringSubmatch(strings.TrimRight(l, "\r\n")); m != nil {
			base := strings.ReplaceAll(strings.TrimSpace(m[1]), `"`, "")
			drop = base == "mcp_servers."+name || strings.HasPrefix(base, "mcp_servers."+name+".")
		}
		if !drop {
			out = append(out, l)
		}
	}
	return strings.Join(out, "")
}

func tomlHas(text, name string) bool {
	for _, l := range strings.Split(text, "\n") {
		if m := tomlSection.FindStringSubmatch(strings.TrimRight(l, "\r")); m != nil {
			if strings.ReplaceAll(strings.TrimSpace(m[1]), `"`, "") == "mcp_servers."+name {
				return true
			}
		}
	}
	return false
}

// tomlQuote is a TOML basic string (backslashes and quotes escaped — Windows paths).
func tomlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func tomlBlock(name string, e MCPEntry) string {
	return fmt.Sprintf("\n[mcp_servers.%s]\ncommand = %s\nargs = [%s]\n\n[mcp_servers.%s.env]\nPCBPILOT_BIN = %s\n",
		name, tomlQuote(e.Node), tomlQuote(e.Server), name, tomlQuote(e.Bin))
}

// tomlEntry reads command/args/PCBPILOT_BIN of [mcp_servers.<name>].
func tomlEntry(text, name string) (jsonEntry, bool) {
	var j jsonEntry
	j.Env = map[string]string{}
	section, found := "", false
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r")
		if m := tomlSection.FindStringSubmatch(l); m != nil {
			section = strings.ReplaceAll(strings.TrimSpace(m[1]), `"`, "")
			if section == "mcp_servers."+name {
				found = true
			}
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch {
		case section == "mcp_servers."+name && k == "command":
			j.Command = tomlUnquote(v)
		case section == "mcp_servers."+name && k == "args":
			inner := strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
			if s := strings.TrimSpace(inner); s != "" {
				j.Args = []string{tomlUnquote(s)}
			}
		case section == "mcp_servers."+name+".env" && k == "PCBPILOT_BIN":
			j.Env["PCBPILOT_BIN"] = tomlUnquote(v)
		}
	}
	return j, found
}

func tomlUnquote(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
		return strings.Trim(v, `"`)
	}
	return strings.Trim(v, `'`)
}

// ── status ────────────────────────────────────────────────────────────────

// MCPRegistrations reports each client's registration against want (a zero
// want reports presence only).
func MCPRegistrations(env ClientEnv, want MCPEntry) []Registration {
	var out []Registration
	judge := func(r Registration, j jsonEntry, found bool) Registration {
		switch {
		case !found:
			r.Status = "missing"
		case want.Server != "" && !want.matches(j):
			r.Status = "stale"
			if len(j.Args) > 0 {
				r.Server = j.Args[0]
			}
			r.Detail = "points at " + r.Server
		default:
			r.Status = "current"
			if len(j.Args) > 0 {
				r.Server = j.Args[0]
			}
		}
		return r
	}
	// claude
	{
		r := Registration{Client: "claude", Config: env.claudeJSON()}
		if m, err := loadObj(env.claudeJSON()); err == nil {
			j, ok := toEntry(peekMap(m, "mcpServers")[MCPName])
			_, present := peekMap(m, "mcpServers")[MCPName]
			out = append(out, judge(r, j, ok && present))
		} else if isDir(env.claudeDir()) || env.hasClaudeCLI() {
			r.Status = "missing"
			out = append(out, r)
		} else {
			r.Status = "absent"
			out = append(out, r)
		}
	}
	// codex
	{
		r := Registration{Client: "codex", Config: env.codexTOML()}
		if !isDir(env.codexDir()) {
			r.Status = "absent"
			out = append(out, r)
		} else {
			raw, _ := os.ReadFile(env.codexTOML())
			j, found := tomlEntry(string(raw), MCPName)
			out = append(out, judge(r, j, found))
		}
	}
	// zcode
	{
		r := Registration{Client: "zcode", Config: env.zcodeJSON()}
		if !isDir(filepath.Dir(env.zcodeJSON())) {
			r.Status = "absent"
			out = append(out, r)
		} else {
			m, _ := loadObj(env.zcodeJSON())
			if m == nil {
				m = map[string]any{}
			}
			v, present := peekMap(m, "mcp", "servers")[MCPName]
			j, ok := toEntry(v)
			out = append(out, judge(r, j, ok && present))
		}
	}
	// agents
	{
		r := Registration{Client: "agents", Config: env.agentsJSON()}
		if m, err := loadObj(env.agentsJSON()); err != nil {
			r.Status = "absent"
			out = append(out, r)
		} else {
			v, present := peekMap(m, "mcpServers")[MCPName]
			j, ok := toEntry(v)
			out = append(out, judge(r, j, ok && present))
		}
	}
	return out
}

// ── register ──────────────────────────────────────────────────────────────

// RegisterMCP registers want with every present client. Errors are per
// client; the returned error joins them.
func RegisterMCP(env ClientEnv, want MCPEntry, dryRun bool) ([]Registration, error) {
	var errs []string
	regs := MCPRegistrations(env, want)
	for i, r := range regs {
		if r.Status == "absent" || r.Status == "current" {
			continue
		}
		if dryRun {
			regs[i].Status = "would-register"
			continue
		}
		prev := r.Status
		var err error
		switch r.Client {
		case "claude":
			err = registerClaude(env, want)
		case "codex":
			err = registerCodex(env, want)
		case "zcode":
			err = registerJSON(env.zcodeJSON(), want, true, "mcp", "servers")
		case "agents":
			err = registerJSON(env.agentsJSON(), want, false, "mcpServers")
		}
		if err != nil {
			regs[i].Status, regs[i].Detail = "error", err.Error()
			errs = append(errs, r.Client+": "+err.Error())
			continue
		}
		regs[i].Server = want.Server
		regs[i].Detail = ""
		if prev == "stale" {
			regs[i].Status = "updated"
		} else {
			regs[i].Status = "registered"
		}
	}
	if len(errs) > 0 {
		return regs, fmt.Errorf("MCP registration: %s", strings.Join(errs, "; "))
	}
	return regs, nil
}

func registerClaude(env ClientEnv, want MCPEntry) error {
	if env.hasClaudeCLI() && env.Run != nil {
		_, _ = env.Run("claude", "mcp", "remove", MCPName, "-s", "user")
		out, err := env.Run("claude", "mcp", "add", MCPName, "--scope", "user",
			"--env", "PCBPILOT_BIN="+want.Bin, "--", want.Node, want.Server)
		if err != nil {
			return fmt.Errorf("claude mcp add: %v: %s", err, strings.TrimSpace(out))
		}
		return nil
	}
	path := env.claudeJSON()
	m, err := loadObj(path)
	if os.IsNotExist(err) {
		m, err = map[string]any{}, nil
	}
	if err != nil {
		return err
	}
	backupOnce(path)
	subMap(m, "mcpServers")[MCPName] = map[string]any{
		"type": "stdio", "command": want.Node, "args": []string{want.Server},
		"env": map[string]string{"PCBPILOT_BIN": want.Bin},
	}
	return saveObj(path, m)
}

func registerCodex(env ClientEnv, want MCPEntry) error {
	path := env.codexTOML()
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		backupOnce(path)
	}
	text := strings.TrimRight(tomlDrop(string(raw), MCPName), "\n")
	if text != "" {
		text += "\n"
	}
	text += tomlBlock(MCPName, want)
	return os.WriteFile(path, []byte(text), 0o644)
}

func registerJSON(path string, want MCPEntry, typed bool, keys ...string) error {
	m, err := loadObj(path)
	if os.IsNotExist(err) {
		m, err = map[string]any{}, nil
	} else if err == nil {
		backupOnce(path)
	}
	if err != nil {
		return err
	}
	entry := map[string]any{"command": want.Node, "args": []string{want.Server}, "env": map[string]string{"PCBPILOT_BIN": want.Bin}}
	if typed {
		entry["type"] = "stdio"
	}
	subMap(m, keys...)[MCPName] = entry
	return saveObj(path, m)
}

// ── clean-upstream ────────────────────────────────────────────────────────

// CleanUpstreamMCP removes the retired upstream registrations (easyeda-agent,
// easyeda) from every client, copying each edited file into backupDir first
// (RESTORE.txt there says how to undo). Returns "client:name" per removal.
func CleanUpstreamMCP(env ClientEnv, backupDir string, dryRun bool) ([]string, error) {
	var removed []string
	backup := func(path string) error {
		if dryRun {
			return nil
		}
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(backupDir, strings.TrimPrefix(filepath.Base(path), ".")+".orig"), raw, 0o600)
	}
	jsonClean := func(label, path string, keys ...string) error {
		m, err := loadObj(path)
		if err != nil {
			return nil // absent or unreadable: nothing of ours to clean
		}
		servers := peekMap(m, keys...)
		var hit []string
		for _, n := range UpstreamMCPNames {
			if _, ok := servers[n]; ok {
				hit = append(hit, n)
			}
		}
		if len(hit) == 0 {
			return nil
		}
		for _, n := range hit {
			removed = append(removed, label+":"+n)
		}
		if dryRun {
			return nil
		}
		if label == "claude" && env.hasClaudeCLI() && env.Run != nil {
			if err := backup(path); err != nil {
				return err
			}
			for _, n := range hit {
				_, _ = env.Run("claude", "mcp", "remove", n, "-s", "user")
			}
			return nil
		}
		if err := backup(path); err != nil {
			return err
		}
		for _, n := range hit {
			delete(servers, n)
		}
		return saveObj(path, m)
	}
	var errs []string
	if err := jsonClean("claude", env.claudeJSON(), "mcpServers"); err != nil {
		errs = append(errs, err.Error())
	}
	if raw, err := os.ReadFile(env.codexTOML()); err == nil {
		text := string(raw)
		var hit []string
		for _, n := range UpstreamMCPNames {
			if tomlHas(text, n) {
				hit = append(hit, n)
			}
		}
		for _, n := range hit {
			removed = append(removed, "codex:"+n)
			text = tomlDrop(text, n)
		}
		if len(hit) > 0 && !dryRun {
			if err := backup(env.codexTOML()); err != nil {
				errs = append(errs, err.Error())
			} else if err := os.WriteFile(env.codexTOML(), []byte(text), 0o644); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if err := jsonClean("zcode", env.zcodeJSON(), "mcp", "servers"); err != nil {
		errs = append(errs, err.Error())
	}
	if err := jsonClean("agents", env.agentsJSON(), "mcpServers"); err != nil {
		errs = append(errs, err.Error())
	}
	if len(removed) > 0 && !dryRun {
		f, err := os.OpenFile(filepath.Join(backupDir, "RESTORE.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "# pcbpilot update removed upstream MCP registrations: %s\n# Edited config files were copied here as *.orig; copy them back to undo.\n", strings.Join(removed, ", "))
			f.Close()
		}
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return removed, nil
}
