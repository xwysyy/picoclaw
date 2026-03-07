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

	"github.com/xwysyy/X-Claw/cmd/x-claw/internal"
	"github.com/xwysyy/X-Claw/cmd/x-claw/internal/agent"
	"github.com/xwysyy/X-Claw/cmd/x-claw/internal/gateway"
	"github.com/xwysyy/X-Claw/cmd/x-claw/internal/version"
)

func slimRootCommands() []*cobra.Command {
	return []*cobra.Command{
		agent.NewAgentCommand(),
		gateway.NewGatewayCommand(),
		version.NewVersionCommand(),
	}
}

func NewXClawCommand() *cobra.Command {
	short := fmt.Sprintf("%s x-claw - Personal AI Assistant v%s\n\n", internal.Logo, internal.GetVersion())

	cmd := &cobra.Command{
		Use:     "x-claw",
		Short:   short,
		Example: "x-claw gateway",
	}

	cmd.AddCommand(slimRootCommands()...)

	return cmd
}

func main() {
	cmd := NewXClawCommand()
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
