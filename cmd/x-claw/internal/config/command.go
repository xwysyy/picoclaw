package config

import (
	"github.com/spf13/cobra"
)

type validateOptions struct {
	Path string
	JSON bool
}

// NewConfigCommand provides configuration utilities (validate, ...).
func NewConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Config utilities",
	}

	cmd.AddCommand(newValidateCommand())
	return cmd
}

func newValidateCommand() *cobra.Command {
	var opts validateOptions

	cmd := &cobra.Command{
		Use:           "validate",
		Short:         "Validate the config file and report problems with JSON paths",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			return validateCmd(opts)
		},
	}

	cmd.Flags().StringVar(&opts.Path, "config", "", "Config file path (default: $X_CLAW_CONFIG or ~/.x-claw/config.json)")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Output validation report as JSON")

	return cmd
}
