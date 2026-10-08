// MINIMAL stand-in — replaced by kicad/core at merge (its `pcbpilot kicad`
// parent). kicad/core's parent must add newKicadPlaceCmd.

package app

import (
	"io"

	"github.com/spf13/cobra"
)

func newKicadCmd(stdout, stderr io.Writer) *cobra.Command {
	c := &cobra.Command{
		Use:   "kicad",
		Short: "KiCad boards through KiCad's own pcbnew python (offline)",
	}
	c.AddCommand(newKicadPlaceCmd(stdout, stderr))
	return c
}
