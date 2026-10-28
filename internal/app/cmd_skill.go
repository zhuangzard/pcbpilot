package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
	"github.com/zhuangzard/pcbpilot/internal/version"
)

// newSkillCmd returns the "skill" subcommand group — inspect and update the
// locally-installed pcbpilot skill dirs (~/.claude/skills/pcbpilot,
// ~/.codex/skills/pcbpilot and ~/.agents/skills/pcbpilot). The daemon
// runs `skill sync` automatically on startup (daemon start --auto-update-skill,
// on by default); these commands are
// the manual, self-describing surface for the same machinery.
//
// Scope note: this only manages the SKILL. The EasyEDA connector .eext has no
// official in-place auto-update (marketplace-only), so a stale connector is
// detected + logged by the daemon, and re-imported by hand — see `docs/quick-start.md`.
func newSkillCmd(stdout, stderr io.Writer) *cobra.Command {
	s := &cobra.Command{
		Use:   "skill",
		Short: "Inspect and update the locally-installed pcbpilot skill dirs",
		Long: "Inspect and update the pcbpilot skills installed for your AI clients: the router\n" +
			"`pcbpilot` plus every focused public Skill of the release, one dir each.\n\n" +
			"  pcbpilot skill status                 show installed skill dirs + versions vs latest release\n" +
			"  pcbpilot skill sync                   update present skill dirs to the latest release\n" +
			"  pcbpilot skill sync --version 0.9.0   pin a specific version\n\n" +
			"The daemon syncs installed Skill dirs to its own version on startup; development builds skip writes.\n" +
			"CODEX_HOME / CLAUDE_CONFIG_DIR override the default client config roots.\n" +
			"To update the CLI binary as well, use `pcbpilot update` (skill sync included).\n" +
			"The connector .eext is NOT covered here (no sideload auto-update) — the daemon\n" +
			"logs a re-import notice when it detects a stale connector.",
	}
	s.AddCommand(
		newSkillStatusCmd(stdout, stderr),
		newSkillSyncCmd(stdout, stderr),
	)
	return s
}

// skillStatusReport is the JSON shape of `skill status`.
type skillStatusReport struct {
	CLIVersion string                   `json:"cliVersion"`
	Latest     string                   `json:"latest,omitempty"`
	LatestErr  string                   `json:"latestError,omitempty"`
	Skills     []string                 `json:"skills"` // public Skills this CLI ships
	Targets    []selfupdate.SkillTarget `json:"targets"`
	// Aligned: every public Skill of every client that has the router is
	// present and at the CLI's version (linked source Skills count as their
	// checkout's metadata.version). Only meaningful for a release CLI.
	Aligned bool `json:"aligned"`
}

// skillsAligned is the `skill status` verdict: false when any public Skill of
// a client that uses pcbpilot is missing, unknown or at another version.
func skillsAligned(targets []selfupdate.SkillTarget, cli string) bool {
	want := selfupdate.SemverCore(cli)
	if want == "" || !selfupdate.IsCleanRelease(cli) {
		return false
	}
	found := false
	for _, t := range targets {
		if !selfupdate.ClientInstalled(t.Client) {
			continue
		}
		found = true
		if !t.Present || t.Installed != want {
			return false
		}
	}
	return found
}

func newSkillStatusCmd(stdout, stderr io.Writer) *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show installed skill dirs, their versions, and the latest release",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := selfupdate.ValidateClients(nil); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()

			rep := skillStatusReport{
				CLIVersion: version.Version,
				Skills:     selfupdate.PublicSkills().Skills,
				Targets:    selfupdate.Targets(false),
			}
			rep.Aligned = skillsAligned(rep.Targets, version.Version)
			if latest, err := selfupdate.LatestReleaseVersion(ctx); err != nil {
				rep.LatestErr = err.Error()
			} else {
				rep.Latest = latest
			}

			if jsonOut {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}

			fmt.Fprintf(stdout, "CLI:    %s\n", version.Version)
			if rep.Latest != "" {
				fmt.Fprintf(stdout, "Latest: v%s", rep.Latest)
				if selfupdate.IsCleanRelease(version.Version) && selfupdate.SemverLess(version.Version, rep.Latest) {
					fmt.Fprint(stdout, "  (CLI behind — run `pcbpilot update`)")
				}
				fmt.Fprintln(stdout)
			} else {
				fmt.Fprintf(stdout, "Latest: (unavailable: %s)\n", rep.LatestErr)
			}
			fmt.Fprintln(stdout, "Skill dirs:")
			for _, t := range rep.Targets {
				state := t.Installed
				switch {
				case !t.Present && t.Skill != selfupdate.SkillName && selfupdate.ClientInstalled(t.Client):
					state = "missing" // the next sync adds it next to the router
				case !t.Present:
					state = "not installed"
				case t.Linked == "" && t.Skill != selfupdate.SkillName && !selfupdate.OwnedSkillDir(t.Dir):
					state = "conflict" // same-named dir pcbpilot did not install; sync leaves it
				case state == "":
					state = "unknown"
				}
				marker := "  "
				if t.Present && rep.Latest != "" && selfupdate.SemverLess(t.Installed, rep.Latest) {
					marker = "↑ " // behind latest
				}
				fmt.Fprintf(stdout, "  %s%-7s %-24s %-14s %s\n", marker, t.Client, t.Skill, state, t.Dir)
			}
			if !rep.Aligned {
				fmt.Fprintf(stdout, "Not aligned with CLI %s — run `pcbpilot skill sync --version %s` (or `pcbpilot update`)\n",
					version.Version, selfupdate.SemverCore(version.Version))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return c
}

func newSkillSyncCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		pinVersion    string
		clients       []string
		preserve      bool
		force         bool
		createMissing bool
		adopt         bool
		jsonOut       bool
	)
	c := &cobra.Command{
		Use:   "sync",
		Short: "Update installed skill dirs to the latest release (or a pinned --version)",
		Long: "Download the release skill bundle and materialize it into your client skill dirs.\n\n" +
			"By default only dirs that already exist are updated, to the LATEST release.\n" +
			"Use --create-missing to install into a client dir that doesn't exist yet,\n" +
			"--preserve to keep local edits and the previous version marker (mixed content), and\n" +
			"--version to pin a specific release instead of latest.",
		Args: cobra.NoArgs,
		Example: `  pcbpilot skill sync
  pcbpilot skill sync --version 0.9.0
  pcbpilot skill sync --client claude --preserve
  pcbpilot skill sync --create-missing`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := selfupdate.ValidateClients(normalizeClients(clients)); err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			target := selfupdate.SemverCore(pinVersion)
			if pinVersion == "" {
				latest, err := selfupdate.LatestReleaseVersion(ctx)
				if err != nil {
					return fmt.Errorf("resolve latest release (pass --version to pin): %w", err)
				}
				target = latest
			} else if target == "" {
				return fmt.Errorf("bad --version %q (want x.y.z)", pinVersion)
			}

			if !cmd.Flags().Changed("preserve") && selfupdate.PreserveFromEnv() {
				preserve = true
			}

			logf := func(format string, a ...any) {
				fmt.Fprintf(stderr, format+"\n", a...)
			}
			res, err := selfupdate.SyncSkills(ctx, selfupdate.SyncOptions{
				TargetVersion: target,
				Clients:       normalizeClients(clients),
				Preserve:      preserve,
				Force:         force,
				CreateMissing: createMissing,
				Adopt:         adopt,
			}, logf)

			if jsonOut {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				_ = enc.Encode(res)
			} else {
				for _, o := range res.Outcomes {
					line := fmt.Sprintf("%-7s %-24s %-11s %s", o.Client, o.Skill, o.Status, o.Dir)
					if o.Err != "" {
						line += "  (" + o.Err + ")"
					}
					fmt.Fprintln(stdout, line)
				}
				fmt.Fprintf(stdout, "→ %d dir(s) changed; requested v%s (preserved directories keep their previous marker)\n", res.Changed, res.Target)
			}
			return err
		},
	}
	c.Flags().StringVar(&pinVersion, "version", "", "pin a release version (default: latest)")
	c.Flags().StringSliceVar(&clients, "client", nil, "limit to clients: claude,codex,agents (default: all present)")
	c.Flags().BoolVar(&preserve, "preserve", false, "keep local edits (never overwrite existing files)")
	c.Flags().BoolVar(&force, "force", false, "sync even if the dir is already at the target version")
	c.Flags().BoolVar(&createMissing, "create-missing", false, "install into a client dir that doesn't exist yet")
	c.Flags().BoolVar(&adopt, "adopt", false, "let a focused Skill replace a same-named dir pcbpilot did not install")
	c.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return c
}

// normalizeClients is a defensive trim for user-passed --client values.
func normalizeClients(in []string) []string {
	var out []string
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}
