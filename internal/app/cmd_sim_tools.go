package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/pkg/simtools"
)

// simToolsEnv is swapped by tests for a fake machine.
var simToolsEnv = simtools.Real

func newSimToolsCmd(stdout, stderr io.Writer) *cobra.Command {
	c := &cobra.Command{
		Use:   "tools",
		Short: "Check / install the external cross-check simulators (ngspice, Elmer FEM)",
		Long: `The single source of truth for the open-source simulators pcbpilot
cross-checks against. The installers (scripts/setup-agent.sh, install.sh,
install.ps1) and the setup verifier all call this command.

  ngspice     REQUIRED  sim power --spice-check, analog SPICE flow
  Elmer FEM   optional  ElmerSolver + ElmerGrid, thermal cross-check of
                        sim post-layout --elmer-check

pcbpilot's own simulators (DC power-tree MNA, trace IR/thermal estimates) are
built into this binary — nothing to install for them.

Install commands per platform:
  macOS          brew install ngspice
                 brew tap elmercsc/elmerfem && brew install elmercsc/elmerfem/elmer
                 (Elmer builds from source: 20-60+ min). No Homebrew → how-to only.
  Debian/Ubuntu  sudo apt-get install -y ngspice
  Ubuntu         sudo add-apt-repository -y ppa:elmer-csc-ubuntu/elmer-csc-ppa
                 sudo apt-get install -y elmerfem-csc   (other distros: source build)
  Fedora/RHEL    sudo dnf install -y ngspice
  Windows        winget (if an ngspice id exists) else choco install ngspice;
                 Elmer: official NSIS installer from the CSC mirror, run /S (UAC)`,
	}
	c.AddCommand(newSimToolsCheckCmd(stdout), newSimToolsInstallCmd(stdout, stderr))
	return c
}

