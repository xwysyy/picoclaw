package cron

import "github.com/spf13/cobra"

func newShowCommand(storePath func() string) *cobra.Command {
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "show <job_id>",
		Short: "Show a scheduled job (including last output and run history)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return cronShowCmd(storePath(), args[0], jsonOut)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON (for scripts)")

	return cmd
}
