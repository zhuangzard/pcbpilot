package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/apidoc"
)

// newApiCmd is the local `eda.*` API discovery surface — no daemon/connector
// needed. Index is embedded (generated from @jlceda/pro-api-types by gen.py), so
// the agent (and a developer scoping a new typed action / debug.exec_js call) can
// answer "what eda.* method do I need" without leaving the CLI.
func newApiCmd(stdout, stderr io.Writer) *cobra.Command {
	api := &cobra.Command{
		Use:   "api",
		Short: "Discover the official eda.* API surface (search / list, offline)",
		Long: func() string {
			ns, m := apidoc.Counts()
			return fmt.Sprintf(
				"Search and browse the embedded eda.* API index (%d namespaces, %d methods,\n"+
					"generated from %s). No daemon or connected window required.\n\n"+
					"This is the self-discovery loop for new typed actions / debug.exec_js calls:\n"+
					"find the eda.* method, read its signature, then wrap it.", ns, m, apidoc.Source())
		}(),
	}
	api.AddCommand(
		newApiSearchCmd(stdout),
		newApiLsCmd(stdout),
		newApiShowCmd(stdout, stderr),
	)
	return api
}

func newApiSearchCmd(stdout io.Writer) *cobra.Command {
	var asJSON bool
	var limit int
	c := &cobra.Command{
		Use:   "search <query>",
		Short: "Rank eda.* methods matching all query terms (name/namespace/summary)",
		Args:  cobra.MinimumNArgs(1),
		Example: `  pcbpilot api search dsn
  pcbpilot api search netflag create
  pcbpilot api search 自动布线 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			hits := apidoc.Search(strings.Join(args, " "), limit)
			if asJSON {
				return writeJSON(stdout, hits)
			}
			if len(hits) == 0 {
				fmt.Fprintln(stdout, "no matches")
				return nil
			}
			for _, m := range hits {
				printMethod(stdout, m)
			}
			fmt.Fprintf(stdout, "\n%d match(es)\n", len(hits))
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit results as JSON")
	c.Flags().IntVar(&limit, "limit", 30, "max results (0 = no cap)")
	return c
}

func newApiLsCmd(stdout io.Writer) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "ls [namespace-filter]",
		Short: "List eda.* namespaces (optionally filtered by substring)",
		Args:  cobra.MaximumNArgs(1),
		Example: `  pcbpilot api ls
  pcbpilot api ls pcb
  pcbpilot api ls sch --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			filter := ""
			if len(args) == 1 {
				filter = args[0]
			}
			names := apidoc.Namespaces(filter)
			if asJSON {
				return writeJSON(stdout, names)
			}
			for _, ns := range names {
				fmt.Fprintf(stdout, "%s  (%d)\n", ns, len(apidoc.MethodsIn(ns)))
			}
			fmt.Fprintf(stdout, "\n%d namespace(s)\n", len(names))
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit namespaces as JSON")
	return c
}

func newApiShowCmd(stdout, stderr io.Writer) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "show <namespace>",
		Short: "List all methods of one eda.* namespace",
		Args:  cobra.ExactArgs(1),
		Example: `  pcbpilot api show eda.sch_PrimitiveComponent
  pcbpilot api show pcb_ManufactureData`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := args[0]
			if !strings.HasPrefix(ns, "eda.") {
				ns = "eda." + ns
			}
			methods := apidoc.MethodsIn(ns)
			if asJSON {
				return writeJSON(stdout, methods)
			}
			if len(methods) == 0 {
				fmt.Fprintf(stderr, "no namespace %q (try `pcbpilot api ls`)\n", ns)
				return errActionFailed
			}
			fmt.Fprintf(stdout, "%s — %d method(s)\n\n", ns, len(methods))
			for _, m := range methods {
				printMethod(stdout, m)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit methods as JSON")
	return c
}

func printMethod(w io.Writer, m apidoc.Method) {
	stab := ""
	if m.Stability != "" {
		stab = " @" + m.Stability
	}
	fmt.Fprintf(w, "%s.%s%s\n", m.NS, m.Method, stab)
	if m.Summary != "" {
		fmt.Fprintf(w, "    %s\n", m.Summary)
	}
	if m.Sig != "" {
		fmt.Fprintf(w, "    %s\n", strings.TrimSuffix(m.Sig, ";"))
	}
}
