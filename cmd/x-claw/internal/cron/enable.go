package cron

import "github.com/spf13/cobra"

func newEnableCommand(storePath func() string) *cobra.Command {
	return newToggleCommand(storePath, "enable", "Enable a job", true)
}

func newDisableCommand(storePath func() string) *cobra.Command {
	return newToggleCommand(storePath, "disable", "Disable a job", false)
}

func newToggleCommand(storePath func() string, use, short string, enabled bool) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Short:   short,
		Args:    cobra.ExactArgs(1),
		Example: "x-claw cron " + use + " 1",
		RunE: func(_ *cobra.Command, args []string) error {
			cronSetJobEnabled(storePath(), args[0], enabled)
			return nil
		},
	}
}
