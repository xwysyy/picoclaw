package estop

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xwysyy/X-Claw/cmd/x-claw/internal"
	"github.com/xwysyy/X-Claw/pkg/tools"
)

func NewEstopCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "estop",
		Short: "Manage the global tool kill switch (estop)",
	}

	cmd.AddCommand(
		newEstopStatusCommand(),
		newEstopSetModeCommand("off", tools.EstopModeOff),
		newEstopSetModeCommand("kill_all", tools.EstopModeKillAll),
		newEstopSetModeCommand("network_kill", tools.EstopModeNetworkKill),
	)

	return cmd
}

func newEstopStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current estop state",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := internal.LoadConfig()
			if err != nil {
				return err
			}
			workspace := cfg.WorkspacePath()
			st, err := tools.LoadEstopState(workspace)
			if err != nil && cfg.Tools.Estop.FailClosed {
				st = tools.EstopState{Mode: tools.EstopModeKillAll, Note: "fail-closed: " + err.Error()}.Normalized()
				err = nil
			}
			if err != nil {
				return err
			}

			data, _ := json.MarshalIndent(st, "", "  ")
			fmt.Println(string(data))
			return nil
		},
	}
}

func newEstopSetModeCommand(name string, mode tools.EstopMode) *cobra.Command {
	var note string
	c := &cobra.Command{
		Use:   name,
		Short: "Set estop mode to " + name,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := internal.LoadConfig()
			if err != nil {
				return err
			}
			workspace := cfg.WorkspacePath()

			st, _ := tools.LoadEstopState(workspace)
			st.Mode = mode
			st.Note = strings.TrimSpace(note)

			saved, err := tools.SaveEstopState(workspace, st)
			if err != nil {
				return err
			}
			data, _ := json.MarshalIndent(saved, "", "  ")
			fmt.Println(string(data))
			return nil
		},
	}
	c.Flags().StringVar(&note, "note", "", "Optional note saved in estop state")
	return c
}
