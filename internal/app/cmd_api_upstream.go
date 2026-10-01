package app

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/apidoc"
)

// newApiUpstreamDiffCmd compares the pinned eda.* surface (the embedded index,
// generated from the @jlceda/pro-api-types version the connector locks) with a
// newer source. Offline by default; the network is touched only with --fetch.
func newApiUpstreamDiffCmd(stdout io.Writer) *cobra.Command {
	var (
		asJSON, fetch, all, failOnBreaking bool
		registry                           string
		timeout                            time.Duration
	)
	c := &cobra.Command{
		Use:   "upstream-diff [newer-source]",
		Short: "Compare the pinned eda.* API surface with a newer pro-api-types / official doc-api source",
		Long: `Upstream API watch. Compares the pinned surface — the embedded index generated from
the @jlceda/pro-api-types version extension/package-lock.json locks — with a newer source,
and reports added / removed classes and methods plus signature and stability changes.

Every change is flagged with:
  connectorUses   the connector source references eda.<ns>.<method> directly
                  (static scan of extension/src, embedded at build time);
  namespaceInUse  the connector references the namespace at all;
  notable         a curated unlock we watch (pcb_Document.autoRouting / autoLayout,
                  sch_PrimitiveBus, sch_PrimitiveAttribute.createNetLabel, ...).
"breakingForConnector" lists used methods that were removed or changed signature.

Newer source (offline, one of):
  <file>.tgz | .tar.gz   an npm tarball of @jlceda/pro-api-types (npm pack / registry download)
  <file>.d.ts | <dir>    an index.d.ts or an unpacked package directory
  <file>.json            a gen.py api-index, or a saved official "easyeda-pro doc api" dump
                         ({"classes":[{callPath, methods:[{name,comment}]}]}; names only)
Online (opt-in): --fetch downloads @jlceda/pro-api-types@latest from the npm registry,
bounded by --timeout. Nothing else uses the network.

Types are leads, not proof: confirm on the target host with "pcbpilot api probe"
before building a typed action on a newly appeared method.`,
		Args: cobra.MaximumNArgs(1),
		Example: `  pcbpilot api upstream-diff --fetch
  pcbpilot api upstream-diff ~/Downloads/pro-api-types-0.4.26.tgz --json
  pcbpilot api upstream-diff ./doc-api-dump.json
  pcbpilot api upstream-diff --fetch --fail-on-breaking   # exit 3 when a used method broke`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fetch == (len(args) == 1) {
				return fmt.Errorf("give exactly one newer source: a path argument or --fetch")
			}
			var newer apidoc.Snapshot
			var err error
			if fetch {
				newer, err = apidoc.FetchLatest(context.Background(), registry, timeout)
			} else {
				newer, err = apidoc.LoadSnapshot(args[0])
			}
			if err != nil {
				return err
			}
			rep := apidoc.Diff(apidoc.PinnedSnapshot(), newer, apidoc.ConnectorUsage())
			if asJSON {
				if err := writeJSON(stdout, rep); err != nil {
					return err
				}
			} else {
				printUpstreamDiff(stdout, rep, all)
			}
			if failOnBreaking && len(rep.BreakingForUs) > 0 {
				return exitCodeError{3}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit the full report as JSON")
	c.Flags().BoolVar(&fetch, "fetch", false, "download @jlceda/pro-api-types@latest from the npm registry (network)")
	c.Flags().StringVar(&registry, "registry", apidoc.DefaultRegistry, "npm registry base URL for --fetch")
	c.Flags().DurationVar(&timeout, "timeout", 20*time.Second, "total network budget for --fetch")
	c.Flags().BoolVar(&all, "all", false, "text mode: list every change (default lists flagged ones and a sample)")
	c.Flags().BoolVar(&failOnBreaking, "fail-on-breaking", false, "exit 3 when a method the connector uses was removed or changed signature")
	return c
}

func printUpstreamDiff(w io.Writer, r apidoc.DiffReport, all bool) {
	ver := func(s apidoc.Snapshot) string {
		if s.Version == "" {
			return "version unknown"
		}
		return s.Version
	}
	fmt.Fprintf(w, "pinned: %s (%s, %d methods)\n", ver(r.Pinned), r.Pinned.Source, r.Pinned.MethodCount)
	fmt.Fprintf(w, "newer:  %s (%s, %s, %d methods)\n", ver(r.Newer), r.Newer.Source, r.Newer.Format, r.Newer.MethodCount)
	if r.SameVersion {
		fmt.Fprintln(w, "same version as pinned")
	}
	fmt.Fprintf(w, "classes: +%d %s  -%d %s\n", len(r.AddedClasses), strings.Join(r.AddedClasses, " "), len(r.RemovedClasses), strings.Join(r.RemovedClasses, " "))
	fmt.Fprintf(w, "methods: +%d added  -%d removed  ~%d signature  ~%d cosmetic-signature  ~%d stability\n", r.Counts.Added, r.Counts.Removed, r.Counts.Signature, r.Counts.Cosmetic, r.Counts.Stability)
	line := func(c apidoc.Change) string {
		s := fmt.Sprintf("  %-9s %s.%s", c.Kind, c.NS, c.Method)
		if c.Kind == "stability" {
			s += fmt.Sprintf(" %s → %s", strings.Join(c.Old, ","), strings.Join(c.New, ","))
		}
		var tags []string
		if c.ConnectorUses {
			tags = append(tags, "connector-uses")
		} else if c.NamespaceInUse {
			tags = append(tags, "namespace-in-use")
		}
		if c.Notable != "" {
			tags = append(tags, "notable: "+c.Notable)
		}
		if len(tags) > 0 {
			s += "  [" + strings.Join(tags, "; ") + "]"
		}
		return s
	}
	fmt.Fprintf(w, "breaking for connector: %d\n", len(r.BreakingForUs))
	for _, c := range r.BreakingForUs {
		fmt.Fprintln(w, line(c))
		for _, o := range c.Old {
			fmt.Fprintf(w, "      - %s\n", o)
		}
		for _, n := range c.New {
			fmt.Fprintf(w, "      + %s\n", n)
		}
	}
	fmt.Fprintf(w, "notable unlocks: %d\n", len(r.NotableUnlocks))
	for _, c := range r.NotableUnlocks {
		fmt.Fprintln(w, line(c))
	}
	shown := 0
	fmt.Fprintln(w, "changes:")
	for _, c := range r.Changes {
		if !all && !c.ConnectorUses && !c.NamespaceInUse && shown >= 20 {
			continue
		}
		fmt.Fprintln(w, line(c))
		shown++
	}
	if hidden := len(r.Changes) - shown; hidden > 0 {
		fmt.Fprintf(w, "  … %d more (use --all or --json)\n", hidden)
	}
	fmt.Fprintln(w, "note:", r.Note)
}
