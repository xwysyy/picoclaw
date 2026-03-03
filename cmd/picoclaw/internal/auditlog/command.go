package auditlog

import "github.com/spf13/cobra"

func NewAuditLogCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auditlog",
		Short: "Inspect and verify the append-only audit log",
	}

	cmd.AddCommand(newVerifyCommand())

	return cmd
}
