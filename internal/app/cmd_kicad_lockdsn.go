package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

func newKicadLockDSNCmd(stdout io.Writer) *cobra.Command {
	var dsn, ses, nets, out string
	c := &cobra.Command{
		Use:   "lock-dsn",
		Short: "Freeze the routed copper of chosen nets from a session into a DSN as fixed wiring (layered routing, experiment)",
		Long: `Reads --ses (a router session), takes the wires and vias of --nets and writes
--dsn plus that copper as (type fix) wiring to --out. A router started on --out
keeps the copper as immovable obstacles and routes only the connections left —
the first layer of layered routing (frame / critical nets first, the rest
after). Nothing is read from or written to a board.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dsn == "" || ses == "" || nets == "" || out == "" {
				return fmt.Errorf("--dsn, --ses, --nets and --out are required")
			}
			src, err := os.ReadFile(dsn)
			if err != nil {
				return err
			}
			sb, err := os.ReadFile(ses)
			if err != nil {
				return err
			}
			w, err := specctra.ParseSES(string(sb))
			if err != nil {
				return err
			}
			layers, perMil, err := kicad.DSNLayers(string(src))
			if err != nil {
				return err
			}
			res, rep, err := specctra.LockWiring(string(src), w, strings.Split(nets, ","), layers, perMil)
			if err != nil {
				return err
			}
			if rep.Segments+rep.Vias == 0 {
				return fmt.Errorf("the session has no copper on %s: nothing to lock", nets)
			}
			if err := os.WriteFile(out, []byte(res), 0o644); err != nil {
				return err
			}
			blob, _ := json.Marshal(rep)
			fmt.Fprintln(stdout, string(blob))
			return nil
		},
	}
	c.Flags().StringVar(&dsn, "dsn", "", "input Specctra DSN (required)")
	c.Flags().StringVar(&ses, "ses", "", "router session with the copper to freeze (required)")
	c.Flags().StringVar(&nets, "nets", "", "nets to freeze, comma-separated (required)")
	c.Flags().StringVar(&out, "out", "", "output DSN (required)")
	return c
}

func newKicadStageDSNCmd(stdout io.Writer) *cobra.Command {
	var dsn, nets, out string
	c := &cobra.Command{
		Use:   "stage-dsn",
		Short: "Split a DSN's net classes into a first-stage copy and the rest (layered routing, experiment)",
		Long: `Moves --nets into copies "<class>__first" of their classes (same circuit and
rule) and writes --out. Route with --router.autorouter.ignore_net_classes set to
the printed restClasses and only the first-stage nets are routed; freeze that
copper with lock-dsn and route again for the rest.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dsn == "" || nets == "" || out == "" {
				return fmt.Errorf("--dsn, --nets and --out are required")
			}
			src, err := os.ReadFile(dsn)
			if err != nil {
				return err
			}
			res, rep, err := specctra.SplitClassesForStage(string(src), strings.Split(nets, ","))
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, []byte(res), 0o644); err != nil {
				return err
			}
			blob, _ := json.Marshal(rep)
			fmt.Fprintln(stdout, string(blob))
			return nil
		},
	}
	c.Flags().StringVar(&dsn, "dsn", "", "input Specctra DSN (required)")
	c.Flags().StringVar(&nets, "nets", "", "first-stage nets, comma-separated (required)")
	c.Flags().StringVar(&out, "out", "", "output DSN (required)")
	return c
}
