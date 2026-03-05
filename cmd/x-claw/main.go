// X-Claw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 X-Claw contributors

package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/xwysyy/picoclaw/cmd/x-claw/internal"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/agent"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/auth"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/cron"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/gateway"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/migrate"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/onboard"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/skills"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/status"
	"github.com/xwysyy/picoclaw/cmd/x-claw/internal/version"
)

func NewXClawCommand() *cobra.Command {
	short := fmt.Sprintf("%s x-claw - Personal AI Assistant v%s\n\n", internal.Logo, internal.GetVersion())

	cmd := &cobra.Command{
		Use:     "x-claw",
		Short:   short,
		Example: "x-claw status",
	}

	cmd.AddCommand(
		onboard.NewOnboardCommand(),
		agent.NewAgentCommand(),
		auth.NewAuthCommand(),
		gateway.NewGatewayCommand(),
		status.NewStatusCommand(),
		cron.NewCronCommand(),
		migrate.NewMigrateCommand(),
		skills.NewSkillsCommand(),
		version.NewVersionCommand(),
	)

	return cmd
}

func main() {
	cmd := NewXClawCommand()
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
