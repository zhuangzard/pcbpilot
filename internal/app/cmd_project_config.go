package app

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/console"
	"github.com/zhuangzard/pcbpilot/pkg/projectconfig"
)

// newProjectConfigCmd is `pcbpilot project-config`: the per-project process
// template (pcbpilot.project.json) the Skill reads before planning a run.
func newProjectConfigCmd(stdout, stderr io.Writer) *cobra.Command {
	var dir string
	pc := &cobra.Command{
		Use:   "project-config",
		Short: "Per-project process template: which steps/sims/report sections run, plus constraints (pcbpilot.project.json)",
		Long: `pcbpilot.project.json lives in the project work dir. It records which design-flow steps
(S0–S6.5, P0–P11), simulations and report sections this project runs or skips, and the
constraint sets (standards, requirement files, mechanical file, fab profile, preferred/banned
parts). Agents read it first and must say in the report which steps were skipped and why.

It is a planning document: it never authorises an EDA write, and guard rules keep the check
that follows a write (S5 after S4, P10 after routing, P6 before P7). Required report
sections (0, 1, 7, 11) cannot be skipped.`,
	}
	pc.PersistentFlags().StringVar(&dir, "dir", ".", "project work dir")

	var name, template, edaProject string
	var noRegister bool
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Create pcbpilot.project.json from a template and register the dir with the console",
		Example: `  pcbpilot project-config init --template quick-proto --name "ESP32 mini" --eda-project ceshi
  pcbpilot project-config show --catalog`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := projectconfig.Load(dir); err == nil {
				return fmt.Errorf("%s already exists in %s — use `project-config set`", projectconfig.FileName, dir)
			} else if !errors.Is(err, projectconfig.ErrNotFound) {
				return err
			}
			c, err := projectconfig.New(name, template)
			if err != nil {
				return err
			}
			c.EDA.Project = edaProject
			if err := projectconfig.Save(dir, c, "cli"); err != nil {
				return err
			}
			if !noRegister {
				if _, err := console.RegisterWorkDir(console.Home(), dir, name, "cli"); err != nil {
					fmt.Fprintf(stderr, "warning: could not register the work dir with the console: %v\n", err)
				}
			}
			return writeJSON(stdout, map[string]any{"path": projectconfig.Path(dir), "config": c, "issues": c.Validate()})
		},
	}
	initCmd.Flags().StringVar(&template, "template", "full", "template: "+strings.Join(projectconfig.TemplateIDs(), " | "))
	initCmd.Flags().StringVar(&name, "name", "", "project display name")
	initCmd.Flags().StringVar(&edaProject, "eda-project", "", "EasyEDA project name or uuid this dir belongs to")
	initCmd.Flags().BoolVar(&noRegister, "no-register", false, "do not add the dir to the console's work-dir list")

	var catalog bool
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the config (JSON) with validation issues, skipped steps and sections",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if catalog {
				return writeJSON(stdout, map[string]any{"templates": projectconfig.Templates, "steps": projectconfig.Steps,
					"sims": projectconfig.Sims, "sections": projectconfig.Sections})
			}
			c, err := projectconfig.Load(dir)
			if err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{"path": projectconfig.Path(dir), "config": c, "issues": c.Validate(),
				"skippedSteps": c.SkippedSteps(), "skippedSections": c.SkippedSections()})
		},
	}
	show.Flags().BoolVar(&catalog, "catalog", false, "print the template/step/sim/section catalog instead")

	var steps, sims, sections, addStd, rmStd, addReq, rmReq, prefer, ban, unprefer, unban []string
	var reason, mech, fab, notes string
	var layers int
	set := &cobra.Command{
		Use:   "set",
		Short: "Change toggles and constraints (validated before writing)",
		Example: `  pcbpilot project-config set --step P10.5=off --sim postLayout=off --section 6A=off --reason "客户只要打样，不做设计后仿真"
  pcbpilot project-config set --add-standard IPC-2221B --add-requirement resources/requirement/spec.md
  pcbpilot project-config set --fab jlcpcb --layers 4 --prefer C6186 --ban C123456`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := projectconfig.Load(dir)
			if err != nil {
				return err
			}
			apply := func(m map[string]projectconfig.Toggle, specs []string, what string) error {
				for _, s := range specs {
					id, val, ok := strings.Cut(s, "=")
					if !ok {
						return fmt.Errorf("--%s wants ID=on|off, got %q", what, s)
					}
					on, err := parseOnOff(val)
					if err != nil {
						return fmt.Errorf("--%s %s: %w", what, s, err)
					}
					if _, known := m[id]; !known {
						return fmt.Errorf("unknown %s id %q (see `project-config show --catalog`)", what, id)
					}
					t := projectconfig.Toggle{Enabled: on}
					if !on {
						t.Reason = reason
					}
					m[id] = t
				}
				return nil
			}
			if err := apply(c.Steps, steps, "step"); err != nil {
				return err
			}
			if err := apply(c.Sims, sims, "sim"); err != nil {
				return err
			}
			if err := apply(c.Report, sections, "section"); err != nil {
				return err
			}
			k := &c.Constraints
			k.Standards = editList(k.Standards, addStd, rmStd)
			k.Requirements = editList(k.Requirements, addReq, rmReq)
			k.Parts.Preferred = editList(k.Parts.Preferred, prefer, unprefer)
			k.Parts.Banned = editList(k.Parts.Banned, ban, unban)
			if cmd.Flags().Changed("mech") {
				k.Mech = mech
			}
			if cmd.Flags().Changed("fab") {
				k.Fab.Profile = fab
			}
			if cmd.Flags().Changed("layers") {
				k.Fab.Layers = layers
			}
			if cmd.Flags().Changed("notes") {
				k.Notes = notes
			}
			if cmd.Flags().Changed("name") {
				c.Name = name
			}
			if cmd.Flags().Changed("eda-project") {
				c.EDA.Project = edaProject
			}
			if err := projectconfig.Save(dir, c, "cli"); err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{"config": c, "issues": c.Validate(), "skippedSteps": c.SkippedSteps()})
		},
	}
	f := set.Flags()
	f.StringArrayVar(&steps, "step", nil, "step toggle ID=on|off (repeatable), e.g. P10.5=off")
	f.StringArrayVar(&sims, "sim", nil, "simulation toggle ID=on|off: power analog monteCarlo postLayout elmer")
	f.StringArrayVar(&sections, "section", nil, "report section toggle ID=on|off, e.g. 6A=off")
	f.StringVar(&reason, "reason", "", "why the toggled-off items are skipped (reports quote it)")
	f.StringArrayVar(&addStd, "add-standard", nil, "add a standard (e.g. IPC-2221B)")
	f.StringArrayVar(&rmStd, "remove-standard", nil, "remove a standard")
	f.StringArrayVar(&addReq, "add-requirement", nil, "add a requirement file (path relative to the work dir)")
	f.StringArrayVar(&rmReq, "remove-requirement", nil, "remove a requirement file")
	f.StringArrayVar(&prefer, "prefer", nil, "add a preferred part (LCSC C-number or MPN)")
	f.StringArrayVar(&unprefer, "unprefer", nil, "remove a preferred part")
	f.StringArrayVar(&ban, "ban", nil, "add a banned part")
	f.StringArrayVar(&unban, "unban", nil, "remove a banned part")
	f.StringVar(&mech, "mech", "", "mechanical spec file (relative path, e.g. mech.json)")
	f.StringVar(&fab, "fab", "", "fab profile (e.g. jlcpcb)")
	f.IntVar(&layers, "layers", 0, "layer count")
	f.StringVar(&notes, "notes", "", "free-form constraint notes")
	f.StringVar(&name, "name", "", "project display name")
	f.StringVar(&edaProject, "eda-project", "", "EasyEDA project name or uuid")

	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate the config; exit 1 on any error-level issue",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := projectconfig.Load(dir)
			if err != nil {
				return err
			}
			issues := c.Validate()
			tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "SEVERITY\tFIELD\tMESSAGE")
			for _, i := range issues {
				fmt.Fprintf(tw, "%s\t%s\t%s\n", i.Severity, i.Field, i.Message)
			}
			_ = tw.Flush()
			if len(issues) == 0 {
				fmt.Fprintln(stdout, "ok: no issues")
			}
			if projectconfig.HasErrors(issues) {
				return errQuiet
			}
			return nil
		},
	}
	pc.AddCommand(initCmd, show, set, validate)
	return pc
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "on", "true", "1", "yes", "enable", "enabled":
		return true, nil
	case "off", "false", "0", "no", "disable", "disabled", "skip":
		return false, nil
	}
	return false, fmt.Errorf("want on|off, got %q", s)
}

func editList(list, add, remove []string) []string {
	for _, a := range add {
		dup := false
		for _, v := range list {
			if strings.EqualFold(v, a) {
				dup = true
			}
		}
		if !dup && strings.TrimSpace(a) != "" {
			list = append(list, strings.TrimSpace(a))
		}
	}
	var out []string
	for _, v := range list {
		drop := false
		for _, r := range remove {
			if strings.EqualFold(v, r) {
				drop = true
			}
		}
		if !drop {
			out = append(out, v)
		}
	}
	return out
}
