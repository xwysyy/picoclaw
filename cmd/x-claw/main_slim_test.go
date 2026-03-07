package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlimCommandSurface(t *testing.T) {
	cmd := NewXClawCommand()

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--help"})

	err := cmd.Execute()
	require.NoError(t, err)

	help := out.String()

	for _, keep := range []string{"gateway", "agent", "version"} {
		assert.Contains(t, help, keep)
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
		assert.NotContains(t, help, removed)
	}
}
