package app

// cmd_kicad.go — `pcbpilot kicad`: design in KiCad (user decision
// 2026-10-09; EasyEDA only receives the finished project via project import).

import (
	"io"

	"github.com/spf13/cobra"
)

func newKicadCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	c := &cobra.Command{
		Use:   "kicad",
		Short: "Design in KiCad: placement, schematic sheets, schematic import/netlist, JLC fab output, LCSC parts",
	}
	c.AddCommand(
		newKicadPlaceCmd(stdout, stderr),
		newKicadSchFitCmd(stdout, stderr),
		newKiCadFabCmd(stdout, stderr),
		newKiCadLcscCmd(stdout, stderr),
		newKicadSchImportCmd(stdout, stderr),
		newKicadSchNetlistCmd(stdout, stderr),
		newKicadSchCheckCmd(stdout, stderr),
	)
	return c
}
