package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
	"github.com/zhuangzard/pcbpilot/internal/version"
)

// newMCPCmd: the MCP server package and its client registrations. `update`
// runs the same code as its mcp step; the installers call `mcp install`.
func newMCPCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	m := &cobra.Command{
		Use:   "mcp",
		Short: "Install / register / check the pcbpilot MCP server (Node.js stdio adapter)",
		Long: `The MCP server ships as the release asset mcp.tar.gz (src + production
node_modules; needs only Node.js >= 20.17). It is installed to
~/.pcbpilot/mcp/<version> with a stable ~/.pcbpilot/mcp/current link, and
registered with every AI client found on this machine:

  claude  ~/.claude.json            (via ` + "`claude mcp add --scope user`" + ` when the CLI exists)
  codex   $CODEX_HOME/config.toml   [mcp_servers.pcbpilot]
  zcode   ~/.zcode/cli/config.json  mcp.servers.pcbpilot
  agents  ~/.agents/mcp.json        (only when the file exists)

Upstream easyeda-agent registrations are removed (backup in
~/.pcbpilot/upstream-backup/). Restart the AI client afterwards.`,
	}
	var (
		ver, localDir     string
		force, noRegister bool
		jsonOut, dryRun   bool
	)
	install := &cobra.Command{
		Use:   "install",
		Short: "Install mcp.tar.gz of a release (default: this CLI's version) and register it",
		Args:  cobra.NoArgs,
		Example: `  pcbpilot mcp install
  pcbpilot mcp install --version 0.6.1
  pcbpilot mcp install --local-dir ./dist`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var src selfupdate.AssetSource
			var err error
			switch {
			case localDir != "":
				if src, err = selfupdate.LocalAssets(localDir); err != nil {
					return err
				}
			default:
				v := selfupdate.SemverCore(ver)
				if v == "" {
					v = selfupdate.SemverCore(version.Version)
				}
				if v == "" || (ver == "" && !selfupdate.IsCleanRelease(version.Version)) {
					return fmt.Errorf("this CLI is a dev build (%s) — pass --version X.Y.Z or --local-dir", version.Version)
				}
				src = selfupdate.ReleaseAssets(v)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			eng := &updateEngine{deps: realUpdateDeps(cfg, nil)}
			res := applyResult{Target: src.Version()}
			plan := updatePlan{src: src, target: src.Version(), force: force, components: map[string]bool{compMCP: true}}
			if noRegister {
				node := eng.node()
				if !node.OK {
					return fmt.Errorf("Node.js not usable — %s", node.Hint)
				}
				out, err := selfupdate.InstallMCP(ctx, src, force)
				if err != nil {
					return err
				}
				fmt.Fprintf(stdout, "mcp %s: %s → %s (%s)\n", out.Status, orDash(out.From), out.To, out.Server)
				return nil
			}
			eng.stepMCP(ctx, plan, &res)
			if !res.Failed && res.MCP != nil && res.MCP.Status != "skipped" {
				eng.verify(ctx, plan, &res)
			}
			if jsonOut {
				emitJSON(stdout, res)
			} else {
				for _, s := range res.Steps {
					tableRow(stdout, s.Component, s.Status, orDash(s.To), s.Where+" "+s.Detail)
				}
				for _, v := range res.Verify {
					fmt.Fprintf(stdout, "  verify %-24s ok=%v %s\n", v.Check, v.OK, v.Detail)
				}
				if !res.Failed {
					fmt.Fprintln(stdout, "restart your AI client so it discovers the pcbpilot MCP server")
				}
			}
			if res.Failed {
				return errQuiet
			}
			return nil
		},
	}
	install.Flags().StringVar(&ver, "version", "", "release version (default: this CLI's version)")
	install.Flags().StringVar(&localDir, "local-dir", "", "install from a local release asset dir")
	install.Flags().BoolVar(&force, "force", false, "re-install even when current")
	install.Flags().BoolVar(&noRegister, "no-register", false, "install files only; do not touch client configs")
	install.Flags().BoolVar(&jsonOut, "json", false, "JSON output")

	status := &cobra.Command{
		Use:   "status",
		Short: "Show the installed MCP version, Node.js and each client's registration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng := &updateEngine{deps: realUpdateDeps(cfg, nil)}
			chk := checkMCP(eng, normVersion(version.Version))
			if jsonOut {
				emitJSON(stdout, chk)
				return nil
			}
			fmt.Fprintf(stdout, "mcp %s (installed %s, server %s)\n", chk.Status, orDash(chk.Installed), chk.Server)
			if chk.Node.OK {
				fmt.Fprintf(stdout, "node %s (%s)\n", chk.Node.Version, chk.Node.Path)
			} else {
				fmt.Fprintf(stdout, "node: %s\n", chk.Node.Hint)
			}
			for _, r := range chk.Clients {
				fmt.Fprintf(stdout, "  %-7s %-8s %s %s\n", r.Client, r.Status, r.Config, r.Detail)
			}
			return nil
		},
	}
	status.Flags().BoolVar(&jsonOut, "json", false, "JSON output")

	register := &cobra.Command{
		Use:   "register",
		Short: "(Re)register ~/.pcbpilot/mcp/current with every present AI client",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			eng := &updateEngine{deps: realUpdateDeps(cfg, nil)}
			node := eng.node()
			if !node.OK {
				return fmt.Errorf("Node.js not usable — %s", node.Hint)
			}
			if _, err := os.Stat(selfupdate.MCPServerPath()); err != nil {
				return fmt.Errorf("MCP server not installed at %s — run `pcbpilot mcp install`", selfupdate.MCPServerPath())
			}
			regs, err := selfupdate.RegisterMCP(eng.clientEnv(), eng.mcpEntry(node.Path), dryRun)
			for _, r := range regs {
				if r.Status != "absent" {
					fmt.Fprintf(stdout, "  %-7s %-14s %s %s\n", r.Client, r.Status, r.Config, r.Detail)
				}
			}
			return err
		},
	}
	register.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change")
	m.AddCommand(install, status, register)
	_ = stderr
	return m
}
