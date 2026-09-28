package app

// The update engine: one implementation of every upgrade step, used by the
// manual `pcbpilot update` and by the daemon's self-updater (daemon_selfupdate.go).
//
//	a cli        download (sha256) + run-verify + atomic swap
//	b skill      every present client dir; symlinked (source) dirs are left alone
//	c mcp        mcp.tar.gz → ~/.pcbpilot/mcp/<v>, current link, client registration
//	d tools      sim tools install (ngspice required, Elmer optional; never prompts)
//	e daemon     restart through the login service (manual update only — the
//	             daemon restarts itself by exiting to its supervisor)
//	f connector  download the .eext to ~/.pcbpilot/connector/, report import steps
//	g verify     versions, MCP handshake, service, sim tools; rollback on failure
//
// Every external effect goes through updateDeps so tests run in a temp HOME
// with a fake release server, a fake service manager and a fake daemon.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/daemon"
	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
	"github.com/zhuangzard/pcbpilot/internal/version"
	"github.com/zhuangzard/pcbpilot/pkg/simtools"
)

// Component names, in apply order.
const (
	compCLI       = "cli"
	compSkill     = "skill"
	compMCP       = "mcp"
	compTools     = "tools"
	compDaemon    = "daemon"
	compConnector = "connector"
)

var allComponents = []string{compCLI, compSkill, compMCP, compTools, compDaemon, compConnector}

// daemonProbe is what the engine needs from a running daemon's /health.
type daemonProbe struct {
	Running  bool
	Version  string
	PID      int
	Port     int
	Windows  []healthWindow
	Activity daemon.Activity
	Raw      json.RawMessage
}

type updateDeps struct {
	goos      string
	home      string // user home (not ~/.pcbpilot)
	binPath   string // the CLI binary a CLI update replaces
	version   string // running CLI version stamp
	runner    func(name string, args ...string) (string, error)
	lookPath  func(string) (string, error)
	simEnv    func() *simtools.Env
	probe     func(ctx context.Context) daemonProbe
	handshake func(ctx context.Context, node, server, bin string) (selfupdate.MCPHandshakeResult, error)
	// stopDaemon stops an identified running daemon (Windows restart path).
	stopDaemon func() error
	sleep      func(time.Duration)
	now        func() time.Time
	uid        int
	logf       func(format string, a ...any)
}

func realUpdateDeps(cfg *appConfig, logw io.Writer) updateDeps {
	home, _ := os.UserHomeDir()
	bin, _ := selfupdate.CurrentBinaryPath()
	return updateDeps{
		goos:     runtime.GOOS,
		home:     home,
		binPath:  bin,
		version:  version.Version,
		runner:   func(n string, a ...string) (string, error) { return daemonServiceRunner(n, a...) },
		lookPath: exec.LookPath,
		simEnv:   func() *simtools.Env { return simToolsEnv() },
		probe:    func(ctx context.Context) daemonProbe { return probeDaemon(ctx, cfg) },
		handshake: func(ctx context.Context, node, server, bin string) (selfupdate.MCPHandshakeResult, error) {
			return selfupdate.MCPHandshake(ctx, node, server, bin)
		},
		stopDaemon: func() error {
			port, _, err := cfg.portRange()
			if err != nil {
				return err
			}
			return stopLocalDaemon(cfg.host, port, io.Discard)
		},
		sleep: time.Sleep,
		now:   time.Now,
		uid:   os.Getuid(),
		logf: func(format string, a ...any) {
			line := fmt.Sprintf(format, a...)
			selfupdate.AppendLog("%s", line)
			if logw != nil {
				fmt.Fprintln(logw, line)
			}
		},
	}
}

