package main

import (
	"bytes"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlimCommandSurface(t *testing.T) {
	cmd := NewXClawCommand()
	allowedCommands := []string{"agent", "gateway", "version"}
	for _, subcmd := range cmd.Commands() {
		assert.True(t, slices.Contains(allowedCommands, subcmd.Name()), "unexpected subcommand %q", subcmd.Name())
	}
	assert.Len(t, cmd.Commands(), len(allowedCommands))

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})

	err := cmd.Execute()
	require.NoError(t, err)

	output := out.String()
	for _, keep := range allowedCommands {
		assert.Contains(t, output, keep)
	}
	for _, removed := range []string{
		"auditlog",
		"auth",
		"config",
		"cron",
		"doctor",
		"estop",
		"export",
		"migrate",
		"onboard",
		"security",
		"skills",
		"status",
	} {
		assert.NotContains(t, output, removed)
	}
}
