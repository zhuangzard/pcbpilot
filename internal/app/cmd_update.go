package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
	"github.com/zhuangzard/pcbpilot/internal/version"
	"github.com/zhuangzard/pcbpilot/pkg/simtools"
)

// exitCodeUpdatesAvailable is the exit code `update --check --exit-code` uses when
// something is behind, so CI/agents can check it without parsing text
// (0 = everything current and aligned, 1 = check failure).
const exitCodeUpdatesAvailable = 10

// updateReport is the JSON shape of `pcbpilot update` / `pcbpilot update --check`.
type updateReport struct {
	Mode            string                 `json:"mode"` // check | apply | source
	CLIVersion      string                 `json:"cliVersion"`
	Latest          string                 `json:"latest,omitempty"`
	LatestErr       string                 `json:"latestError,omitempty"`
	Target          string                 `json:"target,omitempty"`
	Auto            string                 `json:"auto,omitempty"`
	CLI             *selfupdate.CLIOutcome `json:"cli,omitempty"`
	Skills          []updateSkillRow       `json:"skills,omitempty"`
	MCP             *mcpCheck              `json:"mcp,omitempty"`
	Tools           []simtools.Result      `json:"tools,omitempty"`
	Connector       *connectorReport       `json:"connector,omitempty"`
	Source          *sourceStatus          `json:"source,omitempty"`
	Steps           []stepRow              `json:"steps,omitempty"`
	Verify          []verifyRow            `json:"verify,omitempty"`
	Components      []stepRow              `json:"components,omitempty"` // final version table
	ConnectorSteps  []string               `json:"connectorSteps,omitempty"`
	Summary         string                 `json:"summary,omitempty"`
	Behind          int                    `json:"behind"`          // components behind the target
	Mismatched      int                    `json:"mismatched"`      // components at a different or unknown version
	Unverified      int                    `json:"unverified"`      // live components that could not be checked
	RestartRequired bool                   `json:"restartRequired"` // deprecated compatibility field; always false
	Ready           bool                   `json:"ready"`           // exact CLI/Skill/MCP/daemon/connector
	Notes           []string               `json:"notes,omitempty"`
}

// updateSkillRow is one skill dir's before/after. In check mode only the
// "installed" side is filled in.
type updateSkillRow struct {
	Client    string `json:"client"`
	Dir       string `json:"dir"`
	From      string `json:"from,omitempty"`      // version before this run (apply mode)
	Installed string `json:"installed,omitempty"` // version on disk after this run
	Present   bool   `json:"present"`
	Status    string `json:"status"` // behind | ahead | unknown | current | not-installed | linked | updated | created | preserved | skipped | error
	Linked    string `json:"linked,omitempty"`
	Err       string `json:"err,omitempty"`
}

// mcpCheck is the read-only MCP verdict.
type mcpCheck struct {
	Installed string                    `json:"installed,omitempty"`
	Server    string                    `json:"server"`
	Node      selfupdate.NodeInfo       `json:"node"`
	Clients   []selfupdate.Registration `json:"clients,omitempty"`
	Status    string                    `json:"status"` // current | behind | not-installed | no-node
}

// connectorReport is the read-only connector verdict. The .eext has no
// programmatic in-place update for sideloads; the daemon downloads it and
// refuses design actions until the matching connector is imported.
type connectorReport struct {
	UnknownVersions bool     `json:"unknownVersions,omitempty"`
	DaemonRunning   bool     `json:"daemonRunning"`
	DaemonVersion   string   `json:"daemonVersion,omitempty"`
	DaemonStatus    string   `json:"daemonStatus"` // current | mismatch | unknown | not-running
	DaemonPort      int      `json:"daemonPort,omitempty"`
	Versions        []string `json:"versions,omitempty"` // distinct connector versions across windows
	Windows         int      `json:"windows"`
	Status          string   `json:"status"` // ok | behind | mismatch | unknown | no-daemon | no-window
	File            string   `json:"file,omitempty"`
}

func newUpdateCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	var (
		checkOnly     bool
		exitCode      bool
		pinVersion    string
		cliOnly       bool
		skillOnly     bool
		clients       []string
		preserve      bool
		force         bool
		createMissing bool
		jsonOut       bool
		localDir      string
		localBinary   string
		only, skip    []string
		yes           bool
		auto          string
		rollback      bool
		openFolder    bool
		waitConnector bool
		waitTimeout   time.Duration
		noRollback    bool
		binOverride   string
	)
	c := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade", "self-update"},
		Short:   "Upgrade everything: CLI, Skill, MCP, sim tools, daemon (+ connector download)",
		Long: `Bring this installation up to the latest GitHub release in one step, in order:

  cli        download for this platform, sha256-verify, run-verify, atomic swap
  skill      every present client dir (~/.claude, ~/.codex, ~/.agents, ~/.zcode);
             symlinked dirs (source installs) are left alone
  mcp        mcp.tar.gz → ~/.pcbpilot/mcp/<version> (+ current link), registered
             with Claude Code / Codex / ZCode / ~/.agents (needs Node.js >= 20.17)
  tools      ngspice (required) + Elmer FEM (optional); never prompts for sudo
  daemon     restart through the login service (installed when missing)
  connector  download pcbpilot-connector.eext to ~/.pcbpilot/connector/ and
             print the 3 import steps — importing it is the one manual step
  verify     versions, MCP handshake, service, sim tools; a failed required
             check restores the rollback snapshot (~/.pcbpilot/rollback/)

Automatic mode (default ON): the daemon checks at startup, every 6 h and after
30 min idle, and applies a new release when EasyEDA is idle (no action in
flight, no unsaved edits). ` + "`--auto off`" + ` switches to notify-only.
Source installs (built from a git checkout by scripts/setup-agent.sh) are
updated with ` + "`git pull --ff-only`" + ` + setup-agent.sh; a dirty or diverged checkout is
refused. The daemon never pulls a checkout unless ` + "`--auto source`" + `.`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot update                      # everything → latest
  pcbpilot update --check              # report-only table for every component
  pcbpilot update --check --exit-code  # exit 10 when anything is behind
  pcbpilot update --only cli,skill     # just these components
  pcbpilot update --skip tools,daemon
  pcbpilot update --open --wait-connector   # open the .eext folder, wait for the import
  pcbpilot update --rollback           # restore the previous version
  pcbpilot update --auto status|on|off|source
  pcbpilot update --version 0.6.1      # pin a release
  pcbpilot update --local-dir ./dist   # install from local release assets (no GitHub)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if auto != "" {
				return runAutoSetting(cfg, auto, jsonOut, stdout)
			}
			if rollback {
				return runRollback(cmd.Context(), cfg, pinVersion, jsonOut, stdout, stderr)
			}
			// --local-dir with a release (X.Y.Z) package and --binary: the full
			// engine, replacing the CLI at --binary (a simulated or foreign
			// install); X.Y.Z-dev.N packages keep the exact legacy path.
			if localDir != "" && localBinary != "" && !checkOnly && !localDevPackage(localDir) {
				if !filepath.IsAbs(localBinary) {
					return fmt.Errorf("--binary must be an absolute path")
				}
				binOverride = localBinary
				localBinary = ""
			}
			if localDir != "" && (localBinary != "" || checkOnly) {
				for _, flag := range []string{"version", "cli-only", "skill-only", "client", "preserve", "force", "create-missing"} {
					if cmd.Flags().Changed(flag) {
						return fmt.Errorf("--local-dir cannot be combined with --%s", flag)
					}
				}
				if checkOnly && localBinary != "" {
					return fmt.Errorf("local check verifies the running CLI; --binary is install-only")
				}
				return runLocalUpdate(cfg, localDir, localBinary, checkOnly, exitCode, jsonOut, stdout)
			}
			if localBinary != "" {
				return fmt.Errorf("--binary requires --local-dir")
			}
			if localDir != "" && cmd.Flags().Changed("version") {
				return fmt.Errorf("--local-dir cannot be combined with --version")
			}
			if cliOnly && skillOnly {
				return fmt.Errorf("--cli-only and --skill-only are mutually exclusive")
			}
			comps, err := selectComponents(cliOnly, skillOnly, only, skip)
			if err != nil {
				return err
			}
			clients = normalizeClients(clients)
			if comps[compSkill] || len(clients) > 0 {
				if err := selfupdate.ValidateClients(clients); err != nil {
					return err
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
			defer cancel()
			if !cmd.Flags().Changed("preserve") && selfupdate.PreserveFromEnv() {
				preserve = true
			}
			_ = yes // accepted for scripts; update never prompts (sudo steps are printed instead)

			// Source install: pull + setup instead of release assets.
			if repo, ok := sourceInstall(); ok && localDir == "" && !selfupdate.IsCleanRelease(version.Version) && !force && pinVersion == "" {
				return runSourceMode(ctx, cfg, repo, checkOnly, exitCode, jsonOut, stdout, stderr)
			}

			rep := updateReport{Mode: "apply", CLIVersion: version.Version}
			rep.Auto, _ = selfupdate.AutoMode()
			if checkOnly {
				rep.Mode = "check"
			}

			var src selfupdate.AssetSource
			if localDir != "" {
				src, err = selfupdate.LocalAssets(localDir)
				if err != nil {
					return fmt.Errorf("--local-dir %s: %w", localDir, err)
				}
				rep.Target = src.Version()
			} else {
				target := selfupdate.SemverCore(pinVersion)
				if pinVersion == "" {
					latest, err := selfupdate.LatestReleaseVersion(ctx)
					if err != nil {
						rep.LatestErr = err.Error()
						if jsonOut {
							emitJSON(stdout, rep)
							return errQuiet
						}
						return fmt.Errorf("resolve latest release (offline? pass --version to pin): %w", err)
					}
					rep.Latest = latest
					target = latest
				} else if target == "" {
					return fmt.Errorf("bad --version %q (want x.y.z)", pinVersion)
				}
				rep.Target = target
				src = selfupdate.ReleaseAssets(target)
			}

			if checkOnly {
				buildCheckReport(ctx, cfg, &rep, comps, clients, force)
				if jsonOut {
					emitJSON(stdout, rep)
				} else {
					printCheckReport(stdout, rep)
				}
				if exitCode && !rep.Ready {
					return exitCodeError{code: exitCodeUpdatesAvailable}
				}
				return nil
			}

			release, err := selfupdate.AcquireLock("cli")
			if err != nil {
				return fmt.Errorf("%v — wait for it to finish (see ~/.pcbpilot/update.log)", err)
			}
			defer release()
			logw := stderr
			if jsonOut {
				logw = nil
			}
			eng := &updateEngine{deps: realUpdateDeps(cfg, logw)}
			if binOverride != "" {
				eng.deps.binPath = binOverride
				if out, err := exec.Command(binOverride, "--version").Output(); err == nil {
					eng.deps.version = strings.TrimPrefix(strings.TrimSpace(string(out)), "pcbpilot ")
				}
			}
			plan := updatePlan{
				src: src, target: rep.Target, components: comps, clients: clients,
				force: force, preserve: preserve, createMissing: createMissing,
				openFolder: openFolder, noRollback: noRollback,
			}
			if waitConnector {
				plan.waitConnector = waitTimeout
			}
			res := eng.apply(ctx, plan)
			fillApplyReport(&rep, res)
			st := selfupdate.UpdateState{Phase: selfupdate.PhaseDone, From: res.From, To: res.Target, Components: res.Changed,
				Skipped: res.Skipped, At: time.Now().UTC(), By: "cli", Snapshot: res.Snapshot, Summary: res.Summary}
			if res.Connector != nil {
				st.Connector = res.Connector.Path
			}
			if res.Failed {
				st.Phase, st.Error = selfupdate.PhaseFailed, firstFailure(res)
				if len(res.RolledBack) > 0 {
					st.Phase = selfupdate.PhaseRolledBack
				}
			}
			if len(res.Changed) > 0 || res.Failed {
				_ = selfupdate.WriteJSON(selfupdate.UpdateStatePath(), st)
			}
			if jsonOut {
				emitJSON(stdout, rep)
			} else {
				printApplyReport(stdout, rep)
			}
			if res.Failed {
				return errQuiet
			}
			return nil
		},
	}
	f := c.Flags()
	f.BoolVar(&checkOnly, "check", false, "report every component against the target without changing anything")
	f.StringVar(&localDir, "local-dir", "", "use release assets from this local directory (checksums.txt + assets); never query GitHub")
	f.StringVar(&localBinary, "binary", "", "with --local-dir: the CLI binary to replace (absolute; default: this executable). X.Y.Z-dev.N packages: legacy CLI+Skill install")
	f.BoolVar(&exitCode, "exit-code", false, fmt.Sprintf("with --check: exit %d when anything is behind/misaligned", exitCodeUpdatesAvailable))
	f.StringVar(&pinVersion, "version", "", "pin a release version (default: latest); with --rollback: the snapshot to restore")
	f.BoolVar(&cliOnly, "cli-only", false, "same as --only cli")
	f.BoolVar(&skillOnly, "skill-only", false, "same as --only skill")
	f.StringSliceVar(&only, "only", nil, "components to update: cli,skill,mcp,tools,daemon,connector")
	f.StringSliceVar(&skip, "skip", nil, "components to leave alone: cli,skill,mcp,tools,daemon,connector")
	f.StringSliceVar(&clients, "client", nil, "limit skill sync to clients: claude,codex,agents,zcode (default: all present)")
	f.BoolVar(&preserve, "preserve", false, "skill sync: keep local edits (never overwrite existing files)")
	f.BoolVar(&force, "force", false, "overwrite a dev build / re-install even when already at the target")
	f.BoolVar(&createMissing, "create-missing", false, "install the skill into a client dir that doesn't exist yet")
	f.BoolVar(&jsonOut, "json", false, "emit JSON")
	f.BoolVarP(&yes, "yes", "y", false, "non-interactive (update never prompts; sudo-only steps are printed, not run)")
	f.StringVar(&auto, "auto", "", "automatic updates by the daemon: on (default) | off (notify only) | source (also ff-pull a source checkout) | status")
	f.BoolVar(&rollback, "rollback", false, "restore the previous CLI/Skill/MCP from ~/.pcbpilot/rollback and restart the daemon")
	f.BoolVar(&openFolder, "open", false, "open the folder holding the downloaded connector .eext")
	f.BoolVar(&waitConnector, "wait-connector", false, "after updating, wait until EasyEDA reports the new connector version")
	f.DurationVar(&waitTimeout, "wait-timeout", 10*time.Minute, "how long --wait-connector waits")
	f.BoolVar(&noRollback, "no-rollback", false, "keep a failed update in place instead of restoring the snapshot (debugging)")
	return c
}

