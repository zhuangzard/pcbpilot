package app

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// newProjectCmd returns the "project" subcommand group.
func newProjectCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	var window string

	proj := &cobra.Command{
		Use:   "project",
		Short: "Read EasyEDA project and document context",
	}
	proj.PersistentFlags().StringVar(&window, "window", "", "EasyEDA window ID")

	proj.AddCommand(
		func() *cobra.Command {
			var friendlyName, projectName, teamUUID, folderUUID, description string
			var open bool
			c := &cobra.Command{
				Use:     "create",
				Short:   "Create an EasyEDA project container through the official API",
				Args:    cobra.NoArgs,
				Example: `  pcbpilot project create --name "AT32F415 demo" --open`,
				RunE: func(cmd *cobra.Command, args []string) error {
					if friendlyName == "" {
						return fmt.Errorf("--name is required")
					}
					payload := map[string]any{"friendlyName": friendlyName, "open": open}
					if projectName != "" {
						payload["projectName"] = projectName
					}
					if teamUUID != "" {
						payload["teamUuid"] = teamUUID
					}
					if folderUUID != "" {
						payload["folderUuid"] = folderUUID
					}
					if description != "" {
						payload["description"] = description
					}
					return dispatch(cfg, "project.create", window, payload, stdout, stderr)
				},
			}
			c.Flags().StringVar(&friendlyName, "name", "", "project display name (required)")
			c.Flags().StringVar(&projectName, "internal-name", "", "optional internal project name")
			c.Flags().StringVar(&teamUUID, "team", "", "target team/workspace UUID")
			c.Flags().StringVar(&folderUUID, "folder", "", "target folder UUID")
			c.Flags().StringVar(&description, "description", "", "project description")
			c.Flags().BoolVar(&open, "open", false, "open the newly created project")
			return c
		}(),

		// project info → project.current
		&cobra.Command{
			Use:   "info",
			Short: "Read current EasyEDA project information",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return dispatch(cfg, "project.current", window, nil, stdout, stderr)
			},
		},

		// project doc → document.current
		&cobra.Command{
			Use:   "doc",
			Short: "Read active editor document and schematic page context",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				return dispatch(cfg, "document.current", window, nil, stdout, stderr)
			},
		},

		// project open --uuid <uuid> → document.open
		func() *cobra.Command {
			var uuid string
			c := &cobra.Command{
				Use:     "open",
				Short:   "Open a document (schematic page or PCB) by UUID",
				Args:    cobra.NoArgs,
				Example: `  pcbpilot project open --uuid 6b3a2f01-...`,
				RunE: func(cmd *cobra.Command, args []string) error {
					if uuid == "" {
						return fmt.Errorf("--uuid is required")
					}
					return dispatch(cfg, "document.open", window,
						map[string]any{"uuid": uuid}, stdout, stderr)
				},
			}
			c.Flags().StringVar(&uuid, "uuid", "", "document UUID to open (required)")
			return c
		}(),
	)

	return proj
}
