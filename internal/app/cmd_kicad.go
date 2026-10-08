package app

// cmd_kicad.go — `pcbpilot kicad` parent command.
//
// Reconciled at merge: the kicad/core branch owns the real parent (bridge,
// other subcommands). This minimal version only exists so kicad/fab builds on
// its own; keep the other branch's file and add newKiCadFabCmd /
// newKiCadLcscCmd to its AddCommand list.

import (
	"io"

	"github.com/spf13/cobra"
)

func newKiCadCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	c := &cobra.Command{
		Use:   "kicad",
		Short: "KiCad design flow (JLCPCB fab output, LCSC part numbers)",
	}
	c.AddCommand(
		newKiCadFabCmd(stdout, stderr),
		newKiCadLcscCmd(stdout, stderr),
	)
	return c
}