// localDevPackage reports whether a local asset dir holds an X.Y.Z-dev.N
// package (legacy exact install path) rather than a release.
func localDevPackage(dir string) bool {
	src, err := selfupdate.LocalAssets(dir)
	return err != nil || selfupdate.IsLocalVersion(src.Version())
}

func selectComponents(cliOnly, skillOnly bool, only, skip []string) (map[string]bool, error) {
	valid := map[string]bool{}
	for _, c := range allComponents {
		valid[c] = true
	}
	norm := func(xs []string) ([]string, error) {
		var out []string
		for _, x := range xs {
			x = strings.ToLower(strings.TrimSpace(x))
			if x == "" {
				continue
			}
			if x == "skills" {
				x = compSkill
			}
			if !valid[x] {
				return nil, fmt.Errorf("unknown component %q (want %s)", x, strings.Join(allComponents, ","))
			}
			out = append(out, x)
		}
		return out, nil
	}
	o, err := norm(only)
	if err != nil {
		return nil, err
	}
	s, err := norm(skip)
	if err != nil {
		return nil, err
	}
	if cliOnly {
		o = append(o, compCLI)
	}
	if skillOnly {
		o = append(o, compSkill)
	}
	comps := map[string]bool{}
	if len(o) == 0 {
		o = allComponents
	}
	for _, c := range o {
		comps[c] = true
	}
	for _, c := range s {
		delete(comps, c)
	}
	if len(comps) == 0 {
		return nil, fmt.Errorf("--only/--skip left nothing to update")
	}
	return comps, nil
}

