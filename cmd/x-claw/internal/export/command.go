package export

import (
	"fmt"

	"github.com/spf13/cobra"
)

func NewExportCommand() *cobra.Command {
	var (
		sessionKey    string
		lastActive    bool
		outPath       string
		includeCron   bool
		includeTrace  bool
		includeState  bool
		includeConfig bool
	)

	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export a replay bundle (session + traces) for debugging",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			opts := ExportOptions{
				SessionKey:    sessionKey,
				UseLastActive: lastActive,
				OutPath:       outPath,
				IncludeCron:   includeCron,
				IncludeTrace:  includeTrace,
				IncludeState:  includeState,
				IncludeConfig: includeConfig,
			}
			result, err := RunExport(opts)
			if err != nil {
				return err
			}
			fmt.Printf("✓ Exported bundle: %s\n", result.OutputPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&sessionKey, "session", "s", "", "Session key to export (e.g. agent:main:feishu:group:oc_xxx)")
	cmd.Flags().BoolVar(&lastActive, "last-active", false, "Export the session matching workspace last_active (state/state.json)")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "Output .zip path (default: <workspace>/exports/...)")
	cmd.Flags().BoolVar(&includeTrace, "trace", true, "Include tool trace files for this session")
	cmd.Flags().BoolVar(&includeCron, "cron", true, "Include cron/jobs.json")
	cmd.Flags().BoolVar(&includeState, "state", true, "Include state/state.json (last_active)")
	cmd.Flags().BoolVar(&includeConfig, "config", true, "Include redacted config.json snapshot")

	cmd.MarkFlagsMutuallyExclusive("session", "last-active")

	return cmd
}
