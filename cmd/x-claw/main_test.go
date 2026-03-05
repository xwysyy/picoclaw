package main

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xwysyy/picoclaw/cmd/x-claw/internal"
)

func TestNewXClawCommand(t *testing.T) {
	cmd := NewXClawCommand()

	require.NotNil(t, cmd)

	short := fmt.Sprintf("%s x-claw - Personal AI Assistant v%s\n\n", internal.Logo, internal.GetVersion())

	assert.Equal(t, "x-claw", cmd.Use)
	assert.Equal(t, short, cmd.Short)

	assert.True(t, cmd.HasSubCommands())
	assert.True(t, cmd.HasAvailableSubCommands())

	assert.False(t, cmd.HasFlags())

	assert.Nil(t, cmd.Run)
	assert.Nil(t, cmd.RunE)

	assert.Nil(t, cmd.PersistentPreRun)
	assert.Nil(t, cmd.PersistentPostRun)

	allowedCommands := []string{
		"agent",
		"auditlog",
		"auth",
		"config",
		"cron",
		"doctor",
		"estop",
		"export",
		"gateway",
		"migrate",
		"onboard",
		"security",
		"skills",
		"status",
		"version",
	}

	subcommands := cmd.Commands()
	assert.Len(t, subcommands, len(allowedCommands))

	for _, subcmd := range subcommands {
		found := slices.Contains(allowedCommands, subcmd.Name())
		assert.True(t, found, "unexpected subcommand %q", subcmd.Name())

		assert.False(t, subcmd.Hidden)
	}
}