func fillApplyReport(rep *updateReport, res applyResult) {
	rep.CLI = res.CLI
	for _, o := range res.Skills {
		row := updateSkillRow{Client: o.Client, Dir: o.Dir, From: o.From, Present: o.Status != "skipped", Status: o.Status, Err: o.Err, Linked: o.Linked}
		switch o.Status {
		case "updated", "created", "up-to-date":
			row.Installed = o.To
		default:
			row.Installed = o.From
		}
		rep.Skills = append(rep.Skills, row)
	}
	rep.Steps = res.Steps
	rep.Verify = res.Verify
	rep.ConnectorSteps = res.ImportNote
	rep.Summary = res.Summary
	rep.Components = finalTable(res)
	for _, v := range res.Verify {
		if !v.OK && !v.Required {
			rep.Notes = append(rep.Notes, "warning: "+v.Check+" — "+v.Detail)
		}
	}
	for _, s := range res.Skipped {
		rep.Notes = append(rep.Notes, "skipped "+s)
	}
	for _, s := range res.Steps {
		if strings.HasPrefix(s.Component, "tools:") && s.Status == "skipped" && s.Detail != "" {
			rep.Notes = append(rep.Notes, s.Component+": "+s.Detail)
		}
	}
	if hasString(res.Changed, compMCP) || hasString(res.Changed, compSkill) {
		rep.Notes = append(rep.Notes, "restart your AI client (Claude Code / Codex / ZCode) so it loads the new Skill and MCP server")
	}
}

// finalTable keeps the last row per component (skill:<client> rows stay separate).
func finalTable(res applyResult) []stepRow {
	idx := map[string]int{}
	var out []stepRow
	for _, s := range res.Steps {
		if strings.HasPrefix(s.Component, "mcp:") || s.Component == "snapshot" {
			continue
		}
		if i, ok := idx[s.Component]; ok {
			out[i] = s
			continue
		}
		idx[s.Component] = len(out)
		out = append(out, s)
	}
	return out
}

// ── check mode ────────────────────────────────────────────────────────────

func buildCheckReport(ctx context.Context, cfg *appConfig, rep *updateReport, comps map[string]bool, clients []string, force bool) {
	target := rep.Target
	if comps[compCLI] {
		rep.CLI = checkCLI(target, force)
	}
	if comps[compSkill] {
		rep.Skills = checkSkills(target, clients)
	}
	eng := &updateEngine{deps: realUpdateDeps(cfg, nil)}
	if comps[compMCP] {
		rep.MCP = checkMCP(eng, target)
	}
	if comps[compTools] {
		rep.Tools = simToolsEnv().Check().Tools
	}
	if comps[compDaemon] || comps[compConnector] {
		rep.Connector = probeConnector(cfg, target)
		if _, err := os.Stat(selfupdate.ConnectorPath(target)); err == nil {
			rep.Connector.File = selfupdate.ConnectorPath(target)
		}
		if rep.Connector.Status == "behind" || rep.Connector.Status == "mismatch" {
			file := rep.Connector.File
			if file == "" {
				file = selfupdate.ReleaseAssetURL(target, selfupdate.ConnectorAsset) + " (`pcbpilot update` downloads it to ~/.pcbpilot/connector/)"
			}
			rep.ConnectorSteps = selfupdate.ConnectorImportSteps(file)
		}
	}
	rep.Behind = countBehind(*rep)
	rep.Mismatched, rep.Unverified = countVersionGateProblems(*rep)
	rep.Ready = rep.Behind == 0 && rep.Mismatched == 0 && rep.Unverified == 0
	rep.Notes = updateNotes(*rep)
}

