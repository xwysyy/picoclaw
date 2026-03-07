package gateway

import (
	"github.com/spf13/cobra"

	"github.com/xwysyy/X-Claw/internal/app"
)

func NewGatewayCommand() *cobra.Command {
	var debug bool

	cmd := &cobra.Command{
		Use:     "gateway",
		Aliases: []string{"g"},
		Short:   "Start X-Claw gateway",
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return app.RunGateway(app.GatewayOptions{Debug: debug})
		},
	}

	cmd.Flags().BoolVarP(&debug, "debug", "d", false, "Enable debug logging")

	return cmd
}