// probeDaemon reads the daemon's /health (short timeouts; never fails).
func probeDaemon(ctx context.Context, cfg *appConfig) daemonProbe {
	var p daemonProbe
	if cfg == nil {
		return p
	}
	start, end, err := cfg.portRange()
	if err != nil {
		return p
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	scan := scanHealth(ctx, hostPortOptions{host: cfg.host, portStart: start, portEnd: end})
	if scan.Found == nil {
		return p
	}
	p.Running, p.Port, p.Raw = true, scan.Found.Port, scan.Found.Raw
	var parsed struct {
		Version  string          `json:"version"`
		PID      int             `json:"pid"`
		Windows  []healthWindow  `json:"windows"`
		Activity daemon.Activity `json:"activity"`
	}
	if json.Unmarshal(scan.Found.Raw, &parsed) == nil {
		p.Version, p.PID, p.Windows, p.Activity = parsed.Version, parsed.PID, parsed.Windows, parsed.Activity
	}
	return p
}

// updatePlan is one run's choices.
type updatePlan struct {
	src           selfupdate.AssetSource
	target        string // src.Version()
	components    map[string]bool
	clients       []string
	force         bool
	preserve      bool
	createMissing bool
	daemonMode    bool // the daemon applies itself: tools = ngspice only, no restart step, no prompts
	openFolder    bool
	waitConnector time.Duration
	noRollback    bool
}

func (p updatePlan) has(c string) bool { return p.components[c] }

// stepRow is one line of the step report / final component table.
type stepRow struct {
	Component string `json:"component"`
	Status    string `json:"status"` // updated | installed | current | skipped | failed | linked | behind | downloaded | pending-import | restarted | ok | missing
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Where     string `json:"where,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type verifyRow struct {
	Check    string `json:"check"`
	OK       bool   `json:"ok"`
	Required bool   `json:"required"`
	Detail   string `json:"detail,omitempty"`
}

type applyResult struct {
	Target     string                       `json:"target"`
	From       string                       `json:"from"`
	Steps      []stepRow                    `json:"steps"`
	Verify     []verifyRow                  `json:"verify,omitempty"`
	CLI        *selfupdate.CLIOutcome       `json:"cli,omitempty"`
	Skills     []selfupdate.TargetOutcome   `json:"skills,omitempty"`
	MCP        *selfupdate.MCPOutcome       `json:"mcp,omitempty"`
	Clients    []selfupdate.Registration    `json:"mcpClients,omitempty"`
	Tools      []simtools.InstallOutcome    `json:"tools,omitempty"`
	Connector  *selfupdate.ConnectorOutcome `json:"connectorFile,omitempty"`
	ImportNote []string                     `json:"connectorSteps,omitempty"`
	Snapshot   string                       `json:"snapshot,omitempty"`
	Changed    []string                     `json:"changed,omitempty"`
	Skipped    []string                     `json:"skipped,omitempty"`
	Failed     bool                         `json:"failed"`
	RolledBack []string                     `json:"rolledBack,omitempty"`
	Summary    string                       `json:"summary"`
}

func (r *applyResult) add(s stepRow) {
	r.Steps = append(r.Steps, s)
	switch s.Status {
	case "updated", "installed", "restarted", "downloaded":
		if s.Component != compConnector {
			r.Changed = appendUnique(r.Changed, strings.SplitN(s.Component, ":", 2)[0])
		}
	case "failed":
		r.Failed = true
	case "skipped":
		if s.Detail != "" {
			r.Skipped = append(r.Skipped, s.Component+": "+s.Detail)
		}
	}
}

func appendUnique(xs []string, x string) []string {
	for _, y := range xs {
		if y == x {
			return xs
		}
	}
	return append(xs, x)
}

type updateEngine struct {
	deps updateDeps
}

func (e *updateEngine) log(format string, a ...any) {
	if e.deps.logf != nil {
		e.deps.logf(format, a...)
	}
}

// apply runs the selected steps in order. The snapshot is taken first; a
// failed required step or verify restores it (unless noRollback).
func (e *updateEngine) apply(ctx context.Context, plan updatePlan) applyResult {
	d := e.deps
	res := applyResult{Target: plan.target, From: normVersion(d.version)}
	e.log("update: %s → v%s from %s (components: %s)", orDash(d.version), plan.target, plan.src.Describe(), strings.Join(planList(plan), ","))

	snap, err := selfupdate.TakeSnapshot(normVersion(d.version), d.binPath)
	if err != nil {
		res.add(stepRow{Component: "snapshot", Status: "failed", Detail: err.Error()})
		res.Summary = "update aborted: could not save a rollback snapshot: " + err.Error()
		e.log("%s", res.Summary)
		return res
	}
	res.Snapshot = snap.Dir
	e.log("rollback snapshot: %s", snap.Dir)

	if plan.has(compCLI) {
		e.stepCLI(ctx, plan, &res)
	}
	if plan.has(compSkill) && !res.Failed {
		e.stepSkill(ctx, plan, &res)
	}
	if plan.has(compMCP) && !res.Failed {
		e.stepMCP(ctx, plan, &res)
	}
	if plan.has(compTools) && !res.Failed {
		e.stepTools(plan, &res)
	}
	if plan.has(compConnector) && !res.Failed {
		e.stepConnectorDownload(ctx, plan, &res)
	}
	if plan.has(compDaemon) && !res.Failed && !plan.daemonMode {
		e.stepDaemon(ctx, plan, &res)
	}
	if !res.Failed {
		e.verify(ctx, plan, &res)
	}
	if res.Failed && !plan.noRollback && len(res.Changed) > 0 {
		e.rollback(ctx, plan, snap, &res)
	}
	if plan.has(compConnector) && !plan.daemonMode {
		e.stepConnectorReport(ctx, plan, &res)
	}
	res.Summary = summarize(res)
	e.log("%s", res.Summary)
	return res
}

func planList(p updatePlan) []string {
	var out []string
	for _, c := range allComponents {
		if p.has(c) {
			out = append(out, c)
		}
	}
	return out
}

// ── a. CLI ────────────────────────────────────────────────────────────────

func (e *updateEngine) stepCLI(ctx context.Context, plan updatePlan, res *applyResult) {
	d := e.deps
	logf := func(f string, a ...any) { e.log(f, a...) }
	if plan.src.Local() {
		out := selfupdate.CLIOutcome{Path: d.binPath, From: d.version, To: plan.target}
		if normVersion(d.version) == plan.target && !plan.force {
			out.Status = "up-to-date"
		} else {
			asset, err := selfupdate.AssetName(d.goos, runtime.GOARCH)
			var data []byte
			if err == nil {
				data, err = plan.src.Fetch(ctx, asset, 256<<20)
			}
			if err == nil {
				err = selfupdate.InstallBinaryBytes(ctx, data, d.binPath, plan.target)
			}
			if err != nil {
				out.Status, out.Reason = "error", err.Error()
			} else {
				out.Status, out.Checksum = "updated", "verified"
			}
		}
		res.CLI = &out
	} else {
		out, _ := selfupdate.UpdateCLI(ctx, selfupdate.CLIOptions{
			TargetVersion: plan.target, CurrentVersion: d.version, Path: d.binPath, Force: plan.force,
		}, logf)
		res.CLI = &out
	}
	c := res.CLI
	row := stepRow{Component: compCLI, From: normVersion(c.From), To: plan.target, Where: c.Path, Detail: c.Reason}
	switch c.Status {
	case "updated":
		row.Status = "updated"
	case "up-to-date":
		row.Status = "current"
	case "skipped":
		row.Status = "skipped"
	default:
		row.Status = "failed"
	}
	res.add(row)
}

// ── b. Skill ──────────────────────────────────────────────────────────────

func (e *updateEngine) stepSkill(ctx context.Context, plan updatePlan, res *applyResult) {
	opts := selfupdate.SyncOptions{
		TargetVersion: plan.target,
		Clients:       plan.clients,
		Preserve:      plan.preserve,
		Force:         plan.force,
		CreateMissing: plan.createMissing,
	}
	if plan.src.Local() {
		opts.Assets = plan.src
	}
	sync, err := selfupdate.SyncSkills(ctx, opts, func(f string, a ...any) { e.log(f, a...) })
	res.Skills = sync.Outcomes
	for _, o := range sync.Outcomes {
		row := stepRow{Component: compSkill + ":" + o.Client, From: o.From, To: o.To, Where: o.Dir}
		switch o.Status {
		case "updated", "created", "preserved":
			row.Status = "updated"
			if o.Status == "preserved" {
				row.Detail = "local edits kept (preserve); release parity not claimed"
			}
		case "up-to-date":
			row.Status = "current"
		case "linked":
			row.Status, row.Detail = "linked", "linked to "+o.Linked+" (source install — updated by git pull)"
		case "skipped":
			row.Status, row.Detail = "skipped", "not installed"
			row.To = ""
		default:
			row.Status, row.Detail = "failed", o.Err
		}
		res.add(row)
	}
	if err != nil && !res.Failed {
		res.add(stepRow{Component: compSkill, Status: "failed", Detail: err.Error()})
	}
}

// ── c. MCP ────────────────────────────────────────────────────────────────

func (e *updateEngine) clientEnv() selfupdate.ClientEnv {
	return selfupdate.ClientEnv{Home: e.deps.home, LookPath: e.deps.lookPath, Run: e.deps.runner}
}

func (e *updateEngine) node() selfupdate.NodeInfo {
	return selfupdate.FindNode(e.deps.lookPath, e.deps.runner, e.deps.goos)
}

func (e *updateEngine) mcpEntry(node string) selfupdate.MCPEntry {
	return selfupdate.MCPEntry{Node: node, Server: selfupdate.MCPServerPath(), Bin: e.deps.binPath}
}

func (e *updateEngine) stepMCP(ctx context.Context, plan updatePlan, res *applyResult) {
	node := e.node()
	if !node.OK {
		res.add(stepRow{Component: compMCP, Status: "skipped", Detail: "Node.js not usable — " + node.Hint})
		return
	}
	out, err := selfupdate.InstallMCP(ctx, plan.src, plan.force)
	res.MCP = &out
	row := stepRow{Component: compMCP, From: out.From, To: out.To, Where: out.Dir}
	switch {
	case err != nil:
		row.Status, row.Detail = "failed", err.Error()
		res.add(row)
		return
	case out.Status == "skipped":
		row.Status, row.Detail = "skipped", out.Reason
		res.add(row)
		return
	case out.Status == "up-to-date":
		row.Status = "current"
	default:
		row.Status = "updated"
		if out.Status == "installed" {
			row.Status = "installed"
		}
	}
	res.add(row)
	// Retire upstream easyeda-agent registrations, then (re)register ours.
	bk := filepath.Join(selfupdate.Home(), "upstream-backup", e.deps.now().Format("20060102-150405"))
	if removed, err := selfupdate.CleanUpstreamMCP(e.clientEnv(), bk, false); len(removed) > 0 || err != nil {
		detail := "removed upstream " + strings.Join(removed, ", ") + " (backup " + bk + ")"
		if err != nil {
			detail += "; " + err.Error()
		}
		res.add(stepRow{Component: "mcp:upstream", Status: "updated", Detail: detail})
	}
	regs, err := selfupdate.RegisterMCP(e.clientEnv(), e.mcpEntry(node.Path), false)
	res.Clients = regs
	for _, r := range regs {
		if r.Status == "absent" {
			continue
		}
		st := r.Status
		switch st {
		case "registered", "updated":
			st = "updated"
		case "current":
		default:
			st = "failed"
		}
		res.add(stepRow{Component: "mcp:" + r.Client, Status: st, Where: r.Config, Detail: r.Detail})
	}
	_ = err
}

// ── d. simulation tools ───────────────────────────────────────────────────

func (e *updateEngine) stepTools(plan updatePlan, res *applyResult) {
	env := e.deps.simEnv()
	env.StdinTTY = false // never prompt: sudo steps are printed, not run
	opts := simtools.InstallOptions{Yes: true}
	if plan.daemonMode {
		// Unattended: only the required ngspice. Elmer builds from source on
		// macOS (20-60+ min) — `pcbpilot update` or `sim tools install` does it.
		opts.Only = "ngspice"
	}
	var buf strings.Builder
	outs, err := env.Install(&buf, opts)
	for _, l := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(l) != "" {
			e.log("tools: %s", l)
		}
	}
	res.Tools = outs
	for _, o := range outs {
		row := stepRow{Component: compTools + ":" + o.Tool, From: o.Before, To: o.After, Detail: o.Message}
		switch o.Action {
		case "already-ok":
			row.Status = "current"
		case "installed":
			row.Status = "updated"
		case "manual", "planned":
			row.Status = "skipped"
			if row.Detail == "" {
				row.Detail = "needs manual install"
			}
		default:
			row.Status = "skipped"
			if o.Required {
				row.Detail = "required tool not installed: " + o.Message
			}
		}
		res.add(row)
	}
	if plan.daemonMode {
		res.Skipped = append(res.Skipped, "tools:elmer: optional, not installed unattended (pcbpilot sim tools install --yes)")
	}
	_ = err // a missing ngspice is reported by verify, never a rollback reason
}

// ── e. daemon restart ─────────────────────────────────────────────────────

func (e *updateEngine) stepDaemon(ctx context.Context, plan updatePlan, res *applyResult) {
	p := e.deps.probe(ctx)
	if p.Running && (p.Activity.InFlight > 0 || len(p.Activity.UnsavedWindows) > 0) {
		// Wait briefly for a quiet moment; restarting mid-action loses the
		// pending autosave and fails the action in flight.
		for i := 0; i < 12 && (p.Activity.InFlight > 0 || len(p.Activity.UnsavedWindows) > 0); i++ {
			e.deps.sleep(5 * time.Second)
			p = e.deps.probe(ctx)
		}
		if p.Activity.InFlight > 0 || len(p.Activity.UnsavedWindows) > 0 {
			res.add(stepRow{Component: compDaemon, Status: "skipped", From: normVersion(p.Version), To: plan.target,
				Detail: fmt.Sprintf("busy (in flight: %d, unsaved windows: %v) — save, then run `pcbpilot daemon service install` to restart", p.Activity.InFlight, p.Activity.UnsavedWindows)})
			return
		}
	}
	how, err := restartDaemonService(e.deps)
	row := stepRow{Component: compDaemon, From: normVersion(p.Version), To: plan.target, Detail: how}
	if err != nil {
		row.Status, row.Detail = "failed", how+": "+err.Error()
		res.add(row)
		return
	}
	row.Status = "restarted"
	res.add(row)
}

// ── f. connector ──────────────────────────────────────────────────────────

func (e *updateEngine) stepConnectorDownload(ctx context.Context, plan updatePlan, res *applyResult) {
	out, err := selfupdate.DownloadConnector(ctx, plan.src)
	res.Connector = &out
	if err != nil {
		// Never fatal: the connector is imported by hand anyway.
		res.Skipped = append(res.Skipped, "connector download: "+err.Error())
		e.log("connector download failed: %v", err)
		return
	}
	e.log("connector: %s %s", out.Status, out.Path)
}

// stepConnectorReport compares the running connectors with the target and,
// when behind, prints the three import steps with the downloaded file.
func (e *updateEngine) stepConnectorReport(ctx context.Context, plan updatePlan, res *applyResult) {
	path := ""
	if res.Connector != nil {
		path = res.Connector.Path
	}
	p := e.deps.probe(ctx)
	row := stepRow{Component: compConnector, To: plan.target, Where: path}
	versions := connectorVersions(p.Windows)
	row.From = strings.Join(versions, ",")
	switch {
	case !p.Running:
		row.Status, row.Detail = "pending-import", "daemon not running; import the new connector if EasyEDA shows an older one"
	case len(p.Windows) == 0:
		row.Status, row.Detail = "pending-import", "no EasyEDA window connected — cannot read the loaded connector version"
	case allAt(versions, plan.target):
		row.Status = "current"
	default:
		row.Status, row.Detail = "pending-import", "EasyEDA runs connector "+row.From
	}
	res.add(row)
	if row.Status == "pending-import" {
		res.ImportNote = selfupdate.ConnectorImportSteps(path)
	}
	if plan.openFolder && path != "" {
		opener := map[string]string{"darwin": "open", "windows": "explorer"}[e.deps.goos]
		if opener == "" {
			opener = "xdg-open"
		}
		_, _ = e.deps.runner(opener, filepath.Dir(path))
	}
	if plan.waitConnector > 0 && row.Status == "pending-import" {
		deadline := e.deps.now().Add(plan.waitConnector)
		e.log("waiting up to %s for a connector v%s to connect (import it now)…", plan.waitConnector, plan.target)
		for e.deps.now().Before(deadline) {
			e.deps.sleep(3 * time.Second)
			p = e.deps.probe(ctx)
			if v := connectorVersions(p.Windows); len(v) > 0 && allAt(v, plan.target) {
				res.add(stepRow{Component: compConnector, Status: "current", From: row.From, To: plan.target, Detail: "connector v" + plan.target + " connected"})
				res.ImportNote = nil
				return
			}
		}
		res.add(stepRow{Component: compConnector, Status: "pending-import", To: plan.target, Detail: "timed out waiting for the connector"})
	}
}

func connectorVersions(ws []healthWindow) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range ws {
		v := strings.TrimSpace(w.ConnectorVersion)
		if v == "" {
			v = "?"
		}
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func allAt(versions []string, target string) bool {
	if len(versions) == 0 {
		return false
	}
	for _, v := range versions {
		if normVersion(v) != target {
			return false
		}
	}
	return true
}

// ── g. verify + rollback ──────────────────────────────────────────────────

// verify checks the result; rollback-worthy failures set res.Failed.
func (e *updateEngine) verify(ctx context.Context, plan updatePlan, res *applyResult) {
	add := func(check string, ok, required bool, detail string) {
		res.Verify = append(res.Verify, verifyRow{Check: check, OK: ok, Required: required, Detail: detail})
		if !ok && required {
			res.Failed = true
		}
	}
	if plan.has(compCLI) && res.CLI != nil && res.CLI.Status == "updated" {
		out, err := exec.CommandContext(ctx, e.deps.binPath, "--version").Output()
		got := strings.TrimSpace(string(out))
		add("cli "+e.deps.binPath, err == nil && got == "pcbpilot v"+plan.target, true, got)
	}
	if plan.has(compSkill) {
		for _, o := range res.Skills {
			if o.Status != "updated" && o.Status != "created" && o.Status != "up-to-date" {
				continue
			}
			v := strings.TrimSpace(readText(filepath.Join(o.Dir, ".version")))
			_, err := os.Stat(filepath.Join(o.Dir, "SKILL.md"))
			add("skill:"+o.Client, err == nil && v == plan.target, true, v)
		}
	}
	if plan.has(compMCP) && res.MCP != nil && res.MCP.Status != "skipped" && res.MCP.Status != "error" {
		node := e.node()
		if node.OK {
			hs, err := e.deps.handshake(ctx, node.Path, selfupdate.MCPServerPath(), e.deps.binPath)
			detail := fmt.Sprintf("%d tools, server v%s", len(hs.Tools), hs.ServerVersion)
			ok := err == nil && (hs.ServerVersion == "" || hs.ServerVersion == plan.target)
			if err != nil {
				detail = err.Error()
			}
			add("mcp handshake", ok, true, detail)
		}
		for _, r := range res.Clients {
			if r.Status == "absent" {
				continue
			}
			add("mcp registered:"+r.Client, r.Status != "error", false, r.Detail)
		}
	}
	if plan.has(compDaemon) && !plan.daemonMode {
		restarted := false
		for _, s := range res.Steps {
			if s.Component == compDaemon && s.Status == "restarted" {
				restarted = true
			}
		}
		if restarted {
			var p daemonProbe
			for i := 0; i < 20; i++ {
				p = e.deps.probe(ctx)
				if p.Running && normVersion(p.Version) == plan.target {
					break
				}
				e.deps.sleep(500 * time.Millisecond)
			}
			add("daemon v"+plan.target, p.Running && normVersion(p.Version) == plan.target, true, "running "+orDash(p.Version))
		}
		st := readDaemonServiceStatus(e.deps.goos, e.deps.home)
		add("daemon login service", st.Installed && st.BinaryOK, false, st.Path)
	}
	if plan.has(compTools) {
		rep := e.deps.simEnv().Check()
		for _, t := range rep.Tools {
			add("sim tool "+t.Name, t.Status == simtools.StatusOK || !t.Required, false, t.Status+" "+t.Version)
		}
	}
}

func (e *updateEngine) rollback(ctx context.Context, plan updatePlan, snap selfupdate.Snapshot, res *applyResult) {
	e.log("update failed — rolling back from %s", snap.Dir)
	restored, err := selfupdate.RestoreSnapshot(ctx, snap)
	res.RolledBack = restored
	detail := "restored " + strings.Join(restored, ", ")
	if err != nil {
		detail += "; " + err.Error()
	}
	res.add(stepRow{Component: "rollback", Status: "updated", To: snap.Version, Detail: detail})
	if plan.has(compDaemon) && !plan.daemonMode {
		if how, err := restartDaemonService(e.deps); err != nil {
			e.log("rollback: daemon restart (%s) failed: %v", how, err)
		}
	}
}

// summarize is the one-line result, e.g.
// "pcbpilot 0.6.0 → 0.6.1 updated: cli, skill, mcp; connector 0.6.1 downloaded → import it: <path>".
func summarize(res applyResult) string {
	var b strings.Builder
	switch {
	case len(res.RolledBack) > 0:
		fmt.Fprintf(&b, "pcbpilot update to %s FAILED and was rolled back to %s", res.Target, orDash(res.From))
	case res.Failed:
		fmt.Fprintf(&b, "pcbpilot update to %s FAILED", res.Target)
	case len(res.Changed) == 0:
		fmt.Fprintf(&b, "pcbpilot %s: everything already current", res.Target)
	default:
		fmt.Fprintf(&b, "pcbpilot %s → %s updated: %s", orDash(res.From), res.Target, strings.Join(res.Changed, ", "))
	}
	if c := res.Connector; c != nil && c.Path != "" && len(res.ImportNote) > 0 {
		fmt.Fprintf(&b, "; connector %s %s → import it: %s", c.Version, c.Status, c.Path)
	} else if c != nil && c.Path != "" && res.ImportNote == nil && !res.Failed {
		fmt.Fprintf(&b, "; connector %s at %s", c.Version, c.Path)
	}
	for _, s := range res.Steps {
		if s.Status == "failed" {
			fmt.Fprintf(&b, " [%s: %s]", s.Component, s.Detail)
			break
		}
	}
	return b.String()
}

func readText(p string) string { b, _ := os.ReadFile(p); return string(b) }

// ── daemon restart via the login service ─────────────────────────────────

// restartDaemonService restarts (or installs) the login service so it runs
// deps.binPath. Returns a description of what it did.
//
//	macOS   launchctl kickstart -k gui/<uid>/com.pcbpilot.daemon
//	Linux   systemctl --user restart pcbpilot-daemon.service
//	Windows stop the identified daemon, then start it hidden (HKCU Run has no restart)
//
// A missing service, or one pointing at another binary, is (re)installed.
func restartDaemonService(d updateDeps) (string, error) {
	st := readDaemonServiceStatus(d.goos, d.home)
	stale := !st.Installed || (st.Binary != "" && !samePath(st.Binary, d.binPath))
	switch d.goos {
	case "darwin":
		if stale {
			return "daemon service install (launchd)", installDaemonService(d.goos, d.home, d.binPath, true, io.Discard)
		}
		target := fmt.Sprintf("gui/%d/%s", d.uid, daemonServiceLabel)
		out, err := d.runner("launchctl", "kickstart", "-k", target)
		if err != nil {
			return "launchctl kickstart -k " + target, fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
		}
		return "launchctl kickstart -k " + target, nil
	case "linux":
		if stale {
			if err := installDaemonService(d.goos, d.home, d.binPath, true, io.Discard); err != nil {
				return "daemon service install (systemd --user)", err
			}
		}
		out, err := d.runner("systemctl", "--user", "restart", daemonServiceUnit)
		if err != nil {
			return "systemctl --user restart " + daemonServiceUnit, fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
		}
		return "systemctl --user restart " + daemonServiceUnit, nil
	case "windows":
		if stale {
			if err := installDaemonService(d.goos, d.home, d.binPath, false, io.Discard); err != nil {
				return "daemon service install (HKCU Run)", err
			}
		}
		if d.stopDaemon != nil {
			_ = d.stopDaemon()
		}
		out, err := d.runner("powershell.exe", "-NoProfile", "-Command",
			fmt.Sprintf("Start-Process -WindowStyle Hidden -FilePath '%s' -ArgumentList 'daemon','start'", strings.ReplaceAll(d.binPath, "'", "''")))
		if err != nil {
			return "stop + Start-Process hidden", fmt.Errorf("%v: %s", err, strings.TrimSpace(out))
		}
		return "stop + Start-Process hidden", nil
	}
	return "", fmt.Errorf("no login-service support on %s — restart `pcbpilot daemon start` yourself", d.goos)
}

func samePath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = a
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = b
	}
	return filepath.Clean(ra) == filepath.Clean(rb)
}

// httpGetJSON is a small helper for tests and probes.
func httpGetJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}