func checkMCP(eng *updateEngine, target string) *mcpCheck {
	node := eng.node()
	m := &mcpCheck{Installed: selfupdate.MCPInstalledVersion(), Server: selfupdate.MCPServerPath(), Node: node}
	want := selfupdate.MCPEntry{}
	if node.OK {
		want = eng.mcpEntry(node.Path)
	}
	for _, r := range selfupdate.MCPRegistrations(eng.clientEnv(), want) {
		if r.Status != "absent" {
			m.Clients = append(m.Clients, r)
		}
	}
	switch {
	case !node.OK:
		m.Status = "no-node"
	case m.Installed == "":
		m.Status = "not-installed"
	case m.Installed != target:
		m.Status = "behind"
	default:
		m.Status = "current"
		for _, r := range m.Clients {
			if r.Status != "current" {
				m.Status = "behind" // installed but a client is not registered / stale
			}
		}
	}
	return m
}

// checkCLI is the read-only half of the CLI update: same verdicts as
// selfupdate.UpdateCLI, without downloading anything.
func checkCLI(target string, force bool) *selfupdate.CLIOutcome {
	out := &selfupdate.CLIOutcome{From: version.Version, To: target}
	if p, err := selfupdate.CurrentBinaryPath(); err == nil {
		out.Path = p
	}
	switch {
	case !selfupdate.IsCleanRelease(version.Version) && !force:
		out.Status = "skipped"
		out.Reason = fmt.Sprintf("dev build (%s) — --force to install v%s anyway", version.Version, target)
	case selfupdate.SemverLess(version.Version, target):
		out.Status = "behind"
	case selfupdate.SemverCore(version.Version) == target:
		out.Status = "up-to-date"
	default:
		out.Status = "ahead" // running a newer build than the pinned target
	}
	return out
}

// checkSkills reports each skill dir's version against the target.
func checkSkills(target string, clients []string) []updateSkillRow {
	want := map[string]bool{}
	for _, c := range clients {
		want[c] = true
	}
	var rows []updateSkillRow
	for _, t := range selfupdate.Targets(false) {
		if len(want) > 0 && !want[t.Client] {
			continue
		}
		row := updateSkillRow{Client: t.Client, Dir: t.Dir, Installed: t.Installed, Present: t.Present, Linked: t.Linked}
		switch {
		case !t.Present:
			row.Status = "not-installed"
		case t.Linked != "":
			row.Status = "linked"
		case selfupdate.SemverCore(t.Installed) == "":
			row.Status = "unknown"
		case selfupdate.SemverLess(t.Installed, target):
			row.Status = "behind"
		case selfupdate.SemverCore(t.Installed) != target:
			row.Status = "ahead"
		default:
			row.Status = "current"
		}
		rows = append(rows, row)
	}
	return rows
}