func newSimToolsCheckCmd(stdout io.Writer) *cobra.Command {
	var asJSON bool
	var only string
	c := &cobra.Command{
		Use:   "check",
		Short: "Report path, version, minimum and status (ok/missing/outdated) of each simulator",
		Args:  cobra.NoArgs,
		Long: `Report every external simulator: resolved path, version, minimum version,
status (ok / missing / outdated) and the install command for this OS.

Exit status: 0 when every REQUIRED tool (ngspice) is ok, 1 otherwise — a missing
optional Elmer never fails the check. --json prints the same report as JSON
(schemaVersion 1) for scripts; the exit status is the same.`,
		Example: `  pcbpilot sim tools check
  pcbpilot sim tools check --json
  pcbpilot sim tools check --only elmer`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validSimTool(only); err != nil {
				return err
			}
			var names []string
			if only != "" {
				names = []string{only}
			}
			rep := simToolsEnv().Check(names...)
			if asJSON {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(rep); err != nil {
					return err
				}
			} else {
				printSimToolsReport(stdout, rep)
			}
			if !rep.OK {
				return exitCodeError{code: 1}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "print the report as JSON")
	c.Flags().StringVar(&only, "only", "", "check one tool: ngspice | elmer")
	return c
}

func printSimToolsReport(w io.Writer, rep simtools.Report) {
	pm := rep.Platform.PackageManager
	if pm == "" {
		pm = "none detected"
	}
	plat := rep.Platform.OS
	if rep.Platform.Distro != "" {
		plat += "/" + rep.Platform.Distro
	}
	fmt.Fprintf(w, "platform: %s (package manager: %s)\n", plat, pm)
	fmt.Fprintf(w, "built-in: %s\n\n", rep.Builtin)
	for _, t := range rep.Tools {
		kind := "optional"
		if t.Required {
			kind = "required"
		}
		mark := "✔"
		if t.Status != simtools.StatusOK {
			mark = "✘"
			if !t.Required {
				mark = "⚠"
			}
		}
		fmt.Fprintf(w, "%s %-8s [%s] %s", mark, t.Name, kind, t.Status)
		if t.Version != "" {
			fmt.Fprintf(w, "  v%s (min %s)", t.Version, t.MinVersion)
		} else {
			fmt.Fprintf(w, "  (min %s)", t.MinVersion)
		}
		fmt.Fprintln(w)
		for _, b := range sortedStringKeys(t.Paths) {
			fmt.Fprintf(w, "    %s: %s\n", b, t.Paths[b])
		}
		if t.Detail != "" {
			fmt.Fprintf(w, "    %s\n", t.Detail)
		}
		fmt.Fprintf(w, "    used by: %s\n", strings.Join(t.UsedBy, "; "))
		if t.Status != simtools.StatusOK {
			for _, l := range t.Install {
				fmt.Fprintf(w, "    install: %s\n", l)
			}
			if t.Manual != "" {
				fmt.Fprintf(w, "    note: %s\n", t.Manual)
			}
			if t.Warning != "" {
				fmt.Fprintf(w, "    warning: %s\n", t.Warning)
			}
		}
	}
	if !rep.OK {
		fmt.Fprintln(w, "\nrequired tool missing — run: pcbpilot sim tools install --yes")
	}
}

// simToolsOneLine is the one-line simulator status for `update --check`.
func simToolsOneLine(rep simtools.Report) string {
	var parts []string
	for _, t := range rep.Tools {
		p := t.Name + " " + t.Status
		if t.Version != "" {
			p += " v" + t.Version
		}
		if !t.Required {
			p += " (optional)"
		}
		parts = append(parts, p)
	}
	line := "  sim tools    " + strings.Join(parts, ", ")
	if !rep.OK {
		line += "  → pcbpilot sim tools install --yes"
	}
	return line
}

func sortedStringKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func validSimTool(name string) error {
	if name == "" {
		return nil
	}
	if _, ok := simtools.ToolByName(name); !ok {
		return fmt.Errorf("--only must be ngspice or elmer, got %q", name)
	}
	return nil
}

func newSimToolsInstallCmd(stdout, stderr io.Writer) *cobra.Command {
	var o simtools.InstallOptions
	c := &cobra.Command{
		Use:   "install",
		Short: "Install missing/outdated simulators with this platform's package manager",
		Args:  cobra.NoArgs,
		Long: `Install (or upgrade) every simulator that 'sim tools check' reports as
missing or outdated. Each command is printed before it runs; nothing runs without
--yes (or a "y" at the interactive prompt). Tools already ok are left alone.

Exit status is non-zero only when a REQUIRED tool is still not ok afterwards:
ngspice is required; Elmer is best-effort (a failure is a clear warning, exit 0)
because on macOS it builds from source and can take a long time.
--require-elmer makes Elmer required too.

Linux package installs need root: sudo is used when available (it may ask for
your password); in a non-interactive shell where sudo would prompt, the commands
are printed for you to run. On macOS a missing Homebrew is reported with the
official install command — pcbpilot does not install Homebrew itself.
--dry-run prints the plan and exits 0.`,
		Example: `  pcbpilot sim tools install --dry-run
  pcbpilot sim tools install --yes
  pcbpilot sim tools install --yes --only ngspice
  pcbpilot sim tools install --yes --require-elmer`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validSimTool(o.Only); err != nil {
				return err
			}
			o.Confirm = func(prompt string) bool {
				fmt.Fprint(stdout, prompt)
				return readYes()
			}
			_, err := simToolsEnv().Install(stdout, o)
			if errors.Is(err, simtools.ErrRequired) {
				fmt.Fprintln(stderr, "pcbpilot sim tools install: a required simulator is not installed (see above)")
				return exitCodeError{code: 1}
			}
			return err
		},
	}
	f := c.Flags()
	f.BoolVarP(&o.Yes, "yes", "y", false, "run the install commands without asking")
	f.BoolVar(&o.DryRun, "dry-run", false, "print the commands, run nothing")
	f.StringVar(&o.Only, "only", "", "install one tool: ngspice | elmer")
	f.BoolVar(&o.RequireElmer, "require-elmer", false, "treat Elmer as required (non-zero exit if it fails)")
	return c
}
