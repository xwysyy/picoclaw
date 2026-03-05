package status

import (
	"github.com/spf13/cobra"
)

type statusOptions struct {
	JSON bool
}

func NewStatusCommand() *cobra.Command {
	var opts statusOptions
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"s"},
		Short:   "Show X-Claw status",
		RunE: func(_ *cobra.Command, _ []string) error {
			return statusCmd(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Output status as JSON (for scripts)")

	return cmd
}
