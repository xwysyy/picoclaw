package security

import "github.com/spf13/cobra"

type securityOptions struct {
	Check bool
	JSON  bool
}

func NewSecurityCommand() *cobra.Command {
	var opts securityOptions
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Show security posture (sandbox/limits/audit/estop)",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return securityCmd(opts)
		},
	}

	cmd.Flags().BoolVar(&opts.Check, "check", true, "Run security self-check")
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "Output report as JSON (for scripts)")

	return cmd
}