// probeConnector reads the live daemon's /health to report the connector version
// in each open EasyEDA window. Purely informational: never fails the command,
// and a missing daemon is a normal answer, not an error. Since 2026-09-28 the
// connector must equal the release exactly (the daemon enforces it).
func probeConnector(cfg *appConfig, target string) *connectorReport {
	rep := &connectorReport{Status: "no-daemon", DaemonStatus: "not-running"}
	portStart, portEnd, err := cfg.portRange()
	if err != nil {
		return rep
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	scan := scanHealth(ctx, hostPortOptions{host: cfg.host, portStart: portStart, portEnd: portEnd})
	if scan.Found == nil {
		return rep
	}
	rep.DaemonRunning = true
	rep.DaemonPort = scan.Found.Port

	var parsed struct {
		Version string         `json:"version"`
		Windows []healthWindow `json:"windows"`
	}
	if err := json.Unmarshal(scan.Found.Raw, &parsed); err != nil {
		rep.DaemonStatus = "unknown"
		rep.Status = "unknown"
		return rep
	}
	rep.DaemonVersion = parsed.Version
	switch core := selfupdate.SemverCore(parsed.Version); {
	case core == "":
		rep.DaemonStatus = "unknown"
	case core == target:
		rep.DaemonStatus = "current"
	default:
		rep.DaemonStatus = "mismatch"
	}
	rep.Windows = len(parsed.Windows)
	if len(parsed.Windows) == 0 {
		rep.Status = "no-window"
		return rep
	}

	seen := map[string]bool{}
	behind, mismatch, unknown := false, false, false
	for _, w := range parsed.Windows {
		v := strings.TrimSpace(w.ConnectorVersion)
		if v == "" {
			rep.UnknownVersions = true
			unknown = true
			continue
		}
		if !seen[v] {
			seen[v] = true
			rep.Versions = append(rep.Versions, v)
		}
		core := selfupdate.SemverCore(v)
		switch {
		case core == "":
			unknown = true
		case core == target:
			// Exact connector release.
		case selfupdate.SemverLess(v, target):
			behind = true
		default:
			mismatch = true
		}
	}
	sort.Strings(rep.Versions)
	switch {
	case behind:
		rep.Status = "behind"
	case mismatch:
		rep.Status = "mismatch"
	case unknown:
		rep.Status = "unknown"
	default:
		rep.Status = "ok"
	}
	return rep
}

// countBehind preserves the directional part of the explicit update report.
func countBehind(rep updateReport) int {
	n := 0
	if rep.CLI != nil {
		switch rep.CLI.Status {
		case "behind", "error":
			n++
		case "skipped":
			// dev build: intentional, not "behind".
		}
	}
	for _, s := range rep.Skills {
		if s.Status == "behind" || s.Status == "error" || (s.Status == "preserved" && s.Installed != rep.Target) {
			n++
		}
	}
	if rep.MCP != nil && (rep.MCP.Status == "behind" || rep.MCP.Status == "not-installed") {
		n++
	}
	for _, t := range rep.Tools {
		if t.Required && t.Status != simtools.StatusOK {
			n++
		}
	}
	if rep.Connector != nil && rep.Connector.Status == "behind" {
		n++
	}
	return n
}

// countVersionGateProblems supports explicit `update --check --exit-code`
// reconciliation: CLI, Skill, MCP, daemon and connector are compared with the
// selected release.
func countVersionGateProblems(rep updateReport) (mismatched, unverified int) {
	if rep.CLI != nil && (rep.CLI.Status == "ahead" || rep.CLI.Status == "skipped") {
		mismatched++
	}
	for _, s := range rep.Skills {
		if s.Present && (s.Status == "ahead" || s.Status == "unknown") {
			mismatched++
		}
	}
	if c := rep.Connector; c != nil {
		switch c.DaemonStatus {
		case "mismatch":
			mismatched++
		case "unknown", "not-running", "":
			unverified++
		}
		switch c.Status {
		case "mismatch":
			mismatched++
		case "unknown", "no-daemon", "no-window":
			unverified++
		}
	}
	return mismatched, unverified
}

// updateNotes turns the report into the handful of actionable lines a user needs.
func updateNotes(rep updateReport) []string {
	var notes []string
	if rep.Connector != nil && rep.Connector.DaemonStatus == "mismatch" {
		notes = append(notes, "daemon is running a DIFFERENT binary — `pcbpilot update` restarts it through the login service "+
			"(or: pcbpilot daemon service install)")
	}
	if rep.Connector != nil && (rep.Connector.Status == "behind" || rep.Connector.Status == "mismatch") {
		where := rep.Connector.File
		if where == "" {
			where = selfupdate.ReleaseAssetURL(rep.Target, selfupdate.ConnectorAsset)
		}
		notes = append(notes, fmt.Sprintf(
			"connector %s ≠ v%s — the daemon pauses design actions until you re-import the .eext (%s); `pcbpilot update` downloads it and prints the steps",
			strings.Join(rep.Connector.Versions, ","), rep.Target, where))
	}
	if rep.MCP != nil {
		switch rep.MCP.Status {
		case "no-node":
			notes = append(notes, "MCP server needs Node.js — "+rep.MCP.Node.Hint)
		case "not-installed", "behind":
			notes = append(notes, "MCP server "+orDash(rep.MCP.Installed)+" → v"+rep.Target+" — `pcbpilot update --only mcp`")
		}
	}
	for _, t := range rep.Tools {
		if t.Required && t.Status != simtools.StatusOK {
			notes = append(notes, t.Name+" is "+t.Status+" — `pcbpilot update --only tools` (or pcbpilot sim tools install --yes)")
		}
	}
	for _, s := range rep.Skills {
		if s.Status == "preserved" {
			notes = append(notes, fmt.Sprintf("skill %s kept local content and its previous version marker; release parity is not claimed", s.Client))
		}
		if s.Status == "not-installed" {
			notes = append(notes, fmt.Sprintf("skill not installed for %s — `pcbpilot update --create-missing` to add %s", s.Client, s.Dir))
		}
		if s.Status == "skipped" && s.Err != "" {
			notes = append(notes, fmt.Sprintf("skill %s skipped (%s) — `pcbpilot update --create-missing` to install it", s.Client, s.Err))
		}
	}
	return notes
}

// ── printing ──────────────────────────────────────────────────────────────

func tableRow(w io.Writer, name, status, ver, where string) {
	fmt.Fprintf(w, "  %-14s %-15s %-22s %s\n", name, status, ver, where)
}

func printCheckReport(w io.Writer, rep updateReport) {
	label := "latest"
	if rep.Latest == "" {
		label = "target"
	}
	fmt.Fprintf(w, "pcbpilot %s  →  %s v%s   (auto-update: %s)\n\n", rep.CLIVersion, label, rep.Target, orDash(rep.Auto))
	if c := rep.CLI; c != nil {
		ver := trimV(c.From)
		if c.Status == "behind" {
			ver = fmt.Sprintf("%s → %s", trimV(c.From), c.To)
		}
		tableRow(w, "cli", c.Status, ver, c.Path)
		if c.Reason != "" {
			fmt.Fprintf(w, "  %-14s %s\n", "", c.Reason)
		}
	}
	for _, s := range rep.Skills {
		ver := orDash(s.Installed)
		if s.Status == "behind" {
			ver = fmt.Sprintf("%s → %s", orDash(s.Installed), rep.Target)
		}
		where := s.Dir
		if s.Linked != "" {
			where += "  (linked to " + s.Linked + ")"
		}
		tableRow(w, "skill:"+s.Client, s.Status, ver, where)
	}
	if m := rep.MCP; m != nil {
		ver := orDash(m.Installed)
		if m.Status == "behind" || m.Status == "not-installed" {
			ver = fmt.Sprintf("%s → %s", orDash(m.Installed), rep.Target)
		}
		where := m.Server
		if !m.Node.OK {
			where = m.Node.Hint
		}
		tableRow(w, "mcp", m.Status, ver, where)
		for _, r := range m.Clients {
			tableRow(w, "  mcp:"+r.Client, r.Status, "", r.Config)
		}
	}
	for _, t := range rep.Tools {
		kind := "optional"
		if t.Required {
			kind = "required"
		}
		tableRow(w, "tools:"+t.Name, t.Status, orDash(t.Version), kind)
	}
	if c := rep.Connector; c != nil {
		tableRow(w, "daemon", c.DaemonStatus, trimV(c.DaemonVersion), fmt.Sprintf("port :%d", c.DaemonPort))
		ver, where := strings.Join(c.Versions, ","), ""
		switch c.Status {
		case "no-daemon":
			where = "daemon not running — start it to read the connector version"
		case "no-window":
			where = "no EasyEDA window connected"
		default:
			where = fmt.Sprintf("%d window(s)", c.Windows)
		}
		if c.File != "" {
			where += "  file: " + c.File
		}
		tableRow(w, "connector", c.Status, orDash(ver), where)
	}
	fmt.Fprintln(w)
	if rep.Ready {
		fmt.Fprintf(w, "→ READY: every component is exactly v%s\n", rep.Target)
	} else {
		fmt.Fprintf(w, "→ NOT READY: behind=%d mismatched=%d unverified=%d (run `pcbpilot update`)\n", rep.Behind, rep.Mismatched, rep.Unverified)
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(w, "  ! %s\n", n)
	}
	printConnectorSteps(w, rep.ConnectorSteps)
}

func printApplyReport(w io.Writer, rep updateReport) {
	fmt.Fprintf(w, "\npcbpilot update → v%s\n\n", rep.Target)
	for _, s := range rep.Components {
		ver := orDash(s.To)
		if s.From != "" && s.From != s.To {
			ver = fmt.Sprintf("%s → %s", s.From, orDash(s.To))
		}
		mark := map[string]string{"updated": "✓", "installed": "✓", "restarted": "✓", "current": "✓", "failed": "✘", "pending-import": "!", "skipped": "-", "linked": "↪"}[s.Status]
		if mark == "" {
			mark = "·"
		}
		where := s.Where
		if s.Detail != "" {
			if where != "" {
				where += "  "
			}
			where += s.Detail
		}
		tableRow(w, mark+" "+s.Component, s.Status, ver, where)
	}
	if len(rep.Verify) > 0 {
		fmt.Fprintln(w, "\n  verify:")
		for _, v := range rep.Verify {
			mark := "ok  "
			if !v.OK {
				mark = "FAIL"
				if !v.Required {
					mark = "WARN"
				}
			}
			fmt.Fprintf(w, "    %s %s  %s\n", mark, v.Check, v.Detail)
		}
	}
	fmt.Fprintf(w, "\n→ %s\n", rep.Summary)
	for _, n := range rep.Notes {
		fmt.Fprintf(w, "  ! %s\n", n)
	}
	printConnectorSteps(w, rep.ConnectorSteps)
}

func printConnectorSteps(w io.Writer, steps []string) {
	if len(steps) == 0 {
		return
	}
	fmt.Fprintln(w, "\n  Connector — the one manual step (EasyEDA has no extension API; GUI automation is not allowed):")
	for i, s := range steps {
		fmt.Fprintf(w, "    %d. %s\n", i+1, s)
	}
	fmt.Fprintln(w, "    Check: pcbpilot health  (design actions unblock automatically once the new connector connects)")
}

// trimV normalizes a version stamp for the table (the daemon and the CLI
// disagree on whether they carry the leading v).
func trimV(v string) string { return orDash(strings.TrimPrefix(v, "v")) }

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func emitJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ── --auto, --rollback, source mode ───────────────────────────────────────

func runAutoSetting(cfg *appConfig, mode string, jsonOut bool, stdout io.Writer) error {
	if mode != "status" {
		if err := selfupdate.SetAutoMode(mode); err != nil {
			return err
		}
		selfupdate.AppendLog("auto-update set to %s", mode)
	}
	eff, from := selfupdate.AutoMode()
	out := map[string]any{"auto": eff, "source": from, "config": selfupdate.ConfigPath()}
	p := probeDaemon(context.Background(), cfg)
	if u := parseHealthUpdates(p.Raw); u != nil {
		out["daemon"] = u
	}
	if jsonOut {
		emitJSON(stdout, out)
		return nil
	}
	fmt.Fprintf(stdout, "auto-update: %s (%s)\n", eff, from)
	switch eff {
	case selfupdate.AutoOn:
		fmt.Fprintln(stdout, "  the daemon checks at startup, every 6 h and after 30 min idle, and applies a release when EasyEDA is idle")
	case selfupdate.AutoOff:
		fmt.Fprintln(stdout, "  the daemon only checks and notifies; run `pcbpilot update` yourself")
	case selfupdate.AutoSource:
		fmt.Fprintln(stdout, "  releases apply automatically; a source checkout is fast-forwarded when clean (never when dirty or diverged)")
	}
	if u := parseHealthUpdates(p.Raw); u != nil {
		fmt.Fprintf(stdout, "  daemon: v%s, state %s, latest %s\n", u.Current, u.State, orDash(u.Latest))
	} else if !p.Running {
		fmt.Fprintln(stdout, "  daemon not running — updates resume when it runs (pcbpilot daemon service install)")
	}
	return nil
}

func runRollback(ctx context.Context, cfg *appConfig, ver string, jsonOut bool, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	snap, err := selfupdate.LatestSnapshot(ver)
	if err != nil {
		return err
	}
	restored, rerr := selfupdate.RestoreSnapshot(ctx, snap)
	deps := realUpdateDeps(cfg, nil)
	how, derr := restartDaemonService(deps)
	st := selfupdate.UpdateState{Phase: selfupdate.PhaseRolledBack, To: snap.Version, At: time.Now().UTC(), By: "cli",
		Summary: fmt.Sprintf("rolled back to %s (%s)", snap.Version, strings.Join(restored, ", "))}
	_ = selfupdate.WriteJSON(selfupdate.UpdateStatePath(), st)
	selfupdate.AppendLog("%s", st.Summary)
	out := map[string]any{"snapshot": snap.Dir, "version": snap.Version, "restored": restored, "daemon": how}
	if rerr != nil {
		out["error"] = rerr.Error()
	}
	if derr != nil {
		out["daemonError"] = derr.Error()
	}
	if jsonOut {
		emitJSON(stdout, out)
	} else {
		fmt.Fprintf(stdout, "rolled back to v%s from %s: %s\n", snap.Version, snap.Dir, strings.Join(restored, ", "))
		if derr != nil {
			fmt.Fprintf(stdout, "  ! daemon restart (%s) failed: %v — run `pcbpilot daemon service install`\n", how, derr)
		} else {
			fmt.Fprintf(stdout, "  daemon restarted (%s)\n", how)
		}
		fmt.Fprintln(stdout, "  auto-update stays as configured; `pcbpilot update --auto off` keeps this version")
	}
	if rerr != nil {
		return errQuiet
	}
	return nil
}

func runSourceMode(ctx context.Context, cfg *appConfig, repo string, check, exitCode, jsonOut bool, stdout, stderr io.Writer) error {
	if check {
		st := inspectSource(ctx, repo, true)
		rep := updateReport{Mode: "source", CLIVersion: version.Version, Source: &st}
		rep.Auto, _ = selfupdate.AutoMode()
		rep.Ready = st.Error == "" && st.Behind == 0
		if jsonOut {
			emitJSON(stdout, rep)
		} else {
			fmt.Fprintf(stdout, "source install %s (CLI %s, auto-update %s)\n", repo, version.Version, rep.Auto)
			switch {
			case st.Dirty:
				fmt.Fprintf(stdout, "  checkout has %s\n  `pcbpilot update` refuses to pull until it is clean\n", st.Error)
			case st.Error != "":
				fmt.Fprintf(stdout, "  %s\n", st.Error)
			case st.Behind > 0 && st.Ahead > 0:
				fmt.Fprintf(stdout, "  diverged from %s (%d local / %d upstream) — rebase or merge yourself\n", st.Upstream, st.Ahead, st.Behind)
			case st.Behind > 0:
				fmt.Fprintf(stdout, "  %d commit(s) behind %s — `pcbpilot update` pulls (ff-only) and re-runs scripts/setup-agent.sh\n", st.Behind, st.Upstream)
			default:
				fmt.Fprintf(stdout, "  up to date with %s\n", st.Upstream)
			}
		}
		if exitCode && !rep.Ready {
			return exitCodeError{code: exitCodeUpdatesAvailable}
		}
		return nil
	}
	w := stderr
	if jsonOut {
		w = io.Discard
	}
	rows, err := runSourceUpdate(ctx, repo, false, false, w)
	rep := updateReport{Mode: "source", CLIVersion: version.Version, Steps: rows}
	if jsonOut {
		emitJSON(stdout, rep)
	} else {
		for _, r := range rows {
			tableRow(stdout, r.Component, r.Status, "", r.Where)
			if r.Detail != "" {
				fmt.Fprintf(stdout, "    %s\n", r.Detail)
			}
		}
	}
	if errors.Is(err, errSourceRefused) {
		return errQuiet
	}
	if err != nil {
		return errQuiet
	}
	return nil
}
